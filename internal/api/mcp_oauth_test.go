package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// The whole of OAuth for /mcp, against the real handlers: discovery, a client
// registering, a person signing in and approving it, the code exchanged with
// PKCE, the token used on /mcp, refreshed, replayed and revoked.

const (
	oauthBase     = "https://zoomies.test"
	oauthResource = oauthBase + "/mcp"
	claudeReturn  = "https://claude.ai/api/mcp/auth_callback"
	testVerifier  = "a-verifier-that-is-long-enough-to-be-accepted-by-rfc-7636-0123456789"
)

// oauthHarness is a controller reached over https, which is where OAuth for
// MCP turns itself on.
func oauthHarness(t *testing.T, opts ...func(*config.Config)) *harness {
	t.Helper()
	return newHarness(t, append([]func(*config.Config){func(c *config.Config) { c.Server.ExternalURL = oauthBase }}, opts...)...)
}

// form posts an application/x-www-form-urlencoded body, as an OAuth client
// does at the token and revocation endpoints.
func (h *harness) form(path string, v url.Values, headers map[string]string) *response {
	h.t.Helper()
	all := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, val := range headers {
		all[k] = val
	}
	return h.do(request{method: http.MethodPost, path: path, rawBody: v.Encode(), noOrigin: true, headers: all})
}

func (h *harness) registerClient(redirects ...string) string {
	h.t.Helper()
	return h.registerNamedClient("Claude", redirects...)
}

// registerNamedClient registers a client under a name of its own: the same
// name and redirects are the same client, and are handed the same ID.
func (h *harness) registerNamedClient(name string, redirects ...string) string {
	h.t.Helper()
	resp := h.do(request{method: http.MethodPost, path: "/oauth/register", noOrigin: true,
		body: map[string]any{"client_name": name, "redirect_uris": redirects, "token_endpoint_auth_method": "none"}})
	resp.mustStatus(h.t, http.StatusCreated, "registering a client")
	var out struct {
		ClientID string `json:"client_id"`
		Method   string `json:"token_endpoint_auth_method"`
	}
	resp.into(h.t, &out)
	if out.ClientID == "" || out.Method != "none" {
		h.t.Fatalf("registration must answer a public client ID, got %s", resp.body)
	}
	return out.ClientID
}

// authorize starts a sign-in the way Claude's browser tab does.
func (h *harness) authorize(clientID, redirect string, extra url.Values) *response {
	h.t.Helper()
	q := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {auth.PKCEChallenge(testVerifier)}, "code_challenge_method": {"S256"},
		"state": {"opaque-state"}, "scope": {"mcp:read mcp:operate"}, "resource": {oauthResource},
	}
	for k, v := range extra {
		q[k] = v
	}
	return h.do(request{method: http.MethodGet, path: "/oauth/authorize?" + q.Encode(), noOrigin: true})
}

// requestFrom reads the consent page's request ID out of the authorisation
// endpoint's redirect.
func requestFrom(t *testing.T, resp *response) string {
	t.Helper()
	resp.mustStatus(t, http.StatusFound, "the authorisation endpoint")
	loc, err := url.Parse(resp.header.Get("Location"))
	if err != nil || loc.Path != consentPath || loc.Query().Get("request") == "" {
		t.Fatalf("the authorisation endpoint must send the browser to the consent page, got %q", resp.header.Get("Location"))
	}
	return loc.Query().Get("request")
}

// approve decides a request as the signed-in person and returns the code.
func (h *harness) approve(cookie, requestID string, role store.Role) string {
	h.t.Helper()
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/mcp-requests/" + requestID + "/approve", cookie: cookie,
		body: map[string]any{"role": role}})
	resp.mustStatus(h.t, http.StatusOK, "approving")
	var d mcpDecisionResponse
	resp.into(h.t, &d)
	back, err := url.Parse(d.RedirectTo)
	if err != nil {
		h.t.Fatal(err)
	}
	q := back.Query()
	if q.Get("state") != "opaque-state" || q.Get("iss") != oauthBase || q.Get("code") == "" {
		h.t.Fatalf("the redirect must carry the code, the state and the issuer, got %s", d.RedirectTo)
	}
	return q.Get("code")
}

type tokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (h *harness) exchange(clientID, code string, extra url.Values, headers map[string]string) *response {
	h.t.Helper()
	v := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {claudeReturn},
		"code_verifier": {testVerifier}, "resource": {oauthResource}}
	if clientID != "" {
		v.Set("client_id", clientID)
	}
	for k, val := range extra {
		v[k] = val
	}
	return h.form("/oauth/token", v, headers)
}

