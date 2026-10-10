package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

// hostSightLimit is how many of the newest host attempts a look reads to find
// each host's latest. A host whose last attempt is older than that many others
// shows none, which is history and not something in flight: every open attempt
// is read as well, however old.
const hostSightLimit = 500

// HostUpdateView is a host's part in updating, as its card shows it: the latest
// attempt that still says something about the host, whether it can be updated
// now, and why in a sentence.
//
// It holds nothing that moves with the clock or with a heartbeat, so the card
// is sent again only when one of these changes.
type HostUpdateView struct {
	// State is the latest attempt's state while it says something about the
	// host: requested, succeeded, failed, timed_out or cancelled. It is none when
	// there is no such attempt, including one that failed before the host
	// reached its release some other way, and unsupported in its place for a
	// host behind the controller that the helper can never be installed on.
	State string `json:"state"`
	// Reason is the sentence the card shows. For an attempt that ended without
	// the update it is the reason recorded for it, often the helper's own, which
	// can name a path on the host: only the platform role reads it (see For).
	Reason string `json:"reason"`
	// CanUpdate says whether asking for an update now would be taken.
	CanUpdate bool `json:"can_update"`
	// AttemptID is the attempt State describes, and empty with none.
	AttemptID string `json:"attempt_id"`
}

// The states of a host with no attempt worth showing.
const (
	HostUpdateNone = "none"
	// HostUpdateUnsupported is a host behind the controller that the update
	// helper can never be installed on, so the command on its card is the way.
	HostUpdateUnsupported = "unsupported"
)

// For is the update block as one audience may read it. The platform reads it
// whole; every other role is given a fixed sentence for an ended attempt in
// place of its reason, and still learns that it ended and how. It works on the
// fields alone, so the event stream can apply it to a frame it has decoded.
func (u HostUpdateView) For(platform bool) HostUpdateView {
	switch {
	case platform:
		return u
	case u.State == store.UpdateFailed, u.State == store.UpdateTimedOut, u.State == store.UpdateCancelled:
		u.Reason = withheldAttemptError(u.State)
	}
	return u
}

// For is the host as one audience may read it: HostView renders the platform's
// form, and every handler that answers with a host passes it through here with
// the caller's role. The view it returns has an update block of its own, so the
// one it was called on, which may be shared, is never changed.
func (v HostView) For(platform bool) HostView {
	if v.Update == nil {
		return v
	}
	u := v.Update.For(platform)
	v.Update = &u
	return v
}

// hostUpdateGap is what stands between a host that is behind and its update,
// where the update helper is part of the answer. The card and the problems list
// both read it, so the note that a host cannot be updated from here is raised
// for exactly the hosts whose card says so.
type hostUpdateGap int

const (
	// hostGapNone is a host with nothing about the helper to say: it can be
	// updated, it has nothing to update, or something else is in the way.
	hostGapNone hostUpdateGap = iota
	// hostGapHelperMissing is a host whose agent does not offer to update
	// itself, which an install of the helper would change.
	hostGapHelperMissing
	// hostGapHelperImpossible is a host the helper can never be installed on.
	hostGapHelperImpossible
)

// modeOffForHosts is the card's sentence for a host that could be asked but for
// the mode. Every role reads it, so it names the setting and no path.
const modeOffForHosts = "Updating is off, so hosts are not updated from here. Somebody with the platform role can turn it on by " +
	"setting updates.mode to manual or auto on the Configuration page."

