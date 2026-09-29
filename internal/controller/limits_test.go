package controller

import (
	"errors"
	"testing"
)

// limits.runners is decided in the scheduler, but it only protects anything
// if the reconcile loop hands it over: a pool's minimum above the ceiling must
// still leave the fleet at the ceiling.
func TestReconcileCreatesNoRunnerBeyondLimitsRunners(t *testing.T) {
	h := newHarness(t)
	h.cfg.Limits.Runners = 2
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 4
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	h.host("one")
	h.host("two")

	// Enough passes to reach the pool minimum of four, since registration is
	// admitted a runner at a time: without the ceiling this ends at four.
	for range 5 {
		if err := h.c.Reconcile(h.ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		h.c.lifecycleCalls.Wait()
	}
	if n := len(h.runners()); n != 2 {
		t.Fatalf("runners = %d, want 2: limits.runners did not hold", n)
	}
}

func TestTheStoredLimitsRefuseAtTheirCeiling(t *testing.T) {
	h := newHarness(t)
	h.cfg.Limits.Pools = 1
	h.cfg.Limits.Hosts = 1
	inst := h.installation()

	if err := h.c.AdmitPool(h.ctx); err != nil {
		t.Fatalf("an empty instance refused a pool: %v", err)
	}
	h.pool(inst, "only")
	var le *LimitError
	if err := h.c.AdmitPool(h.ctx); !errors.As(err, &le) || le.Setting != "limits.pools" || !errors.Is(err, ErrLimitReached) {
		t.Fatalf("AdmitPool at the ceiling = %v, want a limits.pools LimitError", err)
	}

	if err := h.c.admitHost(h.ctx); err != nil {
		t.Fatalf("an empty instance refused a host: %v", err)
	}
	h.host("only")
	if err := h.c.admitHost(h.ctx); !errors.As(err, &le) || le.Setting != "limits.hosts" {
		t.Fatalf("admitHost at the ceiling = %v, want a limits.hosts LimitError", err)
	}

	h.cfg.Limits.Pools = 0
	if err := h.c.AdmitPool(h.ctx); err != nil {
		t.Fatalf("a ceiling of 0 refused a pool: %v", err)
	}
}

// The pools import creates several pools in one request, so the ceiling is on
// the total it would leave, not on whether there is room for one more.
func TestAdmitPoolsRefusesABatchThatWouldCrossTheCeiling(t *testing.T) {
	h := newHarness(t)
	h.cfg.Limits.Pools = 3
	inst := h.installation()
	h.pool(inst, "first")

	if err := h.c.AdmitPools(h.ctx, 2); err != nil {
		t.Fatalf("two more pools with room for two: %v", err)
	}
	var le *LimitError
	if err := h.c.AdmitPools(h.ctx, 3); !errors.As(err, &le) || le.Setting != "limits.pools" {
		t.Fatalf("three more pools with room for two = %v, want a limits.pools LimitError", err)
	}
	if err := h.c.AdmitPools(h.ctx, 0); err != nil {
		t.Fatalf("no new pools was refused: %v", err)
	}
}
