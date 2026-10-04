package controller

import (
	"context"

	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// stampJobVersions records which build handled a job, once each, so that jobs
// can be compared across releases.
//
// It runs from recordJobChange, which every path that changes a job passes
// through, rather than from the webhook handler alone: the poller, the
// recovery sweep and the agent's own reports all move jobs, and a stamp that
// only one of them wrote would leave whichever saw the job first unlabelled.
//
// The controller's pair is stamped once something here has a claim on the job
// -- a pool matched it or a runner took it -- and never for a job nothing
// here touched, so a hosted job stays "unknown" instead of being credited to
// a release that never saw it. The host's pair waits for the runner, and is
// read from the host row as its last heartbeat left it. The store writes each
// column only while it is NULL, so calling this on every change is safe; the
// checks here only keep it from writing when there is nothing to add.
//
// j is updated in place, because the callers publish it next and the event
// frame is the job's GET shape.
func (c *Controller) stampJobVersions(ctx context.Context, j *store.Job, runner *store.Runner) {
	if j == nil || IsDemoID(j.InstallationID) {
		return
	}
	claimed := j.Matched || j.RunnerID != ""
	var v store.JobVersions
	if claimed && j.ControllerVersion == "" {
		v.ControllerVersion, v.ControllerChannel = version.Version, version.Channel(version.Version)
	}
	if j.RunnerID != "" && (j.HostID == "" || j.AgentVersion == "") {
		if runner == nil || runner.ID != j.RunnerID {
			if r, err := c.st.GetRunner(ctx, j.RunnerID); err == nil {
				runner = r
			} else {
				runner = nil
			}
		}
		if runner != nil && runner.HostID != "" {
			v.HostID = runner.HostID
			if h, err := c.st.GetHost(ctx, runner.HostID); err == nil {
				v.AgentVersion = h.Version
			}
		}
	}
	if v == (store.JobVersions{}) {
		return
	}
	stamped, err := c.st.StampJobVersions(ctx, j.ID, v)
	if err != nil {
		// Not worth failing the delivery over: the job is recorded, and the
		// next change to it tries again.
		c.log.Warn("could not stamp a job with its release", "job", j.ID, "error", err)
		return
	}
	j.ControllerVersion, j.ControllerChannel = stamped.ControllerVersion, stamped.ControllerChannel
	j.AgentVersion, j.HostID = stamped.AgentVersion, stamped.HostID
}

// stampJobGranted records the size of the runner a job ran on, once, so the job
// can say how big its machine was after the runner row is gone and a size can
// be compared across jobs: what was granted, and where that figure came from.
//
// It is read from the runner row, which is the only record of what a runner
// was created with -- recomputing it from the pool and the host would answer
// with today's profile rather than the one the job ran under. It runs from
// recordJobChange beside stampJobVersions for the same reason, and the store
// writes it only while it is still empty, so calling it on every change is
// safe; the checks here only keep it from reading when there is nothing to add.
//
// A runner created with no limits, or one from before allocations were
// recorded, has no source and so nothing to stamp: the job stays "not
// recorded" rather than claiming a size nobody applied.
//
// j is updated in place, because the callers publish it next and the event
// frame is the job's GET shape.
func (c *Controller) stampJobGranted(ctx context.Context, j *store.Job, runner *store.Runner) {
	if j == nil || j.RunnerID == "" || j.GrantedSource != "" || IsDemoID(j.InstallationID) {
		return
	}
	if runner == nil || runner.ID != j.RunnerID {
		r, err := c.st.GetRunner(ctx, j.RunnerID)
		if err != nil {
			return
		}
		runner = r
	}
	if runner.AllocationSource == "" || (runner.AllocatedCPUs <= 0 && runner.AllocatedMemoryMB <= 0) {
		return
	}
	var pool *store.Pool
	if p, err := c.st.GetPool(ctx, runner.PoolID); err == nil {
		pool = p
	}
	cpus, memoryMB := grantedSize(pool, runner)
	stamped, err := c.st.StampJobGranted(ctx, j.ID, cpus, memoryMB, runner.AllocationSource)
	if err != nil {
		// Not worth failing the delivery over: the next change to the job
		// tries again.
		c.log.Warn("could not stamp a job with the size it was granted", "job", j.ID, "error", err)
		return
	}
	j.GrantedCPUs, j.GrantedMemoryMB, j.GrantedSource = stamped.GrantedCPUs, stamped.GrantedMemoryMB, stamped.GrantedSource
}

// grantedSize is the guaranteed size of the machine a runner gave its job: what
// its row says it was created with, and for a docker-in-docker runner with
// limits somebody typed, twice that, because the daemon is given the same
// figure as the runner and the job's work is in both. A size taken from the
// host -- a share or a profile's standard -- is already the whole slot, split
// between the pair, so it is not doubled. CPU lent to the runner later is not
// part of it: this is the guarantee, which is what two jobs can be compared by.
func grantedSize(p *store.Pool, r *store.Runner) (cpus float64, memoryMB int64) {
	cpus, memoryMB = r.AllocatedCPUs, r.AllocatedMemoryMB
	if p == nil || p.DockerMode != store.DockerDinD {
		return cpus, memoryMB
	}
	if typedAllocation(r, p.Resources.CPUs > 0) {
		cpus *= 2
	}
	if typedAllocation(r, p.Resources.MemoryMB > 0) {
		memoryMB *= 2
	}
	return cpus, memoryMB
}

// typedAllocation reports whether one field of a runner's allocation is a figure
// somebody typed, which a docker-in-docker pair gives to each of its containers,
// rather than one slot's worth the pair splits. A reduced or history runner
// keeps the nature of the pool's field: it is smaller or larger than the typed
// size, and still typed.
func typedAllocation(r *store.Runner, fieldTyped bool) bool {
	return r.AllocationSource == store.AllocationFromPool ||
		((r.AllocationSource == store.AllocationReduced || r.AllocationSource == store.AllocationHistory) && fieldTyped)
}
