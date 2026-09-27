package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// withMinimum is a fixed-size pool that will accept a smaller runner when no
// host has room for the standard.
func withMinimum(name string, cpus float64, memoryMB int64, minCPUs float64, minMemoryMB int64) *store.Pool {
	p := limited(name, cpus, memoryMB)
	p.Resources.MinCPUs = minCPUs
	p.Resources.MinMemoryMB = minMemoryMB
	return p
}

// The case the minimum exists for: a machine a little short of the standard
// size. Without a minimum the job waits for a host that is never coming; with
// one it runs, and with as much of the standard as the host can spare rather
// than the minimum, because the minimum is what the operator will accept and
// not what they asked for.
func TestAHostJustShortOfTheStandardRunsTheJobWithAllItCanSpare(t *testing.T) {
	h := sized("short", 1, 16, hostFor(30*1024), 100000)
	p := withMinimum("big", 8, 32*1024, 4, 24*1024)

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	placed := hs.placeAvoiding(p, 1, nil)
	if len(placed) != 1 || placed[0].hostID != "short" {
		t.Fatalf("placed %+v, want one runner on the short host", placed)
	}
	size := placed[0].size
	if size == nil {
		t.Fatal("the runner was placed at the standard size on a host that has no room for it")
	}
	if size.MemoryMB != 30*1024 {
		t.Errorf("memory = %d MB, want the 30 GB the host has, not the %d MB minimum", size.MemoryMB, p.Resources.MinMemoryMB)
	}
	if size.CPUs != 8 {
		t.Errorf("CPU = %g, want the standard 8: the host has room for all of it", size.CPUs)
	}

	// The pool with no minimum is exactly what it was: it does not fit.
	plain := limited("big", 8, 32*1024)
	hs = newHostSet([]*store.Host{h}, []*store.Pool{plain}, nil, now)
	if got := hs.placeAvoiding(plain, 1, nil); len(got) != 0 {
		t.Fatalf("a pool with no minimum was placed on a host too small for it: %+v", got)
	}
}

// A minimum never shrinks a runner the fleet could have given the full size
// to: the standard goes on any host with room for it, even when a short host
// would score better, and the reduced size is only the fallback.
func TestTheStandardSizeWinsWhereverAHostHasRoomForIt(t *testing.T) {
	short := sized("a-short", 4, 16, hostFor(30*1024), 100000)
	whole := sized("b-whole", 1, 16, hostFor(32*1024), 100000)
	p := withMinimum("big", 8, 32*1024, 4, 24*1024)

	hs := newHostSet([]*store.Host{short, whole}, []*store.Pool{p}, nil, now)
	placed := hs.placeAvoiding(p, 2, nil)
	if len(placed) != 2 {
		t.Fatalf("placed %+v, want two runners", placed)
	}
	if placed[0].hostID != "b-whole" || placed[0].size != nil {
		t.Errorf("first runner = %+v, want the standard size on the host with room for it", placed[0])
	}
	if placed[1].hostID != "a-short" || placed[1].size == nil {
		t.Errorf("second runner = %+v, want a reduced size on the short host once the whole one is full", placed[1])
	}
}

// Below the minimum is still no: a floor is a floor.
func TestAHostBelowTheMinimumTakesNothing(t *testing.T) {
	h := sized("tiny", 1, 16, hostFor(20*1024), 100000)
	p := withMinimum("big", 8, 32*1024, 4, 24*1024)
	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	if got := hs.placeAvoiding(p, 1, nil); len(got) != 0 {
		t.Fatalf("placed %+v below the pool's 24 GB minimum", got)
	}
	if HostFits(h, p) {
		t.Error("HostFits says a 20 GB host can run a pool whose minimum is 24 GB")
	}
	if HostShortfall(h, p) == "" {
		t.Error("no shortfall named for a host below the minimum")
	}
}

// A reduced runner is charged what it was given. Charging it the standard
// would read a host filled with reduced runners as over-committed and refuse
// the next one it had room for.
func TestAReducedRunnerIsChargedWhatItWasGiven(t *testing.T) {
	h := sized("short", 2, 16, hostFor(40*1024), 100000)
	p := withMinimum("big", 8, 32*1024, 4, 16*1024)
	reduced := &store.Runner{ID: "r1", PoolID: p.ID, HostID: h.ID, State: store.RunnerBusy,
		AllocatedCPUs: 8, AllocatedMemoryMB: 20 * 1024, AllocationSource: store.AllocationReduced}
	runners := map[string][]*store.Runner{p.ID: {reduced}}

	if got := Reserved(h, []*store.Pool{p}, runners); got.MemoryMB != 20*1024 {
		t.Errorf("reserved memory = %d MB, want the 20 GB the reduced runner was given", got.MemoryMB)
	}
	// 40 GB less the 20 GB promised leaves 20: short of the standard, above
	// the minimum, so the second runner is reduced too, to what is left.
	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, runners, now)
	placed := hs.placeAvoiding(p, 1, nil)
	if len(placed) != 1 || placed[0].size == nil || placed[0].size.MemoryMB != 20*1024 {
		t.Fatalf("placed %+v, want a second runner reduced to the 20 GB left", placed)
	}
}

