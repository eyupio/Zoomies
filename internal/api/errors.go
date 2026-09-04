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
	codeRateLimited   = "rate_limited"
	codeInternal      = "internal"
)

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
	s.logger(r).Error("request failed", "doing", doing, "error", err, "request_id", id)
	detail := ""
	if id != "" {
		detail = "request " + id
	}
	writeError(w, http.StatusInternalServerError, errorEnvelope{Error: errorBody{
		Code:    codeInternal,
		Message: "something went wrong while " + doing + ". The cause is in the controller's log; quote the request ID when reporting it.",
		Detail:  detail,
	}})
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
	default:
		s.internal(w, r, doing, err)
	}
}
