package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeAPI answers /stats and refuses everything else with a 404, which is all
// the transport needs to be exercised against.
type fakeAPI struct{}

type refusal struct{ status int }

func (r refusal) Error() string   { return http.StatusText(r.status) }
func (r refusal) HTTPStatus() int { return r.status }

func (fakeAPI) Call(_ context.Context, method, path string, _ url.Values) ([]byte, error) {
	if method == http.MethodGet && path == "/stats" {
		return []byte(`{ "queued": 3 }`), nil
	}
	return nil, refusal{http.StatusNotFound}
}

func (fakeAPI) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, refusal{http.StatusNotFound}
}

func post(t *testing.T, s *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestHTTPAnswersARequestWithOneJSONBody(t *testing.T) {
	s := New(fakeAPI{}, Options{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"fleet_status"}}`, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("a request must be answered 200 with JSON, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var reply struct {
		ID     int        `json:"id"`
		Result CallResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != 7 || reply.Result.IsError || reply.Result.Content[0].Text != `{"queued":3}` {
		t.Errorf("the reply must carry the request's ID and the route's answer, compacted: %s", rec.Body)
	}
}

// A client that sends something the transport cannot use is told so in a body
// it can read, and the status says which layer refused it.
func TestHTTPRefusesWhatIsNotOneMessage(t *testing.T) {
	s := New(fakeAPI{}, Options{})
	for name, tc := range map[string]struct {
		body   string
		status int
		code   int
	}{
		"garbage": {"not json", http.StatusBadRequest, rpcParseError},
		"batch":   {`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, http.StatusBadRequest, rpcInvalidRequest},
		"huge":    {`{"x":"` + strings.Repeat("a", MaxMessage) + `"}`, http.StatusRequestEntityTooLarge, rpcInvalidRequest},
	} {
		rec := post(t, s, tc.body, nil)
		var reply struct {
			Error *RPCError `json:"error"`
		}
		if rec.Code != tc.status || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.Error == nil || reply.Error.Code != tc.code {
			t.Errorf("%s: want %d with JSON-RPC error %d, got %d %s", name, tc.status, tc.code, rec.Code, rec.Body)
		}
	}
}

// An action tool not offered is answered with the transport's own sentence,
// as a tool result the model reads, rather than as "no such tool".
func TestAnActionNotOfferedIsExplainedNotHidden(t *testing.T) {
	s := New(fakeAPI{}, Options{Refusal: func(tool string) string { return tool + ": ask for the operator role" }})
	rec := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rerun_job","arguments":{"job_id":"job_1"}}}`, nil)
	var reply struct {
		Result CallResult `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	if !reply.Result.IsError || !strings.Contains(reply.Result.Content[0].Text, "operator role") {
		t.Errorf("the refusal must be the transport's sentence, got %s", rec.Body)
	}

	offered := New(fakeAPI{}, Options{Offer: func(tool string) bool { return tool == "rerun_job" }})
	if _, ok := offered.byName["rerun_job"]; !ok {
		t.Error("an action the transport offers must be listed")
	}
	if _, ok := offered.byName["drain_runner"]; ok {
		t.Error("an action the transport does not offer must not be listed")
	}
}
