package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/qr"
	"github.com/eyupio/zoomies/internal/store"
)

// Two-step verification: an authenticator code after the password, for
// accounts that sign in with one.
//
// It is deliberately limited to local password accounts. An account that
// signs in through single sign-on gets its second factor, if it has one, from
// the identity provider, which is where a company's MFA policy already lives;
// asking again here would be a second prompt enforcing nothing new. API
// tokens are untouched too: a token is a long random secret that is already
// something you have, and automation has nobody to type a code.

// signInChallengeTTL is how long a sign-in may wait between the password and
// the code. Long enough to find a phone and open an app; short enough that a
// half-finished sign-in left on a shared machine is not a door left open.
const signInChallengeTTL = 5 * time.Minute

// maxChallengeFailures is how many wrong codes one pending sign-in takes
// before it is thrown away and the password has to be typed again. The rate
// limiters bound guessing overall; this stops one password proof buying an
// unbounded run at the code.
const maxChallengeFailures = 5

// twoStepIssuer is the name an authenticator app files the entry under.
const twoStepIssuer = "Zoomies"

var (
	// ErrInvalidTwoStepCode is a code that is wrong, outside the time window,
	// already used, or a recovery code that was already spent. One sentence
	// for all of them: which one it was is worth nothing to a guesser, and
	// the remedy -- the current code -- is the same.
	ErrInvalidTwoStepCode error = &refusal{kind: ErrInvalidInput,
		msg: "that code is not right, or has already been used; enter the current code from your authenticator app, or one of your recovery codes"}
	// ErrSignInExpired means the pending sign-in is gone: it took too long,
	// took too many wrong codes, or finished already.
	ErrSignInExpired = errors.New("this sign-in has expired; enter your username and password again")
	// ErrTwoStepUnavailable is an account that has no password to put a
	// second step after.
	ErrTwoStepUnavailable error = &refusal{kind: ErrInvalidInput,
		msg: "two-step verification is for accounts that sign in with a password; an account that uses single sign-on gets its second factor from the identity provider"}
	// ErrTwoStepAlreadyOn refuses starting enrolment over a working one.
	ErrTwoStepAlreadyOn error = &refusal{kind: store.ErrConflict,
		msg: "two-step verification is already on for this account; turn it off first to move it to a new device"}
	// ErrTwoStepNotOn refuses an action that needs it on.
	ErrTwoStepNotOn error = &refusal{kind: ErrInvalidInput,
		msg: "two-step verification is not on for this account"}
	// ErrTwoStepNotStarted is a confirmation with no enrolment to confirm.
	ErrTwoStepNotStarted error = &refusal{kind: ErrInvalidInput,
		msg: "no two-step setup is in progress for this account; start again to get a new key"}
	// ErrNoEncryptionKey means the service was built without the instance
	// key, so it has nowhere safe to keep a secret. It is the controller's
	// fault, not the caller's.
	ErrNoEncryptionKey = errors.New("two-step verification needs the instance encryption key, and this service was started without one")
)

// SecondStepRequired is what Login returns in place of a session when the
// password was right and the account has a second step still to take.
//
// It is an error on purpose. Every caller of Login that predates two-step --
// and any written later by somebody who has not read this -- treats it as a
// failed sign-in and mints nothing, so forgetting to handle it fails closed.
type SecondStepRequired struct {
	// Challenge is the plaintext token for the browser's challenge cookie.
	// Only its hash is stored.
	Challenge string
	// Purpose is store.ChallengeVerify or store.ChallengeEnrol.
	Purpose   string
	ExpiresAt time.Time
	User      *store.User
}

func (e *SecondStepRequired) Error() string {
	if e.Purpose == store.ChallengeEnrol {
		return "this instance requires two-step verification; set it up to finish signing in"
	}
	return "enter the code from your authenticator app to finish signing in"
}

// TwoStepStatus is one account's two-step state, as the account page shows it.
type TwoStepStatus struct {
	// Available is false for an account with no password: single sign-on.
	Available bool
	Enabled   bool
	EnabledAt *time.Time
	// RecoveryCodesLeft counts unspent recovery codes.
	RecoveryCodesLeft int
	// Required reports security.require_two_step.
	Required bool
}

