package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

const testUA = "Mozilla/5.0 (test)"

// newTwoStepService is newService with the instance key two-step seals its
// secrets with, and the configuration in the test's hands.
func newTwoStepService(t *testing.T, mutate func(*config.Config)) (*Service, *store.Store, *clock) {
	t.Helper()
	cfg := config.Default()
	cfg.Security.RateLimitLogins = 0
	cfg.Security.SessionTTL = time.Hour
	if mutate != nil {
		mutate(cfg)
	}
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	st, err := store.Open(t.Context(), store.Options{Path: ":memory:", Now: c.Now})
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, cfg, events.New(), WithClock(c.Now), WithKey(key)), st, c
}

// currentCode is what the person's phone shows right now.
func currentCode(t *testing.T, secret string, c *clock) string {
	t.Helper()
	code, err := totpCode(secret, totpStep(c.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// enrol turns two-step on for u and returns the secret and recovery codes,
// then moves the clock on a step so the confirming code is not the next one.
func enrol(t *testing.T, s *Service, u *store.User, c *clock) (string, []string) {
	t.Helper()
	setup, err := s.BeginTwoStep(t.Context(), u.ID, "ci.example.com")
	if err != nil {
		t.Fatalf("BeginTwoStep: %v", err)
	}
	codes, err := s.ConfirmTwoStep(t.Context(), u.ID, currentCode(t, setup.Secret, c), "")
	if err != nil {
		t.Fatalf("ConfirmTwoStep: %v", err)
	}
	c.Advance(totpPeriod)
	return setup.Secret, codes
}

// signInPassword runs the first step and returns the pending sign-in.
func signInPassword(t *testing.T, s *Service, username string) *SecondStepRequired {
	t.Helper()
	_, token, err := s.Login(t.Context(), username, testPassword, "192.0.2.1", testUA)
	var pending *SecondStepRequired
	if !errors.As(err, &pending) {
		t.Fatalf("Login = token %q, %v; want a second step", token, err)
	}
	if token != "" {
		t.Fatal("a session was minted before the second step")
	}
	return pending
}

func TestAnAccountWithoutTwoStepSignsInExactlyAsBefore(t *testing.T) {
	s, st, _ := newTwoStepService(t, nil)
	addUser(t, st, "ada", store.RoleOperator, nil)
	// An enrolment started and abandoned asks nothing at sign-in.
	u, _ := st.GetUserByUsername(t.Context(), "ada")
	if _, err := s.BeginTwoStep(t.Context(), u.ID, "ada"); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(t.Context(), "ada", testPassword, "192.0.2.1", testUA)
	if err != nil || token == "" {
		t.Fatalf("Login = %q, %v; want a session", token, err)
	}
}

func TestTheCodeStepFinishesTheSignInAndCannotBeSkipped(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	secret, _ := enrol(t, s, u, c)

	pending := signInPassword(t, s, "ada")
	if pending.Purpose != store.ChallengeVerify || pending.Challenge == "" {
		t.Fatalf("pending = %+v", pending)
	}
	if !pending.ExpiresAt.Equal(c.Now().Add(signInChallengeTTL)) {
		t.Errorf("the pending sign-in expires at %v, want five minutes from now", pending.ExpiresAt)
	}

	// The challenge is not a session: presented as a cookie, it is nobody.
	if _, err := s.Authenticate(t.Context(), AuthInput{SessionCookie: pending.Challenge}); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("the challenge authenticated as a session: %v", err)
	}
	// Nor is it an enrolment, which would let somebody with only a password
	// replace the account's authenticator.
	if _, err := s.EnrolDuringSignIn(t.Context(), pending.Challenge, "ada", testUA); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("a verify challenge started an enrolment: %v", err)
	}
	// Nor can it be finished from another browser.
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, currentCode(t, secret, c), "192.0.2.1", "curl/8"); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("a challenge was finished with another user agent: %v", err)
	}

	in, err := s.VerifySignIn(t.Context(), pending.Challenge, currentCode(t, secret, c), "192.0.2.1", testUA)
	if err != nil {
		t.Fatalf("VerifySignIn: %v", err)
	}
	if in.Session == "" || in.User.ID != u.ID || in.RecoveryCodeUsed {
		t.Fatalf("sign-in = %+v", in)
	}
	if id, err := s.Authenticate(t.Context(), AuthInput{SessionCookie: in.Session}); err != nil || id.ID != u.ID {
		t.Fatalf("the new session does not authenticate: %v", err)
	}
	// A finished challenge is spent.
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, currentCode(t, secret, c), "192.0.2.1", testUA); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("a finished challenge was used again: %v", err)
	}
}

