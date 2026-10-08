package updates

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// noon is the instant these tests treat as now unless they say otherwise.
var noon = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// asking is a linux/amd64 controller running the given build, offered releases
// under mode with the default soak of a day.
func asking(mode Mode, running string, now time.Time, releases ...Release) ChooseInput {
	return ChooseInput{
		Mode: mode, Soak: 24 * time.Hour, Now: now, Running: running,
		Releases: releases, GOOS: "linux", GOARCH: "amd64",
	}
}

// Off is not "nothing is newer": no release is considered at all, so nothing a
// status page shows can read as an offer. The release is one a person would
// take at once, which leaves only the mode to explain why it is not offered. A
// word this package does not know is read the same way. The configuration
// refuses one when it loads, so arriving with one is a bug, and the safe reading
// of a bug is that nothing gets replaced.
func TestChooseOffersNothingWhenTheModeIsOff(t *testing.T) {
	newer := published("v1.3.5", noon.Add(-72*time.Hour))
	for _, tc := range []struct {
		name string
		mode Mode
	}{
		{"off", ModeOff},
		{"no mode at all", ""},
		{"a word nobody defined", "automatic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Choose(asking(tc.mode, "1.3.4", noon, newer))
			if got.Newer || got.Release.Tag != "" {
				t.Errorf("offered %q (newer: %v) with the mode %q", got.Release.Tag, got.Newer, tc.mode)
			}
			if got.DueBy(noon.AddDate(1, 0, 0)) {
				t.Error("due a year on, with updates off")
			}
			if !strings.HasPrefix(got.Reason, "Updates are off") {
				t.Errorf("Reason = %q, want it to start %q", got.Reason, "Updates are off")
			}
		})
	}
}

// The usual state of a healthy controller, and the one the status shows most.
// It reads the same in both modes: a mode only matters once something newer
// exists to take.
func TestChooseSaysUpToDateWhenRunningTheNewest(t *testing.T) {
	older := published("v1.3.4", noon.Add(-200*time.Hour))
	newest := published("v1.3.5", noon.Add(-72*time.Hour))
	for _, mode := range []Mode{ModeManual, ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			// A release binary reports 1.3.5 while the tag says v1.3.5.
			got := Choose(asking(mode, "1.3.5", noon, older, newest))
			if got.Newer || got.DueBy(noon.AddDate(1, 0, 0)) {
				t.Errorf("a controller on the newest release was offered %q (newer: %v)", got.Release.Tag, got.Newer)
			}
			if got.Release.Tag != "v1.3.5" {
				t.Errorf("Release = %q, want the newest, v1.3.5", got.Release.Tag)
			}
			if !strings.HasPrefix(got.Reason, "Up to date") {
				t.Errorf("Reason = %q, want it to say the controller is up to date", got.Reason)
			}
			// "The newest release" alone would be untrue the day a newer one is
			// still uploading: the sentence means the newest one that can be
			// installed, and says so.
			if !strings.Contains(got.Reason, "the newest release that can be installed") {
				t.Errorf("Reason = %q, want it to say which newest release it means", got.Reason)
			}
		})
	}
}

// A build ahead of the newest release is a candidate, a fork, or a build cut
// after the last tag, and an older binary may not read a database a newer one
// has migrated -- the reason SelfUpdate has its own guard. "Up to date" would be
// untrue of such a build, so the sentence says why nothing happens instead.
func TestChooseNeverOffersADowngrade(t *testing.T) {
	releases := []Release{
		published("v1.3.4", noon.Add(-72*time.Hour)),
		published("v1.3.3", noon.Add(-300*time.Hour)),
	}
	for _, mode := range []Mode{ModeManual, ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			got := Choose(asking(mode, "1.4.0", noon, releases...))
			if got.Newer || got.DueBy(noon.AddDate(1, 0, 0)) {
				t.Errorf("offered a downgrade to %q (newer: %v)", got.Release.Tag, got.Newer)
			}
			if got.Release.Tag != "v1.3.4" {
				t.Errorf("Release = %q, want the newest, v1.3.4", got.Release.Tag)
			}
			for _, want := range []string{"v1.4.0", "v1.3.4", "the newest release that can be installed", "never go back"} {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
				}
			}
		})
	}
}

