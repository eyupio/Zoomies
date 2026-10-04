package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/hosttune"
)

func TestHealthChangesPublishTheSameHostViewAsGET(t *testing.T) {
	h := newHarness(t)
	host := h.host("health")
	sub := h.c.Events().Subscribe(h.ctx, events.SubscribeOptions{Kinds: []events.Kind{events.KindHostUpdated}})
	defer sub.Close()
	r := &hosttune.Report{CheckedAt: h.c.Now(), OS: "linux", Results: []hosttune.Result{{ID: "kernel.pending", Tier: hosttune.Safe, Status: hosttune.Warn}}, RebootPending: true}
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub.C:
		var v HostView
		if err := json.Unmarshal(event.Data, &v); err != nil {
			t.Fatal(err)
		}
		if v.Doctor == nil || !v.Doctor.RebootPending {
			t.Fatal("live view lost report")
		}
	case <-time.After(time.Second):
		t.Fatal("no host health event")
	}
}
func TestDoctorRejectsUnboundedOrDuplicateChecks(t *testing.T) {
	r := &hosttune.Report{CheckedAt: time.Now(), Results: make([]hosttune.Result, 65)}
	if validateDoctor(r, time.Now()) == nil {
		t.Fatal("unbounded report")
	}
	r.Results = []hosttune.Result{{ID: "same", Tier: hosttune.Safe, Status: hosttune.OK}, {ID: "same", Tier: hosttune.Safe, Status: hosttune.OK}}
	if validateDoctor(r, time.Now()) == nil {
		t.Fatal("duplicate IDs")
	}
}

func TestOlderDoctorReportDoesNotUndoANewerHealthChange(t *testing.T) {
	h := newHarness(t)
	host := h.host("health-ordered")
	newer := &hosttune.Report{CheckedAt: h.c.Now(), Results: []hosttune.Result{{ID: "disk.space", Tier: hosttune.Safe, Status: hosttune.OK}}}
	older := &hosttune.Report{CheckedAt: newer.CheckedAt.Add(-time.Minute), Results: []hosttune.Result{{ID: "disk.space", Tier: hosttune.Safe, Status: hosttune.Warn}}}
	for _, r := range []*hosttune.Report{newer, older} {
		if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Doctor.Results[0].Status != hosttune.OK {
		t.Fatal("older report replaced latest health")
	}
}
