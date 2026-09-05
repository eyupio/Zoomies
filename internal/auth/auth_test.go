package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// testPassword is long enough to satisfy MinPasswordLength.
const testPassword = "correct horse battery staple"

// hashedTestPassword is computed once: argon2id is deliberately expensive, and
// a test suite that hashed it per case would spend most of its time in the KDF.
var hashedTestPassword = sync.OnceValue(func() string {
	h, err := cryptox.HashPassword(testPassword)
	if err != nil {
		panic(err)
	}
	return h
})

// clock is a settable time source shared by the store and the service, so a
// test can expire a session without sleeping.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2025, 3, 4, 10, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newService returns a service on a fresh in-memory database. Login rate
// limiting is off by default so unrelated tests cannot exhaust each other's
// budget; TestLoginRateLimit builds its own service with a limit.
func newService(t *testing.T) (*Service, *store.Store, *clock) {
	t.Helper()
	cfg := config.Default()
	cfg.Security.RateLimitLogins = 0
	cfg.Security.SessionTTL = time.Hour
	return newServiceWith(t, cfg)
}

func newServiceWith(t *testing.T, cfg *config.Config) (*Service, *store.Store, *clock) {
	t.Helper()
	c := newClock()
	st, err := store.Open(t.Context(), store.Options{Path: ":memory:", Now: c.Now})
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, cfg, events.New(), WithClock(c.Now)), st, c
}

// addUser inserts an account directly, reusing the one expensive hash.
func addUser(t *testing.T, st *store.Store, username string, role store.Role, mutate func(*store.User)) *store.User {
	t.Helper()
	u := &store.User{Username: username, Role: role, PasswordHash: hashedTestPassword()}
	if mutate != nil {
		mutate(u)
	}
	if err := st.CreateUser(t.Context(), u); err != nil {
		t.Fatalf("creating user %s: %v", username, err)
	}
	return u
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

func TestBootstrapRefusesASecondFirstAdmin(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()

	need, err := s.NeedsBootstrap(ctx)
	if err != nil || !need {
		t.Fatalf("NeedsBootstrap on an empty instance = %v, %v; want true, nil", need, err)
	}

	admin, err := s.CreateFirstAdmin(ctx, "Root", testPassword)
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if admin.Username != "root" {
		t.Errorf("username = %q; want it lowercased to %q", admin.Username, "root")
	}
	if admin.Role != store.RoleAdmin {
		t.Errorf("role = %q; want admin", admin.Role)
	}

	// The whole security of the unauthenticated bootstrap route is that it
	// closes for good once any account exists.
	if _, err := s.CreateFirstAdmin(ctx, "second", testPassword); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("second CreateFirstAdmin = %v; want ErrAlreadyBootstrapped", err)
	}

	// Even a non-admin account has to close it: otherwise anyone could claim
	// admin on an instance that has only viewers.
	if _, err := st.ListUsers(ctx); err != nil {
		t.Fatal(err)
	}
	need, err = s.NeedsBootstrap(ctx)
	if err != nil || need {
		t.Fatalf("NeedsBootstrap after bootstrap = %v, %v; want false, nil", need, err)
	}
}

func TestBootstrapRejectsAShortPassword(t *testing.T) {
	s, _, _ := newService(t)
	if _, err := s.CreateFirstAdmin(t.Context(), "root", "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("CreateFirstAdmin with a short password = %v; want ErrPasswordTooShort", err)
	}
	if !strings.Contains(ErrPasswordTooShort.Error(), "12") {
		t.Errorf("the too-short message should say the minimum: %q", ErrPasswordTooShort)
	}
}

func TestBootstrapIsClosedOnceAnyAccountExists(t *testing.T) {
	s, st, _ := newService(t)
	addUser(t, st, "viewer", store.RoleViewer, nil)
	if _, err := s.CreateFirstAdmin(t.Context(), "root", testPassword); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("CreateFirstAdmin with a viewer present = %v; want ErrAlreadyBootstrapped", err)
	}
}

// ---------------------------------------------------------------------------
// Password login
// ---------------------------------------------------------------------------

