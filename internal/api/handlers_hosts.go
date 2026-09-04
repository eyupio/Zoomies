package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// backendInfoResponse describes one backend a host offers.
type backendInfoResponse struct {
	Kind      store.BackendKind `json:"kind"`
	Available bool              `json:"available"`
	Version   string            `json:"version,omitempty"`
	Rootless  bool              `json:"rootless"`
	Endpoint  string            `json:"endpoint,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	DinD      bool              `json:"supports_dind"`
}

// hostResponse is one agent host and the room it has left.
type hostResponse struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Address       string                `json:"address,omitempty"`
	Embedded      bool                  `json:"embedded"`
	Capacity      int                   `json:"capacity"`
	ActiveRunners int                   `json:"active_runners"`
	Free          int                   `json:"free"`
	Backends      []string              `json:"backends"`
	BackendInfo   []backendInfoResponse `json:"backend_info"`
	Labels        map[string]string     `json:"labels"`
	OS            string                `json:"os,omitempty"`
	Arch          string                `json:"arch,omitempty"`
	Version       string                `json:"version,omitempty"`
	Cordoned      bool                  `json:"cordoned"`
	Healthy       bool                  `json:"healthy"`
	LastHeartbeat time.Time             `json:"last_heartbeat"`
	CreatedAt     time.Time             `json:"created_at"`
}

// hostResponse renders a host as the API returns it.
//
// backend_info is derived from the kinds the agent reported: the store keeps
// only the backends a host said were *available*, so each one is reported
// available here and nothing is invented about the ones that were not. The
// richer detail an agent sends at join is not persisted, so there is nothing
// truthful to put in the version or endpoint fields.
func (s *Server) hostResponse(h *store.Host) hostResponse {
	now := s.ctrl.Now()
	out := hostResponse{
		ID:            h.ID,
		Name:          h.Name,
		Address:       h.Address,
		Embedded:      h.Embedded,
		Capacity:      h.Capacity,
		ActiveRunners: h.ActiveRunners,
		Free:          h.Free(),
		Backends:      emptySlice(h.Backends),
		Labels:        emptyMap(h.Labels),
		OS:            h.OS,
		Arch:          h.Arch,
		Version:       h.Version,
		Cordoned:      h.Cordoned,
		Healthy:       h.Healthy(now),
		LastHeartbeat: h.LastHeartbeat,
		CreatedAt:     h.CreatedAt,
	}
	out.BackendInfo = make([]backendInfoResponse, 0, len(h.Backends))
	for _, kind := range h.Backends {
		out.BackendInfo = append(out.BackendInfo, backendInfoResponse{
			Kind:      store.BackendKind(kind),
			Available: true,
		})
	}
	return out
}

// handleListHosts answers GET /api/v1/hosts.
func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.ctrl.Store().ListHosts(r.Context())
	if err != nil {
		s.internal(w, r, "listing hosts", err)
		return
	}
	out := make([]hostResponse, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, s.hostResponse(h))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// handleGetHost answers GET /api/v1/hosts/{id}.
func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.ctrl.Store().GetHost(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}
	writeJSON(w, http.StatusOK, s.hostResponse(h))
}

type hostUpdateRequest struct {
	Capacity *int               `json:"capacity"`
	Labels   *map[string]string `json:"labels"`
}

// handleUpdateHost changes a host's capacity or labels.
func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}

	var req hostUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	var fields []fieldError
	if req.Capacity != nil {
		switch {
		case *req.Capacity < 0:
			fields = append(fields, fieldError{"capacity", "capacity cannot be negative; use 0 to stop this host taking new runners"})
		case *req.Capacity < h.ActiveRunners:
			// Allowed, and worth saying plainly rather than refusing: the
			// running jobs finish, and nothing new is placed until the host is
			// back under its new capacity.
			s.logger(r).Info("a host's capacity was lowered below the number of runners it is already running",
				"host", h.ID, "capacity", *req.Capacity, "active", h.ActiveRunners)
		}
	}
	if req.Labels != nil {
		for k := range *req.Labels {
			if strings.TrimSpace(k) == "" {
				fields = append(fields, fieldError{"labels", "a label needs a name"})
				break
			}
		}
	}
	if len(fields) > 0 {
		unprocessable(w, "this host cannot be changed as described", fields)
		return
	}

	before := *h
	if req.Capacity != nil {
		h.Capacity = *req.Capacity
	}
	if req.Labels != nil {
		h.Labels = store.StringMap(*req.Labels)
	}
	if err := s.ctrl.Store().UpdateHost(r.Context(), h); err != nil {
		s.fail(w, r, "saving the host", err)
		return
	}

	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "host", id, &before, h)
	// Capacity and labels both decide where runners may be placed.
	s.ctrl.Nudge()
	writeJSON(w, http.StatusOK, s.hostResponse(h))
}

type cordonRequest struct {
	Cordoned bool `json:"cordoned"`
}

// handleCordonHost cordons or uncordons a host.
//
// A cordoned host keeps everything it is already running: cordoning is how an
// operator drains a machine before maintenance without interrupting the jobs on
// it, and killing them would defeat the purpose.
func (s *Server) handleCordonHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}

	var req cordonRequest
	if !decode(w, r, &req) {
		return
	}
	if h.Cordoned != req.Cordoned {
		if err := s.ctrl.Store().SetHostCordoned(r.Context(), id, req.Cordoned); err != nil {
			s.fail(w, r, "cordoning the host", err)
			return
		}
		h.Cordoned = req.Cordoned
		action := "host.uncordon"
		if req.Cordoned {
			action = "host.cordon"
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "host", id, map[string]any{
			"name": h.Name, "cordoned": req.Cordoned, "active_runners": h.ActiveRunners,
		})
		s.ctrl.Nudge()
	}
	writeJSON(w, http.StatusOK, s.hostResponse(h))
}

// handleDeleteHost removes a host.
//
// It refuses while the host still has live runners unless forced, because
// deleting the row cascades to those runners and their workloads would be left
// running on a machine Zoomies no longer knows about.
func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	force := queryBool(r, "force", false)

	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}
	live, err := s.ctrl.Store().ListRunnersForHost(r.Context(), id)
	if err != nil {
		s.internal(w, r, "listing the host's runners", err)
		return
	}
	alive := 0
	for _, run := range live {
		if !run.State.Terminal() {
			alive++
		}
	}
	if alive > 0 && !force {
		conflict(w, fmt.Sprintf("host %s is still running %d runner(s); cordon it and wait for them to finish, or repeat this with ?force=true to remove it and leave those workloads to be cleaned up by their agent", h.Name, alive))
		return
	}

	if err := s.ctrl.Store().DeleteHost(r.Context(), id); err != nil {
		s.fail(w, r, "deleting the host", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "host", id, h)
	s.ctrl.Nudge()
	noContent(w)
}

// ---------------------------------------------------------------------------
// Join tokens
// ---------------------------------------------------------------------------

// joinTokenResponse never carries the secret. The prefix is enough to recognise
// a token in a list and to match a leaked string to a row.
type joinTokenResponse struct {
	ID        string            `json:"id"`
	Prefix    string            `json:"prefix"`
	CreatedBy string            `json:"created_by,omitempty"`
	Capacity  int               `json:"capacity"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	UsedAt    *time.Time        `json:"used_at"`
	UsedByID  string            `json:"used_by_id,omitempty"`
	Usable    bool              `json:"usable"`
}