// A controller on :dev is stamped main-sha-abc1234 and is usually ahead of the
// newest release, so "a release is available" would tell it to downgrade. The
// release below is newer than any version a person would compare, which leaves
// only the build to explain why it is left alone. The release is still reported
// as the newest, because the status shows what is out there even for a build it
// will not touch.
func TestChooseRefusesABuildThatIsNotFromARelease(t *testing.T) {
	newest := published("v9.9.9", noon.Add(-72*time.Hour))
	for _, running := range []string{"main-sha-abc1234", "dev", "v0.2-beta-5-gabc1234", "some-fork-build", ""} {
		for _, mode := range []Mode{ModeManual, ModeAuto} {
			t.Run(string(mode)+" "+strconv.Quote(running), func(t *testing.T) {
				got := Choose(asking(mode, running, noon, newest))
				if got.Newer || got.DueBy(noon.AddDate(1, 0, 0)) {
					t.Errorf("offered %q to the build %q", got.Release.Tag, running)
				}
				if got.Release.Tag != "v9.9.9" {
					t.Errorf("Release = %q, want the newest, v9.9.9, reported although it is not offered", got.Release.Tag)
				}
				if !strings.Contains(got.Reason, "not from a release") {
					t.Errorf("Reason = %q, want it to say the build is not from a release", got.Reason)
				}
			})
		}
	}
}

// "1.2.x" starts like a release and still cannot be placed against one.
// CompareBuilds answers "differs" rather than guess an order, and a guess here
// would be a downgrade. It is a different fault from a build that is not from a
// release, and the sentence says which -- and what to do about it, since the
// person reading it can run the newest release and make it go away. The release
// is reported here too, as it is for a build that is not from one.
func TestChooseLeavesABuildItCannotOrderAlone(t *testing.T) {
	got := Choose(asking(ModeManual, "1.2.x", noon, published("v1.3.5", noon.Add(-72*time.Hour))))
	if got.Newer || got.DueBy(noon.AddDate(1, 0, 0)) {
		t.Errorf("offered %q to a build that cannot be ordered", got.Release.Tag)
	}
	if got.Release.Tag != "v1.3.5" {
		t.Errorf("Release = %q, want the newest, v1.3.5, reported although it is not offered", got.Release.Tag)
	}
	for _, want := range []string{"v1.2.x", "v1.3.5", "cannot be ordered", "Run a released build such as v1.3.5"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
		}
	}
}

// An update that never comes is usually a release still uploading, or a fork
// that publishes differently, and either is quicker to fix when the sentence
// says what a release has to have.
func TestChooseSaysWhatAReleaseNeedsWhenNoneIsComplete(t *testing.T) {
	unfinished := without(published("v1.3.5", noon.Add(-72*time.Hour)), "checksums.txt")
	for _, tc := range []struct {
		name     string
		releases []Release
	}{
		{"no releases listed", nil},
		{"only one that is still uploading", []Release{unfinished}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Choose(asking(ModeManual, "1.3.4", noon, tc.releases...))
			if got.Newer || got.Release.Tag != "" {
				t.Errorf("offered %q with nothing complete to offer", got.Release.Tag)
			}
			for _, want := range []string{"No complete release", "linux/amd64", "publication date", "checksums.txt", "zoomies_linux_amd64"} {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
				}
			}
		})
	}
}

