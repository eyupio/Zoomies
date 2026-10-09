package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// autoFleet is a controller on v1.3.5, the newest release and long public, in
// auto with its helper ready: nothing to do for the controller, so whatever the
// planner does is for its hosts.
func (h *harness) autoFleet(mode string) {
	h.t.Helper()
	withVersion(h.t, "1.3.5")
	h.inMode(mode)
	h.readTheList(releaseEntry("v1.3.5", whenAgo(10*24*time.Hour), rolloutAssets(h.t)...))
	h.installHelper()
}

// rolloutAssets is a release complete for the system the tests run on, for the
// controller, and for linux/amd64, which is what every seeded host says it is.
func rolloutAssets(t *testing.T) []string {
	t.Helper()
	return append(completeAssets(t), updates.AssetName("linux", "amd64"))
}

// openRollout is the rollout that is running or halted, or nil.
func (h *harness) openRollout() *store.UpdateRollout {
	h.t.Helper()
	r, err := h.st.OpenUpdateRollout(h.ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		h.t.Fatalf("OpenUpdateRollout: %v", err)
	}
	return r
}

func (h *harness) lastRollout() *store.UpdateRollout {
	h.t.Helper()
	r, err := h.st.LastEndedUpdateRollout(h.ctx)
	if err != nil {
		h.t.Fatalf("LastEndedUpdateRollout: %v", err)
	}
	return r
}

// openHostAttempt is the open attempt of one host, failing the test without one.
func (h *harness) openHostAttempt(host *store.Host) store.UpdateAttempt {
	h.t.Helper()
	for _, a := range h.openAttempts() {
		if a.Scope == store.UpdateScopeHost && a.HostID == host.ID {
			return a
		}
	}
	h.t.Fatalf("no open attempt for %s: %+v", host.Name, h.openAttempts())
	return store.UpdateAttempt{}
}

// noAttemptFor says a host has never been asked to update.
func (h *harness) noAttemptFor(host *store.Host) {
	h.t.Helper()
	if got, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeHost, host.ID, 10); err != nil || len(got) != 0 {
		h.t.Fatalf("%s was asked to update (%v): %+v", host.Name, err, got)
	}
}

// startedRollout is two passes in auto with hosts behind: the first starts the
// rollout and the second asks its first host.
func (h *harness) startedRollout() *store.UpdateRollout {
	h.t.Helper()
	h.pass(h.c)
	r := h.openRollout()
	if r == nil {
		h.t.Fatalf("no rollout started; the status says %q", h.status().Reason)
	}
	h.pass(h.c)
	return r
}

func drainKick(c *Controller) {
	select {
	case <-c.updates.kick:
	default:
	}
}

func kicked(c *Controller) bool {
	select {
	case <-c.updates.kick:
		return true
	default:
		return false
	}
}

func TestTheLoopStartsARolloutAndUpdatesOneHostThenTheNext(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	first := h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")

	h.pass(h.c)
	r := h.openRollout()
	if r == nil || r.Target != "v1.3.5" || r.Trigger != store.UpdateTriggerAuto || r.State != store.RolloutRunning || len(r.HostIDs) != 0 {
		t.Fatalf("after the first pass the rollout = %+v, want auto's rollout of every host to v1.3.5", r)
	}
	// The rollout opens before any host is asked, so that the first update is
	// a step of something an operator can see, halt and cancel.
	if open := h.openAttempts(); len(open) != 0 {
		t.Fatalf("the pass that started the rollout also asked %+v", open)
	}

	h.pass(h.c)
	a := h.openHostAttempt(first)
	if a.RolloutID != r.ID || a.Trigger != store.UpdateTriggerAuto || a.ToVersion != "v1.3.5" {
		t.Errorf("the first host's attempt = %+v, want auto's, a step of %s, to v1.3.5", a, r.ID)
	}
	if got := updateTasks(h.poll(first)); len(got) != 1 || got[0].UpdateID != a.ID {
		t.Fatalf("vm-a was handed %+v, want its update", got)
	}

	// One at a time: nothing for the second host while the first is open,
	// however many passes run, and even while the first has gone quiet, as a
	// host restarting into its update does.
	h.pass(h.c)
	h.advance(2 * time.Minute)
	h.agentBeat(second, "1.3.4", nil)
	h.pass(h.c)
	h.noAttemptFor(second)
	if got := updateTasks(h.poll(second)); len(got) != 0 {
		t.Fatalf("vm-b was handed %+v while vm-a was updating", got)
	}

	h.advance(time.Minute)
	// vm-a comes back on the release. The heartbeat closes its attempt and
	// wakes the loop, so the next host does not wait for the timer.
	drainKick(h.c)
	h.agentBeat(first, "1.3.5", nil)
	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
		t.Fatalf("vm-a's attempt is %s after it reported v1.3.5, want succeeded", got.State)
	}
	if !kicked(h.c) {
		t.Error("a host arriving on a new version did not wake the update loop")
	}
	h.pass(h.c)
	b := h.openHostAttempt(second)
	if b.RolloutID != r.ID {
		t.Errorf("vm-b's attempt belongs to %q, want %s", b.RolloutID, r.ID)
	}

	h.agentBeat(second, "1.3.5", nil)
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Fatalf("the rollout is still open with every host on v1.3.5: %+v", got)
	}
	if got := h.lastRollout(); got.ID != r.ID || got.State != store.RolloutDone {
		t.Errorf("the rollout ended as %+v, want done", got)
	}
	// Nothing more to do, and nothing done: a finished rollout is not followed by
	// another.
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Errorf("a fleet on the release started %+v", got)
	}
}

