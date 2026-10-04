package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// A source reader publishes and reads notes; everyone else sees neither, and a
// token publishes only when it was given the scope to.
func TestReadersPublishAndReadVersionedNotesAndNobodyElseSeesThem(t *testing.T) {
	h, draft, owner, cookie := sourceHarness(t)
	base := "/api/v1/ai-context/source/" + draft.ID + "/notes"
	note := map[string]any{"slug": "auth-review", "kind": "report", "title": "Authentication review", "body": "# Findings\n\n<script>alert(1)</script>"}

	r := h.do(request{method: http.MethodPost, path: base, cookie: cookie, body: note})
	r.mustStatus(t, http.StatusCreated, "a reader publishing")
	var published store.AIContextArtifact
	r.into(t, &published)
	fresh, err := h.st.GetAIContextFreshness(h.ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Version != 1 || published.ViaKind != "user" || published.SourceCommit != fresh.PublishedCommit || published.SourceCommit == "" {
		t.Fatalf("published = %+v; want version 1, via user, at the verified commit %s", published, fresh.PublishedCommit)
	}

	note["title"] = "Authentication review, revised"
	h.do(request{method: http.MethodPost, path: base, cookie: cookie, body: note}).mustStatus(t, http.StatusCreated, "a second version")
	var page struct {
		Items []store.AIContextArtifact `json:"items"`
		Total int                       `json:"total"`
	}
	r = h.do(request{method: http.MethodGet, path: base, cookie: cookie})
	r.mustStatus(t, http.StatusOK, "listing notes")
	r.into(t, &page)
	if page.Total != 1 || page.Items[0].Version != 2 || page.Items[0].Versions != 2 || page.Items[0].Body != "" {
		t.Fatalf("listing = %+v", page)
	}
	var first store.AIContextArtifact
	r = h.do(request{method: http.MethodGet, path: base + "/auth-review?version=1", cookie: cookie})
	r.mustStatus(t, http.StatusOK, "reading version 1")
	r.into(t, &first)
	if first.Title != "Authentication review" || first.Body != note["body"] {
		t.Fatalf("version 1 = %+v", first)
	}

	note["kind"] = "essay"
	h.do(request{method: http.MethodPost, path: base, cookie: cookie, body: note}).mustStatus(t, http.StatusUnprocessableEntity, "an unknown kind")
	note["kind"] = "report"

	_, outsider := h.user("notes-outsider", store.RoleAdmin)
	h.do(request{method: http.MethodGet, path: base, cookie: outsider}).mustStatus(t, http.StatusNotFound, "a non-member listing")
	h.do(request{method: http.MethodPost, path: base, cookie: outsider, body: note}).mustStatus(t, http.StatusNotFound, "a non-member publishing")

	_, reading, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{Name: "reader-only", Role: store.RoleViewer, UserID: owner.ID, Scopes: []string{"context:read"}})
	if err != nil {
		t.Fatal(err)
	}
	h.do(request{method: http.MethodGet, path: base, token: reading}).mustStatus(t, http.StatusOK, "a read-scoped token listing")
	h.do(request{method: http.MethodPost, path: base, token: reading, body: note}).mustStatus(t, http.StatusForbidden, "a read-scoped token publishing")

	_, writing, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{Name: "ci-reviewer", Role: store.RoleViewer, UserID: owner.ID, Scopes: []string{"context:publish"}})
	if err != nil {
		t.Fatal(err)
	}
	r = h.do(request{method: http.MethodPost, path: base, token: writing, body: note})
	r.mustStatus(t, http.StatusCreated, "a publish-scoped token")
	r.into(t, &published)
	if published.ViaKind != "token" || published.ViaName != "ci-reviewer" {
		t.Fatalf("token note attributed as %s %q", published.ViaKind, published.ViaName)
	}
}

