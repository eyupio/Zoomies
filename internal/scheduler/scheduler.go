// Package scheduler decides how many runners each pool should have.
//
// Decide is a pure function: it takes a snapshot of the fleet and returns a
// plan. It reads no clock, opens no database and performs no I/O, so the whole
// scaling behaviour of the product is reproducible in a table test and
// explainable in the UI -- every action carries the sentence that justified it,
// in the operator's words rather than in counters they have to interpret.
//
// The caller gathers the snapshot, executes the plan and records the reasons.
package scheduler

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// Snapshot is everything the scheduler needs to decide, gathered by the caller.
type Snapshot struct {
	// Now is the decision time. It is a field rather than a clock read so that
	// a test can place a runner exactly one second either side of a timeout.
	Now time.Time
	// Pools is every pool, enabled or not; a disabled pool still gets drained.
	Pools []*store.Pool
	// Runners holds the non-removed runners of each pool, keyed by pool ID.
	Runners map[string][]*store.Runner
	// Jobs holds queued and in-progress jobs. Only queued jobs create demand:
	// an in-progress job is already represented by the busy runner running it.
	Jobs []*store.Job
	// ActiveByRepository and QueuedByRepository make repository fair-share
	// state explicit at the scheduler boundary rather than hiding database reads
	// inside the policy engine.
	ActiveByRepository map[string]int
	QueuedByRepository map[string]int
	// Hosts is every registered agent host, with ActiveRunners filled in.
	Hosts  []*store.Host
	Policy Policy
}

// Policy carries the tunables from config.Scheduler.
//
// Every duration here is disabled by a zero or negative value, so a caller that
// leaves a field unset gets "no timeout" instead of a fleet that reaps itself.
type Policy struct {
	// ScaleUpDelay is how long a job must have been queued before it counts as
	// demand, which damps churn when jobs arrive in bursts.
	ScaleUpDelay time.Duration
	// MaxRunnerLifetime drains a runner that has lived this long, which catches
	// runners wedged by a hung job.
	MaxRunnerLifetime time.Duration
	// ProvisionTimeout fails a runner that never finished registering.
	ProvisionTimeout time.Duration
	// MaxCreatesPerTick caps creates across the whole fleet in one pass, so a
	// thundering herd of queued jobs cannot fill every host at once. Pools are
	// shared fairly among pools at the same priority; zero means no cap.
	MaxCreatesPerTick int
}

// ActionKind is what the caller must do to a runner.
type ActionKind string

const (
	// ActionCreate asks for a new runner on a named host.
	ActionCreate ActionKind = "create"
	// ActionDrain asks a runner to finish its current job and stop. It is only
	// ever emitted for a runner that is not busy.
	ActionDrain ActionKind = "drain"
	// ActionRemove tears down a runner that is already dead.
	ActionRemove ActionKind = "remove"
	// ActionFail marks a runner that never came up as failed, so that the
	// failure is visible instead of the pool silently sitting one short.
	ActionFail ActionKind = "fail"
)

// Action is one change the caller should make to the fleet.
type Action struct {
	Kind     ActionKind `json:"kind"`
	PoolID   string     `json:"pool_id"`
	PoolName string     `json:"pool_name"`
	// RunnerID is empty for ActionCreate, which has no runner yet.
	RunnerID string `json:"runner_id,omitempty"`
	// HostID is the host chosen for an ActionCreate, empty otherwise.
	HostID string `json:"host_id,omitempty"`
	// Reason is operator-facing, e.g. "3 jobs queued > 30s".
	Reason string `json:"reason"`
}

