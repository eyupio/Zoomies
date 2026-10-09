// Package api is Zoomies' HTTP surface: the REST API described by
// api/openapi.yaml, the Server-Sent Events streams the UI lives on, the
// Prometheus endpoint, the GitHub webhook receiver, and the embedded operator
// UI.
//
// Everything here is transport. Decisions about the fleet belong to
// internal/controller, authorisation policy to internal/auth and SQL to
// internal/store; a handler in this package reads a request, asks one of those
// three, and renders the answer in the shape the OpenAPI document promises.
// Keeping it that thin is what makes the contract checkable: the surface can be
// walked route by route in a test, and nothing behind it has an opinion about
// HTTP.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// Error codes. They are the enum in the OpenAPI document's ErrorEnvelope, and
// the UI switches on them, so they are part of the contract rather than free
// text.
const (
	codeBadRequest    = "bad_request"
	codeUnauthorized  = "unauthorized"
	codeForbidden     = "forbidden"
	codeNotFound      = "not_found"
	codeConflict      = "conflict"
	codeUnprocessable = "unprocessable"
	codeTooLarge      = "too_large"
	codeRateLimited   = "rate_limited"
	codeLimitReached  = "limit_reached"
	codeInternal      = "internal"
)

// The refusals of an update. Each is a 409 with a code of its own because a
// client does something different about each: a page offers the Settings link
// for one, the install command for another, and waits for a third.
const (
	codeUpdateModeOff          = "update.mode_off"
	codeUpdateCheckDisabled    = "update.check_disabled"
	codeUpdateHelperMissing    = "update.helper_missing"
	codeUpdateInProgress       = "update.in_progress"
	codeUpdateNotARelease      = "update.not_a_release"
	codeUpdateNothingNewer     = "update.nothing_newer"
	codeUpdateHostCannotUpdate = "update.host_cannot_update"
	codeUpdateRolloutHalted    = "update.rollout_halted"
)

// statusClientClosed is nginx's 499: the client hung up before the response.
// No client ever reads it -- by then there is none -- but the access log
// separates a request the browser abandoned from one the controller failed.
const statusClientClosed = 499

// fieldError names one thing wrong with a request body, in the words the form
// field it belongs to should display.
type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// errorBody is the inner object of the error envelope.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// errorEnvelope is every failure this API returns.
//
// The message is written for a person to read and act on, which is why nothing
// in this package answers with a bare status code: an operator who gets a 403
// from a script needs to be told which role they are missing, not left to guess.
type errorEnvelope struct {
	Error errorBody `json:"error"`
	// Errors carries per-field messages on a 422, so a form can attach each one
	// to the input that caused it.
	Errors []fieldError `json:"errors,omitempty"`
}

// writeJSON renders v as the whole response body.
//
// The value is marshalled before anything is written, so a value that cannot be
// encoded produces a 500 rather than a truncated 200 that a client would parse
// as a successful, empty answer.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Default().Error("could not encode an API response", "error", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"the server built a response it could not encode; check the controller logs"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)+1))
	w.WriteHeader(status)
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n"))
}

// noContent answers a mutation that has nothing to return.
func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func writeError(w http.ResponseWriter, status int, e errorEnvelope) {
	writeJSON(w, status, e)
}

// badRequest is for a request this server could not parse at all.
func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{Code: codeBadRequest, Message: message}})
}

// badRequestField is badRequest when one named parameter is at fault, which is
// what a query-string mistake usually is.
func badRequestField(w http.ResponseWriter, field, message string) {
	writeError(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
		Code: codeBadRequest, Message: message, Field: field,
	}})
}

// payloadTooLarge is the refusal for a body over the limit.
//
// It is 413 rather than 400 because the two say different things to a client:
// a 400 invites it to fix its JSON, and there is nothing wrong with the JSON.
// The webhook endpoint has always answered 413 for the same condition, so this
// is also the two halves of the surface agreeing.
func payloadTooLarge(w http.ResponseWriter, message string) {
	writeError(w, http.StatusRequestEntityTooLarge, errorEnvelope{Error: errorBody{
		Code: codeTooLarge, Message: message,
	}})
}

// unauthorized means "no usable credential", never "wrong role".
func unauthorized(w http.ResponseWriter, message string) {
	if message == "" {
		message = "sign in to use this API"
	}
	writeError(w, http.StatusUnauthorized, errorEnvelope{Error: errorBody{Code: codeUnauthorized, Message: message}})
}

// forbidden means the caller is known but not allowed. The message comes from
// auth.Explain, which names the role or scope that is missing.
func forbidden(w http.ResponseWriter, message string) {
	if message == "" {
		message = "your account is not allowed to do that"
	}
	writeError(w, http.StatusForbidden, errorEnvelope{Error: errorBody{Code: codeForbidden, Message: message}})
}

