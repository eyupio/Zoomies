package controller

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// machineNow is the one instant every test in this file decides at. The
// calculation reads no clock, so a fixed time is all that is needed to place a
// machine exactly one second either side of a timeout.
var machineNow = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func demandPool(name string, labels ...string) *store.Pool {
	return &store.Pool{
		ID: "pool_" + name, Name: name, Enabled: true,
		InstallationID: "inst_1", Backend: store.BackendDocker,
		Labels: labels, MaxRunners: 100,
	}
}

func demandProvider(name string) *store.Provider {
	return &store.Provider{
		ID: "prv_" + name, Name: name, Kind: store.ProviderFake, Enabled: true,
		MachineBackend: store.BackendDocker, MachineCapacity: 2,
		MaxMachines: 10, MaxCreatesInFlight: 10,
		IdleTimeout: store.Duration(15 * time.Minute),
	}
}

func demandHost(id string, capacity, active int) *store.Host {
	return &store.Host{
		ID: id, Name: id, Capacity: capacity, ActiveRunners: active,
		Backends: store.StringSlice{string(store.BackendDocker)}, LastHeartbeat: machineNow,
	}
}

// demandMachine is a machine of p in one state. A machine that has got as far
// as owning a resource carries one, because Owns() -- not the state -- is what
// every ceiling is counted over.
func demandMachine(id string, p *store.Provider, state store.MachineState, age time.Duration) *store.Machine {
	m := &store.Machine{
		ID: id, ProviderID: p.ID, Name: id, State: state,
		CreatedAt: machineNow.Add(-age),
	}
	if state != store.MachinePlanned {
		m.ResourceID, m.ResourceZone = "vm-"+id, "node-1"
	}
	return m
}

// demandPoolPlan is the scheduler's reading of one pool, which is where demand
// comes from. blocked is "nowhere" for a pool no host can run, "capacity" for
// one whose hosts are all full, and "" for a pool that is not blocked at all.
func demandPoolPlan(p *store.Pool, current, desired, queued int, blocked string) scheduler.PoolPlan {
	return scheduler.PoolPlan{
		PoolID: p.ID, PoolName: p.Name, Current: current, Desired: desired, QueuedMatched: queued,
		BlockedAtCapacity:     blocked == "capacity",
		BlockedNoEligibleHost: blocked == "nowhere",
	}
}

// demandJobs are queued jobs any pool in this file claims: the labels are the
// ones nearly every workflow is written with, so a job's pool is decided by
// scheduler.BestPool rather than by the test.
func demandJobs(n int) []*store.Job {
	out := make([]*store.Job, 0, n)
	for i := range n {
		out = append(out, &store.Job{
			ID: "job_" + string(rune('a'+i)), Repo: "acme/widgets", State: store.JobQueued,
			InstallationID: "inst_1", Labels: store.StringSlice{"self-hosted", "linux", "x64"},
			QueuedAt: machineNow.Add(-5 * time.Minute),
		})
	}
	return out
}

func demandLimits() MachineLimits {
	return MachineLimits{
		MaxMachines: 10, MaxCreatesInFlight: 10,
		IdleTimeout: 15 * time.Minute, ScaleDownCooldown: 10 * time.Minute,
	}
}

// planFor is the one provider's decision, so a test says what it means rather
// than indexing a slice.
func planFor(t *testing.T, plan MachinePlan, providerID string) ProviderPlan {
	t.Helper()
	for _, pp := range plan.Providers {
		if pp.ProviderID == providerID {
			return pp
		}
	}
	t.Fatalf("no plan for provider %s in %+v", providerID, plan.Providers)
	return ProviderPlan{}
}

// Two pools short one runner each are two slots, and a machine offering two
// slots answers both. Rounding each pool up to its own machine would buy two
// machines for work one of them can do, which is the bill an operator notices.
func TestTwoPoolsWantingTheSameMachineShapeBuyOne(t *testing.T) {
	p := demandProvider("lab")
	alpha, beta := demandPool("alpha"), demandPool("beta")
	s := MachineSnapshot{
		Now:       machineNow,
		Pools:     []*store.Pool{alpha, beta},
		Providers: []*store.Provider{p},
		Limits:    demandLimits(),
		Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{
			demandPoolPlan(alpha, 0, 1, 1, "nowhere"),
			demandPoolPlan(beta, 0, 1, 1, "nowhere"),
		}},
	}

	got := planFor(t, DecideMachines(s), p.ID)
	if got.Create != 1 || got.Wanted != 1 || got.Have != 0 {
		t.Fatalf("create/wanted/have = %d/%d/%d, want 1/1/0", got.Create, got.Wanted, got.Have)
	}
	want := "buy 1 machine on lab: pools alpha and beta are 2 runner slots short between them, and no host in the fleet can run them"
	if got.Reason != want {
		t.Fatalf("reason = %q, want %q", got.Reason, want)
	}
}

