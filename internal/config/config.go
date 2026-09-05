// Package config loads and validates zoomies.yaml plus its ZOOMIES_* env
// overrides.
//
// Validation has two outputs. Errors stop the process with a message that says
// what to change. Warnings do not stop anything, but every one of them names a
// setting that weakens the default security posture; they are logged at startup
// and surfaced in the UI's problems drawer, so a dangerous toggle is never
// silently in effect.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the complete on-disk configuration.
type Config struct {
	Server         Server         `yaml:"server"`
	Database       Database       `yaml:"database"`
	Security       Security       `yaml:"security"`
	GitHub         GitHub         `yaml:"github"`
	Agent          Agent          `yaml:"agent"`
	Scheduler      Scheduler      `yaml:"scheduler"`
	Log            Log            `yaml:"log"`
	OIDC           OIDC           `yaml:"oidc"`
	Metrics        Metrics        `yaml:"metrics"`
	Retention      Retention      `yaml:"retention"`
	CapacityDemand CapacityDemand `yaml:"capacity_demand"`

	// path records where this config was read from, for error messages.
	path string `yaml:"-"`
}

// CapacityDemand publishes signed requests for host capacity to an external
// provisioner. An empty DestinationURL disables the integration.
type CapacityDemand struct {
	DestinationURL string        `yaml:"destination_url"`
	SigningSecret  string        `yaml:"signing_secret"`
	Cooldown       time.Duration `yaml:"cooldown"`
	Timeout        time.Duration `yaml:"timeout"`
	Pools          []string      `yaml:"pools"`
}

