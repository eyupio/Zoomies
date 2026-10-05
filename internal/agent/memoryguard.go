package agent

import (
	"fmt"
	"math"
	"strings"
)

// The memory valve's guard is the one decision the agent makes for itself.
// Everything else about elastic memory is the controller's: which pools may be
// lent to, how much of a host is spare, how far one runner may go. But a kernel
// that is about to kill a compiler does not wait for a heartbeat thirty seconds
// away, so the agent watches each runner's memory itself and raises a limit
// when it has to, inside the rules the last heartbeat gave it.
//
// The decision is a function of its input -- no clock, no daemon, no host -- so
// that every one of its edges can be a row of a table.

// MemoryValveCode is a stable name for a guard decision, for metrics, tests and
// the Runners page. The controller reads it out of a runner's report, which is
// why it is part of the wire format.
type MemoryValveCode string

const (
	// MemoryHealthy: use is well inside the limit, and nothing was done.
	MemoryHealthy MemoryValveCode = "healthy"
	// MemoryRaised: the limit was raised out of the host's pool.
	MemoryRaised MemoryValveCode = "raised"
	// MemorySpilled: the limit could not be raised far enough and the runner was
	// pressing against it, so it was allowed some swap.
	MemorySpilled MemoryValveCode = "spilled"
	// MemoryAtCeiling: the runner wants more and already holds the most it may.
	MemoryAtCeiling MemoryValveCode = "at_ceiling"
	// MemoryPoolEmpty: the host has no spare memory left to lend.
	MemoryPoolEmpty MemoryValveCode = "pool_empty"
	// MemoryHostFloor: lending would leave the host less free memory than its floor.
	MemoryHostFloor MemoryValveCode = "host_floor"
	// MemoryUnmeasured: the host's free memory could not be read, and a loan
	// that cannot be checked against it is not made.
	MemoryUnmeasured MemoryValveCode = "unmeasured"
	// MemoryUnsupported: the container runtime refused to change a live limit.
	MemoryUnsupported MemoryValveCode = "unsupported"
	// MemoryFailed: the runtime refused for another reason, and will be asked again.
	MemoryFailed MemoryValveCode = "failed"
)

// The thresholds the guard works to. They are constants rather than settings:
// they are what makes the valve behave the same on every host, and a pool that
// wants more or less memory has the ceiling for it.
const (
	// MemoryMargin is how far above its use a container's limit is kept: a
	// third as much again, so that the allocation a compiler makes between two
	// looks is inside it. A faster spike than that can still hit the old limit,
	// which is a limit of the idea and not something a poll interval can fix.
	MemoryMargin = 1.3
	// MemoryHotShare is how much of its limit a container must be using to be
	// watched every second rather than every few.
	MemoryHotShare = 0.70
	// MemoryStepMB is the granularity of a raise. Use creeping up a few
	// megabytes at a time would otherwise cost an update for each.
	MemoryStepMB = 64
	// MemoryMinimumRaiseMB is the least a raise adds when it has the room for
	// more, for the same reason: a raise is a call to the daemon, and one that
	// buys a second of margin is not worth it.
	MemoryMinimumRaiseMB = 128
	// MemorySpillShare is how much of its limit a container that cannot be
	// raised further must be using before it is given swap. Short of the whole
	// of it, because swap is the slower way out and is spent when a kill is
	// close, not when memory is merely tight.
	MemorySpillShare = 0.90
)

// MemoryGuardInput is one container at one look. A docker-in-docker runner is
// two containers, each looked at alone, with HeldMB and CeilingMB the pair's.
type MemoryGuardInput struct {
	// UsageMB is the container's working set: its memory without the page
	// cache the kernel gives back when asked.
	UsageMB int64
	// LimitMB is its memory limit now, and SwapMB the swap it may use beyond
	// that. A container with no memory limit is not the guard's business and
	// is never passed in.
	LimitMB, SwapMB int64
	// HeldMB is the limits of every container of this runner added together,
	// and CeilingMB the most they may come to (scheduler.MemoryCeiling).
	HeldMB, CeilingMB int64
	// PoolMB is what the host may still lend: its capacity less what its
	// runners hold already.
	PoolMB int64
	// AvailableMB is the host's free memory as it is right now and FloorMB the
	// least a loan may leave it. Known is false when it could not be read.
	AvailableMB, FloorMB int64
	AvailableKnown       bool
	// SpillMB is the swap the pool allows this container beyond its limit, and
	// SwapFreeMB the swap the host has free; SwapKnown is false when the host's
	// swap could not be read.
	SpillMB, SwapFreeMB int64
	SwapKnown           bool
}

// MemoryGuardDecision is what the guard would do. LimitMB and SwapMB are what
// the container should have afterwards: never less than it has now, because
// lowering a live limit is what kills the job a loan is meant to save.
type MemoryGuardDecision struct {
	LimitMB, SwapMB int64
	Code            MemoryValveCode
	// Reason is the sentence for an operator: why the limit moved, or why it
	// did not when the container wanted it to.
	Reason string
}

