package store

import (
	"context"
	"strings"
	"testing"
)

func TestTmpfsSizesFitAutomaticMountsToTheMemoryLimit(t *testing.T) {
	cases := []struct {
		name              string
		cfg               TmpfsConfig
		capMB             int64
		wantWork, wantTmp int64
	}{
		{"nothing in memory", TmpfsConfig{}, 16384, 0, 0},
		{"no limit takes the defaults", TmpfsConfig{Work: TmpfsMount{Enabled: true}, Tmp: TmpfsMount{Enabled: true}}, 0, DefaultTmpfsWorkMB, DefaultTmpfsTmpMB},
		{"a roomy limit takes the defaults", TmpfsConfig{Work: TmpfsMount{Enabled: true}}, 32768, DefaultTmpfsWorkMB, 0},
		{"a small limit shrinks the work folder to half of it", TmpfsConfig{Work: TmpfsMount{Enabled: true}}, 4096, 2048, 0},
		{"two automatic mounts share half the limit in proportion", TmpfsConfig{Work: TmpfsMount{Enabled: true}, Tmp: TmpfsMount{Enabled: true}}, 5120, 2048, 512},
		{"a typed size is never shrunk", TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 3000}}, 4096, 3000, 0},
		{"automatic mounts fit what a typed one leaves", TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 1000}, Tmp: TmpfsMount{Enabled: true}}, 4096, 1000, 1024},
		{"an absurdly small limit still gets a usable mount", TmpfsConfig{Work: TmpfsMount{Enabled: true}}, 100, MinTmpfsMB, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, tmp := tc.cfg.Sizes(tc.capMB)
			if work != tc.wantWork || tmp != tc.wantTmp {
				t.Fatalf("Sizes(%d) = %d, %d; want %d, %d", tc.capMB, work, tmp, tc.wantWork, tc.wantTmp)
			}
		})
	}
}

// The proposal is cap plus what the mounts may fill, because the cap was sized
// for the job and a tmpfs spends it. Nothing is proposed where there is nothing
// to raise: no limit, or nothing in memory.
func TestTmpfsProposesRaisingTheLimitByWhatItMayFill(t *testing.T) {
	cfg := TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 6000}, Tmp: TmpfsMount{Enabled: true}}
	if got, want := cfg.ReserveMB(), 6000+DefaultTmpfsTmpMB; got != want {
		t.Fatalf("ReserveMB = %d, want %d", got, want)
	}
	if got, want := cfg.RecommendedMemoryMB(8192), int64(8192+6000)+DefaultTmpfsTmpMB; got != want {
		t.Fatalf("RecommendedMemoryMB = %d, want %d", got, want)
	}
	if got := cfg.RecommendedMemoryMB(0); got != 0 {
		t.Fatalf("a pool with no limit is proposed %d, want nothing", got)
	}
	if got := (TmpfsConfig{}).RecommendedMemoryMB(8192); got != 0 {
		t.Fatalf("a pool with nothing in memory is proposed %d, want nothing", got)
	}
}

func TestTmpfsValidateRefusesWhatCannotWork(t *testing.T) {
	cases := []struct {
		name      string
		cfg       TmpfsConfig
		capMB     int64
		wantField string // empty means valid
		want      string
	}{
		{"off is always valid", TmpfsConfig{}, 1024, "", ""},
		{"automatic sizes are valid on any limit", TmpfsConfig{Work: TmpfsMount{Enabled: true}}, 1024, "", ""},
		{"a disabled mount's size is ignored", TmpfsConfig{Work: TmpfsMount{SizeMB: 1}}, 1024, "", ""},
		{"below the floor", TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 8}}, 0, "tmpfs.work.size_mb", "at least 64 MB"},
		{"negative", TmpfsConfig{Tmp: TmpfsMount{Enabled: true, SizeMB: -1}}, 0, "tmpfs.tmp.size_mb", "negative"},
		{"the whole limit", TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 4096}}, 4096, "tmpfs.work.size_mb", "to at least 8192 MB"},
		{"together they take the limit", TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 3000}, Tmp: TmpfsMount{Enabled: true, SizeMB: 1200}}, 4096, "tmpfs.tmp.size_mb", "total 4200 MB"},
		{"no limit to exceed", TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 999999}}, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			field, problem := tc.cfg.Validate(tc.capMB)
			if field != tc.wantField {
				t.Fatalf("Validate blamed %q, want %q (%s)", field, tc.wantField, problem)
			}
			if tc.want == "" && problem != "" {
				t.Fatalf("Validate = %q, want valid", problem)
			}
			if tc.want != "" && !strings.Contains(problem, tc.want) {
				t.Fatalf("Validate = %q, want it to contain %q", problem, tc.want)
			}
		})
	}
}

