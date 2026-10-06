package scheduler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// sized is a host that has told the fleet how big it is: CPUs, memory and the
// free disk behind its work directory, which is what the reporting half of
// this work taught agents to measure.
func sized(id string, capacity, cpus int, memoryMB, diskFreeMB int64) *store.Host {
	h := testHost(id, capacity, 0)
	h.CPUs = cpus
	h.MemoryMB = memoryMB
	h.DiskTotalMB = diskFreeMB * 2
	h.DiskFreeMB = diskFreeMB
	// A measured host whose daemon can apply every limit, which is what a
	// rootful Docker on cgroup v2 reports and the shape most tests want.
	h.BackendInfo = store.HostBackends{{Kind: store.BackendDocker, Available: true,
		Limits: store.LimitSupport{Known: true, CPU: true, Memory: true, Pids: true}}}
	return h
}

// limited is a pool that says what one of its runners may consume.
func limited(name string, cpus float64, memoryMB int64) *store.Pool {
	p := testPool(name, name)
	p.Resources = store.Resources{CPUs: cpus, MemoryMB: memoryMB}
	return p
}

// hostFor is the machine that leaves want MB allocatable once its own reserve
// is held back. The reserve scales with the machine, so it cannot be added as
// a constant: a test that wrote 32*1024+512 would be describing a floor that
// only applies to small hosts.
func hostFor(want int64) int64 {
	total := want
	for range 8 {
		got := total - (&store.Host{MemoryMB: total}).MemoryReserve()
		if got == want {
			return total
		}
		total += want - got
	}
	return total
}

// The headline case for the whole reservation model, and the one an operator
// would recognise: a host advertises 32 GB, a pool asks for 4 GB a runner, and
// the ninth runner is the one that would have been killed rather than run.
func TestAHostAdmitsWhatItsMemoryCoversAndRefusesTheNext(t *testing.T) {
	// Capacity is deliberately far above what memory allows, so that only the
	// memory rule can be what stops the ninth. The machine is sized so that
	// exactly 32 GB is left after the reserve, which scales with it: a
	// twentieth of the whole, capped, and never under the flat floor.
	h := sized("host_a", 32, 32, hostFor(32*1024), 500*1024)
	p := limited("big", 0, 4096)

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	got := hs.place(p, 12)
	if len(got) != 8 {
		t.Fatalf("placed %d runners on a 32 GB host at 4 GB each, want 8", len(got))
	}
	if more := hs.place(p, 1); len(more) != 0 {
		t.Errorf("the ninth runner was placed on a host with nothing left: %v", more)
	}
}

// Two pools drawing on one host is the case a per-pool check would get wrong:
// each fits on its own, and the host cannot carry both. The slot model already
// had this property and the resource model has to keep it, or the second pool
// is promised memory the first has already been given.
func TestTwoPoolsCannotOversubscribeOneHostsMemory(t *testing.T) {
	h := sized("host_a", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 500*1024)
	first := limited("first", 0, 6*1024)
	second := limited("second", 0, 4*1024)

	hs := newHostSet([]*store.Host{h}, []*store.Pool{first, second}, nil, now)
	if got := hs.place(first, 1); len(got) != 1 {
		t.Fatalf("the first pool was refused a host with 8 GB free: %v", got)
	}
	// 2 GB is left and the second pool wants 4.
	if got := hs.place(second, 1); len(got) != 0 {
		t.Errorf("the second pool was promised memory the first already has: %v", got)
	}
}

// A docker-in-docker pool's build runs in a sidecar that the backend gives the
// same limits as the runner, so the host carries twice what the pool asked
// for. Charging it once would let a fleet promise away a machine twice over.
func TestADockerInDockerRunnerIsChargedForItsSidecar(t *testing.T) {
	h := sized("host_a", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 500*1024)
	p := limited("dind", 0, 4096)
	p.DockerMode = store.DockerDinD

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	got := hs.place(p, 4)
	if len(got) != 1 {
		t.Fatalf("placed %d dind runners on an 8 GB host at 4 GB each, want 1", len(got))
	}

	// The same pool without the sidecar fits twice, which is what says the
	// difference above came from the sidecar and not from the arithmetic.
	plain := limited("plain", 0, 4096)
	hs = newHostSet([]*store.Host{sized("host_b", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 500*1024)}, []*store.Pool{plain}, nil, now)
	if got := hs.place(plain, 4); len(got) != 2 {
		t.Fatalf("placed %d plain runners, want 2", len(got))
	}
}