// encoding/json turns a null, a missing or a zero published_at into the zero
// time without an error, and the soak is counted from that date. A release that
// cannot be dated cannot be soaked, so it must not be mistaken for an old one:
// the release below is perfect and the newest, and read as the zero time it
// would have passed the soak at once, in the one mode meant to hold a release
// back. Manual refuses it too, because a release GitHub cannot date is not one
// to offer to anybody.
func TestChooseNeverOffersAReleaseThatHasNoPublicationDate(t *testing.T) {
	dated := published("v1.3.4", noon.Add(-72*time.Hour))
	undated := published("v1.3.5", time.Time{})
	for _, mode := range []Mode{ModeManual, ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			inEveryRotation([]Release{dated, undated}, func(rotated []Release) {
				got := Choose(asking(mode, "1.3.3", noon, rotated...))
				if got.Release.Tag != "v1.3.4" || !got.Newer {
					t.Errorf("chose %q (newer: %v) from %s first, want the dated v1.3.4", got.Release.Tag, got.Newer, rotated[0].Tag)
				}
			})

			got := Choose(asking(mode, "1.3.3", noon, undated))
			if got.Newer || got.Release.Tag != "" || got.DueBy(noon.AddDate(1, 0, 0)) {
				t.Errorf("offered %q (newer: %v) when it was the only release and had no date", got.Release.Tag, got.Newer)
			}
			for _, want := range []string{"No complete release", "publication date"} {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
				}
			}
		})
	}
}

// Naming a windows binary a release never carries would send somebody looking
// for a file that nothing here installs, so the sentence says the system is the
// reason.
func TestChooseSaysSoWhenNothingUpdatesOnThisSystem(t *testing.T) {
	in := asking(ModeManual, "1.3.4", noon, published("v1.3.5", noon.Add(-72*time.Hour)))
	in.GOOS = "windows"
	got := Choose(in)
	if got.Newer || got.Release.Tag != "" {
		t.Errorf("offered %q on windows", got.Release.Tag)
	}
	for _, want := range []string{"linux and darwin", "windows/amd64"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
		}
	}
	if strings.Contains(got.Reason, "zoomies_windows") {
		t.Errorf("Reason = %q, which names a binary nothing would download", got.Reason)
	}
}

// In manual mode the person pressing the button is the soak, so the wait the
// platform chose for auto must not hold them back: the release below went public
// a minute ago, the soak is a day, and it is theirs to take. The rule says so by
// giving no due time at all.
func TestChooseManualTakesTheNewestAtOnce(t *testing.T) {
	older := published("v1.3.4", noon.Add(-48*time.Hour))
	newest := published("v1.3.5", noon.Add(-time.Minute))
	got := Choose(asking(ModeManual, "1.3.4", noon, older, newest))
	if !got.Newer || got.Release.Tag != "v1.3.5" {
		t.Fatalf("Release = %q, newer = %v; want v1.3.5 and newer", got.Release.Tag, got.Newer)
	}
	if !got.DueAt.IsZero() {
		t.Errorf("DueAt = %s, want none: manual has no wait", got.DueAt)
	}
	if !got.DueBy(noon) {
		t.Error("not due in manual mode a minute after the release")
	}
	for _, want := range []string{"v1.3.5", "v1.3.4", "manual"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
		}
	}
}

// The soak counts from the day GitHub published the release, so the sentence is
// the whole of what an operator needs: how long it has been public, how long
// auto waits, and how long is left.
func TestChooseAutoWaitsForTheSoak(t *testing.T) {
	r := published("v1.3.5", noon.Add(-6*time.Hour))
	got := Choose(asking(ModeAuto, "1.3.4", noon, r))
	if !got.Newer || got.Release.Tag != "v1.3.5" {
		t.Fatalf("Release = %q, newer = %v; want v1.3.5 and newer", got.Release.Tag, got.Newer)
	}
	if want := r.PublishedAt.Add(24 * time.Hour); !got.DueAt.Equal(want) {
		t.Errorf("DueAt = %s, want %s: the publication plus the soak", got.DueAt, want)
	}
	if got.DueBy(noon) {
		t.Error("due six hours into a soak of twenty-four")
	}
	const want = "Waiting: v1.3.5 has been public for 6 hours and auto waits for 24; it can be taken in 18 hours."
	if got.Reason != want {
		t.Errorf("Reason = %q, want %q", got.Reason, want)
	}
}

