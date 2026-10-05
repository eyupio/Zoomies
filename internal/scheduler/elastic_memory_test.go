package scheduler

import (
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func memoryPool(max int64) *store.Pool {
	p := limited("mem", 0, 4096)
	p.MemoryBurst = store.MemoryBurstPolicy{Mode: store.MemoryBurstAutomatic, MaxMemoryMB: max}
	return p
}

func hostWithMemoryCeiling(memoryMB, ceiling int64) *store.Host {
	h := sized("host_a", 4, 8, memoryMB, 100*1024)
	h.RunnerProfile.Standard.BurstMaxMemoryMB = ceiling
	return h
}

// A ceiling is where the owner of the machine and the owner of the pool meet,
// and the host's word is the last: a pool asking for more than the host will
// give simply gets what it will.
func TestTheSmallerOfThePoolsAndTheHostsMemoryCeilingWins(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pool, host int64
		want       int64
	}{
		{"neither says", 0, 0, 0},
		{"only the pool says", 12288, 0, 12288},
		{"only the host says", 0, 8192, 8192},
		{"the host is lower", 12288, 8192, 8192},
		{"the pool is lower", 6144, 8192, 6144},
	} {
		if got := MemoryBurstLimit(memoryPool(tc.pool), hostWithMemoryCeiling(32*1024, tc.host)); got != tc.want {
			t.Errorf("%s: limit = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := MemoryBurstLimit(nil, nil); got != 0 {
		t.Errorf("no pool and no host: limit = %d, want 0", got)
	}
}

func TestAMemoryCeilingIsHalfAsMuchAgainUnlessSomeoneSaidOtherwise(t *testing.T) {
	h := hostWithMemoryCeiling(32*1024, 0)
	for _, tc := range []struct {
		name      string
		pool      int64
		guarantee int64
		want      int64
	}{
		{"the default", 0, 4096, 6144},
		{"a ceiling the operator named is the ceiling, not a multiple", 16384, 4096, 16384},
		// A ceiling under the guarantee would be a request to take memory back,
		// which nothing can do, so it is read as "lend nothing".
		{"never below the guarantee", 2048, 4096, 4096},
		{"no guarantee has no ceiling", 0, 0, 0},
	} {
		if got := MemoryCeiling(memoryPool(tc.pool), h, tc.guarantee); got != tc.want {
			t.Errorf("%s: ceiling = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestAMemoryCeilingNeverPassesTheMachineItRunsOn(t *testing.T) {
	h := hostWithMemoryCeiling(8*1024, 0)
	if got := MemoryCeiling(memoryPool(64*1024), h, 4096); got != 8*1024 {
		t.Fatalf("ceiling on an 8 GB host = %d, want the host's own 8192", got)
	}
	// A daemon smaller than the machine the agent measured refuses a limit
	// above its own memory, so the ceiling is the daemon's.
	h.BackendInfo = store.HostBackends{{Kind: store.BackendDocker, Available: true, MemoryMB: 6 * 1024}}
	if got := MemoryCeiling(memoryPool(64*1024), h, 4096); got != 6*1024 {
		t.Fatalf("ceiling on a 6 GB daemon = %d, want 6144", got)
	}
}

func TestTheFloorIsTheReservePlusAnotherTwentiethOfTheMachine(t *testing.T) {
	h := sized("host_a", 4, 8, 16*1024, 100*1024)
	h.ReserveMemoryMB = 2048
	want := h.MemoryReserve() + 16*1024/20
	if got := MemoryPoolFloor(h); got != want {
		t.Fatalf("floor = %d, want %d (reserve %d plus 5%%)", got, want, h.MemoryReserve())
	}
	if got := MemoryPoolFloor(&store.Host{}); got != 0 {
		t.Fatalf("a host that has not reported its memory has a floor of %d, want 0", got)
	}
	if got := MemoryPoolFloor(nil); got != 0 {
		t.Fatalf("no host has a floor of %d, want 0", got)
	}
}

func ptr[T any](v T) *T { return &v }

// The pool is what is left of the machine once every promise is kept: busy and
// starting runners in full, idle ones by the largest between them, one queued
// start protected, and the floor left alone.
func TestThePoolIsWhatNoGuaranteeNeeds(t *testing.T) {
	const gb = 1024
	work := []MemoryWorkload{
		{ID: "busy", GuaranteeMB: 4 * gb},
		{ID: "idle-1", GuaranteeMB: 4 * gb, Idle: true},
		{ID: "idle-2", GuaranteeMB: 2 * gb, Idle: true},
	}
	got := PlanMemoryPool(MemoryPoolInput{TotalMB: 16 * gb, FloorMB: 2 * gb, StartReserveMB: 0, Workloads: work})
	// 16 - 2 floor - 4 busy - 4 largest idle = 6 GB, none lent.
	if got.CapacityMB != 6*gb || got.PoolMB != 6*gb || got.LentMB != 0 {
		t.Fatalf("pool = %+v, want 6 GB capacity, all of it left", got)
	}
	if got.CommittedMB != 4*gb || got.IdleReserveMB != 4*gb || got.Binding != MemoryBoundLedger {
		t.Fatalf("ledger lines = %+v", got)
	}

	protected := PlanMemoryPool(MemoryPoolInput{TotalMB: 16 * gb, FloorMB: 2 * gb, StartReserveMB: 4 * gb, Workloads: work})
	if protected.CapacityMB != 2*gb {
		t.Fatalf("with a start protected capacity = %d, want 2048", protected.CapacityMB)
	}
}

// A host whose runners are all busy has promised every byte it has, and the
// valve lends none of it: the promise is what the guarantee is. Lending out of
// a busy runner's unused share would be the oversubscription the ceiling and
// the floor exist to prevent.
func TestAFullyPromisedHostLendsNothing(t *testing.T) {
	const gb = 1024
	work := []MemoryWorkload{{ID: "a", GuaranteeMB: 7 * gb}, {ID: "b", GuaranteeMB: 7 * gb}}
	got := PlanMemoryPool(MemoryPoolInput{TotalMB: 16 * gb, FloorMB: 2 * gb, Workloads: work})
	if got.CapacityMB != 0 || got.PoolMB != 0 {
		t.Fatalf("a host with every byte promised has a pool of %+v, want nothing", got)
	}
}

// What has been lent comes off the pool and stays off it: a loan cannot be
// recalled, so the memory it holds is not there for the next runner whatever
// the runner that holds it is doing.
func TestMemoryAlreadyLentComesOffThePoolButNotOffTheCapacity(t *testing.T) {
	const gb = 1024
	work := []MemoryWorkload{{ID: "a", GuaranteeMB: 4 * gb, LentMB: 1 * gb}}
	got := PlanMemoryPool(MemoryPoolInput{TotalMB: 16 * gb, FloorMB: 2 * gb, Workloads: work})
	if got.CapacityMB != 10*gb || got.LentMB != 1*gb || got.PoolMB != 9*gb {
		t.Fatalf("pool = %+v, want capacity 10 GB, lent 1 GB, 9 GB left", got)
	}
}

// A ledger says what was promised; the machine says what is free. The pool is
// out of the smaller, so a host with something else eating its memory lends
// less than its books allow.
func TestTheMemoryTheHostMeasuredAsFreeCanLimitThePool(t *testing.T) {
	const gb = 1024
	work := []MemoryWorkload{{ID: "a", GuaranteeMB: 4 * gb, LentMB: 512}}
	in := MemoryPoolInput{TotalMB: 16 * gb, FloorMB: 2 * gb, AvailableMB: ptr(int64(3 * gb)), Workloads: work}
	got := PlanMemoryPool(in)
	// 3 GB free, 2 GB of it the floor: 1 GB can still be handed out, on top of
	// the 512 MB already out.
	if got.PoolMB != 1*gb || got.CapacityMB != 1*gb+512 || got.Binding != MemoryBoundMeasured {
		t.Fatalf("pool = %+v, want 1 GB left, bound by what was measured", got)
	}

	// Plenty free: the books are the limit again.
	in.AvailableMB = ptr(int64(14 * gb))
	if got := PlanMemoryPool(in); got.Binding != MemoryBoundLedger || got.CapacityMB != 10*gb {
		t.Fatalf("with plenty free the pool = %+v, want the ledger's 10 GB", got)
	}

	// Below the floor there is nothing, and the figure is not negative.
	in.AvailableMB = ptr(int64(gb))
	if got := PlanMemoryPool(in); got.PoolMB != 0 {
		t.Fatalf("below the floor the pool = %+v, want nothing", got)
	}
}

// A runner waiting for a job holds almost none of its memory, which is the whole
// of what there is to lend -- but a host cannot count every warm runner as free,
// or the first burst of jobs would find them all claiming memory it had lent.
// One guarantee is held back between them, the same line the CPU plan draws.
func TestWarmRunnersAreHeldBackByTheLargestOneNotAll(t *testing.T) {
	const gb = 1024
	work := []MemoryWorkload{{ID: "busy", GuaranteeMB: 4 * gb}}
	for _, id := range []string{"i1", "i2", "i3"} {
		work = append(work, MemoryWorkload{ID: id, GuaranteeMB: 4 * gb, Idle: true})
	}
	got := PlanMemoryPool(MemoryPoolInput{TotalMB: 32 * gb, FloorMB: 2 * gb, Workloads: work})
	if got.PoolMB != 22*gb {
		t.Fatalf("pool = %d, want 22 GB: one busy and one idle guarantee held, not four", got.PoolMB)
	}
}

func TestThePlanDoesNotDependOnTheOrderOfTheRunners(t *testing.T) {
	const gb = 1024
	a := []MemoryWorkload{{ID: "x", GuaranteeMB: 4 * gb}, {ID: "y", GuaranteeMB: 2 * gb, Idle: true}, {ID: "z", GuaranteeMB: 3 * gb, Idle: true}}
	b := []MemoryWorkload{a[2], a[0], a[1]}
	in := MemoryPoolInput{TotalMB: 16 * gb, FloorMB: gb}
	in.Workloads = a
	first := PlanMemoryPool(in)
	in.Workloads = b
	if second := PlanMemoryPool(in); first != second {
		t.Fatalf("plans differ with the order: %+v and %+v", first, second)
	}
}

// A lent runner is charged what it holds, so the host has less room for the
// next one -- which is the point of a ledger that does not forget a loan.
func TestALentRunnerIsChargedWhatItHoldsAndGuaranteedWhatItWasPromised(t *testing.T) {
	p := limited("p", 0, 4096)
	h := sized("host_a", 4, 8, hostFor(16*1024), 100*1024)
	r := onHost(testRunner("r1", p, store.RunnerBusy, 0), "host_a")
	r.AllocatedMemoryMB = 4096
	r.AllocationSource = store.AllocationFromPool
	r.LentMemoryMB = 1536

	if got := RunnerGuarantee(p, h, r).MemoryMB; got != 4096 {
		t.Fatalf("guarantee = %d, want 4096: a loan is not part of what was promised", got)
	}
	if got := RunnerCharge(p, h, r).MemoryMB; got != 4096+1536 {
		t.Fatalf("charge = %d, want 5632: the host has lent 1536 on top of the guarantee", got)
	}
	if got := Reserved(h, []*store.Pool{p}, map[string][]*store.Runner{p.ID: {r}}).MemoryMB; got != 4096+1536 {
		t.Fatalf("the host's reserved memory = %d, want the loan counted", got)
	}
	if got := RunnerCharge(p, h, nil).MemoryMB; got != 4096 {
		t.Fatalf("a nil runner is charged %d, want the pool's own 4096", got)
	}
}
