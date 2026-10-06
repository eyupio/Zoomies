package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// A pool with no runner kept warm starts one for each job it gets, and a job that
// arrives into a burst waits for the runners ahead of it to be made. The median
// hides it -- most jobs find a runner free, or one made in seconds -- while the
// slowest few wait many minutes, which is the number a developer waiting on a
// build feels. poolsUnderPressure reads the median on purpose, so that a
// single slow start does not move a size; this reads the tail on purpose, because
// the cure is different: not a bigger slot, but a runner already running.
const (
	// queueTailWait is how long the slowest twentieth of a pool's jobs have to
	// have waited for it to count. Five minutes is long enough that it is not a
	// pull of a cold image, and short enough to be seen before it is a complaint.
	queueTailWait = 5 * time.Minute
	// queueTailJobs is how many jobs that is read over, so that a quiet pool's one
	// slow job is not a pattern.
	queueTailJobs = 10
	// queueWarmRunners is how many runners the remedy keeps warm: enough to take
	// the first of a burst without being a standing cost worth arguing about.
	queueWarmRunners = 2
)

// poolQueueWaitProblems is pool.queue_wait_high: a pool whose tail waited for a
// runner while it keeps none warm and has room to, with the minimum that would.
func (c *Controller) poolQueueWaitProblems(ctx context.Context, out *[]Problem) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	since := c.Now().Add(-pressureWindow)
	stats, err := c.st.JobStats(ctx, store.JobFilter{Since: &since}, []string{store.GroupByPool})
	if err != nil {
		return fmt.Errorf("reading how long jobs waited: %w", err)
	}
	byPool := map[string]store.JobStatsGroup{}
	for _, g := range stats.Groups {
		byPool[g.Keys[store.GroupByPool]] = g
	}
	for _, p := range pools {
		// A pool the controller keeps works its minimum out from the hosts, so there
		// is no number here to propose; and a pool somebody has already given a
		// minimum has decided how warm it wants to be.
		if !p.Enabled || p.FromHosts() || p.MinRunners > 0 || p.MaxRunners <= 1 {
			continue
		}
		g, ok := byPool[p.ID]
		w := g.QueueWait
		if !ok || w.Samples < queueTailJobs || w.P95MS == nil || time.Duration(*w.P95MS)*time.Millisecond < queueTailWait {
			continue
		}
		tail := time.Duration(*w.P95MS) * time.Millisecond
		room, err := c.poolRoom(ctx, p)
		if err != nil {
			return fmt.Errorf("pricing pool %s's minimum: %w", p.Name, err)
		}
		warm := min(queueWarmRunners, p.MaxRunners, room.Runners)
		median := "under half a minute"
		if w.P50MS != nil {
			median = (time.Duration(*w.P50MS) * time.Millisecond).Round(time.Second).String()
		}
		problem := Problem{
			Code:     "pool.queue_wait_high",
			Severity: config.SeverityInfo,
			Title: fmt.Sprintf("pool %s: one job in twenty waited %s or more for a runner, with none kept warm",
				p.Name, tail.Round(time.Second)),
			Detail: fmt.Sprintf("over the last %s, %d jobs waited a median of %s but the slowest twentieth waited %s. The pool keeps no runner warm (minimum 0), so a job that arrives in a burst waits for the runners ahead of it to be made.",
				pressureWindow, w.Samples, median, tail.Round(time.Second)),
			TargetKind: "pool",
			TargetID:   p.ID,
		}
		if warm < 1 {
			problem.Fix = "no host that reaches this pool has room for a runner of it, so a minimum would not start: see the hosts' capacities and runner sizes."
			*out = append(*out, problem)
			continue
		}
		problem.Fix = fmt.Sprintf("keep %d warm (the pool editor's Scale step, or zoomies pools edit %s --min-runners %d). A warm runner is idle capacity held for the pool, so this trades a little of the hosts' room for the first jobs of a burst starting at once.",
			warm, p.Name, warm)
		problem.Remedy = newRemedy(RemedyPoolUpdate, p.ID, fmt.Sprintf("Keep %s warm", plural(warm, "runner")),
			fmt.Sprintf("the pool's hosts have room for %s, and the first %d jobs of a burst start without waiting", plural(room.Runners, "runner"), warm),
			map[string]any{"min_runners": warm}, nil)
		if problem.Remedy != nil {
			// The request replaces none of the pool's resources or profile, so there
			// is no object an edit since could be put back by it: it has no base, and
			// an apply compares none.
			problem.Remedy.Base = ""
		}
		*out = append(*out, problem)
	}
	return nil
}
