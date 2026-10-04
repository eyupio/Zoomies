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
	"math"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/naming"
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

// GivesDaemon reports whether jobs on a pool with this mode get a Docker
// daemon at all. It is the question the pool's image has to answer as well: a
// daemon is worth nothing to a job whose image has no client to reach it with,
// so the image a pool runs is decided by this rather than by the mode's name.
func (d DockerMode) GivesDaemon() bool { return d == DockerDinD || d == DockerHostSocket }

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
	// JobWaiting is a job GitHub is holding for a deployment review. It is not
	// demand: nothing may run it until somebody approves it, and GitHub sends
	// a real "queued" delivery when they do. Recording it as queued made the
	// scheduler start a runner that idled out and was started again on the
	// next pass, for as long as the review took.
	JobWaiting    JobState = "waiting"
	JobQueued     JobState = "queued"
	JobInProgress JobState = "in_progress"
	JobCompleted  JobState = "completed"
)

func (s JobState) Valid() bool {
	switch s {
	case JobWaiting, JobQueued, JobInProgress, JobCompleted:
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
	// RolePlatform is whoever runs the process rather than the fleet: the
	// address it binds, the files it reads, the timers it keeps, the fence
	// it lifts and the backups it holds.
	//
	// It sits above administrator because an instance can be operated by
	// one team for another -- a platform team running Zoomies for a
	// product team -- and until now there was nowhere to put that half:
	// the fleet's administrator was the top rung and read the bind address
	// and the database path along with everything else. On an instance
	// where one team does both, the account that installed it holds this
	// as well, and nothing looks any different.
	RolePlatform Role = "platform"
)

func (r Role) Valid() bool {
	switch r {
	case RoleViewer, RoleOperator, RoleAdmin, RolePlatform:
		return true
	}
	return false
}

// Roles is the closed set, lowest first. It exists so that a refusal can name
// the roles there are rather than the roles somebody remembered: every "use
// viewer, operator or admin" in this repository was written before the fourth
// one, and each was a message telling an operator that a role they had just
// read about in the documentation did not exist.
func Roles() []Role { return []Role{RoleViewer, RoleOperator, RoleAdmin, RolePlatform} }

// RoleList renders the closed set for a sentence: "viewer, operator, admin or
// platform".
func RoleList() string {
	all := Roles()
	names := make([]string, len(all))
	for i, r := range all {
		names[i] = string(r)
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
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
	case RolePlatform:
		return 4
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
	// CPUs and MemoryMB are the machine the daemon runs on, as it reported
	// them, kept beside the limits it can apply because a default CPU share
	// is a request to that daemon: one above its own core count is refused
	// outright, however many cores the agent's machine has.
	CPUs     int   `json:"cpus,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// Limits is whether the daemon behind this backend can enforce a CPU
	// quota, a memory limit and a pids limit at all. A rootless daemon on
	// cgroup v1, or on cgroup v2 with no cpu controller delegated to the user,
	// refuses a container that asks for a CPU quota rather than ignoring it
	// -- so a default limit sent to such a host would fail every create, and
	// the controller has to know before it sends one.
	Limits LimitSupport `json:"limits"`
	// SharedFolder is what the agent said is wrong with the host's shared
	// folder: a containerised agent whose container does not mount it from
	// the host. Empty when nothing is.
	SharedFolder string `json:"shared_folder,omitempty"`
}

// LimitSupport is what a container daemon said about the limits it can apply.
// Known is false from an agent too old to ask, and nothing may read that as
// "cannot": it is read as "do not default anything here", which is what every
// host did before defaults existed.
type LimitSupport struct {
	Known  bool `json:"known"`
	CPU    bool `json:"cpu"`
	Memory bool `json:"memory"`
	Pids   bool `json:"pids"`
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

// Platform is the machine a pool's runners need, or the machine a host is.
//
// It exists because "backend: docker" says how a runner is made and nothing
// about what it is made of. A pool asking for Ubuntu 24.04 on arm64 must not
// have its runners placed on a Debian amd64 host, and until the fleet can
// state the platform on both sides there is no way to stop it.
//
// Every field is optional. An empty field is a promise nobody made, and
// matches anything -- which is what keeps a fleet built before platforms
// existed working exactly as it did.
type Platform struct {
	// OS is a distribution, normalised by naming.NormalizeOS: ubuntu, debian,
	// fedora, rocky, macos, windows. Not a kernel: "linux" does not say which
	// image a runner should be started from.
	OS string `json:"os,omitempty"`
	// OSVersion is the release, as an operator writes it: "24.04", "12".
	OSVersion string `json:"os_version,omitempty"`
	// Arch is amd64 or arm64, normalised by naming.NormalizeArch.
	Arch string `json:"arch,omitempty"`
}

// Normalized returns the platform with each field in its canonical spelling,
// dropping anything Zoomies does not recognise rather than storing a value the
// scheduler would then never match.
func (p Platform) Normalized() Platform {
	return Platform{
		OS:        naming.NormalizeOS(p.OS),
		OSVersion: strings.TrimSpace(p.OSVersion),
		Arch:      naming.NormalizeArch(p.Arch),
	}
}

// Empty reports whether the platform constrains nothing.
func (p Platform) Empty() bool { return p == Platform{} }

// Matches reports whether a host of platform h can run a pool that asks for p.
//
// Comparison is one-directional on purpose: every field the pool leaves empty
// is a question it did not ask, and a host that has not reported a field
// cannot be ruled out by it either. The result is that adding a platform to a
// pool narrows placement, and never widens it.
func (p Platform) Matches(h Platform) bool {
	p, h = p.Normalized(), h.Normalized()
	if p.OS != "" && h.OS != "" && p.OS != h.OS {
		return false
	}
	if p.Arch != "" && h.Arch != "" && p.Arch != h.Arch {
		return false
	}
	if p.OSVersion != "" && h.OSVersion != "" &&
		naming.CompactVersion(p.OSVersion) != naming.CompactVersion(h.OSVersion) {
		return false
	}
	return true
}

// Describe renders the platform for the UI and for scheduler explanations,
// e.g. "Ubuntu 24.04, arm64". It is empty when the platform promises nothing.
func (p Platform) Describe() string {
	return naming.Spec{OS: p.OS, Version: p.OSVersion, Arch: p.Arch}.Platform()
}

// Resources caps what a single runner may consume on its host. Zero means
// "unlimited", which is the backend's own default.
type Resources struct {
	CPUs      float64 `json:"cpus,omitempty"`       // e.g. 2 => --cpus=2
	MemoryMB  int64   `json:"memory_mb,omitempty"`  // e.g. 4096
	DiskGB    int64   `json:"disk_gb,omitempty"`    // advisory; enforced where the backend can
	PidsLimit int64   `json:"pids_limit,omitempty"` // container pids cgroup limit
	// MinCPUs and MinMemoryMB are the least a runner of a fixed-size pool may
	// be given when no host has room for the full size above. CPUs and
	// MemoryMB stay the standard the fleet places at wherever it can; the
	// minimum is what lets a host a little short of that -- a 30 GB machine
	// under a 32 GB pool -- run the job anyway instead of leaving it queued.
	// Such a runner is given as much of the standard as the host can spare,
	// never less than this. On a pool, zero is "the fleet's": the controller
	// reads runners.minimum_* in its place (controller.EffectiveMinimum)
	// rather than it being copied here, so the stored zero is what keeps the
	// pool following that setting. Zero with no fleet minimum is none.
	MinCPUs     float64 `json:"min_cpus,omitempty"`
	MinMemoryMB int64   `json:"min_memory_mb,omitempty"`
	// DaemonSharePercent is the part of a host-sized slot a docker-in-docker pool
	// gives its daemon; the runner keeps the rest. Zero is the even split. It
	// divides only a share the host chose (SplitWithDaemon): a limit typed above is
	// given to both containers in full whatever this says.
	DaemonSharePercent int `json:"daemon_share_percent,omitempty"`
}

const (
	// DefaultDaemonSharePercent is the even split every pool had before the share
	// could be set.
	DefaultDaemonSharePercent = 50
	// MinDaemonSharePercent and MaxDaemonSharePercent keep either container a real
	// share: a half given almost nothing is no limit at all in effect, which is
	// how one build takes the machine.
	MinDaemonSharePercent = 10
	MaxDaemonSharePercent = 90
)

// DaemonPercent is the daemon's share of a split slot, with zero read as even.
func (r Resources) DaemonPercent() int {
	if r.DaemonSharePercent <= 0 {
		return DefaultDaemonSharePercent
	}
	return r.DaemonSharePercent
}

// PairFactor is how many times the smaller half a slot must hold: two for an even
// split, five when one container takes 80%. A slot's floor is each half's own
// floor scaled by it, so uneven pairs are refused on a host too small for the
// thinner half rather than starved there.
func (r Resources) PairFactor() float64 {
	d := r.DaemonPercent()
	return 100 / float64(min(d, 100-d))
}

// Reducible reports whether a runner of these resources may be placed below
// its standard size: some field has a minimum under its standard. A field the
// pool leaves to the host has a slot's share of that host as its standard, so
// any minimum on it counts -- which is what lets an automatic pool run on a
// host with a free slot but less than a whole share of it left. Whether the
// minimum is below a particular host's share is the scheduler's to judge.
func (r Resources) Reducible() bool {
	return (r.MinCPUs > 0 && (r.CPUs <= 0 || r.MinCPUs < r.CPUs)) ||
		(r.MinMemoryMB > 0 && (r.MemoryMB <= 0 || r.MinMemoryMB < r.MemoryMB))
}

// CPUBurstMode says what a pool does with CPU that its live runners have not
// reserved. The empty value is deliberately off so an upgrade never changes
// an existing pool's quotas.
type CPUBurstMode string

const (
	CPUBurstOff       CPUBurstMode = "off"
	CPUBurstObserve   CPUBurstMode = "observe"
	CPUBurstAutomatic CPUBurstMode = "automatic"
)

func (m CPUBurstMode) Valid() bool {
	return m == "" || m == CPUBurstOff || m == CPUBurstObserve || m == CPUBurstAutomatic
}

// CPUBurstPolicy lets an automatically-sized container runner borrow CPU that
// no live runner or imminent start has been promised. Memory is intentionally
// absent: lowering a live memory limit can kill the job it is meant to help.
type CPUBurstPolicy struct {
	Mode CPUBurstMode `json:"mode,omitempty"`
	// MaxCPUs is the most CPU one logical runner may use. Zero means the
	// host's allocatable CPU. For a DinD runner it covers the runner and its
	// sidecar together, just like an automatic slot does.
	MaxCPUs float64 `json:"max_cpus,omitempty"`
	// SizeForCeiling says whether an automatic pool's runners start with the
	// environment that tells Rust, the JVM and .NET how many CPUs to size
	// their workers for: the ceiling rather than the guarantee. Those
	// toolchains read the quota once, at start, and a loan that arrives later
	// is CPU they have no worker to use. Nil is on, so every automatic pool
	// gets it without an edit; false is an operator turning it off.
	SizeForCeiling *bool `json:"size_for_ceiling,omitempty"`
}

// SizesForCeiling reports whether runners of this policy are started with
// their toolchains sized for the ceiling. Only a pool that really lends CPU
// does: sizing for a ceiling that is never lent only adds context switches.
func (p CPUBurstPolicy) SizesForCeiling() bool {
	return p.Enforces() && (p.SizeForCeiling == nil || *p.SizeForCeiling)
}

func (p CPUBurstPolicy) Observes() bool {
	return p.Mode == CPUBurstObserve || p.Mode == CPUBurstAutomatic
}

func (p CPUBurstPolicy) Enforces() bool { return p.Mode == CPUBurstAutomatic }

// SplitWithDaemon divides limits a runner was given by its host's slot between
// the runner and the docker-in-docker daemon that runs beside it.
//
// It exists because the two containers are one runner. A slot is the unit a
// machine is cut into, and an operator who sets a host to eight slots is
// saying it may carry eight runners -- not four, because half of them brought
// a daemon. So the pair is given one slot's share and splits it, and the
// scheduler charges the host one share for the pair: the arithmetic the page
// shows, the quota the daemon applies, and the room the scheduler counts are
// then the same number, which is what stops a host reading as half idle while
// its containers' quotas add up to every core it has.
//
// The halves are even, because the work is on both sides of the pair: a build
// runs in the daemon, and everything else in the job -- the checkout, the
// toolchain, the test run -- is in the runner. A slot too small to give both
// halves what a runner needs is refused rather than divided, so nothing here
// has to decide which half to starve.
//
// Limits an operator typed are not split: those say what the job may have, the
// daemon is given the same, and scheduler.Reserve charges the host for both.
func (r Resources) SplitWithDaemon() (runner, daemon Resources) {
	return r.SplitWithDaemonShare(DefaultDaemonSharePercent)
}

// SplitWithDaemonShare is SplitWithDaemon with the daemon taking percent of the
// slot. The even split stays the default because the work is on both sides, but
// a pool whose builds all run in the daemon can give it more. A percent outside
// the permitted range is the even split, never a half with no room.
func (r Resources) SplitWithDaemonShare(percent int) (runner, daemon Resources) {
	if percent < MinDaemonSharePercent || percent > MaxDaemonSharePercent {
		percent = DefaultDaemonSharePercent
	}
	runner, daemon = r, r
	if r.CPUs > 0 {
		daemon.CPUs = r.CPUs * float64(percent) / 100
		runner.CPUs = r.CPUs - daemon.CPUs
		if percent == DefaultDaemonSharePercent {
			runner.CPUs = r.CPUs / 2
			daemon.CPUs = r.CPUs - runner.CPUs
		}
	}
	if r.MemoryMB > 0 {
		daemon.MemoryMB = r.MemoryMB * int64(percent) / 100
		runner.MemoryMB = r.MemoryMB - daemon.MemoryMB
		if percent == DefaultDaemonSharePercent {
			runner.MemoryMB = r.MemoryMB / 2
			daemon.MemoryMB = r.MemoryMB - runner.MemoryMB
		}
	}
	// Pids and disk are not divided. A pids limit is a guard against a fork
	// bomb rather than a budget, and halving it would refuse a legitimate
	// build on a pool that never asked for less; disk is the cache and the
	// image store, which the daemon holds for the pair.
	return runner, daemon
}

// RunnerSettings is a pool's answer where it disagrees with the fleet's.
//
// Every field is a pointer, and that is the type's whole meaning: nil is "the
// fleet decides", and it keeps deciding -- a pool that overrides nothing
// follows scheduler.provision_timeout when an operator changes it, rather
// than being frozen on whatever it read the day the pool was made. A field
// that is set overrides it, including when what it is set to is zero, because
// zero is a real answer for each of these: "never fail a runner for taking
// too long to register" is a thing a pool on a slow air-gapped registry has
// every reason to say.
//
// These are the settings whose right answer is a property of the pool rather
// than of the fleet. A pool pulling a twelve-gigabyte Windows image and a
// pool booting Alpine do not agree about how long registering should take,
// and a fleet that has to pick one picks the slower -- which leaves the fast
// pool holding a dead runner's slot for ten minutes. IdleTimeout is not here
// because it is already a pool field, and was from the start.
type RunnerSettings struct {
	// ProvisionTimeout fails a runner of this pool that never finishes
	// registering. Overrides scheduler.provision_timeout.
	ProvisionTimeout *Duration `json:"provision_timeout,omitempty"`
	// DrainTimeout fails a runner of this pool that has been draining this
	// long with no job left on it. Overrides scheduler.drain_timeout.
	DrainTimeout *Duration `json:"drain_timeout,omitempty"`
	// MaxRunnerLifetime drains a runner of this pool that has lived this long,
	// the next time it is not busy. Overrides scheduler.max_runner_lifetime.
	// It never ends a job, exactly as the fleet's own does not.
	MaxRunnerLifetime *Duration `json:"max_runner_lifetime,omitempty"`
	// ScaleUpDelay is how long a job for this pool must have been queued
	// before it counts as demand. Overrides scheduler.scale_up_delay.
	ScaleUpDelay *Duration `json:"scale_up_delay,omitempty"`
	// DockerWait is how long a runner of this pool waits for the Docker
	// daemon it was promised before refusing to take a job. Overrides
	// runners.docker_wait, and means nothing on a pool whose docker_mode is
	// none -- there is no daemon to wait for.
	DockerWait *Duration `json:"docker_wait,omitempty"`
}

// Set reports whether this pool overrides anything at all, which is what the
// UI asks before it draws a panel and what an explanation asks before it says
// a figure came from the pool.
func (r RunnerSettings) Set() bool {
	return r.ProvisionTimeout != nil || r.DrainTimeout != nil || r.MaxRunnerLifetime != nil ||
		r.ScaleUpDelay != nil || r.DockerWait != nil
}

// Supports reports whether this host's agent advertises a feature. The
// controller passes the agent package's constant; the store does not know
// what any feature means, only that the agent claimed it.
func (h *Host) Supports(feature string) bool {
	return slices.Contains(h.Features, feature)
}

// Automatic reports whether this pool leaves its runners' size to the host
// they land on: one slot's share of whichever machine the scheduler picks,
// charged and enforced as such.
//
// It is what a pool means by asking for no CPU and no memory, and it is the
// shape a pool has unless somebody chose otherwise. Disk and the pids limit
// are deliberately not part of the question: neither has a share to be given
// (free disk is a measurement rather than a budget) so a pool may cap its
// cache's disk and still leave its size to the host.
func (p *Pool) Automatic() bool {
	return p.Resources.CPUs <= 0 && p.Resources.MemoryMB <= 0
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
	Enabled bool       `json:"enabled"`
	Scope   CacheScope `json:"scope"`
	// SizeLimit is enforced by evicting whole cache entries, least recently
	// modified first, in the gap between one runner and the next. Only a cache
	// in a host directory can be measured, so a limit is refused on a named
	// volume rather than accepted and quietly ignored. Zero is unlimited.
	SizeLimit int64  `json:"size_limit,omitempty"`
	Source    string `json:"source,omitempty"`
	// Repository names the repository a repository-scoped cache belongs to,
	// as "owner/name". A repository-targeted installation supplies it on its
	// own; an organisation-targeted one cannot, because the fleet it serves has
	// many repositories, and this is what lets such a pool have a repository
	// cache without an installation of its own per repository.
	Repository string `json:"repository,omitempty"`
	// Tools keeps the pool's tool cache -- where setup-python, setup-node,
	// setup-go and setup-java unpack what they download -- between runners,
	// in the host's shared folder, with the same scope as the cache above. A
	// job that can write to it can replace a tool the next job runs, which is
	// why it follows the cache's scope rather than being fleet-wide.
	Tools bool `json:"tools,omitempty"`
}

// Pool is a named group of interchangeable runners: what labels they answer to,
// how they are built, and how many of them may exist.
type Pool struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	InstallationID string      `json:"installation_id"`
	Labels         StringSlice `json:"labels"`
	// NoDefaultLabels registers runners with only Labels, without the
	// self-hosted, operating-system and architecture labels actions/runner
	// adds itself -- config.sh's --no-default-labels. GitHub adds those labels
	// to every just-in-time runner regardless, so only a non-ephemeral pool
	// can honour it.
	NoDefaultLabels bool        `json:"no_default_labels,omitempty"`
	RunnerGroup     string      `json:"runner_group,omitempty"`
	Backend         BackendKind `json:"backend"`
	// Platform is the machine this pool's runners need. It picks the runner
	// image when the pool names none, and it stops the scheduler placing a
	// runner on a host that is not that machine.
	Platform      Platform   `json:"platform"`
	Image         string     `json:"image"`
	PullPolicy    PullPolicy `json:"pull_policy"`
	RunnerVersion string     `json:"runner_version,omitempty"`
	MinRunners    int        `json:"min_runners"`
	MaxRunners    int        `json:"max_runners"`
	// RepositoryScaleUpLimit is a best-effort throttle on runner creation
	// attributable to any one repository. GitHub may assign queued work to any
	// compatible idle runner, so this is not a strict execution concurrency cap.
	// Zero leaves the pool unrestricted.
	RepositoryScaleUpLimit int `json:"repository_scale_up_limit,omitempty"`
	// CostPerRunnerHour is administrator supplied; Zoomies never embeds prices.
	CostPerRunnerHour *float64       `json:"cost_per_runner_hour,omitempty"`
	Priority          int            `json:"priority"`
	IdleTimeout       Duration       `json:"idle_timeout"`
	Ephemeral         bool           `json:"ephemeral"`
	DockerMode        DockerMode     `json:"docker_mode"`
	Resources         Resources      `json:"resources"`
	CPUBurst          CPUBurstPolicy `json:"cpu_burst"`
	Cache             CacheConfig    `json:"cache"`
	// Tmpfs keeps the runner's work folder and /tmp in memory. Off unless an
	// operator opts in, because it spends the pool's memory limit on disk speed.
	Tmpfs TmpfsConfig `json:"tmpfs"`
	// SizeFromProfile leaves the size of this pool's runners to the host they
	// land on, as that host's runner profile says it: the standard size the
	// operator gave the host, or the fleet's default where the host says
	// nothing. It is automatic sizing with the host's own answer in place of
	// one slot's share, which is why it excludes typed CPUs and memory -- a
	// pool that has already said how big its runners are has nothing left to
	// take from the host -- and why it is stored: the two read the same from
	// Resources, and only this says which was meant.
	SizeFromProfile bool `json:"size_from_profile,omitempty"`
	// AutoKey marks a pool the controller made and keeps, and says which one:
	// an architecture and a size class (AutoKeyFor). Empty on every pool an
	// operator made, which is every pool that exists before automatic pools are
	// turned on, and what lets the controller change the others never.
	//
	// For such a pool MinRunners, MaxRunners and Enabled are the controller's
	// output -- worked out from the hosts in its class on every pass -- and
	// what the operator asked for is kept beside them: AutoMin runners kept
	// warm, at most AutoCap runners (0 for no cap), and AutoPaused to take the
	// pool out of use. Without that the next pass would read what it wrote as
	// what was wanted.
	AutoKey    string `json:"auto_key,omitempty"`
	AutoMin    int    `json:"auto_min,omitempty"`
	AutoCap    int    `json:"auto_cap,omitempty"`
	AutoPaused bool   `json:"auto_paused,omitempty"`
	// FleetStandard is the fleet's default runner size -- runners.default_cpus
	// and runners.default_memory_mb -- as the controller's sizing copy of a
	// pool that takes its size from the host carries it, for a host whose
	// profile names no standard of its own. It is never stored and never
	// serialised, and zero on every pool read from the database: the scheduler
	// is pure and cannot see the fleet's settings, so the controller hands
	// them over on the copy it already makes of a pool for the minimum
	// (controller.sizingPool). A pool without it falls back to a slot's share,
	// which is what such a pool was sized by before profiles existed.
	FleetStandard RunnerSize `json:"-"`
	// FleetMinimum is the part of this pool's minimum that came from the
	// fleet's runners.minimum_* rather than from the pool: the figure the
	// controller's sizing copy filled in, and zero on a field the pool set
	// itself. It exists so an explanation can say whose minimum a host fell
	// short of, and is never stored or serialised.
	FleetMinimum RunnerSize `json:"-"`
	// HostSelector matches Host.Labels; empty means "any host".
	// RunnerSettings is what this pool overrides of the fleet's own runner
	// timings. Every field is nil on a pool that follows the fleet, which is
	// every pool until somebody says otherwise.
	RunnerSettings RunnerSettings `json:"runner_settings"`
	HostSelector   StringMap      `json:"host_selector"`
	// Env is injected into every runner this pool creates.
	Env StringMap `json:"env"`
	// RunAsRoot disables the backend's default of dropping to an unprivileged
	// user inside the runner. Flagged as dangerous.
	RunAsRoot bool      `json:"run_as_root"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FromHosts reports whether the controller keeps this pool from the hosts it
// finds, rather than an operator having made it. It is not Automatic, which is
// about how a pool's runners are sized and is true of most pools an operator
// made by hand.
func (p *Pool) FromHosts() bool { return p.AutoKey != "" }

// AutoKeyFor is the key of the pool kept for one architecture and size class.
// The architecture is the grammar's own (amd64 or arm64), so a host that says
// "x86_64" and one that says "amd64" are in the same pool.
func AutoKeyFor(arch string, class SizeClass) string {
	return naming.NormalizeArch(arch) + "/" + string(class)
}

// ParseAutoKey is AutoKeyFor read back, for a key that is one.
func ParseAutoKey(key string) (arch string, class SizeClass, ok bool) {
	a, c, found := strings.Cut(key, "/")
	class, valid := ParseSizeClass(c)
	if !found || naming.NormalizeArch(a) != a || a == "" || !valid {
		return "", "", false
	}
	return a, class, true
}

// Spec is the pool in the naming grammar's terms: how much machine each of its
// runners gets, and which machine that is. It is what names the pool, what
// labels it, and what the Pools page prints beside it.
func (p *Pool) Spec() naming.Spec {
	return naming.Spec{
		CPUs:     p.Resources.CPUs,
		MemoryGB: int(p.Resources.MemoryMB / 1024),
		OS:       p.Platform.OS,
		Version:  p.Platform.OSVersion,
		Arch:     p.Platform.Arch,
	}
}

// CanonicalName is the name this pool would be given by `zoomies pools create`
// today. A pool whose name already equals it needs no explanation in the UI.
func (p *Pool) CanonicalName() string { return naming.PoolName(p.Spec()) }

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

// durationNeedsUnit is the one answer both decoders give to a bare number. The
// JSON side used to read 300 as nanoseconds and the YAML side as seconds, so
// the same file meant different things depending on how it arrived; a unit is
// unambiguous, and the OpenAPI schema has always said a duration is a string.
const durationNeedsUnit = `a duration is written with its unit, like "30s", "5m" or "1h30m"`

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%s", durationNeedsUnit)
	}
	return d.parse(s)
}

// UnmarshalYAML lets a pool definition write idle_timeout: 5m.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("%s", durationNeedsUnit)
	}
	return d.parse(s)
}

