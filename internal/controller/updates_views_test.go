package controller

import (
	"encoding/json"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/updates"
)

// readTheList makes the controller read a release list the way the background
// pass does, so the status is tested against what that check records and not
// against a state built by hand: the difference between a list that came back
// empty and one that was never read is made, or lost, in the decode.
func (h *harness) readTheList(entries ...map[string]any) {
	h.t.Helper()
	h.stubGitHub(http.StatusOK, releaseList(entries...))
	if err := h.c.checkForRelease(h.ctx); err != nil {
		h.t.Fatalf("checkForRelease: %v", err)
	}
}

// status renders the update status, which is the one renderer the route, the
// event stream and the metrics share.
func (h *harness) status() *UpdatesView {
	h.t.Helper()
	view, err := h.c.UpdatesView(h.ctx)
	if err != nil {
		h.t.Fatalf("UpdatesView: %v", err)
	}
	return view
}

// whenAgo is a moment this long before now, to the second GitHub gives one in.
// The tests that use it keep their ages clear of a whole hour, because the
// sentences count whole hours and a test sitting on that edge would go red now
// and then for no reason.
func whenAgo(d time.Duration) time.Time {
	return time.Now().UTC().Truncate(time.Second).Add(-d)
}

// A mode of off is a promise that nothing about a release is offered, and the
// promise is kept in the data and not only in the words: a client that reads the
// target and ignores the sentence must find none. The leftovers matter as much
// as the empty case -- a list read while updating was on is still held after it
// is switched off, and nothing in it may read as an offer.
func TestTheStatusSaysOffAndOffersNothingWhenTheModeIsOff(t *testing.T) {
	assets := completeAssets(t)
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, h *harness)
	}{
		{"nothing has been read", func(*testing.T, *harness) {}},
		{"only the latest release has been read", func(t *testing.T, h *harness) {
			h.stubGitHub(http.StatusOK, `{"tag_name":"v1.3.2","html_url":"https://example.invalid/releases/v1.3.2"}`)
			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("checkForRelease: %v", err)
			}
		}},
		{"a list was read while updating was on", func(_ *testing.T, h *harness) {
			h.inMode("manual")
			h.readTheList(releaseEntry("v1.3.2", whenAgo(6*time.Hour), assets...))
			h.inMode("off")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.0")
			tc.prepare(t, h)

			got := h.status()

			if got.Mode != "off" || got.Soak != "24h" {
				t.Errorf("mode %q and soak %q, want off and the 24h the setting holds", got.Mode, got.Soak)
			}
			if got.Running != (UpdatesRunning{Version: "1.3.0", Release: true}) {
				t.Errorf("running = %+v, want 1.3.0 from a release", got.Running)
			}
			if got.Latest != nil || got.Target != nil || got.CheckedAt != nil {
				t.Errorf("latest %+v, target %+v, checked at %v: want none of them while updating is off", got.Latest, got.Target, got.CheckedAt)
			}
			if !strings.HasPrefix(got.Reason, "Updates are off.") || !strings.Contains(got.Reason, "updates.mode") {
				t.Errorf("reason = %q, want the sentence that says updates are off and which setting turns them on", got.Reason)
			}
		})
	}
}

