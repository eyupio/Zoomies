package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// OpenAICompatible speaks the chat-completions protocol: OpenAI itself, and
// Ollama, LM Studio, vLLM, llama.cpp, OpenRouter and the gateways, which
// all serve it. It is the local path and the "it just works" path.
type OpenAICompatible struct {
	cfg  Config
	base string
	// session is the x-opencode-session value, empty for every other server.
	session string
}

// NewOpenAICompatible returns an adapter for the server at cfg.BaseURL.
//
// The address is used as given, minus a trailing slash, with "/v1" added
// only when no path was typed at all: the local servers all serve under
// /v1, OpenRouter under /api/v1, and an operator pastes whichever their
// server's own page showed them. Appending a path to a path they typed
// would double it.
func NewOpenAICompatible(cfg Config) *OpenAICompatible {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if i := strings.Index(base, "://"); i >= 0 && !strings.Contains(base[i+3:], "/") {
		base += "/v1"
	}
	p := &OpenAICompatible{cfg: cfg, base: base}
	if isOpenCode(base) {
		p.session = newSessionID()
	}
	return p
}

// isOpenCode reports whether the address is one of OpenCode's hosted
// endpoints (Zen or Go). Only those get the session header: sending a
// stranger's header to OpenAI or a local server would be noise at best.
func isOpenCode(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// newSessionID returns a random identifier for one adapter's lifetime.
// OpenCode refuses a request with no x-opencode-session (a 400 saying it
// cannot be routed efficiently), and it uses the value only to keep one
// conversation on one backend, so a per-adapter id is enough.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "zoomies"
	}
	return "zoomies-" + hex.EncodeToString(b[:])
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

func (p *OpenAICompatible) body(req assistant.Request, maxTokens int) ([]byte, error) {
	msgs := make([]oaMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		om := oaMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			var tc oaToolCall
			tc.ID, tc.Type = c.ID, "function"
			tc.Function.Name, tc.Function.Arguments = c.Name, string(c.Arguments)
			om.ToolCalls = append(om.ToolCalls, tc)
		}
		msgs = append(msgs, om)
	}
	out := map[string]any{
		"model":          p.model(req.Model),
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if maxTokens > 0 {
		out["max_tokens"] = maxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			fn := map[string]any{"name": t.Name, "description": t.Description}
			if len(t.Parameters) > 0 {
				fn["parameters"] = t.Parameters
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		out["tools"] = tools
	}
	return json.Marshal(out)
}

func (p *OpenAICompatible) model(override string) string {
	if override != "" {
		return override
	}
	return p.cfg.Model
}

func (p *OpenAICompatible) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var r *bytes.Reader
	if body != nil {
		r = bytes.NewReader(body)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	if p.session != "" {
		req.Header.Set("x-opencode-session", p.session)
	}
	resp, err := p.cfg.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the provider: %w", err)
	}
	if err := StatusError(resp, p.cfg.APIKey != ""); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// Chat implements assistant.Provider.
func (p *OpenAICompatible) Chat(ctx context.Context, req assistant.Request) (assistant.Stream, error) {
	return p.chat(ctx, req, req.MaxTokens)
}

func (p *OpenAICompatible) chat(ctx context.Context, req assistant.Request, maxTokens int) (assistant.Stream, error) {
	body, err := p.body(req, maxTokens)
	if err != nil {
		return nil, err
	}
	resp, err := p.do(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	return NewStream(resp, newOpenAIDecoder()), nil
}

// newOpenAIDecoder decodes chat-completion chunks. Tool calls arrive as
// fragments keyed by index across chunks and are emitted whole when the
// choice finishes, which is the only point the arguments are complete JSON.
func newOpenAIDecoder() Decoder {
	type partial struct {
		id, name string
		args     strings.Builder
	}
	var calls []*partial
	flush := func() []assistant.Event {
		var out []assistant.Event
		for _, c := range calls {
			args := c.args.String()
			if args == "" {
				args = "{}"
			}
			out = append(out, assistant.Event{ToolCall: &assistant.ToolCall{ID: c.id, Name: c.name, Arguments: json.RawMessage(args)}})
		}
		calls = nil
		return out
	}
	return func(e ServerEvent) ([]assistant.Event, error) {
		if strings.TrimSpace(e.Data) == "[DONE]" {
			return append(flush(), assistant.Event{Done: true}), nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string       `json:"content"`
					ToolCalls []oaToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(e.Data), &chunk); err != nil {
			return nil, fmt.Errorf("the provider sent a frame that is not JSON: %w", err)
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("the provider answered with an error: %s", chunk.Error.Message)
		}
		var out []assistant.Event
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				out = append(out, assistant.Event{Delta: ch.Delta.Content})
			}
			for _, tc := range ch.Delta.ToolCalls {
				idx := len(calls)
				if tc.Index != nil {
					idx = *tc.Index
				}
				for len(calls) <= idx {
					calls = append(calls, &partial{})
				}
				c := calls[idx]
				if tc.ID != "" {
					c.id = tc.ID
				}
				if tc.Function.Name != "" {
					c.name = tc.Function.Name
				}
				c.args.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				out = append(out, flush()...)
			}
		}
		if chunk.Usage != nil {
			out = append(out, assistant.Event{Usage: &assistant.Usage{InputTokens: chunk.Usage.Prompt, OutputTokens: chunk.Usage.Completion, Reported: true}})
		}
		return out, nil
	}
}

// Check implements assistant.Provider: the model list says the address and
// the key are right, and one token of completion says the model answers. A
// gateway that serves completions and no model list answers the list with a
// 404, which says nothing about the address or the key, so that one status
// is left for the completion to judge.
func (p *OpenAICompatible) Check(ctx context.Context) (assistant.CheckResult, error) {
	start := time.Now()
	resp, err := p.do(ctx, http.MethodGet, "/models", nil)
	var he *HTTPError
	if err != nil && !(errors.As(err, &he) && he.Status == http.StatusNotFound) {
		return assistant.CheckResult{}, err
	}
	if resp != nil {
		resp.Body.Close()
	}
	reported, err := completeOneToken(ctx, func(ctx context.Context) (assistant.Stream, error) {
		return p.chat(ctx, assistant.Request{Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "Say OK."}}}, 1)
	})
	if err != nil {
		return assistant.CheckResult{}, err
	}
	return assistant.CheckResult{Model: p.cfg.Model, Latency: time.Since(start), UsageReported: reported}, nil
}

// Models implements assistant.ModelLister with the same request Check makes.
func (p *OpenAICompatible) Models(ctx context.Context) ([]string, error) {
	resp, err := p.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	return modelsFrom(resp)
}

// completeOneToken runs a chat to its end and reports whether usage came
// back, which is what the settings page tells the administrator.
func completeOneToken(ctx context.Context, open func(context.Context) (assistant.Stream, error)) (bool, error) {
	s, err := open(ctx)
	if err != nil {
		return false, err
	}
	defer s.Close()
	reported := false
	for {
		e, ok := s.Next(ctx)
		if !ok {
			return reported, nil
		}
		if e.Err != nil {
			return false, e.Err
		}
		if e.Usage != nil && e.Usage.Reported {
			reported = true
		}
		if e.Done {
			return reported, nil
		}
	}
}
