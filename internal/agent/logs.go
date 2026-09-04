package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
)

const (
	// relayChunkSize is how much output one read moves.
	relayChunkSize = 32 << 10
	// relayQueueDepth is how far the relay may run ahead of the controller:
	// about 2 MiB of buffered output before anything is dropped.
	relayQueueDepth = 64
	// relayStopWait bounds how long stopping a stream waits for its copy to
	// wind down before the agent gets on with shutting down.
	relayStopWait = 10 * time.Second
)

// logRelay owns the log streams this agent has open. There is one per viewer
// watching a runner in the browser: the controller cannot dial in, so the agent
// pushes the output out.
type logRelay struct {
	tr  Transport
	log *slog.Logger

	mu      sync.Mutex
	streams map[string]*logStream
}

// logStream is one running relay.
type logStream struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *logStream) finish() { s.once.Do(func() { close(s.done) }) }

func newLogRelay(tr Transport, log *slog.Logger) *logRelay {
	return &logRelay{tr: tr, log: log.With("component", "agent.logs"), streams: make(map[string]*logStream)}
}

// start opens a runner's output and copies it to the controller until the
// runner stops producing, the controller closes its side, or cancel is called.
//
// Starting a stream that is already running is a no-op: the controller
// redelivers tasks, and a second copy of one stream would interleave output.
func (r *logRelay) start(ctx context.Context, streamID string, h backend.Handle, b backend.Backend, opts backend.LogOptions) error {
	r.mu.Lock()
	if _, ok := r.streams[streamID]; ok {
		r.mu.Unlock()
		return nil
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &logStream{cancel: cancel, done: make(chan struct{})}
	r.streams[streamID] = s
	r.mu.Unlock()

	src, err := b.Logs(sctx, h, opts)
	if err != nil {
		r.close(streamID, s)
		return fmt.Errorf("agent: opening logs for %s from the %s backend: %w", h, b.Kind(), err)
	}
	dst, err := r.tr.OpenLogStream(sctx, streamID)
	if err != nil {
		src.Close()
		r.close(streamID, s)
		return fmt.Errorf("agent: opening log stream %s to the controller: %w", streamID, err)
	}

	go r.pump(sctx, streamID, s, src, dst)
	return nil
}

// cancel stops one stream, which is what a viewer closing the browser tab
// becomes on the host.
func (r *logRelay) cancel(streamID string) {
	r.mu.Lock()
	s, ok := r.streams[streamID]
	r.mu.Unlock()
	if !ok {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(relayStopWait):
		r.log.Warn("log stream did not stop promptly", "stream", streamID, "waited", relayStopWait)
	}
}

// stopAll ends every stream, called on shutdown.
func (r *logRelay) stopAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.streams))
	for id := range r.streams {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.cancel(id)
	}
}

func (r *logRelay) close(streamID string, s *logStream) {
	r.mu.Lock()
	if cur, ok := r.streams[streamID]; ok && cur == s {
		delete(r.streams, streamID)
	}
	r.mu.Unlock()
	s.cancel()
	s.finish()
}

// pump moves output from the runner to the controller through a bounded queue.
//
// When the queue is full the relay drops output and says so, rather than
// blocking. Blocking would push back on the runner's own output pipe, and a
// container whose stdout nobody drains eventually stops making progress: one
// operator on a slow connection would then be able to stall a build. Losing the
// middle of a log with a marker saying what was lost is the cheaper failure.
func (r *logRelay) pump(ctx context.Context, streamID string, s *logStream, src io.ReadCloser, dst io.WriteCloser) {
	defer r.close(streamID, s)

	// Unblock a backend read that is not watching the context.
	stopWatch := context.AfterFunc(ctx, func() { src.Close() })
	defer stopWatch()

	queue := make(chan []byte, relayQueueDepth)
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		defer func() {
			if err := dst.Close(); err != nil {
				r.log.Debug("closing log stream reported an error", "stream", streamID, "error", err)
			}
		}()
		failed := false
		for chunk := range queue {
			if failed {
				// Keep draining: the reader must never block on a controller
				// that has gone away.
				continue
			}
			if _, err := dst.Write(chunk); err != nil {
				failed = true
				r.log.Debug("controller closed a log stream", "stream", streamID, "error", err)
			}
		}
	}()

	buf := make([]byte, relayChunkSize)
	var dropped int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := make([]byte, 0, n+64)
			if dropped > 0 {
				chunk = append(chunk, fmt.Sprintf("\n[log stream fell behind, %d bytes dropped]\n", dropped)...)
			}
			chunk = append(chunk, buf[:n]...)
			select {
			case queue <- chunk:
				dropped = 0
			default:
				dropped += int64(n)
			}
		}
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				r.log.Debug("log stream ended", "stream", streamID, "error", err)
			}
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if dropped > 0 {
		// The runner has stopped producing, so waiting for room here throttles
		// nothing: a viewer must always be told what was lost, even when the
		// drop happened in the last moments of the stream.
		select {
		case queue <- []byte(fmt.Sprintf("\n[log stream fell behind, %d bytes dropped]\n", dropped)):
		case <-time.After(relayStopWait):
		}
	}

	src.Close()
	close(queue)
	writer.Wait()
}
