package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

// updatesInterval is how often the update loop looks when nothing has woken it.
// It is short because somebody is usually watching: a helper that refuses a
// request, or an upgrade whose pre-flight fails, answers in seconds, and the page
// should say so in seconds. A pass with nothing open is one query.
const updatesInterval = 10 * time.Second

// updatesState is the update loop's own memory: the wake-up flag, the lock that
// keeps two passes from overlapping, and whether the next pass should read the
// release list first.
type updatesState struct {
	kick   chan struct{}
	passMu sync.Mutex
	// checkReleases is set when updating is turned on. The scheduled check may be
	// a day away, and until the list is read the status can only say that it has
	// not been, so the next pass reads it.
	checkReleases atomic.Bool

	// lookMu keeps two lookers (a pass and a press of the button) from reading the
	// database in one order and storing in the other, which would leave the older
	// answer as the one the problems list sees.
	lookMu sync.Mutex
	// sightMu guards sight, which the problems pass reads after every scheduling
	// pass and so must never wait on a read of the database or the folder.
	sightMu sync.Mutex
	sight   updateSight
}

// updateSight is what the update loop last saw of the things the problems list
// reports on. The list is worked out after every scheduling pass, and a probe of
// the update folder and a query for the last attempt do not belong in that path;
// the loop looks every ten seconds and whenever an attempt opens, so this is at
// most that stale.
type updateSight struct {
	// looked says the loop has looked at all. Before it has, nothing is known and
	// nothing is raised: a controller that has only just started has not seen a
	// helper missing.
	looked        bool
	helperMissing bool
	// last is the controller's latest update attempt, open or ended, or nil.
	last *store.UpdateAttempt
}

func newUpdatesState() updatesState {
	return updatesState{kick: make(chan struct{}, 1)}
}

// KickUpdates asks for a pass of the update loop now. It never blocks and never
// queues, like Nudge.
func (c *Controller) KickUpdates() {
	select {
	case c.updates.kick <- struct{}{}:
	default:
	}
}

// updatesLoop watches the attempts in flight, on a timer and whenever something
// asks. It runs whatever updates.mode says: switching updating off stops new
// requests, and an attempt already in flight still has to be closed.
//
// It is a loop of its own, apart from the scheduling pass, because nothing here
// may wait on reconcileMu: a pass holds it for as long as GitHub takes to answer.
func (c *Controller) updatesLoop(ctx context.Context) {
	ticker := time.NewTicker(updatesInterval)
	defer ticker.Stop()
	// At once, because a controller that has just started may be the new build an
	// update asked for, and the page that asked is waiting to hear.
	c.runUpdates(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.updates.kick:
		}
		c.runUpdates(ctx)
	}
}

func (c *Controller) runUpdates(ctx context.Context) {
	if err := c.ReconcileUpdates(ctx); err != nil && ctx.Err() == nil {
		c.log.Error("the update attempts could not be checked; the next pass will try again", "error", err)
	}
}

// ReconcileUpdates runs one pass: read the release list if updating has just
// been turned on, then close every open attempt that has ended. It is exported so
// that tests can force a pass and know it has finished.
//
// Every attempt is judged on what can be seen now, so a pass missed, run twice,
// or run by the next process after a restart comes to the same answer, and the
// store keeps the first ending if two passes race.
func (c *Controller) ReconcileUpdates(ctx context.Context) error {
	c.updates.passMu.Lock()
	defer c.updates.passMu.Unlock()
	// Last, so that the problems list sees the attempts this pass closed. A
	// controller that may not act still looks: what it reports is what is there.
	defer c.lookAtUpdates(ctx)

	// Reading the list changes nothing in the fleet, so a controller that may not
	// act still does it: the status it shows should not be a day stale.
	if c.updates.checkReleases.Swap(false) && c.updateMode() != updates.ModeOff {
		asked, err := c.askForReleases(ctx)
		switch {
		case err != nil:
			if !errors.Is(err, ErrUpdateCheckDisabled) {
				c.log.Info("could not read the release list after updating was turned on; the scheduled check will try again", "error", err)
			}
		case !asked:
			// A press of the button took the minute, perhaps for the latest release
			// alone while updating was still off. The list is still unread, so the
			// flag stays, and a pass after the minute asks.
			c.updates.checkReleases.Store(true)
		default:
			if _, err := c.publishUpdates(ctx); err != nil {
				c.log.Warn("could not work out the update status for the event stream", "error", err)
			}
		}
	}

	if !c.mayAct() {
		return nil
	}
	open, err := c.st.OpenUpdateAttempts(ctx)
	if err != nil {
		return fmt.Errorf("listing the update attempts in flight: %w", err)
	}
	closed := false
	for _, a := range open {
		state, text, ended := c.updateOutcome(a)
		if !ended {
			continue
		}
		if c.finishUpdateAttempt(ctx, a, state, text) {
			closed = true
			if a.Scope == store.UpdateScopeController {
				c.withdrawRequest(a)
			}
		}
	}
	if closed {
		if _, err := c.publishUpdates(ctx); err != nil {
			c.log.Warn("could not work out the update status for the event stream", "error", err)
		}
	}
	return nil
}

