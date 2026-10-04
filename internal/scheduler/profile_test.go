package scheduler

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The fleet the documentation works through: one 12-CPU machine and two
// 4-CPU ones. The memory figures are what is left after the reserve, so the
// arithmetic in the tests is the arithmetic on the page: 11.4 CPU and 30 GB on
// the big host, 3.5 CPU and 15 GB on each small one.
func bigHost(id string) *store.Host {
	h := sized(id, 8, 12, hostFor(30*1024), 100000)
	h.RunnerProfile = store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 3, MemoryMB: 8 * 1024}}
	return h
}

func smallHost(id string) *store.Host {
	h := sized(id, 8, 4, hostFor(15*1024), 100000)
	h.RunnerProfile = store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 1.5, MemoryMB: 4 * 1024}}
	return h
}

// profilePool is a pool that takes its size from the host, with the fleet's
// default size on the copy the way the controller hands it over.
func profilePool(name string, minCPUs float64, minMemoryMB int64) *store.Pool {
	p := testPool(name, name)
	p.SizeFromProfile = true
	p.FleetStandard = store.RunnerSize{CPUs: 2, MemoryMB: 4096}
	p.Resources.MinCPUs, p.Resources.MinMemoryMB = minCPUs, minMemoryMB
	return p
}

// Nothing may change for a host nobody has written a profile for, or for a
// pool that does not ask about one: that is the whole compatibility promise,
// so it is stated as a property of the functions rather than left to the
// existing tests happening to cover it.
func TestAHostWithNoProfileIsSizedExactlyAsBefore(t *testing.T) {
	h := sized("plain", 4, 8, hostFor(16*1024), 100000)
	for _, p := range []*store.Pool{
		testPool("automatic", "a"),
		limited("fixed", 2, 2048),
		withMinimum("reducible", 4, 8192, 1, 2048),
		automatic("auto-min", 1, 1024),
	} {
		if got := sizedOn(p, h); got != p {
			t.Errorf("%s: an unprofiled host copied the pool", p.Name)
		}
		if ex := ExcludedBySize(h, p); ex != nil {
			t.Errorf("%s: an unprofiled host excluded it: %s", p.Name, ex.Reason)
		}
		if cpus, memory := StandardSize(p, h); cpus != 0 || memory != 0 {
			t.Errorf("%s: a pool that does not take its size from the host was given a standard of %g and %d", p.Name, cpus, memory)
		}
	}
	if got := h.Slots(); got != 4 {
		t.Errorf("an unprofiled host has %d slots, want its capacity of 4", got)
	}
	if got := BurstLimit(testPool("a", "a"), h); got != 0 {
		t.Errorf("an unprofiled host and a pool with no ceiling gave a ceiling of %g", got)
	}
}

// A profile pool is sized by the host: the standard it names, and the fleet's
// default where it names none. A docker-in-docker pair is charged one standard
// and splits it, exactly as a share is, so the size the host names is what the
// job gets between its two containers rather than twice that.
func TestAProfilePoolIsGivenTheHostsStandardSize(t *testing.T) {
	h := bigHost("big")
	p := profilePool("heavy", 0, 0)

	if got := Reserve(p, h); got.CPUs != 3 || got.MemoryMB != 8*1024 {
		t.Fatalf("Reserve = %g CPU and %d MB, want the host's standard of 3 and 8192", got.CPUs, got.MemoryMB)
	}
	res, source := Allocation(p, h, true)
	if res.CPUs != 3 || res.MemoryMB != 8*1024 || source != store.AllocationFromProfile {
		t.Fatalf("Allocation = %+v from %q, want 3 CPU and 8192 MB from the profile", res, source)
	}

	p.DockerMode = store.DockerDinD
	if got := Reserve(p, h); got.CPUs != 3 || got.MemoryMB != 8*1024 {
		t.Fatalf("a docker-in-docker pair is charged %g CPU and %d MB, want one standard of 3 and 8192 -- the pair splits it", got.CPUs, got.MemoryMB)
	}
}

