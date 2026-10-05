package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// The controller's half of the memory valve is rules, not decisions. Each
// heartbeat it works out how much memory a host can still lend, how far each
// runner of a pool that has the valve on may go, and hands both to the agent,
// which does the lending a second at a time. The reason is the one the CPU plan
// does not share: a memory limit that is raised cannot be taken back, so there
// is no plan to carry out and replace on the next beat -- only limits to work
// within, and an agent that has to be able to act between two heartbeats.

// memoryBlockedFor is how long a runner that was refused memory keeps its pool
// and host reported as short of it. A refusal is a fact about a moment, and a
// problem raised on every one would come and go with the job; held for a while,
// it is one an operator can open before it has gone.
const memoryBlockedFor = 15 * time.Minute

// memoryHostState is what the controller last worked out about one host's
// memory valve. It is kept in memory only, for the Hosts page, the metrics and
// the problems: it is a fact about the last few heartbeats, and the next
// heartbeat that carries any rewrites it.
type memoryHostState struct {
	At      time.Time
	Pool    scheduler.MemoryPool
	FloorMB int64
	// Observing and Enforcing count the runners with a rule, by mode.
	Observing, Enforcing int
	// Supported is whether the host's agent carries the rules out.
	Supported bool
	// ShortAt is when a runner here was last reported as refused memory because
	// the host had none to give (the pool was empty, or it was down to its
	// floor), and CeilingAt when one of each pool last reached its ceiling.
	ShortAt   time.Time
	ShortCode agent.MemoryValveCode
	CeilingAt map[string]time.Time
}

// memoryState is the host's state, or the zero value for a host that has none.
func (c *Controller) memoryState(hostID string) memoryHostState {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if s := c.memoryHosts[hostID]; s != nil {
		out := *s
		out.CeilingAt = make(map[string]time.Time, len(s.CeilingAt))
		for k, v := range s.CeilingAt {
			out.CeilingAt[k] = v
		}
		return out
	}
	return memoryHostState{}
}

// memoryStates is every host's state, by host ID.
func (c *Controller) memoryStates() map[string]memoryHostState {
	c.memoryMu.Lock()
	ids := make([]string, 0, len(c.memoryHosts))
	for id := range c.memoryHosts {
		ids = append(ids, id)
	}
	c.memoryMu.Unlock()
	out := make(map[string]memoryHostState, len(ids))
	for _, id := range ids {
		out[id] = c.memoryState(id)
	}
	return out
}

// mutateMemoryState applies f to a host's state under the lock, creating it.
func (c *Controller) mutateMemoryState(hostID string, f func(*memoryHostState)) {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.memoryHosts == nil {
		c.memoryHosts = make(map[string]*memoryHostState)
	}
	s := c.memoryHosts[hostID]
	if s == nil {
		s = &memoryHostState{CeilingAt: map[string]time.Time{}}
		c.memoryHosts[hostID] = s
	}
	f(s)
}

// forgetMemoryState drops what a host's last plan worked out once it has nothing
// left for the valve to do, so the state of a host whose pools have turned it off
// does not stand for ever -- but keeps a refusal until it has aged out.
//
// The hold exists so that a problem an operator can only act on is still on the
// list when they open it. A runner that was refused memory is usually an
// ephemeral one: it finishes, is removed, and the host has no live runner at its
// next heartbeat, which is exactly when this is called. Dropping the refusal
// with the plan would have the problem clear thirty seconds after it was raised.
func (c *Controller) forgetMemoryState(hostID string, now time.Time) {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	s := c.memoryHosts[hostID]
	if s == nil {
		return
	}
	for poolID, at := range s.CeilingAt {
		if now.Sub(at) >= memoryBlockedFor {
			delete(s.CeilingAt, poolID)
		}
	}
	short := !s.ShortAt.IsZero() && now.Sub(s.ShortAt) < memoryBlockedFor
	if !short && len(s.CeilingAt) == 0 {
		delete(c.memoryHosts, hostID)
		return
	}
	kept := memoryHostState{CeilingAt: s.CeilingAt}
	if short {
		kept.ShortAt, kept.ShortCode = s.ShortAt, s.ShortCode
	}
	*s = kept
}

