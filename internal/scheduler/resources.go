package scheduler

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/store"
)

// Reservation is what one runner of a pool is charged against the host it is
// placed on. It is the admission half of the resource model: the reporting
// half taught agents to measure their machines, and this is what decides that
// the ninth 4 GB runner does not go on a 32 GB box.
//
// It is a charge, not an enforcement. What actually binds a runner is the
// cgroup limit the container backend applies from the same pool `resources`,
// and the process backend applies none at all -- so a reservation says what
// the fleet has promised away, and only the container backends make the
// promise true.
type Reservation struct {
	CPUs     float64
	MemoryMB int64
	DiskMB   int64
}

// cpuEpsilon absorbs the rounding of a share that does not divide evenly. Eight
// slots on a 30-CPU host are 3.75 each, which is exact, but seven are not, and
// a host must not refuse the last runner it has room for because the eighth
// subtraction left 2.7755575615628914e-16 of a CPU owing.
const cpuEpsilon = 1e-6

// comfortableRunnerCPUs and comfortableRunnerMemoryMB are what a container
// needs to do real work without crawling through a build, rather than merely
// enough to avoid being killed. They match the figures
// controller.overprovisionedSlotMemoryMB and its CPU counterpart judge a
// plain runner's slot against, so the two warnings never disagree about what
// "comfortable" means.
const (
	comfortableRunnerCPUs     = 1.0
	comfortableRunnerMemoryMB = 2048
)

// Reserve is what one runner of p costs on h.
//
// It is pure, and it is per field. A pool that sets a memory limit and no CPU
// limit has said something about memory and nothing about CPU, so the limit
// answers for memory and the fallback answers for CPU; charging zero for the
// half the operator left alone would let a fleet promise away a machine it had
// measured.
//
// The fallback is the host's own allocatable share per slot, and it is chosen
// so that nothing changes on upgrade: a host carrying only pools with no
// limits charges each runner exactly one slot's worth, so its capacity is what
// admits work, exactly as it did before any of this existed.
//
// Disk has no fallback. Free disk is a measurement rather than a budget, and
// dividing what is left into shares would turn every fill of the cache into a
// refusal to place work that had always placed. What guards it instead is the
// reserve floor: a pool that asks for disk is charged it, and a host at or
// below its reserve takes nothing.
//
// A docker-in-docker runner is two containers, and what it is charged follows
// what the two are given. A limit an operator typed says what the job may
// have; the daemon is given the same, so the pair is charged twice, and a host
// too small for what was typed says so. A fallback share is the machine cut
// into slots, and there the pair splits one slot between them -- see
// store.Resources.SplitWithDaemon -- so it is charged one.
//
// A pool that takes its size from the host (SizeFromProfile) has the host's
// standard size where an automatic pool has the share: the profile's, or the
// fleet's default where the host's names none, and one slot's worth to split
// between a docker-in-docker pair exactly as the share is. The host's minimum
// is applied first (sizedOn), so every figure below follows the larger of the
// pool's minimum and the host's.
//
// Both halves of that have to stay true together. Charging one share while
// giving each container a whole one is the bug this once had: a host read as
// half committed while its containers' quotas added up to every core it had,
// the daemon lost the reserve the arithmetic said was being kept for it, and
// creates timed out on a machine the fleet believed was idle. Giving the pair
// one share while charging two is the mirror of it, and is what an operator
// meets as a fleet that will not use the machines it has: eight slots that
// hold four runners, a page promising room the pass refuses, and an
// overcommit warning whose advice -- fewer slots -- makes it worse every time
// it is taken, down to one slot holding nothing at all.
func Reserve(p *store.Pool, h *store.Host) Reservation {
	p = sizedOn(p, h)
	alloc := h.Allocatable()
	res := Reservation{
		CPUs:     p.Resources.CPUs,
		MemoryMB: p.Resources.MemoryMB,
		DiskMB:   p.Resources.DiskGB * 1024,
	}
	cpuFromHost, memoryFromHost := res.CPUs <= 0, res.MemoryMB <= 0
	// A minimum above the slot share is a slot of the minimum's size: the
	// runner is given it and charged it, and the host holds fewer runners
	// than its slots rather than none -- see MinimumSlot.
	floor := MinimumSlot(p)
	standardCPUs, standardMemoryMB := StandardSize(p, h)
	if cpuFromHost {
		slot := share(alloc.CPUs, h.Slots())
		if standardCPUs > 0 {
			slot = standardCPUs
		}
		res.CPUs = max(slot, floor.CPUs)
	}
	if memoryFromHost {
		slot := int64(share(float64(alloc.MemoryMB), h.Slots()))
		if standardMemoryMB > 0 {
			slot = standardMemoryMB
		}
		res.MemoryMB = max(slot, floor.MemoryMB)
	}
	if p.DockerMode == store.DockerDinD {
		// Only what the operator typed doubles. A share is one slot, and the
		// pair is given one slot between them.
		if !cpuFromHost {
			res.CPUs *= 2
		}
		if !memoryFromHost {
			res.MemoryMB *= 2
		}
		// Disk has no fallback, so a figure here is always one somebody typed.
		res.DiskMB *= 2
	}
	return res
}

