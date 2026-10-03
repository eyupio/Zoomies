package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
)

type contextAPI struct {
	path  string
	query url.Values
	body  []byte
	calls int
}

func (a *contextAPI) Call(_ context.Context, method, path string, q url.Values) ([]byte, error) {
	a.path = method + " " + path
	a.query = q
	a.calls++
	return a.body, nil
}
func (*contextAPI) Stream(context.Context, string, string) (io.ReadCloser, error) { return nil, io.EOF }

func TestContextToolsUseOnlyRESTWithCompactArgumentsAndBoundTheMCPEnvelope(t *testing.T) {
	for _, op := range []string{"overview", "read", "search", "pack"} {
		a := &contextAPI{body: []byte(`{"commit":"abc","excerpts":[{"text":"source"}]}`)}
		args := map[string]any{"repository_id": "ctx_demo"}
		if op == "read" {
			args["path"] = "src/a.go"
		}
		if op == "search" {
			args["query"] = "literal.*"
		}
		if op == "pack" {
			args["paths"] = []string{"src/a.go", "src/b.go"}
		}
		raw, _ := json.Marshal(args)
		content, err := callContext(t.Context(), a, op, raw)
		if err != nil || a.calls != 1 || a.path != "GET /ai-context/source/ctx_demo/"+op || len(content) != 1 {
			t.Fatal(op, a, err)
		}
		if op == "pack" && len(a.query["path"]) != 2 {
			t.Fatal("pack lost file selection")
		}
	}
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("\\", 11000)})
	a := &contextAPI{body: body}
	if _, err := callContext(t.Context(), a, "read", json.RawMessage(`{"repository_id":"ctx_demo","path":"a","budget":24000}`)); err == nil || !strings.Contains(err.Error(), "MCP response") {
		t.Fatal("oversized encoded MCP result accepted", err)
	}
}

func TestContextToolsRefuseUnsupportedArgumentsAndUnpinnedContinuationBeforeCallingREST(t *testing.T) {
	for _, tc := range []struct{ op, args string }{{"read", `{"repository_id":"ctx_demo","path":"a","offset":1}`}, {"pack", `{"repository_id":"ctx_demo","paths":[]}`}, {"search", `{"repository_id":"ctx_demo","paths":["a"],"query":"x"}`}, {"read", `{"path":"a"}`}, {"overview", `{"limit":100}`}, {"read", `{"repository_id":"ctx_demo","path":"a","budget":24001}`}} {
		a := &contextAPI{}
		if _, err := callContext(t.Context(), a, tc.op, json.RawMessage(tc.args)); err == nil || a.calls != 0 {
			t.Fatal("invalid request contacted REST", tc)
		}
	}
	a := &contextAPI{body: []byte(`{"items":[]}`)}
	if _, err := callContext(t.Context(), a, "overview", json.RawMessage(`{}`)); err != nil || a.path != "GET /ai-context/access" || a.query.Get("limit") != "12" {
		t.Fatal("bounded discovery", a, err)
	}
}
