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
