package controller

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// DecideMachines is the machine-side twin of scheduler.Decide: it reads a
// snapshot of the fleet and answers how many machines each provider should be
// renting. It is pure -- no clock read, no database, no provider call, no
// randomness -- and it mutates nothing it was handed, so the whole of what a
// fleet decides to spend money on is reproducible in a table test and
// explainable in the sentence each decision carries.
//
// It lives here rather than in internal/scheduler because the scheduler is
// about placing runners on hosts that already exist, and this is about whether
// a host should exist at all: it reads provider and machine rows the scheduler
// knows nothing about, and it must not teach that package about either.
//
// What it does not do is form a second opinion. Which hosts could run a pool is
// scheduler.HostCanRun's answer and which pool claims a queued job is
// scheduler.BestPool's, because a copy of either rule would drift from the one
// that actually places the work, and a fleet that bought machines by one rule
// and refused to use them by another would pay for hosts that never run a job.
//
// The four ways it refuses to count the same capacity twice are stated where
// each one happens: the synthetic host, the shared slot ledger, ready machines,
// and the single claim on a queued job.

// MachineLimits are the fleet-wide bounds, gathered from configuration by the
// caller so that nothing in this file reads configuration for itself.
//
// A maximum of zero is zero, for the fleet exactly as for a pool's max_runners:
// the one number that decides the size of an invoice is not allowed to mean
// "unbounded" because somebody left it unset.
type MachineLimits struct {
	// MaxMachines is how many machines the whole fleet may own at once, across
	// every provider, and MaxCreatesInFlight how many it may be building.
	MaxMachines        int
	MaxCreatesInFlight int
	// IdleTimeout answers for a provider whose row names none of its own, so a
	// provider nobody thought about is not the one machine that never leaves.
	IdleTimeout time.Duration
	// ScaleDownCooldown is how long idleness has to keep being true before a
	// machine is given up, on top of the idle timeout that started it.
	ScaleDownCooldown time.Duration
	// DeleteGrace is how long a machine's host has to have been silent before
	// that silence is believed. Without it a blip releases a machine, because a
	// quiet host is ruled out for every pool and the test that would have said
	// "the fleet still wants this" is skipped rather than failed.
	DeleteGrace time.Duration
}

// MachineSnapshot is everything the decision reads. It is a value rather than a
// set of accessors for the reason scheduler.Snapshot is: a test can place a
// machine exactly one second either side of a timeout, and nothing in the
// calculation can reach past it for a fact nobody wrote down.
type MachineSnapshot struct {
	// Now is the decision time, a field rather than a clock read.
	Now time.Time
	// Plan is the caller's own scheduler.Decide over these same rows. Its pool
	// plans are where demand comes from: Desired, Current and the two blockage
	// flags are the scheduler's reading of the queue, and re-deriving them here
	// would be the copied rule this file exists not to have.
	Plan      scheduler.Plan
	Pools     []*store.Pool
	Hosts     []*store.Host
	Runners   map[string][]*store.Runner
	Jobs      []*store.Job
	Machines  []*store.Machine
	Providers []*store.Provider
	Limits    MachineLimits
}

// MachinePlan is what one pass should do, per provider.
type MachinePlan struct {
	Providers []ProviderPlan `json:"providers"`
	// Unserved is the pools whose queued work a machine would have answered,
	// where a provider could have built one, and nobody was allowed to. The
	// money was never the obstacle, so it is not a ProviderPlan's Blocked: an
	// operator raising a ceiling would be sent to the wrong screen.
	Unserved []UnservedPool `json:"unserved,omitempty"`
}

// UnservedPool is one pool no provider will rent for, with the sentence that
// says whose selector said no.
type UnservedPool struct {
	Pool string `json:"pool"`
	Why  string `json:"why"`
	Fix  string `json:"fix,omitempty"`
}

// ProviderPlan is the decision for one provider, with the sentence that
// justified it in the scheduler's voice -- an operator reading "buy 1 machine
// on proxmox-lab: pool linux-x64 is 2 runner slots short with 3 jobs queued,
// and no host in the fleet can run it" needs nothing else explained.
type ProviderPlan struct {
	ProviderID string `json:"provider_id"`
	// Wanted is how many machines this provider should have, and Have how many
	// it already has: one that owns a resource, or one on the way to owning
	// one. Both are readings, not increments -- a pass that has already bought
	// what the queue asked for wants the same number again, not one more.
	Wanted int `json:"wanted"`
	Have   int `json:"have"`
	// Create is this pass's purchase, after every ceiling has been applied.
	Create int `json:"create"`
	// Drain is the machines that have stopped earning their keep, newest
	// created first so that the one with the least warmed cache goes first.
	Drain  []string `json:"drain,omitempty"`
	Reason string   `json:"reason,omitempty"`
	// Blocked says why Create is below what the queue asked for, and
	// BlockedFix what to change. They are separate because the problems drawer
	// shows the two differently.
	Blocked    string `json:"blocked,omitempty"`
	BlockedFix string `json:"blocked_fix,omitempty"`
}

