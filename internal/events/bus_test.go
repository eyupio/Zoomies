package events

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublishReachesSubscribers(t *testing.T) {
	b := New()
	sub := b.Subscribe(context.Background(), SubscribeOptions{})
	defer sub.Close()

	b.Publish(KindRunnerUpdated, "runner:run_1", map[string]string{"state": "idle"})

	select {
	case e := <-sub.C:
		if e.Kind != KindRunnerUpdated {
			t.Errorf("kind = %s, want %s", e.Kind, KindRunnerUpdated)
		}
		if e.Topic != "runner:run_1" {
			t.Errorf("topic = %q", e.Topic)
		}
		var got map[string]string
		if err := json.Unmarshal(e.Data, &got); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if got["state"] != "idle" {
			t.Errorf("payload = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived")
	}
}

func TestSubscribeFiltersByKindAndTopic(t *testing.T) {
	b := New()
	sub := b.Subscribe(context.Background(), SubscribeOptions{
		Kinds:       []Kind{KindRunnerUpdated},
		TopicPrefix: "runner:",
	})
	defer sub.Close()

	b.Publish(KindPoolUpdated, "pool:p1", nil)     // wrong kind
	b.Publish(KindRunnerUpdated, "host:h1", nil)   // wrong topic
	b.Publish(KindRunnerUpdated, "runner:r1", nil) // both right

	select {
	case e := <-sub.C:
		if e.Topic != "runner:r1" {
			t.Fatalf("got %s/%s; the filter let the wrong event through", e.Kind, e.Topic)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the matching event never arrived")
	}
}

func TestReplayDeliversMissedEvents(t *testing.T) {
	b := New()
	b.Publish(KindScaling, "", map[string]int{"n": 1})
	after := b.LastID()
	b.Publish(KindScaling, "", map[string]int{"n": 2})

	// A client reconnecting with Last-Event-ID should be given what it missed.
	sub := b.Subscribe(context.Background(), SubscribeOptions{Replay: after})
	defer sub.Close()

	select {
	case e := <-sub.C:
		if e.ID != after+1 {
			t.Errorf("replayed event ID = %d, want %d", e.ID, after+1)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was replayed")
	}
}

func TestSlowSubscriberIsCutOffNotBlocking(t *testing.T) {
	b := New()
	b.buffer = 2
	sub := b.Subscribe(context.Background(), SubscribeOptions{})
	defer sub.Close()

	// A wedged browser tab must not be able to stop the fleet from scaling, so
	// publishing past a full queue never blocks.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			b.Publish(KindHeartbeat, "", nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that was not reading")
	}

	// And the subscriber that fell behind is told so, by its feed ending. A
	// stream quietly thinned by dropped events would leave its dashboard wrong
	// until somebody reloaded it; an ended stream makes the browser reconnect
	// and replay. It keeps what it had already been handed.
	received := 0
	for range sub.C {
		received++
	}
	if received != b.buffer {
		t.Fatalf("received %d buffered events before the feed ended, want %d", received, b.buffer)
	}
	if n := b.Subscribers(); n != 0 {
		t.Fatalf("%d subscribers remain after the slow one was cut off, want 0", n)
	}
}

// Subscribe spawns a goroutine that closes the subscription when the context
// ends, so an explicit Close and that goroutine routinely race. Run this with
// -race: it is the regression test for an unsynchronised write in Close.
func TestCloseIsSafeConcurrentlyAndRepeatedly(t *testing.T) {
	b := New()
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		sub := b.Subscribe(ctx, SubscribeOptions{})

		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); cancel() }()
		go func() { defer wg.Done(); sub.Close() }()
		go func() { defer wg.Done(); sub.Close() }()
		wg.Wait()
		cancel()
	}
	// Give the context watchers a moment, then assert nothing leaked.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && b.Subscribers() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := b.Subscribers(); n != 0 {
		t.Errorf("%d subscriptions leaked", n)
	}
}

