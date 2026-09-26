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
	"strconv"
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
	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/go-chi/chi/v5"
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
	logs *logCapture
}

// logCapture is the controller's own log, kept so that a test can assert on
// what it wrote. It exists for the secret-absence tests: an operator's log is
// read by more people than the API is, is shipped to wherever logs are shipped,
// and outlives the request, so a credential written into it is the leak that
// lasts longest.
//
// It records attribute values rather than a formatted line, because a text
// handler escapes what it writes and a secret would then be searched for in one
// form and present in another. The lines live in a sink the derived handlers
// share, because the request logger is a With() of the root one and its lines
// are exactly the ones worth reading.
type logCapture struct {
	sink *logSink
	pre  []slog.Attr
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func newLogCapture() *logCapture { return &logCapture{sink: &logSink{}} }

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Level.String())
	b.WriteString(" ")
	b.WriteString(r.Message)
	write := func(a slog.Attr) {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(a.Value.String())
	}
	for _, a := range c.pre {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool { write(a); return true })

	c.sink.mu.Lock()
	defer c.sink.mu.Unlock()
	c.sink.lines = append(c.sink.lines, b.String())
	return nil
}

func (c *logCapture) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logCapture{sink: c.sink, pre: append(append([]slog.Attr{}, c.pre...), attrs...)}
}

func (c *logCapture) WithGroup(string) slog.Handler { return c }