// poolDemand is one pool's unmet demand, charged to one provider.
type poolDemand struct {
	pool string
	// slots is what is still missing after everything the fleet already has,
	// or is already buying, has been subtracted from it.
	slots  int
	queued int
	// nowhere distinguishes a pool no host can run from one whose hosts are
	// merely full, because they read as completely different faults and only
	// the first is one a machine is the answer to.
	nowhere bool
}

// DecideMachines turns a snapshot into the machines each provider should buy
// and release. See the file comment for what it promises.
func DecideMachines(s MachineSnapshot) MachinePlan {
	providers := sortedProviders(s.Providers)
	if len(providers) == 0 {
		return MachinePlan{}
	}
	pools := sortedPoolsForMachines(s.Pools)
	hosts := sortedHostsForMachines(s.Hosts)
	machines := sortedMachines(s.Machines)

	planned := make(map[string]scheduler.PoolPlan, len(s.Plan.Pools))
	for _, pp := range s.Plan.Pools {
		planned[pp.PoolID] = pp
	}
	byProvider := make(map[string]*store.Provider, len(providers))
	synth := make(map[string]*store.Host, len(providers))
	for _, p := range providers {
		byProvider[p.ID] = p
		synth[p.ID] = providerSynthHost(p, s.Now)
	}
	queued := queuedByPool(pools, s.Jobs)

	// The counts every ceiling is measured against. Owns() is the money
	// question -- a failed machine whose VM was created still costs -- and
	// Pending() is the in-flight one.
	owned, pending, have := map[string]int{}, map[string]int{}, map[string]int{}
	// credit is the shared ledger's second half: what each machine that is
	// still on its way may be counted for, at most once in total however many
	// pools its shape happens to match.
	//
	// Ready machines are deliberately absent. A ready machine is already a host
	// row, already in the scheduler's Current and already in the free slots
	// subtracted below; counting it here as well IS the double count, and a
	// fleet that did it would buy a second machine for demand the first one had
	// already met.
	credit := map[string]int{}
	fleetOwned, fleetPending := 0, 0
	for _, m := range machines {
		p := byProvider[m.ProviderID]
		if p == nil {
			continue
		}
		if m.Owns() {
			owned[p.ID]++
			fleetOwned++
		}
		if m.State.Pending() {
			pending[p.ID]++
			fleetPending++
			credit[m.ID] = machineSlots(m, p)
		}
		if m.Owns() || m.State.Pending() {
			have[p.ID]++
		}
	}

	// free is the ledger's first half: slots a real host has and nothing has
	// claimed yet. It is decremented as pools take from it, so a slot that
	// satisfied linux-x64 is not also available to linux-x64-large -- the same
	// mechanic as hostSet.place mutating its own free counts.
	free := make(map[string]int, len(hosts))
	for _, h := range hosts {
		free[h.ID] = h.Free()
	}

	demand := map[string][]poolDemand{}
	var unserved []UnservedPool
	for _, pool := range pools {
		pp, ok := planned[pool.ID]
		// Only a pool the scheduler says is blocked is demand a machine can
		// answer. A pool merely waiting on this tick's create budget clears
		// itself next pass, and buying a machine for it would pay for a queue
		// that was already moving.
		if !ok || (!pp.BlockedAtCapacity && !pp.BlockedNoEligibleHost) {
			continue
		}
		short := pp.Desired - pp.Current
		if short <= 0 {
			continue
		}
		for _, h := range hosts {
			if short <= 0 {
				break
			}
			if free[h.ID] <= 0 || !scheduler.HostCanRun(h, pool, s.Now) {
				continue
			}
			// A free slot on a host that could run this pool is capacity the
			// fleet already has: the finishing job that releases it costs
			// nothing, and a machine bought against it is paid for twice.
			take := min(free[h.ID], short)
			free[h.ID] -= take
			short -= take
		}
		for _, m := range machines {
			if short <= 0 {
				break
			}
			p := byProvider[m.ProviderID]
			if p == nil || credit[m.ID] <= 0 || !providerServes(p, synth[p.ID], pool, s.Now) {
				continue
			}
			take := min(credit[m.ID], short)
			credit[m.ID] -= take
			short -= take
		}
		if short <= 0 {
			continue
		}
		p := providerFor(providers, synth, pool, owned, s.Now)
		if p == nil {
			if u, ok := unservedBy(providers, synth, pool, s.Now); ok {
				unserved = append(unserved, u)
			}
			// No provider's machines would be allowed to run this pool, so its
			// blockage is not one money can fix. BlockedNoEligibleHost is a
			// hint that a machine might help, never an instruction to buy one.
			continue
		}
		demand[p.ID] = append(demand[p.ID], poolDemand{
			pool:    pool.Name,
			slots:   short,
			queued:  queued[pool.ID],
			nowhere: pp.BlockedNoEligibleHost,
		})
	}

	drains := drainable(s, byProvider, pools, planned, hosts, machines)

	fleetRoom := s.Limits.MaxMachines - fleetOwned
	fleetCreateRoom := s.Limits.MaxCreatesInFlight - fleetPending
	out := MachinePlan{Providers: make([]ProviderPlan, 0, len(providers)), Unserved: unserved}
	for _, p := range providers {
		plan := ProviderPlan{ProviderID: p.ID, Have: have[p.ID], Drain: drains[p.ID]}
		ds := demand[p.ID]
		slots := 0
		for _, d := range ds {
			slots += d.slots
		}
		want := ceilDiv(slots, providerSlots(p))
		plan.Wanted = plan.Have + want

		create, because, fix := want, "", ""
		// Each ceiling in turn, and the sentence kept is whichever one actually
		// binds: an operator told to raise a limit that was not the one holding
		// them back has been sent to the wrong screen.
		bite := func(room int, what, how string) {
			if room < create {
				create = max(room, 0)
				because, fix = what, how
			}
		}
		what, how := providerMachineCeiling(p, owned[p.ID])
		bite(p.MaxMachines-owned[p.ID], what, how)
		// The fleet's two counts are what it owns and is building plus what
		// this pass has already granted to the providers before this one, so
		// the second provider of a pass is told the truth about what is left
		// rather than what there was before anything was bought.
		what, how = fleetMachineCeiling(s.Limits, s.Limits.MaxMachines-fleetRoom)
		bite(fleetRoom, what, how)
		what, how = providerCreateCeiling(p, pending[p.ID])
		bite(p.MaxCreatesInFlight-pending[p.ID], what, how)
		what, how = fleetCreateCeiling(s.Limits, s.Limits.MaxCreatesInFlight-fleetCreateRoom)
		bite(fleetCreateRoom, what, how)

		plan.Create = create
		fleetRoom -= create
		fleetCreateRoom -= create
		if create > 0 {
			plan.Reason = fmt.Sprintf("buy %s on %s: %s", plural(create, "machine"), p.Name, demandReason(ds))
		}
		if create < want {
			plan.Blocked = fmt.Sprintf("%s not bought: %s", plural(want-create, "machine"), because)
			plan.BlockedFix = fix
		}
		out.Providers = append(out.Providers, plan)
	}
	return out
}

