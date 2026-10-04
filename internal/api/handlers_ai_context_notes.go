package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// Notes are what an assistant writes about a repository: reports and plans,
// versioned and attributed. Reading them is the same gate as reading the
// source; writing them is a separate one (ContextPublishAccess), because a
// connection allowed to read must not thereby be allowed to write.

func (s *Server) handleListAIContextNotes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := chiURLParam(r, "id")
	if !s.contextSourceAccess(w, r, id) {
		return
	}
	limit := clamp(queryInt(r, "limit", 50), 1, 100)
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	items, total, err := s.ctrl.Store().ListAIContextArtifacts(r.Context(), id, limit, offset)
	if err != nil {
		s.fail(w, r, "listing notes", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleGetAIContextNote(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := chiURLParam(r, "id")
	if !s.contextSourceAccess(w, r, id) {
		return
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			unprocessable(w, "choose a version of 1 or more, or leave it out for the latest", nil)
			return
		}
		version = n
	}
	note, err := s.ctrl.Store().GetAIContextArtifact(r.Context(), id, chiURLParam(r, "slug"), version)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "no such note, or no such version of it")
			return
		}
		s.fail(w, r, "reading a note", err)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

func (s *Server) handlePublishAIContextNote(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := chiURLParam(r, "id")
	actor := Identity(r.Context())
	allowed, err := s.auth.ContextPublishAccess(r.Context(), actor, id)
	if err != nil {
		s.internal(w, r, "checking publish access", err)
		return
	}
	if !allowed {
		if actor == nil || actor.UserID == "" {
			forbidden(w, "publishing notes requires an owned account")
			return
		}
		if actor.Kind == auth.KindConnection {
			// The connection may well be allowed to read; it is not allowed
			// to write here, and its owner is the one who can change that.
			notFound(w, "this connection may not publish notes to that repository; its owner can allow it under Settings → MCP connections → Source access")
			return
		}
		notFound(w, "source repository is unavailable to this credential")
		return
	}
	var in struct {
		Slug  string `json:"slug"`
		Kind  string `json:"kind"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	user, err := s.ctrl.Store().GetUser(r.Context(), actor.UserID)
	if err != nil {
		s.fail(w, r, "finding the note's author", err)
		return
	}
	author := user.DisplayName
	if author == "" {
		author = user.Username
	}
	note := &store.AIContextArtifact{RepositoryID: id, Slug: in.Slug, Kind: in.Kind, Title: in.Title, Body: in.Body, AuthorName: author}
	switch actor.Kind {
	case auth.KindConnection:
		note.ViaKind = "connection"
		if _, client, ok := strings.Cut(actor.Name, " via "); ok {
			note.ViaName = client
		}
	case auth.KindToken:
		note.ViaKind, note.ViaName = "token", actor.Name
	default:
		note.ViaKind = "user"
	}
	if err := s.ctrl.Store().PublishAIContextArtifact(r.Context(), note, actor.UserID); err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidArtifact):
			unprocessable(w, err.Error(), nil)
		case errors.Is(err, store.ErrConflict):
			conflict(w, strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": "))
		default:
			s.fail(w, r, "publishing a note", err)
		}
		return
	}
	// The connection or token is named in the audit log as well as on the
	// note: an AI-written change is the person's responsibility, through it.
	s.auth.Auditor().Act(r.Context(), actor, "context.publish", "ai_context", id, map[string]any{"slug": note.Slug, "version": note.Version})
	writeJSON(w, http.StatusCreated, note)
}
