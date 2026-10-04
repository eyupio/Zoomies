package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// modeOf reads one of the two switches. An unset or unknown value is off: a
// setting this build has not heard of must not turn a feature on.
func modeOf(v string) string {
	switch v {
	case scheduler.SizeShadow, scheduler.SizeOn:
		return v
	}
	return scheduler.SizeOff
}

// sizeMode is scheduler.size_routing: whether jobs are classed, and whether
// they are sent anywhere on the strength of it.
func (c *Controller) sizeMode() string { return modeOf(c.cfg().Scheduler.SizeRouting) }

// autoMode is scheduler.auto_pools.
func (c *Controller) autoMode() string { return modeOf(c.cfg().Scheduler.AutoPools) }

// AutoPoolsMode is scheduler.auto_pools as the controller reads it, for the
// handlers that must refuse what the reconciler would only undo.
func (c *Controller) AutoPoolsMode() string { return c.autoMode() }

// TracksClasses reports whether hosts are being given a size class, which is
// when a host's size tag has to be one.
func (c *Controller) TracksClasses() bool { return c.tracksClasses() }

// tracksClasses reports whether hosts are given a class at all: either switch
// being on, or being watched, is a reason to know what class each host is in,
// and neither is a reason to write a column nobody reads.
func (c *Controller) tracksClasses() bool {
	return c.sizeMode() != scheduler.SizeOff || c.autoMode() != scheduler.SizeOff
}

// sizeConfig is the settings the pure functions cannot read, gathered for them.
func (c *Controller) sizeConfig() scheduler.SizeConfig {
	cfg := c.cfg()
	s, r := cfg.Scheduler, cfg.Runners
	return scheduler.SizeConfig{
		SmallMax:     scheduler.HostLimit{CPUs: s.SizeSmallMaxCPUs, MemoryMB: s.SizeSmallMaxMemoryMB},
		MediumMax:    scheduler.HostLimit{CPUs: s.SizeMediumMaxCPUs, MemoryMB: s.SizeMediumMaxMemoryMB},
		Small:        store.RunnerSize{CPUs: r.SmallCPUs, MemoryMB: r.SmallMemoryMB},
		Medium:       store.RunnerSize{CPUs: r.MediumCPUs, MemoryMB: r.MediumMemoryMB},
		Large:        store.RunnerSize{CPUs: r.LargeCPUs, MemoryMB: r.LargeMemoryMB},
		Hold:         s.SizeClassHold,
		DefaultClass: store.SizeClass(s.SizeDefaultClass),
		FallbackWait: s.SizeFallbackWait,
	}
}

// routable is a job as the scheduler should claim it: sent to its class while
// size routing is on, and exactly as it always was otherwise. The class is on
// the row either way -- the Jobs page shows it while it is only being watched
// -- so this is where "watching" is kept from becoming "acting", and every
// place that asks which pool a job belongs to asks through it.
func (c *Controller) routable(j *store.Job) *store.Job {
	if j == nil || j.RoutedClass == "" || c.sizeMode() == scheduler.SizeOn {
		return j
	}
	cp := *j
	cp.RoutedClass = ""
	return &cp
}

// routableJobs is routable over a listing, copying only the jobs that need it.
func (c *Controller) routableJobs(jobs []*store.Job) []*store.Job {
	if c.sizeMode() == scheduler.SizeOn {
		return jobs
	}
	out := make([]*store.Job, len(jobs))
	for i, j := range jobs {
		out[i] = c.routable(j)
	}
	return out
}

// bestPool is scheduler.BestPool for a job read from the store.
func (c *Controller) bestPool(pools []*store.Pool, j *store.Job) *store.Pool {
	return scheduler.BestPool(pools, c.routable(j))
}

