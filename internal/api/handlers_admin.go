package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// userResponse is an account. The password hash is not a field here and cannot
// become one by accident: the type is written out rather than derived from the
// domain model.
type userResponse struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	Email              string     `json:"email,omitempty"`
	DisplayName        string     `json:"display_name,omitempty"`
	Role               store.Role `json:"role"`
	OIDCSubject        string     `json:"oidc_subject,omitempty"`
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	LastLoginAt        *time.Time `json:"last_login_at"`
	// TwoStepEnabled is whether the account signs in with a code after its
	// password. It is here so an administrator can see who a reset is for.
	TwoStepEnabled bool `json:"two_step_enabled"`
}

// withTwoStep fills in two_step_enabled for one account. A failure to read
// it is logged and shown as off: the users page is not worth refusing over
// one column.
func (s *Server) withTwoStep(r *http.Request, out userResponse) userResponse {
	ts, err := s.ctrl.Store().GetTwoStep(r.Context(), out.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger(r).Warn("could not read an account's two-step state", "user_id", out.ID, "error", err)
	}
	out.TwoStepEnabled = err == nil && ts.Enabled()
	return out
}

func newUserResponse(u *store.User) userResponse {
	return userResponse{
		ID: u.ID, Username: u.Username, Email: u.Email, DisplayName: u.DisplayName,
		Role: u.Role, OIDCSubject: u.OIDCSubject, Disabled: u.Disabled,
		MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// handleListUsers answers GET /api/v1/users.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.ctrl.Store().ListUsers(r.Context())
	if err != nil {
		s.internal(w, r, "listing accounts", err)
		return
	}
	enabled, err := s.ctrl.Store().TwoStepEnabledUsers(r.Context())
	if err != nil {
		s.internal(w, r, "listing accounts", err)
		return
	}
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		row := newUserResponse(u)
		row.TwoStepEnabled = enabled[u.ID]
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// handleGetUser answers GET /api/v1/users/{id}.
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.ctrl.Store().GetUser(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the account", err)
		return
	}
	writeJSON(w, http.StatusOK, s.withTwoStep(r, newUserResponse(u)))
}

type createUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// handleCreateUser adds an account.
//
// The password may be omitted for an account that will sign in through the
// identity provider: the first single sign-on with that username links it.
// That is only allowed while SSO is on, which is what stops an account being
// created that nobody can ever use.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !decode(w, r, &req) {
		return
	}

	var fields []fieldError
	if strings.TrimSpace(req.Username) == "" {
		fields = append(fields, fieldError{"username", "an account needs a name to sign in with"})
	}
	role := store.Role(strings.ToLower(strings.TrimSpace(req.Role)))
	if role == "" {
		role = store.RoleViewer
	}
	if !role.Valid() {
		fields = append(fields, fieldError{"role", fmt.Sprintf("%q is not a role; use %s", req.Role, store.RoleList())})
	}
	if req.Password != "" {
		if err := auth.CheckPassword(req.Password); err != nil {
			fields = append(fields, fieldError{"password", err.Error()})
		}
	} else if !s.oidc.Enabled() {
		fields = append(fields, fieldError{"password", "this instance has no single sign-on configured, so an account needs a password"})
	}
	if len(fields) > 0 {
		unprocessable(w, "this account could not be created", fields)
		return
	}
	if err := auth.ManageWithin(Identity(r.Context()), role); err != nil {
		forbidden(w, err.Error())
		return
	}

	u, err := s.auth.CreateUser(r.Context(), auth.NewUser{
		Username:    req.Username,
		Password:    req.Password,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Role:        role,
		SSOOnly:     req.Password == "",
		// Somebody else chose this password, so its owner picks their own at
		// first sign-in.
		MustChangePassword: req.Password != "",
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			conflict(w, err.Error())
		case errors.Is(err, auth.ErrInvalidInput):
			unprocessable(w, err.Error(), nil)
		default:
			s.fail(w, r, "creating the account", err)
		}
		return
	}

	s.auth.Auditor().Created(r.Context(), Identity(r.Context()), "user", u.ID, newUserResponse(u))
	writeJSON(w, http.StatusCreated, newUserResponse(u))
}

