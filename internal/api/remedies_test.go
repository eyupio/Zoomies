package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// remedyFleet is a machine set to three slots whose standard runner is most of
// it, so it holds one, with jobs that waited for a runner: the state in which the
// controller proposes a smaller runner and prices it.
func remedyFleet(t *testing.T) (*harness, *store.Host) {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.SizeFromProfile = true
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	host := h.host("dev-box")
	host.CPUs, host.MemoryMB, host.Capacity = 8, 32768, 3
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	profile := store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 6, MemoryMB: 6656}}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{Capacity: &host.Capacity, RunnerProfile: &profile}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		queued := time.Now().Add(-time.Hour)
		started, done := queued.Add(2*time.Minute), queued.Add(5*time.Minute)
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: int64(2000 + i), GitHubRunID: 1, Repo: "acme/widgets", Workflow: "ci", JobName: "build",
			Labels: store.StringSlice{"self-hosted", "linux", "x64"}, State: store.JobCompleted, Conclusion: "success",
			InstallationID: pool.InstallationID, PoolID: pool.ID, Matched: true,
			QueuedAt: queued, StartedAt: &started, CompletedAt: &done,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil || got.Slots() >= got.Capacity {
		t.Fatalf("the fixture must hold fewer runners than its capacity: %v %+v", err, got)
	}
	return h, got
}

type problemsList struct {
	Items []struct {
		Code     string `json:"code"`
		TargetID string `json:"target_id"`
		Remedy   *struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Kind  string `json:"kind"`
		} `json:"remedy"`
	} `json:"items"`
}

func (h *harness) remedyFor(token, code, target string) (id, label string) {
	h.t.Helper()
	r := h.do(request{method: http.MethodGet, path: "/api/v1/problems", token: token})
	if r.status != http.StatusOK {
		h.t.Fatalf("GET /problems = %d: %s", r.status, r.body)
	}
	var list problemsList
	if err := json.Unmarshal(r.body, &list); err != nil {
		h.t.Fatal(err)
	}
	for _, p := range list.Items {
		if p.Code == code && p.TargetID == target && p.Remedy != nil {
			return p.Remedy.ID, p.Remedy.Label
		}
	}
	return "", ""
}

