package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// sseFrame is one parsed Server-Sent Event.
type sseFrame struct {
	id      string
	event   string
	data    string
	comment string
}

// openStream starts an SSE request and returns the frames as they arrive.
//
// The request carries its own context so the test can cancel it, which is how
// the "the handler returns when the client goes away" case is exercised.
func (h *harness) openStream(t *testing.T, ctx context.Context, path, cookie string, headers map[string]string) (<-chan sseFrame, *http.Response) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("building the stream request: %v", err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}

	frames := make(chan sseFrame, 64)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var f sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f != (sseFrame{}) {
					frames <- f
					f = sseFrame{}
				}
			case strings.HasPrefix(line, ":"):
				frames <- sseFrame{comment: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if f.data != "" {
					f.data += "\n"
				}
				f.data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return frames, resp
}

// await waits for a frame matching pred, or fails.
func await(t *testing.T, frames <-chan sseFrame, what string, pred func(sseFrame) bool) sseFrame {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("the stream closed before %s arrived", what)
			}
			if pred(f) {
				return f
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestEventStreamDeliversPublishedEvents is the whole of the UI's liveness: an
// event published on the bus has to reach a browser holding this connection.
func TestEventStreamDeliversPublishedEvents(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames, resp := h.openStream(t, ctx, "/api/v1/events", h.session(u), nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	for _, want := range [][2]string{
		{"Cache-Control", "no-cache"},
		{"X-Accel-Buffering", "no"},
	} {
		if got := resp.Header.Get(want[0]); !strings.Contains(got, want[1]) {
			t.Errorf("%s = %q, want it to contain %q", want[0], got, want[1])
		}
	}

	// The connection is live before anything is published: the comment frame
	// arrives immediately so the browser fires onopen.
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	h.ctrl.Events().Publish(events.KindPoolCreated, "pool:pool_test", map[string]any{"id": "pool_test", "name": "linux-x64"})

	got := await(t, frames, "the pool.created event", func(f sseFrame) bool {
		return f.event == string(events.KindPoolCreated)
	})
	if got.id == "" {
		t.Error("the event carried no id, so a reconnecting client cannot resume")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got.data), &payload); err != nil {
		t.Fatalf("the event payload is not JSON: %v (%q)", err, got.data)
	}
	if payload["name"] != "linux-x64" {
		t.Errorf("payload = %v", payload)
	}
}

// TestEventStreamReplaysFromLastEventID covers reconnection, which is what
// makes a dropped connection invisible rather than a hole in the history.
func TestEventStreamReplaysFromLastEventID(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(u)

	// Publish before anyone is listening; the bus buffers.
	h.ctrl.Events().Publish(events.KindPoolCreated, "pool:a", map[string]any{"id": "a"})
	first := h.ctrl.Events().LastID()
	h.ctrl.Events().Publish(events.KindPoolUpdated, "pool:b", map[string]any{"id": "b"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events", cookie, map[string]string{
		"Last-Event-ID": strconv.FormatUint(first, 10),
	})
	got := await(t, frames, "the replayed event", func(f sseFrame) bool { return f.event != "" })
	if got.event != string(events.KindPoolUpdated) {
		t.Fatalf("replayed %q, want the event after the last id", got.event)
	}

	// The query-string form is honoured too, because a fresh EventSource
	// cannot send the header.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	frames2, _ := h.openStream(t, ctx2, "/api/v1/events?last_event_id="+strconv.FormatUint(first, 10), cookie, nil)
	got2 := await(t, frames2, "the replayed event", func(f sseFrame) bool { return f.event != "" })
	if got2.event != string(events.KindPoolUpdated) {
		t.Fatalf("replayed %q with the query parameter", got2.event)
	}
}

// TestEventStreamKindFilter narrows the stream, which is what a page that only
// cares about runners uses.
func TestEventStreamKindFilter(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames, _ := h.openStream(t, ctx, "/api/v1/events?kinds=runner.updated", h.session(u), nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	h.ctrl.Events().Publish(events.KindPoolCreated, "pool:x", map[string]any{"id": "x"})
	h.ctrl.Events().Publish(events.KindRunnerUpdated, "runner:y", map[string]any{"id": "y"})

	got := await(t, frames, "the runner event", func(f sseFrame) bool { return f.event != "" })
	if got.event != string(events.KindRunnerUpdated) {
		t.Fatalf("received %q despite the kinds filter", got.event)
	}
}

// TestEventStreamEndsWhenTheClientGoesAway is the leak test: cancelling the
// request has to end the handler and release its subscription.
func TestEventStreamEndsWhenTheClientGoesAway(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)

	before := h.ctrl.Events().Subscribers()
	ctx, cancel := context.WithCancel(context.Background())
	frames, _ := h.openStream(t, ctx, "/api/v1/events", h.session(u), nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	if h.ctrl.Events().Subscribers() != before+1 {
		t.Fatalf("subscribers = %d, want %d while a stream is open", h.ctrl.Events().Subscribers(), before+1)
	}

	cancel()
	// The frame channel closes when the response body ends.
	select {
	case _, ok := <-frames:
		if ok {
			// Drain whatever was in flight; the close follows.
			for range frames {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end when the request was cancelled")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.ctrl.Events().Subscribers() == before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscribers = %d after the client went away, want %d", h.ctrl.Events().Subscribers(), before)
}

// TestRunnerLogStream relays an agent's output to a watching browser. It is the
// inverted path: the viewer's request queues a task, the agent answers it with
// a chunked POST, and the bytes come back out here.
func TestRunnerLogStream(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")

	hostID, agentToken := h.agentToken("vm-1")
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	run := h.runner(pool, host, store.RunnerBusy)

	u, _ := h.user("viewer", store.RoleViewer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames, resp := h.openStream(t, ctx, "/api/v1/runners/"+run.ID+"/logs", h.session(u), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log stream status %d", resp.StatusCode)
	}
	await(t, frames, "the attach comment", func(f sseFrame) bool { return f.comment != "" })

	// The controller has queued a stream_logs task; the agent picks it up.
	tasks := h.do(request{method: http.MethodGet, path: "/api/v1/agent/tasks?wait=2", token: agentToken})
	tasks.mustStatus(t, http.StatusOK, "agent task poll")
	var batch struct {
		Tasks []struct {
			Kind     string `json:"kind"`
			StreamID string `json:"stream_id"`
		} `json:"tasks"`
	}
	tasks.into(t, &batch)
	streamID := ""
	for _, task := range batch.Tasks {
		if task.Kind == "stream_logs" {
			streamID = task.StreamID
		}
	}
	if streamID == "" {
		t.Fatalf("no stream_logs task was queued: %+v", batch.Tasks)
	}

	// The agent's outbound relay.
	done := make(chan *response, 1)
	go func() {
		done <- h.do(request{
			method: http.MethodPost, path: "/api/v1/agent/logs/" + streamID,
			token: agentToken, headers: map[string]string{"Content-Type": "application/octet-stream"},
			rawBody: "hello from the runner\n",
		})
	}()

	got := await(t, frames, "a log frame", func(f sseFrame) bool { return f.event == logChunkKind })
	var line string
	if err := json.Unmarshal([]byte(got.data), &line); err != nil {
		t.Fatalf("a log frame is not a JSON string: %v (%q)", err, got.data)
	}
	if !strings.Contains(line, "hello from the runner") {
		t.Errorf("log frame = %q", line)
	}

	// When the agent's body ends, the stream ends too rather than leaving the
	// browser holding an open connection.
	await(t, frames, "the end frame", func(f sseFrame) bool { return f.event == "end" })
	if post := <-done; post.status != http.StatusNoContent && post.status != http.StatusOK {
		t.Errorf("the agent's log POST answered %d", post.status)
	}
}

// TestLogStreamForAnUnknownRunnerIs404 keeps the log pane's error honest.
func TestLogStreamForAnUnknownRunnerIs404(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners/run_nope/logs", cookie: h.session(u)})
	resp.mustStatus(t, http.StatusNotFound, "logs for an unknown runner")
}
