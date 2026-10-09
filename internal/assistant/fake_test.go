package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The fake is what the demo fleet and every later slice's tests talk to, so
// it has to behave like a model in the ways those depend on: it answers from
// the last thing the person said, it reports usage, and it stops when asked.
func TestTheFakeAnswersFromTheLastUserMessage(t *testing.T) {
	f := NewFake(FakeOptions{Model: "demo", Reply: func(last string) string { return "You said: " + last }})
	req := Request{Messages: []Message{{Role: RoleUser, Content: "first"}, {Role: RoleUser, Content: "second"}}}
	events := collect(t, f, req)
	var text strings.Builder
	var usage *Usage
	done := false
	for _, e := range events {
		text.WriteString(e.Delta)
		if e.Usage != nil {
			if done {
				t.Error("usage arrived after Done")
			}
			usage = e.Usage
		}
		if e.Done {
			done = true
		}
	}
	if got := text.String(); got != "You said: second" {
		t.Errorf("answer %q", got)
	}
	if usage == nil || !usage.Reported {
		t.Errorf("usage %+v", usage)
	}
	if !done {
		t.Error("the stream never said Done")
	}
}

func TestTheFakeCallsTheEchoToolWhenItIsOffered(t *testing.T) {
	f := NewFake(FakeOptions{Model: "demo"})
	req := Request{
		Messages: []Message{{Role: RoleUser, Content: `Call the echo tool with "ping"`}},
		Tools:    []Tool{{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	var call *ToolCall
	for _, e := range collect(t, f, req) {
		if e.ToolCall != nil {
			call = e.ToolCall
		}
	}
	if call == nil || call.Name != "echo" {
		t.Fatalf("tool call %+v", call)
	}
	var args struct{ Text string }
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args.Text != `Call the echo tool with "ping"` {
		t.Errorf("arguments %s", call.Arguments)
	}
}

func TestTheFakeStopsWhenTheContextIsCancelled(t *testing.T) {
	f := NewFake(FakeOptions{Model: "demo", Reply: func(string) string { return "one two three four" }})
	ctx, cancel := context.WithCancel(context.Background())
	s, err := f.Chat(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Next(ctx); !ok {
		t.Fatal("no first event")
	}
	cancel()
	e, ok := s.Next(ctx)
	if !ok || !errors.Is(e.Err, context.Canceled) {
		t.Errorf("after cancel: ok=%v event=%+v", ok, e)
	}
}

func TestTheFakePassesTheContract(t *testing.T) {
	RunContractTests(t, "fake", func(t *testing.T) Provider {
		return NewFake(FakeOptions{Model: "demo"})
	})
}

// collect drains a chat into its events, failing the test on an error event.
func collect(t *testing.T, p Provider, req Request) []Event {
	t.Helper()
	ctx := context.Background()
	s, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events []Event
	for {
		e, ok := s.Next(ctx)
		if !ok {
			return events
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
