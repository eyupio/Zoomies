package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The web UI does not show bind.public_no_tls, because a fleet behind
// Cloudflare or any other TLS-terminating proxy is configured correctly and
// would carry the warning forever. The CLI still says it.
func TestForUIHidesTheNoTLSWarningButValidateKeepsIt(t *testing.T) {
	c := Default()
	c.Server.Bind = "0.0.0.0:8080"
	c.Server.TLS.Mode = TLSOff

	all := c.Validate()
	if !hasCode(all, "bind.public_no_tls") {
		t.Fatal("Validate dropped bind.public_no_tls; the CLI has to keep saying it")
	}
	if hasCode(all.ForUI(), "bind.public_no_tls") {
		t.Error("ForUI kept bind.public_no_tls, which the UI must not show")
	}

	// Everything else survives: this is a suppression list of one, not a
	// filter that quietly swallows whatever it does not recognise.
	for _, f := range all {
		if f.Code == "bind.public_no_tls" {
			continue
		}
		if !hasCode(all.ForUI(), f.Code) {
			t.Errorf("ForUI dropped %q, which the UI still needs", f.Code)
		}
	}
}

// An empty result must still be a usable slice: the panel renders "nothing
// needs your attention" from a zero-length list, not from nil.
func TestForUIReturnsEmptyNotNil(t *testing.T) {
	if got := (Findings{}).ForUI(); got == nil || len(got) != 0 {
		t.Fatalf("ForUI() = %#v, want an empty non-nil Findings", got)
	}
}

// The token stands for Cloudflare's published ranges, so it must validate as
// cleanly as the CIDRs it replaces.
func TestTrustedProxiesAcceptsTheCloudflareToken(t *testing.T) {
	c := Default()
	c.Server.TrustedProxies = []string{"cloudflare", "10.0.0.0/8"}
	for _, f := range c.Validate() {
		if f.Code == "proxy.bad_cidr" {
			t.Fatalf("the cloudflare token was treated as a bad CIDR: %v", f)
		}
	}
}

// The embedded ranges are a trust boundary written by hand, so the test
// re-checks every one of them parses.
func TestCloudflareCIDRsAllParse(t *testing.T) {
	if len(CloudflareCIDRs) == 0 {
		t.Fatal("no embedded Cloudflare ranges")
	}
	for _, cidr := range CloudflareCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			t.Errorf("embedded range %q does not parse: %v", cidr, err)
		}
	}
}

func TestExpandTrustedProxiesReplacesTheTokenAndKeepsTheRest(t *testing.T) {
	got := ExpandTrustedProxies([]string{"10.0.0.0/8", "cloudflare"})
	if len(got) != 1+len(CloudflareCIDRs) {
		t.Fatalf("expanded to %d entries, want the CIDR plus %d ranges", len(got), len(CloudflareCIDRs))
	}
	if got[0] != "10.0.0.0/8" {
		t.Errorf("the operator's own CIDR must stay in place, got %q", got[0])
	}
	if got[1] != CloudflareCIDRs[0] {
		t.Errorf("the token did not expand in order, got %q", got[1])
	}
}

func hasCode(fs Findings, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}

// agent.allow_insecure_http sat right next to agent.insecure_skip_verify with
// no Finding of its own: the same class of risk (a long-lived agent token and
// every runner's just-in-time credentials on the wire) went unwarned.
func TestAgentWarnsWhenAllowedToUsePlainHTTP(t *testing.T) {
	c := Default()
	c.Agent.ControllerURL = "http://zoomies.example.com"
	if hasCode(c.Validate(), "agent.insecure_http") {
		t.Fatal("agent.insecure_http fired without the setting turned on")
	}

	c.Agent.AllowInsecureHTTP = true
	if !hasCode(c.Validate(), "agent.insecure_http") {
		t.Fatal("agent.insecure_http did not fire for an agent allowed to use plain HTTP")
	}
}

// A finished runner's container is disk the host never gets back until the
// agent deletes it, so the window has to be a real duration, and a window long
// enough to fill a busy host has to be said out loud rather than found out.
func TestFinishedRetentionIsBoundedInBothDirections(t *testing.T) {
	c := Default()
	c.Agent.Embedded = true
	if f := c.Validate(); hasCode(f, "agent.finished_retention") || hasCode(f, "agent.finished_retention_long") {
		t.Fatalf("the default finished retention drew a finding: %+v", f)
	}

	c.Agent.FinishedRetention = -1
	if f := c.Validate(); !hasCode(f, "agent.finished_retention") {
		t.Fatal("a negative agent.finished_retention passed validation")
	}

	c.Agent.FinishedRetention = maxQuietFinishedRetention + 1
	f := c.Validate()
	if hasCode(f, "agent.finished_retention") {
		t.Fatal("a long agent.finished_retention was reported as an error; it is a warning, not a reason to refuse to start")
	}
	if !hasCode(f, "agent.finished_retention_long") {
		t.Fatal("a day-long agent.finished_retention drew no warning")
	}
}

