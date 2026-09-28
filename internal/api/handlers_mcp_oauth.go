package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// OAuth for /mcp: the discovery documents, the four OAuth endpoints, and the
// REST routes the consent screen and the settings pages use. The decisions --
// what a redirect may be, whether a code is spent, what a role may be -- are
// internal/auth's; this file reads requests and writes the shapes the
// specifications promise.

// consentPath is the UI page an authorisation request is decided on.
const consentPath = "/oauth/consent"

// publicBase is the controller's public origin: the external URL when the
// operator set one, and otherwise the scheme and host this request arrived
// on. The validator raises mcp_oauth.enabled with a sentence about setting the
// external URL, because a Host header is the client's to choose.
func (s *Server) publicBase(r *http.Request) string {
	if ext := strings.TrimRight(strings.TrimSpace(s.cfg().Server.ExternalURL), "/"); ext != "" {
		if u, err := url.Parse(ext); err == nil && u.Host != "" {
			return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
		}
	}
	return s.requestScheme(r) + "://" + strings.ToLower(r.Host)
}

func (s *Server) oauthPolicy(r *http.Request) auth.OAuthPolicy {
	c := s.cfg()
	return auth.OAuthPolicy{
		Issuer:             s.publicBase(r),
		OpenRegistration:   c.Security.MCPOpenRegistration,
		AllowPrivateEgress: c.Security.AllowPrivateEgress,
	}
}

// mcpOAuth guards every OAuth route: with the setting off they are not here
// at all, which is what a client probing for them should find.
func (s *Server) mcpOAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg().MCPOAuthEnabled() {
			if isAPIPath(r.URL.Path) {
				notFound(w, "OAuth sign-in for MCP is off on this controller (security.mcp_oauth); use an API token on /mcp instead")
				return
			}
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// resourceMetadataURL is where /mcp's 401 points a client.
func (s *Server) resourceMetadataURL(r *http.Request) string {
	return s.publicBase(r) + "/.well-known/oauth-protected-resource/mcp"
}

// mcpChallenge is the WWW-Authenticate header /mcp answers a missing or bad
// credential with. With OAuth on it is the one RFC 9728 describes, naming the
// metadata and the scopes, and it is what starts Claude's sign-in.
func (s *Server) mcpChallenge(r *http.Request, errCode string) string {
	if !s.cfg().MCPOAuthEnabled() {
		return `Bearer realm="zoomies"`
	}
	h := `Bearer resource_metadata="` + s.resourceMetadataURL(r) + `", scope="` + strings.Join(auth.MCPScopes, " ") + `"`
	if errCode != "" {
		h += `, error="` + errCode + `"`
	}
	return h
}

// metadataJSON writes a public discovery document. Anybody may read it --
// that is its purpose -- including a browser-based client on another origin.
func metadataJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, v)
}

// handleProtectedResource is RFC 9728 protected resource metadata for /mcp,
// served both at the root well-known address and at the one with /mcp
// appended, which a client tries first.
func (s *Server) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	p := s.oauthPolicy(r)
	metadataJSON(w, map[string]any{
		"resource":                 p.Resource(),
		"authorization_servers":    []string{p.Issuer},
		"scopes_supported":         auth.MCPScopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Zoomies",
		"resource_documentation":   "https://zoomies.sh/connect-claude/",
	})
}

// handleAuthorizationServer is RFC 8414 authorisation server metadata.
//
// There is no OpenID Connect discovery document beside it. This server issues
// no ID tokens and publishes no signing keys, and a document at
// /.well-known/openid-configuration would tell a client it could expect both;
// the MCP specification asks for one discovery mechanism, and a client tries
// this one first.
func (s *Server) handleAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	p := s.oauthPolicy(r)
	doc := map[string]any{
		"issuer":                                         p.Issuer,
		"authorization_endpoint":                         p.Issuer + "/oauth/authorize",
		"token_endpoint":                                 p.Issuer + "/oauth/token",
		"revocation_endpoint":                            p.Issuer + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic", "client_secret_post"},
		"revocation_endpoint_auth_methods_supported":     []string{"none", "client_secret_basic", "client_secret_post"},
		"scopes_supported":                               append(append([]string{}, auth.MCPScopes...), "offline_access"),
		"authorization_response_iss_parameter_supported": true,
		"client_id_metadata_document_supported":          p.OpenRegistration,
		"service_documentation":                          "https://zoomies.sh/connect-claude/",
	}
	if p.OpenRegistration {
		doc["registration_endpoint"] = p.Issuer + "/oauth/register"
	}
	metadataJSON(w, doc)
}

