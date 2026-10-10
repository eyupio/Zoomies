package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

func (s *Server) handleEliRepairs(w http.ResponseWriter, r *http.Request) {
	var rows []*store.EliRepair
	var err error
	if Identity(r.Context()).Can(auth.ActionAssistantRead) {
		rows, err = s.ctrl.Store().ListEliRepairs(r.Context())
	} else {
		rows, err = s.ctrl.Store().ListEliRepairsForOwner(r.Context(), Identity(r.Context()).UserID)
	}
	if err != nil {
		s.internal(w, r, "listing Eli repairs", err)
		return
	}
	admin := Identity(r.Context()).Can(auth.ActionAssistantRead)
	out := []*store.EliRepair{}
	for _, row := range rows {
		if admin || row.UserID == Identity(r.Context()).UserID && row.UserID != "" {
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
func (s *Server) handleEliRepairSettings(w http.ResponseWriter, r *http.Request) {
	identities, err := s.ctrl.Store().EliIdentities(r.Context())
	if err != nil {
		s.internal(w, r, "reading Eli account links", err)
		return
	}
	admin := Identity(r.Context()).Can(auth.ActionAssistantRead)
	visible := []store.EliIdentity{}
	for _, id := range identities {
		if admin || id.UserID == Identity(r.Context()).UserID {
			visible = append(visible, id)
		}
	}
	policies := []store.EliRepairPolicy{}
	if admin {
		policies, err = s.ctrl.Store().EliRepairPolicies(r.Context())
		if err != nil {
			s.internal(w, r, "reading Eli policies", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": visible, "policies": policies})
}
func (s *Server) handleSetEliIdentity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID         string `json:"user_id"`
		GitHubLogin    string `json:"github_login"`
		InstallationID string `json:"installation_id"`
		Repo           string `json:"repo"`
	}
	if !decode(w, r, &in) {
		return
	}
	user, err := s.ctrl.Store().GetUser(r.Context(), in.UserID)
	if err != nil || user.Disabled {
		unprocessable(w, "choose an active Zoomies user", []fieldError{{"user_id", "an active account is required"}})
		return
	}
	if in.GitHubLogin == "" {
		if err := s.ctrl.Store().DeleteEliIdentity(r.Context(), in.UserID); err != nil {
			s.fail(w, r, "removing the GitHub account link", err)
			return
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.github_unlink", "user", in.UserID, nil)
		noContent(w)
		return
	}
	raw, err := s.ctrl.ClientFor(r.Context(), in.InstallationID)
	if err != nil {
		s.fail(w, r, "opening the GitHub installation", err)
		return
	}
	client, ok := raw.(github.RepairClient)
	if !ok {
		conflict(w, "this installation does not support Eli PR repairs")
		return
	}
	actor, err := client.RepairActor(r.Context(), strings.TrimSpace(in.Repo), strings.TrimSpace(in.GitHubLogin))
	if err != nil || actor.ID <= 0 || !actor.CanWrite {
		unprocessable(w, "the GitHub account must have write permission on this repository", []fieldError{{"github_login", "verify the user's GitHub account and repository access before linking it"}})
		return
	}
	id := store.EliIdentity{UserID: user.ID, GitHubUserID: actor.ID, GitHubLogin: actor.Login}
	if err := s.ctrl.Store().SetEliIdentity(r.Context(), id); err != nil {
		s.fail(w, r, "linking the GitHub account", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.github_link", "user", user.ID, id)
	writeJSON(w, http.StatusOK, id)
}
func (s *Server) handleSetEliPolicy(w http.ResponseWriter, r *http.Request) {
	var p store.EliRepairPolicy
	if !decode(w, r, &p) {
		return
	}
	p.Repo = strings.TrimSpace(p.Repo)
	_, _, kind := github.SplitTarget(p.Repo)
	if kind != store.TargetRepo || p.DailyLimit < 1 || p.DailyLimit > 50 {
		unprocessable(w, "choose a repository and a daily repair limit from 1 to 50", nil)
		return
	}
	provider, err := s.ctrl.Store().GetAssistantProvider(r.Context(), p.ProviderID)
	if err != nil || provider.OwnerID != "" || p.Enabled && !provider.Enabled {
		unprocessable(w, "choose an enabled installation provider for unattended repairs", []fieldError{{"provider_id", "a personal provider cannot pay for unattended repairs"}})
		return
	}
	inst, err := s.ctrl.Store().FindInstallationByTarget(r.Context(), p.Repo)
	if err != nil || inst.ID != p.InstallationID {
		unprocessable(w, "choose the installation that serves this repository", nil)
		return
	}
	if err := s.ctrl.Store().SetEliRepairPolicy(r.Context(), p); err != nil {
		s.fail(w, r, "saving the repository repair policy", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.repair_policy", "repository", p.Repo, p)
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) handleRequestEliRepair(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo        string `json:"repo"`
		PullNumber  int    `json:"pull_number"`
		Instruction string `json:"instruction"`
	}
	if !decode(w, r, &in) {
		return
	}
	owner := Identity(r.Context()).UserID
	if owner == "" {
		conflict(w, "sign in with a personal account to request a PR repair")
		return
	}
	row, err := s.ctrl.RequestEliRepair(r.Context(), owner, in.Repo, in.PullNumber, in.Instruction, s.isAssistantAdmin(r))
	switch {
	case errors.Is(err, controller.ErrAssistantSubscriptionRestricted):
		forbidden(w, err.Error())
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "no enabled repair policy or linked GitHub account was found")
	case errors.Is(err, store.ErrConflict), errors.Is(err, controller.ErrAssistantNoModel):
		conflict(w, "the personal provider is missing, repository access is unavailable or the repair budget is reached")
	case err != nil:
		s.internal(w, r, "requesting the PR repair", err)
	default:
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.repair_request", "eli_repair", row.ID, map[string]any{"repo": row.Repo, "pull_number": row.PullNumber})
		writeJSON(w, http.StatusAccepted, row)
	}
}

func (s *Server) handleEliRepairConsent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GitHubUserID int64 `json:"github_user_id"`
		Enabled      bool  `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	owner := Identity(r.Context()).UserID
	if err := s.ctrl.Store().ConfirmEliIdentity(r.Context(), owner, in.GitHubUserID, in.Enabled); err != nil {
		s.fail(w, r, "confirming the personal GitHub account link", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.github_consent", "user", owner, in)
	noContent(w)
}