func TestLoginUnknownUserLooksLikeAWrongPassword(t *testing.T) {
	s, st, _ := newService(t)
	addUser(t, st, "alice", store.RoleAdmin, nil)

	start := time.Now()
	_, _, unknownErr := s.Login(t.Context(), "nobody", testPassword, "10.0.0.1", "test")
	unknownTook := time.Since(start)

	_, _, wrongErr := s.Login(t.Context(), "alice", "not the password", "10.0.0.2", "test")

	if !errors.Is(unknownErr, ErrInvalidCredentials) || !errors.Is(wrongErr, ErrInvalidCredentials) {
		t.Fatalf("errors = %v (unknown), %v (wrong); want both ErrInvalidCredentials", unknownErr, wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("messages differ, which enumerates accounts:\n unknown: %v\n wrong:   %v", unknownErr, wrongErr)
	}
	// cryptox.DummyVerify runs the real KDF (64 MiB, two passes). Skipping it
	// would return in microseconds, which is the timing oracle this guards.
	if unknownTook < 5*time.Millisecond {
		t.Errorf("unknown-user login took %v; too fast to have run the dummy KDF, so response timing enumerates accounts", unknownTook)
	}
}

func TestLoginRefusesDisabledAndSSOOnlyAccounts(t *testing.T) {
	s, st, _ := newService(t)
	addUser(t, st, "disabled", store.RoleViewer, func(u *store.User) { u.Disabled = true })
	addUser(t, st, "sso", store.RoleViewer, func(u *store.User) {
		u.PasswordHash = ""
		u.OIDCSubject = "sub-1"
	})

	if _, _, err := s.Login(t.Context(), "disabled", testPassword, "10.0.0.1", ""); !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("disabled account login = %v; want ErrAccountDisabled", err)
	}
	_, _, err := s.Login(t.Context(), "sso", testPassword, "10.0.0.1", "")
	if !errors.Is(err, ErrSSOOnly) {
		t.Fatalf("OIDC-only account login = %v; want ErrSSOOnly", err)
	}
	if !strings.Contains(err.Error(), "single sign-on") {
		t.Errorf("the SSO-only message should point at SSO: %q", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Security.RateLimitLogins = 2
	s, st, c := newServiceWith(t, cfg)
	addUser(t, st, "alice", store.RoleAdmin, nil)

	for i := range 2 {
		if _, _, err := s.Login(t.Context(), "alice", "wrong", "10.0.0.9", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d = %v; want ErrInvalidCredentials", i+1, err)
		}
	}
	if _, _, err := s.Login(t.Context(), "alice", testPassword, "10.0.0.9", ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third attempt = %v; want ErrRateLimited even with the right password", err)
	}
	// A different address is unaffected.
	if _, _, err := s.Login(t.Context(), "alice", "wrong", "10.0.0.10", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("other address = %v; want ErrInvalidCredentials", err)
	}

	c.Advance(2 * time.Minute)
	if _, _, err := s.Login(t.Context(), "alice", testPassword, "10.0.0.9", ""); err != nil {
		t.Fatalf("after the window = %v; want the login to succeed", err)
	}
}

func TestLoginSuccessResetsTheAddressBudget(t *testing.T) {
	cfg := config.Default()
	cfg.Security.RateLimitLogins = 3
	s, st, _ := newServiceWith(t, cfg)
	addUser(t, st, "alice", store.RoleAdmin, nil)

	if _, _, err := s.Login(t.Context(), "alice", "wrong", "10.0.0.1", ""); err == nil {
		t.Fatal("expected the wrong password to fail")
	}
	if _, _, err := s.Login(t.Context(), "alice", testPassword, "10.0.0.1", ""); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := s.logins.RetryAfter("10.0.0.1"); got != 0 {
		t.Errorf("RetryAfter after a good login = %v; want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func TestSessionRoundTripAndExpiry(t *testing.T) {
	s, st, c := newService(t)
	u := addUser(t, st, "alice", store.RoleOperator, nil)
	ctx := t.Context()

	got, token, err := s.Login(ctx, "alice", testPassword, "10.0.0.1", "curl/8")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got.LastLoginAt == nil {
		t.Error("last_login_at was not recorded")
	}
	if token == "" {
		t.Fatal("login returned an empty session token")
	}

	id, err := s.Authenticate(ctx, AuthInput{SessionCookie: token, IP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Kind != KindUser || id.ID != u.ID || id.Role != store.RoleOperator {
		t.Errorf("identity = %+v; want the operator user %s", id, u.ID)
	}
	if !id.Can(ActionPoolsWrite) || id.Can(ActionUsersWrite) {
		t.Errorf("operator identity has the wrong authority: %s", id)
	}

	// Only the hash is stored, so the database never holds a usable cookie.
	if _, _, err := st.GetSessionByTokenHash(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Error("the raw cookie value resolves to a session; it must be stored hashed")
	}

	c.Advance(2 * time.Hour) // SessionTTL is one hour in these tests
	if _, err := s.Authenticate(ctx, AuthInput{SessionCookie: token}); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired session = %v; want ErrSessionExpired", err)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	s, st, _ := newService(t)
	addUser(t, st, "alice", store.RoleViewer, nil)
	ctx := t.Context()

	_, token, err := s.Login(ctx, "alice", testPassword, "10.0.0.1", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := s.Authenticate(ctx, AuthInput{SessionCookie: token}); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("after logout = %v; want ErrSessionExpired", err)
	}
	// Logging out twice is not an error: the caller wanted to be signed out.
	if err := s.Logout(ctx, token); err != nil {
		t.Errorf("second logout: %v", err)
	}
}

func TestAuthenticateWithoutCredentials(t *testing.T) {
	s, _, _ := newService(t)
	if _, err := s.Authenticate(t.Context(), AuthInput{IP: "10.0.0.1"}); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Authenticate with nothing = %v; want ErrNoCredentials", err)
	}
}

func TestAuthenticateWithAuthDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Security.DisableAuth = true
	s, _, _ := newServiceWith(t, cfg)

	id, err := s.Authenticate(t.Context(), AuthInput{IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Role != store.RoleAdmin || !id.Can(ActionSettingsWrite) {
		t.Errorf("disable_auth identity = %s; want a full administrator", id)
	}
}

func TestDisabledAccountLosesItsSession(t *testing.T) {
	s, st, _ := newService(t)
	u := addUser(t, st, "alice", store.RoleAdmin, nil)
	addUser(t, st, "root", store.RoleAdmin, nil) // so the last-admin rule allows it
	ctx := t.Context()

	_, token, err := s.Login(ctx, "alice", testPassword, "10.0.0.1", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := s.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatalf("SetUserDisabled: %v", err)
	}
	if _, err := s.Authenticate(ctx, AuthInput{SessionCookie: token}); err == nil {
		t.Fatal("a disabled account kept a working session")
	}
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

func TestAPITokenAuthentication(t *testing.T) {
	s, st, c := newService(t)
	ctx := t.Context()

	expiry := c.Now().Add(24 * time.Hour)
	tok, plaintext, err := s.CreateAPIToken(ctx, NewToken{
		Name: "ci", Role: store.RoleOperator, ExpiresAt: &expiry,
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, APITokenPrefix) || !strings.HasPrefix(plaintext, tok.Prefix+"_") {
		t.Errorf("token %q does not have the documented zoo_<id>_<secret> shape (prefix %q)", plaintext, tok.Prefix)
	}
	if got := len(strings.TrimPrefix(plaintext, tok.Prefix+"_")); got != 32 {
		t.Errorf("secret part is %d characters; want 32", got)
	}
	if strings.Contains(tok.TokenHash, plaintext) || tok.TokenHash != cryptox.HashToken(plaintext) {
		t.Error("the stored hash is not the SHA-256 of the plaintext")
	}

	id, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext, IP: "10.0.0.3"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Kind != KindToken || id.TokenID != tok.ID || id.Role != store.RoleOperator {
		t.Errorf("identity = %+v; want the operator token %s", id, tok.ID)
	}

	// A bearer token wins over a session cookie on the same request.
	addUser(t, st, "alice", store.RoleViewer, nil)
	_, cookie, err := s.Login(ctx, "alice", testPassword, "10.0.0.3", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	id, err = s.Authenticate(ctx, AuthInput{Authorization: "bearer " + plaintext, SessionCookie: cookie})
	if err != nil || id.Kind != KindToken {
		t.Fatalf("Authenticate with both = %+v, %v; want the token identity", id, err)
	}

	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer zoo_nope_nope"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown token = %v; want ErrInvalidCredentials", err)
	}

	c.Advance(48 * time.Hour)
	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext}); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expired token = %v; want ErrTokenExpired", err)
	}
}

func TestAPITokenRevoked(t *testing.T) {
	s, _, _ := newService(t)
	ctx := t.Context()

	tok, plaintext, err := s.CreateAPIToken(ctx, NewToken{Name: "ci", Role: store.RoleViewer})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext}); err != nil {
		t.Fatalf("Authenticate before revoking: %v", err)
	}
	if err := s.RevokeAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext}); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("revoked token = %v; want ErrTokenRevoked", err)
	}
}

