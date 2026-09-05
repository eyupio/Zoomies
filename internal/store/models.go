// Package store owns Zoomies' persistent state: the SQLite schema, the domain
// types that map onto it, and every query the rest of the program runs.
//
// Nothing outside this package touches SQL. Everything the controller needs to
// know across a restart lives here -- deliberately, because the project this
// one replaces scattered runtime state across dotfiles inside runner working
// directories, where it could not be queried, backed up or audited.
package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Enumerations
// ---------------------------------------------------------------------------

// TargetType says whether an installation manages runners for a whole
// organisation or for a single repository.
type TargetType string

const (
	TargetOrg  TargetType = "org"
	TargetRepo TargetType = "repo"
)

func (t TargetType) Valid() bool { return t == TargetOrg || t == TargetRepo }

// BackendKind selects how an agent materialises a runner on its host.
type BackendKind string

const (
	BackendDocker  BackendKind = "docker"
	BackendPodman  BackendKind = "podman"
	BackendProcess BackendKind = "process"
)

func (b BackendKind) Valid() bool {
	switch b {
	case BackendDocker, BackendPodman, BackendProcess:
		return true
	}
	return false
}

type PullPolicy string

const (
	PullIfNotPresent PullPolicy = "if-not-present"
	PullAlways       PullPolicy = "always"
	PullPinnedOnly   PullPolicy = "pinned-only"
)

func (p PullPolicy) Valid() bool {
	return p == PullIfNotPresent || p == PullAlways || p == PullPinnedOnly
}

// DockerMode controls whether jobs running on a pool's runners can themselves
// talk to a Docker daemon. Both non-none values weaken isolation, so both are
// surfaced as warnings by the config validator and in the UI problems drawer.
type DockerMode string

const (
	// DockerNone is the default: jobs get no Docker daemon at all.
	DockerNone DockerMode = "none"
	// DockerDinD runs a private, per-runner dockerd sidecar. The job can build
	// images but cannot see the host's containers.
	DockerDinD DockerMode = "dind"
	// DockerHostSocket bind-mounts the host's docker.sock into the runner.
	// Any job on this pool can trivially become root on the host.
	DockerHostSocket DockerMode = "host-socket"
)

func (d DockerMode) Valid() bool {
	switch d {
	case DockerNone, DockerDinD, DockerHostSocket:
		return true
	}
	return false
}

// Dangerous reports whether this mode materially weakens the isolation between
// a workflow job and the host it runs on.
func (d DockerMode) Dangerous() bool { return d == DockerHostSocket }

// RunnerState is the runner lifecycle state machine:
//
//	provisioning -> registering -> idle -> busy -> draining -> removed
//	                                   \-> failed (from any state)
type RunnerState string

const (
	RunnerProvisioning RunnerState = "provisioning"
	RunnerRegistering  RunnerState = "registering"
	RunnerIdle         RunnerState = "idle"
	RunnerBusy         RunnerState = "busy"
	RunnerDraining     RunnerState = "draining"
	RunnerRemoved      RunnerState = "removed"
	RunnerFailed       RunnerState = "failed"
)

func (s RunnerState) Valid() bool {
	switch s {
	case RunnerProvisioning, RunnerRegistering, RunnerIdle, RunnerBusy,
		RunnerDraining, RunnerRemoved, RunnerFailed:
		return true
	}
	return false
}

// Terminal reports whether a runner in this state will never do more work.
func (s RunnerState) Terminal() bool { return s == RunnerRemoved || s == RunnerFailed }

// Live reports whether a runner in this state counts against a pool's max, i.e.
// it exists (or is about to exist) and consumes host capacity.
func (s RunnerState) Live() bool { return !s.Terminal() }

// Assignable reports whether GitHub could hand this runner a job right now.
func (s RunnerState) Assignable() bool { return s == RunnerIdle || s == RunnerBusy }

// validRunnerTransitions is the allow-list enforced by Store.TransitionRunner.
// Keeping it here rather than in the caller means an agent cannot report a
// nonsensical state and corrupt the fleet's accounting.
var validRunnerTransitions = map[RunnerState][]RunnerState{
	RunnerProvisioning: {RunnerRegistering, RunnerDraining, RunnerFailed, RunnerRemoved},
	RunnerRegistering:  {RunnerIdle, RunnerBusy, RunnerDraining, RunnerFailed, RunnerRemoved},
	RunnerIdle:         {RunnerBusy, RunnerDraining, RunnerFailed, RunnerRemoved},
	RunnerBusy:         {RunnerIdle, RunnerDraining, RunnerFailed, RunnerRemoved},
	RunnerDraining:     {RunnerRemoved, RunnerFailed},
	RunnerRemoved:      {},
	RunnerFailed:       {RunnerRemoved},
}

