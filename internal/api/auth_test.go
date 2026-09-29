package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// TestBootstrapCreatesTheFirstAdminThenRefuses is the whole security of the
// bootstrap route: it is unauthenticated, so it has to close permanently the
// moment an account exists.
func TestBootstrapCreatesTheFirstAdmin(t *testing.T) {
	h := newHarness(t)

	meta := h.do(request{method: http.MethodGet, path: "/api/v1/meta"})
	meta.mustStatus(t, http.StatusOK, "meta before bootstrap")
	if got := meta.json(t)["bootstrap_required"]; got != true {
		t.Fatalf("bootstrap_required = %v on a fresh instance, want true", got)
	}

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", body: map[string]any{
		"username": "root", "password": testPassword, "email": "root@example.com",
		"setup_token": h.setupToken(),
	}})
	resp.mustStatus(t, http.StatusCreated, "bootstrap")

	body := resp.json(t)
	// Whoever can read the setup token out of the controller's log already
	// operates the process, so the first account says so.
	if body["role"] != string(store.RolePlatform) {
		t.Errorf("first account has role %v, want platform", body["role"])
	}
	if resp.cookie == nil || resp.cookie.Value == "" {
		t.Fatal("bootstrap did not set a session cookie")
	}
	if !resp.cookie.HttpOnly || resp.cookie.SameSite != http.SameSiteLaxMode || resp.cookie.Path != "/" {
		t.Errorf("session cookie attributes are wrong: %+v", resp.cookie)
	}

	// The cookie it handed back has to work.
	session := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: resp.cookie.Value})
	session.mustStatus(t, http.StatusOK, "session after bootstrap")

	again := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", body: map[string]any{
		"username": "second", "password": testPassword, "setup_token": h.setupToken(),
	}})
	again.mustStatus(t, http.StatusConflict, "second bootstrap")
	if code := again.errorCode(t); code != codeConflict {
		t.Errorf("second bootstrap error code = %q, want %q", code, codeConflict)
	}

	// And the refusal is not a fluke of the first account being an admin: a
	// viewer-only instance stays closed too.
	if meta := h.do(request{method: http.MethodGet, path: "/api/v1/meta"}); meta.json(t)["bootstrap_required"] != false {
		t.Error("bootstrap_required is still true after the first account was created")
	}
}

func TestBootstrapRejectsAShortPassword(t *testing.T) {
	h := newHarness(t)
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", body: map[string]any{
		"username": "root", "password": "short", "setup_token": h.setupToken(),
	}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "bootstrap with a short password")

	var env errorEnvelope
	resp.into(t, &env)
	if len(env.Errors) == 0 || env.Errors[0].Field != "password" {
		t.Fatalf("expected a field error on password, got %+v", env)
	}
	if !strings.Contains(env.Errors[0].Message, "12") {
		t.Errorf("the message does not say how long a password must be: %q", env.Errors[0].Message)
	}
}

func TestTablePreferencesFollowTheSignedInAccount(t *testing.T) {
	h := newHarness(t)
	_, aliceCookie := h.user("alice", store.RoleViewer)
	_, bobCookie := h.user("bob", store.RoleViewer)

	put := h.do(request{method: http.MethodPut, path: "/api/v1/auth/preferences", cookie: aliceCookie,
		body: map[string]any{"table_layouts": map[string]any{"runners": map[string]any{
			"widths": map[string]any{"name": 240}, "order": []string{"name", "state"},
		}}}})
	put.mustStatus(t, http.StatusOK, "save table preferences")

	got := h.do(request{method: http.MethodGet, path: "/api/v1/auth/preferences", cookie: aliceCookie})
	got.mustStatus(t, http.StatusOK, "read table preferences")
	layouts := got.json(t)["table_layouts"].(map[string]any)
	runners := layouts["runners"].(map[string]any)
	if width := runners["widths"].(map[string]any)["name"]; width != float64(240) {
		t.Fatalf("saved name width = %v, want 240", width)
	}

	other := h.do(request{method: http.MethodGet, path: "/api/v1/auth/preferences", cookie: bobCookie})
	other.mustStatus(t, http.StatusOK, "read another account's preferences")
	if layouts, ok := other.json(t)["table_layouts"].(map[string]any); ok && len(layouts) != 0 {
		t.Fatalf("bob inherited alice's table layouts: %v", layouts)
	}
}

