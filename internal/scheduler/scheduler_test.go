package scheduler

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// now is the decision time every test uses; ages are expressed relative to it.
var now = time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

// testPolicy is the shipped default policy, with the scale-up delay off so that
// a test opts into it explicitly.
func testPolicy() Policy {
	return Policy{
		ScaleUpDelay:      0,
		MaxRunnerLifetime: 6 * time.Hour,
		ProvisionTimeout:  5 * time.Minute,
		DrainTimeout:      15 * time.Minute,
		MaxCreatesPerTick: 10,
	}
}

// testInstallation is the one GitHub App installation almost every test runs
// under: a pool and a job that share it are eligible for each other, so a case
// that says nothing about installations exercises the label rule alone. The
// cross-installation cases name their own.
const testInstallation = "ins_acme"

func testPool(name string, labels ...string) *store.Pool {
	return &store.Pool{
		ID:             "pool_" + name,
		Name:           name,
		InstallationID: testInstallation,
		Labels:         store.NormalizeLabels(labels),
		Backend:        store.BackendDocker,
		MaxRunners:     10,
		IdleTimeout:    store.Duration(5 * time.Minute),
		Ephemeral:      true,
		Enabled:        true,
	}
}

func testHost(id string, capacity, active int) *store.Host {
	return &store.Host{
		ID: id, Name: id, Capacity: capacity, ActiveRunners: active,
		Backends: store.StringSlice{"docker"}, LastHeartbeat: now,
	}
}

func testRunner(id string, p *store.Pool, state store.RunnerState, age time.Duration) *store.Runner {
	return &store.Runner{ID: id, PoolID: p.ID, HostID: "host_a", Name: id, State: state, CreatedAt: ago(age)}
}

// idleRunner is a runner that has been idle for idleFor and alive a little
// longer, which is the shape every scale-down test needs.
func idleRunner(id string, p *store.Pool, idleFor time.Duration) *store.Runner {
	r := testRunner(id, p, store.RunnerIdle, idleFor+time.Minute)
	since := ago(idleFor)
	r.LastIdleAt = &since
	return r
}

func queued(id string, waited time.Duration, labels ...string) *store.Job {
	return &store.Job{
		ID: id, Repo: "acme/widgets", JobName: id, State: store.JobQueued,
		InstallationID: testInstallation,
		Labels:         store.StringSlice(labels), QueuedAt: ago(waited),
	}
}

// snap assembles a snapshot, filing each runner under its own pool.
func snap(pools []*store.Pool, runners []*store.Runner, jobs []*store.Job, hosts []*store.Host) Snapshot {
	byPool := map[string][]*store.Runner{}
	for _, r := range runners {
		byPool[r.PoolID] = append(byPool[r.PoolID], r)
	}
	return Snapshot{Now: now, Pools: pools, Runners: byPool, Jobs: jobs, Hosts: hosts,
		Installations: []*store.Installation{{ID: testInstallation, Target: "acme", TargetType: store.TargetOrg}},
		Policy:        testPolicy()}
}

func actionsOf(as []Action, kind ActionKind) []Action {
	var out []Action
	for _, a := range as {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// A runner that just failed to start with FaultBackendBusy is not sent
// straight back to the host that failed it: grant steers the next create for
// this pool toward any other eligible host instead, via recentBusyHosts, so
// one host having a busy moment does not collect every retry in a row.
func TestACreateAvoidsTheHostItsPoolsLastRunnerFailedBusyOn(t *testing.T) {
	p := testPool("shared", "self-hosted")
	failed := testRunner("r_busy", p, store.RunnerFailed, time.Minute)
	failed.HostID = "host_a"
	failed.FaultKind = store.FaultBackendBusy
	job := queued("job-1", time.Minute, "self-hosted")
	s := snap([]*store.Pool{p}, []*store.Runner{failed}, []*store.Job{job},
		[]*store.Host{testHost("host_a", 4, 0), testHost("host_b", 4, 0)})

	pp := only(t, Decide(s))
	creates := actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(creates))
	}
	if creates[0].HostID != "host_b" {
		t.Fatalf("HostID = %q, want the host that did not just fail this pool a create", creates[0].HostID)
	}
}

// Avoiding the host that just failed is a preference, not a refusal: when it
// is the only host this pool can use, the create still goes there rather than
// leaving the job queued for a host that is never going to appear.
func TestACreateStillUsesTheOnlyHostAfterABusyFailureThere(t *testing.T) {
	p := testPool("shared", "self-hosted")
	failed := testRunner("r_busy", p, store.RunnerFailed, time.Minute)
	failed.HostID = "host_a"
	failed.FaultKind = store.FaultBackendBusy
	job := queued("job-1", time.Minute, "self-hosted")
	s := snap([]*store.Pool{p}, []*store.Runner{failed}, []*store.Job{job},
		[]*store.Host{testHost("host_a", 4, 0)})

	pp := only(t, Decide(s))
	creates := actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 || creates[0].HostID != "host_a" {
		t.Fatalf("creates = %+v, want one create still using the pool's only host", creates)
	}
}

// A non-busy start failure says nothing about which host to avoid: an image
// that will not pull fails the same way on every host, so steering away from
// this one would only rebalance a failure that is not the host's fault.
func TestANonBusyStartFailureDoesNotSteerTheNextCreate(t *testing.T) {
	p := testPool("shared", "self-hosted")
	failed := testRunner("r_bad_image", p, store.RunnerFailed, time.Minute)
	failed.HostID = "host_a"
	failed.FaultKind = store.FaultImage
	job := queued("job-1", time.Minute, "self-hosted")
	s := snap([]*store.Pool{p}, []*store.Runner{failed}, []*store.Job{job},
		[]*store.Host{testHost("host_a", 4, 0), testHost("host_b", 4, 0)})

	pp := only(t, Decide(s))
	creates := actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 || creates[0].HostID != "host_a" {
		t.Fatalf("creates = %+v, want the ordinary best-headroom host, unsteered by a non-busy failure", creates)
	}
}

func TestRepositoryScaleUpLimitDoesNotConsumeAnotherRepositoriesCapacity(t *testing.T) {
	p := testPool("shared", "self-hosted")
	p.RepositoryScaleUpLimit = 1
	blocked := queued("blocked", time.Minute, "self-hosted")
	other := queued("other", time.Minute, "self-hosted")
	other.Repo = "acme/other"
	s := snap([]*store.Pool{p}, nil, []*store.Job{blocked, other}, []*store.Host{testHost("host_a", 2, 0)})
	s.ActiveByRepository = map[string]int{p.ID + "\x00acme/widgets": 1}
	plan := Decide(s)
	if got := len(actionsOf(plan.Actions, ActionCreate)); got != 1 {
		t.Fatalf("creates = %d, want one for unblocked repository", got)
	}
	if plan.Pools[0].Blocked != "" {
		t.Fatalf("blocked = %q, want admitted work to keep the pool unblocked", plan.Pools[0].Blocked)
	}
	if plan.Pools[0].QuotaDeferredJobs != 1 || !slices.Equal(plan.Pools[0].QuotaDeferredRepositories, []string{"acme/widgets"}) {
		t.Fatalf("quota deferral = %+v", plan.Pools[0])
	}
}

func TestRepositoryScaleUpLimitWithWarmIdleRunnerIsOnlyAThrottle(t *testing.T) {
	p := testPool("shared", "self-hosted")
	p.RepositoryScaleUpLimit = 1
	idle := idleRunner("warm", p, time.Minute)
	job := queued("deferred", time.Minute, "self-hosted")
	s := snap([]*store.Pool{p}, []*store.Runner{idle}, []*store.Job{job}, []*store.Host{testHost("host_a", 2, 1)})
	s.ActiveByRepository = map[string]int{p.ID + "\x00" + job.Repo: 1}

	pp := only(t, Decide(s))
	if pp.Blocked != "" || countOf(pp.Actions, ActionCreate) != 0 {
		t.Fatalf("plan = %+v, want a deferral rather than pool blockage or creation", pp)
	}
	if pp.QuotaDeferredJobs != 1 || pp.Current != 1 || pp.Desired != 0 {
		t.Fatalf("plan = %+v, want the not-yet-expired idle runner retained and the job reported deferred", pp)
	}
}

func TestRepositoryScaleUpLimitPreservesMinimumForNonEphemeralPool(t *testing.T) {
	p := testPool("durable", "self-hosted")
	p.RepositoryScaleUpLimit, p.MinRunners, p.Ephemeral = 1, 2, false
	job := queued("deferred", time.Minute, "self-hosted")
	s := snap([]*store.Pool{p}, nil, []*store.Job{job}, []*store.Host{testHost("host_a", 4, 0)})
	s.ActiveByRepository = map[string]int{p.ID + "\x00" + job.Repo: 1}

	pp := only(t, Decide(s))
	if pp.Desired != 2 || countOf(pp.Actions, ActionCreate) != 2 {
		t.Fatalf("plan = %+v, want min_runners to create two durable warm runners", pp)
	}
	if pp.QuotaDeferredJobs != 1 || pp.Blocked != "" {
		t.Fatalf("plan = %+v, want quota metadata separate from blocked", pp)
	}
}

// A job that has not waited out the scale-up delay is not deferred by the
// repository limit; it is just new.
//
// Both make a job wait, and only one of them is something an operator can do
// anything about. Charging the limit for a job the delay was holding anyway
// put "1 job deferred by the repository limit for acme/widgets" on a pool
// whose queue the next few seconds were going to release on its own, and sent
// whoever read it to raise a setting that was not the cause.
func TestTheRepositoryLimitIsNotBlamedForTheScaleUpDelay(t *testing.T) {
	p := testPool("shared", "self-hosted")
	p.RepositoryScaleUpLimit = 1
	fresh := queued("fresh", time.Second, "self-hosted")

	s := snap([]*store.Pool{p}, nil, []*store.Job{fresh}, []*store.Host{testHost("host_a", 2, 0)})
	s.Policy.ScaleUpDelay = 30 * time.Second
	s.ActiveByRepository = map[string]int{p.ID + "\x00" + fresh.Repo: 1}

	pp := only(t, Decide(s))
	if pp.QuotaDeferredJobs != 0 || len(pp.QuotaDeferredRepositories) != 0 {
		t.Fatalf("plan = %+v, want a job inside the scale-up delay charged to the delay, not the repository limit", pp)
	}
	if countOf(pp.Actions, ActionCreate) != 0 {
		t.Fatal("a job inside the scale-up delay drove a create")
	}

	// The same job, once it is old enough, is the limit's to defer.
	s.Jobs = []*store.Job{queued("fresh", time.Minute, "self-hosted")}
	pp = only(t, Decide(s))
	if pp.QuotaDeferredJobs != 1 || !slices.Equal(pp.QuotaDeferredRepositories, []string{"acme/widgets"}) {
		t.Fatalf("plan = %+v, want the job deferred by the repository limit once it is past the delay", pp)
	}
}

func countOf(as []Action, kind ActionKind) int { return len(actionsOf(as, kind)) }

func runnerIDs(as []Action, kind ActionKind) []string {
	var out []string
	for _, a := range actionsOf(as, kind) {
		out = append(out, a.RunnerID)
	}
	return out
}

func hostIDs(as []Action) []string {
	var out []string
	for _, a := range actionsOf(as, ActionCreate) {
		out = append(out, a.HostID)
	}
	return out
}

// only returns the single pool plan in a plan, failing when there is not
// exactly one.
func only(t *testing.T, p Plan) PoolPlan {
	t.Helper()
	if len(p.Pools) != 1 {
		t.Fatalf("expected 1 pool plan, got %d", len(p.Pools))
	}
	return p.Pools[0]
}

// ---------------------------------------------------------------------------
// Scale up
// ---------------------------------------------------------------------------

func TestScaleUpFromZero(t *testing.T) {
	p := testPool("linux-x64", "linux", "x64")
	s := snap([]*store.Pool{p}, nil,
		[]*store.Job{
			queued("j1", time.Minute, "self-hosted", "linux", "x64"),
			queued("j2", time.Minute, "self-hosted", "linux", "x64"),
			queued("j3", time.Minute, "self-hosted", "linux", "x64"),
		},
		[]*store.Host{testHost("host_a", 8, 0)})

	plan := Decide(s)
	pp := only(t, plan)
	if pp.Current != 0 || pp.Desired != 3 || pp.QueuedMatched != 3 {
		t.Fatalf("got current=%d desired=%d queued=%d, want 0/3/3", pp.Current, pp.Desired, pp.QueuedMatched)
	}
	if n := countOf(pp.Actions, ActionCreate); n != 3 {
		t.Fatalf("got %d creates, want 3", n)
	}
	if want := "scaled linux-x64 0 -> 3: 3 jobs queued"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
	for _, a := range pp.Actions {
		if a.HostID != "host_a" || a.PoolID != p.ID || a.PoolName != p.Name || a.RunnerID != "" {
			t.Fatalf("unexpected create action %+v", a)
		}
	}
	if len(plan.Actions) != len(pp.Actions) {
		t.Fatalf("flattened actions = %d, want %d", len(plan.Actions), len(pp.Actions))
	}
	if len(plan.Unmatched) != 0 {
		t.Fatalf("unexpected unmatched jobs: %v", plan.Unmatched)
	}
}

