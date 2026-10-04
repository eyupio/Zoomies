// Package backend defines how an agent materialises a runner on its host, and
// provides the implementations: Docker, Podman and a bare process.
//
// The interface is deliberately narrow -- create, inspect, log, remove -- so
// that a future cloud backend that boots a VM per job can satisfy it without
// the agent learning anything new.
package backend

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// ErrNotFound is returned when a handle refers to a workload that no longer
// exists. Callers treat it as "already gone", not as a failure.
var ErrNotFound = errors.New("backend: workload not found")

// ErrUnavailable is returned when the backend's daemon cannot be reached.
var ErrUnavailable = errors.New("backend: not available on this host")

// Handle identifies one runner workload on a host. For containers it is the
// container ID; for the process backend it is the runner's work directory.
type Handle string

// CreateResult separates time spent fetching container images from the work
// needed to create and start the workload. ImagePullDuration is nil when a
// backend cannot observe an image pull (notably the process backend).
type CreateResult struct {
	DinDReadyDuration *time.Duration `json:"dind_ready_duration,omitempty"`
	Handle            Handle         `json:"handle"`
	Digest            string         `json:"digest,omitempty"`
	ImagePullDuration *time.Duration `json:"image_pull_duration,omitempty"`
	CreateDuration    time.Duration  `json:"create_duration"`
}

// Phase is the backend's view of a workload, which is coarser than the runner
// state machine: the backend knows whether the process is alive, not whether
// GitHub has given it a job.
type Phase string

const (
	// PhaseStarting means created but not yet running.
	PhaseStarting Phase = "starting"
	// PhaseRunning means the runner process is up.
	PhaseRunning Phase = "running"
	// PhaseExited means the runner process finished. For an ephemeral runner
	// this is the normal end of life after one job.
	PhaseExited Phase = "exited"
	// PhaseExitUnknown means the process is gone but its exit status was
	// not retained across an agent restart. It is neither success nor failure.
	PhaseExitUnknown Phase = "exit_unknown"
	// PhaseFailed means the workload died unexpectedly.
	PhaseFailed Phase = "failed"
	// PhaseGone means the workload no longer exists.
	PhaseGone Phase = "gone"
)

// Live reports whether the workload still exists and may yet do work. It is
// the question a caller asks before deciding a container is safe to delete,
// and starting counts: a workload created moments ago has not failed, it has
// not finished starting.
func (p Phase) Live() bool { return p == PhaseStarting || p == PhaseRunning }

// Status is a point-in-time report on one workload.
type Status struct {
	Handle    Handle    `json:"handle"`
	Phase     Phase     `json:"phase"`
	ExitCode  int       `json:"exit_code"`
	Message   string    `json:"message,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitedAt  time.Time `json:"exited_at,omitempty"`
	// OOMKilled is the daemon saying the kernel killed a process in the
	// workload for its memory limit. It can be true with any exit code: a
	// step killed under a runner that lived on exits the container cleanly.
	OOMKilled bool `json:"oom_killed,omitempty"`
}

// Stats is a best-effort resource sample. Backends that cannot measure a field
// leave it zero.
type CPUThrottling struct {
	Periods              uint64 `json:"periods"`
	ThrottledPeriods     uint64 `json:"throttled_periods"`
	ThrottledNanoseconds uint64 `json:"throttled_nanoseconds"`
}

type Stats struct {
	SampledAt     *time.Time     `json:"sampled_at,omitempty"`
	CPUThrottling *CPUThrottling `json:"cpu_throttling,omitempty"`

	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes"`
	MemoryLimit int64   `json:"memory_limit,omitempty"`
	// CPUAllocationFactor is filled by the agent. One is the creation quota;
	// above one is elastic CPU and below one is host-pressure throttling.
	CPUAllocationFactor float64 `json:"cpu_allocation_factor,omitempty"`
	// BusiestHalfPercent is, for a docker-in-docker pair, how much of its own
	// creation quota the busier of the two containers is using, in percent.
	// The pair's sum hides the case that matters: the daemon running a build
	// flat out on its half while the runner beside it idles reads as a pair
	// half busy. Zero for a single container, and from an agent that predates
	// it, which a controller reads as "judge the sum", as it always did.
	BusiestHalfPercent float64 `json:"busiest_half_percent,omitempty"`
}

