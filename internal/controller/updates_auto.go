package controller

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

// updateHistoryRead is how many of the newest attempts the planner reads for a
// machine's failures. Two failures of one release on one machine end the
// retrying, and an attempt is at least half an hour after the last, so the
// failures that matter are always among the newest; an open attempt is read
// separately, however old.
const updateHistoryRead = 500

// autoUpdateActor is the planner as an attempt, a request and a rollout record
// who asked: the auto-update identity's name, which the audit log uses too.
func autoUpdateActor() UpdateActor {
	id := auth.AutoUpdateIdentity()
	return UpdateActor{ID: id.ID, Name: id.Name}
}

var askedByPlanner = updateAsk{trigger: store.UpdateTriggerAuto}

// updatesPicture is the rows a snapshot was made from, kept beside it, so that
// the applier acts on the attempts and the rollout the planner saw and the
// status counts the rollout from the same reading.
type updatesPicture struct {
	snap updates.Snapshot
	// rollout is the open rollout and last the latest that ended; either may be
	// nil.
	rollout, last *store.UpdateRollout
	hosts         []*store.Host
	// attempts is the newest attempts, open and ended, newest first, with every
	// open attempt among them however old.
	attempts []store.UpdateAttempt
}

// updatesSnapshot reads everything the planner decides on. It decides
// nothing itself: the rows go into the snapshot as they are, and Decide works
// out from them which failures count, so the rule that halts a rollout is one
// a table of pure tests can try.
//
// The helper is the probe's answer, passed in because the status has just made
// the same probe and a second would read the folder twice.
func (c *Controller) updatesSnapshot(ctx context.Context, helper helperProbe) (*updatesPicture, error) {
	cfg := c.cfg().Updates
	pic := &updatesPicture{}
	s := updates.Snapshot{
		Now: c.Now(), Mode: updateModeOf(cfg.Mode), Soak: cfg.Soak, Running: version.Version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Fenced: !c.mayAct(),
	}
	if state := c.latestRelease(); state != nil {
		s.Releases = state.Releases
	}
	switch {
	case helper.ready:
		s.Helper = updates.HelperStateReady
	case helper.view.State == HelperUnsupported:
		s.Helper = updates.HelperStateUnsupported
		s.HelperWhyNot = helperUnsupportedWhy(updates.HelperSupport(c.helperHostFacts()), "this controller")
	default:
		s.Helper = updates.HelperStateMissing
	}

	var err error
	if pic.rollout, err = c.st.OpenUpdateRollout(ctx); errors.Is(err, store.ErrNotFound) {
		pic.rollout = nil
	} else if err != nil {
		return nil, fmt.Errorf("reading the open rollout: %w", err)
	}
	if pic.last, err = c.st.LastEndedUpdateRollout(ctx); errors.Is(err, store.ErrNotFound) {
		pic.last = nil
	} else if err != nil {
		return nil, fmt.Errorf("reading the last rollout: %w", err)
	}
	s.Rollout, s.LastRollout = rolloutFacts(pic.rollout), rolloutFacts(pic.last)

	recent, err := c.st.ListUpdateAttempts(ctx, "", "", updateHistoryRead)
	if err != nil {
		return nil, fmt.Errorf("reading the update attempts: %w", err)
	}
	open, err := c.st.OpenUpdateAttempts(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the open update attempts: %w", err)
	}
	pic.attempts = recent
	for _, a := range open {
		if !slices.ContainsFunc(recent, func(r store.UpdateAttempt) bool { return r.ID == a.ID }) {
			pic.attempts = append(pic.attempts, a)
		}
	}

	if pic.hosts, err = c.st.ListHosts(ctx); err != nil {
		return nil, fmt.Errorf("listing the hosts: %w", err)
	}
	byHost := make(map[string]*updates.HostFacts, len(pic.hosts))
	target := hostTarget()
	for _, h := range pic.hosts {
		can, why, _ := hostCanSelfUpdate(h, target, c.hostHelperUnsupported(h), c.updateMode())
		s.Hosts = append(s.Hosts, updates.HostFacts{
			ID: h.ID, Name: h.Name, Version: h.Version, GOOS: h.OS, GOARCH: h.Arch,
			Embedded: h.Embedded, Healthy: h.Healthy(s.Now), CanSelfUpdate: can, WhyNot: why,
			ActiveRunners: h.ActiveRunners,
		})
	}
	for i := range s.Hosts {
		byHost[s.Hosts[i].ID] = &s.Hosts[i]
	}
	for _, a := range pic.attempts {
		facts := attemptFacts(a)
		switch {
		case a.Scope == store.UpdateScopeController && a.State == store.UpdateRequested:
			s.Controller = &facts
		case a.Scope == store.UpdateScopeController:
			s.ControllerEnded = append(s.ControllerEnded, facts)
		case byHost[a.HostID] == nil:
			// A host that is gone; its attempt was cancelled with it.
		case a.State == store.UpdateRequested:
			byHost[a.HostID].Open = &facts
		default:
			byHost[a.HostID].Ended = append(byHost[a.HostID].Ended, facts)
		}
	}
	pic.snap = s
	return pic, nil
}

