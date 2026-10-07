package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The host's page offers a command to paste into a terminal, with a token made on
// the spot so that nothing has to be fetched or remembered first. The token is
// the whole of what makes that safe to offer on a click: a person who can only
// read, for a quarter of an hour, and able to read hosts and nothing else. This
// makes it as the page does, through the route the page uses, and holds it to
// each of those, so changing a scope's name or the route's reading of a duration
// breaks here and not in a terminal on somebody's host.
func TestTheShortLivedTokenTheHostPageMakesReadsHostsAndNothingElse(t *testing.T) {
	h := newHarness(t)
	host := h.host("cli-host")
	// Anybody signed in may make a token for themselves; a viewer is the least of
	// them, and the page must work for one.
	_, cookie := h.user("reader", store.RoleViewer)

	made := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", cookie: cookie,
		body: map[string]any{
			"name": "doctor cli-host", "role": "viewer",
			"scopes": []string{"hosts:read"}, "expires_in": "15m",
		}})
	made.mustStatus(t, http.StatusCreated, "a viewer makes the page's token")
	var minted createdTokenResponse
	made.into(t, &minted)

	if minted.ExpiresAt == nil {
		t.Fatal("the token never expires; the page promises it ends by itself")
	}
	if left := time.Until(*minted.ExpiresAt); left < 14*time.Minute || left > 15*time.Minute+5*time.Second {
		t.Errorf("the token has %s left, want a quarter of an hour", left)
	}

	h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, token: minted.Token}).
		mustStatus(t, http.StatusOK, "the token reads the host, which is what zoomies doctor --host asks for")

	for _, refused := range []struct {
		name, method, path string
		body               any
	}{
		{"another resource", http.MethodGet, "/api/v1/pools", nil},
		{"a change to the host", http.MethodPatch, "/api/v1/hosts/" + host.ID, map[string]any{"capacity": 1}},
		{"a token of its own", http.MethodPost, "/api/v1/tokens", map[string]any{"name": "more", "role": "viewer"}},
	} {
		h.do(request{method: refused.method, path: refused.path, token: minted.Token, body: refused.body}).
			mustStatus(t, http.StatusForbidden, "the token is not enough for "+refused.name)
	}
}
