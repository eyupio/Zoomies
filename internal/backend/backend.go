// Package backend defines how an agent materialises a runner on its host, and
// provides the implementations: Docker, Podman and a bare process.
//
// The interface is deliberately narrow -- create, inspect, log, remove -- so
// that a future cloud backend that boots a VM per job can satisfy it without
// the agent learning anything new.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// PhaseFailed means the workload died unexpectedly.
	PhaseFailed Phase = "failed"
	// PhaseGone means the workload no longer exists.
	PhaseGone Phase = "gone"
)

// Status is a point-in-time report on one workload.
type Status struct {
	Handle    Handle    `json:"handle"`
	Phase     Phase     `json:"phase"`
	ExitCode  int       `json:"exit_code"`
	Message   string    `json:"message,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitedAt  time.Time `json:"exited_at,omitempty"`
}

// Stats is a best-effort resource sample. Backends that cannot measure a field
// leave it zero.
type Stats struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes"`
	MemoryLimit int64   `json:"memory_limit,omitempty"`
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
}

// Credentials carry whatever the runner needs to attach itself to GitHub.
//
// JITConfig is the preferred form: a single base64 blob that registers an
// ephemeral runner and cannot be replayed. RegistrationToken is the fallback
// used by non-ephemeral pools, which must run config.sh.
type Credentials struct {
	JITConfig         string `json:"jit_config,omitempty"`
	RegistrationToken string `json:"registration_token,omitempty"`
	// URL is the org or repo URL the runner registers against.
	URL string `json:"url,omitempty"`
	// RunnerGroup and Labels are only needed for the registration-token path;
	// a JIT config already encodes them.
	RunnerGroup string   `json:"runner_group,omitempty"`
	Labels      []string `json:"labels,omitempty"`
}

// Spec is everything a backend needs to create one runner.
type Spec struct {
	// Name is the runner name as GitHub will know it. It doubles as the
	// container name, so it must be unique on the host.
	Name string `json:"name"`
	// RunnerID is the Zoomies runner ID, attached as a label so that orphaned
	// workloads can be traced back after a controller restart.
	RunnerID string `json:"runner_id"`
	PoolID   string `json:"pool_id"`
	PoolName string `json:"pool_name"`

	Image       string            `json:"image"`
	Credentials Credentials       `json:"credentials"`
	Env         map[string]string `json:"env,omitempty"`
	Ephemeral   bool              `json:"ephemeral"`
	Resources   store.Resources   `json:"resources"`
	Cache       store.CacheConfig `json:"cache"`
	Repository  string            `json:"repository,omitempty"`
	DockerMode  store.DockerMode  `json:"docker_mode"`
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
	return nil
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

// Workload pairs a handle with the Zoomies identity recorded on it, so an agent
// restarting into a host full of containers can work out what it owns.
type Workload struct {
	Handle   Handle `json:"handle"`
	Name     string `json:"name"`
	RunnerID string `json:"runner_id"`
	PoolID   string `json:"pool_id"`
	Status   Status `json:"status"`
}

// LabelPrefix namespaces the container labels Zoomies writes.
const LabelPrefix = "io.zoomies."

// Well-known labels applied to every workload a backend creates.
const (
	LabelManaged  = LabelPrefix + "managed"
	LabelRunnerID = LabelPrefix + "runner-id"
	LabelPoolID   = LabelPrefix + "pool-id"
	LabelPoolName = LabelPrefix + "pool-name"
	// Cache diagnostics identify the shared volume and configured approximate cap.
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
