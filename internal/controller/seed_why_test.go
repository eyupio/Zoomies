package controller

import (
	"fmt"
	"slices"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// seededWithTrouble is the demo fleet with the diagnostics fixture on top, after
// the scheduler has had its first pass over it, which is the state an operator
// opening `zoomies demo` with the fixture on finds.
func seededWithTrouble(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	t.Setenv(StuckSeedEnvVar, "1")
	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}
	if err := h.c.SeedStuck(h.ctx); err != nil {
		t.Fatalf("SeedStuck: %v", err)
	}
	// The blocked job's reason is the scheduler's own, so it only exists once the
	// scheduler has looked.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return h
}

// The demo is where somebody first meets "why", and a demo that answers "unknown"
// for any of its own jobs teaches them the feature gives up. Every job it carries
// is diagnosed, and the shapes a fleet in trouble has are each there to be asked
// about: if one of these classes stops being seeded, the first thing a new
// operator tries on it has nothing to find.
func TestEveryJobInTheDemoFleetIsDiagnosed(t *testing.T) {
	h := seededWithTrouble(t)

	jobs, _, err := h.st.ListJobs(h.ctx, store.JobFilter{}, store.Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	seen := map[JobClass][]string{}
	for _, j := range jobs {
		got := h.explain(t, j)
		if got.Class == ClassUnknown {
			t.Errorf("job %s (%s, %s) is classed unknown: %s", j.ID, j.State, j.Conclusion, got.ConfidenceReason)
		}
		seen[got.Class] = append(seen[got.Class], j.ID)
	}
	for _, want := range []JobClass{
		ClassSucceeded, ClassWorkflowFailure, ClassCancelled, ClassTimeout, ClassOOM,
		ClassRunning, ClassQueuedCapacity, ClassQueuedUnmatched, ClassQueuedBlocked, ClassHeldByGitHub,
	} {
		if len(seen[want]) == 0 {
			t.Errorf("no job in the demo fleet is classed %q, so there is nothing to try that class on; classes seen: %v", want, seen)
		}
	}
}

// The failures a fleet has to be able to show are each diagnosed with the
// confidence a recorded cause earns, and with the catalog entry that is true of
// the class and no other.
func TestEachFailureTheDemoSeedsIsNamedWithHighConfidence(t *testing.T) {
	h := seededWithTrouble(t)

	cases := []struct {
		name  string
		id    string
		class JobClass
		// problem is the catalog entry the explanation must point at, "" for none.
		problem string
		// kind is an evidence kind the explanation must carry.
		kind string
	}{
		{"a runner the kernel killed for memory", "job_demo043", ClassOOM, "jobs.oom_killed", EvidenceFault},
		{"a job GitHub stopped at its time limit", fmt.Sprintf("job_demo%03d", demoTimedOutJob), ClassTimeout, "", EvidenceStep},
		{"a job no pool claims", "job_demo049", ClassQueuedUnmatched, "jobs.unmatched", EvidenceLabels},
		{"a job whose pool no host can serve", stuckBlockedJobID, ClassQueuedBlocked, "", EvidenceScheduler},
		{"a job GitHub is holding", stuckHeldJobID, ClassHeldByGitHub, "", EvidenceConclusion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job, err := h.st.GetJob(h.ctx, c.id)
			if err != nil {
				t.Fatalf("the demo has no job %s: %v", c.id, err)
			}
			got := h.explain(t, job)
			if got.Class != c.class || got.Confidence != ConfidenceHigh {
				t.Errorf("%s is %s with %s confidence (%s), want %s with high", c.id, got.Class, got.Confidence, got.ConfidenceReason, c.class)
			}
			if got.ProblemCode != c.problem {
				t.Errorf("%s names problem %q, want %q", c.id, got.ProblemCode, c.problem)
			}
			if !slices.ContainsFunc(got.Evidence, func(e Evidence) bool { return e.Kind == c.kind }) {
				t.Errorf("%s carries no %q evidence: %+v", c.id, c.kind, got.Evidence)
			}
			if len(got.NextSteps) == 0 && got.Fix != "" {
				t.Errorf("%s has a fix and no next steps", c.id)
			}
		})
	}
}

