package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// A token narrowed to one resource used to be one request away from an
// unscoped admin token with no owner and no expiry: the minting route only
// asked for tokens.write, capped nothing at the caller, and attributed the
// result to nobody, so revoking the leaked token and disabling its owner left
// the minted one answering. Every step of that path is closed here.
func TestATokenCannotMintPastItself(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	_, narrow, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{
		Name: "ci-only-tokens", Role: store.RoleAdmin, UserID: admin.ID, Scopes: []string{"tokens:*"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unscoped, which would carry the whole admin role.
	wide := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", token: narrow,
		body: map[string]any{"name": "escaped", "role": "admin"}})
	wide.mustStatus(t, http.StatusUnprocessableEntity, "mint an unscoped token from a scoped one")
	if msg := wide.errorMessage(t); !strings.Contains(msg, "tokens:*") {
		t.Errorf("the refusal does not name the caller's limit: %q", msg)
	}

	// Scoped, but to a resource the caller cannot reach.
	beyond := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", token: narrow,
		body: map[string]any{"name": "escaped", "role": "admin", "scopes": []string{"pools:write"}}})
	beyond.mustStatus(t, http.StatusUnprocessableEntity, "mint a token reaching past the caller's scopes")
	if msg := beyond.errorMessage(t); !strings.Contains(msg, "pools:write") {
		t.Errorf("the refusal does not name the scope that reaches too far: %q", msg)
	}

	// Within its limit is fine, and the result belongs to the same account
	// as the token that made it.
	within := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", token: narrow,
		body: map[string]any{"name": "within", "role": "viewer", "scopes": []string{"tokens:read"}}})
	within.mustStatus(t, http.StatusCreated, "mint a token within the caller's scopes")
	var minted createdTokenResponse
	within.into(t, &minted)
	row, err := h.st.GetAPITokenByHash(h.ctx, cryptox.HashToken(minted.Token))
	if err != nil {
		t.Fatal(err)
	}
	if row.UserID != admin.ID {
		t.Fatalf("a token minted by %s's token is attributed to %q, want %s", admin.Username, row.UserID, admin.ID)
	}

	// So disabling the account ends the descendant too. Another administrator
	// exists first, because the last one cannot be disabled.
	h.user("other-admin", store.RoleAdmin)
	if err := h.ctrl.Auth().SetUserDisabled(h.ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	after := h.do(request{method: http.MethodGet, path: "/api/v1/tokens", token: minted.Token})
	after.mustStatus(t, http.StatusUnauthorized, "use a token whose owner's account was disabled")
}

// A role is capped the same way: an operator's session cannot hand out admin.
func TestAnOperatorCannotMintAnAdminToken(t *testing.T) {
	h := newHarness(t)
	_, cookie := h.user("ops", store.RoleOperator)
	// Anybody signed in may mint their own tokens, so the cap on the role is
	// the gate that matters here.
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", cookie: cookie,
		body: map[string]any{"name": "escaped", "role": "admin"}})
	if resp.status == http.StatusCreated {
		t.Fatal("an operator minted an admin token")
	}
}

// A token nobody owns has nobody to attribute a descendant to, and a token
// with no owner is one that survives every account-level revocation. Such a
// token keeps working for everything else it was made for; it just cannot
// make more.
func TestAnOwnerlessTokenCannotMintTokens(t *testing.T) {
	h := newHarness(t)
	orphan := h.token("ownerless", store.RoleAdmin)
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", token: orphan,
		body: map[string]any{"name": "descendant", "role": "viewer"}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "mint from a token with no owner")
	if msg := resp.errorMessage(t); !strings.Contains(msg, "no owner") {
		t.Errorf("the refusal does not say why: %q", msg)
	}
	// It still does its own job.
	h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: orphan}).mustStatus(t, http.StatusOK, "use the ownerless token")
}
