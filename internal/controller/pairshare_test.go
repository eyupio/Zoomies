package controller

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	if !ok || adv.CPU == nil || adv.Memory == nil || !adv.CPU.DaemonHot || !adv.Memory.DaemonHot {
		t.Fatalf("advice = %+v, ok = %v; want a daemon short of both CPU and memory", adv, ok)
	}
	for _, r := range []*resourceAdvice{adv.CPU, adv.Memory} {
		if r.Current != 50 || r.Proposed < 70 || r.Proposed > 90 || r.Proposed%5 != 0 {
			t.Errorf("%s: proposed %d from %d; want a multiple of five between 70 and 90", r.Resource, r.Proposed, r.Current)
		}
	}
}

func TestARunnerSqueezedWhileTheSidecarIdlesIsOfferedASmallerShare(t *testing.T) {
	w := pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2},
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4})
	adv, ok := judgePair(w)
	if !ok || adv.CPU == nil || adv.Memory == nil {
		t.Fatalf("advice = %+v, ok = %v; want advice for both resources", adv, ok)
	}
	for _, r := range []*resourceAdvice{adv.CPU, adv.Memory} {
		if r.DaemonHot || r.Proposed > 30 || r.Proposed < store.MinDaemonSharePercent {
			t.Errorf("%s: %+v; want a smaller share for the sidecar", r.Resource, r)
		}
	}
}

// CPU and memory are judged apart, which is the point of splitting them: a
// daemon running a build flat out on CPU while the runner holds the memory is
// told to take more CPU and give memory back, in one notice, with a flag for each.
func TestCPUAndMemoryAreAdvisedOnTheirOwn(t *testing.T) {
	// Daemon: CPU-bound, little memory. Runner: nearly idle on CPU, memory-heavy.
	w := pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: 3*gib + gib/2},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: gib / 4})
	adv, ok := judgePair(w)
	if !ok || adv.CPU == nil || adv.Memory == nil {
		t.Fatalf("advice = %+v, ok = %v; want both resources advised", adv, ok)
	}
	if !adv.CPU.DaemonHot || adv.CPU.Proposed < 70 {
		t.Errorf("CPU: %+v; the daemon is short of CPU and should be offered more", adv.CPU)
	}
	if adv.Memory.DaemonHot || adv.Memory.Proposed > 30 {
		t.Errorf("memory: %+v; the runner is short of memory and the daemon's share should come down", adv.Memory)
	}

	// Only the resource that is wrong is advised.
	w = pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: gib})
	adv, ok = judgePair(w)
	if !ok || adv.CPU == nil || adv.Memory != nil {
		t.Fatalf("advice = %+v, ok = %v; want CPU alone", adv, ok)
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
	_, pool, host := h.fleet()
	// One slot to a machine, so that a lopsided share is one the host can carry:
	// the notice does not propose what would cost the pool its runners.
	host.Capacity = 1
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	pool.DockerMode = store.DockerDinD
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	now := h.c.Now()
	for i, s := range pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: gib / 4},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: 3*gib + gib/2}) {
		r := &store.Runner{ID: fmt.Sprintf("run-%d", i%5), PoolID: pool.ID, AllocationSource: store.AllocationFromHost}
		sampled := now.Add(time.Duration(i) * time.Second)
		h.c.observePair(r, backend.Stats{SampledAt: &sampled, Halves: &s.halves})
	}
	p := h.problemOrNil("pool.daemon_share_suggested")
	if p == nil || p.TargetID != pool.ID {
		t.Fatalf("problem = %+v; want one for the pool", p)
	}
	// Both resources want the same figure here, so it is one share and one flag.
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

// A pool that takes its size from its hosts' runner profiles gives each runner
// a share of a slot exactly as one sized by the host's plain share does, so the
// advice about how the pair divides it has to hear from both. Comparing the
// runner's source with "host" alone left every profile-sized pool -- the pools
// the runner-sizes screen makes -- with an empty window and no advice, ever.
func TestASqueezedSidecarIsAdvisedWhetherTheHostsShareOrItsProfileSizedTheRunners(t *testing.T) {
	for _, source := range []string{store.AllocationFromHost, store.AllocationFromProfile} {
		t.Run(source, func(t *testing.T) {
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
				r := &store.Runner{ID: fmt.Sprintf("run-%d", i%5), PoolID: pool.ID, AllocationSource: source}
				sampled := now.Add(time.Duration(i) * time.Second)
				h.c.observePair(r, backend.Stats{SampledAt: &sampled, Halves: &s.halves})
			}
			if p := h.problemOrNil("pool.daemon_share_suggested"); p == nil || p.TargetID != pool.ID {
				t.Fatalf("problem = %+v; want one for the pool whose runners are sized by %q", p, source)
			}
		})
	}
}

