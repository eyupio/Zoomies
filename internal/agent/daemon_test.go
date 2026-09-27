package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// harness starts a joined agent against fakes and stops it when the test ends.
type harness struct {
	t      *testing.T
	agent  *Agent
	tr     *fakeTransport
	be     *fakeBackend
	clock  *testClock
	cancel context.CancelFunc
	done   chan error

	stopOnce sync.Once
	stopErr  error
}

func newAgent(t *testing.T, capacity int) (*Agent, *fakeTransport, *fakeBackend, *testClock) {
	t.Helper()
	tr := newFakeTransport()
	be := newFakeBackend(store.BackendDocker)
	clock := newTestClock()
	a, err := New(Options{
		Name:              "test-host",
		WorkDir:           t.TempDir(),
		Capacity:          capacity,
		Backends:          backend.NewRegistry(be),
		DefaultBackend:    store.BackendDocker,
		Transport:         tr,
		HeartbeatInterval: time.Second,
		Logger:            testLogger(),
		Clock:             clock.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, tr, be, clock
}

func newHarness(t *testing.T, capacity int) *harness {
	t.Helper()
	a, tr, be, clock := newAgent(t, capacity)
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatalf("Join: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, agent: a, tr: tr, be: be, clock: clock, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- a.Run(ctx) }()
	t.Cleanup(func() { h.stop() })
	return h
}

// stop shuts the agent down and returns what Run returned. It is idempotent so
// that a test can stop the agent itself and still have the cleanup run.
func (h *harness) stop() error {
	h.stopOnce.Do(func() {
		h.cancel()
		select {
		case h.stopErr = <-h.done:
		case <-time.After(10 * time.Second):
			h.t.Error("agent did not shut down within 10s")
		}
	})
	return h.stopErr
}

func (h *harness) nextResult() TaskResult {
	h.t.Helper()
	select {
	case res := <-h.tr.results:
		return res
	case <-time.After(5 * time.Second):
		h.t.Fatal("no task result reported within 5s")
		return TaskResult{}
	}
}

func createTask(id, runnerID string) Task {
	return Task{
		ID:       id,
		Kind:     TaskCreateRunner,
		RunnerID: runnerID,
		Spec: &backend.Spec{
			Name:        "runner-" + runnerID,
			RunnerID:    runnerID,
			PoolID:      "pool-1",
			Image:       "ghcr.io/example/runner:latest",
			Ephemeral:   true,
			DockerMode:  store.DockerNone,
			Credentials: backend.Credentials{JITConfig: "jit-blob"},
		},
	}
}

func TestCreatePropagatesBackendDigest(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{createTask("digest-task", "digest-runner")}
	res := h.nextResult()
	if !res.OK || res.Digest != "sha256:resolved" {
		t.Fatalf("create result = %+v, want resolved backend digest", res)
	}
}

func TestJoinPersistsCredentials(t *testing.T) {
	a, tr, _, _ := newAgent(t, 1)
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if a.HostID() != "host-1" {
		t.Fatalf("HostID = %q, want host-1", a.HostID())
	}
	if tr.joinReq.Name != "test-host" || tr.joinReq.Capacity != 1 {
		t.Fatalf("join request did not carry the host identity: %+v", tr.joinReq)
	}
	if len(tr.joinReq.Backends) != 1 || !tr.joinReq.Backends[0].Available {
		t.Fatalf("join request did not report probed backends: %+v", tr.joinReq.Backends)
	}

	creds, err := Load(StatePath(a.opts.WorkDir))
	if err != nil {
		t.Fatalf("Load after Join: %v", err)
	}
	if creds.HostID != "host-1" || creds.AgentToken != "agent-token" {
		t.Fatalf("persisted credentials = %+v", creds)
	}
}

func TestCreateTaskCreatesRunnerAndReports(t *testing.T) {
	h := newHarness(t, 2)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}

	res := h.nextResult()
	if !res.OK || res.TaskID != "task-1" || res.RunnerID != "runner-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.State != store.RunnerRegistering {
		t.Fatalf("State = %q, want %q", res.State, store.RunnerRegistering)
	}
	if res.Handle == "" {
		t.Fatal("result carried no handle, so the controller cannot address the workload")
	}

	created, _, _ := h.be.counts()
	if created != 1 {
		t.Fatalf("Create called %d times, want 1", created)
	}
	if got := h.agent.Runners(); len(got) != 1 || got[0].RunnerID != "runner-1" {
		t.Fatalf("Runners() = %+v", got)
	}
}

func TestDuplicateTaskForSameRunnerIsSkipped(t *testing.T) {
	h := newHarness(t, 4)
	h.be.mu.Lock()
	h.be.createDelay = 150 * time.Millisecond
	h.be.mu.Unlock()

	// Both tasks name one runner, which is what a controller redelivery looks
	// like on the wire.
	h.tr.tasks <- []Task{createTask("task-1", "runner-1"), createTask("task-1-again", "runner-1")}

	res := h.nextResult()
	if !res.OK {
		t.Fatalf("first task failed: %+v", res)
	}
	select {
	case extra := <-h.tr.results:
		t.Fatalf("duplicate task also ran and reported %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
	if created, _, _ := h.be.counts(); created != 1 {
		t.Fatalf("Create called %d times, want 1", created)
	}
}

func TestConcurrentTasksNeverExceedCapacity(t *testing.T) {
	const capacity = 2
	h := newHarness(t, capacity)
	h.be.mu.Lock()
	h.be.createDelay = 60 * time.Millisecond
	h.be.mu.Unlock()

	var batch []Task
	for i := range 8 {
		batch = append(batch, createTask("task-"+strconv.Itoa(i), "runner-"+strconv.Itoa(i)))
	}
	h.tr.tasks <- batch

	for range batch {
		if res := h.nextResult(); !res.OK {
			t.Fatalf("task failed: %+v", res)
		}
	}
	if peak := h.be.peakConcurrency(); peak > capacity {
		t.Fatalf("ran %d creates at once, capacity is %d", peak, capacity)
	}
}

func TestFailedCreateReportsUsefulError(t *testing.T) {
	h := newHarness(t, 1)
	h.be.mu.Lock()
	h.be.createErr = errors.New("pull access denied for ghcr.io/example/runner")
	h.be.mu.Unlock()

	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	res := h.nextResult()
	if res.OK {
		t.Fatal("a failing Create reported OK")
	}
	if res.State != store.RunnerFailed {
		t.Fatalf("State = %q, want %q", res.State, store.RunnerFailed)
	}
	if !strings.Contains(res.Error, "docker") || !strings.Contains(res.Error, "pull access denied") {
		t.Fatalf("error message names neither the backend nor the cause: %q", res.Error)
	}
}

// A create that fails only because the daemon is busy is retried with a
// growing wait between tries rather than failing the runner on the first
// timeout: the daemon has more work than it can answer right now, often only
// for a little longer, not a reason to give up on this create and make the
// pool wait out its own, coarser backoff for a replacement instead.
func TestABusyCreateIsRetriedBeforeFailing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 1)
		h.be.mu.Lock()
		h.be.createErr = fmt.Errorf("docker api: %w", backend.ErrDaemonBusy)
		h.be.createFailTimes = 2
		h.be.mu.Unlock()

		h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
		synctest.Wait()
		res := <-h.tr.results
		if !res.OK {
			t.Fatalf("a create that recovered within its retries reported failure: %+v", res)
		}
		if created, _, _ := h.be.counts(); created != 3 {
			t.Fatalf("Create called %d times, want 3: the first attempt and two retries before it succeeded", created)
		}
	})
}