func (d *Duration) parse(s string) error {
	p, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w; %s", s, err, durationNeedsUnit)
	}
	*d = Duration(p)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// DinDDanger is the one sentence Dangerous returns that a fleet may ask not to
// be told again -- see security.docker_in_docker_expected. It is named so that
// the setting matches on the sentence rather than re-deriving which one it
// meant.
const DinDDanger = "docker-in-docker sidecar: runners get a privileged container"

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
		out = append(out, DinDDanger)
	}
	if p.RunAsRoot {
		out = append(out, "runners execute as root inside the container")
	}
	return out
}

// Host is an agent process and the machine it runs on.
type Host struct {
	Doctor     HostDoctor `json:"doctor,omitempty"`
	Connection string     `json:"connection"`
	ID         string     `json:"id"`
	Name       string     `json:"name"`
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
	// OS is the kernel the agent runs on (Go's GOOS), kept as reported so an
	// operator sees what the machine said about itself.
	OS string `json:"os"`
	// Distro and OSVersion are the distribution and release, which is what
	// decides whether an Ubuntu 24.04 pool may be placed here.
	Distro    string `json:"distro,omitempty"`
	OSVersion string `json:"os_version,omitempty"`
	Arch      string `json:"arch"`
	// CPUs and MemoryMB are the machine's size, reported by the agent. They
	// are what the host's canonical name and the Hosts page say out loud.
	CPUs     int       `json:"cpus,omitempty"`
	MemoryMB int64     `json:"memory_mb,omitempty"`
	Usage    HostUsage `json:"usage,omitempty"`
	// Throttle is the controller's answer to a host that its measurements say
	// is overwhelmed: how far it has stepped the host's admission and its
	// runners' CPU quotas down, and why. It is the controller's alone -- a
	// heartbeat carries the measurements it is decided from and never the
	// decision -- and it is written by its own statement, like the reserve.
	Throttle HostThrottle `json:"throttle,omitempty"`
	// Incidents is the runtime cooldown the agent last reported and the
	// last image that would not pull here. Each half has its own setter;
	// neither UpdateHost nor SetHostReported writes it.
	Incidents HostIncidents `json:"incidents,omitempty"`
	// DiskTotalMB and DiskFreeMB measure the filesystem holding the agent's
	// work directory, which is where a runner's checkout and its caches land.
	// Free is what a runner may use rather than what is unused, since the two
	// differ by the reserve the filesystem keeps for root.
	//
	// Zero is "not measured": an agent too old to report it, or one on a
	// platform with no portable way to ask. It is not "full", and nothing may
	// read it as such -- refusing to place work on every host that predates
	// the field would empty a fleet on upgrade.
	DiskTotalMB int64 `json:"disk_total_mb,omitempty"`
	DiskFreeMB  int64 `json:"disk_free_mb,omitempty"`
	// ReserveCPUs, ReserveMemoryMB and ReserveDiskMB are the operator's, like
	// Capacity: what to hold back from placement for the machine's own sake.
	// An agent reports what it sees and never writes these, so a host cannot
	// talk its way out of the room its operator kept for it.
	ReserveCPUs     int    `json:"reserve_cpus,omitempty"`
	ReserveMemoryMB int64  `json:"reserve_memory_mb,omitempty"`
	ReserveDiskMB   int64  `json:"reserve_disk_mb,omitempty"`
	Version         string `json:"version"`
	// RunnerProfile is the operator's answer to how big a runner is on this
	// host: a minimum, a standard size, and a burst ceiling. It is theirs
	// alone, written through PatchHost and by no heartbeat, and the zero value
	// -- every host until somebody sets one -- changes nothing about how the
	// host is sized.
	RunnerProfile RunnerProfile `json:"runner_profile,omitzero"`
	// SizeClass is the class the controller holds this host in, worked out
	// from its allocatable CPU and memory while size routing or automatic
	// pools are on, and empty otherwise. It is the controller's alone, written
	// by SetHostSizeClass and by no heartbeat and no operator: an operator who
	// wants a host in a class puts a size label on it, which wins without
	// being written into this. EffectiveSizeClass says which one answers.
	SizeClass HostSizeClass `json:"size_class,omitzero"`
	// Features is what the agent says it can do beyond running a backend,
	// re-read from every heartbeat. An agent that advertises "elastic-cpu"
	// can move a live runner's CPU quota; one that does not keeps every runner
	// of an elastic pool at its guaranteed share, and the pool cannot tell.
	// Recorded so the question is answered while a pool is being edited and
	// on the host's own card, not by the next heartbeat after the fact.
	Features StringSlice `json:"features"`
	// Cordoned hosts keep their existing runners but accept no new ones.
	Cordoned bool `json:"cordoned"`
	// ProtocolVersion is the agent protocol this host last reported speaking,
	// or 0 from an agent old enough not to say. Incompatible is what this
	// controller concluded from it.
	//
	// Incompatible excludes the host from placement exactly as a cordon does,
	// and does nothing else: its runners keep working, its agent keeps
	// draining and stopping them, and nothing is refused. A protocol mismatch
	// answered by refusing the heartbeat would restart every agent in the
	// fleet at once, which is the outage the upgrade was meant to avoid.
	ProtocolVersion int       `json:"protocol_version,omitempty"`
	Incompatible    bool      `json:"incompatible"`
	LastHeartbeat   time.Time `json:"last_heartbeat"`
	CreatedAt       time.Time `json:"created_at"`
	// TokenHash authenticates the agent on every request.
	TokenHash string `json:"-"`
	// AgentSessionID is the session the agent last identified itself with, and
	// AgentSessionPrev the one before it. An agent mints a session when it
	// starts, so a restart moves the id forward once and never back.
	//
	// Seeing AgentSessionPrev again is therefore something one agent cannot
	// do: it means two of them share this host's credentials -- a cloned VM,
	// or a copied state directory -- and are splitting its tasks.
	// AgentSessionAlternations counts how often that has happened and
	// AgentSessionAltAt when it last did, so the problem ages out on its own
	// once the duplicate is gone.
	AgentSessionID           string     `json:"agent_session_id,omitempty"`
	AgentSessionPrev         string     `json:"-"`
	AgentSessionAlternations int        `json:"agent_session_alternations,omitempty"`
	AgentSessionAltAt        *time.Time `json:"agent_session_alt_at,omitempty"`
	// Live counters, filled by the store on read.
	ActiveRunners int `json:"active_runners"`
	// UnlimitedRunners is how many of those live runners were created with no
	// CPU quota: a pool with none and defaults off, a process-backend pool, a
	// host whose daemon cannot apply one, or a row from before allocations
	// were recorded. They are the runners a sustained CPU hold can mean
	// something about -- a host whose every runner is inside its quota is
	// running the work it was sized for, however busy that looks.
	UnlimitedRunners int `json:"unlimited_runners,omitempty"`
}

