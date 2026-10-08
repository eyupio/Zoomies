package agent

import (
	"context"
	"errors"
	"time"

	"github.com/eyupio/zoomies/internal/hosttune"
)

// latestDoctor is the periodic report for the heartbeat. A nil Doctor is an
// agent with no monitor (tests, or a build that wires none), not a failure.
func (a *Agent) latestDoctor(ctx context.Context) *hosttune.Report {
	if a.opts.Doctor == nil {
		return nil
	}
	return a.opts.Doctor.Latest(ctx)
}

// canCheckHost says whether this agent can run its OS checks on request.
func (a *Agent) canCheckHost() bool {
	return a.opts.Doctor != nil && a.opts.Doctor.CanCheckNow()
}

// handleCheckHost runs the host checks once and reports exactly one result on
// every path. A task that reported nothing would leave the controller's lease
// on it standing until it expired, and the operator looking at a button that
// had stopped working.
func (a *Agent) handleCheckHost(ctx context.Context, task Task) {
	if ctx.Err() != nil {
		a.reportNotStarted(ctx, task)
		return
	}
	res := TaskResult{TaskID: task.ID, Kind: task.Kind}
	if a.opts.Doctor == nil {
		res.Error = "this agent cannot run its host checks on request"
		res.CompletedAt = a.now()
		a.report(ctx, res)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, hosttune.MonitorRunTimeout)
	defer cancel()
	started := a.now()
	rep, err := a.opts.Doctor.CheckNow(cctx)
	switch {
	case errors.Is(err, hosttune.ErrCheckNowUnsupported), err == nil && rep == nil:
		res.Error = "this agent cannot run its host checks on request"
	case err != nil && ctx.Err() != nil:
		// The agent is shutting down, not the checks failing: give the task
		// back so the controller may offer it again.
		a.reportNotStarted(ctx, task)
		return
	case err != nil:
		res.Error = err.Error()
	default:
		res.OK = true
		res.Doctor = rep
	}
	res.CompletedAt = a.now()
	a.log.Info("ran the host checks on request", "task", task.ID, "ok", res.OK, "took", a.now().Sub(started).Round(time.Millisecond))
	a.report(ctx, res)
}