// oauthError writes an RFC 6749 error body. These endpoints speak OAuth's
// vocabulary rather than this API's envelope, because the client reading them
// is an OAuth library.
func oauthError(w http.ResponseWriter, e *auth.OAuthError, basic bool) {
	w.Header().Set("Cache-Control", "no-store")
	if e.Status == http.StatusUnauthorized && basic {
		w.Header().Set("WWW-Authenticate", `Basic realm="zoomies"`)
	}
	if e.Status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	writeJSON(w, e.Status, map[string]string{"error": e.Code, "error_description": e.Description})
}

func (s *Server) oauthFail(w http.ResponseWriter, r *http.Request, doing string, err error, basic bool) {
	var oe *auth.OAuthError
	if errors.As(err, &oe) {
		oauthError(w, oe, basic)
		return
	}
	// Nothing derived from the error is logged: these requests carry a client
	// secret and a code or refresh token, and an error that ever quoted one
	// would put a live credential in the log. The request ID, which the
	// request logger already carries, ties the 500 to the audit trail.
	s.logger(r).Error("an OAuth request failed", "doing", doing)
	oauthError(w, &auth.OAuthError{Code: "server_error", Status: http.StatusInternalServerError,
		Description: "something went wrong while " + doing + "; request " + RequestID(r.Context())}, basic)
}

// handleRegister is RFC 7591 dynamic client registration.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in auth.ClientRegistration
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil || json.Unmarshal(body, &in) != nil {
		oauthError(w, &auth.OAuthError{Code: "invalid_client_metadata", Status: http.StatusBadRequest,
			Description: "the registration request is not a JSON object"}, false)
		return
	}
	c, err := s.auth.RegisterClient(r.Context(), s.oauthPolicy(r), in, ClientIP(r.Context()))
	if err != nil {
		s.oauthFail(w, r, "registering a client", err, false)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ClientID,
		"client_id_issued_at":        c.CreatedAt.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              []string(c.RedirectURIs),
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      strings.Join(auth.MCPScopes, " "),
	})
}

// handleAuthorize is the authorisation endpoint. It checks the request, keeps
// it, and sends the browser to the consent page -- which shows the sign-in
// form first when nobody is signed in, and comes back to the same address.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := s.oauthPolicy(r)
	req, err := s.auth.BeginAuthorization(r.Context(), p, auth.AuthorizeParams{
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		ResponseType:        q.Get("response_type"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		State:               q.Get("state"),
		Scope:               q.Get("scope"),
		Resource:            q.Get("resource"),
	})
	var refusal *auth.AuthorizeRefusal
	switch {
	case errors.As(err, &refusal) && refusal.RedirectURI != "":
		http.Redirect(w, r, auth.ErrorRedirect(p, refusal.RedirectURI, refusal.State, refusal.Code, refusal.Description), http.StatusFound)
	case errors.As(err, &refusal):
		// The client or its redirect could not be trusted, so the person is
		// told here rather than sent anywhere. Only the code travels: the page
		// renders its own sentence for it, because text read from the address
		// is text anybody can write into a link that opens on this controller.
		// The specific reason goes to the log, for whoever the person asks --
		// at debug, like every other request line, and cut short, because it
		// quotes a client_id and redirect_uri anybody can make as long as a
		// URL, on an endpoint nobody has to sign in to reach.
		s.logger(r).Debug("refused an MCP sign-in on the consent page", "error", refusal.Code, "reason", truncateForLog(refusal.Description, 200))
		http.Redirect(w, r, consentPath+"?"+url.Values{"error": {refusal.Code}}.Encode(), http.StatusFound)
	case err != nil:
		s.internal(w, r, "starting an MCP sign-in", err)
	default:
		http.Redirect(w, r, consentPath+"?"+url.Values{"request": {req.ID}}.Encode(), http.StatusFound)
	}
}