// providerSynthHost is the host this provider's next machine would be, so that
// "could this provider serve that pool" is asked of scheduler.HostCanRun -- the
// predicate that will decide where the runner actually goes -- rather than of a
// second copy of the matching rule that would drift from it.
//
// It is a value nobody keeps: it exists for the length of one decision, and
// writing it to the database would put a host in the fleet that does not exist.
func providerSynthHost(p *store.Provider, now time.Time) *store.Host {
	pf := p.MachinePlatform.Normalized()
	return &store.Host{
		ID:       "synthetic-" + p.ID,
		Name:     p.Name,
		Capacity: providerSlots(p),
		Backends: store.StringSlice{string(p.MachineBackend)},
		Labels:   p.MachineLabels,
		// The kernel is what an agent on such a machine would report about
		// itself, and it is what a host selector asking for os=linux is
		// answered with; the distribution is what a pool's platform is matched
		// against. Filling only one of the two would make this machine fail a
		// test the real one would pass.
		OS:        naming.Kernel(pf.OS),
		Distro:    pf.OS,
		OSVersion: pf.OSVersion,
		Arch:      pf.Arch,
		CPUs:      int(p.MachineCPUs),
		MemoryMB:  p.MachineMemoryMB,
		// A machine that has not been built has all of its disk free, and the
		// host reserve then applies to it exactly as it would to the real one,
		// so a provider offering a machine too small for a pool is refused here
		// rather than after it has been paid for.
		DiskTotalMB: p.MachineDiskMB,
		DiskFreeMB:  p.MachineDiskMB,
		// It has never missed a heartbeat either: without the snapshot's own
		// clock here, HostCanRun's health test would rule out every provider
		// and the fleet would never buy anything.
		LastHeartbeat: now,
	}
}