// A slot is one runner, whatever the runner brought with it.
//
// A defaulted docker-in-docker pair splits one slot between its two containers
// -- store.Resources.SplitWithDaemon -- so four slots are four runners, the
// same as any other pool. The arithmetic that used to charge two shares for
// such a pair is why an operator was told a twelve-core host with four slots
// had room for two, and then told to halve its capacity to match; the advice
// converged on one slot, which held nothing.
func TestADefaultedDockerInDockerPairSharesOneSlot(t *testing.T) {
	h := sized("host_a", 4, 12, 32*1024, 500*1024)
	p := testPool("builders", "builders")
	p.DockerMode = store.DockerDinD

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	if got := hs.place(p, 4); len(got) != 4 {
		t.Fatalf("placed %d defaulted dind runners on a 12-CPU host with 4 slots, want 4", len(got))
	}

	// A pool that typed its own figures is charged for both containers, which
	// is what says the slot -- not the sidecar -- is what changed above: the
	// daemon is given what the job was promised, so the host pays for it.
	// 2 CPU typed is charged 4 with the daemon, so an 11-CPU machine takes two
	// rather than the five it would take at the figure on the pool.
	typed := limited("typed", 2, 4*1024)
	typed.DockerMode = store.DockerDinD
	hs = newHostSet([]*store.Host{sized("host_b", 4, 12, 32*1024, 500*1024)}, []*store.Pool{typed}, nil, now)
	if got := hs.place(typed, 4); len(got) != 2 {
		t.Fatalf("placed %d dind runners of 2 typed CPU on a 12-CPU host, want 2 at twice the charge", len(got))
	}
}

// The invariant underneath that number, stated in the two functions that have
// to agree: what a host is charged for a runner is never less than what its
// backend is told to give one. A charge below the limit is a host promising
// away room it has already handed to a cgroup, which is how a machine reading
// as half committed runs out of cores.
func TestAHostIsNeverChargedLessThanItsRunnersAreGiven(t *testing.T) {
	for _, capacity := range []int{1, 2, 3, 4, 8} {
		for _, mode := range []store.DockerMode{store.DockerNone, store.DockerDinD} {
			h := sized("host_a", capacity, 12, 32*1024, 500*1024)
			p := testPool("builders", "builders")
			p.DockerMode = mode
			// A dind pool's spec creates two containers with these limits;
			// every other pool creates one.
			containers := 1.0
			if mode == store.DockerDinD {
				containers = 2
			}
			given, source := Allocation(p, h, true)
			charge := Reserve(p, h)
			// What the backend hands out: one container of the given limits,
			// or -- where those came from the host's slot and the pool brings
			// a daemon -- the two halves that slot is split into.
			handed := given
			if mode == store.DockerDinD {
				if source == store.AllocationFromHost {
					runner, daemon := given.SplitWithDaemon()
					handed = store.Resources{
						CPUs:     runner.CPUs + daemon.CPUs,
						MemoryMB: runner.MemoryMB + daemon.MemoryMB,
					}
				} else {
					handed = store.Resources{CPUs: containers * given.CPUs, MemoryMB: int64(containers) * given.MemoryMB}
				}
			}
			if charge.CPUs+cpuEpsilon < handed.CPUs {
				t.Errorf("capacity %d, %s: charged %v CPU and handed out %v across its containers",
					capacity, mode, charge.CPUs, handed.CPUs)
			}
			if charge.MemoryMB < handed.MemoryMB {
				t.Errorf("capacity %d, %s: charged %d MB and handed out %d MB across its containers",
					capacity, mode, charge.MemoryMB, handed.MemoryMB)
			}
		}
	}
}