// RunnerCharge is what one live runner is charged on its host: its guarantee
// (RunnerGuarantee) and, on top of it, any memory the memory valve has lent it.
//
// The loan is charged because it cannot be taken back. A runner's limit only
// ever goes up while it lives, so memory lent to it is memory the host no
// longer has for the next one, and a ledger that forgot it would place new work
// into room that is already in use.
func RunnerCharge(p *store.Pool, h *store.Host, r *store.Runner) Reservation {
	res := RunnerGuarantee(p, h, r)
	if r != nil && r.LentMemoryMB > 0 {
		res.MemoryMB += r.LentMemoryMB
	}
	return res
}

// RunnerGuarantee is what one live runner was promised: what its row says it
// was given, where it was given less than its pool's standard size, and the
// larger of that and the pool's own charge otherwise.
//
// The row is the only place a reduced runner's size is written down. Charging
// it the standard instead would have the fleet believe a host it had filled
// with reduced runners was over-committed, and refuse the next one it had room
// for; charging the standard is right for every other runner that was given it.
//
// But the standard is today's, and a runner is given its size once. An operator
// who gives a host's runners less -- the change a suggestion proposes on a host
// that holds fewer runners than its capacity -- would see every runner already
// there charged the new, smaller size, the host look mostly free, and the next
// pass place work on top of runners still holding the old, larger one. So a runner
// is never charged less than its row says it was created with. A row with no
// figure (the process backend, limits off, a row from before they were kept) has
// nothing to say and is charged the standard, as it always was.
func RunnerGuarantee(p *store.Pool, h *store.Host, r *store.Runner) Reservation {
	res := Reserve(p, h)
	if r == nil {
		return res
	}
	reduced := r.AllocationSource == store.AllocationReduced || r.AllocationSource == store.AllocationHistory
	if r.AllocatedCPUs > 0 {
		given := r.AllocatedCPUs * fieldFactor(p, p.Resources.CPUs > 0)
		if reduced {
			res.CPUs = given
		} else {
			res.CPUs = max(res.CPUs, given)
		}
	}
	if r.AllocatedMemoryMB > 0 {
		given := r.AllocatedMemoryMB * int64(fieldFactor(p, p.Resources.MemoryMB > 0))
		if reduced {
			res.MemoryMB = given
		} else {
			res.MemoryMB = max(res.MemoryMB, given)
		}
	}
	return res
}

// fieldFactor is how many containers one field of a runner's size is given
// for: two for a docker-in-docker runner's typed figure, because the daemon is
// given the same limit, and one for a field taken from the host, which is one
// slot the pair shares -- the same distinction Reserve draws.
func fieldFactor(p *store.Pool, typed bool) float64 {
	if typed && p.DockerMode == store.DockerDinD {
		return 2
	}
	return 1
}

// MinimumReserve is the least one runner of p may be charged on h: the pool's
// minimum on each field that has one below its standard, and the standard on
// every other field. It is Reserve for a pool with no minimum.
//
// A field the pool leaves to the host has this host's slot share as its
// standard, raised to the minimum where the share is smaller (MinimumSlot), so
// the minimum is where a reduction stops and never a way up past the
// standard. And an automatic docker-in-docker slot is never cut below
// what it takes to split between a runner and its daemon (ShareFloor), which
// is the same line ShareTooSmall holds a whole slot to.
func MinimumReserve(p *store.Pool, h *store.Host) Reservation {
	p = sizedOn(p, h)
	res := Reserve(p, h)
	if !p.Resources.Reducible() {
		return res
	}
	floor := ShareFloor(p)
	if m := p.Resources.MinCPUs; m > 0 {
		typed := p.Resources.CPUs > 0
		v := m * fieldFactor(p, typed)
		if !typed {
			v = max(v, floor.CPUs)
		}
		res.CPUs = min(res.CPUs, v)
	}
	if m := p.Resources.MinMemoryMB; m > 0 {
		typed := p.Resources.MemoryMB > 0
		v := m * int64(fieldFactor(p, typed))
		if !typed {
			v = max(v, floor.MemoryMB)
		}
		res.MemoryMB = min(res.MemoryMB, v)
	}
	return res
}