func notFound(w http.ResponseWriter, message string) {
	if message == "" {
		message = "no such thing; it may have been removed since the page loaded"
	}
	writeError(w, http.StatusNotFound, errorEnvelope{Error: errorBody{Code: codeNotFound, Message: message}})
}

func conflict(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, errorEnvelope{Error: errorBody{Code: codeConflict, Message: message}})
}

// limitReached is the refusal from one of the limits.* ceilings. It is a 409
// with a code of its own rather than a plain conflict because a client can do
// something different about it -- nothing it resends will help until somebody
// removes a resource or raises the setting the message names -- and the
// setting travels in field so a script need not parse the sentence.
func limitReached(w http.ResponseWriter, err *controller.LimitError) {
	writeError(w, http.StatusConflict, errorEnvelope{Error: errorBody{
		Code: codeLimitReached, Message: err.Error(), Field: err.Setting,
	}})
}

// asLimit reports whether err is a limits.* refusal, and answers it if so.
func asLimit(w http.ResponseWriter, err error) bool {
	var le *controller.LimitError
	if errors.As(err, &le) {
		limitReached(w, le)
		return true
	}
	return false
}

// unprocessable is the validation failure: the request was understood and is
// wrong. Every caller passes the fields, because "invalid request" tells an
// operator nothing about which box to go back and fix.
func unprocessable(w http.ResponseWriter, message string, fields []fieldError) {
	if message == "" {
		message = "that is not a valid request"
	}
	env := errorEnvelope{Error: errorBody{Code: codeUnprocessable, Message: message}, Errors: fields}
	if len(fields) == 1 {
		env.Error.Field = fields[0].Field
	}
	writeError(w, http.StatusUnprocessableEntity, env)
}

// rateLimited answers a caller that has been trying too often. Retry-After is
// set so a client can wait exactly as long as it needs to.
func rateLimited(w http.ResponseWriter, message string, retryAfter time.Duration) {
	if secs := int(retryAfter.Seconds()); secs > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	}
	writeError(w, http.StatusTooManyRequests, errorEnvelope{Error: errorBody{Code: codeRateLimited, Message: message}})
}

// internal logs the cause and returns a message that carries only the request
// ID.
//
// The cause is what an operator needs and an attacker would like, so it goes to
// the log; the request ID goes to the client, which is enough to find the log
// line. doing is a present participle -- "listing pools" -- so the log line and
// the response read as sentences.
func (s *Server) internal(w http.ResponseWriter, r *http.Request, doing string, err error) {
	info := infoFrom(r.Context())
	id := ""
	if info != nil {
		id = info.id
	}
	// A browser that leaves the page cancels whatever it still had in flight,
	// and the query comes back context.Canceled. Nothing is wrong with the
	// controller and nobody is left to read a 500, so it is noted at debug:
	// at error level a page of them teaches an operator to scroll past the
	// failures that do need them.
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		s.logger(r).Debug("the client went away before its response", "doing", doing, "request_id", id)
		w.WriteHeader(statusClientClosed)
		return
	}
	s.logger(r).Error("request failed", "doing", doing, "error", err, "request_id", id)
	detail := ""
	if id != "" {
		detail = "request " + id
	}
	writeError(w, http.StatusInternalServerError, errorEnvelope{Error: errorBody{
		Code:    codeInternal,
		Message: "something went wrong while " + doing + ". " + supportHint(),
		Detail:  detail,
	}})
}

// supportHint is the one sentence every "something went wrong" ends with.
//
// It used to say the cause was in the controller's log, which assumes the
// reader can read that log. On an instance one team operates while another
// uses the fleet, they cannot: the log belongs to the process, and the person
// reading this runs pools on it. So it names the request ID and who to give it
// to, which is true for both -- an operator running their own instance is
// "whoever operates this instance", and the log is theirs to open.
//
// One function rather than nine sentences, because nine sentences is how the
// ninth came to say something the other eight had stopped saying.
func supportHint() string {
	return "Quote the request ID to whoever operates this instance; the cause is in its log."
}

// fail maps the errors the store and the controller return onto status codes.
//
// It exists so that no handler has to remember that a runner in a terminal
// state comes back as store.ErrInvalidTransition and must be a 409: the mapping
// is stated once, here, and every handler funnels its unexpected errors through
// it.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, doing string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, err.Error())
	case errors.Is(err, store.ErrConflict):
		conflict(w, err.Error())
	case errors.Is(err, store.ErrInvalidTransition):
		conflict(w, err.Error())
	case errors.Is(err, controller.ErrStreamUnknown):
		notFound(w, err.Error())
	case errors.Is(err, controller.ErrConfirmationRequired):
		// Not a failure: the operator asked for something whose cost they have
		// not accepted yet. 409 rather than 422, because the request is well
		// formed and it is the runner's state that decides.
		conflict(w, err.Error()+". Send it again with confirm=true to accept that, "+
			"or wait for the job to finish and drain it then.")
	default:
		s.internal(w, r, doing, err)
	}
}
