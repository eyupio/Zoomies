package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// TestSPAFallback covers the rule that makes client-side routing work without
// making an API mistake look like a rendered page.
func TestSPAFallback(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/pools", "/runners/run_abc", "/settings/github/setup"} {
		resp := h.do(request{method: http.MethodGet, path: path})
		resp.mustStatus(t, http.StatusOK, "GET "+path)
		if ct := resp.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s served %q, want HTML", path, ct)
		}
		if !strings.Contains(string(resp.body), "<title>Zoomies</title>") {
			t.Errorf("%s did not serve index.html: %s", path, truncate(resp.body))
		}
		if cc := resp.header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("index.html was served with Cache-Control %q; a cached one points at assets that no longer exist", cc)
		}
	}

	// An unknown API path is a real 404 with a JSON body, never the app.
	api := h.do(request{method: http.MethodGet, path: "/api/v1/nope"})
	api.mustStatus(t, http.StatusNotFound, "GET /api/v1/nope")
	if code := api.errorCode(t); code != codeNotFound {
		t.Errorf("error code = %q, want %q; the body was %s", code, codeNotFound, truncate(api.body))
	}
	if ct := api.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("an API 404 was served as %q", ct)
	}

	// So is a path that looks like a file: serving index.html for a missing
	// script produces JavaScript that is secretly HTML.
	asset := h.do(request{method: http.MethodGet, path: "/assets/missing.js"})
	asset.mustStatus(t, http.StatusNotFound, "GET a missing asset")
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	resp := h.do(request{method: http.MethodGet, path: "/"})

	csp := resp.header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy")
	}
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP is missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "script-src") && strings.Contains(csp, "'unsafe-inline'") &&
		strings.Contains(csp[strings.Index(csp, "script-src"):], "'unsafe-inline'") {
		// 'unsafe-inline' is allowed for styles, never for scripts.
		scriptPart := csp[strings.Index(csp, "script-src"):]
		if end := strings.Index(scriptPart, ";"); end > 0 {
			scriptPart = scriptPart[:end]
		}
		if strings.Contains(scriptPart, "'unsafe-inline'") {
			t.Errorf("script-src allows unsafe-inline: %s", scriptPart)
		}
	}
	// Every inline script the page carries has to be allowed by its own hash,
	// or the UI flashes white on every load with the console full of CSP
	// errors.
	//
	// The invariant is "each inline script present is allowed", not "at least
	// one hash exists". Those differ: a build that has not run `make ui` embeds
	// a placeholder index.html with no inline script at all, and demanding a
	// hash there would fail a build that is behaving perfectly correctly --
	// which is exactly what it did in CI, where the Go job builds with the
	// placeholder on purpose.
	index, err := webdist.ReadFile("webdist/index.html")
	if err != nil {
		t.Fatalf("reading the embedded index.html: %v", err)
	}
	hashes := inlineScriptHashes(index)
	for _, want := range hashes {
		if !strings.Contains(csp, want) {
			t.Errorf("the CSP does not allow the page's inline script %s, so it would be blocked: %s", want, csp)
		}
	}
	if len(hashes) == 0 {
		t.Log("the embedded UI is the placeholder, so there is no inline script to allow; " +
			"run `make ui` to exercise the hashed-script path")
	}
	// The App manifest is a real form that posts to GitHub, and a form-action
	// of 'self' alone makes the browser refuse it -- silently, as a new tab
	// that opens on nothing. This is the directive that lets the connect flow
	// leave the page.
	if !strings.Contains(csp, "form-action 'self' https://github.com") {
		t.Errorf("the CSP does not let a form post to github.com, so the App manifest flow cannot work: %s", csp)
	}
	if got := resp.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := resp.header.Get("Referrer-Policy"); got == "" {
		t.Error("no Referrer-Policy")
	}
	// This request is plain HTTP, so HSTS would be a promise the connection
	// cannot keep.
	if got := resp.header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS on a plain HTTP response: %q", got)
	}
}

func TestInlineScriptHashesMatchTheEmbeddedPage(t *testing.T) {
	h, err := newSPAHandler()
	if err != nil {
		t.Fatalf("newSPAHandler: %v", err)
	}
	// The placeholder page has no inline script; the built one does. Either
	// way, every inline script in the page must be in the policy.
	found := inlineScriptHashes(h.index)
	if len(found) != len(h.inlineScriptHashes()) {
		t.Fatalf("hash count = %d, want %d", len(h.inlineScriptHashes()), len(found))
	}
	if strings.Contains(string(h.index), "localStorage") && len(found) == 0 {
		t.Error("the page has an inline script but no hash was computed for it")
	}
}

