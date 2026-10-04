package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// profile gives a host its runner profile through the one write an operator's
// edit takes, and reads the host back, so a test sees what the next pass will.
func (h *harness) profile(host *store.Host, p store.RunnerProfile) *store.Host {
	h.t.Helper()
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &p}); err != nil {
		h.t.Fatalf("PatchHost: %v", err)
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return got
}

// profilePool is a pool that takes its size from the host, wanting n runners.
func (h *harness) profilePool(inst *store.Installation, name string, n int) *store.Pool {
	h.t.Helper()
	p := h.pool(inst, name)
	p.MinRunners, p.MaxRunners, p.SizeFromProfile = n, max(n, 4), true
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		h.t.Fatalf("UpdatePool: %v", err)
	}
	return p
}

var bigProfile = store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8192}}

// The size a profile pool's runner is given is the host's standard, the row
// says where it came from, and the agent is told "host" -- one that predates
// the value would otherwise read it as a size somebody typed and give a
// docker-in-docker pair's daemon the whole of it on top of the runner's.
func TestAProfilePoolGetsTheSizeItsHostNamesAndTheAgentIsToldHost(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.profilePool(inst, "linux-x64", 1)
	host := h.profile(h.measuredHost("big", 12, 32768, 8, enforcesEverything), bigProfile)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	r := h.onlyRunner()
	if r.AllocatedCPUs != 3 || r.AllocatedMemoryMB != 8192 || r.AllocationSource != store.AllocationFromProfile {
		t.Fatalf("row allocation = %v CPUs, %d MB from %q; want 3, 8192 from %q",
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource, store.AllocationFromProfile)
	}
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil {
		t.Fatal("the create task carries no spec")
	}
	if task.Spec.Resources.CPUs != 3 || task.Spec.Resources.MemoryMB != 8192 {
		t.Fatalf("task spec = %+v; the agent would apply something other than what the row records", task.Spec.Resources)
	}
	if task.Spec.ResourcesSource != store.AllocationFromHost {
		t.Fatalf("the agent is told the source is %q, want %q: an older agent reads any other as typed",
			task.Spec.ResourcesSource, store.AllocationFromHost)
	}
	if view := h.c.runnerView(h.ctx, r); view.AllocationSource != store.AllocationFromProfile {
		t.Fatalf("the Runners page says %q, want the profile named", view.AllocationSource)
	}
}

func TestAProfilePoolOnAHostWithNoStandardGetsTheFleetsDefaultSize(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.profilePool(inst, "linux-x64", 1)
	h.measuredHost("plain", 12, 32768, 8, enforcesEverything)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	r := h.onlyRunner()
	cpus, memoryMB := config.Default().Runners.DefaultRunnerSize()
	if r.AllocatedCPUs != cpus || r.AllocatedMemoryMB != memoryMB || r.AllocationSource != store.AllocationFromProfile {
		t.Fatalf("row allocation = %v CPUs, %d MB from %q; want the fleet's default %v and %d, called a profile size",
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource, cpus, memoryMB)
	}
}

// The slot count a standard gives is what a pass fills a host to, whatever
// capacity the operator set beside it.
func TestAPassFillsAProfiledHostToItsDerivedSlots(t *testing.T) {
	h := newHarness(t)
	// Credentials are minted one at a time by default, which would spread the
	// three creates over three passes; the count is what is under test.
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.RegistrationConcurrency = 8 })
	inst := h.installation()
	h.profilePool(inst, "linux-x64", 8)
	h.profile(h.measuredHost("big", 12, 32768, 8, enforcesEverything), bigProfile)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	if got := len(h.runners()); got != 3 {
		t.Fatalf("%d runners created, want the 3 that 11.4 CPUs and 30 GB hold at 3 CPUs and 8 GB", got)
	}
}