func pairFrom(t *testing.T, resp *response) tokenPair {
	t.Helper()
	resp.mustStatus(t, http.StatusOK, "the token endpoint")
	var p tokenPair
	resp.into(t, &p)
	if !strings.HasPrefix(p.AccessToken, auth.MCPAccessTokenPrefix) || !strings.HasPrefix(p.RefreshToken, auth.MCPRefreshTokenPrefix) ||
		p.TokenType != "Bearer" || p.ExpiresIn <= 0 {
		t.Fatalf("the token response is not a bearer pair: %s", resp.body)
	}
	if resp.header.Get("Cache-Control") != "no-store" {
		t.Errorf("a token response must not be cached, got Cache-Control %q", resp.header.Get("Cache-Control"))
	}
	return p
}

func oauthErrorCode(t *testing.T, resp *response) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	resp.into(t, &e)
	return e.Error
}

// connect runs the whole flow for a person and returns the pair.
func (h *harness) connect(cookie string, role store.Role) (string, tokenPair) {
	h.t.Helper()
	client := h.registerClient(claudeReturn)
	code := h.approve(cookie, requestFrom(h.t, h.authorize(client, claudeReturn, nil)), role)
	return client, pairFrom(h.t, h.exchange(client, code, nil, nil))
}

func (h *harness) auditActions() []string {
	h.t.Helper()
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{}, store.Page{Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

// A client that knows nothing but the URL finds everything else from the
// 401: the resource metadata it points at names the authorisation server,
// whose metadata names every endpoint and says PKCE is S256.
func TestMCPOAuthDiscoveryStartsFromTheUnauthorisedAnswer(t *testing.T) {
	h := oauthHarness(t)

	anon := h.mcpCall("", "tools/list", nil)
	anon.mustStatus(t, http.StatusUnauthorized, "no credential")
	want := `resource_metadata="` + oauthBase + `/.well-known/oauth-protected-resource/mcp"`
	if ch := anon.header.Get("WWW-Authenticate"); !strings.Contains(ch, want) || !strings.Contains(ch, `scope="mcp:read mcp:operate"`) {
		t.Errorf("the 401 must point at the resource metadata and name the scopes, got %q", ch)
	}

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		prm := h.do(request{method: http.MethodGet, path: path, noOrigin: true})
		prm.mustStatus(t, http.StatusOK, path)
		doc := prm.json(t)
		if doc["resource"] != oauthResource {
			t.Errorf("%s: resource must be the MCP endpoint exactly, got %v", path, doc["resource"])
		}
		if servers, _ := doc["authorization_servers"].([]any); len(servers) != 1 || servers[0] != oauthBase {
			t.Errorf("%s: the controller is its own authorisation server, got %v", path, doc["authorization_servers"])
		}
	}

	as := h.do(request{method: http.MethodGet, path: "/.well-known/oauth-authorization-server", noOrigin: true})
	as.mustStatus(t, http.StatusOK, "authorisation server metadata")
	var meta struct {
		Issuer        string   `json:"issuer"`
		Authorize     string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Register      string   `json:"registration_endpoint"`
		Revoke        string   `json:"revocation_endpoint"`
		PKCE          []string `json:"code_challenge_methods_supported"`
		AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
		CIMD          bool     `json:"client_id_metadata_document_supported"`
		ISSParam      bool     `json:"authorization_response_iss_parameter_supported"`
		ResponseTypes []string `json:"response_types_supported"`
	}
	as.into(t, &meta)
	if meta.Issuer != oauthBase || meta.Authorize != oauthBase+"/oauth/authorize" || meta.Token != oauthBase+"/oauth/token" ||
		meta.Register != oauthBase+"/oauth/register" || meta.Revoke != oauthBase+"/oauth/revoke" {
		t.Errorf("the endpoints must all be on the issuer, got %+v", meta)
	}
	if !slices.Equal(meta.PKCE, []string{"S256"}) || !slices.Equal(meta.ResponseTypes, []string{"code"}) {
		t.Errorf("S256 PKCE and the code flow only, got %+v", meta)
	}
	// Claude uses a metadata document only when both of these are said.
	if !meta.CIMD || !slices.Contains(meta.AuthMethods, "none") || !meta.ISSParam {
		t.Errorf("metadata documents, public clients and the iss parameter must be advertised, got %+v", meta)
	}
	for _, m := range []string{"client_secret_basic", "client_secret_post"} {
		if !slices.Contains(meta.AuthMethods, m) {
			t.Errorf("%s must be advertised for an administrator's confidential client, got %v", m, meta.AuthMethods)
		}
	}
}

// The setting off, or the deployment on plain HTTP, and none of it is there:
// the 401 goes back to asking for an API token.
func TestMCPOAuthIsAbsentWhenOff(t *testing.T) {
	off := false
	for name, h := range map[string]*harness{
		"plain http by default": newHarness(t),
		"turned off":            oauthHarness(t, func(c *config.Config) { c.Security.MCPOAuth = &off }),
	} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server", "/oauth/authorize"} {
				if r := h.do(request{method: http.MethodGet, path: path, noOrigin: true}); r.status != http.StatusNotFound {
					t.Errorf("GET %s must be 404 with OAuth off, got %d", path, r.status)
				}
			}
			if r := h.do(request{method: http.MethodPost, path: "/oauth/register", noOrigin: true, body: map[string]any{}}); r.status != http.StatusNotFound {
				t.Errorf("registration must be 404 with OAuth off, got %d", r.status)
			}
			anon := h.mcpCall("", "tools/list", nil)
			if ch := anon.header.Get("WWW-Authenticate"); ch != `Bearer realm="zoomies"` {
				t.Errorf("with OAuth off the challenge must not point at metadata that is not there, got %q", ch)
			}
		})
	}
}

