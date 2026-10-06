package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
)

// applyRemedyRequest names the suggestion to apply, never the change: the change
// is whatever the controller currently proposes for it. A client that could send
// the update itself would have an endpoint that edits any pool or host without
// the route's own role check, which is what this is careful not to be.
type applyRemedyRequest struct {
	// Code and TargetID say which problem; both are as the problem lists them.
	Code     string `json:"code"`
	TargetID string `json:"target_id"`
	// RemedyID is the remedy's ID from the problem the caller was shown. It is
	// optional on purpose -- a script that wants whatever is proposed now may leave
	// it out -- but a UI sends it, so a click applies what was on screen and not a
	// different proposal that arrived a moment later.
	RemedyID string `json:"remedy_id"`
}

// applyRemedyResponse is what was applied and what the route that applied it said.
type applyRemedyResponse struct {
	Applied bool            `json:"applied"`
	Remedy  remedySummary   `json:"remedy"`
	Result  json.RawMessage `json:"result"`
}

type remedySummary struct {
	ID       string                `json:"id"`
	Label    string                `json:"label"`
	Kind     controller.RemedyKind `json:"kind"`
	TargetID string                `json:"target_id"`
}

// handleApplyRemedy answers POST /api/v1/problems/apply.
//
// The change is looked up, not taken: the problems are worked out again now, and
// only a remedy among them is applied, so a stale page, a replayed request or a
// forged one cannot make a change the controller does not currently propose. The
// update then runs as the caller, through the same code as the pool's or host's
// own PATCH, so it is refused for the role that route refuses, it refuses what that
// route refuses -- including a change that leaves a pool with nowhere to run, which
// is never overridden here -- and it writes the audit row that route writes.
func (s *Server) handleApplyRemedy(w http.ResponseWriter, r *http.Request) {
	var in applyRemedyRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Code == "" || in.TargetID == "" {
		unprocessable(w, "name the suggestion to apply", []fieldError{{"code", "the problem's code, as the problems list shows it"}, {"target_id", "the pool or host it is about"}})
		return
	}
	// Worked out for this request, not joined from a computation that began before it:
	// an apply is rare and deliberate, and the shared list can be a computation old,
	// which is long enough for a second operator's apply of the same suggestion to
	// find it still there.
	items, err := s.ctrl.Problems(r.Context())
	if err != nil {
		s.internal(w, r, "gathering the current problems", err)
		return
	}
	platform := isPlatform(r)
	var found *controller.Remedy
	for i := range items {
		p := &items[i]
		if p.Code != in.Code || p.TargetID != in.TargetID || p.Remedy == nil || !p.Audience.For(platform) {
			continue
		}
		found = p.Remedy
		break
	}
	if found == nil {
		// A section of the problems that could not be read leaves its suggestions out,
		// which is not the same as there being none: say the controller could not look,
		// so a script or an agent does not conclude the change was made elsewhere.
		for i := range items {
			if items[i].Code == "controller.problems_partial" {
				writeError(w, http.StatusServiceUnavailable, errorEnvelope{Error: errorBody{Code: codeInternal, Message: "the controller could not read everything it needs to say whether that suggestion still applies, so nothing was changed: " + items[i].Detail + " Try again in a moment."}})
				return
			}
		}
		conflict(w, "that suggestion no longer applies: the controller is not proposing a change for "+in.Code+" on "+in.TargetID+" now. Read the problems again.")
		return
	}
	if in.RemedyID != "" && in.RemedyID != found.ID {
		conflict(w, "that suggestion is out of date: the controller now proposes a different change ("+found.Label+"). Read the problems again and apply that one if it is still what you want.")
		return
	}

	// The role is the route's: a viewer who may read the problem may not make the
	// change it proposes, and the answer says which role is missing.
	action := auth.ActionPoolsWrite
	if found.Kind == controller.RemedyHostUpdate {
		action = auth.ActionHostsWrite
	}
	if id := Identity(r.Context()); !auth.Allowed(id, action) {
		forbidden(w, auth.Explain(id, action))
		return
	}

	// The update's own request: the proposed body, no query -- so nothing the
	// caller sent can switch on confirm -- and a response of its own to read.
	req := r.Clone(noConfirm(r.Context()))
	req.URL.RawQuery = ""
	req.Body = io.NopCloser(bytes.NewReader(found.Body))
	req.Header.Set("Content-Type", "application/json")
	rec := &bufferedResponse{header: http.Header{}, limit: mcpResponseLimit}

	switch found.Kind {
	case controller.RemedyPoolUpdate:
		existing, gerr := s.ctrl.Store().GetPool(r.Context(), found.TargetID)
		if gerr != nil {
			s.fail(w, r, "reading the pool", gerr)
			return
		}
		if found.Base != "" && found.Base != controller.RemedyBase(existing.Resources) {
			conflict(w, changedSinceProposed)
			return
		}
		var body poolInput
		if !decode(rec, req, &body) {
			s.relay(w, rec)
			return
		}
		s.applyPoolUpdate(rec, req, found.TargetID, existing, &body)
	case controller.RemedyHostUpdate:
		h, gerr := s.ctrl.Store().GetHost(r.Context(), found.TargetID)
		if gerr != nil {
			s.fail(w, r, "reading the host", gerr)
			return
		}
		if found.Base != "" && found.Base != controller.RemedyBase(h.RunnerProfile) {
			conflict(w, changedSinceProposed)
			return
		}
		var body hostUpdateRequest
		if !decode(rec, req, &body) {
			s.relay(w, rec)
			return
		}
		s.applyHostUpdate(rec, req, found.TargetID, h, &body)
	default:
		s.internal(w, r, "applying a suggestion", fmt.Errorf("a remedy of unknown kind %q", found.Kind))
		return
	}

	if rec.status >= http.StatusBadRequest {
		// The route's own refusal, as it said it: a 409 that names the machine a
		// pool would no longer fit on is more use to the person than a summary of it.
		s.relay(w, rec)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "problem.remedy_applied", string(found.Kind), found.TargetID, map[string]any{
		"code": in.Code, "remedy": found.ID, "label": found.Label, "effect": found.Effect,
	})
	writeJSON(w, http.StatusOK, applyRemedyResponse{
		Applied: true,
		Remedy:  remedySummary{ID: found.ID, Label: found.Label, Kind: found.Kind, TargetID: found.TargetID},
		Result:  json.RawMessage(bytes.TrimSpace(rec.body.Bytes())),
	})
}

// changedSinceProposed answers an apply whose target was edited between the
// controller working the suggestion out and the apply reading the target. The
// suggestion replaces the pool's resources or the host's runner profile whole, so
// applying it over an edit would quietly undo the edit.
const changedSinceProposed = "that suggestion is out of date: the pool or host was edited after the controller worked it out, and applying it now would put back what that edit changed. Read the problems again."

// relay writes a recorded response out as it was recorded: its status, its
// content type and its body.
func (s *Server) relay(w http.ResponseWriter, rec *bufferedResponse) {
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
}
