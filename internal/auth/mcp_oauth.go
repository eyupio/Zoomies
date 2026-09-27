package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// The controller as its own OAuth 2.1 authorisation server for /mcp.
//
// It follows the MCP authorization specification (revision 2026-07-28): an MCP
// client -- Claude, added as a connector by URL -- finds this server through
// protected resource metadata, identifies itself with a Client ID Metadata
// Document, by registering dynamically, or with a client an administrator made,
// sends the person here to sign in and approve it, and exchanges the code for
// an access token bound to /mcp and to nothing else.
//
// The access token deliberately does not work on the REST API. The person
// approved a connection for an agent's tools, at a role they chose for it; a
// token that also opened every route the role reaches would be an API token
// they never asked for, carried by software they cannot see. /mcp reaches the
// routes it needs in-process, on the connection's own identity, so nothing is
// lost by the refusal -- and the specification forbids a resource server from
// passing a token it was given on to anything else.

// Credential prefixes. Each says what it is when it turns up somewhere it
// should not, as API tokens do.
const (
	MCPAccessTokenPrefix  = "zoomcp_"
	MCPRefreshTokenPrefix = "zoomcpr_"
	MCPCodePrefix         = "zoomcpc_"
	MCPClientSecretPrefix = "zoocs_"
)

// KindConnection is the identity an MCP access token resolves to: a person's
// consent for one client, acting at the role they approved.
const KindConnection = "connection"

// KindClient is the audit actor for what an OAuth client does on its own
// behalf -- registering itself, revoking its own token -- before or without
// a person in the loop.
const KindClient = "client"

// The scopes a connection can carry. They mirror the role approved rather than
// adding a second axis of permission: mcp:read is viewer, and mcp:operate --
// which implies mcp:read -- is operator.
const (
	MCPScopeRead    = "mcp:read"
	MCPScopeOperate = "mcp:operate"
	// scopeOffline is accepted and ignored: a refresh token is always issued,
	// so asking for one changes nothing.
	scopeOffline = "offline_access"
)

// MCPScopes is what the protected resource advertises.
var MCPScopes = []string{MCPScopeRead, MCPScopeOperate}

// ClaudeCallback is the redirect URI the Claude apps -- claude.ai, Desktop,
// mobile -- send, and so the default for a client an administrator creates for
// them. It is a default and not an allow-list: any https or loopback redirect
// a client registers is accepted.
const ClaudeCallback = "https://claude.ai/api/mcp/auth_callback"

// Lifetimes. The access token is short because it is a bearer credential that
// travels on every call; the refresh token is long because a person who
// connected Claude last month expects it still to be connected, and it
// rotates on every use, so a stolen one is found out the first time both
// parties use it.
const (
	mcpAccessTTL  = time.Hour
	mcpRefreshTTL = 30 * 24 * time.Hour
	mcpCodeTTL    = 5 * time.Minute
	mcpRequestTTL = 15 * time.Minute
	// A connection's last_used_at is written at most this often.
	mcpTouchInterval = time.Minute

	mcpRegistrationsPerHour   = 20
	mcpTokenRequestsPerMinute = 60
)

// OAuthPolicy is what each request needs to know about the deployment as it
// stands now: the controller's public address and the two settings that can
// change without a restart.
type OAuthPolicy struct {
	// Issuer is the controller's public origin, with no trailing slash.
	Issuer string
	// OpenRegistration lets clients register themselves or be named by a
	// metadata document; off, only an administrator's clients may ask.
	OpenRegistration bool
	// AllowPrivateEgress lets a metadata document's URL name a private
	// address. It is security.allow_private_egress.
	AllowPrivateEgress bool
}

// Resource is the canonical URI of the MCP endpoint this policy protects.
func (p OAuthPolicy) Resource() string { return p.Issuer + "/mcp" }

// OAuthError is a refusal in RFC 6749's vocabulary. Status is the HTTP status
// the token and registration endpoints answer with; the authorisation
// endpoint carries Code back to the client in the redirect instead.
type OAuthError struct {
	Code        string
	Description string
	Status      int
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Description }

func oauthErr(status int, code, format string, args ...any) *OAuthError {
	return &OAuthError{Code: code, Description: fmt.Sprintf(format, args...), Status: status}
}

// ---------------------------------------------------------------------------
// Clients
// ---------------------------------------------------------------------------