// What the status says about the target is the rule in internal/updates applied
// to what was read, and the sentence is that rule's own. A release that is
// public but still being uploaded is newer than the one it is passed over for,
// and must not be the one named.
func TestTheStatusNamesTheTargetAndWhenAutoWouldTakeIt(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.0")
	h.inMode("auto")
	assets := completeAssets(t)
	published := whenAgo(6*time.Hour + 30*time.Minute)
	h.readTheList(
		releaseEntry("v1.3.3", published.Add(5*time.Hour), assets[1]),
		releaseEntry("v1.3.2", published, assets...),
		releaseEntry("v1.3.1", published.Add(-24*time.Hour), assets...),
	)

	got := h.status()

	if got.Mode != "auto" || got.Soak != "24h" {
		t.Errorf("mode %q and soak %q, want auto and 24h", got.Mode, got.Soak)
	}
	if got.Latest == nil || got.Latest.Tag != "v1.3.2" || got.Latest.URL != "https://example.invalid/releases/v1.3.2" || !got.Latest.PublishedAt.Equal(published) {
		t.Fatalf("latest = %+v, want v1.3.2 with its page and the time GitHub published it, not the newer release that is missing its files", got.Latest)
	}
	if got.Target == nil || got.Target.Tag != "v1.3.2" || !got.Target.Newer {
		t.Fatalf("target = %+v, want v1.3.2, newer than the 1.3.0 running", got.Target)
	}
	// Counted from the day GitHub published it, so the time is fixed by the
	// release and does not slide with the clock.
	due := published.Add(24 * time.Hour)
	if got.Target.DueAt == nil || !got.Target.DueAt.Equal(due) {
		t.Errorf("due at %v, want %v: the release's publication plus the soak", got.Target.DueAt, due)
	}
	state := h.c.latestRelease()
	if got.CheckedAt == nil || !got.CheckedAt.Equal(state.At) {
		t.Errorf("checked at %v, want %v, when the list was read", got.CheckedAt, state.At)
	}
	// The sentence is internal/updates', word for word: the status adds none of
	// its own to a list that was read, so it cannot say one thing while the rule
	// does another.
	want := updates.Choose(updates.ChooseInput{
		Mode: updates.ModeAuto, Soak: 24 * time.Hour, Now: h.c.Now(), Running: "1.3.0",
		Releases: state.Releases, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	})
	if got.Reason != want.Reason {
		t.Errorf("reason = %q, want the rule's own: %q", got.Reason, want.Reason)
	}
	if !strings.HasPrefix(got.Reason, "Waiting: v1.3.2 ") {
		t.Errorf("reason = %q, want it to say v1.3.2 is waiting out the soak", got.Reason)
	}

	// The wait ends with no row written and nothing else to say so, which is why
	// the status is worked out afresh each time and not kept.
	h.advance(18 * time.Hour)
	later := h.status()
	if !strings.HasPrefix(later.Reason, "Ready: v1.3.2 ") {
		t.Errorf("reason after the soak = %q, want the sentence that says it can be taken now", later.Reason)
	}
	if later.Target == nil || later.Target.DueAt == nil || !later.Target.DueAt.Equal(due) {
		t.Errorf("target after the soak = %+v, want the same due time", later.Target)
	}
}

// A person pressing the button is the soak, so manual has nothing to wait for
// and no time to name. The release here is far younger than the soak, which is
// what would give a manual target a due time if the soak were applied to it.
func TestTheStatusOfManualModeNamesNoDueTime(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.0")
	h.inMode("manual")
	h.readTheList(releaseEntry("v1.3.2", whenAgo(90*time.Minute), completeAssets(t)...))

	got := h.status()

	if got.Target == nil || got.Target.Tag != "v1.3.2" || !got.Target.Newer {
		t.Fatalf("target = %+v, want v1.3.2, newer than the 1.3.0 running", got.Target)
	}
	if got.Target.DueAt != nil {
		t.Errorf("due at %v, want none: in manual mode the person asking is the only wait", got.Target.DueAt)
	}
	if !strings.HasPrefix(got.Reason, "Available: v1.3.2 ") {
		t.Errorf("reason = %q, want the sentence that says it is available and waits for someone", got.Reason)
	}
}

// Latest is the newest release that could be installed, whether or not the
// build is behind it, so the page can still say what the newest release is and
// link its notes; the target says there is nothing to take.
func TestTheStatusNamesTheNewestReleaseEvenWhenThereIsNothingNewerToTake(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.2")
	h.inMode("auto")
	published := whenAgo(40*time.Hour + 30*time.Minute)
	h.readTheList(releaseEntry("v1.3.2", published, completeAssets(t)...))

	got := h.status()

	if got.Latest == nil || got.Latest.Tag != "v1.3.2" || !got.Latest.PublishedAt.Equal(published) {
		t.Errorf("latest = %+v, want v1.3.2: it is still the newest release", got.Latest)
	}
	if got.Target == nil || got.Target.Tag != "v1.3.2" || got.Target.Newer {
		t.Errorf("target = %+v, want v1.3.2 and not newer", got.Target)
	}
	if got.Target != nil && got.Target.DueAt != nil {
		t.Errorf("due at %v, want none: there is nothing to take, so there is no time to take it", got.Target.DueAt)
	}
	if !strings.HasPrefix(got.Reason, "Up to date: v1.3.2 ") {
		t.Errorf("reason = %q, want the sentence that says the build is up to date", got.Reason)
	}
}

