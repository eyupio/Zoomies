package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
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

	// Each tool is offered exactly as far as its own action's role reaches:
	// the fleet's actions are an operator's, while publishing a note is a
	// reader's, gated again on the route by membership and consent.
	names := h.mcpToolNames(viewer)
	for tool, action := range mcpToolActions {
		want := store.RoleViewer.AtLeast(action.MinRole())
		if slices.Contains(names, tool) != want {
			t.Errorf("%s offered to a viewer token = %v, want %v (it needs %s): %v", tool, !want, want, action.MinRole(), names)
		}
	}
	r := h.mcpTool(viewer, "drain_runner", map[string]any{"runner_id": "run_x"})
	if !r.IsError || !strings.Contains(resultText(r), "operator") {
		t.Errorf("asking for an action the role cannot use must name the role, got %s", resultText(r))
	}

	names = h.mcpToolNames(operator)
	for tool := range mcpToolActions {
		if mcp.IsAdminTool(tool) {
			continue
		}
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

// A hundred jobs with their steps overflowed a client's output limit at about
// ten. Without them a full page of a hundred has to fit well inside 25k tokens;
// JSON runs at three bytes a token or worse, so 75 KB is the ceiling.
func TestMCPListJobsWithoutStepsFitsAHundredJobsInAModelsContext(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux")
	h.releaseJobs(pool, "v1.3.3", 100, 10_000, time.Now().Add(-2*time.Hour))
	viewer := h.token("reader", store.RoleViewer)

	const budget = 25_000 * 3
	summary := h.mcpTool(viewer, "list_jobs", map[string]any{"limit": 100})
	if summary.IsError {
		t.Fatalf("list_jobs failed: %s", resultText(summary))
	}
	text := resultText(summary)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Items) != 100 {
		t.Fatalf("want 100 jobs, got %d (%v)", len(page.Items), err)
	}
	if _, has := page.Items[0]["steps"]; has {
		t.Errorf("steps must be off by default for MCP: %v", page.Items[0])
	}
	if len(text) > budget {
		t.Errorf("100 summaries are %d bytes, over the %d-byte proxy for 25k tokens", len(text), budget)
	}
	t.Logf("100 job summaries: %d bytes", len(text))

	full := resultText(h.mcpTool(viewer, "list_jobs", map[string]any{"limit": 10, "include_steps": true}))
	if !strings.Contains(full, `"steps":[{`) {
		t.Errorf("include_steps=true must bring the steps back")
	}
}

