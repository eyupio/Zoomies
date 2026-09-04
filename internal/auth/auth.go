// Package auth owns identity, authorisation and the audit trail: who is
// calling, what they are allowed to do, and what they did.
//
// Everything here is deliberately independent of HTTP. The API layer extracts a
// cookie, a header and a client address, hands them to Authenticate, and gets
// back an Identity; from then on it asks Identity.Can(action) and records the
// outcome with the Auditor. Keeping the policy out of the handlers is what
// makes the RBAC table in rbac.go testable as a whole, rather than one endpoint
// at a time.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// Identity kinds. They are stored verbatim in the audit log's actor_kind
// column, so the UI can group by them.
const (
	KindUser   = "user"
	KindToken  = "token"
	KindAgent  = "agent"
	KindSystem = "system"
)

// MinPasswordLength is the shortest password the product accepts. It is a
// length floor and nothing else: no character-class rules, which push people
// towards predictable substitutions rather than longer passwords.
const MinPasswordLength = 12

// Credential prefixes. Each one is self-describing, so a leaked string in a log
// or a paste is immediately recognisable as a Zoomies credential and can be
// revoked by its prefix.
const (
	APITokenPrefix   = "zoo_"
	JoinTokenPrefix  = "zoojoin_"
	AgentTokenPrefix = "zooagent_"
)

// secretBytes is the entropy in every minted credential: 20 random bytes render
// as exactly 32 base32 characters.
const secretBytes = 20

// touchInterval is how stale a token's last_used_at may get. Writing it on
// every request would turn a read-only API call into a database write and
// serialise the whole fleet behind SQLite's single writer; once a minute is
// enough to answer "is this token still in use?".
const touchInterval = time.Minute

// Authentication and authorisation failures. Handlers map these onto status
// codes, so they are sentinels rather than formatted strings.
var (
	// ErrNoCredentials means the request carried neither a session cookie nor
	// a bearer token.
	ErrNoCredentials = errors.New("not signed in")
	// ErrInvalidCredentials is returned for a wrong password, an unknown
	// username, and an unknown token alike: an attacker learns nothing from it.
	ErrInvalidCredentials = errors.New("incorrect username or password")
	// ErrSessionExpired means the cookie was valid but is past its lifetime.
	ErrSessionExpired = errors.New("your session has expired; sign in again")
	// ErrTokenExpired means the API token is past its expiry date.
	ErrTokenExpired = errors.New("this API token has expired; create a new one")
	// ErrTokenRevoked means the API token was revoked by an administrator.
	ErrTokenRevoked = errors.New("this API token has been revoked; create a new one")
	// ErrAccountDisabled means the account exists but has been switched off.
	ErrAccountDisabled = errors.New("this account is disabled; ask an administrator to re-enable it")
	// ErrSSOOnly means the account has no password because it comes from the
	// identity provider.
	ErrSSOOnly = errors.New("this account signs in with single sign-on; use the SSO button on the login page instead of a password")
	// ErrRateLimited means too many login attempts came from one address.
	ErrRateLimited = errors.New("too many login attempts from this address; wait a minute and try again")
	// ErrAlreadyBootstrapped means the first-admin endpoint was called on an
	// instance that already has users.
	ErrAlreadyBootstrapped = errors.New("this instance already has an account, so the first-admin endpoint is closed; sign in, or reset a password with `zoomies users passwd`")
	// ErrLastAdmin means the change would leave nobody able to administer the
	// instance.
	ErrLastAdmin = errors.New("this is the last enabled administrator; give another account the admin role before changing this one")
	// ErrPasswordTooShort is returned by every path that sets a password.
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
)

// Identity is the authenticated caller: a person with a session, a token used
// by automation, an agent reporting on its runners, or the controller itself.
type Identity struct {
	// Kind is one of KindUser, KindToken, KindAgent or KindSystem.
	Kind string
	// ID is the user, token, or host ID this identity resolves to.
	ID string
	// Name is what the audit log shows: a username, a token name, a host name.
	Name string
	// Role is the RBAC level this identity carries.
	Role store.Role
	// Scopes optionally narrows the identity below its role. Empty means the
	// role alone decides.
	Scopes []string
	// TokenID is set when the caller authenticated with an API token, so a
	// leaked token can be traced through the audit log to the actions it took.
	TokenID string
	// IP is the client address the request came from, recorded in the audit log.
	IP string
}

