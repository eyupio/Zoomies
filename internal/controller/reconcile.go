package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
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

// reconcileLoop runs a pass on the configured interval and immediately on
// every nudge, with only one pass in flight at a time.
func (c *Controller) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(c.schedulerInterval())
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
	}
}

// reconcileNow runs one pass and logs a failure rather than propagating it: a
// transient database or GitHub error must not stop the loop.
func (c *Controller) reconcileNow(ctx context.Context) {
	if err := c.Reconcile(ctx); err != nil && ctx.Err() == nil {
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
	snap, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	plan := scheduler.Decide(snap)
	c.setLastPlan(plan)
	c.apply(ctx, snap, plan)
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
	runners := make(map[string][]*store.Runner, len(pools))
	for _, p := range pools {
		rs, err := c.st.ListRunnersForPool(ctx, p.ID)
		if err != nil {
			return scheduler.Snapshot{}, fmt.Errorf("listing runners for pool %s: %w", p.Name, err)
		}
		runners[p.ID] = rs
	}
	jobs, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing queued jobs: %w", err)
	}
	activeByRepository, queuedByRepository, err := c.st.RepositoryJobCounts(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("counting jobs by repository: %w", err)
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return scheduler.Snapshot{}, fmt.Errorf("listing hosts: %w", err)
	}
	return scheduler.Snapshot{
		Now:                c.Now(),
		Pools:              pools,
		Runners:            runners,
		Jobs:               jobs,
		ActiveByRepository: activeByRepository,
		QueuedByRepository: queuedByRepository,
		Hosts:              hosts,
		Policy:             c.policy(),
	}, nil
}

// apply executes a plan pool by pool and records what actually happened.
//
// A failure on one action never abandons the rest: one pool being unable to
// reach GitHub must not stop another pool from draining an idle runner.
func (c *Controller) apply(ctx context.Context, snap scheduler.Snapshot, plan scheduler.Plan) {
	pools := make(map[string]*store.Pool, len(snap.Pools))
	for _, p := range snap.Pools {
		pools[p.ID] = p
	}

	for _, pp := range plan.Pools {
		pool := pools[pp.PoolID]
		if pool == nil {
			continue
		}
		created, drained := 0, 0
		for _, a := range pp.Actions {
			if ctx.Err() != nil {
				return
			}
			switch a.Kind {
			case scheduler.ActionCreate:
				if err := c.createRunner(ctx, pool, a); err != nil {
					c.log.Error("could not create a runner",
						"pool", pool.Name, "host", a.HostID, "reason", a.Reason, "error", err)
					continue
				}
				created++
			case scheduler.ActionDrain:
				if err := c.drainRunnerID(ctx, a.RunnerID, a.Reason, pool); err != nil {
					c.logRunnerAction("drain", a, err)
					continue
				}
				drained++
			case scheduler.ActionRemove:
				if err := c.removeRunnerID(ctx, a.RunnerID, a.Reason, pool); err != nil {
					c.logRunnerAction("remove", a, err)
				}
			case scheduler.ActionFail:
				if err := c.failRunnerID(ctx, a.RunnerID, a.Reason); err != nil {
					c.logRunnerAction("fail", a, err)
				}
			}
		}
		c.recordScaling(ctx, pp, created, drained)
		c.noteBlocked(pp)
	}
}

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
func (c *Controller) createRunner(ctx context.Context, pool *store.Pool, a scheduler.Action) error {
	inst, err := c.st.GetInstallation(ctx, pool.InstallationID)
	if err != nil {
		return fmt.Errorf("pool %s points at installation %s, which is not there; edit the pool to choose an installation: %w",
			pool.Name, pool.InstallationID, err)
	}

	name := github.RunnerName()
	r := &store.Runner{
		PoolID:        pool.ID,
		HostID:        a.HostID,
		Name:          name,
		State:         store.RunnerProvisioning,
		Ephemeral:     pool.Ephemeral,
		Labels:        pool.Labels,
		Image:         c.runnerImage(pool),
		RunnerVersion: c.runnerVersion(pool),
		Message:       a.Reason,
	}
	if err := c.st.CreateRunner(ctx, r); err != nil {
		return fmt.Errorf("creating the runner row for %s: %w", name, err)
	}
	if queued, err := c.st.ListQueuedJobs(ctx); err == nil {
		for _, j := range queued {
			if j.PoolID == pool.ID {
				observeDuration(c.metrics.queuedToCreate, pool.Name, string(pool.Backend), j.QueuedAt, r.CreatedAt)
				break
			}
		}
	}
	c.publishRunner(ctx, events.KindRunnerCreated, r)

	creds, ghID, err := c.mintCredentials(ctx, inst, pool, name)
	if err != nil {
		msg := fmt.Sprintf("GitHub would not register %s: %v", name, err)
		if failed, ferr := c.st.TransitionRunner(ctx, r.ID, store.RunnerFailed, msg); ferr == nil {
			c.publishRunner(ctx, events.KindRunnerUpdated, failed)
		} else {
			c.log.Error("could not mark a runner failed after its registration failed",
				"runner", r.ID, "error", ferr)
		}
		return err
	}
	if ghID != 0 {
		if err := c.st.SetRunnerGitHubID(ctx, r.ID, ghID); err != nil {
			c.log.Error("could not record the GitHub runner ID", "runner", r.ID, "error", err)
		}
		r.GitHubRunnerID = ghID
	}

	spec := backend.Spec{
		Name:          name,
		RunnerID:      r.ID,
		PoolID:        pool.ID,
		PoolName:      pool.Name,
		Image:         r.Image,
		PullPolicy:    pool.PullPolicy,
		Credentials:   creds,
		Env:           pool.Env,
		Ephemeral:     pool.Ephemeral,
		Resources:     pool.Resources,
		Cache:         pool.Cache,
		Repository:    inst.Target,
		DockerMode:    pool.DockerMode,
		RunAsRoot:     pool.RunAsRoot,
		Network:       c.cfg.Agent.Network,
		RunnerVersion: r.RunnerVersion,
	}
	c.enqueue(a.HostID, agent.Task{
		Kind:     agent.TaskCreateRunner,
		RunnerID: r.ID,
		Spec:     &spec,
		Backend:  pool.Backend,
	})
	c.log.Info("creating a runner",
		"pool", pool.Name, "runner", r.ID, "name", name, "host", a.HostID, "reason", a.Reason)
	return nil
}

