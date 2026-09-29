package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

type cancelJobRequest struct {
	Force bool `json:"force"`
}

type cancelJobResponse struct {
	Accepted bool  `json:"accepted"`
	Force    bool  `json:"force"`
	RunID    int64 `json:"run_id"`
}

func (s *Server) handleCancelJobWorkflow(w http.ResponseWriter, r *http.Request) {
	var req cancelJobRequest
	if !decodeOptional(w, r, &req) {
		return
	}
	j, err := s.ctrl.CancelJobWorkflow(r.Context(), chiURLParam(r, "id"), req.Force)
	if err != nil {
		switch {
		case errors.Is(err, controller.ErrWorkflowCancellationDisabled):
			conflict(w, err.Error()+"; set github.allow_workflow_cancellation to true and restart Zoomies")
		case errors.Is(err, controller.ErrJobAlreadyCompleted):
			conflict(w, err.Error())
		case errors.Is(err, github.ErrForbidden):
			forbidden(w, err.Error())
		default:
			s.fail(w, r, "cancelling the GitHub workflow run", err)
		}
		return
	}
	action := "job.cancel_requested"
	if req.Force {
		action = "job.force_cancel_requested"
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "job", j.ID, map[string]any{
		"repo": j.Repo, "run_id": j.GitHubRunID, "force": req.Force,
	})
	writeJSON(w, http.StatusAccepted, cancelJobResponse{Accepted: true, Force: req.Force, RunID: j.GitHubRunID})
}

type cancelWorkflowRunRequest struct {
	Repo  string `json:"repo"`
	RunID int64  `json:"run_id"`
	Force bool   `json:"force"`
}

type cancelWorkflowRunResponse struct {
	Accepted bool   `json:"accepted"`
	Force    bool   `json:"force"`
	Repo     string `json:"repo"`
	RunID    int64  `json:"run_id"`
	Jobs     int    `json:"jobs"`
}

// handleCancelWorkflowRun answers POST /api/v1/workflow-runs/cancel: the
// Workflows page's own run rows cancel a run directly, rather than reaching
// for one of its jobs' IDs the way JobDrawer's cancel button does.
func (s *Server) handleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req cancelWorkflowRunRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Repo == "" {
		badRequestField(w, "repo", "repo is required")
		return
	}
	if req.RunID <= 0 {
		badRequestField(w, "run_id", "run_id must be a positive GitHub run ID")
		return
	}
	jobs, err := s.ctrl.CancelWorkflowRun(r.Context(), req.Repo, req.RunID, req.Force)
	if err != nil {
		switch {
		case errors.Is(err, controller.ErrWorkflowCancellationDisabled):
			conflict(w, err.Error()+"; set github.allow_workflow_cancellation to true and restart Zoomies")
		case errors.Is(err, controller.ErrJobAlreadyCompleted):
			conflict(w, err.Error())
		case errors.Is(err, github.ErrForbidden):
			forbidden(w, err.Error())
		default:
			s.fail(w, r, "cancelling the GitHub workflow run", err)
		}
		return
	}
	action := "workflow_run.cancel_requested"
	if req.Force {
		action = "workflow_run.force_cancel_requested"
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "workflow_run",
		fmt.Sprintf("%s#%d", req.Repo, req.RunID), map[string]any{
			"repo": req.Repo, "run_id": req.RunID, "force": req.Force,
		})
	writeJSON(w, http.StatusAccepted, cancelWorkflowRunResponse{
		Accepted: true, Force: req.Force, Repo: req.Repo, RunID: req.RunID, Jobs: len(jobs),
	})
}

type rerunJobResponse struct {
	Accepted bool  `json:"accepted"`
	RunID    int64 `json:"run_id"`
	// FaultDomain is whose the failure being re-run was, echoed back so a CLI
	// or a script can log what it just spent minutes on.
	FaultDomain string `json:"fault_domain,omitempty"`
}

