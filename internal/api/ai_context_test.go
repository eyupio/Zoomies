package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

func TestContextReaderInstructionsAndBadgeRemainPrivateToMembers(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	discovery, err := h.ctrl.DiscoverAIContext(h.ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := h.ctrl.CreateAIContextDraft(h.ctx, controller.AIContextDraftRequest{InstallationID: inst.ID, RepositoryID: discovery.Repositories[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	reader, cookie := h.user("instructions-reader", store.RoleViewer)
	if err := h.st.ReplaceAIContextMembers(h.ctx, draft.ID, []string{reader.ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetAIContextAvailable(h.ctx, draft.ID, true); err != nil {
		t.Fatal(err)
	}
	response := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/access", cookie: cookie})
	response.mustStatus(t, http.StatusOK, "reader instructions")
	var page struct {
		Items []struct {
			Instructions string `json:"instructions"`
			Badge        string `json:"badge_markdown"`
		} `json:"items"`
	}
	response.into(t, &page)
	if len(page.Items) != 1 || !strings.Contains(page.Items[0].Instructions, "context_overview") || !strings.Contains(page.Items[0].Badge, "zoomies-ai-context.yml/badge.svg") {
		t.Fatal("reader guidance missing")
	}
	if err := h.st.ReplaceAIContextMembers(h.ctx, draft.ID, nil); err != nil {
		t.Fatal(err)
	}
	response = h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/access", cookie: cookie})
	response.into(t, &page)
	if len(page.Items) != 0 {
		t.Fatal("revoked reader retained repository instructions")
	}
}

func TestAIContextDiscoveryRequiresAdministrationOrInstallationOwnership(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	for _, role := range []store.Role{store.RoleViewer, store.RoleOperator} {
		_, cookie := h.user(string(role)+"-context", role)
		h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: cookie}).mustStatus(t, http.StatusNotFound, "source discovery without ownership")
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
	h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/repositories", cookie: operator, body: body}).mustStatus(t, http.StatusNotFound, "operator draft")
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
	if got := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/repositories", cookie: operator}); got.status != http.StatusOK || strings.Contains(string(got.body), draft.ID) {
		t.Fatal("operator metadata list must be empty for a non-owner", got.status)
	}

	draft.Config.KeepSnapshots = 7
	update := map[string]any{"revision": draft.Revision, "config": draft.Config}
	h.do(request{method: http.MethodPatch, path: "/api/v1/ai-context/repositories/" + draft.ID + "/config", cookie: operator, body: update}).mustStatus(t, http.StatusNotFound, "operator configuration")
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

	lookup := h.do(request{method: http.MethodGet, path: fmt.Sprintf("/api/v1/ai-context/draft?installation_id=%s&repository_id=%d", inst.ID, body.RepositoryID), cookie: admin})
	lookup.mustStatus(t, http.StatusOK, "stable draft lookup")
	var recovered store.AIContextRepository
	lookup.into(t, &recovered)
	if recovered.ID != draft.ID || recovered.Revision != 2 {
		t.Fatalf("resumed wrong draft: %+v", recovered)
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/repositories/" + draft.ID, cookie: operator}).mustStatus(t, http.StatusNotFound, "operator draft resumption")
	h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/repositories/" + draft.ID, cookie: admin}).mustStatus(t, http.StatusOK, "draft resumption")
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

func TestConnectionSourceConsentRequiresTheSignedInOwnerAndLiveMembership(t *testing.T) {
	h := oauthHarness(t)
	inst := h.installation()
	reader, cookie := h.user("source-reader", store.RoleViewer)
	_, otherCookie := h.user("other-reader", store.RoleViewer)
	_, admin := h.user("source-admin", store.RoleAdmin)
	client := h.registerNamedClient("Source test", claudeReturn)
	requestID := requestFrom(t, h.authorize(client, claudeReturn, nil))
	code := h.approve(cookie, requestID, store.RoleViewer)
	pair := pairFrom(t, h.exchange(client, code, nil, nil))
	list := h.do(request{method: http.MethodGet, path: "/api/v1/auth/mcp-connections", cookie: cookie})
	list.mustStatus(t, http.StatusOK, "own connections")
	var gs struct {
		Items []mcpConnectionResponse `json:"items"`
	}
	list.into(t, &gs)
	if len(gs.Items) != 1 {
		t.Fatalf("connections = %+v", gs)
	}
	path := "/api/v1/auth/mcp-connections/" + gs.Items[0].ID + "/repositories"
	repo := &store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: inst.ID, RepositoryID: 991}, FullName: "acme/source", Config: aicontext.DefaultConfig("main")}
	if err := h.st.CreateAIContextRepository(h.ctx, repo); err != nil {
		t.Fatal(err)
	}
	membersPath := "/api/v1/ai-context/repositories/" + repo.ID + "/members"
	h.do(request{method: http.MethodPut, path: membersPath, cookie: cookie, body: map[string]any{"user_ids": []string{reader.ID}}}).mustStatus(t, http.StatusNotFound, "viewer membership")
	h.do(request{method: http.MethodPut, path: membersPath, cookie: admin, body: map[string]any{}}).mustStatus(t, http.StatusUnprocessableEntity, "omitted membership")
	h.do(request{method: http.MethodPut, path: membersPath, cookie: admin, body: map[string]any{"user_ids": []string{reader.ID}}}).mustStatus(t, http.StatusOK, "explicit membership")
	h.do(request{method: http.MethodGet, path: path, cookie: otherCookie}).mustStatus(t, http.StatusNotFound, "another owner's choices")
	h.do(request{method: http.MethodPut, path: path, cookie: otherCookie, body: map[string]any{"repository_ids": []string{repo.ID}}}).mustStatus(t, http.StatusNotFound, "another owner's consent")
	h.do(request{method: http.MethodPut, path: path, cookie: cookie, body: map[string]any{}}).mustStatus(t, http.StatusUnprocessableEntity, "omitted consent")
	h.do(request{method: http.MethodPut, path: path, cookie: cookie, body: map[string]any{"repository_ids": []string{repo.ID}}}).mustStatus(t, http.StatusNotFound, "unavailable draft")
	h.st.SetAIContextAvailable(h.ctx, repo.ID, true)

	ownPage := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/access", cookie: cookie})
	ownPage.mustStatus(t, http.StatusOK, "reader repository list")
	var own struct {
		Items []store.AIContextChoice `json:"items"`
		Total int                     `json:"total"`
	}
	ownPage.into(t, &own)
	if len(own.Items) != 1 || own.Total != 1 || own.Items[0].ID != repo.ID {
		t.Fatalf("reader list = %+v", own)
	}
	for _, withoutMembership := range []string{otherCookie, admin} {
		page := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/access", cookie: withoutMembership})
		page.mustStatus(t, http.StatusOK, "unshared source list")
		page.into(t, &own)
		if own.Total != 0 || len(own.Items) != 0 {
			t.Fatalf("fleet role leaked names: %+v", own)
		}
	}
	resp := h.do(request{method: http.MethodGet, path: path, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "eligible choices")
	var choices store.AIContextConnectionSelection
	resp.into(t, &choices)
	if len(choices.Items) != 1 || len(choices.SelectedRepositoryIDs) != 0 {
		t.Fatalf("implicit source consent: %+v", choices)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		h.do(request{method: method, path: path, token: pair.AccessToken, body: map[string]any{"repository_ids": []string{repo.ID}}}).mustStatus(t, http.StatusUnauthorized, "MCP credentials are confined to MCP")
	}
	h.do(request{method: http.MethodPut, path: path, token: h.token("source-automation", store.RoleAdmin), body: map[string]any{"repository_ids": []string{repo.ID}}}).mustStatus(t, http.StatusForbidden, "API token cannot grant source consent")
	h.do(request{method: http.MethodPut, path: path, cookie: cookie, body: map[string]any{"repository_ids": []string{repo.ID}}}).mustStatus(t, http.StatusNoContent, "explicit source consent")
	resp = h.do(request{method: http.MethodGet, path: path, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "saved selection")
	resp.into(t, &choices)
	if len(choices.SelectedRepositoryIDs) != 1 || choices.SelectedRepositoryIDs[0] != repo.ID {
		t.Fatalf("selection = %+v", choices)
	}
	h.do(request{method: http.MethodPut, path: membersPath, cookie: admin, body: map[string]any{"user_ids": []string{}}}).mustStatus(t, http.StatusOK, "remove reader")
	h.do(request{method: http.MethodPut, path: membersPath, cookie: admin, body: map[string]any{"user_ids": []string{reader.ID}}}).mustStatus(t, http.StatusOK, "restore reader")
	resp = h.do(request{method: http.MethodGet, path: path, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "restored membership")
	resp.into(t, &choices)
	if len(choices.SelectedRepositoryIDs) != 0 {
		t.Fatal("restoring membership resurrected connection consent")
	}
	h.do(request{method: http.MethodDelete, path: "/api/v1/auth/mcp-connections/" + gs.Items[0].ID, cookie: cookie}).mustStatus(t, http.StatusNoContent, "revoke connection")
	h.do(request{method: http.MethodGet, path: path, cookie: cookie}).mustStatus(t, http.StatusNotFound, "revoked connection choices")
}

func TestAIContextSetupRequiresReviewAndRecoversTheSamePRWithoutSourceGrants(t *testing.T) {
	h, inst, operator := migrationHarness(t)
	_, admin := h.user("setup-admin", store.RoleAdmin)
	response := h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/discovery?installation_id=" + inst.ID, cookie: admin})
	var discovery controller.AIContextDiscovery
	response.into(t, &discovery)
	selected := discovery.Repositories[0]
	draft := store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: inst.ID, RepositoryID: selected.ID}, FullName: selected.FullName, Config: aicontext.DefaultConfig(selected.DefaultBranch)}
	if err := h.st.CreateAIContextRepository(h.ctx, &draft); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/ai-context/repositories/" + draft.ID + "/setup"
	h.do(request{method: http.MethodGet, path: path, cookie: operator}).mustStatus(t, http.StatusNotFound, "operator preview")
	h.do(request{method: http.MethodPost, path: path, cookie: operator, body: controller.AIContextSetupApproval{}}).mustStatus(t, http.StatusNotFound, "operator setup")
	response = h.do(request{method: http.MethodGet, path: path, cookie: admin})
	response.mustStatus(t, http.StatusOK, "preview")
	var plan controller.AIContextSetupPreview
	response.into(t, &plan)
	if len(plan.Files) != 7 || plan.Setup != nil {
		t.Fatalf("unexpected preview: %+v", plan)
	}
	guide := findSetupContent(t, &plan, aicontext.ContextGuidePath)
	entry := findSetupContent(t, &plan, "AGENTS.md")
	if !strings.Contains(guide, "context_read") || !strings.Contains(guide, "manifest.json") || !strings.Contains(entry, aicontext.ContextGuidePath) || strings.Contains(entry, "snapshot.json") {
		t.Fatal("setup preview did not separate the shared guide from the short entry point")
	}
	h.do(request{method: http.MethodPost, path: path, cookie: admin, body: controller.AIContextSetupApproval{Revision: plan.Revision, PlanHash: "wrong"}}).mustStatus(t, http.StatusConflict, "wrong approval")
	if len(h.gh.Branches(draft.FullName)) != 1 {
		t.Fatal("preview or invalid approval wrote a branch")
	}
	approval := controller.AIContextSetupApproval{Revision: plan.Revision, PlanHash: plan.PlanHash}
	response = h.do(request{method: http.MethodPost, path: path, cookie: admin, body: approval})
	response.mustStatus(t, http.StatusOK, "create PR")
	response.into(t, &plan)
	if plan.Setup == nil || plan.Setup.State != "awaiting_merge" || plan.Setup.PRNumber <= 0 {
		t.Fatalf("PR identity missing: %+v", plan)
	}
	number := plan.Setup.PRNumber
	response = h.do(request{method: http.MethodPost, path: path, cookie: admin, body: approval})
	response.mustStatus(t, http.StatusOK, "recover PR")
	response.into(t, &plan)
	if plan.Setup.PRNumber != number || len(h.gh.Branches(draft.FullName)) != 2 {
		t.Fatal("retry created another PR")
	}
	saved, err := h.st.GetAIContextRepository(h.ctx, draft.ID)
	if err != nil || saved.Available {
		t.Fatal("opening a PR enabled source access")
	}
	h.do(request{method: http.MethodPatch, path: "/api/v1/ai-context/repositories/" + draft.ID + "/config", cookie: admin, body: map[string]any{"revision": draft.Revision, "config": draft.Config}}).mustStatus(t, http.StatusConflict, "frozen setup configuration")
	h.gh.SetPermissions(map[string]string{"contents": "read", "metadata": "read"})
	h.do(request{method: http.MethodPost, path: path, cookie: admin, body: approval}).mustStatus(t, http.StatusUnprocessableEntity, "live write permission removed")
}

func TestAIContextSetupRefusesChangedSourceAndUserOwnedFiles(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	_, admin := h.user("setup-admin", store.RoleAdmin)
	client, err := h.ctrl.ClientFor(h.ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := client.ListRepositories(h.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	selected := repos[0]
	draft := store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: inst.ID, RepositoryID: selected.ID}, FullName: selected.FullName, Config: aicontext.DefaultConfig(selected.DefaultBranch)}
	if err := h.st.CreateAIContextRepository(h.ctx, &draft); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/ai-context/repositories/" + draft.ID + "/setup"
	response := h.do(request{method: http.MethodGet, path: path, cookie: admin})
	response.mustStatus(t, http.StatusOK, "preview")
	var plan controller.AIContextSetupPreview
	response.into(t, &plan)
	h.gh.AddFile(draft.FullName, "new.go", "package new")
	h.do(request{method: http.MethodPost, path: path, cookie: admin, body: controller.AIContextSetupApproval{Revision: plan.Revision, PlanHash: plan.PlanHash}}).mustStatus(t, http.StatusConflict, "stale reviewed source")
	h.gh.AddFile(draft.FullName, aicontext.ConfigPath, "{\"custom\":true}")
	h.do(request{method: http.MethodGet, path: path, cookie: admin}).mustStatus(t, http.StatusUnprocessableEntity, "custom configuration")
	if len(h.gh.Branches(draft.FullName)) != 1 {
		t.Fatal("refusal wrote a branch")
	}
}
