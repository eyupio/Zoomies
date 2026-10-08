package controller

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/version"
)

// A fixture that changes the version a controller reports must be something a
// real controller cannot be talked into by a variable that was left set, so it
// is inert until it is asked for -- and an empty value is not asking.
func TestTheUpdatesFixtureDoesNothingUntilItIsAskedFor(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T){
		"the variable is not set": func(t *testing.T) {
			t.Setenv(UpdatesSeedEnvVar, "")
			if err := os.Unsetenv(UpdatesSeedEnvVar); err != nil {
				t.Fatal(err)
			}
		},
		"the variable is empty": func(t *testing.T) { t.Setenv(UpdatesSeedEnvVar, "") },
		"the variable is blank": func(t *testing.T) { t.Setenv(UpdatesSeedEnvVar, "  ") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "dev")
			h.inMode("manual")
			client := h.c.httpClient
			prepare(t)

			if err := h.c.seedUpdates(h.ctx); err != nil {
				t.Fatalf("seedUpdates: %v", err)
			}

			if version.Version != "dev" {
				t.Errorf("version = %q, want the build's own", version.Version)
			}
			if h.c.httpClient != client {
				t.Error("the controller's HTTP client was replaced although the fixture was not asked for")
			}
			if h.c.latestRelease() != nil {
				t.Error("a release check ran although the fixture was not asked for")
			}
		})
	}
}

// The binary the browser suite drives is a dev build, which no comparison
// accepts, so the fixture has to make it a build that is behind a release and
// give it a release to be behind -- and read the list itself, because the first
// scheduled read is half a minute after start and the page is opened sooner.
func TestTheUpdatesFixtureMakesTheBuildBehindARelease(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "dev")
	h.inMode("manual")
	t.Setenv(UpdatesSeedEnvVar, t.TempDir())

	if err := h.c.seedUpdates(h.ctx); err != nil {
		t.Fatalf("seedUpdates: %v", err)
	}

	got := h.status()
	if got.Running != (UpdatesRunning{Version: "1.3.0", Release: true}) {
		t.Errorf("running = %+v, want 1.3.0 from a release", got.Running)
	}
	if got.Latest == nil || got.Latest.Tag != "v1.3.2" || got.Latest.URL != "https://example.invalid/releases/v1.3.2" {
		t.Fatalf("latest = %+v, want v1.3.2 and the https page its notes are on", got.Latest)
	}
	if got.Target == nil || got.Target.Tag != "v1.3.2" || !got.Target.Newer || got.Target.DueAt != nil {
		t.Errorf("target = %+v, want v1.3.2, newer, with no due time because manual waits for a person", got.Target)
	}
	if got.CheckedAt == nil {
		t.Error("no list was read, so the page opens on a status that says it has not read one")
	}
	if !strings.HasPrefix(got.Reason, "Available: v1.3.2 is newer than the v1.3.0 running now") {
		t.Errorf("reason = %q, want the sentence for a manual target that is ahead", got.Reason)
	}
}

// The two releases are not arbitrary: v1.3.1 has been public for longer than a
// soak and v1.3.2 for less, which is the case the design uses to show what
// auto's wait costs. If either were dated otherwise the fixture would stop
// showing it, and nothing else would notice.
func TestTheUpdatesFixtureHoldsAutoBackAndSaysWhyThroughTheNewerRelease(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "dev")
	// A frozen clock, because the sentence counts whole hours and the fixture is
	// dated from the moment it is made.
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	h.c.clock = func() time.Time { return now }
	h.inMode("auto")
	t.Setenv(UpdatesSeedEnvVar, t.TempDir())

	if err := h.c.seedUpdates(h.ctx); err != nil {
		t.Fatalf("seedUpdates: %v", err)
	}

	got := h.status()
	want := "Waiting: v1.3.2 has been public for 6 hours and auto waits for 24; it can be taken in 18 hours. " +
		"It replaced v1.3.1, which is skipped, and a newer release would start the wait again."
	if got.Reason != want {
		t.Errorf("reason = %q, want %q", got.Reason, want)
	}
	if got.Target == nil || got.Target.DueAt == nil || !got.Target.DueAt.Equal(now.Add(18*time.Hour)) {
		t.Errorf("target = %+v, want v1.3.2 due 18 hours from now", got.Target)
	}

	// The sentence is the same whatever age v1.3.1 is, so the ages are held here:
	// it has been public for more than the soak, which is why it is the release
	// that the newer one cost its turn, and v1.3.2 for less.
	age := map[string]time.Duration{}
	for _, r := range h.c.latestRelease().Releases {
		age[r.Tag] = now.Sub(r.PublishedAt)
	}
	if age["v1.3.1"] <= 24*time.Hour || age["v1.3.2"] >= 24*time.Hour || age["v1.3.1"] <= age["v1.3.2"] {
		t.Errorf("ages = %v, want v1.3.1 older than the 24h soak and v1.3.2 younger than it", age)
	}
}

// The client it replaces is also the one capacity-demand events leave by, so
// whatever the fixture does not know about has to go where it would have gone.
func TestTheUpdatesFixtureOnlyAnswersTheReleaseList(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "dev")
	h.inMode("manual")
	var asked []string
	h.c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		asked = append(asked, r.URL.String())
		return jsonResponse(http.StatusOK, `{}`)
	})}
	t.Setenv(UpdatesSeedEnvVar, t.TempDir())

	if err := h.c.seedUpdates(h.ctx); err != nil {
		t.Fatalf("seedUpdates: %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("the replaced client was asked for %v; the list is the fixture's to answer", asked)
	}

	const other = "https://hooks.example.invalid/capacity"
	resp, err := h.c.httpClient.Post(other, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("a request the fixture does not know: %v", err)
	}
	resp.Body.Close()
	if !slices.Equal(asked, []string{other}) {
		t.Errorf("the replaced client was asked for %v, want exactly %s", asked, other)
	}
}

// The demo fleet is what refuses to run on a real one, so the fixture rides on
// it: a controller that is not a demo never has its version rewritten, whatever
// is left in its environment.
func TestTheUpdatesFixtureStartsWithTheDemoFleetAndNowhereElse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		demo    bool
		version string
	}{
		{"with the demo fleet", true, "1.3.0"},
		{"without the demo fleet", false, "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "dev")
			h.inMode("manual")
			t.Setenv(SeedEnvVar, strconv.FormatBool(tc.demo))
			t.Setenv(UpdatesSeedEnvVar, t.TempDir())

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := h.c.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			if err := h.c.Stop(stopCtx); err != nil {
				t.Fatalf("Stop: %v", err)
			}

			if version.Version != tc.version {
				t.Errorf("version = %q after Start, want %q", version.Version, tc.version)
			}
		})
	}
}
