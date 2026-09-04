package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// jobResponse is one workflow job as Zoomies observed it.
//
// queue_wait_ms and duration_ms are computed here rather than stored: they are
// derived from the three timestamps, and a stored copy would be one more thing
// that can disagree with them.
type jobResponse struct {
	ID          string         `json:"id"`
	GitHubJobID int64          `json:"github_job_id"`
	GitHubRunID int64          `json:"github_run_id"`
	Repo        string         `json:"repo"`
	Workflow    string         `json:"workflow"`
	JobName     string         `json:"job_name"`
	Labels      []string       `json:"labels"`
	State       store.JobState `json:"state"`
	Conclusion  string         `json:"conclusion,omitempty"`
	PoolID      string         `json:"pool_id,omitempty"`
	PoolName    string         `json:"pool_name,omitempty"`
	RunnerID    string         `json:"runner_id,omitempty"`
	RunnerName  string         `json:"runner_name,omitempty"`
	HTMLURL     string         `json:"html_url,omitempty"`
	Matched     bool           `json:"matched"`
	QueuedAt    time.Time      `json:"queued_at"`
	StartedAt   *time.Time     `json:"started_at"`
	CompletedAt *time.Time     `json:"completed_at"`
	QueueWaitMS int64          `json:"queue_wait_ms"`
	DurationMS  int64          `json:"duration_ms"`
}

func newJobResponse(j *store.Job, poolName string) jobResponse {
	return jobResponse{
		ID:          j.ID,
		GitHubJobID: j.GitHubJobID,
		GitHubRunID: j.GitHubRunID,
		Repo:        j.Repo,
		Workflow:    j.Workflow,
		JobName:     j.JobName,
		Labels:      emptySlice(j.Labels),
		State:       j.State,
		Conclusion:  j.Conclusion,
		PoolID:      j.PoolID,
		PoolName:    poolName,
		RunnerID:    j.RunnerID,
		RunnerName:  j.RunnerName,
		HTMLURL:     j.HTMLURL,
		Matched:     j.Matched,
		QueuedAt:    j.QueuedAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		QueueWaitMS: millis(j.QueueWait()),
		DurationMS:  millis(j.Duration()),
	}
}

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
			badRequestField(w, "state", fmt.Sprintf("%q is not a job state; use queued, in_progress or completed", raw))
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
		out = append(out, newJobResponse(j, names[j.PoolID]))
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
	writeJSON(w, http.StatusOK, newJobResponse(j, poolName))
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
