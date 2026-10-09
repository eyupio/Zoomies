package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// withClaudeCode puts a stand-in for Claude Code first on the PATH, signed in as
// the method says and answering with what it is told.
func withClaudeCode(t *testing.T, method string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  --version) echo "2.1.300 (Claude Code)"; exit 0 ;;
  auth) echo '{"loggedIn":true,"authMethod":"` + method + `"}'; exit 0 ;;
esac
cat > /dev/null
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello from"}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":" Claude"}}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"Hello from Claude","usage":{"input_tokens":9,"output_tokens":4}}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func claudeBody(name string) map[string]any {
	return map[string]any{"name": name, "kind": "claude_code", "model": "sonnet"}
}

// claudeHarness is two administrators and a Claude subscription that is Alice's.
func claudeHarness(t *testing.T) (h *harness, alice, bob, id string) {
	t.Helper()
	withClaudeCode(t, "claude.ai")
	h = newHarness(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	_, alice = h.user("alice", store.RoleAdmin)
	_, bob = h.user("bob", store.RoleAdmin)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: claudeBody("Alice's Claude")})
	resp.mustStatus(t, http.StatusCreated, "adding a Claude subscription")
	return h, alice, bob, resp.json(t)["id"].(string)
}

// A Claude subscription has no address and no key for Zoomies to hold, and cannot
// be given the fleet: each is refused with the field to fix, and the sign-in is
// said to be made in Claude Code.
func TestAClaudeSubscriptionHasNoAddressNoKeyAndNoFleetAccess(t *testing.T) {
	withClaudeCode(t, "claude.ai")
	h := newHarness(t)
	_, alice := h.user("alice", store.RoleAdmin)
	for field, body := range map[string]map[string]any{
		"base_url":     {"base_url": "https://api.anthropic.com"},
		"api_key":      {"api_key": "sk-ant-oat01-a-subscription-token"},
		"fleet_access": {"fleet_access": true},
	} {
		b := claudeBody("Claude")
		for k, v := range body {
			b[k] = v
		}
		resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: b})
		resp.mustStatus(t, http.StatusUnprocessableEntity, "a subscription with a "+field)
		if !strings.Contains(string(resp.body), `"`+field+`"`) {
			t.Errorf("the refusal does not name %s: %s", field, resp.body)
		}
		if strings.Contains(string(resp.body), "a-subscription-token") {
			t.Errorf("the refusal repeats the key: %s", resp.body)
		}
	}
	if rows, _ := h.st.ListAssistantProviders(h.ctx); len(rows) != 0 {
		t.Errorf("%d providers were saved", len(rows))
	}
}

// It is the person's who added it, and says so; everybody else sees that it
// exists and whose it is, and that it is not theirs.
func TestAClaudeSubscriptionBelongsToWhoAddedIt(t *testing.T) {
	h, alice, bob, id := claudeHarness(t)
	row, _ := h.st.GetAssistantProvider(h.ctx, id)
	users, _ := h.st.ListUsers(h.ctx)
	var aliceID string
	for _, u := range users {
		if u.Username == "alice" {
			aliceID = u.ID
		}
	}
	if row.OwnerID == "" || row.OwnerID != aliceID {
		t.Fatalf("owner = %q, want alice's %q", row.OwnerID, aliceID)
	}
	for who, cookie := range map[string]string{"alice": alice, "bob": bob} {
		resp := h.do(request{method: http.MethodGet, path: assistantProviders + "/" + id, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, who+" reading it")
		v := resp.json(t)
		mine := who == "alice"
		if v["subscription"] != true || v["owner"] != "alice" || v["owned_by_you"] != mine || v["usable"] != mine {
			t.Errorf("%s sees %v", who, v)
		}
		if v["key_configured"] != false || v["base_url"] != "" {
			t.Errorf("a subscription shows a key or an address: %v", v)
		}
	}
	// A provider that is shared is usable by everybody, and nobody's.
	shared := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: map[string]any{
		"name": "Hosted", "kind": "anthropic", "model": "m", "api_key": "sk-test"}})
	shared.mustStatus(t, http.StatusCreated, "adding a shared provider")
	if v := shared.json(t); v["subscription"] != false || v["usable"] != true || v["owned_by_you"] != false || v["owner"] != nil {
		t.Errorf("a shared provider says %v", v)
	}
}

