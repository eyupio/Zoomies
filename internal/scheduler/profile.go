package scheduler

import (
	"fmt"

	"github.com/eyupio/zoomies/internal/store"
)

// Where one figure of a runner's size came from, in the words the API and the
// problems use. The scheduler's own copies of a pool do not carry it -- a
// minimum inherited from the fleet looks like the pool's own once it is on the
// copy -- so the controller works provenance out from the stored pool, the
// host's profile and the fleet's settings, and these are only the vocabulary.
const (
	// SourcePool is a figure the pool itself states.
	SourcePool = "pool"
	// SourceHost is a figure the host's runner profile states, or one cut from
	// the host's own slots.
	SourceHost = "host"
	// SourceGlobal is the fleet's own setting (runners.*), where neither the
	// pool nor the host said anything.
	SourceGlobal = "global"
)

// sizedOn returns p as it is sized on h: the pool with the larger of its own
// minimum and the host's, on each field the host has one for. The host never
// lowers a minimum, and it never raises one past a size the pool states --
// that pool is not placed here at all (ExcludedBySize), because a runner of a
// pool that said how big it is must not be made bigger by the machine it
// happens to land on.
//
// It is what makes "the larger of the two minimums" true of every sum below
// without each of them knowing about hosts: a share thinner than the minimum
// is raised to it, a runner is reduced no further than it, and the charge
// follows, all from the one pool the existing code already reads. It is
// idempotent, so a function that calls another that also applies it is
// harmless, and it returns p itself when the host adds nothing.
func sizedOn(p *store.Pool, h *store.Host) *store.Pool {
	if p == nil || h == nil {
		return p
	}
	floor := h.RunnerProfile.Minimum
	cpus := raisedMinimum(p.Resources.MinCPUs, floor.CPUs, p.Resources.CPUs)
	memoryMB := int64(raisedMinimum(float64(p.Resources.MinMemoryMB), float64(floor.MemoryMB), float64(p.Resources.MemoryMB)))
	if cpus == p.Resources.MinCPUs && memoryMB == p.Resources.MinMemoryMB {
		return p
	}
	cp := *p
	cp.Resources.MinCPUs, cp.Resources.MinMemoryMB = cpus, memoryMB
	return &cp
}

// raisedMinimum is one field of sizedOn: the pool's minimum, or the host's where
// that is larger and does not pass the size the pool states (typed, zero when
// the pool leaves the field to the host).
func raisedMinimum(own, host, typed float64) float64 {
	if host <= own || (typed > 0 && host > typed) {
		return own
	}
	return host
}

// StandardSize is the size of one runner of a profile pool on h, per field: the
// host's own standard where its profile names one, and the fleet's default
// where it does not -- carried on the pool, Pool.FleetStandard, because the
// scheduler cannot read settings. Zero on a field where neither says, and on
// every pool that does not take its size from the host; the callers read zero
// as "a slot's share", which is how such a pool was sized before profiles.
func StandardSize(p *store.Pool, h *store.Host) (cpus float64, memoryMB int64) {
	if p == nil || !p.SizeFromProfile {
		return 0, 0
	}
	cpus, memoryMB = p.FleetStandard.CPUs, p.FleetStandard.MemoryMB
	if h != nil {
		if c := h.RunnerProfile.Standard.CPUs; c > 0 {
			cpus = c
		}
		if m := h.RunnerProfile.Standard.MemoryMB; m > 0 {
			memoryMB = m
		}
	}
	return cpus, memoryMB
}

// slotSize is what one slot of h is worth to p: the host's slot share, with the
// standard size laid over it for a pool that takes its size from the host. It
// is HostShare as the pool sees it, and every figure that was a slot's share
// for an automatic pool reads from here, so the two modes cannot disagree
// about what a runner is on the same machine.
func slotSize(p *store.Pool, h *store.Host) store.Resources {
	out := HostShare(h)
	cpus, memoryMB := StandardSize(p, h)
	if cpus > 0 {
		out.CPUs = cpus
	}
	if memoryMB > 0 {
		out.MemoryMB = memoryMB
	}
	return out
}

// SizeExclusion is a limit that keeps a pool off a host because of what the
// two say about runner size: the pool states a size the host's minimum is
// above, or the pool takes its size from the host and the host's standard is
// below the pool's floor. It names the limit, because "this host was left out"
// is a complaint and "its minimum runner is 3 CPU and the pool gives 2" is
// something an operator can act on.
type SizeExclusion struct {
	// Code is "host_minimum" for the first and "host_standard" for the second.
	Code string
	// Field is "cpu" or "memory": the dimension that did it.
	Field string
	// Reason finishes "this host is not used because": a lower-case sentence
	// that begins "its", like HostShortfall's, and says what to change.
	Reason string
}

const (
	// ExclusionHostMinimum is a host whose minimum runner is above the size a
	// pool states.
	ExclusionHostMinimum = "host_minimum"
	// ExclusionHostStandard is a host whose standard runner is below the floor
	// of a pool that takes its size from the host.
	ExclusionHostStandard = "host_standard"
)

