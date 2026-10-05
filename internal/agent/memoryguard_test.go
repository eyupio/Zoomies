package agent

import (
	"strings"
	"testing"
	"time"
)

// roomy is a container with everything the guard could ask for: a host with
// plenty free, a pool with plenty in it and a ceiling well above the limit. A
// test changes the one thing it is about.
func roomy() MemoryGuardInput {
	return MemoryGuardInput{
		UsageMB: 2000, LimitMB: 4096,
		HeldMB: 4096, CeilingMB: 6144,
		PoolMB:      4096,
		AvailableMB: 12000, FloorMB: 2000, AvailableKnown: true,
	}
}

func TestAContainerWellInsideItsLimitIsLeftAlone(t *testing.T) {
	in := roomy()
	got := GuardMemory(in)
	if got.Code != MemoryHealthy || got.Changed(in) {
		t.Fatalf("decision = %+v, want healthy and unchanged", got)
	}
	// A third above its use is the margin; sitting exactly on it is still inside.
	in.UsageMB = 3150 // wants 4095, rounded to 4096: the limit it has
	if got := GuardMemory(in); got.Changed(in) {
		t.Fatalf("a container exactly at its margin was changed: %+v", got)
	}
}

// The margin is what lets a build allocate between two looks without being
// killed, so the limit is restored to it rather than raised to a round figure.
func TestPastItsMarginTheLimitIsRaisedToRestoreIt(t *testing.T) {
	in := roomy()
	in.UsageMB = 3300 // wants 4290, so 4352 in whole steps
	got := GuardMemory(in)
	if got.Code != MemoryRaised || got.LimitMB != 4352 || got.SwapMB != 0 {
		t.Fatalf("decision = %+v, want the limit raised to 4352", got)
	}
	if !got.Changed(in) || !strings.Contains(got.Reason, "from 4096 to 4352") {
		t.Fatalf("reason = %q, want it to say what moved", got.Reason)
	}
}

// A raise is a call to the daemon, so one that buys less than a short step is
// rounded up to the least worth making when the room is there.
func TestARaiseIsAtLeastTheMinimumWhenThereIsRoom(t *testing.T) {
	in := roomy()
	in.UsageMB = 3200 // wants 4160: 64 more than it has
	if got := GuardMemory(in); got.LimitMB != 4096+MemoryMinimumRaiseMB {
		t.Fatalf("limit = %d, want %d", got.LimitMB, 4096+MemoryMinimumRaiseMB)
	}
}

func TestTheGuardNeverLowersALimitWhateverTheUse(t *testing.T) {
	for _, usage := range []int64{0, 1, 500, 4000, 9000} {
		in := roomy()
		in.UsageMB = usage
		got := GuardMemory(in)
		if got.LimitMB < in.LimitMB || got.SwapMB < in.SwapMB {
			t.Errorf("usage %d lowered something: %+v", usage, got)
		}
	}
	// No limit at all is no business of the guard's.
	in := roomy()
	in.LimitMB = 0
	if got := GuardMemory(in); got.Changed(in) || got.Code != MemoryHealthy {
		t.Fatalf("a container with no limit was changed: %+v", got)
	}
}

func TestARaiseStopsAtTheRunnersCeiling(t *testing.T) {
	in := roomy()
	in.UsageMB = 3300
	in.CeilingMB = 4300 // 204 MB of room
	got := GuardMemory(in)
	if got.Code != MemoryRaised || got.LimitMB != 4300 {
		t.Fatalf("decision = %+v, want a raise to the ceiling of 4300", got)
	}
	if !strings.Contains(got.Reason, "the most this runner may have (4300 MB)") {
		t.Fatalf("reason = %q, want it to say the ceiling stopped the raise", got.Reason)
	}

	// Already there: it wants more and may not have it.
	in.HeldMB = 4300
	in.LimitMB = 4300
	in.UsageMB = 4000
	got = GuardMemory(in)
	if got.Code != MemoryAtCeiling || got.Changed(in) {
		t.Fatalf("decision = %+v, want at_ceiling and nothing changed", got)
	}
}

// A pair's two halves are limited together: the ceiling is the pair's, and what
// the other half holds is room this one does not have.
func TestADockerInDockerPairSharesOneCeiling(t *testing.T) {
	in := roomy()
	in.LimitMB, in.HeldMB = 2048, 4096 // this half, and the pair
	in.CeilingMB = 6144
	in.UsageMB = 1900 // wants 2496
	got := GuardMemory(in)
	if got.Code != MemoryRaised || got.LimitMB != 2496 {
		t.Fatalf("decision = %+v, want the daemon's half raised to 2496", got)
	}
	// With the other half holding enough to bring the pair to its ceiling, this
	// half has no room whatever it is using.
	in.HeldMB = 6144
	if got := GuardMemory(in); got.Code != MemoryAtCeiling || got.Changed(in) {
		t.Fatalf("a pair at its ceiling was raised: %+v", got)
	}
}

