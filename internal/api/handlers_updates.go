package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"

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
	writeJSON(w, http.StatusOK, status.For(isPlatform(r)))
}

// handleCheckUpdates answers POST /api/v1/updates/check: ask GitHub for the
// release list now, and answer with what the status is once it has been read.
//
// A press inside the minute is not an error and is not asked of GitHub again: the
// controller returns as though it had asked, and the status is the one the last
// request left.
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.CheckForReleases(r.Context()); err != nil {
		s.failUpdate(w, r, err)
		return
	}
	status, err := s.ctrl.UpdatesView(r.Context())
	if err != nil {
		s.internal(w, r, "working out the update status", err)
		return
	}
	writeJSON(w, http.StatusOK, status.For(isPlatform(r)))
}

// updateControllerRequest is the body of POST /updates/controller.
type updateControllerRequest struct {
	// Tag is the release to take. Left out, it is the newest that can be installed
	// on this system.
	Tag string `json:"tag"`
}

// handleUpdateController answers POST /api/v1/updates/controller: ask the update
// helper beside the controller to replace its binary.
//
// It answers 202, because the request is made and nothing is done: the helper
// answers on its own time, and the page learns how it went from the status. The
// audit row is written once the controller has said yes, so a refused press, which
// changed nothing, leaves none.
func (s *Server) handleUpdateController(w http.ResponseWriter, r *http.Request) {
	var in updateControllerRequest
	if !decodeUpdateRequest(w, r, &in) {
		return
	}
	id := Identity(r.Context())
	status, attemptID, err := s.ctrl.RequestControllerUpdateAttempt(r.Context(), controller.UpdateActor{ID: id.ID, Name: id.Name}, in.Tag)
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	// Detached from the request: the helper may already be running what was asked,
	// and a browser that gave up waiting must not leave that with no record of who
	// asked.
	detail := map[string]any{}
	if attempt := status.Controller; attempt != nil {
		detail["from"], detail["to"] = attempt.From, attempt.To
	}
	s.auth.Auditor().Act(context.WithoutCancel(r.Context()), id, "update.controller_requested", "update", attemptID, detail)
	writeJSON(w, http.StatusAccepted, status.For(isPlatform(r)))
}

// updateRefusalCodes gives each refusal the controller can make the code a client
// switches on. A refusal that wraps its sentinel to add a detail keeps its code.
//
// ErrUpdateFenced is not here: a controller that may not act is the plain
// conflict every other handler answers, and a page can do nothing different about
// it than wait.
var updateRefusalCodes = []struct {
	err  error
	code string
}{
	{controller.ErrUpdateModeOff, codeUpdateModeOff},
	{controller.ErrUpdateCheckDisabled, codeUpdateCheckDisabled},
	{controller.ErrUpdateHelperMissing, codeUpdateHelperMissing},
	{controller.ErrUpdateInProgress, codeUpdateInProgress},
	{controller.ErrUpdateNotARelease, codeUpdateNotARelease},
	{controller.ErrUpdateNothingNewer, codeUpdateNothingNewer},
	{controller.ErrUpdateHostCannotUpdate, codeUpdateHostCannotUpdate},
	{controller.ErrUpdateRolloutHalted, codeUpdateRolloutHalted},
	{controller.ErrUpdateFenced, codeConflict},
}

// failUpdate answers an error from an update call: a refusal as the 409 its code
// names, a release check that GitHub or the network would not finish as a 502,
// each with the sentence the controller wrote for the person who pressed the
// button, and anything else as the server failure it is.
func (s *Server) failUpdate(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, controller.ErrUpdateCheckFailed) {
		// Upstream's failure, not this controller's, so it is a warning: an error in
		// the log teaches an operator to look for a fault that is not there. The
		// sentence is the controller's, written for the person who pressed the
		// button.
		s.logger(r).Warn("the release check could not be completed", "error", err)
		writeError(w, http.StatusBadGateway, errorEnvelope{Error: errorBody{Code: codeUpdateCheckFailed, Message: err.Error()}})
		return
	}
	for _, refusal := range updateRefusalCodes {
		if errors.Is(err, refusal.err) {
			writeError(w, http.StatusConflict, errorEnvelope{Error: errorBody{Code: refusal.code, Message: err.Error()}})
			return
		}
	}
	s.fail(w, r, "working out the update", err)
}

// decodeUpdateRequest reads the optional body of POST /updates/controller.
//
// An empty body is allowed, and so is a null one, which says what no body does.
// A field the endpoint does not define is a 422 naming it, not a 400: the body is
// valid JSON and was understood, and the field is what to go back and fix. A typo
// that was ignored would be a 202 for a request that did not say what its sender
// meant. A body that is not an object at all is told what to send, in words
// that name no type of the server's.
func decodeUpdateRequest(w http.ResponseWriter, r *http.Request, into *updateControllerRequest) bool {
	if r.ContentLength == 0 {
		return true
	}
	var body json.RawMessage
	if !decodeLenient(w, r, &body) {
		return false
	}
	body = bytes.TrimSpace(body)
	if string(body) == "null" {
		return true
	}
	var fields map[string]json.RawMessage
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, &fields) != nil {
		unprocessable(w, `send a JSON object such as {"tag": "v1.3.5"}, or no body to take the newest release`, nil)
		return false
	}
	// In name order, so that a body with two mistakes is always told about the
	// same one first.
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if name != "tag" {
			unprocessable(w, "", []fieldError{{name, "this endpoint takes only a tag; remove " + name}})
			return false
		}
	}
	if raw, ok := fields["tag"]; ok {
		if err := json.Unmarshal(raw, &into.Tag); err != nil {
			unprocessable(w, "", []fieldError{{"tag", "the tag must be text such as v1.3.5, or left out for the newest release"}})
			return false
		}
	}
	return true
}
