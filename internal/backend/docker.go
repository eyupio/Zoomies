package backend

// The Docker backend. Podman reuses everything here except the handful of
// differences documented in podman.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The runner image contract.
//
// These are the only environment variables Zoomies promises to set, and the
// entrypoint of the image in deploy/Dockerfile.runner is the only thing that
// reads them. They are constants rather than string literals precisely so that
// the image and the backend cannot drift apart: change one and the compiler
// makes you change the other.
//
// The image entrypoint is expected to:
//
//	if ZOOMIES_JITCONFIG is set   -> exec ./bin/Runner.Listener run --jitconfig "$ZOOMIES_JITCONFIG"
//	otherwise                     -> ./config.sh --unattended --url "$ZOOMIES_RUNNER_URL"
//	                                   --token "$ZOOMIES_RUNNER_TOKEN" --name "$ZOOMIES_RUNNER_NAME"
//	                                   --labels "$ZOOMIES_RUNNER_LABELS" --runnergroup "$ZOOMIES_RUNNER_GROUP"
//	                                   [--ephemeral if ZOOMIES_EPHEMERAL=true]
//	                                   [--no-default-labels if ZOOMIES_RUNNER_NO_DEFAULT_LABELS=true]
//	                                   --disableupdate
//	                                 then exec ./run.sh
const (
	// EnvJITConfig carries the base64 just-in-time configuration. When it is
	// set, no other credential variable is.
	EnvJITConfig = "ZOOMIES_JITCONFIG"
	// EnvRunnerURL is the org or repo URL to register against.
	EnvRunnerURL = "ZOOMIES_RUNNER_URL"
	// EnvRunnerToken is a short-lived registration token.
	EnvRunnerToken = "ZOOMIES_RUNNER_TOKEN"
	// EnvRunnerName is the runner name as GitHub will show it.
	EnvRunnerName = "ZOOMIES_RUNNER_NAME"
	// EnvRunnerLabels is a comma-separated custom label list.
	EnvRunnerLabels = "ZOOMIES_RUNNER_LABELS"
	// EnvRunnerGroup names the runner group, empty for Default.
	EnvRunnerGroup = "ZOOMIES_RUNNER_GROUP"
	// EnvEphemeral is "true" when the runner must exit after one job.
	EnvEphemeral = "ZOOMIES_EPHEMERAL"
	// EnvNoDefaultLabels is "true" when config.sh must register the runner
	// with its custom labels only.
	EnvNoDefaultLabels = "ZOOMIES_RUNNER_NO_DEFAULT_LABELS"
	// EnvExtraCAFile names, inside the runner, the extra certificate authority
	// the entrypoint adds to the image's trust store. It is set only when the
	// host has agent.extra_ca_file.
	EnvExtraCAFile = "ZOOMIES_EXTRA_CA_FILE"

	// EnvUpstreamJITConfig is the name actions/runner itself understands. It is
	// set alongside EnvJITConfig so that an operator can point a pool at a
	// third-party runner image and still have it come up.
	EnvUpstreamJITConfig = "ACTIONS_RUNNER_INPUT_JITCONFIG"
	// EnvUpstreamRegistrationToken is how the process backend hands config.sh
	// its registration token: the runner reads ACTIONS_RUNNER_INPUT_<ARG> for
	// every argument, and an environment is not visible to other local users
	// the way a command line is.
	EnvUpstreamRegistrationToken = "ACTIONS_RUNNER_INPUT_TOKEN"
)

// Labels beyond the well-known set in backend.go, used to find the pieces of a
// runner that are not the runner container itself.
const (
	// LabelRole distinguishes a runner from its docker-in-docker sidecar, so
	// List never reports a sidecar as a workload.
	LabelRole = LabelPrefix + "role"
	// LabelDinDFor names the runner a sidecar belongs to.
	LabelDinDFor = LabelPrefix + "dind-for"
	// LabelWorkDir records a host directory that Zoomies created and must
	// therefore delete on removal. It is absent when the directory already
	// existed, because deleting an operator's directory would be rude.
	LabelWorkDir = LabelPrefix + "workdir"
	// LabelCPUs and LabelMemoryMB record the limits a container was created
	// with, and LabelLimitsFrom where they came from ("pool" or "host"). A
	// throttle scales a container's quota from the first; an agent that
	// adopted the container after a restart has no other record of it. The
	// third decides what an out-of-memory kill tells the operator to change.
	LabelCPUs       = LabelPrefix + "cpus"
	LabelMemoryMB   = LabelPrefix + "memory-mb"
	LabelLimitsFrom = LabelPrefix + "limits-from"
	LabelDockerMode = LabelPrefix + "docker-mode"
)

// Role label values.
const (
	roleRunner = "runner"
	roleDinD   = "dind"
)

// RunnerWorkMount is where a per-runner host scratch directory is bind-mounted
// inside the container.
const RunnerWorkMount = "/home/runner/_work"

// RunnerToolCacheMount is where a pool's tool cache is mounted in a runner, and
// what AGENT_TOOLSDIRECTORY is pointed at when the pool keeps one. It is not
// the image's own /opt/hostedtoolcache: mounting over that would hide the
// toolchains an image such as zoomies-runner-full was built with.
const RunnerToolCacheMount = "/opt/zoomies-tools"

// EnvToolsDirectory is the variable actions/runner reads for its tool cache.
const EnvToolsDirectory = "AGENT_TOOLSDIRECTORY"

// RunnerCacheMount is disposable performance cache space, not persistent
// workflow storage. Operators may evict its contents at any time.
const RunnerCacheMount = "/opt/zoomies-cache"

// DefaultDinDImage is the sidecar image used for docker-in-docker pools.
const DefaultDinDImage = "docker:27-dind"

// dindPort is the TCP port the sidecar's daemon listens on inside the network
// namespace the two containers share. It is never published to the host.
const dindPort = 2375

// Match the runner image's default Docker readiness budget.
const dindStartTimeout = 120 * time.Second

// defaultStopTimeout bounds a graceful stop when the caller does not say.
const defaultStopTimeout = 60 * time.Second

// probeTimeout keeps Probe snappy: it runs on the agent's heartbeat path, and a
// hung daemon must not stall the heartbeat.
const probeTimeout = 5 * time.Second

// PullPolicy decides when an image is fetched.
type PullPolicy string

const (
	// PullNever fails rather than reaching the network, for air-gapped hosts.
	PullNever PullPolicy = "never"
	// PullIfMissing is the default: fetch only what is not already local.
	PullIfMissing PullPolicy = "if-missing"
	// PullAlways refetches every time, so a moving tag is picked up.
	PullAlways PullPolicy = "always"
)

// Valid reports whether p is a known policy.
func (p PullPolicy) Valid() bool {
	switch p {
	case PullNever, PullIfMissing, PullAlways:
		return true
	}
	return false
}

// DockerOptions configures the Docker backend.
type DockerOptions struct {
	// Host is a socket URL. Empty autodetects, preferring a rootless socket.
	Host string
	// Network is the container network runners attach to when a pool does not
	// name one. Empty leaves them on the daemon's default bridge.
	Network string
	// WorkDir is where per-runner scratch directories are created.
	WorkDir string
	// PullPolicy defaults to PullIfMissing.
	PullPolicy PullPolicy
	// DinDImage overrides the docker-in-docker sidecar image, for hosts that
	// mirror images into a private registry.
	DinDImage string
	// RegistryAuth is a base64 X-Registry-Auth value for a private registry.
	RegistryAuth string
	// ExtraCAFile is a PEM bundle on this host that runners and their Docker
	// sidecars are to trust, for a network whose proxy re-signs TLS. Empty
	// changes nothing.
	ExtraCAFile string
	// SharedDir is this host's shared folder (config.SharedDir), where a
	// pool's tool cache is kept. Empty keeps none.
	SharedDir string
	// SharedDirProblem is why there is no SharedDir, when there is a reason
	// an operator can act on; Probe reports it so the Hosts page can.
	SharedDirProblem string
	Logger           *slog.Logger
}

// ExtraCADir is where the extra CA bundle is mounted inside a runner and its
// sidecar. It is a directory of its own, holding nothing else, because the
// sidecar's dockerd is a Go program that reads every file in the directories
// SSL_CERT_DIR names -- a directory shared with anything else would make it
// trust that too.
const ExtraCADir = "/etc/zoomies/ca"

// ExtraCAPath is the bundle's path inside the container.
const ExtraCAPath = ExtraCADir + "/extra-ca.crt"

// flavor holds the few behaviours that differ between Docker and Podman. It
// exists so that podman.go can be a page of differences instead of a copy.
type flavor struct {
	kind         store.BackendKind
	displayName  string
	supportsDinD bool
	// mountSuffix is appended to bind mounts; Podman needs ":z" so that SELinux
	// relabels the host directory for the container.
	mountSuffix string
	// startHint is the command an unreachable-socket message names as the fix,
	// so a Podman probe tells the operator to start Podman, not Docker.
	startHint string
	capDrop   []string
	capAdd    []string
	// runnerUser is the account the runner process drops to, unless the pool
	// asked for root.
	runnerUser string
}

// buildCapabilities are what a build actually needs: unpacking archives and
// installing packages changes ownership and permissions, and test suites kill
// their own children. Everything else is dropped.
var buildCapabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID", "KILL"}

