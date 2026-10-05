package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/machine"
	"github.com/eyupio/zoomies/internal/store"
)

const megabyte = int64(1) << 20

// valveBackend is a container runtime that holds memory limits, as the daemon
// does, and says what each container is using. It is a backend.Backend through
// the plain fake and a backend.MemoryUpdater through the methods below.
type valveBackend struct {
	*fakeBackend
	vmu        sync.Mutex
	containers map[backend.Handle][]backend.MemoryContainer
	usageMB    map[string]int64
	raises     []raisedLimit
	raiseErr   error
	// looks counts the calls made, so a test can see what a tick cost.
	listings, readings int
}

type raisedLimit struct {
	container       string
	limitMB, swapMB int64
}

func newValveBackend() *valveBackend {
	return &valveBackend{
		fakeBackend: newFakeBackend(store.BackendDocker),
		containers:  map[backend.Handle][]backend.MemoryContainer{},
		usageMB:     map[string]int64{},
	}
}

// runner puts a runner's container on the host: created with guaranteeMB and
// holding limitMB, using usageMB.
func (b *valveBackend) runner(h backend.Handle, guaranteeMB, limitMB, usageMB int64) string {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	id := string(h) + "-c"
	b.containers[h] = append(b.containers[h], backend.MemoryContainer{ID: id, GuaranteeMB: guaranteeMB, LimitMB: limitMB})
	b.usageMB[id] = usageMB
	return id
}

func (b *valveBackend) sidecar(h backend.Handle, guaranteeMB, limitMB, usageMB int64) string {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	id := string(h) + "-dind"
	b.containers[h] = append(b.containers[h], backend.MemoryContainer{ID: id, Daemon: true, GuaranteeMB: guaranteeMB, LimitMB: limitMB})
	b.usageMB[id] = usageMB
	return id
}

func (b *valveBackend) use(container string, usageMB int64) {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	b.usageMB[container] = usageMB
}

func (b *valveBackend) limitOf(container string) (limit, swap int64) {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	for _, cs := range b.containers {
		for _, c := range cs {
			if c.ID == container {
				return c.LimitMB, c.SwapMB
			}
		}
	}
	return 0, 0
}

func (b *valveBackend) raised() []raisedLimit {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	return append([]raisedLimit(nil), b.raises...)
}

func (b *valveBackend) MemoryContainers(_ context.Context, h backend.Handle) ([]backend.MemoryContainer, error) {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	b.listings++
	cs, ok := b.containers[h]
	if !ok {
		return nil, backend.ErrNotFound
	}
	return append([]backend.MemoryContainer(nil), cs...), nil
}

func (b *valveBackend) MemoryUsage(_ context.Context, container string) (backend.MemoryReading, error) {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	b.readings++
	usage, ok := b.usageMB[container]
	if !ok {
		return backend.MemoryReading{}, backend.ErrNotFound
	}
	for _, cs := range b.containers {
		for _, c := range cs {
			if c.ID == container {
				return backend.MemoryReading{UsageBytes: usage * megabyte, LimitBytes: c.LimitMB * megabyte}, nil
			}
		}
	}
	return backend.MemoryReading{}, backend.ErrNotFound
}

func (b *valveBackend) RaiseMemory(_ context.Context, container string, limitMB, swapMB int64) error {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	if b.raiseErr != nil {
		return b.raiseErr
	}
	for h, cs := range b.containers {
		for i, c := range cs {
			if c.ID != container {
				continue
			}
			if limitMB < c.LimitMB {
				return backend.ErrMemoryLowering
			}
			b.containers[h][i].LimitMB, b.containers[h][i].SwapMB = limitMB, max(swapMB, c.SwapMB)
			b.raises = append(b.raises, raisedLimit{container, limitMB, swapMB})
			return nil
		}
	}
	return backend.ErrNotFound
}

// valveAgent is an agent over a valveBackend whose host has the memory a test
// says it has.
type valveAgent struct {
	*Agent
	be    *valveBackend
	clock *testClock
	host  *machine.Memory
	mu    sync.Mutex
}

