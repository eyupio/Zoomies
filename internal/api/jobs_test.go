package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

func TestCancelJobWorkflowCallsGitHubAndRecordsRequest(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.GitHub.AllowWorkflowCancellation = true })
	h.gh.SetPermissions(map[string]string{"actions": "write"})
	inst := h.installation()
	pool := h.pool(inst, "linux")
	j := h.job(pool, store.JobQueued)
	_, cookie := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/jobs/" + j.ID + "/cancel", body: map[string]any{"force": false}, cookie: cookie})
	if resp.status != http.StatusAccepted {
		t.Logf("controller log:\n%s", h.logs.text())
	}
	resp.mustStatus(t, http.StatusAccepted, "cancelling a workflow run")

	force := h.do(request{method: http.MethodPost, path: "/api/v1/jobs/" + j.ID + "/cancel", body: map[string]any{"force": true}, cookie: cookie})
	force.mustStatus(t, http.StatusAccepted, "force cancelling a workflow run")

	for _, want := range []string{
		"POST /repos/acme/widgets/actions/runs/1/cancel",
		"POST /repos/acme/widgets/actions/runs/1/force-cancel",
	} {
		if !slices.Contains(h.gh.Requests(), want) {
			t.Fatalf("GitHub requests = %v, want %q", h.gh.Requests(), want)
		}
	}
	events, err := h.ctrl.JobEvents(h.ctx, j.ID)
	if err != nil {
		t.Fatalf("JobEvents: %v", err)
	}
	if len(events) != 2 || events[0].Kind != store.JobEventCancelRequested || events[1].Kind != store.JobEventCancelRequested {
		t.Fatalf("events = %+v, want two cancel_requested entries", events)
	}
	after, err := h.st.GetJob(h.ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if after.State != store.JobQueued {
		t.Fatalf("state = %q, want queued until GitHub confirms cancellation", after.State)
	}
	if after.Provisioning != "paused" {
		t.Fatalf("provisioning = %q, want paused immediately after GitHub accepts run cancellation", after.Provisioning)
	}
}

func TestCancelJobWorkflowCanBeDisabledAndIsOperatorOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.GitHub.AllowWorkflowCancellation = false })
	inst := h.installation()
	pool := h.pool(inst, "linux")
	j := h.job(pool, store.JobQueued)
	_, viewer := h.user("viewer", store.RoleViewer)
	_, operator := h.user("operator", store.RoleOperator)

	h.do(request{method: http.MethodPost, path: "/api/v1/jobs/" + j.ID + "/cancel", body: map[string]any{}, cookie: viewer}).mustStatus(t, http.StatusForbidden, "a viewer cancelling a workflow")
	h.do(request{method: http.MethodPost, path: "/api/v1/jobs/" + j.ID + "/cancel", body: map[string]any{}, cookie: operator}).mustStatus(t, http.StatusConflict, "cancellation while the feature is disabled")
}

// The Workflows page's own run rows cancel a run directly, with no job ID in
// hand -- so unlike cancelling through a job, every job the run still owns
// gets the operator's request in its own timeline, not only whichever job
// happened to be used to reach the run.
func TestCancelWorkflowRunCallsGitHubAndRecordsRequestOnEveryLiveJob(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.GitHub.AllowWorkflowCancellation = true })
	h.gh.SetPermissions(map[string]string{"actions": "write"})
	inst := h.installation()
	pool := h.pool(inst, "linux")
	queued := h.job(pool, store.JobQueued)
	running := h.job(pool, store.JobInProgress)
	_, cookie := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/workflow-runs/cancel",
		body: map[string]any{"repo": "acme/widgets", "run_id": 1, "force": false}, cookie: cookie})
	if resp.status != http.StatusAccepted {
		t.Logf("controller log:\n%s", h.logs.text())
	}
	resp.mustStatus(t, http.StatusAccepted, "cancelling a workflow run")

	var out cancelWorkflowRunResponse
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Accepted || out.Jobs != 2 || out.Repo != "acme/widgets" || out.RunID != 1 {
		t.Fatalf("response = %+v, want accepted with both jobs", out)
	}

	if !slices.Contains(h.gh.Requests(), "POST /repos/acme/widgets/actions/runs/1/cancel") {
		t.Fatalf("GitHub requests = %v, want a run cancellation", h.gh.Requests())
	}

	for _, id := range []string{queued.ID, running.ID} {
		events, err := h.ctrl.JobEvents(h.ctx, id)
		if err != nil {
			t.Fatalf("JobEvents(%s): %v", id, err)
		}
		if len(events) != 1 || events[0].Kind != store.JobEventCancelRequested {
			t.Fatalf("job %s events = %+v, want one cancel_requested entry", id, events)
		}
	}
}