func TestAProfilePoolOnAHostWithNoStandardGetsTheFleetsDefault(t *testing.T) {
	h := sized("plain", 4, 12, hostFor(30*1024), 100000)
	p := profilePool("heavy", 0, 0)

	got := Reserve(p, h)
	if got.CPUs != 2 || got.MemoryMB != 4096 {
		t.Fatalf("Reserve = %g CPU and %d MB, want the fleet's default of 2 and 4096", got.CPUs, got.MemoryMB)
	}
	if _, source := Allocation(p, h, true); source != store.AllocationFromProfile {
		t.Fatalf("the source is %q, want profile: the size is the profile's answer even where the fleet's default stands in for it", source)
	}

	// A host that names only CPU keeps the fleet's default for memory.
	h.RunnerProfile.Standard = store.RunnerStandard{CPUs: 4}
	got = Reserve(p, h)
	if got.CPUs != 4 || got.MemoryMB != 4096 {
		t.Fatalf("Reserve = %g CPU and %d MB, want 4 from the host and 4096 from the fleet", got.CPUs, got.MemoryMB)
	}

	// Without the fleet's figure on the copy -- a caller that did not carry it --
	// the pool is sized by a slot's share, which is what it was before.
	p.FleetStandard = store.RunnerSize{}
	h.RunnerProfile = store.RunnerProfile{}
	want := Reserve(testPool("share", "share"), h)
	if got := Reserve(p, h); got != want {
		t.Fatalf("Reserve = %+v, want a slot's share %+v when neither the host nor the fleet named a size", got, want)
	}
}

// The slot count the standard gives is the divisor of every share, so an
// automatic pool on the same host sees the same slots a profile pool does and
// the host reads as full at the same point for both.
func TestTheSlotsAProfileGivesAreWhatAnAutomaticPoolIsDividedBy(t *testing.T) {
	h := bigHost("big")
	if got := h.Slots(); got != 3 {
		t.Fatalf("Slots() = %d, want 3", got)
	}
	share := HostShare(h)
	if share.CPUs != 3.8 || share.MemoryMB != 10*1024 {
		t.Fatalf("HostShare = %+v, want 11.4 CPU and 30 GB cut into 3: 3.8 and 10240", share)
	}
	if got := Reserve(testPool("auto", "auto"), h); got.CPUs < 3.79 || got.CPUs > 3.81 || got.MemoryMB != 10*1024 {
		t.Fatalf("an automatic pool is charged %+v, want the same share", got)
	}
}

// The host's minimum is the larger of the two: it raises a pool's own and never
// lowers it, and a share too thin for the pool's minimum is raised to it.
func TestAHostsMinimumRaisesAPoolsFloorAndNeverLowersIt(t *testing.T) {
	h := sized("thin", 8, 8, hostFor(16*1024), 100000) // a share of 0.94 CPU and 2 GB
	h.RunnerProfile.Minimum = store.RunnerSize{CPUs: 2, MemoryMB: 4096}

	p := automatic("auto", 0.5, 512)
	if got := Reserve(p, h); got.CPUs != 2 || got.MemoryMB != 4096 {
		t.Fatalf("Reserve = %g CPU and %d MB, want the host's larger minimum, 2 and 4096, where the share is thinner", got.CPUs, got.MemoryMB)
	}

	h.RunnerProfile.Minimum = store.RunnerSize{CPUs: 0.5, MemoryMB: 256}
	p = automatic("auto", 1, 1024)
	if got := sizedOn(p, h); got.Resources.MinCPUs != 1 || got.Resources.MinMemoryMB != 1024 {
		t.Fatalf("a host's smaller minimum lowered the pool's to %g and %d", got.Resources.MinCPUs, got.Resources.MinMemoryMB)
	}

	// The stored pool is never touched: only the copy is raised.
	h.RunnerProfile.Minimum = store.RunnerSize{CPUs: 4, MemoryMB: 8192}
	p = automatic("auto", 1, 1024)
	_ = sizedOn(p, h)
	if p.Resources.MinCPUs != 1 || p.Resources.MinMemoryMB != 1024 {
		t.Fatalf("raising the minimum wrote %g and %d into the pool itself", p.Resources.MinCPUs, p.Resources.MinMemoryMB)
	}
}