// The image store is charged to the daemon's container, which has a memory
// limit of its own, so it is sized from that limit and not the runner's.
func TestTheImageStoreIsFittedToTheDaemonsMemoryLimit(t *testing.T) {
	on := TmpfsConfig{Daemon: TmpfsMount{Enabled: true}}
	cases := []struct {
		name  string
		cfg   TmpfsConfig
		capMB int64
		want  int64
	}{
		{"off", TmpfsConfig{}, 16384, 0},
		{"no limit takes the default", on, 0, DefaultTmpfsDaemonMB},
		{"a roomy limit takes the default", on, 65536, DefaultTmpfsDaemonMB},
		{"a small limit shrinks it to half", on, 6000, 3000},
		{"a typed size is never shrunk", TmpfsConfig{Daemon: TmpfsMount{Enabled: true, SizeMB: 5000}}, 6000, 5000},
		{"an absurdly small limit still gets a usable store", on, 100, MinTmpfsMB},
	}
	for _, tc := range cases {
		if got := tc.cfg.DaemonSize(tc.capMB); got != tc.want {
			t.Errorf("%s: DaemonSize(%d) = %d, want %d", tc.name, tc.capMB, got, tc.want)
		}
	}
}

// A typed limit is given to the runner and to the daemon alike, and each is
// charged for its own folders, so the proposed limit covers whichever needs more
// -- not both, which would pay for room neither container uses.
func TestTheProposedLimitCoversTheContainerThatNeedsMore(t *testing.T) {
	cfg := TmpfsConfig{
		Work:   TmpfsMount{Enabled: true, SizeMB: 2000},
		Daemon: TmpfsMount{Enabled: true, SizeMB: 6000},
	}
	if got, want := cfg.RecommendedMemoryMB(8192), int64(8192+6000); got != want {
		t.Fatalf("RecommendedMemoryMB = %d, want the daemon's 6000 added once, %d", got, want)
	}
	cfg.Daemon.SizeMB = 1000
	if got, want := cfg.RecommendedMemoryMB(8192), int64(8192+2000); got != want {
		t.Fatalf("RecommendedMemoryMB = %d, want the runner's 2000 added once, %d", got, want)
	}
	if got := (TmpfsConfig{Daemon: TmpfsMount{Enabled: true}}).DaemonReserveMB(); got != DefaultTmpfsDaemonMB {
		t.Fatalf("DaemonReserveMB = %d, want the default %d", got, DefaultTmpfsDaemonMB)
	}
	if !(TmpfsConfig{Daemon: TmpfsMount{Enabled: true}}).Any() {
		t.Fatal("a pool keeping only its image store in memory reads as keeping nothing")
	}
}

// The image store is held to its own container's limit: it does not add up with
// the runner's folders, which are charged to another cgroup.
func TestTheImageStoreIsValidatedAgainstTheDaemonsOwnLimit(t *testing.T) {
	both := TmpfsConfig{
		Work:   TmpfsMount{Enabled: true, SizeMB: 3000},
		Daemon: TmpfsMount{Enabled: true, SizeMB: 3000},
	}
	if field, problem := both.Validate(4096); field != "" {
		t.Fatalf("each fits its own 4096 MB limit, but %s was refused: %s", field, problem)
	}
	whole := TmpfsConfig{Daemon: TmpfsMount{Enabled: true, SizeMB: 4096}}
	field, problem := whole.Validate(4096)
	if field != "tmpfs.daemon.size_mb" || !strings.Contains(problem, "to at least 8192 MB") {
		t.Fatalf("Validate = %q, %q; want the image store named with the limit that fits it", field, problem)
	}
	if field, _ := (TmpfsConfig{Daemon: TmpfsMount{Enabled: true, SizeMB: 8}}).Validate(0); field != "tmpfs.daemon.size_mb" {
		t.Fatalf("a store below the floor blamed %q", field)
	}
}