// failOn is the host's agent relaying its helper's answer that the update
// failed, with the host still on the old build.
func (h *harness) failOn(host *store.Host, a store.UpdateAttempt) {
	h.t.Helper()
	h.agentBeat(host, "1.3.4", &agent.UpdateReport{ID: a.ID, OK: false, Tag: a.ToVersion,
		Error: "the checksum of /var/lib/zoomies-update/zoomies did not match", FinishedAt: h.c.Now().UTC()})
	if got := h.attempt(a.ID); got.State != store.UpdateFailed {
		h.t.Fatalf("the attempt is %s after its failure was reported, want failed", got.State)
	}
}

func TestARolloutHaltsOnTheFirstFailureAndResumesWhenAnOperatorSaysSo(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	first := h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")
	r := h.startedRollout()
	drainKick(h.c)
	h.failOn(first, h.openHostAttempt(first))
	// The version did not move, so it is the attempt closing that wakes the loop.
	if !kicked(h.c) {
		t.Error("an attempt closing did not wake the update loop")
	}

	h.advance(time.Minute)
	h.pass(h.c)
	halted := h.openRollout()
	if halted == nil || halted.State != store.RolloutHalted {
		t.Fatalf("after a failure the rollout = %+v, want halted", halted)
	}
	// It names the host so that a person knows where to look, and not the
	// helper's text, which can name a path on the host and is read by every role.
	if !strings.Contains(halted.HaltedReason, "vm-a") || strings.Contains(halted.HaltedReason, "/var/lib") {
		t.Errorf("halted reason = %q, want vm-a named and no path", halted.HaltedReason)
	}

	// Halted starts nothing, however many passes run.
	h.advance(5 * time.Minute)
	h.agentBeat(first, "1.3.4", nil)
	h.agentBeat(second, "1.3.4", nil)
	h.pass(h.c)
	h.pass(h.c)
	h.noAttemptFor(second)
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, nil); !errors.Is(err, ErrUpdateRolloutHalted) {
		t.Errorf("starting a rollout over a halted one: err = %v, want ErrUpdateRolloutHalted", err)
	}

	h.advance(time.Minute)
	if _, _, err := h.c.ResumeRollout(h.ctx, alice); err != nil {
		t.Fatalf("ResumeRollout: %v", err)
	}
	h.advance(time.Minute)
	h.agentBeat(first, "1.3.4", nil)
	h.agentBeat(second, "1.3.4", nil)
	h.pass(h.c)
	// The failure it was halted on is before the resume, so it does not halt it
	// again, and the rollout goes on to the next host while vm-a waits out the
	// half hour after its failure.
	if got := h.openRollout(); got == nil || got.ID != r.ID || got.State != store.RolloutRunning {
		t.Fatalf("after the resume the rollout = %+v, want %s running", got, r.ID)
	}
	h.openHostAttempt(second)

	// A second failure, after the resume, halts it again.
	h.failOn(second, h.openHostAttempt(second))
	h.advance(time.Minute)
	h.pass(h.c)
	if got := h.openRollout(); got == nil || got.State != store.RolloutHalted || !strings.Contains(got.HaltedReason, "vm-b") {
		t.Errorf("after vm-b failed the rollout = %+v, want halted on vm-b", got)
	}
}

// A restart empties every queue. The rows say where the rollout stands, so the
// next process carries on from them: the host on the release is recorded as
// done and the next host is asked.
func TestARestartMidRolloutCarriesOn(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	first := h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")
	r := h.startedRollout()
	a := h.openHostAttempt(first)
	if got := updateTasks(h.poll(first)); len(got) != 1 {
		t.Fatalf("vm-a was handed %d update tasks, want 1", len(got))
	}

	// The settings live in the database, which the harness does not keep; the
	// new process is told the mode the old one ran in.
	h.c = h.restart()
	h.inMode("auto")
	h.agentBeat(first, "1.3.5", nil)
	h.pass(h.c)

	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
		t.Errorf("vm-a's attempt is %s after the restart, want succeeded", got.State)
	}
	b := h.openHostAttempt(second)
	if b.RolloutID != r.ID {
		t.Errorf("vm-b's attempt is a step of %q, want the rollout from before the restart, %s", b.RolloutID, r.ID)
	}
	if got := updateTasks(h.poll(second)); len(got) != 1 || got[0].UpdateID != b.ID {
		t.Errorf("vm-b was handed %+v after the restart, want its update", got)
	}
}

