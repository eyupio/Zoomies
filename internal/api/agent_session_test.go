package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
)

// The session travels as a header rather than a body field, so that one place
// -- the agent middleware every authenticated agent request already passes
// through -- sees it, instead of three request shapes changing. This is the
// test that the wire actually carries it: the store rule and the problem are
// both tested on their own, and neither would notice a header nobody reads.
func TestAnAgentsSessionHeaderReachesTheHostRow(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("vm-1")

	beat := func(session string) {
		t.Helper()
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/agent/heartbeat", token: token,
			headers: map[string]string{agent.HeaderAgentSession: session},
			body:    agent.HeartbeatRequest{ProtocolVersion: 1, Capacity: 2, Version: "test"}})
		resp.mustStatus(t, http.StatusOK, "heartbeat")
	}

	beat("ses_one")
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.AgentSessionID != "ses_one" {
		t.Fatalf("agent_session_id = %q, want the session the header carried", host.AgentSessionID)
	}

	// Forward once is a restart; back again is the duplicate.
	beat("ses_two")
	beat("ses_one")
	host, err = h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.AgentSessionAlternations != 1 {
		t.Fatalf("alternations = %d, want 1 after the sessions swapped back", host.AgentSessionAlternations)
	}
}

// A huge session value is dropped before it reaches the hosts row, so a
// misbehaving agent that holds a token cannot keep bloating the database by
// alternating two very long strings.
func TestAnOversizedAgentSessionHeaderIsNotRecorded(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("vm-1")

	oversize := "ses_" + strings.Repeat("a", maxAgentSessionBytes)
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/agent/heartbeat", token: token,
		headers: map[string]string{agent.HeaderAgentSession: oversize},
		body:    agent.HeartbeatRequest{ProtocolVersion: 1, Capacity: 2, Version: "test"}})
	resp.mustStatus(t, http.StatusOK, "a heartbeat with an oversized session header")

	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.AgentSessionID != "" {
		t.Fatalf("agent_session_id = %q, want empty for a header past the %d-byte cap", host.AgentSessionID, maxAgentSessionBytes)
	}
}

// An agent from before the header sends nothing, and must keep working: a
// controller upgraded ahead of its agents is the normal state during a rolling
// upgrade.
func TestAnAgentThatSendsNoSessionIsStillServed(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("vm-1")

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/agent/heartbeat", token: token,
		body: agent.HeartbeatRequest{ProtocolVersion: 1, Capacity: 2, Version: "test"}})
	resp.mustStatus(t, http.StatusOK, "a heartbeat with no session header")

	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.AgentSessionID != "" || host.AgentSessionAlternations != 0 {
		t.Fatalf("host = %+v, want no session recorded for an agent that sent none", host)
	}
}
