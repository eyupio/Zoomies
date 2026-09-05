package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// runnerResponse is the shape GET /runners returns, rendered by the controller
// so the event stream's runner.* frames are the same JSON. See
// controller/views.go for why the renderer lives there.
type runnerResponse = controller.RunnerView

// runnerDetailResponse adds what the detail page shows and a list would not:
// the host and pool in full, the state history, and whether logs can be had at
// all -- so the log pane can say why it is empty instead of just being empty.
type runnerDetailResponse struct {
	controller.RunnerView
	Host          *hostResponse   `json:"host,omitempty"`
	Pool          *poolResponse   `json:"pool,omitempty"`
	Timeline      []timelineEntry `json:"timeline"`
	LogsAvailable bool            `json:"logs_available"`
}

// timelineEntry is one step of a runner's life.
type timelineEntry struct {
	State      store.RunnerState `json:"state"`
	At         time.Time         `json:"at"`
	DurationMS int64             `json:"duration_ms"`
	Message    string            `json:"message,omitempty"`
}

// handleListRunners answers GET /api/v1/runners.
func (s *Server) handleListRunners(w http.ResponseWriter, r *http.Request) {
	filter := store.RunnerFilter{
		PoolIDs:        queryList(r, "pool_id"),
		HostIDs:        queryList(r, "host_id"),
		Search:         r.URL.Query().Get("q"),
		IncludeRemoved: queryBool(r, "include_removed", false),
	}
	for _, raw := range queryList(r, "state") {
		st := store.RunnerState(raw)
		if !st.Valid() {
			badRequestField(w, "state", fmt.Sprintf("%q is not a runner state; use provisioning, registering, idle, busy, draining, removed or failed", raw))
			return
		}
		filter.States = append(filter.States, st)
	}

	p := parsePage(r)
	runners, total, err := s.ctrl.Store().ListRunners(r.Context(), filter, p)
	if err != nil {
		s.internal(w, r, "listing runners", err)
		return
	}
	view, err := s.ctrl.RunnerRenderer(r.Context(), runners)
	if err != nil {
		s.internal(w, r, "listing runners", err)
		return
	}
	out := make([]runnerResponse, 0, len(runners))
	for _, run := range runners {
		out = append(out, view.View(run))
	}
	writeJSON(w, http.StatusOK, newPage(out, total, p))
}