// Registration is a public client with PKCE; a redirect that is neither https
// nor loopback is refused; and one address cannot fill the clients table.
func TestMCPOAuthDynamicRegistration(t *testing.T) {
	h := oauthHarness(t)

	for _, tc := range []struct {
		name      string
		redirects []string
		want      int
		code      string
	}{
		{"claude.ai", []string{claudeReturn}, http.StatusCreated, ""},
		{"claude code's loopback", []string{"http://localhost/callback", "http://127.0.0.1/callback"}, http.StatusCreated, ""},
		{"plain http elsewhere", []string{"http://evil.example/callback"}, http.StatusBadRequest, "invalid_redirect_uri"},
		{"a custom scheme", []string{"claude://callback"}, http.StatusBadRequest, "invalid_redirect_uri"},
		{"a fragment", []string{"https://claude.ai/cb#x"}, http.StatusBadRequest, "invalid_redirect_uri"},
		{"none at all", nil, http.StatusBadRequest, "invalid_redirect_uri"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(request{method: http.MethodPost, path: "/oauth/register", noOrigin: true,
				body: map[string]any{"client_name": "Claude", "redirect_uris": tc.redirects}})
			resp.mustStatus(t, tc.want, tc.name)
			if tc.code != "" {
				if got := oauthErrorCode(t, resp); got != tc.code {
					t.Errorf("error %q, want %q", got, tc.code)
				}
			}
		})
	}
	if !slices.Contains(h.auditActions(), "mcp_client.register") {
		t.Errorf("a registration must be audited, got %v", h.auditActions())
	}

	// Each one new, because a client registering again as itself is handed
	// its registration back and never counted.
	limited := false
	for i := range 150 {
		r := h.do(request{method: http.MethodPost, path: "/oauth/register", noOrigin: true,
			body: map[string]any{"client_name": fmt.Sprintf("client %d", i), "redirect_uris": []string{claudeReturn}}})
		if r.status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("registration must be rate limited per address")
	}
}

