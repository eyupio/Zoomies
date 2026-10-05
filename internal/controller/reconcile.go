package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// reapInterval is how often Zoomies' view of the fleet is compared against the
// runners GitHub thinks exist. It is slow because it costs one API call per
// installation and the situation it fixes -- an orphaned registration -- is
// untidy rather than urgent.
const reapInterval = 10 * time.Minute

// lifecycleCallTimeout bounds a detached create or remove's GitHub work: a
// credential mint is one call, a registration delete is up to two (a lookup
// by name, then the delete), each already bounded by the client's own
// per-request timeout. This is the outer net for the pair of them together,
// matching the same defence-in-depth the machine loop gives a provider call.
const lifecycleCallTimeout = 2 * time.Minute

// reconcileLoop runs a pass on the configured interval and immediately on
// every nudge, with only one pass in flight at a time.
func (c *Controller) reconcileLoop(ctx context.Context) {
	interval := c.schedulerInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// One pass at startup so a controller that was down while jobs queued
	// starts creating runners without waiting out the interval.
	c.reconcileNow(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcileNow(ctx)
		case <-c.nudges:
			// Every nudge that arrived while the previous pass ran has already
			// collapsed into the single token we just took, so this is one
			// pass for the whole burst.
			c.reconcileNow(ctx)
		}
		// The interval is a runtime setting, and UpdateConfig nudges this
		// loop, so a change is in force from the pass after it was accepted.
		if d := c.schedulerInterval(); d != interval {
			interval = d
			ticker.Reset(d)
		}
	}
}

// reconcileNow runs one pass and logs a failure rather than propagating it: a
// transient database or GitHub error must not stop the loop.
func (c *Controller) reconcileNow(ctx context.Context) {
	if err := c.Reconcile(ctx); err != nil && ctx.Err() == nil {
		c.metrics.reconcileErrors.Inc()
		c.log.Error("reconcile pass failed; the next pass will try again", "error", err)
	}
}

// Reconcile runs exactly one scheduling pass: snapshot the fleet, decide, and
// apply. It is exported so that tests and the API can force a pass and know it
// has finished, which Nudge deliberately cannot promise.
func (c *Controller) Reconcile(ctx context.Context) error {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	started := time.Now()
	// What the previous pass held back at the minting limit is spent: this
	// pass either gets those creations through or holds them back again, and
	// the problems list should describe now rather than then.
	c.resetDeferredMints()
	if err := c.recoverHostCleanup(ctx); err != nil {
		return fmt.Errorf("recovering unfinished host cleanup: %w", err)
	}
	placementVersion := c.placementVersion.Load()
	snap, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	plan := scheduler.Decide(snap)
	if c.cfg().Scheduler.PlacementMode == "shadow" {
		candidate := snap
		candidate.PreferReadiness = true
		alternative := scheduler.Decide(candidate)
		c.log.Debug("compared host placement policies", "current", plan.Pools, "readiness", alternative.Pools)
	}
	for _, pp := range plan.Pools {
		if pp.History != "" {
			c.log.Debug("job history placement", "pool", pp.PoolName, "mode", snap.HistorySizing, "decision", pp.History)
		}
	}
	c.rememberPlacement(snap, plan, placementVersion)
	c.setLastPlan(plan)
	c.setReserved(snap)
	c.apply(ctx, snap, plan)
	c.routeFallbacks(ctx, snap, plan)
	c.publishCapacitySignals(ctx, snap, plan)
	// The pass may have changed what the queue and the fleet look like, and
	// time alone changes the wait percentiles; this is the moment the Overview
	// learns either way.
	c.publishDerived(ctx)
	c.passes.Add(1)
	c.metrics.reconcileDuration.Observe(time.Since(started).Seconds())
	return nil
}

// snapshot gathers everything the scheduler needs in one place. The scheduler
// reads no clock and no database, so this is the only point at which a
// decision is coupled to the state of the world.
func (c *Controller) snapshot(ctx context.Context) (scheduler.Snapshot, error) {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing pools: %w", err)
	}
	// The scheduler sizes a pool by its minimum, so it is handed the minimum
	// in force -- the fleet's where the pool set none -- rather than the row.
	pools = c.sizingPools(pools)
	runners, err := c.st.SchedulingRunners(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing scheduling runners: %w", err)
	}
	jobs, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing queued jobs: %w", err)
	}
	jobs = c.routableJobs(jobs)
	activeByRepository, queuedByRepository, err := c.st.RepositoryJobCounts(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("counting jobs by repository: %w", err)
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing hosts: %w", err)
	}
	installations, err := c.st.ListInstallations(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing installations: %w", err)
	}
	lastProvisioned, err := c.st.LastProvisioned(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("reading provisioning order: %w", err)
	}
	now := c.Now()
	readiness := make(map[string]map[string]time.Duration)
	if c.cfg().Scheduler.PlacementMode == "readiness" || c.cfg().Scheduler.PlacementMode == "shadow" {
		if estimates, err := c.st.StartupEstimates(ctx, now.Add(-24*time.Hour)); err == nil {
			images := map[string]string{}
			for _, p := range pools {
				images[p.ID] = c.RunnerImage(p)
			}
			for _, e := range estimates {
				if images[e.PoolID] != e.Image {
					continue
				}
				if readiness[e.PoolID] == nil {
					readiness[e.PoolID] = map[string]time.Duration{}
				}
				readiness[e.PoolID][e.HostID] = time.Duration(e.Milliseconds) * time.Millisecond
			}
		}
	}
	mode := c.cfg().Scheduler.HistorySizing
	var history map[store.JobUsageKey][]store.JobPeak
	if mode == scheduler.HistoryShadow || mode == scheduler.HistoryOn {
		// Read for every distinct queued job, whichever pool ends up running
		// it: the store answers per pool and the scheduler picks the pool's.
		// A failed read costs this pass its history and nothing else -- the
		// fleet places as it did before history existed.
		var keys []store.JobUsageKey
		for _, j := range jobs {
			if j.State == store.JobQueued {
				keys = append(keys, store.JobUsageKey{Repo: j.Repo, Workflow: j.Workflow, JobName: j.JobName})
			}
		}
		if h, err := c.st.JobUsageHistory(ctx, keys); err == nil {
			history = h
		} else {
			c.log.Warn("could not read job history; placing without it this pass", "error", err)
		}
	}
	return scheduler.Snapshot{
		JobHistory: history, HistorySizing: mode,
		HostOrder: c.cfg().Scheduler.HostOrder,
		Readiness: readiness, PreferReadiness: c.cfg().Scheduler.PlacementMode == "readiness",
		LastProvisioned:    lastProvisioned,
		Now:                now,
		HeldInstallations:  c.heldInstallations(now),
		Pools:              pools,
		Runners:            runners,
		Jobs:               jobs,
		ActiveByRepository: activeByRepository,
		QueuedByRepository: queuedByRepository,
		Hosts:              hosts,
		Installations:      installations,
		Policy:             c.policy(),
		Jitter:             poolJitter(pools),
	}, nil
}

