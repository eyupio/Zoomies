package scheduler

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// autoHost is a host with a held class, the way the controller leaves one after
// it has looked: sixteen slots, so that the machine and not the slot count is
// what limits it.
func autoHost(name string, cpus int, memoryMB int64, class store.SizeClass, opts ...func(*store.Host)) *store.Host {
	h := &store.Host{
		ID: "host_" + name, Name: name, CPUs: cpus, MemoryMB: memoryMB, Capacity: 16,
		Arch: "amd64", OS: "linux", Backends: store.StringSlice{"docker"},
		LastHeartbeat: sizeEpoch, SizeClass: store.HostSizeClass{Class: class},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

func arch(a string) func(*store.Host) { return func(h *store.Host) { h.Arch = a } }
func cordoned(h *store.Host)          { h.Cordoned = true }
func silentFor(d time.Duration) func(*store.Host) {
	return func(h *store.Host) { h.LastHeartbeat = sizeEpoch.Add(-d) }
}
func labelled(k, v string) func(*store.Host) {
	return func(h *store.Host) {
		if h.Labels == nil {
			h.Labels = store.StringMap{}
		}
		h.Labels[k] = v
	}
}
func offering(backends ...string) func(*store.Host) {
	return func(h *store.Host) { h.Backends = backends }
}

// sizingFor is the hook the controller supplies: a pool kept for a class is
// sized by that class's runner where the host says nothing of its own.
func sizingFor(cfg SizeConfig) func(*store.Pool) *store.Pool {
	return func(p *store.Pool) *store.Pool {
		if !p.FromHosts() {
			return p
		}
		_, class, _ := store.ParseAutoKey(p.AutoKey)
		cp := *p
		cp.FleetStandard = cfg.Runner(class)
		return &cp
	}
}

func autoInput(hosts []*store.Host, pools ...*store.Pool) AutoPoolInput {
	return AutoPoolInput{
		Now: sizeEpoch, Hosts: hosts, Pools: pools, InstallationID: "inst_1",
		Grace: 10 * time.Minute, Size: sizingFor(DefaultSizeConfig()),
	}
}

// existing is a pool the controller made earlier, as the store would return it.
func existing(in AutoPoolInput, a string, class store.SizeClass, max int, enabled bool) *store.Pool {
	p := NewAutoPool(in, a, class, store.BackendDocker)
	p.ID = "pool_" + a + "_" + string(class)
	p.MaxRunners, p.Enabled = max, enabled
	return p
}

// apply is what the controller does with a plan, for the tests that run it
// again: creates are stored, and every other change replaces the pool.
func apply(pools []*store.Pool, plan AutoPoolPlan) []*store.Pool {
	out := slices.Clone(pools)
	for _, c := range plan.Changes {
		after := *c.After
		if c.Kind == AutoPoolCreate {
			after.ID = "pool_" + c.Key
			out = append(out, &after)
			continue
		}
		for i, p := range out {
			if p.ID == c.Before.ID {
				out[i] = &after
			}
		}
	}
	return out
}

func changeFor(t *testing.T, plan AutoPoolPlan, key string) AutoPoolChange {
	t.Helper()
	for _, c := range plan.Changes {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no change for %s in %+v", key, plan.Changes)
	return AutoPoolChange{}
}

func TestTheFirstHostOfAClassMakesItsPool(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)})
	plan := PlanAutoPools(in)
	if len(plan.Changes) != 1 {
		t.Fatalf("a host with no pool for its class made %d changes: %+v", len(plan.Changes), plan.Changes)
	}
	c := plan.Changes[0]
	p := c.After
	if c.Kind != AutoPoolCreate || c.Key != "amd64/medium" || c.Before != nil {
		t.Fatalf("change = %+v", c)
	}
	if p.Name != "zoomies-medium" || !p.FromHosts() || p.AutoKey != "amd64/medium" || p.InstallationID != "inst_1" {
		t.Errorf("the pool is %+v", p)
	}
	if want := []string{"linux", "x64", "zoomies", "zoomies-medium"}; !slices.Equal(store.NormalizeLabels(p.Labels), want) {
		t.Errorf("labels = %v, want %v: the base label, the class, and the two that keep another machine's jobs off it", p.Labels, want)
	}
	if !p.SizeFromProfile || p.Platform.Arch != "amd64" || p.HostSelector[store.LabelSize] != "medium" || len(p.HostSelector) != 1 {
		t.Errorf("a pool for a class takes its size from the host, on the right machine and class: %+v", p)
	}
	if p.Backend != store.BackendDocker || !p.Ephemeral || !p.Enabled || p.Resources != (store.Resources{}) {
		t.Errorf("the pool's fixed settings are %+v", p)
	}
	// A 12-CPU, 32 GB host has 11.4 CPUs and 31130 MB left, and a medium runner
	// is 2 CPUs and 4 GB: five by CPU and seven by memory.
	if p.MaxRunners != 5 || p.MinRunners != 0 || c.Slots != 5 || !slices.Equal(c.Hosts, []string{"build-1"}) {
		t.Errorf("max %d, min %d, %d slots for %v; want 5, 0, 5 for build-1", p.MaxRunners, p.MinRunners, c.Slots, c.Hosts)
	}
	if !strings.Contains(c.Cause, "build-1") || !strings.Contains(c.Cause, "5 slots") {
		t.Errorf("the cause %q does not say which host and how many slots", c.Cause)
	}
	if got := plan.Members["amd64/medium"]; !slices.Equal(got, []string{"build-1"}) {
		t.Errorf("members = %v", got)
	}
}

