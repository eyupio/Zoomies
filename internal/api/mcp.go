package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/mcp"
	"github.com/eyupio/zoomies/internal/version"
)

// mcpToolActions is the action each MCP tool that changes the fleet needs. A
// token whose role reaches it is offered the tool; one whose role does not is
// not, and is told why if its agent asks anyway. The controller still decides
// each call on its own route -- this only keeps the list an agent chooses from
// honest.
var mcpToolActions = map[string]auth.Action{
	"rerun_job":       auth.ActionJobsRerun,
	"drain_runner":    auth.ActionRunnersDrain,
	"update_pool":     auth.ActionPoolsWrite,
	"apply_remedy":    auth.ActionProblemsApply,
	"update_host":     auth.ActionHostsWrite,
	"context_publish": auth.ActionContextPublish,

	// The administrator tools: offered only to an administrator, and only while
	// security.mcp_admin_tools is on. See mcp.IsAdminTool.
	"edit_host":           auth.ActionSettingsWrite,
	"clear_host_throttle": auth.ActionSettingsWrite,
	"get_settings":        auth.ActionSettingsRead,
	"update_settings":     auth.ActionSettingsWrite,
}

// mcpResponseLimit bounds what one tool call reads back from a route. It is
// the stdio server's log limit: a runner's log is the only answer that is ever
// large, and the tool keeps only its tail.
const mcpResponseLimit = 32 << 20

// handleMCP is the fleet over MCP's Streamable HTTP transport, for an agent
// that can reach the controller but cannot, or would rather not, launch
// `zoomies mcp` beside itself -- a cloud session, a hosted agent.
//
// It adds no authority. The caller's bearer token is resolved exactly as the
// API resolves it, and every tool call is handed back to this server's own
// router as the documented route with that same token, so it passes through
// the same role check, scope check, audit trail and access log that
// `zoomies jobs list` does. A session cookie is not accepted: the endpoint is
// for agents, and refusing the cookie means a page the operator happens to
// visit cannot drive it, whatever the browser sends along.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// The specification asks a server to check Origin, so that a page
	// rebinding its own name to this address cannot reach a server that
	// trusts where a request came from. A bearer token already stops that;
	// this is the second lock the specification asks for, and an agent sends
	// no Origin at all.
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !s.originAllowed(origin) && !s.sameOriginAsRequest(r, origin) {
		forbidden(w, "this MCP request came from "+origin+", which is not this controller's origin. "+
			"Add it to server.allowed_origins if that is deliberate.")
		return
	}
	id, err := s.authenticateMCP(r)
	if err != nil {
		// Before the challenge: on a 500 it would send the client off to start
		// a sign-in that cannot help.
		if errors.Is(err, auth.ErrAuthBackend) {
			s.internal(w, r, "checking your credentials", err)
			return
		}
		// The challenge is what tells an MCP client to ask for credentials
		// rather than give up. With OAuth on it names the metadata that starts
		// a browser sign-in; with it off, an API token is the only way in.
		errCode := ""
		if hasBearer(r) {
			errCode = "invalid_token"
		}
		w.Header().Set("WWW-Authenticate", s.mcpChallenge(r, errCode))
		msg := err.Error() + "; send an API token as Authorization: Bearer zoo_... -- " +
			"create one with `zoomies tokens create --role viewer` or on the API tokens settings page"
		if s.cfg().MCPOAuthEnabled() {
			msg = err.Error() + "; sign in through the client's OAuth flow, whose metadata is at " + s.resourceMetadataURL(r) +
				", or send an API token as Authorization: Bearer zoo_..."
		}
		unauthorized(w, msg)
		return
	}
	if info := infoFrom(r.Context()); info != nil {
		info.identity = id
		info.log = info.log.With("identity", id.String())
	}

	mcp.New(inProcessAPI{s: s, from: r, as: id}, mcp.Options{
		Offer: func(tool string) bool {
			_, ok := mcpMissingAction(id, tool)
			return ok && (!mcp.IsAdminTool(tool) || s.cfg().Security.MCPAdminTools)
		},
		Refusal: func(tool string) string {
			missing, ok := mcpMissingAction(id, tool)
			if ok && mcp.IsAdminTool(tool) {
				return tool + " is an administrator tool, and this controller does not offer those over MCP. " +
					"An administrator turns it on at Settings, Security, \"Offer administrator tools over MCP\" (security.mcp_admin_tools)."
			}
			if id.Kind == auth.KindConnection {
				return tool + " changes the fleet, and is not offered to this connection: " + auth.Explain(id, missing) +
					". Disconnect and connect again choosing the operator role to be offered it."
			}
			return tool + " changes the fleet, and is not offered to this token: " + auth.Explain(id, missing)
		},
	}).ServeHTTP(w, r)
}

// mcpMissingAction says whether the caller may use a tool and, when not, the action
// they lack. apply_remedy is the one tool that needs two: the route's own
// problems.apply and then the update it makes, which is a pool's or a host's. A
// token with the first alone would be offered the tool and refused when it called it,
// which is not the honest list the tools list promises.
func mcpMissingAction(id *auth.Identity, tool string) (auth.Action, bool) {
	a, known := mcpToolActions[tool]
	if !known {
		return a, false
	}
	if !auth.Allowed(id, a) {
		return a, false
	}
	if tool == "apply_remedy" && !auth.Allowed(id, auth.ActionPoolsWrite) && !auth.Allowed(id, auth.ActionHostsWrite) {
		return auth.ActionPoolsWrite, false
	}
	return a, true
}