// ClientRegistration is an RFC 7591 registration request, reduced to the
// fields this server acts on.
type ClientRegistration struct {
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

// RegisterClient stores a client that registered itself. It is always a
// public client: a secret handed to software on the strength of an
// unauthenticated request proves nothing about who holds it, so PKCE is what
// binds the code to the client that asked for it.
func (s *Service) RegisterClient(ctx context.Context, p OAuthPolicy, in ClientRegistration, ip string) (*store.OAuthClient, error) {
	if !p.OpenRegistration {
		return nil, oauthErr(403, "access_denied",
			"this controller does not let clients register themselves (security.mcp_open_registration is off); "+
				"ask an administrator for a client ID from Settings, MCP clients")
	}
	if !s.registrations.Allow(ip) {
		return nil, oauthErr(429, "too_many_requests", "too many client registrations from this address; wait and try again")
	}
	switch in.TokenEndpointAuthMethod {
	case "", "none":
	default:
		// RFC 7591 lets the server replace what was asked for and say so in
		// the response, which is what an MCP client expects; refusing would
		// only turn a working flow into an error.
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return nil, oauthErr(400, "invalid_client_metadata", "grant type %q is not offered here; use authorization_code with refresh_token", g)
		}
	}
	for _, r := range in.ResponseTypes {
		if r != "code" {
			return nil, oauthErr(400, "invalid_client_metadata", "response type %q is not offered here; use code", r)
		}
	}
	redirects, err := checkRedirectURIs(in.RedirectURIs)
	if err != nil {
		return nil, err
	}
	name := clientName(in.ClientName, in.ClientURI, redirects)
	c := &store.OAuthClient{
		Kind:         store.OAuthClientDynamic,
		Name:         name,
		ClientURI:    safeHTTPS(in.ClientURI),
		RedirectURIs: redirects,
		CreatedIP:    ip,
	}
	if err := s.store.CreateOAuthClient(ctx, c); err != nil {
		return nil, fmt.Errorf("registering an OAuth client: %w", err)
	}
	s.audit.Act(ctx, &Identity{Kind: KindClient, Name: name, IP: ip}, "mcp_client.register", "mcp_client", c.ID,
		map[string]any{"name": name, "redirect_uris": []string(redirects)})
	return c, nil
}

// NewMCPClient is an administrator's client.
type NewMCPClient struct {
	Name         string
	RedirectURIs []string
	// Confidential gives the client a secret, shown once, that it must
	// present at the token endpoint as well as its PKCE verifier.
	Confidential bool
}

// CreateMCPClient stores an administrator's client and returns its secret in
// the clear, once, when it is confidential.
func (s *Service) CreateMCPClient(ctx context.Context, actor *Identity, in NewMCPClient) (*store.OAuthClient, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", Invalid("a client needs a name; it is what people see on the consent screen")
	}
	if len(name) > 100 {
		return nil, "", Invalid("a client name may be at most 100 characters")
	}
	uris := in.RedirectURIs
	if len(uris) == 0 {
		uris = []string{ClaudeCallback}
	}
	redirects, err := checkRedirectURIs(uris)
	if err != nil {
		var oe *OAuthError
		if errors.As(err, &oe) {
			return nil, "", Invalid("%s", oe.Description)
		}
		return nil, "", err
	}
	c := &store.OAuthClient{
		Kind:         store.OAuthClientAdmin,
		Name:         name,
		RedirectURIs: redirects,
	}
	if actor != nil {
		c.CreatedBy, c.CreatedIP = actor.Name, actor.IP
	}
	var secret string
	if in.Confidential {
		secret = MCPClientSecretPrefix + store.NewSecret(secretBytes)
		c.SecretHash, c.SecretPrefix = cryptox.HashToken(secret), secret[:len(MCPClientSecretPrefix)+4]
	}
	if err := s.store.CreateOAuthClient(ctx, c); err != nil {
		return nil, "", fmt.Errorf("creating an MCP client: %w", err)
	}
	s.audit.Created(ctx, actor, "mcp_client", c.ID, map[string]any{
		"name": name, "redirect_uris": []string(redirects), "confidential": in.Confidential,
	})
	return c, secret, nil
}

// RotateMCPClientSecret replaces a confidential client's secret. The old one
// stops working at once; connections already made keep working, because a
// refresh presents the new secret from then on.
func (s *Service) RotateMCPClientSecret(ctx context.Context, actor *Identity, id string) (*store.OAuthClient, string, error) {
	c, err := s.store.GetOAuthClient(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if c.Kind != store.OAuthClientAdmin || !c.Confidential() {
		return nil, "", Invalid("only a confidential client an administrator created has a secret to rotate; %s is a %s client", c.Name, clientKindWord(c))
	}
	if c.Revoked() {
		return nil, "", Invalid("%s has been revoked; create a new client instead", c.Name)
	}
	secret := MCPClientSecretPrefix + store.NewSecret(secretBytes)
	prefix := secret[:len(MCPClientSecretPrefix)+4]
	if err := s.store.SetOAuthClientSecret(ctx, id, cryptox.HashToken(secret), prefix); err != nil {
		return nil, "", err
	}
	s.audit.Act(ctx, actor, "mcp_client.secret_rotate", "mcp_client", id, map[string]any{"name": c.Name})
	c, err = s.store.GetOAuthClient(ctx, id)
	return c, secret, err
}

// RevokeMCPClient turns a client away and ends every connection it holds.
func (s *Service) RevokeMCPClient(ctx context.Context, actor *Identity, id string) error {
	c, err := s.store.GetOAuthClient(ctx, id)
	if err != nil {
		return err
	}
	grants, err := s.store.ListOAuthGrants(ctx, "")
	if err != nil {
		return err
	}
	n, err := s.store.RevokeOAuthClient(ctx, id)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if g.ClientID == id {
			s.touched.Delete(g.ID)
		}
	}
	s.audit.Act(ctx, actor, "mcp_client.revoke", "mcp_client", id, map[string]any{"name": c.Name, "connections_ended": n})
	return nil
}

func clientKindWord(c *store.OAuthClient) string {
	switch c.Kind {
	case store.OAuthClientDynamic:
		return "self-registered"
	case store.OAuthClientMetadata:
		return "metadata-document"
	}
	if c.Confidential() {
		return "confidential"
	}
	return "public"
}

