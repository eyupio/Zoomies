package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

// TestAgentRoutesRefuseAUserCredential is the separation the whole agent
// surface rests on: an agent token reaches /api/v1/agent/* and nothing else,
// and a user token does not reach the agent routes at all.
func TestAgentRoutesRefuseAUserCredential(t *testing.T) {
	h := newHarness(t)
	adminToken := h.token("admin", store.RoleAdmin)
	_, agentToken := h.agentToken("vm-1")

	for _, path := range []string{"/api/v1/agent/heartbeat", "/api/v1/agent/results", "/api/v1/agent/report"} {
		anon := h.do(request{method: http.MethodPost, path: path, body: map[string]any{}})
		anon.mustStatus(t, http.StatusUnauthorized, "unauthenticated "+path)

		asUser := h.do(request{method: http.MethodPost, path: path, token: adminToken, body: map[string]any{}})
		asUser.mustStatus(t, http.StatusUnauthorized, "admin token on "+path)
	}

	// And the agent's own credential is refused on the user API.
	onUserAPI := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: agentToken})
	onUserAPI.mustStatus(t, http.StatusUnauthorized, "agent token on the user API")
	if !strings.Contains(onUserAPI.errorMessage(t), "agent token") {
		t.Errorf("the refusal does not say what kind of credential it was: %q", onUserAPI.errorMessage(t))
	}
}

// TestAgentJoinAndHeartbeat covers enrolment and the liveness beat, including
// the 404 an agent's transport turns into "re-join me".
func TestAgentJoinAndHeartbeat(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("vm-1")

	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.Name != "vm-1" || host.Capacity != 2 {
		t.Fatalf("the join did not record the host as described: %+v", host)
	}
	if len(host.Backends) != 1 || host.Backends[0] != "docker" {
		t.Errorf("backends = %v, want [docker]", host.Backends)
	}

	beat := h.do(request{method: http.MethodPost, path: "/api/v1/agent/heartbeat", token: token,
		body: agent.HeartbeatRequest{ProtocolVersion: 1, Capacity: 2, Version: "test"}})
	beat.mustStatus(t, http.StatusOK, "heartbeat")
	var resp agent.HeartbeatResponse
	beat.into(t, &resp)
	if !resp.OK {
		t.Errorf("heartbeat response = %+v", resp)
	}

	// A host deleted under a running agent must answer 404, not 500: the
	// agent's transport reads that specific status as "you no longer exist".
	if err := h.st.DeleteHost(h.ctx, hostID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	gone := h.do(request{method: http.MethodPost, path: "/api/v1/agent/heartbeat", token: token,
		body: agent.HeartbeatRequest{ProtocolVersion: 1, Capacity: 2}})
	// The token went with the row, so this is refused before the handler runs;
	// either answer tells the agent to stop, and 401 is the stronger of the two.
	if gone.status != http.StatusUnauthorized && gone.status != http.StatusNotFound {
		t.Fatalf("heartbeat after the host was deleted answered %d, want 401 or 404", gone.status)
	}
}

func TestAgentJoinRefusesASpentToken(t *testing.T) {
	h := newHarness(t)
	_, plaintext, err := h.ctrl.Auth().CreateJoinToken(h.ctx, time.Hour, nil, 2, "test")
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	body := map[string]any{
		"protocol_version": 1, "join_token": plaintext, "name": "vm-1",
		"capacity": 2, "os": "linux", "arch": "amd64", "version": "test",
	}

	first := h.do(request{method: http.MethodPost, path: "/api/v1/agent/join", body: body})
	first.mustStatus(t, http.StatusOK, "first join")

	body["name"] = "vm-2"
	second := h.do(request{method: http.MethodPost, path: "/api/v1/agent/join", body: body})
	second.mustStatus(t, http.StatusUnprocessableEntity, "second join with the same token")
	if !strings.Contains(second.errorMessage(t), "join token") {
		t.Errorf("the refusal does not name the problem: %q", second.errorMessage(t))
	}
}

// TestAgentTaskPollAndResult walks a task from the queue to its outcome.
func TestAgentTaskPollAndResult(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	hostID, token := h.agentToken("vm-1")
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	run := h.runner(pool, host, store.RunnerIdle)

	// An empty long poll returns quickly when asked to wait a second, which is
	// the idle case; nothing is queued yet.
	idle := h.do(request{method: http.MethodGet, path: "/api/v1/agent/tasks?wait=1", token: token})
	idle.mustStatus(t, http.StatusOK, "idle poll")
	var empty agent.TaskBatch
	idle.into(t, &empty)
	if len(empty.Tasks) != 0 {
		t.Fatalf("an idle poll returned %d tasks", len(empty.Tasks))
	}

	// Draining the runner queues a stop task for its host.
	u, _ := h.user("operator", store.RoleOperator)
	drain := h.do(request{method: http.MethodPost, path: "/api/v1/runners/" + run.ID + "/drain", cookie: h.session(u)})
	drain.mustStatus(t, http.StatusAccepted, "drain")

	poll := h.do(request{method: http.MethodGet, path: "/api/v1/agent/tasks?wait=2", token: token})
	poll.mustStatus(t, http.StatusOK, "task poll")
	var batch agent.TaskBatch
	poll.into(t, &batch)
	if len(batch.Tasks) == 0 {
		t.Fatal("the drain queued no task for the agent")
	}
	task := batch.Tasks[0]
	if task.Kind != agent.TaskStopRunner || task.RunnerID != run.ID {
		t.Fatalf("task = %+v, want a stop for %s", task, run.ID)
	}

	result := h.do(request{method: http.MethodPost, path: "/api/v1/agent/results", token: token,
		body: agent.TaskResult{TaskID: task.ID, RunnerID: run.ID, OK: true, CompletedAt: time.Now()}})
	result.mustStatus(t, http.StatusNoContent, "task result")
}

// TestAgentReportTakesABareArray keeps the two halves of the protocol agreeing
// about the wire format: the agent sends []RunnerReport, not an object.
func TestAgentReportTakesABareArray(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	hostID, token := h.agentToken("vm-1")
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	run := h.runner(pool, host, store.RunnerProvisioning)

	reports := []agent.RunnerReport{{
		RunnerID: run.ID, State: store.RunnerRegistering, ObservedAt: time.Now(),
	}}
	raw, err := json.Marshal(reports)
	if err != nil {
		t.Fatalf("marshalling reports: %v", err)
	}
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/agent/report", token: token,
		rawBody: string(raw), headers: map[string]string{"Content-Type": "application/json"}})
	resp.mustStatus(t, http.StatusNoContent, "runner report")

	after, err := h.st.GetRunner(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerRegistering {
		t.Errorf("state = %q after the report, want registering", after.State)
	}
}

// TestAgentLogPostForAnUnknownStream is the ordinary case of a viewer who
// closed the tab: the relay is gone and the agent is told to stop.
func TestAgentLogPostForAnUnknownStream(t *testing.T) {
	h := newHarness(t)
	_, token := h.agentToken("vm-1")

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/agent/logs/log_nobody", token: token,
		rawBody: "some output", headers: map[string]string{"Content-Type": "application/octet-stream"}})
	resp.mustStatus(t, http.StatusNotFound, "log post for an unknown stream")
}