// poolJitter draws one number per pool for the scheduler to spread its
// start-failure backoff with.
//
// It is drawn here because the scheduler has no random source, for the same
// reason it has no clock: a plan has to be reproducible from the snapshot that
// produced it, and a decision that reached for rand inside could not be put in
// front of a test or explained to an operator afterwards.
func poolJitter(pools []*store.Pool) map[string]float64 {
	out := make(map[string]float64, len(pools))
	for _, p := range pools {
		out[p.ID] = rand.Float64()
	}
	return out
}

// apply executes a plan pool by pool and records what actually happened.
//
// A failure on one action never abandons the rest: one pool being unable to
// reach GitHub must not stop another pool from draining an idle runner.
func (c *Controller) apply(ctx context.Context, snap scheduler.Snapshot, plan scheduler.Plan) {
	// A fenced fleet has decided and does nothing about it. The plan is still
	// computed and published above, which is the point: an operator recovering
	// from a backup can see exactly what this controller would do the moment
	// they lift the fence, and can tell "nothing to do" from "not allowed to".
	if !c.mayAct() {
		return
	}
	pools := make(map[string]*store.Pool, len(snap.Pools))
	for _, p := range snap.Pools {
		pools[p.ID] = p
	}
	// The host a create lands on decides what the runner is given: its
	// default limits are one slot's share of that machine, and the snapshot
	// is the one view of the host the decision was made against.
	hosts := make(map[string]*store.Host, len(snap.Hosts))
	for _, h := range snap.Hosts {
		hosts[h.ID] = h
	}

	// When each pool's first waiting job was queued, for the queued-to-create
	// metric. The snapshot already holds the queued jobs, in the order the
	// store returns them; reading them again for every runner created -- inside
	// reconcileMu, where every other pool's scheduling waits -- was a full list
	// query per create for a figure the pass already had.
	queuedSince := make(map[string]time.Time, len(plan.Pools))
	for _, j := range snap.Jobs {
		if _, seen := queuedSince[j.PoolID]; j.PoolID != "" && !seen {
			queuedSince[j.PoolID] = j.QueuedAt
		}
	}

	type changes struct{ created, drained int }
	// Each scheduler drain asks GitHub a question before it stops anything, and
	// this loop holds reconcileMu throughout, so the number asked per pass is
	// capped. A drain left over is not lost: the scheduler decides it again
	// next pass, from a snapshot that has caught up.
	withdrawals := 0
	counts := make(map[string]*changes, len(plan.Pools))
	for _, a := range plan.Actions {
		if ctx.Err() != nil {
			return
		}
		pool := pools[a.PoolID]
		if pool == nil {
			continue
		}
		n := counts[a.PoolID]
		if n == nil {
			n = &changes{}
			counts[a.PoolID] = n
		}
		switch a.Kind {
		case scheduler.ActionCreate:
			if IsDemoID(pool.InstallationID) {
				// The demo installation has no GitHub behind it, and its
				// client refuses every credential, so a create here can only
				// end as a failed row -- and five of them inside ten minutes
				// put the pool on a start backoff that reads "keeps failing
				// to start" on every queued job in the fixture. The runners
				// a demo shows are the ones the seed wrote; the rest of its
				// demand is left standing rather than manufactured into
				// failures a fleet with nothing wrong with it never had.
				continue
			}
			if err := c.createRunner(ctx, pool, hosts[a.HostID], a, queuedSince[pool.ID]); err != nil {
				if !errors.Is(err, errRegistrationDeferred) {
					c.log.Error("could not create a runner", "pool", pool.Name, "host", a.HostID, "reason", a.Reason, "error", err)
				}
				continue
			}
			n.created++
		case scheduler.ActionDrain:
			if withdrawals >= maxWithdrawalsPerPass {
				c.log.Debug("left a drain for the next pass so this one does not hold the scheduling lock on GitHub",
					"runner", a.RunnerID, "pool", a.PoolName)
				continue
			}
			withdrawals++
			if err := c.drainRunnerID(ctx, a.RunnerID, a.Reason, pool); err != nil {
				c.logRunnerAction("drain", a, err)
				continue
			}
			n.drained++
		case scheduler.ActionRemove:
			if err := c.removeRunnerID(ctx, a.RunnerID, a.Reason, pool); err != nil {
				c.logRunnerAction("remove", a, err)
			}
		case scheduler.ActionFail:
			if err := c.failRunner(ctx, a.RunnerID, a.Reason, store.FaultRunnerExited, true); err != nil {
				c.logRunnerAction("fail", a, err)
			}
		}
	}
	for _, pp := range plan.Pools {
		if pools[pp.PoolID] == nil {
			continue
		}
		if n := counts[pp.PoolID]; n != nil {
			c.recordScaling(ctx, pp, n.created, n.drained)
		}
		c.noteBlocked(pp)
		if err := c.st.RecordUsageCapacity(ctx, pp.PoolID, c.Now(), pp.BlockedAtCapacity); err != nil {
			c.log.Error("could not record usage capacity", "pool", pp.PoolID, "error", err)
		}
	}
}

// maxWithdrawalsPerPass bounds how many scheduler drains one pass attempts. Each
// can wait up to withdrawTimeout on GitHub under reconcileMu, so a pool disabled
// with dozens of idle runners would otherwise stall every other pool, the
// machine loop's callers and the webhook-driven passes for minutes.
const maxWithdrawalsPerPass = 8

// withdrawTimeout is how long one registration withdrawal may wait on GitHub.
const withdrawTimeout = 5 * time.Second

// logRunnerAction reports a failed action, quietly when the runner has simply
// gone: two passes racing over the same dead runner is normal, not an error.
func (c *Controller) logRunnerAction(what string, a scheduler.Action, err error) {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidTransition) {
		c.log.Debug("skipped a runner action that no longer applies",
			"action", what, "runner", a.RunnerID, "error", err)
		return
	}
	c.log.Error("could not apply a runner action",
		"action", what, "runner", a.RunnerID, "pool", a.PoolName, "reason", a.Reason, "error", err)
}

