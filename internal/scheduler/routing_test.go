package scheduler

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// classPool is the pool the controller keeps for an architecture and class, in
// the installation the shared test fixtures run under.
func classPool(a string, class store.SizeClass, max int) *store.Pool {
	p := NewAutoPool(AutoPoolInput{InstallationID: testInstallation}, a, class, store.BackendDocker)
	p.ID = "pool_" + a + "_" + string(class)
	p.MaxRunners = max
	return p
}

// classHost is a machine in a class, big enough that a pool's runners rather
// than its slot count are what it runs out of.
func classHost(name string, class store.SizeClass, cpus int, memoryMB int64) *store.Host {
	return &store.Host{
		ID: "host_" + name, Name: name, CPUs: cpus, MemoryMB: memoryMB, Capacity: 16, Arch: "amd64", OS: "linux",
		Backends: store.StringSlice{"docker"}, LastHeartbeat: now, SizeClass: store.HostSizeClass{Class: class},
	}
}

// routedJob is a queued job that has been classed and sent to a class.
func routedJob(id string, waited time.Duration, class store.SizeClass, labels ...string) *store.Job {
	j := queued(id, waited, labels...)
	j.SizeClass, j.RoutedClass, j.SizeBasis = class, class, store.SizeBasisHistory
	return j
}

// route is one reconcile pass of the routing step: decide, then ask which jobs
// should go elsewhere.
func route(s Snapshot) ([]Reroute, Plan) {
	cfg := DefaultSizeConfig()
	sized := make([]*store.Pool, len(s.Pools))
	for i, p := range s.Pools {
		sized[i] = sizingFor(cfg)(p)
	}
	s.Pools = sized
	plan := Decide(s)
	return Fallbacks(RoutingInput{
		Now: s.Now, Pools: s.Pools, Hosts: s.Hosts, Jobs: s.Jobs, Plan: plan, Config: cfg, Size: sizingFor(cfg),
	}), plan
}

func busy(id string, p *store.Pool, host string) *store.Runner {
	r := testRunner(id, p, store.RunnerBusy, time.Hour)
	r.HostID = host
	return r
}

// ---------------------------------------------------------------------------
// The claim
// ---------------------------------------------------------------------------

func claimed(pools []*store.Pool, j *store.Job) string {
	if p := BestPool(pools, j); p != nil {
		return p.Name
	}
	return ""
}

func TestAJobRoutedToAClassIsClaimedByThePoolOfThatClass(t *testing.T) {
	pools := []*store.Pool{
		classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeMedium, 4), classPool("amd64", store.SizeLarge, 4),
	}
	for _, class := range store.SizeClasses() {
		j := routedJob("j", time.Minute, class, "self-hosted", "zoomies")
		if got, want := claimed(pools, j), "zoomies-"+string(class); got != want {
			t.Errorf("a job routed to %s was claimed by %q, want %q", class, got, want)
		}
	}
	// A job that has not been routed -- every job while size routing is off --
	// is ordered exactly as it always was: by name.
	plain := queued("j", time.Minute, "self-hosted", "zoomies")
	if got := claimed(pools, plain); got != "zoomies-large" {
		t.Errorf("an unrouted job was claimed by %q; the old order is by name, which puts zoomies-large first", got)
	}
}

// With no pool in the class a job is routed to, it is claimed by the nearest:
// larger first, because a larger host always has what the job needs.
func TestAClassWithNoPoolIsClaimedByTheNearestLargerAndThenTheNearestSmaller(t *testing.T) {
	pools := []*store.Pool{classPool("amd64", store.SizeMedium, 4), classPool("amd64", store.SizeLarge, 4)}
	if got := claimed(pools, routedJob("j", 0, store.SizeSmall, "zoomies")); got != "zoomies-medium" {
		t.Errorf("small with no small pool was claimed by %q, want the next size up", got)
	}
	pools = []*store.Pool{classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeMedium, 4)}
	if got := claimed(pools, routedJob("j", 0, store.SizeLarge, "zoomies")); got != "zoomies-medium" {
		t.Errorf("large with no larger pool was claimed by %q, want the nearest below", got)
	}
	pools = []*store.Pool{classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeLarge, 4)}
	if got := claimed(pools, routedJob("j", 0, store.SizeMedium, "zoomies")); got != "zoomies-large" {
		t.Errorf("medium with no medium pool was claimed by %q, want large before small", got)
	}
}

