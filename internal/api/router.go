package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/github"
)

// maxBodyBytes bounds an ordinary JSON request. It is generous for a pool
// definition and far below anything that could be used to make the controller
// buffer memory on purpose. The webhook sets its own, larger, limit, and the
// agent's log relay is a stream and is exempt.
const maxBodyBytes = 1 << 20

// routes builds the whole surface, in the order documented in
// docs/api-surface.md: health, the spec, the webhook, the agent API, the user
// API, metrics, and then the UI, which catches everything left over.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	// The order of these matters. The request ID and the client address are
	// established first because everything downstream -- logging, the login
	// rate limiter, the audit trail -- records them; recovery wraps the
	// handlers so that a panic becomes a 500 rather than a dropped connection;
	// the access log sits outside the handlers so it can time them.
	r.Use(s.withRequestInfo)
	r.Use(s.recoverPanics)
	r.Use(s.accessLog)
	r.Use(s.securityHeaders)

	r.NotFound(s.notFoundHandler)
	r.MethodNotAllowed(methodNotAllowed)

	// Health. Unauthenticated on purpose: a load balancer cannot sign in, and
	// neither of these says anything a stranger could use.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	// The contract this server implements, served next to it so that a client
	// can always fetch the spec for the version it is actually talking to.
	r.Get("/api/openapi.yaml", s.handleOpenAPI)

	// What a crawler asks for before anything else. Both are rendered per
	// request rather than embedded, because both have to name this
	// controller's own address and only a request knows it. Mounted here so
	// they win over the SPA's file lookup, which would 404 a path with an
	// extension.
	r.Get("/robots.txt", s.handleRobots)
	r.Get("/sitemap.xml", s.handleSitemap)

	// The webhook. Mounted for every method rather than POST alone so that
	// GitHub's own "wrong method" case gets the controller's message, which
	// says what the endpoint is for, instead of a bare 405.
	r.Handle(s.cfg.GitHub.WebhookPath, http.HandlerFunc(s.ctrl.HandleWebhook))

	r.Mount("/api/v1/agent", s.agentRoutes())
	r.Mount("/api/v1", s.apiRoutes())

	if s.cfg.Metrics.Enabled {
		r.Handle(s.cfg.Metrics.Path, s.metricsHandler())
	}

	return r
}