// recordScaling writes the scaling row the UI's history shows, but only when
// the pool's size actually moved. A "cannot scale" reason is real and gets
// logged, yet writing it as a scaling event every ten seconds would bury the
// decisions that did something.
func (c *Controller) recordScaling(ctx context.Context, pp scheduler.PoolPlan, created, drained int) {
	to := pp.Current + created - drained
	if pp.Reason == "" || to == pp.Current {
		return
	}
	e := &store.ScalingEvent{
		PoolID:   pp.PoolID,
		PoolName: pp.PoolName,
		From:     pp.Current,
		To:       to,
		Reason:   pp.Reason,
	}
	if err := c.st.AppendScalingEvent(ctx, e); err != nil {
		c.log.Error("could not record a scaling event", "pool", pp.PoolName, "error", err)
		return
	}
	c.metrics.scalingEvents.WithLabelValues(pp.PoolName, e.Direction()).Inc()
	c.publish(events.KindScaling, "pool:"+pp.PoolID, e)
	c.log.Info("scaled a pool", "pool", pp.PoolName, "from", e.From, "to", e.To, "reason", pp.Reason)
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

// createRunner materialises one ActionCreate: a row, a GitHub credential and a
// task for the chosen host's agent.
//
// The row is written before the credential is minted so that a failure has
// somewhere to be recorded. A runner that never got a JIT config is marked
// failed with GitHub's own error as its message, which is what the operator
// sees on the Runners page -- the alternative, deleting the row, leaves a pool
// that silently sits one runner short with nothing to explain it.
//
// host is the machine the action chose, from the snapshot the plan was made
// against, and it may be nil for a host that vanished between snapshot and
// apply: the runner is then created with the pool's own limits and nothing
// else, exactly as every runner was before defaults existed. The limits are
// decided here and written on the row before the create task carries them,
// so the Runners page can say what a runner was given and where the figure
// came from -- an OOM kill on a defaulted limit points at the host's capacity,
// not at a pool field nobody set.
// queuedSince is when the pool's first waiting job was queued, or zero when it
// has none.
func (c *Controller) createRunner(ctx context.Context, pool *store.Pool, host *store.Host, a scheduler.Action, queuedSince time.Time) error {
	if !c.mayAct() {
		return nil
	}
	inst, err := c.st.GetInstallation(ctx, pool.InstallationID)
	if err != nil {
		return fmt.Errorf("pool %s points at installation %s, which is not there; edit the pool to choose an installation: %w",
			pool.Name, pool.InstallationID, err)
	}

	if !c.admitCredentialMint(inst.ID) {
		return errRegistrationDeferred
	}
	detached := false
	defer func() {
		if !detached {
			c.releaseCredentialMint(inst.ID)
		}
	}()
	// Sized as the pass sized it, so a minimum the pool follows from the
	// fleet raises a thin share exactly as one of its own would; the pool
	// itself is not written.
	cfg := c.cfg()
	resources, source := scheduler.Allocation(sizingPool(pool, cfg.Runners), host, cfg.Scheduler.DefaultRunnerLimits)
	if a.Size != nil {
		// No host had room for the pool's standard size, and the pass placed
		// this one smaller, at or above the pool's minimum. The row carries
		// what it was given, because that is what its host is charged for it
		// and what an operator reading a slow job needs to know.
		resources.CPUs, resources.MemoryMB = a.Size.CPUs, a.Size.MemoryMB
		source = store.AllocationReduced
		if a.SizedFromHistory {
			// Larger than the share, not smaller: the jobs waiting are known
			// to need more than a slot of this host.
			source = store.AllocationHistory
		}
	}
	resources.MinCPUs, resources.MinMemoryMB = 0, 0
	name := github.RunnerName(pool)
	r := &store.Runner{
		PoolID:            pool.ID,
		HostID:            a.HostID,
		Name:              name,
		State:             store.RunnerProvisioning,
		Ephemeral:         pool.Ephemeral,
		Labels:            pool.Labels,
		Image:             c.RunnerImage(pool),
		RunnerVersion:     c.runnerVersion(pool),
		Message:           a.Reason,
		AllocatedCPUs:     resources.CPUs,
		AllocatedMemoryMB: resources.MemoryMB,
		AllocationSource:  source,
		SizedForCPUs:      buildSizedCPUs(pool, host, resources.CPUs),
	}
	if err := c.st.CreateRunner(ctx, r); err != nil {
		return fmt.Errorf("creating the runner row for %s: %w", name, err)
	}
	if !queuedSince.IsZero() {
		observeDuration(c.metrics.queuedToCreate, pool.Name, string(pool.Backend), queuedSince, r.CreatedAt)
	}
	c.publishRunner(ctx, events.KindRunnerCreated, r)

	// Minting a credential is a GitHub round trip. It is detached here, the
	// same way the machine loop detaches a provider call from its own pass
	// lock: reconcileMu must not span it, or a pool's scale-up holds every
	// other pool and installation's scheduling for as long as GitHub takes to
	// answer. The row already exists and carries RunnerProvisioning, so the
	// next snapshot counts it towards the pool's current size either way.
	c.lifecycleCalls.Add(1)
	detached = true
	go func() {
		defer c.lifecycleCalls.Done()
		// Release admission before signalling completion to lifecycle waiters.
		defer c.releaseCredentialMint(inst.ID)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCallTimeout)
		defer cancel()
		c.finishCreateRunner(cctx, inst, pool, r, a, name, resources, source)
	}()
	c.log.Info("creating a runner",
		"pool", pool.Name, "runner", r.ID, "name", name, "host", a.HostID, "reason", a.Reason)
	return nil
}