// A host with no profile and a pool that says nothing about one are sized as
// they always were: that is the compatibility promise, held at the one place
// the controller hands a pool to the scheduler.
func TestSizingAPoolCarriesTheFleetsFiguresOnlyWhereTheyApply(t *testing.T) {
	fleet := config.Default().Runners
	plain := &store.Pool{ID: "pool_a", Name: "a"}
	if got := sizingPool(plain, fleet); got != plain {
		t.Fatal("a pool that inherits nothing was copied")
	}

	fleet.MinimumCPUs, fleet.MinimumMemoryMB = 1, 1024
	got := sizingPool(plain, fleet)
	if got == plain || got.Resources.MinCPUs != 1 || got.FleetMinimum.CPUs != 1 || got.FleetMinimum.MemoryMB != 1024 {
		t.Fatalf("an inherited minimum was not carried and marked as the fleet's: %+v / %+v", got.Resources, got.FleetMinimum)
	}
	if plain.Resources.MinCPUs != 0 || plain.FleetMinimum != (store.RunnerSize{}) {
		t.Fatal("sizing wrote the fleet's figures into the stored pool")
	}

	own := &store.Pool{ID: "pool_b", Name: "b", Resources: store.Resources{MinCPUs: 2, MinMemoryMB: 2048}}
	if got := sizingPool(own, fleet); got != own {
		t.Fatalf("a pool that set its own minimum was copied: %+v", got)
	}

	profile := &store.Pool{ID: "pool_c", Name: "c", SizeFromProfile: true}
	got = sizingPool(profile, config.Default().Runners)
	cpus, memoryMB := config.Default().Runners.DefaultRunnerSize()
	if got == profile || got.FleetStandard != (store.RunnerSize{CPUs: cpus, MemoryMB: memoryMB}) {
		t.Fatalf("a pool sized by its host was not given the fleet's default: %+v", got.FleetStandard)
	}
	if profile.FleetStandard != (store.RunnerSize{}) {
		t.Fatal("the default was written into the stored pool")
	}
}

// What the host's own page shows: the slots its standard gives and what sets
// them, and every figure with whose it is, so an empty field reads as "follows
// the fleet" and not as nothing.
func TestAHostViewSaysWhereEachFigureOfItsProfileCameFrom(t *testing.T) {
	h := newHarness(t)
	h.c.UpdateConfig(func(cfg *config.Config) {
		cfg.Runners.MinimumCPUs, cfg.Runners.MinimumMemoryMB = 0.5, 512
	})
	host := h.profile(h.measuredHost("big", 12, 32768, 2, enforcesEverything), store.RunnerProfile{
		Minimum:  store.RunnerSize{CPUs: 1},
		Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8192, BurstMaxCPUs: 6},
	})

	v := h.c.HostView(host)
	if v.Slots != 2 || v.SlotsLimitedBy != "capacity" {
		t.Errorf("slots = %d limited by %q, want 2 limited by capacity: the machine holds 3 and the operator allows 2", v.Slots, v.SlotsLimitedBy)
	}
	if v.RunnerProfile == nil || v.RunnerProfile.Standard.CPUs != 3 {
		t.Errorf("the profile as the operator wrote it is missing: %+v", v.RunnerProfile)
	}
	min, std := v.EffectiveProfile.Minimum, v.EffectiveProfile.Standard
	if min.CPUs != 1 || min.CPUsSource != scheduler.SourceHost || min.MemoryMB != 512 || min.MemoryMBSource != scheduler.SourceGlobal {
		t.Errorf("effective minimum = %+v: CPU is the host's, memory follows the fleet", min)
	}
	if std.CPUs != 3 || std.CPUsSource != scheduler.SourceHost || std.MemoryMB != 8192 || std.MemoryMBSource != scheduler.SourceHost ||
		std.BurstMaxCPUs != 6 || std.BurstMaxCPUsSource != scheduler.SourceHost {
		t.Errorf("effective standard = %+v", std)
	}

	// No profile: the view says nothing was written, and the effective
	// figures are the fleet's with their source.
	plain := h.c.HostView(h.measuredHost("plain", 8, 16384, 4, enforcesEverything))
	if plain.RunnerProfile != nil || plain.Slots != 4 || plain.SlotsLimitedBy != "" {
		t.Errorf("an unprofiled host reads as profiled: %+v, %d slots limited by %q", plain.RunnerProfile, plain.Slots, plain.SlotsLimitedBy)
	}
	defCPUs, defMemory := h.cfg.Runners.DefaultRunnerSize()
	if got := plain.EffectiveProfile.Standard; got.CPUs != defCPUs || got.MemoryMB != defMemory || got.CPUsSource != scheduler.SourceGlobal {
		t.Errorf("an unprofiled host's standard = %+v, want the fleet's default from %q", got, scheduler.SourceGlobal)
	}
	if plain.EffectiveProfile.Standard.BurstMaxCPUs != 0 || plain.EffectiveProfile.Standard.BurstMaxCPUsSource != "" {
		t.Errorf("a host with no ceiling shows one: %+v", plain.EffectiveProfile.Standard)
	}
}

func TestWhatSetsAHostsSlotsIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     store.Host
		slots    int
		limit    string
		profiled bool
	}{
		{"no standard", store.Host{CPUs: 12, MemoryMB: 32768, Capacity: 4}, 4, "", false},
		{"CPU holds fewer than memory", store.Host{CPUs: 12, MemoryMB: 65536, Capacity: 8,
			RunnerProfile: store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 4, MemoryMB: 4096}}}, 2, "cpu", true},
		{"memory holds fewer than CPU", store.Host{CPUs: 32, MemoryMB: 16384, Capacity: 8,
			RunnerProfile: store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 1, MemoryMB: 8192}}}, 1, "memory", true},
		{"capacity is the lower", store.Host{CPUs: 12, MemoryMB: 32768, Capacity: 2,
			RunnerProfile: store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8192}}}, 2, "capacity", true},
		{"the two agree", store.Host{CPUs: 12, MemoryMB: 32768, Capacity: 3,
			RunnerProfile: store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8192}}}, 3, "", true},
		{"paused", store.Host{CPUs: 12, MemoryMB: 32768, Capacity: 0,
			RunnerProfile: store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8192}}}, 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slots, limit := hostSlots(&tc.host)
			if slots != tc.slots || limit != tc.limit {
				t.Fatalf("hostSlots = %d limited by %q, want %d limited by %q", slots, limit, tc.slots, tc.limit)
			}
		})
	}
}

// A pool that states its size is never raised by a host whose minimum is above
// it: the host is left out, the refusal is its own code with the limit named,
// and editing a host's minimum above the last size a pool could run is refused
// like any other edit that strands a pool.
func TestAHostMinimumAboveAPoolsStatedSizeLeavesTheHostOutAndRefusesTheStrandingEdit(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "small")
	pool.Resources = store.Resources{CPUs: 2, MemoryMB: 2048}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	host := h.measuredHost("big", 12, 32768, 8, enforcesEverything)

	// Before: the pool runs there. The proposed edit raises the host's minimum
	// above the pool's size, which would leave it nowhere.
	proposed := *host
	proposed.RunnerProfile.Minimum = store.RunnerSize{CPUs: 3}
	got, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Pool != pool.Name || got[0].Code != ExcludedProfile {
		t.Fatalf("strandings = %+v, want the one pool, refused for its profile", got)
	}
	if !strings.Contains(got[0].Reason, "minimum runner size is 3 CPU") || !strings.Contains(got[0].Reason, "above the 2 CPU") {
		t.Errorf("the refusal does not name the limit: %q", got[0].Reason)
	}

	// Saved anyway, the wizard's count and the pool's room say the same thing.
	saved := h.profile(host, proposed.RunnerProfile)
	fit, err := h.c.HostFit(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if fit.Count != 0 || len(fit.Excluded) != 1 || fit.Excluded[0].Code != ExcludedProfile {
		t.Fatalf("fit = %+v, want no host and one excluded for its profile", fit)
	}
	if !strings.Contains(fit.Detail, "big cannot run it") {
		t.Errorf("the explanation does not name the host: %q", fit.Detail)
	}
	room, err := h.c.PoolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(room.Hosts) != 1 || room.Hosts[0].ExcludedBy != ExcludedProfile || room.Hosts[0].Room != 0 ||
		!strings.Contains(room.Hosts[0].Excluded, "minimum runner size") {
		t.Fatalf("room = %+v, want the host listed with no room and the limit that did it", room.Hosts)
	}
	if room.Runners != 0 {
		t.Errorf("room counts %d runners on a host the pool is not allowed on", room.Runners)
	}
	if code, _ := HostRefusal(saved, pool); code != ExcludedProfile {
		t.Errorf("HostRefusal code = %q, want %q", code, ExcludedProfile)
	}
}