func TestAHostJoiningRaisesTheMaximumByItsSlotsAndNothingIsRestarted(t *testing.T) {
	a, b := autoHost("build-1", 12, 32768, store.SizeMedium), autoHost("build-2", 12, 32768, store.SizeMedium)
	in := autoInput([]*store.Host{a, b})
	in.Pools = []*store.Pool{existing(in, "amd64", store.SizeMedium, 5, true)}
	in.Previous = map[string][]string{"amd64/medium": {"build-1"}}

	plan := PlanAutoPools(in)
	c := changeFor(t, plan, "amd64/medium")
	if c.Kind != AutoPoolResize || c.After.MaxRunners != 10 || c.Before.MaxRunners != 5 {
		t.Fatalf("a second host made %+v", c)
	}
	if c.Cause != "maximum runners 5 → 10: host build-2 joined" {
		t.Errorf("cause = %q", c.Cause)
	}
	if len(plan.Changes) != 1 {
		t.Errorf("a host joining changed more than its own pool: %+v", plan.Changes)
	}
	// Without a last look the cause can only say what the pool is now.
	in.Previous = nil
	if c := changeFor(t, PlanAutoPools(in), "amd64/medium"); !strings.Contains(c.Cause, "recomputed from 2 hosts, 10 slots") {
		t.Errorf("cause = %q", c.Cause)
	}
}

// A host that is cordoned, quiet for too long or running an agent this
// controller cannot place work on takes no new runners, so the pool does not
// promise its slots. A host that was merely quiet for a while still counts: that
// is what the grace is for.
func TestACordonedSilentOrIncompatibleHostStopsCounting(t *testing.T) {
	cases := []struct {
		name   string
		opt    func(*store.Host)
		counts bool
		text   string
	}{
		{"a cordon", cordoned, false, "host build-2 was cordoned"},
		{"quiet for longer than the grace", silentFor(11 * time.Minute), false, "host build-2 has not been heard from for too long"},
		{"quiet for less than the grace", silentFor(9 * time.Minute), true, ""},
		{"an agent this controller cannot place work on", func(h *store.Host) { h.Incompatible = true }, false, "runs an agent this controller cannot place work on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := autoHost("build-1", 12, 32768, store.SizeMedium), autoHost("build-2", 12, 32768, store.SizeMedium, tc.opt)
			in := autoInput([]*store.Host{a, b})
			in.Pools = []*store.Pool{existing(in, "amd64", store.SizeMedium, 10, true)}
			in.Previous = map[string][]string{"amd64/medium": {"build-1", "build-2"}}
			plan := PlanAutoPools(in)
			if tc.counts {
				if len(plan.Changes) != 0 {
					t.Fatalf("a host inside the grace changed the pool: %+v", plan.Changes)
				}
				return
			}
			c := changeFor(t, plan, "amd64/medium")
			if c.After.MaxRunners != 5 || !c.After.Enabled || !strings.Contains(c.Cause, tc.text) {
				t.Fatalf("the pool after losing a host = max %d, enabled %v, cause %q; want 5, true and %q",
					c.After.MaxRunners, c.After.Enabled, c.Cause, tc.text)
			}
		})
	}
}

// Running jobs finish and nothing new is placed on the host: the plan lowers the
// maximum and does not touch a runner, which is the scheduler's to drain or let
// finish.
func TestTheLastHostGoingDisablesThePoolAndKeepsItWithItsHistory(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium, cordoned)})
	pool := existing(in, "amd64", store.SizeMedium, 5, true)
	pool.MinRunners = 0
	in.Pools = []*store.Pool{pool}
	in.Previous = map[string][]string{"amd64/medium": {"build-1"}}

	plan := PlanAutoPools(in)
	c := changeFor(t, plan, "amd64/medium")
	if c.Kind != AutoPoolDisable || c.After.Enabled || c.After.MaxRunners != 0 || c.After.MinRunners != 0 {
		t.Fatalf("the last host cordoned made %+v", c)
	}
	if c.After.ID != pool.ID || c.After.Name != pool.Name || c.After.AutoKey != pool.AutoKey {
		t.Fatalf("the pool lost its record: %+v", c.After)
	}
	if want := "no medium x64 host is left to take work: host build-1 was cordoned"; c.Cause != want {
		t.Errorf("cause = %q, want %q", c.Cause, want)
	}

	// Planning again from the disabled pool changes nothing: the pool is where it should be.
	in.Pools = apply(in.Pools, plan)
	if again := PlanAutoPools(in); len(again.Changes) != 0 {
		t.Fatalf("a disabled pool with no hosts was changed again: %+v", again.Changes)
	}

	// A host of the class comes back: the pool is in use again.
	in.Hosts = []*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}
	in.Previous = map[string][]string{"amd64/medium": nil}
	back := changeFor(t, PlanAutoPools(in), "amd64/medium")
	if back.Kind != AutoPoolEnable || !back.After.Enabled || back.After.MaxRunners != 5 || !strings.Contains(back.Cause, "host build-1 is back") {
		t.Fatalf("a returning host made %+v", back)
	}
}