// handleGetRunner answers GET /api/v1/runners/{id}.
func (s *Server) handleGetRunner(w http.ResponseWriter, r *http.Request) {
	run, err := s.ctrl.Store().GetRunner(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the runner", err)
		return
	}
	view, err := s.ctrl.RunnerRenderer(r.Context(), []*store.Runner{run})
	if err != nil {
		s.internal(w, r, "reading the runner", err)
		return
	}
	detail := runnerDetailResponse{
		RunnerView: view.View(run),
		Timeline:   runnerTimeline(run, s.ctrl.Now()),
	}

	if host, herr := s.ctrl.Store().GetHost(r.Context(), run.HostID); herr == nil {
		h := s.ctrl.HostView(host)
		detail.Host = &h
		// Logs come from the runner's own agent, so a host that is not
		// checking in cannot produce them and a removed runner no longer has a
		// container to read.
		detail.LogsAvailable = host.Healthy(s.ctrl.Now()) && run.State != store.RunnerRemoved
	}
	if pool, perr := s.ctrl.Store().GetPool(r.Context(), run.PoolID); perr == nil {
		if pv, verr := s.ctrl.PoolRenderer(r.Context()); verr == nil {
			p := pv.View(pool)
			detail.Pool = &p
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleRunnerTimeline answers GET /api/v1/runners/{id}/timeline.
func (s *Server) handleRunnerTimeline(w http.ResponseWriter, r *http.Request) {
	run, err := s.ctrl.Store().GetRunner(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the runner", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(runnerTimeline(run, s.ctrl.Now())))
}

// runnerTimeline reconstructs a runner's life from the timestamps its row
// carries.
//
// There is no per-transition history table, deliberately: a busy fleet would
// write millions of rows to answer a question the four stamped timestamps
// already answer. What this cannot show is a runner that went idle, busy and
// idle again -- only the most recent of those is recorded -- so it is a summary
// of the runner's life rather than an audit trail of it, and the audit log is
// where the actions people took are kept.
func runnerTimeline(r *store.Runner, now time.Time) []timelineEntry {
	entries := []timelineEntry{{
		State:   store.RunnerProvisioning,
		At:      r.CreatedAt,
		Message: "the controller asked an agent to create this runner",
	}}
	if r.StartedAt != nil {
		entries = append(entries, timelineEntry{
			State:   store.RunnerRegistering,
			At:      *r.StartedAt,
			Message: "the workload came up and registered with GitHub",
		})
	}
	if r.LastIdleAt != nil && (r.StartedAt == nil || r.LastIdleAt.After(*r.StartedAt)) {
		entries = append(entries, timelineEntry{
			State:   store.RunnerIdle,
			At:      *r.LastIdleAt,
			Message: "waiting for a job",
		})
	}
	if r.FinishedAt != nil {
		entries = append(entries, timelineEntry{
			State:   r.State,
			At:      *r.FinishedAt,
			Message: r.Message,
		})
	} else {
		entries = append(entries, timelineEntry{
			State:   r.State,
			At:      latest(r.CreatedAt, r.StartedAt, r.LastIdleAt),
			Message: r.Message,
		})
	}

	// Each entry lasts until the next one; the last one is still running,
	// unless the runner has finished, in which case it is over.
	for i := range entries {
		var until time.Time
		switch {
		case i+1 < len(entries):
			until = entries[i+1].At
		case r.FinishedAt != nil:
			until = *r.FinishedAt
		default:
			until = now
		}
		entries[i].DurationMS = millis(until.Sub(entries[i].At))
	}
	return entries
}

func latest(base time.Time, others ...*time.Time) time.Time {
	out := base
	for _, t := range others {
		if t != nil && t.After(out) {
			out = *t
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Acting on runners
// ---------------------------------------------------------------------------

// handleDrainRunner asks a runner to finish its current job and exit.
//
// It answers 202 rather than 200: the runner has been told, and it stops when
// its job does. A runner that is already terminal is a 409, because "drain a
// removed runner" is a request the fleet cannot honour and quietly succeeding
// would leave the operator believing something happened.
func (s *Server) handleDrainRunner(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	run, err := s.ctrl.DrainRunner(r.Context(), id, drainReason(r))
	if err != nil {
		s.fail(w, r, "draining the runner", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "runner.drain", "runner", id, map[string]any{
		"name": run.Name, "state": run.State, "pool_id": run.PoolID,
	})
	s.ctrl.Nudge()

	view, verr := s.ctrl.RunnerRenderer(r.Context(), []*store.Runner{run})
	if verr != nil {
		s.internal(w, r, "reading the runner back", verr)
		return
	}
	writeJSON(w, http.StatusAccepted, view.View(run))
}

// handleDeleteRunner removes a runner, draining first unless forced.
func (s *Server) handleDeleteRunner(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	force := queryBool(r, "force", false)
	run, err := s.ctrl.RemoveRunner(r.Context(), id, drainReason(r), force)
	if err != nil {
		s.fail(w, r, "removing the runner", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "runner.delete", "runner", id, map[string]any{
		"name": run.Name, "state": run.State, "pool_id": run.PoolID, "force": force,
	})
	s.ctrl.Nudge()
	w.WriteHeader(http.StatusAccepted)
}

// drainReason names who asked, so the runner's message and the audit row agree.
func drainReason(r *http.Request) string {
	id := Identity(r.Context())
	if id == nil || id.Name == "" {
		return "requested through the API"
	}
	return "requested by " + id.Name
}

type bulkRunnerRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
	Force  bool     `json:"force"`
}

type bulkRunnerResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type bulkRunnerResponse struct {
	Results []bulkRunnerResult `json:"results"`
}

// handleBulkRunners drains or deletes several runners at once.
//
// It answers 200 with a result per ID rather than a single status: selecting
// forty runners and being told only that "something failed" is not an answer
// anyone can act on, and a partial failure here is normal -- one of the forty
// finished its job a second before the request arrived.
func (s *Server) handleBulkRunners(w http.ResponseWriter, r *http.Request) {
	var req bulkRunnerRequest
	if !decode(w, r, &req) {
		return
	}

	var fields []fieldError
	switch req.Action {
	case "drain", "delete":
	case "":
		fields = append(fields, fieldError{"action", "say what to do with these runners: drain or delete"})
	default:
		fields = append(fields, fieldError{"action", fmt.Sprintf("%q is not an action; use drain or delete", req.Action)})
	}
	switch {
	case len(req.IDs) == 0:
		fields = append(fields, fieldError{"ids", "no runners were selected"})
	case len(req.IDs) > 500:
		fields = append(fields, fieldError{"ids", fmt.Sprintf("%d runners is more than the 500 this endpoint accepts at once; do it in batches", len(req.IDs))})
	}
	if len(fields) > 0 {
		unprocessable(w, "this bulk action could not be carried out", fields)
		return
	}

	// Deleting is the stronger action, so a caller allowed only to drain must
	// not reach it through the bulk endpoint. The route gate checks the weaker
	// one; this is the other half of that.
	id := Identity(r.Context())
	if req.Action == "delete" && !auth.Allowed(id, auth.ActionRunnersDelete) {
		forbidden(w, auth.Explain(id, auth.ActionRunnersDelete))
		return
	}

	results := make([]bulkRunnerResult, 0, len(req.IDs))
	changed := 0
	for _, runnerID := range req.IDs {
		var err error
		var run *store.Runner
		if req.Action == "drain" {
			run, err = s.ctrl.DrainRunner(r.Context(), runnerID, drainReason(r))
		} else {
			run, err = s.ctrl.RemoveRunner(r.Context(), runnerID, drainReason(r), req.Force)
		}
		if err != nil {
			results = append(results, bulkRunnerResult{ID: runnerID, Error: err.Error()})
			continue
		}
		changed++
		results = append(results, bulkRunnerResult{ID: runnerID, OK: true})
		s.auth.Auditor().Act(r.Context(), id, "runner."+req.Action, "runner", runnerID, map[string]any{
			"name": run.Name, "state": run.State, "bulk": true, "force": req.Force,
		})
	}
	if changed > 0 {
		s.ctrl.Nudge()
	}
	writeJSON(w, http.StatusOK, bulkRunnerResponse{Results: results})
}
