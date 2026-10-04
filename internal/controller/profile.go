package controller

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// A runner's size is decided from three places -- the fleet's runners.*
// settings, the host's runner profile and the pool -- and an operator reading a
// number needs to know which of them it came from before they know where to
// change it. These views carry each figure with its source, in the vocabulary
// scheduler.SourcePool, SourceHost and SourceGlobal, with an empty source
// meaning that nobody has said and the figure is zero.

// RunnerSizeView is a CPU and memory figure, as the API renders one.
type RunnerSizeView struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int64   `json:"memory_mb"`
}

// EffectiveSizeView is one tier of a host's effective profile: the figure in
// force on each field, and whose it is.
type EffectiveSizeView struct {
	CPUs           float64 `json:"cpus"`
	MemoryMB       int64   `json:"memory_mb"`
	CPUsSource     string  `json:"cpus_source,omitempty"`
	MemoryMBSource string  `json:"memory_mb_source,omitempty"`
}

// EffectiveStandardView is the standard tier, which carries the most CPU one
// runner may be lent as well.
type EffectiveStandardView struct {
	EffectiveSizeView
	BurstMaxCPUs       float64 `json:"burst_max_cpus,omitempty"`
	BurstMaxCPUsSource string  `json:"burst_max_cpus_source,omitempty"`
}

// EffectiveProfileView is what a runner on a host is held to, with the fleet's
// setting standing in wherever the host says nothing. It is what the host's
// own form shows beside the fields an operator can edit, so that an empty field
// reads as "follows the fleet's 2 CPU" rather than as nothing.
type EffectiveProfileView struct {
	Minimum  EffectiveSizeView     `json:"minimum"`
	Standard EffectiveStandardView `json:"standard"`
}

// sourced picks a figure and says whose it is: the host's where it set one, the
// fleet's where it did not, and nobody's -- zero, and an empty source -- where
// neither did.
func sourced[T float64 | int64](host, global T) (T, string) {
	switch {
	case host > 0:
		return host, scheduler.SourceHost
	case global > 0:
		return global, scheduler.SourceGlobal
	}
	return 0, ""
}

// EffectiveProfile resolves h's runner profile against the fleet's settings: the
// host's own figure on each field it names, and runners.minimum_* and
// runners.default_* where it does not.
//
// A host's minimum that follows the fleet is shown as such, but a pool that
// sets a minimum of its own still wins over the fleet's for the runners it
// places there: the host's own figure is a floor under the pool's, and the
// fleet's is only the answer the pool follows when it says nothing.
func EffectiveProfile(h *store.Host, fleet config.Runners) EffectiveProfileView {
	var out EffectiveProfileView
	min, std := h.RunnerProfile.Minimum, h.RunnerProfile.Standard
	out.Minimum.CPUs, out.Minimum.CPUsSource = sourced(min.CPUs, fleet.MinimumCPUs)
	out.Minimum.MemoryMB, out.Minimum.MemoryMBSource = sourced(min.MemoryMB, fleet.MinimumMemoryMB)
	defaultCPUs, defaultMemoryMB := fleet.DefaultRunnerSize()
	out.Standard.CPUs, out.Standard.CPUsSource = sourced(std.CPUs, defaultCPUs)
	out.Standard.MemoryMB, out.Standard.MemoryMBSource = sourced(std.MemoryMB, defaultMemoryMB)
	out.Standard.BurstMaxCPUs, out.Standard.BurstMaxCPUsSource = sourced(std.BurstMaxCPUs, 0)
	return out
}

// hostSlots says how many runners the host takes before any throttle and which
// limit sets that, so the host's card can say "capacity 4 is below the 6 its
// machine holds" instead of leaving an operator to find out why a host with a
// standard size runs fewer runners than it should.
//
// The limit is empty where there is no standard to derive from, and where the
// capacity and the machine agree.
func hostSlots(h *store.Host) (slots int, limitedBy string) {
	slots = h.Slots()
	std := h.RunnerProfile.Standard
	if !std.Sized() || h.Capacity <= 0 {
		return slots, ""
	}
	alloc := h.Allocatable()
	byCPU, byMemory := h.Capacity, h.Capacity
	if std.CPUs > 0 && alloc.CPUsKnown {
		byCPU = int((alloc.CPUs + 1e-6) / std.CPUs)
	}
	if std.MemoryMB > 0 && alloc.MemoryKnown {
		byMemory = int(alloc.MemoryMB / std.MemoryMB)
	}
	switch {
	case slots < h.Capacity && byCPU <= byMemory:
		return slots, "cpu"
	case slots < h.Capacity:
		return slots, "memory"
	case h.Capacity < min(byCPU, byMemory):
		// The machine holds more than the operator lets it take.
		return slots, "capacity"
	}
	return slots, ""
}