// ReducedSize is what a runner of p is given on a host with only left to
// spare, where that is less than the pool's standard: as much of the standard
// as there is, never below the minimum. It returns the charge against the host
// and the limits each container is created with, and false when even the
// minimum does not fit.
//
// It gives the most the host can spare rather than the minimum, because the
// minimum is a floor the operator will accept, not a size they asked for: a
// 30 GB machine under a 32 GB pool with a 24 GB minimum should run the job
// with 30, not 24. CPU is rounded down to a hundredth of a core, so the charge
// never exceeds what is left.
//
// A field the pool leaves to the host is given explicitly here -- the share,
// or what is left of it -- because the runner is created at exactly what it
// is charged: a reduced runner's row is the only record of its size.
func ReducedSize(p *store.Pool, h *store.Host, left Reservation, known store.HostAllocation) (Reservation, store.Resources, bool) {
	p = sizedOn(p, h)
	floor := MinimumReserve(p, h)
	if !p.Resources.Reducible() || !fits(left, floor, known) {
		return Reservation{}, store.Resources{}, false
	}
	charge := Reserve(p, h)
	cpuFactor := fieldFactor(p, p.Resources.CPUs > 0)
	memFactor := int64(fieldFactor(p, p.Resources.MemoryMB > 0))
	if known.CPUsKnown && left.CPUs+cpuEpsilon < charge.CPUs && floor.CPUs < charge.CPUs {
		each := math.Floor(left.CPUs/cpuFactor*100+cpuEpsilon) / 100
		charge.CPUs = max(each*cpuFactor, floor.CPUs)
	}
	if known.MemoryKnown && left.MemoryMB < charge.MemoryMB && floor.MemoryMB < charge.MemoryMB {
		charge.MemoryMB = max(left.MemoryMB/memFactor*memFactor, floor.MemoryMB)
	}
	if !fits(left, charge, known) {
		return Reservation{}, store.Resources{}, false
	}
	grant := p.Resources
	grant.MinCPUs, grant.MinMemoryMB = 0, 0
	grant.CPUs = charge.CPUs / cpuFactor
	grant.MemoryMB = charge.MemoryMB / memFactor
	return charge, grant, true
}

// share is one slot's worth of a host-wide figure.
func share(total float64, capacity int) float64 {
	if capacity < 1 || total <= 0 {
		return 0
	}
	return total / float64(capacity)
}

// Reserved is what the live runners already on h have promised away.
//
// It is the same sum newHostSet seeds a pass with, and it is exported because
// the Hosts page has to show it: an operator can see what a machine is and
// what it has left, and without this cannot see what the fleet has committed
// on it -- which is the number that explains why a host with free slots is
// taking nothing.
//
// Only live rows count, exactly as the slot count does: the snapshot keeps
// failed runners so the Runners page can show them, and charging a host for a
// runner that is gone would shrink the fleet every time one failed.
//
// Disk is deliberately absent. Free disk is a measurement of the filesystem as
// it is now, so what the runners already there have written is in the figure
// already; adding their reservations to it would charge the same bytes twice.
func Reserved(h *store.Host, pools []*store.Pool, runners map[string][]*store.Runner) Reservation {
	var out Reservation
	if h == nil {
		return out
	}
	for _, p := range pools {
		if p == nil {
			continue
		}
		for _, r := range runners[p.ID] {
			if r == nil || r.HostID != h.ID || !r.State.Live() {
				continue
			}
			res := RunnerCharge(p, h, r)
			out.CPUs += res.CPUs
			out.MemoryMB += res.MemoryMB
		}
	}
	return out
}

// HostFits reports whether one runner of the pool would fit on an empty host:
// whether this is the size of machine the pool could ever run on, before
// anything running on it is counted.
//
// Room left on the host is separate, and is the scheduler's own accounting
// during a pass -- the same split HostCanRun already makes for slots.
func HostFits(h *store.Host, p *store.Pool) bool {
	if ExcludedBySize(h, p) != nil {
		return false
	}
	p = sizedOn(p, h)
	if ShareTooSmall(h, p) != "" {
		return false
	}
	alloc := h.Allocatable()
	whole := Reservation{CPUs: alloc.CPUs, MemoryMB: alloc.MemoryMB, DiskMB: alloc.DiskMB}
	// A machine too small for the standard size can still take a runner at
	// the pool's minimum, which is what a minimum is for.
	return fits(whole, MinimumReserve(p, h), alloc)
}