// Can reports whether this identity may perform a.
func (i *Identity) Can(a Action) bool { return Allowed(i, a) }

// String renders the identity for a log line: "user alice (admin)".
func (i *Identity) String() string {
	if i == nil {
		return "unauthenticated"
	}
	name := i.Name
	if name == "" {
		name = i.ID
	}
	s := fmt.Sprintf("%s %s (%s)", i.Kind, name, i.Role)
	if len(i.Scopes) > 0 {
		s += " scopes=" + strings.Join(i.Scopes, ",")
	}
	return s
}

// subject is the noun Explain uses about this identity: an operator reading
// "your token has viewer" knows to look at the token, not at their account.
func (i *Identity) subject() string {
	switch i.Kind {
	case KindToken:
		return "token"
	case KindAgent:
		return "agent"
	case KindSystem:
		return "system identity"
	default:
		return "account"
	}
}

// SystemIdentity is the actor for things the controller does on its own --
// scaling decisions, retention pruning, health probes -- so those audit rows
// have a name rather than an empty actor.
func SystemIdentity() *Identity {
	return &Identity{Kind: KindSystem, ID: "system", Name: "zoomies", Role: store.RoleAdmin}
}

// AgentIdentity is the actor for a request authenticated with an agent's own
// token. Agents are not fleet operators: they may only reach /api/v1/agent/*,
// which the API routes separately, so this identity carries the viewer role and
// exists mainly to attribute audit rows to a host.
func AgentIdentity(h *store.Host, ip string) *Identity {
	return &Identity{Kind: KindAgent, ID: h.ID, Name: h.Name, Role: store.RoleViewer, IP: ip}
}

// DevIdentity is what every request resolves to when security.disable_auth is
// on. Config validation refuses that setting unless the listener is on
// loopback, and it produces a startup warning, so this cannot be reached by
// accident on a real deployment.
func DevIdentity(ip string) *Identity {
	return &Identity{Kind: KindUser, ID: "dev", Name: "auth-disabled", Role: store.RoleAdmin, IP: ip}
}

// Service is the package's entry point: it owns the store handle, the security
// configuration and the login rate limiter.
type Service struct {
	store  *store.Store
	cfg    config.Security
	events *events.Bus
	logger *slog.Logger
	clock  func() time.Time

	logins *RateLimiter
	audit  *Auditor

	// bootstrapMu serialises CreateFirstAdmin so two simultaneous requests
	// cannot both pass the "no users exist" check.
	bootstrapMu sync.Mutex
	// touched remembers when each token's last_used_at was last written.
	touched sync.Map // token ID -> time.Time
}

// Option customises a Service at construction.
type Option func(*Service)

// WithClock overrides the clock. Tests use it to expire sessions and tokens
// without sleeping.
func WithClock(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.clock = fn
		}
	}
}