// TwoStepSetup is what enrolment shows once: the key, the address an app
// scans, and that address drawn as a QR code.
type TwoStepSetup struct {
	Secret string
	URI    string
	QRSVG  string
}

// SignIn is a finished two-step sign-in.
type SignIn struct {
	User    *store.User
	Session string
	// RecoveryCodeUsed says a recovery code stood in for the app, which the
	// audit row records and the page warns about.
	RecoveryCodeUsed  bool
	RecoveryCodesLeft int
	// RecoveryCodes is set when the sign-in was an enrolment: the codes the
	// person has to write down before they go any further.
	RecoveryCodes []string
}

// WithKey gives the service the instance key it seals authenticator secrets
// with. Without it, two-step verification refuses to start.
func WithKey(k *cryptox.Key) Option {
	return func(s *Service) { s.key = k }
}

// RequireTwoStep reports whether every local account must use two-step.
func (s *Service) RequireTwoStep() bool { return s.cfg.RequireTwoStep }

// secondStep decides, after a correct password, whether the sign-in needs
// another step, and opens a challenge for it when it does.
func (s *Service) secondStep(ctx context.Context, u *store.User, ip, ua string) (*SecondStepRequired, error) {
	purpose := ""
	ts, err := s.store.GetTwoStep(ctx, u.ID)
	switch {
	case err == nil && ts.Enabled():
		purpose = store.ChallengeVerify
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("reading two-step state: %w", err)
	case s.cfg.RequireTwoStep:
		purpose = store.ChallengeEnrol
	default:
		return nil, nil
	}
	token := store.NewSecret(32)
	c := &store.SignInChallenge{
		TokenHash: cryptox.HashToken(token),
		UserID:    u.ID,
		Purpose:   purpose,
		IP:        ip,
		UserAgent: truncate(ua, 512),
		ExpiresAt: s.Now().Add(signInChallengeTTL),
	}
	if err := s.store.CreateSignInChallenge(ctx, c); err != nil {
		return nil, fmt.Errorf("starting the second step: %w", err)
	}
	return &SecondStepRequired{Challenge: token, Purpose: purpose, ExpiresAt: c.ExpiresAt, User: u}, nil
}

// challenge resolves a pending sign-in and the account behind it, and
// refuses one that has expired, is for the other purpose, or is being
// finished from a different browser than the one that typed the password.
func (s *Service) challenge(ctx context.Context, token, purpose, ua string) (*store.SignInChallenge, *store.User, error) {
	if token == "" {
		return nil, nil, ErrSignInExpired
	}
	c, err := s.store.GetSignInChallenge(ctx, cryptox.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrSignInExpired
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the pending sign-in: %w", err)
	}
	if !s.Now().Before(c.ExpiresAt) {
		_ = s.store.DeleteSignInChallenge(ctx, c.TokenHash)
		return nil, nil, ErrSignInExpired
	}
	// The cookie is the binding; the user agent is a second, cheap one, so a
	// challenge cookie lifted into another browser is not enough on its own.
	if c.Purpose != purpose || c.UserAgent != truncate(ua, 512) {
		return nil, nil, ErrSignInExpired
	}
	u, err := s.store.GetUser(ctx, c.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrSignInExpired
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the account: %w", err)
	}
	if u.Disabled {
		return nil, nil, ErrAccountDisabled
	}
	return c, u, nil
}

// allowAttempt charges one attempt at a second factor to the same limiters a
// password attempt is charged to, so a code is no cheaper to guess than the
// password in front of it.
func (s *Service) allowAttempt(ip, username string) bool {
	if ip != "" && !s.logins.Allow(ip) {
		s.logger.Warn("two-step rate limit hit", "ip", ip, "username", username)
		return false
	}
	if account := normalizeForLimiter(username); account != "" && !s.accountLogins.Allow(account) {
		s.logger.Warn("two-step rate limit hit for an account", "ip", ip, "username", username)
		return false
	}
	return true
}