// Nobody but the owner uses it, changes it, tests it or makes it the default, and
// the refusal has a code that says why. Any administrator may remove it: it holds
// no credential, and a person who has gone leaves it behind.
func TestAnotherAdministratorCannotUseSomeonesClaudeSubscription(t *testing.T) {
	h, alice, bob, id := claudeHarness(t)
	path := assistantProviders + "/" + id
	for name, req := range map[string]request{
		"change it":          {method: http.MethodPatch, path: path, body: map[string]any{"model": "opus"}},
		"test it":            {method: http.MethodPost, path: path + "/check", body: map[string]any{}},
		"make it default":    {method: http.MethodPost, path: path + "/default", body: map[string]any{}},
		"test it as a draft": {method: http.MethodPost, path: assistantProviders + "/check", body: map[string]any{"id": id, "kind": "claude_code", "model": "sonnet", "name": "x"}},
		"list its models":    {method: http.MethodPost, path: assistantProviders + "/models", body: map[string]any{"id": id, "kind": "claude_code", "model": "sonnet", "name": "x"}},
		"chat through it":    {method: http.MethodPost, path: assistantChat, body: map[string]any{"provider_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}}}},
	} {
		req.cookie = bob
		resp := h.do(req)
		resp.mustStatus(t, http.StatusForbidden, "bob trying to "+name)
		if !strings.Contains(string(resp.body), "assistant.provider_not_yours") {
			t.Errorf("bob trying to %s: %s", name, resp.body)
		}
	}
	// Nothing he tried changed it.
	if row, _ := h.st.GetAssistantProvider(h.ctx, id); row.Model != "sonnet" || row.IsDefault {
		t.Errorf("the provider was changed: %+v", row)
	}
	// Alice can do all of that.
	h.do(request{method: http.MethodPatch, path: path, cookie: alice, body: map[string]any{"model": "opus"}}).mustStatus(t, http.StatusOK, "alice changing it")
	h.do(request{method: http.MethodPost, path: path + "/default", cookie: alice, body: map[string]any{}}).mustStatus(t, http.StatusOK, "alice making it the default")
	// And an administrator who is not the owner can take it away, with its name typed.
	h.do(request{method: http.MethodDelete, path: path, cookie: bob, body: map[string]any{"name": "Alice's Claude"}}).mustStatus(t, http.StatusNoContent, "bob removing it")
}

// The test reads how Claude Code is signed in and spends nothing; a copy signed in
// with an API key is turned away with what to do.
func TestTestingAClaudeSubscriptionReadsHowClaudeCodeIsSignedIn(t *testing.T) {
	h, alice, _, id := claudeHarness(t)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: alice, body: map[string]any{}})
	resp.mustStatus(t, http.StatusOK, "testing it")
	if v := resp.json(t); v["ok"] != true || !strings.Contains(v["model"].(string), "Claude Code 2.1.300") {
		t.Errorf("check = %v", v)
	}

	withClaudeCode(t, "api_key")
	resp = h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: alice, body: map[string]any{}})
	resp.mustStatus(t, http.StatusOK, "testing it again")
	if v := resp.json(t); v["ok"] != false || !strings.Contains(strings.ToLower(v["error"].(string)), "api key") {
		t.Errorf("a copy signed in with a key passed: %v", v)
	}
}

// Eli answers through the owner's Claude Code, in the words it streams, and the
// chat says it could not read the fleet; the audit row says which provider and
// that no tool was used.
func TestEliAnswersThroughTheOwnersClaudeCode(t *testing.T) {
	h, alice, _, id := claudeHarness(t)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: alice, body: map[string]any{}}).
		mustStatus(t, http.StatusOK, "making it the default")
	resp := h.do(request{method: http.MethodPost, path: assistantChat, cookie: alice, readStream: true, body: chatBody("user", "What is a runner?")})
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
	if text != "Hello from Claude" || done["provider"] != "Alice's Claude" || done["model"] != "sonnet" || done["fleet_access"] != false {
		t.Errorf("text = %q, done = %v", text, done)
	}
	rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"assistant.chat"}}, store.Page{Limit: 5})
	if len(rows) != 1 || !strings.Contains(rows[0].After, `"provider":"Alice's Claude"`) || !strings.Contains(rows[0].After, `"fleet_access":false`) {
		t.Errorf("audit rows = %+v", rows)
	}
}

// When the default is somebody else's subscription, a chat is answered by the
// first shared provider the person may use; with none, there is no model for them
// and they are not given the other person's.
func TestWhenTheDefaultIsSomeoneElsesSubscriptionAChatFallsBackOrHasNoModel(t *testing.T) {
	h, alice, bob, id := claudeHarness(t)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: alice, body: map[string]any{}}).
		mustStatus(t, http.StatusOK, "making it the default")
	ask := func(cookie string) *response {
		return h.do(request{method: http.MethodPost, path: assistantChat, cookie: cookie, readStream: true, body: chatBody("user", "hi")})
	}
	ask(bob).mustStatus(t, http.StatusConflict, "bob with nothing but alice's subscription")

	srvURL := startShared(t, h, alice)
	resp := ask(bob)
	resp.mustStatus(t, http.StatusOK, "bob with a shared provider")
	var done map[string]any
	for _, f := range frames(t, resp.body) {
		if f.kind == "done" {
			done = f.data
		}
	}
	if done == nil || done["provider"] != "Shared" {
		t.Errorf("done = %v (shared provider at %s)", done, srvURL)
	}
	// Alice, with her own as the default, still gets hers.
	for _, f := range frames(t, ask(alice).body) {
		if f.kind == "done" && f.data["provider"] != "Alice's Claude" {
			t.Errorf("alice was answered by %v", f.data["provider"])
		}
	}
}

