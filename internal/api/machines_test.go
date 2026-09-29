package api

import (
	"context"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// machineResourceID is the provider's own identifier for a test's machine,
// derived from its name because the store refuses two machines on one resource
// -- which is the constraint that stops two rows believing they own one VM.
func machineResourceID(name string) string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(name))
	return strconv.Itoa(100 + int(sum.Sum32()%9000))
}

// machineOn is a machine that has reached a state and, when a host is given,
// enrolled onto it -- which is what makes it a machine with work on it.
func (h *harness) machineOn(p *store.Provider, name string, state store.MachineState, host *store.Host) *store.Machine {
	h.t.Helper()
	m := &store.Machine{
		ProviderID:   p.ID,
		Name:         name,
		State:        state,
		ResourceZone: "zone-a",
		ResourceID:   machineResourceID(name),
		Capacity:     p.MachineCapacity,
		Labels:       store.StringMap{},
	}
	if host != nil {
		m.HostID = host.ID
	}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		h.t.Fatalf("CreateMachine: %v", err)
	}
	return m
}

// A machine exists because demand asked for one. One creation path means one
// accounting path, so there is no route that makes one: a hand-made machine
// would be supply the reconciler would then decide to delete.
func TestThereIsNoWayToCreateAMachineByHand(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/machines", cookie: cookie,
		body: map[string]any{"provider_id": "prv_anything"}})
	if resp.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST /machines answered %d, want 405: %s", resp.status, truncate(resp.body))
	}
}

// Deleting a machine destroys the VM its runners are running on, so the first
// call refuses and says what to do; the second, after a drain, is the short one.
func TestDeletingAMachineWithLiveRunnersIsRefusedUnlessForced(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)

	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-busy", store.MachineReady, host)
	h.runner(pool, host, store.RunnerBusy)

	refused := h.do(request{method: http.MethodDelete, path: "/api/v1/machines/" + m.ID, cookie: cookie})
	refused.mustStatus(t, http.StatusConflict, "delete a machine with a busy runner")
	msg := refused.errorMessage(t)
	if !strings.Contains(msg, "1 runner") {
		t.Errorf("the refusal does not say how many runners are on it: %q", msg)
	}
	if !strings.Contains(msg, "force=true") {
		t.Errorf("the refusal does not say how to insist: %q", msg)
	}

	forced := h.do(request{method: http.MethodDelete, path: "/api/v1/machines/" + m.ID + "?force=true", cookie: cookie})
	forced.mustStatus(t, http.StatusOK, "force-delete a machine")
	var out machineResponse
	forced.into(t, &out)
	if out.State != store.MachineDeleting {
		t.Errorf("the machine is %s rather than deleting; a delete is finished when the resource is gone, not when this returns", out.State)
	}
}

// A quarantined machine's resource may belong to somebody else, which is
// exactly what quarantine means. Deleting it -- forced or not -- would destroy
// another fleet's machine, so the refusal points at release instead.
func TestAQuarantinedMachineIsNeverDeletableAndTheRefusalPointsAtRelease(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-doubtful", store.MachineQuarantined, nil)

	for _, path := range []string{
		"/api/v1/machines/" + m.ID,
		"/api/v1/machines/" + m.ID + "?force=true",
	} {
		resp := h.do(request{method: http.MethodDelete, path: path, cookie: cookie})
		resp.mustStatus(t, http.StatusConflict, "delete a quarantined machine")
		msg := resp.errorMessage(t)
		if !strings.Contains(msg, "release") {
			t.Errorf("the refusal does not point at the escape hatch: %q", msg)
		}
		if !strings.Contains(msg, "zone-a/"+machineResourceID(m.Name)) {
			t.Errorf("the refusal does not name the resource to go and look at: %q", msg)
		}
	}
	row, err := h.st.GetMachine(h.ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if row.State != store.MachineQuarantined {
		t.Errorf("the machine moved to %s despite the refusal", row.State)
	}
}

// Release forgets a row and touches nothing, which cannot be undone and cannot
// be checked afterwards -- so the machine's name has to be typed, and the audit
// row keeps the provider's identifiers the database is about to stop holding.
func TestReleasingAMachineNeedsItsNameAndThenForgetsIt(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-doubtful", store.MachineQuarantined, nil)

	wrong := h.do(request{method: http.MethodPost, path: "/api/v1/machines/" + m.ID + "/release", cookie: cookie,
		body: map[string]any{"name": "zoomies-mach-something-else"}})
	wrong.mustStatus(t, http.StatusUnprocessableEntity, "release with the wrong name")
	if msg := wrong.errorMessage(t); !strings.Contains(msg, m.Name) {
		t.Errorf("the refusal does not say what to type: %q", msg)
	}
	if _, err := h.st.GetMachine(h.ctx, m.ID); err != nil {
		t.Fatalf("the machine was forgotten despite the wrong name: %v", err)
	}

	ok := h.do(request{method: http.MethodPost, path: "/api/v1/machines/" + m.ID + "/release", cookie: cookie,
		body: map[string]any{"name": m.Name}})
	ok.mustStatus(t, http.StatusNoContent, "release a machine")
	if _, err := h.st.GetMachine(h.ctx, m.ID); err == nil {
		t.Fatal("the machine is still there after being released")
	}

	audit := h.do(request{method: http.MethodGet, path: "/api/v1/audit?limit=50", cookie: cookie})
	audit.mustStatus(t, http.StatusOK, "audit")
	body := string(audit.body)
	if !strings.Contains(body, "machine.release") {
		t.Error("releasing a machine was not audited")
	}
	if !strings.Contains(body, machineResourceID(m.Name)) {
		t.Error("the audit row does not keep the resource identifier, which is now the only record of it")
	}
}

// A draining machine keeps the work it has and accepts no more. Without the
// cordon the scheduler would go on placing runners onto a host whose VM is
// about to be destroyed, and it would never empty.
func TestDrainingAMachineCordonsItsHost(t *testing.T) {
	h := newHarness(t)
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-leaving", store.MachineReady, host)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/machines/" + m.ID + "/drain", cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "drain a machine")
	var out machineResponse
	resp.into(t, &out)
	if out.State != store.MachineDraining {
		t.Errorf("the machine is %s rather than draining", out.State)
	}

	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !got.Cordoned {
		t.Error("the host was not cordoned, so the scheduler would keep placing runners on a machine that is leaving")
	}
}