// sizeOnArrival works out the class a job joins the queue in, before it is
// claimed, so the claim can use it. It writes nothing: it fills the size fields
// on the job in hand and returns what ApplyJob has to stamp once the job has a
// row, or nil when there is nothing to stamp.
//
// A job that is already known keeps whatever classed it first, and the claim
// reads that, so a later delivery for the same job cannot send it somewhere
// else than its first did. That holds for every delivery and not only for a
// queued one: the in_progress and completed deliveries of a job that a runner of
// its class took are claimed afresh too, and a claim made without the route would
// credit the job to whichever pool of its labels sorts first, so the Jobs page,
// the per-pool counters and the usage history would all name a pool the job
// never ran in. A job first seen already running was answered by whatever is
// running it, and has no class to be routed to.
//
// A job that no pool of this fleet answers is not this fleet's work -- a job for
// GitHub's own runners, or another provider's, or one whose labels name nothing
// here -- and is not classed: every such job in every repository the installation
// covers would otherwise carry a class badge, a line on its timeline and a count
// in the metric, for a decision nobody was making about it.
func (c *Controller) sizeOnArrival(ctx context.Context, job *store.Job, pools []*store.Pool) *store.JobClassing {
	mode := c.sizeMode()
	if mode == scheduler.SizeOff {
		return nil
	}
	existing, err := c.st.GetJobByGitHubID(ctx, job.GitHubJobID)
	switch {
	case err == nil:
		job.SizeClass, job.SizeReason, job.SizeBasis, job.SizeFloorMB = existing.SizeClass, existing.SizeReason, existing.SizeBasis, existing.SizeFloorMB
		job.RoutedClass, job.RoutedNote = existing.RoutedClass, existing.RoutedNote
		if existing.SizeBasis != "" {
			return nil
		}
		// Known, but from before size routing was on: classed now.
	case !errors.Is(err, store.ErrNotFound):
		c.log.Warn("could not look up a job before classing it; it is claimed without a class", "github_job_id", job.GitHubJobID, "error", err)
		return nil
	}
	if job.State != store.JobQueued && job.State != store.JobWaiting {
		return nil
	}
	if c.bestPool(pools, job) == nil {
		return nil
	}

	cl := c.sizeConfig().AtQueue(job.Labels, c.sizePinFor(ctx, job.Repo, job.Workflow, job.JobName), c.keptClass(ctx, job.Repo, job.Workflow, job.JobName))
	classing := store.JobClassing{Class: cl.Class, Reason: cl.Reason, Basis: cl.Basis, FloorMB: cl.FloorMB, Route: mode == scheduler.SizeOn}
	job.SizeClass, job.SizeReason, job.SizeBasis, job.SizeFloorMB = cl.Class, cl.Reason, cl.Basis, cl.FloorMB
	job.RoutedClass = ""
	if classing.Route {
		job.RoutedClass = cl.Class
	}
	return &classing
}

// stampSize writes what sizeOnArrival decided, and returns the job as it is
// stored. A failure costs the job its class and nothing else: it is claimed
// and run as it always was.
func (c *Controller) stampSize(ctx context.Context, saved *store.Job, classing *store.JobClassing) *store.Job {
	if classing == nil || saved == nil {
		return saved
	}
	stamped, wrote, err := c.st.StampJobClass(ctx, saved.ID, *classing)
	if err != nil {
		c.log.Warn("could not record a job's size class", "job", saved.ID, "error", err)
		return saved
	}
	// Counted by the delivery that classed the job: the webhook and the poller
	// both see a queued job, and the second finds it already stamped, as does a
	// late delivery for a job that has since started and was never classed.
	if wrote {
		c.metrics.jobsSized.WithLabelValues(stamped.SizeBasis, string(stamped.SizeClass), c.sizeMode()).Inc()
	}
	return stamped
}

// sizePinFor is the pin that applies to a job, nil for none. A failed read
// answers as none, and says so: a class worked out without the pin is wrong in
// a way an operator can see and undo, where a job refused for it is not.
func (c *Controller) sizePinFor(ctx context.Context, repo, workflow, job string) *store.SizePin {
	pin, err := c.st.SizePinFor(ctx, repo, workflow, job)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("could not read the size pins; classing a job without them", "repo", repo, "error", err)
		}
		return nil
	}
	return pin
}

// keptClass is the class kept from a job's earlier runs, nil for a job that has
// none.
func (c *Controller) keptClass(ctx context.Context, repo, workflow, job string) *store.JobClass {
	kept, err := c.st.GetJobClass(ctx, repo, workflow, job)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("could not read a job's kept class", "repo", repo, "job_name", job, "error", err)
		}
		return nil
	}
	return kept
}

// sizeMessage is the line the job's timeline carries for how it was classed.
func (c *Controller) sizeMessage(j *store.Job) string {
	msg := fmt.Sprintf("classed %s because %s", j.SizeClass, j.SizeReason)
	if j.RoutedClass == "" {
		msg += "; size routing is only watching, so nothing was sent anywhere on its account"
	}
	return msg
}

