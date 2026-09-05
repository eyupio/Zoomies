package controller

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

func kindsOfEvents(events []*store.JobEvent) []store.JobEventKind {
	out := make([]store.JobEventKind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// runnerLostTotal reads the lost-runner counter back through the registry, the
// way a scrape would, so the test needs nothing the production code does not.
func (h *harness) runnerLostTotal() float64 {
	h.t.Helper()
	families, err := h.c.metrics.reg.Gather()
	if err != nil {
		h.t.Fatalf("Gather: %v", err)
	}
	total := 0.0
	for _, mf := range families {
		if mf.GetName() != "zoomies_jobs_runner_lost_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			total += m.GetCounter().GetValue()
		}
	}
	return total
}

func (h *harness) timeline(jobID string) []*store.JobEvent {
	h.t.Helper()
	events, err := h.c.JobEvents(h.ctx, jobID)
	if err != nil {
		h.t.Fatalf("JobEvents: %v", err)
	}
	return events
}

// The timeline is the answer to "what happened to my job?", and it has to stay
// honest under GitHub's delivery guarantees: at least once, and out of order. A
// redelivery must not add a line, and a completed job must say where it failed
// in the words of the step that failed.
func TestJobDeliveriesWriteTheTimelineOnceEach(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}

	h.deliverJob(jobEvent{Action: "queued", JobID: 7007, Name: "test", Workflow: "CI", Labels: labels})
	job, err := h.st.GetJobByGitHubID(h.ctx, 7007)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	events := h.timeline(job.ID)
	if got := kindsOfEvents(events); !slices.Equal(got, []store.JobEventKind{store.JobEventQueued, store.JobEventClaimed}) {
		t.Fatalf("after queued: %v, want queued then claimed", got)
	}
	if !strings.Contains(events[0].Message, "CI / test") || !strings.Contains(events[0].Message, "acme/widgets") {
		t.Fatalf("queued entry does not name the job: %q", events[0].Message)
	}
	if !strings.Contains(events[1].Message, pool.Name) || events[1].Source != sourceWebhook {
		t.Fatalf("claimed entry = %+v, want the pool named and the webhook as source", events[1])
	}

	// GitHub sends the same delivery again.
	h.deliverJob(jobEvent{Action: "queued", JobID: 7007, Name: "test", Workflow: "CI", Labels: labels})
	if got := h.timeline(job.ID); len(got) != 2 {
		t.Fatalf("a redelivered queued added a timeline entry: %v", kindsOfEvents(got))
	}

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r := h.onlyRunner()
	mustReport(t, h, host.ID, r.ID, store.RunnerRegistering)
	mustReportRunning(t, h, host.ID, r.ID)

	h.deliverJob(jobEvent{Action: "in_progress", JobID: 7007, Name: "test", Workflow: "CI", Labels: labels, RunnerName: r.Name})
	events = h.timeline(job.ID)
	if got := kindsOfEvents(events); !slices.Equal(got, []store.JobEventKind{store.JobEventQueued, store.JobEventClaimed, store.JobEventStarted}) {
		t.Fatalf("after in_progress: %v", got)
	}
	started := events[2]
	if !strings.Contains(started.Message, r.Name) || !strings.Contains(started.Message, host.Name) || started.RunnerID != r.ID {
		t.Fatalf("started entry = %+v, want the runner and its host named", started)
	}

	h.deliverJob(jobEvent{Action: "completed", JobID: 7007, Name: "test", Workflow: "CI", Labels: labels,
		RunnerName: r.Name, Conclusion: "failure", Steps: failingSteps()})
	events = h.timeline(job.ID)
	if got := kindsOfEvents(events); len(got) != 4 || got[3] != store.JobEventCompleted {
		t.Fatalf("after completed: %v", got)
	}
	if want := "failed at step 2, Run tests"; !strings.Contains(events[3].Message, want) {
		t.Fatalf("completed entry = %q, want it to say %q", events[3].Message, want)
	}

	// And the row itself carries what the drawer shows.
	job, _ = h.st.GetJobByGitHubID(h.ctx, 7007)
	if step := job.FailedStep(); step == nil || step.Name != "Run tests" {
		t.Fatalf("FailedStep = %+v", step)
	}
	view := h.c.jobView(h.ctx, job)
	if view.FailedStep == nil || view.FailedStep.Number != 2 || len(view.Steps) != 3 {
		t.Fatalf("job view = %+v, want the failed step named and every step carried", view)
	}

	// A duplicate completed delivery is the commonest redelivery of all.
	h.deliverJob(jobEvent{Action: "completed", JobID: 7007, Name: "test", Workflow: "CI", Labels: labels,
		RunnerName: r.Name, Conclusion: "failure", Steps: failingSteps()})
	if got := h.timeline(job.ID); len(got) != 4 {
		t.Fatalf("a redelivered completed added a timeline entry: %v", kindsOfEvents(got))
	}
}