// dropMemoryState forgets a host that has gone, refusals and all.
func (c *Controller) dropMemoryState(hostID string) {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	delete(c.memoryHosts, hostID)
}

// valveRunnerStates are the states in which a runner has a container that can
// be watched. A provisioning runner has none yet, and a finished one has no
// memory left to lend.
func valveRunnerState(s store.RunnerState) bool {
	switch s {
	case store.RunnerRegistering, store.RunnerIdle, store.RunnerBusy, store.RunnerDraining:
		return true
	}
	return false
}

// valveSample is the memory valve's word on a runner in a report, nil for none.
func valveSample(rep agent.RunnerReport) *backend.MemoryValveSample { return rep.Stats.MemoryValve }

// elasticMemoryDirective is the rules for one host's memory valve, worked out
// from one heartbeat. It is nil when no runner on the host has a pool with the
// valve on, and when the host's agent is too old to carry the rules out -- which
// pool.elastic_memory_unsupported says, so a pool set to automatic is never
// quietly a pool that is not.
//
// The pool is what no guarantee needs: busy and starting runners are charged
// in full, idle ones by the largest between them, one compatible queued start is
// protected, the host's floor is left alone, and the smaller of what the books
// allow and what the host measured as free is the capacity (see
// scheduler.PlanMemoryPool). What a runner has been lent comes off it, and the
// figure counted is the larger of what the last report said and what this
// heartbeat's did, so a loan made a moment ago is already spent.
func (c *Controller) elasticMemoryDirective(ctx context.Context, h *store.Host, req agent.HeartbeatRequest, now time.Time) *agent.ElasticMemoryDirective {
	if !c.mayAct() || h == nil || h.MemoryMB <= 0 {
		return nil
	}
	runners, err := c.st.ListRunnersForHost(ctx, h.ID)
	if err != nil {
		c.log.Warn("could not plan elastic memory for a host", "host", h.ID, "error", err)
		return nil
	}
	if !slices.ContainsFunc(runners, func(r *store.Runner) bool { return r != nil && r.State.Live() }) {
		c.forgetMemoryState(h.ID, now)
		return nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		c.log.Warn("could not list pools while planning elastic memory", "host", h.ID, "error", err)
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

	var (
		workloads []scheduler.MemoryWorkload
		rules     []agent.ElasticMemoryRunner
		starting  = make(map[string]int)
		out       memoryHostState
	)
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
		// What the runner was launched with, not what its pool would charge for
		// one now: a pool or host edited while it lives changes the second and
		// not the first, and it is the first that is at risk and that a loan is
		// on top of.
		guarantee := scheduler.RunnerGuarantee(p, h, r).MemoryMB
		if launched, ok := scheduler.LaunchedMemoryMB(p, r); ok {
			guarantee = launched
		}
		lent := r.LentMemoryMB
		if rep, ok := reports[r.ID]; ok {
			if v := valveSample(rep); v != nil {
				lent = max(lent, v.LentBytes>>20)
			}
		}
		workloads = append(workloads, scheduler.MemoryWorkload{
			ID: r.ID, GuaranteeMB: guarantee, LentMB: lent, Idle: r.State == store.RunnerIdle,
		})
		if !p.MemoryBurst.Observes() || !valveRunnerState(r.State) || r.AllocatedMemoryMB <= 0 || guarantee <= 0 {
			continue
		}
		if p.Backend != store.BackendDocker && p.Backend != store.BackendPodman {
			continue
		}
		ceiling := scheduler.MemoryCeiling(p, h, guarantee)
		if ceiling <= guarantee {
			continue
		}
		rules = append(rules, agent.ElasticMemoryRunner{
			RunnerID: r.ID, Mode: p.MemoryBurst.Mode, GuaranteeMB: guarantee, CeilingMB: ceiling, SpillMB: p.MemoryBurst.SpillMB,
		})
		if p.MemoryBurst.Enforces() {
			out.Enforcing++
		} else {
			out.Observing++
		}
	}
	if len(rules) == 0 {
		c.forgetMemoryState(h.ID, now)
		return nil
	}

	var startReserve int64
	if len(rules) > 0 {
		startReserve = c.elasticStartReserve(ctx, h, poolByID, starting).MemoryMB
	}
	in := scheduler.MemoryPoolInput{
		TotalMB: h.MemoryMB, FloorMB: scheduler.MemoryPoolFloor(h),
		StartReserveMB: startReserve, Workloads: workloads,
	}
	if h.Usage.Fresh(now) {
		in.AvailableMB = h.Usage.MemoryAvailableMB
	}
	plan := scheduler.PlanMemoryPool(in)
	supported := slices.Contains(req.Features, agent.FeatureElasticMemory)

	out.At, out.Pool, out.FloorMB, out.Supported = now, plan, in.FloorMB, supported
	c.mutateMemoryState(h.ID, func(s *memoryHostState) {
		out.ShortAt, out.ShortCode, out.CeilingAt = s.ShortAt, s.ShortCode, s.CeilingAt
		if out.CeilingAt == nil {
			out.CeilingAt = map[string]time.Time{}
		}
		*s = out
	})
	c.observeMemoryReports(ctx, h, runners, poolByID, reports, supported, now)
	if !supported {
		return nil
	}
	return &agent.ElasticMemoryDirective{CapacityMB: plan.CapacityMB, FloorMB: in.FloorMB, Runners: rules}
}

