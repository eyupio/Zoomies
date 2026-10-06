package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// reconcileLoop keeps the host and the agent's beliefs about it in step.
func (a *Agent) reconcileLoop(ctx context.Context) error {
	ticker := time.NewTicker(defaultReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		reports, err := a.ReconcileOnce(ctx)
		if err != nil {
			// A backend that cannot be listed is worth saying out loud, but it
			// is not fatal: the other backends still work, and the daemon may
			// simply be restarting.
			a.log.Warn("reconcile did not complete", "error", err)
		}
		if len(reports) == 0 {
			continue
		}
		a.sendReports(ctx, reports)
	}
}

// sendReports delivers one pass's observations and, once the controller has
// accepted them, remembers which finished runners it now knows about.
func (a *Agent) sendReports(ctx context.Context, reports []RunnerReport) {
	rctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	if err := a.tr.ReportRunners(rctx, reports); err != nil {
		a.log.Warn("could not report runner observations", "runners", len(reports), "error", err)
		return
	}
	a.markReported(reports)
}

// ReconcileOnce compares every backend's workloads against what the agent
// believes and returns the observations worth sending to the controller.
//
// It is exported so the behaviour that deletes things can be tested directly
// rather than through a ticker.
func (a *Agent) ReconcileOnce(ctx context.Context) ([]RunnerReport, error) {
	now := a.now()
	var reports []RunnerReport
	var errs []error
	seen := make(map[string]bool)

	// A backend that could not be listed says nothing about the runners on
	// it. Treating "not listed" like "not seen" would let a transient daemon
	// error a minute into a runner's life declare it gone, have the controller
	// mark its job lost, and then reap the live container as an orphan once
	// the daemon answered again.
	unlisted := make(map[store.BackendKind]bool)

	kinds := a.opts.Backends.Kinds()
	slices.Sort(kinds)
	for _, kind := range kinds {
		b, err := a.opts.Backends.Get(kind)
		if err != nil {
			errs = append(errs, err)
			unlisted[kind] = true
			continue
		}
		workloads, err := b.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agent: listing %s workloads on this host: %w", kind, err))
			unlisted[kind] = true
			continue
		}

		a.mu.Lock()
		pending := a.inventoryPending[kind]
		delete(a.inventoryPending, kind)
		a.mu.Unlock()
		if pending {
			for _, w := range workloads {
				if w.RunnerID != "" && !w.Sidecar {
					a.adopt(w.RunnerID, kind, w)
				}
			}
		}
		for _, w := range workloads {
			if w.Sidecar {
				// A sidecar carries its runner's id, so it must never be
				// looked up as one. The backend only lists it once that
				// runner has gone, and what is left is a privileged daemon
				// nobody is using. What that is worth reporting is reapOrphan's
				// decision, not this loop's.
				if rep, ok := a.reapOrphan(ctx, b, kind, w, now); ok {
					reports = append(reports, rep)
				}
				continue
			}
			r, tracked := a.snapshot(w.RunnerID)
			if !tracked {
				if rep, ok := a.reapOrphan(ctx, b, kind, w, now); ok {
					reports = append(reports, rep)
				}
				continue
			}
			seen[w.RunnerID] = true
			a.forgetOrphan(w.Handle)
			if rep, ok := a.observe(ctx, b, r, w, now); ok {
				reports = append(reports, rep)
			}
		}
	}

	// A runner the agent tracks whose workload appears nowhere has been removed
	// behind the agent's back -- by an operator with docker rm, or by a daemon
	// restart with cleanup.
	for _, r := range a.trackedRunners() {
		if r.hostRemoved {
			// Delivery of a completed removal does not depend on the backend
			// still answering, or on the startup grace for missing workloads.
			if !seen[r.runnerID] {
				reports = append(reports, r.report())
			}
			continue
		}
		if seen[r.runnerID] || unlisted[r.kind] {
			continue
		}
		if now.Sub(r.createdAt) < missingGrace {
			// A workload created moments ago may not be listed yet; declaring
			// it gone would fail a runner that is starting perfectly well.
			continue
		}
		if r.terminal {
			if r.reported {
				a.untrack(r.runnerID)
			} else {
				reports = append(reports, r.report())
			}
			continue
		}
		msg := fmt.Sprintf("workload %s is no longer on host %s", r.handle, a.opts.Name)
		a.markTerminal(r.runnerID, store.RunnerRemoved, backend.PhaseGone, 0, msg, "", now)
		a.log.Info("runner workload disappeared", "runner", r.runnerID, "handle", r.handle, "backend", r.kind)
		reports = append(reports, RunnerReport{
			RunnerID:   r.runnerID,
			State:      store.RunnerRemoved,
			Handle:     r.handle,
			Phase:      backend.PhaseGone,
			Message:    msg,
			ObservedAt: now,
		})
	}

	return reports, errors.Join(errs...)
}