func dockerFlavor() flavor {
	return flavor{
		kind:         store.BackendDocker,
		displayName:  "Docker",
		supportsDinD: true,
		startHint:    "systemctl --user start docker, or systemctl start docker",
		capDrop:      []string{"ALL"},
		capAdd:       slices.Clone(buildCapabilities),
		runnerUser:   "runner",
	}
}

// DockerBackend runs runners as containers on a Docker daemon.
type DockerBackend struct {
	api     *APIClient
	fl      flavor
	network string
	workDir string
	pull    PullPolicy
	dind    string
	auth    string
	extraCA string
	// sharedDir is DockerOptions.SharedDir, and sharedProblem its
	// SharedDirProblem.
	sharedDir     string
	sharedProblem string
	// toolCacheProblem is the last tool cache folder a runner could not have
	// because it cannot write to it; see prepareCacheDirs.
	toolCacheMu      sync.Mutex
	toolCacheProblem string
	log              *slog.Logger
	// nameRelease is how long a create waits for the daemon to release a name
	// whose container has gone. A field rather than the constant so a test can
	// exercise the wait without spending it.
	nameRelease time.Duration
	// nameSettle is how long a create waits for the daemon to finish a create
	// of the same name that this agent stopped waiting on, measured from the
	// moment it stopped. A field for the same reason.
	nameSettle time.Duration
	// removalWait is how long a removal waits for one the daemon already has
	// under way; see removalWaitBudget. A field for the same reason.
	removalWait time.Duration
	// abandoned is every container name whose create the daemon did not answer
	// in time, and when this agent gave up on it. It is what tells a 409 for a
	// container nobody can inspect apart from a leaked name: the first is a
	// create of ours the daemon is still finishing, the second is a finding.
	// Podman embeds this backend and inherits it.
	abandonedMu sync.Mutex
	abandoned   map[string]time.Time
}

var _ Backend = (*DockerBackend)(nil)

// A throttle reaches a running job through the update endpoint, and Podman
// inherits the implementation by embedding.
var _ ResourceUpdater = (*DockerBackend)(nil)

// NewDocker builds a Docker backend. It does not contact the daemon: a host
// where Docker is not running must still be able to start an agent and report
// the backend as unavailable, which is what Probe is for.
func NewDocker(opts DockerOptions) (*DockerBackend, error) {
	return newContainerBackend(opts, dockerFlavor(), DetectDockerHost, "unix:///var/run/docker.sock")
}

