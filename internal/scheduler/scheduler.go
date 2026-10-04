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
	Readiness       map[string]map[string]time.Duration
	PreferReadiness bool
	// Now is the decision time. It is a field rather than a clock read so that
	// a test can place a runner exactly one second either side of a timeout.
	Now             time.Time
	LastProvisioned map[string]time.Time
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
	Hosts []*store.Host
	// Installations is every configured GitHub App installation. Eligibility
	// is decided from the identifiers a pool and a job already carry, so this
	// is here only so that an explanation can name the organisation or
	// repository an operator recognises instead of an opaque identifier.
	Installations []*store.Installation
	Policy        Policy
	// Jitter spreads the start-failure backoff, keyed by pool ID and redrawn
	// on every pass. It arrives here rather than being drawn here for the same
	// reason Now does: the scheduler reads no clock and no random source, so a
	// decision is reproducible from its inputs alone and a test can place a
	// pool exactly either side of its wait.
	//
	// Values outside [0,1) are clamped, and a pool with no entry gets none --
	// a caller that has not thought about jitter keeps the old behaviour.
	Jitter map[string]float64
	// HeldInstallations is every installation GitHub is rate-limiting, keyed
	// by ID, with the moment its quota returns. A pool on a held installation
	// creates nothing: the create would be refused identically, spend another
	// call against a quota that is already gone, and leave a failed row whose
	// only message is the refusal. The poller and the reap already stand down
	// from a held installation; this is the scheduler doing the same.
	HeldInstallations map[string]time.Time
	// JobHistory is the recent measured runs of each queued job, keyed by the
	// job and the pool that ran them, and HistorySizing is
	// scheduler.history_sizing: off, shadow or on. The caller reads the
	// history; the profile, and what it means for placement, are decided
	// here. An empty mode is off.
	JobHistory    map[store.JobUsageKey][]store.JobPeak
	HistorySizing string
	// HostOrder is scheduler.host_order: the order placement prefers hosts that
	// can all take a runner in -- see HostOrderHeadroom and the constants
	// beside it. An empty value is headroom, which is what every fleet had
	// before the setting existed.
	HostOrder string
}

const (
	// failedRetention is how long a failed runner stays on the Runners page
	// before the reap removes it. Removing it at once, as the scheduler used
	// to, freed nothing -- a failed runner holds no host slot -- and took the
	// only record of why it failed off the page within a second of it
	// appearing. Ten minutes is long enough to read, and the failures still
	// on the page are also what the start backoff below is decided on.
	failedRetention = 10 * time.Minute

	// startBackoff is how long a pool waits after a runner fails to start
	// before it creates another; each further failure doubles the wait, up to
	// maxStartBackoff. Without it a runner that dies on creation is replaced
	// in the same pass that notices, and a pool whose image cannot be pulled
	// creates, fails and removes a runner every second -- two GitHub API
	// calls a time, until the installation's hourly quota is spent on nothing.
	startBackoff    = 10 * time.Second
	maxStartBackoff = 5 * time.Minute

	// startBackoffJitter is how much of a pool's wait may be added to it, at
	// most. Without it every pool broken by the same thing -- a daemon that
	// went down, a registry that stopped answering -- counts the same doubling
	// from the same failure and retries in the same second, so the recovery
	// arrives as the same thundering herd that broke it.
	startBackoffJitter = 0.2
)

// Policy carries the tunables from config.Scheduler.
//
// Every duration here is disabled by a zero or negative value, so a caller that
// leaves a field unset gets "no timeout" instead of a fleet that reaps itself.
type Policy struct {
	// ScaleUpDelay is how long a job must have been queued before it counts as
	// demand, which damps churn when jobs arrive in bursts.
	ScaleUpDelay time.Duration
	// MaxRunnerLifetime drains a runner that has lived this long, the next
	// time it is not busy. It bounds how long a persistent runner's state and
	// credentials live; reap never drains a busy runner, so it is no answer to
	// a hung job.
	MaxRunnerLifetime time.Duration
	// ProvisionTimeout fails a runner that never finished registering.
	ProvisionTimeout time.Duration
	// DrainTimeout fails a runner that has been draining this long with no job
	// left on it. The agent task queue is in memory by design, so a controller
	// restart drops a stop that was already issued; without this the row stays
	// in draining, holds its host slot and leaves its pool one runner short for
	// as long as the controller runs. A runner still finishing a job is never
	// touched by it -- that wait is what a drain is -- so this bounds only the
	// drain that has nothing to wait for. Zero leaves a drain unbounded.
	DrainTimeout time.Duration
	// MaxCreatesPerTick caps creates across the whole fleet in one pass, so a
	// thundering herd of queued jobs cannot fill every host at once. Pools are
	// shared fairly among pools at the same priority; zero means no cap.
	MaxCreatesPerTick int
	// MaxRunners is limits.runners: the most live runners the whole fleet may
	// hold, across every pool. Zero means no ceiling. Unlike the per-tick
	// budget it does not refill on the next pass -- only a runner ending makes
	// room -- so a pool it holds back says which setting did it.
	MaxRunners int
	// Interval is how often the reconcile loop runs. A lower-priority pool
	// whose oldest queued job has waited at least this long is owed one
	// create before a higher tier spends the rest of the budget, so a deep
	// backlog at the top cannot starve every other pool pass after pass.
	// Zero turns that share off and priority alone decides.
	Interval time.Duration
}

// For is this policy as one pool sees it: the fleet's figures, with whatever
// that pool overrides of them applied.
//
// It is a method on the fleet's policy rather than a field on the pool so
// that a pool which overrides nothing keeps following the fleet -- including
// after an operator changes the fleet's figure, which a value copied onto the
// pool at creation would not. That is the same reason a pool's size is left
// to the host it lands on rather than frozen at the share it had on the day.
//
// MaxCreatesPerTick is deliberately not overridable. It is a fleet-wide
// budget shared between pools, and a pool that could raise its own share of
// it would be taking the protection from the others.
func (p Policy) For(pool *store.Pool) Policy {
	if pool == nil || !pool.RunnerSettings.Set() {
		return p
	}
	rs := pool.RunnerSettings
	if rs.ScaleUpDelay != nil {
		p.ScaleUpDelay = rs.ScaleUpDelay.Duration()
	}
	if rs.MaxRunnerLifetime != nil {
		p.MaxRunnerLifetime = rs.MaxRunnerLifetime.Duration()
	}
	if rs.ProvisionTimeout != nil {
		p.ProvisionTimeout = rs.ProvisionTimeout.Duration()
	}
	if rs.DrainTimeout != nil {
		p.DrainTimeout = rs.DrainTimeout.Duration()
	}
	return p
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
	// Size is set on an ActionCreate that no host had room for at the pool's
	// standard size: what the runner is created with instead, at or above the
	// pool's minimum. Nil means the standard.
	Size *store.Resources `json:"size,omitempty"`
	// SizedFromHistory says Size is above the standard rather than below it:
	// the runner was sized for what the jobs waiting are known to need.
	SizedFromHistory bool `json:"sized_from_history,omitempty"`
}

