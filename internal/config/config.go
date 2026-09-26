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
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/machine"
	"github.com/eyupio/zoomies/internal/naming"
)

// Config is the complete on-disk configuration.
type Config struct {
	Server         Server         `yaml:"server"`
	Database       Database       `yaml:"database"`
	Security       Security       `yaml:"security"`
	GitHub         GitHub         `yaml:"github"`
	Agent          Agent          `yaml:"agent"`
	Runners        Runners        `yaml:"runners"`
	Scheduler      Scheduler      `yaml:"scheduler"`
	Log            Log            `yaml:"log"`
	OIDC           OIDC           `yaml:"oidc"`
	Metrics        Metrics        `yaml:"metrics"`
	Status         Status         `yaml:"status"`
	Retention      Retention      `yaml:"retention"`
	Limits         Limits         `yaml:"limits"`
	Images         Images         `yaml:"images"`
	Updates        Updates        `yaml:"updates"`
	CapacityDemand CapacityDemand `yaml:"capacity_demand"`
	Provider       Provider       `yaml:"provider"`
	UI             UI             `yaml:"ui"`
	Backup         Backup         `yaml:"backup"`

	// Bootstrap is the first identity an unattended install asks for. It is
	// read from the environment only -- see Bootstrap.
	Bootstrap Bootstrap `yaml:"-"`

	// path records where this config was read from, for error messages.
	path string `yaml:"-"`
	// keyInFile records that the encryption key was written in that file, as
	// opposed to arriving from the environment after the file was read. The
	// warning about a key in the config file has to look at where the key came
	// from, not at whether one is present.
	keyInFile bool `yaml:"-"`
	// sources records which layer set each key -- see sources.go. It is what
	// the settings page reads to say whether a value came from the file, the
	// database or the environment, and therefore whether changing it here will
	// survive a restart.
	sources map[string]Source `yaml:"-"`
}

// Bootstrap names the first identity a controller creates for itself when
// its database has no accounts, so an instance a compose file or a Terraform
// module brought up is usable with nobody at a browser and nobody reading the
// setup token out of the log -- a compose file has nobody watching.
//
// It is environment only: never a key in zoomies.yaml nor a row in the
// settings table. It is a one-shot instruction rather than a setting -- it
// acts once, on an empty database, and is ignored ever after -- so storing it
// would keep a value that means nothing in a place that implies it does. The
// secrets are named by path rather than carried in the variables, because a
// process's environment is readable by more things than a 0600 file is:
// `docker inspect`, /proc, a crash report.
type Bootstrap struct {
	// Admin is the username of the first account.
	Admin string
	// PasswordFile holds that account's password.
	PasswordFile string
	// TokenFile holds an API token the provisioner generated. The controller
	// registers it as a platform token for the account and prints nothing:
	// the secret is chosen by whoever already holds it, so there is no second
	// copy to scrape out of a log.
	TokenFile string
}

// Requested reports whether any of the bootstrap variables is set.
func (b Bootstrap) Requested() bool {
	return b.Admin != "" || b.PasswordFile != "" || b.TokenFile != ""
}

// Variables names the bootstrap variables that are set, for a message that
// has to tell an operator which ones to remove.
func (b Bootstrap) Variables() []string {
	var out []string
	if b.Admin != "" {
		out = append(out, "ZOOMIES_BOOTSTRAP_ADMIN")
	}
	if b.PasswordFile != "" {
		out = append(out, "ZOOMIES_BOOTSTRAP_PASSWORD_FILE")
	}
	if b.TokenFile != "" {
		out = append(out, "ZOOMIES_BOOTSTRAP_TOKEN_FILE")
	}
	return out
}

