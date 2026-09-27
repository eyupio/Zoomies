package agent

import (
	"context"
	"errors"
	"fmt"
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

func TestAnUnknownProcessExitDoesNotInventSuccessOrFailure(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	track(a, "runner-unknown", "wl-unknown", true)
	w := exited("wl-unknown", "runner-unknown", -1)
	w.Status.Phase = backend.PhaseExitUnknown
	be.setWorkloads(w)
	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerRemoved || reports[0].Phase != backend.PhaseExitUnknown {
		t.Fatalf("unknown exit must retire the runner without declaring a failure: %+v", reports)
	}
	if !strings.Contains(reports[0].Message, "unknown") || strings.Contains(reports[0].Message, "cleanly") {
		t.Fatalf("the message must disclose the missing exit status: %q", reports[0].Message)
	}
}

func TestHostRemovalConfirmationIsRetriedUntilAcknowledged(t *testing.T) {
	a, tr, be, clock := newAgent(t, 2)
	a.retention = time.Second
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	clock.advance(2 * time.Second)
	reports, err := a.ReconcileOnce(context.Background())
	if err != nil || len(reports) != 1 || !reports[0].HostRemoved {
		t.Fatalf("removal confirmation: %+v, %v", reports, err)
	}
	tr.reportErr = errors.New("controller unavailable")
	a.sendReports(context.Background(), reports)
	a.releaseUnknown([]string{"runner-1"})
	retry, err := a.ReconcileOnce(context.Background())
	if err != nil || len(retry) != 1 || !retry[0].HostRemoved {
		t.Fatalf("lost confirmation was not retried: %+v, %v", retry, err)
	}
	tr.reportErr = nil
	a.sendReports(context.Background(), retry)
	if len(a.Runners()) != 0 {
		t.Fatal("an acknowledged confirmation remained pending")
	}
}

func TestRemovalConfirmsCompanionsEvenWhenTheParentIsGone(t *testing.T) {
	_, _, be, _ := newAgent(t, 2)
	companion := running("sidecar-1", "runner-1")
	companion.Sidecar = true
	other := running("sidecar-2", "runner-2")
	other.Sidecar = true
	be.setWorkloads(companion, other)
	if err := removeRunnerWorkload(context.Background(), be, "runner-1", ""); err != nil {
		t.Fatal(err)
	}
	left, err := be.List(context.Background())
	if err != nil || len(left) != 1 || left[0].RunnerID != "runner-2" {
		t.Fatalf("cleanup must remove only this runner's companion: %+v, %v", left, err)
	}
	be.listErr = errors.New("daemon unavailable")
	if err := removeRunnerWorkload(context.Background(), be, "runner-1", ""); err == nil {
		t.Fatal("failed inventory was treated as proof of absence")
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

	a.markReported(reports)

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

// The entrypoint's own exit codes mean "never registered", and the container
// log is the only place that says why. The report has to point there, because
// the job log an operator would otherwise open does not exist.
func TestReconcileExplainsAnEntrypointExitCode(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 69))

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerFailed {
		t.Fatalf("reports = %+v, want one failed", reports)
	}
	if !strings.Contains(reports[0].Message, "runners.docker_wait") {
		t.Fatalf("report does not say what exit 69 means: %q", reports[0].Message)
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

func TestReconcileCarriesSeparatelySampledStatsForRunningWorkloads(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(running("wl-1", "runner-1"))
	be.mu.Lock()
	be.stats = backend.Stats{CPUPercent: 12.5, MemoryBytes: 1 << 20}
	be.mu.Unlock()
	a.sampleStats(context.Background())

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one", reports)
	}
	if reports[0].Stats.CPUPercent != 12.5 || reports[0].Stats.SampledAt == nil || reports[0].Phase != backend.PhaseRunning {
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
	a.markReported(reports)
	a.ReconcileOnce(context.Background())
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
	if len(reports) != 1 || !reports[0].HostRemoved {
		t.Fatalf("cleanup did not produce a positive removal confirmation: %+v", reports)
	}
	if got := a.Runners(); len(got) != 1 || !got[0].HostRemoved {
		t.Fatalf("confirmation must remain pending until acknowledged: %+v", got)
	}
	a.markReported(reports)
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
	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	// A removal that failed -- as opposed to one still under way -- is said
	// at once: the container is still on the host and nothing will take it.
	if len(reports) != 1 || reports[0].CleanupError == "" {
		t.Fatalf("a failed removal was not reported: %+v", reports)
	}
	if got := a.Runners(); len(got) != 1 {
		t.Fatalf("forgot a runner whose workload is still on the host: %+v", got)
	}

	be.mu.Lock()
	be.removeErr = nil
	be.mu.Unlock()
	reports, err = a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || !reports[0].HostRemoved {
		t.Fatalf("recovery did not produce a removal confirmation: %+v", reports)
	}
	a.markReported(reports)
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times after the backend recovered, want 1", removed)
	}
	if got := a.Runners(); len(got) != 0 {
		t.Fatalf("still tracking a runner whose workload was removed: %+v", got)
	}
}

// errStillRemoving is what the backend says when the daemon is carrying out a
// removal of the workload that it has not finished.
var errStillRemoving = fmt.Errorf("backend: removing docker-in-docker sidecar for runner-1: docker api: DELETE /containers/runner-1-dind: 409: removal of container runner-1-dind is already in progress: %w", backend.ErrRemovalInProgress)

// A docker-in-docker sidecar can take minutes to remove, and the daemon
// finishes a removal it has started whether or not anyone waits. Each pass
// that found one still going used to be reported as a failed cleanup: the
// Runners page said something was left behind, and the attempt count climbed,
// while the daemon was deleting it.
func TestAWorkloadTheDaemonIsStillRemovingIsNotAFailedCleanup(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.retention = 0
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	ctx := context.Background()

	be.mu.Lock()
	be.removeErr = errStillRemoving
	be.mu.Unlock()
	for pass := range 5 {
		reports, err := a.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("ReconcileOnce: %v", err)
		}
		if len(reports) != 0 {
			t.Fatalf("pass %d reported a removal the daemon is still carrying out: %+v", pass, reports)
		}
		if got := a.Runners(); len(got) != 1 {
			t.Fatalf("forgot a runner whose workload is still being removed: %+v", got)
		}
		clock.advance(defaultReconcileInterval)
	}

	be.mu.Lock()
	be.removeErr = nil
	be.mu.Unlock()
	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || !reports[0].HostRemoved || reports[0].CleanupError != "" {
		t.Fatalf("the finished removal was not confirmed: %+v", reports)
	}
}

// The window is a bound, not a way to hide a daemon that has stopped making
// progress: past it the removal is reported, with what to do about it.
func TestARemovalStillUnderWayPastItsBoundIsReported(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.retention = 0
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	ctx := context.Background()

	be.mu.Lock()
	be.removeErr = errStillRemoving
	be.mu.Unlock()
	if reports, _ := a.ReconcileOnce(ctx); len(reports) != 0 {
		t.Fatalf("the first sight of a removal under way was reported: %+v", reports)
	}
	clock.advance(removalSettle)
	reports, err := a.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].CleanupError == "" {
		t.Fatalf("a removal stuck past its bound was not reported: %+v", reports)
	}
	msg := reports[0].CleanupError
	if !strings.Contains(msg, "10m0s") || !strings.Contains(msg, "restart") || !strings.Contains(msg, "already in progress") {
		t.Fatalf("the report should say how long, what to do, and keep the daemon's words: %s", msg)
	}
}