// The warning is about a key written into zoomies.yaml, where backups and
// configuration management can read it. A key that arrived from the
// environment while a config file merely existed used to trip it too, and its
// fix text told the operator to do what they had already done.
func TestKeyInConfigWarningLooksAtWhereTheKeyCameFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zoomies.yaml")
	if err := os.WriteFile(path, []byte("server:\n  bind: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZOOMIES_ENCRYPTION_KEY", "0000000000000000000000000000000000000000000=")

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if hasCode(c.Validate(), "crypto.key_in_config") {
		t.Fatal("a key from the environment was reported as written in the config file")
	}

	if err := os.WriteFile(path, []byte("security:\n  encryption_key: 0000000000000000000000000000000000000000000=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !hasCode(c.Validate(), "crypto.key_in_config") {
		t.Fatal("a key written in the config file was not warned about")
	}
}

// docs/configuration.md says a bare Enterprise Server hostname is accepted and
// /api/v3 appended. The loader used to leave it alone and the validator then
// refused to start on it.
func TestABareEnterpriseHostnameIsNormalisedNotRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.yaml")
	if err := os.WriteFile(path, []byte("github:\n  api_base_url: ghes.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.APIBaseURL != "https://ghes.example.com/api/v3" {
		t.Fatalf("api_base_url = %q, want https://ghes.example.com/api/v3", c.GitHub.APIBaseURL)
	}
	if hasCode(c.Validate(), "github.api_base_malformed") {
		t.Fatal("a bare hostname the docs accept was refused by the validator")
	}
}

// GHE.com is not an Enterprise Server: its API is the root of its own api.
// host. Normalised like one, it gained /api/v3 and every call 404ed.
func TestAGHEComTenantIsNotGivenTheEnterpriseServerPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.yaml")
	if err := os.WriteFile(path, []byte("github:\n  api_base_url: octocorp.ghe.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.APIBaseURL != "https://api.octocorp.ghe.com" {
		t.Fatalf("api_base_url = %q, want https://api.octocorp.ghe.com", c.GitHub.APIBaseURL)
	}
	if hasCode(c.Validate(), "github.api_base_malformed") {
		t.Fatal("a GHE.com tenant was refused by the validator")
	}
}

// Each of these is a value that quietly weakens the default posture, which is
// exactly what the validator exists to name, and none of them used to draw a
// finding. They are warnings: every one has a legitimate use somewhere, and
// the point is that the operator is told, not stopped.
func TestQuietDangerousValuesAreNamed(t *testing.T) {
	cases := []struct {
		name string
		code string
		set  func(c *Config)
	}{
		{"any origin", "origins.any", func(c *Config) { c.Server.AllowedOrigins = []string{"*"} }},
		{"a plaintext origin on an https controller", "origins.insecure", func(c *Config) {
			c.Server.ExternalURL = "https://zoomies.example.com"
			c.Server.AllowedOrigins = []string{"http://dash.example.com"}
		}},
		{"every address is a trusted proxy", "proxy.trust_everyone", func(c *Config) {
			c.Server.TrustedProxies = []string{"0.0.0.0/0"}
		}},
		{"every IPv6 address is a trusted proxy", "proxy.trust_everyone", func(c *Config) {
			c.Server.TrustedProxies = []string{"::/0"}
		}},
		{"no login rate limit", "auth.no_login_limit", func(c *Config) { c.Security.RateLimitLogins = 0 }},
		{"a heartbeat interval near the lost-host timeout", "agent.heartbeat_interval_long", func(c *Config) {
			c.Agent.HeartbeatInterval = MaxQuietHeartbeatInterval + time.Second
		}},
		{"a plaintext identity provider", "oidc.insecure_issuer", func(c *Config) {
			c.OIDC.Enabled = true
			c.OIDC.Issuer = "http://sso.example.com"
			c.OIDC.ClientID = "zoomies"
			c.Server.ExternalURL = "https://zoomies.example.com"
		}},
		// The two stability toggles: each is what keeps a host from being
		// overwhelmed, and switching either off is a choice worth a line at
		// startup rather than a surprise on the day the fleet falls over.
		{"runners without default limits", "scheduler.default_runner_limits_off", func(c *Config) {
			c.Scheduler.DefaultRunnerLimits = false
		}},
		{"hosts that are never throttled", "scheduler.host_throttling_off", func(c *Config) {
			c.Scheduler.HostThrottling = false
		}},
		// The one warning here that is not about safety. Re-running a job the
		// fleet broke spends the installation's GitHub minutes with nobody
		// asking, so a deployment should read it at startup rather than find
		// it on a bill.
		{"jobs re-run without asking", "scheduler.auto_rerun_on", func(c *Config) {
			c.Scheduler.AutoRerun = true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			if hasCode(c.Validate(), tc.code) {
				t.Fatalf("the default configuration drew %s", tc.code)
			}
			tc.set(c)
			fs := c.Validate()
			if !hasCode(fs, tc.code) {
				t.Fatalf("no %s finding; got %+v", tc.code, fs)
			}
			for _, f := range fs {
				if f.Code == tc.code && f.Severity != SeverityWarning {
					t.Fatalf("%s is %s; it should warn, not refuse to start", tc.code, f.Severity)
				}
			}
		})
	}
}

// A plaintext origin costs nothing extra on a controller that is itself
// plaintext -- bind.public_no_tls already covers that deployment -- and
// nothing at all on loopback, where a Vite dev server lives.
func TestPlaintextOriginsAreOnlyNamedOnAnHTTPSController(t *testing.T) {
	c := Default()
	c.Server.AllowedOrigins = []string{"http://dash.example.com", "http://localhost:5173"}
	if hasCode(c.Validate(), "origins.insecure") {
		t.Fatal("a plaintext origin on a plaintext controller drew origins.insecure")
	}
	c.Server.TLS.Mode = TLSSelfSigned
	named := 0
	for _, f := range c.Validate() {
		if f.Code == "origins.insecure" {
			named++
		}
	}
	if named != 1 {
		t.Fatalf("origins.insecure was raised %d times, want once: the loopback origin is exempt", named)
	}
}

// A proxy range that is merely wide is a choice; only the range that is every
// address is the warning, and a bad CIDR stays the error it always was.
func TestOnlyTheEverythingRangeDrawsTheProxyWarning(t *testing.T) {
	c := Default()
	c.Server.TrustedProxies = []string{"10.0.0.0/8", "192.168.1.5", "cloudflare"}
	if fs := c.Validate(); hasCode(fs, "proxy.trust_everyone") || hasCode(fs, "proxy.bad_cidr") {
		t.Fatalf("an ordinary proxy list drew a finding: %+v", fs)
	}
	c.Server.TrustedProxies = []string{"not-a-cidr"}
	if !hasCode(c.Validate(), "proxy.bad_cidr") {
		t.Fatal("an unparseable proxy entry was accepted")
	}
}

// agent.root is about the process that runs runners. A controller with no
// embedded agent runs none, so a container escape has nothing of its to land
// on; the warning used to fire there anyway, on every root controller in front
// of a fleet of remote agents, and taught operators to ignore it.
func TestTheRootWarningIsAboutTheProcessThatRunsRunners(t *testing.T) {
	old := euid
	euid = func() int { return 0 }
	t.Cleanup(func() { euid = old })

	c := Default()
	c.Agent.Embedded = false
	c.Agent.ControllerURL = ""
	c.Agent.Backend = "docker"
	if hasCode(c.Validate(), "agent.root") {
		t.Fatal("agent.root fired for a controller that runs no agent")
	}

	c.Agent.Embedded = true
	if !hasCode(c.Validate(), "agent.root") {
		t.Fatal("agent.root did not fire for a root agent using docker")
	}

	c.Agent.Embedded = false
	c.Agent.ControllerURL = "https://zoomies.example.com"
	if !hasCode(c.Validate(), "agent.root") {
		t.Fatal("agent.root did not fire for a standalone root agent")
	}
}

// disable_auth on bind: localhost:8080 was refused as a public bind while the
// same word in external_url counted as local, because BindsPublicly assumed
// any name resolves off-host. localhost and the names under .localhost never
// do; RFC 6761 reserves them for this machine.
func TestALocalhostBindIsNotPublic(t *testing.T) {
	for bind, public := range map[string]bool{
		"localhost:8080":           false,
		"LOCALHOST:8080":           false,
		"dev.localhost:8080":       false,
		"127.0.0.1:8080":           false,
		"[::1]:8080":               false,
		"0.0.0.0:8080":             true,
		":8080":                    true,
		"[::]:8080":                true,
		"10.0.0.5:8080":            true,
		"zoomies.example.com:8080": true,
	} {
		c := Default()
		c.Server.Bind = bind
		if got := c.BindsPublicly(); got != public {
			t.Errorf("BindsPublicly(%q) = %v, want %v", bind, got, public)
		}
	}
	c := Default()
	c.Server.ExternalURL = "http://dev.localhost:8080"
	if !c.ExternalURLIsLocal() {
		t.Fatal("a .localhost external URL did not count as local")
	}
	if hasCode(c.Validate(), "external_url.insecure") {
		t.Fatal("plaintext to this machine drew the insecure external URL warning")
	}
}

// The file is read as forgivingly as the environment: tls.mode was lowercased
// from ZOOMIES_TLS_MODE but not from zoomies.yaml, "warning" was a level the
// logger understood and the validator refused, and an empty file was told
// apart from a broken one by the text of the error.
func TestTheFileIsReadAsForgivinglyAsTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.yaml")
	if err := os.WriteFile(path, []byte("server:\n  tls:\n    mode: Self-Signed\nlog:\n  level: Warning\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.TLS.Mode != TLSSelfSigned {
		t.Fatalf("tls.mode = %q, want %q", c.Server.TLS.Mode, TLSSelfSigned)
	}
	if c.Log.Level != "warn" {
		t.Fatalf("log.level = %q, want warn", c.Log.Level)
	}
	if hasCode(c.Validate(), "log.level") {
		t.Fatal("a warning level the logger understands was refused")
	}

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("an empty file did not load as an empty configuration: %v", err)
	}
}

