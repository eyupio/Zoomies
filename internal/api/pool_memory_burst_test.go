package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// createPool makes a pool through the API and returns what the API answered, so
// a test reads the policy the way a client does and not from the store.
func createPool(t *testing.T, h *harness, cookie string, body map[string]any) controller.PoolView {
	t.Helper()
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusCreated, "create")
	var created controller.PoolView
	res.into(t, &created)
	return created
}

// A new container pool watches memory whatever its size. Elastic CPU waits for
// a pool whose size is left to its host, because the host's share is its
// guaranteed base; the memory valve needs only a limit to watch, so a pool sized
// by hand gets the evidence too -- and it changes nothing while it is only
// watching, so there is no reason to make an operator ask.
func TestANewContainerPoolWatchesMemoryWhateverItsSize(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	for _, tc := range []struct {
		name      string
		resources map[string]any
	}{
		{"sized-by-its-host", nil},
		{"sized-by-hand", map[string]any{"cpus": 4, "memory_mb": 8192}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := poolBody(inst.ID)
			body["name"] = tc.name
			if tc.resources != nil {
				body["resources"] = tc.resources
			}
			created := createPool(t, h, cookie, body)
			if created.MemoryBurst.Mode != store.MemoryBurstObserve {
				t.Fatalf("memory valve = %+v, want it watching and changing nothing", created.MemoryBurst)
			}
			stored, err := h.st.GetPool(h.ctx, created.ID)
			if err != nil {
				t.Fatalf("GetPool: %v", err)
			}
			if stored.MemoryBurst.Mode != store.MemoryBurstObserve {
				t.Errorf("the stored policy = %+v, want what the API answered", stored.MemoryBurst)
			}
		})
	}

	// Saying nothing about memory is not the same as saying off: a pool made with
	// the valve off stays off, which is how an operator who would rather have the
	// kill keeps it.
	body := poolBody(inst.ID)
	body["name"] = "off-on-purpose"
	body["memory_burst"] = map[string]any{"mode": "off"}
	if created := createPool(t, h, cookie, body); created.MemoryBurst.Mode != store.MemoryBurstOff {
		t.Errorf("a pool made with the valve off came out as %+v", created.MemoryBurst)
	}
}

// The valve is a container runtime's feature: it is the runtime that can raise a
// live container's limit. A process pool therefore starts without it and is
// refused it by name, rather than being handed a setting that does nothing.
func TestAProcessPoolHasNoMemoryValve(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	body := poolBody(inst.ID)
	body["backend"] = "process"
	if created := createPool(t, h, cookie, body); created.MemoryBurst.Mode != store.MemoryBurstOff {
		t.Fatalf("process pool memory valve = %+v, want off", created.MemoryBurst)
	}

	body["name"] = "asks-anyway"
	body["memory_burst"] = map[string]any{"mode": "observe"}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusUnprocessableEntity, "create")
	if !strings.Contains(string(res.body), `"field":"memory_burst.mode"`) || !strings.Contains(string(res.body), "Docker or Podman") {
		t.Fatalf("the refusal does not name memory_burst.mode and the backends that have it: %s", res.body)
	}
}

// A pool that asks to be lent memory keeps the ceiling and the swap it asked
// for, exactly, and reads them back from the pool page.
func TestAPoolCanAskToBeLentMemoryWithACeilingAndSwap(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	body := poolBody(inst.ID)
	body["resources"] = map[string]any{"cpus": 2, "memory_mb": 4096}
	body["memory_burst"] = map[string]any{"mode": "automatic", "max_memory_mb": 6144, "spill_mb": 2048}
	created := createPool(t, h, cookie, body)
	want := store.MemoryBurstPolicy{Mode: store.MemoryBurstAutomatic, MaxMemoryMB: 6144, SpillMB: 2048}
	if created.MemoryBurst != want {
		t.Fatalf("created policy = %+v, want %+v", created.MemoryBurst, want)
	}

	got := h.do(request{method: http.MethodGet, path: "/api/v1/pools/" + created.ID, cookie: cookie})
	got.mustStatus(t, http.StatusOK, "get")
	var read controller.PoolView
	got.into(t, &read)
	if read.MemoryBurst != want {
		t.Errorf("the pool page reads %+v, want %+v", read.MemoryBurst, want)
	}
	assertShape(t, loadSpec(t), "Pool", got.body)
}

