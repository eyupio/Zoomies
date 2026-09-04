package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// Tunables for the relay. A browser tab that has been backgrounded stops
// reading its SSE connection, and a runner compiling something noisy produces
// megabytes a minute; the queue depth bounds how much of that is held for a
// reader who may never come back.
const (
	logSubscriberQueue = 512
	logReadChunk       = 32 << 10
)

// ErrStreamUnknown is returned when an agent posts a log stream nobody is
// waiting for, which happens whenever the last viewer closed the tab between
// the task being issued and the agent acting on it.
var ErrStreamUnknown = errors.New("controller: no log stream is open with that ID")

// logRelay fans a runner's output out to everyone watching it.
//
// The direction is inverted compared with what one would expect, because the
// controller never dials an agent: a viewer asks the controller, the
// controller queues a stream_logs task, and the agent opens an outbound
// chunked POST that lands in AcceptLogStream.
type logRelay struct {
	c *Controller

	mu       sync.Mutex
	streams  map[string]*logStream // by stream ID
	byRunner map[string]string     // runner ID -> stream ID
}

type logStream struct {
	id       string
	runnerID string
	hostID   string

	mu     sync.Mutex
	subs   map[int]chan []byte
	nextID int
	closed bool
}

func newLogRelay(c *Controller) *logRelay {
	return &logRelay{c: c, streams: map[string]*logStream{}, byRunner: map[string]string{}}
}

// OpenLogStream subscribes to a runner's output and returns the channel the
// API's SSE handler reads, plus the function that unsubscribes.
//
// The channel is closed when the stream ends, so a handler can range over it.
// Bytes are dropped rather than queued without limit when a reader falls
// behind: a wedged browser tab must not hold a runner's output in memory.
func (c *Controller) OpenLogStream(ctx context.Context, runnerID string, opts backend.LogOptions) (<-chan []byte, func(), error) {
	r, err := c.st.GetRunner(ctx, runnerID)
	if err != nil {
		return nil, nil, err
	}
	if r.HostID == "" {
		return nil, nil, fmt.Errorf("runner %s is not on a host, so it has no logs to stream", runnerID)
	}
	if r.State == store.RunnerRemoved {
		// The container is gone and so is its output; saying so beats an empty
		// stream that looks like a runner producing nothing.
		return nil, nil, fmt.Errorf("runner %s has been removed; an ephemeral runner's output only exists while its container does", runnerID)
	}
	return c.relay.subscribe(r, opts)
}

// subscribe attaches a viewer, starting the stream if this is the first one.
func (lr *logRelay) subscribe(r *store.Runner, opts backend.LogOptions) (<-chan []byte, func(), error) {
	lr.mu.Lock()
	streamID, ok := lr.byRunner[r.ID]
	var s *logStream
	if ok {
		s = lr.streams[streamID]
	}
	fresh := s == nil
	if fresh {
		streamID = "log_" + store.NewSecret(8)
		s = &logStream{id: streamID, runnerID: r.ID, hostID: r.HostID, subs: map[int]chan []byte{}}
		lr.streams[streamID] = s
		lr.byRunner[r.ID] = streamID
	}
	lr.mu.Unlock()

	ch, subID, err := s.add()
	if err != nil {
		return nil, nil, err
	}

	if fresh {
		lr.c.enqueue(r.HostID, agent.Task{
			Kind:       agent.TaskStreamLogs,
			RunnerID:   r.ID,
			StreamID:   streamID,
			LogOptions: &opts,
		})
		lr.c.log.Debug("opened a log relay", "runner", r.ID, "stream", streamID, "host", r.HostID)
	}

	var once sync.Once
	cancel := func() { once.Do(func() { lr.unsubscribe(s, subID) }) }
	return ch, cancel, nil
}

// unsubscribe drops one viewer and tears the stream down when it was the last.
func (lr *logRelay) unsubscribe(s *logStream, subID int) {
	last := s.remove(subID)
	if !last {
		return
	}
	lr.mu.Lock()
	if lr.streams[s.id] == s {
		delete(lr.streams, s.id)
		delete(lr.byRunner, s.runnerID)
	}
	lr.mu.Unlock()

	// Nobody is watching, so tell the agent to stop reading the container's
	// output and close its outbound POST.
	lr.c.enqueue(s.hostID, agent.Task{
		Kind:     agent.TaskCancelLogs,
		RunnerID: s.runnerID,
		StreamID: s.id,
	})
	s.close()
	lr.c.log.Debug("closed a log relay; the last viewer went away", "runner", s.runnerID, "stream", s.id)
}

// AcceptLogStream consumes the agent's outbound chunked POST and fans it out.
// It returns when the agent closes the body or the stream is torn down.
func (c *Controller) AcceptLogStream(streamID string, r io.Reader) error {
	c.relay.mu.Lock()
	s := c.relay.streams[streamID]
	c.relay.mu.Unlock()
	if s == nil {
		return fmt.Errorf("%w: %s", ErrStreamUnknown, streamID)
	}

	buf := make([]byte, logReadChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// The buffer is reused, so each subscriber gets its own copy.
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if !s.send(chunk) {
				// The last viewer left mid-stream; the cancel task is already
				// queued, and returning ends the agent's POST.
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			s.close()
			return fmt.Errorf("log stream %s ended: %w", streamID, err)
		}
	}

	// The runner's output has finished, which for an ephemeral runner means
	// the job is over. Closing the subscriber channels is what ends the SSE
	// responses rather than leaving browsers holding an open connection.
	c.relay.mu.Lock()
	if c.relay.streams[streamID] == s {
		delete(c.relay.streams, streamID)
		delete(c.relay.byRunner, s.runnerID)
	}
	c.relay.mu.Unlock()
	s.close()
	return nil
}

// streamIDFor returns the stream currently open for a runner, or "".
func (lr *logRelay) streamIDFor(runnerID string) string {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.byRunner[runnerID]
}

// closeAll ends every stream, used at shutdown so no SSE response is left open.
func (lr *logRelay) closeAll() {
	lr.mu.Lock()
	streams := make([]*logStream, 0, len(lr.streams))
	for _, s := range lr.streams {
		streams = append(streams, s)
	}
	lr.streams = map[string]*logStream{}
	lr.byRunner = map[string]string{}
	lr.mu.Unlock()
	for _, s := range streams {
		s.close()
	}
}

// add registers a viewer.
func (s *logStream) add() (chan []byte, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, ErrStreamUnknown
	}
	s.nextID++
	ch := make(chan []byte, logSubscriberQueue)
	s.subs[s.nextID] = ch
	return ch, s.nextID, nil
}

// remove drops a viewer and reports whether it was the last one.
func (s *logStream) remove(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.subs[id]
	if !ok {
		return false
	}
	delete(s.subs, id)
	close(ch)
	return len(s.subs) == 0
}

// send fans one chunk out, dropping for subscribers that are not keeping up.
// It reports whether anybody is still listening.
func (s *logStream) send(chunk []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.subs) == 0 {
		return false
	}
	for _, ch := range s.subs {
		select {
		case ch <- chunk:
		default:
			// Dropped rather than blocked. A slow reader loses lines; every
			// other viewer, and the agent writing into the relay, do not stall.
		}
	}
	return true
}

// close ends the stream for everyone still attached.
func (s *logStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
}
