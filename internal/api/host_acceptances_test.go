package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// acceptanceHost is a host whose agent has reported one counted warning, which
// is the only thing there is to accept.
func (h *harness) acceptanceHost(name string) *store.Host {
	h.t.Helper()
	host := h.host(name)
	r := &hosttune.Report{CheckedAt: h.ctrl.Now(), OS: "linux", Distro: "ubuntu 24.04", Results: []hosttune.Result{
		{ID: "docker.logs", Title: "Docker log rotation", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "no rotation", Recommended: "10m x 3"},
		{ID: "disk.space", Title: "Work directory free space", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "3% free (3.0 GiB)"},
	}}
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		h.t.Fatal(err)
	}
	return host
}

func acceptBody(check string) map[string]any {
	return map[string]any{
		"check_id": check, "seen_current": "no rotation",
		"reason":     "rotation is done by the log shipper on this machine",
		"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
}

// An operator can accept and revoke; a viewer can do neither. The permission is
// the point: silencing a host's alarm is a decision, and it is the operator's.
func TestOnlyAnOperatorCanAcceptOrRevokeAHostCheck(t *testing.T) {
	h := newHarness(t)
	host := h.acceptanceHost("accept-rbac")
	path := "/api/v1/hosts/" + host.ID + "/check-acceptances"
	_, viewer := h.user("viewer-accept", store.RoleViewer)
	_, operator := h.user("operator-accept", store.RoleOperator)

	h.do(request{method: http.MethodPut, path: path, body: acceptBody("docker.logs"), cookie: viewer}).
		mustStatus(t, http.StatusForbidden, "a viewer accepting")
	res := h.do(request{method: http.MethodPut, path: path, body: acceptBody("docker.logs"), cookie: operator})
	res.mustStatus(t, http.StatusOK, "an operator accepting")
	doc := res.json(t)["doctor"].(map[string]any)
	if doc["summary"].(map[string]any)["accepted"].(float64) != 1 {
		t.Errorf("the answer is not the host as it now stands: %v", doc["summary"])
	}
	h.do(request{method: http.MethodDelete, path: path + "/docker.logs", cookie: viewer}).
		mustStatus(t, http.StatusForbidden, "a viewer revoking")
	h.do(request{method: http.MethodDelete, path: path + "/docker.logs", cookie: operator}).
		mustStatus(t, http.StatusOK, "an operator revoking")
}

// A token narrowed to reading hosts, or to cordoning them, must not be able to
// silence a check: the scope is hosts:accept (or hosts:* or *).
func TestATokenNeedsTheAcceptScopeToAcceptAHostCheck(t *testing.T) {
	h := newHarness(t)
	host := h.acceptanceHost("accept-scope")
	path := "/api/v1/hosts/" + host.ID + "/check-acceptances"
	for scope, want := range map[string]int{
		"hosts:read": http.StatusForbidden, "hosts:cordon": http.StatusForbidden,
		"hosts:accept": http.StatusOK, "hosts:*": http.StatusOK,
	} {
		tok := h.token("accept-"+strings.ReplaceAll(scope, ":", "-"), store.RoleOperator, scope)
		h.do(request{method: http.MethodPut, path: path, body: acceptBody("docker.logs"), token: tok}).
			mustStatus(t, want, "scope "+scope)
	}
	if got := auth.ActionHostsAccept.Scope(); got != "hosts:accept" {
		t.Errorf("scope = %q, want hosts:accept", got)
	}
}

func TestAcceptingARefusedCheckAnswers422OnTheCheck(t *testing.T) {
	h := newHarness(t)
	host := h.acceptanceHost("accept-refuse")
	_, op := h.user("operator-refuse", store.RoleOperator)
	path := "/api/v1/hosts/" + host.ID + "/check-acceptances"
	for _, id := range []string{"disk.space", hosttune.KernelPending, "no.such.check"} {
		res := h.do(request{method: http.MethodPut, path: path, body: acceptBody(id), cookie: op})
		res.mustStatus(t, http.StatusUnprocessableEntity, id)
		if f := res.json(t)["error"].(map[string]any)["field"]; f != "check_id" {
			t.Errorf("%s: field = %v, want check_id", id, f)
		}
	}
	short := acceptBody("docker.logs")
	short["reason"] = "too short"
	short["expires_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	res := h.do(request{method: http.MethodPut, path: path, body: short, cookie: op})
	res.mustStatus(t, http.StatusUnprocessableEntity, "bad reason and end")
	if errs, _ := res.json(t)["errors"].([]any); len(errs) != 2 {
		t.Errorf("errors = %v, want both fields at once", errs)
	}
	long := acceptBody("docker.logs")
	long["expires_at"] = time.Now().Add(400 * 24 * time.Hour).UTC().Format(time.RFC3339)
	h.do(request{method: http.MethodPut, path: path, body: long, cookie: op}).
		mustStatus(t, http.StatusUnprocessableEntity, "an end more than a year away")
	stale := acceptBody("docker.logs")
	stale["seen_current"] = "something else"
	h.do(request{method: http.MethodPut, path: path, body: stale, cookie: op}).
		mustStatus(t, http.StatusConflict, "a value that changed while deciding")
	h.do(request{method: http.MethodPut, path: "/api/v1/hosts/missing/check-acceptances", body: acceptBody("docker.logs"), cookie: op}).
		mustStatus(t, http.StatusNotFound, "an unknown host")
}

// Double accept replaces, revoke is idempotent, and the audit log keeps the
// person's reason: only a decision that changed something has a row.
func TestAcceptAndRevokeAreAuditedWithTheReason(t *testing.T) {
	h := newHarness(t)
	host := h.acceptanceHost("accept-audit")
	_, op := h.user("operator-audit", store.RoleOperator)
	path := "/api/v1/hosts/" + host.ID + "/check-acceptances"
	for i := 0; i < 2; i++ {
		h.do(request{method: http.MethodPut, path: path, body: acceptBody("docker.logs"), cookie: op}).
			mustStatus(t, http.StatusOK, "accepting")
	}
	got, _ := h.st.GetHost(h.ctx, host.ID)
	if len(got.Acceptances) != 1 {
		t.Fatalf("two accepts left %d rows, want 1", len(got.Acceptances))
	}
	h.do(request{method: http.MethodDelete, path: path + "/docker.logs", cookie: op}).mustStatus(t, http.StatusOK, "revoking")
	h.do(request{method: http.MethodDelete, path: path + "/docker.logs", cookie: op}).mustStatus(t, http.StatusOK, "revoking again")

	accepted, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"host.check_accepted"}}, store.Page{Limit: 10})
	if len(accepted) != 2 || !strings.Contains(accepted[0].After, "rotation is done by the log shipper") || accepted[0].TargetID != host.ID {
		t.Errorf("accepted audit rows = %+v", accepted)
	}
	// The report's value is agent-written text; the log carries the person's
	// reason, not the host's words.
	if strings.Contains(accepted[0].After, `"current"`) {
		t.Errorf("audit row carries the host-reported value: %s", accepted[0].After)
	}
	revoked, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"host.check_revoked"}}, store.Page{Limit: 10})
	if len(revoked) != 1 {
		t.Errorf("revoked audit rows = %d, want 1: the second revoke ended nothing", len(revoked))
	}
}