// Info describes a backend's capabilities on this particular host. The agent
// reports it to the controller, and the installer prints it during setup.
type Info struct {
	Kind store.BackendKind `json:"kind"`
	// Available is false when the daemon could not be reached; Detail then
	// explains why in terms the operator can act on.
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	// Rootless reports whether the daemon runs without root privileges, which
	// is the configuration Zoomies prefers.
	Rootless bool `json:"rootless"`
	// Endpoint is the socket or command actually in use.
	Endpoint string `json:"endpoint,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// SupportsDinD reports whether a docker-in-docker sidecar can be created.
	SupportsDinD bool `json:"supports_dind"`
	// HostSocketPath is where the host daemon's socket lives, for pools that
	// have explicitly opted into host-socket mode.
	HostSocketPath string `json:"host_socket_path,omitempty"`
	// CPUs and MemoryMB are the machine the daemon is running on, as the
	// daemon sees it. They are the honest bound for what this host can run,
	// which the agent's own view is not: a containerised agent is held to its
	// cgroup, and the runners it starts through this daemon are siblings on the
	// host rather than children inside that cgroup. Zero where the daemon did
	// not say, which includes every backend that is not a container runtime.
	CPUs     int   `json:"cpus,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// Limits is whether the daemon can apply a CPU quota, a memory limit and
	// a pids limit. A daemon that cannot apply a quota refuses the container
	// outright rather than ignoring the request, so the controller reads this
	// before it gives a runner a default limit; Known is false from a backend
	// that has not asked, which the controller reads as "default nothing".
	Limits store.LimitSupport `json:"limits"`
	// SharedFolder says what is wrong with this host's shared folder, the
	// one runners' caches are bound from, and is empty when nothing is:
	// see SharedFolderProblem.
	SharedFolder string `json:"shared_folder,omitempty"`
}

