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
	"reflect"
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

// failedHostUpdate is a host whose update the helper refused with a sentence that
// names a path on the host, and that path.
func (h *harness) failedHostUpdate() (*store.Host, string) {
	h.t.Helper()
	useVersion(h.t, "1.3.5")
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	host := h.host("vm-update")
	host.Version, host.Features = "1.3.4", []string{agent.FeatureSelfUpdate}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.ctrl.RequestHostUpdate(h.ctx, controller.UpdateActor{ID: "usr_x", Name: "x"}, host.ID); err != nil {
		h.t.Fatalf("RequestHostUpdate: %v", err)
	}
	latest, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeHost, host.ID, 1)
	if err != nil || len(latest) != 1 {
		h.t.Fatalf("no attempt (%v)", err)
	}
	const path = "/usr/local/bin/zoomies"
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion, Version: "1.3.4", Features: host.Features,
		Update: &agent.UpdateReport{ID: latest[0].ID, Error: "could not replace " + path, FinishedAt: time.Now().UTC()},
	}); err != nil {
		h.t.Fatalf("Heartbeat: %v", err)
	}
	return host, path
}

// assertHostReasonFor checks one host body: told the update failed, and given the
// helper's text only when the reader holds the platform role.
func assertHostReasonFor(t *testing.T, what, body, path string, platform bool) {
	t.Helper()
	if got := strings.Contains(body, path); got != platform {
		t.Errorf("%s: the helper's text is there = %v, want %v: %s", what, got, platform, body)
	}
	if !strings.Contains(body, `"state":"failed"`) {
		t.Errorf("%s was not told the update failed: %s", what, body)
	}
}

// A host's failed update can carry the helper's own sentence, which can name a
// path on that host. The host's card is read by every role; only the platform
// is given the text, from every route that answers with a host.
func TestAHostsUpdateReasonIsWithheldBelowPlatform(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	host, path := h.failedHostUpdate()

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
		for _, p := range []string{"/api/v1/hosts", "/api/v1/hosts/" + host.ID} {
			resp := h.do(request{method: http.MethodGet, path: p, cookie: tc.cookie})
			resp.mustStatus(t, http.StatusOK, "reading the host")
			assertHostReasonFor(t, tc.role+" reading "+p, string(resp.body), path, tc.platform)
		}
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/hosts/" + host.ID + "/cordon", cookie: tc.cookie,
			body: map[string]any{"cordoned": false}})
		if resp.status == http.StatusOK {
			assertHostReasonFor(t, tc.role+" cordoning", string(resp.body), path, tc.platform)
		}
	}
	for _, tc := range []struct {
		role     string
		cookie   string
		platform bool
	}{
		{"admin", as.admin, false},
		{"platform", as.platform, true},
	} {
		resp := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: tc.cookie,
			body: map[string]any{"capacity": 4}})
		resp.mustStatus(t, http.StatusOK, "editing the host")
		assertHostReasonFor(t, tc.role+" editing the host", string(resp.body), path, tc.platform)
		resp = h.do(request{method: http.MethodPost, path: "/api/v1/hosts/" + host.ID + "/throttle/clear", cookie: tc.cookie})
		resp.mustStatus(t, http.StatusOK, "clearing the throttle")
		assertHostReasonFor(t, tc.role+" clearing the throttle", string(resp.body), path, tc.platform)
	}
}

// The stream carries the host in the platform's form and narrows it per
// subscriber, live and on a replay, so that a platform account's card is what
// its GET says and nobody else's carries the helper's text.
func TestAHostFrameIsTheGETShapeForEachRole(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callers := []struct {
		role     string
		cookie   string
		platform bool
	}{
		{"viewer", as.viewer, false},
		{"operator", as.operator, false},
		{"admin", as.admin, false},
		{"platform", as.platform, true},
	}
	type watcher struct {
		role     string
		cookie   string
		platform bool
		frames   <-chan sseFrame
	}
	var watchers []watcher
	for _, tc := range callers {
		frames, _ := h.openStream(t, ctx, "/api/v1/events?kinds=host.updated", tc.cookie, nil)
		await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })
		watchers = append(watchers, watcher{tc.role, tc.cookie, tc.platform, frames})
	}
	bus := h.ctrl.Events()
	// A Last-Event-ID of zero asks for no replay, so the stream is given an event
	// to resume after.
	bus.Publish(events.KindPoolUpdated, "pool:marker", map[string]any{"id": "marker"})
	before := bus.LastID()
	host, path := h.failedHostUpdate()

	failed := func(f sseFrame) bool {
		return f.event == string(events.KindHostUpdated) && strings.Contains(f.data, `"state":"failed"`)
	}
	for _, w := range watchers {
		frame := await(t, w.frames, "the "+w.role+"'s host frame", failed)
		assertHostReasonFor(t, w.role+"'s live frame", frame.data, path, w.platform)
		if w.platform {
			resp := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: w.cookie})
			resp.mustStatus(t, http.StatusOK, "reading the host")
			var fromFrame, fromGET map[string]any
			decodeFrame(t, frame, &fromFrame)
			resp.into(t, &fromGET)
			if !reflect.DeepEqual(fromFrame["update"], fromGET["update"]) {
				t.Errorf("the platform's frame has update %v and its GET %v", fromFrame["update"], fromGET["update"])
			}
		}

		replayed, _ := h.openStream(t, ctx, "/api/v1/events?kinds=host.updated", w.cookie, map[string]string{
			"Last-Event-ID": bus.WireID(before),
		})
		frame = await(t, replayed, "the "+w.role+"'s replayed host frame", failed)
		assertHostReasonFor(t, w.role+"'s replayed frame", frame.data, path, w.platform)
	}
}