// Review Focus 3: a host deleted mid-rollout will never report. Its attempt
// is cancelled with it, which is not a failure, and the rollout goes on.
func TestAHostDeletedMidRolloutDoesNotHangTheRollout(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	first := h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")
	r := h.startedRollout()
	a := h.openHostAttempt(first)

	drainKick(h.c)
	if err := h.c.DeleteHost(h.ctx, first.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if got := h.attempt(a.ID); got.State != store.UpdateCancelled {
		t.Errorf("the deleted host's attempt is %s, want cancelled", got.State)
	}
	if !kicked(h.c) {
		t.Error("deleting a host with an update open did not wake the update loop")
	}
	h.pass(h.c)
	if got := h.openRollout(); got == nil || got.State != store.RolloutRunning {
		t.Fatalf("the rollout = %+v, want it still running", got)
	}
	h.openHostAttempt(second)

	h.agentBeat(second, "1.3.5", nil)
	h.pass(h.c)
	if got := h.lastRollout(); got.ID != r.ID || got.State != store.RolloutDone {
		t.Errorf("the rollout ended as %+v, want done", got)
	}
}

// Review Focus 5: switching updating off stops what has not started, and lets
// what has finish and be recorded.
func TestSwitchingToOffCancelsAPendingRolloutButLetsAnInFlightAttemptFinish(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	first := h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")
	r := h.startedRollout()
	a := h.openHostAttempt(first)

	h.inMode("off")
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Fatalf("the rollout is still open with updating off: %+v", got)
	}
	last := h.lastRollout()
	if last.ID != r.ID || last.State != store.RolloutCancelled || last.CancelledBy != "" {
		t.Errorf("the rollout ended as %+v, want cancelled by the planner, not by a person", last)
	}
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Fatalf("vm-a's attempt is %s, want it still in flight", got.State)
	}

	h.agentBeat(first, "1.3.5", nil)
	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
		t.Errorf("vm-a's attempt is %s after it reported the release, want it recorded as succeeded", got.State)
	}
	h.pass(h.c)
	h.noAttemptFor(second)
	if got := h.status(); got.Rollout == nil || got.Rollout.State != store.RolloutCancelled || !strings.Contains(got.Reason, "off") {
		t.Errorf("the status says %q with rollout %+v, want the cancelled rollout and why nothing moves", got.Reason, got.Rollout)
	}
}

// R4: the controller audits only what the planner did on its own, and as the
// auto-update actor, so an operator reading the log can tell an update nobody
// pressed for.
func TestAutoUpdatesAreAuditedAsTheAutoUpdateActor(t *testing.T) {
	t.Run("a rollout", func(t *testing.T) {
		h := newHarness(t)
		h.autoFleet("auto")
		host := h.updatableHost("vm-a")
		r := h.startedRollout()
		a := h.openHostAttempt(host)
		if a.RequestedBy != "zoomies auto-update" || r.StartedBy != "zoomies auto-update" {
			t.Errorf("requested by %q and started by %q, want the auto-update actor's name", a.RequestedBy, r.StartedBy)
		}
		h.agentBeat(host, "1.3.5", nil)
		h.pass(h.c)
		for action, target := range map[string]string{
			"update.rollout_started": r.ID, "update.host_requested": a.ID, "update.rollout_finished": r.ID,
		} {
			assertAutoAudited(t, h, action, target)
		}
	})
	t.Run("the controller", func(t *testing.T) {
		h := newHarness(t)
		withVersion(t, "1.3.4")
		h.inMode("auto")
		h.readTheList(releaseEntry("v1.3.5", whenAgo(48*time.Hour), rolloutAssets(t)...))
		h.installHelper()
		h.pass(h.c)
		open := h.openAttempts()
		if len(open) != 1 || open[0].Scope != store.UpdateScopeController || open[0].Trigger != store.UpdateTriggerAuto ||
			open[0].RequestedBy != "zoomies auto-update" {
			t.Fatalf("open attempts = %+v, want auto's request for the controller", open)
		}
		req := h.takeRequest()
		if req.ID != open[0].ID || req.Tag != "v1.3.5" || req.RequestedBy != "zoomies auto-update" {
			t.Errorf("the request = %+v, want the attempt's, to v1.3.5, from the auto-update actor", req)
		}
		assertAutoAudited(t, h, "update.controller_requested", open[0].ID)
	})
}

func assertAutoAudited(t *testing.T, h *harness, action, target string) {
	t.Helper()
	rows := h.audits(action)
	if len(rows) != 1 {
		t.Fatalf("%d %s rows, want 1", len(rows), action)
	}
	if e := rows[0]; e.ActorKind != "system" || e.ActorID != "auto-update" || e.ActorName != "zoomies auto-update" || e.TargetID != target {
		t.Errorf("%s audited as %+v, want the auto-update actor on %s", action, e, target)
	}
}

