package controller

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"time"
	"unicode"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

// The views below are the JSON of the update status. GET /updates returns one,
// updates.updated carries the same, and the metrics read the same, all rendered
// by UpdatesView and by nothing else: a frame dropped straight into the page is
// what a fetch would have returned, and a gauge cannot say something the page
// does not.

// UpdatesView is what the update mode would do about the releases on offer, and
// why.
//
// It is worked out when asked and never stored. It changes when the clock passes
// the end of a soak as much as when a list is read, and no row is written for
// either.
//
// Latest, Target and CheckedAt are null while updating is off and until a list
// has been read. A list that was read and holds nothing that can be installed is
// not the same: it has a time, and a sentence of its own.
type UpdatesView struct {
	// Mode is updates.mode as the controller acts on it, so a word the registry
	// does not know reads here, as everywhere else, as off.
	Mode string `json:"mode"`
	// Soak is updates.soak as the Settings page writes it. manual ignores it.
	Soak    string         `json:"soak"`
	Running UpdatesRunning `json:"running"`
	// Latest is the newest release that could be installed on this system,
	// whether or not the running build is behind it.
	Latest *UpdatesRelease `json:"latest"`
	// Target is what the mode would do about Latest.
	Target *UpdatesTarget `json:"target"`
	// Reason is the sentence the page shows. It is internal/updates' own wherever
	// a list has been read.
	Reason string `json:"reason"`
	// CheckedAt is when the list Latest was chosen from was read.
	CheckedAt *time.Time `json:"checked_at"`
	// Helper says whether this controller can update itself, and if not, how a
	// person with root on its host makes it able to.
	Helper UpdatesHelper `json:"helper"`
	// Controller is this controller's latest update attempt, open or ended, and
	// null when it has never had one.
	Controller *UpdatesAttempt `json:"controller"`
}

// UpdatesHelper is the update helper beside the controller, as far as the
// controller can see it: its marker, in the folder the installer recorded.
type UpdatesHelper struct {
	// State is ready or missing.
	State string `json:"state"`
	// Reason is the sentence the page shows. It names no path, because every role
	// reads it.
	Reason string `json:"reason"`
	// InstallCommand is what installs the helper, for a person to copy, and empty
	// when it is ready.
	InstallCommand string `json:"install_command"`
}

