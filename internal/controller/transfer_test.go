package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

func TestRestartLoadsTransferPreparationBeforeSchedulingWork(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)
	if err := h.st.SetSetting(h.ctx, store.SettingTransferDraining, "true", false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	if err := h.c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !h.c.transferDraining.Load() {
		t.Fatal("scheduler started without persisted preparation")
	}
	stop, done := context.WithTimeout(h.ctx, 5*time.Second)
	defer done()
	if err := h.c.Stop(stop); err != nil {
		t.Fatal(err)
	}
	saved, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil || !saved.Enabled {
		t.Fatal("restart rewrote the saved pool", err)
	}
	busy, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil || busy.State != store.RunnerBusy {
		t.Fatal("restart interrupted busy work", err)
	}
}

func TestOneClickTransferPreparationLetsBusyJobsFinishAndKeepsPoolSettings(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)
	p, err := h.c.PrepareTransfer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Draining || p.Ready || p.BusyRunners != 1 {
		t.Fatalf("unexpected progress: %+v", p)
	}
	if err = h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	current, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil || current.State != store.RunnerBusy {
		t.Fatal("preparation interrupted a busy job", err)
	}
	stored, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil || !stored.Enabled {
		t.Fatal("preparation rewrote pool settings", err)
	}
	snap, err := h.c.snapshot(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Pools[0].Enabled {
		t.Fatal("preparation allowed new demand")
	}
	if h.c.Fenced().Fenced {
		t.Fatal("fence stopped cleanup before the running job finished")
	}
	for _, state := range []store.RunnerState{store.RunnerDraining, store.RunnerRemoved} {
		if _, err = h.st.TransitionRunner(h.ctx, r.ID, state, "job finished"); err != nil {
			t.Fatal(err)
		}
	}
	p, err = h.c.TransferProgress(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready || p.PendingCleanup != 1 {
		t.Fatal("unfinished cleanup was ignored", p)
	}
	if _, err = h.st.ConfirmRunnerCleanup(h.ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = h.st.RecordRegistrationDeleted(h.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	p, err = h.c.TransferProgress(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready || !h.c.Fenced().Fenced {
		t.Fatalf("drained fleet not fenced: %+v", p)
	}
	if err = h.st.TransferReady(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err = h.c.CancelTransfer(h.ctx); err == nil {
		t.Fatal("cancel silently lifted a cutover fence")
	}
	if err = h.c.Unfence(h.ctx); err != nil {
		t.Fatal(err)
	}
	if h.c.transferDraining.Load() {
		t.Fatal("explicit recovery did not resume scheduling")
	}
}

func TestCancellingPreparationResumesSavedPoolsWithoutChangingThem(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.runnerRow(pool, host, store.RunnerBusy)
	if _, err := h.c.PrepareTransfer(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.c.CancelTransfer(h.ctx); err != nil {
		t.Fatal(err)
	}
	p, err := h.c.TransferProgress(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Draining || h.c.transferDraining.Load() {
		t.Fatal("preparation was not cancelled")
	}
	snap, err := h.c.snapshot(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Pools[0].Enabled {
		t.Fatal("saved pool did not resume")
	}
}

func TestAnEmptyInstanceIsReadyAfterOnePrepareClick(t *testing.T) {
	h := newHarness(t)
	p, err := h.c.PrepareTransfer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready || !p.Draining {
		t.Fatalf("empty instance not ready: %+v", p)
	}
	again, err := h.c.PrepareTransfer(h.ctx)
	if err != nil || !again.Ready {
		t.Fatalf("repeat preparation not idempotent: %+v %v", again, err)
	}
}

func TestPreparationCancelsOnlyMachineReservationsThatNeverIssuedACall(t *testing.T) {
	h := newHarness(t)
	p := h.providerRow(t, "transfer-provider")
	unissued := &store.Machine{ProviderID: p.ID, Name: "unissued", State: store.MachinePlanned}
	issued := &store.Machine{ProviderID: p.ID, Name: "issued", State: store.MachinePlanned}
	for _, m := range []*store.Machine{unissued, issued} {
		if err := h.st.CreateMachine(h.ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.st.ClaimMachineOperation(h.ctx, issued.ID, store.MachineOperation{Kind: store.MachineOpCreate, Holder: "worker", Timeout: time.Minute}, h.c.Now()); err != nil {
		t.Fatal(err)
	}
	progress, err := h.c.PrepareTransfer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Ready || progress.MachineOperations != 1 {
		t.Fatalf("in-flight operation ignored: %+v", progress)
	}
	row, err := h.st.GetMachine(h.ctx, unissued.ID)
	if err != nil || row.State != store.MachineFailed {
		t.Fatal("unissued reservation not cancelled", err)
	}
	row, err = h.st.GetMachine(h.ctx, issued.ID)
	if err != nil || row.State != store.MachinePlanned || row.OpID == "" {
		t.Fatal("issued reservation changed", err)
	}
}

// Every count in a transfer's preparation moves as runners are confirmed gone
// and jobs finish, and no row says "the transfer moved": the page an operator
// watches while the fleet drains was the one page that sat still until it was
// reloaded. A pass sends the preparation when it changes, in the shape
// GET /transfers/preparation returns, with the sentence that says what is
// happening and whether any of it needs them.
func TestTransferPreparationIsSentToThePageAsItMoves(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	sub := h.listen(events.KindTransfer)
	pass := func(doing string) {
		t.Helper()
		if err := h.c.Reconcile(h.ctx); err != nil {
			t.Fatalf("Reconcile %s: %v", doing, err)
		}
	}

	// Nothing is being prepared, so a pass has nothing to say and reads
	// nothing to say it.
	pass("with no transfer")
	nothingFor(t, sub)

	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.c.PrepareTransfer(h.ctx); err != nil {
		t.Fatal(err)
	}
	// The click itself is announced, before any pass.
	first := nextOfKind(t, sub, events.KindTransfer)
	if first["draining"] != true || first["live_runners"] != float64(1) {
		t.Errorf("first frame = %v, want draining with one live runner", first)
	}
	if summary, _ := first["summary"].(string); !strings.HasPrefix(summary, "Preparing: 1 idle runner being withdrawn.") {
		t.Errorf("first frame summary = %q", summary)
	}

	// The runner is withdrawn and removed; nobody has confirmed it gone yet.
	for _, state := range []store.RunnerState{store.RunnerDraining, store.RunnerRemoved} {
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, state, "withdrawn"); err != nil {
			t.Fatal(err)
		}
	}
	pass("after the runner was removed")
	cleanup := nextOfKind(t, sub, events.KindTransfer)
	if cleanup["pending_cleanup"] != float64(1) || cleanup["cleanup_awaiting_host"] != float64(1) {
		t.Errorf("frame after removal = %v, want one runner awaiting its host", cleanup)
	}
	waits, _ := cleanup["waiting"].([]any)
	if len(waits) != 1 {
		t.Fatalf("frame after removal names %d waits, want the one runner: %v", len(waits), cleanup["waiting"])
	}
	wait, _ := waits[0].(map[string]any)
	if wait["kind"] != "cleanup" || wait["name"] != r.Name || wait["host"] != "vm-1" || wait["host_healthy"] != true {
		t.Errorf("the wait = %v, want the runner on vm-1", wait)
	}
	if detail, _ := wait["detail"].(string); !strings.Contains(detail, "vm-1 to confirm it is gone") {
		t.Errorf("the wait's detail = %q", detail)
	}
	if summary, _ := cleanup["summary"].(string); !strings.Contains(summary, "Nothing needs you yet") {
		t.Errorf("summary with a healthy host = %q, want it to say nothing is needed", summary)
	}

	// Nothing has moved, so nothing is said.
	pass("with nothing changed")
	nothingFor(t, sub)

	// The host goes quiet. Nothing is written anywhere, and the sentence has
	// to change: nobody but that host can confirm its runner gone.
	h.advance(2 * store.HeartbeatTimeout)
	pass("after the host fell silent")
	silent := nextOfKind(t, sub, events.KindTransfer)
	if summary, _ := silent["summary"].(string); !strings.Contains(summary, "Host vm-1 has stopped sending heartbeats") {
		t.Errorf("summary with a silent host = %q, want it to name vm-1", summary)
	}

	// Both sides confirm, and the pass that finds the fleet drained raises the
	// fence and says so.
	if _, err := h.st.ConfirmRunnerCleanup(h.ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordRegistrationDeleted(h.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	pass("after cleanup was confirmed")
	ready := nextOfKind(t, sub, events.KindTransfer)
	if ready["ready"] != true || len(ready["waiting"].([]any)) != 0 {
		t.Errorf("frame after cleanup = %v, want ready with nothing waiting", ready)
	}
	if summary, _ := ready["summary"].(string); !strings.HasPrefix(summary, "Ready to export.") {
		t.Errorf("summary when ready = %q", summary)
	}
}

// A runner withdrawn while idle ran no job, and until now nothing asked its
// host about it again if the one report of its removal was lost: the row sat
// at "awaiting cleanup" with nothing on the host, and a transfer behind it
// never finished preparing. After a grace the host is asked again.
func TestAFinishedRunnerNobodyConfirmedGoneIsAskedAboutAgain(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	for _, state := range []store.RunnerState{store.RunnerDraining, store.RunnerRemoved} {
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, state, "withdrawn"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	for _, task := range h.tasksFor(host.ID) {
		if task.Kind == agent.TaskRemoveRunner && task.RunnerID == r.ID {
			t.Fatal("the host was asked inside the grace, while it may still be keeping the container for its logs")
		}
	}
	h.advance(hostCleanupGrace + time.Minute)
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	if task := h.taskOfKind(host.ID, agent.TaskRemoveRunner); task.RunnerID != r.ID {
		t.Fatalf("the remove task is for %q, want %q", task.RunnerID, r.ID)
	}
}
