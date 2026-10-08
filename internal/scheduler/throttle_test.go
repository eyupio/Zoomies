package scheduler

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/store"
)

// pressed is a measured host with a fresh sample of the given shape, carrying
// two live runners of which one has no CPU quota -- the shape on which a CPU
// hold can mean something.
func pressed(now time.Time, cpu float64, held bool, load float64, memory int64) *store.Host {
	h := sized("host_a", 4, 8, 16*1024, 500*1024)
	h.ActiveRunners, h.UnlimitedRunners = 2, 1
	h.Usage = store.HostUsage{CPUPercent: &cpu, CPUHeld: held, LoadAverage1: &load, MemoryAvailableMB: &memory, SampledAt: now}
	return h
}

// A host whose every runner is inside its CPU quota is running exactly the
// work it was sized for, and with the quotas summing to the machine less its
// reserve that work sits at 95% CPU all day. The hold that reads that as
// overload must not climb the ladder: a throttle that took a healthy host,
// watched it calm down, lifted and took it again would run that loop for
// ever, slowing every job on the host for most of every cycle.
func TestAFullHostInsideItsQuotasIsNeverThrottledForItsCPU(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	h := pressed(now, 96, true, 15, 8192)
	h.CPUs, h.ActiveRunners, h.UnlimitedRunners = 16, 4, 0
	for i := 0; i < 10; i++ {
		at := now.Add(time.Duration(i) * ThrottleStep)
		h.Usage.SampledAt = at
		if got := NextThrottle(h, at); got.Active() {
			t.Fatalf("a full, in-quota host was throttled on step %d: %+v", i, got)
		}
	}
	// The same host with one runner nothing binds is a different host: that
	// runner can take more than its share, and the hold is about it.
	h.UnlimitedRunners = 1
	h.Usage.SampledAt = now
	if got := NextThrottle(h, now); !got.Active() || !strings.Contains(got.Reason, "1 runner here that no CPU limit binds") {
		t.Fatalf("a host with an unlimited runner under a CPU hold was not throttled: %+v", got)
	}
}

// The ladder climbs one rung per step while the pressure keeps coming back,
// and stops at the top: a fourth rung would leave a host taking nothing, which
// is a cordon, and a cordon is the operator's.
func TestAnOverwhelmedHostClimbsOneRungPerStepUpToTheTop(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	h := pressed(now, 99, true, 4, 8192)

	got := NextThrottle(h, now)
	if got.Level != 1 || got.Since == nil || !got.Since.Equal(now) || got.ChangedAt == nil || !strings.Contains(got.Reason, "CPU") {
		t.Fatalf("first step = %+v, want rung 1 with the CPU hold as its reason", got)
	}
	// Still overwhelmed a minute later: too soon for the next rung. The
	// quota lowered on the live containers has to be given time to show in
	// a load average that itself averages over a minute.
	h.Throttle = got
	later := now.Add(ThrottleStep - time.Second)
	h.Usage.SampledAt = later
	if got = NextThrottle(h, later); got.Level != 1 {
		t.Fatalf("a second rung came %s after the first, before ThrottleStep: %+v", ThrottleStep-time.Second, got)
	}
	later = now.Add(ThrottleStep)
	h.Usage.SampledAt = later
	if got = NextThrottle(h, later); got.Level != 2 || !got.ChangedAt.Equal(later) || !got.Since.Equal(now) {
		t.Fatalf("second step = %+v, want rung 2 with the episode still dated from the first", got)
	}
	h.Throttle = got
	later = later.Add(ThrottleStep)
	h.Usage.SampledAt = later
	got = NextThrottle(h, later)
	h.Throttle = got
	later = later.Add(ThrottleStep)
	h.Usage.SampledAt = later
	if got = NextThrottle(h, later); got.Level != store.MaxThrottleLevel {
		t.Fatalf("level = %d after four overwhelmed steps, want the top rung %d and no further", got.Level, store.MaxThrottleLevel)
	}
	if h.Throttle = got; h.EffectiveCapacity() != 1 {
		t.Fatalf("effective capacity at the top rung = %d, want the one slot a throttle never takes", h.EffectiveCapacity())
	}
}

