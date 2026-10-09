package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// decodeData turns each event's data into one delta, with "done" ending the
// stream, which is enough shape to test the plumbing every adapter shares.
func decodeData(e ServerEvent) ([]assistant.Event, error) {
	if e.Data == "done" {
		return []assistant.Event{{Done: true}}, nil
	}
	return []assistant.Event{{Delta: e.Data}}, nil
}

func open(t *testing.T, handler http.HandlerFunc) (assistant.Stream, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return NewStream(resp, decodeData), cancel
}

// A connection cut before the server says it is done is a failure the person
// should see as one, not a reply that happens to be short.
func TestAStreamCutBeforeItsTerminalEventReturnsAnError(t *testing.T) {
	s, cancel := open(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\ndata: two\n\n")
	})
	defer cancel()
	defer s.Close()
	var deltas []string
	for {
		e, ok := s.Next(context.Background())
		if !ok {
			t.Fatal("the stream ended with neither Done nor an error")
		}
		if e.Err != nil {
			if len(deltas) != 2 {
				t.Errorf("deltas before the error: %q", deltas)
			}
			return
		}
		if e.Done {
			t.Fatal("a cut stream reported Done")
		}
		deltas = append(deltas, e.Delta)
	}
}

func TestCancellingTheContextEndsTheStream(t *testing.T) {
	s, cancel := open(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	defer s.Close()
	cancel()
	done := make(chan assistant.Event, 1)
	go func() { e, _ := s.Next(context.Background()); done <- e }()
	select {
	case e := <-done:
		if !errors.Is(e.Err, context.Canceled) {
			t.Errorf("error %v is not context.Canceled", e.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("Next did not return within a second of cancellation")
	}
}

func TestADoneEventEndsTheStreamAndCloseIsNil(t *testing.T) {
	s, cancel := open(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: hi\n\ndata: done\n\n")
	})
	defer cancel()
	var last assistant.Event
	for {
		e, ok := s.Next(context.Background())
		if !ok {
			break
		}
		last = e
	}
	if !last.Done {
		t.Errorf("last event %+v is not Done", last)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
