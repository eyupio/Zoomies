package controller

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// The controller never dials an agent, so a log tail is a task out and a
// chunked POST back. This is that round trip.
func TestLogRelayDeliversToASubscriber(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	ch, cancel, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true, Tail: 100})
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	defer cancel()

	task := h.taskOfKind(host.ID, agent.TaskStreamLogs)
	if task.RunnerID != r.ID || task.StreamID == "" {
		t.Fatalf("stream task = %+v, want one for runner %s with a stream ID", task, r.ID)
	}
	if task.LogOptions == nil || !task.LogOptions.Follow || task.LogOptions.Tail != 100 {
		t.Fatalf("log options = %+v, want the ones the viewer asked for", task.LogOptions)
	}

	done := make(chan []byte, 1)
	go func() { done <- drain(t, ch, 2*time.Second) }()

	if err := h.c.AcceptLogStream(host.ID, task.StreamID, strings.NewReader("run 1/3\nrun 2/3\nrun 3/3\n")); err != nil {
		t.Fatalf("AcceptLogStream: %v", err)
	}

	got := <-done
	if !bytes.Contains(got, []byte("run 2/3")) {
		t.Fatalf("subscriber received %q, want the runner's output", got)
	}
}

// Two viewers of the same runner share one stream, so opening a second tab
// does not make the agent read the container's output twice.
func TestSecondViewerSharesTheStream(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	_, cancel1, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("first OpenLogStream: %v", err)
	}
	defer cancel1()
	ch2, cancel2, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("second OpenLogStream: %v", err)
	}
	defer cancel2()

	var streams int
	for _, task := range h.tasksFor(host.ID) {
		if task.Kind == agent.TaskStreamLogs {
			streams++
		}
	}
	if streams != 1 {
		t.Fatalf("queued %d stream tasks for two viewers, want 1", streams)
	}

	streamID := h.c.relay.streamIDFor(r.ID)
	go func() { _ = h.c.AcceptLogStream(host.ID, streamID, strings.NewReader("shared output\n")) }()
	if got := drain(t, ch2, 2*time.Second); !bytes.Contains(got, []byte("shared output")) {
		t.Fatalf("the second viewer received %q", got)
	}
}

// When the last viewer goes away the agent is told to stop reading, otherwise
// a closed browser tab would keep a container's output flowing forever.
func TestLastViewerLeavingCancelsTheStream(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	ch, cancel, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	streamID := h.c.relay.streamIDFor(r.ID)
	cancel()

	if !h.hasTaskOfKind(host.ID, agent.TaskCancelLogs) {
		t.Fatal("no cancel task was queued when the last viewer left")
	}
	if _, open := <-ch; open {
		t.Fatal("the subscriber's channel is still open after it unsubscribed")
	}
	if err := h.c.AcceptLogStream(host.ID, streamID, strings.NewReader("late")); !errors.Is(err, ErrStreamUnknown) {
		t.Fatalf("AcceptLogStream after cancel = %v, want ErrStreamUnknown", err)
	}
}

// A browser that has stopped reading must lose lines rather than hold up the
// agent writing into the relay.
func TestLogRelayDropsRatherThanBlocks(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	// Subscribe and deliberately never read.
	_, cancel, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	defer cancel()
	streamID := h.taskOfKind(host.ID, agent.TaskStreamLogs).StreamID

	// One byte per Read, so this is several times the subscriber's queue depth
	// in separate chunks.
	payload := bytes.Repeat([]byte("x"), logSubscriberQueue*3)
	done := make(chan error, 1)
	go func() { done <- h.c.AcceptLogStream(host.ID, streamID, &byteReader{data: payload}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AcceptLogStream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AcceptLogStream blocked on a subscriber that stopped reading")
	}
}

// Logs for a runner whose container is gone are not an empty stream: the API
// gets an error it can turn into an explanation.
func TestLogStreamForARemovedRunnerIsRefused(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "gone"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}

	if _, _, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{}); err == nil {
		t.Fatal("OpenLogStream succeeded for a removed runner")
	} else if !strings.Contains(err.Error(), "ephemeral") {
		t.Fatalf("error = %v, want one explaining that the container is gone", err)
	}
}

// Opening a stream for a runner on a host that has not heartbeated recently
// would queue a task nobody will ever poll for: no agent is coming back to
// read it. Refusing here, rather than opening a stream that waits forever, is
// what stops a repeatedly reopened tab from piling up queued tasks for a host
// that is never coming back.
func TestLogStreamForAnUnhealthyHostIsRefused(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)
	host.LastHeartbeat = time.Now().Add(-store.HeartbeatTimeout - time.Minute)
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}

	if _, _, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{}); err == nil {
		t.Fatal("OpenLogStream succeeded for a host with no recent heartbeat")
	} else if !strings.Contains(err.Error(), "not been heard from") {
		t.Fatalf("error = %v, want one explaining the host is unreachable", err)
	}
	if h.hasTaskOfKind(host.ID, agent.TaskStreamLogs) {
		t.Fatal("a stream task was queued for a host nothing will ever poll")
	}
}

