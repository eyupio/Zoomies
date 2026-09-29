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
	"crypto/sha256"
	"encoding/hex"
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

// The per-account login limit is derived from security.rate_limit_logins rather
// than configured separately: one number for "how hard may somebody guess" is
// enough, and a second knob would mostly be set wrong. The factor and window
// make it deliberately slack -- a legitimate person fumbling their password on
// three devices must never trip it, and anyone can trip somebody else's counter
// by trying, so the cost of tripping it has to stay small and self-healing.
const (
	accountLimitFactor = 5
	accountLimitWindow = 15 * time.Minute
)

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
	// ErrTokenOwnerDisabled means the token is valid but the account it was
	// issued to has been disabled.
	ErrTokenOwnerDisabled = errors.New("the account this API token belongs to is disabled; the token cannot be used")
	// ErrTokenOrphaned means the account the token was issued to no longer
	// exists. It is a separate sentence from the one above because it is a
	// different thing to have to fix.
	ErrTokenOrphaned = errors.New("the account this API token belonged to has been deleted; the token cannot be used")
	// ErrAccountDisabled means the account exists but has been switched off.
	ErrAccountDisabled = errors.New("this account is disabled; ask an administrator to re-enable it")
	// ErrSSOOnly means the account has no password because it comes from the
	// identity provider.
	ErrSSOOnly = errors.New("this account signs in with single sign-on; use the SSO button on the login page instead of a password")
	// ErrRateLimited means too many login attempts came from one address.
	ErrRateLimited = errors.New("too many login attempts from this address; wait a minute and try again")
	// ErrBadSetupToken means the bootstrap request carried no setup token, or
	// the wrong one. The right one is printed in this controller's log. It is
	// ErrInvalidInput because it is the caller's to fix, not the controller's.
	ErrBadSetupToken error = &refusal{
		kind: ErrInvalidInput,
		msg:  "that is not this controller's setup token; it is printed in the controller's log at startup, on a line beginning \"setup token\"",
	}
	// ErrAlreadyBootstrapped means the first-admin endpoint was called on an
	// instance that already has users.
	ErrAlreadyBootstrapped = errors.New("this instance already has an account, so the first-admin endpoint is closed; sign in, or have an administrator reset the password with `zoomies users passwd <user-id>`")
	// ErrLastAdmin means the change would leave nobody able to administer the
	// instance.
	ErrLastAdmin = errors.New("this is the last enabled administrator; give another account the admin role before changing this one")
	// ErrLastPlatform means the change would leave nobody able to operate
	// the process: no one to change a timer, lift the recovery fence or
	// restore a backup. An administrator cannot stand in, which is the
	// point of the role, so this is its own refusal.
	ErrLastPlatform = errors.New("this is the last enabled platform account; give another account the platform role before changing this one")
	// ErrPasswordTooShort is returned by every path that sets a password.
	ErrPasswordTooShort error = &refusal{kind: ErrInvalidInput, msg: fmt.Sprintf("password must be at least %d characters", MinPasswordLength)}
	// ErrWrongPassword means ChangePassword's caller gave the wrong current
	// password -- the one failure of theirs that is not an internal error, so
	// it needs its own sentinel rather than falling into a generic default.
	ErrWrongPassword error = &refusal{kind: ErrInvalidInput, msg: "the current password is not correct"}

	// ErrInvalidInput marks a refusal the caller can act on: a username with a
	// character it may not carry, a role that does not exist, a token without
	// a name, a join token that has been spent. The API answers one with a 422
	// carrying the message. Every other error this package returns is a
	// failure of the controller's own -- the database not answering, most
	// likely -- and is answered with a 500 and a request ID, so that a SQLite
	// error is never handed to an anonymous caller as the text of a
	// validation message.
	ErrInvalidInput = errors.New("invalid input")
)