// hostCanSelfUpdate is the single place that says whether a host can be updated
// from here, and if not, why, in a sentence for the card. The view, the request,
// the problems list and the planner all ask it, so the button, the note and the
// planner never disagree.
//
// target is the release the host would be taken to, empty when this controller
// is not a release; unsupported is why the helper can never be installed on the
// host, or nothing known. A host that cannot have it is said to be so only where
// that is why it cannot be updated, so the card never explains a helper to a
// host that has nothing to update.
//
// The mode is asked after everything that turning updating on would not
// change, so a host the helper can never serve keeps saying the command is the
// way, and before the helper is: while updating is off nothing is offered, and
// a card asking for a root unit to be installed would be offering something.
func hostCanSelfUpdate(h *store.Host, target string, unsupported updates.HelperUnsupported, mode updates.Mode) (can bool, why string, gap hostUpdateGap) {
	switch {
	case h.Embedded:
		return false, "This is the agent inside the controller, so it is updated with the controller, from Settings → Updates.", hostGapNone
	case target == "":
		return false, "This controller is not running a release, so there is no release to take its hosts to. Install a release on the controller with zoomies upgrade first.", hostGapNone
	case strings.TrimSpace(h.Version) == "":
		byHandNow := "Update it on the host with zoomies upgrade."
		if h.OS == "windows" {
			byHandNow = byHand(h)
		}
		return false, "This host's agent has not said which version it runs, so Zoomies cannot tell whether " + target + " is newer. " + byHandNow, hostGapNone
	}
	// Only a release build can be behind. CompareBuilds reads what follows a
	// hyphen as a pre-release, so a describe build such as 1.3.5-3-gabcdef1,
	// which is ahead of v1.3.5, would otherwise read as behind it, and the
	// button would take it back. The planner keeps the same gate of its own.
	if _, ok := updates.TargetTag(h.Version); !ok {
		return false, "This host runs the build " + reportedVersion(h.Version) + ", which is not a release build, so Zoomies cannot tell whether " +
			target + " is newer. " + byHand(h), hostGapNone
	}
	switch version.CompareBuilds(h.Version, target) {
	case version.SkewNone, version.SkewAhead:
		return false, "This host already runs " + target + " or a later release, so there is nothing to update.", hostGapNone
	case version.SkewDiffers:
		return false, "This host runs the build " + reportedVersion(h.Version) + ", which is not a release build, so Zoomies cannot tell whether " +
			target + " is newer. " + byHand(h), hostGapNone
	}
	selfUpdates := h.Supports(agent.FeatureSelfUpdate)
	if !selfUpdates && unsupported != "" {
		return false, "The update helper cannot be installed on this host: " + helperUnsupportedWhy(unsupported, "its agent") +
			". " + byHand(h), hostGapHelperImpossible
	}
	if mode == updates.ModeOff {
		return false, modeOffForHosts, hostGapNone
	}
	if !selfUpdates {
		return false, "This host's agent does not offer to update itself, which it does only once the update helper is installed on the host. " +
			"Run sudo zoomies updates helper install there, or update it with the command below.", hostGapHelperMissing
	}
	return true, "This host runs " + reportedVersion(h.Version) + " and can be updated to " + target + ".", hostGapNone
}

// byHand is the end of a card's sentence that sends a person to update the
// host on the host itself. On Windows there is no command at the foot of the
// card to point at (see hostUpgrade), only the steps.
func byHand(h *store.Host) string {
	if h.OS == "windows" {
		return "Update it by hand on the host, with the steps at the foot of this card."
	}
	return "Update it on the host with the command below."
}

// byHandOnCard is what the foot of a host's card holds for updating it by
// hand, for a sentence that points at it from elsewhere.
func byHandOnCard(h *store.Host) string {
	if h.OS == "windows" {
		return "steps"
	}
	return "command"
}

// hostBehind says whether a host runs a release build older than target: the
// hosts a rollout to target is for, whether or not each can update itself.
// hostCanSelfUpdate asks the same of the version, so the two never disagree.
func hostBehind(h *store.Host, target string) bool {
	if _, ok := updates.TargetTag(h.Version); !ok || h.Embedded || target == "" {
		return false
	}
	return version.CompareBuilds(h.Version, target) == version.SkewBehind
}