// find returns the finding with this code, or a zero Finding.
func find(fs Findings, code string) Finding {
	for _, f := range fs {
		if f.Code == code {
			return f
		}
	}
	return Finding{}
}

// security.disable_auth turns every request into an administrator, so the guard
// on it has to ask whether anything can reach the listener -- not merely what
// address it is bound to. A loopback bind behind a reverse proxy is the
// deployment this project recommends, and it is reachable by the whole world.
func TestDisableAuthIsRefusedWhereverTheControllerIsReachable(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:   "loopback with nothing in front",
			mutate: func(c *Config) { c.Server.Bind = "127.0.0.1:8080" },
		},
		{
			name:    "bound to every interface",
			mutate:  func(c *Config) { c.Server.Bind = "0.0.0.0:8080" },
			wantErr: true,
		},
		{
			name: "loopback, but an external URL says something forwards to it",
			mutate: func(c *Config) {
				c.Server.Bind = "127.0.0.1:8080"
				c.Server.ExternalURL = "https://zoomies.example.com"
			},
			wantErr: true,
		},
		{
			name: "loopback, but a trusted proxy says something forwards to it",
			mutate: func(c *Config) {
				c.Server.Bind = "127.0.0.1:8080"
				c.Server.TrustedProxies = []string{"10.0.0.0/8"}
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Database.Path = filepath.Join(t.TempDir(), "zoomies.db")
			c.Security.DisableAuth = true
			tc.mutate(c)

			f := find(c.Validate(), "auth.disabled")
			if f.Code == "" {
				t.Fatal("disable_auth produced no finding at all")
			}
			if got := f.Severity == SeverityError; got != tc.wantErr {
				t.Fatalf("severity = %q (fatal=%v); want fatal=%v", f.Severity, got, tc.wantErr)
			}
			if err := c.Validate().Err(); (err != nil) != tc.wantErr {
				t.Fatalf("Err() = %v; want startup to be stopped=%v", err, tc.wantErr)
			}
		})
	}
}

