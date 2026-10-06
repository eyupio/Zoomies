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
	pruneInterval    = time.Hour
)

// backgroundLoop runs the periodic work: the fleet sampler behind the
// Overview's sparklines, retention pruning, credential expiry, the image
// refresh that keeps a moving tag current on the hosts, the release check
// behind the update notice, and the sweep that re-offers tasks whose agent
// never answered.
func (c *Controller) backgroundLoop(ctx context.Context) {
	ticker := time.NewTicker(housekeepingTick)
	defer ticker.Stop()

	var last housekeeping
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.housekeep(ctx, &last)
	}
}

// housekeeping remembers when each of the slower jobs last ran, so that one
// ticker can pace work that wants a minute, an hour and a day apart.
type housekeeping struct {
	sample, prune, imageRefresh, updateCheck, toolchainScan time.Time
}

// housekeep is one pass of the background loop, pulled out of the ticker so
// that a test can run a pass without waiting for one.
func (c *Controller) housekeep(ctx context.Context, last *housekeeping) {
	now := c.Now()
	c.sweepTasks(ctx, now)
	c.checkHostHealth(ctx)
	c.settleThrottles(ctx, now)
	c.expireStaleQueuedJobs(ctx, now)
	// A host going quiet is a problem and a capacity change, and neither
	// waits for the next reconcile pass to be told about.
	c.publishDerived(ctx)

	// The minute is the unit: the store keys a sample on it and the chart
	// draws a point per minute, so the sampler has to land in every one.
	// Firing "once sixty seconds have passed" from a thirty-second ticker
	// looked the same and was not: a tick that arrived a few milliseconds
	// short of the minute skipped, the next one wrote, and the rows fell
	// ninety seconds apart -- every third minute on the chart an empty slot.
	// Firing when the minute has changed puts a row in each, whatever the
	// ticker's jitter, and the store's upsert makes a second pass in the same
	// minute harmless.
	if now.Truncate(time.Minute).After(last.sample.Truncate(time.Minute)) {
		last.sample = now
		if err := c.sample(ctx); err != nil {
			c.log.Warn("could not record a fleet sample", "error", err)
		}
	}
	if now.Sub(last.prune) >= pruneInterval {
		last.prune = now
		c.prune(ctx)
	}
	// The zero time is deliberately allowed to fire on the first tick: a
	// controller that has just come back up has no idea what moved while it
	// was down, and the pass is idempotent.
	if d := c.cfg().Images.RefreshInterval; d > 0 && now.Sub(last.imageRefresh) >= d {
		last.imageRefresh = now
		c.refreshPoolImages(ctx)
	}
	if d := c.cfg().Updates.CheckInterval; d > 0 && now.Sub(last.updateCheck) >= d {
		last.updateCheck = now
		c.checkForRelease(ctx)
	}
	// In the background, on the loop's own context so shutdown stops it: a
	// scan takes minutes on a large organisation, and this pass must not.
	// One already running -- started by hand -- is the answer, and this tick
	// is not retried.
	if d := c.cfg().GitHub.ToolchainScanInterval; d > 0 && now.Sub(last.toolchainScan) >= d {
		last.toolchainScan = now
		if err := c.startToolchainScan(ctx, false); err != nil && !errors.Is(err, ErrToolchainScanRunning) {
			c.log.Warn("could not start the scheduled toolchain scan", "error", err)
		}
	}
}

