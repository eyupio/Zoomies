package scheduler

import (
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The throttle ladder. Each constant was chosen against another, and the
// relationships are pinned by tests in internal/controller (invariants_test.go)
// and documented in docs/architecture.md.
const (
	// ThrottleStep is the least time between one rung and the next up. The
	// CPU hold that feeds it needs 30 s of saturation, so the first rung can
	// come half a minute after the pressure starts; the second waits for the
	// first to have had an effect, because a quota lowered on a live
	// container takes a scheduling period or two to show in a load average
	// that itself averages over a minute.
	ThrottleStep = 2 * time.Minute
	// ThrottleRecovery is how long a host has to stay calm before it climbs
	// down one rung. Longer than a step up on purpose: a host that recovered
	// in a minute and was pushed straight back over would otherwise oscillate
	// with the ladder rather than settle on it.
	ThrottleRecovery = 5 * time.Minute
	// StaleThrottleReset is how long a host may go without a fresh usage
	// sample before its throttle is lifted anyway. The measurements the
	// ladder climbs on have stopped coming -- an agent downgraded to a build
	// that does not send them, or a host that went away -- and a host that
	// cannot be measured is placed by its configured capacity, exactly as the
	// holds already fall back. It is longer than the time a host is given up
	// on as lost, so a host that comes back is not still throttled for
	// pressure nobody can see.
	StaleThrottleReset = 10 * time.Minute

	// LoadPerCPUOverwhelmed is the one-minute load average per CPU at which a
	// host counts as overwhelmed: twice as many runnable tasks as there are
	// cores to run them, which is the shape a machine takes when its CPU
	// occupancy has already pinned at 100% and stopped saying anything.
	// LoadPerCPUCalm is where it is calm again -- one runnable task per core,
	// where nothing is queued behind anything.
	LoadPerCPUOverwhelmed = 2.0
	LoadPerCPUCalm        = 1.0
	// cpuCalmPercent is the CPU occupancy below which a host is calm, and it
	// is the same figure that releases the admission hold, so the two never
	// disagree about whether a host has recovered.
	cpuCalmPercent = 85.0
)

// NextThrottle is the ladder: given a host as it stands, with the measurements
// its agent last sent and the rung it is on, it returns the rung it should be
// on now. It is pure, and the controller persists the result when it differs.
//
// Three things can be true of a host with a fresh sample. It is overwhelmed:
// the CPU hold has tripped, the load average is past twice its cores, or its
// memory is at the reserve -- and then it climbs a rung, if ThrottleStep has
// passed since the last one. It is calm: CPU under 85%, load under one per
// core, memory above the reserve -- and then a calm streak runs, and after
// ThrottleRecovery it climbs down a rung and the streak starts again. Or it
// is neither, in the band between, where the rung is kept and the streak is
// broken: a host at 90% CPU is not overwhelmed, and it is not a host to give
// slots back to either.
//
// Without a fresh sample nothing moves, until StaleThrottleReset has passed
// since the last one; then the throttle is lifted, because a host nobody can
// measure is placed by its configured capacity.
func NextThrottle(h *store.Host, now time.Time) store.HostThrottle {
	t := h.Throttle
	if !h.Usage.Fresh(now) {
		if t.Active() && !h.Usage.SampledAt.IsZero() && now.Sub(h.Usage.SampledAt) >= StaleThrottleReset {
			return store.HostThrottle{}
		}
		if t.Active() && h.Usage.SampledAt.IsZero() {
			// Nothing was ever measured, so nothing can have decided this;
			// a row can only look like this after the usage was cleared.
			return store.HostThrottle{}
		}
		return t
	}
	overwhelmed, reason := hostOverwhelmed(h)
	switch {
	case overwhelmed:
		t.CalmSince = nil
		if t.Level >= store.MaxThrottleLevel {
			return t
		}
		if t.ChangedAt != nil && now.Sub(*t.ChangedAt) < ThrottleStep {
			return t
		}
		t.Level++
		at := now
		t.ChangedAt = &at
		t.Reason = reason
		if t.Since == nil {
			since := now
			t.Since = &since
		}
		return t
	case hostCalm(h):
		if !t.Active() {
			return store.HostThrottle{}
		}
		if t.CalmSince == nil {
			since := now
			t.CalmSince = &since
			return t
		}
		if now.Sub(*t.CalmSince) < ThrottleRecovery {
			return t
		}
		t.Level--
		at := now
		t.ChangedAt = &at
		t.CalmSince = &at
		if t.Level <= 0 {
			return store.HostThrottle{}
		}
		return t
	default:
		t.CalmSince = nil
		return t
	}
}

// hostOverwhelmed reports whether a host's fresh measurements say it has been
// pushed past what it can do, and which measurement says so, in the words the
// throttle's reason carries.
//
// Overwhelmed means pressure beyond the plan, not use of it. A host whose
// every runner is inside its CPU quota is running exactly the work it was
// sized for, and with the quotas summing to the machine less its reserve that
// work sits at 95% CPU all day: a hold that read that as overload would
// throttle a healthy host, watch it calm down, lift the throttle and throttle
// it again, for ever. So the CPU hold counts only while the host carries a
// runner with no quota -- the one kind of runner that can take more than its
// share -- and the load average is what says a host has been pushed past its
// cores by anything at all, quotas included: CFS dequeues a throttled task, so
// load climbs past the core count only when more is runnable than the plan
// allows for.
func hostOverwhelmed(h *store.Host) (bool, string) {
	u := h.Usage
	if u.CPUHeld && h.UnlimitedRunners > 0 {
		return true, fmt.Sprintf("CPU has been at or above 95%% for 30 s with %s here that no CPU limit binds",
			plural(h.UnlimitedRunners, "runner"))
	}
	if u.LoadAverage1 != nil && h.CPUs > 0 && *u.LoadAverage1 >= LoadPerCPUOverwhelmed*float64(h.CPUs) {
		return true, fmt.Sprintf("the 1-minute load average is %.1f, at least twice the host's %d CPUs", *u.LoadAverage1, h.CPUs)
	}
	if v := u.MemoryAvailableMB; v != nil && *v <= h.MemoryReserve() {
		return true, "available memory is at or below the host's reserve"
	}
	return false, ""
}

// hostCalm reports whether every fresh measurement says the host has room
// again. Each figure is judged only where it was measured: a host that reports
// CPU alone is calm when its CPU is.
//
// The CPU bar is reachable from every rung: a throttled host's quotas sum to
// three quarters of the machine or less, so a host whose runners are all
// inside them falls under 85% the moment the quotas take. A host that stays
// above it is carrying something the quotas do not bind.
func hostCalm(h *store.Host) bool {
	u := h.Usage
	if u.CPUHeld {
		return false
	}
	// Judged without CPU runners are using because it was lent them: the
	// throttle withdraws every boost anyway, and counting it here would hold a
	// host on its rung for doing what the plan asked.
	if v := u.UnlentCPUPercent(); v != nil && *v >= cpuCalmPercent {
		return false
	}
	if u.LoadAverage1 != nil && h.CPUs > 0 && *u.LoadAverage1 >= LoadPerCPUCalm*float64(h.CPUs) {
		return false
	}
	if v := u.MemoryAvailableMB; v != nil && *v <= h.MemoryReserve() {
		return false
	}
	return true
}

// ThrottleFloorFactor is the least CPU factor a throttle may apply on a host
// whose profile sets a minimum runner size: the factor that leaves the smallest
// limited container on the host exactly that minimum. It is zero where there is
// no minimum or nothing limited to protect, and never above one -- a container
// already under the minimum cannot be helped by a factor, so the CPU part of
// the throttle simply stands down while it runs and the slot part of it goes
// on.
//
// The factor is the host's and one number reaches every runner on it, so the
// smallest container sets it and the larger ones are throttled a little less
// than the ladder alone would have. That is the safe direction: a throttle
// exists to stop a host being overwhelmed, and the minimum exists to stop a
// runner being too small to be of use, and a runner throttled below the size
// its host says is the least it may have is the second failure bought with the
// first.
//
// containerCPUs are the CPU quotas each limited container of the host's live
// runners was created with; a zero is a container with no quota, which there is
// nothing to scale.
func ThrottleFloorFactor(minimumCPUs float64, containerCPUs []float64) float64 {
	if minimumCPUs <= 0 {
		return 0
	}
	factor := 0.0
	for _, cpus := range containerCPUs {
		if cpus > 0 {
			factor = max(factor, minimumCPUs/cpus)
		}
	}
	return min(factor, 1)
}

// ThrottleReason is the throttle as one sentence for the Hosts page and the
// problems drawer: what it took, why, what it is doing to the jobs already
// running, and how it ends. Empty when the host is not throttled.
//
// The clause about running jobs is only said where it can be true. A runner
// with no CPU limit has nothing to scale, and a host that offers only the
// process backend has no container to update, so an operator there is told
// what the throttle took and not what it cannot do.
func ThrottleReason(h *store.Host) string {
	t := h.Throttle
	if !t.Active() {
		return ""
	}
	slots := fmt.Sprintf("throttled to %d of %d slots (step %d of %d) after sustained pressure: %s",
		h.EffectiveCapacity(), h.Slots(), t.Level, store.MaxThrottleLevel, t.Reason)
	jobs := "running jobs continue"
	if limited := h.ActiveRunners - h.UnlimitedRunners; limited > 0 && hostCanThrottleContainers(h) {
		jobs = fmt.Sprintf("running jobs continue, the %s with a CPU limit at %d%% of it",
			plural(limited, "runner"), int(t.CPUFactor()*100))
		if floor := h.RunnerProfile.Minimum.CPUs; floor > 0 {
			// Said here because the percentage above is the ladder's, and the
			// host's own minimum is what can make it less -- an operator who
			// sees a runner at more than that percentage should find why on
			// the same line.
			jobs += fmt.Sprintf(", but never below this host's minimum of %s CPU a runner", formatCPUs(floor))
		}
	}
	return fmt.Sprintf("%s; %s, and the throttle lifts one step after %s of calm",
		slots, jobs, formatDuration(ThrottleRecovery))
}

// hostCanThrottleContainers reports whether the host offers a backend whose
// workloads have a quota to lower: any container backend does, and the
// process backend does not.
func hostCanThrottleContainers(h *store.Host) bool {
	for _, kind := range h.Backends {
		if kind != string(store.BackendProcess) {
			return true
		}
	}
	return false
}