// sizePhrase says a runner size the way the reason for a create does.
func sizePhrase(cpus float64, memoryMB int64) string {
	parts := []string{}
	if cpus > 0 {
		parts = append(parts, formatCPUs(cpus)+" CPU")
	}
	if memoryMB > 0 {
		parts = append(parts, formatMB(memoryMB))
	}
	return strings.Join(parts, " and ")
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
	// eligible is the number of queued jobs that drove Desired: past the
	// scale-up delay and admitted by the repository limit. The scale-up reason
	// is written from it, so it names the jobs the pool is scaling for rather
	// than every job on the queue.
	eligible int
	// supply is the runners already there for a queued job to take: idle, or
	// still starting. A busy runner is not, and a draining one never will be.
	supply int
	// Unserved is how many of the queued jobs that drove Desired have no runner
	// idle, starting or created for them this pass: the pool is at its maximum,
	// or no host has room. A pool held back by a start failure or a rate limit
	// counts too, because for the job waiting it is the same thing. It is what
	// tells a job waiting for room in its class from one whose runner is simply
	// on its way.
	Unserved int `json:"unserved,omitempty"`
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
	// BlockedNoEligibleHost is the other half of that distinction: not one host
	// in the fleet could run this pool, so no job finishing anywhere will ever
	// clear it. It is the state a new machine fixes and a finished job does not,
	// which is why it is reported separately rather than folded into
	// BlockedAtCapacity -- that field means "the fleet is working and full", and
	// the problems drawer and the usage record both read it as such.
	BlockedNoEligibleHost bool `json:"blocked_no_eligible_host,omitempty"`
	// BlockedAlternatives are the backends the hosts this pool otherwise fits
	// already offer. "Point this pool at a backend they already offer" is half
	// the fix for a pool blocked on its backend, and an operator cannot act on
	// it without being told which backend that is -- so the answer travels with
	// the reason, to the problems drawer, the pool page and the CLI.
	BlockedAlternatives []string `json:"blocked_alternatives,omitempty"`
	// Failing is set when the pool's recent runners died before they ever
	// registered and the scheduler is holding off creating more for a while.
	// It says how many failed, the latest reason and when the next attempt
	// is, because a pool in this state has jobs waiting on runners that keep
	// failing, and the problems drawer has to be able to say why.
	Failing string `json:"failing,omitempty"`
	// FailingFault is the category behind that, taken from the most recent
	// failed runner. The sentence above says how many and when; this says what
	// to do, and they are different questions -- "the last four runners failed
	// to start" is a pool to look at, and "because the image could not be
	// pulled" is a tag to fix.
	//
	// It travels in the plan rather than being looked up again by each reader
	// for the reason the rest of this struct does: the problems drawer, the
	// pool page and a job's explanation all ask, and three lookups against a
	// fleet that has moved on is three answers.
	FailingFault store.FaultKind `json:"failing_fault,omitempty"`
	// Held is set when the pool's installation is inside a GitHub rate-limit
	// backoff. It says until when, because the pool is otherwise healthy and
	// its jobs are waiting on nothing the operator can see.
	Held string `json:"held,omitempty"`
	// History is what the jobs waiting on this pool are known to need and
	// what that did to placement: the hosts held off and the size given, or,
	// under shadow, what it would have done. Empty when nothing is known or
	// it changed nothing.
	History string `json:"history,omitempty"`
	// HistoryUnfit is set when a job waiting on this pool is known to need
	// more than any host that can run the pool has to give. Such a job is
	// placed as it always was, and is likely to fail the way it did before;
	// only a larger host fixes it, which is why it is reported.
	HistoryUnfit string   `json:"history_unfit,omitempty"`
	Actions      []Action `json:"actions,omitempty"`
}

// Plan is one tick's worth of decisions.
type Plan struct {
	Pools []PoolPlan `json:"pools"`
	// Actions is every pool's actions flattened, in execution order.
	Actions []Action `json:"actions,omitempty"`
	// Unmatched holds queued jobs no pool will run, each with the reason the
	// nearest pool refused it. They will never run, so the UI surfaces them as
	// a configuration problem.
	Unmatched []UnmatchedJob `json:"unmatched,omitempty"`
}

// UnmatchedJob is a queued job no pool claimed, and why.
type UnmatchedJob struct {
	Job *store.Job `json:"job"`
	// Reason is empty when the job's labels simply match no pool here, which
	// is the case the Jobs page already explains on its own. It is set when
	// something less obvious refused the job -- a pool that advertises exactly
	// those labels but belongs to another GitHub App installation.
	Reason string `json:"reason,omitempty"`
}

// Decide turns a snapshot of the fleet into the actions that move it towards
// what the queue is asking for. It is deterministic: the same snapshot always
// yields the same plan, down to the order of the actions.
func Decide(s Snapshot) Plan {
	pools := sortedPools(s.Pools)
	demand, unmatched := assign(pools, s.Jobs, targetsOf(s.Installations))

	t := &tick{
		now:                s.Now,
		policy:             s.Policy,
		hosts:              newHostSet(s.Hosts, pools, s.Runners, s.Now),
		budget:             s.Policy.MaxCreatesPerTick,
		activeByRepository: s.ActiveByRepository,
		poolCount:          len(pools),
		jitter:             s.Jitter,
		lastProvisioned:    s.LastProvisioned,
		held:               s.HeldInstallations,
		stillRunning:       runnersWithAJob(s.Jobs),
		history:            s.JobHistory,
		historySizing:      s.HistorySizing,
	}
	t.hosts.readiness, t.hosts.preferReadiness = s.Readiness, s.PreferReadiness
	t.hosts.order = s.HostOrder
	if t.budget <= 0 {
		// An unset cap must not stall the fleet; host capacity still bounds us.
		t.budget = math.MaxInt
	}

	plan := Plan{Pools: make([]PoolPlan, 0, len(pools)), Unmatched: unmatched}
	for _, p := range pools {
		pp := t.decidePool(p, s.Runners[p.ID], demand[p.ID])
		plan.Pools = append(plan.Pools, pp)
	}
	if limit := s.Policy.MaxRunners; limit > 0 {
		// Counted after the reap, from what each pool still holds, so a runner
		// this pass is failing or removing does not keep its place under the
		// ceiling for one pass longer than it keeps its host slot.
		live := 0
		for _, pp := range plan.Pools {
			live += pp.Current
		}
		if room := max(limit-live, 0); room < t.budget {
			t.budget, t.atCeiling = room, true
		}
	}
	t.allocate(pools, plan.Pools, s.Runners, demand)
	for i := range plan.Pools {
		pp := &plan.Pools[i]
		pp.Unserved = max(0, pp.eligible-pp.supply-creates(pp.Actions))
	}
	// Finish cleanup before starting replacements, then preserve the exact
	// allocation order when registration admission admits only part of a plan.
	for _, pp := range plan.Pools {
		for _, action := range pp.Actions {
			if action.Kind != ActionCreate {
				plan.Actions = append(plan.Actions, action)
			}
		}
	}
	plan.Actions = append(plan.Actions, t.creates...)
	return plan
}

// tick is the mutable state of a single Decide call: capacity handed out so
// far, and the create budget left for the pools that have not been served yet.
type tick struct {
	creates []Action
	now     time.Time
	policy  Policy
	hosts   *hostSet
	budget  int
	// atCeiling says the budget was cut to what limits.runners leaves, so a
	// pool left short is told that rather than blamed on the tick.
	atCeiling          bool
	activeByRepository map[string]int
	poolCount          int
	jitter             map[string]float64
	lastProvisioned    map[string]time.Time
	held               map[string]time.Time
	// stillRunning is every runner with a job in progress on it, whatever the
	// row says its state is. A runner given up as lost is failed while its job
	// is still running, and the late-report path keeps that workload alive
	// on purpose; the reap has to know not to take it back.
	stillRunning map[string]bool
	// history and historySizing are Snapshot.JobHistory and HistorySizing.
	history       map[store.JobUsageKey][]store.JobPeak
	historySizing string
}

// assign maps every queued job onto the pool that will run it, and collects the
// ones nothing claims, each with the reason it went unclaimed.
func assign(pools []*store.Pool, jobs []*store.Job, targets map[string]string) (map[string][]*store.Job, []UnmatchedJob) {
	demand := make(map[string][]*store.Job, len(pools))
	var unmatched []UnmatchedJob
	for _, j := range sortedJobs(jobs) {
		if j.State != store.JobQueued || j.Provisioning != "" {
			continue
		}
		p, reason := bestPool(pools, j, targets)
		if p == nil {
			unmatched = append(unmatched, UnmatchedJob{Job: j, Reason: reason})
			continue
		}
		demand[p.ID] = append(demand[p.ID], j)
	}
	return demand, unmatched
}

// targetsOf indexes installations by identifier so that an explanation can
// name the organisation or repository one manages.
func targetsOf(insts []*store.Installation) map[string]string {
	if len(insts) == 0 {
		return nil
	}
	out := make(map[string]string, len(insts))
	for _, i := range insts {
		if i != nil {
			out[i.ID] = i.Target
		}
	}
	return out
}