// A code seen once -- over a shoulder, or relayed by a phishing page -- is
// worth nothing the second time, even inside its thirty seconds.
func TestACodeIsRefusedTheSecondTime(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	secret, _ := enrol(t, s, u, c)
	code := currentCode(t, secret, c)

	if _, err := s.VerifySignIn(t.Context(), signInPassword(t, s, "ada").Challenge, code, "", testUA); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := s.VerifySignIn(t.Context(), signInPassword(t, s, "ada").Challenge, code, "", testUA); !errors.Is(err, ErrInvalidTwoStepCode) {
		t.Fatalf("second use = %v, want ErrInvalidTwoStepCode", err)
	}
	// And the confirming code of an enrolment counts as used.
	s2, st2, c2 := newTwoStepService(t, nil)
	u2 := addUser(t, st2, "grace", store.RoleOperator, nil)
	setup, _ := s2.BeginTwoStep(t.Context(), u2.ID, "grace")
	first := currentCode(t, setup.Secret, c2)
	if _, err := s2.ConfirmTwoStep(t.Context(), u2.ID, first, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.VerifySignIn(t.Context(), signInPassword(t, s2, "grace").Challenge, first, "", testUA); !errors.Is(err, ErrInvalidTwoStepCode) {
		t.Fatalf("the enrolment's code signed in = %v", err)
	}
}

func TestAPendingSignInExpiresAfterFiveMinutes(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	secret, _ := enrol(t, s, u, c)
	pending := signInPassword(t, s, "ada")

	c.Advance(signInChallengeTTL)
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, currentCode(t, secret, c), "", testUA); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("an expired sign-in = %v, want ErrSignInExpired", err)
	}
}

func TestWrongCodesEndThePendingSignIn(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	secret, _ := enrol(t, s, u, c)
	pending := signInPassword(t, s, "ada")

	for i := 1; i < maxChallengeFailures; i++ {
		if _, err := s.VerifySignIn(t.Context(), pending.Challenge, "000000", "", testUA); !errors.Is(err, ErrInvalidTwoStepCode) {
			t.Fatalf("wrong code %d = %v, want ErrInvalidTwoStepCode", i, err)
		}
	}
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, "000000", "", testUA); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("the last wrong code = %v, want the sign-in ended", err)
	}
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, currentCode(t, secret, c), "", testUA); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("the right code after the sign-in ended = %v", err)
	}
}

// A code is no cheaper to guess than the password in front of it: every
// attempt is charged to the same per-address counter, and a success is what
// clears it.
func TestCodeFailuresCountTowardTheSignInLimit(t *testing.T) {
	s, st, c := newTwoStepService(t, func(cfg *config.Config) { cfg.Security.RateLimitLogins = 3 })
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	secret, _ := enrol(t, s, u, c)

	pending := signInPassword(t, s, "ada") // attempt 1
	for i := 0; i < 2; i++ {               // attempts 2 and 3
		if _, err := s.VerifySignIn(t.Context(), pending.Challenge, "000000", "192.0.2.1", testUA); !errors.Is(err, ErrInvalidTwoStepCode) {
			t.Fatalf("wrong code = %v", err)
		}
	}
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, currentCode(t, secret, c), "192.0.2.1", testUA); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("a fourth attempt inside the minute = %v, want ErrRateLimited", err)
	}
	if _, _, err := s.Login(t.Context(), "ada", testPassword, "192.0.2.1", testUA); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the password after the codes = %v, want ErrRateLimited", err)
	}
}

func TestARecoveryCodeSignsInOnce(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	_, codes := enrol(t, s, u, c)
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("%d recovery codes, want %d", len(codes), RecoveryCodeCount)
	}

	in, err := s.VerifySignIn(t.Context(), signInPassword(t, s, "ada").Challenge, codes[3], "", testUA)
	if err != nil {
		t.Fatalf("a recovery code: %v", err)
	}
	if !in.RecoveryCodeUsed || in.RecoveryCodesLeft != RecoveryCodeCount-1 {
		t.Fatalf("sign-in = used %v, left %d", in.RecoveryCodeUsed, in.RecoveryCodesLeft)
	}
	if _, err := s.VerifySignIn(t.Context(), signInPassword(t, s, "ada").Challenge, codes[3], "", testUA); !errors.Is(err, ErrInvalidTwoStepCode) {
		t.Fatalf("the same recovery code again = %v", err)
	}
	// Typed in capitals with a space instead of the hyphen, another one still works.
	typed := codes[4][:5] + " " + codes[4][6:]
	if _, err := s.VerifySignIn(t.Context(), signInPassword(t, s, "ada").Challenge, typed, "", testUA); err != nil {
		t.Fatalf("a recovery code typed loosely: %v", err)
	}
}