// A host with one slot must still be able to run the pool it was joined for,
// as long as that one slot is big enough to give both containers of the pair
// a comfortable share. Charging two shares for a defaulted pair would refuse
// every one of them on every small host in the fleet -- an upgrade that
// empties a fleet rather than protecting it -- so the charge stays one share;
// what changed is that the share itself now has to clear a real floor. See
// TestASplitTooThinForBothContainersIsRefused for the host this one is not.
func TestASingleSlotHostStillPlacesAWellSizedDefaultedDockerInDockerRunner(t *testing.T) {
	h := sized("host_ample", 1, 4, 8*1024, 500*1024)
	p := testPool("builders", "builders")
	p.DockerMode = store.DockerDinD

	if !HostFits(h, p) {
		t.Fatalf("a one-slot host with room for both containers was refused the pool it exists for: %s", HostShortfall(h, p))
	}
	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	if got := hs.place(p, 2); len(got) != 1 {
		t.Fatalf("placed %d runners on a one-slot host, want 1", len(got))
	}
}

// Stability over performance: a defaulted docker-in-docker pair still splits
// one slot between a runner and the daemon that builds for it, and a slot too
// thin to give both of them a comfortable share -- a core and 2 GB each, not
// half of the bare minimum that only keeps a runner from being killed -- is
// refused rather than handed to a daemon that cannot keep up with its own
// job. That daemon timing out on a create is the backend_busy fault this
// refusal exists to prevent, not merely warn about after the fact.
func TestASplitTooThinForBothContainersIsRefused(t *testing.T) {
	// 2 CPUs less the half-core floor is 1.5, which split between a runner
	// and its daemon is 0.75 each: enough to avoid store.MinRunnerCPUs, not
	// enough to keep a nested dockerd answering under load.
	h := sized("host_thin", 1, 2, 4*1024, 500*1024)
	p := testPool("builders", "builders")
	p.DockerMode = store.DockerDinD

	if field := ShareTooSmall(h, p); field != "cpu" {
		t.Fatalf("ShareTooSmall = %q, want cpu: 1.5 allocatable CPU split between a runner and its daemon is not a comfortable core each", field)
	}
	if HostFits(h, p) {
		t.Error("a one-slot host with 1.5 CPU to split between two containers was accepted for a pair that needs a core each")
	}
	why := HostShortfall(h, p)
	for _, want := range []string{"1.5 allocatable CPU", "needs at least 2", "Docker daemon"} {
		if !strings.Contains(why, want) {
			t.Errorf("HostShortfall = %q, want it to mention %q", why, want)
		}
	}
	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	if got := hs.place(p, 2); len(got) != 0 {
		t.Fatalf("placed %d runners on a host too thin for the pair, want 0: %v", len(got), got)
	}
}

// The upgrade property, and the one that decides whether this change is safe
// to ship: a host carrying only pools that set no limits admits exactly what
// its capacity admitted before any of this existed. The fallback share is what
// makes that true -- each runner is charged one slot's worth of the machine,
// so the slot count is still what runs out.
func TestAHostOfUnlimitedPoolsStillAdmitsItsCapacity(t *testing.T) {
	for _, capacity := range []int{1, 3, 4, 7, 8} {
		h := sized("host_a", capacity, 30, 30*1024, 500*1024)
		p := testPool("unlimited", "unlimited")

		hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
		got := hs.place(p, capacity+3)
		if len(got) != capacity {
			t.Errorf("capacity %d admitted %d runners; the upgrade changed what a host takes", capacity, len(got))
		}
	}
}

// What the fallback share is actually for. The upgrade property above holds
// with or without it -- a pool charged nothing is bounded by slots, and so is
// a pool charged one slot's worth -- so the case that separates them is a host
// carrying both kinds at once. An unlimited pool that costs nothing lets a
// fleet hand the same memory to a pool that asked for it by name.
func TestAnUnlimitedPoolStillSpendsTheMemoryItTakes(t *testing.T) {
	h := sized("host_a", 2, 8, 8*1024+store.MinHostReserveMemoryMB, 500*1024)
	unlimited := testPool("unlimited", "unlimited")
	named := limited("named", 0, 6*1024)

	hs := newHostSet([]*store.Host{h}, []*store.Pool{unlimited, named}, nil, now)
	if got := hs.place(unlimited, 1); len(got) != 1 {
		t.Fatalf("the unlimited pool was refused an empty host: %v", got)
	}
	// Half the machine is one slot's worth and is now spoken for, so the 6 GB
	// pool does not fit beside it even though a slot is free.
	if got := hs.place(named, 1); len(got) != 0 {
		t.Errorf("6 GB was promised on a host with 4 GB left: %v", got)
	}
}