// PoolPlan is the decision for one pool.
type PoolPlan struct {
	PoolID   string `json:"pool_id"`
	PoolName string `json:"pool_name"`
	// Current is the number of live runners left after this tick's reaping;
	// a runner being failed or retired no longer counts towards the pool.
	Current int `json:"current"`
	// Desired is the runner count the pool is aiming at.
	Desired int `json:"desired"`
	// QueuedMatched counts every queued job this pool claims, including jobs
	// still inside ScaleUpDelay and therefore not yet driving a create.
	QueuedMatched int `json:"queued_matched"`
	// QuotaDeferredJobs counts queued jobs which did not contribute to desired
	// capacity because their repository reached the pool's best-effort scale-up
	// limit. It is separate from Blocked because admitted work may still scale.
	QuotaDeferredJobs int `json:"quota_deferred_jobs,omitempty"`
	// QuotaDeferredRepositories names the repositories represented by those
	// deferred jobs, in stable order.
	QuotaDeferredRepositories []string `json:"quota_deferred_repositories,omitempty"`
	// Reason is the sentence shown in the UI, e.g.
	// "scaled linux-x64 2 -> 4: 3 jobs queued > 30s". It is empty when the
	// pool's size did not change.
	Reason string `json:"reason,omitempty"`
	// Blocked is set when this pool needed runners and the fleet had nowhere to
	// put them: no host offers its backend, matches its host selector, or has
	// room left. It holds the sentence naming which, and it is what the
	// problems drawer reports -- a pool in this state looks completely healthy
	// while its jobs queue forever, so it has to be said out loud somewhere.
	//
	// It stays empty when the shortfall was only this tick's create budget,
	// which the next pass clears on its own and which no operator can act on.
	Blocked string `json:"blocked,omitempty"`
	// BlockedFix is what to change to unblock it, kept apart from Blocked
	// because the problems drawer shows the two differently.
	BlockedFix string `json:"blocked_fix,omitempty"`
	// BlockedAtCapacity distinguishes a fleet that is merely full -- every host
	// could run this pool and all of them are busy, which the next finished job
	// clears -- from one where no host can ever run it. Both are worth saying;
	// only the second is a fault.
	BlockedAtCapacity bool `json:"blocked_at_capacity,omitempty"`
	// BlockedAlternatives are the backends the hosts this pool otherwise fits
	// already offer. "Point this pool at a backend they already offer" is half
	// the fix for a pool blocked on its backend, and an operator cannot act on
	// it without being told which backend that is -- so the answer travels with
	// the reason, to the problems drawer, the pool page and the CLI.
	BlockedAlternatives []string `json:"blocked_alternatives,omitempty"`
	Actions             []Action `json:"actions,omitempty"`
}

// Plan is one tick's worth of decisions.
type Plan struct {
	Pools []PoolPlan `json:"pools"`
	// Actions is every pool's actions flattened, in execution order.
	Actions []Action `json:"actions,omitempty"`
	// Unmatched holds queued jobs no enabled pool claims. They will never run,
	// so the UI surfaces them as a configuration problem.
	Unmatched []*store.Job `json:"unmatched,omitempty"`
}

// Decide turns a snapshot of the fleet into the actions that move it towards
// what the queue is asking for. It is deterministic: the same snapshot always
// yields the same plan, down to the order of the actions.
func Decide(s Snapshot) Plan {
	pools := sortedPools(s.Pools)
	demand, unmatched := assign(pools, s.Jobs)

	t := &tick{
		now:                s.Now,
		policy:             s.Policy,
		hosts:              newHostSet(s.Hosts, s.Now),
		budget:             s.Policy.MaxCreatesPerTick,
		activeByRepository: s.ActiveByRepository,
		poolCount:          len(pools),
	}
	if t.budget <= 0 {
		// An unset cap must not stall the fleet; host capacity still bounds us.
		t.budget = math.MaxInt
	}

	plan := Plan{Pools: make([]PoolPlan, 0, len(pools)), Unmatched: unmatched}
	for _, p := range pools {
		pp := t.decidePool(p, s.Runners[p.ID], demand[p.ID])
		plan.Pools = append(plan.Pools, pp)
	}
	t.allocate(pools, plan.Pools, s.Runners, demand)
	// Pool plans remain in name order, and their actions are flattened in that
	// same stable order even though capacity was granted round by round.
	for _, pp := range plan.Pools {
		plan.Actions = append(plan.Actions, pp.Actions...)
	}
	return plan
}

// tick is the mutable state of a single Decide call: capacity handed out so
// far, and the create budget left for the pools that have not been served yet.
type tick struct {
	now                time.Time
	policy             Policy
	hosts              *hostSet
	budget             int
	activeByRepository map[string]int
	poolCount          int
}