func TestCancelWorkflowRunValidatesItsBody(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.GitHub.AllowWorkflowCancellation = true })
	_, operator := h.user("operator", store.RoleOperator)

	h.do(request{method: http.MethodPost, path: "/api/v1/workflow-runs/cancel",
		body: map[string]any{"run_id": 1}, cookie: operator}).
		mustStatus(t, http.StatusBadRequest, "a run without a repository")
	h.do(request{method: http.MethodPost, path: "/api/v1/workflow-runs/cancel",
		body: map[string]any{"repo": "acme/widgets"}, cookie: operator}).
		mustStatus(t, http.StatusBadRequest, "a run without a run ID")
	h.do(request{method: http.MethodPost, path: "/api/v1/workflow-runs/cancel",
		body: map[string]any{"repo": "acme/nothing-here", "run_id": 999}, cookie: operator}).
		mustStatus(t, http.StatusNotFound, "a run this fleet has never heard of")
}

// A run this fleet knows about but whose every job has already completed is
// refused for the same reason cancelling through one of its jobs is: there is
// nothing left for GitHub to stop.
func TestCancelWorkflowRunRefusesAFinishedRun(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.GitHub.AllowWorkflowCancellation = true })
	inst := h.installation()
	pool := h.pool(inst, "linux")
	h.job(pool, store.JobCompleted)
	_, operator := h.user("operator", store.RoleOperator)

	h.do(request{method: http.MethodPost, path: "/api/v1/workflow-runs/cancel",
		body: map[string]any{"repo": "acme/widgets", "run_id": 1}, cookie: operator}).
		mustStatus(t, http.StatusConflict, "a run every job of which has completed")
}

// The Jobs page asks for managed=true by default, so the parameter has to reach
// the store: GitHub reports every job in an installed repository, and a fleet
// view that silently includes hosted-runner jobs answers "how is my fleet
// doing?" with somebody else's numbers.
func TestListJobsManagedLeavesOutJobsThisFleetNeverRan(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	mine := h.job(pool, store.JobCompleted)

	now := time.Now().Add(-time.Minute)
	started := now.Add(time.Second)
	done := now.Add(time.Second * 30)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 99, GitHubRunID: 99, Repo: "acme/widgets", Workflow: "ci",
		JobName: "hosted", Labels: store.StringSlice{"ubuntu-latest"},
		State: store.JobCompleted, Conclusion: "success",
		QueuedAt: now, StartedAt: &started, CompletedAt: &done,
	}); err != nil {
		t.Fatalf("recording the hosted job: %v", err)
	}

	_, cookie := h.user("viewer", store.RoleViewer)

	var page struct {
		Items []struct {
			ID      string `json:"id"`
			JobName string `json:"job_name"`
		} `json:"items"`
		Total int `json:"total"`
	}
	resp := h.do(request{method: "GET", path: "/api/v1/jobs?managed=true", cookie: cookie})
	resp.mustStatus(t, 200, "listing managed jobs")
	resp.into(t, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != mine.ID {
		t.Fatalf("managed jobs = %+v (total %d), want only %s", page.Items, page.Total, mine.ID)
	}

	resp = h.do(request{method: "GET", path: "/api/v1/jobs", cookie: cookie})
	resp.mustStatus(t, 200, "listing every job")
	resp.into(t, &page)
	if page.Total != 2 {
		t.Fatalf("unfiltered total = %d, want both jobs -- the toggle has to be able to show them", page.Total)
	}
}

