package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// createdRunner puts a runner in the tracked set the way handleCreate does,
// without a task round trip: created with these resources, at its full
// allocation.
func createdRunner(a *Agent, runnerID string, handle backend.Handle, res store.Resources) {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.runners[runnerID] = &tracked{
		runnerID: runnerID, name: "runner-" + runnerID, kind: store.BackendDocker, handle: handle,
		createdAt: now, state: store.RunnerRegistering, phase: backend.PhaseRunning, observedAt: now,
		resources: res, appliedCPUFactor: new(float64(1)),
	}
}

// beat answers one heartbeat with the given throttle and returns the calls
// the backend received during it.
func beat(t *testing.T, a *Agent, tr *fakeTransport, be *fakeBackend, d *ThrottleDirective, elastic ...ElasticCPUDirective) []resourceUpdate {
	t.Helper()
	tr.mu.Lock()
	tr.beatResp = &HeartbeatResponse{OK: true, Throttle: d, ElasticCPU: elastic}
	tr.mu.Unlock()
	before := len(be.resourceUpdates())
	if err := a.heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	return be.resourceUpdates()[before:]
}

func TestElasticCPUBoostsAndThenRestoresARunner(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2, MemoryMB: 4096})

	got := beat(t, a, tr, be, &ThrottleDirective{CPUFactor: 1}, ElasticCPUDirective{
		RunnerID: "run_a", CPUFactor: 2, BaseCPUs: 2, TargetCPUs: 4,
	})
	if len(got) != 1 || got[0].res.CPUs != 4 || got[0].res.MemoryMB != 4096 {
		t.Fatalf("updates = %+v, want the CPU quota raised to 4 and memory unchanged", got)
	}

	got = beat(t, a, tr, be, &ThrottleDirective{CPUFactor: 1})
	if len(got) != 1 || got[0].res.CPUs != 2 {
		t.Fatalf("updates = %+v, want the runner restored to its 2 CPU guarantee", got)
	}
}

func TestHostPressureWinsOverElasticCPU(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2})
	a.opts.BootstrapCPUGrace = 0

	got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5}, ElasticCPUDirective{
		RunnerID: "run_a", CPUFactor: 2, BaseCPUs: 2, TargetCPUs: 4,
	})
	if len(got) != 1 || got[0].res.CPUs != 1 {
		t.Fatalf("updates = %+v, want pressure to tighten the runner to 1 CPU", got)
	}
}

// Once every runner on an overwhelmed host is busy, the effective capacity
// reaches nothing: the jobs already running are what is overwhelming it. The
// directive has to reach each of them, once, and the same directive on the
// next beat must cost the daemon nothing.
func TestAThrottleDirectiveReducesEachLimitedRunnerOnceAndNotAgainOnTheSameBeat(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2, MemoryMB: 4096, PidsLimit: 512})
	createdRunner(a, "run_b", "wl-b", store.Resources{CPUs: 0.75, MemoryMB: 1900})

	got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5})
	if len(got) != 2 {
		t.Fatalf("updates = %+v, want one per limited runner", got)
	}
	want := map[backend.Handle]store.Resources{
		"wl-a": {CPUs: 1, MemoryMB: 4096, PidsLimit: 512},
		// 0.375 is not a quota the daemon or the pool field speaks in.
		"wl-b": {CPUs: 0.38, MemoryMB: 1900},
	}
	for _, u := range got {
		if u.res != want[u.handle] {
			t.Errorf("%s was given %+v, want %+v: the base scaled by the factor, everything else as created", u.handle, u.res, want[u.handle])
		}
	}

	if again := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5}); len(again) != 0 {
		t.Fatalf("the same directive was applied again on the next beat: %+v", again)
	}
	if lower := beat(t, a, tr, be, &ThrottleDirective{Level: 3, CPUFactor: 0.5}); len(lower) != 0 {
		t.Fatalf("a step that keeps the same factor was applied again: %+v", lower)
	}
}