// Zoomies cannot see a proxy terminating TLS in front of it, so a publicly
// bound controller whose cookies lack Secure has to be told about it: one
// plain-HTTP request to that host otherwise hands over a live session.
func TestInsecureCookieOnAPublicBindIsNamed(t *testing.T) {
	c := Default()
	c.Database.Path = filepath.Join(t.TempDir(), "zoomies.db")
	c.Server.Bind = "0.0.0.0:8080"
	c.Server.ExternalURL = ""
	c.normalize()

	if f := find(c.Validate(), "auth.cookie_insecure"); f.Severity != SeverityWarning {
		t.Fatalf("public bind with insecure cookies produced %+v; want a warning", f)
	}

	// An https external URL derives Secure by itself, so the warning goes away.
	c.Security.CookieSecure = nil
	c.Server.ExternalURL = "https://zoomies.example.com"
	c.normalize()
	if f := find(c.Validate(), "auth.cookie_insecure"); f.Code != "" {
		t.Errorf("an https external URL should have turned Secure on: %+v", f)
	}

	// So does saying so outright, which is the reverse-proxy case.
	secure := true
	c.Security.CookieSecure = &secure
	c.Server.ExternalURL = ""
	if f := find(c.Validate(), "auth.cookie_insecure"); f.Code != "" {
		t.Errorf("cookie_secure was set explicitly and still warned: %+v", f)
	}

	// A loopback-only instance is not warned about: there is no network hop.
	c.Security.CookieSecure = nil
	c.Server.Bind = "127.0.0.1:8080"
	c.normalize()
	if f := find(c.Validate(), "auth.cookie_insecure"); f.Code != "" {
		t.Errorf("a loopback bind should not warn about cookie security: %+v", f)
	}
}

