package scheduler

import (
	"encoding/json"
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
		MaxCreatesPerTick: 10,
	}
}

func testPool(name string, labels ...string) *store.Pool {
	return &store.Pool{
		ID:          "pool_" + name,
		Name:        name,
		Labels:      store.NormalizeLabels(labels),
		Backend:     store.BackendDocker,
		MaxRunners:  10,
		IdleTimeout: store.Duration(5 * time.Minute),
		Ephemeral:   true,
		Enabled:     true,
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
		Labels: store.StringSlice(labels), QueuedAt: ago(waited),
	}
}

// snap assembles a snapshot, filing each runner under its own pool.
func snap(pools []*store.Pool, runners []*store.Runner, jobs []*store.Job, hosts []*store.Host) Snapshot {
	byPool := map[string][]*store.Runner{}
	for _, r := range runners {
		byPool[r.PoolID] = append(byPool[r.PoolID], r)
	}
	return Snapshot{Now: now, Pools: pools, Runners: byPool, Jobs: jobs, Hosts: hosts, Policy: testPolicy()}
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
	if want := "scaled linux-x64 0 -> 2: 5 jobs queued"; pp.Reason != want {
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
		{"a failed runner is removed", testRunner("r1", p, store.RunnerFailed, time.Minute),
			ActionRemove, "runner failed; removing it to free host capacity"},
		{"an old idle runner is retired", idleRunner("r1", p, 7*time.Hour),
			ActionDrain, "runner reached the 6h maximum lifetime"},
		{"an old busy runner keeps its job", testRunner("r1", p, store.RunnerBusy, 7*time.Hour), "", ""},
		{"an old draining runner is left to drain", testRunner("r1", p, store.RunnerDraining, 7*time.Hour), "", ""},
		{"a young idle runner is left alone", idleRunner("r1", p, time.Minute), "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := snap([]*store.Pool{p}, []*store.Runner{tc.runner}, nil, []*store.Host{testHost("host_a", 8, 1)})
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
	s := snap([]*store.Pool{p}, []*store.Runner{testRunner("dead", p, store.RunnerFailed, time.Minute)},
		nil, []*store.Host{testHost("host_a", 8, 1)})

	pp := only(t, Decide(s))
	if countOf(pp.Actions, ActionRemove) != 1 || countOf(pp.Actions, ActionCreate) != 1 {
		t.Fatalf("want one remove and one create, got %+v", pp.Actions)
	}
	if pp.Actions[0].Kind != ActionRemove {
		t.Fatal("cleanup must come before the create that reuses the capacity")
	}
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
	if want := "scaled linux-x64 0 -> 3: 5 jobs queued"; pp.Reason != want {
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
	if len(plan.Unmatched) != 1 || plan.Unmatched[0].ID != "j1" {
		t.Fatalf("a job whose only pool is disabled must be reported unmatched, got %v", plan.Unmatched)
	}
}

func TestDisabledPoolIsStillReaped(t *testing.T) {
	p := testPool("linux-x64", "linux")
	p.Enabled = false
	runners := []*store.Runner{
		testRunner("dead", p, store.RunnerFailed, time.Minute),
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
	for _, j := range plan.Unmatched {
		got = append(got, j.ID)
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
		testRunner("r_dead", gpu, store.RunnerFailed, time.Minute),
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
	if len(plan.Unmatched) != 1 || plan.Unmatched[0].ID != "j_lost" {
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
		for _, j := range plan.Unmatched {
			order = append(order, j.ID)
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
