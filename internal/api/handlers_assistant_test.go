package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

const assistantProviders = "/api/v1/assistant/providers"

func assistantAdmin(t *testing.T, opts ...func(*config.Config)) (*harness, string) {
	t.Helper()
	h := newHarness(t, opts...)
	_, cookie := h.user("alice", store.RoleAdmin)
	return h, cookie
}

func providerBody(srv *assistanttest.Server, name string) map[string]any {
	return map[string]any{"name": name, "kind": "openai_compatible", "base_url": srv.URL + "/v1", "model": "m", "api_key": "sk-test"}
}

// Which model answers, and with what key, is an administrator's decision
// until the step-up slice makes it cost more than an admin cookie. The kinds
// are admin's too: only the page that adds a provider asks for them, and the
// rail locks that page from its own table.
func TestAssistantProvidersAreAdminOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	for _, role := range []store.Role{store.RoleViewer, store.RoleOperator} {
		_, cookie := h.user("user-"+string(role), role)
		h.do(request{method: http.MethodGet, path: assistantProviders, cookie: cookie}).mustStatus(t, http.StatusForbidden, string(role)+" listing")
		h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "x")}).mustStatus(t, http.StatusForbidden, string(role)+" creating")
		h.do(request{method: http.MethodGet, path: assistantProviders + "/kinds", cookie: cookie}).mustStatus(t, http.StatusForbidden, string(role)+" reading the kinds")
	}
}

func TestCreatingAProviderSealsTheKeyAndNeverReturnsIt(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")})
	resp.mustStatus(t, http.StatusCreated, "creating")
	if strings.Contains(string(resp.body), "sk-test") {
		t.Fatalf("the key is in the response: %s", resp.body)
	}
	view := resp.json(t)
	if view["key_configured"] != true || view["name"] != "Ollama" || view["kind"] != "openai_compatible" || view["local"] != true {
		t.Errorf("view %v", view)
	}
	row, err := h.st.GetAssistantProvider(h.ctx, view["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if key, err := h.key.OpenString(row.KeyEnc); err != nil || key != "sk-test" {
		t.Errorf("sealed key opens to %q, %v", key, err)
	}
	list := h.do(request{method: http.MethodGet, path: assistantProviders, cookie: cookie})
	list.mustStatus(t, http.StatusOK, "listing")
	if strings.Contains(string(list.body), "sk-test") {
		t.Errorf("the key is in the list: %s", list.body)
	}
}

func TestAPrivateBaseURLIsRefusedUntilTheAssistantSwitchIsOn(t *testing.T) {
	h, cookie := assistantAdmin(t)
	srv := assistanttest.NewOpenAI(t)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a loopback address with the switch off")
	if body := string(resp.body); !strings.Contains(body, "assistant.allow_private_provider") || !strings.Contains(body, `"base_url"`) {
		t.Errorf("the refusal does not name the switch and the field: %s", body)
	}
	h2, cookie2 := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	h2.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie2, body: providerBody(srv, "Ollama")}).mustStatus(t, http.StatusCreated, "with the switch on")
}

func TestPatchingWithoutAKeyKeepsTheOldOne(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	created := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")}).json(t)
	id := created["id"].(string)
	h.do(request{method: http.MethodPatch, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"model": "m2", "api_key": ""}}).mustStatus(t, http.StatusOK, "patching")
	row, _ := h.st.GetAssistantProvider(h.ctx, id)
	if key, _ := h.key.OpenString(row.KeyEnc); key != "sk-test" || row.Model != "m2" {
		t.Errorf("after the patch: key %q model %q", key, row.Model)
	}
	h.do(request{method: http.MethodPatch, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"api_key": "sk-new"}}).mustStatus(t, http.StatusOK, "rotating")
	row, _ = h.st.GetAssistantProvider(h.ctx, id)
	if key, _ := h.key.OpenString(row.KeyEnc); key != "sk-new" {
		t.Errorf("after rotating: key %q", key)
	}
}

func TestCheckingADraftUsesTheFormsKeyAndStoresNothing(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	resp := h.do(request{method: http.MethodPost, path: assistantProviders + "/check", cookie: cookie, body: providerBody(srv, "draft")})
	resp.mustStatus(t, http.StatusOK, "checking a draft")
	check := resp.json(t)
	if check["ok"] != true || check["model"] != "m" || check["usage_reported"] != true {
		t.Errorf("check %v", check)
	}
	if reqs := srv.Requests(); len(reqs) == 0 || reqs[0].Header.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("the draft's key was not sent: %+v", reqs)
	}
	if rows, _ := h.st.ListAssistantProviders(h.ctx); len(rows) != 0 {
		t.Errorf("a draft check stored %d rows", len(rows))
	}
}