// Identity is the authenticated caller: a person with a session, a token used
// by automation, an agent reporting on its runners, or the controller itself.
type Identity struct {
	// Kind is one of KindUser, KindToken, KindConnection, KindAgent or
	// KindSystem.
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
	// UserID is the account this identity acts for: the user's own ID for a
	// session, and the owner of the token for a token. It is what a token
	// minted by this identity is attributed to, so that disabling or deleting
	// the account still ends every credential that descends from it. Empty
	// for an agent, the controller itself, and a token that has no owner.
	UserID string
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
	case KindConnection:
		return "MCP connection"
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
// on. Config validation refuses that setting on anything that looks reachable
// -- a non-loopback bind, an external URL, or a trusted proxy -- and produces a
// startup warning on the loopback-with-nothing-in-front case that is left, so
// this cannot be reached by accident on a real deployment.
//
// It carries the platform role rather than the administrator one. With
// authentication off there is no second audience to hold anything back from:
// whoever reaches the socket has the process and everything in it, and a
// developer instance that answered 403 on its own backups would be teaching a
// distinction that does not exist there.
func DevIdentity(ip string) *Identity {
	return &Identity{Kind: KindUser, ID: "dev", Name: "auth-disabled", Role: store.RolePlatform, UserID: "dev", IP: ip}
}

// Service is the package's entry point: it owns the store handle, the security
// configuration and the login rate limiter.
type Service struct {
	store  *store.Store
	cfg    config.Security
	events *events.Bus
	logger *slog.Logger
	clock  func() time.Time

	// setupToken gates the unauthenticated bootstrap route. It is per process
	// and never persisted: a restart mints a new one and prints it again,
	// which is the behaviour an operator who has lost the first one wants.
	setupToken string

	logins *RateLimiter
	// accountLogins is the same counter keyed on the username instead of the
	// address, so a distributed attempt against one account is still bounded.
	accountLogins *RateLimiter
	// joins bounds agent enrolment, which is the other unauthenticated route
	// that takes a credential and says whether it was right. Without it a join
	// token is guarded only by its own entropy.
	//
	// A counter of its own rather than the login one, at the same setting: an
	// attacker hammering /agent/join from an address administrators also sign
	// in from would otherwise lock them out of the UI, which turns a bounded
	// guessing attempt into a denial of service on the thing that fixes it.
	joins *RateLimiter
	// registrations bounds MCP client registration, the one route that
	// writes a row for anybody who asks; tokens bounds the token endpoint.
	registrations *RateLimiter
	tokens        *RateLimiter
	// metadata fetches client ID metadata documents.
	metadata *metadataFetcher
	audit    *Auditor
	// key seals authenticator secrets. Nil leaves two-step verification
	// unable to start, which it says, rather than storing a secret in the
	// clear.
	key *cryptox.Key

	// bootstrapMu serialises CreateFirstAdmin so two simultaneous requests
	// cannot both pass the "no users exist" check.
	bootstrapMu sync.Mutex
	// The last-admin check and the account write are one decision. The
	// store's writer lock serialises statements, not the reads before them.
	adminMu sync.Mutex
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
	s.metadata = newMetadataFetcher()
	for _, o := range opts {
		o(s)
	}
	s.setupToken = store.NewSecret(secretBytes)
	s.logins = NewRateLimiter(s.cfg.RateLimitLogins, time.Minute, s.clock)
	s.accountLogins = NewRateLimiter(s.cfg.RateLimitLogins*accountLimitFactor, accountLimitWindow, s.clock)
	s.joins = NewRateLimiter(s.cfg.RateLimitLogins, time.Minute, s.clock)
	// Claude registers afresh on every new connection, so a person connecting
	// a few surfaces in an afternoon is well inside this; a script filling the
	// clients table is not.
	s.registrations = NewRateLimiter(mcpRegistrationsPerHour, time.Hour, s.clock)
	s.tokens = NewRateLimiter(mcpTokenRequestsPerMinute, time.Minute, s.clock)
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

// SetupToken is the one-time credential the unauthenticated bootstrap route
// asks for. It is minted per process, held only in memory, and printed at
// startup while the instance has no accounts; a restart mints a new one.
//
// It exists because "no user exists yet" is a condition an attacker can also
// satisfy. A fresh controller published to the internet -- which is what the
// reference compose file does -- would otherwise hand administrator rights to
// whoever loads the page first, and the operator would have no way to tell.
// Holding the log is the proof of ownership that the empty database is not.
func (s *Service) SetupToken() string { return s.setupToken }

// refusal is an error with a message written for the caller and a kind the
// API switches on. The kind is what errors.Is answers to, so a refusal reads
// as its message and matches as its sentinel.
type refusal struct {
	kind error
	msg  string
}

func (r *refusal) Error() string        { return r.msg }
func (r *refusal) Is(target error) bool { return target == r.kind }

// Invalid builds an error that is ErrInvalidInput and reads as the message.
// It is exported for the controller's join path, which refuses an agent for
// reasons of the same kind -- a protocol mismatch, a nameless host -- and
// wants the API to answer them the same way.
func Invalid(format string, args ...any) error {
	return &refusal{kind: ErrInvalidInput, msg: fmt.Sprintf(format, args...)}
}

// conflict builds an error that is store.ErrConflict and reads as the message.
func conflict(format string, args ...any) error {
	return &refusal{kind: store.ErrConflict, msg: fmt.Sprintf(format, args...)}
}

// CreateFirstAdmin creates the initial administrator.
//
// It is the trusted, local path: `zoomies init` and the installer call it,
// having already established that whoever is running them is on the console.
// Anything reachable over the network must use CreateFirstAdminWithSetupToken
// instead, which is the same thing plus proof that the caller can read this
// process's log.
//
// The "no user exists" check is the other half of the security here. It must
// stay the first thing this function does, it must run under bootstrapMu so two
// simultaneous requests cannot both pass it, and it must never be relaxed into
// "no admin exists": that would let anyone claim admin on an instance that
// already has viewers.
func (s *Service) CreateFirstAdmin(ctx context.Context, username, password string) (*store.User, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	return s.createFirstAdmin(ctx, username, password)
}

// CreateFirstAdminWithSetupToken is CreateFirstAdmin for a caller who arrived
// over the network. The token is checked inside the same lock, so a wrong one
// cannot race a right one.
func (s *Service) CreateFirstAdminWithSetupToken(ctx context.Context, username, password, setupToken string) (*store.User, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()

	if !cryptox.ConstantTimeEqual(s.setupToken, strings.TrimSpace(setupToken)) {
		return nil, ErrBadSetupToken
	}
	return s.createFirstAdmin(ctx, username, password)
}

func (s *Service) createFirstAdmin(ctx context.Context, username, password string) (*store.User, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting accounts: %w", err)
	}
	if n > 0 {
		return nil, ErrAlreadyBootstrapped
	}
	// The first account is the platform one. Whoever reaches this route can
	// read the setup token out of the controller's log, or is running the
	// installer on the host itself -- which is to say they already operate
	// the process, and the role is the one that says so. On an instance
	// where one team does both it changes nothing they can see.
	return s.createUser(ctx, NewUser{
		Username: username,
		Password: password,
		Role:     store.RolePlatform,
	})
}

// How the first identity was made, as the auth.bootstrap audit row records
// it. The row answers "who bootstrapped this instance, and how" on every
// path, because an unexplained platform account is exactly the thing an
// operator inheriting an instance needs to be able to account for.
const (
	BootstrapSetupToken  = "setup_token"
	BootstrapAnswerFile  = "answer_file"
	BootstrapInstaller   = "installer_prompt"
	BootstrapEnvPassword = "environment_password"
	BootstrapEnvToken    = "environment_token"
)

// AuditBootstrap writes the auth.bootstrap row for a first identity. actor is
// whoever made it: the person at the first-run page, or the system for the
// installer and the environment, where nobody has signed in to be named.
func (s *Service) AuditBootstrap(ctx context.Context, actor *Identity, u *store.User, method, tokenID string) {
	detail := map[string]any{"username": u.Username, "role": u.Role, "method": method}
	if tokenID != "" {
		detail["token_id"] = tokenID
	}
	_ = s.audit.Record(ctx, actor, "auth.bootstrap", "auth", u.ID, nil, detail)
}

// MinBootstrapTokenLength is the shortest token ZOOMIES_BOOTSTRAP_TOKEN_FILE
// may hold. It matches the entropy a minted token carries, so a provisioner's
// token is no easier to guess than one this service would have made.
const MinBootstrapTokenLength = 32

// Unattended is the first identity a provisioner asks for through the
// environment: a username with exactly one of a password or an API token.
type Unattended struct {
	Username string
	Password string
	Token    string
}

// CreateUnattendedIdentity makes the first identity on an empty database, as
// platform, and audits it with the system as the actor.
//
// It is the trusted path CreateFirstAdmin is -- whoever sets a process's
// environment already operates it -- and it takes the same lock and the same
// "no account exists" check, so it can never add a second platform account to
// an instance somebody has already claimed. ErrAlreadyBootstrapped is the
// answer once one exists, which the caller turns into a warning.
//
// The token variant registers the token the provisioner supplied rather than
// minting one. A minted token would have to be printed or written somewhere
// for the provisioner to find, which is the log scraping this exists to
// remove; a supplied one is already where the provisioner needs it. The
// account it belongs to has no password, so it cannot sign in to the UI at
// all: it is automation's identity, and a person gets their own.
func (s *Service) CreateUnattendedIdentity(ctx context.Context, in Unattended) (*store.User, *store.APIToken, error) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()

	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("counting accounts: %w", err)
	}
	if n > 0 {
		return nil, nil, ErrAlreadyBootstrapped
	}

	if in.Token == "" {
		u, err := s.createUser(ctx, NewUser{Username: in.Username, Password: in.Password, Role: store.RolePlatform})
		if err != nil {
			return nil, nil, err
		}
		s.AuditBootstrap(ctx, SystemIdentity(), u, BootstrapEnvPassword, "")
		return u, nil, nil
	}

	token := strings.TrimSpace(in.Token)
	switch {
	case len(token) < MinBootstrapTokenLength:
		return nil, nil, Invalid("the bootstrap token is %d characters; it needs at least %d, for example `openssl rand -hex 32`",
			len(token), MinBootstrapTokenLength)
	case strings.ContainsAny(token, " \t\r\n"):
		return nil, nil, Invalid("the bootstrap token contains whitespace; it is sent in an Authorization header, which cannot carry it")
	case strings.HasPrefix(token, AgentTokenPrefix), strings.HasPrefix(token, JoinTokenPrefix):
		// Authentication reads those prefixes as another kind of credential
		// and refuses them before looking the token up.
		return nil, nil, Invalid("the bootstrap token starts with an agent or join token prefix and would never be accepted on the API")
	}
	username, err := normalizeUsername(in.Username)
	if err != nil {
		return nil, nil, err
	}
	u := &store.User{Username: username, Role: store.RolePlatform}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, nil, fmt.Errorf("creating account %q: %w", username, err)
	}
	id := store.NewID(store.PrefixToken)
	t := &store.APIToken{
		ID:        id,
		Name:      "bootstrap",
		Role:      store.RolePlatform,
		OwnerRole: store.RolePlatform,
		UserID:    u.ID,
		TokenHash: cryptox.HashToken(token),
		Prefix:    APITokenPrefix + idFragment(id),
	}
	if err := s.store.CreateAPIToken(ctx, t); err != nil {
		// An account with no password and no token is one nobody can ever
		// use, and it would close bootstrap for good. Take it back out, so
		// the next start -- or the setup token -- can try again.
		if derr := s.store.DeleteUser(ctx, u.ID); derr != nil {
			s.logger.Error("could not remove the half-made bootstrap account", "user_id", u.ID, "error", derr)
		}
		return nil, nil, fmt.Errorf("registering the bootstrap token: %w", err)
	}
	s.AuditBootstrap(ctx, SystemIdentity(), u, BootstrapEnvToken, t.ID)
	return u, t, nil
}

