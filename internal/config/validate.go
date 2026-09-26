package config

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

// Severity ranks a validation finding.
type Severity string

const (
	// SeverityError stops startup.
	SeverityError Severity = "error"
	// SeverityWarning does not stop startup but is logged and shown in the UI.
	SeverityWarning Severity = "warning"
	// SeverityInfo is a note worth showing once, such as "auth is disabled
	// because you asked for it".
	SeverityInfo Severity = "info"
)

// maxQuietFinishedRetention is the longest agent.finished_retention that passes
// without a warning. Past a day, finished containers stop being something an
// operator reads and start being the reason the host has no disk.
const maxQuietFinishedRetention = 24 * time.Hour

// HostUnhealthyAfter is how long a host may go without a heartbeat before the
// controller counts it unhealthy and stops placing runners on it. It is the
// store's HeartbeatTimeout, restated here because this package sits below the
// store and cannot import it; a test in internal/controller holds the two
// equal.
//
// It is the earlier and softer of the two judgements a quiet host is given, and
// the distinction is load-bearing: this one only stops new work arriving, so a
// deadline that may not destroy anything belongs against RunnersLostAfter
// below, not against this.
const HostUnhealthyAfter = 90 * time.Second

// RunnersLostAfter is the later, harder judgement: how long a host may be
// silent before the runners still recorded on it are failed, and the job one of
// them was running is marked the fleet's failure. It is the controller's
// hostLostAfter, restated for the same reason and held equal by the same test.
//
// Anything that destroys a machine has to outlast this rather than
// HostUnhealthyAfter. A machine deleted between the two is deleted while the
// controller still counts its runners live -- which is to say, out from under a
// job that is still running.
const RunnersLostAfter = 5 * time.Minute

// RunnerCreateBudget is how long the agent gives itself to materialise one
// runner: a cold image pull on a slow link is minutes, not seconds. It is
// agent.CreateTimeout, restated for the same reason and held equal by the same
// test.
const RunnerCreateBudget = 15 * time.Minute

// ImageDockerWait is what the runner image waits for its daemon when
// runners.docker_wait leaves the choice to it -- the default in
// deploy/runner-entrypoint.sh, which a test in internal/docs holds to the
// script.
const ImageDockerWait = 2 * time.Minute

// MaxQuietHeartbeatInterval is the longest agent.heartbeat_interval that draws
// no warning: half the silence that makes a host unhealthy, so that one late
// heartbeat does not.
const MaxQuietHeartbeatInterval = HostUnhealthyAfter / 2

// euid is os.Geteuid, replaceable so a test can ask what a root process would
// be told without being one.
var euid = os.Geteuid

// MaxDockerWait is the longest runners.docker_wait the runner image accepts.
// The entrypoint refuses anything past 3600 seconds, and a value it refuses
// starts no runner.
const MaxDockerWait = time.Hour

// ReservedRunnerEnv is the environment the controller writes for each runner
// individually: its identity and its credentials. runners.env may not name
// them, because one value for the whole fleet is wrong for every runner in
// it. The names are the runner image's contract, spelled out in
// deploy/runner-entrypoint.sh and internal/backend, which this package cannot
// import; a test in internal/backend keeps the two lists the same.
var ReservedRunnerEnv = []string{
	"ZOOMIES_JITCONFIG", "ACTIONS_RUNNER_INPUT_JITCONFIG",
	"ZOOMIES_RUNNER_URL", "ZOOMIES_RUNNER_TOKEN", "ZOOMIES_RUNNER_NAME",
	"ZOOMIES_RUNNER_LABELS", "ZOOMIES_RUNNER_GROUP", "ZOOMIES_EPHEMERAL",
	"ZOOMIES_RUNNER_NO_DEFAULT_LABELS",
}