func newContainerBackend(opts DockerOptions, fl flavor, detect func() []string, fallback string) (*DockerBackend, error) {
	host := strings.TrimSpace(opts.Host)
	if host == "" {
		host = pickEndpoint(detect(), fallback)
	}
	api, err := NewAPIClient(host)
	if err != nil {
		return nil, err
	}

	pull := opts.PullPolicy
	if pull == "" {
		pull = PullIfMissing
	}
	if !pull.Valid() {
		return nil, fmt.Errorf("backend: %q is not a pull policy; use never, if-missing or always", pull)
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	dind := opts.DinDImage
	if dind == "" {
		dind = DefaultDinDImage
	}

	return &DockerBackend{
		api:     api,
		fl:      fl,
		network: strings.TrimSpace(opts.Network),
		workDir: strings.TrimSpace(opts.WorkDir),
		pull:    pull,
		dind:    dind,
		auth:    opts.RegistryAuth,
		extraCA: strings.TrimSpace(opts.ExtraCAFile),

		sharedDir:     strings.TrimSpace(opts.SharedDir),
		sharedProblem: opts.SharedDirProblem,
		log:           log.With("backend", string(fl.kind)),

		nameRelease: nameReleaseBudget,
		nameSettle:  nameSettleBudget,
		removalWait: removalWaitBudget,
	}, nil
}

// Kind identifies the implementation.
func (b *DockerBackend) Kind() store.BackendKind { return b.fl.kind }

// SocketPath is the unix socket this backend talks to, or "" for a TCP
// endpoint.
func (b *DockerBackend) SocketPath() string { return b.api.SocketPath() }

// Probe reports what this daemon can do, never failing: an agent on a host with
// no Docker must still start and say so.
func (b *DockerBackend) Probe(ctx context.Context) Info {
	info := Info{
		Kind:         b.fl.kind,
		Endpoint:     b.api.Endpoint(),
		SharedFolder: firstNonEmpty(b.sharedProblem, b.lastToolCacheProblem()),
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if err := b.api.Ping(ctx); err != nil {
		info.Detail = b.unreachableDetail(err)
		return info
	}

	info.Available = true
	info.SupportsDinD = b.fl.supportsDinD
	info.HostSocketPath = b.api.SocketPath()

	if v, err := b.api.Version(ctx); err == nil {
		info.Version = v.Version
	}
	sys, err := b.api.Info(ctx)
	if err == nil {
		if info.Version == "" {
			info.Version = sys.ServerVersion
		}
		info.Rootless = IsRootless(sys)
		// The machine the daemon is on, which is the machine the runners will
		// be on: they are started through this daemon as siblings, so an agent
		// held to a two-core cgroup is still talking to a sixty-four core host.
		info.CPUs = sys.NCPU
		if sys.MemTotal > 0 {
			info.MemoryMB = sys.MemTotal / (1 << 20)
		}
		// What the daemon can enforce, in its own words. Rootless Docker on a
		// host that delegates only memory and pids to the user -- the default
		// on Debian and Ubuntu -- says so here, and a CPU quota sent to it
		// would be refused at create rather than quietly ignored.
		info.Limits = store.LimitSupport{Known: true, CPU: sys.CPUCfsQuota, Memory: sys.MemoryLimit, Pids: sys.PidsLimit}
	}
	if !info.Rootless && IsRootlessEndpoint(b.api.SocketPath()) {
		info.Rootless = true
	}

	mode := "rootful"
	if info.Rootless {
		mode = "rootless"
	}
	info.Detail = fmt.Sprintf("%s %s (%s) at %s", b.fl.displayName, info.Version, mode, info.Endpoint)
	if !info.Rootless && b.fl.kind == store.BackendDocker {
		info.Detail += "; a rootless daemon would confine a compromised job to this user account"
	}
	return info
}

// unreachableDetail turns a transport failure into a sentence naming the fix,
// in terms of the daemon this backend actually is: a Podman probe must never
// tell the operator to install or start Docker.
func (b *DockerBackend) unreachableDetail(err error) string {
	if sock := b.api.SocketPath(); sock != "" {
		// In a container a missing socket is not a daemon to install: it is
		// a mount the container was created without, which a Compose file
		// from an older release, or one written by hand, can lack.
		if _, serr := os.Stat(sock); errors.Is(serr, fs.ErrNotExist) && runningInContainer() {
			return fmt.Sprintf("no socket at %s inside this container: it was not given the host's %s socket. Add `- %s:%s` to the zoomies service's volumes (or `--volume %s:%s` to its docker run), or run `zoomies upgrade --yes` on the host, which adds it",
				sock, b.fl.displayName, sock, sock, sock, sock)
		}
		if serr := canUseSocket(sock, b.fl.displayName, b.fl.startHint); serr != nil {
			return strings.TrimPrefix(serr.Error(), "backend: not available on this host: ")
		}
	}
	return strings.TrimPrefix(err.Error(), "backend: not available on this host: ")
}

// containerOptions are the host-level decisions Create has already taken by the
// time a container config is assembled. Keeping them explicit is what lets the
// config builders be pure functions with a table test.
type containerOptions struct {
	Now time.Time
	// Network is attached at creation time. It is empty when NetworkMode is set,
	// since a container sharing another's namespace has no network of its own.
	Network string
	// NetworkMode is an explicit mode such as "container:<id>", used to put a
	// runner inside its docker-in-docker sidecar's network namespace.
	NetworkMode string
	// HostSocket is a host daemon socket to bind-mount, for host-socket pools.
	HostSocket string
	// SocketGID owns that socket. The runner is uid 1001 inside the container
	// and the socket is typically root:docker on the host, so without the gid
	// as a supplementary group the mount is there and unopenable -- "permission
	// denied while trying to connect to the Docker daemon socket", which reads
	// like a host misconfiguration and is not one.
	SocketGID int
	// DockerHost is the value of DOCKER_HOST inside the runner.
	DockerHost string
	// WorkDirMount is a host directory to bind at RunnerWorkMount.
	WorkDirMount string
	// WorkDirOwned records that Zoomies created that directory and must delete
	// it again on removal.
	WorkDirOwned bool
	// DinDImage is only used when building a sidecar config.
	DinDImage string
	// ProxyEnv is the agent's proxy variables the pool did not set itself,
	// as KEY=value pairs. The caller resolves it once per create, so that
	// building a config stays a pure function of its inputs.
	ProxyEnv []string
	// ExtraCAFile is a host PEM bundle to mount read-only at ExtraCAPath in
	// the runner and its sidecar.
	ExtraCAFile string
	// ToolCacheDir is the host folder holding this pool's tool cache, bound
	// at RunnerToolCacheMount. Create resolves and creates it; empty mounts
	// nothing and leaves the image's own tool cache in place.
	ToolCacheDir string
	// ToolFarmDir is the host folder holding this runner's own tool cache,
	// bound at RunnerToolCacheMount; it is set whenever ToolCacheDir is.
	ToolFarmDir string
}

// extraCABind is the read-only mount for the extra CA bundle. The Podman
// relabel suffix is kept: the source is the operator's own bundle, placed for
// this purpose, and without the label SELinux leaves the mount unreadable.
func extraCABind(fl flavor, source string) string {
	return source + ":" + ExtraCAPath + ":ro" + strings.ReplaceAll(fl.mountSuffix, ":", ",")
}

// buildRunnerConfig assembles the container config for one runner.
// pairLimits is what each half of a docker-in-docker runner is given.
//
// A limit an operator typed says what the job may have, and the daemon is
// given the same: the build runs in the daemon, so a pool that asked for eight
// gigabytes and got them only in the container that is not building would have
// asked for nothing. scheduler.Reserve charges the host for both.
//
// A limit that came from the host's slot is one slot, and the pair splits it,
// because a slot is one runner: an operator who set a host to eight slots said
// it may carry eight runners, not four because half of them brought a daemon.
// The scheduler charges one share for the pair to match.
//
// Anything else -- a spec from a controller that says nothing about where its
// limits came from -- is treated as typed, which is what such a spec has always
// meant and leaves an older fleet exactly as it was.
func pairLimits(spec Spec) (runner, daemon store.Resources) {
	if spec.ResourcesSource != store.AllocationFromHost {
		return spec.Resources, spec.Resources
	}
	return spec.Resources.SplitWithDaemon()
}

func buildRunnerConfig(spec Spec, fl flavor, o containerOptions) ContainerCreateRequest {
	labels := spec.Labels(o.Now)
	labels[LabelRole] = roleRunner
	labels[LabelDockerMode] = string(spec.DockerMode)
	if source, err := cacheSource(spec); err == nil && source != "" {
		labels[LabelCacheVolume] = source
		labels[LabelCacheSizeLimit] = fmt.Sprint(spec.Cache.SizeLimit)
	}
	if o.WorkDirOwned && o.WorkDirMount != "" {
		labels[LabelWorkDir] = o.WorkDirMount
	}
	if o.ToolFarmDir != "" {
		labels[LabelToolFarm] = o.ToolFarmDir
	}
	runnerRes := spec.Resources
	if spec.DockerMode == store.DockerDinD {
		runnerRes, _ = pairLimits(spec)
	}
	stampResourceLabels(labels, spec, runnerRes)

	cfg := ContainerCreateRequest{
		Image: spec.Image,
		// A container that shares another's network namespace has no namespace
		// of its own to name, and the daemon rejects the combination outright.
		Hostname: hostnameFor(spec.Name, o.NetworkMode),
		Env:      runnerEnv(spec, o),
		Labels:   labels,
		// No TTY: a TTY merges stdout and stderr and mangles the log framing,
		// and nothing is attached to the runner interactively.
		Tty: false,
		// The runner treats SIGINT as "finish the current job, then exit", which
		// is exactly what a drain means.
		StopSignal: "SIGINT",
		HostConfig: &HostConfig{
			LogConfig: runnerLogConfig(fl),
			// AutoRemove would delete the container the instant it exits, taking
			// its exit code and its logs with it -- and those are the two things
			// a failed job investigation needs. The agent removes it itself once
			// the exit has been reported and agent.finished_retention has passed.
			AutoRemove:    false,
			RestartPolicy: RestartPolicy{Name: "no"},
			// No SecurityOpt: the runner image gives its unprivileged user
			// passwordless sudo because a great many real workflows assume it, and
			// "no-new-privileges" would silently defeat that -- the flag disables
			// the setuid escalation sudo itself depends on, with a kernel error
			// that names sudo rather than Zoomies. CapDrop below is the actual
			// boundary: sudo only ever regains this capability set, never root's.
			CapDrop:     slices.Clone(fl.capDrop),
			CapAdd:      slices.Clone(fl.capAdd),
			NetworkMode: o.NetworkMode,
			// The root filesystem stays writable on purpose: builds write to it
			// constantly, and a read-only rootfs would break most workflows for
			// a benefit the ephemeral lifecycle already provides.
			ReadonlyRootfs: false,
		},
	}
	if !spec.RunAsRoot {
		cfg.User = fl.runnerUser
	}

	hc := cfg.HostConfig
	res := runnerRes
	if res.CPUs > 0 {
		hc.NanoCPUs = nanoCPUs(res.CPUs)
	}
	if res.MemoryMB > 0 {
		hc.Memory = res.MemoryMB * 1024 * 1024
		// Without an equal swap limit the container can swap past its memory
		// cap, which turns an OOM into an unexplained slowdown.
		hc.MemorySwap = hc.Memory
	}
	if res.PidsLimit > 0 {
		limit := res.PidsLimit
		hc.PidsLimit = &limit
	}

	if o.HostSocket != "" {
		// No relabel suffix here. ":z" relabels the *source*, and the source
		// is the host's own Docker or Podman socket; Podman's documentation
		// warns against relabelling system files, and a socket the daemon
		// itself can no longer open is every container on the host failing.
		// The work and cache directories below are Zoomies' own to label.
		hc.Binds = append(hc.Binds, o.HostSocket+":/var/run/docker.sock")
		// Root already reaches the socket, and adding a group to a root
		// container only widens what it can do for no gain.
		if o.SocketGID > 0 && !spec.RunAsRoot {
			hc.GroupAdd = append(hc.GroupAdd, strconv.Itoa(o.SocketGID))
		}
	}
	if o.WorkDirMount != "" {
		hc.Binds = append(hc.Binds, o.WorkDirMount+":"+RunnerWorkMount+fl.mountSuffix)
	}
	if source, err := cacheSource(spec); err == nil && source != "" {
		hc.Binds = append(hc.Binds, source+":"+RunnerCacheMount+fl.mountSuffix)
	}
	if o.ToolCacheDir != "" && o.ToolFarmDir != "" {
		hc.Binds = append(hc.Binds,
			o.ToolCacheDir+":"+RunnerToolCacheSharedMount+":ro"+strings.ReplaceAll(fl.mountSuffix, ":", ","),
			o.ToolFarmDir+":"+RunnerToolCacheMount+fl.mountSuffix)
	}
	if o.ExtraCAFile != "" {
		hc.Binds = append(hc.Binds, extraCABind(fl, o.ExtraCAFile))
	}
	if o.Network != "" && o.NetworkMode == "" {
		hc.NetworkMode = o.Network
		cfg.NetworkingConfig = &NetworkingConfig{
			EndpointsConfig: map[string]*EndpointSettings{
				o.Network: {Aliases: []string{sanitizeHostname(spec.Name)}},
			},
		}
	}
	return cfg
}

// runnerEnv builds the environment in a stable order, credentials first, so
// that two identical specs produce identical containers.
func runnerEnv(spec Spec, o containerOptions) []string {
	env := []string{
		EnvRunnerName + "=" + spec.Name,
		EnvEphemeral + "=" + boolString(spec.Ephemeral),
	}
	if jit := spec.Credentials.JITConfig; jit != "" {
		env = append(env, EnvJITConfig+"="+jit, EnvUpstreamJITConfig+"="+jit)
	} else {
		env = append(env,
			EnvRunnerURL+"="+spec.Credentials.URL,
			EnvRunnerToken+"="+spec.Credentials.RegistrationToken,
			EnvRunnerLabels+"="+strings.Join(spec.Credentials.Labels, ","),
			EnvRunnerGroup+"="+spec.Credentials.RunnerGroup,
		)
		if spec.Credentials.NoDefaultLabels {
			env = append(env, EnvNoDefaultLabels+"=true")
		}
	}
	if o.DockerHost != "" {
		// DOCKER_TLS_CERTDIR is emptied to match the sidecar, which listens in
		// the clear inside the network namespace the two containers share.
		env = append(env, "DOCKER_HOST="+o.DockerHost, "DOCKER_TLS_CERTDIR=")
	}
	env = append(env, o.ProxyEnv...)
	// Before the pool's own variables, so a pool that names a tool cache of
	// its own in env keeps it: the daemon keeps the last of a repeated name.
	if o.ToolCacheDir != "" && o.ToolFarmDir != "" {
		env = append(env, EnvToolsDirectory+"="+RunnerToolCacheMount)
	}

	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+spec.Env[k])
	}
	// Last, so that a pool's env cannot point the entrypoint somewhere the
	// mount is not: the daemon keeps the last of a repeated name.
	if o.ExtraCAFile != "" {
		env = append(env, EnvExtraCAFile+"="+ExtraCAPath)
	}
	return env
}

