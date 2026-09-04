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
		rctx, cancel := context.WithTimeout(ctx, reportTimeout)
		err = a.tr.ReportRunners(rctx, reports)
		cancel()
		if err != nil {
			a.log.Warn("could not report runner observations", "runners", len(reports), "error", err)
		}
	}
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

	kinds := a.opts.Backends.Kinds()
	slices.Sort(kinds)
	for _, kind := range kinds {
		b, err := a.opts.Backends.Get(kind)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		workloads, err := b.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("agent: listing %s workloads on this host: %w", kind, err))
			continue
		}

		for _, w := range workloads {
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
		if seen[r.runnerID] || r.terminal {
			continue
		}
		if now.Sub(r.createdAt) < missingGrace {
			// A workload created moments ago may not be listed yet; declaring
			// it gone would fail a runner that is starting perfectly well.
			continue
		}
		msg := fmt.Sprintf("workload %s is no longer on host %s", r.handle, a.opts.Name)
		a.markTerminal(r.runnerID, store.RunnerRemoved, backend.PhaseGone, 0, msg, now)
		a.log.Info("runner workload disappeared", "runner", r.runnerID, "handle", r.handle, "backend", r.kind)
		reports = append(reports, RunnerReport{
			RunnerID:   r.runnerID,
			State:      store.RunnerRemoved,
			Handle:     r.handle,
			Phase:      backend.PhaseGone,
			Message:    msg,
			ObservedAt: now,
		})
		a.untrack(r.runnerID)
	}

	return reports, errors.Join(errs...)
}

// observe turns one live workload into a report, and decides the fate of one
// that has stopped.
func (a *Agent) observe(ctx context.Context, b backend.Backend, r tracked, w backend.Workload, now time.Time) (RunnerReport, bool) {
	switch w.Status.Phase {
	case backend.PhaseExited, backend.PhaseFailed, backend.PhaseGone:
		if r.terminal {
			return RunnerReport{}, false
		}
		state, msg := terminalOutcome(r, w.Status)
		a.markTerminal(r.runnerID, state, w.Status.Phase, w.Status.ExitCode, msg, now)
		a.log.Info("runner reached the end of its life",
			"runner", r.runnerID, "handle", w.Handle, "state", state, "exit_code", w.Status.ExitCode, "detail", msg)
		return RunnerReport{
			RunnerID:   r.runnerID,
			State:      state,
			Handle:     w.Handle,
			Phase:      w.Status.Phase,
			ExitCode:   w.Status.ExitCode,
			Message:    msg,
			ObservedAt: now,
		}, true

	default:
		stats, err := b.Stats(ctx, w.Handle)
		if err != nil && !errors.Is(err, backend.ErrNotFound) {
			// Stats are best effort: a backend that cannot measure is not a
			// reason to stop reporting that the runner is alive.
			a.log.Debug("could not sample runner stats", "runner", r.runnerID, "handle", w.Handle, "error", err)
		}
		a.markRunning(r.runnerID, w, stats, now)
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
func terminalOutcome(r tracked, s backend.Status) (store.RunnerState, string) {
	switch {
	case s.ExitCode == 0:
		if r.ephemeral {
			return store.RunnerRemoved, "ephemeral runner exited cleanly after its job"
		}
		return store.RunnerRemoved, "runner exited cleanly"
	case r.stopping:
		// Zoomies asked this workload to stop, so the non-zero code is the
		// backend's kill, not the job's.
		return store.RunnerRemoved, fmt.Sprintf("runner was stopped by the controller and exited with code %d", s.ExitCode)
	default:
		msg := fmt.Sprintf("runner exited with code %d", s.ExitCode)
		if s.Message != "" {
			msg += ": " + s.Message
		}
		return store.RunnerFailed, msg
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
	if !a.polled.Load() {
		return RunnerReport{}, false
	}
	first := a.markOrphan(w.Handle, now)
	if now.Sub(first) < orphanGrace {
		return RunnerReport{}, false
	}

	if err := b.Remove(ctx, w.Handle); err != nil && !errors.Is(err, backend.ErrNotFound) {
		// Keep the orphan record so the next pass tries again rather than
		// restarting the grace period.
		a.log.Warn("could not remove an orphaned runner workload; it is still consuming capacity on this host",
			"backend", kind, "handle", w.Handle, "name", w.Name, "error", err)
		return RunnerReport{}, false
	}
	a.forgetOrphan(w.Handle)
	a.log.Warn("removed an orphaned runner workload left behind by an earlier agent",
		"backend", kind, "handle", w.Handle, "name", w.Name, "runner", w.RunnerID, "unclaimed_for", now.Sub(first))

	if w.RunnerID == "" {
		// Nothing to report: the workload carried no runner ID, so the
		// controller has no row to move.
		return RunnerReport{}, false
	}
	return RunnerReport{
		RunnerID:   w.RunnerID,
		State:      store.RunnerRemoved,
		Handle:     w.Handle,
		Phase:      backend.PhaseGone,
		Message:    fmt.Sprintf("removed orphaned %s workload %s that no task claimed", kind, w.Name),
		ObservedAt: now,
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
	return *r, true
}

func (a *Agent) trackedRunners() []tracked {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]tracked, 0, len(a.runners))
	for _, r := range a.runners {
		out = append(out, *r)
	}
	return out
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
	r.stats = stats
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

func (a *Agent) markTerminal(runnerID string, state store.RunnerState, phase backend.Phase, exitCode int, msg string, now time.Time) {
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
	r.observedAt = now
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