// A host the profile keeps a pool off is listed with its reason, and then takes
// no part in what the room says about where the pool lands. Counted as a host
// "promising more slots than it can back" it would send an operator to lower the
// capacity of a machine that is working as set, and counted among the hosts the
// pool "can land on" it would inflate the sentence about the room it has.
func TestAHostTheProfileKeepsAPoolOffIsNotOvercommittedAndNotAHostItLandsOn(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "small")
	pool.Resources = store.Resources{CPUs: 2, MemoryMB: 2048}
	pool.MinRunners, pool.MaxRunners = 0, 6
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Eight slots and a 3 CPU minimum above the pool's 2: kept off.
	h.profile(h.measuredHost("big", 12, 32768, 8, enforcesEverything), store.RunnerProfile{
		Minimum: store.RunnerSize{CPUs: 3},
	})
	// Two slots on 3.5 CPUs at 2 each: it can back one, so this one is
	// overcommitted, and is the only host that should be named as such.
	h.measuredHost("ok", 4, 8192, 2, enforcesEverything)

	room, err := h.c.PoolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(room.Hosts) != 2 || len(room.Placeable()) != 1 || room.Placeable()[0].Host != "ok" {
		t.Fatalf("hosts = %+v, placeable = %+v; want both listed and only ok placeable", room.Hosts, room.Placeable())
	}
	var over, above *Problem
	warnings := PoolRoomWarnings(pool, room)
	for i := range warnings {
		switch warnings[i].Code {
		case "pool.host_overcommitted":
			over = &warnings[i]
		case "pool.max_above_room":
			above = &warnings[i]
		}
	}
	if over == nil || !strings.Contains(over.Detail, "ok (2 slots, room for 1)") || strings.Contains(over.Detail, "big") {
		t.Errorf("overcommitted warning = %+v, want it to name ok and not big", over)
	}
	if above == nil || !strings.Contains(above.Detail, "the 1 host it can land on has room for 1 runner") {
		t.Errorf("max-above-room warning = %+v, want it to count the one host the pool can land on", above)
	}
}

// The standing problems are raised only where a profile is in play, so a fleet
// that never uses one sees the same list after the upgrade.
func TestThePoolsAProfileLeavesWithoutAHostAreAProblemAndAFleetDefaultIsSaid(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	heavy := h.profilePool(inst, "heavy", 0)
	heavy.Resources.MinCPUs = 3
	if err := h.st.UpdatePool(h.ctx, heavy); err != nil {
		t.Fatal(err)
	}
	h.profile(h.measuredHost("small-a", 4, 16384, 4, enforcesEverything),
		store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 1.5, MemoryMB: 4096}})
	h.profile(h.measuredHost("small-b", 4, 16384, 4, enforcesEverything),
		store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 1.5, MemoryMB: 4096}})

	got := h.problemsByCode()
	p, ok := got["pool.no_eligible_host"]
	if !ok {
		t.Fatalf("no pool.no_eligible_host among %v", h.problemCodes())
	}
	for _, want := range []string{"small-a: its standard runner is 1.5 CPU", "small-b:", "below this pool's minimum of 3 CPU"} {
		if !strings.Contains(p.Detail, want) {
			t.Errorf("the problem %q does not say %q", p.Detail, want)
		}
	}
	if p.TargetKind != "pool" || p.TargetID != heavy.ID {
		t.Errorf("the problem is about %s %s, want the pool", p.TargetKind, p.TargetID)
	}
	if _, also := got["pool.profile_default"]; also {
		t.Error("the default-size note was raised for a pool no host can run")
	}

	// A host that names no standard runs a profile pool at the fleet's default,
	// and the note says which hosts and what the figure is.
	free := h.profilePool(inst, "free", 0)
	h.measuredHost("plain", 8, 16384, 4, enforcesEverything)
	got = h.problemsByCode()
	note, ok := got["pool.profile_default"]
	if !ok || note.TargetID != free.ID || note.Severity != config.SeverityInfo {
		t.Fatalf("pool.profile_default = %+v (present %v), want an info entry about the free pool", note, ok)
	}
	if !strings.Contains(note.Detail, "plain") || !strings.Contains(note.Detail, "2 CPU and 4 GB") {
		t.Errorf("the note does not name the host and the default: %q", note.Detail)
	}
}