// UpdatesAttempt is one request to update, and how it ended.
type UpdatesAttempt struct {
	ID string `json:"id"`
	// State is requested until it ends, then succeeded, failed, timed_out or
	// cancelled.
	State string `json:"state"`
	// From is the build that asked, as it reports itself, and To the release tag
	// it asked for.
	From string `json:"from"`
	To   string `json:"to"`
	// Trigger is manual for a person and auto for the update mode.
	Trigger     string     `json:"trigger"`
	RequestedAt time.Time  `json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	// Error is why it did not succeed, often the helper's own sentence, and empty
	// otherwise. Below the platform role it is a fixed sentence for the state (see
	// For), because the text can name a folder on the controller's host.
	Error string `json:"error"`
}

// UpdatesRunning is the build being updated, as it reports itself: a release
// binary says "1.3.5", without the v its tag has.
type UpdatesRunning struct {
	Version string `json:"version"`
	// Release says the build came from a release. A build from main is usually
	// ahead of the newest release, so only one that did can be offered an update
	// without it being a downgrade.
	Release bool `json:"release"`
}

// UpdatesRelease is a release on offer.
type UpdatesRelease struct {
	Tag string `json:"tag"`
	// URL is the release's page, so its notes can be read before it is taken.
	URL string `json:"url"`
	// PublishedAt is when GitHub published it, which is where the soak starts.
	PublishedAt time.Time `json:"published_at"`
}

// UpdatesTarget is what the mode would do about the newest release.
type UpdatesTarget struct {
	Tag string `json:"tag"`
	// Newer says the release is a later one than the running build. Only then is
	// there anything to take, and only then does DueAt mean anything.
	Newer bool `json:"newer"`
	// DueAt is the earliest time the mode takes the release: the end of the soak
	// in auto. It is null in manual, where the person asking is the only wait.
	DueAt *time.Time `json:"due_at"`
}

// For is the status as one audience may read it. The platform reads it whole.
//
// An attempt's error is often the helper's own sentence, or the controller's
// about a folder it could not write, and either can name a path on the
// controller's host. That host is the platform's to know about, so every other
// role is given a sentence for the state in its place, and still learns that the
// attempt ended and how. The helper's own sentence stays: it is fixed, and names
// no path, because every role reads it.
//
// It narrows a copy. The view is rendered once and handed to every audience, and
// the attempt it points to is shared.
func (v UpdatesView) For(platform bool) UpdatesView {
	if platform || v.Controller == nil || v.Controller.Error == "" {
		return v
	}
	attempt := *v.Controller
	attempt.Error = withheldAttemptError(attempt.State)
	v.Controller = &attempt
	return v
}

// withheldAttemptError is what a role below the platform reads where the text of
// an attempt's error would be. It is keyed on the state alone, so that it can
// never carry anything the error did.
func withheldAttemptError(state string) string {
	switch state {
	case store.UpdateTimedOut:
		return "The update helper did not answer in time. Whoever holds the platform role can read the detail."
	case store.UpdateCancelled:
		return "The update was cancelled. Whoever holds the platform role can read the detail."
	}
	return "The update did not succeed. Whoever holds the platform role can read why."
}

// UpdatesView works out the status now.
func (c *Controller) UpdatesView(ctx context.Context) (*UpdatesView, error) {
	// Loaded once, so that a settings change between two loads cannot give one
	// frame the old soak beside the new mode.
	cfg := c.cfg().Updates
	mode := updateModeOf(cfg.Mode)
	_, fromRelease := version.Release(version.Version)
	view := &UpdatesView{
		Mode:    string(mode),
		Soak:    config.TidyDuration(cfg.Soak),
		Running: UpdatesRunning{Version: version.Version, Release: fromRelease},
		Helper:  c.probeUpdateHelper().view,
	}
	// The last attempt is shown in every mode: one that was in flight when
	// updating was switched off still ends, and the page says how.
	attempts, err := c.st.ListUpdateAttempts(ctx, store.UpdateScopeController, "", 1)
	if err != nil {
		return nil, fmt.Errorf("reading the controller's update attempts: %w", err)
	}
	if len(attempts) > 0 {
		view.Controller = attemptView(attempts[0])
	}

	state := c.latestRelease()
	// Having a release is not the same as having read the list. The check that ran
	// while updating was off asked only for the latest release, and switching to a
	// mode that wants the list starts no check of its own, so for a while the
	// status has a mode and no list. Handed none, the rule would say that no
	// complete release exists, which nobody has been told: a list with nothing in
	// it is an answer, and not having asked is not.
	listRead := state != nil && state.Releases != nil
	// A build that is not from a release is told so whether or not the list has
	// been read. One from main never reads it, because it follows the head of main
	// in every mode, so promising a read would be false for as long as it ran; and
	// the rule has the better sentence for any of them.
	if mode != updates.ModeOff && fromRelease && !listRead {
		view.Reason = unreadReason(cfg.CheckInterval)
		return view, nil
	}

	var releases []updates.Release
	if listRead {
		releases = state.Releases
	}
	// A list that was never read goes in as nil only where the rule does not
	// depend on it: it decides off first, and a build that is not from a release
	// before it asks what the list holds. A list left over from when updating was
	// on offers nothing once it is off, for the same reason.
	target := updates.Choose(updates.ChooseInput{
		Mode: mode, Soak: cfg.Soak, Now: c.Now(), Running: version.Version,
		Releases: releases, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	})
	view.Reason = target.Reason
	if mode == updates.ModeOff {
		return view, nil
	}
	if listRead {
		at := state.At.UTC()
		view.CheckedAt = &at
	}
	if target.Release.Tag == "" {
		return view, nil
	}

	view.Latest = &UpdatesRelease{
		Tag: target.Release.Tag, URL: releasePageURL(target.Release.URL), PublishedAt: target.Release.PublishedAt.UTC(),
	}
	view.Target = &UpdatesTarget{Tag: target.Release.Tag, Newer: target.Newer}
	if target.Newer && !target.DueAt.IsZero() {
		due := target.DueAt.UTC()
		view.Target.DueAt = &due
	}
	return view, nil
}

// attemptView is an attempt as the status shows it.
func attemptView(a store.UpdateAttempt) *UpdatesAttempt {
	out := &UpdatesAttempt{
		ID: a.ID, State: a.State, From: a.FromVersion, To: a.ToVersion, Trigger: a.Trigger,
		RequestedAt: a.RequestedAt.UTC(), Error: a.Error,
	}
	if a.FinishedAt != nil {
		at := a.FinishedAt.UTC()
		out.FinishedAt = &at
	}
	return out
}

// unreadReason says why there is nothing to offer yet, for a mode that wants a
// list nobody has read.
//
// It claims nothing about what the list holds, which is the one thing not known:
// the rule's sentence for a list with no complete release in it would tell the
// operator that none exists.
func unreadReason(interval time.Duration) string {
	if interval <= 0 {
		return "Zoomies has not read the release list, and updates.check_interval is 0, which switches the check off. " +
			"Set updates.check_interval to a duration such as 24h to be offered releases."
	}
	return fmt.Sprintf("Zoomies has not read the release list yet. It reads it every %s, so the next read is due within that time.",
		config.TidyDuration(interval))
}

// releasePageURL is a release's page when it is one a browser can safely be sent
// to, and blank when it is not.
//
// The address arrives as GitHub's html_url and the UI puts it in a link. GitHub
// is trusted to be GitHub, but a proxy in between, or a response that has been
// tampered with, is not, and a javascript: address in an href runs in the
// operator's session. Only an absolute https address with a host and no
// control characters passes; the page then simply has no link.
func releasePageURL(raw string) string {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return raw
}