// A throttle that never lifts is a job slowed for ever. A factor of 1 is the
// controller saying the host is calm, and every throttled runner goes back to
// what it was created with.
func TestAFactorOfOneRestoresEveryThrottledRunner(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2})
	beat(t, a, tr, be, &ThrottleDirective{Level: 1, CPUFactor: 0.75})

	got := beat(t, a, tr, be, &ThrottleDirective{Level: 0, CPUFactor: 1})
	if len(got) != 1 || got[0].handle != "wl-a" || got[0].res.CPUs != 2 {
		t.Fatalf("updates = %+v, want wl-a restored to 2 CPUs", got)
	}
	// A directive with no factor at all is a controller that has cleared the
	// throttle without saying so in numbers, and reads the same way.
	beat(t, a, tr, be, &ThrottleDirective{Level: 1, CPUFactor: 0.5})
	if got := beat(t, a, tr, be, &ThrottleDirective{}); len(got) != 1 || got[0].res.CPUs != 2 {
		t.Fatalf("updates = %+v, want a zero factor read as full allocation", got)
	}
}

// A runner with no CPU limit -- defaults off, or a row from before they
// existed -- has no quota to scale. Asking the daemon to set one would be a
// new limit rather than a throttle, and it is the controller's problem panel
// that says such runners exist.
func TestARunnerWithNoCPULimitIsLeftAlone(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_unlimited", "wl-u", store.Resources{MemoryMB: 4096})
	createdRunner(a, "run_limited", "wl-l", store.Resources{CPUs: 1})

	got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5})
	if len(got) != 1 || got[0].handle != "wl-l" {
		t.Fatalf("updates = %+v, want only the limited runner touched", got)
	}
}

// An adopted runner was made by an agent that may have throttled it and then
// died, and this agent has no record either way. Its base comes from the
// workload's labels, and it is reconciled on the first beat whichever way the
// controller says -- the backend's update costs nothing when it already
// matches, so the safe direction is to ask.
func TestAnAdoptedRunnerIsReconciledOnTheFirstBeat(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	be.setWorkloads(backend.Workload{
		Handle: "wl-old", Name: "runner-old", RunnerID: "run_old",
		Status:    backend.Status{Phase: backend.PhaseRunning, StartedAt: time.Now().Add(-time.Hour)},
		Resources: store.Resources{CPUs: 4, MemoryMB: 8192},
	})
	a.adoptExisting(context.Background())

	got := beat(t, a, tr, be, &ThrottleDirective{Level: 0, CPUFactor: 1})
	if len(got) != 1 || got[0].handle != "wl-old" || got[0].res != (store.Resources{CPUs: 4, MemoryMB: 8192}) {
		t.Fatalf("updates = %+v, want the adopted runner reconciled to its labelled allocation", got)
	}
	if again := beat(t, a, tr, be, &ThrottleDirective{Level: 0, CPUFactor: 1}); len(again) != 0 {
		t.Fatalf("once reconciled the runner was asked about again: %+v", again)
	}
	// And a standing throttle reaches it the same way.
	if got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5}); len(got) != 1 || got[0].res.CPUs != 2 {
		t.Fatalf("updates = %+v, want the adopted runner throttled from its labelled base", got)
	}
}

// An older controller sends no directive. It never throttled anything, so
// there is nothing to restore, and reconciling every adopted runner against
// a controller that will never ask for it is a request per runner for nothing.
func TestANilDirectiveFromAnOlderControllerTouchesNothing(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2})
	be.setWorkloads(backend.Workload{
		Handle: "wl-old", Name: "runner-old", RunnerID: "run_old",
		Status:    backend.Status{Phase: backend.PhaseRunning, StartedAt: time.Now().Add(-time.Hour)},
		Resources: store.Resources{CPUs: 4},
	})
	a.adoptExisting(context.Background())

	if got := beat(t, a, tr, be, nil); len(got) != 0 {
		t.Fatalf("an older controller's beat changed quotas: %+v", got)
	}
}

// A controller downgraded under a standing throttle would otherwise leave
// every runner slowed for ever: the nil it now sends is the one nil that has
// to mean "restore".
func TestANilDirectiveAfterAThrottleRestoresTheRunners(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2})
	beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5})

	got := beat(t, a, tr, be, nil)
	if len(got) != 1 || got[0].res.CPUs != 2 {
		t.Fatalf("updates = %+v, want the runner restored", got)
	}
}