func TestScaleUpReasonNamesTheDelay(t *testing.T) {
	p := testPool("linux-x64", "linux", "x64")
	s := snap([]*store.Pool{p}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux")},
		[]*store.Host{testHost("host_a", 8, 0)})
	s.Policy.ScaleUpDelay = 30 * time.Second

	pp := only(t, Decide(s))
	if want := "scaled linux-x64 0 -> 2: 2 jobs queued > 30s"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
	if got := actionsOf(pp.Actions, ActionCreate)[0].Reason; got != "2 jobs queued > 30s" {
		t.Fatalf("action reason = %q", got)
	}
}

func TestScaleUpRespectsScaleUpDelay(t *testing.T) {
	p := testPool("linux-x64", "linux")
	s := snap([]*store.Pool{p}, nil,
		[]*store.Job{queued("j1", time.Second, "linux")},
		[]*store.Host{testHost("host_a", 8, 0)})
	s.Policy.ScaleUpDelay = 30 * time.Second

	pp := only(t, Decide(s))
	if len(pp.Actions) != 0 || pp.Reason != "" {
		t.Fatalf("a job queued 1s ago scaled the pool: %+v", pp)
	}
	if pp.Desired != 0 {
		t.Fatalf("desired = %d, want 0", pp.Desired)
	}
	if pp.QueuedMatched != 1 {
		t.Fatalf("queued matched = %d, want 1: the job is still demand, just not yet actionable", pp.QueuedMatched)
	}
}

func TestScaleUpAtTheDelayBoundary(t *testing.T) {
	p := testPool("linux-x64", "linux")
	s := snap([]*store.Pool{p}, nil,
		[]*store.Job{queued("j1", 30*time.Second, "linux")},
		[]*store.Host{testHost("host_a", 8, 0)})
	s.Policy.ScaleUpDelay = 30 * time.Second

	if n := countOf(only(t, Decide(s)).Actions, ActionCreate); n != 1 {
		t.Fatalf("a job queued for exactly the delay produced %d creates, want 1", n)
	}
}

func TestScaleUpRespectsPoolMax(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MaxRunners = 2
	jobs := []*store.Job{
		queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux"),
		queued("j3", time.Minute, "linux"), queued("j4", time.Minute, "linux"),
	}
	pp := only(t, Decide(snap([]*store.Pool{p}, nil, jobs, []*store.Host{testHost("host_a", 8, 0)})))

	if pp.Desired != 2 {
		t.Fatalf("desired = %d, want 2 (the pool max)", pp.Desired)
	}
	if n := countOf(pp.Actions, ActionCreate); n != 2 {
		t.Fatalf("got %d creates, want 2", n)
	}
	if want := "scaled linux-x64 0 -> 2: 4 jobs queued"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
}

func TestScaleUpRespectsMaxCreatesPerTick(t *testing.T) {
	p := testPool("linux-x64", "linux")
	var jobs []*store.Job
	for _, id := range []string{"j1", "j2", "j3", "j4", "j5"} {
		jobs = append(jobs, queued(id, time.Minute, "linux"))
	}
	s := snap([]*store.Pool{p}, nil, jobs, []*store.Host{testHost("host_a", 8, 0)})
	s.Policy.MaxCreatesPerTick = 2

	pp := only(t, Decide(s))
	if n := countOf(pp.Actions, ActionCreate); n != 2 {
		t.Fatalf("got %d creates, want 2", n)
	}
	if pp.Desired != 5 {
		t.Fatalf("desired = %d, want 5: the cap limits this tick, not the target", pp.Desired)
	}
	if want := "cannot scale linux-x64 2 -> 5: this tick's global limit of 2 new runners is exhausted; the next pass will continue"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
}

func TestMaxCreatesPerTickIsAFleetBudget(t *testing.T) {
	first, second := testPool("aaa", "aaa"), testPool("zzz", "zzz")
	s := snap([]*store.Pool{second, first},
		nil,
		[]*store.Job{queued("j1", time.Minute, "aaa"), queued("j2", time.Minute, "zzz")},
		[]*store.Host{testHost("host_a", 8, 0)})
	s.Policy.MaxCreatesPerTick = 1

	plan := Decide(s)
	if len(plan.Actions) != 1 || plan.Actions[0].PoolName != "aaa" {
		t.Fatalf("the budget was not spent on the first pool by name: %+v", plan.Actions)
	}
	want := "cannot scale zzz 0 -> 1: this tick's global limit of 1 new runner is exhausted; the next pass will continue"
	if got := plan.Pools[1].Reason; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestCreateBudgetIsRoundRobinAcrossUnequalShortfalls(t *testing.T) {
	a, b, c := testPool("aaa", "aaa"), testPool("bbb", "bbb"), testPool("ccc", "ccc")
	jobs := []*store.Job{queued("a1", time.Minute, "aaa"), queued("a2", time.Minute, "aaa"), queued("a3", time.Minute, "aaa"),
		queued("b1", time.Minute, "bbb"), queued("c1", time.Minute, "ccc"), queued("c2", time.Minute, "ccc")}
	s := snap([]*store.Pool{c, a, b}, nil, jobs, []*store.Host{testHost("host_a", 20, 0)})
	s.Policy.MaxCreatesPerTick = 4

	plan := Decide(s)
	want := map[string]int{"aaa": 2, "bbb": 1, "ccc": 1}
	for _, pp := range plan.Pools {
		if got := countOf(pp.Actions, ActionCreate); got != want[pp.PoolName] {
			t.Errorf("%s creates = %d, want %d", pp.PoolName, got, want[pp.PoolName])
		}
	}
	if !strings.Contains(plan.Pools[2].Reason, "global limit") {
		t.Fatalf("deferred pool reason = %q, want global budget", plan.Pools[2].Reason)
	}
}

func TestCreateBudgetHonoursPriorityAndReportsCapacitySeparately(t *testing.T) {
	highA, highB := testPool("high-a", "ha"), testPool("high-b", "hb")
	low := testPool("low", "low")
	highA.Priority, highB.Priority, low.Priority = 10, 10, 0
	jobs := []*store.Job{queued("ha1", time.Minute, "ha"), queued("ha2", time.Minute, "ha"),
		queued("hb1", time.Minute, "hb"), queued("hb2", time.Minute, "hb"), queued("low1", time.Minute, "low")}
	s := snap([]*store.Pool{low, highB, highA}, nil, jobs, []*store.Host{testHost("host_a", 3, 0)})
	s.Policy.MaxCreatesPerTick = 5

	plan := Decide(s)
	if countOf(plan.Pools[0].Actions, ActionCreate) != 2 || countOf(plan.Pools[1].Actions, ActionCreate) != 1 {
		t.Fatalf("high-priority tier was not served round-robin: %+v", plan.Actions)
	}
	if plan.Pools[1].Blocked == "" || !strings.Contains(plan.Pools[1].Reason, "at capacity") {
		t.Fatalf("capacity-deferred high pool = %+v, want host-capacity reason", plan.Pools[1])
	}
	if plan.Pools[2].Blocked == "" || !strings.Contains(plan.Pools[2].Reason, "at capacity") {
		t.Fatalf("capacity-deferred low pool = %+v, want host-capacity reason", plan.Pools[2])
	}
}

func TestMinRunnersScalesUpWithoutDemand(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MinRunners = 2
	pp := only(t, Decide(snap([]*store.Pool{p}, nil, nil, []*store.Host{testHost("host_a", 8, 0)})))

	if pp.Desired != 2 || countOf(pp.Actions, ActionCreate) != 2 {
		t.Fatalf("got desired=%d creates=%d, want 2/2", pp.Desired, countOf(pp.Actions, ActionCreate))
	}
	if want := "scaled linux-x64 0 -> 2: pool minimum is 2 runners"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
}

// Pool membership is a live predicate, not a list captured by the pool wizard.
// A host that joins after the pool was created must be considered on the very
// next pass when its backend, platform and selector values fit the pool.
func TestNewMatchingHostAutomaticallyExpandsAnExistingPool(t *testing.T) {
	p := testPool("linux", "self-hosted")
	p.MinRunners = 1
	p.HostSelector = store.StringMap{"arch": "arm64"}

	before := only(t, Decide(snap([]*store.Pool{p}, nil, nil, nil)))
	if !strings.Contains(before.Blocked, "no agent hosts") {
		t.Fatalf("before host joined = %+v, want the existing pool blocked on having no host", before)
	}

	joined := testHost("new-host", 4, 0)
	joined.Arch = "arm64"
	after := only(t, Decide(snap([]*store.Pool{p}, nil, nil, []*store.Host{joined})))
	if after.Blocked != "" || countOf(after.Actions, ActionCreate) != 1 {
		t.Fatalf("after matching host joined = %+v, want one runner created without editing the pool", after)
	}
	if after.Actions[0].HostID != joined.ID {
		t.Fatalf("runner host = %q, want newly joined host %q", after.Actions[0].HostID, joined.ID)
	}
}

func TestIdleRunnersAbsorbQueuedJobs(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{idleRunner("r1", p, time.Minute), idleRunner("r2", p, time.Minute)}
	jobs := []*store.Job{queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux")}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, jobs, []*store.Host{testHost("host_a", 8, 2)})))
	if len(pp.Actions) != 0 || pp.Reason != "" {
		t.Fatalf("idle runners did not absorb the queue: %+v", pp)
	}
	if pp.Current != 2 || pp.Desired != 2 {
		t.Fatalf("got current=%d desired=%d, want 2/2", pp.Current, pp.Desired)
	}
}

func TestBusyRunnersDoNotAbsorbQueuedJobs(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{testRunner("r1", p, store.RunnerBusy, time.Minute)}
	jobs := []*store.Job{queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux")}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, jobs, []*store.Host{testHost("host_a", 8, 1)})))
	if pp.Desired != 3 {
		t.Fatalf("desired = %d, want 3 (1 busy + 2 queued)", pp.Desired)
	}
	if n := countOf(pp.Actions, ActionCreate); n != 2 {
		t.Fatalf("got %d creates, want 2", n)
	}
}

// A runner that has been told to stop will never pick up a job, so it cannot
// stand in for the one a queued job needs. A drain that hangs used to starve
// the pool while it looked healthy: one draining runner absorbed one queued
// job's demand until the drain completed.
func TestDrainingRunnersDoNotAbsorbQueuedJobs(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{testRunner("r1", p, store.RunnerDraining, time.Minute)}
	jobs := []*store.Job{queued("j1", time.Minute, "linux")}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, jobs, []*store.Host{testHost("host_a", 8, 1)})))
	if pp.Desired != 2 {
		t.Fatalf("desired = %d, want 2 (1 draining + 1 queued)", pp.Desired)
	}
	if n := countOf(pp.Actions, ActionCreate); n != 1 {
		t.Fatalf("got %d creates, want 1: a draining runner is not capacity for a queued job", n)
	}
}

// The other half of the same rule: until the drain finishes the runner still
// occupies a slot, so a pool at its maximum waits rather than overshooting.
func TestDrainingRunnerStillCountsAgainstTheMaximum(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MaxRunners = 1
	runners := []*store.Runner{testRunner("r1", p, store.RunnerDraining, time.Minute)}
	jobs := []*store.Job{queued("j1", time.Minute, "linux")}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, jobs, []*store.Host{testHost("host_a", 8, 1)})))
	if n := countOf(pp.Actions, ActionCreate); n != 0 {
		t.Fatalf("got %d creates, want 0: the slot is not free until the drain finishes", n)
	}
}

func TestProvisioningRunnersCountTowardsDemand(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{
		testRunner("r1", p, store.RunnerProvisioning, time.Minute),
		testRunner("r2", p, store.RunnerRegistering, time.Minute),
	}
	jobs := []*store.Job{
		queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux"),
		queued("j3", time.Minute, "linux"),
	}
	pp := only(t, Decide(snap([]*store.Pool{p}, runners, jobs, []*store.Host{testHost("host_a", 8, 2)})))

	if n := countOf(pp.Actions, ActionCreate); n != 1 {
		t.Fatalf("got %d creates, want 1: runners already on the way count", n)
	}
}

