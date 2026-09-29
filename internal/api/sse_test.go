package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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

// TestARunnerThatPrintsMoreThanTheBodyLimitIsNotCutOff is the log relay's
// exemption from the request body limit, asserted on a stream that is really
// open -- which is the only place it shows. A relay for a stream nobody is
// watching is refused before its body is read, so it answers the same whether
// the limit applies to it or not.
//
// The exemption matters because the body here is not a request, it is a
// runner's whole output for as long as the job runs. Applying the ordinary
// limit would cut off every build that prints more than a megabyte, and it
// would do it by tearing down the operator's log view mid-build.
func TestARunnerThatPrintsMoreThanTheBodyLimitIsNotCutOff(t *testing.T) {
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

	// A verbose build: comfortably over the limit an ordinary request gets,
	// with the marker at the very end so that finding it proves the whole body
	// was read rather than the first megabyte of it.
	const marker = "the last line of a very talkative build"
	verbose := strings.Repeat("a line of build output that goes on a bit\n", (maxBodyBytes/42)+1024) + marker + "\n"
	if len(verbose) <= maxBodyBytes {
		t.Fatalf("the fixture is %d bytes, which is inside the %d byte limit it is meant to exceed", len(verbose), maxBodyBytes)
	}

	done := make(chan *response, 1)
	go func() {
		done <- h.do(request{
			method: http.MethodPost, path: "/api/v1/agent/logs/" + streamID,
			token: agentToken, headers: map[string]string{"Content-Type": "application/octet-stream"},
			rawBody: verbose,
		})
	}()

	await(t, frames, "the end of a body over the limit", func(f sseFrame) bool {
		if f.event != logChunkKind {
			return false
		}
		var line string
		if err := json.Unmarshal([]byte(f.data), &line); err != nil {
			return false
		}
		return strings.Contains(line, marker)
	})

	if post := <-done; post.status != http.StatusNoContent && post.status != http.StatusOK {
		t.Errorf("the agent's oversize log POST answered %d: %s", post.status, truncate(post.body))
	}
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

// The heartbeat re-checks the credential a stream was opened with, and a
// failed read is not a revoked credential. Ending every operator's stream with
// "sign in again" because the database blinked once sends them to a login page
// for a session that is still good; the stream stays up and the next beat
// tries again.
func TestEventStreamSurvivesTheDatabaseFailingAtAHeartbeat(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames, _ := h.openStream(t, ctx, "/api/v1/events", h.session(u), nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })
	if err := h.st.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	// Several beats at the harness's 50ms: the first re-check has failed by
	// the time the later heartbeats arrive.
	beats := 0
	deadline := time.After(5 * time.Second)
	for beats < 4 {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("the stream closed when the database failed; it should ride it out")
			}
			if f.event == "end" {
				t.Fatalf("the stream was ended with %q for a failed read, not a revoked credential", f.data)
			}
			if f.comment == "heartbeat" {
				beats++
			}
		case <-deadline:
			t.Fatalf("only %d heartbeats arrived", beats)
		}
	}
}