// observe turns one live workload into a report, and decides the fate of one
// that has stopped.
func (a *Agent) observe(ctx context.Context, b backend.Backend, r tracked, w backend.Workload, now time.Time) (RunnerReport, bool) {
	if r.hostRemoved {
		return r.report(), true
	}
	switch w.Status.Phase {
	case backend.PhaseExited, backend.PhaseExitUnknown, backend.PhaseFailed, backend.PhaseGone:
		if r.terminal {
			// Its end of life has been reported; what is left is the workload
			// itself, and it is this agent's job to get rid of it.
			return a.cleanUp(ctx, b, r, w, now)
		}
		state, msg, fault := terminalOutcome(r, w.Status)
		a.markTerminal(r.runnerID, state, w.Status.Phase, w.Status.ExitCode, msg, fault, now)
		a.log.Info("runner reached the end of its life",
			"runner", r.runnerID, "handle", w.Handle, "state", state, "exit_code", w.Status.ExitCode, "detail", msg)
		return RunnerReport{
			RunnerID:   r.runnerID,
			State:      state,
			Handle:     w.Handle,
			Phase:      w.Status.Phase,
			ExitCode:   w.Status.ExitCode,
			Fault:      fault,
			Message:    msg,
			ObservedAt: now,
		}, true

	default:
		// Resource sampling runs separately, so a slow stats endpoint cannot
		// delay noticing exits or returning lifecycle observations.
		stats := r.stats
		a.markRunning(r.runnerID, w, stats, now)
		// The valve's word travels with every report of a live runner. The
		// controller replaces a runner's whole sample with the latest report, so a
		// report that left it out would blank the runner's memory facts until the
		// next heartbeat put them back, and count a near-limit runner twice.
		stats.MemoryValve = r.valveSample
		// No lifecycle state is claimed for a live runner. Whether it is idle
		// or busy is GitHub's answer, not the host's, and guessing here would
		// fight the controller's state machine.
		return RunnerReport{
			RunnerID:   r.runnerID,
			Handle:     w.Handle,
			Phase:      w.Status.Phase,
			Stats:      stats,
			ObservedAt: now,
		}, true
	}
}

