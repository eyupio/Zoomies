package controller

import (
	"github.com/eyupio/zoomies/internal/config"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/store"
)

// A timeout says nothing about whether the work happened. Treating one as a
// failure is how a fleet ends up with two machines and one row -- and the row
// names only one of them, so the other is never drained, never deleted and
// never anything but a line on the bill.
func TestATimeoutIsNotEvidenceThatCreationFailed(t *testing.T) {
	h := newHarness(t)
	h.machineFleet(t)
	h.fake.SetAmbiguousAfterWork("create")

	h.machinePass(t)
	m := h.onlyMachine(t)
	if !m.OpOutcomeUnknown {
		t.Fatal("a create whose answer was never heard was not recorded as unknown; the next pass would retry it")
	}

	// The connection comes back. Every pass from here resolves by asking.
	h.fake.ClearFailures()
	for range 3 {
		h.pastBackoff(t, m.ID)
		h.machinePass(t)
	}
	if got := h.callsTo("create"); got != 1 {
		t.Fatalf("the provider was asked to create %d times after one ambiguous answer, want once", got)
	}
	if got := len(h.fake.Machines()); got != 1 {
		t.Fatalf("the provider holds %d machines after one ambiguous create, want 1", got)
	}
	if got := h.machineByID(t, m.ID); got.OwnershipVerifiedAt == nil {
		t.Fatal("the machine was never adopted: an ambiguous create is resolved by finding what was made")
	}
	h.assertOneResourcePerMachine(t)
}

// An ambiguous answer with nothing behind it is the same answer as one with a
// machine behind it. The difference is only knowable by looking, and looking
// twice before reusing the identity is what stops a provider that is slow to
// admit a resource exists from being asked for a second one.
func TestAnAmbiguousCreateIsResolvedByLookingNotByCreating(t *testing.T) {
	h := newHarness(t)
	h.machineFleet(t)
	h.fake.SetAmbiguousBeforeWork("create")

	h.machinePass(t)
	m := h.onlyMachine(t)
	h.fake.ClearFailures()

	// The first look finds nothing and creates nothing.
	h.pastBackoff(t, m.ID)
	h.machinePass(t)
	if got := h.callsTo("create"); got != 1 {
		t.Fatalf("the provider was asked to create %d times after one look, want the first one only", got)
	}

	// The second, a call timeout later, reuses the identity rather than
	// allocating a new one.
	h.pastBackoff(t, m.ID)
	h.machinePass(t)
	if got := h.callsTo("allocate"); got != 1 {
		t.Fatalf("a second identity was allocated (%d allocates); a retry of one machine reuses the identity it already has", got)
	}
	h.assertOneResourcePerMachine(t)
}

// Past the ambiguity timeout the honest answer is that nobody knows, and the
// honest action is to ask a person. Retrying for ever would leave a machine
// that may exist being paid for, with nothing on any page to say so.
func TestAnUnresolvedAmbiguityIsQuarantinedRatherThanRetried(t *testing.T) {
	h := newHarness(t)
	h.machineFleet(t)
	h.fake.SetAmbiguousBeforeWork("create")

	h.machinePass(t)
	m := h.onlyMachine(t)
	h.fake.ClearFailures()
	h.advance(h.cfg.Provider.AmbiguityTimeout + time.Minute)

	h.machinePass(t)
	got := h.machineByID(t, m.ID)
	if got.State != store.MachineQuarantined {
		t.Fatalf("an ambiguity nobody could resolve left the machine %s, want quarantined", got.State)
	}
	if !slices.Contains(h.problemCodes(), "provider.ownership_unverified") {
		t.Fatalf("a quarantined machine raised %v, want provider.ownership_unverified", h.problemCodes())
	}
}