// The flow as Claude drives it, end to end, and what the person sees and
// can undo afterwards.
func TestMCPOAuthSignsAPersonInAndTheConnectionActsAsThem(t *testing.T) {
	h := oauthHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	idle := h.runner(pool, h.host("boogie"), store.RunnerIdle)
	u, cookie := h.user("olive", store.RoleOperator)

	client := h.registerClient(claudeReturn)
	reqID := requestFrom(t, h.authorize(client, claudeReturn, nil))

	// Nobody signed in: the consent page's data is not given out, and the
	// UI shows the sign-in form in place.
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-requests/" + reqID}).mustStatus(t, http.StatusUnauthorized, "no session")
	// An API token is not a person deciding.
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-requests/" + reqID, token: h.token("t", store.RoleAdmin)}).
		mustStatus(t, http.StatusUnprocessableEntity, "a token")

	view := h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-requests/" + reqID, cookie: cookie})
	view.mustStatus(t, http.StatusOK, "the consent view")
	var consent mcpConsentResponse
	view.into(t, &consent)
	if consent.Client.Name != "Claude" || consent.RedirectHost != "claude.ai" || consent.LoopbackOnly ||
		!slices.Equal(consent.Roles, []store.Role{store.RoleViewer, store.RoleOperator}) || consent.SuggestedRole != store.RoleOperator {
		t.Errorf("the consent view must name the client, its redirect host and the roles on offer, got %+v", consent)
	}

	code := h.approve(cookie, reqID, store.RoleOperator)
	pair := pairFrom(t, h.exchange(client, code, nil, nil))
	if pair.Scope != "mcp:read mcp:operate" {
		t.Errorf("an operator connection carries both scopes, got %q", pair.Scope)
	}

	names := h.mcpToolNames(pair.AccessToken)
	if !slices.Contains(names, "drain_runner") || !slices.Contains(names, "get_job") {
		t.Errorf("an operator connection is offered the action tools, got %v", names)
	}
	if r := h.mcpTool(pair.AccessToken, "drain_runner", map[string]any{"runner_id": idle.ID}); r.IsError {
		t.Fatalf("draining through the connection failed: %s", resultText(r))
	}
	if after, _ := h.st.GetRunner(h.ctx, idle.ID); after.State != store.RunnerDraining {
		t.Errorf("the drain must have happened, runner is %s", after.State)
	}
	actions := h.auditActions()
	for _, want := range []string{"mcp_connection.grant", "mcp_connection.call"} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit must record %s, got %v", want, actions)
		}
	}

	var mine list[mcpConnectionResponse]
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-connections", cookie: cookie}).into(t, &mine)
	if len(mine.Items) != 1 || mine.Items[0].UserID != u.ID || mine.Items[0].ClientName != "Claude" || mine.Items[0].Role != store.RoleOperator {
		t.Fatalf("the person must see their connection, got %+v", mine.Items)
	}

	// Somebody else cannot end it by its ID from their own account page.
	_, other := h.user("oscar", store.RoleOperator)
	h.do(request{method: http.MethodDelete, path: "/api/v1/auth/mcp-connections/" + mine.Items[0].ID, cookie: other}).
		mustStatus(t, http.StatusNotFound, "somebody else's connection")

	h.do(request{method: http.MethodDelete, path: "/api/v1/auth/mcp-connections/" + mine.Items[0].ID, cookie: cookie}).
		mustStatus(t, http.StatusNoContent, "disconnecting")
	h.mcpCall(pair.AccessToken, "tools/list", nil).mustStatus(t, http.StatusUnauthorized, "a disconnected token")
	if r := h.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {pair.RefreshToken}, "client_id": {client}}, nil); r.status != http.StatusBadRequest {
		t.Errorf("a disconnected connection's refresh token must be refused, got %d", r.status)
	}
	if !slices.Contains(h.auditActions(), "mcp_connection.revoke") {
		t.Error("disconnecting must be audited")
	}
}

// Every way an exchange can be wrong, each refused with the error code RFC
// 6749 names.
func TestMCPOAuthRefusesAWrongExchange(t *testing.T) {
	h := oauthHarness(t)
	_, cookie := h.user("olive", store.RoleOperator)
	client := h.registerClient(claudeReturn)
	other := h.registerNamedClient("Another client", claudeReturn)
	fresh := func() string {
		return h.approve(cookie, requestFrom(t, h.authorize(client, claudeReturn, nil)), store.RoleViewer)
	}

	for _, tc := range []struct {
		name  string
		code  func() string
		as    string
		extra url.Values
		want  string
	}{
		{"a wrong verifier", fresh, client, url.Values{"code_verifier": {"another-verifier-that-is-long-enough-to-pass-the-length-check-000"}}, "invalid_grant"},
		{"no verifier", fresh, client, url.Values{"code_verifier": {""}}, "invalid_request"},
		{"a different redirect", fresh, client, url.Values{"redirect_uri": {"https://claude.ai/elsewhere"}}, "invalid_grant"},
		{"a different resource", fresh, client, url.Values{"resource": {"https://elsewhere.test/mcp"}}, "invalid_target"},
		{"another client", fresh, other, nil, "invalid_grant"},
		{"a made-up code", func() string { return auth.MCPCodePrefix + "nothing" }, client, nil, "invalid_grant"},
		{"no grant type", fresh, client, url.Values{"grant_type": {""}}, "invalid_request"},
		{"client credentials", fresh, client, url.Values{"grant_type": {"client_credentials"}}, "unsupported_grant_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.exchange(tc.as, tc.code(), tc.extra, nil)
			if resp.status != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", resp.status, resp.body)
			}
			if got := oauthErrorCode(t, resp); got != tc.want {
				t.Errorf("error %q, want %q", got, tc.want)
			}
		})
	}

	// A code works once. The second presentation means somebody else has it,
	// so what the first one bought is revoked too.
	code := fresh()
	first := pairFrom(t, h.exchange(client, code, nil, nil))
	replay := h.exchange(client, code, nil, nil)
	if replay.status != http.StatusBadRequest || oauthErrorCode(t, replay) != "invalid_grant" {
		t.Fatalf("a reused code must be invalid_grant, got %d %s", replay.status, replay.body)
	}
	h.mcpCall(first.AccessToken, "tools/list", nil).mustStatus(t, http.StatusUnauthorized, "a token bought with a replayed code")
	if !slices.Contains(h.auditActions(), "mcp_connection.replay") {
		t.Error("a replayed code must be audited")
	}
	if r := h.form("/oauth/token", url.Values{"grant_type": {"authorization_code"}}, map[string]string{"Content-Type": "application/json"}); r.status != http.StatusBadRequest {
		t.Errorf("a token request that is not a form must be refused, got %d", r.status)
	}
}

