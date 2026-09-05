package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	for _, cidr := range c.Server.TrustedProxies {
		if strings.TrimSpace(cidr) == TrustedProxyCloudflare {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			if net.ParseIP(cidr) == nil {
				add(Finding{
					Code: "proxy.bad_cidr", Severity: SeverityError, Setting: "server.trusted_proxies",
					Title: fmt.Sprintf("%q is not an IP address or CIDR", cidr),
					Fix:   `use a form like "10.0.0.0/8" or "192.168.1.5", or the word "cloudflare".`,
				})
			}
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
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
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
	if c.Security.EncryptionKey != "" && c.path != "" {
		add(Finding{
			Code: "crypto.key_in_config", Severity: SeverityWarning, Setting: "security.encryption_key",
			Title:  "the encryption key is written in the config file",
			Detail: "anything that can read " + c.path + " -- backups, configuration management, a support bundle -- can decrypt every stored secret.",
			Fix:    "move it to security.encryption_key_file (mode 0600) or the ZOOMIES_ENCRYPTION_KEY environment variable.",
		})
	}

	// --- Authentication ---------------------------------------------------
	if c.Security.DisableAuth {
		sev := SeverityError
		fix := "remove security.disable_auth."
		if !public {
			sev = SeverityWarning
			fix = "acceptable for local development only; never set this on a host others can reach."
		}
		add(Finding{
			Code: "auth.disabled", Severity: sev, Setting: "security.disable_auth",
			Title: "authentication is disabled",
			Detail: "every request is treated as an administrator: anyone who can reach the " +
				"listener can create pools, read the audit log and drain the fleet." +
				map[bool]string{true: " The listener is not on loopback, so this is refused.", false: ""}[public],
			Fix: fix,
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
	if c.Agent.Embedded || c.Agent.ControllerURL != "" {
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
		if os.Geteuid() == 0 && c.Agent.Backend == "process" {
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
		if c.Agent.WorkDir == "" {
			add(Finding{
				Code: "agent.workdir", Severity: SeverityError, Setting: "agent.work_dir",
				Title: "no work directory",
				Fix:   "set agent.work_dir, e.g. /var/lib/zoomies/work.",
			})
		}
	}
	if !c.Agent.Embedded && c.Agent.ControllerURL == "" {
		add(Finding{
			Code: "agent.none", Severity: SeverityInfo, Setting: "agent.embedded",
			Title:  "no embedded agent",
			Detail: "this controller cannot run runners itself; at least one standalone agent must join it.",
			Fix:    "run `zoomies agent join <controller-url> --token <join-token>` on a host, or set agent.embedded to true.",
		})
	}
	if os.Geteuid() == 0 && (c.Agent.Backend == "docker" || c.Agent.Backend == "podman") {
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
	if c.Scheduler.MaxRunnerLifetime > 0 && c.Scheduler.MaxRunnerLifetime < 10*time.Minute {
		add(Finding{
			Code: "scheduler.lifetime_short", Severity: SeverityWarning, Setting: "scheduler.max_runner_lifetime",
			Title:  fmt.Sprintf("runners are force-drained after %s", c.Scheduler.MaxRunnerLifetime),
			Detail: "jobs longer than that will never complete; the runner is drained while they run.",
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

	// --- Metrics and logging ---------------------------------------------
	if c.Metrics.Enabled && c.Metrics.Public {
		add(Finding{
			Code: "metrics.public", Severity: SeverityWarning, Setting: "metrics.public",
			Title:  "the metrics endpoint is unauthenticated",
			Detail: "repository names, workflow names and pool names appear in metric labels.",
			Fix:    "leave metrics.public off and give Prometheus a viewer API token.",
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
	if c.Log.Level == "debug" {
		add(Finding{
			Code: "log.debug", Severity: SeverityInfo, Setting: "log.level",
			Title:  "debug logging is on",
			Detail: "request paths and GitHub API interactions are logged in full. Secrets are redacted, but repository and workflow names are not.",
		})
	}

	return fs
}

// ValidateStrict is Validate plus the error check, for callers that only want
// to know whether they may start.
func (c *Config) ValidateStrict() (Findings, error) {
	fs := c.Validate()
	return fs, fs.Err()
}