// apiRoutes is the user-facing API: everything under /api/v1 that is not an
// agent route.
func (s *Server) apiRoutes() chi.Router {
	r := chi.NewRouter()
	r.NotFound(apiNotFound)
	r.MethodNotAllowed(methodNotAllowed)
	r.Use(limitBody)
	r.Use(s.csrf)

	// Unauthenticated: the three things a browser needs before it has a
	// session, and the two halves of the single sign-on handshake.
	r.Group(func(r chi.Router) {
		r.Use(s.optionalAuth)
		r.Get("/meta", s.handleMeta)
		r.Post("/auth/bootstrap", s.handleBootstrap)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/auth/oidc/start", s.handleOIDCStart)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)

		// Every authenticated identity may end its own session, see itself and
		// change its own password; there is no separate role for that.
		r.Post("/auth/logout", s.handleLogout)
		r.Get("/auth/session", s.handleSession)
		r.Post("/auth/password", s.handleChangePassword)

		// Overview.
		r.With(s.require(auth.ActionStatsRead)).Get("/stats", s.handleStats)
		r.With(s.require(auth.ActionStatsRead)).Get("/samples", s.handleSamples)
		r.With(s.require(auth.ActionStatsRead)).Get("/problems", s.handleProblems)
		r.With(s.require(auth.ActionStatsRead)).Get("/scaling-events", s.handleScalingEvents)
		r.With(s.require(auth.ActionEventsRead)).Get("/events", s.handleEvents)

		// Installations.
		r.Route("/installations", func(r chi.Router) {
			r.With(s.require(auth.ActionInstallationsRead)).Get("/", s.handleListInstallations)
			r.With(s.require(auth.ActionInstallationsWrite)).Post("/", s.handleCreateInstallation)
			r.With(s.require(auth.ActionInstallationsWrite)).Post("/manifest", s.handleCreateManifest)
			r.With(s.require(auth.ActionInstallationsWrite)).Post("/manifest/exchange", s.handleExchangeManifest)
			r.With(s.require(auth.ActionInstallationsRead)).Get("/{id}", s.handleGetInstallation)
			r.With(s.require(auth.ActionInstallationsWrite)).Patch("/{id}", s.handleUpdateInstallation)
			r.With(s.require(auth.ActionInstallationsDelete)).Delete("/{id}", s.handleDeleteInstallation)
			r.With(s.require(auth.ActionInstallationsVerify)).Post("/{id}/verify", s.handleVerifyInstallation)
			r.With(s.require(auth.ActionInstallationsRead)).Get("/{id}/runner-groups", s.handleRunnerGroups)
			r.With(s.require(auth.ActionInstallationsRead)).Get("/{id}/rate-limit", s.handleRateLimit)
		})
		r.With(s.require(auth.ActionWebhooksRead)).Get("/webhook-deliveries", s.handleWebhookDeliveries)
		r.With(s.require(auth.ActionWebhooksTest)).Post("/webhook-test", s.handleWebhookTest)

		// Pools.
		r.Route("/pools", func(r chi.Router) {
			r.With(s.require(auth.ActionPoolsRead)).Get("/", s.handleListPools)
			r.With(s.require(auth.ActionPoolsWrite)).Post("/", s.handleCreatePool)
			r.With(s.require(auth.ActionPoolsWrite)).Post("/validate", s.handleValidatePool)
			r.With(s.require(auth.ActionPoolsRead)).Get("/{id}", s.handleGetPool)
			r.With(s.require(auth.ActionPoolsWrite)).Patch("/{id}", s.handleUpdatePool)
			r.With(s.require(auth.ActionPoolsDelete)).Delete("/{id}", s.handleDeletePool)
			r.With(s.require(auth.ActionPoolsWrite)).Post("/{id}/enable", s.handleEnablePool)
			r.With(s.require(auth.ActionPoolsWrite)).Post("/{id}/disable", s.handleDisablePool)
			r.With(s.require(auth.ActionPoolsWrite)).Post("/{id}/prewarm", s.handlePrewarmPool)
		})

		// Runners.
		r.Route("/runners", func(r chi.Router) {
			r.With(s.require(auth.ActionRunnersRead)).Get("/", s.handleListRunners)
			// The bulk endpoint accepts both drain and delete; the route gate
			// is the weaker of the two and the handler checks the stronger one
			// against the body, so a token scoped to drains cannot delete.
			r.With(s.require(auth.ActionRunnersDrain)).Post("/bulk", s.handleBulkRunners)
			r.With(s.require(auth.ActionRunnersRead)).Get("/{id}", s.handleGetRunner)
			r.With(s.require(auth.ActionRunnersDelete)).Delete("/{id}", s.handleDeleteRunner)
			r.With(s.require(auth.ActionRunnersDrain)).Post("/{id}/drain", s.handleDrainRunner)
			r.With(s.require(auth.ActionRunnersRead)).Get("/{id}/timeline", s.handleRunnerTimeline)
			r.With(s.require(auth.ActionLogsRead)).Get("/{id}/logs", s.handleRunnerLogs)
			r.With(s.require(auth.ActionLogsRead)).Get("/{id}/logs/download", s.handleDownloadRunnerLogs)
		})

		// Jobs.
		r.With(s.require(auth.ActionUsageRead)).Get("/usage", s.handleUsage)
		r.With(s.require(auth.ActionUsageRead)).Get("/usage.csv", s.handleUsageCSV)
		r.Route("/jobs", func(r chi.Router) {
			r.Use(s.require(auth.ActionJobsRead))
			r.Get("/", s.handleListJobs)
			r.Get("/facets", s.handleJobFacets)
			r.Get("/{id}", s.handleGetJob)
			r.Get("/{id}/events", s.handleJobEvents)
		})

		// Hosts and enrolment.
		r.Route("/hosts", func(r chi.Router) {
			r.With(s.require(auth.ActionHostsRead)).Get("/", s.handleListHosts)
			r.With(s.require(auth.ActionHostsRead)).Get("/{id}", s.handleGetHost)
			r.With(s.require(auth.ActionHostsWrite)).Patch("/{id}", s.handleUpdateHost)
			r.With(s.require(auth.ActionHostsCordon)).Post("/{id}/cordon", s.handleCordonHost)
			r.With(s.require(auth.ActionHostsDelete)).Delete("/{id}", s.handleDeleteHost)
		})
		r.Route("/join-tokens", func(r chi.Router) {
			r.With(s.require(auth.ActionJoinsRead)).Get("/", s.handleListJoinTokens)
			r.With(s.require(auth.ActionJoinsRead)).Get("/{id}", s.handleGetJoinToken)
			r.With(s.require(auth.ActionJoinsWrite)).Post("/", s.handleCreateJoinToken)
			r.With(s.require(auth.ActionJoinsWrite)).Delete("/{id}", s.handleDeleteJoinToken)
		})

		// Migrations: what moving a repository's workflows onto this fleet
		// would change, and then doing it. The plan writes nothing.
		r.Route("/migrations", func(r chi.Router) {
			r.With(s.require(auth.ActionMigrationsRead)).Post("/plan", s.handleMigrationPlan)
			r.With(s.require(auth.ActionMigrationsWrite)).Post("/pull-requests", s.handleMigrationApply)
		})

		// Audit.
		r.Route("/audit", func(r chi.Router) {
			r.Use(s.require(auth.ActionAuditRead))
			r.Get("/", s.handleListAudit)
			r.Get("/actions", s.handleAuditActions)
		})

		// Users, tokens and settings.
		r.Route("/users", func(r chi.Router) {
			r.With(s.require(auth.ActionUsersRead)).Get("/", s.handleListUsers)
			r.With(s.require(auth.ActionUsersWrite)).Post("/", s.handleCreateUser)
			r.With(s.require(auth.ActionUsersRead)).Get("/{id}", s.handleGetUser)
			r.With(s.require(auth.ActionUsersWrite)).Patch("/{id}", s.handleUpdateUser)
			r.With(s.require(auth.ActionUsersWrite)).Delete("/{id}", s.handleDeleteUser)
			r.With(s.require(auth.ActionUsersWrite)).Post("/{id}/password", s.handleResetPassword)
		})
		r.Route("/tokens", func(r chi.Router) {
			r.With(s.require(auth.ActionTokensRead)).Get("/", s.handleListTokens)
			r.With(s.require(auth.ActionTokensWrite)).Post("/", s.handleCreateToken)
			r.With(s.require(auth.ActionTokensWrite)).Delete("/{id}", s.handleRevokeToken)
		})
		r.With(s.require(auth.ActionSettingsRead)).Get("/settings", s.handleGetSettings)
		r.With(s.require(auth.ActionSettingsWrite)).Patch("/settings", s.handleUpdateSettings)
	})

	return r
}