// The one guaranteed path: a job that asks for a class by name is claimed only
// by a pool that carries the label, whatever it was routed to.
func TestASizeLabelIsAnsweredOnlyByThePoolsThatCarryIt(t *testing.T) {
	pools := []*store.Pool{classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeLarge, 4)}
	j := routedJob("j", 0, store.SizeSmall, "zoomies", "zoomies-large")
	if got := claimed(pools, j); got != "zoomies-large" {
		t.Errorf("a job that asks for zoomies-large was claimed by %q", got)
	}
	j = routedJob("j", 0, store.SizeLarge, "zoomies", "zoomies-medium")
	if got := claimed(pools, j); got != "" {
		t.Errorf("a job that asks for zoomies-medium was claimed by %q though no pool carries it", got)
	}
}

// Coexistence. A pool an operator made sits between the pool in the right class
// and every other: it was not made for a class, so it has no reason to lose to
// a pool made for the wrong one, and none to beat the right one.
func TestAnOperatorPoolSitsBetweenTheRightClassAndTheWrongOnes(t *testing.T) {
	operator := testPool("zoomies-ops", "zoomies", "zoomies-ops", "linux", "x64")
	small, medium := classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeMedium, 4)
	pools := []*store.Pool{operator, small, medium}

	if got := claimed(pools, routedJob("j", 0, store.SizeMedium, "zoomies")); got != "zoomies-medium" {
		t.Errorf("a medium job was claimed by %q, want the medium pool", got)
	}
	if got := claimed(pools, routedJob("j", 0, store.SizeLarge, "zoomies")); got != "zoomies-ops" {
		t.Errorf("a large job with no large pool was claimed by %q, want the operator's pool over a smaller class", got)
	}
	// The same, with the operator's pool named so that it sorts first and last.
	for _, name := range []string{"zoomies-a", "zoomies-z"} {
		op := testPool(name, "zoomies", name, "linux", "x64")
		if got := claimed([]*store.Pool{op, small, medium}, routedJob("j", 0, store.SizeSmall, "zoomies")); got != "zoomies-small" {
			t.Errorf("with operator pool %s a small job was claimed by %q", name, got)
		}
	}
}

// Surplus is still what decides first: a pool that advertises less than the job
// did not ask for is the better fit, whatever class it is in. An operator's
// pool that carries nothing but the label is claimed ahead of the pools the
// controller keeps, which is the coexistence rule, and the reason a fleet that
// wants its base-label jobs routed leaves no such pool in the way.
func TestSurplusStillDecidesBeforeClass(t *testing.T) {
	bare := testPool("zoomies-everything", "zoomies", "linux", "x64")
	medium := classPool("amd64", store.SizeMedium, 4)
	if got := claimed([]*store.Pool{medium, bare}, routedJob("j", 0, store.SizeMedium, "zoomies")); got != "zoomies-everything" {
		t.Errorf("claimed by %q; the pool with fewer labels the job did not ask for should win", got)
	}
}

func TestTheDefaultArchitectureIsClaimedForAJobThatNamesNone(t *testing.T) {
	pools := []*store.Pool{classPool("arm64", store.SizeMedium, 4), classPool("amd64", store.SizeMedium, 4)}
	if got := claimed(pools, routedJob("j", 0, store.SizeMedium, "zoomies")); got != "zoomies-medium" {
		t.Errorf("a job that names no architecture was claimed by %q, want the amd64 pool", got)
	}
	if got := claimed(pools, routedJob("j", 0, store.SizeMedium, "zoomies", "arm64")); got != "zoomies-arm64-medium" {
		t.Errorf("a job that asks for arm64 was claimed by %q", got)
	}
	// An architecture that has no pool in the class is still a better match
	// than a class that has the architecture's pool.
	only := []*store.Pool{classPool("arm64", store.SizeMedium, 4), classPool("amd64", store.SizeLarge, 4)}
	if got := claimed(only, routedJob("j", 0, store.SizeMedium, "zoomies")); got != "zoomies-arm64-medium" {
		t.Errorf("claimed by %q; the right class comes before the default architecture", got)
	}
}