// Whatever arrives on a log relay is shown to operators as that runner's
// output. Every other agent endpoint checks the reporting host owns what it is
// reporting on; this one has to check the same thing against the stream, or one
// host in the fleet can put words in another's mouth.
func TestLogRelayRefusesAnotherHostsStream(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)
	intruder := h.host("vm-intruder")

	ch, cancel, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	defer cancel()
	streamID := h.taskOfKind(host.ID, agent.TaskStreamLogs).StreamID

	err = h.c.AcceptLogStream(intruder.ID, streamID, strings.NewReader("you have been pwned\n"))
	if !errors.Is(err, ErrStreamUnknown) {
		t.Fatalf("another host writing into the stream = %v; want ErrStreamUnknown", err)
	}
	// And the answer is the same one a stream that never existed gets, so
	// probing for streams you do not own tells you nothing. The two messages
	// name their own stream id, so they are not compared for equality; what
	// must match is the sentinel, and what must not appear in either is any
	// hint that this particular id belongs to somebody.
	never := h.c.AcceptLogStream(intruder.ID, "log_nosuchstream", strings.NewReader("x"))
	if !errors.Is(never, ErrStreamUnknown) {
		t.Fatalf("a stream that never existed = %v; want the same ErrStreamUnknown", never)
	}
	for _, telltale := range []string{host.ID, host.Name, r.ID, "owner", "another", "belongs", "forbidden"} {
		if strings.Contains(err.Error(), telltale) {
			t.Fatalf("the refusal names %q, which tells an intruder the stream is real: %v", telltale, err)
		}
	}

	// Nothing the intruder sent reached the viewer.
	select {
	case chunk, ok := <-ch:
		if ok {
			t.Fatalf("the viewer received %q from a host that does not own the runner", chunk)
		}
		t.Fatal("the stream was closed by a refused write")
	default:
	}

	// The owning host is unaffected.
	go func() { _ = h.c.AcceptLogStream(host.ID, streamID, strings.NewReader("real output\n")) }()
	if got := drain(t, ch, 2*time.Second); !bytes.Contains(got, []byte("real output")) {
		t.Fatalf("the owning host's output did not arrive: %q", got)
	}
}

// cutReader hands out its data and then fails the way a dropped connection
// does, rather than ending with a clean EOF.
type cutReader struct {
	data []byte
	done bool
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("read tcp: connection reset by peer")
	}
	r.done = true
	return copy(p, r.data), nil
}

// A job that finishes under a watcher ends the agent's chunked POST without a
// clean EOF -- the container is gone, and the connection with it. That is how
// every watched job ends, and it used to be logged as an error and answered
// with a 500 each time.
func TestALogStreamCutMidJobEndsQuietly(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	lines, cancel, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	defer cancel()
	streamID := h.taskOfKind(host.ID, agent.TaskStreamLogs).StreamID

	if err := h.c.AcceptLogStream(host.ID, streamID, &cutReader{data: []byte("last line\n")}); err != nil {
		t.Fatalf("AcceptLogStream = %v, want the stream to end quietly", err)
	}
	select {
	case chunk := <-lines:
		if string(chunk) != "last line\n" {
			t.Fatalf("delivered %q before the cut, want the last line", chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the output before the cut was not delivered")
	}
}

// A viewer that opens a runner whose container is not there is told why,
// straight away. Before this the failed task was only logged, so the browser
// waited on a relay no agent would ever feed, and a second tab attached to the
// same dead stream instead of asking for a fresh one.
func TestAFailedLogTaskEndsTheRelayWithTheAgentsReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind agent.TaskKind // what the agent puts in its result; "" is an older agent
	}{
		{"agent names the kind", agent.TaskStreamLogs},
		{"older agent, controller's record decides", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			r := h.runnerRow(pool, host, store.RunnerFailed)

			ch, cancel, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
			if err != nil {
				t.Fatalf("OpenLogStream: %v", err)
			}
			defer cancel()

			batch, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
			if err != nil {
				t.Fatalf("PollTasks: %v", err)
			}
			const reason = "no workload for runner on this host, so its logs are gone"
			if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
				TaskID:   batch.Tasks[0].ID,
				Kind:     tc.kind,
				RunnerID: r.ID,
				OK:       false,
				Error:    reason,
			}); err != nil {
				t.Fatalf("ReportResult: %v", err)
			}

			got := drain(t, ch, 2*time.Second)
			if !bytes.Contains(got, []byte(reason)) {
				t.Fatalf("viewer received %q, want the agent's reason %q", got, reason)
			}
			if id := h.c.relay.streamIDFor(r.ID); id != "" {
				t.Fatalf("the dead stream %s is still registered for the runner", id)
			}

			// A second tab must get a relay of its own, and the agent a task
			// to open it.
			_, cancel2, err := h.c.OpenLogStream(h.ctx, r.ID, backend.LogOptions{Follow: true})
			if err != nil {
				t.Fatalf("second OpenLogStream: %v", err)
			}
			defer cancel2()
			if task := h.taskOfKind(host.ID, agent.TaskStreamLogs); task.StreamID == "" {
				t.Fatal("no fresh stream_logs task was queued for the second viewer")
			}
		})
	}
}