// ShareTooSmall names the field whose slot share is below what a runner needs
// to be a runner, and is empty when the host can back this pool.
//
// It exists because the two halves of sizing are checked in different places
// and must not disagree. A figure an operator types is refused below a quarter
// of a core or 512 MB, because underneath those the runner binary is killed
// before it takes a job or crawls through one in a way that reads as a broken
// image. A pool that leaves its size to the host is given the host's own
// share, and nothing was checking that -- so a 32-slot capacity on an
// eight-core machine handed every runner 0.23 of a core, which the same
// operator would have been refused for typing.
//
// It is answered by refusing the placement rather than by rounding the share
// up. Rounding up is over-provisioning by another name: the whole point of the
// share is that the slots add up to the machine, and a floor applied to each
// of them adds up to more than there is. The host is cut into more slots than
// it has runners' worth of room, and the fix is the capacity, which
// HostShortfall says.
//
// A pool with a minimum on the field is the exception, and not a rounding:
// its operator said the least a runner may have, so a thinner share is raised
// to that and the runner is charged it (MinimumSlot). The charge is what keeps
// that honest -- the host admits as many runners as its machine covers, not as
// many as it has slots -- which is exactly what rounding without one did not.
//
// Only a pool that left the size to the host is asked, and only on a field the
// agent has measured: a fixed size answers for itself at the API, and a host
// that has measured nothing is placed by slots alone exactly as it was before
// any of this existed.
func ShareTooSmall(h *store.Host, p *store.Pool) string {
	// A host an operator paused (capacity zero) is full, not too small: a typed pool
	// is told so, and an automatic one divided the machine by no slots and read the
	// answer as a share of nothing -- which reads as "no host could ever run this" to
	// whatever decides to rent another machine.
	if h == nil || p == nil || !p.Automatic() || h.Capacity <= 0 {
		return ""
	}
	p = sizedOn(p, h)
	alloc := h.Allocatable()
	share := slotSize(p, h)
	// A docker-in-docker runner is two containers sharing one slot, so the
	// slot has to carry two runners' worth of floor rather than one. Refusing
	// the host here is what keeps the split honest: a slot that cannot be
	// divided without starving the runner is not a slot this pool can use, and
	// saying so names the capacity to change -- where dividing it anyway would
	// hand the daemon whatever was left, which on a small enough slot is
	// nothing at all, and no limit is how a build takes the machine.
	//
	// A field with a minimum is never too small: its runners are given the
	// minimum where the share is less (MinimumSlot), so the host holds fewer
	// of them rather than none.
	need := ShareFloor(p)
	given := MinimumSlot(p)
	if alloc.CPUsKnown && given.CPUs <= 0 && share.CPUs < need.CPUs {
		return "cpu"
	}
	if alloc.MemoryKnown && given.MemoryMB <= 0 && share.MemoryMB < need.MemoryMB {
		return "memory"
	}
	return ""
}

// MinimumSlot is, on each field an automatic pool has a minimum for, the least
// one of its slots is given: the minimum per container, twice over for a
// runner that shares its slot with a Docker daemon. It is zero on a field with
// no minimum, and on a pool that typed its own size.
//
// A slot's share smaller than this is raised to it rather than refused. The
// minimum is the operator saying the least a runner of the pool may have, and
// a share under it used to leave the host out altogether -- so a runner killed
// for want of memory could not be given more by raising the minimum, only by
// lowering the host's capacity, which is a setting on another page for every
// pool on the host. The runner is charged what it is given, so the host
// admits as many of them as its machine covers and no more: slots and room
// are both honoured, and whichever runs out first binds.
//
// A share above the minimum is still what the runner is given. The minimum is
// the floor, never the size: a runner is given all of the slot it lands in.
func MinimumSlot(p *store.Pool) Reservation {
	if p == nil || !p.Automatic() {
		return Reservation{}
	}
	floor := ShareFloor(p)
	var out Reservation
	if p.Resources.MinCPUs > 0 {
		out.CPUs = floor.CPUs
	}
	if p.Resources.MinMemoryMB > 0 {
		out.MemoryMB = floor.MemoryMB
	}
	return out
}

// ShareFloor is the least one slot may be worth on a host this pool can use:
// what a runner needs to be one, or -- where the runner brings a daemon into
// the same slot -- what each of the two needs to keep up with its own half of
// the job, doubled into one figure for the slot they share.
//
// The two containers of a defaulted docker-in-docker pair are not judged by
// the bare minimum that only keeps a runner from being killed
// (store.MinRunnerCPUs, store.MinRunnerMemoryMB): stability over performance
// means each half has to clear what any runner needs to do real work, the
// same comfortableRunnerCPUs and comfortableRunnerMemoryMB a plain runner's
// slot is judged against. A slot too thin for that is refused rather than
// split into two containers that will not keep up -- see
// TestASplitTooThinForBothContainersIsRefused -- which trades density for a
// daemon that answers its creates.
//
// A minimum the operator typed replaces it on its field. The comfortable
// figure is a judgement made for a pool nobody sized; a pool whose operator
// said "never less than 1 GB a container" has been sized, and refusing a host
// whose share gives each container 1.4 GB would be overruling them -- which is
// how an automatic docker-in-docker pool with a minimum used to sit queued
// beside a 3 GB machine that could run it. The minimum is per container, so a
// pair's slot needs it twice; and it is never taken below what keeps a runner
// alive, which the API already refuses to store.
func ShareFloor(p *store.Pool) Reservation {
	floor := comfortFloor(p)
	if p == nil || !p.Automatic() {
		return floor
	}
	cpuPair, memPair := 1.0, 1.0
	if p.DockerMode == store.DockerDinD {
		cpuPair, memPair = p.Resources.CPUPairFactor(), p.Resources.MemoryPairFactor()
	}
	if m := p.Resources.MinCPUs; m > 0 && p.Resources.CPUs <= 0 {
		floor.CPUs = max(m, store.MinRunnerCPUs) * cpuPair
	}
	if m := p.Resources.MinMemoryMB; m > 0 && p.Resources.MemoryMB <= 0 {
		floor.MemoryMB = int64(math.Ceil(float64(max(m, store.MinRunnerMemoryMB)) * memPair))
	}
	return floor
}