// A daemon that refuses -- Podman without the endpoint, a rootless daemon
// that cannot set a quota -- must not fail the beat that carries the host's
// runners, and must not say so on every one of them either: one line per
// runner per factor is what an operator can read.
func TestARefusedUpdateIsLoggedOnceAndDoesNotBreakTheHeartbeat(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	var logs lockedBuffer
	a.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2})
	be.mu.Lock()
	be.updateErr = errors.New("the daemon does not support updating a running container")
	be.mu.Unlock()

	for range 3 {
		beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5})
	}
	if n := strings.Count(logs.String(), "level=WARN msg=\"could not change a runner's CPU quota"); n != 1 {
		t.Fatalf("the refusal was warned about %d times over three beats, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "throttling 1 runners to 50% of their CPU allocation") {
		t.Fatalf("the throttle was not announced:\n%s", logs.String())
	}

	// A different factor is a different refusal, worth its own line.
	beat(t, a, tr, be, &ThrottleDirective{Level: 1, CPUFactor: 0.75})
	if n := strings.Count(logs.String(), "level=WARN msg=\"could not change a runner's CPU quota"); n != 2 {
		t.Fatalf("a refusal at a new factor was not logged, %d warnings:\n%s", n, logs.String())
	}

	// The runner was never moved off its full allocation, so a factor of 1
	// has nothing to restore and must not pretend otherwise.
	beat(t, a, tr, be, &ThrottleDirective{Level: 0, CPUFactor: 1})
	if strings.Contains(logs.String(), "restoring") {
		t.Fatalf("a runner still at its full allocation was announced as restored:\n%s", logs.String())
	}

	// Once the daemon recovers the retry lands.
	be.mu.Lock()
	be.updateErr = nil
	be.mu.Unlock()
	if got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5}); len(got) != 1 || got[0].res.CPUs != 1 {
		t.Fatalf("updates = %+v, want the retry to land once the daemon answers", got)
	}
	if got := beat(t, a, tr, be, &ThrottleDirective{Level: 0, CPUFactor: 1}); len(got) != 1 || got[0].res.CPUs != 2 {
		t.Fatalf("updates = %+v, want the runner restored now that it really was throttled", got)
	}
}

// A runner created while the host is throttled comes up at its full
// allocation, and on an overwhelmed host that is one more full-speed job
// until the next beat. It is throttled as soon as it exists.
func TestARunnerCreatedUnderAThrottleIsThrottledAtOnce(t *testing.T) {
	h := newHarness(t, 2)
	h.tr.mu.Lock()
	h.tr.beatResp = &HeartbeatResponse{OK: true, Throttle: &ThrottleDirective{Level: 2, CPUFactor: 0.5}}
	h.tr.mu.Unlock()
	if err := h.agent.heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	task := createTask("task-1", "runner-1")
	task.Spec.Resources = store.Resources{CPUs: 2, MemoryMB: 2048}
	h.tr.tasks <- []Task{task}
	res := h.nextResult()
	if !res.OK {
		t.Fatalf("create failed: %+v", res)
	}
	got := h.be.resourceUpdates()
	if len(got) != 1 || got[0].handle != res.Handle || got[0].res != (store.Resources{CPUs: 1, MemoryMB: 2048}) {
		t.Fatalf("updates = %+v, want the new runner throttled to half before its result was reported", got)
	}
}

// A runner created while nothing is throttled already has its full
// allocation; asking the daemon to confirm that on the next beat would be a
// request per create for nothing.
func TestARunnerCreatedWithoutAThrottleIsNotAskedAboutOnTheNextBeat(t *testing.T) {
	h := newHarness(t, 2)
	task := createTask("task-1", "runner-1")
	task.Spec.Resources = store.Resources{CPUs: 2}
	h.tr.tasks <- []Task{task}
	h.nextResult()

	h.tr.mu.Lock()
	h.tr.beatResp = &HeartbeatResponse{OK: true, Throttle: &ThrottleDirective{Level: 0, CPUFactor: 1}}
	h.tr.mu.Unlock()
	if err := h.agent.heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got := h.be.resourceUpdates(); len(got) != 0 {
		t.Fatalf("updates = %+v, want none for a runner created at its full allocation", got)
	}
}