// observeMemoryReports counts what the agent reported of each runner with a rule
// this heartbeat: a decision count per pool, mode and outcome, which is what
// observe mode exists to produce, and the times a pool's runners were last
// refused memory, which is what the problems read.
func (c *Controller) observeMemoryReports(ctx context.Context, h *store.Host, runners []*store.Runner, pools map[string]*store.Pool, reports map[string]agent.RunnerReport, supported bool, now time.Time) {
	for _, r := range runners {
		if r == nil || !valveRunnerState(r.State) {
			continue
		}
		p := pools[r.PoolID]
		if p == nil || !p.MemoryBurst.Observes() {
			continue
		}
		mode := string(p.MemoryBurst.Mode)
		outcome := "unreported"
		switch rep, ok := reports[r.ID]; {
		case !supported:
			outcome = "unsupported_agent"
		case ok && valveSample(rep) != nil:
			outcome = valveOutcome(valveSample(rep).Code)
		}
		c.metrics.elasticMemoryDecisions.WithLabelValues(p.Name, mode, outcome).Inc()
		switch agent.MemoryValveCode(outcome) {
		case agent.MemoryPoolEmpty, agent.MemoryHostFloor:
			// Only a pool that lends has refused anything. An observing pool's
			// runners draw on a pool its own virtual loans drain, and "it would have
			// been refused" is evidence on the runner and in the decision counts, not
			// a host that ran out of memory and a runner that "was not given it".
			if p.MemoryBurst.Enforces() {
				c.mutateMemoryState(h.ID, func(s *memoryHostState) { s.ShortAt, s.ShortCode = now, agent.MemoryValveCode(outcome) })
			}
		case agent.MemoryAtCeiling:
			c.mutateMemoryState(h.ID, func(s *memoryHostState) { s.CeilingAt[p.ID] = now })
		}
	}
}

// memoryValveMoved reports whether a new valve sample changes what a runner's
// memory view says, against the one stored from the heartbeat before: the loan,
// the swap, the latest decision, the mode and whether it has come near a limit.
// Only these move the view, so only these are published.
func memoryValveMoved(previous json.RawMessage, current *backend.MemoryValveSample) bool {
	var prev backend.Stats
	if len(previous) > 0 {
		_ = json.Unmarshal(previous, &prev)
	}
	was := prev.MemoryValve
	switch {
	case was == nil && current == nil:
		return false
	case was == nil || current == nil:
		return true
	}
	return was.Mode != current.Mode || was.Code != current.Code || was.LentBytes != current.LentBytes ||
		was.SpillBytes != current.SpillBytes || was.WouldLendBytes != current.WouldLendBytes ||
		was.WouldSpillBytes != current.WouldSpillBytes || was.NearLimit != current.NearLimit
}