// Explicit pool values are unchanged: a pool that states its size is not made
// bigger by a host whose minimum is above it. The host is left out, and says
// which limit did it.
func TestAPoolThatStatesItsSizeIsNeverRaisedByAHostMinimum(t *testing.T) {
	h := sized("picky", 4, 12, hostFor(30*1024), 100000)
	h.RunnerProfile.Minimum = store.RunnerSize{CPUs: 3}
	p := limited("small", 2, 2048)

	ex := ExcludedBySize(h, p)
	if ex == nil || ex.Code != ExclusionHostMinimum || ex.Field != "cpu" {
		t.Fatalf("ExcludedBySize = %+v, want the host's CPU minimum", ex)
	}
	for _, want := range []string{"minimum runner size is 3 CPU", "above the 2 CPU this pool gives", "never given more than it states"} {
		if !strings.Contains(ex.Reason, want) {
			t.Errorf("the reason %q does not say %q", ex.Reason, want)
		}
	}
	if HostFits(h, p) || HostCouldRun(h, p) {
		t.Fatal("the host was left in for a pool whose size it is above")
	}
	if !strings.Contains(HostShortfall(h, p), "minimum runner size is 3 CPU") {
		t.Fatalf("the shortfall does not say what excluded the host: %q", HostShortfall(h, p))
	}
	if room := HostRoomFor(h, p); room.Room != 0 || room.LimitedBy != "profile" {
		t.Fatalf("HostRoomFor = %+v, want no room, limited by the profile", room)
	}

	// At or below the pool's size the host is fine, and the runner is the
	// pool's own size: the minimum is a floor under a reduction, not a size.
	h.RunnerProfile.Minimum = store.RunnerSize{CPUs: 2, MemoryMB: 1024}
	if ExcludedBySize(h, p) != nil || !HostFits(h, p) {
		t.Fatal("a minimum equal to the pool's size excluded the host")
	}
	if got := Reserve(p, h); got.CPUs != 2 || got.MemoryMB != 2048 {
		t.Fatalf("Reserve = %g CPU and %d MB, want the pool's own 2 and 2048", got.CPUs, got.MemoryMB)
	}

	memory := sized("memory", 4, 12, hostFor(30*1024), 100000)
	memory.RunnerProfile.Minimum = store.RunnerSize{MemoryMB: 4096}
	if ex := ExcludedBySize(memory, p); ex == nil || ex.Field != "memory" || !strings.Contains(ex.Reason, "4 GB") {
		t.Fatalf("a memory minimum above the pool's gave %+v", ex)
	}
}

// A host is eligible for a profile pool only if its standard meets the pool's
// floor. The standard may be the host's own or the fleet's default standing in
// for it, and the sentence says which, because the fix is a different field.
func TestAProfilePoolIsNotPlacedWhereTheStandardIsBelowItsFloor(t *testing.T) {
	p := profilePool("heavy", 3, 6*1024)

	small := smallHost("small")
	ex := ExcludedBySize(small, p)
	if ex == nil || ex.Code != ExclusionHostStandard || ex.Field != "cpu" {
		t.Fatalf("ExcludedBySize = %+v, want the small host's CPU standard", ex)
	}
	for _, want := range []string{"its standard runner is 1.5 CPU", "below this pool's minimum of 3 CPU"} {
		if !strings.Contains(ex.Reason, want) {
			t.Errorf("the reason %q does not say %q", ex.Reason, want)
		}
	}
	if ExcludedBySize(bigHost("big"), p) != nil {
		t.Fatal("the big host's standard meets the floor and was excluded")
	}

	// No standard of its own: the fleet's default of 2 CPU is below the floor.
	plain := sized("plain", 4, 12, hostFor(30*1024), 100000)
	ex = ExcludedBySize(plain, p)
	if ex == nil || !strings.Contains(ex.Reason, "names no standard runner size, so the fleet's default of 2 CPU applies") {
		t.Fatalf("a host with no standard gave %+v", ex)
	}

	// Memory is the field that does it when CPU is fine.
	memory := bigHost("memory")
	memory.RunnerProfile.Standard.MemoryMB = 4096
	ex = ExcludedBySize(memory, p)
	if ex == nil || ex.Field != "memory" || !strings.Contains(ex.Reason, "below this pool's minimum of 6 GB") {
		t.Fatalf("a memory standard under the floor gave %+v", ex)
	}

	// The host's own minimum above its own standard is the host contradicting
	// itself, and the sentence blames the host's minimum rather than the pool's.
	odd := bigHost("odd")
	odd.RunnerProfile.Minimum = store.RunnerSize{CPUs: 5}
	free := profilePool("free", 0, 0)
	ex = ExcludedBySize(odd, free)
	if ex == nil || !strings.Contains(ex.Reason, "below this host's own minimum of 5 CPU") {
		t.Fatalf("a host minimum above its standard gave %+v", ex)
	}
	if HostFits(small, p) {
		t.Fatal("HostFits kept a host the pool is excluded from")
	}
}