func TestAFleetWithNoProfilesRaisesNeitherProfileProblem(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.Resources = store.Resources{CPUs: 64, MemoryMB: 262144} // no host can hold it, for reasons that are not a profile
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.measuredHost("plain", 8, 16384, 4, enforcesEverything)
	for code := range h.problemsByCode() {
		if code == "pool.no_eligible_host" || code == "pool.profile_default" {
			t.Fatalf("%s raised on a fleet that has not used a runner profile", code)
		}
	}
}

// A throttle takes CPU off every runner on a host by one factor, and a host's
// minimum is the least a runner there may be given: the controller raises the
// factor it sends so the smallest limited runner stays at the minimum, and
// stands the CPU part of the throttle down for one already under it.
func TestTheThrottleNeverTakesARunnerBelowTheHostsMinimum(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.measuredHost("big", 12, 32768, 8, enforcesEverything)
	if err := h.st.SetHostThrottle(h.ctx, host.ID, store.HostThrottle{Level: 2}, 0); err != nil {
		t.Fatal(err)
	}
	h.sharedRunner(pool, host, store.RunnerBusy, 1.5)
	h.sharedRunner(pool, host, store.RunnerBusy, 3)

	factor := func(min float64) float64 {
		t.Helper()
		got := h.profile(host, store.RunnerProfile{Minimum: store.RunnerSize{CPUs: min}})
		return h.c.throttleDirective(h.ctx, got).CPUFactor
	}
	if got := factor(0); got != 0.5 {
		t.Fatalf("with no minimum the factor is %v, want the ladder's 0.5", got)
	}
	// 1 CPU of a 1.5 CPU runner is two thirds.
	if got := factor(1); got < 0.666 || got > 0.667 {
		t.Fatalf("with a 1 CPU minimum the factor is %v, want 2/3 so the 1.5 CPU runner keeps 1", got)
	}
	// A minimum the ladder already respects changes nothing.
	if got := factor(0.5); got != 0.5 {
		t.Fatalf("with a 0.5 CPU minimum the factor is %v, want the ladder's 0.5", got)
	}
	// A runner already under the minimum cannot be helped by a factor: the CPU
	// part of the throttle stands down while it runs.
	if got := factor(2); got != 1 {
		t.Fatalf("with a 2 CPU minimum above a 1.5 CPU runner the factor is %v, want 1", got)
	}
	if d := h.c.throttleDirective(h.ctx, h.reread(t, host.ID)); d.Level != 2 {
		t.Fatalf("the level is %d, want the rung still reported", d.Level)
	}
}

func TestAnUnthrottledHostAndAHostWithNoMinimumAreSentTheLaddersOwnFactor(t *testing.T) {
	h := newHarness(t)
	host := h.profile(h.measuredHost("big", 12, 32768, 8, enforcesEverything),
		store.RunnerProfile{Minimum: store.RunnerSize{CPUs: 2}})
	if d := h.c.throttleDirective(h.ctx, host); d.CPUFactor != 1 || d.Level != 0 {
		t.Fatalf("an unthrottled host is sent %+v, want level 0 and factor 1 whatever its minimum", d)
	}
}