// noteMemoryValve records what a runner's report said about the valve: the loan
// onto its row, where the placement ledger reads it, and the first time it came
// near a limit onto the near-limit count. It reports whether anything the
// runner's view shows changed.
//
// The loan is written when it grows and never when it shrinks: a limit only ever
// goes up while a runner lives, so a smaller figure is an older report overtaken
// by a newer one, and writing it would hand a runner's memory to the next
// placement as room.
func (c *Controller) noteMemoryValve(ctx context.Context, r *store.Runner, rep agent.RunnerReport) bool {
	v := valveSample(rep)
	moved := memoryValveMoved(r.ResourceSample, v)
	if v == nil {
		return moved
	}
	if lent := v.LentBytes >> 20; lent > r.LentMemoryMB {
		if err := c.st.SetRunnerLentMemory(ctx, r.ID, lent); err != nil {
			c.log.Warn("could not record the memory lent to a runner", "runner", r.ID, "error", err)
		} else {
			c.log.Info("a runner was lent memory", "runner", r.ID, "name", r.Name, "lent_mb", lent, "was_mb", r.LentMemoryMB)
			r.LentMemoryMB = lent
			moved = true
		}
	}
	if v.NearLimit && moved {
		var prev backend.Stats
		if len(r.ResourceSample) > 0 {
			_ = json.Unmarshal(r.ResourceSample, &prev)
		}
		if prev.MemoryValve == nil || !prev.MemoryValve.NearLimit {
			if p, err := c.st.GetPool(ctx, r.PoolID); err == nil {
				// The mode is the pool's, which the controller knows, and not the
				// agent's word for it: a label is a series for every value it takes.
				c.metrics.elasticMemoryNearLimit.WithLabelValues(p.Name, string(p.MemoryBurst.Mode)).Inc()
			}
		}
	}
	return moved
}