// ---------------------------------------------------------------------------
// Password login
// ---------------------------------------------------------------------------

// LoginRetryAfter says how long an address refused by the login rate limit has
// to wait, so the 429 can carry a Retry-After a client can act on rather than
// the promise of one.
func (s *Service) LoginRetryAfter(ip string) time.Duration {
	return s.logins.RetryAfter(ip)
}

// AllowJoin reports whether another enrolment attempt from this address is
// within the rate limit, and records it.
//
// Enrolment is unauthenticated and answers whether the join token was right,
// which makes it guessable in exactly the way a sign-in is. Legitimate joins
// are rare -- once per machine -- so a limit that never troubles an operator
// still costs an attacker every attempt.
func (s *Service) AllowJoin(ip string) bool { return s.joins.Allow(ip) }

// JoinRetryAfter says how long a refused address has to wait, so the 429 can
// carry a Retry-After the agent's own backoff can use.
func (s *Service) JoinRetryAfter(ip string) time.Duration { return s.joins.RetryAfter(ip) }

// AllowSSOStart reports whether another single sign-on start from this address
// is within the rate limit, and records it. The login limiter is shared:
// /auth/oidc/start mints a fresh entry in the bounded state cache on every
// call, so an unrated endpoint lets one address lock every real SSO sign-in
// out with ErrTooManyPendingSignIns until states expire.
func (s *Service) AllowSSOStart(ip string) bool { return s.logins.Allow(ip) }