// terminalOutcome works out whether a stopped workload finished or failed.
//
// An ephemeral runner exiting after one job is the normal, successful end of
// life and by far the commonest event in the system, so it must never be
// reported as a failure -- an operator who sees "failed" for every completed
// job stops reading the fleet's health at all.
func terminalOutcome(r tracked, s backend.Status) (store.RunnerState, string, store.FaultKind) {
	switch {
	case s.Phase == backend.PhaseExitUnknown:
		// Removed describes the runner's lifecycle, not its job's outcome.
		// GitHub remains authoritative about whether the job succeeded.
		return store.RunnerRemoved, "runner process exited; its exit status is unknown after an agent restart; check the job's GitHub outcome", ""
	case s.ExitCode == 0 && s.OOMKilled:
		// The runner finished, and the daemon says the kernel killed
		// something in it for memory on the way: a step's process, which the
		// job then failed on like any test. The lifecycle is a clean end; the
		// fault is what lets the controller put it on the job.
		if s.SidecarOOMKilled {
			// The build ran in the pool's Docker sidecar, which has a limit of
			// its own, and its message says which container and what moves it.
			return store.RunnerRemoved, "runner exited after its job, but " + s.Message, store.FaultOutOfMemory
		}
		return store.RunnerRemoved, "runner exited after its job, but the kernel killed a process in it for its memory limit", store.FaultOutOfMemory
	case s.ExitCode == 0:
		if r.ephemeral {
			return store.RunnerRemoved, "ephemeral runner exited cleanly after its job", ""
		}
		return store.RunnerRemoved, "runner exited cleanly", ""
	case r.stopping:
		// Zoomies asked this workload to stop, so the non-zero code is the
		// backend's kill, not the job's.
		return store.RunnerRemoved, fmt.Sprintf("runner was stopped by the controller and exited with code %d", s.ExitCode), ""
	case s.ExitCode == 137 && s.OOMReported && !s.OOMKilled:
		// SIGKILL, and the daemon -- asked, and answering -- says the kernel did not do it for
		// memory. Something outside killed the container: docker stop after its grace period,
		// an operator, or a maintenance restart's --kill-running. Calling it an out-of-memory
		// kill put it on the job's history, where it grew the pool's runner by half again and
		// held back advice to lower its minimum for a week, for a job that never ran short.
		return store.RunnerFailed, "runner was killed (exit 137), but the daemon reports no memory kill: it was stopped from outside, by docker stop, an operator or the host", store.FaultRunnerExited
	default:
		msg := fmt.Sprintf("runner exited with code %d", s.ExitCode)
		if s.Message != "" {
			msg += ": " + s.Message
		} else if hint := entrypointExitHint(s.ExitCode); hint != "" {
			msg += ": " + hint
		}
		fault := exitFault(s.ExitCode)
		if s.OOMKilled {
			fault = store.FaultOutOfMemory
		}
		return store.RunnerFailed, msg, fault
	}
}

// exitFault categorises a runner's exit code.
//
// The codes carry real information and this is the only place holding it: 137
// is the kernel's OOM killer via the daemon, and the entrypoint's reserved
// sysexits codes each name a reason the runner never took a job at all. The
// controller sees only the sentence these produce, by which point the code is
// gone.
//
// Anything else is FaultRunnerExited on purpose. A code we have no meaning for
// is a runner that stopped for a reason nobody here observed, and inventing a
// category would send an operator to fix something that is not broken.
func exitFault(code int) store.FaultKind {
	switch code {
	case 137:
		// 128+SIGKILL. The commonest cause by a distance is the memory limit, unless the
		// daemon said it was not (terminalOutcome asks it first);
		// the message carries the daemon's own words where it had any, so an
		// operator reading "out of memory" against a kill that was not one
		// still has the sentence that says so.
		return store.FaultOutOfMemory
	case 7:
		// actions/runner refuses jobs when its version is no longer supported.
		return store.FaultConfig
	case 64:
		// EX_USAGE: started with no credentials, so it could not register.
		return store.FaultRegistration
	case 69:
		// EX_UNAVAILABLE: the pool's Docker daemon never became ready.
		return store.FaultBackend
	case 78:
		// EX_CONFIG: the entrypoint refused its configuration.
		return store.FaultConfig
	}
	return store.FaultRunnerExited
}

// entrypointExitHint says what a runner image's entrypoint meant by an exit
// code of its own. The codes are sysexits.h values the entrypoint reserves --
// separate from actions/runner exit codes -- and each one is a runner that never
// took a job, so the container's log is short and says exactly what was
// wrong. A bare "exited with code 69" sent operators to GitHub's job log
// instead, where there is nothing, because the runner never registered.
func entrypointExitHint(code int) string {
	switch code {
	case 7:
		return "GitHub rejected the runner version as outdated; update the pool's runner image or process runner version, and check its pull policy before retrying"
	case 64:
		return "the image was started with no credentials, so the entrypoint could not register a runner; read the container's log"
	case 69:
		return "the pool's Docker daemon never became ready, so the entrypoint refused to take a job it could not run; " +
			"read the container's log, and on a slow host raise runners.docker_wait on the Settings page to allow the daemon longer"
	case 78:
		return "the entrypoint refused its configuration; read the container's log for the setting it named"
	}
	return ""
}