// PoolHostSizing is how one pool is sized on one host, with where each figure
// came from. It is the row an operator reads to see why a runner of this pool
// on that host is the size it is, and what they would change to move it.
type PoolHostSizing struct {
	// Standard is what one runner of the pool is given there: the pool's own
	// figure, the host's standard (or the fleet's default standing in for it)
	// for a pool that takes its size from the host, or one slot's share.
	Standard               RunnerSizeView `json:"standard"`
	StandardCPUsSource     string         `json:"standard_cpus_source,omitempty"`
	StandardMemoryMBSource string         `json:"standard_memory_mb_source,omitempty"`
	// Floor is the least a runner is given there: the larger of the pool's
	// minimum -- its own, or the fleet's where it follows it -- and the host's.
	Floor               RunnerSizeView `json:"floor"`
	FloorCPUsSource     string         `json:"floor_cpus_source,omitempty"`
	FloorMemoryMBSource string         `json:"floor_memory_mb_source,omitempty"`
	// Ceiling is the most CPU one runner may use there, its guarantee and a
	// loan together, for a pool that lends CPU: the smaller of the pool's and
	// the host's. Zero is "as much as the host's allocatable CPU".
	CeilingCPUs   float64 `json:"ceiling_cpus,omitempty"`
	CeilingSource string  `json:"ceiling_source,omitempty"`
}

// poolHostSizing resolves one pool on one host. raw is the pool as stored and p
// the copy the fleet sizes it by (sizingPool): the figures come from the copy
// and the sources from the pair, because a minimum the copy inherited is
// indistinguishable from the pool's own once it is there.
func poolHostSizing(raw, p *store.Pool, h *store.Host, fleet config.Runners) PoolHostSizing {
	var out PoolHostSizing
	charge := scheduler.Reserve(p, h)
	out.Standard = RunnerSizeView{CPUs: charge.CPUs, MemoryMB: charge.MemoryMB}
	stdCPUs, stdMemory := scheduler.StandardSize(p, h)
	hostStd := h.RunnerProfile.Standard
	switch {
	case p.SizeFromProfile:
		out.StandardCPUsSource = standardSource(stdCPUs, hostStd.CPUs > 0)
		out.StandardMemoryMBSource = standardSource(float64(stdMemory), hostStd.MemoryMB > 0)
	default:
		// Typed on the pool, or one slot's share of the host.
		out.StandardCPUsSource, out.StandardMemoryMBSource = scheduler.SourceHost, scheduler.SourceHost
		if raw.Resources.CPUs > 0 {
			out.StandardCPUsSource = scheduler.SourcePool
		}
		if raw.Resources.MemoryMB > 0 {
			out.StandardMemoryMBSource = scheduler.SourcePool
		}
	}

	poolCPUs, poolMemory := EffectiveMinimum(raw.Resources, fleet)
	poolCPUSource, poolMemorySource := scheduler.SourcePool, scheduler.SourcePool
	if raw.Resources.MinCPUs <= 0 {
		poolCPUSource = scheduler.SourceGlobal
	}
	if raw.Resources.MinMemoryMB <= 0 {
		poolMemorySource = scheduler.SourceGlobal
	}
	hostMin := h.RunnerProfile.Minimum
	out.Floor.CPUs, out.FloorCPUsSource = poolCPUs, orNone(poolCPUs, poolCPUSource)
	if hostMin.CPUs > poolCPUs {
		out.Floor.CPUs, out.FloorCPUsSource = hostMin.CPUs, scheduler.SourceHost
	}
	out.Floor.MemoryMB, out.FloorMemoryMBSource = poolMemory, orNone(float64(poolMemory), poolMemorySource)
	if hostMin.MemoryMB > poolMemory {
		out.Floor.MemoryMB, out.FloorMemoryMBSource = hostMin.MemoryMB, scheduler.SourceHost
	}

	if p.CPUBurst.Observes() {
		pool, host := p.CPUBurst.MaxCPUs, hostStd.BurstMaxCPUs
		out.CeilingCPUs = scheduler.BurstLimit(p, h)
		switch {
		case pool > 0 && host > 0 && host < pool:
			out.CeilingSource = scheduler.SourceHost
		case pool > 0:
			out.CeilingSource = scheduler.SourcePool
		case host > 0:
			out.CeilingSource = scheduler.SourceHost
		}
	}
	return out
}

// standardSource is the source of one field of a profile pool's standard: the
// host's own where its profile names it, the fleet's default where it does
// not, and the host's slot share -- which is the host's -- where not even the
// fleet's default was carried.
func standardSource(figure float64, fromHost bool) string {
	switch {
	case fromHost:
		return scheduler.SourceHost
	case figure > 0:
		return scheduler.SourceGlobal
	}
	return scheduler.SourceHost
}