// provider.create_timeout is documented as bounding the whole asynchronous
// creation of a machine, and the ambiguity timeout only covers a resource the
// provider cannot find. A create whose task never finishes -- a PVE task lost
// with its node, a stale clone lock -- was therefore waited on for ever, and
// because a machine that is still creating counts against the in-flight create
// limit, one such machine stopped the provider buying anything.
func TestAMachineStuckCreatingIsFailedAtTheCreateTimeoutAndFreesItsSlot(t *testing.T) {
	h := newHarness(t)
	h.machineFleet(t)
	h.fake.SetAsync("create", 1<<30)

	h.machinePass(t)
	m := h.onlyMachine(t)
	if m.State != store.MachineCreating {
		t.Fatalf("machine is %s after the first pass, want creating", m.State)
	}

	// Inside the timeout it is simply a slow create, and is left alone.
	h.advance(h.cfg.Provider.CreateTimeout / 2)
	h.machinePass(t)
	if got := h.machineByID(t, m.ID); got.State != store.MachineCreating {
		t.Fatalf("a create inside its timeout was %s, want it left creating", got.State)
	}

	h.advance(h.cfg.Provider.CreateTimeout)
	h.machinePass(t)
	got := h.machineByID(t, m.ID)
	if got.State != store.MachineFailed {
		t.Fatalf("a create that ran past provider.create_timeout is %s, want failed", got.State)
	}
	if got.State.Pending() {
		t.Fatal("a failed machine still counts against the in-flight create limit")
	}
	if !strings.Contains(got.Message, "still being paid for") {
		t.Fatalf("the failure does not say the resource may still be billed: %q", got.Message)
	}
	if h.callsTo("delete") != 0 {
		t.Fatal("giving up on a create deleted a resource that may be somebody's work")
	}
	if !slices.Contains(h.problemCodes(), "provider.machine_failed") {
		t.Fatalf("a failed machine raised %v, want provider.machine_failed", h.problemCodes())
	}
}

// The starting phase is bounded by the same setting, counted from when the
// resource was created, for a provider that cannot be powered on by us.
func TestAMachineStuckStartingIsFailedAtTheCreateTimeout(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := &store.Machine{ProviderID: row.ID, State: store.MachinePlanned}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	if err := h.st.SetMachineResource(h.ctx, m.ID, "zone-a", "9001", "ours", h.c.controllerID()); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}
	for _, to := range []store.MachineState{store.MachineCreating, store.MachineStarting} {
		if _, err := h.st.TransitionMachine(h.ctx, m.ID, to, ""); err != nil {
			t.Fatalf("TransitionMachine to %s: %v", to, err)
		}
	}

	h.advance(h.cfg.Provider.CreateTimeout + time.Minute)
	h.machinePass(t)
	if got := h.machineByID(t, m.ID); got.State != store.MachineFailed {
		t.Fatalf("a machine starting for longer than provider.create_timeout is %s, want failed", got.State)
	}
}

// A machine wearing our naming and somebody else's ownership marks is another
// fleet's. Deleting it would destroy a machine that is running somebody's work,
// so it is quarantined and left exactly where it is.
func TestAVMWearingOurNameWithAnotherOwnerIsQuarantinedNotDeleted(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)

	// A machine of another fleet's, wearing the naming this one mints: a
	// recycled identifier, which is the realistic accident.
	m := &store.Machine{ProviderID: row.ID, State: store.MachineCreating}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	h.fake.PlantForeign(m.Name)
	theirs := h.fake.Machines()[0]
	if err := h.st.SetMachineResource(h.ctx, m.ID, theirs.Ref.Zone, theirs.Ref.ID,
		"ours", h.c.controllerID()); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}

	h.machinePass(t)

	got := h.machineByID(t, m.ID)
	if got.State != store.MachineQuarantined {
		t.Fatalf("a resource owned by another fleet left our machine %s, want quarantined", got.State)
	}
	if got.OwnershipError == "" {
		t.Fatal("a quarantined machine says nothing about what disagreed; the whole entry is for a person to read")
	}
	if h.callsTo("delete") != 0 {
		t.Fatal("a resource this fleet could not prove it owns was deleted")
	}
}