// The drawer's timeline and the failed filter are the two things this change
// gives an operator, and both have to be reachable over the API the UI uses.
func TestJobTimelineAndFailedFilterAreServed(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	green := h.job(pool, store.JobCompleted)
	red := h.job(pool, store.JobInProgress)
	if _, _, err := h.st.SetJobRunnerFault(h.ctx, red.ID, "runner zoomies-x stopped while this job was running: exited with code 137", store.FaultOutOfMemory); err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}
	if err := h.st.AppendJobEvent(h.ctx, &store.JobEvent{JobID: red.ID, Kind: store.JobEventQueued, Source: "webhook", Message: "GitHub queued it"}); err != nil {
		t.Fatalf("AppendJobEvent: %v", err)
	}
	if err := h.st.AppendJobEvent(h.ctx, &store.JobEvent{JobID: red.ID, Kind: store.JobEventRunnerLost, Source: "agent", Message: "the runner stopped", RunnerName: "zoomies-x"}); err != nil {
		t.Fatalf("AppendJobEvent: %v", err)
	}
	_, cookie := h.user("viewer", store.RoleViewer)

	var timeline struct {
		Items []struct {
			Kind       string `json:"kind"`
			Source     string `json:"source"`
			Message    string `json:"message"`
			RunnerName string `json:"runner_name"`
			At         string `json:"at"`
		} `json:"items"`
	}
	resp := h.do(request{method: "GET", path: "/api/v1/jobs/" + red.ID + "/events", cookie: cookie})
	resp.mustStatus(t, 200, "reading the timeline")
	resp.into(t, &timeline)
	if len(timeline.Items) != 2 || timeline.Items[0].Kind != "queued" || timeline.Items[1].Kind != "runner_lost" {
		t.Fatalf("timeline = %+v, want queued then runner_lost", timeline.Items)
	}
	if timeline.Items[1].RunnerName != "zoomies-x" || timeline.Items[1].At == "" {
		t.Fatalf("runner_lost entry = %+v, want the runner named and a timestamp", timeline.Items[1])
	}

	resp = h.do(request{method: "GET", path: "/api/v1/jobs/" + green.ID + "/events", cookie: cookie})
	resp.mustStatus(t, 200, "a job with no history")
	resp.into(t, &timeline)
	if timeline.Items == nil || len(timeline.Items) != 0 {
		t.Fatalf("an empty timeline should be an empty list, got %+v", timeline.Items)
	}

	resp = h.do(request{method: "GET", path: "/api/v1/jobs/job_missing/events", cookie: cookie})
	resp.mustStatus(t, 404, "a timeline for a job that does not exist")

	var page struct {
		Items []struct {
			ID          string `json:"id"`
			RunnerFault string `json:"runner_fault"`
			Steps       []any  `json:"steps"`
			FailedStep  *struct {
				Name string `json:"name"`
			} `json:"failed_step"`
		} `json:"items"`
		Total int `json:"total"`
	}
	resp = h.do(request{method: "GET", path: "/api/v1/jobs?failed=true", cookie: cookie})
	resp.mustStatus(t, 200, "listing failed jobs")
	resp.into(t, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != red.ID {
		t.Fatalf("failed jobs = %+v (total %d), want only %s", page.Items, page.Total, red.ID)
	}
	if page.Items[0].RunnerFault == "" || page.Items[0].Steps == nil || page.Items[0].FailedStep != nil {
		t.Fatalf("failed job = %+v, want its fault, an empty steps list and no failed step", page.Items[0])
	}
}