// assign maps every queued job onto the pool that will run it, and collects the
// ones nothing claims.
func assign(pools []*store.Pool, jobs []*store.Job) (map[string][]*store.Job, []*store.Job) {
	demand := make(map[string][]*store.Job, len(pools))
	var unmatched []*store.Job
	for _, j := range sortedJobs(jobs) {
		if j.State != store.JobQueued {
			continue
		}
		p := BestPool(pools, j.Labels)
		if p == nil {
			unmatched = append(unmatched, j)
			continue
		}
		demand[p.ID] = append(demand[p.ID], j)
	}
	return demand, unmatched
}

// decidePool applies the reap, scale-up and scale-down rules to one pool.
func (t *tick) decidePool(p *store.Pool, runners []*store.Runner, queued []*store.Job) PoolPlan {
	plan := PoolPlan{PoolID: p.ID, PoolName: p.Name, QueuedMatched: len(queued)}

	actions, remaining := t.reap(p, sortedRunners(runners))
	plan.Actions = actions

	live, busy := 0, 0
	for _, r := range remaining {
		if r.State.Live() {
			live++
		}
		if r.State == store.RunnerBusy {
			busy++
		}
	}
	plan.Current = live

	if !p.Enabled {
		t.disable(p, &plan, remaining, live)
		return plan
	}

	eligible := 0
	quotaRepositories := map[string]bool{}
	admitted := map[string]int{}
	for _, j := range queued {
		if p.RepositoryScaleUpLimit > 0 && t.activeByRepository[p.ID+"\x00"+j.Repo]+admitted[j.Repo] >= p.RepositoryScaleUpLimit {
			plan.QuotaDeferredJobs++
			quotaRepositories[j.Repo] = true
			continue
		}
		if t.now.Sub(j.QueuedAt) >= t.policy.ScaleUpDelay {
			eligible++
			admitted[j.Repo]++
		}
	}
	for repo := range quotaRepositories {
		plan.QuotaDeferredRepositories = append(plan.QuotaDeferredRepositories, repo)
	}
	slices.Sort(plan.QuotaDeferredRepositories)
	// Idle runners are already counted in live, so subtracting live from the
	// target is what stops the scheduler from creating a runner for a job an
	// idle one will pick up within the second.
	plan.Desired = clamp(max(p.MinRunners, busy+eligible), p.MinRunners, p.MaxRunners)

	switch {
	case plan.Desired < live:
		t.scaleDown(p, &plan, remaining, live)
	}
	return plan
}

// allocate shares creation capacity after every pool's desired size has been
// calculated. Priority tiers are exhausted from highest to lowest; within a
// tier each backlogged pool receives one slot per round.
func (t *tick) allocate(pools []*store.Pool, plans []PoolPlan, runners map[string][]*store.Runner, demand map[string][]*store.Job) {
	tiers := append([]*store.Pool(nil), pools...)
	slices.SortStableFunc(tiers, func(a, b *store.Pool) int { return cmp.Compare(b.Priority, a.Priority) })
	byID := make(map[string]*PoolPlan, len(plans))
	for i := range plans {
		byID[plans[i].PoolID] = &plans[i]
	}

	for start := 0; start < len(tiers); {
		end := start + 1
		for end < len(tiers) && tiers[end].Priority == tiers[start].Priority {
			end++
		}
		active := append([]*store.Pool(nil), tiers[start:end]...)
		for len(active) > 0 && t.budget > 0 {
			next := active[:0]
			for _, p := range active {
				pp := byID[p.ID]
				if !p.Enabled || pp.Desired <= pp.Current+creates(pp.Actions) {
					continue
				}
				if !t.grant(p, pp, runners[p.ID], demand[p.ID]) {
					continue
				}
				if pp.Desired > pp.Current+creates(pp.Actions) {
					next = append(next, p)
				}
				if t.budget == 0 {
					break
				}
			}
			active = next
		}
		start = end
		if t.budget == 0 {
			break
		}
	}
	for _, p := range pools {
		pp := byID[p.ID]
		got := creates(pp.Actions)
		if !p.Enabled || pp.Desired <= pp.Current+got || pp.Blocked != "" {
			continue
		}
		why := fmt.Sprintf("this tick's global limit of %s is exhausted; the next pass will continue", plural(t.policy.MaxCreatesPerTick, "new runner"))
		pp.Reason = cannotScale(p.Name, pp.Current+got, pp.Desired, why)
	}
}