// Platform is what this machine is, in the terms a pool asks in.
//
// The distribution wins over the kernel: "linux" cannot pick an image, and a
// pool asking for Ubuntu 24.04 needs to know it landed on Ubuntu. macOS and
// Windows report no distribution, so for them the kernel is the answer.
func (h *Host) Platform() Platform {
	os := h.Distro
	if naming.NormalizeOS(os) == "" {
		os = h.OS
	}
	return Platform{OS: os, OSVersion: h.OSVersion, Arch: h.Arch}.Normalized()
}

// Spec is the host in the naming grammar's terms: how much machine it is, what
// it runs, and which machine it is.
//
// The machine part is taken back out of a name that is already canonical, so
// that re-deriving a host's name yields the name it already has rather than
// nesting one inside the other on every heartbeat.
func (h *Host) Spec() naming.Spec {
	machine := h.Name
	if parsed, ok := naming.Parse(machine); ok {
		machine = parsed.Suffix
	}
	p := h.Platform()
	return naming.Spec{
		CPUs:     float64(h.CPUs),
		MemoryGB: int(h.MemoryMB / 1024),
		OS:       p.OS,
		Version:  p.OSVersion,
		Arch:     p.Arch,
		Suffix:   machine,
	}
}

// CanonicalName is the name this machine would be given by `zoomies agent
// join` today, which is what the UI offers when a host is called something
// that says nothing.
func (h *Host) CanonicalName() string { return h.Spec().String() }