// finishCreateRunner mints the GitHub credential a newly-created runner row
// needs and either enqueues its create task or marks it failed, whichever
// GitHub answers. It runs apart from the pass that wrote the row -- see
// createRunner -- so its own errors end here rather than back in apply().
func (c *Controller) finishCreateRunner(ctx context.Context, inst *store.Installation, pool *store.Pool, r *store.Runner, a scheduler.Action, name string, resources store.Resources, source string) {
	creds, ghID, err := c.mintCredentials(ctx, inst, pool, name)
	if err != nil {
		msg := fmt.Sprintf("GitHub would not register %s: %v", name, err)
		if failed, ferr := c.st.FailRunner(ctx, r.ID, msg, store.FaultRegistration); ferr == nil {
			c.publishRunner(ctx, events.KindRunnerUpdated, failed)
			if !errors.Is(err, github.ErrRateLimited) {
				c.noteRunnerStartFailure(ctx, r, failed)
			}
		} else {
			c.log.Error("could not mark a runner failed after its registration failed",
				"runner", r.ID, "error", ferr)
		}
		c.log.Error("could not create a runner",
			"pool", pool.Name, "host", a.HostID, "reason", a.Reason, "error", err)
		return
	}
	if ghID != 0 {
		if err := c.st.SetRunnerGitHubID(ctx, r.ID, ghID); err != nil {
			c.log.Error("could not record the GitHub runner ID", "runner", r.ID, "error", err)
		}
		r.GitHubRunnerID = ghID
	}

	spec := backend.Spec{
		Name:        name,
		RunnerID:    r.ID,
		PoolID:      pool.ID,
		PoolName:    pool.Name,
		Image:       r.Image,
		PullPolicy:  pool.PullPolicy,
		Credentials: creds,
		Env:         withBuildSizing(runnerEnv(c.cfg().Runners, pool), r.SizedForCPUs),
		Ephemeral:   pool.Ephemeral,
		// What the row records, not the pool's field: a pool that sets no
		// limit gets one slot's share of the host, and the agent applies
		// whatever this says without knowing the difference.
		Resources:                resources,
		ResourcesSource:          wireSource(source, pool),
		Cache:                    pool.Cache,
		Tmpfs:                    pool.Tmpfs,
		DaemonSharePercent:       pool.Resources.DaemonSharePercent,
		DaemonCPUSharePercent:    pool.Resources.DaemonCPUSharePercent,
		DaemonMemorySharePercent: pool.Resources.DaemonMemorySharePercent,
		// An organisation installation's target is the organisation, which is
		// no repository at all; a pool under one names its cache's repository
		// itself, and that is the identity the runner should carry.
		Repository:    firstNonEmpty(strings.TrimSpace(pool.Cache.Repository), inst.Target),
		DockerMode:    pool.DockerMode,
		RunAsRoot:     pool.RunAsRoot,
		Network:       c.cfg().Agent.Network,
		RunnerVersion: r.RunnerVersion,
	}
	if timeout := c.policy().For(pool).ProvisionTimeout; timeout > 0 {
		spec.StartBefore = r.CreatedAt.Add(timeout)
	}
	// The host's say over in-memory folders, read now rather than when the pass
	// began: an operator who turned them off while the GitHub call was in flight
	// meant this runner too. A host that cannot be read gets disk, which is what
	// it had before the setting existed, rather than the pool's word over a
	// machine whose owner may have said no.
	if pool.Tmpfs.Any() {
		host, err := c.st.GetHost(ctx, a.HostID)
		if err == nil {
			spec.Tmpfs, spec.TmpfsMaxMB = host.RunnerProfile.Tmpfs.Apply(pool.Tmpfs)
			spec.TmpfsHost = host.RunnerProfile.Tmpfs
		} else {
			c.log.Warn("could not read the host to apply its in-memory folder policy; creating the runner with its folders on disk",
				"pool", pool.Name, "host", a.HostID, "error", err)
			spec.Tmpfs, spec.TmpfsMaxMB, spec.TmpfsHost = store.TmpfsConfig{}, 0, store.HostTmpfs{}
			host = nil
		}
		c.recordScratch(ctx, r, pool, host, spec)
	}

	// Credential minting is deliberately detached from the reconcile pass. In
	// that gap GitHub may cancel the queued job and the next pass may drain the
	// now-surplus provisioning row. Re-read under the reconcile lock before
	// publishing the create task: otherwise the late JIT response can enqueue a
	// create after the stop, bringing up exactly the runner cancellation meant
	// to avoid.
	//
	// Hold the lock only across the local read and enqueue, never across the
	// GitHub calls above or the cleanup below. This leaves the slow-call promise
	// of createRunner intact while making the final state check indivisible from
	// the scheduler pass that can drain it.
	c.reconcileMu.Lock()
	current, currentErr := c.st.GetRunner(ctx, r.ID)
	if currentErr == nil && current.State == store.RunnerProvisioning {
		c.enqueueLifecycle(ctx, a.HostID, agent.Task{
			Kind:     agent.TaskCreateRunner,
			RunnerID: r.ID,
			Spec:     &spec,
			Backend:  pool.Backend,
		})
		c.reconcileMu.Unlock()
		return
	}
	c.reconcileMu.Unlock()

	if currentErr != nil {
		// A JIT configuration may already have registered the runner even though
		// its row disappeared. Use the copy carrying that registration ID so the
		// remote side is not leaked.
		c.deleteRegistration(ctx, r, pool)
		c.log.Warn("discarded a runner credential because its row disappeared before creation",
			"runner", r.ID, "error", currentErr)
		return
	}

	switch current.State {
	case store.RunnerDraining:
		// The host removal is idempotent even when the earlier stop already found
		// nothing. removeRunner also deletes the newly-minted registration and
		// leaves durable cleanup intent if either side is temporarily unavailable.
		if _, err := c.removeRunner(ctx, current,
			"queued demand disappeared before this runner finished starting", pool); err != nil {
			c.log.Warn("could not finish cancelling a runner whose credential arrived late",
				"runner", current.ID, "state", current.State, "error", err)
		}
	case store.RunnerFailed:
		// A failed start must remain failed long enough to be visible in the
		// pool diagnostics and failure counters. The create task was never
		// published, so only the registration minted by this late response needs
		// cleanup; transitioning the row to removed here would erase the fault.
		c.deleteRegistration(ctx, current, pool)
	case store.RunnerRemoved:
		c.enqueueLifecycle(ctx, current.HostID, agent.Task{
			Kind: agent.TaskRemoveRunner, RunnerID: current.ID, Backend: pool.Backend,
		})
		c.deleteRegistration(ctx, current, pool)
	default:
		// A create task has already advanced this runner. Do not enqueue a second
		// one; GitHub's in-progress event remains authoritative if it is busy.
		c.log.Debug("runner moved on before its detached credential mint finished; skipped duplicate create",
			"runner", current.ID, "state", current.State)
	}
}

// wireSource is the allocation source an agent is sent. The runner's row and
// the API say "profile" where a host's runner profile decided the size, but an
// agent that predates the value reads a source it does not know as a size
// somebody typed, and would hand a docker-in-docker pair's daemon the whole
// figure on top of the runner's -- twice what the host was charged. What an
// agent does with a size is the same for both, one slot split between the
// pair, so it is told "host", which every agent that exists understands.
//
// A reduced or history size is the same thing for a pair whose pool types
// nothing: the ledger charges it one slot and the figure is that slot, so it is
// split too. Left as "reduced" the agent reads it as typed, and a runner placed on a
// host with little to spare -- the only time a reduced size is chosen -- got the
// whole of that little for each container, twice what it was charged. A pool that
// types one or both figures keeps its source: a typed limit goes to both
// containers in full, which is what such a pool asked for.
func wireSource(source string, pool *store.Pool) string {
	switch {
	case source == store.AllocationFromProfile:
		return store.AllocationFromHost
	case (source == store.AllocationReduced || source == store.AllocationHistory) &&
		pool.DockerMode == store.DockerDinD && pool.Resources.CPUs <= 0 && pool.Resources.MemoryMB <= 0:
		return store.AllocationFromHost
	}
	return source
}