// comfortFloor is ShareFloor for a pool nobody gave a minimum: the bare
// minimum for one runner, or a comfortable figure for each of a pair.
func comfortFloor(p *store.Pool) Reservation {
	if p != nil && p.DockerMode == store.DockerDinD && p.Automatic() {
		return Reservation{
			CPUs:     comfortableRunnerCPUs * p.Resources.CPUPairFactor(),
			MemoryMB: int64(math.Ceil(float64(comfortableRunnerMemoryMB) * p.Resources.MemoryPairFactor())),
		}
	}
	return Reservation{CPUs: store.MinRunnerCPUs, MemoryMB: store.MinRunnerMemoryMB}
}

// fits is the one comparison, so that the empty-host question and the
// room-left-during-a-pass question can never drift apart. A figure the agent
// never measured constrains nothing: an agent that predates the fields places
// by slots alone, which is what keeps an upgrade from emptying a fleet.
func fits(left, want Reservation, known store.HostAllocation) bool {
	if known.DiskKnown {
		// A host at or below its disk reserve takes no runner at all, however
		// little the pool asks for. The reserve is the room the machine needs
		// to do the work -- an image layer, a checkout, a log -- and not a
		// budget to be spent down to nothing.
		if known.DiskMB <= 0 || left.DiskMB < want.DiskMB {
			return false
		}
	}
	if known.CPUsKnown && left.CPUs+cpuEpsilon < want.CPUs {
		return false
	}
	if known.MemoryKnown && left.MemoryMB < want.MemoryMB {
		return false
	}
	return true
}

// HostReduction says when a host can run p, but gives its runners less than
// the pool would get on a larger machine: an automatic pool whose share there
// is below the comfortable size and above the minimum its operator set, or a
// fixed pool whose standard does not fit and whose minimum does. An automatic
// pool whose minimum is above the share is said too: its runners there are
// given the minimum, and the host holds fewer of them than it has slots. It is
// empty otherwise.
//
// It is information, not a warning. The minimum is the operator saying what
// they will accept, and a runner at it is the pool working as configured; the
// sentence is there so the smaller runners on that host are not a surprise.
func HostReduction(h *store.Host, p *store.Pool) string {
	if h == nil || p == nil || !HostFits(h, p) {
		return ""
	}
	p = sizedOn(p, h)
	alloc := h.Allocatable()
	if p.Automatic() {
		// A pool that takes its size from the host is given the size the host
		// names, which is what its operator chose for the machine: there is
		// no thinner-than-usual share to apologise for.
		if p.SizeFromProfile {
			return ""
		}
		share := HostShare(h)
		comfort := comfortFloor(p)
		per := ""
		if p.DockerMode == store.DockerDinD {
			per = ", split between the runner and its Docker daemon"
		}
		// A share under the minimum is raised to it, so the runners there are
		// not smaller -- the host holds fewer of them. That is the sentence,
		// because a host running three of an eight-slot pool is otherwise a
		// puzzle.
		given := MinimumSlot(p)
		switch {
		case alloc.MemoryKnown && given.MemoryMB > share.MemoryMB:
			return fmt.Sprintf("its slot share is %s of memory, less than this pool's minimum, so its runners there are given %s%s and it holds fewer of them than its %s",
				formatMB(share.MemoryMB), formatMB(given.MemoryMB), per, plural(h.Slots(), "slot"))
		case alloc.CPUsKnown && given.CPUs > share.CPUs+cpuEpsilon:
			return fmt.Sprintf("its slot share is %s CPU, less than this pool's minimum, so its runners there are given %s CPU%s and it holds fewer of them than its %s",
				formatCPUs(share.CPUs), formatCPUs(given.CPUs), per, plural(h.Slots(), "slot"))
		case alloc.MemoryKnown && share.MemoryMB < comfort.MemoryMB:
			return fmt.Sprintf("its slot share is %s of memory%s, less than the %s this pool's runners get room to work in on a larger machine; they run there at that share, above this pool's minimum",
				formatMB(share.MemoryMB), per, formatMB(comfort.MemoryMB))
		case alloc.CPUsKnown && share.CPUs < comfort.CPUs:
			return fmt.Sprintf("its slot share is %s CPU%s, less than the %s this pool's runners get room to work in on a larger machine; they run there at that share, above this pool's minimum",
				formatCPUs(share.CPUs), per, formatCPUs(comfort.CPUs))
		}
		return ""
	}
	if !p.Resources.Reducible() {
		return ""
	}
	whole := Reservation{CPUs: alloc.CPUs, MemoryMB: alloc.MemoryMB, DiskMB: alloc.DiskMB}
	if fits(whole, Reserve(p, h), alloc) {
		return ""
	}
	return "it is smaller than this pool's standard size, so its runners there get what the machine can spare -- never less than this pool's minimum"
}