// TestLoginIsIndistinguishable checks the one property a login endpoint has to
// have: a wrong password and an unknown user must look the same.
func TestLoginIsIndistinguishable(t *testing.T) {
	h := newHarness(t)
	h.user("alice", store.RoleOperator)

	wrongPassword := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: map[string]any{
		"username": "alice", "password": "not-the-password",
	}})
	unknownUser := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: map[string]any{
		"username": "nobody-at-all", "password": "not-the-password",
	}})

	wrongPassword.mustStatus(t, http.StatusUnauthorized, "wrong password")
	unknownUser.mustStatus(t, http.StatusUnauthorized, "unknown user")
	if a, b := wrongPassword.errorMessage(t), unknownUser.errorMessage(t); a != b {
		t.Fatalf("a wrong password and an unknown user are distinguishable:\n  %q\n  %q", a, b)
	}
	if wrongPassword.cookie != nil && wrongPassword.cookie.Value != "" {
		t.Error("a refused login set a session cookie")
	}
}

func TestLoginLogoutRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.user("alice", store.RoleOperator)

	login := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: map[string]any{
		"username": "alice", "password": testPassword,
	}})
	login.mustStatus(t, http.StatusOK, "login")
	if login.cookie == nil {
		t.Fatal("login set no session cookie")
	}
	cookie := login.cookie.Value

	body := login.json(t)
	if body["name"] != "alice" || body["role"] != string(store.RoleOperator) {
		t.Errorf("identity = %v, want alice/operator", body)
	}

	session := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: cookie})
	session.mustStatus(t, http.StatusOK, "session")

	logout := h.do(request{method: http.MethodPost, path: "/api/v1/auth/logout", cookie: cookie})
	logout.mustStatus(t, http.StatusNoContent, "logout")
	if logout.cookie == nil || logout.cookie.MaxAge >= 0 {
		t.Errorf("logout did not expire the cookie: %+v", logout.cookie)
	}

	after := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: cookie})
	after.mustStatus(t, http.StatusUnauthorized, "session after logout")
}

func TestLoginIsRateLimited(t *testing.T) {
	h := newHarness(t)
	h.user("alice", store.RoleViewer)

	var last *response
	for range h.cfg.Security.RateLimitLogins + 2 {
		last = h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: map[string]any{
			"username": "alice", "password": "wrong",
		}})
	}
	last.mustStatus(t, http.StatusTooManyRequests, "login after too many attempts")
	if code := last.errorCode(t); code != codeRateLimited {
		t.Errorf("error code = %q, want %q", code, codeRateLimited)
	}
	// The helper always promised the header and the handler always passed
	// zero, so a client that honoured it retried immediately.
	secs, err := strconv.Atoi(last.header.Get("Retry-After"))
	if err != nil || secs <= 0 || secs > 60 {
		t.Errorf("Retry-After = %q, want the seconds left of the one-minute window", last.header.Get("Retry-After"))
	}
}

// TestBearerTokenAuthenticates covers the credential automation uses, including
// the two ways it stops working.
func TestBearerTokenAuthenticates(t *testing.T) {
	h := newHarness(t)

	token := h.token("ci", store.RoleViewer)
	ok := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: token})
	ok.mustStatus(t, http.StatusOK, "pools with a token")

	// A viewer token may not write.
	refused := h.do(request{method: http.MethodPost, path: "/api/v1/pools", token: token, body: map[string]any{}})
	refused.mustStatus(t, http.StatusForbidden, "write with a viewer token")

	// Revoked.
	revoked, plaintext, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{Name: "old", Role: store.RoleAdmin})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := h.ctrl.Auth().RevokeAPIToken(h.ctx, revoked.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: plaintext})
	resp.mustStatus(t, http.StatusUnauthorized, "revoked token")
	if !strings.Contains(resp.errorMessage(t), "revoked") {
		t.Errorf("the message does not say the token was revoked: %q", resp.errorMessage(t))
	}

}

