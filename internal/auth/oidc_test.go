package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// discovery serves just enough of an OpenID provider for oidc.NewProvider to
// finish its discovery request, so the constructor can be tested without a real
// identity provider or any network access.
func discovery(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	return srv
}

func TestNewOIDCDisabledIsNotAnError(t *testing.T) {
	p, err := NewOIDC(t.Context(), config.Default().OIDC, "https://zoomies.example.com")
	if p != nil || err != nil {
		t.Fatalf("NewOIDC with SSO off = %v, %v; want nil, nil", p, err)
	}
	// A nil provider must stay usable, so callers need no branch.
	if p.Enabled() || p.AllowSignup() || p.RedirectURL() != "" {
		t.Error("a nil provider claims to be configured")
	}
	if p.RoleFor([]string{"admins"}) != store.RoleViewer {
		t.Error("a nil provider should map every group to viewer")
	}
	if _, _, err := p.Start(); err == nil {
		t.Error("Start on a nil provider should say SSO is not configured")
	}
}

func TestNewOIDCRequiresItsSettings(t *testing.T) {
	cfg := config.Default().OIDC
	cfg.Enabled = true
	if _, err := NewOIDC(t.Context(), cfg, "https://zoomies.example.com"); err == nil ||
		!strings.Contains(err.Error(), "oidc.issuer") {
		t.Errorf("missing issuer = %v; want an error naming oidc.issuer", err)
	}

	cfg.Issuer = "https://login.example.com"
	if _, err := NewOIDC(t.Context(), cfg, "https://zoomies.example.com"); err == nil ||
		!strings.Contains(err.Error(), "oidc.client_id") {
		t.Errorf("missing client credentials = %v; want an error naming oidc.client_id", err)
	}

	cfg.ClientID, cfg.ClientSecret = "id", "shh"
	if _, err := NewOIDC(t.Context(), cfg, ""); err == nil ||
		!strings.Contains(err.Error(), "server.external_url") {
		t.Errorf("no way to build a redirect URL = %v; want an error naming external_url", err)
	}
}

func TestNewOIDCDerivesTheRedirectURL(t *testing.T) {
	srv := discovery(t)
	cfg := config.Default().OIDC
	cfg.Enabled, cfg.Issuer, cfg.ClientID, cfg.ClientSecret = true, srv.URL, "zoomies", "shh"

	p, err := NewOIDC(t.Context(), cfg, "https://zoomies.example.com/")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	if want := "https://zoomies.example.com" + OIDCCallbackPath; p.RedirectURL() != want {
		t.Errorf("redirect URL = %q; want %q", p.RedirectURL(), want)
	}
	if !containsFold(p.oauth.Scopes, "openid") {
		t.Errorf("scopes = %v; want the openid scope to be present", p.oauth.Scopes)
	}

	url, state, err := p.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, want := range []string{srv.URL + "/authorize", "client_id=zoomies", "state=" + state, "nonce="} {
		if !strings.Contains(url, want) {
			t.Errorf("authorisation URL %q is missing %q", url, want)
		}
	}

	// The state is single-use: a replayed callback is refused before any code
	// is exchanged.
	if _, err := p.Complete(t.Context(), "not-a-state", "code"); err == nil {
		t.Error("an unknown state was accepted")
	}
	if _, ok := p.states.take(state); !ok {
		t.Fatal("the state Start returned was not remembered")
	}
	if _, err := p.Complete(t.Context(), state, "code"); err == nil {
		t.Error("a spent state was accepted a second time")
	}
}

func TestStateCacheIsSingleUseAndExpires(t *testing.T) {
	c := newClock()
	cache := newStateCache(10*time.Minute, c.Now)

	cache.put("state-1", "nonce-1")
	if nonce, ok := cache.take("state-1"); !ok || nonce != "nonce-1" {
		t.Fatalf("take = %q, %v; want nonce-1, true", nonce, ok)
	}
	if _, ok := cache.take("state-1"); ok {
		t.Error("a state was spendable twice")
	}
	if _, ok := cache.take(""); ok {
		t.Error("an empty state was accepted")
	}

	cache.put("state-2", "nonce-2")
	c.Advance(11 * time.Minute)
	if _, ok := cache.take("state-2"); ok {
		t.Error("an expired state was accepted")
	}
	// Putting a new entry sweeps the expired ones.
	cache.put("state-3", "nonce-3")
	if len(cache.entries) != 1 {
		t.Errorf("cache holds %d entries; want only the live one", len(cache.entries))
	}
}