// A job records the size of the runner that took it, once, so the job can say
// how big its machine was after the runner is gone -- and the size is the one
// the runner row recorded, not what today's profile would give.
func TestAJobIsStampedWithTheSizeOfTheRunnerThatTookIt(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.profilePool(inst, "linux-x64", 0)
	host := h.profile(h.measuredHost("big", 12, 32768, 8, enforcesEverything), bigProfile)
	labels := []string{"self-hosted", "linux", "x64", "demo"}

	h.deliverJob(jobEvent{Action: "queued", JobID: 9401, Labels: labels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.c.lifecycleCalls.Wait()
	h.advance(5 * time.Second)
	if _, err := h.c.PollTasks(h.ctx, host.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	r := h.onlyRunner()
	h.deliverJob(jobEvent{Action: "in_progress", JobID: 9401, RunnerName: r.Name, Labels: labels})

	// The operator resizes the host afterwards; the job keeps what it ran on.
	h.profile(host, store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 6, MemoryMB: 16384}})
	h.deliverJob(jobEvent{Action: "completed", JobID: 9401, RunnerName: r.Name, Labels: labels, Conclusion: "success"})

	got, err := h.st.GetJobByGitHubID(h.ctx, 9401)
	if err != nil {
		t.Fatal(err)
	}
	if got.GrantedCPUs != 3 || got.GrantedMemoryMB != 8192 || got.GrantedSource != store.AllocationFromProfile {
		t.Fatalf("granted %v CPUs and %d MB from %q, want 3 and 8192 from %q",
			got.GrantedCPUs, got.GrantedMemoryMB, got.GrantedSource, store.AllocationFromProfile)
	}
	if view := NewJobView(got, "linux-x64"); view.GrantedCPUs != 3 || view.GrantedMemoryMB != 8192 || view.GrantedSource != "profile" {
		t.Fatalf("the job view says %v, %d, %q", view.GrantedCPUs, view.GrantedMemoryMB, view.GrantedSource)
	}
}

// The guarantee a docker-in-docker job ran with is both containers' together,
// where somebody typed the figure, and the one slot the pair split where the
// host decided it.
func TestAGrantedSizeCountsTheDaemonOnlyWhereTheFigureWasTyped(t *testing.T) {
	dind := &store.Pool{DockerMode: store.DockerDinD, Resources: store.Resources{CPUs: 2, MemoryMB: 4096}}
	runner := func(source string) *store.Runner {
		return &store.Runner{AllocatedCPUs: 2, AllocatedMemoryMB: 4096, AllocationSource: source}
	}
	for _, tc := range []struct {
		name   string
		pool   *store.Pool
		source string
		cpus   float64
		memory int64
	}{
		{"typed on a daemon pool", dind, store.AllocationFromPool, 4, 8192},
		{"reduced from a typed size", dind, store.AllocationReduced, 4, 8192},
		{"a share the pair splits", &store.Pool{DockerMode: store.DockerDinD}, store.AllocationFromHost, 2, 4096},
		{"a profile's standard the pair splits", &store.Pool{DockerMode: store.DockerDinD, SizeFromProfile: true}, store.AllocationFromProfile, 2, 4096},
		{"no daemon", &store.Pool{Resources: store.Resources{CPUs: 2, MemoryMB: 4096}}, store.AllocationFromPool, 2, 4096},
		{"no pool left", nil, store.AllocationFromPool, 2, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cpus, memory := grantedSize(tc.pool, runner(tc.source))
			if cpus != tc.cpus || memory != tc.memory {
				t.Fatalf("grantedSize = %v CPUs and %d MB, want %v and %d", cpus, memory, tc.cpus, tc.memory)
			}
		})
	}
}

func TestAPoolSizedByItsHostReadsAsProfileOnTheAPI(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.profilePool(inst, "linux-x64", 0)
	if got := PoolSizing(pool); got != SizingProfile {
		t.Fatalf("PoolSizing = %q, want %q", got, SizingProfile)
	}
	r, err := h.c.PoolRenderer(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := r.View(pool)
	cpus, memoryMB := h.cfg.Runners.DefaultRunnerSize()
	if !v.SizeFromProfile || v.Sizing != "profile" || v.FleetStandard == nil ||
		v.FleetStandard.CPUs != cpus || v.FleetStandard.MemoryMB != memoryMB {
		t.Fatalf("the pool view = sizing %q, from profile %v, fleet standard %+v", v.Sizing, v.SizeFromProfile, v.FleetStandard)
	}

	// Another mode carries no fleet standard, and its minimum says whose it is.
	plain := h.pool(inst, "plain")
	plain.Resources.MinCPUs = 1
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Runners.MinimumMemoryMB = 1024 })
	r, _ = h.c.PoolRenderer(h.ctx)
	pv := r.View(plain)
	if pv.FleetStandard != nil || pv.SizeFromProfile {
		t.Errorf("an automatic pool carries a profile's fleet standard: %+v", pv.FleetStandard)
	}
	if pv.EffectiveMinimum.CPUsSource != scheduler.SourcePool || pv.EffectiveMinimum.MemoryMBSource != scheduler.SourceGlobal {
		t.Errorf("the minimum's sources = %q and %q, want pool and global", pv.EffectiveMinimum.CPUsSource, pv.EffectiveMinimum.MemoryMBSource)
	}
}