func TestAnEmptyPoolLendsNothing(t *testing.T) {
	in := roomy()
	in.UsageMB = 3300
	in.PoolMB = 0
	got := GuardMemory(in)
	if got.Code != MemoryPoolEmpty || got.Changed(in) {
		t.Fatalf("decision = %+v, want pool_empty and nothing changed", got)
	}
	if !strings.Contains(got.Reason, "no spare memory left to lend") {
		t.Fatalf("reason = %q", got.Reason)
	}

	// The last of the pool is lent, and the raise says it was the last.
	in.PoolMB = 100
	got = GuardMemory(in)
	if got.Code != MemoryRaised || got.LimitMB != 4196 || !strings.Contains(got.Reason, "last of what the host has to lend") {
		t.Fatalf("decision = %+v, want the last 100 MB lent", got)
	}
}

// The pool is the controller's idea, which is thirty seconds old at worst. The
// machine's own free memory is checked at the moment of the raise, and a loan is
// made out of the smaller of the two.
func TestAHostAtItsFloorLendsNothingWhateverThePoolSays(t *testing.T) {
	in := roomy()
	in.UsageMB = 3300
	in.AvailableMB, in.FloorMB = 2000, 2000
	got := GuardMemory(in)
	if got.Code != MemoryHostFloor || got.Changed(in) {
		t.Fatalf("decision = %+v, want host_floor and nothing changed", got)
	}
	in.AvailableMB = 2100
	got = GuardMemory(in)
	if got.LimitMB != 4196 || got.Code != MemoryRaised {
		t.Fatalf("decision = %+v, want only the 100 MB above the floor lent", got)
	}
}

func TestAHostThatCannotBeMeasuredLendsNothing(t *testing.T) {
	in := roomy()
	in.UsageMB = 3300
	in.AvailableKnown = false
	got := GuardMemory(in)
	if got.Code != MemoryUnmeasured || got.Changed(in) {
		t.Fatalf("decision = %+v, want unmeasured and nothing changed", got)
	}
}

// Swap is the last resort, and it is spent when a kill is close: nearly at the
// limit, with nothing left in memory to give.
func TestSwapIsAllowedOnlyWhenTheMemoryCannotBeFoundAndAKillIsClose(t *testing.T) {
	in := roomy()
	in.PoolMB = 0
	in.SpillMB, in.SwapFreeMB, in.SwapKnown = 2048, 8192, true

	in.UsageMB = 3300 // 80% of the limit: tight, not about to be killed
	if got := GuardMemory(in); got.Code != MemoryPoolEmpty || got.SwapMB != 0 {
		t.Fatalf("at 80%% decision = %+v, want no swap yet", got)
	}

	in.UsageMB = 3900 // 95%
	got := GuardMemory(in)
	if got.Code != MemorySpilled || got.SwapMB != 2048 || got.LimitMB != 4096 {
		t.Fatalf("at 95%% decision = %+v, want 2048 MB of swap and the limit as it was", got)
	}
	if !strings.Contains(got.Reason, "allowed 2048 MB of swap") {
		t.Fatalf("reason = %q", got.Reason)
	}

	// Once granted it is not granted again.
	in.SwapMB = 2048
	if got := GuardMemory(in); got.Changed(in) {
		t.Fatalf("swap already allowed, yet %+v", got)
	}
}

func TestSwapIsBoundedByWhatTheHostHasAndWhatThePoolAllows(t *testing.T) {
	in := roomy()
	in.PoolMB = 0
	in.UsageMB = 3900
	in.SpillMB, in.SwapKnown = 4096, true
	in.SwapFreeMB = 512
	if got := GuardMemory(in); got.SwapMB != 512 {
		t.Fatalf("swap = %d, want the 512 MB the host has free", got.SwapMB)
	}
	in.SwapFreeMB = 0
	got := GuardMemory(in)
	if got.Changed(in) || !strings.Contains(got.Reason, "none free") {
		t.Fatalf("decision = %+v, want nothing changed and a reason that says why", got)
	}
	// A pool that does not allow swap never has any.
	in.SpillMB, in.SwapFreeMB = 0, 8192
	if got := GuardMemory(in); got.SwapMB != 0 || got.Code != MemoryPoolEmpty {
		t.Fatalf("decision = %+v, want no swap for a pool that did not allow it", got)
	}
	// A host whose swap could not be read is not guessed at.
	in.SpillMB, in.SwapKnown = 4096, false
	if got := GuardMemory(in); got.SwapMB != 0 {
		t.Fatalf("swap = %d on a host whose swap was not read", got.SwapMB)
	}
}