// Each of the three signals climbs the ladder on its own, and names itself:
// the reason is what the Hosts page shows, and "sustained pressure" alone
// would send an operator to look at the CPU when the memory was the problem.
func TestEachPressureSignalNamesItself(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		host *store.Host
		want string
	}{
		{"the CPU hold, with a runner no limit binds", pressed(now, 99, true, 1, 8192), "CPU has been at or above 95% for 30 s with 1 runner here that no CPU limit binds"},
		{"a load average past twice the cores", pressed(now, 60, false, 17, 8192), "load average is 17.0, at least twice the host's 8 CPUs"},
		{"memory at the reserve", pressed(now, 60, false, 1, store.MinHostReserveMemoryMB), "available memory is at or below the host's reserve"},
	}
	for _, tc := range cases {
		got := NextThrottle(tc.host, now)
		if got.Level != 1 || !strings.Contains(got.Reason, tc.want) {
			t.Errorf("%s: throttle = %+v, want rung 1 mentioning %q", tc.name, got, tc.want)
		}
	}
	// And a host that is merely busy climbs nothing: 90% CPU, a load of one
	// per core and memory to spare is a host doing its job.
	if got := NextThrottle(pressed(now, 90, false, 8, 8192), now); got.Active() {
		t.Errorf("a busy host was throttled: %+v", got)
	}
}

// Coming down is slower than going up, on purpose. A host that recovered in a
// minute and was pushed straight back over would otherwise oscillate with the
// ladder rather than settle on it, so a rung is given back only after a
// stretch of calm long enough to mean something -- and the stretch starts
// again if the calm breaks.
func TestAThrottleLiftsOneRungPerStretchOfCalmAndABusySampleBreaksTheStretch(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	h := pressed(now, 20, false, 1, 8192)
	at := now.Add(-ThrottleStep)
	h.Throttle = store.HostThrottle{Level: 2, Since: &at, ChangedAt: &at, Reason: "CPU has been at or above 95% for 30 s"}

	got := NextThrottle(h, now)
	if got.Level != 2 || got.CalmSince == nil || !got.CalmSince.Equal(now) {
		t.Fatalf("the first calm sample = %+v, want the streak to start and the rung to stay", got)
	}
	h.Throttle = got
	// A busy sample in the band between calm and overwhelmed breaks the
	// streak without moving the rung.
	busy := now.Add(time.Minute)
	h.Usage.SampledAt = busy
	*h.Usage.CPUPercent = 90
	if got = NextThrottle(h, busy); got.Level != 2 || got.CalmSince != nil {
		t.Fatalf("a busy sample = %+v, want the rung kept and the streak broken", got)
	}
	h.Throttle = got
	*h.Usage.CPUPercent = 20
	calm := busy.Add(time.Minute)
	h.Usage.SampledAt = calm
	got = NextThrottle(h, calm)
	h.Throttle = got
	almost := calm.Add(ThrottleRecovery - time.Second)
	h.Usage.SampledAt = almost
	if got = NextThrottle(h, almost); got.Level != 2 {
		t.Fatalf("a rung was given back a second before ThrottleRecovery: %+v", got)
	}
	done := calm.Add(ThrottleRecovery)
	h.Usage.SampledAt = done
	got = NextThrottle(h, done)
	if got.Level != 1 || got.CalmSince == nil || !got.CalmSince.Equal(done) || got.Reason == "" {
		t.Fatalf("after ThrottleRecovery = %+v, want rung 1, the streak restarted and the reason kept", got)
	}
	h.Throttle = got
	last := done.Add(ThrottleRecovery)
	h.Usage.SampledAt = last
	if got = NextThrottle(h, last); got != (store.HostThrottle{}) {
		t.Fatalf("the last rung did not clear the episode: %+v", got)
	}
}

// A host nobody can measure any more is placed by its configured capacity,
// exactly as the holds fall back -- but not at once. One missed sample is a
// slow heartbeat; ten minutes of them is an agent downgraded to a build that
// does not measure, or a host that went away, and a host that comes back
// must not still be throttled for pressure nobody can see.
func TestAThrottleOutlivesAMissedSampleAndNotAStaleOne(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	h := pressed(now, 99, true, 4, 8192)
	h.Throttle = NextThrottle(h, now)
	if !h.Throttle.Active() {
		t.Fatal("setup: the host was not throttled")
	}
	stale := now.Add(store.HostUsageMaxAge + time.Second)
	if got := NextThrottle(h, stale); got != h.Throttle {
		t.Fatalf("a stale sample moved the throttle: %+v", got)
	}
	if got := NextThrottle(h, now.Add(StaleThrottleReset)); got.Active() {
		t.Fatalf("the throttle survived %s without a sample: %+v", StaleThrottleReset, got)
	}
	// A throttle with no measurement behind it at all cannot have been
	// decided by anything, and is cleared.
	h.Usage = store.HostUsage{}
	if got := NextThrottle(h, now); got.Active() {
		t.Fatalf("a throttle with no usage behind it survived: %+v", got)
	}
}

