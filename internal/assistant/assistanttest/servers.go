// Package assistanttest holds fake model servers for tests: one speaking the
// OpenAI chat-completions protocol, one speaking the Anthropic Messages
// protocol. Each records what it was asked so a test can assert the path,
// the headers and the body, and each answers the contract's prompts so the
// adapters' contract runs are about plumbing and not a model's mood.
package assistanttest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Recorded is one request the server saw.
type Recorded struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

// Server is a fake model server. Status, when set, is what every request
// answers with, and Body its body; NoUsage leaves usage out of the stream.
type Server struct {
	*httptest.Server
	Model   string
	Status  int
	Body    string
	NoUsage bool

	mu       sync.Mutex
	requests []Recorded
}

// Requests returns what the server has seen so far.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.requests...)
}

func (s *Server) record(r *http.Request) Recorded {
	rec := Recorded{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rec.Body)
	}
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	s.mu.Unlock()
	return rec
}

func (s *Server) refuse(w http.ResponseWriter) bool {
	if s.Status == 0 {
		return false
	}
	w.WriteHeader(s.Status)
	fmt.Fprint(w, s.Body)
	return true
}

// lastUser finds the last user message's text in either protocol's body.
func lastUser(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			for _, b := range c {
				bm, _ := b.(map[string]any)
				if bm["type"] == "text" {
					return bm["text"].(string)
				}
			}
		}
	}
	return ""
}

func wantsTool(body map[string]any) bool {
	tools, _ := body["tools"].([]any)
	return len(tools) > 0 && strings.HasPrefix(lastUser(body), "Call the echo tool")
}

func sse(w http.ResponseWriter, event string, data any) {
	raw, _ := json.Marshal(data)
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// NewOpenAI starts a server speaking the OpenAI chat-completions protocol
// under /v1, and closes it when the test ends.
func NewOpenAI(t testing.TB) *Server {
	t.Helper()
	s := &Server{Model: "fake-gpt"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if s.refuse(w) {
			return
		}
		fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model"}]}`, s.Model)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		rec := s.record(r)
		if s.refuse(w) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any) map[string]any {
			return map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": s.Model,
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		}
		if wantsTool(rec.Body) {
			sse(w, "", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "echo", "arguments": `{"text":`}}}}, nil))
			sse(w, "", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0,
				"function": map[string]any{"arguments": `"ping"}`}}}}, nil))
			sse(w, "", chunk(map[string]any{}, "tool_calls"))
		} else {
			for _, word := range []string{"Hello", " from", " the", " fake"} {
				sse(w, "", chunk(map[string]any{"content": word}, nil))
			}
			sse(w, "", chunk(map[string]any{}, "stop"))
		}
		if !s.NoUsage {
			sse(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": s.Model, "choices": []any{},
				"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 4}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		http.Error(w, `{"error":{"message":"no such route"}}`, http.StatusNotFound)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// NewAnthropic starts a server speaking the Anthropic Messages protocol, and
// closes it when the test ends.
func NewAnthropic(t testing.TB) *Server {
	t.Helper()
	s := &Server{Model: "fake-claude"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		rec := s.record(r)
		if s.refuse(w) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		usage := map[string]any{"input_tokens": 7}
		if s.NoUsage {
			usage = nil
		}
		sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1", "type": "message",
			"role": "assistant", "model": s.Model, "content": []any{}, "usage": usage}})
		if wantsTool(rec.Body) {
			sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
				"content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "echo", "input": map[string]any{}}})
			sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"text":`}})
			sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": `"ping"}`}})
			sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		} else {
			sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
				"content_block": map[string]any{"type": "text", "text": ""}})
			for _, word := range []string{"Hello", " from", " the", " fake"} {
				sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
					"delta": map[string]any{"type": "text_delta", "text": word}})
			}
			sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		}
		delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}
		if !s.NoUsage {
			delta["usage"] = map[string]any{"output_tokens": 4}
		}
		sse(w, "message_delta", delta)
		sse(w, "message_stop", map[string]any{"type": "message_stop"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		http.Error(w, `{"type":"error","error":{"type":"not_found_error","message":"no such route"}}`, http.StatusNotFound)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}
