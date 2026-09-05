package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// OIDCCallbackPath is where the identity provider sends the browser back to.
// It has to match the redirect URI registered with the provider, so it is a
// constant rather than something an operator can drift out of sync.
const OIDCCallbackPath = "/api/v1/auth/oidc/callback"

// OIDCStateTTL is how long a login may sit half-finished at the identity
// provider before its state and nonce are forgotten.
const OIDCStateTTL = 10 * time.Minute

// Claims is what a completed OIDC login tells us about the person, already
// mapped onto Zoomies' own vocabulary.
type Claims struct {
	// Subject is the provider's stable identifier for the person. It is what
	// accounts are linked by, because usernames and email addresses change.
	Subject string
	// Username is the account name inside Zoomies, from cfg.UsernameClaim.
	Username string
	Email    string
	// Name is the display name, if the provider sent one.
	Name string
	// Groups are the raw group values from cfg.GroupsClaim.
	Groups []string
	// Role is what those groups map to.
	Role store.Role
}

// OIDCProvider is optional single sign-on. A nil *OIDCProvider is a valid
// "SSO is switched off" value: every method tolerates it, so callers do not
// need a branch for the common case.
type OIDCProvider struct {
	cfg      config.OIDC
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	states   *stateCache
	logger   *slog.Logger
}

// NewOIDC builds the provider, discovering the issuer's endpoints over the
// network. It returns (nil, nil) when SSO is disabled, so a caller can assign
// the result unconditionally.
//
// It is named NewOIDC rather than New because New is already this package's
// Service constructor.
func NewOIDC(ctx context.Context, cfg config.OIDC, externalURL string) (*OIDCProvider, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, errors.New("oidc.enabled is on but oidc.issuer is empty; set it to your identity provider's issuer URL, e.g. https://login.example.com")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("oidc.enabled is on but oidc.client_id or oidc.client_secret is empty; register Zoomies as an application with your identity provider and paste both here")
	}

	redirect := strings.TrimSpace(cfg.RedirectURL)
	if redirect == "" {
		if externalURL == "" {
			return nil, errors.New("oidc needs a redirect URL; set server.external_url so it can be derived, or set oidc.redirect_url explicitly")
		}
		redirect = strings.TrimRight(externalURL, "/") + OIDCCallbackPath
	}

	provider, err := oidc.NewProvider(ctx, strings.TrimRight(cfg.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("could not read the OpenID configuration from %s: %w; check oidc.issuer and that this host can reach it", cfg.Issuer, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	// Without the openid scope the provider returns no ID token at all, which
	// fails much later and much less clearly.
	if !containsFold(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}

	return &OIDCProvider{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirect,
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		states:   newStateCache(OIDCStateTTL, time.Now),
		logger:   slog.Default(),
	}, nil
}

// Enabled reports whether single sign-on is configured.
func (p *OIDCProvider) Enabled() bool { return p != nil }

// RedirectURL is where the provider sends the browser back to; the settings
// page shows it so an operator can paste it into the provider's console.
func (p *OIDCProvider) RedirectURL() string {
	if p == nil {
		return ""
	}
	return p.oauth.RedirectURL
}

// AllowSignup reports whether a successful login may create an account.
func (p *OIDCProvider) AllowSignup() bool { return p != nil && p.cfg.AllowSignup }

// Start begins a login: it mints a single-use state and nonce, remembers them,
// and returns the URL to redirect the browser to.
func (p *OIDCProvider) Start() (authURL, state string, err error) {
	if p == nil {
		return "", "", errors.New("single sign-on is not configured on this instance")
	}
	state = store.NewSecret(secretBytes)
	nonce := store.NewSecret(secretBytes)
	p.states.put(state, nonce)
	return p.AuthCodeURL(state, nonce), state, nil
}

// AuthCodeURL renders the provider's authorisation URL for a state and nonce.
func (p *OIDCProvider) AuthCodeURL(state, nonce string) string {
	if p == nil {
		return ""
	}
	return p.oauth.AuthCodeURL(state, oidc.Nonce(nonce))
}

// Complete finishes a login begun by Start: it spends the state, exchanges the
// code and returns the claims. A state that is unknown, already spent or too
// old is refused -- that is what makes the flow single-use. It proves the
// handshake began on this controller, not that it began in this browser; the
// API layer adds that half by binding the state to a cookie set at Start.
func (p *OIDCProvider) Complete(ctx context.Context, state, code string) (*Claims, error) {
	if p == nil {
		return nil, errors.New("single sign-on is not configured on this instance")
	}
	nonce, ok := p.states.take(state)
	if !ok {
		return nil, errors.New("this sign-in link has already been used or has expired; start again from the login page")
	}
	return p.Exchange(ctx, code, nonce)
}

// Exchange swaps an authorisation code for an ID token, verifies it, and maps
// its claims onto a Zoomies identity. The nonce must be the one that was sent
// with the authorisation request.
func (p *OIDCProvider) Exchange(ctx context.Context, code, nonce string) (*Claims, error) {
	if p == nil {
		return nil, errors.New("single sign-on is not configured on this instance")
	}
	tok, err := p.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("the identity provider rejected the sign-in: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("the identity provider returned no ID token; check that Zoomies is asking for the openid scope and that the application is an OpenID Connect one")
	}
	idToken, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("the ID token did not verify: %w", err)
	}
	// A constant-time comparison is not strictly necessary for a value the
	// client already knows, but it costs nothing and keeps the habit.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("the ID token's nonce does not match the sign-in that started it; start again from the login page")
	}

	var raws map[string]any
	if err := idToken.Claims(&raws); err != nil {
		return nil, fmt.Errorf("could not read the ID token's claims: %w", err)
	}
	return p.claimsFrom(idToken.Subject, raws)
}