// A pool's page says what its maximum is made of, so the plan reports the room
// the hosts give each pool whether or not anything about it has to change.
func TestThePlanSaysWhatRoomEachPoolsHostsGiveItWhetherOrNotItChanges(t *testing.T) {
	hosts := []*store.Host{
		autoHost("build-1", 12, 32768, store.SizeMedium),
		autoHost("build-2", 12, 32768, store.SizeMedium),
		autoHost("tiny-1", 4, 16384, store.SizeSmall),
	}
	in := autoInput(hosts)
	plan := PlanAutoPools(in)
	if plan.Slots["amd64/medium"] != 10 || plan.Slots["amd64/small"] != 3 {
		t.Fatalf("slots = %v; two medium hosts give ten and a small one three", plan.Slots)
	}
	in.Pools = apply(in.Pools, plan)
	again := PlanAutoPools(in)
	if len(again.Changes) != 0 || again.Slots["amd64/medium"] != 10 || again.Slots["amd64/small"] != 3 {
		t.Fatalf("a pass with nothing to change reports slots %v and %d changes", again.Slots, len(again.Changes))
	}
	// A cap lowers the maximum and not what the hosts give.
	for _, p := range in.Pools {
		if p.AutoKey == "amd64/medium" {
			p.AutoCap = 4
		}
	}
	again = PlanAutoPools(in)
	if c := changeFor(t, again, "amd64/medium"); c.After.MaxRunners != 4 || again.Slots["amd64/medium"] != 10 {
		t.Fatalf("a cap of four made a maximum of %d and reported %v as what the hosts give", c.After.MaxRunners, again.Slots)
	}
}

// The figures docs/auto-pools.md works its example from, so that the page and the
// code cannot disagree about what a 12-CPU machine is, or how many runners of a
// class a host holds.
func TestTheWorkedExampleInTheDocsGivesTheFiguresTheDocsPrint(t *testing.T) {
	eight := func(h *store.Host) { h.Capacity = 8 }
	machines := []struct {
		name         string
		cpus         int
		memoryMB     int64
		class        store.SizeClass
		allocCPUs    float64
		allocMemory  int64
		wantSlots    int
		wantPoolName string
	}{
		{"big-1", 32, 131072, store.SizeLarge, 30.4, 124519, 7, "zoomies-large"},
		{"build-1", 12, 32768, store.SizeMedium, 11.4, 31130, 5, "zoomies-medium"},
		{"build-2", 12, 32768, store.SizeMedium, 11.4, 31130, 5, "zoomies-medium"},
		{"tiny-1", 4, 16384, store.SizeSmall, 3.5, 15565, 3, "zoomies-small"},
		{"tiny-2", 4, 16384, store.SizeSmall, 3.5, 15565, 3, "zoomies-small"},
	}
	cfg := DefaultSizeConfig()
	var hosts []*store.Host
	for _, m := range machines {
		h := autoHost(m.name, m.cpus, m.memoryMB, "", eight)
		a := h.Allocatable()
		if a.CPUs != m.allocCPUs || a.MemoryMB != m.allocMemory {
			t.Errorf("%s has %v CPUs and %d MB allocatable, the page says %v and %d", m.name, a.CPUs, a.MemoryMB, m.allocCPUs, m.allocMemory)
		}
		if class, ok := cfg.HostClass(h); !ok || class != m.class {
			t.Errorf("%s is in class %q, the page says %s", m.name, class, m.class)
		}
		h.SizeClass = store.HostSizeClass{Class: m.class}
		if room := HostRoomFor(h, sizingFor(cfg)(NewAutoPool(autoInput(nil), "amd64", m.class, store.BackendDocker))).Room; room != m.wantSlots {
			t.Errorf("%s holds %d runners of the %s class, the page says %d", m.name, room, m.class, m.wantSlots)
		}
		hosts = append(hosts, h)
	}
	plan := PlanAutoPools(autoInput(hosts))
	got := map[string]int{}
	for _, c := range plan.Changes {
		got[c.After.Name] = c.After.MaxRunners
	}
	if got["zoomies-large"] != 7 || got["zoomies-medium"] != 10 || got["zoomies-small"] != 6 || len(got) != 3 {
		t.Fatalf("the pools' maximums are %v; the page says 7, 10 and 6", got)
	}
}

func TestAnOperatorsPauseKeepsAPoolOutOfUseWhateverItsHostsDo(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)})
	pool := existing(in, "amd64", store.SizeMedium, 0, false)
	pool.AutoPaused = true
	in.Pools = []*store.Pool{pool}

	plan := PlanAutoPools(in)
	c := changeFor(t, plan, "amd64/medium")
	if c.After.Enabled || c.After.MaxRunners != 5 || c.Kind == AutoPoolEnable {
		t.Fatalf("a paused pool = %+v; it should stay out of use and still say what room its hosts give it", c)
	}
	in.Pools = apply(in.Pools, plan)
	if again := PlanAutoPools(in); len(again.Changes) != 0 {
		t.Fatalf("a paused pool is changed on every pass: %+v", again.Changes)
	}
}

