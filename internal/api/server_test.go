package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// TestWriteTimeoutStaysZero is a regression test for the setting that would
// break live streaming if anybody tidied it up.
func TestWriteTimeoutStaysZero(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.ReadTimeout = 30 * time.Second
		c.Server.WriteTimeout = 45 * time.Second // even when the config asks
		c.Server.IdleTimeout = 90 * time.Second
	})

	srv := h.api.httpServer(context.Background())
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s; a non-zero one cuts off every SSE stream and log tail", srv.WriteTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %s, want the configured 30s", srv.ReadTimeout)
	}
	if srv.IdleTimeout != 90*time.Second {
		t.Errorf("IdleTimeout = %s, want the configured 90s", srv.IdleTimeout)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is unset, so a slow-loris client can hold a connection open")
	}
	if srv.BaseContext == nil {
		t.Error("no BaseContext, so shutdown cannot end an open stream")
	}
}

// TestListenAndServeStartsAndStops covers the lifecycle a service manager sees.
func TestListenAndServeStartsAndStops(t *testing.T) {
	addr := freeAddr(t)
	h := newHarness(t, func(c *config.Config) { c.Server.Bind = addr })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.api.ListenAndServe(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("healthz = %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v on a clean shutdown", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ListenAndServe did not return after its context was cancelled")
	}

	if _, err := http.Get(base + "/healthz"); err == nil {
		t.Fatal("the listener is still accepting connections after shutdown")
	}
}

// TestListenAndServeWithSelfSignedTLS covers certificate generation, its
// persistence, and that the listener actually serves TLS with it.
func TestListenAndServeWithSelfSignedTLS(t *testing.T) {
	dir := t.TempDir()
	addr := freeAddr(t)
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	h := newHarness(t, func(c *config.Config) {
		c.Server.Bind = addr
		c.Server.TLS = config.TLS{
			Mode: config.TLSSelfSigned, CertFile: certFile, KeyFile: keyFile,
			Hosts: []string{"zoomies.test"},
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.api.ListenAndServe(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(certFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	pem, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("no certificate was generated: %v", err)
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("no key was generated: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the private key is mode %o, want 600", perm)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the generated certificate is not usable as a CA, so an agent could not pin it")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: pool, ServerName: "zoomies.test",
		MinVersion: tls.VersionTLS12,
	}}, Timeout: 5 * time.Second}

	// The certificate names zoomies.test, so the dial goes to the listener's
	// address while the handshake verifies that name.
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	waitForTLS(t, client, "https://zoomies.test/healthz")

	resp, err := client.Get("https://zoomies.test/healthz")
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz over TLS = %d", resp.StatusCode)
	}
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS header on a TLS connection")
	}

	// A second server reuses the stored certificate rather than minting a new
	// one, which is what makes pinning it on an agent worth doing.
	before, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("reading the certificate: %v", err)
	}
	cert, err := h.api.selfSignedCertificate()
	if err != nil {
		t.Fatalf("selfSignedCertificate: %v", err)
	}
	after, _ := os.ReadFile(certFile)
	if string(before) != string(after) {
		t.Error("the certificate was regenerated; an agent pinning it would have to be reconfigured on every restart")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the certificate: %v", err)
	}
	if !leaf.IsCA {
		t.Error("the certificate is not self-issued as a CA, so agent.ca_file cannot pin it")
	}
	for _, want := range []string{"zoomies.test", "localhost"} {
		if !containsString(leaf.DNSNames, want) {
			t.Errorf("the certificate does not name %q: %v", want, leaf.DNSNames)
		}
	}

	cancel()
	<-done
}

func TestTLSFilesModeNeedsBothFiles(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.TLS = config.TLS{Mode: config.TLSFiles, CertFile: "/nowhere/cert.pem"}
	})
	err := h.api.ListenAndServe(context.Background())
	if err == nil {
		t.Fatal("a half-configured TLS mode was accepted")
	}
	if !strings.Contains(err.Error(), "key_file") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestUnknownTLSModeIsRefused(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.TLS = config.TLS{Mode: config.TLSMode("acme")}
	})
	err := h.api.ListenAndServe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "off, self-signed or files") {
		t.Fatalf("error = %v, want one that lists the modes", err)
	}
}

func TestTrustedProxyParsing(t *testing.T) {
	if _, err := parseTrustedProxies([]string{"10.0.0.0/8", "192.0.2.7", "::1"}); err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	_, err := parseTrustedProxies([]string{"the load balancer"})
	if err == nil {
		t.Fatal("a nonsense CIDR was accepted")
	}
	if !strings.Contains(err.Error(), "trusted_proxies") {
		t.Errorf("the error does not name the setting: %v", err)
	}
}

// The cloudflare token must expand to Cloudflare's published ranges, so a
// connection from any edge address is trusted without copying CIDRs about.
func TestTrustedProxyCloudflareTokenExpandsToThePublishedRanges(t *testing.T) {
	nets, err := parseTrustedProxies([]string{"cloudflare"})
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	if len(nets) != len(config.CloudflareCIDRs) {
		t.Fatalf("expanded to %d networks, want the %d published ranges", len(nets), len(config.CloudflareCIDRs))
	}
	for _, addr := range []string{"104.16.0.1", "162.158.5.5", "2606:4700::1"} {
		ip := net.ParseIP(addr)
		if !slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(ip) }) {
			t.Errorf("Cloudflare edge address %s is not trusted by the token", addr)
		}
	}
	// And an address outside the ranges stays untrusted, or the token would
	// open the trust boundary wider than Cloudflare.
	if ip := net.ParseIP("192.0.2.7"); slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(ip) }) {
		t.Error("a non-Cloudflare address is trusted by the token")
	}
}

// freeAddr returns a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("closing the probe listener: %v", err)
	}
	return addr
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the server never came up at %s", url)
}

func waitForTLS(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the TLS server never came up at %s: %v", url, last)
}

func containsString(in []string, want string) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}

// TestMountPathCollisionsAreRefused turns a configuration mistake into a
// message rather than a panic from the router.
func TestMountPathCollisionsAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*config.Config)
		says  string
	}{
		{"webhook over the API", func(c *config.Config) { c.GitHub.WebhookPath = "/api/v1/pools" }, "github.webhook_path"},
		{"webhook at the root", func(c *config.Config) { c.GitHub.WebhookPath = "/" }, "the UI"},
		{"metrics over health", func(c *config.Config) { c.Metrics.Path = "/healthz" }, "metrics.path"},
		{"metrics over the webhook", func(c *config.Config) { c.Metrics.Path = "/webhooks/github" }, "metrics.path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.apply(cfg)
			if err := checkMountPaths(cfg); err == nil {
				t.Fatal("the collision was accepted")
			} else if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the error does not name %q: %v", tc.says, err)
			}
		})
	}
	if err := checkMountPaths(config.Default()); err != nil {
		t.Fatalf("the default configuration was refused: %v", err)
	}
}