// reservedRunnerEnv returns the reserved names env sets, sorted.
func reservedRunnerEnv(env map[string]string) []string {
	var out []string
	for k := range env {
		if slices.Contains(ReservedRunnerEnv, strings.ToUpper(strings.TrimSpace(k))) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// runsAgent reports whether this process runs runners itself: as the embedded
// agent inside a controller, or as a standalone agent joined to one. The
// agent settings only mean something when it does.
func (c *Config) runsAgent() bool {
	return c.Agent.Embedded || c.Agent.ControllerURL != ""
}

// servesTLS reports whether browsers reach this controller over HTTPS, whether
// the listener terminates it or a proxy named in server.external_url does.
func (c *Config) servesTLS() bool {
	if c.Server.TLS.Mode != TLSOff {
		return true
	}
	u, err := url.Parse(c.Server.ExternalURL)
	return err == nil && u.Scheme == "https"
}

// Finding is one validation result, phrased so that it can be printed to a
// terminal and rendered in the UI's problems drawer without rewording.
type Finding struct {
	// Code is a stable identifier, e.g. "bind.public_no_tls".
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	// Setting names the config key involved, e.g. "server.bind".
	Setting string `json:"setting,omitempty"`
	// Title is one line: what is true.
	Title string `json:"title"`
	// Detail says why it matters and what the consequence is.
	Detail string `json:"detail"`
	// Fix says what to change, concretely.
	Fix string `json:"fix,omitempty"`
	// Source is the layer the offending value came from, and Undo is the
	// command that takes it back out of that layer. Both are filled in by
	// Config.Validate for findings that name a setting.
	//
	// It is the half of a configuration problem that is hardest to work out
	// and easiest for the program to know. An operator told "server.bind is
	// wrong, set it to a loopback address" will go and edit the file -- and if
	// the value came from the database or from a ZOOMIES_* variable, the file
	// they edit is the one layer that cannot win, so the controller refuses
	// again with the same message and they have learned nothing. Saying which
	// layer is speaking turns a loop into one command.
	Source Source `json:"source,omitempty"`
	// Undo is what to type to take the value back out. Empty when there is
	// nothing to undo -- a wrong value in the file is edited in the file, and
	// a default that fails validation is a bug here rather than a
	// configuration to change.
	//
	// It matters most in the case the settings page cannot help with: a value
	// saved from the UI that stops the controller starting. The page is behind
	// the controller, so the way back has to be something an operator can type
	// at a stopped one, and being told what to type at the moment it refuses
	// is the difference between a minute and an afternoon.
	//
	// It is a field rather than a method because it is sent to the browser,
	// and a browser that assembled the command itself would be a second place
	// for it to be assembled differently.
	Undo string `json:"undo,omitempty"`
}

// undoFor works out that command for a layer and a key.
func undoFor(source Source, setting string) string {
	f := Finding{Source: source, Setting: setting}
	return f.undoCommand()
}

func (f Finding) undoCommand() string {
	switch f.Source {
	case SourceDatabase:
		return "zoomies config unset " + f.Setting
	case SourceEnvironment:
		if s, ok := LookupSetting(f.Setting); ok && s.Env != "" {
			return "unset " + s.Env
		}
	}
	return ""
}

// SourceSentence says which layer set the value and what to type to take it
// back out, as one line for a terminal or a log.
//
// Empty for the file and the defaults: a value in the file is edited in the
// file, which is where the operator would have looked anyway, and a default
// that fails validation is a bug in this package rather than a configuration
// anybody can change.
func (f Finding) SourceSentence() string {
	switch f.Source {
	case SourceDatabase:
		return "this value is stored in this fleet's database, so editing the configuration file will not change it; " +
			"with the controller stopped, `" + f.Undo + "` puts the file or the default back in charge"
	case SourceEnvironment:
		if undo := f.Undo; undo != "" {
			return "this value comes from the environment, which is the last word, so neither the file nor the database can change it; " +
				"`" + undo + "` in the controller's environment hands it back"
		}
		return "this value comes from a ZOOMIES_* variable in the environment, which overrides both the file and the database"
	}
	return ""
}

func (f Finding) String() string {
	s := fmt.Sprintf("[%s] %s", f.Severity, f.Title)
	if f.Detail != "" {
		s += " -- " + f.Detail
	}
	if f.Fix != "" {
		s += " Fix: " + f.Fix
	}
	return s
}

// Findings is an ordered list of validation results.
type Findings []Finding

// Errors returns only the findings that stop startup.
func (fs Findings) Errors() Findings { return fs.bySeverity(SeverityError) }

// Warnings returns only the non-fatal findings.
func (fs Findings) Warnings() Findings { return fs.bySeverity(SeverityWarning) }

func (fs Findings) bySeverity(s Severity) Findings {
	var out Findings
	for _, f := range fs {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// uiHidden lists the finding codes the web UI does not surface.
//
// bind.public_no_tls is the only one so far, and it is here because it is true
// of every deployment that terminates TLS at a reverse proxy -- Cloudflare,
// nginx, Caddy -- which is the arrangement its own fix recommends and by far
// the most common way Zoomies is run. The controller cannot see that proxy
// from behind it, so the warning sat permanently on the Overview of correctly
// configured fleets, and a count that is always amber is a count nobody reads.
//
// It is not dropped: `zoomies config check` and the startup banner still print
// it, where it is read once by the person doing the deploying rather than
// every day by everybody else.
var uiHidden = map[string]bool{
	"bind.public_no_tls": true,
}

// ForUI drops the findings the web UI does not surface, leaving the CLI's own
// output alone. It returns an empty, non-nil slice when nothing is left, which
// is what the problems drawer renders "nothing needs your attention" from.
func (fs Findings) ForUI() Findings {
	out := make(Findings, 0, len(fs))
	for _, f := range fs {
		if !uiHidden[f.Code] {
			out = append(out, f)
		}
	}
	return out
}

// Err returns a single error describing every fatal finding, or nil.
func (fs Findings) Err() error {
	errs := fs.Errors()
	if len(errs) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("configuration is not valid:\n")
	for _, f := range errs {
		fmt.Fprintf(&b, "  - %s: %s\n", f.Setting, f.Title)
		if f.Detail != "" {
			fmt.Fprintf(&b, "      %s\n", f.Detail)
		}
		if f.Fix != "" {
			fmt.Fprintf(&b, "      fix: %s\n", f.Fix)
		}
		// Which layer is saying it, and how to take it back. Without this an
		// operator edits the file, restarts, and gets the same refusal.
		if where := f.SourceSentence(); where != "" {
			fmt.Fprintf(&b, "      %s\n", where)
		}
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// Validate checks the configuration and returns every finding, fatal or not.
//
// The warnings are the point of this function. Each one corresponds to a
// setting that trades safety for convenience: a public bind without TLS, a
// Docker socket handed to jobs, running as root, authentication switched off.
// The operator sees all of them at startup and again in the UI, so no dangerous
// default is ever in effect quietly.
func (c *Config) Validate() Findings {
	var fs Findings
	add := func(f Finding) { fs = append(fs, f) }

	if mode := c.Scheduler.PlacementMode; mode != "" && mode != "headroom" && mode != "shadow" && mode != "readiness" {
		add(Finding{Code: "scheduler.placement_mode", Severity: SeverityError, Setting: "scheduler.placement_mode", Title: "unknown host placement policy", Fix: "choose headroom, shadow or readiness."})
	}
	if c.Scheduler.RegistrationConcurrency < 1 || c.Scheduler.RegistrationConcurrency > 16 {
		add(Finding{Code: "scheduler.registration_concurrency", Severity: SeverityError, Setting: "scheduler.registration_concurrency", Title: "registration concurrency is outside its supported range", Detail: "Unbounded credential requests amplify GitHub outages and rate limits.", Fix: "set scheduler.registration_concurrency between 1 and 16; the default is 1."})
	}

	if c.Agent.PrewarmTimeout < time.Second || c.Agent.PrewarmTimeout > 15*time.Minute {
		add(Finding{Code: "agent.prewarm_timeout", Severity: SeverityError, Setting: "agent.prewarm_timeout", Title: "background preparation timeout is outside its supported range", Detail: "Background image preparation needs a bounded budget independent of foreground creates.", Fix: "set agent.prewarm_timeout between 1s and 15m; the default is 5m."})
	}
	if c.Agent.PrewarmJitter < 0 || c.Agent.PrewarmJitter > 5*time.Minute {
		add(Finding{Code: "agent.prewarm_jitter", Severity: SeverityError, Setting: "agent.prewarm_jitter", Title: "background preparation stagger is outside its supported range", Detail: "Background refresh delays must remain bounded.", Fix: "set agent.prewarm_jitter between 0s and 5m; the default is 30s."})
	}
	// --- Listener ---------------------------------------------------------
	if c.Server.Bind == "" {
		add(Finding{
			Code: "bind.empty", Severity: SeverityError, Setting: "server.bind",
			Title:  "no listen address configured",
			Detail: "the controller has nowhere to accept connections.",
			Fix:    `set server.bind, for example "127.0.0.1:8080".`,
		})
	} else if _, _, err := net.SplitHostPort(c.Server.Bind); err != nil {
		add(Finding{
			Code: "bind.malformed", Severity: SeverityError, Setting: "server.bind",
			Title:  fmt.Sprintf("%q is not a host:port address", c.Server.Bind),
			Detail: err.Error(),
			Fix:    `use the form "127.0.0.1:8080" or ":8080".`,
		})
	}

	public := c.BindsPublicly()
	if public && c.Server.TLS.Mode == TLSOff {
		add(Finding{
			Code: "bind.public_no_tls", Severity: SeverityWarning, Setting: "server.bind",
			Title: fmt.Sprintf("listening on %s without TLS", c.Server.Bind),
			Detail: "session cookies, API tokens and the GitHub App private key you paste " +
				"during setup all cross the network in cleartext.",
			Fix: "put a TLS-terminating reverse proxy in front, or set server.tls.mode to " +
				"self-signed or files. If a proxy already terminates TLS, this warning is expected.",
		})
	}
	if public && len(c.Server.TrustedProxies) == 0 && c.Server.TLS.Mode == TLSOff {
		add(Finding{
			Code: "proxy.untrusted", Severity: SeverityInfo, Setting: "server.trusted_proxies",
			Title:  "client IPs come from the socket, not X-Forwarded-For",
			Detail: "audit entries and login rate limiting will record your proxy's address for every request.",
			Fix:    "list your proxy's CIDR in server.trusted_proxies, or the word `cloudflare` when Cloudflare is in front.",
		})
	}
	if c.Security.DockerInDockerExpected {
		add(Finding{
			Code: "dind.expected", Severity: SeverityInfo, Setting: "security.docker_in_docker_expected",
			Title: "docker-in-docker pools are not listed as dangerous here",
			Detail: "a pool whose docker_mode is dind still runs its daemon in a privileged container beside each runner; " +
				"this fleet has said it knows, so that setting no longer raises pool.dangerous on its own. " +
				"The host docker socket and persistent runners still do.",
			Fix: "turn security.docker_in_docker_expected off to be told again.",
		})
	}
	if c.Server.AllowIndexing {
		add(Finding{
			Code: "indexing.allowed", Severity: SeverityWarning, Setting: "server.allow_indexing",
			Title: "search engines are invited to index this controller",
			Detail: "robots.txt now allows crawling and advertises the sitemap, so the sign-in " +
				"page and this controller's address can appear in public search results.",
			Fix: "leave server.allow_indexing off unless this instance is deliberately public.",
		})
	}
	for _, cidr := range c.Server.TrustedProxies {
		if strings.TrimSpace(cidr) == TrustedProxyCloudflare {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			if net.ParseIP(cidr) == nil {
				add(Finding{
					Code: "proxy.bad_cidr", Severity: SeverityError, Setting: "server.trusted_proxies",
					Title: fmt.Sprintf("%q is not an IP address or CIDR", cidr),
					Fix:   `use a form like "10.0.0.0/8" or "192.168.1.5", or the word "cloudflare".`,
				})
			}
			continue
		}
		// A zero-length prefix is every address there is. It is what an
		// operator writes to make a header-based setup "just work", and what
		// it does is let any client choose the address the audit log records
		// and the login limiter counts.
		if ones, _ := network.Mask.Size(); ones == 0 {
			add(Finding{
				Code: "proxy.trust_everyone", Severity: SeverityWarning, Setting: "server.trusted_proxies",
				Title: fmt.Sprintf("%s trusts every client to say where it came from", cidr),
				Detail: "X-Forwarded-For is believed from any address, so a caller can pick the IP the audit log " +
					"records for it and defeat login rate limiting by rotating the one it claims.",
				Fix: "list only your proxy's own address range, or the word `cloudflare` when Cloudflare is in front.",
			})
		}
	}
	for _, origin := range c.Server.AllowedOrigins {
		o := strings.TrimSpace(origin)
		if o == "*" {
			add(Finding{
				Code: "origins.any", Severity: SeverityWarning, Setting: "server.allowed_origins",
				Title: "any website may act with a signed-in operator's session",
				Detail: `"*" switches the origin check off: a page on any site an operator visits while signed in ` +
					"can create pools, drain runners and mint tokens with their session cookie, which is the " +
					"cross-site request forgery the check exists to stop.",
				Fix: "list the origins that host the UI, e.g. https://zoomies.example.com, instead of \"*\".",
			})
			continue
		}
		if u, err := url.Parse(o); err == nil && u.Scheme == "http" && !loopbackHost(u.Hostname()) && c.servesTLS() {
			add(Finding{
				Code: "origins.insecure", Severity: SeverityWarning, Setting: "server.allowed_origins",
				Title: fmt.Sprintf("%s is allowed to act on this controller over plaintext", o),
				Detail: "a page served over http:// can be rewritten by anything on the network path, and whatever " +
					"rewrites it inherits the permission this entry grants: to make changes with an operator's session.",
				Fix: "serve that origin over https:// and list the https:// address here.",
			})
		}
	}

	switch c.Server.TLS.Mode {
	case TLSOff, TLSSelfSigned:
	case TLSFiles:
		if c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "" {
			add(Finding{
				Code: "tls.files_missing", Severity: SeverityError, Setting: "server.tls",
				Title: "tls.mode is \"files\" but cert_file or key_file is empty",
				Fix:   "set both server.tls.cert_file and server.tls.key_file.",
			})
		} else {
			for name, p := range map[string]string{"cert_file": c.Server.TLS.CertFile, "key_file": c.Server.TLS.KeyFile} {
				if _, err := os.Stat(p); err != nil {
					add(Finding{
						Code: "tls.file_unreadable", Severity: SeverityError, Setting: "server.tls." + name,
						Title:  fmt.Sprintf("cannot read %s", p),
						Detail: err.Error(),
					})
				}
			}
		}
	default:
		add(Finding{
			Code: "tls.mode_unknown", Severity: SeverityError, Setting: "server.tls.mode",
			Title: fmt.Sprintf("%q is not a TLS mode", c.Server.TLS.Mode),
			Fix:   `use "off", "self-signed" or "files".`,
		})
	}
	if c.Server.TLS.Mode == TLSSelfSigned {
		add(Finding{
			Code: "tls.self_signed", Severity: SeverityInfo, Setting: "server.tls.mode",
			Title:  "serving a self-signed certificate",
			Detail: "browsers will warn, and GitHub will refuse to deliver webhooks to it.",
			Fix:    "for webhooks to work, terminate TLS with a certificate GitHub trusts, or run in polling mode.",
		})
	}

	// --- External URL and webhooks ---------------------------------------
	if c.Server.ExternalURL == "" {
		add(Finding{
			Code: "external_url.missing", Severity: SeverityWarning, Setting: "server.external_url",
			Title: "no external URL configured",
			Detail: "Zoomies cannot tell GitHub where to deliver webhooks, so scaling will " +
				"depend entirely on the fallback poller and will react in tens of seconds rather than instantly.",
			Fix: "set server.external_url to the address GitHub can reach, e.g. https://zoomies.example.com.",
		})
	} else if !c.ExternalURLValid() {
		add(Finding{
			Code: "external_url.malformed", Severity: SeverityError, Setting: "server.external_url",
			Title: fmt.Sprintf("%q is not an absolute URL", c.Server.ExternalURL),
			Fix:   "include the scheme, e.g. https://zoomies.example.com.",
		})
	} else if u, err := url.Parse(c.Server.ExternalURL); err == nil && u.Scheme == "http" {
		// Plaintext to this machine costs nothing; ExternalURLIsLocal is the
		// same question the join command and the webhook probe ask.
		if !c.ExternalURLIsLocal() {
			add(Finding{
				Code: "external_url.insecure", Severity: SeverityWarning, Setting: "server.external_url",
				Title:  "external URL uses http://",
				Detail: "GitHub will deliver webhooks over plaintext, and the HMAC secret is the only thing protecting them from forgery in transit.",
				Fix:    "use https:// once you have a certificate.",
			})
		}
	}
	if !c.GitHub.PollFallback {
		add(Finding{
			Code: "poll.disabled", Severity: SeverityWarning, Setting: "github.poll_fallback",
			Title:  "the queued-job poller is disabled",
			Detail: "if a webhook delivery is lost or misconfigured, jobs will queue forever with no runner created and nothing to notice it.",
			Fix:    "leave github.poll_fallback on unless you have external monitoring of webhook delivery.",
		})
	}
	if c.GitHub.PollInterval > 0 && c.GitHub.PollInterval < 10*time.Second {
		add(Finding{
			Code: "poll.too_fast", Severity: SeverityWarning, Setting: "github.poll_interval",
			Title:  fmt.Sprintf("polling every %s will consume your GitHub API rate limit", c.GitHub.PollInterval),
			Detail: "a GitHub App installation gets 5,000 requests an hour per installation.",
			Fix:    "use 30s or more, and rely on webhooks for latency.",
		})
	}
	if c.GitHub.APIBaseURL == "" {
		add(Finding{
			Code: "github.api_base_missing", Severity: SeverityError, Setting: "github.api_base_url",
			Title: "no GitHub API base URL",
			Fix:   "use https://api.github.com, or https://your-ghes-host/api/v3 for Enterprise Server.",
		})
	} else if u, err := url.Parse(c.GitHub.APIBaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		add(Finding{
			Code: "github.api_base_malformed", Severity: SeverityError, Setting: "github.api_base_url",
			Title: fmt.Sprintf("%q is not an absolute URL", c.GitHub.APIBaseURL),
		})
	}

	// --- Storage and secrets ---------------------------------------------
	if c.Database.Path == "" {
		add(Finding{
			Code: "db.path_missing", Severity: SeverityError, Setting: "database.path",
			Title: "no database path",
			Fix:   "set database.path, e.g. /var/lib/zoomies/zoomies.db.",
		})
	} else if dir := filepath.Dir(c.Database.Path); dir != "" {
		if info, err := os.Stat(dir); err == nil && !info.IsDir() {
			add(Finding{
				Code: "db.parent_not_dir", Severity: SeverityError, Setting: "database.path",
				Title: fmt.Sprintf("%s exists and is not a directory", dir),
			})
		}
	}

	hasKey := c.Security.EncryptionKey != ""
	if !hasKey && c.Security.EncryptionKeyFile != "" {
		if _, err := os.Stat(c.Security.EncryptionKeyFile); err == nil {
			hasKey = true
		}
	}
	if !hasKey {
		add(Finding{
			Code: "crypto.no_key", Severity: SeverityWarning, Setting: "security.encryption_key_file",
			Title: "no encryption key yet",
			Detail: "one will be generated on first start. Back it up: without it, the stored " +
				"GitHub App private key and webhook secrets cannot be decrypted.",
			Fix: "run `zoomies init` to generate and record one, or set ZOOMIES_ENCRYPTION_KEY.",
		})
	}
	if c.keyInFile {
		add(Finding{
			Code: "crypto.key_in_config", Severity: SeverityWarning, Setting: "security.encryption_key",
			Title:  "the encryption key is written in the config file",
			Detail: "anything that can read " + c.path + " — backups, configuration management, a support bundle — can decrypt every stored secret.",
			Fix:    "move it to security.encryption_key_file (mode 0600) or the ZOOMIES_ENCRYPTION_KEY environment variable.",
		})
	}

	// --- Authentication ---------------------------------------------------
	if c.Security.DisableAuth {
		// The bind address alone is not the question. The deployment this
		// project recommends is a loopback bind behind a reverse proxy, and in
		// exactly that shape "127.0.0.1" would wave through a controller the
		// whole internet can reach. An external URL or a trusted proxy is the
		// operator saying, in the configuration itself, that something in front
		// forwards to this listener.
		reachable := c.LikelyReachable()
		sev := SeverityError
		fix := "remove security.disable_auth."
		why := " The listener is not on loopback, so this is refused."
		switch {
		case !reachable:
			sev = SeverityWarning
			fix = "acceptable for local development only; never set this on a host others can reach."
			why = ""
		case !public:
			why = " This controller is behind a proxy or has an external URL, so it is not only reachable from this host, and this is refused."
		}
		add(Finding{
			Code: "auth.disabled", Severity: sev, Setting: "security.disable_auth",
			Title: "authentication is disabled",
			Detail: "every request is treated as an administrator: anyone who can reach the " +
				"listener can create pools, read the audit log and drain the fleet." + why,
			Fix: fix,
		})
	}
	// A session cookie without Secure is one a single plaintext request to the
	// same host hands to anyone watching. Zoomies cannot see that a proxy in
	// front terminates TLS, so it says so rather than guessing.
	if !c.CookieSecureValue() && public {
		add(Finding{
			Code: "auth.cookie_insecure", Severity: SeverityWarning, Setting: "security.cookie_secure",
			Title:  "session cookies are sent without the Secure attribute",
			Detail: "any plain-HTTP request to this host will carry a live session cookie, in the clear.",
			Fix: "set server.external_url to your https address (which turns this on by itself), " +
				"or set security.cookie_secure to true if TLS is terminated in front of this controller.",
		})
	}
	// Half a bootstrap is refused rather than guessed at. An account with no
	// credential is one nobody can sign in as, and a credential with no name
	// is an account nobody asked for; either way the provisioner that set it
	// is waiting on an instance that will never become usable.
	if b := c.Bootstrap; b.Requested() {
		var problem string
		switch {
		case b.Admin == "":
			problem = "ZOOMIES_BOOTSTRAP_ADMIN is not set, so there is no name for the first account"
		case b.PasswordFile == "" && b.TokenFile == "":
			problem = "neither ZOOMIES_BOOTSTRAP_PASSWORD_FILE nor ZOOMIES_BOOTSTRAP_TOKEN_FILE is set, so the first account would have no way in"
		case b.PasswordFile != "" && b.TokenFile != "":
			problem = "both ZOOMIES_BOOTSTRAP_PASSWORD_FILE and ZOOMIES_BOOTSTRAP_TOKEN_FILE are set, and the first account is made one way or the other"
		}
		if problem != "" {
			add(Finding{
				Code: "bootstrap.incomplete", Severity: SeverityError,
				Title:  "the unattended first account is only half described",
				Detail: problem + ".",
				Fix: "set ZOOMIES_BOOTSTRAP_ADMIN and exactly one of ZOOMIES_BOOTSTRAP_PASSWORD_FILE or " +
					"ZOOMIES_BOOTSTRAP_TOKEN_FILE, or unset all three and use the setup token.",
			})
		}
	}
	if c.Security.RateLimitLogins <= 0 {
		add(Finding{
			Code: "auth.no_login_limit", Severity: SeverityWarning, Setting: "security.rate_limit_logins",
			Title:  "password guessing is not rate limited",
			Detail: "the sign-in form answers every attempt as fast as it can, so a password can be brute-forced from one address at whatever rate the controller sustains.",
			Fix:    "set security.rate_limit_logins to a small number of attempts per address per minute; the default is 10.",
		})
	}
	if c.Security.SessionTTL <= 0 {
		add(Finding{
			Code: "auth.session_ttl", Severity: SeverityError, Setting: "security.session_ttl",
			Title: "session_ttl must be positive",
			Fix:   `use a duration like "168h".`,
		})
	} else if c.Security.SessionTTL > 90*24*time.Hour {
		add(Finding{
			Code: "auth.session_ttl_long", Severity: SeverityWarning, Setting: "security.session_ttl",
			Title:  fmt.Sprintf("browser sessions last %s", c.Security.SessionTTL),
			Detail: "a stolen session cookie stays valid for that long.",
		})
	}
	if c.OIDC.Enabled {
		if c.OIDC.Issuer == "" || c.OIDC.ClientID == "" {
			add(Finding{
				Code: "oidc.incomplete", Severity: SeverityError, Setting: "oidc",
				Title: "OIDC is enabled but issuer or client_id is empty",
				Fix:   "set oidc.issuer and oidc.client_id, or set oidc.enabled to false.",
			})
		}
		if c.OIDC.RedirectURL == "" && c.Server.ExternalURL == "" {
			add(Finding{
				Code: "oidc.no_redirect", Severity: SeverityError, Setting: "oidc.redirect_url",
				Title: "OIDC needs a redirect URL",
				Fix:   "set oidc.redirect_url, or set server.external_url and it will be derived.",
			})
		}
		if u, err := url.Parse(c.OIDC.Issuer); err == nil && u.Scheme == "http" && !loopbackHost(u.Hostname()) {
			add(Finding{
				Code: "oidc.insecure_issuer", Severity: SeverityWarning, Setting: "oidc.issuer",
				Title:  "single sign-on talks to its identity provider in the clear",
				Detail: "discovery, the token exchange and the client secret all travel to " + c.OIDC.Issuer + " over plaintext HTTP, where anything on the path can read or replace them and sign in as anyone.",
				Fix:    "use the issuer's https:// address.",
			})
		}
		if c.OIDC.LinkByUsername {
			add(Finding{
				Code: "oidc.link_by_username", Severity: SeverityWarning, Setting: "oidc.link_by_username",
				Title:  "a first single sign-on login may take over a password account of the same name",
				Detail: "whoever the identity provider says is \"admin\" signs in as the local admin, with its role; that is safe only when nobody at the provider can influence their own username claim.",
				Fix:    "leave it on only for the migration to single sign-on, then turn it off; or link accounts by hand and leave it off.",
			})
		}
		if c.OIDC.AllowSignup && len(c.OIDC.AdminGroups) == 0 && len(c.OIDC.OperatorGroups) == 0 {
			add(Finding{
				Code: "oidc.open_signup", Severity: SeverityWarning, Setting: "oidc.allow_signup",
				Title:  "anyone your identity provider authenticates gets an account",
				Detail: "they land in the viewer role, which can still read job history, repository names and the audit log.",
				Fix:    "restrict the application in your IdP, or turn oidc.allow_signup off and create accounts explicitly.",
			})
		}
	}

	// --- Agent and backends ----------------------------------------------
	if c.runsAgent() {
		switch c.Agent.Backend {
		case "docker", "podman", "process":
		case "":
			add(Finding{
				Code: "agent.backend_missing", Severity: SeverityError, Setting: "agent.backend",
				Title: "no runner backend selected",
				Fix:   `use "docker", "podman" or "process".`,
			})
		default:
			add(Finding{
				Code: "agent.backend_unknown", Severity: SeverityError, Setting: "agent.backend",
				Title: fmt.Sprintf("%q is not a runner backend", c.Agent.Backend),
				Fix:   `use "docker", "podman" or "process".`,
			})
		}
		if c.Agent.Capacity <= 0 {
			add(Finding{
				Code: "agent.capacity", Severity: SeverityError, Setting: "agent.capacity",
				Title: "agent.capacity must be at least 1",
				Fix:   fmt.Sprintf("this host has %d CPUs; %d is a reasonable starting point.", runtime.NumCPU(), defaultCapacity()),
			})
		}
		if c.Agent.Backend == "process" {
			add(Finding{
				Code: "agent.process_backend", Severity: SeverityWarning, Setting: "agent.backend",
				Title: "the process backend gives jobs no container isolation",
				Detail: "workflow steps run directly on this host as the agent's user, " +
					"sharing its filesystem, package manager and network.",
				Fix: "use the docker or podman backend unless you specifically need host access.",
			})
		}
		if euid() == 0 && c.Agent.Backend == "process" {
			add(Finding{
				Code: "agent.process_root", Severity: SeverityWarning, Setting: "agent.backend",
				Title:  "the process backend is running as root",
				Detail: "every workflow step from every matched repository executes as root on this host.",
				Fix:    "run the agent as a dedicated unprivileged user.",
			})
		}
		if c.Agent.InsecureSkipVerify {
			add(Finding{
				Code: "agent.insecure_tls", Severity: SeverityWarning, Setting: "agent.insecure_skip_verify",
				Title:  "the agent does not verify the controller's certificate",
				Detail: "anything on the network path can impersonate the controller and hand this agent arbitrary containers to run.",
				Fix:    "pin the controller CA with agent.ca_file instead.",
			})
		}
		if c.Agent.AllowInsecureHTTP {
			add(Finding{
				Code: "agent.insecure_http", Severity: SeverityWarning, Setting: "agent.allow_insecure_http",
				Title:  "the agent talks to the controller over plain HTTP",
				Detail: "this agent's long-lived token and every runner's just-in-time registration credentials cross the network in the clear.",
				Fix:    "put TLS in front of the controller and turn this off, or restrict it to a loopback/private link you trust.",
			})
		}
		if c.Agent.AllowUnverifiedRunnerDownload {
			add(Finding{
				Code: "agent.unverified_runner_download", Severity: SeverityWarning, Setting: "agent.allow_unverified_runner_download",
				Title:  "the process backend may install a runner it cannot verify",
				Detail: "an actions/runner archive whose SHA-256 Zoomies does not know will be downloaded and executed on this host as the agent's user; anything between this host and the download source could substitute its own.",
				Fix:    "pin the release with github.runner_version and give its digest in agent.runner_sha256 (it is in the actions/runner release notes), then turn this off.",
			})
		}
		if p := strings.TrimSpace(c.Agent.ExtraCAFile); p != "" {
			if f, ok := checkExtraCA(p); !ok {
				add(f)
			} else {
				add(Finding{
					Code: "agent.extra_ca", Severity: SeverityInfo, Setting: "agent.extra_ca_file",
					Title:  "container runners trust an extra certificate authority",
					Detail: fmt.Sprintf("every job on this host trusts certificates signed by %s as well as the image's own roots, so whoever holds that CA's key can read and alter the jobs' TLS traffic. That is what a TLS-intercepting proxy needs, and it is only right if the CA is your organisation's.", p),
					Fix:    "nothing, if the CA is your proxy's; otherwise clear agent.extra_ca_file.",
				})
			}
		}
		if c.Agent.WorkDir == "" {
			add(Finding{
				Code: "agent.workdir", Severity: SeverityError, Setting: "agent.work_dir",
				Title: "no work directory",
				Fix:   "set agent.work_dir, e.g. /var/lib/zoomies/work.",
			})
		}
		if c.Agent.DockerBuildCacheMB < 0 || c.Agent.DockerBuildCacheMB > 1048576 {
			add(Finding{
				Code: "agent.docker_build_cache_mb", Severity: SeverityError, Setting: "agent.docker_build_cache_mb",
				Title: "Docker build cache target must be between 0 and 1048576 MiB",
				Fix:   "use 5120 for a 5 GiB cache target, or 0 to disable automatic builder cache cleanup.",
			})
		}
		if c.Agent.BootstrapCPUGrace < 0 || c.Agent.BootstrapCPUGrace > 10*time.Minute {
			add(Finding{
				Code: "agent.bootstrap_cpu_grace", Severity: SeverityError, Setting: "agent.bootstrap_cpu_grace",
				Title: "startup CPU grace must be between 0s and 10m",
				Fix:   "use 2m to protect registration, or 0s to apply pressure throttling immediately; normal resource limits remain enforced.",
			})
		}
		if c.Agent.BootstrapCPUGrace >= 0 && c.Agent.BootstrapCPUGrace < 2*time.Minute {
			add(Finding{
				Code: "agent.bootstrap_cpu_grace_short", Severity: SeverityWarning, Setting: "agent.bootstrap_cpu_grace",
				Title:  "new runners have little protection from pressure throttling",
				Detail: "registration may compete with running jobs while already constrained by its normal CPU quota; an additional pressure reduction can delay startup on a loaded host.",
				Fix:    "set agent.bootstrap_cpu_grace to 2m and restart the agent; use 0s only when immediate throttling is intentional. Standalone agents need this setting on their own hosts.",
			})
		}
		if c.Agent.FinishedRetention < 0 {
			add(Finding{
				Code: "agent.finished_retention", Severity: SeverityError, Setting: "agent.finished_retention",
				Title: "agent.finished_retention cannot be negative",
				Fix:   "set it to how long a finished runner's output should stay readable on the host, e.g. 10m, or 0s to remove it straight away.",
			})
		}
		if c.Agent.FinishedRetention > maxQuietFinishedRetention {
			// Not a security setting, but the failure it leads to is the same
			// shape as the ones this list exists for: a toggle that looks
			// harmless until the host stops working. A day of finished
			// containers on a busy host is a full disk, and a full disk is
			// every job on it failing at once.
			add(Finding{
				Code: "agent.finished_retention_long", Severity: SeverityWarning, Setting: "agent.finished_retention",
				Title:  fmt.Sprintf("finished runners stay on the host for %s", c.Agent.FinishedRetention),
				Detail: "every finished runner leaves its container, sidecar and scratch directory on disk for that long, so a busy host accumulates a day's worth of job residue.",
				Fix:    "keep agent.finished_retention to minutes: long enough to read a finished runner's log, not long enough to fill the disk.",
			})
		}
	}
	if c.Agent.HeartbeatInterval > MaxQuietHeartbeatInterval {
		// The controller hands this interval to every agent at join, and it
		// counts a host unhealthy after a fixed silence. Past half of that
		// silence one late heartbeat is enough to flip the host unhealthy, and
		// a host that flaps is one the scheduler keeps leaving runners off.
		add(Finding{
			Code: "agent.heartbeat_interval_long", Severity: SeverityWarning, Setting: "agent.heartbeat_interval",
			Title:  fmt.Sprintf("hosts heartbeat every %s but go unhealthy after %s", c.Agent.HeartbeatInterval, HostUnhealthyAfter),
			Detail: "a single delayed heartbeat is enough to mark a host unhealthy and keep new runners off it until the next one lands, so the fleet flaps in and out of capacity.",
			Fix:    fmt.Sprintf("keep agent.heartbeat_interval at %s or less.", MaxQuietHeartbeatInterval),
		})
	}
	// --- Runners ----------------------------------------------------------
	if c.Runners.DockerWait < 0 || c.Runners.DockerWait > MaxDockerWait {
		// The runner image refuses anything outside 1..3600 seconds with a
		// configuration exit, so a value it would refuse is a fleet whose
		// every Docker pool fails to start a runner. Say so here instead.
		add(Finding{
			Code: "runners.docker_wait", Severity: SeverityError, Setting: "runners.docker_wait",
			Title:  fmt.Sprintf("runners.docker_wait is %s, which the runner image refuses", c.Runners.DockerWait),
			Detail: "the runner entrypoint accepts a wait of one second to one hour, and exits with a configuration error for anything else, so no runner on a Docker pool would take a job.",
			Fix:    "set runners.docker_wait to between 1s and 1h, e.g. 2m, or 0s to leave the image's own default.",
		})
	}
	if reserved := reservedRunnerEnv(c.Runners.Env); len(reserved) > 0 {
		add(Finding{
			Code: "runners.env_reserved", Severity: SeverityError, Setting: "runners.env",
			Title:  fmt.Sprintf("runners.env sets %s, which the runner contract owns", strings.Join(reserved, ", ")),
			Detail: "those variables carry each runner's own name, credentials and labels, written by the controller for that runner alone; one value for every runner would register them all as the same runner, or none at all.",
			Fix:    "remove them from runners.env. The runner's name, labels, group and credentials come from its pool.",
		})
	}

	if !c.runsAgent() {
		add(Finding{
			Code: "agent.none", Severity: SeverityInfo, Setting: "agent.embedded",
			Title:  "no embedded agent",
			Detail: "this controller cannot run runners itself; at least one standalone agent must join it.",
			Fix:    "run `zoomies agent join <controller-url> --token <join-token>` on a host, or set agent.embedded to true.",
		})
	}
	// The warning is about the process that runs runners. A controller with no
	// agent in it runs none, so a container escape has nothing of its to land
	// on, and a warning that fires there anyway is one operators learn to
	// ignore everywhere.
	if c.runsAgent() && euid() == 0 && (c.Agent.Backend == "docker" || c.Agent.Backend == "podman") {
		add(Finding{
			Code: "agent.root", Severity: SeverityWarning, Setting: "agent",
			Title:  "the agent process is running as root",
			Detail: "it does not need to be. A container escape from a runner lands on a root-owned process.",
			Fix:    "run as a dedicated user in the docker group, or use a rootless Docker/Podman socket.",
		})
	}

	// --- Scheduler --------------------------------------------------------
	if c.Scheduler.Interval <= 0 {
		add(Finding{
			Code: "scheduler.interval", Severity: SeverityError, Setting: "scheduler.interval",
			Title: "scheduler.interval must be positive",
			Fix:   `use a duration like "10s".`,
		})
	}
	if c.Scheduler.MaxCreatesPerTick <= 0 {
		add(Finding{
			Code: "scheduler.burst", Severity: SeverityError, Setting: "scheduler.max_creates_per_tick",
			Title: "max_creates_per_tick must be at least 1",
		})
	}
	if !c.Scheduler.DefaultRunnerLimits {
		add(Finding{
			Code: "scheduler.default_runner_limits_off", Severity: SeverityWarning, Setting: "scheduler.default_runner_limits",
			Title:  "runners whose pool sets no limits are given none",
			Detail: "a pool that leaves cpus or memory_mb unset is still charged one slot's share of its host, but its runners are created with no cgroup limit at all, so a host's worth of them can each take every core and all of the memory. That is how a host gets overwhelmed and its Docker daemon stops answering.",
			Fix:    "leave scheduler.default_runner_limits on, or set cpus and memory_mb on every pool.",
		})
	}
	if !c.Scheduler.HostThrottling {
		add(Finding{
			Code: "scheduler.host_throttling_off", Severity: SeverityWarning, Setting: "scheduler.host_throttling",
			Title:  "hosts are not throttled when they are overwhelmed",
			Detail: "the pressure holds still refuse new starts while a host's CPU or memory is acutely short, but nothing outlasts a sample: a host pushed past its size on and off keeps being let back in at full capacity, and the runners already on it are never slowed down.",
			Fix:    "leave scheduler.host_throttling on unless something outside Zoomies manages the hosts' load.",
		})
	}
	if c.Scheduler.AutoRerun {
		// Not a weakening of the security posture, but a standing permission
		// to spend somebody's money without asking, which is the other thing
		// this list exists to say out loud before a deployment discovers it.
		add(Finding{
			Code: "scheduler.auto_rerun_on", Severity: SeverityWarning, Setting: "scheduler.auto_rerun",
			Title:  "jobs this fleet breaks are re-run without asking",
			Detail: fmt.Sprintf("a job whose runner died under it is sent back to GitHub automatically, up to %d time(s) per workflow run. That spends GitHub minutes on this installation's account, and a job that got as far as running may have had side effects outside GitHub that its author expected to happen once.", c.Scheduler.AutoRerunLimit),
			Fix:    "turn scheduler.auto_rerun off to leave the re-run to the button on the job, or lower scheduler.auto_rerun_limit.",
		})
	}
	if c.Scheduler.AutoRerunLimit < 1 || c.Scheduler.AutoRerunLimit > 5 {
		// Bounded at both ends on purpose. Zero would make the setting above
		// do nothing while still reading as on, and a large number turns a
		// fault the fleet is causing every time into a bill.
		add(Finding{
			Code: "scheduler.auto_rerun_limit", Severity: SeverityError, Setting: "scheduler.auto_rerun_limit",
			Title: "auto_rerun_limit must be between 1 and 5",
			Fix:   "use 1 to re-run a broken run once, or turn scheduler.auto_rerun off.",
		})
	}
	// A runner's start is bounded three times over, and this is the only one
	// that gives up: the agent allows itself RunnerCreateBudget for the create,
	// the controller will not re-offer the task for longer still, and the
	// scheduler fails the row once it has been starting for this long. Set
	// below what the other two budget, the scheduler condemns runners the rest
	// of the system is patiently still making -- and then replaces them, so the
	// host pulls the same image twice over a link that was already the reason
	// the first pull was slow. A cold fleet is where that bites, and a cold
	// fleet is the one least able to absorb it.
	if c.Runners.EffectiveDockerWait() < 3*time.Minute {
		add(Finding{
			Code: "runners.docker_wait_short", Severity: SeverityWarning, Setting: "runners.docker_wait",
			Title:  "Docker startup has less than the recommended three minutes",
			Detail: "a quota-limited DinD daemon can need longer to initialise on a loaded host. A short readiness wait can replace runners that were still making progress.",
			Fix:    "set runners.docker_wait to 3m, or keep the shorter wait if it is deliberate and tested on these hosts.",
		})
	}
	start := RunnerCreateBudget + c.Runners.EffectiveDockerWait()
	if c.Scheduler.ProvisionTimeout > 0 && c.Scheduler.ProvisionTimeout <= start {
		add(Finding{
			Code: "scheduler.provision_timeout_short", Severity: SeverityWarning, Setting: "scheduler.provision_timeout",
			Title: fmt.Sprintf("runners are failed after %s but may legitimately take %s to start",
				c.Scheduler.ProvisionTimeout, start),
			Detail: fmt.Sprintf("an agent gives itself %s for a create, because a cold image pull on a slow link "+
				"is minutes rather than seconds, and a runner on a pool that provides Docker then waits up to %s "+
				"for that daemon before it registers. A provision timeout inside that fails runners that are "+
				"still coming up, and the replacement pulls the same image over the same link.",
				RunnerCreateBudget, c.Runners.EffectiveDockerWait()),
			Fix: "set scheduler.provision_timeout above the two together, such as 20m.",
		})
	}
	if c.Scheduler.MaxRunnerLifetime > 0 && c.Scheduler.MaxRunnerLifetime < 10*time.Minute {
		add(Finding{
			Code: "scheduler.lifetime_short", Severity: SeverityWarning, Setting: "scheduler.max_runner_lifetime",
			Title:  fmt.Sprintf("idle runners are recycled after %s", c.Scheduler.MaxRunnerLifetime),
			Detail: "a runner that old is drained the moment it is not busy, so a pool that keeps a minimum re-registers its runners that often and any warm cache goes with them. Running jobs are never interrupted by it.",
			Fix:    "keep scheduler.max_runner_lifetime to hours, or clear it for no limit.",
		})
	}

	// --- Runner images ----------------------------------------------------
	if c.GitHub.ToolchainScanInterval < 0 {
		add(Finding{
			Code: "github.toolchain_scan_negative", Severity: SeverityError, Setting: "github.toolchain_scan_interval",
			Title: "github.toolchain_scan_interval cannot be negative",
			Fix:   "use a duration such as 24h, or 0 to scan only when asked.",
		})
	}
	if c.GitHub.ToolchainScanInterval > 0 && c.GitHub.ToolchainScanInterval < time.Hour {
		add(Finding{
			Code: "github.toolchain_scan_too_fast", Severity: SeverityWarning, Setting: "github.toolchain_scan_interval",
			Title:  fmt.Sprintf("every workflow in every repository is read every %s", c.GitHub.ToolchainScanInterval),
			Detail: "each scan spends GitHub API calls from the quota the scheduler uses to find queued jobs, and workflows change far less often than that.",
			Fix:    "use 24h, or scan by hand after changing workflows.",
		})
	}
	if c.Images.RefreshInterval < 0 {
		add(Finding{
			Code: "images.refresh_negative", Severity: SeverityError, Setting: "images.refresh_interval",
			Title: "images.refresh_interval cannot be negative",
			Fix:   `use a duration like "1h", or 0 to leave images alone.`,
		})
	}
	if c.Images.RefreshInterval > 0 && c.Images.RefreshInterval < 5*time.Minute {
		add(Finding{
			Code: "images.refresh_too_fast", Severity: SeverityWarning, Setting: "images.refresh_interval",
			Title:  fmt.Sprintf("every pool's image is checked every %s", c.Images.RefreshInterval),
			Detail: "each pass is a registry round trip for every pool on every host that can run it, and an image changes no more often than it is built.",
			Fix:    `use "15m" or more; the default hour is soon enough for a tag that moves on a merge.`,
		})
	}
	if c.Images.RefreshInterval == 0 {
		add(Finding{
			Code: "images.refresh_off", Severity: SeverityInfo, Setting: "images.refresh_interval",
			Title:  "runner images are never refreshed",
			Detail: "a pool that names a moving tag keeps whatever its hosts pulled the first time; prewarming still runs when a pool is created or edited.",
			Fix:    `set images.refresh_interval to "1h" unless this fleet is air-gapped or pins every pool to a digest.`,
		})
	}

	// --- Retention --------------------------------------------------------
	if c.Retention.Audit != 0 {
		add(Finding{
			Code: "retention.audit_renamed", Severity: SeverityInfo, Setting: "retention.audit",
			Title:  "retention.audit is now retention.scaling_events",
			Detail: "the value is honoured as the scaling-history window, which is all it ever bounded: audit rows are never pruned, whatever this setting says.",
			Fix:    "rename the key to retention.scaling_events (ZOOMIES_RETENTION_SCALING_EVENTS) and remove retention.audit.",
		})
	}

	// --- Update check -----------------------------------------------------
	if c.Updates.CheckInterval < 0 {
		add(Finding{
			Code: "updates.interval_negative", Severity: SeverityError, Setting: "updates.check_interval",
			Title: "updates.check_interval cannot be negative",
			Fix:   `use a duration like "24h", or 0 to never ask.`,
		})
	}
	if c.Updates.CheckInterval > 0 && c.Updates.CheckInterval < time.Hour {
		add(Finding{
			Code: "updates.interval_too_fast", Severity: SeverityWarning, Setting: "updates.check_interval",
			Title:  fmt.Sprintf("asking github.com for the current release every %s", c.Updates.CheckInterval),
			Detail: "releases are published far less often than that, and the check is unauthenticated, so it draws on a rate limit shared by everything else leaving this address.",
			Fix:    `use "24h", which still notices a release within a working day.`,
		})
	}

	// --- External capacity provisioner -----------------------------------
	if c.CapacityDemand.DestinationURL != "" {
		u, err := url.Parse(c.CapacityDemand.DestinationURL)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			add(Finding{Code: "capacity_demand.url", Severity: SeverityError, Setting: "capacity_demand.destination_url", Title: "capacity-demand destination is not an absolute HTTP URL"})
		}
		if c.CapacityDemand.SigningSecret == "" {
			add(Finding{Code: "capacity_demand.secret", Severity: SeverityError, Setting: "capacity_demand.signing_secret", Title: "capacity-demand signing secret is empty", Fix: "set a high-entropy shared secret."})
		}
		if c.CapacityDemand.Cooldown <= 0 {
			add(Finding{Code: "capacity_demand.cooldown", Severity: SeverityError, Setting: "capacity_demand.cooldown", Title: "capacity-demand cooldown must be positive"})
		}
		if c.CapacityDemand.Timeout <= 0 {
			add(Finding{Code: "capacity_demand.timeout", Severity: SeverityError, Setting: "capacity_demand.timeout", Title: "capacity-demand timeout must be positive"})
		}
	}

	// --- Outbound addresses ----------------------------------------------
	// A warning here, never an error. The value in front of the validator
	// came from whoever runs the process -- the file, the environment, or a
	// row an earlier write already passed -- and an upgrade must not stop an
	// install that has been talking to a LAN Enterprise Server for a year.
	// The threat is somebody with settings rights writing one of these
	// through the API, and the API refuses that at the write.
	for _, o := range c.OutboundURLs() {
		if f := CheckOutboundURL(o.Setting, o.Value, c.Security.AllowPrivateEgress); f != nil {
			f.Severity = SeverityWarning
			add(*f)
		}
	}

	// --- Backups ----------------------------------------------------------
	c.validateBackupRemotes(add)

	// --- Metrics and logging ---------------------------------------------
	// --- Limits -------------------------------------------------------------
	// A ceiling guards an instance many people reach against one caller spending what
	// every other caller needs. On a controller only this machine can reach
	// there is no other caller, so a ceiling protects nothing and can only
	// refuse the operator -- which is worth saying before it does.
	for _, l := range []struct {
		key   string
		value int
	}{
		{"limits.hosts", c.Limits.Hosts},
		{"limits.pools", c.Limits.Pools},
		{"limits.runners", c.Limits.Runners},
		{"limits.join_tokens", c.Limits.JoinTokens},
		{"limits.event_subscribers", c.Limits.EventSubscribers},
	} {
		switch {
		case l.value < 0:
			add(Finding{
				Code: "limits.negative", Severity: SeverityError, Setting: l.key,
				Title: fmt.Sprintf("%s is %d, and a ceiling cannot be negative", l.key, l.value),
				Fix:   fmt.Sprintf("set %s to 0 for no ceiling, or to the most this instance should hold.", l.key),
			})
		case l.value > 0 && !c.LikelyReachable():
			add(Finding{
				Code: "limits.loopback", Severity: SeverityWarning, Setting: l.key,
				Title:  fmt.Sprintf("%s is set on a controller only this machine can reach", l.key),
				Detail: "a ceiling protects an instance many people reach from one caller spending what the others need; with nobody else able to reach this one, it can only refuse you.",
				Fix:    fmt.Sprintf("set %s back to 0, or leave it if this controller will be reachable once it is behind a proxy or given an external URL.", l.key),
			})
		}
	}

	if c.Metrics.Enabled && c.Metrics.Public {
		add(Finding{
			Code: "metrics.public", Severity: SeverityWarning, Setting: "metrics.public",
			Title:  "the metrics endpoint is unauthenticated",
			Detail: "repository names, workflow names and pool names appear in metric labels.",
			Fix:    "leave metrics.public off and give Prometheus a viewer API token.",
		})
	}
	// The status projection is name-free by construction, so what public
	// discloses is not who the fleet is but how it is doing: that it exists,
	// its release, whether it is blocked, and roughly how busy it is. That is
	// the point for a developer with no account and a fact a stranger could
	// use, so it is said out loud rather than left as a quiet toggle.
	switch c.Status.Mode {
	case StatusOff, StatusAuthenticated:
	case StatusPublic:
		if public && c.Server.TLS.Mode == TLSOff {
			// The page is harmless over plain HTTP; the listener it opens on a
			// public address is not, and "public" is the setting that asks
			// strangers to come and find it.
			add(Finding{
				Code: "status.public_no_tls", Severity: SeverityError, Setting: "status.mode",
				Title: fmt.Sprintf("the fleet status is public on %s without TLS", c.Server.Bind),
				Detail: "status.mode public invites people with no account to a listener that is not on loopback and does not terminate TLS, " +
					"so every sign-in on the same address would cross the network in the clear.",
				Fix: "set server.tls.mode to self-signed or files, bind server.bind to 127.0.0.1 behind a reverse proxy that terminates TLS, " +
					"or set status.mode to authenticated.",
			})
			break
		}
		add(Finding{
			Code: "status.public", Severity: SeverityWarning, Setting: "status.mode",
			Title: "the fleet status is readable without an account",
			Detail: "anyone who can reach this controller can read /status, /status.svg and /api/v1/status: " +
				"the fleet's state, the release it runs, banded counts of queued and running jobs, the rounded queue wait " +
				"and the codes of the fleet's current problems. No pool, host, repository, runner or job name is in it.",
			Fix: "set status.mode to authenticated if only people with an account should see it, or off to serve none of it.",
		})
	default:
		add(Finding{
			Code: "status.mode", Severity: SeverityError, Setting: "status.mode",
			Title: fmt.Sprintf("%q is not a status mode", c.Status.Mode),
			Fix:   "use off, authenticated or public.",
		})
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add(Finding{
			Code: "log.level", Severity: SeverityError, Setting: "log.level",
			Title: fmt.Sprintf("%q is not a log level", c.Log.Level),
			Fix:   "use debug, info, warn or error.",
		})
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add(Finding{
			Code: "log.format", Severity: SeverityError, Setting: "log.format",
			Title: fmt.Sprintf("%q is not a log format", c.Log.Format),
			Fix:   "use json or text.",
		})
	}
	// The settings page and the environment refuse a layout that is not one
	// of the two before it is set; a file is the one way a wrong one arrives,
	// and it is caught here for the same reason a wrong log level is.
	for key, value := range map[string]string{
		"ui.capacity_map.overview_layout": c.UI.CapacityMap.OverviewLayout,
		"ui.capacity_map.hosts_layout":    c.UI.CapacityMap.HostsLayout,
	} {
		if !slices.Contains(CapacityLayouts, value) {
			add(Finding{
				Code: "ui.capacity_map.layout", Severity: SeverityError, Setting: key,
				Title: fmt.Sprintf("%q is not a capacity map layout", value),
				Fix:   "use overlay for every host on one chart, or split for a chart per host.",
			})
		}
	}
	if c.Log.Level == "debug" {
		add(Finding{
			Code: "log.debug", Severity: SeverityInfo, Setting: "log.level",
			Title:  "debug logging is on",
			Detail: "request paths and GitHub API interactions are logged in full. Secrets are redacted, but repository and workflow names are not.",
		})
	}

	// --- Infrastructure providers ---
	//
	// Every finding below is silent while provider.enabled is false, which is
	// the default. A deployment that has not asked to rent machines should not
	// be told how to bound something it is not doing, and a warning nobody can
	// act on is what teaches people to ignore the drawer.
	if c.Provider.Enabled {
		if c.Provider.Interval <= 0 {
			add(Finding{
				Code: "provider.interval", Severity: SeverityError, Setting: "provider.interval",
				Title: "the provider interval must be positive",
				Fix:   "set provider.interval to how often machines should be reconciled, such as 30s.",
			})
		}
		for _, t := range []struct {
			setting string
			value   time.Duration
		}{
			{"provider.call_timeout", c.Provider.CallTimeout},
			{"provider.create_timeout", c.Provider.CreateTimeout},
			{"provider.bootstrap_timeout", c.Provider.BootstrapTimeout},
			{"provider.delete_timeout", c.Provider.DeleteTimeout},
			{"provider.ambiguity_timeout", c.Provider.AmbiguityTimeout},
		} {
			if t.value <= 0 {
				add(Finding{
					Code: "provider.timeouts", Severity: SeverityError, Setting: t.setting,
					Title: fmt.Sprintf("%s must be positive", t.setting),
					Fix:   "give every provider operation a bound; an unbounded one wedges the machine loop.",
				})
			}
		}
		if c.Provider.AmbiguityTimeout > 0 && c.Provider.CreateTimeout > 0 &&
			c.Provider.AmbiguityTimeout <= c.Provider.CreateTimeout {
			add(Finding{
				Code: "provider.timeouts", Severity: SeverityError, Setting: "provider.ambiguity_timeout",
				Title: "provider.ambiguity_timeout is not longer than provider.create_timeout",
				Detail: "an operation whose outcome is unknown is given this long to be resolved by looking. " +
					"Set below the create timeout, a machine that is merely still being built is treated as " +
					"one nobody can account for, and quarantined while it is working.",
				Fix: "set provider.ambiguity_timeout comfortably longer than provider.create_timeout.",
			})
		}
		if c.Provider.EnrolTimeout <= 0 || c.Provider.EnrolTimeout < HostUnhealthyAfter {
			add(Finding{
				Code: "provider.enrol_timeout", Severity: SeverityError, Setting: "provider.enrol_timeout",
				Title: "provider.enrol_timeout is shorter than the silence that makes a host unhealthy",
				Detail: fmt.Sprintf("a machine that has joined and gone quiet for %s is still only unhealthy, "+
					"so giving enrolment less than that gives up on machines that arrived.", HostUnhealthyAfter),
				Fix: "set provider.enrol_timeout to how long a machine may take to boot and join, such as 15m.",
			})
		}
		if c.Provider.MaxMachines <= 0 {
			add(Finding{
				Code: "provider.no_ceiling", Severity: SeverityWarning, Setting: "provider.max_machines",
				Title: "providers are enabled but no machine may be rented",
				Detail: "a maximum of none is none, as it is for a pool's max_runners, so nothing will be " +
					"created however much work queues. The number is deliberately not optional: it is the " +
					"one setting that decides the size of an invoice.",
				Fix: "set provider.max_machines to the most machines you are willing to pay for at once.",
			})
		}
		if c.Provider.Paused {
			add(Finding{
				Code: "provider.paused", Severity: SeverityInfo, Setting: "provider.paused",
				Title:  "new machines are paused by configuration",
				Detail: "existing machines still drain, delete, recover and have their ownership verified. Only creation is held.",
				Fix:    "set provider.paused to false to let the fleet rent machines again.",
			})
		}
		// Against RunnersLostAfter, not HostUnhealthyAfter. Ninety seconds of
		// silence only stops new runners being placed on a host; the runners
		// already on it are not given up until five minutes, and a machine
		// deleted before then is deleted out from under a job that is still
		// running -- which is the outcome this grace exists to prevent, so the
		// grace has to be measured against the judgement that frees them.
		if c.Provider.DeleteGrace > 0 && c.Provider.DeleteGrace <= RunnersLostAfter {
			add(Finding{
				Code: "provider.delete_grace_short", Severity: SeverityWarning, Setting: "provider.delete_grace",
				Title: "provider.delete_grace is no longer than the silence that loses a host's runners",
				Detail: fmt.Sprintf("a quiet host is only unhealthy after %s; its runners are not given up until "+
					"%s, and until they are the fleet still believes they are running jobs. A grace at or below "+
					"that destroys the machine for a network blip -- taking the job it was running with it.",
					HostUnhealthyAfter, RunnersLostAfter),
				Fix: "set provider.delete_grace well above that, such as 10m.",
			})
		}
		// A quiet host is ruled out for every pool, so a machine whose host has
		// merely gone silent looks idle long before anyone has decided it is
		// gone. An idle timeout inside that window drains machines for a
		// network blip.
		if c.Provider.IdleTimeout > 0 && c.Provider.IdleTimeout <= RunnersLostAfter {
			add(Finding{
				Code: "provider.idle_timeout_short", Severity: SeverityWarning, Setting: "provider.idle_timeout",
				Title: "provider.idle_timeout is no longer than the silence that loses a host's runners",
				Detail: fmt.Sprintf("a host that has gone quiet carries no runner anything will place, so it reads "+
					"as idle from the moment it falls silent. Set at or below the %s its runners are given, a "+
					"machine is drained for a blip rather than for being unwanted.",
					RunnersLostAfter),
				Fix: "set provider.idle_timeout above that, such as 15m.",
			})
		}
		if c.Provider.ScaleDownCooldown > 0 && c.Provider.IdleTimeout > 0 &&
			c.Provider.ScaleDownCooldown < c.Provider.IdleTimeout {
			add(Finding{
				Code: "provider.scale_down_fast", Severity: SeverityWarning, Setting: "provider.scale_down_cooldown",
				Title: "machines are removed sooner than one idle period",
				Detail: "a fleet that buys a machine on every burst and removes it in the quiet between two " +
					"of them pays the creation cost repeatedly and is never warm when the work arrives.",
				Fix: "set provider.scale_down_cooldown to at least provider.idle_timeout.",
			})
		}
	}

	// Stamp the layer on every finding that names a setting, rather than
	// asking eighty-odd constructions above to remember. Which layer set a
	// value is a property of the assembled configuration, not of the rule that
	// judged it, and nothing above here has any business knowing it.
	for i := range fs {
		if fs[i].Setting != "" {
			fs[i].Source = c.Source(fs[i].Setting)
			fs[i].Undo = undoFor(fs[i].Source, fs[i].Setting)
		}
	}
	return fs
}

// ValidateStrict is Validate plus the error check, for callers that only want
// to know whether they may start.
func (c *Config) ValidateStrict() (Findings, error) {
	fs := c.Validate()
	return fs, fs.Err()
}

// validateBackupRemotes checks the offsite destinations.
//
// The two findings that are not errors are the point of it. A remote with no
// passphrase puts the whole fleet -- every repository name, every job, every
// sealed credential -- in somebody else's bucket as a file anyone holding the
// bucket can open; a remote reached over plain HTTP hands the credentials that
// open it to the network on the way. Neither stops a fleet that means it, and
// neither is allowed to be silent.
func (c *Config) validateBackupRemotes(add func(Finding)) {
	seen := map[string]int{}
	for i, r := range c.Backup.Remotes {
		named := r.Name
		if named == "" {
			named = fmt.Sprintf("the remote at position %d", i+1)
		}
		if n, dup := seen[r.Name]; dup {
			add(Finding{
				Code: "backup.remote_duplicate", Severity: SeverityError, Setting: "backup.remotes",
				Title:  fmt.Sprintf("two backup remotes are both called %q", r.Name),
				Detail: fmt.Sprintf("the one at position %d and the one at position %d. The name is how the Backups tab, the log and the problems drawer tell them apart, so one of them would be unaddressable.", n+1, i+1),
				Fix:    "give each remote its own name.",
			})
		}
		seen[r.Name] = i
		if !ValidRemoteName(r.Name) {
			add(Finding{
				Code: "backup.remote_name", Severity: SeverityError, Setting: "backup.remotes",
				Title:  fmt.Sprintf("%q is not a usable name for a backup remote", r.Name),
				Detail: "the name is a path component in the API and a word in a log line.",
				Fix:    "use lower-case letters, digits and dashes, such as offsite or s3-frankfurt, and not a word the API already uses there (" + strings.Join(ReservedRemoteNames, ", ") + ").",
			})
		}
		if r.Disabled {
			continue
		}

		endpoint := strings.TrimSpace(r.Endpoint)
		bucket := strings.TrimSpace(r.Bucket)
		if endpoint == "" || bucket == "" {
			// A half-written remote is the dangerous case: it looks configured
			// and it copies nothing, so the fleet believes it has an offsite
			// backup it has never had.
			missing := "an endpoint"
			switch {
			case endpoint != "":
				missing = "a bucket"
			case bucket != "":
				missing = "an endpoint"
			default:
				missing = "an endpoint and a bucket"
			}
			add(Finding{
				Code: "backup.remote_incomplete", Severity: SeverityError, Setting: "backup.remotes",
				Title:  fmt.Sprintf("the backup remote %s has no %s", named, missing),
				Detail: "nothing would be copied to it, and nothing would say so.",
				Fix:    "give it an endpoint and a bucket, or set disabled: true until it is ready.",
			})
			continue
		}

		u, err := url.Parse(endpoint)
		egress := CheckOutboundURL("backup.remotes", endpoint, c.Security.AllowPrivateEgress)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			add(Finding{
				Code: "backup.remote_endpoint", Severity: SeverityError, Setting: "backup.remotes",
				Title: fmt.Sprintf("the backup remote %s has an endpoint that is not an HTTP URL: %q", named, endpoint),
				Fix:   "write the service's URL, such as https://s3.eu-west-2.amazonaws.com or http://minio:9000.",
			})
		case egress != nil:
			// Named for the remote, because backup.remotes is a list and the
			// key alone does not say which entry to change; a warning, for the
			// reason the outbound settings above give.
			egress.Severity = SeverityWarning
			egress.Title = fmt.Sprintf("the backup remote %s points at %s", named, strings.TrimPrefix(egress.Title, "backup.remotes points at "))
			add(*egress)
		case u.Scheme == "http" && !loopbackHost(u.Hostname()):
			add(Finding{
				Code: "backup.remote_insecure", Severity: SeverityWarning, Setting: "backup.remotes",
				Title:  fmt.Sprintf("the backup remote %s is reached over plain HTTP", named),
				Detail: "the access key, the signature and the backup itself cross the network in the clear, so anyone on the path can both read the fleet and write to the bucket afterwards.",
				Fix:    "use https:// unless the endpoint is on this host or a network you own end to end.",
			})
		}

		if strings.TrimSpace(r.AccessKeyID) == "" || strings.TrimSpace(r.SecretAccessKey) == "" {
			add(Finding{
				Code: "backup.remote_credentials", Severity: SeverityError, Setting: "backup.remotes",
				Title:  fmt.Sprintf("the backup remote %s has no credentials", named),
				Detail: "every request to it would be refused, and the copies would pile up unsent.",
				Fix:    fmt.Sprintf("set its access_key_id and secret_access_key, or ZOOMIES_BACKUP_REMOTE_%d_ACCESS_KEY_ID and ZOOMIES_BACKUP_REMOTE_%d_SECRET_ACCESS_KEY.", i+1, i+1),
			})
		}
		if !r.Encrypted() {
			add(Finding{
				Code: "backup.remote_plaintext", Severity: SeverityWarning, Setting: "backup.remotes",
				Title:  fmt.Sprintf("the backup remote %s is sent the backup unencrypted", named),
				Detail: "a backup is the whole fleet: every repository and job it has seen, every account, and the sealed GitHub App credentials. In " + r.Where() + " it is a file anyone who can read the bucket can open.",
				Fix:    "set a passphrase on the remote — the archive is then sealed with argon2id and AES-256-GCM before it leaves this host — and keep it wherever you keep the encryption key. Nothing here can recover it.",
			})
		}
		if r.Passphrase != "" && len(r.Passphrase) < MinBackupPassphrase {
			add(Finding{
				Code: "backup.remote_passphrase_short", Severity: SeverityWarning, Setting: "backup.remotes",
				Title:  fmt.Sprintf("the backup remote %s has a passphrase of %d characters", named, len(r.Passphrase)),
				Detail: fmt.Sprintf("the archive is only as private as this, and the Backups tab refuses anything shorter than %d for the same download.", MinBackupPassphrase),
				Fix:    "use a long random passphrase; it is typed once, into a file.",
			})
		}
	}

	if c.Backup.Interval > 0 && len(c.EnabledBackupRemotes()) == 0 {
		// Raised from what the file says, because that is all this package
		// knows. A destination an administrator added on the Backups page is
		// a database row, so the settings API drops this finding when the
		// fleet has one -- see NoRemoteFinding, which names the code once for
		// both halves.
		add(Finding{
			Code: "backup.no_remote", Severity: SeverityInfo, Setting: "backup.remotes",
			Title:  "backups are taken but never leave this host",
			Detail: "the schedule keeps copies beside the database, which is a backup against a mistake and not against the disk, the machine or the datacentre.",
			Fix:    "add an S3-compatible destination on the Backups page, or under backup.remotes here — or keep shipping the directory yourself. The point is that one of the three is somebody's job.",
		})
	}
}

// NoRemoteFinding is the code for "backups never leave this host".
//
// It is named because two packages have to agree about it: this one raises it
// from the configuration file, and the settings API drops it when the fleet
// has a destination stored in its database -- which this package cannot see,
// and which would otherwise make the finding tell an operator to do a thing
// they have already done.
//
// The Finding above spells the code out rather than using this constant, so
// that the docs test which parses `Code:` literals can still see it; a test
// holds the two together.
const NoRemoteFinding = "backup.no_remote"

// MinBackupPassphrase is the shortest passphrase worth calling one. The API
// refuses an encrypted download below it, and the validator says so about a
// remote's.
const MinBackupPassphrase = 8

// ReservedRemoteNames are the words the API already uses where a remote's name
// goes, so a destination called one of them would be a destination no route
// could address.
var ReservedRemoteNames = []string{"check"}

// ValidRemoteName is the shape a remote's name may take: it is a path
// component in the API and a word in a log line, so it is kept to the
// characters that are both, and to names no route has already taken.
//
// It is exported because the destinations stored in the database are held to
// the same rule. A name that worked in one place and not the other would be a
// trap, and the two lists end up merged.
func ValidRemoteName(name string) bool {
	if name == "" || slices.Contains(ReservedRemoteNames, name) {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// checkExtraCA refuses an agent.extra_ca_file the runners could not use. The
// file is bind-mounted into every container runner, so a relative path, a
// missing file or one that is not a certificate would otherwise surface as
// every create failing, or as every job failing TLS with nothing to say why
// -- which, behind the proxy this setting exists for, is exactly the symptom
// the operator was trying to fix.
func checkExtraCA(p string) (Finding, bool) {
	refuse := func(title, detail string) (Finding, bool) {
		return Finding{
			Code: "agent.extra_ca_invalid", Severity: SeverityError, Setting: "agent.extra_ca_file",
			Title: title, Detail: detail,
			Fix: "set agent.extra_ca_file to the absolute path of a PEM file on this host holding your organisation's root CA certificate, or clear it.",
		}, false
	}
	if !filepath.IsAbs(p) {
		return refuse(fmt.Sprintf("agent.extra_ca_file %q is not an absolute path", p),
			"the file is bind-mounted into each runner container, and the container engine only mounts an absolute host path.")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return refuse(fmt.Sprintf("cannot read agent.extra_ca_file %s", p), err.Error())
	}
	var n int
	for rest := b; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return refuse(fmt.Sprintf("agent.extra_ca_file %s holds a certificate that does not parse", p), err.Error())
		}
		n++
	}
	if n == 0 {
		return refuse(fmt.Sprintf("agent.extra_ca_file %s holds no PEM certificate", p),
			"the file needs at least one -----BEGIN CERTIFICATE----- block; a DER file converts with `openssl x509 -inform der -in ca.der -out ca.pem`.")
	}
	return Finding{}, true
}