// mintCredentials asks GitHub for whatever this pool's runners register with.
//
// Ephemeral pools get a JIT configuration: it registers exactly one runner,
// cannot be replayed, and expires quickly, which is what makes it safe to hand
// to a container through its environment. Non-ephemeral pools have to run
// config.sh, so they get a registration token instead.
func (c *Controller) mintCredentials(ctx context.Context, inst *store.Installation, pool *store.Pool, name string) (creds backend.Credentials, id int64, err error) {
	if c.githubHeld(inst.ID, c.Now()) {
		return backend.Credentials{}, 0, errRegistrationHeld
	}
	defer func() {
		if errors.Is(err, github.ErrRateLimited) && err != errRegistrationHeld {
			c.holdRateLimited(inst.ID, err, c.Now(), "registering a runner")
		}
	}()
	client, err := c.clients.get(ctx, inst)
	if err != nil {
		return backend.Credentials{}, 0, err
	}

	if pool.Ephemeral {
		group, unresolved, publicBlocked := c.clients.runnerGroupID(ctx, inst, client, pool.RunnerGroup)
		c.noteRunnerGroup(pool, pool.RunnerGroup, unresolved, publicBlocked)
		if c.githubHeld(inst.ID, c.Now()) {
			return backend.Credentials{}, 0, errRegistrationHeld
		}
		jit, err := client.CreateJITConfig(ctx, github.JITRequest{
			Name:          name,
			Labels:        pool.Labels,
			RunnerGroupID: group,
		})
		c.observeGitHub(inst.ID, err)
		if err != nil {
			return backend.Credentials{}, 0, err
		}
		return backend.Credentials{JITConfig: jit.Encoded}, jit.RunnerID, nil
	}

	tok, err := client.CreateRegistrationToken(ctx)
	c.observeGitHub(inst.ID, err)
	if err != nil {
		return backend.Credentials{}, 0, err
	}
	return backend.Credentials{
		RegistrationToken: tok.Token,
		ExpiresAt:         tok.ExpiresAt,
		URL:               client.WebURL(),
		RunnerGroup:       pool.RunnerGroup,
		Labels:            pool.Labels,
		NoDefaultLabels:   pool.NoDefaultLabels,
	}, 0, nil
}

// RunnerImage returns the image a pool's runners use.
//
// Three answers in order. A pool that names an image gets it, unchanged: an
// operator who has built their own is not second-guessed. Otherwise the pool's
// platform picks the variant, so a pool called zoomies-4vcpu-debian-12 boots
// the Debian 12 image without anyone keeping the two in step by hand. A pool
// that names neither falls back to the instance default.
//
// Whichever it lands on, a pool that gives its jobs a daemon then runs that
// image's Docker variant. The API already writes the swap into the pool when
// it is saved; it is made again here because this is the one place that sees
// the other two answers, and because what runs should be decided where the
// runner is made rather than trusted to every path that ever wrote a pool row.
func (c *Controller) RunnerImage(p *store.Pool) string {
	return config.ResolvePoolRunnerImage(p.Image, p.Platform.OS, p.Platform.OSVersion,
		c.cfg().GitHub.RunnerImage, p.DockerMode.GivesDaemon())
}

func (c *Controller) runnerVersion(p *store.Pool) string {
	if strings.TrimSpace(p.RunnerVersion) != "" {
		return p.RunnerVersion
	}
	return c.cfg().GitHub.RunnerVersion
}

// ---------------------------------------------------------------------------
// Drain, remove, fail
// ---------------------------------------------------------------------------

// ErrConfirmationRequired is an operator asking to drain a runner that is
// running a job, without having said they accept losing it.
//
// A drain is not the gentle option it reads as. The stop task carries
// agent.DefaultStopTimeout, so the runner gets five minutes and is then killed;
// a twenty-minute job drained at minute one dies at minute six and GitHub marks
// it failed. That is the behaviour -- an operator draining a host for
// maintenance needs the machine to actually empty -- but it must not be
// something they get by accident, so the paths an operator reaches ask first.
//
// The scheduler is deliberately not subject to this. It reaches a drain through
// the unexported drainRunnerID, and never proposes one for a busy runner in the
// first place; automatic scale-down must not be waiting on anybody to confirm.
var ErrConfirmationRequired = errors.New("this runner is running a job, and draining it will end that job")

// DrainRunner asks a runner to finish its current job and exit.
//
// confirmed is the operator saying they accept that a job in flight will be
// ended: see ErrConfirmationRequired. It is ignored for a runner that is not
// busy, because there is nothing to lose.
func (c *Controller) DrainRunner(ctx context.Context, runnerID, reason string, confirmed bool) (*store.Runner, error) {
	r, err := c.st.GetRunner(ctx, runnerID)
	if err != nil {
		return nil, err
	}
	if r.State.Terminal() {
		return nil, fmt.Errorf("%w: runner %s is already %s", store.ErrInvalidTransition, runnerID, r.State)
	}
	if r.State == store.RunnerBusy && !confirmed {
		return nil, fmt.Errorf("%w: runner %s has five minutes to finish and is then stopped", ErrConfirmationRequired, r.Name)
	}
	if reason == "" {
		reason = "drained by an operator"
	}
	return c.drainRunner(ctx, r, reason, nil, false)
}

// RemoveRunner tears a runner down now and deletes its GitHub registration.
// Without force it drains instead, which still ends a running job after the
// stop timeout -- so the unforced path asks for the same acknowledgement a
// direct drain does.
func (c *Controller) RemoveRunner(ctx context.Context, runnerID, reason string, force, confirmed bool) (*store.Runner, error) {
	r, err := c.st.GetRunner(ctx, runnerID)
	if err != nil {
		return nil, err
	}
	if reason == "" {
		reason = "removed by an operator"
	}
	if !force && r.State == store.RunnerBusy {
		if !confirmed {
			return nil, fmt.Errorf("%w: runner %s has five minutes to finish and is then stopped", ErrConfirmationRequired, r.Name)
		}
		return c.drainRunner(ctx, r, reason+" (draining first so the running job finishes)", nil, false)
	}
	if r.State == store.RunnerRemoved {
		return r, nil
	}
	return c.removeRunner(ctx, r, reason, nil)
}

func (c *Controller) drainRunnerID(ctx context.Context, id, reason string, pool *store.Pool) error {
	r, err := c.st.GetRunner(ctx, id)
	if err != nil {
		return err
	}
	_, err = c.drainRunner(ctx, r, reason, pool, true)
	return err
}