// dindEnv is the sidecar's environment. The daemon pulls every image a job's
// builds and services name, so behind a proxy it needs the same proxy the
// runner has -- the agent's, or the pool's where the pool sets one. Nothing
// else from the pool's env reaches the daemon: those are the job's variables,
// not the daemon's.
//
// The runner reaches this daemon on 127.0.0.1, which proxy-aware clients
// already exempt from the proxy, so NO_PROXY needs nothing added for it.
func dindEnv(spec Spec, o containerOptions) []string {
	env := []string{"DOCKER_TLS_CERTDIR="}
	env = append(env, o.ProxyEnv...)
	for _, k := range proxyEnvKeys {
		if v, ok := spec.Env[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// buildDinDConfig assembles the privileged sidecar that gives a pool its own
// Docker daemon.
//
// The sidecar owns the network namespace and the runner joins it, rather than
// the other way round, because a container can only join a namespace that is
// already running. That ordering also means the daemon's port is reachable from
// the runner alone and from nowhere else on the host.
func buildDinDConfig(spec Spec, fl flavor, o containerOptions) ContainerCreateRequest {
	labels := spec.Labels(o.Now)
	labels[LabelRole] = roleDinD
	labels[LabelDinDFor] = spec.Name
	labels[LabelName] = dindName(spec.Name)
	_, daemonRes := pairLimits(spec)
	stampResourceLabels(labels, spec, daemonRes)

	cfg := ContainerCreateRequest{
		Image: o.DinDImage,
		Healthcheck: &HealthConfig{
			Test:     []string{"CMD", "docker", "--host=tcp://127.0.0.1:2375", "info"},
			Interval: 5 * time.Second, Timeout: 5 * time.Second, Retries: 3,
		},
		Hostname: sanitizeHostname(dindName(spec.Name)),
		Labels:   labels,
		Tty:      false,
		Env:      dindEnv(spec, o),
		Cmd:      []string{"dockerd", "--host=tcp://127.0.0.1:2375", "--host=unix:///var/run/docker.sock"},
		HostConfig: &HostConfig{
			LogConfig: runnerLogConfig(fl),
			// A nested daemon needs real privileges; this is the cost of the
			// mode, and the pool that asked for it is flagged as dangerous.
			Privileged:    true,
			AutoRemove:    false,
			RestartPolicy: RestartPolicy{Name: "no"},
			NetworkMode:   o.Network,
		},
	}
	// With docker_mode: dind the builds themselves run inside this sidecar,
	// not the runner, so a pool's memory, CPU and pids limits are worth nothing
	// unless they bind the sidecar too.
	hc := cfg.HostConfig
	res := daemonRes
	if res.CPUs > 0 {
		hc.NanoCPUs = nanoCPUs(res.CPUs)
	}
	if res.MemoryMB > 0 {
		hc.Memory = res.MemoryMB * 1024 * 1024
		hc.MemorySwap = hc.Memory
	}
	if res.PidsLimit > 0 {
		limit := res.PidsLimit
		hc.PidsLimit = &limit
	}
	// The sidecar pulls every image a dind job uses, so behind a proxy that
	// re-signs TLS it needs the CA as much as the runner does. dockerd is Go,
	// and Go adds every file in the SSL_CERT_DIR directories to the system
	// roots, so naming the mount's directory alongside the image's own is the
	// whole of it -- no trust-store command to run in an image that is not
	// ours.
	if o.ExtraCAFile != "" {
		hc.Binds = append(hc.Binds, extraCABind(fl, o.ExtraCAFile))
		cfg.Env = append(cfg.Env, "SSL_CERT_DIR=/etc/ssl/certs:"+ExtraCADir)
	}
	if o.Network != "" {
		cfg.NetworkingConfig = &NetworkingConfig{
			EndpointsConfig: map[string]*EndpointSettings{
				o.Network: {Aliases: []string{sanitizeHostname(spec.Name)}},
			},
		}
	}
	return cfg
}

// stampResourceLabels records the limits a container is created with, and
// where they came from, on the container itself. The runner and its sidecar
// both carry them: a throttle scales each container's quota from its own
// label, and an agent that adopted the pair after a restart has no other
// record of what either was given.
//
// Each is stamped with its own half, because under a slot's share the two
// halves differ -- a label that said what the pair was given between them
// would have the throttle scale one container by the other's quota.
//
// A missing label reads back as zero, so only a limit that was set is
// written, and the source only when there is a limit for it to describe. A
// spec from a controller that predates the source says nothing about it, and
// a limit it did set can only have been the pool's own.
func stampResourceLabels(labels map[string]string, spec Spec, res store.Resources) {
	if res.CPUs > 0 {
		labels[LabelCPUs] = strconv.FormatFloat(res.CPUs, 'f', -1, 64)
	}
	if res.MemoryMB > 0 {
		labels[LabelMemoryMB] = strconv.FormatInt(res.MemoryMB, 10)
	}
	if res.CPUs > 0 || res.MemoryMB > 0 {
		source := spec.ResourcesSource
		if source == "" {
			source = store.AllocationFromPool
		}
		labels[LabelLimitsFrom] = source
	}
}

// resourcesFromLabels reads back what stampResourceLabels wrote. A label that
// is missing or unreadable is zero rather than an error: it is a container
// from an older release, or one somebody edited, and either way the worst
// outcome is a runner the throttle leaves alone.
func resourcesFromLabels(labels map[string]string) store.Resources {
	var res store.Resources
	if v, err := strconv.ParseFloat(labels[LabelCPUs], 64); err == nil && v > 0 && !math.IsInf(v, 0) {
		res.CPUs = v
	}
	if v, err := strconv.ParseInt(labels[LabelMemoryMB], 10, 64); err == nil && v > 0 {
		res.MemoryMB = v
	}
	return res
}

// nanoCPUs is the daemon's unit for a CPU quota. Rounded rather than
// truncated so that the quota a throttle computes for a live container and
// the one its create wrote compare equal when they mean the same share; a
// float that lands a nanosecond short would otherwise be an update per beat.
func nanoCPUs(cpus float64) int64 { return int64(math.Round(cpus * 1e9)) }

// UpdateResources moves a running runner's CPU quota, and its docker-in-docker
// sidecar's, to res.CPUs -- except that a boost of a pair goes to the sidecar,
// see below. It is how a throttle reaches a job that is already
// running, and it is idempotent: a container whose quota already matches is
// not asked to change, so the agent can send the same figure on every beat
// without a durable record of what it last sent.
//
// Only the CPU quota moves. A memory limit lowered under a live process is
// refused by the daemon or kills the process, and neither is a throttle; the
// smaller effective capacity is how memory pressure reaches new work. A
// quota of zero asks for nothing, because the update endpoint reads zero as
// "leave it alone", not as "remove the limit".
func (b *DockerBackend) UpdateResources(ctx context.Context, h Handle, res store.Resources) error {
	if res.CPUs <= 0 {
		return nil
	}
	insp, err := b.api.ContainerInspect(ctx, string(h))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("backend: inspecting container %s before changing its CPU quota: %w", shortID(string(h)), err)
	}
	want := nanoCPUs(res.CPUs)
	var name string
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
		name = labels[LabelName]
	}
	var sidecars []ContainerSummary
	if name != "" {
		// The sidecar does the build's work under docker_mode dind, so a
		// throttle that left it alone would slow the runner process and
		// nothing else. It is found by the name label the runner carries,
		// which is what its own dind-for label was written from.
		sidecars, err = b.api.ContainerList(ctx, map[string][]string{
			"label": {LabelManaged + "=true", LabelDinDFor + "=" + name},
		})
		if err != nil {
			return fmt.Errorf("backend: listing the docker-in-docker sidecar of %s: %w", name, err)
		}
	}

	// A boost is the pair's, and it goes to the half doing the work. Scaling
	// both halves by the factor gave the idle one cores it had no use for and
	// the busy one only half the loan, so one half keeps its own quota and the
	// other is given the rest of the pair's target. Which one is measured: a
	// docker build works in the daemon, but a job whose steps run in the
	// runner container -- setup-go, go build -- does not, and lending it to
	// the daemon left that job capped at its half under "Maximum boost". With
	// no sample to judge by, the daemon has it, as it always did.
	//
	// The request still carries the runner's half times the factor, as every
	// agent has always sent it, so neither side of the protocol changed and a
	// container without the labels to split by keeps the old behaviour.
	runnerWant, sidecarWant := want, want
	runnerHalf := resourcesFromLabels(labels).CPUs
	if runnerHalf > 0 && res.CPUs > runnerHalf && len(sidecars) == 1 {
		if sidecarHalf := resourcesFromLabels(sidecars[0].Labels).CPUs; sidecarHalf > 0 {
			factor := res.CPUs / runnerHalf
			pair := (runnerHalf + sidecarHalf) * factor
			runnerWant, sidecarWant = nanoCPUs(runnerHalf), nanoCPUs(sidecarHalf)
			if b.runnerHalfIsBusier(ctx, string(h), labels, sidecars[0]) {
				runnerWant = nanoCPUs(math.Floor((pair-sidecarHalf)*100+1e-9) / 100)
			} else {
				sidecarWant = nanoCPUs(math.Floor((pair-runnerHalf)*100+1e-9) / 100)
			}
		}
	}

	// A loan moving from the daemon to the runner is taken back before it is
	// lent again, so the pair is never above its target in between.
	runnerGains := insp.HostConfig != nil && runnerWant > insp.HostConfig.NanoCPUs
	if !runnerGains {
		if err := b.updateCPUQuota(ctx, string(h), insp.HostConfig, runnerWant); err != nil {
			return err
		}
	}
	for _, s := range sidecars {
		sinsp, err := b.api.ContainerInspect(ctx, s.ID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// Gone between the listing and the look: nothing to throttle.
				continue
			}
			return fmt.Errorf("backend: inspecting the docker-in-docker sidecar of %s: %w", name, err)
		}
		if err := b.updateCPUQuota(ctx, s.ID, sinsp.HostConfig, sidecarWant); err != nil {
			return err
		}
	}
	if runnerGains {
		return b.updateCPUQuota(ctx, string(h), insp.HostConfig, runnerWant)
	}
	return nil
}