// hostTarget is the release every host is taken to: the one this controller
// runs, and nothing when it is not a release.
func hostTarget() string {
	tag, _ := updates.TargetTag(version.Version)
	return tag
}

// hostCannotUpdateError is a refusal that reads as the card's own sentence and is
// still ErrUpdateHostCannotUpdate to errors.Is, so the API gives it the stable
// code and the person reads what the card would have told them.
type hostCannotUpdateError struct{ sentence string }

func (e *hostCannotUpdateError) Error() string        { return e.sentence }
func (e *hostCannotUpdateError) Is(target error) bool { return target == ErrUpdateHostCannotUpdate }

// RequestHostUpdate asks a host's agent to have its helper take it to this
// controller's release, and returns the host with the attempt on it.
//
// The order is RequestControllerUpdateAttempt's: everything that can refuse does
// so before anything is written, and the attempt is recorded before the task is
// queued, because the store's one open attempt per host is what stops two callers
// each sending one. Nothing is dialled: the task goes out on the agent's own poll,
// and success is the host heartbeating the release, not the task's answer.
//
// The helper's path unit stops after five starts in ten minutes. One open attempt
// per host, and the planner's 30-minute wait after a failure, keep a host far below it.
func (c *Controller) RequestHostUpdate(ctx context.Context, by UpdateActor, hostID string) (*HostView, error) {
	view, _, err := c.requestHostUpdate(ctx, by, hostID, askedByHand)
	return view, err
}

// requestHostUpdate is RequestHostUpdate for either asker, returning the attempt
// it opened as well: the button and the planner go through every refusal alike,
// so the card and the planner never disagree about a host.
func (c *Controller) requestHostUpdate(ctx context.Context, by UpdateActor, hostID string, ask updateAsk) (*HostView, *store.UpdateAttempt, error) {
	if !c.mayAct() {
		return nil, nil, fmt.Errorf("%w: %s", ErrUpdateFenced, c.notActingReason())
	}
	// Read once, and the one reading answers both questions below. Read twice, a
	// switch to off between the two would pass the gate and then be refused as a
	// host that cannot update, with the wrong code for what happened.
	mode := readUpdateMode(c)
	if mode == updates.ModeOff {
		return nil, nil, ErrUpdateModeOff
	}
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return nil, nil, err
	}
	target := hostTarget()
	if target == "" {
		return nil, nil, ErrUpdateNotARelease
	}
	if can, why, _ := hostCanSelfUpdate(h, target, c.hostHelperUnsupported(h), mode); !can {
		return nil, nil, &hostCannotUpdateError{sentence: why}
	}

	attempt := &store.UpdateAttempt{
		Scope: store.UpdateScopeHost, HostID: h.ID, FromVersion: h.Version, ToVersion: target,
		Trigger: ask.trigger, RequestedBy: requestedBy(by), RolloutID: ask.rolloutID,
	}
	if err := c.st.CreateUpdateAttempt(ctx, attempt); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, nil, ErrUpdateInProgress
		}
		return nil, nil, fmt.Errorf("recording the update attempt: %w", err)
	}
	c.log.Info("asked a host's agent to update it", "attempt", attempt.ID, "host", h.ID, "name", h.Name,
		"from", attempt.FromVersion, "to", target, "requested_by", attempt.RequestedBy)
	// Seen before the task is queued, so that a result coming back at once finds
	// the attempt it answers.
	c.lookAtHostUpdates(context.WithoutCancel(ctx))
	c.sendHostUpdate(h, *attempt)
	c.publishHost(h)
	view := c.HostView(h)
	return &view, attempt, nil
}

