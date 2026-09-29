package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// mustBeLimit asserts the refusal every limits.* ceiling gives: a 409, the
// stable code a client can switch on, and a message and field naming the
// setting -- the one thing an operator reading it needs in order to act.
func mustBeLimit(t *testing.T, resp *response, setting, what string) {
	t.Helper()
	resp.mustStatus(t, http.StatusConflict, what)
	if code := resp.errorCode(t); code != codeLimitReached {
		t.Errorf("code = %q, want %q", code, codeLimitReached)
	}
	if msg := resp.errorMessage(t); !strings.Contains(msg, setting) {
		t.Errorf("the refusal does not name %s: %q", setting, msg)
	}
	var env errorEnvelope
	resp.into(t, &env)
	if env.Error.Field != setting {
		t.Errorf("field = %q, want %q", env.Error.Field, setting)
	}
}

func TestPoolCreationStopsAtLimitsPools(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.Pools = 1 })
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)
	inst := h.installation()

	h.pool(inst, "existing")
	refused := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	mustBeLimit(t, refused, "limits.pools", "a pool beyond limits.pools")
	if n, _ := h.st.CountPools(h.ctx); n != 1 {
		t.Errorf("a refused create still wrote a pool: %d pools", n)
	}
}

// Import creates the same rows a POST does, so it must meet the same ceiling:
// otherwise an operator, who cannot change a platform-scoped limit, walks past
// it with one document naming many new pools. Edits to pools that exist are not
// new pools and must still go through at the ceiling.
func TestPoolImportStopsAtLimitsPools(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.Pools = 2 })
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	inst := h.installation()
	h.host("vm-1")
	h.pool(inst, "zoomies-existing")

	entry := func(name string) string {
		return "  - name: " + name + "\n    installation: " + inst.Target + "\n    labels: [" + name + "]\n"
	}
	over := "pools:\n" + entry("one") + entry("two")
	for _, dry := range []bool{true, false} {
		refused := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie,
			body: map[string]any{"document": over, "dry_run": dry}})
		mustBeLimit(t, refused, "limits.pools", "an import of two new pools with room for one")
		if n, _ := h.st.CountPools(h.ctx); n != 1 {
			t.Fatalf("dry_run=%v: a refused import wrote pools: %d pools, want 1", dry, n)
		}
	}

	// The ceiling is on the total, so one new pool still fits...
	importPools(t, h, cookie, map[string]any{"document": "pools:\n" + entry("one")}, http.StatusOK)
	// ...and at the ceiling an edit is admitted where a new pool is not.
	edit := "pools:\n  - name: zoomies-existing\n    installation: " + inst.Target + "\n    max_runners: 3\n"
	out := importPools(t, h, cookie, map[string]any{"document": edit}, http.StatusOK)
	if !out.Applied || out.Summary.Change != 1 {
		t.Fatalf("an edit at the ceiling = %+v, want it applied", out)
	}
	refused := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie,
		body: map[string]any{"document": "pools:\n" + entry("two")}})
	mustBeLimit(t, refused, "limits.pools", "a new pool at the ceiling")
}

func TestJoinTokenMintingStopsAtLimitsJoinTokens(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.JoinTokens = 2 })
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	mint := func() *response {
		return h.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: cookie,
			body: map[string]any{"ttl": "1h"}})
	}
	for range 2 {
		mint().mustStatus(t, http.StatusCreated, "a join token under the ceiling")
	}
	mustBeLimit(t, mint(), "limits.join_tokens", "a third outstanding join token under a ceiling of 2")
}

// A host is the expensive thing a join token buys, so the ceiling is checked
// before the token is spent: the agent refused for want of room keeps a token
// it can use once a host is removed.
func TestAgentJoinStopsAtLimitsHostsWithoutSpendingTheToken(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.Hosts = 1 })
	h.host("existing")

	_, plaintext, err := h.ctrl.Auth().CreateJoinToken(h.ctx, time.Hour, nil, 2, "test")
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	body := map[string]any{
		"protocol_version": 1, "join_token": plaintext, "name": "vm-two",
		"capacity": 2, "os": "linux", "arch": "amd64", "version": "test",
	}
	refused := h.do(request{method: http.MethodPost, path: "/api/v1/agent/join", body: body})
	mustBeLimit(t, refused, "limits.hosts", "a host beyond limits.hosts")
	if n, _ := h.st.CountOutstandingJoinTokens(h.ctx); n != 1 {
		t.Errorf("the refused join spent its token: %d outstanding, want 1", n)
	}
}

func TestTheEventStreamStopsAtLimitsEventSubscribers(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.EventSubscribers = 1 })
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, first := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("the first stream was refused: %d", first.StatusCode)
	}
	<-frames // the "connected" comment: the subscription is certainly held

	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	second, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the second stream: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a stream beyond limits.event_subscribers", second.StatusCode)
	}
	raw, _ := io.ReadAll(second.Body)
	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("the refusal is not the error envelope: %s", raw)
	}
	if env.Error.Code != codeLimitReached || !strings.Contains(env.Error.Message, "limits.event_subscribers") {
		t.Errorf("refusal = %+v", env.Error)
	}
}