// mintCredentials asks GitHub for whatever this pool's runners register with.
//
// Ephemeral pools get a JIT configuration: it registers exactly one runner,
// cannot be replayed, and expires quickly, which is what makes it safe to hand
// to a container through its environment. Non-ephemeral pools have to run
// config.sh, so they get a registration token instead.
func (c *Controller) mintCredentials(ctx context.Context, inst *store.Installation, pool *store.Pool, name string) (backend.Credentials, int64, error) {
	client, err := c.clients.get(ctx, inst)
	if err != nil {
		return backend.Credentials{}, 0, err
	}

	if pool.Ephemeral {
		group := c.clients.runnerGroupID(ctx, inst, client, pool.RunnerGroup)
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
		URL:               client.WebURL(),
		RunnerGroup:       pool.RunnerGroup,
		Labels:            pool.Labels,
	}, 0, nil
}

// runnerImage returns the image a pool's runners use, falling back to the
// instance default so a pool created without one still works.
func (c *Controller) runnerImage(p *store.Pool) string {
	if strings.TrimSpace(p.Image) != "" {
		return p.Image
	}
	return c.cfg.GitHub.RunnerImage
}

func (c *Controller) runnerVersion(p *store.Pool) string {
	if strings.TrimSpace(p.RunnerVersion) != "" {
		return p.RunnerVersion
	}
	return c.cfg.GitHub.RunnerVersion
}

// ---------------------------------------------------------------------------
// Drain, remove, fail
// ---------------------------------------------------------------------------

// DrainRunner asks a runner to finish its current job and exit. It never
// interrupts work in progress: the state change is what the agent's stop task
// means, and a busy runner keeps its job until the job ends.
func (c *Controller) DrainRunner(ctx context.Context, runnerID, reason string) (*store.Runner, error) {
	r, err := c.st.GetRunner(ctx, runnerID)
	if err != nil {
		return nil, err
	}
	if r.State.Terminal() {
		return nil, fmt.Errorf("%w: runner %s is already %s", store.ErrInvalidTransition, runnerID, r.State)
	}
	if reason == "" {
		reason = "drained by an operator"
	}
	return c.drainRunner(ctx, r, reason, nil)
}

