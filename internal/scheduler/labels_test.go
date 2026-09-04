package scheduler

import (
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestMatches(t *testing.T) {
	tests := []struct {
		name string
		pool []string
		job  []string
		want bool
	}{
		{"exact match", []string{"gpu"}, []string{"gpu"}, true},
		{"pool is a superset", []string{"gpu", "cuda12"}, []string{"gpu"}, true},
		{"job label missing from pool", []string{"gpu"}, []string{"gpu", "cuda12"}, false},
		{"job asks only for implicit labels", []string{"gpu"},
			[]string{"self-hosted", "linux", "x64"}, true},
		{"implicit-only job and label-less pool", nil, []string{"self-hosted", "linux", "x64"}, true},
		{"label-less pool cannot serve a real label", nil, []string{"gpu"}, false},
		{"empty job labels match anything", []string{"gpu"}, nil, true},
		{"pool declares the os the job asked for",
			[]string{"linux", "gpu"}, []string{"self-hosted", "linux", "gpu"}, true},
		{"os contradiction",
			[]string{"windows", "gpu"}, []string{"self-hosted", "linux", "gpu"}, false},
		{"arch contradiction",
			[]string{"linux", "arm64", "gpu"}, []string{"self-hosted", "linux", "x64", "gpu"}, false},
		{"pool declares no arch, so any arch matches",
			[]string{"linux", "gpu"}, []string{"self-hosted", "arm64", "gpu"}, true},
		{"pool declares an arch the job did not ask for",
			[]string{"linux", "arm64", "gpu"}, []string{"self-hosted", "linux", "gpu"}, true},
		{"case and whitespace are normalised",
			[]string{" GPU ", "Cuda12"}, []string{"gpu", " cuda12"}, true},
		{"self-hosted alone never contradicts",
			[]string{"linux", "x64"}, []string{"self-hosted"}, true},
		{"duplicate labels do not change the answer",
			[]string{"gpu", "gpu"}, []string{"gpu", "GPU"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.pool, tc.job); got != tc.want {
				t.Fatalf("Matches(%v, %v) = %v, want %v", tc.pool, tc.job, got, tc.want)
			}
			score := Score(tc.pool, tc.job)
			if (score >= 0) != tc.want {
				t.Fatalf("Score(%v, %v) = %d, which disagrees with Matches", tc.pool, tc.job, score)
			}
		})
	}
}

func TestScorePrefersTheLeastSurplus(t *testing.T) {
	job := []string{"self-hosted", "linux", "gpu"}
	exact := Score([]string{"gpu"}, job)
	oneSurplus := Score([]string{"gpu", "cuda12"}, job)
	twoSurplus := Score([]string{"gpu", "cuda12", "bigmem"}, job)

	if !(exact > oneSurplus && oneSurplus > twoSurplus) {
		t.Fatalf("scores are not ordered by surplus: exact=%d one=%d two=%d",
			exact, oneSurplus, twoSurplus)
	}
	if twoSurplus < 0 {
		t.Fatalf("a matching pool scored negative: %d", twoSurplus)
	}
	// A label the job did ask for is not surplus, even an implicit one.
	if got := Score([]string{"linux", "gpu"}, job); got != exact {
		t.Fatalf("Score with a requested implicit label = %d, want %d", got, exact)
	}
	if got := Score([]string{"windows"}, job); got >= 0 {
		t.Fatalf("Score for a contradicting pool = %d, want negative", got)
	}
}

func TestBestPool(t *testing.T) {
	general := &store.Pool{ID: "p1", Name: "general", Labels: store.StringSlice{"linux", "x64"}, Enabled: true}
	gpu := &store.Pool{ID: "p2", Name: "gpu", Labels: store.StringSlice{"linux", "x64", "gpu"}, Enabled: true}
	bigGPU := &store.Pool{ID: "p3", Name: "gpu-big", Labels: store.StringSlice{"linux", "x64", "gpu", "bigmem"}, Enabled: true}
	disabled := &store.Pool{ID: "p4", Name: "arm", Labels: store.StringSlice{"gpu"}, Enabled: false}
	pools := []*store.Pool{bigGPU, disabled, gpu, general, nil}

	tests := []struct {
		name string
		job  []string
		want string
	}{
		{"implicit-only job takes the least specific pool", []string{"self-hosted", "linux", "x64"}, "general"},
		{"gpu job takes the smallest pool that has a gpu", []string{"self-hosted", "gpu"}, "gpu"},
		{"bigmem job needs the big pool", []string{"gpu", "bigmem"}, "gpu-big"},
		{"nothing claims an unknown label", []string{"windows-2022"}, ""},
		{"a disabled pool is never chosen", []string{"gpu", "arm64"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BestPool(pools, tc.job)
			switch {
			case got == nil && tc.want != "":
				t.Fatalf("BestPool(%v) = nil, want %s", tc.job, tc.want)
			case got != nil && got.Name != tc.want:
				t.Fatalf("BestPool(%v) = %s, want %q", tc.job, got.Name, tc.want)
			}
		})
	}
}

func TestBestPoolTieBreaksOnName(t *testing.T) {
	// Two pools that fit the job equally well: the name decides, whatever order
	// the caller happened to pass them in.
	beta := &store.Pool{ID: "p-beta", Name: "beta", Labels: store.StringSlice{"gpu"}, Enabled: true}
	alpha := &store.Pool{ID: "p-alpha", Name: "alpha", Labels: store.StringSlice{"gpu"}, Enabled: true}

	for _, pools := range [][]*store.Pool{{beta, alpha}, {alpha, beta}} {
		got := BestPool(pools, []string{"gpu"})
		if got == nil || got.Name != "alpha" {
			t.Fatalf("BestPool = %v, want alpha", got)
		}
	}

	// Same name (only possible mid-rename) falls back to the ID.
	a := &store.Pool{ID: "p-a", Name: "same", Labels: store.StringSlice{"gpu"}, Enabled: true}
	b := &store.Pool{ID: "p-b", Name: "same", Labels: store.StringSlice{"gpu"}, Enabled: true}
	if got := BestPool([]*store.Pool{b, a}, []string{"gpu"}); got != a {
		t.Fatalf("BestPool = %v, want the lower ID", got)
	}
}