// The authorisation endpoint never sends a person to a redirect the client did
// not register; everything else it refuses goes back to the client with the
// error, the state and the issuer.
func TestMCPOAuthAuthorizationRefusals(t *testing.T) {
	h := oauthHarness(t)
	client := h.registerClient(claudeReturn)

	for _, tc := range []struct {
		name     string
		clientID string
		redirect string
		extra    url.Values
		// here: the refusal is shown on the controller's own page.
		here bool
		want string
	}{
		{"an unregistered redirect", client, "https://evil.example/callback", nil, true, "invalid_request"},
		{"an unknown client", "oac_nosuchclient", claudeReturn, nil, true, "invalid_client"},
		{"no PKCE", client, claudeReturn, url.Values{"code_challenge": {""}}, false, "invalid_request"},
		{"plain PKCE", client, claudeReturn, url.Values{"code_challenge_method": {"plain"}}, false, "invalid_request"},
		{"another resource", client, claudeReturn, url.Values{"resource": {"https://elsewhere.test/mcp"}}, false, "invalid_target"},
		{"an unknown scope", client, claudeReturn, url.Values{"scope": {"admin"}}, false, "invalid_scope"},
		{"the implicit flow", client, claudeReturn, url.Values{"response_type": {"token"}}, false, "unsupported_response_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.authorize(tc.clientID, tc.redirect, tc.extra)
			resp.mustStatus(t, http.StatusFound, tc.name)
			loc, _ := url.Parse(resp.header.Get("Location"))
			if tc.here {
				if loc.Host != "" || loc.Path != consentPath || loc.Query().Get("error") != tc.want {
					t.Fatalf("must be shown on the controller's own page, got %s", loc)
				}
				if loc.Query().Has("error_description") {
					// Text in that address is rendered on this controller;
					// the page writes its own sentence from the code.
					t.Errorf("the consent page's address must carry the code alone, got %s", loc)
				}
				return
			}
			if !strings.HasPrefix(loc.String(), claudeReturn) {
				t.Fatalf("must go back to the client, got %s", loc)
			}
			q := loc.Query()
			if q.Get("error") != tc.want || q.Get("state") != "opaque-state" || q.Get("iss") != oauthBase {
				t.Errorf("want error %s with state and iss, got %s", tc.want, loc)
			}
		})
	}

	// A person who declines sends the client access_denied.
	_, cookie := h.user("olive", store.RoleViewer)
	reqID := requestFrom(t, h.authorize(client, claudeReturn, nil))
	deny := h.do(request{method: http.MethodPost, path: "/api/v1/auth/mcp-requests/" + reqID + "/deny", cookie: cookie})
	deny.mustStatus(t, http.StatusOK, "declining")
	var d mcpDecisionResponse
	deny.into(t, &d)
	if !strings.Contains(d.RedirectTo, "error=access_denied") {
		t.Errorf("declining must send access_denied, got %s", d.RedirectTo)
	}
	h.do(request{method: http.MethodPost, path: "/api/v1/auth/mcp-requests/" + reqID + "/approve", cookie: cookie}).
		mustStatus(t, http.StatusNotFound, "a request already answered")
}

// Rotation: each refresh spends the refresh token and hands out a new pair;
// presenting a spent one again revokes the whole family, the legitimate
// client's newest tokens included.
func TestMCPOAuthRefreshRotatesAndAReplayEndsTheConnection(t *testing.T) {
	h := oauthHarness(t)
	_, cookie := h.user("olive", store.RoleViewer)
	client, pair := h.connect(cookie, store.RoleViewer)

	refresh := func(tok string) *response {
		return h.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok}, "client_id": {client}, "resource": {oauthResource}}, nil)
	}
	second := pairFrom(t, refresh(pair.RefreshToken))
	if second.RefreshToken == pair.RefreshToken || second.AccessToken == pair.AccessToken {
		t.Fatal("a refresh must hand out a new pair")
	}
	if names := h.mcpToolNames(second.AccessToken); !slices.Contains(names, "get_job") {
		t.Errorf("the new access token must work, got %v", names)
	}
	if r := h.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {second.RefreshToken}, "client_id": {client}, "scope": {"mcp:operate"}}, nil); oauthErrorCode(t, r) != "invalid_scope" {
		t.Errorf("a refresh cannot widen a viewer connection, got %s", r.body)
	}

	replay := refresh(pair.RefreshToken)
	if replay.status != http.StatusBadRequest || oauthErrorCode(t, replay) != "invalid_grant" {
		t.Fatalf("a spent refresh token must be invalid_grant, got %d %s", replay.status, replay.body)
	}
	h.mcpCall(second.AccessToken, "tools/list", nil).mustStatus(t, http.StatusUnauthorized, "the family after a replay")
	if r := refresh(second.RefreshToken); r.status != http.StatusBadRequest {
		t.Errorf("the newest refresh token must go with the family, got %d", r.status)
	}
}