// HostShortfall says why one runner of p could not fit on an empty h, in terms
// an operator can act on: what the machine has to place on, and what this pool
// is charged for a runner. It is empty when the runner would fit, so it is
// also the sentence for "why is this host not in the count".
//
// The charge is the number worth spelling out, because it is the one nothing
// on the pool shows. A pool that asks for 8 CPU and gives its jobs Docker in
// Docker is charged 16 on every host -- the sidecar gets the same limits -- so
// a 12-CPU machine refuses it, and an operator reading the pool's own "8 CPU"
// against a "12 vCPU" host card has no way to see why.
func HostShortfall(h *store.Host, p *store.Pool) string {
	// The host's own profile first: it is a limit two things an operator wrote
	// disagree about, and the sentence has to name both rather than send them
	// to lower a limit that was never the problem.
	if ex := ExcludedBySize(h, p); ex != nil {
		return ex.Reason
	}
	p = sizedOn(p, h)
	// The share first, because it is a different fix from every case below:
	// nothing about the pool is wrong, and sending an operator to lower a
	// limit it does not set would send them to the wrong screen.
	if field := ShareTooSmall(h, p); field != "" {
		share := slotSize(p, h)
		need := ShareFloor(p)
		dind := p.DockerMode == store.DockerDinD
		pair := ""
		if dind {
			pair = ", because this pool's runners share their slot with a Docker daemon"
		}
		if p.SizeFromProfile {
			// The size is the host's standard, not a share of its slots, so
			// there is no slot count to change: the standard is.
			standard := "the fleet's default of "
			if h.RunnerProfile.Standard.Sized() {
				standard = "its standard runner of "
			}
			if field == "cpu" {
				return fmt.Sprintf("%s%s CPU is below the %s a runner needs to keep up with its own job%s -- raise the host's standard runner size",
					standard, formatCPUs(share.CPUs), formatCPUs(need.CPUs), pair)
			}
			return fmt.Sprintf("%s%s is below the %s a runner needs before it is killed before taking a job%s -- raise the host's standard runner size",
				standard, formatMB(share.MemoryMB), formatMB(need.MemoryMB), pair)
		}
		switch field {
		case "cpu":
			return fmt.Sprintf("it is set to %s, which divides its %s allocatable CPU into shares of %s each, and a runner needs at least %s to keep up with its own job%s%s",
				plural(h.Slots(), "slot"), formatCPUs(h.Allocatable().CPUs),
				formatCPUs(share.CPUs), formatCPUs(need.CPUs), pair, capacityThatFits(h, need))
		default:
			return fmt.Sprintf("it is set to %s, which divides its %s of allocatable memory into shares of %s each, and a runner is killed before it takes a job below %s%s%s",
				plural(h.Slots(), "slot"), formatMB(h.Allocatable().MemoryMB),
				formatMB(share.MemoryMB), formatMB(need.MemoryMB), pair, capacityThatFits(h, need))
		}
	}
	alloc := h.Allocatable()
	left := Reservation{CPUs: alloc.CPUs, MemoryMB: alloc.MemoryMB, DiskMB: alloc.DiskMB}
	want := MinimumReserve(p, h)
	if fits(left, want, alloc) {
		return ""
	}
	dind := p.DockerMode == store.DockerDinD
	switch {
	case alloc.DiskKnown && alloc.DiskMB <= 0:
		// The reserve floor, not a limit anyone typed. Sending an operator to
		// lower the pool's disk request would be sending them to the wrong
		// screen: this host refuses every pool until space is freed on it.
		return "its work directory is at or below its disk reserve, so it takes no runner of any pool"
	case alloc.DiskKnown && left.DiskMB < want.DiskMB:
		return fmt.Sprintf("it has %s of disk free to place on, and one runner of this pool is charged %s%s",
			formatMB(left.DiskMB), formatMB(want.DiskMB), charged(p.Resources.DiskGB > 0, dind))
	case alloc.CPUsKnown && left.CPUs+cpuEpsilon < want.CPUs:
		return fmt.Sprintf("it has %s CPU to place on, and one runner of this pool is charged %s%s",
			formatCPUs(left.CPUs), formatCPUs(want.CPUs), charged(p.Resources.CPUs > 0, dind)+minimumCharged(p, "cpu"))
	case alloc.MemoryKnown && left.MemoryMB < want.MemoryMB:
		return fmt.Sprintf("it has %s of memory to place on, and one runner of this pool is charged %s%s",
			formatMB(left.MemoryMB), formatMB(want.MemoryMB), charged(p.Resources.MemoryMB > 0, dind)+minimumCharged(p, "memory"))
	}
	return "it is too small for this pool's limits"
}