// cleanUp deletes a finished runner's workload from the host once nothing needs
// it any more: the controller has been told how the runner ended, and the
// window an operator gets to read its output has passed.
//
// Nothing else does this. A clean exit is the normal end of an ephemeral
// runner's life, and the controller marks the row removed and moves on -- it
// has no reason to send a task for a runner it already considers gone. Left to
// that, every finished job would leave its container on the host for ever:
// the writable layer with the checkout in it, the runner's own log, and a
// docker-in-docker sidecar that is still running. A host that has been busy
// for a week would be a host with no disk left.
//
// The report gate is what keeps the exit code safe. A workload deleted before
// the controller has heard how it ended takes the code with it, and the row it
// belongs to would sit in idle or busy until the reconciler declared it
// vanished, with nothing to say why.
func (a *Agent) cleanUp(ctx context.Context, b backend.Backend, r tracked, w backend.Workload, now time.Time) (RunnerReport, bool) {
	if a.mutationsPaused.Load() {
		return RunnerReport{}, false
	}
	if !r.reported || now.Sub(r.terminalAt) < a.retention {
		return RunnerReport{}, false
	}
	if ctx.Err() != nil {
		// The agent is shutting down. A removal started now would hold the
		// shutdown for as long as the daemon takes; the workload will still
		// be there for the next agent to find.
		return RunnerReport{}, false
	}
	// A stop or remove task for this runner may be in flight. The task owns
	// the workload until it reports, and two removals racing over one
	// container is exactly what claim exists to prevent.
	if !a.claim(r.runnerID) {
		return RunnerReport{}, false
	}
	defer a.release(r.runnerID)

	// Shutdown must not cut a removal in half; the backend converges on a
	// retry, but a sidecar left behind by a cancelled context is a privileged
	// container running for nobody until the next pass.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RemoveTimeout)
	defer cancel()
	if err := removeRunnerWorkload(rctx, b, r.runnerID, w.Handle); err != nil {
		// A removal the daemon is still carrying out is not reported until
		// it has had removalSettle to finish; the next pass asks again.
		since, pending := a.removalPending(r.runnerID, err, now)
		if pending {
			a.log.Info("the daemon is still removing a finished runner's workload; checking again on the next pass",
				"runner", r.runnerID, "handle", w.Handle, "backend", r.kind, "removing_for", now.Sub(since), "error", err)
			return RunnerReport{}, false
		}
		if !since.IsZero() {
			err = stuckRemoval(now.Sub(since), err)
		}
		a.log.Warn("could not remove a finished runner's workload; it is still taking up disk on this host and will be retried",
			"runner", r.runnerID, "handle", w.Handle, "backend", r.kind, "error", err)
		return RunnerReport{RunnerID: r.runnerID, State: r.state, CleanupError: err.Error(), ObservedAt: now}, true
	}
	a.removalPending(r.runnerID, nil, now)
	a.mu.Lock()
	if tracked := a.runners[r.runnerID]; tracked != nil {
		tracked.hostRemoved = true
		tracked.phase = backend.PhaseGone
		tracked.observedAt = now
	}
	a.mu.Unlock()
	a.log.Info("removed a finished runner's workload from this host",
		"runner", r.runnerID, "name", r.name, "handle", w.Handle, "backend", r.kind,
		"state", r.state, "kept_for", now.Sub(r.terminalAt))
	return RunnerReport{RunnerID: r.runnerID, State: r.state, Phase: backend.PhaseGone, HostRemoved: true, ObservedAt: now}, true
}

// A missing parent container does not establish that its DinD sidecar is
// gone. Verify the managed inventory and remove only companions of this
// runner before reporting a positive host confirmation.
func removeRunnerWorkload(ctx context.Context, b backend.Backend, runnerID string, handle backend.Handle) error {
	if handle != "" {
		if err := b.Remove(ctx, handle); err != nil && !errors.Is(err, backend.ErrNotFound) {
			return err
		}
	}
	workloads, err := b.List(ctx)
	if err != nil {
		return fmt.Errorf("confirming removal of %s: %w", runnerID, err)
	}
	for _, w := range workloads {
		if w.RunnerID != runnerID {
			continue
		}
		if !w.Sidecar {
			return fmt.Errorf("runner workload %s is still present after removal", w.Handle)
		}
		if err := b.Remove(ctx, w.Handle); err != nil && !errors.Is(err, backend.ErrNotFound) {
			return fmt.Errorf("removing companion %s: %w", w.Handle, err)
		}
	}
	return nil
}