func TestCheckingAProviderRecordsTheResultOnTheRow(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	id := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")}).json(t)["id"].(string)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: cookie}).mustStatus(t, http.StatusOK, "checking")
	view := h.do(request{method: http.MethodGet, path: assistantProviders + "/" + id, cookie: cookie}).json(t)
	check, _ := view["last_check"].(map[string]any)
	if check == nil || check["ok"] != true || check["model"] != "m" || check["usage_reported"] != true {
		t.Fatalf("last_check %v", view["last_check"])
	}
	if ms, _ := check["latency_ms"].(float64); ms < 0 {
		t.Errorf("latency_ms %v", check["latency_ms"])
	}
}

// Review Focus 2: a 401 from the provider must not put the key, or the
// request that carried it, on the settings page or in the row.
func TestACheckThatFailsNamesTheCauseAndNotTheKey(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	id := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")}).json(t)["id"].(string)
	srv.Status, srv.Body = 401, `{"error":{"message":"bad key sk-test"}}`
	resp := h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "a failed check is still an answer")
	check := resp.json(t)
	if check["ok"] != false || !strings.Contains(check["error"].(string), "refused the key") || strings.Contains(string(resp.body), "sk-test") {
		t.Errorf("check %s", resp.body)
	}
	row, _ := h.st.GetAssistantProvider(h.ctx, id)
	if strings.Contains(string(row.LastCheck), "sk-test") || !strings.Contains(string(row.LastCheck), "refused the key") {
		t.Errorf("row's last check %s", row.LastCheck)
	}
}

func TestSettingTheDefaultIsAudited(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	a := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "A")}).json(t)["id"].(string)
	b := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "B")}).json(t)["id"].(string)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + a + "/default", cookie: cookie}).mustStatus(t, http.StatusOK, "default a")
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + b + "/default", cookie: cookie}).mustStatus(t, http.StatusOK, "default b")
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"assistant.provider.default"}}, store.Page{Limit: 5})
	if err != nil || len(rows) != 2 {
		t.Errorf("audit rows %d, %v", len(rows), err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	h.do(request{method: http.MethodGet, path: assistantProviders, cookie: cookie}).into(t, &list)
	for _, p := range list.Items {
		if (p["id"] == b) != (p["is_default"] == true) {
			t.Errorf("default flags %v", list.Items)
		}
	}
}

func TestDeletingAProviderNeedsItsNameInTheBody(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	id := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")}).json(t)["id"].(string)
	h.do(request{method: http.MethodDelete, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"name": "Olama"}}).mustStatus(t, http.StatusConflict, "the wrong name")
	h.do(request{method: http.MethodDelete, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"name": "Ollama"}}).mustStatus(t, http.StatusNoContent, "the right name")
	if rows, _ := h.st.ListAssistantProviders(h.ctx); len(rows) != 0 {
		t.Errorf("%d rows after delete", len(rows))
	}
}

// Review: Test in the Edit dialog ran as a draft with no key, so a working
// hosted provider reported "the provider refused the key". A draft check
// that names the saved row borrows its sealed key when the box is blank.
func TestCheckingADraftOfASavedProviderBorrowsItsSealedKey(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	id := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")}).json(t)["id"].(string)
	body := providerBody(srv, "Ollama")
	body["api_key"] = ""
	body["id"] = id
	body["model"] = "m2"
	resp := h.do(request{method: http.MethodPost, path: assistantProviders + "/check", cookie: cookie, body: body})
	resp.mustStatus(t, http.StatusOK, "checking a draft of a saved row")
	reqs := srv.Requests()
	last := reqs[len(reqs)-1]
	if last.Header.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("the saved key was not borrowed: %v", last.Header.Get("Authorization"))
	}
	if last.Body["model"] != "m2" {
		t.Errorf("the draft's own fields were not used: %v", last.Body["model"])
	}
}

// Review: a PATCH that does not name base_url re-ran the egress check on the
// stored one, so Disable failed on a loopback provider once the switch was
// turned off. The check is about saving an address, not about a row holding one.
func TestPatchingWithoutABaseURLDoesNotRecheckTheStoredOne(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	id := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: providerBody(srv, "Ollama")}).json(t)["id"].(string)
	h.ctrl.UpdateConfig(func(c *config.Config) { c.Assistant.AllowPrivateProvider = false })
	h.do(request{method: http.MethodPatch, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"enabled": false}}).mustStatus(t, http.StatusOK, "disabling with the switch off")
	h.do(request{method: http.MethodPatch, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"base_url": srv.URL + "/v1"}}).mustStatus(t, http.StatusUnprocessableEntity, "re-saving the address with the switch off")
}

// Review minor: the key box is the one place a secret belongs. An address
// with a username and password in it would be saved and rendered back on
// the card, so it is refused where it is typed.
func TestABaseURLWithCredentialsInItIsRefused(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	body := providerBody(srv, "Ollama")
	body["base_url"] = strings.Replace(srv.URL, "http://", "http://user:hunter2@", 1) + "/v1"
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "an address carrying credentials")
	if !strings.Contains(string(resp.body), `"base_url"`) || strings.Contains(string(resp.body), "hunter2") {
		t.Errorf("the refusal does not name the field, or echoes the password: %s", resp.body)
	}
}

