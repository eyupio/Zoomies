package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

// What an update would take is read out of this controller's own state and
// names nothing of the fleet's, so a viewer -- the lowest role there is -- reads
// it. The route table is where the role is written down and walked, so the test
// starts from its row: a status route that was added without one would be
// reachable by nobody's test.
func TestAViewerCanReadTheUpdatesStatus(t *testing.T) {
	h := newHarness(t)
	// The build under test is "dev", which no release comparison accepts, and the
	// status would say so and nothing else.
	was := version.Version
	version.Version = "1.3.0"
	t.Cleanup(func() { version.Version = was })

	var row *route
	for _, rt := range routeTable(h.fixtures()) {
		if rt.method == http.MethodGet && rt.path == "/api/v1/updates" {
			row = &rt
		}
	}
	if row == nil {
		t.Fatal("the route table has no row for GET /api/v1/updates, so no walk checks who may call it")
	}
	if row.role != store.RoleViewer || row.action != auth.ActionUpdatesRead {
		t.Errorf("the route table says %s needs %s and %s; want viewer and %s", row.path, row.role, row.action, auth.ActionUpdatesRead)
	}

	viewer, _ := h.user("viewer", store.RoleViewer)
	read := func(doing string) updatesResponse {
		t.Helper()
		resp := h.do(request{method: row.method, path: row.path, cookie: h.session(viewer)})
		resp.mustStatus(t, http.StatusOK, doing)
		var status updatesResponse
		resp.into(t, &status)
		return status
	}

	// Updating is off until somebody turns it on, and the status says so with the
	// build it is about and the setting that changes it.
	off := read("a viewer reading the update status")
	if off.Mode != "off" || off.Running.Version != "1.3.0" || !off.Running.Release {
		t.Errorf("status = %+v, want off, running 1.3.0 from a release", off)
	}
	if !strings.HasPrefix(off.Reason, "Updates are off.") {
		t.Errorf("reason = %q, want the sentence that says updates are off", off.Reason)
	}
	if off.Latest != nil || off.Target != nil {
		t.Errorf("latest %+v and target %+v, want neither while updating is off", off.Latest, off.Target)
	}

	// Turned on, and not yet read: the page that reads this is the first thing an
	// operator opens after changing the mode, and it has to say what is going on
	// rather than that there is nothing to take.
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	on := read("a viewer reading the update status once updating is on")
	if on.Mode != "manual" || !strings.Contains(on.Reason, "has not read the release list yet") {
		t.Errorf("status = %+v, want manual, and a reason that says the list has not been read", on)
	}
}

// The document names the modes the status can report, and the Settings page
// offers the same three; a generated client that was given a fourth, or one
// fewer, would be wrong about a word it switches on. There are now three places
// those words live, and this is the one that keeps the document in step with the
// setting.
func TestTheUpdateStatusModeEnumListsEveryModeTheSettingOffers(t *testing.T) {
	doc := loadSpec(t)
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	status, _ := schemas["UpdatesStatus"].(map[string]any)
	props, _ := status["properties"].(map[string]any)
	mode, _ := props["mode"].(map[string]any)
	enum, _ := mode["enum"].([]any)

	var listed []string
	for _, v := range enum {
		if s, ok := v.(string); ok {
			listed = append(listed, s)
		}
	}
	setting, ok := config.LookupSetting("updates.mode")
	if !ok {
		t.Fatal("updates.mode is not in the settings registry")
	}
	offered := slices.Clone(setting.Choices)
	slices.Sort(listed)
	slices.Sort(offered)
	if !slices.Equal(listed, offered) {
		t.Errorf("UpdatesStatus.mode lists %v, but updates.mode offers %v", listed, offered)
	}
}

// ---------------------------------------------------------------------------
// The check and the controller update
// ---------------------------------------------------------------------------

// releaseFeed stands in for api.github.com. It answers every request with a
// refusal until a test gives it a release list, so that a route that asks GitHub
// something, in a walk that calls it as every role, reaches nothing outside the
// test. Any other host is left to the real transport, because the controller's
// client also delivers the capacity-demand events that other tests listen for.
type releaseFeed struct {
	mu   sync.Mutex
	list string
	// refuse, when non-zero, is the status GitHub answers with whatever the list
	// is; down is a network that cannot be reached at all.
	refuse int
	down   error
}

func (f *releaseFeed) serve(list string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = list
}

func (f *releaseFeed) answer(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = status
}

func (f *releaseFeed) unreachable(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = err
}

func (f *releaseFeed) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.github.com" {
		return http.DefaultTransport.RoundTrip(r)
	}
	f.mu.Lock()
	list, refuse, down := f.list, f.refuse, f.down
	f.mu.Unlock()
	if down != nil {
		return nil, down
	}
	status := http.StatusOK
	switch {
	case refuse != 0:
		status, list = refuse, `{"message":"refused in a test"}`
	case list == "":
		status, list = http.StatusServiceUnavailable, `{"message":"no network in tests"}`
	}
	return &http.Response{
		StatusCode: status, Body: io.NopCloser(strings.NewReader(list)), Request: r,
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// useVersion stamps the build for one test: the whole question an update asks is
// what the binary was built from, and the test binary is "dev".
func useVersion(t *testing.T, v string) {
	t.Helper()
	was := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = was })
}