// HeartbeatTimeout is how long a host may go silent before it is considered
// unhealthy. It is three times the agent's default heartbeat interval.
const HeartbeatTimeout = 90 * time.Second

// Healthy reports whether the agent has checked in recently enough.
func (h *Host) Healthy(now time.Time) bool {
	return now.Sub(h.LastHeartbeat) < HeartbeatTimeout
}

// Available reports whether the scheduler may place a new runner here.
func (h *Host) Available(now time.Time) bool {
	return h.Healthy(now) && !h.Cordoned && h.ActiveRunners < h.EffectiveCapacity()
}

// EffectiveCapacity is how many runners this host takes right now: its
// configured capacity, stepped down by an active throttle. Capacity is the
// operator's and the throttle never writes it; this is the two read together,
// and it is what every slot count on the host is measured against while the
// throttle stands.
//
// It never falls below one while the capacity is at least one. A throttle is
// a host being pushed too hard, not a host being taken out of the fleet: the
// pressure holds already refuse every new start while the pressure is acute,
// and the throttle is what outlasts them. A host it took to zero would look
// exactly like a cordon, and an operator reading "0 slots" would go looking
// for who cordoned it.
func (h *Host) EffectiveCapacity() int {
	slots := h.Slots()
	if slots <= 0 || !h.Throttle.Active() {
		return slots
	}
	return max(1, int(math.Floor(float64(slots)*h.Throttle.Factor())))
}