// orNone is a source for a figure that may be zero: zero is nobody's.
func orNone(figure float64, source string) string {
	if figure <= 0 {
		return ""
	}
	return source
}

// profileProblems are the two things a runner profile can leave an operator
// without a way to see: a pool that has hosts and no host it can use, and a
// pool that takes its size from the host onto a host that gives it none.
//
// Both are asked of the pools and the hosts as they stand, and neither is
// raised for a fleet that has not used a profile: the first only where some
// host is kept off a pool by one, and the second only for a pool that asked to
// be sized by the host. That is what keeps the list the same on upgrade.
func profileProblems(hosts []*store.Host, pools []*store.Pool, fleet config.Runners) []Problem {
	var out []Problem
	for _, p := range pools {
		if p == nil || !p.Enabled || len(hosts) == 0 {
			continue
		}
		if prob, ok := noEligibleHostProblem(hosts, p); ok {
			out = append(out, prob)
		}
		if prob, ok := profileDefaultProblem(hosts, p, fleet); ok {
			out = append(out, prob)
		}
	}
	return out
}

// maxListedHosts keeps a sentence about a hundred hosts readable.
const maxListedHosts = 8

// noEligibleHostProblem is the pool that no host could ever run, where at least
// one host is left out by a runner profile. Each host is named with the limit
// that did it, in the same words the pool's explanation and the 409 on a host
// edit use, because "no eligible host" with no reasons is the complaint an
// operator cannot act on.
//
// A pool no host could run for reasons that have nothing to do with profiles --
// a selector that matches nothing, a backend nobody offers -- is the existing
// explanations' to give; this entry exists for the new reason, and lists the
// others beside it only so the whole picture is one sentence.
func noEligibleHostProblem(hosts []*store.Host, p *store.Pool) (Problem, bool) {
	var lines []string
	byProfile := 0
	for _, h := range hosts {
		if scheduler.HostCouldRun(h, p) {
			return Problem{}, false
		}
		code, reason := HostRefusal(h, p)
		if code == ExcludedProfile {
			byProfile++
		}
		if len(lines) < maxListedHosts {
			lines = append(lines, hostLabel(h)+": "+reason)
		}
	}
	if byProfile == 0 {
		return Problem{}, false
	}
	more := ""
	if n := len(hosts) - len(lines); n > 0 {
		more = fmt.Sprintf(" (and %s more)", plural(n, "host"))
	}
	return Problem{
		Code:     "pool.no_eligible_host",
		Severity: config.SeverityWarning,
		Title:    fmt.Sprintf("pool %s has no host that can run it", p.Name),
		Detail: fmt.Sprintf("No host can run this pool, and %d of the %s are kept off it by their runner profiles: %s%s. It looks healthy and starts no runner while this holds.",
			byProfile, plural(len(hosts), "host"), strings.Join(lines, "; "), more),
		Fix:        "change those hosts' minimum or standard runner size, or this pool's size or minimum, or add a host whose profile suits it.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// profileDefaultProblem is a pool that asked to be sized by its host, placed on
// hosts whose profiles name no standard size: they run it at the fleet's
// default, which is the same figure on a four-core box and a sixty-four-core
// one, and the pool did not ask for a figure that is the same everywhere.
func profileDefaultProblem(hosts []*store.Host, p *store.Pool, fleet config.Runners) (Problem, bool) {
	if !p.SizeFromProfile {
		return Problem{}, false
	}
	var names []string
	for _, h := range hosts {
		if h.RunnerProfile.Standard.Sized() || !scheduler.HostCouldRun(h, p) {
			continue
		}
		names = append(names, hostLabel(h))
	}
	if len(names) == 0 {
		return Problem{}, false
	}
	slices.Sort(names)
	cpus, memoryMB := fleet.DefaultRunnerSize()
	listed := names
	more := ""
	if len(listed) > maxListedHosts {
		more = fmt.Sprintf(" and %s more", plural(len(names)-maxListedHosts, "host"))
		listed = listed[:maxListedHosts]
	}
	return Problem{
		Code:     "pool.profile_default",
		Severity: config.SeverityInfo,
		Title:    fmt.Sprintf("pool %s takes its size from its host, and some hosts name none", p.Name),
		Detail: fmt.Sprintf("%s have no standard runner size in their profile (%s%s), so this pool's runners there are the fleet's default of %s CPU and %s -- the same on a small machine and a large one. The pool asked to be sized by the host, and these hosts have not said how.",
			plural(len(names), "host"), strings.Join(listed, ", "), more, scheduler.FormatCPUs(cpus), formatRoomMB(memoryMB)),
		Fix:        "give each of those hosts a standard runner size on its card, or set runners.default_cpus and runners.default_memory_mb to the size most of them should run.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}