// WithLogger sets the logger; the default is slog's.
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// New builds the auth service. bus may be nil, in which case audit events are
// written to the database but not streamed to the UI.
func New(st *store.Store, cfg *config.Config, bus *events.Bus, opts ...Option) *Service {
	if cfg == nil {
		cfg = config.Default()
	}
	s := &Service{
		store:  st,
		cfg:    cfg.Security,
		events: bus,
		logger: slog.Default(),
		clock:  time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	s.logins = NewRateLimiter(s.cfg.RateLimitLogins, time.Minute, s.clock)
	s.audit = NewAuditor(st, bus, s.logger)
	return s
}

// Auditor returns the audit recorder, so callers do not each build their own.
func (s *Service) Auditor() *Auditor { return s.audit }

// Now returns the service clock, always in UTC.
func (s *Service) Now() time.Time { return s.clock().UTC() }

// SessionTTL is how long a new browser session lasts.
func (s *Service) SessionTTL() time.Duration {
	if s.cfg.SessionTTL > 0 {
		return s.cfg.SessionTTL
	}
	return 7 * 24 * time.Hour
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

// NeedsBootstrap reports whether this instance has no accounts yet. The login
// page calls it so a fresh install shows "create the first administrator"
// instead of a form nobody can submit.
func (s *Service) NeedsBootstrap(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("counting accounts: %w", err)
	}
	return n == 0, nil
}

// CreateFirstAdmin creates the initial administrator.
//
// This is reachable without authentication -- it has to be, or a fresh install
// could never be used -- so the "no user exists" check below is the whole
// security of it. It must stay the first thing this function does, it must run
// under bootstrapMu so two simultaneous requests cannot both pass it, and it
// must never be relaxed into "no admin exists": that would let anyone claim
// admin on an instance that already has viewers.
func (s *Service) CreateFirstAdmin(ctx context.Context, username, password string) (*store.User, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()

	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting accounts: %w", err)
	}
	if n > 0 {
		return nil, ErrAlreadyBootstrapped
	}
	return s.createUser(ctx, NewUser{
		Username: username,
		Password: password,
		Role:     store.RoleAdmin,
	})
}

// ---------------------------------------------------------------------------
// Password login
// ---------------------------------------------------------------------------

// Login verifies a password and returns the user together with the plaintext
// session token the caller should set as a cookie. The token is not stored: the
// database holds only its SHA-256, so a database leak does not hand over live
// sessions.
func (s *Service) Login(ctx context.Context, username, password, ip, ua string) (*store.User, string, error) {
	if !s.logins.Allow(ip) {
		s.logger.Warn("login rate limit hit", "ip", ip, "username", username)
		return nil, "", ErrRateLimited
	}

	u, err := s.store.GetUserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		// Burn the same CPU a real verification would, so response timing does
		// not tell an attacker which usernames exist.
		cryptox.DummyVerify(password)
		return nil, "", ErrInvalidCredentials
	}
	if err != nil {
		return nil, "", fmt.Errorf("looking up account: %w", err)
	}
	if u.PasswordHash == "" {
		cryptox.DummyVerify(password)
		return nil, "", ErrSSOOnly
	}
	if u.Disabled {
		cryptox.DummyVerify(password)
		return nil, "", ErrAccountDisabled
	}
	if !cryptox.VerifyPassword(password, u.PasswordHash) {
		return nil, "", ErrInvalidCredentials
	}

	token, err := s.NewSession(ctx, u, ip, ua)
	if err != nil {
		return nil, "", err
	}
	now := s.Now()
	if err := s.store.TouchLogin(ctx, u.ID, now); err != nil {
		// A missing last-login stamp is cosmetic; refusing the login would not
		// be. Log it and let the person in.
		s.logger.Warn("could not record last login", "user", u.Username, "error", err)
	}
	u.LastLoginAt = &now
	// The attempts that got here were legitimate, so they should not count
	// against the next person behind the same NAT.
	s.logins.Reset(ip)
	return u, token, nil
}

// NewSession mints a browser session for a user who has already been
// authenticated by some other means -- a password, an OIDC callback, or a
// password change that invalidated the old cookie. It returns the plaintext
// token exactly once.
func (s *Service) NewSession(ctx context.Context, u *store.User, ip, ua string) (string, error) {
	token := store.NewSecret(32)
	now := s.Now()
	sess := &store.Session{
		UserID:    u.ID,
		TokenHash: cryptox.HashToken(token),
		UserAgent: truncate(ua, 512),
		IP:        ip,
		ExpiresAt: now.Add(s.SessionTTL()),
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	return token, nil
}

// Logout deletes the session behind a cookie value. Logging out a cookie that
// is already gone is not an error: the caller wanted to be signed out, and it is.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, cryptox.HashToken(token)); err != nil {
		return fmt.Errorf("ending session: %w", err)
	}
	return nil
}