// Which installation a job belongs to is now half of why a pool can or cannot
// run it, so the answer has to be on the wire: the Jobs page explains an
// unclaimed job from the row it was handed, and a field the server keeps to
// itself is a field the page cannot say anything about.
func TestAJobCarriesTheInstallationItBelongsTo(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	mine := h.job(pool, store.JobQueued)
	_, cookie := h.user("viewer", store.RoleViewer)

	var out struct {
		ID             string `json:"id"`
		InstallationID string `json:"installation_id"`
	}
	resp := h.do(request{method: "GET", path: "/api/v1/jobs/" + mine.ID, cookie: cookie})
	resp.mustStatus(t, 200, "getting a job")
	resp.into(t, &out)
	if out.InstallationID != inst.ID {
		t.Fatalf("installation_id = %q, want %q", out.InstallationID, inst.ID)
	}
}

// The explanation is its own route rather than a field on the job, so this is
// the check that it is reachable, authorised like the rest of the jobs surface,
// and carries the shape the drawer and the CLI will both render.
func TestTheJobExplanationIsItsOwnRoute(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	pool := h.pool(inst, "linux-x64")
	_ = pool

	job, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 7001, Repo: "acme/widgets", Workflow: "CI", JobName: "build",
		Labels: store.NormalizeLabels([]string{"self-hosted", "cuda12"}),
		State:  store.JobQueued, QueuedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	u, _ := h.user("viewer", store.RoleViewer)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/" + job.ID + "/explanation", cookie: h.session(u)})
	resp.mustStatus(t, http.StatusOK, "explanation")
	var out struct {
		JobID   string `json:"job_id"`
		State   string `json:"state"`
		Summary string `json:"summary"`
		Fix     string `json:"fix"`
		Waiting bool   `json:"waiting"`
		Blocked bool   `json:"blocked"`
	}
	resp.into(t, &out)

	if out.JobID != job.ID || out.State != string(store.JobQueued) {
		t.Errorf("the explanation is not about the job asked for: %+v", out)
	}
	// A viewer can read it, and what they read is an answer rather than an
	// empty string they have to interpret.
	if out.Summary == "" {
		t.Error("the explanation has no summary; the page that renders it would show nothing")
	}
	if !out.Blocked || out.Fix == "" {
		t.Errorf("a job no pool claims should be blocked with something to do: %+v", out)
	}

	// A job that does not exist is a 404 rather than an explanation of nothing.
	missing := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/job_nope/explanation", cookie: h.session(u)})
	missing.mustStatus(t, http.StatusNotFound, "explanation for a missing job")
}