// slotEpsilon absorbs the rounding of CPU that does not divide evenly: 11.4
// allocatable CPUs hold three runners of 3.8, and the float answer must not
// come out a hair under three.
const slotEpsilon = 1e-6

// Slots is how many runners this host takes, before any throttle: its capacity,
// or -- where the operator has given it a standard runner size -- as many of
// that size as its allocatable machine holds, never more than the capacity.
//
// The capacity is the operator's ceiling and the profile is what the machine
// can carry, so the smaller of the two is the count; a host whose capacity is
// below what its standard would give says so on the Hosts page rather than
// quietly running fewer runners than its size allows. Without a standard, or
// where the agent has not measured the machine, the capacity alone stands --
// exactly as it did before profiles -- and a capacity of zero still takes
// nothing, whatever the standard says: it is how an operator pauses a host,
// and a size must never un-pause one.
//
// A standard too large for the machine to hold even once gives zero, and the
// host takes nothing of the pools that size from it. That is a standard that
// cannot be run, not a host that was paused, and is reported as the first.
func (h *Host) Slots() int {
	std := h.RunnerProfile.Standard
	if h.Capacity <= 0 || !std.Sized() {
		return h.Capacity
	}
	alloc := h.Allocatable()
	slots, bound := h.Capacity, false
	if std.CPUs > 0 && alloc.CPUsKnown {
		slots, bound = min(slots, int((alloc.CPUs+slotEpsilon)/std.CPUs)), true
	}
	if std.MemoryMB > 0 && alloc.MemoryKnown {
		slots, bound = min(slots, int(alloc.MemoryMB/std.MemoryMB)), true
	}
	if !bound {
		return h.Capacity
	}
	return max(slots, 0)
}

// The selector keys every host answers for without an operator typing
// anything. The agent reports the OS and architecture of the machine it runs
// on, so a pool can ask for arm64 or windows without a fleet being hand
// labelled first -- which is what "label every ARM box before you can select
// one" used to cost.
const (
	LabelOS   = "os"
	LabelArch = "arch"
)

// SelectorValue returns what this host answers for one host-selector key.
//
// An operator's own label wins over the reported fact, so a fleet that already
// labels `arch` by hand keeps the meaning it chose -- including a deliberate
// lie, like calling an amd64 box `arch=legacy` to keep pools off it. Only when
// no label claims the key does the agent's report answer for it.
func (h *Host) SelectorValue(key string) string {
	if v, ok := h.Labels[key]; ok {
		return v
	}
	switch key {
	case LabelOS:
		return h.OS
	case LabelArch:
		return h.Arch
	case LabelSize:
		return string(h.SizeClass.Class)
	}
	return ""
}

// Free returns the number of additional runners this host can take, against
// its effective capacity: a throttled host has fewer slots free than its
// configured capacity would say, and that is the number the scheduler and the
// Hosts page both have to use, or the page promises slots the pass refuses.
func (h *Host) Free() int {
	if n := h.EffectiveCapacity() - h.ActiveRunners; n > 0 {
		return n
	}
	return 0
}