// Every other road into a machine's deletion cordons its host first, because
// the scheduler's snapshot carries hosts and never machines: an idle machine
// with a queued job matching its pools stays placeable for the whole minutes-long
// delete, and a runner placed in that window dies with the VM.
func TestDeletingAMachineCordonsItsHost(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-idle", store.MachineReady, host)

	resp := h.do(request{method: http.MethodDelete, path: "/api/v1/machines/" + m.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "delete an idle machine")

	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !got.Cordoned {
		t.Error("the host of a machine being deleted was not cordoned, so the scheduler could place a runner on a VM that is about to be destroyed")
	}
}

// A refused delete must have no side effect: cordoning before the refusal would
// take a busy machine out of service by way of a request that said no.
func TestARefusedMachineDeleteLeavesItsHostUncordoned(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-busy", store.MachineReady, host)
	h.runner(pool, host, store.RunnerBusy)

	resp := h.do(request{method: http.MethodDelete, path: "/api/v1/machines/" + m.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusConflict, "delete a machine with a busy runner")

	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.Cordoned {
		t.Error("a refused delete cordoned the host")
	}
}

// A drain the state machine refuses -- only a ready machine can drain -- used to
// cordon the host first and then answer 409, so a request that reported an error
// still took a failed machine's enrolled host out of service, with no audit row.
func TestARefusedMachineDrainLeavesItsHostUncordoned(t *testing.T) {
	h := newHarness(t)
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-broken", store.MachineFailed, host)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/machines/" + m.ID + "/drain", cookie: cookie})
	resp.mustStatus(t, http.StatusConflict, "drain a failed machine")

	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.Cordoned {
		t.Error("a refused drain cordoned the host")
	}
}

// A machine's host is not an ordinary host: the VM outlives the row, and an
// operator who deleted it here would go on paying for a machine nothing is
// tracking. The refusal says which act removes the VM as well.
func TestDeletingAHostThatIsAMachineSaysToDeleteTheMachineInstead(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	h.machineOn(prov, "zoomies-mach-serving", store.MachineReady, host)

	refused := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID, cookie: cookie})
	refused.mustStatus(t, http.StatusConflict, "delete a host that is a machine")
	msg := refused.errorMessage(t)
	for _, want := range []string{host.Name, prov.Name, "force=true"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q: %q", want, msg)
		}
	}

	// Forcing still works; what it does to the VM is the next test's business.
	forced := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID + "?force=true", cookie: cookie})
	forced.mustStatus(t, http.StatusNoContent, "force-delete a host that is a machine")
}

// The refusal above promises that ?force=true forgets the host and leaves the VM
// running. The machine row's host_id is ON DELETE SET NULL, and the machine loop
// reads a Ready machine with no host as one to release, so a bare host delete
// drained and destroyed the VM -- and any job still on it -- within two passes.
// Forcing has to forget the machine row as well, and audit it, because after
// that the audit row is the only record of the resource.
func TestForceDeletingAMachinesHostLeavesTheVMRunningAndForgetsTheRow(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-serving", store.MachineReady, host)

	forced := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID + "?force=true", cookie: cookie})
	forced.mustStatus(t, http.StatusNoContent, "force-delete a host that is a machine")

	if row, err := h.st.GetMachine(h.ctx, m.ID); err == nil {
		t.Fatalf("the machine is still recorded as %s, so the machine loop would release the VM the operator was promised would be left running", row.State)
	}

	audit := h.do(request{method: http.MethodGet, path: "/api/v1/audit?limit=50", cookie: cookie})
	audit.mustStatus(t, http.StatusOK, "audit")
	body := string(audit.body)
	if !strings.Contains(body, "machine.release") {
		t.Error("forgetting the machine was not audited")
	}
	if !strings.Contains(body, machineResourceID(m.Name)) {
		t.Error("the audit row does not keep the resource identifier, which is now the only record of it")
	}
}

