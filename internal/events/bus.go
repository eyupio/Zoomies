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
	// The provider and machine kinds carry the same JSON their GET routes
	// return, as every other resource frame does. A machine is deleted from
	// the page only when its row goes -- the row outlives the resource on
	// purpose -- so machine.deleted is the prune's frame rather than the
	// hypervisor's.
	KindProviderUpdated Kind = "provider.updated"
	KindProviderDeleted Kind = "provider.deleted"
	KindMachineUpdated  Kind = "machine.updated"
	KindMachineDeleted  Kind = "machine.deleted"
	// The Kennel Club kinds follow the same rule. kennel.updated is one
	// repository exactly as GET /kennel/repositories/{id} returns it, and
	// kennel.deleted is the id of one that is gone -- pruned, or taken away with
	// its installation. kennel.summary is the Overview's document, computed and
	// sent only when it changes, as stats and problems.updated are: nothing
	// writes a row when a repository's last evaluation ages, so nothing could
	// announce it.
	KindKennelUpdated Kind = "kennel.updated"
	KindKennelDeleted Kind = "kennel.deleted"
	KindKennelSummary Kind = "kennel.summary"
	KindProblems      Kind = "problems.updated"
	KindStats         Kind = "stats"
	KindAudit         Kind = "audit"
	KindWebhook       Kind = "webhook.delivery"
	// KindHeartbeat is an empty keep-alive so that proxies do not close an
	// idle SSE connection.
	KindHeartbeat Kind = "heartbeat"
	// KindResync tells a client that what it holds may be stale and it should
	// fetch the resources afresh: the events between its last ID and now could
	// not all be replayed -- the ring had moved on, or the controller
	// restarted and the IDs began again -- or too much was deleted at once to
	// announce row by row. It is never in the ring, because it is true of the
	// connections it was sent to and of no connection made afterwards; the
	// SSE handler writes its own directly, and the controller sends one with
	// Bus.PublishTransient.
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

	// buffer is the per-subscriber queue depth. It is deliberately larger
	// than ringCap: a subscriber is handed the whole replay before it has
	// read a byte, so a queue only as deep as the ring is full at the moment
	// it is created, and the next publish -- one already waiting on this same
	// lock, on a fleet busy enough to have filled the ring -- would drop it.
	// The tab reconnects, replays a full ring again and is dropped again.
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
		buffer:  512,
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
	if e, ok := frame(kind, topic, v); ok {
		b.publish(e, true)
	}
}

// PublishTransient is Publish for a frame that must reach the subscribers who
// are here now and must never be replayed to one that arrives later.
//
// KindResync is the whole of it: it says "what you hold may be stale, fetch
// the resources again", which is true of the tab it was sent to and says
// nothing about a tab that connects afterwards -- that one is already fetching
// everything. Replaying it would send a client that has just loaded the fleet
// straight back to load it again.
func (b *Bus) PublishTransient(kind Kind, topic string, v any) {
	if e, ok := frame(kind, topic, v); ok {
		b.publish(e, false)
	}
}

// frame renders a payload into an unnumbered event, or reports that it could
// not be. The number is publish's to draw, under the lock.
func frame(kind Kind, topic string, v any) (Event, bool) {
	var raw json.RawMessage
	if v != nil {
		enc, err := json.Marshal(v)
		if err != nil {
			slog.Error("events: could not marshal payload", "kind", kind, "error", err)
			return Event{}, false
		}
		raw = enc
	}
	return Event{Kind: kind, Topic: topic, Data: raw, At: time.Now().UTC()}, true
}

// publish numbers the event, appends it to the ring and hands it to every
// subscriber.
//
// The number is drawn here, under the lock, rather than by the caller. Drawing
// it first and taking the lock afterwards let two publishers -- the reconcile
// loop and an API handler, say -- enter the ring in the opposite order to their
// numbers, and the wire with them. A browser handed 6 before 5 reconnects with
// 6 as its last id and never sees 5 again, while Subscribe still reports the
// replay complete because the ring's oldest id is no longer its lowest. There
// is no polling to put that right, so the tab quietly shows a fleet that has
// moved on.
//
// The sends happen under the lock on purpose. Close closes a subscriber's
// channel under that same lock, and a send on a closed channel panics even
// from a select with a default case -- so sending from a snapshot taken
// before releasing the lock let a browser tab closing at the wrong instant
// panic whichever loop was publishing, and the reconcile loop does not come
// back from that. Every send here is non-blocking, so holding the lock costs
// the other publishers microseconds.
// It still draws a number when it is not kept: the number is what a client
// comes back with, and a frame delivered without one would have the client
// resume from before it and be sent the events it has already seen.
func (b *Bus) publish(e Event, keep bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e.ID = b.seq.Add(1)
	if keep && b.ringCap > 0 {
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
	// done is closed by Close, so the goroutine Subscribe starts to watch the
	// context ends with the subscription. Without it a subscription closed by
	// hand under a context that never ends -- the SSE handler closes its own
	// before the request's context is cancelled -- left one goroutine waiting
	// per connection for the life of the process.
	done chan struct{}
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
		if s.done != nil {
			close(s.done)
		}
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
	s, _ := b.SubscribeWithin(ctx, opts, 0)
	return s
}

// SubscribeWithin is Subscribe with a ceiling on how many feeds may be open,
// reporting false and subscribing nothing when limit are already open. Zero
// is no ceiling.
//
// The count and the subscription are taken under the one lock, so a burst of
// streams opened together cannot all see room for one and all take it.
func (b *Bus) SubscribeWithin(ctx context.Context, opts SubscribeOptions, limit int) (*Subscription, bool) {
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

	b.mu.Lock()
	if limit > 0 && len(b.subs) >= limit {
		b.mu.Unlock()
		return nil, false
	}
	ch := make(chan Event, b.buffer)
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
					// The queue is deeper than the ring, so this is not
					// reachable -- but a replay that silently dropped a frame
					// while still calling itself complete is the one outcome
					// this whole path exists to prevent, so say so instead.
					complete = false
				}
			}
		}
	}
	if opts.Incomplete {
		complete = false
	}
	b.mu.Unlock()

	s := &Subscription{C: ch, bus: b, id: sub.id, Complete: complete, done: make(chan struct{})}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				s.Close()
			case <-s.done:
			}
		}()
	}
	return s, true
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
