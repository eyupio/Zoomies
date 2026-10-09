package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// updated_at says when the provider's configuration last changed, and the
// problems list compares it with the last connection check. Pausing and
// resuming change no setting a check would verify, so they leave it alone;
// moving it would raise "not checked since its settings changed" on the page
// of an operator who has just pressed the kill switch, and would make the
// controller rebuild a provider client that had not changed.
func TestPausingAndResumingAProviderLeaveItsUpdatedAtAlone(t *testing.T) {
	now := time.Date(2026, 5, 9, 8, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	p := seedProvider(t, s)
	created := p.UpdatedAt

	for _, paused := range []bool{true, false} {
		now = now.Add(time.Hour)
		if err := s.SetProviderPaused(ctx, p.ID, paused, "the hypervisor is being upgraded"); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetProvider(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Paused != paused {
			t.Fatalf("paused = %v, want %v", got.Paused, paused)
		}
		if !got.UpdatedAt.Equal(created) {
			t.Fatalf("pausing=%v moved updated_at from %v to %v", paused, created, got.UpdatedAt)
		}
	}
}

// Everything about a provider has to survive a round trip, because a controller
// reads these rows on the pass after the one that wrote them and acts on what
// comes back. A field that silently did not persist would be a ceiling that
// stopped applying.
func TestAProviderComesBackAsItWasWritten(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p := &Provider{
		Kind: ProviderFake, Name: "lab", Endpoint: "https://pve.example:8006",
		CAPEM: "-----BEGIN CERTIFICATE-----", InsecureSkipVerify: true,
		Settings:        StringMap{"node": "pve-1", "storage": "local-lvm"},
		MachineLabels:   StringMap{"zone": "lab"},
		MachineCapacity: 3, MachineBackend: BackendPodman,
		MachinePlatform: Platform{OS: "Ubuntu", OSVersion: "24.04", Arch: "x86_64"},
		MachineCPUs:     4, MachineMemoryMB: 8192, MachineDiskMB: 40960,
		PoolSelector: StringMap{"tier": "build"},
		MaxMachines:  6, MaxCreatesInFlight: 2,
		IdleTimeout: Duration(20 * time.Minute), CostPerMachineHour: 0.12,
		Enabled: true,
	}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if !HasPrefix(p.ID, PrefixProvider) {
		t.Errorf("provider id %q does not say what it is", p.ID)
	}

	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != "lab" || got.Kind != ProviderFake || !got.InsecureSkipVerify {
		t.Fatalf("the provider came back as %+v", got)
	}
	if got.Settings["storage"] != "local-lvm" || got.PoolSelector["tier"] != "build" {
		t.Errorf("settings = %v, pool selector = %v", got.Settings, got.PoolSelector)
	}
	// Normalised on the way in, like a pool's, so the scheduler never has to
	// think about how an operator spelled an architecture.
	if got.MachinePlatform.Arch != "amd64" || got.MachinePlatform.OS != "ubuntu" {
		t.Errorf("machine platform = %+v, want it normalised", got.MachinePlatform)
	}
	if got.IdleTimeout.Duration() != 20*time.Minute || got.MachineCPUs != 4 || got.MaxMachines != 6 {
		t.Errorf("the machine shape came back as %+v", got)
	}

	// A second provider cannot take the name, because the name is what an
	// operator quotes and what a problem names.
	if err := s.CreateProvider(ctx, &Provider{Kind: ProviderFake, Name: "lab"}); !errors.Is(err, ErrConflict) {
		t.Errorf("two providers called lab = %v, want ErrConflict", err)
	}
}

// A provider nobody has configured buys nothing. Zero is the ceiling a row
// arrives with, and it refuses everything: a provider that started renting the
// moment its credential was accepted would spend money on a connection test.
func TestAProviderWithNoCeilingRefusesEverything(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := &Provider{Kind: ProviderFake, Name: "fresh"}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxMachines != 0 {
		t.Fatalf("max_machines = %d on a provider nobody has configured, want 0", got.MaxMachines)
	}
	if got.MachineBackend != BackendDocker {
		t.Errorf("machine backend = %q, want the default", got.MachineBackend)
	}
}

// The operator's edits and the reconciler's observations are separate writers
// on purpose. A pass that read the row a minute ago must not be able to put a
// ceiling, a credential or a kill switch back to what it was -- silently, with
// nothing to see and nobody told.
func TestAReconcilePassCannotWriteBackAnOperatorsProviderEdit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)

	if err := s.SetProviderCredentials(ctx, p.ID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProviderPaused(ctx, p.ID, true, "the hypervisor is being upgraded"); err != nil {
		t.Fatal(err)
	}
	until := s.Now().Add(5 * time.Minute)
	if err := s.SetProviderBreaker(ctx, p.ID, 3, &until); err != nil {
		t.Fatal(err)
	}

	// An operator editing the form they opened before any of that submits the
	// row as they read it, with no credential and no pause on it.
	stale := *p
	stale.MaxMachines = 9
	if err := s.UpdateProvider(ctx, &stale); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxMachines != 9 {
		t.Errorf("the operator's edit did not land: max_machines = %d", got.MaxMachines)
	}
	if len(got.CredentialsEnc) == 0 {
		t.Error("the edit erased the sealed credential, so the next call would fail with nothing to explain it")
	}
	if !got.Paused || got.PausedReason == "" {
		t.Error("the edit released a pause somebody pressed on purpose")
	}
	if got.ConsecutiveFailures != 3 || got.PausedUntil == nil {
		t.Error("the edit reset the breaker, so a provider that has failed three times would be called again at once")
	}

	// And the observations do not disturb the configuration either.
	if err := s.SetProviderChecked(ctx, p.ID, s.Now(), "the node refused the credential", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProviderSwept(ctx, p.ID, s.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetProvider(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got.MaxMachines != 9 || got.LastCheckError == "" || got.LastSweepAt == nil {
		t.Fatalf("an observation changed the configuration, or did not record itself: %+v", got)
	}
}

// The breaker and the kill switch are separate columns because they are
// separate decisions. A breaker that expires must not release a pause a person
// pressed, and unpausing must not leave a reason behind that says nothing is
// wrong when something is.
func TestTheBreakerAndTheOperatorsPauseAreNotTheSameSwitch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)

	until := s.Now().Add(time.Minute)
	if err := s.SetProviderBreaker(ctx, p.ID, 5, &until); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProviderPaused(ctx, p.ID, true, "waiting for a bigger disk"); err != nil {
		t.Fatal(err)
	}
	// The breaker resets on a success, which says nothing about the pause.
	if err := s.SetProviderBreaker(ctx, p.ID, 0, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Paused {
		t.Fatal("a recovering breaker un-pressed the operator's kill switch")
	}
	if got.PausedUntil != nil || got.ConsecutiveFailures != 0 {
		t.Errorf("the breaker did not reset: until %v, %d failures", got.PausedUntil, got.ConsecutiveFailures)
	}

	if err := s.SetProviderPaused(ctx, p.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetProvider(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got.Paused || got.PausedReason != "" {
		t.Errorf("a resumed provider still carries %q", got.PausedReason)
	}
}

// Deleting a provider row must not be the thing that loses track of a running
// VM. The rows are the only record of what was rented, so the refusal says how
// many are still out there rather than cascading them away.
func TestDeletingAProviderWithLiveMachinesIsRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)

	live := seedMachine(t, s, p.ID, MachineReady)
	if err := s.SetMachineResource(ctx, live.ID, "pve-1", "143", "fp", "ctl_a"); err != nil {
		t.Fatal(err)
	}
	// A failed machine whose VM was created is exactly as much of a reason to
	// refuse: its resource costs money whatever this controller thinks of it.
	failed := seedMachine(t, s, p.ID, MachineFailed)
	if err := s.SetMachineResource(ctx, failed.ID, "pve-1", "144", "fp", "ctl_a"); err != nil {
		t.Fatal(err)
	}

	err := s.DeleteProvider(ctx, p.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProvider = %v, want ErrConflict", err)
	}
	if _, err := s.GetProvider(ctx, p.ID); err != nil {
		t.Fatalf("the refused delete still removed the provider: %v", err)
	}

	// Once every resource is confirmed gone, the provider goes, and the
	// history of the machines that belonged to it goes with it.
	for _, m := range []*Machine{live, failed} {
		if _, err := s.ConfirmMachineDeleted(ctx, m.ID, s.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteProvider(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProvider once nothing is left: %v", err)
	}
	if _, err := s.GetProvider(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProvider after the delete = %v, want ErrNotFound", err)
	}
	if _, err := s.GetMachine(ctx, live.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a machine of a deleted provider survived it, pointing at nothing: %v", err)
	}
}

// The question this answers is asked once, at startup, before deciding whether
// a new encryption key may be generated. A provider's credential is opened by
// that key, so a controller that generated a fresh one would start, show its
// hypervisor as configured, and fail inside the first call that rented a
// machine.
func TestHasSealedSecretsSeesAProviderCredential(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p := seedProvider(t, s)
	has, err := s.HasSealedSecrets(ctx)
	if err != nil {
		t.Fatalf("HasSealedSecrets: %v", err)
	}
	if has {
		t.Fatal("a provider with no credential yet counts as a sealed secret, so a first run would refuse to generate a key")
	}

	if err := s.SetProviderCredentials(ctx, p.ID, []byte("sealed")); err != nil {
		t.Fatalf("SetProviderCredentials: %v", err)
	}
	has, err = s.HasSealedSecrets(ctx)
	if err != nil {
		t.Fatalf("HasSealedSecrets: %v", err)
	}
	if !has {
		t.Fatal("a provider credential does not count as a sealed secret, so a lost key would be replaced and the provider would stop working")
	}
}

// Providers are read by every pass, so the listing is unpaginated and ordered
// by the name an operator gave -- the one thing they can predict.
func TestProvidersAreListedByName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		if err := s.CreateProvider(ctx, &Provider{Kind: ProviderFake, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	want := []string{"alpha", "middle", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("ListProviders returned %d providers, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.Name != want[i] {
			t.Fatalf("providers came back as %s..., want %v", p.Name, want)
		}
	}
}

// The private connection address is a lasting capability to open connections
// to a hypervisor's API, so it lives beside the credential: sealed, cleared
// only on purpose, and never carried by a form that did not mention it.
func TestAProvidersPrivateConnectionSurvivesAnEditAndIsClearedOnPurpose(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedProvider(t, s)

	if err := s.SetProviderTailcatAddress(ctx, p.ID, []byte("sealed-address")); err != nil {
		t.Fatalf("SetProviderTailcatAddress: %v", err)
	}
	stale := *p
	stale.MaxMachines = 4
	if err := s.UpdateProvider(ctx, &stale); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	got, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.TailcatAddressEnc) != "sealed-address" {
		t.Fatalf("an edit that never mentioned the connection changed it: %q", got.TailcatAddressEnc)
	}

	if err := s.SetProviderTailcatAddress(ctx, p.ID, nil); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	got, err = s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TailcatAddressEnc) != 0 {
		t.Fatal("clearing the private connection left it in place")
	}
	if err := s.SetProviderTailcatAddress(ctx, "prv_missing", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing provider returned %v, want ErrNotFound", err)
	}
}