func newValveAgent(t *testing.T) *valveAgent {
	t.Helper()
	be := newValveBackend()
	clock := newTestClock()
	v := &valveAgent{be: be, clock: clock, host: &machine.Memory{TotalMB: 16384, AvailableMB: 12000, SwapTotalMB: 4096, SwapFreeMB: 4096}}
	a, err := New(Options{
		Name: "test-host", WorkDir: t.TempDir(), Capacity: 4,
		Backends: backend.NewRegistry(be), DefaultBackend: store.BackendDocker,
		Transport: newFakeTransport(), HeartbeatInterval: time.Second,
		Logger: testLogger(), Clock: clock.Now,
		ReadMemory: func(int64) (machine.Memory, bool) {
			v.mu.Lock()
			defer v.mu.Unlock()
			if v.host == nil {
				return machine.Memory{}, false
			}
			return *v.host, true
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v.Agent = a
	return v
}

func (v *valveAgent) setHost(m *machine.Memory) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.host = m
}

// run puts a runner on the host, tracked by the agent as one that is working.
// Its containers are put in the runtime by valveBackend.runner, and the
// controller's rule for it is given by tell.
func (v *valveAgent) run(id string, handle backend.Handle) {
	now := v.now()
	v.mu.Lock()
	v.Agent.mu.Lock()
	v.Agent.runners[id] = &tracked{
		runnerID: id, name: "runner-" + id, kind: store.BackendDocker, handle: handle,
		createdAt: now, state: store.RunnerBusy, phase: backend.PhaseRunning, observedAt: now,
	}
	v.Agent.mu.Unlock()
	v.mu.Unlock()
}

// tell hands the agent a heartbeat's memory rules.
func (v *valveAgent) tell(capacityMB, floorMB int64, rules ...ElasticMemoryRunner) {
	v.applyMemoryDirective(&ElasticMemoryDirective{CapacityMB: capacityMB, FloorMB: floorMB, Runners: rules})
}

// tick runs the guard once, a second after the last.
func (v *valveAgent) tick() {
	v.clock.advance(time.Second)
	v.guardMemory(context.Background())
}

func (v *valveAgent) report(id string) *backend.MemoryValveSample {
	v.Agent.mu.Lock()
	defer v.Agent.mu.Unlock()
	return v.Agent.runners[id].report().Stats.MemoryValve
}

func auto(id string, guarantee, ceiling int64) ElasticMemoryRunner {
	return ElasticMemoryRunner{RunnerID: id, Mode: store.MemoryBurstAutomatic, GuaranteeMB: guarantee, CeilingMB: ceiling}
}

// The valve's whole promise in one runner: a build that is about to pass its
// limit is given more before the kernel kills it, and the loan is counted.
func TestARunnerNearItsLimitIsLentMemoryBeforeTheKernelKillsIt(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3300)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))

	v.tick()

	if limit, _ := v.be.limitOf(c); limit != 4352 {
		t.Fatalf("limit = %d MB, want 4352: use of 3300 MB plus its margin, in whole steps", limit)
	}
	got := v.report("run_a")
	if got == nil || got.Mode != "automatic" || got.LentBytes != 256*megabyte || got.Raises != 1 || got.Code != string(MemoryRaised) {
		t.Fatalf("report = %+v, want 256 MB lent by one raise", got)
	}
	// 3300 of 4096 is 80%: tight, but not within a tenth of the limit.
	if got.NearLimit {
		t.Fatalf("report = %+v, a runner at 80%% of its limit has not come within a tenth of it", got)
	}
}

// The limit only goes up. A job that grew and then shrank keeps what it was
// lent, because lowering a live limit is what kills.
func TestALoanIsNeverTakenBackWhenUseFalls(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3300)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))
	v.tick()

	v.be.use(c, 500)
	for range 10 {
		v.tick()
	}
	if limit, _ := v.be.limitOf(c); limit != 4352 {
		t.Fatalf("limit = %d MB after use fell, want it left at 4352", limit)
	}
	if got := len(v.be.raised()); got != 1 {
		t.Fatalf("%d raises, want only the first", got)
	}
}

