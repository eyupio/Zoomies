package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

func ofKind(kind events.Kind) func(sseFrame) bool {
	return func(f sseFrame) bool { return f.event == string(kind) }
}

func decodeFrame(t *testing.T, f sseFrame, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(f.data), into); err != nil {
		t.Fatalf("%s payload is not JSON: %v (%q)", f.event, err, f.data)
	}
}

// The operator who changes a pool is looking at the response; every other
// open dashboard learns about it from the stream, in the same shape GET
// /pools returns, so the row it already has can simply be replaced.
func TestPoolChangesReachEveryOpenDashboard(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	created := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	created.mustStatus(t, http.StatusCreated, "create")
	var pool poolResponse
	created.into(t, &pool)

	var announced poolResponse
	decodeFrame(t, await(t, frames, "pool.created", ofKind(events.KindPoolCreated)), &announced)
	if announced.ID != pool.ID || announced.Name != "zoomies-linux-x64" {
		t.Fatalf("pool.created = %+v, want the pool just created", announced)
	}
	if announced.InstallationTarget != inst.Target {
		t.Errorf("pool.created installation_target = %q, want %q: the frame must carry the GET shape", announced.InstallationTarget, inst.Target)
	}

	h.do(request{method: http.MethodPost, path: "/api/v1/pools/" + pool.ID + "/disable", cookie: cookie}).
		mustStatus(t, http.StatusOK, "disable")
	decodeFrame(t, await(t, frames, "pool.updated", ofKind(events.KindPoolUpdated)), &announced)
	if announced.ID != pool.ID || announced.Enabled {
		t.Errorf("pool.updated after disable = %+v, want enabled=false", announced)
	}

	h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + pool.ID, cookie: cookie}).
		mustStatus(t, http.StatusOK, "delete")
	var gone struct {
		ID string `json:"id"`
	}
	decodeFrame(t, await(t, frames, "pool.deleted", ofKind(events.KindPoolDeleted)), &gone)
	if gone.ID != pool.ID {
		t.Errorf("pool.deleted id = %q, want %q", gone.ID, pool.ID)
	}
}

// Cordoning and deleting a host are operator actions the Hosts page reads from
// its cache, so both have to be announced -- and the announcement has to carry
// `healthy`, which the store row does not.
func TestHostChangesReachEveryOpenDashboard(t *testing.T) {
	h := newHarness(t)
	host := h.host("vm-1")
	// Deleting a host needs the admin role; cordoning only needs operator.
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	h.do(request{method: http.MethodPost, path: "/api/v1/hosts/" + host.ID + "/cordon", cookie: cookie,
		body: map[string]any{"cordoned": true}}).mustStatus(t, http.StatusOK, "cordon")
	var announced hostResponse
	decodeFrame(t, await(t, frames, "host.updated", ofKind(events.KindHostUpdated)), &announced)
	if announced.ID != host.ID || !announced.Cordoned {
		t.Fatalf("host.updated after cordon = %+v, want cordoned=true", announced)
	}
	if !announced.Healthy {
		t.Errorf("host.updated says a host that just heartbeat is unhealthy: %+v", announced)
	}

	h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID, cookie: cookie}).
		mustStatus(t, http.StatusNoContent, "delete")
	var gone struct {
		ID string `json:"id"`
	}
	decodeFrame(t, await(t, frames, "host.deleted", ofKind(events.KindHostDeleted)), &gone)
	if gone.ID != host.ID {
		t.Errorf("host.deleted id = %q, want %q", gone.ID, host.ID)
	}
}

// Removing an installation takes its pools with it. The pools are announced
// first, each on its own, and then the installation: a page that drops the
// pools has nothing left to explain when the installation goes.
func TestRemovingAnInstallationAnnouncesItsPoolsFirst(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	h.do(request{method: http.MethodDelete, path: "/api/v1/installations/" + inst.ID, cookie: cookie}).
		mustStatus(t, http.StatusOK, "delete")

	var gone struct {
		ID string `json:"id"`
	}
	first := await(t, frames, "the first deletion", func(f sseFrame) bool {
		return f.event == string(events.KindPoolDeleted) || f.event == string(events.KindInstallationDeleted)
	})
	if first.event != string(events.KindPoolDeleted) {
		t.Fatalf("the first frame was %s, want the pool to go before the installation", first.event)
	}
	decodeFrame(t, first, &gone)
	if gone.ID != pool.ID {
		t.Errorf("pool.deleted id = %q, want %q", gone.ID, pool.ID)
	}
	decodeFrame(t, await(t, frames, "installation.deleted", ofKind(events.KindInstallationDeleted)), &gone)
	if gone.ID != inst.ID {
		t.Errorf("installation.deleted id = %q, want %q", gone.ID, inst.ID)
	}
}