func TestRequiringTwoStepMakesAnAccountEnrolBeforeItGetsASession(t *testing.T) {
	s, st, c := newTwoStepService(t, func(cfg *config.Config) { cfg.Security.RequireTwoStep = true })
	u := addUser(t, st, "ada", store.RoleOperator, nil)

	pending := signInPassword(t, s, "ada")
	if pending.Purpose != store.ChallengeEnrol {
		t.Fatalf("purpose = %q, want enrol", pending.Purpose)
	}
	// An enrolment challenge cannot be finished with a code: there is no
	// authenticator yet, and the step is setting one up.
	if _, err := s.VerifySignIn(t.Context(), pending.Challenge, "000000", "", testUA); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("verify on an enrolment challenge = %v", err)
	}
	setup, err := s.EnrolDuringSignIn(t.Context(), pending.Challenge, "ada", testUA)
	if err != nil {
		t.Fatalf("EnrolDuringSignIn: %v", err)
	}
	if setup.Secret == "" || setup.QRSVG == "" {
		t.Fatalf("setup = %+v", setup)
	}
	if _, err := s.ConfirmDuringSignIn(t.Context(), pending.Challenge, "000000", "", testUA); !errors.Is(err, ErrInvalidTwoStepCode) {
		t.Fatalf("a wrong first code = %v", err)
	}
	in, err := s.ConfirmDuringSignIn(t.Context(), pending.Challenge, currentCode(t, setup.Secret, c), "", testUA)
	if err != nil {
		t.Fatalf("ConfirmDuringSignIn: %v", err)
	}
	if in.Session == "" || len(in.RecoveryCodes) != RecoveryCodeCount {
		t.Fatalf("sign-in = session %q, %d codes", in.Session, len(in.RecoveryCodes))
	}
	status, err := s.TwoStepStatus(t.Context(), u.ID)
	if err != nil || !status.Enabled || !status.Required {
		t.Fatalf("status = %+v, %v", status, err)
	}
	// The next sign-in asks for a code, not for enrolment.
	c.Advance(totpPeriod)
	if p := signInPassword(t, s, "ada"); p.Purpose != store.ChallengeVerify {
		t.Fatalf("the sign-in after enrolling asked to %s", p.Purpose)
	}
}

// Single sign-on accounts have no password to put a step after, and their
// second factor belongs to the identity provider: requiring two-step here
// must neither lock them out nor offer them an enrolment.
func TestSingleSignOnAccountsAreNotAskedForTwoStep(t *testing.T) {
	s, st, _ := newTwoStepService(t, func(cfg *config.Config) { cfg.Security.RequireTwoStep = true })
	u := addUser(t, st, "sso", store.RoleViewer, func(u *store.User) {
		u.PasswordHash = ""
		u.OIDCSubject = "sub-123"
	})
	status, err := s.TwoStepStatus(t.Context(), u.ID)
	if err != nil || status.Available || status.Enabled {
		t.Fatalf("status = %+v, %v; want unavailable", status, err)
	}
	if _, err := s.BeginTwoStep(t.Context(), u.ID, "sso"); !errors.Is(err, ErrTwoStepUnavailable) {
		t.Fatalf("BeginTwoStep for an SSO account = %v", err)
	}
	// The callback's path into a session is NewSession, which two-step does
	// not stand in front of.
	token, err := s.NewSession(t.Context(), u, "", testUA)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.Authenticate(t.Context(), AuthInput{SessionCookie: token}); err != nil || id.ID != u.ID {
		t.Fatalf("an SSO session does not authenticate with two-step required: %v", err)
	}
}

// Tokens are for automation, which has nobody to type a code; they are
// already a long random secret the caller holds.
func TestAPITokensAreNotAskedForACode(t *testing.T) {
	s, st, c := newTwoStepService(t, func(cfg *config.Config) { cfg.Security.RequireTwoStep = true })
	u := addUser(t, st, "ada", store.RoleAdmin, nil)
	enrol(t, s, u, c)
	_, plain, err := s.CreateAPIToken(t.Context(), NewToken{Name: "ci", Role: store.RoleViewer, UserID: u.ID, OwnerRole: store.RoleAdmin})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, err := s.Authenticate(t.Context(), AuthInput{Authorization: "Bearer " + plain}); err != nil {
		t.Fatalf("a token of an account with two-step on: %v", err)
	}
}

