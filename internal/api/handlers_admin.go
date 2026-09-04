package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
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
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, newUserResponse(u))
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
	writeJSON(w, http.StatusOK, newUserResponse(u))
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
// identity provider; the auth service refuses one that has neither a password
// nor an OIDC subject, which is what stops an account being created that nobody
// can ever use.
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
		fields = append(fields, fieldError{"role", fmt.Sprintf("%q is not a role; use viewer, operator or admin", req.Role)})
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

	u, err := s.auth.CreateUser(r.Context(), auth.NewUser{
		Username:    req.Username,
		Password:    req.Password,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Role:        role,
		// Somebody else chose this password, so its owner picks their own at
		// first sign-in.
		MustChangePassword: req.Password != "",
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) || strings.Contains(err.Error(), "already exists") {
			conflict(w, err.Error())
			return
		}
		unprocessable(w, err.Error(), nil)
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
				{"role", fmt.Sprintf("%q is not a role; use viewer, operator or admin", *req.Role)},
			})
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
	if req.Disabled != nil && *req.Disabled && !before.Disabled {
		// A disabled account must not keep a live cookie.
		if err := s.auth.LogoutAll(r.Context(), u.ID); err != nil {
			s.logger(r).Warn("could not end sessions for a disabled account", "user", u.Username, "error", err)
		}
	}

	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "user", id, newUserResponse(&before), newUserResponse(u))
	writeJSON(w, http.StatusOK, newUserResponse(u))
}

// handleDeleteUser removes an account, refusing to remove the last admin.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	u, err := s.ctrl.Store().GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the account", err)
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

// handleListTokens answers GET /api/v1/tokens.
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.ctrl.Store().ListAPITokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing API tokens", err)
		return
	}
	out := make([]tokenResponse, 0, len(tokens))
	for _, t := range tokens {
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
		fields = append(fields, fieldError{"role", fmt.Sprintf("%q is not a role; use viewer, operator or admin", req.Role)})
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

	id := Identity(r.Context())
	userID := ""
	if id != nil && id.Kind == auth.KindUser {
		userID = id.ID
	}
	token, plaintext, err := s.auth.CreateAPIToken(r.Context(), auth.NewToken{
		Name: strings.TrimSpace(req.Name), Role: role, UserID: userID,
		Scopes: req.Scopes, ExpiresAt: expiresAt,
	})
	if err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	// The audit row records that a credential was minted, never the credential.
	s.auth.Auditor().Created(r.Context(), id, "token", token.ID, newTokenResponse(token))

	writeJSON(w, http.StatusCreated, createdTokenResponse{
		tokenResponse: newTokenResponse(token),
		Token:         plaintext,
	})
}