// Server controls the HTTP listener.
type Server struct {
	// Bind defaults to 127.0.0.1:8080. Binding to 0.0.0.0 without TLS is a
	// warning, not an error, because a reverse proxy in front is legitimate.
	Bind string `yaml:"bind"`
	// ExternalURL is how GitHub and browsers reach this controller. It is
	// required once webhooks are in play, since it forms the webhook URL.
	ExternalURL string `yaml:"external_url"`
	TLS         TLS    `yaml:"tls"`
	// TrustedProxies lists CIDRs whose X-Forwarded-For header is believed.
	// Empty means client IPs come from the socket, which is the safe default.
	// The word "cloudflare" expands to Cloudflare's published ranges.
	TrustedProxies []string      `yaml:"trusted_proxies"`
	ReadTimeout    time.Duration `yaml:"read_timeout"`
	WriteTimeout   time.Duration `yaml:"write_timeout"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	// AllowedOrigins restricts browser origins for state-changing requests.
	// Empty means same-origin only, which is what the embedded UI needs.
	AllowedOrigins []string `yaml:"allowed_origins"`
	// AllowIndexing invites search engines into the UI. Off by default: a
	// controller is somebody's infrastructure rather than somebody's website,
	// so robots.txt declines crawling until an operator says otherwise.
	AllowIndexing bool `yaml:"allow_indexing"`
}

// TLSMode selects how the listener terminates TLS.
type TLSMode string

const (
	// TLSOff serves plain HTTP. Correct behind a reverse proxy; a warning
	// otherwise.
	TLSOff TLSMode = "off"
	// TLSSelfSigned generates and persists a self-signed certificate.
	TLSSelfSigned TLSMode = "self-signed"
	// TLSFiles uses an operator-provided certificate and key.
	TLSFiles TLSMode = "files"
)

// TLS configures transport security for the controller listener.
type TLS struct {
	Mode     TLSMode `yaml:"mode"`
	CertFile string  `yaml:"cert_file"`
	KeyFile  string  `yaml:"key_file"`
	// Hosts are the names baked into a generated self-signed certificate.
	Hosts []string `yaml:"hosts"`
}

// Database points at the SQLite file.
type Database struct {
	Path string `yaml:"path"`
}

// Security holds instance-wide security settings.
type Security struct {
	// EncryptionKey is a base64 or hex 32-byte key. Prefer EncryptionKeyFile
	// or the ZOOMIES_ENCRYPTION_KEY environment variable; a key written into
	// zoomies.yaml is a key in your configuration management system.
	EncryptionKey     string `yaml:"encryption_key"`
	EncryptionKeyFile string `yaml:"encryption_key_file"`
	// SessionTTL is how long a browser login lasts.
	SessionTTL time.Duration `yaml:"session_ttl"`
	// CookieSecure forces the Secure attribute on session cookies. It is
	// derived from the external URL when unset.
	CookieSecure *bool `yaml:"cookie_secure"`
	// DisableAuth removes all authentication. It exists for local development
	// only and is refused unless the listener is on loopback.
	DisableAuth bool `yaml:"disable_auth"`
	// RateLimitLogins caps password attempts per source address per minute.
	RateLimitLogins int `yaml:"rate_limit_logins"`
}

// GitHub configures the GitHub integration.
type GitHub struct {
	// APIBaseURL is https://api.github.com for github.com, or
	// https://ghes.example.com/api/v3 for GitHub Enterprise Server.
	APIBaseURL    string `yaml:"api_base_url"`
	UploadBaseURL string `yaml:"upload_base_url"`
	WebhookPath   string `yaml:"webhook_path"`
	// PollInterval drives the fallback poller that lists queued jobs when
	// webhooks cannot reach this controller.
	PollInterval time.Duration `yaml:"poll_interval"`
	// PollFallback enables that poller. It is on by default: a controller that
	// silently stops scaling because a webhook was misconfigured is worse than
	// a few extra API calls.
	PollFallback bool `yaml:"poll_fallback"`
	// RunnerImage is the default container image for new pools.
	RunnerImage string `yaml:"runner_image"`
	// RunnerVersion pins the actions/runner release; empty tracks the image.
	RunnerVersion string `yaml:"runner_version"`
}

// Agent configures the runner-executing half of the system.
type Agent struct {
	// Embedded runs an agent inside the controller process, so the single-VM
	// case needs exactly one process.
	Embedded bool   `yaml:"embedded"`
	Name     string `yaml:"name"`
	Capacity int    `yaml:"capacity"`
	Backend  string `yaml:"backend"`
	// DockerHost is a docker/podman socket URL. Empty means autodetect, which
	// prefers a rootless socket over the root one.
	DockerHost string            `yaml:"docker_host"`
	WorkDir    string            `yaml:"work_dir"`
	Labels     map[string]string `yaml:"labels"`
	// ControllerURL and JoinToken are used by `zoomies agent` in standalone
	// mode; they are ignored by the embedded agent.
	ControllerURL string `yaml:"controller_url"`
	JoinToken     string `yaml:"join_token"`
	AgentToken    string `yaml:"agent_token"`
	// CAFile pins the controller's certificate for a standalone agent.
	CAFile string `yaml:"ca_file"`
	// ClientCertFile and ClientKeyFile enable mTLS to the controller.
	ClientCertFile string `yaml:"client_cert_file"`
	ClientKeyFile  string `yaml:"client_key_file"`
	// InsecureSkipVerify disables controller certificate verification.
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	HeartbeatInterval  time.Duration `yaml:"heartbeat_interval"`
	// Network is an optional pre-existing container network to attach runners to.
	Network string `yaml:"network"`
	// RunnerSHA256 is the expected digest of the actions/runner archive the
	// process backend downloads for this host's OS and architecture. Zoomies
	// ships the digests for the release it pins; an operator who pins another
	// release with github.runner_version supplies its digest here, from the
	// actions/runner release notes, and gets a verified download.
	RunnerSHA256 string `yaml:"runner_sha256"`
	// AllowUnverifiedRunnerDownload lets the process backend install a runner
	// archive whose digest it cannot check. Off by default and warned about:
	// the alternative is executing whatever the network handed over.
	AllowUnverifiedRunnerDownload bool `yaml:"allow_unverified_runner_download"`
	// RunnerDownloadURL replaces github.com/actions/runner/releases/download as
	// the place the process backend fetches archives from, for hosts that
	// mirror releases internally. The path below it is the same.
	RunnerDownloadURL string `yaml:"runner_download_url"`
	// FinishedRetention is how long a finished runner's workload -- the exited
	// container with its output, its sidecar and scratch directory, or the
	// process backend's runner directory -- stays on the host after the
	// controller has been told how the runner ended, before the agent deletes
	// it. It is the window an operator has to read a finished runner's log.
	// Zero deletes on the next pass. Unlike retention.runners, which keeps the
	// row, this is disk on the host: a busy host keeps one finished
	// container per job for this long.
	FinishedRetention time.Duration `yaml:"finished_retention"`
}

// Scheduler tunes the scaling loop.
type Scheduler struct {
	// Interval is how often the reconcile loop runs even without an event.
	Interval time.Duration `yaml:"interval"`
	// ScaleUpDelay makes the scheduler wait before reacting to a queued job,
	// which damps churn when jobs arrive in bursts. Zero reacts immediately.
	ScaleUpDelay time.Duration `yaml:"scale_up_delay"`
	// MaxRunnerLifetime force-drains a runner that has lived this long, which
	// catches runners wedged by a hung job.
	MaxRunnerLifetime time.Duration `yaml:"max_runner_lifetime"`
	// ProvisionTimeout fails a runner that never finishes registering.
	ProvisionTimeout time.Duration `yaml:"provision_timeout"`
	// MaxCreatesPerTick caps how many runners may be created in one pass, so a
	// thundering herd of queued jobs cannot exhaust a host in one go.
	MaxCreatesPerTick int `yaml:"max_creates_per_tick"`
}

// Log configures structured logging.
type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // json | text
}

// OIDC configures optional single sign-on.
type OIDC struct {
	Enabled      bool     `yaml:"enabled"`
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`
	// UsernameClaim and GroupsClaim map token claims onto Zoomies identities.
	UsernameClaim string `yaml:"username_claim"`
	GroupsClaim   string `yaml:"groups_claim"`
	// AdminGroups and OperatorGroups map IdP groups onto Zoomies roles. A user
	// in no mapped group gets the viewer role.
	AdminGroups    []string `yaml:"admin_groups"`
	OperatorGroups []string `yaml:"operator_groups"`
	// AllowSignup provisions an account on first successful login.
	AllowSignup bool `yaml:"allow_signup"`
}

