package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// The text of the failed runs of eyupio/rea that this feature exists for.
const (
	quotaRunLog = `2026-10-06T06:47:48.0799042Z With the provided path, there will be 3 files uploaded
2026-10-06T06:47:48.2010869Z ##[error]Failed to CreateArtifact: Artifact storage quota has been hit. Unable to upload any new artifacts. Usage is recalculated every 6-12 hours.
`
	oversizedRunLog = `2026-10-03T17:02:49.4790859Z Context generation refused: A source file exceeds the context size limit; add an exclusion
2026-10-03T17:02:49.4899378Z ##[error]Process completed with exit code 1.
`
)

func quotaRun(commit string, finished time.Time) github.FakeContextRun {
	return github.FakeContextRun{Commit: commit, Conclusion: "failure", Finished: finished, Jobs: []github.FakeContextJob{
		{Name: "generate", Conclusion: "failure", Log: quotaRunLog, Steps: []github.FakeContextStep{
			{Name: "Set up job", Conclusion: "success"}, {Name: "Generate bounded source context", Conclusion: "success"},
			{Name: "Run actions/upload-artifact@043fb46", Conclusion: "failure"}}},
		{Name: "publish", Conclusion: "skipped"},
	}}
}

// noRunnerRun is runs 11 and 12 of eyupio/rea: a job that was queued, never
// given a runner, and cancelled by its own timeout, in a run marked failed.
func noRunnerRun(commit string, finished time.Time) github.FakeContextRun {
	return github.FakeContextRun{Commit: commit, Conclusion: "failure", Finished: finished, Jobs: []github.FakeContextJob{
		{Name: "generate", Conclusion: "cancelled"}, {Name: "publish", Conclusion: "skipped"},
	}}
}

// mergedContext is a repository whose setup has been reviewed and merged, and
// whose context is behind its trusted branch: the state in which the workflow is
// supposed to be running, and the only one in which a failed run matters.
func mergedContext(t *testing.T) (h *harness, draft store.AIContextRepository, commit, admin string) {
	t.Helper()
	h, inst, _ := migrationHarness(t)
	_, admin = h.user("diagnosis-admin", store.RoleAdmin)
	discovery, err := h.ctrl.DiscoverAIContext(h.ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	selected := discovery.Repositories[0]
	draft = store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: inst.ID, RepositoryID: selected.ID}, FullName: selected.FullName, Config: aicontext.DefaultConfig(selected.DefaultBranch)}
	if err := h.st.CreateAIContextRepository(h.ctx, &draft); err != nil {
		t.Fatal(err)
	}
	plan, err := h.ctrl.PreviewAIContextSetup(h.ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = h.ctrl.CreateAIContextSetupPR(h.ctx, draft.ID, controller.AIContextSetupApproval{Revision: plan.Revision, PlanHash: plan.PlanHash})
	if err != nil {
		t.Fatal(err)
	}
	if !h.gh.MergeContextPull(draft.FullName, plan.Setup.PRNumber) {
		t.Fatal("merge failed")
	}
	// The first pass finds nothing published for the current commit, which is how
	// the commit the workflow is being waited on for becomes known.
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, time.Hour)
	fresh, err := h.st.GetAIContextFreshness(h.ctx, draft.ID)
	if err != nil || fresh.State != "stale" || fresh.DesiredCommit == "" {
		t.Fatalf("freshness after merge: %+v %v", fresh, err)
	}
	return h, draft, fresh.DesiredCommit, admin
}

func (h *harness) contextDiagnosis(t *testing.T, admin, id string) *aicontext.Diagnosis {
	t.Helper()
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/repositories/" + id, cookie: admin})
	resp.mustStatus(t, http.StatusOK, "reading the repository")
	var out struct {
		Diagnosis *aicontext.Diagnosis `json:"diagnosis"`
	}
	resp.into(t, &out)
	return out.Diagnosis
}

func (h *harness) contextProblem(t *testing.T, code string) *controller.Problem {
	t.Helper()
	problems, err := h.ctrl.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range problems {
		if problems[i].Code == code {
			return &problems[i]
		}
	}
	return nil
}

