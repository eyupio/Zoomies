package controller

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// elasticDemandPercent is the share of its guarantee a runner must be using
// to be lent CPU. Whether it keeps a loan once lent is scheduler.DecideLoan's
// question, judged against the loan rather than the guarantee.
const elasticDemandPercent = 80.0

// elasticSaturatedPercent is how much of its own quota the busier half of a
// docker-in-docker pair must be using to count as pressing against its limit.
const elasticSaturatedPercent = scheduler.LoanSaturatedShare * 100

// elasticCPUTargets makes one host-wide decision from one coherent heartbeat.
// Every busy or starting runner is charged its guarantee before any spare CPU
// is lent, the idle runners one guarantee between them, and one compatible
// queued start is protected so a fast job cannot starve the next job out of
// the machine.
//
// Charging idle runners only one guarantee lets a busy runner borrow what warm
// capacity is not using, at the cost of a bounded oversubscription: if a
// second idle runner is handed a job, it may compete with a boost until the
// next heartbeat, whose plan charges it in full. The agent replaces its whole
// set of boosts from every plan, so the lent CPU is back within one heartbeat
// interval and a quota update, and a runner is never below its own quota.
func (c *Controller) elasticCPUTargets(ctx context.Context, h *store.Host, req agent.HeartbeatRequest, now time.Time) []agent.ElasticCPUDirective {
	if !c.mayAct() {
		return nil
	}
	// Nothing measured is no decision at all: there is nothing to judge the
	// host by, so nothing is recorded as if there were.
	if h == nil || !h.Usage.Fresh(now) || h.Usage.CPUPercent == nil {
		return nil
	}
	alloc := h.Allocatable()
	if !alloc.CPUsKnown || alloc.CPUs <= 0 {
		return nil
	}

	runners, err := c.st.ListRunnersForHost(ctx, h.ID)
	if err != nil {
		c.log.Warn("could not plan elastic CPU for a host", "host", h.ID, "error", err)
		return nil
	}
	// A runner between jobs, or gone, starts its next loan from nothing: an
	// idle runner's next job is a different job.
	busy := make(map[string]bool, len(runners))
	for _, r := range runners {
		if r != nil && r.State == store.RunnerBusy {
			busy[r.ID] = true
		}
	}
	c.sweepLoans(h.ID, busy)
	// This runs on every heartbeat of every host, so it stops as soon as there
	// is nothing to decide: before the fleet-wide pool read when the host runs
	// nothing, and before any plan or record when nothing here is elastic.
	if !slices.ContainsFunc(runners, func(r *store.Runner) bool { return r != nil && r.State.Live() }) {
		return nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		c.log.Warn("could not list pools while planning elastic CPU", "host", h.ID, "error", err)
		return nil
	}
	pools = c.sizingPools(pools)
	poolByID := make(map[string]*store.Pool, len(pools))
	for _, p := range pools {
		poolByID[p.ID] = p
	}
	reports := make(map[string]agent.RunnerReport, len(req.Runners))
	for _, rep := range req.Runners {
		reports[rep.RunnerID] = rep
	}

	supported := slices.Contains(req.Features, agent.FeatureElasticCPU)

	// Two ledgers of the same runners. applied is the plan the agent is sent:
	// only a runner that will actually be boosted -- an automatic pool on an
	// agent that can move a quota -- may take part in its water-fill. An
	// observe runner, or one on an agent too old to boost it, is held at its
	// guarantee there, because a share the plan hands it is a share nobody
	// uses and nobody else is lent. hypothetical lets every observing runner
	// compete, and answers only the question observe mode asks: what would a
	// boost have been?
	applied := make([]scheduler.ElasticCPUWorkload, 0, len(runners))
	hypothetical := make([]scheduler.ElasticCPUWorkload, 0, len(runners))
	poolByRunner := make(map[string]*store.Pool, len(runners))
	starting := make(map[string]int)
	lent := 0.0
	loanCodes := make(map[string]scheduler.LoanCode)
	for _, r := range runners {
		if r == nil || !r.State.Live() {
			continue
		}
		p := poolByID[r.PoolID]
		if p == nil {
			continue
		}
		if r.State == store.RunnerProvisioning || r.State == store.RunnerRegistering {
			starting[p.ID]++
		}
		base := elasticBase(p, h, r)
		poolByRunner[r.ID] = p
		if rep, ok := reports[r.ID]; ok {
			lent += lentInUse(rep.Stats, base, now)
		}
		eligible := p.Automatic() && p.CPUBurst.Observes() && (p.Backend == store.BackendDocker || p.Backend == store.BackendPodman) && r.State == store.RunnerBusy && r.AllocatedCPUs > 0
		w := scheduler.ElasticCPUWorkload{ID: r.ID, BaseCPUs: base, MaxCPUs: base, Idle: r.State == store.RunnerIdle}
		a := w
		if eligible && base > 0 {
			w.MaxCPUs = elasticCeiling(p, h, alloc.CPUs)
			if rep, ok := reports[r.ID]; ok {
				w.Demanding = elasticCPUDemanding(r, rep.Stats, base, now)
				if p.CPUBurst.Enforces() && supported {
					// Only a runner that can really be lent CPU has a loan to
					// waste; an observing one keeps the plain demand test.
					d := c.decideLoan(r, p, base, rep.Stats, w.Demanding, now)
					w.Demanding = d.Lend
					loanCodes[r.ID] = d.Code
				}
			}
			if p.CPUBurst.Enforces() && supported {
				a = w
			}
		}
		hypothetical = append(hypothetical, w)
		applied = append(applied, a)
	}

	elastic, demanding := false, false
	for _, w := range hypothetical {
		if p := poolByRunner[w.ID]; p != nil && p.CPUBurst.Observes() && w.BaseCPUs > 0 && w.MaxCPUs > w.BaseCPUs {
			elastic = true
			demanding = demanding || w.Demanding
		}
	}
	if !elastic {
		return nil
	}

	if elasticHostBusy(h, lent) {
		// A host too busy to lend is still a decision, and it is recorded as
		// one. Observe mode exists so an operator can read how often a boost
		// would happen before switching one on; a busy heartbeat that left no
		// trace would count only the calm ones, and overstate the answer.
		for _, w := range hypothetical {
			p := poolByRunner[w.ID]
			if p == nil || !p.CPUBurst.Observes() || w.BaseCPUs <= 0 || w.MaxCPUs <= w.BaseCPUs {
				continue
			}
			c.metrics.elasticCPUDecisions.WithLabelValues(p.Name, string(p.CPUBurst.Mode), "host_busy").Inc()
			c.metrics.elasticCPUFactor.WithLabelValues(p.Name, string(p.CPUBurst.Mode)).Observe(1)
		}
		return nil
	}

	// The start reserve only shapes a plan that lends something. With no
	// runner demanding, every one stays at its guarantee whatever the reserve
	// is, so the fleet-wide read of queued jobs it needs is skipped.
	reserve := 0.0
	if demanding {
		reserve = c.elasticStartReserve(ctx, h, poolByID, starting)
	}
	targets := scheduler.ElasticCPUPlan(alloc.CPUs, reserve, applied)
	would := targets
	if !slices.Equal(applied, hypothetical) {
		would = scheduler.ElasticCPUPlan(alloc.CPUs, reserve, hypothetical)
	}
	directives := make([]agent.ElasticCPUDirective, 0, len(hypothetical))
	for _, w := range hypothetical {
		p := poolByRunner[w.ID]
		if p == nil || !p.CPUBurst.Observes() || w.BaseCPUs <= 0 || w.MaxCPUs <= w.BaseCPUs {
			continue
		}
		enforced := p.CPUBurst.Enforces() && supported
		target := would[w.ID]
		if enforced {
			target = targets[w.ID]
		}
		outcome := "base"
		if target > w.BaseCPUs+0.01 {
			outcome = "burst"
		}
		switch loanCodes[w.ID] {
		case scheduler.LoanReclaimed:
			outcome = "reclaimed"
		case scheduler.LoanBackingOff:
			outcome = "backing_off"
		}
		if p.CPUBurst.Enforces() && !supported {
			outcome = "unsupported_agent"
		}
		c.metrics.elasticCPUDecisions.WithLabelValues(p.Name, string(p.CPUBurst.Mode), outcome).Inc()
		c.metrics.elasticCPUFactor.WithLabelValues(p.Name, string(p.CPUBurst.Mode)).Observe(target / w.BaseCPUs)
		if !enforced || target <= w.BaseCPUs+0.01 {
			continue
		}
		directives = append(directives, agent.ElasticCPUDirective{
			RunnerID: w.ID, CPUFactor: target / w.BaseCPUs, BaseCPUs: w.BaseCPUs,
			TargetCPUs: target, Reason: "squirrel_spotted",
		})
	}
	return directives
}