func TestTurningTwoStepOffTakesThePasswordAndACode(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	secret, codes := enrol(t, s, u, c)

	for _, tc := range []struct {
		name     string
		password string
		code     string
		want     error
	}{
		{"wrong password", "not the password at all", currentCode(t, secret, c), ErrWrongPassword},
		{"no code", testPassword, "", ErrInvalidTwoStepCode},
		{"wrong code", testPassword, "000000", ErrInvalidTwoStepCode},
	} {
		if err := s.DisableTwoStep(t.Context(), u.ID, tc.password, tc.code, ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: DisableTwoStep = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := s.RegenerateRecoveryCodes(t.Context(), u.ID, testPassword, "000000", ""); !errors.Is(err, ErrInvalidTwoStepCode) {
		t.Errorf("RegenerateRecoveryCodes with a wrong code = %v", err)
	}
	fresh, err := s.RegenerateRecoveryCodes(t.Context(), u.ID, testPassword, currentCode(t, secret, c), "")
	if err != nil || len(fresh) != RecoveryCodeCount {
		t.Fatalf("RegenerateRecoveryCodes = %d codes, %v", len(fresh), err)
	}
	// The old set died with the new one's issue; a new one works to turn it off.
	if err := s.DisableTwoStep(t.Context(), u.ID, testPassword, codes[0], ""); !errors.Is(err, ErrInvalidTwoStepCode) {
		t.Errorf("an old recovery code = %v", err)
	}
	if err := s.DisableTwoStep(t.Context(), u.ID, testPassword, fresh[0], ""); err != nil {
		t.Fatalf("DisableTwoStep: %v", err)
	}
	if _, token, err := s.Login(t.Context(), "ada", testPassword, "", testUA); err != nil || token == "" {
		t.Fatalf("with two-step off, Login = %q, %v", token, err)
	}
}

func TestTurningTwoStepOnEndsEveryOtherSession(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	here, _ := s.NewSession(t.Context(), u, "", testUA)
	elsewhere, _ := s.NewSession(t.Context(), u, "", testUA)

	setup, err := s.BeginTwoStep(t.Context(), u.ID, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmTwoStep(t.Context(), u.ID, currentCode(t, setup.Secret, c), here); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(t.Context(), AuthInput{SessionCookie: here}); err != nil {
		t.Errorf("the session that turned it on was ended: %v", err)
	}
	if _, err := s.Authenticate(t.Context(), AuthInput{SessionCookie: elsewhere}); err == nil {
		t.Error("a session signed in with the password alone outlived turning two-step on")
	}
	if _, err := s.BeginTwoStep(t.Context(), u.ID, "ada"); !errors.Is(err, ErrTwoStepAlreadyOn) {
		t.Errorf("beginning again over a working enrolment = %v", err)
	}
}

func TestAnAdministratorsResetRemovesTheSecondFactorAndEndsSessions(t *testing.T) {
	s, st, c := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	enrol(t, s, u, c)
	session, _ := s.NewSession(t.Context(), u, "", testUA)

	removed, err := s.ResetTwoStep(t.Context(), u.ID)
	if err != nil || !removed {
		t.Fatalf("ResetTwoStep = %v, %v", removed, err)
	}
	if _, err := s.Authenticate(t.Context(), AuthInput{SessionCookie: session}); err == nil {
		t.Error("a session outlived the reset")
	}
	if _, token, err := s.Login(t.Context(), "ada", testPassword, "", testUA); err != nil || token == "" {
		t.Fatalf("after a reset, Login = %q, %v; want the password alone", token, err)
	}
	if removed, _ := s.ResetTwoStep(t.Context(), u.ID); removed {
		t.Error("a second reset said it removed something")
	}
	if _, err := s.ResetTwoStep(t.Context(), "usr_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("resetting a missing account = %v", err)
	}
}

// A service built without the instance key has nowhere safe to put a
// secret, and says so rather than storing one in the clear.
func TestTwoStepRefusesToStartWithoutTheInstanceKey(t *testing.T) {
	s, st, _ := newService(t)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	if _, err := s.BeginTwoStep(t.Context(), u.ID, "ada"); !errors.Is(err, ErrNoEncryptionKey) {
		t.Fatalf("BeginTwoStep without a key = %v", err)
	}
	if _, err := st.GetTwoStep(t.Context(), u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("something was stored: %v", err)
	}
}

func TestTheSecretIsStoredSealed(t *testing.T) {
	s, st, _ := newTwoStepService(t, nil)
	u := addUser(t, st, "ada", store.RoleOperator, nil)
	setup, err := s.BeginTwoStep(t.Context(), u.ID, "ada")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := st.GetTwoStep(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(ts.SecretEnc) == setup.Secret || len(ts.SecretEnc) <= len(setup.Secret) {
		t.Fatal("the authenticator secret is in the database in the clear")
	}
}