func (h *harness) staleReason(t *testing.T, id string) string {
	t.Helper()
	fresh, err := h.st.GetAIContextFreshness(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return fresh.Failure
}

// eyupio/rea, 5 to 6 October: every run failed at the artifact hand-off, and
// Zoomies started the workflow again every half hour without saying why.
func TestAFullArtifactQuotaIsExplainedAndWaitedOutRatherThanHammered(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	var artifacts []github.FakeArtifact
	for i := 0; i < 118; i++ {
		artifacts = append(artifacts, github.FakeArtifact{Name: "rea-graph-studio-windows-amd64-alpha", Bytes: 30 << 20})
	}
	h.gh.AddArtifacts(draft.FullName, artifacts...)
	dispatches := func() int { return h.gh.ContextDispatches(draft.FullName) }

	h.gh.AddContextRun(draft.FullName, quotaRun(commit, time.Now()))
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if dispatches() != 0 {
		t.Fatalf("the workflow was started %d times into a full quota", dispatches())
	}

	// The card says why, in words, and what is holding the space.
	reason := h.staleReason(t, draft.ID)
	if !strings.Contains(reason, "artifact storage is full") || !strings.Contains(reason, "retention-days") {
		t.Errorf("the card says %q", reason)
	}
	d := h.contextDiagnosis(t, admin, draft.ID)
	if d == nil || d.Cause != aicontext.CauseArtifactQuota || d.Commit != commit || d.RunURL == "" {
		t.Fatalf("diagnosis = %+v", d)
	}
	if !strings.Contains(d.Detail, "118 of them, named rea-graph-studio-windows-amd64-alpha, hold 3.5 GiB") {
		t.Errorf("the diagnosis does not say what holds the storage: %s", d.Detail)
	}
	if d.Retry == nil || !d.Retry.Automatic || d.Retry.AttemptsLeft != 3 || d.Retry.NextAt == nil ||
		d.Retry.NextAt.Before(time.Now().Add(5*time.Hour+50*time.Minute)) || d.Retry.NextAt.After(time.Now().Add(6*time.Hour+5*time.Minute)) {
		t.Errorf("retry plan = %+v, want an automatic retry in about six hours", d.Retry)
	}

	// It reaches the problems list, where an operator who is not on this page
	// sees it, loud because the context stays stale until somebody acts.
	p := h.contextProblem(t, "ai_context.artifact_quota")
	if p == nil || p.Severity != config.SeverityError || p.TargetKind != "ai_context" || p.TargetID != draft.ID || p.Audience != controller.AudienceFleet {
		t.Fatalf("problem = %+v", p)
	}
	if !strings.Contains(p.Title, draft.FullName) || !strings.Contains(p.Fix, "Zoomies will start the workflow again from") {
		t.Errorf("problem sentences: %q / %q", p.Title, p.Fix)
	}

	// Once the window has passed it is tried again, and the old failure is no
	// longer what the card blames while the new run is on its way.
	h.gh.AddContextRun(draft.FullName, quotaRun(commit, time.Now().Add(-7*time.Hour)))
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if dispatches() != 1 {
		t.Fatalf("dispatches after the window = %d, want 1", dispatches())
	}
	if h.contextDiagnosis(t, admin, draft.ID) != nil || h.contextProblem(t, "ai_context.artifact_quota") != nil {
		t.Error("the card still blames a run that has been replaced")
	}
	// And not again at once: the next wait counts from that start.
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if dispatches() != 1 {
		t.Errorf("dispatches = %d, want the second attempt to wait", dispatches())
	}
}

// eyupio/rea, 3 to 4 October: the first template refused the whole run for one
// large file. Running it again was never going to help.
func TestAFailureNoRunCanFixIsNeverRetriedAndPointsAtTheFix(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	h.gh.AddContextRun(draft.FullName, github.FakeContextRun{Commit: commit, Conclusion: "failure", Finished: time.Now().Add(-3 * time.Hour), Jobs: []github.FakeContextJob{
		{Name: "generate", Conclusion: "failure", Log: oversizedRunLog, Steps: []github.FakeContextStep{
			{Name: "Set up job", Conclusion: "success"}, {Name: "Generate bounded source context", Conclusion: "failure"}}},
		{Name: "publish", Conclusion: "skipped"},
	}})
	for i := 0; i < 3; i++ {
		_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	}
	if n := h.gh.ContextDispatches(draft.FullName); n != 0 {
		t.Fatalf("the workflow was started %d times for a failure it would repeat", n)
	}
	d := h.contextDiagnosis(t, admin, draft.ID)
	if d == nil || d.Cause != aicontext.CauseOversizedFile || d.Action != aicontext.ActionRepair || d.Retry == nil || d.Retry.Automatic || d.Retry.NextAt != nil {
		t.Fatalf("diagnosis = %+v", d)
	}
	if reason := h.staleReason(t, draft.ID); !strings.Contains(reason, "Reinstall / repair") {
		t.Errorf("the card does not name the fix: %q", reason)
	}
	p := h.contextProblem(t, "ai_context.oversized_file")
	if p == nil || !strings.Contains(p.Fix, "will not start the workflow again until this is fixed") {
		t.Fatalf("problem = %+v", p)
	}
}

// eyupio/rea, 5 October: GitHub never gave the job a runner. That is its fault
// and it passes, so Zoomies tries again without sitting out the grace period.
func TestARunnerGitHubNeverGaveIsRetriedWithoutWaitingForTheGrace(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	h.gh.AddContextRun(draft.FullName, noRunnerRun(commit, time.Now()))
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, time.Hour)
	if n := h.gh.ContextDispatches(draft.FullName); n != 0 {
		t.Fatalf("started at once, %d", n)
	}
	if d := h.contextDiagnosis(t, admin, draft.ID); d == nil || d.Cause != aicontext.CauseRunnerUnavailable || !d.Retry.Automatic {
		t.Fatalf("diagnosis = %+v", d)
	}
	if p := h.contextProblem(t, "ai_context.runner_unavailable"); p == nil || p.Severity != config.SeverityWarning {
		t.Fatalf("a passing GitHub fault should be a warning: %+v", p)
	}

	h.gh.AddContextRun(draft.FullName, noRunnerRun(commit, time.Now().Add(-31*time.Minute)))
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, time.Hour)
	if n := h.gh.ContextDispatches(draft.FullName); n != 1 {
		t.Fatalf("dispatches = %d, want one half an hour after the failure whatever the grace says", n)
	}
}