// LogoutAll ends every session belonging to a user.
func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	if err := s.store.DeleteUserSessions(ctx, userID); err != nil {
		return fmt.Errorf("ending sessions for %s: %w", userID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Request authentication
// ---------------------------------------------------------------------------

// AuthInput is what the API layer extracts from a request. Keeping it a struct
// rather than an *http.Request is what stops this package from depending on the
// shape of the transport.
type AuthInput struct {
	// SessionCookie is the raw cookie value, empty when there is no cookie.
	SessionCookie string
	// Authorization is the raw Authorization header, empty when there is none.
	Authorization string
	// IP is the client address, already resolved through any trusted proxy.
	IP string
}

// Authenticate resolves a request to an identity.
//
// A bearer token wins over a session cookie: a browser that is signed in but is
// deliberately calling the API with a token should get the token's role, not
// its own. Every failure is one of the sentinels above so handlers can tell
// "expired, sign in again" apart from "no credentials at all".
func (s *Service) Authenticate(ctx context.Context, r AuthInput) (*Identity, error) {
	if s.cfg.DisableAuth {
		return DevIdentity(r.IP), nil
	}
	if tok := bearerToken(r.Authorization); tok != "" {
		return s.authenticateToken(ctx, tok, r.IP)
	}
	if r.SessionCookie != "" {
		return s.authenticateSession(ctx, r.SessionCookie, r.IP)
	}
	return nil, ErrNoCredentials
}

// AuthenticateAgent resolves an agent's own token to its host. Agent
// credentials are a separate class: they never carry a role and are only
// accepted on the agent routes.
func (s *Service) AuthenticateAgent(ctx context.Context, authorization string) (*store.Host, error) {
	tok := bearerToken(authorization)
	if tok == "" {
		return nil, ErrNoCredentials
	}
	h, err := s.store.FindHostByTokenHash(ctx, cryptox.HashToken(tok))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("looking up agent: %w", err)
	}
	return h, nil
}

func (s *Service) authenticateToken(ctx context.Context, token, ip string) (*Identity, error) {
	// Credentials are prefixed for exactly this: an operator who pastes the
	// wrong one gets told which one they pasted instead of "unauthorized".
	switch {
	case strings.HasPrefix(token, AgentTokenPrefix):
		return nil, errors.New("that is an agent token; it is only accepted on /api/v1/agent/*, not on the user API")
	case strings.HasPrefix(token, JoinTokenPrefix):
		return nil, errors.New("that is a join token; it enrols an agent with `zoomies agent join` and cannot be used to call the API")
	}
	t, err := s.store.GetAPITokenByHash(ctx, cryptox.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("looking up API token: %w", err)
	}
	if t.Revoked {
		return nil, ErrTokenRevoked
	}
	now := s.Now()
	if t.Expired(now) {
		return nil, ErrTokenExpired
	}
	s.touch(ctx, t.ID, now)
	return &Identity{
		Kind:    KindToken,
		ID:      t.ID,
		Name:    t.Name,
		Role:    t.Role,
		Scopes:  t.Scopes,
		TokenID: t.ID,
		IP:      ip,
	}, nil
}

func (s *Service) authenticateSession(ctx context.Context, cookie, ip string) (*Identity, error) {
	sess, u, err := s.store.GetSessionByTokenHash(ctx, cryptox.HashToken(cookie))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrSessionExpired
	}
	if err != nil {
		return nil, fmt.Errorf("looking up session: %w", err)
	}
	if !s.Now().Before(sess.ExpiresAt) {
		// Clean up on the way past; the retention job would get there
		// eventually, but a browser holding a dead cookie will keep asking.
		if err := s.store.DeleteSession(ctx, sess.TokenHash); err != nil {
			s.logger.Warn("could not delete expired session", "session", sess.ID, "error", err)
		}
		return nil, ErrSessionExpired
	}
	if u.Disabled {
		return nil, ErrAccountDisabled
	}
	return &Identity{Kind: KindUser, ID: u.ID, Name: u.Username, Role: u.Role, IP: ip}, nil
}