func ptr[T any](v T) *T { return &v }

// TestExpiredTokenIsRefused drives the clock forward rather than the row
// backwards, which is what the auth service's injectable clock is for.
func TestExpiredTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	_, plaintext, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{
		Name: "short-lived", Role: store.RoleAdmin, ExpiresAt: ptr(time.Now().Add(50 * time.Millisecond)),
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: plaintext})
	resp.mustStatus(t, http.StatusUnauthorized, "expired token")
	if !strings.Contains(resp.errorMessage(t), "expired") {
		t.Errorf("the message does not say the token expired: %q", resp.errorMessage(t))
	}
}

// TestCSRF is the cookie/bearer asymmetry: a cookie rides along on a
// cross-origin request and a bearer token does not, so only the first needs the
// same-origin check.
func TestCSRF(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.pool(inst, "linux-x64")
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	token := h.token("ci", store.RoleOperator)

	foreign := h.do(request{
		method: http.MethodPost, path: "/api/v1/pools/validate",
		cookie: cookie, origin: "https://evil.example.com",
		body: map[string]any{"name": "x"},
	})
	foreign.mustStatus(t, http.StatusForbidden, "cookie POST from a foreign origin")
	if !strings.Contains(foreign.errorMessage(t), "evil.example.com") {
		t.Errorf("the refusal does not name the origin: %q", foreign.errorMessage(t))
	}

	sameOrigin := h.do(request{
		method: http.MethodPost, path: "/api/v1/pools/validate",
		cookie: cookie, body: map[string]any{"name": "x"},
	})
	if sameOrigin.status == http.StatusForbidden {
		t.Fatalf("a same-origin cookie POST was refused: %s", truncate(sameOrigin.body))
	}

	// The same foreign-origin request with a bearer token is allowed, because
	// a browser could not have made it.
	withToken := h.do(request{
		method: http.MethodPost, path: "/api/v1/pools/validate",
		token: token, origin: "https://evil.example.com",
		body: map[string]any{"name": "x"},
	})
	if withToken.status == http.StatusForbidden {
		t.Fatalf("a bearer POST from another origin was refused: %s", truncate(withToken.body))
	}

	// A cookie request with no browser headers at all cannot be shown to be
	// same-origin, so it is refused too.
	headerless := h.do(request{
		method: http.MethodPost, path: "/api/v1/pools/validate",
		cookie: cookie, noOrigin: true, body: map[string]any{"name": "x"},
	})
	headerless.mustStatus(t, http.StatusForbidden, "cookie POST with no Origin")

	// Reads are never refused: a cross-origin GET cannot be read by the page
	// that made it, and refusing it would break nothing but link-following.
	read := h.do(request{
		method: http.MethodGet, path: "/api/v1/pools", cookie: cookie,
		origin: "https://evil.example.com",
	})
	read.mustStatus(t, http.StatusOK, "cross-origin GET")
}