// agentRoutes is the half of the API agents talk to. It is mounted separately
// because it uses a different credential class entirely: an agent token is not
// a user session and cannot reach anything else.
func (s *Server) agentRoutes() chi.Router {
	r := chi.NewRouter()
	r.NotFound(apiNotFound)
	r.MethodNotAllowed(methodNotAllowed)

	// Join is the one anonymous agent route: it is the call that mints the
	// credential every other one carries.
	r.With(limitBody).Post(strings.TrimPrefix(agent.PathJoin, "/api/v1/agent"), s.handleAgentJoin)

	r.Group(func(r chi.Router) {
		r.Use(s.agentAuth)
		r.With(limitBody).Post(strings.TrimPrefix(agent.PathHeartbeat, "/api/v1/agent"), s.handleAgentHeartbeat)
		r.Get(strings.TrimPrefix(agent.PathTasks, "/api/v1/agent"), s.handleAgentTasks)
		r.With(limitBody).Post(strings.TrimPrefix(agent.PathResults, "/api/v1/agent"), s.handleAgentResult)
		r.With(limitBody).Post(strings.TrimPrefix(agent.PathReport, "/api/v1/agent"), s.handleAgentReport)
		// No body limit: this one is a runner's whole output, streamed for as
		// long as the job runs.
		r.Post(strings.TrimPrefix(agent.PathLogs, "/api/v1/agent")+"/{stream_id}", s.handleAgentLogs)
	})
	return r
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// withRequestInfo establishes the request ID, the client address and the
// request-scoped logger.
func (s *Server) withRequestInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 || !printableASCII(id) {
			id = newRequestID()
		}
		info := &requestInfo{id: id, ip: s.resolveClientIP(r)}
		info.log = s.log.With("request_id", id, "ip", info.ip)

		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxRequestInfo, info)
		ctx = withRequestLogger(ctx, info.log)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanics turns a panic in a handler into a 500.
//
// The stack goes to the log, never to the client: a stack trace names internal
// paths and package versions, and the operator who needs it can read it where
// it is safe to print.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is net/http's own "stop, quietly" signal;
			// swallowing it would hide a deliberate abort.
			if err, ok := rec.(error); ok && err == http.ErrAbortHandler {
				panic(rec)
			}
			s.logger(r).Error("a request handler panicked",
				"panic", fmt.Sprint(rec), "method", r.Method, "path", r.URL.Path,
				"stack", string(debug.Stack()))
			writeError(w, http.StatusInternalServerError, errorEnvelope{Error: errorBody{
				Code:    codeInternal,
				Message: "the controller hit an unexpected error handling this request. The details are in its log; quote the request ID.",
				Detail:  "request " + RequestID(r.Context()),
			}})
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLog records one line per request at debug.
//
// Debug rather than info because a live UI makes a request per interaction and
// an SSE reconnect per network blip; at info this would drown everything else
// an operator is trying to read.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		info := infoFrom(r.Context())
		who := "anonymous"
		if info != nil && info.identity != nil {
			who = info.identity.String()
		}
		s.logger(r).Debug("request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"bytes", rec.written, "duration", time.Since(started).Round(time.Millisecond),
			"identity", who)
	})
}