// runnerHalfIsBusier says whether the runner container is using more of its
// own quota than its docker-in-docker sidecar is of its. False when either
// cannot be sampled, which leaves a boost where it has always gone.
func (b *DockerBackend) runnerHalfIsBusier(ctx context.Context, runner string, runnerLabels map[string]string, sidecar ContainerSummary) bool {
	rs, err := b.api.ContainerStats(ctx, runner)
	if err != nil {
		return false
	}
	ss, err := b.api.ContainerStats(ctx, sidecar.ID)
	if err != nil {
		return false
	}
	return halfPercent(rs.CPUPercent, runnerLabels) > halfPercent(ss.CPUPercent, sidecar.Labels)
}

// updateCPUQuota sends the quota only when the container's differs.
func (b *DockerBackend) updateCPUQuota(ctx context.Context, id string, hc *HostConfig, want int64) error {
	if hc != nil && hc.NanoCPUs == want {
		return nil
	}
	if err := b.api.ContainerUpdate(ctx, id, UpdateConfig{NanoCPUs: want}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("backend: changing the CPU quota of container %s: %w", shortID(id), err)
	}
	b.log.Debug("changed a container's CPU quota", "container", shortID(id), "cpus", float64(want)/1e9)
	return nil
}

// Create materialises one runner. It replaces any container of the same name,
// so that a redelivered task converges instead of failing.
func (b *DockerBackend) Create(ctx context.Context, spec Spec) (Handle, error) {
	r, err := b.CreateWithResult(ctx, spec)
	return r.Handle, err
}

func (b *DockerBackend) CreateWithResult(ctx context.Context, spec Spec) (result CreateResult, createErr error) {
	if err := spec.Validate(); err != nil {
		return CreateResult{}, err
	}
	if _, err := cacheSource(spec); err != nil {
		return CreateResult{}, err
	}
	if strings.TrimSpace(spec.Image) == "" {
		return CreateResult{}, fmt.Errorf("backend: pool %q has no image; set the pool's image to a runner image before creating runners", spec.PoolName)
	}
	if spec.DockerMode == store.DockerDinD && !b.fl.supportsDinD {
		return CreateResult{}, fmt.Errorf("backend: the %s backend cannot run docker-in-docker", b.fl.kind)
	}

	name := containerName(spec.Name)
	if err := b.removeOwnedForCreate(ctx, spec, false); err != nil {
		return CreateResult{}, err
	}
	if err := b.removeOwnedForCreate(ctx, spec, true); err != nil {
		return CreateResult{}, err
	}

	createRef, digest, pulled, pullDuration, err := b.prepareImage(ctx, spec.Image, spec.PullPolicy)
	if err != nil {
		return CreateResult{}, err
	}
	_ = pulled // pullDuration deliberately carries whether a pull occurred.
	// Create from exactly the immutable image we just resolved, by a reference
	// the daemon can look up. This prevents a moving tag from changing between
	// preparation and the create request.
	spec.Image = createRef
	createStarted := time.Now()

	opts := containerOptions{Now: time.Now(), DinDImage: b.dind, ProxyEnv: inheritedProxyEnv(os.LookupEnv, spec.Env), ExtraCAFile: b.extraCA}
	network := firstNonEmpty(strings.TrimSpace(spec.Network), b.network)
	if network != "" {
		if err := b.ensureNetwork(ctx, network); err != nil {
			return CreateResult{}, err
		}
		opts.Network = network
	}

	workDir, owned, err := b.ensureWorkDir(spec)
	if err != nil {
		return CreateResult{}, err
	}
	opts.WorkDirMount, opts.WorkDirOwned = workDir, owned
	// Every failure after allocating scratch space must unwind it, including a
	// cancelled pull/start or a lost create response. Use the deterministic
	// names because the daemon may have created a container without returning
	// its ID, and the sidecar's ID when this call did get one. Cleanup gets its
	// own bounded context, not the expired create's.
	var dindID string
	defer func() {
		if createErr != nil {
			if err := b.cleanupFailedCreate(ctx, spec, createErr, dindID, workDir, owned); err != nil {
				createErr = errors.Join(createErr, err)
			}
			if opts.ToolFarmDir != "" {
				if err := os.RemoveAll(opts.ToolFarmDir); err != nil {
					createErr = errors.Join(createErr, fmt.Errorf("cleaning failed runner tool cache: %w", err))
				}
			}
		}
	}()

	// After the defer, not before: the farm has no container carrying its
	// label until the create succeeds, so this cleanup is the only thing that
	// can reap it when the network or work directory setup fails first.
	b.prepareCacheDirs(spec, &opts)
	b.pruneCacheFor(ctx, spec)

	var dindReadyDuration *time.Duration
	switch spec.DockerMode {
	case store.DockerDinD:
		if _, err := b.ensureImage(ctx, b.dind); err != nil {
			return CreateResult{}, err
		}
		dindStarted := time.Now()
		dindID, err = b.startDinD(ctx, spec, opts)
		dindElapsed := time.Since(dindStarted)
		dindReadyDuration = &dindElapsed
		b.log.Info("Docker sidecar readiness completed", "duration", dindElapsed, "ok", err == nil)
		if err != nil {
			return CreateResult{}, err
		}
		// The runner lives in the sidecar's network namespace, so it has no
		// network attachment of its own.
		opts.NetworkMode = "container:" + dindID
		opts.Network = ""
		opts.DockerHost = fmt.Sprintf("tcp://127.0.0.1:%d", dindPort)
	case store.DockerHostSocket:
		sock := b.api.SocketPath()
		if sock == "" {
			return CreateResult{}, fmt.Errorf("backend: pool %q asks for the host docker socket, but %s is a TCP endpoint with no socket to mount; use docker mode dind or none", spec.PoolName, b.api.Endpoint())
		}
		opts.HostSocket = sock
		// A socket whose owner we cannot read still gets mounted rather than
		// failing the create: the pool asked for it, and a runner that is root,
		// or a socket that is world-writable, needs no group to open it.
		if _, gid, ok := statOwner(sock); ok {
			opts.SocketGID = gid
		}
		// Logged on every create, not once: this is the setting that turns any
		// workflow on this pool into root on this host, and it should be visible
		// in the log of every runner it applies to.
		b.log.Warn("mounting the host docker socket into a runner: any job on this pool can become root on this host",
			"runner", spec.Name, "pool", spec.PoolName, "socket", sock)
	}

	cfg := buildRunnerConfig(spec, b.fl, opts)
	id, err := b.createWithConflictRecovery(ctx, spec, cfg, false)
	if err != nil {
		return CreateResult{}, daemonErr(fmt.Errorf("backend: creating container %s: %w", name, err))
	}
	if err := b.api.ContainerStart(ctx, id); err != nil {
		return CreateResult{}, daemonErr(fmt.Errorf("backend: starting container %s: %w", name, err))
	}

	b.log.Info("runner container started",
		"runner", spec.Name, "pool", spec.PoolName, "image", spec.Image,
		"container", shortID(id), "docker_mode", string(spec.DockerMode))
	return CreateResult{DinDReadyDuration: dindReadyDuration, Handle: Handle(id), Digest: digest, ImagePullDuration: pullDuration, CreateDuration: time.Since(createStarted)}, nil
}

// prepareImage applies the task's pool policy, then resolves the image before
// container creation. Empty policy is the wire-compatible legacy case.
func (b *DockerBackend) prepareImage(ctx context.Context, image string, policy store.PullPolicy) (createRef, digest string, pulled bool, pullDuration *time.Duration, err error) {
	pull := false
	switch policy {
	case store.PullAlways:
		pull = true
	case store.PullIfNotPresent, store.PullPinnedOnly:
		present, err := b.api.ImageInspect(ctx, image)
		if err != nil {
			return "", "", false, nil, imageErr(fmt.Errorf("backend: looking for image %s: %w", image, err))
		}
		pull = !present
	case "":
		if b.pull == PullAlways {
			pull = true
		} else {
			present, err := b.api.ImageInspect(ctx, image)
			if err != nil {
				return "", "", false, nil, imageErr(fmt.Errorf("backend: looking for image %s: %w", image, err))
			}
			if !present && b.pull == PullNever {
				return "", "", false, nil, imageErr(fmt.Errorf("backend: image %s is not on this host and the pull policy is %q; pull it here first (docker pull %s) or set the pull policy to if-missing", image, b.pull, image))
			}
			pull = !present
		}
	default:
		return "", "", false, nil, fmt.Errorf("backend: %q is not a pool pull policy", policy)
	}
	var duration *time.Duration
	if pull {
		started := time.Now()
		if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
			return "", "", false, nil, imageErr(fmt.Errorf("backend: pulling %s: %w", image, err))
		}
		d := time.Since(started)
		duration = &d
	}
	createRef, digest, err = b.api.ImageIdentity(ctx, image)
	if err != nil {
		return "", "", pull, duration, imageErr(fmt.Errorf("backend: resolving image %s digest: %w", image, err))
	}
	if digest == "" {
		return "", "", pull, duration, imageErr(fmt.Errorf("backend: image %s has no immutable digest", image))
	}
	return createRef, digest, pull, duration, nil
}