// R4: a person's start, resume and cancel are audited by the handler that took
// them, with the caller's identity. The controller only writes down who, so a
// press is one audit row and not two.
func TestAnOperatorsRolloutRecordsTheOperatorAndWritesNoAuditRowOfItsOwn(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("manual")
	h.updatableHost("vm-a")

	view, _, err := h.c.StartHostRollout(h.ctx, alice, nil)
	if err != nil {
		t.Fatalf("StartHostRollout: %v", err)
	}
	r := h.openRollout()
	if r == nil || r.Trigger != store.UpdateTriggerManual || r.StartedBy != "alice" {
		t.Fatalf("the rollout = %+v, want alice's, by hand", r)
	}
	if view.Rollout == nil || view.Rollout.ID != r.ID {
		t.Errorf("the status returned carries rollout %+v, want %s", view.Rollout, r.ID)
	}
	if _, err := h.st.HaltUpdateRollout(h.ctx, r.ID, "vm-a did not come back"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.c.ResumeRollout(h.ctx, alice); err != nil {
		t.Fatalf("ResumeRollout: %v", err)
	}
	if _, _, err := h.c.CancelRollout(h.ctx, alice); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}
	if got := h.lastRollout(); got.ID != r.ID || got.State != store.RolloutCancelled || got.CancelledBy != "alice" {
		t.Errorf("the rollout ended as %+v, want cancelled by alice", got)
	}
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{}, store.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rows {
		if strings.HasPrefix(e.Action, "update.") {
			t.Errorf("the controller wrote an audit row of its own for a person's press: %+v", e)
		}
	}
	if _, _, err := h.c.ResumeRollout(h.ctx, alice); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("resuming with no rollout open: err = %v, want ErrNotFound", err)
	}
}

func TestStartingARolloutIsRefusedWhenItWouldDoNothingOrDoubleUp(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("off")
	host := h.updatableHost("vm-a")
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, nil); !errors.Is(err, ErrUpdateModeOff) {
		t.Errorf("with updating off: err = %v, want ErrUpdateModeOff", err)
	}
	h.inMode("manual")
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{"host_nothere"}); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("for a host that is not there: err = %v, want ErrUnknownHost", err)
	}
	h.agentHost("vm-current", "1.3.5", agent.FeatureSelfUpdate)
	h.agentHost("vm-describe", "1.3.5-3-gabcdef1", agent.FeatureSelfUpdate)
	current := h.agentHost("vm-current-2", "v1.3.5", agent.FeatureSelfUpdate)
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{current.ID}); !errors.Is(err, ErrUpdateNothingNewer) {
		t.Errorf("for a host on the release: err = %v, want ErrUpdateNothingNewer", err)
	}
	old := h.agentHost("vm-old", "1.3.4")
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{old.ID}); !errors.Is(err, ErrUpdateHostCannotUpdate) {
		t.Errorf("for a host with no helper: err = %v, want ErrUpdateHostCannotUpdate", err)
	}
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{host.ID}); err != nil {
		t.Fatalf("StartHostRollout: %v", err)
	}
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, nil); !errors.Is(err, ErrUpdateInProgress) {
		t.Errorf("over a running rollout: err = %v, want ErrUpdateInProgress", err)
	}
	h.fence("restored from a backup")
	for name, err := range map[string]error{
		"start":  func() error { _, _, err := h.c.StartHostRollout(h.ctx, alice, nil); return err }(),
		"resume": func() error { _, _, err := h.c.ResumeRollout(h.ctx, alice); return err }(),
		"cancel": func() error { _, _, err := h.c.CancelRollout(h.ctx, alice); return err }(),
	} {
		if !errors.Is(err, ErrUpdateFenced) {
			t.Errorf("%s while fenced: err = %v, want ErrUpdateFenced", name, err)
		}
	}
}

// A rollout started for named hosts takes those and no others, so an
// administrator who asked for one host does not find the fleet restarted.
func TestARolloutStartedForNamedHostsUpdatesOnlyThose(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("manual")
	left := h.updatableHost("vm-a")
	asked := h.updatableHost("vm-b")
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{asked.ID}); err != nil {
		t.Fatalf("StartHostRollout: %v", err)
	}
	h.pass(h.c)
	h.openHostAttempt(asked)
	h.agentBeat(asked, "1.3.5", nil)
	h.pass(h.c)
	h.pass(h.c)
	h.noAttemptFor(left)
	if got := h.lastRollout(); got.State != store.RolloutDone {
		t.Errorf("the rollout ended as %+v, want done once vm-b was", got)
	}
}

// Switching from auto to manual is how an operator stops automation; a rollout
// auto started stops with it.
func TestSwitchingToManualCancelsARolloutAutoStarted(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")
	h.startedRollout()
	h.inMode("manual")
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Fatalf("auto's rollout is still open in manual: %+v", got)
	}
	h.noAttemptFor(second)
}

// A person who cancelled auto's rollout meant it; the next pass must not start
// the same one again.
func TestAutoDoesNotStartAgainARolloutAPersonCancelled(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	h.updatableHost("vm-a")
	h.pass(h.c)
	if h.openRollout() == nil {
		t.Fatal("no rollout started")
	}
	if _, _, err := h.c.CancelRollout(h.ctx, alice); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}
	h.pass(h.c)
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Fatalf("auto started %+v after a person cancelled its rollout", got)
	}
	if got := h.status().Reason; !strings.Contains(got, "cancelled by hand") {
		t.Errorf("the status says %q, want why auto starts nothing", got)
	}
}