func creates(actions []Action) int {
	n := 0
	for _, a := range actions {
		if a.Kind == ActionCreate {
			n++
		}
	}
	return n
}

func (t *tick) grant(p *store.Pool, plan *PoolPlan, runners []*store.Runner, queued []*store.Job) bool {
	hosts := t.hosts.place(p, 1)
	if len(hosts) == 0 {
		b := t.hosts.why(p)
		plan.Reason = cannotScale(p.Name, plan.Current+creates(plan.Actions), plan.Desired, sentence(b.what, b.fix))
		plan.Blocked, plan.BlockedFix = b.what, b.fix
		plan.BlockedAtCapacity, plan.BlockedAlternatives = b.atCapacity, b.alternatives
		return false
	}
	busy, eligible := 0, 0
	for _, r := range runners {
		if r.State == store.RunnerBusy {
			busy++
		}
	}
	for _, r := range queued {
		if t.now.Sub(r.QueuedAt) >= t.policy.ScaleUpDelay {
			eligible++
		}
	}
	reason := upReason(p, busy, eligible, t.policy.ScaleUpDelay)
	plan.Actions = append(plan.Actions, Action{Kind: ActionCreate, PoolID: p.ID, PoolName: p.Name, HostID: hosts[0], Reason: reason})
	t.budget--
	plan.Reason = scaled(p.Name, plan.Current, plan.Current+creates(plan.Actions), reason)
	return true
}

// reap removes from consideration the runners the scheduler can no longer count
// on, and returns the actions that clean them up. A busy runner is never
// touched here: only an explicit operator drain interrupts a running job.
func (t *tick) reap(p *store.Pool, runners []*store.Runner) (actions []Action, remaining []*store.Runner) {
	var removes, fails, retires []Action
	for _, r := range runners {
		age := r.Age(t.now)
		switch {
		case r.State == store.RunnerRemoved:
			// Already gone; it neither costs capacity nor needs an action.
		case r.State == store.RunnerFailed:
			removes = append(removes, t.action(ActionRemove, p, r,
				"runner failed; removing it to free host capacity"))
		case starting(r.State) && t.policy.ProvisionTimeout > 0 && age > t.policy.ProvisionTimeout:
			fails = append(fails, t.action(ActionFail, p, r, fmt.Sprintf(
				"stuck in %s for %s, past the %s provision timeout; check the host's agent log",
				r.State, formatDuration(age), formatDuration(t.policy.ProvisionTimeout))))
		case t.policy.MaxRunnerLifetime > 0 && age > t.policy.MaxRunnerLifetime &&
			r.State != store.RunnerBusy && r.State != store.RunnerDraining:
			retires = append(retires, t.action(ActionDrain, p, r, fmt.Sprintf(
				"runner reached the %s maximum lifetime",
				formatDuration(t.policy.MaxRunnerLifetime))))
		default:
			remaining = append(remaining, r)
		}
	}
	// Cleanup first: removing dead runners frees host capacity for the creates
	// that follow in the same tick.
	actions = append(actions, removes...)
	actions = append(actions, fails...)
	actions = append(actions, retires...)
	return actions, remaining
}

// disable drains a disabled pool to zero. Its busy runners are left alone, so
// disabling a pool never kills a job that is already running.
func (t *tick) disable(p *store.Pool, plan *PoolPlan, remaining []*store.Runner, live int) {
	plan.Desired = 0
	n := 0
	for _, r := range remaining {
		if r.State == store.RunnerBusy || r.State == store.RunnerDraining {
			continue
		}
		plan.Actions = append(plan.Actions, t.action(ActionDrain, p, r, "pool is disabled"))
		n++
	}
	if n > 0 {
		plan.Reason = scaled(p.Name, live, live-n, "pool is disabled")
	}
}

