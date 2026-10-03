package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// Owning an installation is delegated configuration authority over exactly that
// installation. These tests pin both halves: what an owner can now do, and the
// things that must stay out of reach (other people, tokens, and source itself).
func TestInstallationOwnersEnableTheirRepositoriesWithoutSourceAccess(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	owner, ownerCookie := h.user("context-owner", store.RoleViewer)
	_, outsiderCookie := h.user("context-outsider", store.RoleViewer)
	other, _ := h.user("context-other", store.RoleViewer)
	_, admin := h.user("context-owner-admin", store.RoleAdmin)
	root := "/api/v1/ai-context/"

	h.do(request{method: http.MethodGet, path: root + "discovery?installation_id=" + inst.ID, cookie: ownerCookie}).mustStatus(t, http.StatusNotFound, "owner before delegation")

	h.do(request{method: http.MethodPut, path: root + "installations/" + inst.ID + "/owners", cookie: ownerCookie,
		body: map[string]any{"user_ids": []string{owner.ID}}}).mustStatus(t, http.StatusForbidden, "owners cannot delegate to themselves")
	h.do(request{method: http.MethodPut, path: root + "installations/" + inst.ID + "/owners", cookie: admin,
		body: map[string]any{}}).mustStatus(t, http.StatusUnprocessableEntity, "missing owners must not mean remove all")
	h.do(request{method: http.MethodPut, path: root + "installations/" + inst.ID + "/owners", cookie: admin,
		body: map[string]any{"user_ids": []string{"missing"}}}).mustStatus(t, http.StatusNotFound, "unknown owner")
	h.do(request{method: http.MethodPut, path: root + "installations/" + inst.ID + "/owners", cookie: admin,
		body: map[string]any{"user_ids": []string{owner.ID}}}).mustStatus(t, http.StatusOK, "delegation")

	var installations struct {
		Items []store.AIContextInstallationRef `json:"items"`
	}
	r := h.do(request{method: http.MethodGet, path: root + "installations", cookie: ownerCookie})
	r.mustStatus(t, http.StatusOK, "owned installations")
	r.into(t, &installations)
	if len(installations.Items) != 1 || installations.Items[0].ID != inst.ID {
		t.Fatal("owner should see exactly their installation", installations.Items)
	}
	r = h.do(request{method: http.MethodGet, path: root + "installations", cookie: outsiderCookie})
	r.into(t, &installations)
	if len(installations.Items) != 0 {
		t.Fatal("a non-owner learned of an installation")
	}

	var discovery struct {
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	r = h.do(request{method: http.MethodGet, path: root + "discovery?installation_id=" + inst.ID, cookie: ownerCookie})
	r.mustStatus(t, http.StatusOK, "owner discovery")
	r.into(t, &discovery)
	if len(discovery.Repositories) == 0 {
		t.Fatal("no repositories discovered")
	}
	h.do(request{method: http.MethodGet, path: root + "discovery?installation_id=" + inst.ID, cookie: outsiderCookie}).mustStatus(t, http.StatusNotFound, "non-owner discovery")

	draftBody := map[string]any{"installation_id": inst.ID, "repository_id": discovery.Repositories[0].ID}
	h.do(request{method: http.MethodPost, path: root + "repositories", cookie: outsiderCookie, body: draftBody}).mustStatus(t, http.StatusNotFound, "non-owner draft")
	r = h.do(request{method: http.MethodPost, path: root + "repositories", cookie: ownerCookie, body: draftBody})
	r.mustStatus(t, http.StatusCreated, "owner draft")
	var draft store.AIContextRepository
	r.into(t, &draft)

	// Ownership is not membership: creating a draft grants nobody source.
	h.do(request{method: http.MethodGet, path: root + "source/" + draft.ID + "/overview", cookie: ownerCookie}).mustStatus(t, http.StatusNotFound, "owner has no source access")

	for _, path := range []string{"repositories/" + draft.ID, "repositories/" + draft.ID + "/members"} {
		h.do(request{method: http.MethodGet, path: root + path, cookie: ownerCookie}).mustStatus(t, http.StatusOK, "owner "+path)
		h.do(request{method: http.MethodGet, path: root + path, cookie: outsiderCookie}).mustStatus(t, http.StatusNotFound, "outsider "+path)
	}
	// The fake GitHub's loopback host fails the production template gate, so the
	// preview itself answers 422 here; what matters is who gets that far.
	setup := "repositories/" + draft.ID + "/setup"
	if got := h.do(request{method: http.MethodGet, path: root + setup, cookie: ownerCookie}); got.status == http.StatusNotFound || got.status == http.StatusForbidden {
		t.Fatal("owner was refused the setup preview", got.status)
	}
	h.do(request{method: http.MethodGet, path: root + setup, cookie: outsiderCookie}).mustStatus(t, http.StatusNotFound, "outsider setup")
	h.do(request{method: http.MethodGet, path: root + "repositories/missing", cookie: outsiderCookie}).mustStatus(t, http.StatusNotFound, "missing looks the same as unowned")

	var page struct {
		Total int `json:"total"`
	}
	r = h.do(request{method: http.MethodGet, path: root + "repositories", cookie: ownerCookie})
	r.into(t, &page)
	if page.Total != 1 {
		t.Fatal("owner list", page.Total)
	}
	r = h.do(request{method: http.MethodGet, path: root + "repositories?installation_id=" + inst.ID, cookie: outsiderCookie})
	r.mustStatus(t, http.StatusOK, "outsider list")
	r.into(t, &page)
	if page.Total != 0 {
		t.Fatal("outsider listed another installation's repositories")
	}

	// An owner may be their own reader, but cannot choose other people.
	h.do(request{method: http.MethodPut, path: root + "repositories/" + draft.ID + "/members", cookie: ownerCookie,
		body: map[string]any{"user_ids": []string{other.ID}}}).mustStatus(t, http.StatusUnprocessableEntity, "owner assigning someone else")
	if err := h.st.ReplaceAIContextMembers(h.ctx, draft.ID, []string{other.ID}); err != nil {
		t.Fatal(err)
	}
	h.do(request{method: http.MethodPut, path: root + "repositories/" + draft.ID + "/members", cookie: ownerCookie,
		body: map[string]any{"user_ids": []string{owner.ID}}}).mustStatus(t, http.StatusOK, "owner reading their own repository")
	members, _ := h.st.AIContextMembers(h.ctx, draft.ID)
	if len(members) != 2 {
		t.Fatal("an owner's save must keep the administrator's readers", members)
	}
	r = h.do(request{method: http.MethodGet, path: root + "repositories/" + draft.ID + "/members", cookie: ownerCookie})
	if strings.Contains(string(r.body), other.ID) {
		t.Fatal("an owner learned who else reads the repository")
	}
	h.do(request{method: http.MethodPut, path: root + "repositories/" + draft.ID + "/members", cookie: ownerCookie,
		body: map[string]any{"user_ids": []string{}}}).mustStatus(t, http.StatusOK, "owner leaving")
	members, _ = h.st.AIContextMembers(h.ctx, draft.ID)
	if len(members) != 1 || members[0] != other.ID {
		t.Fatal("leaving removed someone else", members)
	}

	// A bearer token never inherits its owner's delegation.
	_, token, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{Name: "owner-token", Role: store.RoleViewer, UserID: owner.ID, Scopes: []string{"context:manage"}})
	if err != nil {
		t.Fatal(err)
	}
	h.do(request{method: http.MethodGet, path: root + "repositories/" + draft.ID, token: token}).mustStatus(t, http.StatusNotFound, "owner's token")

	// Revocation is immediate.
	h.do(request{method: http.MethodPut, path: root + "installations/" + inst.ID + "/owners", cookie: admin,
		body: map[string]any{"user_ids": []string{}}}).mustStatus(t, http.StatusOK, "removing the owner")
	h.do(request{method: http.MethodGet, path: root + "repositories/" + draft.ID, cookie: ownerCookie}).mustStatus(t, http.StatusNotFound, "former owner")
}
