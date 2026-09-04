package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
)

// burstReader produces a fixed number of full chunks as fast as it is read, and
// says when it has been drained.
type burstReader struct {
	mu        sync.Mutex
	remaining int
	drained   chan struct{}
	once      sync.Once
}

func newBurstReader(chunks int) *burstReader {
	return &burstReader{remaining: chunks, drained: make(chan struct{})}
}

func (r *burstReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remaining == 0 {
		r.once.Do(func() { close(r.drained) })
		return 0, io.EOF
	}
	r.remaining--
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func (r *burstReader) Close() error { return nil }

func waitFn(t *testing.T, what string, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func TestLogRelayCopiesOutputEndToEnd(t *testing.T) {
	tr := newFakeTransport()
	be := newFakeBackend("docker")
	be.mu.Lock()
	be.logs = func() io.ReadCloser { return io.NopCloser(strings.NewReader("cloning repository\nrunning job\n")) }
	be.mu.Unlock()

	relay := newLogRelay(tr, testLogger())
	if err := relay.start(context.Background(), "stream-1", "wl-1", be, backend.LogOptions{Follow: true}); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFn(t, "the stream to finish", 5*time.Second, func() bool {
		s := tr.stream("stream-1")
		return s != nil && s.isClosed()
	})
	if got := tr.stream("stream-1").String(); got != "cloning repository\nrunning job\n" {
		t.Fatalf("relayed %q", got)
	}
}

func TestLogRelayCancelStopsTheStream(t *testing.T) {
	tr := newFakeTransport()
	be := newFakeBackend("docker")
	pr, pw := io.Pipe()
	be.mu.Lock()
	be.logs = func() io.ReadCloser { return pr }
	be.mu.Unlock()

	relay := newLogRelay(tr, testLogger())
	if err := relay.start(context.Background(), "stream-1", "wl-1", be, backend.LogOptions{Follow: true}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := pw.Write([]byte("still going\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFn(t, "the first chunk to arrive", 5*time.Second, func() bool {
		s := tr.stream("stream-1")
		return s != nil && strings.Contains(s.String(), "still going")
	})

	relay.cancel("stream-1")
	if !tr.stream("stream-1").isClosed() {
		t.Fatal("cancel did not close the controller's side of the stream")
	}
	relay.mu.Lock()
	open := len(relay.streams)
	relay.mu.Unlock()
	if open != 0 {
		t.Fatalf("%d streams still registered after cancel", open)
	}
	pw.Close()
}

func TestLogRelayDropsRatherThanBlockingOnASlowController(t *testing.T) {
	tr := newFakeTransport()
	// Every write to the controller takes 10ms: writing all 300 chunks would
	// take three seconds, far longer than the runner should ever wait.
	tr.mu.Lock()
	tr.writeHold = 10 * time.Millisecond
	tr.mu.Unlock()

	be := newFakeBackend("docker")
	src := newBurstReader(300)
	be.mu.Lock()
	be.logs = func() io.ReadCloser { return src }
	be.mu.Unlock()

	relay := newLogRelay(tr, testLogger())
	if err := relay.start(context.Background(), "stream-1", "wl-1", be, backend.LogOptions{Follow: true}); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The copy out of the runner must finish at the runner's pace, not the
	// controller's: a slow viewer must never throttle a job.
	select {
	case <-src.drained:
	case <-time.After(2 * time.Second):
		t.Fatal("the copy from the runner blocked on the slow controller")
	}

	waitFn(t, "the stream to finish", 10*time.Second, func() bool {
		s := tr.stream("stream-1")
		return s != nil && s.isClosed()
	})
	relayed := tr.stream("stream-1").String()
	if !strings.Contains(relayed, "log stream fell behind") {
		t.Fatal("output was dropped without telling the viewer that it was")
	}
	if len(relayed) >= 300*relayChunkSize {
		t.Fatal("nothing was dropped, so the relay must have blocked")
	}
}

func TestLogRelayIgnoresARedeliveredStart(t *testing.T) {
	tr := newFakeTransport()
	be := newFakeBackend("docker")
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	be.mu.Lock()
	be.logs = func() io.ReadCloser { return pr }
	be.mu.Unlock()

	relay := newLogRelay(tr, testLogger())
	for range 3 {
		if err := relay.start(context.Background(), "stream-1", "wl-1", be, backend.LogOptions{Follow: true}); err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	relay.mu.Lock()
	open := len(relay.streams)
	relay.mu.Unlock()
	if open != 1 {
		t.Fatalf("%d streams open, want 1: a redelivered stream_logs task must not open a second copy", open)
	}
	relay.stopAll()
}

func TestAgentStreamsAndCancelsLogsForARunner(t *testing.T) {
	h := newHarness(t, 1)
	h.be.mu.Lock()
	h.be.logs = func() io.ReadCloser { return io.NopCloser(strings.NewReader("job output\n")) }
	h.be.mu.Unlock()

	h.tr.tasks <- []Task{createTask("task-1", "runner-1")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("create failed: %+v", res)
	}

	h.tr.tasks <- []Task{{ID: "task-2", Kind: TaskStreamLogs, RunnerID: "runner-1", StreamID: "stream-1"}}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("stream_logs failed: %+v", res)
	}
	waitFn(t, "the log output to arrive", 5*time.Second, func() bool {
		s := h.tr.stream("stream-1")
		return s != nil && strings.Contains(s.String(), "job output")
	})

	h.tr.tasks <- []Task{{ID: "task-3", Kind: TaskCancelLogs, RunnerID: "runner-1", StreamID: "stream-1"}}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("cancel_logs failed: %+v", res)
	}
}

func TestStreamLogsForAnUnknownRunnerIsReported(t *testing.T) {
	h := newHarness(t, 1)
	h.tr.tasks <- []Task{{ID: "task-1", Kind: TaskStreamLogs, RunnerID: "runner-404", StreamID: "stream-1"}}

	res := h.nextResult()
	if res.OK || !strings.Contains(res.Error, "runner-404") {
		t.Fatalf("unexpected result: %+v", res)
	}
}