// text is everything logged so far, as one string to search.
func (c *logCapture) text() string {
	c.sink.mu.Lock()
	defer c.sink.mu.Unlock()
	return strings.Join(c.sink.lines, "\n")
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
	logs := newLogCapture()
	logger := slog.New(logs)
	// A registry holding the fake, so the provider routes have a driver to
	// answer about. Without one every one of them would answer "this build
	// has no driver for that kind", and the walks below would be asserting
	// that a refusal is well-formed rather than that a route works.
	providers, err := provider.NewRegistry(provider.NewFakeFactory())
	if err != nil {
		t.Fatalf("provider.NewRegistry: %v", err)
	}
	ctrl, err := controller.New(controller.Options{
		Store:     st,
		Config:    cfg,
		Key:       key,
		Auth:      auth.New(st, cfg, bus, auth.WithLogger(logger)),
		Events:    bus,
		GitHub:    &fakeFactory{gh: gh},
		Providers: providers,
		Logger:    logger,
		Clock:     time.Now,
	})
	if err != nil {
		t.Fatalf("controller.New: %v", err)
	}

	// A short stream heartbeat, because it is also how often a live stream
	// re-checks the credential it was opened with: at the shipped twenty
	// seconds the revocation tests would each wait one.
	s, err := New(Options{Controller: ctrl, Logger: logger, StreamHeartbeat: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	return &harness{t: t, srv: srv, api: s, ctrl: ctrl, st: st, gh: gh, cfg: cfg, key: key, ctx: ctx, logs: logs}
}

// setupToken is the credential the first-run route asks for. It is minted per
// process by the auth service and printed at startup, so a test reads it the
// same way an operator reads it out of the log.
func (h *harness) setupToken() string { return h.ctrl.Auth().SetupToken() }

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

// provider is a configured provider of the kind the fake registry can build,
// so a route that goes on to talk to a driver has one to talk to.
func (h *harness) provider(name string) *store.Provider {
	h.t.Helper()
	p := &store.Provider{
		Kind:               store.ProviderFake,
		Name:               name,
		Endpoint:           "https://pve.example.com:8006",
		Settings:           store.StringMap{"zone": "zone-a"},
		MachineCapacity:    2,
		MachineBackend:     store.BackendDocker,
		MaxMachines:        4,
		MaxCreatesInFlight: 1,
		IdleTimeout:        store.Duration(15 * time.Minute),
		Enabled:            true,
	}
	if err := h.st.CreateProvider(h.ctx, p); err != nil {
		h.t.Fatalf("CreateProvider: %v", err)
	}
	sealed, err := h.key.SealString("prv-token-" + name)
	if err != nil {
		h.t.Fatalf("SealString: %v", err)
	}
	if err := h.st.SetProviderCredentials(h.ctx, p.ID, sealed); err != nil {
		h.t.Fatalf("SetProviderCredentials: %v", err)
	}
	got, err := h.st.GetProvider(h.ctx, p.ID)
	if err != nil {
		h.t.Fatalf("GetProvider: %v", err)
	}
	return got
}

// machine is a ready machine belonging to a provider, written straight into
// the state a test needs rather than replayed through the whole lifecycle.
func (h *harness) machine(p *store.Provider, name string) *store.Machine {
	h.t.Helper()
	m := &store.Machine{
		ProviderID:   p.ID,
		Name:         name,
		State:        store.MachinePlanned,
		ResourceZone: "zone-a",
		ResourceID:   machineResourceID(name),
		Capacity:     p.MachineCapacity,
		Labels:       store.StringMap{},
	}
	if err := h.st.CreateMachine(h.ctx, m); err != nil {
		h.t.Fatalf("CreateMachine: %v", err)
	}
	return m
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
		// The installation is the pool's, as ingest would have resolved it
		// from the repository: a job carrying none is ineligible for every
		// pool, which is a different test from the ones using this helper.
		InstallationID: pool.InstallationID,
		PoolID:         pool.ID,
		Matched:        true,
		QueuedAt:       time.Now().Add(-time.Minute),
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
	// action is the permission the route's own gate checks, and the scope a
	// narrowed token therefore has to carry. Empty means no gate: the public
	// routes, and the three self-service ones every signed-in identity may
	// call. It is written out beside the role for the same reason the role is
	// -- a column read back off the router would agree with a mistake.
	action auth.Action
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
		// Off by default, so this walk sees a 404; the modes are status_test.go's.
		{method: "GET", path: "/api/v1/status", public: true},

		{method: "POST", path: "/api/v1/auth/login", public: true, checksCredentials: true,
			body: map[string]any{"username": "nobody", "password": "x"}},
		{method: "POST", path: "/api/v1/auth/bootstrap", public: true, body: map[string]any{"username": "nobody", "password": testPassword, "setup_token": "not-the-token"}},
		{method: "GET", path: "/api/v1/auth/oidc/start", public: true},
		{method: "GET", path: "/api/v1/auth/oidc/callback", public: true},

		{method: "GET", path: "/api/v1/auth/session", role: store.RoleViewer},
		{method: "GET", path: "/api/v1/auth/preferences", role: store.RoleViewer},
		{method: "PUT", path: "/api/v1/auth/preferences", role: store.RoleViewer,
			body: map[string]any{"table_layouts": map[string]any{}}},
		{method: "POST", path: "/api/v1/auth/logout", role: store.RoleViewer},
		{method: "POST", path: "/api/v1/auth/password", role: store.RoleViewer,
			body: map[string]any{"old_password": "x", "new_password": testPassword}},

		{method: "GET", path: "/api/v1/stats", role: store.RoleViewer, action: auth.ActionStatsRead},
		{method: "GET", path: "/api/v1/samples", role: store.RoleViewer, action: auth.ActionStatsRead},
		{method: "GET", path: "/api/v1/problems", role: store.RoleViewer, action: auth.ActionStatsRead},
		{method: "GET", path: "/api/v1/scaling-events", role: store.RoleViewer, action: auth.ActionStatsRead},
		{method: "GET", path: "/api/v1/events", role: store.RoleViewer, action: auth.ActionEventsRead},
		{method: "GET", path: "/api/v1/usage", role: store.RoleViewer, action: auth.ActionUsageRead},
		{method: "GET", path: "/api/v1/usage.csv", role: store.RoleViewer, action: auth.ActionUsageRead},

		{method: "GET", path: "/api/v1/installations", role: store.RoleViewer, action: auth.ActionInstallationsRead},
		{method: "POST", path: "/api/v1/installations", role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionInstallationsWrite},
		{method: "GET", path: "/api/v1/installations/" + ids.installation, role: store.RoleViewer, action: auth.ActionInstallationsRead},
		{method: "PATCH", path: "/api/v1/installations/" + ids.installation, role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionInstallationsWrite},
		{method: "DELETE", path: "/api/v1/installations/missing", role: store.RoleAdmin, action: auth.ActionInstallationsDelete},
		{method: "POST", path: "/api/v1/installations/" + ids.installation + "/verify", role: store.RoleOperator, action: auth.ActionInstallationsVerify},
		{method: "POST", path: "/api/v1/installations/" + ids.installation + "/export", role: store.RoleAdmin, action: auth.ActionInstallationsExport},
		{method: "POST", path: "/api/v1/installations/import", role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionInstallationsWrite},
		{method: "GET", path: "/api/v1/installations/" + ids.installation + "/runner-groups", role: store.RoleViewer, action: auth.ActionInstallationsRead},
		{method: "GET", path: "/api/v1/installations/" + ids.installation + "/rate-limit", role: store.RoleViewer, action: auth.ActionInstallationsRead},
		{method: "GET", path: "/api/v1/installations/" + ids.installation + "/report", role: store.RoleViewer, action: auth.ActionUsageRead},
		{method: "POST", path: "/api/v1/installations/manifest", role: store.RoleAdmin, action: auth.ActionInstallationsWrite,
			body: map[string]any{"target": "acme", "target_type": "org"}},
		{method: "POST", path: "/api/v1/installations/manifest/handoff", role: store.RoleAdmin, action: auth.ActionInstallationsWrite},
		{method: "POST", path: "/api/v1/installations/manifest/exchange", role: store.RoleAdmin, action: auth.ActionInstallationsWrite,
			body: map[string]any{"code": ""}},
		{method: "GET", path: "/api/v1/webhook-deliveries", role: store.RoleViewer, action: auth.ActionWebhooksRead},
		{method: "POST", path: "/api/v1/webhook-test", role: store.RoleOperator, action: auth.ActionWebhooksTest},

		{method: "GET", path: "/api/v1/pools", role: store.RoleViewer, action: auth.ActionPoolsRead},
		{method: "POST", path: "/api/v1/pools", role: store.RoleOperator, body: map[string]any{}, action: auth.ActionPoolsWrite},
		{method: "POST", path: "/api/v1/pools/validate", role: store.RoleOperator, body: map[string]any{}, action: auth.ActionPoolsWrite},
		{method: "GET", path: "/api/v1/pools/platforms", role: store.RoleViewer, action: auth.ActionPoolsRead},
		{method: "GET", path: "/api/v1/pools/defaults", role: store.RoleViewer, action: auth.ActionPoolsRead},
		{method: "GET", path: "/api/v1/pools/export", role: store.RoleViewer, action: auth.ActionPoolsRead},
		{method: "POST", path: "/api/v1/pools/import", role: store.RoleOperator, body: map[string]any{"dry_run": true}, action: auth.ActionPoolsWrite},
		{method: "GET", path: "/api/v1/pools/" + ids.pool, role: store.RoleViewer, action: auth.ActionPoolsRead},
		{method: "PATCH", path: "/api/v1/pools/" + ids.pool, role: store.RoleOperator, body: map[string]any{}, action: auth.ActionPoolsWrite},
		{method: "DELETE", path: "/api/v1/pools/missing", role: store.RoleOperator, action: auth.ActionPoolsDelete},
		{method: "POST", path: "/api/v1/pools/" + ids.pool + "/enable", role: store.RoleOperator, action: auth.ActionPoolsWrite},
		{method: "POST", path: "/api/v1/pools/" + ids.pool + "/disable", role: store.RoleOperator, action: auth.ActionPoolsWrite},
		{method: "POST", path: "/api/v1/pools/" + ids.pool + "/prewarm", role: store.RoleOperator, action: auth.ActionPoolsWrite},
		{method: "GET", path: "/api/v1/toolchains", role: store.RoleViewer, action: auth.ActionPoolsRead},
		{method: "POST", path: "/api/v1/toolchains/scan", role: store.RoleOperator, action: auth.ActionPoolsWrite},

		{method: "GET", path: "/api/v1/runners", role: store.RoleViewer, action: auth.ActionRunnersRead},
		{method: "GET", path: "/api/v1/runners/" + ids.runner, role: store.RoleViewer, action: auth.ActionRunnersRead},
		{method: "DELETE", path: "/api/v1/runners/missing", role: store.RoleOperator, action: auth.ActionRunnersDelete},
		{method: "POST", path: "/api/v1/runners/missing/drain", role: store.RoleOperator, action: auth.ActionRunnersDrain},
		{method: "GET", path: "/api/v1/runners/" + ids.runner + "/timeline", role: store.RoleViewer, action: auth.ActionRunnersRead},
		{method: "POST", path: "/api/v1/runners/bulk", role: store.RoleOperator, action: auth.ActionRunnersDrain,
			body: map[string]any{"action": "drain", "ids": []string{"missing"}}},
		{method: "GET", path: "/api/v1/runners/missing/logs", role: store.RoleViewer, action: auth.ActionLogsRead},
		{method: "GET", path: "/api/v1/runners/missing/logs/download", role: store.RoleViewer, action: auth.ActionLogsRead},

		{method: "GET", path: "/api/v1/provisioning", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "GET", path: "/api/v1/provisioning/selection", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "POST", path: "/api/v1/provisioning/bulk", role: store.RoleOperator, action: auth.ActionProvisioningWrite},
		{method: "GET", path: "/api/v1/jobs", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "GET", path: "/api/v1/workflow-runs", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "POST", path: "/api/v1/workflow-runs/cancel", role: store.RoleOperator, action: auth.ActionJobsCancel,
			body: map[string]any{"repo": "acme/widgets", "run_id": 1}},
		{method: "POST", path: "/api/v1/workflow-runs/provisioning", role: store.RoleOperator, action: auth.ActionProvisioningWrite,
			body: map[string]any{"repo": "acme/widgets", "run_id": 1, "action": "pause"}},
		{method: "GET", path: "/api/v1/jobs/facets", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "GET", path: "/api/v1/jobs/" + ids.job, role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "GET", path: "/api/v1/jobs/" + ids.job + "/events", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "GET", path: "/api/v1/jobs/" + ids.job + "/explanation", role: store.RoleViewer, action: auth.ActionJobsRead},
		{method: "POST", path: "/api/v1/jobs/" + ids.job + "/cancel", role: store.RoleOperator, action: auth.ActionJobsCancel},
		{method: "POST", path: "/api/v1/jobs/" + ids.job + "/rerun", role: store.RoleOperator, action: auth.ActionJobsRerun},

		{method: "GET", path: "/api/v1/diagnostics/bundle", role: store.RoleAdmin, action: auth.ActionDiagnosticsRead},

		{method: "GET", path: "/api/v1/backups", role: store.RolePlatform, action: auth.ActionBackupsRead},
		{method: "POST", path: "/api/v1/backups", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "POST", path: "/api/v1/backups/upload", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "DELETE", path: "/api/v1/backups/restore", role: store.RolePlatform, action: auth.ActionBackupsRestore},
		{method: "POST", path: "/api/v1/backups/restore/apply", role: store.RolePlatform, action: auth.ActionBackupsRestore},
		{method: "DELETE", path: "/api/v1/backups/restore/outcome", role: store.RolePlatform, action: auth.ActionBackupsRestore},
		{method: "GET", path: "/api/v1/backups/zoomies-19990101-000000", role: store.RolePlatform, action: auth.ActionBackupsRead},
		{method: "DELETE", path: "/api/v1/backups/zoomies-19990101-000000", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "POST", path: "/api/v1/backups/zoomies-19990101-000000/verify", role: store.RolePlatform, action: auth.ActionBackupsRead},
		{method: "GET", path: "/api/v1/backups/zoomies-19990101-000000/download", role: store.RolePlatform, action: auth.ActionBackupsRead},
		{method: "POST", path: "/api/v1/backups/zoomies-19990101-000000/download", role: store.RolePlatform, action: auth.ActionBackupsRead,
			body: map[string]any{"passphrase": "long enough"}},
		{method: "POST", path: "/api/v1/backups/zoomies-19990101-000000/restore", role: store.RolePlatform, action: auth.ActionBackupsRestore},
		{method: "POST", path: "/api/v1/backups/offsite", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "POST", path: "/api/v1/backups/prune", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "POST", path: "/api/v1/backups/remotes", role: store.RolePlatform, action: auth.ActionBackupsWrite,
			body: map[string]any{"name": "wherever", "endpoint": "https://s3.example.com", "bucket": "b", "secret_access_key": "s"}},
		{method: "POST", path: "/api/v1/backups/remotes/check", role: store.RolePlatform, action: auth.ActionBackupsRead,
			body: map[string]any{"name": "wherever", "endpoint": "https://s3.example.com", "bucket": "b", "secret_access_key": "s"}},
		{method: "PATCH", path: "/api/v1/backups/remotes/offsite", role: store.RolePlatform, action: auth.ActionBackupsWrite,
			body: map[string]any{"keep": 3}},
		{method: "DELETE", path: "/api/v1/backups/remotes/offsite", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "GET", path: "/api/v1/backups/remotes/offsite/copies", role: store.RolePlatform, action: auth.ActionBackupsRead},
		{method: "POST", path: "/api/v1/backups/remotes/offsite/check", role: store.RolePlatform, action: auth.ActionBackupsRead},
		{method: "POST", path: "/api/v1/backups/remotes/offsite/copies/zoomies-19990101-000000/fetch", role: store.RolePlatform, action: auth.ActionBackupsWrite},
		{method: "DELETE", path: "/api/v1/backups/remotes/offsite/copies/zoomies-19990101-000000", role: store.RolePlatform, action: auth.ActionBackupsWrite},

		{method: "GET", path: "/api/v1/recovery", role: store.RoleViewer, action: auth.ActionStatsRead},
		{method: "POST", path: "/api/v1/recovery/unfence", role: store.RolePlatform, action: auth.ActionRecoveryWrite},

		{method: "GET", path: "/api/v1/hosts", role: store.RoleViewer, action: auth.ActionHostsRead},

		{method: "GET", path: "/api/v1/hosts/samples", role: store.RoleViewer, action: auth.ActionHostsRead},
		{method: "GET", path: "/api/v1/hosts/" + ids.host, role: store.RoleViewer, action: auth.ActionHostsRead},
		{method: "PATCH", path: "/api/v1/hosts/" + ids.host, role: store.RoleOperator, body: map[string]any{}, action: auth.ActionHostsWrite},
		{method: "POST", path: "/api/v1/hosts/" + ids.host + "/cordon", role: store.RoleOperator, action: auth.ActionHostsCordon,
			body: map[string]any{"cordoned": false}},
		{method: "POST", path: "/api/v1/hosts/" + ids.host + "/throttle/clear", role: store.RoleOperator, action: auth.ActionHostsWrite},
		{method: "DELETE", path: "/api/v1/hosts/missing", role: store.RoleAdmin, action: auth.ActionHostsDelete},

		{method: "GET", path: "/api/v1/providers", role: store.RoleViewer, action: auth.ActionProvidersRead},
		{method: "POST", path: "/api/v1/providers", role: store.RoleAdmin, action: auth.ActionProvidersWrite,
			body: map[string]any{"kind": "fake", "name": "made-by-the-route-walk", "settings": map[string]string{"zone": "zone-a"}}},
		{method: "POST", path: "/api/v1/providers/validate", role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionProvidersWrite},
		{method: "POST", path: "/api/v1/providers/discover", role: store.RoleAdmin, action: auth.ActionProvidersWrite,
			body: map[string]any{"kind": "fake", "name": "draft", "credential": "tok", "settings": map[string]string{"zone": "zone-a"}}},
		{method: "GET", path: "/api/v1/providers/kinds", role: store.RoleViewer, action: auth.ActionProvidersRead},
		{method: "GET", path: "/api/v1/providers/" + ids.provider, role: store.RoleViewer, action: auth.ActionProvidersRead},
		{method: "PATCH", path: "/api/v1/providers/" + ids.provider, role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionProvidersWrite},
		{method: "DELETE", path: "/api/v1/providers/missing", role: store.RoleAdmin, action: auth.ActionProvidersDelete},
		{method: "POST", path: "/api/v1/providers/" + ids.provider + "/check", role: store.RoleOperator, action: auth.ActionProvidersPause},
		{method: "GET", path: "/api/v1/providers/" + ids.provider + "/discovery", role: store.RoleOperator, action: auth.ActionProvidersPause},
		{method: "GET", path: "/api/v1/providers/" + ids.provider + "/orphans", role: store.RoleAdmin, action: auth.ActionProvidersWrite},
		{method: "POST", path: "/api/v1/providers/" + ids.provider + "/pause", role: store.RoleOperator, action: auth.ActionProvidersPause},
		{method: "POST", path: "/api/v1/providers/" + ids.provider + "/resume", role: store.RoleOperator, action: auth.ActionProvidersPause},

		{method: "GET", path: "/api/v1/machines", role: store.RoleViewer, action: auth.ActionMachinesRead},
		{method: "GET", path: "/api/v1/machines/" + ids.machine, role: store.RoleViewer, action: auth.ActionMachinesRead},
		{method: "POST", path: "/api/v1/machines/missing/drain", role: store.RoleOperator, action: auth.ActionMachinesDrain},
		{method: "DELETE", path: "/api/v1/machines/missing", role: store.RoleAdmin, action: auth.ActionMachinesDelete},
		{method: "POST", path: "/api/v1/machines/missing/release", role: store.RoleAdmin, action: auth.ActionMachinesDelete,
			body: map[string]any{"name": "never-the-right-name"}},

		{method: "GET", path: "/api/v1/join-tokens", role: store.RoleAdmin, action: auth.ActionJoinsRead},
		{method: "GET", path: "/api/v1/join-tokens/missing", role: store.RoleAdmin, action: auth.ActionJoinsRead},
		{method: "POST", path: "/api/v1/join-tokens", role: store.RoleAdmin, body: map[string]any{"ttl": "15m"}, action: auth.ActionJoinsWrite},
		{method: "DELETE", path: "/api/v1/join-tokens/missing", role: store.RoleAdmin, action: auth.ActionJoinsWrite},

		{method: "POST", path: "/api/v1/migrations/plan", role: store.RoleOperator, action: auth.ActionMigrationsRead,
			body: map[string]any{"installation_id": ids.installation}},
		{method: "POST", path: "/api/v1/migrations/pull-requests", role: store.RoleOperator, action: auth.ActionMigrationsWrite,
			body: map[string]any{"installation_id": ids.installation, "repos": []string{}, "mapping": map[string]string{}}},

		{method: "GET", path: "/api/v1/audit", role: store.RoleViewer, action: auth.ActionAuditRead},
		{method: "GET", path: "/api/v1/audit/actions", role: store.RoleViewer, action: auth.ActionAuditRead},

		{method: "GET", path: "/api/v1/users", role: store.RoleAdmin, action: auth.ActionUsersRead},
		{method: "POST", path: "/api/v1/users", role: store.RoleAdmin, body: map[string]any{"username": "", "role": "viewer"}, action: auth.ActionUsersWrite},
		{method: "GET", path: "/api/v1/users/missing", role: store.RoleAdmin, action: auth.ActionUsersRead},
		{method: "PATCH", path: "/api/v1/users/missing", role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionUsersWrite},
		{method: "DELETE", path: "/api/v1/users/missing", role: store.RoleAdmin, action: auth.ActionUsersWrite},
		{method: "POST", path: "/api/v1/users/missing/password", role: store.RoleAdmin, action: auth.ActionUsersWrite,
			body: map[string]any{"new_password": testPassword}},

		{method: "GET", path: "/api/v1/tokens", role: store.RoleAdmin, action: auth.ActionTokensRead},
		{method: "POST", path: "/api/v1/tokens", role: store.RoleAdmin, body: map[string]any{"name": "", "role": "viewer"}, action: auth.ActionTokensWrite},
		{method: "DELETE", path: "/api/v1/tokens/missing", role: store.RoleAdmin, action: auth.ActionTokensWrite},

		{method: "GET", path: "/api/v1/settings", role: store.RoleAdmin, action: auth.ActionSettingsRead},
		{method: "PATCH", path: "/api/v1/settings", role: store.RoleAdmin, body: map[string]any{}, action: auth.ActionSettingsWrite},
		{method: "GET", path: "/api/v1/settings/export", role: store.RoleAdmin, action: auth.ActionSettingsRead},
		{method: "POST", path: "/api/v1/settings/import", role: store.RoleAdmin, action: auth.ActionSettingsWrite,
			body: map[string]any{"document": "log:\n  level: info\n", "dry_run": true}},

		{method: "GET", path: "/metrics", role: store.RoleViewer, action: auth.ActionMetricsRead},
	}
}

type fixtureIDs struct {
	installation string
	pool         string
	runner       string
	host         string
	job          string
	provider     string
	machine      string
}

func (h *harness) fixtures() fixtureIDs {
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	run := h.runner(pool, host, store.RunnerIdle)
	job := h.job(pool, store.JobQueued)
	prov := h.provider("proxmox-lab")
	machine := h.machine(prov, "zoomies-mach-fixture")
	return fixtureIDs{
		installation: inst.ID, pool: pool.ID, runner: run.ID, host: host.ID, job: job.ID,
		provider: prov.ID, machine: machine.ID,
	}
}

// TestRouteAuthorisation walks every route with five callers.
//
// This is the test that catches an endpoint added without a role: an
// unauthenticated request must be refused, a viewer must not be able to
// write, and each role in turn must get through to exactly the handlers its
// authority reaches. The platform caller is the fifth, and the rows it alone
// may reach -- the fence and the backups -- are the ones that would otherwise
// silently fall back to an administrator.
func TestRouteAuthorisation(t *testing.T) {
	h := newHarness(t)
	ids := h.fixtures()

	viewer, _ := h.user("viewer", store.RoleViewer)
	operator, _ := h.user("operator", store.RoleOperator)
	admin, _ := h.user("admin", store.RoleAdmin)
	platform, _ := h.user("platform", store.RolePlatform)

	callers := []struct {
		name string
		role store.Role
		user *store.User
	}{
		{name: "anonymous"},
		{name: "viewer", role: store.RoleViewer, user: viewer},
		{name: "operator", role: store.RoleOperator, user: operator},
		{name: "admin", role: store.RoleAdmin, user: admin},
		{name: "platform", role: store.RolePlatform, user: platform},
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

// TestScopedTokenRouteAuthorisation is the walk for the second gate.
//
// A role says how much of the fleet an identity may touch; a scope narrows a
// token below its role, and it is the mechanism behind every "give CI a token
// that can only drain runners". Until now exactly one route had a test for it,
// and that one was really testing a handler's second check on its body. The
// route gate itself -- s.require, on sixty-odd routes -- had none, so a token
// scoped to nothing in particular would have reached all of them.
//
// Every token here carries the admin role, so a refusal can only be about the
// scope: if the role gate fired instead, the message would name a role and the
// assertion below would catch it.
func TestScopedTokenRouteAuthorisation(t *testing.T) {
	h := newHarness(t)
	ids := h.fixtures()

	// One token per resource, carrying every scope in the system except that
	// resource's. That is stronger than a single decoy scope: it proves the
	// route is gated on its own permission and not merely on holding some
	// scope, and it survives a new action being added elsewhere.
	elsewhere := map[string]string{}
	tokenWithoutResource := func(res string) string {
		if tok, ok := elsewhere[res]; ok {
			return tok
		}
		var scopes []string
		for _, a := range auth.AllActions() {
			if a.Resource() != res {
				scopes = append(scopes, a.Scope())
			}
		}
		// Minted at the top role on purpose: the only thing this token is
		// missing must be the scope, or the refusal it collects is about
		// the role gate and the scope gate goes untested.
		tok := h.token("everything-but-"+res, store.RolePlatform, scopes...)
		elsewhere[res] = tok
		return tok
	}

	gated := 0
	for _, rt := range routeTable(ids) {
		if rt.action == "" {
			continue
		}
		gated++
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			refused := h.do(request{method: rt.method, path: rt.path, body: rt.body,
				token: tokenWithoutResource(rt.action.Resource())})
			refused.mustStatus(t, http.StatusForbidden, "a token scoped to every other resource")
			// The message names the scope that is missing, because the person
			// reading it has to know what to add to the token.
			if msg := refused.errorMessage(t); !strings.Contains(msg, rt.action.Scope()) {
				t.Errorf("the refusal does not name the %q scope: %q", rt.action.Scope(), msg)
			}

			// The positive half, three ways of holding the permission. Without
			// it, a gate that refused everything would pass the walk.
			for _, scope := range []string{rt.action.Scope(), rt.action.Resource() + ":*", "*"} {
				// At the route's own role, so this half tests the scope
				// and nothing else.
				allowed := h.do(request{method: rt.method, path: rt.path, body: rt.body,
					token: h.token(scope+" for "+rt.method+" "+rt.path, rt.role, scope)})
				if allowed.status == http.StatusForbidden || allowed.status == http.StatusUnauthorized {
					t.Errorf("a token scoped to %q was refused %d: %s", scope, allowed.status, truncate(allowed.body))
				}
			}
		})
	}
	if gated == 0 {
		t.Fatal("no route in the table carries an action, so this walked nothing")
	}
}

// TestEveryActionIsReachableThroughARoute keeps the column above honest in the
// direction the walk cannot: an action nothing in the table claims is either a
// permission no route enforces -- so a token scoped to it grants nothing and an
// operator has been sold a scope that does not exist -- or a route that arrived
// without a row, which is the same hole the role walk exists to close.
func TestEveryActionIsReachableThroughARoute(t *testing.T) {
	h := newHarness(t)
	claimed := map[auth.Action]bool{}
	for _, rt := range routeTable(h.fixtures()) {
		if rt.action != "" {
			claimed[rt.action] = true
		}
	}
	for _, a := range auth.AllActions() {
		if !claimed[a] {
			t.Errorf("%s is a permission no route in the table checks", a)
		}
	}
}

// TestScopesAreNarrowerThanRoles is the property the two walks together imply
// and neither states: a scope may take permissions away from a role and must
// never add one back. An admin-scoped token held by a viewer is still a viewer.
func TestScopesAreNarrowerThanRoles(t *testing.T) {
	h := newHarness(t)
	ids := h.fixtures()

	// A viewer holding the widest scope there is.
	viewer := h.token("wide-open viewer", store.RoleViewer, "*")

	resp := h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + ids.pool, token: viewer})
	resp.mustStatus(t, http.StatusForbidden, "a viewer with the * scope deleting a pool")
	if msg := resp.errorMessage(t); !strings.Contains(msg, string(store.RoleOperator)) {
		t.Errorf("the refusal does not name the role that is missing: %q", msg)
	}
	// And the reading it may do is unaffected.
	read := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: viewer})
	read.mustStatus(t, http.StatusOK, "a viewer with the * scope listing pools")
}