// A runner using a little of its limit costs the daemon a look every few
// seconds and nothing else; a hot one is looked at every second.
func TestAQuietRunnerIsLookedAtLessOftenThanAHotOne(t *testing.T) {
	v := newValveAgent(t)
	v.be.runner("wl-q", 4096, 4096, 500)
	v.be.runner("wl-h", 4096, 4096, 3000) // 73%: hot, but inside its margin
	v.run("run_q", "wl-q")
	v.run("run_h", "wl-h")
	v.tell(8192, 2048, auto("run_q", 4096, 6144), auto("run_h", 4096, 6144))

	v.tick() // first look at both
	before := v.be.readings
	for range 4 {
		v.tick()
	}
	read := v.be.readings - before
	if read != 4 {
		t.Fatalf("%d readings in four ticks, want 4: the hot runner every second and the quiet one not at all", read)
	}
	v.tick() // the fifth second after the first look
	if got := v.be.readings - before - read; got != 2 {
		t.Fatalf("%d readings on the tick the quiet runner is due, want 2", got)
	}
}

// A raise leaves a runner with a wide margin, so a few looks later it reads
// cold, but what needed the raise has not stopped. Left to the quiet interval a
// build climbing at thirty megabytes a second is a hundred and fifty late to its
// next look, which a real daemon showed: the runner was raised once and killed
// at the new limit before the guard looked again.
func TestARunnerThatIsClimbingIsLookedAtEverySecondWhateverShareOfItsLimitItUses(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 256, 256, 200)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 256, 1024))

	v.tick() // 200 of 256: raised to 384
	if limit, _ := v.be.limitOf(c); limit != 384 {
		t.Fatalf("limit = %d MB, want 384", limit)
	}

	// 60% and then 69% of the new limit: under the share that makes a runner
	// hot, and each a thirty-megabyte climb that would reach the limit in a few
	// seconds.
	for i, usage := range []int64{232, 264} {
		v.be.use(c, usage)
		before := v.be.readings
		v.tick()
		if got := v.be.readings - before; got != 1 {
			t.Fatalf("tick %d: %d readings, want the climbing runner looked at every second", i+2, got)
		}
	}
	if limit, _ := v.be.limitOf(c); limit != 384 {
		t.Fatalf("limit = %d MB, want it still inside its margin at 384", limit)
	}

	// Past its margin again, and raised again; then it stops, and is left to the
	// quiet interval like any other.
	v.be.use(c, 296)
	v.tick()
	if limit, _ := v.be.limitOf(c); limit != 512 {
		t.Fatalf("limit = %d MB, want 512", limit)
	}
	v.tick() // flat: looked at once more, and found not to be climbing
	before := v.be.readings
	for range 3 {
		v.tick()
	}
	if got := v.be.readings - before; got != 0 {
		t.Fatalf("%d readings of a runner that stopped climbing, want none until the quiet interval is up", got)
	}
}

// Observe mode is the evidence an operator reads before letting the valve move
// anything: what it would have lent, and nothing actually changes.
func TestAnObservingValveRecordsWhatItWouldHaveLentAndChangesNothing(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	rule := auto("run_a", 4096, 6144)
	rule.Mode = store.MemoryBurstObserve
	v.tell(8192, 2048, rule)

	v.tick()

	if limit, _ := v.be.limitOf(c); limit != 4096 {
		t.Fatalf("limit = %d MB, want it untouched while observing", limit)
	}
	if len(v.be.raised()) != 0 {
		t.Fatalf("raises = %+v, want none", v.be.raised())
	}
	got := v.report("run_a")
	if got == nil || got.Mode != "observe" || got.LentBytes != 0 || got.WouldLendBytes < 1024*megabyte || !got.NearLimit {
		t.Fatalf("report = %+v, want nothing lent, over a gigabyte it would have lent, and the near-limit flag", got)
	}
	if got.Code != string(MemoryRaised) {
		t.Fatalf("code = %q, want the decision it would have made", got.Code)
	}
	if !strings.HasPrefix(got.Reason, "would have raised the limit") {
		t.Fatalf("reason = %q, want it worded as what would have been done", got.Reason)
	}
}

