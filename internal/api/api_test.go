package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// testKey is a fixed instance key. The API loads its own copy from the
// configuration, so the test has to hand the same one to the controller or the
// two would seal and unseal with different keys -- which is exactly the bug the
// shared-configuration arrangement exists to prevent.
const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// fakeFactory hands out GitHub clients backed by github.NewFake.
type fakeFactory struct {
	gh  *github.FakeGitHub
	err error
}

func (f *fakeFactory) For(_ context.Context, inst *store.Installation, pem []byte) (github.Client, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(pem) == 0 {
		return nil, errors.New("fake factory: no private key")
	}
	return f.gh.Client(inst.Target, inst.TargetType), nil
}

// harness is a real controller, a real store and the API in front of them,
// served from an httptest listener so that every test exercises the same
// middleware chain a browser would.
type harness struct {
	t    *testing.T
	srv  *httptest.Server
	api  *Server
	ctrl *controller.Controller
	st   *store.Store
	gh   *github.FakeGitHub
	cfg  *config.Config
	key  *cryptox.Key
	ctx  context.Context
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Server.Bind = "127.0.0.1:0"
	cfg.Server.ExternalURL = "http://zoomies.test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "zoomies.db")
	cfg.Security.EncryptionKey = testKey
	cfg.Security.EncryptionKeyFile = ""
	cfg.Agent.Embedded = false
	cfg.Agent.Backend = "process"
	cfg.Agent.WorkDir = filepath.Join(t.TempDir(), "work")
	cfg.Metrics.Enabled = true
	return cfg
}

func newHarness(t *testing.T, opts ...func(*config.Config)) *harness {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gh := github.NewFake()
	t.Cleanup(gh.Close)

	cfg := testConfig(t)
	for _, o := range opts {
		o(cfg)
	}
	key, err := cryptox.ParseKey(cfg.Security.EncryptionKey)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}

	bus := events.New()
	logger := slog.New(slog.DiscardHandler)
	ctrl, err := controller.New(controller.Options{
		Store:  st,
		Config: cfg,
		Key:    key,
		Auth:   auth.New(st, cfg, bus, auth.WithLogger(logger)),
		Events: bus,
		GitHub: &fakeFactory{gh: gh},
		Logger: logger,
		Clock:  time.Now,
	})
	if err != nil {
		t.Fatalf("controller.New: %v", err)
	}

	s, err := New(Options{Controller: ctrl, Logger: logger})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	return &harness{t: t, srv: srv, api: s, ctrl: ctrl, st: st, gh: gh, cfg: cfg, key: key, ctx: ctx}
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

// request is one call to the test server, described declaratively so a table
// test can build forty of them without forty helper functions.
type request struct {
	method string
	path   string
	body   any
	// token is sent as a bearer credential.
	token string
	// cookie is sent as the session cookie.
	cookie string
	// origin overrides the same-origin header an unsafe request needs; the
	// zero value uses the test server's own origin, which is what a browser on
	// this page would send.
	origin string
	// noOrigin suppresses that header entirely, for the CSRF tests.
	noOrigin bool
	// rawBody sends a body that is not JSON, which is what the agent's log
	// relay is.
	rawBody string
	headers map[string]string
}

type response struct {
	status int
	body   []byte
	header http.Header
	cookie *http.Cookie
}

func (h *harness) do(req request) *response {
	h.t.Helper()

	var body io.Reader
	switch {
	case req.rawBody != "":
		body = strings.NewReader(req.rawBody)
	case req.body != nil:
		raw, err := json.Marshal(req.body)
		if err != nil {
			h.t.Fatalf("encoding request body: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(req.method, h.srv.URL+req.path, body)
	if err != nil {
		h.t.Fatalf("building request: %v", err)
	}
	if req.body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.token != "" {
		r.Header.Set("Authorization", "Bearer "+req.token)
	}
	if req.cookie != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: req.cookie})
	}
	switch {
	case req.noOrigin:
	case req.origin != "":
		r.Header.Set("Origin", req.origin)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	default:
		r.Header.Set("Origin", h.srv.URL)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Redirects are part of what is being asserted (the OIDC and login
			// flows), so they are reported rather than followed.
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(r)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.method, req.path, err)
	}
	defer resp.Body.Close()
	// A stream answers with its headers and then stays open, so reading its
	// body to EOF would block until the client's own timeout. The SSE tests
	// read those bodies deliberately, with their own reader.
	var raw []byte
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}

	out := &response{status: resp.StatusCode, body: raw, header: resp.Header}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			out.cookie = c
		}
	}
	return out
}

