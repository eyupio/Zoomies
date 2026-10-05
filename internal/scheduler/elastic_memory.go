package scheduler

import (
	"github.com/eyupio/zoomies/internal/store"
)

// DefaultMemoryCeilingFactor is how much memory one runner may hold, as a
// multiple of its guarantee, when neither its pool nor its host has named a
// ceiling. Half as much again is enough to turn most kills that a build just
// failed to fit under into a job that finished, and little enough that one
// runaway job cannot take a machine from the runners beside it.
const DefaultMemoryCeilingFactor = 1.5

// MemoryPoolSafetyShare is the share of a host's memory that is kept free over
// and above the reserve the operator set, before any of it is lent. The reserve
// is what the host's own software needs; this is what the page cache, the
// daemon and a runner that is still starting need, so that a loan never leaves
// the machine with exactly as much room as it has just been measured to need.
const MemoryPoolSafetyShare = 0.05

// LaunchedMemoryMB is the memory a runner's containers were created with, summed
// over them, read from its row: what the host actually put at risk when the
// runner started, and so the figure a loan is a loan on top of.
//
// The pool's current size is not that figure. A pool or a host edited while the
// runner lives -- a capacity changed, a standard resized -- changes what
// Reserve says a runner of that pool costs, and a valve that took its guarantee
// from there would read a runner holding 7782 MB as guaranteed 3891, give it a
// ceiling it is already above, and call a refusal a policy. The row is the one
// place the size it was launched at is written down. It is false for a runner
// with none, which is every runner of a backend that sets no limit.
//
// A docker-in-docker runner's typed size is per container, so the pair holds
// twice it; a size taken from the host is one slot the pair splits, so it holds
// it once. That is the distinction Reserve draws, read from how the runner was
// sized rather than from how the pool is sized now.
func LaunchedMemoryMB(p *store.Pool, r *store.Runner) (int64, bool) {
	if p == nil || r == nil || r.AllocatedMemoryMB <= 0 {
		return 0, false
	}
	mb := r.AllocatedMemoryMB
	if p.DockerMode == store.DockerDinD && (r.AllocationSource == store.AllocationFromPool ||
		((r.AllocationSource == store.AllocationReduced || r.AllocationSource == store.AllocationHistory) && p.Resources.MemoryMB > 0)) {
		mb *= 2
	}
	return mb, true
}

// MemoryCeiling is the most one runner of p may hold on h, given what it was
// guaranteed. It is the ceiling the pool named or, where it named none,
// DefaultMemoryCeilingFactor times the guarantee; the host's own ceiling then
// lowers that and never raises it, for the reason CPU's does -- whoever owns the
// machine has the last word on how much of it one job may take, and a host that
// caps its runners at 32 GB has not asked for a pool that left the figure alone
// to be given 32 GB. It is never below the guarantee, because a ceiling lower
// than what a runner already has would be a request to take memory back, which
// nothing here can do; and never more than the memory of the host, or of the
// daemon the runner runs on where that is the smaller machine.
//
// That last is the one easily forgotten, as it is for CPU: a Docker Desktop or
// VM daemon is smaller than the machine the agent measured, and refuses a limit
// above its own memory outright, so a ceiling past it would be a loan that fails
// on every attempt.
func MemoryCeiling(p *store.Pool, h *store.Host, guaranteeMB int64) int64 {
	if guaranteeMB <= 0 {
		return 0
	}
	ceiling := int64(float64(guaranteeMB) * DefaultMemoryCeilingFactor)
	if p != nil && p.MemoryBurst.MaxMemoryMB > 0 {
		ceiling = p.MemoryBurst.MaxMemoryMB
	}
	if h != nil {
		if capMB := h.RunnerProfile.Standard.BurstMaxMemoryMB; capMB > 0 {
			ceiling = min(ceiling, capMB)
		}
		if h.MemoryMB > 0 {
			ceiling = min(ceiling, h.MemoryMB)
		}
		if p != nil {
			if info, ok := h.BackendInfo.Find(p.Backend); ok && info.MemoryMB > 0 {
				ceiling = min(ceiling, info.MemoryMB)
			}
		}
	}
	return max(ceiling, guaranteeMB)
}

// MemoryPoolFloor is the least free memory a host is left with by any loan: the
// reserve its operator set, or the floor where that is larger (Host.MemoryReserve),
// and a twentieth of the machine beyond it (MemoryPoolSafetyShare). Zero for a
// host that has not reported its memory, which has nothing to be left.
func MemoryPoolFloor(h *store.Host) int64 {
	if h == nil || h.MemoryMB <= 0 {
		return 0
	}
	return h.MemoryReserve() + int64(MemoryPoolSafetyShare*float64(h.MemoryMB))
}