// An unmatched job says so on its timeline, and says when that changed: the
// operator who creates the missing pool should see the job claimed without
// having to work out which delivery did it.
func TestAnUnmatchedJobIsClaimedWhenAPoolAppears(t *testing.T) {
	h := newHarness(t)
	inst, _, _ := h.fleet()
	labels := []string{"self-hosted", "linux", "gpu"}

	h.deliverJob(jobEvent{Action: "queued", JobID: 8008, Labels: labels})
	job, err := h.st.GetJobByGitHubID(h.ctx, 8008)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	events := h.timeline(job.ID)
	if got := kindsOfEvents(events); !slices.Equal(got, []store.JobEventKind{store.JobEventQueued, store.JobEventUnmatched}) {
		t.Fatalf("timeline = %v, want queued then unmatched", got)
	}
	if !strings.Contains(events[1].Message, "no enabled pool") {
		t.Fatalf("unmatched entry does not explain itself: %q", events[1].Message)
	}

	gpu := h.pool(inst, "gpu", labels...)
	// GitHub redelivers, or the poller re-lists the queue; either way the
	// same payload arrives again and this time a pool answers it.
	h.deliverJob(jobEvent{Action: "queued", JobID: 8008, Labels: labels})
	events = h.timeline(job.ID)
	if got := kindsOfEvents(events); !slices.Equal(got, []store.JobEventKind{store.JobEventQueued, store.JobEventUnmatched, store.JobEventClaimed}) {
		t.Fatalf("timeline = %v, want a claimed entry added", got)
	}
	if !strings.Contains(events[2].Message, gpu.Name) {
		t.Fatalf("claimed entry = %q, want the new pool named", events[2].Message)
	}
}

// startJobOnRunner takes a job from queued to running on a runner this fleet
// started, the way the real sequence does.
func startJobOnRunner(t *testing.T, h *harness, hostID string, jobID int64, labels []string) (*store.Job, *store.Runner) {
	t.Helper()
	h.deliverJob(jobEvent{Action: "queued", JobID: jobID, Name: "test", Workflow: "CI", Labels: labels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r := h.onlyRunner()
	mustReport(t, h, hostID, r.ID, store.RunnerRegistering)
	mustReportRunning(t, h, hostID, r.ID)
	h.deliverJob(jobEvent{Action: "in_progress", JobID: jobID, Name: "test", Workflow: "CI", Labels: labels, RunnerName: r.Name})
	job, err := h.st.GetJobByGitHubID(h.ctx, jobID)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if job.RunnerID != r.ID {
		t.Fatalf("the job was not linked to runner %s", r.ID)
	}
	return job, r
}

// A runner that dies under a job produces a failure GitHub cannot tell from a
// test failure. The job has to say the fleet did it: on the row, on the
// timeline, in the problems drawer, and in the failed filter -- before GitHub
// has even noticed.
func TestARunnerFailingUnderItsJobIsRecordedOnTheJob(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 9009, labels)

	err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, State: store.RunnerFailed, ExitCode: 137,
		Message:    "runner exited with code 137: the container was killed for exceeding its memory limit",
		ObservedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}

	after, err := h.st.GetJob(h.ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if after.State != store.JobInProgress {
		t.Fatalf("job state = %q; GitHub has not said the job is over, so Zoomies must not", after.State)
	}
	if !strings.Contains(after.RunnerFault, r.Name) || !strings.Contains(after.RunnerFault, "code 137") {
		t.Fatalf("runner fault = %q, want the runner named and the agent's message kept", after.RunnerFault)
	}

	events := h.timeline(job.ID)
	last := events[len(events)-1]
	if last.Kind != store.JobEventRunnerLost || last.Source != sourceAgent || last.RunnerID != r.ID {
		t.Fatalf("last timeline entry = %+v, want runner_lost from the agent", last)
	}

	// The agent reports the same exit again -- a runner report and then the
	// task result that carries it both arrive -- and the failed runner is
	// terminal, so the second report is dropped by the state machine. The
	// job must still carry exactly one line and one count for it.
	before := h.runnerLostTotal()
	if before != 1 {
		t.Fatalf("zoomies_jobs_runner_lost_total = %v after one lost runner, want 1", before)
	}
	h.c.noteRunnerLost(h.ctx, r, sourceAgent, "runner exited with code 137: reported again")
	if got := h.timeline(job.ID); len(got) != len(events) {
		t.Fatalf("a repeated report added a timeline entry: %v", kindsOfEvents(got))
	}
	if after := h.runnerLostTotal(); after != before {
		t.Fatalf("a repeated report counted the runner as lost again: %v -> %v", before, after)
	}
	if again, _ := h.st.GetJob(h.ctx, job.ID); !strings.Contains(again.RunnerFault, "memory limit") {
		t.Fatalf("a repeated report replaced the first fault: %q", again.RunnerFault)
	}

	if codes := h.problemCodes(); !slices.Contains(codes, "jobs.runner_lost") {
		t.Fatalf("problems = %v, want jobs.runner_lost", codes)
	}
	failed, total, err := h.st.ListJobs(h.ctx, store.JobFilter{FailedOnly: true}, store.Page{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != 1 || failed[0].ID != job.ID {
		t.Fatalf("failed jobs = %+v, want the orphaned job before GitHub reports it", failed)
	}

	// GitHub catches up, and the completed entry says the fleet's part in it.
	h.deliverJob(jobEvent{Action: "completed", JobID: 9009, Name: "test", Workflow: "CI", Labels: labels,
		RunnerName: r.Name, Conclusion: "failure"})
	events = h.timeline(job.ID)
	done := events[len(events)-1]
	if done.Kind != store.JobEventCompleted || !strings.Contains(done.Message, "the runner had stopped under it") {
		t.Fatalf("completed entry = %+v", done)
	}
	if after, _ = h.st.GetJob(h.ctx, job.ID); after.RunnerFault == "" {
		t.Fatal("the completed delivery erased the fault")
	}
}

// An ephemeral runner exits after every job, and GitHub's completed delivery
// races the agent noticing. A clean exit is never a fault, whichever arrives
// first -- or every finished job would be reported as a runner lost.
func TestACleanExitUnderAJobIsNotAFault(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 1010, labels)

	err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, State: store.RunnerRemoved, Message: "ephemeral runner exited cleanly after its job", ObservedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}
	after, _ := h.st.GetJob(h.ctx, job.ID)
	if after.RunnerFault != "" {
		t.Fatalf("a clean exit was recorded as a fault: %q", after.RunnerFault)
	}
	for _, e := range h.timeline(job.ID) {
		if e.Kind == store.JobEventRunnerLost {
			t.Fatalf("a clean exit wrote a runner_lost entry: %+v", e)
		}
	}
	if codes := h.problemCodes(); slices.Contains(codes, "jobs.runner_lost") {
		t.Fatalf("problems = %v; a clean exit is not a lost runner", codes)
	}
}

// Force-removing a busy runner is the operator's decision, and the job it was
// running is the one that pays for it. The job's timeline should say so rather
// than leaving GitHub's "failure" to be blamed on the tests.
func TestForceRemovingABusyRunnerIsRecordedOnItsJob(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 1111, labels)

	if _, err := h.c.RemoveRunner(h.ctx, r.ID, "removed by an operator", true); err != nil {
		t.Fatalf("RemoveRunner: %v", err)
	}
	after, _ := h.st.GetJob(h.ctx, job.ID)
	if !strings.Contains(after.RunnerFault, "removed by an operator") {
		t.Fatalf("runner fault = %q, want the operator's reason", after.RunnerFault)
	}
	// The agent did not see this one go; the controller did it, and the
	// timeline says so rather than blaming the host.
	events := h.timeline(job.ID)
	if last := events[len(events)-1]; last.Kind != store.JobEventRunnerLost || last.Source != sourceController {
		t.Fatalf("last timeline entry = %+v, want runner_lost from the controller", last)
	}

	// Without force the runner drains, the job keeps running, and nothing is
	// written against it.
	h2 := newHarness(t)
	_, _, host2 := h2.fleet()
	job2, r2 := startJobOnRunner(t, h2, host2.ID, 1212, labels)
	if _, err := h2.c.RemoveRunner(h2.ctx, r2.ID, "removed by an operator", false); err != nil {
		t.Fatalf("RemoveRunner without force: %v", err)
	}
	if after2, _ := h2.st.GetJob(h2.ctx, job2.ID); after2.RunnerFault != "" {
		t.Fatalf("a drain was recorded as a fault: %q", after2.RunnerFault)
	}
}

