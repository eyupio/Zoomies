package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedProvider(t *testing.T, s *Store) *Provider {
	t.Helper()
	p := &Provider{
		Kind: ProviderFake, Name: "lab", Endpoint: "https://pve.example:8006",
		MachineCapacity: 2, MachineBackend: BackendDocker, MaxMachines: 4,
		MaxCreatesInFlight: 1, IdleTimeout: Duration(15 * time.Minute), Enabled: true,
	}
	if err := s.CreateProvider(context.Background(), p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	return p
}

// seedMachine writes a machine straight into a state, which is how a test
// reaches one without replaying the whole lifecycle to get there.
func seedMachine(t *testing.T, s *Store, providerID string, state MachineState) *Machine {
	t.Helper()
	m := &Machine{ProviderID: providerID, State: state}
	if err := s.CreateMachine(context.Background(), m); err != nil {
		t.Fatalf("CreateMachine in %s: %v", state, err)
	}
	return m
}

var everyMachineState = []MachineState{
	MachinePlanned, MachineCreating, MachineStarting, MachineBootstrapping,
	MachineEnrolling, MachineReady, MachineDraining, MachineDeleting,
	MachineDeleted, MachineFailed, MachineQuarantined,
}

// Every column of a machine has to survive a round trip. The driver binds
// positionally and ignores a surplus argument, so an argument without a
// placeholder shifts every later column onto its neighbour's value -- and on
// this row that means a machine coming back pointing at somebody else's
// resource. Comparing the whole struct is what keeps the two lists honest.
func TestAMachineComesBackAsItWasWritten(t *testing.T) {
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	p := seedProvider(t, s)

	stamp := func(offset time.Duration) *time.Time {
		t := now.Add(offset)
		return &t
	}
	want := &Machine{
		ProviderID: p.ID, Name: "zoomies-mach-abcdefghijklm", State: MachineReady,
		Message: "running", PoolID: pool.ID,
		OwnerControllerID: "ctl_a", OwnerFingerprint: "fingerprint",
		ResourceZone: "pve-1", ResourceID: "143",
		ResourceDetail: StringMap{"template": "9000"}, Address: "10.0.0.5",
		OwnershipVerifiedAt: stamp(time.Minute), OwnershipError: "",
		HostID: host.ID, JoinTokenID: "join_abc", Capacity: 2,
		Labels: StringMap{"zone": "lab"},
		OpID:   "mop_abcdefghijklm", OpKind: MachineOpBootstrap, OpHandle: "UPID:pve-1:1",
		OpHolder: "ctl_a", OpStartedAt: stamp(time.Minute), OpDeadlineAt: stamp(5 * time.Minute),
		OpOutcomeUnknown: true,
		Attempts:         2, NextAttemptAt: stamp(time.Hour),
		ProviderError: "the node was busy", BootstrapError: "the agent would not install",
		ReservationExpiresAt: stamp(2 * time.Hour), CreateStartedAt: stamp(time.Second),
		CreatedOKAt: stamp(2 * time.Second), StartedAt: stamp(3 * time.Second),
		BootstrappedAt: stamp(4 * time.Second), EnrolledAt: stamp(5 * time.Second),
		ReadyAt: stamp(6 * time.Second), IdleSince: stamp(7 * time.Second),
		DrainingAt: stamp(8 * time.Second), DeleteStartedAt: stamp(9 * time.Second),
		DeletedAt: stamp(10 * time.Second),
	}
	if err := s.CreateMachine(ctx, want); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	got, err := s.GetMachine(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the machine came back changed:\n got  %+v\n want %+v", got, want)
	}
}

// The allow-list lives in the store rather than in the reconciler so that a
// pass which has just lost a race, or a provider that answered oddly, cannot
// talk a machine into a state that makes the fleet's accounting wrong. This
// walks every pair, so a state added to the map without a thought about what
// may reach it fails here rather than in production.
func TestAMachineOnlyMovesThroughStatesItsLifecycleAllows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)

	for _, from := range everyMachineState {
		for _, to := range everyMachineState {
			m := seedMachine(t, s, p.ID, from)
			got, err := s.TransitionMachine(ctx, m.ID, to, "")
			want := CanTransitionMachine(from, to)
			switch {
			case want && err != nil:
				t.Errorf("%s -> %s was refused: %v", from, to, err)
			case !want && !errors.Is(err, ErrInvalidTransition):
				t.Errorf("%s -> %s returned %v, want ErrInvalidTransition", from, to, err)
			case want && got.State != to:
				t.Errorf("%s -> %s left the machine in %s", from, to, got.State)
			}
		}
	}

	// A state nothing defines is refused before the row is read, so a typo in
	// a caller cannot write a value the CHECK constraint would then have to
	// catch.
	m := seedMachine(t, s, p.ID, MachineReady)
	if _, err := s.TransitionMachine(ctx, m.ID, MachineState("retired"), ""); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("transition to an invented state = %v, want ErrInvalidTransition", err)
	}
}