// installHelper makes the update folder as the installer leaves it, marker and
// all, where the controller was told to look.
func (h *harness) installHelper() {
	h.t.Helper()
	if err := os.MkdirAll(h.updateDir, 0o750); err != nil {
		h.t.Fatalf("making the update folder: %v", err)
	}
	marker, err := json.Marshal(channel.Marker{
		V: updates.WireVersion, Version: "1.3.4", Binary: "/usr/local/bin/zoomies", InstalledAt: time.Now().UTC(),
	})
	if err != nil {
		h.t.Fatalf("encoding the marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.updateDir, channel.MarkerFile), marker, 0o644); err != nil {
		h.t.Fatalf("writing the marker: %v", err)
	}
}

// offerRelease makes GitHub's list hold one release this system could install.
func (h *harness) offerRelease(tag string) {
	h.t.Helper()
	binary := updates.AssetName(runtime.GOOS, runtime.GOARCH)
	if binary == "" {
		h.t.Skipf("no release is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	raw, err := json.Marshal([]map[string]any{{
		"tag_name": tag, "html_url": "https://example.invalid/releases/" + tag,
		"published_at": time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339),
		"assets":       []map[string]any{{"name": "checksums.txt"}, {"name": binary}},
	}})
	if err != nil {
		h.t.Fatalf("encoding the release list: %v", err)
	}
	h.feed.serve(string(raw))
}

// updateCallers is one signed-in session per role.
type updateCallers struct {
	viewer, operator, admin, platform string
}

func (h *harness) updateCallers() updateCallers {
	h.t.Helper()
	session := func(name string, role store.Role) string {
		u, _ := h.user(name, role)
		return h.session(u)
	}
	return updateCallers{
		viewer:   session("update-viewer", store.RoleViewer),
		operator: session("update-operator", store.RoleOperator),
		admin:    session("update-admin", store.RoleAdmin),
		platform: session("update-platform", store.RolePlatform),
	}
}

// readyToUpdate is a controller on 1.3.4, in manual mode, offered v1.3.5 by the
// list an administrator has just asked for, with a helper beside it: everything
// a press of the controller button needs.
func (h *harness) readyToUpdate(as updateCallers) {
	h.t.Helper()
	useVersion(h.t, "1.3.4")
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	h.offerRelease("v1.3.5")
	h.installHelper()
	h.do(request{method: http.MethodPost, path: "/api/v1/updates/check", cookie: as.admin}).
		mustStatus(h.t, http.StatusOK, "reading the release list")
}

func (h *harness) updateAudit(action string) []*store.AuditEvent {
	h.t.Helper()
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{action}}, store.Page{Limit: 10})
	if err != nil {
		h.t.Fatalf("ListAudit: %v", err)
	}
	return rows
}

func (h *harness) openUpdateAttempts() []store.UpdateAttempt {
	h.t.Helper()
	open, err := h.st.OpenUpdateAttempts(h.ctx)
	if err != nil {
		h.t.Fatalf("OpenUpdateAttempts: %v", err)
	}
	return open
}

// routeRow finds one row of the route table, because the table is where a role is
// written down and walked.
func routeRow(t *testing.T, method, path string) route {
	t.Helper()
	for _, rt := range routeTable(fixtureIDs{}) {
		if rt.method == method && rt.path == path {
			return rt
		}
	}
	t.Fatalf("the route table has no row for %s %s, so no walk checks who may call it", method, path)
	return route{}
}

const (
	updatesCheckPath      = "/api/v1/updates/check"
	updatesControllerPath = "/api/v1/updates/controller"
)