// A machine already being deleted is the one case where forgetting the row would
// be the destructive act: the delete is under way and the VM is going anyway, so
// dropping the row would abandon a half-finished delete and leak the resource.
func TestForceDeletingTheHostOfAMachineThatIsAlreadyDeletingLeavesTheDeleteRunning(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	host := h.host("zoomies-mach-host")
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-leaving", store.MachineDeleting, host)

	forced := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID + "?force=true", cookie: cookie})
	forced.mustStatus(t, http.StatusNoContent, "force-delete the host of a deleting machine")

	row, err := h.st.GetMachine(h.ctx, m.ID)
	if err != nil {
		t.Fatalf("the deleting machine was forgotten, abandoning its delete: %v", err)
	}
	if row.State != store.MachineDeleting {
		t.Errorf("the machine is %s, want it left deleting", row.State)
	}
}

// An ordinary host is not a machine, and the check for one must not turn every
// host deletion into a refusal.
func TestDeletingAnOrdinaryHostIsUnaffectedByTheMachineCheck(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	host := h.host("a-machine-somebody-else-made")

	resp := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusNoContent, "delete an ordinary host")
}

// The machines list is what the Hosts page's band and the provider page both
// read, so its filters have to narrow rather than merely decorate -- and a
// state nobody has heard of is the caller's mistake, named.
func TestTheMachinesListIsFilteredByProviderAndState(t *testing.T) {
	h := newHarness(t)
	viewer, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(viewer)
	first := h.provider("proxmox-lab")
	second := h.provider("proxmox-dr")
	h.machineOn(first, "zoomies-mach-one", store.MachineReady, nil)
	h.machineOn(first, "zoomies-mach-two", store.MachinePlanned, nil)
	h.machineOn(second, "zoomies-mach-three", store.MachineReady, nil)

	byProvider := h.do(request{method: http.MethodGet, path: "/api/v1/machines?provider=" + first.ID, cookie: cookie})
	byProvider.mustStatus(t, http.StatusOK, "machines by provider")
	var page struct {
		Items []machineResponse `json:"items"`
		Total int               `json:"total"`
	}
	byProvider.into(t, &page)
	if page.Total != 2 {
		t.Errorf("filtering by provider returned %d machines, want 2", page.Total)
	}
	for _, m := range page.Items {
		if m.ProviderID != first.ID {
			t.Errorf("machine %s belongs to %s", m.Name, m.ProviderID)
		}
		if m.ProviderName != first.Name {
			t.Errorf("machine %s does not carry its provider's name, so the grid would show an identifier", m.Name)
		}
	}

	byState := h.do(request{method: http.MethodGet, path: "/api/v1/machines?state=ready", cookie: cookie})
	byState.mustStatus(t, http.StatusOK, "machines by state")
	byState.into(t, &page)
	if page.Total != 2 {
		t.Errorf("filtering by state returned %d machines, want 2", page.Total)
	}

	bad := h.do(request{method: http.MethodGet, path: "/api/v1/machines?state=melted", cookie: cookie})
	bad.mustStatus(t, http.StatusBadRequest, "an unknown machine state")
	if msg := bad.errorMessage(t); !strings.Contains(msg, "quarantined") {
		t.Errorf("the refusal does not list the states there are: %q", msg)
	}
}

// The event stream is how a page follows a machine, and a frame carrying a
// store row rather than the view would repaint it without its provider's name,
// its timeline or whether it is safe to delete.
func TestAMachineFrameIsTheShapeTheRouteReturns(t *testing.T) {
	h := newHarness(t)
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	prov := h.provider("proxmox-lab")
	m := h.machineOn(prov, "zoomies-mach-watched", store.MachineReady, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, resp := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event stream status %d", resp.StatusCode)
	}

	drained := h.do(request{method: http.MethodPost, path: "/api/v1/machines/" + m.ID + "/drain", cookie: cookie})
	drained.mustStatus(t, http.StatusOK, "drain a machine")

	frame := await(t, frames, "a machine frame", func(f sseFrame) bool {
		return f.event == "machine.updated"
	})
	for _, want := range []string{`"provider_name":"proxmox-lab"`, `"timeline"`, `"safe_to_delete"`} {
		if !strings.Contains(frame.data, want) {
			t.Errorf("the machine frame is not the GET shape; it is missing %s: %s", want, frame.data)
		}
	}
	if strings.Contains(frame.data, "owner_fingerprint") {
		t.Error("the frame carries the ownership fingerprint, which is the mark a delete is checked against")
	}
}
