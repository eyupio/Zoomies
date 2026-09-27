package api

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// SignInCookie carries a sign-in that has passed the password and waits for
// its second step. It is scoped to the auth routes, lives as long as the
// pending sign-in does, and is Strict: nothing but this UI's own requests
// should ever send it.
const SignInCookie = "zoomies_sign_in"

const signInCookiePath = "/api/v1/auth"

// signInChallengeResponse is the 202 a correct password gets when the
// account has a second step still to take.
type signInChallengeResponse struct {
	TwoStep   string    `json:"two_step"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

// twoStepSignInResponse is a finished second step: who is now signed in,
// and what the page should say about recovery codes.
type twoStepSignInResponse struct {
	Identity          identityResponse `json:"identity"`
	RecoveryCodeUsed  bool             `json:"recovery_code_used"`
	RecoveryCodesLeft *int             `json:"recovery_codes_left,omitempty"`
	RecoveryCodes     []string         `json:"recovery_codes,omitempty"`
}

type twoStepStatusResponse struct {
	Available         bool       `json:"available"`
	Enabled           bool       `json:"enabled"`
	EnabledAt         *time.Time `json:"enabled_at,omitempty"`
	RecoveryCodesLeft int        `json:"recovery_codes_left"`
	Required          bool       `json:"required"`
}

type twoStepSetupResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
	QRSVG  string `json:"qr_svg"`
}

type twoStepCodeRequest struct {
	Code string `json:"code"`
}

type twoStepReauthRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (s *Server) setSignInCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SignInCookie,
		Value:    token,
		Path:     signInCookiePath,
		HttpOnly: true,
		Secure:   s.cfg().CookieSecureValue(),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(time.Until(expires).Round(time.Second) / time.Second),
	})
}

func (s *Server) clearSignInCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SignInCookie,
		Value:    "",
		Path:     signInCookiePath,
		HttpOnly: true,
		Secure:   s.cfg().CookieSecureValue(),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func signInChallenge(r *http.Request) string {
	if c, err := r.Cookie(SignInCookie); err == nil {
		return c.Value
	}
	return ""
}

// startSecondStep answers a correct password whose account has a second step
// to take: the challenge goes into a cookie the page cannot read, and the
// body says which step comes next.
func (s *Server) startSecondStep(w http.ResponseWriter, pending *auth.SecondStepRequired) {
	s.setSignInCookie(w, pending.Challenge, pending.ExpiresAt)
	writeJSON(w, http.StatusAccepted, signInChallengeResponse{
		TwoStep: pending.Purpose, Username: pending.User.Username, ExpiresAt: pending.ExpiresAt,
	})
}

// twoStepHost is the address the authenticator app files the entry under:
// the configured external address, or the one this request arrived on.
func (s *Server) twoStepHost(r *http.Request) string {
	if u, err := url.Parse(s.cfg().Server.ExternalURL); err == nil && u.Host != "" {
		return u.Host
	}
	return r.Host
}

// failSecondStep maps a refused second step onto a response. A code that is
// wrong is a 401 like a wrong password; a sign-in that has run out clears
// its cookie, because the page has to go back to the password.
func (s *Server) failSecondStep(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		ip := ClientIP(r.Context())
		rateLimited(w, err.Error(), s.auth.LoginRetryAfter(ip))
	case errors.Is(err, auth.ErrSignInExpired):
		s.clearSignInCookie(w)
		unauthorized(w, err.Error())
	case errors.Is(err, auth.ErrInvalidTwoStepCode):
		// Pointed at the code field, which is how the page tells "try the
		// next code" apart from "start again with the password".
		writeError(w, http.StatusUnauthorized, errorEnvelope{Error: errorBody{
			Code: codeUnauthorized, Message: err.Error(), Field: "code"}})
	case errors.Is(err, auth.ErrAccountDisabled):
		s.clearSignInCookie(w)
		unauthorized(w, err.Error())
	default:
		s.internal(w, r, "finishing the sign-in", err)
	}
}

// signedIn finishes a two-step sign-in the way handleLogin finishes a
// password-only one: the session cookie, the audit row, the identity.
func (s *Server) signedIn(w http.ResponseWriter, r *http.Request, in *auth.SignIn, how string) {
	s.clearSignInCookie(w)
	s.setSessionCookie(w, in.Session)
	u := in.User
	id := &auth.Identity{Kind: auth.KindUser, ID: u.ID, Name: u.Username, Role: u.Role, UserID: u.ID, IP: ClientIP(r.Context())}
	s.auth.Auditor().Auth(r.Context(), id, "auth.login", map[string]any{"role": u.Role, "two_step": how})
	out := twoStepSignInResponse{
		Identity:         newIdentityResponse(id, u.MustChangePassword),
		RecoveryCodeUsed: in.RecoveryCodeUsed,
		RecoveryCodes:    in.RecoveryCodes,
	}
	if in.RecoveryCodeUsed {
		left := in.RecoveryCodesLeft
		out.RecoveryCodesLeft = &left
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTwoStepVerify answers POST /auth/two-step/verify: the code step of a
// sign-in.
func (s *Server) handleTwoStepVerify(w http.ResponseWriter, r *http.Request) {
	var req twoStepCodeRequest
	if !decode(w, r, &req) {
		return
	}
	in, err := s.auth.VerifySignIn(r.Context(), signInChallenge(r), req.Code, ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		s.failSecondStep(w, r, err)
		return
	}
	how := "code"
	if in.RecoveryCodeUsed {
		how = "recovery_code"
	}
	s.signedIn(w, r, in, how)
}

// handleTwoStepEnrolStart answers POST /auth/two-step/enrol: the setup a
// sign-in has to finish because the instance requires two-step.
func (s *Server) handleTwoStepEnrolStart(w http.ResponseWriter, r *http.Request) {
	setup, err := s.auth.EnrolDuringSignIn(r.Context(), signInChallenge(r), s.twoStepHost(r), r.UserAgent())
	if err != nil {
		s.failSetup(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, setupResponse(setup))
}

// handleTwoStepEnrolConfirm answers POST /auth/two-step/enrol/confirm.
func (s *Server) handleTwoStepEnrolConfirm(w http.ResponseWriter, r *http.Request) {
	var req twoStepCodeRequest
	if !decode(w, r, &req) {
		return
	}
	in, err := s.auth.ConfirmDuringSignIn(r.Context(), signInChallenge(r), req.Code, ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		s.failSecondStep(w, r, err)
		return
	}
	s.auth.Auditor().Act(r.Context(), &auth.Identity{Kind: auth.KindUser, ID: in.User.ID, Name: in.User.Username,
		Role: in.User.Role, UserID: in.User.ID, IP: ClientIP(r.Context())}, "user.two_step_enabled", "user", in.User.ID,
		map[string]any{"during_sign_in": true})
	s.signedIn(w, r, in, "enrolled")
}

func setupResponse(setup *auth.TwoStepSetup) twoStepSetupResponse {
	return twoStepSetupResponse{Secret: setup.Secret, URI: setup.URI, QRSVG: setup.QRSVG}
}

func (s *Server) failSetup(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrSignInExpired), errors.Is(err, auth.ErrAccountDisabled):
		s.failSecondStep(w, r, err)
	case errors.Is(err, auth.ErrRateLimited):
		rateLimited(w, err.Error(), s.auth.LoginRetryAfter(ClientIP(r.Context())))
	case errors.Is(err, auth.ErrTwoStepAlreadyOn):
		conflict(w, err.Error())
	case errors.Is(err, auth.ErrInvalidTwoStepCode):
		unprocessable(w, err.Error(), []fieldError{{"code", err.Error()}})
	case errors.Is(err, auth.ErrInvalidInput):
		unprocessable(w, err.Error(), nil)
	default:
		s.fail(w, r, "setting up two-step verification", err)
	}
}

// ownAccount is the account a self-service two-step route acts on: a person
// signed in with a session. A token is refused, because two-step guards the
// sign-in a token never makes.
func ownAccount(w http.ResponseWriter, r *http.Request) (*auth.Identity, bool) {
	id := Identity(r.Context())
	if id == nil || id.Kind != auth.KindUser || !store.HasPrefix(id.ID, store.PrefixUser) {
		forbidden(w, "two-step verification belongs to an account signing in with a password; "+
			"sign in to the UI to change it -- an API token is not asked for a code and cannot set one up")
		return nil, false
	}
	return id, true
}

// handleTwoStepStatus answers GET /auth/two-step.
func (s *Server) handleTwoStepStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := ownAccount(w, r)
	if !ok {
		return
	}
	st, err := s.auth.TwoStepStatus(r.Context(), id.ID)
	if err != nil {
		s.fail(w, r, "reading two-step verification", err)
		return
	}
	writeJSON(w, http.StatusOK, twoStepStatusResponse{
		Available: st.Available, Enabled: st.Enabled, EnabledAt: st.EnabledAt,
		RecoveryCodesLeft: st.RecoveryCodesLeft, Required: st.Required,
	})
}

// handleTwoStepSetup answers POST /auth/two-step/setup: a new key, not yet
// in force.
func (s *Server) handleTwoStepSetup(w http.ResponseWriter, r *http.Request) {
	id, ok := ownAccount(w, r)
	if !ok {
		return
	}
	setup, err := s.auth.BeginTwoStep(r.Context(), id.ID, s.twoStepHost(r))
	if err != nil {
		s.failSetup(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, setupResponse(setup))
}

// handleTwoStepConfirm answers POST /auth/two-step/confirm: the first code,
// which turns two-step on and returns the recovery codes.
func (s *Server) handleTwoStepConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := ownAccount(w, r)
	if !ok {
		return
	}
	var req twoStepCodeRequest
	if !decode(w, r, &req) {
		return
	}
	keep := ""
	if c, err := r.Cookie(SessionCookie); err == nil {
		keep = c.Value
	}
	codes, err := s.auth.ConfirmTwoStep(r.Context(), id.ID, req.Code, keep, ClientIP(r.Context()))
	if err != nil {
		s.failSetup(w, r, err)
		return
	}
	s.auth.Auditor().Act(r.Context(), id, "user.two_step_enabled", "user", id.ID, nil)
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

// failReauth maps a refused password-and-code proof onto field errors the
// dialog can put under the right box.
func (s *Server) failReauth(w http.ResponseWriter, r *http.Request, doing string, err error) {
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		rateLimited(w, err.Error(), s.auth.LoginRetryAfter(ClientIP(r.Context())))
	case errors.Is(err, auth.ErrWrongPassword):
		unprocessable(w, err.Error(), []fieldError{{"password", err.Error()}})
	case errors.Is(err, auth.ErrInvalidTwoStepCode):
		unprocessable(w, err.Error(), []fieldError{{"code", err.Error()}})
	case errors.Is(err, auth.ErrInvalidInput):
		unprocessable(w, err.Error(), nil)
	default:
		s.fail(w, r, doing, err)
	}
}

// handleTwoStepDisable answers POST /auth/two-step/disable.
func (s *Server) handleTwoStepDisable(w http.ResponseWriter, r *http.Request) {
	id, ok := ownAccount(w, r)
	if !ok {
		return
	}
	var req twoStepReauthRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.auth.DisableTwoStep(r.Context(), id.ID, req.Password, req.Code, ClientIP(r.Context())); err != nil {
		s.failReauth(w, r, "turning two-step verification off", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), id, "user.two_step_disabled", "user", id.ID, nil)
	noContent(w)
}

// handleTwoStepRecoveryCodes answers POST /auth/two-step/recovery-codes.
func (s *Server) handleTwoStepRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	id, ok := ownAccount(w, r)
	if !ok {
		return
	}
	var req twoStepReauthRequest
	if !decode(w, r, &req) {
		return
	}
	codes, err := s.auth.RegenerateRecoveryCodes(r.Context(), id.ID, req.Password, req.Code, ClientIP(r.Context()))
	if err != nil {
		s.failReauth(w, r, "issuing new recovery codes", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), id, "user.recovery_codes_regenerated", "user", id.ID, nil)
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

// handleResetTwoStep answers DELETE /users/{id}/two-step: the
// administrator's answer to a lost phone. It is audited whether or not there
// was anything to remove, because an administrator taking the second factor
// off somebody's account is exactly what the audit log is for.
func (s *Server) handleResetTwoStep(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	target, err := s.ctrl.Store().GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the account", err)
		return
	}
	if err := auth.ManageWithin(Identity(r.Context()), target.Role); err != nil {
		forbidden(w, err.Error())
		return
	}
	removed, err := s.auth.ResetTwoStep(r.Context(), id)
	if err != nil {
		s.fail(w, r, "resetting two-step verification", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "user.two_step_reset", "user", id,
		map[string]any{"username": target.Username, "had_two_step": removed})
	noContent(w)
}