// A resource wearing this fleet's naming with no row at all is reported and
// never removed. One left over from a database that was lost is the operator's
// to remove; one still doing work belongs to something else, and a sweep cannot
// tell the two apart.
func TestAnUntrackedResourceIsReportedAndNeverDeleted(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	name := store.NewMachineName("mach_orphaned")
	h.fake.PlantOrphan(name, provider.Owner{ControllerID: h.c.controllerID(), ProviderID: row.ID})

	h.machinePass(t)

	orphans := h.c.ProviderOrphans()[row.ID]
	if !slices.Contains(orphans, name) {
		t.Fatalf("the sweep reported orphans %v, want %s among them", orphans, name)
	}
	if !slices.Contains(h.problemCodes(), "provider.orphan_found") {
		t.Fatalf("an untracked resource raised %v, want provider.orphan_found", h.problemCodes())
	}
	if h.callsTo("delete") != 0 {
		t.Fatal("an untracked resource was deleted; it may be somebody else's and it may be running")
	}
	for _, got := range h.fake.Machines() {
		if got.Ref.Name == name {
			return
		}
	}
	t.Fatal("the untracked resource is gone from the provider, and nothing here should have removed it")
}

// A resource that went away outside Zoomies is not a delete that worked: there
// is nothing left to delete, whatever was running on it has been lost, and the
// fleet has to say so rather than quietly recording a tidy removal.
func TestAMachineThatVanishedOutsideZoomiesSaysSo(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := h.readyMachine(t, row)

	h.fake.Vanish(m.ResourceID)
	h.advance(h.cfg.Provider.SweepInterval + time.Minute)
	h.machinePass(t)

	got := h.machineByID(t, m.ID)
	if got.State != store.MachineFailed {
		t.Fatalf("a machine destroyed outside Zoomies is %s, want failed", got.State)
	}
	if got.DeletedAt != nil {
		t.Fatal("a machine nobody deleted was recorded as deleted; that stamp is what frees the budget and it has to mean a delete")
	}
	if !slices.Contains(h.problemCodes(), "provider.machine_failed") {
		t.Fatalf("a vanished machine raised %v, want provider.machine_failed", h.problemCodes())
	}
}

// A sweep's answer goes stale. A delete needs a fresh look, in the pass that
// deletes, because an observation from ten minutes ago is a statement about a
// machine that may have been rebuilt since.
func TestADeleteAlwaysInspectsAfresh(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := h.readyMachine(t, row)

	before := h.callsTo("inspect")
	h.beginDrainFor(t, m)
	h.drive(t, m.ID, store.MachineDeleted, 8)

	if h.callsTo("inspect") <= before {
		t.Fatal("a machine was deleted without a fresh inspect; a sweep's answer is not evidence for a delete")
	}
	// And the view says the same thing to an operator: a machine whose last
	// confirmation has gone stale is not offered a delete button.
	stale := &store.Machine{ID: "mach_x", ResourceID: "143",
		OwnershipVerifiedAt: ptrTime(h.c.Now().Add(-2 * observationMaxAge))}
	if ok, why := machineSafeToDelete(stale, h.c.Now()); ok {
		t.Fatalf("a machine last verified %s ago was offered as safe to delete (%q)", 2*observationMaxAge, why)
	}
}

// A reservation that never became a create is released rather than left
// holding a place in the ceiling for ever. Nothing was created -- the identity
// is written before the call and this row has none -- so nothing is at risk.
func TestAReservationThatNeverBecameACreateIsReleased(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	expires := h.c.Now().Add(-time.Minute)
	m := &store.Machine{ProviderID: row.ID, State: store.MachinePlanned, ReservationExpiresAt: &expires}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}

	h.machinePass(t)
	got := h.machineByID(t, m.ID)
	if got.State != store.MachineFailed {
		t.Fatalf("an expired reservation is %s, want failed so it stops holding a place in the ceiling", got.State)
	}
}

// A repeated observation changes nothing. Deliveries and sweeps are
// at-least-once, so a pass that saw the same thing twice must write the same
// row twice rather than moving the machine on twice.
func TestARepeatedProviderObservationChangesNothing(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := h.readyMachine(t, row)

	before := h.machineByID(t, m.ID)
	for range 3 {
		h.advance(h.cfg.Provider.SweepInterval + time.Minute)
		h.machinePass(t)
	}
	after := h.machineByID(t, m.ID)
	if after.State != before.State {
		t.Fatalf("three sweeps over an unchanged machine moved it from %s to %s", before.State, after.State)
	}
	if !after.ReadyAt.Equal(*before.ReadyAt) {
		t.Fatal("a repeated observation rewrote when the machine became ready; the timeline is first-write-wins")
	}
	h.assertOneResourcePerMachine(t)
}