// A proposal that is taken must end the warning, or the next one follows it
// upward. Folders are fitted into half a limit, so the proposed limit is at
// least twice what they may take -- which is more than the limit plus them when
// the limit is smaller than they are.
func TestAProposedLimitEndsTheWarningWhenItIsTaken(t *testing.T) {
	for _, cfg := range []TmpfsConfig{
		{Work: TmpfsMount{Enabled: true}},
		{Daemon: TmpfsMount{Enabled: true}},
		{Work: TmpfsMount{Enabled: true}, Tmp: TmpfsMount{Enabled: true}, Daemon: TmpfsMount{Enabled: true}},
	} {
		for _, limit := range []int64{1024, 2048, 6144, 8192} {
			rec := cfg.RecommendedMemoryMB(limit)
			work, tmp := cfg.Sizes(rec)
			if work+tmp < cfg.ReserveMB() || cfg.DaemonSize(rec) < cfg.DaemonReserveMB() {
				t.Errorf("%+v: at the proposed %d MB (from %d) the folders are still fitted down: %d+%d, store %d",
					cfg, rec, limit, work, tmp, cfg.DaemonSize(rec))
			}
		}
	}
	// And where the sum is already enough, it is the sum.
	cfg := TmpfsConfig{Work: TmpfsMount{Enabled: true}}
	if got, want := cfg.RecommendedMemoryMB(6144), int64(6144+4096); got != want {
		t.Errorf("RecommendedMemoryMB(6144) = %d, want %d", got, want)
	}
}

// A host's policy is its owner's last word: off means none, a ceiling lowers a
// folder and never raises it, and a host that says nothing changes nothing.
func TestAHostsPolicyLowersAFolderAndNeverRaisesIt(t *testing.T) {
	pool := TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 4096}, Daemon: TmpfsMount{Enabled: true}}

	if got, max := (HostTmpfs{}).Apply(pool); got != pool || max != 0 {
		t.Errorf("a silent host changed the pool's setting: %+v max %d", got, max)
	}
	if got, max := (HostTmpfs{Disabled: true, MaxMB: 1024}).Apply(pool); got.Any() || max != 0 {
		t.Errorf("a disabled host kept %+v with a ceiling of %d", got, max)
	}
	if got, max := (HostTmpfs{MaxMB: 2048}).Apply(pool); got != pool || max != 2048 {
		t.Errorf("a ceiling must leave the setting as it was and hand the agent the number: %+v max %d", got, max)
	}

	for _, tc := range []struct{ size, max, want int64 }{
		{4096, 0, 4096},    // no ceiling
		{4096, 2048, 2048}, // lowered
		{1024, 2048, 1024}, // never raised
		{0, 2048, 0},       // a folder that is off stays off
	} {
		if got := Cap(tc.size, tc.max); got != tc.want {
			t.Errorf("Cap(%d, %d) = %d, want %d", tc.size, tc.max, got, tc.want)
		}
	}
}

// The policy is part of the runner profile, so it must survive being stored and
// read back with the figures beside it, and a profile that says only this is
// still a profile an operator set.
func TestAHostsInMemoryPolicyIsStoredWithItsRunnerProfile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	h := &Host{Name: "builder", Capacity: 2, Backends: StringSlice{"docker"}, Labels: StringMap{}, OS: "linux", Arch: "amd64"}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	profile := RunnerProfile{
		Standard: RunnerStandard{CPUs: 2, MemoryMB: 8192},
		Tmpfs:    HostTmpfs{Disabled: true},
	}
	if !(RunnerProfile{Tmpfs: HostTmpfs{MaxMB: 512}}).Set() {
		t.Fatal("a profile that only caps the folders reads as one nobody set")
	}
	if err := s.PatchHost(ctx, h.ID, HostChanges{RunnerProfile: &profile}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunnerProfile != profile {
		t.Fatalf("profile = %+v, want %+v", got.RunnerProfile, profile)
	}
}

// A typed size is checked against the pool's stored limit, but the runner may be
// given less (a host's share, or a reduced fixed size); folders that reach the
// real limit would OOM-kill the runner as they filled.
func TestTypedTmpfsSizesAreScaledIntoARunnerLimitSmallerThanTheyAssume(t *testing.T) {
	c := TmpfsConfig{Work: TmpfsMount{Enabled: true, SizeMB: 4096}, Tmp: TmpfsMount{Enabled: true, SizeMB: 1024}}
	work, tmp := c.Sizes(1024)
	if work+tmp > 512 || work <= tmp {
		t.Fatalf("sizes %d and %d should fit in half of 1024 MB, work larger", work, tmp)
	}
	if work, tmp := c.Sizes(16384); work != 4096 || tmp != 1024 {
		t.Fatalf("a roomy limit must leave typed sizes alone, got %d and %d", work, tmp)
	}
	d := TmpfsConfig{Daemon: TmpfsMount{Enabled: true, SizeMB: 8192}}
	if got := d.DaemonSize(2048); got != 1024 {
		t.Fatalf("daemon size = %d, want 1024", got)
	}
}