// `zoomies why --latest-failed` against `zoomies demo` is how the feature is
// first tried, and it asks the jobs list for the newest job that went wrong. What
// it finds there has to be a failure the fleet can say something true about,
// rather than the workflow's own, which is the answer that teaches the least.
func TestTheNewestFailureInTheDemoIsOneTheFleetCanSpeakTo(t *testing.T) {
	h := newHarness(t)
	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}
	newest, _, err := h.st.ListJobs(h.ctx, store.JobFilter{FailedOnly: true}, store.Page{Limit: 1})
	if err != nil || len(newest) != 1 {
		t.Fatalf("ListJobs: %v, %d jobs", err, len(newest))
	}
	got := h.explain(t, newest[0])
	if got.Class != ClassOOM || got.Confidence != ConfidenceHigh {
		t.Errorf("the newest failure in the demo, %s, is %s with %s confidence; want an out-of-memory kill with high", newest[0].ID, got.Class, got.Confidence)
	}
}

// The timed-out job replaces one failure with another, so every total the demo's
// pages and tests already count stays where it was.
func TestTheTimedOutJobIsAFailureTheDemoAlreadyHad(t *testing.T) {
	h := newHarness(t)
	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}
	job, err := h.st.GetJob(h.ctx, fmt.Sprintf("job_demo%03d", demoTimedOutJob))
	if err != nil {
		t.Fatal(err)
	}
	if job.Conclusion != "timed_out" || !store.IsFailedConclusion(job.Conclusion) {
		t.Fatalf("conclusion = %q, want timed_out, which GitHub counts as a failure", job.Conclusion)
	}
	if step := job.FailedStep(); step == nil || step.Conclusion != "timed_out" {
		t.Errorf("the step it stopped at = %+v, want the working step, concluded timed_out", step)
	}
	if ran := job.CompletedAt.Sub(*job.StartedAt); ran.Minutes() != 30 {
		t.Errorf("it ran for %s, want the half hour of a workflow's own limit", ran)
	}
}

// The timed-out job is made from a failure, not added to them, which is what lets
// the fixture claim that every page counting the demo's failures still counts the
// same number. A change that took a different job, or added one, moves these.
func TestTheDemosOutcomesStayWhereTheirPagesCountThem(t *testing.T) {
	h := newHarness(t)
	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}
	jobs, _, err := h.st.ListJobs(h.ctx, store.JobFilter{}, store.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	failed := 0
	for _, j := range jobs {
		if j.State == store.JobCompleted {
			counts[j.Conclusion]++
		}
		if j.Failed() {
			failed++
		}
	}
	// Seven workflow failures, one of them stopped at its time limit, and the
	// job whose runner was lost make eight; seven more were cancelled.
	if failed != 8 || counts["timed_out"] != 1 || counts["cancelled"] != 7 {
		t.Errorf("failed %d, timed out %d, cancelled %d; want 8, 1 and 7: %v", failed, counts["timed_out"], counts["cancelled"], counts)
	}
}

// A job waiting on a blocked pool is a job still queued: the sweep that retires
// queued jobs GitHub no longer lists is for the ones left behind by a day, and a
// fixture that was already that old when it was written would be gone on the
// controller's first housekeeping pass, taking the question it exists to ask with it.
func TestTheBlockedJobSurvivesTheSweepForStaleQueuedJobs(t *testing.T) {
	h := seededWithTrouble(t)
	h.c.expireStaleQueuedJobs(h.ctx, h.c.Now())
	job, err := h.st.GetJob(h.ctx, stuckBlockedJobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != store.JobQueued {
		t.Errorf("the blocked job is %s after the stale sweep, want it still queued", job.State)
	}
}
