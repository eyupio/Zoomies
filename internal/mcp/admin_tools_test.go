package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A host is edited in the fields named, its labels are replaced whole as the
// description says, and cordoning goes to its own route after the edit.
func TestEditHostSendsTheNamedFieldsThenCordons(t *testing.T) {
	r := &recorder{object: `{"name":"old","labels":{"a":"1"},"reserve_disk_mb":10,"cordoned":false}`}
	out, err := call(t, "edit_host", r, `{"host_id":"host_1","name":"new","labels":{"size":"large"},"reserve_disk_mb":2048,"cordoned":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.paths) != 2 || r.paths[0] != "PATCH /hosts/host_1" || r.paths[1] != "POST /hosts/host_1/cordon" {
		t.Fatalf("requests = %v", r.paths)
	}
	var sent struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
		Disk   int64             `json:"reserve_disk_mb"`
	}
	if err := json.Unmarshal([]byte(r.sent[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Name != "new" || sent.Labels["size"] != "large" || len(sent.Labels) != 1 || sent.Disk != 2048 {
		t.Errorf("patch body = %s", r.sent[0])
	}
	if strings.Contains(r.sent[0], "confirm") || !strings.Contains(r.sent[1], `"cordoned":true`) {
		t.Errorf("bodies = %v", r.sent)
	}
	if !strings.Contains(out, `"cordoned"`) {
		t.Errorf("the reply must report the cordon: %s", out)
	}
}

func TestEditHostRefusesNothingToDoAndAnEmptyName(t *testing.T) {
	r := &recorder{object: `{}`}
	for _, tc := range []struct{ args, want string }{
		{`{"host_id":"host_1"}`, "at least one setting"},
		{`{"host_id":"host_1","name":"  "}`, "cannot be empty"},
	} {
		if _, err := call(t, "edit_host", r, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.args, err, tc.want)
		}
	}
	if len(r.sent) != 0 {
		t.Errorf("a refused call sent %v", r.sent)
	}
}

func TestClearHostThrottlePostsToTheRoute(t *testing.T) {
	r := &recorder{object: `{}`}
	if _, err := call(t, "clear_host_throttle", r, `{"host_id":"host_1"}`); err != nil {
		t.Fatal(err)
	}
	if len(r.paths) != 1 || r.paths[0] != "POST /hosts/host_1/throttle/clear" {
		t.Errorf("requests = %v", r.paths)
	}
}

// Settings that decide who gets in, what the controller trusts and where it
// keeps its state are a person's to change, so the tool refuses them before
// anything is sent, and names every one it refused.
func TestUpdateSettingsOnlyTouchesTuningKeys(t *testing.T) {
	r := &recorder{object: `{}`}
	_, err := call(t, "update_settings", r, `{"changes":{"retention.jobs":"720h","security.disable_auth":true,"oidc.issuer":"x"}}`)
	if err == nil || !strings.Contains(err.Error(), "oidc.issuer, security.disable_auth") {
		t.Fatalf("error = %v, want both refused keys named", err)
	}
	if len(r.sent) != 0 {
		t.Errorf("a refused call sent %v", r.sent)
	}
	if _, err := call(t, "update_settings", r, `{"changes":{"retention.jobs":"720h","scheduler.size_routing":null}}`); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(r.sent[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["retention.jobs"] != "720h" || len(sent) != 2 {
		t.Errorf("body = %s; want the changes as given, null included", r.sent[0])
	}
	if _, ok := sent["scheduler.size_routing"]; !ok {
		t.Errorf("a null must be sent, because it clears the stored setting: %s", r.sent[0])
	}
}

func TestGetSettingsFiltersByPrefix(t *testing.T) {
	r := &recorder{object: `{"settings":[{"key":"retention.jobs","value":"720h"},{"key":"security.session_ttl","value":"168h"}],"pending_restart":["server.port"]}`}
	out, err := call(t, "get_settings", r, `{"prefix":"retention"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "retention.jobs") || strings.Contains(out, "session_ttl") || !strings.Contains(out, "server.port") {
		t.Errorf("out = %s", out)
	}
}

func TestAdministratorToolsAreActionsAndNamed(t *testing.T) {
	got := map[string]bool{}
	for _, tl := range tools() {
		got[tl.Name] = tl.action
	}
	for name := range adminTools {
		if !got[name] {
			t.Errorf("%s must be an action tool so a transport has to offer it", name)
		}
	}
}

// The switch that lets the controller edit its own fleet is not one an agent may
// turn on, whatever role its token has: it is a security setting, and the tuning
// allowlist does not reach any.
func TestAnAgentCannotTurnOnAutomaticApplyOverMCP(t *testing.T) {
	r := &recorder{object: `{}`}
	for _, key := range []string{"security.auto_apply_remedies", "security.mcp_admin_tools"} {
		_, err := call(t, "update_settings", r, `{"changes":{"`+key+`":true}}`)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: error = %v, want a refusal naming it", key, err)
		}
	}
	if len(r.sent) != 0 {
		t.Errorf("a refused call sent %v", r.sent)
	}
}

// An assistant is steered by text a workflow wrote, so it must not be able to
// switch on a controller that replaces its own binary, or shorten the wait that
// stands between a bad release and every host. The check interval stays
// tunable: it only changes how often a question is put to github.com.
func TestUpdateSettingsCannotWriteTheUpdatesMode(t *testing.T) {
	if tunable("updates.mode") || tunable("updates.soak") {
		t.Error("the update mode or its soak is on the tuning allowlist")
	}
	if !tunable("updates.check_interval") {
		t.Error("updates.check_interval was taken off the tuning allowlist with the rest of the section")
	}

	r := &recorder{object: `{}`}
	for key, value := range map[string]string{"updates.mode": `"auto"`, "updates.soak": `"0"`} {
		_, err := call(t, "update_settings", r, `{"changes":{"`+key+`":`+value+`}}`)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: error = %v, want a refusal naming it", key, err)
		}
	}
	if len(r.sent) != 0 {
		t.Errorf("a refused call sent %v", r.sent)
	}
	if _, err := call(t, "update_settings", r, `{"changes":{"updates.check_interval":"12h"}}`); err != nil {
		t.Errorf("the check interval was refused: %v", err)
	}
}

// An entry that does not end in a dot names one key. Read as a prefix it would
// also admit updates.check_interval_x the day the section grows one, and the
// allowlist exists so that a new key is not tunable until somebody names it.
func TestAnAllowlistEntryWithoutATrailingDotNamesOneKey(t *testing.T) {
	for key, want := range map[string]bool{
		"updates.check_interval":   true,
		"updates.check_interval_x": false,
		"updates.check_interval.x": false,
		"scheduler.anything":       true,
	} {
		if got := tunable(key); got != want {
			t.Errorf("tunable(%q) = %v, want %v", key, got, want)
		}
	}

	_, err := call(t, "update_settings", &recorder{object: `{}`}, `{"changes":{"updates.check_interval_x":"1h"}}`)
	if err == nil || !strings.Contains(err.Error(), "updates.check_interval_x cannot be changed over MCP") {
		t.Fatalf("error = %v, want a refusal naming the key", err)
	}
	if !strings.Contains(err.Error(), "only these can") || strings.Contains(err.Error(), "only keys under") {
		t.Errorf("the refusal reads as though every entry were a prefix: %v", err)
	}
}