func TestAPITokenRejectsUnknownScopes(t *testing.T) {
	s, _, _ := newService(t)
	_, _, err := s.CreateAPIToken(t.Context(), NewToken{
		Name: "ci", Role: store.RoleOperator, Scopes: []string{"pools:read", "poolz:write"},
	})
	if err == nil || !strings.Contains(err.Error(), "poolz:write") {
		t.Fatalf("CreateAPIToken with a typo = %v; want an error naming the bad scope", err)
	}
}

func TestAPITokenLastUsedIsWrittenAtMostOncePerMinute(t *testing.T) {
	s, st, c := newService(t)
	ctx := t.Context()

	_, plaintext, err := s.CreateAPIToken(ctx, NewToken{Name: "ci", Role: store.RoleViewer})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	lastUsed := func() *time.Time {
		toks, err := st.ListAPITokens(ctx)
		if err != nil || len(toks) != 1 {
			t.Fatalf("ListAPITokens = %v, %v", toks, err)
		}
		return toks[0].LastUsedAt
	}

	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext}); err != nil {
		t.Fatal(err)
	}
	first := lastUsed()
	if first == nil {
		t.Fatal("last_used_at was not recorded on first use")
	}

	c.Advance(10 * time.Second)
	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext}); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(*first) {
		t.Errorf("last_used_at moved to %v after 10s; it should be written at most once a minute", got)
	}

	c.Advance(2 * time.Minute)
	if _, err := s.Authenticate(ctx, AuthInput{Authorization: "Bearer " + plaintext}); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); got.Equal(*first) {
		t.Error("last_used_at was not refreshed after the interval elapsed")
	}
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func TestLastAdminInvariant(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()
	admin := addUser(t, st, "root", store.RoleAdmin, nil)
	viewer := addUser(t, st, "eve", store.RoleViewer, nil)

	demote := *admin
	demote.Role = store.RoleViewer
	if err := s.UpdateUser(ctx, &demote); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demoting the last admin = %v; want ErrLastAdmin", err)
	}
	if err := s.SetUserDisabled(ctx, admin.ID, true); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the last admin = %v; want ErrLastAdmin", err)
	}
	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the last admin = %v; want ErrLastAdmin", err)
	}
	if !strings.Contains(ErrLastAdmin.Error(), "admin role") {
		t.Errorf("the last-admin message should say what to do: %q", ErrLastAdmin)
	}

	// Removing a non-admin is fine, and so is removing an admin once there is
	// a second one.
	if err := s.DeleteUser(ctx, viewer.ID); err != nil {
		t.Errorf("deleting a viewer: %v", err)
	}
	second := addUser(t, st, "root2", store.RoleAdmin, nil)
	if err := s.DeleteUser(ctx, admin.ID); err != nil {
		t.Errorf("deleting an admin with another one present: %v", err)
	}
	if err := s.SetUserDisabled(ctx, second.ID, true); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the now-last admin = %v; want ErrLastAdmin", err)
	}
}

