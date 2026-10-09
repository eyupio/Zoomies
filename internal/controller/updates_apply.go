package controller

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

const (
	// updateAttemptTimeout is how long an attempt may stay open. The engine's own
	// worst case is fifty minutes, so a shorter limit would fail an update that
	// was still working; the helper's unit allows two hours, so this ends first.
	updateAttemptTimeout = 90 * time.Minute
	// helperInstallCommand is what installs the helper, run by somebody with root
	// on the host. Nothing the controller does can run it, which is the point.
	helperInstallCommand = "sudo zoomies updates helper install"
	// maxRequestedBy is the most of a name the helper accepts in requested_by.
	// internal/updates refuses anything longer, and does not export its limit;
	// WriteRequest would refuse a longer name rather than write it, so the two
	// cannot drift apart without a test noticing.
	maxRequestedBy = 128
)

// The helper's states as the status names them.
const (
	HelperReady   = "ready"
	HelperMissing = "missing"
	// HelperUnsupported is a controller the helper can never be installed
	// beside, where the install command would only refuse.
	HelperUnsupported = "unsupported"
)

// UpdateActor is who asked for an update, as the request and the attempt record
// it. It is only ever written down: the API checks the caller's role and writes
// the audit row before it gets here.
type UpdateActor struct {
	ID, Name string
}

// updateDir is the update folder this controller would write a request into, and
// false when there is none to find. A test names its own; a real controller finds
// it the way the installer recorded it.
func (c *Controller) updateDir() (string, bool) {
	if c.updateFolder != "" {
		return c.updateFolder, true
	}
	return channel.Locate(channel.Locator{
		ConfigPath:  c.cfg().Path(),
		SharedDir:   config.SharedDir(),
		InContainer: backend.InContainer(),
	})
}

// helperProbe is what looking for the helper found: the folder, whether the
// helper is ready, what the status says about it, and, when it is not ready, the
// refusal a person pressing the button reads.
type helperProbe struct {
	dir     string
	ready   bool
	view    UpdatesHelper
	refusal string
}

// probeUpdateHelper looks for the helper beside this controller. The marker is
// the installer's last step, so a folder without one is a helper that was never
// finished, and is treated as none.
//
// Where the helper is not there and can never be (see updates.HelperSupport),
// the status says why, and offers the upgrade by hand in place of an install
// that would refuse. A marker found is a better witness than that reasoning,
// except about an operating system that cannot run the units at all.
//
// The status is shown to every role, so its sentence names no path; the refusal
// goes only to the person who pressed the button, and names the folder so that
// they can find it.
func (c *Controller) probeUpdateHelper() helperProbe {
	cause := c.helperUnsupported()
	missing := func(p helperProbe) helperProbe {
		if cause == "" {
			return p
		}
		why := helperUnsupportedWhy(cause, "this controller")
		return helperProbe{dir: p.dir, view: UpdatesHelper{
			State:          HelperUnsupported,
			Reason:         "The update helper cannot be installed here: " + why + ". Update this controller on its host with the command below.",
			UpgradeCommand: controllerUpgradeCommand,
		}, refusal: "the update helper cannot be installed here: " + why + "; update this controller on its host with zoomies upgrade"}
	}
	view := UpdatesHelper{
		State: HelperMissing,
		Reason: "No update helper is installed on this controller's host, so the controller cannot update itself. " +
			"Somebody with root on the host can install it with the command below; until then, update it with zoomies upgrade.",
		InstallCommand: helperInstallCommand,
	}
	if cause == updates.HelperUnsupportedOS {
		return missing(helperProbe{})
	}
	dir, ok := c.updateDir()
	if !ok {
		return missing(helperProbe{view: view, refusal: "this controller has no update folder, which installing the helper records; run \"" +
			helperInstallCommand + "\" on the controller's host"})
	}
	// Ready and not the marker alone: a folder that is a link reads its target's
	// marker, and WriteRequest refuses to write through one, so the status would
	// offer an update that every press then refuses.
	_, found, err := channel.Ready(dir)
	switch {
	case err != nil:
		c.log.Debug("the update folder is not as the installer leaves it", "error", err)
		view.Reason = "The update folder on this controller's host, or the helper's marker in it, is not as the installer leaves it, so the helper is treated as missing. " +
			"Install it again with the command below."
		return missing(helperProbe{dir: dir, view: view, refusal: fmt.Sprintf("the update folder %s, or its marker, is not as the installer leaves it (%v); run \"%s\" on the controller's host to install it again",
			dir, err, helperInstallCommand)})
	case !found:
		return missing(helperProbe{dir: dir, view: view, refusal: fmt.Sprintf("there is no %s in the update folder %s, so the helper is not installed; run \"%s\" on the controller's host",
			channel.MarkerFile, dir, helperInstallCommand)})
	}
	return helperProbe{dir: dir, ready: true, view: UpdatesHelper{
		State:  HelperReady,
		Reason: "The update helper is installed on this controller's host, so the controller can update itself.",
	}}
}