// Asking GitHub is the administrator's, and replacing the controller's binary is
// the platform's: it is the one update that ends the process serving the request,
// on a host the fleet's administrator may not own.
func TestOnlyThePlatformMayUpdateTheControllerAndAnAdministratorMayCheck(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdate(as)

	check := routeRow(t, http.MethodPost, updatesCheckPath)
	if check.role != store.RoleAdmin || check.action != auth.ActionUpdatesCheck {
		t.Errorf("the route table says %s needs %s and %s; want admin and %s", check.path, check.role, check.action, auth.ActionUpdatesCheck)
	}
	apply := routeRow(t, http.MethodPost, updatesControllerPath)
	if apply.role != store.RolePlatform || apply.action != auth.ActionUpdatesApply {
		t.Errorf("the route table says %s needs %s and %s; want platform and %s", apply.path, apply.role, apply.action, auth.ActionUpdatesApply)
	}

	for _, tc := range []struct {
		role   store.Role
		cookie string
		check  int
		apply  int
	}{
		{store.RoleViewer, as.viewer, http.StatusForbidden, http.StatusForbidden},
		{store.RoleOperator, as.operator, http.StatusForbidden, http.StatusForbidden},
		{store.RoleAdmin, as.admin, http.StatusOK, http.StatusForbidden},
		{store.RolePlatform, as.platform, http.StatusOK, http.StatusAccepted},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			got := h.do(request{method: http.MethodPost, path: updatesCheckPath, cookie: tc.cookie})
			got.mustStatus(t, tc.check, "asking for a release as "+string(tc.role))
			if tc.check == http.StatusForbidden && !strings.Contains(got.errorMessage(t), "admin") {
				t.Errorf("the refusal does not name the admin role: %q", got.errorMessage(t))
			}
			got = h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: tc.cookie})
			got.mustStatus(t, tc.apply, "updating the controller as "+string(tc.role))
			if tc.apply == http.StatusForbidden && !strings.Contains(got.errorMessage(t), "platform") {
				t.Errorf("the refusal does not name the platform role: %q", got.errorMessage(t))
			}
		})
	}
	if open := h.openUpdateAttempts(); len(open) != 1 {
		t.Errorf("%d attempts open after four callers pressed the button, want the platform's one", len(open))
	}
}

// The check answers with the status it has just learnt, and the controller route
// with the status that holds the attempt it opened: the page repaints from the
// response, with no second request to make.
func TestTheCheckAndTheControllerRouteAnswerWithTheStatus(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	useVersion(t, "1.3.4")
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	h.offerRelease("v1.3.5")
	h.installHelper()
	doc := loadSpec(t)

	checked := h.do(request{method: http.MethodPost, path: updatesCheckPath, cookie: as.admin})
	checked.mustStatus(t, http.StatusOK, "asking for a release")
	assertShape(t, doc, "UpdatesStatus", checked.body)
	var status updatesResponse
	checked.into(t, &status)
	if status.Latest == nil || status.Latest.Tag != "v1.3.5" || status.CheckedAt == nil {
		t.Errorf("the check answered %+v, want the release it has just read", status)
	}

	started := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
	started.mustStatus(t, http.StatusAccepted, "updating the controller")
	assertShape(t, doc, "UpdatesStatus", started.body)
	var after updatesResponse
	started.into(t, &after)
	if after.Controller == nil || after.Controller.State != "requested" || after.Controller.To != "v1.3.5" || after.Controller.Trigger != "manual" {
		t.Errorf("the controller route answered %+v, want the attempt it opened, requested, for v1.3.5", after.Controller)
	}
	if open := h.openUpdateAttempts(); len(open) != 1 || after.Controller == nil || open[0].ID != after.Controller.ID {
		t.Errorf("open attempts = %+v, want the one the response names", open)
	}
}

// A tag is optional, and one that is in the list is taken as it stands.
func TestTheControllerRouteTakesATagThatIsInTheList(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdate(as)

	resp := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform, body: map[string]any{"tag": "v1.3.5"}})
	resp.mustStatus(t, http.StatusAccepted, "updating the controller to a named tag")
	if open := h.openUpdateAttempts(); len(open) != 1 || open[0].ToVersion != "v1.3.5" {
		t.Errorf("open attempts = %+v, want one for v1.3.5", open)
	}
}

// wantUpdateRefusals is every sentinel in the controller's updates_errors.go with
// the status and code the API gives it. The fenced one has no code of its own:
// it is the plain conflict every other handler gives a controller that may not
// act.
var wantUpdateRefusals = []struct {
	name   string
	err    error
	status int
	code   string
}{
	{"ErrUpdateCheckDisabled", controller.ErrUpdateCheckDisabled, http.StatusConflict, "update.check_disabled"},
	{"ErrUpdateModeOff", controller.ErrUpdateModeOff, http.StatusConflict, "update.mode_off"},
	{"ErrUpdateHelperMissing", controller.ErrUpdateHelperMissing, http.StatusConflict, "update.helper_missing"},
	{"ErrUpdateInProgress", controller.ErrUpdateInProgress, http.StatusConflict, "update.in_progress"},
	{"ErrUpdateNotARelease", controller.ErrUpdateNotARelease, http.StatusConflict, "update.not_a_release"},
	{"ErrUpdateNothingNewer", controller.ErrUpdateNothingNewer, http.StatusConflict, "update.nothing_newer"},
	{"ErrUpdateHostCannotUpdate", controller.ErrUpdateHostCannotUpdate, http.StatusConflict, "update.host_cannot_update"},
	{"ErrUpdateRolloutHalted", controller.ErrUpdateRolloutHalted, http.StatusConflict, "update.rollout_halted"},
	{"ErrUpdateFenced", controller.ErrUpdateFenced, http.StatusConflict, "conflict"},
	// Not a refusal of the request: GitHub would not let the check finish.
	{"ErrUpdateCheckFailed", controller.ErrUpdateCheckFailed, http.StatusBadGateway, "update.check_failed"},
}