// An observing runner draws on the same pool as the others, so a host does not
// report that every runner on it would have taken all of what is spare.
func TestObserversShareThePoolTheyWouldHaveDrawnOn(t *testing.T) {
	v := newValveAgent(t)
	v.be.runner("wl-a", 4096, 4096, 3900)
	v.be.runner("wl-b", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.run("run_b", "wl-b")
	ra, rb := auto("run_a", 4096, 8192), auto("run_b", 4096, 8192)
	ra.Mode, rb.Mode = store.MemoryBurstObserve, store.MemoryBurstObserve
	v.tell(1500, 2048, ra, rb)

	v.tick()

	wa, wb := v.report("run_a").WouldLendBytes, v.report("run_b").WouldLendBytes
	if (wa+wb)/megabyte > 1500 {
		t.Fatalf("observers would have lent %d and %d MB from a pool of 1500", wa/megabyte, wb/megabyte)
	}
}

// The pool is shared. What one runner has been lent is gone for the next, the
// last of it goes to whoever is next in line, and the runner after that is told
// there is none rather than lent what is not there.
func TestWhatOneRunnerIsLentIsGoneFromThePoolForTheNext(t *testing.T) {
	v := newValveAgent(t)
	a := v.be.runner("wl-a", 4096, 4096, 3900)
	b := v.be.runner("wl-b", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.run("run_b", "wl-b")
	// 1500 MB between two runners that each want a gigabyte.
	v.tell(1500, 2048, auto("run_a", 4096, 8192), auto("run_b", 4096, 8192))

	v.tick()

	la, _ := v.be.limitOf(a)
	lb, _ := v.be.limitOf(b)
	if lent := (la - 4096) + (lb - 4096); lent != 1500 {
		t.Fatalf("%d MB lent (%d and %d) from a pool of 1500, want exactly all of it and no more", lent, la-4096, lb-4096)
	}

	// The first in line was served in full; the second wants more than was left
	// and is told the pool is empty on the looks that follow.
	v.tick()
	v.tick()
	if got := v.report("run_a"); got.Code != string(MemoryHealthy) {
		t.Fatalf("the runner served in full says %q, want healthy", got.Code)
	}
	if got := v.report("run_b"); got.Code != string(MemoryPoolEmpty) {
		t.Fatalf("the runner left short says %q, want pool_empty", got.Code)
	}
	la2, _ := v.be.limitOf(a)
	lb2, _ := v.be.limitOf(b)
	if la2+lb2 != la+lb {
		t.Fatalf("the pair of runners holds %d MB after more ticks, was %d: nothing is left to lend", la2+lb2, la+lb)
	}
}

// A heartbeat is thirty seconds away and the host's memory moves faster: the
// valve checks what is free at the moment of the raise, and the pool the
// controller worked out thirty seconds ago does not override it.
func TestAHostThatHasFilledUpSinceTheLastHeartbeatLendsNothing(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))
	v.setHost(&machine.Memory{TotalMB: 16384, AvailableMB: 2048}) // exactly its floor

	v.tick()

	if limit, _ := v.be.limitOf(c); limit != 4096 {
		t.Fatalf("limit = %d MB on a host at its floor, want it untouched", limit)
	}
	if got := v.report("run_a"); got.Code != string(MemoryHostFloor) {
		t.Fatalf("code = %q, want host_floor", got.Code)
	}
}

func TestARunnerOnAHostWithNoReadingOfItsMemoryIsLentNothing(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))
	v.setHost(nil)

	v.tick()

	if limit, _ := v.be.limitOf(c); limit != 4096 {
		t.Fatalf("limit = %d MB with no reading of the host, want it untouched", limit)
	}
	if got := v.report("run_a"); got.Code != string(MemoryUnmeasured) {
		t.Fatalf("code = %q, want unmeasured", got.Code)
	}
}