// One pool, sized three ways on three hosts, each figure with its source: the
// row the pool's page reads to see why a runner there is the size it is.
func TestAPoolHostRowSaysWhereEachSizeCameFrom(t *testing.T) {
	h := newHarness(t)
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Runners.MinimumCPUs = 0.5 })
	inst := h.installation()
	pool := h.profilePool(inst, "linux-x64", 0)
	pool.Resources.MinMemoryMB = 1024
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic, MaxCPUs: 8}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.profile(h.measuredHost("big", 12, 32768, 8, enforcesEverything), store.RunnerProfile{
		Minimum:  store.RunnerSize{CPUs: 2},
		Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8192, BurstMaxCPUs: 5},
	})
	h.measuredHost("plain", 8, 16384, 4, enforcesEverything)

	room, err := h.c.PoolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]PoolHostRoom{}
	for _, r := range room.Hosts {
		rows[r.Host] = r
	}
	big, plain := rows["big"].Sizing, rows["plain"].Sizing
	if big.Standard.CPUs != 3 || big.StandardCPUsSource != scheduler.SourceHost || big.StandardMemoryMBSource != scheduler.SourceHost {
		t.Errorf("big's standard = %+v from %q and %q", big.Standard, big.StandardCPUsSource, big.StandardMemoryMBSource)
	}
	if big.Floor.CPUs != 2 || big.FloorCPUsSource != scheduler.SourceHost || big.Floor.MemoryMB != 1024 || big.FloorMemoryMBSource != scheduler.SourcePool {
		t.Errorf("big's floor = %+v from %q and %q: the host's 2 CPU beats the fleet's 0.5, the pool's own memory stands",
			big.Floor, big.FloorCPUsSource, big.FloorMemoryMBSource)
	}
	if big.CeilingCPUs != 5 || big.CeilingSource != scheduler.SourceHost {
		t.Errorf("big's ceiling = %v from %q, want the host's 5 under the pool's 8", big.CeilingCPUs, big.CeilingSource)
	}
	if plain.StandardCPUsSource != scheduler.SourceGlobal || plain.StandardMemoryMBSource != scheduler.SourceGlobal {
		t.Errorf("plain's standard comes from %q and %q, want the fleet's default", plain.StandardCPUsSource, plain.StandardMemoryMBSource)
	}
	if plain.Floor.CPUs != 0.5 || plain.FloorCPUsSource != scheduler.SourceGlobal {
		t.Errorf("plain's floor = %v from %q, want the fleet's 0.5", plain.Floor.CPUs, plain.FloorCPUsSource)
	}
	if plain.CeilingCPUs != 8 || plain.CeilingSource != scheduler.SourcePool {
		t.Errorf("plain's ceiling = %v from %q, want the pool's 8", plain.CeilingCPUs, plain.CeilingSource)
	}
}

func TestOnlyKnownHostOrdersPassValidation(t *testing.T) {
	for _, ok := range []string{"", "headroom", "largest_standard", "best_fit"} {
		cfg := config.Default()
		cfg.Scheduler.HostOrder = ok
		for _, f := range cfg.Validate() {
			if f.Code == "scheduler.host_order" {
				t.Errorf("%q was refused: %s", ok, f.Title)
			}
		}
	}
	cfg := config.Default()
	cfg.Scheduler.HostOrder = "spread"
	found := false
	for _, f := range cfg.Validate() {
		found = found || f.Code == "scheduler.host_order"
	}
	if !found {
		t.Error("an unknown host order was accepted")
	}
}