// A refusal a client can do something different about has a code of its own, and
// it survives the detail the controller wraps around it: which folder, which
// release.
func TestEachUpdateRefusalCarriesItsStableCode(t *testing.T) {
	h := newHarness(t)
	for _, tc := range wantUpdateRefusals {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{tc.err, fmt.Errorf("%w: some detail the person needs", tc.err)} {
				rec := httptest.NewRecorder()
				h.api.failUpdate(rec, httptest.NewRequest(http.MethodPost, updatesControllerPath, nil), err)
				resp := &response{status: rec.Code, body: rec.Body.Bytes()}
				resp.mustStatus(t, tc.status, "refusing with "+tc.name)
				if got := resp.errorCode(t); got != tc.code {
					t.Errorf("%v has code %q, want %q", err, got, tc.code)
				}
				if got := resp.errorMessage(t); got != err.Error() {
					t.Errorf("message = %q, want the sentence the controller wrote: %q", got, err.Error())
				}
			}
		})
	}

	t.Run("anything else is a server failure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.api.failUpdate(rec, httptest.NewRequest(http.MethodPost, updatesControllerPath, nil), errors.New("the disk is full"))
		resp := &response{status: rec.Code, body: rec.Body.Bytes()}
		resp.mustStatus(t, http.StatusInternalServerError, "an error that is not a refusal")
		if strings.Contains(string(resp.body), "the disk is full") {
			t.Errorf("the cause is in the response: %s", resp.body)
		}
	})
}

// A refusal added to the controller without a code would reach the page as a 500
// that blames the server for something a person can fix. The sentinels are read
// out of the file that declares them, so that adding one fails here until it has
// a row in the table above, and a row in the table fails until the mapper
// answers it.
func TestEveryUpdateRefusalTheControllerDeclaresHasACode(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "controller", "updates_errors.go"), nil, 0)
	if err != nil {
		t.Fatalf("reading the controller's update refusals: %v", err)
	}
	var declared []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				if strings.HasPrefix(name.Name, "Err") {
					declared = append(declared, name.Name)
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no refusals in updates_errors.go, so this checked nothing")
	}

	h := newHarness(t)
	mapped := map[string]bool{}
	for _, tc := range wantUpdateRefusals {
		mapped[tc.name] = true
		rec := httptest.NewRecorder()
		h.api.failUpdate(rec, httptest.NewRequest(http.MethodPost, updatesControllerPath, nil), tc.err)
		if rec.Code == http.StatusInternalServerError {
			t.Errorf("%s is answered as a server failure; give it a code in failUpdate", tc.name)
		}
	}
	for _, name := range declared {
		if !mapped[name] {
			t.Errorf("controller.%s has no row in wantUpdateRefusals, so nothing checks that it has a code", name)
		}
	}
	if len(mapped) != len(declared) {
		t.Errorf("wantUpdateRefusals names %d refusals and updates_errors.go declares %d", len(mapped), len(declared))
	}
}

// Each refusal as a person meets it: through the route, from the state that
// causes it, with the audit trail untouched, because nothing was done.
func TestTheUpdateRoutesRefuseWithTheirCodesFromTheStateThatCausesThem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness, as updateCallers)
		do    func(h *harness, as updateCallers) *response
		code  string
	}{
		{
			name:  "updates are off",
			setup: func(h *harness, as updateCallers) { useVersion(h.t, "1.3.4") },
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
			},
			code: "update.mode_off",
		},
		{
			name: "the release check is switched off",
			setup: func(h *harness, as updateCallers) {
				h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.CheckInterval = 0 })
			},
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesCheckPath, cookie: as.admin})
			},
			code: "update.check_disabled",
		},
		{
			name: "the build is not a release",
			setup: func(h *harness, as updateCallers) {
				h.readyToUpdate(as)
				useVersion(h.t, "main-sha-abc1234")
			},
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
			},
			code: "update.not_a_release",
		},
		{
			name: "there is no helper",
			setup: func(h *harness, as updateCallers) {
				h.readyToUpdate(as)
				if err := os.Remove(filepath.Join(h.updateDir, channel.MarkerFile)); err != nil {
					h.t.Fatalf("removing the marker: %v", err)
				}
			},
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
			},
			code: "update.helper_missing",
		},
		{
			name:  "the tag is not in the list",
			setup: func(h *harness, as updateCallers) { h.readyToUpdate(as) },
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform, body: map[string]any{"tag": "v9.9.9"}})
			},
			code: "update.nothing_newer",
		},
		{
			name:  "the tag is not the shape of a release",
			setup: func(h *harness, as updateCallers) { h.readyToUpdate(as) },
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform, body: map[string]any{"tag": "latest"}})
			},
			code: "update.nothing_newer",
		},
		{
			name: "the release is the one already running",
			setup: func(h *harness, as updateCallers) {
				h.readyToUpdate(as)
				useVersion(h.t, "1.3.5")
			},
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
			},
			code: "update.nothing_newer",
		},
		{
			name: "an update is already in flight",
			setup: func(h *harness, as updateCallers) {
				h.readyToUpdate(as)
				h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform}).
					mustStatus(h.t, http.StatusAccepted, "the first press")
				// The helper has taken its request, so the second press is refused
				// for the attempt and not for the request that is still waiting.
				if err := os.Remove(filepath.Join(h.updateDir, channel.RequestFile)); err != nil {
					h.t.Fatalf("taking the request: %v", err)
				}
			},
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
			},
			code: "update.in_progress",
		},
		{
			name: "the controller may not act",
			setup: func(h *harness, as updateCallers) {
				h.readyToUpdate(as)
				if err := h.st.SetRecoveryFence(h.ctx, true, "restored from a copy"); err != nil {
					h.t.Fatalf("fencing: %v", err)
				}
				if err := h.ctrl.LoadFence(h.ctx); err != nil {
					h.t.Fatalf("loading the fence: %v", err)
				}
			},
			do: func(h *harness, as updateCallers) *response {
				return h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform})
			},
			code: "conflict",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			as := h.updateCallers()
			tc.setup(h, as)
			before := len(h.updateAudit("update.controller_requested"))

			resp := tc.do(h, as)
			resp.mustStatus(t, http.StatusConflict, tc.name)
			if got := resp.errorCode(t); got != tc.code {
				t.Errorf("code = %q, want %q (message %q)", got, tc.code, resp.errorMessage(t))
			}
			if after := len(h.updateAudit("update.controller_requested")); after != before {
				t.Errorf("a refused press wrote %d audit rows, want none: nothing was done", after-before)
			}
		})
	}
}