// drainRunner moves a runner to draining and asks its host to stop it.
//
// unlessBusy is for the scheduler's own drains, which were decided for a runner
// that was not running a job. Its registration is withdrawn from GitHub first,
// which GitHub refuses for a runner it has given a job; then the row is checked
// in the same write as the transition, so one that has taken a job since is
// refused with store.ErrInvalidTransition instead of having its job stopped.
// An operator's drain passes false: draining a busy runner is what they asked
// for, behind the confirmation DrainRunner has already taken.
func (c *Controller) drainRunner(ctx context.Context, r *store.Runner, reason string, pool *store.Pool, unlessBusy bool) (*store.Runner, error) {
	if !c.mayAct() {
		return nil, errors.New("controller authority is paused; retry after recovery or lease renewal")
	}
	transition := c.st.TransitionRunner
	if unlessBusy {
		if err := c.withdrawRegistration(ctx, r, pool); err != nil {
			return nil, err
		}
		transition = c.st.TransitionRunnerUnlessBusy
	}
	updated, err := transition(ctx, r.ID, store.RunnerDraining, reason)
	if err != nil {
		return nil, err
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	c.enqueueLifecycle(ctx, r.HostID, agent.Task{
		Kind:        agent.TaskStopRunner,
		RunnerID:    r.ID,
		Backend:     c.backendKind(ctx, r, pool),
		StopTimeout: agent.DefaultStopTimeout,
	})
	c.log.Info("draining a runner", "runner", r.ID, "name", r.Name, "reason", reason)
	return updated, nil
}

// withdrawRegistration takes a runner's registration off GitHub before the
// scheduler stops it, and refuses the drain when GitHub will not let it go.
//
// The row is the last thing this controller heard, not what GitHub has since
// done. A runner it still reads as idle can have been handed a job a second
// ago -- on a polling-only fleet, up to a whole sweep ago -- and the stop that
// follows is a SIGINT, which the runner answers by cancelling the job it is
// on. TransitionRunnerUnlessBusy cannot see that job: it guards only what has
// already been recorded here.
//
// GitHub can. It refuses to delete a registration that is running a job, and
// once the registration is gone it has nothing to assign a job to, so asking
// first makes the stop safe whichever way the answer falls. A refusal, or no
// answer at all, leaves the runner as it was: the next pass decides again, by
// which time a sweep has recorded the job if there is one.
//
// It asks GitHub inside a scheduling pass -- a delete, and a listing first for
// a runner whose ID was never recorded -- which removeRunnerID goes out of its
// way to avoid. A drain is rare and a job cancelled by its own fleet is not
// something a later pass can put back, so this one waits, for no longer than
// a known-job check does.
func (c *Controller) withdrawRegistration(ctx context.Context, r *store.Runner, pool *store.Pool) error {
	if pool == nil {
		p, err := c.st.GetPool(ctx, r.PoolID)
		if err != nil {
			return fmt.Errorf("finding the pool of %s to withdraw its registration: %w", r.Name, err)
		}
		pool = p
	}
	if IsDemoID(pool.InstallationID) || (r.GitHubRunnerID == 0 && !store.IsRunnerName(r.Name)) {
		return nil
	}
	inst, err := c.st.GetInstallation(ctx, pool.InstallationID)
	if err != nil {
		return fmt.Errorf("finding the installation of %s to withdraw its registration: %w", r.Name, err)
	}
	if c.githubHeld(inst.ID, c.Now()) {
		return fmt.Errorf("%w: GitHub is rate-limiting the installation of %s, so its registration cannot be withdrawn yet",
			store.ErrInvalidTransition, r.Name)
	}
	client, err := c.clients.get(ctx, inst)
	if err != nil {
		return fmt.Errorf("reaching GitHub to withdraw the registration of %s: %w", r.Name, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, withdrawTimeout)
	defer cancel()
	id := r.GitHubRunnerID
	if id == 0 {
		remote, err := client.ListRunners(callCtx)
		c.observeGitHub(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrRateLimited) {
				c.holdRateLimited(inst.ID, err, c.Now(), "finding a runner registration to withdraw")
			}
			return fmt.Errorf("finding the registration of %s to withdraw it: %w", r.Name, err)
		}
		for _, gr := range remote {
			if gr.Name == r.Name {
				id = gr.ID
				break
			}
		}
		if id == 0 {
			// Never registered, or already gone: nothing can be assigned to it.
			return nil
		}
	}
	err = client.DeleteRunner(callCtx, id)
	c.observeGitHub(inst.ID, err)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, github.ErrRunnerBusy):
		return fmt.Errorf("%w: GitHub says %s is running a job, so it is not being stopped", store.ErrInvalidTransition, r.Name)
	case errors.Is(err, github.ErrRateLimited):
		c.holdRateLimited(inst.ID, err, c.Now(), "withdrawing a runner registration")
	}
	return fmt.Errorf("withdrawing the registration of %s before stopping it: %w", r.Name, err)
}

// removeRunnerID is the scheduler's own path to a removal, reached only from
// apply() under reconcileMu. Deleting the GitHub registration is detached the
// same way minting a credential is in createRunner: the task goes out and this
// returns at once, so a bulk drain does not hold every other pool and
// installation's scheduling for as long as GitHub takes to answer each one in
// turn.
//
// claimRemoval is what makes that safe. The row does not read as
// RunnerRemoved until the detached call finishes -- see finishRemoveRunner --
// so without it the next pass would see the same not-yet-removed runner and
// redecide the identical ActionRemove before the first attempt had finished,
// dispatching a second registration delete alongside the first.
func (c *Controller) removeRunnerID(ctx context.Context, id, reason string, pool *store.Pool) error {
	r, err := c.st.GetRunner(ctx, id)
	if err != nil {
		return err
	}
	if !c.claimRemoval(r.ID) {
		return nil
	}
	c.enqueueLifecycle(ctx, r.HostID, agent.Task{
		Kind:     agent.TaskRemoveRunner,
		RunnerID: r.ID,
		Backend:  c.backendKind(ctx, r, pool),
	})
	c.lifecycleCalls.Add(1)
	go func() {
		defer c.lifecycleCalls.Done()
		defer c.releaseRemoval(r.ID)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleCallTimeout)
		defer cancel()
		if _, err := c.finishRemoveRunner(cctx, r, reason, pool); err != nil {
			c.logRunnerAction("remove", scheduler.Action{RunnerID: id, Reason: reason}, err)
		}
	}()
	return nil
}

// claimRemoval reports whether this runner's removal may proceed, claiming it
// for the caller if so. A runner already claimed keeps whatever pass claimed
// it first.
func (c *Controller) claimRemoval(runnerID string) bool {
	c.removingMu.Lock()
	defer c.removingMu.Unlock()
	if _, ok := c.removing[runnerID]; ok {
		return false
	}
	c.removing[runnerID] = struct{}{}
	return true
}

func (c *Controller) releaseRemoval(runnerID string) {
	c.removingMu.Lock()
	delete(c.removing, runnerID)
	c.removingMu.Unlock()
}

// removeRunner tears the workload down, deletes the GitHub registration and
// marks the row removed, in that order, synchronously -- used by the two
// paths that answer a caller with the result: the operator-facing
// RemoveRunner, and a host reporting a runner it was told to give up as lost.
func (c *Controller) removeRunner(ctx context.Context, r *store.Runner, reason string, pool *store.Pool) (*store.Runner, error) {
	if !c.mayAct() {
		return nil, errors.New("controller authority is paused; retry after recovery or lease renewal")
	}
	c.enqueueLifecycle(ctx, r.HostID, agent.Task{
		Kind:     agent.TaskRemoveRunner,
		RunnerID: r.ID,
		Backend:  c.backendKind(ctx, r, pool),
	})
	return c.finishRemoveRunner(ctx, r, reason, pool)
}