// startDinD keeps the host's serial startup slot until dockerd answers. A
// running container only proves its entrypoint started; releasing the slot
// then let subsequent pulls compete with a quota-limited daemon still booting.
func (b *DockerBackend) startDinD(ctx context.Context, spec Spec, opts containerOptions) (string, error) {
	limit := dindStartTimeout
	if raw, ok := spec.Env["ZOOMIES_DOCKER_WAIT"]; ok {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 3600 || len(raw) > 4 || strings.Trim(raw, "0123456789") != "" {
			return "", fmt.Errorf("backend: ZOOMIES_DOCKER_WAIT must be a whole number of seconds from 1 to 3600")
		}
		limit = time.Duration(seconds) * time.Second
	}
	cfg := buildDinDConfig(spec, b.fl, opts)
	id, err := b.createWithConflictRecovery(ctx, spec, cfg, true)
	if err != nil {
		return "", fmt.Errorf("backend: creating the docker-in-docker sidecar for %s: %w", spec.Name, err)
	}
	if err := b.api.ContainerStart(ctx, id); err != nil {
		return "", fmt.Errorf("backend: starting the docker-in-docker sidecar for %s: %w", spec.Name, err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		insp, err := b.api.ContainerInspect(readyCtx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return "", err
			}
			lastErr = err
		} else if state := insp.State; state != nil {
			if state.OOMKilled || state.Dead || state.Status == "exited" {
				return "", fmt.Errorf("backend: docker-in-docker sidecar for %s exited before its daemon was ready (exit %d, OOM killed: %t); check its logs and the pool's memory allocation", spec.Name, state.ExitCode, state.OOMKilled)
			}
			if state.Running && state.Health == nil {
				lastErr = errors.New("runtime did not report sidecar health; the runtime must honour container healthchecks and the custom DinD image must provide the docker CLI")
			}
			if state.Running && state.Health != nil && state.Health.Status == "healthy" {
				b.log.Warn("docker-in-docker daemon ready: this runner has a privileged container", "runner", spec.Name, "pool", spec.PoolName, "container", shortID(id))
				return id, nil
			}
		}
		select {
		case <-readyCtx.Done():
			return "", fmt.Errorf("backend: docker-in-docker daemon for %s did not become ready within %s; check sidecar logs and host pressure, or raise runners.docker_wait: %w", spec.Name, limit, errors.Join(readyCtx.Err(), lastErr))
		case <-ticker.C:
		}
	}
}

// cleanupFailedCreate unwinds what a create that failed for cause left behind.
//
// The sidecar this call started is this call's to remove, whatever the cause:
// nothing else can ever bind to it -- only the runner this call would have
// created with container:<id> -- no controller remove task is queued for a
// runner that failed at create, and a privileged daemon should not idle on an
// overloaded host for the two minutes the orphan sweep takes to reach it. It
// goes by the ID startDinD returned, so the conflict skip below, which protects
// the contested occupant, does not also protect a sidecar nobody is contesting.
//
// The removals by deterministic name are skipped on a conflict because the
// name is then held by a container this runner must not touch, or one whose
// ownership could not be established, and looking it up by name would take
// it. The scratch directory is not skipped: owned means this call created it.
func (b *DockerBackend) cleanupFailedCreate(ctx context.Context, spec Spec, cause error, dindID, workDir string, owned bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	var errs []error
	if dindID != "" {
		switch err := b.removeContainer(cleanupCtx, dindID); {
		case err == nil:
			b.forgetAbandonedCreate(dindName(containerName(spec.Name)))
			b.log.Info("removed the docker-in-docker sidecar of a runner that failed to create", "runner", spec.Name, "container", shortID(dindID))
		case errors.Is(err, ErrNotFound):
			// Already gone, by the orphan sweep or a hand; nothing left to finish.
			b.forgetAbandonedCreate(dindName(containerName(spec.Name)))
		default:
			errs = append(errs, fmt.Errorf("removing the docker-in-docker sidecar %s this create started: %w", shortID(dindID), err))
		}
	}
	if !errors.Is(cause, ErrContainerConflict) {
		if err := b.removeOwnedForCreate(cleanupCtx, spec, false); err != nil {
			return errors.Join(append(errs, fmt.Errorf("cleaning failed runner creation: %w", err))...)
		}
		// There may be a sidecar even when the runner never reached creation.
		if err := b.removeOwnedForCreate(cleanupCtx, spec, true); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	if owned && workDir != "" {
		if err := os.RemoveAll(workDir); err != nil {
			errs = append(errs, fmt.Errorf("cleaning failed runner scratch directory: %w", err))
		}
	}
	return errors.Join(errs...)
}

// ensureImage applies the pull policy.
func (b *DockerBackend) ensureImage(ctx context.Context, image string) (bool, error) {
	if b.pull == PullAlways {
		if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
			return false, imageErr(fmt.Errorf("backend: pulling %s: %w", image, err))
		}
		return true, nil
	}

	present, err := b.api.ImageInspect(ctx, image)
	if err != nil {
		return false, imageErr(fmt.Errorf("backend: looking for image %s: %w", image, err))
	}
	if present {
		return false, nil
	}
	if b.pull == PullNever {
		return false, imageErr(fmt.Errorf("backend: image %s is not on this host and the pull policy is %q; pull it here first (docker pull %s) or set the pull policy to if-missing", image, b.pull, image))
	}
	if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
		return false, imageErr(fmt.Errorf("backend: pulling %s: %w", image, err))
	}
	return true, nil
}

func (b *DockerBackend) PrewarmImage(ctx context.Context, image string, policy store.PullPolicy) (string, error) {
	if policy == store.PullPinnedOnly && !isDigestImageReference(image) {
		return "", fmt.Errorf("backend: pinned-only requires an image digest")
	}
	_, digest, _, _, err := b.prepareImage(ctx, image, policy)
	return digest, err
}

// PrewarmDinD prepares this host's configured sidecar image. The controller
// cannot name it itself because agents may use different registry mirrors.
func (b *DockerBackend) PrewarmDinD(ctx context.Context) error {
	if !b.fl.supportsDinD {
		return fmt.Errorf("backend: the %s backend cannot run docker-in-docker", b.fl.kind)
	}
	_, err := b.ensureImage(ctx, b.dind)
	return err
}

// ensureNetwork creates a user-defined network on demand. The daemon's built-in
// modes are passed through untouched.
func (b *DockerBackend) ensureNetwork(ctx context.Context, name string) error {
	switch name {
	case "bridge", "host", "none", "default":
		return nil
	}
	if err := b.api.NetworkEnsure(ctx, name); err != nil {
		return fmt.Errorf("backend: preparing network %s: %w", name, err)
	}
	return nil
}

// ensureWorkDir creates the runner's scratch directory, reporting whether it was
// this call that created it. Only a directory Zoomies created is deleted again.
//
// A container gets a host directory only when the spec asks for one; a relative
// path is taken as relative to the agent's work directory. The directory is
// mounted in as the runner's work folder, so the image's runner account must be
// able to write to it: on a host whose agent user differs from the image's
// runner uid, leave WorkDir unset and let the container use its own filesystem.
func (b *DockerBackend) ensureWorkDir(spec Spec) (string, bool, error) {
	dir := strings.TrimSpace(spec.WorkDir)
	if dir == "" {
		return "", false, nil
	}
	if !filepath.IsAbs(dir) && b.workDir != "" {
		dir = filepath.Join(b.workDir, dir)
	}
	if _, err := os.Stat(dir); err == nil {
		return dir, false, nil
	}
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return "", false, fmt.Errorf("backend: creating the work directory %s for runner %s: %w", dir, spec.Name, err)
	}
	return dir, true, nil
}

// prepareCacheDirs creates the host folders a runner's caches are bound from,
// writable by the runner, before the daemon is asked to bind them: a folder
// the daemon creates for a bind is root's, and the runner cannot write to it.
//
// A cache is an accelerator, so a folder that cannot be made does not stop the
// runner: the pool cache falls back to what the daemon would have done, and
// the tool cache is left out, which costs the job its downloads and nothing
// else. Both are logged, because a cache that never warms is otherwise silent.
func (b *DockerBackend) prepareCacheDirs(spec Spec, opts *containerOptions) {
	if dir, ok := cacheDirectory(spec); ok {
		if err := ensureRunnerWritableDir(dir); err != nil {
			b.log.Warn("could not create the pool cache folder; the daemon will create it, owned by root", "runner", spec.Name, "dir", dir, "error", err)
		}
	}
	dir, err := toolCacheDir(spec, b.sharedDir)
	if err != nil || dir == "" {
		return
	}
	if err := ensureRunnerWritableDir(dir); err != nil {
		b.log.Warn("could not create the tool cache folder; this runner starts without it", "runner", spec.Name, "dir", dir, "error", err)
		return
	}
	// A folder that was already there is left as its owner made it, and one
	// the runner cannot write to is worse than none: every setup action that
	// downloads fails with EACCES instead of downloading. That is the folder
	// a host's daemon makes, root's, when the path it is handed is not the
	// one the agent created -- a container that does not mount the shared
	// folder -- and it outlives the mount being fixed.
	if why := runnerCannotWrite(dir); why != "" {
		problem := fmt.Sprintf("the tool cache folder %s %s, so runners start without it and download their toolchains; give it to the runner or open it (sudo chmod 0777 %s), or delete it and Zoomies makes it again", dir, why, dir)
		b.noteToolCacheProblem(problem)
		b.log.Warn("the tool cache folder is not writable by the runner; this runner starts without it", "runner", spec.Name, "dir", dir, "fix", problem)
		return
	}
	b.noteToolCacheProblem("")
	farm := toolFarmDir(b.sharedDir, spec.Name)
	if err := buildToolFarm(dir, farm); err != nil {
		b.log.Warn("could not make the runner's own tool cache; this runner starts without the kept one", "runner", spec.Name, "dir", farm, "error", err)
		_ = os.RemoveAll(farm)
		return
	}
	opts.ToolCacheDir, opts.ToolFarmDir = dir, farm
}