// The re-run is the one thing Zoomies can do about a failure it caused, and
// the API is where the button, the CLI and a script all reach it. Its refusals
// matter as much as its success: each one is a different sentence, because
// "wait" and "you are looking at the wrong job" send somebody to different
// places.
func TestRerunIsServedForAFailedJobAndRefusedWithAReasonOtherwise(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	_, operator := h.user("operator", store.RoleOperator)

	// A job still running has nothing to run again.
	running := h.job(pool, store.JobInProgress)
	resp := h.do(request{method: "POST", path: "/api/v1/jobs/" + running.ID + "/rerun", cookie: operator})
	resp.mustStatus(t, 409, "re-running a job that has not finished")
	if !strings.Contains(string(resp.body), "has not finished") {
		t.Fatalf("the refusal does not say what to wait for: %s", resp.body)
	}

	// One that succeeded is the other refusal: GitHub reruns the failed jobs
	// of a run, so there is nothing for it to do.
	green := h.job(pool, store.JobCompleted)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: green.GitHubJobID, State: store.JobCompleted, Conclusion: "success",
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	resp = h.do(request{method: "POST", path: "/api/v1/jobs/" + green.ID + "/rerun", cookie: operator})
	resp.mustStatus(t, 409, "re-running a job that did not fail")
	if !strings.Contains(string(resp.body), "did not fail") {
		t.Fatalf("the refusal does not say why: %s", resp.body)
	}

	// A job the fleet broke: accepted, with the run named and the domain
	// echoed back so a script can log what it just spent minutes on.
	red := h.job(pool, store.JobCompleted)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: red.GitHubJobID, State: store.JobCompleted, Conclusion: "failure",
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if _, _, err := h.st.SetJobRunnerFault(h.ctx, red.ID, "runner zoomies-x stopped: out of memory", store.FaultOutOfMemory); err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}
	resp = h.do(request{method: "POST", path: "/api/v1/jobs/" + red.ID + "/rerun", cookie: operator})
	resp.mustStatus(t, 202, "re-running a job the fleet broke")
	var out struct {
		Accepted    bool   `json:"accepted"`
		RunID       int64  `json:"run_id"`
		FaultDomain string `json:"fault_domain"`
	}
	resp.into(t, &out)
	if !out.Accepted || out.RunID != red.GitHubRunID || out.FaultDomain != "fleet" {
		t.Fatalf("rerun response = %+v, want the run named and the domain echoed", out)
	}

	// And it spends somebody's CI minutes, so a viewer may not.
	_, viewer := h.user("watcher", store.RoleViewer)
	h.do(request{method: "POST", path: "/api/v1/jobs/" + red.ID + "/rerun", cookie: viewer}).
		mustStatus(t, 403, "a viewer asking for a re-run")

	h.do(request{method: "POST", path: "/api/v1/jobs/job_missing/rerun", cookie: operator}).
		mustStatus(t, 404, "re-running a job that does not exist")
}

// Whose a failure was is the question an operator arrives with, and GitHub
// records both halves as "failure" -- so the two filters are the only place it
// can be asked. They have to be genuinely opposite halves of the failed list,
// and a category nobody knows has to be refused rather than quietly matching
// everything, which would read as a fleet in better shape than it is.
func TestTheFaultFiltersAreOppositeHalvesAndRefuseAnUnknownCategory(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	_, cookie := h.user("viewer", store.RoleViewer)

	ours := h.job(pool, store.JobCompleted)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: ours.GitHubJobID, State: store.JobCompleted, Conclusion: "failure",
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if _, _, err := h.st.SetJobRunnerFault(h.ctx, ours.ID, "runner zoomies-x stopped: out of memory", store.FaultOutOfMemory); err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}
	theirs := h.job(pool, store.JobCompleted)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: theirs.GitHubJobID, State: store.JobCompleted, Conclusion: "failure",
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	type jobView struct {
		ID          string `json:"id"`
		FaultKind   string `json:"fault_kind"`
		FaultDomain string `json:"fault_domain"`
		FaultFix    string `json:"fault_fix"`
	}
	// A fresh page per call, because decoding into a reused one merges rather
	// than replaces: `fault_kind` is omitempty, so a job that carries none
	// would keep the previous job's category and the assertion below would
	// pass on a value nothing sent.
	only := func(query, want, why string) jobView {
		t.Helper()
		var page struct {
			Items []jobView `json:"items"`
			Total int       `json:"total"`
		}
		resp := h.do(request{method: "GET", path: "/api/v1/jobs?" + query, cookie: cookie})
		resp.mustStatus(t, 200, why)
		resp.into(t, &page)
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("%s returned %+v (total %d), want only %s", query, page.Items, page.Total, want)
		}
		return page.Items[0]
	}

	// The remedy travels with the job, so the UI, the CLI and the problems
	// drawer cannot offer three different answers to the same failure.
	got := only("faulted=true", ours.ID, "the fleet's own failures")
	if got.FaultKind != "out_of_memory" || got.FaultDomain != "fleet" ||
		!strings.Contains(got.FaultFix, "memory limit") {
		t.Fatalf("the fleet's failure does not carry its category and fix: %+v", got)
	}
	got = only("workflow_failed=true", theirs.ID, "the workflows' own failures")
	if got.FaultKind != "" || got.FaultDomain != "workflow" {
		t.Fatalf("a test failure was given a fleet category: %+v", got)
	}
	only("fault=out_of_memory", ours.ID, "narrowing to one category")

	// Both halves at once is neither of them, and says so rather than
	// returning an empty page with no explanation.
	h.do(request{method: "GET", path: "/api/v1/jobs?faulted=true&workflow_failed=true", cookie: cookie}).
		mustStatus(t, 400, "asking for both halves of one list")

	resp := h.do(request{method: "GET", path: "/api/v1/jobs?fault=quantum_decoherence", cookie: cookie})
	resp.mustStatus(t, 400, "a category this build does not know")
	if !strings.Contains(string(resp.body), "out_of_memory") {
		t.Fatalf("the refusal does not name the categories to use instead: %s", resp.body)
	}
}