// CanTransition reports whether from -> to is a legal runner state change.
func CanTransition(from, to RunnerState) bool {
	if from == to {
		return true
	}
	return slices.Contains(validRunnerTransitions[from], to)
}

// JobState mirrors the lifecycle GitHub reports over workflow_job webhooks.
type JobState string

const (
	JobQueued     JobState = "queued"
	JobInProgress JobState = "in_progress"
	JobCompleted  JobState = "completed"
)

func (s JobState) Valid() bool {
	switch s {
	case JobQueued, JobInProgress, JobCompleted:
		return true
	}
	return false
}

// Role is the RBAC level attached to a user or an API token.
type Role string

const (
	// RoleViewer may read everything except secrets.
	RoleViewer Role = "viewer"
	// RoleOperator may additionally act on the fleet: drain, delete, restart
	// runners, cordon hosts, and create or edit pools.
	RoleOperator Role = "operator"
	// RoleAdmin may additionally manage users, tokens, installations and settings.
	RoleAdmin Role = "admin"
)

func (r Role) Valid() bool {
	switch r {
	case RoleViewer, RoleOperator, RoleAdmin:
		return true
	}
	return false
}

// rank orders roles so that AtLeast can compare them.
func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// AtLeast reports whether r carries at least the authority of want.
func (r Role) AtLeast(want Role) bool { return r.rank() >= want.rank() && r.rank() > 0 }

// ---------------------------------------------------------------------------
// Helper column types
// ---------------------------------------------------------------------------

// StringSlice is a []string persisted as a JSON array in a TEXT column.
type StringSlice []string

func (s *StringSlice) Scan(v any) error {
	*s = nil
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if len(t) == 0 {
			return nil
		}
		return json.Unmarshal(t, s)
	case string:
		if t == "" {
			return nil
		}
		return json.Unmarshal([]byte(t), s)
	}
	return fmt.Errorf("store: cannot scan %T into StringSlice", v)
}

func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		s = StringSlice{}
	}
	b, err := json.Marshal([]string(s))
	return string(b), err
}

// StringMap is a map[string]string persisted as a JSON object in a TEXT column.
type StringMap map[string]string

func (m *StringMap) Scan(v any) error {
	*m = nil
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if len(t) == 0 {
			return nil
		}
		return json.Unmarshal(t, m)
	case string:
		if t == "" {
			return nil
		}
		return json.Unmarshal([]byte(t), m)
	}
	return fmt.Errorf("store: cannot scan %T into StringMap", v)
}

func (m StringMap) Value() (driver.Value, error) {
	if m == nil {
		m = StringMap{}
	}
	b, err := json.Marshal(map[string]string(m))
	return string(b), err
}