// TestCSRFRefusesACrossOriginLogin covers the one claim the csrf middleware's
// own doc comment makes and nothing asserted: the check runs on the whole
// /api/v1 subtree rather than only on cookie-authenticated routes, because a
// login CSRF -- signing the victim into the attacker's account, so that
// everything they then do is in a session the attacker can read -- is worth
// refusing too. Scoping the middleware to authenticated routes would look like
// a tidy-up and would silently reopen it.
func TestCSRFRefusesACrossOriginLogin(t *testing.T) {
	h := newHarness(t)
	h.user("alice", store.RoleAdmin)

	credentials := map[string]any{"username": "alice", "password": testPassword}

	foreign := h.do(request{
		method: http.MethodPost, path: "/api/v1/auth/login",
		origin: "https://evil.example.com", body: credentials,
	})
	foreign.mustStatus(t, http.StatusForbidden, "login posted from a foreign origin")
	if !strings.Contains(foreign.errorMessage(t), "evil.example.com") {
		t.Errorf("the refusal does not name the origin: %q", foreign.errorMessage(t))
	}
	// The refusal has to happen before the credentials are checked, or a
	// foreign page still learns whether the password was right -- and the
	// cookie must not have been minted whatever the answer was.
	if foreign.cookie != nil {
		t.Errorf("a cross-origin login still set a session cookie: %+v", foreign.cookie)
	}

	// Bootstrap mints the first admin, so a forged one is worse still.
	bootstrap := h.do(request{
		method: http.MethodPost, path: "/api/v1/auth/bootstrap",
		origin: "https://evil.example.com",
		body:   map[string]any{"username": "mallory", "password": testPassword, "setup_token": "whatever"},
	})
	bootstrap.mustStatus(t, http.StatusForbidden, "bootstrap posted from a foreign origin")

	// And the counterpart that keeps the check from being a blanket ban: a
	// plain client with no browser headers and no cookie is not a forged
	// request, it is curl, and it is about to be asked for credentials anyway.
	headless := h.do(request{
		method: http.MethodPost, path: "/api/v1/auth/login",
		noOrigin: true, body: credentials,
	})
	if headless.status == http.StatusForbidden {
		t.Fatalf("a non-browser login was refused as cross-origin: %s", truncate(headless.body))
	}
	headless.mustStatus(t, http.StatusOK, "non-browser login")

	// The ordinary browser login from the controller's own page still works.
	sameOrigin := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", body: credentials})
	sameOrigin.mustStatus(t, http.StatusOK, "same-origin login")
	if sameOrigin.cookie == nil {
		t.Error("a same-origin login minted no session cookie")
	}
}

func TestAllowedOriginsPermitsAConfiguredOrigin(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.AllowedOrigins = []string{"https://ops.example.com"}
	})
	u, _ := h.user("operator", store.RoleOperator)

	resp := h.do(request{
		method: http.MethodPost, path: "/api/v1/pools/validate",
		cookie: h.session(u), origin: "https://ops.example.com",
		body: map[string]any{"name": "x"},
	})
	if resp.status == http.StatusForbidden {
		t.Fatalf("a configured origin was refused: %s", truncate(resp.body))
	}
}

// TestDisableAuthSynthesisesThePlatformIdentity covers the local-development
// switch. It is refused by config validation off loopback, so the only thing
// to check here is that it does what it says: with authentication off there
// is no second audience, and the identity it synthesises reaches everything.
func TestDisableAuthSynthesisesThePlatformIdentity(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Security.DisableAuth = true })

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session"})
	resp.mustStatus(t, http.StatusOK, "session with auth disabled")
	body := resp.json(t)
	if body["name"] != "auth-disabled" || body["role"] != string(store.RolePlatform) {
		t.Fatalf("identity = %v, want the auth-disabled platform identity", body)
	}

	meta := h.do(request{method: http.MethodGet, path: "/api/v1/meta"})
	if meta.json(t)["auth_disabled"] != true {
		t.Error("meta does not report that authentication is disabled")
	}
}

// TestDisableAuthHasNoAccountForPreferences guards against a regression where
// the synthesised admin's fixed ID ("dev") was passed to the store as if it
// were a real user row: a GET degraded silently, but a PUT tried to insert a
// user_preferences row with no users row to reference, and the foreign key
// refused it with a 500 rather than the "no account to save this to" that an
// auth-disabled instance actually means.
func TestDisableAuthHasNoAccountForPreferences(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Security.DisableAuth = true })

	got := h.do(request{method: http.MethodGet, path: "/api/v1/auth/preferences"})
	got.mustStatus(t, http.StatusOK, "read preferences with auth disabled")
	if layouts, ok := got.json(t)["table_layouts"].(map[string]any); ok && len(layouts) != 0 {
		t.Fatalf("auth-disabled session has table layouts from nowhere: %v", layouts)
	}

	put := h.do(request{method: http.MethodPut, path: "/api/v1/auth/preferences",
		body: map[string]any{"table_layouts": map[string]any{}}})
	put.mustStatus(t, http.StatusForbidden, "save preferences with auth disabled")
}

