package updates

import (
	"fmt"
	"strconv"
	"time"

	"github.com/eyupio/zoomies/internal/version"
)

// Mode is what the platform has chosen to do about new releases. The values are
// those of the updates.mode setting.
//
// internal/config lists the same three words for the setting. This package does
// not import it, so a word added to one is a word to add to the other.
type Mode string

const (
	// ModeOff considers no release at all.
	ModeOff Mode = "off"
	// ModeManual offers the newest release to a person, who is the only wait.
	ModeManual Mode = "manual"
	// ModeAuto takes the newest release once it has been public for the soak.
	ModeAuto Mode = "auto"
)

// ChooseInput is everything Choose needs, gathered by the caller.
type ChooseInput struct {
	Mode Mode
	// Soak is how long auto waits after a release is published; manual ignores
	// it. It is never negative: the configuration refuses a negative soak when
	// it loads.
	Soak time.Duration
	// Now is the decision time. It is a field rather than a clock read so that a
	// test can place a soak one nanosecond either side of its end.
	Now time.Time
	// Running is the version of the build being updated as it reports itself,
	// which for a release binary is "1.3.5" without the v its tag has.
	Running  string
	Releases []Release
	// GOOS and GOARCH name the system whose binary would be installed.
	GOOS, GOARCH string
}

// Target is what the mode would do about the releases on offer, and why.
type Target struct {
	// Release is the newest complete release for the system, whether or not it
	// is newer than what is running. It is the zero Release when there is none,
	// and when updates are off.
	Release Release
	// Newer says whether Release is a later release than the one running. Only
	// then is there anything to take.
	Newer bool
	// DueAt is the earliest time the mode takes Release: the end of the soak in
	// auto, and zero in manual, where the person asking is the only wait. It
	// means nothing unless Newer.
	DueAt time.Time
	// Reason is the sentence the status shows, in an operator's words.
	Reason string
}

// DueBy says whether the mode may take the release at now. It is the one place
// the wait is compared with a clock: Choose asks it too when it words the
// reason, so the sentence and the decision cannot disagree about the instant the
// wait ends.
func (t Target) DueBy(now time.Time) bool {
	return t.Newer && !now.Before(t.DueAt)
}

// Choose says which release the mode would take, when, and why.
//
// It is Newest, then a comparison of what is running with that release, then the
// mode. Only a build that is behind is Newer: one that is ahead would be a
// downgrade, and the same release written two ways, 1.3.5 and v1.3.5, is no
// change at all.
func Choose(in ChooseInput) Target {
	if in.Mode != ModeManual && in.Mode != ModeAuto {
		return Target{Reason: "Updates are off. Set updates.mode to manual or auto to be offered new releases."}
	}
	best, found := Newest(in.Releases, in.GOOS, in.GOARCH)
	t := Target{Release: best}

	release, fromRelease := version.Release(in.Running)
	if !fromRelease {
		// A controller on :dev is usually ahead of the newest release, so any
		// answer but "leave it alone" would be a downgrade.
		t.Reason = "This build is not from a release, so updates leave it alone. Run a released build and this clears."
		return t
	}
	if !found {
		t.Reason = noReleaseReason(in.GOOS, in.GOARCH)
		return t
	}

	running := "v" + release
	switch version.CompareBuilds(in.Running, best.Tag) {
	case version.SkewBehind:
		t.Newer = true
	case version.SkewNone:
		t.Reason = "Up to date: " + best.Tag + " is the newest release."
		return t
	case version.SkewAhead:
		t.Reason = fmt.Sprintf("Ahead of the newest release: this build is %s and the newest is %s, and updates never go back.", running, best.Tag)
		return t
	default:
		t.Reason = fmt.Sprintf("This build, %s, cannot be ordered against %s, so updates leave it alone.", running, best.Tag)
		return t
	}

	if in.Mode == ModeManual {
		t.Reason = fmt.Sprintf("Available: %s is newer than the %s running now; manual mode waits for someone to update.", best.Tag, running)
		return t
	}
	t.DueAt = best.PublishedAt.Add(in.Soak)
	t.Reason = autoReason(t, in.Soak, in.Now)
	return t
}

// noReleaseReason says why no release can be offered to this system. The two
// causes are told apart because their fixes differ: a system nothing here
// updates has none, and anywhere else the release itself is what to look at.
func noReleaseReason(goos, goarch string) string {
	asset := AssetName(goos, goarch)
	if asset == "" {
		return fmt.Sprintf("Updates are available only on linux and darwin, and this is %s/%s.", goos, goarch)
	}
	return fmt.Sprintf("No complete release is available for %s/%s. A complete release has a vX.Y.Z tag and a publication date, is not a draft or a prerelease, and carries %s and %s.",
		goos, goarch, checksumsAsset, asset)
}

// autoReason words an auto target that is newer: inside the soak, past it, or
// dated ahead of the clock.
func autoReason(t Target, soak time.Duration, now time.Time) string {
	tag := t.Release.Tag
	age := now.Sub(t.Release.PublishedAt)
	if age < 0 {
		// GitHub's clock and this controller's can disagree, and a release can be
		// dated ahead on purpose. Either way the wait has not started, and an age
		// below zero in the sentence would read as nonsense.
		return fmt.Sprintf("Waiting: %s is dated in the future, so it does not count as public yet; it can be taken in %s.",
			tag, spanOf(t.DueAt.Sub(now)))
	}
	wait := "auto does not wait"
	if soak > 0 {
		wait = "auto waits for " + spanOf(soak).following(spanOf(age))
	}
	if t.DueBy(now) {
		return fmt.Sprintf("Ready: %s has been public for %s and %s; it can be taken now.", tag, spanOf(age), wait)
	}
	return fmt.Sprintf("Waiting: %s has been public for %s and %s; it can be taken in %s.",
		tag, spanOf(age), wait, spanOf(t.DueAt.Sub(now)))
}

// span is a length of time as the sentences say it: whole hours from an hour up,
// whole minutes below that, and nothing finer.
type span struct {
	n    int
	unit string // "hour", "minute", or empty for less than a minute
}

// spanOf rounds down: a release is never said to have been public for longer
// than it has, and the time left, recomputed on every pass, only ever errs short.
func spanOf(d time.Duration) span {
	switch {
	case d >= time.Hour:
		return span{int(d / time.Hour), "hour"}
	case d >= time.Minute:
		return span{int(d / time.Minute), "minute"}
	}
	return span{}
}

func (s span) String() string {
	switch {
	case s.unit == "":
		return "less than a minute"
	case s.n == 1:
		return "1 " + s.unit
	}
	return strconv.Itoa(s.n) + " " + s.unit + "s"
}

// following words s for a sentence that has just said prev. When both count the
// same unit it is not said twice, as in "public for 6 hours and auto waits for
// 24", unless that would leave a bare 1, which reads as a count of nothing.
func (s span) following(prev span) string {
	if s.unit != "" && s.unit == prev.unit && s.n > 1 {
		return strconv.Itoa(s.n)
	}
	return s.String()
}
