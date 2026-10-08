package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
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