// SSOStartRetryAfter is the Retry-After the 429 promises.
func (s *Service) SSOStartRetryAfter(ip string) time.Duration { return s.logins.RetryAfter(ip) }

// Login verifies a password and returns the user together with the plaintext
// session token the caller should set as a cookie. The token is not stored: the
// database holds only its SHA-256, so a database leak does not hand over live
// sessions.
//
// Nothing this function returns distinguishes an account that exists from one
// that does not until the caller has proved they hold its password. "Disabled"
// and "signs in with SSO" are useful things to be told, but only once you have
// shown the account is yours; before that they are an enumeration oracle, and
// an SSO-only account -- which has no password to prove anything with -- is
// simply "incorrect username or password". The reason is logged and audited
// either way, so an operator diagnosing a failed sign-in still has it.
// Each Argon2 verification consumes 64 MiB; IP/account windows alone cannot
// bound the memory used by requests spread across distinct identities.
var passwordChecks = make(chan struct{}, 2)

func (s *Service) Login(ctx context.Context, username, password, ip, ua string) (*store.User, string, error) {
	select {
	case passwordChecks <- struct{}{}:
		defer func() { <-passwordChecks }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	default:
		return nil, "", ErrRateLimited
	}

	account := normalizeForLimiter(username)
	if !s.logins.Allow(ip) {
		s.logger.Warn("login rate limit hit", "ip", ip, "username", username)
		return nil, "", ErrRateLimited
	}
	// A second window, keyed on the account rather than the address, so that an
	// attacker with a pool of addresses does not get an unbounded budget against
	// one person. It is deliberately far looser than the per-address limit: a
	// counter that a stranger can trip is a counter that locks the owner out,
	// so this one only catches sustained guessing, and it heals on its own.
	if account != "" && !s.accountLogins.Allow(account) {
		s.logger.Warn("login rate limit hit for an account", "ip", ip, "username", username)
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
		s.logger.Info("a password sign-in was attempted for an account that has none",
			"user", u.Username, "ip", ip, "reason", ErrSSOOnly)
		return nil, "", ErrInvalidCredentials
	}
	if !cryptox.VerifyPassword(password, u.PasswordHash) {
		return nil, "", ErrInvalidCredentials
	}
	// The password was right, so the caller owns this account and can be told
	// why it still cannot sign in.
	if u.Disabled {
		return nil, "", ErrAccountDisabled
	}

	// A second step still to take is returned as an error, so a caller
	// that does not know about it signs nobody in. The limiters are not reset
	// yet either: the sign-in has not succeeded, and the code attempts that
	// follow are charged to the same counters.
	pending, err := s.secondStep(ctx, u, ip, ua)
	if err != nil {
		return nil, "", err
	}
	if pending != nil {
		return nil, "", pending
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
	// against the next person behind the same NAT, nor against this account.
	s.logins.Reset(ip)
	s.accountLogins.Reset(account)
	return u, token, nil
}

// AuditLoginFailure records a refused sign-in.
//
// It lives here rather than in the HTTP handler because deciding what may be
// written down is an auth-package question. The submitted username is recorded
// only when it names an account that exists: an unrecognised one is far more
// often a password typed into the wrong field than a real attempt, and the
// audit log is readable by every viewer on the instance. What is left in its
// place is a short fingerprint, so a run of attempts against one bad value can
// still be correlated without storing the value.
func (s *Service) AuditLoginFailure(ctx context.Context, username, ip string, cause error) {
	shown := "[unrecognised:" + fingerprint(username) + "]"
	if u, err := s.store.GetUserByUsername(ctx, username); err == nil {
		shown = u.Username
	}
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	s.audit.Auth(ctx, &Identity{Kind: KindUser, Name: shown, IP: ip}, "auth.login_failed", map[string]any{
		"username": shown, "reason": reason,
	})
}

// fingerprint is a short, stable, one-way tag for a value that must not be
// stored. Eight hex characters is enough to correlate repeats and far too
// little to reverse.
func fingerprint(v string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(v))))
	return hex.EncodeToString(sum[:4])
}