// clientCredentials reads how a client identified itself: HTTP Basic with the
// form-encoded ID and secret RFC 6749 section 2.3.1 describes, or form fields.
// Both at once is refused, as the RFC asks.
func clientCredentials(r *http.Request) (auth.ClientCredentials, *auth.OAuthError) {
	cc := auth.ClientCredentials{ClientID: r.PostForm.Get("client_id"), Secret: r.PostForm.Get("client_secret")}
	h := r.Header.Get("Authorization")
	scheme, value, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "basic") {
		return cc, nil
	}
	if cc.Secret != "" {
		return cc, &auth.OAuthError{Code: "invalid_request", Status: http.StatusBadRequest,
			Description: "send the client secret one way, not in both the Authorization header and the form"}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	id, secret, found := strings.Cut(string(raw), ":")
	if err != nil || !found {
		return cc, &auth.OAuthError{Code: "invalid_client", Status: http.StatusUnauthorized, Description: "the Basic credentials are not client_id:client_secret"}
	}
	id, err1 := url.QueryUnescape(id)
	secret, err2 := url.QueryUnescape(secret)
	if err1 != nil || err2 != nil {
		return cc, &auth.OAuthError{Code: "invalid_client", Status: http.StatusUnauthorized, Description: "the Basic credentials are not form-encoded"}
	}
	if cc.ClientID != "" && cc.ClientID != id {
		return cc, &auth.OAuthError{Code: "invalid_request", Status: http.StatusBadRequest, Description: "client_id in the form is not the one in the Authorization header"}
	}
	return auth.ClientCredentials{ClientID: id, Secret: secret, Basic: true}, nil
}

func parseOAuthForm(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/x-www-form-urlencoded") {
		oauthError(w, &auth.OAuthError{Code: "invalid_request", Status: http.StatusBadRequest,
			Description: "send the request as application/x-www-form-urlencoded, as RFC 6749 asks"}, false)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, &auth.OAuthError{Code: "invalid_request", Status: http.StatusBadRequest, Description: "the form could not be read"}, false)
		return false
	}
	return true
}

// handleToken is the token endpoint: a code or a refresh token in, a fresh
// access and refresh pair out.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if !parseOAuthForm(w, r) {
		return
	}
	cc, cerr := clientCredentials(r)
	if cerr != nil {
		oauthError(w, cerr, cc.Basic)
		return
	}
	f := r.PostForm
	resp, err := s.auth.Token(r.Context(), s.oauthPolicy(r), cc, auth.TokenRequest{
		GrantType:    f.Get("grant_type"),
		Code:         f.Get("code"),
		RedirectURI:  f.Get("redirect_uri"),
		CodeVerifier: f.Get("code_verifier"),
		RefreshToken: f.Get("refresh_token"),
		Resource:     f.Get("resource"),
		Scope:        f.Get("scope"),
	}, ClientIP(r.Context()))
	if err != nil {
		s.oauthFail(w, r, "issuing a token", err, cc.Basic)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, resp)
}