// sendHostUpdate queues the task for an open attempt, unless the host's agent
// has already taken it or would refuse it. It is called on every pass, because
// the queue lives in memory: a restart, or a task the agent never answered, is
// made good by queueing it again.
//
// enqueue stamps nothing and checks nothing, so the gates are here. An agent
// that does not advertise the feature answers an unknown kind with a failure,
// and the agent inside the controller is updated with the controller.
func (c *Controller) sendHostUpdate(h *store.Host, a store.UpdateAttempt) bool {
	if h == nil || !c.mayAct() || h.Embedded || !h.Supports(agent.FeatureSelfUpdate) {
		return false
	}
	if c.handedOver(a.ID) {
		return false
	}
	return c.enqueue(h.ID, agent.Task{Kind: agent.TaskUpdateAgent, UpdateID: a.ID, UpdateTag: a.ToVersion})
}

// handedOver says whether the agent answered that it wrote this attempt's
// request, or already runs its release. After that the task is not sent again:
// the outcome comes on the heartbeat, and a second request would wait behind the
// first.
func (c *Controller) handedOver(attemptID string) bool {
	c.updates.handedMu.Lock()
	defer c.updates.handedMu.Unlock()
	return c.updates.handed[attemptID]
}

func (c *Controller) noteHandedOver(attemptID string) {
	c.updates.handedMu.Lock()
	defer c.updates.handedMu.Unlock()
	if c.updates.handed == nil {
		c.updates.handed = make(map[string]bool)
	}
	c.updates.handed[attemptID] = true
}

// withdrawHostUpdate takes an ended attempt's task back from its host's queue,
// waiting or held by the agent, so that a host that comes back after its attempt
// timed out is not then updated with nothing recording it.
func (c *Controller) withdrawHostUpdate(a store.UpdateAttempt) {
	c.updates.handedMu.Lock()
	delete(c.updates.handed, a.ID)
	c.updates.handedMu.Unlock()
	c.withdrawUpdateTasks(a.HostID, func(t agent.Task) bool { return t.UpdateID == a.ID }, true)
}

// withdrawHostUpdates takes back every update task of a host whose registration
// is going away, and forgets what the loop saw of it.
func (c *Controller) withdrawHostUpdates(hostID string) {
	c.withdrawUpdateTasks(hostID, func(agent.Task) bool { return true }, true)
	c.forgetHostUpdates(hostID)
}

// withdrawUpdateTasks drops a host's update tasks that match, without making a
// queue for a host that has none.
func (c *Controller) withdrawUpdateTasks(hostID string, match func(agent.Task) bool, inFlight bool) {
	q, ok := c.queues.all()[hostID]
	if !ok {
		return
	}
	q.withdraw(func(t agent.Task) bool { return t.Kind == agent.TaskUpdateAgent && match(t) }, inFlight)
}

// applyUpdateTaskResult takes the agent's answer to an update task. The answer
// says only whether the request reached the helper; whether the update worked
// comes on a later heartbeat, from the agent the update started.
func (c *Controller) applyUpdateTaskResult(ctx context.Context, hostID string, task agent.Task, known bool, res agent.TaskResult) {
	if !known || task.UpdateID == "" {
		// Not a task this process handed out, so the attempt it answers is not
		// known. The pass sends a task of its own while the attempt is open.
		return
	}
	switch {
	case res.OK:
		c.noteHandedOver(task.UpdateID)
		return
	case res.NotStarted:
		// Given back unstarted too often to offer again; the pass queues it anew.
		return
	case !c.mayAct():
		return
	}
	latest, err := c.st.ListUpdateAttempts(ctx, store.UpdateScopeHost, hostID, 1)
	if err != nil {
		c.log.Warn("could not read the update attempt an agent answered; it stays open until the host reports or it times out", "host", hostID, "error", err)
		return
	}
	// Only the open attempt of this host, and only the one the task was for: an
	// answer to an older task must not close a newer attempt.
	if len(latest) == 0 || latest[0].ID != task.UpdateID || latest[0].State != store.UpdateRequested {
		return
	}
	name := c.hostName(ctx, hostID)
	var text string
	switch said := reportedSentence(res.Error); {
	case strings.Contains(said, "unknown task kind"):
		text = "The agent on " + name + " is older than this controller and cannot update itself. Update it with the command on its card."
	case said == "":
		text = "The agent on " + name + " did not hand the request to its update helper, and gave no reason. Look at the agent's log on the host."
	default:
		text = cutAt("The agent on "+name+" did not hand the request to its update helper: "+said, maxHelperSentence)
	}
	_, _ = c.closeHostAttempt(ctx, latest[0], store.UpdateFailed, text)
}