// retention.audit never bounded audit rows, which are not pruned; it bounded
// scaling history. The key is renamed to say so, and a file that still uses
// the old name keeps its window and is told once to rename it.
func TestRetentionAuditIsHonouredAsScalingEventsAndFlagged(t *testing.T) {
	c := Default()
	if c.Retention.ScalingEvents != 365*24*time.Hour || c.Retention.Audit != 0 {
		t.Fatalf("defaults: scaling_events = %s, audit = %s", c.Retention.ScalingEvents, c.Retention.Audit)
	}
	if hasCode(c.Validate(), "retention.audit_renamed") {
		t.Fatal("a file that never used retention.audit is asked to rename it")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "zoomies.yaml")
	if err := os.WriteFile(path, []byte("retention:\n  audit: 48h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Retention.ScalingEvents != 48*time.Hour {
		t.Fatalf("scaling_events = %s, want the 48h retention.audit set", loaded.Retention.ScalingEvents)
	}
	if !hasCode(loaded.Validate(), "retention.audit_renamed") {
		t.Fatal("retention.audit in the file raised no finding asking for the rename")
	}

	// The new key wins over the environment alias too.
	t.Setenv("ZOOMIES_RETENTION_SCALING_EVENTS", "72h")
	loaded, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Retention.ScalingEvents != 48*time.Hour {
		// The file's audit key is applied in normalize, after the environment,
		// so it still wins here; that is the cost of honouring it at all and
		// the finding is what asks the operator to remove it.
		t.Fatalf("scaling_events = %s with both keys set, want 48h from the file's audit key", loaded.Retention.ScalingEvents)
	}
	if err := os.WriteFile(path, []byte("retention:\n  jobs: 1h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Retention.ScalingEvents != 72*time.Hour {
		t.Fatalf("scaling_events = %s, want 72h from ZOOMIES_RETENTION_SCALING_EVENTS", loaded.Retention.ScalingEvents)
	}
}

// A deployment that is not renting machines is told nothing about how to bound
// it. Every provider finding is conditional on provider.enabled, and the two
// test harnesses that assert "no warnings" depend on the defaults staying
// quiet -- so this is the test that keeps the defaults default.
func TestTheProviderSectionIsSilentUntilItIsTurnedOn(t *testing.T) {
	c := Default()
	for _, f := range c.Validate() {
		if strings.HasPrefix(f.Code, "provider.") {
			t.Errorf("the default configuration raised %s: %s", f.Code, f.Title)
		}
	}
}

// A maximum of none is none, so a fleet told to rent machines without a ceiling
// rents nothing -- which is the safe direction, and useless, and therefore
// worth saying out loud. It is a warning rather than an error because the
// controller is otherwise fine and everything else it does still works.
func TestEnablingProvidersWithNoCeilingIsAWarningNotARefusal(t *testing.T) {
	c := Default()
	c.Provider.Enabled = true

	f := c.Validate()
	if !hasCode(f, "provider.no_ceiling") {
		t.Fatal("a fleet allowed to rent machines but capped at none drew no warning")
	}
	if err := f.Errors().Err(); err != nil {
		t.Fatalf("a fleet with no ceiling refused to start: %v", err)
	}

	c.Provider.MaxMachines = 4
	if f := c.Validate(); hasCode(f, "provider.no_ceiling") {
		t.Fatal("a fleet with a ceiling still drew the warning about not having one")
	}
}

// The deadlines have to be ordered, and the two that matter are the ones an
// operator is most likely to shorten: an ambiguity window inside the creation
// window quarantines machines that are merely still being built, and an
// enrolment window shorter than the silence that loses a host gives up on
// machines that arrived and went briefly quiet.
func TestProviderDeadlinesMustOutlastTheWorkTheyCover(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Config)
		code string
	}{
		{"an unbounded call", func(c *Config) { c.Provider.CallTimeout = 0 }, "provider.timeouts"},
		{"ambiguity inside creation", func(c *Config) { c.Provider.AmbiguityTimeout = c.Provider.CreateTimeout }, "provider.timeouts"},
		{"enrolment shorter than an unhealthy host", func(c *Config) { c.Provider.EnrolTimeout = HostUnhealthyAfter - time.Second }, "provider.enrol_timeout"},
		{"no interval", func(c *Config) { c.Provider.Interval = 0 }, "provider.interval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Provider.Enabled = true
			c.Provider.MaxMachines = 4
			tc.set(c)

			f := c.Validate()
			if !hasCode(f, tc.code) {
				t.Fatalf("%s passed validation; want %s. got %+v", tc.name, tc.code, f)
			}
			if len(f.Errors()) == 0 {
				t.Fatalf("%s was not an error, so the controller would start with it", tc.name)
			}
		})
	}
}

