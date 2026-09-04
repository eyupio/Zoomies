package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
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