func TestADaemonShareDividesASlotUnevenlyAndEvenByDefault(t *testing.T) {
	slot := Resources{CPUs: 10, MemoryMB: 10000}
	r, d := slot.SplitWithDaemon()
	if r.CPUs != 5 || d.CPUs != 5 || r.MemoryMB != 5000 || d.MemoryMB != 5000 {
		t.Fatalf("default split = %+v / %+v, want even", r, d)
	}
	r, d = slot.SplitWithDaemonShare(70)
	if d.CPUs != 7 || r.CPUs != 3 || d.MemoryMB != 7000 || r.MemoryMB != 3000 {
		t.Fatalf("70%% split = %+v / %+v", r, d)
	}
	if r2, d2 := slot.SplitWithDaemonShare(5); r2 != r0(slot) || d2 != d0(slot) {
		t.Fatalf("a share out of range must fall back to even, got %+v / %+v", r2, d2)
	}
	if got := (Resources{DaemonSharePercent: 80}).PairFactor(); got != 5 {
		t.Fatalf("PairFactor(80) = %v, want 5", got)
	}
	if got := (Resources{}).PairFactor(); got != 2 {
		t.Fatalf("PairFactor() = %v, want 2", got)
	}
}

func r0(s Resources) Resources { r, _ := s.SplitWithDaemon(); return r }
func d0(s Resources) Resources { _, d := s.SplitWithDaemon(); return d }

// An automatic folder is in memory where the runner has room for it to be
// worth having, and on disk where it has not; the one furthest below its floor
// goes first, because giving it up leaves the other more.
func TestAnAutomaticFolderGoesToDiskWhereTheRunnerIsTooSmallForIt(t *testing.T) {
	auto := TmpfsConfig{Work: TmpfsMount{Enabled: true, Auto: true}, Tmp: TmpfsMount{Enabled: true, Auto: true}}
	for _, tc := range []struct {
		name          string
		cfg           TmpfsConfig
		capMB         int64
		host          HostTmpfs
		work, tmp     int64
		wantWorkAtMin bool
	}{
		{"a 2 GB runner has no room for either", auto, 2048, HostTmpfs{}, 0, 0, false},
		{"a roomy runner takes both at their defaults", auto, 32768, HostTmpfs{}, 4096, 1024, false},
		{"/tmp gives way first and the work folder takes the room", auto, 5120, HostTmpfs{}, 2560, 0, true},
		{"a host's bigger standard is asked for, and fitted", TmpfsConfig{Work: TmpfsMount{Enabled: true, Auto: true}}, 65536, HostTmpfs{WorkMB: 16384}, 16384, 0, false},
		{"a host ceiling under the floor puts the folder on disk", TmpfsConfig{Work: TmpfsMount{Enabled: true, Auto: true}}, 65536, HostTmpfs{MaxMB: 1024}, 0, 0, false},
		{"a folder that is not automatic is never dropped", TmpfsConfig{Work: TmpfsMount{Enabled: true}, Tmp: TmpfsMount{Enabled: true}}, 2048, HostTmpfs{}, 819, 204, false},
		{"a typed size lowers the floor to itself", TmpfsConfig{Work: TmpfsMount{Enabled: true, Auto: true, SizeMB: 1024}}, 4096, HostTmpfs{}, 1024, 0, false},
	} {
		work, tmp := tc.cfg.PlaceRunner(tc.capMB, tc.host)
		if work != tc.work || tmp != tc.tmp {
			t.Errorf("%s: placed %d and %d MB, want %d and %d", tc.name, work, tmp, tc.work, tc.tmp)
		}
	}

	d := TmpfsConfig{Daemon: TmpfsMount{Enabled: true, Auto: true}}
	if got := d.PlaceDaemon(3072, HostTmpfs{}); got != 0 {
		t.Errorf("a 3 GB daemon was given %d MB of image store, want disk", got)
	}
	if got := d.PlaceDaemon(16384, HostTmpfs{}); got != 8192 {
		t.Errorf("a 16 GB daemon was given %d MB of image store, want 8192", got)
	}
	manual := TmpfsConfig{Daemon: TmpfsMount{Enabled: true}}
	if got := manual.PlaceDaemon(3072, HostTmpfs{}); got != 1536 {
		t.Errorf("a manual store on a 3 GB daemon was given %d MB, want 1536", got)
	}
}