// failChallenge counts a wrong code against the pending sign-in, and ends it
// once it has had its share.
// The refusal is audited here, because only here is the account known: the
// handler holds nothing but a cookie.
func (s *Service) failChallenge(ctx context.Context, c *store.SignInChallenge, u *store.User, ip string) error {
	s.audit.Auth(ctx, &Identity{Kind: KindUser, ID: u.ID, Name: u.Username, IP: ip}, "auth.two_step_failed",
		map[string]any{"username": u.Username, "step": c.Purpose})
	n, err := s.store.FailSignInChallenge(ctx, c.TokenHash)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("could not count a wrong two-step code", "error", err)
	}
	if n >= maxChallengeFailures || errors.Is(err, store.ErrNotFound) {
		_ = s.store.DeleteSignInChallenge(ctx, c.TokenHash)
		return ErrSignInExpired
	}
	return ErrInvalidTwoStepCode
}

// finish mints the session a completed sign-in has earned, and forgives the
// attempts that led to it, exactly as a password-only sign-in does.
func (s *Service) finish(ctx context.Context, c *store.SignInChallenge, u *store.User, ip, ua string) (string, error) {
	if err := s.store.DeleteSignInChallenge(ctx, c.TokenHash); err != nil {
		return "", fmt.Errorf("closing the pending sign-in: %w", err)
	}
	token, err := s.NewSession(ctx, u, ip, ua)
	if err != nil {
		return "", err
	}
	now := s.Now()
	if err := s.store.TouchLogin(ctx, u.ID, now); err != nil {
		s.logger.Warn("could not record last login", "user", u.Username, "error", err)
	}
	u.LastLoginAt = &now
	s.logins.Reset(ip)
	s.accountLogins.Reset(normalizeForLimiter(u.Username))
	return token, nil
}

// VerifySignIn finishes a sign-in with a code from the authenticator app or
// a recovery code.
func (s *Service) VerifySignIn(ctx context.Context, challenge, code, ip, ua string) (*SignIn, error) {
	c, u, err := s.challenge(ctx, challenge, store.ChallengeVerify, ua)
	if err != nil {
		return nil, err
	}
	if !s.allowAttempt(ip, u.Username) {
		return nil, ErrRateLimited
	}
	usedRecovery, err := s.checkSecondFactor(ctx, u.ID, code)
	if errors.Is(err, ErrInvalidTwoStepCode) {
		return nil, s.failChallenge(ctx, c, u, ip)
	}
	if err != nil {
		return nil, err
	}
	token, err := s.finish(ctx, c, u, ip, ua)
	if err != nil {
		return nil, err
	}
	out := &SignIn{User: u, Session: token, RecoveryCodeUsed: usedRecovery}
	if usedRecovery {
		out.RecoveryCodesLeft, _ = s.store.RemainingRecoveryCodes(ctx, u.ID)
	}
	return out, nil
}

// checkSecondFactor accepts a current authenticator code, once, or an
// unspent recovery code, once. It reports which it was.
func (s *Service) checkSecondFactor(ctx context.Context, userID, code string) (recovery bool, err error) {
	ts, err := s.store.GetTwoStep(ctx, userID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !ts.Enabled()) {
		return false, ErrTwoStepNotOn
	}
	if err != nil {
		return false, fmt.Errorf("reading two-step state: %w", err)
	}
	if looksLikeRecoveryCode(code) {
		ok, err := s.store.UseRecoveryCode(ctx, userID, cryptox.HashToken(normaliseCode(code)))
		if err != nil {
			return false, fmt.Errorf("checking a recovery code: %w", err)
		}
		if !ok {
			return false, ErrInvalidTwoStepCode
		}
		return true, nil
	}
	secret, err := s.openSecret(ts.SecretEnc)
	if err != nil {
		return false, err
	}
	step, ok := matchTOTP(secret, code, s.Now())
	if !ok {
		return false, ErrInvalidTwoStepCode
	}
	// The replay guard: a code for a step at or before the last one
	// accepted is refused, even though it is arithmetically right.
	fresh, err := s.store.AcceptTwoStepStep(ctx, userID, step)
	if err != nil {
		return false, fmt.Errorf("recording the code: %w", err)
	}
	if !fresh {
		return false, ErrInvalidTwoStepCode
	}
	return false, nil
}

func (s *Service) sealSecret(secret string) ([]byte, error) {
	if s.key == nil {
		return nil, ErrNoEncryptionKey
	}
	return s.key.SealString(secret)
}