// markReported records that the controller has accepted a report carrying
// these runners' end of life, which is what makes their workloads eligible for
// removal. Only a terminal report counts: a live runner's report says nothing
// about how it ended, and a runner that finished after the report was
// assembled has not been reported yet.
func (a *Agent) markReported(reports []RunnerReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, rep := range reports {
		if !rep.State.Terminal() {
			continue
		}
		if r, ok := a.runners[rep.RunnerID]; ok && r.terminal && r.state == rep.State {
			r.reported = true
			if rep.HostRemoved && r.hostRemoved {
				delete(a.runners, rep.RunnerID)
			}
		}
	}
}

// reapOrphan removes a managed workload that nothing claims, reporting whether
// it produced an observation worth sending.
//
// Two guards, because the failure mode of getting this wrong is deleting a
// working fleet:
//
//  1. the agent must have completed at least one task poll since start, so a
//     controller that is down (or a task queue that has not been read yet) can
//     never be mistaken for "nobody owns these runners";
//  2. the workload must have gone unclaimed for orphanGrace, so a create whose
//     task result is still in flight is not reaped out from under the
//     controller.
//
// Backend.List only returns workloads carrying the io.zoomies.managed label, so
// nothing an operator started by hand is ever a candidate.
func (a *Agent) reapOrphan(ctx context.Context, b backend.Backend, kind store.BackendKind, w backend.Workload, now time.Time) (RunnerReport, bool) {
	if a.mutationsPaused.Load() {
		return RunnerReport{}, false
	}
	if !a.polled.Load() {
		return RunnerReport{}, false
	}
	// A slow create is not tracked until it returns. In particular, its DinD
	// sidecar can appear long before the runner. Never race a lifecycle task
	// over those partially created resources.
	if w.RunnerID != "" {
		if !a.claim(w.RunnerID) {
			return RunnerReport{}, false
		}
		defer a.release(w.RunnerID)
	}
	first := a.markOrphan(w.Handle, now)
	if now.Sub(first) < orphanGrace {
		return RunnerReport{}, false
	}

	if ctx.Err() != nil {
		// The agent is shutting down, and a removal started now would hold
		// the shutdown for as long as the daemon takes. The orphan will still
		// be there for the next agent to find.
		return RunnerReport{}, false
	}
	// The budget cleanUp gives a removal, and for the same reasons: a
	// sidecar's DELETE is held until the daemon has finished it, and a
	// shutdown must not cut it in half.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RemoveTimeout)
	defer cancel()
	var err error
	if w.Sidecar || w.RunnerID == "" {
		err = b.Remove(rctx, w.Handle)
	} else {
		err = removeRunnerWorkload(rctx, b, w.RunnerID, w.Handle)
	}
	// The window is kept per runner, so one a remove task opened carries on
	// here once the heartbeat has released the runner; a workload with no
	// runner ID is kept by its handle.
	key := w.RunnerID
	if key == "" {
		key = "handle:" + string(w.Handle)
	}
	since, pending := a.removalPending(key, err, now)
	if pending {
		// Not a failure: the daemon is finishing a removal it has started.
		// The orphan record stays, so the next pass asks again.
		a.log.Info("the daemon is still removing an orphaned runner workload; checking again on the next pass",
			"backend", kind, "handle", w.Handle, "name", w.Name, "removing_for", now.Sub(since), "error", err)
		return RunnerReport{}, false
	}
	if !since.IsZero() {
		// Stuck, and this is the only place left that will say so. A runner
		// the controller removed while it was still up is released here by
		// the heartbeat, and the remove task that found its sidecar still
		// going answered pending, which recorded nothing on its row -- so
		// nothing on the controller will ever ask about it again.
		err = stuckRemoval(now.Sub(since), err)
		a.log.Warn("could not remove an orphaned runner workload; it is still consuming capacity on this host",
			"backend", kind, "handle", w.Handle, "name", w.Name, "error", err)
		if w.RunnerID == "" {
			return RunnerReport{}, false
		}
		// No state: the row is already terminal, and a cleanup error on a
		// terminal row is recorded without moving it -- which is also what
		// makes the controller send the remove again.
		return RunnerReport{RunnerID: w.RunnerID, CleanupError: err.Error(), ObservedAt: now}, true
	}
	if err != nil && !errors.Is(err, backend.ErrNotFound) {
		// Keep the orphan record so the next pass tries again rather than
		// restarting the grace period.
		a.log.Warn("could not remove an orphaned runner workload; it is still consuming capacity on this host",
			"backend", kind, "handle", w.Handle, "name", w.Name, "error", err)
		return RunnerReport{}, false
	}
	a.forgetOrphan(w.Handle)
	what := "runner workload"
	if w.Sidecar {
		what = "docker-in-docker sidecar"
	}
	a.log.Warn("removed an orphaned "+what+" left behind by an earlier agent",
		"backend", kind, "handle", w.Handle, "name", w.Name, "runner", w.RunnerID, "unclaimed_for", now.Sub(first))

	if w.RunnerID == "" {
		// Nothing to report: the workload carried no runner ID, so the
		// controller has no row to move.
		return RunnerReport{}, false
	}
	if w.Sidecar {
		// The sidecar's removal is not the runner's. Its runner container has
		// its own way of being declared gone, and reporting this one as the
		// runner would move a row on the strength of the wrong container.
		return RunnerReport{}, false
	}
	return RunnerReport{
		RunnerID:    w.RunnerID,
		State:       store.RunnerRemoved,
		Handle:      w.Handle,
		Phase:       backend.PhaseGone,
		HostRemoved: true,
		Message:     fmt.Sprintf("removed orphaned %s workload %s that no task claimed", kind, w.Name),
		ObservedAt:  now,
	}, true
}

