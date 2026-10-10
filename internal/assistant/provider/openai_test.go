package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
)

func drain(t *testing.T, p assistant.Provider, req assistant.Request) []assistant.Event {
	t.Helper()
	s, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events []assistant.Event
	for {
		e, ok := s.Next(context.Background())
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

func hello() assistant.Request {
	return assistant.Request{Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "Say hello"}}}
}

// "/v1" is the commonest thing to get wrong about a local server's address:
// Ollama, LM Studio and vLLM all serve under it, OpenRouter under /api/v1,
// and an operator pastes whichever the server's own page showed them.
func TestOpenAICompatibleAppendsChatCompletionsOnceWhateverThePathGiven(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	for given, wantPath := range map[string]string{
		"":           "/v1/chat/completions",
		"/":          "/v1/chat/completions",
		"/v1":        "/v1/chat/completions",
		"/v1/":       "/v1/chat/completions",
		"/openai":    "/openai/chat/completions",
		"/openai/v1": "/openai/v1/chat/completions",
	} {
		before := len(srv.Requests())
		// The fake serves only /v1, so a path it does not know is answered
		// 404; what this test asserts is the path that was asked for.
		p := NewOpenAICompatible(Config{BaseURL: srv.URL + given, Model: "m"})
		if s, err := p.Chat(context.Background(), hello()); err == nil {
			s.Close()
		}
		reqs := srv.Requests()
		if len(reqs) != before+1 || reqs[before].Path != wantPath {
			t.Errorf("base %q: requested %q, want %q", given, reqs[len(reqs)-1].Path, wantPath)
		}
	}
}

func TestOpenAICompatibleStreamsDeltasToolCallsAndUsage(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	p := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"})
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
	if call == nil || call.Name != "echo" || call.ID != "call_1" || string(call.Arguments) != `{"text":"ping"}` {
		t.Errorf("tool call %+v", call)
	}
	last := srv.Requests()[len(srv.Requests())-1].Body
	tools, _ := last["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools sent: %v", last["tools"])
	}
	if last["stream"] != true {
		t.Error("stream was not requested")
	}
}

func TestOpenAICompatibleSendsTheKeyOnlyWhenItHasOne(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	drain(t, NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"}), hello())
	drain(t, NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m", APIKey: "sk-test"}), hello())
	reqs := srv.Requests()
	if got := reqs[0].Header.Get("Authorization"); got != "" {
		t.Errorf("without a key, Authorization %q", got)
	}
	if got := reqs[1].Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("with a key, Authorization %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// OpenCode answers 400 "Request is missing x-opencode-session" to anything
// without the header, so a test of an OpenCode preset failed before the key
// or the model was ever looked at. Other servers must not be sent it.
func TestOpenAICompatibleSendsAStableSessionHeaderOnlyToOpenCode(t *testing.T) {
	var seen []http.Header
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Header.Clone())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: http.Header{}}, nil
	})}
	for base, want := range map[string]bool{
		"https://opencode.ai/zen/go/v1": true,
		"https://opencode.ai/zen/v1":    true,
		"https://api.openai.com/v1":     false,
		"http://localhost:11434":        false,
	} {
		seen = nil
		p := NewOpenAICompatible(Config{BaseURL: base, Model: "m", Client: client})
		for i := 0; i < 2; i++ {
			if resp, err := p.do(context.Background(), http.MethodGet, "/models", nil); err == nil {
				resp.Body.Close()
			}
		}
		if len(seen) != 2 {
			t.Fatalf("%s: %d requests", base, len(seen))
		}
		a, b := seen[0].Get("x-opencode-session"), seen[1].Get("x-opencode-session")
		if (a != "") != want || a != b {
			t.Errorf("%s: session headers %q then %q, want sent=%v and stable", base, a, b, want)
		}
	}
}

func TestOpenAICompatibleCheckListsModelsThenCompletesOneToken(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	res, err := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"}).Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[0].Path != "/v1/models" || reqs[1].Path != "/v1/chat/completions" {
		t.Fatalf("requests %+v", reqs)
	}
	if reqs[1].Body["max_tokens"] != float64(1) {
		t.Errorf("max_tokens %v", reqs[1].Body["max_tokens"])
	}
	if res.Model != "m" || !res.UsageReported || res.Latency <= 0 {
		t.Errorf("result %+v", res)
	}
	srv.NoUsage = true
	res, _ = NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"}).Check(context.Background())
	if res.UsageReported {
		t.Error("usage reported without a usage chunk")
	}
}

