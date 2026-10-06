package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

func failedRead(cause aicontext.Cause, finished time.Time) *aiContextDiagnosis {
	return &aiContextDiagnosis{outcome: aicontext.OutcomeFailed, diagnosis: &aicontext.Diagnosis{Cause: cause}, finished: finished}
}

// How long Zoomies waits, and how often it tries, is a decision about money and
// patience rather than about code, so each row says what it is protecting.
func TestRunDueWaitsAsLongAsTheCauseNeeds(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	tests := []struct {
		name  string
		run   aiContextRun
		known *aiContextDiagnosis
		grace time.Duration
		want  bool
	}{
		{
			// A run in progress is the one thing that must never be started over:
			// the concurrency group cancels it, and with it the work it has done.
			name:  "a run still working is left alone however long it has taken",
			run:   aiContextRun{since: ago(48 * time.Hour)},
			known: &aiContextDiagnosis{outcome: aicontext.OutcomeRunning},
			want:  false,
		},
		{
			// GitHub recalculates storage every six to twelve hours; an hour-old
			// failure would only fail again.
			name:  "a full quota is not retried within GitHub's recalculation window",
			run:   aiContextRun{since: ago(2 * time.Hour)},
			known: failedRead(aicontext.CauseArtifactQuota, ago(5*time.Hour+59*time.Minute)),
			want:  false,
		},
		{
			name:  "a full quota is retried once the window has passed",
			run:   aiContextRun{since: ago(8 * time.Hour)},
			known: failedRead(aicontext.CauseArtifactQuota, ago(6*time.Hour)),
			want:  true,
		},
		{
			name:  "a full quota is tried three times and then left to a person",
			run:   aiContextRun{since: ago(40 * time.Hour), attempts: 3, lastStart: ago(7 * time.Hour)},
			known: failedRead(aicontext.CauseArtifactQuota, ago(7*time.Hour)),
			want:  false,
		},
		{
			name:  "a second quota attempt waits the window from the first, not from the failure",
			run:   aiContextRun{since: ago(12 * time.Hour), attempts: 1, lastStart: ago(3 * time.Hour)},
			known: failedRead(aicontext.CauseArtifactQuota, ago(8*time.Hour)),
			want:  false,
		},
		{
			// The point of the change: the old rule would have started this one
			// after the grace and again half an hour later, for ever the same.
			name:  "an oversized file is never retried",
			run:   aiContextRun{since: ago(72 * time.Hour)},
			known: failedRead(aicontext.CauseOversizedFile, ago(72*time.Hour)),
			want:  false,
		},
		{
			name:  "a runner GitHub failed to give is retried after half an hour",
			run:   aiContextRun{since: ago(time.Hour)},
			known: failedRead(aicontext.CauseRunnerUnavailable, ago(31*time.Minute)),
			want:  true,
		},
		{
			// A cause Zoomies has read replaces the grace it would otherwise sit
			// out: the grace is for a push-triggered run nobody has looked at.
			name:  "a diagnosed failure ignores the grace period",
			run:   aiContextRun{since: ago(time.Minute)},
			known: failedRead(aicontext.CauseRunnerUnavailable, ago(40*time.Minute)),
			grace: time.Hour,
			want:  true,
		},
		{
			name:  "a runner failure gets four attempts, not two",
			run:   aiContextRun{since: ago(4 * time.Hour), attempts: 3, lastStart: ago(31 * time.Minute)},
			known: failedRead(aicontext.CauseRunnerUnavailable, ago(31*time.Minute)),
			want:  true,
		},
		{
			name:  "nothing known keeps the original rule: wait out the grace",
			run:   aiContextRun{since: ago(10 * time.Minute)},
			grace: 30 * time.Minute,
			want:  false,
		},
		{
			name:  "nothing known keeps the original rule: then start it",
			run:   aiContextRun{since: ago(31 * time.Minute)},
			grace: 30 * time.Minute,
			want:  true,
		},
		{
			name: "nothing known keeps the original rule: twice at most",
			run:  aiContextRun{since: ago(5 * time.Hour), attempts: aiContextRunAttempts, lastStart: ago(2 * time.Hour)},
			want: false,
		},
		{
			// The run succeeded and the context is still behind, so the run is
			// not the explanation; fall back to waiting for publication.
			name:  "a successful run gives no reason to start another before the grace",
			run:   aiContextRun{since: ago(time.Minute)},
			known: &aiContextDiagnosis{outcome: aicontext.OutcomeSucceeded},
			grace: time.Hour,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aiContextRunDue(now, tt.grace, &tt.run, tt.known); got != tt.want {
				t.Errorf("due = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryPlanSaysWhenAndHowOftenZoomiesWillTryAgain(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	c := &Controller{clock: func() time.Time { return now }, aiContextRuns: map[string]*aiContextRun{}}
	known := failedRead(aicontext.CauseArtifactQuota, now.Add(-time.Hour))
	known.commit = "abc"

	plan := c.aiContextRetryPlanLocked("r", known)
	if !plan.Automatic || plan.AttemptsLeft != 3 || plan.NextAt == nil || !plan.NextAt.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("plan = %+v, want three attempts, the first at 17:00", plan)
	}

	// Having started it, the wait counts from then, and the attempt is used.
	c.aiContextRuns["r"] = &aiContextRun{commit: "abc", attempts: 1, lastStart: now.Add(-time.Minute)}
	plan = c.aiContextRetryPlanLocked("r", known)
	if plan.AttemptsLeft != 2 || !plan.NextAt.Equal(now.Add(6*time.Hour-time.Minute)) {
		t.Fatalf("after one attempt: %+v", plan)
	}

	// Attempts for another commit are not this commit's.
	c.aiContextRuns["r"] = &aiContextRun{commit: "other", attempts: 3, lastStart: now}
	if plan = c.aiContextRetryPlanLocked("r", known); plan.AttemptsLeft != 3 {
		t.Errorf("another commit's attempts were counted: %+v", plan)
	}

	// Used up, or never going to happen: no time is promised.
	c.aiContextRuns["r"] = &aiContextRun{commit: "abc", attempts: 3}
	if plan = c.aiContextRetryPlanLocked("r", known); plan.Automatic || plan.NextAt != nil {
		t.Errorf("exhausted plan promises a retry: %+v", plan)
	}
	stuck := failedRead(aicontext.CauseOversizedFile, now)
	stuck.commit = "abc"
	if plan = c.aiContextRetryPlanLocked("r", stuck); plan.Automatic || plan.NextAt != nil || plan.AttemptsLeft != 0 {
		t.Errorf("a cause no run can fix promises a retry: %+v", plan)
	}

	// A time already gone means the next pass.
	old := failedRead(aicontext.CauseRunnerUnavailable, now.Add(-5*time.Hour))
	old.commit = "abc"
	c.aiContextRuns["r"] = nil
	if plan = c.aiContextRetryPlanLocked("r", old); plan.NextAt == nil || plan.NextAt.Before(now) {
		t.Errorf("next attempt in the past: %+v", plan)
	}
}

func TestEveryCauseHasAProblemCodeWithItsOwnSentence(t *testing.T) {
	seen := map[string]aicontext.Cause{}
	for _, cause := range aicontext.Causes {
		shape, ok := aiContextProblemShapes[cause]
		if !ok || shape.Code == "" || !strings.HasPrefix(shape.Code, "ai_context.") {
			t.Errorf("%s has no problem code: %+v", cause, shape)
			continue
		}
		if other, dup := seen[shape.Code]; dup {
			t.Errorf("%s and %s share the code %s", cause, other, shape.Code)
		}
		seen[shape.Code] = cause
		if audienceFor(shape.Code) != AudienceFleet {
			t.Errorf("%s: an AI Context failure is the fleet's to act on, got %q", shape.Code, audienceFor(shape.Code))
		}
	}
}

func TestRetrySentenceNeverLeavesTheReaderGuessing(t *testing.T) {
	next := time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		d    aicontext.Diagnosis
		want string
	}{
		{"will retry", aicontext.Diagnosis{Cause: aicontext.CauseArtifactQuota, Retry: &aicontext.RetryPlan{Automatic: true, NextAt: &next, AttemptsLeft: 2}}, "from 6 Oct 18:00 UTC (2 attempts left"},
		{"will not", aicontext.Diagnosis{Cause: aicontext.CauseOversizedFile, Retry: &aicontext.RetryPlan{}}, "will not start the workflow again until this is fixed"},
		{"gave up", aicontext.Diagnosis{Cause: aicontext.CauseArtifactQuota, Retry: &aicontext.RetryPlan{}}, "as often as it will"},
	}
	for _, tt := range tests {
		if got := aiContextRetrySentence(&tt.d); !strings.Contains(got, tt.want) {
			t.Errorf("%s: %q does not contain %q", tt.name, got, tt.want)
		}
	}
}