// normalizeForLimiter renders a username as the per-account rate limiter's key.
// It uses the same normalisation the store looks accounts up by, so "Alice" and
// "alice " cannot be counted as two separate accounts.
func normalizeForLimiter(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
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
	case strings.HasPrefix(token, MCPAccessTokenPrefix), strings.HasPrefix(token, MCPRefreshTokenPrefix):
		return nil, errors.New("that is an MCP access token; it is accepted on /mcp only, for the connection it was approved for. " +
			"For the REST API, create an API token")
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
	// The owner still has to be somebody, and somebody still allowed in.
	//
	// A token carries its own role, so nothing above this line consults the
	// account it was issued to: disabling an account ended its sessions and
	// left its tokens answering with full authority, and deleting one left
	// them answering on behalf of a row that no longer exists -- api_tokens
	// has no foreign key to users, so the delete cascades nowhere.
	// SetUserDisabled and DeleteUser now revoke them, and this is the check
	// that does not depend on having remembered to.
	//
	// The same goes for the role. Demoting an account changed the row and
	// nothing it had already issued, so a demoted administrator's non-expiring
	// admin token kept managing users and minting successors. A token never
	// acts above its owner's current role, as an MCP connection does not. Not
	// roleCeiling: that caps at operator, and a still-admin owner must keep an
	// admin token. An owner whose role is unknown ranks nothing, so it fails
	// closed.
	role := t.Role
	if t.UserID != "" {
		u, err := s.store.GetUser(ctx, t.UserID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, ErrTokenOrphaned
		case err != nil:
			return nil, fmt.Errorf("looking up the owner of an API token: %w", err)
		case u.Disabled:
			return nil, ErrTokenOwnerDisabled
		}
		if !u.Role.AtLeast(t.Role) {
			role = u.Role
		}
	}
	s.touch(ctx, t.ID, now)
	return &Identity{
		Kind:    KindToken,
		ID:      t.ID,
		Name:    t.Name,
		Role:    role,
		Scopes:  t.Scopes,
		TokenID: t.ID,
		UserID:  t.UserID,
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
	return &Identity{Kind: KindUser, ID: u.ID, Name: u.Username, Role: u.Role, UserID: u.ID, IP: ip}, nil
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
			return ErrWrongPassword
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
	Username    string
	Password    string
	Email       string
	DisplayName string
	Role        store.Role
	OIDCSubject string
	// SSOOnly creates an account with no password and no subject yet: one an
	// administrator makes ahead of the person's first single sign-on, which
	// links it by username. The caller checks that SSO is actually on, since
	// an account nobody can ever sign in to is what this guards against.
	SSOOnly            bool
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
		return nil, Invalid("%q is not a role; use %s", in.Role, store.RoleList())
	}
	var hash string
	if in.Password != "" {
		if err := CheckPassword(in.Password); err != nil {
			return nil, err
		}
		if hash, err = cryptox.HashPassword(in.Password); err != nil {
			return nil, err
		}
	} else if in.OIDCSubject == "" && !in.SSOOnly {
		return nil, Invalid("an account needs either a password or an OIDC subject; give a password, or enable single sign-on")
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
			return nil, conflict("an account named %q already exists", username)
		}
		return nil, fmt.Errorf("creating account %q: %w", username, err)
	}
	return u, nil
}