// A typo in a field name would otherwise be a 202 for a request that did not say
// what its sender meant, on the one button that replaces the binary.
func TestAnUnknownFieldInTheBodyIsRefused(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdate(as)

	for _, tc := range []struct {
		name  string
		body  any
		field string
	}{
		{"a field nobody defined", map[string]any{"tag": "v1.3.5", "force": true}, "force"},
		{"a typo for tag", map[string]any{"tga": "v1.3.5"}, "tga"},
		{"a tag that is not text", map[string]any{"tag": 135}, "tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform, body: tc.body})
			resp.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			if got := resp.errorCode(t); got != "unprocessable" {
				t.Errorf("code = %q, want unprocessable", got)
			}
			var env errorEnvelope
			resp.into(t, &env)
			if env.Error.Field != tc.field {
				t.Errorf("field = %q, want %q: the message is %q", env.Error.Field, tc.field, env.Error.Message)
			}
		})
	}
	if open := h.openUpdateAttempts(); len(open) != 0 {
		t.Errorf("%d attempts open after refused bodies, want none", len(open))
	}
	if rows := h.updateAudit("update.controller_requested"); len(rows) != 0 {
		t.Errorf("%d audit rows after refused bodies, want none", len(rows))
	}

	// And a body that is empty, or says nothing, is the newest release.
	h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform, body: map[string]any{}}).
		mustStatus(t, http.StatusAccepted, "a body with no fields")
}

// The audit trail names the person who pressed the button, and the controller
// records the same name as the one who asked, so that the attempt and the trail
// say the same thing. It is written after the controller has said yes, and not
// before: a press that did nothing is not something somebody did.
func TestTheControllerRouteRecordsAnAuditRowForTheCaller(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdate(as)
	lead, _ := h.user("ops-lead", store.RolePlatform)

	resp := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: h.session(lead)})
	resp.mustStatus(t, http.StatusAccepted, "updating the controller")
	var status updatesResponse
	resp.into(t, &status)
	if status.Controller == nil {
		t.Fatal("the response holds no attempt")
	}

	rows := h.updateAudit("update.controller_requested")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	row := rows[0]
	if row.ActorID != lead.ID || row.ActorName != "ops-lead" || row.TargetKind != "update" || row.TargetID != status.Controller.ID {
		t.Errorf("audit row = %+v, want ops-lead's, about the attempt %s", row, status.Controller.ID)
	}
	if !strings.Contains(row.After, "v1.3.5") || !strings.Contains(row.After, "1.3.4") {
		t.Errorf("audit detail = %q, want what it was from and to", row.After)
	}
	attempts, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeController, "", 5)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v, %v", attempts, err)
	}
	if attempts[0].RequestedBy != "ops-lead" {
		t.Errorf("the attempt says %q asked, want ops-lead", attempts[0].RequestedBy)
	}
}

// failedUpdate presses the button when the folder cannot take the request, which
// is the attempt whose error names a path: a request is already waiting there.
// It returns the folder, as it reads in JSON.
func (h *harness) failedUpdate(as updateCallers) string {
	h.t.Helper()
	h.readyToUpdate(as)
	if err := os.WriteFile(filepath.Join(h.updateDir, channel.RequestFile), []byte("{}"), 0o640); err != nil {
		h.t.Fatalf("leaving a request in the folder: %v", err)
	}
	h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform}).
		mustStatus(h.t, http.StatusConflict, "pressing the button with a request already waiting")
	quoted, err := json.Marshal(h.updateDir)
	if err != nil {
		h.t.Fatalf("encoding the folder: %v", err)
	}
	return strings.Trim(string(quoted), `"`)
}

