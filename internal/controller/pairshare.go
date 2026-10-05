package controller

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"time"
)

// A docker-in-docker runner is two containers dividing one slot, and the right
// division is a fact about the jobs, not the machine: a pool whose builds all
// run in the daemon wants most of the slot there, and one that compiles in the
// runner wants the opposite. Nothing on the host can tell which -- only what
// each container actually used can -- so the controller keeps a window of that
// per pool and says when the division is visibly wrong. It only says: the
// split is a pool setting (resources.daemon_share_percent), and an operator,
// who knows what the pool is for, makes the change.

const (
	// pairWindow is how far back a pool's samples count. A day's jobs would hide
	// a change of workload; an hour could be one unusual build.
	pairWindow = 6 * time.Hour
	// pairMaxSamples bounds the window per pool. Heartbeats arrive every few
	// seconds per busy runner, so this is well over the window for any pool this
	// controller serves, and a bound keeps a busy fleet's memory flat.
	pairMaxSamples = 4000
	// pairMinSamples and pairMinRunners keep one long job from speaking for a
	// pool: its samples are all the same job, and a pool's advice should rest on
	// several.
	pairMinSamples = 60
	pairMinRunners = 3
	// pairHot is a container's use of its own limit, at its 95th percentile,
	// above which it is the one being squeezed; pairIdle is the most the other
	// may use for the squeeze to be the division's fault rather than the job's.
	pairHot  = 0.85
	pairIdle = 0.50
	// pairHeadroom is what a container is proposed above what it used, so a
	// proposal is not a limit the job would sit exactly at.
	pairHeadroom = 1.25
	// pairMinMove is the least change worth proposing, in percentage points.
	pairMinMove = 10
)

// pairSample is one heartbeat of one busy pair.
type pairSample struct {
	at     time.Time
	runner string
	halves backend.PairHalves
}

// observePair records a busy runner's two halves. Only a runner sized from the
// host counts, by its share or by its profile's standard size: a typed limit
// goes to both containers in full, so there is no division to judge.
func (c *Controller) observePair(r *store.Runner, st backend.Stats) {
	if st.Halves == nil || !store.SizedByHost(r.AllocationSource) || st.SampledAt == nil {
		return
	}
	h := *st.Halves
	if !(h.Runner.CPULimit > 0 || h.Runner.MemoryLimit > 0) || !(h.Daemon.CPULimit > 0 || h.Daemon.MemoryLimit > 0) {
		return
	}
	now := c.Now()
	c.pairMu.Lock()
	defer c.pairMu.Unlock()
	if c.pairs == nil {
		c.pairs = make(map[string][]pairSample)
	}
	w := append(c.pairs[r.PoolID], pairSample{at: now, runner: r.ID, halves: h})
	cut := 0
	for cut < len(w) && now.Sub(w[cut].at) > pairWindow {
		cut++
	}
	w = w[cut:]
	if len(w) > pairMaxSamples {
		w = w[len(w)-pairMaxSamples:]
	}
	c.pairs[r.PoolID] = w
}

// pairAdvice is a proposed division and the evidence for it.
type pairAdvice struct {
	Current, Proposed       int
	DaemonHot               bool
	HotPercent, IdlePercent float64
	Samples, Runners        int
}

// utilisation is a container's use of its own limit: the larger of its CPU and
// memory fractions, because either one binding squeezes it.
func (u halfView) utilisation() float64 {
	var out float64
	if u.cpuLimit > 0 {
		out = u.cpus / u.cpuLimit
	}
	if u.memLimit > 0 {
		out = max(out, float64(u.mem)/float64(u.memLimit))
	}
	return out
}

type halfView struct {
	cpus, cpuLimit float64
	mem, memLimit  int64
}

func view(h backend.HalfUse) halfView {
	return halfView{h.CPUs, h.CPULimit, h.MemoryBytes, h.MemoryLimit}
}

// pairPercentile is the p-th (0..1) value of xs, which it sorts.
func pairPercentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	return xs[min(int(math.Ceil(p*float64(len(xs))))-1, len(xs)-1)]
}