func (s *Service) openSecret(sealed []byte) (string, error) {
	if s.key == nil {
		return "", ErrNoEncryptionKey
	}
	secret, err := s.key.OpenString(sealed)
	if err != nil {
		return "", fmt.Errorf("opening the authenticator secret (was the encryption key changed?): %w", err)
	}
	return secret, nil
}

// TwoStepStatus reports an account's two-step state.
func (s *Service) TwoStepStatus(ctx context.Context, userID string) (*TwoStepStatus, error) {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &TwoStepStatus{Available: u.PasswordHash != "", Required: s.cfg.RequireTwoStep}
	ts, err := s.store.GetTwoStep(ctx, userID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return out, nil
	case err != nil:
		return nil, err
	}
	out.Enabled, out.EnabledAt = ts.Enabled(), ts.EnabledAt
	if out.Enabled {
		if out.RecoveryCodesLeft, err = s.store.RemainingRecoveryCodes(ctx, userID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// BeginTwoStep mints a new secret for an account and stores it, unconfirmed.
// host is the address the instance was reached on; the authenticator app
// lists the entry as the account and that address, so two instances do not
// produce two identical entries.
func (s *Service) BeginTwoStep(ctx context.Context, userID, host string) (*TwoStepSetup, error) {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.PasswordHash == "" {
		return nil, ErrTwoStepUnavailable
	}
	secret := newTOTPSecret()
	sealed, err := s.sealSecret(secret)
	if err != nil {
		return nil, err
	}
	if err := s.store.BeginTwoStep(ctx, u.ID, sealed); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, ErrTwoStepAlreadyOn
		}
		return nil, fmt.Errorf("storing the new secret: %w", err)
	}
	label := u.Username
	if host != "" {
		label += " (" + host + ")"
	}
	uri := totpURI(secret, twoStepIssuer, label)
	code, err := qr.Encode([]byte(uri))
	if err != nil {
		return nil, fmt.Errorf("drawing the QR code: %w", err)
	}
	// Dark on white in both themes: a scanner wants contrast, and a code
	// drawn light-on-dark does not scan in every app.
	return &TwoStepSetup{Secret: secret, URI: uri, QRSVG: code.SVG("#000000", "#ffffff")}, nil
}

// ConfirmTwoStep checks the first code from the new authenticator, turns
// two-step on, and returns the recovery codes -- the only time they exist in
// plaintext. Every other session for the account is ended: whoever holds one
// signed in with less than the account now asks for.
func (s *Service) ConfirmTwoStep(ctx context.Context, userID, code, keepSession, ip string) ([]string, error) {
	// Charged like every other code attempt. The secret being guessed is the
	// caller's own, so this guards less than a sign-in does, but a code that
	// is free to guess on one route is a limiter with a hole in it.
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !s.allowAttempt(ip, u.Username) {
		return nil, ErrRateLimited
	}
	return s.confirmTwoStep(ctx, userID, code, keepSession)
}

// confirmTwoStep is ConfirmTwoStep for a caller that has already charged the
// attempt.
func (s *Service) confirmTwoStep(ctx context.Context, userID, code, keepSession string) ([]string, error) {
	ts, err := s.store.GetTwoStep(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrTwoStepNotStarted
	}
	if err != nil {
		return nil, err
	}
	if ts.Enabled() {
		return nil, ErrTwoStepAlreadyOn
	}
	secret, err := s.openSecret(ts.SecretEnc)
	if err != nil {
		return nil, err
	}
	step, ok := matchTOTP(secret, code, s.Now())
	if !ok {
		return nil, ErrInvalidTwoStepCode
	}
	codes := newRecoveryCodes()
	if err := s.store.ConfirmTwoStep(ctx, userID, step, hashCodes(codes)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, ErrTwoStepAlreadyOn
		}
		return nil, fmt.Errorf("turning two-step on: %w", err)
	}
	s.endOtherSessions(ctx, userID, keepSession)
	return codes, nil
}

// EnrolDuringSignIn is BeginTwoStep for somebody who is not signed in yet:
// the instance requires two-step, their password was right, and this is the
// setup they have to finish before they get a session.
func (s *Service) EnrolDuringSignIn(ctx context.Context, challenge, host, ua string) (*TwoStepSetup, error) {
	_, u, err := s.challenge(ctx, challenge, store.ChallengeEnrol, ua)
	if err != nil {
		return nil, err
	}
	return s.BeginTwoStep(ctx, u.ID, host)
}