// RFC 7009: revoking the refresh token ends the connection; the endpoint says
// the same thing for a token it has never seen.
func TestMCPOAuthRevocationEndpoint(t *testing.T) {
	h := oauthHarness(t)
	_, cookie := h.user("olive", store.RoleViewer)
	client, pair := h.connect(cookie, store.RoleViewer)

	h.form("/oauth/revoke", url.Values{"token": {"zoomcpr_nothing"}, "client_id": {client}}, nil).mustStatus(t, http.StatusOK, "an unknown token")
	h.form("/oauth/revoke", url.Values{"token": {pair.RefreshToken}, "client_id": {client}}, nil).mustStatus(t, http.StatusOK, "revoking")
	h.mcpCall(pair.AccessToken, "tools/list", nil).mustStatus(t, http.StatusUnauthorized, "after revocation")
}

// A person gives a connection at most their own role, and never more than
// operator; a connection follows its person down if they are demoted.
func TestMCPOAuthRoleCeiling(t *testing.T) {
	h := oauthHarness(t)
	viewer, cookie := h.user("vera", store.RoleViewer)
	client := h.registerClient(claudeReturn)
	reqID := requestFrom(t, h.authorize(client, claudeReturn, nil))

	var consent mcpConsentResponse
	h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-requests/" + reqID, cookie: cookie}).into(t, &consent)
	if !slices.Equal(consent.Roles, []store.Role{store.RoleViewer}) || consent.SuggestedRole != store.RoleViewer {
		t.Errorf("a viewer can only give viewer, got %+v", consent)
	}
	for _, role := range []store.Role{store.RoleOperator, store.RoleAdmin} {
		h.do(request{method: http.MethodPost, path: "/api/v1/auth/mcp-requests/" + reqID + "/approve", cookie: cookie, body: map[string]any{"role": role}}).
			mustStatus(t, http.StatusUnprocessableEntity, "a viewer giving "+string(role))
	}
	pair := pairFrom(t, h.exchange(client, h.approve(cookie, reqID, store.RoleViewer), nil, nil))
	if names := h.mcpToolNames(pair.AccessToken); slices.Contains(names, "drain_runner") {
		t.Errorf("a viewer connection must not be offered actions, got %v", names)
	}
	if r := h.mcpTool(pair.AccessToken, "drain_runner", map[string]any{"runner_id": "run_x"}); !r.IsError || !strings.Contains(resultText(r), "operator role") {
		t.Errorf("asking anyway must say to connect again as operator, got %s", resultText(r))
	}

	admin, adminCookie := h.user("ada", store.RoleAdmin)
	_, adminPair := h.connect(adminCookie, store.RoleOperator)
	_ = viewer
	admin.Role = store.RoleViewer
	if err := h.st.UpdateUser(h.ctx, admin); err != nil {
		t.Fatal(err)
	}
	if names := h.mcpToolNames(adminPair.AccessToken); slices.Contains(names, "drain_runner") {
		t.Errorf("a demoted person's connection must be demoted with them, got %v", names)
	}
}

