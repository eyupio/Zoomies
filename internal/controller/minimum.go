package controller

import (
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// EffectiveMinimum is the minimum size a pool's runners are actually held to:
// the pool's own figure on a field where it set one, and otherwise the fleet's
// runners.minimum_cpus / runners.minimum_memory_mb.
//
// The fleet figure is read live rather than copied into the pool when it is
// created, so changing it moves every pool that never said otherwise on the
// next pass -- and saving a pool never freezes today's value into it. A fleet
// minimum at or above a pool's typed standard size means nothing for that pool
// (the same rule the wizard's opening value follows), and is ignored there. On
// an automatic field it applies as-is: below a host's slot share it is how far
// a runner may be reduced, and above it the scheduler gives each runner the
// minimum (scheduler.MinimumSlot).
func EffectiveMinimum(r store.Resources, fleet config.Runners) (cpus float64, memoryMB int64) {
	cpus, memoryMB = r.MinCPUs, r.MinMemoryMB
	if cpus <= 0 && fleet.MinimumCPUs > 0 && (r.CPUs <= 0 || fleet.MinimumCPUs < r.CPUs) {
		cpus = fleet.MinimumCPUs
	}
	if memoryMB <= 0 && fleet.MinimumMemoryMB > 0 && (r.MemoryMB <= 0 || fleet.MinimumMemoryMB < r.MemoryMB) {
		memoryMB = fleet.MinimumMemoryMB
	}
	return cpus, memoryMB
}

// sizingPool returns p as the fleet sizes it: a copy with the inherited
// minimum filled in, and -- for a pool that takes its size from the host --
// the fleet's default runner size laid beside it for a host whose profile names
// none. It is p itself when nothing is inherited. It is for the callers that
// decide or report on a runner's size -- scheduling, room, fit, problems -- and
// never for one that stores, exports or renders the pool's own settings: a copy
// written back would turn "the fleet's figure" into a figure of the pool's own,
// and the fleet setting would stop moving it.
//
// The copy also remembers which minimum was the fleet's (Pool.FleetMinimum),
// so that a host left out of a pool for being under it can say whose figure it
// was under.
func sizingPool(p *store.Pool, fleet config.Runners) *store.Pool {
	if p == nil {
		return nil
	}
	cpus, memoryMB := EffectiveMinimum(p.Resources, fleet)
	var standard, inherited store.RunnerSize
	if p.SizeFromProfile {
		standard.CPUs, standard.MemoryMB = fleet.DefaultRunnerSize()
	}
	if p.Resources.MinCPUs <= 0 && cpus > 0 {
		inherited.CPUs = cpus
	}
	if p.Resources.MinMemoryMB <= 0 && memoryMB > 0 {
		inherited.MemoryMB = memoryMB
	}
	if cpus == p.Resources.MinCPUs && memoryMB == p.Resources.MinMemoryMB &&
		standard == p.FleetStandard && inherited == p.FleetMinimum {
		return p
	}
	cp := *p
	cp.Resources.MinCPUs, cp.Resources.MinMemoryMB = cpus, memoryMB
	cp.FleetStandard, cp.FleetMinimum = standard, inherited
	return &cp
}

// sizingPools is sizingPool over a listing, against the settings in force now.
func (c *Controller) sizingPools(pools []*store.Pool) []*store.Pool {
	fleet := c.cfg().Runners
	out := make([]*store.Pool, len(pools))
	for i, p := range pools {
		out[i] = sizingPool(p, fleet)
	}
	return out
}