// charged explains the one case where the number an operator sees on the pool
// is not the number the host is charged. A defaulted field needs no such
// sentence even though it doubles too: its doubled share is capped at the
// machine it is being compared against, so it can never be what a host is too
// small for.
func charged(set, dind bool) string {
	if set && dind {
		return ", twice what it asks for, because a docker-in-docker runner is charged for its sidecar too"
	}
	return ""
}

// minimumCharged names the pool's own minimum where it is what a runner of an
// automatic pool is charged, because that is a figure the operator set and can
// lower -- and nothing else in the sentence says where the charge came from.
func minimumCharged(p *store.Pool, field string) string {
	floor := MinimumSlot(p)
	pair := ""
	if p.DockerMode == store.DockerDinD {
		pair = ", twice over for a runner and its Docker daemon, which share one slot"
	}
	switch {
	case field == "cpu" && floor.CPUs > 0:
		return fmt.Sprintf(", this pool's minimum of %s CPU a container%s", formatCPUs(max(p.Resources.MinCPUs, store.MinRunnerCPUs)), pair)
	case field == "memory" && floor.MemoryMB > 0:
		return fmt.Sprintf(", this pool's minimum of %s a container%s", formatMB(max(p.Resources.MinMemoryMB, store.MinRunnerMemoryMB)), pair)
	}
	return ""
}

// FormatCPUs is formatCPUs for the problems that quote a host's share, so the
// figure an operator reads there is rendered by the one function that renders
// it everywhere else.
func FormatCPUs(v float64) string { return formatCPUs(v) }

// FormatMB is formatMB for the sentences outside this package that quote a
// size, for the reason FormatCPUs exists.
func FormatMB(mb int64) string { return formatMB(mb) }