// An agent that predates the resource fields reports nothing, and nothing is
// not zero. Reading an unmeasured host as an empty one would refuse every
// placement on it, which on a fleet mid-upgrade is every host.
func TestAHostThatReportedNoResourcesIsStillPlacedBySlots(t *testing.T) {
	h := testHost("host_old", 3, 0) // no CPUs, no memory, no disk
	p := limited("big", 8, 64*1024)

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	if got := hs.place(p, 5); len(got) != 3 {
		t.Fatalf("placed %d runners on an unmeasured host with 3 slots, want 3: %v", len(got), got)
	}
}

// The reservation is rebuilt from the runner rows on every pass -- there is no
// second table holding it -- so which rows count is the whole of the
// arithmetic. Live rows count and terminal ones do not, exactly as the host's
// own slot count does: a failed runner is not holding any memory, and charging
// the host for it would shrink the fleet a little with every failure.
func TestOnlyLiveRunnersHoldAReservation(t *testing.T) {
	p := limited("big", 0, 4096)
	h := sized("host_a", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 500*1024)
	// One live runner is holding 4 GB; the failed and removed rows are not.
	h.ActiveRunners = 1
	runners := map[string][]*store.Runner{p.ID: {
		onHost(testRunner("r_live", p, store.RunnerBusy, 0), "host_a"),
		onHost(testRunner("r_failed", p, store.RunnerFailed, 0), "host_a"),
		onHost(testRunner("r_gone", p, store.RunnerRemoved, 0), "host_a"),
	}}

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, runners, now)
	if got := hs.place(p, 3); len(got) != 1 {
		t.Fatalf("placed %d runners beside one live 4 GB runner on an 8 GB host, want 1: %v", len(got), got)
	}
}

// Disk is a gate rather than a budget: a host with less free space than its
// reserve takes no runner at all, whatever the pool asks for, because the
// reserve is the room the machine needs to do the work rather than something
// to spend down. Nothing evicts to make room -- retention is ZF-105's.
func TestAHostBelowItsDiskReserveTakesNothing(t *testing.T) {
	full := sized("host_full", 4, 16, 16*1024, store.MinHostReserveDiskMB-1)
	roomy := sized("host_roomy", 4, 16, 16*1024, 200*1024)
	p := testPool("unlimited", "unlimited")

	hs := newHostSet([]*store.Host{full, roomy}, []*store.Pool{p}, nil, now)
	for _, id := range hs.place(p, 4) {
		if id == "host_full" {
			t.Fatalf("a runner was placed on a host below its disk reserve")
		}
	}

	// And the operator is told which resource it was, because "no capacity"
	// would send them to wait for a job that finishing will not help: a runner
	// leaves its caches behind on purpose.
	only := newHostSet([]*store.Host{full}, []*store.Pool{p}, nil, now)
	b := only.why(p)
	if !strings.Contains(b.what, "low on disk") {
		t.Errorf("the reason does not name the disk: %q", b.what)
	}
	if !strings.Contains(b.fix, "free space") {
		t.Errorf("the fix does not say what to free: %q", b.fix)
	}
}

// A machine too small for one runner is a different answer from a machine that
// is busy, and an operator told to wait for the second when they have the
// first waits for ever.
func TestAHostTooSmallForThePoolSaysSoRatherThanSayingItIsFull(t *testing.T) {
	h := sized("host_small", 4, 4, 4*1024, 200*1024)
	p := limited("huge", 0, 32*1024)

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	b := hs.why(p)
	if !strings.Contains(b.what, "too small for this pool's limits") {
		t.Errorf("the reason does not say the machine is too small: %q", b.what)
	}
	if b.atCapacity {
		t.Error("a host too small for the pool was reported as merely busy")
	}
	if !strings.Contains(b.fix, "lower this pool's") {
		t.Errorf("the fix does not name the pool's own limits: %q", b.fix)
	}
}