// clientName is what the consent screen calls a client. A client that gives
// no name is named after where it sends people, which is the one thing about
// it this server has checked.
func clientName(name, uri string, redirects []string) string {
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > 100 {
		name = name[:100]
	}
	if name != "" {
		return name
	}
	if u, err := url.Parse(uri); err == nil && u.Host != "" {
		return u.Host
	}
	if len(redirects) > 0 {
		if u, err := url.Parse(redirects[0]); err == nil {
			return u.Host
		}
	}
	return "Unnamed client"
}

func safeHTTPS(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || len(raw) > 500 {
		return ""
	}
	return u.String()
}

// checkRedirectURIs applies the specification's one rule for where a code may
// be sent -- https, or http to this machine's loopback -- and refuses a
// fragment, which OAuth forbids because the code would be appended after it.
func checkRedirectURIs(in []string) (store.StringSlice, error) {
	if len(in) == 0 {
		return nil, oauthErr(400, "invalid_redirect_uri", "a client needs at least one redirect URI")
	}
	if len(in) > 10 {
		return nil, oauthErr(400, "invalid_redirect_uri", "a client may register at most 10 redirect URIs")
	}
	var out store.StringSlice
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		u, err := url.Parse(raw)
		switch {
		case err != nil || u.Host == "" || len(raw) > 500:
			return nil, oauthErr(400, "invalid_redirect_uri", "%q is not an absolute URL", raw)
		case u.Fragment != "" || strings.Contains(raw, "#"):
			return nil, oauthErr(400, "invalid_redirect_uri", "%q has a fragment, which a redirect URI may not", raw)
		case u.User != nil:
			return nil, oauthErr(400, "invalid_redirect_uri", "%q carries a user name, which a redirect URI may not", raw)
		case u.Scheme == "https":
		case u.Scheme == "http" && loopbackRedirect(u):
		default:
			return nil, oauthErr(400, "invalid_redirect_uri",
				"%q is neither https nor http to this machine's loopback; those are the only places a code may be sent", raw)
		}
		if !slices.Contains(out, raw) {
			out = append(out, raw)
		}
	}
	return out, nil
}

func loopbackRedirect(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// redirectMatches compares a requested redirect URI with a registered one.
//
// It is exact, as the specification requires, with the one exception RFC 8252
// makes: a native client on this machine listens on whatever port it could
// get, so an http loopback redirect matches whatever the port. localhost is
// matched the same way as 127.0.0.1, because Claude Code sends it.
func redirectMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	r, err1 := url.Parse(registered)
	q, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil || r.Scheme != "http" || q.Scheme != "http" || !loopbackRedirect(r) {
		return false
	}
	return strings.EqualFold(r.Hostname(), q.Hostname()) && r.EscapedPath() == q.EscapedPath() &&
		r.RawQuery == q.RawQuery && q.User == nil && q.Fragment == ""
}

func clientAllowsRedirect(c *store.OAuthClient, uri string) bool {
	return slices.ContainsFunc(c.RedirectURIs, func(r string) bool { return redirectMatches(r, uri) })
}

// LoopbackOnly reports whether every redirect a client registered is on the
// person's own machine, which the consent screen says, because any program on
// that machine can listen there and claim to be the client.
func LoopbackOnly(c *store.OAuthClient) bool {
	for _, r := range c.RedirectURIs {
		u, err := url.Parse(r)
		if err != nil || !loopbackRedirect(u) {
			return false
		}
	}
	return len(c.RedirectURIs) > 0
}