// TestMetricsNeedsAViewer covers the default and the escape hatch, both of
// which are security decisions: job and repository names are in the label set.
func TestMetricsNeedsAViewer(t *testing.T) {
	h := newHarness(t)

	anon := h.do(request{method: http.MethodGet, path: "/metrics"})
	anon.mustStatus(t, http.StatusUnauthorized, "metrics without a credential")

	u, _ := h.user("viewer", store.RoleViewer)
	withViewer := h.do(request{method: http.MethodGet, path: "/metrics", cookie: h.session(u)})
	withViewer.mustStatus(t, http.StatusOK, "metrics as a viewer")
	if !strings.Contains(string(withViewer.body), "# HELP") {
		t.Errorf("that does not look like Prometheus text format: %s", truncate(withViewer.body))
	}
}

func TestMetricsPublicIsOpen(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Metrics.Public = true })
	resp := h.do(request{method: http.MethodGet, path: "/metrics"})
	resp.mustStatus(t, http.StatusOK, "public metrics")
}

func TestMetricsCanBeDisabled(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Metrics.Enabled = false })
	resp := h.do(request{method: http.MethodGet, path: "/metrics"})
	// With metrics off the path is not a route at all, so it falls through to
	// the SPA like any other unknown path.
	if resp.status == http.StatusOK && strings.Contains(string(resp.body), "# HELP") {
		t.Fatal("metrics are served despite being disabled")
	}
}

func TestHealthAndReadiness(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp := h.do(request{method: http.MethodGet, path: path})
		resp.mustStatus(t, http.StatusOK, path)
		if resp.json(t)["ok"] != true {
			t.Errorf("%s did not report ok: %s", path, truncate(resp.body))
		}
	}
}

// TestOpenAPIIsServed checks that a client can fetch the contract for the build
// it is talking to.
func TestOpenAPIIsServed(t *testing.T) {
	h := newHarness(t)
	resp := h.do(request{method: http.MethodGet, path: "/api/openapi.yaml"})
	resp.mustStatus(t, http.StatusOK, "openapi.yaml")
	if !strings.HasPrefix(string(resp.body), "openapi: 3.1.0") {
		t.Fatalf("that is not the OpenAPI document: %s", truncate(resp.body))
	}
	if ct := resp.header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestEmbeddedSpecMatchesTheSource is the drift check.
//
// The embed directive cannot reach api/openapi.yaml from this package, so the copy in
// openapi_spec.go is generated. This test is what stops the two from silently
// disagreeing about what the API promises.
func TestEmbeddedSpecMatchesTheSource(t *testing.T) {
	source, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Skipf("api/openapi.yaml is not readable from here: %v", err)
	}
	embedded, err := openapiSpec()
	if err != nil {
		t.Fatalf("openapiSpec: %v", err)
	}
	if string(embedded) != string(source) {
		t.Fatal("the embedded OpenAPI document is out of date; " +
			"run `go run internal/api/gen_openapi.go` from the repository root and commit the result")
	}
}

// TestWebhookPathIsMounted checks that GitHub's deliveries reach the
// controller's own handler, signature check and all.
func TestWebhookPathIsMounted(t *testing.T) {
	h := newHarness(t)

	unsigned := h.do(request{method: http.MethodPost, path: h.cfg.GitHub.WebhookPath,
		headers: map[string]string{"X-GitHub-Event": "ping"}, body: map[string]any{"zen": "hi"}})
	if unsigned.status != http.StatusUnauthorized {
		t.Fatalf("an unsigned delivery answered %d, want 401", unsigned.status)
	}

	// It is recorded either way, which is what makes a misconfigured secret
	// visible instead of silent.
	deliveries, err := h.st.ListDeliveries(h.ctx, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) == 0 {
		t.Fatal("the refused delivery was not recorded")
	}
}

func TestRequestIDIsReturned(t *testing.T) {
	h := newHarness(t)
	resp := h.do(request{method: http.MethodGet, path: "/healthz"})
	if resp.header.Get("X-Request-ID") == "" {
		t.Fatal("no X-Request-ID on the response")
	}
	given := h.do(request{method: http.MethodGet, path: "/healthz",
		headers: map[string]string{"X-Request-ID": "from-the-proxy"}})
	if got := given.header.Get("X-Request-ID"); got != "from-the-proxy" {
		t.Errorf("X-Request-ID = %q, want the one the client sent", got)
	}
}