// The scheduler counts a throttled host as its own kind of blockage. "At
// capacity" would send an operator to raise a capacity that is not the
// problem: the host is being pushed too hard at the slots it already has, and
// the fix is fewer slots or tighter pools, not more of either.
func TestAThrottledHostIsNamedAsSuchWhenNothingCanBePlaced(t *testing.T) {
	// The package clock, so the host's heartbeat and the sample are fresh
	// against the snapshot's Now.
	h := pressed(now, 60, false, 1, 8192)
	at := now.Add(-time.Minute)
	h.Throttle = store.HostThrottle{Level: 2, Since: &at, ChangedAt: &at, Reason: "CPU has been at or above 95% for 30 s"}
	// Four slots, two live runners with a limit each: full at the throttle's
	// two, not at the operator's four.
	p := limited("builders", 1, 1024)
	p.MaxRunners = 4
	runners := []*store.Runner{
		testRunner("run_1", p, store.RunnerBusy, time.Minute),
		testRunner("run_2", p, store.RunnerBusy, time.Minute),
	}
	h.ActiveRunners, h.UnlimitedRunners = 2, 0
	plan := Decide(snap([]*store.Pool{p}, runners, []*store.Job{queued("job_1", time.Minute, "builders")}, []*store.Host{h}))
	if len(plan.Actions) != 0 {
		t.Fatalf("a throttled host at its effective capacity was given a runner: %+v", plan.Actions)
	}
	pp := plan.Pools[0]
	if !strings.Contains(pp.Blocked, "1 throttled after sustained pressure") || !strings.Contains(pp.Blocked, "throttled to 2 of 4 slots") {
		t.Errorf("Blocked = %q, want the throttled count and the host's own reason", pp.Blocked)
	}
	if !strings.Contains(pp.BlockedFix, "lower those hosts' capacity") {
		t.Errorf("BlockedFix = %q, want the throttle's fix", pp.BlockedFix)
	}
	// To a provisioner a throttled host is a full one: more hosts is the one
	// answer that helps either way, so the signal it listens for is raised.
	if !pp.BlockedAtCapacity {
		t.Error("a fleet blocked only by throttles did not ask for capacity")
	}
	// Two runners, both with a limit: the sentence says what the throttle is
	// doing to them, and which step it is on.
	if got := ThrottleReason(h); !strings.Contains(got, "step 2 of 3") || !strings.Contains(got, "the 2 runners with a CPU limit at 50% of it") {
		t.Errorf("ThrottleReason = %q", got)
	}
	// A runner nothing binds is not promised a slowdown it will not get, and
	// a host with only the process backend has no container to slow at all.
	h.UnlimitedRunners = 2
	if got := ThrottleReason(h); strings.Contains(got, "CPU limit at") || !strings.Contains(got, "running jobs continue") {
		t.Errorf("ThrottleReason for unlimited runners = %q", got)
	}
	h.UnlimitedRunners = 0
	h.Backends = store.StringSlice{"process"}
	if got := ThrottleReason(h); strings.Contains(got, "CPU limit at") {
		t.Errorf("ThrottleReason for a process-only host = %q", got)
	}
}

// A failure message is not ASCII, and the ellipsis must not be paid for with
// half a character.
//
// The text comes from a daemon, a registry or GitHub, so the byte the limit
// lands on is routinely in the middle of one. What used to come out the other
// side reached the problems drawer and the pool page with a replacement glyph
// in the middle of a word -- which reads as Zoomies having mangled the message
// rather than shortened it.
func TestSummariseCutsBetweenCharacters(t *testing.T) {
	// A message whose 159th byte falls inside a three-byte character.
	message := strings.Repeat("a", 158) + "→" + strings.Repeat("b", 40)
	got := summarise(message)
	if !utf8.ValidString(got) {
		t.Fatalf("summarise produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("summarise = %q, want it to end in an ellipsis", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("summarise = %q, which carries a replacement character", got)
	}

	// A message that fits is returned whole, whatever it is made of.
	short := "could not pull ghcr.io/acme/runner: manifest unknown, check the tag"
	if got := summarise(short); got != short {
		t.Fatalf("summarise(%q) = %q, want it unchanged", short, got)
	}
	if got := summarise("   "); got != "no reason was recorded" {
		t.Fatalf("summarise of whitespace = %q", got)
	}
}