// Retries are spent only on the one fault a retry can fix. A failure of any
// other kind -- here, a registry refusing the image -- fails on the first
// attempt exactly as it did before retrying existed: nothing about waiting
// and asking the same daemon the same thing again would change that answer.
func TestANonBusyCreateFailureIsNotRetried(t *testing.T) {
	h := newHarness(t, 1)
	h.be.mu.Lock()
	h.be.createErr = errors.New("pull access denied for ghcr.io/example/runner")
	h.be.createFailTimes = 1
	h.be.mu.Unlock()

	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	res := h.nextResult()
	if res.OK {
		t.Fatal("a failing Create reported OK")
	}
	if created, _, _ := h.be.counts(); created != 1 {
		t.Fatalf("Create called %d times, want 1: a non-busy failure must not be retried", created)
	}
}

// Retries exhaust rather than loop forever: a daemon that stays busy through
// every attempt still fails the runner, just later than the first timeout, so
// the pool's own replacement and backoff take over as they always have.
func TestABusyCreateThatNeverClearsStillFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, 1)
		h.be.mu.Lock()
		h.be.createErr = fmt.Errorf("docker api: %w", backend.ErrDaemonBusy)
		h.be.mu.Unlock()

		h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
		synctest.Wait()
		res := <-h.tr.results
		if res.OK {
			t.Fatal("a create that never recovered reported OK")
		}
		want := createBusyRetries + 1
		if created, _, _ := h.be.counts(); created != want {
			t.Fatalf("Create called %d times, want %d: the first attempt and every retry", created, want)
		}
	})
}

func TestUnknownTaskKindIsStillReported(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{{ID: "task-1", Kind: TaskKind("teleport_runner"), RunnerID: "runner-1"}}

	res := h.nextResult()
	if res.OK || res.TaskID != "task-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Error, "teleport_runner") {
		t.Fatalf("error does not name the unknown kind: %q", res.Error)
	}
}