// A problem's remedy is applied by naming the problem, and it runs the host's own
// update as the caller: the role is that route's, the audit row is written, and
// the suggestion then no longer applies.
func TestApplyingARemedyMakesTheProposedChangeAsTheCaller(t *testing.T) {
	h, host := remedyFleet(t)
	operator := h.token("actor", store.RoleOperator)
	viewer := h.token("reader", store.RoleViewer)

	id, label := h.remedyFor(viewer, "host.slots_below_capacity", host.ID)
	if id == "" || !strings.HasPrefix(label, "Give each runner") {
		t.Fatalf("the problem must carry a remedy a viewer can read, got %q %q", id, label)
	}
	apply := map[string]any{"code": "host.slots_below_capacity", "target_id": host.ID, "remedy_id": id}

	// Reading is a viewer's; changing the host is not.
	r := h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: viewer, body: apply})
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "operator") {
		t.Errorf("a viewer applying a remedy = %d %s; want 403 naming the role", r.status, r.body)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Fatal("a refused apply changed the host")
	}

	r = h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: operator, body: apply})
	if r.status != http.StatusOK {
		t.Fatalf("applying = %d: %s", r.status, r.body)
	}
	var out struct {
		Applied bool            `json:"applied"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || !out.Applied || len(out.Result) == 0 {
		t.Errorf("the answer must say it applied and carry what the host's own update answered: %s", r.body)
	}
	after, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RunnerProfile.Standard.CPUs >= 6 || after.Slots() != after.Capacity || after.Capacity != host.Capacity {
		t.Errorf("the host must hold its capacity now and keep it: standard %v, slots %d, capacity %d", after.RunnerProfile.Standard.CPUs, after.Slots(), after.Capacity)
	}
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"problem.remedy_applied"}}, store.Page{Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the apply must be on the audit trail: %v %v", rows, err)
	}

	// The change was made, so the controller proposes nothing, and a second apply
	// is refused as out of date rather than made again.
	r = h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: operator, body: apply})
	if r.status != http.StatusConflict || !strings.Contains(string(r.body), "no longer applies") {
		t.Errorf("a second apply = %d %s; want 409", r.status, r.body)
	}
}

// What is applied is what the controller proposes now, never what a client sends:
// a remedy ID that is not the current one, or a problem with none, changes nothing.
func TestARemedyIsNeverAppliedThatTheControllerIsNotProposing(t *testing.T) {
	h, host := remedyFleet(t)
	operator := h.token("actor", store.RoleOperator)

	r := h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: operator,
		body: map[string]any{"code": "host.slots_below_capacity", "target_id": host.ID, "remedy_id": "rem_000000000000"}})
	if r.status != http.StatusConflict || !strings.Contains(string(r.body), "out of date") {
		t.Errorf("a remedy ID that is not the current one = %d %s; want 409 out of date", r.status, r.body)
	}
	r = h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: operator,
		body: map[string]any{"code": "host.unhealthy", "target_id": host.ID}})
	if r.status != http.StatusConflict {
		t.Errorf("a problem the controller proposes nothing for = %d %s; want 409", r.status, r.body)
	}
	r = h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: operator,
		body: map[string]any{"code": "host.slots_below_capacity", "target_id": host.ID, "change": map[string]any{"capacity": 0}}})
	if r.status != http.StatusBadRequest && r.status != http.StatusUnprocessableEntity {
		t.Errorf("a request that sends its own change = %d %s; want it refused", r.status, r.body)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 || got.Capacity != host.Capacity {
		t.Errorf("a refused apply changed the host: %+v", got)
	}
}

// An agent over MCP applies the same remedy through the same endpoint: it reads the
// proposal in list_problems and names it, and it is refused as a viewer is.
func TestAnAgentAppliesARemedyOverMCPAsItsTokenAllows(t *testing.T) {
	h, host := remedyFleet(t)
	operator := h.token("actor", store.RoleOperator)
	viewer := h.token("reader", store.RoleViewer)

	if names := h.mcpToolNames(viewer); containsTool(names, "apply_remedy") {
		t.Errorf("a viewer token was offered apply_remedy: %v", names)
	}
	if r := h.mcpTool(viewer, "apply_remedy", map[string]any{"code": "host.slots_below_capacity", "target_id": host.ID}); !r.IsError || !strings.Contains(resultText(r), "operator") {
		t.Errorf("a viewer asking must be told which role is missing, got %s", resultText(r))
	}

	// A token that may apply suggestions but not change a pool or a host would be
	// offered the tool and refused when it called it. The list is honest: it is not
	// offered.
	narrow := h.token("narrow", store.RoleOperator, "problems:apply", "stats:read")
	if names := h.mcpToolNames(narrow); containsTool(names, "apply_remedy") {
		t.Errorf("a token without pools:write or hosts:write was offered apply_remedy: %v", names)
	}

	id, _ := h.remedyFor(operator, "host.slots_below_capacity", host.ID)
	r := h.mcpTool(operator, "apply_remedy", map[string]any{"code": "host.slots_below_capacity", "target_id": host.ID, "remedy_id": id})
	if r.IsError {
		t.Fatalf("apply_remedy failed: %s", resultText(r))
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.Slots() != got.Capacity {
		t.Errorf("the host must hold its capacity now: slots %d, capacity %d", got.Slots(), got.Capacity)
	}
}

func containsTool(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// Applying a suggestion needs the update's own scope as well as the apply route's,
// so a token limited to applying suggestions cannot change a host or a pool by
// naming a problem about it. Nothing else pins this: it rests on a second check in
// the handler, and a refactor that dropped it would pass every other test.
func TestAScopeThatMayApplySuggestionsStillNeedsTheScopeOfTheChange(t *testing.T) {
	h, host := remedyFleet(t)
	narrow := h.token("narrow", store.RoleOperator, "problems:apply", "stats:read")
	id, _ := h.remedyFor(h.token("reader", store.RoleViewer), "host.slots_below_capacity", host.ID)
	apply := map[string]any{"code": "host.slots_below_capacity", "target_id": host.ID, "remedy_id": id}

	r := h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: narrow, body: apply})
	if r.status != http.StatusForbidden || !strings.Contains(string(r.body), "hosts:write") {
		t.Errorf("a token without hosts:write = %d %s; want 403 naming the scope", r.status, r.body)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Fatal("a refused apply changed the host")
	}

	wide := h.token("wide", store.RoleOperator, "problems:apply", "hosts:write")
	if r := h.do(request{method: http.MethodPost, path: "/api/v1/problems/apply", token: wide, body: apply}); r.status != http.StatusOK {
		t.Errorf("a token with both scopes = %d %s; want 200", r.status, r.body)
	}
}

// The apply route never sends confirm, so a refusal that tells its caller to send it
// again with confirm=true sends them to a flag they cannot set.
func TestAStrandingRefusalToASuggestionDoesNotTellItToSendConfirm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/problems/apply", nil)
	if got := strandingEnding(req); got != strandingConfirm {
		t.Errorf("an ordinary edit ends %q", got)
	}
	req = req.WithContext(forRemedy(req.Context()))
	ending := strandingEnding(req)
	if strings.Contains(ending, "confirm") || !strings.Contains(ending, "yourself") {
		t.Errorf("a suggestion's refusal ends %q; it must say to make the change by hand", ending)
	}
	msg := poolStrandingRefusal(controller.Stranding{Pool: "builders", Host: "big", Reason: "needs 10 GB"}, ending)
	if strings.Contains(msg, "confirm=true") {
		t.Errorf("the refusal still names confirm=true: %s", msg)
	}
}