// noteHostUpdate is what a heartbeat says about the host's open attempt: the
// host running the release, which is the only proof of success, or the helper's
// answer relayed by the agent. It says whether a report the beat carried was
// held rather than recorded, which the answer passes on so that the agent sends
// it again instead of taking it as delivered.
//
// The open attempt is the one the loop last saw, so a beat with nothing to say
// reads nothing. A beat that carries a result reads the store instead, because
// the agent sends a result until a beat has been answered without it held: one
// matched against a stale picture would be lost.
func (c *Controller) noteHostUpdate(ctx context.Context, h *store.Host, rep *agent.UpdateReport) (held bool) {
	if !c.mayAct() {
		if rep != nil {
			c.warnReportHeld(h, "this controller is fenced or does not hold the lease, so it records nothing until it may act again", nil)
		}
		return rep != nil
	}
	a, ok := c.hostAttempt(h.ID)
	if rep != nil {
		latest, err := readHostAttempt(c, ctx, h.ID)
		if err != nil {
			c.warnReportHeld(h, "the host's update attempt could not be read to match the result against", err)
			return true
		}
		if len(latest) == 1 {
			a, ok = latest[0], true
		}
	}
	if !ok || a.State != store.UpdateRequested {
		// A report with no open attempt of this host's to answer is ignored: an
		// agent relays whatever its update folder holds, which can be a result
		// for an attempt already closed, or for the controller itself where the
		// two share a folder.
		return false
	}
	state, text, ended := hostReportOutcome(a, h.Version, rep)
	if !ended {
		return false
	}
	closed, err := c.closeHostAttempt(ctx, a, state, text)
	if closed {
		c.publishHost(h)
	}
	return rep != nil && err != nil
}

// readHostAttempt is a host's latest attempt from the store. It is a variable
// so that a test can make the read fail once.
var readHostAttempt = func(c *Controller, ctx context.Context, hostID string) ([]store.UpdateAttempt, error) {
	return c.st.ListUpdateAttempts(ctx, store.UpdateScopeHost, hostID, 1)
}

// heldReportWarnEvery is how often one host's held report is logged. A fenced
// controller holds it on every beat until it may act, and a line every few
// seconds for each host would bury the one that says why it is fenced.
const heldReportWarnEvery = 10 * time.Minute

// warnReportHeld logs that a host's update result was held, and why, at most
// once per heldReportWarnEvery for each host.
func (c *Controller) warnReportHeld(h *store.Host, why string, err error) {
	now := c.Now()
	c.updates.heldMu.Lock()
	last, seen := c.updates.heldWarned[h.ID]
	if seen && now.Sub(last) < heldReportWarnEvery {
		c.updates.heldMu.Unlock()
		return
	}
	if c.updates.heldWarned == nil {
		c.updates.heldWarned = make(map[string]time.Time)
	}
	c.updates.heldWarned[h.ID] = now
	c.updates.heldMu.Unlock()
	attrs := []any{"host", h.ID, "name", h.Name, "reason", why}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	c.log.Warn("did not record the update result a host's agent reported; the agent sends it again until it is recorded", attrs...)
}