// The two scale-down settings are warnings rather than errors: both describe a
// fleet that works and wastes money, which is a thing to be told about and not
// a reason to refuse to start.
func TestScaleDownSettingsThatCostMoneyAreWarnings(t *testing.T) {
	c := Default()
	c.Provider.Enabled = true
	c.Provider.MaxMachines = 4
	c.Provider.DeleteGrace = RunnersLostAfter
	c.Provider.ScaleDownCooldown = c.Provider.IdleTimeout - time.Minute

	f := c.Validate()
	if !hasCode(f, "provider.delete_grace_short") {
		t.Error("a delete grace no longer than the silence that loses a host drew no warning")
	}
	if !hasCode(f, "provider.scale_down_fast") {
		t.Error("removing machines sooner than one idle period drew no warning")
	}
	if err := f.Err(); err != nil {
		t.Fatalf("a wasteful but working fleet refused to start: %v", err)
	}
}

// The pause is worth saying once, so that a fleet that is quiet because
// somebody held it does not look like a fleet that is quiet because nothing is
// queued.
func TestThePauseSaysSoWithoutBeingAProblem(t *testing.T) {
	c := Default()
	c.Provider.Enabled = true
	c.Provider.MaxMachines = 4
	c.Provider.Paused = true

	f := c.Validate()
	if !hasCode(f, "provider.paused") {
		t.Fatal("a paused provider said nothing at startup")
	}
	for _, got := range f {
		if strings.HasPrefix(got.Code, "provider.") && got.Severity != SeverityInfo {
			t.Fatalf("the pause was reported as something to fix: %+v", got)
		}
	}
}

// The capacity map's layouts are the one enum a file can get wrong that the
// settings page never could: the page offers a menu, and the API and the
// environment refuse anything off it. A file is read as it is, so a misspelt
// layout is caught by the validator, named after the key it sits in, and
// stops startup the way a misspelt log level does -- a controller running on
// a value it silently replaced is the thing this design exists to prevent.
func TestACapacityMapLayoutTheFileGetsWrongIsAFinding(t *testing.T) {
	layoutFindings := func(c *Config) Findings {
		var out Findings
		for _, f := range c.Validate() {
			if f.Code == "ui.capacity_map.layout" {
				out = append(out, f)
			}
		}
		return out
	}
	c := Default()
	if got := layoutFindings(c); len(got) != 0 {
		t.Fatalf("the defaults raise %d layout findings: %+v", len(got), got)
	}
	c.UI.CapacityMap.HostsLayout = "sideways"
	found := layoutFindings(c)
	if len(found) != 1 {
		t.Fatalf("expected one finding for a bad layout, got %d: %+v", len(found), found)
	}
	if found[0].Severity != SeverityError || found[0].Setting != "ui.capacity_map.hosts_layout" {
		t.Errorf("the finding is %s about %q; want an error about ui.capacity_map.hosts_layout", found[0].Severity, found[0].Setting)
	}
	for _, want := range []string{"sideways", "overlay", "split"} {
		if !strings.Contains(found[0].Title+" "+found[0].Fix, want) {
			t.Errorf("the finding does not mention %q: %+v", want, found[0])
		}
	}

	// The same value through the settings path is refused before it lands,
	// and the refusal says what the choices are.
	if _, err := c.SetValueString("ui.capacity_map.overview_layout", "sideways"); err == nil {
		t.Fatal("a nonsense layout was accepted by the settings path")
	} else if !strings.Contains(err.Error(), "overlay") || !strings.Contains(err.Error(), "split") {
		t.Errorf("the refusal does not name the choices: %v", err)
	}
	if _, err := c.SetValueString("ui.capacity_map.overview_layout", "Split"); err != nil {
		t.Fatalf("a layout in the wrong case was refused: %v", err)
	}
	if c.UI.CapacityMap.OverviewLayout != CapacityLayoutSplit {
		t.Errorf("the overview layout is %q after setting split", c.UI.CapacityMap.OverviewLayout)
	}
	// The two pages are set separately: one moving leaves the other alone.
	if c.UI.CapacityMap.HostsLayout != "sideways" {
		t.Errorf("setting the overview layout changed the hosts layout to %q", c.UI.CapacityMap.HostsLayout)
	}
}

