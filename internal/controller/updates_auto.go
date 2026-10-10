package controller

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

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
	// attempts is the attempts to the releases machines would be taken to now
	// and the rollouts' steps, open and ended, with every open attempt.
	attempts []store.UpdateAttempt
}

// updatesSnapshot reads everything the planner decides on. It decides
// nothing itself: the rows go into the snapshot as they are, and Decide works
// out from them which failures count, so the rule that halts a rollout is one
// a table of pure tests can try.
//
// The settings and the helper are passed in, because the status has just read
// both: a second reading of the settings could give the snapshot another mode
// than the status beside it, and a second probe would read the folder twice.
func (c *Controller) updatesSnapshot(ctx context.Context, cfg config.Updates, helper helperProbe) (*updatesPicture, error) {
	pic := &updatesPicture{}
	s := updates.Snapshot{
		Now: c.Now(), Mode: updateModeOf(cfg.Mode), Soak: cfg.Soak, Running: version.Version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Fenced: !c.mayAct(), HostGraceUntil: c.hostGraceUntil(),
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

	// What the planner decides on is a machine's attempts to the release it
	// would be taken to now (the hosts' target and the controller's choice) and
	// the rollouts' steps, not the newest attempts fleet-wide: a host's two
	// failures must hold however many other attempts are newer.
	tags := tagsInQuestion(s)
	var rollouts []string
	for _, r := range []*store.UpdateRollout{pic.rollout, pic.last} {
		if r != nil {
			rollouts = append(rollouts, r.ID)
		}
	}
	recent, err := c.st.UpdateAttemptsFor(ctx, tags, rollouts)
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
		can, why, _ := hostCanSelfUpdate(h, target, c.hostHelperUnsupported(h), s.Mode)
		s.Hosts = append(s.Hosts, updates.HostFacts{
			ID: h.ID, Name: h.Name, Version: h.Version, GOOS: h.OS, GOARCH: h.Arch,
			Embedded: h.Embedded, Healthy: h.Healthy(s.Now), CanSelfUpdate: can, WhyNot: why,
			ActiveRunners: h.ActiveRunners,
		})
	}
	for i := range s.Hosts {
		byHost[s.Hosts[i].ID] = &s.Hosts[i]
	}
	var gone []updates.HostFacts
	for _, a := range pic.attempts {
		facts := attemptFacts(a)
		switch {
		case a.Scope == store.UpdateScopeController && a.State == store.UpdateRequested:
			s.Controller = &facts
		case a.Scope == store.UpdateScopeController:
			s.ControllerEnded = append(s.ControllerEnded, facts)
		case byHost[a.HostID] == nil && a.State == store.UpdateRequested:
			// A host with no row and an attempt still open: deleting a host
			// cancels its attempt in the same transaction, so this is a race
			// or a fault, and something may still be restarting. It blocks the
			// next start as any open attempt does, and times out as one.
			gone = append(gone, updates.HostFacts{ID: a.HostID, Name: a.HostID, Open: &facts})
		case byHost[a.HostID] == nil:
		case a.State == store.UpdateRequested:
			byHost[a.HostID].Open = &facts
		default:
			byHost[a.HostID].Ended = append(byHost[a.HostID].Ended, facts)
		}
	}
	s.Hosts = append(s.Hosts, gone...)
	pic.snap = s
	return pic, nil
}

// tagsInQuestion is the releases a machine would be taken to now: the hosts'
// target and the controller's choice. The planner reads the attempts to them,
// and the retention pass keeps their failures, so both ask the same question
// of the same snapshot fields.
func tagsInQuestion(s updates.Snapshot) []string {
	var tags []string
	if target := hostTarget(); target != "" {
		tags = append(tags, target)
	}
	choice := updates.Choose(updates.ChooseInput{Mode: s.Mode, Soak: s.Soak, Now: s.Now, Running: s.Running,
		Releases: s.Releases, GOOS: s.GOOS, GOARCH: s.GOARCH})
	if tag := choice.Release.Tag; tag != "" && !slices.Contains(tags, tag) {
		tags = append(tags, tag)
	}
	return tags
}

