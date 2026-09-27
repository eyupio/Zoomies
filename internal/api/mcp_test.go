package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/mcp"
	"github.com/eyupio/zoomies/internal/store"
)

// mcpCall posts one JSON-RPC request to /mcp the way an MCP client does.
func (h *harness) mcpCall(token, method string, params any) *response {
	h.t.Helper()
	return h.do(request{
		method: http.MethodPost, path: "/mcp", token: token, noOrigin: true,
		body:    map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params},
		headers: map[string]string{"Accept": "application/json, text/event-stream"},
	})
}

type mcpReply struct {
	Result json.RawMessage `json:"result"`
	Error  *mcp.RPCError   `json:"error"`
}

func decodeMCP(t *testing.T, resp *response) mcpReply {
	t.Helper()
	if resp.status != http.StatusOK {
		t.Fatalf("MCP request answered %d: %s", resp.status, resp.body)
	}
	var r mcpReply
	if err := json.Unmarshal(resp.body, &r); err != nil {
		t.Fatalf("the answer was not JSON-RPC: %v\n%s", err, resp.body)
	}
	if r.Error != nil {
		t.Fatalf("protocol error %d: %s", r.Error.Code, r.Error.Message)
	}
	return r
}

func (h *harness) mcpTool(token, name string, args any) mcp.CallResult {
	h.t.Helper()
	var r mcp.CallResult
	if err := json.Unmarshal(decodeMCP(h.t, h.mcpCall(token, "tools/call", map[string]any{"name": name, "arguments": args})).Result, &r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) mcpToolNames(token string) []string {
	h.t.Helper()
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(decodeMCP(h.t, h.mcpCall(token, "tools/list", nil)).Result, &list); err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func resultText(r mcp.CallResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// The endpoint is for agents, which send a token. A session cookie is refused
// even though it would authenticate anywhere else, so that a page the operator
// visits cannot drive an agent's endpoint with the operator's own session.
func TestMCPOverHTTPTakesABearerTokenAndNothingElse(t *testing.T) {
	h := newHarness(t)

	anon := h.mcpCall("", "tools/list", nil)
	if anon.status != http.StatusUnauthorized {
		t.Fatalf("no credential must be 401, got %d: %s", anon.status, anon.body)
	}
	if !strings.HasPrefix(anon.header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("a 401 must carry a Bearer challenge so the client knows to ask for a token, got %q", anon.header.Get("WWW-Authenticate"))
	}
	if !strings.Contains(string(anon.body), "tokens create") {
		t.Errorf("the refusal must say how to get a token: %s", anon.body)
	}

	admin, _ := h.user("alice", store.RoleAdmin)
	cookie := h.do(request{
		method: http.MethodPost, path: "/mcp", cookie: h.session(admin),
		body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
	})
	if cookie.status != http.StatusUnauthorized {
		t.Errorf("a session cookie must not open the MCP endpoint, got %d", cookie.status)
	}

	if names := h.mcpToolNames(h.token("agent", store.RoleViewer)); !slices.Contains(names, "get_job") {
		t.Errorf("a viewer token must be offered the read-only tools, got %v", names)
	}
}

// The actions follow the token: a tool the role cannot use is not offered, so
// the model never plans around it, and asking anyway says which role is
// missing. Every action tool needs an entry, or a new one would be offered to
// nobody without anyone deciding that.
func TestMCPOverHTTPOffersActionsAsFarAsTheTokensRoleReaches(t *testing.T) {
	for _, tool := range mcp.ActionTools() {
		if _, ok := mcpToolActions[tool]; !ok {
			t.Errorf("MCP action tool %s has no entry in mcpToolActions", tool)
		}
	}

	h := newHarness(t)
	viewer := h.token("reader", store.RoleViewer)
	operator := h.token("actor", store.RoleOperator)

	names := h.mcpToolNames(viewer)
	for tool := range mcpToolActions {
		if slices.Contains(names, tool) {
			t.Errorf("%s must not be offered to a viewer token, got %v", tool, names)
		}
	}
	r := h.mcpTool(viewer, "drain_runner", map[string]any{"runner_id": "run_x"})
	if !r.IsError || !strings.Contains(resultText(r), "operator") {
		t.Errorf("asking for an action the role cannot use must name the role, got %s", resultText(r))
	}

	names = h.mcpToolNames(operator)
	for tool := range mcpToolActions {
		if !slices.Contains(names, tool) {
			t.Errorf("%s must be offered to an operator token, got %v", tool, names)
		}
	}
}

// A tool call is the route itself, with the caller's token: the record comes
// back as the API renders it, and a refusal the route makes -- a busy runner
// is not drained without a person confirming it -- reaches the agent intact.
func TestMCPOverHTTPCallsTheRoutesWithTheCallersToken(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	host := h.host("boogie")
	job := h.job(pool, store.JobQueued)
	idle := h.runner(pool, host, store.RunnerIdle)
	busy := h.runner(pool, host, store.RunnerBusy)
	operator := h.token("actor", store.RoleOperator)

	got := h.mcpTool(operator, "get_job", map[string]any{"job_id": job.ID})
	if got.IsError {
		t.Fatalf("get_job failed: %s", resultText(got))
	}
	var doc struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
		Explanation json.RawMessage `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(got.Content[0].Text), &doc); err != nil || doc.Job.ID != job.ID || len(doc.Explanation) == 0 {
		t.Errorf("get_job must return the job and its explanation, got %s (%v)", got.Content[0].Text, err)
	}

	missing := h.mcpTool(operator, "get_job", map[string]any{"job_id": "job_nope"})
	if !missing.IsError || !strings.Contains(resultText(missing), "list_jobs") {
		t.Errorf("an unknown job must say where the IDs come from, got %s", resultText(missing))
	}

	if r := h.mcpTool(operator, "drain_runner", map[string]any{"runner_id": idle.ID}); r.IsError {
		t.Fatalf("draining an idle runner failed: %s", resultText(r))
	}
	if after, _ := h.st.GetRunner(h.ctx, idle.ID); after.State != store.RunnerDraining {
		t.Errorf("the drain must have happened, runner is %s", after.State)
	}

	r := h.mcpTool(operator, "drain_runner", map[string]any{"runner_id": busy.ID})
	if !r.IsError || !strings.Contains(resultText(r), "five minutes") {
		t.Errorf("a busy runner's drain must come back as the route's own refusal, got %s", resultText(r))
	}
	if after, _ := h.st.GetRunner(h.ctx, busy.ID); after.State != store.RunnerBusy {
		t.Errorf("a refused drain must leave the busy runner alone, it is %s", after.State)
	}
}

// What the transport itself answers, before any tool runs.
func TestMCPOverHTTPAnswersTheTransportsOwnCases(t *testing.T) {
	h := newHarness(t)
	viewer := h.token("reader", store.RoleViewer)

	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Instructions    string `json:"instructions"`
	}
	if err := json.Unmarshal(decodeMCP(t, h.mcpCall(viewer, "initialize", map[string]any{"protocolVersion": "2025-06-18"})).Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != "2025-06-18" || !strings.Contains(init.Instructions, "untrusted") {
		t.Errorf("initialize must negotiate and carry the untrusted-data instructions: %+v", init)
	}

	note := h.do(request{method: http.MethodPost, path: "/mcp", token: viewer, noOrigin: true,
		body: map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}})
	if note.status != http.StatusAccepted || len(note.body) != 0 {
		t.Errorf("a notification must be 202 with no body, got %d %q", note.status, note.body)
	}

	get := h.do(request{method: http.MethodGet, path: "/mcp", token: viewer, noOrigin: true})
	if get.status != http.StatusMethodNotAllowed || get.header.Get("Allow") != http.MethodPost {
		t.Errorf("a GET must be 405 naming POST, not the SPA, got %d %q", get.status, get.header.Get("Allow"))
	}

	old := h.do(request{method: http.MethodPost, path: "/mcp", token: viewer, noOrigin: true,
		body:    map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"},
		headers: map[string]string{"MCP-Protocol-Version": "1999-01-01"}})
	if old.status != http.StatusBadRequest {
		t.Errorf("an unsupported MCP-Protocol-Version must be 400, got %d", old.status)
	}

	// The specification asks for the Origin check, against DNS rebinding.
	foreign := h.do(request{method: http.MethodPost, path: "/mcp", token: viewer, origin: "https://evil.example",
		body: map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}})
	if foreign.status != http.StatusForbidden {
		t.Errorf("a request from a foreign origin must be 403, got %d", foreign.status)
	}
	if cache := h.mcpCall(viewer, "ping", nil).header.Get("Cache-Control"); cache != "no-store" {
		t.Errorf("an MCP answer carries fleet data and must not be cached, got %q", cache)
	}
}