// elasticBase is the guarantee a runner is lent CPU on top of.
func elasticBase(p *store.Pool, h *store.Host, r *store.Runner) float64 {
	if p.Automatic() && r.AllocatedCPUs > 0 {
		// An automatic runner keeps the host share it was actually launched
		// with. Recomputing it after a host-capacity edit would corrupt both
		// the committed ledger and any factor sent to the agent.
		return r.AllocatedCPUs
	}
	return scheduler.Reserve(p, h).CPUs
}

// elasticCeiling is the most one runner of p may be lent up to on h: the
// pool's own ceiling and the host's (scheduler.BurstLimit, the smaller of the
// two), the host's allocatable CPU, and the daemon's core count, whichever is
// least. The last is the one easily forgotten: a Docker Desktop
// or VM daemon is smaller than the machine the agent measured, and refuses a
// quota above its own cores outright ("range of CPUs is from 0.01 to N"), so a
// boost past it would be no boost at all and a warning on every heartbeat.
func elasticCeiling(p *store.Pool, h *store.Host, allocatable float64) float64 {
	ceiling := scheduler.BurstLimit(p, h)
	if ceiling <= 0 || ceiling > allocatable {
		ceiling = allocatable
	}
	if info, ok := h.BackendInfo.Find(p.Backend); ok && info.CPUs > 0 {
		ceiling = min(ceiling, float64(info.CPUs))
	}
	return ceiling
}