// retainedUpdateTags is the releases whose failed attempts the retention pass
// keeps: tagsInQuestion, read from the settings and releases the snapshot would
// be made from, and the release the controller's latest attempt was for. The
// last is there because with updating off no release list is read, so the
// choice names nothing, and the controller's failures would otherwise age out
// and the cap restart when updating is turned on again.
func (c *Controller) retainedUpdateTags(ctx context.Context, now time.Time) ([]string, error) {
	cfg := c.cfg().Updates
	s := updates.Snapshot{Now: now, Mode: updateModeOf(cfg.Mode), Soak: cfg.Soak, Running: version.Version,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if state := c.latestRelease(); state != nil {
		s.Releases = state.Releases
	}
	tags := tagsInQuestion(s)
	last, err := c.st.ListUpdateAttempts(ctx, store.UpdateScopeController, "", 1)
	if err != nil {
		return nil, fmt.Errorf("reading the controller's latest update attempt: %w", err)
	}
	if len(last) == 1 && !slices.Contains(tags, last[0].ToVersion) {
		tags = append(tags, last[0].ToVersion)
	}
	return tags, nil
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
	pic, err := c.updatesSnapshot(ctx, c.cfg().Updates, c.probeUpdateHelper())
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
	// Not audited: the pass's own closer records the same time-out without a
	// row, and which of the two saw the ninetieth minute first is chance. The
	// attempt's state and error are the record.
	return c.endAttempt(ctx, a, store.UpdateTimedOut, timedOutText(a))
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

// beforeRolloutStep runs between autoUpdateHost's second reading of the
// rollout and the host's attempt being written. It does nothing; it is a
// variable so that a test can land a cancel in that gap.
var beforeRolloutStep = func(*Controller) {}

func (c *Controller) autoUpdateHost(ctx context.Context, pic *updatesPicture, step updates.Action) bool {
	if pic.rollout == nil {
		return false
	}
	// Read again: a person may have cancelled or halted the rollout, or switched
	// to manual, since the snapshot, and a host asked now would restart after
	// they said stop. Only the same rollout, still running, and in manual one a
	// person started, is moved on.
	now, err := c.st.OpenUpdateRollout(ctx)
	if err != nil || now.ID != pic.rollout.ID || now.State != store.RolloutRunning ||
		(c.updateMode() != updates.ModeAuto && now.Trigger != store.UpdateTriggerManual) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("automatic updating could not read the open rollout again before asking a host; the next pass will decide again", "error", err)
		}
		return false
	}
	beforeRolloutStep(c)
	// A cancel or halt can still land here, after the read above. The store
	// writes the step only while the rollout runs, in the insert's own
	// transaction, so a host is never asked for a rollout a person stopped.
	_, attempt, err := c.requestHostUpdate(ctx, autoUpdateActor(), step.HostID,
		updateAsk{trigger: store.UpdateTriggerAuto, rolloutID: pic.rollout.ID})
	if errors.Is(err, store.ErrRolloutNotRunning) {
		c.log.Info("the rollout stopped before its next host was asked, so that host was not asked",
			"host", step.HostID, "rollout", pic.rollout.ID)
		return false
	}
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

// RolloutChange is what a person's start, resume or cancel did: the rollout it
// acted on, and whether that moved. The handler audits it, so a press that
// moved nothing (a resume of a rollout already running) is still recorded, and
// says so rather than claiming it resumed something.
type RolloutChange struct {
	ID, Target string
	Changed    bool
}

// ErrUnknownHost refuses a rollout of a host id that names no host. It is not
// store.ErrNotFound, which the API answers as the route's own 404: an id the
// caller sent is the body to fix.
var ErrUnknownHost = errors.New("no host has that id")

// unknownHostError reads as the sentence that names the id, and is still
// ErrUnknownHost to errors.Is, so the handler needs no prefix trimmed off.
type unknownHostError struct{ id string }

func (e *unknownHostError) Error() string {
	return fmt.Sprintf("no host has the id %q; list the hosts to find it", cutAt(e.id, 64))
}
func (e *unknownHostError) Is(target error) bool { return target == ErrUnknownHost }

// StartHostRollout starts a rollout of every host behind this controller's
// release, or of hostIDs, and returns the status with it. The planner moves it
// on from the next pass, one host at a time; nothing is asked of a host here.
//
// by is only written down, as the rollout's started_by. The handler that took
// the request audits it with the caller's identity. Once the rollout is written
// the change names it even when the status after it cannot be worked out, so
// that the handler can audit a rollout that exists before it reports the
// failure.
func (c *Controller) StartHostRollout(ctx context.Context, by UpdateActor, hostIDs []string) (*UpdatesView, RolloutChange, error) {
	var none RolloutChange
	if !c.mayAct() {
		return nil, none, fmt.Errorf("%w: %s", ErrUpdateFenced, c.notActingReason())
	}
	mode := readUpdateMode(c)
	if mode == updates.ModeOff {
		return nil, none, ErrUpdateModeOff
	}
	target := hostTarget()
	if target == "" {
		return nil, none, ErrUpdateNotARelease
	}
	if err := c.refuseOverOpenRollout(ctx); err != nil {
		return nil, none, err
	}
	// One listing of the fleet, and the ids read against it: a repeated id is
	// one host, and the list can never be longer than the fleet.
	fleet, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, none, fmt.Errorf("listing the hosts: %w", err)
	}
	hosts := fleet
	var ids store.StringSlice
	if len(hostIDs) > 0 {
		byID := make(map[string]*store.Host, len(fleet))
		for _, h := range fleet {
			byID[h.ID] = h
		}
		hosts = nil
		for _, id := range hostIDs {
			h, ok := byID[id]
			if !ok {
				return nil, none, &unknownHostError{id: id}
			}
			if slices.Contains(ids, id) {
				continue
			}
			hosts, ids = append(hosts, h), append(ids, id)
		}
	}
	behind, able := 0, 0
	for _, h := range hosts {
		if !hostBehind(h, target) {
			continue
		}
		behind++
		if can, _, _ := hostCanSelfUpdate(h, target, c.hostHelperUnsupported(h), mode); can {
			able++
		}
	}
	switch {
	case behind == 0:
		return nil, none, fmt.Errorf("%w: no host asked for runs a release behind %s", ErrUpdateNothingNewer, target)
	case able == 0:
		return nil, none, &hostCannotUpdateError{sentence: fmt.Sprintf("No host behind %s can update itself, because none has the update helper installed. "+
			"Run sudo zoomies updates helper install on each Linux host, or update them by hand as their cards say.", target)}
	}
	r := &store.UpdateRollout{Target: target, Trigger: store.UpdateTriggerManual, StartedBy: requestedBy(by), HostIDs: ids}
	if err := c.st.CreateUpdateRollout(ctx, r); err != nil {
		if errors.Is(err, store.ErrConflict) {
			if err := c.refuseOverOpenRollout(ctx); err != nil {
				return nil, none, err
			}
			return nil, none, ErrUpdateInProgress
		}
		return nil, none, fmt.Errorf("recording the rollout: %w", err)
	}
	c.log.Info("a rollout was started", "rollout", r.ID, "to", target, "hosts", len(ids), "started_by", r.StartedBy)
	c.KickUpdates()
	change := RolloutChange{ID: r.ID, Target: r.Target, Changed: true}
	view, err := c.publishUpdates(ctx)
	return view, change, err
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
// rollout already running is left as it is, and the change says nothing moved.
func (c *Controller) ResumeRollout(ctx context.Context, by UpdateActor) (*UpdatesView, RolloutChange, error) {
	r, err := c.openRolloutFor(ctx, "resume")
	if err != nil {
		return nil, RolloutChange{}, err
	}
	moved, err := c.st.ResumeUpdateRollout(ctx, r.ID)
	if err != nil {
		return nil, RolloutChange{}, fmt.Errorf("resuming the rollout: %w", err)
	}
	if moved {
		c.log.Info("a rollout was resumed", "rollout", r.ID, "to", r.Target, "resumed_by", requestedBy(by))
		c.KickUpdates()
	}
	view, err := c.publishUpdates(ctx)
	return view, RolloutChange{ID: r.ID, Target: r.Target, Changed: moved}, err
}

// CancelRollout ends the open rollout. An update a helper has already been
// handed finishes by itself and is recorded; nothing new starts for the rollout.
// Who cancelled is kept, because auto does not start again what a person
// stopped.
func (c *Controller) CancelRollout(ctx context.Context, by UpdateActor) (*UpdatesView, RolloutChange, error) {
	r, err := c.openRolloutFor(ctx, "cancel")
	if err != nil {
		return nil, RolloutChange{}, err
	}
	moved, err := c.st.CancelUpdateRollout(ctx, r.ID, requestedBy(by))
	if err != nil {
		return nil, RolloutChange{}, fmt.Errorf("cancelling the rollout: %w", err)
	}
	if moved {
		c.log.Info("a rollout was cancelled", "rollout", r.ID, "to", r.Target, "cancelled_by", requestedBy(by))
		c.KickUpdates()
	}
	view, err := c.publishUpdates(ctx)
	return view, RolloutChange{ID: r.ID, Target: r.Target, Changed: moved}, err
}