func TestChangeOwnPassword(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("alice", store.RoleViewer)
	cookie := h.session(u)

	wrong := h.do(request{method: http.MethodPost, path: "/api/v1/auth/password", cookie: cookie,
		body: map[string]any{"old_password": "nope", "new_password": "another-good-password"}})
	wrong.mustStatus(t, http.StatusUnprocessableEntity, "change with the wrong current password")

	ok := h.do(request{method: http.MethodPost, path: "/api/v1/auth/password", cookie: cookie,
		body: map[string]any{"old_password": testPassword, "new_password": "another-good-password"}})
	ok.mustStatus(t, http.StatusNoContent, "change password")
	if ok.cookie == nil || ok.cookie.Value == "" {
		t.Fatal("changing a password did not issue a fresh session for this browser")
	}

	// The old cookie is gone; the new one works.
	old := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: cookie})
	old.mustStatus(t, http.StatusUnauthorized, "the old session after a password change")
	fresh := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: ok.cookie.Value})
	fresh.mustStatus(t, http.StatusOK, "the new session after a password change")
}

// TestChangeOwnPasswordIsRateLimited is the 429 the OpenAPI spec promises for
// the route: a stolen session cookie must not be a free oracle for the account
// password, so wrong "current password" guesses are charged to the login limits
// and told when to come back.
func TestChangeOwnPasswordIsRateLimited(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("alice", store.RoleViewer)
	cookie := h.session(u)

	var last *response
	for range h.cfg.Security.RateLimitLogins + 2 {
		last = h.do(request{method: http.MethodPost, path: "/api/v1/auth/password", cookie: cookie,
			body: map[string]any{"old_password": "nope", "new_password": "another-good-password"}})
	}
	last.mustStatus(t, http.StatusTooManyRequests, "change password after too many guesses")
	if code := last.errorCode(t); code != codeRateLimited {
		t.Errorf("error code = %q, want %q", code, codeRateLimited)
	}
	if secs, err := strconv.Atoi(last.header.Get("Retry-After")); err != nil || secs <= 0 {
		t.Errorf("Retry-After = %q, want the seconds left of the window", last.header.Get("Retry-After"))
	}
}

// ---------------------------------------------------------------------------
// Single sign-on
// ---------------------------------------------------------------------------

// The OIDC state is minted server-side and remembered in a cookie on the
// browser that began the handshake. Without that binding, a state an attacker
// obtained by signing in as themselves is one this controller would accept from
// anybody's browser -- and the victim would end up signed in as the attacker.
//
// The provider here is a bare value: Enabled() only asks whether there is one,
// and the state check happens before anything touches it, which is the point.
func TestOIDCCallbackRefusesAStateThisBrowserDidNotStart(t *testing.T) {
	h := newHarness(t)
	h.api.useOIDC(&auth.OIDCProvider{})

	cases := []struct {
		name   string
		cookie string
		state  string
	}{
		{name: "no cookie at all", state: "state-from-somewhere-else"},
		{name: "a state from another browser", cookie: "ours", state: "theirs"},
		{name: "a cookie but no state", cookie: "ours"},
		{name: "neither"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request{method: http.MethodGet, path: "/api/v1/auth/oidc/callback?code=abc&state=" + tc.state}
			if tc.cookie != "" {
				req.headers = map[string]string{"Cookie": oidcStateCookie + "=" + tc.cookie}
			}
			resp := h.do(req)
			resp.mustStatus(t, http.StatusFound, "callback with "+tc.name)
			if loc := resp.header.Get("Location"); !strings.HasPrefix(loc, "/login?error=") {
				t.Fatalf("Location = %q; want a redirect back to the login page", loc)
			}
			if resp.cookie != nil && resp.cookie.Value != "" {
				t.Fatal("a refused callback issued a session")
			}
		})
	}
}