// json decodes a response body, failing the test when it is not JSON.
func (r *response) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("response is not a JSON object (status %d): %v\nbody: %s", r.status, err, truncate(r.body))
	}
	return out
}

func (r *response) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decoding response (status %d): %v\nbody: %s", r.status, err, truncate(r.body))
	}
}

// errorCode returns the code of an error envelope, or "" when the body is not
// one.
func (r *response) errorCode(t *testing.T) string {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(r.body, &env); err != nil {
		return ""
	}
	return env.Error.Code
}

func (r *response) errorMessage(t *testing.T) string {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(r.body, &env); err != nil {
		return ""
	}
	return env.Error.Message
}

func truncate(b []byte) string {
	if len(b) > 512 {
		return string(b[:512]) + "..."
	}
	return string(b)
}

func (r *response) mustStatus(t *testing.T, want int, what string) {
	t.Helper()
	if r.status != want {
		t.Fatalf("%s: status %d, want %d\nbody: %s", what, r.status, want, truncate(r.body))
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// testPassword is what every account in these tests signs in with.
const testPassword = "correct-horse-battery"

// testPasswordHash is argon2id over testPassword, computed once for the whole
// test binary.
//
// Hashing a password is deliberately expensive -- that is the point of it --
// and these tests create dozens of accounts that never verify one. Reusing a
// single hash keeps the suite from spending most of its time proving that
// argon2 is slow.
var testPasswordHash = sync.OnceValue(func() string {
	hash, err := cryptox.HashPassword(testPassword)
	if err != nil {
		panic("hashing the test password: " + err.Error())
	}
	return hash
})

// user creates an account and returns a session cookie for it.
func (h *harness) user(username string, role store.Role) (*store.User, string) {
	h.t.Helper()
	u := &store.User{Username: username, Role: role, PasswordHash: testPasswordHash()}
	if err := h.st.CreateUser(h.ctx, u); err != nil {
		h.t.Fatalf("creating %s: %v", username, err)
	}
	token, err := h.ctrl.Auth().NewSession(h.ctx, u, "127.0.0.1", "test")
	if err != nil {
		h.t.Fatalf("session for %s: %v", username, err)
	}
	return u, token
}

// session mints another browser session for an existing account.
func (h *harness) session(u *store.User) string {
	h.t.Helper()
	token, err := h.ctrl.Auth().NewSession(h.ctx, u, "127.0.0.1", "test")
	if err != nil {
		h.t.Fatalf("session for %s: %v", u.Username, err)
	}
	return token
}

// token mints an API token with a role.
func (h *harness) token(name string, role store.Role, scopes ...string) string {
	h.t.Helper()
	_, plaintext, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{
		Name: name, Role: role, Scopes: scopes,
	})
	if err != nil {
		h.t.Fatalf("creating token %s: %v", name, err)
	}
	return plaintext
}

// installation seeds a GitHub App installation whose secrets are sealed with
// the same key the API loaded.
func (h *harness) installation() *store.Installation {
	h.t.Helper()
	pem, err := h.key.SealString("-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----")
	if err != nil {
		h.t.Fatalf("sealing key: %v", err)
	}
	secret, err := h.key.SealString("webhook-secret")
	if err != nil {
		h.t.Fatalf("sealing secret: %v", err)
	}
	inst := &store.Installation{
		AppID:            h.gh.AppID(),
		InstallationID:   h.gh.InstallationID(),
		Target:           "acme",
		TargetType:       store.TargetOrg,
		APIBaseURL:       h.gh.URL(),
		PrivateKeyEnc:    pem,
		WebhookSecretEnc: secret,
	}
	if err := h.st.CreateInstallation(h.ctx, inst); err != nil {
		h.t.Fatalf("CreateInstallation: %v", err)
	}
	return inst
}

func (h *harness) pool(inst *store.Installation, name string) *store.Pool {
	h.t.Helper()
	p := &store.Pool{
		Name:           name,
		InstallationID: inst.ID,
		Labels:         store.StringSlice{"self-hosted", "linux", "x64", name},
		Backend:        store.BackendDocker,
		Image:          "ghcr.io/eyupio/zoomies-runner:test",
		MaxRunners:     4,
		IdleTimeout:    store.Duration(5 * time.Minute),
		Ephemeral:      true,
		DockerMode:     store.DockerNone,
		Enabled:        true,
	}
	if err := h.st.CreatePool(h.ctx, p); err != nil {
		h.t.Fatalf("CreatePool: %v", err)
	}
	return p
}

func (h *harness) host(name string) *store.Host {
	h.t.Helper()
	host := &store.Host{
		Name:          name,
		Capacity:      4,
		Backends:      store.StringSlice{"docker"},
		Labels:        store.StringMap{},
		OS:            "linux",
		Arch:          "amd64",
		LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		h.t.Fatalf("CreateHost: %v", err)
	}
	return host
}

func (h *harness) runner(pool *store.Pool, host *store.Host, state store.RunnerState) *store.Runner {
	h.t.Helper()
	r := &store.Runner{
		PoolID:    pool.ID,
		HostID:    host.ID,
		Name:      "zoomies-" + pool.Name + "-" + store.NewSecret(4),
		State:     store.RunnerProvisioning,
		Ephemeral: true,
		Labels:    pool.Labels,
	}
	if err := h.st.CreateRunner(h.ctx, r); err != nil {
		h.t.Fatalf("CreateRunner: %v", err)
	}
	for _, to := range pathTo(state) {
		out, err := h.st.TransitionRunner(h.ctx, r.ID, to, "")
		if err != nil {
			h.t.Fatalf("TransitionRunner(%s): %v", to, err)
		}
		r = out
	}
	return r
}

// pathTo returns the transitions that reach a state from provisioning, since
// the store refuses an illegal jump.
func pathTo(state store.RunnerState) []store.RunnerState {
	switch state {
	case store.RunnerProvisioning:
		return nil
	case store.RunnerRegistering:
		return []store.RunnerState{store.RunnerRegistering}
	case store.RunnerIdle:
		return []store.RunnerState{store.RunnerRegistering, store.RunnerIdle}
	case store.RunnerBusy:
		return []store.RunnerState{store.RunnerRegistering, store.RunnerIdle, store.RunnerBusy}
	case store.RunnerDraining:
		return []store.RunnerState{store.RunnerRegistering, store.RunnerIdle, store.RunnerDraining}
	case store.RunnerRemoved:
		return []store.RunnerState{store.RunnerRegistering, store.RunnerIdle, store.RunnerDraining, store.RunnerRemoved}
	case store.RunnerFailed:
		return []store.RunnerState{store.RunnerFailed}
	default:
		return nil
	}
}

func (h *harness) job(pool *store.Pool, state store.JobState) *store.Job {
	h.t.Helper()
	j := &store.Job{
		GitHubJobID: time.Now().UnixNano(),
		GitHubRunID: 1,
		Repo:        "acme/widgets",
		Workflow:    "ci",
		JobName:     "build",
		Labels:      store.StringSlice{"self-hosted", "linux", "x64"},
		State:       state,
		PoolID:      pool.ID,
		Matched:     true,
		QueuedAt:    time.Now().Add(-time.Minute),
	}
	out, err := h.st.UpsertJob(h.ctx, j)
	if err != nil {
		h.t.Fatalf("UpsertJob: %v", err)
	}
	return out
}

// agentToken enrols a host the way an agent would and returns its credential.
func (h *harness) agentToken(name string) (string, string) {
	h.t.Helper()
	_, plaintext, err := h.ctrl.Auth().CreateJoinToken(h.ctx, time.Hour, nil, 2, "test")
	if err != nil {
		h.t.Fatalf("CreateJoinToken: %v", err)
	}
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/agent/join", body: map[string]any{
		"protocol_version": 1,
		"join_token":       plaintext,
		"name":             name,
		"capacity":         2,
		"os":               "linux",
		"arch":             "amd64",
		"version":          "test",
		"backends":         []map[string]any{{"kind": "docker", "available": true}},
	}})
	resp.mustStatus(h.t, http.StatusOK, "agent join")
	var out struct {
		HostID     string `json:"host_id"`
		AgentToken string `json:"agent_token"`
	}
	resp.into(h.t, &out)
	return out.HostID, out.AgentToken
}

