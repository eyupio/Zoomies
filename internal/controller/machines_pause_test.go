package controller

import (
	"slices"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// The kill switch has three layers, and what separates them is what each one
// is allowed to stop. These tests are the table in the design, made to fail.

// The operator's switch stops the fleet buying and nothing else. A switch that
// also stopped draining and deleting would strand running machines nobody is
// watching -- which is the opposite of what somebody reaching for a kill switch
// wants.
func TestAPausedProviderCreatesNothingAndStillDrainsAndDeletes(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	ready := h.readyMachine(t, row)
	h.pauseProvider(t, row, "the hypervisor is being patched")
	h.queueWork(t)

	before := len(h.machines())
	h.machinePass(t)
	if got := len(h.machines()); got != before {
		t.Fatalf("a paused provider bought %d more machines", got-before)
	}

	h.beginDrainFor(t, ready)
	m := h.drive(t, ready.ID, store.MachineDeleted, 8)
	if m.DeletedAt == nil {
		t.Fatal("a paused provider would not release a machine; a kill switch that strands machines is not one")
	}
}

// The pause is a row rather than a live setting, because a kill switch a
// restart lifts is not a kill switch.
func TestThePauseSurvivesARestart(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	h.pauseProvider(t, row, "we are over budget this month")

	h.c = h.restart()
	got, err := h.st.GetProvider(h.ctx, row.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if held := h.c.provisioningHeld(got, h.c.Now()); held == "" {
		t.Fatal("a restart lifted the pause")
	}
	h.machinePass(t)
	if len(h.machines()) != 0 {
		t.Fatal("a restarted controller bought a machine from a paused provider")
	}
	if !slices.Contains(h.problemCodes(), "provider.provisioning_paused") {
		t.Fatalf("a paused provider raised %v, want provider.provisioning_paused", h.problemCodes())
	}
}

// Pressing the kill switch changes no setting a connection check would verify,
// so it must not tell the operator the provider "has not been checked since its
// settings changed". That warning turns the public status page degraded, and it
// would clear only after a check they had no reason to run.
func TestPausingACheckedProviderDoesNotClaimItsSettingsChanged(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	if err := h.st.SetProviderChecked(h.ctx, row.ID, time.Now(), ""); err != nil {
		t.Fatalf("SetProviderChecked: %v", err)
	}
	if slices.Contains(h.problemCodes(), "provider.template_unverified") {
		t.Fatalf("a freshly checked provider raised template_unverified: %v", h.problemCodes())
	}

	// updated_at has millisecond resolution; without the gap a pause in the
	// same millisecond as the check would pass for the wrong reason.
	time.Sleep(5 * time.Millisecond)
	h.pauseProvider(t, row, "the hypervisor is being patched")
	if slices.Contains(h.problemCodes(), "provider.template_unverified") {
		t.Fatalf("pausing raised template_unverified: %v", h.problemCodes())
	}

	time.Sleep(5 * time.Millisecond)
	if err := h.st.SetProviderPaused(h.ctx, row.ID, false, ""); err != nil {
		t.Fatalf("SetProviderPaused: %v", err)
	}
	if slices.Contains(h.problemCodes(), "provider.template_unverified") {
		t.Fatalf("resuming raised template_unverified: %v", h.problemCodes())
	}
}

// The same switch in the configuration file, for an operator who wants a
// restart to come back held.
func TestTheConfiguredPauseHoldsEveryProvider(t *testing.T) {
	h := newHarness(t)
	h.machineFleet(t)
	h.c.UpdateConfig(func(c *config.Config) { c.Provider.Paused = true })

	h.machinePass(t)
	if got := len(h.machines()); got != 0 {
		t.Fatalf("provider.paused bought %d machines", got)
	}
}

// The fence is stricter than the kill switch on purpose, and the difference is
// the delete. A fenced database may be a restored copy, so its machine rows may
// describe resources a different, live controller owns -- and destroying
// somebody else's running machine on the strength of a restored row is the
// worst thing this system could do.
func TestAFencedFleetTouchesNoProviderMutationAtAll(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	ready := h.readyMachine(t, row)
	h.beginDrainFor(t, ready)
	h.queueWork(t)

	deletes, creates := h.callsTo("delete"), h.callsTo("create")
	h.fence("restored from a backup taken on Tuesday")
	for range 3 {
		h.machinePass(t)
	}

	if got := h.callsTo("create"); got != creates {
		t.Fatalf("a fenced fleet made %d creates", got-creates)
	}
	if got := h.callsTo("delete"); got != deletes {
		t.Fatalf("a fenced fleet made %d deletes; a restored row may describe another controller's machine", got-deletes)
	}
	// Looking is still allowed, and is the whole reason a fenced fleet is
	// worth running at all: an operator has to be able to see what it would do.
	if h.callsTo("list")+h.callsTo("inspect") == 0 {
		t.Fatal("a fenced fleet stopped observing as well as acting, so nothing can be reviewed before the fence is lifted")
	}
}

// Lifting the fence does not hand the deletes straight back. Every machine has
// to prove its ownership afresh first, because the rows that survived the
// restore are exactly the rows whose resources may belong to somebody else.
func TestLiftingTheFenceMakesEveryMachineProveItselfAgain(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := h.readyMachine(t, row)
	if got := h.machineByID(t, m.ID); got.OwnershipVerifiedAt == nil {
		t.Fatal("a machine that was built and enrolled has no ownership proof to take away")
	}

	h.fence("restored from a backup")
	if err := h.c.Unfence(h.ctx); err != nil {
		t.Fatalf("Unfence: %v", err)
	}
	if got := h.machineByID(t, m.ID); got.OwnershipVerifiedAt != nil {
		t.Fatal("lifting the fence left yesterday's ownership proof standing, so a delete could act on it")
	}
	if ok, _ := machineSafeToDelete(h.machineByID(t, m.ID), h.c.Now()); ok {
		t.Fatal("a machine with no fresh proof is offered as safe to delete")
	}
}

// Two controllers each cloning a machine is the expensive failure this design
// exists for. Nothing else in this codebase stops on lease loss, because
// nothing else spends money.
func TestALeaseLostControllerCreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.machineFleet(t)
	h.c.leaseLost.Store(&store.ControllerLease{Holder: "ctl_theother", Host: "elsewhere"})

	h.machinePass(t)
	if got := len(h.machines()); got != 0 {
		t.Fatalf("a controller that has lost the database lease bought %d machines", got)
	}
	if h.callsTo("create") != 0 {
		t.Fatal("a controller that has lost the lease called a provider; two controllers cloning is two invoices")
	}
}

// A provider that has been switched off keeps everything it has and is offered
// nothing new. The distinction matters because the machines it owns are still
// real: disabling a provider must not be a way to lose track of them.
func TestADisabledProviderKeepsItsMachinesAndBuysNoMore(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := h.readyMachine(t, row)

	row.Enabled = false
	if err := h.st.UpdateProvider(h.ctx, row); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	h.queueWork(t)
	h.machinePass(t)

	if got := h.machineByID(t, m.ID); got.State != store.MachineReady {
		t.Fatalf("disabling a provider left its ready machine %s; the machine is still rented and still real", got.State)
	}
	if got := len(h.machines()); got != 1 {
		t.Fatalf("a disabled provider bought %d machines", got-1)
	}
}

// The machine loop is not started at all unless providers are switched on.
// Renting machines spends money, and nothing should start doing that because a
// release added the ability to.
func TestNothingIsRentedUntilProvidersAreSwitchedOn(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.pool(inst, "linux-x64")
	h.providerRow(t, "lab")
	h.queueWork(t)

	h.machinePass(t)
	if got := len(h.machines()); got != 0 {
		t.Fatalf("providers are off and %d machines were bought", got)
	}
	if got := h.callsTo("allocate"); got != 0 {
		t.Fatal("providers are off and one was called anyway")
	}
}
