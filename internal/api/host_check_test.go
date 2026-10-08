package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// checkableHost is a connected host whose agent has said it can answer a check,
// which is the only kind the route will queue a task for.
func (h *harness) checkableHost(name string) *store.Host {
	h.t.Helper()
	host := h.host(name)
	req := agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Features: []string{agent.FeatureHostCheck}}
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, req); err != nil {
		h.t.Fatalf("Heartbeat: %v", err)
	}
	return host
}

func checkPath(host *store.Host) string { return "/api/v1/hosts/" + host.ID + "/health-check" }

// A check spends the host's CPU and forks a few dozen processes there, so it has
// a scope of its own: a token narrowed to reading, cordoning or accepting on
// hosts must not be able to start one.
func TestATokenNeedsTheCheckScopeToAskAHostToCheck(t *testing.T) {
	h := newHarness(t)
	for scope, want := range map[string]int{
		"hosts:read": http.StatusForbidden, "hosts:cordon": http.StatusForbidden,
		"hosts:accept": http.StatusForbidden, "hosts:write": http.StatusForbidden,
		"hosts:check": http.StatusAccepted, "hosts:*": http.StatusAccepted, "*": http.StatusAccepted,
	} {
		name := "scope-" + strings.NewReplacer(":", "-", "*", "all").Replace(scope)
		host := h.checkableHost(name)
		tok := h.token(name, store.RoleOperator, scope)
		h.do(request{method: http.MethodPost, path: checkPath(host), token: tok}).
			mustStatus(t, want, "scope "+scope)
	}
	if got := auth.ActionHostsCheck.Scope(); got != "hosts:check" {
		t.Errorf("scope = %q, want hosts:check", got)
	}
}

// Viewers never: the role is the second half of the same rule as the scope.
func TestOnlyAnOperatorCanAskAHostToCheck(t *testing.T) {
	h := newHarness(t)
	host := h.checkableHost("check-roles")
	_, viewer := h.user("check-viewer", store.RoleViewer)
	_, operator := h.user("check-operator", store.RoleOperator)
	h.do(request{method: http.MethodPost, path: checkPath(host), cookie: viewer}).
		mustStatus(t, http.StatusForbidden, "a viewer")
	h.do(request{method: http.MethodPost, path: checkPath(host), cookie: operator}).
		mustStatus(t, http.StatusAccepted, "an operator")
	h.do(request{method: http.MethodPost, path: "/api/v1/hosts/missing/health-check", cookie: operator}).
		mustStatus(t, http.StatusNotFound, "an unknown host")
}

// A press that queued a task is audited once; a repeat press while the agent has
// not answered changes nothing and so writes nothing, and the answer carries the
// state the page reads.
func TestAskingAHostToCheckIsAuditedOnceAndAnswersTheHost(t *testing.T) {
	h := newHarness(t)
	host := h.checkableHost("check-audit")
	_, op := h.user("check-audit-op", store.RoleOperator)
	for i := 0; i < 2; i++ {
		res := h.do(request{method: http.MethodPost, path: checkPath(host), cookie: op})
		res.mustStatus(t, http.StatusAccepted, "asking")
		var v struct {
			ID          string `json:"id"`
			HealthCheck *struct {
				State string `json:"state"`
			} `json:"health_check"`
		}
		res.into(t, &v)
		if v.ID != host.ID || v.HealthCheck == nil || v.HealthCheck.State != "asked" {
			t.Fatalf("press %d answered %s, want the host with health_check.state asked", i+1, truncate(res.body))
		}
	}
	rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"host.check_requested"}}, store.Page{Limit: 10})
	if len(rows) != 1 || rows[0].TargetID != host.ID {
		t.Errorf("audit rows = %+v, want exactly one for the host", rows)
	}
}

// Every refusal says what to do about it, and none of them queues or audits.
func TestAskingAHostThatCannotCheckAnswers409InWords(t *testing.T) {
	h := newHarness(t)
	_, op := h.user("check-refuse-op", store.RoleOperator)

	old := h.host("check-old-agent") // connected, but advertises no host-check
	away := &store.Host{
		Name: "check-away", Capacity: 4, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		Features: store.StringSlice{agent.FeatureHostCheck}, OS: "linux", Arch: "amd64",
		LastHeartbeat: time.Now().Add(-time.Hour),
	}
	if err := h.st.CreateHost(h.ctx, away); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		host *store.Host
		want string
	}{
		{"an agent that cannot check on request", old, "cannot check on request"},
		{"a host that is not connected", away, "not connected"},
	} {
		res := h.do(request{method: http.MethodPost, path: checkPath(tc.host), cookie: op})
		res.mustStatus(t, http.StatusConflict, tc.name)
		msg, _ := res.json(t)["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, tc.want) {
			t.Errorf("%s: message = %q, want it to contain %q", tc.name, msg, tc.want)
		}
	}
	rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"host.check_requested"}}, store.Page{Limit: 10})
	if len(rows) != 0 {
		t.Errorf("refusals wrote %d audit rows, want none", len(rows))
	}
}

