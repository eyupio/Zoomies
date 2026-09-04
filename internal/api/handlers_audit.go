package api

import (
	"net/http"

	"github.com/eyupio/zoomies/internal/store"
)

// handleListAudit answers GET /api/v1/audit.
//
// The rows are returned as the store holds them: the before/after documents
// were redacted when they were written, so nothing here has to remember to do
// it again on the way out.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	filter := store.AuditFilter{
		ActorIDs:    queryList(r, "actor_id"),
		Actions:     queryList(r, "action"),
		TargetKinds: queryList(r, "target_kind"),
		TargetID:    r.URL.Query().Get("target_id"),
		Search:      r.URL.Query().Get("q"),
	}
	var err error
	if filter.Since, err = queryTime(r, "since"); err != nil {
		badRequestField(w, "since", err.Error())
		return
	}
	if filter.Until, err = queryTime(r, "until"); err != nil {
		badRequestField(w, "until", err.Error())
		return
	}

	p := parsePage(r)
	events, total, err := s.ctrl.Store().ListAudit(r.Context(), filter, p)
	if err != nil {
		s.internal(w, r, "reading the audit log", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(events, total, p))
}

// handleAuditActions lists the distinct action names, for the filter menu.
func (s *Server) handleAuditActions(w http.ResponseWriter, r *http.Request) {
	actions, err := s.ctrl.Store().AuditActions(r.Context())
	if err != nil {
		s.internal(w, r, "reading the audit log's action names", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(actions))
}
