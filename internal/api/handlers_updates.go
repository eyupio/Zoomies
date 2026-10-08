package api

import (
	"net/http"

	"github.com/eyupio/zoomies/internal/controller"
)

// The update routes are transport. What an update would take, and why, is the
// controller's to say, and the handler renders it.

// updatesResponse is GET /updates, and the payload of updates.updated.
type updatesResponse = controller.UpdatesView

// handleGetUpdates answers GET /api/v1/updates.
//
// It answers 200 whatever the mode and whatever has been read: the page that
// reads it is the one an operator opens to find out why nothing is offered, and a
// refusal would leave it with nothing to say.
func (s *Server) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	status, err := s.ctrl.UpdatesView(r.Context())
	if err != nil {
		s.internal(w, r, "working out the update status", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