// refreshPoolImages prewarms every pool's image again on the hosts that can run
// it, so that a pool naming a moving tag picks up a rebuilt image without
// anyone touching the pool.
//
// Prewarming is otherwise only triggered by a pool being created, edited or
// prewarmed by hand, which means a moving image -- :dev on each main build or
// :latest on each full release -- would reach a host once and never again.
//
// Nothing here can affect scheduling: PrewarmPool records its outcome per host
// and queues an idempotent task, and a host that cannot reach the registry
// fails that task alone. The runners it is already running are untouched, and
// so is the image any of them was created from; a container keeps the image it
// started with until it is replaced.
func (c *Controller) refreshPoolImages(ctx context.Context) {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		c.log.Warn("could not list pools to refresh their images", "error", err)
		return
	}
	var refreshed, hosts int
	for _, p := range pools {
		n, err := c.PrewarmPool(ctx, p)
		switch {
		case errors.Is(err, ErrPrewarmUnsupported):
			// A process-backend pool has no image to pull; its runners fetch
			// the actions/runner archive themselves.
			continue
		case err != nil:
			c.log.Warn("could not refresh a pool's image", "pool", p.ID, "error", err)
			continue
		}
		if n > 0 {
			refreshed++
			hosts += n
		}
	}
	if refreshed > 0 {
		c.log.Debug("queued an image refresh", "pools", refreshed, "hosts", hosts)
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
			if task.Kind == agent.TaskRemoveRunner {
				if r, err := c.st.GetRunner(ctx, task.RunnerID); err == nil {
					c.noteCleanupFailure(ctx, r, task.Kind, "host did not confirm removal; cleanup will be retried")
				}
				continue
			}
			// A dropped create is noticed by the runner's provision timeout,
			// which says so on the Runners page. A dropped stop is noticed by
			// nothing: the runner would sit in draining for ever, counted
			// against its pool's maximum and holding a slot on its host, so
			// it is failed here and the next pass frees the slot.
			if task.Kind != agent.TaskStopRunner {
				continue
			}
			reason := fmt.Sprintf("the host never confirmed stopping this runner after %d deliveries; it is presumed gone", maxTaskAttempts)
			if err := c.failRunnerID(ctx, task.RunnerID, reason, store.FaultRunnerExited); err != nil &&
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
	stats, err := c.st.StatsSince(ctx, c.Now().Add(-time.Hour), false)
	if err != nil {
		return err
	}
	s.QueuedJobs, s.RunningJobs = stats.Queued, stats.Running
	// The fleet's own figures are recorded alongside, so the Overview's
	// sparkline can follow its "other runners" toggle rather than making the
	// line disagree with the tile above it.
	fleet, err := c.st.StatsSince(ctx, c.Now().Add(-time.Hour), true)
	if err != nil {
		return err
	}
	s.FleetQueuedJobs, s.FleetRunningJobs = fleet.Queued, fleet.Running
	if err := c.st.RecordSample(ctx, s); err != nil {
		return err
	}
	return c.sampleHosts(ctx, s.At)
}

// sampleHosts writes the same minute for every host: its slots, what its
// agent last measured, and what the scheduler has promised away on it. It is
// what lets the Hosts page say whether "CPU 93%" is a spike or the whole
// afternoon, which a card with one number cannot.
//
// A measurement older than HostUsageMaxAge is left out rather than carried
// forward. A host that stopped reporting would otherwise draw a flat line at
// its last reading, and a flat line is exactly what a healthy, quiet machine
// draws too.
func (c *Controller) sampleHosts(ctx context.Context, now time.Time) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return err
	}
	samples := make([]store.HostSample, 0, len(hosts))
	for _, h := range hosts {
		samples = append(samples, c.hostSample(h, now))
	}
	return c.st.RecordHostSamples(ctx, samples)
}

// hostSample is one host as one row of history, from the same figures the
// host's view is rendered from, so the newest point of the chart and the card
// beside it never disagree.
func (c *Controller) hostSample(h *store.Host, now time.Time) store.HostSample {
	alloc := h.Allocatable()
	s := store.HostSample{
		HostID:              h.ID,
		At:                  now,
		Capacity:            h.EffectiveCapacity(),
		ActiveRunners:       h.ActiveRunners,
		CPUs:                int64(h.CPUs),
		MemoryMB:            h.MemoryMB,
		AllocatableCPUs:     alloc.CPUs,
		AllocatableMemoryMB: alloc.MemoryMB,
		DiskTotalMB:         h.DiskTotalMB,
		DiskFreeMB:          h.DiskFreeMB,
	}
	if h.Usage.Fresh(now) {
		s.CPUPercent = h.Usage.CPUPercent
		s.LoadAverage1 = h.Usage.LoadAverage1
		s.MemoryAvailableMB = h.Usage.MemoryAvailableMB
	}
	if res, ok := c.reservedOn(h.ID); ok {
		cpus, mem := res.CPUs, res.MemoryMB
		s.ReservedCPUs = &cpus
		s.ReservedMemoryMB = &mem
	}
	return s
}

