package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// heartbeatInterval is how often a live stream sends something.
//
// Twenty seconds is comfortably inside the idle timeout of every proxy and load
// balancer anyone puts in front of this, which is what keeps a quiet fleet's
// event stream from being closed under the operator every minute. The UI's
// watchdog is set from the same number.
const heartbeatInterval = 20 * time.Second

// sseWriter is one Server-Sent Events response.
//
// It exists so that the two streams -- fleet events and a runner's log tail --
// cannot disagree about framing, flushing or heartbeats, which is the usual way
// one of two SSE endpoints ends up subtly buffered.
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// startSSE writes the headers and returns the stream, or nil when the client
// cannot be streamed to.
func startSSE(w http.ResponseWriter, r *http.Request) *sseWriter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which turns a live stream
	// into one that arrives in lumps minutes late. This is the header that
	// turns that off, and it costs nothing when there is no nginx.
	h.Set("X-Accel-Buffering", "no")

	s := &sseWriter{w: w, rc: http.NewResponseController(w)}
	// A stream has no length and no deadline. The server's WriteTimeout is
	// already 0 for exactly this reason; clearing it here as well means a
	// caller that serves this handler from a listener of their own cannot cut
	// the stream off by setting one.
	_ = s.rc.SetWriteDeadline(time.Time{})

	w.WriteHeader(http.StatusOK)
	// The first flush is what makes the browser's EventSource fire onopen,
	// which the UI's connection indicator is driven by.
	s.flush()
	return s
}

// event writes one named event with its id and JSON payload.
func (s *sseWriter) event(kind string, id uint64, data []byte) error {
	var b strings.Builder
	if id > 0 {
		b.WriteString("id: ")
		b.WriteString(strconv.FormatUint(id, 10))
		b.WriteByte('\n')
	}
	if kind != "" {
		b.WriteString("event: ")
		b.WriteString(kind)
		b.WriteByte('\n')
	}
	if len(data) == 0 {
		data = []byte("{}")
	}
	// A payload containing a newline has to be split across data: lines or the
	// frame ends early and the client sees truncated JSON.
	for line := range strings.SplitSeq(string(data), "\n") {
		b.WriteString("data: ")
		b.WriteString(strings.TrimSuffix(line, "\r"))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := io.WriteString(s.w, b.String()); err != nil {
		return err
	}
	s.flush()
	return nil
}

// comment writes a heartbeat. A comment frame keeps the connection alive
// without the client having to know about a keep-alive event kind.
func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.flush()
	return nil
}

// retry tells the browser how long to wait before reconnecting on its own.
func (s *sseWriter) retry(d time.Duration) error {
	if _, err := fmt.Fprintf(s.w, "retry: %d\n\n", d.Milliseconds()); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *sseWriter) flush() { _ = s.rc.Flush() }

// ---------------------------------------------------------------------------
// GET /api/v1/events
// ---------------------------------------------------------------------------

// handleEvents streams every change the operator would want to see.
//
// There is no polling anywhere in the UI, so this endpoint is the whole of the
// dashboard's liveness. It replays from Last-Event-ID on reconnect, which is
// what makes a dropped connection invisible rather than a hole in the history.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	opts := events.SubscribeOptions{
		Kinds:       parseKinds(r.URL.Query().Get("kinds")),
		TopicPrefix: strings.TrimSpace(r.URL.Query().Get("topic")),
		Replay:      lastEventID(r),
	}

	ctx := r.Context()
	// The subscription is unsubscribed by cancelling the context it was made
	// with, which the bus watches and acts on. That is one owner of the
	// teardown rather than two: closing it here as well would mean this
	// handler and the bus's own watcher racing to do the same thing, and the
	// point of the cancel is that it happens on every exit from this function,
	// including a write failure to a browser that has already gone.
	subCtx, unsubscribe := context.WithCancel(ctx)
	defer unsubscribe()
	sub := s.ctrl.Events().Subscribe(subCtx, opts)

	stream := startSSE(w, r)
	_ = stream.retry(2 * time.Second)
	// An immediate heartbeat lets the client start its stall watchdog without
	// waiting twenty seconds to learn the stream works.
	if err := stream.comment("connected"); err != nil {
		return
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// The browser navigated away, or the process is shutting down.
			return
		case ev, ok := <-sub.C:
			if !ok {
				// The bus dropped this subscriber for falling behind. Ending
				// the response makes the client reconnect with its last event
				// ID and catch up, which is better than silently going stale.
				return
			}
			if err := stream.event(string(ev.Kind), ev.ID, ev.Data); err != nil {
				return
			}
		case <-ticker.C:
			if err := stream.comment("heartbeat"); err != nil {
				return
			}
			// The UI's watchdog is driven by heartbeat *events*, not comment
			// frames, so one is sent as well; it carries the server's clock,
			// which is what the top bar's "live" indicator shows.
			if err := stream.event(string(events.KindHeartbeat), 0, heartbeatPayload(s)); err != nil {
				return
			}
		}
	}
}