func TestOpenAICompatibleTurnsARefusalIntoWords(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	srv.Status, srv.Body = 401, `{"error":{"message":"bad key sk-test"}}`
	_, err := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m", APIKey: "sk-test"}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused the key") || strings.Contains(err.Error(), "sk-test") {
		t.Errorf("err %v", err)
	}
}

func TestOpenAICompatiblePassesTheContract(t *testing.T) {
	assistant.RunContractTests(t, "openai-compatible", func(t *testing.T) assistant.Provider {
		return NewOpenAICompatible(Config{BaseURL: assistanttest.NewOpenAI(t).URL, Model: "m"})
	})
}

// Review minor: some gateways serve chat completions and nothing else, so a
// 404 from the model list must not fail the check; the one-token completion
// is what says whether the provider answers. A refused key on the list is
// still a refusal.
func TestOpenAICompatibleCheckSurvivesAGatewayWithNoModelList(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	srv.ModelsStatus = 404
	res, err := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"}).Check(context.Background())
	if err != nil {
		t.Fatalf("a 404 on /models failed the check: %v", err)
	}
	if res.Model != "m" {
		t.Errorf("result %+v", res)
	}
	srv.ModelsStatus, srv.Body = 401, `{"error":{"message":"nope"}}`
	if _, err := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m", APIKey: "k"}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the key") {
		t.Errorf("a 401 on /models: %v", err)
	}
}

// Review minor: a 401 with no key configured should send a person to add a
// key, not to rotate one they never set.
func TestOpenAICompatibleSaysAKeyIsNeededWhenNoneIsConfigured(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	srv.Status, srv.Body = 401, `{"error":{"message":"missing key"}}`
	_, err := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "needs an API key") {
		t.Errorf("err %v", err)
	}
}

// A provider that stops at its output ceiling says so with finish_reason
// length, and a thinking model spends that ceiling on reasoning the adapter
// never shows. Without the cut being said, an answer with no words in it looks
// exactly like a finished one, and the person is shown silence.
func TestOpenAICompatibleSaysWhenTheProviderCutTheAnswer(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	srv.Cut = true
	p := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"})
	var text strings.Builder
	var done *assistant.Event
	for _, e := range drain(t, p, hello()) {
		text.WriteString(e.Delta)
		if e.Done {
			d := e
			done = &d
		}
	}
	if text.String() != "" {
		t.Errorf("reasoning reached the page as words: %q", text.String())
	}
	if done == nil || !done.Cut {
		t.Errorf("the end of a cut answer does not say it was cut: %+v", done)
	}
}

// A provider told how hard to think is told on every request, the check
// included, so a model that does not take the setting says so under Test and
// not in the middle of a conversation. One told nothing is sent nothing: a
// server that does not know the field may refuse the whole request.
func TestOpenAICompatibleSendsTheReasoningEffortItWasGiven(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	drain(t, NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m", ReasoningEffort: "low"}), hello())
	if _, err := NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m", ReasoningEffort: "low"}).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	drain(t, NewOpenAICompatible(Config{BaseURL: srv.URL, Model: "m"}), hello())
	var chats []map[string]any
	for _, r := range srv.Requests() {
		if r.Path == "/v1/chat/completions" {
			chats = append(chats, r.Body)
		}
	}
	if len(chats) != 3 {
		t.Fatalf("chat requests = %d", len(chats))
	}
	if chats[0]["reasoning_effort"] != "low" || chats[1]["reasoning_effort"] != "low" {
		t.Errorf("the effort was not sent on the chat and the check: %v, %v", chats[0]["reasoning_effort"], chats[1]["reasoning_effort"])
	}
	if _, sent := chats[2]["reasoning_effort"]; sent {
		t.Errorf("an effort nobody set was sent: %v", chats[2]["reasoning_effort"])
	}
}