// The relay forwards whatever each read returns, so a chunk boundary can fall
// inside a multi-byte character. Rendering each half as its own JSON string
// turned one check mark into two replacement glyphs in the live view while the
// downloaded log, which is raw bytes, stayed right. The live tail holds the
// incomplete tail of a chunk back until the rest of the character arrives.
func TestLogStreamKeepsACharacterSplitAcrossTwoReadsWhole(t *testing.T) {
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
	frames, _ := h.openStream(t, ctx, "/api/v1/runners/"+run.ID+"/logs", h.session(u), nil)
	await(t, frames, "the attach comment", func(f sseFrame) bool { return f.comment != "" })

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

	// The agent's body is a pipe so the two halves of U+2713 (E2 9C 93) are
	// separate writes, and so separate chunks on the wire.
	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/v1/agent/logs/"+streamID, pr)
	if err != nil {
		t.Fatalf("building the relay request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	var got strings.Builder
	next := func(what string) {
		t.Helper()
		f := await(t, frames, what, func(f sseFrame) bool { return f.event == logChunkKind })
		var s string
		if err := json.Unmarshal([]byte(f.data), &s); err != nil {
			t.Fatalf("a log frame is not a JSON string: %v (%q)", err, f.data)
		}
		got.WriteString(s)
	}

	if _, err := pw.Write([]byte("ok \xe2\x9c")); err != nil {
		t.Fatalf("writing the first half: %v", err)
	}
	next("the first log frame")
	if _, err := pw.Write([]byte("\x93 done\n")); err != nil {
		t.Fatalf("writing the second half: %v", err)
	}
	next("the second log frame")
	pw.Close()
	<-done

	if want := "ok ✓ done\n"; got.String() != want {
		t.Errorf("the live tail showed %q, want %q", got.String(), want)
	}
}

// Only the start of a character that cannot be finished yet is held back.
// Anything else -- whole characters, bytes that are invalid outright -- goes
// out at once, so the tail never waits on a character that will not arrive.
func TestSplitIncompleteRuneHoldsBackOnlyAnUnfinishedCharacter(t *testing.T) {
	for _, tc := range []struct {
		name        string
		in          string
		whole, rest string
	}{
		{"plain text", "hello\n", "hello\n", ""},
		{"nothing", "", "", ""},
		{"a whole three-byte character", "a✓", "a✓", ""},
		{"a check mark cut after its first byte", "a\xe2", "a", "\xe2"},
		{"a check mark cut after its second byte", "a\xe2\x9c", "a", "\xe2\x9c"},
		{"an emoji cut after three bytes", "a\xf0\x9f\x9a", "a", "\xf0\x9f\x9a"},
		{"a two-byte character cut after its first byte", "a\xc3", "a", "\xc3"},
		{"a lone continuation byte", "a\x9c", "a\x9c", ""},
		{"a start byte followed by a byte that cannot continue it", "a\xe2A", "a\xe2A", ""},
		{"a byte that is never valid", "a\xff", "a\xff", ""},
		{"a whole character before an unfinished one", "✓\xe2\x9c", "✓", "\xe2\x9c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whole, rest := splitIncompleteRune([]byte(tc.in))
			if string(whole) != tc.whole || string(rest) != tc.rest {
				t.Errorf("splitIncompleteRune(%q) = %q, %q; want %q, %q", tc.in, whole, rest, tc.whole, tc.rest)
			}
		})
	}
}

// TestLogStreamForAnUnknownRunnerIs404 keeps the log pane's error honest.
func TestLogStreamForAnUnknownRunnerIs404(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners/run_nope/logs", cookie: h.session(u)})
	resp.mustStatus(t, http.StatusNotFound, "logs for an unknown runner")
}

// A reconnecting client whose gap the server cannot replay used to be handed
// nothing and told nothing: after a controller restart its last id was a
// number from another sequence, so nothing matched, and the tab quietly showed
// a fleet that no longer existed. The first frame now says so.
func TestEventStreamSaysWhenItCannotReplayTheGap(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(u)
	bus := h.ctrl.Events()
	bus.Publish(events.KindPoolCreated, "pool:a", map[string]any{"id": "a"})

	t.Run("an id from another run of the controller", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames, _ := h.openStream(t, ctx, "/api/v1/events", cookie, map[string]string{
			"Last-Event-ID": "previousrun.7",
		})
		got := await(t, frames, "the first event frame", func(f sseFrame) bool { return f.event != "" })
		if got.event != string(events.KindResync) {
			t.Fatalf("first frame after a restart = %q, want resync", got.event)
		}
		if got.id != "" {
			t.Fatalf("the resync frame carried an id (%q); it must leave the client's last id alone", got.id)
		}
	})

	t.Run("an id this run still holds is replayed without a resync", func(t *testing.T) {
		first := bus.LastID()
		bus.Publish(events.KindPoolUpdated, "pool:b", map[string]any{"id": "b"})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames, _ := h.openStream(t, ctx, "/api/v1/events", cookie, map[string]string{
			"Last-Event-ID": bus.WireID(first),
		})
		got := await(t, frames, "the replayed event", func(f sseFrame) bool { return f.event != "" })
		if got.event != string(events.KindPoolUpdated) {
			t.Fatalf("first frame on a covered reconnect = %q, want the replayed pool.updated", got.event)
		}
		if got.id != bus.WireID(bus.LastID()) {
			t.Fatalf("event id = %q, want the epoch-qualified %q", got.id, bus.WireID(bus.LastID()))
		}
	})
}

