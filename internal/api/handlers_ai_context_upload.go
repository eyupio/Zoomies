package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// handleAIContextUpload receives a Zoomies-only snapshot from the managed
// workflow. It has no session and no API token: the only credential is the
// run's GitHub Actions OIDC token, minted for this controller's upload address,
// which the controller checks before it reads anything else the request says.
// It is mounted outside the API's body limit because a snapshot can be up to
// aicontext.MaxSnapshotBytes; this handler bounds the body itself.
func (s *Server) handleAIContextUpload(w http.ResponseWriter, r *http.Request) {
	audience := aicontext.UploadURLFor(s.cfg().Server.ExternalURL)
	if audience == "" {
		notFound(w, "this controller accepts Zoomies-only uploads only when server.external_url is an https address GitHub's runners can reach")
		return
	}
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		unauthorized(w, "send the workflow run's GitHub Actions OIDC token, minted for this upload address, as a bearer token")
		return
	}
	// The token first: nobody without one gets the controller to buffer a body.
	if err := s.ctrl.CheckAIContextUploadToken(r.Context(), raw, audience); err != nil {
		if errors.Is(err, github.ErrActionsKeysUnavailable) {
			actionsKeysUnavailable(w, err)
			return
		}
		if errors.Is(err, github.ErrActionsToken) {
			unauthorized(w, "the GitHub Actions OIDC token is not valid for this controller: check that the workflow requests it for this upload address and that it has not expired")
			return
		}
		s.fail(w, r, "verifying the upload's GitHub Actions token", err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, aicontext.MaxSnapshotBytes+1))
	if err != nil {
		badRequest(w, "the upload body could not be read")
		return
	}
	if len(body) > aicontext.MaxSnapshotBytes {
		payloadTooLarge(w, "the snapshot is larger than 32 MiB; add exclusions to the repository's AI Context configuration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := s.ctrl.IngestAIContextUpload(ctx, raw, audience, body)
	switch {
	case err == nil:
	case errors.Is(err, github.ErrActionsKeysUnavailable):
		actionsKeysUnavailable(w, err)
		return
	case errors.Is(err, github.ErrActionsToken):
		unauthorized(w, "the GitHub Actions OIDC token is not valid for this controller: check that the workflow requests it for this upload address and that it has not expired")
		return
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "no repository on this controller is set up for Zoomies-only output with this GitHub repository ID")
		return
	case errors.Is(err, controller.ErrAIContextUploadSuperseded):
		conflict(w, err.Error())
		return
	case errors.Is(err, controller.ErrAIContextUploadRefused):
		forbidden(w, err.Error())
		return
	default:
		s.fail(w, r, "verifying the uploaded AI context", err)
		return
	}
	var commit, snapshot string
	if out.Freshness != nil {
		commit, snapshot = out.Freshness.PublishedCommit, out.Freshness.Digest
	}
	s.auth.Auditor().Act(r.Context(), &auth.Identity{Kind: auth.KindSystem, Name: "github-actions"}, "context.upload", "ai_context", out.ID,
		map[string]any{"commit": commit, "snapshot_id": snapshot})
	writeJSON(w, http.StatusAccepted, map[string]any{"repository_id": out.ID, "commit": commit, "snapshot_id": snapshot})
}

// actionsKeysUnavailable is a 503 rather than a 401: the token was not judged,
// and the run's log should send the operator to this controller's network.
func actionsKeysUnavailable(w http.ResponseWriter, err error) {
	host := "token.actions.githubusercontent.com"
	var keys *github.ActionsKeysError
	if errors.As(err, &keys) {
		host = keys.Host
	}
	writeError(w, http.StatusServiceUnavailable, errorEnvelope{Error: errorBody{
		Code:    codeInternal,
		Message: "this controller could not fetch GitHub's Actions signing keys from " + host + ", so the upload's token could not be checked; allow outbound https to that host and re-run the workflow",
	}})
}