// A transition stamps the phase it enters, once. The machine page's timeline is
// derived from these stamps, so a stamp that moved every time a pass repeated
// itself would make the timeline say a machine had been created twice.
func TestEachPhaseIsStampedWhenItIsFirstEntered(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachinePlanned)

	if _, err := s.TransitionMachine(ctx, m.ID, MachineCreating, "asking for a VM"); err != nil {
		t.Fatal(err)
	}
	first := now
	now = now.Add(time.Minute)
	// Repeating the state a machine is already in is legal and must change
	// nothing: a pass that reports "still creating" is not a second create.
	got, err := s.TransitionMachine(ctx, m.ID, MachineCreating, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.CreateStartedAt == nil || !got.CreateStartedAt.Equal(first) {
		t.Fatalf("create_started_at = %v, want the first entry at %v", got.CreateStartedAt, first)
	}
	if got.Message != "asking for a VM" {
		t.Errorf("an empty message overwrote the one an operator can read: %q", got.Message)
	}

	// Draining is the exception: it can be cancelled and started again, and
	// the drain timeout has to count from the latest one.
	for _, to := range []MachineState{MachineBootstrapping, MachineEnrolling, MachineReady, MachineDraining} {
		if _, err := s.TransitionMachine(ctx, m.ID, to, ""); err != nil {
			t.Fatalf("%s: %v", to, err)
		}
	}
	now = now.Add(time.Hour)
	if _, err := s.TransitionMachine(ctx, m.ID, MachineReady, ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	again, err := s.TransitionMachine(ctx, m.ID, MachineDraining, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.DrainingAt == nil || !again.DrainingAt.Equal(now) {
		t.Fatalf("draining_at = %v after a second drain, want %v: the drain timeout would count from the abandoned one", again.DrainingAt, now)
	}
	if again.ReadyAt == nil || !again.ReadyAt.After(first) {
		t.Fatalf("ready_at = %v, want the first time this machine was ready", again.ReadyAt)
	}
}

// deleted_at is what stops a machine counting against its provider's ceiling
// and against the bill. Only a provider that looked and could not find the
// resource may write it, so a state change -- which is only ever what this
// controller believes -- must not.
func TestAStateChangeNeverConfirmsADelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineDeleting)

	got, err := s.TransitionMachine(ctx, m.ID, MachineDeleted, "the delete call returned 200")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt != nil {
		t.Fatal("moving to deleted stamped deleted_at; a 200 from a delete call is not a confirmation that the resource is gone")
	}
}