// Whatever the outcome, the state cookie is spent: leaving it in place would
// let the same callback be replayed against this browser.
func TestOIDCCallbackClearsTheStateCookie(t *testing.T) {
	h := newHarness(t)
	h.api.useOIDC(&auth.OIDCProvider{})

	resp := h.do(request{
		method:  http.MethodGet,
		path:    "/api/v1/auth/oidc/callback?code=abc&state=theirs",
		headers: map[string]string{"Cookie": oidcStateCookie + "=ours"},
	})
	resp.mustStatus(t, http.StatusFound, "refused callback")

	var cleared bool
	for _, c := range resp.header.Values("Set-Cookie") {
		if strings.HasPrefix(c, oidcStateCookie+"=;") || strings.Contains(c, oidcStateCookie+"=;") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("the state cookie was not cleared: %v", resp.header.Values("Set-Cookie"))
	}
}

// The bootstrap route has to be open on a fresh install and closed to everybody
// who is not the operator, and "no account exists yet" does not tell those two
// apart. The setup token does: it is printed in this controller's log, so
// holding it means having deployed the thing.
func TestBootstrapNeedsTheSetupToken(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{name: "no token at all", body: map[string]any{"username": "root", "password": testPassword}},
		{name: "an empty token", body: map[string]any{"username": "root", "password": testPassword, "setup_token": "  "}},
		{name: "somebody else's guess", body: map[string]any{"username": "root", "password": testPassword, "setup_token": "zoo-not-the-token"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", body: tc.body})
			resp.mustStatus(t, http.StatusUnprocessableEntity, "bootstrap with "+tc.name)

			var env errorEnvelope
			resp.into(t, &env)
			if len(env.Errors) == 0 || env.Errors[0].Field != "setup_token" {
				t.Fatalf("expected a field error on setup_token, got %+v", env)
			}
			if resp.cookie != nil && resp.cookie.Value != "" {
				t.Fatal("a refused bootstrap issued a session")
			}
		})
	}

	// Nothing was created, so the instance is still waiting for its first
	// account rather than quietly belonging to whoever tried.
	if meta := h.do(request{method: http.MethodGet, path: "/api/v1/meta"}); meta.json(t)["bootstrap_required"] != true {
		t.Fatal("a refused bootstrap created an account")
	}
	users, err := h.st.ListUsers(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Fatalf("accounts = %d after refused bootstraps; want none", len(users))
	}
}

// The compose deployment is https on the outside and plain http inside, and an
// operator checking the container by IP before DNS exists would otherwise
// create the one administrator and lose the session in the same second: the
// browser will not keep a Secure cookie from an insecure page, and the endpoint
// that made the account has closed for good. The browser's Origin is the one
// fact that says which page the request came from.
func TestBootstrapRefusesAPageThatWouldDropTheSecureCookie(t *testing.T) {
	secure := true
	h := newHarness(t, func(c *config.Config) {
		c.Server.ExternalURL = "https://zoomies.test"
		c.Server.AllowedOrigins = []string{"http://10.0.0.5"}
		c.Security.CookieSecure = &secure
	})

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", origin: "http://10.0.0.5",
		body: map[string]any{"username": "root", "password": testPassword, "setup_token": h.setupToken()}})
	resp.mustStatus(t, http.StatusBadRequest, "bootstrap from a plain-http page")
	for _, want := range []string{"http://10.0.0.5", "https://zoomies.test", "Secure", "ZOOMIES_COOKIE_SECURE=false"} {
		if !strings.Contains(resp.errorMessage(t), want) {
			t.Errorf("the refusal does not say %q: %s", want, resp.errorMessage(t))
		}
	}

	// Nothing was created by the refused request: the same account can still
	// be made from the https page.
	ok := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", origin: "https://zoomies.test",
		body: map[string]any{"username": "root", "password": testPassword, "setup_token": h.setupToken()}})
	ok.mustStatus(t, http.StatusCreated, "bootstrap from the https page")
	if ok.cookie == nil || !ok.cookie.Secure {
		t.Fatal("the session cookie from the https page is not Secure")
	}
}