// A removal that fails for another reason in between starts the window
// again: the earlier wait was for a removal that has since ended.
func TestARemovalThatFailsDifferentlyStartsTheWindowAgain(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.retention = 0
	reportedAndFinished(t, a, be, "runner-1", "wl-1")
	ctx := context.Background()
	setRemoveErr := func(err error) {
		be.mu.Lock()
		be.removeErr = err
		be.mu.Unlock()
	}

	setRemoveErr(errStillRemoving)
	_, _ = a.ReconcileOnce(ctx)
	clock.advance(removalSettle - time.Minute)
	setRemoveErr(errors.New("unable to remove filesystem"))
	if reports, _ := a.ReconcileOnce(ctx); len(reports) != 1 || reports[0].CleanupError == "" {
		t.Fatalf("a removal that failed was not reported at once: %+v", reports)
	}
	clock.advance(2 * time.Minute)
	setRemoveErr(errStillRemoving)
	if reports, _ := a.ReconcileOnce(ctx); len(reports) != 0 {
		t.Fatalf("a fresh removal under way inherited the old one's window: %+v", reports)
	}
}

// A backend that cannot be listed says nothing about the runners on it. The
// missing-workload pass used to treat "not listed" like "not seen", so a
// transient daemon error a minute into a runner's life declared it gone, the
// controller marked its job lost, and once the daemon answered again the live
// container was reaped as an orphan.
func TestAListingFailureDoesNotDeclareTrackedRunnersGone(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(running("wl-1", "runner-1"))
	clock.advance(missingGrace + time.Second)

	be.mu.Lock()
	be.listErr = backend.ErrUnavailable
	be.mu.Unlock()
	reports, err := a.ReconcileOnce(context.Background())
	if err == nil {
		t.Fatal("a backend that cannot be listed was reported as a clean reconcile")
	}
	for _, r := range reports {
		if r.State == store.RunnerRemoved || r.State == store.RunnerFailed {
			t.Fatalf("declared a runner gone while its backend could not be listed: %+v", r)
		}
	}
	if got := a.Runners(); len(got) != 1 {
		t.Fatalf("tracked runners = %+v, want the one the listing failure said nothing about", got)
	}

	// The daemon answers again and the container is still there: the runner
	// is observed as before, and nothing has to be undone.
	be.mu.Lock()
	be.listErr = nil
	be.mu.Unlock()
	reports, err = a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	for _, r := range reports {
		if r.State == store.RunnerRemoved || r.State == store.RunnerFailed {
			t.Fatalf("a runner that survived a listing failure was reported gone: %+v", r)
		}
	}
	if got := a.Runners(); len(got) != 1 {
		t.Fatalf("tracked runners = %+v, want one", got)
	}

	// A workload that really has disappeared is still declared gone once the
	// backend can be listed: the change narrows the rule, it does not blunt it.
	be.setWorkloads()
	reports, err = a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerRemoved {
		t.Fatalf("reports = %+v, want one removed", reports)
	}
}