// The helper's sentence and the controller's about a folder it could not write
// are for whoever owns the host. A viewer's page does not need the layout of the
// controller's filesystem to know the update failed, and an administrator of the
// fleet is not the platform's operator.
func TestTheStatusWithholdsTheHelpersTextBelowPlatform(t *testing.T) {
	t.Run("from GET /updates", func(t *testing.T) {
		h := newHarness(t)
		as := h.updateCallers()
		folder := h.failedUpdate(as)

		for _, tc := range []struct {
			role     string
			cookie   string
			platform bool
		}{
			{"viewer", as.viewer, false},
			{"operator", as.operator, false},
			{"admin", as.admin, false},
			{"platform", as.platform, true},
		} {
			t.Run(tc.role, func(t *testing.T) {
				resp := h.do(request{method: http.MethodGet, path: "/api/v1/updates", cookie: tc.cookie})
				resp.mustStatus(t, http.StatusOK, "reading the status")
				var status updatesResponse
				resp.into(t, &status)
				assertWithheldBelowPlatform(t, tc.role, tc.platform, status, string(resp.body), folder)
			})
		}
	})

	// The check answers with the same status, and an administrator is the lowest
	// role that may ask for it.
	t.Run("from POST /updates/check", func(t *testing.T) {
		h := newHarness(t)
		as := h.updateCallers()
		folder := h.failedUpdate(as)

		for _, tc := range []struct {
			role     string
			cookie   string
			platform bool
		}{
			{"admin", as.admin, false},
			{"platform", as.platform, true},
		} {
			t.Run(tc.role, func(t *testing.T) {
				resp := h.do(request{method: http.MethodPost, path: updatesCheckPath, cookie: tc.cookie})
				resp.mustStatus(t, http.StatusOK, "checking for a release")
				var status updatesResponse
				resp.into(t, &status)
				assertWithheldBelowPlatform(t, tc.role, tc.platform, status, string(resp.body), folder)
			})
		}
	})

	t.Run("from the event stream", func(t *testing.T) {
		h := newHarness(t)
		as := h.updateCallers()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		type watcher struct {
			role     string
			platform bool
			frames   <-chan sseFrame
		}
		var watchers []watcher
		for _, tc := range []struct {
			role     string
			cookie   string
			platform bool
		}{
			{"viewer", as.viewer, false},
			{"operator", as.operator, false},
			{"admin", as.admin, false},
			{"platform", as.platform, true},
		} {
			frames, _ := h.openStream(t, ctx, "/api/v1/events", tc.cookie, nil)
			await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })
			watchers = append(watchers, watcher{tc.role, tc.platform, frames})
		}

		folder := h.failedUpdate(as)

		for _, w := range watchers {
			frame := await(t, w.frames, "the "+w.role+"'s update frame", func(f sseFrame) bool {
				return f.event == string(events.KindUpdates) && strings.Contains(f.data, `"state":"failed"`)
			})
			var status updatesResponse
			decodeFrame(t, frame, &status)
			assertWithheldBelowPlatform(t, w.role, w.platform, status, frame.data, folder)
		}
	})
}

func assertWithheldBelowPlatform(t *testing.T, role string, platform bool, status updatesResponse, raw, folder string) {
	t.Helper()
	if status.Controller == nil || status.Controller.State != "failed" {
		t.Fatalf("the %s's status holds %+v, want the failed attempt", role, status.Controller)
	}
	if platform {
		if !strings.Contains(status.Controller.Error, folder) || !strings.Contains(raw, folder) {
			t.Errorf("the platform's error = %q, want the text as it was written, folder and all", status.Controller.Error)
		}
		return
	}
	if status.Controller.Error == "" {
		t.Errorf("the %s was given no reason at all", role)
	}
	if strings.Contains(raw, folder) || strings.Contains(raw, channel.RequestFile) {
		t.Errorf("the %s was sent a path: %s", role, raw)
	}
	// What the page shows about the helper is its own sentence, which names no
	// path for any role, and is kept.
	if status.Helper.State != controller.HelperReady || status.Helper.Reason == "" {
		t.Errorf("the %s's helper = %+v, want the status's own sentence", role, status.Helper)
	}
}

// A frame this version cannot read is not one to guess about. The platform is
// entitled to the whole of it and is sent it as it came; anyone else is sent
// nothing, because the document is exactly what the filter exists to withhold.
func TestAnUpdatesFrameTheFilterCannotReadIsOnlyPassedToThePlatform(t *testing.T) {
	garbled := []byte(`{"controller":{"error":"cannot write in /var/lib/zoomies-update"`)

	if out, ok := updatesFor(garbled, true); !ok || string(out) != string(garbled) {
		t.Errorf("updatesFor(platform) = %q, %v, want the frame untouched", out, ok)
	}
	if out, ok := updatesFor(garbled, false); ok {
		t.Errorf("updatesFor(below platform) = %q, true, want it refused", out)
	}

	whole := []byte(`{"mode":"manual","controller":{"id":"upd_1","state":"failed","error":"cannot write in /var/lib/zoomies-update"}}`)
	out, ok := updatesFor(whole, false)
	if !ok || strings.Contains(string(out), "/var/lib") {
		t.Errorf("updatesFor(below platform) = %q, %v, want the frame with the path withheld", out, ok)
	}
}