func TestContextCancellationUnsubscribes(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	b.Subscribe(ctx, SubscribeOptions{})
	if b.Subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1", b.Subscribers())
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && b.Subscribers() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := b.Subscribers(); n != 0 {
		t.Errorf("subscribers = %d after cancellation, want 0", n)
	}
}

// A subscriber closing while a publish is in flight is the everyday case: a
// browser tab closes and its SSE handler's context ends while the reconcile
// loop is announcing a state change. Sending on the closed channel panics,
// and the loop that was publishing does not come back from a panic.
func TestPublishRacesClose(t *testing.T) {
	b := New()
	for i := 0; i < 2000; i++ {
		sub := b.Subscribe(context.Background(), SubscribeOptions{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			b.Publish(KindRunnerUpdated, "runner:r1", map[string]string{"id": "r1"})
		}()
		go func() {
			defer wg.Done()
			sub.Close()
		}()
		wg.Wait()
	}
	if n := b.Subscribers(); n != 0 {
		t.Errorf("%d subscriptions leaked", n)
	}
}

// Replay used to be silent about what it could not do: a client whose last id
// predated the ring, or whose id came from before a restart, was handed
// nothing and told nothing. Subscribe now says whether the gap was covered.
func TestReplayReportsWhetherItCoveredTheGap(t *testing.T) {
	b := New()
	b.ringCap = 3
	for i := range 6 {
		b.Publish(KindScaling, "", map[string]int{"n": i})
	}
	last := b.LastID()

	cases := []struct {
		name     string
		replay   uint64
		complete bool
	}{
		{"a fresh subscription asks for nothing and misses nothing", 0, true},
		{"the id just before the ring's oldest is covered", last - 3, true},
		{"an id the ring no longer reaches back to is not", 1, false},
		{"the latest id, with nothing since, is covered", last, true},
		{"an id from another run of the process is not", last + 40, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := b.Subscribe(context.Background(), SubscribeOptions{Replay: tc.replay})
			defer sub.Close()
			if sub.Complete != tc.complete {
				t.Fatalf("Complete = %v, want %v", sub.Complete, tc.complete)
			}
		})
	}

	t.Run("a caller that knows better can mark it incomplete", func(t *testing.T) {
		sub := b.Subscribe(context.Background(), SubscribeOptions{Replay: last, Incomplete: true})
		defer sub.Close()
		if sub.Complete {
			t.Fatal("Incomplete was ignored")
		}
	})
}

// IDs restart at one with every process, so an id alone cannot say which
// sequence it came from. On the wire it carries the run's epoch.
func TestWireIDsCarryTheEpoch(t *testing.T) {
	b := New()
	wire := b.WireID(42)
	if !strings.HasPrefix(wire, b.Epoch()+".") || !strings.HasSuffix(wire, ".42") {
		t.Fatalf("WireID(42) = %q, want <epoch>.42", wire)
	}
	if id, same := b.ParseWireID(wire); id != 42 || !same {
		t.Fatalf("ParseWireID(%q) = %d, %v; want 42 in this epoch", wire, id, same)
	}
	if id, same := b.ParseWireID("otherrun.42"); id != 42 || same {
		t.Fatalf("an id from another epoch parsed as %d, %v; want 42 and not this epoch", id, same)
	}
	// A bare number is what a client that predates the epoch sends; it is
	// taken as this epoch's so an upgrade does not resync every tab.
	if id, same := b.ParseWireID("42"); id != 42 || !same {
		t.Fatalf("a bare id parsed as %d, %v", id, same)
	}
	if id, same := b.ParseWireID("not-an-id"); id != 0 || same {
		t.Fatalf("garbage parsed as %d, %v", id, same)
	}
	other := New()
	if other.Epoch() == b.Epoch() {
		t.Fatal("two buses share an epoch; a restart would look like the same run")
	}
}
