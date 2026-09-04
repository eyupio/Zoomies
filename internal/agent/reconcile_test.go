package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// track makes the agent believe it started a runner, which is what a create
// task would otherwise have done.
func track(a *Agent, runnerID string, handle backend.Handle, ephemeral bool) *tracked {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := &tracked{
		runnerID:  runnerID,
		name:      "runner-" + runnerID,
		kind:      store.BackendDocker,
		handle:    handle,
		ephemeral: ephemeral,
		createdAt: a.now(),
		state:     store.RunnerRegistering,
		phase:     backend.PhaseStarting,
	}
	a.runners[runnerID] = r
	return r
}

func exited(handle backend.Handle, runnerID string, code int) backend.Workload {
	return backend.Workload{
		Handle:   handle,
		Name:     "runner-" + runnerID,
		RunnerID: runnerID,
		Status:   backend.Status{Handle: handle, Phase: backend.PhaseExited, ExitCode: code},
	}
}

func running(handle backend.Handle, runnerID string) backend.Workload {
	return backend.Workload{
		Handle:   handle,
		Name:     "runner-" + runnerID,
		RunnerID: runnerID,
		Status:   backend.Status{Handle: handle, Phase: backend.PhaseRunning},
	}
}

func TestReconcileRemovesOrphansOnlyAfterASuccessfulPoll(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	be.setWorkloads(running("wl-ghost", "runner-ghost"))
	ctx := context.Background()

	// A controller outage must never look like "nobody owns these runners".
	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("reported %+v before any successful poll", reports)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed an orphan before the controller had ever answered a poll")
	}

	a.polled.Store(true)
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed an orphan on the first pass; it must be unclaimed for the grace period first")
	}

	clock.advance(orphanGrace + time.Second)
	reports, err = a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times, want 1", removed)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerRemoved || reports[0].RunnerID != "runner-ghost" {
		t.Fatalf("unexpected reports: %+v", reports)
	}
}

func TestReconcileReportsCleanEphemeralExitAsRemoved(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 0))

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one", reports)
	}
	if reports[0].State != store.RunnerRemoved {
		t.Fatalf("State = %q, want %q (an ephemeral runner exiting after its job is success)", reports[0].State, store.RunnerRemoved)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("reconcile removed a tracked workload the controller has not asked it to remove")
	}

	// The end of a runner's life is reported once, not on every pass.
	again, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-reported a terminal runner: %+v", again)
	}
}

func TestReconcileReportsNonZeroExitAsFailed(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 137))

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerFailed {
		t.Fatalf("reports = %+v, want one failed", reports)
	}
	if reports[0].ExitCode != 137 || !strings.Contains(reports[0].Message, "137") {
		t.Fatalf("report does not carry the exit code: %+v", reports[0])
	}
}

func TestReconcileTreatsAStoppedRunnerAsRemoved(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	a.markStopping("runner-1")
	be.setWorkloads(exited("wl-1", "runner-1", 143))

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	// Zoomies asked for the stop, so the signal exit code is not a job failure.
	if len(reports) != 1 || reports[0].State != store.RunnerRemoved {
		t.Fatalf("reports = %+v, want one removed", reports)
	}
}

func TestReconcileSamplesStatsForRunningWorkloads(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(running("wl-1", "runner-1"))
	be.mu.Lock()
	be.stats = backend.Stats{CPUPercent: 12.5, MemoryBytes: 1 << 20}
	be.mu.Unlock()

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one", reports)
	}
	if reports[0].Stats.CPUPercent != 12.5 || reports[0].Phase != backend.PhaseRunning {
		t.Fatalf("report did not carry the sample: %+v", reports[0])
	}
	// Whether a live runner is idle or busy is GitHub's answer, not the host's.
	if reports[0].State != "" {
		t.Fatalf("State = %q, want no claim for a live runner", reports[0].State)
	}
}

func TestReconcileReportsAWorkloadThatDisappeared(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads()

	// A runner created moments ago may simply not be listed yet.
	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("declared a just-created runner gone: %+v", reports)
	}

	clock.advance(missingGrace + time.Second)
	reports, err = a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerRemoved {
		t.Fatalf("reports = %+v, want one removed", reports)
	}
	if got := a.Runners(); len(got) != 0 {
		t.Fatalf("runner still tracked: %+v", got)
	}
}

func TestReconcileSurfacesBackendListFailures(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	be.mu.Lock()
	be.listErr = backend.ErrUnavailable
	be.mu.Unlock()

	if _, err := a.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("a backend that cannot be listed was reported as a clean reconcile")
	}
}