func TestCancelledDemandStopsSurplusStartingRunnersImmediately(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.IdleTimeout = store.Duration(30 * time.Minute)
	runners := []*store.Runner{
		testRunner("registering-old", p, store.RunnerRegistering, 2*time.Minute),
		testRunner("provisioning-old", p, store.RunnerProvisioning, 90*time.Second),
		testRunner("provisioning-new", p, store.RunnerProvisioning, time.Minute),
	}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, nil,
		[]*store.Host{testHost("host_a", 8, 3)})))
	drains := actionsOf(pp.Actions, ActionDrain)
	if got := runnerIDs(pp.Actions, ActionDrain); !slices.Equal(got, []string{"provisioning-new", "provisioning-old", "registering-old"}) {
		t.Fatalf("drained %v, want newest provisioning capacity first and registering last", got)
	}
	for _, a := range drains {
		if !strings.Contains(a.Reason, "demand disappeared") {
			t.Fatalf("drain reason = %q, want it to explain the cancelled demand", a.Reason)
		}
	}
	if !strings.Contains(pp.Reason, "still starting after demand disappeared") {
		t.Fatalf("pool reason = %q", pp.Reason)
	}
}

func TestStartingRunnerForAnInProgressJobIsNeverCancelledAsSurplus(t *testing.T) {
	p := testPool("linux-x64", "linux")
	r := testRunner("behind", p, store.RunnerProvisioning, time.Minute)
	running := &store.Job{
		ID: "job-1", PoolID: p.ID, RunnerID: r.ID, State: store.JobInProgress,
		Repo: "acme/widgets", InstallationID: testInstallation,
	}

	pp := only(t, Decide(snap([]*store.Pool{p}, []*store.Runner{r}, []*store.Job{running},
		[]*store.Host{testHost("host_a", 8, 1)})))
	if drains := actionsOf(pp.Actions, ActionDrain); len(drains) != 0 {
		t.Fatalf("drained a behind runner whose job is already running: %+v", drains)
	}
}