// Credentials carry whatever the runner needs to attach itself to GitHub.
//
// JITConfig is the preferred form: a single base64 blob that registers an
// ephemeral runner and cannot be replayed. RegistrationToken is the fallback
// used by non-ephemeral pools, which must run config.sh.
type Credentials struct {
	// ExpiresAt is GitHub's token expiry, when supplied. Zero means unknown.
	ExpiresAt         time.Time `json:"expires_at,omitzero"`
	JITConfig         string    `json:"jit_config,omitempty"`
	RegistrationToken string    `json:"registration_token,omitempty"`
	// URL is the org or repo URL the runner registers against.
	URL string `json:"url,omitempty"`
	// RunnerGroup and Labels are only needed for the registration-token path;
	// a JIT config already encodes them.
	RunnerGroup string   `json:"runner_group,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	// NoDefaultLabels passes --no-default-labels to config.sh. Like Labels it
	// only means anything on the registration-token path.
	NoDefaultLabels bool `json:"no_default_labels,omitempty"`
}

// Spec is everything a backend needs to create one runner.
type Spec struct {
	// StartBefore bounds queue wait using the controller's provision timeout.
	// An existing workload is adopted even after this deadline.
	StartBefore time.Time `json:"start_before,omitzero"`
	// Name is the runner name as GitHub will know it. It doubles as the
	// container name, so it must be unique on the host.
	Name string `json:"name"`
	// RunnerID is the Zoomies runner ID, attached as a label so that orphaned
	// workloads can be traced back after a controller restart.
	RunnerID string `json:"runner_id"`
	PoolID   string `json:"pool_id"`
	PoolName string `json:"pool_name"`

	Image string `json:"image"`
	// PullPolicy controls preparation of Image for this individual runner. An
	// empty value is accepted for tasks produced by older controllers and lets
	// the container backend use its configured compatibility default.
	PullPolicy  store.PullPolicy  `json:"pull_policy,omitempty"`
	Credentials Credentials       `json:"credentials"`
	Env         map[string]string `json:"env,omitempty"`
	Ephemeral   bool              `json:"ephemeral"`
	Resources   store.Resources   `json:"resources"`
	// ResourcesSource says where Resources came from: the pool's own limits,
	// or the host's default share of its machine (store.AllocationFromPool
	// and store.AllocationFromHost). A backend records it on the workload,
	// because the difference decides what a runner killed for exceeding its
	// memory limit should tell the operator to change. Empty from a
	// controller that predates it, which a backend reads as the pool's.
	ResourcesSource string `json:"resources_source,omitempty"`
	// DaemonSharePercent is the part of a host-sized slot the docker-in-docker
	// daemon is given; zero is the even split. See store.Resources.
	DaemonSharePercent int               `json:"daemon_share_percent,omitempty"`
	Cache              store.CacheConfig `json:"cache"`
	// Tmpfs keeps the runner's work folder and /tmp in memory. A container
	// backend mounts them; an agent that predates the field ignores it and the
	// runner simply uses disk, which is why the controller warns about a pool
	// placed on such a host (agent.FeatureTmpfs, pool.tmpfs_unsupported) rather
	// than leaving a job as slow as before with nothing saying why.
	Tmpfs store.TmpfsConfig `json:"tmpfs,omitzero"`
	// TmpfsMaxMB is the host's ceiling on any one in-memory folder, applied after
	// the folder is fitted to the runner's limit. Zero is none. It is separate
	// from Tmpfs because it can only be applied here: a pool left to size itself
	// is fitted to a limit the controller does not resolve, and the agent does.
	TmpfsMaxMB int64            `json:"tmpfs_max_mb,omitempty"`
	Repository string           `json:"repository,omitempty"`
	DockerMode store.DockerMode `json:"docker_mode"`
	// RunAsRoot keeps the container's default user instead of dropping to the
	// unprivileged "runner" account.
	RunAsRoot bool `json:"run_as_root"`
	// Network is an optional container network to attach to.
	Network string `json:"network,omitempty"`
	// WorkDir is the host directory this runner may use as scratch space.
	WorkDir string `json:"work_dir,omitempty"`
	// RunnerVersion pins the actions/runner release for the process backend,
	// which downloads it rather than getting it from an image.
	RunnerVersion string `json:"runner_version,omitempty"`
}

// Validate checks a spec before a backend acts on it.
func (s *Spec) Validate() error {
	if s.Name == "" {
		return errors.New("backend: spec.Name is required")
	}
	if s.Credentials.JITConfig == "" && s.Credentials.RegistrationToken == "" {
		return errors.New("backend: spec needs either a JIT config or a registration token")
	}
	if s.Credentials.RegistrationToken != "" && s.Credentials.URL == "" {
		return errors.New("backend: registration-token runners need a URL to register against")
	}
	if !s.DockerMode.Valid() {
		return fmt.Errorf("backend: %q is not a docker mode", s.DockerMode)
	}
	if s.PullPolicy != "" && !s.PullPolicy.Valid() {
		return fmt.Errorf("backend: %q is not a pool pull policy", s.PullPolicy)
	}
	if s.PullPolicy == store.PullPinnedOnly && !isDigestImageReference(s.Image) {
		return errors.New("backend: pinned-only requires an image digest")
	}
	return nil
}

func isDigestImageReference(ref string) bool {
	const marker = "@sha256:"
	i := strings.LastIndex(ref, marker)
	if i <= 0 || len(ref[i+len(marker):]) != 64 {
		return false
	}
	_, err := hex.DecodeString(ref[i+len(marker):])
	return err == nil
}

// LogOptions controls a log stream.
type LogOptions struct {
	// Follow keeps the stream open and appends new output.
	Follow bool
	// Tail limits the initial backlog; 0 means everything.
	Tail int
	// Since filters to output produced after this time.
	Since time.Time
	// Timestamps prefixes each line with an RFC3339 timestamp.
	Timestamps bool
}

// Backend creates and manages runner workloads on one host.
//
// Implementations must be safe for concurrent use: the agent runs several
// lifecycle operations at once.
type Backend interface {
	// Kind identifies the implementation.
	Kind() store.BackendKind
	// Probe reports what this backend can do on this host. It never returns an
	// error for "daemon is not installed"; that is Info.Available == false with
	// an explanatory Detail, because the agent should report it rather than
	// crash.
	Probe(ctx context.Context) Info
	// Create starts one runner and returns its handle. It must be idempotent
	// with respect to Spec.Name: recreating an existing name replaces it.
	Create(ctx context.Context, spec Spec) (Handle, error)
	// Status inspects one workload. It returns ErrNotFound if the workload is
	// gone.
	Status(ctx context.Context, h Handle) (Status, error)
	// Stats samples resource usage. A backend that cannot measure returns a
	// zero Stats and no error.
	Stats(ctx context.Context, h Handle) (Stats, error)
	// Logs streams a workload's output. The caller closes the reader.
	Logs(ctx context.Context, h Handle, opts LogOptions) (io.ReadCloser, error)
	// Stop asks the runner to finish its current job and exit, waiting at most
	// timeout before killing it. It is how a drain reaches the workload.
	Stop(ctx context.Context, h Handle, timeout time.Duration) error
	// Remove deletes the workload and its scratch space. Removing something
	// that is already gone is not an error.
	Remove(ctx context.Context, h Handle) error
	// List returns every workload this backend owns, including ones the agent
	// has forgotten about. The agent uses it to reap orphans after a restart.
	List(ctx context.Context) ([]Workload, error)
}

// TimedCreator is implemented by backends that expose creation timing. It is
// optional so third-party and older backends retain the explicit unavailable
// path rather than fabricating an image-pull duration.
type TimedCreator interface {
	CreateWithResult(context.Context, Spec) (CreateResult, error)
}

// ImagePrewarmer is implemented by container backends. It prepares an image
// without starting a runner and returns the immutable digest actually present.
type ImagePrewarmer interface {
	PrewarmImage(ctx context.Context, image string, policy store.PullPolicy) (string, error)
}

// Workload pairs a handle with the Zoomies identity recorded on it, so an agent
// restarting into a host full of containers can work out what it owns.
type Workload struct {
	Handle   Handle `json:"handle"`
	Name     string `json:"name"`
	RunnerID string `json:"runner_id"`
	PoolID   string `json:"pool_id"`
	Status   Status `json:"status"`
	// Sidecar marks a supporting container -- today only the docker-in-docker
	// daemon a pool can ask for -- rather than a runner. A backend only lists
	// one once the runner it served has gone, so it is always something to
	// clean up and never something to manage.
	//
	// It carries its runner's id, because that is the only thing that says
	// whose leftovers these are. Anything that finds a runner by id must
	// therefore skip it: binding a runner's slot to its sidecar would stop the
	// sidecar when the controller asked for the runner and leave the real
	// container running somebody's job unattended.
	Sidecar bool `json:"sidecar,omitempty"`
	// Resources are the limits the workload was created with, read back from
	// the labels the backend stamped on it. They are what a throttle scales
	// from, and they have to come from the workload rather than from the
	// agent's memory because the agent adopts what it finds after a restart
	// and remembers nothing about how it was made.
	Resources store.Resources `json:"resources,omitempty"`
}

// ResourceUpdater is implemented by a backend that can change a running
// workload's limits in place. It is how a throttle reaches a job that is
// already running: the one lever a host has left once every runner on it is
// busy, and one that slows a job rather than ending it.
//
// Only the CPU quota is expected to move. A backend may ignore any other
// field, and the container backends do: a memory limit lowered under a live
// process is refused by the daemon or kills the process, and neither is a
// throttle.
type ResourceUpdater interface {
	UpdateResources(ctx context.Context, h Handle, res store.Resources) error
}

// LabelPrefix namespaces the container labels Zoomies writes.
const LabelPrefix = "io.zoomies."

// Well-known labels applied to every workload a backend creates.
const (
	LabelManaged  = LabelPrefix + "managed"
	LabelRunnerID = LabelPrefix + "runner-id"
	LabelPoolID   = LabelPrefix + "pool-id"
	LabelPoolName = LabelPrefix + "pool-name"
	// Cache diagnostics identify the shared volume and the size limit enforced
	// against it between one runner and the next.
	LabelCacheVolume    = LabelPrefix + "cache-volume"
	LabelCacheSizeLimit = LabelPrefix + "cache-size-limit"
	LabelName           = LabelPrefix + "name"
	LabelCreated        = LabelPrefix + "created-at"
)

// Labels returns the label set to stamp on a workload built from spec.
func (s *Spec) Labels(now time.Time) map[string]string {
	return map[string]string{
		LabelManaged:  "true",
		LabelRunnerID: s.RunnerID,
		LabelPoolID:   s.PoolID,
		LabelPoolName: s.PoolName,
		LabelName:     s.Name,
		LabelCreated:  now.UTC().Format(time.RFC3339),
	}
}

// Registry holds the backends an agent has available.
type Registry struct {
	backends map[store.BackendKind]Backend
}

// NewRegistry builds a registry from a list of backends.
func NewRegistry(bs ...Backend) *Registry {
	r := &Registry{backends: make(map[store.BackendKind]Backend, len(bs))}
	for _, b := range bs {
		if b != nil {
			r.backends[b.Kind()] = b
		}
	}
	return r
}

// Get returns the backend for a kind.
func (r *Registry) Get(k store.BackendKind) (Backend, error) {
	b, ok := r.backends[k]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, k)
	}
	return b, nil
}

// Kinds returns the registered backend kinds.
func (r *Registry) Kinds() []store.BackendKind {
	out := make([]store.BackendKind, 0, len(r.backends))
	for k := range r.backends {
		out = append(out, k)
	}
	return out
}

// Probe reports on every registered backend.
func (r *Registry) Probe(ctx context.Context) []Info {
	out := make([]Info, 0, len(r.backends))
	for _, b := range r.backends {
		out = append(out, b.Probe(ctx))
	}
	return out
}

// Available returns the kinds whose daemon actually answered.
func (r *Registry) Available(ctx context.Context) []string {
	var out []string
	for _, i := range r.Probe(ctx) {
		if i.Available {
			out = append(out, string(i.Kind))
		}
	}
	return out
}