// An MCP connection allowed to read a repository is not thereby allowed to
// write about it. Its owner's separate publish consent is what lets it, and
// the note says which connection wrote it.
func TestAConnectionPublishesNotesOnlyWithItsOwnersPublishConsent(t *testing.T) {
	h, draft, owner, cookie := sourceHarness(t)
	h.cfg.Server.ExternalURL = oauthBase
	client := h.registerClient(claudeReturn)
	req := requestFrom(t, h.authorize(client, claudeReturn, nil))
	pair := pairFrom(t, h.exchange(client, h.approve(cookie, req, store.RoleViewer), nil, nil))
	grants, err := h.st.ListOAuthGrants(h.ctx, owner.ID)
	if err != nil || len(grants) != 1 {
		t.Fatal("grant", err)
	}
	publish := map[string]any{"repository_id": draft.ID, "slug": "plan", "kind": "plan", "title": "Refactor plan", "body": "1. Split the handler."}

	// Read consent only.
	h.do(request{method: http.MethodPut, path: "/api/v1/auth/mcp-connections/" + grants[0].ID + "/repositories", cookie: cookie,
		body: map[string]any{"repository_ids": []string{draft.ID}}}).mustStatus(t, http.StatusNoContent, "read consent")
	if result := h.mcpTool(pair.AccessToken, "context_publish", publish); !result.IsError || !strings.Contains(resultText(result), "Source access") {
		t.Fatal("a connection with read consent alone published, or was not told where to ask", resultText(result))
	}

	// Publishing to a repository it may not read is refused outright.
	h.do(request{method: http.MethodPut, path: "/api/v1/auth/mcp-connections/" + grants[0].ID + "/repositories", cookie: cookie,
		body: map[string]any{"repository_ids": []string{}, "publish_repository_ids": []string{draft.ID}}}).mustStatus(t, http.StatusUnprocessableEntity, "publish without read")

	h.do(request{method: http.MethodPut, path: "/api/v1/auth/mcp-connections/" + grants[0].ID + "/repositories", cookie: cookie,
		body: map[string]any{"repository_ids": []string{draft.ID}, "publish_repository_ids": []string{draft.ID}}}).mustStatus(t, http.StatusNoContent, "publish consent")
	result := h.mcpTool(pair.AccessToken, "context_publish", publish)
	if result.IsError {
		t.Fatal("publish with consent", resultText(result))
	}
	if strings.Contains(resultText(result), "Split the handler") {
		t.Fatal("the reply echoed the body back")
	}
	note, err := h.st.GetAIContextArtifact(h.ctx, draft.ID, "plan", 0)
	if err != nil || note.ViaKind != "connection" || note.ViaName == "" || note.AuthorName == "" {
		t.Fatalf("connection note attributed as %+v, %v", note, err)
	}
	listed := h.mcpTool(pair.AccessToken, "context_notes", map[string]any{"repository_id": draft.ID})
	if listed.IsError || !strings.Contains(resultText(listed), "Refactor plan") {
		t.Fatal("listing notes over MCP", resultText(listed))
	}
	read := h.mcpTool(pair.AccessToken, "context_notes", map[string]any{"repository_id": draft.ID, "slug": "plan"})
	if read.IsError || !strings.Contains(resultText(read), "Split the handler") {
		t.Fatal("reading a note over MCP", resultText(read))
	}

	// Saving read consent from a client that knows nothing of publishing
	// keeps the publish consent; an explicit empty list removes it.
	h.do(request{method: http.MethodPut, path: "/api/v1/auth/mcp-connections/" + grants[0].ID + "/repositories", cookie: cookie,
		body: map[string]any{"repository_ids": []string{draft.ID}}}).mustStatus(t, http.StatusNoContent, "read-only save")
	if result := h.mcpTool(pair.AccessToken, "context_publish", publish); result.IsError {
		t.Fatal("a read-only save dropped publish consent", resultText(result))
	}
	h.do(request{method: http.MethodPut, path: "/api/v1/auth/mcp-connections/" + grants[0].ID + "/repositories", cookie: cookie,
		body: map[string]any{"repository_ids": []string{draft.ID}, "publish_repository_ids": []string{}}}).mustStatus(t, http.StatusNoContent, "clearing publish consent")
	if result := h.mcpTool(pair.AccessToken, "context_publish", publish); !result.IsError {
		t.Fatal("cleared publish consent still published")
	}
}