func (b *DockerBackend) noteToolCacheProblem(p string) {
	b.toolCacheMu.Lock()
	b.toolCacheProblem = p
	b.toolCacheMu.Unlock()
}

func (b *DockerBackend) lastToolCacheProblem() string {
	b.toolCacheMu.Lock()
	defer b.toolCacheMu.Unlock()
	return b.toolCacheProblem
}

// removalWaitBudget is how long a removal waits for one the daemon already has
// under way before handing the question back to its caller.
//
// It is short on purpose. Everything that removes a container runs inside a
// budget of its own -- the agent's two minutes for a whole runner, a pass of
// the reconcile loop -- with other work to do after this, and a removal still
// under way at the end of it is not lost: the daemon finishes it anyway, and
// the next attempt meets the answer. What the wait buys is the common case, a
// removal a few seconds from finishing, settling inside the attempt that
// found it rather than costing a whole pass.
const removalWaitBudget = 30 * time.Second

// removeContainer deletes a container, waiting out a removal the daemon
// already has under way rather than calling it a failure.
//
// The wait asks the daemon again rather than looking: a removal in progress is
// not part of what a list or an inspect reports on current Docker, and an
// inspect queues behind the lock the removal holds. A second DELETE is
// answered at once, and says exactly where the daemon has got to -- a 409
// while it is still removing, a 404 once the container has gone, and the
// daemon's own reason if the earlier removal failed and left the container
// dead, which is then a failure worth returning. The caller treats
// ErrNotFound as it did before.
func (b *DockerBackend) removeContainer(ctx context.Context, ref string) error {
	// Timed from the first DELETE, not the first retry: that call is held
	// until the daemon has finished or the caller's deadline passes, and a
	// wait measured after it reported a removal cut off at two minutes as
	// still going "after 0s".
	started := time.Now()
	err := b.api.ContainerRemove(ctx, ref, true)
	if !errors.Is(err, ErrRemovalInProgress) {
		return err
	}
	gap := nameReleaseFirstGap
	for ctx.Err() == nil && time.Since(started) < b.removalWait {
		timer := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
		if err = b.api.ContainerRemove(ctx, ref, true); !errors.Is(err, ErrRemovalInProgress) {
			if err == nil || errors.Is(err, ErrNotFound) {
				b.log.Info("a removal the daemon already had under way has finished", "container", ref, "waited", time.Since(started).Round(100*time.Millisecond))
			}
			return err
		}
		if gap *= 2; gap > nameReleaseMaxGap {
			gap = nameReleaseMaxGap
		}
	}
	return fmt.Errorf("backend: the daemon was still removing container %s after %s; it finishes a removal it has started whether or not anyone waits for it: %w",
		ref, time.Since(started).Round(100*time.Millisecond), err)
}

// removeByName deletes a container by name if it exists, which is how Create
// stays idempotent.
func (b *DockerBackend) removeByName(ctx context.Context, name string) error {
	err := b.removeContainer(ctx, name)
	if err == nil {
		b.log.Info("replaced an existing container of the same name", "container", name)
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return fmt.Errorf("backend: removing the existing container %s: %w", name, err)
}

// Status inspects one container.
func (b *DockerBackend) Status(ctx context.Context, h Handle) (Status, error) {
	insp, err := b.api.ContainerInspect(ctx, string(h))
	if err != nil {
		return Status{}, err
	}
	return statusFromInspect(h, insp), nil
}

func statusFromInspect(h Handle, insp *ContainerInspect) Status {
	st := Status{Handle: h, Phase: PhaseStarting}
	if insp == nil || insp.State == nil {
		return st
	}
	s := insp.State
	st.ExitCode = s.ExitCode
	st.StartedAt = parseDockerTime(s.StartedAt)
	st.Message = s.Error

	switch {
	case s.Running, s.Paused:
		st.Phase = PhaseRunning
		if s.Paused {
			st.Message = "container is paused"
		}
	case s.Restarting:
		st.Phase = PhaseStarting
	case s.Status == "created":
		st.Phase = PhaseStarting
	case s.Status == "removing":
		st.Phase = PhaseGone
	default:
		st.ExitedAt = parseDockerTime(s.FinishedAt)
		if s.ExitCode == 0 && !s.OOMKilled {
			st.Phase = PhaseExited
		} else {
			st.Phase = PhaseFailed
		}
		if s.OOMKilled {
			st.Message = oomMessage(insp)
		}
	}
	return st
}

// oomMessage tells the operator what to change after an out-of-memory kill,
// which depends on where the limit came from. Telling someone to raise a pool
// field nobody set sends them to the wrong page: a limit that was the host's
// default share moves with the host's capacity, or with a limit of the pool's
// own.
func oomMessage(insp *ContainerInspect) string {
	if insp.Config != nil && insp.Config.Labels[LabelLimitsFrom] == store.AllocationFromHost {
		return "container was killed for exceeding its memory limit, which was the host's share of its memory, or the pool's minimum where that was more; " +
			"raise the pool's minimum memory so no runner is given less, set memory_mb on the pool to give its runners a limit of their own, or lower the host's capacity so each runner's share is larger"
	}
	return "container was killed for exceeding its memory limit; raise the pool's memory_mb"
}

// Stats samples one logical runner. Docker-in-Docker is two containers but one
// job, so the sidecar's build CPU and memory are included in the same sample.
// The agent handles sampling failures separately from lifecycle observations,
// retaining the last successful sample.
func (b *DockerBackend) Stats(ctx context.Context, h Handle) (Stats, error) {
	s, err := b.api.ContainerStats(ctx, string(h))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Stats{}, err
		}
		return Stats{}, err
	}
	out := Stats(s)
	insp, err := b.api.ContainerInspect(ctx, string(h))
	if err != nil || insp.Config == nil || insp.Config.Labels[LabelDockerMode] != string(store.DockerDinD) {
		return out, nil
	}
	name := insp.Config.Labels[LabelName]
	if name == "" {
		return out, nil
	}
	sidecars, err := b.api.ContainerList(ctx, map[string][]string{
		"label": {LabelManaged + "=true", LabelDinDFor + "=" + name},
	})
	if err != nil {
		return Stats{}, err
	}
	busiest := halfPercent(out.CPUPercent, insp.Config.Labels)
	for _, sidecar := range sidecars {
		sample, err := b.api.ContainerStats(ctx, sidecar.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Stats{}, err
		}
		busiest = max(busiest, halfPercent(sample.CPUPercent, sidecar.Labels))
		out = addStats(out, Stats(sample))
	}
	out.BusiestHalfPercent = busiest
	return out, nil
}

// halfPercent is a container's CPU use as a share of the quota it was created
// with, from the label its create stamped. Zero where there is no label to
// judge by -- an unlimited container, or one from an older release.
func halfPercent(cpuPercent float64, labels map[string]string) float64 {
	if half := resourcesFromLabels(labels).CPUs; half > 0 {
		return cpuPercent / half
	}
	return 0
}

func addStats(a, b Stats) Stats {
	a.CPUPercent += b.CPUPercent
	a.MemoryBytes += b.MemoryBytes
	a.MemoryLimit += b.MemoryLimit
	if a.SampledAt == nil || b.SampledAt != nil && b.SampledAt.After(*a.SampledAt) {
		a.SampledAt = b.SampledAt
	}
	if a.CPUThrottling != nil && b.CPUThrottling != nil {
		a.CPUThrottling = &CPUThrottling{
			Periods:              a.CPUThrottling.Periods + b.CPUThrottling.Periods,
			ThrottledPeriods:     a.CPUThrottling.ThrottledPeriods + b.CPUThrottling.ThrottledPeriods,
			ThrottledNanoseconds: a.CPUThrottling.ThrottledNanoseconds + b.CPUThrottling.ThrottledNanoseconds,
		}
	} else if a.CPUThrottling == nil {
		a.CPUThrottling = b.CPUThrottling
	}
	return a
}

// Logs streams a container's output, demultiplexed.
func (b *DockerBackend) Logs(ctx context.Context, h Handle, opts LogOptions) (io.ReadCloser, error) {
	return b.api.ContainerLogs(ctx, string(h), LogQuery{
		Stdout:     true,
		Stderr:     true,
		Follow:     opts.Follow,
		Tail:       opts.Tail,
		Since:      opts.Since,
		Timestamps: opts.Timestamps,
	})
}

// Stop asks the runner to finish its job and exit, then kills what is left.
func (b *DockerBackend) Stop(ctx context.Context, h Handle, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}
	err := b.api.ContainerStop(ctx, string(h), timeout)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backend: stopping container %s: %w", shortID(string(h)), err)
	}

	// The daemon's own stop escalates to SIGKILL, but a daemon that answered
	// early leaves the guarantee to us.
	insp, err := b.api.ContainerInspect(ctx, string(h))
	if err != nil || insp.State == nil || !insp.State.Running {
		return nil
	}
	b.log.Warn("container ignored the graceful stop; killing it", "container", shortID(string(h)), "timeout", timeout)
	if err := b.api.ContainerKill(ctx, string(h), "SIGKILL"); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("backend: killing container %s: %w", shortID(string(h)), err)
	}
	return nil
}

