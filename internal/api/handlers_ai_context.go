package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

func (s *Server) handleDiscoverAIContext(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("installation_id")
	if id == "" {
		unprocessable(w, "choose the GitHub installation to discover repositories", []fieldError{{"installation_id", "an installation is required"}})
		return
	}
	if !s.contextInstallationAccess(w, r, id) {
		return
	}
	out, err := s.ctrl.DiscoverAIContext(r.Context(), id)
	if err != nil {
		s.fail(w, r, "discovering AI context repositories", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateAIContextDraft(w http.ResponseWriter, r *http.Request) {
	var req controller.AIContextDraftRequest
	if !decode(w, r, &req) {
		return
	}
	if !s.contextInstallationAccess(w, r, req.InstallationID) {
		return
	}
	out, err := s.ctrl.CreateAIContextDraft(r.Context(), req)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			unprocessable(w, err.Error(), nil)
			return
		}
		s.fail(w, r, "preparing the AI context draft", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context.draft", "ai_context", out.ID, map[string]any{"repository_id": out.Key.RepositoryID})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleListAIContextRepositories(w http.ResponseWriter, r *http.Request) {
	limit := clamp(queryInt(r, "limit", 50), 1, 100)
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	if len(r.URL.Query().Get("q")) > 200 {
		unprocessable(w, "use a search query of at most 200 characters", nil)
		return
	}
	var rows []store.AIContextRepository
	var total int
	var err error
	if installation := r.URL.Query().Get("installation_id"); contextAdmin(r) {
		rows, total, err = s.ctrl.Store().ListAIContextRepositoriesFiltered(r.Context(), limit, offset, installation, r.URL.Query().Get("q"))
	} else {
		// An owner lists across their own installations only; a filter naming
		// someone else's simply matches nothing.
		actor := Identity(r.Context())
		if actor == nil || actor.Kind != auth.KindUser || actor.UserID == "" {
			notFound(w, "installation or repository is unavailable to this account")
			return
		}
		rows, total, err = s.ctrl.Store().ListAIContextRepositoriesOwnedBy(r.Context(), limit, offset, installation, r.URL.Query().Get("q"), actor.UserID)
	}
	if err != nil {
		s.fail(w, r, "listing AI context repositories", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleUpdateAIContextConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Revision int64            `json:"revision"`
		Config   aicontext.Config `json:"config"`
	}
	if !decode(w, r, &req) {
		return
	}
	// The upload address is the controller's to set, never the caller's: it
	// is where the workflow's OIDC token is aimed, so a client cannot point a
	// repository's uploads somewhere else.
	req.Config.UploadURL = ""
	if req.Config.Destination == aicontext.Zoomies {
		req.Config.UploadURL = aicontext.UploadURLFor(s.cfg().Server.ExternalURL)
	}
	if err := req.Config.Validate(); err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	id := chiURLParam(r, "id")
	existing, ok := s.contextRepositoryAccess(w, r, id)
	if !ok {
		return
	}
	// Initial setup is anchored to the discovered default branch. Supporting
	// arbitrary branches needs its own live ref verification, not a text box.
	if req.Config.SourceBranch != existing.Config.SourceBranch {
		unprocessable(w, "keep the discovered source branch; changing it requires repository verification", nil)
		return
	}
	if err := s.ctrl.Store().SaveAIContextConfig(r.Context(), id, req.Revision, req.Config); err != nil {
		s.fail(w, r, "saving AI context configuration", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context.configure", "ai_context", id, map[string]any{"revision": req.Revision + 1})
	existing, err := s.ctrl.Store().GetAIContextRepository(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading AI context configuration", err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleGetAIContextMembers(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if _, ok := s.contextRepositoryAccess(w, r, id); !ok {
		return
	}
	ids, err := s.ctrl.Store().AIContextMembers(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading AI context members", err)
		return
	}
	if !contextAdmin(r) {
		// An owner sees only whether they themselves are a reader; other
		// people's membership is the administrator's to show.
		own := []string{}
		for _, member := range ids {
			if member == Identity(r.Context()).UserID {
				own = append(own, member)
			}
		}
		ids = own
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_ids": ids})
}

func (s *Server) handlePutAIContextMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserIDs *[]string `json:"user_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.UserIDs == nil {
		unprocessable(w, "provide user_ids explicitly; use an empty array to remove all readers", nil)
		return
	}
	if !uniqueSelection(w, *in.UserIDs, 200, "context readers") {
		return
	}
	id := chiURLParam(r, "id")
	if _, ok := s.contextRepositoryAccess(w, r, id); !ok {
		return
	}
	selection := *in.UserIDs
	if !contextAdmin(r) {
		// Owning an installation lets a person read their own repositories;
		// choosing other readers stays with administrators, who can see the
		// user directory. Everyone else's membership is left as it is.
		self := Identity(r.Context()).UserID
		for _, user := range selection {
			if user != self {
				unprocessable(w, "installation owners can add or remove only themselves as readers; ask an administrator to assign others", nil)
				return
			}
		}
		current, err := s.ctrl.Store().AIContextMembers(r.Context(), id)
		if err != nil {
			s.fail(w, r, "reading AI context members", err)
			return
		}
		merged := []string{}
		for _, member := range current {
			if member != self {
				merged = append(merged, member)
			}
		}
		selection = append(merged, selection...)
	}
	if err := s.ctrl.Store().ReplaceAIContextMembers(r.Context(), id, selection); err != nil {
		s.fail(w, r, "saving AI context members", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context.members", "ai_context", id, map[string]any{"members": len(selection)})
	if !contextAdmin(r) {
		selection = *in.UserIDs
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_ids": selection})
}

func uniqueSelection(w http.ResponseWriter, ids []string, maximum int, label string) bool {
	if len(ids) > maximum {
		unprocessable(w, fmt.Sprintf("select at most %d %s", maximum, label), nil)
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || len(id) > 100 || seen[id] {
			unprocessable(w, "select each "+label+" once using a known ID", nil)
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *Server) handleGetOwnContextSelection(w http.ResponseWriter, r *http.Request) {
	uid := connectionOwner(w, r)
	if uid == "" {
		return
	}
	limit := clamp(queryInt(r, "limit", 50), 1, 100)
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	out, err := s.ctrl.Store().AIContextConnectionChoices(r.Context(), chiURLParam(r, "id"), uid, limit, offset)
	if err != nil {
		s.fail(w, r, "reading connection source consent", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutOwnContextSelection(w http.ResponseWriter, r *http.Request) {
	if connectionOwner(w, r) == "" {
		return
	}
	var in struct {
		RepositoryIDs *[]string `json:"repository_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.RepositoryIDs == nil {
		unprocessable(w, "provide repository_ids explicitly; use an empty array to remove all source access", nil)
		return
	}
	if !uniqueSelection(w, *in.RepositoryIDs, 100, "source repositories") {
		return
	}
	if err := s.auth.SetContextConnectionRepositories(r.Context(), Identity(r.Context()), chiURLParam(r, "id"), *in.RepositoryIDs); err != nil {
		s.fail(w, r, "saving connection source consent", err)
		return
	}
	noContent(w)
}

func (s *Server) handleGetAIContextRepository(w http.ResponseWriter, r *http.Request) {
	out, ok := s.contextRepositoryAccess(w, r, chiURLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleFindAIContextDraft(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("repository_id"), 10, 64)
	if err != nil || id <= 0 || r.URL.Query().Get("installation_id") == "" {
		unprocessable(w, "choose a known installation and repository ID", nil)
		return
	}
	if !s.contextInstallationAccess(w, r, r.URL.Query().Get("installation_id")) {
		return
	}
	out, err := s.ctrl.FindAIContextDraft(r.Context(), r.URL.Query().Get("installation_id"), id)
	if err != nil {
		s.fail(w, r, "finding an AI context draft", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleListReaderAIContext(w http.ResponseWriter, r *http.Request) {
	id := Identity(r.Context())
	if id.UserID == "" || (id.Kind != auth.KindUser && id.Kind != auth.KindToken && id.Kind != auth.KindConnection) {
		forbidden(w, "source repositories require explicit membership for an account")
		return
	}
	limit := clamp(queryInt(r, "limit", 50), 1, 100)
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	if len(r.URL.Query().Get("q")) > 200 {
		unprocessable(w, "use a search query of at most 200 characters", nil)
		return
	}
	var rows []store.AIContextChoice
	var total int
	var err error
	if id.Kind == auth.KindConnection {
		rows, total, err = s.ctrl.Store().ListAIContextGrantedRepositories(r.Context(), id.ID, id.UserID, limit, offset, r.URL.Query().Get("q"))
	} else {
		rows, total, err = s.ctrl.Store().ListAIContextReaderRepositories(r.Context(), id.UserID, limit, offset, r.URL.Query().Get("q"))
	}
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		s.fail(w, r, "listing your AI context repositories", err)
		return
	}
	type readerInstructions struct {
		store.AIContextChoice
		Instructions  string `json:"instructions"`
		BadgeMarkdown string `json:"badge_markdown"`
	}
	items := make([]readerInstructions, 0, len(rows))
	for _, row := range rows {
		allowed, err := s.auth.ContextAccess(r.Context(), id, row.ID)
		if err != nil {
			s.fail(w, r, "checking instruction access", err)
			return
		}
		if !allowed {
			continue
		}
		repository, err := s.ctrl.Store().GetAIContextRepository(r.Context(), row.ID)
		if err != nil {
			s.fail(w, r, "reading assistant instructions", err)
			return
		}
		items = append(items, readerInstructions{AIContextChoice: row, Instructions: aicontext.AssistantInstructions(repository.Key, repository.FullName, repository.Config), BadgeMarkdown: aicontext.BadgeMarkdown(repository.Key, repository.FullName)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handlePreviewAIContextSetup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.contextRepositoryAccess(w, r, chiURLParam(r, "id")); !ok {
		return
	}
	out, err := s.ctrl.PreviewAIContextSetup(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			unprocessable(w, err.Error(), nil)
			return
		}
		s.fail(w, r, "previewing managed context setup", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateAIContextSetupPR(w http.ResponseWriter, r *http.Request) {
	var req controller.AIContextSetupApproval
	if !decode(w, r, &req) {
		return
	}
	id := chiURLParam(r, "id")
	if _, ok := s.contextRepositoryAccess(w, r, id); !ok {
		return
	}
	out, err := s.ctrl.CreateAIContextSetupPR(r.Context(), id, req)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			unprocessable(w, err.Error(), nil)
			return
		}
		s.fail(w, r, "creating managed context setup PR", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context.setup", "ai_context", id, map[string]any{"pr_number": out.Setup.PRNumber, "plan_hash": out.PlanHash})
	writeJSON(w, http.StatusOK, out)
}

// Recheck reports persisted verification state even if GitHub is unavailable.
// It checks existing output; it never triggers a workflow or writes a branch.
func (s *Server) handleRecheckAIContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.contextRepositoryAccess(w, r, id); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	_ = s.ctrl.RefreshAIContext(ctx, id)
	out, err := s.ctrl.Store().GetAIContextRepository(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading context verification", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context.recheck", "ai_context", id, map[string]any{"available": out.Available})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAIContextMaintenance(w http.ResponseWriter, r *http.Request) {
	var req controller.AIContextMaintenanceRequest
	if !decode(w, r, &req) {
		return
	}
	id := chiURLParam(r, "id")
	if _, ok := s.contextRepositoryAccess(w, r, id); !ok {
		return
	}
	var out *controller.AIContextSetupPreview
	var err error
	if strings.HasSuffix(r.URL.Path, "/apply") {
		out, err = s.ctrl.ApplyAIContextMaintenance(r.Context(), id, req)
	} else {
		out, err = s.ctrl.PreviewAIContextMaintenance(r.Context(), id, req)
	}
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInput) {
			unprocessable(w, err.Error(), nil)
			return
		}
		s.fail(w, r, "maintaining AI Context", err)
		return
	}
	if out.Setup != nil {
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context."+req.Mode, "ai_context", id, map[string]any{"plan_hash": out.PlanHash})
	}
	writeJSON(w, http.StatusOK, out)
}