// When the two resources want different figures the notice says so, with a flag
// for each: more CPU for a daemon that is building, less memory for one that is not.
func TestAMixedSqueezeIsOneNoticeWithAFlagForEachResource(t *testing.T) {
	h := newHarness(t)
	_, pool, _ := h.fleet()
	pool.DockerMode = store.DockerDinD
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	now := h.c.Now()
	for i, s := range pairWindowOf(100, 5,
		backend.HalfUse{CPUs: 0.1, MemoryBytes: 3*gib + gib/2},
		backend.HalfUse{CPUs: 1.9, MemoryBytes: gib / 4}) {
		r := &store.Runner{ID: fmt.Sprintf("run-%d", i%5), PoolID: pool.ID, AllocationSource: store.AllocationFromHost}
		sampled := now.Add(time.Duration(i) * time.Second)
		h.c.observePair(r, backend.Stats{SampledAt: &sampled, Halves: &s.halves})
	}
	p := h.problemOrNil("pool.daemon_share_suggested")
	if p == nil {
		t.Fatal("no pool.daemon_share_suggested problem")
	}
	for _, want := range []string{"--daemon-cpu-share", "--daemon-memory-share"} {
		if !strings.Contains(p.Fix, want) {
			t.Errorf("fix %q does not carry %s", p.Fix, want)
		}
	}
	if strings.Contains(p.Fix, "--daemon-share ") {
		t.Errorf("fix %q offers one share where the resources disagree", p.Fix)
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
		sampled := now.Add(time.Duration(i) * time.Second)
		h.c.observePair(r, backend.Stats{SampledAt: &sampled, Halves: &s.halves})
	}
	if p := h.problemOrNil("pool.daemon_share_suggested"); p != nil {
		t.Fatalf("a pool the controller keeps was advised to change a setting it has not got: %+v", p)
	}
}

// squeezedRunnerPool is a Docker-in-Docker pool whose runner is flat out while its
// sidecar idles, on hosts of the given size with one slot each: the pool in the
// report this is written from, a 35% sidecar on four-core machines. Heartbeats
// carry the limits that division gives a slot of that size.
func squeezedRunnerPool(t *testing.T, hostCPUs int) (*harness, *store.Pool) {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.DockerMode = store.DockerDinD
	pool.Resources.MinCPUs = 1
	pool.Resources.DaemonCPUSharePercent = 35
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		h.measuredHost(fmt.Sprintf("build-%d", i), hostCPUs, 32768, 1, enforcesEverything)
	}
	slot := float64(hostCPUs) - 0.5
	now := h.c.Now()
	for i := 0; i < 100; i++ {
		halves := backend.PairHalves{
			Runner: backend.HalfUse{CPULimit: slot * 0.65, CPUs: slot * 0.65, MemoryLimit: 4 * gib, MemoryBytes: gib / 4},
			Daemon: backend.HalfUse{CPULimit: slot * 0.35, CPUs: 0.05, MemoryLimit: 4 * gib, MemoryBytes: gib / 4},
		}
		r := &store.Runner{ID: fmt.Sprintf("run-%d", i%5), PoolID: pool.ID, AllocationSource: store.AllocationFromHost}
		sampled := now.Add(time.Duration(i) * time.Second)
		h.c.observePair(r, backend.Stats{SampledAt: &sampled, Halves: &halves})
	}
	return h, pool
}

// The share that the use points at is not advice if taking it costs the pool its
// hosts: a sidecar held to the pool's smallest runner at 10% is a request for a
// slot ten times that, and four-core machines have none. The notice said so only
// after the operator had made the change, in a warning about the same pool.
func TestAShareThatWouldCostTheFleetItsRunnersIsNotProposed(t *testing.T) {
	h, pool := squeezedRunnerPool(t, 4)
	before, err := h.c.poolRoom(h.ctx, pool)
	if err != nil || before.Runners == 0 {
		t.Fatalf("the pool has room for %d runners (%v); the case needs a fleet that runs it today", before.Runners, err)
	}
	p := h.problemOrNil("pool.daemon_share_suggested")
	if p == nil {
		t.Fatal("no notice: the squeeze is real and an operator told nothing cannot know the share was priced")
	}
	if strings.Contains(p.Fix, "--daemon-cpu-share") || strings.Contains(p.Fix, "--daemon-share") {
		t.Errorf("fix %q proposes a share that costs runners", p.Fix)
	}
	for _, want := range []string{"would leave its hosts room for", "smallest runner"} {
		if !strings.Contains(p.Detail+" "+p.Fix, want) {
			t.Errorf("notice %q / %q does not say %q", p.Detail, p.Fix, want)
		}
	}
}