// formatCPUs writes a CPU count the way the pool form takes one: whole where
// it is whole, and a share where the host's capacity did not divide evenly.
// Two decimals is where a share stops being worth reading -- the rounding this
// absorbs is the same one cpuEpsilon exists for.
func formatCPUs(v float64) string {
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// formatMB writes a size in the unit an operator would say it in.
func formatMB(mb int64) string {
	if mb >= 1024 && mb%1024 == 0 {
		return strconv.FormatInt(mb/1024, 10) + " GB"
	}
	if mb >= 1024 {
		return strconv.FormatFloat(float64(mb)/1024, 'f', 1, 64) + " GB"
	}
	return strconv.FormatInt(mb, 10) + " MB"
}

// HostRoom is how many runners of p an empty h could hold, and which of the
// machine's figures ran out first.
//
// It is the arithmetic behind "this host has room for four of these", and it
// is the scheduler's rather than the browser's for the same reason HostFits
// is: the charge a runner carries is not the figure on the pool -- a
// docker-in-docker slot is charged for its sidecar too, and an unmeasured
// field constrains nothing -- and a count worked out from the pool's own
// numbers would disagree with where the fleet actually places runners.
//
// The slot count is part of the answer, not a separate one: a host set to six
// slots holds six runners however much machine is left over, and a host whose
// slots outrun its machine is the overload this function exists to show. Both
// are returned, so a caller can say which is binding.
type HostRoom struct {
	// Slots is what the operator set -- or, where the host has a standard
	// runner size, what its machine holds of that, up to the capacity -- less
	// any active throttle: the ceiling the scheduler would honour whatever
	// else the machine could take.
	Slots int
	// Fits is how many runners of this size the machine itself has room for,
	// ignoring the slot count. It is zero on a host too small for one.
	Fits int
	// Room is the smaller of the two, which is what the pool can actually
	// expect from this host.
	Room int
	// LimitedBy names what ran out: "slots", "cpu", "memory", "disk", or ""
	// where the host has measured nothing and only its slots bind. It is
	// "profile" where the host's runner profile keeps the pool off it
	// altogether (ExcludedBySize).
	LimitedBy string
}

// HostRoomFor counts the room on an empty host. Live runners are deliberately
// not subtracted: this answers "how big is this machine, in runners of this
// pool", which is the question a pool's size and a host's capacity are chosen
// against, and it does not change every time a job starts.
func HostRoomFor(h *store.Host, p *store.Pool) HostRoom {
	out := HostRoom{Slots: h.EffectiveCapacity()}
	// A host whose runner profile keeps the pool off it holds none of it, and
	// says so before any arithmetic, which would otherwise count runners of a
	// size this pool will never be given there.
	if ExcludedBySize(h, p) != nil {
		out.Fits, out.Room, out.LimitedBy = 0, 0, "profile"
		return out
	}
	p = sizedOn(p, h)
	// A host cut into shares too small to run a runner holds none of this
	// pool, whatever its slot count says. Reporting the slot count here would
	// promise room the pass refuses.
	if field := ShareTooSmall(h, p); field != "" {
		out.Fits, out.Room, out.LimitedBy = 0, 0, field
		return out
	}
	alloc := h.Allocatable()
	// The same gate fits() applies: a host at or below its disk reserve takes
	// no runner of any pool, so a pool that asks for no disk still has no room
	// here. Skipping it left the pool page and pool.max_above_room counting
	// runners the pass will never place.
	if alloc.DiskKnown && alloc.DiskMB <= 0 {
		out.Fits, out.Room, out.LimitedBy = 0, 0, "disk"
		return out
	}
	want := Reserve(p, h)

	fits := -1
	limit := ""
	consider := func(n int, by string) {
		if fits == -1 || n < fits {
			fits, limit = n, by
		}
	}
	if alloc.CPUsKnown && want.CPUs > 0 {
		consider(int((alloc.CPUs+cpuEpsilon)/want.CPUs), "cpu")
	}
	if alloc.MemoryKnown && want.MemoryMB > 0 {
		consider(int(alloc.MemoryMB/want.MemoryMB), "memory")
	}
	// Disk only where the pool asks for it. Free disk is a measurement rather
	// than a budget -- see Reserve -- so dividing what is left into runners
	// would put a machine's cache on the pool's account.
	if alloc.DiskKnown && want.DiskMB > 0 {
		consider(int(alloc.DiskMB/want.DiskMB), "disk")
	}
	// A pool with a minimum runs one more runner than the standard count
	// wherever what is left after the standard ones still covers the minimum,
	// because that is what the pass places: the standard wherever it fits, and
	// then one runner given all the host can spare. Only one -- a reduced
	// runner takes the whole remainder, so there is never a second behind it.
	// Counting the standard alone called a host the pool runs on one it cannot
	// use; counting every runner at the minimum promised runners the pass
	// never makes.
	if fits >= 0 && MinimumReserve(p, h) != want {
		n := float64(fits)
		rest := Reservation{
			CPUs:     alloc.CPUs - n*want.CPUs,
			MemoryMB: alloc.MemoryMB - int64(fits)*want.MemoryMB,
			DiskMB:   alloc.DiskMB - int64(fits)*want.DiskMB,
		}
		if _, _, ok := ReducedSize(p, h, rest, alloc); ok {
			fits++
		}
	}
	if fits == -1 {
		// Nothing measured: the host places by slots alone, exactly as it did
		// before any of this existed.
		out.Fits, out.Room, out.LimitedBy = out.Slots, out.Slots, ""
		return out
	}
	out.Fits = max(fits, 0)
	out.Room = min(out.Fits, out.Slots)
	out.LimitedBy = limit
	if out.Slots < out.Fits {
		out.LimitedBy = "slots"
	}
	return out
}

// capacityThatFits is the sentence that names the capacity at which each slot of h
// covers need, or "" when there is none to name. The refusal says what is wrong with
// the split; an automatic pool has no limit of its own to lower, so without the number
// an operator is left to find the capacity by trying -- and lowering it is the only
// change that makes the host usable. A host whose whole allocatable does not cover one
// slot, or that is already at the capacity named, is told nothing here.
func capacityThatFits(h *store.Host, need Reservation) string {
	a := h.Allocatable()
	n := math.MaxInt
	if a.CPUsKnown && need.CPUs > 0 {
		n = min(n, int(math.Floor(a.CPUs/need.CPUs+1e-9)))
	}
	if a.MemoryKnown && need.MemoryMB > 0 {
		n = min(n, int(a.MemoryMB/need.MemoryMB))
	}
	if n < 1 || n >= h.Slots() || n == math.MaxInt {
		return ""
	}
	return fmt.Sprintf(" -- lower the host's capacity to %d, which gives each slot that much", n)
}
