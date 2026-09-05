package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

const testWebhookSecret = "s3cret-for-tests"

// fakeFactory hands out clients backed by github.NewFake. The private key is
// checked but not used, which is what lets a test seal a dummy key and still
// exercise the unseal-then-build path the real factory takes.
type fakeFactory struct {
	gh  *github.FakeGitHub
	err error
}

func (f *fakeFactory) For(_ context.Context, inst *store.Installation, privateKeyPEM []byte) (github.Client, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(privateKeyPEM) == 0 {
		return nil, errors.New("fake factory: no private key")
	}
	return f.gh.Client(inst.Target, inst.TargetType), nil
}

// harness is a controller wired to an in-memory store and a fake GitHub.
type harness struct {
	t       *testing.T
	c       *Controller
	st      *store.Store
	gh      *github.FakeGitHub
	factory *fakeFactory
	key     *cryptox.Key
	cfg     *config.Config
	ctx     context.Context
}

// testConfig is a configuration that validates without a single warning, so a
// test asserting "Problems is empty" is asserting about the fleet rather than
// about the defaults.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Server.Bind = "127.0.0.1:8080"
	cfg.Server.ExternalURL = "https://zoomies.test"
	cfg.Database.Path = filepath.Join(t.TempDir(), "zoomies.db")
	cfg.Security.EncryptionKey = "0000000000000000000000000000000000000000000="
	// No embedded agent: it would otherwise warn about running as root, which
	// CI often is, and this is a controller test either way.
	cfg.Agent.Embedded = false
	cfg.Agent.Backend = "process"
	cfg.Agent.WorkDir = filepath.Join(t.TempDir(), "work")
	return cfg
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	gh := github.NewFake()
	t.Cleanup(gh.Close)

	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	cfg := testConfig(t)
	bus := events.New()
	factory := &fakeFactory{gh: gh}

	c, err := New(Options{
		Store:  st,
		Config: cfg,
		Key:    key,
		Auth:   auth.New(st, cfg, bus),
		Events: bus,
		GitHub: factory,
		Logger: slog.New(slog.DiscardHandler),
		Clock:  time.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{t: t, c: c, st: st, gh: gh, factory: factory, key: key, cfg: cfg, ctx: ctx}
}

// installation seeds a GitHub App installation with a sealed webhook secret.
func (h *harness) installation() *store.Installation {
	h.t.Helper()
	pem, err := h.key.SealString("-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----")
	if err != nil {
		h.t.Fatalf("sealing key: %v", err)
	}
	secret, err := h.key.SealString(testWebhookSecret)
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

// pool seeds an enabled ephemeral docker pool.
func (h *harness) pool(inst *store.Installation, name string, labels ...string) *store.Pool {
	h.t.Helper()
	if len(labels) == 0 {
		labels = []string{"self-hosted", "linux", "x64", "demo"}
	}
	p := &store.Pool{
		Name:           name,
		InstallationID: inst.ID,
		Labels:         labels,
		Backend:        store.BackendDocker,
		Image:          "ghcr.io/eyupio/zoomies-runner:test",
		MinRunners:     0,
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

// host seeds a healthy docker host with room for four runners.
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

// fleet is the common setup: one installation, one pool, one host.
func (h *harness) fleet() (*store.Installation, *store.Pool, *store.Host) {
	inst := h.installation()
	return inst, h.pool(inst, "linux-x64"), h.host("vm-1")
}

// ---------------------------------------------------------------------------
// Webhook helpers
// ---------------------------------------------------------------------------

type jobEvent struct {
	Action     string
	JobID      int64
	RunID      int64
	Repo       string
	Name       string
	Workflow   string
	Labels     []string
	RunnerName string
	Conclusion string
	QueuedAt   time.Time
	// Steps, when set, are rendered as GitHub's steps array.
	Steps []map[string]any
}

// failingSteps renders the steps of a job that failed on its second step, which
// is the shape a completed delivery with a failure has.
func failingSteps() []map[string]any {
	return []map[string]any{
		{"number": 1, "name": "Checkout", "status": "completed", "conclusion": "success"},
		{"number": 2, "name": "Run tests", "status": "completed", "conclusion": "failure"},
		{"number": 3, "name": "Upload", "status": "completed", "conclusion": "skipped"},
	}
}

func (e jobEvent) body() []byte {
	if e.Repo == "" {
		e.Repo = "acme/widgets"
	}
	if e.QueuedAt.IsZero() {
		e.QueuedAt = time.Now().Add(-time.Minute)
	}
	status := e.Action
	if status == "waiting" {
		status = "queued"
	}
	job := map[string]any{
		"id":            e.JobID,
		"run_id":        e.RunID,
		"name":          e.Name,
		"workflow_name": e.Workflow,
		"labels":        e.Labels,
		"status":        status,
		"runner_name":   e.RunnerName,
		"created_at":    e.QueuedAt.Format(time.RFC3339),
		"html_url":      "https://github.com/" + e.Repo + "/actions/runs/1",
	}
	if e.Action == "in_progress" || e.Action == "completed" {
		job["started_at"] = e.QueuedAt.Add(30 * time.Second).Format(time.RFC3339)
	}
	if e.Action == "completed" {
		job["completed_at"] = e.QueuedAt.Add(2 * time.Minute).Format(time.RFC3339)
		job["conclusion"] = e.Conclusion
	}
	if e.Steps != nil {
		job["steps"] = e.Steps
	}
	b, _ := json.Marshal(map[string]any{
		"action":       e.Action,
		"workflow_job": job,
		"repository":   map[string]any{"full_name": e.Repo},
		"installation": map[string]any{"id": 42},
	})
	return b
}

// sign produces the HMAC header GitHub sends.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// deliver posts one webhook and returns the recorded response.
func (h *harness) deliver(event string, body []byte, secret string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set(github.EventTypeHeader, event)
	req.Header.Set(github.DeliveryIDHeader, "delivery-"+store.NewSecret(4))
	if secret != "" {
		req.Header.Set(github.SignatureHeader, sign(secret, body))
	}
	rec := httptest.NewRecorder()
	h.c.HandleWebhook(rec, req)
	return rec
}

// deliverJob posts a signed workflow_job delivery.
func (h *harness) deliverJob(e jobEvent) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.deliver("workflow_job", e.body(), testWebhookSecret)
}

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

// tasksFor returns the tasks queued for a host without consuming them.
func (h *harness) tasksFor(hostID string) []agent.Task {
	q := h.c.queues.get(hostID)
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]agent.Task, 0, len(q.pending))
	for _, lt := range q.pending {
		out = append(out, lt.task)
	}
	return out
}

// taskOfKind returns the first queued task of a kind, or fails.
func (h *harness) taskOfKind(hostID string, kind agent.TaskKind) agent.Task {
	h.t.Helper()
	for _, t := range h.tasksFor(hostID) {
		if t.Kind == kind {
			return t
		}
	}
	h.t.Fatalf("no %s task queued for host %s (queued: %v)", kind, hostID, kindsOf(h.tasksFor(hostID)))
	return agent.Task{}
}

func (h *harness) hasTaskOfKind(hostID string, kind agent.TaskKind) bool {
	for _, t := range h.tasksFor(hostID) {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

func kindsOf(tasks []agent.Task) []agent.TaskKind {
	out := make([]agent.TaskKind, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Kind)
	}
	return out
}

// runners returns every runner row, terminal ones included.
func (h *harness) runners() []*store.Runner {
	h.t.Helper()
	rs, _, err := h.st.ListRunners(h.ctx, store.RunnerFilter{IncludeRemoved: true}, store.Page{Limit: 500})
	if err != nil {
		h.t.Fatalf("ListRunners: %v", err)
	}
	return rs
}

// onlyRunner asserts there is exactly one runner and returns it.
func (h *harness) onlyRunner() *store.Runner {
	h.t.Helper()
	rs := h.runners()
	if len(rs) != 1 {
		h.t.Fatalf("expected exactly one runner, got %d", len(rs))
	}
	return rs[0]
}

// deliveries returns the recorded webhook deliveries.
func (h *harness) deliveries() []*store.WebhookDelivery {
	h.t.Helper()
	ds, err := h.st.ListDeliveries(h.ctx, "", 100)
	if err != nil {
		h.t.Fatalf("ListDeliveries: %v", err)
	}
	return ds
}

// problemCodes returns the codes Problems reports, for set assertions.
func (h *harness) problemCodes() []string {
	h.t.Helper()
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		h.t.Fatalf("Problems: %v", err)
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

// drain reads a channel until it closes or the deadline passes.
func drain(t *testing.T, ch <-chan []byte, timeout time.Duration) []byte {
	t.Helper()
	var buf bytes.Buffer
	deadline := time.After(timeout)
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return buf.Bytes()
			}
			buf.Write(b)
		case <-deadline:
			return buf.Bytes()
		}
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// byteReader yields one byte per Read, which is how a test produces many small
// chunks without a real network.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

// newGetRequest builds a GET to the webhook path, for the method check.
func newGetRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/webhooks/github", nil)
}

func recorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// mustReport plays the part of an agent asserting a runner state.
func mustReport(t *testing.T, h *harness, hostID, runnerID string, state store.RunnerState) {
	t.Helper()
	err := h.c.ReportRunners(h.ctx, hostID, []agent.RunnerReport{{
		RunnerID: runnerID, State: state, ObservedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("ReportRunners(%s): %v", state, err)
	}
}

// mustReportRunning plays the part of an agent whose workload is up but which
// deliberately asserts no runner state, because whether GitHub has given the
// runner a job is not the agent's call.
func mustReportRunning(t *testing.T, h *harness, hostID, runnerID string) {
	t.Helper()
	err := h.c.ReportRunners(h.ctx, hostID, []agent.RunnerReport{{
		RunnerID: runnerID, Phase: backend.PhaseRunning,
		Handle: backend.Handle("container-" + runnerID), ObservedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("ReportRunners(running): %v", err)
	}
}