// handleRevoke is RFC 7009 token revocation.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if !parseOAuthForm(w, r) {
		return
	}
	cc, cerr := clientCredentials(r)
	if cerr != nil {
		oauthError(w, cerr, cc.Basic)
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		oauthError(w, &auth.OAuthError{Code: "invalid_request", Status: http.StatusBadRequest, Description: "the request names no token"}, cc.Basic)
		return
	}
	if err := s.auth.RevokeToken(r.Context(), s.oauthPolicy(r), cc, token, ClientIP(r.Context())); err != nil {
		s.oauthFail(w, r, "revoking a token", err, cc.Basic)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// The consent screen
// ---------------------------------------------------------------------------

type mcpConsentClient struct {
	ID        string `json:"id"`
	ClientID  string `json:"client_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	ClientURI string `json:"client_uri,omitempty"`
}

type mcpConsentResponse struct {
	ID             string           `json:"id"`
	Client         mcpConsentClient `json:"client"`
	RedirectURI    string           `json:"redirect_uri"`
	RedirectHost   string           `json:"redirect_host"`
	LoopbackOnly   bool             `json:"loopback_only"`
	Resource       string           `json:"resource"`
	RequestedScope string           `json:"requested_scope"`
	Roles          []store.Role     `json:"roles"`
	SuggestedRole  store.Role       `json:"suggested_role"`
	YourRole       store.Role       `json:"your_role"`
	ExpiresAt      time.Time        `json:"expires_at"`
}

type mcpDecisionResponse struct {
	RedirectTo string `json:"redirect_to"`
}

// consentFail answers the consent routes' refusals: a request that has gone
// is a 404 that says so in words a person can act on.
func (s *Server) consentFail(w http.ResponseWriter, r *http.Request, doing string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "that sign-in request has expired or has already been answered; start the connection again from the client")
	case errors.Is(err, auth.ErrInvalidInput):
		unprocessable(w, err.Error(), nil)
	default:
		s.internal(w, r, doing, err)
	}
}

func (s *Server) handleGetMCPRequest(w http.ResponseWriter, r *http.Request) {
	id := Identity(r.Context())
	v, err := s.auth.DescribeAuthorization(r.Context(), id, chiURLParam(r, "id"))
	if err != nil {
		s.consentFail(w, r, "reading an MCP sign-in request", err)
		return
	}
	writeJSON(w, http.StatusOK, mcpConsentResponse{
		ID: v.Request.ID,
		Client: mcpConsentClient{
			ID: v.Client.ID, ClientID: v.Client.ClientID, Name: v.Client.Name, Kind: v.Client.Kind, ClientURI: v.Client.ClientURI,
		},
		RedirectURI:    v.Request.RedirectURI,
		RedirectHost:   v.RedirectHost,
		LoopbackOnly:   v.LoopbackOnly,
		Resource:       v.Request.Resource,
		RequestedScope: v.Request.Scope,
		Roles:          v.Roles,
		SuggestedRole:  v.SuggestedRole,
		YourRole:       id.Role,
		ExpiresAt:      v.Request.ExpiresAt,
	})
}

func (s *Server) handleApproveMCPRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role store.Role `json:"role"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	d, err := s.auth.ApproveAuthorization(r.Context(), s.oauthPolicy(r), Identity(r.Context()), chiURLParam(r, "id"), in.Role)
	if err != nil {
		s.consentFail(w, r, "approving an MCP connection", err)
		return
	}
	writeJSON(w, http.StatusOK, mcpDecisionResponse{RedirectTo: d.RedirectTo})
}

func (s *Server) handleDenyMCPRequest(w http.ResponseWriter, r *http.Request) {
	d, err := s.auth.DenyAuthorization(r.Context(), s.oauthPolicy(r), Identity(r.Context()), chiURLParam(r, "id"))
	if err != nil {
		s.consentFail(w, r, "declining an MCP connection", err)
		return
	}
	writeJSON(w, http.StatusOK, mcpDecisionResponse{RedirectTo: d.RedirectTo})
}

// ---------------------------------------------------------------------------
// Connections and clients
// ---------------------------------------------------------------------------

type mcpConnectionResponse struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	ClientKind string     `json:"client_kind"`
	UserID     string     `json:"user_id"`
	Username   string     `json:"username"`
	Role       store.Role `json:"role"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func newMCPConnection(g *store.OAuthGrant) mcpConnectionResponse {
	return mcpConnectionResponse{
		ID: g.ID, ClientID: g.ClientID, ClientName: g.ClientName, ClientKind: g.ClientKind,
		UserID: g.UserID, Username: g.Username, Role: g.Role, Scope: g.Scope,
		CreatedAt: g.CreatedAt, LastUsedAt: g.LastUsedAt,
	}
}

// connectionOwner is the signed-in person's own user ID, or "" with the refusal
// already written when the caller is not a person.
func connectionOwner(w http.ResponseWriter, r *http.Request) string {
	id := Identity(r.Context())
	if id == nil || id.Kind != auth.KindUser || !store.HasPrefix(id.ID, store.PrefixUser) {
		forbidden(w, "MCP connections belong to a person's account; sign in to see yours")
		return ""
	}
	return id.ID
}

func (s *Server) listMCPConnections(w http.ResponseWriter, r *http.Request, userID string) {
	gs, err := s.auth.ListMCPConnections(r.Context(), userID)
	if err != nil {
		s.internal(w, r, "listing MCP connections", err)
		return
	}
	out := make([]mcpConnectionResponse, 0, len(gs))
	for _, g := range gs {
		out = append(out, newMCPConnection(g))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

func (s *Server) handleListOwnMCPConnections(w http.ResponseWriter, r *http.Request) {
	if uid := connectionOwner(w, r); uid != "" {
		s.listMCPConnections(w, r, uid)
	}
}

func (s *Server) handleRevokeOwnMCPConnection(w http.ResponseWriter, r *http.Request) {
	uid := connectionOwner(w, r)
	if uid == "" {
		return
	}
	if err := s.auth.RevokeMCPConnection(r.Context(), Identity(r.Context()), chiURLParam(r, "id"), uid); err != nil {
		s.fail(w, r, "revoking an MCP connection", err)
		return
	}
	noContent(w)
}

func (s *Server) handleListMCPConnections(w http.ResponseWriter, r *http.Request) {
	s.listMCPConnections(w, r, "")
}

func (s *Server) handleRevokeMCPConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.RevokeMCPConnection(r.Context(), Identity(r.Context()), chiURLParam(r, "id"), ""); err != nil {
		s.fail(w, r, "revoking an MCP connection", err)
		return
	}
	noContent(w)
}

type mcpClientResponse struct {
	ID              string     `json:"id"`
	ClientID        string     `json:"client_id"`
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	ClientURI       string     `json:"client_uri,omitempty"`
	RedirectURIs    []string   `json:"redirect_uris"`
	Confidential    bool       `json:"confidential"`
	SecretPrefix    string     `json:"secret_prefix,omitempty"`
	SecretRotatedAt *time.Time `json:"secret_rotated_at,omitempty"`
	CreatedBy       string     `json:"created_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	Connections     int        `json:"connections"`
	// ClientSecret is set once, in the response that creates or rotates it.
	ClientSecret string `json:"client_secret,omitempty"`
}

