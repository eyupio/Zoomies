package installer

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// testPlan is a plan pointing entirely at a temporary directory, so that
// config.Validate's file checks look at files this test controls.
func testPlan(t *testing.T) Plan {
	t.Helper()
	dir := t.TempDir()
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	keyFile := filepath.Join(dir, "encryption.key")
	if err := cryptox.WriteKeyFile(keyFile, key); err != nil {
		t.Fatalf("WriteKeyFile: %v", err)
	}
	return Plan{
		Mode:        ModeSingle,
		ConfigDir:   dir,
		StateDir:    dir,
		ConfigFile:  filepath.Join(dir, "zoomies.yaml"),
		KeyFile:     keyFile,
		DBPath:      filepath.Join(dir, "zoomies.db"),
		WorkDir:     filepath.Join(dir, "work"),
		Backend:     store.BackendDocker,
		DockerHost:  "unix:///run/user/1000/docker.sock",
		Rootless:    true,
		Capacity:    4,
		Embedded:    true,
		Listen:      ListenLoopback,
		Bind:        "127.0.0.1:8080",
		TLSMode:     config.TLSOff,
		ExternalURL: "http://localhost:8080",
		AdminUser:   "admin",
	}
}

func findingCodes(fs config.Findings) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestPlanConfigIsValidForEveryListenerChoice(t *testing.T) {
	certDir := t.TempDir()
	cert := writeFile(t, certDir, "cert.pem", "not really a certificate, but it exists")
	key := writeFile(t, certDir, "key.pem", "nor is this")

	cases := []struct {
		name        string
		mutate      func(*Plan)
		wantCodes   []string
		unwantCodes []string
	}{
		{
			name:        "loopback is the quiet default",
			mutate:      func(p *Plan) { ListenLoopback.apply(p, 8080, "build-01") },
			unwantCodes: []string{"bind.public_no_tls", "external_url.missing", "external_url.insecure"},
		},
		{
			name:   "reverse proxy names the plaintext bind",
			mutate: func(p *Plan) { ListenProxy.apply(p, 8080, "zoomies.example.com") },
			// TLS is terminated by the proxy, so the listener really is plain
			// HTTP and the operator is told so.
			wantCodes:   []string{"bind.public_no_tls"},
			unwantCodes: []string{"external_url.insecure"},
		},
		{
			name:   "cloudflare trusts its published ranges",
			mutate: func(p *Plan) { ListenCloudflare.apply(p, 8080, "zoomies.example.com") },
			// The same plain-HTTP story as any proxy, but the proxy question
			// is answered already, so no untrusted-proxy finding.
			wantCodes:   []string{"bind.public_no_tls"},
			unwantCodes: []string{"proxy.untrusted", "proxy.bad_cidr", "external_url.insecure"},
		},
		{
			name:        "self-signed says GitHub will refuse it",
			mutate:      func(p *Plan) { ListenSelfSigned.apply(p, 8443, "zoomies.example.com") },
			wantCodes:   []string{"tls.self_signed"},
			unwantCodes: []string{"bind.public_no_tls"},
		},
		{
			name: "a real certificate is warning-free",
			mutate: func(p *Plan) {
				ListenTLSFiles.apply(p, 8443, "zoomies.example.com")
				p.TLSCertFile, p.TLSKeyFile = cert, key
			},
			unwantCodes: []string{"bind.public_no_tls", "tls.files_missing", "tls.file_unreadable", "tls.self_signed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testPlan(t)
			tc.mutate(&p)
			cfg := p.Config()

			findings := cfg.Validate()
			if err := findings.Err(); err != nil {
				t.Fatalf("the installer must never produce a configuration that refuses to start:\n%v", err)
			}
			codes := findingCodes(findings)
			for _, want := range tc.wantCodes {
				if !contains(codes, want) {
					t.Errorf("expected finding %q, got %v", want, codes)
				}
			}
			for _, unwanted := range tc.unwantCodes {
				if contains(codes, unwanted) {
					t.Errorf("did not expect finding %q, got %v", unwanted, codes)
				}
			}
		})
	}
}