// A release check that GitHub or the network would not finish is not the
// server's fault and not the person's. The controller wrote a sentence for it that
// says what failed and what to try, and an opaque 500 hid that from the one
// person who can act on it.
func TestAFailedReleaseCheckIsABadGatewayThatCarriesTheControllersSentence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *releaseFeed)
		want  []string
	}{
		{"GitHub is unavailable", func(f *releaseFeed) { f.answer(http.StatusServiceUnavailable) },
			[]string{"GitHub answered 503", "try again later"}},
		{"GitHub refuses the controller", func(f *releaseFeed) { f.answer(http.StatusForbidden) },
			[]string{"GitHub answered 403", "nothing has changed"}},
		{"the network is down", func(f *releaseFeed) { f.unreachable(errors.New("dial tcp: no route to host")) },
			[]string{"could not reach GitHub", "no route to host", "outbound HTTPS requests to api.github.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			as := h.updateCallers()
			h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
			tc.setup(h.feed)

			resp := h.do(request{method: http.MethodPost, path: updatesCheckPath, cookie: as.admin})
			resp.mustStatus(t, http.StatusBadGateway, tc.name)
			if got := resp.errorCode(t); got != "update.check_failed" {
				t.Errorf("code = %q, want update.check_failed", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(resp.errorMessage(t), want) {
					t.Errorf("message = %q, want it to say %q", resp.errorMessage(t), want)
				}
			}
			if strings.Contains(resp.errorMessage(t), "request ID") {
				t.Errorf("message = %q, want the controller's sentence and not the server-failure one", resp.errorMessage(t))
			}
			logged := h.logs.text()
			if strings.Contains(logged, "ERROR") {
				t.Errorf("an upstream failure was logged as an error:\n%s", logged)
			}
			if !strings.Contains(logged, "WARN the release check could not be completed") {
				t.Errorf("an upstream failure left no warning in the log:\n%s", logged)
			}
		})
	}
}

// A body that is not an object has no field to name, so the refusal says what to
// send. The decoder's own complaint would name a type of the server's.
func TestABodyThatIsNotAnObjectIsToldWhatToSend(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdate(as)

	for _, body := range []string{`[]`, `"v1.3.5"`, `42`, `true`} {
		t.Run(body, func(t *testing.T) {
			resp := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform,
				rawBody: body, headers: map[string]string{"Content-Type": "application/json"}})
			resp.mustStatus(t, http.StatusUnprocessableEntity, body)
			msg := resp.errorMessage(t)
			if !strings.Contains(msg, `send a JSON object such as {"tag": "v1.3.5"}, or no body`) {
				t.Errorf("message = %q, want what to send", msg)
			}
			for _, leak := range []string{"unmarshal", "Go value", "json:", "main."} {
				if strings.Contains(msg, leak) {
					t.Errorf("message = %q, want no Go type or package names (%q)", msg, leak)
				}
			}
		})
	}
	if open := h.openUpdateAttempts(); len(open) != 0 {
		t.Errorf("%d attempts open after refused bodies, want none", len(open))
	}
}

// The body limit is the server's, and the refusal says so as it does everywhere
// else: 413, because there is nothing wrong with the JSON, only too much of it.
func TestAnOversizedBodyOnTheControllerRouteIsTooLarge(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdate(as)

	resp := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform,
		rawBody: `{"tag":"` + strings.Repeat("a", maxBodyBytes) + `"}`, headers: map[string]string{"Content-Type": "application/json"}})
	resp.mustStatus(t, http.StatusRequestEntityTooLarge, "an oversized body")
	if got := resp.errorCode(t); got != codeTooLarge {
		t.Errorf("code = %q, want %q", got, codeTooLarge)
	}
	if open := h.openUpdateAttempts(); len(open) != 0 {
		t.Errorf("%d attempts open after a refused body, want none", len(open))
	}
}

// A tag of null says what leaving it out says: the newest release.
func TestANullTagIsTheSameAsNoTag(t *testing.T) {
	for _, body := range []string{`{"tag": null}`, `null`} {
		t.Run(body, func(t *testing.T) {
			h := newHarness(t)
			as := h.updateCallers()
			h.readyToUpdate(as)
			resp := h.do(request{method: http.MethodPost, path: updatesControllerPath, cookie: as.platform,
				rawBody: body, headers: map[string]string{"Content-Type": "application/json"}})
			resp.mustStatus(t, http.StatusAccepted, body)
			if open := h.openUpdateAttempts(); len(open) != 1 || open[0].ToVersion != "v1.3.5" {
				t.Errorf("open attempts = %+v, want one for the newest release, v1.3.5", open)
			}
		})
	}
}