// lookAtUpdates refreshes what the problems list reports on: whether the helper
// is installed, and how the controller's latest attempt stands.
//
// A failed read of the attempts keeps the last one seen. Clearing it would drop
// an error the operator has not yet read because the database was busy.
func (c *Controller) lookAtUpdates(ctx context.Context) {
	c.updates.lookMu.Lock()
	defer c.updates.lookMu.Unlock()

	missing := c.probeUpdateHelper().view.State == HelperMissing
	attempts, err := c.st.ListUpdateAttempts(ctx, store.UpdateScopeController, "", 1)
	if err != nil && ctx.Err() == nil {
		c.log.Warn("could not read the controller's latest update attempt for the problems list; the next pass will try again", "error", err)
	}

	c.updates.sightMu.Lock()
	defer c.updates.sightMu.Unlock()
	c.updates.sight.looked = true
	c.updates.sight.helperMissing = missing
	if err != nil {
		return
	}
	c.updates.sight.last = nil
	if len(attempts) > 0 {
		last := attempts[0]
		c.updates.sight.last = &last
	}
}

// updateSightNow is a copy of what the loop last saw.
func (c *Controller) updateSightNow() updateSight {
	c.updates.sightMu.Lock()
	defer c.updates.sightMu.Unlock()
	return c.updates.sight
}

// updateOutcome says whether an open attempt has ended, and how.
//
// The time-out is the last word and is checked after everything else, so an
// answer that arrived in the same pass as the ninetieth minute is the one kept.
// It needs no mode: an attempt asked for before updating was switched off is
// still in flight, and still has to be recorded however it ends.
func (c *Controller) updateOutcome(a store.UpdateAttempt) (state, text string, ended bool) {
	if a.Scope == store.UpdateScopeController {
		if state, text, ended := c.controllerOutcome(a); ended {
			return state, text, true
		}
	}
	if c.Now().Sub(a.RequestedAt) <= updateAttemptTimeout {
		return "", "", false
	}
	if a.Scope == store.UpdateScopeController {
		return store.UpdateTimedOut, fmt.Sprintf("No answer came from the update helper within 90 minutes, and this controller still runs %s. "+
			"Look at journalctl -u zoomies-update and zoomies updates helper status on the controller's host.", version.Version), true
	}
	return store.UpdateTimedOut, fmt.Sprintf("The host did not come back on %s within 90 minutes. "+
		"Look at journalctl -u zoomies-update and zoomies updates helper status on the host.", a.ToVersion), true
}

// controllerOutcome is how the controller's own attempt ended, if it has.
//
// Success is this process running the tag or something later. The helper's word
// alone is not enough, since a result can say done while the old build is still
// the one running; and its word is not needed either, because the new process
// may start before the result is written, and is itself the proof. For the same
// reason a failure the helper reported is not believed over a process that
// already runs the target: whatever it complained of, the update took, and
// "still runs the old build" would be untrue of it.
func (c *Controller) controllerOutcome(a store.UpdateAttempt) (state, text string, ended bool) {
	arrived := runsAtLeast(version.Version, a.ToVersion)
	res, answered := c.resultFor(a)
	switch {
	case arrived:
		if answered && !res.OK {
			c.log.Warn("the update helper reported a failure, but this controller runs the release it was asked for, so the attempt is recorded as succeeded",
				"attempt", a.ID, "to", a.ToVersion, "running", version.Version, "helper", cutAt(res.Error, maxHelperSentence))
		}
		return store.UpdateSucceeded, "", true
	case !answered:
		return "", "", false
	case res.OK:
		return store.UpdateFailed, fmt.Sprintf("The update helper says %s is installed, but this controller still runs %s, so the new release is not the one running. "+
			"Restart zoomies on the controller's host, and look at journalctl -u zoomies-update there if it does not come back on %s.",
			a.ToVersion, version.Version, a.ToVersion), true
	}
	if msg := strings.TrimSpace(cutAt(res.Error, maxHelperSentence)); msg != "" {
		return store.UpdateFailed, msg, true
	}
	return store.UpdateFailed, "The update helper says the update failed, and gave no reason. Look at journalctl -u zoomies-update on the controller's host.", true
}

// runsAtLeast says whether running is the release tag names or a later one.
// Release binaries report 1.3.5 where the tag is v1.3.5, so only CompareBuilds
// can say whether the two are the same release.
func runsAtLeast(running, tag string) bool {
	if strings.TrimSpace(running) == "" || strings.TrimSpace(tag) == "" {
		return false
	}
	switch version.CompareBuilds(running, tag) {
	case version.SkewNone, version.SkewAhead:
		return true
	}
	return false
}