// sidecar is what a backend lists when a runner container has gone out of band
// and its privileged docker-in-docker daemon is still running. It carries the
// runner's id, because that is the only thing that says whose leftovers it is.
func sidecar(handle backend.Handle, runnerID string) backend.Workload {
	return backend.Workload{
		Handle:   handle,
		Name:     "runner-" + runnerID + "-dind",
		RunnerID: runnerID,
		Sidecar:  true,
		Status:   backend.Status{Handle: handle, Phase: backend.PhaseRunning},
	}
}

// The sidecar shares its runner's id, so an agent that looks runners up by id
// alone finds the sidecar and concludes the runner is alive and well -- at the
// wrong handle. The runner is then never declared missing, its job is never
// marked lost, a later stop goes to the daemon rather than the container, and
// the sidecar is never reaped. It has to be treated as rubbish from the start.
func TestReconcileTreatsAnAbandonedSidecarAsRubbishNotAsItsRunner(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.polled.Store(true)
	track(a, "runner-1", "wl-1", true)
	// The runner container has been removed behind the agent's back; only the
	// sidecar is left on the host.
	be.setWorkloads(sidecar("dind-1", "runner-1"))

	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("nothing is decided within the grace periods, got %+v", reports)
	}

	clock.advance(orphanGrace + time.Second)
	reports, err = a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}

	if got := removedHandles(be); len(got) != 1 || got[0] != "dind-1" {
		t.Fatalf("the abandoned sidecar must be removed, removed = %v", got)
	}
	if len(reports) != 1 {
		t.Fatalf("want one report, the runner's own disappearance, got %+v", reports)
	}
	if reports[0].RunnerID != "runner-1" || reports[0].State != store.RunnerRemoved {
		t.Fatalf("report = %+v", reports[0])
	}
	// The runner's own handle, not the sidecar's: the report has to be about
	// the container that held the job.
	if reports[0].Handle != "wl-1" {
		t.Fatalf("the report named the sidecar rather than the runner: %+v", reports[0])
	}
	a.markReported(reports)
	a.ReconcileOnce(context.Background())
	if _, ok := a.snapshot("runner-1"); ok {
		t.Fatal("a runner acknowledged gone must stop being tracked")
	}
}

// Reaping a sidecar is not the runner's removal. Its runner container reaches
// the controller through its own absence, and reporting this one as the runner
// would move a row on the strength of the wrong container.
func TestReconcileDoesNotReportASidecarAsItsRunnersRemoval(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	a.polled.Store(true)
	// Nothing tracked: this is a host the agent restarted onto, where the
	// runner is long gone and only its sidecar remains.
	be.setWorkloads(sidecar("dind-1", "runner-1"))

	if _, err := a.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	clock.advance(orphanGrace + time.Second)
	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if _, _, removed := be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times, want 1", removed)
	}
	if len(reports) != 0 {
		t.Fatalf("a sidecar has no runner row to move, got %+v", reports)
	}
}
