package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// recorder answers GETs with a canned object and keeps every request body, so a
// test can see exactly what a tool would have sent without a controller.
type recorder struct {
	object string
	sent   []string
}

func (r *recorder) Call(_ context.Context, _, _ string, _ url.Values) ([]byte, error) {
	return []byte(r.object), nil
}
func (r *recorder) CallBody(_ context.Context, method, path string, _ url.Values, body []byte) ([]byte, error) {
	if method != http.MethodPatch && !(method == http.MethodPost && path == "/problems/apply") {
		return nil, refusal{http.StatusMethodNotAllowed}
	}
	r.sent = append(r.sent, string(body))
	return []byte(`{}`), nil
}
func (*recorder) Stream(context.Context, string, string) (io.ReadCloser, error) { return nil, nil }

func call(t *testing.T, name string, c API, args string) (string, error) {
	t.Helper()
	for _, tl := range tools() {
		if tl.Name != name {
			continue
		}
		out, err := tl.call(context.Background(), c, json.RawMessage(args))
		if err != nil {
			return "", err
		}
		return out[0].Text, nil
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

// A pool is changed in the settings the call names and nowhere else, and a setting
// the tool has never heard of -- one the API gains next year -- is carried through,
// because the merge is into what the API returned and not into a copy of its types.
func TestUpdatePoolMergesIntoWhatTheAPIReturned(t *testing.T) {
	r := &recorder{object: `{"name":"p","resources":{"min_cpus":1,"future_field":7,"daemon_cpu_share_percent":35},"cpu_burst":{"mode":"automatic","size_for_ceiling":true}}`}
	if _, err := call(t, "update_pool", r, `{"pool_id":"pool_1","daemon_cpu_share_percent":20,"cpu_burst_max_cpus":6}`); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Resources map[string]any `json:"resources"`
		CPUBurst  map[string]any `json:"cpu_burst"`
	}
	if err := json.Unmarshal([]byte(r.sent[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Resources["future_field"] != 7.0 || sent.Resources["min_cpus"] != 1.0 || sent.Resources["daemon_cpu_share_percent"] != 20.0 {
		t.Errorf("the resources must be the pool's with only the share changed: %v", sent.Resources)
	}
	if sent.CPUBurst["mode"] != "automatic" || sent.CPUBurst["max_cpus"] != 6.0 || sent.CPUBurst["size_for_ceiling"] != true {
		t.Errorf("the burst policy must keep its mode: %v", sent.CPUBurst)
	}
	if strings.Contains(r.sent[0], "confirm") {
		t.Errorf("the tool must never override the controller's refusal: %s", r.sent[0])
	}
}

func TestSizingToolsRefuseWhatTheControllerWouldOrAPersonShouldDecide(t *testing.T) {
	r := &recorder{object: `{}`}
	for _, tc := range []struct{ tool, args, want string }{
		{"update_pool", `{"pool_id":"pool_1"}`, "at least one setting"},
		{"update_pool", `{"pool_id":"pool_1","min_cpus":0.1}`, "follow the fleet's"},
		{"update_pool", `{"pool_id":"pool_1","min_memory_mb":100}`, "at least 512"},
		{"update_host", `{"host_id":"host_1","capacity":0}`, "cordons it"},
		{"update_host", `{"host_id":"host_1"}`, "at least one setting"},
		{"update_host", `{"host_id":"host_1","nonsense":1}`, "input schema"},
	} {
		if _, err := call(t, tc.tool, r, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: error = %v, want it to say %q", tc.tool, tc.args, err, tc.want)
		}
	}
	if len(r.sent) != 0 {
		t.Errorf("a refused call sent %v", r.sent)
	}
}

// Both are action tools: offered only where the transport says so.
func TestSizingToolsAreActions(t *testing.T) {
	got := map[string]bool{}
	for _, tl := range tools() {
		got[tl.Name] = tl.action
	}
	if !got["update_pool"] || !got["update_host"] {
		t.Errorf("update_pool and update_host change the fleet and must be action tools: %v", got)
	}
}

// An agent names the problem and the proposal it read, never the change: the
// controller applies what it proposes now, as the caller.
func TestApplyRemedySendsTheProblemAndNeverTheChange(t *testing.T) {
	r := &recorder{object: `{}`}
	if _, err := call(t, "apply_remedy", r, `{"code":"host.slots_below_capacity","target_id":"host_1","remedy_id":"rem_aaa"}`); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatalf("sent %v", r.sent)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(r.sent[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got["code"] != "host.slots_below_capacity" || got["target_id"] != "host_1" || got["remedy_id"] != "rem_aaa" || len(got) != 3 {
		t.Errorf("sent %v; want the problem, its target and the proposal's id and nothing else", got)
	}
	if _, err := call(t, "apply_remedy", r, `{"code":"host.slots_below_capacity","target_id":"host_1","body":{}}`); err == nil {
		t.Error("a call that tries to send its own change must be refused by the schema")
	}
	if _, err := call(t, "apply_remedy", r, `{"target_id":"host_1"}`); err == nil {
		t.Error("a call with no problem code must be refused")
	}
}