// The window this covers is GitHub's: it accepts a cancellation at once, and
// reports the jobs over in its own time. Until this, the fleet spent that
// window reporting cancelled work as waiting or running -- a queue depth
// nobody could clear, and a Running tile counting a job whose runner had
// already been taken away.
func TestCancellingAWorkflowRunClearsItsJobsFromTheFleetsFigures(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.GitHub.AllowWorkflowCancellation = true })
	h.gh.SetPermissions(map[string]string{"actions": "write"})
	inst := h.installation()
	pool := h.pool(inst, "linux")
	queued := h.job(pool, store.JobQueued)
	running := h.job(pool, store.JobInProgress)
	_, operator := h.user("operator", store.RoleOperator)

	figures := func() (int, int) {
		t.Helper()
		resp := h.do(request{method: "GET", path: "/api/v1/stats", cookie: operator})
		resp.mustStatus(t, http.StatusOK, "stats")
		var out struct {
			Fleet struct {
				QueuedJobs  int `json:"queued_jobs"`
				RunningJobs int `json:"running_jobs"`
			} `json:"fleet"`
		}
		if err := json.Unmarshal(resp.body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Fleet.QueuedJobs, out.Fleet.RunningJobs
	}
	listed := func(query string) int {
		t.Helper()
		resp := h.do(request{method: "GET", path: "/api/v1/jobs?" + query, cookie: operator})
		resp.mustStatus(t, http.StatusOK, query)
		var page struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(resp.body, &page); err != nil {
			t.Fatal(err)
		}
		return page.Total
	}

	if q, r := figures(); q != 1 || r != 1 {
		t.Fatalf("before cancelling: queued=%d running=%d, want 1 and 1", q, r)
	}

	// Both jobs belong to the same run, so cancelling either takes both.
	h.do(request{method: http.MethodPost, path: "/api/v1/jobs/" + queued.ID + "/cancel",
		body: map[string]any{"force": false}, cookie: operator}).
		mustStatus(t, http.StatusAccepted, "cancel")

	if q, r := figures(); q != 0 || r != 0 {
		t.Fatalf("after cancelling: queued=%d running=%d, want 0 and 0", q, r)
	}
	// GitHub is still the one that says how they ended, so the rows have not
	// moved on -- which is exactly why the figures needed a second signal.
	for _, id := range []string{queued.ID, running.ID} {
		after, err := h.st.GetJob(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if after.State == store.JobCompleted {
			t.Fatalf("job %s was concluded locally; GitHub owns the conclusion", id)
		}
		if !after.Cancelling() {
			t.Fatalf("job %s does not report itself as cancelling: %+v", id, after)
		}
	}

	// The lists agree with the figures, and the history still holds both.
	if n := listed("state=queued&cancelling=false"); n != 0 {
		t.Errorf("queued list = %d, want 0", n)
	}
	if n := listed("state=in_progress&cancelling=false"); n != 0 {
		t.Errorf("running list = %d, want 0", n)
	}
	if n := listed("cancelling=true"); n != 2 {
		t.Errorf("cancelling list = %d, want both jobs", n)
	}
	if n := listed("state=queued&state=in_progress"); n != 2 {
		t.Errorf("history = %d, want both jobs still listed", n)
	}

	// And the drawer says so rather than naming a runner the fleet took away.
	why := h.do(request{method: "GET", path: "/api/v1/jobs/" + running.ID + "/explanation", cookie: operator})
	if !strings.Contains(string(why.body), "workflow run was cancelled") {
		t.Errorf("explanation: %s", why.body)
	}
}

// The explanation now says which class of failure a job is and quotes the
// runner's last lines around the decisive one. The lines are the only part
// a caller can size, and the bounds matter: none when none were asked for,
// never more than the store keeps, and a value that is not a number is a
// 400 that names the parameter rather than a silent default.
func TestTheExplanationCarriesItsClassAndABoundedExcerpt(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	h.pool(inst, "linux-x64")
	done := time.Now().Add(-time.Minute)
	job, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 7002, Repo: "acme/widgets", Workflow: "CI", JobName: "build",
		Labels: store.NormalizeLabels([]string{"self-hosted", "linux-x64"}),
		State:  store.JobCompleted, Conclusion: "failure", QueuedAt: time.Now().Add(-time.Hour), CompletedAt: &done,
		RunnerID: "run_oom", RunnerFault: "runner exited with code 137: the container was killed for exceeding its memory limit", FaultKind: store.FaultOutOfMemory,
	})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	tail := make([]string, 40)
	for i := range tail {
		tail[i] = fmt.Sprintf("step output %d", i+1)
	}
	tail[30] = "Killed process 2231 (node) total-vm:9812344kB"
	if _, err := h.st.SetJobOutputTail(h.ctx, "run_oom", tail); err != nil {
		t.Fatalf("SetJobOutputTail: %v", err)
	}

	u, _ := h.user("viewer", store.RoleViewer)
	get := func(query string) (map[string]any, *response) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/" + job.ID + "/explanation" + query, cookie: h.session(u)})
		var out map[string]any
		if resp.status == http.StatusOK {
			resp.into(t, &out)
		}
		return out, resp
	}

	out, resp := get("")
	resp.mustStatus(t, http.StatusOK, "explanation")
	if out["class"] != "oom" || out["problem_code"] != "jobs.oom_killed" {
		t.Fatalf("class %v, problem_code %v; want oom and jobs.oom_killed", out["class"], out["problem_code"])
	}
	kinds := map[string]bool{}
	for _, e := range out["evidence"].([]any) {
		kinds[e.(map[string]any)["kind"].(string)] = true
	}
	if !kinds["fault_kind"] || !kinds["exit_code"] || !kinds["conclusion"] {
		t.Fatalf("evidence kinds = %v; want the fault, the exit code and the conclusion", kinds)
	}
	if steps := out["next_steps"].([]any); len(steps) == 0 {
		t.Fatal("no next steps for an out-of-memory kill")
	}
	excerpt := out["log_excerpt"].(map[string]any)
	lines := excerpt["lines"].([]any)
	if len(lines) != 12 || lines[11].(map[string]any)["decisive"] != true {
		t.Fatalf("default excerpt = %d lines, last %v; want 12 ending on the decisive one", len(lines), lines[len(lines)-1])
	}

	if out, _ := get("?logs=0"); out["log_excerpt"] != nil {
		t.Fatalf("logs=0 gives null, not %v", out["log_excerpt"])
	}
	if out, _ := get("?logs=99"); len(out["log_excerpt"].(map[string]any)["lines"].([]any)) != 31 {
		t.Fatalf("logs=99 is clamped to what the store keeps: %v", out["log_excerpt"])
	}
	_, resp = get("?logs=x")
	resp.mustStatus(t, http.StatusBadRequest, "logs=x")
	if !strings.Contains(string(resp.body), `"logs"`) {
		t.Fatalf("the 400 does not name the parameter: %s", resp.body)
	}
}