// "Is it due" is asked in one place, DueBy, and the sentence asks it too, so
// what the status says and what the planner does cannot disagree about the
// instant the wait ends. The wait is over at that instant, not after it, and a
// nanosecond earlier it is not.
func TestChooseAutoIsDueExactlyAtTheBoundary(t *testing.T) {
	r := published("v1.3.5", noon.Add(-6*time.Hour))
	due := Choose(asking(ModeAuto, "1.3.4", noon, r)).DueAt
	target := Choose(asking(ModeAuto, "1.3.4", due, r))

	if !target.DueBy(due) {
		t.Error("not due at the instant the soak ends")
	}
	if target.DueBy(due.Add(-time.Nanosecond)) {
		t.Error("due a nanosecond before the soak ends")
	}
	if !strings.HasPrefix(target.Reason, "Ready:") {
		t.Errorf("Reason at the boundary = %q, want it to start Ready:", target.Reason)
	}
	if early := Choose(asking(ModeAuto, "1.3.4", due.Add(-time.Nanosecond), r)); !strings.HasPrefix(early.Reason, "Waiting:") {
		t.Errorf("Reason a nanosecond early = %q, want it to start Waiting:", early.Reason)
	}
}

// The cost of the soak is that a release replaced inside it is never taken, and
// that is the point: v1.3.1 has been public for 25 hours here and would be due,
// but v1.3.2 landed four and a half hours after it, so the controller waits a
// day from v1.3.2 instead. v1.3.1 is not the target at any time, whichever way
// round GitHub lists the two, and the sentence says it was skipped: an operator
// looking at a release that has been public for 25 hours will ask why it was
// not taken.
func TestANewerReleaseRestartsTheSoak(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)
	first := published("v1.3.1", t0)
	second := published("v1.3.2", t0.Add(4*time.Hour+26*time.Minute))
	ends := second.PublishedAt.Add(24 * time.Hour)
	now := t0.Add(25 * time.Hour)

	inEveryRotation([]Release{first, second}, func(rotated []Release) {
		got := Choose(asking(ModeAuto, "1.3.0", now, rotated...))
		if got.Release.Tag != "v1.3.2" {
			t.Fatalf("target = %q from %s first, want v1.3.2", got.Release.Tag, rotated[0].Tag)
		}
		if !got.DueAt.Equal(ends) {
			t.Errorf("DueAt = %s, want %s: a day from v1.3.2's own publication", got.DueAt, ends)
		}
		if got.DueBy(now) {
			t.Error("due at 25 hours, which only v1.3.1 had waited for")
		}
		const want = "Waiting: v1.3.2 has been public for 20 hours and auto waits for 24; it can be taken in 3 hours. It replaced v1.3.1, which is skipped, and a newer release would start the wait again."
		if got.Reason != want {
			t.Errorf("Reason = %q, want %q", got.Reason, want)
		}
		for _, hours := range []int{5, 24, 25, 28, 29, 100} {
			at := t0.Add(time.Duration(hours) * time.Hour)
			if tag := Choose(asking(ModeAuto, "1.3.0", at, rotated...)).Release.Tag; tag != "v1.3.2" {
				t.Errorf("target = %q at %d hours, want v1.3.2 whenever it is asked", tag, hours)
			}
		}
		if !Choose(asking(ModeAuto, "1.3.0", ends, rotated...)).DueBy(ends) {
			t.Error("v1.3.2 is not due once its own day has passed")
		}
	})
}

// The price of the soak is that a project publishing faster than the wait is
// never updated by auto, and the sentence has to be the thing that says so. Five
// releases about five hours apart, 25 hours after the first: only the one just
// before the target is named, because the earlier ones were pushed aside by it
// in turn and a list of them would grow with every release.
func TestOnlyTheReleaseJustBeforeTheTargetIsNamedAsSkipped(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)
	releases := []Release{
		published("v1.3.1", t0),
		published("v1.3.2", t0.Add(5*time.Hour)),
		published("v1.3.3", t0.Add(10*time.Hour)),
		published("v1.3.4", t0.Add(15*time.Hour)),
		published("v1.3.5", t0.Add(19*time.Hour)),
	}
	now := t0.Add(25 * time.Hour)

	const want = "Waiting: v1.3.5 has been public for 6 hours and auto waits for 24; it can be taken in 18 hours. It replaced v1.3.4, which is skipped, and a newer release would start the wait again."
	inEveryRotation(releases, func(rotated []Release) {
		if got := Choose(asking(ModeAuto, "1.3.0", now, rotated...)); got.Reason != want {
			t.Errorf("Reason = %q from %s first, want %q", got.Reason, rotated[0].Tag, want)
		}
	})
}