// A profile pool never has a reduced runner: it is excluded where the standard
// is below the floor instead, so no runner is ever given less than the pool's
// minimum, and none given less than the host's standard unless the host is
// busy -- where the existing reduction down to the floor still applies.
func TestAProfilePoolIsReducedOnlyDownToItsFloorOnABusyHost(t *testing.T) {
	h := bigHost("big")
	p := profilePool("heavy", 2, 4*1024)
	r := &store.Runner{ID: "r1", PoolID: p.ID, HostID: h.ID, State: store.RunnerBusy, AllocatedCPUs: 3, AllocatedMemoryMB: 8 * 1024, AllocationSource: store.AllocationFromProfile}
	h.ActiveRunners = 1
	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, map[string][]*store.Runner{p.ID: {r}}, now)

	// Plenty of room: the second runner is a whole standard.
	got := hs.placeAvoiding(p, 1, nil)
	if len(got) != 1 || got[0].size != nil {
		t.Fatalf("placed %+v, want one runner at the standard size", got)
	}
}

// What the scheduler says to an operator whose pool has nowhere to go because
// of a profile names the limit and what to change, and does not call the fleet
// full or the host too small.
func TestAPoolExcludedByEveryProfileSaysWhichLimitDidIt(t *testing.T) {
	p := profilePool("heavy", 3, 0)
	hosts := []*store.Host{smallHost("small-a"), smallHost("small-b")}
	hs := newHostSet(hosts, []*store.Pool{p}, nil, now)

	if got := hs.place(p, 1); len(got) != 0 {
		t.Fatalf("placed %v on hosts whose standard is below the pool's floor", got)
	}
	b := hs.why(p)
	for _, want := range []string{"2 excluded by their runner profile", "small-a: its standard runner is 1.5 CPU"} {
		if !strings.Contains(b.what, want) {
			t.Errorf("the explanation %q does not say %q", b.what, want)
		}
	}
	if !strings.Contains(b.fix, "minimum or standard runner size") {
		t.Errorf("the fix %q does not name the fields to change", b.fix)
	}
	if b.atCapacity {
		t.Error("a pool excluded by profile was called at capacity, which is a reason to wait")
	}
	if !b.noEligibleHost {
		t.Error("a pool excluded by profile was not reported as having no host that could ever run it")
	}
}

