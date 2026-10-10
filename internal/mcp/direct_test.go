package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Something that is not an MCP client can list and call the same tools. A tool
// that changes the fleet is in neither the list nor reachable unless the server
// was given leave to offer it, and the refusal is an answer a model can read.
func TestToolsCanBeListedAndCalledWithoutJSONRPC(t *testing.T) {
	s := New(fakeAPI{}, Options{})
	var names []string
	for _, d := range s.Definitions() {
		names = append(names, d.Name)
		if d.Description == "" || d.InputSchema["type"] != "object" {
			t.Errorf("%s has no description or schema: %+v", d.Name, d)
		}
	}
	if !slices.Contains(names, "fleet_status") || slices.Contains(names, "drain_runner") || slices.Contains(names, "rerun_job") {
		t.Errorf("definitions = %v", names)
	}

	text, failed, err := s.CallTool(context.Background(), "fleet_status", nil)
	if err != nil || failed || !slices.Equal(text, []string{`{"queued":3}`}) {
		t.Errorf("a read tool: %q, %v, %v", text, failed, err)
	}
	text, failed, err = s.CallTool(context.Background(), "drain_runner", []byte(`{"runner_id":"r"}`))
	if err != nil || !failed || len(text) != 1 || !strings.Contains(text[0], "drain_runner") {
		t.Errorf("a tool that is not offered: %q, %v, %v", text, failed, err)
	}
	if _, _, err = s.CallTool(context.Background(), "no_such_tool", nil); err == nil {
		t.Error("a name no tool has was called")
	}

	// With leave to offer it, the same tool is listed.
	all := New(fakeAPI{}, Options{Offer: func(string) bool { return true }})
	var offered []string
	for _, d := range all.Definitions() {
		offered = append(offered, d.Name)
	}
	if !slices.Contains(offered, "drain_runner") {
		t.Errorf("an offered action tool was not listed: %v", offered)
	}
}

// A tool may answer in several blocks of text; a caller that is not an MCP
// client is handed them as the blocks they are, in order, because a block is
// how a tool sets a stranger's words apart from its own.
func TestSeveralBlocksOfAnAnswerStayApartAndInOrder(t *testing.T) {
	s := New(fakeAPI{}, Options{})
	s.byName["two_blocks"] = &tool{Name: "two_blocks", call: func(context.Context, API, json.RawMessage) ([]Content, error) {
		return []Content{{Type: "text", Text: "first"}, {Type: "text", Text: "second"}}, nil
	}}
	text, failed, err := s.CallTool(context.Background(), "two_blocks", nil)
	if err != nil || failed || !slices.Equal(text, []string{"first", "second"}) {
		t.Errorf("text = %q, failed %v, err %v", text, failed, err)
	}
}