// hostReportOutcome says whether a host's open attempt has ended, from the
// version the host reports and the result its agent relays.
//
// Running the release, or a later one, is success, whatever the result says: an
// agent asked to update to a release it already runs answers with nothing
// written, and no result ever follows. Versions are compared with CompareBuilds,
// because a release binary reports 1.3.5 where the tag is v1.3.5.
//
// A result answers the attempt that carries its id. A failure without an id is
// the helper refusing a request it could not trust, which echoes no id; one
// finished after the attempt was asked for can only be the answer to it.
func hostReportOutcome(a store.UpdateAttempt, running string, rep *agent.UpdateReport) (state, text string, ended bool) {
	if runsAtLeast(running, a.ToVersion) {
		return store.UpdateSucceeded, "", true
	}
	if rep == nil {
		return "", "", false
	}
	switch {
	case rep.ID == a.ID:
	case rep.ID == "" && !rep.OK && rep.FinishedAt.After(a.RequestedAt):
	default:
		return "", "", false
	}
	if rep.OK {
		return store.UpdateFailed, fmt.Sprintf("The update helper on the host says %s is installed, but its agent still reports %s, so the new release is not the one running. "+
			"Restart the zoomies agent on the host, and look at journalctl -u zoomies-update there if it does not come back on %s.",
			a.ToVersion, reportedVersion(running), a.ToVersion), true
	}
	if msg := reportedSentence(rep.Error); msg != "" {
		return store.UpdateFailed, msg, true
	}
	return store.UpdateFailed, "The update helper on the host says the update failed, and gave no reason. Look at journalctl -u zoomies-update on the host.", true
}

// reportedSentence is text an agent relayed, made fit to keep. The agent cleans
// it before sending, but the controller does not take an agent's word for what
// is safe to store and show: invalid UTF-8 is replaced, a control or
// direction-changing character becomes a space, and it is cut to the length the
// controller keeps of its own helper's sentence.
func reportedSentence(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidiControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	return strings.TrimSpace(cutAt(strings.TrimSpace(s), maxHelperSentence))
}

// reportedVersion is a version an agent reported, cut to a name's length for a
// sentence. It is the agent's word and may be anything.
func reportedVersion(s string) string {
	return cutAt(naming.ForSentence(s), 64)
}

// closeHostAttempt ends a host's open attempt, takes its task back, and brings
// what the loop knows up to date so that the card rendered next says so. It
// says whether this call was the one that closed it, and the store's error
// when the ending could not be written.
func (c *Controller) closeHostAttempt(ctx context.Context, a store.UpdateAttempt, state, text string) (bool, error) {
	closed, err := c.recordUpdateEnding(ctx, a, state, text)
	if !closed {
		return false, err
	}
	c.withdrawHostUpdate(a)
	c.lookAtHostUpdates(ctx)
	return true, nil
}

// keepHostUpdateGoing is what a pass does for a host's attempt that has not
// ended. With updating switched off nothing new is started: a task the agent has
// not yet taken is taken back, and the attempt ends by itself at its time-out.
// One the agent has taken, or still holds, carries on, and its outcome is
// recorded however it ends.
func (c *Controller) keepHostUpdateGoing(h *store.Host, a store.UpdateAttempt) {
	if c.updateMode() == updates.ModeOff {
		c.withdrawUpdateTasks(a.HostID, func(t agent.Task) bool { return t.UpdateID == a.ID }, false)
		return
	}
	c.sendHostUpdate(h, a)
}

// hostAttemptOutcome is how a pass judges a host's open attempt: the host gone,
// the host on the release, or the time-out. A host that has merely gone quiet is
// none of these, because an update restarts the agent and the planner counts
// only the attempt's own time-out as failure.
func (c *Controller) hostAttemptOutcome(a store.UpdateAttempt, h *store.Host) (state, text string, ended bool) {
	if h == nil {
		return store.UpdateCancelled, "The host was removed before the update finished, so nothing will report on it. Enrol the host again to update it.", true
	}
	if state, text, ended := hostReportOutcome(a, h.Version, nil); ended {
		return state, text, true
	}
	return c.updateOutcome(a)
}