// The worked example the documentation walks through, so that the page and
// the code cannot disagree about it.
func TestTheMixedFleetExample(t *testing.T) {
	big, a, b := bigHost("big"), smallHost("small-a"), smallHost("small-b")

	if big.Slots() != 3 || a.Slots() != 2 || b.Slots() != 2 {
		t.Fatalf("slots = %d, %d, %d, want 3, 2, 2", big.Slots(), a.Slots(), b.Slots())
	}

	anywhere := profilePool("ci", 0, 0)
	heavy := profilePool("heavy", 3, 0)
	for _, h := range []*store.Host{big, a, b} {
		if room := HostRoomFor(h, anywhere); room.Room != h.Slots() {
			t.Errorf("%s: room for %d of the pool with no floor, want its %d slots (%+v)", h.ID, room.Room, h.Slots(), room)
		}
	}
	if room := HostRoomFor(big, heavy); room.Room != 3 {
		t.Errorf("big: room for %d of the pool with a 3 CPU floor, want 3", room.Room)
	}
	for _, h := range []*store.Host{a, b} {
		if room := HostRoomFor(h, heavy); room.Room != 0 || room.LimitedBy != "profile" {
			t.Errorf("%s: room %+v for the pool with a 3 CPU floor, want none, excluded by its profile", h.ID, room)
		}
	}

	// The third pool states a small size, and the large host's own minimum of
	// 2 CPU is what keeps it off that machine: a pool that states its size is
	// never given more than it states, so the host is left out rather than the
	// runner made bigger. The small hosts hold two of its runners each.
	big.RunnerProfile.Minimum = store.RunnerSize{CPUs: 2}
	lint := limited("lint", 1, 2048)
	if ex := ExcludedBySize(big, lint); ex == nil || ex.Code != ExclusionHostMinimum {
		t.Errorf("big: a 2 CPU minimum above the 1 CPU the pool states should keep it off, got %+v", ex)
	}
	if room := HostRoomFor(big, lint); room.Room != 0 || room.LimitedBy != "profile" {
		t.Errorf("big: room %+v for the pool that states 1 CPU, want none, excluded by its profile", room)
	}
	for _, h := range []*store.Host{a, b} {
		if room := HostRoomFor(h, lint); room.Room != 2 {
			t.Errorf("%s: room for %d of the pool that states 1 CPU, want its 2 slots (%+v)", h.ID, room.Room, room)
		}
	}
	// The host's 2 CPU minimum is below the 3 CPU floor of the second pool, so
	// the pool's own floor is the larger of the two and the big host still runs it.
	if room := HostRoomFor(big, heavy); room.Room != 3 {
		t.Errorf("big: room for %d of the pool with a 3 CPU floor once the host has a 2 CPU minimum, want 3", room.Room)
	}
	// And it is the same seven slots the page counts: 3 and 2 and 2.
	if total := big.Slots() + a.Slots() + b.Slots(); total != 7 {
		t.Errorf("the fleet has %d slots, the page says 7", total)
	}
}

// Slots track the host's profile through the pass: the pass stops filling a
// host at the derived count, not at the capacity set beside it.
func TestAPassFillsAProfiledHostToItsDerivedSlotsAndNoFurther(t *testing.T) {
	big := bigHost("big") // capacity 8, but the machine holds 3 standard runners
	p := profilePool("ci", 0, 0)
	hs := newHostSet([]*store.Host{big}, []*store.Pool{p}, nil, now)
	if got := hs.place(p, 8); len(got) != 3 {
		t.Fatalf("placed %d runners, want the 3 the machine holds", len(got))
	}
}

func TestTheHostOrderChoosesBetweenHostsThatAllFit(t *testing.T) {
	p := profilePool("ci", 0, 0)
	pick := func(order string, loadBig int) string {
		big, small := bigHost("a-big"), smallHost("b-small")
		big.ActiveRunners = loadBig
		var runners []*store.Runner
		for i := range loadBig {
			r := testRunner("r"+string(rune('0'+i)), p, store.RunnerBusy, 0)
			r.HostID = big.ID
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource = 3, 8*1024, store.AllocationFromProfile
			runners = append(runners, r)
		}
		hs := newHostSet([]*store.Host{big, small}, []*store.Pool{p}, map[string][]*store.Runner{p.ID: runners}, now)
		hs.order = order
		got := hs.place(p, 1)
		if len(got) != 1 {
			t.Fatalf("%s with %d on the big host: placed %v", order, loadBig, got)
		}
		return got[0]
	}
	for _, tc := range []struct {
		order   string
		loadBig int
		want    string
	}{
		// Both hosts empty: headroom takes the larger machine, and so does
		// the largest standard; best fit fills the smaller first.
		{HostOrderHeadroom, 0, "a-big"},
		{"", 0, "a-big"},
		{HostOrderLargestStandard, 0, "a-big"},
		{HostOrderBestFit, 0, "b-small"},
		// The big host already carries two: headroom now prefers the empty
		// small one, while the largest standard still goes where the big
		// runners are and best fit keeps packing the host it started.
		{HostOrderHeadroom, 2, "b-small"},
		{HostOrderLargestStandard, 2, "a-big"},
		{HostOrderBestFit, 2, "a-big"},
	} {
		if got := pick(tc.order, tc.loadBig); got != tc.want {
			t.Errorf("order %q with %d runners on the big host placed on %s, want %s", tc.order, tc.loadBig, got, tc.want)
		}
	}
}

