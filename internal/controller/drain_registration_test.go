package controller

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// retireIdleRunner sets up the drain the scheduler decides for a runner it
// reads as idle -- here by disabling its pool, the plainest way to ask for one
// -- with the runner registered at GitHub, and applies it.
func (h *harness) retireIdleRunner(t *testing.T, prepare func(name string)) (*store.Runner, *store.Host) {
	t.Helper()
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	h.gh.AddRunner(idle.Name, pool.Labels)
	if prepare != nil {
		prepare(idle.Name)
	}
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
		t.Fatalf("the plan %+v does not drain the idle runner, so this test proves nothing", plan.Actions)
	}
	h.c.apply(h.ctx, snap, plan)
	return h.runnerByID(t, idle.ID), host
}

func (h *harness) registered(name string) bool {
	return slices.ContainsFunc(h.gh.Runners(), func(r github.Runner) bool { return r.Name == name })
}

// The row is what this controller last heard, and on a polling-only fleet that
// can be a whole sweep old. GitHub may have given the runner a job since, and
// the stop a drain sends is a SIGINT, which the runner answers by cancelling
// that job. GitHub knows, and refuses to give up the registration; the drain
// has to take that as its answer and leave the runner alone.
func TestADrainIsRefusedWhenGitHubHasGivenTheRunnerAJobNothingHereHasSeen(t *testing.T) {
	h := newHarness(t)
	after, host := h.retireIdleRunner(t, func(name string) { h.gh.SetRunnerBusy(name, true) })

	if after.State != store.RunnerIdle {
		t.Fatalf("runner = %s, want it left idle for the next sweep to find its job", after.State)
	}
	if h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Fatal("a stop was sent to a runner GitHub says is running a job")
	}
	if !h.registered(after.Name) {
		t.Fatal("the registration of a runner with a job on it was removed")
	}
}

// A runner that is going away must not still be able to be handed work, so a
// drain that goes ahead leaves no registration behind it. That the registration
// goes first is what the two refusals around this test pin: a stop sent before
// the answer came back would reach the busy runner and the unanswered one.
func TestADrainThatGoesAheadLeavesNoRegistrationToAssignAJobTo(t *testing.T) {
	h := newHarness(t)
	after, host := h.retireIdleRunner(t, nil)

	if h.registered(after.Name) {
		t.Fatal("the runner was stopped while GitHub could still assign it a job")
	}
	if after.State != store.RunnerDraining {
		t.Fatalf("runner = %s, want it draining", after.State)
	}
	if !h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Fatal("nothing was asked to stop the drained runner")
	}
}

// No answer is not permission. A runner that could not be deregistered can
// still be given a job, so it is left running and the next pass asks again.
func TestADrainWaitsWhenGitHubCannotBeAsked(t *testing.T) {
	h := newHarness(t)
	after, host := h.retireIdleRunner(t, func(string) {
		h.gh.SetMethodError(http.MethodDelete, "/actions/runners/", http.StatusInternalServerError, "unavailable")
	})

	if after.State != store.RunnerIdle {
		t.Fatalf("runner = %s, want it left idle until GitHub answers", after.State)
	}
	if h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Fatal("a stop was sent to a runner whose registration could not be withdrawn")
	}
	if !h.registered(after.Name) {
		t.Fatal("the registration is gone although the delete failed")
	}
}

// A rate limit met while looking a registration up has to stand the
// installation down like any other, or every drain of every pass asks again,
// under the scheduling lock, for an answer GitHub has said it will not give.
func TestADrainStandsDownWhenLookingUpARegistrationIsRateLimited(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	first := h.runnerRow(pool, host, store.RunnerIdle)
	second := h.runnerRow(pool, host, store.RunnerIdle)
	h.gh.AddRunner(first.Name, pool.Labels)
	h.gh.AddRunner(second.Name, pool.Labels)
	h.gh.SetRateLimit(5000, 0, time.Now().Add(time.Hour))
	h.gh.SetMethodError(http.MethodGet, "/actions/runners", http.StatusForbidden, "API rate limit exceeded")

	if _, err := h.c.drainRunner(h.ctx, first, "idle", pool, true); err == nil {
		t.Fatal("a drain went ahead although the registration could not be looked up")
	}
	if !h.c.githubHeld(inst.ID, time.Now()) {
		t.Fatal("the installation was not stood down after the lookup was rate-limited")
	}
	before := len(h.gh.Requests())
	if _, err := h.c.drainRunner(h.ctx, second, "idle", pool, true); err == nil {
		t.Fatal("a drain went ahead while the installation was stood down")
	}
	if asked := len(h.gh.Requests()) - before; asked != 0 {
		t.Fatalf("a second drain made %d GitHub requests during the stand-down", asked)
	}
	for _, r := range []*store.Runner{first, second} {
		if got := h.runnerByID(t, r.ID); got.State != store.RunnerIdle {
			t.Fatalf("runner %s = %s, want it left idle", r.Name, got.State)
		}
	}
}

// A pass holds the scheduling lock for every drain it applies, and each drain
// now waits on GitHub. Disabling a pool with many idle runners must not turn
// into one pass that waits on GitHub that many times; the rest are decided
// again on the next pass, so none is lost.
func TestAPassWithdrawsOnlyAFewRegistrationsAndLeavesTheRestForTheNext(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	total := maxWithdrawalsPerPass + 4
	ids := make([]string, 0, total)
	for range total {
		r := h.runnerRow(pool, host, store.RunnerIdle)
		h.gh.AddRunner(r.Name, pool.Labels)
		ids = append(ids, r.ID)
	}
	pool.Enabled = false
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	draining := func() int {
		n := 0
		for _, id := range ids {
			if h.runnerByID(t, id).State == store.RunnerDraining {
				n++
			}
		}
		return n
	}
	pass := func() {
		snap, err := h.c.snapshot(h.ctx)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		h.c.apply(h.ctx, snap, scheduler.Decide(snap))
	}

	pass()
	if got := draining(); got != maxWithdrawalsPerPass {
		t.Fatalf("one pass drained %d runners, want the cap of %d", got, maxWithdrawalsPerPass)
	}
	pass()
	if got := draining(); got != total {
		t.Fatalf("after a second pass %d runners are draining, want all %d", got, total)
	}
}