// lentCPUPercent is the share of the host, in percent, that its runners were
// using out of CPU lent to them in this heartbeat. The host's admission hold
// and throttle are judged without it (see store.ObserveHostUsage).
//
// It reads the fleet only when some runner reports a boost, which is the only
// time the answer can be anything but zero.
func (c *Controller) lentCPUPercent(ctx context.Context, h *store.Host, reports []agent.RunnerReport, now time.Time) float64 {
	if h == nil || h.CPUs <= 0 || !slices.ContainsFunc(reports, func(rep agent.RunnerReport) bool { return rep.Stats.CPUAllocationFactor > 1 }) {
		return 0
	}
	runners, err := c.st.ListRunnersForHost(ctx, h.ID)
	if err != nil {
		return 0
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return 0
	}
	pools = c.sizingPools(pools)
	poolByID := make(map[string]*store.Pool, len(pools))
	for _, p := range pools {
		poolByID[p.ID] = p
	}
	byID := make(map[string]*store.Runner, len(runners))
	for _, r := range runners {
		if r != nil {
			byID[r.ID] = r
		}
	}
	lent := 0.0
	for _, rep := range reports {
		r := byID[rep.RunnerID]
		if r == nil || !r.State.Live() {
			continue
		}
		if p := poolByID[r.PoolID]; p != nil {
			lent += lentInUse(rep.Stats, elasticBase(p, h, r), now)
		}
	}
	return lent / float64(h.CPUs) * 100
}