func rolloutFacts(r *store.UpdateRollout) *updates.Rollout {
	if r == nil {
		return nil
	}
	return &updates.Rollout{
		ID: r.ID, Target: r.Target, Trigger: r.Trigger, State: r.State, HaltedReason: r.HaltedReason,
		HostIDs: slices.Clone([]string(r.HostIDs)), Since: r.ResumedAt, CancelledBy: r.CancelledBy,
	}
}

func attemptFacts(a store.UpdateAttempt) updates.Attempt {
	out := updates.Attempt{
		ID: a.ID, Scope: a.Scope, HostID: a.HostID, To: a.ToVersion, State: a.State,
		RequestedAt: a.RequestedAt, Error: a.Error,
	}
	if a.FinishedAt != nil {
		out.FinishedAt = *a.FinishedAt
	}
	return out
}

// runUpdatePlan asks the planner what to do and does it, and says whether
// anything changed. A snapshot that cannot be read is a pass that does nothing:
// the next one reads it again.
func (c *Controller) runUpdatePlan(ctx context.Context) bool {
	if !c.mayAct() {
		return false
	}
	pic, err := c.updatesSnapshot(ctx, c.probeUpdateHelper())
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("could not read what automatic updating decides on; the next pass will try again", "error", err)
		}
		return false
	}
	return c.applyUpdatePlan(ctx, pic, updates.Decide(pic.snap))
}

// applyUpdatePlan carries out a plan, in order. Each step is the method a
// button calls, with the auto-update identity, so the planner can do nothing a
// person pressing the same button could not, and is refused by the same rules.
//
// The controller writes the audit row itself here, and only here: a person's
// press is audited by the handler that took it.
func (c *Controller) applyUpdatePlan(ctx context.Context, pic *updatesPicture, plan updates.Plan) bool {
	// Checked again, because the snapshot was read a moment ago and a fence or a
	// lost lease since then must stop the plan, not the next one.
	if !c.mayAct() {
		return false
	}
	changed, requested := false, false
	for _, step := range plan.Actions {
		switch step.Kind {
		case updates.ActionTimeOut:
			changed = c.autoTimeOut(ctx, pic, step) || changed
		case updates.ActionRequestController, updates.ActionUpdateHost:
			// The helper's path unit allows five starts in ten minutes, so a pass
			// writes at most one request, whatever a plan holds.
			if requested {
				continue
			}
			requested = true
			if step.Kind == updates.ActionRequestController {
				changed = c.autoRequestController(ctx, step) || changed
			} else {
				changed = c.autoUpdateHost(ctx, pic, step) || changed
			}
		case updates.ActionStartRollout:
			changed = c.autoStartRollout(ctx, step) || changed
		case updates.ActionHalt, updates.ActionFinishRollout, updates.ActionCancelRollout:
			changed = c.autoEndRollout(ctx, pic, step) || changed
		}
	}
	return changed
}

// autoAudit writes the audit row of one of the planner's own steps.
func (c *Controller) autoAudit(ctx context.Context, action, targetID string, detail map[string]any) {
	auth.NewAuditor(c.st, c.bus, c.log).Act(context.WithoutCancel(ctx), auth.AutoUpdateIdentity(), action, "update", targetID, detail)
}

func (c *Controller) autoTimeOut(ctx context.Context, pic *updatesPicture, step updates.Action) bool {
	i := slices.IndexFunc(pic.attempts, func(a store.UpdateAttempt) bool { return a.ID == step.AttemptID })
	if i < 0 || pic.attempts[i].State != store.UpdateRequested {
		return false
	}
	a := pic.attempts[i]
	if !c.endAttempt(ctx, a, store.UpdateTimedOut, timedOutText(a)) {
		return false
	}
	c.autoAudit(ctx, "update.timed_out", a.ID, map[string]any{"scope": a.Scope, "host": a.HostID, "to": a.ToVersion, "reason": step.Reason})
	return true
}