// providerServes is whether this provider may rent a machine for this pool:
// both sides' selectors agree, and the machine it would build is one the pool's
// runner could be placed on. The two are asked separately because they fail
// differently: a machine that does not fit is nothing an operator chose, and a
// selector that refuses is, so only the second is ever explained as a choice.
func providerServes(p *store.Provider, synth *store.Host, pool *store.Pool, now time.Time) bool {
	return scheduler.Pair(p, pool).Agrees && scheduler.HostCanRun(synth, pool, now)
}

// unservedBy explains why no provider will rent for a pool, or returns false
// when the reason is not a selector: no provider's machine would suit the pool
// at all, which is not something either side's opt-in decided.
func unservedBy(providers []*store.Provider, synth map[string]*store.Host, pool *store.Pool, now time.Time) (UnservedPool, bool) {
	for _, p := range providers {
		if !scheduler.HostCanRun(synth[p.ID], pool, now) {
			continue
		}
		if pair := scheduler.Pair(p, pool); !pair.Agrees {
			return UnservedPool{Pool: pool.Name, Why: pair.Why, Fix: pair.Fix}, true
		}
	}
	return UnservedPool{}, false
}

// providerFor picks which provider a pool's unmet demand is charged to.
//
// Exactly one, deterministically. Charging every provider whose machines could
// run the pool would buy the same shortfall from each of them, and the ledger
// above exists precisely so that a slot is paid for once. Order is the
// providers' own -- name, then id -- and a provider with no room left under its
// own ceiling is passed over, so a half-configured row that may own nothing
// cannot starve a configured one. When none has room the first eligible
// provider still takes the demand, because a refusal an operator can read is
// worth more than silence.
func providerFor(providers []*store.Provider, synth map[string]*store.Host, pool *store.Pool, owned map[string]int, now time.Time) *store.Provider {
	var first *store.Provider
	for _, p := range providers {
		if !providerServes(p, synth[p.ID], pool, now) {
			continue
		}
		if first == nil {
			first = p
		}
		if p.MaxMachines-owned[p.ID] > 0 {
			return p
		}
	}
	return first
}

