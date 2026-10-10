package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/eyupio/zoomies/internal/controller"
)

// assistantChatInput is the body of a chat: the conversation so far, ending in
// the person's question, and optionally which provider to ask. Nothing is kept
// between requests; the page holds the history and sends it again.
type assistantChatInput struct {
	ProviderID string `json:"provider_id"`
	Messages   []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// handleAssistantChat answers a conversation as a stream of Server-Sent Events:
// `delta` frames carrying text, one `usage` frame when the provider reports it,
// and `done`, or `error` if the answer failed after it began.
//
// Anything that can be refused is refused before the stream opens, with a status
// and a code a client can act on, so a stream that opens has an answer coming.
// A failure after that is a frame and not a status, because the status went out
// with the first byte.
func (s *Server) handleAssistantChat(w http.ResponseWriter, r *http.Request) {
	var in assistantChatInput
	if !decode(w, r, &in) {
		return
	}
	req := controller.AssistantChatRequest{ProviderID: in.ProviderID, OwnerID: s.assistantOwner(r)}
	for _, m := range in.Messages {
		req.Messages = append(req.Messages, controller.AssistantChatMessage{Role: m.Role, Content: m.Content})
	}
	chat, err := s.ctrl.StartAssistantChat(r.Context(), req)
	var invalid *controller.AssistantChatInvalid
	switch {
	case errors.As(err, &invalid):
		unprocessable(w, invalid.Message, []fieldError{{invalid.Field, invalid.Message}})
		return
	case errors.Is(err, controller.ErrAssistantNoModel):
		conflict(w, "no assistant model is set up; add a provider, test it and make it the default under Settings, Assistant")
		return
	case err != nil:
		// The model's end failed, not this controller. The adapters write their
		// errors for a person and never carry the request or the key.
		writeError(w, http.StatusBadGateway, errorEnvelope{Error: errorBody{Code: codeAssistantProviderFailed, Message: err.Error()}})
		return
	}
	defer chat.Close()

	stream := startSSE(w, r)
	send := func(kind string, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		return stream.event(kind, "", b) == nil
	}
	for {
		ev, ok := chat.Next(r.Context())
		if !ok {
			return
		}
		switch {
		case ev.Err != nil:
			send("error", map[string]string{"message": ev.Err.Error()})
			return
		case ev.Usage != nil:
			if !send("usage", map[string]int{"input_tokens": ev.Usage.InputTokens, "output_tokens": ev.Usage.OutputTokens}) {
				return
			}
		case ev.Done:
			send("done", map[string]string{"provider": chat.Provider, "model": chat.Model})
			return
		case ev.Delta != "":
			if !send("delta", map[string]string{"text": ev.Delta}) {
				return
			}
		}
	}
}
