package provider

import (
	"context"
	"errors"
	"iter"
	"net/http"

	"github.com/eyupio/zoomies/internal/assistant"
)

// Decoder turns one server event into the assistant's events: none for a
// keep-alive or a frame the adapter ignores, several when one frame carries
// a delta and the usage, and one with Done when the server is finished.
type Decoder func(ServerEvent) ([]assistant.Event, error)

// NewStream wraps a streaming response. Events are read in the caller's
// goroutine on Next, so there is nothing to leak; cancellation closes the
// body from a context.AfterFunc, which is what makes a blocked read return.
func NewStream(resp *http.Response, decode Decoder) assistant.Stream {
	s := &stream{resp: resp, decode: decode}
	s.next, s.stop = iter.Pull2(ReadEvents(resp.Body))
	return s
}

type stream struct {
	resp   *http.Response
	decode Decoder
	next   func() (ServerEvent, error, bool)
	stop   func()
	queue  []assistant.Event
	ended  bool
}

func (s *stream) Next(ctx context.Context) (assistant.Event, bool) {
	if s.ended {
		return assistant.Event{}, false
	}
	for len(s.queue) == 0 {
		if err := ctx.Err(); err != nil {
			return s.end(assistant.Event{Err: err})
		}
		// The read below blocks on the body; closing the body is what frees
		// it when the context ends first.
		stopWatch := context.AfterFunc(ctx, func() { s.resp.Body.Close() })
		ev, err, ok := s.next()
		stopWatch()
		if ctx.Err() != nil {
			return s.end(assistant.Event{Err: ctx.Err()})
		}
		if err != nil {
			return s.end(assistant.Event{Err: err})
		}
		if !ok {
			// The server went away before it said it was done. A reply that
			// happens to be short is not the same thing, so this is an error.
			return s.end(assistant.Event{Err: errors.New("the provider closed the stream before it finished answering")})
		}
		events, err := s.decode(ev)
		if err != nil {
			return s.end(assistant.Event{Err: err})
		}
		s.queue = append(s.queue, events...)
	}
	e := s.queue[0]
	s.queue = s.queue[1:]
	if e.Done || e.Err != nil {
		return s.end(e)
	}
	return e, true
}

func (s *stream) end(e assistant.Event) (assistant.Event, bool) {
	s.ended = true
	s.stop()
	s.resp.Body.Close()
	return e, true
}

// Close releases the connection. After the stream ended it is a no-op, and
// before, it drains nothing: a reply nobody is reading is not worth the
// bytes.
func (s *stream) Close() error {
	if !s.ended {
		s.ended = true
		s.stop()
	}
	s.resp.Body.Close()
	return nil
}
