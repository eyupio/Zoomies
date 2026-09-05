package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

// Reconciling a fleet that is already where it should be must do nothing at
// all. A pass that created a runner every ten seconds would be the worst kind
// of bug: expensive, invisible, and self-inflicted.
func TestReconcileIsIdempotent(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	h.deliverJob(jobEvent{Action: "queued", JobID: 1, Labels: []string{"self-hosted", "linux", "x64", "demo"}})

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	first := h.runners()
	tasks := len(h.tasksFor(host.ID))

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := h.runners(); len(got) != len(first) {
		t.Fatalf("second pass created runners: %d -> %d", len(first), len(got))
	}
	if got := len(h.tasksFor(host.ID)); got != tasks {
		t.Fatalf("second pass queued more tasks: %d -> %d", tasks, got)
	}
	if got := len(h.gh.Runners()); got != 1 {
		t.Fatalf("registered %d runners with GitHub, want 1", got)
	}
}

// Many nudges in quick succession must cost one reconcile, not one each. The
// channel holds a single token, which is the whole mechanism.
func TestNudgesCoalesce(t *testing.T) {
	h := newHarness(t)
	for range 50 {
		h.c.Nudge()
	}
	if got := len(h.c.nudges); got != 1 {
		t.Fatalf("50 nudges left %d tokens queued, want 1", got)
	}

	// And through the loop: with the timer set an hour out, the only pass after
	// the loop has settled is the one the burst of nudges asks for. A second
	// controller is used so the token left above does not count towards it.
	h2 := newHarness(t)
	h2.cfg.Scheduler.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h2.c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { h2.c.Stop(context.Background()) })

	eventually(t, 2*time.Second, "the first reconcile pass", func() bool {
		return h2.c.passes.Load() >= 1
	})
	base := h2.c.passes.Load()
	for range 50 {
		h2.c.Nudge()
	}
	eventually(t, 2*time.Second, "the nudged reconcile pass", func() bool {
		return h2.c.passes.Load() > base
	})
	time.Sleep(100 * time.Millisecond)
	// One extra allows for the single nudge that can legitimately land while
	// the pass it triggered is still running; fifty would mean no coalescing.
	if got := h2.c.passes.Load(); got > base+2 {
		t.Fatalf("%d reconcile passes for 50 nudges, want at most %d", got-base, 2)
	}
}