// "Counts against the ceiling" is defined twice: Machine.Owns for the
// reconciler's arithmetic, and the WHERE clause in CountOwnedMachines for the
// budget. Nothing held the two equal, so a machine that stopped counting in one
// and not the other would have a provider buying a machine it already pays for.
// This test is the equation -- live_rows_test.go's reason, one layer out.
func TestAMachineCountsAgainstItsProviderExactlyWhileItHasAResource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	now := s.Now()

	owned := 0
	for _, state := range everyMachineState {
		for _, withResource := range []bool{false, true} {
			for _, confirmedGone := range []bool{false, true} {
				m := seedMachine(t, s, p.ID, state)
				if withResource {
					// The machine's own id stands in for a VM id; what matters
					// here is that each row names a different resource.
					if err := s.SetMachineResource(ctx, m.ID, "pve-1", m.ID, "fp", "ctl_1"); err != nil {
						t.Fatalf("SetMachineResource: %v", err)
					}
				}
				if confirmedGone {
					if _, err := s.ConfirmMachineDeleted(ctx, m.ID, now); err != nil {
						t.Fatalf("ConfirmMachineDeleted: %v", err)
					}
				}
				if withResource && !confirmedGone {
					owned++
				}
			}
		}
	}

	rows, _, err := s.ListMachines(ctx, MachineFilter{IncludeDeleted: true}, Page{Limit: 500})
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	goSays := 0
	for _, m := range rows {
		if m.Owns() {
			goSays++
		}
	}
	sqlSays, err := s.CountOwnedMachines(ctx)
	if err != nil {
		t.Fatalf("CountOwnedMachines: %v", err)
	}
	if goSays != owned || sqlSays[p.ID] != owned {
		t.Fatalf("Owns() counts %d machines and CountOwnedMachines counts %d, with %d holding a resource: "+
			"the two halves of the ownership predicate disagree", goSays, sqlSays[p.ID], owned)
	}
	if owned == 0 || owned == len(rows) {
		t.Fatalf("%d of %d rows own a resource; the test needs both kinds to mean anything", owned, len(rows))
	}
}

// Two passes, or two controllers, reaching the same machine at once is the
// ordinary case on a fleet with a standby. The claim is one statement so that
// the decision is made inside the write: a read both of them could make first
// decides nothing, and two passes each believing they own a create is how one
// machine becomes two VMs.
func TestOnlyOnePassCanClaimAnOperation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachinePlanned)
	now := s.Now()

	first, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_a", Timeout: 5 * time.Minute}, now)
	if err != nil {
		t.Fatalf("the first claim was refused: %v", err)
	}
	if first.OpID == "" || first.OpHolder != "ctl_a" {
		t.Fatalf("the claim did not record who holds it: %+v", first)
	}

	_, err = s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_b", Timeout: 5 * time.Minute}, now)
	if !errors.Is(err, ErrMachineBusy) {
		t.Fatalf("the second claim = %v, want ErrMachineBusy", err)
	}
	// The refusal names the holder, because the operator reading it wants to
	// know which controller to go and look at.
	if msg := err.Error(); !strings.Contains(msg, "ctl_a") {
		t.Errorf("the refusal does not name the controller holding the machine: %s", msg)
	}

	// And finishing it gives the claim back, so the next pass can take a step.
	if err := s.FinishMachineOperation(ctx, m.ID, first.OpID, MachineOpSucceeded, "created", nil); err != nil {
		t.Fatalf("FinishMachineOperation: %v", err)
	}
	if _, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpStart, Holder: "ctl_b", Timeout: time.Minute}, now); err != nil {
		t.Fatalf("claiming after a finished operation: %v", err)
	}
}

// A claim needs a deadline because a controller that dies mid-operation must
// not hold a machine for ever. An expired one is therefore takeable -- and
// taking it must leave every piece of evidence about the call that went out, or
// the pass taking over has nothing to ask the provider about.
func TestAnExpiredClaimIsTakeableAndTakingItMutatesNothing(t *testing.T) {
	now := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineCreating)

	claimed, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_dead", Timeout: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMachineOperationHandle(ctx, m.ID, claimed.OpID, "UPID:pve-1:0000A1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMachineOutcomeUnknown(ctx, m.ID, claimed.OpID, "the request timed out after it went out"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMachineResource(ctx, m.ID, "pve-1", "143", "fp", "ctl_dead"); err != nil {
		t.Fatal(err)
	}

	// Still held while the deadline stands.
	now = now.Add(30 * time.Second)
	if _, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_live", Timeout: time.Minute}, now); !errors.Is(err, ErrMachineBusy) {
		t.Fatalf("a live claim was taken from its holder: %v", err)
	}

	now = now.Add(2 * time.Minute)
	taken, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_live", Timeout: time.Minute}, now)
	if err != nil {
		t.Fatalf("an expired claim was not takeable, so the machine is stuck for ever: %v", err)
	}
	if taken.OpHolder != "ctl_live" {
		t.Errorf("op_holder = %q after the takeover, want ctl_live", taken.OpHolder)
	}
	if taken.OpHandle != "UPID:pve-1:0000A1" {
		t.Errorf("op_handle = %q; the takeover threw away the only evidence of what the dead controller's call did", taken.OpHandle)
	}
	if !taken.OpOutcomeUnknown {
		t.Error("the takeover cleared op_outcome_unknown, so the next pass would read silence as failure and create a second machine")
	}
	if taken.ResourceID != "143" || taken.ResourceZone != "pve-1" {
		t.Errorf("the takeover moved the machine's identity: %s on %s", taken.ResourceID, taken.ResourceZone)
	}
	if taken.State != MachineCreating {
		t.Errorf("state = %s after a takeover; a claim is not a lifecycle change", taken.State)
	}
}