// Updating switched on is not the same as the list having been read. Nothing
// reads it on the switch -- the background pass does, on the check interval --
// so for a while the status has a mode and no list, and an empty answer from the
// rule would say that GitHub has no release to offer, which nobody has asked it.
// The status says what is true instead.
func TestTheStatusSaysSoWhenTheListHasNotBeenReadSinceUpdatingWasTurnedOn(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		for _, tc := range []struct {
			name    string
			prepare func(t *testing.T, h *harness)
		}{
			{"no check has answered", func(*testing.T, *harness) {}},
			// The answer the check gave while updating was off is the latest
			// release and nothing else, so it is a release without a list.
			{"only the latest release was read, while updating was off", func(t *testing.T, h *harness) {
				h.stubGitHub(http.StatusOK, `{"tag_name":"v1.3.4","html_url":"https://example.invalid/releases/v1.3.4"}`)
				if err := h.c.checkForRelease(h.ctx); err != nil {
					t.Fatalf("checkForRelease: %v", err)
				}
				if h.c.latestRelease() == nil {
					t.Fatal("the check recorded nothing, so this is not the state it is meant to test")
				}
			}},
		} {
			t.Run(mode+": "+tc.name, func(t *testing.T) {
				h := newHarness(t)
				withVersion(t, "1.3.0")
				tc.prepare(t, h)
				h.inMode(mode)

				got := h.status()

				if got.Mode != mode {
					t.Errorf("mode = %q, want %q", got.Mode, mode)
				}
				if !strings.Contains(got.Reason, "has not read the release list yet") || !strings.Contains(got.Reason, "every 24h") {
					t.Errorf("reason = %q, want it to say the list has not been read and how often it is", got.Reason)
				}
				// What GitHub holds is not known, and the sentence for a list
				// with nothing in it that can be taken would claim to know.
				if strings.Contains(got.Reason, "GitHub") || strings.Contains(got.Reason, "No complete release") {
					t.Errorf("reason = %q, want no claim about what is in a list nobody has read", got.Reason)
				}
				if got.Latest != nil || got.Target != nil || got.CheckedAt != nil {
					t.Errorf("latest %+v, target %+v, checked at %v: want none of them before a list is read", got.Latest, got.Target, got.CheckedAt)
				}
			})
		}
	}
}

// The other half of the distinction, and the reason it is worth drawing: a list
// that came back empty has been read, and the rule's own sentence about it -- what
// a complete release is, and what this system needs -- is the one to give.
func TestAListThatCameBackEmptyIsNotAListThatWasNeverRead(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.0")
	h.inMode("manual")
	h.readTheList()

	got := h.status()

	if !strings.HasPrefix(got.Reason, "No complete release is available for ") {
		t.Errorf("reason = %q, want the rule's sentence for a list with nothing in it that can be taken", got.Reason)
	}
	if strings.Contains(got.Reason, "not read") {
		t.Errorf("reason = %q, want no suggestion that the list is unread: it was read, and it was empty", got.Reason)
	}
	if got.CheckedAt == nil {
		t.Error("checked at is empty, but a list was read")
	}
	if got.Latest != nil || got.Target != nil {
		t.Errorf("latest %+v and target %+v, want neither: nothing in the list can be installed", got.Latest, got.Target)
	}
}

// updates.check_interval 0 is the air-gap switch, and with it the list is never
// read. Promising that it will be, within a time, would be false, and the
// operator who set a mode beside it needs to be told which setting to look at.
func TestTheStatusNamesTheSettingThatStopsTheListBeingRead(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.0")
	h.inMode("manual")
	h.c.live.Update(func(c *config.Config) { c.Updates.CheckInterval = 0 })

	got := h.status()

	if !strings.Contains(got.Reason, "has not read the release list") || !strings.Contains(got.Reason, "updates.check_interval") {
		t.Errorf("reason = %q, want it to say the list is unread and name updates.check_interval", got.Reason)
	}
	if strings.Contains(got.Reason, "every ") {
		t.Errorf("reason = %q, want no promise of a read that the setting rules out", got.Reason)
	}
}