// RequestControllerUpdate is RequestControllerUpdateAttempt for a caller that
// wants only the status.
func (c *Controller) RequestControllerUpdate(ctx context.Context, by UpdateActor, tag string) (*UpdatesView, error) {
	view, _, err := c.RequestControllerUpdateAttempt(ctx, by, tag)
	return view, err
}

// RequestControllerUpdateAttempt asks the helper beside this controller to take it to
// tag, or to the newest release that can be installed here when tag is empty,
// and returns the status with the attempt in it.
//
// The order is the design. Everything that can refuse does so before anything is
// written. The attempt is recorded before the request, because the store's rule
// of one open attempt per target is what stops two callers each writing one. And
// a request that cannot be written closes the attempt it opened, so a failed
// button leaves nothing in flight.
//
// The helper's unit stops after five starts in ten minutes. One open attempt at a
// time, each closed by an answer or after 90 minutes, keeps requests well below it.
//
// The attempt's id comes back beside the status so that a caller that audits the
// press does not have to find it in the status, where it is only the latest.
func (c *Controller) RequestControllerUpdateAttempt(ctx context.Context, by UpdateActor, tag string) (*UpdatesView, string, error) {
	if !c.mayAct() {
		return nil, "", fmt.Errorf("%w: %s", ErrUpdateFenced, c.notActingReason())
	}
	if c.updateMode() == updates.ModeOff {
		return nil, "", ErrUpdateModeOff
	}
	if _, ok := version.Release(version.Version); !ok {
		return nil, "", ErrUpdateNotARelease
	}
	helper := c.probeUpdateHelper()
	if !helper.ready {
		return nil, "", fmt.Errorf("%w: %s", ErrUpdateHelperMissing, helper.refusal)
	}
	target, err := c.controllerTarget(tag)
	if err != nil {
		return nil, "", err
	}

	attempt := &store.UpdateAttempt{
		Scope: store.UpdateScopeController, FromVersion: version.Version, ToVersion: target,
		Trigger: store.UpdateTriggerManual, RequestedBy: requestedBy(by),
	}
	if err := c.st.CreateUpdateAttempt(ctx, attempt); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, "", ErrUpdateInProgress
		}
		return nil, "", fmt.Errorf("recording the update attempt: %w", err)
	}
	// A new attempt supersedes the last one's failure in the problems list now,
	// not at the next pass.
	c.lookAtUpdates(context.WithoutCancel(ctx))
	// The time as the row keeps it, to the millisecond, so that the request and
	// the attempt it belongs to say the same thing when they are read side by side.
	err = channel.WriteRequest(helper.dir, updates.Request{
		V: updates.WireVersion, ID: attempt.ID, Tag: target,
		RequestedBy: attempt.RequestedBy, RequestedAt: attempt.RequestedAt.Truncate(time.Millisecond),
	})
	if err != nil {
		// Closed even if the caller has gone, or the attempt would sit open for 90
		// minutes and refuse every press until then.
		c.finishUpdateAttempt(context.WithoutCancel(ctx), *attempt, store.UpdateFailed,
			"The request could not be handed to the update helper, so nothing ran: "+err.Error())
		// WriteRequest leaves nothing of its own behind when it fails, so this
		// only ever finds another request, which it leaves; it is here so that no
		// path that closes an attempt can strand that attempt's request. An earlier
		// request still waiting is the ordinary refusal and has already been said to
		// the person, so it is not also a warning about a request that is not ours.
		if !errors.Is(err, channel.ErrRequestPending) {
			c.withdrawRequest(*attempt)
		}
		c.lookAtUpdates(context.WithoutCancel(ctx))
		_, _ = c.publishUpdates(context.WithoutCancel(ctx))
		return nil, "", fmt.Errorf("%w: %w", ErrUpdateHelperMissing, err)
	}
	c.log.Info("asked the update helper to update this controller",
		"attempt", attempt.ID, "from", attempt.FromVersion, "to", target, "requested_by", attempt.RequestedBy)
	// The loop watches for the answer from now on; a refusal comes back in seconds.
	c.KickUpdates()
	view, err := c.publishUpdates(ctx)
	if err != nil {
		// The request is in the folder and the helper may already be running it,
		// so this is not a failure to report: an error here would tell the person
		// to press again, and the press would be refused as in flight. They get
		// the attempt they made, and the next pass sends the whole status.
		c.log.Warn("asked for the update, but could not work out the status to return", "attempt", attempt.ID, "error", err)
		return c.requestedView(*attempt, helper.view), attempt.ID, nil
	}
	return view, attempt.ID, nil
}