// A host an operator enrolled by hand never becomes a machine this controller
// may destroy. Deletion authority is the existence of a machine row this
// controller wrote before the resource existed, and there is no route that
// attaches one to a host somebody already had.
func TestAnImportedHostNeverAcquiresDeletionAuthority(t *testing.T) {
	h := newHarness(t)
	h.enableProviders(t)
	host := h.host("imported-1")

	if _, err := h.st.GetMachineByHost(h.ctx, host.ID); err == nil {
		t.Fatal("a host nobody rented has a machine row, which is the only thing that grants a delete")
	}
	// Labelling it cannot change that: labels are the operator's and are
	// writable through the API.
	host.Labels = store.StringMap{"zoomies.machine": "mach_pretend"}
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	if _, err := h.st.GetMachineByHost(h.ctx, host.ID); err == nil {
		t.Fatal("a label gave a host a machine row; a host that can be labelled into deletion authority can be destroyed by anyone with the operator role")
	}
}

// A credential that enrolled somebody else is a copied guest.
//
// The machine minted a token for itself, and a different machine spent it. That
// can only happen if the guest was cloned after it was given its credential, and
// the answer is emphatically not to take the host: two machines now claim it,
// and which one owns it is a question for a person. The alternative -- linking
// it anyway -- would hand this machine deletion authority over a host that
// belongs to another.
func TestATokenSpentBySomebodyElseQuarantinesTheMachineRatherThanTakingTheHost(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)

	m := &store.Machine{ProviderID: row.ID, State: store.MachineEnrolling}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	// The token belongs to another machine and was legitimately redeemed by the
	// host it names. What is wrong is this row pointing at it -- a restored
	// database, or a row edited by hand -- and the scope check cannot catch
	// that, because the redemption itself was correct.
	tok, _, err := h.c.Auth().CreateScopedJoinToken(h.ctx, auth.JoinScope{
		TTL: time.Hour, Capacity: 1, MachineID: "mach_somebodyelse", ExpectedName: "zoomies-mach-stranger",
	})
	if err != nil {
		t.Fatalf("CreateScopedJoinToken: %v", err)
	}
	host := h.host("zoomies-mach-stranger")
	if _, err := h.st.RedeemJoinToken(h.ctx, tok.TokenHash, store.JoinClaim{HostID: host.ID, Name: host.Name}, h.c.Now()); err != nil {
		t.Fatalf("RedeemJoinToken: %v", err)
	}
	if err := h.st.SetMachineJoinToken(h.ctx, m.ID, tok.ID); err != nil {
		t.Fatalf("SetMachineJoinToken: %v", err)
	}

	h.machinePass(t)

	got := h.machineByID(t, m.ID)
	if got.State != store.MachineQuarantined {
		t.Fatalf("state = %s, want quarantined: a token spent by another machine is a copied guest", got.State)
	}
	if got.HostID != "" {
		t.Fatalf("the machine took host %s, which another machine's token enrolled", got.HostID)
	}
	if got.OwnershipError == "" {
		t.Fatal("the quarantine says nothing about what disagreed, and the entry exists for a person to read")
	}
}

// A machine that has minted a credential and not yet had it spent is simply
// waiting. It is neither ready nor broken, and a pass that moved it either way
// would be guessing.
func TestAMachineWhoseCredentialIsUnspentIsStillWaiting(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)

	m := &store.Machine{ProviderID: row.ID, State: store.MachineEnrolling}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	tok, _, err := h.c.Auth().CreateScopedJoinToken(h.ctx, auth.JoinScope{
		TTL: time.Hour, Capacity: 1, MachineID: m.ID, ExpectedName: m.Name,
	})
	if err != nil {
		t.Fatalf("CreateScopedJoinToken: %v", err)
	}
	if err := h.st.SetMachineJoinToken(h.ctx, m.ID, tok.ID); err != nil {
		t.Fatalf("SetMachineJoinToken: %v", err)
	}

	h.machinePass(t)

	got := h.machineByID(t, m.ID)
	if got.State != store.MachineEnrolling {
		t.Fatalf("state = %s, want it still enrolling: nothing has happened yet", got.State)
	}
	if got.HostID != "" {
		t.Fatal("a machine whose token nobody redeemed was linked to a host anyway")
	}
}

