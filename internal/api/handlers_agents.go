package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// agentHost returns the host an agent request authenticated as.
func agentHost(r *http.Request) *store.Host {
	h, _ := r.Context().Value(ctxAgentHost).(*store.Host)
	return h
}

// handleAgentJoin redeems a join token and enrols a host.
//
// It is the one anonymous agent route, because it is the call that mints the
// credential every other one carries. The join token is single-use and
// short-lived, and the agent token it returns is shown exactly once.
func (s *Server) handleAgentJoin(w http.ResponseWriter, r *http.Request) {
	var req agent.JoinRequest
	if !decode(w, r, &req) {
		return
	}
	resp, err := s.ctrl.Join(r.Context(), req, ClientIP(r.Context()))
	if err != nil {
		// A refused join is almost always a spent or mistyped token, which is
		// the agent operator's to fix rather than an internal failure.
		s.logger(r).Warn("refused an agent join", "name", req.Name, "error", err)
		unprocessable(w, err.Error(), nil)
		return
	}
	// The host is its own actor: the join is the moment it becomes one, and an
	// audit row that says "system enrolled a host" answers a different question
	// from the one an operator is asking.
	joined := &auth.Identity{
		Kind: auth.KindAgent, ID: resp.HostID, Name: req.Name,
		Role: store.RoleViewer, IP: ClientIP(r.Context()),
	}
	s.auth.Auditor().Act(r.Context(), joined, "host.join", "host", resp.HostID, map[string]any{
		"name": req.Name, "os": req.OS, "arch": req.Arch, "capacity": req.Capacity,
		"version": req.Version, "ip": ClientIP(r.Context()),
	})
	s.ctrl.Nudge()
	writeJSON(w, http.StatusOK, resp)
}

// handleAgentHeartbeat records that a host is alive.
//
// A host the controller no longer knows about is a 404, and the agent's
// transport turns that specific status into "re-join me": no amount of retrying
// brings back a row an operator deleted.
func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	host := agentHost(r)
	var req agent.HeartbeatRequest
	if !decode(w, r, &req) {
		return
	}
	resp, err := s.ctrl.Heartbeat(r.Context(), host.ID, req)
	if err != nil {
		if errors.Is(err, agent.ErrHostGone) || errors.Is(err, store.ErrNotFound) {
			notFound(w, err.Error())
			return
		}
		s.internal(w, r, "recording a heartbeat", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAgentTasks is the long poll.
//
// It blocks until this host has work or the wait elapses, which is what makes a
// task reach an agent in the same instant it is queued while an idle agent
// makes two or three requests a minute. The request's own read deadline is
// cleared for the same reason the server's WriteTimeout is zero.
func (s *Server) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	host := agentHost(r)
	wait := agent.DefaultPollWait
	if raw := strings.TrimSpace(r.URL.Query().Get("wait")); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			wait = time.Duration(secs) * time.Second
		}
	}
	// The poll outlives an ordinary request by design, so the deadline that
	// bounds an ordinary request must not apply to it.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})

	batch, err := s.ctrl.PollTasks(r.Context(), host.ID, wait)
	if err != nil {
		s.internal(w, r, "waiting for tasks", err)
		return
	}
	if batch.Tasks == nil {
		batch.Tasks = []agent.Task{}
	}
	writeJSON(w, http.StatusOK, batch)
}

// handleAgentResult applies the outcome of one task.
func (s *Server) handleAgentResult(w http.ResponseWriter, r *http.Request) {
	host := agentHost(r)
	var res agent.TaskResult
	if !decode(w, r, &res) {
		return
	}
	if err := s.ctrl.ReportResult(r.Context(), host.ID, res); err != nil {
		s.fail(w, r, "applying a task result", err)
		return
	}
	noContent(w)
}

// handleAgentReport merges an agent's observations of its runners.
//
// The body is a bare JSON array rather than an object with one field: the agent
// sends []RunnerReport, and wrapping it here would mean the two halves of the
// protocol disagreeing about the wire format.
func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	host := agentHost(r)
	var reports []agent.RunnerReport
	if !decode(w, r, &reports) {
		return
	}
	if err := s.ctrl.ReportRunners(r.Context(), host.ID, reports); err != nil {
		s.fail(w, r, "applying runner reports", err)
		return
	}
	noContent(w)
}

// handleAgentLogs consumes an agent's outbound log relay.
//
// This is the inverted half of log streaming: the controller can never dial an
// agent, so a viewer's request makes the controller queue a task, and the agent
// answers by opening this chunked POST. The body is read for as long as the
// runner produces output, which is why it is exempt from the body-size limit
// and clears the read deadline.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	streamID := chiURLParam(r, "stream_id")
	if streamID == "" {
		badRequest(w, "a log relay needs the stream ID from the stream_logs task")
		return
	}
	// A relayed stream lasts as long as the job does.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})

	if err := s.ctrl.AcceptLogStream(streamID, r.Body); err != nil {
		if errors.Is(err, controller.ErrStreamUnknown) {
			// Nobody is watching any more -- the tab was closed between the
			// task being issued and the agent acting on it. That is ordinary,
			// and the agent stops when it sees the 404.
			notFound(w, err.Error())
			return
		}
		s.internal(w, r, "relaying a log stream", err)
		return
	}
	noContent(w)
}
