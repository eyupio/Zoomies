package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// Anybody signed in manages their own API tokens -- the CLI and a script of
// their own are ordinary work -- and never at a role above their own. They see
// and revoke only theirs; an administrator still sees and revokes everybody's.
func TestAnyoneSignedInManagesTheirOwnTokensAndOnlyThose(t *testing.T) {
	h := newHarness(t)
	admin, adminCookie := h.user("admin", store.RoleAdmin)
	_, viewerCookie := h.user("vera", store.RoleViewer)
	_, otherCookie := h.user("otto", store.RoleOperator)

	mint := func(cookie, name, role string) createdTokenResponse {
		t.Helper()
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", cookie: cookie,
			body: map[string]any{"name": name, "role": role, "expires_in": "720h"}})
		resp.mustStatus(t, http.StatusCreated, "minting "+name)
		var out createdTokenResponse
		resp.into(t, &out)
		return out
	}
	list := func(cookie string) []string {
		t.Helper()
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/tokens", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "listing tokens")
		var out struct {
			Items []tokenResponse `json:"items"`
		}
		resp.into(t, &out)
		var names []string
		for _, tk := range out.Items {
			names = append(names, tk.Name)
		}
		slices.Sort(names)
		return names
	}

	vera := mint(viewerCookie, "vera-laptop", "viewer")
	otto := mint(otherCookie, "otto-script", "operator")
	adminTok := mint(adminCookie, "admin-ci", "admin")
	if adminTok.UserID != admin.ID {
		t.Errorf("a token must say whose it is: user_id %q, want %s", adminTok.UserID, admin.ID)
	}

	// Never above the caller's own role.
	above := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", cookie: viewerCookie,
		body: map[string]any{"name": "escalate", "role": "operator"}})
	above.mustStatus(t, http.StatusUnprocessableEntity, "a viewer minting an operator token")

	if got := list(viewerCookie); !slices.Equal(got, []string{"vera-laptop"}) {
		t.Errorf("a viewer sees %v, want only their own", got)
	}
	if got := list(adminCookie); !slices.Equal(got, []string{"admin-ci", "otto-script", "vera-laptop"}) {
		t.Errorf("an administrator sees %v, want everybody's", got)
	}

	// Somebody else's token is not there, to a caller who may only manage
	// their own -- a 404 rather than a 403, which would confirm it exists.
	h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + otto.ID, cookie: viewerCookie}).
		mustStatus(t, http.StatusNotFound, "a viewer revoking somebody else's token")
	h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + vera.ID, cookie: viewerCookie}).
		mustStatus(t, http.StatusNoContent, "a viewer revoking their own")
	h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + otto.ID, cookie: adminCookie}).
		mustStatus(t, http.StatusNoContent, "an administrator revoking somebody else's")

	// Purging reaches only the caller's own, unless they may manage everybody's.
	h.do(request{method: http.MethodPost, path: "/api/v1/tokens/purge", cookie: viewerCookie, body: map[string]any{"all": true}}).
		mustStatus(t, http.StatusForbidden, "a viewer purging everybody's")
	own := h.do(request{method: http.MethodPost, path: "/api/v1/tokens/purge", cookie: viewerCookie, body: map[string]any{}})
	own.mustStatus(t, http.StatusOK, "a viewer purging their own")
	var purged purgeTokensResponse
	own.into(t, &purged)
	if len(purged.Deleted) != 1 || purged.Deleted[0].ID != vera.ID {
		t.Errorf("a viewer's purge deleted %+v, want only their own revoked token", purged.Deleted)
	}
	if got := list(adminCookie); !slices.Contains(got, "otto-script") {
		t.Errorf("a viewer's purge must leave other people's spent tokens alone, left %v", got)
	}

	// Every step is on the record.
	actions := h.auditActions()
	for _, want := range []string{"token.create", "token.revoke", "token.delete"} {
		if !slices.Contains(actions, want) {
			t.Errorf("no %s audit row among %v", want, actions)
		}
	}
}

// Signing out everywhere else ends the other sessions and every MCP
// connection, and leaves this browser signed in.
func TestSigningOutOtherSessionsEndsThemAndMCPConnections(t *testing.T) {
	h := oauthHarness(t)
	u, here := h.user("olive", store.RoleOperator)
	elsewhere := h.session(u)
	_, pair := h.connect(here, store.RoleViewer)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/logout-others", cookie: here})
	resp.mustStatus(t, http.StatusOK, "signing out other sessions")
	var out logoutOthersResponse
	resp.into(t, &out)
	if out.MCPConnectionsEnded != 1 {
		t.Errorf("mcp_connections_ended = %d, want 1", out.MCPConnectionsEnded)
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: here}).
		mustStatus(t, http.StatusOK, "this browser after signing out the others")
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: elsewhere}).
		mustStatus(t, http.StatusUnauthorized, "another browser after being signed out")
	if _, err := h.ctrl.Auth().AuthenticateMCP(h.ctx, auth.OAuthPolicy{Issuer: oauthBase, OpenRegistration: true}, pair.AccessToken, ""); err == nil {
		t.Error("the MCP access token must stop working")
	}
	for _, want := range []string{"auth.logout_others", "mcp_connection.revoke_all"} {
		if !slices.Contains(h.auditActions(), want) {
			t.Errorf("no %s audit row", want)
		}
	}

	// An API token has no sessions of its own to end.
	tok := h.token("script", store.RoleOperator)
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/logout-others", token: tok}).
		mustStatus(t, http.StatusForbidden, "an API token signing out other sessions")
}

