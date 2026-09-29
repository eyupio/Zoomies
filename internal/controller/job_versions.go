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
