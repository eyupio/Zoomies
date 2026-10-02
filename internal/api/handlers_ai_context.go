package api

import (
	"errors"
	"net/http"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
)

func (s *Server) handleDiscoverAIContext(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("installation_id")
	if id == "" {
		unprocessable(w, "choose the GitHub installation to discover repositories", []fieldError{{"installation_id", "an installation is required"}})
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
	rows, total, err := s.ctrl.Store().ListAIContextRepositories(r.Context(), limit, offset)
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
	if err := req.Config.Validate(); err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	id := chiURLParam(r, "id")
	existing, err := s.ctrl.Store().GetAIContextRepository(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the AI context draft", err)
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
	existing, err = s.ctrl.Store().GetAIContextRepository(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading AI context configuration", err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}