// A host frame that names no ended update is passed on as it came, and one the
// filter cannot read goes only to the platform.
func TestAHostFrameTheFilterCannotReadIsOnlyPassedToThePlatform(t *testing.T) {
	plain := []byte(`{"id":"hst_1","update":{"state":"requested","reason":"asked for"}}`)
	if out, ok := hostsFor(plain, false); !ok || string(out) != string(plain) {
		t.Errorf("hostsFor(a frame with nothing to withhold) = %q, %v, want it untouched", out, ok)
	}
	garbled := []byte(`{"id":"hst_1","update":{"state":"failed","reason":"cannot write /var/lib/zoomies-update"`)
	if out, ok := hostsFor(garbled, true); !ok || string(out) != string(garbled) {
		t.Errorf("hostsFor(platform) = %q, %v, want the frame untouched", out, ok)
	}
	if out, ok := hostsFor(garbled, false); ok {
		t.Errorf("hostsFor(below platform) = %q, true, want it refused", out)
	}
}

// On the stream, a host frame the filter cannot read is dropped for everybody
// below the platform rather than passed on unread: it could carry exactly what
// is withheld. The platform is sent it as it came.
func TestAnUnreadableHostFrameIsDroppedBelowPlatform(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	viewer, _ := h.openStream(t, ctx, "/api/v1/events?kinds=host.updated", as.viewer, nil)
	await(t, viewer, "the viewer's opening comment", func(f sseFrame) bool { return f.comment != "" })
	platform, _ := h.openStream(t, ctx, "/api/v1/events?kinds=host.updated", as.platform, nil)
	await(t, platform, "the platform's opening comment", func(f sseFrame) bool { return f.comment != "" })

	bus := h.ctrl.Events()
	unreadable := `["failed","/var/lib/zoomies-update"]`
	bus.Publish(events.KindHostUpdated, "host:hst_odd", json.RawMessage(unreadable))
	bus.Publish(events.KindHostUpdated, "host:hst_marker", json.RawMessage(`{"id":"hst_marker"}`))

	got := await(t, viewer, "the viewer's marker frame", func(f sseFrame) bool { return f.event == string(events.KindHostUpdated) })
	if strings.Contains(got.data, "zoomies-update") || !strings.Contains(got.data, "hst_marker") {
		t.Errorf("the viewer's first host frame is %s, want the unreadable one dropped and the marker next", got.data)
	}
	got = await(t, platform, "the platform's first host frame", func(f sseFrame) bool { return f.event == string(events.KindHostUpdated) })
	if got.data != unreadable {
		t.Errorf("the platform's first host frame is %s, want the unreadable one as it came", got.data)
	}
}

// hostUpdatePath is the route that updates one host.
func hostUpdatePath(hostID string) string { return "/api/v1/hosts/" + hostID + "/update" }

// updatableHost is a host that is behind a controller on a release and whose agent
// offers to update itself: everything a press of its button needs but the mode.
func (h *harness) updatableHost(name string) *store.Host {
	h.t.Helper()
	host := h.host(name)
	host.Version, host.Features = "1.3.4", []string{agent.FeatureSelfUpdate}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		h.t.Fatalf("SetHostReported: %v", err)
	}
	return host
}

// readyToUpdateAHost is a controller on 1.3.5 in manual mode. The host route reads
// no list of releases and needs no helper beside the controller: the release a host
// is taken to is the one the controller runs, and the helper that matters is the
// host's own.
func (h *harness) readyToUpdateAHost() {
	h.t.Helper()
	useVersion(h.t, "1.3.5")
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
}