// A pause is said as a pause. The sentence is what the audit log and the
// Overview feed show, and "maximum runners 5 → 5" for a pool that was put out of
// use would send an operator looking for a host that did nothing.
func TestPausingAndResumingAPoolSayWhoDidIt(t *testing.T) {
	hosts := []*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}
	in := autoInput(hosts)
	pool := existing(in, "amd64", store.SizeMedium, 5, true)
	in.Pools = []*store.Pool{pool}
	in.Previous = map[string][]string{"amd64/medium": {"build-1"}}

	paused := *pool
	paused.AutoPaused = true
	in.Pools = []*store.Pool{&paused}
	c := changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolDisable || c.After.Enabled || !strings.Contains(c.Cause, "an operator paused the pool") {
		t.Fatalf("pausing a pool made %+v", c)
	}

	// The pool as it is once the pause has been applied, and then lifted.
	off := paused
	off.Enabled = false
	off.AutoPaused = false
	in.Pools = []*store.Pool{&off}
	c = changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolEnable || !c.After.Enabled || !strings.Contains(c.Cause, "an operator resumed the pool") {
		t.Fatalf("resuming a pool made %+v", c)
	}

	// With no host counted the last time, the same pool is not being resumed: a
	// host has come back.
	in.Previous = map[string][]string{"amd64/medium": {}}
	c = changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolEnable || !strings.Contains(c.Cause, "host build-1 is back") {
		t.Fatalf("a returning host made %+v", c)
	}
}

func TestAnOperatorsCapAndWarmCountAreNamedInTheCause(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)})
	in.Previous = map[string][]string{"amd64/medium": {"build-1"}}

	capped := existing(in, "amd64", store.SizeMedium, 5, true)
	capped.AutoCap = 3
	in.Pools = []*store.Pool{capped}
	c := changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolResize || c.Cause != "maximum runners 5 → 3: an operator capped the pool at 3 runners" {
		t.Fatalf("a cap made %q (%s)", c.Cause, c.Kind)
	}

	warm := existing(in, "amd64", store.SizeMedium, 5, true)
	warm.AutoMin = 2
	in.Pools = []*store.Pool{warm}
	c = changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolResize || c.Cause != "warm runners 0 → 2: an operator asked for 2 runners kept warm" {
		t.Fatalf("a warm count made %q (%s)", c.Cause, c.Kind)
	}

	// A host that changed what it offers is still said to have, when nothing else did.
	plain := existing(in, "amd64", store.SizeMedium, 4, true)
	in.Pools = []*store.Pool{plain}
	c = changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Cause != "maximum runners 4 → 5: the slots of host build-1 changed" {
		t.Fatalf("a change in a host's slots made %q", c.Cause)
	}
}

func TestAnOperatorsCapAndWarmCountAreRespected(t *testing.T) {
	hosts := []*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}
	cases := []struct {
		name         string
		min, cap     int
		wantMin, max int
	}{
		{"no cap and nothing kept warm", 0, 0, 0, 5},
		{"a cap below the slots", 0, 3, 0, 3},
		{"a cap above the slots does nothing", 0, 9, 0, 5},
		{"runners kept warm", 2, 0, 2, 5},
		{"warm runners cannot outnumber the cap", 5, 3, 3, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := autoInput(hosts)
			pool := existing(in, "amd64", store.SizeMedium, 0, true)
			pool.AutoMin, pool.AutoCap = tc.min, tc.cap
			in.Pools = []*store.Pool{pool}
			c := changeFor(t, PlanAutoPools(in), "amd64/medium")
			if c.After.MaxRunners != tc.max || c.After.MinRunners != tc.wantMin {
				t.Fatalf("max %d, min %d; want %d, %d", c.After.MaxRunners, c.After.MinRunners, tc.max, tc.wantMin)
			}
			// What the operator asked for is never overwritten by what was derived from it.
			if c.After.AutoMin != tc.min || c.After.AutoCap != tc.cap {
				t.Fatalf("the controller's output was written over the operator's ask: %+v", c.After)
			}
		})
	}
}

// The maximum follows the host's slots as the scheduler counts them, so a host
// the throttle has stepped down offers fewer.
func TestAnActiveThrottleLowersThePoolsMaximum(t *testing.T) {
	h := autoHost("build-1", 12, 32768, store.SizeMedium)
	h.Capacity = 4
	in := autoInput([]*store.Host{h})
	pool := existing(in, "amd64", store.SizeMedium, 4, true)
	in.Pools = []*store.Pool{pool}
	if len(PlanAutoPools(in).Changes) != 0 {
		t.Fatal("an unthrottled host with four slots should leave a pool at four alone")
	}
	h.Throttle = store.HostThrottle{Level: 2}
	c := changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.After.MaxRunners != 2 {
		t.Fatalf("a host on throttle rung two gives %d slots, want 2", c.After.MaxRunners)
	}
}