// lookAtHostUpdates refreshes what the loop knows of each host's latest attempt,
// which the host view and the problems list read without a query each.
//
// A failed read keeps what was last seen, as lookAtUpdates does.
func (c *Controller) lookAtHostUpdates(ctx context.Context) {
	c.updates.hostLookMu.Lock()
	defer c.updates.hostLookMu.Unlock()
	recent, err := c.st.ListUpdateAttempts(ctx, store.UpdateScopeHost, "", hostSightLimit)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("could not read the hosts' update attempts; the next pass will try again", "error", err)
		}
		return
	}
	open, err := c.st.OpenUpdateAttempts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("could not read the hosts' open update attempts; the next pass will try again", "error", err)
		}
		return
	}
	latest := make(map[string]store.UpdateAttempt)
	for _, a := range recent {
		if _, seen := latest[a.HostID]; !seen {
			latest[a.HostID] = a
		}
	}
	// An open attempt is the latest of its host by the store's own rule, so this
	// only matters for one older than every attempt read above.
	for _, a := range open {
		if a.Scope == store.UpdateScopeHost {
			latest[a.HostID] = a
		}
	}
	c.updates.hostMu.Lock()
	c.updates.hostLast = latest
	c.updates.hostMu.Unlock()

	// An attempt can end without passing through closeHostAttempt: the host's
	// row deleted with it, or another process closing it. What is remembered of
	// it goes when it is no longer open, whichever way it ended.
	stillOpen := make(map[string]bool, len(open))
	for _, a := range open {
		stillOpen[a.ID] = true
	}
	c.updates.handedMu.Lock()
	for id := range c.updates.handed {
		if !stillOpen[id] {
			delete(c.updates.handed, id)
		}
	}
	c.updates.handedMu.Unlock()
}

// hostAttempt is the latest attempt the loop saw for a host.
func (c *Controller) hostAttempt(hostID string) (store.UpdateAttempt, bool) {
	c.updates.hostMu.RLock()
	defer c.updates.hostMu.RUnlock()
	a, ok := c.updates.hostLast[hostID]
	return a, ok
}

// forgetHostUpdates drops a deleted host from what the loop knows. Its open
// attempt was cancelled with the row, in the same transaction.
func (c *Controller) forgetHostUpdates(hostID string) {
	c.updates.hostMu.Lock()
	delete(c.updates.hostLast, hostID)
	delete(c.updates.hostUnsupported, hostID)
	c.updates.hostMu.Unlock()
	c.updates.heldMu.Lock()
	delete(c.updates.heldWarned, hostID)
	c.updates.heldMu.Unlock()
}

// hostUpdateView renders a host's part in updating from what the loop last saw.
func (c *Controller) hostUpdateView(h *store.Host) *HostUpdateView {
	target := hostTarget()
	can, why, gap := hostCanSelfUpdate(h, target, c.hostHelperUnsupported(h), c.updateMode())
	out := &HostUpdateView{State: HostUpdateNone, Reason: why, CanUpdate: can}
	if gap == hostGapHelperImpossible {
		out.State = HostUpdateUnsupported
	}
	a, ok := c.hostAttempt(h.ID)
	if !ok {
		return out
	}
	switch a.State {
	case store.UpdateRequested:
		out.State, out.AttemptID, out.CanUpdate = a.State, a.ID, false
		out.Reason = "The update to " + a.ToVersion + " has been asked for. The host is updated once its agent reports " + a.ToVersion + "."
	case store.UpdateSucceeded:
		out.State, out.AttemptID = a.State, a.ID
	case store.UpdateFailed, store.UpdateTimedOut, store.UpdateCancelled:
		// Once the host is on that release some other way, the failure is
		// history and the card says what is true now.
		if runsAtLeast(h.Version, a.ToVersion) {
			return out
		}
		out.State, out.AttemptID = a.State, a.ID
		out.Reason = withoutDirectionControls(a.Error)
		if strings.TrimSpace(out.Reason) == "" {
			out.Reason = withheldAttemptError(a.State)
		}
	}
	return out
}