func TestCreateTaskWithoutSpecIsReported(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{{ID: "task-1", Kind: TaskCreateRunner, RunnerID: "runner-1"}}

	res := h.nextResult()
	if res.OK || !strings.Contains(res.Error, "no spec") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRemoveTaskRemovesWorkload(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}

	h.tr.tasks <- []Task{{ID: "task-2", Kind: TaskRemoveRunner, RunnerID: "runner-1"}}
	res := h.nextResult()
	if !res.OK || res.State != store.RunnerRemoved {
		t.Fatalf("unexpected remove result: %+v", res)
	}
	if _, _, removed := h.be.counts(); removed != 1 {
		t.Fatalf("Remove called %d times, want 1", removed)
	}
	if got := h.agent.Runners(); len(got) != 0 {
		t.Fatalf("runner still tracked after removal: %+v", got)
	}
}

func TestRunnerIsClaimableAgainAsSoonAsItsResultIsReported(t *testing.T) {
	// A controller sends the next task for a runner the moment it sees the
	// last one's result -- a remove straight after a create is the ordinary
	// case. The agent skips a task for a runner that already has one in
	// flight, and skipping is silent, so if the finished task still held its
	// claim when the result went out, that next task would be dropped and left
	// waiting on redelivery. Nothing in the test suite could see the window:
	// it is only as wide as a goroutine takes to unwind its defers, which is
	// why this asks the question from inside the report itself.
	h := newHarness(t, 1)

	claimable := make(chan bool, 4)
	h.tr.mu.Lock()
	h.tr.onResult = func(res TaskResult) {
		if res.RunnerID == "" {
			return
		}
		// What the controller's next task would find.
		got := h.agent.claim(res.RunnerID)
		if got {
			h.agent.release(res.RunnerID)
		}
		claimable <- got
	}
	h.tr.mu.Unlock()

	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}
	select {
	case free := <-claimable:
		if !free {
			t.Fatal("runner-1 was still claimed by the create when its result was reported, so the controller's next task for it would have been skipped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the result was never reported")
	}

	// And the create's own goroutine, unwinding afterwards, must not release a
	// claim that by then belongs to somebody else.
	if !h.agent.claim("runner-1") {
		t.Fatal("runner-1 could not be claimed after the create finished")
	}
	time.Sleep(50 * time.Millisecond)
	h.agent.mu.Lock()
	held := h.agent.inflight["runner-1"]
	h.agent.mu.Unlock()
	if !held {
		t.Fatal("claim on runner-1 was released by the finished task")
	}
	h.agent.release("runner-1")
}

func TestStopTaskUsesTheTaskTimeout(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}

	h.tr.tasks <- []Task{{ID: "task-2", Kind: TaskStopRunner, RunnerID: "runner-1", StopTimeout: time.Minute}}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("unexpected stop result: %+v", res)
	}
	if _, stopped, _ := h.be.counts(); stopped != 1 {
		t.Fatalf("Stop called %d times, want 1", stopped)
	}
}

func TestShutdownLeavesRunnersRunning(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}

	if err := h.stop(); err != nil {
		t.Fatalf("Run returned %v, want nil on a clean shutdown", err)
	}
	// A restarting agent that killed its runners would kill the jobs on them.
	if _, stopped, removed := h.be.counts(); stopped != 0 || removed != 0 {
		t.Fatalf("shutdown stopped %d and removed %d workloads; it must touch neither", stopped, removed)
	}
	workloads, _ := h.be.List(context.Background())
	if len(workloads) != 1 {
		t.Fatalf("workloads after shutdown = %d, want 1", len(workloads))
	}
	select {
	case <-h.tr.reports:
	default:
		t.Fatal("shutdown did not flush a final runner report")
	}
}

func TestHeartbeatCarriesCapacityAndRunners(t *testing.T) {
	h := newHarness(t, 3)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case beat := <-h.tr.beats:
			if beat.Capacity != 3 {
				t.Fatalf("heartbeat capacity = %d, want 3", beat.Capacity)
			}
			if len(beat.Runners) == 1 && beat.Runners[0].RunnerID == "runner-1" {
				return
			}
		case <-deadline:
			t.Fatal("no heartbeat carried the created runner within 5s")
		}
	}
}

func TestHostGoneStopsTheAgentWithInstructions(t *testing.T) {
	a, tr, _, _ := newAgent(t, 1)
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.mu.Lock()
	tr.beatErr = ErrHostGone
	tr.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Run(ctx)
	if !errors.Is(err, ErrHostGone) {
		t.Fatalf("Run error = %v, want one wrapping ErrHostGone", err)
	}
	if !strings.Contains(err.Error(), "zoomies agent join") {
		t.Fatalf("error does not tell the operator how to recover: %v", err)
	}
}