// Changed reports whether the decision asks the daemon for anything.
func (d MemoryGuardDecision) Changed(in MemoryGuardInput) bool {
	return d.LimitMB > in.LimitMB || d.SwapMB > in.SwapMB
}

// MemoryWanted is the limit the guard would keep a container at, given what it
// is using: use plus the margin, rounded up to a step.
func MemoryWanted(usageMB int64) int64 {
	if usageMB <= 0 {
		return 0
	}
	want := int64(math.Ceil(float64(usageMB) * MemoryMargin))
	return (want + MemoryStepMB - 1) / MemoryStepMB * MemoryStepMB
}

// MemoryHot reports whether a container is using enough of its limit to be
// watched closely.
func MemoryHot(usageMB, limitMB int64) bool {
	return limitMB > 0 && float64(usageMB) >= MemoryHotShare*float64(limitMB)
}

// GuardMemory decides whether to raise one container's limit.
//
// A container keeps its limit a third above its use. While it does, nothing
// happens. When use climbs past that point the limit is raised to restore the
// margin, out of the host's pool, within the runner's ceiling, and only so far
// as the host can spare the memory above its floor right now -- the pool is the
// controller's idea of what is spare, and the free memory is the machine's, and
// a loan is made out of the smaller.
//
// If that cannot cover what the container wants and it is nearly at its limit,
// and its pool allows swap and the host has some, it is allowed swap: the last
// resort, which turns a kill into a slowdown.
//
// A decision is always at least what the container has. The guard never asks
// for less, whatever use does.
func GuardMemory(in MemoryGuardInput) MemoryGuardDecision {
	out := MemoryGuardDecision{LimitMB: in.LimitMB, SwapMB: in.SwapMB, Code: MemoryHealthy}
	if in.LimitMB <= 0 {
		return out
	}
	want := MemoryWanted(in.UsageMB)
	if in.LimitMB >= want {
		return out
	}

	// What it wants, and the four things that can say no. They are asked in the
	// order an operator would fix them: the runner's own ceiling, then the
	// host's pool, then whether the host can be measured, then its floor.
	spare := in.AvailableMB - in.FloorMB
	room := max(in.CeilingMB-in.HeldMB, 0)
	var blocked MemoryValveCode
	var because string
	switch {
	case room <= 0:
		blocked, because = MemoryAtCeiling, fmt.Sprintf("it holds %d MB, the most this runner may have", in.HeldMB)
	case in.PoolMB <= 0:
		blocked, because = MemoryPoolEmpty, "the host has no spare memory left to lend"
	case !in.AvailableKnown:
		blocked, because = MemoryUnmeasured, "the host's free memory could not be measured, so none is lent"
	case spare <= 0:
		blocked, because = MemoryHostFloor, fmt.Sprintf("the host has %d MB free and keeps %d MB back", in.AvailableMB, in.FloorMB)
	}
	var said []string
	if blocked == "" {
		give := max(want-in.LimitMB, MemoryMinimumRaiseMB)
		give = min(give, room, in.PoolMB, spare)
		out.LimitMB, out.Code = in.LimitMB+give, MemoryRaised
		said = append(said, fmt.Sprintf("raised the limit from %d to %d MB: it was using %d MB", in.LimitMB, out.LimitMB, in.UsageMB))
		if out.LimitMB >= want {
			out.Reason = said[0]
			return out
		}
		// Short of what it wanted: whichever of the three bound the raise is
		// why, and the first that did is named.
		switch give {
		case room:
			blocked, because = MemoryAtCeiling, fmt.Sprintf("that is the most this runner may have (%d MB)", in.CeilingMB)
		case in.PoolMB:
			blocked, because = MemoryPoolEmpty, "that was the last of what the host has to lend"
		default:
			blocked, because = MemoryHostFloor, fmt.Sprintf("that leaves the host its floor of %d MB", in.FloorMB)
		}
		said = append(said, because)
	} else {
		out.Code = blocked
		said = append(said, fmt.Sprintf("it is using %d MB of its %d MB limit and wants more, but %s", in.UsageMB, in.LimitMB, because))
	}

	// Not enough, or nothing at all. A kill is close if the container is
	// pressing against what it has now, and swap is the way out that costs
	// speed rather than the job.
	if float64(in.UsageMB) >= MemorySpillShare*float64(out.LimitMB) {
		allowance := min(in.SpillMB, in.SwapFreeMB)
		switch {
		case in.SpillMB <= 0:
			// The pool does not allow swap, so there is nothing to add.
		case !in.SwapKnown || in.SwapFreeMB <= 0:
			said = append(said, "swap is allowed but the host has none free")
		case allowance > in.SwapMB:
			out.SwapMB, out.Code = allowance, MemorySpilled
			said = append(said, fmt.Sprintf("allowed %d MB of swap beyond its %d MB limit", allowance, out.LimitMB))
		}
	}
	out.Reason = strings.Join(said, "; ")
	return out
}