// decidePool applies the reap, scale-up and scale-down rules to one pool.
func (t *tick) decidePool(p *store.Pool, runners []*store.Runner, queued []*store.Job) PoolPlan {
	plan := PoolPlan{PoolID: p.ID, PoolName: p.Name, QueuedMatched: len(queued)}

	actions, remaining := t.reap(p, sortedRunners(runners))
	plan.Actions = actions

	live, busy, draining := 0, 0, 0
	for _, r := range remaining {
		if r.State.Live() {
			live++
		}
		switch r.State {
		case store.RunnerBusy:
			busy++
		case store.RunnerDraining:
			draining++
		}
	}
	plan.Current = live
	plan.supply = live - busy - draining

	if !p.Enabled {
		t.disable(p, &plan, remaining, live)
		return plan
	}

	eligible := 0
	quotaRepositories := map[string]bool{}
	admitted := map[string]int{}
	for _, j := range queued {
		// The delay is asked about first because it is the cheaper reason to
		// be waiting and the one nobody can act on. A job still inside it
		// would not have driven a create whatever the repository limit said,
		// so counting it as deferred by that limit made the pool's reason
		// read "3 jobs deferred by the repository limit for acme/widgets"
		// about jobs the next few seconds were going to release anyway --
		// and sent an operator to change a setting that was not the cause.
		if !j.ProvisionNow && t.now.Sub(j.QueuedAt) < t.policy.For(p).ScaleUpDelay {
			continue
		}
		if p.RepositoryScaleUpLimit > 0 && t.activeByRepository[p.ID+"\x00"+j.Repo]+admitted[j.Repo] >= p.RepositoryScaleUpLimit {
			plan.QuotaDeferredJobs++
			quotaRepositories[j.Repo] = true
			continue
		}
		eligible++
		admitted[j.Repo]++
	}
	for repo := range quotaRepositories {
		plan.QuotaDeferredRepositories = append(plan.QuotaDeferredRepositories, repo)
	}
	slices.Sort(plan.QuotaDeferredRepositories)
	// Idle runners are already counted in live, so subtracting live from the
	// target is what stops the scheduler from creating a runner for a job an
	// idle one will pick up within the second. A draining runner is counted on
	// both sides: it still holds a slot on its host until it is gone, which is
	// what keeps the pool under its maximum, but it will never take a job, so
	// it must not stand in for the runner a queued job is waiting on.
	plan.Desired = clamp(max(p.MinRunners, busy+draining+eligible), p.MinRunners, p.MaxRunners)
	plan.eligible = eligible
	plan.Failing, plan.FailingFault = t.holdAfterStartFailures(p, runners)
	plan.Held = t.holdWhileRateLimited(p)

	switch {
	case plan.Desired < live:
		t.scaleDown(p, &plan, remaining, live)
	}
	return plan
}

// holdAfterStartFailures decides whether a pool's recent failures should stop
// it creating for now, and returns the sentence that says so, or "" when it
// may go ahead.
//
// Only runners that died before registering count: they are evidence that
// the pool cannot start a runner at the moment -- an image that will not
// pull, a host that cannot reach GitHub -- whereas a runner that ran jobs
// and then failed says nothing about the next create. The wait doubles with
// each failure still on the page, so a broken pool settles at one attempt
// every few minutes rather than one a second; it recovers on its own, because
// a pass that creates nothing adds no failure and the wait simply runs out.
func (t *tick) holdAfterStartFailures(p *store.Pool, runners []*store.Runner) (string, store.FaultKind) {
	failed := startFailures(runners, t.now)
	if len(failed) == 0 {
		return "", ""
	}
	wait := maxStartBackoff
	if shift := len(failed) - 1; shift < 8 && startBackoff<<shift < maxStartBackoff {
		wait = startBackoff << shift
	}
	wait = jittered(wait, t.jitter[p.ID])
	since := t.now.Sub(failedAt(failed[0]))
	if since >= wait {
		return "", ""
	}
	which := "the last runner"
	if len(failed) > 1 {
		which = "the last " + plural(len(failed), "runner")
	}
	return fmt.Sprintf("%s failed to start, most recently %s ago (%s); trying again in %s",
		which, formatDuration(since.Truncate(time.Second)), summarise(failed[0].Message),
		formatDuration((wait - since).Round(time.Second))), failed[0].FaultKind
}

