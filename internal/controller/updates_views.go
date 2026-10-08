package controller

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/eyupio/zoomies/internal/config"
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

// UpdatesView works out the status now.
func (c *Controller) UpdatesView(ctx context.Context) (*UpdatesView, error) {
	cfg := c.cfg().Updates
	mode := c.updateMode()
	_, fromRelease := version.Release(version.Version)
	view := &UpdatesView{
		Mode:    string(mode),
		Soak:    config.TidyDuration(cfg.Soak),
		Running: UpdatesRunning{Version: version.Version, Release: fromRelease},
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
		Tag: target.Release.Tag, URL: target.Release.URL, PublishedAt: target.Release.PublishedAt.UTC(),
	}
	view.Target = &UpdatesTarget{Tag: target.Release.Tag, Newer: target.Newer}
	if target.Newer && !target.DueAt.IsZero() {
		due := target.DueAt.UTC()
		view.Target.DueAt = &due
	}
	return view, nil
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