type updateUserRequest struct {
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
	Disabled    *bool   `json:"disabled"`
}

// handleUpdateUser changes an account.
//
// Demoting or disabling the last enabled administrator is refused by the auth
// service, which is the invariant behind "you cannot lock yourself out of your
// own controller".
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	u, err := s.ctrl.Store().GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the account", err)
		return
	}

	var req updateUserRequest
	if !decode(w, r, &req) {
		return
	}
	if err := auth.ManageWithin(Identity(r.Context()), u.Role); err != nil {
		forbidden(w, err.Error())
		return
	}
	before := *u
	if req.Email != nil {
		u.Email = strings.TrimSpace(*req.Email)
	}
	if req.DisplayName != nil {
		u.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Role != nil {
		role := store.Role(strings.ToLower(strings.TrimSpace(*req.Role)))
		if !role.Valid() {
			unprocessable(w, "this account could not be changed", []fieldError{
				{"role", fmt.Sprintf("%q is not a role; use %s", *req.Role, store.RoleList())},
			})
			return
		}
		if err := auth.ManageWithin(Identity(r.Context()), role); err != nil {
			forbidden(w, err.Error())
			return
		}
		u.Role = role
	}
	if req.Disabled != nil {
		u.Disabled = *req.Disabled
	}

	if err := s.auth.UpdateUser(r.Context(), u); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) || errors.Is(err, store.ErrConflict) {
			conflict(w, err.Error())
			return
		}
		s.fail(w, r, "saving the account", err)
		return
	}
	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "user", id, newUserResponse(&before), newUserResponse(u))
	writeJSON(w, http.StatusOK, s.withTwoStep(r, newUserResponse(u)))
}