func TestPlanConfigKeepsTheKeyOutOfTheConfigFile(t *testing.T) {
	p := testPlan(t)
	cfg := p.Config()
	if cfg.Security.EncryptionKey != "" {
		t.Fatal("the key itself must never be written into zoomies.yaml")
	}
	if cfg.Security.EncryptionKeyFile != p.KeyFile {
		t.Fatalf("key file = %q, want %q", cfg.Security.EncryptionKeyFile, p.KeyFile)
	}
	if !cfg.CookieSecureValue() && strings.HasPrefix(cfg.Server.ExternalURL, "https://") {
		t.Fatal("an https external URL must set secure cookies")
	}
}

func TestPlanConfigMissingCertificateIsAnError(t *testing.T) {
	p := testPlan(t)
	ListenTLSFiles.apply(&p, 8443, "zoomies.example.com")
	p.TLSCertFile, p.TLSKeyFile = "", ""

	if err := p.Config().Validate().Err(); err == nil {
		t.Fatal("tls.mode=files without files must be a fatal finding")
	}
}

func TestDefaultExternalURL(t *testing.T) {
	cases := []struct {
		name   string
		choice ListenChoice
		bind   string
		mode   config.TLSMode
		host   string
		want   string
	}{
		{"loopback", ListenLoopback, "127.0.0.1:8080", config.TLSOff, "build-01", "http://localhost:8080"},
		{"loopback on 80", ListenLoopback, "127.0.0.1:80", config.TLSOff, "build-01", "http://localhost"},
		{"tls files", ListenTLSFiles, "0.0.0.0:8443", config.TLSFiles, "zoomies.example.com", "https://zoomies.example.com:8443"},
		{"tls on 443", ListenTLSFiles, "0.0.0.0:443", config.TLSFiles, "zoomies.example.com", "https://zoomies.example.com"},
		{"behind a proxy", ListenProxy, "0.0.0.0:8080", config.TLSOff, "zoomies.example.com", "https://zoomies.example.com"},
		{"behind cloudflare", ListenCloudflare, "0.0.0.0:8080", config.TLSOff, "zoomies.example.com", "https://zoomies.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultExternalURL(tc.choice, tc.bind, tc.mode, tc.host); got != tc.want {
				t.Fatalf("defaultExternalURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListenChoiceFor(t *testing.T) {
	cases := []struct {
		bind    string
		mode    config.TLSMode
		trusted []string
		want    ListenChoice
	}{
		{"127.0.0.1:8080", config.TLSOff, nil, ListenLoopback},
		{"[::1]:8080", config.TLSOff, nil, ListenLoopback},
		{"0.0.0.0:8080", config.TLSOff, nil, ListenProxy},
		{"0.0.0.0:8080", config.TLSOff, []string{"cloudflare"}, ListenCloudflare},
		{"0.0.0.0:8443", config.TLSFiles, nil, ListenTLSFiles},
		{"0.0.0.0:8443", config.TLSSelfSigned, nil, ListenSelfSigned},
	}
	for _, tc := range cases {
		if got := listenChoiceFor(tc.bind, tc.mode, tc.trusted); got != tc.want {
			t.Errorf("listenChoiceFor(%q, %q, %v) = %q, want %q", tc.bind, tc.mode, tc.trusted, got, tc.want)
		}
	}
}

// The Cloudflare choice must answer the proxy question itself, or choosing it
// quietly leaves every audit row recording Cloudflare.
func TestCloudflareChoiceTrustsCloudflare(t *testing.T) {
	p := testPlan(t)
	ListenCloudflare.apply(&p, 8080, "zoomies.example.com")
	if !slices.Contains(p.TrustedProxies, config.TrustedProxyCloudflare) {
		t.Fatal("the Cloudflare choice did not trust the cloudflare token")
	}
	if p.Bind != "0.0.0.0:8080" || p.TLSMode != config.TLSOff {
		t.Fatalf("bind %s TLS %s, want 0.0.0.0:8080 with TLS off", p.Bind, p.TLSMode)
	}
}

func TestValidateCIDRListAcceptsTheCloudflareToken(t *testing.T) {
	if err := validateCIDRList("cloudflare, 10.0.0.0/8"); err != nil {
		t.Fatalf("the token and a CIDR must both pass: %v", err)
	}
	if err := validateCIDRList("the load balancer"); err == nil {
		t.Fatal("a nonsense entry was accepted")
	}
}

func TestSuggestPool(t *testing.T) {
	cases := []struct {
		os, arch string
		backend  store.BackendKind
		capacity int
		wantName string
		wantCmd  string
	}{
		{"linux", "amd64", store.BackendDocker, 4, "zoomies-linux-x64",
			"zoomies pools create --name zoomies-linux-x64 --labels zoomies,zoomies-linux-x64 --backend docker --max 4"},
		{"linux", "arm64", store.BackendPodman, 2, "zoomies-linux-arm64",
			"zoomies pools create --name zoomies-linux-arm64 --labels zoomies,zoomies-linux-arm64 --backend podman --max 2"},
		{"linux", "amd64", store.BackendProcess, 1, "zoomies-linux-x64-host",
			"zoomies pools create --name zoomies-linux-x64-host --labels zoomies,zoomies-linux-x64-host --backend process --max 1"},
		{"darwin", "arm64", store.BackendDocker, 8, "zoomies-macos-arm64",
			"zoomies pools create --name zoomies-macos-arm64 --labels zoomies,zoomies-macos-arm64 --backend docker --max 8"},
		{"linux", "amd64", store.BackendDocker, 0, "zoomies-linux-x64",
			"zoomies pools create --name zoomies-linux-x64 --labels zoomies,zoomies-linux-x64 --backend docker --max 1"},
	}
	for _, tc := range cases {
		got := SuggestPool(tc.os, tc.arch, tc.backend, tc.capacity)
		if got.Name != tc.wantName {
			t.Errorf("SuggestPool(%s/%s, %s).Name = %q, want %q", tc.os, tc.arch, tc.backend, got.Name, tc.wantName)
		}
		if cmd := got.Command(); cmd != tc.wantCmd {
			t.Errorf("Command() = %q\nwant %q", cmd, tc.wantCmd)
		}
		// The line the installer prints is the one a workflow copies, so it is
		// the branded label on its own rather than a list to decode.
		if runsOn := got.RunsOn(); runsOn != tc.wantName {
			t.Errorf("RunsOn() = %q, want %q", runsOn, tc.wantName)
		}
	}
}

func TestBackendChoicesPreferRootless(t *testing.T) {
	det := Detection{
		Docker: RuntimeInfo{Kind: store.BackendDocker, Available: true, Rootless: false, Endpoint: "unix:///var/run/docker.sock", Installed: true},
		Podman: RuntimeInfo{Kind: store.BackendPodman, Available: true, Rootless: true, Endpoint: "unix:///run/user/1000/podman/podman.sock", Installed: true},
	}
	choices := backendChoices(det)
	if len(choices) < 3 {
		t.Fatalf("want rootless podman, root docker and process; got %d", len(choices))
	}
	if choices[0].Kind != store.BackendPodman || !choices[0].Rootless {
		t.Fatalf("rootless must come first, got %+v", choices[0])
	}
	if choices[1].Kind != store.BackendDocker || choices[1].Warning == "" {
		t.Fatalf("a root daemon must be offered with what it costs, got %+v", choices[1])
	}
	last := choices[len(choices)-1]
	if last.Kind != store.BackendProcess || last.Warning == "" {
		t.Fatalf("the process backend must be offered last, with its warning: %+v", last)
	}
}

func TestBackendChoicesNameWhatToStart(t *testing.T) {
	det := Detection{
		Docker: RuntimeInfo{Kind: store.BackendDocker, Installed: true, Detail: "no socket at /var/run/docker.sock"},
	}
	choices := backendChoices(det)
	var unavailable *BackendChoice
	for i := range choices {
		if !choices[i].Available {
			unavailable = &choices[i]
		}
	}
	if unavailable == nil {
		t.Fatal("an installed-but-dead runtime should still be listed, with how to start it")
	}
	if unavailable.Fix == "" {
		t.Fatalf("the choice must say what to start: %+v", unavailable)
	}
	if choices[len(choices)-1].Kind != store.BackendProcess {
		t.Fatal("the process backend must still be offered when no runtime answered")
	}
}

func TestApplyAnswersOverlaysOnlyWhatIsMentioned(t *testing.T) {
	base := testPlan(t)
	a := &Answers{Backend: "process", Capacity: 9, ExternalURL: "https://zoomies.example.com"}
	a.Admin.Username = "ada"
	a.Admin.Password = "correct-horse-battery"

	got, err := applyAnswers(base, a)
	if err != nil {
		t.Fatalf("applyAnswers: %v", err)
	}
	if got.Backend != store.BackendProcess || got.Capacity != 9 {
		t.Fatalf("named keys not applied: %+v", got)
	}
	if got.ExternalURL != "https://zoomies.example.com" || got.AdminUser != "ada" || got.adminPassword == "" {
		t.Fatalf("admin and URL not applied: %+v", got)
	}
	if got.Bind != base.Bind || got.KeyFile != base.KeyFile {
		t.Fatal("keys the file did not mention must keep their detected values")
	}
	if len(got.Missing()) != 0 {
		t.Fatalf("nothing should be missing now: %v", keysOf(got.Missing()))
	}
}

func TestApplyAnswersRejectsNonsense(t *testing.T) {
	base := testPlan(t)
	for name, a := range map[string]*Answers{
		"backend":  {Backend: "kubernetes"},
		"tls mode": {TLS: AnswersTLS{Mode: "maybe"}},
		"bind":     {Bind: "8080"},
	} {
		if _, err := applyAnswers(base, a); err == nil {
			t.Errorf("%s: want an error naming the bad value", name)
		}
	}
}

func TestPlanMissingSkipsAdminWhenTheDatabaseExists(t *testing.T) {
	p := testPlan(t)
	p.AdminUser, p.adminPassword = "", ""
	if len(p.Missing()) == 0 {
		t.Fatal("a fresh install must insist on an administrator")
	}
	if err := os.WriteFile(p.DBPath, []byte("not really sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.Missing(); len(got) != 0 {
		t.Fatalf("an existing database already has its accounts; got %v", keysOf(got))
	}
}

func TestParseMode(t *testing.T) {
	for _, in := range []string{"single", "controller", "agent", "SINGLE", " agent "} {
		if _, err := ParseMode(in); err != nil {
			t.Errorf("ParseMode(%q): %v", in, err)
		}
	}
	if _, err := ParseMode("cluster"); err == nil {
		t.Error("want an error naming the alternatives")
	}
	if m, err := ParseMode(""); err != nil || m != "" {
		t.Errorf("an empty mode means \"ask\", got %q %v", m, err)
	}
}

func TestNewRejectsABadAnswerFileBeforeTouchingTheHost(t *testing.T) {
	_, err := New(Options{AnswersFile: filepath.Join(t.TempDir(), "missing.yaml"), NonInteractive: true, Out: os.Stdout})
	if err == nil {
		t.Fatal("a missing answer file must fail before anything is installed")
	}
}

func TestWaitHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := waitHealthy(context.Background(), srv.Client(), srv.URL+"/healthz", 2*time.Second); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
}

func TestWaitHealthyGivesUpWithTheReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := waitHealthy(context.Background(), srv.Client(), srv.URL+"/healthz", 50*time.Millisecond)
	if err == nil {
		t.Fatal("want an error when the controller never becomes healthy")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("the error should carry what actually happened, got: %v", err)
	}
}

func TestLocalHealthURL(t *testing.T) {
	p := testPlan(t)
	if got := localHealthURL(p); got != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("localHealthURL = %q", got)
	}
	ListenSelfSigned.apply(&p, 8443, "zoomies.example.com")
	if got := localHealthURL(p); got != "https://127.0.0.1:8443/healthz" {
		t.Fatalf("localHealthURL with TLS = %q", got)
	}
}

func TestInsecureLoopbackTransportRefusesOffHost(t *testing.T) {
	tr := insecureLoopbackTransport()
	_, err := tr.DialContext(context.Background(), "tcp", "198.51.100.7:443")
	if err == nil {
		t.Fatal("skipping verification must be confined to loopback")
	}
}

func TestSplitBind(t *testing.T) {
	if host, port, err := splitBind("0.0.0.0:8080"); err != nil || host != "0.0.0.0" || port != 8080 {
		t.Fatalf("splitBind = %q %d %v", host, port, err)
	}
	if _, _, err := splitBind("8080"); err == nil {
		t.Fatal("want an error for a bind with no host")
	}
}

func TestDefaultAppName(t *testing.T) {
	if got := defaultAppName("acme"); got != "zoomies-acme" {
		t.Fatalf("defaultAppName = %q", got)
	}
	if got := defaultAppName("acme/widgets"); got != "zoomies-acme-widgets" {
		t.Fatalf("defaultAppName = %q", got)
	}
	long := defaultAppName(strings.Repeat("verylongorgname", 4))
	if len(long) > 34 {
		t.Fatalf("GitHub allows 34 characters, got %d (%q)", len(long), long)
	}
}

func TestUIWritesPlainTextToANonTerminal(t *testing.T) {
	var buf strings.Builder
	u := newUI(&buf)
	u.step("Looking around")
	u.ok("did the thing")
	u.warn("this is dangerous")
	u.note("detail")

	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("no escape codes when the output is not a terminal:\n%q", out)
	}
	for _, want := range []string{"-> Looking around", "ok did the thing", "!! this is dangerous", "detail"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDefaultServiceUser(t *testing.T) {
	if u, g := defaultServiceUser(Detection{OS: "linux", Root: true, User: "root"}); u != "zoomies" || g != "zoomies" {
		t.Fatalf("a root Linux install gets its own account, got %q/%q", u, g)
	}
	if u, _ := defaultServiceUser(Detection{OS: "darwin", Root: false, User: "ada"}); u != "ada" {
		t.Fatalf("macOS installs run as the operator, got %q", u)
	}
	if u, _ := defaultServiceUser(Detection{OS: "linux", Root: false, User: "ada"}); u != "ada" {
		t.Fatalf("a non-root install cannot create an account, got %q", u)
	}
}

// A free port is a moving target, so this checks the two things that must
// always hold rather than a specific number.
func TestPortChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parsing %q: %v", portStr, err)
	}

	if PortFree("127.0.0.1", port) {
		t.Fatalf("port %d is in use by this test", port)
	}
	if free, err := BindFree(net.JoinHostPort("127.0.0.1", portStr)); err != nil || free {
		t.Fatalf("BindFree = %v, %v; want false", free, err)
	}
	if _, err := BindFree("nonsense"); err == nil {
		t.Fatal("BindFree must reject an address that is not host:port")
	}

	next, ok := NextFreePort("127.0.0.1", port, 50)
	if !ok {
		t.Fatal("there should be a free port somewhere above this one")
	}
	if next == port {
		t.Fatalf("NextFreePort returned the busy port %d", port)
	}
}