// A build from main never reads the list: it follows the head of main in every
// mode, because it is usually ahead of the newest release. Saying that the list
// is about to be read would be wrong for as long as it ran, and would hide the
// true and more useful sentence, that updates leave it alone.
func TestTheStatusLeavesABuildThatIsNotFromAReleaseAlone(t *testing.T) {
	const alone = "This build is not from a release"
	for _, tc := range []struct {
		version string
		mode    string
		read    bool
		reason  string
	}{
		{"main-sha-abc1234", "manual", false, alone},
		{"dev", "manual", false, alone},
		{"dev", "manual", true, alone},
		// What `git describe` gives a local build: a release tag with commits on
		// top. It reads like a release and is not one that can be downloaded.
		{"v1.3.0-5-gabc1234", "manual", false, alone},
		{"v1.3.0-5-gabc1234", "manual", true, alone},
		{"main-sha-abc1234", "auto", false, alone},
		{"main-sha-abc1234", "auto", true, alone},
		// Off decides before the build is looked at, so it says that and not the
		// sentence about the build: nothing is offered either way.
		{"main-sha-abc1234", "off", false, "Updates are off."},
	} {
		name := tc.version + ", " + tc.mode + ", list not read"
		if tc.read {
			name = tc.version + ", " + tc.mode + ", list read"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, tc.version)
			h.inMode(tc.mode)
			if tc.read {
				h.readTheList(releaseEntry("v1.3.2", whenAgo(6*time.Hour), completeAssets(t)...))
			}

			got := h.status()

			if got.Running != (UpdatesRunning{Version: tc.version, Release: false}) {
				t.Errorf("running = %+v, want %s and not from a release", got.Running, tc.version)
			}
			if !strings.HasPrefix(got.Reason, tc.reason) {
				t.Errorf("reason = %q, want it to begin %q", got.Reason, tc.reason)
			}
			if got.Target != nil && got.Target.Newer {
				t.Errorf("target = %+v, want nothing to take: a build that is ahead of the releases would be taken back", got.Target)
			}
		})
	}
}

// The configuration refuses a mode it does not know when it loads, so one
// arriving here is a bug, and the status reads it as the controller does: as off.
// Reporting the word itself would show a mode the rest of the controller is not
// acting on.
func TestTheStatusReadsAModeNobodyDefinedAsOff(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.0")
	h.inMode("automatic")

	if got := h.status(); got.Mode != "off" {
		t.Errorf("mode = %q, want off", got.Mode)
	}
}

// The soak is written as the Settings page writes it, so the panel and the page
// that changes it never show one value two ways.
func TestTheSoakIsWrittenAsTheSettingsPageWritesIt(t *testing.T) {
	for _, tc := range []struct {
		soak time.Duration
		want string
	}{
		{24 * time.Hour, "24h"},
		{36 * time.Hour, "36h"},
		{90 * time.Minute, "1h30m"},
		{0, "0s"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.0")
			h.c.live.Update(func(c *config.Config) { c.Updates.Soak = tc.soak })

			if got := h.status(); got.Soak != tc.want {
				t.Errorf("soak = %q, want %q", got.Soak, tc.want)
			}
		})
	}
}

// The document says every field is always present, so a client reads a missing
// release as null and never has to ask whether the key is there.
func TestTheStatusAlwaysCarriesEveryFieldAndIsNullWhereThereIsNothing(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.0")
	raw, err := json.Marshal(h.status())
	if err != nil {
		t.Fatalf("marshalling the status: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("the status is not a JSON object: %v", err)
	}
	for _, key := range []string{"mode", "soak", "running", "latest", "target", "reason", "checked_at", "helper", "controller"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("the status has no %q: %s", key, raw)
		}
	}
	for _, key := range []string{"latest", "target", "checked_at", "controller"} {
		if got := string(fields[key]); got != "null" {
			t.Errorf("%s = %s, want null while there is nothing to name", key, got)
		}
	}
	if len(fields) != 9 {
		t.Errorf("the status has %d fields, want the nine the document lists: %s", len(fields), raw)
	}
}