// scaleDown drains surplus runners that have been idle for longer than the
// pool's idle timeout, longest-idle first, and never below MinRunners.
func (t *tick) scaleDown(p *store.Pool, plan *PoolPlan, remaining []*store.Runner, live int) {
	idle := t.drainable(p, remaining)
	n := min(live-max(plan.Desired, p.MinRunners), len(idle))
	if n <= 0 {
		return
	}
	timeout := p.IdleTimeout.Duration()
	for _, r := range idle[:n] {
		reason := "surplus idle runner"
		if timeout > 0 {
			reason = fmt.Sprintf("idle for %s, over the %s idle timeout",
				formatDuration(r.IdleFor(t.now)), formatDuration(timeout))
		}
		plan.Actions = append(plan.Actions, t.action(ActionDrain, p, r, reason))
	}
	what := plural(n, "runner") + " idle"
	if timeout > 0 {
		what += " > " + formatDuration(timeout)
	}
	plan.Reason = scaled(p.Name, live, live-n, what)
}

// drainable returns the idle runners that have waited out the pool's idle
// timeout, longest-idle first so that the coldest runner goes first.
func (t *tick) drainable(p *store.Pool, runners []*store.Runner) []*store.Runner {
	timeout := p.IdleTimeout.Duration()
	var out []*store.Runner
	for _, r := range runners {
		if r.State == store.RunnerIdle && r.IdleFor(t.now) >= timeout {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b *store.Runner) int {
		return idleSince(a).Compare(idleSince(b))
	})
	return out
}

func (t *tick) action(kind ActionKind, p *store.Pool, r *store.Runner, reason string) Action {
	return Action{Kind: kind, PoolID: p.ID, PoolName: p.Name, RunnerID: r.ID, Reason: reason}
}

// ---------------------------------------------------------------------------
// Host selection
// ---------------------------------------------------------------------------

// hostSet tracks the capacity left on each host as the plan is built, so that
// two pools cannot both be promised the last free slot on the same host.
type hostSet struct {
	hosts []*store.Host
	free  map[string]int
	now   time.Time
}

func newHostSet(hosts []*store.Host, now time.Time) *hostSet {
	hs := &hostSet{hosts: sortedHosts(hosts), free: make(map[string]int, len(hosts)), now: now}
	for _, h := range hs.hosts {
		hs.free[h.ID] = h.Free()
	}
	return hs
}

// place reserves up to n slots for the pool and returns the chosen host IDs.
// It may return fewer than n, or none at all, when the fleet is out of room.
func (hs *hostSet) place(p *store.Pool, n int) []string {
	out := make([]string, 0, n)
	for len(out) < n {
		h := hs.pick(p)
		if h == nil {
			break
		}
		hs.free[h.ID]--
		out = append(out, h.ID)
	}
	return out
}

// pick returns the eligible host with the most room left. Spreading runners
// over hosts keeps one busy host from becoming the fleet's single point of
// failure; the host ID breaks ties so the choice is reproducible.
func (hs *hostSet) pick(p *store.Pool) *store.Host {
	var best *store.Host
	for _, h := range hs.hosts {
		if !hs.eligible(h, p) {
			continue
		}
		if best == nil || hs.free[h.ID] > hs.free[best.ID] {
			best = h
		}
	}
	return best
}

func (hs *hostSet) eligible(h *store.Host, p *store.Pool) bool {
	return hs.free[h.ID] > 0 && h.Healthy(hs.now) && !h.Cordoned &&
		slices.Contains(h.Backends, string(p.Backend)) && selects(p.HostSelector, h.Labels)
}

// selects reports whether every key and value of the selector is present on the
// host. An empty selector means "any host".
func selects(selector, labels store.StringMap) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// blockage is why one pool could not be placed: what is true, what to change,
// and the two facts the callers treat differently -- whether the fleet is
// merely full, and which other backends would work right now.
type blockage struct {
	what string
	fix  string
	// atCapacity is the one case that is not a misconfiguration: every host
	// could run this pool and all of them are busy.
	atCapacity bool
	// alternatives are the backends offered by the hosts that match this pool
	// in every other way, in the order a pool would sensibly move to them.
	alternatives []string
}