// createJoinTokenResponse adds the two things that exist exactly once: the
// plaintext token, and the command to paste on the new host.
type createJoinTokenResponse struct {
	joinTokenResponse
	Token   string `json:"token"`
	Command string `json:"command"`
}

func (s *Server) joinTokenResponse(t *store.JoinToken) joinTokenResponse {
	return joinTokenResponse{
		ID:        t.ID,
		Prefix:    t.Prefix,
		CreatedBy: t.CreatedBy,
		Capacity:  t.Capacity,
		Labels:    emptyMap(t.Labels),
		CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt,
		UsedAt:    t.UsedAt,
		UsedByID:  t.UsedByID,
		Usable:    t.Usable(s.ctrl.Now()),
	}
}

// handleListJoinTokens answers GET /api/v1/join-tokens.
func (s *Server) handleListJoinTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.ctrl.Store().ListJoinTokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing join tokens", err)
		return
	}
	out := make([]joinTokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, s.joinTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

type createJoinTokenRequest struct {
	TTL      string            `json:"ttl"`
	Capacity int               `json:"capacity"`
	Labels   map[string]string `json:"labels"`
}

// handleCreateJoinToken mints a single-use enrolment credential.
func (s *Server) handleCreateJoinToken(w http.ResponseWriter, r *http.Request) {
	req := createJoinTokenRequest{TTL: "15m", Capacity: 2}
	if !decodeOptional(w, r, &req) {
		return
	}

	var fields []fieldError
	ttl := 15 * time.Minute
	if raw := strings.TrimSpace(req.TTL); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fields = append(fields, fieldError{"ttl", fmt.Sprintf("%q is not a duration; write it like 15m, 1h or 24h", raw)})
		case d <= 0:
			fields = append(fields, fieldError{"ttl", "a join token has to be usable for some length of time; try 15m"})
		default:
			ttl = d
		}
	}
	if req.Capacity < 0 {
		fields = append(fields, fieldError{"capacity", "capacity cannot be negative; leave it at 0 to let the agent decide from the host's CPU count"})
	}
	if len(fields) > 0 {
		unprocessable(w, "this join token could not be created", fields)
		return
	}

	id := Identity(r.Context())
	createdBy := ""
	if id != nil {
		createdBy = id.Name
	}
	token, plaintext, err := s.auth.CreateJoinToken(r.Context(), ttl, req.Labels, req.Capacity, createdBy)
	if err != nil {
		s.internal(w, r, "creating the join token", err)
		return
	}
	// The plaintext is deliberately not in the audit row: the row records that
	// a credential was minted, not the credential.
	s.auth.Auditor().Created(r.Context(), id, "join_token", token.ID, token)

	writeJSON(w, http.StatusCreated, createJoinTokenResponse{
		joinTokenResponse: s.joinTokenResponse(token),
		Token:             plaintext,
		Command:           s.joinCommand(plaintext),
	})
}

// joinCommand renders the one-liner for the new host.
//
// It names the controller's external URL because that is the address the agent
// has to reach; when it is not configured the command is still printed, with
// the placeholder in it, since an operator who has not set it yet needs to see
// what is missing rather than a blank field.
func (s *Server) joinCommand(token string) string {
	controller := s.cfg.Server.ExternalURL
	if controller == "" {
		controller = "https://<this-controller>"
	}
	return fmt.Sprintf("curl -fsSL https://zoomies.sh/install.sh | sh -s -- --mode agent --controller %s --join-token %s",
		controller, token)
}

// handleDeleteJoinToken revokes an unused join token.
func (s *Server) handleDeleteJoinToken(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	tokens, err := s.ctrl.Store().ListJoinTokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing join tokens", err)
		return
	}
	var found *store.JoinToken
	for _, t := range tokens {
		if t.ID == id {
			found = t
			break
		}
	}
	if found == nil {
		notFound(w, "there is no join token "+id+"; it may already have been used or revoked")
		return
	}
	if err := s.ctrl.Store().DeleteJoinToken(r.Context(), id); err != nil {
		s.fail(w, r, "revoking the join token", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "join_token", id, found)
	noContent(w)
}
