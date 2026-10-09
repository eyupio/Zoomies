package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// FakeOptions shapes a fake provider. Reply, when set, is what it says for a
// last user message; Fail, when set, is what every call returns.
type FakeOptions struct {
	Model string
	Reply func(last string) string
	Fail  error
}

// Fake is a provider that answers without a network, for the demo fleet and
// for tests. It streams its reply a word at a time so cancellation between
// deltas is a real path, reports usage, and calls a tool named echo when one
// is offered, which is the one canned behaviour the contract relies on.
type Fake struct {
	opts FakeOptions
}

// NewFake returns a fake provider.
func NewFake(opts FakeOptions) *Fake {
	if opts.Model == "" {
		opts.Model = "fake"
	}
	if opts.Reply == nil {
		opts.Reply = func(last string) string { return "The built-in model heard: " + last }
	}
	return &Fake{opts: opts}
}

// Chat implements Provider.
func (f *Fake) Chat(ctx context.Context, req Request) (Stream, error) {
	if f.opts.Fail != nil {
		return nil, f.opts.Fail
	}
	last := lastUserMessage(req.Messages)
	var events []Event
	if hasTool(req.Tools, "echo") {
		args, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{last})
		events = append(events, Event{ToolCall: &ToolCall{ID: "call_echo", Name: "echo", Arguments: args}})
	} else {
		reply := f.opts.Reply(last)
		for i, word := range strings.Split(reply, " ") {
			if i > 0 {
				word = " " + word
			}
			events = append(events, Event{Delta: word})
		}
	}
	events = append(events,
		Event{Usage: &Usage{InputTokens: len(last), OutputTokens: len(events), Reported: true}},
		Event{Done: true})
	return &fakeStream{events: events}, nil
}

// Models implements ModelLister: the model it was given and one larger, so the
// demo's list has something to choose between.
func (f *Fake) Models(ctx context.Context) ([]string, error) {
	if f.opts.Fail != nil {
		return nil, f.opts.Fail
	}
	return []string{f.opts.Model, f.opts.Model + "-large"}, nil
}

// Check implements Provider.
func (f *Fake) Check(ctx context.Context) (CheckResult, error) {
	if f.opts.Fail != nil {
		return CheckResult{}, f.opts.Fail
	}
	return CheckResult{Model: f.opts.Model, Latency: time.Millisecond, UsageReported: true}, nil
}

type fakeStream struct {
	events []Event
	ended  bool
}

func (s *fakeStream) Next(ctx context.Context) (Event, bool) {
	if s.ended {
		return Event{}, false
	}
	if err := ctx.Err(); err != nil {
		s.ended = true
		return Event{Err: err}, true
	}
	if len(s.events) == 0 {
		s.ended = true
		return Event{}, false
	}
	e := s.events[0]
	s.events = s.events[1:]
	if e.Done {
		s.ended = true
	}
	return e, true
}

func (s *fakeStream) Close() error { return nil }

func lastUserMessage(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

func hasTool(tools []Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
