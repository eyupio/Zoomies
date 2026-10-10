package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
)

func TestAnthropicSendsTheMessagesRequestWithItsHeaders(t *testing.T) {
	srv := assistanttest.NewAnthropic(t)
	p := NewAnthropic(Config{BaseURL: srv.URL, APIKey: "sk-ant", Model: "m"})
	tool := assistant.Tool{Name: "echo", Description: "Echo", Parameters: json.RawMessage(`{"type":"object"}`)}
	req := assistant.Request{
		System: "Be brief.",
		Messages: []assistant.Message{
			{Role: assistant.RoleUser, Content: "hi"},
			{Role: assistant.RoleAssistant, ToolCalls: []assistant.ToolCall{{ID: "toolu_0", Name: "echo", Arguments: json.RawMessage(`{"text":"x"}`)}}},
			{Role: assistant.RoleTool, ToolCallID: "toolu_0", Content: "x"},
			{Role: assistant.RoleUser, Content: "Say hello"},
		},
		Tools: []assistant.Tool{tool},
	}
	drain(t, p, req)
	rec := srv.Requests()[0]
	if rec.Path != "/v1/messages" || rec.Header.Get("x-api-key") != "sk-ant" || rec.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("request %s %v", rec.Path, rec.Header)
	}
	if rec.Body["system"] != "Be brief." || rec.Body["stream"] != true || rec.Body["model"] != "m" {
		t.Errorf("body %v", rec.Body)
	}
	tools, _ := rec.Body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["input_schema"] == nil {
		t.Errorf("tools %v", rec.Body["tools"])
	}
	msgs, _ := rec.Body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages %v", msgs)
	}
	second, _ := msgs[1].(map[string]any)
	blocks, _ := second["content"].([]any)
	if second["role"] != "assistant" || len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "tool_use" {
		t.Errorf("assistant tool turn %v", second)
	}
	third, _ := msgs[2].(map[string]any)
	blocks, _ = third["content"].([]any)
	if third["role"] != "user" || len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "tool_result" || blocks[0].(map[string]any)["tool_use_id"] != "toolu_0" {
		t.Errorf("tool result turn %v", third)
	}
}

func TestAnthropicStreamsContentBlockEventsIntoDeltasToolCallsAndUsage(t *testing.T) {
	srv := assistanttest.NewAnthropic(t)
	p := NewAnthropic(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	var text strings.Builder
	var usage *assistant.Usage
	for _, e := range drain(t, p, hello()) {
		text.WriteString(e.Delta)
		if e.Usage != nil {
			usage = e.Usage
		}
	}
	if text.String() != "Hello from the fake" {
		t.Errorf("text %q", text.String())
	}
	if usage == nil || !usage.Reported || usage.InputTokens != 7 || usage.OutputTokens != 4 {
		t.Errorf("usage %+v", usage)
	}
	tool := assistant.Tool{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}
	req := assistant.Request{Messages: []assistant.Message{{Role: assistant.RoleUser, Content: assistant.ContractPrompt}}, Tools: []assistant.Tool{tool}}
	var call *assistant.ToolCall
	for _, e := range drain(t, p, req) {
		if e.ToolCall != nil {
			call = e.ToolCall
		}
	}
	if call == nil || call.Name != "echo" || call.ID != "toolu_1" || string(call.Arguments) != `{"text":"ping"}` {
		t.Errorf("tool call %+v", call)
	}
}

func TestAnthropicCheckIsOneRequestOfOneToken(t *testing.T) {
	srv := assistanttest.NewAnthropic(t)
	res, err := NewAnthropic(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Body["max_tokens"] != float64(1) {
		t.Errorf("requests %+v", reqs)
	}
	if res.Model != "m" || !res.UsageReported {
		t.Errorf("result %+v", res)
	}
}

func TestAnthropicPassesTheContract(t *testing.T) {
	assistant.RunContractTests(t, "anthropic", func(t *testing.T) assistant.Provider {
		return NewAnthropic(Config{BaseURL: assistanttest.NewAnthropic(t).URL, APIKey: "k", Model: "m"})
	})
}

// Review Focus 1 on the second adapter: a gateway in front of the API
// usually publishes its address ending in /v1, and the Messages path must
// not double it.
func TestAnthropicAppendsTheMessagesPathOnceWhateverThePathGiven(t *testing.T) {
	srv := assistanttest.NewAnthropic(t)
	for given, wantPath := range map[string]string{
		"":       "/v1/messages",
		"/":      "/v1/messages",
		"/v1":    "/v1/messages",
		"/v1/":   "/v1/messages",
		"/gw":    "/gw/v1/messages",
		"/gw/v1": "/gw/v1/messages",
	} {
		before := len(srv.Requests())
		p := NewAnthropic(Config{BaseURL: srv.URL + given, APIKey: "k", Model: "m"})
		if s, err := p.Chat(context.Background(), hello()); err == nil {
			s.Close()
		}
		reqs := srv.Requests()
		if len(reqs) != before+1 || reqs[before].Path != wantPath {
			t.Errorf("base %q: requested %q, want %q", given, reqs[len(reqs)-1].Path, wantPath)
		}
	}
}

func TestAnthropicSaysAKeyIsNeededWhenNoneIsConfigured(t *testing.T) {
	srv := assistanttest.NewAnthropic(t)
	srv.Status, srv.Body = 401, `{"type":"error","error":{"message":"missing key"}}`
	_, err := NewAnthropic(Config{BaseURL: srv.URL, Model: "m"}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "needs an API key") {
		t.Errorf("err %v", err)
	}
}