// holdWhileRateLimited returns the sentence that says a pool's installation
// is inside a GitHub rate-limit backoff, or "" when it is not.
//
// It is a hold on creates only. Draining and removing a runner ask GitHub for
// nothing, so a held pool still scales down.
func (t *tick) holdWhileRateLimited(p *store.Pool) string {
	until, ok := t.held[p.InstallationID]
	if !ok || !t.now.Before(until) {
		return ""
	}
	return fmt.Sprintf("GitHub is rate-limiting this pool's installation; not asking it to register runners for %s",
		formatDuration(until.Sub(t.now).Round(time.Second)))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// jittered lengthens a wait by up to startBackoffJitter of itself.
//
// It only ever adds. Shortening a backoff would let the pool that is failing
// hardest come back soonest, which is the opposite of what the doubling is
// for; spreading the herd is worth a little extra wait and nothing else.
func jittered(d time.Duration, f float64) time.Duration {
	if f <= 0 {
		return d
	}
	if f > 1 {
		f = 1
	}
	return d + time.Duration(float64(d)*startBackoffJitter*f)
}

// startFailures returns the runners that failed before ever registering and
// whose failure is still on the page, newest first.
func startFailures(runners []*store.Runner, now time.Time) []*store.Runner {
	var out []*store.Runner
	for _, r := range runners {
		if r.State == store.RunnerFailed && r.RegisteredAt == nil && now.Sub(failedAt(r)) < failedRetention {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b *store.Runner) int {
		return failedAt(b).Compare(failedAt(a))
	})
	return out
}

// recentBusyHosts is the hosts a runner of this pool most recently failed to
// start on with FaultBackendBusy, whose failure is still on the page --
// startFailures' own window, so a host stops being avoided exactly when it
// stops counting toward the pool's own start-failure backoff.
//
// It is what grant steers a replacement away from where it can: the daemon
// that timed out on the last create is not evidence the pool is broken --
// holdAfterStartFailures already treats FaultBackendBusy like any other start
// failure and waits regardless of which host caused it -- but it is evidence
// that this particular host, right now, is one to try last rather than
// first, if the fleet has anywhere else to offer.
func recentBusyHosts(runners []*store.Runner, now time.Time) map[string]bool {
	var out map[string]bool
	for _, r := range startFailures(runners, now) {
		if r.FaultKind != store.FaultBackendBusy || r.HostID == "" {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[r.HostID] = true
	}
	return out
}

// runnersWithAJob is the set of runners a job in progress is attributed to.
func runnersWithAJob(jobs []*store.Job) map[string]bool {
	out := map[string]bool{}
	for _, j := range jobs {
		if j.State == store.JobInProgress && j.RunnerID != "" {
			out[j.RunnerID] = true
		}
	}
	return out
}

// drainingFor is how long a runner has been draining.
//
// A row written before the column existed has no draining_since, and its age
// is the only clock left. That over-counts -- a long-lived runner drained a
// moment ago looks overdue at once -- but the alternative is a drain from
// before the upgrade that no timeout can ever reach, which is the state this
// rule exists to end.
func drainingFor(r *store.Runner, now time.Time) time.Duration {
	if r.DrainingSince != nil {
		return now.Sub(*r.DrainingSince)
	}
	return r.Age(now)
}

// failedAt is when a runner failed. The store stamps finished_at on the
// transition; a row without one falls back to its creation, which is the
// conservative reading for a failure of unknown age.
func failedAt(r *store.Runner) time.Time {
	if r.FinishedAt != nil {
		return *r.FinishedAt
	}
	return r.CreatedAt
}

// summarise keeps a runner's failure message to one clause of a sentence. The
// full text is on the Runners page; here it is the hint, not the report.
//
// The cut is by bytes and the message is not: it comes from a daemon, a
// registry or GitHub, and a limit that landed inside a character left half of
// one behind -- which reaches the problems drawer and the pool page as a
// replacement glyph in the middle of a word. Dropping the partial character is
// what the reader wanted from the ellipsis anyway.
func summarise(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return "no reason was recorded"
	}
	const limit = 160
	if len(message) > limit {
		return strings.ToValidUTF8(message[:limit-1], "") + "…"
	}
	return message
}

// allocate shares creation capacity after every pool's desired size has been
// calculated. Priority tiers are exhausted from highest to lowest; within a
// tier each backlogged pool receives one slot per round.
func (t *tick) allocate(pools []*store.Pool, plans []PoolPlan, runners map[string][]*store.Runner, demand map[string][]*store.Job) {
	tiers := append([]*store.Pool(nil), pools...)
	slices.SortStableFunc(tiers, func(a, b *store.Pool) int {
		if c := cmp.Compare(b.Priority, a.Priority); c != 0 {
			return c
		}
		// Under a small per-tick budget, start with the least recently served
		// backlogged pool. Round-robin within a pass alone starves later pools.
		// Explicit run-now demand comes first within its configured priority tier.
		aq, bq := demand[a.ID], demand[b.ID]
		aNow, bNow := len(aq) > 0 && aq[0].ProvisionNow, len(bq) > 0 && bq[0].ProvisionNow
		if aNow != bNow {
			if aNow {
				return -1
			}
			return 1
		}
		latest := func(id string) time.Time {
			last := t.lastProvisioned[id]
			for _, r := range runners[id] {
				if r != nil && r.CreatedAt.After(last) {
					last = r.CreatedAt
				}
			}
			return last
		}
		if c := latest(a.ID).Compare(latest(b.ID)); c != 0 {
			return c
		}
		// Waiting is only comparable between pools that have something waiting,
		// so having demand is its own key ahead of the wait. Comparing wait
		// only when both had demand and falling to the ID otherwise made a
		// pool with none -- a warm minimum -- order by ID against pools ordered
		// by wait: a cycle, and a sort left to pick whichever it met first.
		if (len(aq) > 0) != (len(bq) > 0) {
			if len(aq) > 0 {
				return -1
			}
			return 1
		}
		if len(aq) > 0 {
			if c := aq[0].QueuedAt.Compare(bq[0].QueuedAt); c != 0 {
				return c
			}
		}
		return strings.Compare(a.ID, b.ID)
	})
	byID := make(map[string]*PoolPlan, len(plans))
	for i := range plans {
		byID[plans[i].PoolID] = &plans[i]
	}

	// The share across tiers is granted before any tier is served, so that
	// what is left is what priority then decides.
	shared, floor := t.shareAcrossTiers(tiers, byID, runners, demand)
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
				if why := firstNonEmpty(pp.Failing, pp.Held); why != "" {
					// Held back, not blocked: there is somewhere to put a
					// runner, and the pool will try again on its own once the
					// wait is out. The reason still says what went unserved.
					pp.Reason = cannotScale(p.Name, pp.Current, pp.Desired, why)
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
		if !p.Enabled || pp.Desired <= pp.Current+got || pp.Blocked != "" || pp.Failing != "" || pp.Held != "" {
			continue
		}
		why := fmt.Sprintf("this tick's global limit of %s is exhausted; the next pass will continue", plural(t.policy.MaxCreatesPerTick, "new runner"))
		// The runner ceiling is the binding reason when it holds: no share
		// across priorities makes room it does not have.
		if t.atCeiling {
			why = fmt.Sprintf("the fleet holds the %s limits.runners allows; a runner ending makes room, or raise limits.runners",
				plural(t.policy.MaxRunners, "live runner"))
		} else if shared > 0 && p.Priority > floor {
			why = fmt.Sprintf("deferred for fairness across priorities: %s of this tick's global limit of %s went to lower-priority pools whose jobs had waited a full scheduling interval; the next pass will continue",
				plural(shared, "create"), plural(t.policy.MaxCreatesPerTick, "new runner"))
		}
		pp.Reason = cannotScale(p.Name, pp.Current+got, pp.Desired, why)
	}
}

// shareAcrossTiers grants one create to every pool below the highest tier
// with demand whose oldest queued job has waited a full interval, before that
// tier is served. Without it a top-priority pool with a deep backlog takes
// the whole budget every pass and a lower tier waits for as long as the
// backlog lasts -- priority should order the fleet's work, not stop some of
// it. A lower tier that has not yet waited an interval takes nothing, so a
// burst at the top still gets the tick to itself.
//
// It returns how many creates it granted and the lowest priority that
// received one; a pool above that priority left short this tick was deferred
// by the share, and its reason says so.
func (t *tick) shareAcrossTiers(tiers []*store.Pool, byID map[string]*PoolPlan, runners map[string][]*store.Runner, demand map[string][]*store.Job) (granted, floor int) {
	if t.policy.Interval <= 0 || t.policy.MaxCreatesPerTick <= 0 {
		// An uncapped tick serves every tier anyway; there is nothing to share.
		return 0, 0
	}
	wants := func(p *store.Pool) bool {
		pp := byID[p.ID]
		return p.Enabled && pp.Desired > pp.Current+creates(pp.Actions) && pp.Failing == "" && pp.Held == ""
	}
	top, found := 0, false
	for _, p := range tiers {
		if wants(p) {
			top, found = p.Priority, true
			break
		}
	}
	if !found {
		return 0, 0
	}
	for _, p := range tiers {
		if t.budget == 0 {
			break
		}
		if p.Priority >= top || !wants(p) || !t.waitedAnInterval(demand[p.ID]) {
			continue
		}
		if t.grant(p, byID[p.ID], runners[p.ID], demand[p.ID]) {
			granted++
			// tiers is sorted highest first, so the last grant is the lowest.
			floor = p.Priority
		}
	}
	return granted, floor
}

// waitedAnInterval reports whether any of these queued jobs has waited at
// least one reconcile interval, measured against the snapshot's time.
func (t *tick) waitedAnInterval(queued []*store.Job) bool {
	for _, j := range queued {
		if !j.QueuedAt.IsZero() && t.now.Sub(j.QueuedAt) >= t.policy.Interval {
			return true
		}
	}
	return false
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
	var need Requirement
	var heldOff map[string]string
	if t.historySizing == HistoryOn || t.historySizing == HistoryShadow {
		need, plan.HistoryUnfit = t.poolNeed(p, queued)
		if need.Known() {
			heldOff = t.hosts.heldOff(p, need)
		}
	}
	enforce := t.historySizing == HistoryOn && need.Known()
	if enforce {
		t.hosts.need = &need
	}
	placed := t.hosts.placeAvoiding(p, 1, recentBusyHosts(runners, t.now))
	t.hosts.need = nil
	if len(placed) == 0 && enforce && len(heldOff) > 0 {
		// Some host would have taken a runner at the pool's own size, and
		// every one of them is short of what the jobs waiting are known to
		// need. That is a wait for a larger host to free up, not a fleet with
		// nowhere to go: poolNeed only asks for what some host has.
		held := t.hosts.sortedPhrases(heldOff)
		plan.History = held
		plan.Reason = cannotScale(p.Name, plan.Current+creates(plan.Actions), plan.Desired, held)
		plan.Blocked = held
		plan.BlockedFix = "wait for work on a larger host to finish, or add a host with that much room; the requirement is the jobs' own recent peaks, and scheduler.history_sizing=shadow places them as before"
		plan.BlockedAtCapacity = true
		return false
	}
	if len(placed) == 0 {
		b := t.hosts.why(p)
		plan.Reason = cannotScale(p.Name, plan.Current+creates(plan.Actions), plan.Desired, sentence(b.what, b.fix))
		plan.Blocked, plan.BlockedFix = b.what, b.fix
		plan.BlockedAtCapacity, plan.BlockedAlternatives = b.atCapacity, b.alternatives
		plan.BlockedNoEligibleHost = b.noEligibleHost
		return false
	}
	busy := 0
	for _, r := range runners {
		if r.State == store.RunnerBusy {
			busy++
		}
	}
	// The reason counts the jobs the pool is scaling for. Counting the queue
	// here instead used to say "3 jobs queued" for a pool the repository
	// limit let scale for one of them, which reads as a shortfall.
	delay := t.policy.For(p).ScaleUpDelay
	expedited := false
	for _, j := range queued {
		if j.ProvisionNow {
			expedited = true
			delay = 0
			break
		}
	}
	reason := upReason(p, busy, plan.eligible, delay)
	if expedited {
		reason += " (includes Run now demand)"
	}
	if plan.QuotaDeferredJobs > 0 {
		reason += fmt.Sprintf(" (%s deferred by the repository limit for %s)",
			plural(plan.QuotaDeferredJobs, "job"), strings.Join(plan.QuotaDeferredRepositories, ", "))
	}
	if size := placed[0].size; size != nil && placed[0].raised {
		reason += fmt.Sprintf(" (sized to %s)", sizePhrase(size.CPUs, size.MemoryMB))
	} else if size != nil {
		// Said in the reason as well as carried, so the Runners page and the
		// scaling history both show that this runner is smaller than its pool
		// asks for, and why.
		if p.Automatic() {
			// An automatic pool has no standard of its own to name: its
			// standard is a slot's share of whichever host it lands on.
			reason += fmt.Sprintf(" (reduced to %s: no host with a free slot had a whole share of it left)",
				sizePhrase(size.CPUs, size.MemoryMB))
		} else {
			reason += fmt.Sprintf(" (reduced to %s: no host had room for the standard %s)",
				sizePhrase(size.CPUs, size.MemoryMB), sizePhrase(p.Resources.CPUs, p.Resources.MemoryMB))
		}
	}
	switch {
	case enforce:
		note := fmt.Sprintf("placed for the jobs waiting, which need %s", need)
		if held := t.hosts.sortedPhrases(heldOff); held != "" {
			note += "; " + held
		}
		plan.History = note
		reason += " (" + note + ")"
	case need.Known():
		// Shadow: the placement stands, and what history sizing would have
		// done differently is recorded beside it rather than in the reason,
		// which describes what actually happened.
		if held, ok := heldOff[placed[0].hostID]; ok {
			plan.History = "shadow: history sizing would have " + held
		}
	}
	action := Action{Kind: ActionCreate, PoolID: p.ID, PoolName: p.Name, HostID: placed[0].hostID, Reason: reason, Size: placed[0].size, SizedFromHistory: placed[0].raised}
	plan.Actions = append(plan.Actions, action)
	t.creates = append(t.creates, action)
	t.budget--
	plan.Reason = scaled(p.Name, plan.Current, plan.Current+creates(plan.Actions), reason)
	return true
}

// reap removes from consideration the runners the scheduler can no longer count
// on, and returns the actions that clean them up. A busy runner is never
// touched here: only an explicit operator drain interrupts a running job.
func (t *tick) reap(p *store.Pool, runners []*store.Runner) (actions []Action, remaining []*store.Runner) {
	var removes, fails, retires []Action
	// The pool's own timings, where it has any. A pool that pulls a large
	// image and a pool that boots in seconds do not agree about how long
	// registering should take, and reaping both by the slower one leaves the
	// faster pool holding a dead runner's slot.
	pol := t.policy.For(p)
	for _, r := range runners {
		age := r.Age(t.now)
		switch {
		case r.State == store.RunnerRemoved:
			// Already gone; it neither costs capacity nor needs an action.
			// Whatever it left on its host is the agent's to clean up, and
			// it does so on its own once the retention window has passed.
		case r.State == store.RunnerFailed:
			// A failed runner holds no host slot, so there is nothing to free
			// by removing it quickly, and everything to lose: the message on
			// it is the only record of why it failed. It stays on the page
			// for the retention, then goes.
			//
			// Unless a job is still running on it. A host that went quiet
			// long enough to be given up as lost has its runners failed, and
			// when it comes back with the job still going the workload is
			// deliberately left to finish. Removing the row now would remove
			// the container under that job for no reason but tidiness; the
			// removal waits until the job has ended, which the reap sees on
			// a later pass.
			if t.now.Sub(failedAt(r)) >= failedRetention && !t.stillRunning[r.ID] {
				removes = append(removes, t.action(ActionRemove, p, r, fmt.Sprintf(
					"runner failed %s ago; its failure has been on the Runners page long enough",
					formatDuration(t.now.Sub(failedAt(r)).Truncate(time.Minute)))))
			}
		case starting(r.State) && pol.ProvisionTimeout > 0 && age > pol.ProvisionTimeout:
			fails = append(fails, t.action(ActionFail, p, r, fmt.Sprintf(
				"stuck in %s for %s, past the %s provision timeout; check the host's agent log",
				r.State, formatDuration(age), formatDuration(pol.ProvisionTimeout))))
		case r.State == store.RunnerDraining && r.CurrentJobID == "" &&
			pol.DrainTimeout > 0 && drainingFor(r, t.now) > pol.DrainTimeout:
			// Only a drain with nothing left to wait for. A runner draining
			// with a job still on it is doing exactly what a drain asks, for
			// as long as that job takes; there is no maximum job duration in
			// this system by design, and inventing one here would kill
			// somebody's build from the wrong end of the fleet.
			fails = append(fails, t.action(ActionFail, p, r, fmt.Sprintf(
				"draining with no job for %s, past the %s drain timeout; its stop was never carried out, so the slot is being taken back",
				formatDuration(drainingFor(r, t.now)), formatDuration(pol.DrainTimeout))))
		case pol.MaxRunnerLifetime > 0 && age > pol.MaxRunnerLifetime &&
			r.State != store.RunnerBusy && r.State != store.RunnerDraining && !t.stillRunning[r.ID]:
			retires = append(retires, t.action(ActionDrain, p, r, fmt.Sprintf(
				"runner reached the %s maximum lifetime",
				formatDuration(pol.MaxRunnerLifetime))))
		case staleImage(p, r) && r.State != store.RunnerBusy && r.State != store.RunnerDraining && !t.stillRunning[r.ID]:
			// The pool's page says one image and this runner was made from
			// another. A warm runner kept from before the change would take
			// the next job onto the old image -- for a pool that just gained
			// a Docker daemon, onto an image with no client for it -- while
			// every page says the pool is fixed. It is replaced instead; the
			// scale-up rules below make a new one from the right image in the
			// same tick when the pool still wants it.
			retires = append(retires, t.action(ActionDrain, p, r, fmt.Sprintf(
				"pool image is now %s; this runner was made from %s", p.Image, r.Image)))
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

// staleImage reports whether a runner was made from an image other than the
// one its pool now names. A pool with no image of its own runs the instance
// default, which the runner row records and the pool row does not, so those
// are never compared.
func staleImage(p *store.Pool, r *store.Runner) bool {
	return p.Image != "" && r.Image != "" && r.Image != p.Image
}

// disable drains a disabled pool to zero. Its busy runners are left alone, so
// disabling a pool never kills a job that is already running.
func (t *tick) disable(p *store.Pool, plan *PoolPlan, remaining []*store.Runner, live int) {
	plan.Desired = 0
	n := 0
	for _, r := range remaining {
		if r.State == store.RunnerBusy || r.State == store.RunnerDraining || t.stillRunning[r.ID] {
			continue
		}
		plan.Actions = append(plan.Actions, t.action(ActionDrain, p, r, "pool is disabled"))
		n++
	}
	if n > 0 {
		plan.Reason = scaled(p.Name, live, live-n, "pool is disabled")
	}
}

// scaleDown first stops surplus runners that have not finished starting, then
// drains idle runners that have waited out the pool's idle timeout. Starting
// capacity has never run a job and has no useful warmth to preserve: when
// queued demand disappears (most commonly because GitHub cancelled the job),
// making it finish booting only to wait through the idle timeout wastes the
// host resources the cancellation just released.
//
// A starting row whose job is already in progress is excluded. The job event
// normally advances that row to busy, but stillRunning is the durable guard
// for the short window where the row has not caught up yet.
func (t *tick) scaleDown(p *store.Pool, plan *PoolPlan, remaining []*store.Runner, live int) {
	surplus := live - max(plan.Desired, p.MinRunners)
	if surplus <= 0 {
		return
	}

	starting := t.unneededStarting(remaining)
	stopStarting := min(surplus, len(starting))
	for _, r := range starting[:stopStarting] {
		plan.Actions = append(plan.Actions, t.action(ActionDrain, p, r,
			"queued demand disappeared before this runner finished starting"))
	}
	surplus -= stopStarting

	idle := t.drainable(p, remaining)
	stopIdle := min(surplus, len(idle))
	timeout := p.IdleTimeout.Duration()
	for _, r := range idle[:stopIdle] {
		reason := "surplus idle runner"
		if timeout > 0 {
			reason = fmt.Sprintf("idle for %s, over the %s idle timeout",
				formatDuration(r.IdleFor(t.now)), formatDuration(timeout))
		}
		plan.Actions = append(plan.Actions, t.action(ActionDrain, p, r, reason))
	}

	stopped := stopStarting + stopIdle
	if stopped == 0 {
		return
	}
	var reasons []string
	if stopStarting > 0 {
		reasons = append(reasons, plural(stopStarting, "runner")+" still starting after demand disappeared")
	}
	if stopIdle > 0 {
		what := plural(stopIdle, "runner") + " idle"
		if timeout > 0 {
			what += " > " + formatDuration(timeout)
		}
		reasons = append(reasons, what)
	}
	plan.Reason = scaled(p.Name, live, live-stopped, strings.Join(reasons, " and "))
}

// unneededStarting orders capacity that may be cancelled before it becomes
// useful. Provisioning comes before registering, and newer rows come first:
// preserve the runner closest to being ready when only part of a burst became
// surplus. A job already known to be running always wins over a stale runner
// state and keeps its workload.
func (t *tick) unneededStarting(runners []*store.Runner) []*store.Runner {
	var out []*store.Runner
	for _, r := range runners {
		if starting(r.State) && !t.stillRunning[r.ID] {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b *store.Runner) int {
		if a.State != b.State {
			if a.State == store.RunnerProvisioning {
				return -1
			}
			return 1
		}
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// drainable returns the idle runners that have waited out the pool's idle
// timeout, longest-idle first so that the coldest runner goes first.
//
// A runner's row can read idle while a job is already running on it: the
// agent's report and GitHub's in_progress delivery arrive in either order,
// and an idle report that lands after the job started puts the row back to
// idle with the idle clock it had before. Drained on that row, the runner is
// stopped mid-job and killed when the stop times out -- a job GitHub reports
// as "the self-hosted runner lost communication", and the fleet as exit 137.
// A job in progress is the durable answer, as it is for a starting runner.
func (t *tick) drainable(p *store.Pool, runners []*store.Runner) []*store.Runner {
	timeout := p.IdleTimeout.Duration()
	var out []*store.Runner
	for _, r := range runners {
		if r.State == store.RunnerIdle && r.IdleFor(t.now) >= timeout && !t.stillRunning[r.ID] {
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

// hostSet tracks what is left on each host as the plan is built, so that two
// pools cannot both be promised the last free slot -- or the last four
// gigabytes -- on the same host.
type hostSet struct {
	readiness       map[string]map[string]time.Duration
	preferReadiness bool
	hosts           []*store.Host
	free            map[string]int
	observedCPU     map[string]float64
	warming         map[string]int
	// left is the room still unpromised on each host, and alloc what the host
	// had to begin with. They are separate because the second says which
	// figures were measured at all, and an unmeasured figure constrains
	// nothing.
	left  map[string]Reservation
	alloc map[string]store.HostAllocation
	// promisedMemory is the memory still unpromised on each host by
	// reservation alone, before measured headroom is taken into account, and
	// ours says whether the host is running or starting any runner of ours.
	// Together they let an idle host take a share the host itself chose
	// without its operating system's own use counting against it; see
	// memoryLeft.
	promisedMemory map[string]int64
	ours           map[string]bool
	now            time.Time
	// order is scheduler.host_order; see Snapshot.HostOrder.
	order string
	// need is set while a runner is being placed for a known requirement
	// under scheduler.history_sizing=on: a host that cannot hold it is not
	// eligible, and the runner is sized up to it where its pool allows.
	need *Requirement
}

// newHostSet seeds each host with its allocatable resources less what the
// runners already on it have been promised.
//
// Only live runners count, exactly as the slot count does: the snapshot keeps
// failed rows so the Runners page can show them, and charging a host for a
// runner that is gone would shrink the fleet every time one failed.
//
// Disk is the exception, and deliberately: free disk is a measurement of the
// filesystem as it is now, so what the runners already there have written is
// in the number already. Subtracting their reservations too would charge the
// same bytes twice. It is charged only for the runners this pass adds, which
// have written nothing yet.
func newHostSet(hosts []*store.Host, pools []*store.Pool, runners map[string][]*store.Runner, now time.Time) *hostSet {
	hs := &hostSet{
		hosts:          sortedHosts(hosts),
		free:           make(map[string]int, len(hosts)),
		observedCPU:    make(map[string]float64, len(hosts)),
		warming:        make(map[string]int, len(hosts)),
		left:           make(map[string]Reservation, len(hosts)),
		alloc:          make(map[string]store.HostAllocation, len(hosts)),
		promisedMemory: make(map[string]int64, len(hosts)),
		ours:           make(map[string]bool, len(hosts)),
		now:            now,
	}
	for _, h := range hs.hosts {
		hs.free[h.ID] = h.Free()
		a := h.Allocatable()
		hs.alloc[h.ID] = a
		hs.left[h.ID] = Reservation{CPUs: a.CPUs, MemoryMB: a.MemoryMB, DiskMB: a.DiskMB}
	}
	for _, h := range hs.hosts {
		// One sum, shared with the host view: what the page shows a host has
		// promised away and what a pass starts from must be the same number,
		// or an operator reads one figure and the scheduler acts on another.
		res := Reserved(h, pools, runners)
		l := hs.left[h.ID]
		l.CPUs -= res.CPUs
		l.MemoryMB -= res.MemoryMB
		hs.promisedMemory[h.ID] = l.MemoryMB
		hs.ours[h.ID] = hostRunsOurs(h, runners)
		pending := hs.pending(h, pools, runners)
		if h.Usage.Fresh(now) {
			if v := h.Usage.MemoryAvailableMB; v != nil {
				// The host sample already includes running work, and what the
				// operating system itself is using is the reserve's whole
				// job -- so the reserve is not taken off here. It is already
				// inside Allocatable, which is what a slot's size is measured
				// against; charging it a second time here makes measured
				// headroom permanently one reserve tighter than the figure a
				// pool asks for, and a host cut into a single slot then wants
				// its whole allocatable memory and can never have that much
				// left. That host reported "short of memory" at every pass,
				// at any size of machine, and its slot was never filled.
				// Subtract only starts whose demand the sample cannot yet
				// contain, then take the tighter of measured headroom and
				// reservation headroom.
				available := *v - pending.MemoryMB
				l.MemoryMB = min(l.MemoryMB, max(available, 0))
			}
			// CPU runners are using because it was lent them is not
			// taken: the next elastic plan gives it back to a start.
			if v := h.Usage.UnlentCPUPercent(); v != nil {
				hs.observedCPU[h.ID] = max(float64(h.CPUs)*(1-*v/100)-h.CPUReserve()-pending.CPUs, 0)
			}
		}
		hs.left[h.ID] = l
	}
	return hs
}

// placement is one runner placed by a pass: the host it goes on and, where no
// host had room for the pool's standard size, the smaller size it was given.
type placement struct {
	hostID string
	// size is nil for a runner at the pool's standard size.
	size *store.Resources
	// raised says size is larger than the standard, not smaller: the runner
	// was sized up to what the jobs waiting are known to need.
	raised bool
}

// place reserves up to n slots for the pool and returns the chosen host IDs.
// It may return fewer than n, or none at all, when the fleet is out of room.
func (hs *hostSet) place(p *store.Pool, n int) []string {
	var ids []string
	for _, pl := range hs.placeAvoiding(p, n, nil) {
		ids = append(ids, pl.hostID)
	}
	return ids
}

// placeAvoiding is place steered away from a set of hosts where another
// eligible one exists.
//
// It exists for the one create that already knows a specific host is a bad
// idea: a replacement for a job whose last runner just failed with
// FaultBackendBusy on that host. Avoiding it is worth doing only as long as
// somewhere else can take the runner -- pick still returns an avoided host
// rather than refuse the placement once it is the only one left, because a
// host that failed one create a few minutes ago is still better than no host
// at all.
//
// A runner goes at the pool's standard size wherever any host has room for
// it. Only when none has does a pool with a minimum fall back to a reduced
// size on the host that can spare the most -- so a minimum never shrinks a
// runner the fleet could have given the full size to.
func (hs *hostSet) placeAvoiding(p *store.Pool, n int, avoid map[string]bool) []placement {
	out := make([]placement, 0, n)
	for len(out) < n {
		var size *store.Resources
		h := hs.pick(p, avoid)
		res := Reservation{}
		if h != nil {
			res = Reserve(p, h)
		} else {
			var grant store.Resources
			h, res, grant = hs.pickReduced(p, avoid)
			if h == nil {
				break
			}
			size = &grant
		}
		raised := false
		if hs.need != nil {
			before := size
			res, size = sizeForNeed(p, res, size, *hs.need)
			raised = size != before
		}
		hs.free[h.ID]--
		hs.warming[h.ID]++
		l := hs.left[h.ID]
		l.CPUs -= res.CPUs
		l.MemoryMB -= res.MemoryMB
		l.DiskMB -= res.DiskMB
		hs.left[h.ID] = l
		hs.promisedMemory[h.ID] -= res.MemoryMB
		hs.ours[h.ID] = true
		if cpu, ok := hs.observedCPU[h.ID]; ok {
			hs.observedCPU[h.ID] = max(cpu-res.CPUs, 0)
		}
		out = append(out, placement{hostID: h.ID, size: size, raised: raised})
	}
	return out
}

// pickReduced is pick for a runner below its standard size: the host, among
// those that could take one otherwise, that can give it the most. It returns
// the host, the charge against it and the size to create the runner at, or a
// nil host when not even the minimum fits anywhere.
func (hs *hostSet) pickReduced(p *store.Pool, avoid map[string]bool) (*store.Host, Reservation, store.Resources) {
	if !p.Resources.Reducible() {
		return nil, Reservation{}, store.Resources{}
	}
	type option struct {
		h      *store.Host
		charge Reservation
		grant  store.Resources
		worth  float64
	}
	var best, bestAvoided *option
	for _, h := range hs.hosts {
		if hs.free[h.ID] <= 0 || !HostCanRun(h, p, hs.now) ||
			(hostUnderCPUPressure(h, hs.now) && hs.warming[h.ID] > 0) ||
			(hs.need != nil && hs.shortOf(h, p, *hs.need) != "") {
			continue
		}
		charge, grant, ok := ReducedSize(p, h, hs.leftFor(h, p), hs.alloc[h.ID])
		if !ok {
			continue
		}
		// How much of the standard this host can give, as a share of it: the
		// bigger the runner, the better the job goes. The standard is this
		// host's own, which for a field left to the host is its slot share.
		standard := Reserve(p, h)
		worth := 0.0
		if standard.CPUs > 0 {
			worth += charge.CPUs / standard.CPUs
		}
		if standard.MemoryMB > 0 {
			worth += float64(charge.MemoryMB) / float64(standard.MemoryMB)
		}
		o := &option{h: h, charge: charge, grant: grant, worth: worth}
		slot := &best
		if avoid[h.ID] {
			slot = &bestAvoided
		}
		if *slot == nil || o.worth > (*slot).worth || (o.worth == (*slot).worth && h.ID < (*slot).h.ID) {
			*slot = o
		}
	}
	if best == nil {
		best = bestAvoided
	}
	if best == nil {
		return nil, Reservation{}, store.Resources{}
	}
	return best.h, best.charge, best.grant
}

// pick prefers the host with the most proportional headroom after placing
// this pool's runner. It averages the CPU and memory fractions left, after
// eligibility has enforced every limit. The score is recomputed after every
// placement, across pools.
// Hosts with no CPU/memory specifications retain the slot rule; the host ID
// breaks exact ties.
//
// avoid is consulted only once every other eligible host has been ruled out:
// an avoided host is tracked alongside the rest and returned only when it is
// the best -- or only -- one left, so a hint to go elsewhere never turns into
// a refusal the fleet had room to avoid.
func (hs *hostSet) pick(p *store.Pool, avoid map[string]bool) *store.Host {
	var best, bestAvoided *store.Host
	for _, h := range hs.hosts {
		if !hs.eligible(h, p) {
			continue
		}
		if avoid[h.ID] {
			if bestAvoided == nil || hs.prefer(h, bestAvoided, p) {
				bestAvoided = h
			}
			continue
		}
		if best == nil || hs.prefer(h, best, p) {
			best = h
		}
	}
	if best != nil {
		return best
	}
	return bestAvoided
}

func (hs *hostSet) eligible(h *store.Host, p *store.Pool) bool {
	return hs.eligibleAsSized(h, p) && (hs.need == nil || hs.shortOf(h, p, *hs.need) == "")
}

// eligibleAsSized is eligible without a job history's requirement: whether h
// would take a runner of p at the pool's own size.
func (hs *hostSet) eligibleAsSized(h *store.Host, p *store.Pool) bool {
	return hs.free[h.ID] > 0 && hs.hasRoom(h, p) && HostCanRun(h, p, hs.now) &&
		!(hostUnderCPUPressure(h, hs.now) && hs.warming[h.ID] > 0)
}

// hasRoom reports whether what is still unpromised on the host covers one more
// runner of this pool.
func (hs *hostSet) hasRoom(h *store.Host, p *store.Pool) bool {
	return fits(hs.leftFor(h, p), Reserve(p, h), hs.alloc[h.ID])
}

// leftFor is the room on h as a runner of p sees it: left, with memory
// answered by memoryLeft.
func (hs *hostSet) leftFor(h *store.Host, p *store.Pool) Reservation {
	l := hs.left[h.ID]
	l.MemoryMB = hs.memoryLeft(h, p)
	return l
}

// memoryLeft is how much memory h can still give a runner of p.
//
// Usually the tighter of what is unpromised and what was measured free. The
// measurement is what catches work the reservations cannot see -- the runners
// already on the host using what they were promised, or something outside
// Zoomies using the machine -- and a runner must not be handed memory that is
// not there.
//
// The exception is an idle host and a pool that leaves its size to the host.
// Then the runner's share is carved out of allocatable, which is the machine
// less the reserve set aside for its own operating system -- and measured free
// memory is the machine less what that operating system actually uses. On a
// host whose system uses more than the reserve guessed, the second is always
// the smaller, so a share of the whole machine never fits it: a host of one
// slot refused its only slot at every pass, idle, with most of its memory
// free, and reported itself short of memory for it. Nothing of ours is there
// to protect, so the measurement has nothing to say that the pressure hold
// does not already say -- that stops every start once free memory falls to the
// reserve -- and reservation alone decides.
//
// A stated size keeps the measured check even on an idle host. An operator
// who wrote 8 GB meant the job needs it, and 6 GB free is not 8.
func (hs *hostSet) memoryLeft(h *store.Host, p *store.Pool) int64 {
	if p.Resources.MemoryMB <= 0 && !hs.ours[h.ID] {
		return hs.promisedMemory[h.ID]
	}
	return hs.left[h.ID].MemoryMB
}

// hostRunsOurs reports whether any live runner of ours is on h, in any pool.
func hostRunsOurs(h *store.Host, runners map[string][]*store.Runner) bool {
	for _, rs := range runners {
		for _, r := range rs {
			if r != nil && r.HostID == h.ID && r.State.Live() {
				return true
			}
		}
	}
	return false
}

// HostCanRun is the placement rule, in one place: a host may take a runner for
// a pool when it is available, offers the pool's backend and satisfies the
// pool's host selector, and is a machine one of its runners would fit on at
// all. The wizard's "matching hosts" count, the capacity-demand signal and
// image prewarming all ask this same question, and each used to answer it with
// a copy of its own that a new rule here would have left behind. Room left on
// the host is the scheduler's own accounting and is checked separately.
func HostCanRun(h *store.Host, p *store.Pool, now time.Time) bool {
	return HostAvailable(h, now) && HostCouldRun(h, p)
}

// HostCouldRun is that same rule with the weather left out: whether this host
// and this pool are configured to go together at all, whatever the host is
// doing at the moment.
//
// The split is what lets an edit be judged. Cordoning a host, or waiting for
// its agent, changes where a runner goes today; changing its labels or its
// reserve can change whether a pool has anywhere to go at all, and only the
// second is a conflict between two things an operator typed.
func HostCouldRun(h *store.Host, p *store.Pool) bool {
	return HostOffers(h, p) && HostIsPlatform(h, p) && HostSelects(h, p) && HostFits(h, p)
}

// HostIsPlatform reports whether a host is the machine the pool asked for.
//
// It is separate from HostSelects because it is not a selector: a selector is
// something an operator writes about their own fleet, and this is a promise the
// pool makes about what its runners are -- the same promise that picks the
// runner image. An empty field on either side constrains nothing, so a pool
// with no platform still goes anywhere and a host whose agent has not reported
// one is never ruled out.
func HostIsPlatform(h *store.Host, p *store.Pool) bool {
	return p.Platform.Matches(h.Platform())
}

// HostAvailable reports whether a host may take new runners at all: its agent
// is heartbeating, an operator has not cordoned it, and its agent speaks a
// protocol this controller understands. Fresh pressure measurements may also
// temporarily hold new starts without changing connectivity or the cordon.
//
// An incompatible agent is excluded here rather than refused at the door, so
// its existing runners keep working and are drained as normal. A fleet
// mid-upgrade shrinks host by host instead of falling over all at once.
func HostAvailable(h *store.Host, now time.Time) bool {
	return h.Healthy(now) && !h.Cordoned && !h.Incompatible && HostAdmissionReason(h, now) == ""
}

// HostOffers reports whether a host's agent offers the pool's backend.
func HostOffers(h *store.Host, p *store.Pool) bool {
	return slices.Contains(h.Backends, string(p.Backend))
}

// HostSelects reports whether a host satisfies the pool's host selector: every
// key of the selector answers with the value the selector asks for, and an
// empty selector means "any host".
//
// A host answers for its own labels and, for `os` and `arch`, for what its
// agent reported about the machine -- see Host.SelectorValue. So a pool may ask
// for arm64 or windows across a fleet nobody has labelled, which is the case
// this indirection exists for.
func HostSelects(h *store.Host, p *store.Pool) bool {
	for k, v := range p.HostSelector {
		if h.SelectorValue(k) != v {
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
	// noEligibleHost is its opposite: not one host could take this pool's
	// runner whatever it finished. Waiting does not clear this one -- only a
	// host that can run the pool does, whether an operator adds it or a
	// provider rents it.
	noEligibleHost bool
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
			// An empty fleet is the purest form of "no host can ever run this":
			// there is nothing to wait for, and it is exactly the shortfall a
			// host provisioner exists to answer.
			noEligibleHost: true,
		}
	}
	var unhealthy, cordoned, incompatible, backend, platform, selector, profile, tooSmall, full int
	var shortCPU, shortMemory, lowDisk, held, warming, throttled, committed int
	var detail string
	for _, h := range hs.hosts {
		switch {
		case !h.Healthy(hs.now):
			unhealthy++
		case h.Cordoned:
			cordoned++
		case h.Incompatible:
			incompatible++
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
		case !HostIsPlatform(h, p):
			platform++
		case !HostSelects(h, p):
			selector++
		case ExcludedBySize(h, p) != nil:
			// What the host's runner profile and the pool's size say about each
			// other, which no amount of waiting changes -- so it is asked before
			// the weather, and counted in neither of the flags that mean wait.
			profile++
			if detail == "" {
				detail = h.Name + ": " + ExcludedBySize(h, p).Reason
			}
		case HostAdmissionReason(h, hs.now) != "":
			held++
			if detail == "" {
				detail = h.Name + ": " + HostAdmissionReason(h, hs.now)
			}
		case hostUnderCPUPressure(h, hs.now) && hs.warming[h.ID] > 0:
			warming++
		case h.Throttle.Active() && hs.free[h.ID] <= 0 && h.ActiveRunners < h.Slots():
			// Slots the operator configured and the throttle has taken back.
			// It is its own count because "at capacity" would send an
			// operator to raise a capacity that is not the problem: the host
			// is being pushed too hard at the slots it already has.
			throttled++
			if detail == "" {
				detail = h.Name + ": " + ThrottleReason(h)
			}
		case hs.alloc[h.ID].DiskKnown && hs.alloc[h.ID].DiskMB <= 0:
			// The disk gate is asked before the sizing one, because a host at
			// its reserve refuses every pool and "too small for this pool's
			// limits" would send the operator to change limits that were never
			// the problem.
			lowDisk++
		case !HostFits(h, p):
			// The machine itself is too small for one runner of this pool, so
			// nothing finishing on it will ever make room. That is a different
			// answer from "wait", and an operator told to wait for it would
			// wait for ever.
			tooSmall++
		case hs.free[h.ID] <= 0:
			full++
		default:
			// It has a slot and is the right kind of machine, so what is left
			// on it is what ran out. Naming which resource is the difference
			// between adding memory and adding a host.
			//
			// It is measured against the minimum, because that is what the
			// pass has already failed to place: a host with memory for a
			// reduced runner and no CPU for one is short of CPU, and naming
			// memory would send the operator to add what the pool could
			// already run in.
			left, alloc, want := hs.leftFor(h, p), hs.alloc[h.ID], MinimumReserve(p, h)
			switch {
			case alloc.DiskKnown && (alloc.DiskMB <= 0 || left.DiskMB < want.DiskMB):
				lowDisk++
			case alloc.MemoryKnown && left.MemoryMB < want.MemoryMB:
				shortMemory++
				if hs.ours[h.ID] {
					committed++
				}
			case alloc.CPUsKnown && left.CPUs+cpuEpsilon < want.CPUs:
				shortCPU++
				if hs.ours[h.ID] {
					committed++
				}
			default:
				full++
			}
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
	add(incompatible, "running an agent this controller cannot talk to")
	add(backend, "without the "+string(p.Backend)+" backend")
	add(platform, "not "+p.Platform.Describe())
	add(selector, "not matching the pool's host selector")
	add(profile, "excluded by their runner profile")
	add(tooSmall, "too small for this pool's limits")
	add(shortMemory, "short of memory")
	add(shortCPU, "short of CPU")
	add(lowDisk, "low on disk")
	add(held, "holding new starts while host pressure clears")
	add(warming, "starting one runner at a time while CPU is busy")
	add(throttled, "throttled after sustained pressure")
	add(full, "at capacity")
	// onlyFull is the fleet whose every host is out of slots, as opposed to one
	// that is also short of memory or CPU. Both are "at capacity" to a caller,
	// but the fix text below tells them apart, so it asks this rather than
	// atCapacity.
	onlyFull := full+throttled == len(hs.hosts)
	b := blockage{
		what: fmt.Sprintf("no host can take a new %s runner (%s)",
			p.Backend, strings.Join(parts, ", ")),
		// A throttled host is full at the slots it has left, and to a
		// provisioner that is the same fact as full: more hosts is the one
		// answer that helps either way, so a fleet blocked only by throttles
		// asks for capacity exactly as a full one does.
		//
		// A host whose memory or CPU is promised to runners of ours is full in
		// the same sense: a free slot it cannot use until a job finishes and
		// returns what it holds. It counts only where a runner of ours is
		// there to return it -- a shortfall with none is pressure from outside
		// the fleet, which no job of ours finishing will ease, and it stays in
		// neither flag as it always has. Disk is never counted, because a
		// finishing runner leaves its caches behind on purpose.
		atCapacity: full+throttled+committed == len(hs.hosts),
		// Not one host was merely full, committed, throttled, held or warming,
		// so nothing finishing -- and no pressure easing -- frees a slot this
		// pool could use. The counts above already say which way each host
		// failed; this says only that none of them can be waited out.
		//
		// Full, committed and throttled hosts are waited out until a job ends
		// or the pressure eases, and a held or warming host lifts on its own;
		// each is a reason to wait rather than to buy. A machine takes minutes
		// to clone, and a hold raised by a thirty-second CPU spike is gone
		// before the clone finishes. Held and warming count in NEITHER flag on
		// purpose. In this one a held host would say no host could ever run
		// the pool, which is how a spike buys virtual machines and tells an
		// external provisioner to scale from zero; in atCapacity it would ask
		// for the same machines from the other direction. Counted in neither,
		// the pool is simply skipped and the next pass decides again, a minute
		// older and usually clear.
		noEligibleHost: full+throttled+committed+held+warming == 0,
	}
	if detail != "" {
		// The agent's own words about the backend it could not use. They name
		// the fix far more precisely than any count can.
		b.what += ". " + detail
	}
	switch {
	case throttled > 0 && held+warming == 0:
		// A throttle outlasts the pressure that raised it, so "wait for it
		// to clear" is only half the answer: a host that keeps being pushed
		// past its size is a host with too many slots, or pools whose limits
		// let a job take more of it than a slot's worth.
		b.fix = "wait for the throttle to lift, lower those hosts' capacity or the pools' limits so their runners fit the machine, or add a host; running jobs continue"
	case held+warming > 0:
		b.fix = "wait for host pressure to clear, reduce other work on those hosts, or add a compatible host; running jobs continue"
	case onlyFull:
		b.fix = "wait for a job to finish, raise a host's capacity, or add a host"
	case lowDisk > 0 && lowDisk+unhealthy+cordoned == len(hs.hosts):
		// Disk is the one of the three that no job finishing will return: a
		// runner that exits leaves its caches behind on purpose, so telling an
		// operator to wait here is telling them to wait for nothing.
		b.fix = "free space on those hosts' work directories, lower the pool's disk request, or add a host"
	case profile > 0 && profile+unhealthy+cordoned == len(hs.hosts):
		// Both halves are things an operator wrote, so both are named: either
		// can be changed, and which one is right is theirs to say.
		b.fix = "change those hosts' minimum or standard runner size, or this pool's size or minimum, or add a host whose profile suits it"
	case tooSmall > 0 && tooSmall+unhealthy+cordoned == len(hs.hosts):
		b.fix = "lower this pool's CPU or memory limits, or add a host large enough to run one"
	case shortMemory+shortCPU > 0 && shortMemory+shortCPU+unhealthy+cordoned == len(hs.hosts):
		switch {
		case p.Automatic() && !p.Resources.Reducible():
			// An automatic pool has no limits to lower. What it can be given
			// is a minimum, which lets a runner start with what is left of a
			// host's slot rather than waiting for a whole one.
			b.fix = "wait for a runner to finish, give this pool a minimum CPU and memory so a runner can start with less than a whole share of a host, or add a host"
		case p.Resources.Reducible():
			b.fix = "wait for a runner to finish, lower this pool's minimum, or add a host"
		default:
			b.fix = "wait for a runner to finish, lower this pool's limits, or add a host"
		}
	case incompatible > 0 && incompatible+unhealthy+cordoned == len(hs.hosts):
		// The one blockage whose fix is a version rather than a resource, and
		// the one an operator is least likely to guess: nothing is broken, the
		// hosts are up, and the fleet has simply stopped placing on them.
		b.fix = "upgrade the agent on those hosts to this controller's release; their existing runners keep working and are drained as normal"
	case unhealthy == len(hs.hosts):
		b.fix = "check that the zoomies agent is running on those hosts and can reach this controller"
	case platform > 0 && platform+unhealthy+cordoned == len(hs.hosts):
		// Naming the platform is the difference between an operator adding a
		// host and adding the *right* one: a pool that wants Ubuntu 24.04
		// arm64 will keep failing to place on the amd64 box they just built
		// unless the message says so.
		b.fix = fmt.Sprintf("add a %s host, or change this pool's platform to one your fleet already has",
			p.Platform.Describe())
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
		if hs.free[h.ID] <= 0 || !h.Healthy(hs.now) || h.Cordoned || h.Incompatible || !HostSelects(h, p) {
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
		if hs.free[h.ID] > 0 && h.Healthy(hs.now) && !h.Cordoned && !h.Incompatible &&
			slices.Contains(h.Backends, kind) && HostSelects(h, p) {
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

// FormatDuration renders a duration the way every scheduler sentence does, so
// a caller outside the package that names one of its constants -- the wizard
// naming the throttle's recovery -- reads the same as the host card.
func FormatDuration(d time.Duration) string { return formatDuration(d) }

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
		if a.ProvisionNow != b.ProvisionNow {
			if a.ProvisionNow {
				return -1
			}
			return 1
		}
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
