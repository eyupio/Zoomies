package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
)

func (s *Server) contextSourceAccess(w http.ResponseWriter, r *http.Request, id string) bool {
	actor, err := s.resolveIdentity(r)
	if err != nil {
		if errors.Is(err, auth.ErrAuthBackend) {
			s.internal(w, r, "checking source credentials", err)
			return false
		}
		unauthorized(w, "source credentials are no longer valid")
		return false
	}
	if !auth.Allowed(actor, auth.ActionContextRead) {
		forbidden(w, "source credentials no longer allow context.read")
		return false
	}
	allowed, err := s.auth.ContextAccess(r.Context(), actor, id)
	if err != nil {
		s.internal(w, r, "checking source access", err)
		return false
	}
	if !allowed {
		if actor.UserID == "" {
			forbidden(w, "source access requires an owned account")
			return false
		}
		notFound(w, "source repository is unavailable to this credential")
		return false
	}
	return true
}

func (s *Server) handleAIContextSource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := chiURLParam(r, "id")
	if !s.contextSourceAccess(w, r, id) {
		return
	}
	q := r.URL.Query()
	number := func(name string, fallback, min, max int) (int, bool) {
		text := q.Get(name)
		if text == "" {
			return fallback, true
		}
		n, err := strconv.Atoi(text)
		if err != nil || n < min || n > max {
			unprocessable(w, "choose a valid "+name, nil)
			return 0, false
		}
		return n, true
	}
	budget, ok := number("budget", 8000, 1024, aicontext.MaxResponseBytes)
	if !ok {
		return
	}
	offset, ok := number("offset", 0, 0, 1000000)
	if !ok {
		return
	}
	maxLimit := 100
	if strings.HasSuffix(r.URL.Path, "/search") {
		maxLimit = 12
	}
	limit, ok := number("limit", 12, 1, maxLimit)
	if !ok {
		return
	}
	if offset > 0 && q.Get("commit") == "" {
		unprocessable(w, "pin the returned commit when continuing a page", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	snapshot, fresh, err := s.ctrl.VerifiedAIContextSnapshot(ctx, id)
	// Revocation during network verification must close the response as well.
	if !s.contextSourceAccess(w, r, id) {
		return
	}
	if err != nil {
		conflict(w, "current source could not be verified; ask an administrator to recheck AI Context")
		return
	}
	if expected := q.Get("commit"); expected != "" && expected != snapshot.Manifest.SourceCommit {
		conflict(w, "source changed; restart retrieval using the current commit")
		return
	}
	var body []byte
	switch r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] {
	case "overview":
		body, err = snapshot.FilePage(fresh.Digest, offset, limit, budget)
	case "read":
		body, err = snapshot.ReadPage(fresh.Digest, []string{q.Get("path")}, offset, budget)
	case "pack":
		body, err = snapshot.ReadPage(fresh.Digest, q["path"], offset, budget)
	case "search":
		body, err = snapshot.SearchResult(fresh.Digest, q.Get("query"), q.Get("prefix"), offset, limit, budget)
	default:
		notFound(w, "source operation")
		return
	}
	if err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	if !s.contextSourceAccess(w, r, id) {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