// A machine already on its way is capacity, and it is capacity once. Crediting
// it to every pool its labels match would satisfy three pools with one machine
// on paper and leave two of them queueing for ever.
func TestOverlappingPoolsNeverCreditOneMachineTwice(t *testing.T) {
	p := demandProvider("lab")
	alpha, beta := demandPool("alpha"), demandPool("beta")
	onTheWay := demandMachine("mach_1", p, store.MachinePlanned, time.Minute)
	s := MachineSnapshot{
		Now:       machineNow,
		Pools:     []*store.Pool{alpha, beta},
		Providers: []*store.Provider{p},
		Machines:  []*store.Machine{onTheWay},
		Limits:    demandLimits(),
		Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{
			demandPoolPlan(alpha, 0, 2, 2, "nowhere"),
			demandPoolPlan(beta, 0, 2, 2, "nowhere"),
		}},
	}

	got := planFor(t, DecideMachines(s), p.ID)
	// The machine covers alpha entirely; beta is still two slots short, which
	// is one more machine and not two.
	if got.Create != 1 {
		t.Fatalf("create = %d, want 1: the pending machine answers one pool, not both", got.Create)
	}
	if got.Have != 1 || got.Wanted != 2 {
		t.Fatalf("have/wanted = %d/%d, want 1/2", got.Have, got.Wanted)
	}
	if !strings.Contains(got.Reason, "pool beta is 2 runner slots short") {
		t.Fatalf("reason = %q, want it to name the pool the machine could not cover", got.Reason)
	}
}

// A ready machine is already a host row, already counted in the scheduler's
// Current and already in the free slots this calculation subtracts. Counting
// its capacity here as well is the double count, and a fleet that did it would
// refuse to buy the machine the queue is actually waiting for.
func TestReadyMachinesAreNotCountedTwice(t *testing.T) {
	p := demandProvider("lab")
	pool := demandPool("alpha")
	host := demandHost("host_1", 2, 2) // full: both slots are running jobs
	ready := demandMachine("mach_1", p, store.MachineReady, time.Hour)
	ready.HostID = host.ID
	s := MachineSnapshot{
		Now:       machineNow,
		Pools:     []*store.Pool{pool},
		Hosts:     []*store.Host{host},
		Providers: []*store.Provider{p},
		Machines:  []*store.Machine{ready},
		Limits:    demandLimits(),
		Plan:      scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 2, 4, 2, "capacity")}},
	}

	got := planFor(t, DecideMachines(s), p.ID)
	if got.Create != 1 {
		t.Fatalf("create = %d, want 1: a ready machine's slots are the host's, and they are busy", got.Create)
	}
	if got.Have != 1 || got.Wanted != 2 {
		t.Fatalf("have/wanted = %d/%d, want 1/2", got.Have, got.Wanted)
	}
}