// The other half of that distinction: a host large enough for the pool that
// has already promised its memory away is short of memory, not too small, and
// waiting is exactly the right advice.
func TestAHostWhoseMemoryIsSpentSaysItIsShortOfMemory(t *testing.T) {
	p := limited("big", 0, 6*1024)
	h := sized("host_a", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 200*1024)
	h.ActiveRunners = 1
	runners := map[string][]*store.Runner{p.ID: {onHost(testRunner("r1", p, store.RunnerBusy, 0), "host_a")}}

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, runners, now)
	b := hs.why(p)
	if !strings.Contains(b.what, "short of memory") {
		t.Errorf("the reason does not name memory: %q", b.what)
	}
	if strings.Contains(b.what, "too small") {
		t.Errorf("a host that fits the pool was called too small: %q", b.what)
	}
}

// HostCanRun is the one placement rule, so the wizard's count, the
// capacity-demand signal and image prewarming inherit the fit without asking
// for it. A host that cannot hold one runner of the pool is not a host that
// can run it, whatever else is true of it.
func TestHostCanRunAsksWhetherARunnerFits(t *testing.T) {
	small := sized("host_small", 4, 2, 2*1024, 200*1024)
	big := sized("host_big", 4, 16, 32*1024, 200*1024)
	p := limited("huge", 0, 16*1024)

	if HostCanRun(small, p, now) {
		t.Error("a host with 2 GB was offered a pool that reserves 16")
	}
	if !HostCanRun(big, p, now) {
		t.Error("a host with 32 GB was refused a pool that reserves 16")
	}
}

// The reserve is the operator's, and an agent reports what it sees rather than
// what it may have: memory an operator held back for the machine is not
// memory the fleet may promise to a job.
func TestTheOperatorsReserveIsHeldBackFromPlacement(t *testing.T) {
	p := limited("big", 0, 4096)
	h := sized("host_a", 8, 16, 12*1024, 200*1024)
	h.ReserveMemoryMB = 8 * 1024

	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	if got := hs.place(p, 3); len(got) != 1 {
		t.Fatalf("placed %d runners in the 4 GB left after an 8 GB reserve, want 1: %v", len(got), got)
	}
}

// onHost puts a test runner somewhere other than the helper's default host.
func onHost(r *store.Runner, hostID string) *store.Runner {
	r.HostID = hostID
	return r
}

// The case that sent an operator to this function: the wizard's placement step
// counts two Linux amd64 hosts, its review step counts one, and the 12-CPU
// machine is the one that vanished -- because docker-in-docker charges its
// sidecar too, so an 8-CPU pool costs 16 and the "8 CPU" on the pool is not a
// number anything on screen can be compared against.
func TestTheShortfallNamesTheDoubledDockerInDockerCharge(t *testing.T) {
	h := sized("host_a", 8, 12, 31*1024, 500*1024)
	p := limited("builders", 8, 0)
	p.DockerMode = store.DockerDinD

	// 11.4 rather than 12: the floor under the CPU reserve keeps a
	// twentieth of the machine for the daemon, and the shortfall names what
	// may actually be placed on, which is the figure the pass fits against.
	got := HostShortfall(h, p)
	for _, want := range []string{"11.4 CPU", "charged 16", "sidecar"} {
		if !strings.Contains(got, want) {
			t.Errorf("HostShortfall = %q, want it to mention %q", got, want)
		}
	}
	// The same pool without the sidecar fits the same machine, which is what
	// makes the doubling worth naming rather than describing as a limit.
	p.DockerMode = store.DockerNone
	if got := HostShortfall(h, p); got != "" {
		t.Errorf("HostShortfall = %q for a pool that fits", got)
	}
}

// A host that never measured itself constrains nothing, so it is placed by its
// slot count alone. That is deliberate -- it is what stops an upgrade emptying
// a fleet -- and the shortfall has to agree with it, or the wizard would name a
// size problem the scheduler does not have.
func TestAnUnmeasuredHostHasNoShortfall(t *testing.T) {
	h := testHost("host_a", 6, 0)
	p := limited("builders", 64, 512*1024)

	if got := HostShortfall(h, p); got != "" {
		t.Errorf("HostShortfall = %q for a host that has reported no size", got)
	}
}