// A host's update replaces software that runs as root on a machine the controller
// does not own, so it is an administrator's: an operator, who may cordon and drain
// the host, may not, and the refusal names the role they are missing.
func TestOnlyAnAdministratorMayUpdateAHost(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()

	row := routeRow(t, http.MethodPost, hostUpdatePath(""))
	if row.role != store.RoleAdmin || row.action != auth.ActionHostsUpdate {
		t.Errorf("the route table says %s needs %s and %s; want admin and %s", row.path, row.role, row.action, auth.ActionHostsUpdate)
	}

	for _, tc := range []struct {
		role   store.Role
		cookie string
		want   int
	}{
		{store.RoleViewer, as.viewer, http.StatusForbidden},
		{store.RoleOperator, as.operator, http.StatusForbidden},
		{store.RoleAdmin, as.admin, http.StatusAccepted},
		{store.RolePlatform, as.platform, http.StatusAccepted},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			host := h.updatableHost("vm-" + string(tc.role))
			got := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: tc.cookie})
			got.mustStatus(t, tc.want, "updating a host as "+string(tc.role))
			if tc.want == http.StatusForbidden && !strings.Contains(got.errorMessage(t), "admin") {
				t.Errorf("the refusal does not name the admin role: %q", got.errorMessage(t))
			}
		})
	}
	if open := h.openUpdateAttempts(); len(open) != 2 {
		t.Errorf("%d attempts open after four callers pressed the button, want the administrator's and the platform's", len(open))
	}
}

// A token is held to the scope as well as the role, and the scope is the host
// update's own: one minted to cordon hosts cannot replace an agent's binary.
func TestAScopedTokenNeedsTheHostsUpdateScope(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdateAHost()
	host := h.updatableHost("vm-scoped")

	refused := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID),
		token: h.token("cordon only", store.RoleAdmin, "hosts:cordon")})
	refused.mustStatus(t, http.StatusForbidden, "a token scoped to cordoning")
	if !strings.Contains(refused.errorMessage(t), "hosts:update") {
		t.Errorf("the refusal does not name the hosts:update scope: %q", refused.errorMessage(t))
	}
	h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID),
		token: h.token("update only", store.RoleAdmin, "hosts:update")}).
		mustStatus(t, http.StatusAccepted, "a token scoped to hosts:update")
}

func TestAnUnknownHostIs404(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()

	resp := h.do(request{method: http.MethodPost, path: hostUpdatePath("hst_nobody"), cookie: as.admin})
	resp.mustStatus(t, http.StatusNotFound, "updating a host nobody enrolled")
	if got := resp.errorCode(t); got != "not_found" {
		t.Errorf("code = %q, want not_found", got)
	}
	if open := h.openUpdateAttempts(); len(open) != 0 {
		t.Errorf("%d attempts open after a press on a host that is not there, want none", len(open))
	}
	if rows := h.updateAudit("update.host_requested"); len(rows) != 0 {
		t.Errorf("%d audit rows for a press that did nothing, want none", len(rows))
	}
}

// The agent inside the controller is updated with the controller, and the refusal
// says so in the sentence its card shows, under a code a client can switch on.
func TestAnEmbeddedHostIs409WithItsCode(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	host := &store.Host{
		Name: "controller", Capacity: 4, Embedded: true, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		Version: "1.3.4", Features: store.StringSlice{agent.FeatureSelfUpdate}, OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatalf("SetHostReported: %v", err)
	}

	resp := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: as.admin})
	resp.mustStatus(t, http.StatusConflict, "updating the controller's own agent")
	if got := resp.errorCode(t); got != "update.host_cannot_update" {
		t.Errorf("code = %q, want update.host_cannot_update (message %q)", got, resp.errorMessage(t))
	}
	if !strings.Contains(resp.errorMessage(t), "updated with the controller") {
		t.Errorf("message = %q, want the card's own sentence", resp.errorMessage(t))
	}
	if open := h.openUpdateAttempts(); len(open) != 0 {
		t.Errorf("%d attempts open after a refused press, want none", len(open))
	}
}