// Somewhere between the share the use points at and the one the pool has is one
// the hosts can carry, and that is the one to offer.
func TestAShareIsBroughtBackToOneTheHostsCanCarry(t *testing.T) {
	h, pool := squeezedRunnerPool(t, 6)
	p := h.problemOrNil("pool.daemon_share_suggested")
	if p == nil {
		t.Fatal("no notice")
	}
	if !strings.Contains(p.Fix, "--daemon-cpu-share 20") {
		t.Fatalf("fix %q: want the 20%% that a 5.5-core slot can carry, not the 10%% it cannot", p.Fix)
	}

	// And taking it loses no runner.
	before, err := h.c.poolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	pool.Resources.DaemonCPUSharePercent = 20
	after, err := h.c.poolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if after.Runners < before.Runners {
		t.Errorf("the proposed share leaves room for %d runners, down from %d", after.Runners, before.Runners)
	}
}

// The notice carries its proposal as numbers, so that the UI can make the change
// in one click without reading a sentence for it.
func TestTheNoticeCarriesTheShareItProposesForTheUIToApply(t *testing.T) {
	h, _ := squeezedRunnerPool(t, 6)
	p := h.problemOrNil("pool.daemon_share_suggested")
	if p == nil || p.DaemonShare == nil {
		t.Fatalf("problem = %+v; want one with the change attached", p)
	}
	if p.DaemonShare.CPUPercent != 20 || p.DaemonShare.MemoryPercent != 0 {
		t.Errorf("change = %+v; want 20%% of CPU and nothing for memory, which has no advice", p.DaemonShare)
	}

	// A notice that only explains why it will not move the share has nothing to apply.
	h, _ = squeezedRunnerPool(t, 4)
	p = h.problemOrNil("pool.daemon_share_suggested")
	if p == nil || p.DaemonShare != nil {
		t.Errorf("problem = %+v; want the notice with no change to apply, since moving the share costs runners", p)
	}
}

// Applying the suggestion has to end it. The window is judged at the newest
// sample's division, which is the old one until a runner made after the edit
// reports, so without this the card stays up offering the change just made.
func TestTheNoticeClearsAtOnceWhenThePoolsShareIsChanged(t *testing.T) {
	h, pool := squeezedRunnerPool(t, 6)
	if h.problemOrNil("pool.daemon_share_suggested") == nil {
		t.Fatal("no notice to clear")
	}
	pool.Resources.DaemonCPUSharePercent = 20
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("pool.daemon_share_suggested"); p != nil {
		t.Fatalf("the notice is still up after the share was changed: %+v", p)
	}
}

// The agent sends its last sample with the heartbeat and again with each reconcile
// report, and keeps sending it when the next sample fails. The same reading is not
// more evidence for arriving again, or 60 samples would be half an hour of one
// runner.
func TestTheSameSampleArrivingTwiceIsCountedOnce(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "builders")
	pool.DockerMode = store.DockerDinD
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	now := h.c.Now()
	halves := backend.PairHalves{
		Runner: backend.HalfUse{MemoryBytes: gib, MemoryLimit: 4 * gib, CPULimit: 2},
		Daemon: backend.HalfUse{MemoryBytes: gib, MemoryLimit: 4 * gib, CPULimit: 2},
	}
	r := &store.Runner{ID: "run-1", PoolID: pool.ID, AllocationSource: store.AllocationFromHost}
	for i := 0; i < 5; i++ {
		h.c.observePair(r, backend.Stats{SampledAt: &now, Halves: &halves})
	}
	later := now.Add(30 * time.Second)
	h.c.observePair(r, backend.Stats{SampledAt: &later, Halves: &halves})
	h.c.observePair(r, backend.Stats{SampledAt: &later, Halves: &halves})
	if n := len(h.c.pairs[pool.ID]); n != 2 {
		t.Errorf("%d samples kept from two readings", n)
	}
}
