package store

import (
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