func TestRoleForMapsGroups(t *testing.T) {
	p := &OIDCProvider{cfg: config.OIDC{
		AdminGroups:    []string{"Platform-Admins"},
		OperatorGroups: []string{"ci-operators"},
	}}
	cases := []struct {
		groups []string
		want   store.Role
	}{
		{nil, store.RoleViewer},
		{[]string{"everyone"}, store.RoleViewer},
		{[]string{"ci-operators"}, store.RoleOperator},
		{[]string{"platform-admins"}, store.RoleAdmin},                 // case-insensitive
		{[]string{"ci-operators", "Platform-Admins"}, store.RoleAdmin}, // admin wins
	}
	for _, tc := range cases {
		if got := p.RoleFor(tc.groups); got != tc.want {
			t.Errorf("RoleFor(%v) = %q; want %q", tc.groups, got, tc.want)
		}
	}
}

func TestClaimsFrom(t *testing.T) {
	p := &OIDCProvider{cfg: config.OIDC{
		UsernameClaim: "preferred_username",
		GroupsClaim:   "groups",
		AdminGroups:   []string{"admins"},
	}}

	c, err := p.claimsFrom("sub-1", map[string]any{
		"preferred_username": "alice",
		"email":              "alice@example.com",
		"name":               "Alice Example",
		"groups":             []any{"admins", "everyone"},
	})
	if err != nil {
		t.Fatalf("claimsFrom: %v", err)
	}
	if c.Username != "alice" || c.Email != "alice@example.com" || c.Name != "Alice Example" {
		t.Errorf("claims = %+v", c)
	}
	if c.Role != store.RoleAdmin {
		t.Errorf("role = %q; want admin", c.Role)
	}

	// Providers that send no preferred_username fall back to the email rather
	// than failing a login nobody can debug.
	c, err = p.claimsFrom("sub-2", map[string]any{"email": "bob@example.com", "groups": "admins"})
	if err != nil {
		t.Fatalf("claimsFrom: %v", err)
	}
	if c.Username != "bob@example.com" {
		t.Errorf("username = %q; want the email as a fallback", c.Username)
	}
	if c.Role != store.RoleAdmin {
		t.Errorf("a groups claim sent as a single string was ignored: %+v", c)
	}

	if _, err := p.claimsFrom("", map[string]any{"email": "x@example.com"}); err == nil {
		t.Error("claims with no subject were accepted; there would be nothing to link the account by")
	}
}