// The mode names live in two places that cannot import each other: the choices
// the setting offers, in internal/config, and the modes the rule acts on, in
// internal/updates. A choice the second does not know would be offered on the
// Settings page, accepted, and then read as off by every pass, so the setting
// would say one thing while the controller did another. This is the test that
// notices when only one of the two has been told.
func TestEveryUpdateModeTheRegistryOffersIsOneThePlannerKnows(t *testing.T) {
	setting, ok := config.LookupSetting("updates.mode")
	if !ok {
		t.Fatal("updates.mode is not in the settings registry")
	}
	if !slices.Contains(setting.Choices, string(updates.ModeOff)) {
		t.Errorf("the registry offers %v, and off is not among them: off is the default and the way back", setting.Choices)
	}
	newer := []updates.Release{{
		Tag: "v1.3.2", PublishedAt: time.Now().Add(-48 * time.Hour),
		Assets: []string{"checksums.txt", updates.AssetName("linux", "amd64")},
	}}
	for _, choice := range setting.Choices {
		t.Run(choice, func(t *testing.T) {
			h := newHarness(t)
			h.inMode(choice)
			mode := updates.Mode(choice)

			if got := h.c.updateMode(); got != mode {
				t.Errorf("the controller reads %q as %q, want it taken as it is", choice, got)
			}
			target := updates.Choose(updates.ChooseInput{
				Mode: mode, Now: time.Now(), Running: "1.3.0", Releases: newer, GOOS: "linux", GOARCH: "amd64",
			})
			switch {
			case mode == updates.ModeOff && target.Release.Tag != "":
				t.Errorf("off offered %s", target.Release.Tag)
			case mode != updates.ModeOff && !target.Newer:
				t.Errorf("the planner does not act on %q: it offers nothing when a newer release is there, which is how it treats a mode it does not know", choice)
			}
		})
	}
}

// A check that finishes writes no row and publishes nothing of its own: the
// status is computed from what the check recorded, and the pass that follows
// sends the frame. So an open page hears about a finished check exactly once,
// from the pass, and a check does not need a publish call that could be forgotten
// or could say something the GET would not.
func TestAFinishedCheckReachesAnOpenStreamOnTheNextPassAndNotBefore(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	withVersion(t, "1.3.0")
	h.inMode("manual")
	sub := h.listen(events.KindUpdates)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	before := nextOfKind(t, sub, events.KindUpdates)
	if reason, _ := before["reason"].(string); !strings.Contains(reason, "has not read the release list") {
		t.Fatalf("frame before the check: reason = %q, want it to say the list is unread", reason)
	}

	h.readTheList(releaseEntry("v1.3.2", whenAgo(6*time.Hour), completeAssets(t)...))
	nothingFor(t, sub)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile after the check: %v", err)
	}
	after := nextOfKind(t, sub, events.KindUpdates)
	if target, _ := after["target"].(map[string]any); target["tag"] != "v1.3.2" || after["checked_at"] == nil {
		t.Errorf("frame after the check: target %v, checked_at %v, want v1.3.2 and the time it was read", after["target"], after["checked_at"])
	}
}

// The address is put in a link on the page, and it arrives off the network. Only
// an absolute https one is passed on; anything else leaves the release without a
// link and is never an error, because the page works without one.
func TestTheReleasePageIsLinkedOnlyWhenItIsAnAbsoluteHTTPSAddress(t *testing.T) {
	for _, tc := range []struct {
		name, url, want string
	}{
		{"a release page", "https://github.com/eyupio/zoomies/releases/tag/v1.3.5", "https://github.com/eyupio/zoomies/releases/tag/v1.3.5"},
		{"a script address", "javascript:alert(1)", ""},
		{"plain http", "http://x/", ""},
		{"https with no host", "https://", ""},
		{"a newline in the address", "https://github.com/eyupio/zoomies/releases/tag/v1.3.5\nx", ""},
		{"a tab in the address", "https://github.com/\teyupio", ""},
		{"a delete character in the address", "https://github.com/\x7f", ""},
		{"a relative address", "/eyupio/zoomies/releases", ""},
		{"nothing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := releasePageURL(tc.url); got != tc.want {
				t.Errorf("releasePageURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}

	// And the status uses it, so a list GitHub's answer put a bad address in is
	// shown without the link and not with it.
	h := newHarness(t)
	withVersion(t, "1.3.0")
	h.inMode("manual")
	entry := releaseEntry("v1.3.2", whenAgo(6*time.Hour), completeAssets(t)...)
	entry["html_url"] = "javascript:alert(1)"
	h.readTheList(entry)
	got := h.status()
	if got.Latest == nil || got.Latest.Tag != "v1.3.2" || got.Latest.URL != "" {
		t.Errorf("latest = %+v, want v1.3.2 with no link", got.Latest)
	}
}
