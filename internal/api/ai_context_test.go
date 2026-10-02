package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

func TestAIContextDiscoveryRequiresConfigurationAuthority(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	for _, role := range []store.Role{store.RoleViewer, store.RoleOperator} {
		_, cookie := h.user(string(role)+"-context", role)
		h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: cookie}).mustStatus(t, http.StatusForbidden, "source discovery")
	}
	_, cookie := h.user("context-admin", store.RoleAdmin)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "source discovery")
	var out controller.AIContextDiscovery
	resp.into(t, &out)
	if len(out.Repositories) != 3 || out.Capped {
		t.Fatalf("discovery = %+v", out)
	}
	seen := map[int64]bool{}
	for _, repo := range out.Repositories {
		if repo.ID <= 0 || seen[repo.ID] {
			t.Fatalf("repository identity is missing or duplicated: %+v", repo)
		}
		seen[repo.ID] = true
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery", cookie: cookie}).mustStatus(t, http.StatusUnprocessableEntity, "missing installation")
}

func TestAIContextDiscoveryDistinguishesReadingFromSetup(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	h.gh.SetPermissions(map[string]string{"contents": "read", "metadata": "read"})
	_, cookie := h.user("context-admin", store.RoleAdmin)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "read-only app discovery")
	var out controller.AIContextDiscovery
	resp.into(t, &out)
	if !out.CanReadContents || len(out.MissingSetupPermissions) == 0 {
		t.Fatalf("read-only permissions = %+v", out)
	}
	h.gh.SetPermissions(map[string]string{"metadata": "read"})
	resp = h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "metadata-only app discovery")
	resp.into(t, &out)
	if out.CanReadContents {
		t.Fatal("metadata permission must not imply source permission")
	}
}

func TestAIContextDraftUsesDiscoveredIdentityAndGrantsNobodySourceAccess(t *testing.T) {
	h, inst, operator := migrationHarness(t)
	_, admin := h.user("draft-admin", store.RoleAdmin)
	discovery := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: admin})
	discovery.mustStatus(t, http.StatusOK, "discovery")
	var candidates controller.AIContextDiscovery
	discovery.into(t, &candidates)
	body := controller.AIContextDraftRequest{InstallationID: inst.ID, RepositoryID: candidates.Repositories[0].ID}
	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: operator, body: body}).mustStatus(t, http.StatusForbidden, "operator draft")
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: admin, body: body})
	resp.mustStatus(t, http.StatusCreated, "create draft")
	var draft store.AIContextRepository
	resp.into(t, &draft)
	if draft.Available || draft.Revision != 1 || draft.Key.RepositoryID != body.RepositoryID || draft.FullName != candidates.Repositories[0].FullName || draft.Config.SourceBranch != candidates.Repositories[0].DefaultBranch {
		t.Fatalf("draft = %+v", draft)
	}
	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: admin, body: body}).mustStatus(t, http.StatusConflict, "duplicate draft")
	resp = h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/repositories?limit=1&offset=0", cookie: admin})
	resp.mustStatus(t, http.StatusOK, "draft list")
	var page struct {
		Items []store.AIContextRepository `json:"items"`
		Total int                         `json:"total"`
		Limit int                         `json:"limit"`
	}
	resp.into(t, &page)
	if len(page.Items) != 1 || page.Total != 1 || page.Limit != 1 {
		t.Fatalf("page = %+v", page)
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/repositories", cookie: operator}).mustStatus(t, http.StatusForbidden, "operator metadata list")

	draft.Config.KeepSnapshots = 7
	update := map[string]any{"revision": draft.Revision, "config": draft.Config}
	h.do(request{method: http.MethodPatch, path: "/api/v1/ai-context/repositories/" + draft.ID + "/config", cookie: operator, body: update}).mustStatus(t, http.StatusForbidden, "operator configuration")
	resp = h.do(request{method: http.MethodPatch, path: "/api/v1/ai-context/repositories/" + draft.ID + "/config", cookie: admin, body: update})
	resp.mustStatus(t, http.StatusOK, "save configuration")
	var saved store.AIContextRepository
	resp.into(t, &saved)
	if saved.Revision != 2 || saved.Config.KeepSnapshots != 7 || saved.Available {
		t.Fatalf("saved = %+v", saved)
	}
	h.do(request{method: http.MethodPatch, path: "/api/v1/ai-context/repositories/" + draft.ID + "/config", cookie: admin, body: update}).mustStatus(t, http.StatusConflict, "stale configuration")
	saved.Config.SourceBranch = "unverified-branch"
	h.do(request{method: http.MethodPatch, path: "/api/v1/ai-context/repositories/" + draft.ID + "/config", cookie: admin, body: map[string]any{"revision": saved.Revision, "config": saved.Config}}).mustStatus(t, http.StatusUnprocessableEntity, "unverified source branch")
	body.RepositoryID = 999999
	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: admin, body: body}).mustStatus(t, http.StatusUnprocessableEntity, "undiscovered repository")
}

func TestAIContextDraftRefusesMissingContentsAndArchivedRepositories(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	_, admin := h.user("draft-admin", store.RoleAdmin)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: admin})
	resp.mustStatus(t, http.StatusOK, "discovery")
	var candidates controller.AIContextDiscovery
	resp.into(t, &candidates)
	body := controller.AIContextDraftRequest{InstallationID: inst.ID, RepositoryID: candidates.Repositories[0].ID}
	h.gh.SetArchived(candidates.Repositories[0].FullName, true)
	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: admin, body: body}).mustStatus(t, http.StatusUnprocessableEntity, "archived repository")
	h.gh.SetArchived(candidates.Repositories[0].FullName, false)
	h.gh.SetPermissions(map[string]string{"metadata": "read"})
	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: admin, body: body}).mustStatus(t, http.StatusUnprocessableEntity, "missing source permission")
}

func TestAIContextDiscoveryReportsItsRepositoryCeiling(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	for i := 0; i < 500; i++ {
		h.gh.AddRepo(fmt.Sprintf("acme/repo-%03d", i))
	}
	_, admin := h.user("discovery-admin", store.RoleAdmin)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: admin})
	resp.mustStatus(t, http.StatusOK, "bounded discovery")
	var out controller.AIContextDiscovery
	resp.into(t, &out)
	if !out.Capped || len(out.Repositories) != 500 {
		t.Fatalf("capped discovery = %d, %v", len(out.Repositories), out.Capped)
	}
}