// HostBackend is one line of an agent's capability probe: what it found, or
// what stopped it finding anything.
//
// It mirrors backend.Info, which the store cannot name because the backend
// package is built on top of this one. Only the fields an operator or the
// scheduler needs are kept; the host's Docker socket path is deliberately not
// among them, since nothing reads it back and it is a detail of the machine
// rather than of the fleet.
type HostBackend struct {
	Kind BackendKind `json:"kind"`
	// Available is false when the daemon did not answer. Detail then explains
	// why in the agent's own words, which is usually the whole fix.
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Rootless  bool   `json:"rootless"`
	Endpoint  string `json:"endpoint,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// SupportsDinD reports whether this backend can give a job its own Docker
	// daemon in a sidecar.
	SupportsDinD bool `json:"supports_dind"`
}

// HostBackends is a probe result persisted as a JSON array in a TEXT column.
type HostBackends []HostBackend

func (b *HostBackends) Scan(v any) error {
	*b = nil
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if len(t) == 0 {
			return nil
		}
		return json.Unmarshal(t, b)
	case string:
		if t == "" {
			return nil
		}
		return json.Unmarshal([]byte(t), b)
	}
	return fmt.Errorf("store: cannot scan %T into HostBackends", v)
}

func (b HostBackends) Value() (driver.Value, error) {
	if b == nil {
		b = HostBackends{}
	}
	s, err := json.Marshal([]HostBackend(b))
	return string(s), err
}

// Kinds returns the backends that answered, sorted, which is what a pool is
// matched against.
func (b HostBackends) Kinds() StringSlice {
	var out StringSlice
	for _, i := range b {
		if i.Available {
			out = append(out, string(i.Kind))
		}
	}
	slices.Sort(out)
	return out
}

// Find returns what the probe said about one backend, if it said anything.
func (b HostBackends) Find(kind BackendKind) (HostBackend, bool) {
	for _, i := range b {
		if i.Kind == kind {
			return i, true
		}
	}
	return HostBackend{}, false
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Installation is one GitHub App installation and the org or repo it targets.
// APIBaseURL and UploadBaseURL are non-empty for GitHub Enterprise Server.
type Installation struct {
	ID             string     `json:"id"`
	AppID          int64      `json:"app_id"`
	InstallationID int64      `json:"installation_id"`
	Target         string     `json:"target"`      // "acme" or "acme/widgets"
	TargetType     TargetType `json:"target_type"` // org | repo
	APIBaseURL     string     `json:"api_base_url"`
	UploadBaseURL  string     `json:"upload_base_url"`
	// PrivateKeyEnc holds the PEM private key sealed with the instance key.
	// It is never rendered by the API.
	PrivateKeyEnc []byte `json:"-"`
	// WebhookSecretEnc holds the sealed HMAC secret for this installation.
	WebhookSecretEnc []byte    `json:"-"`
	AppSlug          string    `json:"app_slug"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	// Health fields, refreshed by the controller's installation prober.
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// Healthy reports whether the last credential check succeeded.
func (i *Installation) Healthy() bool { return i.LastError == "" }

// Resources caps what a single runner may consume on its host. Zero means
// "unlimited", which is the backend's own default.
type Resources struct {
	CPUs      float64 `json:"cpus,omitempty"`       // e.g. 2 => --cpus=2
	MemoryMB  int64   `json:"memory_mb,omitempty"`  // e.g. 4096
	DiskGB    int64   `json:"disk_gb,omitempty"`    // advisory; enforced where the backend can
	PidsLimit int64   `json:"pids_limit,omitempty"` // container pids cgroup limit
}

type CacheScope string

const (
	CacheScopePool       CacheScope = "pool"
	CacheScopeRepository CacheScope = "repository"
)

func (s CacheScope) Valid() bool { return s == CacheScopePool || s == CacheScopeRepository }

// CacheConfig describes disposable accelerator data mounted at /opt/zoomies-cache.
// It is not persistent workflow storage and may be evicted. Source is either an
// absolute host directory prefix or a named-volume prefix.
type CacheConfig struct {
	Enabled   bool       `json:"enabled"`
	Scope     CacheScope `json:"scope"`
	SizeLimit int64      `json:"size_limit,omitempty"` // approximate bytes; zero is unlimited
	Source    string     `json:"source,omitempty"`
}

// Pool is a named group of interchangeable runners: what labels they answer to,
// how they are built, and how many of them may exist.
type Pool struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	InstallationID string      `json:"installation_id"`
	Labels         StringSlice `json:"labels"`
	RunnerGroup    string      `json:"runner_group,omitempty"`
	Backend        BackendKind `json:"backend"`
	Image          string      `json:"image"`
	PullPolicy     PullPolicy  `json:"pull_policy"`
	RunnerVersion  string      `json:"runner_version,omitempty"`
	MinRunners     int         `json:"min_runners"`
	MaxRunners     int         `json:"max_runners"`
	// RepositoryConcurrencyLimit caps active runners attributable to any one
	// repository. Zero leaves the pool unrestricted.
	RepositoryConcurrencyLimit int `json:"repository_concurrency_limit,omitempty"`
	// CostPerRunnerHour is administrator supplied; Zoomies never embeds prices.
	CostPerRunnerHour *float64    `json:"cost_per_runner_hour,omitempty"`
	Priority          int         `json:"priority"`
	IdleTimeout       Duration    `json:"idle_timeout"`
	Ephemeral         bool        `json:"ephemeral"`
	DockerMode        DockerMode  `json:"docker_mode"`
	Resources         Resources   `json:"resources"`
	Cache             CacheConfig `json:"cache"`
	// HostSelector matches Host.Labels; empty means "any host".
	HostSelector StringMap `json:"host_selector"`
	// Env is injected into every runner this pool creates.
	Env StringMap `json:"env"`
	// RunAsRoot disables the backend's default of dropping to an unprivileged
	// user inside the runner. Flagged as dangerous.
	RunAsRoot bool      `json:"run_as_root"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PoolPrewarm struct {
	PoolID    string    `json:"pool_id"`
	HostID    string    `json:"host_id"`
	HostName  string    `json:"host_name,omitempty"`
	Image     string    `json:"image"`
	State     string    `json:"state"`
	Digest    string    `json:"digest,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Duration is a time.Duration that marshals to and from a Go duration string
// ("5m", "1h30s") so config files and API payloads stay readable.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string          { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		p, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(p)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\" or a nanosecond count")
	}
	*d = Duration(n)
	return nil
}