// A finished runner has nothing left to throttle, and a runner the backend
// no longer has is not a refusal: neither is worth a warning or a retry.
func TestFinishedAndGoneRunnersAreNotThrottled(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	var logs lockedBuffer
	a.log = slog.New(slog.NewTextHandler(&logs, nil))
	createdRunner(a, "run_done", "wl-done", store.Resources{CPUs: 2})
	a.markTerminal("run_done", store.RunnerRemoved, backend.PhaseExited, 0, "done", "", a.now())
	createdRunner(a, "run_gone", "wl-gone", store.Resources{CPUs: 2})
	be.mu.Lock()
	be.updateErr = backend.ErrNotFound
	be.mu.Unlock()

	if got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5}); len(got) != 0 {
		t.Fatalf("updates = %+v", got)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("a gone runner was warned about:\n%s", logs.String())
	}
}

// lockedBuffer is a bytes.Buffer a logger can write from any goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Two callers can want the same runner at the same factor: a create throttling
// the runner it has just made, and the heartbeat that lands while it is doing
// so. applyThrottle chooses its candidates under the lock and then releases it
// to talk to the daemon, so the second caller used to see a runner whose
// applied factor had not been written yet and ask for the same quota again.
//
// The duplicate changes nothing, because the update is idempotent -- which is
// why it went unnoticed, surfacing only as a test that counted the calls and
// failed on a loaded machine. It is still a second daemon call per create on
// exactly the host that is already overwhelmed, which is the one place Zoomies
// should be asking for less.
func TestTwoCallersThrottlingOneRunnerAskTheDaemonOnce(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2, MemoryMB: 2048})

	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	be.mu.Lock()
	be.updateGate, be.updateEntered = gate, entered
	be.mu.Unlock()

	// The first caller, held inside the daemon call.
	a.mu.Lock()
	a.cpuFactor = 0.5
	a.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyThrottle(context.Background(), false)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first caller never reached the daemon")
	}

	// The gate is stood down before the second caller runs, so that a build
	// without the marker asks the daemon and fails the count below rather than
	// blocking behind the first caller and hanging the test. The first caller
	// captured its own gate on the way in and is still held.
	be.mu.Lock()
	be.updateGate, be.updateEntered = nil, nil
	be.mu.Unlock()

	// The second, arriving in exactly the window the marker exists to close.
	// It must find the runner spoken for and leave it alone.
	beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5})

	close(gate)
	<-done

	if got := be.resourceUpdates(); len(got) != 1 {
		t.Fatalf("updates = %+v, want one: the second caller asked for a quota already being set", got)
	}

	// And the marker is released, or a refusal would be held off for ever by
	// something nothing clears. The factor is applied by now, so a new one is
	// what proves the runner is still reachable.
	if got := beat(t, a, tr, be, &ThrottleDirective{Level: 0, CPUFactor: 1}); len(got) != 1 {
		t.Fatalf("updates = %+v, want the restore to land: the runner is still claimed by a call that finished", got)
	}
}

func TestStartupGraceKeepsNormalLimitsThenAppliesStandingThrottle(t *testing.T) {
	a, tr, be, clock := newAgent(t, 4)
	a.opts.BootstrapCPUGrace = 2 * time.Minute
	createdRunner(a, "new", "wl-new", store.Resources{CPUs: 1, MemoryMB: 2048})
	createdRunner(a, "old", "wl-old", store.Resources{CPUs: 1, MemoryMB: 2048})
	a.runners["old"].createdAt = clock.Now().Add(-time.Hour)
	directive := &ThrottleDirective{Level: 2, CPUFactor: 0.5}
	if got := beat(t, a, tr, be, directive); len(got) != 1 || got[0].handle != "wl-old" {
		t.Fatalf("startup throttled or old job left alone: %+v", got)
	}
	clock.advance(2 * time.Minute)
	got := beat(t, a, tr, be, directive)
	if len(got) != 1 || got[0].handle != "wl-new" || got[0].res.CPUs != 0.5 || got[0].res.MemoryMB != 2048 {
		t.Fatalf("grace did not expire under unchanged directive: %+v", got)
	}
}

