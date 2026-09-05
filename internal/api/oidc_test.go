package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

// fakeIssuer serves just enough OpenID discovery for NewOIDC to build a
// provider. It never issues a token: these tests are about the half of the
// handshake this server owns.
func fakeIssuer(t *testing.T) *httptest.Server {
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

func oidcHarness(t *testing.T) *harness {
	t.Helper()
	issuer := fakeIssuer(t)
	return newHarness(t, func(c *config.Config) {
		c.OIDC = config.OIDC{Enabled: true, Issuer: issuer.URL, ClientID: "zoomies", ClientSecret: "secret"}
	})
}

// loginError is the reason a failed sign-in was sent back to the login page with.
func loginError(t *testing.T, res *response) string {
	t.Helper()
	if res.status != http.StatusFound {
		t.Fatalf("status = %d, want a redirect to the login page; body %s", res.status, res.body)
	}
	loc, err := url.Parse(res.header.Get("Location"))
	if err != nil || loc.Path != "/login" {
		t.Fatalf("Location = %q, want /login?error=...", res.header.Get("Location"))
	}
	return loc.Query().Get("error")
}

// The callback must only finish a handshake in the browser that started it.
// The server remembering the state proves the handshake began here; the cookie
// proves it began in this browser -- without it, an attacker could start their
// own sign-in, keep the callback URL, and have a victim load it, signing the
// victim in as the attacker.
func TestOIDCCallbackIsBoundToTheBrowserThatStartedIt(t *testing.T) {
	h := oidcHarness(t)
	if h.api.oidcErr != nil {
		t.Fatalf("single sign-on did not set up: %v", h.api.oidcErr)
	}

	start := h.do(request{method: http.MethodGet, path: "/api/v1/auth/oidc/start"})
	if start.status != http.StatusFound {
		t.Fatalf("start: status = %d, want 302; body %s", start.status, start.body)
	}
	to, err := url.Parse(start.header.Get("Location"))
	if err != nil {
		t.Fatalf("start: Location %q: %v", start.header.Get("Location"), err)
	}
	state := to.Query().Get("state")
	if state == "" {
		t.Fatalf("start: no state in %s", to)
	}
	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: start.header}).Cookies() {
		if c.Name == oidcStateCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != state || !cookie.HttpOnly {
		t.Fatalf("start: state cookie = %+v, want an HttpOnly cookie carrying %q", cookie, state)
	}

	callback := "/api/v1/auth/oidc/callback?code=abc&state=" + url.QueryEscape(state)

	// No cookie: a link somebody else made.
	reason := loginError(t, h.do(request{method: http.MethodGet, path: callback}))
	if !strings.Contains(reason, "did not start in this browser") {
		t.Fatalf("without the cookie the callback was not refused; reason %q", reason)
	}

	// The wrong cookie: a browser mid-way through a different sign-in.
	reason = loginError(t, h.do(request{method: http.MethodGet, path: callback,
		headers: map[string]string{"Cookie": oidcStateCookie + "=someone-elses"}}))
	if !strings.Contains(reason, "did not start in this browser") {
		t.Fatalf("with another browser's cookie the callback was not refused; reason %q", reason)
	}

	// The right cookie: the check passes and the handshake proceeds to the
	// token exchange, which the fake issuer refuses. That failure, and not
	// the browser check, is what comes back -- so the refusals above did not
	// spend the state.
	reason = loginError(t, h.do(request{method: http.MethodGet, path: callback,
		headers: map[string]string{"Cookie": oidcStateCookie + "=" + state}}))
	if strings.Contains(reason, "did not start in this browser") || strings.Contains(reason, "already been used") {
		t.Fatalf("with its own cookie the browser was refused: %q", reason)
	}
}