func TestRunWithoutCredentialsExplainsHowToJoin(t *testing.T) {
	a, _, _, _ := newAgent(t, 1)
	err := a.Run(context.Background())
	if !errors.Is(err, ErrNotJoined) {
		t.Fatalf("Run error = %v, want ErrNotJoined", err)
	}
	if !strings.Contains(err.Error(), "zoomies agent join") {
		t.Fatalf("error does not tell the operator how to join: %v", err)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	base := func() Options {
		return Options{
			Name:      "h",
			WorkDir:   t.TempDir(),
			Capacity:  1,
			Backends:  backend.NewRegistry(newFakeBackend(store.BackendDocker)),
			Transport: newFakeTransport(),
			Logger:    testLogger(),
		}
	}
	cases := map[string]func(*Options){
		"name":      func(o *Options) { o.Name = "" },
		"work_dir":  func(o *Options) { o.WorkDir = "" },
		"capacity":  func(o *Options) { o.Capacity = 0 },
		"backends":  func(o *Options) { o.Backends = backend.NewRegistry() },
		"transport": func(o *Options) { o.Transport = nil },
		"backend":   func(o *Options) { o.DefaultBackend = store.BackendKind("kubernetes") },
		"heartbeat": func(o *Options) { o.HeartbeatInterval = time.Millisecond },
		"retention": func(o *Options) { o.FinishedRetention = -time.Minute },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			opts := base()
			break_(&opts)
			if _, err := New(opts); err == nil {
				t.Fatal("New accepted invalid options")
			}
		})
	}

	// One registered backend is enough to infer the default.
	opts := base()
	a, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.opts.DefaultBackend != store.BackendDocker {
		t.Fatalf("DefaultBackend = %q, want docker", a.opts.DefaultBackend)
	}
}

func TestStopTaskDoesNotClaimRemovalWhenTheBackendWillNotAnswer(t *testing.T) {
	h := newHarness(t, 1)
	h.be.mu.Lock()
	h.be.listErr = backend.ErrUnavailable
	h.be.mu.Unlock()

	// The agent has no record of this runner, so it has to ask the host -- and
	// the host will not say. Reporting "removed" here would tell the controller
	// a runner is gone while its job is still running.
	h.tr.tasks <- []Task{{ID: "task-1", Kind: TaskStopRunner, RunnerID: "runner-unknown"}}
	res := h.nextResult()
	if res.OK {
		t.Fatalf("unexpected success: %+v", res)
	}
	if res.State == store.RunnerRemoved {
		t.Fatalf("claimed the runner was removed without being able to look: %+v", res)
	}
	if !strings.Contains(res.Error, "nothing was changed") {
		t.Fatalf("error does not say the host was left alone: %q", res.Error)
	}
	if _, stopped, _ := h.be.counts(); stopped != 0 {
		t.Fatal("stopped something despite not knowing what")
	}
}

// The common startup race: the agent comes up before its container daemon, or
// before its user's new docker group membership is in effect, and joins saying
// it can run nothing. A host with no backends matches no pool, so every pool on
// that backend looks healthy while its jobs queue forever. The agent has to
// find the daemon on its own once it appears.
func TestAgentReprobesUntilABackendAnswers(t *testing.T) {
	a, tr, be, _ := newAgent(t, 1)
	be.setUnavailable(true)
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.mu.Lock()
	joined := tr.joinReq
	tr.mu.Unlock()
	if len(joined.Backends) != 1 || joined.Backends[0].Available {
		t.Fatalf("join backends = %+v, want docker reported as unavailable", joined.Backends)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// The daemon arrives while the agent is running. Nothing restarts.
	be.setUnavailable(false)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case beat := <-tr.beats:
			if len(beat.Backends) == 1 && beat.Backends[0].Available {
				cancel()
				<-done
				return
			}
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no heartbeat reported the backend within 10s of it becoming available")
		}
	}
}

// Once something answers, the probe settles down: it is a ping per backend, and
// a fleet of hosts doing it every heartbeat is noise for no news.
func TestAgentDoesNotReprobeOnEveryHeartbeatWhenHealthy(t *testing.T) {
	h := newHarness(t, 1)

	// The join probe is the one that has happened so far.
	if got := h.be.probeCount(); got != 1 {
		t.Fatalf("probes after join = %d, want 1", got)
	}
	for range 3 {
		select {
		case <-h.tr.beats:
		case <-time.After(5 * time.Second):
			t.Fatal("no heartbeat within 5s")
		}
	}
	if got := h.be.probeCount(); got != 1 {
		t.Fatalf("probes = %d after three heartbeats, want the one from join", got)
	}

	// Time passes, and the agent looks again: a daemon can be stopped or
	// upgraded under a running agent.
	h.clock.advance(backendProbeInterval)
	deadline := time.After(5 * time.Second)
	for h.be.probeCount() == 1 {
		select {
		case <-h.tr.beats:
		case <-deadline:
			t.Fatalf("no probe within 5s of %s passing", backendProbeInterval)
		}
	}
}