// BlockedNoEligibleHost is a hint that a machine might help, never an
// instruction to buy one: a pool blocked because nothing matches its selector
// is not served by a provider whose machines would not match it either, and
// buying one would spend money and leave the queue exactly where it was.
func TestNoMachineIsBoughtForAPoolItsShapeCouldNotServe(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pool       func(*store.Pool)
		provider   func(*store.Provider)
		wantCreate int
	}{
		{name: "nothing in the way", pool: func(*store.Pool) {}, provider: func(*store.Provider) {}, wantCreate: 1},
		{
			name:     "the backend the pool needs",
			pool:     func(p *store.Pool) { p.Backend = store.BackendPodman },
			provider: func(*store.Provider) {},
		},
		{
			name:     "a host selector the machine does not answer",
			pool:     func(p *store.Pool) { p.HostSelector = store.StringMap{"zone": "b"} },
			provider: func(p *store.Provider) { p.MachineLabels = store.StringMap{"zone": "a"} },
		},
		{
			name:     "the platform the pool asked for",
			pool:     func(p *store.Pool) { p.Platform = store.Platform{OS: "windows"} },
			provider: func(p *store.Provider) { p.MachinePlatform = store.Platform{OS: "ubuntu", OSVersion: "24.04"} },
		},
		{
			name:     "a machine too small for the pool's limits",
			pool:     func(p *store.Pool) { p.Resources = store.Resources{CPUs: 8} },
			provider: func(p *store.Provider) { p.MachineCPUs = 4 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := demandProvider("lab")
			tc.provider(p)
			pool := demandPool("alpha")
			tc.pool(pool)
			s := MachineSnapshot{
				Now: machineNow, Pools: []*store.Pool{pool}, Providers: []*store.Provider{p},
				Limits: demandLimits(),
				Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 2, 2, "nowhere")}},
			}

			got := planFor(t, DecideMachines(s), p.ID)
			if got.Create != tc.wantCreate {
				t.Fatalf("create = %d, want %d", got.Create, tc.wantCreate)
			}
			if tc.wantCreate == 0 && (got.Reason != "" || got.Blocked != "") {
				t.Fatalf("reason %q / blocked %q, want both empty: this is not a shortfall money can fix", got.Reason, got.Blocked)
			}
		})
	}
}

// Scale from zero is the whole point of a provider: a fleet with no host at all
// emits no capacity signal a finished job could ever clear, so the number of
// machines bought has to come out of the queue itself -- and be exactly enough
// for it, rounded up to whole machines.
func TestScaleFromZeroBuysExactlyTheMachinesTheQueueNeeds(t *testing.T) {
	p := demandProvider("lab")
	pool := demandPool("linux-x64", "linux", "x64")
	s := MachineSnapshot{
		Now: machineNow, Pools: []*store.Pool{pool}, Providers: []*store.Provider{p},
		Jobs: demandJobs(5), Limits: demandLimits(),
		Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 5, 5, "nowhere")}},
	}

	got := planFor(t, DecideMachines(s), p.ID)
	if got.Create != 3 || got.Wanted != 3 || got.Have != 0 {
		t.Fatalf("create/wanted/have = %d/%d/%d, want 3/3/0 for 5 slots on 2-slot machines", got.Create, got.Wanted, got.Have)
	}
	want := "buy 3 machines on lab: pool linux-x64 is 5 runner slots short with 5 jobs queued, and no host in the fleet can run it"
	if got.Reason != want {
		t.Fatalf("reason = %q, want %q", got.Reason, want)
	}
}

// The number of machines a provider should have is a reading, not an
// increment. A pass that added its answer to what the last pass asked for would
// buy the same shortfall again every time it ran, which is how a queue of five
// jobs ends up costing fifty machines.
func TestAMachineTargetIsAReadingNotAnIncrement(t *testing.T) {
	p := demandProvider("lab")
	pool := demandPool("linux-x64", "linux", "x64")
	s := MachineSnapshot{
		Now: machineNow, Pools: []*store.Pool{pool}, Providers: []*store.Provider{p},
		Jobs: demandJobs(5), Limits: demandLimits(),
		Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 5, 5, "nowhere")}},
	}
	first := planFor(t, DecideMachines(s), p.ID)
	if first.Create != 3 {
		t.Fatalf("first pass create = %d, want 3", first.Create)
	}

	// The pass acted: three machines are on their way and nothing else moved.
	for _, id := range []string{"mach_1", "mach_2", "mach_3"} {
		s.Machines = append(s.Machines, demandMachine(id, p, store.MachinePlanned, time.Minute))
	}
	for range 2 {
		got := planFor(t, DecideMachines(s), p.ID)
		if got.Create != 0 {
			t.Fatalf("create = %d, want 0: the machines already on their way are the answer", got.Create)
		}
		if got.Wanted != 3 || got.Have != 3 {
			t.Fatalf("wanted/have = %d/%d, want 3/3", got.Wanted, got.Have)
		}
	}
}

