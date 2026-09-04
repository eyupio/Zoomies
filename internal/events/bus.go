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
	KindProblems      Kind = "problems.updated"
	KindStats         Kind = "stats"
	KindAudit         Kind = "audit"
	KindWebhook       Kind = "webhook.delivery"
	// KindHeartbeat is an empty keep-alive so that proxies do not close an
	// idle SSE connection.
	KindHeartbeat Kind = "heartbeat"
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
// A slow subscriber is dropped rather than allowed to block the publisher: an
// operator whose browser tab is wedged must not stop the fleet from scaling.
type Bus struct {
	mu     sync.RWMutex
	subs   map[int]*subscriber
	nextID int
	seq    atomic.Uint64

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
	// dropped counts events discarded because this subscriber fell behind.
	dropped atomic.Uint64
}

// New returns a bus with sensible queue depths.
func New() *Bus {
	return &Bus{
		subs:    map[int]*subscriber{},
		buffer:  256,
		ringCap: 256,
	}
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

func (b *Bus) publish(e Event) {
	b.mu.Lock()
	if b.ringCap > 0 {
		b.ring = append(b.ring, e)
		if len(b.ring) > b.ringCap {
			b.ring = b.ring[len(b.ring)-b.ringCap:]
		}
	}
	subs := make([]*subscriber, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		if s.filter != nil && !s.filter(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			// The subscriber is not keeping up. Drop rather than block; the
			// client will resynchronise on its next full fetch.
			if n := s.dropped.Add(1); n == 1 || n%100 == 0 {
				slog.Warn("events: subscriber is behind, dropping events",
					"subscriber", s.id, "dropped", n)
			}
		}
	}
}

// Subscription is a live event feed.
type Subscription struct {
	C   <-chan Event
	bus *Bus
	id  int
}

// Close unsubscribes. It is safe to call more than once.
func (s *Subscription) Close() {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.mu.Lock()
	if sub, ok := s.bus.subs[s.id]; ok {
		delete(s.bus.subs, s.id)
		close(sub.ch)
	}
	s.bus.mu.Unlock()
	s.bus = nil
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
	if opts.Replay > 0 {
		for _, e := range b.ring {
			if e.ID > opts.Replay && filter(e) {
				select {
				case ch <- e:
				default:
				}
			}
		}
	}
	b.mu.Unlock()

	s := &Subscription{C: ch, bus: b, id: sub.id}
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
