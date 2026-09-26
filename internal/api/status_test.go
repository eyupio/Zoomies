package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

func withStatusMode(mode config.StatusMode) func(*config.Config) {
	return func(c *config.Config) { c.Status.Mode = mode }
}

// statusRoutes are the three ways the status is read, plus the built page's
// own file name, which the SPA would otherwise serve whatever the setting.
var statusRoutes = []string{"/api/v1/status", "/status", "/status.html", "/status.svg"}

// A default install serves nothing new: every status route is a 404, to
// anyone, signed in or not -- the same answer as a path that never existed,
// so turning the feature on is the only way anybody learns it is there.
func TestTheDefaultLeavesEveryStatusRouteA404(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	for _, path := range statusRoutes {
		for _, cookie := range []string{"", h.session(admin)} {
			resp := h.do(request{method: http.MethodGet, path: path, cookie: cookie})
			if resp.status != http.StatusNotFound {
				t.Errorf("GET %s (signed in: %v) = %d, want 404 while status.mode is off", path, cookie != "", resp.status)
			}
		}
	}
}

// Each mode lets in exactly who it says.
func TestTheStatusModeDecidesWhoMayRead(t *testing.T) {
	cases := []struct {
		mode      config.StatusMode
		anonymous int
		signedIn  int
	}{
		{config.StatusAuthenticated, http.StatusUnauthorized, http.StatusOK},
		{config.StatusPublic, http.StatusOK, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			h := newHarness(t, withStatusMode(tc.mode))
			viewer, _ := h.user("viewer", store.RoleViewer)
			for _, path := range statusRoutes {
				if got := h.do(request{method: http.MethodGet, path: path}).status; got != tc.anonymous {
					t.Errorf("anonymous GET %s = %d, want %d", path, got, tc.anonymous)
				}
				if got := h.do(request{method: http.MethodGet, path: path, cookie: h.session(viewer)}).status; got != tc.signedIn {
					t.Errorf("signed-in GET %s = %d, want %d", path, got, tc.signedIn)
				}
			}
		})
	}
}

// The API half of the fixture-name test: a fleet with distinctive names
// produces a body that carries none of them, in the shape the spec promises.
func TestTheStatusBodyNamesNothingInTheFleet(t *testing.T) {
	h := newHarness(t, withStatusMode(config.StatusPublic))
	inst := h.installation()
	pool := h.pool(inst, "wombatpool")
	host := h.host("platypushost")
	runner := h.runner(pool, host, store.RunnerBusy)
	job := h.job(pool, store.JobQueued)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/status"})
	resp.mustStatus(t, http.StatusOK, "the public status")
	for _, name := range []string{pool.Name, pool.ID, host.Name, host.ID, runner.Name, runner.ID, job.ID, job.Repo, inst.Target, inst.ID} {
		if strings.Contains(string(resp.body), name) {
			t.Errorf("the status body contains %q: %s", name, resp.body)
		}
	}

	var body map[string]json.RawMessage
	resp.into(t, &body)
	for _, key := range []string{"state", "since", "version", "queued", "running", "median_wait_minutes", "p95_wait_minutes", "reasons", "explanations"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the body has no %q: %s", key, resp.body)
		}
	}
	var reasons []map[string]any
	if err := json.Unmarshal(body["reasons"], &reasons); err != nil {
		t.Fatal(err)
	}
	for _, r := range reasons {
		for k := range r {
			if k != "code" && k != "severity" && k != "since" {
				t.Errorf("a reason carries %q; it may carry code, severity and since and nothing else", k)
			}
		}
	}
}

// The badge is an image a README can embed: SVG, the state in words, and a
// colour from the status mapping rather than one of its own.
func TestTheBadgeIsTheStateAsAnImage(t *testing.T) {
	h := newHarness(t, withStatusMode(config.StatusPublic))
	resp := h.do(request{method: http.MethodGet, path: "/status.svg"})
	resp.mustStatus(t, http.StatusOK, "the badge")
	if ct := resp.header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("Content-Type = %q", ct)
	}
	svg := string(resp.body)
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "fleet: ") {
		t.Fatalf("not a status badge: %s", svg)
	}
	found := false
	for _, colour := range badgeColours {
		found = found || strings.Contains(svg, colour)
	}
	if !found {
		t.Errorf("the badge uses none of the three state colours: %s", svg)
	}
}

// A badge fetched by somebody signed in must never land in a shared cache: a
// proxy or CDN would hand it to the next request for the same URL, signed in
// or not, without that request ever reaching the mode check. Only a public
// status may be cached publicly.
func TestOnlyAPublicBadgeMayBeCachedByAProxy(t *testing.T) {
	for _, tc := range []struct {
		mode   config.StatusMode
		public bool
	}{
		{config.StatusPublic, true},
		{config.StatusAuthenticated, false},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			h := newHarness(t, withStatusMode(tc.mode))
			viewer, _ := h.user("viewer", store.RoleViewer)
			resp := h.do(request{method: http.MethodGet, path: "/status.svg", cookie: h.session(viewer)})
			resp.mustStatus(t, http.StatusOK, "the badge")
			cc := resp.header.Get("Cache-Control")
			if got := strings.Contains(cc, "public"); got != tc.public {
				t.Errorf("Cache-Control = %q in %s mode; public caching allowed = %v, want %v", cc, tc.mode, got, tc.public)
			}
			if !tc.public && !strings.Contains(cc, "no-store") {
				t.Errorf("Cache-Control = %q; an authenticated badge must not be stored", cc)
			}
		})
	}
}
