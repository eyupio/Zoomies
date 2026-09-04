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
		Name:      runnerNamePrefix + pool.Name + "-" + store.NewSecret(4),
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