// why explains why no host could take a runner for p, naming the counts an
// operator can act on. It is split into what is true and what to change,
// because the problems drawer shows those as two different things; sentence
// joins them for the one-line scaling reason.
func (hs *hostSet) why(p *store.Pool) blockage {
	if len(hs.hosts) == 0 {
		return blockage{
			what: "no agent hosts are registered, so there is nowhere to put a runner",
			fix:  "run 'zoomies agent' on a machine that can host runners, using a join token from the Hosts page",
		}
	}
	var unhealthy, cordoned, backend, selector, full int
	var detail string
	for _, h := range hs.hosts {
		switch {
		case !h.Healthy(hs.now):
			unhealthy++
		case h.Cordoned:
			cordoned++
		case !slices.Contains(h.Backends, string(p.Backend)):
			backend++
			// The agent's own probe usually names the fix -- a socket that is
			// not readable, a daemon that is not running -- and "without the
			// docker backend" alone sends an operator looking in the wrong
			// place. Take the first host that has an explanation.
			if detail == "" {
				if info, ok := h.BackendInfo.Find(p.Backend); ok && !info.Available && info.Detail != "" {
					detail = h.Name + " reports: " + info.Detail
				}
			}
		case !selects(p.HostSelector, h.Labels):
			selector++
		default:
			full++
		}
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(unhealthy, "unhealthy")
	add(cordoned, "cordoned")
	add(backend, "without the "+string(p.Backend)+" backend")
	add(selector, "not matching the pool's host selector")
	add(full, "at capacity")
	b := blockage{
		what: fmt.Sprintf("no host can take a new %s runner (%s)",
			p.Backend, strings.Join(parts, ", ")),
		atCapacity: full == len(hs.hosts),
	}
	if detail != "" {
		// The agent's own words about the backend it could not use. They name
		// the fix far more precisely than any count can.
		b.what += ". " + detail
	}
	switch {
	case b.atCapacity:
		b.fix = "wait for a job to finish, raise a host's capacity, or add a host"
	case unhealthy == len(hs.hosts):
		b.fix = "check that the zoomies agent is running on those hosts and can reach this controller"
	case backend > 0 && backend+unhealthy+cordoned == len(hs.hosts):
		// Only a pool blocked on its backend can be unblocked by changing it,
		// so that is the only case that carries alternatives. Offering them for
		// a full fleet or an unmatched selector would send an operator to
		// change the one thing that was never the problem.
		b.alternatives = hs.otherBackends(p)
		b.fix = fmt.Sprintf("make the %s backend usable on one of those hosts%s",
			p.Backend, hs.switchTo(p, b.alternatives))
	default:
		b.fix = "add a host, raise a host's capacity, uncordon one, or relax the pool's host selector"
	}
	return b
}

// backendOrder is the order alternatives are offered in: the two container
// backends first, since they are interchangeable as far as isolation goes, and
// the process backend last because moving to it means jobs stop being contained
// at all. It is a suggestion, never a change Zoomies makes by itself.
var backendOrder = []store.BackendKind{store.BackendDocker, store.BackendPodman, store.BackendProcess}

// otherBackends lists what the hosts that fit this pool in every other way --
// healthy, uncordoned, selected, with room -- do offer instead of the backend
// it asks for. It is the answer to "point this pool at a backend they already
// offer", which is not actionable until somebody says which one.
func (hs *hostSet) otherBackends(p *store.Pool) []string {
	offered := map[string]int{}
	for _, h := range hs.hosts {
		if hs.free[h.ID] <= 0 || !h.Healthy(hs.now) || h.Cordoned || !selects(p.HostSelector, h.Labels) {
			continue
		}
		for _, kind := range h.Backends {
			if kind != string(p.Backend) {
				offered[kind]++
			}
		}
	}
	var out []string
	for _, kind := range backendOrder {
		if offered[string(kind)] > 0 {
			out = append(out, string(kind))
		}
	}
	return out
}

// switchTo turns the alternatives into the second half of the fix, or into an
// honest full stop when there is no second half: a fleet that offers nothing
// else needs a daemon fixed, and saying "or use another backend" to an operator
// who has none is how a problems drawer stops being believed.
func (hs *hostSet) switchTo(p *store.Pool, alternatives []string) string {
	switch len(alternatives) {
	case 0:
		return "; they offer no other backend to switch this pool to"
	case 1:
		return fmt.Sprintf(", or point this pool at %s, which %s already offers",
			alternatives[0], plural(hs.offering(p, alternatives[0]), "host"))
	default:
		var parts []string
		for _, kind := range alternatives {
			parts = append(parts, fmt.Sprintf("%s (%s)", kind, plural(hs.offering(p, kind), "host")))
		}
		return ", or point this pool at a backend they already offer: " + strings.Join(parts, ", ")
	}
}

// offering counts the hosts that would take this pool if it asked for kind.
func (hs *hostSet) offering(p *store.Pool, kind string) int {
	n := 0
	for _, h := range hs.hosts {
		if hs.free[h.ID] > 0 && h.Healthy(hs.now) && !h.Cordoned &&
			slices.Contains(h.Backends, kind) && selects(p.HostSelector, h.Labels) {
			n++
		}
	}
	return n
}

// sentence is why() as one line, for the scaling reason an operator reads in a
// pool's history.
func sentence(what, fix string) string {
	if fix == "" {
		return what
	}
	return what + "; " + fix
}

// ---------------------------------------------------------------------------
// Reasons
// ---------------------------------------------------------------------------

// scaled renders the sentence the UI and the scaling_events table show:
// "scaled linux-x64 2 -> 4: 3 jobs queued > 30s".
func scaled(pool string, from, to int, why string) string {
	return fmt.Sprintf("scaled %s %d -> %d: %s", pool, from, to, why)
}

// cannotScale renders the same sentence for a scale-up that could not happen,
// because an operator needs to see the demand that went unserved.
func cannotScale(pool string, from, to int, why string) string {
	return fmt.Sprintf("cannot scale %s %d -> %d: %s", pool, from, to, why)
}

// upReason names the demand behind a scale-up. Jobs win over the pool minimum
// because that is what the operator is watching when a queue is backing up.
func upReason(p *store.Pool, busy, eligible int, delay time.Duration) string {
	if eligible == 0 || busy+eligible < p.MinRunners {
		return fmt.Sprintf("pool minimum is %s", plural(p.MinRunners, "runner"))
	}
	if delay <= 0 {
		return plural(eligible, "job") + " queued"
	}
	return fmt.Sprintf("%s queued > %s", plural(eligible, "job"), formatDuration(delay))
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// formatDuration is time.Duration.String() without the trailing zero units, so
// an operator reads "5m" and "6h" rather than "5m0s" and "6h0m0s".
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// ---------------------------------------------------------------------------
// Deterministic ordering
// ---------------------------------------------------------------------------

func sortedPools(in []*store.Pool) []*store.Pool {
	out := compact(in)
	slices.SortStableFunc(out, func(a, b *store.Pool) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func sortedRunners(in []*store.Runner) []*store.Runner {
	out := compact(in)
	slices.SortStableFunc(out, func(a, b *store.Runner) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func sortedJobs(in []*store.Job) []*store.Job {
	out := compact(in)
	slices.SortStableFunc(out, func(a, b *store.Job) int {
		if c := a.QueuedAt.Compare(b.QueuedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func sortedHosts(in []*store.Host) []*store.Host {
	out := compact(in)
	slices.SortStableFunc(out, func(a, b *store.Host) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// compact copies a slice without its nil entries, so that sorting never
// reorders the caller's own slice and a stray nil cannot panic a decision.
func compact[T any](in []*T) []*T {
	out := make([]*T, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}

// idleSince is the instant a runner became idle, or its creation time when the
// agent never reported one.
func idleSince(r *store.Runner) time.Time {
	if r.LastIdleAt != nil {
		return *r.LastIdleAt
	}
	return r.CreatedAt
}

func starting(s store.RunnerState) bool {
	return s == store.RunnerProvisioning || s == store.RunnerRegistering
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		// A pool whose max is below its min is a misconfiguration; the hard cap
		// wins, because exceeding it is what costs money.
		return hi
	}
	return min(max(v, lo), hi)
}