// The same snapshot always yields the same plan, whatever order its rows
// arrived in. A decision that moved with a map walk or a database's ordering
// would buy a machine on one pass and release it on the next.
func TestDecideMachinesIsDeterministic(t *testing.T) {
	build := func(reversed bool) MachineSnapshot {
		lab, spare := demandProvider("lab"), demandProvider("spare")
		spare.MaxMachines = 0 // may own nothing, so demand walks past it
		alpha, beta := demandPool("alpha"), demandPool("beta")
		host := demandHost("host_1", 2, 2)
		ready := demandMachine("mach_ready", lab, store.MachineReady, 3*time.Hour)
		ready.HostID = host.ID
		ready.IdleSince = ptrTime(machineNow.Add(-2 * time.Hour))
		planned := demandMachine("mach_planned", lab, store.MachinePlanned, time.Minute)
		s := MachineSnapshot{
			Now:       machineNow,
			Pools:     []*store.Pool{alpha, beta},
			Hosts:     []*store.Host{host},
			Providers: []*store.Provider{lab, spare},
			Machines:  []*store.Machine{ready, planned},
			Jobs:      demandJobs(3),
			Limits:    demandLimits(),
			Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{
				demandPoolPlan(alpha, 2, 6, 3, "capacity"),
				demandPoolPlan(beta, 0, 2, 2, "nowhere"),
			}},
		}
		if reversed {
			reverse(s.Pools)
			reverse(s.Providers)
			reverse(s.Machines)
			reverse(s.Plan.Pools)
		}
		return s
	}

	want := DecideMachines(build(false))
	for i := range 5 {
		if got := DecideMachines(build(i%2 == 1)); !reflect.DeepEqual(got, want) {
			t.Fatalf("pass %d = %+v, want %+v", i, got.Providers, want.Providers)
		}
	}
}

// The calculation is handed the controller's own live rows, so anything it
// wrote through them -- or any slice it sorted in place -- would be a change to
// the fleet's state made by a function that only claims to decide.
func TestDecideMachinesDoesNotMutateItsInput(t *testing.T) {
	lab := demandProvider("lab")
	// Every slice is handed over in an order the calculation has to sort, so an
	// in-place sort shows up as a changed snapshot rather than as nothing.
	alpha, beta := demandPool("alpha"), demandPool("beta")
	host := demandHost("host_2", 2, 0)
	ready := demandMachine("mach_ready", lab, store.MachineReady, 3*time.Hour)
	ready.HostID = host.ID
	ready.IdleSince = ptrTime(machineNow.Add(-2 * time.Hour))
	planned := demandMachine("mach_planned", lab, store.MachinePlanned, time.Minute)
	s := MachineSnapshot{
		Now:       machineNow,
		Pools:     []*store.Pool{beta, alpha},
		Hosts:     []*store.Host{host},
		Providers: []*store.Provider{lab},
		Machines:  []*store.Machine{planned, ready},
		Runners:   map[string][]*store.Runner{alpha.ID: {}},
		Jobs:      demandJobs(2),
		Limits:    demandLimits(),
		Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{
			demandPoolPlan(alpha, 0, 3, 2, "nowhere"),
			demandPoolPlan(beta, 0, 0, 0, ""),
		}},
	}
	before := cloneSnapshot(s)

	DecideMachines(s)

	if !reflect.DeepEqual(s, before) {
		t.Fatalf("the snapshot changed:\n got %+v\nwant %+v", s, before)
	}
}

// Idleness has to be sustained before it is acted on. A machine released
// between two bursts is one the second burst pays to build again -- minutes of
// queueing for a hypervisor clone -- so the idle timeout says when a machine
// stopped being useful and the cooldown is how long that has to keep being
// true.
func TestScaleDownNeedsTheObservationToHoldForOneCooldown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		idleFor   time.Duration
		idleNever bool
		busy      bool
		wantDrain bool
	}{
		{name: "never idle", idleNever: true},
		{name: "idle, but not yet for the idle timeout", idleFor: 5 * time.Minute},
		{name: "past the idle timeout, still inside the cooldown", idleFor: 20 * time.Minute},
		{name: "idle past the timeout and the cooldown", idleFor: 30 * time.Minute, wantDrain: true},
		{name: "idle long enough, but a runner landed on it", idleFor: 30 * time.Minute, busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := demandProvider("lab")
			pool := demandPool("alpha")
			host := demandHost("host_1", 2, 0)
			m := demandMachine("mach_1", p, store.MachineReady, 4*time.Hour)
			m.HostID = host.ID
			if !tc.idleNever {
				m.IdleSince = ptrTime(machineNow.Add(-tc.idleFor))
			}
			runners := map[string][]*store.Runner{}
			if tc.busy {
				host.ActiveRunners = 1
				runners[pool.ID] = []*store.Runner{{ID: "run_1", PoolID: pool.ID, HostID: host.ID, State: store.RunnerBusy}}
			}
			s := MachineSnapshot{
				Now: machineNow, Pools: []*store.Pool{pool}, Hosts: []*store.Host{host},
				Providers: []*store.Provider{p}, Machines: []*store.Machine{m}, Runners: runners,
				Limits: demandLimits(),
				Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 0, 0, "")}},
			}

			got := planFor(t, DecideMachines(s), p.ID)
			if drained := len(got.Drain) == 1; drained != tc.wantDrain {
				t.Fatalf("drain = %v, want drained = %v", got.Drain, tc.wantDrain)
			}
		})
	}
}