// touch records that a token was used, at most once per touchInterval.
func (s *Service) touch(ctx context.Context, tokenID string, now time.Time) {
	if prev, ok := s.touched.Load(tokenID); ok {
		if last, ok := prev.(time.Time); ok && now.Sub(last) < touchInterval {
			return
		}
	}
	s.touched.Store(tokenID, now)
	if err := s.store.TouchAPIToken(ctx, tokenID, now); err != nil {
		s.logger.Warn("could not record token use", "token", tokenID, "error", err)
	}
}

// bearerToken extracts the credential from an Authorization header. The scheme
// is compared case-insensitively because HTTP says it is case-insensitive and
// some clients send "bearer".
func bearerToken(header string) string {
	scheme, value, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// ---------------------------------------------------------------------------
// Passwords
// ---------------------------------------------------------------------------

// ChangePassword sets a new password for the caller's own account.
//
// The old password is required except when the account is flagged
// MustChangePassword, which is how the installer's bootstrap admin gets past
// the password it was handed on the command line. Every session for the account
// is ended, so a stolen cookie does not survive the change; the caller should
// issue a fresh session with NewSession for the browser that made the request.
func (s *Service) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string) error {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if u.PasswordHash == "" && !u.MustChangePassword {
		return ErrSSOOnly
	}
	if !u.MustChangePassword {
		if !cryptox.VerifyPassword(oldPassword, u.PasswordHash) {
			return errors.New("the current password is not correct")
		}
	}
	if err := CheckPassword(newPassword); err != nil {
		return err
	}
	hash, err := cryptox.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.SetPassword(ctx, u.ID, hash); err != nil {
		return fmt.Errorf("saving password: %w", err)
	}
	if err := s.store.DeleteUserSessions(ctx, u.ID); err != nil {
		s.logger.Warn("could not end existing sessions after password change",
			"user", u.Username, "error", err)
	}
	return nil
}

// CheckPassword enforces the one password rule the product has.
func CheckPassword(p string) error {
	if len([]rune(p)) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// NewUser describes an account to create. Password may be empty for an account
// that will sign in through OIDC.
type NewUser struct {
	Username           string
	Password           string
	Email              string
	DisplayName        string
	Role               store.Role
	OIDCSubject        string
	MustChangePassword bool
}

// CreateUser adds an account.
func (s *Service) CreateUser(ctx context.Context, in NewUser) (*store.User, error) {
	return s.createUser(ctx, in)
}

func (s *Service) createUser(ctx context.Context, in NewUser) (*store.User, error) {
	username, err := normalizeUsername(in.Username)
	if err != nil {
		return nil, err
	}
	if in.Role == "" {
		in.Role = store.RoleViewer
	}
	if !in.Role.Valid() {
		return nil, fmt.Errorf("%q is not a role; use viewer, operator or admin", in.Role)
	}
	var hash string
	if in.Password != "" {
		if err := CheckPassword(in.Password); err != nil {
			return nil, err
		}
		if hash, err = cryptox.HashPassword(in.Password); err != nil {
			return nil, err
		}
	} else if in.OIDCSubject == "" {
		return nil, errors.New("an account needs either a password or an OIDC subject; give a password, or enable single sign-on")
	}

	u := &store.User{
		Username:           username,
		Email:              strings.TrimSpace(in.Email),
		DisplayName:        strings.TrimSpace(in.DisplayName),
		Role:               in.Role,
		PasswordHash:       hash,
		OIDCSubject:        in.OIDCSubject,
		MustChangePassword: in.MustChangePassword,
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, fmt.Errorf("an account named %q already exists", username)
		}
		return nil, fmt.Errorf("creating account %q: %w", username, err)
	}
	return u, nil
}

// UpdateUser saves profile, role and disabled changes, refusing any change that
// would leave the instance without an administrator.
func (s *Service) UpdateUser(ctx context.Context, u *store.User) error {
	existing, err := s.store.GetUser(ctx, u.ID)
	if err != nil {
		return err
	}
	if !u.Role.Valid() {
		return fmt.Errorf("%q is not a role; use viewer, operator or admin", u.Role)
	}
	if err := s.ensureAdminRemains(ctx, existing, u.Role, u.Disabled); err != nil {
		return err
	}
	// The password hash is not part of a profile update; it moves only through
	// ChangePassword and ResetPassword, so a caller cannot blank it by mistake.
	u.PasswordHash = existing.PasswordHash
	if err := s.store.UpdateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("an account named %q already exists", u.Username)
		}
		return err
	}
	return nil
}