// Each refusal the controller makes keeps its code through the host route, with
// nothing opened and nothing in the audit trail, because nothing was done.
func TestTheHostRouteRefusesWithTheCodesOfTheStateThatCausesThem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness, host *store.Host)
		code  string
	}{
		{"updates are off", func(h *harness, _ *store.Host) {
			h.ctrl.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "off" })
		}, "update.mode_off"},
		{"the build is not a release", func(h *harness, _ *store.Host) { useVersion(h.t, "main-sha-abc1234") }, "update.not_a_release"},
		{"the host is already on the release", func(h *harness, host *store.Host) {
			host.Version = "1.3.5"
			if err := h.st.SetHostReported(h.ctx, host); err != nil {
				h.t.Fatal(err)
			}
		}, "update.host_cannot_update"},
		{"the host's agent does not offer to update itself", func(h *harness, host *store.Host) {
			host.Features = nil
			if err := h.st.SetHostReported(h.ctx, host); err != nil {
				h.t.Fatal(err)
			}
		}, "update.host_cannot_update"},
		{"an update is already open for the host", func(h *harness, host *store.Host) {
			if _, err := h.ctrl.RequestHostUpdate(h.ctx, controller.UpdateActor{ID: "usr_x", Name: "x"}, host.ID); err != nil {
				h.t.Fatal(err)
			}
		}, "update.in_progress"},
		{"the controller may not act", func(h *harness, _ *store.Host) {
			if err := h.st.SetRecoveryFence(h.ctx, true, "restored from a copy"); err != nil {
				h.t.Fatalf("fencing: %v", err)
			}
			if err := h.ctrl.LoadFence(h.ctx); err != nil {
				h.t.Fatalf("loading the fence: %v", err)
			}
		}, "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			as := h.updateCallers()
			h.readyToUpdateAHost()
			host := h.updatableHost("vm-refused")
			tc.setup(h, host)
			openBefore := len(h.openUpdateAttempts())

			resp := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: as.admin})
			resp.mustStatus(t, http.StatusConflict, tc.name)
			if got := resp.errorCode(t); got != tc.code {
				t.Errorf("code = %q, want %q (message %q)", got, tc.code, resp.errorMessage(t))
			}
			if open := len(h.openUpdateAttempts()); open != openBefore {
				t.Errorf("%d attempts open after a refusal, was %d", open, openBefore)
			}
			if rows := h.updateAudit("update.host_requested"); len(rows) != 0 {
				t.Errorf("a refused press wrote %d audit rows, want none: nothing was done", len(rows))
			}
		})
	}
}

// The page repaints from the answer, so the answer is the host as a GET gives it,
// with the update block holding the attempt just opened.
func TestTheBodyIsTheHostViewWithItsUpdateBlock(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	host := h.updatableHost("vm-view")
	doc := loadSpec(t)

	resp := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: as.admin})
	resp.mustStatus(t, http.StatusAccepted, "updating a host")
	assertShape(t, doc, "Host", resp.body)
	var view struct {
		ID     string `json:"id"`
		Update struct {
			State     string `json:"state"`
			CanUpdate bool   `json:"can_update"`
			AttemptID string `json:"attempt_id"`
			Reason    string `json:"reason"`
		} `json:"update"`
	}
	resp.into(t, &view)
	open := h.openUpdateAttempts()
	if len(open) != 1 {
		t.Fatalf("open attempts = %+v, want the one the answer names", open)
	}
	if view.ID != host.ID || view.Update.State != "requested" || view.Update.CanUpdate || view.Update.AttemptID != open[0].ID {
		t.Errorf("the answer's host = %+v, want %s, requested, not updatable again, naming attempt %s", view, host.ID, open[0].ID)
	}
	if !strings.Contains(view.Update.Reason, "v1.3.5") {
		t.Errorf("reason = %q, want the card's sentence about the release asked for", view.Update.Reason)
	}

	var got struct {
		Update struct {
			AttemptID string `json:"attempt_id"`
		} `json:"update"`
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: as.admin}).into(t, &got)
	if got.Update.AttemptID != view.Update.AttemptID {
		t.Errorf("GET names attempt %q, the answer %q: the page would repaint to something else", got.Update.AttemptID, view.Update.AttemptID)
	}
}

// The audit trail names the person who pressed the button, and the attempt records
// the same name. Neither holds a path: the row says which host and what it went
// from and to, and the attempt it opened is the target.
func TestTheHostRouteRecordsAnAuditRowForTheCaller(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdateAHost()
	host := h.updatableHost("vm-audited")
	lead, _ := h.user("fleet-lead", store.RoleAdmin)

	resp := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: h.session(lead)})
	resp.mustStatus(t, http.StatusAccepted, "updating a host")

	attempts, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeHost, host.ID, 5)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v, %v", attempts, err)
	}
	if attempts[0].RequestedBy != "fleet-lead" {
		t.Errorf("the attempt says %q asked, want fleet-lead", attempts[0].RequestedBy)
	}
	rows := h.updateAudit("update.host_requested")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	row := rows[0]
	if row.ActorID != lead.ID || row.ActorName != "fleet-lead" || row.TargetKind != "update" || row.TargetID != attempts[0].ID {
		t.Errorf("audit row = %+v, want fleet-lead's, about the attempt %s", row, attempts[0].ID)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(row.After), &detail); err != nil {
		t.Fatalf("audit detail %q is not JSON: %v", row.After, err)
	}
	want := map[string]any{"host_id": host.ID, "from": "1.3.4", "to": "v1.3.5"}
	if !reflect.DeepEqual(detail, want) {
		t.Errorf("audit detail = %v, want only %v", detail, want)
	}
}