// UpdateUser saves profile, role and disabled changes, refusing any change that
// would leave the instance without an administrator.
func (s *Service) UpdateUser(ctx context.Context, u *store.User) error {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	existing, err := s.store.GetUser(ctx, u.ID)
	if err != nil {
		return err
	}
	if !u.Role.Valid() {
		return Invalid("%q is not a role; use %s", u.Role, store.RoleList())
	}
	if err := s.ensureAdminRemains(ctx, existing, u.Role, u.Disabled); err != nil {
		return err
	}
	// The password hash is not part of a profile update; it moves only through
	// ChangePassword and ResetPassword, so a caller cannot blank it by mistake.
	u.PasswordHash = existing.PasswordHash
	if err := s.store.UpdateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return conflict("an account named %q already exists", u.Username)
		}
		return err
	}
	return nil
}

// SetUserDisabled enables or disables an account.
func (s *Service) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
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
		// Nor a live token. A token carries its own role and is not looked up
		// through the account, so this is the difference between disabling an
		// account and disabling the access it was given.
		if n, err := s.store.RevokeAPITokensForUser(ctx, id); err != nil {
			s.logger.Warn("could not revoke API tokens for disabled account", "user", u.Username, "error", err)
		} else if n > 0 {
			s.logger.Info("revoked API tokens for disabled account", "user", u.Username, "tokens", n)
		}
	}
	return nil
}