// Every way a policy can be wrong is refused with the field it is wrong in and a
// sentence an operator can act on, and nothing is written.
func TestAMemoryPolicyThatCannotWorkIsRefusedByField(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	for _, tc := range []struct {
		name   string
		policy map[string]any
		field  string
		reason string
	}{
		{"a mode that is not one", map[string]any{"mode": "sometimes"}, "memory_burst.mode", "off, observe or automatic"},
		{"a negative ceiling", map[string]any{"mode": "automatic", "max_memory_mb": -1}, "memory_burst.max_memory_mb", "cannot be negative"},
		{"a ceiling too small to hold the runner", map[string]any{"mode": "automatic", "max_memory_mb": 256}, "memory_burst.max_memory_mb", "512 MB"},
		{"a ceiling with nothing to lend", map[string]any{"mode": "automatic", "max_memory_mb": 4096}, "memory_burst.max_memory_mb", "nothing to lend"},
		{"a negative swap allowance", map[string]any{"mode": "observe", "spill_mb": -1}, "memory_burst.spill_mb", "cannot be negative"},
		{"swap while the valve is off", map[string]any{"mode": "off", "spill_mb": 1024}, "memory_burst.spill_mb", "means nothing while the valve is off"},
		{"swap past a terabyte", map[string]any{"mode": "automatic", "spill_mb": 2 << 20}, "memory_burst.spill_mb", "a typo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := poolBody(inst.ID)
			body["name"] = "refused"
			body["resources"] = map[string]any{"cpus": 2, "memory_mb": 4096}
			body["memory_burst"] = tc.policy
			res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
			res.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			if text := string(res.body); !strings.Contains(text, `"field":"`+tc.field+`"`) || !strings.Contains(text, tc.reason) {
				t.Fatalf("the refusal does not name %s and say %q: %s", tc.field, tc.reason, text)
			}
		})
	}
	if pools, err := h.st.ListPools(h.ctx); err != nil || len(pools) != 0 {
		t.Fatalf("a refused policy left a pool behind: %v %v", pools, err)
	}

	// The dry run says the same, because that is where the editor finds out.
	body := poolBody(inst.ID)
	body["memory_burst"] = map[string]any{"mode": "automatic", "spill_mb": -5}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	res.into(t, &verdict)
	if verdict.Valid || len(verdict.Errors) != 1 || verdict.Errors[0].Field != "memory_burst.spill_mb" {
		t.Errorf("the dry run's verdict = %+v, want it invalid in memory_burst.spill_mb", verdict)
	}
}

// An edit that does not name the policy leaves it alone, and one that does
// replaces it whole -- the way cpu_burst is edited -- with the mode read as a
// person types it.
func TestEditingAPoolLeavesItsMemoryPolicyAloneUnlessItIsNamed(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	body := poolBody(inst.ID)
	body["memory_burst"] = map[string]any{"mode": "automatic", "max_memory_mb": 6144, "spill_mb": 512}
	body["resources"] = map[string]any{"cpus": 2, "memory_mb": 4096}
	created := createPool(t, h, cookie, body)
	path := "/api/v1/pools/" + created.ID

	patch := func(body map[string]any) controller.PoolView {
		t.Helper()
		res := h.do(request{method: http.MethodPatch, path: path, cookie: cookie, body: body})
		res.mustStatus(t, http.StatusOK, "patch")
		var out controller.PoolView
		res.into(t, &out)
		return out
	}
	if got := patch(map[string]any{"max_runners": 8}); got.MemoryBurst != created.MemoryBurst {
		t.Fatalf("an edit that did not name the policy changed it: %+v, was %+v", got.MemoryBurst, created.MemoryBurst)
	}
	got := patch(map[string]any{"memory_burst": map[string]any{"mode": "  Observe "}})
	if got.MemoryBurst != (store.MemoryBurstPolicy{Mode: store.MemoryBurstObserve}) {
		t.Fatalf("a policy that was named is %+v, want it replaced whole, with the mode normalised", got.MemoryBurst)
	}
	if stored, _ := h.st.GetPool(h.ctx, created.ID); stored.MemoryBurst != got.MemoryBurst {
		t.Errorf("the stored policy = %+v, want what the edit answered", stored.MemoryBurst)
	}

	// A bad edit is refused and the policy is as it was.
	res := h.do(request{method: http.MethodPatch, path: path, cookie: cookie,
		body: map[string]any{"memory_burst": map[string]any{"mode": "observe", "max_memory_mb": 100}}})
	res.mustStatus(t, http.StatusUnprocessableEntity, "a ceiling too small to hold the runner")
	if stored, _ := h.st.GetPool(h.ctx, created.ID); stored.MemoryBurst != got.MemoryBurst {
		t.Errorf("a refused edit was written: %+v", stored.MemoryBurst)
	}
}