// A docker-in-docker runner gives its daemon the same limits it has, so the
// pair is charged twice what each is given, and a reduced pair splits what the
// host can spare between them.
func TestAReducedDockerInDockerPairSplitsWhatIsLeft(t *testing.T) {
	h := sized("short", 1, 16, hostFor(12*1024), 100000)
	p := withMinimum("builds", 4, 8*1024, 2, 4*1024)
	p.DockerMode = store.DockerDinD

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	placed := hs.placeAvoiding(p, 1, nil)
	if len(placed) != 1 || placed[0].size == nil {
		t.Fatalf("placed %+v, want one reduced pair", placed)
	}
	if got := placed[0].size.MemoryMB; got != 6*1024 {
		t.Errorf("each container = %d MB, want 6 GB: half of the 12 GB the host can spare", got)
	}
}

// A reduced docker-in-docker runner's row holds the per-container limit --
// what ReducedSize hands to the backend, and what the previous test reads off
// placed[0].size -- not the host-side charge that placement actually
// subtracted. RunnerCharge has to double a typed field back to recover that
// charge, the same doubling Reserve applies to a full-size pair. Get the
// factor backwards and a host already holding one of these runners is read as
// half as loaded as it is: exactly the mismatch Reserve's own comment warns
// about ("a host read as half committed while its containers' quotas added up
// to every core it had").
func TestAReducedDockerInDockerRunnerIsChargedTwiceItsRow(t *testing.T) {
	h := sized("short", 1, 16, hostFor(12*1024), 100000)
	p := withMinimum("builds", 4, 8*1024, 2, 4*1024)
	p.DockerMode = store.DockerDinD

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	placed := hs.placeAvoiding(p, 1, nil)
	if len(placed) != 1 || placed[0].size == nil {
		t.Fatalf("placed %+v, want one reduced pair", placed)
	}
	grant := *placed[0].size

	r := &store.Runner{ID: "r1", PoolID: p.ID, HostID: h.ID, State: store.RunnerBusy,
		AllocatedCPUs: grant.CPUs, AllocatedMemoryMB: grant.MemoryMB, AllocationSource: store.AllocationReduced}

	got := RunnerCharge(p, h, r)
	if got.CPUs != grant.CPUs*2 {
		t.Errorf("CPU charge = %g, want double the row's per-container %g: a docker-in-docker pair is charged for both containers", got.CPUs, grant.CPUs)
	}
	if got.MemoryMB != grant.MemoryMB*2 {
		t.Errorf("memory charge = %d MB, want double the row's per-container %d MB", got.MemoryMB, grant.MemoryMB)
	}

	// Reserved sums RunnerCharge across a host's live runners -- the figure
	// the next reconcile pass trusts to know what room is left. It must agree
	// with what the placement pass just subtracted for this exact runner.
	runners := map[string][]*store.Runner{p.ID: {r}}
	if reserved := Reserved(h, []*store.Pool{p}, runners); reserved != got {
		t.Errorf("Reserved = %+v, want it to match RunnerCharge's %+v for the host's one live runner", reserved, got)
	}
}

// The create's reason says the runner is smaller than its pool asks for, and
// the action carries the size, so the Runners page and the scaling history
// both explain a slow job on a reduced runner.
func TestAReducedCreateSaysSoInItsReason(t *testing.T) {
	h := sized("short", 1, 16, hostFor(30*1024), 100000)
	p := withMinimum("big", 8, 32*1024, 4, 24*1024)
	p.Labels = store.NormalizeLabels([]string{"big"})
	s := snap([]*store.Pool{p}, nil, []*store.Job{queued("j1", time.Minute, "big")}, []*store.Host{h})
	creates := actionsOf(Decide(s).Actions, ActionCreate)
	if len(creates) != 1 {
		t.Fatalf("creates = %+v, want one", creates)
	}
	if creates[0].Size == nil || creates[0].Size.MemoryMB != 30*1024 {
		t.Errorf("size = %+v, want 30 GB carried on the action", creates[0].Size)
	}
	if !strings.Contains(creates[0].Reason, "reduced to 8 CPU and 30 GB") {
		t.Errorf("reason = %q, want it to say the runner was reduced and to what", creates[0].Reason)
	}
}
