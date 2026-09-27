package api

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238's algorithm, recomputed here independently of the auth package.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// codeAt is an authenticator app: RFC 6238 written out a second time, so
// these tests do not lean on the implementation they are checking.
func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatalf("decoding the secret: %v", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

// signInCookieOf picks the pending-sign-in cookie out of a response.
func signInCookieOf(res *response) *http.Cookie {
	for _, c := range (&http.Response{Header: res.header}).Cookies() {
		if c.Name == SignInCookie {
			return c
		}
	}
	return nil
}

func withSignIn(value string) map[string]string {
	return map[string]string{"Cookie": SignInCookie + "=" + value}
}

// enrolOverAPI turns two-step on for a signed-in account the way the account
// page does, and returns the key.
func enrolOverAPI(t *testing.T, h *harness, session string) (string, []string) {
	t.Helper()
	setup := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/setup", cookie: session})
	setup.mustStatus(t, http.StatusOK, "setup")
	var s struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
		QR     string `json:"qr_svg"`
	}
	setup.into(t, &s)
	if !strings.HasPrefix(s.URI, "otpauth://totp/Zoomies:") || !strings.Contains(s.URI, "zoomies.test") {
		t.Errorf("otpauth address = %q, want it to name the issuer and this instance", s.URI)
	}
	if !strings.HasPrefix(s.QR, "<svg") {
		t.Errorf("qr_svg is not an SVG document: %.40s", s.QR)
	}
	confirm := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/confirm", cookie: session,
		body: map[string]any{"code": codeAt(t, s.Secret, time.Now())}})
	confirm.mustStatus(t, http.StatusOK, "confirm")
	var codes struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	confirm.into(t, &codes)
	return s.Secret, codes.RecoveryCodes
}

func TestTwoStepSignInOverTheAPI(t *testing.T) {
	h := newHarness(t)
	u, session := h.user("alice", store.RoleOperator)
	secret, codes := enrolOverAPI(t, h, session)
	if len(codes) != 10 {
		t.Fatalf("%d recovery codes, want 10", len(codes))
	}

	login := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": testPassword}})
	login.mustStatus(t, http.StatusAccepted, "login with two-step on")
	if login.cookie != nil {
		t.Fatal("the password alone set a session cookie")
	}
	pending := signInCookieOf(login)
	if pending == nil || !pending.HttpOnly || pending.SameSite != http.SameSiteStrictMode || pending.Path != "/api/v1/auth" {
		t.Fatalf("pending sign-in cookie = %+v", pending)
	}
	if pending.MaxAge <= 0 || pending.MaxAge > 300 {
		t.Errorf("pending sign-in cookie lives %ds, want at most five minutes", pending.MaxAge)
	}
	if body := login.json(t); body["two_step"] != "verify" || body["username"] != "alice" {
		t.Errorf("202 body = %v", body)
	}

	// The pending cookie is not a session anywhere.
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", headers: withSignIn(pending.Value)}).
		mustStatus(t, http.StatusUnauthorized, "session with only the pending cookie")

	wrong := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/verify",
		headers: withSignIn(pending.Value), body: map[string]any{"code": "000000"}})
	wrong.mustStatus(t, http.StatusUnauthorized, "a wrong code")
	if !strings.Contains(string(wrong.body), `"field":"code"`) {
		t.Errorf("a wrong code is not pointed at the code field, so the page cannot tell it from an expired sign-in: %s", wrong.body)
	}

	// The confirming code used this step, so the phone's next one is the
	// one a person would type.
	next := codeAt(t, secret, time.Now().Add(30*time.Second))
	ok := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/verify",
		headers: withSignIn(pending.Value), body: map[string]any{"code": next}})
	ok.mustStatus(t, http.StatusOK, "the right code")
	if ok.cookie == nil || ok.cookie.Value == "" {
		t.Fatal("the code step set no session")
	}
	var in struct {
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
		RecoveryCodeUsed bool `json:"recovery_code_used"`
	}
	ok.into(t, &in)
	if in.Identity.ID != u.ID || in.RecoveryCodeUsed {
		t.Errorf("sign-in = %+v", in)
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: ok.cookie.Value}).
		mustStatus(t, http.StatusOK, "the new session")

	// The same code on a fresh sign-in is a replay.
	again := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": testPassword}})
	again.mustStatus(t, http.StatusAccepted, "second login")
	replay := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/verify",
		headers: withSignIn(signInCookieOf(again).Value), body: map[string]any{"code": next}})
	replay.mustStatus(t, http.StatusUnauthorized, "a replayed code")

	// A recovery code stands in, once, and the answer says how many are left.
	rec := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/verify",
		headers: withSignIn(signInCookieOf(again).Value), body: map[string]any{"code": codes[0]}})
	rec.mustStatus(t, http.StatusOK, "a recovery code")
	if body := rec.json(t); body["recovery_code_used"] != true || body["recovery_codes_left"] != float64(9) {
		t.Errorf("recovery sign-in = %v", body)
	}

	// Signed in, the account page sees it on.
	status := h.do(request{method: http.MethodGet, path: "/api/v1/auth/two-step", cookie: ok.cookie.Value})
	status.mustStatus(t, http.StatusOK, "status")
	if body := status.json(t); body["enabled"] != true || body["recovery_codes_left"] != float64(9) || body["available"] != true {
		t.Errorf("status = %v", body)
	}
}