// UnmarshalYAML lets zoomies.yaml write idle_timeout: 5m.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		var n int64
		if err2 := unmarshal(&n); err2 != nil {
			return fmt.Errorf("duration must be a string like \"5m\"")
		}
		*d = Duration(time.Duration(n) * time.Second)
		return nil
	}
	p, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(p)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Dangerous returns the list of pool settings that weaken the default security
// posture, phrased for direct display in the UI's problems drawer.
func (p *Pool) Dangerous() []string {
	var out []string
	if !p.Ephemeral {
		out = append(out, "persistent runners: job state and credentials leak between workflow runs")
	}
	if p.DockerMode == DockerHostSocket {
		out = append(out, "host docker socket mounted: any job on this pool can become root on the host")
	}
	if p.DockerMode == DockerDinD {
		out = append(out, "docker-in-docker sidecar: runners get a privileged container")
	}
	if p.RunAsRoot {
		out = append(out, "runners execute as root inside the container")
	}
	return out
}

// Host is an agent process and the machine it runs on.
type Host struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Address is how the host reported itself; the controller never dials it.
	// Agents always connect outbound to the controller.
	Address string `json:"address,omitempty"`
	// Embedded marks the agent that runs inside the controller process.
	Embedded bool `json:"embedded"`
	// Capacity is the maximum number of concurrent runners this host accepts.
	Capacity int `json:"capacity"`
	// Backends lists the runner backends this host can actually service. It is
	// what a pool's backend is matched against, so it holds only the kinds the
	// agent found available.
	Backends StringSlice `json:"backends"`
	// BackendInfo is the agent's last full probe, including the backends that
	// were not available and why. It is what the UI shows an operator whose
	// host is not taking work; the scheduler reads Backends, never this.
	BackendInfo HostBackends `json:"backend_info,omitempty"`
	Labels      StringMap    `json:"labels"`
	OS          string       `json:"os"`
	Arch        string       `json:"arch"`
	Version     string       `json:"version"`
	// Cordoned hosts keep their existing runners but accept no new ones.
	Cordoned      bool      `json:"cordoned"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	CreatedAt     time.Time `json:"created_at"`
	// TokenHash authenticates the agent on every request.
	TokenHash string `json:"-"`
	// Live counters, filled by the store on read.
	ActiveRunners int `json:"active_runners"`
}

// HeartbeatTimeout is how long a host may go silent before it is considered
// unhealthy. It is three times the agent's default heartbeat interval.
const HeartbeatTimeout = 90 * time.Second

// Healthy reports whether the agent has checked in recently enough.
func (h *Host) Healthy(now time.Time) bool {
	return now.Sub(h.LastHeartbeat) < HeartbeatTimeout
}

// Available reports whether the scheduler may place a new runner here.
func (h *Host) Available(now time.Time) bool {
	return h.Healthy(now) && !h.Cordoned && h.ActiveRunners < h.Capacity
}

// Free returns the number of additional runners this host can take.
func (h *Host) Free() int {
	if n := h.Capacity - h.ActiveRunners; n > 0 {
		return n
	}
	return 0
}

// Runner is one runner instance: a row that the controller creates in
// "provisioning" and an agent then materialises, reports on, and tears down.
type Runner struct {
	ID     string      `json:"id"`
	PoolID string      `json:"pool_id"`
	HostID string      `json:"host_id"`
	Name   string      `json:"name"`
	State  RunnerState `json:"state"`
	// GitHubRunnerID is assigned once GitHub acknowledges the registration.
	GitHubRunnerID int64 `json:"github_runner_id,omitempty"`
	// ContainerID (or PID, for the process backend) identifies the workload
	// on the host. Opaque to the controller.
	ContainerID   string      `json:"container_id,omitempty"`
	Ephemeral     bool        `json:"ephemeral"`
	Labels        StringSlice `json:"labels"`
	Image         string      `json:"image,omitempty"`
	ImageDigest   string      `json:"image_digest,omitempty"`
	RunnerVersion string      `json:"runner_version,omitempty"`
	// CurrentJobID points at the jobs row this runner is executing, if any.
	CurrentJobID       string         `json:"current_job_id,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	ImagePullDuration  *time.Duration `json:"image_pull_duration,omitempty"`
	ContainerStartedAt *time.Time     `json:"container_started_at,omitempty"`
	RegisteredAt       *time.Time     `json:"registered_at,omitempty"`
	StartedAt          *time.Time     `json:"started_at,omitempty"`
	// LastIdleAt is when the runner most recently became idle; the scale-down
	// path measures the idle timeout from here.
	LastIdleAt  *time.Time `json:"last_idle_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Message     string     `json:"message,omitempty"`
	JobsHandled int        `json:"jobs_handled"`
	// CPUPercent and MemoryBytes are best-effort samples from the agent.
	CPUPercent  float64 `json:"cpu_percent,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
}