// handleRevokeToken disables a token while keeping its row, so the audit trail
// can still resolve the actions it took.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	tokens, err := s.ctrl.Store().ListAPITokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing API tokens", err)
		return
	}
	idx := slices.IndexFunc(tokens, func(t *store.APIToken) bool { return t.ID == id })
	if idx < 0 {
		notFound(w, "there is no API token "+id)
		return
	}
	if err := s.auth.RevokeAPIToken(r.Context(), id); err != nil {
		s.fail(w, r, "revoking the token", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "token.revoke", "token", id, map[string]any{
		"name": tokens[idx].Name, "prefix": tokens[idx].Prefix,
	})
	noContent(w)
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// settingsResponse is the effective configuration, every secret blanked, with
// the validator's findings so the UI can put each one next to the setting it is
// about.
type settingsResponse struct {
	Config              map[string]any   `json:"config"`
	Findings            []config.Finding `json:"findings"`
	ConfigPath          string           `json:"config_path,omitempty"`
	RestartRequiredKeys []string         `json:"restart_required_keys"`
	Version             string           `json:"version"`
	DatabasePath        string           `json:"database_path"`
	EventSubscribers    int              `json:"event_subscribers"`
}

// runtimeWritable is the set of settings this process can change while it is
// running, and what each one does.
//
// Everything else needs a restart, and says so rather than appearing to work:
// rebinding the listener, changing the database or re-keying the instance are
// not things a running process can do to itself without dropping every
// connection it has.
var runtimeWritable = map[string]string{
	"log.level":                      "how much detail the controller logs",
	"github.poll_interval":           "how often the fallback poller looks for queued jobs",
	"scheduler.interval":             "how often the scheduler runs a pass",
	"scheduler.scale_up_delay":       "how long a job must wait before it counts as demand",
	"scheduler.max_runner_lifetime":  "when a long-lived runner is drained",
	"scheduler.provision_timeout":    "when a runner that never registered is failed",
	"scheduler.max_creates_per_tick": "how many runners may be created in one pass",
	"retention.jobs":                 "how long job history is kept",
	"retention.runners":              "how long finished runners are kept",
	"retention.audit":                "how long the audit log is kept",
	"retention.samples":              "how long the Overview's samples are kept",
	"retention.webhooks":             "how long webhook deliveries are kept",
}

// restartRequiredKeys lists the settings that exist and cannot be changed here.
// The UI renders them read-only with this list, rather than discovering one at
// a time by being refused.
var restartRequiredKeys = sync.OnceValue(func() []string {
	keys := []string{
		"server.bind", "server.external_url", "server.tls.mode", "server.tls.cert_file",
		"server.tls.key_file", "server.trusted_proxies", "server.allowed_origins",
		"server.read_timeout", "server.idle_timeout",
		"database.path",
		"security.encryption_key", "security.encryption_key_file", "security.session_ttl",
		"security.cookie_secure", "security.disable_auth", "security.rate_limit_logins",
		"github.api_base_url", "github.webhook_path", "github.poll_fallback",
		"github.runner_image", "github.runner_version",
		"agent.embedded", "agent.name", "agent.capacity", "agent.backend", "agent.docker_host",
		"agent.work_dir", "agent.labels", "agent.network", "agent.heartbeat_interval",
		"log.format",
		"oidc.enabled", "oidc.issuer", "oidc.client_id", "oidc.client_secret",
		"oidc.redirect_url", "oidc.scopes", "oidc.username_claim", "oidc.groups_claim",
		"oidc.admin_groups", "oidc.operator_groups", "oidc.allow_signup",
		"metrics.enabled", "metrics.path", "metrics.public",
	}
	slices.Sort(keys)
	return keys
})

// settingsConfig renders the configuration for display.
//
// It is built key by key rather than marshalled from the struct: that way a new
// secret added to config.Config cannot appear here by default, and the keys
// match the ones an operator would write in zoomies.yaml. Secrets are not
// blanked so much as absent, except where their presence is itself the useful
// fact -- whether an encryption key is configured, for instance.
func (s *Server) settingsConfig() map[string]any {
	c := s.cfg
	return map[string]any{
		"server": map[string]any{
			"bind":            c.Server.Bind,
			"external_url":    c.Server.ExternalURL,
			"read_timeout":    c.Server.ReadTimeout.String(),
			"write_timeout":   c.Server.WriteTimeout.String(),
			"idle_timeout":    c.Server.IdleTimeout.String(),
			"trusted_proxies": emptySlice(c.Server.TrustedProxies),
			"allowed_origins": emptySlice(c.Server.AllowedOrigins),
			"tls": map[string]any{
				"mode":      string(c.Server.TLS.Mode),
				"cert_file": c.Server.TLS.CertFile,
				"key_file":  c.Server.TLS.KeyFile,
				"hosts":     emptySlice(c.Server.TLS.Hosts),
			},
		},
		"database": map[string]any{"path": c.Database.Path},
		"security": map[string]any{
			// The key itself never leaves the process; whether one is set does.
			"encryption_key_configured": s.key != nil,
			"encryption_key_file":       c.Security.EncryptionKeyFile,
			"session_ttl":               c.Security.SessionTTL.String(),
			"cookie_secure":             c.CookieSecureValue(),
			"disable_auth":              c.Security.DisableAuth,
			"rate_limit_logins":         c.Security.RateLimitLogins,
		},
		"github": map[string]any{
			"api_base_url":    c.GitHub.APIBaseURL,
			"upload_base_url": c.GitHub.UploadBaseURL,
			"webhook_path":    c.GitHub.WebhookPath,
			"webhook_url":     c.WebhookURL(),
			"poll_interval":   c.GitHub.PollInterval.String(),
			"poll_fallback":   c.GitHub.PollFallback,
			"runner_image":    c.GitHub.RunnerImage,
			"runner_version":  c.GitHub.RunnerVersion,
			"polling_only":    s.ctrl.PollingOnly(),
		},
		"agent": map[string]any{
			"embedded":           c.Agent.Embedded,
			"name":               c.Agent.Name,
			"capacity":           c.Agent.Capacity,
			"backend":            c.Agent.Backend,
			"docker_host":        c.Agent.DockerHost,
			"work_dir":           c.Agent.WorkDir,
			"labels":             c.Agent.Labels,
			"network":            c.Agent.Network,
			"heartbeat_interval": c.Agent.HeartbeatInterval.String(),
		},
		"scheduler": map[string]any{
			"interval":             c.Scheduler.Interval.String(),
			"scale_up_delay":       c.Scheduler.ScaleUpDelay.String(),
			"max_runner_lifetime":  c.Scheduler.MaxRunnerLifetime.String(),
			"provision_timeout":    c.Scheduler.ProvisionTimeout.String(),
			"max_creates_per_tick": c.Scheduler.MaxCreatesPerTick,
		},
		"log": map[string]any{"level": c.Log.Level, "format": c.Log.Format},
		"oidc": map[string]any{
			"enabled":         c.OIDC.Enabled,
			"issuer":          c.OIDC.Issuer,
			"client_id":       c.OIDC.ClientID,
			"redirect_url":    s.oidcRedirectURL(),
			"scopes":          emptySlice(c.OIDC.Scopes),
			"username_claim":  c.OIDC.UsernameClaim,
			"groups_claim":    c.OIDC.GroupsClaim,
			"admin_groups":    emptySlice(c.OIDC.AdminGroups),
			"operator_groups": emptySlice(c.OIDC.OperatorGroups),
			"allow_signup":    c.OIDC.AllowSignup,
		},
		"metrics": map[string]any{
			"enabled": c.Metrics.Enabled,
			"path":    c.Metrics.Path,
			"public":  c.Metrics.Public,
		},
		"retention": map[string]any{
			"jobs":     c.Retention.Jobs.String(),
			"runners":  c.Retention.Runners.String(),
			"audit":    c.Retention.Audit.String(),
			"samples":  c.Retention.Samples.String(),
			"webhooks": c.Retention.Webhooks.String(),
		},
	}
}

func (s *Server) oidcRedirectURL() string {
	if s.oidc.Enabled() {
		return s.oidc.RedirectURL()
	}
	return s.cfg.OIDC.RedirectURL
}

// handleGetSettings answers GET /api/v1/settings.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsResponse{
		Config:              s.settingsConfig(),
		Findings:            s.cfg.Validate(),
		ConfigPath:          s.cfg.Path(),
		RestartRequiredKeys: restartRequiredKeys(),
		Version:             version.Short(),
		DatabasePath:        s.ctrl.Store().Path(),
		EventSubscribers:    s.ctrl.Events().Subscribers(),
	})
}