func (c *Controller) autoRequestController(ctx context.Context, step updates.Action) bool {
	_, id, err := c.requestControllerUpdate(ctx, autoUpdateActor(), step.Tag, askedByPlanner)
	if err != nil {
		c.log.Warn("automatic updating could not ask for the controller's update; the next pass will decide again", "to", step.Tag, "error", err)
		return false
	}
	c.autoAudit(ctx, "update.controller_requested", id, map[string]any{"from": version.Version, "to": step.Tag, "reason": step.Reason})
	return true
}

func (c *Controller) autoUpdateHost(ctx context.Context, pic *updatesPicture, step updates.Action) bool {
	if pic.rollout == nil {
		return false
	}
	_, attempt, err := c.requestHostUpdate(ctx, autoUpdateActor(), step.HostID,
		updateAsk{trigger: store.UpdateTriggerAuto, rolloutID: pic.rollout.ID})
	if err != nil {
		c.log.Warn("automatic updating could not ask a host to update; the next pass will decide again",
			"host", step.HostID, "rollout", pic.rollout.ID, "error", err)
		return false
	}
	c.autoAudit(ctx, "update.host_requested", attempt.ID, map[string]any{
		"host": step.HostID, "from": attempt.FromVersion, "to": attempt.ToVersion, "rollout": pic.rollout.ID, "reason": step.Reason,
	})
	return true
}

func (c *Controller) autoStartRollout(ctx context.Context, step updates.Action) bool {
	r := &store.UpdateRollout{Target: step.Tag, Trigger: store.UpdateTriggerAuto, StartedBy: requestedBy(autoUpdateActor())}
	if err := c.st.CreateUpdateRollout(ctx, r); err != nil {
		// A conflict is a person's rollout started since the snapshot, which is
		// the one to carry on.
		if !errors.Is(err, store.ErrConflict) {
			c.log.Warn("automatic updating could not start a rollout; the next pass will decide again", "to", step.Tag, "error", err)
		}
		return false
	}
	c.log.Info("automatic updating started a rollout", "rollout", r.ID, "to", r.Target)
	c.autoAudit(ctx, "update.rollout_started", r.ID, map[string]any{"to": r.Target, "reason": step.Reason})
	// The first host goes on the next pass, which is now rather than in ten
	// seconds.
	c.KickUpdates()
	return true
}

// autoEndRollout halts, finishes or cancels the rollout the planner saw. Each
// move is guarded by the state it is from, so one a person made since the
// snapshot stands, and this one is dropped.
func (c *Controller) autoEndRollout(ctx context.Context, pic *updatesPicture, step updates.Action) bool {
	if pic.rollout == nil {
		return false
	}
	id := pic.rollout.ID
	var moved bool
	var err error
	var action string
	switch step.Kind {
	case updates.ActionHalt:
		action = "update.rollout_halted"
		moved, err = c.st.HaltUpdateRollout(ctx, id, step.Reason)
	case updates.ActionFinishRollout:
		action = "update.rollout_finished"
		moved, err = c.st.FinishUpdateRollout(ctx, id, store.RolloutDone)
	default:
		action = "update.rollout_cancelled"
		moved, err = c.st.FinishUpdateRollout(ctx, id, store.RolloutCancelled)
	}
	if err != nil {
		c.log.Warn("automatic updating could not record where a rollout stands; the next pass will decide again", "rollout", id, "error", err)
		return false
	}
	if !moved {
		return false
	}
	c.log.Info("automatic updating moved a rollout on", "rollout", id, "to", pic.rollout.Target, "action", action, "reason", step.Reason)
	c.autoAudit(ctx, action, id, map[string]any{"to": pic.rollout.Target, "host": step.HostID, "reason": step.Reason})
	return true
}