// Metrics configures the Prometheus endpoint.
type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	// Public exposes /metrics without authentication. Off by default because
	// job and repository names are visible in the label set.
	Public bool `yaml:"public"`
}

// Retention bounds how much history the database keeps.
type Retention struct {
	Jobs     time.Duration `yaml:"jobs"`
	Runners  time.Duration `yaml:"runners"`
	Audit    time.Duration `yaml:"audit"`
	Samples  time.Duration `yaml:"samples"`
	Webhooks time.Duration `yaml:"webhooks"`
}

// Path returns the file this config was loaded from, or "" for defaults.
func (c *Config) Path() string { return c.path }

// Default returns a configuration that is safe to run as-is: loopback only,
// ephemeral Docker runners, no Docker socket exposed to jobs, auth on.
func Default() *Config {
	return &Config{
		Server: Server{
			Bind:         "127.0.0.1:8080",
			TLS:          TLS{Mode: TLSOff},
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 0, // 0: SSE streams and log tails must not be cut off
			IdleTimeout:  120 * time.Second,
		},
		Database: Database{Path: defaultStatePath("zoomies.db")},
		Security: Security{
			EncryptionKeyFile: defaultConfigPath("encryption.key"),
			SessionTTL:        7 * 24 * time.Hour,
			RateLimitLogins:   10,
		},
		GitHub: GitHub{
			APIBaseURL:   "https://api.github.com",
			WebhookPath:  "/webhooks/github",
			PollInterval: 30 * time.Second,
			PollFallback: true,
			RunnerImage:  DefaultRunnerImage,
		},
		Agent: Agent{
			Embedded:          true,
			Capacity:          defaultCapacity(),
			Backend:           "docker",
			WorkDir:           defaultStatePath("work"),
			HeartbeatInterval: 30 * time.Second,
			FinishedRetention: 10 * time.Minute,
		},
		Scheduler: Scheduler{
			Interval:          10 * time.Second,
			ScaleUpDelay:      0,
			MaxRunnerLifetime: 6 * time.Hour,
			ProvisionTimeout:  5 * time.Minute,
			MaxCreatesPerTick: 10,
		},
		Log:     Log{Level: "info", Format: "json"},
		Metrics: Metrics{Enabled: true, Path: "/metrics"},
		OIDC: OIDC{
			Scopes:        []string{"openid", "profile", "email"},
			UsernameClaim: "preferred_username",
			GroupsClaim:   "groups",
		},
		Retention: Retention{
			Jobs:     30 * 24 * time.Hour,
			Runners:  7 * 24 * time.Hour,
			Audit:    365 * 24 * time.Hour,
			Samples:  7 * 24 * time.Hour,
			Webhooks: 7 * 24 * time.Hour,
		},
		CapacityDemand: CapacityDemand{Cooldown: 10 * time.Minute, Timeout: 10 * time.Second},
	}
}