func TestCompletionMessagesReadAsSentences(t *testing.T) {
	started := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	done := started.Add(3*time.Minute + 10*time.Second)
	base := store.Job{State: store.JobCompleted, StartedAt: &started, CompletedAt: &done}
	cases := []struct {
		name string
		mod  func(j *store.Job)
		want string
	}{
		{"success", func(j *store.Job) { j.Conclusion = "success" }, "succeeded after 3m 10s"},
		{"failure without steps", func(j *store.Job) { j.Conclusion = "failure" }, "failed after 3m 10s"},
		{"failure at a step", func(j *store.Job) {
			j.Conclusion = "failure"
			j.Steps = store.JobSteps{{Number: 1, Name: "Checkout", Conclusion: "success"}, {Number: 2, Name: "Run tests", Conclusion: "failure"}}
		}, "failed at step 2, Run tests, after 3m 10s"},
		{"timed out", func(j *store.Job) { j.Conclusion = "timed_out" }, "timed out after 3m 10s"},
		{"cancelled", func(j *store.Job) { j.Conclusion = "cancelled" }, "cancelled after 3m 10s"},
		{"runner lost", func(j *store.Job) { j.Conclusion = "failure"; j.RunnerFault = "x" }, "the runner had stopped under it"},
	}
	for _, tc := range cases {
		j := base
		tc.mod(&j)
		if got := completionMessage(&j); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, got, tc.want)
		}
	}
	if got := roundDuration(90 * time.Minute); got != "1h 30m" {
		t.Errorf("roundDuration(90m) = %q", got)
	}
}
