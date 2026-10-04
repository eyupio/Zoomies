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