func TestDinDCreationAndAdoptionUseTheSamePerContainerThrottleBudget(t *testing.T) {
	for _, source := range []string{store.AllocationFromHost, store.AllocationFromPool, ""} {
		t.Run(source, func(t *testing.T) {
			a, tr, be, _ := newAgent(t, 4)
			task := createTask("task", "runner")
			task.Spec.DockerMode = store.DockerDinD
			task.Spec.ResourcesSource = source
			task.Spec.Resources = store.Resources{CPUs: 4, MemoryMB: 8192}
			a.handleCreate(context.Background(), task, func() {})
			want := 4.0
			if source == store.AllocationFromHost {
				want = 2
			}
			if got := beat(t, a, tr, be, &ThrottleDirective{CPUFactor: 0.5}); len(got) != 1 || got[0].res.CPUs != want/2 {
				t.Fatalf("throttle: %+v, base %v", got, want)
			}
			if got := beat(t, a, tr, be, &ThrottleDirective{CPUFactor: 1}); len(got) != 1 || got[0].res.CPUs != want {
				t.Fatalf("restoration exceeded container budget: %+v, want %v", got, want)
			}
		})
	}
}

func TestAdoptionDoesNotRestartTheBootstrapGrace(t *testing.T) {
	a, tr, be, clock := newAgent(t, 4)
	a.opts.BootstrapCPUGrace = 2 * time.Minute
	be.setWorkloads(backend.Workload{Handle: "old", RunnerID: "old", Status: backend.Status{Phase: backend.PhaseRunning, StartedAt: clock.Now().Add(-time.Hour)}, Resources: store.Resources{CPUs: 1}})
	a.adoptExisting(context.Background())
	if got := beat(t, a, tr, be, &ThrottleDirective{CPUFactor: 0.5}); len(got) != 1 || got[0].res.CPUs != 0.5 {
		t.Fatalf("old adopted runner gained startup grace: %+v", got)
	}
}

// A boost is lent on the controller's word and only withdrawn by its next plan.
// With the controller gone no plan comes, so the agent gives the CPU back
// itself -- not on the first missed beat, which is a restart, but once enough
// have gone by that nothing is coming. A throttle is left standing: it is the
// safe direction to be wrong in.
func TestBoostsAreGivenBackWhenTheControllerStopsAnswering(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2, MemoryMB: 4096})
	if got := beat(t, a, tr, be, &ThrottleDirective{CPUFactor: 1}, ElasticCPUDirective{
		RunnerID: "run_a", CPUFactor: 2, BaseCPUs: 2, TargetCPUs: 4,
	}); len(got) != 1 || got[0].res.CPUs != 4 {
		t.Fatalf("updates = %+v, want the runner lent 4 CPUs", got)
	}

	tr.mu.Lock()
	tr.beatErr = errors.New("controller unreachable")
	tr.mu.Unlock()
	miss := func() []resourceUpdate {
		before := len(be.resourceUpdates())
		if err := a.heartbeat(context.Background()); err == nil {
			t.Fatal("heartbeat succeeded against an unreachable controller")
		}
		return be.resourceUpdates()[before:]
	}
	for i := 1; i < boostExpiryMisses; i++ {
		if got := miss(); len(got) != 0 {
			t.Fatalf("after %d missed beat(s) the agent changed %+v; a restart should not cost a running job its boost", i, got)
		}
	}
	if got := miss(); len(got) != 1 || got[0].res.CPUs != 2 {
		t.Fatalf("after %d missed beats updates = %+v, want the runner back at its 2 CPU guarantee", boostExpiryMisses, got)
	}
	if got := miss(); len(got) != 0 {
		t.Fatalf("a further missed beat changed %+v; a boost given back is given back once", got)
	}
}

func TestAThrottleOutlastsAControllerThatStopsAnswering(t *testing.T) {
	a, tr, be, _ := newAgent(t, 4)
	createdRunner(a, "run_a", "wl-a", store.Resources{CPUs: 2})
	a.opts.BootstrapCPUGrace = 0
	if got := beat(t, a, tr, be, &ThrottleDirective{Level: 2, CPUFactor: 0.5}); len(got) != 1 || got[0].res.CPUs != 1 {
		t.Fatalf("updates = %+v, want the runner throttled to 1 CPU", got)
	}
	tr.mu.Lock()
	tr.beatErr = errors.New("controller unreachable")
	tr.mu.Unlock()
	before := len(be.resourceUpdates())
	for range boostExpiryMisses + 2 {
		_ = a.heartbeat(context.Background())
	}
	if got := be.resourceUpdates()[before:]; len(got) != 0 {
		t.Fatalf("a missing controller lifted the throttle: %+v; only a boost is given back", got)
	}
}