// A machine has one resource for its whole life. A second identity would be a
// leak with a row pointing away from it: the first VM still exists, still
// costs money, and nothing left in the database names it.
func TestAMachineKeepsTheResourceItWasFirstGiven(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineCreating)

	if err := s.SetMachineResource(ctx, m.ID, "pve-1", "143", "fingerprint-1", "ctl_a"); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}
	err := s.SetMachineResource(ctx, m.ID, "pve-2", "144", "fingerprint-2", "ctl_b")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a second resource = %v, want ErrConflict", err)
	}

	got, err := s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResourceID != "143" || got.ResourceZone != "pve-1" || got.OwnerFingerprint != "fingerprint-1" {
		t.Fatalf("the refused write still changed the row: %s on %s, fingerprint %q",
			got.ResourceID, got.ResourceZone, got.OwnerFingerprint)
	}

	// Two machines cannot name one resource either, which is what the sweep
	// relies on when it asks whose a VM is.
	other := seedMachine(t, s, p.ID, MachineCreating)
	if err := s.SetMachineResource(ctx, other.ID, "pve-1", "143", "fingerprint-3", "ctl_a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second machine took the same VM: %v", err)
	}
}

// The pass that stamps the confirmation is the one that publishes the deletion
// and frees the budget, so exactly one caller may stamp it however many
// controllers and retries arrive at the same answer at once.
func TestConfirmingADeleteStampsExactlyOnce(t *testing.T) {
	now := time.Date(2026, 9, 13, 11, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineDeleting)
	if err := s.SetMachineResource(ctx, m.ID, "pve-1", "143", "fp", "ctl_a"); err != nil {
		t.Fatal(err)
	}

	first, err := s.ConfirmMachineDeleted(ctx, m.ID, now)
	if err != nil || !first {
		t.Fatalf("ConfirmMachineDeleted = %v, %v; want the first confirmation to count", first, err)
	}
	later := now.Add(time.Hour)
	again, err := s.ConfirmMachineDeleted(ctx, m.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("a second confirmation also counted, so the deletion would be published twice and the budget freed twice")
	}

	got, err := s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == nil || !got.DeletedAt.Equal(now) {
		t.Fatalf("deleted_at = %v, want the first confirmation at %v", got.DeletedAt, now)
	}
	if got.Owns() {
		t.Fatal("a machine whose resource is confirmed gone still counts against its provider")
	}
}

