package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
)

// The limits are inclusive: a conversation exactly at one is sent, and one byte
// past it is not. A limit that is off by one turns away the person who typed
// exactly what the page told them they could.
func TestAConversationExactlyAtTheLimitsIsSent(t *testing.T) {
	turns := make([]AssistantChatMessage, 0, assistantChatMaxMessages)
	for len(turns) < assistantChatMaxMessages-1 {
		turns = append(turns, AssistantChatMessage{Role: "user", Content: "q"}, AssistantChatMessage{Role: "assistant", Content: "a"})
	}
	turns = turns[:assistantChatMaxMessages-1]
	turns = append(turns, AssistantChatMessage{Role: "user", Content: "q"})
	if _, err := ValidateAssistantChat(turns); err != nil {
		t.Errorf("%d messages: %v", len(turns), err)
	}

	one := []AssistantChatMessage{{Role: "user", Content: strings.Repeat("x", assistantChatMaxMessageBytes)}}
	if _, err := ValidateAssistantChat(one); err != nil {
		t.Errorf("a message of exactly %d bytes: %v", assistantChatMaxMessageBytes, err)
	}

	// Four messages of the largest size are under the total, and a fifth, however
	// short, takes it over only if the total is what it says.
	var big []AssistantChatMessage
	for len(big)*assistantChatMaxMessageBytes < assistantChatMaxTotalBytes {
		big = append(big, AssistantChatMessage{Role: "user", Content: strings.Repeat("y", assistantChatMaxMessageBytes)})
	}
	if _, err := ValidateAssistantChat(big); err != nil {
		t.Errorf("a conversation of exactly %d bytes: %v", assistantChatMaxTotalBytes, err)
	}
	if _, err := ValidateAssistantChat(append(big, AssistantChatMessage{Role: "user", Content: "z"})); err == nil {
		t.Error("a conversation one byte over the total was sent")
	}
}

// What is sent is what was typed, trimmed, in the roles the page may use.
func TestAConversationIsSentTrimmedAndInTheRolesItWasGiven(t *testing.T) {
	got, err := ValidateAssistantChat([]AssistantChatMessage{
		{Role: "user", Content: "  hello \n"}, {Role: "assistant", Content: "hi"}, {Role: "user", Content: "why?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []assistant.Message{
		{Role: assistant.RoleUser, Content: "hello"}, {Role: assistant.RoleAssistant, Content: "hi"}, {Role: assistant.RoleUser, Content: "why?"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := ValidateAssistantChat([]AssistantChatMessage{{Role: "user", Content: "bad \xff text"}}); err == nil {
		t.Error("text that is not UTF-8 was sent")
	}
}

type scriptedStream struct{ events []assistant.Event }

func (s *scriptedStream) Next(context.Context) (assistant.Event, bool) {
	if len(s.events) == 0 {
		return assistant.Event{}, false
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, true
}
func (s *scriptedStream) Close() error { return nil }

// No tool is offered, so a model that calls one has invented it. The call is
// dropped and the words around it are kept.
func TestAToolCallNoToolWasOfferedForIsDropped(t *testing.T) {
	chat := &AssistantChat{stream: &scriptedStream{events: []assistant.Event{
		{Delta: "a"}, {ToolCall: &assistant.ToolCall{ID: "1", Name: "echo"}}, {Delta: "b"}, {Done: true},
	}}}
	var got []string
	for {
		ev, ok := chat.Next(context.Background())
		if !ok {
			break
		}
		if ev.ToolCall != nil {
			t.Fatal("a tool call reached the caller")
		}
		got = append(got, ev.Delta)
	}
	if strings.Join(got, "") != "ab" {
		t.Errorf("deltas = %q", got)
	}
}