// The spec: no cordoning, and the controller never touches an operator's
// cordon. A cordoned host is updated, and is still cordoned afterwards.
func TestARolloutNeverChangesAnOperatorsCordon(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	host := h.updatableHost("vm-a")
	if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}
	h.startedRollout()
	h.openHostAttempt(host)
	if !h.mustHost(host.ID).Cordoned {
		t.Error("asking the host to update lifted its cordon")
	}
	h.agentBeat(host, "1.3.5", nil)
	h.pass(h.c)
	got := h.mustHost(host.ID)
	if !got.Cordoned || got.Version != "1.3.5" {
		t.Errorf("after the rollout the host is %+v, want it on 1.3.5 and still cordoned", got)
	}
}

func TestTheLoopDoesNothingWhileFenced(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	host := h.updatableHost("vm-a")
	h.fence("restored from a backup")
	h.pass(h.c)
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Errorf("a fenced controller started %+v", got)
	}
	h.noAttemptFor(host)

	// The applier checks for itself: a plan read before the fence went up is
	// not carried out after it.
	pic, err := h.c.updatesSnapshot(h.ctx, h.c.cfg().Updates, h.c.probeUpdateHelper())
	if err != nil {
		t.Fatal(err)
	}
	plan := updates.Plan{Actions: []updates.Action{
		{Kind: updates.ActionStartRollout, Tag: "v1.3.5", Reason: "a plan from before the fence"},
		{Kind: updates.ActionRequestController, Tag: "v1.3.5", Reason: "a plan from before the fence"},
	}}
	if h.c.applyUpdatePlan(h.ctx, pic, plan) {
		t.Error("the applier says it changed something while fenced")
	}
	if got := h.openRollout(); got != nil {
		t.Errorf("the applier started %+v while fenced", got)
	}
	if _, err := os.Stat(filepath.Join(h.updateDir, channel.RequestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the applier wrote a request while fenced: %v", err)
	}
}

// The panel shows the rollout and the planner's sentence from the frame alone,
// and a heartbeat that moves no version does not repaint it.
func TestTheStatusFrameCarriesTheRolloutAndTheSentence(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	first := h.updatableHost("vm-a")
	second := h.updatableHost("vm-b")
	sub := h.listen(events.KindUpdates)

	h.pass(h.c)
	frame := latestOfKind(t, sub, events.KindUpdates)
	rollout, _ := frame["rollout"].(map[string]any)
	if rollout["state"] != store.RolloutRunning || rollout["target"] != "v1.3.5" || rollout["done"] != 0.0 || rollout["total"] != 2.0 || rollout["current"] != "" {
		t.Errorf("the frame's rollout = %v, want running to v1.3.5, none of two done", frame["rollout"])
	}

	h.pass(h.c)
	frame = latestOfKind(t, sub, events.KindUpdates)
	rollout, _ = frame["rollout"].(map[string]any)
	if rollout["current"] != "vm-a" {
		t.Errorf("the frame's rollout = %v, want vm-a being updated", frame["rollout"])
	}
	reason, _ := frame["reason"].(string)
	if !strings.Contains(reason, "vm-a") || !strings.Contains(reason, "v1.3.5") {
		t.Errorf("the frame's reason = %q, want the planner's sentence about vm-a", reason)
	}

	// A beat with nothing new, from either host, is not a new status.
	h.agentBeat(second, "1.3.4", nil)
	h.advance(30 * time.Second)
	h.agentBeat(first, "1.3.4", nil)
	h.pass(h.c)
	nothingFor(t, sub)

	h.agentBeat(first, "1.3.5", nil)
	h.pass(h.c)
	frame = latestOfKind(t, sub, events.KindUpdates)
	rollout, _ = frame["rollout"].(map[string]any)
	if rollout["done"] != 1.0 || rollout["total"] != 2.0 || rollout["current"] != "vm-b" {
		t.Errorf("the frame's rollout = %v, want one of two done and vm-b next", frame["rollout"])
	}
	// The frame is the GET shape.
	view := h.status()
	raw, _ := json.Marshal(view)
	var fetched map[string]any
	if err := json.Unmarshal(raw, &fetched); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(fetched["rollout"]); string(got) != mustJSON(t, frame["rollout"]) {
		t.Errorf("the frame's rollout %s is not what GET returns, %s", mustJSON(t, frame["rollout"]), got)
	}
}