// The route takes no body, and is told so when it is sent one. A body that edited
// the host the way PATCH does would otherwise be a 202 that dropped what its sender
// asked for.
func TestTheHostRouteTakesNoBody(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	host := h.updatableHost("vm-bodies")

	for _, tc := range []struct {
		name  string
		body  any
		field string
	}{
		{"a PATCH body", map[string]any{"capacity": 8}, "capacity"},
		{"a tag, which only the controller route takes", map[string]any{"tag": "v1.3.5"}, "tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: as.admin, body: tc.body})
			resp.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			var env errorEnvelope
			resp.into(t, &env)
			if env.Error.Code != "unprocessable" || env.Error.Field != tc.field {
				t.Errorf("error = %+v, want unprocessable naming %q", env.Error, tc.field)
			}
		})
	}
	t.Run("a body that is not an object", func(t *testing.T) {
		resp := h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: as.admin, body: []string{"v1.3.5"}})
		resp.mustStatus(t, http.StatusUnprocessableEntity, "an array")
	})
	if open := h.openUpdateAttempts(); len(open) != 0 {
		t.Errorf("%d attempts open after refused bodies, want none", len(open))
	}
	if rows := h.updateAudit("update.host_requested"); len(rows) != 0 {
		t.Errorf("%d audit rows after refused bodies, want none", len(rows))
	}

	// And nothing, an empty object and null are all the same request.
	for i, body := range []any{nil, map[string]any{}} {
		other := h.updatableHost(fmt.Sprintf("vm-empty-%d", i))
		h.do(request{method: http.MethodPost, path: hostUpdatePath(other.ID), cookie: as.admin, body: body}).
			mustStatus(t, http.StatusAccepted, "a request with nothing to say")
	}
}

// Asking for a host's update is seen by every open page as the host's own frame,
// in the GET shape, and not as a store row.
func TestAskingForAHostsUpdateSendsTheHostToEveryOpenPage(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	host := h.updatableHost("vm-frame")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events?kinds=host.updated", as.viewer, nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	h.do(request{method: http.MethodPost, path: hostUpdatePath(host.ID), cookie: as.admin}).
		mustStatus(t, http.StatusAccepted, "updating a host")

	frame := await(t, frames, "the host once asked", func(f sseFrame) bool {
		return f.event == string(events.KindHostUpdated) && strings.Contains(f.data, `"state":"requested"`)
	})
	if !strings.Contains(frame.data, host.ID) || !strings.Contains(frame.data, `"healthy"`) {
		t.Errorf("the frame is %s, want the host as a GET renders it", frame.data)
	}
}

const (
	updatesHostsPath   = "/api/v1/updates/hosts"
	rolloutResumePath  = "/api/v1/updates/rollout/resume"
	updatesRolloutPath = "/api/v1/updates/rollout"
)

// openRollout is the rollout the store holds open, or nil.
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

// haltedRollout is a rollout to v1.3.5 the planner halted on a host's failure,
// waiting for a person to look.
func (h *harness) haltedRollout() *store.UpdateRollout {
	h.t.Helper()
	r := &store.UpdateRollout{Target: "v1.3.5", Trigger: store.UpdateTriggerManual, StartedBy: "someone"}
	if err := h.st.CreateUpdateRollout(h.ctx, r); err != nil {
		h.t.Fatalf("CreateUpdateRollout: %v", err)
	}
	if moved, err := h.st.HaltUpdateRollout(h.ctx, r.ID, "vm-1 could not update to v1.3.5."); err != nil || !moved {
		h.t.Fatalf("HaltUpdateRollout: %v, %v", moved, err)
	}
	return r
}

// A rollout replaces the agent of every host it reaches, one after another, so it
// is the administrator's, as one host's update is: an operator may not start it,
// let a halted one go on, or stop it, and the refusal names the role they lack.
func TestOnlyAnAdministratorMayStartResumeOrCancelARollout(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	h.updatableHost("vm-rollout")

	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, updatesHostsPath},
		{http.MethodPost, rolloutResumePath},
		{http.MethodDelete, updatesRolloutPath},
	} {
		row := routeRow(t, rt.method, rt.path)
		if row.role != store.RoleAdmin || row.action != auth.ActionUpdatesRollout {
			t.Errorf("the route table says %s %s needs %s and %s; want admin and %s", rt.method, rt.path, row.role, row.action, auth.ActionUpdatesRollout)
		}
	}

	for _, who := range []struct {
		role   store.Role
		cookie string
	}{{store.RoleViewer, as.viewer}, {store.RoleOperator, as.operator}} {
		for _, rt := range []struct{ method, path string }{
			{http.MethodPost, updatesHostsPath},
			{http.MethodPost, rolloutResumePath},
			{http.MethodDelete, updatesRolloutPath},
		} {
			got := h.do(request{method: rt.method, path: rt.path, cookie: who.cookie})
			got.mustStatus(t, http.StatusForbidden, string(who.role)+" calling "+rt.method+" "+rt.path)
			if !strings.Contains(got.errorMessage(t), "admin") {
				t.Errorf("the refusal does not name the admin role: %q", got.errorMessage(t))
			}
		}
	}
	if r := h.openRollout(); r != nil {
		t.Fatalf("a rollout is open after only refused callers: %+v", r)
	}

	h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin}).
		mustStatus(t, http.StatusAccepted, "starting a rollout as an administrator")
	h.do(request{method: http.MethodPost, path: rolloutResumePath, cookie: as.admin}).
		mustStatus(t, http.StatusAccepted, "resuming a rollout as an administrator")
	h.do(request{method: http.MethodDelete, path: updatesRolloutPath, cookie: as.admin}).
		mustStatus(t, http.StatusOK, "cancelling a rollout as an administrator")
	if r := h.openRollout(); r != nil {
		t.Errorf("a rollout is still open after it was cancelled: %+v", r)
	}
}

