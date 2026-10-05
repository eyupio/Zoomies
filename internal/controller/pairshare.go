package controller

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

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
// host counts: a typed limit goes to both containers in full, so there is no
// division to judge.
func (c *Controller) observePair(r *store.Runner, st backend.Stats) {
	if st.Halves == nil || r.AllocationSource != store.AllocationFromHost || st.SampledAt == nil {
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

// resourceAdvice is a proposed division of one resource, CPU or memory, and the
// evidence for it. The two are judged apart because they are split apart: a pool
// whose builds are CPU in the daemon while its runner holds the memory has a
// daemon short of CPU and a runner short of memory, and one share cannot say it.
type resourceAdvice struct {
	Resource                string
	Current, Proposed       int
	DaemonHot               bool
	HotPercent, IdlePercent float64
}

// pairAdvice is what the window shows about how a slot is divided: a proposal
// for each resource it found wrong, and what it rested on.
type pairAdvice struct {
	CPU, Memory      *resourceAdvice
	Samples, Runners int
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
// work, for CPU and for memory. It is pure, like the scheduler's decisions: the
// window and nothing else.
//
// Only the samples taken at the division in force now count. The newest
// sample's division is the pool's current one, and a window that straddles an
// edit would judge the old split with the new one's name.
func judgePair(window []pairSample) (pairAdvice, bool) {
	if len(window) == 0 {
		return pairAdvice{}, false
	}
	cpuShare := func(s pairSample) int {
		d, r := s.halves.Daemon.CPULimit, s.halves.Runner.CPULimit
		if d > 0 && r > 0 {
			return int(math.Round(100 * d / (d + r)))
		}
		return 0
	}
	memShare := func(s pairSample) int {
		d, r := float64(s.halves.Daemon.MemoryLimit), float64(s.halves.Runner.MemoryLimit)
		if d > 0 && r > 0 {
			return int(math.Round(100 * d / (d + r)))
		}
		return 0
	}
	newest := window[len(window)-1]
	curCPU, curMem := cpuShare(newest), memShare(newest)
	if curCPU == 0 && curMem == 0 {
		return pairAdvice{}, false
	}
	var rCPU, dCPU, rMem, dMem []float64
	var slotCPU, slotMem float64
	runners := map[string]bool{}
	n := 0
	for _, s := range window {
		if cpuShare(s) != curCPU || memShare(s) != curMem {
			continue
		}
		n++
		runners[s.runner] = true
		rCPU, dCPU = append(rCPU, s.halves.Runner.CPUs), append(dCPU, s.halves.Daemon.CPUs)
		rMem, dMem = append(rMem, float64(s.halves.Runner.MemoryBytes)), append(dMem, float64(s.halves.Daemon.MemoryBytes))
		slotCPU = max(slotCPU, s.halves.Runner.CPULimit+s.halves.Daemon.CPULimit)
		slotMem = max(slotMem, float64(s.halves.Runner.MemoryLimit+s.halves.Daemon.MemoryLimit))
	}
	if n < pairMinSamples || len(runners) < pairMinRunners {
		return pairAdvice{}, false
	}
	// Use as a part of each container's own limit, which is what "squeezed" means.
	over := func(used []float64, limit float64) []float64 {
		out := make([]float64, len(used))
		for i, u := range used {
			if limit > 0 {
				out[i] = u / limit
			}
		}
		return out
	}
	adv := pairAdvice{Samples: n, Runners: len(runners)}
	if curCPU > 0 && slotCPU > 0 {
		rLimit, dLimit := slotCPU*float64(100-curCPU)/100, slotCPU*float64(curCPU)/100
		adv.CPU = judgeResource("CPU", curCPU, over(rCPU, rLimit), over(dCPU, dLimit), rCPU, dCPU, slotCPU)
	}
	if curMem > 0 && slotMem > 0 {
		rLimit, dLimit := slotMem*float64(100-curMem)/100, slotMem*float64(curMem)/100
		adv.Memory = judgeResource("memory", curMem, over(rMem, rLimit), over(dMem, dLimit), rMem, dMem, slotMem)
	}
	return adv, adv.CPU != nil || adv.Memory != nil
}

// judgeResource judges one resource: it is wrong when one container's 95th
// percentile use of its own limit is at least pairHot and the other's is at most
// pairIdle, and the proposal is each container's own 95th percentile use with
// headroom, as a part of the slot, rounded to five and kept in range. It has to
// move the slot toward the squeezed container by enough to matter; anything else
// is a number to second-guess for no gain.
func judgeResource(name string, current int, rUse, dUse, rUsed, dUsed []float64, slot float64) *resourceAdvice {
	rHot, dHot := pairPercentile(slices.Clone(rUse), 0.95), pairPercentile(slices.Clone(dUse), 0.95)
	adv := &resourceAdvice{Resource: name, Current: current}
	switch {
	case dHot >= pairHot && rHot <= pairIdle:
		adv.DaemonHot, adv.HotPercent, adv.IdlePercent = true, dHot*100, rHot*100
	case rHot >= pairHot && dHot <= pairIdle:
		adv.HotPercent, adv.IdlePercent = rHot*100, dHot*100
	default:
		return nil
	}
	need := func(used []float64) float64 {
		return max(pairPercentile(slices.Clone(used), 0.95)/slot*pairHeadroom, 0.05)
	}
	d, r := need(dUsed), need(rUsed)
	proposed := int(math.Round(100*d/(d+r)/5) * 5)
	proposed = min(max(proposed, store.MinDaemonSharePercent), store.MaxDaemonSharePercent)
	if adv.DaemonHot && proposed < current+pairMinMove || !adv.DaemonHot && proposed > current-pairMinMove {
		return nil
	}
	adv.Proposed = proposed
	return adv
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
		var lines, flags []string
		titles := []string{}
		for _, r := range []*resourceAdvice{adv.CPU, adv.Memory} {
			if r == nil {
				continue
			}
			hot, idle, raise, flag := "runner", "Docker sidecar", "lower", "--daemon-"+strings.ToLower(r.Resource)+"-share"
			if r.DaemonHot {
				hot, idle, raise = "Docker sidecar", "runner", "raise"
			}
			titles = append(titles, fmt.Sprintf("its %s is short of %s while the %s beside it is not", hot, r.Resource, idle))
			lines = append(lines, fmt.Sprintf("%s: the %s used at least %.0f%% of its own limit for one sample in twenty, while the %s used at most %.0f%% of its; "+
				"it is divided %d%% to the sidecar now, and %s it to about %d%%",
				r.Resource, hot, r.HotPercent, idle, r.IdlePercent, r.Current, raise, r.Proposed))
			flags = append(flags, fmt.Sprintf("%s %d", flag, r.Proposed))
		}
		// Two resources that want the same figure are one share, which is also the
		// shorter command and the one that leaves the pool a single setting.
		if adv.CPU != nil && adv.Memory != nil && adv.CPU.Proposed == adv.Memory.Proposed {
			flags = []string{fmt.Sprintf("--daemon-share %d", adv.CPU.Proposed)}
		}
		*out = append(*out, Problem{
			Code:     "pool.daemon_share_suggested",
			Severity: config.SeverityInfo,
			Title:    fmt.Sprintf("pool %s: %s", p.Name, strings.Join(titles, "; and ")),
			Detail: fmt.Sprintf("across %d samples from %d runners over the last %s. A runner and its sidecar divide one slot, CPU and memory each on their own share. %s.",
				adv.Samples, adv.Runners, pairWindow, strings.Join(lines, ". ")),
			Fix: fmt.Sprintf("change the sidecar's share in the pool editor (Size step), or zoomies pools edit %s %s, and watch the next few jobs. "+
				"It applies to runners created after the change.", p.Name, strings.Join(flags, " ")),
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}
	return nil
}