// latestOfKind is the last frame of a kind already sent, so a test can read the
// status as it stands after a pass that may have sent more than one.
func latestOfKind(t *testing.T, sub *events.Subscription, kind events.Kind) map[string]any {
	t.Helper()
	last := nextOfKind(t, sub, kind)
	for {
		select {
		case e := <-sub.C:
			if e.Kind != kind {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal(e.Data, &frame); err != nil {
				t.Fatalf("decoding a %s frame: %v", kind, err)
			}
			last = frame
		case <-time.After(100 * time.Millisecond):
			return last
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A host can reach a new version with no attempt of the loop's to close: an
// operator ran zoomies upgrade on it. The rollout waiting on it should still hear
// at once.
func TestAHostOnANewVersionWakesTheLoop(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("manual")
	host := h.updatableHost("vm-a")
	drainKick(h.c)
	h.agentBeat(host, "1.3.4", nil)
	if kicked(h.c) {
		t.Error("a beat with the same version woke the update loop")
	}
	h.agentBeat(host, "1.3.5", nil)
	if !kicked(h.c) {
		t.Error("a host on a new version did not wake the update loop")
	}
}

// flipModeAfterFirstRead makes the mode read as first on the first reading and
// as then on every reading after it, which is a person switching updating off
// while a request is half-way through its checks.
func flipModeAfterFirstRead(t *testing.T, first, then updates.Mode) {
	t.Helper()
	prev := readUpdateMode
	reads := 0
	readUpdateMode = func(*Controller) updates.Mode {
		reads++
		if reads == 1 {
			return first
		}
		return then
	}
	t.Cleanup(func() { readUpdateMode = prev })
}

// A request reads the mode once and acts on that reading. Read twice, a switch
// to off in between passed the gate and was then refused as a host that cannot
// update, and the page switched on the wrong code.
func TestAHostUpdateReadsTheModeOnceSoASwitchToOffIsNeverTheWrongRefusal(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	flipModeAfterFirstRead(t, updates.ModeManual, updates.ModeOff)
	_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
	if errors.Is(err, ErrUpdateHostCannotUpdate) || (err != nil && !errors.Is(err, ErrUpdateModeOff)) {
		t.Errorf("with the mode switched off mid-request: err = %v, want the request taken on the one reading, or ErrUpdateModeOff", err)
	}

	other := newHarness(t)
	other.hostsCanUpdate()
	second := other.updatableHost("vm-2")
	flipModeAfterFirstRead(t, updates.ModeOff, updates.ModeManual)
	if _, err := other.c.RequestHostUpdate(other.ctx, alice, second.ID); !errors.Is(err, ErrUpdateModeOff) {
		t.Errorf("read as off: err = %v, want ErrUpdateModeOff", err)
	}

	// Starting a rollout asks the same two questions.
	third := newHarness(t)
	third.autoFleet("manual")
	third.updatableHost("vm-3")
	flipModeAfterFirstRead(t, updates.ModeManual, updates.ModeOff)
	if _, _, err := third.c.StartHostRollout(third.ctx, alice, nil); errors.Is(err, ErrUpdateHostCannotUpdate) || (err != nil && !errors.Is(err, ErrUpdateModeOff)) {
		t.Errorf("starting a rollout with the mode switched off mid-request: err = %v, want it taken on the one reading, or ErrUpdateModeOff", err)
	}
}

// The planner reads the same answer the card does (#797): with updating off a
// host cannot be updated from here, and says so, in the snapshot as on the card.
func TestThePlannersSnapshotSaysWhatTheCardSaysWithUpdatingOff(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("off")
	host := h.updatableHost("vm-a")
	pic, err := h.c.updatesSnapshot(h.ctx, h.c.cfg().Updates, h.c.probeUpdateHelper())
	if err != nil {
		t.Fatal(err)
	}
	card := h.view(host.ID).Update
	got := pic.snap.Hosts[0]
	if got.CanSelfUpdate || card.CanUpdate || got.WhyNot != card.Reason || !strings.Contains(got.WhyNot, "Updating is off") {
		t.Errorf("snapshot says %v %q and the card %v %q, want both unable, with the same sentence that updating is off",
			got.CanSelfUpdate, got.WhyNot, card.CanUpdate, card.Reason)
	}
}

// A describe build such as 1.3.5-3-gabcdef1 is ahead of v1.3.5, and CompareBuilds
// reads its suffix as a pre-release, which would put it behind. The card, the
// button, the planner and the rollout's count must all say the one thing: it is
// not a release build, and nothing here takes it anywhere.
func TestEveryAnswerAgreesThatADescribeBuildIsNotUpdatedFromHere(t *testing.T) {
	for _, v := range []string{"1.3.5-3-gabcdef1", "v1.3.5-dirty", "1.3.4-2-gabcdef1"} {
		t.Run(v, func(t *testing.T) {
			h := newHarness(t)
			h.autoFleet("manual")
			host := h.agentHost("vm-describe", v, agent.FeatureSelfUpdate)
			behind := h.updatableHost("vm-behind")

			card := h.view(host.ID).Update
			if card.CanUpdate || !strings.Contains(card.Reason, "not a release build") {
				t.Errorf("the card says %v %q, want it unable, because the build is not a release", card.CanUpdate, card.Reason)
			}
			if _, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID); !errors.Is(err, ErrUpdateHostCannotUpdate) || !strings.Contains(err.Error(), "not a release build") {
				t.Errorf("the button: err = %v, want the card's refusal", err)
			}
			if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{host.ID}); !errors.Is(err, ErrUpdateNothingNewer) {
				t.Errorf("a rollout of it alone: err = %v, want ErrUpdateNothingNewer, since it is not behind", err)
			}
			pic, err := h.c.updatesSnapshot(h.ctx, h.c.cfg().Updates, h.c.probeUpdateHelper())
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range pic.snap.Hosts {
				if f.ID == host.ID && (f.CanSelfUpdate || f.WhyNot != card.Reason) {
					t.Errorf("the planner is told %v %q, want what the card says", f.CanSelfUpdate, f.WhyNot)
				}
			}

			if _, _, err := h.c.StartHostRollout(h.ctx, alice, nil); err != nil {
				t.Fatalf("StartHostRollout: %v", err)
			}
			if r := h.status().Rollout; r == nil || r.Total != 1 {
				t.Errorf("the rollout counts %+v, want one host: vm-behind, and not the describe build", r)
			}
			h.pass(h.c)
			h.openHostAttempt(behind)
			h.noAttemptFor(host)
		})
	}
}

// Two failures of one release on one host hand it to an operator, and that must
// hold however busy the rest of the fleet has been since: reading only the
// newest attempts would lose the two failures behind five hundred others, and
// the planner would try a third time.
func TestTwoFailuresStillHoldAHostBehindFiveHundredNewerAttempts(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	// A newer release still soaking, so that the release the controller would
	// take is not the one its hosts are taken to.
	h.readTheList(releaseEntry("v1.3.5", whenAgo(10*24*time.Hour), rolloutAssets(t)...),
		releaseEntry("v1.3.6", whenAgo(time.Hour), rolloutAssets(t)...))
	host := h.updatableHost("vm-a")
	for range 2 {
		a := &store.UpdateAttempt{Scope: store.UpdateScopeHost, HostID: host.ID, FromVersion: "1.3.4", ToVersion: "v1.3.5",
			Trigger: store.UpdateTriggerAuto, RequestedBy: "zoomies auto-update"}
		if err := h.st.CreateUpdateAttempt(h.ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.FinishUpdateAttempt(h.ctx, a.ID, store.UpdateFailed, "the download failed"); err != nil {
			t.Fatal(err)
		}
	}
	h.advance(time.Hour)
	for i := range 501 {
		a := &store.UpdateAttempt{Scope: store.UpdateScopeHost, HostID: fmt.Sprintf("host_gone%03d", i), FromVersion: "1.3.3", ToVersion: "v1.3.4",
			Trigger: store.UpdateTriggerManual, RequestedBy: "alice"}
		if err := h.st.CreateUpdateAttempt(h.ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.FinishUpdateAttempt(h.ctx, a.ID, store.UpdateSucceeded, ""); err != nil {
			t.Fatal(err)
		}
	}
	h.agentBeat(host, "1.3.4", nil)
	h.pass(h.c)
	h.pass(h.c)
	if got := h.openRollout(); got != nil {
		t.Errorf("auto started %+v for a host it has failed twice to update", got)
	}
	if open := h.openAttempts(); len(open) != 0 {
		t.Errorf("auto asked again: %+v", open)
	}
	if got := h.status().Reason; !strings.Contains(got, "an operator must act") {
		t.Errorf("the status says %q, want that an operator must act", got)
	}
}

// A cancel, or a switch to manual, between the planner reading the rollout and
// the applier asking the host must stop that host's update too: otherwise one
// more machine restarts after a person said stop.
func TestARolloutStoppedBetweenThePlanAndItsStepAsksNoHost(t *testing.T) {
	for _, stop := range []struct {
		name string
		do   func(h *harness)
	}{
		{"cancelled", func(h *harness) {
			if _, _, err := h.c.CancelRollout(h.ctx, alice); err != nil {
				h.t.Fatalf("CancelRollout: %v", err)
			}
		}},
		{"switched to manual", func(h *harness) { h.inMode("manual") }},
		{"halted", func(h *harness) {
			if _, err := h.st.HaltUpdateRollout(h.ctx, h.openRollout().ID, "vm-z did not come back"); err != nil {
				h.t.Fatal(err)
			}
		}},
	} {
		t.Run(stop.name, func(t *testing.T) {
			h := newHarness(t)
			h.autoFleet("auto")
			host := h.updatableHost("vm-a")
			h.pass(h.c)
			pic, err := h.c.updatesSnapshot(h.ctx, h.c.cfg().Updates, h.c.probeUpdateHelper())
			if err != nil {
				t.Fatal(err)
			}
			plan := updates.Decide(pic.snap)
			if len(plan.Actions) != 1 || plan.Actions[0].Kind != updates.ActionUpdateHost {
				t.Fatalf("the plan is %+v, want vm-a's update", plan.Actions)
			}
			stop.do(h)
			h.c.applyUpdatePlan(h.ctx, pic, plan)
			h.noAttemptFor(host)
		})
	}
}

// Host ids are read against one listing of the fleet: a repeated id is one host,
// an embedded agent is never a rollout's (it goes with the controller), and an
// id that names no host is refused with that id, so the caller knows which.
func TestARolloutsHostIdsAreReadOnceAgainstTheFleet(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("manual")
	host := h.updatableHost("vm-a")
	embedded := &store.Host{
		Name: "controller", Capacity: 4, Embedded: true, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, embedded); err != nil {
		t.Fatal(err)
	}
	embedded.Version, embedded.Features = "1.3.4", []string{agent.FeatureSelfUpdate}
	if err := h.st.SetHostReported(h.ctx, embedded); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{embedded.ID}); !errors.Is(err, ErrUpdateNothingNewer) {
		t.Errorf("a rollout of the embedded agent alone: err = %v, want ErrUpdateNothingNewer", err)
	}
	_, _, err := h.c.StartHostRollout(h.ctx, alice, []string{host.ID, "host_nothere"})
	// Not ErrNotFound: the handler reads that as the route's own 404, and an id
	// in the body is the body to fix.
	if !errors.Is(err, ErrUnknownHost) || errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "host_nothere") {
		t.Errorf("with an id that names no host: err = %v, want ErrUnknownHost naming it", err)
	}
	if _, _, err := h.c.StartHostRollout(h.ctx, alice, []string{host.ID, host.ID, embedded.ID}); err != nil {
		t.Fatalf("StartHostRollout: %v", err)
	}
	if r := h.openRollout(); len(r.HostIDs) != 2 {
		t.Errorf("the rollout is for %v, want each id once", r.HostIDs)
	}
}

// An open attempt whose host has no row is still something under way, and the
// planner starts nothing beside it, as it would beside any open attempt.
func TestAnOpenAttemptOfAHostWithNoRowStillBlocksTheNextStart(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("auto")
	host := h.updatableHost("vm-a")
	ghost := &store.UpdateAttempt{Scope: store.UpdateScopeHost, HostID: "host_gone", FromVersion: "1.3.4", ToVersion: "v1.3.5",
		Trigger: store.UpdateTriggerManual, RequestedBy: "alice"}
	if err := h.st.CreateUpdateAttempt(h.ctx, ghost); err != nil {
		t.Fatal(err)
	}
	h.c.applyUpdatePlan(h.ctx, h.mustPicture(), updates.Decide(h.mustPicture().snap))
	h.c.applyUpdatePlan(h.ctx, h.mustPicture(), updates.Decide(h.mustPicture().snap))
	if got := h.openRollout(); got != nil {
		t.Errorf("auto started %+v beside an open attempt", got)
	}
	h.noAttemptFor(host)
}

func (h *harness) mustPicture() *updatesPicture {
	h.t.Helper()
	pic, err := h.c.updatesSnapshot(h.ctx, h.c.cfg().Updates, h.c.probeUpdateHelper())
	if err != nil {
		h.t.Fatal(err)
	}
	return pic
}

// The handler audits the rollout a press acted on, and says whether it moved: a
// resume of a rollout that is already running is still a press, and the record
// must say it changed nothing rather than claim it resumed something.
func TestResumeAndCancelSayWhichRolloutTheyActedOnAndWhetherItMoved(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("manual")
	h.updatableHost("vm-a")
	_, started, err := h.c.StartHostRollout(h.ctx, alice, nil)
	if err != nil {
		t.Fatalf("StartHostRollout: %v", err)
	}
	r := h.openRollout()
	if started != (RolloutChange{ID: r.ID, Target: "v1.3.5", Changed: true}) {
		t.Errorf("the start says %+v, want rollout %s to v1.3.5, changed", started, r.ID)
	}
	if _, err := h.st.HaltUpdateRollout(h.ctx, r.ID, "vm-a did not come back"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name string
		call func() (*UpdatesView, RolloutChange, error)
		want bool
	}{
		{"resuming the halted rollout", func() (*UpdatesView, RolloutChange, error) { return h.c.ResumeRollout(h.ctx, alice) }, true},
		{"resuming it again while it runs", func() (*UpdatesView, RolloutChange, error) { return h.c.ResumeRollout(h.ctx, alice) }, false},
		{"cancelling it", func() (*UpdatesView, RolloutChange, error) { return h.c.CancelRollout(h.ctx, alice) }, true},
	} {
		_, got, err := step.call()
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if want := (RolloutChange{ID: r.ID, Target: "v1.3.5", Changed: step.want}); got != want {
			t.Errorf("%s says %+v, want %+v", step.name, got, want)
		}
	}
}

// A rollout that was written stays written when the status after it cannot be
// worked out, so the caller is told which rollout it was: the handler audits it
// before it reports the failure, and a press is never missing from the record.
func TestARolloutPressWhoseStatusCannotBeReadStillSaysWhichRolloutItMoved(t *testing.T) {
	h := newHarness(t)
	h.autoFleet("manual")
	h.updatableHost("vm-a")
	render := renderUpdates
	t.Cleanup(func() { renderUpdates = render })
	renderUpdates = func(*Controller, context.Context) (*UpdatesView, error) {
		return nil, errors.New("the database is busy")
	}

	_, started, err := h.c.StartHostRollout(h.ctx, alice, nil)
	r := h.openRollout()
	if err == nil || r == nil || started != (RolloutChange{ID: r.ID, Target: "v1.3.5", Changed: true}) {
		t.Fatalf("start: err = %v, change = %+v, open = %+v; want the failure and the rollout it wrote", err, started, r)
	}
	if _, err := h.st.HaltUpdateRollout(h.ctx, r.ID, "vm-a did not come back"); err != nil {
		t.Fatal(err)
	}
	if _, got, err := h.c.ResumeRollout(h.ctx, alice); err == nil || got != (RolloutChange{ID: r.ID, Target: "v1.3.5", Changed: true}) {
		t.Errorf("resume: err = %v, change = %+v; want the failure and rollout %s, changed", err, got, r.ID)
	}
	if _, got, err := h.c.CancelRollout(h.ctx, alice); err == nil || got != (RolloutChange{ID: r.ID, Target: "v1.3.5", Changed: true}) {
		t.Errorf("cancel: err = %v, change = %+v; want the failure and rollout %s, changed", err, got, r.ID)
	}
}