// securityHeaders sets the response headers that constrain what a browser will
// do with what we send it.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			// A year, without includeSubDomains: Zoomies has no business
			// making promises about the other hosts on its parent domain.
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy is the policy the embedded UI needs and nothing more.
//
// The index page carries one inline script -- the theme bootstrap that runs
// before the first paint, so a dark-mode dashboard does not flash white -- and
// it is allowed by its hash rather than by 'unsafe-inline', so any other inline
// script (an injected one, for instance) is still refused. Styles need
// 'unsafe-inline' because the component framework sets element styles directly.
//
// formTargets are the origins a form on the page may post to besides this
// one. The App manifest flow is a real HTML form that posts to GitHub -- that
// is how GitHub's API works, there is no JSON equivalent -- and a form-action
// of 'self' alone makes the browser refuse the submission. It does so silently
// from the operator's point of view: the new tab opens on nothing, a reload
// turns the POST into a GET, and GitHub answers that with an empty "create an
// App" form that looks as if Zoomies had sent one with no fields in it.
func contentSecurityPolicy(scriptHashes, formTargets []string) string {
	script := "'self'"
	for _, h := range scriptHashes {
		script += " '" + h + "'"
	}
	form := "'self'"
	for _, t := range formTargets {
		form += " " + t
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + script,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action " + form,
		"frame-ancestors 'none'",
	}, "; ")
}

// manifestFormTargets lists the GitHub origins the App manifest form may be
// posted to: github.com always, and the Enterprise Server this controller is
// configured against when there is one.
//
// It is a startup-time list because the policy is a response header on every
// page, not something the manifest endpoint can adjust per request. An
// Enterprise host named only in the connect dialog, and not in the
// configuration, is therefore refused by handleCreateManifest with a message
// saying which setting to change -- the alternative is a form the browser
// blocks without telling anyone why.
func manifestFormTargets(apiBaseURL string) []string {
	targets := []string{"https://github.com"}
	if normalised, err := github.NormalizeAPIBaseURL(apiBaseURL); err == nil {
		apiBaseURL = normalised
	}
	if origin := formOrigin(github.WebURLForAPI(apiBaseURL)); origin != "" && origin != targets[0] {
		targets = append(targets, origin)
	}
	return targets
}

// formOrigin reduces a URL to the scheme and host a CSP source expression
// wants; anything unparseable yields "" rather than a directive that is
// itself invalid.
func formOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// formAllowed reports whether the page's policy lets a form post to raw.
func (s *Server) formAllowed(raw string) bool {
	origin := formOrigin(raw)
	if origin == "" {
		return false
	}
	if self := formOrigin(s.cfg.Server.ExternalURL); self != "" && strings.EqualFold(self, origin) {
		return true
	}
	for _, t := range s.formTargets {
		if strings.EqualFold(t, origin) {
			return true
		}
	}
	return false
}

// limitBody caps how much of a request body a handler can be made to read.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Fallbacks
// ---------------------------------------------------------------------------

// notFoundHandler is the root fallback: an unknown /api path is a real 404,
// and anything else is a route in the single-page app.
func (s *Server) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		apiNotFound(w, r)
		return
	}
	s.spa.ServeHTTP(w, r)
}

// apiNotFound answers an unknown API path in the shape every other API error
// takes, so a client never has to parse an HTML page to find out what happened.
func apiNotFound(w http.ResponseWriter, r *http.Request) {
	notFound(w, "there is no "+r.Method+" "+r.URL.Path+" in this API; see /api/openapi.yaml for what there is")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, errorEnvelope{Error: errorBody{
		Code:    codeBadRequest,
		Message: r.Method + " is not accepted on " + r.URL.Path + "; see /api/openapi.yaml for the methods that are",
	}})
}

// isAPIPath reports whether a path belongs to the machine surface rather than
// to the UI. The UI must never be served in place of an API 404: a fetch that
// gets HTML back reports a JSON parse error, which sends whoever is debugging
// it in exactly the wrong direction.
func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

// statusRecorder remembers what a handler answered, for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
	wrote   bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	w.wrote = true
	n, err := w.ResponseWriter.Write(b)
	w.written += n
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer, which is what the
// SSE handlers use to flush and to clear their deadlines.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req"
	}
	return hex.EncodeToString(b[:])
}

func printableASCII(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
