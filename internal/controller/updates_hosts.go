package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// reached its release some other way.
	State string `json:"state"`
	// Reason is the sentence the card shows. Below the platform role a failed
	// attempt's reason is a fixed sentence for the state, because the text can
	// be the helper's own and name a path on the host (see HostView.For).
	Reason string `json:"reason"`
	// CanUpdate says whether asking for an update now would be taken.
	CanUpdate bool `json:"can_update"`
	// AttemptID is the attempt State describes, and empty with none.
	AttemptID string `json:"attempt_id"`

	// detail is the attempt's own reason, kept for the platform role. It is
	// unexported so that every frame and every response carries the fixed
	// sentence unless a caller that knows the role asks for more.
	detail string
}

// HostUpdateNone is the state of a host with no attempt worth showing.
const HostUpdateNone = "none"

// For is the host as one audience may read it. Every view is rendered with the
// fixed sentence in place of an attempt's own reason, so a frame on the stream,
// a diagnostics bundle or a handler that forgets to ask never carries the
// helper's text; only a caller that knows its reader holds the platform role
// gets it back.
//
// It narrows a copy, because the update block it points to is shared.
func (v HostView) For(platform bool) HostView {
	if !platform || v.Update == nil || v.Update.detail == "" {
		return v
	}
	u := *v.Update
	u.Reason = u.detail
	v.Update = &u
	return v
}

// hostCanSelfUpdate is the single place that says whether a host can be updated
// from here, and if not, why, in a sentence for the card. The view, the request
// and the planner all ask it, so the button and the planner never disagree.
//
// target is the release the host would be taken to, empty when this controller
// is not a release.
func hostCanSelfUpdate(h *store.Host, target string) (can bool, why string) {
	switch {
	case h.Embedded:
		return false, "This is the agent inside the controller, so it is updated with the controller, from Settings → Updates."
	case target == "":
		return false, "This controller is not running a release, so there is no release to take its hosts to. Install a release on the controller with zoomies upgrade first."
	case strings.TrimSpace(h.Version) == "":
		return false, "This host's agent has not said which version it runs, so Zoomies cannot tell whether " + target + " is newer. Update it on the host with the command below."
	}
	switch version.CompareBuilds(h.Version, target) {
	case version.SkewNone, version.SkewAhead:
		return false, "This host already runs " + target + " or a later release, so there is nothing to update."
	case version.SkewDiffers:
		return false, "This host runs the build " + reportedVersion(h.Version) + ", which is not a release, so Zoomies cannot tell whether " +
			target + " is newer. Update it on the host with the command below."
	}
	if !h.Supports(agent.FeatureSelfUpdate) {
		return false, "This host's agent does not offer to update itself, which it does only once the update helper is installed on the host. " +
			"Run sudo zoomies updates helper install there, or update it with the command below."
	}
	return true, "This host runs " + reportedVersion(h.Version) + " and can be updated to " + target + "."
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
	if !c.mayAct() {
		return nil, fmt.Errorf("%w: %s", ErrUpdateFenced, c.notActingReason())
	}
	if c.updateMode() == updates.ModeOff {
		return nil, ErrUpdateModeOff
	}
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	target := hostTarget()
	if target == "" {
		return nil, ErrUpdateNotARelease
	}
	if can, why := hostCanSelfUpdate(h, target); !can {
		return nil, &hostCannotUpdateError{sentence: why}
	}

	attempt := &store.UpdateAttempt{
		Scope: store.UpdateScopeHost, HostID: h.ID, FromVersion: h.Version, ToVersion: target,
		Trigger: store.UpdateTriggerManual, RequestedBy: requestedBy(by),
	}
	if err := c.st.CreateUpdateAttempt(ctx, attempt); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, ErrUpdateInProgress
		}
		return nil, fmt.Errorf("recording the update attempt: %w", err)
	}
	c.log.Info("asked a host's agent to update it", "attempt", attempt.ID, "host", h.ID, "name", h.Name,
		"from", attempt.FromVersion, "to", target, "requested_by", attempt.RequestedBy)
	// Seen before the task is queued, so that a result coming back at once finds
	// the attempt it answers.
	c.lookAtHostUpdates(context.WithoutCancel(ctx))
	c.sendHostUpdate(h, *attempt)
	c.publishHost(h)
	view := c.HostView(h)
	return &view, nil
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

// withdrawHostUpdate takes an ended attempt's task off its host's queue if it is
// still waiting there, so that a host that comes back after its attempt timed
// out is not then updated with nothing recording it.
func (c *Controller) withdrawHostUpdate(a store.UpdateAttempt) {
	c.updates.handedMu.Lock()
	delete(c.updates.handed, a.ID)
	c.updates.handedMu.Unlock()
	q, ok := c.queues.all()[a.HostID]
	if !ok {
		return
	}
	q.withdraw(func(t agent.Task) bool { return t.Kind == agent.TaskUpdateAgent && t.UpdateID == a.ID })
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
	c.closeHostAttempt(ctx, latest[0], store.UpdateFailed, text)
}

// noteHostUpdate is what a heartbeat says about the host's open attempt: the
// host running the release, which is the only proof of success, or the helper's
// answer relayed by the agent.
//
// The open attempt is the one the loop last saw, so a beat with nothing to say
// reads nothing. A beat that carries a result reads the store instead, because
// the agent sends a result until a beat has been answered and not again: one
// matched against a stale picture would be lost.
func (c *Controller) noteHostUpdate(ctx context.Context, h *store.Host, rep *agent.UpdateReport) {
	if !c.mayAct() {
		return
	}
	a, ok := c.hostAttempt(h.ID)
	if rep != nil {
		latest, err := c.st.ListUpdateAttempts(ctx, store.UpdateScopeHost, h.ID, 1)
		if err != nil {
			c.log.Warn("could not read a host's update attempt to match the result its agent reported", "host", h.ID, "error", err)
		} else if len(latest) == 1 {
			a, ok = latest[0], true
		}
	}
	if !ok || a.State != store.UpdateRequested {
		// A report with no open attempt of this host's to answer is ignored: an
		// agent relays whatever its update folder holds, which can be a result
		// for an attempt already closed, or for the controller itself where the
		// two share a folder.
		return
	}
	state, text, ended := hostReportOutcome(a, h.Version, rep)
	if !ended {
		return
	}
	if c.closeHostAttempt(ctx, a, state, text) {
		c.publishHost(h)
	}
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
// says whether this call was the one that closed it.
func (c *Controller) closeHostAttempt(ctx context.Context, a store.UpdateAttempt, state, text string) bool {
	if !c.finishUpdateAttempt(ctx, a, state, text) {
		return false
	}
	c.withdrawHostUpdate(a)
	c.lookAtHostUpdates(ctx)
	return true
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
	c.updates.hostMu.Unlock()
}

// hostUpdateView renders a host's part in updating from what the loop last saw.
func (c *Controller) hostUpdateView(h *store.Host) *HostUpdateView {
	target := hostTarget()
	can, why := hostCanSelfUpdate(h, target)
	out := &HostUpdateView{State: HostUpdateNone, Reason: why, CanUpdate: can}
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
		out.Reason = withheldAttemptError(a.State)
		out.detail = withoutDirectionControls(a.Error)
	}
	return out
}