// claimsFrom maps a raw claim set onto Claims. It is separate from Exchange so
// the mapping can be tested without an identity provider.
func (p *OIDCProvider) claimsFrom(subject string, raw map[string]any) (*Claims, error) {
	usernameClaim := p.cfg.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}
	groupsClaim := p.cfg.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}

	c := &Claims{
		Subject:  subject,
		Username: claimString(raw, usernameClaim),
		Email:    claimString(raw, "email"),
		Name:     claimString(raw, "name"),
		Groups:   claimStrings(raw, groupsClaim),
	}
	if c.Username == "" {
		// Providers that do not send preferred_username almost always send an
		// email; falling back beats failing a login the operator cannot debug.
		c.Username = c.Email
	}
	if c.Username == "" {
		c.Username = subject
	}
	if c.Subject == "" {
		return nil, fmt.Errorf("the identity provider sent no subject claim, so this login cannot be linked to an account")
	}
	c.Role = p.RoleFor(c.Groups)
	return c, nil
}

// RoleFor maps the identity provider's groups onto a Zoomies role. Admin wins
// over operator, and anyone in no mapped group is a viewer -- so misconfiguring
// the group names produces read-only access rather than accidental admins.
func (p *OIDCProvider) RoleFor(groups []string) store.Role {
	if p == nil {
		return store.RoleViewer
	}
	for _, g := range groups {
		if containsFold(p.cfg.AdminGroups, g) {
			return store.RoleAdmin
		}
	}
	for _, g := range groups {
		if containsFold(p.cfg.OperatorGroups, g) {
			return store.RoleOperator
		}
	}
	return store.RoleViewer
}

// mapsRoles reports whether the operator configured any group mapping at all.
// When they did not, EnsureUser leaves an existing account's role alone rather
// than demoting everyone to viewer on their next login.
func (p *OIDCProvider) mapsRoles() bool {
	return p != nil && (len(p.cfg.AdminGroups) > 0 || len(p.cfg.OperatorGroups) > 0)
}