// "It replaced v1.3.4, which is skipped" is true only of a release that could
// have been taken and was passed over. One that is already installed, older than
// what is running, or never complete, dated or final was never in the way, and
// naming it would send an operator looking for a release that cannot exist. In
// each case the sentence is the plain one, byte for byte.
func TestAReleaseThatWasNeverInTheWayIsNotCalledSkipped(t *testing.T) {
	const plain = "Waiting: v1.3.5 has been public for 6 hours and auto waits for 24; it can be taken in 18 hours."
	target := published("v1.3.5", noon.Add(-6*time.Hour))
	draft := published("v1.3.4", noon.Add(-20*time.Hour))
	draft.Draft = true
	prerelease := published("v1.3.4", noon.Add(-20*time.Hour))
	prerelease.Prerelease = true

	for _, tc := range []struct {
		name    string
		running string
		others  []Release
	}{
		{"nothing else was published", "1.3.4", nil},
		{"the only other release is the one running", "1.3.4", []Release{published("v1.3.4", noon.Add(-30*time.Hour))}},
		{"the only other release is older than the one running", "1.3.4", []Release{published("v1.3.2", noon.Add(-30*time.Hour))}},
		{"the other release is the target's own version, made again", "1.3.4", []Release{published("v1.3.5", noon.Add(-30*time.Hour))}},
		{"the release between is still uploading", "1.3.3", []Release{without(published("v1.3.4", noon.Add(-20*time.Hour)), "checksums.txt")}},
		{"the release between is a draft", "1.3.3", []Release{draft}},
		{"the release between is a prerelease", "1.3.3", []Release{prerelease}},
		{"the release between has no date", "1.3.3", []Release{published("v1.3.4", time.Time{})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inEveryRotation(append([]Release{target}, tc.others...), func(rotated []Release) {
				if got := Choose(asking(ModeAuto, tc.running, noon, rotated...)); got.Reason != plain {
					t.Errorf("Reason = %q from %s first, want the plain %q", got.Reason, rotated[0].Tag, plain)
				}
			})
		})
	}
}

// The second sentence belongs to a wait that was restarted and to nothing else.
// Once the wait is over the release is on its way and there is nothing to
// explain; a release dated in the future has not started a wait at all; and
// manual has no wait to restart, so a person asking is told what is there
// without a history of what auto would have done. Each of these has a skipped
// release in the list, which is what would tempt a careless rule to mention it.
func TestTheRestartIsOnlyMentionedWhileAutoIsWaiting(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)
	first := published("v1.3.1", t0)
	second := published("v1.3.2", t0.Add(4*time.Hour+26*time.Minute))
	ends := second.PublishedAt.Add(24 * time.Hour)

	for _, tc := range []struct {
		name string
		mode Mode
		now  time.Time
		want string
	}{
		{"the wait is over", ModeAuto, ends, "Ready: v1.3.2 has been public for 24 hours and auto waits for 24; it can be taken now."},
		{"the release is dated in the future", ModeAuto, t0.Add(time.Hour), "Waiting: v1.3.2 is dated in the future, so it does not count as public yet; it can be taken in 27 hours."},
		{"manual", ModeManual, t0.Add(25 * time.Hour), "Available: v1.3.2 is newer than the v1.3.0 running now; manual mode waits for someone to update."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inEveryRotation([]Release{first, second}, func(rotated []Release) {
				if got := Choose(asking(tc.mode, "1.3.0", tc.now, rotated...)); got.Reason != tc.want {
					t.Errorf("Reason = %q from %s first, want %q", got.Reason, rotated[0].Tag, tc.want)
				}
			})
		})
	}
}