// DefaultRunnerImage is the image new pools use unless told otherwise. It is
// built from deploy/Dockerfile.runner and tracks the current actions/runner
// release and its .NET 8 dependency.
const DefaultRunnerImage = "ghcr.io/eyupio/zoomies-runner:latest"

func defaultCapacity() int {
	// One runner per two cores is a defensible starting point: a job usually
	// wants more than one core, and the host still has room to breathe.
	if n := runtime.NumCPU() / 2; n > 0 {
		return n
	}
	return 1
}

// StateDir is where the controller keeps its database and runner work areas.
func StateDir() string {
	if v := os.Getenv("ZOOMIES_STATE_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "zoomies")
		}
	}
	if os.Geteuid() == 0 {
		return "/var/lib/zoomies"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "zoomies")
	}
	return ".zoomies"
}

// ConfigDir is where zoomies.yaml and the encryption key live.
func ConfigDir() string {
	if v := os.Getenv("ZOOMIES_CONFIG_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "zoomies")
		}
	}
	if os.Geteuid() == 0 {
		return "/etc/zoomies"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "zoomies")
	}
	return ".zoomies"
}

func defaultStatePath(name string) string  { return filepath.Join(StateDir(), name) }
func defaultConfigPath(name string) string { return filepath.Join(ConfigDir(), name) }

// DefaultConfigFile is the path Load uses when none is given.
func DefaultConfigFile() string { return filepath.Join(ConfigDir(), "zoomies.yaml") }

// Load reads a config file, applies environment overrides and fills defaults.
// A missing file is not an error when path is empty: an operator running
// `zoomies controller` with only environment variables gets working defaults.
func Load(path string) (*Config, error) {
	cfg := Default()
	explicit := path != ""
	if path == "" {
		path = DefaultConfigFile()
	}

	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A strict decoder turns a typo like "extenal_url" into an error that
		// names the line, instead of a setting that silently does nothing.
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(cfg); err != nil && err.Error() != "EOF" {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		cfg.path = path
	case os.IsNotExist(err) && !explicit:
		// Defaults plus environment.
	default:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	cfg.normalize()
	return cfg, nil
}

// LoadOrDefault is Load, but a config file that fails to parse is fatal while a
// missing one is not. It is what the daemon entry points call.
func LoadOrDefault(path string) (*Config, error) { return Load(path) }

// Save writes the config to path with 0640 permissions.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# zoomies.yaml -- see https://github.com/eyupio/zoomies/blob/main/docs/configuration.md\n" +
		"# Every setting here can be overridden with a ZOOMIES_* environment variable.\n\n"
	return os.WriteFile(path, append([]byte(header), b...), 0o640)
}