// Where a runner's resource limits came from.
const (
	// AllocationFromPool means the pool set the limit itself.
	AllocationFromPool = "pool"
	// AllocationFromHost means the pool left the limit unset and the runner
	// was given one slot's share of the host it was placed on.
	AllocationFromHost = "host"
	// AllocationReduced means no host had room for the pool's standard size,
	// and the runner was given what one could spare, at or above the pool's
	// minimum. The figures on the row are what it was given, and what its
	// host is charged for it.
	AllocationReduced = "reduced"
	// AllocationHistory means the runner was sized up, above the host's share,
	// to what the jobs waiting on its pool are known to need from their recent
	// runs (scheduler.history_sizing=on). Like a reduced runner, the figures
	// on the row are what it was given and what its host is charged.
	AllocationHistory = "history"
	// AllocationFromProfile means the pool leaves its size to the host and the
	// runner was given that host's standard size: the operator's runner
	// profile, or the fleet's default where the host's says nothing.
	//
	// It is what the runner row and the API say. An agent is sent "host"
	// instead: one that predates this value reads a source it does not know as
	// a size somebody typed, and would double a docker-in-docker pair's limits.
	AllocationFromProfile = "profile"
)

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
	// TaskIssuedAt is when this runner's lifecycle task was last handed to its
	// host, stamped again on every redelivery.
	//
	// The task queue is in memory by design, so a controller restart drops it.
	// What the restart must not drop is the time: without this a provisioning
	// row is indistinguishable from one whose create was issued a second ago,
	// and the provision timeout is counted from the wrong end.
	TaskIssuedAt *time.Time `json:"task_issued_at,omitempty"`
	// CreateTaskIssuedAt is the first delivery to the host, never enqueue or retry.
	CreateTaskIssuedAt *time.Time `json:"create_task_issued_at,omitempty"`
	HostRemovedAt      *time.Time `json:"host_removed_at,omitempty"`
	// CleanupEstimatedAt preserves pre-RC timestamps that did not require both confirmations.
	CleanupEstimatedAt *time.Time `json:"cleanup_estimated_at,omitempty"`
	// CleanupError, CleanupFailedAt and CleanupAttempts record a stop or
	// remove the agent could not complete, or a registration GitHub would not
	// delete.
	//
	// They exist because a terminal row had nowhere to put that. The result
	// came back for a runner already marked removed, the state machine refused
	// the transition -- correctly; a removed runner cannot become failed -- and
	// it was dropped at debug level. The container stayed on the host and
	// nobody was told.
	CleanupError    string     `json:"cleanup_error,omitempty"`
	CleanupFailedAt *time.Time `json:"cleanup_failed_at,omitempty"`
	CleanupAttempts int        `json:"cleanup_attempts,omitempty"`
	// HostCleanupError and RegistrationCleanupError are the two halves
	// CleanupError is joined from: what the host said about its container,
	// and what GitHub said about the registration. They are kept apart
	// because the two need different things done, and one sentence read
	// back for a keyword could only say one of them -- a busy registration
	// named alongside a container still on its host was all an operator was
	// told about, and the container was the part that needed them.
	HostCleanupError         string `json:"host_cleanup_error,omitempty"`
	RegistrationCleanupError string `json:"registration_cleanup_error,omitempty"`
	// RegistrationDeletedAt is when GitHub confirmed the registration was gone.
	// Unset on a terminal runner is the ghost: a row this fleet has finished
	// with, and a registration still on somebody's organisation.
	RegistrationDeletedAt *time.Time `json:"registration_deleted_at,omitempty"`
	// CleanedUpAt is the end of the cleanup interval: the moment nothing of
	// this runner is left, on the host or on GitHub. It is the counterpart of
	// CreatedAt at the other end of the life, and the measurement contract's
	// end of that interval.
	CleanedUpAt *time.Time `json:"cleaned_up_at,omitempty"`
	// DrainingSince is when the runner entered draining, and is the clock the
	// drain timeout is counted from. Created-at is the wrong one: a runner that
	// worked all day before being drained is not overdue the moment it drains.
	DrainingSince *time.Time `json:"draining_since,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	// LastIdleAt is when the runner most recently became idle; the scale-down
	// path measures the idle timeout from here.
	LastIdleAt  *time.Time `json:"last_idle_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Message     string     `json:"message,omitempty"`
	JobsHandled int        `json:"jobs_handled"`
	// ResourceSample carries sample freshness and optional runtime diagnostics.
	ResourceSample json.RawMessage `json:"resource_sample,omitempty"`
	// CPUPercent and MemoryBytes are best-effort samples from the agent.
	CPUPercent  float64 `json:"cpu_percent,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	// AllocatedCPUs and AllocatedMemoryMB are the limits this runner's
	// workload was created with, and AllocationSource says whether they came
	// from the pool's own resources or from the host's default share of its
	// machine. They are recorded here rather than recomputed because the
	// question they answer is historical: a runner killed for exceeding a
	// memory limit needs to say which limit, and a pool edited since or a host
	// whose capacity moved would give a different answer today. Zero and
	// empty on rows from before the allocation was recorded.
	AllocatedCPUs     float64 `json:"allocated_cpus,omitempty"`
	AllocatedMemoryMB int64   `json:"allocated_memory_mb,omitempty"`
	AllocationSource  string  `json:"allocation_source,omitempty"`
	// SizedForCPUs is the CPU count the runner's toolchains were told to size
	// their workers for when it started (see CPUBurstPolicy.SizeForCeiling),
	// or zero when they were told nothing. Recorded rather than recomputed,
	// because a pool edited since would give a different answer than the one
	// the running job was started with.
	SizedForCPUs int `json:"sized_for_cpus,omitempty"`
	// FaultKind categorises why a failed runner failed. Message says it in the
	// backend's or GitHub's own words; this is what makes "every runner in this
	// pool fails to start, and always for the same reason" visible from
	// anywhere but one runner's page.
	FaultKind FaultKind `json:"fault_kind,omitempty"`
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

// The provisioning states an operator can put a queued job into. The empty
// string is the fourth and the default: ready, provisioning demand as usual.
const (
	// ProvisioningPaused holds a job's demand without taking the job out of
	// the queue. It is still waiting, and resuming it is one click.
	ProvisioningPaused = "paused"
	// ProvisioningDeleted is the operator saying this work should not be run
	// here at all. It is restorable, which is why the row survives, but until
	// it is restored the job is no longer part of what the fleet is waiting on.
	ProvisioningDeleted = "deleted"
)

// Job is a GitHub Actions workflow job as Zoomies observed it.
type Job struct {
	// Provisioning is operator-owned and never overwritten by GitHub deliveries.
	// Empty means ready; paused and deleted suppress this job's scale-up demand.
	Provisioning string `json:"provisioning"`
	ProvisionNow bool   `json:"provision_now"`
	// CancelRequestedAt is when an operator's cancellation of this job's
	// workflow run was accepted by GitHub. Like Provisioning it is the fleet's
	// own note, never written by a delivery, because GitHub's completion event
	// is still what settles the conclusion -- and that event can be minutes
	// behind, which is exactly the window this exists to describe.
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`

	ID          string      `json:"id"`
	GitHubJobID int64       `json:"github_job_id"`
	GitHubRunID int64       `json:"github_run_id"`
	Repo        string      `json:"repo"`     // "acme/widgets"
	Workflow    string      `json:"workflow"` // workflow name
	JobName     string      `json:"job_name"`
	Labels      StringSlice `json:"labels"`
	State       JobState    `json:"state"`
	Conclusion  string      `json:"conclusion,omitempty"` // success|failure|cancelled|skipped
	// InstallationID is the installation whose GitHub App covers this job's
	// repository, resolved at ingest. It is what stops a pool from claiming
	// work in a target it has no credentials for; empty means no installation
	// here covers the repository, which is a reason rather than a wildcard.
	InstallationID string     `json:"installation_id,omitempty"`
	PoolID         string     `json:"pool_id,omitempty"`
	RunnerID       string     `json:"runner_id,omitempty"`
	RunnerName     string     `json:"runner_name,omitempty"`
	HTMLURL        string     `json:"html_url,omitempty"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	// Matched records whether any enabled pool claimed this job's labels. An
	// unmatched queued job is a configuration problem worth surfacing.
	Matched bool `json:"matched"`
	// EligibleAt is when this job first became something this fleet could act
	// on, which is the start of the scheduling-latency interval the
	// measurement contract defines. It is stamped the first time Matched
	// becomes true and never moved afterwards.
	//
	// Nil is "not eligible yet", or a job from before the column existed. It
	// is deliberately not zero: a zero time reads as 1970 and would make every
	// latency computed from it enormous rather than obviously absent.
	//
	// The interval it starts ends at Runner.TaskIssuedAt. Neither the queue
	// wait before eligibility nor a configured scale-up delay belongs in it --
	// the first is GitHub's and the operator's, the second is a setting doing
	// what it was set to do.
	EligibleAt *time.Time `json:"eligible_at,omitempty"`
	// HeadBranch, HeadSHA and RunAttempt say what the job ran for. A failure
	// on a release branch and one on a feature branch are different news.
	HeadBranch string `json:"head_branch,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
	RunAttempt int    `json:"run_attempt,omitempty"`
	// RunNumber is GitHub's own sequential number for the workflow run this job
	// belongs to -- the "#1009" its Actions UI shows next to the workflow name,
	// so an operator can find the same run there. workflow_job deliveries never
	// carry it, so it arrives late, backfilled from a workflow_run lookup once
	// one is made; zero means that lookup has not happened yet.
	RunNumber int64 `json:"run_number,omitempty"`
	// Steps are the job's steps as GitHub last reported them. The completed
	// delivery carries every step with its conclusion, which is how a failed
	// job can say which step it stopped at.
	Steps JobSteps `json:"steps"`
	// RunnerFault is the fleet's own side of a failure: what the runner that
	// was executing this job said when it stopped before GitHub reported the
	// job over. GitHub records such a job as "failure" like any test failure;
	// this is what tells "the tests failed" from "the runner died".
	RunnerFault string `json:"runner_fault,omitempty"`
	// FaultKind is the same fact as a category, so a thousand of these can be
	// counted rather than only read one at a time. It is set with RunnerFault
	// and never without it; empty means the fleet has nothing to confess.
	FaultKind FaultKind `json:"fault_kind,omitempty"`
	// ControllerVersion, ControllerChannel, AgentVersion and HostID say which
	// build ran the job and where, so that jobs can be compared across
	// releases. They are stamped once by StampJobVersions -- the controller's
	// pair when a pool claims the job, the host's pair when a runner takes it --
	// and never rewritten by a later delivery. Empty on a job recorded before
	// migration 0060, and on one nothing here ever claimed; neither is
	// backfilled, because no timestamp says which release was running.
	ControllerVersion string `json:"controller_version,omitempty"`
	ControllerChannel string `json:"controller_channel,omitempty"`
	AgentVersion      string `json:"agent_version,omitempty"`
	HostID            string `json:"host_id,omitempty"`
	// PeakCPUs and PeakMemoryMB are the most the job's runner was measured
	// using while the job ran on it; zero is "never measured". They are what
	// the next run of the same job is sized and placed from.
	PeakCPUs     float64 `json:"peak_cpus,omitempty"`
	PeakMemoryMB int64   `json:"peak_memory_mb,omitempty"`
	// OOMKilled says the kernel killed the runner, or a step's process inside
	// it, for its memory limit while this job ran. A killed job's peak is the
	// limit it hit rather than what it needed, which the profile allows for.
	OOMKilled bool `json:"oom_killed,omitempty"`
	// GrantedCPUs, GrantedMemoryMB and GrantedSource are the size of the runner
	// that took the job -- its guaranteed share, not CPU lent to it later --
	// and where that size came from (the Allocation* constants). They are
	// copied from the runner once, by StampJobGranted, so a job can say how big
	// the machine it ran on was after the runner row is gone. Zero and empty
	// are "not recorded": a job from before migration 0071, or one on a runner
	// created with no limits.
	GrantedCPUs     float64 `json:"granted_cpus,omitempty"`
	GrantedMemoryMB int64   `json:"granted_memory_mb,omitempty"`
	GrantedSource   string  `json:"granted_source,omitempty"`
	// SizeClass is the class the controller put this job in when it first saw
	// it, SizeReason the sentence for why, and SizeBasis how it got there (the
	// SizeBasis* constants). They are stamped once, by StampJobClass, so a job
	// can say what it was taken to need after the history that decided it has
	// moved on. Empty is "not classified": a job from before migration 0075,
	// or any job while size routing is off.
	SizeClass  SizeClass `json:"size_class,omitempty"`
	SizeReason string    `json:"size_reason,omitempty"`
	SizeBasis  string    `json:"size_basis,omitempty"`
	// SizeFloorMB is the memory the job is known to need, from its history,
	// and zero when the class did not come from there. A class smaller than the
	// job's is only an acceptable fallback if its runners have at least this.
	SizeFloorMB int64 `json:"size_floor_mb,omitempty"`
	// RoutedClass is the class the job is sent to, which is SizeClass until
	// the wait for room in it runs out and the job is allowed a larger one;
	// RoutedNote then says so. RanClass is the class of the host that took it,
	// stamped once when a runner does. A job whose RanClass differs from its
	// SizeClass ran somewhere other than where it was classed, which is what
	// the Jobs page calls a fallback, and what the label advice report counts.
	RoutedClass SizeClass `json:"routed_class,omitempty"`
	RoutedNote  string    `json:"routed_note,omitempty"`
	RanClass    SizeClass `json:"ran_class,omitempty"`
	// CPUPeriods and CPUThrottledPeriods are how many CPU enforcement periods
	// the job's runner had, and in how many it was held back by its quota, at
	// the last sample taken while the job ran. Their ratio is how much of the
	// time the job wanted more CPU than it had; zero is "never sampled".
	CPUPeriods          int64 `json:"cpu_periods,omitempty"`
	CPUThrottledPeriods int64 `json:"cpu_throttled_periods,omitempty"`
}

// How a job's size class was decided.
const (
	// SizeBasisExplicit means the job asked for a class by name in its
	// runs-on, which is the one path that is guaranteed: only a runner that
	// carries the label can take the job.
	SizeBasisExplicit = "explicit"
	// SizeBasisPin means an operator said which class this job, or every job
	// of its repository, belongs in.
	SizeBasisPin = "pin"
	// SizeBasisHistory means the class was worked out from how much CPU and
	// memory recent runs of the job were measured using.
	SizeBasisHistory = "history"
	// SizeBasisDefault means the job has no usable history yet and was given
	// the fleet's default class.
	SizeBasisDefault = "default"
)

// Throttled is the share of its CPU periods the job's runner was held back
// in, from 0 to 1, and false when it was never sampled.
func (j *Job) Throttled() (float64, bool) {
	if j.CPUPeriods <= 0 {
		return 0, false
	}
	return min(1, float64(j.CPUThrottledPeriods)/float64(j.CPUPeriods)), true
}