// A machine whose slots a pool is still waiting for is not idle capacity,
// however long nothing has run on it: releasing it would reopen the shortfall
// the next pass then buys a machine to close.
func TestADrainIsRefusedWhileAPoolItServesIsStillShort(t *testing.T) {
	p := demandProvider("lab")
	pool := demandPool("alpha")
	host := demandHost("host_1", 2, 0)
	m := demandMachine("mach_1", p, store.MachineReady, 4*time.Hour)
	m.HostID = host.ID
	m.IdleSince = ptrTime(machineNow.Add(-2 * time.Hour))
	s := MachineSnapshot{
		Now: machineNow, Pools: []*store.Pool{pool}, Hosts: []*store.Host{host},
		Providers: []*store.Provider{p}, Machines: []*store.Machine{m},
		Limits: demandLimits(),
		Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 1, 3, 2, "capacity")}},
	}

	if got := planFor(t, DecideMachines(s), p.ID); len(got.Drain) != 0 {
		t.Fatalf("drain = %v, want nothing released while the pool is still short", got.Drain)
	}
}

// Newest first, so the machine with the least warmed cache is the one thrown
// away, and the order is fixed so two passes over one snapshot never disagree
// about which machine goes.
func TestTheNewestIdleMachineIsDrainedFirst(t *testing.T) {
	p := demandProvider("lab")
	pool := demandPool("alpha")
	older, newer := demandHost("host_old", 2, 0), demandHost("host_new", 2, 0)
	first := demandMachine("mach_older", p, store.MachineReady, 6*time.Hour)
	first.HostID, first.IdleSince = older.ID, ptrTime(machineNow.Add(-2*time.Hour))
	second := demandMachine("mach_newer", p, store.MachineReady, time.Hour)
	second.HostID, second.IdleSince = newer.ID, ptrTime(machineNow.Add(-2*time.Hour))
	s := MachineSnapshot{
		Now: machineNow, Pools: []*store.Pool{pool}, Hosts: []*store.Host{older, newer},
		Providers: []*store.Provider{p}, Machines: []*store.Machine{first, second},
		Limits: demandLimits(),
		Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 0, 0, "")}},
	}

	got := planFor(t, DecideMachines(s), p.ID)
	if want := []string{"mach_newer", "mach_older"}; !reflect.DeepEqual(got.Drain, want) {
		t.Fatalf("drain = %v, want %v", got.Drain, want)
	}
}

