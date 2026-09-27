package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
)

// ServeHTTP answers one message of MCP's Streamable HTTP transport: a POST
// carrying a single JSON-RPC message, answered with a single JSON body.
//
// It is stateless on purpose. There is no session ID and no event stream:
// every tool here is one request and one answer, so there is nothing a server
// would ever push, and a session would be state the controller holds for a
// client that may never come back. A cancellation is the client closing the
// request, which cancels its context; notifications/cancelled names a request
// on another connection and is accepted and ignored, as it is for one that has
// already finished.
//
// Authentication, and the Origin check the specification asks for, are the
// caller's: they are the controller's rules, and this package knows nothing of
// credentials beyond the API it was handed.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpError(w, http.StatusMethodNotAllowed, "this endpoint takes one MCP message per POST and offers no event stream to "+r.Method)
		return
	}
	if v := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); v != "" && !slices.Contains(ProtocolVersions, v) {
		httpError(w, http.StatusBadRequest, "MCP protocol version "+v+" is not one this server speaks; it speaks "+strings.Join(ProtocolVersions, ", "))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxMessage+1))
	if err != nil {
		httpError(w, http.StatusBadRequest, "reading the message: "+err.Error())
		return
	}
	if len(body) > MaxMessage {
		httpError(w, http.StatusRequestEntityTooLarge, "a message may be at most 4 MiB")
		return
	}

	req, rerr := parse(body)
	if rerr != nil {
		writeReply(w, http.StatusBadRequest, rpcResponse{ID: json.RawMessage("null"), Error: rerr})
		return
	}
	if req.Method == "" || req.notification() {
		// A response to nothing we asked, or a notification: the
		// specification's answer to both is 202 with no body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.handle(r.Context(), req)
	writeReply(w, http.StatusOK, rpcResponse{ID: req.ID, Result: result, Error: rerr})
}

func writeReply(w http.ResponseWriter, status int, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encode(resp))
}

// httpError refuses a request that never became a JSON-RPC message, in the
// JSON-RPC shape anyway: a client reading the body finds a sentence where it
// expected one, whichever layer refused it.
func httpError(w http.ResponseWriter, status int, msg string) {
	writeReply(w, status, rpcResponse{ID: json.RawMessage("null"), Error: &RPCError{rpcInvalidRequest, msg}})
}