func (s *Server) handleRerunJobWorkflow(w http.ResponseWriter, r *http.Request) {
	j, err := s.ctrl.RerunJobWorkflow(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		switch {
		case errors.Is(err, controller.ErrJobNotFinished):
			conflict(w, err.Error()+", so there is nothing to run again; wait for it to finish")
		case errors.Is(err, controller.ErrJobDidNotFail):
			conflict(w, err.Error()+"; GitHub only reruns the failed jobs of a run")
		case errors.Is(err, github.ErrForbidden):
			forbidden(w, err.Error())
		default:
			s.fail(w, r, "asking GitHub to re-run the workflow run", err)
		}
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "job.rerun_requested", "job", j.ID, map[string]any{
		"repo": j.Repo, "run_id": j.GitHubRunID, "fault_domain": j.FaultDomain(), "fault_kind": string(j.FaultKind),
	})
	writeJSON(w, http.StatusAccepted, rerunJobResponse{Accepted: true, RunID: j.GitHubRunID, FaultDomain: j.FaultDomain()})
}

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
//
// Two ways to page, for two kinds of caller. limit and offset are what the UI's
// grid uses, and they still are. `before` is a cursor for anything that walks
// the whole history: it continues after the last job of the previous page, so a
// job queued in between cannot shift a row from one page to the next. Both
// answers carry `next` where a cursor would continue from.
//
// include_steps=false returns each job as a summary, without the step list
// that is most of its weight.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	filter, ok := parseJobFilter(w, r)
	if !ok {
		return
	}

	p := parsePage(r)
	provisioning := strings.HasPrefix(r.URL.Path, "/api/v1/provisioning")
	includeSteps := true
	if !provisioning {
		var ok bool
		if includeSteps, ok = queryStrictBool(w, r, "include_steps", true); !ok {
			return
		}
	}

	var jobs []*store.Job
	var total int
	var next *store.JobCursor
	var err error
	q := r.URL.Query()
	switch {
	case !provisioning && q.Get("before") != "":
		if q.Get("offset") != "" || q.Get("sort") != "" || q.Get("order") != "" {
			badRequestField(w, "before", "before continues the default order, newest queued first; it cannot be combined with offset, sort or order")
			return
		}
		cursor, cerr := store.ParseJobCursor(q.Get("before"))
		if cerr != nil {
			badRequestField(w, "before", "before is not a cursor; pass the next value of the previous page unchanged")
			return
		}
		jobs, total, next, err = s.ctrl.Store().ListJobsAfter(r.Context(), filter, &cursor, p.Limit)
		p.Offset = 0
	default:
		jobs, total, err = s.ctrl.Store().ListJobs(r.Context(), filter, p)
		// A cursor is only meaningful in the default order, and only while
		// there is more to read.
		if err == nil && !provisioning && p.Sort == "" && p.Desc && len(jobs) > 0 && p.Offset+len(jobs) < total {
			last := jobs[len(jobs)-1]
			next = &store.JobCursor{QueuedAt: last.QueuedAt, ID: last.ID}
		}
	}
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

	if provisioning {
		out := make([]jobResponse, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, controller.NewJobView(j, names[j.PoolID]))
		}
		counts, err := s.ctrl.Store().ProvisioningCounts(r.Context(), filter)
		if err != nil {
			s.internal(w, r, "counting provisioning demand", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total, "counts": counts})
		return
	}

	nextToken := ""
	if next != nil {
		nextToken = next.Encode()
	}
	if !includeSteps {
		out := make([]controller.JobSummaryView, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, controller.NewJobSummaryView(j, names[j.PoolID]))
		}
		pg := newPage(out, total, p)
		pg.Next = nextToken
		writeJSON(w, http.StatusOK, pg)
		return
	}
	out := make([]jobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, controller.NewJobView(j, names[j.PoolID]))
	}
	pg := newPage(out, total, p)
	pg.Next = nextToken
	writeJSON(w, http.StatusOK, pg)
}

// jobStatsResponse is GET /api/v1/jobs/stats.
type jobStatsResponse struct {
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	GroupBy []string  `json:"group_by"`
	// DurationExcludes names the conclusions left out of every duration
	// percentile, and Notes says the rest of what a reader needs to know to
	// read the figures -- in the response, because the numbers get quoted
	// without the documentation.
	DurationExcludes []string              `json:"duration_excludes"`
	Notes            []string              `json:"notes"`
	Truncated        bool                  `json:"truncated"`
	Groups           []store.JobStatsGroup `json:"groups"`
}

// defaultStatsSpan is how far back statistics reach when the caller names no
// start: a week, which is long enough to show a release and short enough to
// answer at once.
const defaultStatsSpan = 7 * 24 * time.Hour