// SetUserDisabled enables or disables an account.
func (s *Service) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Disabled == disabled {
		return nil
	}
	if err := s.ensureAdminRemains(ctx, u, u.Role, disabled); err != nil {
		return err
	}
	u.Disabled = disabled
	if err := s.store.UpdateUser(ctx, u); err != nil {
		return err
	}
	if disabled {
		// A disabled account must not keep a live cookie.
		if err := s.store.DeleteUserSessions(ctx, id); err != nil {
			s.logger.Warn("could not end sessions for disabled account", "user", u.Username, "error", err)
		}
	}
	return nil
}

// DeleteUser removes an account, refusing to remove the last administrator.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ensureAdminRemains(ctx, u, store.RoleViewer, true); err != nil {
		return err
	}
	return s.store.DeleteUser(ctx, id)
}

// ResetPassword is the administrator's reset: it sets a password without
// knowing the old one and flags the account so the owner must change it at next
// login. Every existing session for that account is ended.
func (s *Service) ResetPassword(ctx context.Context, userID, newPassword string) error {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if err := CheckPassword(newPassword); err != nil {
		return err
	}
	hash, err := cryptox.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.SetPassword(ctx, u.ID, hash); err != nil {
		return fmt.Errorf("saving password: %w", err)
	}
	u.PasswordHash = hash
	u.MustChangePassword = true
	if err := s.store.UpdateUser(ctx, u); err != nil {
		return err
	}
	if err := s.store.DeleteUserSessions(ctx, u.ID); err != nil {
		s.logger.Warn("could not end existing sessions after password reset",
			"user", u.Username, "error", err)
	}
	return nil
}

