package api

import (
	"net/http"

	"github.com/eyupio/zoomies/internal/controller"
)

func (s *Server) handlePreviewKennelGuidance(w http.ResponseWriter, r *http.Request) {
	plan, err := s.ctrl.PreviewKennelGuidance(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.failKennel(w, r, "previewing agent guidance", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleCreateKennelGuidancePR(w http.ResponseWriter, r *http.Request) {
	var approval controller.KennelGuidanceApproval
	if !decode(w, r, &approval) {
		return
	}
	id := chiURLParam(r, "id")
	plan, err := s.ctrl.CreateKennelGuidancePR(r.Context(), id, approval)
	if err != nil {
		s.failKennel(w, r, "opening an agent guidance draft pull request", err)
		return
	}
	_ = s.auth.Auditor().Record(r.Context(), Identity(r.Context()), "kennel.guidance_pr", "kennel_repository", id, nil, map[string]any{"plan_hash": plan.PlanHash, "pull_request": plan.PullRequest.Number, "base_commit": plan.BaseCommit})
	writeJSON(w, http.StatusOK, plan)
}
