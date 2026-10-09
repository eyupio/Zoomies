package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
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

// maxRolloutBodyBytes bounds the body of POST /updates/hosts. A list of host ids
// for a fleet of thousands fits in it many times over, and the controller reads
// every id against the fleet, so the bound is the server's general one cut to
// what a list a person chose can need.
const maxRolloutBodyBytes = 64 << 10

// handleStartRollout answers POST /api/v1/updates/hosts: start a rollout of every
// host behind this controller's release, or of the hosts the body names.
//
// It answers 202, because nothing is asked of a host here: the planner moves the
// rollout on from its next pass, one host at a time, and the page follows it in
// updates.updated. One press writes one rollout row and no request, so it can
// never spend the helpers' start limit on its own.
func (s *Server) handleStartRollout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRolloutBodyBytes)
	hostIDs, ok := decodeRolloutRequest(w, r)
	if !ok {
		return
	}
	id := Identity(r.Context())
	status, err := s.ctrl.StartHostRollout(r.Context(), controller.UpdateActor{ID: id.ID, Name: id.Name}, hostIDs)
	if errors.Is(err, store.ErrNotFound) {
		// The only thing a start does not find is a host the body named, so it is
		// the body to fix, and the controller's sentence names the id. The
		// sentinel's own words are no use to the person reading it.
		msg := strings.TrimPrefix(err.Error(), store.ErrNotFound.Error()+": ")
		unprocessable(w, msg, []fieldError{{"host_ids", msg}})
		return
	}
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	detail := map[string]any{}
	if len(hostIDs) > 0 {
		detail["host_ids"] = dedupe(hostIDs)
	}
	s.auditRollout(r, "update.rollout_started", status, detail)
	writeJSON(w, http.StatusAccepted, status.For(isPlatform(r)))
}

// handleResumeRollout answers POST /api/v1/updates/rollout/resume: let a halted
// rollout carry on. It answers 202 for the same reason a start does: the next
// host is asked on the planner's pass, not here.
func (s *Server) handleResumeRollout(w http.ResponseWriter, r *http.Request) {
	if !decodeNoUpdateBody(w, r) {
		return
	}
	id := Identity(r.Context())
	status, err := s.ctrl.ResumeRollout(r.Context(), controller.UpdateActor{ID: id.ID, Name: id.Name})
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	s.auditRollout(r, "update.rollout_resumed", status, map[string]any{})
	writeJSON(w, http.StatusAccepted, status.For(isPlatform(r)))
}

// handleCancelRollout answers DELETE /api/v1/updates/rollout: end the open
// rollout. It answers 200, because the cancel is done when it returns; an update
// a helper was already handed finishes by itself and is recorded.
func (s *Server) handleCancelRollout(w http.ResponseWriter, r *http.Request) {
	id := Identity(r.Context())
	status, err := s.ctrl.CancelRollout(r.Context(), controller.UpdateActor{ID: id.ID, Name: id.Name})
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	s.auditRollout(r, "update.rollout_cancelled", status, map[string]any{})
	writeJSON(w, http.StatusOK, status.For(isPlatform(r)))
}

// auditRollout writes the row for a person's press once the controller has said
// yes. The controller writes none for a person, so this is the only record of who
// started, resumed or stopped a rollout. Detached from the request, because the
// rollout has changed whether or not the browser is still waiting.
func (s *Server) auditRollout(r *http.Request, action string, status *controller.UpdatesView, detail map[string]any) {
	targetID := ""
	if ro := status.Rollout; ro != nil {
		targetID, detail["to"] = ro.ID, ro.Target
	}
	s.auth.Auditor().Act(context.WithoutCancel(r.Context()), Identity(r.Context()), action, "update", targetID, detail)
}

// dedupe is ids with each kept once, in the order first given, as the controller
// reads them.
func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// decodeRolloutRequest reads the optional body of POST /updates/hosts: no body,
// or one without host_ids, is every host behind; host_ids is the hosts to take.
//
// A list that is there must name at least one host, each by a non-empty id. An
// empty or null list is refused rather than read as every host, because a client
// whose selection came out empty asked for none, and walking the whole fleet is
// the opposite of that.
func decodeRolloutRequest(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	const usage = `send a JSON object such as {"host_ids": ["hst_k3f9qz2m"]}, or no body to update every host behind`
	fields, ok := readUpdateFields(w, r, usage)
	if !ok {
		return nil, false
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if name != "host_ids" {
			unprocessable(w, "", []fieldError{{name, "this endpoint takes only host_ids; remove " + name}})
			return nil, false
		}
	}
	raw, ok := fields["host_ids"]
	if !ok {
		return nil, true
	}
	const want = "host_ids must be a list of host ids, such as [\"hst_k3f9qz2m\"]; leave it out to update every host behind"
	var ids []string
	if string(bytes.TrimSpace(raw)) == "null" || json.Unmarshal(raw, &ids) != nil {
		unprocessable(w, "", []fieldError{{"host_ids", want}})
		return nil, false
	}
	if len(ids) == 0 {
		unprocessable(w, "", []fieldError{{"host_ids", "host_ids names no host; name at least one, or leave it out to update every host behind"}})
		return nil, false
	}
	if slices.Contains(ids, "") {
		unprocessable(w, "", []fieldError{{"host_ids", "host_ids holds an empty id; every entry must be a host id, as zoomies hosts list shows"}})
		return nil, false
	}
	return ids, true
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
// meant.
func decodeUpdateRequest(w http.ResponseWriter, r *http.Request, into *updateControllerRequest) bool {
	fields, ok := readUpdateFields(w, r, `send a JSON object such as {"tag": "v1.3.5"}, or no body to take the newest release`)
	if !ok {
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

// decodeNoUpdateBody reads the body of POST /hosts/{id}/update, which has none to
// give: a host is taken to the release the controller runs, and which host is in
// the path. A body that would have edited the host the way PATCH does, or named a
// release the way the controller's route does, is refused with the field named and
// not ignored, because a 202 would tell its sender the host was changed as asked.
func decodeNoUpdateBody(w http.ResponseWriter, r *http.Request) bool {
	fields, ok := readUpdateFields(w, r, "send no body, or an empty JSON object")
	if !ok {
		return false
	}
	if len(fields) > 0 {
		// The first in name order, so that a body with two mistakes is always told
		// about the same one.
		name := slices.Sorted(maps.Keys(fields))[0]
		unprocessable(w, "", []fieldError{{name, "this endpoint takes no body; remove " + name}})
		return false
	}
	return true
}

// readUpdateFields reads the JSON object a body holds as its fields, none for no
// body or a null one. A body that is not an object at all is told what to send
// (usage), in words that name no type of the server's.
func readUpdateFields(w http.ResponseWriter, r *http.Request, usage string) (map[string]json.RawMessage, bool) {
	if r.ContentLength == 0 {
		return nil, true
	}
	var body json.RawMessage
	if !decodeLenient(w, r, &body) {
		return nil, false
	}
	body = bytes.TrimSpace(body)
	if string(body) == "null" {
		return nil, true
	}
	var fields map[string]json.RawMessage
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, &fields) != nil {
		unprocessable(w, usage, nil)
		return nil, false
	}
	return fields, true
}