// GitHub's clock and this controller's can disagree, and a release can be
// dated ahead on purpose. Either way it has not been public yet, so the wait
// has not started. The sentence must not show a negative age, which would read
// as "public for -1 hours".
func TestChooseTreatsAFuturePublishedAtAsNotYetDue(t *testing.T) {
	r := published("v1.3.5", noon.Add(time.Hour))
	got := Choose(asking(ModeAuto, "1.3.4", noon, r))
	if !got.Newer {
		t.Fatal("a release dated in the future was not offered as newer; it should be, and merely not yet due")
	}
	if got.DueBy(noon) {
		t.Error("due before the release was published")
	}
	if want := r.PublishedAt.Add(24 * time.Hour); !got.DueAt.Equal(want) {
		t.Errorf("DueAt = %s, want %s: the soak still counts from the date GitHub gave", got.DueAt, want)
	}
	if strings.Contains(got.Reason, "-") {
		t.Errorf("Reason = %q, which shows a negative number", got.Reason)
	}
	for _, want := range []string{"v1.3.5", "dated in the future", "25 hours"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
		}
	}
}

// The sentence is read at a glance and recomputed on every pass, so it says
// whole units and rounds down: a release is never said to have waited longer than
// it has, and the unit of the soak is left off when the age has just said it.
func TestChooseReasonsRoundDurationsDown(t *testing.T) {
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name      string
		age, soak time.Duration
		want      string
	}{
		{"just published", 0, day, "Waiting: v1.3.5 has been public for less than a minute and auto waits for 24 hours; it can be taken in 24 hours."},
		{"a few seconds in", 59 * time.Second, day, "Waiting: v1.3.5 has been public for less than a minute and auto waits for 24 hours; it can be taken in 23 hours."},
		{"one minute in", time.Minute, day, "Waiting: v1.3.5 has been public for 1 minute and auto waits for 24 hours; it can be taken in 23 hours."},
		{"a second short of an hour", time.Hour - time.Second, day, "Waiting: v1.3.5 has been public for 59 minutes and auto waits for 24 hours; it can be taken in 23 hours."},
		{"an hour in", time.Hour, day, "Waiting: v1.3.5 has been public for 1 hour and auto waits for 24; it can be taken in 23 hours."},
		{"a second short of two hours", 2*time.Hour - time.Second, day, "Waiting: v1.3.5 has been public for 1 hour and auto waits for 24; it can be taken in 22 hours."},
		{"an hour from the end", 23 * time.Hour, day, "Waiting: v1.3.5 has been public for 23 hours and auto waits for 24; it can be taken in 1 hour."},
		{"a minute from the end", day - time.Minute, day, "Waiting: v1.3.5 has been public for 23 hours and auto waits for 24; it can be taken in 1 minute."},
		{"half a minute from the end", day - 30*time.Second, day, "Waiting: v1.3.5 has been public for 23 hours and auto waits for 24; it can be taken in less than a minute."},
		{"a soak in minutes", 10 * time.Minute, 30 * time.Minute, "Waiting: v1.3.5 has been public for 10 minutes and auto waits for 30; it can be taken in 20 minutes."},
		{"a soak of an hour and a half", 30 * time.Minute, 90 * time.Minute, "Waiting: v1.3.5 has been public for 30 minutes and auto waits for 1 hour; it can be taken in 1 hour."},
		{"a soak of an hour", 10 * time.Minute, time.Hour, "Waiting: v1.3.5 has been public for 10 minutes and auto waits for 1 hour; it can be taken in 50 minutes."},
		{"an hour against a soak of an hour", time.Hour, time.Hour, "Ready: v1.3.5 has been public for 1 hour and auto waits for 1 hour; it can be taken now."},
		{"the day is up", day, day, "Ready: v1.3.5 has been public for 24 hours and auto waits for 24; it can be taken now."},
		{"long past the soak", 26 * time.Hour, day, "Ready: v1.3.5 has been public for 26 hours and auto waits for 24; it can be taken now."},
		{"no soak at all", 5 * time.Minute, 0, "Ready: v1.3.5 has been public for 5 minutes and auto does not wait; it can be taken now."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := asking(ModeAuto, "1.3.4", noon, published("v1.3.5", noon.Add(-tc.age)))
			in.Soak = tc.soak
			got := Choose(in)
			if got.Reason != tc.want {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.want)
			}
			if want := noon.Add(-tc.age).Add(tc.soak); !got.DueAt.Equal(want) {
				t.Errorf("DueAt = %s, want %s", got.DueAt, want)
			}
		})
	}
}