// handleDeleteUser removes an account, refusing to remove the last admin.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	u, err := s.ctrl.Store().GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the account", err)
		return
	}
	if err := auth.ManageWithin(Identity(r.Context()), u.Role); err != nil {
		forbidden(w, err.Error())
		return
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) {
			conflict(w, err.Error())
			return
		}
		s.fail(w, r, "deleting the account", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "user", id, newUserResponse(u))
	noContent(w)
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// handleResetPassword is the administrator's reset. It flags the account so its
// owner chooses their own password at the next sign-in, and ends every session
// the account had.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	var req resetPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := auth.CheckPassword(req.NewPassword); err != nil {
		unprocessable(w, "that password cannot be used", []fieldError{{"new_password", err.Error()}})
		return
	}
	target, err := s.ctrl.Store().GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the account", err)
		return
	}
	if err := auth.ManageWithin(Identity(r.Context()), target.Role); err != nil {
		forbidden(w, err.Error())
		return
	}
	if err := s.auth.ResetPassword(r.Context(), id, req.NewPassword); err != nil {
		s.fail(w, r, "resetting the password", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "user.password_reset", "user", id, nil)
	noContent(w)
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

// tokenResponse is a token's metadata. The value itself exists in plaintext
// exactly once, in the response to the call that created it.
type tokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       store.Role `json:"role"`
	Scopes     []string   `json:"scopes"`
	Prefix     string     `json:"prefix"`
	Revoked    bool       `json:"revoked"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type createdTokenResponse struct {
	tokenResponse
	Token string `json:"token"`
}

func newTokenResponse(t *store.APIToken) tokenResponse {
	return tokenResponse{
		ID: t.ID, Name: t.Name, Role: t.Role, Scopes: emptySlice(t.Scopes),
		Prefix: t.Prefix, Revoked: t.Revoked, CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
	}
}

// tokenVisibleTo says whether this caller may see a token at all.
//
// A token minted by a platform account is the platform's -- the metrics
// scraper reading an instance it operates for another team, the verifier
// checking its backups -- and on the Tokens page it used to sit beside the
// fleet's own, revocable by any administrator. A fleet that can switch off the
// monitoring of a process it does not run is a fleet that can do so by
// accident.
//
// A token from before the column existed has no owner recorded, and stays
// visible to whoever could see it before. Guessing an owner from the token's
// role would hide the fleet's own automation from it, because 0045 promoted
// those tokens to platform for an unrelated reason.
func tokenVisibleTo(t *store.APIToken, who store.Role) bool {
	if t.OwnerRole == store.RolePlatform {
		return who.AtLeast(store.RolePlatform)
	}
	return true
}

// handleListTokens answers GET /api/v1/tokens.
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.ctrl.Store().ListAPITokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing API tokens", err)
		return
	}
	who := callerRole(r)
	out := make([]tokenResponse, 0, len(tokens))
	for _, t := range tokens {
		if !tokenVisibleTo(t, who) {
			continue
		}
		out = append(out, newTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

type createTokenRequest struct {
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Scopes    []string `json:"scopes"`
	ExpiresIn string   `json:"expires_in"`
}

// handleCreateToken mints an API token and shows it once.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if !decode(w, r, &req) {
		return
	}

	var fields []fieldError
	if strings.TrimSpace(req.Name) == "" {
		fields = append(fields, fieldError{"name", "a token needs a name; it is how you will recognise it in the list later"})
	}
	role := store.Role(strings.ToLower(strings.TrimSpace(req.Role)))
	if role == "" {
		role = store.RoleViewer
	}
	if !role.Valid() {
		fields = append(fields, fieldError{"role", fmt.Sprintf("%q is not a role; use %s", req.Role, store.RoleList())})
	}
	if err := auth.ValidateScopes(req.Scopes); err != nil {
		fields = append(fields, fieldError{"scopes", err.Error()})
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.ExpiresIn); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fields = append(fields, fieldError{"expires_in", fmt.Sprintf("%q is not a duration; write it like 720h for 30 days", raw)})
		case d <= 0:
			fields = append(fields, fieldError{"expires_in", "an expiry has to be in the future; leave it empty for a token that never expires"})
		default:
			t := s.auth.Now().Add(d)
			expiresAt = &t
		}
	}
	if len(fields) > 0 {
		unprocessable(w, "this token could not be created", fields)
		return
	}

	// A token is attributed to the account behind the caller -- the person
	// signed in, or the owner of the token being used -- so that disabling or
	// deleting that account ends every credential descended from it. A token
	// with no owner has nobody to attribute to, and minting from it would
	// produce credentials that outlive every revocation, so it cannot.
	id := Identity(r.Context())
	if id == nil || id.UserID == "" {
		unprocessable(w, "this token has no owner, so it cannot mint tokens; sign in as a user, or use a token created by one", nil)
		return
	}
	if err := auth.MintWithin(id, role, req.Scopes); err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	token, plaintext, err := s.auth.CreateAPIToken(r.Context(), auth.NewToken{
		Name: strings.TrimSpace(req.Name), Role: role, UserID: id.UserID,
		Scopes: req.Scopes, ExpiresAt: expiresAt, OwnerRole: id.Role,
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			unprocessable(w, err.Error(), nil)
			return
		}
		s.fail(w, r, "creating the token", err)
		return
	}
	// The audit row records that a credential was minted, never the credential.
	s.auth.Auditor().Created(r.Context(), id, "token", token.ID, newTokenResponse(token))

	writeJSON(w, http.StatusCreated, createdTokenResponse{
		tokenResponse: newTokenResponse(token),
		Token:         plaintext,
	})
}

// handleRevokeToken answers DELETE /api/v1/tokens/{id}. Without ?purge it
// disables a token while keeping its row, so the list and the audit trail can
// still resolve the actions it took. With ?purge=true it removes a token that
// is already revoked or expired; a live one is a 409, because deleting is
// tidying up after a revocation and never a quieter way to do one.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	tokens, err := s.ctrl.Store().ListAPITokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing API tokens", err)
		return
	}
	idx := slices.IndexFunc(tokens, func(t *store.APIToken) bool { return t.ID == id })
	// A token this caller cannot see answers exactly as one that is not there.
	// Refusing with a 403 would confirm the platform holds a credential by
	// this ID, which is the fact being withheld.
	if idx < 0 || !tokenVisibleTo(tokens[idx], callerRole(r)) {
		notFound(w, "there is no API token "+id)
		return
	}
	token := tokens[idx]
	if purge, _ := strconv.ParseBool(r.URL.Query().Get("purge")); purge {
		live := fmt.Sprintf("%s (%s) still works, so it cannot be deleted; revoke it first", token.Name, token.Prefix)
		if !tokenSpent(token, s.auth.Now()) {
			conflict(w, live)
			return
		}
		if err := s.deleteToken(r, token); err != nil {
			if errors.Is(err, store.ErrConflict) {
				conflict(w, live)
				return
			}
			s.fail(w, r, "deleting the token", err)
			return
		}
		noContent(w)
		return
	}
	if err := s.auth.RevokeAPIToken(r.Context(), id); err != nil {
		s.fail(w, r, "revoking the token", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "token.revoke", "token", id, tokenAuditDetail(token))
	noContent(w)
}

// tokenSpent says whether a token can no longer authenticate, which is the
// only kind that may be deleted.
func tokenSpent(t *store.APIToken, now time.Time) bool {
	return t.Revoked || (t.ExpiresAt != nil && t.ExpiresAt.Before(now))
}

// tokenAuditDetail is what an audit row says about a token: enough to match it
// to a leaked string or a line in a log, and never the secret. It is written
// before a deletion, so the row still names the token once the token is gone.
func tokenAuditDetail(t *store.APIToken) map[string]any {
	return map[string]any{"name": t.Name, "prefix": t.Prefix, "user_id": t.UserID, "revoked": t.Revoked}
}

// deleteToken removes one spent token and writes the audit row for it.
func (s *Server) deleteToken(r *http.Request, t *store.APIToken) error {
	if err := s.auth.DeleteAPIToken(r.Context(), t.ID); err != nil {
		return err
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "token.delete", "token", t.ID, tokenAuditDetail(t))
	return nil
}

type purgeTokensRequest struct {
	UserID string `json:"user_id"`
	All    bool   `json:"all"`
}

type purgeTokensResponse struct {
	Deleted []tokenResponse `json:"deleted"`
}

// handlePurgeTokens answers POST /api/v1/tokens/purge: delete every revoked or
// expired token the caller owns, one account's, or -- with all -- every one
// the caller can see. Tokens that still work are never touched, so this is
// safe to press without reading the list first.
func (s *Server) handlePurgeTokens(w http.ResponseWriter, r *http.Request) {
	var req purgeTokensRequest
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	owner := strings.TrimSpace(req.UserID)
	// Both at once is ambiguous, and reading it as "all" would purge every
	// account's tokens for a caller who meant one.
	if req.All && owner != "" {
		unprocessable(w, "user_id and all ask different questions; give user_id to purge one account's spent tokens, or all to purge every one", []fieldError{{"all", "cannot be combined with user_id"}})
		return
	}
	if !req.All && owner == "" {
		id := Identity(r.Context())
		if id == nil || id.UserID == "" {
			unprocessable(w, "this caller has no account, so it owns no tokens; name one with user_id, or pass all to purge every spent token", nil)
			return
		}
		owner = id.UserID
	}
	tokens, err := s.ctrl.Store().ListAPITokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing API tokens", err)
		return
	}
	now, who := s.auth.Now(), callerRole(r)
	out := purgeTokensResponse{Deleted: []tokenResponse{}}
	for _, t := range tokens {
		if !tokenVisibleTo(t, who) || !tokenSpent(t, now) || (!req.All && t.UserID != owner) {
			continue
		}
		if err := s.deleteToken(r, t); err != nil {
			// Gone already, or un-spent by a clock that moved: neither is a
			// reason to stop tidying the rest.
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
				continue
			}
			s.fail(w, r, "deleting the token", err)
			return
		}
		out.Deleted = append(out.Deleted, newTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, out)
}