// A runner's row can read idle -- with the idle clock it had before -- while a
// job is already running on it, when the agent's idle report lands after the
// job started. Drained on that row it was stopped mid-job and killed with 137,
// and GitHub reported "the self-hosted runner lost communication". A job in
// progress keeps its runner, whatever the row says, on every path that retires
// runners that are not busy.
func TestAnIdleRowWhoseJobIsRunningIsNeverDrained(t *testing.T) {
	cases := map[string]func(p *store.Pool, r *store.Runner){
		"idle timeout":  func(p *store.Pool, r *store.Runner) {},
		"pool disabled": func(p *store.Pool, r *store.Runner) { p.Enabled = false },
		"image changed": func(p *store.Pool, r *store.Runner) { p.Image = "new:1"; r.Image = "old:1" },
		"maximum lifetime": func(p *store.Pool, r *store.Runner) {
			d := store.Duration(time.Minute)
			p.RunnerSettings.MaxRunnerLifetime = &d
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			p := testPool("linux-x64", "linux")
			r := idleRunner("r1", p, 30*time.Minute)
			set(p, r)
			running := &store.Job{
				ID: "job-1", PoolID: p.ID, RunnerID: r.ID, State: store.JobInProgress,
				Repo: "acme/widgets", InstallationID: testInstallation,
			}
			plan := Decide(snap([]*store.Pool{p}, []*store.Runner{r}, []*store.Job{running},
				[]*store.Host{testHost("host_a", 8, 1)}))
			for _, pp := range plan.Pools {
				for _, a := range pp.Actions {
					if a.RunnerID == r.ID && (a.Kind == ActionDrain || a.Kind == ActionRemove || a.Kind == ActionFail) {
						t.Fatalf("%s a runner whose job is running: %+v", a.Kind, a)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scale down
// ---------------------------------------------------------------------------

func TestScaleDownAfterIdleTimeout(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MinRunners = 1
	runners := []*store.Runner{
		idleRunner("r1", p, 10*time.Minute),
		idleRunner("r2", p, 20*time.Minute),
		idleRunner("r3", p, 30*time.Minute),
		idleRunner("r4", p, 6*time.Minute),
	}
	pp := only(t, Decide(snap([]*store.Pool{p}, runners, nil, []*store.Host{testHost("host_a", 8, 4)})))

	if want := "scaled linux-x64 4 -> 1: 3 runners idle > 5m"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
	// Longest-idle first, and never below the pool minimum.
	if got := runnerIDs(pp.Actions, ActionDrain); !slices.Equal(got, []string{"r3", "r2", "r1"}) {
		t.Fatalf("drained %v, want r3, r2, r1 (coldest first)", got)
	}
	if countOf(pp.Actions, ActionCreate) != 0 {
		t.Fatal("scale-down should not create anything")
	}
}

func TestScaleDownWaitsForTheIdleTimeout(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{idleRunner("r1", p, time.Minute), idleRunner("r2", p, 4*time.Minute)}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, nil, []*store.Host{testHost("host_a", 8, 2)})))
	if len(pp.Actions) != 0 || pp.Reason != "" {
		t.Fatalf("drained a runner before its idle timeout: %+v", pp)
	}
	if pp.Current != 2 || pp.Desired != 0 {
		t.Fatalf("got current=%d desired=%d, want 2/0", pp.Current, pp.Desired)
	}
}

func TestScaleDownNeverDrainsABusyRunner(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{
		testRunner("busy1", p, store.RunnerBusy, time.Hour),
		testRunner("busy2", p, store.RunnerBusy, time.Hour),
		idleRunner("idle1", p, 10*time.Minute),
		idleRunner("idle2", p, 11*time.Minute),
	}
	pp := only(t, Decide(snap([]*store.Pool{p}, runners, nil, []*store.Host{testHost("host_a", 8, 4)})))

	if got := runnerIDs(pp.Actions, ActionDrain); !slices.Equal(got, []string{"idle2", "idle1"}) {
		t.Fatalf("drained %v, want only the idle runners", got)
	}
	if pp.Desired != 2 {
		t.Fatalf("desired = %d, want 2: the busy runners are still needed", pp.Desired)
	}
}

func TestScaleDownStopsAtMinRunners(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MinRunners = 3
	var runners []*store.Runner
	for _, id := range []string{"r1", "r2", "r3", "r4"} {
		runners = append(runners, idleRunner(id, p, 30*time.Minute))
	}
	pp := only(t, Decide(snap([]*store.Pool{p}, runners, nil, []*store.Host{testHost("host_a", 8, 4)})))

	if n := countOf(pp.Actions, ActionDrain); n != 1 {
		t.Fatalf("drained %d runners, want 1: the pool minimum is 3", n)
	}
	if want := "scaled linux-x64 4 -> 3: 1 runner idle > 5m"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
}

func TestQueuedJobsPreventScaleDown(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{idleRunner("r1", p, time.Hour), idleRunner("r2", p, time.Hour)}
	jobs := []*store.Job{queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux")}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, jobs, []*store.Host{testHost("host_a", 8, 2)})))
	if len(pp.Actions) != 0 {
		t.Fatalf("drained runners the queue still needs: %+v", pp.Actions)
	}
}

// ---------------------------------------------------------------------------
// Reaping
// ---------------------------------------------------------------------------

func TestReap(t *testing.T) {
	p := testPool("linux-x64", "linux")
	tests := []struct {
		name       string
		runner     *store.Runner
		wantKind   ActionKind
		wantReason string
	}{
		{"provisioning past the timeout fails", testRunner("r1", p, store.RunnerProvisioning, 6*time.Minute),
			ActionFail, "stuck in provisioning for 6m, past the 5m provision timeout; check the host's agent log"},
		{"registering past the timeout fails", testRunner("r1", p, store.RunnerRegistering, 10*time.Minute),
			ActionFail, "stuck in registering for 10m, past the 5m provision timeout; check the host's agent log"},
		{"provisioning inside the timeout is left alone",
			testRunner("r1", p, store.RunnerProvisioning, 4*time.Minute), "", ""},
		{"a failed runner is removed once its failure has been readable for a while",
			testRunner("r1", p, store.RunnerFailed, 11*time.Minute),
			ActionRemove, "runner failed 11m ago; its failure has been on the Runners page long enough"},
		{"a recently failed runner is left on the page", testRunner("r1", p, store.RunnerFailed, time.Minute), "", ""},
		{"an old idle runner is retired", idleRunner("r1", p, 7*time.Hour),
			ActionDrain, "runner reached the 6h maximum lifetime"},
		{"an old busy runner keeps its job", testRunner("r1", p, store.RunnerBusy, 7*time.Hour), "", ""},
		// Old enough for the maximum lifetime, which must not drain a runner
		// that is already draining, but not yet past the drain timeout.
		{"an old draining runner is left to drain", drainingRunner("r1", p, time.Minute), "", ""},
		{"a drain with nothing left to wait for is failed once it is overdue",
			drainingRunner("r1", p, 16*time.Minute), ActionFail,
			"draining with no job for 16m, past the 15m drain timeout; its stop was never carried out, so the slot is being taken back"},
		{"a young idle runner is left alone", idleRunner("r1", p, time.Minute), "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var jobs []*store.Job
			if tc.name == "provisioning inside the timeout is left alone" {
				// It is still needed capacity. With no queued demand the new
				// cancellation path correctly stops it before the timeout.
				jobs = []*store.Job{queued("j1", time.Minute, "linux")}
			}
			s := snap([]*store.Pool{p}, []*store.Runner{tc.runner}, jobs, []*store.Host{testHost("host_a", 8, 1)})
			pp := only(t, Decide(s))
			if tc.wantKind == "" {
				if len(pp.Actions) != 0 {
					t.Fatalf("expected no action, got %+v", pp.Actions)
				}
				return
			}
			if len(pp.Actions) != 1 {
				t.Fatalf("expected 1 action, got %+v", pp.Actions)
			}
			a := pp.Actions[0]
			if a.Kind != tc.wantKind || a.RunnerID != "r1" || a.Reason != tc.wantReason {
				t.Fatalf("action = %+v, want kind %s reason %q", a, tc.wantKind, tc.wantReason)
			}
		})
	}
}

func TestReapIsDisabledByAZeroPolicy(t *testing.T) {
	p := testPool("linux-x64", "linux")
	runners := []*store.Runner{
		testRunner("r1", p, store.RunnerProvisioning, 48*time.Hour),
		idleRunner("r2", p, 48*time.Hour),
	}
	s := snap([]*store.Pool{p}, runners, nil, []*store.Host{testHost("host_a", 8, 2)})
	s.Policy.ProvisionTimeout = 0
	s.Policy.MaxRunnerLifetime = 0
	p.IdleTimeout = store.Duration(0)
	p.MinRunners = 2

	pp := only(t, Decide(s))
	if len(pp.Actions) != 0 {
		t.Fatalf("an unset timeout reaped runners: %+v", pp.Actions)
	}
}

func TestRetiredRunnerIsReplaced(t *testing.T) {
	// A runner past its maximum lifetime no longer counts towards the pool, so
	// the same tick both retires it and starts its replacement.
	p := testPool("linux-x64", "linux")
	p.MinRunners = 1
	s := snap([]*store.Pool{p}, []*store.Runner{idleRunner("old", p, 7*time.Hour)}, nil,
		[]*store.Host{testHost("host_a", 8, 1)})

	pp := only(t, Decide(s))
	if got := runnerIDs(pp.Actions, ActionDrain); !slices.Equal(got, []string{"old"}) {
		t.Fatalf("drained %v, want [old]", got)
	}
	if n := countOf(pp.Actions, ActionCreate); n != 1 {
		t.Fatalf("got %d creates, want 1", n)
	}
	if pp.Actions[0].Kind != ActionDrain {
		t.Fatalf("the drain must be planned before the create, got %v", pp.Actions[0].Kind)
	}
}

func TestFailedRunnerDoesNotHoldThePoolShort(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MinRunners = 1
	s := snap([]*store.Pool{p}, []*store.Runner{testRunner("dead", p, store.RunnerFailed, 11*time.Minute)},
		nil, []*store.Host{testHost("host_a", 8, 1)})

	pp := only(t, Decide(s))
	if countOf(pp.Actions, ActionRemove) != 1 || countOf(pp.Actions, ActionCreate) != 1 {
		t.Fatalf("want one remove and one create, got %+v", pp.Actions)
	}
	if pp.Actions[0].Kind != ActionRemove {
		t.Fatal("cleanup must come before the create that reuses the capacity")
	}
}

// startFailed is a runner that died before it ever registered, failedFor ago:
// the shape a bad image or an unreachable GitHub produces.
func startFailed(id string, p *store.Pool, failedFor time.Duration, message string) *store.Runner {
	r := testRunner(id, p, store.RunnerFailed, failedFor+2*time.Second)
	at := ago(failedFor)
	r.FinishedAt = &at
	r.Message = message
	return r
}

// A runner that died on creation used to be replaced in the same pass that
// noticed, so a pool with a bad image created, failed and removed a runner
// every second and spent two GitHub API calls each time. The pool now waits,
// and the wait doubles with each failure still on the page.
func TestAPoolWhoseRunnersDieOnStartBacksOff(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.MinRunners = 1
	hosts := []*store.Host{testHost("host_a", 8, 0)}

	t.Run("one fresh failure holds the pool for the base wait", func(t *testing.T) {
		s := snap([]*store.Pool{p}, []*store.Runner{startFailed("r1", p, time.Second, "No such image: sha256:abc")}, nil, hosts)
		pp := only(t, Decide(s))
		if countOf(pp.Actions, ActionCreate) != 0 {
			t.Fatalf("a create was planned a second after the last one failed: %+v", pp.Actions)
		}
		want := "the last runner failed to start, most recently 1s ago (No such image: sha256:abc); trying again in 9s"
		if pp.Failing != want {
			t.Fatalf("Failing = %q, want %q", pp.Failing, want)
		}
		if !strings.Contains(pp.Reason, want) || !strings.HasPrefix(pp.Reason, "cannot scale linux-x64 0 -> 1") {
			t.Fatalf("the plan's reason does not say what went unserved and why: %q", pp.Reason)
		}
		if pp.Blocked != "" {
			t.Fatalf("a held pool was reported as blocked: %q -- there is a host with room, the pool is merely waiting", pp.Blocked)
		}
	})

	t.Run("the wait doubles with each failure", func(t *testing.T) {
		runners := []*store.Runner{
			startFailed("r1", p, 30*time.Second, "third"),
			startFailed("r2", p, 50*time.Second, "second"),
			startFailed("r3", p, 90*time.Second, "first"),
		}
		s := snap([]*store.Pool{p}, runners, nil, hosts)
		pp := only(t, Decide(s))
		// Three failures: 10s, 20s, 40s. The newest was 30s ago, so 10s to go.
		want := "the last 3 runners failed to start, most recently 30s ago (third); trying again in 10s"
		if pp.Failing != want {
			t.Fatalf("Failing = %q, want %q", pp.Failing, want)
		}
		if countOf(pp.Actions, ActionCreate) != 0 {
			t.Fatalf("created inside the wait: %+v", pp.Actions)
		}
	})

	t.Run("once the wait is out the pool tries again", func(t *testing.T) {
		runners := []*store.Runner{
			startFailed("r1", p, 41*time.Second, "third"),
			startFailed("r2", p, 60*time.Second, "second"),
			startFailed("r3", p, 90*time.Second, "first"),
		}
		s := snap([]*store.Pool{p}, runners, nil, hosts)
		pp := only(t, Decide(s))
		if pp.Failing != "" {
			t.Fatalf("still held after the 40s wait: %q", pp.Failing)
		}
		if countOf(pp.Actions, ActionCreate) != 1 {
			t.Fatalf("want one create after the wait, got %+v", pp.Actions)
		}
	})

	t.Run("the wait is capped", func(t *testing.T) {
		var runners []*store.Runner
		for i := range 12 {
			runners = append(runners, startFailed(fmt.Sprintf("r%d", i), p, time.Duration(i+1)*20*time.Second, "boom"))
		}
		s := snap([]*store.Pool{p}, runners, nil, hosts)
		pp := only(t, Decide(s))
		if !strings.HasSuffix(pp.Failing, "trying again in 4m40s") {
			t.Fatalf("twelve failures should wait the 5m cap, 20s in: %q", pp.Failing)
		}
	})

	t.Run("a runner that ran and then failed is not a start failure", func(t *testing.T) {
		r := startFailed("r1", p, time.Second, "exit 137 under a job")
		registered := ago(time.Hour)
		r.RegisteredAt = &registered
		s := snap([]*store.Pool{p}, []*store.Runner{r}, nil, hosts)
		pp := only(t, Decide(s))
		if pp.Failing != "" || countOf(pp.Actions, ActionCreate) != 1 {
			t.Fatalf("a runner lost under a job held the pool back: failing=%q actions=%+v", pp.Failing, pp.Actions)
		}
	})

	t.Run("a failure that has left the page no longer counts", func(t *testing.T) {
		s := snap([]*store.Pool{p}, []*store.Runner{startFailed("r1", p, 11*time.Minute, "old news")}, nil, hosts)
		pp := only(t, Decide(s))
		if pp.Failing != "" || countOf(pp.Actions, ActionCreate) != 1 || countOf(pp.Actions, ActionRemove) != 1 {
			t.Fatalf("an old failure should be removed and ignored: failing=%q actions=%+v", pp.Failing, pp.Actions)
		}
	})

	t.Run("a long message is cut to a hint", func(t *testing.T) {
		long := strings.Repeat("the docker backend could not create the runner ", 8)
		s := snap([]*store.Pool{p}, []*store.Runner{startFailed("r1", p, time.Second, long)}, nil, hosts)
		pp := only(t, Decide(s))
		if len(pp.Failing) > 260 || !strings.Contains(pp.Failing, "…") {
			t.Fatalf("the hold sentence carries the whole message: %d chars", len(pp.Failing))
		}
	})
}

// ---------------------------------------------------------------------------
// Host selection
// ---------------------------------------------------------------------------

func TestHostSelection(t *testing.T) {
	selectorPool := func() *store.Pool {
		p := testPool("linux-x64", "linux")
		p.HostSelector = store.StringMap{"zone": "eu", "disk": "ssd"}
		return p
	}
	podmanPool := func() *store.Pool {
		p := testPool("linux-x64", "linux")
		p.Backend = store.BackendPodman
		return p
	}
	unhealthy := func() *store.Host {
		h := testHost("host_sick", 8, 0)
		h.LastHeartbeat = ago(5 * time.Minute)
		return h
	}
	cordoned := func() *store.Host {
		h := testHost("host_cordoned", 8, 0)
		h.Cordoned = true
		return h
	}
	labelled := func(id string, kv map[string]string) *store.Host {
		h := testHost(id, 8, 0)
		h.Labels = kv
		return h
	}

	tests := []struct {
		name     string
		pool     *store.Pool
		hosts    []*store.Host
		wantHost string // "" means "no create at all"
		wantWhy  string // substring of the pool plan reason when nothing is created
	}{
		{name: "cordoned host is skipped", pool: testPool("linux-x64", "linux"),
			hosts: []*store.Host{cordoned()}, wantWhy: "(1 cordoned)"},
		{name: "unhealthy host is skipped", pool: testPool("linux-x64", "linux"),
			hosts: []*store.Host{unhealthy()}, wantWhy: "(1 unhealthy)"},
		{name: "full host is skipped", pool: testPool("linux-x64", "linux"),
			hosts: []*store.Host{testHost("host_full", 2, 2)}, wantWhy: "(1 at capacity)"},
		{name: "backend mismatch is skipped", pool: podmanPool(),
			hosts:   []*store.Host{testHost("host_a", 8, 0)},
			wantWhy: "no host can take a new podman runner (1 without the podman backend)"},
		{name: "host selector mismatch is skipped", pool: selectorPool(),
			hosts:   []*store.Host{labelled("host_a", map[string]string{"zone": "us", "disk": "ssd"})},
			wantWhy: "(1 not matching the pool's host selector)"},
		{name: "host selector must match every key", pool: selectorPool(),
			hosts:   []*store.Host{labelled("host_a", map[string]string{"zone": "eu"})},
			wantWhy: "(1 not matching the pool's host selector)"},
		{name: "matching selector is used", pool: selectorPool(),
			hosts:    []*store.Host{labelled("host_a", map[string]string{"zone": "eu", "disk": "ssd", "extra": "ok"})},
			wantHost: "host_a"},
		{name: "no hosts at all", pool: testPool("linux-x64", "linux"),
			hosts: nil, wantWhy: "no agent hosts are registered"},
		{name: "the emptiest host wins", pool: testPool("linux-x64", "linux"),
			hosts: []*store.Host{testHost("host_a", 8, 7), testHost("host_b", 8, 1),
				testHost("host_c", 8, 4)},
			wantHost: "host_b"},
		{name: "ties break on host id", pool: testPool("linux-x64", "linux"),
			hosts:    []*store.Host{testHost("host_z", 4, 0), testHost("host_a", 4, 0)},
			wantHost: "host_a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := snap([]*store.Pool{tc.pool}, nil,
				[]*store.Job{queued("j1", time.Minute, "linux")}, tc.hosts)
			pp := only(t, Decide(s))
			creates := actionsOf(pp.Actions, ActionCreate)
			if tc.wantHost == "" {
				if len(creates) != 0 {
					t.Fatalf("expected no create, got %+v", creates)
				}
				if !strings.HasPrefix(pp.Reason, "cannot scale linux-x64 0 -> 1: ") {
					t.Fatalf("reason = %q, want a cannot-scale sentence", pp.Reason)
				}
				if !strings.Contains(pp.Reason, tc.wantWhy) {
					t.Fatalf("reason = %q, want it to mention %q", pp.Reason, tc.wantWhy)
				}
				// A pool that wanted a runner and got nowhere to put it is
				// reported as blocked, which is what the problems drawer shows:
				// nothing else in the product says this happened.
				if pp.Blocked == "" || pp.BlockedFix == "" {
					t.Fatalf("plan = %+v, want it marked blocked with a fix", pp)
				}
				return
			}
			if len(creates) != 1 || creates[0].HostID != tc.wantHost {
				t.Fatalf("creates = %+v, want one on %s", creates, tc.wantHost)
			}
		})
	}
}

func TestCreatesSpreadOverHostsAndRespectCapacity(t *testing.T) {
	p := testPool("linux-x64", "linux")
	var jobs []*store.Job
	for _, id := range []string{"j1", "j2", "j3", "j4", "j5"} {
		jobs = append(jobs, queued(id, time.Minute, "linux"))
	}
	hosts := []*store.Host{testHost("host_a", 2, 0), testHost("host_b", 3, 2)}

	pp := only(t, Decide(snap([]*store.Pool{p}, nil, jobs, hosts)))
	got := hostIDs(pp.Actions)
	if want := []string{"host_a", "host_a", "host_b"}; !slices.Equal(got, want) {
		t.Fatalf("placed on %v, want %v", got, want)
	}
	if want := "cannot scale linux-x64 3 -> 5: no host can take a new docker runner (2 at capacity); wait for a job to finish, raise a host's capacity, or add a host"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q: the sentence reports what was actually started", pp.Reason, want)
	}
}

func TestCreatesSpreadOverEquallyFreeHosts(t *testing.T) {
	p := testPool("linux-x64", "linux")
	var jobs []*store.Job
	for _, id := range []string{"j1", "j2", "j3", "j4"} {
		jobs = append(jobs, queued(id, time.Minute, "linux"))
	}
	hosts := []*store.Host{testHost("host_b", 4, 0), testHost("host_a", 4, 0)}

	got := hostIDs(only(t, Decide(snap([]*store.Pool{p}, nil, jobs, hosts))).Actions)
	if want := []string{"host_a", "host_b", "host_a", "host_b"}; !slices.Equal(got, want) {
		t.Fatalf("placed on %v, want %v", got, want)
	}
}

func TestHostCapacityIsSharedBetweenPools(t *testing.T) {
	first, second := testPool("aaa", "aaa"), testPool("zzz", "zzz")
	jobs := []*store.Job{queued("j1", time.Minute, "aaa"), queued("j2", time.Minute, "zzz")}
	plan := Decide(snap([]*store.Pool{first, second}, nil, jobs, []*store.Host{testHost("host_a", 1, 0)}))

	if len(plan.Actions) != 1 || plan.Actions[0].PoolName != "aaa" {
		t.Fatalf("the single free slot went to %+v", plan.Actions)
	}
	if !strings.Contains(plan.Pools[1].Reason, "at capacity") {
		t.Fatalf("second pool reason = %q, want it to blame capacity", plan.Pools[1].Reason)
	}
}

// ---------------------------------------------------------------------------
// Pools that claim nothing, and jobs nothing claims
// ---------------------------------------------------------------------------