// normalize fills in values that depend on other values.
func (c *Config) normalize() {
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	c.Log.Format = strings.ToLower(strings.TrimSpace(c.Log.Format))
	c.Agent.Backend = strings.ToLower(strings.TrimSpace(c.Agent.Backend))
	if c.Server.TLS.Mode == "" {
		c.Server.TLS.Mode = TLSOff
	}
	if !strings.HasPrefix(c.GitHub.WebhookPath, "/") {
		c.GitHub.WebhookPath = "/" + c.GitHub.WebhookPath
	}
	if !strings.HasPrefix(c.Metrics.Path, "/") {
		c.Metrics.Path = "/" + c.Metrics.Path
	}
	c.Server.ExternalURL = strings.TrimRight(c.Server.ExternalURL, "/")
	c.Agent.ControllerURL = strings.TrimRight(c.Agent.ControllerURL, "/")
	c.CapacityDemand.DestinationURL = strings.TrimSpace(c.CapacityDemand.DestinationURL)
	if c.Agent.Name == "" {
		if h, err := os.Hostname(); err == nil {
			c.Agent.Name = h
		} else {
			c.Agent.Name = "localhost"
		}
	}
	if c.Security.CookieSecure == nil {
		secure := strings.HasPrefix(c.Server.ExternalURL, "https://") || c.Server.TLS.Mode != TLSOff
		c.Security.CookieSecure = &secure
	}
}

// WebhookURL returns the URL GitHub should deliver to, or "" if the external
// URL is not configured.
func (c *Config) WebhookURL() string {
	if c.Server.ExternalURL == "" {
		return ""
	}
	return c.Server.ExternalURL + c.GitHub.WebhookPath
}

// CookieSecureValue resolves the tri-state cookie flag.
func (c *Config) CookieSecureValue() bool {
	return c.Security.CookieSecure != nil && *c.Security.CookieSecure
}

// BindsPublicly reports whether the listener accepts connections from off-host.
func (c *Config) BindsPublicly() bool {
	host, _, err := net.SplitHostPort(c.Server.Bind)
	if err != nil {
		host = c.Server.Bind
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]", "*":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		// A hostname; assume it resolves off-host.
		return true
	}
	return !ip.IsLoopback()
}

