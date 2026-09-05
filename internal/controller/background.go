package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

// The housekeeping cadences. They share one goroutine and one ticker: none of
// this work is urgent, and three timers would be three things to reason about
// at shutdown instead of one.
const (
	housekeepingTick = 30 * time.Second
	sampleInterval   = time.Minute
	pruneInterval    = time.Hour
)

// backgroundLoop runs the periodic work: the fleet sampler behind the
// Overview's sparklines, retention pruning, credential expiry, and the sweep
// that re-offers tasks whose agent never answered.
func (c *Controller) backgroundLoop(ctx context.Context) {
	ticker := time.NewTicker(housekeepingTick)
	defer ticker.Stop()

	var lastSample, lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := c.Now()
		c.sweepTasks(ctx, now)
		c.checkHostHealth(ctx)
		c.expireStaleQueuedJobs(ctx, now)
		// A host going quiet is a problem and a capacity change, and neither
		// waits for the next reconcile pass to be told about.
		c.publishDerived(ctx)

		if now.Sub(lastSample) >= sampleInterval {
			lastSample = now
			if err := c.sample(ctx); err != nil {
				c.log.Warn("could not record a fleet sample", "error", err)
			}
		}
		if now.Sub(lastPrune) >= pruneInterval {
			lastPrune = now
			c.prune(ctx)
		}
	}
}

// sweepTasks re-offers tasks whose lease expired without a result.
//
// Delivery is at-least-once and every task is idempotent, so a redelivery
// costs the agent a no-op. The alternative -- assuming one delivery is enough
// -- means a runner that was never created and nothing to say why.
func (c *Controller) sweepTasks(ctx context.Context, now time.Time) {
	for hostID, q := range c.queues.all() {
		requeued, dropped := q.sweep(now)
		if requeued > 0 {
			c.log.Warn("re-queued tasks a host never reported on",
				"host", hostID, "tasks", requeued)
		}
		if len(dropped) == 0 {
			continue
		}
		c.log.Error("gave up redelivering tasks to a host",
			"host", hostID, "tasks", len(dropped), "attempts", maxTaskAttempts)
		for _, task := range dropped {
			// A dropped create is noticed by the runner's provision timeout,
			// which says so on the Runners page. A dropped stop is noticed by
			// nothing: the runner would sit in draining for ever, counted
			// against its pool's maximum and holding a slot on its host, so
			// it is failed here and the next pass frees the slot.
			if task.Kind != agent.TaskStopRunner {
				continue
			}
			reason := fmt.Sprintf("the host never confirmed stopping this runner after %d deliveries; it is presumed gone", maxTaskAttempts)
			if err := c.failRunnerID(ctx, task.RunnerID, reason); err != nil &&
				!errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrInvalidTransition) {
				c.log.Warn("could not fail a runner whose stop was never confirmed", "runner", task.RunnerID, "error", err)
			}
		}
	}
}

// sample writes one point for the Overview's sparklines. RecordSample keys on
// the minute, so a restart mid-minute overwrites rather than double-counts.
func (c *Controller) sample(ctx context.Context) error {
	counts, err := c.st.CountRunnersByPool(ctx)
	if err != nil {
		return err
	}
	var s store.FleetSample
	s.At = c.Now()
	for _, pc := range counts {
		s.IdleRunners += pc.Idle
		s.BusyRunners += pc.Busy
		s.TotalRunners += pc.Live()
	}
	stats, err := c.st.StatsSince(ctx, c.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	s.QueuedJobs, s.RunningJobs = stats.Queued, stats.Running
	return c.st.RecordSample(ctx, s)
}

// prune enforces the retention windows and expires credentials.
//
// Every deletion is logged at debug with a count, because "where did my job
// history go?" should be answerable from the log rather than from reading this
// function.
func (c *Controller) prune(ctx context.Context) {
	now := c.Now()
	r := c.cfg().Retention

	type job struct {
		what   string
		window time.Duration
		fn     func(context.Context, time.Time) (int64, error)
	}
	for _, j := range []job{
		{"jobs", r.Jobs, c.st.PruneJobs},
		{"runners", r.Runners, c.st.PruneRunners},
		{"fleet samples", r.Samples, c.st.PruneSamples},
		{"webhook deliveries", r.Webhooks, c.st.PruneDeliveries},
		// Scaling history is decision history, so it follows the audit window.
		// The audit rows themselves have no prune: an audit trail a process can
		// quietly delete is not one, so store deliberately offers no way.
		{"scaling events", r.Audit, c.st.PruneScalingEvents},
	} {
		// A zero or negative window means "keep everything", which is what an
		// operator who cleared the setting meant.
		if j.window <= 0 {
			continue
		}
		n, err := j.fn(ctx, now.Add(-j.window))
		if err != nil {
			c.log.Warn("could not prune history", "what", j.what, "error", err)
			continue
		}
		if n > 0 {
			c.log.Debug("pruned history", "what", j.what, "rows", n, "older_than", j.window)
		}
	}

	if n, err := c.st.PruneSessions(ctx, now); err != nil {
		c.log.Warn("could not prune expired sessions", "error", err)
	} else if n > 0 {
		c.log.Debug("pruned expired sessions", "rows", n)
	}
	if n, err := c.st.PruneJoinTokens(ctx, now); err != nil {
		c.log.Warn("could not prune expired join tokens", "error", err)
	} else if n > 0 {
		c.log.Debug("pruned expired join tokens", "rows", n)
	}
}