func TestEnsureUser(t *testing.T) {
	p := &OIDCProvider{cfg: config.OIDC{AdminGroups: []string{"admins"}}}
	ctx := t.Context()

	newStore := func(t *testing.T) *store.Store {
		t.Helper()
		c := newClock()
		st, err := store.Open(ctx, store.Options{Path: ":memory:", Now: c.Now})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}

	t.Run("links by subject", func(t *testing.T) {
		st := newStore(t)
		want := addUser(t, st, "alice", store.RoleViewer, func(u *store.User) { u.OIDCSubject = "sub-1" })

		got, err := p.EnsureUser(ctx, st, &Claims{
			Subject: "sub-1", Username: "renamed-at-the-idp", Role: store.RoleAdmin,
		}, false)
		if err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
		if got.ID != want.ID {
			t.Fatalf("user = %s; want the account linked by subject (%s)", got.ID, want.ID)
		}
		if got.Role != store.RoleAdmin {
			t.Errorf("role = %q; want the identity provider's groups to be authoritative", got.Role)
		}
	})

	t.Run("adopts an existing username only when the account was made for SSO", func(t *testing.T) {
		st := newStore(t)
		want := addUser(t, st, "alice", store.RoleOperator, func(u *store.User) { u.PasswordHash = "" })

		got, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-9", Username: "Alice", Role: store.RoleViewer}, false)
		if err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
		if got.ID != want.ID || got.OIDCSubject != "sub-9" {
			t.Fatalf("user = %+v; want the existing account linked to sub-9", got)
		}
		stored, err := st.GetUser(ctx, want.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.OIDCSubject != "sub-9" {
			t.Error("the link was not persisted")
		}
	})

	// With a provider whose users can influence their own username claim, a
	// sign-in as "admin" used to inherit the local admin's account and role.
	t.Run("refuses to take over a password account by name unless told to", func(t *testing.T) {
		st := newStore(t)
		local := addUser(t, st, "admin", store.RoleAdmin, nil)

		_, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-10", Username: "admin"}, true)
		if err == nil || !strings.Contains(err.Error(), "signs in with a password") || !strings.Contains(err.Error(), "link_by_username") {
			t.Fatalf("EnsureUser = %v; want a refusal that names the setting", err)
		}
		stored, _ := st.GetUser(ctx, local.ID)
		if stored.OIDCSubject != "" {
			t.Fatal("the password account was linked anyway")
		}

		linking := &OIDCProvider{cfg: config.OIDC{LinkByUsername: true}}
		got, err := linking.EnsureUser(ctx, st, &Claims{Subject: "sub-10", Username: "admin"}, false)
		if err != nil {
			t.Fatalf("EnsureUser with link_by_username: %v", err)
		}
		if got.ID != local.ID || got.OIDCSubject != "sub-10" || got.PasswordHash == "" {
			t.Fatalf("user = %+v; want the local account linked, password kept", got)
		}
	})

	t.Run("refuses a second identity for one username", func(t *testing.T) {
		st := newStore(t)
		addUser(t, st, "alice", store.RoleViewer, func(u *store.User) { u.OIDCSubject = "sub-1" })

		_, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-2", Username: "alice"}, true)
		if err == nil || !strings.Contains(err.Error(), "already linked") {
			t.Fatalf("EnsureUser = %v; want a refusal naming the existing link", err)
		}
	})

	t.Run("refuses to create when signup is off", func(t *testing.T) {
		st := newStore(t)
		_, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-3", Username: "carol"}, false)
		if err == nil {
			t.Fatal("an unknown user was provisioned with signup off")
		}
		if !strings.Contains(err.Error(), "create it first") || !strings.Contains(err.Error(), "allow_signup") {
			t.Errorf("error = %q; want it to tell the operator what to do", err)
		}
	})

	t.Run("creates when signup is on", func(t *testing.T) {
		st := newStore(t)
		u, err := p.EnsureUser(ctx, st, &Claims{
			Subject: "sub-4", Username: "dave", Email: "dave@example.com", Role: store.RoleOperator,
		}, true)
		if err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
		if u.PasswordHash != "" {
			t.Error("a provisioned account was given a password hash")
		}
		if u.Role != store.RoleOperator || u.Email != "dave@example.com" {
			t.Errorf("provisioned user = %+v", u)
		}
		// A second login finds the same account rather than making another.
		again, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-4", Username: "dave"}, true)
		if err != nil || again.ID != u.ID {
			t.Fatalf("second sign-in = %v, %v; want the same account", again, err)
		}
	})

	t.Run("refuses a disabled account", func(t *testing.T) {
		st := newStore(t)
		addUser(t, st, "eve", store.RoleViewer, func(u *store.User) {
			u.OIDCSubject, u.Disabled = "sub-5", true
		})
		if _, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-5", Username: "eve"}, true); !errors.Is(err, ErrAccountDisabled) {
			t.Fatalf("EnsureUser on a disabled account = %v; want ErrAccountDisabled", err)
		}
	})

	t.Run("leaves the role alone when no groups are mapped", func(t *testing.T) {
		st := newStore(t)
		unmapped := &OIDCProvider{cfg: config.OIDC{}}
		addUser(t, st, "frank", store.RoleAdmin, func(u *store.User) { u.OIDCSubject = "sub-6" })

		got, err := unmapped.EnsureUser(ctx, st, &Claims{
			Subject: "sub-6", Username: "frank", Role: store.RoleViewer,
		}, false)
		if err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
		if got.Role != store.RoleAdmin {
			t.Errorf("role = %q; an instance with no group mapping must not demote its admins on login", got.Role)
		}
	})

	t.Run("rejects an unusable username", func(t *testing.T) {
		st := newStore(t)
		if _, err := p.EnsureUser(ctx, st, &Claims{Subject: "sub-7", Username: "no spaces please"}, true); err == nil {
			t.Fatal("a username with spaces was accepted")
		}
		if _, err := p.EnsureUser(ctx, st, nil, true); err == nil {
			t.Fatal("nil claims were accepted")
		}
	})
}
