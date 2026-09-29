package controller

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
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
	h.c.lifecycleCalls.Wait()
	first := h.runners()
	tasks := len(h.tasksFor(host.ID))

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
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

// Minting a credential is a GitHub round trip. It must never be able to hold
// up the scheduling pass: a pool resized during a GitHub slowdown would
// otherwise stall every other pool and installation's scheduling for as long
// as GitHub took to answer -- the same argument that moved a slow provider
// call, and capacity-demand delivery, out of their own locks.
func TestASlowCredentialMintDoesNotHoldTheReconcilePass(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	h.gh.SetDelay("", "generate-jitconfig", 2*time.Second)
	h.deliverJob(jobEvent{Action: "queued", JobID: 1, Labels: []string{"self-hosted", "linux", "x64", "demo"}})

	started := time.Now()
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("a scheduling pass took %s while a credential mint was in flight; reconcileMu is held across it", took)
	}

	// The row exists and already counts toward the pool's size, which is what
	// stops the next pass from deciding to create a second one for the same
	// demand while this one is still minting.
	r := h.onlyRunner()
	if r.State != store.RunnerProvisioning {
		t.Fatalf("runner state = %q before the mint answers, want %q", r.State, store.RunnerProvisioning)
	}

	h.c.lifecycleCalls.Wait()
	after := h.onlyRunner()
	if after.GitHubRunnerID == 0 {
		t.Fatal("the detached mint never finished: the runner has no GitHub runner ID")
	}
	if !h.hasTaskOfKind(after.HostID, agent.TaskCreateRunner) {
		t.Fatal("the detached mint never enqueued the create task")
	}
}