// A machine in enrolling with no credential at all waits for its timeout to say
// so, rather than being walked backwards. Nothing returns to bootstrapping: the
// state machine has no such edge, because a machine that may already have been
// handed a credential must not be handed a second one.
func TestAMachineWithNoCredentialIsNotWalkedBackwards(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)

	m := &store.Machine{ProviderID: row.ID, State: store.MachineEnrolling}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}

	h.machinePass(t)

	if got := h.machineByID(t, m.ID); got.State != store.MachineEnrolling {
		t.Fatalf("state = %s, want it left alone until its enrolment timeout speaks", got.State)
	}
}

// A resource that went away underneath a working machine is not a deletion this
// fleet can tidy up: there is nothing left to delete, and whatever the host was
// running is gone. Its host is cordoned so no more work is placed on something
// that no longer exists, and the machine is failed with the provider's name in
// the sentence rather than silently removed.
func TestAMachineDestroyedOutsideZoomiesCordonsItsHostAndSaysSo(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)

	m := &store.Machine{ProviderID: row.ID, State: store.MachineReady}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	host := h.host(m.Name)
	if err := h.st.SetMachineResource(h.ctx, m.ID, "zone-a", "9001", "ours", h.c.controllerID()); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}
	if err := h.st.LinkMachineHost(h.ctx, m.ID, host.ID, "", h.c.Now()); err != nil {
		t.Fatalf("LinkMachineHost: %v", err)
	}
	// The provider has never heard of it: somebody removed it by hand.
	h.machinePass(t)

	got := h.machineByID(t, m.ID)
	if got.State != store.MachineFailed {
		t.Fatalf("state = %s, want failed: the resource is gone and there is nothing to delete", got.State)
	}
	if got.ProviderError == "" {
		t.Fatal("a machine that vanished says nothing about it; the message is the only record")
	}
	gotHost, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !gotHost.Cordoned {
		t.Fatal("the host of a machine that no longer exists is still taking work")
	}
}