func TestAHostThatChangesClassMovesBetweenPoolsInOnePass(t *testing.T) {
	in := autoInput([]*store.Host{
		autoHost("build-1", 12, 32768, store.SizeMedium),
		autoHost("build-2", 12, 32768, store.SizeMedium), // was small until now
	})
	small := existing(in, "amd64", store.SizeSmall, 3, true)
	medium := existing(in, "amd64", store.SizeMedium, 5, true)
	in.Pools = []*store.Pool{small, medium}
	in.Previous = map[string][]string{"amd64/small": {"build-2"}, "amd64/medium": {"build-1"}}

	plan := PlanAutoPools(in)
	if len(plan.Changes) != 2 {
		t.Fatalf("a host changing class made %d changes: %+v", len(plan.Changes), plan.Changes)
	}
	gone := changeFor(t, plan, "amd64/small")
	if gone.Kind != AutoPoolDisable || !strings.Contains(gone.Cause, "host build-2") {
		t.Errorf("the class it left made %+v", gone)
	}
	grew := changeFor(t, plan, "amd64/medium")
	if grew.Kind != AutoPoolResize || grew.After.MaxRunners != 10 {
		t.Errorf("the class it joined made %+v", grew)
	}
}

func TestHostsOfDifferentArchitecturesAreInDifferentPools(t *testing.T) {
	in := autoInput([]*store.Host{
		autoHost("build-1", 12, 32768, store.SizeMedium, arch("x86_64")),
		autoHost("arm-1", 12, 32768, store.SizeMedium, arch("aarch64")),
		autoHost("build-2", 12, 32768, store.SizeMedium, arch("amd64")),
	})
	plan := PlanAutoPools(in)
	if len(plan.Changes) != 2 {
		t.Fatalf("two architectures made %d changes: %+v", len(plan.Changes), plan.Changes)
	}
	amd := changeFor(t, plan, "amd64/medium")
	arm := changeFor(t, plan, "arm64/medium")
	if amd.After.Name != "zoomies-medium" || arm.After.Name != "zoomies-arm64-medium" {
		t.Errorf("names = %q and %q", amd.After.Name, arm.After.Name)
	}
	if amd.After.MaxRunners != 10 || arm.After.MaxRunners != 5 {
		t.Errorf("max = %d and %d, want 10 and 5", amd.After.MaxRunners, arm.After.MaxRunners)
	}
	if !slices.Contains(store.NormalizeLabels(arm.After.Labels), "arm64") || slices.Contains(store.NormalizeLabels(arm.After.Labels), "x64") {
		t.Errorf("the arm64 pool's labels are %v", arm.After.Labels)
	}
	// The classes of one architecture come in order, smallest first.
	in.Hosts = append(in.Hosts, autoHost("small-1", 4, 16384, store.SizeSmall))
	keys := []string{}
	for _, c := range PlanAutoPools(in).Changes {
		keys = append(keys, c.Key)
	}
	if !slices.Equal(keys, []string{"amd64/small", "amd64/medium", "arm64/medium"}) {
		t.Errorf("changes are in the order %v", keys)
	}
}

// An operator's tag decides which pool a host is in, and a size that is not a
// class puts it in none rather than quietly into the class the machine says. A
// class written as "Large" is not one: the pool's host selector compares the
// label exactly and would never match the host, so counting it towards the pool
// would promise slots no runner could be placed in.
func TestAnOperatorsSizeTagPlacesAHostAndABadOneLeavesItOut(t *testing.T) {
	in := autoInput([]*store.Host{
		autoHost("tagged", 4, 16384, store.SizeSmall, labelled("size", "large")),
		autoHost("odd", 4, 16384, store.SizeSmall, labelled("size", "huge")),
		autoHost("loud", 4, 16384, store.SizeSmall, labelled("size", "Large")),
	})
	plan := PlanAutoPools(in)
	if len(plan.Changes) != 1 || plan.Changes[0].Key != "amd64/large" {
		t.Fatalf("changes = %+v; want one pool, for large", plan.Changes)
	}
	if got := plan.Changes[0].Hosts; !slices.Equal(got, []string{"tagged"}) {
		t.Errorf("the large pool counts %v, want only the host that asked for it", got)
	}
	subjects := []string{}
	for _, f := range plan.Findings {
		if f.Code != FindingHostSkipped || !strings.Contains(f.Fix, "small, medium or large") {
			t.Errorf("finding %+v", f)
		}
		subjects = append(subjects, f.Subject)
	}
	slices.Sort(subjects)
	if !slices.Equal(subjects, []string{"loud", "odd"}) {
		t.Fatalf("findings are for %v, want the two hosts whose tag is not a class", subjects)
	}
}

func TestAHostWithNoClassYetCountsForNothingAndIsNoFinding(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("new", 4, 16384, "")})
	plan := PlanAutoPools(in)
	if len(plan.Changes) != 0 || len(plan.Findings) != 0 {
		t.Fatalf("an unclassified host made %+v", plan)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != SkipNoClass {
		t.Fatalf("skipped = %+v", plan.Skipped)
	}
}

func TestOnlyWhatAnOperatorCanFixBecomesAFinding(t *testing.T) {
	in := autoInput([]*store.Host{
		autoHost("cordoned", 4, 16384, store.SizeSmall, cordoned),
		autoHost("quiet", 4, 16384, store.SizeSmall, silentFor(time.Hour)),
		autoHost("risc", 4, 16384, store.SizeSmall, arch("riscv64")),
		autoHost("process-only", 4, 16384, store.SizeSmall, offering("process")),
		autoHost("fine", 4, 16384, store.SizeSmall),
	})
	plan := PlanAutoPools(in)
	var subjects []string
	for _, f := range plan.Findings {
		subjects = append(subjects, f.Subject)
	}
	slices.Sort(subjects)
	if !slices.Equal(subjects, []string{"process-only", "risc"}) {
		t.Fatalf("findings are for %v; a cordon and a quiet host are not things to fix", subjects)
	}
	if len(plan.Skipped) != 4 {
		t.Fatalf("skipped = %d hosts, want 4", len(plan.Skipped))
	}
}