// The fallback is a provider that is on: a switched-off one that sorts first is not
// asked, or a person would be answered by a model an administrator had turned off.
func TestTheFallbackIsNeverAProviderThatIsSwitchedOff(t *testing.T) {
	h, alice, bob, id := claudeHarness(t)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: alice, body: map[string]any{}}).
		mustStatus(t, http.StatusOK, "making it the default")
	off := assistanttest.NewOpenAI(t)
	body := providerBody(off, "Aaa switched off")
	body["enabled"] = false
	h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: body}).
		mustStatus(t, http.StatusCreated, "adding a provider that is off")
	startShared(t, h, alice)

	resp := h.do(request{method: http.MethodPost, path: assistantChat, cookie: bob, readStream: true, body: chatBody("user", "hi")})
	resp.mustStatus(t, http.StatusOK, "bob with one provider on")
	for _, f := range frames(t, resp.body) {
		if f.kind == "done" && f.data["provider"] != "Shared" {
			t.Errorf("answered by %v, want the one that is on", f.data["provider"])
		}
	}
}

// Local models only refuses it, because Claude Code dials Anthropic itself, which
// no promise about the dialer can cover.
func TestLocalModelsOnlyRefusesClaudeCode(t *testing.T) {
	withClaudeCode(t, "claude.ai")
	h := newHarness(t, func(c *config.Config) { c.Assistant.LocalOnly = true })
	_, alice := h.user("alice", store.RoleAdmin)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: alice, body: claudeBody("Alice's Claude")})
	resp.mustStatus(t, http.StatusCreated, "adding it")
	id := resp.json(t)["id"].(string)
	resp = h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: alice, body: map[string]any{}})
	resp.mustStatus(t, http.StatusOK, "testing it")
	if v := resp.json(t); v["ok"] != false || !strings.Contains(v["error"].(string), "Local models only") {
		t.Errorf("check = %v", v)
	}
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: alice, body: map[string]any{}}).
		mustStatus(t, http.StatusOK, "making it the default")
	h.do(request{method: http.MethodPost, path: assistantChat, cookie: alice, readStream: true, body: chatBody("user", "hi")}).
		mustStatus(t, http.StatusBadGateway, "asking with local only on")
}

// startShared adds a provider every administrator may use, and returns where it is.
func startShared(t *testing.T, h *harness, cookie string) string {
	t.Helper()
	srv := assistanttest.NewOpenAI(t)
	h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Shared")}).
		mustStatus(t, http.StatusCreated, "adding a shared provider")
	return srv.URL
}

// Even if a Claude subscription somehow had the fleet switch on, as a row written
// around the API could, it is not offered Eli's tools: the tool it runs has its
// own turned off and takes a question as text.
func TestAClaudeSubscriptionIsNeverOfferedTheFleet(t *testing.T) {
	h, alice, _, id := claudeHarness(t)
	row, _ := h.st.GetAssistantProvider(h.ctx, id)
	row.FleetAccess = true
	if err := h.st.UpdateAssistantProvider(h.ctx, row); err != nil {
		t.Fatal(err)
	}
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: alice, body: map[string]any{}}).
		mustStatus(t, http.StatusOK, "making it the default")
	resp := h.do(request{method: http.MethodPost, path: assistantChat, cookie: alice, readStream: true, body: chatBody("user", "How is the fleet?")})
	resp.mustStatus(t, http.StatusOK, "asking")
	for _, f := range frames(t, resp.body) {
		if f.kind == "tool" || (f.kind == "done" && f.data["fleet_access"] != false) {
			t.Errorf("Claude Code was given the fleet: %v %v", f.kind, f.data)
		}
	}
}

// A token that belongs to nobody cannot add a subscription: there is nobody for
// it to belong to.
func TestATokenWithNoOwnerCannotAddASubscription(t *testing.T) {
	withClaudeCode(t, "claude.ai")
	h := newHarness(t)
	token := h.token("ci", store.RoleAdmin)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, token: token, body: claudeBody("Claude")})
	resp.mustStatus(t, http.StatusForbidden, "a token with no owner adding a subscription")
	if rows, _ := h.st.ListAssistantProviders(h.ctx); len(rows) != 0 {
		t.Errorf("%d providers were saved", len(rows))
	}
}