// The pools most fleets run are the ones the controller keeps, and they refuse
// every field that is worked out from the hosts. The memory valve is a policy and
// not a figure, so it is the operator's here as well: otherwise the pools that
// need it most would be the ones that could never turn it on.
func TestAnOperatorCanChangeTheMemoryValveOnAPoolTheControllerKeeps(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	path := "/api/v1/pools/" + pool.ID

	if pool.MemoryBurst.Mode != store.MemoryBurstObserve {
		t.Fatalf("the pool the controller made watches memory as %+v, want it observing like one made by hand", pool.MemoryBurst)
	}

	patch := func(policy map[string]any) *response {
		return h.do(request{method: http.MethodPatch, path: path, cookie: cookie, body: map[string]any{"memory_burst": policy}})
	}
	res := patch(map[string]any{"mode": "automatic", "max_memory_mb": 8192, "spill_mb": 1024})
	res.mustStatus(t, http.StatusOK, "turning the valve on")
	var view controller.PoolView
	res.into(t, &view)
	want := store.MemoryBurstPolicy{Mode: store.MemoryBurstAutomatic, MaxMemoryMB: 8192, SpillMB: 1024}
	if view.MemoryBurst != want {
		t.Fatalf("the pool after the edit = %+v, want %+v", view.MemoryBurst, want)
	}
	// The pass the edit ran is the one that would put back anything it owns; the
	// policy is not among that, and survives it.
	if err := h.ctrl.ReconcileAutoPools(h.ctx); err != nil {
		t.Fatalf("ReconcileAutoPools: %v", err)
	}
	if stored, _ := h.st.GetPool(h.ctx, pool.ID); stored.MemoryBurst != want {
		t.Fatalf("the controller put the policy back: %+v", stored.MemoryBurst)
	}

	// A policy that cannot work is refused here as it is anywhere, and the pool is
	// not touched.
	patch(map[string]any{"mode": "automatic", "spill_mb": -5}).mustStatus(t, http.StatusUnprocessableEntity, "a negative swap allowance")
	if stored, _ := h.st.GetPool(h.ctx, pool.ID); stored.MemoryBurst != want {
		t.Fatalf("a refused edit was written: %+v", stored.MemoryBurst)
	}
}

// What a pool lends is part of what the pool is, so it travels with it.
func TestAnExportedPoolCarriesItsMemoryPolicyAndAnOlderDocumentLeavesOneAlone(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	zonedHost(h)
	pool := richPool(h, inst)
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	doc, _ := exportPoolsDoc(t, h, cookie)
	if len(doc.Pools) != 1 || doc.Pools[0].MemoryBurst != pool.MemoryBurst {
		t.Fatalf("the export carries %+v, want the pool's %+v", doc.Pools, pool.MemoryBurst)
	}

	// A document written before the policy existed names no memory_burst, and an
	// entry that leaves a setting out keeps the pool's value for it.
	older := map[string]any{"export_version": poolsExportVersion, "pools": []any{map[string]any{
		"name": pool.Name, "installation": "acme", "labels": []string{"builders", "gpu"}, "max_runners": 9,
	}}}
	text, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	importPools(t, h, cookie, map[string]any{"document": string(text)}, http.StatusOK)
	after, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if after.MaxRunners != 9 || after.MemoryBurst != pool.MemoryBurst {
		t.Errorf("an older document left the pool at maximum %d with memory %+v; it should change the maximum and keep the policy %+v",
			after.MaxRunners, after.MemoryBurst, pool.MemoryBurst)
	}
}