// A configuration problem is hard for one reason: "I changed it and nothing
// happened". Four layers stack, the file is the one people edit, and it is the
// one a stored or pinned value beats -- so a finding that names a setting and
// not the layer holding it sends an operator to edit the wrong thing, restart,
// and read the same complaint having learned nothing.
//
// It matters most where the settings page cannot help at all: a value saved
// from that page which stops the controller starting is unreachable from a page
// the controller serves, so the way back has to be something typed at a stopped
// one -- and the moment to be told it is the moment it refuses.
func TestAFindingSaysWhichLayerSetTheValueAndHowToUndoIt(t *testing.T) {
	cfg := Default()
	cfg.Server.Bind = "not-a-host-port"
	cfg.note("server.bind", SourceDatabase)

	findings := cfg.Validate()
	var bind *Finding
	for i := range findings {
		if findings[i].Setting == "server.bind" {
			bind = &findings[i]
			break
		}
	}
	if bind == nil {
		t.Fatalf("a malformed bind produced no finding about server.bind: %v", findings)
	}
	if bind.Source != SourceDatabase {
		t.Fatalf("source = %q, want database", bind.Source)
	}
	if bind.Undo != "zoomies config unset server.bind" {
		t.Fatalf("undo = %q, want the command that takes it back out of the database", bind.Undo)
	}
	// And the refusal itself carries it, because that is the text an operator
	// reads at the moment the controller will not start.
	err := findings.Err()
	if err == nil {
		t.Fatal("a malformed bind did not stop startup")
	}
	if !strings.Contains(err.Error(), "zoomies config unset server.bind") {
		t.Fatalf("the refusal does not say how to undo it:\n%s", err)
	}

	// The environment is the other layer the file cannot beat, and it needs a
	// different instruction: there is nothing to unset in the database.
	cfg.note("server.bind", SourceEnvironment)
	for _, f := range cfg.Validate() {
		if f.Setting != "server.bind" {
			continue
		}
		if f.Undo != "unset ZOOMIES_BIND" {
			t.Fatalf("undo = %q, want the variable unset", f.Undo)
		}
	}

	// A value in the file says nothing: it is edited where it is, which is
	// where somebody would have looked anyway, and a line telling them so
	// would be a line they learn to skip on every other finding.
	cfg.note("server.bind", SourceFile)
	for _, f := range cfg.Validate() {
		if f.Setting == "server.bind" && (f.Undo != "" || f.SourceSentence() != "") {
			t.Fatalf("a value in the file offered an undo: %q / %q", f.Undo, f.SourceSentence())
		}
	}
}

// The provision timeout is the only one of the three bounds on a runner's start
// that gives up, so a value inside what the other two budget is a fleet that
// condemns runners which are still coming up -- and then has the pool replace
// each one, putting a second pull of the same image on the link that was slow
// to begin with. It is a warning rather than an error because such a fleet does
// work, expensively, on fast hosts.
func TestAProvisionTimeoutInsideARunnersStartIsWarnedAbout(t *testing.T) {
	c := Default()
	c.Scheduler.ProvisionTimeout = RunnerCreateBudget
	f := c.Validate()
	if !hasCode(f, "scheduler.provision_timeout_short") {
		t.Errorf("a provision timeout of %s drew no warning against a %s create budget: %+v",
			c.Scheduler.ProvisionTimeout, RunnerCreateBudget, f)
	}
	if err := f.Err(); err != nil {
		t.Fatalf("a fleet that starts runners slowly refused to start: %v", err)
	}

	// Zero is off, and a fleet that has switched the timeout off has said that
	// runners may take as long as they take. Warning about that would be
	// warning about the absence of the thing being warned about.
	c.Scheduler.ProvisionTimeout = 0
	if f := c.Validate(); hasCode(f, "scheduler.provision_timeout_short") {
		t.Error("switching the provision timeout off drew the warning for setting it too low")
	}

	// The wait counted is the one that happens, not the one that was written
	// down: runners.docker_wait of zero leaves the image's own two minutes in
	// place, so a timeout that only just cleared the create budget is still
	// inside a real start.
	c.Runners.DockerWait = 0
	c.Scheduler.ProvisionTimeout = RunnerCreateBudget + time.Minute
	if f := c.Validate(); !hasCode(f, "scheduler.provision_timeout_short") {
		t.Errorf("a timeout %s above the create budget drew no warning, though the image still waits %s for its daemon",
			time.Minute, ImageDockerWait)
	}
}