// The codes are written in three places a compiler does not connect: the
// constants here, the enum a generated client switches on, and the conventions
// list. A code in the constants and not the enum is one the client's types say
// cannot happen.
func TestTheErrorCodesInGoAndInTheDocumentAreTheSameSet(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatalf("reading errors.go: %v", err)
	}
	inGo := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if strings.HasPrefix(name.Name, "code") && ok && lit.Kind == token.STRING {
					inGo[strings.Trim(lit.Value, `"`)] = true
				}
			}
		}
	}
	if !inGo["update.mode_off"] || !inGo["internal"] {
		t.Fatalf("found %v in errors.go, so this read the constants wrongly", inGo)
	}

	doc := loadSpec(t)
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	envelope, _ := schemas["ErrorEnvelope"].(map[string]any)
	props, _ := envelope["properties"].(map[string]any)
	errObj, _ := props["error"].(map[string]any)
	errProps, _ := errObj["properties"].(map[string]any)
	code, _ := errProps["code"].(map[string]any)
	enum, _ := code["enum"].([]any)
	inDoc := map[string]bool{}
	for _, v := range enum {
		if s, ok := v.(string); ok {
			inDoc[s] = true
		}
	}
	for c := range inGo {
		if !inDoc[c] {
			t.Errorf("errors.go has the code %q and the ErrorCode enum in api/openapi.yaml does not", c)
		}
	}
	for c := range inDoc {
		if !inGo[c] {
			t.Errorf("the ErrorCode enum in api/openapi.yaml has %q and errors.go has no constant for it", c)
		}
	}

	conventions, err := os.ReadFile(filepath.Join("..", "..", "docs", "api-surface.md"))
	if err != nil {
		t.Fatal(err)
	}
	for c := range inGo {
		if strings.HasPrefix(c, "update.") {
			continue // listed together under Updates, where the docs test checks them.
		}
		if !strings.Contains(string(conventions), "`"+c+"`") {
			t.Errorf("docs/api-surface.md does not list the error code %q", c)
		}
	}
	if !strings.Contains(string(conventions), "`update.check_failed`") {
		t.Error("docs/api-surface.md does not list update.check_failed")
	}
}

// What a check learns is every open page's to know, and the page that pressed the
// button is only one of them.
func TestACheckSendsTheNewStatusToEveryOpenPage(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	useVersion(t, "1.3.4")
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	h.offerRelease("v1.3.5")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events", as.viewer, nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	h.do(request{method: http.MethodPost, path: updatesCheckPath, cookie: as.admin}).
		mustStatus(t, http.StatusOK, "checking for a release")

	frame := await(t, frames, "the status the check learnt", ofKind(events.KindUpdates))
	var status updatesResponse
	decodeFrame(t, frame, &status)
	if status.Latest == nil || status.Latest.Tag != "v1.3.5" || status.CheckedAt == nil {
		t.Errorf("the frame holds %+v, want the release the check has just read", status)
	}
}

// A host's failed update can carry the helper's own sentence, which can name a
// path on that host. The host's card is read by every role; only the platform
// is given the text, from the list and from the host alike.
func TestAHostsUpdateReasonIsWithheldBelowPlatform(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	useVersion(t, "1.3.5")
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	host := h.host("vm-update")
	host.Version, host.Features = "1.3.4", []string{"self-update"}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ctrl.RequestHostUpdate(h.ctx, controller.UpdateActor{ID: "usr_x", Name: "x"}, host.ID); err != nil {
		t.Fatalf("RequestHostUpdate: %v", err)
	}
	latest, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeHost, host.ID, 1)
	if err != nil || len(latest) != 1 {
		t.Fatalf("no attempt (%v)", err)
	}
	const path = "/usr/local/bin/zoomies"
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion, Version: "1.3.4", Features: host.Features,
		Update: &agent.UpdateReport{ID: latest[0].ID, Error: "could not replace " + path, FinishedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	for _, tc := range []struct {
		role     string
		cookie   string
		platform bool
	}{
		{"viewer", as.viewer, false},
		{"admin", as.admin, false},
		{"platform", as.platform, true},
	} {
		for _, p := range []string{"/api/v1/hosts", "/api/v1/hosts/" + host.ID} {
			resp := h.do(request{method: http.MethodGet, path: p, cookie: tc.cookie})
			resp.mustStatus(t, http.StatusOK, "reading the host")
			if got := strings.Contains(string(resp.body), path); got != tc.platform {
				t.Errorf("%s reading %s: the helper's text is there = %v, want %v", tc.role, p, got, tc.platform)
			}
			if !strings.Contains(string(resp.body), `"state":"failed"`) {
				t.Errorf("%s reading %s was not told the update failed: %s", tc.role, p, resp.body)
			}
		}
	}
}