// judgePair says whether a window of samples shows the slot divided against the
// work. It is pure, like the scheduler's decisions: the window and nothing else.
//
// Only the samples taken at the division in force now count. The newest
// sample's division is the pool's current one, and a window that straddles an
// edit would judge the old split with the new one's name.
func judgePair(window []pairSample) (pairAdvice, bool) {
	if len(window) == 0 {
		return pairAdvice{}, false
	}
	share := func(s pairSample) int {
		d, r := s.halves.Daemon, s.halves.Runner
		if d.CPULimit > 0 && r.CPULimit > 0 {
			return int(math.Round(100 * d.CPULimit / (d.CPULimit + r.CPULimit)))
		}
		if d.MemoryLimit > 0 && r.MemoryLimit > 0 {
			return int(math.Round(100 * float64(d.MemoryLimit) / float64(d.MemoryLimit+r.MemoryLimit)))
		}
		return 0
	}
	current := share(window[len(window)-1])
	if current == 0 {
		return pairAdvice{}, false
	}
	var rUse, dUse, rCPU, dCPU, rMem, dMem []float64
	runners := map[string]bool{}
	var slotCPU, slotMem float64
	for _, s := range window {
		if share(s) != current {
			continue
		}
		runners[s.runner] = true
		rUse = append(rUse, view(s.halves.Runner).utilisation())
		dUse = append(dUse, view(s.halves.Daemon).utilisation())
		rCPU, dCPU = append(rCPU, s.halves.Runner.CPUs), append(dCPU, s.halves.Daemon.CPUs)
		rMem, dMem = append(rMem, float64(s.halves.Runner.MemoryBytes)), append(dMem, float64(s.halves.Daemon.MemoryBytes))
		slotCPU = max(slotCPU, s.halves.Runner.CPULimit+s.halves.Daemon.CPULimit)
		slotMem = max(slotMem, float64(s.halves.Runner.MemoryLimit+s.halves.Daemon.MemoryLimit))
	}
	if len(rUse) < pairMinSamples || len(runners) < pairMinRunners {
		return pairAdvice{}, false
	}
	rHot, dHot := pairPercentile(slices.Clone(rUse), 0.95), pairPercentile(slices.Clone(dUse), 0.95)
	adv := pairAdvice{Current: current, Samples: len(rUse), Runners: len(runners)}
	switch {
	case dHot >= pairHot && rHot <= pairIdle:
		adv.DaemonHot, adv.HotPercent, adv.IdlePercent = true, dHot*100, rHot*100
	case rHot >= pairHot && dHot <= pairIdle:
		adv.HotPercent, adv.IdlePercent = rHot*100, dHot*100
	default:
		return pairAdvice{}, false
	}
	// What each container needs, as a part of the slot: its 95th percentile use
	// with headroom, on whichever of CPU and memory takes more of the slot.
	need := func(cpu, mem []float64) float64 {
		var n float64
		if slotCPU > 0 {
			n = pairPercentile(slices.Clone(cpu), 0.95) / slotCPU
		}
		if slotMem > 0 {
			n = max(n, pairPercentile(slices.Clone(mem), 0.95)/slotMem)
		}
		return max(n*pairHeadroom, 0.05)
	}
	d, r := need(dCPU, dMem), need(rCPU, rMem)
	proposed := int(math.Round(100*d/(d+r)/5) * 5)
	proposed = min(max(proposed, store.MinDaemonSharePercent), store.MaxDaemonSharePercent)
	// The proposal has to move the slot toward the squeezed container, by enough
	// to matter; anything else is a number to second-guess for no gain.
	if adv.DaemonHot && proposed < current+pairMinMove || !adv.DaemonHot && proposed > current-pairMinMove {
		return pairAdvice{}, false
	}
	adv.Proposed = proposed
	return adv, true
}

// daemonShareAdviceProblems is the standing advice on how a pool divides its
// slot. Recomputed every pass, so it stays while the window shows it and clears
// itself when the split is changed, or the work is.
func (c *Controller) daemonShareAdviceProblems(ctx context.Context, out *[]Problem) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	for _, p := range pools {
		// A pool the controller keeps divides its slot evenly and has no setting
		// for it: the advice would send an operator to an edit that is refused, and
		// one that was accepted would be put back with the rest of the pool's fixed
		// settings. They make a pool of their own where the split matters.
		if !p.Enabled || p.DockerMode != store.DockerDinD || p.FromHosts() {
			continue
		}
		c.pairMu.Lock()
		window := slices.Clone(c.pairs[p.ID])
		c.pairMu.Unlock()
		adv, ok := judgePair(window)
		if !ok {
			continue
		}
		hot, idle, raise := "runner", "Docker sidecar", "lower"
		if adv.DaemonHot {
			hot, idle, raise = "Docker sidecar", "runner", "raise"
		}
		*out = append(*out, Problem{
			Code:     "pool.daemon_share_suggested",
			Severity: config.SeverityInfo,
			Title:    fmt.Sprintf("pool %s: its %s is short of room while the %s beside it is not", p.Name, hot, idle),
			Detail: fmt.Sprintf("across %d samples from %d runners over the last %s, the %s used at least %.0f%% of its own limit for one sample in twenty, "+
				"while the %s used at most %.0f%% of its. A runner and its sidecar divide one slot, and it is divided %d%% to the sidecar now.",
				adv.Samples, adv.Runners, pairWindow, hot, adv.HotPercent, idle, adv.IdlePercent, adv.Current),
			Fix: fmt.Sprintf("%s the sidecar's share to about %d%% -- Docker sidecar's share in the pool editor, or zoomies pools edit %s --daemon-share %d -- "+
				"and watch the next few jobs. It applies to runners created after the change.", raise, adv.Proposed, p.Name, adv.Proposed),
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}
	return nil
}