// Disk is the dimension with no fallback and its own floor, so a pool that asks
// for more of it than a host has left is refused in the host's terms rather
// than in the pool's.
func TestTheShortfallNamesTheDiskAPoolAskedFor(t *testing.T) {
	h := sized("host_a", 8, 8, 32*1024, 40*1024)
	p := limited("archivers", 0, 0)
	p.Resources = store.Resources{DiskGB: 200}

	got := HostShortfall(h, p)
	if !strings.Contains(got, "disk") || !strings.Contains(got, "charged 200 GB") {
		t.Errorf("HostShortfall = %q, want the disk the pool asked for", got)
	}
}

// A host cut into more slots than it has runners' worth of room cannot back a
// pool that leaves its size to the host.
//
// The two halves of sizing are checked in different places, and this is what
// keeps them agreeing. A figure an operator types is refused below a quarter
// of a core; the share a host hands out was not checked at all, so a 32-slot
// capacity on an eight-core machine gave every runner 0.23 of a core -- which
// the same operator would have been refused for typing, and which the runner
// binary cannot keep up with.
//
// It is answered by refusing the host rather than by rounding the share up.
// Rounding up is over-provisioning by another name: the slots are meant to add
// up to the machine, and a floor applied to each of them adds up to more than
// there is.
func TestAHostCutTooFineCannotBackAnAutomaticPool(t *testing.T) {
	automatic := &store.Pool{Name: "zoomies-auto"}

	// Eight cores, 32 slots. Allocatable is 8 - max(0.5, 0.4) = 7.5, so each
	// slot is 0.23 -- under the floor.
	tooFine := &store.Host{
		Name: "sliced-1", Capacity: 32, CPUs: 8, MemoryMB: 65536,
	}
	if field := ShareTooSmall(tooFine, automatic); field != "cpu" {
		t.Errorf("ShareTooSmall = %q, want cpu: 32 slots on eight cores is 0.23 each", field)
	}
	if HostFits(tooFine, automatic) {
		t.Error("a host handing out 0.23 of a core per runner was accepted")
	}
	why := HostShortfall(tooFine, automatic)
	if !strings.Contains(why, "slot") || !strings.Contains(why, "0.23") {
		t.Errorf("the shortfall does not say the slot count is the cause: %q", why)
	}
	// The room agrees with the placement, or the page promises slots the pass
	// refuses.
	if room := HostRoomFor(tooFine, automatic); room.Room != 0 || room.LimitedBy != "cpu" {
		t.Errorf("room = %+v, want none, limited by cpu", room)
	}

	// The same machine at four slots is 1.87 each, which is fine.
	sensible := &store.Host{Name: "sliced-1", Capacity: 4, CPUs: 8, MemoryMB: 65536}
	if field := ShareTooSmall(sensible, automatic); field != "" {
		t.Errorf("ShareTooSmall = %q on a host handing out 1.87 of a core, want none", field)
	}
	if !HostFits(sensible, automatic) {
		t.Error("a host handing out 1.87 of a core per runner was refused")
	}

	// Memory answers for itself: 64 slots on 32 GB is 512 MB each, and 128 is
	// 256 -- under the floor with CPU to spare.
	thin := &store.Host{Name: "thin-1", Capacity: 128, CPUs: 128, MemoryMB: 32768}
	if field := ShareTooSmall(thin, automatic); field != "memory" {
		t.Errorf("ShareTooSmall = %q, want memory", field)
	}

	// A pool that names its own size answers for it at the API, so the share
	// is not its question and the floor is not applied to it here.
	fixed := &store.Pool{Name: "zoomies-fixed", Resources: store.Resources{CPUs: 2, MemoryMB: 4096}}
	if field := ShareTooSmall(tooFine, fixed); field != "" {
		t.Errorf("ShareTooSmall = %q for a pool that sets its own size, want none", field)
	}

	// A host that has measured nothing is placed by slots alone, exactly as it
	// was before any of this existed. Refusing it would empty a fleet the
	// moment it upgraded.
	unmeasured := &store.Host{Name: "old-1", Capacity: 32}
	if field := ShareTooSmall(unmeasured, automatic); field != "" {
		t.Errorf("ShareTooSmall = %q on a host that has measured nothing, want none", field)
	}
	if !HostFits(unmeasured, automatic) {
		t.Error("a host that has measured nothing was refused")
	}
}