func TestOnlyKnownHostOrdersAreValid(t *testing.T) {
	for _, ok := range []string{"", "headroom", "largest_standard", "best_fit"} {
		if !ValidHostOrder(ok) {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{"spread", "Headroom", "largest", "random"} {
		if ValidHostOrder(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The host's ceiling can only lower the pool's, and an unset side does not
// count: whoever owns the machine has the last word on how much of it one job
// may take.
func TestTheBurstCeilingIsTheSmallerOfThePoolsAndTheHosts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pool, host float64
		want       float64
	}{
		{"neither says", 0, 0, 0},
		{"only the pool", 8, 0, 8},
		{"only the host", 0, 6, 6},
		{"the host is lower", 8, 6, 6},
		{"the pool is lower", 4, 6, 4},
		{"equal", 5, 5, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPool("elastic", "elastic")
			p.CPUBurst.MaxCPUs = tc.pool
			h := bigHost("big")
			h.RunnerProfile.Standard.BurstMaxCPUs = tc.host
			if got := BurstLimit(p, h); got != tc.want {
				t.Fatalf("BurstLimit = %g, want %g", got, tc.want)
			}
		})
	}
}

// The floor is the factor that leaves the smallest limited container exactly
// the minimum, capped at one: a container already under it cannot be helped by
// a factor, so the CPU part of the throttle stands down for it.
func TestTheThrottleFloorFactorProtectsTheSmallestLimitedContainer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		minimum    float64
		containers []float64
		want       float64
	}{
		{"no minimum", 0, []float64{1.5, 3}, 0},
		{"nothing limited to protect", 1, nil, 0},
		{"unlimited containers have nothing to scale", 1, []float64{0, 0}, 0},
		{"the smallest sets it", 1, []float64{3, 1.5, 6}, 1.0 / 1.5},
		{"one container", 1, []float64{4}, 0.25},
		{"a container already at the minimum", 2, []float64{2, 4}, 1},
		{"a container under the minimum stands the factor down", 2, []float64{1.5, 4}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ThrottleFloorFactor(tc.minimum, tc.containers)
			if got < tc.want-1e-9 || got > tc.want+1e-9 {
				t.Fatalf("ThrottleFloorFactor(%v, %v) = %v, want %v", tc.minimum, tc.containers, got, tc.want)
			}
		})
	}
}

// The percentage on the host card is the ladder's, and the host's own minimum
// is what can make a runner's share larger than it; the sentence says so where
// it can be true and not otherwise.
func TestAThrottledHostWithAMinimumSaysItWillNotGoBelowIt(t *testing.T) {
	h := sized("big", 8, 12, hostFor(30*1024), 100000)
	h.ActiveRunners = 2
	h.Throttle = store.HostThrottle{Level: 2, Reason: "CPU stayed above 85%"}

	if got := ThrottleReason(h); strings.Contains(got, "never below") {
		t.Fatalf("a host with no minimum says %q", got)
	}
	h.RunnerProfile.Minimum = store.RunnerSize{CPUs: 1.5}
	if got := ThrottleReason(h); !strings.Contains(got, "at 50% of it, but never below this host's minimum of 1.5 CPU a runner") {
		t.Fatalf("the sentence does not name the host's floor: %q", got)
	}
	// And the slot count it quotes is the profile's, not the capacity beside it.
	h.RunnerProfile.Standard = store.RunnerStandard{CPUs: 3, MemoryMB: 8 * 1024}
	if got := ThrottleReason(h); !strings.Contains(got, "throttled to 1 of 3 slots") {
		t.Fatalf("the slots are not the profile's: %q", got)
	}
}