// ---------------------------------------------------------------------------
// The route table
// ---------------------------------------------------------------------------

// route describes one endpoint's authorisation, which is the property this
// package most needs a test for: a new endpoint added without a role check
// would otherwise ship silently.
type route struct {
	method string
	path   string
	// role is the minimum role that may call it. Empty means the route is
	// unauthenticated.
	role store.Role
	body any
	// public marks a route that is reachable without any credential at all.
	public bool
	// checksCredentials marks a public route that answers 401 about the
	// credentials in its body rather than about the caller -- which is exactly
	// what a failed sign-in is.
	checksCredentials bool
}

// routeTable is every route in api/openapi.yaml, plus health and the spec.
//
// It is written out by hand rather than derived from the router on purpose: a
// table generated from the thing it checks would agree with a mistake.
func routeTable(ids fixtureIDs) []route {
	return []route{
		{method: "GET", path: "/healthz", public: true},
		{method: "GET", path: "/readyz", public: true},
		{method: "GET", path: "/api/openapi.yaml", public: true},
		{method: "GET", path: "/api/v1/meta", public: true},

		{method: "POST", path: "/api/v1/auth/login", public: true, checksCredentials: true,
			body: map[string]any{"username": "nobody", "password": "x"}},
		{method: "POST", path: "/api/v1/auth/bootstrap", public: true, body: map[string]any{"username": "nobody", "password": testPassword}},
		{method: "GET", path: "/api/v1/auth/oidc/start", public: true},
		{method: "GET", path: "/api/v1/auth/oidc/callback", public: true},

		{method: "GET", path: "/api/v1/auth/session", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/auth/logout", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/auth/password", role: store.RoleViewer,
			body: map[string]any{"old_password": "x", "new_password": testPassword}},

		{method: "GET", path: "/api/v1/stats", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/samples", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/problems", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/scaling-events", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/events", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/usage", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/usage.csv", role: store.RoleViewer},

		{method: "GET", path: "/api/v1/installations", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/installations", role: store.RoleAdmin, body: map[string]any{}},
		{method: "GET", path: "/api/v1/installations/" + ids.installation, role: store.RoleViewer},
		{method: "PATCH", path: "/api/v1/installations/" + ids.installation, role: store.RoleAdmin, body: map[string]any{}},
		{method: "DELETE", path: "/api/v1/installations/missing", role: store.RoleAdmin},
		{method: "POST", path: "/api/v1/installations/" + ids.installation + "/verify", role: store.RoleOperator},
		{method: "GET", path: "/api/v1/installations/" + ids.installation + "/runner-groups", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/installations/" + ids.installation + "/rate-limit", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/installations/manifest", role: store.RoleAdmin,
			body: map[string]any{"target": "acme", "target_type": "org"}},
		{method: "POST", path: "/api/v1/installations/manifest/exchange", role: store.RoleAdmin,
			body: map[string]any{"code": ""}},
		{method: "GET", path: "/api/v1/webhook-deliveries", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/webhook-test", role: store.RoleOperator},

		{method: "GET", path: "/api/v1/pools", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/pools", role: store.RoleOperator, body: map[string]any{}},
		{method: "POST", path: "/api/v1/pools/validate", role: store.RoleOperator, body: map[string]any{}},
		{method: "GET", path: "/api/v1/pools/" + ids.pool, role: store.RoleViewer},
		{method: "PATCH", path: "/api/v1/pools/" + ids.pool, role: store.RoleOperator, body: map[string]any{}},
		{method: "DELETE", path: "/api/v1/pools/missing", role: store.RoleOperator},
		{method: "POST", path: "/api/v1/pools/" + ids.pool + "/enable", role: store.RoleOperator},
		{method: "POST", path: "/api/v1/pools/" + ids.pool + "/disable", role: store.RoleOperator},
		{method: "POST", path: "/api/v1/pools/" + ids.pool + "/prewarm", role: store.RoleOperator},

		{method: "GET", path: "/api/v1/runners", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/runners/" + ids.runner, role: store.RoleViewer},
		{method: "DELETE", path: "/api/v1/runners/missing", role: store.RoleOperator},
		{method: "POST", path: "/api/v1/runners/missing/drain", role: store.RoleOperator},
		{method: "GET", path: "/api/v1/runners/" + ids.runner + "/timeline", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/runners/bulk", role: store.RoleOperator,
			body: map[string]any{"action": "drain", "ids": []string{"missing"}}},
		{method: "GET", path: "/api/v1/runners/missing/logs", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/runners/missing/logs/download", role: store.RoleViewer},

		{method: "GET", path: "/api/v1/jobs", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/jobs/facets", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/jobs/" + ids.job, role: store.RoleViewer},
		{method: "GET", path: "/api/v1/jobs/" + ids.job + "/events", role: store.RoleViewer},

		{method: "GET", path: "/api/v1/hosts", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/hosts/" + ids.host, role: store.RoleViewer},
		{method: "PATCH", path: "/api/v1/hosts/" + ids.host, role: store.RoleOperator, body: map[string]any{}},
		{method: "POST", path: "/api/v1/hosts/" + ids.host + "/cordon", role: store.RoleOperator,
			body: map[string]any{"cordoned": false}},
		{method: "DELETE", path: "/api/v1/hosts/missing", role: store.RoleAdmin},

		{method: "GET", path: "/api/v1/join-tokens", role: store.RoleAdmin},
		{method: "GET", path: "/api/v1/join-tokens/missing", role: store.RoleAdmin},
		{method: "POST", path: "/api/v1/join-tokens", role: store.RoleAdmin, body: map[string]any{"ttl": "15m"}},
		{method: "DELETE", path: "/api/v1/join-tokens/missing", role: store.RoleAdmin},

		{method: "POST", path: "/api/v1/migrations/plan", role: store.RoleOperator,
			body: map[string]any{"installation_id": ids.installation}},
		{method: "POST", path: "/api/v1/migrations/pull-requests", role: store.RoleOperator,
			body: map[string]any{"installation_id": ids.installation, "repos": []string{}, "mapping": map[string]string{}}},

		{method: "GET", path: "/api/v1/audit", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/audit/actions", role: store.RoleViewer},

		{method: "GET", path: "/api/v1/users", role: store.RoleAdmin},
		{method: "POST", path: "/api/v1/users", role: store.RoleAdmin, body: map[string]any{"username": "", "role": "viewer"}},
		{method: "GET", path: "/api/v1/users/missing", role: store.RoleAdmin},
		{method: "PATCH", path: "/api/v1/users/missing", role: store.RoleAdmin, body: map[string]any{}},
		{method: "DELETE", path: "/api/v1/users/missing", role: store.RoleAdmin},
		{method: "POST", path: "/api/v1/users/missing/password", role: store.RoleAdmin,
			body: map[string]any{"new_password": testPassword}},

		{method: "GET", path: "/api/v1/tokens", role: store.RoleAdmin},
		{method: "POST", path: "/api/v1/tokens", role: store.RoleAdmin, body: map[string]any{"name": "", "role": "viewer"}},
		{method: "DELETE", path: "/api/v1/tokens/missing", role: store.RoleAdmin},

		{method: "GET", path: "/api/v1/settings", role: store.RoleAdmin},
		{method: "PATCH", path: "/api/v1/settings", role: store.RoleAdmin, body: map[string]any{}},

		{method: "GET", path: "/metrics", role: store.RoleViewer},
	}
}

type fixtureIDs struct {
	installation string
	pool         string
	runner       string
	host         string
	job          string
}

func (h *harness) fixtures() fixtureIDs {
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	run := h.runner(pool, host, store.RunnerIdle)
	job := h.job(pool, store.JobQueued)
	return fixtureIDs{
		installation: inst.ID, pool: pool.ID, runner: run.ID, host: host.ID, job: job.ID,
	}
}

// TestRouteAuthorisation walks every route with four callers.
//
// This is the test that catches an endpoint added without a role: an
// unauthenticated request must be refused, a viewer must not be able to write,
// and an operator and an admin must get through to the handler.
func TestRouteAuthorisation(t *testing.T) {
	h := newHarness(t)
	ids := h.fixtures()

	viewer, _ := h.user("viewer", store.RoleViewer)
	operator, _ := h.user("operator", store.RoleOperator)
	admin, _ := h.user("admin", store.RoleAdmin)

	callers := []struct {
		name string
		role store.Role
		user *store.User
	}{
		{name: "anonymous"},
		{name: "viewer", role: store.RoleViewer, user: viewer},
		{name: "operator", role: store.RoleOperator, user: operator},
		{name: "admin", role: store.RoleAdmin, user: admin},
	}

	for _, rt := range routeTable(ids) {
		for _, caller := range callers {
			t.Run(rt.method+" "+rt.path+"/"+caller.name, func(t *testing.T) {
				// A session per request: POST /auth/logout ends the one it is
				// given, and every later route would otherwise be testing a
				// cookie this test itself invalidated.
				cookie := ""
				if caller.user != nil {
					cookie = h.session(caller.user)
				}
				resp := h.do(request{
					method: rt.method, path: rt.path, body: rt.body, cookie: cookie,
				})
				switch {
				case rt.public:
					if resp.status == http.StatusForbidden {
						t.Fatalf("public route answered 403: %s", truncate(resp.body))
					}
					if resp.status == http.StatusUnauthorized && !rt.checksCredentials {
						t.Fatalf("public route answered 401: %s", truncate(resp.body))
					}
				case cookie == "":
					if resp.status != http.StatusUnauthorized {
						t.Fatalf("unauthenticated request answered %d, want 401: %s", resp.status, truncate(resp.body))
					}
				case !caller.role.AtLeast(rt.role):
					if resp.status != http.StatusForbidden {
						t.Fatalf("%s answered %d, want 403: %s", caller.name, resp.status, truncate(resp.body))
					}
					if msg := resp.errorMessage(t); !strings.Contains(msg, string(rt.role)) {
						t.Errorf("403 message does not name the %s role: %q", rt.role, msg)
					}
				default:
					if resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden {
						t.Fatalf("%s was refused with %d: %s", caller.name, resp.status, truncate(resp.body))
					}
				}
			})
		}
	}
}

// TestRouteTableCoversTheSpec checks the hand-written table against the
// OpenAPI document, so a path added to the contract without a test here is
// reported rather than quietly untested.
func TestRouteTableCoversTheSpec(t *testing.T) {
	placeholders := fixtureIDs{
		installation: "ins_x", pool: "pool_x", runner: "run_x", host: "host_x", job: "job_x",
	}
	tested := map[string]bool{}
	for _, rt := range routeTable(placeholders) {
		tested[rt.method+" "+normalisePath(rt.path)] = true
	}

	spec, err := openapiSpec()
	if err != nil {
		t.Fatalf("openapiSpec: %v", err)
	}
	for _, op := range specOperations(t, spec) {
		key := op.method + " " + op.path
		if !tested[key] {
			t.Errorf("%s is in api/openapi.yaml but not in the route table", key)
		}
	}
}

// normalisePath turns a concrete test path back into the template the OpenAPI
// document uses, so the two can be compared.
func normalisePath(p string) string {
	p = strings.TrimPrefix(p, "/api/v1")
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if i == 0 || part == "" {
			continue
		}
		if strings.HasPrefix(part, "ins_") || strings.HasPrefix(part, "pool_") ||
			strings.HasPrefix(part, "run_") || strings.HasPrefix(part, "host_") ||
			strings.HasPrefix(part, "job_") || part == "missing" {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// specOperation is one method-and-path pair from the OpenAPI document.
type specOperation struct {
	method string
	path   string
}

// specOperations reads the paths out of the spec with a deliberately small
// parser: the document's shape is fixed and known, and pulling in a YAML
// dependency to read two levels of it would be more machinery than the check
// is worth.
func specOperations(t *testing.T, spec []byte) []specOperation {
	t.Helper()
	var out []specOperation
	var path string
	inPaths := false
	for _, line := range strings.Split(string(spec), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
			continue
		case line == "components:":
			inPaths = false
		}
		if !inPaths || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if path == "" || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		method := strings.TrimSuffix(strings.TrimSpace(line), ":")
		switch method {
		case "get", "post", "patch", "delete", "put":
			out = append(out, specOperation{method: strings.ToUpper(method), path: path})
		}
	}
	if len(out) == 0 {
		t.Fatal("no operations found in the OpenAPI document; the parser above is wrong")
	}
	return out
}