// TestClientIPIgnoresUntrustedForwardedFor is the property that keeps the login
// rate limiter and the audit log honest.
func TestClientIPIgnoresUntrustedForwardedFor(t *testing.T) {
	h := newHarness(t)
	h.user("alice", store.RoleViewer)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		headers: map[string]string{"X-Forwarded-For": "203.0.113.9"},
		body:    map[string]any{"username": "alice", "password": testPassword}})
	resp.mustStatus(t, http.StatusOK, "login")

	events, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"auth.login"}}, store.Page{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the login was not audited")
	}
	if events[0].IP == "203.0.113.9" {
		t.Fatal("an untrusted X-Forwarded-For was believed; a client can forge its own address")
	}
	if !strings.HasPrefix(events[0].IP, "127.0.0.1") {
		t.Errorf("recorded address = %q, want the socket's", events[0].IP)
	}
}

func TestTrustedProxyIsBelieved(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.TrustedProxies = []string{"127.0.0.0/8", "::1/128"}
	})
	h.user("alice", store.RoleViewer)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		headers: map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1"},
		body:    map[string]any{"username": "alice", "password": testPassword}})
	resp.mustStatus(t, http.StatusOK, "login")

	events, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"auth.login"}}, store.Page{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) == 0 || events[0].IP != "10.0.0.1" {
		t.Fatalf("recorded address = %q, want the right-most untrusted entry", events[0].IP)
	}
}

func TestUnknownJSONFieldIsRefused(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)

	body := poolBody(inst.ID)
	body["max_runner"] = 8 // a typo for max_runners

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: h.session(u), body: body})
	resp.mustStatus(t, http.StatusBadRequest, "a typo in a field name")
	if !strings.Contains(resp.errorMessage(t), "max_runner") {
		t.Errorf("the message does not name the unknown field: %q", resp.errorMessage(t))
	}
}

// Behind Cloudflare the origin sees Cloudflare's address on every connection.
// Without honouring its header the audit log records Cloudflare for every
// action and the login rate limiter throttles the whole internet as one client.
func TestCloudflareConnectingIPIsBelievedFromATrustedProxy(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.TrustedProxies = []string{"127.0.0.0/8", "::1/128"}
	})
	h.user("alice", store.RoleViewer)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		headers: map[string]string{
			"CF-Connecting-IP": "198.51.100.7",
			// What the client sent, with Cloudflare's value appended. The
			// unambiguous header wins.
			"X-Forwarded-For": "203.0.113.1, 198.51.100.7",
		},
		body: map[string]any{"username": "alice", "password": testPassword}})
	resp.mustStatus(t, http.StatusOK, "login")

	events, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"auth.login"}}, store.Page{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the login was not audited")
	}
	if events[0].IP != "198.51.100.7" {
		t.Errorf("recorded address = %q, want the client's 198.51.100.7", events[0].IP)
	}
}

// The same header from a peer that is not a trusted proxy must be ignored, or
// anyone can pick their own address and with it defeat the rate limiter and
// forge the address in every audit row they cause.
func TestCloudflareConnectingIPIsIgnoredFromAnUntrustedPeer(t *testing.T) {
	h := newHarness(t)
	h.user("alice", store.RoleViewer)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/auth/login",
		headers: map[string]string{"CF-Connecting-IP": "198.51.100.7"},
		body:    map[string]any{"username": "alice", "password": testPassword}})
	resp.mustStatus(t, http.StatusOK, "login")

	events, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"auth.login"}}, store.Page{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the login was not audited")
	}
	if events[0].IP == "198.51.100.7" {
		t.Fatal("an untrusted client's CF-Connecting-IP was believed")
	}
}

// A controller configured against GitHub Enterprise Server posts its manifests
// there, so that origin has to be in the policy too -- and only that origin,
// derived from the API base, not a wildcard that would let an injected form
// post anywhere.
func TestSecurityHeadersLetTheManifestFormReachEnterpriseServer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.GitHub.APIBaseURL = "https://ghes.example.com/api/v3/"
	})
	csp := h.do(request{method: http.MethodGet, path: "/"}).header.Get("Content-Security-Policy")

	if !strings.Contains(csp, "form-action 'self' https://github.com https://ghes.example.com;") {
		t.Errorf("the CSP does not name the Enterprise host the manifest posts to: %s", csp)
	}
	if strings.Contains(csp, "https:;") || strings.Contains(csp, "form-action 'self' https: ") {
		t.Errorf("form-action must name origins, not a scheme: %s", csp)
	}
}