func TestTheBackendOfANewPoolIsDockerWhereAnyHostOffersItAndPodmanOtherwise(t *testing.T) {
	both := autoInput([]*store.Host{
		autoHost("a", 12, 32768, store.SizeMedium, offering("podman")),
		autoHost("b", 12, 32768, store.SizeMedium, offering("docker", "podman")),
	})
	c := PlanAutoPools(both).Changes[0]
	if c.After.Backend != store.BackendDocker || !slices.Equal(c.Hosts, []string{"b"}) {
		t.Fatalf("a pool for one docker host and one podman host = %s with hosts %v; want docker with the host that offers it", c.After.Backend, c.Hosts)
	}
	podman := autoInput([]*store.Host{autoHost("a", 12, 32768, store.SizeMedium, offering("podman"))})
	if c := PlanAutoPools(podman).Changes[0]; c.After.Backend != store.BackendPodman {
		t.Fatalf("a podman-only fleet's pool is %s", c.After.Backend)
	}
	// Once a pool exists its backend is what hosts are counted against, so a
	// pool never changes backend under its own runners.
	in := autoInput([]*store.Host{autoHost("a", 12, 32768, store.SizeMedium, offering("podman"))})
	pool := existing(in, "amd64", store.SizeMedium, 5, true)
	in.Pools = []*store.Pool{pool}
	c = changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolDisable || c.After.Backend != store.BackendDocker {
		t.Fatalf("a docker pool with only a podman host = %+v", c)
	}
}

// Nothing an operator made is ever rewritten, claimed or deleted. Where the pool
// the controller would make would collide with one of theirs it is not made, and
// the operator is told why.
func TestAutomaticPoolsCoexistWithOperatorPools(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)})
	operator := &store.Pool{
		ID: "pool_ops", Name: "zoomies-ops", InstallationID: "inst_1", Backend: store.BackendDocker, MaxRunners: 4, Enabled: true,
		Labels: store.StringSlice{"zoomies", "zoomies-ops"}, HostSelector: store.StringMap{"size": "medium"},
	}
	in.Pools = []*store.Pool{operator}
	plan := PlanAutoPools(in)
	if len(plan.Changes) != 1 || plan.Changes[0].Kind != AutoPoolCreate {
		t.Fatalf("an unrelated operator pool changed the plan: %+v", plan.Changes)
	}
	for _, c := range plan.Changes {
		if c.Before == operator || c.After == operator {
			t.Fatal("the plan touches an operator's pool")
		}
	}
	in.Pools = apply(in.Pools, plan)
	if operator.MaxRunners != 4 || !operator.Enabled || operator.FromHosts() {
		t.Fatalf("the operator's pool was changed: %+v", operator)
	}
	if again := PlanAutoPools(in); len(again.Changes) != 0 {
		t.Fatalf("a second pass changed %+v", again.Changes)
	}
}

func TestAnOperatorPoolWithTheNameOrTheClassLabelIsNeverTakenOver(t *testing.T) {
	hosts := []*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}

	named := &store.Pool{ID: "pool_a", Name: "Zoomies-Medium", InstallationID: "inst_1", Backend: store.BackendDocker, Enabled: true,
		Labels: store.StringSlice{"zoomies", "mine"}}
	plan := PlanAutoPools(autoInput(hosts, named))
	if len(plan.Changes) != 0 || len(plan.Findings) != 1 || plan.Findings[0].Code != FindingNameTaken ||
		!strings.Contains(plan.Findings[0].Message, "zoomies-medium") {
		t.Fatalf("a pool with the name = %+v", plan)
	}

	labelled := &store.Pool{ID: "pool_b", Name: "zoomies-gpu", InstallationID: "inst_1", Backend: store.BackendDocker, Enabled: true,
		Labels: store.StringSlice{"zoomies", "ZOOMIES-MEDIUM"}}
	plan = PlanAutoPools(autoInput(hosts, labelled))
	if len(plan.Changes) != 0 || len(plan.Findings) != 1 || plan.Findings[0].Code != FindingLabelTaken ||
		!strings.Contains(plan.Findings[0].Message, "zoomies-gpu") {
		t.Fatalf("a pool with the label = %+v", plan)
	}

	// The same label on a pool in another installation is not a clash: it cannot
	// claim this installation's jobs.
	elsewhere := &store.Pool{ID: "pool_c", Name: "zoomies-gpu", InstallationID: "inst_2", Backend: store.BackendDocker, Enabled: true,
		Labels: store.StringSlice{"zoomies", "zoomies-medium"}}
	if plan := PlanAutoPools(autoInput(hosts, elsewhere)); len(plan.Changes) != 1 {
		t.Fatalf("a label in another installation blocked the pool: %+v", plan)
	}
}