// A quiet host is two judgements, not one, and every deadline that may destroy
// a machine has to be measured against the later of them. Measured against the
// ninety seconds that merely make a host unhealthy, both of these accepted a
// value that deletes or drains a machine while the controller still believes a
// job is running on it.
func TestMachineDeadlinesAreJudgedAgainstTheSilenceThatFreesRunners(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Config)
		code string
	}{
		{"a grace inside the runners' own", func(c *Config) { c.Provider.DeleteGrace = RunnersLostAfter }, "provider.delete_grace_short"},
		{"draining inside the runners' own", func(c *Config) { c.Provider.IdleTimeout = RunnersLostAfter }, "provider.idle_timeout_short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Provider.Enabled = true
			c.Provider.MaxMachines = 4
			tc.set(c)

			f := c.Validate()
			if !hasCode(f, tc.code) {
				t.Fatalf("%s passed unremarked; want %s. got %+v", tc.name, tc.code, f)
			}
			if err := f.Err(); err != nil {
				t.Fatalf("a fleet that wastes machines refused to start: %v", err)
			}
		})
	}

	// And the defaults clear both, or every fleet that turns providers on would
	// be told its own defaults were wrong.
	c := Default()
	c.Provider.Enabled = true
	c.Provider.MaxMachines = 4
	for _, code := range []string{"provider.delete_grace_short", "provider.idle_timeout_short"} {
		if f := c.Validate(); hasCode(f, code) {
			t.Errorf("the default provider settings raise %s against themselves", code)
		}
	}
}

// The bound on automatic re-runs is refused at both ends. Zero would leave
// scheduler.auto_rerun reading as on while doing nothing, which is worse than
// either answer; a large one turns a fault the fleet causes every time into a
// bill nobody chose.
func TestTheAutomaticRerunLimitIsRefusedOutsideItsRange(t *testing.T) {
	for _, tc := range []struct {
		limit int
		want  bool
	}{{0, true}, {1, false}, {5, false}, {6, true}} {
		c := Default()
		c.Scheduler.AutoRerunLimit = tc.limit
		if got := hasCode(c.Validate(), "scheduler.auto_rerun_limit"); got != tc.want {
			t.Errorf("limit %d drew the finding = %v, want %v", tc.limit, got, tc.want)
		}
	}
}

// Requiring two-step is not a risk, so it is not a warning; it is info,
// because the two things it does not cover -- single sign-on accounts and
// API tokens -- are the ones an operator turning it on is most likely to
// assume it does.
func TestRequiringTwoStepIsInfoAndSaysWhatItDoesNotCover(t *testing.T) {
	for _, tc := range []struct {
		name      string
		require   bool
		oidc      bool
		want      bool
		wantsOIDC bool
	}{
		{"off by default", false, false, false, false},
		{"on", true, false, true, false},
		{"on with single sign-on", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Security.RequireTwoStep = tc.require
			c.OIDC.Enabled = tc.oidc
			var got *Finding
			for _, f := range c.Validate() {
				if f.Code == "two_step.required" {
					f := f
					got = &f
				}
			}
			if (got != nil) != tc.want {
				t.Fatalf("two_step.required present = %v, want %v", got != nil, tc.want)
			}
			if got == nil {
				return
			}
			if got.Severity != SeverityInfo {
				t.Errorf("severity = %v, want info", got.Severity)
			}
			if !strings.Contains(got.Detail, "API tokens") {
				t.Errorf("the finding does not say API tokens are unaffected: %s", got.Detail)
			}
			if strings.Contains(got.Detail, "single sign-on") != tc.wantsOIDC {
				t.Errorf("mentions single sign-on = %v, want %v: %s", !tc.wantsOIDC, tc.wantsOIDC, got.Detail)
			}
		})
	}
	if Default().Security.RequireTwoStep {
		t.Fatal("two-step is required by default; it must be opt-in")
	}
}