// A pool that is disabled drains what it can, but a busy runner keeps its job:
// nothing in the scheduler path ever interrupts work in progress.
func TestSchedulerDrainsIdleRunnersAndNeverBusyOnes(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()

	idle := h.runnerRow(pool, host, store.RunnerIdle)
	busy := h.runnerRow(pool, host, store.RunnerBusy)

	pool.Enabled = false
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	after, err := h.st.GetRunner(h.ctx, idle.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerDraining {
		t.Fatalf("idle runner state = %q, want %q", after.State, store.RunnerDraining)
	}
	stillBusy, err := h.st.GetRunner(h.ctx, busy.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if stillBusy.State != store.RunnerBusy {
		t.Fatalf("busy runner state = %q; draining it would have interrupted a job", stillBusy.State)
	}

	var stopped []string
	for _, task := range h.tasksFor(host.ID) {
		if task.Kind == agent.TaskStopRunner {
			stopped = append(stopped, task.RunnerID)
		}
	}
	if !slices.Contains(stopped, idle.ID) {
		t.Fatalf("no stop task for the drained runner; queued stops: %v", stopped)
	}
	if slices.Contains(stopped, busy.ID) {
		t.Fatal("a stop task was queued for a busy runner")
	}

	// A scaling event explains what happened, in the scheduler's words.
	evs, err := h.st.ListScalingEvents(h.ctx, pool.ID, 10)
	if err != nil {
		t.Fatalf("ListScalingEvents: %v", err)
	}
	if len(evs) != 1 || !strings.Contains(evs[0].Reason, "pool is disabled") {
		t.Fatalf("scaling events = %+v, want one naming the disabled pool", evs)
	}
}

// Draining through the API is allowed to touch a busy runner, because a drain
// lets the job finish; it is deleting that would interrupt one.
func TestDrainRunnerQueuesAStopTask(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	got, err := h.c.DrainRunner(h.ctx, r.ID, "")
	if err != nil {
		t.Fatalf("DrainRunner: %v", err)
	}
	if got.State != store.RunnerDraining {
		t.Fatalf("state = %q, want %q", got.State, store.RunnerDraining)
	}
	task := h.taskOfKind(host.ID, agent.TaskStopRunner)
	if task.RunnerID != r.ID {
		t.Fatalf("stop task is for %s, want %s", task.RunnerID, r.ID)
	}
	if task.StopTimeout != agent.DefaultStopTimeout {
		t.Fatalf("stop timeout = %s, want %s", task.StopTimeout, agent.DefaultStopTimeout)
	}
}

// When GitHub will not mint a credential the runner must be visibly failed,
// with GitHub's own words on it. A pool silently sitting one runner short is
// the failure this guards against.
func TestFailedJITMintMarksTheRunnerFailed(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	h.gh.SetError("generate-jitconfig", 403, "Resource not accessible by integration")

	h.deliverJob(jobEvent{Action: "queued", JobID: 7, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	r := h.onlyRunner()
	if r.State != store.RunnerFailed {
		t.Fatalf("runner state = %q, want %q", r.State, store.RunnerFailed)
	}
	for _, want := range []string{"GitHub would not register", "Resource not accessible by integration"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("runner message %q does not contain %q", r.Message, want)
		}
	}
	if h.hasTaskOfKind(r.HostID, agent.TaskCreateRunner) {
		t.Fatal("a create task was queued for a runner that has no credentials")
	}
}

// An ephemeral runner that has done its job is reported gone by its agent, and
// its GitHub registration has to go with it: an orphan quietly fills the
// organisation's runner list and can be assigned work that never runs.
func TestRemovedRunnerHasItsRegistrationReaped(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()

	h.deliverJob(jobEvent{Action: "queued", JobID: 8, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r := h.onlyRunner()
	if len(h.gh.Runners()) != 1 {
		t.Fatalf("GitHub holds %d registrations, want 1", len(h.gh.Runners()))
	}

	mustReport(t, h, host.ID, r.ID, store.RunnerRegistering)
	mustReport(t, h, host.ID, r.ID, store.RunnerRemoved)

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerRemoved {
		t.Fatalf("runner state = %q, want %q", after.State, store.RunnerRemoved)
	}

	h.c.reap(h.ctx)
	if got := h.gh.Runners(); len(got) != 0 {
		t.Fatalf("GitHub still holds %d registrations for a removed runner: %+v", len(got), got)
	}
}

// The reaper must never touch a registration that is not Zoomies' to delete.
func TestReapLeavesOtherToolsRunnersAlone(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	h.gh.AddRunner("some-other-tools-runner", []string{"linux"})
	// A Zoomies-shaped name that is online and that we have no row for: the
	// row may simply not have been written yet, so it stays. Only an offline
	// one, or one whose row says it is dead, may be deleted.
	h.gh.AddRunner("zoomies-linux-x64-unknown", []string{"linux"})

	h.c.reap(h.ctx)

	names := make([]string, 0, 2)
	for _, r := range h.gh.Runners() {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	want := []string{"some-other-tools-runner", "zoomies-linux-x64-unknown"}
	if !slices.Equal(names, want) {
		t.Fatalf("runners after reap = %v, want %v", names, want)
	}
}

// runnerRow writes a runner directly, for tests about what happens to one that
// already exists.
func (h *harness) runnerRow(pool *store.Pool, host *store.Host, state store.RunnerState) *store.Runner {
	h.t.Helper()
	now := time.Now()
	idle := now.Add(-time.Hour)
	r := &store.Runner{
		PoolID:    pool.ID,
		HostID:    host.ID,
		Name:      store.NewRunnerName(),
		State:     state,
		Ephemeral: pool.Ephemeral,
		Labels:    pool.Labels,
		StartedAt: &now,
	}
	if state == store.RunnerIdle {
		r.LastIdleAt = &idle
	}
	if err := h.st.CreateRunner(h.ctx, r); err != nil {
		h.t.Fatalf("CreateRunner: %v", err)
	}
	return r
}

// A runner that died on creation used to be replaced in the same pass that
// noticed: the agent's failure report nudged a pass, the pass removed the
// failed runner and created another, the agent failed that one too, and a pool
// with a bad image churned through a runner a second -- two GitHub API calls a
// time -- with the reason gone from the page before anyone could read it.
func TestARunnerThatDiesOnCreationIsNotReplacedUntilTheWaitIsOut(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.deliverJob(jobEvent{Action: "queued", JobID: 7001, Labels: []string{"self-hosted", "linux", "x64", "demo"}})

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	first := h.runners()
	if len(first) != 1 || first[0].State != store.RunnerProvisioning {
		t.Fatalf("after the first pass: %+v, want one provisioning runner", first)
	}

	batch, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
	if err != nil || len(batch.Tasks) != 1 {
		t.Fatalf("PollTasks: %v, %d tasks", err, len(batch.Tasks))
	}
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: batch.Tasks[0].ID, RunnerID: first[0].ID, OK: false,
		Error: "the docker backend could not create runner: No such image: sha256:9f2c",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	// The pass the failure provokes.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	after := h.runners()
	if len(after) != 1 {
		t.Fatalf("the failed runner was replaced straight away: %+v", after)
	}
	if after[0].State != store.RunnerFailed {
		t.Fatalf("the failed runner is %q; it should stay on the page as failed, with its reason", after[0].State)
	}
	if after[0].Message != "the docker backend could not create runner: No such image: sha256:9f2c" {
		t.Fatalf("the failure's reason was lost: %q", after[0].Message)
	}
	plan, _ := h.c.getLastPlan()
	if plan == nil || len(plan.Pools) != 1 || plan.Pools[0].Failing == "" {
		t.Fatalf("the plan does not say the pool is waiting: %+v", plan)
	}
	if !strings.Contains(plan.Pools[0].Failing, "No such image") || !strings.Contains(plan.Pools[0].Failing, "trying again in") {
		t.Fatalf("the wait is not explained in the plan: %q", plan.Pools[0].Failing)
	}
	codes := h.problemCodes()
	if !contains(codes, "pool.runners_failing") || !contains(codes, "runners.failed") {
		t.Fatalf("problems = %v, want the pool held back and the failed runner both reported", codes)
	}
	if n := len(h.gh.Runners()); n != 1 {
		t.Fatalf("GitHub was asked for %d registrations, want the one the first runner used", n)
	}
	_ = pool
}

// A queued row is demand for as long as it exists. A job whose completed
// delivery was lost -- repository deleted, run cancelled while the poller was
// rate-limited -- used to hold a pool's desired count up for ever, with a
// runner created for it, idling out, and created again. GitHub gives up on a
// job nobody picked up within a day; so does the controller.
func TestAQueuedJobGitHubNeverStartedIsRetiredAfterADay(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	h.c.clock = func() time.Time { return now }

	h.deliverJob(jobEvent{Action: "queued", JobID: 8001, QueuedAt: now.Add(-25 * time.Hour),
		Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	h.deliverJob(jobEvent{Action: "queued", JobID: 8002, QueuedAt: now.Add(-2 * time.Hour),
		Labels: []string{"self-hosted", "linux", "x64", "demo"}})

	h.c.expireStaleQueuedJobs(h.ctx, now)

	old, err := h.st.GetJobByGitHubID(h.ctx, 8001)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if old.State != store.JobCompleted || old.Conclusion != "stale" || old.CompletedAt == nil {
		t.Fatalf("the day-old job = state %q conclusion %q; want completed as stale", old.State, old.Conclusion)
	}
	recent, err := h.st.GetJobByGitHubID(h.ctx, 8002)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if recent.State != store.JobQueued {
		t.Fatalf("a two-hour-old job was retired: %q", recent.State)
	}
	queued, err := h.st.ListQueuedJobs(h.ctx)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued jobs after retiring = %d (%v), want the recent one only", len(queued), err)
	}
	timeline, err := h.st.ListJobEvents(h.ctx, old.ID)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	last := timeline[len(timeline)-1]
	if last.Kind != store.JobEventCompleted || !strings.Contains(last.Message, "presumed cancelled or lost") {
		t.Fatalf("the timeline does not say why the job was retired: %+v", last)
	}
}

// A job GitHub is holding for a deployment review is not demand. Recorded as
// queued, it had the scheduler start a runner that idled out and was started
// again on the next pass, for as long as the review took.
func TestAJobWaitingForApprovalIsNotDemandUntilItIsQueued(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}

	h.deliverJob(jobEvent{Action: "waiting", JobID: 8101, Labels: labels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(h.runners()); got != 0 {
		t.Fatalf("a waiting job had %d runners created for it", got)
	}
	j, err := h.st.GetJobByGitHubID(h.ctx, 8101)
	if err != nil || j.State != store.JobWaiting {
		t.Fatalf("job = %+v, %v; want it recorded as waiting", j, err)
	}

	// The approval arrives as an ordinary queued delivery, and moves the job
	// forward into demand.
	h.deliverJob(jobEvent{Action: "queued", JobID: 8101, Labels: labels})
	j, err = h.st.GetJobByGitHubID(h.ctx, 8101)
	if err != nil || j.State != store.JobQueued {
		t.Fatalf("job after approval = %+v, %v; want queued", j, err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(h.runners()); got != 1 {
		t.Fatalf("the approved job had %d runners created for it, want 1", got)
	}
}

// A runner registered with a registration token -- a non-ephemeral pool -- has
// no GitHub ID on its row, so removing it deleted nothing, and the container's
// own config.sh remove ran with a token that had usually expired. The
// registration is found by name instead.
func TestRemovingATokenRegisteredRunnerDeletesItsRegistrationByName(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if r.GitHubRunnerID != 0 {
		t.Fatalf("the fixture runner carries GitHub ID %d; this test is about a runner without one", r.GitHubRunnerID)
	}
	h.gh.AddRunner(r.Name, []string{"self-hosted", "linux"})
	h.gh.AddRunner("somebody-elses-runner", []string{"self-hosted"})

	if _, err := h.c.RemoveRunner(h.ctx, r.ID, "test", true); err != nil {
		t.Fatalf("RemoveRunner: %v", err)
	}
	names := make([]string, 0, 1)
	for _, gr := range h.gh.Runners() {
		names = append(names, gr.Name)
	}
	if !slices.Equal(names, []string{"somebody-elses-runner"}) {
		t.Fatalf("registrations after removal = %v, want only the runner that was never ours", names)
	}
}
