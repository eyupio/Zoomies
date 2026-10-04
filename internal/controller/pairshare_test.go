package controller

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

const gib = int64(1 << 30)

// pairWindowOf is n samples from runners runners of a slot divided evenly
// (2 cores and 4 GiB each side) in which the two halves use what is given.
func pairWindowOf(n, runners int, runnerUse, daemonUse backend.HalfUse) []pairSample {
	out := make([]pairSample, 0, n)
	for i := 0; i < n; i++ {
		r, d := runnerUse, daemonUse
		r.CPULimit, r.MemoryLimit = 2, 4*gib
		d.CPULimit, d.MemoryLimit = 2, 4*gib
		out = append(out, pairSample{runner: fmt.Sprintf("r%d", i%runners), halves: backend.PairHalves{Runner: r, Daemon: d}})
	}
	return out
}

// A build that runs flat out in the daemon while the runner idles is the case
// the sums hide, and the proposal has to move the slot toward the daemon.
func TestASidecarSqueezedWhileTheRunnerIdlesIsOfferedALargerShare(t *testing.T) {
	w := pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2})
	adv, ok := judgePair(w)
	if !ok || !adv.DaemonHot {
		t.Fatalf("advice = %+v, ok = %v; want a squeezed daemon", adv, ok)
	}
	if adv.Current != 50 || adv.Proposed < 70 || adv.Proposed > 90 || adv.Proposed%5 != 0 {
		t.Errorf("proposed %d from %d; want a multiple of five between 70 and 90", adv.Proposed, adv.Current)
	}
}

func TestARunnerSqueezedWhileTheSidecarIdlesIsOfferedASmallerShare(t *testing.T) {
	w := pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2},
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4})
	adv, ok := judgePair(w)
	if !ok || adv.DaemonHot || adv.Proposed > 30 || adv.Proposed < store.MinDaemonSharePercent {
		t.Fatalf("advice = %+v, ok = %v; want a smaller share for the sidecar", adv, ok)
	}
}

// Advice rests on several runners and enough samples, and on a squeeze that is
// one-sided: both halves busy is a pool that needs a bigger slot, which no
// division fixes.
func TestNoShareAdviceWithoutEvidenceOrWhenBothHalvesAreBusy(t *testing.T) {
	hot := backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2}
	idle := backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4}
	for name, w := range map[string][]pairSample{
		"too few samples":      pairWindowOf(10, 5, idle, hot),
		"one runner only":      pairWindowOf(100, 1, idle, hot),
		"both halves busy":     pairWindowOf(100, 5, hot, hot),
		"both halves idle":     pairWindowOf(100, 5, idle, idle),
		"an empty window":      nil,
		"daemon moderately in": pairWindowOf(100, 5, idle, backend.HalfUse{CPUs: 1, MemoryBytes: 2 * gib}),
	} {
		if adv, ok := judgePair(w); ok {
			t.Errorf("%s: advised %+v", name, adv)
		}
	}
}

// After an edit the old samples describe a division that is gone; judging them
// would propose a change to a setting that has already been changed.
func TestSamplesFromBeforeAShareWasChangedAreIgnored(t *testing.T) {
	hot := backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2}
	idle := backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4}
	w := pairWindowOf(100, 5, idle, hot)
	// The newest samples are from a 75/25 slot, too few to judge on their own.
	for i := 0; i < 5; i++ {
		s := w[0]
		s.halves.Daemon.CPULimit, s.halves.Runner.CPULimit = 3, 1
		w = append(w, s)
	}
	if adv, ok := judgePair(w); ok {
		t.Fatalf("advised %+v from a window that straddles an edit", adv)
	}
}

// End to end: heartbeats from busy host-sized pairs fill the pool's window, and
// the standing problem names the pool and the command that changes it, then
// clears when the pool's daemon is given the share.
func TestASqueezedSidecarShowsUpAsAStandingProblemUntilTheShareMoves(t *testing.T) {
	h := newHarness(t)
	_, pool, _ := h.fleet()
	pool.DockerMode = store.DockerDinD
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	now := h.c.Now()
	for i, s := range pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2}) {
		r := &store.Runner{ID: fmt.Sprintf("run-%d", i%5), PoolID: pool.ID, AllocationSource: store.AllocationFromHost}
		h.c.observePair(r, backend.Stats{SampledAt: &now, Halves: &s.halves})
	}
	p := h.problemOrNil("pool.daemon_share_suggested")
	if p == nil || p.TargetID != pool.ID {
		t.Fatalf("problem = %+v; want one for the pool", p)
	}
	if want := "zoomies pools edit " + pool.Name + " --daemon-share"; !strings.Contains(p.Fix, want) {
		t.Errorf("fix %q does not carry the command %q", p.Fix, want)
	}

	// Typed pools have no division to judge: nothing is recorded for them.
	typed := &store.Runner{ID: "typed", PoolID: "other", AllocationSource: ""}
	h.c.observePair(typed, backend.Stats{SampledAt: &now, Halves: &backend.PairHalves{}})
	if len(h.c.pairs["other"]) != 0 {
		t.Error("a sample from a typed-limit runner was recorded")
	}
}

// A pool the controller keeps divides its slot evenly and refuses an edit of
// the division, so advice to change it would be advice to do what the pool does
// not allow.
func TestAPoolTheControllerKeepsIsNotAdvisedOnHowItDividesItsSlot(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsDockerMode = "dind" })
	h.mediumHost("build-1")
	h.autoPass()
	pool := h.poolNamed("zoomies-medium")
	if pool.DockerMode != store.DockerDinD || !pool.FromHosts() {
		t.Fatalf("the pool is %+v; this test needs a Docker-in-Docker pool the controller keeps", pool)
	}
	now := h.c.Now()
	for i, s := range pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2}) {
		r := &store.Runner{ID: fmt.Sprintf("run-%d", i%5), PoolID: pool.ID, AllocationSource: store.AllocationFromHost}
		h.c.observePair(r, backend.Stats{SampledAt: &now, Halves: &s.halves})
	}
	if p := h.problemOrNil("pool.daemon_share_suggested"); p != nil {
		t.Fatalf("a pool the controller keeps was advised to change a setting it has not got: %+v", p)
	}
}