// A container runner's scratch space is its own filesystem, never the agent's
// work directory: that directory is shared by every runner on the host, owned
// by the wrong account for the image, and -- when the agent is itself a
// container -- a path the host daemon cannot even see. Mounting it produced a
// runner whose first job failed on an empty root-owned _work.
func TestCreateTaskDoesNotHandTheAgentsWorkDirToTheBackend(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}

	h.be.mu.Lock()
	defer h.be.mu.Unlock()
	if len(h.be.created) != 1 {
		t.Fatalf("created %d runners, want 1", len(h.be.created))
	}
	if got := h.be.created[0].WorkDir; got != "" {
		t.Fatalf("the backend was given work dir %q; the runner should use its own filesystem", got)
	}
}

// Delivery is at-least-once: a create whose result was lost is offered again
// once its lease expires. The backend's Create begins by removing a workload of
// the same name, so the redelivery used to destroy a runner that might be
// mid-job and rebuild it with a JIT configuration GitHub had already used.
func TestARedeliveredCreateReportsTheRunnerThatAlreadyExists(t *testing.T) {
	h := newHarness(t, 2)
	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	first := h.nextResult()
	if !first.OK || first.Handle == "" {
		t.Fatalf("first create: %+v", first)
	}

	h.tr.tasks <- []Task{createTask("task-1-again", "runner-1")}
	again := h.nextResult()
	if !again.OK || again.TaskID != "task-1-again" || again.RunnerID != "runner-1" {
		t.Fatalf("redelivered create: %+v", again)
	}
	if again.Handle != first.Handle {
		t.Fatalf("the redelivery reported handle %q, want the existing %q", again.Handle, first.Handle)
	}
	if created, _, _ := h.be.counts(); created != 1 {
		t.Fatalf("Create called %d times, want the one that made the runner", created)
	}
}

// The controller sheds load by asking every agent it is holding to wait, so it
// asks them all in the same instant. Waiting exactly as long as it asked would
// bring the whole fleet back together and re-form the queue the backoff was
// spreading out, so the wait is jittered.
func TestAShedBackoffIsSpreadRatherThanWaitedExactly(t *testing.T) {
	const asked = 10 * time.Second
	batch := &TaskBatch{Backoff: asked}

	seen := map[time.Duration]bool{}
	for range 50 {
		got := pollWait(batch, 0)
		if got <= 0 || got > asked {
			t.Fatalf("wait of %s for a %s backoff: it should be shorter, and it should still be a wait", got, asked)
		}
		seen[got] = true
	}
	if len(seen) == 1 {
		t.Fatal("every agent asked to wait would return in the same instant, which is the queue the backoff exists to spread")
	}
}

// A backoff is the controller's answer, not a suggestion to weigh against the
// spin guard: an agent that found no work still waits for it.
func TestABackoffOutranksTheSpinGuard(t *testing.T) {
	if got := pollWait(&TaskBatch{Backoff: time.Second}, time.Hour); got < 500*time.Millisecond {
		t.Fatalf("wait = %s after a poll that took an hour, want most of the second the controller asked for", got)
	}
}

// A batch with work in it is dispatched and polled again straight away: the
// spin guard exists for an idle loop, and applying it to a busy one would put
// a floor under how fast a host can be given its next task.
func TestABatchWithWorkIsPolledAgainImmediately(t *testing.T) {
	batch := &TaskBatch{Tasks: []Task{{ID: "t1", Kind: TaskCreateRunner}}}
	if got := pollWait(batch, 0); got != 0 {
		t.Fatalf("wait = %s after a batch with work in it, want none", got)
	}
}

// An idle poll the controller answered instantly is held off, or the loop
// would spin at whatever rate it can dial.
func TestAnInstantIdlePollIsHeldOff(t *testing.T) {
	if got := pollWait(&TaskBatch{}, 0); got != minPollInterval {
		t.Fatalf("wait = %s after an instant empty poll, want the %s floor", got, minPollInterval)
	}
	if got := pollWait(&TaskBatch{}, minPollInterval); got != 0 {
		t.Fatalf("wait = %s after a poll that already took the floor, want none", got)
	}
}