// drainable is the scale-down half: the machines that may be given up, keyed by
// provider.
//
// A machine qualifies when it is ready, its host is running nothing, it has
// been idle for the provider's idle timeout plus one scale-down cooldown, and
// removing its slots would not reopen a shortfall for any pool it serves. The
// two durations are added rather than checked separately because they say
// different things about the same observation: the idle timeout is when a
// machine stopped being useful, and the cooldown is how long that has to keep
// being true before the fleet pays the price of building it again -- a quiet
// minute between two bursts must not destroy what the second burst wants.
func drainable(s MachineSnapshot, byProvider map[string]*store.Provider, pools []*store.Pool, planned map[string]scheduler.PoolPlan, hosts []*store.Host, machines []*store.Machine) map[string][]string {
	byHost := make(map[string]*store.Host, len(hosts))
	for _, h := range hosts {
		byHost[h.ID] = h
	}
	live := map[string]int{}
	for _, rs := range s.Runners {
		for _, r := range rs {
			if r != nil && r.State.Live() {
				live[r.HostID]++
			}
		}
	}
	// slack is how many slots each pool could lose and still have room for
	// every runner it is aiming at. It is a ledger too: a drain granted here
	// spends the slack of every pool that host served, so two machines are
	// never both released against the same spare capacity.
	//
	// The slots counted are the ones a throttle leaves, not the ones the
	// operator configured. A throttled host keeps the runners placed before
	// the throttle and will re-place fewer when they finish, so counting the
	// slots it has just given back would release a machine to cover work
	// nothing can place -- and the fleet would buy the same machine again on
	// the next burst, having paid twice for one clone. Every site below reads
	// the same figure, or the ledger stops balancing and two machines could be
	// released against one host's spare slots.
	slack := make(map[string]int, len(pools))
	for _, pool := range pools {
		capacity := 0
		for _, h := range hosts {
			if scheduler.HostCanRun(h, pool, s.Now) {
				capacity += h.EffectiveCapacity()
			}
		}
		slack[pool.ID] = capacity - planned[pool.ID].Desired
	}

	// Newest first, tie-broken by id: the youngest machine has the least
	// warmed cache to throw away, and the order is the same on every pass, so
	// two passes over one snapshot never disagree about which machine goes.
	candidates := slices.Clone(machines)
	slices.SortStableFunc(candidates, func(a, b *store.Machine) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})

	out := map[string][]string{}
	for _, m := range candidates {
		p := byProvider[m.ProviderID]
		host := byHost[m.HostID]
		if p == nil || host == nil || m.State != store.MachineReady || live[host.ID] > 0 {
			continue
		}
		if m.IdleSince == nil || s.Now.Sub(*m.IdleSince) < idleTimeout(p, s.Limits)+s.Limits.ScaleDownCooldown {
			continue
		}
		// A host that has gone quiet is ruled out for every pool below, so the
		// spare-capacity test is not failed by a machine the fleet still wants
		// -- it is never reached, and a network blip releases the machine.
		// provider.delete_grace is how long the silence has to last before it
		// is treated as the machine being gone. It is longer than the
		// host-lost judgement, so by then the runners that were on it have
		// already been failed and the emptiness is real rather than reported.
		if !host.Healthy(s.Now) && s.Now.Sub(host.LastHeartbeat) < s.Limits.DeleteGrace {
			continue
		}
		served := make([]*store.Pool, 0, len(pools))
		spare := true
		for _, pool := range pools {
			if !scheduler.HostCanRun(host, pool, s.Now) {
				continue
			}
			pp := planned[pool.ID]
			if slack[pool.ID] < host.EffectiveCapacity() || pp.Desired > pp.Current {
				spare = false
				break
			}
			served = append(served, pool)
		}
		if !spare {
			continue
		}
		for _, pool := range served {
			slack[pool.ID] -= host.EffectiveCapacity()
		}
		out[p.ID] = append(out[p.ID], m.ID)
	}
	return out
}

// queuedByPool counts the queued jobs each pool has claimed, so a purchase can
// be justified by the work an operator can see waiting.
//
// The claim is scheduler.BestPool's, which gives a job to exactly one pool. A
// job counted for both of two pools that advertise its labels would be a job
// two machines were bought for.
func queuedByPool(pools []*store.Pool, jobs []*store.Job) map[string]int {
	out := make(map[string]int, len(pools))
	for _, j := range jobs {
		if j == nil || j.State != store.JobQueued || j.Provisioning != "" {
			continue
		}
		if p := scheduler.BestPool(pools, j); p != nil {
			out[p.ID]++
		}
	}
	return out
}

// providerSlots is how many runners one machine of this provider offers. A row
// that says nothing still offers one, because demand divided by zero slots
// would buy nothing for ever and never say why.
func providerSlots(p *store.Provider) int { return max(p.MachineCapacity, 1) }

// machineSlots is what one machine on its way may be counted for: what it was
// planned with, or its provider's shape when the row does not say. The row
// wins, because a provider whose shape was edited after a machine was planned
// is not evidence about the machine already being built.
func machineSlots(m *store.Machine, p *store.Provider) int {
	if m.Capacity > 0 {
		return m.Capacity
	}
	return providerSlots(p)
}

// idleTimeout is the provider's own, or the fleet's when its row names none.
func idleTimeout(p *store.Provider, l MachineLimits) time.Duration {
	if d := time.Duration(p.IdleTimeout); d > 0 {
		return d
	}
	return l.IdleTimeout
}

func ceilDiv(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// ---------------------------------------------------------------------------
// The sentences
// ---------------------------------------------------------------------------

// demandReason says what the machines are for, naming the pools an operator
// would go and look at and what is waiting in them.
func demandReason(ds []poolDemand) string {
	slots, queued, nowhere := 0, 0, 0
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		slots += d.slots
		queued += d.queued
		if d.nowhere {
			nowhere++
		}
		names = append(names, d.pool)
	}
	what := fmt.Sprintf("%s is %s short", poolList(names), plural(slots, "runner slot"))
	if len(names) > 1 {
		what = fmt.Sprintf("%s are %s short between them", poolList(names), plural(slots, "runner slot"))
	}
	if queued > 0 {
		what += " with " + plural(queued, "job") + " queued"
	}
	switch {
	case nowhere == len(ds):
		return what + ", and no host in the fleet can run " + them(len(ds))
	case nowhere == 0:
		return what + ", and every host that can run " + them(len(ds)) + " is full"
	}
	return what + ", and the fleet has nowhere to put " + them(len(ds))
}