// elasticHostBusy reports whether the host is too busy to lend CPU: throttled,
// high on CPU or load, or at its memory reserve.
//
// lent is the CPU this plan has already lent and the runners are using, and
// the CPU test is made without it. A boost is lent from the host's spare CPU,
// so a boost being used is a host whose CPU goes up -- and judged on the raw
// figure, a boost that did its job pushed the host over the line, the next
// heartbeat withdrew it, the host fell quiet, and the one after lent it again.
// Every other heartbeat, for as long as the job ran: a CPU-bound runner lent
// twice its guarantee averaged half as much again, and paid a quota change
// each time. What the line is for is CPU the plan did not account for --
// outside work, a runner with no limit, the daemon -- and that is what is
// left once the lent CPU in use is taken out.
//
// The same is why the sustained-CPU hold is not consulted here: it is decided
// on the raw figure, and a host held only because of what it was lent is the
// same flap a heartbeat later.
func elasticHostBusy(h *store.Host, lent float64) bool {
	cpu := *h.Usage.CPUPercent
	if h.CPUs > 0 {
		cpu = max(cpu-lent/float64(h.CPUs)*100, 0)
	}
	return h.Throttle.Active() || cpu >= 85 ||
		h.Usage.LoadAverage1 != nil && h.CPUs > 0 && *h.Usage.LoadAverage1 >= 2*float64(h.CPUs) ||
		h.Usage.MemoryAvailableMB != nil && *h.Usage.MemoryAvailableMB <= h.MemoryReserve()
}

// lentInUse is how much CPU a runner is using beyond its guarantee because it
// was lent it: its use above base, and never more than the boost it was given.
// A runner at or below its creation quota contributes nothing, so a runner
// with no limit that is using more than its share is left counted as the load
// it is, not written off as ours.
func lentInUse(st backend.Stats, base float64, now time.Time) float64 {
	if base <= 0 || st.CPUAllocationFactor <= 1 || st.SampledAt == nil || st.SampledAt.After(now) || now.Sub(*st.SampledAt) > store.HostUsageMaxAge {
		return 0
	}
	used := st.CPUPercent / 100
	return min(max(used-base, 0), (st.CPUAllocationFactor-1)*base)
}

func elasticCPUDemanding(r *store.Runner, current backend.Stats, base float64, now time.Time) bool {
	if current.SampledAt == nil || current.SampledAt.After(now) || now.Sub(*current.SampledAt) > store.HostUsageMaxAge {
		return false
	}
	if current.CPUPercent >= base*elasticDemandPercent {
		return true
	}
	// A docker-in-docker pair is judged by its busier half as well. The build
	// runs in the daemon, which saturates its own half of the slot while the
	// pair together reads barely half busy -- judged on the sum alone, a
	// build waited most of its run before it was lent anything.
	if current.BusiestHalfPercent >= elasticDemandPercent {
		return true
	}
	return throttlingRose(r, current)
}

// throttlingRose reports whether the cgroup's throttling counters moved since
// the runner's previous sample: the kernel saying, in its own terms, that the
// runner wanted more CPU than its quota allowed.
func throttlingRose(r *store.Runner, current backend.Stats) bool {
	var previous backend.Stats
	if len(r.ResourceSample) == 0 || json.Unmarshal(r.ResourceSample, &previous) != nil {
		return false
	}
	if previous.CPUThrottling == nil || current.CPUThrottling == nil {
		return false
	}
	return current.CPUThrottling.ThrottledPeriods > previous.CPUThrottling.ThrottledPeriods ||
		current.CPUThrottling.ThrottledNanoseconds > previous.CPUThrottling.ThrottledNanoseconds
}