// A token is held to the rollout's own scope: one minted to update a single host
// cannot walk the fleet.
func TestAScopedTokenNeedsTheUpdatesRolloutScope(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdateAHost()
	h.updatableHost("vm-scoped")

	refused := h.do(request{method: http.MethodPost, path: updatesHostsPath,
		token: h.token("one host", store.RoleAdmin, "hosts:update")})
	refused.mustStatus(t, http.StatusForbidden, "a token scoped to one host's update")
	if !strings.Contains(refused.errorMessage(t), "updates:rollout") {
		t.Errorf("the refusal does not name the updates:rollout scope: %q", refused.errorMessage(t))
	}
	h.do(request{method: http.MethodPost, path: updatesHostsPath,
		token: h.token("rollouts", store.RoleAdmin, "updates:rollout")}).
		mustStatus(t, http.StatusAccepted, "a token scoped to updates:rollout")
}

// The answer is the status a GET would give, with the rollout in it, so the page
// repaints from the answer and not from a guess.
func TestStartingARolloutAnswersWithTheStatusThatHoldsIt(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	h.updatableHost("vm-a")
	h.updatableHost("vm-b")

	resp := h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin})
	resp.mustStatus(t, http.StatusAccepted, "starting a rollout")
	var status updatesResponse
	resp.into(t, &status)
	open := h.openRollout()
	if open == nil {
		t.Fatal("no rollout is open after an accepted start")
	}
	if status.Rollout == nil || status.Rollout.ID != open.ID || status.Rollout.Target != "v1.3.5" || status.Rollout.State != store.RolloutRunning {
		t.Errorf("the answer's rollout is %+v, want the running rollout %s to v1.3.5", status.Rollout, open.ID)
	}
	if len(open.HostIDs) != 0 {
		t.Errorf("a start with no host_ids recorded %v, want none: every host behind", open.HostIDs)
	}
}

// A host named twice is one host, and the rollout takes only the hosts named.
func TestARolloutOfNamedHostsTakesEachOnce(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	a := h.updatableHost("vm-a")
	h.updatableHost("vm-b")

	h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin,
		body: map[string]any{"host_ids": []string{a.ID, a.ID}}}).
		mustStatus(t, http.StatusAccepted, "starting a rollout of one host named twice")
	open := h.openRollout()
	if open == nil || !reflect.DeepEqual([]string(open.HostIDs), []string{a.ID}) {
		t.Errorf("the rollout is %+v, want one of %s alone", open, a.ID)
	}
}

// An id that names no host is the person's mistake, not the server's: it is a
// 422 on host_ids whose message names the id, and nothing is started.
func TestAnUnknownHostInARolloutIsRefusedByName(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	a := h.updatableHost("vm-a")

	resp := h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin,
		body: map[string]any{"host_ids": []string{a.ID, "hst_nobody"}}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a rollout naming a host nobody enrolled")
	var env errorEnvelope
	resp.into(t, &env)
	if env.Error.Code != codeUnprocessable || env.Error.Field != "host_ids" {
		t.Errorf("error = %+v, want unprocessable on host_ids", env.Error)
	}
	if !strings.Contains(env.Error.Message, "hst_nobody") || strings.HasPrefix(env.Error.Message, "not found") {
		t.Errorf("message = %q, want a sentence naming hst_nobody", env.Error.Message)
	}
	if r := h.openRollout(); r != nil {
		t.Errorf("a rollout was started for a list with an unknown host: %+v", r)
	}
	if rows := h.updateAudit("update.rollout_started"); len(rows) != 0 {
		t.Errorf("%d audit rows for a start that did nothing, want none", len(rows))
	}
}