// DeleteUser removes an account, refusing to remove the last administrator.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ensureAdminRemains(ctx, u, store.RoleViewer, true); err != nil {
		return err
	}
	// Before the row goes, because api_tokens has no foreign key to users:
	// the delete cascades to sessions and to nothing else, so a token left
	// behind here is a working credential belonging to an account that no
	// longer exists. Revoked rather than deleted, so the audit trail still
	// says who had it.
	if n, err := s.store.RevokeAPITokensForUser(ctx, id); err != nil {
		// Refused rather than logged: carrying on would delete the account and
		// leave its credentials working, which is the failure this prevents.
		return fmt.Errorf("revoking the API tokens of %s before deleting the account: %w", u.Username, err)
	} else if n > 0 {
		s.logger.Info("revoked API tokens for deleted account", "user", u.Username, "tokens", n)
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
	if err := s.ensurePlatformRemains(ctx, existing, newRole, newDisabled); err != nil {
		return err
	}
	// "At least admin" rather than exactly admin: a platform account
	// outranks one, so an instance holding only that is still administered.
	stillAdmin := newRole.AtLeast(store.RoleAdmin) && !newDisabled
	wasAdmin := existing.Role.AtLeast(store.RoleAdmin) && !existing.Disabled
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

// ensurePlatformRemains refuses a change that would take away the last
// enabled platform account. Demoting one to administrator is a demotion in
// the only direction that matters here: the settings the process reads about
// itself, the fence and the backups all need a platform, and an instance
// with none is one only a shell can rescue.
func (s *Service) ensurePlatformRemains(ctx context.Context, existing *store.User, newRole store.Role, newDisabled bool) error {
	stillPlatform := newRole == store.RolePlatform && !newDisabled
	wasPlatform := existing.Role == store.RolePlatform && !existing.Disabled
	if !wasPlatform || stillPlatform {
		return nil
	}
	n, err := s.store.CountPlatform(ctx)
	if err != nil {
		return fmt.Errorf("counting platform accounts: %w", err)
	}
	if n <= 1 {
		return ErrLastPlatform
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
	// OwnerRole is the role of whoever is minting this token, recorded so a
	// platform account's own automation is not listed to, or revocable by,
	// the fleet whose instance it operates. Empty on a path with no caller --
	// the CLI on the host, a test -- which reads as "nobody's secret".
	OwnerRole store.Role
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
		return nil, "", Invalid("a token needs a name; it is how you will recognise it later")
	}
	if in.Role == "" {
		in.Role = store.RoleViewer
	}
	if !in.Role.Valid() {
		return nil, "", Invalid("%q is not a role; use %s", in.Role, store.RoleList())
	}
	if err := ValidateScopes(in.Scopes); err != nil {
		return nil, "", Invalid("%v", err)
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.Now()) {
		return nil, "", Invalid("the expiry date is in the past; leave it empty for a token that never expires")
	}

	id := store.NewID(store.PrefixToken)
	prefix := APITokenPrefix + idFragment(id)
	plaintext := prefix + "_" + store.NewSecret(secretBytes)

	t := &store.APIToken{
		ID:        id,
		Name:      name,
		Role:      in.Role,
		OwnerRole: in.OwnerRole,
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

// DeleteAPIToken removes a revoked or expired token for good. A token that
// still works is refused with store.ErrConflict: revoke it first.
func (s *Service) DeleteAPIToken(ctx context.Context, id string) error {
	return s.store.DeleteAPIToken(ctx, id)
}

// ---------------------------------------------------------------------------
// Join tokens and agent credentials
// ---------------------------------------------------------------------------

// DefaultJoinTTL is how long a join token lasts when the caller does not say.
// It is short because a join token is pasted into a terminal within a minute of
// being created.
const DefaultJoinTTL = time.Hour

// JoinScope is everything a join token may be pinned to.
//
// The two scope fields are what separates a credential a person pastes into a
// terminal from one that travels inside a guest, where it is readable by
// everyone who can read that guest's disk. Both are empty on the human path.
type JoinScope struct {
	// TTL is how long the token lasts. A machine's is the enrolment timeout
	// plus a grace rather than DefaultJoinTTL: an hour is a long time for a
	// credential nobody is watching.
	TTL      time.Duration
	Labels   map[string]string
	Capacity int
	// MachineID is the machine this token was minted for, and ExpectedName the
	// name it must join under. The store refuses any other name inside the
	// transaction that spends the token.
	MachineID    string
	ExpectedName string
	CreatedBy    string
}

// CreateJoinToken mints the single-use credential that lets a new agent enrol.
// Like an API token, the plaintext is returned exactly once.
func (s *Service) CreateJoinToken(ctx context.Context, ttl time.Duration, labels map[string]string, capacity int, createdBy string) (*store.JoinToken, string, error) {
	return s.CreateScopedJoinToken(ctx, JoinScope{
		TTL: ttl, Labels: labels, Capacity: capacity, CreatedBy: createdBy,
	})
}

// CreateScopedJoinToken is CreateJoinToken with the two scope fields. The
// unscoped signature stays for the human path, where a person pastes a command
// and picks the host's name themselves.
func (s *Service) CreateScopedJoinToken(ctx context.Context, sc JoinScope) (*store.JoinToken, string, error) {
	ttl := sc.TTL
	if ttl <= 0 {
		ttl = DefaultJoinTTL
	}
	if sc.Capacity < 0 {
		return nil, "", Invalid("capacity cannot be negative; leave it at 0 to let the agent decide from its CPU count")
	}
	id := store.NewID(store.PrefixJoin)
	prefix := JoinTokenPrefix + idFragment(id)
	plaintext := prefix + "_" + store.NewSecret(secretBytes)

	t := &store.JoinToken{
		ID:           id,
		TokenHash:    cryptox.HashToken(plaintext),
		Prefix:       prefix,
		CreatedBy:    sc.CreatedBy,
		Labels:       store.StringMap(sc.Labels),
		Capacity:     sc.Capacity,
		ExpiresAt:    s.Now().Add(ttl),
		MachineID:    sc.MachineID,
		ExpectedName: sc.ExpectedName,
	}
	if err := s.store.CreateJoinToken(ctx, t); err != nil {
		return nil, "", fmt.Errorf("creating join token: %w", err)
	}
	return t, plaintext, nil
}

// RedeemJoinToken spends a join token for a host. It is single-use: the store
// marks it spent in the same transaction that reads it, so two agents racing
// with the same token cannot both enrol.
//
// A token minted for one machine refuses any other name, and the refusal is
// the agent operator's to act on rather than this controller's, so it becomes
// an Invalid like the other three.
func (s *Service) RedeemJoinToken(ctx context.Context, token string, claim store.JoinClaim) (*store.JoinToken, error) {
	if strings.TrimSpace(token) == "" {
		return nil, Invalid("no join token supplied; create one with `zoomies hosts join-token create`")
	}
	t, err := s.store.RedeemJoinToken(ctx, cryptox.HashToken(token), claim, s.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, Invalid("this join token is not valid; create a new one with `zoomies hosts join-token create`")
	case errors.Is(err, store.ErrJoinTokenUsed):
		return nil, Invalid("this join token has already been used; each one enrols exactly one host, so create another with `zoomies hosts join-token create`")
	case errors.Is(err, store.ErrJoinTokenExpired):
		return nil, Invalid("this join token has expired; create a new one with `zoomies hosts join-token create`")
	case errors.Is(err, store.ErrJoinTokenScope):
		return nil, Invalid("this join token was minted for a machine Zoomies created and cannot enrol %q; "+
			"it is not a token to reuse by hand, and a machine that has lost it is replaced rather than re-enrolled", claim.Name)
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
		return "", Invalid("username is required")
	}
	if len(u) > 64 {
		return "", Invalid("username must be 64 characters or fewer")
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '@' || r == '+':
		default:
			return "", Invalid("username %q contains %q; use letters, digits and . - _ @ +", in, string(r))
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