func TestPoolsKeptForAnotherInstallationArePutOutOfUse(t *testing.T) {
	in := autoInput(nil)
	old := existing(in, "amd64", store.SizeMedium, 5, true)
	old.InstallationID = "inst_other"
	in.Pools = []*store.Pool{old}
	plan := PlanAutoPools(in)
	c := changeFor(t, plan, "amd64/medium")
	if c.Kind != AutoPoolDisable || c.After.Enabled || c.After.MaxRunners != 0 || c.After.InstallationID != "inst_other" ||
		!strings.Contains(c.Cause, "another GitHub App installation") {
		t.Fatalf("a pool for the wrong installation = %+v", c)
	}
	in.Pools = apply(in.Pools, plan)
	if again := PlanAutoPools(in); len(again.Changes) != 0 {
		t.Fatalf("a pool already out of use was changed again: %+v", again.Changes)
	}
}

// The plan is recomputed from the hosts as they are, so a pool whose fixed
// settings drifted -- because the docker mode setting changed -- is brought
// back, and nothing else about it is.
func TestAPoolWhoseFixedSettingsDriftedIsReshaped(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)})
	pool := existing(in, "amd64", store.SizeMedium, 5, true)
	in.DockerMode = store.DockerDinD
	in.Pools = []*store.Pool{pool}
	c := changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.Kind != AutoPoolReshape || c.After.DockerMode != store.DockerDinD || c.After.MaxRunners != 5 || c.After.AutoKey != "amd64/medium" {
		t.Fatalf("a drifted docker mode made %+v", c)
	}
	// Whatever else an operator set on the pool is theirs.
	pool.IdleTimeout = store.Duration(time.Hour)
	pool.AutoCap = 4
	in.DockerMode = store.DockerNone
	pool.DockerMode = store.DockerNone
	c = changeFor(t, PlanAutoPools(in), "amd64/medium")
	if c.After.IdleTimeout != store.Duration(time.Hour) || c.After.MaxRunners != 4 {
		t.Fatalf("the plan overwrote an operator's setting: %+v", c.After)
	}
}

// The property the reconciler leans on: it is run on every event and on a timer,
// and after a pass has been applied the next one has nothing to do.
func TestPlanningAgainAfterApplyingAPlanChangesNothing(t *testing.T) {
	hosts := []*store.Host{
		autoHost("big-1", 32, 131072, store.SizeLarge),
		autoHost("mid-1", 12, 32768, store.SizeMedium),
		autoHost("mid-2", 12, 32768, store.SizeMedium, cordoned),
		autoHost("small-1", 4, 16384, store.SizeSmall),
		autoHost("arm-1", 8, 32768, store.SizeMedium, arch("arm64")),
	}
	in := autoInput(hosts)
	first := PlanAutoPools(in)
	if len(first.Changes) != 4 {
		t.Fatalf("five hosts in four classes made %d changes: %+v", len(first.Changes), first.Changes)
	}
	in.Pools = apply(nil, first)
	in.Previous = first.Members
	if second := PlanAutoPools(in); len(second.Changes) != 0 {
		t.Fatalf("a second pass changed %+v", second.Changes)
	}
}

// A pool that needs its limits moved and its fixed settings corrected at once is
// one change that says both, so the controller can write both in one statement.
// Applied as limits alone, the second half waited for the next pass, and each of
// the two passes wrote an audit row for what was really one decision.
func TestAPoolThatNeedsBothItsLimitsAndItsFixedSettingsCorrectedIsOneChange(t *testing.T) {
	in := autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)})
	pool := existing(in, "amd64", store.SizeMedium, 2, true)
	pool.Labels = store.StringSlice{"zoomies", "zoomies-medium", "linux", "x64", "left-behind"}
	plan := PlanAutoPools(autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}, pool))

	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %+v; want the one", plan.Changes)
	}
	c := plan.Changes[0]
	if c.Kind != AutoPoolResize || !c.Reshaped {
		t.Fatalf("the change is %s, reshaped %v; want a resize that also reshapes", c.Kind, c.Reshaped)
	}
	if !strings.Contains(c.Cause, "also brought back in line") {
		t.Errorf("the cause %q does not say the fixed settings were corrected too", c.Cause)
	}
	if slices.Contains(c.After.Labels, "left-behind") || c.After.MaxRunners != 5 {
		t.Errorf("the pool after is %+v", c.After)
	}
	// And a pool that needs only one of the two says only that.
	only := existing(in, "amd64", store.SizeMedium, 5, true)
	only.Labels = store.StringSlice{"zoomies", "zoomies-medium", "linux", "x64", "left-behind"}
	plan = PlanAutoPools(autoInput([]*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}, only))
	if len(plan.Changes) != 1 || plan.Changes[0].Kind != AutoPoolReshape || !plan.Changes[0].Reshaped {
		t.Fatalf("a pool whose limits are right and whose labels are not = %+v", plan.Changes)
	}
}