// A maximum of none is none, for a provider and for the fleet alike -- the same
// rule a pool's max_runners of zero obeys. Zero meaning "unlimited" would put
// the number that decides the size of an invoice behind a value somebody can
// leave unset.
func TestACeilingOfNoMachinesBuysNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   func(*MachineLimits)
		provider func(*store.Provider)
		want     string
		wantFix  string
	}{
		{
			name:     "the provider may own none",
			limits:   func(*MachineLimits) {},
			provider: func(p *store.Provider) { p.MaxMachines = 0 },
			want:     "3 machines not bought: lab may own no machines at all",
			wantFix:  "set this provider's maximum machines to the number of machines you are willing to pay for",
		},
		{
			name:     "the fleet may own none",
			limits:   func(l *MachineLimits) { l.MaxMachines = 0 },
			provider: func(*store.Provider) {},
			want:     "3 machines not bought: provider.max_machines is 0, so this fleet rents nothing",
			wantFix:  "set provider.max_machines to the number of machines this fleet may pay for",
		},
		{
			name:     "the provider may build none",
			limits:   func(*MachineLimits) {},
			provider: func(p *store.Provider) { p.MaxCreatesInFlight = 0 },
			want:     "3 machines not bought: lab may build no machine at a time",
			wantFix:  "raise this provider's maximum creates in flight above 0",
		},
		{
			name:     "the fleet may build none",
			limits:   func(l *MachineLimits) { l.MaxCreatesInFlight = 0 },
			provider: func(*store.Provider) {},
			want:     "3 machines not bought: provider.max_creates_in_flight is 0, so nothing may be built",
			wantFix:  "set provider.max_creates_in_flight to the number of machines that may be built at once",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := demandProvider("lab")
			tc.provider(p)
			limits := demandLimits()
			tc.limits(&limits)
			pool := demandPool("linux-x64", "linux", "x64")
			s := MachineSnapshot{
				Now: machineNow, Pools: []*store.Pool{pool}, Providers: []*store.Provider{p},
				Jobs: demandJobs(5), Limits: limits,
				Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 5, 5, "nowhere")}},
			}

			got := planFor(t, DecideMachines(s), p.ID)
			if got.Create != 0 || got.Reason != "" {
				t.Fatalf("create %d reason %q, want nothing bought", got.Create, got.Reason)
			}
			if got.Blocked != tc.want || got.BlockedFix != tc.wantFix {
				t.Fatalf("blocked %q / fix %q,\nwant %q / %q", got.Blocked, got.BlockedFix, tc.want, tc.wantFix)
			}
		})
	}
}