// Login gets the same refusal, and a loopback page does not: browsers treat
// localhost as a secure context and keep the cookie there, which is how a
// developer runs this over plain http on purpose.
func TestLoginRefusesAPageThatWouldDropTheSecureCookieButNotLocalhost(t *testing.T) {
	secure := true
	h := newHarness(t, func(c *config.Config) {
		c.Server.ExternalURL = "https://zoomies.test"
		c.Server.AllowedOrigins = []string{"http://10.0.0.5", "http://localhost:8080"}
		c.Security.CookieSecure = &secure
	})
	h.user("alice", store.RoleAdmin)

	refused := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", origin: "http://10.0.0.5",
		body: map[string]any{"username": "alice", "password": testPassword}})
	refused.mustStatus(t, http.StatusBadRequest, "login from a plain-http page")

	local := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login", origin: "http://localhost:8080",
		body: map[string]any{"username": "alice", "password": testPassword}})
	local.mustStatus(t, http.StatusOK, "login from localhost")
}

// A database that stops answering used to come back through the anonymous
// routes as a 422 quoting the driver's error, as though the caller had typed
// something wrong; it never reached the error log either, because the 422
// path does not log. The cause belongs in the log and the caller gets a
// request ID to quote.
func TestDatabaseFailuresAreInternalErrorsNotValidationMessages(t *testing.T) {
	h := newHarness(t)
	if err := h.st.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	boot := h.do(request{method: http.MethodPost, path: "/api/v1/auth/bootstrap", body: map[string]any{
		"username": "root", "password": testPassword, "setup_token": h.setupToken(),
	}})
	boot.mustStatus(t, http.StatusInternalServerError, "bootstrap against a closed database")
	if msg := boot.errorMessage(t); strings.Contains(strings.ToLower(msg), "sql") || strings.Contains(msg, "closed") {
		t.Fatalf("the database error reached an anonymous caller: %q", msg)
	}

	ready := h.do(request{method: http.MethodGet, path: "/readyz"})
	ready.mustStatus(t, http.StatusServiceUnavailable, "readiness against a closed database")
	body := ready.json(t)
	if msg, _ := body["message"].(string); strings.Contains(strings.ToLower(msg), "sql") || strings.Contains(msg, "closed") {
		t.Fatalf("the readiness probe quoted the database error: %q", msg)
	}
}

// A 401 tells every client to sign in again, and the web client acts on it by
// dropping a perfectly good session. A database that stops answering while a
// credential is being looked up used to come back as exactly that, with the
// driver's own error in the body. It is the controller that is broken, not the
// credential, so it is a 500 with a request ID and the cause in the log only.
func TestDatabaseFailuresWhileCheckingACredentialAreInternalErrorsNotSignOuts(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	token := h.token("ci", store.RoleViewer)
	if err := h.st.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	for _, tc := range []struct {
		name string
		req  request
	}{
		{"a session cookie", request{method: http.MethodGet, path: "/api/v1/pools", cookie: cookie}},
		{"an API token", request{method: http.MethodGet, path: "/api/v1/pools", token: token}},
		{"an API token on the MCP endpoint", request{
			method: http.MethodPost, path: "/mcp", token: token, noOrigin: true,
			body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(tc.req)
			resp.mustStatus(t, http.StatusInternalServerError, "a credential check against a closed database")
			if code := resp.errorCode(t); code != codeInternal {
				t.Errorf("error code = %q, want %q", code, codeInternal)
			}
			msg := strings.ToLower(resp.errorMessage(t))
			for _, leak := range []string{"looking up", "sql", "closed"} {
				if strings.Contains(msg, leak) {
					t.Errorf("the database error reached the caller: %q", msg)
				}
			}
			if detail, _ := resp.json(t)["error"].(map[string]any)["detail"].(string); !strings.HasPrefix(detail, "request ") {
				t.Errorf("detail = %q, want a request ID the operator can quote", detail)
			}
			// A challenge on a 500 would send an MCP client off to start a
			// sign-in that cannot help.
			if got := resp.header.Get("WWW-Authenticate"); got != "" {
				t.Errorf("WWW-Authenticate = %q on a server error, want none", got)
			}
		})
	}
}

