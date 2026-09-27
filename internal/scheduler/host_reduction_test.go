package scheduler

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// A host that runs a pool smaller than a larger one would is said once, as
// information, and only then: a host that gives the pool its full size, one
// that cannot run it at all and a pool that set no minimum to shrink to all
// say nothing, because a note on every host is a note nobody reads.
func TestAHostSaysItRunsAPoolSmallerOnlyWhenItDoes(t *testing.T) {
	big := sized("big", 2, 16, hostFor(32*1024), 100000)
	thinCPU := sized("thin-cpu", 2, 3, hostFor(32*1024), 100000)
	small := sized("small", 1, 4, hostFor(2867), 100000)

	dind := func(p *store.Pool) *store.Pool { p.DockerMode = store.DockerDinD; return p }
	fixed := func(cpus, minCPUs float64) *store.Pool {
		p := limited("fixed", cpus, 1024)
		p.Resources.MinCPUs = minCPUs
		return p
	}

	cases := []struct {
		name string
		host *store.Host
		pool *store.Pool
		want []string // empty: no sentence
	}{
		{"no host", nil, automatic("auto", 0, 1024), nil},
		{"no pool", big, nil, nil},
		{"a host that cannot run it", small, dind(automatic("plain", 0, 0)), nil},
		{"an automatic pool at its full share", big, dind(automatic("auto", 0.5, 1024)), nil},
		{"an automatic pair short of CPU", thinCPU, dind(automatic("auto", 0.5, 1024)),
			[]string{"CPU, split between the runner and its Docker daemon", "above this pool's minimum"}},
		{"an automatic pair short of memory", small, dind(automatic("auto", 0, 1024)),
			[]string{"of memory, split between the runner and its Docker daemon"}},
		{"a fixed pool whose standard fits", big, fixed(4, 1), nil},
		{"a fixed pool with no minimum", thinCPU, fixed(8, 0), nil},
		{"a fixed pool cut to its minimum", thinCPU, fixed(8, 1),
			[]string{"smaller than this pool's standard size", "never less than this pool's minimum"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HostReduction(tc.host, tc.pool)
			if len(tc.want) == 0 {
				if got != "" {
					t.Errorf("HostReduction = %q, want nothing", got)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("HostReduction = %q, want it to contain %q", got, w)
				}
			}
		})
	}
}

// A CPU minimum the operator typed is theirs to lower, so a machine too small
// for it names it as what the runner is charged -- and, for a pair, that it is
// needed twice over. A share under the minimum alone is not a shortfall: the
// runner is given the minimum (TestAMinimumAboveTheShareIsGivenRatherThanRefused).
func TestAMinimumTheWholeMachineCannotCoverNamesIt(t *testing.T) {
	h := sized("thin-cpu", 2, 3, hostFor(32*1024), 100000)
	p := automatic("auto", 2, 0)
	p.DockerMode = store.DockerDinD
	got := HostShortfall(h, p)
	for _, w := range []string{"CPU to place on", "this pool's minimum of 2 CPU a container", "twice over for a runner and its Docker daemon"} {
		if !strings.Contains(got, w) {
			t.Errorf("shortfall = %q, want it to contain %q", got, w)
		}
	}
}