// ConfirmDuringSignIn finishes an enrolment the sign-in required, and signs
// the person in.
func (s *Service) ConfirmDuringSignIn(ctx context.Context, challenge, code, ip, ua string) (*SignIn, error) {
	c, u, err := s.challenge(ctx, challenge, store.ChallengeEnrol, ua)
	if err != nil {
		return nil, err
	}
	if !s.allowAttempt(ip, u.Username) {
		return nil, ErrRateLimited
	}
	codes, err := s.confirmTwoStep(ctx, u.ID, code, "")
	if errors.Is(err, ErrInvalidTwoStepCode) {
		return nil, s.failChallenge(ctx, c, u, ip)
	}
	if err != nil {
		return nil, err
	}
	token, err := s.finish(ctx, c, u, ip, ua)
	if err != nil {
		return nil, err
	}
	return &SignIn{User: u, Session: token, RecoveryCodes: codes, RecoveryCodesLeft: len(codes)}, nil
}

// reauthenticate is the proof DisableTwoStep and RegenerateRecoveryCodes ask
// for: the password and a current code. A stolen session cookie is neither,
// so it cannot take the second factor off the account it stole.
func (s *Service) reauthenticate(ctx context.Context, userID, password, code, ip string) (*store.User, error) {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.PasswordHash == "" {
		return nil, ErrTwoStepUnavailable
	}
	if !s.allowAttempt(ip, u.Username) {
		return nil, ErrRateLimited
	}
	if !cryptox.VerifyPassword(password, u.PasswordHash) {
		return nil, ErrWrongPassword
	}
	if _, err := s.checkSecondFactor(ctx, u.ID, code); err != nil {
		return nil, err
	}
	return u, nil
}

// DisableTwoStep turns two-step off for the caller's own account.
func (s *Service) DisableTwoStep(ctx context.Context, userID, password, code, ip string) error {
	if _, err := s.reauthenticate(ctx, userID, password, code, ip); err != nil {
		return err
	}
	if _, err := s.store.DeleteTwoStep(ctx, userID); err != nil {
		return fmt.Errorf("turning two-step off: %w", err)
	}
	return nil
}

// RegenerateRecoveryCodes replaces every recovery code the account has, and
// returns the new ones.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID, password, code, ip string) ([]string, error) {
	if _, err := s.reauthenticate(ctx, userID, password, code, ip); err != nil {
		return nil, err
	}
	codes := newRecoveryCodes()
	if err := s.store.ReplaceRecoveryCodes(ctx, userID, hashCodes(codes)); err != nil {
		return nil, fmt.Errorf("storing new recovery codes: %w", err)
	}
	return codes, nil
}

// ResetTwoStep is the administrator's reset, for somebody who has lost their
// authenticator and their recovery codes. It removes the secret and the
// codes and ends every session the account has, and it reports whether there
// was anything to remove. The caller audits it.
func (s *Service) ResetTwoStep(ctx context.Context, userID string) (bool, error) {
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return false, err
	}
	removed, err := s.store.DeleteTwoStep(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("resetting two-step: %w", err)
	}
	if err := s.store.DeleteUserSessions(ctx, userID); err != nil {
		s.logger.Warn("could not end sessions after a two-step reset", "user_id", userID, "error", err)
	}
	return removed, nil
}

// endOtherSessions ends every session of an account but the one making the
// request, identified by its plaintext cookie.
func (s *Service) endOtherSessions(ctx context.Context, userID, keep string) {
	keepHash := ""
	if keep != "" {
		keepHash = cryptox.HashToken(keep)
	}
	if err := s.store.DeleteUserSessionsExcept(ctx, userID, keepHash); err != nil {
		s.logger.Warn("could not end other sessions after turning two-step on", "user_id", userID, "error", err)
	}
}

func hashCodes(codes []string) []string {
	out := make([]string, len(codes))
	for i, c := range codes {
		out[i] = cryptox.HashToken(normaliseCode(c))
	}
	return out
}