// A provider that answers its listing slowly cannot hold up the fleet.
//
// The sweep runs inside the pass, before demand, so everything behind it waits:
// every reservation, every drain and every delete. Proxmox reads one
// configuration per guest to find its marks, so a cluster whose API has wedged
// stalls once per candidate rather than once -- which is how a sweep nobody
// bounded blocks a pass for many minutes and leaves a drained machine powered
// on and billed. A sweep cut short costs nothing: the next pass retries it with
// nothing stamped and nothing acted on.
func TestASlowSweepIsCutShortRatherThanHoldingUpThePass(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	// The interval is the ceiling on the budget, so it is the cheap way to
	// make this test's bound small; the provider's own asking price would
	// otherwise set it.
	h.c.UpdateConfig(func(c *config.Config) {
		c.Provider.CallTimeout = 20 * time.Millisecond
		c.Provider.SweepInterval = 200 * time.Millisecond
	})
	h.cfg = h.c.Config()
	h.fake.SetDelay("list", 30*time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.machinePass(t)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("a pass was still waiting on a provider's listing; the sweep is not bounded")
	}

	// The sweep did not finish, so it is not recorded as having happened: the
	// interval is paced by that stamp, and a sweep cut short has to be tried
	// again rather than counted.
	fresh, err := h.st.GetProvider(h.ctx, row.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if fresh.LastSweepAt != nil {
		t.Errorf("a sweep that timed out was recorded as done at %v", fresh.LastSweepAt)
	}
}

// The budget is a call apiece, because a sweep is a listing plus a read per
// candidate -- and never longer than the interval that paces it, since a sweep
// still running when the next is due has already failed at being paced.
func TestTheSweepBudgetGrowsWithTheMachinesItHasToReadAndStopsAtTheInterval(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	h.c.UpdateConfig(func(c *config.Config) {
		c.Provider.CallTimeout = time.Second
		c.Provider.SweepInterval = time.Hour
	})
	h.cfg = h.c.Config()
	pr, err := h.c.providerFor(h.ctx, row)
	if err != nil {
		t.Fatalf("building the provider: %v", err)
	}
	// A provider may ask for longer than the operator's timeout and never for
	// less supervision, so the per-call figure is whichever is larger. Read it
	// rather than assumed, so this test says the rule instead of a number.
	call := h.c.Config().Provider.CallTimeout
	if ask := pr.p.Capabilities().Deadlines.Call; ask > call {
		call = ask
	}

	env := &machineEnv{cfg: h.c.Config()}
	if got := h.c.sweepTimeout(env, pr); got != 2*call {
		t.Errorf("with no machines the budget is %s, want %s: room for the listing and one orphan", got, 2*call)
	}
	for range 4 {
		env.list = append(env.list, &store.Machine{ProviderID: row.ID})
	}
	// Another provider's machines are not this sweep's to read.
	env.list = append(env.list, &store.Machine{ProviderID: "prv_elsewhere"})
	if got := h.c.sweepTimeout(env, pr); got != 6*call {
		t.Errorf("with four machines the budget is %s, want %s: a call apiece plus two", got, 6*call)
	}

	env.cfg.Provider.SweepInterval = call / 2
	if got := h.c.sweepTimeout(env, pr); got != call/2 {
		t.Errorf("the budget is %s, want it capped at the interval that paces it", got)
	}
}

// A quarantined machine's host stops taking work.
//
// The scheduler's snapshot carries hosts and never machines, so it cannot work
// this out for itself: without the cordon a machine can be recorded as one the
// fleet cannot prove it owns while its host goes on accepting runners. Whoever
// the resource really belongs to may power it off at any moment, and every
// runner placed after the quarantine is a job lost.
//
// Driven through quarantineMachine rather than through a sweep because the
// store will not let a test repoint a machine that already has a resource --
// "a machine has one resource for its whole life" -- and a ready machine, which
// is the only kind with a host, always has one.
func TestQuarantiningAMachineStopsItsHostTakingNewWork(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := h.readyMachine(t, row)
	if m.HostID == "" {
		t.Fatal("a ready machine has no host; the fixture is not what this test needs")
	}
	host, err := h.st.GetHost(h.ctx, m.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.Cordoned {
		t.Fatal("the host was already cordoned before the quarantine")
	}

	if err := h.c.quarantineMachine(h.ctx, &machineEnv{now: h.c.Now()}, m,
		"that resource's ownership record says another machine"); err != nil {
		t.Fatalf("quarantineMachine: %v", err)
	}

	if got := h.machineByID(t, m.ID); got.State != store.MachineQuarantined {
		t.Fatalf("the machine is %s, want quarantined", got.State)
	}
	fresh, err := h.st.GetHost(h.ctx, m.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !fresh.Cordoned {
		t.Error("a quarantined machine's host is still taking new runners")
	}
	if h.callsTo("delete") != 0 {
		t.Error("a resource this fleet could not prove it owns was deleted")
	}
}

// And a quarantine raised before there is a host cordons nothing, because there
// is nothing to cordon -- every pre-enrolment quarantine is in that position.
func TestQuarantiningAMachineWithNoHostYetCordonsNothing(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	m := &store.Machine{ProviderID: row.ID, State: store.MachineCreating}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	h.fake.PlantForeign(m.Name)
	theirs := h.fake.Machines()[0]
	if err := h.st.SetMachineResource(h.ctx, m.ID, theirs.Ref.Zone, theirs.Ref.ID,
		"ours", h.c.controllerID()); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}

	h.machinePass(t)

	if got := h.machineByID(t, m.ID); got.State != store.MachineQuarantined {
		t.Fatalf("the machine is %s, want quarantined", got.State)
	}
	hosts, err := h.st.ListHosts(h.ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	for _, host := range hosts {
		if host.Cordoned {
			t.Errorf("host %s was cordoned by a quarantine that had no host of its own", host.ID)
		}
	}
}