// ExternalURLValid reports whether the external URL parses as an absolute URL.
func (c *Config) ExternalURLValid() bool {
	if c.Server.ExternalURL == "" {
		return false
	}
	u, err := url.Parse(c.Server.ExternalURL)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// ExternalURLIsLocal reports whether the address this controller believes it is
// reached at is one only this machine can reach.
//
// It is the question behind three separate failures, so there is one answer to
// it: GitHub cannot deliver a webhook to loopback, and a webhook URL is fixed
// when the App is created; a join command naming loopback tells the new host to
// join itself; and a "check reachability" probe made from the controller only
// proves the controller can reach itself. A loopback external URL is a
// perfectly good default for a fleet reached through an SSH tunnel -- it is not
// a misconfiguration, which is why this is a question and not a warning.
func (c *Config) ExternalURLIsLocal() bool {
	if c.Server.ExternalURL == "" {
		return false
	}
	u, err := url.Parse(c.Server.ExternalURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// applyEnv overlays ZOOMIES_* environment variables.
func (c *Config) applyEnv() error {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	strs := func(key string, dst *[]string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = splitList(v)
		}
	}
	var errs []string
	boolean := func(key string, dst *bool) {
		v, ok := os.LookupEnv(key)
		if !ok {
			return
		}
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s=%q is not a boolean (use true or false)", key, v))
			return
		}
		*dst = b
	}
	integer := func(key string, dst *int) {
		v, ok := os.LookupEnv(key)
		if !ok {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s=%q is not an integer", key, v))
			return
		}
		*dst = n
	}
	dur := func(key string, dst *time.Duration) {
		v, ok := os.LookupEnv(key)
		if !ok {
			return
		}
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s=%q is not a duration (try 30s, 5m, 2h)", key, v))
			return
		}
		*dst = d
	}

	str("ZOOMIES_BIND", &c.Server.Bind)
	str("ZOOMIES_EXTERNAL_URL", &c.Server.ExternalURL)
	if v, ok := os.LookupEnv("ZOOMIES_TLS_MODE"); ok {
		c.Server.TLS.Mode = TLSMode(strings.ToLower(strings.TrimSpace(v)))
	}
	str("ZOOMIES_TLS_CERT_FILE", &c.Server.TLS.CertFile)
	str("ZOOMIES_TLS_KEY_FILE", &c.Server.TLS.KeyFile)
	strs("ZOOMIES_TLS_HOSTS", &c.Server.TLS.Hosts)
	strs("ZOOMIES_TRUSTED_PROXIES", &c.Server.TrustedProxies)
	strs("ZOOMIES_ALLOWED_ORIGINS", &c.Server.AllowedOrigins)
	boolean("ZOOMIES_ALLOW_INDEXING", &c.Server.AllowIndexing)

	str("ZOOMIES_DB_PATH", &c.Database.Path)

	str("ZOOMIES_ENCRYPTION_KEY", &c.Security.EncryptionKey)
	str("ZOOMIES_ENCRYPTION_KEY_FILE", &c.Security.EncryptionKeyFile)
	dur("ZOOMIES_SESSION_TTL", &c.Security.SessionTTL)
	boolean("ZOOMIES_DISABLE_AUTH", &c.Security.DisableAuth)
	integer("ZOOMIES_RATE_LIMIT_LOGINS", &c.Security.RateLimitLogins)
	if v, ok := os.LookupEnv("ZOOMIES_COOKIE_SECURE"); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("ZOOMIES_COOKIE_SECURE=%q is not a boolean", v))
		} else {
			c.Security.CookieSecure = &b
		}
	}

	str("ZOOMIES_GITHUB_API_BASE_URL", &c.GitHub.APIBaseURL)
	str("ZOOMIES_GITHUB_UPLOAD_BASE_URL", &c.GitHub.UploadBaseURL)
	str("ZOOMIES_WEBHOOK_PATH", &c.GitHub.WebhookPath)
	dur("ZOOMIES_POLL_INTERVAL", &c.GitHub.PollInterval)
	boolean("ZOOMIES_POLL_FALLBACK", &c.GitHub.PollFallback)
	str("ZOOMIES_RUNNER_IMAGE", &c.GitHub.RunnerImage)
	str("ZOOMIES_RUNNER_VERSION", &c.GitHub.RunnerVersion)

	boolean("ZOOMIES_AGENT_EMBEDDED", &c.Agent.Embedded)
	str("ZOOMIES_AGENT_NAME", &c.Agent.Name)
	integer("ZOOMIES_AGENT_CAPACITY", &c.Agent.Capacity)
	str("ZOOMIES_AGENT_BACKEND", &c.Agent.Backend)
	str("ZOOMIES_DOCKER_HOST", &c.Agent.DockerHost)
	// Docker's own variable is honoured only when Zoomies' is not set. The
	// compose file hands the whole .env to the container, and an operator
	// whose daemon is rootless or remote keeps DOCKER_HOST in that file for
	// docker and compose themselves; read second, it would silently override
	// the socket the compose file names explicitly.
	if _, explicit := os.LookupEnv("ZOOMIES_DOCKER_HOST"); !explicit {
		str("DOCKER_HOST", &c.Agent.DockerHost)
	}
	str("ZOOMIES_WORK_DIR", &c.Agent.WorkDir)
	str("ZOOMIES_CONTROLLER_URL", &c.Agent.ControllerURL)
	str("ZOOMIES_JOIN_TOKEN", &c.Agent.JoinToken)
	str("ZOOMIES_AGENT_TOKEN", &c.Agent.AgentToken)
	str("ZOOMIES_AGENT_CA_FILE", &c.Agent.CAFile)
	str("ZOOMIES_AGENT_CLIENT_CERT_FILE", &c.Agent.ClientCertFile)
	str("ZOOMIES_AGENT_CLIENT_KEY_FILE", &c.Agent.ClientKeyFile)
	boolean("ZOOMIES_AGENT_INSECURE_SKIP_VERIFY", &c.Agent.InsecureSkipVerify)
	dur("ZOOMIES_HEARTBEAT_INTERVAL", &c.Agent.HeartbeatInterval)
	str("ZOOMIES_AGENT_NETWORK", &c.Agent.Network)
	dur("ZOOMIES_AGENT_FINISHED_RETENTION", &c.Agent.FinishedRetention)
	str("ZOOMIES_AGENT_RUNNER_SHA256", &c.Agent.RunnerSHA256)
	boolean("ZOOMIES_AGENT_ALLOW_UNVERIFIED_RUNNER_DOWNLOAD", &c.Agent.AllowUnverifiedRunnerDownload)
	str("ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL", &c.Agent.RunnerDownloadURL)
	if v, ok := os.LookupEnv("ZOOMIES_AGENT_LABELS"); ok {
		m, err := parseKV(v)
		if err != nil {
			errs = append(errs, "ZOOMIES_AGENT_LABELS: "+err.Error())
		} else {
			c.Agent.Labels = m
		}
	}

	dur("ZOOMIES_SCHEDULER_INTERVAL", &c.Scheduler.Interval)
	dur("ZOOMIES_SCALE_UP_DELAY", &c.Scheduler.ScaleUpDelay)
	dur("ZOOMIES_MAX_RUNNER_LIFETIME", &c.Scheduler.MaxRunnerLifetime)
	dur("ZOOMIES_PROVISION_TIMEOUT", &c.Scheduler.ProvisionTimeout)
	integer("ZOOMIES_MAX_CREATES_PER_TICK", &c.Scheduler.MaxCreatesPerTick)

	str("ZOOMIES_LOG_LEVEL", &c.Log.Level)
	str("ZOOMIES_LOG_FORMAT", &c.Log.Format)

	boolean("ZOOMIES_OIDC_ENABLED", &c.OIDC.Enabled)
	str("ZOOMIES_OIDC_ISSUER", &c.OIDC.Issuer)
	str("ZOOMIES_OIDC_CLIENT_ID", &c.OIDC.ClientID)
	str("ZOOMIES_OIDC_CLIENT_SECRET", &c.OIDC.ClientSecret)
	str("ZOOMIES_OIDC_REDIRECT_URL", &c.OIDC.RedirectURL)
	strs("ZOOMIES_OIDC_SCOPES", &c.OIDC.Scopes)
	str("ZOOMIES_OIDC_USERNAME_CLAIM", &c.OIDC.UsernameClaim)
	str("ZOOMIES_OIDC_GROUPS_CLAIM", &c.OIDC.GroupsClaim)
	strs("ZOOMIES_OIDC_ADMIN_GROUPS", &c.OIDC.AdminGroups)
	strs("ZOOMIES_OIDC_OPERATOR_GROUPS", &c.OIDC.OperatorGroups)
	boolean("ZOOMIES_OIDC_ALLOW_SIGNUP", &c.OIDC.AllowSignup)

	boolean("ZOOMIES_METRICS_ENABLED", &c.Metrics.Enabled)
	str("ZOOMIES_METRICS_PATH", &c.Metrics.Path)
	boolean("ZOOMIES_METRICS_PUBLIC", &c.Metrics.Public)

	dur("ZOOMIES_RETENTION_JOBS", &c.Retention.Jobs)
	dur("ZOOMIES_RETENTION_RUNNERS", &c.Retention.Runners)
	dur("ZOOMIES_RETENTION_AUDIT", &c.Retention.Audit)
	dur("ZOOMIES_RETENTION_SAMPLES", &c.Retention.Samples)
	dur("ZOOMIES_RETENTION_WEBHOOKS", &c.Retention.Webhooks)
	str("ZOOMIES_CAPACITY_DEMAND_URL", &c.CapacityDemand.DestinationURL)
	str("ZOOMIES_CAPACITY_DEMAND_SIGNING_SECRET", &c.CapacityDemand.SigningSecret)
	dur("ZOOMIES_CAPACITY_DEMAND_COOLDOWN", &c.CapacityDemand.Cooldown)
	dur("ZOOMIES_CAPACITY_DEMAND_TIMEOUT", &c.CapacityDemand.Timeout)
	strs("ZOOMIES_CAPACITY_DEMAND_POOLS", &c.CapacityDemand.Pools)

	if len(errs) > 0 {
		return fmt.Errorf("invalid environment configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseKV(v string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not key=value", p)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return out, nil
}