// refreshJobClass works a job's class out again from its runs, when one of them
// has just finished, and keeps the answer for the next time the job is queued.
// Only a measured run is evidence, and an unmeasured one changes nothing.
//
// The pin and the job's own labels are left out of this on purpose: the kept
// class is what the runs say, so that removing a pin hands the job back to its
// history and not to a default.
func (c *Controller) refreshJobClass(ctx context.Context, j *store.Job) {
	if j == nil || j.Repo == "" || c.sizeMode() == scheduler.SizeOff || IsDemoID(j.InstallationID) {
		return
	}
	if j.PeakCPUs <= 0 && j.PeakMemoryMB <= 0 && !j.OOMKilled {
		return
	}
	now := c.Now()
	runs, err := c.st.JobClassHistory(ctx, j.Repo, j.Workflow, j.JobName, now.Add(-scheduler.ClassWindow), store.JobClassHistoryLimit)
	if err != nil {
		c.log.Warn("could not read a job's history to class it", "job", j.ID, "error", err)
		return
	}
	cl := c.sizeConfig().Classify(scheduler.ClassifyInput{Runs: runs, Current: c.keptClass(ctx, j.Repo, j.Workflow, j.JobName), Now: now})
	if cl.Basis != store.SizeBasisHistory {
		return
	}
	kept := &store.JobClass{Repo: j.Repo, Workflow: j.Workflow, JobName: j.JobName,
		Class: cl.Class, Reason: cl.Reason, Basis: cl.Basis, FloorMB: cl.FloorMB, Runs: cl.Runs}
	previous, err := c.st.PutJobClass(ctx, kept)
	if err != nil {
		c.log.Warn("could not keep a job's class", "job", j.ID, "error", err)
		return
	}
	if previous != cl.Class {
		key := strings.Join([]string{j.Repo, j.Workflow, j.JobName}, " / ")
		from := "no class"
		if previous != "" {
			from = string(previous)
		}
		c.systemAudit(ctx, "size_class.move", "job_class", key,
			map[string]any{"class": string(previous)},
			map[string]any{"class": string(cl.Class), "cause": fmt.Sprintf("%s was in %s, and is now %s because %s", j.JobName, from, cl.Class, cl.Reason)})
	}
}

// stampJobRan records the class of the host that took a job, once.
func (c *Controller) stampJobRan(ctx context.Context, j *store.Job, runner *store.Runner) {
	if j == nil || j.RunnerID == "" || j.RanClass != "" || !c.tracksClasses() || IsDemoID(j.InstallationID) {
		return
	}
	if runner == nil || runner.ID != j.RunnerID {
		r, err := c.st.GetRunner(ctx, j.RunnerID)
		if err != nil {
			return
		}
		runner = r
	}
	host, err := c.st.GetHost(ctx, runner.HostID)
	if err != nil {
		return
	}
	class, _ := host.EffectiveSizeClass()
	if !class.Valid() {
		return
	}
	stamped, wrote, err := c.st.StampJobRan(ctx, j.ID, class)
	if err != nil {
		c.log.Warn("could not record the class of the host that took a job", "job", j.ID, "error", err)
		return
	}
	if j.RanClass == "" {
		j.RanClass = stamped.RanClass
	}
	// Counted by the delivery that wrote it, because the webhook and the poller
	// both see the job start and the second finds it already recorded.
	if wrote && j.SizeClass.Valid() {
		c.metrics.jobsRan.WithLabelValues(string(j.SizeClass), string(stamped.RanClass), c.sizeMode()).Inc()
	}
}

// ReclassifyQueuedJobs puts the jobs waiting for a runner back through the
// classification, for the jobs of one repository, one workflow or one job. It is
// what makes an operator's pin reach the queue and not only the jobs that arrive
// afterwards: a pin made to fix a job that is stuck in the wrong class has to fix
// that one. It returns how many jobs changed class.
func (c *Controller) ReclassifyQueuedJobs(ctx context.Context, repo, workflow, jobName string) (int, error) {
	// A pin is what the label advice leaves a job alone for, whichever way it
	// changed.
	c.forgetLabelAdvice()
	mode := c.sizeMode()
	if mode == scheduler.SizeOff {
		return 0, nil
	}
	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return 0, err
	}
	// Jobs held for a deployment review are classed when they arrive, and a pin
	// made while they wait for an approver is what they should be queued by.
	held, err := c.st.ListHeldJobs(ctx)
	if err != nil {
		return 0, err
	}
	queued = append(queued, held...)
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return 0, err
	}
	cfg := c.sizeConfig()
	changed := 0
	// The pools of the classes the jobs were put in have demand they did not have,
	// whether or not the loop got to the end of the queue.
	defer func() {
		if changed > 0 {
			c.Nudge()
		}
	}()
	for _, j := range queued {
		if (j.State != store.JobQueued && j.State != store.JobWaiting) || !strings.EqualFold(j.Repo, repo) ||
			(workflow != "" && j.Workflow != workflow) || (jobName != "" && j.JobName != jobName) {
			continue
		}
		cl := cfg.AtQueue(j.Labels, c.sizePinFor(ctx, j.Repo, j.Workflow, j.JobName), c.keptClass(ctx, j.Repo, j.Workflow, j.JobName))
		moved, err := c.st.SetQueuedJobClass(ctx, j.ID, store.JobClassing{
			Class: cl.Class, Reason: cl.Reason, Basis: cl.Basis, FloorMB: cl.FloorMB, Route: mode == scheduler.SizeOn,
		})
		if err != nil {
			return changed, err
		}
		if !moved {
			continue
		}
		changed++
		if updated, err := c.st.GetJob(ctx, j.ID); err == nil {
			updated = c.reclaim(ctx, pools, updated)
			c.appendJobEvent(ctx, updated, store.JobEventSized, c.sizeMessage(updated))
			c.publishJob(ctx, updated)
		}
	}
	return changed, nil
}