// Starting the workflow while it is running cancels the run in progress.
func TestARunStillWorkingIsNeverStartedOver(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	h.gh.AddContextRun(draft.FullName, github.FakeContextRun{Commit: commit, Status: "in_progress"})
	for i := 0; i < 3; i++ {
		_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	}
	if n := h.gh.ContextDispatches(draft.FullName); n != 0 {
		t.Fatalf("the workflow was started %d times over a run in progress", n)
	}
	if h.contextDiagnosis(t, admin, draft.ID) != nil {
		t.Error("a run that has not finished was blamed")
	}
}

// A run that succeeded is not the explanation for a context that is still
// behind, so the old rule -- a grace, then a bounded number of starts -- applies.
func TestAPassedRunThatLeavesTheContextBehindKeepsTheOriginalRule(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	h.gh.AddContextRun(draft.FullName, github.FakeContextRun{Commit: commit, Conclusion: "success"})
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, time.Hour)
	if n := h.gh.ContextDispatches(draft.FullName); n != 0 {
		t.Fatalf("started inside the grace: %d", n)
	}
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if n := h.gh.ContextDispatches(draft.FullName); n != 1 {
		t.Fatalf("dispatches after the grace = %d, want 1", n)
	}
	if h.contextDiagnosis(t, admin, draft.ID) != nil {
		t.Error("a run that succeeded was blamed")
	}
}

// A failure that is not about the last run must not be shown beside it.
func TestADiagnosisIsDroppedWhenTheSetupNoLongerTrustsTheWorkflow(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	h.gh.AddContextRun(draft.FullName, quotaRun(commit, time.Now()))
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if h.contextDiagnosis(t, admin, draft.ID) == nil {
		t.Fatal("no diagnosis to drop")
	}
	h.gh.AddFile(draft.FullName, aicontext.WorkflowPath, "unreviewed workflow")
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if h.contextDiagnosis(t, admin, draft.ID) != nil || h.contextProblem(t, "ai_context.artifact_quota") != nil {
		t.Error("a run's failure was still shown after the workflow stopped being the reviewed one")
	}
}