func TestDisabledPoolDrainsToZero(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.Enabled = false
	p.MinRunners = 2
	runners := []*store.Runner{
		idleRunner("idle1", p, time.Second),
		testRunner("busy1", p, store.RunnerBusy, time.Minute),
		testRunner("prov1", p, store.RunnerProvisioning, time.Minute),
	}
	s := snap([]*store.Pool{p}, runners,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{testHost("host_a", 8, 3)})

	plan := Decide(s)
	pp := only(t, plan)
	if pp.Desired != 0 {
		t.Fatalf("desired = %d, want 0", pp.Desired)
	}
	if countOf(pp.Actions, ActionCreate) != 0 {
		t.Fatal("a disabled pool must not create runners")
	}
	// Oldest first, and the busy runner keeps its job.
	if got := runnerIDs(pp.Actions, ActionDrain); !slices.Equal(got, []string{"idle1", "prov1"}) {
		t.Fatalf("drained %v, want the non-busy runners", got)
	}
	for _, a := range actionsOf(pp.Actions, ActionDrain) {
		if a.Reason != "pool is disabled" {
			t.Fatalf("drain reason = %q", a.Reason)
		}
	}
	if want := "scaled linux-x64 3 -> 1: pool is disabled"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
	if len(plan.Unmatched) != 1 || plan.Unmatched[0].Job.ID != "j1" {
		t.Fatalf("a job whose only pool is disabled must be reported unmatched, got %v", plan.Unmatched)
	}
}

func TestDisabledPoolIsStillReaped(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.Enabled = false
	runners := []*store.Runner{
		testRunner("dead", p, store.RunnerFailed, 11*time.Minute),
		testRunner("stuck", p, store.RunnerProvisioning, 10*time.Minute),
	}
	pp := only(t, Decide(snap([]*store.Pool{p}, runners, nil, []*store.Host{testHost("host_a", 8, 2)})))

	if countOf(pp.Actions, ActionRemove) != 1 || countOf(pp.Actions, ActionFail) != 1 {
		t.Fatalf("actions = %+v, want a remove and a fail", pp.Actions)
	}
	if countOf(pp.Actions, ActionDrain) != 0 {
		t.Fatal("a reaped runner must not also be drained")
	}
}

func TestUnmatchedJobsAreReported(t *testing.T) {
	p := testPool("linux-x64", "linux", "x64")
	jobs := []*store.Job{
		queued("gpu", time.Minute, "self-hosted", "gpu"),
		queued("windows", time.Minute, "self-hosted", "windows"),
		queued("ok", time.Minute, "self-hosted", "linux", "x64"),
	}
	plan := Decide(snap([]*store.Pool{p}, nil, jobs, []*store.Host{testHost("host_a", 8, 0)}))

	var got []string
	for _, u := range plan.Unmatched {
		got = append(got, u.Job.ID)
	}
	if !slices.Equal(got, []string{"gpu", "windows"}) {
		t.Fatalf("unmatched = %v, want [gpu windows]", got)
	}
	if only(t, plan).QueuedMatched != 1 {
		t.Fatalf("the matched job was not counted")
	}
}

func TestInProgressJobsAreNotDemand(t *testing.T) {
	p := testPool("linux-x64", "linux")
	running := queued("j1", time.Hour, "linux")
	running.State = store.JobInProgress
	s := snap([]*store.Pool{p}, []*store.Runner{testRunner("r1", p, store.RunnerBusy, time.Hour)},
		[]*store.Job{running}, []*store.Host{testHost("host_a", 8, 1)})

	pp := only(t, Decide(s))
	if len(pp.Actions) != 0 || pp.Desired != 1 {
		t.Fatalf("an in-progress job created work: desired=%d actions=%+v", pp.Desired, pp.Actions)
	}
	if len(Decide(s).Unmatched) != 0 {
		t.Fatal("an in-progress job must never be reported unmatched")
	}
}

func TestNothingToDo(t *testing.T) {
	p := testPool("linux-x64", "linux")
	plan := Decide(snap([]*store.Pool{p}, nil, nil, []*store.Host{testHost("host_a", 8, 0)}))
	pp := only(t, plan)
	if pp.Reason != "" || len(pp.Actions) != 0 || len(plan.Actions) != 0 {
		t.Fatalf("an idle fleet produced %+v", pp)
	}
}

func TestEmptySnapshot(t *testing.T) {
	plan := Decide(Snapshot{Now: now, Policy: testPolicy()})
	if len(plan.Pools) != 0 || len(plan.Actions) != 0 || len(plan.Unmatched) != 0 {
		t.Fatalf("empty snapshot produced %+v", plan)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// busyFleet is a snapshot with every rule in play at once: it is what the
// determinism test shuffles.
func busyFleet() Snapshot {
	gpu := testPool("gpu", "linux", "gpu")
	gpu.MinRunners, gpu.MaxRunners = 1, 4
	general := testPool("general", "linux", "x64")
	general.MaxRunners = 6
	disabled := testPool("retired", "retired")
	disabled.Enabled = false

	runners := []*store.Runner{
		idleRunner("r_idle_old", general, 30*time.Minute),
		idleRunner("r_idle_new", general, time.Minute),
		testRunner("r_busy", general, store.RunnerBusy, time.Hour),
		testRunner("r_stuck", gpu, store.RunnerProvisioning, 20*time.Minute),
		testRunner("r_dead", gpu, store.RunnerFailed, 11*time.Minute),
		idleRunner("r_retired", disabled, time.Hour),
	}
	jobs := []*store.Job{
		queued("j_gpu1", 2*time.Minute, "self-hosted", "linux", "gpu"),
		queued("j_gpu2", 2*time.Minute, "self-hosted", "gpu"),
		queued("j_general", 2*time.Minute, "self-hosted", "linux", "x64"),
		queued("j_lost", 2*time.Minute, "self-hosted", "macos"),
	}
	hosts := []*store.Host{testHost("host_a", 4, 1), testHost("host_b", 4, 3), testHost("host_c", 2, 2)}
	return snap([]*store.Pool{general, gpu, disabled}, runners, jobs, hosts)
}

func TestDecideIsDeterministic(t *testing.T) {
	first, err := json.Marshal(Decide(busyFleet()))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		s := busyFleet()
		// Reversing every input slice must not change a single byte of output.
		if i%2 == 0 {
			slices.Reverse(s.Pools)
			slices.Reverse(s.Jobs)
			slices.Reverse(s.Hosts)
			for k := range s.Runners {
				slices.Reverse(s.Runners[k])
			}
		}
		got, err := json.Marshal(Decide(s))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(first) {
			t.Fatalf("plan %d differs:\n got %s\nwant %s", i, got, first)
		}
	}
}

func TestDecideDoesNotMutateTheSnapshot(t *testing.T) {
	s := busyFleet()
	pools := slices.Clone(s.Pools)
	jobs := slices.Clone(s.Jobs)
	hosts := slices.Clone(s.Hosts)

	Decide(s)

	if !slices.Equal(pools, s.Pools) || !slices.Equal(jobs, s.Jobs) || !slices.Equal(hosts, s.Hosts) {
		t.Fatal("Decide reordered the caller's slices")
	}
}

func TestBusyFleetPlan(t *testing.T) {
	plan := Decide(busyFleet())
	byPool := map[string]PoolPlan{}
	for _, pp := range plan.Pools {
		byPool[pp.PoolName] = pp
	}
	if got := []string{plan.Pools[0].PoolName, plan.Pools[1].PoolName, plan.Pools[2].PoolName}; !slices.Equal(
		got, []string{"general", "gpu", "retired"}) {
		t.Fatalf("pool plans are not in name order: %v", got)
	}

	// general: one busy runner plus one queued job, two idle runners, so the
	// pool is already big enough and the cold idle runner goes.
	general := byPool["general"]
	if want := "scaled general 3 -> 2: 1 runner idle > 5m"; general.Reason != want {
		t.Fatalf("general reason = %q, want %q", general.Reason, want)
	}
	if got := runnerIDs(general.Actions, ActionDrain); !slices.Equal(got, []string{"r_idle_old"}) {
		t.Fatalf("general drained %v", got)
	}

	// gpu: the stuck runner fails, the dead one is removed, and two queued jobs
	// need two new runners.
	gpu := byPool["gpu"]
	if countOf(gpu.Actions, ActionFail) != 1 || countOf(gpu.Actions, ActionRemove) != 1 {
		t.Fatalf("gpu actions = %+v", gpu.Actions)
	}
	if want := "scaled gpu 0 -> 2: 2 jobs queued"; gpu.Reason != want {
		t.Fatalf("gpu reason = %q, want %q", gpu.Reason, want)
	}
	// host_a has three slots free, host_b one and host_c none, so both runners
	// land on host_a: after the first, it is still the emptiest host.
	if got := hostIDs(gpu.Actions); !slices.Equal(got, []string{"host_a", "host_a"}) {
		t.Fatalf("gpu placed on %v, want host_a twice", got)
	}

	// Specificity decides where a job lands: the plain linux/x64 job goes to
	// general even though the gpu pool also matches it.
	if byPool["general"].QueuedMatched != 1 || byPool["gpu"].QueuedMatched != 2 {
		t.Fatalf("demand split general=%d gpu=%d, want 1/2",
			byPool["general"].QueuedMatched, byPool["gpu"].QueuedMatched)
	}

	if want := "scaled retired 1 -> 0: pool is disabled"; byPool["retired"].Reason != want {
		t.Fatalf("retired reason = %q, want %q", byPool["retired"].Reason, want)
	}
	if len(plan.Unmatched) != 1 || plan.Unmatched[0].Job.ID != "j_lost" {
		t.Fatalf("unmatched = %v, want [j_lost]", plan.Unmatched)
	}
}

// ---------------------------------------------------------------------------
// Sentence helpers
// ---------------------------------------------------------------------------

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		30 * time.Second:           "30s",
		5 * time.Minute:            "5m",
		6 * time.Hour:              "6h",
		90 * time.Second:           "1m30s",
		time.Hour + 30*time.Minute: "1h30m",
		10 * time.Second:           "10s",
		0:                          "0s",
		2*time.Hour + 5*time.Minute + 3*time.Second: "2h5m3s",
	}
	for d, want := range tests {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestPlural(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "0 jobs"}, {1, "1 job"}, {2, "2 jobs"}} {
		if got := plural(tc.n, "job"); got != tc.want {
			t.Errorf("plural(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Edge cases an operator can still configure their way into
// ---------------------------------------------------------------------------

func TestUnsetCreateBudgetDoesNotStallTheFleet(t *testing.T) {
	p := testPool("linux-x64", "linux")
	var jobs []*store.Job
	for _, id := range []string{"j1", "j2", "j3", "j4", "j5", "j6"} {
		jobs = append(jobs, queued(id, time.Minute, "linux"))
	}
	s := snap([]*store.Pool{p}, nil, jobs, []*store.Host{testHost("host_a", 8, 0)})
	s.Policy.MaxCreatesPerTick = 0

	if n := countOf(only(t, Decide(s)).Actions, ActionCreate); n != 6 {
		t.Fatalf("got %d creates, want 6: an unset cap means no cap", n)
	}
}

func TestMaxRunnersWinsOverMinRunners(t *testing.T) {
	// The schema forbids max < min, so this only happens if something wrote
	// around it. The hard cap must still hold: exceeding it is what costs money.
	p := testPool("linux-x64", "linux")
	p.MinRunners, p.MaxRunners = 3, 1
	pp := only(t, Decide(snap([]*store.Pool{p}, nil, nil, []*store.Host{testHost("host_a", 8, 0)})))

	if pp.Desired != 1 || countOf(pp.Actions, ActionCreate) != 1 {
		t.Fatalf("desired=%d creates=%d, want 1/1", pp.Desired, countOf(pp.Actions, ActionCreate))
	}
}

func TestIdleRunnerWithoutAnIdleTimestamp(t *testing.T) {
	p := testPool("linux-x64", "linux")
	r := testRunner("r1", p, store.RunnerIdle, time.Hour) // no LastIdleAt

	pp := only(t, Decide(snap([]*store.Pool{p}, []*store.Runner{r}, nil,
		[]*store.Host{testHost("host_a", 8, 1)})))
	if len(pp.Actions) != 0 {
		t.Fatalf("a runner with no idle timestamp was drained on a 5m timeout: %+v", pp.Actions)
	}

	// With no idle timeout at all, the same runner is surplus immediately, and
	// it sorts by creation time against a runner that does have a timestamp.
	p.IdleTimeout = store.Duration(0)
	warm := idleRunner("r2", p, time.Second)
	pp = only(t, Decide(snap([]*store.Pool{p}, []*store.Runner{warm, r}, nil,
		[]*store.Host{testHost("host_a", 8, 2)})))
	if got := runnerIDs(pp.Actions, ActionDrain); !slices.Equal(got, []string{"r1", "r2"}) {
		t.Fatalf("drained %v, want [r1 r2]", got)
	}
	if want := "scaled linux-x64 2 -> 0: 2 runners idle"; pp.Reason != want {
		t.Fatalf("reason = %q, want %q", pp.Reason, want)
	}
	if got := pp.Actions[0].Reason; got != "surplus idle runner" {
		t.Fatalf("drain reason = %q", got)
	}
}

func TestTiesBreakOnIDNotInputOrder(t *testing.T) {
	// Two pools sharing a name (possible mid-rename) and two jobs queued in the
	// same millisecond: neither may depend on the order the caller collected.
	a := testPool("same", "a")
	a.ID = "pool_a"
	b := testPool("same", "b")
	b.ID = "pool_b"
	// j1 and j2 arrived in the same millisecond; j0 arrived before both.
	jobs := []*store.Job{
		queued("j2", time.Minute, "nope"),
		queued("j0", 2*time.Minute, "nope"),
		queued("j1", time.Minute, "nope"),
	}

	for _, pools := range [][]*store.Pool{{a, b}, {b, a}} {
		plan := Decide(snap(pools, nil, jobs, []*store.Host{testHost("host_a", 8, 0)}))
		if plan.Pools[0].PoolID != "pool_a" {
			t.Fatalf("pool order = %s first, want pool_a", plan.Pools[0].PoolID)
		}
		var order []string
		for _, u := range plan.Unmatched {
			order = append(order, u.Job.ID)
		}
		if !slices.Equal(order, []string{"j0", "j1", "j2"}) {
			t.Fatalf("unmatched order = %v, want oldest first then by ID", order)
		}
	}
}

// "1 without the docker backend" sends an operator looking at the pool, when
// what is wrong is on the host and the agent already said so. The probe's own
// sentence is the fix, so it travels with the reason.
func TestBlockedReasonCarriesTheHostsExplanation(t *testing.T) {
	host := testHost("host_a", 8, 0)
	host.Backends = store.StringSlice{"process"}
	host.BackendInfo = store.HostBackends{{
		Kind:   store.BackendDocker,
		Detail: "/var/run/docker.sock is not readable by this agent",
	}}

	s := snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{host})
	pp := only(t, Decide(s))

	if len(actionsOf(pp.Actions, ActionCreate)) != 0 {
		t.Fatalf("created a runner on a host with no docker: %+v", pp.Actions)
	}
	if !strings.Contains(pp.Blocked, "is not readable by this agent") {
		t.Fatalf("blocked = %q, want the agent's own explanation", pp.Blocked)
	}
	if !strings.Contains(pp.Blocked, host.Name) {
		t.Fatalf("blocked = %q, want it to name the host", pp.Blocked)
	}
}

// A scale-up that only ran out of this tick's create budget is not blocked:
// the next pass makes the runner, and an operator has nothing to do about it.
func TestBudgetShortfallIsNotReportedAsBlocked(t *testing.T) {
	p := testPool("linux-x64", "linux")
	s := snap([]*store.Pool{p}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux"), queued("j2", time.Minute, "linux")},
		[]*store.Host{testHost("host_a", 8, 0)})
	s.Policy.MaxCreatesPerTick = 1

	pp := only(t, Decide(s))
	if len(actionsOf(pp.Actions, ActionCreate)) != 1 {
		t.Fatalf("creates = %+v, want the one the budget allows", pp.Actions)
	}
	if pp.Blocked != "" {
		t.Fatalf("blocked = %q, want nothing for a shortfall the next tick clears", pp.Blocked)
	}
}

// "point this pool at a backend they already offer" is not something an
// operator can act on until Zoomies says which backend that is.
func TestBlockedOnBackendNamesWhatTheHostsDoOffer(t *testing.T) {
	host := testHost("host_a", 8, 0)
	host.Backends = store.StringSlice{"podman"}
	host.BackendInfo = store.HostBackends{{
		Kind:   store.BackendDocker,
		Detail: "permission denied on /var/run/docker.sock",
	}}

	pp := only(t, Decide(snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{host})))

	if !slices.Equal(pp.BlockedAlternatives, []string{"podman"}) {
		t.Fatalf("alternatives = %v, want the backend the host offers", pp.BlockedAlternatives)
	}
	if !strings.Contains(pp.BlockedFix, "point this pool at podman, which 1 host already offers") {
		t.Fatalf("fix = %q, want it to name podman and the count", pp.BlockedFix)
	}
}

func TestBlockedOnBackendListsEveryAlternativeInOrder(t *testing.T) {
	a := testHost("host_a", 8, 0)
	a.Backends = store.StringSlice{"podman", "process"}
	b := testHost("host_b", 8, 0)
	b.Backends = store.StringSlice{"process"}

	pp := only(t, Decide(snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{a, b})))

	if !slices.Equal(pp.BlockedAlternatives, []string{"podman", "process"}) {
		t.Fatalf("alternatives = %v, want podman before process", pp.BlockedAlternatives)
	}
	if !strings.Contains(pp.BlockedFix, "podman (1 host), process (2 hosts)") {
		t.Fatalf("fix = %q, want each alternative with the hosts that offer it", pp.BlockedFix)
	}
}