// RemoveRunner tears a runner down now and deletes its GitHub registration.
// Without force it drains instead, so a running job is not interrupted.
func (c *Controller) RemoveRunner(ctx context.Context, runnerID, reason string, force bool) (*store.Runner, error) {
	r, err := c.st.GetRunner(ctx, runnerID)
	if err != nil {
		return nil, err
	}
	if reason == "" {
		reason = "removed by an operator"
	}
	if !force && r.State == store.RunnerBusy {
		return c.drainRunner(ctx, r, reason+" (draining first so the running job finishes)", nil)
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
	_, err = c.drainRunner(ctx, r, reason, pool)
	return err
}

// drainRunner moves a runner to draining and asks its host to stop it.
func (c *Controller) drainRunner(ctx context.Context, r *store.Runner, reason string, pool *store.Pool) (*store.Runner, error) {
	updated, err := c.st.TransitionRunner(ctx, r.ID, store.RunnerDraining, reason)
	if err != nil {
		return nil, err
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	c.enqueue(r.HostID, agent.Task{
		Kind:        agent.TaskStopRunner,
		RunnerID:    r.ID,
		Backend:     c.backendKind(ctx, r, pool),
		StopTimeout: agent.DefaultStopTimeout,
	})
	c.log.Info("draining a runner", "runner", r.ID, "name", r.Name, "reason", reason)
	return updated, nil
}

func (c *Controller) removeRunnerID(ctx context.Context, id, reason string, pool *store.Pool) error {
	r, err := c.st.GetRunner(ctx, id)
	if err != nil {
		return err
	}
	_, err = c.removeRunner(ctx, r, reason, pool)
	return err
}

// removeRunner tears the workload down, deletes the GitHub registration and
// marks the row removed, in that order: the task goes first because the agent
// is the slow part, and the registration goes before the row so that a crash
// in between leaves a row we can still find the registration from.
func (c *Controller) removeRunner(ctx context.Context, r *store.Runner, reason string, pool *store.Pool) (*store.Runner, error) {
	c.enqueue(r.HostID, agent.Task{
		Kind:     agent.TaskRemoveRunner,
		RunnerID: r.ID,
		Backend:  c.backendKind(ctx, r, pool),
	})
	c.deleteRegistration(ctx, r, pool)

	updated, err := c.st.TransitionRunner(ctx, r.ID, store.RunnerRemoved, reason)
	if err != nil {
		return nil, err
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	c.log.Info("removed a runner", "runner", r.ID, "name", r.Name, "reason", reason)
	return updated, nil
}

func (c *Controller) failRunnerID(ctx context.Context, id, reason string) error {
	updated, err := c.st.TransitionRunner(ctx, id, store.RunnerFailed, reason)
	if err != nil {
		return err
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	c.log.Warn("a runner failed", "runner", updated.ID, "name", updated.Name, "reason", reason)
	return nil
}

// deleteRegistration removes a runner's registration from GitHub. Failures are
// logged rather than returned: the reaper will find it again, and a GitHub
// outage must not stop Zoomies from freeing the host's capacity.
func (c *Controller) deleteRegistration(ctx context.Context, r *store.Runner, pool *store.Pool) {
	if r.GitHubRunnerID == 0 {
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
		return
	}
	inst, err := c.st.GetInstallation(ctx, pool.InstallationID)
	if err != nil {
		return
	}
	client, err := c.clients.get(ctx, inst)
	if err != nil {
		c.log.Warn("could not delete a GitHub runner registration", "runner", r.ID, "error", err)
		return
	}
	err = client.DeleteRunner(ctx, r.GitHubRunnerID)
	c.observeGitHub(inst.ID, err)
	if err != nil {
		c.log.Warn("could not delete a GitHub runner registration",
			"runner", r.ID, "github_runner_id", r.GitHubRunnerID, "error", err)
	}
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
			c.reap(ctx)
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
	for _, inst := range insts {
		if ctx.Err() != nil {
			return
		}
		client, err := c.clients.get(ctx, inst)
		if err != nil {
			continue
		}
		remote, err := client.ListRunners(ctx)
		c.observeGitHub(inst.ID, err)
		if err != nil {
			c.log.Warn("could not list GitHub runners while reaping", "installation", inst.ID, "error", err)
			continue
		}
		for _, gr := range remote {
			if !store.IsRunnerName(gr.Name) {
				continue
			}
			if gr.Busy {
				continue
			}
			row, rerr := c.st.GetRunnerByName(ctx, gr.Name)
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
			if err != nil {
				c.log.Warn("could not delete an orphaned runner registration",
					"installation", inst.ID, "runner_name", gr.Name, "error", err)
				continue
			}
			c.log.Info("deleted an orphaned GitHub runner registration",
				"installation", inst.ID, "target", inst.Target, "runner_name", gr.Name)
		}
	}
}
