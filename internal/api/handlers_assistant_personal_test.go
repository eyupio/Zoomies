package api

import (
	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"net/http"
	"testing"
)

func TestPersonalProvidersCannotBeUsedOrChangedByAnotherAccount(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	_, alice := h.user("alice", store.RoleViewer)
	_, bob := h.user("bob", store.RoleAdmin)
	srv := assistanttest.NewOpenAI(t)
	base := "/api/v1/assistant/personal/providers"
	create := h.do(request{method: "POST", path: base, cookie: alice, body: providerBody(srv, "My model")})
	create.mustStatus(t, 201, "viewer creating own provider")
	id := create.json(t)["id"].(string)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", base + "/" + id, nil}, {"PATCH", base + "/" + id, map[string]any{"enabled": false}},
		{"DELETE", base + "/" + id, map[string]any{"name": "My model"}}, {"POST", base + "/" + id + "/check", nil}, {"POST", base + "/" + id + "/default", nil},
		{"POST", base + "/check", map[string]any{"id": id}}, {"POST", base + "/models", map[string]any{"id": id}},
	} {
		h.do(request{method: tc.method, path: tc.path, cookie: bob, body: tc.body}).mustStatus(t, http.StatusNotFound, "another account cannot access provider")
	}
	h.do(request{method: "GET", path: assistantProviders + "/" + id, cookie: bob}).mustStatus(t, 404, "installation admin cannot read personal provider")
	list := h.do(request{method: "GET", path: base, cookie: bob}).json(t)
	if len(list["items"].([]any)) != 0 {
		t.Fatal("another account's provider listed")
	}
	h.do(request{method: "POST", path: "/api/v1/assistant/personal/chat", cookie: bob, body: map[string]any{"provider_id": id, "messages": []map[string]string{{"role": "user", "content": "hello"}}}}).mustStatus(t, 409, "another account cannot charge provider")
	h.do(request{method: "GET", path: base, token: h.token("unowned", store.RoleAdmin, "*")}).mustStatus(t, 403, "unowned token")
}
func TestPersonalProviderDefaultsDoNotChangeInstallationDefaults(t *testing.T) {
	h, admin := assistantAdmin(t, func(c *config.Config) { c.Assistant.AllowPrivateProvider = true })
	_, viewer := h.user("viewer", store.RoleViewer)
	srv := assistanttest.NewOpenAI(t)
	shared := h.do(request{method: "POST", path: assistantProviders, cookie: admin, body: providerBody(srv, "Same name")}).json(t)
	ownBase := "/api/v1/assistant/personal/providers"
	own := h.do(request{method: "POST", path: ownBase, cookie: viewer, body: providerBody(srv, "Same name")})
	own.mustStatus(t, 201, "same name across owners")
	for _, entry := range []struct{ base, id, cookie string }{{assistantProviders, shared["id"].(string), admin}, {ownBase, own.json(t)["id"].(string), viewer}} {
		h.do(request{method: "POST", path: entry.base + "/" + entry.id + "/default", cookie: entry.cookie}).mustStatus(t, 200, "set independent default")
	}
	row, _ := h.st.GetAssistantProvider(h.ctx, shared["id"].(string))
	if !row.IsDefault {
		t.Fatal("personal default cleared installation default")
	}
}

func TestOnlyThePersonalProviderOwnerCanConfirmTheirGitHubLink(t *testing.T) {
	h := newHarness(t)
	owner, cookie := h.user("owner", store.RoleViewer)
	_, admin := h.user("admin", store.RoleAdmin)
	if e := h.st.SetEliIdentity(h.ctx, store.EliIdentity{UserID: owner.ID, GitHubUserID: 42, GitHubLogin: "octo"}); e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/assistant/repairs/consent"
	h.do(request{method: "PUT", path: path, cookie: admin, body: map[string]any{"github_user_id": 42, "enabled": true, "user_id": owner.ID}}).mustStatus(t, 400, "cannot choose another owner in the body")
	h.do(request{method: "PUT", path: path, cookie: admin, body: map[string]any{"github_user_id": 42, "enabled": true}}).mustStatus(t, 404, "administrator cannot confirm on behalf of another user")
	h.do(request{method: "PUT", path: path, cookie: cookie, body: map[string]any{"github_user_id": 43, "enabled": true}}).mustStatus(t, 404, "stale link")
	h.do(request{method: "PUT", path: path, cookie: cookie, body: map[string]any{"github_user_id": 42, "enabled": true}}).mustStatus(t, 204, "own link")
	h.do(request{method: "PUT", path: path, cookie: cookie, body: map[string]any{"github_user_id": 42, "enabled": false}}).mustStatus(t, 204, "revoke own link")
	rows, _ := h.st.EliIdentities(h.ctx)
	if rows[0].Confirmed {
		t.Fatal("consent not revoked")
	}
}