// MemoryWorkload is one live runner in a host's memory ledger. Every live runner
// belongs in the input, including ones that will never be lent anything: their
// guarantee is already promised, so it is not memory to lend.
type MemoryWorkload struct {
	ID string
	// GuaranteeMB is what the runner was created with, summed over its
	// containers: its guarantee, without any loan (RunnerGuarantee).
	GuaranteeMB int64
	// LentMB is what it has been lent since, which is still held.
	LentMB int64
	// Idle is a live runner waiting for a job. Idle runners are charged together
	// only the largest single guarantee among them, as they are for CPU: at most
	// one of them can be handed the next job before the next plan, and memory
	// they are not using is the whole of what there is to lend. The oversubscription
	// that allows is bounded by the next plan, which charges whichever turned busy
	// in full, and by the agent's own look at the host before each raise.
	Idle bool
}

// MemoryPoolInput is one host at one heartbeat.
type MemoryPoolInput struct {
	// TotalMB is the host's memory, and FloorMB what a loan must leave free
	// (MemoryPoolFloor).
	TotalMB, FloorMB int64
	// StartReserveMB protects an imminent runner when compatible work is queued:
	// the guarantee of the largest one, as it is for CPU.
	StartReserveMB int64
	// AvailableMB is what the host measured as available now, nil when it did
	// not. A loan is made out of memory that is free, not out of memory the
	// ledger says nobody promised, so the smaller of the two is what there is.
	AvailableMB *int64
	Workloads   []MemoryWorkload
}

// MemoryPool is what a host can lend, and how that was worked out, in words the
// Hosts page can say.
type MemoryPool struct {
	// CapacityMB is the most that may be lent on the host in total, what is
	// lent already included. It is what an agent compares its own running total
	// with, so a loan it has made since the controller last counted still comes
	// out of it.
	CapacityMB int64
	// LentMB is what is lent now, and PoolMB what is left to lend: the shared
	// pool the runners that need memory draw on.
	LentMB, PoolMB int64
	// CommittedMB, IdleReserveMB and StartReserveMB are the lines of the
	// ledger the capacity was taken from, so a pool of nothing can say whose
	// guarantees it went to.
	CommittedMB, IdleReserveMB, StartReserveMB int64
	// Binding says what limited CapacityMB: MemoryBoundLedger when it was the
	// promises already made, MemoryBoundMeasured when it was the memory the host
	// measured as free.
	Binding MemoryBound
}

// MemoryBound names what limited a host's pool of memory.
type MemoryBound string

const (
	MemoryBoundLedger   MemoryBound = "ledger"
	MemoryBoundMeasured MemoryBound = "measured"
)

// PlanMemoryPool works out how much memory a host may lend: what is neither
// promised to a runner nor needed to leave the machine its floor, and is free
// now.
//
// It is the memory counterpart of ElasticCPUPlan, and the two differ in the
// one way that matters. CPU is lent and taken back by the same plan, one
// heartbeat at a time; memory cannot be taken back, so this does not say what
// each runner is to have -- it says how much the host as a whole may give away,
// and the agent hands it out as runners need it, bounded by the ceilings. What
// has been given away already comes off the pool, and never comes back until
// the runner that holds it is gone.
//
// Busy and starting runners are charged their guarantee in full; idle runners
// the largest one between them; one compatible queued start is protected. What
// is left of the machine beyond that, above its floor, is the capacity.
func PlanMemoryPool(in MemoryPoolInput) MemoryPool {
	out := MemoryPool{StartReserveMB: max(in.StartReserveMB, 0)}
	// The ledger is sums and one maximum, so the order the runners come in changes
	// nothing and nothing is sorted.
	for _, w := range in.Workloads {
		out.LentMB += max(w.LentMB, 0)
		if w.Idle {
			out.IdleReserveMB = max(out.IdleReserveMB, max(w.GuaranteeMB, 0))
			continue
		}
		out.CommittedMB += max(w.GuaranteeMB, 0)
	}

	ledger := max(in.TotalMB-max(in.FloorMB, 0)-out.CommittedMB-out.IdleReserveMB-out.StartReserveMB, 0)
	out.CapacityMB, out.Binding = ledger, MemoryBoundLedger
	if in.AvailableMB != nil {
		// What is free now, above the floor, is what can still be handed out;
		// what is lent already is in use, so it is added back to make a total.
		if measured := max(*in.AvailableMB-max(in.FloorMB, 0), 0) + out.LentMB; measured < out.CapacityMB {
			out.CapacityMB, out.Binding = measured, MemoryBoundMeasured
		}
	}
	out.PoolMB = max(out.CapacityMB-out.LentMB, 0)
	return out
}