// finishRemoveRunner deletes a runner's GitHub registration and marks its row
// removed, in that order: the registration goes first so that a crash in
// between leaves a row we can still find the registration from.
func (c *Controller) finishRemoveRunner(ctx context.Context, r *store.Runner, reason string, pool *store.Pool) (*store.Runner, error) {
	c.deleteRegistration(ctx, r, pool)

	updated, err := c.st.TransitionRunner(ctx, r.ID, store.RunnerRemoved, reason)
	if err != nil {
		return nil, err
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	c.log.Info("removed a runner", "runner", r.ID, "name", r.Name, "reason", reason)
	if r.State == store.RunnerBusy {
		// Only a forced removal reaches here with a job still running, and
		// that job is about to fail on GitHub for a reason only this fleet
		// knows.
		// An operator chose this. It is the fleet's failure in the sense that
		// the workflow did nothing wrong, and the one kind that needs no
		// fixing -- so it is worth telling apart from the runner that died on
		// its own, which is a fleet with a problem.
		c.noteRunnerLost(ctx, r, sourceController, reason, store.FaultRemoved)
	}
	return updated, nil
}

// failRunnerID marks a runner failed and says what category the failure
// belongs to, so the row, the job under it and the fleet's counts all agree
// about whose fault it was. Callers that genuinely cannot narrow it pass
// store.FaultRunnerExited, which reads as "go and look" rather than as a
// diagnosis nobody made.
func (c *Controller) failRunnerID(ctx context.Context, id, reason string, fault store.FaultKind) error {
	return c.failRunner(ctx, id, reason, fault, false)
}

// failRunner is failRunnerID with the scheduler's condition available: a
// failure planned against a snapshot is refused, with store.ErrInvalidTransition,
// for a runner that has since taken a job, since failing it would end that job.
// The host-lost and agent-exit paths fail busy runners on purpose and pass false.
func (c *Controller) failRunner(ctx context.Context, id, reason string, fault store.FaultKind, unlessBusy bool) error {
	before, err := c.st.GetRunner(ctx, id)
	if err != nil {
		return err
	}
	fail := c.st.FailRunner
	if unlessBusy {
		fail = c.st.FailRunnerUnlessBusy
	}
	updated, err := fail(ctx, id, reason, fault)
	if err != nil {
		return err
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	c.log.Warn("a runner failed", "runner", updated.ID, "name", updated.Name, "reason", reason, "fault", updated.FaultKind)
	c.noteRunnerLost(ctx, before, sourceController, reason, updated.FaultKind)
	c.noteRunnerStartFailure(ctx, before, updated)
	return nil
}

// deleteRegistration removes a runner's registration from GitHub. Failures are
// logged rather than returned: the reaper will find it again, and a GitHub
// outage must not stop Zoomies from freeing the host's capacity.
//
// A JIT-registered runner carries the ID GitHub minted for it. One registered
// with a registration token -- a non-ephemeral pool -- has none until GitHub
// is asked, so it is looked up by name; the alternative was the container's
// own config.sh remove on exit, with a registration token that expires after
// an hour and had usually expired, leaving exactly the ghost that comment
// promised to prevent.
func (c *Controller) deleteRegistration(ctx context.Context, r *store.Runner, pool *store.Pool) {
	if !c.mayAct() {
		return
	}
	if r.GitHubRunnerID == 0 && !store.IsRunnerName(r.Name) {
		c.confirmCleanup(ctx, r.ID, false)
		return
	}
	if pool == nil {
		p, err := c.st.GetPool(ctx, r.PoolID)
		if err != nil {
			return
		}
		pool = p
	}
	if IsDemoID(pool.InstallationID) {
		// Demo fixtures have no GitHub behind them; there is nothing to delete.
		c.confirmCleanup(ctx, r.ID, false)
		return
	}
	inst, err := c.st.GetInstallation(ctx, pool.InstallationID)
	if err != nil {
		return
	}
	if c.githubHeld(inst.ID, c.Now()) {
		return
	}
	client, err := c.clients.get(ctx, inst)
	if err != nil {
		c.log.Warn("could not delete a GitHub runner registration", "runner", r.ID, "error", err)
		return
	}
	id := r.GitHubRunnerID
	if id == 0 {
		remote, err := client.ListRunners(ctx)
		c.observeGitHub(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrRateLimited) {
				c.holdRateLimited(inst.ID, err, c.Now(), "finding a runner registration")
			}
			c.log.Warn("could not list GitHub runners to find a registration to delete", "runner", r.ID, "name", r.Name, "error", err)
			return
		}
		for _, gr := range remote {
			if gr.Name == r.Name {
				if gr.Busy {
					c.deferBusyRegistration(ctx, r)
					return
				}
				id = gr.ID
				break
			}
		}
		if id == 0 {
			// Never registered, or already gone: either way there is nothing
			// to delete, and nothing worth a warning.
			c.confirmCleanup(ctx, r.ID, false)
			return
		}
	}
	err = client.DeleteRunner(ctx, id)
	c.observeGitHub(inst.ID, err)
	if errors.Is(err, github.ErrRunnerBusy) {
		c.deferBusyRegistration(ctx, r)
		return
	}
	if errors.Is(err, github.ErrRateLimited) {
		c.holdRateLimited(inst.ID, err, c.Now(), "deleting a runner registration")
	}
	if err != nil {
		reason := fmt.Sprintf("the GitHub runner registration could not be deleted: %v", err)
		// Recorded on the row, not only logged. A registration Zoomies could
		// not delete is a ghost on somebody's organisation, and a log line and
		// a counter are not something an operator finds before the runner list
		// is full of them. The reap will try again; until it succeeds, the row
		// says so and runners.cleanup_failed names it.
		if rerr := c.st.RecordRegistrationCleanupFailure(ctx, r.ID, reason); rerr != nil {
			c.log.Warn("could not record a failed registration delete", "runner", r.ID, "error", rerr)
		}
		c.log.Warn("could not delete a GitHub runner registration",
			"runner", r.ID, "github_runner_id", id, "error", err)
		return
	}
	c.confirmCleanup(ctx, r.ID, false)
}

// backendKind names the backend a task should run on. It comes from the pool,
// with the host's default as a fallback for a runner whose pool has gone.
func (c *Controller) backendKind(ctx context.Context, r *store.Runner, pool *store.Pool) store.BackendKind {
	if pool != nil {
		return pool.Backend
	}
	if p, err := c.st.GetPool(ctx, r.PoolID); err == nil {
		return p.Backend
	}
	return ""
}

// ---------------------------------------------------------------------------
// Reaping orphaned GitHub registrations
// ---------------------------------------------------------------------------

// reapLoop periodically reconciles Zoomies' view against GitHub's.
func (c *Controller) reapLoop(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// Not while fenced. Reaping deletes GitHub registrations, and a
			// restored database's idea of which runners are gone is as old as
			// the backup -- so the one thing it must not do is act on it.
			if !c.Fenced().Fenced {
				c.reap(ctx)
			}
			timer.Reset(reapInterval)
		}
	}
}