// A raise that could not cover the whole of what was wanted is still made, and
// the container, if it is then close to the end of it, is given swap as well.
func TestAPartialRaiseAndSwapCanComeTogether(t *testing.T) {
	in := roomy()
	in.UsageMB = 4000
	in.CeilingMB = 4200
	in.SpillMB, in.SwapFreeMB, in.SwapKnown = 1024, 8192, true
	got := GuardMemory(in)
	if got.LimitMB != 4200 || got.SwapMB != 1024 || got.Code != MemorySpilled {
		t.Fatalf("decision = %+v, want the limit at its ceiling and 1024 MB of swap", got)
	}
	if !strings.Contains(got.Reason, "raised the limit from 4096 to 4200") || !strings.Contains(got.Reason, "allowed 1024 MB of swap") {
		t.Fatalf("reason = %q, want both halves said", got.Reason)
	}
}

// An observing valve changes nothing, and the reason it gives is the one the
// Runners page puts beside "would have lent": a sentence that said a limit was
// raised would contradict the card it sits on.
func TestAnObservedDecisionIsWordedAsWhatWouldHaveBeenDone(t *testing.T) {
	in := roomy()
	in.UsageMB = 4000
	in.CeilingMB = 4200
	in.SpillMB, in.SwapFreeMB, in.SwapKnown = 1024, 8192, true
	in.Observing = true
	got := GuardMemory(in)
	if got.LimitMB != 4200 || got.SwapMB != 1024 || got.Code != MemorySpilled {
		t.Fatalf("decision = %+v, want the same decision an acting valve makes", got)
	}
	for _, want := range []string{"would have raised the limit from 4096 to 4200", "would have allowed 1024 MB of swap"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason = %q, want it to say %q", got.Reason, want)
		}
	}
	if strings.Contains(got.Reason, " raised the limit") && !strings.Contains(got.Reason, "would have raised") {
		t.Fatalf("reason = %q, an observing valve raised nothing", got.Reason)
	}
}

func TestWhatAContainerIsKeptAtAndWhenItIsWatchedClosely(t *testing.T) {
	for _, tc := range []struct {
		usage, want int64
	}{{0, 0}, {-5, 0}, {1000, 1344}, {3000, 3904}} {
		if got := MemoryWanted(tc.usage); got != tc.want {
			t.Errorf("MemoryWanted(%d) = %d, want %d", tc.usage, got, tc.want)
		}
	}
	if !MemoryHot(2900, 4096) || MemoryHot(2800, 4096) || MemoryHot(100, 0) {
		t.Fatal("hot is 70% of the limit, and nothing without one")
	}
}

// Share of the limit says how tight a runner is, not how fast it is going. A
// runner that has just been raised reads cold, and a build still climbing at
// thirty megabytes a second is a hundred and fifty late to a quiet look five
// seconds on, so the pace is read too.
func TestARunnerThatWouldReachItsLimitBeforeTheNextQuietLookIsClimbing(t *testing.T) {
	for _, tc := range []struct {
		name               string
		prev, usage, limit int64
		since              time.Duration
		climbing           bool
	}{
		{"a build climbing at thirty a second under a wide margin", 114, 146, 256, time.Second, true},
		{"the same climb with a limit that is far away", 114, 146, 4096, time.Second, false},
		{"a slow creep is not a climb", 100, 106, 256, time.Second, false},
		{"use that fell is not a climb", 200, 150, 256, time.Second, false},
		{"use that did not move is not a climb", 150, 150, 256, time.Second, false},
		{"a first look has nothing to measure against", 0, 150, 256, time.Second, false},
		{"two looks no time apart say nothing", 100, 140, 256, 0, false},
		{"a climb over a long gap is read at its pace, which is slow", 100, 130, 256, 30 * time.Second, false},
		{"a runner already past its limit is as close as it gets", 200, 260, 256, time.Second, true},
		{"a container with no limit has nothing to reach", 100, 400, 0, time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MemoryClimbing(tc.prev, tc.usage, tc.limit, tc.since); got != tc.climbing {
				t.Fatalf("MemoryClimbing(%d, %d, %d, %v) = %v, want %v", tc.prev, tc.usage, tc.limit, tc.since, got, tc.climbing)
			}
		})
	}
}