// memoryValveProblems says what the valve was refused, as far as the last few
// minutes show. Both are held for memoryBlockedFor after the last refusal and
// then clear by themselves: a refusal is a fact about a moment, but one an
// operator can only act on if it is still on the list when they open it.
//
// They are the only place an operator learns that the valve ran dry: the runner
// that was refused says so in its own view, and nothing else adds those up.
func (c *Controller) memoryValveProblems(ctx context.Context, out *[]Problem) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	poolByID := make(map[string]*store.Pool, len(pools))
	for _, p := range pools {
		poolByID[p.ID] = p
		// A pool the valve is on for, placed on a host whose agent cannot carry
		// it out, is a pool that is quietly not doing what it says. It is on the
		// pool's own page already; it is here too because the operator who
		// switched a pool to observe and is waiting a month for the evidence
		// reads the problems list, not each pool.
		if !p.Enabled || !p.MemoryBurst.Observes() {
			continue
		}
		// poolRoom and not PoolRoom: all that is wanted is which hosts can carry
		// the valve out, and the whole answer also works out the folder plan and
		// the sidecar presets, which is several reads of the hosts for each pool
		// on every pass of the problems.
		room, err := c.poolRoom(ctx, p)
		if err != nil {
			return fmt.Errorf("counting the room pool %s has: %w", p.Name, err)
		}
		if w, ok := heldWithoutMemoryValve(p, room.Placeable()); ok {
			*out = append(*out, w)
		}
	}

	states := c.memoryStates()
	if len(states) == 0 {
		return nil
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	now := c.Now()
	reached := map[string][]string{}
	for _, h := range hosts {
		s, ok := states[h.ID]
		if !ok {
			continue
		}
		if !s.ShortAt.IsZero() && now.Sub(s.ShortAt) < memoryBlockedFor {
			why := "had no spare memory left to lend"
			if s.ShortCode == agent.MemoryHostFloor {
				why = "was down to the memory it keeps free for itself"
			}
			since := s.ShortAt
			*out = append(*out, Problem{
				Code:     "host.memory_pool_exhausted",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("host %s ran out of memory to lend its runners", h.Name),
				Detail: fmt.Sprintf("a runner here needed more memory than it was created with, and the host %s: %s of %s was lent out and %s held back. "+
					"The runner was not given it, so a job that needed it was left to the limit it had.",
					why, humanMB(s.Pool.LentMB), humanMB(h.MemoryMB), humanMB(s.FloorMB)),
				Fix: "a host lends only memory that no runner's share needs, so this one is short because its slots add up to its machine: " +
					"lower its capacity or give its runners a smaller share, or put the memory-hungry pool on a larger host.",
				TargetKind: "host",
				TargetID:   h.ID,
				Since:      &since,
			})
		}
		for poolID, at := range s.CeilingAt {
			if now.Sub(at) < memoryBlockedFor {
				reached[poolID] = append(reached[poolID], h.Name)
			}
		}
	}
	for poolID, hostNames := range reached {
		p := poolByID[poolID]
		if p == nil {
			continue
		}
		slices.Sort(hostNames)
		*out = append(*out, Problem{
			Code:     "pool.memory_ceiling_reached",
			Severity: config.SeverityInfo,
			Title:    fmt.Sprintf("pool %s: a job reached the most memory a runner may be lent", p.Name),
			Detail: "a runner of this pool held all it is allowed -- its own share and what the valve may add to it -- and still wanted more, on " +
				strings.Join(hostNames, ", ") + ".",
			Fix: fmt.Sprintf("raise the pool's memory ceiling -- Memory ceiling in the pool editor, or zoomies pools edit %s --memory-burst-max <MB> -- "+
				"or give each runner more to start with. A host's own ceiling lowers this one and never raises it.", p.Name),
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}
	return nil
}

// memoryValveEpilogue is what a kill sentence adds when the memory valve was in
// play: that it had lent the runner something and it was not enough, or that it
// was on and could not lend, and why. The question an operator asks of a kill on
// a pool that has the valve is "why did it not save this job?", and the runner's
// own record is the only place that is answered.
//
// It is empty for a runner the valve had nothing to do with, so a pool that does
// not use it reads exactly as before.
func memoryValveEpilogue(p *store.Pool, r *store.Runner) string {
	var sample backend.Stats
	if len(r.ResourceSample) > 0 {
		_ = json.Unmarshal(r.ResourceSample, &sample)
	}
	v := sample.MemoryValve
	lent := r.LentMemoryMB
	if v != nil {
		lent = max(lent, v.LentBytes>>20)
	}
	switch {
	case lent > 0:
		// All of what it was created with, which for a typed docker-in-docker pair
		// is both containers' and not the one figure on the row.
		created := r.AllocatedMemoryMB
		if launched, ok := scheduler.LaunchedMemoryMB(p, r); ok {
			created = launched
		}
		out := fmt.Sprintf(" The memory valve had lent it %s on top of its %s, so it held %s when it was killed",
			humanMB(lent), humanMB(created), humanMB(created+lent))
		if v != nil && blockedCode(v.Code) && v.Reason != "" {
			out += " -- " + v.Reason
		}
		return out + "."
	case v != nil && v.Mode == string(store.MemoryBurstAutomatic) && blockedCode(v.Code) && v.Reason != "":
		return " The memory valve was on for this pool and lent it nothing: " + v.Reason + "."
	case v != nil && v.Mode == string(store.MemoryBurstObserve):
		return " The memory valve was only observing for this pool, so it lent nothing; it would have given the runner up to " +
			humanMB(v.WouldLendBytes>>20) + " more."
	}
	return ""
}