// resultFor is the helper's answer to attempt a, if the folder holds one.
//
// Anything else in result.json is ignored, quietly: a result left from before a
// restart, an answer to an attempt already closed, a file that is not a result.
// The service can write the folder, so nothing in it is evidence of anything but
// what the helper is showing; it only ever closes the attempt it answers.
//
// One answer carries no id. The helper refuses a request it cannot trust (one it
// cannot parse, or one owned by another account) without echoing an id it has
// no reason to believe. Only one attempt is open at a time and only this service
// writes requests, so a refusal without an id written after this attempt was
// asked for can only be the answer to it; ignored, it would leave the attempt to
// time out after 90 minutes with no reason given.
func (c *Controller) resultFor(a store.UpdateAttempt) (updates.Result, bool) {
	dir, ok := c.updateDir()
	if !ok {
		return updates.Result{}, false
	}
	res, found, err := channel.ReadResult(dir)
	if err != nil {
		c.log.Debug("ignoring a result in the update folder that cannot be read", "error", err)
		return updates.Result{}, false
	}
	switch {
	case !found:
		return updates.Result{}, false
	case res.ID == a.ID:
		return res, true
	case res.ID == "" && !res.OK && res.FinishedAt.After(a.RequestedAt):
		return res, true
	}
	return updates.Result{}, false
}

// maxHelperSentence bounds what of the helper's error an attempt keeps. The
// result file may carry far more, and the row is read on every status; a
// sentence an operator reads is a few hundred bytes, and the log tail is where
// the rest is.
const maxHelperSentence = 2048

// cutAt is s cut to at most limit bytes, at a character boundary.
func cutAt(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// withdrawRequest takes back the controller's own request once its attempt has
// ended without the helper taking it, so that the next press is not refused by
// a request nobody will read. Anything there that is not provably this
// attempt's request is left for a person.
func (c *Controller) withdrawRequest(a store.UpdateAttempt) {
	dir, ok := c.updateDir()
	if !ok {
		return
	}
	switch got, err := channel.WithdrawRequest(dir, a.ID); {
	case err != nil:
		c.log.Warn("could not take back the request of an update attempt that has ended; remove request.json from the update folder by hand",
			"attempt", a.ID, "error", err)
	case got == channel.RequestWithdrawn:
		c.log.Info("took back the request of an update attempt that has ended, which the update helper never read", "attempt", a.ID)
	case got == channel.RequestNotOurs:
		c.log.Warn("the update folder holds a request.json that is not this attempt's, so it was left alone; the next update is refused until it is gone",
			"attempt", a.ID)
	}
}

// finishUpdateAttempt closes an open attempt and counts it, and says whether it
// was this call that closed it: the store keeps the first ending, so a pass and
// a button racing count one.
func (c *Controller) finishUpdateAttempt(ctx context.Context, a store.UpdateAttempt, state, text string) bool {
	closed, err := c.st.FinishUpdateAttempt(ctx, a.ID, state, text)
	if err != nil {
		c.log.Warn("could not record how an update attempt ended; the next pass will try again", "attempt", a.ID, "error", err)
		return false
	}
	if !closed {
		return false
	}
	c.metrics.updateAttempts.WithLabelValues(a.Scope, state).Inc()
	if state == store.UpdateSucceeded {
		c.log.Info("an update attempt succeeded", "attempt", a.ID, "scope", a.Scope, "host", a.HostID, "to", a.ToVersion)
	} else {
		c.log.Warn("an update attempt did not succeed", "attempt", a.ID, "scope", a.Scope, "host", a.HostID,
			"to", a.ToVersion, "state", state, "reason", text)
	}
	return true
}

// renderUpdates is UpdatesView. It is a variable so that a test can make the
// status fail to render after a request has been written.
var renderUpdates = (*Controller).UpdatesView

// publishUpdates sends the update status now, if it differs from what was last
// sent, and returns it. A request and a closed attempt write rows, and the page
// that asked should not wait for the next pass to hear of them.
//
// The status is worked out with derivedMu held, as publishDerived works out its
// own: two publishers that each rendered first and then waited for the lock
// could send an older status after a newer one, and the page keeps whichever
// frame came last.
func (c *Controller) publishUpdates(ctx context.Context) (*UpdatesView, error) {
	c.derivedMu.Lock()
	defer c.derivedMu.Unlock()
	view, err := renderUpdates(c, ctx)
	if err != nil {
		return nil, err
	}
	c.sendUpdates(view)
	return view, nil
}

// sendUpdates publishes a status that differs from the one last sent. Called
// with derivedMu held, and with a status rendered under it.
func (c *Controller) sendUpdates(view *UpdatesView) {
	if c.bus == nil {
		return
	}
	if raw, changed := c.derivedChanged(&c.lastUpdates, view); changed {
		c.bus.Publish(events.KindUpdates, "", json.RawMessage(raw))
	}
}
