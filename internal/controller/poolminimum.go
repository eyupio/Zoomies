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
	// What each half used is in memory and gates the advice below, so a pool whose
	// samples do not yet cover it -- every pool for half an hour after a restart -- is
	// not read about at all.
	var ids []string
	halvesOf := map[string]halfPeaks{}
	for _, p := range candidates {
		if !pressure[p.ID] {
			continue
		}
		if halves, ok := c.pairMemoryPeaks(p.ID); ok {
			halvesOf[p.ID] = halves
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
		halves, ok := halvesOf[p.ID]
		if !ok {
			continue
		}
		// The window is the last few hours and the week is what spans the heavy job: a
		// weekly build whose daemon used 2.6 GB is missing from a window of light phases,
		// and a floor sized by the window alone was lowered under it. The most each half
		// used in any job of the week counts, and the advice waits until enough jobs have
		// been measured that way -- every pool for a week after an upgrade.
		if g.MeasuredHalves < minimumEvidenceJobs {
			continue
		}
		halves.runnerMB = max(halves.runnerMB, g.PeakRunnerMemoryMB)
		halves.daemonMB = max(halves.daemonMB, g.PeakDaemonMemoryMB)
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
			// The floor is not far above what the pool's heaviest job needs. If that is
			// one job among many, it is the job and not the pool that holds the floor, and
			// that is worth saying even though nothing can be offered to apply.
			held, err := c.minimumHeldByJob(ctx, p, fleet, floor, since)
			if err != nil {
				return err
			}
			if held != nil {
				*out = append(*out, *held)
			}
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
			"so the slot has to be %s for the runner's half to reach it. Over the last %s its jobs used at most %s, from at least %d with a measured peak, with no job killed for memory. "+
			"Typical jobs waited %s or more for a runner in the last %s.",
			scheduler.FormatMB(floor), scheduler.FormatMB(max(sized.Resources.MinMemoryMB, store.MinRunnerMemoryMB)), p.Resources.DaemonMemoryPercent(),
			scheduler.FormatMB(floor), minimumEvidenceWindow, scheduler.FormatMB(*g.PeakMemoryMB), minimumEvidenceJobs, pressureMedianWait, pressureWindow)
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

// minimumHeldByJob is pool.minimum_held_by_job: a pool's floor is where it is
// because of one job name, and without it the rest would run in a slot that is far
// smaller and fits more runners on every host.
//
// The week's statistics are by pool, so a single heavy job -- a weekly build, a
// release -- is all the pool's evidence says, and every runner carries a slot sized
// for it. Nothing is proposed to apply, because lowering the minimum before that job
// is somewhere else is what gets it killed; the notice names it and the ways to move it.
func (c *Controller) minimumHeldByJob(ctx context.Context, p *store.Pool, fleet config.Runners, floor int64, since time.Time) (*Problem, error) {
	stats, err := c.st.JobStats(ctx, store.JobFilter{Since: &since, PoolIDs: []string{p.ID}}, []string{store.GroupByPool, store.GroupByJobName})
	if err != nil {
		return nil, fmt.Errorf("reading which job holds pool %s's floor: %w", p.Name, err)
	}
	// A name that is not in the list could be the one that matters, so a truncated
	// answer says nothing.
	if stats.Truncated {
		return nil, nil
	}
	var holder *store.JobStatsGroup
	for i := range stats.Groups {
		g := &stats.Groups[i]
		if g.PeakMemoryMB != nil && (holder == nil || *g.PeakMemoryMB > *holder.PeakMemoryMB) {
			holder = g
		}
	}
	if holder == nil {
		return nil, nil
	}
	var othersPeak int64
	var othersMeasured int
	for i := range stats.Groups {
		g := &stats.Groups[i]
		if g == holder {
			continue
		}
		othersMeasured += g.MeasuredMemory
		if g.PeakMemoryMB != nil {
			othersPeak = max(othersPeak, *g.PeakMemoryMB)
		}
	}
	if othersMeasured < minimumEvidenceJobs {
		return nil, nil
	}
	need := int64(math.Ceil(float64(othersPeak)*minimumHeadroom/256) * 256)
	if need >= floor || float64(floor-need) < minimumReductionWorth*float64(floor) {
		return nil, nil
	}
	perContainer := max(int64(math.Ceil(float64(need)/p.Resources.MemoryPairFactor()/256)*256), store.MinRunnerMemoryMB)
	cand := *p
	cand.Resources.MinMemoryMB = perContainer
	candFloor := scheduler.ShareFloor(sizingPool(&cand, fleet)).MemoryMB
	if candFloor >= floor {
		return nil, nil
	}
	before, err := c.poolRoom(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("pricing pool %s's smallest runner: %w", p.Name, err)
	}
	after, err := c.poolRoom(ctx, &cand)
	if err != nil {
		return nil, fmt.Errorf("pricing pool %s's smallest runner: %w", p.Name, err)
	}
	if after.Runners <= before.Runners {
		return nil, nil
	}
	name := workflowText(holder.Keys[store.GroupByJobName])
	return &Problem{
		Code:     "pool.minimum_held_by_job",
		Severity: config.SeverityInfo,
		Title: fmt.Sprintf("pool %s: one job, %s, holds every runner's slot at %s; the pool's other jobs used at most %s",
			p.Name, name, scheduler.FormatMB(floor), scheduler.FormatMB(othersPeak)),
		Detail: fmt.Sprintf("the pool's smallest runner puts every slot at least %s, and that is where it is because of %q, which used up to %s in the last %s. "+
			"The pool's other jobs used at most %s, so a slot of %s would hold them with room to spare, and the pool's hosts would then have room for %s instead of %d. "+
			"The controller does not propose lowering the smallest runner while that job shares the pool, because it is the job that would be killed.",
			scheduler.FormatMB(floor), name, scheduler.FormatMB(*holder.PeakMemoryMB), minimumEvidenceWindow,
			scheduler.FormatMB(othersPeak), scheduler.FormatMB(candFloor), plural(after.Runners, "runner"), before.Runners),
		Fix: fmt.Sprintf("move %q off this pool and then lower the smallest runner to %s a container (zoomies pools edit %s --min-memory-mb %d). "+
			"A pool of its own, with a label only that job asks for, is the one way that is certain; with scheduler.size_routing on, a size label or `zoomies size pin` sends it to a larger class "+
			"of the pool's own runners, and scheduler.history_sizing=on gives it a larger runner from what it used before, though only after a run or two.",
			name, scheduler.FormatMB(perContainer), p.Name, perContainer),
		TargetKind: "pool", TargetID: p.ID,
	}, nil
}