// handleUpdateSettings changes the settings that are safe to change here.
//
// The change takes effect on this running controller; it does not rewrite
// zoomies.yaml, so a setting that is also in the file goes back to the file's
// value at the next restart. config_path in the response names the file to
// edit, and the refusal for everything else says so in as many words.
//
// The writes go into the configuration the controller's loops are reading, so
// they are serialised here against each other. Each one is a single machine
// word -- a duration, an int, a short string header -- which a concurrent
// reader observes either before or after, never half-written, on the 64-bit
// platforms Zoomies is built for. The alternative would be a lock inside
// config.Config that every reader in the program has to remember to take, for a
// setting an operator changes once a month.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !decode(w, r, &raw) {
		return
	}
	flat := map[string]any{}
	flatten("", raw, flat)

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	var fields []fieldError
	applied := map[string]any{}
	before := map[string]any{}

	for key, value := range flat {
		if _, ok := runtimeWritable[key]; !ok {
			if slices.Contains(restartRequiredKeys(), key) {
				fields = append(fields, fieldError{key, fmt.Sprintf(
					"%s cannot be changed while the controller is running; edit %s and restart it", key, s.configFileName())})
				continue
			}
			fields = append(fields, fieldError{key, fmt.Sprintf("%q is not a setting this API knows about", key)})
			continue
		}
		prev, err := s.applySetting(key, value)
		if err != nil {
			// The description says what the setting is for, which is what makes
			// "5 munutes is not a duration" into a sentence an operator can act
			// on without opening the documentation.
			fields = append(fields, fieldError{key, fmt.Sprintf("%s (%s): %s", key, runtimeWritable[key], err)})
			continue
		}
		before[key], applied[key] = prev, value
	}

	if len(fields) > 0 {
		unprocessable(w, "these settings could not be changed", fields)
		return
	}
	if len(applied) > 0 {
		s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "settings", "settings", before, applied)
		// The scheduler tunables change what the next pass decides.
		s.ctrl.Nudge()
	}
	s.handleGetSettings(w, r)
}