// JobStep is one step of a workflow job as GitHub reported it.
type JobStep struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"` // queued | in_progress | completed
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// JobSteps is persisted as a JSON array in a TEXT column.
type JobSteps []JobStep

func (s *JobSteps) Scan(v any) error {
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
	return fmt.Errorf("store: cannot scan %T into JobSteps", v)
}

func (s JobSteps) Value() (driver.Value, error) {
	if s == nil {
		s = JobSteps{}
	}
	b, err := json.Marshal([]JobStep(s))
	return string(b), err
}

// JobEventKind names one thing that happened to a job.
type JobEventKind string

const (
	// JobEventQueued: GitHub announced the job and it is waiting for a runner.
	JobEventQueued JobEventKind = "queued"
	// JobEventWaiting: GitHub is holding the job for a deployment review, so
	// nothing here can start it yet. The time it spends here is GitHub's, and
	// the timeline says so rather than folding it into the queue wait.
	JobEventWaiting JobEventKind = "waiting"
	// JobEventApproved: the review passed and the job joined the queue. This
	// is the moment the queue wait starts for a job that was held.
	JobEventApproved JobEventKind = "approved"
	// JobEventClaimed: an enabled pool answers the job's labels.
	JobEventClaimed JobEventKind = "claimed"
	// JobEventUnmatched: no enabled pool does, so nothing here will start it.
	JobEventUnmatched JobEventKind = "unmatched"
	// JobEventStarted: a runner picked the job up.
	JobEventStarted JobEventKind = "started"
	// JobEventCompleted: GitHub reported the job over, with its conclusion.
	JobEventCompleted JobEventKind = "completed"
	// JobEventRunnerLost: the runner executing the job stopped before GitHub
	// reported the job over. This is the fleet's fault, not the workflow's.
	JobEventRunnerLost JobEventKind = "runner_lost"
	// JobEventRunnerReturned: the runner reported lost is alive after all, and
	// this job is still running on it. It only ever follows a runner_lost on
	// the same job, and it exists because the timeline had no way to withdraw
	// one: a job that finishes normally after its runner was written off read
	// as a contradiction, and an operator had to guess which entry to believe.
	JobEventRunnerReturned JobEventKind = "runner_returned"
	// JobEventCancelRequested records an operator request sent to GitHub. The
	// eventual completed event remains authoritative for the outcome.
	JobEventCancelRequested JobEventKind = "cancel_requested"
	// JobEventRunnerStartFailed: a runner this pool started for work like this
	// one died before it could take a job. The job itself is untouched -- it is
	// still queued, and the next runner may well run it -- which is exactly why
	// it needs saying. A pool that cannot start a container looks, from the
	// job's side, identical to a pool that is merely busy, and an operator
	// watching a queue that never moves had nowhere to find out which.
	JobEventRunnerStartFailed JobEventKind = "runner_start_failed"
	// JobEventRerunRequested records a re-run this fleet asked GitHub for. As
	// with a cancel, GitHub remains authoritative: the re-run arrives as new
	// jobs with a higher run attempt, and this entry is only the record that
	// somebody here asked.
	JobEventRerunRequested JobEventKind = "rerun_requested"
	// JobEventOOMKilled records the kernel killing the job's runner, or one of
	// its steps, for its memory limit. It may land after completed: a runner
	// whose step was killed finishes the job and says so only as it exits.
	JobEventOOMKilled JobEventKind = "oom_killed"
	// JobEventSized: the controller put the job in a size class, or sent it to
	// another one, with the sentence that says why. Only written while size
	// routing is on or being watched.
	JobEventSized JobEventKind = "sized"
)

// JobEvent is one entry in a job's timeline: what happened, who observed it,
// and the sentence the UI shows for it.
type JobEvent struct {
	ID    string       `json:"id"`
	JobID string       `json:"job_id"`
	Kind  JobEventKind `json:"kind"`
	// Source is who saw it happen: "webhook", "poller", "agent" or
	// "controller". A timeline that is all "poller" is a controller that no
	// webhook is reaching, which is worth being able to see.
	Source     string    `json:"source"`
	Message    string    `json:"message"`
	RunnerID   string    `json:"runner_id,omitempty"`
	RunnerName string    `json:"runner_name,omitempty"`
	At         time.Time `json:"at"`
}

// JobChange reports what an upsert actually changed, so the caller can write
// the timeline from the transition rather than from the delivery: webhook
// deliveries are at-least-once, and a redelivered "queued" must not add a
// second "queued" entry.
type JobChange struct {
	// Created is true when the store had never seen this job.
	Created bool
	// PreviousState is the state before the upsert; empty when Created.
	PreviousState JobState
	// StateChanged is true when the job moved forward through its lifecycle.
	StateChanged bool
	// Claimed is true when the job was unclaimed before and a pool claims it now.
	Claimed bool
	// RunnerLinked is true when the job was linked to one of this fleet's
	// runner rows for the first time.
	RunnerLinked bool
}

// FailedConclusions are the GitHub conclusions that count a job as failed
// wherever Zoomies says "failed": the Overview's count, the Jobs page's filter
// and Job.Failed. cancelled and skipped are not here, because a person or a
// condition on the job chose them; nor is stale, Zoomies' own word for a job
// GitHub stopped talking about. The UI keeps the same list in status.ts.
var FailedConclusions = []string{"failure", "timed_out", "startup_failure"}

// IsFailedConclusion reports whether a conclusion is one of FailedConclusions.
func IsFailedConclusion(conclusion string) bool {
	return slices.Contains(FailedConclusions, conclusion)
}

// Failed reports whether a job went wrong on either side: GitHub concluded it
// did, or a runner of this fleet stopped under it, which GitHub may still be
// waiting to hear about. It is the Go spelling of the SQL predicate the store's
// queries use, so a count and a listing cannot disagree.
func (j *Job) Failed() bool {
	return IsFailedConclusion(j.Conclusion) || j.RunnerFault != ""
}

// FailedStep returns the step a job stopped at: the first step that did not
// succeed, on a job that has completed. Nil when every step succeeded or was
// skipped, or while the job is still running.
//
// It says where, not whether. Whether a job counts as failed is Failed's call,
// made from the job's own conclusion; this is set for a cancelled job too,
// because its completion message says which step the cancel landed in. The
// first step that did not succeed is the one whose output the operator wants,
// because every step after it is skipped or cancelled as a consequence.
func (j *Job) FailedStep() *JobStep {
	if j.State != JobCompleted {
		return nil
	}
	for i := range j.Steps {
		st := &j.Steps[i]
		switch st.Conclusion {
		case "", "success", "skipped", "neutral":
			continue
		}
		return st
	}
	return nil
}

// Cancelling reports whether this job is on its way out because somebody
// cancelled its workflow run, and GitHub has not yet said so.
//
// GitHub owns the conclusion, so the row still reads `queued` or `in_progress`
// for as long as its completion delivery takes -- and the fleet used to report
// the job as waiting or running for that whole window. It is neither: the
// queued half raises no demand and the running half has had its runner taken
// away. Once the job completes this is history, and the conclusion says what
// happened.
func (j *Job) Cancelling() bool {
	return j.CancelRequestedAt != nil && j.State != JobCompleted
}

// RemovedFromQueue reports whether an operator took this job out of the queue.
//
// Zoomies cannot unqueue a job at GitHub, so the row stays `queued` for as
// long as GitHub keeps offering it -- which is why every figure that answers
// "how much work is waiting?" has to exclude these by hand. An operator who
// empties the queue and then reads a queue depth of forty on the Overview has
// been told the removal did nothing. A paused job is not removed: it is on
// hold, still waiting, and the Queue page lists it by default.
func (j *Job) RemovedFromQueue() bool { return j.Provisioning == ProvisioningDeleted }

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
	ID         string `json:"id"`
	DeliveryID string `json:"delivery_id"`
	Event      string `json:"event"`
	Action     string `json:"action,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Status     string `json:"status"` // accepted | rejected | error
	Error      string `json:"error,omitempty"`
	// InstallationID is the installation whose webhook secret verified this
	// delivery. It is not necessarily the one that owns the repository: when
	// no installation covers the repository every configured secret is tried,
	// and knowing which one answered is how "your secrets are out of step with
	// your installations" is told from "this delivery was forged".
	InstallationID string    `json:"installation_id,omitempty"`
	ReceivedAt     time.Time `json:"received_at"`
}

// ControllerLease is the running control plane, as the database knows it.
//
// It is what a second controller reads before it starts: a file lock catches
// another process on the same host, and this catches the copy on a shared
// filesystem or on another machine, which no lock on this host can see.
type ControllerLease struct {
	// Holder is the controller instance, minted fresh on every start, so that
	// a restarted controller can tell its own old row from a live rival's.
	Holder string `json:"holder"`
	// Host, PID and Version are for the operator staring at the refusal: they
	// have to be able to go and find the thing that is already running.
	Host       string    `json:"host,omitempty"`
	PID        int       `json:"pid,omitempty"`
	Version    string    `json:"version,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	RenewedAt  time.Time `json:"renewed_at"`
}

