package events

import (
	"context"
	"encoding/json"
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

func TestSlowSubscriberIsDroppedNotBlocking(t *testing.T) {
	b := New()
	b.buffer = 2
	sub := b.Subscribe(context.Background(), SubscribeOptions{})
	defer sub.Close()

	// A wedged browser tab must not be able to stop the fleet from scaling, so
	// publishing past a full queue drops rather than blocks.
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