// What the body may hold is a list of host ids and nothing else. An empty list,
// or a null one, is refused rather than read as every host: a client whose
// selection came out empty must not walk the whole fleet.
func TestTheRolloutBodyIsChecked(t *testing.T) {
	for _, tc := range []struct {
		name, body, field, says string
	}{
		{"an empty id", `{"host_ids": ["hst_a", ""]}`, "host_ids", "empty id"},
		{"an empty list", `{"host_ids": []}`, "host_ids", "names no host"},
		{"a null list", `{"host_ids": null}`, "host_ids", "list of host ids"},
		{"a list of numbers", `{"host_ids": [1]}`, "host_ids", "list of host ids"},
		{"a string, not a list", `{"host_ids": "hst_a"}`, "host_ids", "list of host ids"},
		{"a field the route does not take", `{"tag": "v1.3.5"}`, "tag", "remove tag"},
		{"not an object", `["hst_a"]`, "", "send a JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			as := h.updateCallers()
			h.readyToUpdateAHost()
			h.updatableHost("vm-a")

			resp := h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin,
				rawBody: tc.body, headers: map[string]string{"Content-Type": "application/json"}})
			resp.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			var env errorEnvelope
			resp.into(t, &env)
			if env.Error.Field != tc.field {
				t.Errorf("field = %q, want %q: %+v", env.Error.Field, tc.field, env)
			}
			if !strings.Contains(string(resp.body), tc.says) {
				t.Errorf("the refusal does not say %q: %s", tc.says, resp.body)
			}
			if r := h.openRollout(); r != nil {
				t.Errorf("a rollout was started from a refused body: %+v", r)
			}
		})
	}
}

// Resuming takes no body, and is told so when it is sent one, as the host route
// is: a 202 would tell its sender that whatever it asked for was done.
func TestTheResumeRouteTakesNoBody(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	h.updatableHost("vm-a")
	halted := h.haltedRollout()

	resp := h.do(request{method: http.MethodPost, path: rolloutResumePath, cookie: as.admin, body: map[string]any{"host_ids": []string{"hst_a"}}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a resume with a body")
	if r := h.openRollout(); r == nil || r.ID != halted.ID || r.State != store.RolloutHalted {
		t.Errorf("the rollout is %+v after a refused resume, want %s still halted", r, halted.ID)
	}
}

// The list is bounded well below the server's general limit: a fleet's worth of
// ids fits many times over, and anything bigger is not a list a person chose.
func TestAnOversizedRolloutBodyIsTooLarge(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	h.updatableHost("vm-a")

	resp := h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin,
		rawBody: `{"host_ids":["` + strings.Repeat("a", maxRolloutBodyBytes) + `"]}`, headers: map[string]string{"Content-Type": "application/json"}})
	resp.mustStatus(t, http.StatusRequestEntityTooLarge, "an oversized body")
	if r := h.openRollout(); r != nil {
		t.Errorf("a rollout was started from a refused body: %+v", r)
	}
	if maxRolloutBodyBytes >= maxBodyBytes {
		t.Errorf("the rollout body limit is %d, not below the server's %d", maxRolloutBodyBytes, maxBodyBytes)
	}
}

// Each refusal of a start as a person meets it, from the state that causes it,
// with nothing started and nothing audited.
func TestStartingARolloutRefusesWithTheCodeOfTheStateThatCausesIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness)
		code  string
	}{
		{"a halted rollout waits for a person", func(h *harness) {
			h.readyToUpdateAHost()
			h.updatableHost("vm-a")
			h.haltedRollout()
		}, "update.rollout_halted"},
		{"no host is behind", func(h *harness) {
			h.readyToUpdateAHost()
			host := h.updatableHost("vm-current")
			host.Version = "1.3.5"
			if err := h.st.SetHostReported(h.ctx, host); err != nil {
				h.t.Fatalf("SetHostReported: %v", err)
			}
		}, "update.nothing_newer"},
		{"updating is off", func(h *harness) {
			useVersion(h.t, "1.3.5")
			h.updatableHost("vm-a")
		}, "update.mode_off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			as := h.updateCallers()
			tc.setup(h)
			before := h.openRollout()

			resp := h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: as.admin})
			resp.mustStatus(t, http.StatusConflict, tc.name)
			if got := resp.errorCode(t); got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
			if after := h.openRollout(); !reflect.DeepEqual(before, after) {
				t.Errorf("the open rollout went from %+v to %+v on a refusal", before, after)
			}
			if rows := h.updateAudit("update.rollout_started"); len(rows) != 0 {
				t.Errorf("%d audit rows for a refused start, want none", len(rows))
			}
		})
	}
}