// ExcludedBySize reports whether h's runner profile keeps p off it, and which
// limit does. It is nil for every host without a profile and for every pool
// that is not asking about one of its figures, so it changes nothing until an
// operator has said something.
//
// A pool that states its size is never silently made bigger -- the host's
// minimum above it excludes the host rather than raising the runner -- and a
// pool that takes its size from the host is never silently made smaller than
// its own floor: a host whose standard is below that floor is not one it can
// use. A pool that leaves its size to a slot's share is in neither case: its
// runner is the share, and a minimum above that raises the share
// (MinimumSlot) rather than excluding the host.
func ExcludedBySize(h *store.Host, p *store.Pool) *SizeExclusion {
	if h == nil || p == nil {
		return nil
	}
	floor := h.RunnerProfile.Minimum
	switch {
	case p.SizeFromProfile:
		cpus, memoryMB := StandardSize(p, h)
		poolMin := p.Resources.MinCPUs
		if cpus > 0 && max(poolMin, floor.CPUs) > cpus+cpuEpsilon {
			whose, need := poolsOrFleets(p.FleetMinimum.CPUs > 0), poolMin
			if floor.CPUs >= poolMin {
				whose, need = "this host's own", floor.CPUs
			}
			return &SizeExclusion{ExclusionHostStandard, "cpu", fmt.Sprintf(
				"%s, below %s minimum of %s CPU -- give the host a standard runner of at least that, or lower the minimum",
				standardPhrase(formatCPUs(cpus)+" CPU", h.RunnerProfile.Standard.CPUs > 0), whose, formatCPUs(need))}
		}
		poolMinMemory := p.Resources.MinMemoryMB
		if memoryMB > 0 && max(poolMinMemory, floor.MemoryMB) > memoryMB {
			whose, need := poolsOrFleets(p.FleetMinimum.MemoryMB > 0), poolMinMemory
			if floor.MemoryMB >= poolMinMemory {
				whose, need = "this host's own", floor.MemoryMB
			}
			return &SizeExclusion{ExclusionHostStandard, "memory", fmt.Sprintf(
				"%s, below %s minimum of %s -- give the host a standard runner of at least that, or lower the minimum",
				standardPhrase(formatMB(memoryMB)+" of memory", h.RunnerProfile.Standard.MemoryMB > 0), whose, formatMB(need))}
		}
	case !p.Automatic():
		if p.Resources.CPUs > 0 && floor.CPUs > p.Resources.CPUs+cpuEpsilon {
			return &SizeExclusion{ExclusionHostMinimum, "cpu", fmt.Sprintf(
				"its minimum runner size is %s CPU, above the %s CPU this pool gives each runner, and a pool that states its size is never given more than it states -- lower the host's minimum, or raise the pool's CPU",
				formatCPUs(floor.CPUs), formatCPUs(p.Resources.CPUs))}
		}
		if p.Resources.MemoryMB > 0 && floor.MemoryMB > p.Resources.MemoryMB {
			return &SizeExclusion{ExclusionHostMinimum, "memory", fmt.Sprintf(
				"its minimum runner size is %s of memory, above the %s this pool gives each runner, and a pool that states its size is never given more than it states -- lower the host's minimum, or raise the pool's memory",
				formatMB(floor.MemoryMB), formatMB(p.Resources.MemoryMB))}
		}
	}
	return nil
}

// poolsOrFleets names whose minimum a figure is: the pool's own, or the
// fleet's where the pool follows it -- an operator told "this pool's minimum"
// would go looking for a field the pool never set.
func poolsOrFleets(fleet bool) string {
	if fleet {
		return "the fleet's"
	}
	return "this pool's"
}

// standardPhrase says what a host's standard runner is, and where the figure
// came from, because a host that names none is sized by the fleet and a
// sentence that blamed its "standard" would send an operator to a field the
// host does not have. The figure carries its unit.
func standardPhrase(figure string, fromHost bool) string {
	if fromHost {
		return "its standard runner is " + figure
	}
	return "its profile names no standard runner size, so the fleet's default of " + figure + " applies"
}

// BurstLimit is the most CPU one runner of p may use on h -- its guarantee and
// whatever it is lent together -- where either side has said: the smaller of
// the pool's ceiling and the host's. An unset side does not count, so a pool
// with a ceiling and a host with none keeps the pool's, and neither set is
// zero, which callers read as "as much as the host's allocatable CPU".
//
// The host's ceiling can only lower the pool's, never raise it: whoever owns
// the machine has the last word on how much of it one job may take, and a
// pool asking for more than the host will give simply gets what it will.
func BurstLimit(p *store.Pool, h *store.Host) float64 {
	pool := 0.0
	if p != nil {
		pool = p.CPUBurst.MaxCPUs
	}
	host := 0.0
	if h != nil {
		host = h.RunnerProfile.Standard.BurstMaxCPUs
	}
	switch {
	case pool > 0 && host > 0:
		return min(pool, host)
	case pool > 0:
		return pool
	default:
		return host
	}
}