// Swap is where a build goes when the memory could not be found, and only when
// the pool allowed it.
func TestSwapIsOfferedOnlyWhenThePoolAllowsItAndTheMemoryIsNotThere(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 4000)
	v.run("run_a", "wl-a")
	rule := auto("run_a", 4096, 4096) // at its ceiling already
	v.tell(8192, 2048, rule)
	v.tick()
	if _, swap := v.be.limitOf(c); swap != 0 {
		t.Fatalf("swap = %d MB for a pool that did not allow it", swap)
	}

	rule.SpillMB = 2048
	v.tell(8192, 2048, rule)
	v.tick()
	if limit, swap := v.be.limitOf(c); limit != 4096 || swap != 2048 {
		t.Fatalf("limit %d MB and swap %d MB, want the limit untouched and 2048 MB of swap", limit, swap)
	}
	got := v.report("run_a")
	if got.SpillBytes != 2048*megabyte || got.Code != string(MemorySpilled) {
		t.Fatalf("report = %+v, want 2048 MB spilled", got)
	}
}

// A docker-in-docker runner is two containers under one ceiling. The daemon,
// where the build is, is the one raised; the runner beside it is not.
func TestAPairRaisesTheContainerThatIsRunningOutUnderOneCeiling(t *testing.T) {
	v := newValveAgent(t)
	r := v.be.runner("wl-a", 2048, 2048, 300)
	d := v.be.sidecar("wl-a", 2048, 2048, 1900)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))

	v.tick()

	if limit, _ := v.be.limitOf(r); limit != 2048 {
		t.Fatalf("the runner's limit = %d MB, want it left alone: it was using 300", limit)
	}
	if limit, _ := v.be.limitOf(d); limit != 2496 {
		t.Fatalf("the daemon's limit = %d MB, want 2496", limit)
	}
	if got := v.report("run_a"); got.LentBytes != 448*megabyte {
		t.Fatalf("lent = %d MB, want the daemon's 448", got.LentBytes/megabyte)
	}

	// The pair shares one ceiling: with the daemon at 6144 less what the runner
	// holds, there is no more for either.
	v.be.use(d, 2490)
	for range 12 {
		v.tick()
	}
	rl, _ := v.be.limitOf(r)
	dl, _ := v.be.limitOf(d)
	if rl+dl > 6144 {
		t.Fatalf("the pair holds %d MB, over its ceiling of 6144", rl+dl)
	}
}

// What was lent is what the daemon says it holds, not what an agent remembers:
// a restarted agent finds it again.
func TestARestartedAgentFindsALoanItsPredecessorMade(t *testing.T) {
	v := newValveAgent(t)
	v.be.runner("wl-a", 4096, 5120, 1000) // already raised by an agent that has since died
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))

	v.tick()

	got := v.report("run_a")
	if got == nil || got.LentBytes != 1024*megabyte {
		t.Fatalf("report = %+v, want the 1024 MB the daemon holds beyond the creation limit", got)
	}
	if n := len(v.be.raised()); n != 0 {
		t.Fatalf("%d raises for a runner that was using a quarter of its limit", n)
	}
}

// Switching a pool's valve off stops the lending but not the telling: a limit
// that was raised stays raised, and the host's books must go on counting it.
func TestARunnerWhoseRuleIsWithdrawnStillReportsWhatItHolds(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3300)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))
	v.tick()

	v.applyMemoryDirective(nil)
	v.be.use(c, 4300)
	v.tick()

	if n := len(v.be.raised()); n != 1 {
		t.Fatalf("%d raises, want the one made before the rule went", n)
	}
	got := v.report("run_a")
	if got == nil || got.Mode != "off" || got.LentBytes != 256*megabyte {
		t.Fatalf("report = %+v, want mode off and the 256 MB still held", got)
	}
}

// A controller restart must not be what kills a job: the agent keeps working on
// the rules it has when a heartbeat does not arrive.
func TestARuleOutlivesAHeartbeatThatNeverArrives(t *testing.T) {
	v := newValveAgent(t)
	c := v.be.runner("wl-a", 4096, 4096, 3300)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))
	v.clock.advance(10 * time.Minute)

	v.be.use(c, 4000)
	v.tick()
	if limit, _ := v.be.limitOf(c); limit <= 4096 {
		t.Fatalf("limit = %d MB ten minutes after the last heartbeat, want the guard still working", limit)
	}
}