func (s *Server) configFileName() string {
	if p := s.cfg.Path(); p != "" {
		return p
	}
	return "zoomies.yaml"
}

// applySetting writes one setting into the live configuration, returning what
// it was.
//
// The loops that read these values do so on every pass, so a change is in
// effect immediately -- with two exceptions worth knowing about: the scheduler
// and poller tickers were built with their interval at startup, so a new
// interval takes effect at the next restart, and the log level applies to
// loggers built after it changes. Both are still accepted here because the
// stored value is what the settings page shows and what a restart will use.
func (s *Server) applySetting(key string, value any) (any, error) {
	switch key {
	case "log.level":
		v, err := stringValue(value)
		if err != nil {
			return nil, err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		if !slices.Contains([]string{"debug", "info", "warn", "error"}, v) {
			return nil, fmt.Errorf("%q is not a log level; use debug, info, warn or error", v)
		}
		prev := s.cfg.Log.Level
		s.cfg.Log.Level = v
		return prev, nil

	case "github.poll_interval":
		return applyDuration(value, &s.cfg.GitHub.PollInterval, time.Second)
	case "scheduler.interval":
		return applyDuration(value, &s.cfg.Scheduler.Interval, time.Second)
	case "scheduler.scale_up_delay":
		return applyDuration(value, &s.cfg.Scheduler.ScaleUpDelay, 0)
	case "scheduler.max_runner_lifetime":
		return applyDuration(value, &s.cfg.Scheduler.MaxRunnerLifetime, 0)
	case "scheduler.provision_timeout":
		return applyDuration(value, &s.cfg.Scheduler.ProvisionTimeout, 0)
	case "scheduler.max_creates_per_tick":
		n, err := intValue(value)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, errors.New("this cannot be negative; use 0 for no cap")
		}
		prev := s.cfg.Scheduler.MaxCreatesPerTick
		s.cfg.Scheduler.MaxCreatesPerTick = n
		return prev, nil

	case "retention.jobs":
		return applyDuration(value, &s.cfg.Retention.Jobs, 0)
	case "retention.runners":
		return applyDuration(value, &s.cfg.Retention.Runners, 0)
	case "retention.audit":
		return applyDuration(value, &s.cfg.Retention.Audit, 0)
	case "retention.samples":
		return applyDuration(value, &s.cfg.Retention.Samples, 0)
	case "retention.webhooks":
		return applyDuration(value, &s.cfg.Retention.Webhooks, 0)
	}
	return nil, fmt.Errorf("%q is not a setting this API knows about", key)
}

// applyDuration parses a Go duration and writes it, refusing anything below
// minimum -- a poll interval of one millisecond is a denial of service against
// GitHub, not a configuration choice.
func applyDuration(value any, into *time.Duration, minimum time.Duration) (any, error) {
	raw, err := stringValue(value)
	if err != nil {
		return nil, err
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%q is not a duration; write it like 30s, 5m or 720h", raw)
	}
	if d < 0 {
		return nil, errors.New("a duration here cannot be negative; use 0 to switch it off")
	}
	if minimum > 0 && d > 0 && d < minimum {
		return nil, fmt.Errorf("%s is too short; the smallest useful value is %s", d, minimum)
	}
	prev := into.String()
	*into = d
	return prev, nil
}

func stringValue(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expected a string, got %T", v)
	}
	return s, nil
}

func intValue(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("expected a whole number, got %v", n)
		}
		return int(n), nil
	case string:
		var out int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &out); err != nil {
			return 0, fmt.Errorf("%q is not a whole number", n)
		}
		return out, nil
	default:
		return 0, fmt.Errorf("expected a whole number, got %T", v)
	}
}

// flatten turns a nested settings object into dotted keys, so that a client may
// send either {"retention":{"jobs":"720h"}} or {"retention.jobs":"720h"} -- the
// UI's form produces one and a script written by hand produces the other.
func flatten(prefix string, in map[string]any, out map[string]any) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			flatten(key, nested, out)
			continue
		}
		out[key] = v
	}
}
