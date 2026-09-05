// Package events is the in-process publish/subscribe bus that makes the UI
// live.
//
// Every state change the operator would want to see -- a runner changing state,
// a scaling decision, a job starting -- is published here, and the API's
// Server-Sent Events endpoint fans it out to browsers. There is no polling
// anywhere in the UI.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kind names an event type. The UI switches on these to decide what to update,
// so they are part of the API's contract.
type Kind string

const (
	KindRunnerCreated Kind = "runner.created"
	KindRunnerUpdated Kind = "runner.updated"
	KindRunnerDeleted Kind = "runner.deleted"
	KindPoolCreated   Kind = "pool.created"
	KindPoolUpdated   Kind = "pool.updated"
	KindPoolDeleted   Kind = "pool.deleted"
	KindJobUpdated    Kind = "job.updated"
	KindHostUpdated   Kind = "host.updated"
	KindHostDeleted   Kind = "host.deleted"
	KindScaling       Kind = "scaling"
	KindInstallation  Kind = "installation.updated"
	// KindInstallationDeleted is sent when an installation is removed; the
	// pools that depended on it go with it, each with its own pool.deleted.
	KindInstallationDeleted Kind = "installation.deleted"
	KindProblems            Kind = "problems.updated"
	KindStats               Kind = "stats"
	KindAudit               Kind = "audit"
	KindWebhook             Kind = "webhook.delivery"
	// KindHeartbeat is an empty keep-alive so that proxies do not close an
	// idle SSE connection.
	KindHeartbeat Kind = "heartbeat"
	// KindResync tells a reconnecting client that the events between its last
	// ID and now could not all be replayed -- the ring had moved on, or the
	// controller restarted and the IDs began again -- so what it holds may be
	// stale and it should fetch the resources afresh. It is the first frame
	// on such a connection and is never in the ring.
	KindResync Kind = "resync"
)

// Event is one published change.
type Event struct {
	// ID is a monotonic sequence number, sent as the SSE event id so a
	// reconnecting client can say where it left off.
	ID   uint64 `json:"id"`
	Kind Kind   `json:"kind"`
	// Topic narrows the event, e.g. "runner:run_abc" or "pool:pool_xyz".
	// Subscribers may filter on it.
	Topic string `json:"topic,omitempty"`
	// Data is the payload, already marshalled by the publisher.
	Data json.RawMessage `json:"data,omitempty"`
	At   time.Time       `json:"at"`
}

// Bus fans events out to subscribers.
//
// A slow subscriber is cut off rather than allowed to block the publisher: an
// operator whose browser tab is wedged must not stop the fleet from scaling.
// Cut off, not thinned: its feed ends, the SSE handler closes the response,
// and the browser reconnects with its last event ID and catches up from the
// ring. Dropping single events would leave that tab quietly showing a fleet
// that no longer exists, with no polling to ever put it right.
type Bus struct {
	mu     sync.RWMutex
	subs   map[int]*subscriber
	nextID int
	seq    atomic.Uint64

	// epoch names this process's run. Event IDs restart at one with every
	// controller start, so a client's Last-Event-ID from before a restart is
	// a number from a different sequence -- usually one this process has not
	// reached yet, which replayed nothing and said nothing. The epoch travels
	// with the ID on the wire so a client that comes back with the wrong one
	// is told to resynchronise instead.
	epoch string

	// buffer is the per-subscriber queue depth.
	buffer int
	// ring keeps recent events so a client that reconnects within a few
	// seconds does not miss anything.
	ring    []Event
	ringCap int
}

type subscriber struct {
	id     int
	ch     chan Event
	filter func(Event) bool
}

// New returns a bus with sensible queue depths.
func New() *Bus {
	return &Bus{
		subs:    map[int]*subscriber{},
		epoch:   strconv.FormatInt(time.Now().UnixNano(), 36),
		buffer:  256,
		ringCap: 256,
	}
}

// Epoch identifies this process's run of the sequence; see Bus.epoch.
func (b *Bus) Epoch() string { return b.epoch }

// WireID renders an event ID for the wire: the epoch, a dot, the sequence
// number. ParseWireID reads it back.
func (b *Bus) WireID(id uint64) string {
	return b.epoch + "." + strconv.FormatUint(id, 10)
}