// StartHostRollout starts a rollout of every host behind this controller's
// release, or of hostIDs, and returns the status with it. The planner moves it
// on from the next pass, one host at a time; nothing is asked of a host here.
//
// by is only written down, as the rollout's started_by. The handler that took
// the request audits it with the caller's identity.
func (c *Controller) StartHostRollout(ctx context.Context, by UpdateActor, hostIDs []string) (*UpdatesView, error) {
	if !c.mayAct() {
		return nil, fmt.Errorf("%w: %s", ErrUpdateFenced, c.notActingReason())
	}
	if c.updateMode() == updates.ModeOff {
		return nil, ErrUpdateModeOff
	}
	target := hostTarget()
	if target == "" {
		return nil, ErrUpdateNotARelease
	}
	if err := c.refuseOverOpenRollout(ctx); err != nil {
		return nil, err
	}
	var hosts []*store.Host
	var ids store.StringSlice
	if len(hostIDs) == 0 {
		list, err := c.st.ListHosts(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the hosts: %w", err)
		}
		hosts = list
	}
	for _, id := range hostIDs {
		if slices.Contains(ids, id) {
			continue
		}
		h, err := c.st.GetHost(ctx, id)
		if err != nil {
			return nil, err
		}
		hosts, ids = append(hosts, h), append(ids, id)
	}
	behind, able := 0, 0
	for _, h := range hosts {
		// The planner's own test: only a release build is behind.
		if _, ok := updates.TargetTag(h.Version); !ok || h.Embedded || version.CompareBuilds(h.Version, target) != version.SkewBehind {
			continue
		}
		behind++
		if can, _, _ := hostCanSelfUpdate(h, target, c.hostHelperUnsupported(h), c.updateMode()); can {
			able++
		}
	}
	switch {
	case behind == 0:
		return nil, fmt.Errorf("%w: no host asked for runs a release behind %s", ErrUpdateNothingNewer, target)
	case able == 0:
		return nil, &hostCannotUpdateError{sentence: fmt.Sprintf("No host behind %s can update itself, because none has the update helper installed. "+
			"Run sudo zoomies updates helper install on each, or update them with the command on their cards.", target)}
	}
	r := &store.UpdateRollout{Target: target, Trigger: store.UpdateTriggerManual, StartedBy: requestedBy(by), HostIDs: ids}
	if err := c.st.CreateUpdateRollout(ctx, r); err != nil {
		if errors.Is(err, store.ErrConflict) {
			if err := c.refuseOverOpenRollout(ctx); err != nil {
				return nil, err
			}
			return nil, ErrUpdateInProgress
		}
		return nil, fmt.Errorf("recording the rollout: %w", err)
	}
	c.log.Info("a rollout was started", "rollout", r.ID, "to", target, "hosts", len(ids), "started_by", r.StartedBy)
	c.KickUpdates()
	return c.publishUpdates(ctx)
}

// refuseOverOpenRollout is the refusal for a rollout asked for while one is
// open: halted is waiting for a person to look, and running is in progress.
func (c *Controller) refuseOverOpenRollout(ctx context.Context) error {
	r, err := c.st.OpenUpdateRollout(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("reading the open rollout: %w", err)
	case r.State == store.RolloutHalted:
		return ErrUpdateRolloutHalted
	}
	return ErrUpdateInProgress
}

// openRolloutFor is the open rollout, for a person resuming or cancelling it,
// with the refusals they share.
func (c *Controller) openRolloutFor(ctx context.Context, doing string) (*store.UpdateRollout, error) {
	if !c.mayAct() {
		return nil, fmt.Errorf("%w: %s", ErrUpdateFenced, c.notActingReason())
	}
	if c.updateMode() == updates.ModeOff {
		return nil, ErrUpdateModeOff
	}
	r, err := c.st.OpenUpdateRollout(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: there is no rollout to %s", store.ErrNotFound, doing)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the open rollout: %w", err)
	}
	return r, nil
}

// ResumeRollout lets a halted rollout carry on. The failure it halted on stays
// on the host's attempt, and the planner reads only failures after the resume,
// so the host that failed waits out its retry and the rollout goes on. A
// rollout already running is left as it is.
func (c *Controller) ResumeRollout(ctx context.Context, by UpdateActor) (*UpdatesView, error) {
	r, err := c.openRolloutFor(ctx, "resume")
	if err != nil {
		return nil, err
	}
	moved, err := c.st.ResumeUpdateRollout(ctx, r.ID)
	if err != nil {
		return nil, fmt.Errorf("resuming the rollout: %w", err)
	}
	if moved {
		c.log.Info("a rollout was resumed", "rollout", r.ID, "to", r.Target, "resumed_by", requestedBy(by))
		c.KickUpdates()
	}
	return c.publishUpdates(ctx)
}

// CancelRollout ends the open rollout. An update a helper has already been
// handed finishes by itself and is recorded; nothing new starts for the rollout.
// Who cancelled is kept, because auto does not start again what a person
// stopped.
func (c *Controller) CancelRollout(ctx context.Context, by UpdateActor) (*UpdatesView, error) {
	r, err := c.openRolloutFor(ctx, "cancel")
	if err != nil {
		return nil, err
	}
	moved, err := c.st.CancelUpdateRollout(ctx, r.ID, requestedBy(by))
	if err != nil {
		return nil, fmt.Errorf("cancelling the rollout: %w", err)
	}
	if moved {
		c.log.Info("a rollout was cancelled", "rollout", r.ID, "to", r.Target, "cancelled_by", requestedBy(by))
		c.KickUpdates()
	}
	return c.publishUpdates(ctx)
}