func TestMCPJobStatsIsOneCallPerReleaseAndTakesDays(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux")
	now := time.Now()
	h.releaseJobs(pool, "v1.2.0", 3, 20_000, now.Add(-10*24*time.Hour))
	h.releaseJobs(pool, "v1.3.0", 3, 30_000, now.Add(-2*24*time.Hour))
	viewer := h.token("reader", store.RoleViewer)

	res := h.mcpTool(viewer, "job_stats", map[string]any{"group_by": []string{"controller_version"}, "since": "30d"})
	if res.IsError {
		t.Fatalf("job_stats failed: %s", resultText(res))
	}
	var doc struct {
		Groups []struct {
			Keys map[string]string `json:"keys"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(resultText(res)), &doc); err != nil || len(doc.Groups) != 2 {
		t.Fatalf("want a group per release, got %s (%v)", resultText(res), err)
	}
	for _, args := range []map[string]any{
		{"group_by": []string{"repo"}},
		{"since": "last tuesday"},
		{"unknown": true},
	} {
		if r := h.mcpTool(viewer, "job_stats", args); !r.IsError {
			t.Errorf("job_stats accepted %v", args)
		}
	}
}

// An agent that changes a pool's sizing goes through the pool's own route with
// the caller's token, and the pool keeps everything it had: the API replaces
// `resources` whole, so a tool that sent only the share would have cleared the
// pool's smallest runner in the same call.
func TestMCPUpdatePoolChangesTheNamedSettingAndKeepsTheRest(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.DockerMode = store.DockerDinD
	pool.Resources.MinCPUs, pool.Resources.MinMemoryMB = 1, 1536
	pool.Resources.DaemonCPUSharePercent, pool.Resources.DaemonMemorySharePercent = 35, 15
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	operator := h.token("actor", store.RoleOperator)
	viewer := h.token("reader", store.RoleViewer)

	if r := h.mcpTool(viewer, "update_pool", map[string]any{"pool_id": pool.ID, "daemon_cpu_share_percent": 20}); !r.IsError || !strings.Contains(resultText(r), "operator") {
		t.Errorf("a viewer asking to change a pool must be told which role is missing, got %s", resultText(r))
	}

	r := h.mcpTool(operator, "update_pool", map[string]any{"pool_id": pool.ID, "daemon_cpu_share_percent": 20})
	if r.IsError {
		t.Fatalf("update_pool failed: %s", resultText(r))
	}
	after, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	res := after.Resources
	if res.DaemonCPUSharePercent != 20 {
		t.Errorf("the share must have changed, is %d", res.DaemonCPUSharePercent)
	}
	if res.MinCPUs != 1 || res.MinMemoryMB != 1536 || res.DaemonMemorySharePercent != 15 {
		t.Errorf("everything the call did not name must be kept: %+v", res)
	}
	if !strings.Contains(resultText(r), `"daemon_cpu_share_percent":{"before":35,"after":20}`) {
		t.Errorf("the answer must say what changed, got %s", resultText(r))
	}

	// The smallest runner can be lowered on its own, and the shares survive it.
	if r := h.mcpTool(operator, "update_pool", map[string]any{"pool_id": pool.ID, "min_cpus": 0.75, "min_memory_mb": 1024}); r.IsError {
		t.Fatalf("lowering the minimum failed: %s", resultText(r))
	}
	after, _ = h.st.GetPool(h.ctx, pool.ID)
	if after.Resources.MinCPUs != 0.75 || after.Resources.MinMemoryMB != 1024 || after.Resources.DaemonCPUSharePercent != 20 {
		t.Errorf("the minimum must move and the shares stay: %+v", after.Resources)
	}
}

func TestMCPUpdateHostChangesTheNamedSettingAndKeepsTheRest(t *testing.T) {
	h := newHarness(t)
	host := h.host("boogie")
	host.CPUs, host.MemoryMB = 8, 32768
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	profile := store.RunnerProfile{
		Minimum:  store.RunnerSize{CPUs: 1},
		Standard: store.RunnerStandard{CPUs: 6},
		Tmpfs:    store.HostTmpfs{MaxMB: 2048},
	}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &profile}); err != nil {
		t.Fatal(err)
	}
	operator := h.token("actor", store.RoleOperator)

	r := h.mcpTool(operator, "update_host", map[string]any{"host_id": host.ID, "standard_cpus": 2.25, "reserve_cpus": 2})
	if r.IsError {
		t.Fatalf("update_host failed: %s", resultText(r))
	}
	after, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RunnerProfile.Standard.CPUs != 2.25 || after.ReserveCPUs != 2 {
		t.Errorf("the named settings must change: standard %v reserve %d", after.RunnerProfile.Standard.CPUs, after.ReserveCPUs)
	}
	if after.RunnerProfile.Minimum.CPUs != 1 || after.RunnerProfile.Tmpfs.MaxMB != 2048 {
		t.Errorf("the rest of the profile must be kept: %+v", after.RunnerProfile)
	}

	// Stopping a host taking runners is a person's decision, not an agent's.
	if r := h.mcpTool(operator, "update_host", map[string]any{"host_id": host.ID, "capacity": 0}); !r.IsError {
		t.Errorf("a capacity of zero must be refused, got %s", resultText(r))
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.Capacity != host.Capacity {
		t.Errorf("a refused call must change nothing, capacity is %d", got.Capacity)
	}
}

// The administrator tools need two things at once: a role that reaches them and
// the controller's switch. Either alone offers nothing, and an agent that asks
// anyway is told which of the two is missing, so the person can fix the right one.
func TestMCPAdministratorToolsNeedTheSwitchAndTheRole(t *testing.T) {
	admin := func(h *harness) string { return h.token("boss", store.RoleAdmin) }

	off := newHarness(t)
	for _, tool := range mcp.ActionTools() {
		if mcp.IsAdminTool(tool) && slices.Contains(off.mcpToolNames(admin(off)), tool) {
			t.Errorf("%s must not be offered while security.mcp_admin_tools is off", tool)
		}
	}
	r := off.mcpTool(admin(off), "update_settings", map[string]any{"changes": map[string]any{"retention.jobs": "720h"}})
	if !r.IsError || !strings.Contains(resultText(r), "security.mcp_admin_tools") {
		t.Errorf("asking while the switch is off must name it, got %s", resultText(r))
	}

	on := newHarness(t, func(c *config.Config) { c.Security.MCPAdminTools = true })
	names := on.mcpToolNames(admin(on))
	for _, tool := range mcp.ActionTools() {
		if mcp.IsAdminTool(tool) && !slices.Contains(names, tool) {
			t.Errorf("%s must be offered to an administrator once the switch is on, got %v", tool, names)
		}
	}
	operator := on.mcpToolNames(on.token("actor", store.RoleOperator))
	for _, tool := range mcp.ActionTools() {
		if mcp.IsAdminTool(tool) && slices.Contains(operator, tool) {
			t.Errorf("%s must not be offered to an operator even with the switch on", tool)
		}
	}

	denied := on.mcpTool(admin(on), "update_settings", map[string]any{"changes": map[string]any{"security.disable_auth": true}})
	if !denied.IsError || !strings.Contains(resultText(denied), "cannot be changed over MCP") {
		t.Errorf("a security key must be refused even for an administrator, got %s", resultText(denied))
	}
}
