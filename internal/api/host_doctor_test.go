package api

import (
	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
	"net/http"
	"testing"
)

func TestHostAPIIncludesTheLatestDoctorResult(t *testing.T) {
	h := newHarness(t)
	host := h.host("health-host")
	r := &hosttune.Report{CheckedAt: h.ctrl.Now(), OS: "linux", Distro: "ubuntu 24.04", Results: []hosttune.Result{{ID: "disk.space", Title: "Disk space", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "2%", Recommended: "10%", Rationale: "Leave room for builds."}}}
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	u, _ := h.user("reader", store.RoleViewer)
	res := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: h.session(u)})
	res.mustStatus(t, http.StatusOK, "doctor report")
	var v hostResponse
	res.into(t, &v)
	if v.Doctor == nil || len(v.Doctor.Results) != 1 {
		t.Fatalf("missing report: %+v", v.Doctor)
	}
}
