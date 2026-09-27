package scheduler

import (
	"math"

	"github.com/eyupio/zoomies/internal/store"
)

// Allocation is what one runner of p is limited to when it is created on h,
// and where those limits came from.
//
// It is the enforcement half of the resource model, and it closes the gap the
// charge left open. Reserve charges a pool that sets no limit one slot's share
// of the host, so the books balance -- and until now the runner itself was
// given no limit at all, so the books balanced while eight runners each took
// every core on the machine. A pool's own limits win where it set them; where
// it did not, the runner is given exactly what it was charged, as a cgroup
// limit the container backends apply.
//
// It is per field, as Reserve is, and for the same reason: a pool that sets
// memory and leaves CPU alone has said something about memory and nothing
// about CPU. Disk and the pids limit pass through untouched -- disk has no
// share to give (it is a measurement, not a budget) and a pids limit is not a
// thing the machine's size says anything about.
//
// A docker-in-docker runner's sidecar is given what this function hands back
// as spec.Resources, and what that is depends on where the figure came from.
// A limit an operator typed is given to both containers in full -- the build
// runs in the daemon, so it needs the same figure the runner was promised --
// and Reserve charges the host for both. A defaulted figure is one slot's
// share, and store.Resources.SplitWithDaemon divides it between the pair
// before either container is created, because a slot is one runner however
// many containers it takes to run it; Reserve charges one share to match.
//
// A slot too small to give both halves what a runner needs is refused rather
// than divided -- see ShareFloor -- because handing the daemon whatever is
// left over is no limit at all, which is how one build takes the machine.
//
// A default is only given where it would bind. The process backend applies no
// limit at all, so a defaulted figure on one of its runners would be a number
// on the Runners page saying the opposite of the truth. And a daemon that
// cannot apply a CPU quota -- rootless Docker on a host that delegates only
// memory and pids to the user -- refuses the container rather than ignoring
// the request, so a default sent there would fail every create on the host.
// The host's own probe says what its daemon can do, and a probe from an agent
// too old to say is read as "nothing", which is what every host did before.
//
// An automatic pool with a minimum above the slot share is given the minimum
// instead -- see MinimumSlot -- because a share smaller than the least the
// operator said a runner may have is a runner set up to be killed.
//
// With defaults off the pool's limits are the whole answer, which is what
// every fleet had before this existed.
func Allocation(p *store.Pool, h *store.Host, defaults bool) (store.Resources, string) {
	res := p.Resources
	source := ""
	if res.CPUs > 0 || res.MemoryMB > 0 {
		source = store.AllocationFromPool
	}
	if !defaults || h == nil || p.Backend == store.BackendProcess {
		return res, source
	}
	info, _ := h.BackendInfo.Find(p.Backend)
	limits := hostLimits(h, p.Backend)
	alloc := h.Allocatable()
	def := HostShare(h)
	// A share under the pool's minimum is raised to it, which is what Reserve
	// charges: the minimum is the least a runner is given, never the most.
	floor := MinimumSlot(p)
	def.CPUs = max(def.CPUs, floor.CPUs)
	def.MemoryMB = max(def.MemoryMB, floor.MemoryMB)
	if res.CPUs <= 0 && alloc.CPUsKnown && limits.CPU && def.CPUs > 0 {
		// The share is a request to the daemon, and a daemon refuses a quota
		// above its own core count whatever the agent's machine has: a
		// Docker Desktop VM on a ten-core laptop has four. The host is sized
		// by its daemon, so this is a belt on braces, for a row whose size
		// and probe disagree.
		if info.CPUs > 0 && def.CPUs > float64(info.CPUs) {
			def.CPUs = float64(info.CPUs)
		}
		res.CPUs = def.CPUs
		source = store.AllocationFromHost
	}
	if res.MemoryMB <= 0 && alloc.MemoryKnown && limits.Memory && def.MemoryMB > 0 {
		res.MemoryMB = def.MemoryMB
		source = store.AllocationFromHost
	}
	return res, source
}

// HostShare is one slot's share of a host: the CPU and memory a runner of a
// pool that sets neither is given by Allocation, before the host's daemon is
// asked whether it can apply them. Zero on a field the host has not measured.
//
// It is exported so that the problem which warns about an over-provisioned
// host can say what each runner's default share would be in the same figures
// Allocation hands out, rather than in a second sum that could drift from it.
func HostShare(h *store.Host) store.Resources {
	if h == nil {
		return store.Resources{}
	}
	alloc := h.Allocatable()
	var out store.Resources
	if alloc.CPUsKnown {
		out.CPUs = shareCPUs(alloc.CPUs, h.Capacity)
	}
	if alloc.MemoryKnown {
		out.MemoryMB = int64(share(float64(alloc.MemoryMB), h.Capacity))
	}
	return out
}

// DefaultsBind reports which default limits would bind on this host at all:
// whether any available container backend on it has said it can apply a CPU
// quota, and a memory limit. It is what a problem about the host asks before
// it names "each runner's default share", because on a host offering only
// the process backend, or one whose daemon cannot apply the limit, or one
// whose agent has not said, the share is never given and a sentence naming
// it would say the opposite of the truth.
func DefaultsBind(h *store.Host) (cpu, memory bool) {
	for _, info := range h.BackendInfo {
		if !info.Available || info.Kind == store.BackendProcess || !info.Limits.Known {
			continue
		}
		cpu = cpu || info.Limits.CPU
		memory = memory || info.Limits.Memory
	}
	return cpu, memory
}

// hostLimits is what the host's daemon for this backend said it can enforce.
// Unknown is every field false, which defaults nothing.
func hostLimits(h *store.Host, kind store.BackendKind) store.LimitSupport {
	info, ok := h.BackendInfo.Find(kind)
	if !ok || !info.Limits.Known {
		return store.LimitSupport{}
	}
	return info.Limits
}

// shareCPUs is one slot's worth of CPU, floored to the hundredth the pool
// form takes limits in. Floored rather than rounded: three runners rounded
// up from 0.667 to 0.67 would together be promised a hundredth of a core the
// host does not have, and the daemon refuses a quota above the machine.
func shareCPUs(total float64, capacity int) float64 {
	return math.Floor(share(total, capacity)*100) / 100
}