// The other half of the rule above: a credential the database answered about
// and refused stays a 401, so the UI still signs out an expired session.
func TestARefusedCredentialIsStillA401(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name string
		req  request
	}{
		{"an unknown session cookie", request{method: http.MethodGet, path: "/api/v1/pools", cookie: "not-a-session"}},
		{"an unknown API token", request{method: http.MethodGet, path: "/api/v1/pools", token: "zoo_not-a-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.do(tc.req).mustStatus(t, http.StatusUnauthorized, tc.name)
		})
	}
}

// TestForwardedProtoIsBelievedOnlyFromATrustedProxy is the header's side of the
// rule X-Forwarded-For has always followed.
//
// The scheme is not decoration. It decides whether an https:// Origin counts as
// this controller's own -- which is a CSRF check -- and whether the response
// carries HSTS. Believing a header any client can set let a caller choose the
// answer to both.
func TestForwardedProtoIsBelievedOnlyFromATrustedProxy(t *testing.T) {
	// The harness's requests come from loopback, so a loopback CIDR is what
	// makes this test server "behind a proxy".
	const loopback = "127.0.0.0/8"

	secureOrigin := func(h *harness) string {
		return "https://" + strings.TrimPrefix(h.srv.URL, "http://")
	}

	t.Run("a forged header does not make an https origin same-origin", func(t *testing.T) {
		h := newHarness(t)
		u, _ := h.user("operator", store.RoleOperator)

		resp := h.do(request{
			method: http.MethodPost, path: "/api/v1/pools/validate",
			cookie: h.session(u), origin: secureOrigin(h),
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			body:    map[string]any{"name": "x"},
		})
		resp.mustStatus(t, http.StatusForbidden, "a cookie POST claiming https over a plain connection")
	})

	t.Run("the same header from a trusted proxy is believed", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) {
			c.Server.TrustedProxies = []string{loopback}
		})
		u, _ := h.user("operator", store.RoleOperator)

		resp := h.do(request{
			method: http.MethodPost, path: "/api/v1/pools/validate",
			cookie: h.session(u), origin: secureOrigin(h),
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			body:    map[string]any{"name": "x"},
		})
		if resp.status == http.StatusForbidden {
			t.Fatalf("a request through a configured proxy was refused: %s", truncate(resp.body))
		}
	})

	t.Run("HSTS is not emitted because a client asked for it", func(t *testing.T) {
		h := newHarness(t)
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/meta",
			headers: map[string]string{"X-Forwarded-Proto": "https"}})
		if got := resp.header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("Strict-Transport-Security = %q on a plain connection; anyone who can reach "+
				"the listener could pin this host in a browser for a year", got)
		}
	})

	t.Run("HSTS is emitted behind a proxy that terminates TLS", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) {
			c.Server.TrustedProxies = []string{loopback}
		})
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/meta",
			headers: map[string]string{"X-Forwarded-Proto": "https"}})
		if got := resp.header.Get("Strict-Transport-Security"); got == "" {
			t.Error("no Strict-Transport-Security behind a configured TLS-terminating proxy, " +
				"which is the deployment the header exists for")
		}
	})

	// A chain appends, and the entries after the first are hops inside the
	// operator's own network. The client's own scheme is the left-most one, so
	// a plain request that crossed an internal TLS hop is still plain.
	t.Run("a chain is read left to right", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) {
			c.Server.TrustedProxies = []string{loopback}
		})
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/meta",
			headers: map[string]string{"X-Forwarded-Proto": "http, https"}})
		if got := resp.header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("Strict-Transport-Security = %q, though the client reached the first proxy over http", got)
		}
	})
}