func heartbeatPayload(s *Server) []byte {
	return []byte(`{"at":"` + s.ctrl.Now().Format(time.RFC3339) + `"}`)
}

// lastEventID reads where a reconnecting client left off.
//
// The header is what the browser's own EventSource retry sends. The query
// parameter is what the UI sends when it has given up and opened a fresh
// EventSource, which cannot carry the header; honouring both is what makes a
// reconnect seamless either way.
func lastEventID(r *http.Request) uint64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		// A malformed id means "start from now" rather than an error: the
		// alternative is a client that can never reconnect until it is
		// reloaded by hand.
		return 0
	}
	return n
}

func parseKinds(raw string) []events.Kind {
	var out []events.Kind
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, events.Kind(k))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// GET /api/v1/runners/{id}/logs
// ---------------------------------------------------------------------------

// logChunkKind is the event name a log frame carries. It is not one of the
// fleet event kinds: this is a different stream with a payload of its own.
const logChunkKind = "log"

// handleRunnerLogs tails a runner's output.
//
// The controller never dials an agent, so this asks the controller to have the
// runner's agent open an outbound relay and then fans that out here. The two
// things this handler must get right are ending the stream when the runner's
// output ends, and unsubscribing on every exit -- a leaked subscription keeps a
// relay open on a host for a browser tab that closed hours ago.
func (s *Server) handleRunnerLogs(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	opts := backend.LogOptions{
		Follow: queryBool(r, "follow", true),
		Tail:   queryInt(r, "tail", 1000),
	}
	if opts.Tail < 0 {
		opts.Tail = 0
	}

	ctx := r.Context()
	ch, cancel, err := s.ctrl.OpenLogStream(ctx, id, opts)
	if err != nil {
		s.logStreamFailed(w, r, err)
		return
	}
	// Always, on every path out: this is what tells the agent to stop reading
	// the container's output once the last viewer has gone.
	defer cancel()

	stream := startSSE(w, r)
	_ = stream.retry(2 * time.Second)
	if err := stream.comment("attached to " + id); err != nil {
		return
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				// The runner's output finished -- for an ephemeral runner that
				// means the job is over. Say so and end the response, rather
				// than leaving the browser holding a connection that will never
				// carry another byte.
				_ = stream.event("end", 0, []byte(`{"reason":"the runner's output ended"}`))
				return
			}
			if err := stream.event(logChunkKind, 0, jsonString(string(chunk))); err != nil {
				return
			}
		case <-ticker.C:
			if err := stream.comment("heartbeat"); err != nil {
				return
			}
		}
	}
}

// handleDownloadRunnerLogs returns a snapshot of a runner's output as a file.
//
// It is the same relay as the live tail, read until it goes quiet rather than
// followed: "download" has to terminate, and a runner that is still producing
// output would otherwise stream forever into a file the browser never finishes.
func (s *Server) handleDownloadRunnerLogs(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	run, err := s.ctrl.Store().GetRunner(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the runner", err)
		return
	}

	ch, cancel, err := s.ctrl.OpenLogStream(r.Context(), id, backend.LogOptions{Follow: false, Tail: 0})
	if err != nil {
		s.logStreamFailed(w, r, err)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+logFilename(run.Name)+`"`)
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// A backlog arrives in a burst and then stops; this bounds the wait for the
	// first chunk and the gap between chunks, so a wedged agent cannot hold the
	// request open indefinitely.
	const firstChunkWait = 10 * time.Second
	const quietFor = 2 * time.Second

	timer := time.NewTimer(firstChunkWait)
	defer timer.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			_ = rc.Flush()
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietFor)
		case <-timer.C:
			return
		}
	}
}

// logStreamFailed answers a stream that could not be opened.
//
// A runner that has been removed, or that never reached a host, cannot produce
// output; that is a 409 with the controller's own sentence rather than a 500,
// because it is a fact about the runner and not a fault in the server.
func (s *Server) logStreamFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, controller.ErrStreamUnknown) {
		s.fail(w, r, "opening the log stream", err)
		return
	}
	conflict(w, err.Error())
}

// logFilename renders the Content-Disposition name, keeping it to characters
// that survive every filesystem and every quoting rule on the way.
func logFilename(runnerName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, runnerName)
	if safe == "" {
		safe = "runner"
	}
	return safe + ".log"
}

// jsonString renders a log chunk as a JSON string, which is what keeps a line
// containing a newline or a quote from breaking the frame.
func jsonString(s string) []byte {
	b, err := marshalJSON(s)
	if err != nil {
		return []byte(`""`)
	}
	return b
}