// An operator sent to raise the limit that was not the one holding them back
// has been sent to the wrong screen, so the sentence names whichever ceiling
// actually bound -- and the ones that did not bite say nothing.
func TestTheCeilingThatBindsIsTheOneNamed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limits     func(*MachineLimits)
		provider   func(*store.Provider)
		owned      int
		wantCreate int
		wantBlock  string
	}{
		{
			name:       "the provider is nearly full",
			limits:     func(*MachineLimits) {},
			provider:   func(p *store.Provider) { p.MaxMachines = 3 },
			owned:      2,
			wantCreate: 1,
			wantBlock:  "2 machines not bought: lab owns 2 machines and its maximum is 3",
		},
		{
			name:       "the fleet's ceiling is the lower one",
			limits:     func(l *MachineLimits) { l.MaxMachines = 2 },
			provider:   func(*store.Provider) {},
			wantCreate: 2,
			wantBlock:  "1 machine not bought: provider.max_machines allows only 2 machines across the fleet",
		},
		{
			name:       "the provider may only build one at a time",
			limits:     func(*MachineLimits) {},
			provider:   func(p *store.Provider) { p.MaxCreatesInFlight = 1 },
			wantCreate: 1,
			wantBlock:  "2 machines not bought: lab may build only 1 machine at a time",
		},
		{
			name:       "nothing binds",
			limits:     func(*MachineLimits) {},
			provider:   func(*store.Provider) {},
			wantCreate: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := demandProvider("lab")
			tc.provider(p)
			limits := demandLimits()
			tc.limits(&limits)
			pool := demandPool("linux-x64", "linux", "x64")
			s := MachineSnapshot{
				Now: machineNow, Pools: []*store.Pool{pool}, Providers: []*store.Provider{p},
				Jobs: demandJobs(5), Limits: limits,
				Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 5, 5, "nowhere")}},
			}
			// Machines that already own a resource are what a maximum counts.
			for i := range tc.owned {
				s.Machines = append(s.Machines, demandMachine("mach_own_"+string(rune('a'+i)), p, store.MachineFailed, time.Hour))
			}

			got := planFor(t, DecideMachines(s), p.ID)
			if got.Create != tc.wantCreate {
				t.Fatalf("create = %d, want %d", got.Create, tc.wantCreate)
			}
			if got.Blocked != tc.wantBlock {
				t.Fatalf("blocked = %q, want %q", got.Blocked, tc.wantBlock)
			}
			if tc.wantBlock != "" && got.BlockedFix == "" {
				t.Fatal("blocked with no fix: a refusal an operator cannot act on")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ptrTime(t time.Time) *time.Time { return &t }

func reverse[T any](in []T) {
	for i, j := 0, len(in)-1; i < j; i, j = i+1, j-1 {
		in[i], in[j] = in[j], in[i]
	}
}

// cloneSnapshot copies a snapshot deeply enough that reflect.DeepEqual against
// it catches both a field written through a pointer and a slice sorted in
// place.
func cloneSnapshot(s MachineSnapshot) MachineSnapshot {
	out := s
	out.Pools = clonePointers(s.Pools)
	out.Hosts = clonePointers(s.Hosts)
	out.Machines = clonePointers(s.Machines)
	out.Providers = clonePointers(s.Providers)
	out.Jobs = clonePointers(s.Jobs)
	out.Plan.Pools = append([]scheduler.PoolPlan(nil), s.Plan.Pools...)
	if s.Runners != nil {
		out.Runners = make(map[string][]*store.Runner, len(s.Runners))
		for k, v := range s.Runners {
			out.Runners[k] = clonePointers(v)
		}
	}
	return out
}

func clonePointers[T any](in []*T) []*T {
	if in == nil {
		return nil
	}
	out := make([]*T, len(in))
	for i, v := range in {
		if v != nil {
			c := *v
			out[i] = &c
		}
	}
	return out
}

// A blip is not a machine that has gone away.
//
// A silent host is ruled out for every pool, so the test that would have said
// "the fleet still wants this machine" is skipped rather than failed, and an
// idle machine becomes releasable the moment its host stops answering. That
// turns a network partition into a delete: the VM is up, doing nothing wrong,
// and would answer again in five minutes. provider.delete_grace is how long the
// silence has to last before it is believed, and it is deliberately longer than
// the host-lost judgement so that by the time it expires the runners that were
// on the host have already been failed and the emptiness is real.
func TestAMachineIsNotReleasedWhileItsHostsSilenceIsStillShorterThanTheGrace(t *testing.T) {
	limits := demandLimits()
	limits.DeleteGrace = 10 * time.Minute

	build := func(silentFor time.Duration) MachineSnapshot {
		p := demandProvider("lab")
		pool := demandPool("alpha")
		host := demandHost("host_1", 2, 0)
		host.LastHeartbeat = machineNow.Add(-silentFor)
		m := demandMachine("mach_1", p, store.MachineReady, 4*time.Hour)
		m.HostID = host.ID
		m.IdleSince = ptrTime(machineNow.Add(-2 * time.Hour))
		return MachineSnapshot{
			Now: machineNow, Pools: []*store.Pool{pool}, Hosts: []*store.Host{host},
			Providers: []*store.Provider{p}, Machines: []*store.Machine{m},
			Limits: limits,
			Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 0, 0, "")}},
		}
	}

	// Quiet for two minutes: past the health timeout, nowhere near the grace.
	if got := planFor(t, DecideMachines(build(2*time.Minute)), "prv_lab"); len(got.Drain) != 0 {
		t.Errorf("a machine was released after two minutes of silence: %v", got.Drain)
	}
	// Quiet for fifteen: the silence has outlasted the grace and is believed.
	if got := planFor(t, DecideMachines(build(15*time.Minute)), "prv_lab"); len(got.Drain) != 1 {
		t.Errorf("a machine silent past its grace was kept: drain = %v", got.Drain)
	}
	// And a machine whose host is answering is unaffected by any of it.
	if got := planFor(t, DecideMachines(build(0)), "prv_lab"); len(got.Drain) != 1 {
		t.Errorf("a healthy idle machine was kept: drain = %v", got.Drain)
	}
}

// An unset grace leaves the decision exactly as it was, which is how the
// validator already treats it.
func TestAnUnsetDeleteGraceChangesNothing(t *testing.T) {
	p := demandProvider("lab")
	pool := demandPool("alpha")
	host := demandHost("host_1", 2, 0)
	host.LastHeartbeat = machineNow.Add(-2 * time.Minute)
	m := demandMachine("mach_1", p, store.MachineReady, 4*time.Hour)
	m.HostID = host.ID
	m.IdleSince = ptrTime(machineNow.Add(-2 * time.Hour))
	s := MachineSnapshot{
		Now: machineNow, Pools: []*store.Pool{pool}, Hosts: []*store.Host{host},
		Providers: []*store.Provider{p}, Machines: []*store.Machine{m},
		Limits: demandLimits(),
		Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 0, 0, 0, "")}},
	}
	if got := planFor(t, DecideMachines(s), p.ID); len(got.Drain) != 1 {
		t.Errorf("an unset grace withheld a release: drain = %v", got.Drain)
	}
}