func newMCPClient(c *store.OAuthClient, connections int) mcpClientResponse {
	uris := []string(c.RedirectURIs)
	if uris == nil {
		uris = []string{}
	}
	return mcpClientResponse{
		ID: c.ID, ClientID: c.ClientID, Kind: c.Kind, Name: c.Name, ClientURI: c.ClientURI, RedirectURIs: uris,
		Confidential: c.Confidential(), SecretPrefix: c.SecretPrefix, SecretRotatedAt: c.SecretRotatedAt,
		CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt, RevokedAt: c.RevokedAt,
		Connections: connections,
	}
}

func (s *Server) handleListMCPClients(w http.ResponseWriter, r *http.Request) {
	cs, err := s.ctrl.Store().ListOAuthClients(r.Context())
	if err != nil {
		s.internal(w, r, "listing MCP clients", err)
		return
	}
	counts, err := s.ctrl.Store().CountOAuthGrantsByClient(r.Context())
	if err != nil {
		s.internal(w, r, "counting MCP connections", err)
		return
	}
	out := make([]mcpClientResponse, 0, len(cs))
	for _, c := range cs {
		out = append(out, newMCPClient(c, counts[c.ID]))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

func (s *Server) handleCreateMCPClient(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string   `json:"name"`
		RedirectURIs []string `json:"redirect_uris"`
		Confidential bool     `json:"confidential"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, secret, err := s.auth.CreateMCPClient(r.Context(), Identity(r.Context()), auth.NewMCPClient{
		Name: in.Name, RedirectURIs: in.RedirectURIs, Confidential: in.Confidential,
	})
	if errors.Is(err, auth.ErrInvalidInput) {
		unprocessable(w, err.Error(), nil)
		return
	}
	if err != nil {
		s.internal(w, r, "creating an MCP client", err)
		return
	}
	out := newMCPClient(c, 0)
	out.ClientSecret = secret
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleRotateMCPClientSecret(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	// Counted before the rotation, because once the old secret is gone the new
	// one must reach the caller: a failed count after it would answer 500 and
	// strand a secret nobody can read. Rotating removes no connection, so the
	// number is the same either side of it; a count that fails reads as none
	// rather than costing the operator the secret.
	counts, cerr := s.ctrl.Store().CountOAuthGrantsByClient(r.Context())
	if cerr != nil {
		s.logger(r).Warn("could not count an MCP client's connections before rotating its secret", "client", id, "error", cerr)
	}
	c, secret, err := s.auth.RotateMCPClientSecret(r.Context(), Identity(r.Context()), id)
	if errors.Is(err, auth.ErrInvalidInput) {
		unprocessable(w, err.Error(), nil)
		return
	}
	if err != nil {
		s.fail(w, r, "rotating an MCP client's secret", err)
		return
	}
	out := newMCPClient(c, counts[c.ID])
	out.ClientSecret = secret
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeMCPClient(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.RevokeMCPClient(r.Context(), Identity(r.Context()), chiURLParam(r, "id")); err != nil {
		s.fail(w, r, "revoking an MCP client", err)
		return
	}
	noContent(w)
}

// truncateForLog cuts s to at most n bytes on a rune boundary, marking the
// cut, so a value a caller chose cannot make one log line as long as it likes.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