// The token is for /mcp. The REST API refuses it and says so, and /mcp
// refuses one issued for another address.
func TestMCPOAuthTokenWorksOnMCPAlone(t *testing.T) {
	h := oauthHarness(t)
	_, cookie := h.user("olive", store.RoleOperator)
	_, pair := h.connect(cookie, store.RoleOperator)

	for _, path := range []string{"/api/v1/pools", "/api/v1/auth/session", "/api/v1/runners"} {
		r := h.do(request{method: http.MethodGet, path: path, token: pair.AccessToken})
		if r.status != http.StatusUnauthorized || !strings.Contains(r.errorMessage(t), "/mcp only") {
			t.Errorf("GET %s with an MCP token must be 401 naming /mcp, got %d %s", path, r.status, r.body)
		}
	}

	h.cfg.Server.ExternalURL = "https://renamed.test"
	r := h.mcpCall(pair.AccessToken, "tools/list", nil)
	if r.status != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("a token for another resource must be 401 invalid_token, got %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}
}

// An administrator's confidential client: its secret is checked by either
// method RFC 6749 names, a wrong or missing one is refused, and a rotated one
// stops working at once. PKCE is still required.
func TestMCPOAuthConfidentialClient(t *testing.T) {
	h := oauthHarness(t)
	_, admin := h.user("ada", store.RoleAdmin)

	created := h.do(request{method: http.MethodPost, path: "/api/v1/mcp-clients", cookie: admin,
		body: map[string]any{"name": "Claude for the team", "confidential": true}})
	created.mustStatus(t, http.StatusCreated, "creating a client")
	var c mcpClientResponse
	created.into(t, &c)
	if !c.Confidential || !strings.HasPrefix(c.ClientSecret, auth.MCPClientSecretPrefix) || !slices.Equal(c.RedirectURIs, []string{claudeReturn}) {
		t.Fatalf("a confidential client with Claude's callback and a secret shown once, got %+v", c)
	}

	basic := func(id, secret string) map[string]string {
		return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(id)+":"+url.QueryEscape(secret)))}
	}
	code := func() string {
		return h.approve(admin, requestFrom(t, h.authorize(c.ClientID, claudeReturn, nil)), store.RoleViewer)
	}

	pairFrom(t, h.exchange("", code(), nil, basic(c.ClientID, c.ClientSecret)))
	pairFrom(t, h.exchange(c.ClientID, code(), url.Values{"client_secret": {c.ClientSecret}}, nil))

	for _, tc := range []struct {
		name    string
		id      string
		extra   url.Values
		headers map[string]string
	}{
		{"a wrong secret by basic", "", nil, basic(c.ClientID, "zoocs_wrong")},
		{"a wrong secret by post", c.ClientID, url.Values{"client_secret": {"zoocs_wrong"}}, nil},
		{"no secret", c.ClientID, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.exchange(tc.id, code(), tc.extra, tc.headers)
			if r.status != http.StatusUnauthorized || oauthErrorCode(t, r) != "invalid_client" {
				t.Errorf("want 401 invalid_client, got %d %s", r.status, r.body)
			}
		})
	}
	if r := h.exchange("", code(), url.Values{"code_verifier": {""}}, basic(c.ClientID, c.ClientSecret)); r.status != http.StatusBadRequest {
		t.Errorf("a confidential client still needs PKCE, got %d", r.status)
	}

	rotated := h.do(request{method: http.MethodPost, path: "/api/v1/mcp-clients/" + c.ID + "/secret", cookie: admin})
	rotated.mustStatus(t, http.StatusOK, "rotating")
	var after mcpClientResponse
	rotated.into(t, &after)
	if after.ClientSecret == "" || after.ClientSecret == c.ClientSecret {
		t.Fatal("rotation must hand out a new secret")
	}
	if after.Connections == 0 {
		// Two connections were made above, and rotating ends neither.
		t.Error("the rotated client's response must count the connections it still has, got 0")
	}
	if r := h.exchange("", code(), nil, basic(c.ClientID, c.ClientSecret)); r.status != http.StatusUnauthorized {
		t.Errorf("the old secret must stop working, got %d", r.status)
	}
	pairFrom(t, h.exchange("", code(), nil, basic(c.ClientID, after.ClientSecret)))

	var clients list[mcpClientResponse]
	h.do(request{method: http.MethodGet, path: "/api/v1/mcp-clients", cookie: admin}).into(t, &clients)
	if len(clients.Items) != 1 || clients.Items[0].ClientSecret != "" || clients.Items[0].Connections == 0 {
		t.Errorf("the list shows the client and its connections and never its secret, got %+v", clients.Items)
	}
	var everyone list[mcpConnectionResponse]
	h.do(request{method: http.MethodGet, path: "/api/v1/mcp-connections", cookie: admin}).into(t, &everyone)
	if len(everyone.Items) == 0 {
		t.Error("an administrator sees every connection")
	}

	h.do(request{method: http.MethodDelete, path: "/api/v1/mcp-clients/" + c.ID, cookie: admin}).mustStatus(t, http.StatusNoContent, "revoking")
	if r := h.exchange("", code2(t, h, admin, c.ClientID), nil, basic(c.ClientID, after.ClientSecret)); r.status != http.StatusUnauthorized {
		t.Errorf("a revoked client must be refused, got %d", r.status)
	}
	for _, want := range []string{"mcp_client.create", "mcp_client.secret_rotate", "mcp_client.revoke"} {
		if !slices.Contains(h.auditActions(), want) {
			t.Errorf("audit must record %s", want)
		}
	}
}