// The four ceilings, each in the words of the setting that has to change. A
// maximum of none is none, so each one says that outright rather than reporting
// a room of zero that an operator would read as a coincidence.
func providerMachineCeiling(p *store.Provider, owned int) (string, string) {
	switch {
	case p.MaxMachines <= 0:
		return fmt.Sprintf("%s may own no machines at all", p.Name),
			"set this provider's maximum machines to the number of machines you are willing to pay for"
	case owned == 0:
		return fmt.Sprintf("%s may own only %s", p.Name, plural(p.MaxMachines, "machine")),
			"raise this provider's maximum machines"
	}
	return fmt.Sprintf("%s owns %s and its maximum is %d", p.Name, plural(owned, "machine"), p.MaxMachines),
		"raise this provider's maximum machines, or wait for one of its machines to be released"
}

func fleetMachineCeiling(l MachineLimits, owned int) (string, string) {
	switch {
	case l.MaxMachines <= 0:
		return "provider.max_machines is 0, so this fleet rents nothing",
			"set provider.max_machines to the number of machines this fleet may pay for"
	case owned == 0:
		return fmt.Sprintf("provider.max_machines allows only %s across the fleet", plural(l.MaxMachines, "machine")),
			"raise provider.max_machines"
	}
	return fmt.Sprintf("the fleet owns %s and provider.max_machines is %d", plural(owned, "machine"), l.MaxMachines),
		"raise provider.max_machines, or wait for a machine to be released"
}

func providerCreateCeiling(p *store.Provider, building int) (string, string) {
	switch {
	case p.MaxCreatesInFlight <= 0:
		return fmt.Sprintf("%s may build no machine at a time", p.Name),
			"raise this provider's maximum creates in flight above 0"
	case building == 0:
		return fmt.Sprintf("%s may build only %s at a time", p.Name, plural(p.MaxCreatesInFlight, "machine")),
			"raise this provider's maximum creates in flight"
	}
	return fmt.Sprintf("%s is already building %s and may build %d at a time", p.Name, plural(building, "machine"), p.MaxCreatesInFlight),
		"raise this provider's maximum creates in flight, or wait for one to finish"
}

func fleetCreateCeiling(l MachineLimits, building int) (string, string) {
	switch {
	case l.MaxCreatesInFlight <= 0:
		return "provider.max_creates_in_flight is 0, so nothing may be built",
			"set provider.max_creates_in_flight to the number of machines that may be built at once"
	case building == 0:
		return fmt.Sprintf("provider.max_creates_in_flight allows only %s at a time", plural(l.MaxCreatesInFlight, "machine")),
			"raise provider.max_creates_in_flight"
	}
	return fmt.Sprintf("the fleet is already building %s and provider.max_creates_in_flight is %d", plural(building, "machine"), l.MaxCreatesInFlight),
		"raise provider.max_creates_in_flight, or wait for one to finish"
}

func poolList(names []string) string {
	switch len(names) {
	case 0:
		return "no pool"
	case 1:
		return "pool " + names[0]
	case 2:
		return "pools " + names[0] + " and " + names[1]
	}
	return "pools " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// ---------------------------------------------------------------------------
// Deterministic ordering
//
// Every walk over a snapshot's rows is ordered, and every slice is copied
// before it is sorted: a decision function that reordered its caller's slices
// would be mutating the input it promises not to touch, and one that walked a
// map would answer differently on identical input.
// ---------------------------------------------------------------------------

func sortedProviders(in []*store.Provider) []*store.Provider {
	out := withoutNil(in)
	slices.SortStableFunc(out, func(a, b *store.Provider) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// sortedPoolsForMachines orders pools the way the scheduler does, so that the
// fleet takes from its shared ledger in the same order it places runners.
func sortedPoolsForMachines(in []*store.Pool) []*store.Pool {
	out := withoutNil(in)
	slices.SortStableFunc(out, func(a, b *store.Pool) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func sortedHostsForMachines(in []*store.Host) []*store.Host {
	out := withoutNil(in)
	slices.SortStableFunc(out, func(a, b *store.Host) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func sortedMachines(in []*store.Machine) []*store.Machine {
	out := withoutNil(in)
	slices.SortStableFunc(out, func(a, b *store.Machine) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func withoutNil[T any](in []*T) []*T {
	out := make([]*T, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}