// Reading runs is a courtesy. Without the permission, or with GitHub failing, the
// card is exactly what it was before: generic, and still working.
func TestWithoutBeingAbleToReadRunsTheCardIsWhatItWasBefore(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	h.gh.AddContextRun(draft.FullName, quotaRun(commit, time.Now()))
	h.gh.SetPermissions(map[string]string{"metadata": "read", "contents": "write", "workflows": "write"})
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if h.contextDiagnosis(t, admin, draft.ID) != nil {
		t.Error("a diagnosis appeared without the permission to read runs")
	}
	if reason := h.staleReason(t, draft.ID); strings.Contains(reason, "artifact storage") || reason == "" {
		t.Errorf("the card says %q, want the generic reason", reason)
	}
}

// Every cause the classifier can name arrives on the card and in the list with a
// code of its own, from a real run read back through the API.
func TestEveryCauseReachesTheProblemsList(t *testing.T) {
	cases := map[aicontext.Cause]github.FakeContextJob{
		aicontext.CausePublicationRefused: {Name: "publish", Conclusion: "failure",
			Log:   "Context publication refused. Check GitHub write access, artifact integrity, source freshness and generated branch ownership.\n##[error]Process completed with exit code 1.\n",
			Steps: []github.FakeContextStep{{Name: "Validate and atomically publish the generated branch", Conclusion: "failure"}}},
		aicontext.CauseDeliveryFailed: {Name: "upload", Conclusion: "failure",
			Log:   "Zoomies refused the upload (HTTP 401): sign in\n##[error]Process completed with exit code 1.\n",
			Steps: []github.FakeContextStep{{Name: "Upload the verified context to Zoomies", Conclusion: "failure"}}},
		aicontext.CauseTooMuchSource: {Name: "generate", Conclusion: "failure",
			Log:   "Context generation refused: Serialized snapshot exceeds its limit; add exclusions\n##[error]Process completed with exit code 1.\n",
			Steps: []github.FakeContextStep{{Name: "Generate bounded source context", Conclusion: "failure"}}},
		aicontext.CauseSetupFailed: {Name: "generate", Conclusion: "failure",
			Steps: []github.FakeContextStep{{Name: "Install pinned generator without repository scripts", Conclusion: "failure"}}},
	}
	for cause, job := range cases {
		t.Run(string(cause), func(t *testing.T) {
			h, draft, commit, admin := mergedContext(t)
			h.gh.AddContextRun(draft.FullName, github.FakeContextRun{Commit: commit, Conclusion: "failure", Finished: time.Now(), Jobs: []github.FakeContextJob{job}})
			_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
			d := h.contextDiagnosis(t, admin, draft.ID)
			if d == nil || d.Cause != cause {
				t.Fatalf("diagnosis = %+v, want %s", d, cause)
			}
			code := fmt.Sprintf("ai_context.%s", cause)
			if cause == aicontext.CauseUnknown {
				code = "ai_context.run_failed"
			}
			if p := h.contextProblem(t, code); p == nil || p.Detail != d.Detail {
				t.Fatalf("no problem %s carrying the diagnosis: %+v", code, p)
			}
		})
	}
}

// GitHub can take a few seconds to list a run it has just accepted. If the next
// pass read the failed run before it, still long enough ago, and started another,
// the workflow's concurrency group would cancel the one a person had just asked for.
func TestAManualRegenerateIsNotImmediatelyFollowedByAnAutomaticOne(t *testing.T) {
	h, draft, commit, admin := mergedContext(t)
	// A failure long enough ago that a pass would start the workflow again itself.
	h.gh.AddContextRun(draft.FullName, quotaRun(commit, time.Now().Add(-7*time.Hour)))

	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories/" + draft.ID + "/regenerate", cookie: admin}).
		mustStatus(t, http.StatusAccepted, "regenerating by hand")
	if n := h.gh.ContextDispatches(draft.FullName); n != 1 {
		t.Fatalf("dispatches after the request = %d, want 1", n)
	}
	// The card stops blaming the run that has just been replaced.
	if h.contextDiagnosis(t, admin, draft.ID) != nil {
		t.Error("the card still blames a run that was just replaced")
	}

	// The fake, like GitHub a moment after accepting a run, does not list the new
	// one: the next pass sees the old failure, which on its own is due a retry.
	_ = h.ctrl.SyncAIContext(h.ctx, draft.ID, 0)
	if n := h.gh.ContextDispatches(draft.FullName); n != 1 {
		t.Fatalf("dispatches = %d: a second run was started on top of the one just requested", n)
	}
}