// TestRouteTableCoversTheSpec checks the hand-written table against the
// OpenAPI document, so a path added to the contract without a test here is
// reported rather than quietly untested.
func TestRouteTableCoversTheSpec(t *testing.T) {
	placeholders := fixtureIDs{
		installation: "ins_x", pool: "pool_x", runner: "run_x", host: "host_x", job: "job_x",
		provider: "prv_x", machine: "mach_x",
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
		if op.internal {
			// The agent routes carry a different credential class and are
			// exercised by agents_test.go with agent tokens.
			continue
		}
		key := op.method + " " + op.path
		if !tested[key] {
			t.Errorf("%s is in api/openapi.yaml but not in the route table", key)
		}
	}
}

// TestTheSpecCoversTheRouter is the other direction. The document's info block
// says it is the whole surface, and until this test the check ran one way
// only: everything in the spec had a route, while eight routes had no spec.
func TestTheSpecCoversTheRouter(t *testing.T) {
	h := newHarness(t)
	spec, err := openapiSpec()
	if err != nil {
		t.Fatalf("openapiSpec: %v", err)
	}
	documented := map[string]bool{}
	for _, op := range specOperations(t, spec) {
		documented[op.method+" "+op.path] = true
	}
	routes, ok := h.api.handler.(chi.Routes)
	if !ok {
		t.Fatalf("the server's handler is a %T, not a chi router", h.api.handler)
	}
	err = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") {
			// Health, the spec itself, the webhook, metrics and the UI are
			// not API operations; docs/api-surface.md describes them.
			return nil
		}
		path := strings.TrimPrefix(route, "/api/v1")
		if len(path) > 1 {
			// chi reports the root of a Route group with a trailing slash.
			path = strings.TrimSuffix(path, "/")
		}
		if !documented[method+" "+path] {
			t.Errorf("%s %s is served but not in api/openapi.yaml", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
}

// TestEveryOperationRecordsItsRole holds the info block to its word: each
// operation names its minimum role, unless it is anonymous or an agent route.
// Three operations shipped without one and nothing noticed.
func TestEveryOperationRecordsItsRole(t *testing.T) {
	spec, err := openapiSpec()
	if err != nil {
		t.Fatalf("openapiSpec: %v", err)
	}
	for _, op := range specOperations(t, spec) {
		switch {
		case op.internal:
			if op.role != "" {
				t.Errorf("%s %s is an agent route but names a user role %q", op.method, op.path, op.role)
			}
		case op.anonymous:
			if op.role != "" {
				t.Errorf("%s %s is anonymous but names a role %q", op.method, op.path, op.role)
			}
		case op.role == "":
			t.Errorf("%s %s has no x-zoomies-role", op.method, op.path)
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
		// A backup remote is named by its own name rather than by a
		// prefixed id, so the segment after "remotes" is the parameter
		// whatever it is called. The two verbs among them -- /backups/offsite
		// and /backups/remotes/check -- stay themselves, which is also why a
		// remote may not be called "check".
		if i > 0 && parts[i-1] == "remotes" && part != "check" {
			parts[i] = "{name}"
			continue
		}
		if strings.HasPrefix(part, "ins_") || strings.HasPrefix(part, "pool_") ||
			strings.HasPrefix(part, "run_") || strings.HasPrefix(part, "host_") ||
			strings.HasPrefix(part, "job_") || strings.HasPrefix(part, "prv_") ||
			strings.HasPrefix(part, "mach_") || strings.HasPrefix(part, "zoomies-") || part == "missing" {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// specOperation is one method-and-path pair from the OpenAPI document, with
// the three things the checks here ask of it.
type specOperation struct {
	method string
	path   string
	// internal marks an agent route: x-internal: true.
	internal bool
	// anonymous marks an operation with security: [].
	anonymous bool
	// role is the x-zoomies-role, or empty.
	role string
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
		trimmed := strings.TrimSpace(line)
		// An operation's own keys sit exactly six spaces in.
		if path != "" && len(out) > 0 && strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") {
			op := &out[len(out)-1]
			switch {
			case trimmed == "x-internal: true":
				op.internal = true
			case trimmed == "security: []":
				op.anonymous = true
			case strings.HasPrefix(trimmed, "x-zoomies-role:"):
				op.role = strings.TrimSpace(strings.TrimPrefix(trimmed, "x-zoomies-role:"))
			}
			continue
		}
		if path == "" || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		method := strings.TrimSuffix(trimmed, ":")
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

// API responses are authenticated and change by the second; a proxy or a
// browser cache that kept one would show a signed-out user the previous
// user's fleet. Every route under /api/v1 says so.
func TestAPIResponsesAreNeverCached(t *testing.T) {
	h := newHarness(t)
	token := h.token("cache", store.RoleViewer)
	for _, rt := range []request{
		{method: http.MethodGet, path: "/api/v1/meta"},
		{method: http.MethodGet, path: "/api/v1/pools", token: token},
		{method: http.MethodGet, path: "/api/v1/agent/tasks"},
	} {
		resp := h.do(rt)
		if cc := resp.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s %s: Cache-Control = %q, want no-store", rt.method, rt.path, cc)
		}
	}
}

// TestOversizeRequestBodyIsRefused covers the limit that stops a caller making
// the controller buffer memory on purpose.
//
// The status is the point as much as the refusal is. A 400 tells a client its
// JSON is wrong, and the JSON is not wrong -- there is simply too much of it,
// which is a different thing to do about it. The webhook endpoint has answered
// 413 for the same condition since it was written, so this is also the two
// halves of the surface giving one answer.
func TestOversizeRequestBodyIsRefused(t *testing.T) {
	h := newHarness(t)
	token := h.token("bulky", store.RoleAdmin)
	_, agentToken := h.agentToken("vm-1")

	// Built from the production constant, so raising the limit does not
	// quietly leave this test asserting nothing.
	oversize := `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	json := map[string]string{"Content-Type": "application/json"}

	for _, tc := range []struct {
		name string
		req  request
	}{
		{"the user API", request{method: http.MethodPost, path: "/api/v1/pools",
			token: token, rawBody: oversize, headers: json}},
		{"the anonymous agent join", request{method: http.MethodPost, path: "/api/v1/agent/join",
			rawBody: oversize, headers: json}},
		{"an authenticated agent route", request{method: http.MethodPost, path: "/api/v1/agent/heartbeat",
			token: agentToken, rawBody: oversize, headers: json}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(tc.req)
			resp.mustStatus(t, http.StatusRequestEntityTooLarge, "an oversize body on "+tc.req.path)
			if code := resp.errorCode(t); code != codeTooLarge {
				t.Errorf("code = %q, want %q", code, codeTooLarge)
			}
			// The message names the limit, because a client that has just been
			// refused needs to know what it has to fit inside.
			if msg := resp.errorMessage(t); !strings.Contains(msg, strconv.Itoa(maxBodyBytes)) {
				t.Errorf("the refusal does not name the %d byte limit: %q", maxBodyBytes, msg)
			}
		})
	}

	// A body just under the limit is not refused for its size. It is refused
	// for being a nonsense pool, which is the handler's business and proves
	// the middleware let it through.
	t.Run("a body under the limit", func(t *testing.T) {
		under := `{"name":"` + strings.Repeat("a", maxBodyBytes/2) + `"}`
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools",
			token: token, rawBody: under, headers: json})
		if resp.status == http.StatusRequestEntityTooLarge {
			t.Fatalf("a body inside the limit was refused as too large: %s", truncate(resp.body))
		}
	})

	// The log relay's exemption is asserted where it can be observed, in
	// TestARunnerThatPrintsMoreThanTheBodyLimitIsNotCutOff: a relay for a
	// stream nobody is watching answers 404 before it reads a byte, so it
	// would answer the same whether the limit applied to it or not.
}

// The Overview reads the poller's state from /meta, and the two facts it needs
// are different questions: whether the fallback poller is running at all, and
// whether it is still sweeping. A controller that has never swept says so by
// omitting the stamp rather than by sending a zero time, because "1 January
// year 1" rendered on a page is worse than a blank.
func TestMetaSaysWhetherTheFallbackPollerIsRunning(t *testing.T) {
	h := newHarness(t)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/meta"})
	resp.mustStatus(t, http.StatusOK, "meta")
	var out struct {
		PollerEnabled    bool       `json:"poller_enabled"`
		PollerLastPollAt *time.Time `json:"poller_last_poll_at"`
	}
	resp.into(t, &out)
	if !out.PollerEnabled {
		t.Error("the fallback poller is on by default and /meta says it is not")
	}
	if out.PollerLastPollAt != nil {
		t.Errorf("a controller that has not swept reported a last poll: %v", out.PollerLastPollAt)
	}
}

// The capacity map's layouts are settings, and every browser reads them from
// /meta rather than from the settings route only an administrator may see.
// The two are live, so the answer moves in the same request that changed it,
// and the two pages move separately: a fleet may want the Overview glanced
// at as one chart and the Hosts page opened a chart per machine.
func TestMetaCarriesTheCapacityMapLayoutsAndAChangeIsLive(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	read := func() (overview, hosts string) {
		t.Helper()
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/meta"})
		resp.mustStatus(t, http.StatusOK, "meta")
		var out struct {
			CapacityMap struct {
				Overview string `json:"overview_layout"`
				Hosts    string `json:"hosts_layout"`
			} `json:"capacity_map"`
		}
		resp.into(t, &out)
		return out.CapacityMap.Overview, out.CapacityMap.Hosts
	}
	if o, hl := read(); o != "overlay" || hl != "overlay" {
		t.Fatalf("the defaults are %q and %q, want overlay for both", o, hl)
	}

	patched := h.do(request{method: http.MethodPatch, path: "/api/v1/settings", cookie: cookie,
		body: map[string]any{"ui.capacity_map.hosts_layout": "split"}})
	patched.mustStatus(t, http.StatusOK, "patch the hosts layout")
	if o, hl := read(); o != "overlay" || hl != "split" {
		t.Errorf("after setting the hosts layout, meta says %q and %q; want overlay and split", o, hl)
	}
	// It is stored, so the next start reads it from the database rather than
	// forgetting what the administrator chose.
	if row, err := h.ctrl.Store().GetInstanceSetting(t.Context(), "ui.capacity_map.hosts_layout"); err != nil {
		t.Errorf("the layout was not written: %v", err)
	} else if row.Value != "split" {
		t.Errorf("stored %q, want split", row.Value)
	}

	// A layout that is not one of the two is refused, and the refusal names
	// the two, because "not a capacity map layout" alone sends somebody to
	// the documentation for a menu of two items.
	refused := h.do(request{method: http.MethodPatch, path: "/api/v1/settings", cookie: cookie,
		body: map[string]any{"ui.capacity_map.overview_layout": "sideways"}})
	refused.mustStatus(t, http.StatusUnprocessableEntity, "a layout that does not exist")
	for _, want := range []string{"overlay", "split"} {
		if !strings.Contains(string(refused.body), want) {
			t.Errorf("the refusal does not offer %q: %s", want, refused.body)
		}
	}
	if o, _ := read(); o != "overlay" {
		t.Errorf("a refused change moved the overview layout to %q", o)
	}
}
