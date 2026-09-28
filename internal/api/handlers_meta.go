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
	TailcatAvailable  bool   `json:"tailcat_available"`
	Version           string `json:"version"`
	VersionChannel    string `json:"version_channel,omitempty"`
	Commit            string `json:"commit,omitempty"`
	BootstrapRequired bool   `json:"bootstrap_required"`
	AuthDisabled      bool   `json:"auth_disabled"`
	OIDCEnabled       bool   `json:"oidc_enabled"`
	OIDCLabel         string `json:"oidc_label,omitempty"`
	// PasswordLoginHidden says the sign-in page should offer single sign-on
	// alone. It is true only while single sign-on is working, so a provider
	// that is down never leaves the page with no way in; an administrator
	// can still ask for the form with /login?password.
	PasswordLoginHidden bool   `json:"password_login_hidden,omitempty"`
	ExternalURL         string `json:"external_url,omitempty"`
	WebhookURL          string `json:"webhook_url,omitempty"`
	PollingOnly         bool   `json:"polling_only"`
	// PollerEnabled and PollerLastPollAt say whether the safety net is there
	// and when it last ran. They sit beside polling_only because they are the
	// same class of fact about this deployment, and none of the three tells an
	// unauthenticated visitor anything they could act on.
	PollerEnabled               bool       `json:"poller_enabled"`
	PollerLastPollAt            *time.Time `json:"poller_last_poll_at,omitempty"`
	WorkflowCancellationEnabled bool       `json:"workflow_cancellation_enabled"`
	// ProvidersAvailable says this deployment can rent machines: a driver to
	// rent from, and an operator who has turned the machine loop on. The nav
	// asks it so that a build with no drivers does not offer a page whose
	// every button would refuse.
	ProvidersAvailable bool `json:"providers_available"`
	// CapacityMap is how the host capacity map opens on each page that
	// carries it, as the fleet's settings say. It is here rather than on the
	// settings route because every operator's browser needs it and only an
	// administrator may read the settings; a chart layout tells an
	// unauthenticated visitor nothing they could act on.
	CapacityMap capacityMapDefaults `json:"capacity_map"`
	// QueueWarningThreshold carries ui.queue_warning_threshold: how many
	// queued jobs it takes before the queue tiles turn to their warning
	// colour. It sits here for the same reason CapacityMap does -- every
	// operator's browser needs it to render a tile, and a number is nothing
	// an unauthenticated visitor could act on.
	QueueWarningThreshold int `json:"queue_warning_threshold"`
}

// capacityMapDefaults carries ui.capacity_map.* to the browser.
type capacityMapDefaults struct {
	OverviewLayout string `json:"overview_layout"`
	HostsLayout    string `json:"hosts_layout"`
}

// handleMeta answers GET /api/v1/meta.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	needsBootstrap, err := s.auth.NeedsBootstrap(r.Context())
	if err != nil {
		s.internal(w, r, "checking whether this instance has any accounts", err)
		return
	}
	out := metaResponse{
		TailcatAvailable:            s.tailcatAvailable(),
		Version:                     version.Short(),
		VersionChannel:              version.Channel(version.Version),
		Commit:                      version.Commit,
		BootstrapRequired:           needsBootstrap,
		AuthDisabled:                s.cfg().Security.DisableAuth,
		OIDCEnabled:                 s.oidcProvider().Enabled(),
		ExternalURL:                 s.cfg().Server.ExternalURL,
		WebhookURL:                  s.cfg().WebhookURL(),
		PollingOnly:                 s.ctrl.PollingOnly(),
		PollerEnabled:               s.ctrl.PollerEnabled(),
		WorkflowCancellationEnabled: s.cfg().GitHub.AllowWorkflowCancellation,
		ProvidersAvailable:          s.ctrl.ProvidersAvailable(),
		CapacityMap: capacityMapDefaults{
			OverviewLayout: s.cfg().UI.CapacityMap.OverviewLayout,
			HostsLayout:    s.cfg().UI.CapacityMap.HostsLayout,
		},
		QueueWarningThreshold: s.cfg().UI.QueueWarningThreshold,
	}
	if last := s.ctrl.LastPollAt(); !last.IsZero() {
		out.PollerLastPollAt = &last
	}
	if out.OIDCEnabled {
		out.OIDCLabel = oidcLabel(s.cfg().OIDC.Label, s.cfg().OIDC.Issuer)
		out.PasswordLoginHidden = s.passwordLoginHidden()
	}
	writeJSON(w, http.StatusOK, out)
}

// oidcLabel is the words on the sign-in button: oidc.label when an operator
// has chosen some, and otherwise the issuer's host, which is what an operator
// recognises without being told.
func oidcLabel(label, issuer string) string {
	if label = strings.TrimSpace(label); label != "" {
		return label
	}
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
	// The ledger is the readiness question itself: a database that answers
	// and has taken every migration is one this build can serve. It is also
	// what "schema version" means here, so the probe says which.
	applied, err := s.ctrl.Store().AppliedMigrations(ctx)
	if err != nil {
		// The cause goes to the log. This route is anonymous, and a database
		// error names paths and drivers that are nobody else's business.
		s.logger(r).Warn("readiness probe failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"message": "the database is not answering; the cause is in the controller's log",
		})
		return
	}
	body := map[string]any{"ok": true, "version": version.Short()}
	if n := len(applied); n > 0 {
		body["schema"] = map[string]any{"applied": n, "latest": applied[n-1].Name}
	}
	// Whether anybody can sign in yet, so a provisioner asks one endpoint "can
	// I use this" rather than polling /readyz and /api/v1/meta in turn. It is
	// reported and does not make the probe fail: an instance with no accounts
	// is serving exactly what it should -- the first-run page -- and taking it
	// out of a load balancer would hide that page from the person who needs it.
	need, err := s.auth.NeedsBootstrap(ctx)
	if err != nil {
		s.logger(r).Warn("readiness probe could not count accounts", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"message": "the database is not answering; the cause is in the controller's log",
		})
		return
	}
	body["bootstrap_required"] = need
	// A fenced fleet is not ready, and saying so is the point: it is serving,
	// it is deciding, and it is doing none of it, which from outside looks
	// exactly like a healthy fleet with nothing queued. A load balancer taking
	// it out of rotation is the correct outcome, and so is a deployment that
	// refuses to go green until somebody has looked.
	//
	// Liveness is deliberately unaffected: the container's health check is
	// /healthz, so a fenced controller is not restarted by its own runtime --
	// which would achieve nothing and lose the operator's session.
	if f := s.ctrl.Fenced(); f.Fenced {
		body["ok"] = false
		body["fenced"] = true
		body["message"] = "this fleet is held for recovery and is applying nothing; the problems drawer says what to check before lifting the fence"
		if f.Reason != "" {
			body["reason"] = f.Reason
		}
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
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