// A fleet with nothing else to offer must not be told to switch to something.
func TestBlockedOnBackendSaysSoWhenThereIsNoAlternative(t *testing.T) {
	host := testHost("host_a", 8, 0)
	host.Backends = nil

	pp := only(t, Decide(snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{host})))

	if len(pp.BlockedAlternatives) != 0 {
		t.Fatalf("alternatives = %v, want none", pp.BlockedAlternatives)
	}
	if strings.Contains(pp.BlockedFix, "point this pool at") {
		t.Fatalf("fix = %q, want no offer of a backend nothing has", pp.BlockedFix)
	}
	if !strings.Contains(pp.BlockedFix, "no other backend to switch this pool to") {
		t.Fatalf("fix = %q, want it to say the daemon is the only way out", pp.BlockedFix)
	}
}

// A host that only has room for nothing is not an alternative: switching the
// pool's backend would leave it exactly as stuck.
func TestBlockedAlternativesIgnoreHostsWithNoRoom(t *testing.T) {
	full := testHost("host_full", 1, 1)
	full.Backends = store.StringSlice{"podman"}
	mismatched := testHost("host_a", 8, 0)
	mismatched.Backends = store.StringSlice{"process"}

	pp := only(t, Decide(snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{full, mismatched})))

	if slices.Contains(pp.BlockedAlternatives, "podman") {
		t.Fatalf("alternatives = %v, want nothing from a host with no capacity", pp.BlockedAlternatives)
	}
}

// A pool the fleet is merely too busy for is not solved by a different backend,
// so it is never offered one.
func TestAtCapacityCarriesNoAlternatives(t *testing.T) {
	host := testHost("host_a", 1, 1)
	host.Backends = store.StringSlice{"docker", "podman"}

	pp := only(t, Decide(snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{host})))

	if !pp.BlockedAtCapacity {
		t.Fatalf("plan = %+v, want a full fleet reported as full", pp)
	}
	if len(pp.BlockedAlternatives) != 0 {
		t.Fatalf("alternatives = %v, want none for a fleet that is only busy", pp.BlockedAlternatives)
	}
}

// With a repository limit of one and three jobs from the same repository the
// reason used to read "3 jobs queued" on a scale-up to a single runner, which
// an operator reads as a shortfall. It counts the jobs the pool is scaling for
// and says where the rest went.
func TestTheScaleUpReasonCountsTheJobsItIsScalingForAndNamesTheDeferred(t *testing.T) {
	p := testPool("shared", "self-hosted")
	p.RepositoryScaleUpLimit = 1
	jobs := []*store.Job{
		queued("one", time.Minute, "self-hosted"),
		queued("two", time.Minute, "self-hosted"),
		queued("three", time.Minute, "self-hosted"),
	}
	s := snap([]*store.Pool{p}, nil, jobs, []*store.Host{testHost("host_a", 4, 0)})

	pp := only(t, Decide(s))
	if pp.Desired != 1 || countOf(pp.Actions, ActionCreate) != 1 {
		t.Fatalf("plan = %+v, want one runner for the one admitted job", pp)
	}
	want := "scaled shared 0 -> 1: 1 job queued (2 jobs deferred by the repository limit for acme/widgets)"
	if pp.Reason != want {
		t.Fatalf("reason = %q\nwant     %q", pp.Reason, want)
	}
	if pp.Actions[0].Reason != "1 job queued (2 jobs deferred by the repository limit for acme/widgets)" {
		t.Fatalf("action reason = %q", pp.Actions[0].Reason)
	}
}