// ensureAdminRemains refuses a change that would take away the last enabled
// admin. It is the invariant behind "you cannot lock yourself out".
func (s *Service) ensureAdminRemains(ctx context.Context, existing *store.User, newRole store.Role, newDisabled bool) error {
	stillAdmin := newRole == store.RoleAdmin && !newDisabled
	wasAdmin := existing.Role == store.RoleAdmin && !existing.Disabled
	if !wasAdmin || stillAdmin {
		return nil
	}
	n, err := s.store.CountAdmins(ctx)
	if err != nil {
		return fmt.Errorf("counting administrators: %w", err)
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

// NewToken describes an API token to mint.
type NewToken struct {
	Name string
	Role store.Role
	// UserID attributes the token to the account that created it, so revoking
	// a person's access can find their tokens.
	UserID string
	// Scopes optionally narrows the token below its role. Empty means the role
	// alone decides.
	Scopes []string
	// ExpiresAt is when the token stops working. Nil never expires, which the
	// UI warns about but does not forbid.
	ExpiresAt *time.Time
}

// CreateAPIToken mints an API token and returns the plaintext exactly once: the
// database keeps only its SHA-256, so nothing -- not the UI, not a support
// bundle, not a database dump -- can show it again.
//
// The format is "zoo_<8 chars of the token's ID>_<32 chars of entropy>". The
// middle field is stored as Prefix so the UI can name the token in a list and
// an operator can match a leaked string to a row without holding the secret.
func (s *Service) CreateAPIToken(ctx context.Context, in NewToken) (*store.APIToken, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", errors.New("a token needs a name; it is how you will recognise it later")
	}
	if in.Role == "" {
		in.Role = store.RoleViewer
	}
	if !in.Role.Valid() {
		return nil, "", fmt.Errorf("%q is not a role; use viewer, operator or admin", in.Role)
	}
	if err := ValidateScopes(in.Scopes); err != nil {
		return nil, "", err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.Now()) {
		return nil, "", errors.New("the expiry date is in the past; leave it empty for a token that never expires")
	}

	id := store.NewID(store.PrefixToken)
	prefix := APITokenPrefix + idFragment(id)
	plaintext := prefix + "_" + store.NewSecret(secretBytes)

	t := &store.APIToken{
		ID:        id,
		Name:      name,
		Role:      in.Role,
		UserID:    in.UserID,
		Scopes:    store.StringSlice(normalizeScopes(in.Scopes)),
		TokenHash: cryptox.HashToken(plaintext),
		Prefix:    prefix,
		ExpiresAt: in.ExpiresAt,
	}
	if err := s.store.CreateAPIToken(ctx, t); err != nil {
		return nil, "", fmt.Errorf("creating API token: %w", err)
	}
	return t, plaintext, nil
}

// RevokeAPIToken disables a token while keeping its row, so the audit trail
// still resolves the actions it took.
func (s *Service) RevokeAPIToken(ctx context.Context, id string) error {
	return s.store.RevokeAPIToken(ctx, id)
}

// ---------------------------------------------------------------------------
// Join tokens and agent credentials
// ---------------------------------------------------------------------------

// DefaultJoinTTL is how long a join token lasts when the caller does not say.
// It is short because a join token is pasted into a terminal within a minute of
// being created.
const DefaultJoinTTL = time.Hour

// CreateJoinToken mints the single-use credential that lets a new agent enrol.
// Like an API token, the plaintext is returned exactly once.
func (s *Service) CreateJoinToken(ctx context.Context, ttl time.Duration, labels map[string]string, capacity int, createdBy string) (*store.JoinToken, string, error) {
	if ttl <= 0 {
		ttl = DefaultJoinTTL
	}
	if capacity < 0 {
		return nil, "", errors.New("capacity cannot be negative; leave it at 0 to let the agent decide from its CPU count")
	}
	id := store.NewID(store.PrefixJoin)
	prefix := JoinTokenPrefix + idFragment(id)
	plaintext := prefix + "_" + store.NewSecret(secretBytes)

	t := &store.JoinToken{
		ID:        id,
		TokenHash: cryptox.HashToken(plaintext),
		Prefix:    prefix,
		CreatedBy: createdBy,
		Labels:    store.StringMap(labels),
		Capacity:  capacity,
		ExpiresAt: s.Now().Add(ttl),
	}
	if err := s.store.CreateJoinToken(ctx, t); err != nil {
		return nil, "", fmt.Errorf("creating join token: %w", err)
	}
	return t, plaintext, nil
}

// RedeemJoinToken spends a join token for a host. It is single-use: the store
// marks it spent in the same transaction that reads it, so two agents racing
// with the same token cannot both enrol.
func (s *Service) RedeemJoinToken(ctx context.Context, token, hostID string) (*store.JoinToken, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("no join token supplied; create one with `zoomies hosts join-token create`")
	}
	t, err := s.store.RedeemJoinToken(ctx, cryptox.HashToken(token), hostID, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("this join token is not valid; create a new one with `zoomies hosts join-token create`")
	}
	return t, err
}

// NewAgentToken mints the long-lived credential an agent gets when it enrols,
// returning the plaintext and the hash to store. The controller shows the
// plaintext to the agent once, in the join response.
func NewAgentToken() (plaintext, hash string) {
	plaintext = AgentTokenPrefix + store.NewSecret(secretBytes)
	return plaintext, cryptox.HashToken(plaintext)
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// idFragment returns the first 8 characters of an ID's random part, which is
// what a credential's visible prefix is built from.
func idFragment(id string) string {
	_, rest, ok := strings.Cut(id, "_")
	if !ok {
		rest = id
	}
	if len(rest) > 8 {
		rest = rest[:8]
	}
	return rest
}

// normalizeUsername lowercases and checks a username. The character set is
// restricted so a username cannot be confused with a path segment or an email
// header when it turns up in a URL or a log line.
func normalizeUsername(in string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(in))
	if u == "" {
		return "", errors.New("username is required")
	}
	if len(u) > 64 {
		return "", errors.New("username must be 64 characters or fewer")
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '@' || r == '+':
		default:
			return "", fmt.Errorf("username %q contains %q; use letters, digits and . - _ @ +", in, string(r))
		}
	}
	return u, nil
}

// normalizeScopes lowercases, trims and de-duplicates a scope list.
func normalizeScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
