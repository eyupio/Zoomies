package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The name a host joined under comes from the agent's configuration, or from a
// container ID, and the operator is the one who has to read it every day.
func TestAnOperatorCanRenameAHost(t *testing.T) {
	h := newHarness(t)
	host := h.host("7096d9a9b798")
	operator, _ := h.user("operator", store.RoleOperator)

	res := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID,
		cookie: h.session(operator), body: map[string]any{"name": "  build-box  "}})
	res.mustStatus(t, http.StatusOK, "rename the host")

	var view hostResponse
	res.into(t, &view)
	if view.Name != "build-box" {
		t.Fatalf("name = %q, want the trimmed one", view.Name)
	}
	stored, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if stored.Name != "build-box" {
		t.Fatalf("stored name = %q; the rename lived in the response alone", stored.Name)
	}

	// A request that names something else leaves the name where it was.
	h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID,
		cookie: h.session(operator), body: map[string]any{"capacity": 3}}).
		mustStatus(t, http.StatusOK, "change the capacity")
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.Name != "build-box" {
		t.Fatalf("changing the capacity cleared the name: %q", stored.Name)
	}
}

// Two hosts called the same thing could not be told apart in a pool's reasons,
// and an agent joining by name would find the wrong row.
func TestAHostCannotBeRenamedToAnotherHostsName(t *testing.T) {
	h := newHarness(t)
	h.host("taken")
	host := h.host("vm-2")
	operator, _ := h.user("operator", store.RoleOperator)

	for name, body := range map[string]string{
		"taken":      "taken",
		"empty":      "   ",
		"too long":   strings.Repeat("a", maxHostNameLength+1),
		"line break": "two\nlines",
	} {
		res := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID,
			cookie: h.session(operator), body: map[string]any{"name": body}})
		res.mustStatus(t, http.StatusUnprocessableEntity, "rename to "+name)
	}
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.Name != "vm-2" {
		t.Fatalf("name = %q after refused renames, want it unchanged", stored.Name)
	}

	// Saving the name it already has is not a clash with itself.
	h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID,
		cookie: h.session(operator), body: map[string]any{"name": "vm-2"}}).
		mustStatus(t, http.StatusOK, "keep the same name")
}