// A restored database is a copy: the machines it names may have been deleted,
// rebuilt or handed to somebody else since the backup was taken. Ownership is
// what a delete needs, so a restore takes the proof away and each machine has
// to earn it again before this copy may destroy anything.
func TestARestoredDatabaseMarksEveryMachineUnverified(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	now := s.Now()

	var ids []string
	for range 3 {
		m := seedMachine(t, s, p.ID, MachineReady)
		if err := s.SetMachineOwnershipVerified(ctx, m.ID, now); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	untouched := seedMachine(t, s, p.ID, MachinePlanned)

	n, err := s.MarkMachinesUnverified(ctx)
	if err != nil {
		t.Fatalf("MarkMachinesUnverified: %v", err)
	}
	if n != 3 {
		t.Fatalf("MarkMachinesUnverified = %d, want the 3 machines that had a proof", n)
	}
	for _, id := range ids {
		got, err := s.GetMachine(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.OwnershipVerifiedAt != nil {
			t.Fatalf("machine %s kept its proof of ownership across a restore", id)
		}
		if got.State != MachineReady {
			t.Fatalf("machine %s was moved out of its state by the restore: %s", id, got.State)
		}
	}
	if got, err := s.GetMachine(ctx, untouched.ID); err != nil || got.OwnershipVerifiedAt != nil {
		t.Fatalf("the machine that never had a proof was changed: %v, %v", got, err)
	}
}

// An observation that failed takes the last good one with it. Leaving
// yesterday's stamp behind would let a check that has since gone wrong keep
// authorising deletions, which is the whole thing ownership exists to stop.
func TestAFailedOwnershipCheckTakesTheProofWithIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineReady)

	if err := s.SetMachineOwnershipVerified(ctx, m.ID, s.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMachineOwnershipError(ctx, m.ID, "VM 143 says it belongs to machine mach_91c"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnershipVerifiedAt != nil {
		t.Fatal("a failed ownership check left the previous proof standing")
	}
	if got.OwnershipError == "" {
		t.Fatal("nothing records what disagreed, so nobody can act on it")
	}

	// And a later check that agrees settles the complaint.
	if err := s.SetMachineOwnershipVerified(ctx, m.ID, s.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetMachine(ctx, m.ID); err != nil || got.OwnershipError != "" {
		t.Fatalf("a successful check left the old complaint behind: %q, %v", got.OwnershipError, err)
	}
}

// The two error columns belong to two independent systems: a hypervisor that
// would not answer, and an agent install inside the guest that failed. A
// success on one must not erase the other's complaint, which is the rule 0022
// wrote down for runner cleanup.
func TestAProviderSuccessDoesNotSettleTheGuestsComplaint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineBootstrapping)
	now := s.Now()

	if err := s.RecordMachineFailure(ctx, m.ID, MachineErrorBootstrap, "the agent would not install", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMachineFailure(ctx, m.ID, MachineErrorProvider, "the node was unreachable", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 2 || got.NextAttemptAt == nil {
		t.Fatalf("attempts = %d, next attempt = %v; the backoff has nothing to count from", got.Attempts, got.NextAttemptAt)
	}

	claimed, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpBootstrap, Holder: "ctl_a", Timeout: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMachineOperation(ctx, m.ID, claimed.OpID, MachineOpSucceeded, "", nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderError != "" || got.Attempts != 0 || got.NextAttemptAt != nil {
		t.Fatalf("a success left the provider's complaint or its backoff behind: %q, %d attempts, next %v",
			got.ProviderError, got.Attempts, got.NextAttemptAt)
	}
	if got.BootstrapError == "" {
		t.Fatal("a reachable hypervisor settled a complaint about the guest, which nothing has fixed")
	}
}

// Giving a claim back because a guard refused, or because the controller is
// stopping, is not the machine's fault. Counting it as an attempt would walk
// the backoff up towards giving up on a machine nothing has gone wrong with.
func TestReleasingAClaimDoesNotCountAnAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachinePlanned)
	now := s.Now()

	claimed, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_a", Timeout: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMachineOperation(ctx, m.ID, claimed.OpID, MachineOpReleased, "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 0 {
		t.Fatalf("attempts = %d after a released claim, want 0", got.Attempts)
	}
	if got.OpID != "" || got.OpKind != MachineOpNone {
		t.Fatalf("the claim was not given back: %q %q", got.OpID, got.OpKind)
	}

	// A failure is the other half of the same writer, and does count.
	again, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_a", Timeout: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	retry := now.Add(30 * time.Second)
	if err := s.FinishMachineOperation(ctx, m.ID, again.OpID, MachineOpFailed, "the clone failed", &retry); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetMachine(ctx, m.ID); err != nil {
		t.Fatal(err)
	} else if got.Attempts != 1 || got.ProviderError != "the clone failed" {
		t.Fatalf("a failed operation recorded %d attempts and %q", got.Attempts, got.ProviderError)
	}
}

// A pass whose claim expired and was taken by somebody else must not be able to
// finish the operation it no longer holds: the controller that took it over is
// the one that will answer for it.
func TestAPassThatLostItsClaimCannotFinishTheOperation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineCreating)
	now := s.Now()

	first, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_a", Timeout: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := s.ClaimMachineOperation(ctx, m.ID,
		MachineOperation{Kind: MachineOpCreate, Holder: "ctl_b", Timeout: time.Minute}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMachineOperation(ctx, m.ID, first.OpID, MachineOpFailed, "gave up", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMachine(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpID != taken.OpID || got.OpHolder != "ctl_b" {
		t.Fatalf("the previous holder finished an operation it had lost: op %q held by %q", got.OpID, got.OpHolder)
	}
	if got.Attempts != 0 {
		t.Fatalf("attempts = %d; the lost pass counted an attempt against somebody else's operation", got.Attempts)
	}
}

// host_id is the only thing that grants this controller deletion authority over
// a host, so it is written once, by the redemption of a token this machine
// minted, and never moved.
func TestAMachineIsEnrolledAsExactlyOneHost(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, host := seedPool(t, s)
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineEnrolling)
	now := s.Now()

	if err := s.LinkMachineHost(ctx, m.ID, host.ID, "join_abc", now); err != nil {
		t.Fatalf("LinkMachineHost: %v", err)
	}
	other := &Host{Name: "vm-2", Capacity: 2}
	if err := s.CreateHost(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkMachineHost(ctx, m.ID, other.ID, "join_def", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-linking a machine = %v, want ErrConflict", err)
	}

	// And two machines cannot claim one host, which is what stops a second row
	// acquiring authority over a host somebody already enrolled.
	second := seedMachine(t, s, p.ID, MachineEnrolling)
	if err := s.LinkMachineHost(ctx, second.ID, host.ID, "join_ghi", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("two machines claimed one host: %v", err)
	}

	got, err := s.GetMachineByHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetMachineByHost: %v", err)
	}
	if got.ID != m.ID || got.JoinTokenID != "join_abc" || got.EnrolledAt == nil {
		t.Fatalf("the enrolment link does not name the token that made it: %+v", got)
	}
}

// A machine name is what a sweep matches on a hypervisor an operator also runs
// their own guests on. Mistaking somebody else's VM for one of ours deletes
// something nobody asked us to touch, so the brand is not decoration.
func TestAMachineNameSaysWhichFleetRentedIt(t *testing.T) {
	s := newTestStore(t)
	m := seedMachine(t, s, seedProvider(t, s).ID, MachinePlanned)

	if !IsMachineName(m.Name) {
		t.Fatalf("CreateMachine named a machine %q, which IsMachineName does not recognise", m.Name)
	}
	for _, name := range []string{"debian-12", "zoomies", "zoomies-linux-x64-corgi-abcdefgh", "", "mach_abc"} {
		if IsMachineName(name) {
			t.Errorf("IsMachineName(%q) = true, so a sweep would treat it as ours", name)
		}
	}
}

// A machine still carrying a resource is never pruned however old it is: the
// row is the only record that something was rented and may still be running,
// and deleting it is how a fleet loses a VM it is still paying for.
func TestPruningKeepsEveryMachineThatMayStillExist(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	p := seedProvider(t, s)

	gone := seedMachine(t, s, p.ID, MachineDeleting)
	if err := s.SetMachineResource(ctx, gone.ID, "pve-1", "143", "fp", "ctl_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmMachineDeleted(ctx, gone.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionMachine(ctx, gone.ID, MachineDeleted, ""); err != nil {
		t.Fatal(err)
	}
	neverBuilt := seedMachine(t, s, p.ID, MachineFailed)
	stillOut := seedMachine(t, s, p.ID, MachineFailed)
	if err := s.SetMachineResource(ctx, stillOut.ID, "pve-1", "144", "fp", "ctl_a"); err != nil {
		t.Fatal(err)
	}
	live := seedMachine(t, s, p.ID, MachineReady)

	n, ids, err := s.PruneMachines(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("PruneMachines: %v", err)
	}
	if n != 2 || len(ids) != 2 {
		t.Fatalf("PruneMachines removed %d rows (%v), want the confirmed-deleted one and the one that never existed", n, ids)
	}
	for _, id := range []string{gone.ID, neverBuilt.ID} {
		if !contains(ids, id) {
			t.Errorf("%s was kept, though nothing of it exists", id)
		}
	}
	for _, id := range []string{stillOut.ID, live.ID} {
		if _, err := s.GetMachine(ctx, id); err != nil {
			t.Errorf("%s was pruned while its resource may still exist: %v", id, err)
		}
	}
}

// The recovery cursor is a keyset rather than an offset, so one machine stuck
// behind an unreachable node cannot starve the rest of the queue.
func TestPendingWorkPagesPastTheMachinesItHasAlreadySeen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)

	want := map[string]bool{}
	for range 5 {
		want[seedMachine(t, s, p.ID, MachineCreating).ID] = true
	}
	settled := seedMachine(t, s, p.ID, MachineDeleting)
	if _, err := s.TransitionMachine(ctx, settled.ID, MachineDeleted, ""); err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	cursor := ""
	for range 10 {
		page, err := s.PendingMachineWork(ctx, cursor, 2)
		if err != nil {
			t.Fatalf("PendingMachineWork: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			seen[m.ID]++
			cursor = m.ID
		}
	}
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("machine %s was served %d times, want once", id, seen[id])
		}
	}
	if seen[settled.ID] != 0 {
		t.Errorf("a machine whose life is over is still offered as work")
	}
}