// Age returns how long the runner has existed.
func (r *Runner) Age(now time.Time) time.Duration { return now.Sub(r.CreatedAt) }

// IdleFor returns how long the runner has been idle, or 0 if it is not idle.
func (r *Runner) IdleFor(now time.Time) time.Duration {
	if r.State != RunnerIdle || r.LastIdleAt == nil {
		return 0
	}
	return now.Sub(*r.LastIdleAt)
}

// Job is a GitHub Actions workflow job as Zoomies observed it.
type Job struct {
	ID          string      `json:"id"`
	GitHubJobID int64       `json:"github_job_id"`
	GitHubRunID int64       `json:"github_run_id"`
	Repo        string      `json:"repo"`     // "acme/widgets"
	Workflow    string      `json:"workflow"` // workflow name
	JobName     string      `json:"job_name"`
	Labels      StringSlice `json:"labels"`
	State       JobState    `json:"state"`
	Conclusion  string      `json:"conclusion,omitempty"` // success|failure|cancelled|skipped
	PoolID      string      `json:"pool_id,omitempty"`
	RunnerID    string      `json:"runner_id,omitempty"`
	RunnerName  string      `json:"runner_name,omitempty"`
	HTMLURL     string      `json:"html_url,omitempty"`
	QueuedAt    time.Time   `json:"queued_at"`
	StartedAt   *time.Time  `json:"started_at,omitempty"`
	CompletedAt *time.Time  `json:"completed_at,omitempty"`
	// Matched records whether any enabled pool claimed this job's labels. An
	// unmatched queued job is a configuration problem worth surfacing.
	Matched bool `json:"matched"`
}

// QueueWait returns how long the job waited before a runner picked it up.
func (j *Job) QueueWait() time.Duration {
	if j.StartedAt == nil {
		return 0
	}
	return j.StartedAt.Sub(j.QueuedAt)
}

// Duration returns how long the job executed, or 0 if it has not finished.
func (j *Job) Duration() time.Duration {
	if j.StartedAt == nil || j.CompletedAt == nil {
		return 0
	}
	return j.CompletedAt.Sub(*j.StartedAt)
}