// Images controls how the fleet keeps the images its pools run up to date.
type Images struct {
	// RefreshInterval is how often every pool's image is prewarmed again on
	// the hosts that can run it. Prewarming otherwise happens only when a pool
	// is created, edited or prewarmed by hand, so a pool that names a moving
	// tag -- which the default ghcr.io/eyupio/zoomies-runner:latest is -- keeps
	// running whatever its hosts first pulled, however many times the tag has
	// moved since.
	//
	// The work is idempotent and off every job's critical path: it costs a
	// registry round trip per pool per host, and a pull only when the tag has
	// actually moved. Zero switches it off, which is what an air-gapped fleet
	// or one that pins every pool to a digest wants.
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

// UI holds what the web UI opens with. Nothing here changes what the fleet
// does; it changes what an operator sees first. Every one of these is a
// starting point the page itself lets an operator move away from, and the
// page remembers the move in that browser -- so what is set here is what
// somebody who has never chosen sees, on every screen they open it on.
type UI struct {
	CapacityMap CapacityMap `yaml:"capacity_map"`
	// QueueWarningThreshold is how many jobs have to be queued before the
	// queue tiles on the Overview, Jobs and Pools pages turn to their warning
	// colour. Unlike the capacity map's layouts, there is no per-browser
	// override to fall back to: this is the one answer every operator sees,
	// so a fleet whose queue habitually sits at a dozen jobs can say so rather
	// than living with an amber tile that never turns off.
	QueueWarningThreshold int `yaml:"queue_warning_threshold"`
}

// CapacityMap is how the host capacity map first draws itself on each of the
// two pages that carry it. The two are separate on purpose: the Overview is
// glanced at, where every host on one chart answers "which machine is busy",
// and the Hosts page is where one machine gets looked into, where a chart per
// host answers "what has it been doing" -- and a fleet may want each page to
// open on its own answer.
type CapacityMap struct {
	// OverviewLayout is the layout the Overview's map opens with: overlay,
	// every host on one chart, or split, a chart for each.
	OverviewLayout string `yaml:"overview_layout"`
	// HostsLayout is the same choice for the Hosts page.
	HostsLayout string `yaml:"hosts_layout"`
}

// The capacity map's layouts, as the settings and the UI name them.
const (
	CapacityLayoutOverlay = "overlay"
	CapacityLayoutSplit   = "split"
)

// CapacityLayouts lists the layouts the map can open in, in the order the
// settings page offers them.
var CapacityLayouts = []string{CapacityLayoutOverlay, CapacityLayoutSplit}

// Updates controls whether this controller asks github.com which release of
// Zoomies is current, so that being out of date is something the UI says rather
// than something an operator finds out later.
type Updates struct {
	// CheckInterval is how often that question is asked. Zero switches the
	// check off, and with it the one request Zoomies makes to github.com that
	// is not about your fleet -- which is what an air-gapped deployment, or one
	// pointed at GitHub Enterprise Server with no route to github.com, wants.
	//
	// Nothing is ever downloaded or installed by this: the controller does not
	// update itself, it only says that a newer release exists.
	CheckInterval time.Duration `yaml:"check_interval"`
}

// Backup is the controller's own copies of its database: where they go, how
// often one is taken, how many are kept, and which object stores each one is
// copied to afterwards.
//
// The copies are the same layout `zoomies backup` writes and `zoomies restore`
// reads, so a scheduled copy is restorable by exactly the command that restores
// one taken by hand. A copy beside the database is a backup against a mistake
// and not against the disk, which is what Remotes is for: the same archive the
// Backups tab downloads, put in somebody else's bucket by the fleet rather than
// by a cron line nobody has checked since they wrote it.
type Backup struct {
	// Directory is where backups are kept. Empty is a `backups` directory
	// beside the database, which on a container deployment is the mounted
	// volume -- the one place a copy survives the container being recreated.
	// A relative path is relative to the database's directory, not to
	// wherever the process was started from.
	Directory string `yaml:"directory"`
	// Interval is how often the controller takes a copy of its own accord.
	// Zero switches scheduled copies off; the settings page and the command
	// line still take one on demand.
	Interval time.Duration `yaml:"interval"`
	// Keep is how many of the controller's own copies are kept; the oldest
	// beyond it are deleted after each new one. Zero keeps every one. Copies
	// an operator uploaded are never counted and never deleted by this.
	Keep int `yaml:"keep"`
	// Remotes are the S3-compatible buckets every new backup is copied to.
	// Empty is the old behaviour and still the default: nothing leaves the
	// host unless somebody says where it goes.
	//
	// They live in the file rather than in the database, unlike a provider's
	// credentials, because of the day they are for. A fleet whose database is
	// gone has no stored settings to read, and `zoomies restore --from` has to
	// be able to find the copy and open the bucket with nothing but
	// zoomies.yaml in front of it. Keep the file mode 0600 and prefer
	// ZOOMIES_BACKUP_REMOTE_SECRET_ACCESS_KEY for the secret.
	Remotes []BackupRemote `yaml:"remotes"`
}

// BackupRemote is one S3-compatible destination: a bucket somebody else's disk
// is responsible for, and what it takes to write to it.
//
// Any implementation of the S3 API does -- AWS, MinIO, Ceph, Backblaze B2,
// Cloudflare R2, Garage -- because the client is the same few signed requests
// against all of them. There is no SDK behind it, for the reason there is no
// Docker SDK behind internal/backend: four verbs of one protocol is less code
// than the dependency that implements two hundred.
type BackupRemote struct {
	// Name is what the Backups tab, the log and the problems drawer call this
	// destination. Empty is "offsite" for the first and "offsite-2" for the
	// next, and two remotes may not share one.
	Name string `yaml:"name"`
	// Endpoint is the service's URL: https://s3.eu-west-2.amazonaws.com,
	// https://<account>.r2.cloudflarestorage.com, http://minio:9000. The
	// scheme decides whether the connection is encrypted -- there is no
	// separate "use TLS" switch to disagree with it.
	Endpoint string `yaml:"endpoint"`
	// Region is what the request is signed for. Empty is us-east-1, which is
	// what every S3 implementation that has no regions of its own accepts.
	Region string `yaml:"region"`
	// Bucket is the bucket. Zoomies never creates it: a backup destination
	// that appears because the controller made it is a destination nobody has
	// checked the retention, versioning or access policy of.
	Bucket string `yaml:"bucket"`
	// Prefix is the key prefix inside it, so one bucket can hold several
	// fleets. "zoomies/prod" puts a backup at zoomies/prod/zoomies-....tar.gz.
	Prefix string `yaml:"prefix"`
	// AccessKeyID and SecretAccessKey are the credentials the requests are
	// signed with. The secret is a secret: it belongs in the environment, or
	// in a file only the controller reads.
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	// Passphrase encrypts the archive before it is uploaded, with the same
	// argon2id and AES-256-GCM the Backups tab's encrypted download uses.
	//
	// Empty uploads the plain archive, and the validator says what that costs:
	// a backup is the whole fleet, and in a bucket it is the whole fleet on
	// somebody else's disk. Nothing on the controller can recover a lost
	// passphrase -- the archive opens with it and with nothing else.
	Passphrase string `yaml:"passphrase"`
	// PathStyle puts the bucket in the path (http://minio:9000/bucket/key)
	// rather than in the hostname. Unset chooses: virtual-hosted style for
	// AWS's own endpoints, path style for everything else, which is what
	// MinIO, Ceph and a bare IP address need.
	PathStyle *bool `yaml:"path_style"`
	// Keep is how many copies this remote holds; the oldest beyond it are
	// deleted after each upload. Zero keeps every one, exactly as backup.keep
	// does, and is the default: object storage is cheap and the operator who
	// wants an offsite copy usually wants the long tail of them.
	Keep int `yaml:"keep"`
	// Disabled stops uploads to this remote without removing what it holds or
	// making its configuration something an operator has to reconstruct.
	Disabled bool `yaml:"disabled"`
}

// Enabled reports whether this remote is configured enough to be used.
func (r BackupRemote) Enabled() bool {
	return !r.Disabled && strings.TrimSpace(r.Endpoint) != "" && strings.TrimSpace(r.Bucket) != ""
}

// Encrypted reports whether the archive is sealed before it leaves the host.
func (r BackupRemote) Encrypted() bool { return strings.TrimSpace(r.Passphrase) != "" }

// Where reports the bucket and prefix as one phrase, for a log line or a row
// on the Backups tab.
func (r BackupRemote) Where() string {
	where := r.Bucket
	if p := strings.Trim(strings.TrimSpace(r.Prefix), "/"); p != "" {
		where += "/" + p
	}
	return where
}

// EnabledBackupRemotes is the remotes a copy is actually sent to, named and in
// the order the file lists them.
func (c *Config) EnabledBackupRemotes() []BackupRemote {
	var out []BackupRemote
	for _, r := range c.Backup.Remotes {
		if r.Enabled() {
			out = append(out, r)
		}
	}
	return out
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

// Provider bounds what the infrastructure providers may do. The providers
// themselves -- their endpoints, credentials and the machine each one offers --
// are rows in the database, edited from the UI and sealed at rest; a credential
// in a configuration file is a credential in a backup, a diagnostics bundle and
// a screenshot.
//
// What lives here is the part an operator wants to set once and have a restart
// honour: whether machines may be rented at all, the ceilings nothing may
// exceed, and how long each step of a machine's life is given before somebody
// is asked about it.
type Provider struct {
	// Enabled decides whether the machine loop runs at all. Off by default:
	// renting a machine spends money, and nothing in this system should start
	// doing that because a release added the ability to.
	Enabled bool `yaml:"enabled"`
	// Paused is the kill switch in the file, for an operator who wants a
	// restart to come back held. It blocks new machines only -- draining,
	// deleting, recovering and verifying ownership all continue, because a
	// switch that also stopped those would strand running VMs nobody is
	// watching. The same switch exists per provider as a row, which is what the
	// Hosts page presses.
	Paused bool `yaml:"paused"`
	// Interval is how often the machine loop runs. It is separate from the
	// scheduler's because it is a slower thing: a clone takes minutes, and the
	// pass that watches one has nothing to gain from a ten-second tick.
	Interval time.Duration `yaml:"interval"`
	// SweepInterval is how often each provider is asked for everything it
	// believes it is running, which is how an orphaned VM and a resource that
	// vanished underneath us are both found. It is paced rather than per pass
	// because it is one API call per provider and the answer changes slowly.
	SweepInterval time.Duration `yaml:"sweep_interval"`
	// MaxMachines is the fleet-wide ceiling across every provider.
	//
	// Zero rents nothing, exactly as a pool's max_runners of zero runs nothing:
	// a maximum of none is none. It is the default, so a fleet that turns
	// providers on has to say in the same breath how many machines it is
	// willing to pay for, and the validator says so when it has not. The
	// alternative -- zero meaning "unbounded" -- puts the one number that
	// decides the size of an invoice behind a value somebody can leave unset.
	MaxMachines int `yaml:"max_machines"`
	// MaxCreatesInFlight caps how many machines may be being built at once
	// across the fleet, so a burst of queued jobs cannot ask a hypervisor for
	// fifty clones in one pass.
	MaxCreatesInFlight int `yaml:"max_creates_in_flight"`
	// ScaleUpDelay is how long a pool's demand must stand before a machine is
	// bought for it. It defaults to zero, unlike the scheduler's: that one damps
	// runner churn, and a runner costs seconds, whereas a machine that takes
	// four minutes to arrive has already spent the delay by being slow.
	ScaleUpDelay time.Duration `yaml:"scale_up_delay"`
	// CallTimeout bounds one API request to a provider.
	CallTimeout time.Duration `yaml:"call_timeout"`
	// CreateTimeout bounds the whole asynchronous creation of a machine, not
	// the request that starts it.
	CreateTimeout time.Duration `yaml:"create_timeout"`
	// BootstrapTimeout bounds installing the agent inside a machine that is up.
	BootstrapTimeout time.Duration `yaml:"bootstrap_timeout"`
	// EnrolTimeout is how long a bootstrapped machine has to appear as a host.
	// It has to outlast a heartbeat timeout, or a machine that joined and went
	// briefly quiet would be given up on.
	EnrolTimeout time.Duration `yaml:"enrol_timeout"`
	// DeleteTimeout bounds an asynchronous deletion.
	DeleteTimeout time.Duration `yaml:"delete_timeout"`
	// AmbiguityTimeout is how long an operation whose outcome is unknown is
	// reconciled by looking before a person is asked instead. It must outlast
	// CreateTimeout: a create that is merely slow is not an unknown outcome.
	AmbiguityTimeout time.Duration `yaml:"ambiguity_timeout"`
	// IdleTimeout is how long a machine's host must have had no runner on it
	// before the machine is drained.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// ScaleDownCooldown is how long that idleness must hold continuously before
	// anything is deleted, so a quiet minute between two bursts does not
	// destroy the machines the second burst is about to want.
	ScaleDownCooldown time.Duration `yaml:"scale_down_cooldown"`
	// DeleteGrace is how long a machine whose host has gone silent is left
	// alone before it is treated as lost. It has to outlast the controller's
	// own "this host is lost" judgement, or a network blip would destroy a
	// machine that is in the middle of a job.
	DeleteGrace time.Duration `yaml:"delete_grace"`
}

// Server controls the HTTP listener.
type Server struct {
	// TailcatEnabled permits private agent connections, started on first enrolment.
	TailcatEnabled bool `yaml:"tailcat_enabled"`
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
	// only and is refused wherever the controller looks reachable -- see
	// LikelyReachable, which counts an external URL or a trusted proxy as
	// reachable even on a loopback bind.
	DisableAuth bool `yaml:"disable_auth"`
	// RateLimitLogins caps password attempts per source address per minute.
	RateLimitLogins int `yaml:"rate_limit_logins"`
	// DockerInDockerExpected stops a pool that gives its jobs a private daemon
	// raising pool.dangerous for the privileged sidecar that daemon runs in.
	//
	// A warning an operator cannot act on is a warning they stop reading, and
	// with it the ones they could. A fleet whose whole purpose is building
	// container images has made that choice once, deliberately, and repeating
	// it for every such pool on every pass buries the settings that are still
	// worth a second look. It silences that one sentence and nothing else: the
	// host socket still warns, because it hands a job root on the host, and so
	// do persistent runners.
	DockerInDockerExpected bool `yaml:"docker_in_docker_expected"`
	// AllowPrivateEgress lets the settings that make this process dial a URL
	// -- the OIDC issuer, the GitHub API, the capacity-demand destination,
	// the runner download mirror, a backup remote, a provider -- name this
	// machine, its link-local network or a private range. See
	// CheckOutboundURL. It is off because those addresses are the platform's
	// own neighbourhood, and on a single-team instance whose identity
	// provider or Enterprise Server is on the LAN it is the one key to set.
	AllowPrivateEgress bool `yaml:"allow_private_egress"`
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
	// AllowWorkflowCancellation lets operators ask GitHub to cancel the whole
	// workflow run that owns a Zoomies job. It is on by default; operators who
	// want a read-only Actions grant can explicitly disable it.
	AllowWorkflowCancellation bool `yaml:"allow_workflow_cancellation"`
	// ToolchainScanInterval is how often every workflow every installation
	// can see is read for the toolchains its jobs install; 0 scans only when
	// asked. Off by default, because a scan reads every workflow file in
	// every repository -- thousands of calls on a large organisation, from
	// the quota the scheduler needs to find queued jobs.
	ToolchainScanInterval time.Duration `yaml:"toolchain_scan_interval"`
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
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
	// AllowInsecureHTTP permits an http:// controller URL that is not on
	// loopback. Off by default: the agent token and the runner registration
	// credentials in every create task would otherwise cross the network in
	// the clear.
	AllowInsecureHTTP bool          `yaml:"allow_insecure_http"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
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
	// RegistryAuth is a base64 X-Registry-Auth value the container backends
	// send when they pull. Without it a pool on a private registry cannot use
	// pull_policy: pinned-only at all, because the pull it needs is the one
	// the registry refuses. Best supplied as ZOOMIES_REGISTRY_AUTH rather than
	// written into zoomies.yaml: it is a credential.
	RegistryAuth string `yaml:"registry_auth"`
	// RunnerDownloadURL replaces github.com/actions/runner/releases/download as
	// the place the process backend fetches archives from, for hosts that
	// mirror releases internally. The path below it is the same.
	RunnerDownloadURL string `yaml:"runner_download_url"`
	// ExtraCAFile is a PEM bundle on this host that container runners, and
	// their Docker sidecars, add to what they trust. It is a host setting and
	// not a pool one because a TLS-intercepting proxy is a property of the
	// network the host sits on: every job there needs it, whichever pool it
	// came from. The process backend ignores it -- a bare runner already uses
	// the host's own trust store.
	ExtraCAFile string `yaml:"extra_ca_file"`
	// FinishedRetention is how long a finished runner's workload -- the exited
	// container with its output, its sidecar and scratch directory, or the
	// process backend's runner directory -- stays on the host after the
	// controller has been told how the runner ended, before the agent deletes
	// it. It is the window an operator has to read a finished runner's log.
	// Zero deletes on the next pass. Unlike retention.runners, which keeps the
	// row, this is disk on the host: a busy host keeps one finished
	// container per job for this long.
	FinishedRetention time.Duration `yaml:"finished_retention"`
	// PrewarmTimeout bounds background work separately from foreground starts.
	PrewarmTimeout time.Duration `yaml:"prewarm_timeout"`
	PrewarmJitter  time.Duration `yaml:"prewarm_jitter"`
	// BootstrapCPUGrace preserves normal quotas before host-pressure reductions.
	BootstrapCPUGrace time.Duration `yaml:"bootstrap_cpu_grace"`
	// DockerBuildCacheMB is the target for unused Docker builder cache. Zero
	// disables automatic cache pruning on shared or externally managed daemons.
	DockerBuildCacheMB int `yaml:"docker_build_cache_mb"`
}

// Scheduler tunes the scaling loop.
type Scheduler struct {
	// PlacementMode keeps readiness-based host selection opt-in until measured.
	PlacementMode string `yaml:"placement_mode"`
	// Interval is how often the reconcile loop runs even without an event.
	Interval time.Duration `yaml:"interval"`
	// ScaleUpDelay makes the scheduler wait before reacting to a queued job,
	// which damps churn when jobs arrive in bursts. Zero reacts immediately.
	ScaleUpDelay time.Duration `yaml:"scale_up_delay"`
	// MaxRunnerLifetime drains a runner that has lived this long, the next
	// time it is not busy. It bounds how long a persistent runner's state and
	// credentials live; it never ends a job, so it is no answer to a hung
	// one -- that is what a workflow's timeout-minutes is for.
	MaxRunnerLifetime time.Duration `yaml:"max_runner_lifetime"`
	// ProvisionTimeout fails a runner that never finishes registering. It is
	// the last of three bounds on a runner's start and the only one that gives
	// up, so it has to outlast the other two: the agent's own create budget,
	// which covers a cold image pull, and the wait a Docker pool's runner does
	// for its daemon before it registers at all. Set inside those, it condemns
	// runners the rest of the system is patiently still making.
	ProvisionTimeout time.Duration `yaml:"provision_timeout"`
	// DrainTimeout fails a runner that has been draining this long with no job
	// left on it, so a stop lost to a controller restart stops holding a host
	// slot for ever. A runner still finishing a job is never touched by it.
	DrainTimeout time.Duration `yaml:"drain_timeout"`
	// MaxCreatesPerTick caps how many runners may be created in one pass, so a
	// thundering herd of queued jobs cannot exhaust a host in one go.
	MaxCreatesPerTick int `yaml:"max_creates_per_tick"`
	// RegistrationConcurrency bounds outstanding credential requests per installation.
	RegistrationConcurrency int `yaml:"registration_concurrency"`
	// DefaultRunnerLimits gives a runner whose pool sets no CPU or memory
	// limit one slot's share of its host's machine as a real cgroup limit --
	// the same share the scheduler already charges it. Off, such a runner is
	// given no limit at all, and a host's worth of them can each take every
	// core, which is the overload this setting exists to prevent.
	DefaultRunnerLimits bool `yaml:"default_runner_limits"`
	// AutoRerun asks GitHub to run a job again when this fleet is what broke
	// it -- a runner that died under it, not a test that failed. It is off by
	// default and deliberately so: GitHub minutes are the operator's to spend,
	// and a job that got as far as running may have had side effects its
	// author knows about and this does not.
	AutoRerun bool `yaml:"auto_rerun"`
	// AutoRerunLimit is how many times one workflow run may be re-run
	// automatically. It is what stops a fault the fleet is causing every time
	// from re-running the same job for ever, and it is counted from GitHub's
	// own run attempt rather than from anything we store: the bound therefore
	// survives a controller restart, and a re-run an operator asked for by
	// hand counts against it like any other.
	AutoRerunLimit int `yaml:"auto_rerun_limit"`
	// HostThrottling lets the controller throttle a host whose measurements
	// say it is overwhelmed: fewer slots, and a lower CPU quota on the runners
	// already on it, stepped back up after a stretch of calm. Off, the
	// pressure holds still refuse new starts while the pressure is acute, and
	// nothing outlasts them.
	HostThrottling bool `yaml:"host_throttling"`
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
	// PlatformGroups maps onto the role above administrator. It is listed
	// first when a login is mapped, so somebody in both a platform group
	// and an admin group gets the higher of the two rather than whichever
	// the code happened to check first.
	PlatformGroups []string `yaml:"platform_groups"`
	// AllowSignup provisions an account on first successful login.
	AllowSignup bool `yaml:"allow_signup"`
	// LinkByUsername lets a first single sign-on login take over an existing
	// local account that has a password and the same username. Off, the
	// username alone links only to an account created for SSO -- one with no
	// password -- because with an identity provider whose users can influence
	// their own username claim, a sign-in as "admin" would otherwise inherit
	// the local admin's role. Turn it on for the one migration where that is
	// the intention, then turn it off again.
	LinkByUsername bool `yaml:"link_by_username"`
}

// Metrics configures the Prometheus endpoint.
type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	// Public exposes /metrics without authentication. Off by default because
	// job and repository names are visible in the label set.
	Public bool `yaml:"public"`
}

// StatusMode says who may read the fleet's status projection: the
// name-free, banded summary at /api/v1/status, the page at /status and the
// badge at /status.svg.
type StatusMode string

const (
	// StatusOff serves none of the three. It is the default, because a new
	// capability starts disabled and this one's cost is disclosure.
	StatusOff StatusMode = "off"
	// StatusAuthenticated serves them to anyone signed in, whatever their
	// role.
	StatusAuthenticated StatusMode = "authenticated"
	// StatusPublic serves them to anyone who can reach the listener, which is
	// the point for a developer with no account whose job has queued, and is
	// warned about for the same reason.
	StatusPublic StatusMode = "public"
)

// Status configures the fleet's status projection.
type Status struct {
	Mode StatusMode `yaml:"mode"`
}

// Runners is what every runner this fleet creates is started with, whichever
// pool and host it lands on. A pool's own env is layered over it, so the fleet
// says what is usual and a pool says what is different.
//
// It is a section of its own rather than part of Agent because it is not about
// the agent at all: the controller writes these into the runner's environment
// when it builds the create task, and a standalone agent on another host never
// reads them.
type Runners struct {
	// DockerWait is how long a runner on a pool that provides Docker waits
	// for that daemon before refusing to take a job. It reaches the runner
	// image as ZOOMIES_DOCKER_WAIT, in whole seconds. Zero leaves the image's
	// own default in place.
	DockerWait time.Duration `yaml:"docker_wait"`
	// Env is extra environment for every runner. It is visible to every job
	// that runs, so it is the place for a proxy or a mirror and not for a
	// credential: a pool's env, or a GitHub secret, is where those belong.
	Env map[string]string `yaml:"env"`
	// DefaultCPUs and DefaultMemoryMB are where a pool's size sliders open
	// when somebody chooses a fixed size. They are not what a pool with no
	// size becomes: such a pool is given one slot's share of whichever host
	// each runner lands on, charged against that host and applied as a real
	// cgroup limit, which is the sizing most fleets want and what a new pool
	// does.
	//
	// The distinction matters because the two behave differently as a fleet
	// grows. A share follows the machine, so one pool is sized correctly on
	// every host it reaches; a figure typed here is the same everywhere, so it
	// fits the host it was chosen for and strands the machine on the ones that
	// joined later.
	//
	// They are a fleet's answer rather than a constant because the right
	// answer is the shape of the machines and the jobs: two cores and four
	// gigabytes suits most, and a fleet of small boxes or of compilers is
	// entitled to say otherwise once rather than on every pool it creates.
	// Zero or less is not "no limit" -- it is nothing having been said, and
	// DefaultRunnerSize answers with the built-in figures.
	DefaultCPUs     float64 `yaml:"default_cpus"`
	DefaultMemoryMB int64   `yaml:"default_memory_mb"`
	// MinimumCPUs and MinimumMemoryMB are the fleet's minimum: the least a
	// runner may be given when no host has room for its standard size -- the
	// figures above for a fixed pool, a whole slot's share of the host for an
	// automatic one. Unlike the standard figures they are not an opening
	// value: every pool whose own minimum is zero follows them live
	// (controller.EffectiveMinimum), so changing them moves those pools with
	// no row rewritten. Zero is none, which is what every fleet was before
	// they existed, so an upgrade changes nothing.
	MinimumCPUs     float64 `yaml:"minimum_cpus"`
	MinimumMemoryMB int64   `yaml:"minimum_memory_mb"`
}

// The size a runner gets where nothing else says: two cores and four
// gigabytes. It is what a pool created today opens on, and it is deliberately
// modest -- a figure an operator raises for the pool that needs it is better
// than one that fits three runners on a machine that has room for eight.
const (
	DefaultRunnerCPUs     = 2
	DefaultRunnerMemoryMB = 4096
)

// EffectiveDockerWait is how long a Docker pool's runner actually waits for its
// daemon, which is not the same as what this setting says: zero does not mean
// no wait, it means the runner image chooses, and the image waits two minutes.
// Anything reasoning about how long a runner may take to start has to read the
// wait that happens rather than the one that was configured.
func (r Runners) EffectiveDockerWait() time.Duration {
	if r.DockerWait > 0 {
		return r.DockerWait
	}
	return ImageDockerWait
}

// DefaultRunnerSize is the per-runner CPU and memory a pool gets when it names
// none, with the built-in figures standing in for a setting nobody has given a
// usable value.
func (r Runners) DefaultRunnerSize() (cpus float64, memoryMB int64) {
	cpus, memoryMB = r.DefaultCPUs, r.DefaultMemoryMB
	if cpus <= 0 {
		cpus = DefaultRunnerCPUs
	}
	if memoryMB <= 0 {
		memoryMB = DefaultRunnerMemoryMB
	}
	return cpus, memoryMB
}

// Limits are fleet-wide ceilings on what callers of the API and agents can
// make the controller hold. Each is zero by default, meaning unlimited.
//
// They exist for an instance several teams use, where one team's script
// minting join tokens in a loop, or a browser left open in forty tabs, spends
// memory and database rows every other team shares. A limit refuses the
// request that would cross it, with a message naming the setting, rather than
// letting the process find out by running out of something.
type Limits struct {
	// Hosts caps enrolled hosts. A host that joins again under its own name
	// replaces its row and is not counted twice.
	Hosts int `yaml:"hosts"`
	// Pools caps pools.
	Pools int `yaml:"pools"`
	// Runners caps live runners across every pool. The scheduler stops
	// creating at the ceiling and says so in each pool's scaling reason.
	Runners int `yaml:"runners"`
	// JoinTokens caps join tokens that are still outstanding: neither used
	// nor expired.
	JoinTokens int `yaml:"join_tokens"`
	// EventSubscribers caps open live-update streams.
	EventSubscribers int `yaml:"event_subscribers"`
}

// Retention bounds how much history the database keeps.
//
// Audit rows are deliberately absent: an audit trail a process can quietly
// delete is not one, and the store offers no way to prune them.
type Retention struct {
	Jobs    time.Duration `yaml:"jobs"`
	Runners time.Duration `yaml:"runners"`
	// RunnerSessions is how long the usage ledger keeps a gone runner's
	// session. It is separate from Runners, and far longer, because the row
	// is what an operator debugs with for a week and the session is what they
	// reconcile a year's cloud bill against.
	RunnerSessions time.Duration `yaml:"runner_sessions"`
	// ScalingEvents is how long scaling decisions are kept. It used to be
	// called audit, and a file that still says so is honoured -- see Audit.
	ScalingEvents time.Duration `yaml:"scaling_events"`
	// Audit is the old name for ScalingEvents. It never bounded audit rows,
	// which are not pruned, so the name promised a deletion that never
	// happened and hid one that did. A value here still sets ScalingEvents,
	// with an info finding asking for the rename.
	Audit    time.Duration `yaml:"audit"`
	Samples  time.Duration `yaml:"samples"`
	Webhooks time.Duration `yaml:"webhooks"`
	// Machines is how long a deleted machine's row is kept. The row outlives
	// the VM on purpose: it is the only record of what was rented, when, and
	// what it cost, and an operator reconciling a hypervisor bill against the
	// fleet is reading exactly this.
	Machines time.Duration `yaml:"machines"`
}

// Path returns the file this config was loaded from, or "" for defaults.
func (c *Config) Path() string { return c.path }

// Default returns a configuration that is safe to run as-is: loopback only,
// ephemeral Docker runners, no Docker socket exposed to jobs, auth on.
func Default() *Config {
	return &Config{
		Server: Server{
			TailcatEnabled: true,
			Bind:           "127.0.0.1:8080",
			TLS:            TLS{Mode: TLSOff},
			ReadTimeout:    30 * time.Second,
			WriteTimeout:   0, // 0: SSE streams and log tails must not be cut off
			IdleTimeout:    120 * time.Second,
		},
		Database: Database{Path: defaultStatePath("zoomies.db")},
		Security: Security{
			EncryptionKeyFile: defaultConfigPath("encryption.key"),
			SessionTTL:        7 * 24 * time.Hour,
			RateLimitLogins:   10,
		},
		GitHub: GitHub{
			APIBaseURL:                "https://api.github.com",
			WebhookPath:               "/webhooks/github",
			PollInterval:              30 * time.Second,
			PollFallback:              true,
			AllowWorkflowCancellation: true,
			RunnerImage:               DefaultRunnerImage,
		},
		Agent: Agent{
			Embedded:          true,
			Capacity:          defaultCapacity(),
			Backend:           "docker",
			WorkDir:           defaultStatePath("work"),
			HeartbeatInterval: 30 * time.Second,
			// The controller already retains runner history and the agent waits for
			// its terminal report to be acknowledged before removal. Keeping an
			// exited container as well makes every job consume host disk for no
			// default benefit; operators who debug from container logs can opt in.
			FinishedRetention:  0,
			BootstrapCPUGrace:  2 * time.Minute,
			PrewarmTimeout:     5 * time.Minute,
			PrewarmJitter:      30 * time.Second,
			DockerBuildCacheMB: 0,
		},
		Scheduler: Scheduler{
			Interval:          10 * time.Second,
			ScaleUpDelay:      0,
			MaxRunnerLifetime: 6 * time.Hour,
			// Longer than the agent's own create budget and the runner image's
			// Docker wait together, because those two are what a runner's start
			// actually costs on a cold host: fifteen minutes for a pull on a
			// slow link, then two for the daemon. Five minutes -- what this was
			// before the runners section existed to say the second number --
			// failed runners that were still coming up and had the pool replace
			// them, which put a second pull of the same image on the same link.
			// The genuinely broken create does not wait for this: the agent
			// reports the failure and the row fails on the report. This is the
			// backstop for the create that is never reported at all.
			ProvisionTimeout:        20 * time.Minute,
			DrainTimeout:            15 * time.Minute,
			MaxCreatesPerTick:       10,
			DefaultRunnerLimits:     true,
			RegistrationConcurrency: 1,
			PlacementMode:           "headroom",
			HostThrottling:          true,
			AutoRerunLimit:          1,
		},
		Log:     Log{Level: "info", Format: "json"},
		Metrics: Metrics{Enabled: true, Path: "/metrics"},
		Status:  Status{Mode: StatusOff},
		OIDC: OIDC{
			Scopes:        []string{"openid", "profile", "email"},
			UsernameClaim: "preferred_username",
			GroupsClaim:   "groups",
		},
		Retention: Retention{
			Jobs:           30 * 24 * time.Hour,
			Runners:        7 * 24 * time.Hour,
			RunnerSessions: 365 * 24 * time.Hour,
			ScalingEvents:  365 * 24 * time.Hour,
			Samples:        7 * 24 * time.Hour,
			Webhooks:       7 * 24 * time.Hour,
			Machines:       7 * 24 * time.Hour,
		},
		// Hourly is soon enough that a host picks up a rebuilt image the same
		// working day, and rare enough that the registry never notices.
		Images: Images{RefreshInterval: time.Hour},
		// Two minutes matches the runner image's own default: dockerd in a
		// fresh sidecar on a host that is also extracting images takes longer
		// than the thirty seconds the first version allowed.
		Runners: Runners{
			DockerWait:      3 * time.Minute,
			DefaultCPUs:     DefaultRunnerCPUs,
			DefaultMemoryMB: DefaultRunnerMemoryMB,
		},
		// Daily: releases are not frequent, and a controller that asks once a
		// day still tells you within a working day of one being published.
		Updates: Updates{CheckInterval: 24 * time.Hour},
		// Nightly, keeping a week. The database holds configuration and
		// history rather than anything a workflow depends on minute to
		// minute, so a day is the right grain; seven copies bounds the disk
		// on a fleet whose database is large, and covers the week it takes
		// to notice a mistake made on Monday.
		Backup:         Backup{Interval: 24 * time.Hour, Keep: 7},
		CapacityDemand: CapacityDemand{Cooldown: 10 * time.Minute, Timeout: 10 * time.Second},
		// Off, with no ceiling and every step generously bounded. The numbers
		// are what a hypervisor actually takes: a full clone of a small Linux
		// template is minutes rather than seconds, and a guest that has to
		// finish cloud-init before its agent starts is minutes again. A
		// deployment that turns this on sets max_machines in the same edit,
		// which is what the validator's warning is for.
		Provider: Provider{
			Interval:           30 * time.Second,
			SweepInterval:      10 * time.Minute,
			MaxCreatesInFlight: 2,
			CallTimeout:        30 * time.Second,
			CreateTimeout:      20 * time.Minute,
			BootstrapTimeout:   10 * time.Minute,
			EnrolTimeout:       15 * time.Minute,
			DeleteTimeout:      15 * time.Minute,
			AmbiguityTimeout:   30 * time.Minute,
			IdleTimeout:        15 * time.Minute,
			ScaleDownCooldown:  15 * time.Minute,
			DeleteGrace:        10 * time.Minute,
		},
		// Every host on one chart, on both pages: it is the layout that reads
		// a small fleet at a glance, and a fleet large enough to want the
		// other one is a fleet whose administrator has been to the settings.
		UI: UI{
			CapacityMap: CapacityMap{
				OverviewLayout: CapacityLayoutOverlay,
				HostsLayout:    CapacityLayoutOverlay,
			},
			// One: a queue with anything at all waiting is worth an operator's
			// glance, which is the behaviour this setting replaces.
			QueueWarningThreshold: 1,
		},
	}
}

// DefaultRunnerImage is the image a pool uses when it names neither an image
// nor an operating system. It is the default variant of the catalogue in
// internal/naming, built from deploy/Dockerfile.runner; a pool that does name
// an operating system gets that variant instead.
var DefaultRunnerImage = naming.DefaultRunnerImage()

// DefaultRunnerDockerImage is DefaultRunnerImage plus a Docker client: the
// same file's runner-docker target, published under the same tags. It is what
// a pool whose docker_mode gives its jobs a daemon actually runs, whichever of
// the two the pool names; see RunnerImageFor.
var DefaultRunnerDockerImage = stockRunnerDockerRepository + ":" + naming.RunnerImageTag

// The two repositories RunnerImageFor translates between.
const (
	stockRunnerRepository       = "ghcr.io/eyupio/zoomies-runner"
	stockRunnerDockerRepository = "ghcr.io/eyupio/zoomies-runner-docker"
)

// ResolvePoolRunnerImage keeps automatic images on the build's channel, including
// their Docker CLI variants: the pool's own image where it has one, the variant
// its platform names, or the instance default, and then the Docker swap.
func ResolvePoolRunnerImage(poolImage, os, version, instanceDefault string, daemon bool) string {
	return RunnerImageFor(naming.ResolveRunnerImage(poolImage, os, version, instanceDefault), daemon)
}

// RunnerImageFor returns the image a pool's runners are created from, given
// the image the pool names and whether its docker_mode gives jobs a daemon.
//
// A docker_mode gives a job a daemon and nothing else; the client has to come
// from the image, and the stock runner image carries none, on purpose, because
// most pools never build an image. Leaving the operator to remember that -- to
// set the mode *and* swap the image for its Docker variant -- was the mistake
// everybody made: the daemon came up, the job reached its first docker step,
// and it failed with "Unable to locate executable file: docker", which names
// the missing binary and not the reason.
//
// So the swap is made here, once, whenever daemon is true, and only where the
// variant is known to exist: the stock repository, no digest, under a tag this
// build's own catalogue accounts for -- see naming.PublishedRunnerTag. One step
// of one workflow publishes both images, so such a tag exists for both or for
// neither, which a pinned pool's tag from some other build does not promise.
//
// Everything else is left exactly as it was given. A digest names one exact
// image and cannot be moved to another. A commit pin may name a run whose
// second build never finished. An image of the operator's own is theirs to
// equip, and so is a mirror of the stock image under another registry: whether
// the mirror carries the variant is not something this code can know. A pool
// that asks for a daemon while on one of those is told so -- the pool's page
// and the problems drawer carry pool.docker_client_missing -- rather than left
// to find out one failed job at a time.
//
// The swap is never reversed. A pool that stops asking for a daemon keeps the
// client, which costs it pull time and nothing else, and may be using it
// against a DOCKER_HOST of its own.
func RunnerImageFor(image string, daemon bool) string {
	if !daemon {
		return image
	}
	repo, tag, digest := naming.SplitImage(image)
	if repo != stockRunnerRepository || digest != "" || !naming.PublishedRunnerTag(tag) {
		return image
	}
	return stockRunnerDockerRepository + tag
}

// MissingDockerClient answers, for the image a pool actually runs, whether
// Zoomies knows it carries no Docker client -- and if so, which reference to
// pin instead: the same tag or digest position on the variant's repository, so
// the operator is given something to paste rather than a repository to search.
//
// "Knows" is the whole of it. An image nobody here published may carry a client
// or may not, and warning about every one of them would teach an operator to
// ignore the warning that matters. Only the stock runner image is certain, and
// only a reference RunnerImageFor could not move gets this far.
func MissingDockerClient(image string) (pin string, missing bool) {
	repo, tag, digest := naming.SplitImage(image)
	if repo != stockRunnerRepository {
		return "", false
	}
	return stockRunnerDockerRepository + tag + digest, true
}

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
	if dir := windowsDir(); dir != "" {
		return dir
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

// SharedDir is the folder Zoomies keeps data on the host in that runners, and
// anything else outside this process, have to reach: /var/lib/zoomies/shared
// on a Linux host where Zoomies runs as root, and the shared folder under the
// state directory everywhere else.
//
// It is not a setting, because a Compose deployment fixes it: the controller's
// container mounts the host's /var/lib/zoomies/shared at the same path, so the
// path the controller hands the host's Docker daemon for a runner's mount
// means the same folder on both sides. A setting that could move it inside
// the container without moving the mount would point every runner at a
// folder the daemon cannot see.
func SharedDir() string { return filepath.Join(StateDir(), "shared") }

// SharedLayout is every folder this release keeps under SharedDir, one per
// purpose. Each is created when an agent starts, so a later release adds a
// purpose by adding a line here and nothing else: the shared folder itself is
// writable by Zoomies for exactly that reason.
var SharedLayout = []string{
	// cache/pools is where a pool cache with an absolute source lives,
	// when the operator points it here.
	"cache/pools",
	// cache/tools holds each pool's tool cache: the Python, Node.js, Go and
	// Java the setup-* actions unpack, kept between runners.
	"cache/tools",
}

// EnsureSharedDir creates SharedDir and every folder in SharedLayout that is
// missing, and reports the first it could not. It changes nothing that
// already exists.
func EnsureSharedDir() error {
	for _, sub := range append([]string{""}, SharedLayout...) {
		dir := filepath.Join(SharedDir(), filepath.FromSlash(sub))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}

// ConfigDir is where zoomies.yaml and the encryption key live.
func ConfigDir() string {
	if v := os.Getenv("ZOOMIES_CONFIG_DIR"); v != "" {
		return v
	}
	if dir := windowsDir(); dir != "" {
		return dir
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

// windowsDir is the one directory Zoomies uses on Windows, for configuration
// and state alike: %ProgramData%\zoomies. A per-user directory would put the
// agent's credentials under whichever account ran `zoomies agent join`, and
// the service runs as LocalSystem, whose profile is not a place an operator
// looks. Empty everywhere but Windows.
func windowsDir() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "zoomies")
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
	if err := cfg.readFile(path); err != nil {
		return nil, err
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	cfg.normalize()
	return cfg, nil
}

// readFile decodes a config file over c. An empty path is the default file,
// which may be missing; a path named explicitly may not.
func (c *Config) readFile(path string) error {
	cfg := c
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
		// An empty file is an empty configuration, not a parse error.
		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: %w", path, err)
		}
		cfg.path = path
		cfg.keyInFile = cfg.Security.EncryptionKey != ""
		for _, key := range keysIn(b) {
			cfg.note(key, SourceFile)
		}
	case os.IsNotExist(err) && !explicit:
		// Defaults plus environment.
	default:
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return nil
}

// LoadOrDefault is Load, but a config file that fails to parse is fatal while a
// missing one is not. It is what the daemon entry points call.
func LoadOrDefault(path string) (*Config, error) { return Load(path) }

// Save writes the configuration to path with 0640 permissions.
//
// It writes what an operator actually chose, not the whole struct. The two
// keys that get the database open go in whatever they are set to -- the file
// is the only place they can live -- and so does everything a standalone agent
// needs to reach its controller. Beyond that, only a value that differs from
// the built-in default is written.
//
// This used to marshal every field, which put all eighty-nine settings in the
// file of every fresh install. That was merely noisy while the file was the
// only source of configuration; now that the database is the one an operator
// edits, it would be worse than noisy -- a key spelled in the file is a key the
// file is in charge of, so a generated file listing all of them would hand the
// whole configuration back to a text editor on the controller's host on the day
// it was installed.
//
// It also stops writing the defaults *as* defaults. agent.capacity comes from
// this machine's core count and database.path from this machine's state
// directory, so a file that spells them freezes one host's answers, and a
// second host given a copy of that file inherits them.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := yaml.Marshal(c.sparse())
	if err != nil {
		return err
	}
	header := "# zoomies.yaml -- the settings this host needs before it can read the rest.\n" +
		"#\n" +
		"# Everything else lives in the database named below and is changed on the\n" +
		"# settings page. A key written here is still honoured -- it is the layer\n" +
		"# underneath the database -- and a ZOOMIES_* environment variable still\n" +
		"# overrides both. See https://github.com/eyupio/zoomies/blob/main/docs/configuration.md\n\n"
	return os.WriteFile(path, append([]byte(header), b...), 0o640)
}

// sparse renders the configuration as the nested tree Save writes: the keys
// that have to be in a file, plus anything that is not the default.
func (c *Config) sparse() map[string]any {
	defaults := Default()
	out := map[string]any{}
	for _, s := range Settings() {
		value, err := c.Value(s.Key)
		if err != nil {
			continue
		}
		text := Text(s, value)
		// A bootstrap key is always written: the file is the only place it can
		// live, so leaving it out because it happens to equal the default would
		// mean the next start had to guess. Everything else, including the
		// agent transport keys, is written only when it differs -- an agent
		// setting at its default is a setting nobody has chosen.
		//
		// agent.work_dir is the one exception to "only when it differs": its
		// default is derived from the euid of whatever process asks for it, and
		// `zoomies agent join` always runs as root while the service it installs
		// almost always runs as an unprivileged one. A join that happens to
		// compute the same path root would default to must still pin it, or the
		// service recomputes a different default under its own uid, finds no
		// credentials at the path it guessed, and fails to start -- while the
		// controller, which saw the join succeed, still shows the host as added.
		if s.Scope != ScopeBootstrap && s.Key != "agent.work_dir" {
			if def, derr := defaults.Value(s.Key); derr == nil && Text(s, def) == text {
				continue
			}
		} else if text == "" {
			continue
		}
		putPath(out, s.Key, yamlValue(s, value))
	}
	return out
}

// yamlValue renders a value the way the strict decoder reads it back: a
// duration as the text an operator writes, and everything else as itself.
func yamlValue(s Setting, value any) any {
	if s.Kind == KindDuration {
		return Text(s, value)
	}
	return value
}

// putPath writes a dotted key into a tree of maps.
func putPath(into map[string]any, key string, value any) {
	parts := strings.Split(key, ".")
	for _, part := range parts[:len(parts)-1] {
		child, ok := into[part].(map[string]any)
		if !ok {
			child = map[string]any{}
			into[part] = child
		}
		into = child
	}
	into[parts[len(parts)-1]] = value
}

// normalize fills in values that depend on other values.
func (c *Config) normalize() {
	// The old name for the scaling-history window still sets it. Only a
	// positive value carries over: zero is what an unset key reads as, and
	// treating it as "keep everything" would turn every file that never
	// mentioned the key into one that switched pruning off.
	//
	// It does not carry over a value the fleet has stored. normalize runs last,
	// after the database layer, so without this an administrator who set the
	// scaling-history window on the settings page would get a 200, an audit
	// row, a stored value -- and a controller quietly running the number in a
	// years-old file. The finding asking for the rename is still raised, which
	// is what eventually removes the file's key altogether.
	if c.Retention.Audit > 0 && c.Source("retention.scaling_events") != SourceDatabase {
		c.Retention.ScalingEvents = c.Retention.Audit
	}
	c.normalizeBackupRemotes()
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	if c.Log.Level == "warning" {
		// slog's name for the level is warn; the file may say either.
		c.Log.Level = "warn"
	}
	c.Log.Format = strings.ToLower(strings.TrimSpace(c.Log.Format))
	c.Agent.Backend = strings.ToLower(strings.TrimSpace(c.Agent.Backend))
	// The environment override was always lowercased; the file is now too, so
	// "Self-Signed" in zoomies.yaml is the same mode as self-signed.
	c.Server.TLS.Mode = TLSMode(strings.ToLower(strings.TrimSpace(string(c.Server.TLS.Mode))))
	c.Status.Mode = StatusMode(strings.ToLower(strings.TrimSpace(string(c.Status.Mode))))
	if c.Status.Mode == "" {
		c.Status.Mode = StatusOff
	}
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
	// A bare Enterprise Server hostname is accepted, as the docs promise: it
	// gains its scheme and /api/v3 here rather than failing validation for the
	// lack of them. github.com in any spelling is left as the default reads.
	if s := strings.TrimSpace(c.GitHub.APIBaseURL); s != "" && !strings.Contains(s, "api.github.com") && s != "https://github.com" {
		if n, err := NormalizeGitHubAPIBaseURL(s); err == nil {
			c.GitHub.APIBaseURL = strings.TrimRight(n, "/")
		}
	}
	c.CapacityDemand.DestinationURL = strings.TrimSpace(c.CapacityDemand.DestinationURL)
	if c.Agent.Name == "" {
		// A host named for what it is -- "zoomies-16vcpu-32gb-ubuntu-2404-
		// build01" -- answers at a glance the questions a bare hostname makes
		// an operator open three pages to answer.
		c.Agent.Name = machine.DefaultHostName()
	}
	if c.Security.CookieSecure == nil {
		secure := strings.HasPrefix(c.Server.ExternalURL, "https://") || c.Server.TLS.Mode != TLSOff
		c.Security.CookieSecure = &secure
	}
}

// NormalizeGitHubAPIBaseURL turns whatever an operator wrote for a GitHub API
// base into the form the client needs: github.com in any spelling becomes
// https://api.github.com/, a GHE.com tenant becomes its api. host, and an
// Enterprise Server host gains https:// and /api/v3 when it lacks them. The
// result always ends in a slash.
func NormalizeGitHubAPIBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "https://github.com" || s == "https://api.github.com" {
		return "https://api.github.com/", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	s = strings.TrimRight(s, "/")
	if tenant, ok := gheComTenant(s); ok {
		return tenant, nil
	}
	if !strings.HasSuffix(s, "/api/v3") && !strings.HasSuffix(s, "/api/uploads") &&
		!strings.Contains(s, "api.github.com") {
		s += "/api/v3"
	}
	return s + "/", nil
}

// gheComSuffix is the domain GitHub Enterprise Cloud with data residency
// serves every tenant from.
const gheComSuffix = ".ghe.com"

// gheComTenant recognises a GHE.com tenant and returns its API base.
//
// GHE.com is laid out like github.com rather than like Enterprise Server: the
// REST API is its own host, api.<tenant>.ghe.com, at the root. Treated as an
// Enterprise Server it gained /api/v3, and every request -- the App's token,
// every JIT config -- came back 404, which reads as a wrong App ID rather than
// a wrong URL. So the tenant's web address, the api. host, and either with a
// stray /api/v3 all arrive at the one base that works. Any other host under
// the domain -- uploads.<tenant>.ghe.com for upload_base_url -- keeps its name
// and loses only the path.
func gheComTenant(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	labels, ok := strings.CutSuffix(host, gheComSuffix)
	if !ok || labels == "" {
		return "", false
	}
	if !strings.Contains(labels, ".") {
		host = "api." + host
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return "https://" + host + "/", true
}

// IsGHECom reports whether an API base URL belongs to a GHE.com tenant.
func IsGHECom(apiBaseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(apiBaseURL))
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Hostname()), gheComSuffix)
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

// LikelyReachable reports whether anything other than this machine can reach
// the controller.
//
// It is deliberately broader than BindsPublicly. A loopback bind is only
// private when nothing forwards to it, and the deployment this project
// recommends -- loopback plus a reverse proxy -- is precisely the case where
// the bind address says "private" and the truth is "the internet". An external
// URL or a configured trusted proxy is the operator telling us, in the
// configuration itself, that something in front does forward to this listener.
func (c *Config) LikelyReachable() bool {
	return c.BindsPublicly() ||
		strings.TrimSpace(c.Server.ExternalURL) != "" ||
		len(c.Server.TrustedProxies) > 0
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
	// Any other name is assumed to resolve off-host, except the ones that
	// never can.
	return !loopbackHost(host)
}

// loopbackHost reports whether a host name or address can only ever be this
// machine: a loopback IP, localhost, or a name under .localhost, which RFC 6761
// reserves for exactly that. It is the one answer to the question the bind
// address, the external URL, the allowed origins and the OIDC issuer all ask,
// so that "localhost" cannot count as local in one of them and public in
// another, as it once did between external_url and bind.
// LoopbackHost reports whether a hostname names only this machine.
//
// It is exported because the same question is asked about a backup
// destination's endpoint, which the controller warns about and this package
// cannot see: a bucket on loopback is a developer's MinIO, and one anywhere
// else over plain HTTP is credentials on somebody's network.
func LoopbackHost(host string) bool { return loopbackHost(host) }

func loopbackHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	return loopbackHost(u.Hostname())
}

// applyEnv overlays this process's ZOOMIES_* environment variables.
func (c *Config) applyEnv() error { return c.applyEnvFrom(os.LookupEnv) }

// applyEnvFrom overlays the ZOOMIES_* variables of an environment, which is
// this process's for everything but a caller working out another process's
// configuration -- see Effective.
func (c *Config) applyEnvFrom(lookup func(string) (string, bool)) error {
	getenv := func(k string) string { v, _ := lookup(k); return v }
	var errs []string
	for _, s := range Settings() {
		raw, ok := lookup(s.Env)
		if !ok {
			continue
		}
		if _, err := c.SetValueString(s.Key, raw); err != nil {
			// The variable is what the operator wrote, so the variable is what
			// the message names -- not the dotted key they would have to work
			// back to.
			var se *SettingError
			if errors.As(err, &se) {
				errs = append(errs, fmt.Sprintf("%s: %s", s.Env, se.Reason))
			} else {
				errs = append(errs, fmt.Sprintf("%s: %s", s.Env, err))
			}
			continue
		}
		c.note(s.Key, SourceEnvironment)
	}

	// retention.audit has no registry row -- it is the old spelling of
	// retention.scaling_events rather than a setting of its own -- but a
	// deployment still setting the variable is honoured, and normalize says so.
	if v, ok := lookup("ZOOMIES_RETENTION_AUDIT"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("ZOOMIES_RETENTION_AUDIT=%q is not a duration (try 30s, 5m, 2h)", v))
		} else {
			c.Retention.Audit = d
		}
	}

	// The backup remotes have no registry rows -- they are a list of
	// destinations carrying credentials rather than a setting the page edits --
	// so their environment is read here. A container deployment keeps the
	// bucket in the file and the secret key in the environment, which is the
	// whole reason this exists.
	if err := c.applyRemoteEnv(lookup); err != nil {
		errs = append(errs, err.Error())
	}

	// The bootstrap identity has no registry row either: it is an instruction
	// to an empty database, not a setting -- see Bootstrap.
	c.Bootstrap = Bootstrap{
		Admin:        strings.TrimSpace(getenv("ZOOMIES_BOOTSTRAP_ADMIN")),
		PasswordFile: strings.TrimSpace(getenv("ZOOMIES_BOOTSTRAP_PASSWORD_FILE")),
		TokenFile:    strings.TrimSpace(getenv("ZOOMIES_BOOTSTRAP_TOKEN_FILE")),
	}

	// Docker's own variable is honoured only when Zoomies' is not set. The
	// compose file hands the whole .env to the container, and an operator
	// whose daemon is rootless or remote keeps DOCKER_HOST in that file for
	// docker and compose themselves; read second, it would silently override
	// the socket the compose file names explicitly.
	if _, explicit := lookup("ZOOMIES_DOCKER_HOST"); !explicit {
		if v, ok := lookup("DOCKER_HOST"); ok {
			c.Agent.DockerHost = v
			c.note("agent.docker_host", SourceEnvironment)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid environment configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// backupRemoteEnvPrefixes are the spellings one remote's environment may take:
// ZOOMIES_BACKUP_REMOTE_BUCKET for the first, ZOOMIES_BACKUP_REMOTE_2_BUCKET
// for the second. The unnumbered form is the first remote, because a fleet
// with one offsite bucket should not have to count.
const backupRemoteEnvMax = 9

// applyRemoteEnv overlays ZOOMIES_BACKUP_REMOTE_* onto backup.remotes.
//
// A variable that names a remote the file already has fills that one in; one
// that names a remote past the end of the list appends it. So a compose file
// can hand over only the secret key of a bucket zoomies.yaml describes, or
// describe the whole destination with no file at all.
func (c *Config) applyRemoteEnv(lookup func(string) (string, bool)) error {
	var errs []string
	touched := false
	for i := 1; i <= backupRemoteEnvMax; i++ {
		prefixes := []string{fmt.Sprintf("ZOOMIES_BACKUP_REMOTE_%d_", i)}
		if i == 1 {
			prefixes = append(prefixes, "ZOOMIES_BACKUP_REMOTE_")
		}
		// Each field remembers which variable it came from, so an error names
		// the one the operator actually wrote rather than the other spelling
		// of it.
		type envField struct{ variable, value string }
		fields := map[string]envField{}
		for _, prefix := range prefixes {
			for _, name := range []string{
				"NAME", "ENDPOINT", "REGION", "BUCKET", "PREFIX",
				"ACCESS_KEY_ID", "SECRET_ACCESS_KEY", "PASSPHRASE",
				"PATH_STYLE", "KEEP", "DISABLED",
			} {
				if v, ok := lookup(prefix + name); ok {
					// The numbered spelling is read first and wins, so a file
					// that sets both is not ambiguous about which it meant.
					if _, already := fields[name]; !already {
						fields[name] = envField{variable: prefix + name, value: v}
					}
				}
			}
		}
		if len(fields) == 0 {
			continue
		}

		// A name that matches a remote the file already describes overlays
		// that one wherever it sits in the list, which is what an operator
		// who wrote the variables by name expects.
		idx := i - 1
		if name := strings.TrimSpace(fields["NAME"].value); name != "" {
			for n, existing := range c.Backup.Remotes {
				if strings.EqualFold(strings.TrimSpace(existing.Name), name) {
					idx = n
					break
				}
			}
		}
		for len(c.Backup.Remotes) <= idx {
			c.Backup.Remotes = append(c.Backup.Remotes, BackupRemote{})
		}
		r := &c.Backup.Remotes[idx]
		for name, field := range fields {
			raw := field.value
			switch name {
			case "NAME":
				r.Name = raw
			case "ENDPOINT":
				r.Endpoint = raw
			case "REGION":
				r.Region = raw
			case "BUCKET":
				r.Bucket = raw
			case "PREFIX":
				r.Prefix = raw
			case "ACCESS_KEY_ID":
				r.AccessKeyID = raw
			case "SECRET_ACCESS_KEY":
				r.SecretAccessKey = raw
			case "PASSPHRASE":
				r.Passphrase = raw
			case "PATH_STYLE":
				b, err := strconv.ParseBool(strings.TrimSpace(raw))
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s=%q is not true or false", field.variable, raw))
					continue
				}
				r.PathStyle = &b
			case "KEEP":
				n, err := strconv.Atoi(strings.TrimSpace(raw))
				if err != nil || n < 0 {
					errs = append(errs, fmt.Sprintf("%s=%q is not a number of copies to keep", field.variable, raw))
					continue
				}
				r.Keep = n
			case "DISABLED":
				b, err := strconv.ParseBool(strings.TrimSpace(raw))
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s=%q is not true or false", field.variable, raw))
					continue
				}
				r.Disabled = b
			}
		}
		touched = true
	}
	if touched {
		c.note("backup.remotes", SourceEnvironment)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n  - "))
	}
	return nil
}

// normalizeBackupRemotes tidies what the file and the environment left, and
// names the ones nobody named: the name is what the log, the problems drawer
// and the API route all address a destination by, so every remote has to have
// one whether or not an operator thought of it.
func (c *Config) normalizeBackupRemotes() {
	for i := range c.Backup.Remotes {
		r := &c.Backup.Remotes[i]
		r.Name = strings.ToLower(strings.TrimSpace(r.Name))
		if r.Name == "" {
			r.Name = "offsite"
			if i > 0 {
				r.Name = fmt.Sprintf("offsite-%d", i+1)
			}
		}
		r.Endpoint = strings.TrimRight(strings.TrimSpace(r.Endpoint), "/")
		r.Region = strings.TrimSpace(r.Region)
		r.Bucket = strings.TrimSpace(r.Bucket)
		r.Prefix = strings.Trim(strings.TrimSpace(r.Prefix), "/")
		r.AccessKeyID = strings.TrimSpace(r.AccessKeyID)
		r.SecretAccessKey = strings.TrimSpace(r.SecretAccessKey)
		if r.Keep < 0 {
			r.Keep = 0
		}
	}
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

// Normalize fills in the values that are derived from other values. It is
// exported for the settings API, which builds a candidate configuration by
// assigning into a copy and has to derive the rest of it before asking the
// validator whether the result would start.
func (c *Config) Normalize() { c.normalize() }

// readFile and decodeYAML are Load's two halves, separated so the seed importer
// can read the same file the same strict way without also applying the
// environment over it -- which is exactly what it must not do.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func decodeYAML(doc []byte, into *Config) error {
	dec := yaml.NewDecoder(strings.NewReader(string(doc)))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