// How hard the model thinks is set per provider, because the value is the
// provider's to interpret and a thinking model at its default effort can spend
// an answer's whole room on reasoning. It is kept, shown, and sent on every
// request; a value no provider takes is refused by name, and a kind whose tool
// has no such setting is refused rather than silently ignored.
func TestAProvidersReasoningEffortIsKeptShownAndSent(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	body := providerBody(srv, "DeepSeek")
	body["reasoning_effort"] = "low"
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	resp.mustStatus(t, http.StatusCreated, "creating with an effort")
	view := resp.json(t)
	if view["reasoning_effort"] != "low" {
		t.Errorf("view = %v, want reasoning_effort low", view)
	}
	id := view["id"].(string)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/check", cookie: cookie}).mustStatus(t, http.StatusOK, "checking")
	var sent []any
	for _, r := range srv.Requests() {
		if r.Path == "/v1/chat/completions" {
			sent = append(sent, r.Body["reasoning_effort"])
		}
	}
	if len(sent) == 0 || sent[len(sent)-1] != "low" {
		t.Errorf("the check did not send the effort: %v", sent)
	}

	body["reasoning_effort"] = "max"
	bad := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	bad.mustStatus(t, http.StatusUnprocessableEntity, "an effort no provider takes here")
	if !strings.Contains(string(bad.body), `"reasoning_effort"`) {
		t.Errorf("the refusal does not name the field: %s", bad.body)
	}

	patch := h.do(request{method: http.MethodPatch, path: assistantProviders + "/" + id, cookie: cookie, body: map[string]any{"reasoning_effort": ""}})
	patch.mustStatus(t, http.StatusOK, "clearing it")
	if patch.json(t)["reasoning_effort"] != "" {
		t.Errorf("cleared effort still shown: %v", patch.json(t))
	}
}

func TestAReasoningEffortIsRefusedOnAKindWhoseToolHasNoSuchSetting(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewAnthropic(t)
	body := map[string]any{"name": "Claude", "kind": "anthropic", "base_url": srv.URL + "/v1", "model": "m", "api_key": "k", "reasoning_effort": "low"}
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "an effort on a kind that has none")
	if !strings.Contains(string(resp.body), `"reasoning_effort"`) {
		t.Errorf("the refusal does not name the field: %s", resp.body)
	}
}

// How much the model may write in one round is set per provider, because the
// right number is the model's and the cost of the room is the provider's price.
// It is kept, shown, sent as the request's ceiling, and said on the done frame
// with the provider, so a cut answer can tell the person what to raise and on
// what. A number outside the bounds is refused by name, and a kind whose tool
// takes no ceiling is refused rather than silently ignored.
func TestAProvidersOutputLimitIsKeptSentAndSaidOnTheDoneFrame(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	srv := assistanttest.NewOpenAI(t)
	body := providerBody(srv, "DeepSeek")
	body["max_output_tokens"] = 16384
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	resp.mustStatus(t, http.StatusCreated, "creating with a limit")
	view := resp.json(t)
	if view["max_output_tokens"] != float64(16384) {
		t.Errorf("view = %v, want max_output_tokens 16384", view)
	}
	id := view["id"].(string)
	h.do(request{method: http.MethodPost, path: assistantProviders + "/" + id + "/default", cookie: cookie}).mustStatus(t, http.StatusOK, "making it the default")
	chat := h.do(request{method: http.MethodPost, path: assistantChat, cookie: cookie, readStream: true, body: chatBody("user", "Which host?")})
	chat.mustStatus(t, http.StatusOK, "asking")
	var done map[string]any
	for _, f := range frames(t, chat.body) {
		if f.kind == "done" {
			done = f.data
		}
	}
	if done == nil || done["output_limit"] != float64(16384) || done["provider_id"] != id {
		t.Errorf("done = %v, want output_limit 16384 and the provider's id", done)
	}
	reqs := srv.Requests()
	if last := reqs[len(reqs)-1].Body; last["max_tokens"] != float64(16384) {
		t.Errorf("the request's ceiling = %v, want the provider's", last["max_tokens"])
	}

	body["max_output_tokens"] = 500
	bad := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	bad.mustStatus(t, http.StatusUnprocessableEntity, "a limit below the floor")
	if !strings.Contains(string(bad.body), `"max_output_tokens"`) {
		t.Errorf("the refusal does not name the field: %s", bad.body)
	}
}

func TestAnOutputLimitIsRefusedOnAKindWhoseToolTakesNone(t *testing.T) {
	h, cookie := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	body := map[string]any{"name": "Claude Code", "kind": "claude_code", "max_output_tokens": 16384}
	resp := h.do(request{method: http.MethodPost, path: assistantProviders, cookie: cookie, body: body})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a limit on a tool that takes none")
	if !strings.Contains(string(resp.body), `"max_output_tokens"`) {
		t.Errorf("the refusal does not name the field: %s", resp.body)
	}
}