// A throttled host has already given back slots it will not re-place, and a
// machine must not be released to cover work those slots can no longer do.
//
// The throttle takes capacity from future placements and leaves the runners
// already on the host alone, so a throttled host sits over its effective
// capacity until they finish. Counting the operator's configured number there
// says the fleet has room it does not have: the machine goes, the next burst
// queues, the pool reports itself at capacity because the throttle-taken slots
// count as full, and the fleet buys the same machine again -- two clones paid
// for, and the queue latency of the second.
func TestAMachineIsKeptWhileAThrottleHasTakenTheSlotsThatWouldReplaceIt(t *testing.T) {
	// The operator's own host, full and throttled a rung: four configured
	// slots, three once the throttle has had its share, and four runners on it
	// that the throttle does not evict.
	build := func(level int) MachineSnapshot {
		p := demandProvider("lab")
		pool := demandPool("alpha")
		busy := demandHost("host_busy", 4, 4)
		busy.Throttle = store.HostThrottle{Level: level}
		rented := demandHost("host_rented", 2, 0)
		m := demandMachine("mach_1", p, store.MachineReady, 4*time.Hour)
		m.HostID = rented.ID
		m.IdleSince = ptrTime(machineNow.Add(-2 * time.Hour))
		return MachineSnapshot{
			Now: machineNow, Pools: []*store.Pool{pool},
			Hosts:     []*store.Host{busy, rented},
			Providers: []*store.Provider{p}, Machines: []*store.Machine{m},
			Limits: demandLimits(),
			// The pool is satisfied only because the busy host's four runners
			// are still going.
			Plan: scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(pool, 4, 4, 4, "")}},
		}
	}

	// Unthrottled the fleet really does have the slack, so the machine goes.
	if got := planFor(t, DecideMachines(build(0)), "prv_lab"); len(got.Drain) != 1 {
		t.Errorf("an idle machine beside an unthrottled fleet was kept: drain = %v", got.Drain)
	}
	// Throttled one rung, the busy host will re-place only three of its four,
	// so the fleet is short and the machine is what covers it.
	if got := planFor(t, DecideMachines(build(1)), "prv_lab"); len(got.Drain) != 0 {
		t.Errorf("a machine was released against slots a throttle had already taken: drain = %v", got.Drain)
	}
}

// Renting is spending money, so a provider and a pool must both agree. One
// side's yes never outweighs the other's no, and the refusal is a sentence that
// says whose selector said it, because the queue looks identical either way.
func TestAMachineIsRentedOnlyWhereTheProviderAndThePoolBothAgree(t *testing.T) {
	tests := []struct {
		name      string
		provider  map[string]string
		pool      map[string]string
		wantBuy   int
		wantWhyIn string
	}{
		{name: "nobody restricts anything", wantBuy: 1},
		{name: "both agree", provider: map[string]string{"tier": "large"}, pool: map[string]string{"kind": "fake"}, wantBuy: 1},
		{name: "the provider refuses the pool", provider: map[string]string{"gpu": ""}, wantWhyIn: "provider lab only rents for pools matching gpu"},
		{name: "the pool refuses the provider", pool: map[string]string{"name": "elsewhere"}, wantWhyIn: "pool alpha only lets providers matching name=elsewhere"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := demandProvider("lab")
			p.PoolSelector = tc.provider
			alpha := demandPool("alpha")
			alpha.Labels = append(alpha.Labels, "tier=large")
			alpha.ProviderSelector = tc.pool
			s := MachineSnapshot{
				Now: machineNow, Pools: []*store.Pool{alpha}, Providers: []*store.Provider{p},
				Limits: demandLimits(),
				Plan:   scheduler.Plan{Pools: []scheduler.PoolPlan{demandPoolPlan(alpha, 0, 1, 1, "nowhere")}},
			}

			plan := DecideMachines(s)
			if got := planFor(t, plan, p.ID).Create; got != tc.wantBuy {
				t.Fatalf("create = %d, want %d", got, tc.wantBuy)
			}
			if tc.wantWhyIn == "" {
				if len(plan.Unserved) != 0 {
					t.Fatalf("a pool that was served is listed as unserved: %+v", plan.Unserved)
				}
				return
			}
			if len(plan.Unserved) != 1 || !strings.Contains(plan.Unserved[0].Why, tc.wantWhyIn) {
				t.Fatalf("unserved = %+v, want one that says %q", plan.Unserved, tc.wantWhyIn)
			}
			if plan.Unserved[0].Fix == "" {
				t.Error("the refusal does not say what to change")
			}
		})
	}
}