// reap deletes GitHub registrations for runners Zoomies considers gone.
//
// An orphaned registration is not harmless: it sits in the organisation's
// runner list forever, and a workflow targeting its labels can be assigned to
// a runner that no longer exists, where the job waits until it times out.
//
// The rule for what may be deleted is deliberately narrow, because this code
// is one API call away from deleting somebody else's runners:
//
//   - the name must carry the "zoomies-" prefix that store.NewRunnerName mints,
//     so a runner another tool registered is never a candidate;
//   - and either Zoomies has a row for it in a terminal state (we know it is
//     dead), or Zoomies has no row at all and GitHub reports it offline (the
//     row was lost, and an offline registration can do no work anyway).
//
// A registration Zoomies has a live row for, or one that is online but
// unknown, is left alone: those are cases where deleting would interrupt work.
func (c *Controller) reap(ctx context.Context) {
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		c.log.Error("could not list installations to reap runner registrations", "error", err)
		return
	}
	now := c.Now()
	for _, inst := range insts {
		if ctx.Err() != nil {
			return
		}
		// An installation already refusing for quota is not asked again until
		// the hold runs out. The reap shares that hold with the poller: both
		// spend the same quota, so a stand-down either of them earned is one
		// the other would otherwise spend a refused call rediscovering.
		if c.githubHeld(inst.ID, now) {
			continue
		}
		client, err := c.clients.get(ctx, inst)
		if err != nil {
			continue
		}
		remote, err := client.ListRunners(ctx)
		c.observeGitHub(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrRateLimited) {
				c.holdRateLimited(inst.ID, err, now, "listing runners")
				continue
			}
			c.log.Warn("could not list GitHub runners while reaping", "installation", inst.ID, "error", err)
			continue
		}
		// A successful, fully paginated listing is also positive evidence
		// for registrations GitHub removed itself while cleanup was pending.
		presentNames := make(map[string]bool, len(remote))
		presentIDs := make(map[int64]bool, len(remote))
		for _, gr := range remote {
			presentNames[gr.Name] = true
			presentIDs[gr.ID] = true
		}
		pending, perr := c.st.RunnersPendingRegistrationCleanup(ctx, inst.ID)
		if perr != nil {
			c.log.Warn("could not load pending registration cleanup", "installation", inst.ID, "error", perr)
			continue
		}
		for _, row := range pending {
			if !presentNames[row.Name] && (row.GitHubRunnerID == 0 || !presentIDs[row.GitHubRunnerID]) {
				c.confirmCleanup(ctx, row.ID, false)
			}
		}
		// A fleet's runners share a handful of pools, so the pool each row
		// names is read once per pass rather than once per registration.
		// Only a successful read is remembered: a failed one is retried for
		// the next registration, exactly as before.
		pools := map[string]*store.Pool{}
		for _, gr := range remote {
			if !store.IsRunnerName(gr.Name) {
				continue
			}
			row, rerr := c.st.GetRunnerByName(ctx, gr.Name)
			if rerr == nil {
				pool, ok := pools[row.PoolID]
				var err error
				if !ok {
					pool, err = c.st.GetPool(ctx, row.PoolID)
					if err == nil {
						pools[row.PoolID] = pool
					}
				}
				if err != nil || pool.InstallationID != inst.ID || (row.GitHubRunnerID != 0 && row.GitHubRunnerID != gr.ID) {
					continue
				}
			}
			if gr.Busy {
				if rerr == nil && row.State.Terminal() {
					c.deferBusyRegistration(ctx, row)
				}
				continue
			}
			switch {
			case rerr == nil && row.State.Terminal():
				// We know this one is dead.
			case errors.Is(rerr, store.ErrNotFound) && strings.EqualFold(gr.Status, "offline"):
				// Our row is gone and the runner cannot pick up work.
			default:
				continue
			}
			err := client.DeleteRunner(ctx, gr.ID)
			c.observeGitHub(inst.ID, err)
			if errors.Is(err, github.ErrRateLimited) {
				// Stop on this installation rather than working down the rest
				// of its list. Every one of them would be refused the same
				// way, and each refusal is another call against a quota that
				// is already gone -- which is how a reap turns one exhausted
				// window into two.
				c.holdRateLimited(inst.ID, err, now, "deleting an orphaned registration")
				break
			}
			if errors.Is(err, github.ErrRunnerBusy) {
				if row != nil {
					c.deferBusyRegistration(ctx, row)
				}
				continue
			}
			if err != nil {
				// Recorded on the row when there is one, the same as the
				// first attempt: without this the panel freezes on whatever
				// the original failure said while the reap loop keeps
				// quietly retrying (or not) behind it.
				if row != nil {
					reason := fmt.Sprintf("the GitHub runner registration could not be deleted: %v", err)
					if rerr := c.st.RecordRegistrationCleanupFailure(ctx, row.ID, reason); rerr != nil {
						c.log.Warn("could not record a failed registration delete", "runner", row.ID, "error", rerr)
					}
				}
				c.log.Warn("could not delete an orphaned runner registration",
					"installation", inst.ID, "runner_name", gr.Name, "error", err)
				continue
			}
			c.log.Info("deleted an orphaned GitHub runner registration",
				"installation", inst.ID, "target", inst.Target, "runner_name", gr.Name)
			// The reap is the retry for a delete that failed earlier, so it is
			// also what clears the row's complaint about it.
			if row, rerr := c.st.GetRunnerByName(ctx, gr.Name); rerr == nil {
				c.confirmCleanup(ctx, row.ID, false)
			}
		}
	}
}

// GitHub's busy flag is authoritative for deletion even when our row is terminal.
// Do not cancel a workflow or count waiting as another failed delete.
func (c *Controller) deferBusyRegistration(ctx context.Context, r *store.Runner) {
	reason := fmt.Sprintf("GitHub still reports %s as running a job; cleanup is deferred until GitHub reports it idle or absent", r.Name)
	if err := c.st.DeferRegistrationCleanup(ctx, r.ID, reason); err != nil {
		c.log.Warn("could not record deferred registration cleanup", "runner", r.ID, "error", err)
		return
	}
	c.publishRunnerByID(ctx, r.ID)
}
