package controller

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// A Docker-in-Docker runner is two containers dividing one slot, and the pool's
// smallest runner is a figure per container. So a thin sidecar multiplies it: a
// 1.5 GB minimum with a sidecar holding 15% of the memory is a request for a slot
// of 10 GB, because only a 10 GB slot gives the thinner half 1.5. Nothing about
// that is wrong while the jobs need it. It is a trap when they have never come near
// it, and the cost is not paid by the pool but by its machines, which hold fewer
// runners than they could while jobs wait.
//
// Only memory is judged. Memory is a limit a job is killed at, so what jobs have used
// is evidence of what a slot has to hold. CPU is not: a build's CPU peak is a burst
// the elastic valve lends for, and a slot is not sized by it.

const (
	// minimumEvidenceWindow is how far back the jobs counted run. A week spans the
	// regular builds and the weekly one.
	minimumEvidenceWindow = 7 * 24 * time.Hour
	// minimumEvidenceJobs is how many jobs with a measured peak it takes to say what
	// a pool's jobs need. A pool with a dozen has not shown its heaviest one, and a job
	// too short to be sampled has no peak to count.
	minimumEvidenceJobs = 20
	// minimumHeadroom is what a slot is kept above the most a job has used, because
	// the next job is allowed to be somewhat heavier than the heaviest so far.
	minimumHeadroom = 1.5
	// minimumReductionWorth is the least fall in the slot's memory floor worth
	// proposing, as a fraction: a change that moves the floor a few percent is a
	// number to second-guess for no gain.
	minimumReductionWorth = 0.25
)

// halfPeaks is the most memory each container of a pair used in the window.
type halfPeaks struct{ runnerMB, daemonMB int64 }

// pairMemoryPeaks is what each half of a pool's pairs used, at most, over the
// samples still in the window that were taken at the share in force now. It says
// false until there are enough samples from enough runners to be about the pool
// rather than one job. The samples are the last few hours and the job evidence is a
// week, so this is the part of the evidence that can be short; the headroom is what
// covers a heavier job than seen.
//
// The division is the share of the slot, not the daemon's limit in bytes: a pool
// that spans two sizes of host has a different limit on each and one share. Keyed
// on the limit, which host reported last decided which machines the evidence came
// from -- and the small, floor-bound ones, where a minimum is what sizes the slot,
// were the ones dropped. The peak is then the largest in megabytes over all of
// them, which is the conservative figure for a floor.
func (c *Controller) pairMemoryPeaks(poolID string) (halfPeaks, bool) {
	c.pairMu.Lock()
	window := slices.Clone(freshPairs(c.pairs[poolID], c.Now()))
	c.pairMu.Unlock()
	if len(window) == 0 {
		return halfPeaks{}, false
	}
	current := pairMemShare(window[len(window)-1])
	if current == 0 {
		return halfPeaks{}, false
	}
	var out halfPeaks
	var cover pairCover
	for _, s := range window {
		if share := pairMemShare(s); share == 0 || !sameShare(share, current) {
			continue
		}
		cover.add(s)
		out.runnerMB = max(out.runnerMB, s.halves.Runner.MemoryBytes>>20)
		out.daemonMB = max(out.daemonMB, s.halves.Daemon.MemoryBytes>>20)
	}
	return out, cover.enough()
}

