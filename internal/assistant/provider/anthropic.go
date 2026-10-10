package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// anthropicVersion is the API version header every request carries.
const anthropicVersion = "2023-06-01"

// anthropicDefaultMaxTokens bounds an answer when the request did not: the
// Messages API requires the field.
const anthropicDefaultMaxTokens = 4096

// Anthropic speaks the Messages API over plain HTTP, as the Docker and
// Proxmox clients do their APIs: a few hundred lines rather than a module.
type Anthropic struct {
	cfg  Config
	base string
}

// NewAnthropic returns an adapter for the API at cfg.BaseURL.
//
// A gateway in front of the API usually publishes its address ending in /v1,
// and the Messages path begins with it, so a trailing /v1 is taken off once
// rather than doubled.
func NewAnthropic(cfg Config) *Anthropic {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	base = strings.TrimSuffix(base, "/v1")
	return &Anthropic{cfg: cfg, base: base}
}

func (p *Anthropic) body(req assistant.Request, maxTokens int) ([]byte, error) {
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case assistant.RoleSystem:
			// The API takes the system prompt as its own field; a system
			// message in the turns is folded into it.
			continue
		case assistant.RoleTool:
			msgs = append(msgs, map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content},
			}})
		case assistant.RoleAssistant:
			if len(m.ToolCalls) == 0 {
				msgs = append(msgs, map[string]any{"role": "assistant", "content": m.Content})
				continue
			}
			var blocks []any
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, c := range m.ToolCalls {
				input := c.Arguments
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": input})
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": blocks})
		default:
			msgs = append(msgs, map[string]any{"role": "user", "content": m.Content})
		}
	}
	out := map[string]any{"model": p.model(req.Model), "max_tokens": maxTokens, "messages": msgs, "stream": true}
	system := req.System
	for _, m := range req.Messages {
		if m.Role == assistant.RoleSystem {
			system = strings.TrimSpace(system + "\n\n" + m.Content)
		}
	}
	if system != "" {
		out["system"] = system
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		out["tools"] = tools
	}
	return json.Marshal(out)
}

func (p *Anthropic) model(override string) string {
	if override != "" {
		return override
	}
	return p.cfg.Model
}

func (p *Anthropic) chat(ctx context.Context, req assistant.Request, maxTokens int) (assistant.Stream, error) {
	body, err := p.body(req, maxTokens)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "text/event-stream")
	hr.Header.Set("x-api-key", p.cfg.APIKey)
	hr.Header.Set("anthropic-version", anthropicVersion)
	resp, err := p.cfg.client().Do(hr)
	if err != nil {
		return nil, fmt.Errorf("reaching the provider: %w", err)
	}
	if err := StatusError(resp, p.cfg.APIKey != ""); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return NewStream(resp, newAnthropicDecoder()), nil
}

// Chat implements assistant.Provider.
func (p *Anthropic) Chat(ctx context.Context, req assistant.Request) (assistant.Stream, error) {
	return p.chat(ctx, req, req.MaxTokens)
}

// newAnthropicDecoder decodes the Messages stream: text deltas as they come,
// a tool_use block gathered from its input_json_delta fragments and emitted
// at its content_block_stop, usage from message_start and message_delta
// together, and message_stop as Done.
func newAnthropicDecoder() Decoder {
	type block struct {
		id, name string
		args     strings.Builder
		tool     bool
	}
	blocks := map[int]*block{}
	usage := assistant.Usage{}
	return func(e ServerEvent) ([]assistant.Event, error) {
		var frame struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Message *struct {
				Usage *struct {
					Input int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage *struct {
				Output int `json:"output_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if e.Data == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(e.Data), &frame); err != nil {
			return nil, fmt.Errorf("the provider sent a frame that is not JSON: %w", err)
		}
		switch frame.Type {
		case "error":
			msg := "unknown error"
			if frame.Error != nil {
				msg = frame.Error.Message
			}
			return nil, fmt.Errorf("the provider answered with an error: %s", msg)
		case "message_start":
			if frame.Message != nil && frame.Message.Usage != nil {
				usage.InputTokens, usage.Reported = frame.Message.Usage.Input, true
			}
		case "content_block_start":
			if frame.ContentBlock != nil && frame.ContentBlock.Type == "tool_use" {
				blocks[frame.Index] = &block{id: frame.ContentBlock.ID, name: frame.ContentBlock.Name, tool: true}
			}
		case "content_block_delta":
			if frame.Delta == nil {
				return nil, nil
			}
			switch frame.Delta.Type {
			case "text_delta":
				return []assistant.Event{{Delta: frame.Delta.Text}}, nil
			case "input_json_delta":
				if b := blocks[frame.Index]; b != nil {
					b.args.WriteString(frame.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if b := blocks[frame.Index]; b != nil && b.tool {
				delete(blocks, frame.Index)
				args := b.args.String()
				if args == "" {
					args = "{}"
				}
				return []assistant.Event{{ToolCall: &assistant.ToolCall{ID: b.id, Name: b.name, Arguments: json.RawMessage(args)}}}, nil
			}
		case "message_delta":
			if frame.Usage != nil {
				usage.OutputTokens, usage.Reported = frame.Usage.Output, true
			}
			if usage.Reported {
				u := usage
				return []assistant.Event{{Usage: &u}}, nil
			}
		case "message_stop":
			return []assistant.Event{{Done: true}}, nil
		}
		return nil, nil
	}
}

// Models implements assistant.ModelLister. The list is the first hundred the API
// returns, which is more than a person picks from.
func (p *Anthropic) Models(ctx context.Context) ([]string, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/v1/models?limit=100", nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("x-api-key", p.cfg.APIKey)
	hr.Header.Set("anthropic-version", anthropicVersion)
	resp, err := p.cfg.client().Do(hr)
	if err != nil {
		return nil, fmt.Errorf("reaching the provider: %w", err)
	}
	if err := StatusError(resp, p.cfg.APIKey != ""); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return modelsFrom(resp)
}

// Check implements assistant.Provider with one request of one token: the
// Messages API has no model list worth a second round trip.
func (p *Anthropic) Check(ctx context.Context) (assistant.CheckResult, error) {
	start := time.Now()
	reported, err := completeOneToken(ctx, func(ctx context.Context) (assistant.Stream, error) {
		return p.chat(ctx, assistant.Request{Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "Say OK."}}}, 1)
	})
	if err != nil {
		return assistant.CheckResult{}, err
	}
	return assistant.CheckResult{Model: p.cfg.Model, Latency: time.Since(start), UsageReported: reported}, nil
}