// TestEveryPrewarmInABatchIsPulled covers a collision between tasks that have
// no runner between them.
//
// dispatch claims a task by its runner ID so that two lifecycle tasks for one
// runner cannot race on the host. A prewarm carries no runner, so every prewarm
// claimed the same empty key: the first took it and the rest returned at once,
// reporting nothing. Two Docker pools on one host is all it takes, and the loss
// is permanent rather than a retry away -- a task that reports no result stays
// outstanding on the controller, and enqueue refuses a key that is already in
// flight, so that pool is never prewarmed on that host again.
func TestEveryPrewarmInABatchIsPulled(t *testing.T) {
	h := newHarness(t, 2)

	h.tr.tasks <- []Task{
		{ID: "prewarm-a", Kind: TaskPrewarmImage, PoolID: "pool_a", Backend: h.be.Kind(), Image: "img-a", PullPolicy: store.PullIfNotPresent},
		{ID: "prewarm-b", Kind: TaskPrewarmImage, PoolID: "pool_b", Backend: h.be.Kind(), Image: "img-b", PullPolicy: store.PullIfNotPresent},
	}

	// Both have to report, or the controller's lease on the silent one never
	// clears.
	seen := map[string]bool{}
	for range 2 {
		res := h.nextResult()
		if !res.OK {
			t.Errorf("prewarm %s failed: %s", res.TaskID, res.Error)
		}
		seen[res.TaskID] = true
	}
	for _, id := range []string{"prewarm-a", "prewarm-b"} {
		if !seen[id] {
			t.Errorf("%s reported no result; the controller will wait on it for ever", id)
		}
	}

	pulls := h.be.pulls()
	slices.Sort(pulls)
	if !slices.Equal(pulls, []string{"img-a", "img-b"}) {
		t.Errorf("images pulled = %v, want both img-a and img-b", pulls)
	}
}

// A remove task that finds the daemon still removing the workload answers
// that it is pending rather than failed. The controller counted every such
// answer as a failed cleanup, and it re-sends a remove on every scheduling
// pass while a host's cleanup is outstanding, so one slow sidecar climbed
// through "attempts" while the daemon was finishing the job.
func TestARemoveTaskFindingTheDaemonStillRemovingAnswersPending(t *testing.T) {
	a, tr, be, clock := newAgent(t, 2)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 0))
	be.mu.Lock()
	be.removeErr = errStillRemoving
	be.mu.Unlock()
	ctx := context.Background()
	remove := func(id string) TaskResult {
		t.Helper()
		a.handleRemove(ctx, Task{ID: id, Kind: TaskRemoveRunner, RunnerID: "runner-1"}, func() {})
		select {
		case res := <-tr.results:
			return res
		default:
			t.Fatalf("task %s was not answered", id)
		}
		return TaskResult{}
	}

	res := remove("task-1")
	if res.OK || !res.CleanupPending || !strings.Contains(res.Error, "still removing") {
		t.Fatalf("a removal under way was not answered as pending: %+v", res)
	}
	if got := a.Runners(); len(got) != 1 {
		t.Fatalf("forgot a runner whose workload is still being removed: %+v", got)
	}

	// Past the bound it is stuck, and answered as the failure it has become.
	clock.advance(removalSettle)
	res = remove("task-2")
	if res.OK || res.CleanupPending || !strings.Contains(res.Error, "restart") {
		t.Fatalf("a removal stuck past its bound was still answered as pending: %+v", res)
	}

	be.mu.Lock()
	be.removeErr = nil
	be.mu.Unlock()
	res = remove("task-3")
	if !res.OK || res.CleanupPending || res.State != store.RunnerRemoved {
		t.Fatalf("the finished removal was not confirmed: %+v", res)
	}
}

// A plain refusal is still a failure the controller must hear about at once.
func TestARemoveTaskThatFailsIsNotAnsweredAsPending(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 0))
	be.mu.Lock()
	be.removeErr = errors.New("sidecar is in use")
	be.mu.Unlock()

	a.handleRemove(context.Background(), Task{ID: "task-1", Kind: TaskRemoveRunner, RunnerID: "runner-1"}, func() {})
	res := <-tr.results
	if res.OK || res.CleanupPending || !strings.Contains(res.Error, "sidecar is in use") {
		t.Fatalf("a refused removal = %+v, want an ordinary failure", res)
	}
}