func TestAPendingSignInCannotBeFinishedWithoutItsCookie(t *testing.T) {
	h := newHarness(t)
	_, session := h.user("alice", store.RoleOperator)
	secret, _ := enrolOverAPI(t, h, session)
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": testPassword}}).
		mustStatus(t, http.StatusAccepted, "login")

	for name, headers := range map[string]map[string]string{
		"no cookie":        nil,
		"a made-up cookie": withSignIn("not-a-real-challenge"),
	} {
		res := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/verify", headers: headers,
			body: map[string]any{"code": codeAt(t, secret, time.Now().Add(30*time.Second))}})
		if res.status != http.StatusUnauthorized || res.cookie != nil {
			t.Errorf("%s: status %d, session %v; want 401 and no session", name, res.status, res.cookie)
		}
	}
}

func TestTheSelfServiceRoutesRefuseATokenAndTakeThePasswordToTurnItOff(t *testing.T) {
	h := newHarness(t)
	_, session := h.user("alice", store.RoleOperator)
	secret, _ := enrolOverAPI(t, h, session)

	tok := h.token("ci", store.RoleAdmin)
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/two-step", token: tok}).
		mustStatus(t, http.StatusForbidden, "status with a token")

	bad := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/disable", cookie: session,
		body: map[string]any{"password": "wrong-password-entirely", "code": codeAt(t, secret, time.Now().Add(30*time.Second))}})
	bad.mustStatus(t, http.StatusUnprocessableEntity, "disable with the wrong password")
	if !strings.Contains(string(bad.body), `"password"`) {
		t.Errorf("the refusal does not point at the password field: %s", bad.body)
	}
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/disable", cookie: session,
		body: map[string]any{"password": testPassword, "code": codeAt(t, secret, time.Now().Add(30*time.Second))}}).
		mustStatus(t, http.StatusNoContent, "disable")
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": testPassword}}).
		mustStatus(t, http.StatusOK, "login with two-step off")
}

func TestAnAdministratorsTwoStepResetIsAudited(t *testing.T) {
	h := newHarness(t)
	target, session := h.user("alice", store.RoleOperator)
	enrolOverAPI(t, h, session)
	_, adminSession := h.user("root", store.RoleAdmin)

	list := h.do(request{method: http.MethodGet, path: "/api/v1/users", cookie: adminSession})
	list.mustStatus(t, http.StatusOK, "list users")
	if !strings.Contains(string(list.body), `"username":"alice"`) || !strings.Contains(string(list.body), `"two_step_enabled":true`) {
		t.Errorf("the users list does not show alice's two-step: %s", truncate(list.body))
	}

	// A viewer cannot.
	_, viewer := h.user("val", store.RoleViewer)
	h.do(request{method: http.MethodDelete, path: "/api/v1/users/" + target.ID + "/two-step", cookie: viewer}).
		mustStatus(t, http.StatusForbidden, "a viewer's reset")

	h.do(request{method: http.MethodDelete, path: "/api/v1/users/" + target.ID + "/two-step", cookie: adminSession}).
		mustStatus(t, http.StatusNoContent, "reset")

	events, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"user.two_step_reset"}}, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TargetID != target.ID || events[0].ActorName != "root" {
		t.Fatalf("audit rows = %+v, want one naming alice's account and the administrator", events)
	}
	if !strings.Contains(events[0].After, `"had_two_step":true`) {
		t.Errorf("the audit row does not say there was something to reset: %s", events[0].After)
	}
	// Her sessions ended, and her password alone signs her in again.
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: session}).
		mustStatus(t, http.StatusUnauthorized, "alice's old session")
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": testPassword}}).
		mustStatus(t, http.StatusOK, "alice signs in after the reset")
}

func TestRequiringTwoStepSendsASignInToEnrolment(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Security.RequireTwoStep = true })
	h.user("alice", store.RoleOperator)

	login := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": testPassword}})
	login.mustStatus(t, http.StatusAccepted, "login")
	if body := login.json(t); body["two_step"] != "enrol" {
		t.Fatalf("202 body = %v, want the enrol step", body)
	}
	pending := signInCookieOf(login).Value

	setup := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/enrol", headers: withSignIn(pending)})
	setup.mustStatus(t, http.StatusOK, "enrol")
	var s struct {
		Secret string `json:"secret"`
	}
	setup.into(t, &s)

	done := h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/enrol/confirm", headers: withSignIn(pending),
		body: map[string]any{"code": codeAt(t, s.Secret, time.Now())}})
	done.mustStatus(t, http.StatusOK, "confirm")
	if done.cookie == nil {
		t.Fatal("finishing enrolment did not sign in")
	}
	var in struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	done.into(t, &in)
	if len(in.RecoveryCodes) != 10 {
		t.Fatalf("%d recovery codes, want 10", len(in.RecoveryCodes))
	}
}

// An account from the identity provider has no password for a step to
// follow; requiring two-step must leave its session, and its account page,
// working.
func TestSingleSignOnAccountsAreUnaffectedByRequiredTwoStep(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Security.RequireTwoStep = true })
	u := &store.User{Username: "sso-user", Role: store.RoleViewer, OIDCSubject: "sub-1"}
	if err := h.st.CreateUser(h.ctx, u); err != nil {
		t.Fatal(err)
	}
	session := h.session(u)
	h.do(request{method: http.MethodGet, path: "/api/v1/stats", cookie: session}).
		mustStatus(t, http.StatusOK, "a single sign-on session")
	status := h.do(request{method: http.MethodGet, path: "/api/v1/auth/two-step", cookie: session})
	status.mustStatus(t, http.StatusOK, "status")
	if body := status.json(t); body["available"] != false || body["required"] != true {
		t.Errorf("status = %v, want unavailable for this account", body)
	}
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/two-step/setup", cookie: session}).
		mustStatus(t, http.StatusUnprocessableEntity, "setup for a single sign-on account")
}