// poolMinimumAdviceProblems is pool.minimum_overcharges.
func (c *Controller) poolMinimumAdviceProblems(ctx context.Context, out *[]Problem) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	// Decide who could be advised before reading anything about jobs: the week's
	// statistics are the dearest query in the problems pass, and most fleets have no
	// pool this is about. The pools it reads them for are then named, so it costs what
	// it concerns and not every job the fleet ran.
	var candidates []*store.Pool
	for _, p := range pools {
		if p.Enabled && p.DockerMode == store.DockerDinD && p.Automatic() && !p.FromHosts() {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	pressure, err := c.poolsUnderPressure(ctx)
	if err != nil {
		return err
	}
	var ids []string
	for _, p := range candidates {
		if pressure[p.ID] {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	since := c.Now().Add(-minimumEvidenceWindow)
	stats, err := c.st.JobStats(ctx, store.JobFilter{Since: &since, PoolIDs: ids}, []string{store.GroupByPool})
	if err != nil {
		return fmt.Errorf("reading what jobs used: %w", err)
	}
	byPool := map[string]store.JobStatsGroup{}
	for _, g := range stats.Groups {
		byPool[g.Keys[store.GroupByPool]] = g
	}
	fleet := c.cfg().Runners
	for _, p := range pools {
		if !p.Enabled || p.DockerMode != store.DockerDinD || !p.Automatic() || p.FromHosts() || !pressure[p.ID] {
			continue
		}
		g, ok := byPool[p.ID]
		// A killed job's peak is the limit it hit, not what it needed, and a pool
		// that has been killed for memory is not one to shrink the floor of.
		if !ok || g.PeakMemoryMB == nil || g.MeasuredMemory < minimumEvidenceJobs || g.OOMKilled > 0 {
			continue
		}
		// The job's peak is the two containers added together, but each has its own
		// limit: a slot the sum fits can still be too small for a thin sidecar, whose
		// kill takes the job with it. Without what each half used there is nothing to
		// size the halves by, so the advice waits for it.
		halves, ok := c.pairMemoryPeaks(p.ID)
		if !ok {
			continue
		}
		sized := sizingPool(p, fleet)
		factor := p.Resources.MemoryPairFactor()
		floor := scheduler.ShareFloor(sized).MemoryMB
		need := int64(math.Ceil(float64(*g.PeakMemoryMB)*minimumHeadroom/256) * 256)
		daemonFrac := float64(p.Resources.DaemonMemoryPercent()) / 100
		for _, half := range []struct {
			peak int64
			frac float64
		}{{halves.runnerMB, 1 - daemonFrac}, {halves.daemonMB, daemonFrac}} {
			if half.frac > 0 {
				need = max(need, int64(math.Ceil(float64(half.peak)*minimumHeadroom/half.frac/256)*256))
			}
		}
		if need >= floor || float64(floor-need) < minimumReductionWorth*float64(floor) {
			continue
		}
		perContainer := max(int64(math.Ceil(float64(need)/factor/256)*256), store.MinRunnerMemoryMB)
		cand := *p
		cand.Resources.MinMemoryMB = perContainer
		candFloor := scheduler.ShareFloor(sizingPool(&cand, fleet)).MemoryMB
		if candFloor >= floor {
			continue
		}

		before, err := c.poolRoom(ctx, p)
		if err != nil {
			return fmt.Errorf("pricing pool %s's smallest runner: %w", p.Name, err)
		}
		after, err := c.poolRoom(ctx, &cand)
		if err != nil {
			return fmt.Errorf("pricing pool %s's smallest runner: %w", p.Name, err)
		}
		detail := fmt.Sprintf("a runner of this pool is charged at least %s of memory on every host, because its smallest runner is %s a container and the Docker sidecar holds %d%% of the memory, "+
			"so the slot has to be %s for the runner's half to reach it. Over the last %s its %d measured jobs (of %d completed) used at most %s, with no job killed for memory. "+
			"Typical jobs waited %s or more for a runner in the last %s.",
			scheduler.FormatMB(floor), scheduler.FormatMB(max(sized.Resources.MinMemoryMB, store.MinRunnerMemoryMB)), p.Resources.DaemonMemoryPercent(),
			scheduler.FormatMB(floor), minimumEvidenceWindow, g.MeasuredMemory, g.Count, scheduler.FormatMB(*g.PeakMemoryMB), pressureMedianWait, pressureWindow)
		fix := fmt.Sprintf("lower the pool's smallest runner to %s a container (the pool editor's Size step, or zoomies pools edit %s --min-memory-mb %d), "+
			"which keeps every slot at least %s, 1.5 times the most a job has used and enough for each of the two containers' own peaks. "+
			"Memory a job keeps in a tmpfs counts as used and is taken from the same slot.", scheduler.FormatMB(perContainer), p.Name, perContainer,
			scheduler.FormatMB(candFloor))
		var remedy *Remedy
		if after.Runners > before.Runners {
			res := p.Resources
			res.MinMemoryMB = perContainer
			remedy = newRemedy(RemedyPoolUpdate, p.ID, "Lower the smallest runner to "+scheduler.FormatMB(perContainer),
				fmt.Sprintf("the pool's hosts have room for %s instead of %d", plural(after.Runners, "runner"), before.Runners),
				map[string]any{"resources": res}, p.Resources)
		} else {
			detail += " Lowering it would not give the pool's hosts room for more runners, so it is not proposed."
			fix = "the smallest runner is not what limits this pool's hosts: see the hosts' runner sizes and capacities."
		}
		*out = append(*out, Problem{
			Code:     "pool.minimum_overcharges",
			Severity: config.SeverityInfo,
			Title: fmt.Sprintf("pool %s: its smallest runner charges every runner at least %s, and its jobs have used at most %s",
				p.Name, scheduler.FormatMB(floor), scheduler.FormatMB(*g.PeakMemoryMB)),
			Detail: detail, Fix: fix, Remedy: remedy,
			TargetKind: "pool", TargetID: p.ID,
		})
	}
	return nil
}
