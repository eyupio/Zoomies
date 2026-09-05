package api

import (
	"net/http"
	"time"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// defaultStatsWindow matches the OpenAPI document's default for ?window=.
const defaultStatsWindow = time.Hour

// handleStats answers the Overview's cards: what the queue is doing, what the
// fleet is doing, and how long jobs are waiting.
//
// controller.Stats is already the response shape -- its JSON tags are the
// Stats schema -- so there is no translation layer here to fall out of step
// with the document.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	window, err := queryDuration(r, "window", defaultStatsWindow)
	if err != nil {
		badRequestField(w, "window", err.Error())
		return
	}
	stats, err := s.ctrl.Stats(r.Context(), window)
	if err != nil {
		s.internal(w, r, "computing fleet statistics", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleSamples returns the sparkline points.
//
// `since` wins over `window` when both are given, because a client that names
// an exact instant is resuming a chart it already has and re-sending points it
// has already drawn would make the line jump.
func (s *Server) handleSamples(w http.ResponseWriter, r *http.Request) {
	since, err := queryTime(r, "since")
	if err != nil {
		badRequestField(w, "since", err.Error())
		return
	}
	if since == nil {
		window, werr := queryDuration(r, "window", defaultStatsWindow)
		if werr != nil {
			badRequestField(w, "window", werr.Error())
			return
		}
		t := s.ctrl.Now().Add(-window)
		since = &t
	}

	samples, err := s.ctrl.Samples(r.Context(), *since)
	if err != nil {
		s.internal(w, r, "reading fleet samples", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(samples))
}

// handleProblems answers the UI's problems drawer.
//
// The body is controller.ProblemsView, which is also what a problems.updated
// frame carries, so the drawer cannot see two shapes for the same list.
func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	items, err := s.ctrl.Problems(r.Context())
	if err != nil {
		s.internal(w, r, "gathering the current problems", err)
		return
	}
	writeJSON(w, http.StatusOK, controller.NewProblemsView(items))
}

// handleScalingEvents lists the scheduler's recent decisions, each with the
// sentence that justified it.
func (s *Server) handleScalingEvents(w http.ResponseWriter, r *http.Request) {
	limit := clamp(queryInt(r, "limit", defaultLimit), 1, maxLimit)
	events, err := s.ctrl.Store().ListScalingEvents(r.Context(), r.URL.Query().Get("pool_id"), limit)
	if err != nil {
		s.internal(w, r, "reading the scaling history", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(events))
}

// poolNames indexes pools by ID, which several responses need in order to show
// a name next to an ID.
func poolNames(pools []*store.Pool) map[string]string {
	out := make(map[string]string, len(pools))
	for _, p := range pools {
		out[p.ID] = p.Name
	}
	return out
}
