package controller

import (
	"context"
	"fmt"
	"math"
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
	// minimumEvidenceJobs is how many measured jobs it takes to say what a pool's
	// jobs need. A pool with a dozen jobs has not shown its heaviest one.
	minimumEvidenceJobs = 20
	// minimumHeadroom is what a slot is kept above the most a job has used, because
	// the next job is allowed to be somewhat heavier than the heaviest so far.
	minimumHeadroom = 1.5
	// minimumReductionWorth is the least fall in the slot's memory floor worth
	// proposing, as a fraction: a change that moves the floor a few percent is a
	// number to second-guess for no gain.
	minimumReductionWorth = 0.25
)

// poolMinimumAdviceProblems is pool.minimum_overcharges.
func (c *Controller) poolMinimumAdviceProblems(ctx context.Context, out *[]Problem) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	pressure, err := c.poolsUnderPressure(ctx)
	if err != nil {
		return err
	}
	if len(pressure) == 0 {
		return nil
	}
	since := c.Now().Add(-minimumEvidenceWindow)
	stats, err := c.st.JobStats(ctx, store.JobFilter{Since: &since}, []string{store.GroupByPool})
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
		if !ok || g.PeakMemoryMB == nil || g.Count < minimumEvidenceJobs || g.OOMKilled > 0 {
			continue
		}
		sized := sizingPool(p, fleet)
		factor := p.Resources.MemoryPairFactor()
		floor := scheduler.ShareFloor(sized).MemoryMB
		need := int64(math.Ceil(float64(*g.PeakMemoryMB)*minimumHeadroom/256) * 256)
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
			"so the slot has to be %s for the runner's half to reach it. Over the last %s its %d measured jobs used at most %s, with no job killed for memory. "+
			"Typical jobs waited %s or more for a runner in the last %s.",
			scheduler.FormatMB(floor), scheduler.FormatMB(max(sized.Resources.MinMemoryMB, store.MinRunnerMemoryMB)), p.Resources.DaemonMemoryPercent(),
			scheduler.FormatMB(floor), minimumEvidenceWindow, g.Count, scheduler.FormatMB(*g.PeakMemoryMB), pressureMedianWait, pressureWindow)
		fix := fmt.Sprintf("lower the pool's smallest runner to %s a container (the pool editor's Size step, or zoomies pools edit %s --min-memory-mb %d), "+
			"which keeps every slot at least %s, 1.5 times the most a job has used.", scheduler.FormatMB(perContainer), p.Name, perContainer,
			scheduler.FormatMB(candFloor))
		var remedy *Remedy
		if after.Runners > before.Runners {
			res := p.Resources
			res.MinMemoryMB = perContainer
			remedy = newRemedy(RemedyPoolUpdate, p.ID, "Lower the smallest runner to "+scheduler.FormatMB(perContainer),
				fmt.Sprintf("the pool's hosts have room for %s instead of %d", plural(after.Runners, "runner"), before.Runners),
				map[string]any{"resources": res})
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