// EnsureUser resolves a set of verified claims to a local account.
//
// It links by subject first, because that is the only identifier a provider
// promises not to reuse. Failing that it adopts an existing account with the
// same username -- which is how an instance moves from local passwords to SSO
// without recreating everyone -- and only then, if allowSignup is on, creates
// one.
func (p *OIDCProvider) EnsureUser(ctx context.Context, st *store.Store, claims *Claims, allowSignup bool) (*store.User, error) {
	if claims == nil || claims.Subject == "" {
		return nil, errors.New("no verified claims to sign in with")
	}
	username, err := normalizeUsername(claims.Username)
	if err != nil {
		return nil, fmt.Errorf("the identity provider's username claim is not usable: %w", err)
	}

	u, err := st.GetUserByOIDCSubject(ctx, claims.Subject)
	switch {
	case err == nil:
		return p.sync(ctx, st, u, claims, username)
	case !errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("looking up the linked account: %w", err)
	}

	u, err = st.GetUserByUsername(ctx, username)
	switch {
	case err == nil:
		if u.OIDCSubject != "" && u.OIDCSubject != claims.Subject {
			return nil, fmt.Errorf("the account %q is already linked to a different single sign-on identity; an administrator must unlink or rename it", username)
		}
		u.OIDCSubject = claims.Subject
		return p.sync(ctx, st, u, claims, username)
	case !errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("looking up the account: %w", err)
	}

	if !allowSignup {
		return nil, fmt.Errorf("no Zoomies account exists for %q; an administrator must create it first, or turn on oidc.allow_signup to create accounts on first sign-in", username)
	}
	role := claims.Role
	if !role.Valid() {
		role = store.RoleViewer
	}
	u = &store.User{
		Username:    username,
		Email:       claims.Email,
		DisplayName: claims.Name,
		Role:        role,
		OIDCSubject: claims.Subject,
	}
	if err := st.CreateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("creating the account for %q: %w", username, err)
	}
	return u, nil
}

// sync updates a linked account from the provider's claims. The identity
// provider is authoritative for the profile, and for the role too when group
// mappings are configured.
func (p *OIDCProvider) sync(ctx context.Context, st *store.Store, u *store.User, claims *Claims, username string) (*store.User, error) {
	if u.Disabled {
		return nil, ErrAccountDisabled
	}
	before := *u
	u.OIDCSubject = claims.Subject
	u.Username = username
	if claims.Email != "" {
		u.Email = claims.Email
	}
	if claims.Name != "" {
		u.DisplayName = claims.Name
	}
	// The role follows the identity provider only when the operator actually
	// mapped groups to roles, and only when the mapping produced a real role;
	// otherwise an existing account keeps the role an administrator gave it.
	if p.mapsRoles() && claims.Role.Valid() {
		u.Role = claims.Role
	}
	if before == *u {
		return u, nil
	}
	if err := st.UpdateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("updating the account for %q: %w", username, err)
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// State and nonce cache
// ---------------------------------------------------------------------------

// stateCache remembers the state and nonce of logins that are in flight.
//
// It is deliberately in memory: a state lives for one redirect, and losing the
// lot on restart means everyone mid-login clicks the button again. Entries are
// single-use, so a replayed callback is refused.
type stateCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]stateEntry
}

type stateEntry struct {
	nonce   string
	expires time.Time
}

func newStateCache(ttl time.Duration, clock func() time.Time) *stateCache {
	if clock == nil {
		clock = time.Now
	}
	return &stateCache{ttl: ttl, now: clock, entries: map[string]stateEntry{}}
}

func (c *stateCache) put(state, nonce string) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[state] = stateEntry{nonce: nonce, expires: now.Add(c.ttl)}
}

// take returns the nonce for a state and forgets it, so a state can be spent
// exactly once.
func (c *stateCache) take(state string) (string, bool) {
	if state == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[state]
	if !ok {
		return "", false
	}
	delete(c.entries, state)
	if c.now().After(e.expires) {
		return "", false
	}
	return e.nonce, true
}

// ---------------------------------------------------------------------------
// Claim helpers
// ---------------------------------------------------------------------------

func claimString(raw map[string]any, key string) string {
	if key == "" {
		return ""
	}
	switch v := raw[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	}
	return ""
}

// claimStrings reads a claim that may be a list or, with some providers, a
// single string.
func claimStrings(raw map[string]any, key string) []string {
	switch v := raw[key].(type) {
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