// A runner is given its size once, and the host is charged for what it was given.
// An operator who gives a host's runners less -- which is what a suggestion for a
// host that holds fewer runners than its capacity proposes, made while work is
// running -- must not see the runners already there charged the new, smaller size:
// the host would look mostly free and the next pass would place work on top of
// runners still holding the old, larger one.
func TestARunnerIsNeverChargedLessThanItWasCreatedWith(t *testing.T) {
	h := bigHost("big")
	p := profilePool("builders", 0, 0)
	r := &store.Runner{AllocationSource: store.AllocationFromProfile, AllocatedCPUs: 3, AllocatedMemoryMB: 8 * 1024}
	if got := RunnerGuarantee(p, h, r); got.CPUs != 3 || got.MemoryMB != 8*1024 {
		t.Fatalf("a runner at the standard is charged %+v", got)
	}

	h.RunnerProfile = store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 1, MemoryMB: 2 * 1024}}
	got := RunnerGuarantee(p, h, r)
	if got.CPUs != 3 || got.MemoryMB != 8*1024 {
		t.Errorf("after the standard was lowered the live runner is charged %+v, want what it was created with", got)
	}

	// A runner with no figures on its row has nothing to say and takes the standard.
	if got := RunnerGuarantee(p, h, &store.Runner{AllocationSource: store.AllocationFromProfile}); got.CPUs != 1 || got.MemoryMB != 2*1024 {
		t.Errorf("a row with no figures is charged %+v, want the standard", got)
	}

	// Raising the standard still raises the charge: the larger figure wins.
	h.RunnerProfile = store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 4, MemoryMB: 10 * 1024}}
	if got := RunnerGuarantee(p, h, r); got.CPUs != 4 || got.MemoryMB != 10*1024 {
		t.Errorf("after the standard was raised the runner is charged %+v, want the larger", got)
	}
}

// An automatic pool has no limit of its own to lower, so the refusal that says each slot is
// too thin has to say what to set: the capacity at which a slot covers the floor. Without it
// an operator who switched a pool to docker-in-docker on a default-capacity host was left to
// find the number by trying. The figure named must be one that works.
func TestARefusedShareNamesTheCapacityThatFits(t *testing.T) {
	h := sized("host_default", 4, 8, 16*1024, 500*1024)
	p := testPool("builders", "builders")
	p.DockerMode = store.DockerDinD
	if ShareTooSmall(h, p) == "" {
		t.Fatal("the fixture must be refused for the test to mean anything")
	}
	why := HostShortfall(h, p)
	if !strings.Contains(why, "lower the host's capacity to ") {
		t.Fatalf("HostShortfall = %q, want it to name a capacity", why)
	}
	var n int
	if _, err := fmt.Sscanf(why[strings.Index(why, "capacity to ")+len("capacity to "):], "%d", &n); err != nil || n < 1 || n >= h.Slots() {
		t.Fatalf("capacity named in %q is not a lower, positive figure (%d, %v)", why, n, err)
	}
	h.Capacity = n
	if got := ShareTooSmall(h, p); got != "" {
		t.Errorf("at the named capacity %d the host is still refused (%s)", n, got)
	}
	if !HostFits(h, p) {
		t.Errorf("at the named capacity %d one runner of the pool still does not fit", n)
	}

	// A host whose whole allocatable cannot cover one slot is told nothing to set.
	tiny := sized("host_tiny", 1, 2, 4*1024, 500*1024)
	if strings.Contains(HostShortfall(tiny, p), "lower the host's capacity") {
		t.Errorf("a host at one slot was told to lower its capacity: %q", HostShortfall(tiny, p))
	}
}