// ---------------------------------------------------------------------------
// Unserved
// ---------------------------------------------------------------------------

func TestAPoolSaysHowManyOfItsQueuedJobsHaveNoRunnerComingForThem(t *testing.T) {
	cases := []struct {
		name    string
		runners func(p *store.Pool) []*store.Runner
		max     int
		jobs    int
		want    int
	}{
		{"every runner busy and the pool at its maximum", func(p *store.Pool) []*store.Runner {
			return []*store.Runner{busy("r1", p, "host_a"), busy("r2", p, "host_a")}
		}, 2, 3, 3},
		{"an idle runner is there for the oldest job", func(p *store.Pool) []*store.Runner {
			return []*store.Runner{busy("r1", p, "host_a"), testRunner("r2", p, store.RunnerIdle, time.Minute)}
		}, 2, 3, 2},
		{"a runner still starting is there too", func(p *store.Pool) []*store.Runner {
			return []*store.Runner{busy("r1", p, "host_a"), testRunner("r2", p, store.RunnerRegistering, time.Second)}
		}, 2, 2, 1},
		{"room to create one for each job leaves none unserved", func(p *store.Pool) []*store.Runner { return nil }, 5, 3, 0},
		{"room for only some of them", func(p *store.Pool) []*store.Runner {
			return []*store.Runner{busy("r1", p, "host_a")}
		}, 3, 4, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := classPool("amd64", store.SizeMedium, tc.max)
			var jobs []*store.Job
			for i := range tc.jobs {
				jobs = append(jobs, routedJob("j"+string(rune('a'+i)), time.Duration(10-i)*time.Minute, store.SizeMedium, "zoomies"))
			}
			s := snap([]*store.Pool{sizingFor(DefaultSizeConfig())(p)}, tc.runners(p), jobs, []*store.Host{classHost("a", store.SizeMedium, 12, 32768)})
			pp := only(t, Decide(s))
			if pp.Unserved != tc.want {
				t.Fatalf("Unserved = %d, want %d (plan %+v)", pp.Unserved, tc.want, pp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Falling back
// ---------------------------------------------------------------------------

func TestAJobWhoseClassHasNoPoolIsSentOnAtOnce(t *testing.T) {
	// Medium has no host at all, so a job routed to it would never be taken
	// there. It is moved on the first pass, not after a wait.
	small, large := classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeLarge, 4)
	job := routedJob("j1", time.Second, store.SizeMedium, "zoomies")
	moves, _ := route(snap([]*store.Pool{small, large}, nil, []*store.Job{job}, []*store.Host{
		classHost("a", store.SizeSmall, 4, 16384), classHost("b", store.SizeLarge, 32, 131072),
	}))
	if len(moves) != 1 || moves[0].To != store.SizeLarge || moves[0].From != store.SizeMedium || moves[0].Job != job {
		t.Fatalf("moves = %+v; want j1 sent from medium to large", moves)
	}
	if want := "there is no medium pool (no medium host is enrolled), so it was sent to large, the next size up"; moves[0].Note != want {
		t.Errorf("note = %q, want %q", moves[0].Note, want)
	}
}

// A pool an operator made that carries a class's label is the pool of that class
// where the controller has made none -- it makes none for a class an operator's
// pool already answers to -- so a job routed to the class is not taken to have no
// pool and sent on, past the pool made for it, to a larger one.
func TestAnOperatorPoolThatCarriesTheClassLabelIsThePoolOfThatClass(t *testing.T) {
	mine := testPool("mine", "zoomies", "zoomies-medium", "linux", "x64")
	large := classPool("amd64", store.SizeLarge, 4)
	job := routedJob("j1", time.Second, store.SizeMedium, "zoomies")
	hosts := []*store.Host{classHost("a", store.SizeMedium, 12, 32768), classHost("b", store.SizeLarge, 32, 131072)}

	moves, _ := route(snap([]*store.Pool{mine, large}, nil, []*store.Job{job}, hosts))
	if len(moves) != 0 {
		t.Fatalf("moves = %+v; the operator's medium pool is a pool of the class, so nothing needs sending on", moves)
	}
	// A pool that has the label and is disabled is not, and the job is sent on.
	mine.Enabled = false
	moves, _ = route(snap([]*store.Pool{mine, large}, nil, []*store.Job{job}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeLarge {
		t.Fatalf("moves = %+v; with the operator's pool disabled there is no medium pool", moves)
	}
	// And a pool that carries no class label serves no class: it is the neutral
	// pool it was before.
	neutral := testPool("neutral", "zoomies", "zoomies-ops", "linux", "x64")
	moves, _ = route(snap([]*store.Pool{neutral, large}, nil, []*store.Job{job}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeLarge {
		t.Fatalf("moves = %+v; a pool with no class label is not a medium pool", moves)
	}
}

func TestAJobIsSentToASmallerClassOnlyWhereItStillHoldsWhatItNeeds(t *testing.T) {
	small, medium := classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeMedium, 4)
	hosts := []*store.Host{classHost("a", store.SizeSmall, 4, 16384), classHost("b", store.SizeMedium, 12, 32768)}
	pools := []*store.Pool{small, medium}

	// Large has no pool and nothing is larger. A job that needs 3 GB fits a
	// medium runner (4 GB) and not a small one (2 GB): medium it is.
	need := routedJob("j1", time.Second, store.SizeLarge, "zoomies")
	need.SizeFloorMB = 3000
	moves, _ := route(snap(pools, nil, []*store.Job{need}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeMedium || !strings.Contains(moves[0].Note, "fits there") ||
		!strings.Contains(moves[0].Note, "2.9 GB") {
		t.Fatalf("moves = %+v; want the job sent to medium, which holds it", moves)
	}

	// A job that needs more than any runner has is still sent to the nearest
	// pool, because the alternative is a job that never starts -- and the note
	// says it may be too small.
	big := routedJob("j2", time.Second, store.SizeLarge, "zoomies")
	big.SizeFloorMB = 9000
	moves, _ = route(snap(pools, nil, []*store.Job{big}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeMedium || !strings.Contains(moves[0].Note, "smaller than it is known to need") ||
		!strings.Contains(moves[0].Note, "add a large host") {
		t.Fatalf("moves = %+v; want the job sent to medium with a warning", moves)
	}

	// With no floor -- a job nobody knows the needs of -- it is the same.
	unknown := routedJob("j3", time.Second, store.SizeLarge, "zoomies")
	moves, _ = route(snap(pools, nil, []*store.Job{unknown}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeMedium {
		t.Fatalf("moves = %+v; want a job with no floor sent to the nearest pool rather than left to wait for ever", moves)
	}
}

// A class that has a pool but no room is waited on first: a runner is on its
// way, or a job is about to finish, and moving every job the moment a host is
// full would put light jobs on large hosts all day.
func TestAJobWaitsForRoomInItsOwnClassBeforeAnotherIsAllowed(t *testing.T) {
	medium, large := classPool("amd64", store.SizeMedium, 2), classPool("amd64", store.SizeLarge, 4)
	hosts := []*store.Host{classHost("a", store.SizeMedium, 12, 32768), classHost("b", store.SizeLarge, 32, 131072)}
	runners := []*store.Runner{busy("r1", medium, "host_a"), busy("r2", medium, "host_a")}

	young := routedJob("j1", time.Minute, store.SizeMedium, "zoomies")
	if moves, _ := route(snap([]*store.Pool{medium, large}, runners, []*store.Job{young}, hosts)); len(moves) != 0 {
		t.Fatalf("a job that had waited a minute was moved: %+v", moves)
	}

	old := routedJob("j1", 2*time.Minute+10*time.Second, store.SizeMedium, "zoomies")
	moves, plan := route(snap([]*store.Pool{medium, large}, runners, []*store.Job{old}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeLarge || moves[0].From != store.SizeMedium {
		t.Fatalf("moves = %+v; want j1 allowed on large", moves)
	}
	if want := "no medium host had room for it for 2m10s, so it was allowed on large"; moves[0].Note != want {
		t.Errorf("note = %q, want %q", moves[0].Note, want)
	}
	for _, pp := range plan.Pools {
		if pp.PoolName == "zoomies-medium" && pp.Unserved != 1 {
			t.Errorf("medium's plan says %d jobs unserved, want 1", pp.Unserved)
		}
	}
}

// The runners that exist or are being made go to the oldest jobs, and only the
// jobs left over are candidates, however long the others have waited.
func TestOnlyTheJobsThePoolHasNoRunnerForAreMoved(t *testing.T) {
	medium, large := classPool("amd64", store.SizeMedium, 2), classPool("amd64", store.SizeLarge, 4)
	hosts := []*store.Host{classHost("a", store.SizeMedium, 12, 32768), classHost("b", store.SizeLarge, 32, 131072)}
	runners := []*store.Runner{busy("r1", medium, "host_a"), testRunner("r2", medium, store.RunnerIdle, time.Minute)}
	runners[1].HostID = "host_a"
	jobs := []*store.Job{
		routedJob("oldest", 10*time.Minute, store.SizeMedium, "zoomies"),
		routedJob("middle", 6*time.Minute, store.SizeMedium, "zoomies"),
		routedJob("newest", 3*time.Minute, store.SizeMedium, "zoomies"),
	}
	moves, _ := route(snap([]*store.Pool{medium, large}, runners, jobs, hosts))
	// One idle runner is there for the oldest job. The two behind it have no
	// runner, and both have waited longer than the wait.
	var names []string
	for _, m := range moves {
		names = append(names, m.Job.ID)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"middle", "newest"}) {
		t.Fatalf("moved %v, want the two jobs with no runner coming and not the oldest", names)
	}
}

func TestAJobIsOnlyMovedToAClassThatHasRoomForIt(t *testing.T) {
	medium, large := classPool("amd64", store.SizeMedium, 2), classPool("amd64", store.SizeLarge, 1)
	hosts := []*store.Host{classHost("a", store.SizeMedium, 12, 32768), classHost("b", store.SizeLarge, 32, 131072)}
	runners := []*store.Runner{busy("r1", medium, "host_a"), busy("r2", medium, "host_a")}
	jobs := []*store.Job{
		routedJob("a", 5*time.Minute, store.SizeMedium, "zoomies"),
		routedJob("b", 4*time.Minute, store.SizeMedium, "zoomies"),
		routedJob("c", 3*time.Minute, store.SizeMedium, "zoomies"),
	}
	moves, _ := route(snap([]*store.Pool{medium, large}, runners, jobs, hosts))
	// Large can start one runner. Three jobs have no runner at medium, and only
	// one of them can go: the next two stay where they are rather than queueing
	// at a pool that cannot start them either.
	if len(moves) != 1 || moves[0].Job.ID != "a" || moves[0].To != store.SizeLarge {
		t.Fatalf("moves = %+v; want only the oldest job sent to large, which has room for one", moves)
	}

	full := classPool("amd64", store.SizeLarge, 1)
	runners = append(runners, busy("r3", full, "host_b"))
	if moves, _ := route(snap([]*store.Pool{medium, full}, runners, jobs, hosts)); len(moves) != 0 {
		t.Fatalf("with every class full a job was moved: %+v", moves)
	}
}

// A class can have a pool for each architecture, and a job that names no
// architecture is claimed by the amd64 one whatever room either has. Room is
// therefore the amd64 pool's to have: a job is not sent to a class on the
// strength of an arm64 pool's room that the claim will never give it.
func TestAJobIsOnlyMovedToAClassWhosePoolTheClaimWouldGiveItHasRoomFor(t *testing.T) {
	medium := classPool("amd64", store.SizeMedium, 2)
	armLarge, large := classPool("arm64", store.SizeLarge, 4), classPool("amd64", store.SizeLarge, 1)
	armHost := classHost("arm", store.SizeLarge, 32, 131072)
	armHost.Arch = "arm64"
	hosts := []*store.Host{classHost("a", store.SizeMedium, 12, 32768), classHost("b", store.SizeLarge, 32, 131072), armHost}
	pools := []*store.Pool{medium, armLarge, large}
	job := routedJob("j", 5*time.Minute, store.SizeMedium, "zoomies")

	// The claim for a large job picks the amd64 pool, which is full; the arm64
	// pool has room that the job would never be given.
	if got := claimed([]*store.Pool{armLarge, large}, routedJob("c", 0, store.SizeLarge, "zoomies")); got != "zoomies-large" {
		t.Fatalf("the claim for a large job is %q; this test is about the amd64 pool winning it", got)
	}
	runners := []*store.Runner{busy("r1", medium, "host_a"), busy("r2", medium, "host_a"), busy("r3", large, "host_b")}
	if moves, _ := route(snap(pools, runners, []*store.Job{job}, hosts)); len(moves) != 0 {
		t.Fatalf("a job was sent to large because its arm64 pool has room, though the claim gives it the full amd64 pool: %+v", moves)
	}

	// With room at the amd64 pool the job is sent, and the room is counted
	// against that pool, so a second job is not sent to the one runner.
	runners = []*store.Runner{busy("r1", medium, "host_a"), busy("r2", medium, "host_a")}
	second := routedJob("k", 4*time.Minute, store.SizeMedium, "zoomies")
	moves, _ := route(snap(pools, runners, []*store.Job{job, second}, hosts))
	if len(moves) != 1 || moves[0].Job != job || moves[0].To != store.SizeLarge {
		t.Fatalf("moves = %+v; want only the older job sent to large, which has room for one runner at the pool it would land in", moves)
	}
}

// A class whose hosts cannot take a runner -- every one cordoned, say -- shows
// room on paper, because its maximum is derived from them. The plan says it
// could not place what it was asked for, and that is what a job is not sent to.
func TestAJobIsNotSentToAClassWhoseHostsCannotTakeAnotherRunner(t *testing.T) {
	medium, large := classPool("amd64", store.SizeMedium, 1), classPool("amd64", store.SizeLarge, 4)
	cordonedHost := classHost("c", store.SizeLarge, 32, 131072)
	cordonedHost.Cordoned = true
	hosts := []*store.Host{classHost("b", store.SizeMedium, 12, 32768), cordonedHost}
	runners := []*store.Runner{busy("r1", medium, "host_b")}
	jobs := []*store.Job{
		routedJob("waiting-medium", 5*time.Minute, store.SizeMedium, "zoomies"),
		routedJob("waiting-large", time.Second, store.SizeLarge, "zoomies"),
	}
	moves, plan := route(snap([]*store.Pool{medium, large}, runners, jobs, hosts))
	for _, pp := range plan.Pools {
		if pp.PoolName == "zoomies-large" && pp.Blocked == "" && !pp.BlockedNoEligibleHost {
			t.Fatalf("the large pool was not blocked by its only host being cordoned: %+v", pp)
		}
	}
	if len(moves) != 0 {
		t.Fatalf("a job was sent to a class that cannot start a runner: %+v", moves)
	}
}

// A job goes to the first class in its order that has room: its own, then
// larger ones, then smaller ones that hold what it needs. It is only ever moved
// while it has no runner coming, so a job that was sent somewhere and is being
// served is not passed back.
func TestAJobGoesToTheFirstClassInItsOrderThatHasRoom(t *testing.T) {
	small, medium, large := classPool("amd64", store.SizeSmall, 4), classPool("amd64", store.SizeMedium, 1), classPool("amd64", store.SizeLarge, 4)
	hosts := []*store.Host{classHost("a", store.SizeSmall, 4, 16384), classHost("b", store.SizeMedium, 12, 32768), classHost("c", store.SizeLarge, 32, 131072)}
	pools := []*store.Pool{small, medium, large}

	// Medium is full: larger comes before smaller.
	runners := []*store.Runner{busy("r1", medium, "host_b")}
	j := routedJob("j", 5*time.Minute, store.SizeMedium, "zoomies")
	j.SizeFloorMB = 1000
	moves, _ := route(snap(pools, runners, []*store.Job{j}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeLarge {
		t.Fatalf("moves = %+v; want the job sent to large before small", moves)
	}

	// Sent to large and large is full too, while medium has room again: it goes
	// back to its own class, and says so.
	medium.MaxRunners = 4
	large.MaxRunners = 1
	runners = []*store.Runner{busy("r1", medium, "host_b"), busy("r2", large, "host_c")}
	j = routedJob("j", 5*time.Minute, store.SizeMedium, "zoomies")
	j.RoutedClass = store.SizeLarge
	moves, _ = route(snap(pools, runners, []*store.Job{j}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeMedium || !strings.Contains(moves[0].Note, "went back to medium, its own class") {
		t.Fatalf("moves = %+v; want the job sent back to medium", moves)
	}

	// Medium and large both full, small free: small only if the job's known
	// need fits there.
	medium.MaxRunners, large.MaxRunners = 1, 1
	runners = []*store.Runner{busy("r1", medium, "host_b"), busy("r2", large, "host_c")}
	j = routedJob("j", 5*time.Minute, store.SizeMedium, "zoomies")
	if moves, _ := route(snap(pools, runners, []*store.Job{j}, hosts)); len(moves) != 0 {
		t.Fatalf("a job with no known floor was offered a smaller class: %+v", moves)
	}
	j.SizeFloorMB = 1000
	moves, _ = route(snap(pools, runners, []*store.Job{j}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeSmall || !strings.Contains(moves[0].Note, "still holds the 1000 MB it needs") {
		t.Fatalf("moves = %+v; want the job sent to small, which holds it", moves)
	}
	j.SizeFloorMB = 3000
	if moves, _ := route(snap(pools, runners, []*store.Job{j}, hosts)); len(moves) != 0 {
		t.Fatalf("a job that needs more than a small runner has was sent there: %+v", moves)
	}

	// A job being served is not moved, whatever has room.
	served := routedJob("served", 5*time.Minute, store.SizeMedium, "zoomies")
	served.RoutedClass = store.SizeLarge
	idle := testRunner("r3", large, store.RunnerIdle, time.Minute)
	idle.HostID = "host_c"
	if moves, _ := route(snap(pools, append(runners, idle), []*store.Job{served}, hosts)); len(moves) != 0 {
		t.Fatalf("a job with a runner waiting for it was moved: %+v", moves)
	}
}

// What an author asked for by name, and what nobody has classed, are never moved.
func TestSomeJobsAreNeverMoved(t *testing.T) {
	medium, large := classPool("amd64", store.SizeMedium, 1), classPool("amd64", store.SizeLarge, 4)
	hosts := []*store.Host{classHost("a", store.SizeMedium, 12, 32768), classHost("b", store.SizeLarge, 32, 131072)}
	runners := []*store.Runner{busy("r1", medium, "host_a")}

	explicit := routedJob("explicit", 10*time.Minute, store.SizeMedium, "zoomies", "zoomies-medium")
	explicit.SizeBasis = store.SizeBasisExplicit
	unrouted := routedJob("unrouted", 10*time.Minute, store.SizeMedium, "zoomies")
	unrouted.RoutedClass = ""
	unclassed := queued("unclassed", 10*time.Minute, "zoomies")
	running := routedJob("running", 10*time.Minute, store.SizeMedium, "zoomies")
	running.State = store.JobInProgress
	held := routedJob("held", 10*time.Minute, store.SizeMedium, "zoomies")
	held.Provisioning = "paused"

	moves, _ := route(snap([]*store.Pool{medium, large}, runners, []*store.Job{explicit, unrouted, unclassed, running, held}, hosts))
	if len(moves) != 0 {
		t.Fatalf("jobs that must stay were moved: %+v", moves)
	}

	// An explicit job whose class has no pool waits there: it asked for it.
	only := routedJob("only", 10*time.Minute, store.SizeLarge, "zoomies", "zoomies-large")
	only.SizeBasis = store.SizeBasisExplicit
	if moves, _ := route(snap([]*store.Pool{medium}, nil, []*store.Job{only}, hosts)); len(moves) != 0 {
		t.Fatalf("an explicit job with no pool in its class was moved: %+v", moves)
	}
}

// A pool the operator has paused, or whose last host has gone, is a class with
// no pool.
func TestADisabledPoolIsAClassWithNoPool(t *testing.T) {
	medium, large := classPool("amd64", store.SizeMedium, 4), classPool("amd64", store.SizeLarge, 4)
	medium.Enabled = false
	hosts := []*store.Host{classHost("b", store.SizeLarge, 32, 131072)}
	moves, _ := route(snap([]*store.Pool{medium, large}, nil, []*store.Job{routedJob("j", time.Second, store.SizeMedium, "zoomies")}, hosts))
	if len(moves) != 1 || moves[0].To != store.SizeLarge {
		t.Fatalf("moves = %+v; want the job sent to large", moves)
	}
}

func TestClassOrderOffersLargerClassesBeforeSmallerOnes(t *testing.T) {
	cases := map[store.SizeClass][]store.SizeClass{
		store.SizeSmall:  {store.SizeSmall, store.SizeMedium, store.SizeLarge},
		store.SizeMedium: {store.SizeMedium, store.SizeLarge, store.SizeSmall},
		store.SizeLarge:  {store.SizeLarge, store.SizeMedium, store.SizeSmall},
	}
	for class, want := range cases {
		if got := classOrder(class); !slices.Equal(got, want) {
			t.Errorf("classOrder(%s) = %v, want %v", class, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// At the door
// ---------------------------------------------------------------------------

func TestAJobJoinsTheQueueInTheClassItsLabelItsPinOrItsHistoryGives(t *testing.T) {
	cfg := DefaultSizeConfig()
	kept := &store.JobClass{Class: store.SizeLarge, Basis: store.SizeBasisHistory, Reason: "its memory needs about 7 GB", FloorMB: 7168, Runs: 12}
	pin := &store.SizePin{Repo: "acme/api", Class: store.SizeSmall}

	got := cfg.AtQueue([]string{"zoomies", "zoomies-medium"}, pin, kept)
	if got.Class != store.SizeMedium || got.Basis != store.SizeBasisExplicit {
		t.Errorf("a size label = %+v", got)
	}
	got = cfg.AtQueue([]string{"zoomies"}, pin, kept)
	if got.Class != store.SizeSmall || got.Basis != store.SizeBasisPin {
		t.Errorf("a pin = %+v", got)
	}
	got = cfg.AtQueue([]string{"zoomies"}, nil, kept)
	if got.Class != store.SizeLarge || got.Basis != store.SizeBasisHistory || got.Reason != kept.Reason || got.FloorMB != 7168 || got.Runs != 12 {
		t.Errorf("a kept class = %+v; it should arrive with the reason and the floor it was kept with", got)
	}
	got = cfg.AtQueue([]string{"zoomies"}, nil, nil)
	if got.Class != store.SizeMedium || got.Basis != store.SizeBasisDefault {
		t.Errorf("a job with nothing known = %+v", got)
	}
	// A kept class with no basis recorded is history: it can only have come from there.
	got = cfg.AtQueue(nil, nil, &store.JobClass{Class: store.SizeSmall})
	if got.Basis != store.SizeBasisHistory {
		t.Errorf("a kept class with no basis = %+v", got)
	}
}