// snapshot returns a copy of what the agent believes about one runner, so
// reconciliation can do I/O without holding the lock.
func (a *Agent) snapshot(runnerID string) (tracked, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.runners[runnerID]
	if !ok {
		return tracked{}, false
	}
	return r.copyForReport(), true
}

func (a *Agent) trackedRunners() []tracked {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]tracked, 0, len(a.runners))
	for _, r := range a.runners {
		out = append(out, r.copyForReport())
	}
	return out
}

// copyForReport is a copy of one runner that is safe to read after the lock is
// let go: what the memory valve says of it is taken now, while the lock is
// held, and the valve itself is not carried.
func (t *tracked) copyForReport() tracked {
	cp := *t
	cp.valveSample = t.valve.sample()
	cp.valve = nil
	return cp
}

func (a *Agent) markRunning(runnerID string, w backend.Workload, stats backend.Stats, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.runners[runnerID]
	if !ok {
		return
	}
	r.handle = w.Handle
	r.phase = w.Status.Phase
	// The stats loop owns r.stats; this observation may carry an older copy.
	r.observedAt = now
	if r.name == "" {
		r.name = w.Name
	}
	if r.phase == backend.PhaseRunning && r.state == store.RunnerRegistering {
		// The workload is up; whether GitHub has given it a job is not this
		// agent's call, so stop asserting a state at all from here on.
		r.state = ""
	}
}

func (a *Agent) markTerminal(runnerID string, state store.RunnerState, phase backend.Phase, exitCode int, msg string, fault store.FaultKind, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.runners[runnerID]
	if !ok {
		return
	}
	r.state = state
	r.phase = phase
	r.exitCode = exitCode
	r.message = msg
	r.fault = fault
	r.observedAt = now
	if !r.terminal {
		r.terminalAt = now
	}
	r.terminal = true
}

// markOrphan records when an unclaimed workload was first seen and returns that
// time, which is how the grace period is measured.
func (a *Agent) markOrphan(h backend.Handle, now time.Time) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if first, ok := a.orphans[h]; ok {
		return first
	}
	a.orphans[h] = now
	return now
}

func (a *Agent) forgetOrphan(h backend.Handle) {
	a.mu.Lock()
	delete(a.orphans, h)
	a.mu.Unlock()
}
