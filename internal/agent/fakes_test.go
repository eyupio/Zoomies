package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// fakeBackend is a Backend that never touches a container runtime, so the whole
// agent can be exercised on a machine with no Docker daemon.
type fakeBackend struct {
	kind store.BackendKind

	mu          sync.Mutex
	nextHandle  int
	created     []backend.Spec
	stopped     []backend.Handle
	removed     []backend.Handle
	workloads   []backend.Workload
	createErr   error
	createDelay time.Duration
	removeErr   error
	listErr     error
	stats       backend.Stats
	logs        func() io.ReadCloser
	inflight    int
	maxInflight int
	// unavailable makes Probe answer as a daemon that is not there, which is
	// what a host looks like before its Docker is up. probes counts how often
	// the agent has asked.
	unavailable bool
	probes      int
}

func newFakeBackend(kind store.BackendKind) *fakeBackend {
	return &fakeBackend{kind: kind}
}

func (f *fakeBackend) Kind() store.BackendKind { return f.kind }

func (f *fakeBackend) Probe(context.Context) backend.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if f.unavailable {
		return backend.Info{
			Kind:   f.kind,
			Detail: "cannot connect to the daemon: no such file or directory",
		}
	}
	return backend.Info{Kind: f.kind, Available: true, Version: "fake", Endpoint: "memory"}
}

// setUnavailable flips what the next probe will find.
func (f *fakeBackend) setUnavailable(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable = v
}

func (f *fakeBackend) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

func (f *fakeBackend) Create(ctx context.Context, spec backend.Spec) (backend.Handle, error) {
	f.mu.Lock()
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	f.created = append(f.created, spec)
	f.nextHandle++
	handle := backend.Handle(fmt.Sprintf("wl-%d", f.nextHandle))
	delay, err := f.createDelay, f.createErr
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}

	f.mu.Lock()
	f.inflight--
	if err == nil {
		f.workloads = append(f.workloads, backend.Workload{
			Handle:   handle,
			Name:     spec.Name,
			RunnerID: spec.RunnerID,
			PoolID:   spec.PoolID,
			Status:   backend.Status{Handle: handle, Phase: backend.PhaseRunning},
		})
	}
	f.mu.Unlock()

	if err != nil {
		return "", err
	}
	return handle, nil
}

func (f *fakeBackend) Status(_ context.Context, h backend.Handle) (backend.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.workloads {
		if w.Handle == h {
			return w.Status, nil
		}
	}
	return backend.Status{}, backend.ErrNotFound
}

func (f *fakeBackend) Stats(context.Context, backend.Handle) (backend.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, nil
}

func (f *fakeBackend) Logs(context.Context, backend.Handle, backend.LogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	fn := f.logs
	f.mu.Unlock()
	if fn == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	return fn(), nil
}

func (f *fakeBackend) Stop(_ context.Context, h backend.Handle, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, h)
	return nil
}

func (f *fakeBackend) Remove(_ context.Context, h backend.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, h)
	f.workloads = slicesDelete(f.workloads, h)
	return nil
}

func (f *fakeBackend) List(context.Context) ([]backend.Workload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]backend.Workload, len(f.workloads))
	copy(out, f.workloads)
	return out, nil
}

func slicesDelete(ws []backend.Workload, h backend.Handle) []backend.Workload {
	out := ws[:0]
	for _, w := range ws {
		if w.Handle != h {
			out = append(out, w)
		}
	}
	return out
}

func (f *fakeBackend) setWorkloads(ws ...backend.Workload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workloads = ws
}

func (f *fakeBackend) counts() (created, stopped, removed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created), len(f.stopped), len(f.removed)
}

func (f *fakeBackend) peakConcurrency() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

// fakeTransport stands in for the controller. Task batches are pushed onto
// tasks; everything the agent sends back lands on a buffered channel the test
// reads.
type fakeTransport struct {
	tasks   chan []Task
	results chan TaskResult
	reports chan []RunnerReport
	beats   chan HeartbeatRequest

	mu        sync.Mutex
	hostID    string
	token     string
	joinReq   *JoinRequest
	joinResp  *JoinResponse
	joinErr   error
	beatResp  *HeartbeatResponse
	beatErr   error
	pollErr   error
	streams   map[string]*fakeStream
	openErr   error
	writeHold time.Duration
	polls     int
	// onResult stands in for a controller that acts on a result the instant it
	// arrives, which is the only way to observe what the agent's state looks
	// like at the moment it publishes one.
	onResult func(TaskResult)
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		tasks:    make(chan []Task, 8),
		results:  make(chan TaskResult, 64),
		reports:  make(chan []RunnerReport, 64),
		beats:    make(chan HeartbeatRequest, 64),
		joinResp: &JoinResponse{HostID: "host-1", AgentToken: "agent-token", ControllerVersion: "test"},
		beatResp: &HeartbeatResponse{OK: true},
		streams:  make(map[string]*fakeStream),
	}
}

func (f *fakeTransport) Join(_ context.Context, req JoinRequest) (*JoinResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joinReq = &req
	if f.joinErr != nil {
		return nil, f.joinErr
	}
	return f.joinResp, nil
}

func (f *fakeTransport) Heartbeat(_ context.Context, req HeartbeatRequest) (*HeartbeatResponse, error) {
	select {
	case f.beats <- req:
	default:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beatErr != nil {
		return nil, f.beatErr
	}
	return f.beatResp, nil
}

func (f *fakeTransport) PollTasks(ctx context.Context, wait time.Duration) (*TaskBatch, error) {
	f.mu.Lock()
	f.polls++
	err := f.pollErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case batch := <-f.tasks:
		return &TaskBatch{Tasks: batch}, nil
	case <-time.After(wait):
		return &TaskBatch{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeTransport) ReportResult(_ context.Context, res TaskResult) error {
	f.mu.Lock()
	hook := f.onResult
	f.mu.Unlock()
	if hook != nil {
		hook(res)
	}
	select {
	case f.results <- res:
	default:
	}
	return nil
}

func (f *fakeTransport) ReportRunners(_ context.Context, reports []RunnerReport) error {
	select {
	case f.reports <- reports:
	default:
	}
	return nil
}

func (f *fakeTransport) OpenLogStream(_ context.Context, streamID string) (io.WriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return nil, f.openErr
	}
	s := &fakeStream{hold: f.writeHold}
	f.streams[streamID] = s
	return s, nil
}

func (f *fakeTransport) SetCredentials(hostID, agentToken string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostID, f.token = hostID, agentToken
}

func (f *fakeTransport) Describe() string { return "fake://controller" }

func (f *fakeTransport) stream(id string) *fakeStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[id]
}

// fakeStream is the controller's side of one log relay.
type fakeStream struct {
	hold time.Duration

	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
	writes int
}

func (s *fakeStream) Write(p []byte) (int, error) {
	if s.hold > 0 {
		time.Sleep(s.hold)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("stream closed")
	}
	s.writes++
	return s.buf.Write(p)
}

func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeStream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *fakeStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// testClock is a clock the tests move by hand, so grace periods can be crossed
// without sleeping through them.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
