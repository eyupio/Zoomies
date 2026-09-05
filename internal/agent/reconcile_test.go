package agent

import (
	"context"
	"errors"
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
	a, _, be, clock := newAgent(t, 2)
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
		t.Fatal("reconcile removed a tracked workload before the controller had been told how it ended")
	}

	// The end of a runner's life is reported once, not on every pass.
	again, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-reported a terminal runner: %+v", again)
	}

	// Once the workload is gone, however it went, the agent stops carrying
	// the runner in every heartbeat.
	be.setWorkloads()
	clock.advance(missingGrace + time.Second)
	if again, err = a.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-reported a runner that was already reported terminal: %+v", again)
	}
	if got := a.Runners(); len(got) != 0 {
		t.Fatalf("still tracking a finished runner whose workload is gone: %+v", got)
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

// reportedAndFinished puts the agent where it is after an ephemeral runner has
// exited and the reconciler has told the controller so: one terminal report
// delivered, the workload still on the host.
func reportedAndFinished(t *testing.T, a *Agent, be *fakeBackend, runnerID string, handle backend.Handle) {
	t.Helper()
	a.polled.Store(true)
	track(a, runnerID, handle, true)
	be.setWorkloads(exited(handle, runnerID, 0))
	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerRemoved {
		t.Fatalf("reports = %+v, want one removed", reports)
	}
	a.sendReports(context.Background(), reports)
}

// A clean exit is the commonest thing in the system, and the controller has no
// reason to send a task for a runner it already considers gone. If the agent
// did not delete the container, nothing would, and every job would leave its
// writable layer, its log and a running docker-in-docker sidecar on the host
// until the disk filled.
func TestReconcileRemovesAFinishedWorkloadOnceReportedAndRetentionHasPassed(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.retention = 10 * time.Minute
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	ctx := context.Background()

	// Reported, but the window is what gives an operator time to read the
	// runner's output from the Runners page, so it stays for now.
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed a finished workload before its retention window had passed")
	}

	clock.advance(a.retention + time.Second)
	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times after the retention window, want 1", removed)
	}
	// The controller already knows how this runner ended; deleting the
	// leftovers is not news.
	if len(reports) != 0 {
		t.Fatalf("cleaning up produced reports: %+v", reports)
	}
	if got := a.Runners(); len(got) != 0 {
		t.Fatalf("still tracking a runner whose workload it just removed: %+v", got)
	}
}

// Deleting the workload before the controller has heard how the runner ended
// would take the exit code with it and leave a live row for a runner that no
// longer exists. However long ago it finished, an unreported workload stays.
func TestReconcileKeepsAFinishedWorkloadUntilTheControllerHasBeenTold(t *testing.T) {
	a, tr, be, clock := newAgent(t, 2)
	a.retention = 0
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 0))
	ctx := context.Background()

	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	clock.advance(time.Hour)
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed a finished workload the controller had not been told about")
	}

	// A report the controller refused does not count as telling it.
	tr.mu.Lock()
	tr.reportErr = errors.New("controller unreachable")
	tr.mu.Unlock()
	a.sendReports(ctx, reports)
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed a finished workload after a report the controller never received")
	}

	tr.mu.Lock()
	tr.reportErr = nil
	tr.mu.Unlock()
	a.sendReports(ctx, reports)
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times once the controller had the report, want 1", removed)
	}
}

// The heartbeat carries every tracked runner, terminal ones included, so an
// acknowledged beat is as good as a report: the controller knows.
func TestHeartbeatCountsAsTellingTheControllerHowARunnerEnded(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	a.retention = 0
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 0))
	ctx := context.Background()

	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	tr.mu.Lock()
	tr.beatErr = errors.New("controller unreachable")
	tr.mu.Unlock()
	if err := a.heartbeat(ctx); err == nil {
		t.Fatal("a refused heartbeat was reported as success")
	}
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed a finished workload on the strength of a heartbeat the controller never answered")
	}

	tr.mu.Lock()
	tr.beatErr = nil
	tr.mu.Unlock()
	if err := a.heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times after an acknowledged heartbeat, want 1", removed)
	}
}

// A failed runner is cleaned up the same way. The controller usually sends a
// remove task for one of those, but a task can be lost to a restart, and a
// host must not depend on it to get its disk back.
func TestReconcileRemovesAFailedWorkloadTheSameWay(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.retention = 0
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 137))
	ctx := context.Background()

	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerFailed {
		t.Fatalf("reports = %+v, want one failed", reports)
	}
	a.sendReports(ctx, reports)
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times for a reported failed runner, want 1", removed)
	}
}

// A task for the same runner owns its workload until it reports; two removals
// racing over one container is the thing the claim exists to prevent.
func TestReconcileLeavesAFinishedWorkloadToATaskAlreadyHandlingIt(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.retention = 0
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	ctx := context.Background()

	if !a.claim("runner-1") {
		t.Fatal("could not claim the runner for a pretend task")
	}
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 0 {
		t.Fatal("removed a workload while a task for its runner was in flight")
	}

	a.release("runner-1")
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times once the task had finished, want 1", removed)
	}
}

// A removal the backend refused is not forgotten: the workload is still on the
// disk, so the next pass tries again rather than the agent giving up on it.
func TestReconcileRetriesAFinishedWorkloadItCouldNotRemove(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.retention = 0
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	ctx := context.Background()

	be.mu.Lock()
	be.removeErr = errors.New("daemon busy")
	be.mu.Unlock()
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := a.Runners(); len(got) != 1 {
		t.Fatalf("forgot a runner whose workload is still on the host: %+v", got)
	}

	be.mu.Lock()
	be.removeErr = nil
	be.mu.Unlock()
	if _, err := a.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times after the backend recovered, want 1", removed)
	}
	if got := a.Runners(); len(got) != 0 {
		t.Fatalf("still tracking a runner whose workload was removed: %+v", got)
	}
}