// The whole round trip over HTTP: the task goes out on the agent's own poll, a
// failed answer ends the request without ending the cooldown, and the asker is
// told how long to wait in a header a client can act on.
func TestAFailedCheckStillHoldsTheCooldownAndSaysHowLong(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("check-cooldown")
	if _, err := h.ctrl.Heartbeat(h.ctx, hostID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Features: []string{agent.FeatureHostCheck}}); err != nil {
		t.Fatal(err)
	}
	_, op := h.user("check-cooldown-op", store.RoleOperator)
	path := "/api/v1/hosts/" + hostID + "/health-check"
	h.do(request{method: http.MethodPost, path: path, cookie: op}).mustStatus(t, http.StatusAccepted, "asking")

	poll := h.do(request{method: http.MethodGet, path: "/api/v1/agent/tasks?wait=2", token: token})
	poll.mustStatus(t, http.StatusOK, "task poll")
	var batch agent.TaskBatch
	poll.into(t, &batch)
	if len(batch.Tasks) != 1 || batch.Tasks[0].Kind != agent.TaskCheckHost {
		t.Fatalf("the agent was handed %+v, want one check_host", batch.Tasks)
	}
	h.do(request{method: http.MethodPost, path: "/api/v1/agent/results", token: token,
		body: agent.TaskResult{TaskID: batch.Tasks[0].ID, Kind: agent.TaskCheckHost, Error: "no luck", CompletedAt: time.Now()}}).
		mustStatus(t, http.StatusNoContent, "a failed answer")

	res := h.do(request{method: http.MethodPost, path: path, cookie: op})
	res.mustStatus(t, http.StatusTooManyRequests, "asking again at once")
	if secs, err := strconv.Atoi(res.header.Get("Retry-After")); err != nil || secs < 1 || secs > 15 {
		t.Errorf("Retry-After = %q, want whole seconds within the 15 s cooldown", res.header.Get("Retry-After"))
	}
	if code := res.json(t)["error"].(map[string]any)["code"]; code != "rate_limited" {
		t.Errorf("code = %v, want rate_limited", code)
	}
}

// A report fills a task result, and the results route caps a body at 1 MiB. The
// report a real host sends fits with room to spare, and so does the largest one
// the controller would accept at all, so a busy host's answer is never cut off
// into a "malformed" refusal that says nothing about the real cause.
func TestAHostCheckResultFitsTheBodyCapAtItsLargestAndIsIngested(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("check-result")
	if _, err := h.ctrl.Heartbeat(h.ctx, hostID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Features: []string{agent.FeatureHostCheck}}); err != nil {
		t.Fatal(err)
	}
	_, op := h.user("check-result-op", store.RoleOperator)
	h.do(request{method: http.MethodPost, path: "/api/v1/hosts/" + hostID + "/health-check", cookie: op}).
		mustStatus(t, http.StatusAccepted, "asking")
	poll := h.do(request{method: http.MethodGet, path: "/api/v1/agent/tasks?wait=2", token: token})
	var batch agent.TaskBatch
	poll.into(t, &batch)
	if len(batch.Tasks) != 1 {
		t.Fatalf("tasks = %+v", batch.Tasks)
	}

	sized := func(n int, field int) *hosttune.Report {
		r := &hosttune.Report{CheckedAt: h.ctrl.Now(), OS: "linux", Distro: "ubuntu 24.04"}
		for i := 0; i < n; i++ {
			r.Results = append(r.Results, hosttune.Result{
				ID: "check." + strconv.Itoa(i), Title: strings.Repeat("t", min(field, 200)), Tier: hosttune.Safe, Status: hosttune.Warn,
				Current: strings.Repeat("c", field), Recommended: strings.Repeat("r", field),
				Rationale: strings.Repeat("a", min(field, 1024)), Reason: strings.Repeat("e", field),
			})
		}
		return r
	}
	largest, _ := json.Marshal(agent.TaskResult{TaskID: "x", Kind: agent.TaskCheckHost, OK: true, Doctor: sized(64, 4096)})
	if len(largest) >= maxBodyBytes {
		t.Fatalf("the largest accepted report is %d bytes, over the %d byte body cap", len(largest), maxBodyBytes)
	}

	realistic := sized(32, 300)
	h.do(request{method: http.MethodPost, path: "/api/v1/agent/results", token: token,
		body: agent.TaskResult{TaskID: batch.Tasks[0].ID, Kind: agent.TaskCheckHost, OK: true, Doctor: realistic, CompletedAt: time.Now()}}).
		mustStatus(t, http.StatusNoContent, "a real-sized answer")
	got, err := h.st.GetHost(h.ctx, hostID)
	if err != nil || got.Doctor.Report == nil || len(got.Doctor.Results) != 32 {
		t.Fatalf("the report was not ingested: %+v, %v", got, err)
	}
}