// requestedView is the status as far as it is known without reading anything:
// the settings, the build, the helper and the attempt just made.
func (c *Controller) requestedView(a store.UpdateAttempt, helper UpdatesHelper) *UpdatesView {
	cfg := c.cfg().Updates
	_, fromRelease := version.Release(version.Version)
	return &UpdatesView{
		Mode:       string(updateModeOf(cfg.Mode)),
		Soak:       config.TidyDuration(cfg.Soak),
		Running:    UpdatesRunning{Version: version.Version, Release: fromRelease},
		Reason:     fmt.Sprintf("The update to %s has been asked for, and the update helper will answer.", a.ToVersion),
		Helper:     helper,
		Controller: attemptView(a),
	}
}

// controllerTarget is the tag an update of this controller takes: the one asked
// for, or the newest release that can be installed here. It has to be a release
// in the list this controller read and complete for this system, so that the
// helper is never sent to a download that cannot work, and newer than the build
// that is running.
//
// The soak is not applied. It is how long auto waits on its own; a person
// pressing the button has decided.
func (c *Controller) controllerTarget(tag string) (string, error) {
	state := c.latestRelease()
	if state == nil || state.Releases == nil {
		return "", fmt.Errorf("%w: this controller has not read the release list yet; check for releases, then try again", ErrUpdateNothingNewer)
	}
	var pick updates.Release
	var found bool
	if tag == "" {
		pick, found = updates.Newest(state.Releases, runtime.GOOS, runtime.GOARCH)
		if !found {
			return "", fmt.Errorf("%w: the release list holds no release that can be installed on %s/%s", ErrUpdateNothingNewer, runtime.GOOS, runtime.GOARCH)
		}
	} else {
		if !updates.ValidTag(tag) {
			return "", fmt.Errorf("%w: %q is not a release tag of the form vMAJOR.MINOR.PATCH", ErrUpdateNothingNewer, tag)
		}
		var named []updates.Release
		for _, r := range state.Releases {
			if r.Tag == tag {
				named = append(named, r)
			}
		}
		// Newest over the one release asks the same question of it that it asks of
		// the whole list, so a tag is taken here only if it would be offered there.
		pick, found = updates.Newest(named, runtime.GOOS, runtime.GOARCH)
		if !found {
			return "", fmt.Errorf("%w: %s is not a release in the list this controller read that can be installed on %s/%s; check for releases, or choose one the status names",
				ErrUpdateNothingNewer, tag, runtime.GOOS, runtime.GOARCH)
		}
	}
	if version.CompareBuilds(version.Version, pick.Tag) != version.SkewBehind {
		return "", fmt.Errorf("%w: this controller runs %s, and %s is not newer", ErrUpdateNothingNewer, version.Version, pick.Tag)
	}
	return pick.Tag, nil
}

// notActingReason says why mayAct is false, for the refusal.
func (c *Controller) notActingReason() string {
	if c.Fenced().Fenced {
		return "this fleet is fenced for recovery; check what the restore did not bring with it, then lift the fence"
	}
	return "this controller does not hold the database's lease, so another may be running against it; update from the controller that holds it"
}

// requestedBy is who asked, as the helper will log it: a name it accepts.
//
// The helper refuses a requested_by over 128 bytes or with a character that is
// not printable, because root writes it into a log; a name from single sign-on
// can be either. Refused there, the button would fail for that person every time,
// so the name is cut to fit here instead, dropping what cannot be printed and
// stopping at a whole character. The ID stands in for a name with nothing left.
func requestedBy(by UpdateActor) string {
	for _, s := range []string{by.Name, by.ID} {
		if v := printableWithin(s, maxRequestedBy); v != "" {
			return v
		}
	}
	return "unknown"
}

// printableWithin is s without its unprintable characters, cut to at most limit
// bytes at a character boundary. Invalid UTF-8 comes back as U+FFFD, which is
// what encoding/json would have made of it anyway.
func printableWithin(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsGraphic(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