// prune enforces the retention windows and expires credentials.
//
// Every deletion is logged at debug with a count, because "where did my job
// history go?" should be answerable from the log rather than from reading this
// function.
func (c *Controller) prune(ctx context.Context) {
	now := c.Now()
	r := c.cfg().Retention

	// The roll-up goes first: it is computed from the sessions, and the
	// sessions prune below refuses to delete any it has not absorbed, so a
	// pass that ran them the other way round would only prune less. Running it
	// with every retention off costs one small transaction a pass and means
	// switching a retention on later loses nothing.
	if n, err := c.st.RollUpUsage(ctx, now); err != nil {
		c.log.Warn("could not roll up usage", "error", err)
	} else if n > 0 {
		c.log.Debug("rolled up usage", "days", n)
	}
	// The installation report's counts are rolled up the same way, before
	// the job and session rows they are counted from can go. A job still
	// unfinished when the jobs prune would take it no longer holds its day
	// open: it is counted as what it was, because it is about to be deleted.
	var settled time.Time
	if r.Jobs > 0 {
		settled = now.Add(-r.Jobs)
	}
	if n, err := c.st.RollUpInstallations(ctx, now, settled); err != nil {
		c.log.Warn("could not roll up the installation report", "error", err)
	} else if n > 0 {
		c.log.Debug("rolled up the installation report", "days", n)
	}
	// Neither the jobs nor the sessions a day is counted from are pruned
	// before that day is in the roll-up. A failed roll-up above therefore
	// prunes less this pass, never loses a count.
	counted, countedErr := c.st.InstallationRollupUntil(ctx)
	if countedErr != nil {
		c.log.Warn("could not read the installation roll-up", "error", countedErr)
	}
	heldBack := func(fn func(context.Context, time.Time) (int64, error)) func(context.Context, time.Time) (int64, error) {
		return func(ctx context.Context, before time.Time) (int64, error) {
			if countedErr != nil {
				return 0, nil
			}
			if counted.Before(before) {
				before = counted
			}
			return fn(ctx, before)
		}
	}

	type job struct {
		what   string
		window time.Duration
		fn     func(context.Context, time.Time) (int64, error)
	}
	for _, j := range []job{
		{"jobs", r.Jobs, heldBack(c.st.PruneJobs)},
		{"runners", r.Runners, func(ctx context.Context, before time.Time) (int64, error) {
			// Each pruned row is announced, or the Runners page keeps showing
			// runners that no longer exist until it is reloaded.
			ids, err := c.st.PruneRunners(ctx, before)
			c.publishRunnersDeleted(ids)
			return int64(len(ids)), err
		}},
		{"runner sessions", r.RunnerSessions, heldBack(c.st.PruneRunnerSessions)},
		{"fleet samples", r.Samples, c.st.PruneSamples},
		{"host samples", r.Samples, c.st.PruneHostSamples},
		{"webhook deliveries", r.Webhooks, c.st.PruneDeliveries},
		// The audit rows themselves have no prune: an audit trail a process can
		// quietly delete is not one, so store deliberately offers no way.
		{"scaling events", r.ScalingEvents, c.st.PruneScalingEvents},
		// Only the machines that cannot be about anything live: a failed
		// machine still naming a resource is never pruned, however old, because
		// the row is the only record that something was rented and may still be
		// running. PruneMachines enforces that; this announces what went.
		{"machines", r.Machines, func(ctx context.Context, before time.Time) (int64, error) {
			n, ids, err := c.st.PruneMachines(ctx, before)
			c.publishMachinesDeleted(ids)
			return n, err
		}},
		{"usage capacity", r.Jobs, c.st.PruneUsageCapacity},
		// The class kept for a job is worked out from its runs, and goes when
		// they do: a job that comes back after that long starts in the default
		// class, as one that has never run does.
		{"job classes", r.Jobs, c.st.PruneJobClasses},
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

	// Kennel Club's own rows, ninety days after the fleet last served the
	// repository they are about. This runs with the feature off.
	c.pruneKennel(ctx, now)

	if n, err := c.st.PruneSessions(ctx, now); err != nil {
		c.log.Warn("could not prune expired sessions", "error", err)
	} else if n > 0 {
		c.log.Debug("pruned expired sessions", "rows", n)
	}
	if n, err := c.st.PruneOAuth(ctx, now); err != nil {
		c.log.Warn("could not prune expired MCP sign-ins", "error", err)
	} else if n > 0 {
		c.log.Debug("pruned expired MCP sign-ins", "rows", n)
	}
	if n, err := c.st.PruneJoinTokens(ctx, now); err != nil {
		c.log.Warn("could not prune expired join tokens", "error", err)
	} else if n > 0 {
		c.log.Debug("pruned expired join tokens", "rows", n)
	}
}