// handleJobStats answers GET /api/v1/jobs/stats: completed jobs counted and
// timed, grouped by up to two of controller_version, day, host, pool and
// job_name. It takes the job listing's filters, and the window is
// [since, until) with until defaulting to now and since to a week before it.
//
// The window is bounded by limits.job_stats_window because the figures are
// computed from the job rows on each request; a refusal names the setting, so
// the person who needs a longer look knows where the limit is kept.
func (s *Server) handleJobStats(w http.ResponseWriter, r *http.Request) {
	filter, ok := parseJobFilter(w, r)
	if !ok {
		return
	}
	now := s.ctrl.Now()
	if filter.Until == nil {
		filter.Until = &now
	}
	limit := s.cfg().Limits.JobStatsSpan()
	if filter.Since == nil {
		// Never longer than the limit: an operator who set it to two days has
		// not asked for a week, and a default that refused itself would be a
		// 400 for a request that named nothing wrong.
		since := filter.Until.Add(-min(defaultStatsSpan, limit))
		filter.Since = &since
	}
	if !filter.Since.Before(*filter.Until) {
		badRequestField(w, "since", "since must be before until")
		return
	}
	if filter.Until.Sub(*filter.Since) > limit {
		badRequestField(w, "since", fmt.Sprintf(
			"the window from since to until is %s, and job statistics cover at most %s; narrow it, or raise limits.job_stats_window",
			filter.Until.Sub(*filter.Since).Round(time.Hour), limit))
		return
	}
	// Statistics are of finished work, and the listing's own state filter
	// would only make an answer that disagrees with its own counts.
	filter.States = nil

	groupBy := queryList(r, "group_by")
	if len(groupBy) > 2 {
		badRequestField(w, "group_by", "group_by takes at most two keys")
		return
	}
	for i, g := range groupBy {
		if !slices.Contains(store.JobStatsGroupKeys, g) {
			badRequestField(w, "group_by", fmt.Sprintf("%q is not something jobs can be grouped by; use %s", g, strings.Join(store.JobStatsGroupKeys, ", ")))
			return
		}
		if slices.Contains(groupBy[:i], g) {
			badRequestField(w, "group_by", fmt.Sprintf("%q is named twice", g))
			return
		}
	}

	res, err := s.ctrl.Store().JobStats(r.Context(), filter, groupBy)
	if err != nil {
		s.internal(w, r, "computing job statistics", err)
		return
	}
	groups := res.Groups
	if groups == nil {
		groups = []store.JobStatsGroup{}
	}
	writeJSON(w, http.StatusOK, jobStatsResponse{
		Since: *filter.Since, Until: *filter.Until, GroupBy: emptySlice(groupBy),
		DurationExcludes: []string{"cancelled", "skipped"},
		Notes: []string{
			"Only completed jobs are counted, selected by when they were queued: since is included and until is not.",
			"Duration percentiles leave out cancelled and skipped jobs; queue wait percentiles keep them.",
			"Startup is the runner's container start less its creation, and exists only while the runner's own record is kept (retention.runners).",
			"Jobs recorded before their controller version was stamped, and jobs no pool here claimed, are grouped as \"unknown\".",
			"A percentile is null when no job in the group could be measured.",
		},
		Truncated: res.Truncated,
		Groups:    groups,
	})
}