// TestAStreamEndsWhenItsCredentialIsRevoked is the thing a live stream had that
// no other route did: an authorisation decision taken once and then held for as
// long as the tab was open.
//
// Signing out, revoking the token, disabling the account or letting the session
// expire all left the stream running -- every movement of the fleet still
// arriving at a browser whose credential the operator had just taken away,
// until something else happened to break the connection. Every other route
// re-checks on every request; a stream's heartbeat is the closest thing it has
// to one.
func TestAStreamEndsWhenItsCredentialIsRevoked(t *testing.T) {
	for _, tc := range []struct {
		name string
		// revoke takes the credential away by the route an operator would.
		revoke func(t *testing.T, h *harness, u *store.User, cookie, adminCookie string)
	}{
		{"signing out", func(t *testing.T, h *harness, _ *store.User, cookie, _ string) {
			h.do(request{method: http.MethodPost, path: "/api/v1/auth/logout", cookie: cookie}).
				mustStatus(t, http.StatusNoContent, "logout")
		}},
		{"the account being disabled", func(t *testing.T, h *harness, u *store.User, _, adminCookie string) {
			h.do(request{method: http.MethodPatch, path: "/api/v1/users/" + u.ID,
				cookie: adminCookie, body: map[string]any{"disabled": true}}).
				mustStatus(t, http.StatusOK, "disable")
		}},
		{"the account being deleted", func(t *testing.T, h *harness, u *store.User, _, adminCookie string) {
			h.do(request{method: http.MethodDelete, path: "/api/v1/users/" + u.ID, cookie: adminCookie}).
				mustStatus(t, http.StatusNoContent, "delete")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// An admin who stays signed in, so the revoking call has a caller
			// and the last-admin invariant is never the thing that refuses it.
			admin, _ := h.user("root", store.RoleAdmin)
			adminCookie := h.session(admin)
			watcher, _ := h.user("watcher", store.RoleOperator)
			cookie := h.session(watcher)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frames, resp := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("event stream status %d", resp.StatusCode)
			}
			await(t, frames, "the attach comment", func(f sseFrame) bool { return f.comment != "" })

			tc.revoke(t, h, watcher, cookie, adminCookie)

			end := await(t, frames, "an end frame", func(f sseFrame) bool { return f.event == "end" })
			if !strings.Contains(end.data, "sign in again") {
				t.Errorf("the end frame does not say what to do about it: %q", end.data)
			}
			// And the response really ends, rather than the frame being sent
			// into a connection that stays open.
			for range frames {
			}
		})
	}
}

// The log stream is authorised on a different action, and holds a relay open on
// a host for as long as it runs -- so a revoked credential there costs a machine
// something, not just a browser.
func TestALogStreamEndsWhenItsCredentialIsRevoked(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	hostID, _ := h.agentToken("vm-1")
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	run := h.runner(pool, host, store.RunnerBusy)

	u, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(u)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, resp := h.openStream(t, ctx, "/api/v1/runners/"+run.ID+"/logs", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log stream status %d", resp.StatusCode)
	}
	await(t, frames, "the attach comment", func(f sseFrame) bool { return f.comment != "" })

	h.do(request{method: http.MethodPost, path: "/api/v1/auth/logout", cookie: cookie}).
		mustStatus(t, http.StatusNoContent, "logout")

	end := await(t, frames, "an end frame", func(f sseFrame) bool { return f.event == "end" })
	if !strings.Contains(end.data, "sign in again") {
		t.Errorf("the end frame does not say what to do about it: %q", end.data)
	}
}

// The other half, which a check that ended every stream would also pass: a
// credential that is still good keeps its stream.
func TestALiveStreamSurvivesItsOwnHeartbeat(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("watcher", store.RoleOperator)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, resp := h.openStream(t, ctx, "/api/v1/events", h.session(u), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event stream status %d", resp.StatusCode)
	}

	// Several heartbeats' worth. Any of them re-checks the credential, and a
	// stream that ended here would be one nobody could keep open at all.
	beats := 0
	deadline := time.After(5 * time.Second)
	for beats < 3 {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("the stream ended though the credential is still good")
			}
			if f.event == "end" {
				t.Fatalf("the stream ended on a live credential: %q", f.data)
			}
			if f.event == string(events.KindHeartbeat) {
				beats++
			}
		case <-deadline:
			t.Fatalf("only %d heartbeats arrived", beats)
		}
	}
}

func TestPoolEventsFollowTheWatchersCurrentRole(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	watcher, _ := h.user("watcher", store.RoleOperator)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	frames, _ := h.openStream(t, ctx, "/api/v1/events", h.session(watcher), nil)
	await(t, frames, "the opening comment", func(f sseFrame) bool { return f.comment != "" })

	for _, role := range []store.Role{store.RoleOperator, store.RoleViewer, store.RoleOperator} {
		h.do(request{method: http.MethodPatch, path: "/api/v1/users/" + watcher.ID,
			cookie: h.session(admin), body: map[string]any{"role": role}}).
			mustStatus(t, http.StatusOK, "change the watcher's role")
		h.ctrl.Events().Publish(events.KindPoolUpdated, "pool:pool_test", map[string]any{
			"id": "pool_test", "env": map[string]string{"REGISTRY_PASSWORD": "private-value"},
		})
		frame := await(t, frames, "the pool event", func(f sseFrame) bool {
			return f.event == string(events.KindPoolUpdated)
		})
		var payload struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
			t.Fatal(err)
		}
		want := "private-value"
		if role == store.RoleViewer {
			want = ""
		}
		if got, ok := payload.Env["REGISTRY_PASSWORD"]; !ok || got != want {
			t.Fatalf("role %s received env %v; want the key with value %q", role, payload.Env, want)
		}
	}
}