// decideLoan asks the scheduler whether a busy runner of an automatic pool
// keeps, loses or may be given a loan, and remembers the answer for the next
// heartbeat. The memory lives in this process only: a restarted controller
// forgets a backoff, which costs at most one wasted loan per runner, and
// keeps the store free of a row written on every heartbeat.
func (c *Controller) decideLoan(r *store.Runner, p *store.Pool, base float64, st backend.Stats, demanding bool, now time.Time) scheduler.LoanDecision {
	if st.SampledAt == nil || st.SampledAt.After(now) || now.Sub(*st.SampledAt) > store.HostUsageMaxAge {
		// Nothing current to judge by: no loan, and nothing learned.
		return scheduler.LoanDecision{Code: scheduler.LoanNotDemanding, Reason: "not lent: no current sample"}
	}
	factor := st.CPUAllocationFactor
	if factor <= 0 {
		factor = 1
	}
	in := scheduler.LoanInput{
		Now: now, SampledAt: *st.SampledAt,
		BaseCPUs: base, LimitCPUs: base * factor, UsedCPUs: st.CPUPercent / 100,
		Saturated: throttlingRose(r, st) || st.BusiestHalfPercent >= elasticSaturatedPercent,
		Demanding: demanding,
	}
	if p.DockerMode == store.DockerDinD && r.AllocationSource == store.AllocationFromHost && st.BusiestHalfPercent > 0 {
		// The pair's loan is given to its busier half alone, so that half is
		// what is judged. A host-sized pair splits its slot evenly (see
		// store.Resources.SplitWithDaemon), and BusiestHalfPercent is that
		// half's use of its own share. An agent too old to report it leaves
		// the pair judged on its sum, as it always was.
		half, _ := store.Resources{CPUs: base}.SplitWithDaemon()
		in.HolderBaseCPUs = half.CPUs
		in.HolderUsedCPUs = st.BusiestHalfPercent / 100 * half.CPUs
	}
	c.loansMu.Lock()
	defer c.loansMu.Unlock()
	if c.loans == nil {
		c.loans = make(map[string]loanEntry)
	}
	in.Memory = c.loans[r.ID].memory
	d := scheduler.DecideLoan(in)
	c.loans[r.ID] = loanEntry{host: r.HostID, memory: d.Memory}
	if d.Code == scheduler.LoanReclaimed || d.Code == scheduler.LoanBackoffOverride {
		c.log.Info("elastic CPU loan changed", "pool", p.Name, "runner", r.ID, "decision", string(d.Code), "reason", d.Reason)
	}
	return d
}

// loanEntry is one runner's loan memory and the host it runs on, which is
// what lets a host's plan sweep entries for runners it no longer lists.
type loanEntry struct {
	host   string
	memory scheduler.LoanMemory
}

// sweepLoans drops the loan memory of every runner of host that is not busy
// in this plan. A runner that went straight from busy to gone -- every
// ephemeral runner, once its job ends -- is never seen idle, and may no
// longer be listed at all; keyed only by what the listing returned, its entry
// would outlive it for as long as the controller ran.
func (c *Controller) sweepLoans(host string, busy map[string]bool) {
	c.loansMu.Lock()
	defer c.loansMu.Unlock()
	for id, e := range c.loans {
		if e.host == host && !busy[id] {
			delete(c.loans, id)
		}
	}
}

// elasticStartReserve is the one share held back for a compatible queued job.
//
// starting counts this host's runners of each pool that are already on their
// way up. Each of those is charged its guarantee in the plan's ledger, and is
// the runner a queued job of its pool is about to land on; holding a second
// share back for the same job shrank every boost by one share during every
// start. Only a pool with more jobs queued than runners starting for it here
// still needs room kept.
func (c *Controller) elasticStartReserve(ctx context.Context, h *store.Host, pools map[string]*store.Pool, starting map[string]int) float64 {
	if intent := c.placement.Load(); intent != nil && intent.Version == c.placementVersion.Load() && c.Now().Sub(intent.At) >= 0 && c.Now().Sub(intent.At) < 30*time.Second {
		reserve := 0.0
		for id, n := range intent.Starts[h.ID] {
			if p := pools[id]; p != nil && n > starting[id] && scheduler.HostCouldRun(h, p) {
				reserve = math.Max(reserve, scheduler.Reserve(p, h).CPUs)
			}
		}
		return reserve
	}

	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return 0
	}
	waiting := make(map[string]int)
	for _, job := range queued {
		waiting[job.PoolID]++
	}
	reserve := 0.0
	for id, n := range waiting {
		p := pools[id]
		if p == nil || n <= starting[id] || !scheduler.HostCouldRun(h, p) {
			continue
		}
		reserve = math.Max(reserve, scheduler.Reserve(p, h).CPUs)
	}
	return reserve
}
