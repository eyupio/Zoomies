package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/version"
)

// metaResponse is what the login page reads before anyone has signed in: it is
// what decides whether to show a password form, a single sign-on button, or the
// first-run bootstrap form. Nothing in it is a secret.
type metaResponse struct {
	Version           string `json:"version"`
	Commit            string `json:"commit,omitempty"`
	BootstrapRequired bool   `json:"bootstrap_required"`
	AuthDisabled      bool   `json:"auth_disabled"`
	OIDCEnabled       bool   `json:"oidc_enabled"`
	OIDCLabel         string `json:"oidc_label,omitempty"`
	ExternalURL       string `json:"external_url,omitempty"`
	WebhookURL        string `json:"webhook_url,omitempty"`
	PollingOnly       bool   `json:"polling_only"`
}

// handleMeta answers GET /api/v1/meta.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	needsBootstrap, err := s.auth.NeedsBootstrap(r.Context())
	if err != nil {
		s.internal(w, r, "checking whether this instance has any accounts", err)
		return
	}
	out := metaResponse{
		Version:           version.Short(),
		Commit:            version.Commit,
		BootstrapRequired: needsBootstrap,
		AuthDisabled:      s.cfg().Security.DisableAuth,
		OIDCEnabled:       s.oidc.Enabled(),
		ExternalURL:       s.cfg().Server.ExternalURL,
		WebhookURL:        s.cfg().WebhookURL(),
		PollingOnly:       s.ctrl.PollingOnly(),
	}
	if out.OIDCEnabled {
		out.OIDCLabel = oidcLabel(s.cfg().OIDC.Issuer)
	}
	writeJSON(w, http.StatusOK, out)
}

// oidcLabel turns an issuer URL into the words on the sign-in button. There is
// no setting for it: the issuer's host is what an operator recognises, and one
// more string to keep in step with the identity provider is one more thing to
// get wrong.
func oidcLabel(issuer string) string {
	host := issuer
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		host = u.Host
	}
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "Sign in with single sign-on"
	}
	return "Sign in with " + host
}

// handleHealthz is liveness: the process is up and serving. It deliberately
// touches nothing else, so that a database problem takes the instance out of
// readiness without making a service manager restart a controller that is
// perfectly capable of reporting the problem.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"ok":true}`+"\n")
}

// handleReadyz is readiness: the database answers, which after Open means the
// migrations applied too.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	if _, err := s.ctrl.Store().CountUsers(ctx); err != nil {
		// The cause goes to the log. This route is anonymous, and a database
		// error names paths and drivers that are nobody else's business.
		s.logger(r).Warn("readiness probe failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"message": "the database is not answering; the cause is in the controller's log",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Short()})
}

// ---------------------------------------------------------------------------
// The specification
// ---------------------------------------------------------------------------

var (
	specOnce  sync.Once
	specBytes []byte
	specErr   error
)

// openapiSpec decompresses the generated copy of api/openapi.yaml once.
func openapiSpec() ([]byte, error) {
	specOnce.Do(func() {
		raw, err := base64.StdEncoding.DecodeString(openapiSpecGzB64)
		if err != nil {
			specErr = err
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			specErr = err
			return
		}
		defer zr.Close()
		specBytes, specErr = io.ReadAll(zr)
	})
	return specBytes, specErr
}

// handleOpenAPI serves the contract this build implements.
//
// It is unauthenticated on purpose: a client that cannot yet sign in still
// needs to know what it is talking to, and the document describes the surface
// rather than anything on it.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	spec, err := openapiSpec()
	if err != nil {
		s.internal(w, r, "decoding the embedded OpenAPI document", err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, "openapi.yaml", buildTime(), bytes.NewReader(spec))
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// metricsHandler serves the Prometheus registry.
//
// It needs the viewer role by default, because repository, workflow and pool
// names appear in the label set and an unauthenticated /metrics publishes the
// shape of somebody's engineering organisation. metrics.public turns the check
// off for a Prometheus that cannot hold a token; the configuration validator
// warns about it, and so does the problems drawer.
func (s *Server) metricsHandler() http.Handler {
	h := promhttp.HandlerFor(s.ctrl.Registry(), promhttp.HandlerOpts{
		ErrorLog:          promLogger{s.log},
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	})
	if s.cfg().Metrics.Public {
		return h
	}
	return s.authenticate(s.require(auth.ActionMetricsRead)(h))
}

// promLogger adapts slog to the interface promhttp wants.
type promLogger struct{ log *slog.Logger }

func (l promLogger) Println(v ...any) {
	l.log.Warn("gathering metrics", "error", fmt.Sprint(v...))
}

// contextWithTimeout bounds a probe so a wedged database cannot hold a health
// check open until the load balancer's own timeout.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