// resolveClient finds the client a client_id names. An https client_id with a
// path is a Client ID Metadata Document: it is fetched, checked and kept, so
// that it can be listed and revoked like any other.
func (s *Service) resolveClient(ctx context.Context, p OAuthPolicy, clientID string) (*store.OAuthClient, error) {
	if clientID == "" {
		return nil, oauthErr(400, "invalid_request", "the request names no client_id")
	}
	c, err := s.store.GetOAuthClientByClientID(ctx, clientID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if c != nil && c.Revoked() {
		return nil, oauthErr(401, "invalid_client", "client %s has been revoked on this controller", c.Name)
	}
	if c != nil && c.Kind != store.OAuthClientMetadata {
		if c.Kind == store.OAuthClientDynamic && !p.OpenRegistration {
			return nil, oauthErr(401, "invalid_client", "this controller no longer accepts self-registered clients; ask an administrator for a client ID")
		}
		return c, nil
	}
	if !IsMetadataClientID(clientID) {
		return nil, oauthErr(401, "invalid_client", "this controller has no client %q; register again, or ask an administrator for a client ID", clientID)
	}
	if !p.OpenRegistration {
		return nil, oauthErr(401, "invalid_client", "this controller does not accept client metadata documents (security.mcp_open_registration is off); ask an administrator for a client ID")
	}
	if c != nil && s.metadata.fresh(clientID, s.Now()) {
		return c, nil
	}
	doc, err := s.metadata.fetch(ctx, clientID, p.AllowPrivateEgress)
	if err != nil {
		if c != nil {
			// The document was good a while ago and cannot be reached now;
			// what it said then is still the best answer there is.
			s.logger.Warn("could not refresh a client metadata document", "client_id", clientID, "error", err)
			return c, nil
		}
		return nil, oauthErr(400, "invalid_client", "the client metadata document at %s could not be used: %v", clientID, err)
	}
	s.metadata.remember(clientID, s.Now())
	if c != nil {
		if err := s.store.UpdateOAuthMetadataClient(ctx, c.ID, doc.name, doc.uri, doc.redirects); err != nil {
			return nil, err
		}
		return s.store.GetOAuthClient(ctx, c.ID)
	}
	c = &store.OAuthClient{
		ClientID:     clientID,
		Kind:         store.OAuthClientMetadata,
		Name:         doc.name,
		ClientURI:    doc.uri,
		RedirectURIs: doc.redirects,
	}
	if err := s.store.CreateOAuthClient(ctx, c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Two first sign-ins at once; the other one stored it.
			return s.store.GetOAuthClientByClientID(ctx, clientID)
		}
		return nil, err
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------

// AuthorizeParams is an authorisation request as it arrived.
type AuthorizeParams struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
	Resource            string
}

// AuthorizeRefusal is an authorisation request that cannot go ahead. When
// RedirectURI is set it has been checked against the client and the error goes
// back there; when it is empty the client or its redirect could not be
// trusted, and the person is told on this controller's own page instead --
// sending them to an unchecked address is the open redirect OAuth warns about.
type AuthorizeRefusal struct {
	*OAuthError
	RedirectURI string
	State       string
}

// BeginAuthorization checks an authorisation request and stores it for the
// person to decide on.
func (s *Service) BeginAuthorization(ctx context.Context, p OAuthPolicy, in AuthorizeParams) (*store.OAuthRequest, error) {
	c, err := s.resolveClient(ctx, p, in.ClientID)
	if err != nil {
		var oe *OAuthError
		if errors.As(err, &oe) {
			return nil, &AuthorizeRefusal{OAuthError: oe}
		}
		return nil, err
	}
	redirect := in.RedirectURI
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	if redirect == "" || !clientAllowsRedirect(c, redirect) {
		return nil, &AuthorizeRefusal{OAuthError: oauthErr(400, "invalid_request",
			"%s asked to send you to %s, which is not a redirect URI it registered, so this controller will not send you there", c.Name, orNone(redirect))}
	}
	back := func(code, format string, args ...any) error {
		return &AuthorizeRefusal{OAuthError: oauthErr(400, code, format, args...), RedirectURI: redirect, State: in.State}
	}
	if in.ResponseType != "code" {
		return nil, back("unsupported_response_type", "only the authorization code flow is offered (response_type=code)")
	}
	if in.CodeChallenge == "" {
		return nil, back("invalid_request", "PKCE is required: send code_challenge with code_challenge_method=S256")
	}
	if in.CodeChallengeMethod != "S256" {
		return nil, back("invalid_request", "only the S256 code challenge method is accepted")
	}
	if !validChallenge(in.CodeChallenge) {
		return nil, back("invalid_request", "code_challenge is not a base64url SHA-256 digest")
	}
	scope, err := normaliseScope(in.Scope)
	if err != nil {
		return nil, back("invalid_scope", "%s", err.Error())
	}
	resource, err := CheckResource(p, in.Resource)
	if err != nil {
		return nil, back("invalid_target", "%s", err.Error())
	}
	if len(in.State) > 2000 {
		return nil, back("invalid_request", "state is longer than this controller keeps")
	}
	r := &store.OAuthRequest{
		ClientID:      c.ID,
		RedirectURI:   redirect,
		State:         in.State,
		CodeChallenge: in.CodeChallenge,
		Resource:      resource,
		Scope:         scope,
		ExpiresAt:     s.Now().Add(mcpRequestTTL),
	}
	if err := s.store.CreateOAuthRequest(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func orNone(s string) string {
	if s == "" {
		return "no address"
	}
	return s
}

// CheckResource validates an RFC 8707 resource indicator against the one
// resource this server protects, and returns its canonical form. An absent
// one is taken to mean /mcp, since there is nothing else it could mean; one
// naming anything else is refused, because a token is only ever issued for
// the audience it will be checked against.
func CheckResource(p OAuthPolicy, raw string) (string, error) {
	want := p.Resource()
	if raw == "" {
		return want, nil
	}
	if canonicalResource(raw) != canonicalResource(want) {
		return "", fmt.Errorf("this controller issues tokens for %s only, and %s is not it", want, raw)
	}
	return want, nil
}

// canonicalResource lowercases the scheme and host and drops a trailing slash,
// which the specification asks a server to accept for robustness.
func canonicalResource(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Fragment != "" {
		return "\x00invalid"
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

func normaliseScope(raw string) (string, error) {
	var out []string
	for _, sc := range strings.Fields(raw) {
		switch sc {
		case MCPScopeRead, MCPScopeOperate:
			if !slices.Contains(out, sc) {
				out = append(out, sc)
			}
		case scopeOffline:
		default:
			return "", fmt.Errorf("%q is not a scope this controller offers; it offers %s", sc, strings.Join(MCPScopes, " and "))
		}
	}
	return strings.Join(out, " "), nil
}

// scopeFor is the scope a connection at a role carries.
func scopeFor(r store.Role) string {
	if r.AtLeast(store.RoleOperator) {
		return MCPScopeRead + " " + MCPScopeOperate
	}
	return MCPScopeRead
}

// roleCeiling is the highest role a person can hand a connection: their own,
// and never more than operator, which is the most any MCP tool needs.
func roleCeiling(r store.Role) store.Role {
	if r.AtLeast(store.RoleOperator) {
		return store.RoleOperator
	}
	return store.RoleViewer
}

// ConsentView is what the consent screen shows about a waiting request.
type ConsentView struct {
	Request       *store.OAuthRequest
	Client        *store.OAuthClient
	RedirectHost  string
	LoopbackOnly  bool
	Roles         []store.Role
	SuggestedRole store.Role
}

// DescribeAuthorization returns what the consent screen shows, for the person
// signed in.
func (s *Service) DescribeAuthorization(ctx context.Context, id *Identity, requestID string) (*ConsentView, error) {
	if err := consentingPerson(id); err != nil {
		return nil, err
	}
	r, err := s.store.GetOAuthRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetOAuthClient(ctx, r.ClientID)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(r.RedirectURI)
	v := &ConsentView{Request: r, Client: c, LoopbackOnly: LoopbackOnly(c)}
	if u != nil {
		v.RedirectHost = u.Host
	}
	ceiling := roleCeiling(id.Role)
	v.Roles = []store.Role{store.RoleViewer}
	if ceiling == store.RoleOperator {
		v.Roles = append(v.Roles, store.RoleOperator)
	}
	// Least privilege unless the client asked for more and the person can
	// give it: the choice is theirs either way.
	v.SuggestedRole = store.RoleViewer
	if strings.Contains(r.Scope, MCPScopeOperate) && ceiling == store.RoleOperator {
		v.SuggestedRole = store.RoleOperator
	}
	return v, nil
}

func consentingPerson(id *Identity) error {
	if id == nil || id.Kind != KindUser || !store.HasPrefix(id.ID, store.PrefixUser) {
		return Invalid("connecting an MCP client is done by a person signed in to this controller in a browser, not with a token")
	}
	return nil
}

// Decision is where the person's browser goes next.
type Decision struct {
	RedirectTo string
	Grant      *store.OAuthGrant
}

// ApproveAuthorization turns a waiting request into a connection at the role
// the person chose, and returns the client's redirect with the code.
func (s *Service) ApproveAuthorization(ctx context.Context, p OAuthPolicy, id *Identity, requestID string, role store.Role) (*Decision, error) {
	if err := consentingPerson(id); err != nil {
		return nil, err
	}
	if role == "" {
		role = store.RoleViewer
	}
	if !role.Valid() {
		return nil, Invalid("%q is not a role; a connection is viewer or operator", role)
	}
	if ceiling := roleCeiling(id.Role); !ceiling.AtLeast(role) {
		return nil, Invalid("a connection can have at most the %s role: no more than your own %s role, and never more than operator, which is all any MCP tool needs", ceiling, id.Role)
	}
	r, err := s.store.GetOAuthRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetOAuthClient(ctx, r.ClientID)
	if err != nil {
		return nil, err
	}
	// The request was checked when it began, up to fifteen minutes ago. An
	// administrator who has since closed open registration meant it for the
	// requests already waiting too, and approving one would hand the person a
	// code the token endpoint then refuses, with nothing on this page to say why.
	if c.Revoked() {
		return nil, Invalid("%s was revoked on this controller after it asked to connect; nothing was shared", c.Name)
	}
	if c.Kind != store.OAuthClientAdmin && !p.OpenRegistration {
		return nil, Invalid("this controller stopped accepting self-registered clients (security.mcp_open_registration is off) "+
			"after %s asked to connect, so nothing was shared; ask an administrator for a client ID from Settings, MCP clients, "+
			"and connect again with it", c.Name)
	}
	code := MCPCodePrefix + store.NewSecret(secretBytes)
	g := &store.OAuthGrant{
		ClientID:  c.ID,
		UserID:    id.ID,
		Role:      role,
		Scope:     scopeFor(role),
		Resource:  r.Resource,
		CreatedIP: id.IP,
	}
	tok := &store.OAuthToken{
		TokenHash:     cryptox.HashToken(code),
		RedirectURI:   r.RedirectURI,
		CodeChallenge: r.CodeChallenge,
		ExpiresAt:     s.Now().Add(mcpCodeTTL),
	}
	if err := s.store.ApproveOAuthRequest(ctx, requestID, g, tok); err != nil {
		return nil, err
	}
	s.audit.Act(ctx, id, "mcp_connection.grant", "mcp_connection", g.ID, map[string]any{
		"client": c.Name, "client_id": c.ID, "role": string(role), "redirect_uri": r.RedirectURI,
	})
	q := url.Values{"code": {code}, "iss": {p.Issuer}}
	if r.State != "" {
		q.Set("state", r.State)
	}
	return &Decision{RedirectTo: appendQuery(r.RedirectURI, q), Grant: g}, nil
}

// DenyAuthorization discards a waiting request and returns the client's
// redirect carrying access_denied.
func (s *Service) DenyAuthorization(ctx context.Context, p OAuthPolicy, id *Identity, requestID string) (*Decision, error) {
	if err := consentingPerson(id); err != nil {
		return nil, err
	}
	r, err := s.store.GetOAuthRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	took, err := s.store.TakeOAuthRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if !took {
		return nil, fmt.Errorf("oauth request %s: %w", requestID, store.ErrNotFound)
	}
	if c, err := s.store.GetOAuthClient(ctx, r.ClientID); err == nil {
		s.audit.Act(ctx, id, "mcp_connection.deny", "mcp_client", c.ID, map[string]any{"client": c.Name})
	}
	return &Decision{RedirectTo: ErrorRedirect(p, r.RedirectURI, r.State, "access_denied", "the person signed in to Zoomies declined the connection")}, nil
}

// ErrorRedirect is a client's redirect carrying an OAuth error, with the
// issuer so the client can tell which server refused it (RFC 9207).
func ErrorRedirect(p OAuthPolicy, redirect, state, code, description string) string {
	q := url.Values{"error": {code}, "iss": {p.Issuer}}
	if description != "" {
		q.Set("error_description", description)
	}
	if state != "" {
		q.Set("state", state)
	}
	return appendQuery(redirect, q)
}

func appendQuery(raw string, q url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	have := u.Query()
	for k, v := range q {
		have[k] = v
	}
	u.RawQuery = have.Encode()
	return u.String()
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

// ClientCredentials is how a client identified itself at the token or
// revocation endpoint: by Basic authentication, by form fields, or by its
// client_id alone.
type ClientCredentials struct {
	ClientID string
	Secret   string
	// Basic is set when the credentials came in an Authorization header, so
	// that a refusal can answer with the challenge RFC 6749 asks for.
	Basic bool
}

// TokenRequest is a token endpoint request's form.
type TokenRequest struct {
	GrantType    string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	Resource     string
	Scope        string
}

// TokenResponse is RFC 6749 section 5.1's answer.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// authenticateClient checks who is at the token endpoint. A confidential
// client must present its secret, by either method; a public one must not
// present one, because a secret nobody issued is a sign of a client talking
// to the wrong server.
func (s *Service) authenticateClient(ctx context.Context, p OAuthPolicy, cc ClientCredentials) (*store.OAuthClient, error) {
	fail := func(format string, args ...any) error {
		return oauthErr(401, "invalid_client", format, args...)
	}
	if cc.ClientID == "" {
		return nil, fail("the request does not say which client it is; send client_id")
	}
	c, err := s.store.GetOAuthClientByClientID(ctx, cc.ClientID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fail("this controller has no client %q", cc.ClientID)
	}
	if err != nil {
		return nil, err
	}
	if c.Revoked() {
		return nil, fail("client %s has been revoked on this controller", c.Name)
	}
	if c.Kind != store.OAuthClientAdmin && !p.OpenRegistration {
		return nil, fail("this controller no longer accepts self-registered clients; ask an administrator for a client ID")
	}
	switch {
	case c.Confidential() && cc.Secret == "":
		return nil, fail("client %s is confidential; send its secret with client_secret_basic or client_secret_post", c.Name)
	case c.Confidential() && !cryptox.ConstantTimeEqual(cryptox.HashToken(cc.Secret), c.SecretHash):
		return nil, fail("that is not the secret of client %s; if it was rotated, use the new one", c.Name)
	case !c.Confidential() && cc.Secret != "":
		return nil, fail("client %s is public and has no secret; send client_id alone", c.Name)
	}
	return c, nil
}

// Token answers the token endpoint.
func (s *Service) Token(ctx context.Context, p OAuthPolicy, cc ClientCredentials, in TokenRequest, ip string) (*TokenResponse, error) {
	if !s.tokens.Allow(ip) {
		return nil, oauthErr(429, "slow_down", "too many token requests from this address; wait and try again")
	}
	c, err := s.authenticateClient(ctx, p, cc)
	if err != nil {
		return nil, err
	}
	if in.Resource != "" {
		if _, err := CheckResource(p, in.Resource); err != nil {
			return nil, oauthErr(400, "invalid_target", "%s", err.Error())
		}
	}
	switch in.GrantType {
	case "authorization_code":
		return s.exchangeCode(ctx, p, c, in, ip)
	case "refresh_token":
		return s.refresh(ctx, p, c, in, ip)
	case "":
		return nil, oauthErr(400, "invalid_request", "the request names no grant_type")
	default:
		return nil, oauthErr(400, "unsupported_grant_type", "grant type %q is not offered; use authorization_code or refresh_token", in.GrantType)
	}
}

func (s *Service) exchangeCode(ctx context.Context, p OAuthPolicy, c *store.OAuthClient, in TokenRequest, ip string) (*TokenResponse, error) {
	bad := func(format string, args ...any) error { return oauthErr(400, "invalid_grant", format, args...) }
	if in.Code == "" || in.CodeVerifier == "" {
		return nil, oauthErr(400, "invalid_request", "an authorization_code request needs code and code_verifier")
	}
	hash := cryptox.HashToken(in.Code)
	t, err := s.store.GetOAuthToken(ctx, hash)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.Kind != store.OAuthCode) {
		return nil, bad("that authorization code is not one this controller issued, or it has expired")
	}
	if err != nil {
		return nil, err
	}
	g, err := s.store.GetOAuthGrant(ctx, t.GrantID)
	if err != nil {
		return nil, err
	}
	if g.ClientID != c.ID {
		return nil, bad("that authorization code was issued to a different client")
	}
	if t.UsedAt != nil {
		// OAuth 2.1 section 4.1.3: a code presented twice means somebody
		// else has it, so everything it was exchanged for goes too.
		s.revokeReplayed(ctx, g, "authorization code used twice", ip)
		return nil, bad("that authorization code has already been used; the connection it made has been revoked, so sign in again")
	}
	if !s.Now().Before(t.ExpiresAt) {
		return nil, bad("that authorization code has expired; sign in again")
	}
	if in.RedirectURI != "" && in.RedirectURI != t.RedirectURI {
		return nil, bad("redirect_uri does not match the one the code was issued for")
	}
	if !verifyPKCE(in.CodeVerifier, t.CodeChallenge) {
		return nil, bad("the code_verifier does not match the code_challenge sent with the authorization request")
	}
	if in.Resource != "" && canonicalResource(in.Resource) != canonicalResource(g.Resource) {
		return nil, oauthErr(400, "invalid_target", "the code was issued for %s", g.Resource)
	}
	return s.issue(ctx, c, g, hash, ip)
}

func (s *Service) refresh(ctx context.Context, p OAuthPolicy, c *store.OAuthClient, in TokenRequest, ip string) (*TokenResponse, error) {
	bad := func(format string, args ...any) error { return oauthErr(400, "invalid_grant", format, args...) }
	if in.RefreshToken == "" {
		return nil, oauthErr(400, "invalid_request", "a refresh_token request needs refresh_token")
	}
	hash := cryptox.HashToken(in.RefreshToken)
	t, err := s.store.GetOAuthToken(ctx, hash)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.Kind != store.OAuthRefresh) {
		return nil, bad("that refresh token is not one this controller issued, or its connection has been revoked; sign in again")
	}
	if err != nil {
		return nil, err
	}
	g, err := s.store.GetOAuthGrant(ctx, t.GrantID)
	if err != nil {
		return nil, err
	}
	if g.ClientID != c.ID {
		return nil, bad("that refresh token was issued to a different client")
	}
	if t.UsedAt != nil {
		// Rotation's whole point: the legitimate client and a thief cannot
		// both keep using one family, and the second to try gives it away.
		s.revokeReplayed(ctx, g, "refresh token used twice", ip)
		return nil, bad("that refresh token has already been used; the connection has been revoked, so sign in again")
	}
	if !s.Now().Before(t.ExpiresAt) {
		return nil, bad("that refresh token has expired; sign in again")
	}
	if in.Scope != "" {
		want, err := normaliseScope(in.Scope)
		if err != nil {
			return nil, oauthErr(400, "invalid_scope", "%s", err.Error())
		}
		for _, sc := range strings.Fields(want) {
			if !strings.Contains(" "+g.Scope+" ", " "+sc+" ") {
				return nil, oauthErr(400, "invalid_scope", "this connection was approved for %s; connect again to be granted %s", g.Scope, sc)
			}
		}
	}
	return s.issue(ctx, c, g, hash, ip)
}

// issue spends a code or refresh token and hands out a fresh pair, after the
// checks that belong to the person rather than the token: an account that has
// been disabled since it approved the connection gets nothing new.
func (s *Service) issue(ctx context.Context, c *store.OAuthClient, g *store.OAuthGrant, spent, ip string) (*TokenResponse, error) {
	u, err := s.store.GetUser(ctx, g.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		return nil, oauthErr(400, "invalid_grant", "the account that approved this connection is disabled or gone")
	}
	if err != nil {
		return nil, err
	}
	access := MCPAccessTokenPrefix + store.NewSecret(secretBytes)
	refresh := MCPRefreshTokenPrefix + store.NewSecret(secretBytes)
	now := s.Now()
	err = s.store.SpendOAuthToken(ctx, spent,
		&store.OAuthToken{TokenHash: cryptox.HashToken(access), Kind: store.OAuthAccess, ExpiresAt: now.Add(mcpAccessTTL)},
		&store.OAuthToken{TokenHash: cryptox.HashToken(refresh), Kind: store.OAuthRefresh, ExpiresAt: now.Add(mcpRefreshTTL)},
	)
	switch {
	case errors.Is(err, store.ErrConflict):
		// Lost a race with another use of the same code or refresh token:
		// the same replay, arriving at the same moment.
		s.revokeReplayed(ctx, g, "credential used twice at once", ip)
		return nil, oauthErr(400, "invalid_grant", "that credential has already been used; the connection has been revoked, so sign in again")
	case errors.Is(err, store.ErrNotFound):
		return nil, oauthErr(400, "invalid_grant", "that credential has expired or its connection has been revoked; sign in again")
	case err != nil:
		return nil, err
	}
	if err := s.store.TouchOAuthClient(ctx, c.ID); err != nil {
		s.logger.Warn("could not record client use", "client", c.ID, "error", err)
	}
	return &TokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int(mcpAccessTTL / time.Second),
		RefreshToken: refresh,
		Scope:        scopeFor(effectiveRole(g.Role, u.Role)),
	}, nil
}

func (s *Service) revokeReplayed(ctx context.Context, g *store.OAuthGrant, why, ip string) {
	err := s.store.RevokeOAuthGrant(ctx, g.ID, why)
	if errors.Is(err, store.ErrConflict) {
		// Already ended, by this replay's twin or by the person. The first
		// revocation is on the record; a row for every later presentation
		// would let whoever holds the credential fill the audit log with it.
		return
	}
	if err != nil {
		s.logger.Error("could not revoke a connection after a replayed credential", "connection", g.ID, "error", err)
	}
	s.touched.Delete(g.ID)
	s.audit.Act(ctx, &Identity{Kind: KindSystem, ID: "system", Name: "zoomies", IP: ip}, "mcp_connection.replay", "mcp_connection", g.ID,
		map[string]any{"reason": why, "client": g.ClientName, "user": g.Username})
}

// RevokeToken answers RFC 7009. It says nothing about whether the token was
// real, as the RFC asks: a revocation endpoint that told a caller which tokens
// exist would be an oracle for guessing them.
func (s *Service) RevokeToken(ctx context.Context, p OAuthPolicy, cc ClientCredentials, token, ip string) error {
	c, err := s.authenticateClient(ctx, p, cc)
	if err != nil {
		return err
	}
	hash := cryptox.HashToken(token)
	t, err := s.store.GetOAuthToken(ctx, hash)
	if err != nil {
		return nil
	}
	g, err := s.store.GetOAuthGrant(ctx, t.GrantID)
	if err != nil || g.ClientID != c.ID {
		return nil
	}
	if t.Kind == store.OAuthRefresh {
		// The refresh token is the connection; revoking it ends what it
		// would have renewed, access tokens included.
		if err := s.store.RevokeOAuthGrant(ctx, g.ID, "revoked by the client"); err == nil {
			s.touched.Delete(g.ID)
			s.audit.Act(ctx, &Identity{Kind: KindClient, ID: c.ID, Name: c.Name, IP: ip}, "mcp_connection.revoke", "mcp_connection", g.ID,
				map[string]any{"by": "client", "client": c.Name, "user": g.Username})
		}
		return nil
	}
	return s.store.DeleteOAuthToken(ctx, hash)
}

// ---------------------------------------------------------------------------
// Using a token
// ---------------------------------------------------------------------------

// IsMCPAccessToken reports whether a bearer credential is an MCP access token,
// which is the question /mcp asks before choosing how to check it.
func IsMCPAccessToken(token string) bool { return strings.HasPrefix(token, MCPAccessTokenPrefix) }

// BearerToken extracts the credential from an Authorization header.
func BearerToken(header string) string { return bearerToken(header) }

// Errors an MCP access token is refused with. Each is a 401: the client's
// answer to all of them is to refresh or sign in again.
var (
	ErrMCPTokenInvalid  = errors.New("this controller does not recognise that MCP access token; the client should sign in again")
	ErrMCPTokenExpired  = errors.New("that MCP access token has expired; the client should use its refresh token")
	ErrMCPTokenAudience = errors.New("that MCP access token was issued for a different resource, and is only accepted where it was meant to be used")
)

// AuthenticateMCP resolves an MCP access token to the connection it belongs
// to. The role is the lower of what was approved and what the person holds
// now, so demoting an account demotes its connections with it.
func (s *Service) AuthenticateMCP(ctx context.Context, p OAuthPolicy, token, ip string) (*Identity, error) {
	a, err := s.store.ResolveOAuthAccess(ctx, cryptox.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrMCPTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("looking up an MCP access token: %w", err)
	}
	now := s.Now()
	switch {
	case !now.Before(a.Token.ExpiresAt):
		return nil, ErrMCPTokenExpired
	case a.Grant.Revoked() || a.ClientGone:
		return nil, ErrMCPTokenInvalid
	case a.User.Disabled:
		return nil, ErrAccountDisabled
	case canonicalResource(a.Grant.Resource) != canonicalResource(p.Resource()):
		return nil, ErrMCPTokenAudience
	}
	if prev, ok := s.touched.Load(a.Grant.ID); !ok || now.Sub(prev.(time.Time)) >= mcpTouchInterval {
		s.touched.Store(a.Grant.ID, now)
		if err := s.store.TouchOAuthGrant(ctx, a.Grant.ID, now); err != nil {
			s.logger.Warn("could not record MCP connection use", "connection", a.Grant.ID, "error", err)
		}
	}
	return &Identity{
		Kind:    KindConnection,
		ID:      a.Grant.ID,
		Name:    a.User.Username + " via " + a.ClientName,
		Role:    effectiveRole(a.Grant.Role, a.User.Role),
		TokenID: a.Grant.ID,
		UserID:  a.User.ID,
		IP:      ip,
	}, nil
}

func effectiveRole(granted, holds store.Role) store.Role {
	if holds.AtLeast(granted) {
		return granted
	}
	return roleCeiling(holds)
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

// ListMCPConnections returns one person's connections, or everybody's when
// userID is empty.
func (s *Service) ListMCPConnections(ctx context.Context, userID string) ([]*store.OAuthGrant, error) {
	return s.store.ListOAuthGrants(ctx, userID)
}

// RevokeMCPConnection ends a connection. ownerID limits it to one person's
// own, so that the account page cannot end somebody else's by ID; it is empty
// for an administrator.
func (s *Service) RevokeMCPConnection(ctx context.Context, actor *Identity, id, ownerID string) error {
	g, err := s.store.GetOAuthGrant(ctx, id)
	if err != nil {
		return err
	}
	if ownerID != "" && g.UserID != ownerID {
		return fmt.Errorf("mcp connection %s: %w", id, store.ErrNotFound)
	}
	reason := "revoked by " + actor.Name
	if err := s.store.RevokeOAuthGrant(ctx, id, reason); err != nil {
		return err
	}
	// The last-used throttle keeps an entry per live connection; an ended
	// one would otherwise stay in it for the life of the process.
	s.touched.Delete(id)
	s.audit.Act(ctx, actor, "mcp_connection.revoke", "mcp_connection", id, map[string]any{
		"client": g.ClientName, "user": g.Username, "role": string(g.Role),
	})
	return nil
}

// ---------------------------------------------------------------------------
// PKCE
// ---------------------------------------------------------------------------

func validChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil
}

// verifyPKCE checks an RFC 7636 S256 verifier against its challenge.
func verifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	return cryptox.ConstantTimeEqual(base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
}

// PKCEChallenge is the S256 challenge for a verifier. Tests and the CLI's own
// checks use it; the server only ever verifies.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
