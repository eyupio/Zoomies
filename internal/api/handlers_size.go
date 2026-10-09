package api

import (
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// handleAutoPools answers GET /api/v1/auto-pools: the state of size routing and
// of the pools the controller keeps, as the last pass of its reconciler found it.
func (s *Server) handleAutoPools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.AutoPools())
}

// handleListSizePins answers GET /api/v1/size-pins.
func (s *Server) handleListSizePins(w http.ResponseWriter, r *http.Request) {
	pins, err := s.ctrl.Store().ListSizePins(r.Context())
	if err != nil {
		s.internal(w, r, "listing size pins", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(pins))
}

// sizePinRequest is what PUT /size-pins takes.
type sizePinRequest struct {
	Repo     string          `json:"repo"`
	Workflow string          `json:"workflow"`
	JobName  string          `json:"job_name"`
	Class    store.SizeClass `json:"class"`
}

// sizePinResult is a pin and how many waiting jobs it moved.
type sizePinResult struct {
	Pin          *store.SizePin `json:"pin"`
	Reclassified int            `json:"reclassified"`
}

// pinFields says what is wrong with a pin's parts, by field. The rule is the one
// store.SizePin holds, which says it once per field so that a form can put each
// sentence where it belongs.
func pinFields(repo, workflow, job string, class store.SizeClass, checkClass bool) []fieldError {
	var fields []fieldError
	pin := store.SizePin{Repo: repo, Workflow: workflow, JobName: job, Class: class}
	for _, problem := range pin.Problems(checkClass) {
		fields = append(fields, fieldError{problem.Field, problem.Message})
	}
	return fields
}

// pinKey names a pin in an audit row.
func pinKey(p *store.SizePin) string {
	if p.ForRepository() {
		return p.Repo
	}
	return p.Repo + " / " + p.Workflow + " / " + p.JobName
}

// handleSetSizePin answers PUT /api/v1/size-pins.
//
// A pin that only reached the jobs that arrive afterwards would not fix the job
// that is stuck in the wrong class right now, which is why it is made, so the
// jobs already waiting are put back through the classification here and the
// answer says how many of them changed.
func (s *Server) handleSetSizePin(w http.ResponseWriter, r *http.Request) {
	var req sizePinRequest
	if !decode(w, r, &req) {
		return
	}
	if fields := pinFields(req.Repo, req.Workflow, req.JobName, req.Class, true); len(fields) > 0 {
		unprocessable(w, "this pin cannot be saved as described", fields)
		return
	}
	id := Identity(r.Context())
	pin := &store.SizePin{Repo: req.Repo, Workflow: req.Workflow, JobName: req.JobName, Class: req.Class}
	if id != nil {
		pin.CreatedBy = id.Name
	}
	if err := s.ctrl.Store().SetSizePin(r.Context(), pin); err != nil {
		s.fail(w, r, "saving the pin", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), id, "size_pin.set", "size_pin", pinKey(pin), map[string]any{
		"repo": pin.Repo, "workflow": pin.Workflow, "job_name": pin.JobName, "class": pin.Class,
	})
	n, err := s.ctrl.ReclassifyQueuedJobs(r.Context(), pin.Repo, pin.Workflow, pin.JobName)
	if err != nil {
		// The pin stands and the next job that arrives is classed by it; the ones
		// already waiting are the ones this failed for.
		s.logger(r).Warn("a size pin was saved but the jobs already waiting could not be reclassed", "pin", pinKey(pin), "error", err)
	}
	writeJSON(w, http.StatusOK, sizePinResult{Pin: pin, Reclassified: n})
}

// handleDeleteSizePin answers DELETE /api/v1/size-pins.
func (s *Server) handleDeleteSizePin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, workflow, job := q.Get("repo"), q.Get("workflow"), q.Get("job_name")
	if fields := pinFields(repo, workflow, job, "", false); len(fields) > 0 {
		unprocessable(w, "this pin cannot be removed as described", fields)
		return
	}
	if err := s.ctrl.Store().DeleteSizePin(r.Context(), repo, workflow, job); err != nil {
		s.fail(w, r, "removing the pin", err)
		return
	}
	pin := &store.SizePin{Repo: strings.ToLower(strings.TrimSpace(repo)), Workflow: workflow, JobName: job}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "size_pin.delete", "size_pin", pinKey(pin), map[string]any{
		"repo": pin.Repo, "workflow": pin.Workflow, "job_name": pin.JobName,
	})
	n, err := s.ctrl.ReclassifyQueuedJobs(r.Context(), pin.Repo, pin.Workflow, pin.JobName)
	if err != nil {
		s.logger(r).Warn("a size pin was removed but the jobs already waiting could not be reclassed", "pin", pinKey(pin), "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]int{"reclassified": n})
}

// adviceResponse is a page of label advice with how much there is of each kind,
// whatever page or kind was asked for.
type adviceResponse struct {
	page[*scheduler.Advice]
	Counts map[string]int          `json:"counts"`
	Window controller.AdviceWindow `json:"window"`
}

// handleLabelAdvice answers GET /api/v1/label-advice.
func (s *Server) handleLabelAdvice(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	switch kind {
	case "", scheduler.AdviceTooSmall, scheduler.AdviceUnguaranteed, scheduler.AdviceTooLarge:
	default:
		badRequestField(w, "kind", "kind is too_small, unguaranteed or too_large")
		return
	}
	window, err := querySpan(r, "window", 0)
	if err != nil {
		badRequestField(w, "window", err.Error())
		return
	}
	all, applied, err := s.ctrl.LabelAdvice(r.Context(), controller.AdviceOptions{Window: window, Repo: r.URL.Query().Get("repo")})
	if err != nil {
		s.internal(w, r, "working out label advice", err)
		return
	}
	// A sparse row has no kind, so a kind filter never returns it, and it is
	// counted under its state rather than under the empty kind.
	counts := map[string]int{scheduler.AdviceTooSmall: 0, scheduler.AdviceUnguaranteed: 0, scheduler.AdviceTooLarge: 0, scheduler.AdviceStateNotEnoughData: 0}
	var matching []*scheduler.Advice
	for _, a := range all {
		if a.State != scheduler.AdviceStateOK {
			counts[a.State]++
		} else {
			counts[a.Kind]++
		}
		if kind == "" || a.Kind == kind {
			matching = append(matching, a)
		}
	}
	p := parsePage(r)
	lo := min(p.Offset, len(matching))
	hi := min(lo+p.Limit, len(matching))
	writeJSON(w, http.StatusOK, adviceResponse{page: newPage(matching[lo:hi], len(matching), p), Counts: counts, Window: applied})
}