// An operator removing a runner while its create is still running must still
// get the runner removed.
//
// The two tasks arrive together -- the controller queues the remove the moment
// the row is deleted, which can be seconds after the create was issued -- and
// the claim that stops two creates racing used to drop the remove with them.
// Nothing then reported on it, so the container ran on the host until the
// task's lease expired minutes later, long after the runner had gone from the
// UI. The later task waits for the claim instead of being thrown away.
func TestARemoveArrivingDuringACreateRunsOnceTheCreateIsDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, tr, b := startupAgent(t)
		ctx := context.Background()
		a.dispatch(ctx, createTask("task-create", "runner-1"))
		synctest.Wait()
		startupEntered(t, b, "runner-1")
		// Deliver after the backend has begun, to distinguish cancellation
		// of active work from a create still waiting for its startup slot.
		a.dispatch(ctx, Task{ID: "task-remove", Kind: TaskRemoveRunner, RunnerID: "runner-1"})
		b.finish <- nil
		synctest.Wait()
		a.tasks.Wait()

		results := map[string]TaskResult{}
		for range 2 {
			res := <-tr.results
			results[res.TaskID] = res
		}
		if !results["task-create"].OK || !results["task-remove"].OK {
			t.Fatalf("lifecycle tasks failed: %+v", results)
		}
		if results["task-remove"].State != store.RunnerRemoved {
			t.Fatalf("remove did not confirm removal: %+v", results)
		}
		if _, _, removed := b.counts(); removed == 0 {
			t.Fatal("the runner's workload was left on the host")
		}
		if !a.claim("runner-1") {
			t.Fatal("runner-1 is still claimed after both tasks finished")
		}
		a.release("runner-1")
	})
}

// A duplicate of the task already running is still dropped. The controller
// redelivers what it has not seen a result for, and doing that work twice --
// two creates for one runner racing on the host -- is what the claim exists to
// prevent.
func TestADuplicateOfTheTaskInFlightIsStillDropped(t *testing.T) {
	a, _, _, _ := newAgent(t, 1)
	first := createTask("task-1", "runner-1")
	if claimed, held := a.claimOrQueue(first); !claimed || held {
		t.Fatalf("first create: claimed=%v held=%v, want claimed", claimed, held)
	}
	if claimed, held := a.claimOrQueue(createTask("task-2", "runner-1")); claimed || held {
		t.Fatalf("duplicate create: claimed=%v held=%v, want dropped", claimed, held)
	}

	// A remove supersedes it, and a second remove behind that one does not.
	remove := Task{ID: "task-3", Kind: TaskRemoveRunner, RunnerID: "runner-1"}
	if claimed, held := a.claimOrQueue(remove); claimed || !held {
		t.Fatalf("remove during a create: claimed=%v held=%v, want held", claimed, held)
	}
	if claimed, held := a.claimOrQueue(Task{ID: "task-4", Kind: TaskRemoveRunner, RunnerID: "runner-1"}); claimed || held {
		t.Fatalf("second remove: claimed=%v held=%v, want dropped", claimed, held)
	}
}

// Only one task waits, so when two supersede the create in flight it is the
// later of them that stays: a remove does everything the stop it replaces
// would have, and the controller redelivers the stop if it still wants it.
func TestTheLaterOfTwoWaitingTasksIsTheOneKept(t *testing.T) {
	a, _, _, _ := newAgent(t, 1)
	if claimed, _ := a.claimOrQueue(createTask("task-1", "runner-1")); !claimed {
		t.Fatal("the create did not take the claim")
	}
	if claimed, held := a.claimOrQueue(Task{ID: "task-2", Kind: TaskStopRunner, RunnerID: "runner-1"}); claimed || !held {
		t.Fatalf("stop during a create: claimed=%v held=%v, want held", claimed, held)
	}
	if claimed, held := a.claimOrQueue(Task{ID: "task-3", Kind: TaskRemoveRunner, RunnerID: "runner-1"}); claimed || !held {
		t.Fatalf("remove behind the waiting stop: claimed=%v held=%v, want held", claimed, held)
	}

	a.mu.Lock()
	waiting := a.waiting["runner-1"]
	a.mu.Unlock()
	if waiting.ID != "task-3" {
		t.Fatalf("the task waiting is %q, want the remove that supersedes the stop", waiting.ID)
	}

	// A log relay is not lifecycle work and ranks below all of them, so it is
	// never what a runner's claim is handed to.
	if claimed, held := a.claimOrQueue(Task{ID: "task-4", Kind: TaskStreamLogs, RunnerID: "runner-1", StreamID: "str_1"}); claimed || held {
		t.Fatalf("stream during a create: claimed=%v held=%v, want dropped", claimed, held)
	}
}