// ParseWireID reads a Last-Event-ID the client sent back. It returns the
// sequence number to replay from and whether that number belongs to this
// process's sequence at all; a bare number, from a client that predates the
// epoch, is taken as this epoch's so an upgrade does not resync every tab for
// nothing.
func (b *Bus) ParseWireID(raw string) (id uint64, sameEpoch bool) {
	raw = strings.TrimSpace(raw)
	epoch, seq, dotted := strings.Cut(raw, ".")
	if !dotted {
		seq, epoch = raw, b.epoch
	}
	n, err := strconv.ParseUint(seq, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, epoch == b.epoch
}

// Publish marshals v and delivers it to every matching subscriber. Marshalling
// failures are logged rather than returned, because a publisher in the middle
// of a reconcile has nothing useful to do with the error.
func (b *Bus) Publish(kind Kind, topic string, v any) {
	var raw json.RawMessage
	if v != nil {
		enc, err := json.Marshal(v)
		if err != nil {
			slog.Error("events: could not marshal payload", "kind", kind, "error", err)
			return
		}
		raw = enc
	}
	b.publish(Event{
		ID:    b.seq.Add(1),
		Kind:  kind,
		Topic: topic,
		Data:  raw,
		At:    time.Now().UTC(),
	})
}

// publish appends to the ring and hands the event to every subscriber.
//
// The sends happen under the lock on purpose. Close closes a subscriber's
// channel under that same lock, and a send on a closed channel panics even
// from a select with a default case -- so sending from a snapshot taken
// before releasing the lock let a browser tab closing at the wrong instant
// panic whichever loop was publishing, and the reconcile loop does not come
// back from that. Every send here is non-blocking, so holding the lock costs
// the other publishers microseconds.
func (b *Bus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ringCap > 0 {
		b.ring = append(b.ring, e)
		if len(b.ring) > b.ringCap {
			b.ring = b.ring[len(b.ring)-b.ringCap:]
		}
	}

	for _, s := range b.subs {
		if s.filter != nil && !s.filter(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			// The subscriber is not keeping up. Ending its feed here, under
			// the same lock Close takes and after removing it from the map,
			// is what stops Close from closing the channel a second time.
			// What it has already been handed stays in the channel for it to
			// drain; only then does it see the end and reconnect.
			slog.Warn("events: subscriber is behind; ending its feed so the client resynchronises",
				"subscriber", s.id, "buffer", b.buffer)
			delete(b.subs, s.id)
			close(s.ch)
		}
	}
}

// Subscription is a live event feed.
type Subscription struct {
	C   <-chan Event
	bus *Bus
	id  int
	// Complete reports whether the subscriber has everything it asked for: a
	// subscription that asked for no replay, or one whose Replay the ring
	// still reached back to. When it is false the client missed events it
	// will never be sent, and the SSE handler tells it so with a resync
	// frame before anything else.
	Complete bool
	// once makes Close idempotent AND race-free. Both matter: Subscribe spawns
	// a goroutine that closes the subscription when the context ends, so an
	// explicit Close from the handler and that goroutine routinely run at the
	// same time. An earlier version cleared s.bus at the end of Close, which
	// was an unsynchronised write to a field the other caller was reading.
	once sync.Once
}

// Close unsubscribes. It is safe to call more than once and from more than one
// goroutine at a time.
func (s *Subscription) Close() {
	if s == nil || s.bus == nil {
		return
	}
	s.once.Do(func() {
		s.bus.mu.Lock()
		if sub, ok := s.bus.subs[s.id]; ok {
			delete(s.bus.subs, s.id)
			close(sub.ch)
		}
		s.bus.mu.Unlock()
	})
}

// SubscribeOptions narrows a subscription.
type SubscribeOptions struct {
	// Kinds, when non-empty, limits delivery to these event kinds.
	Kinds []Kind
	// TopicPrefix, when non-empty, limits delivery to matching topics.
	TopicPrefix string
	// Replay delivers buffered events with an ID greater than this before any
	// new ones, so a reconnecting client can catch up.
	Replay uint64
	// Incomplete says the caller already knows the client missed events --
	// its Last-Event-ID was from another run of the process -- so the
	// subscription is marked incomplete whatever the ring holds.
	Incomplete bool
}

// Subscribe returns a feed. The caller must Close it.
func (b *Bus) Subscribe(ctx context.Context, opts SubscribeOptions) *Subscription {
	kinds := map[Kind]bool{}
	for _, k := range opts.Kinds {
		kinds[k] = true
	}
	filter := func(e Event) bool {
		if len(kinds) > 0 && !kinds[e.Kind] {
			return false
		}
		if opts.TopicPrefix != "" && !strings.HasPrefix(e.Topic, opts.TopicPrefix) {
			return false
		}
		return true
	}

	ch := make(chan Event, b.buffer)

	b.mu.Lock()
	b.nextID++
	sub := &subscriber{id: b.nextID, ch: ch, filter: filter}
	b.subs[sub.id] = sub
	// Replay under the same lock so an event cannot slip between the replay
	// and the subscription taking effect.
	complete := true
	if opts.Replay > 0 {
		// The ring covers the gap when the event after Replay is still in
		// it, or when nothing has been published since. A Replay beyond the
		// sequence is from another run of the process and covers nothing.
		last := b.seq.Load()
		switch {
		case opts.Replay > last:
			complete = false
		case opts.Replay == last:
		case len(b.ring) == 0 || b.ring[0].ID > opts.Replay+1:
			complete = false
		}
		for _, e := range b.ring {
			if e.ID > opts.Replay && filter(e) {
				select {
				case ch <- e:
				default:
				}
			}
		}
	}
	if opts.Incomplete {
		complete = false
	}
	b.mu.Unlock()

	s := &Subscription{C: ch, bus: b, id: sub.id, Complete: complete}
	if ctx != nil {
		go func() {
			<-ctx.Done()
			s.Close()
		}()
	}
	return s
}

// Subscribers returns the current subscriber count, which the UI shows on the
// Settings page as a sanity check that live updates are wired up.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// LastID returns the most recently issued event ID.
func (b *Bus) LastID() uint64 { return b.seq.Load() }