// A password change ends the account's MCP connections as well as its other
// sessions, through the API as through the service.
func TestChangingThePasswordEndsMCPConnections(t *testing.T) {
	h := oauthHarness(t)
	_, cookie := h.user("olive", store.RoleOperator)
	_, pair := h.connect(cookie, store.RoleViewer)
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/password", cookie: cookie,
		body: map[string]any{"old_password": testPassword, "new_password": "an entirely different passphrase"}}).
		mustStatus(t, http.StatusNoContent, "changing the password")
	if _, err := h.ctrl.Auth().AuthenticateMCP(h.ctx, auth.OAuthPolicy{Issuer: oauthBase, OpenRegistration: true}, pair.AccessToken, ""); err == nil {
		t.Error("the MCP access token must stop working after a password change")
	}
}

// The sign-in button says what the operator chose, or else the issuer's host.
func TestTheSignInButtonSaysWhatTheOperatorChose(t *testing.T) {
	for _, tc := range []struct {
		label, want string
	}{
		{"", "Sign in with "},
		{"  Sign in with Okta  ", "Sign in with Okta"},
	} {
		issuer := fakeIssuer(t)
		h := newHarness(t, func(c *config.Config) {
			c.OIDC = config.OIDC{Enabled: true, Issuer: issuer.URL, ClientID: "zoomies", ClientSecret: "secret", Label: tc.label}
		})
		var meta metaResponse
		h.do(request{method: http.MethodGet, path: "/api/v1/meta"}).into(t, &meta)
		if tc.label == "" {
			if meta.OIDCLabel != oidcLabel("", issuer.URL) || meta.OIDCLabel == "" {
				t.Errorf("with no label the button says %q, want the issuer's host", meta.OIDCLabel)
			}
			continue
		}
		if meta.OIDCLabel != tc.want {
			t.Errorf("oidc_label = %q, want %q", meta.OIDCLabel, tc.want)
		}
	}
}

// With the password form hidden, the page offers single sign-on alone and a
// correct password below administrator is refused; an administrator still
// gets in. While single sign-on is not working the form is never hidden,
// because the page would then offer no way in at all.
func TestHidingThePasswordFormKeepsAnAdministratorsWayIn(t *testing.T) {
	issuer := fakeIssuer(t)
	h := newHarness(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Enabled: true, Issuer: issuer.URL, ClientID: "zoomies", ClientSecret: "secret", HidePasswordLogin: true}
	})
	h.user("vera", store.RoleViewer)
	h.user("admin", store.RoleAdmin)

	var meta metaResponse
	h.do(request{method: http.MethodGet, path: "/api/v1/meta"}).into(t, &meta)
	if !meta.PasswordLoginHidden {
		t.Fatal("password_login_hidden must be true while single sign-on works and the setting is on")
	}
	login := func(name string) *response {
		return h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: map[string]any{"username": name, "password": testPassword}})
	}
	login("vera").mustStatus(t, http.StatusForbidden, "a viewer's password with the form hidden")
	login("admin").mustStatus(t, http.StatusOK, "an administrator's password with the form hidden")

	// Single sign-on going away brings the form, and the password, back.
	h.api.useOIDC(nil)
	meta = metaResponse{}
	h.do(request{method: http.MethodGet, path: "/api/v1/meta"}).into(t, &meta)
	if meta.PasswordLoginHidden {
		t.Error("the form must not be hidden while single sign-on is not working")
	}
	login("vera").mustStatus(t, http.StatusOK, "a viewer's password while single sign-on is down")
}

// A provider that is down when the controller starts is tried again in the
// background, and the problems list says so until it answers.
func TestSingleSignOnComesUpWhenItsProviderDoes(t *testing.T) {
	var up atomic.Bool
	issuer := httptest.NewServer(nil)
	t.Cleanup(issuer.Close)
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() || r.URL.Path != "/.well-known/openid-configuration" {
			http.Error(w, "not yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize",
			"token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	h := newHarness(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Enabled: true, Issuer: issuer.URL, ClientID: "zoomies", ClientSecret: "secret"}
		c.Server.ExternalURL = "https://zoomies.example"
	})
	if h.api.oidcProvider().Enabled() || h.api.oidcFailure() == nil {
		t.Fatal("single sign-on must not be up while its provider is down")
	}
	codes := func() []string {
		ps, err := h.ctrl.Problems(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.Code)
		}
		return out
	}
	if !slices.Contains(codes(), "oidc.unavailable") {
		t.Fatalf("the problems list must say single sign-on is down, got %v", codes())
	}

	h.api.oidcRetry = 10 * time.Millisecond
	done := make(chan struct{})
	go func() { h.api.retryOIDC(t.Context()); close(done) }()
	time.Sleep(50 * time.Millisecond)
	up.Store(true)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("single sign-on did not come up after its provider did")
	}
	if !h.api.oidcProvider().Enabled() {
		t.Fatal("the retry loop stopped without a provider")
	}
	if slices.Contains(codes(), "oidc.unavailable") {
		t.Error("the problem must clear once single sign-on works")
	}
}