// code2 tries to start a sign-in for a client that may have been revoked, and
// returns a placeholder code when the controller refuses to, which is the
// point being tested.
func code2(t *testing.T, h *harness, cookie, clientID string) string {
	t.Helper()
	resp := h.authorize(clientID, claudeReturn, nil)
	loc, _ := url.Parse(resp.header.Get("Location"))
	if id := loc.Query().Get("request"); id != "" {
		return h.approve(cookie, id, store.RoleViewer)
	}
	return auth.MCPCodePrefix + "refused"
}

// With open registration off, only an administrator's clients may ask: the
// registration endpoint refuses, the metadata stops advertising it and
// metadata documents, and a client that registered earlier is turned away.
func TestMCPOAuthWithRegistrationClosed(t *testing.T) {
	h := oauthHarness(t)
	early := h.registerClient(claudeReturn)
	h.cfg.Security.MCPOpenRegistration = false

	r := h.do(request{method: http.MethodPost, path: "/oauth/register", noOrigin: true, body: map[string]any{"redirect_uris": []string{claudeReturn}}})
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "mcp_open_registration") {
		t.Errorf("registration must be refused naming the setting, got %d %s", r.status, r.body)
	}
	meta := h.do(request{method: http.MethodGet, path: "/.well-known/oauth-authorization-server", noOrigin: true}).json(t)
	if _, ok := meta["registration_endpoint"]; ok || meta["client_id_metadata_document_supported"] != false {
		t.Errorf("closed registration must not be advertised, got %v", meta)
	}
	if loc := h.authorize(early, claudeReturn, nil).header.Get("Location"); !strings.Contains(loc, "error=invalid_client") {
		t.Errorf("a self-registered client must be turned away, got %s", loc)
	}
	if loc := h.authorize("https://claude.ai/oauth/claude-code-client-metadata", "http://localhost:3118/callback", nil).header.Get("Location"); !strings.Contains(loc, "error=invalid_client") {
		t.Errorf("a metadata document must be turned away, got %s", loc)
	}

	_, admin := h.user("ada", store.RoleAdmin)
	var c mcpClientResponse
	h.do(request{method: http.MethodPost, path: "/api/v1/mcp-clients", cookie: admin, body: map[string]any{"name": "Claude"}}).into(t, &c)
	if c.Confidential || c.ClientSecret != "" {
		t.Errorf("a client created without confidential is public, got %+v", c)
	}
	pairFrom(t, h.exchange(c.ClientID, h.approve(admin, requestFrom(t, h.authorize(c.ClientID, claudeReturn, nil)), store.RoleViewer), nil, nil))
}

// The consent routes and connections list only ever describe; they never
// leak a code or a token into a JSON body a page could log.
func TestMCPOAuthConsentViewCarriesNoCredential(t *testing.T) {
	h := oauthHarness(t)
	_, cookie := h.user("olive", store.RoleViewer)
	client := h.registerClient("http://localhost/callback")
	reqID := requestFrom(t, h.authorize(client, "http://localhost:4242/callback", nil))
	body := h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-requests/" + reqID, cookie: cookie})
	var v mcpConsentResponse
	body.into(t, &v)
	if !v.LoopbackOnly || v.RedirectHost != "localhost:4242" {
		t.Errorf("a loopback-only client must be flagged, got %+v", v)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), auth.MCPCodePrefix) || strings.Contains(string(raw), "code_challenge") {
		t.Errorf("the consent view must not carry the challenge or a code: %s", raw)
	}
}

// Single sign-on that begins on the consent screen comes back to it, and the
// return address can name nothing else: it is read from a query string, so
// anything wider would be an open redirect on the sign-in path.
func TestSingleSignOnReturnsOnlyToTheConsentScreen(t *testing.T) {
	for addr, want := range map[string]bool{
		"/oauth/consent?request=oar_abcdefghijklm":                        true,
		"/oauth/consent?request=oar_abcdefghijklm&next=https://evil.test": false,
		"/oauth/consent?request=ses_abcdefghijklm":                        false,
		"https://evil.test/oauth/consent?request=oar_abcdefghijklm":       false,
		"//evil.test/oauth/consent?request=oar_abcdefghijklm":             false,
		"/settings?request=oar_abcdefghijklm":                             false,
		"/":                                                               false,
		"":                                                                false,
	} {
		if got := consentReturn(addr); got != want {
			t.Errorf("consentReturn(%q) = %v, want %v", addr, got, want)
		}
	}
}