// A runtime that cannot change a live limit is found out once and then left
// alone; its runners say why they are not lent anything.
func TestARuntimeThatCannotRaiseALimitIsNotAskedAgain(t *testing.T) {
	v := newValveAgent(t)
	v.be.raiseErr = backend.ErrMemoryUpdateUnsupported
	c := v.be.runner("wl-a", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))

	v.tick()
	before := v.be.readings
	for range 5 {
		v.tick()
	}
	if v.be.readings != before {
		t.Fatalf("the runtime was looked at %d more times after it refused", v.be.readings-before)
	}
	if limit, _ := v.be.limitOf(c); limit != 4096 {
		t.Fatalf("limit = %d MB, want it untouched", limit)
	}
	if got := v.report("run_a"); got.Code != string(MemoryUnsupported) || got.Reason == "" {
		t.Fatalf("report = %+v, want unsupported and a reason", got)
	}
	// A runner that arrives later on the same runtime says the same.
	v.be.runner("wl-b", 4096, 4096, 100)
	v.run("run_b", "wl-b")
	v.tell(8192, 2048, auto("run_a", 4096, 6144), auto("run_b", 4096, 6144))
	v.tick()
	if got := v.report("run_b"); got.Code != string(MemoryUnsupported) {
		t.Fatalf("report = %+v for a runner on a runtime that refused, want unsupported", got)
	}
}

// Any other refusal is tried again at the next look: the daemon may recover.
func TestARaiseTheDaemonRefusesIsTriedAgain(t *testing.T) {
	v := newValveAgent(t)
	v.be.raiseErr = errors.New("daemon is busy")
	c := v.be.runner("wl-a", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))

	v.tick()
	if got := v.report("run_a"); got.Code != string(MemoryFailed) {
		t.Fatalf("code = %q, want failed", got.Code)
	}
	v.be.vmu.Lock()
	v.be.raiseErr = nil
	v.be.vmu.Unlock()
	v.tick()
	if limit, _ := v.be.limitOf(c); limit <= 4096 {
		t.Fatalf("limit = %d MB, want it raised once the daemon answered", limit)
	}
}

// A runner whose job has ended is no business of the valve's, and a runner that
// vanishes between the look and the raise is not an error.
func TestAFinishedRunnerIsLeftAlone(t *testing.T) {
	v := newValveAgent(t)
	v.be.runner("wl-a", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))
	v.Agent.mu.Lock()
	v.Agent.runners["run_a"].terminal = true
	v.Agent.mu.Unlock()

	v.tick()
	if len(v.be.raised()) != 0 {
		t.Fatal("a finished runner was raised")
	}
}

func TestOnlyRunnersTheControllerNamesAreWatched(t *testing.T) {
	v := newValveAgent(t)
	named := v.be.runner("wl-a", 4096, 4096, 3900)
	other := v.be.runner("wl-b", 4096, 4096, 3900)
	v.run("run_a", "wl-a")
	v.run("run_b", "wl-b")
	v.tell(8192, 2048, auto("run_a", 4096, 6144))

	v.tick()

	if limit, _ := v.be.limitOf(named); limit <= 4096 {
		t.Fatalf("the named runner's limit = %d MB, want it raised", limit)
	}
	if limit, _ := v.be.limitOf(other); limit != 4096 {
		t.Fatalf("an unnamed runner's limit = %d MB, want it untouched", limit)
	}
	if got := v.report("run_b"); got != nil {
		t.Fatalf("report = %+v for a runner nothing is watching, want none", got)
	}
}

// The valve is offered wherever the host's memory can be read the way a loan
// has to be checked against it.
func TestTheAgentSaysItCanLendMemoryOnlyWhereItCanCheckTheHost(t *testing.T) {
	v := newValveAgent(t)
	has := func(a *Agent) bool {
		for _, f := range a.features() {
			if f == FeatureElasticMemory {
				return true
			}
		}
		return false
	}
	if !has(v.Agent) {
		t.Fatal("an agent that can read the host's memory did not advertise the valve")
	}
	for _, f := range []string{FeatureElasticCPU, FeatureToolCacheFill, FeatureTmpfs} {
		found := false
		for _, g := range v.features() {
			found = found || g == f
		}
		if !found {
			t.Errorf("the feature %q was dropped", f)
		}
	}
}