// Deleting a GitHub registration is the other slow call in a reconcile pass,
// and the more consequential one: draining a pool of many runners issues one
// per runner in the same pass, so this is the call whose sequential cost a
// bulk drain actually pays.
func TestASlowRegistrationDeleteDoesNotHoldTheCaller(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if err := h.st.SetRunnerGitHubID(h.ctx, r.ID, 999); err != nil {
		t.Fatalf("SetRunnerGitHubID: %v", err)
	}
	h.gh.SetDelay(http.MethodDelete, "/actions/runners/", 2*time.Second)

	started := time.Now()
	if err := h.c.removeRunnerID(h.ctx, r.ID, "test removal", pool); err != nil {
		t.Fatalf("removeRunnerID: %v", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("removeRunnerID took %s while the registration delete was in flight; it is not detached", took)
	}

	h.c.lifecycleCalls.Wait()
	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerRemoved {
		t.Fatalf("runner state = %q once the detached removal finished, want %q", after.State, store.RunnerRemoved)
	}
}

// The pass that finds a removal already under way must not start a second
// one: the row does not read as RunnerRemoved until the first detached call
// finishes, so without a claim the next pass would redecide the identical
// ActionRemove and delete the same registration twice.
func TestASlowRemovalDoesNotStartASecondOne(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if err := h.st.SetRunnerGitHubID(h.ctx, r.ID, 999); err != nil {
		t.Fatalf("SetRunnerGitHubID: %v", err)
	}
	h.gh.SetDelay(http.MethodDelete, "/actions/runners/", 500*time.Millisecond)

	for range 3 {
		if err := h.c.removeRunnerID(h.ctx, r.ID, "test removal", pool); err != nil {
			t.Fatalf("removeRunnerID: %v", err)
		}
	}
	h.c.lifecycleCalls.Wait()

	deletes := 0
	for _, req := range h.gh.Requests() {
		if strings.HasPrefix(req, http.MethodDelete+" ") && strings.Contains(req, "/actions/runners/") {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("GitHub saw %d registration deletes for one runner claimed three times, want 1", deletes)
	}
	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerRemoved {
		t.Fatalf("runner state = %q, want %q", after.State, store.RunnerRemoved)
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

	// Ten seconds rather than two: this asserts that a pass happens, not how
	// fast. CI runs this package on a shared runner where it has taken 173
	// seconds against twelve locally, and at that ratio a two-second budget is
	// measuring the runner rather than the controller. What the test is
	// actually for -- that fifty nudges coalesce into one pass -- is the count
	// below, and that is unchanged.
	eventually(t, 10*time.Second, "the first reconcile pass", func() bool {
		return h2.c.passes.Load() >= 1
	})
	base := h2.c.passes.Load()
	for range 50 {
		h2.c.Nudge()
	}
	eventually(t, 10*time.Second, "the nudged reconcile pass", func() bool {
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

	got, err := h.c.DrainRunner(h.ctx, r.ID, "", true)
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
	h.c.lifecycleCalls.Wait()

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
	h.c.lifecycleCalls.Wait()
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

// A runner GitHub still calls busy is left alone, however long Zoomies has
// known it is gone: GitHub refuses to delete a runner it believes is running
// a job with no override, so retrying into that refusal would only spend an
// API call reproducing a result the listing already gave for free. Whether
// this is a few seconds of GitHub's own bookkeeping catching up or a
// registration genuinely stuck busy is not something the reap loop can tell,
// or needs to: either way there is nothing to do here but wait and recheck
// next pass.
func TestReapLeavesABusyRegistrationAlone(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRemoved)
	h.gh.AddRunner(r.Name, pool.Labels)
	h.gh.SetRunnerBusy(r.Name, true)
	if err := h.c.confirmCleanup(h.ctx, r.ID, true); err != nil {
		t.Fatalf("confirmCleanup: %v", err)
	}
	h.advance(24 * time.Hour)

	h.c.reap(h.ctx)

	if len(h.gh.Runners()) != 1 {
		t.Fatal("a runner GitHub still calls busy must not be deleted")
	}
	if countRequests(h, "DELETE") != 0 {
		t.Fatal("reap must not spend a delete call on a runner the listing already reported busy")
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
		Name:      store.NewRunnerName(pool),
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

	if _, err := h.c.RemoveRunner(h.ctx, r.ID, "test", true, false); err != nil {
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

// A pool that gives its jobs a daemon runs the stock image's Docker variant,
// whatever the row says: the API writes the swap into new pools, but a pool
// saved before it did still names the stock image, and a runner made from
// that image has a daemon it cannot reach. The row is left alone here -- the
// migration that rewrites it is the store's -- and the runner gets the image
// that works.
func TestAPoolThatGivesJobsADaemonRunsTheDockerImage(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.DockerMode = store.DockerDinD
	pool.Image = "ghcr.io/eyupio/zoomies-runner:main"
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	host := h.host("vm-1")

	h.deliverJob(jobEvent{Action: "queued", JobID: 11, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	const want = "ghcr.io/eyupio/zoomies-runner-docker:main"
	r := h.onlyRunner()
	if r.Image != want {
		t.Fatalf("runner image = %q, want %q: a daemon with no client is the failure this exists to prevent", r.Image, want)
	}
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil || task.Spec.Image != want {
		t.Fatalf("create task image = %+v, want %q", task.Spec, want)
	}

	// Warming the pool pulls the image the runners will use, not the one the
	// row names: pulling the other one warms nothing.
	if _, err := h.c.PrewarmPool(h.ctx, pool); err != nil {
		t.Fatalf("PrewarmPool: %v", err)
	}
	prewarm := h.taskOfKind(host.ID, agent.TaskPrewarmImage)
	if prewarm.Image != want {
		t.Fatalf("prewarm image = %q, want %q", prewarm.Image, want)
	}
}

// The one place that sees a pool with no image of its own is the controller,
// which falls back to github.runner_image; at its stock default that fallback
// needs the same swap, or a daemon pool made by hand would get the image with
// no client.
func TestAPoolOnTheDefaultImageGetsTheDockerVariantWhenItAsksForADaemon(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.DockerMode = store.DockerHostSocket
	pool.Image = ""
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	host := h.host("vm-1")

	h.deliverJob(jobEvent{Action: "queued", JobID: 12, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	want := config.RunnerImageFor(config.DefaultRunnerImage, true)
	if want == config.DefaultRunnerImage {
		t.Fatal("the default image is not swapped at all; this test has nothing to check")
	}
	if r := h.onlyRunner(); r.Image != want {
		t.Fatalf("runner image = %q, want the default's Docker variant %q", r.Image, want)
	}
	if task := h.taskOfKind(host.ID, agent.TaskCreateRunner); task.Spec == nil || task.Spec.Image != want {
		t.Fatalf("create task image = %+v, want %q", task.Spec, want)
	}
}

// countRequests is how many of the fake's logged requests contain fragment.
func countRequests(h *harness, fragment string) int {
	n := 0
	for _, r := range h.gh.Requests() {
		if strings.Contains(r, fragment) {
			n++
		}
	}
	return n
}

// seedOrphans gives GitHub n registrations the reap is entitled to delete: a
// Zoomies-minted name, and a row of ours that is already terminal.
func seedOrphans(h *harness, pool *store.Pool, host *store.Host, n int) {
	h.t.Helper()
	for range n {
		r := h.runnerRow(pool, host, store.RunnerProvisioning)
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "done"); err != nil {
			h.t.Fatalf("TransitionRunner: %v", err)
		}
		h.gh.AddRunner(r.Name, []string{"linux"})
	}
}

// The reap walks an installation's whole orphan list. Once GitHub has said the
// quota is gone, every remaining delete is refused the same way -- and each
// refusal is another call against a quota that is already spent, which is how
// one exhausted window becomes two. It stops on the first, and stands the
// installation down for as long as GitHub asked.
//
// Only the delete is refused: the listing shares its path, and refusing that
// too would test the rule above this one instead of this one.
func TestTheReapStopsAtTheFirstRefusedDeleteInsteadOfSpendingTheRest(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	seedOrphans(h, pool, host, 3)

	// A 429 with the quota otherwise healthy, which is GitHub's secondary rate
	// limit. Exhausting the primary one instead would prove less than it looks:
	// go-github refuses a call locally once a response has told it the quota is
	// spent, so the calls this rule saves would never leave the process anyway.
	// The secondary limit has no such guard, and neither does the log.
	h.gh.SetRateLimit(5000, 4999, time.Now().Add(time.Hour))
	h.gh.SetMethodError(http.MethodDelete, "/actions/runners/",
		http.StatusTooManyRequests, "You have exceeded a secondary rate limit")

	h.c.reap(h.ctx)

	if got := countRequests(h, "DELETE"); got != 1 {
		t.Fatalf("the reap made %d delete calls after the first was refused for quota, want 1", got)
	}
	if !h.c.githubHeld(inst.ID, time.Now()) {
		t.Fatal("the installation was not stood down after GitHub refused it for quota")
	}
	// The registrations are still there, which is the point: they are reaped
	// on a later pass rather than on a quota that is gone.
	if len(h.gh.Runners()) != 3 {
		t.Fatalf("GitHub holds %d registrations, want 3", len(h.gh.Runners()))
	}
}

// The listing is refused for quota just as readily as the delete, and it is
// the first call the reap makes. Standing down on it is what stops the sweep
// asking again a minute later for the whole of an exhausted window.
func TestTheReapStandsDownWhenTheListingIsRefusedForQuota(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	seedOrphans(h, pool, host, 2)

	h.gh.SetRateLimit(5000, 4999, time.Now().Add(time.Hour))
	h.gh.SetMethodError(http.MethodGet, "/actions/runners",
		http.StatusTooManyRequests, "You have exceeded a secondary rate limit")

	h.c.reap(h.ctx)

	if got := countRequests(h, "DELETE"); got != 0 {
		t.Fatalf("the reap made %d delete calls without a listing to decide from", got)
	}
	if !h.c.githubHeld(inst.ID, time.Now()) {
		t.Fatal("a refused listing left the installation open to being asked again at once")
	}
}

// An installation already standing down is not asked again until the hold runs
// out. The poller earns holds too, and rediscovering the same refusal from the
// reap costs exactly the call the hold exists to save.
func TestTheReapSkipsAnInstallationThatIsStandingDown(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	seedOrphans(h, pool, host, 1)

	h.c.holdGitHub(inst.ID, time.Now().Add(10*time.Minute))
	before := len(h.gh.Requests())

	h.c.reap(h.ctx)

	if got := len(h.gh.Requests()); got != before {
		t.Fatalf("the reap spent %d calls on an installation that is standing down", got-before)
	}
	if len(h.gh.Runners()) != 1 {
		t.Fatal("the registration should still be there; the reap had no business deleting it yet")
	}
}

// TestDrainingABusyRunnerNeedsConfirming covers what a drain actually costs.
//
// A drain reads as the gentle option, and the CLI and the docs both called it
// one. It is not: the stop task carries agent.DefaultStopTimeout, so the runner
// gets five minutes and is then killed. A twenty-minute job drained at minute
// one dies at minute six and GitHub marks it failed. That is the behaviour an
// operator draining a host for maintenance needs -- the machine has to actually
// empty -- but it must not be something they get by accident.
func TestDrainingABusyRunnerNeedsConfirming(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	busy := h.runnerRow(pool, host, store.RunnerBusy)

	if _, err := h.c.DrainRunner(h.ctx, busy.ID, "operator asked", false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("DrainRunner on a busy runner = %v, want ErrConfirmationRequired", err)
	}
	// And it did nothing: no state change, no stop task.
	after, err := h.st.GetRunner(h.ctx, busy.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerBusy {
		t.Errorf("runner state = %q, want busy: the refusal should have changed nothing", after.State)
	}
	if h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Error("the refusal still queued a stop task, so the job is being killed anyway")
	}

	// Confirmed, it goes through.
	if _, err := h.c.DrainRunner(h.ctx, busy.ID, "operator asked", true); err != nil {
		t.Fatalf("DrainRunner confirmed: %v", err)
	}
	if !h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Error("a confirmed drain did not queue a stop task")
	}
}

// TestDrainingAnIdleRunnerNeedsNoConfirmation is the limit of the rule: there is
// no job to lose, so nothing to accept.
func TestDrainingAnIdleRunnerNeedsNoConfirmation(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)

	if _, err := h.c.DrainRunner(h.ctx, idle.ID, "scaling down", false); err != nil {
		t.Fatalf("draining an idle runner asked for a confirmation it should not need: %v", err)
	}
}

// TestAnUnforcedRemoveOfABusyRunnerNeedsConfirming covers the drain that does
// not call itself one. DELETE /runners/{id} without force is a drain when the
// runner is busy, and kills the job on the same five-minute timer.
func TestAnUnforcedRemoveOfABusyRunnerNeedsConfirming(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	busy := h.runnerRow(pool, host, store.RunnerBusy)

	if _, err := h.c.RemoveRunner(h.ctx, busy.ID, "", false, false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("unforced RemoveRunner on a busy runner = %v, want ErrConfirmationRequired", err)
	}
	// force is the operator having already accepted it, and must not ask again.
	if _, err := h.c.RemoveRunner(h.ctx, busy.ID, "", true, false); err != nil {
		t.Fatalf("a forced remove asked for confirmation: %v", err)
	}
}

// The poller and the reap already stand down from an installation GitHub is
// rate-limiting; the scheduler used to ask it for a JIT configuration on every
// pass anyway. Each request was refused, each refusal became a failed runner
// row, and the pool then backed off from its own failures on top of the hold
// GitHub asked for -- with a Runners page full of rows saying the same thing.
func TestAHeldInstallationMintsNothingAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	inst, pool, _ := h.fleet()
	h.deliverJob(jobEvent{Action: "queued", JobID: 61, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	// A minute, not longer: the clock is moved past it below, and a host
	// that has not been heard from for ninety seconds is one the scheduler
	// places nothing on, which would look exactly like the hold still holding.
	h.c.holdGitHub(inst.ID, h.c.Now().Add(time.Minute))

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.runners(); len(got) != 0 {
		t.Fatalf("a held installation got %d runner rows, want none: %+v", len(got), got[0])
	}
	if got := len(h.gh.Runners()); got != 0 {
		t.Fatalf("GitHub was asked to register %d runners during a hold", got)
	}
	if codes := h.problemCodes(); !slices.Contains(codes, "pool.github_rate_limited") {
		t.Fatalf("problems = %v, want pool.github_rate_limited so the waiting job is explained", codes)
	}
	if p := h.problem(t, "pool.github_rate_limited"); p.TargetID != pool.ID {
		t.Fatalf("the problem targets %s, want the pool %s", p.TargetID, pool.ID)
	}

	// The hold lifts and the next pass creates.
	h.advance(61 * time.Second)
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile after the hold: %v", err)
	}
	if got := h.runners(); len(got) != 1 {
		t.Fatalf("got %d runners after the hold lifted, want 1", len(got))
	}
	if codes := h.problemCodes(); slices.Contains(codes, "pool.github_rate_limited") {
		t.Fatalf("pool.github_rate_limited is still raised after the hold lifted: %v", codes)
	}
}

// The scheduler only proposes a drain for a runner that was not busy in its
// snapshot, but apply() runs the plan against live rows, and GitHub's
// in_progress webhook is handled without the reconcile lock. A runner that
// takes a job in between used to be drained anyway -- busy to draining is a
// legal move -- which queued a five-minute stop and killed the job that had
// just started. The plan is decided first and the job started after it, as
// the webhook would, and the drain must then be skipped rather than applied.
func TestAPlannedDrainIsSkippedWhenTheRunnerHasSinceTakenAJob(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	pool.Enabled = false
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	snap, err := h.c.snapshot(h.ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	plan := scheduler.Decide(snap)
	if !slices.ContainsFunc(plan.Actions, func(a scheduler.Action) bool {
		return a.Kind == scheduler.ActionDrain && a.RunnerID == idle.ID
	}) {
		t.Fatalf("the plan %+v does not drain the idle runner of a disabled pool, so this test proves nothing", plan.Actions)
	}

	job := h.queuedJob(t, nil, []string{"self-hosted", "linux", "x64", "demo"})
	if _, _, err := h.st.StartRunnerJob(h.ctx, idle.ID, job.ID, ""); err != nil {
		t.Fatalf("StartRunnerJob: %v", err)
	}
	h.c.apply(h.ctx, snap, plan)

	after, err := h.st.GetRunner(h.ctx, idle.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerBusy || after.CurrentJobID != job.ID {
		t.Fatalf("runner = %s with job %q, want it left busy with %s: the drain would have killed a job that had just started",
			after.State, after.CurrentJobID, job.ID)
	}
	if h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Error("a stop task was queued for a runner that had taken a job")
	}
	evs, err := h.st.ListScalingEvents(h.ctx, pool.ID, 10)
	if err != nil {
		t.Fatalf("ListScalingEvents: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("scaling events = %+v, want none: nothing was drained", evs)
	}
}

// The same window for a runner the scheduler meant to fail as stuck: one that
// took a job while the plan was being applied is working, and failing it would
// end the job under it.
func TestAPlannedFailureIsSkippedWhenTheRunnerHasSinceTakenAJob(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	starting := h.runnerRow(pool, host, store.RunnerRegistering)

	snap, err := h.c.snapshot(h.ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	plan := scheduler.Plan{
		Pools: []scheduler.PoolPlan{{PoolID: pool.ID}},
		Actions: []scheduler.Action{{Kind: scheduler.ActionFail, PoolID: pool.ID, PoolName: pool.Name,
			RunnerID: starting.ID, Reason: "did not register within the provisioning timeout"}},
	}

	job := h.queuedJob(t, nil, []string{"self-hosted", "linux", "x64", "demo"})
	if _, _, err := h.st.StartRunnerJob(h.ctx, starting.ID, job.ID, ""); err != nil {
		t.Fatalf("StartRunnerJob: %v", err)
	}
	h.c.apply(h.ctx, snap, plan)

	after, err := h.st.GetRunner(h.ctx, starting.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerBusy {
		t.Fatalf("runner state = %s, want busy: failing it would end the job it just took", after.State)
	}
}
