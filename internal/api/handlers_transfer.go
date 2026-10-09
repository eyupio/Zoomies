package api

import "net/http"

func (s *Server) handleTransferProgress(w http.ResponseWriter, r *http.Request) {
	p, err := s.ctrl.TransferProgress(r.Context())
	if err != nil {
		s.internal(w, r, "reading transfer preparation", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePrepareTransfer(w http.ResponseWriter, r *http.Request) {
	p, err := s.ctrl.PrepareTransfer(r.Context())
	if err != nil {
		conflict(w, err.Error())
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "instance.prepare_transfer", "instance", "", nil)
	writeJSON(w, http.StatusAccepted, p)
}

func (s *Server) handleCancelTransfer(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.CancelTransfer(r.Context()); err != nil {
		conflict(w, err.Error())
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "instance.cancel_transfer", "instance", "", nil)
	w.WriteHeader(http.StatusNoContent)
}