// A task that never got a capacity slot before the agent shut down is
// reported as such rather than dropped. Silence is the one outcome the
// controller cannot act on: it would hold the task in flight until its lease
// expired, where this answer has it redelivered to the next host that polls.
func TestATaskShutDownBeforeItStartedIsReportedAsRedeliverable(t *testing.T) {
	h := newHarness(t, 1)
	h.be.mu.Lock()
	h.be.createDelay = 3 * time.Second
	h.be.mu.Unlock()

	// The first create takes the host's only slot and holds it, so the second
	// is still waiting for one when the agent is asked to stop.
	h.tr.tasks <- []Task{createTask("task-slow", "runner-1")}
	waitFn(t, "the first create to take the host's only slot", 5*time.Second, func() bool {
		h.be.mu.Lock()
		defer h.be.mu.Unlock()
		return h.be.inflight == 1
	})
	h.tr.tasks <- []Task{createTask("task-waiting", "runner-2")}
	time.Sleep(200 * time.Millisecond)
	go h.stop()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case res := <-h.tr.results:
			if res.TaskID != "task-waiting" {
				continue
			}
			if res.OK || !res.NotStarted || !strings.Contains(res.Error, "safe to redeliver") {
				t.Fatalf("result = %+v, want the waiting task reported as redeliverable", res)
			}
			// A failed state would have the controller fail a runner nothing
			// happened to, which is what every upgrade used to do.
			if res.State != "" {
				t.Fatalf("state = %q, want none: the runner was never touched", res.State)
			}
			return
		case <-deadline:
			t.Fatal("the task that never started was never reported")
		}
	}
}

// A claim released by an agent whose task loop is not running still hands the
// task waiting behind it on. The handover carries a context of its own rather
// than checking for a missing one, which is why this is the case that proves
// it: the agent here has never started, so there is no poll loop to borrow
// from, and a nil context would panic the goroutine the release starts.
func TestAHandoverWorksBeforeTheTaskLoopIsRunning(t *testing.T) {
	a, tr, _, _ := newAgent(t, 1)
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if claimed, _ := a.claimOrQueue(createTask("task-1", "runner-1")); !claimed {
		t.Fatal("the create did not take the claim")
	}
	remove := Task{ID: "task-2", Kind: TaskRemoveRunner, RunnerID: "runner-1"}
	if _, held := a.claimOrQueue(remove); !held {
		t.Fatal("the remove was not held behind the create")
	}

	a.release("runner-1")

	select {
	case res := <-tr.results:
		if res.TaskID != "task-2" || !res.OK {
			t.Fatalf("result = %+v, want the waiting remove reported", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the task waiting on the claim was never started")
	}
}

// The reconciler's claim is not one anything can queue behind: it hands the
// claim to nobody, so a task left waiting on it would never start. Those are
// dropped, and the controller's redelivery is what brings them back.
func TestATaskArrivingDuringReconcilerWorkIsDroppedRatherThanHeld(t *testing.T) {
	a, _, _, _ := newAgent(t, 1)
	if !a.claim("runner-1") {
		t.Fatal("could not take the reconciler's claim")
	}
	if claimed, held := a.claimOrQueue(Task{ID: "task-1", Kind: TaskRemoveRunner, RunnerID: "runner-1"}); claimed || held {
		t.Fatalf("remove during reconciler work: claimed=%v held=%v, want dropped", claimed, held)
	}
	a.release("runner-1")
}

// Model an upgrade while Docker is between sidecar creation and runner start.
// The old 30-second grace let Run return before the create result existed.
func TestShutdownFinishesAdmittedCreateAndKeepsReporting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, tr, b := startupAgent(t)
		if err := a.Join(context.Background(), "join-token"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		tr.tasks <- []Task{createTask("create", "runner-1")}
		synctest.Wait()
		startupEntered(t, b, "runner-1")
		cancel()
		synctest.Wait()
		for len(tr.beats) > 0 {
			<-tr.beats
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("agent exited with an unfinished create: %v", err)
		default:
		}
		if len(tr.beats) == 0 {
			t.Fatal("agent stopped reporting while its admitted create was running")
		}
		b.finish <- nil
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		select {
		case res := <-tr.results:
			if !res.OK || res.Handle == "" {
				t.Fatalf("create was not completed and reported: %+v", res)
			}
		default:
			t.Fatal("agent exited before reporting its create result")
		}
		if created, stopped, removed := b.counts(); created != 1 || stopped != 0 || removed != 0 {
			t.Fatalf("upgrade changed workloads: create=%d stop=%d remove=%d", created, stopped, removed)
		}
	})
}

func TestShutdownTaskWaitRemainsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, _, _ := newAgent(t, 1)
		a.tasks.Add(1)
		start := time.Now()
		if a.waitForTasks(context.Background(), time.Minute) {
			t.Fatal("unfinished work reported complete")
		}
		a.tasks.Done()
		synctest.Wait()
		if elapsed := time.Since(start); elapsed != time.Minute {
			t.Fatalf("wait took %s, want one minute", elapsed)
		}
	})
}