// There is nothing to resume or cancel without an open rollout, and the answer
// says so as a 404 rather than pretending it did something.
func TestResumingOrCancellingWithNoRolloutIs404(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()

	for _, rt := range []struct{ method, path, verb string }{
		{http.MethodPost, rolloutResumePath, "resume"},
		{http.MethodDelete, updatesRolloutPath, "cancel"},
	} {
		resp := h.do(request{method: rt.method, path: rt.path, cookie: as.admin})
		resp.mustStatus(t, http.StatusNotFound, rt.verb+" with no rollout")
		if !strings.Contains(resp.errorMessage(t), "no rollout to "+rt.verb) {
			t.Errorf("message = %q, want it to say there is no rollout to %s", resp.errorMessage(t), rt.verb)
		}
	}
	if rows := h.updateAudit("update.rollout_resumed"); len(rows) != 0 {
		t.Errorf("%d resume audit rows for a press that did nothing", len(rows))
	}
	if rows := h.updateAudit("update.rollout_cancelled"); len(rows) != 0 {
		t.Errorf("%d cancel audit rows for a press that did nothing", len(rows))
	}
}

// Resuming lets a halted rollout go on, and cancelling ends it; the open rollout
// in the store is what each answer is judged on.
func TestResumeLetsAHaltedRolloutGoOnAndCancelEndsIt(t *testing.T) {
	h := newHarness(t)
	as := h.updateCallers()
	h.readyToUpdateAHost()
	h.updatableHost("vm-a")
	halted := h.haltedRollout()

	resp := h.do(request{method: http.MethodPost, path: rolloutResumePath, cookie: as.admin})
	resp.mustStatus(t, http.StatusAccepted, "resuming the halted rollout")
	var status updatesResponse
	resp.into(t, &status)
	if status.Rollout == nil || status.Rollout.ID != halted.ID || status.Rollout.State != store.RolloutRunning {
		t.Errorf("the answer's rollout is %+v, want %s running", status.Rollout, halted.ID)
	}

	resp = h.do(request{method: http.MethodDelete, path: updatesRolloutPath, cookie: as.admin})
	resp.mustStatus(t, http.StatusOK, "cancelling the rollout")
	resp.into(t, &status)
	if status.Rollout == nil || status.Rollout.ID != halted.ID || status.Rollout.State != store.RolloutCancelled {
		t.Errorf("the answer's rollout is %+v, want %s cancelled", status.Rollout, halted.ID)
	}
	if r := h.openRollout(); r != nil {
		t.Errorf("a rollout is open after the cancel: %+v", r)
	}
}

// The person who pressed each button is in the audit trail, under their own
// name: the controller writes no row for a person, so a row missing here is a
// press nobody can account for.
func TestEachRolloutRouteRecordsAnAuditRowForTheCaller(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdateAHost()
	a := h.updatableHost("vm-a")
	lead, _ := h.user("fleet-lead", store.RoleAdmin)
	cookie := h.session(lead)

	h.do(request{method: http.MethodPost, path: updatesHostsPath, cookie: cookie,
		body: map[string]any{"host_ids": []string{a.ID}}}).
		mustStatus(t, http.StatusAccepted, "starting a rollout")
	open := h.openRollout()
	if open == nil {
		t.Fatal("no rollout is open")
	}
	if open.StartedBy != "fleet-lead" {
		t.Errorf("the rollout says %q started it, want fleet-lead", open.StartedBy)
	}
	if _, err := h.st.HaltUpdateRollout(h.ctx, open.ID, "vm-a could not update."); err != nil {
		t.Fatal(err)
	}
	h.do(request{method: http.MethodPost, path: rolloutResumePath, cookie: cookie}).
		mustStatus(t, http.StatusAccepted, "resuming the rollout")
	h.do(request{method: http.MethodDelete, path: updatesRolloutPath, cookie: cookie}).
		mustStatus(t, http.StatusOK, "cancelling the rollout")

	for _, tc := range []struct {
		action string
		detail map[string]any
	}{
		{"update.rollout_started", map[string]any{"to": "v1.3.5", "host_ids": []any{a.ID}}},
		{"update.rollout_resumed", map[string]any{"to": "v1.3.5"}},
		{"update.rollout_cancelled", map[string]any{"to": "v1.3.5"}},
	} {
		rows := h.updateAudit(tc.action)
		if len(rows) != 1 {
			t.Errorf("%d %s rows, want 1", len(rows), tc.action)
			continue
		}
		row := rows[0]
		if row.ActorID != lead.ID || row.ActorName != "fleet-lead" || row.TargetKind != "update" || row.TargetID != open.ID {
			t.Errorf("%s row = %+v, want fleet-lead's, about the rollout %s", tc.action, row, open.ID)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(row.After), &detail); err != nil {
			t.Fatalf("audit detail %q is not JSON: %v", row.After, err)
		}
		if !reflect.DeepEqual(detail, tc.detail) {
			t.Errorf("%s detail = %v, want %v", tc.action, detail, tc.detail)
		}
	}
}