// A token minted for one machine travels inside a guest, where it is readable
// by more people than one an operator pastes into a terminal. Recording which
// machine it belongs to is what a later slice checks the redemption against.
func TestAJoinTokenRemembersTheMachineItWasMintedFor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)
	m := seedMachine(t, s, p.ID, MachineBootstrapping)

	tok := &JoinToken{
		TokenHash: "hash", Prefix: "zjt_", Capacity: 2,
		ExpiresAt: s.Now().Add(time.Hour), MachineID: m.ID, ExpectedName: m.Name,
	}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	got, err := s.GetJoinToken(ctx, tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != m.ID || got.ExpectedName != m.Name {
		t.Fatalf("the token came back scoped to %q/%q, want %q/%q", got.MachineID, got.ExpectedName, m.ID, m.Name)
	}

	// A token an operator asked for by hand carries neither, and still works.
	plain := &JoinToken{TokenHash: "hash-2", Prefix: "zjt_", ExpiresAt: s.Now().Add(time.Hour)}
	if err := s.CreateJoinToken(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetJoinToken(ctx, plain.ID); err != nil || got.MachineID != "" {
		t.Fatalf("an unscoped join token came back scoped to %q: %v", got.MachineID, err)
	}
}

// A forced host delete that forgets the machine behind it has to be one
// transaction. Done as two writes, a failure or a cancelled request between them
// leaves the machine row gone and the host standing: the VM is still billed and
// nothing records that it exists.
func TestDeletingAHostForgetsItsMachineOnlyWhenTheHostGoesToo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)
	prov := seedProvider(t, s)
	m := &Machine{ProviderID: prov.ID, State: MachineReady, HostID: host.ID}
	if err := s.CreateMachine(ctx, m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}

	// The host is not there to delete, so the machine must not be forgotten
	// either.
	if _, err := s.DeleteHostForgettingMachine(ctx, "host_missing", m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing host = %v, want ErrNotFound", err)
	}
	if _, err := s.GetMachine(ctx, m.ID); err != nil {
		t.Fatalf("the machine was forgotten although the host delete failed: %v", err)
	}

	if _, err := s.DeleteHostForgettingMachine(ctx, host.ID, m.ID); err != nil {
		t.Fatalf("DeleteHostForgettingMachine: %v", err)
	}
	if _, err := s.GetHost(ctx, host.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the host survived: %v", err)
	}
	if _, err := s.GetMachine(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the machine survived its host: %v", err)
	}
}
