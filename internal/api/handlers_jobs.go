package api

import (
	"fmt"
	"net/http"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// jobResponse is one workflow job as Zoomies observed it.
//
// queue_wait_ms and duration_ms are computed here rather than stored: they are
// derived from the three timestamps, and a stored copy would be one more thing
// that can disagree with them.
// jobResponse is the shape GET /jobs returns, rendered by the controller so
// the event stream's job.updated frames are the same JSON. See
// controller/views.go for why the renderer lives there.
type jobResponse = controller.JobView

// handleListJobs answers GET /api/v1/jobs.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	filter := store.JobFilter{
		Repos:       queryList(r, "repo"),
		Workflows:   queryList(r, "workflow"),
		PoolIDs:     queryList(r, "pool_id"),
		RunnerIDs:   queryList(r, "runner_id"),
		Conclusions: queryList(r, "conclusion"),
		Labels:      queryList(r, "label"),
		Search:      r.URL.Query().Get("q"),
	}
	for _, raw := range queryList(r, "state") {
		st := store.JobState(raw)
		if !st.Valid() {
			badRequestField(w, "state", fmt.Sprintf("%q is not a job state; use waiting, queued, in_progress or completed", raw))
			return
		}
		filter.States = append(filter.States, st)
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
	if unmatched := queryBoolPtr(r, "unmatched"); unmatched != nil {
		filter.UnmatchedOnly = *unmatched
	}
	if managed := queryBoolPtr(r, "managed"); managed != nil {
		filter.ManagedOnly = *managed
	}
	if failed := queryBoolPtr(r, "failed"); failed != nil {
		filter.FailedOnly = *failed
	}

	p := parsePage(r)
	jobs, total, err := s.ctrl.Store().ListJobs(r.Context(), filter, p)
	if err != nil {
		s.internal(w, r, "listing jobs", err)
		return
	}
	pools, err := s.ctrl.Store().ListPools(r.Context())
	if err != nil {
		s.internal(w, r, "listing jobs", err)
		return
	}
	names := poolNames(pools)

	out := make([]jobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, controller.NewJobView(j, names[j.PoolID]))
	}
	writeJSON(w, http.StatusOK, newPage(out, total, p))
}

// handleGetJob answers GET /api/v1/jobs/{id}.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.ctrl.Store().GetJob(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the job", err)
		return
	}
	poolName := ""
	if j.PoolID != "" {
		if p, perr := s.ctrl.Store().GetPool(r.Context(), j.PoolID); perr == nil {
			poolName = p.Name
		}
	}
	writeJSON(w, http.StatusOK, controller.NewJobView(j, poolName))
}

// jobEventResponse is one entry of a job's timeline. The store's record is the
// documented shape: it has no derived fields, so there is nothing for a
// renderer to add.
type jobEventResponse = store.JobEvent

// handleJobEvents answers GET /api/v1/jobs/{id}/events: what happened to a
// job, in order, in sentences. The drawer fetches it when it opens and again
// on every job.updated frame for that job, so the list is never stale for
// longer than the stream is.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.ctrl.JobEvents(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the job's timeline", err)
		return
	}
	out := make([]jobEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, *e)
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// jobFacetsResponse populates the filter menus.
type jobFacetsResponse struct {
	Repos       []string `json:"repos"`
	Workflows   []string `json:"workflows"`
	Conclusions []string `json:"conclusions"`
}

// handleJobFacets answers GET /api/v1/jobs/facets.
//
// The distinct values come from the database rather than from the page the UI
// happens to be showing, so the filter menu offers every repository that has
// ever run a job here, not only the fifty most recent.
func (s *Server) handleJobFacets(w http.ResponseWriter, r *http.Request) {
	out := jobFacetsResponse{}
	for _, f := range []struct {
		column string
		into   *[]string
	}{
		{"repo", &out.Repos},
		{"workflow", &out.Workflows},
		{"conclusion", &out.Conclusions},
	} {
		values, err := s.ctrl.Store().JobDistinct(r.Context(), f.column, 500)
		if err != nil {
			s.internal(w, r, "reading the distinct "+f.column+" values", err)
			return
		}
		*f.into = emptySlice(values)
	}
	writeJSON(w, http.StatusOK, out)
}
