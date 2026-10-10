package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// subscriptionTool is one of the tools besides Claude Code that a person's own
// subscription is used through, with a stand-in for it.
type subscriptionTool struct {
	kind, name, bin string
	// script is the stand-in: signed in or not, and answering with "Hello from <tool>".
	script func(signedIn bool) string
}

var subscriptionTools = []subscriptionTool{
	{"codex", "Codex", "codex", func(signedIn bool) string {
		status := `echo "Logged in using ChatGPT"; exit 0`
		if !signedIn {
			status = `echo "Not logged in"; exit 1`
		}
		return `#!/bin/sh
case "$1" in
  --version) echo "codex-cli 0.99.0"; exit 0 ;;
  login) ` + status + ` ;;
esac
cat > /dev/null
echo '{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"Hello from Codex"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":9,"output_tokens":4}}'
`
	}},
	{"copilot", "GitHub Copilot", "copilot", func(signedIn bool) string {
		ask := `printf 'Hello from Copilot'`
		if !signedIn {
			ask = `echo "not logged in" >&2; exit 1`
		}
		return `#!/bin/sh
case "$1" in
  version) echo "GitHub Copilot CLI 1.2.3"; exit 0 ;;
esac
` + ask + "\n"
	}},
}

func (tl subscriptionTool) install(t *testing.T, signedIn bool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tl.bin), []byte(tl.script(signedIn)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (tl subscriptionTool) body(name string) map[string]any {
	return map[string]any{"name": name, "kind": tl.kind}
}

// Codex and Copilot are somebody's own subscription like Claude Code: nothing to
// type but a name, no address, no key and no fleet, and the model is left to the
// tool when none is named, which a model API's provider cannot do.
func TestCodexAndCopilotAreSubscriptionsWithNoAddressNoKeyAndNoFleetAccess(t *testing.T) {
	for _, tl := range subscriptionTools {
		t.Run(tl.kind, func(t *testing.T) {
			tl.install(t, true)
			h := newHarness(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
			_, alice := h.user("alice", store.RoleAdmin)
			for field, extra := range map[string]map[string]any{
				"base_url":     {"base_url": "https://example.test"},
				"api_key":      {"api_key": "sk-a-key-it-must-not-hold"},
				"fleet_access": {"fleet_access": true},
			} {
				b := tl.body(tl.name)
				for k, v := range extra {
					b[k] = v
				}
				resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: b})
				resp.mustStatus(t, http.StatusUnprocessableEntity, tl.kind+" with a "+field)
				if !strings.Contains(string(resp.body), `"`+field+`"`) || strings.Contains(string(resp.body), "a-key-it-must-not-hold") {
					t.Errorf("the refusal for %s: %s", field, resp.body)
				}
			}
			resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: tl.body(tl.name)})
			resp.mustStatus(t, http.StatusCreated, "adding it with no model")
			v := resp.json(t)
			if v["subscription"] != true || v["owned_by_you"] != true || v["usable"] != true || v["model"] != "" {
				t.Errorf("view = %v", v)
			}
			// A provider that is a model API still has to name its model.
			srvBody := map[string]any{"name": "Local", "kind": "openai_compatible", "base_url": "http://localhost:11434/v1"}
			resp = h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: srvBody})
			resp.mustStatus(t, http.StatusUnprocessableEntity, "a model API with no model")
			if !strings.Contains(string(resp.body), `"model"`) {
				t.Errorf("the refusal does not name the model: %s", resp.body)
			}
		})
	}
}

