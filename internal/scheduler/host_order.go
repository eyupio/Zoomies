package scheduler

import (
	"math"

	"github.com/eyupio/zoomies/internal/store"
)

// The orders placement prefers hosts in, when several can take the runner.
// Every order is only a preference among hosts that already fit: nothing here
// makes a host eligible or ineligible, so changing it can move where runners
// go and never whether they start.
const (
	// HostOrderHeadroom picks the host with the most CPU and memory left,
	// as a fraction of what it has, after the runner is placed. It spreads
	// work, and it is what every fleet had before the setting existed, which
	// is why it is the default: changing the default would change where every
	// install without a runner profile puts its runners.
	HostOrderHeadroom = "headroom"
	// HostOrderLargestStandard picks the host whose runner of this pool is the
	// largest -- the biggest standard size, CPU first and then memory --
	// falling back to headroom between hosts that offer the same size. It is
	// the order for a fleet of unlike machines sized by profile: a heavy job
	// goes where the big runners are, and the small hosts take what the big
	// one has no room left for.
	HostOrderLargestStandard = "largest_standard"
	// HostOrderBestFit picks the host with the least left after the runner is
	// placed, so a host is filled before the next is started. It packs, which
	// leaves whole machines idle for the work that needs them -- and on a fleet
	// of unlike machines it fills the smallest first, which is the opposite of
	// what a heavy job wants.
	HostOrderBestFit = "best_fit"
)

// HostOrders is every value scheduler.host_order accepts, in the order the
// settings page offers them.
var HostOrders = []string{HostOrderHeadroom, HostOrderLargestStandard, HostOrderBestFit}

// ValidHostOrder reports whether s is a placement order. The empty string is
// headroom, so a setting that was never written is valid.
func ValidHostOrder(s string) bool {
	if s == "" {
		return true
	}
	for _, o := range HostOrders {
		if s == o {
			return true
		}
	}
	return false
}

// compareStandard orders two runner charges by size: positive when a is the
// larger, with CPU deciding first and memory second. Two charges within
// rounding of each other on a field are equal on it, so a share that did not
// divide evenly does not decide the order by a hair.
func compareStandard(a, b Reservation) int {
	switch {
	case a.CPUs > b.CPUs+cpuEpsilon:
		return 1
	case b.CPUs > a.CPUs+cpuEpsilon:
		return -1
	case a.MemoryMB > b.MemoryMB:
		return 1
	case b.MemoryMB > a.MemoryMB:
		return -1
	}
	return 0
}

// tighter is HostOrderBestFit's preference: whether h leaves less than best
// does once the runner is placed. Hosts that left the same -- or that nobody
// has measured, where only slots can say -- are told apart by the slots they
// have free, fewest first, so that a half-used host is finished before a
// fresh one is started; a tie after that keeps the host that was first, which
// is the lower ID.
func (hs *hostSet) tighter(h, best *store.Host, p *store.Pool, measured bool) bool {
	if measured {
		score, bestScore := hs.score(h, p), hs.score(best, p)
		if math.Abs(score-bestScore) > cpuEpsilon {
			return score < bestScore
		}
	}
	return hs.free[h.ID] < hs.free[best.ID]
}
