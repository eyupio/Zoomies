package api

import (
	"errors"
	"net/http"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// contextAdmin reports whether the caller holds the administrator's
// context.configure authority, which reaches every installation.
func contextAdmin(r *http.Request) bool {
	return auth.Allowed(Identity(r.Context()), auth.ActionContextConfigure)
}

// contextInstallationAccess is the per-installation half of the gate on
// configuring AI Context. An administrator reaches every installation; anyone
// else must be a signed-in person who owns this one. Tokens and OAuth
// connections never qualify, so an agent cannot enable repositories on its
// owner's behalf. Refusals are a 404 so the answer does not reveal which
// installations exist.
func (s *Server) contextInstallationAccess(w http.ResponseWriter, r *http.Request, installationID string) bool {
	if installationID != "" && contextAdmin(r) {
		return true
	}
	actor := Identity(r.Context())
	if installationID != "" && actor != nil && actor.Kind == auth.KindUser && actor.UserID != "" {
		owns, err := s.ctrl.Store().UserOwnsAIContextInstallation(r.Context(), installationID, actor.UserID)
		if err != nil {
			s.internal(w, r, "checking installation ownership", err)
			return false
		}
		if owns {
			return true
		}
	}
	notFound(w, "installation or repository is unavailable to this account")
	return false
}

// contextRepositoryAccess loads a draft or enabled repository and applies the
// installation gate to the installation it belongs to. A missing repository
// and an unowned one look the same to a non-administrator.
func (s *Server) contextRepositoryAccess(w http.ResponseWriter, r *http.Request, id string) (*store.AIContextRepository, bool) {
	repository, err := s.ctrl.Store().GetAIContextRepository(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && !contextAdmin(r) {
			notFound(w, "installation or repository is unavailable to this account")
			return nil, false
		}
		s.fail(w, r, "finding the AI context repository", err)
		return nil, false
	}
	if !s.contextInstallationAccess(w, r, repository.Key.InstallationID) {
		return nil, false
	}
	return repository, true
}

// handleListContextInstallations returns the installations the caller may
// enable repositories for, with nothing beyond a label.
func (s *Server) handleListContextInstallations(w http.ResponseWriter, r *http.Request) {
	var out []store.AIContextInstallationRef
	var err error
	if contextAdmin(r) {
		out, err = s.ctrl.Store().AllAIContextInstallations(r.Context())
	} else {
		actor := Identity(r.Context())
		if actor == nil || actor.Kind != auth.KindUser || actor.UserID == "" {
			out = []store.AIContextInstallationRef{}
		} else {
			out, err = s.ctrl.Store().AIContextOwnedInstallations(r.Context(), actor.UserID)
		}
	}
	if err != nil {
		s.fail(w, r, "listing manageable installations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGetContextInstallationOwners(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if _, err := s.ctrl.Store().GetInstallation(r.Context(), id); err != nil {
		s.fail(w, r, "finding the installation", err)
		return
	}
	ids, err := s.ctrl.Store().AIContextInstallationOwners(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading installation owners", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_ids": ids})
}

// handlePutContextInstallationOwners is an administrator's explicit delegation.
// Owning an installation lets a person enable its repositories; it does not make
// them a reader and does not grant any connection consent.
func (s *Server) handlePutContextInstallationOwners(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserIDs *[]string `json:"user_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.UserIDs == nil {
		unprocessable(w, "provide user_ids explicitly; use an empty array to remove every owner", nil)
		return
	}
	if !uniqueSelection(w, *in.UserIDs, store.MaxContextInstallationOwners, "installation owners") {
		return
	}
	id := chiURLParam(r, "id")
	if err := s.ctrl.Store().ReplaceAIContextInstallationOwners(r.Context(), id, *in.UserIDs); err != nil {
		s.fail(w, r, "saving installation owners", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "context.owners", "installation", id, map[string]any{"owners": len(*in.UserIDs)})
	writeJSON(w, http.StatusOK, map[string]any{"user_ids": *in.UserIDs})
}