// Pool names are unique across the instance and a pool kept for the installation
// the pools used to belong to keeps its name when they are moved, so the pool for
// the new one cannot be made. It is said, with what to do about it.
func TestAPoolKeptForAnotherInstallationThatHoldsTheNameIsAFinding(t *testing.T) {
	hosts := []*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}
	old := existing(AutoPoolInput{InstallationID: "inst_old"}, "amd64", store.SizeMedium, 5, false)
	plan := PlanAutoPools(autoInput(hosts, old))

	for _, c := range plan.Changes {
		if c.Kind == AutoPoolCreate {
			t.Fatalf("a pool was planned that the name already held: %+v", c)
		}
	}
	var named *AutoPoolFinding
	for i := range plan.Findings {
		if plan.Findings[i].Code == FindingNameTaken {
			named = &plan.Findings[i]
		}
	}
	if named == nil || !strings.Contains(named.Message, "another GitHub App installation") ||
		!strings.Contains(named.Fix, "delete pool zoomies-medium") {
		t.Fatalf("findings = %+v; want the name held by the old installation's pool to be said", plan.Findings)
	}
	// With the old pool gone the new one is made.
	plan = PlanAutoPools(autoInput(hosts))
	if len(plan.Changes) != 1 || plan.Changes[0].Kind != AutoPoolCreate {
		t.Fatalf("with the name free the plan is %+v", plan.Changes)
	}
}

// autoCollision stops the controller making a pool beside one an operator made,
// and nothing stopped an operator making theirs beside one the controller keeps.
// The two split the jobs that ask for the class, so it is said.
func TestAnOperatorPoolThatTakesTheClassLabelOfAKeptPoolIsAFinding(t *testing.T) {
	hosts := []*store.Host{autoHost("build-1", 12, 32768, store.SizeMedium)}
	in := autoInput(hosts)
	kept := existing(in, "amd64", store.SizeMedium, 5, true)
	mine := testPool("mine", "zoomies", "zoomies-medium", "linux", "x64")
	mine.InstallationID = "inst_1"

	plan := PlanAutoPools(autoInput(hosts, kept, mine))
	if len(plan.Changes) != 0 {
		t.Fatalf("a pool in order changed: %+v", plan.Changes)
	}
	if len(plan.Findings) != 1 || plan.Findings[0].Code != FindingLabelTaken || plan.Findings[0].Subject != "zoomies-medium" ||
		!strings.Contains(plan.Findings[0].Message, "pool mine answers to zoomies-medium") ||
		!strings.Contains(plan.Findings[0].Fix, "take zoomies-medium off pool mine") {
		t.Fatalf("findings = %+v", plan.Findings)
	}
	// A pool of theirs that does not carry the label, or is switched off, is none.
	other := testPool("other", "zoomies", "zoomies-ops", "linux", "x64")
	other.InstallationID = "inst_1"
	off := testPool("off", "zoomies", "zoomies-medium")
	off.InstallationID, off.Enabled = "inst_1", false
	if plan = PlanAutoPools(autoInput(hosts, kept, other, off)); len(plan.Findings) != 0 {
		t.Fatalf("findings = %+v for pools that do not split the class", plan.Findings)
	}
}

// A host that has crossed its disk reserve places nothing, and the scheduler
// already says so every pass. The pool's maximum is about what the machine holds:
// following the free space would put a line in the audit log every time a host
// dipped below the reserve and came back, and disable the pool in between.
func TestAHostLowOnDiskStillCountsItsSlotsTowardsThePoolsMaximum(t *testing.T) {
	roomy := autoHost("a", 12, 32768, store.SizeMedium, func(h *store.Host) { h.DiskTotalMB, h.DiskFreeMB = 500_000, 400_000 })
	full := autoHost("a", 12, 32768, store.SizeMedium, func(h *store.Host) { h.DiskTotalMB, h.DiskFreeMB = 500_000, 1_000 })
	slots := func(h *store.Host) int {
		plan := PlanAutoPools(autoInput([]*store.Host{h}))
		if len(plan.Changes) != 1 {
			t.Fatalf("changes = %+v; want the pool made", plan.Changes)
		}
		return plan.Slots["amd64/medium"]
	}
	if r, f := slots(roomy), slots(full); r != f || r == 0 {
		t.Fatalf("a host with 400 GB free holds %d runners and one with 1 GB free holds %d; the pool's maximum is not about the disk", r, f)
	}
	// The scheduler is the one that refuses the full host, and it still does.
	pool := NewAutoPool(autoInput(nil), "amd64", store.SizeMedium, store.BackendDocker)
	if room := HostRoomFor(full, sizingFor(DefaultSizeConfig())(pool)); room.Room != 0 || room.LimitedBy != "disk" {
		t.Fatalf("the full host's room is %+v; the scheduler places nothing on it", room)
	}
}

func TestSilenceIsCountedFromWhenTheControllerBeganListeningIfThatIsLater(t *testing.T) {
	quiet := autoHost("a", 12, 32768, store.SizeMedium, silentFor(3*time.Hour))
	in := autoInput([]*store.Host{quiet})
	if plan := PlanAutoPools(in); len(plan.Changes) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != SkipSilent {
		t.Fatalf("with no start time a host silent for three hours was %+v; want it dropped", plan)
	}

	in.Since = in.Now.Add(-time.Minute)
	plan := PlanAutoPools(in)
	if len(plan.Skipped) != 0 || len(plan.Changes) != 1 || plan.Changes[0].Kind != AutoPoolCreate {
		t.Fatalf("a host silent only while the controller was down was %+v; want it counted", plan)
	}

	// Not for ever: the grace runs from the later of the two.
	in.Since = in.Now.Add(-in.Grace - time.Minute)
	if plan := PlanAutoPools(in); len(plan.Skipped) != 1 {
		t.Fatalf("a host silent since before the grace ran from the start was %+v; want it dropped", plan.Skipped)
	}
}
