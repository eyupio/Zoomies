// Package assistant defines what a model provider is to the assistant: one
// streaming chat call with tool calls, usage and cancellation, and one check.
// Every adapter satisfies Provider and passes RunContractTests, so the panel,
// the tools and the limits in later slices never branch on which model
// answers. Nothing outside this package imports an adapter (purity_test.go).
package assistant

import (
	"context"
	"encoding/json"
	"time"
)

// Role is who a message is from. The names are the ones both wire protocols
// use, so an adapter maps nothing.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn. A tool's answer is a message of RoleTool carrying the
// ToolCallID it answers; an assistant turn that called tools carries them.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// Tool is a function the model may call. Parameters is a JSON Schema object.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is the model asking for a tool, with its arguments as the JSON
// the model wrote. The caller parses them; a model may escape strings in
// ways a substring match would miss.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Request is one chat call. System is the operator's framing, sent the way
// each protocol wants it. MaxTokens zero means the adapter's default.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Usage is what the provider said it counted. Reported is false when the
// provider sends no usage at all, which is distinct from zero tokens.
type Usage struct {
	InputTokens  int
	OutputTokens int
	Reported     bool
}

// Event is one thing a stream yields: a text delta, a complete tool call,
// the usage, the end, or an error. An error ends the stream; Done does too.
// Cut, on the end, says the provider stopped the answer at its output ceiling
// rather than because the model had finished: a thinking model can spend the
// whole ceiling on reasoning the adapter never shows and send no words at all,
// and an end that does not say so makes that silence look like an answer.
type Event struct {
	Delta    string
	ToolCall *ToolCall
	Usage    *Usage
	Done     bool
	Cut      bool
	Err      error
}

// Stream is a chat in progress. Next blocks for the next event and reports
// false once the stream has ended; the caller closes it whichever way the
// stream ended, so an adapter can release its connection.
type Stream interface {
	Next(ctx context.Context) (Event, bool)
	Close() error
}

// CheckResult is what a check learned: which model answered, how long the
// round trip took, and whether usage came back with it.
type CheckResult struct {
	Model         string
	Latency       time.Duration
	UsageReported bool
}

// Provider is a model the assistant can talk to.
type Provider interface {
	Chat(ctx context.Context, req Request) (Stream, error)
	Check(ctx context.Context) (CheckResult, error)
}

// ModelLister is optional: a provider that can say which models it serves, so
// the settings page can offer them in a list and not ask a person to remember
// how the provider spells one. A provider that cannot is typed into.
type ModelLister interface {
	// Models returns the model names, sorted and without repeats. The list is
	// the provider's own and is a choice for a person to make, not a fact the
	// assistant relies on.
	Models(ctx context.Context) ([]string, error)
}
