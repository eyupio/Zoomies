package store

import (
	"context"
	"testing"
)

// An existing pool must come out of an upgrade with the valve off: the empty
// policy is what its row holds, and neither mode may read true for it.
func TestAPoolWithNoMemoryPolicyIsNotLentMemory(t *testing.T) {
	var p MemoryBurstPolicy
	if p.Observes() || p.Enforces() {
		t.Fatalf("an empty policy observes or enforces: %+v", p)
	}
	for _, tc := range []struct {
		mode              MemoryBurstMode
		valid, observes   bool
		enforces          bool
		describeTheChoice string
	}{
		{"", true, false, false, "unset is off"},
		{MemoryBurstOff, true, false, false, "off"},
		{MemoryBurstObserve, true, true, false, "observe records without moving a limit"},
		{MemoryBurstAutomatic, true, true, true, "automatic moves limits"},
		{"manual", false, false, false, "anything else is refused"},
	} {
		p := MemoryBurstPolicy{Mode: tc.mode}
		if tc.mode.Valid() != tc.valid || p.Observes() != tc.observes || p.Enforces() != tc.enforces {
			t.Errorf("%s: mode %q: valid=%v observes=%v enforces=%v", tc.describeTheChoice, tc.mode,
				tc.mode.Valid(), p.Observes(), p.Enforces())
		}
	}
}

func TestAPoolKeepsItsMemoryPolicyThroughCreateAndUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)

	got, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryBurst != (MemoryBurstPolicy{}) {
		t.Fatalf("a pool made without a memory policy has one: %+v", got.MemoryBurst)
	}

	pool.MemoryBurst = MemoryBurstPolicy{Mode: MemoryBurstAutomatic, MaxMemoryMB: 12288, SpillMB: 4096}
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	got, err = s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryBurst != pool.MemoryBurst {
		t.Fatalf("memory policy = %+v, want %+v", got.MemoryBurst, pool.MemoryBurst)
	}

	// An import creates and updates through ApplyPools, which must be the same
	// row as a pool made on its own.
	copyOf := *pool
	copyOf.ID, copyOf.Name = "", "zoomies-lent-copy"
	copyOf.MemoryBurst = MemoryBurstPolicy{Mode: MemoryBurstObserve}
	if err := s.ApplyPools(ctx, []*Pool{&copyOf}, nil); err != nil {
		t.Fatalf("ApplyPools: %v", err)
	}
	made, err := s.GetPool(ctx, copyOf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if made.MemoryBurst.Mode != MemoryBurstObserve {
		t.Fatalf("an imported pool lost its memory policy: %+v", made.MemoryBurst)
	}
}

// The loan is the agent's to report. A whole-row update from a read taken
// before the report must not put it back to zero, because the placement ledger
// would then hand the runner's lent memory to the next runner as room.
func TestARunnersLentMemorySurvivesAWholeRowUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "lent", State: RunnerBusy, AllocatedMemoryMB: 4096}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunnerLentMemory(ctx, r.ID, 512); err != nil {
		t.Fatal(err)
	}
	stale, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.LentMemoryMB != 512 {
		t.Fatalf("lent memory = %d, want 512", stale.LentMemoryMB)
	}
	if err := s.SetRunnerLentMemory(ctx, r.ID, 1536); err != nil {
		t.Fatal(err)
	}
	stale.Message = "written from an older read"
	if err := s.UpdateRunner(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LentMemoryMB != 1536 {
		t.Fatalf("lent memory after a stale whole-row update = %d, want 1536", got.LentMemoryMB)
	}

	if err := s.SetRunnerLentMemory(ctx, r.ID, -5); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetRunner(ctx, r.ID); got.LentMemoryMB != 0 {
		t.Fatalf("a negative figure was stored as %d, want 0", got.LentMemoryMB)
	}
}

func TestAHostsRunnerProfileCarriesAMemoryCeiling(t *testing.T) {
	std := RunnerStandard{BurstMaxMemoryMB: 8192}
	if std.Sized() {
		t.Fatal("a ceiling alone names no size, so it must not make the slot count the profile's")
	}
	profile := RunnerProfile{Standard: std}
	if !profile.Set() {
		t.Fatal("a profile with only a memory ceiling reads as unset")
	}
}

// A runner's in-memory folders are recorded once, at the moment its create task
// is built, and read back as they were: a pool edited since must not rewrite
// what a running job was started with.
func TestARunnersInMemoryFoldersAreRecordedOnceAndReadBack(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "scratchy", State: RunnerProvisioning}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil || got.Scratch.Any() {
		t.Fatalf("a runner with nothing recorded has %+v, %v", got.Scratch, err)
	}

	want := RunnerScratch{Folders: []ScratchFolder{
		{Kind: ScratchWork, AskedMB: 4096, SizeMB: 2048, Auto: true},
		{Kind: ScratchTmp, AskedMB: 1024, Auto: true, Why: ScratchAutoTooSmall},
		{Kind: ScratchDaemon, AskedMB: 8192, SizeMB: 4096},
	}}
	if err := s.SetRunnerScratch(ctx, r.ID, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scratch.Folders) != 3 || got.Scratch.Folders[0] != want.Folders[0] || got.Scratch.Folders[1] != want.Folders[1] || got.Scratch.Folders[2] != want.Folders[2] {
		t.Fatalf("scratch = %+v, want %+v", got.Scratch, want)
	}
	if got.Scratch.InMemory() != 2 || got.Scratch.Folders[1].InMemory() {
		t.Fatalf("%d folders in memory, want the work folder and the image store, and not /tmp", got.Scratch.InMemory())
	}

	// A whole-row update from an older read does not rewrite it.
	got.Message = "written from a read taken before"
	got.Scratch = RunnerScratch{}
	if err := s.UpdateRunner(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetRunner(ctx, r.ID)
	if err != nil || len(again.Scratch.Folders) != 3 {
		t.Fatalf("scratch after a whole-row update = %+v, %v; want it kept", again.Scratch, err)
	}
}