// Each is its owner's alone, as Claude Code is.
func TestCodexAndCopilotBelongToWhoAddedThem(t *testing.T) {
	for _, tl := range subscriptionTools {
		t.Run(tl.kind, func(t *testing.T) {
			tl.install(t, true)
			h := newHarness(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
			_, alice := h.user("alice", store.RoleAdmin)
			_, bob := h.user("bob", store.RoleAdmin)
			resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: tl.body("Alice's " + tl.name)})
			resp.mustStatus(t, http.StatusCreated, "adding it")
			id := resp.json(t)["id"].(string)
			path := assistantProviders + "/" + id
			for name, req := range map[string]request{
				"test it":         {method: http.MethodPost, path: path + "/check", body: map[string]any{}},
				"make it default": {method: http.MethodPost, path: path + "/default", body: map[string]any{}},
				"chat through it": {method: http.MethodPost, path: assistantChat, body: map[string]any{"provider_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}}}},
			} {
				req.cookie = bob
				r := h.do(req)
				r.mustStatus(t, http.StatusForbidden, "bob trying to "+name)
				if !strings.Contains(string(r.body), "assistant.provider_not_yours") {
					t.Errorf("bob trying to %s: %s", name, r.body)
				}
			}
			h.do(request{method: http.MethodPost, path: path + "/check", cookie: alice, body: map[string]any{}}).
				mustStatus(t, http.StatusOK, "alice testing it")
		})
	}
}

// Eli answers through the owner's tool and says so in the audit log, with the tool's
// own default model named as such, and the fleet is not offered.
func TestEliAnswersThroughTheOwnersCodexOrCopilot(t *testing.T) {
	for _, tl := range subscriptionTools {
		t.Run(tl.kind, func(t *testing.T) {
			tl.install(t, true)
			h := newHarness(t)
			_, alice := h.user("alice", store.RoleAdmin)
			resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: tl.body("Alice's " + tl.name)})
			resp.mustStatus(t, http.StatusCreated, "adding it")
			id := resp.json(t)["id"].(string)
			h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: alice, body: map[string]any{}}).
				mustStatus(t, http.StatusOK, "making it the default")
			resp = h.do(request{method: http.MethodPost, path: assistantChat, cookie: alice, readStream: true, body: chatBody("user", "What is a runner?")})
			resp.mustStatus(t, http.StatusOK, "asking")
			var text string
			var done map[string]any
			for _, f := range frames(t, resp.body) {
				switch f.kind {
				case "delta":
					text += f.data["text"].(string)
				case "done":
					done = f.data
				case "error":
					t.Fatalf("the answer failed: %v", f.data)
				}
			}
			if text != "Hello from "+strings.TrimPrefix(tl.name, "GitHub ") || done["provider"] != "Alice's "+tl.name ||
				done["model"] != "its default model" || done["fleet_access"] != false {
				t.Errorf("text = %q, done = %v", text, done)
			}
			rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"assistant.chat"}}, store.Page{Limit: 5})
			if len(rows) != 1 || !strings.Contains(rows[0].After, `"fleet_access":false`) {
				t.Errorf("audit rows = %+v", rows)
			}
		})
	}
}

// Testing says who is not signed in, and Codex signed in with an API key is turned
// away, because that is not the plan; Local models only refuses both.
func TestTestingCodexAndCopilotSaysWhatIsWrong(t *testing.T) {
	for _, tl := range subscriptionTools {
		t.Run(tl.kind+" not signed in", func(t *testing.T) {
			tl.install(t, false)
			h := newHarness(t)
			_, alice := h.user("alice", store.RoleAdmin)
			resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: tl.body(tl.name)})
			id := resp.json(t)["id"].(string)
			resp = h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: alice, body: map[string]any{}})
			resp.mustStatus(t, http.StatusOK, "testing it")
			if v := resp.json(t); v["ok"] != false || !strings.Contains(v["error"].(string), "nobody is signed in") {
				t.Errorf("check = %v", v)
			}
		})
		t.Run(tl.kind+" local only", func(t *testing.T) {
			tl.install(t, true)
			h := newHarness(t, func(c *config.Config) { c.Assistant.LocalOnly = true })
			_, alice := h.user("alice", store.RoleAdmin)
			resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: tl.body(tl.name)})
			id := resp.json(t)["id"].(string)
			resp = h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: alice, body: map[string]any{}})
			resp.mustStatus(t, http.StatusOK, "testing it")
			if v := resp.json(t); v["ok"] != false || !strings.Contains(v["error"].(string), "Local models only") {
				t.Errorf("check = %v", v)
			}
		})
	}
}