// handleListWorkflowRuns answers GET /api/v1/workflow-runs: the jobs summed
// up per workflow run, which is the unit GitHub's Actions tab lists and the
// one the Workflows page shows before it is opened to the jobs inside.
//
// It takes the job listing's filters, read at the run's level -- a status
// names the run's own, anything else keeps a run when any of its jobs
// matches -- so the Workflows page and the Jobs page share one address bar.
func (s *Server) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	filter, ok := parseJobFilter(w, r)
	if !ok {
		return
	}
	p := parsePage(r)
	runs, total, err := s.ctrl.Store().ListWorkflowRuns(r.Context(), filter, p)
	if err != nil {
		s.internal(w, r, "listing workflow runs", err)
		return
	}
	out := make([]controller.WorkflowRunView, 0, len(runs))
	for _, run := range runs {
		out = append(out, controller.NewWorkflowRunView(run))
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

// handleJobExplanation answers GET /api/v1/jobs/{id}/explanation.
//
// It is its own endpoint rather than a field on the job, because the answer is
// computed from the last scheduler plan and the fleet around the job rather
// than from the row -- so it would be wrong on every cached copy of a job the
// event stream has already delivered.
func (s *Server) handleJobExplanation(w http.ResponseWriter, r *http.Request) {
	out, err := s.ctrl.ExplainJob(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "explaining the job", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func parseJobFilter(w http.ResponseWriter, r *http.Request) (store.JobFilter, bool) {
	filter := store.JobFilter{
		Repos:        queryList(r, "repo"),
		Branches:     queryList(r, "branch"),
		Provisioning: queryList(r, "provisioning"),
		Workflows:    queryList(r, "workflow"),
		PoolIDs:      queryList(r, "pool_id"),
		RunnerIDs:    queryList(r, "runner_id"),
		Conclusions:  queryList(r, "conclusion"),
		Labels:       queryList(r, "label"),
		Search:       r.URL.Query().Get("q"),

		ControllerVersions: queryList(r, "controller_version"),
		HostIDs:            queryList(r, "host_id"),
		// Not split on commas, unlike the lists around it: a matrix job's
		// name is "build (ubuntu-latest, 3.12)", and a filter that cut it in
		// two would match nothing while looking like it was working.
		JobNames: nonEmpty(r.URL.Query()["job_name"]),
	}
	var hostedOK bool
	if filter.Hosted, hostedOK = queryStrictBoolPtr(w, r, "hosted"); !hostedOK {
		return filter, false
	}
	for _, raw := range queryList(r, "run_id") {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			badRequestField(w, "run_id", fmt.Sprintf("%q is not a workflow run ID; GitHub's are whole numbers, the run_id on a job", raw))
			return filter, false
		}
		filter.RunIDs = append(filter.RunIDs, id)
	}
	for _, raw := range queryList(r, "state") {
		st := store.JobState(raw)
		if !st.Valid() {
			badRequestField(w, "state", fmt.Sprintf("%q is not a job state; use waiting, queued, in_progress or completed", raw))
			return filter, false
		}
		filter.States = append(filter.States, st)
	}

	var err error
	if filter.Since, err = queryTime(r, "since"); err != nil {
		badRequestField(w, "since", err.Error())
		return filter, false
	}
	if filter.Until, err = queryTime(r, "until"); err != nil {
		badRequestField(w, "until", err.Error())
		return filter, false
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
	if faulted := queryBoolPtr(r, "faulted"); faulted != nil {
		filter.FaultedOnly = *faulted
	}
	if workflowFailed := queryBoolPtr(r, "workflow_failed"); workflowFailed != nil {
		filter.WorkflowFailedOnly = *workflowFailed
	}
	// Kept as a pointer rather than flattened to a bool: absent means every
	// job, which is what a history wants, and `cancelling=false` is a
	// different question from not asking at all.
	filter.Cancelling = queryBoolPtr(r, "cancelling")
	if filter.FaultedOnly && filter.WorkflowFailedOnly {
		badRequestField(w, "faulted", "faulted and workflow_failed ask for opposite halves of the same list; send one")
		return filter, false
	}
	for _, raw := range r.URL.Query()["fault"] {
		// Refused rather than ignored. A category this build does not know
		// matches nothing, and a filter that silently returned everything
		// would read as a fleet in better shape than it is.
		kind := store.FaultKind(raw)
		if !kind.Valid() {
			badRequestField(w, "fault", fmt.Sprintf("%q is not a fault category; use one of %s", raw, faultKindList()))
			return filter, false
		}
		filter.FaultKinds = append(filter.FaultKinds, kind)
	}

	for _, v := range filter.Provisioning {
		switch v {
		case "ready", "expedited", "paused", "deleted":
		default:
			badRequestField(w, "provisioning", "use ready, expedited, paused or deleted")
			return filter, false
		}
	}
	if filter.Since != nil && filter.Until != nil && filter.Since.After(*filter.Until) {
		badRequestField(w, "since", "start must be before end")
		return filter, false
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/provisioning") {
		filter.States = []store.JobState{store.JobQueued}
		filter.ManagedOnly = true
	}
	return filter, true
}

// faultKindList names the categories for an error message, so a 400 tells the
// caller what to send instead of only that what they sent was wrong.
func faultKindList() string {
	kinds := store.FaultKinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// nonEmpty drops the blank values of a repeated parameter.
func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// queryStrictBoolPtr is queryBoolPtr for a parameter where a typo should be
// refused rather than read as absent: `hosted=flase` quietly returning every
// job would be taken for an answer about hosted jobs.
func queryStrictBoolPtr(w http.ResponseWriter, r *http.Request, name string) (*bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, true
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		badRequestField(w, name, fmt.Sprintf("%s must be true or false, not %q", name, raw))
		return nil, false
	}
	return &b, true
}

func queryStrictBool(w http.ResponseWriter, r *http.Request, name string, fallback bool) (bool, bool) {
	v, ok := queryStrictBoolPtr(w, r, name)
	if !ok {
		return false, false
	}
	if v == nil {
		return fallback, true
	}
	return *v, true
}