// reclaim points a job whose route has just changed at the pool that claims it
// now, and returns the job as it is stored. The scheduler does not need it --
// every pass works the claim out afresh -- but the Jobs page and a pool's queue
// count read the row, and they would otherwise go on listing the job under the
// pool it was sent away from.
func (c *Controller) reclaim(ctx context.Context, pools []*store.Pool, j *store.Job) *store.Job {
	var poolID string
	if p := c.bestPool(pools, j); p != nil {
		poolID = p.ID
	}
	moved, err := c.st.SetJobClaim(ctx, j.ID, poolID)
	if err != nil {
		c.log.Warn("could not point a re-routed job at the pool that claims it", "job", j.ID, "error", err)
		return j
	}
	if !moved {
		return j
	}
	if fresh, err := c.st.GetJob(ctx, j.ID); err == nil {
		return fresh
	}
	return j
}

// appendJobEvent adds a line to a job's timeline from the controller itself.
func (c *Controller) appendJobEvent(ctx context.Context, j *store.Job, kind store.JobEventKind, message string) {
	e := &store.JobEvent{JobID: j.ID, Kind: kind, Source: "controller", Message: message, At: c.Now()}
	if err := c.st.AppendJobEvent(ctx, e); err != nil {
		c.log.Warn("could not record a job timeline entry", "job", j.ID, "kind", kind, "error", err)
	}
}

// routeFallbacks sends the jobs the plan found stuck to another class, and says
// so on each one's timeline. It acts only while size routing is on and this
// controller may act, and it writes the routing only: the next pass claims the
// job for the pool of its new class, so a move costs one pass and no second
// scheduling decision is made outside the scheduler.
func (c *Controller) routeFallbacks(ctx context.Context, snap scheduler.Snapshot, plan scheduler.Plan) {
	if c.sizeMode() != scheduler.SizeOn || !c.mayAct() {
		return
	}
	moves := scheduler.Fallbacks(scheduler.RoutingInput{
		Now: snap.Now, Pools: snap.Pools, Hosts: snap.Hosts, Jobs: snap.Jobs, Plan: plan, Config: c.sizeConfig(),
	})
	if len(moves) == 0 {
		return
	}
	for _, m := range moves {
		moved, err := c.st.SetJobRouted(ctx, m.Job.ID, m.From, m.To, m.Note)
		if err != nil {
			c.log.Warn("could not send a job to another size class", "job", m.Job.ID, "to", m.To, "error", err)
			continue
		}
		if !moved {
			continue
		}
		if updated, err := c.st.GetJob(ctx, m.Job.ID); err == nil {
			updated = c.reclaim(ctx, snap.Pools, updated)
			c.appendJobEvent(ctx, updated, store.JobEventSized, fmt.Sprintf("sent to the %s pool: %s", m.To, m.Note))
			c.publishJob(ctx, updated)
		}
		c.metrics.jobsRerouted.WithLabelValues(string(m.From), string(m.To)).Inc()
	}
	// The pools of the classes the jobs were sent to have demand they did not
	// have a moment ago.
	c.Nudge()
}

// systemAudit records something the controller did on its own, under the system
// identity -- the one the throttle's and the capacity receiver's rows carry, so
// that an audit filtered by actor finds all of them -- with the same row an
// operator's action leaves.
func (c *Controller) systemAudit(ctx context.Context, action, targetKind, targetID string, before, after any) {
	_ = auth.NewAuditor(c.st, c.bus, c.log).Record(ctx, auth.SystemIdentity(), action, targetKind, targetID, before, after)
}