// authenticateMCP resolves /mcp's caller: an MCP access token when OAuth is
// on and the credential is one, and otherwise exactly as the API resolves a
// bearer token.
func (s *Server) authenticateMCP(r *http.Request) (*auth.Identity, error) {
	header := r.Header.Get("Authorization")
	if tok := auth.BearerToken(header); auth.IsMCPAccessToken(tok) && s.cfg().MCPOAuthEnabled() && !s.cfg().Security.DisableAuth {
		return s.auth.AuthenticateMCP(r.Context(), s.oauthPolicy(r), tok, ClientIP(r.Context()))
	}
	return s.auth.Authenticate(r.Context(), auth.AuthInput{Authorization: header, IP: ClientIP(r.Context())})
}

type contextCredentialCheck struct{}

func inProcessIdentity(r *http.Request) *auth.Identity {
	id, _ := r.Context().Value(ctxInProcess).(*auth.Identity)
	return id
}

// inProcessAPI is the REST API as the MCP tools see it from inside the
// controller: each call is a request to this server's own router, carrying the
// MCP request's credential and client address, so nothing about it differs
// from the same call made over the network except the network.
type inProcessAPI struct {
	s    *Server
	from *http.Request
	// as is the /mcp caller. An MCP connection's token is not forwarded: it
	// is accepted on /mcp alone, and the specification forbids passing it on,
	// so the route sees the connection's identity instead.
	as *auth.Identity
	// direct is a caller that was already authenticated by this server, such
	// as the assistant acting for the person chatting: its identity is handed
	// to the route as it is, and no credential is forwarded, so what the route
	// allows is what that person is allowed and nothing about the request that
	// carried it matters.
	direct bool
}

func (a inProcessAPI) Call(ctx context.Context, method, path string, q url.Values) ([]byte, error) {
	return a.do(ctx, method, path, q, "application/json", nil)
}

func (a inProcessAPI) CallBody(ctx context.Context, method, path string, q url.Values, body []byte) ([]byte, error) {
	return a.do(ctx, method, path, q, "application/json", body)
}

func (a inProcessAPI) Stream(ctx context.Context, path, accept string) (io.ReadCloser, error) {
	// A log download ends when the relay goes quiet, so reading it whole is
	// what the stdio server's client does too; there is no connection here to
	// hold open for it.
	body, err := a.do(ctx, http.MethodGet, path, nil, accept, nil)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (a inProcessAPI) do(ctx context.Context, method, path string, q url.Values, accept string, body []byte) ([]byte, error) {
	target := "/api/v1" + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	// The tool's context descends from the /mcp request's, which carries the
	// router's own state for that request; left there, the router would take
	// this for the rest of that dispatch rather than a request of its own.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	// The tools never send confirm=true, so a refusal that says to send it again with
	// it is advice an agent cannot follow, and one that loops it.
	ctx = noConfirm(ctx)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The address and the proxy headers go along so that the client address
	// the audit trail records is the agent's, resolved through the same
	// trusted proxies, rather than this process's own.
	req.RemoteAddr = a.from.RemoteAddr
	req.Host = a.from.Host
	req.TLS = a.from.TLS
	forward := []string{"Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-IP"}
	if a.direct {
		forward = forward[1:]
		req = req.WithContext(context.WithValue(req.Context(), ctxInProcess, a.as))
	} else if a.as != nil && a.as.Kind == auth.KindConnection {
		forward = forward[1:]
		req = req.WithContext(context.WithValue(req.Context(), ctxInProcess, a.as))
		req = req.WithContext(context.WithValue(req.Context(), contextCredentialCheck{}, func(ctx context.Context) (*auth.Identity, error) { return a.s.authenticateMCP(a.from.WithContext(ctx)) }))
	}
	for _, h := range forward {
		if v := a.from.Header.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "zoomies-mcp/"+version.Version)

	rec := &bufferedResponse{header: http.Header{}, limit: mcpResponseLimit}
	a.s.handler.ServeHTTP(rec, req)
	if a.as != nil && a.as.Kind == auth.KindConnection && method != http.MethodGet {
		// The route audits what it did as the connection already; this row
		// says it was an MCP tool that asked, and how the route answered,
		// so a refused attempt is on the record too.
		a.s.auth.Auditor().Act(ctx, a.as, "mcp_connection.call", "mcp_connection", a.as.ID,
			map[string]any{"method": method, "path": target, "status": rec.status})
	}
	if rec.status >= 400 {
		return nil, refusalFrom(rec.status, rec.body.Bytes())
	}
	return rec.body.Bytes(), nil
}

// bufferedResponse collects a route's answer for a tool to read. It stops
// taking bytes at its limit, which ends a log download the way a closed
// connection would.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	limit  int
}

var errResponseFull = errors.New("the answer is larger than an MCP tool reads")

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	room := b.limit - b.body.Len()
	if room <= 0 {
		return 0, errResponseFull
	}
	if len(p) > room {
		b.body.Write(p[:room])
		return room, errResponseFull
	}
	return b.body.Write(p)
}

// mcpRefusal is a route's refusal, carrying the controller's own sentence --
// which role is missing, which field is wrong -- to the agent intact.
type mcpRefusal struct {
	status  int
	message string
}

func (e *mcpRefusal) Error() string   { return e.message }
func (e *mcpRefusal) HTTPStatus() int { return e.status }

func refusalFrom(status int, body []byte) error {
	var envelope errorEnvelope
	msg := ""
	if json.Unmarshal(body, &envelope) == nil {
		msg = envelope.Error.Message
		if envelope.Error.Field != "" {
			msg += " (field " + envelope.Error.Field + ")"
		}
		if envelope.Error.Detail != "" {
			msg += ": " + envelope.Error.Detail
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &mcpRefusal{status: status, message: msg}
}