func TestLastAdminInvariantIgnoresDisabledAdmins(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()
	admin := addUser(t, st, "root", store.RoleAdmin, nil)
	addUser(t, st, "onleave", store.RoleAdmin, func(u *store.User) { u.Disabled = true })

	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the last *enabled* admin = %v; want ErrLastAdmin", err)
	}
}

func TestCreateUserValidation(t *testing.T) {
	s, _, _ := newService(t)
	ctx := t.Context()

	if _, err := s.CreateUser(ctx, NewUser{Username: "bad user", Password: testPassword}); err == nil {
		t.Error("a username with a space was accepted")
	}
	// An account made ahead of its owner's first single sign-on has no
	// password and, until that sign-in links it, no subject either.
	if sso, err := s.CreateUser(ctx, NewUser{Username: "sso-only", SSOOnly: true}); err != nil {
		t.Fatalf("CreateUser(SSOOnly): %v", err)
	} else if sso.PasswordHash != "" || sso.OIDCSubject != "" {
		t.Fatalf("SSO-only user = %+v, want no password hash and no subject yet", sso)
	}
	if _, _, err := s.Login(ctx, "sso-only", testPassword, "10.0.0.1", "test"); !errors.Is(err, ErrSSOOnly) {
		t.Fatalf("Login(sso-only) = %v, want ErrSSOOnly", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "nopass"}); err == nil {
		t.Error("an account with neither a password nor an OIDC subject was accepted")
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "bob", Password: testPassword, Role: "wizard"}); err == nil {
		t.Error("an unknown role was accepted")
	}
	u, err := s.CreateUser(ctx, NewUser{Username: "Bob", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Role != store.RoleViewer {
		t.Errorf("default role = %q; want viewer", u.Role)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "bob", Password: testPassword}); err == nil {
		t.Error("a duplicate username was accepted")
	}
}

