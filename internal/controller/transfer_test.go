package controller

import (
	"context"
	"testing"
	"time"

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