// HostCanRun is the placement rule, and the one the wizard, the capacity
// signal and prewarming ask, so a host the scheduler would place on and a
// host they count are the same host.
func TestHostCanRunIsTheOnePlacementRule(t *testing.T) {
	p := testPool("gpu", "gpu")
	p.HostSelector = store.StringMap{"zone": "a"}
	fits := testHost("host_a", 2, 0)
	fits.Labels = store.StringMap{"zone": "a"}

	cases := []struct {
		name string
		mut  func(h *store.Host)
		want bool
	}{
		{"a healthy, uncordoned host with the backend and the labels", func(*store.Host) {}, true},
		{"cordoned", func(h *store.Host) { h.Cordoned = true }, false},
		{"silent", func(h *store.Host) { h.LastHeartbeat = now.Add(-time.Hour) }, false},
		{"without the backend", func(h *store.Host) { h.Backends = store.StringSlice{"process"} }, false},
		{"in the wrong zone", func(h *store.Host) { h.Labels = store.StringMap{"zone": "b"} }, false},
		{"full, which is the scheduler's own accounting and not this rule's", func(h *store.Host) { h.ActiveRunners = h.Capacity }, true},
	}
	for _, tc := range cases {
		h := *fits
		tc.mut(&h)
		if got := HostCanRun(&h, p, now); got != tc.want {
			t.Errorf("%s: HostCanRun = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A runner made from an image the pool no longer names is replaced, and only
// when it is not doing anything: the change that matters most is a pool that
// gained a Docker daemon and with it a different image, whose warm runners
// would otherwise take the next Docker job onto an image with no client.
func TestARunnerOnAnImageThePoolNoLongerNamesIsReplaced(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.Image = "ghcr.io/eyupio/zoomies-runner-docker:latest"
	stale := func(id string, state store.RunnerState) *store.Runner {
		r := testRunner(id, p, state, time.Minute)
		if state == store.RunnerIdle {
			since := ago(time.Minute)
			r.LastIdleAt = &since
		}
		r.Image = "ghcr.io/eyupio/zoomies-runner:latest"
		return r
	}
	current := idleRunner("r1", p, time.Minute)
	current.Image = p.Image
	unrecorded := idleRunner("r1", p, time.Minute)

	const reason = "pool image is now ghcr.io/eyupio/zoomies-runner-docker:latest; this runner was made from ghcr.io/eyupio/zoomies-runner:latest"
	tests := []struct {
		name       string
		runner     *store.Runner
		wantReason string
	}{
		{"an idle runner on the old image is drained", stale("r1", store.RunnerIdle), reason},
		{"one still registering is drained before it can take a job", stale("r1", store.RunnerRegistering), reason},
		{"a busy one keeps its job", stale("r1", store.RunnerBusy), ""},
		{"one already draining is left to drain", stale("r1", store.RunnerDraining), ""},
		{"a runner on the pool's image is left alone", current, ""},
		{"a runner row that never recorded an image is left alone", unrecorded, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := snap([]*store.Pool{p}, []*store.Runner{tc.runner}, nil, []*store.Host{testHost("host_a", 8, 1)})
			pp := only(t, Decide(s))
			var drains []Action
			for _, a := range pp.Actions {
				if a.Kind == ActionDrain {
					drains = append(drains, a)
				}
			}
			if tc.wantReason == "" {
				if len(drains) != 0 {
					t.Fatalf("expected no drain, got %+v", drains)
				}
				return
			}
			if len(drains) != 1 || drains[0].RunnerID != "r1" || drains[0].Reason != tc.wantReason {
				t.Fatalf("drains = %+v, want one of r1 with reason %q", drains, tc.wantReason)
			}
		})
	}

	// A pool with no image of its own runs the instance default, which its
	// runner rows record and it does not; that difference is not staleness.
	p.Image = ""
	s := snap([]*store.Pool{p}, []*store.Runner{stale("r1", store.RunnerIdle)}, nil, []*store.Host{testHost("host_a", 8, 1)})
	for _, a := range only(t, Decide(s)).Actions {
		if a.Kind == ActionDrain {
			t.Fatalf("a pool on the default image drained %+v", a)
		}
	}
}

// A pool may select on the OS and architecture the agent reports, so an
// operator can keep arm64 work on arm64 boxes across a fleet nobody has
// labelled. An operator's own label still wins, which is what keeps a fleet
// that already labelled `arch` by hand meaning what it chose.
func TestHostSelectsMatchesReportedOSAndArch(t *testing.T) {
	arm := testHost("host_arm", 2, 0)
	arm.OS, arm.Arch = "linux", "arm64"
	win := testHost("host_win", 2, 0)
	win.OS, win.Arch = "windows", "amd64"
	// The one host whose operator disagrees with its agent.
	lied := testHost("host_lied", 2, 0)
	lied.OS, lied.Arch = "linux", "amd64"
	lied.Labels = store.StringMap{"arch": "legacy"}

	cases := []struct {
		name     string
		selector store.StringMap
		host     *store.Host
		want     bool
	}{
		{"unlabelled arm64 host, selector asks for arm64", store.StringMap{"arch": "arm64"}, arm, true},
		{"unlabelled arm64 host, selector asks for amd64", store.StringMap{"arch": "amd64"}, arm, false},
		{"windows host by os", store.StringMap{"os": "windows"}, win, true},
		{"linux selector does not take the windows host", store.StringMap{"os": "linux"}, win, false},
		{"os and arch together", store.StringMap{"os": "linux", "arch": "arm64"}, arm, true},
		{"os matches but arch does not", store.StringMap{"os": "linux", "arch": "amd64"}, arm, false},
		{"an explicit label beats the reported arch", store.StringMap{"arch": "legacy"}, lied, true},
		{"and the reported arch no longer answers once a label claims the key", store.StringMap{"arch": "amd64"}, lied, false},
		{"an empty selector still means any host", nil, arm, true},
		{"a key nothing answers for matches nothing", store.StringMap{"zone": "eu"}, arm, false},
	}
	for _, tc := range cases {
		p := testPool("p", "p")
		p.HostSelector = tc.selector
		if got := HostSelects(tc.host, p); got != tc.want {
			t.Errorf("%s: HostSelects = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Selecting on a reported fact has to reach placement, not just the predicate:
// this is the case an operator actually types, and the pool has to land on the
// arm64 box and leave the amd64 one alone.
func TestPlacementHonoursAReportedArchSelector(t *testing.T) {
	arm := testHost("host_arm", 4, 0)
	arm.OS, arm.Arch = "linux", "arm64"
	amd := testHost("host_amd", 4, 0)
	amd.OS, amd.Arch = "linux", "amd64"

	p := testPool("arm-pool", "arm-pool")
	p.HostSelector = store.StringMap{"arch": "arm64"}

	hs := newHostSet([]*store.Host{arm, amd}, []*store.Pool{p}, nil, now)
	got := hs.place(p, 3)
	if len(got) != 3 {
		t.Fatalf("placed %d runners, want 3: %v", len(got), got)
	}
	for _, id := range got {
		if id != "host_arm" {
			t.Errorf("placed on %s, want only host_arm", id)
		}
	}
}

// hostOn returns a host that has told the fleet what machine it is.
func hostOn(id, distro, version, arch string, capacity int) *store.Host {
	h := testHost(id, capacity, 0)
	h.OS, h.Distro, h.OSVersion, h.Arch = "linux", distro, version, arch
	return h
}

func TestRunnersOnlyLandOnHostsThatMatchThePoolsPlatform(t *testing.T) {
	pool := testPool("zoomies-4vcpu-ubuntu-2404-arm64", "zoomies-4vcpu-ubuntu-2404-arm64")
	pool.Platform = store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "arm64"}

	hosts := []*store.Host{
		hostOn("host_amd", "ubuntu", "24.04", "amd64", 4),
		hostOn("host_old", "ubuntu", "22.04", "arm64", 4),
		hostOn("host_deb", "debian", "12", "arm64", 4),
		hostOn("host_right", "ubuntu", "24.04", "arm64", 4),
	}
	jobs := []*store.Job{queued("job1", time.Minute, "zoomies-4vcpu-ubuntu-2404-arm64")}

	plan := Decide(snap([]*store.Pool{pool}, nil, jobs, hosts))
	creates := actionsOf(plan.Actions, ActionCreate)
	if len(creates) != 1 {
		t.Fatalf("got %d creates, want 1: %+v", len(creates), creates)
	}
	if creates[0].HostID != "host_right" {
		t.Errorf("placed on %s; the only Ubuntu 24.04 arm64 host is host_right", creates[0].HostID)
	}
}

func TestAPoolWithNoPlatformStillGoesAnywhere(t *testing.T) {
	// Every fleet built before platforms existed has pools like this one. They
	// must keep placing exactly as they did.
	pool := testPool("linux-x64", "linux-x64")
	hosts := []*store.Host{hostOn("host_a", "debian", "12", "amd64", 2)}
	jobs := []*store.Job{queued("job1", time.Minute, "linux-x64")}

	plan := Decide(snap([]*store.Pool{pool}, nil, jobs, hosts))
	if got := len(actionsOf(plan.Actions, ActionCreate)); got != 1 {
		t.Fatalf("got %d creates, want 1", got)
	}
}

func TestAPlatformMismatchExplainsItselfWithTheMachineToAdd(t *testing.T) {
	pool := testPool("zoomies-4vcpu-ubuntu-2404-arm64", "zoomies-4vcpu-ubuntu-2404-arm64")
	pool.Platform = store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "arm64"}
	hosts := []*store.Host{hostOn("host_amd", "ubuntu", "24.04", "amd64", 4)}
	jobs := []*store.Job{queued("job1", time.Minute, "zoomies-4vcpu-ubuntu-2404-arm64")}

	plan := Decide(snap([]*store.Pool{pool}, nil, jobs, hosts))
	if got := len(actionsOf(plan.Actions, ActionCreate)); got != 0 {
		t.Fatalf("got %d creates, want none: the only host is amd64", got)
	}
	reason := plan.Pools[0].Reason
	for _, want := range []string{"1 not Ubuntu 24.04, arm64", "add a Ubuntu 24.04, arm64 host"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not contain %q", reason, want)
		}
	}
}

func TestAHostThatHasNotSaidWhatItIsIsNotRuledOut(t *testing.T) {
	// An agent from before this field existed reports no distribution. Refusing
	// to place on it would turn an upgrade into an outage.
	pool := testPool("zoomies-4vcpu-ubuntu-2404", "zoomies-4vcpu-ubuntu-2404")
	pool.Platform = store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "amd64"}
	hosts := []*store.Host{testHost("host_quiet", 4, 0)}
	jobs := []*store.Job{queued("job1", time.Minute, "zoomies-4vcpu-ubuntu-2404")}

	plan := Decide(snap([]*store.Pool{pool}, nil, jobs, hosts))
	if got := len(actionsOf(plan.Actions, ActionCreate)); got != 1 {
		t.Fatalf("got %d creates, want 1", got)
	}
}

// drainingRunner is a runner that was asked to stop drainedFor ago and has
// been in draining ever since, with nothing left to wait for.
func drainingRunner(id string, p *store.Pool, drainedFor time.Duration) *store.Runner {
	r := testRunner(id, p, store.RunnerDraining, drainedFor+time.Hour)
	since := ago(drainedFor)
	r.DrainingSince = &since
	return r
}

// The stop task queue is in memory by design, so a controller restart drops a
// stop that had already gone out. Nothing counted against the row it left
// behind: draining is neither a failure to see nor a runner to replace, so it
// held its slot on the host and its pool ran one short for as long as the
// controller lived.
func TestARunnerDrainingWithNothingLeftToWaitForIsEventuallyFailed(t *testing.T) {
	p := testPool("builders", "self-hosted")
	r := drainingRunner("run_stuck", p, 16*time.Minute)

	plan := Decide(snap([]*store.Pool{p}, []*store.Runner{r}, nil, []*store.Host{testHost("host_a", 4, 1)}))

	fails := actionsOf(plan.Actions, ActionFail)
	if len(fails) != 1 || fails[0].RunnerID != "run_stuck" {
		t.Fatalf("the stuck drain must be failed, got %+v", plan.Actions)
	}
	// The reason has to name the drain, or an operator reads "failed" and goes
	// looking for a fault on the host that never happened.
	if !strings.Contains(fails[0].Reason, "drain timeout") {
		t.Fatalf("reason = %q", fails[0].Reason)
	}
}

// The timeout is counted from the moment it entered draining, not from birth.
// A runner that served jobs all day and was drained a minute ago is not overdue.
func TestADrainIsTimedFromWhenItStartedDrainingNotFromBirth(t *testing.T) {
	p := testPool("builders", "self-hosted")
	r := testRunner("run_old", p, store.RunnerDraining, 20*time.Hour)
	since := ago(time.Minute)
	r.DrainingSince = &since

	plan := Decide(snap([]*store.Pool{p}, []*store.Runner{r}, nil, []*store.Host{testHost("host_a", 4, 1)}))

	if fails := actionsOf(plan.Actions, ActionFail); len(fails) != 0 {
		t.Fatalf("a runner that has only just started draining was failed: %+v", fails)
	}
}

// A drain waits for the job the runner is running, however long that takes.
// Zoomies has no maximum job duration by design -- that is the workflow's
// timeout-minutes -- and failing here would end somebody's build from the far
// side of the fleet, with a reason that names nothing they did.
func TestADrainStillFinishingItsJobIsNeverFailedForTakingTooLong(t *testing.T) {
	p := testPool("builders", "self-hosted")
	r := drainingRunner("run_working", p, 9*time.Hour)
	r.CurrentJobID = "job_long"

	plan := Decide(snap([]*store.Pool{p}, []*store.Runner{r}, nil, []*store.Host{testHost("host_a", 4, 1)}))

	if fails := actionsOf(plan.Actions, ActionFail); len(fails) != 0 {
		t.Fatalf("a runner still running a job was failed for draining slowly: %+v", fails)
	}
}

// Zero is off, which is what a deployment that has never set it had before.
func TestNoDrainTimeoutLeavesADrainAlone(t *testing.T) {
	p := testPool("builders", "self-hosted")
	r := drainingRunner("run_stuck", p, 30*24*time.Hour)
	s := snap([]*store.Pool{p}, []*store.Runner{r}, nil, []*store.Host{testHost("host_a", 4, 1)})
	s.Policy.DrainTimeout = 0

	if fails := actionsOf(Decide(s).Actions, ActionFail); len(fails) != 0 {
		t.Fatalf("a drain was failed with the timeout off: %+v", fails)
	}
}

// A row written before draining_since existed still has to be reachable, or the
// very drains this rule was added for -- the ones already stuck when it shipped
// -- would be the only ones it could never end.
func TestADrainFromBeforeTheColumnExistedIsStillBounded(t *testing.T) {
	p := testPool("builders", "self-hosted")
	r := testRunner("run_legacy", p, store.RunnerDraining, 3*time.Hour)
	r.DrainingSince = nil

	plan := Decide(snap([]*store.Pool{p}, []*store.Runner{r}, nil, []*store.Host{testHost("host_a", 4, 1)}))

	if fails := actionsOf(plan.Actions, ActionFail); len(fails) != 1 {
		t.Fatalf("a drain with no recorded start was left stuck for ever: %+v", plan.Actions)
	}
}

// A pool that cannot start a runner waits, and the wait doubles with each
// failure. Every pool broken by the same thing -- a daemon that went down, a
// registry that stopped answering -- counts the same doubling from the same
// failure, so without jitter they all come back in the same second and the
// recovery arrives as the same herd that broke them.
func TestTheStartBackoffIsSpreadByTheJitterTheSnapshotCarries(t *testing.T) {
	p := testPool("builders", "self-hosted")
	// One start failure: the wait is the base backoff, and the pool is exactly
	// far enough past it to be let go with no jitter at all.
	failed := testRunner("run_dead", p, store.RunnerFailed, time.Minute)
	when := ago(startBackoff + time.Second)
	failed.FinishedAt = &when

	held := func(jitter float64) bool {
		s := snap([]*store.Pool{p}, []*store.Runner{failed}, []*store.Job{queued("job_1", time.Minute, "self-hosted")},
			[]*store.Host{testHost("host_a", 4, 0)})
		s.Jitter = map[string]float64{p.ID: jitter}
		return only(t, Decide(s)).Failing != ""
	}

	if held(0) {
		t.Fatal("with no jitter the wait is out, so the pool may try again")
	}
	// The same pool, at the same instant, with the jitter drawn high: the wait
	// is longer and it is still holding.
	if !held(1) {
		t.Fatal("the jitter was not applied, so every pool that broke together retries together")
	}
}

// Jitter only ever lengthens a wait. Shortening it would let the pool failing
// hardest come back soonest, which is the opposite of what the doubling is for.
func TestJitterNeverShortensAWait(t *testing.T) {
	for _, f := range []float64{0, 0.5, 1, 2, -1} {
		if got := jittered(time.Minute, f); got < time.Minute {
			t.Fatalf("jittered(1m, %v) = %s, which is shorter than the wait it spreads", f, got)
		}
		if got := jittered(time.Minute, f); got > time.Minute+time.Duration(startBackoffJitter*float64(time.Minute)) {
			t.Fatalf("jittered(1m, %v) = %s, which is past the share it may add", f, got)
		}
	}
}

// A pool whose installation GitHub is rate-limiting creates nothing. The
// request would be refused identically, spend another call against a quota
// that is already gone, and leave a failed row whose only message is the
// refusal -- and the pool would then back off from its own failures on top of
// the hold GitHub asked for. Scaling down asks GitHub for nothing, so it
// still happens.
func TestARateLimitedInstallationHoldsCreatesButNotDrains(t *testing.T) {
	p := testPool("linux-x64", "linux", "x64")
	s := snap([]*store.Pool{p}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux", "x64")},
		[]*store.Host{testHost("host_a", 8, 0)})
	s.HeldInstallations = map[string]time.Time{testInstallation: now.Add(7 * time.Minute)}

	pp := only(t, Decide(s))
	if n := countOf(pp.Actions, ActionCreate); n != 0 {
		t.Fatalf("got %d creates for a held installation, want 0", n)
	}
	if pp.Held == "" || !strings.Contains(pp.Held, "7m") {
		t.Fatalf("Held = %q, want it to say how long the hold lasts", pp.Held)
	}
	if !strings.Contains(pp.Reason, "rate-limiting") {
		t.Fatalf("reason = %q, want it to name the hold", pp.Reason)
	}
	if pp.Blocked != "" {
		t.Fatalf("Blocked = %q; a hold is not a placement failure", pp.Blocked)
	}

	// An expired hold is no hold. The controller drops these as it reads
	// them, but the scheduler must not depend on that.
	s.HeldInstallations[testInstallation] = now.Add(-time.Second)
	pp = only(t, Decide(s))
	if n := countOf(pp.Actions, ActionCreate); n != 1 {
		t.Fatalf("got %d creates after the hold expired, want 1", n)
	}
	if pp.Held != "" {
		t.Fatalf("Held = %q after the hold expired, want empty", pp.Held)
	}

	// Another installation's hold is not this pool's.
	s.HeldInstallations = map[string]time.Time{"ins_someoneelse": now.Add(time.Hour)}
	if pp := only(t, Decide(s)); countOf(pp.Actions, ActionCreate) != 1 {
		t.Fatalf("a hold on another installation stopped this pool creating")
	}

	// And a held pool still scales down.
	s.HeldInstallations = map[string]time.Time{testInstallation: now.Add(time.Hour)}
	s.Jobs = nil
	s.Runners[p.ID] = []*store.Runner{idleRunner("r1", p, time.Hour)}
	pp = only(t, Decide(s))
	if n := countOf(pp.Actions, ActionDrain); n != 1 {
		t.Fatalf("got %d drains while held, want 1: draining asks GitHub for nothing", n)
	}
}

// A host that goes quiet is given up as lost and its runners are failed, and
// when it comes back with a job still running the workload is deliberately
// left to finish. The reap must not undo that: removing the row removes the
// container, and the job with it, for no reason but tidiness.
func TestAFailedRunnerStillRunningAJobIsNotReaped(t *testing.T) {
	p := testPool("linux-x64", "linux")
	r := testRunner("r1", p, store.RunnerFailed, 11*time.Minute)
	hosts := []*store.Host{testHost("host_a", 8, 1)}
	running := &store.Job{ID: "job_1", PoolID: p.ID, RunnerID: "r1", State: store.JobInProgress,
		Repo: "acme/widgets", InstallationID: testInstallation}

	pp := only(t, Decide(snap([]*store.Pool{p}, []*store.Runner{r}, []*store.Job{running}, hosts)))
	if got := actionsOf(pp.Actions, ActionRemove); len(got) != 0 {
		t.Fatalf("a failed runner with a job still on it was removed: %+v", got)
	}

	// Once the job has ended the row goes the way every failed row goes.
	running.State = store.JobCompleted
	pp = only(t, Decide(snap([]*store.Pool{p}, []*store.Runner{r}, []*store.Job{running}, hosts)))
	if got := actionsOf(pp.Actions, ActionRemove); len(got) != 1 || got[0].RunnerID != "r1" {
		t.Fatalf("the failed runner was not removed once its job had finished: %+v", pp.Actions)
	}
}

// An empty fleet is demand, not a full fleet.
//
// Until this was said out loud, a pool with queued jobs and nowhere at all to
// put a runner reported neither blockage: `why` answers the empty fleet before
// it counts anything, so `atCapacity` stayed false and the only signal a host
// provisioner listens for was never raised. Scaling from zero -- the whole
// point of renting a host -- was silent.
func TestAPoolWithNoHostsAtAllStillSaysItsJobsHaveNowhereToGo(t *testing.T) {
	pp := only(t, Decide(snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, nil)))

	if !pp.BlockedNoEligibleHost {
		t.Fatalf("plan = %+v, want an empty fleet reported as one no job will clear", pp)
	}
	// The two flags mean different things and the distinction is load-bearing:
	// "full" is a fleet working, and problems.go and the usage record both read
	// it that way.
	if pp.BlockedAtCapacity {
		t.Fatal("an empty fleet is not a fleet at capacity")
	}
	if pp.Blocked == "" {
		t.Fatal("a blocked pool still has to say so in words")
	}
}

// The fleet being busy and the fleet being wrong are different answers, and an
// operator told to wait for the second one waits for ever.
func TestAFullFleetIsStillDistinguishedFromAFleetThatCanNeverRunThePool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hosts []*store.Host
		// pool and runners are optional: the rows about memory and CPU need a
		// pool that states a size and a runner already holding part of a host.
		pool           *store.Pool
		runners        []*store.Runner
		atCapacity     bool
		noEligibleHost bool
	}{
		{
			name:       "every host busy",
			hosts:      []*store.Host{testHost("host_a", 1, 1), testHost("host_b", 2, 2)},
			atCapacity: true,
		},
		{
			name: "every host offering another backend",
			hosts: func() []*store.Host {
				h := testHost("host_a", 8, 0)
				h.Backends = store.StringSlice{"process"}
				return []*store.Host{h}
			}(),
			noEligibleHost: true,
		},
		{
			name: "one host busy and one that could never run it",
			hosts: func() []*store.Host {
				other := testHost("host_b", 8, 0)
				other.Backends = store.StringSlice{"process"}
				return []*store.Host{testHost("host_a", 1, 1), other}
			}(),
			// Neither: one host is only busy, so a finished job would clear it.
		},
		{
			// A pressure hold is the most transient blockage there is: a job
			// spikes the CPU, new starts pause for a few seconds, and it
			// clears on its own. Reading it as "no host can ever run this"
			// would clone virtual machines for a thirty-second spike -- and
			// tell an external provisioner to scale from zero as well -- while
			// the hold lifts long before the first clone finishes.
			name: "every host under a momentary pressure hold",
			hosts: func() []*store.Host {
				h := withUsage(sized("host_a", 4, 4, 8192, 100000), 99, 12000)
				h.Usage.CPUHeld = true
				return []*store.Host{h}
			}(),
			// Neither: the hold is waited out, not bought around.
		},
		{
			// And a hold on one host does not excuse the rest: the others are
			// wrong in a way no waiting fixes, but this one is not.
			name: "one host held and one that could never run it",
			hosts: func() []*store.Host {
				h := withUsage(sized("host_a", 4, 4, 8192, 100000), 99, 12000)
				h.Usage.CPUHeld = true
				other := testHost("host_b", 8, 0)
				other.Backends = store.StringSlice{"process"}
				return []*store.Host{h, other}
			}(),
			// Neither: the held host clears itself.
		},
		{
			// A throttle is the fleet being busy, not the fleet being wrong.
			// It asks for capacity like a full host does, because more hosts
			// is the answer either way -- but it lifts on its own, so a fleet
			// blocked only by throttles must never read as one no host can
			// ever run. That would buy a machine for a fleet that needed a
			// minute, and the machine would outlive the pressure.
			name: "every host throttled back to no slots",
			hosts: func() []*store.Host {
				h := testHost("host_a", 4, 1)
				h.Throttle = store.HostThrottle{Level: store.MaxThrottleLevel}
				return []*store.Host{h}
			}(),
			atCapacity: true,
		},
		{
			// And a throttle on one host does not excuse the rest: the others
			// are wrong in a way no waiting fixes, but this one is not, so
			// there is still something to wait for.
			name: "one host throttled and one that could never run it",
			hosts: func() []*store.Host {
				h := testHost("host_a", 4, 1)
				h.Throttle = store.HostThrottle{Level: store.MaxThrottleLevel}
				other := testHost("host_b", 8, 0)
				other.Backends = store.StringSlice{"process"}
				return []*store.Host{h, other}
			}(),
			// Neither: the throttled host clears itself.
		},
		{
			// A host with a free slot whose memory is promised to a runner
			// that is still working is the ordinary busy state: the job
			// finishing returns it. Reading it as "no host could ever run
			// this pool" raises an error, and tells an external provisioner
			// to scale from zero, for a fleet that needed a minute.
			name: "every host's memory promised to a runner that will finish",
			hosts: func() []*store.Host {
				h := sized("host_a", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 200*1024)
				h.ActiveRunners = 1
				return []*store.Host{h}
			}(),
			pool:       limited("linux", 0, 6*1024),
			runners:    []*store.Runner{onHost(testRunner("r1", limited("linux", 0, 6*1024), store.RunnerBusy, 0), "host_a")},
			atCapacity: true,
		},
		{
			// CPU is returned by a finishing job exactly as memory is.
			name: "every host's CPU promised to a runner that will finish",
			hosts: func() []*store.Host {
				h := sized("host_a", 8, 8, 64*1024, 200*1024)
				h.ActiveRunners = 1
				return []*store.Host{h}
			}(),
			pool:       limited("linux", 6, 1024),
			runners:    []*store.Runner{onHost(testRunner("r1", limited("linux", 6, 1024), store.RunnerBusy, 0), "host_a")},
			atCapacity: true,
		},
		{
			// Memory the host has lost to something that is not ours is not
			// returned by any job of ours finishing, so it stays what it was:
			// neither flag, and the pool is simply skipped until it eases.
			name: "memory lost to work outside the fleet",
			hosts: []*store.Host{
				withUsage(sized("host_a", 8, 16, 16*1024, 200*1024), 10, 1024),
			},
			pool:           limited("linux", 0, 6*1024),
			noEligibleHost: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := tc.pool
			if pool == nil {
				pool = testPool("linux-x64", "linux")
			}
			pp := only(t, Decide(snap([]*store.Pool{pool}, tc.runners,
				[]*store.Job{queued("j1", time.Minute, "linux")}, tc.hosts)))

			if pp.BlockedAtCapacity != tc.atCapacity {
				t.Errorf("BlockedAtCapacity = %v, want %v (%s)", pp.BlockedAtCapacity, tc.atCapacity, pp.Blocked)
			}
			if pp.BlockedNoEligibleHost != tc.noEligibleHost {
				t.Errorf("BlockedNoEligibleHost = %v, want %v (%s)", pp.BlockedNoEligibleHost, tc.noEligibleHost, pp.Blocked)
			}
		})
	}
}

// A fleet blocked only by memory that live runners hold is at capacity to the
// callers that count it, but the operator is still told the more specific
// thing: the limits are what to change, not only the number of hosts. Folding
// it into the plain "at capacity" advice would have hidden that.
func TestAMemoryCommittedFleetKeepsTheAdviceAboutMemory(t *testing.T) {
	p := limited("linux", 0, 6*1024)
	h := sized("host_a", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 200*1024)
	h.ActiveRunners = 1
	runners := []*store.Runner{onHost(testRunner("r1", p, store.RunnerBusy, 0), "host_a")}

	pp := only(t, Decide(snap([]*store.Pool{p}, runners, []*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{h})))
	if !strings.Contains(pp.BlockedFix, "lower this pool's limits") {
		t.Errorf("BlockedFix = %q, want the advice about the pool's limits", pp.BlockedFix)
	}
}