// AuditEvent records one mutating action, who performed it, and what changed.
type AuditEvent struct {
	ID         string    `json:"id"`
	ActorID    string    `json:"actor_id"`
	ActorName  string    `json:"actor_name"`
	ActorKind  string    `json:"actor_kind"` // user | token | agent | system | webhook
	Action     string    `json:"action"`     // "pool.create", "runner.drain", ...
	TargetKind string    `json:"target_kind"`
	TargetID   string    `json:"target_id"`
	Before     string    `json:"before,omitempty"` // JSON, secrets redacted
	After      string    `json:"after,omitempty"`
	IP         string    `json:"ip,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// CapacityDemandDelivery is the durable deduplication and delivery record for
// one kind of capacity signal for a pool.
type CapacityDemandDelivery struct {
	PoolID, EventType, EventID, Payload string
	ObservedSince                       time.Time
	AttemptedAt, DeliveredAt            *time.Time
	StatusCode, Attempts                int
	LastError                           string
}

// ScalingEvent records one scheduler decision, with the reason in the words the
// UI shows: "scaled linux-x64 2 -> 4: 3 jobs queued > 30s".
type ScalingEvent struct {
	ID        string    `json:"id"`
	PoolID    string    `json:"pool_id"`
	PoolName  string    `json:"pool_name"`
	From      int       `json:"from"`
	To        int       `json:"to"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// Direction returns "up", "down" or "hold".
func (e *ScalingEvent) Direction() string {
	switch {
	case e.To > e.From:
		return "up"
	case e.To < e.From:
		return "down"
	}
	return "hold"
}

// WebhookDelivery records one inbound webhook so that delivery failures are
// visible instead of silently dropped.
type WebhookDelivery struct {
	ID         string    `json:"id"`
	DeliveryID string    `json:"delivery_id"`
	Event      string    `json:"event"`
	Action     string    `json:"action,omitempty"`
	Repo       string    `json:"repo,omitempty"`
	Status     string    `json:"status"` // accepted | rejected | error
	Error      string    `json:"error,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

// User is a local account authenticated with an argon2id password hash.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	Role         Role   `json:"role"`
	PasswordHash string `json:"-"`
	// Subject is set for accounts provisioned through OIDC; such accounts have
	// no password hash and cannot log in with one.
	OIDCSubject string     `json:"oidc_subject,omitempty"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	// MustChangePassword is set for the bootstrap admin created by the installer.
	MustChangePassword bool `json:"must_change_password"`
}

// Session is a browser login session, keyed by a hashed cookie value.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	TokenHash string    `json:"-"`
	UserAgent string    `json:"user_agent,omitempty"`
	IP        string    `json:"ip,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// APIToken is a long-lived credential for automation, scoped by role and
// optionally limited to a set of pools.
type APIToken struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   Role   `json:"role"`
	UserID string `json:"user_id,omitempty"`
	// Scopes optionally narrows a token further than its role, e.g.
	// ["pools:read", "runners:write"].
	Scopes     StringSlice `json:"scopes"`
	TokenHash  string      `json:"-"`
	Prefix     string      `json:"prefix"` // first chars, shown in the UI to identify a token
	CreatedAt  time.Time   `json:"created_at"`
	ExpiresAt  *time.Time  `json:"expires_at,omitempty"`
	LastUsedAt *time.Time  `json:"last_used_at,omitempty"`
	Revoked    bool        `json:"revoked"`
}

// Expired reports whether the token is past its expiry.
func (t *APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

// JoinToken is a short-lived, single-use credential that lets a new agent
// register itself with the controller.
type JoinToken struct {
	ID        string     `json:"id"`
	TokenHash string     `json:"-"`
	Prefix    string     `json:"prefix"`
	CreatedBy string     `json:"created_by"`
	Labels    StringMap  `json:"labels"`
	Capacity  int        `json:"capacity"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	UsedByID  string     `json:"used_by_id,omitempty"`
}

// Usable reports whether the token can still be redeemed.
func (t *JoinToken) Usable(now time.Time) bool {
	return t.UsedAt == nil && now.Before(t.ExpiresAt)
}

// Setting is a single key/value row in the settings table. Values marked secret
// are stored sealed and never leave the process in plaintext.
type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Secret    bool      `json:"secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Label matching
// ---------------------------------------------------------------------------

// ImplicitLabels are the labels the actions/runner binary always advertises.
// Workflows list them in runs-on, but they say nothing about what a pool must
// provide, so matching ignores them.
var ImplicitLabels = map[string]bool{
	"self-hosted": true,
	"linux":       true,
	"windows":     true,
	"macos":       true,
	"x64":         true,
	"arm":         true,
	"arm64":       true,
}

// NormalizeLabel lowercases and trims a label. GitHub treats runner labels
// case-insensitively, and operators reliably get the case wrong.
func NormalizeLabel(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NormalizeLabels returns a de-duplicated, lowercased, sorted copy.
func NormalizeLabels(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, l := range in {
		n := NormalizeLabel(l)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