// ---------------------------------------------------------------------------
// Passwords
// ---------------------------------------------------------------------------

func TestChangePassword(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()
	u := addUser(t, st, "alice", store.RoleViewer, nil)

	_, cookie, err := s.Login(ctx, "alice", testPassword, "10.0.0.1", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := s.ChangePassword(ctx, u.ID, "wrong", "a much longer password"); err == nil {
		t.Error("the wrong current password was accepted")
	}
	if err := s.ChangePassword(ctx, u.ID, testPassword, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short new password = %v; want ErrPasswordTooShort", err)
	}
	if err := s.ChangePassword(ctx, u.ID, testPassword, "a much longer password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Changing a password ends every session for that account.
	if _, err := s.Authenticate(ctx, AuthInput{SessionCookie: cookie}); err == nil {
		t.Error("the old session cookie still works after a password change")
	}
	if _, _, err := s.Login(ctx, "alice", "a much longer password", "10.0.0.1", ""); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
}

func TestChangePasswordSkipsTheOldOneWhenAChangeIsForced(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()
	u := addUser(t, st, "installer", store.RoleAdmin, func(u *store.User) { u.MustChangePassword = true })

	if err := s.ChangePassword(ctx, u.ID, "", "a much longer password"); err != nil {
		t.Fatalf("ChangePassword on a must-change account: %v", err)
	}
	after, err := st.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.MustChangePassword {
		t.Error("must_change_password is still set after the change")
	}
}

func TestResetPasswordForcesAChange(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()
	u := addUser(t, st, "alice", store.RoleViewer, nil)

	if err := s.ResetPassword(ctx, u.ID, "a much longer password"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	after, err := st.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.MustChangePassword {
		t.Error("an administrative reset should force a change at next login")
	}
	if _, _, err := s.Login(ctx, "alice", "a much longer password", "10.0.0.1", ""); err != nil {
		t.Errorf("login after reset: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Join tokens and agents
// ---------------------------------------------------------------------------

func TestJoinTokenIsSingleUseAndExpires(t *testing.T) {
	s, st, c := newService(t)
	ctx := t.Context()

	jt, plaintext, err := s.CreateJoinToken(ctx, time.Hour, map[string]string{"zone": "eu"}, 4, "usr_1")
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, JoinTokenPrefix) || jt.Prefix == "" {
		t.Errorf("join token %q does not carry the zoojoin_ prefix", plaintext)
	}

	h := &store.Host{Name: "host-a", Capacity: 4}
	if err := st.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemJoinToken(ctx, plaintext, h.ID); err != nil {
		t.Fatalf("RedeemJoinToken: %v", err)
	}
	if _, err := s.RedeemJoinToken(ctx, plaintext, h.ID); err == nil {
		t.Error("a join token was redeemed twice")
	}

	_, second, err := s.CreateJoinToken(ctx, time.Minute, nil, 0, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(2 * time.Minute)
	if _, err := s.RedeemJoinToken(ctx, second, h.ID); err == nil {
		t.Error("an expired join token was accepted")
	}
}

func TestAgentTokenAuthenticates(t *testing.T) {
	s, st, _ := newService(t)
	ctx := t.Context()

	plaintext, hash := NewAgentToken()
	h := &store.Host{Name: "host-a", Capacity: 2, TokenHash: hash}
	if err := st.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, err := s.AuthenticateAgent(ctx, "Bearer "+plaintext)
	if err != nil {
		t.Fatalf("AuthenticateAgent: %v", err)
	}
	if got.ID != h.ID {
		t.Errorf("host = %s; want %s", got.ID, h.ID)
	}
	if _, err := s.AuthenticateAgent(ctx, "Bearer zooagent_nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown agent token = %v; want ErrInvalidCredentials", err)
	}
	if _, err := s.AuthenticateAgent(ctx, ""); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("missing agent token = %v; want ErrNoCredentials", err)
	}
}

func TestBearerTokenParsing(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":   "abc",
		"bearer  abc ": "abc",
		"BEARER abc":   "abc",
		"Basic abc":    "",
		"abc":          "",
		"":             "",
	}
	for header, want := range cases {
		if got := bearerToken(header); got != want {
			t.Errorf("bearerToken(%q) = %q; want %q", header, got, want)
		}
	}
}

func TestIdentityString(t *testing.T) {
	var nilID *Identity
	if got := nilID.String(); got != "unauthenticated" {
		t.Errorf("nil identity = %q", got)
	}
	id := &Identity{Kind: KindToken, Name: "ci", Role: store.RoleOperator, Scopes: []string{"pools:read"}}
	if got := id.String(); !strings.Contains(got, "ci") || !strings.Contains(got, "operator") || !strings.Contains(got, "pools:read") {
		t.Errorf("identity string = %q; want it to name the token, its role and its scopes", got)
	}
}

// Every refusal this package makes for a reason the caller can act on is
// ErrInvalidInput, and a name that is taken is store.ErrConflict, so the API
// can answer them with the message and answer everything else -- a database
// that is not there -- with a request ID instead of quoting it.
func TestRefusalsSayWhatKindTheyAre(t *testing.T) {
	s, _, _ := newService(t)
	ctx := t.Context()

	if _, err := s.CreateUser(ctx, NewUser{Username: "no spaces here", Password: "correct-horse-battery"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a bad username = %v; want ErrInvalidInput", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "sam", Password: "correct-horse-battery", Role: "owner"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a bad role = %v; want ErrInvalidInput", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "sam", Password: "short"}); !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("a short password = %v; want both ErrPasswordTooShort and ErrInvalidInput", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "sam", Password: "correct-horse-battery"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, err := s.CreateUser(ctx, NewUser{Username: "sam", Password: "correct-horse-battery"})
	if !errors.Is(err, store.ErrConflict) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a taken name = %v; want store.ErrConflict and not ErrInvalidInput", err)
	}
	if err == nil || !strings.Contains(err.Error(), `an account named "sam" already exists`) {
		t.Fatalf("the conflict does not read as a sentence: %v", err)
	}

	if _, _, err := s.CreateAPIToken(ctx, NewToken{Name: "", Role: store.RoleViewer}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a nameless token = %v; want ErrInvalidInput", err)
	}
	if _, _, err := s.CreateAPIToken(ctx, NewToken{Name: "ci", Scopes: []string{"pools:fly"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unknown scope = %v; want ErrInvalidInput", err)
	}
	if _, err := s.RedeemJoinToken(ctx, "zjt_not_a_real_token", "host_x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a bad join token = %v; want ErrInvalidInput", err)
	}
}