// Stale reports whether a lease has not been renewed inside ttl, which is what
// lets a controller that was killed rather than stopped be replaced without an
// operator having to force it.
func (l *ControllerLease) Stale(now time.Time, ttl time.Duration) bool {
	return l == nil || !now.Before(l.RenewedAt.Add(ttl))
}

// Describe names the holder in the one sentence an operator gets.
func (l *ControllerLease) Describe() string {
	if l == nil {
		return "nobody"
	}
	where := l.Host
	if where == "" {
		where = "an unnamed host"
	}
	if l.PID > 0 {
		return fmt.Sprintf("%s (pid %d) on %s", l.Holder, l.PID, where)
	}
	return fmt.Sprintf("%s on %s", l.Holder, where)
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
	ID   string `json:"id"`
	Name string `json:"name"`
	Role Role   `json:"role"`
	// OwnerRole is the role of the identity that minted this token, which is
	// not the same question as Role: 0045 promoted every administrator's token
	// to platform to keep what it could already do, so Role says what a token
	// may reach and this says whose it is. Empty means a token from before the
	// column existed, which is nobody's secret and stays visible to whoever
	// could see it before.
	OwnerRole Role   `json:"owner_role,omitempty"`
	UserID    string `json:"user_id,omitempty"`
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
	// MachineID and ExpectedName scope a token to the one machine it was
	// minted for. Empty on the tokens an operator hands out by hand, which are
	// theirs to look after; set on the ones that travel inside a guest, where
	// the credential is readable by more people than the person who asked for
	// it. Scoping is what stops a copied VM image enrolling as somebody else.
	MachineID    string `json:"machine_id,omitempty"`
	ExpectedName string `json:"expected_name,omitempty"`
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
//
// Pool and job labels are normalized once on the way out of the store
// (queries_fleet.go, queries_events.go), so most calls -- above all
// internal/scheduler's Matches/Score/Eligible, which compare every queued
// job against every pool on each reconcile tick -- pass a slice that is
// already normalized. isNormalized checks that in a single allocation-free
// pass and lets that common case skip the map and sort below entirely.
func NormalizeLabels(in []string) []string {
	if len(in) > 0 && isNormalized(in) {
		return in
	}
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

// isNormalized reports whether in is already de-duplicated, lowercased, and
// strictly increasing -- i.e. exactly what NormalizeLabels would produce from
// it. Strictly increasing (rather than merely sorted) is what rules out a
// duplicate in the same pass as the ordering check.
func isNormalized(in []string) bool {
	for i, l := range in {
		if l == "" || NormalizeLabel(l) != l {
			return false
		}
		if i > 0 && in[i-1] >= l {
			return false
		}
	}
	return true
}

// The floors under a host's reserve. An operator's reserve is what to hold
// back from placement for the machine's own sake, and zero is not a safe
// answer for two of the three resources. A host with no memory left over for
// the kernel and its page cache does not run jobs slowly, it has one of them
// killed; a work directory with nothing left on it fails a checkout before a
// job's first step. Both failures land on a job rather than on the machine, so
// the operator who did not set a reserve never learns it was theirs to set.
//
// CPU has a floor too, and it is there for the daemon rather than for the
// jobs. A CPU quota is a share of the one resource that is never exhausted,
// only contended, and a contended machine does still finish the job -- but
// the runners' quotas are not the only thing on the machine. dockerd,
// containerd and the agent have to answer in the gaps the quotas leave, and a
// host whose quotas add up to every core leaves none: the daemon stops
// answering, creates time out, and the fleet reads a busy host as a broken
// one. Half a core, or a twentieth of the machine on a large one, is what
// those three need to keep answering while every runner is flat out.
const (
	MinHostReserveMemoryMB int64 = 512
	// MinHostReserveMemoryFraction raises the memory reserve in step with the
	// machine, exactly as the CPU fraction below does and for the same reason.
	// A flat 512 MB is the right floor on a small box and far too little on a
	// large one: a 64 GB host booked down to its last half gigabyte has no
	// page cache left, and the daemon minding sixty-four containers is the
	// first thing to suffer for it. It is capped, because a tenth of a very
	// large machine is more than the daemon will ever want -- MaxHostReserveMemoryMB
	// is where holding more back stops buying anything.
	MinHostReserveMemoryFraction float64 = 0.05
	MaxHostReserveMemoryMB       int64   = 8192
	MinHostReserveDiskMB         int64   = 2048
	// MinHostReserveCPUs is the least CPU held back from placement, and
	// MinHostReserveCPUFraction raises it in step with the machine: a
	// sixty-four core host runs a daemon with sixty-four containers to mind.
	MinHostReserveCPUs        float64 = 0.5
	MinHostReserveCPUFraction float64 = 0.05
)

// The controller's own room, held back on top of the floors above on the one
// host it runs on.
//
// The floors are sized for dockerd, containerd and the agent. On the host
// whose agent is embedded, the controller is on the machine too -- the
// scheduler, the single database writer every heartbeat and webhook waits on,
// the API and the event stream -- and it answers in the same gaps. A twelve
// core host held back six tenths of a core for all of that, and with its
// runners flat out that was the controller's share of the machine: the fleet
// slowed down at the moment it was busiest, because what schedules it was
// starved by what it had scheduled. A core and a gigabyte is enough for the
// controller to keep answering; it is added to the floor rather than folded
// into it, so a small host is not left with less for its daemon than it had.
const (
	ControllerReserveCPUs     float64 = 1
	ControllerReserveMemoryMB int64   = 1024
)

// MemoryReserve is what is held back from placement on this host's memory: the
// operator's reserve, or the floor when that is larger. Zero on a host that
// has not reported its memory, where there is nothing to hold back from.
//
// It is a function rather than a constant for the reason CPUReserve is: the
// floor depends on the machine, and a caller that applied a flat figure to a
// 64 GB box and a 2 GB one would be wrong about one of them.
func (h *Host) MemoryReserve() int64 {
	if h.MemoryMB <= 0 {
		return 0
	}
	floor := min(max(MinHostReserveMemoryMB, int64(MinHostReserveMemoryFraction*float64(h.MemoryMB))), MaxHostReserveMemoryMB)
	if h.Embedded {
		floor += ControllerReserveMemoryMB
	}
	return max(h.ReserveMemoryMB, floor)
}

// The least machine a runner can be given and still be one.
//
// Below these the runner binary cannot keep up with its own job: it is killed
// before it takes one, or it crawls through it in a way that reads as a broken
// image rather than as a limit somebody set. They bound a figure an operator
// types, and they bound the share a host hands out on a pool that leaves its
// size to the host -- a machine cut into more slots than it has runners' worth
// of room is the same mistake either way, and it must be refused in both
// places or the two disagree about the same limit.
const (
	MinRunnerCPUs     float64 = 0.25
	MinRunnerMemoryMB int64   = 512
)

// CPUReserve is what is held back from placement on this host's CPUs: the
// operator's reserve, or the floor when that is larger. Zero on a host that
// has not reported its CPUs, where there is nothing to hold back from.
func (h *Host) CPUReserve() float64 {
	if h.CPUs <= 0 {
		return 0
	}
	floor := max(MinHostReserveCPUs, MinHostReserveCPUFraction*float64(h.CPUs))
	if h.Embedded {
		floor += ControllerReserveCPUs
	}
	return max(float64(h.ReserveCPUs), floor)
}

// HostAllocation is what the scheduler may hand out on a host: the machine as
// its agent measured it, less what its operator and the floors above hold
// back.
//
// Each figure carries whether it is known, and that is the whole point of the
// type. Zero means two different things on a host row -- "the agent measured
// none left" and "no agent ever measured it" -- and reading the second as the
// first would empty a fleet the moment it upgraded, which is the failure the
// reporting half of this work was careful to avoid.
type HostAllocation struct {
	CPUs     float64
	MemoryMB int64
	DiskMB   int64

	CPUsKnown   bool
	MemoryKnown bool
	DiskKnown   bool
}

// Allocatable is the machine less its reserve, floored at zero.
//
// A measured host that is smaller than its own reserve allocates nothing, and
// that is the right answer rather than a bug: it is a machine with no room to
// run a job in, and it says so instead of accepting one and killing it.
func (h *Host) Allocatable() HostAllocation {
	a := HostAllocation{
		CPUsKnown:   h.CPUs > 0,
		MemoryKnown: h.MemoryMB > 0,
		// A host that reports a total but no free space has measured a full
		// disk; one that reports neither has measured nothing.
		DiskKnown: h.DiskTotalMB > 0,
	}
	if a.CPUsKnown {
		a.CPUs = max(float64(h.CPUs)-h.CPUReserve(), 0)
	}
	if a.MemoryKnown {
		a.MemoryMB = max(h.MemoryMB-h.MemoryReserve(), 0)
	}
	if a.DiskKnown {
		a.DiskMB = max(h.DiskFreeMB-max(h.ReserveDiskMB, MinHostReserveDiskMB), 0)
	}
	return a
}
