package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ContractPrompt is the one prompt the contract sends with a tool offered: a
// real adapter's contract run answers it from a fake server that calls the
// tool, so the test is about the adapter's plumbing and not a model's mood.
const ContractPrompt = `Call the echo tool with "ping"`

// RunContractTests is the conformance suite every provider passes. It lives
// beside the interface so that the interface's promises (a stream ends, usage
// precedes Done, cancellation is honoured, Close is safe) are tested the same
// way for each adapter rather than described in a comment.
func RunContractTests(t *testing.T, name string, open func(t *testing.T) Provider) {
	t.Helper()
	ask := func(text string, tools ...Tool) Request {
		return Request{Messages: []Message{{Role: RoleUser, Content: text}}, Tools: tools}
	}
	t.Run(name+"/a chat streams at least one delta and ends with Done", func(t *testing.T) {
		events := drain(t, open(t), ask("Say hello"))
		deltas := 0
		for _, e := range events {
			if e.Delta != "" {
				deltas++
			}
		}
		if deltas == 0 {
			t.Error("no text delta")
		}
		if last := events[len(events)-1]; !last.Done {
			t.Errorf("last event %+v is not Done", last)
		}
	})
	t.Run(name+"/usage, when reported, arrives before Done", func(t *testing.T) {
		seenDone := false
		for _, e := range drain(t, open(t), ask("Say hello")) {
			if e.Usage != nil && seenDone {
				t.Error("usage after Done")
			}
			if e.Done {
				seenDone = true
			}
		}
	})
	t.Run(name+"/a cancelled context ends the stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := open(t).Chat(ctx, ask("Count to one hundred slowly"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		cancel()
		deadline := time.After(2 * time.Second)
		for {
			type result struct {
				e  Event
				ok bool
			}
			ch := make(chan result, 1)
			go func() { e, ok := s.Next(ctx); ch <- result{e, ok} }()
			select {
			case r := <-ch:
				if !r.ok {
					t.Fatal("the stream ended without an error event")
				}
				if r.e.Err != nil {
					if !errors.Is(r.e.Err, context.Canceled) {
						t.Errorf("error %v is not context.Canceled", r.e.Err)
					}
					if _, ok := s.Next(ctx); ok {
						t.Error("Next returned an event after the error")
					}
					return
				}
				if r.e.Done {
					t.Fatal("the stream finished instead of being cancelled")
				}
			case <-deadline:
				t.Fatal("Next did not return within two seconds of cancellation")
			}
		}
	})
	t.Run(name+"/Close after Done returns nil", func(t *testing.T) {
		s, err := open(t).Chat(context.Background(), ask("Say hello"))
		if err != nil {
			t.Fatal(err)
		}
		for {
			e, ok := s.Next(context.Background())
			if !ok || e.Done || e.Err != nil {
				break
			}
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	t.Run(name+"/Check names the model", func(t *testing.T) {
		res, err := open(t).Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Model == "" {
			t.Error("Check returned no model")
		}
	})
	t.Run(name+"/a tool offered with the contract prompt is called", func(t *testing.T) {
		tool := Tool{Name: "echo", Description: "Echo the text", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}
		var call *ToolCall
		for _, e := range drain(t, open(t), ask(ContractPrompt, tool)) {
			if e.ToolCall != nil {
				call = e.ToolCall
			}
		}
		if call == nil || call.Name != "echo" {
			t.Fatalf("tool call %+v", call)
		}
		if !json.Valid(call.Arguments) {
			t.Errorf("arguments %q are not JSON", call.Arguments)
		}
	})
}

// drain reads a chat to its end and fails on an error event.
func drain(t *testing.T, p Provider, req Request) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events []Event
	for {
		e, ok := s.Next(ctx)
		if !ok {
			t.Fatal("the stream ended without Done")
		}
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
		events = append(events, e)
		if e.Done {
			return events
		}
	}
}