// Remove tears down the runner, its docker-in-docker sidecar and any scratch
// directory Zoomies created for it. Removing what is already gone is success.
func (b *DockerBackend) Remove(ctx context.Context, h Handle) error {
	var name, workDir, toolFarm string
	insp, err := b.api.ContainerInspect(ctx, string(h))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("backend: inspecting container before removal: %w", err)
	}
	if err == nil && insp.Config != nil {
		name = insp.Config.Labels[LabelName]
		workDir = insp.Config.Labels[LabelWorkDir]
		toolFarm = insp.Config.Labels[LabelToolFarm]
	}

	if name != "" {
		if err := b.removeByName(ctx, dindName(containerName(name))); err != nil {
			// Keep the parent and its labels so a retry can find the sidecar.
			return fmt.Errorf("backend: removing docker-in-docker sidecar for %s: %w", name, err)
		}
		b.forgetAbandonedCreate(dindName(containerName(name)))
	}
	if workDir != "" {
		// Stop writers before removing scratch space, but retain the container
		// labels until that succeeds. Otherwise its path is lost on a retry.
		if err := b.Stop(ctx, h, 10*time.Second); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := os.RemoveAll(workDir); err != nil {
			return fmt.Errorf("backend: removing runner work directory %s: %w", workDir, err)
		}
	}
	if toolFarm != "" {
		if err := b.Stop(ctx, h, 10*time.Second); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := b.removeRunnerToolFarm(ctx, *insp, os.RemoveAll); err != nil {
			return fmt.Errorf("backend: removing runner tool cache %s: %w", toolFarm, err)
		}
	}
	if err := b.removeContainer(ctx, string(h)); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("backend: removing container %s: %w", shortID(string(h)), err)
	}
	// Removed by us is seen through: a create of either name that the daemon
	// was slow to finish has nothing left to finish.
	if name != "" {
		b.forgetAbandonedCreate(containerName(name))
	}
	return nil
}

// pruneCacheFor brings this spec's cache back under its limit, when it is safe.
//
// Eviction happens as a runner is created because that was believed to be the
// moment the cache is idle. It is, for a pool that runs one runner at a time.
// For any other pool it is not: the cache belongs to every runner of its scope
// at once, so evicting as the second runner starts deletes files out from under
// the job the first is running -- the exact failure this timing was chosen to
// design out.
//
// A listing that fails counts as in use. The limit is a ceiling on how far a
// host may drift, not a quota, so carrying one runner's worth of excess costs
// some disk; deleting a running job's cache costs the job.
func (b *DockerBackend) pruneCacheFor(ctx context.Context, spec Spec) {
	// No limit is nothing to enforce, and no reason to ask the daemon.
	if spec.Cache.SizeLimit <= 0 {
		return
	}
	dir, ok := cacheDirectory(spec)
	if !ok {
		return
	}
	switch inUse, err := b.cacheInUse(ctx, dir); {
	case err != nil:
		b.log.Warn("could not tell whether another runner is using this cache, so its size limit was left unenforced this time",
			"dir", dir, "error", err)
	case inUse:
		b.log.Info("another runner is still using this cache, so its size limit was left unenforced this time",
			"dir", dir, "limit_bytes", spec.Cache.SizeLimit)
	default:
		pruneCacheContext(ctx, dir, spec.Cache.SizeLimit, b.log)
	}
}

// cacheInUse reports whether a container that has not finished is running
// against this cache directory.
//
// The question is asked of the cache itself rather than of the pool, because
// the cache is what is being deleted: two runners of one pool under a
// repository-scoped cache hold different directories and do not block each
// other, and anything that does share the directory does, whatever pool it
// belongs to. The runner container records its cache in a label, so the daemon
// can answer it directly.
func (b *DockerBackend) cacheInUse(ctx context.Context, dir string) (bool, error) {
	summaries, err := b.api.ContainerList(ctx, map[string][]string{
		"label": {LabelManaged + "=true", LabelCacheVolume + "=" + dir},
	})
	if err != nil {
		return false, fmt.Errorf("backend: listing the containers using cache %s: %w", dir, err)
	}
	for _, s := range summaries {
		switch phaseFromState(s.State) {
		case PhaseExited, PhaseFailed, PhaseGone:
			// Finished, so it is not reading the cache any more.
		default:
			return true, nil
		}
	}
	return false, nil
}

// List returns every runner container this backend owns, plus any sidecar
// whose runner has gone.
//
// The filter is on our own labels, so a host that also runs unrelated
// containers is never touched -- an agent reaping orphans must not be able to
// delete somebody's database.
//
// A sidecar is listed only once its runner container has disappeared, which is
// the one case nothing else cleans it up: Remove takes a live runner's sidecar
// with it, but a runner container that goes away out of band -- docker rm, a
// daemon restart with cleanup -- leaves a privileged daemon running for a job
// that ended. While the runner is still there, returning both would hand the
// caller two containers claiming one runner id.
func (b *DockerBackend) List(ctx context.Context) ([]Workload, error) {
	summaries, err := b.api.ContainerList(ctx, map[string][]string{
		"label": {LabelManaged + "=true"},
	})
	if err != nil {
		return nil, fmt.Errorf("backend: listing runner containers: %w", err)
	}

	// Whether a sidecar is abandoned is a question about the whole listing
	// rather than about the sidecar on its own, so the runners are gathered
	// first and the sidecars judged against them.
	out := make([]Workload, 0, len(summaries))
	var sidecars []ContainerSummary
	runnerNames := make(map[string]bool, len(summaries))
	runnerIDs := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		// A container from before the role label existed is a runner: the
		// sidecar is the only thing that has ever carried a different role.
		if s.Labels[LabelRole] == roleDinD {
			sidecars = append(sidecars, s)
			continue
		}
		if n := s.Labels[LabelName]; n != "" {
			runnerNames[n] = true
		}
		if id := s.Labels[LabelRunnerID]; id != "" {
			runnerIDs[id] = true
		}
		out = append(out, b.workloadFrom(ctx, s, false))
	}

	for _, s := range sidecars {
		// Two ways to find the runner, because leaving a live pool without its
		// Docker daemon is far worse than leaving a dead one's behind: the
		// name it was built for, and failing that the runner id both share.
		if n := s.Labels[LabelDinDFor]; n != "" && runnerNames[n] {
			continue
		}
		if id := s.Labels[LabelRunnerID]; id != "" && runnerIDs[id] {
			continue
		}
		out = append(out, b.workloadFrom(ctx, s, true))
	}
	return out, nil
}

// workloadFrom renders one container summary as a Workload.
func (b *DockerBackend) workloadFrom(ctx context.Context, s ContainerSummary, sidecar bool) Workload {
	w := Workload{
		Handle:    Handle(s.ID),
		Name:      s.Labels[LabelName],
		RunnerID:  s.Labels[LabelRunnerID],
		PoolID:    s.Labels[LabelPoolID],
		Sidecar:   sidecar,
		Status:    Status{Handle: Handle(s.ID), Phase: phaseFromState(s.State)},
		Resources: resourcesFromLabels(s.Labels),
	}
	if w.Name == "" && len(s.Names) > 0 {
		w.Name = strings.TrimPrefix(s.Names[0], "/")
	}
	// The summary has no exit code, and an exit code is the whole point of
	// looking at a container that has stopped.
	if w.Status.Phase == PhaseExited || w.Status.Phase == PhaseFailed {
		if insp, err := b.api.ContainerInspect(ctx, s.ID); err == nil {
			w.Status = statusFromInspect(Handle(s.ID), insp)
		}
	}
	return w
}

func phaseFromState(state string) Phase {
	switch state {
	case "created":
		return PhaseStarting
	case "running", "paused", "restarting":
		return PhaseRunning
	case "exited":
		return PhaseExited
	case "dead":
		return PhaseFailed
	case "removing":
		return PhaseGone
	}
	return PhaseStarting
}

// ---------------------------------------------------------------------------
// Naming helpers
// ---------------------------------------------------------------------------

// containerName makes a runner name acceptable to the daemon, which only
// allows [a-zA-Z0-9][a-zA-Z0-9_.-]*.
func containerName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "zoomies-runner"
	}
	// The daemon insists the first character is alphanumeric.
	if c := out[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		out = "z" + out
	}
	return out
}

// dindName is the sidecar name for a runner. Deriving it rather than storing it
// means a Remove can find the sidecar even when the runner row is gone.
//
// The runner *container* is a different matter: Remove learns the name from
// that container's own label, so once it has gone there is nothing left to
// derive from. That case is List's, which returns the sidecar as an orphan.
func dindName(name string) string { return containerName(name) + "-dind" }

// hostnameFor returns the hostname to set, or "" for the network modes where
// the daemon refuses to accept one.
func hostnameFor(name, networkMode string) string {
	if networkMode == "host" || strings.HasPrefix(networkMode, "container:") {
		return ""
	}
	return sanitizeHostname(name)
}

// sanitizeHostname produces an RFC 1123 hostname, since the daemon rejects
// anything else and runner names may contain characters that are fine in a
// container name but not in a hostname.
func sanitizeHostname(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if h == "" {
		h = "runner"
	}
	if len(h) > 63 {
		h = strings.Trim(h[:63], "-")
	}
	return h
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
