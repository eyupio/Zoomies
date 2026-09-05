package backend

// The Docker backend. Podman reuses everything here except the handful of
// differences documented in podman.go.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
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
//	                                   [--ephemeral if ZOOMIES_EPHEMERAL=true] --disableupdate
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

	// EnvUpstreamJITConfig is the name actions/runner itself understands. It is
	// set alongside EnvJITConfig so that an operator can point a pool at a
	// third-party runner image and still have it come up.
	EnvUpstreamJITConfig = "ACTIONS_RUNNER_INPUT_JITCONFIG"
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
)

// Role label values.
const (
	roleRunner = "runner"
	roleDinD   = "dind"
)

// RunnerWorkMount is where a per-runner host scratch directory is bind-mounted
// inside the container.
const RunnerWorkMount = "/home/runner/_work"

// RunnerCacheMount is disposable performance cache space, not persistent
// workflow storage. Operators may evict its contents at any time.
const RunnerCacheMount = "/opt/zoomies-cache"

// DefaultDinDImage is the sidecar image used for docker-in-docker pools.
const DefaultDinDImage = "docker:27-dind"

// dindPort is the TCP port the sidecar's daemon listens on inside the network
// namespace the two containers share. It is never published to the host.
const dindPort = 2375

// dindStartTimeout bounds how long we wait for the sidecar container to come up
// before giving up on the pair.
const dindStartTimeout = 30 * time.Second

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
	Logger       *slog.Logger
}

// flavor holds the few behaviours that differ between Docker and Podman. It
// exists so that podman.go can be a page of differences instead of a copy.
type flavor struct {
	kind         store.BackendKind
	displayName  string
	supportsDinD bool
	// mountSuffix is appended to bind mounts; Podman needs ":z" so that SELinux
	// relabels the host directory for the container.
	mountSuffix string
	securityOpt []string
	capDrop     []string
	capAdd      []string
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
		securityOpt:  []string{"no-new-privileges"},
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
	log     *slog.Logger
}

var _ Backend = (*DockerBackend)(nil)

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
		log:     log.With("backend", string(fl.kind)),
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
		Kind:     b.fl.kind,
		Endpoint: b.api.Endpoint(),
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

// unreachableDetail turns a transport failure into a sentence naming the fix.
func (b *DockerBackend) unreachableDetail(err error) string {
	if sock := b.api.SocketPath(); sock != "" {
		if serr := CanUseDockerSocket(sock); serr != nil {
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
}

// buildRunnerConfig assembles the container config for one runner.
func buildRunnerConfig(spec Spec, fl flavor, o containerOptions) ContainerCreateRequest {
	labels := spec.Labels(o.Now)
	labels[LabelRole] = roleRunner
	if source, err := cacheSource(spec); err == nil && source != "" {
		labels[LabelCacheVolume] = source
		labels[LabelCacheSizeLimit] = fmt.Sprint(spec.Cache.SizeLimit)
	}
	if o.WorkDirOwned && o.WorkDirMount != "" {
		labels[LabelWorkDir] = o.WorkDirMount
	}

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
			// AutoRemove would delete the container the instant it exits, taking
			// its exit code and its logs with it -- and those are the two things
			// a failed job investigation needs.
			AutoRemove:    false,
			RestartPolicy: RestartPolicy{Name: "no"},
			SecurityOpt:   slices.Clone(fl.securityOpt),
			CapDrop:       slices.Clone(fl.capDrop),
			CapAdd:        slices.Clone(fl.capAdd),
			NetworkMode:   o.NetworkMode,
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
	if spec.Resources.CPUs > 0 {
		hc.NanoCPUs = int64(spec.Resources.CPUs * 1e9)
	}
	if spec.Resources.MemoryMB > 0 {
		hc.Memory = spec.Resources.MemoryMB * 1024 * 1024
		// Without an equal swap limit the container can swap past its memory
		// cap, which turns an OOM into an unexplained slowdown.
		hc.MemorySwap = hc.Memory
	}
	if spec.Resources.PidsLimit > 0 {
		limit := spec.Resources.PidsLimit
		hc.PidsLimit = &limit
	}

	if o.HostSocket != "" {
		hc.Binds = append(hc.Binds, o.HostSocket+":/var/run/docker.sock"+fl.mountSuffix)
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

// cacheSource derives an isolated daemon volume or host directory without
// placing unchecked pool/repository values into it.
func cacheSource(spec Spec) (string, error) {
	c := spec.Cache
	if !c.Enabled {
		return "", nil
	}
	if !c.Scope.Valid() {
		return "", fmt.Errorf("backend: %q is not a cache scope", c.Scope)
	}
	key := spec.PoolID
	if c.Scope == store.CacheScopeRepository {
		if !strings.Contains(spec.Repository, "/") {
			return "", fmt.Errorf("backend: repository cache scope requires a repository-targeted installation")
		}
		key += "-" + spec.Repository
	}
	safe := sanitizeHostname(key)
	if safe == "" || safe == "." || safe == ".." {
		return "", fmt.Errorf("backend: unsafe cache identity")
	}
	prefix := strings.TrimSpace(c.Source)
	if strings.Contains(prefix, "..") {
		return "", fmt.Errorf("backend: unsafe cache source %q: path traversal is not allowed", prefix)
	}
	if prefix == "" {
		prefix = "zoomies-cache"
	}
	if filepath.IsAbs(prefix) {
		return filepath.Join(prefix, safe), nil
	}
	if strings.ContainsAny(prefix, `/\\:`) {
		return "", fmt.Errorf("backend: unsafe cache volume prefix %q", prefix)
	}
	return prefix + "-" + safe, nil
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
	}
	if o.DockerHost != "" {
		// DOCKER_TLS_CERTDIR is emptied to match the sidecar, which listens in
		// the clear inside the network namespace the two containers share.
		env = append(env, "DOCKER_HOST="+o.DockerHost, "DOCKER_TLS_CERTDIR=")
	}

	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+spec.Env[k])
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

	cfg := ContainerCreateRequest{
		Image:    o.DinDImage,
		Hostname: sanitizeHostname(dindName(spec.Name)),
		Labels:   labels,
		Tty:      false,
		Env: []string{
			"DOCKER_TLS_CERTDIR=",
		},
		Cmd: []string{"dockerd", "--host=tcp://127.0.0.1:2375", "--host=unix:///var/run/docker.sock"},
		HostConfig: &HostConfig{
			// A nested daemon needs real privileges; this is the cost of the
			// mode, and the pool that asked for it is flagged as dangerous.
			Privileged:    true,
			AutoRemove:    false,
			RestartPolicy: RestartPolicy{Name: "no"},
			NetworkMode:   o.Network,
		},
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

// Create materialises one runner. It replaces any container of the same name,
// so that a redelivered task converges instead of failing.
func (b *DockerBackend) Create(ctx context.Context, spec Spec) (Handle, error) {
	r, err := b.CreateWithResult(ctx, spec)
	return r.Handle, err
}

func (b *DockerBackend) CreateWithResult(ctx context.Context, spec Spec) (CreateResult, error) {
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
	if err := b.removeByName(ctx, name); err != nil {
		return CreateResult{}, err
	}
	if err := b.removeByName(ctx, dindName(name)); err != nil {
		return CreateResult{}, err
	}

	pullStarted := time.Now()
	pulled, err := b.ensureImage(ctx, spec.Image)
	if err != nil {
		return CreateResult{}, err
	}
	var pullDuration *time.Duration
	if pulled {
		d := time.Since(pullStarted)
		pullDuration = &d
	}
	createStarted := time.Now()

	opts := containerOptions{Now: time.Now(), DinDImage: b.dind}
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

	var dindID string
	switch spec.DockerMode {
	case store.DockerDinD:
		if _, err := b.ensureImage(ctx, b.dind); err != nil {
			return CreateResult{}, err
		}
		dindID, err = b.startDinD(ctx, spec, opts)
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
	id, err := b.api.ContainerCreate(ctx, name, cfg)
	if err != nil {
		b.cleanupFailedCreate(ctx, dindID, workDir, owned)
		return CreateResult{}, fmt.Errorf("backend: creating container %s: %w", name, err)
	}
	if err := b.api.ContainerStart(ctx, id); err != nil {
		_ = b.api.ContainerRemove(ctx, id, true)
		b.cleanupFailedCreate(ctx, dindID, workDir, owned)
		return CreateResult{}, fmt.Errorf("backend: starting container %s: %w", name, err)
	}

	b.log.Info("runner container started",
		"runner", spec.Name, "pool", spec.PoolName, "image", spec.Image,
		"container", shortID(id), "docker_mode", string(spec.DockerMode))
	return CreateResult{Handle: Handle(id), ImagePullDuration: pullDuration, CreateDuration: time.Since(createStarted)}, nil
}

// startDinD creates and starts the sidecar, waiting until the daemon reports it
// as running.
//
// It waits on the container, not on dockerd inside it: a host that refuses
// privileged containers fails here, where the error can name the pool, instead
// of much later as a connection refused inside somebody's job. Waiting for the
// nested daemon to finish booting is the runner image's job, since only it
// knows when its first docker command runs.
func (b *DockerBackend) startDinD(ctx context.Context, spec Spec, opts containerOptions) (string, error) {
	cfg := buildDinDConfig(spec, b.fl, opts)
	id, err := b.api.ContainerCreate(ctx, dindName(containerName(spec.Name)), cfg)
	if err != nil {
		return "", fmt.Errorf("backend: creating the docker-in-docker sidecar for %s: %w", spec.Name, err)
	}
	if err := b.api.ContainerStart(ctx, id); err != nil {
		_ = b.api.ContainerRemove(ctx, id, true)
		return "", fmt.Errorf("backend: starting the docker-in-docker sidecar for %s: %w", spec.Name, err)
	}

	running := func() bool {
		insp, err := b.api.ContainerInspect(ctx, id)
		return err == nil && insp.State != nil && insp.State.Running
	}
	if !waitFor(ctx, running, dindStartTimeout, 200*time.Millisecond) {
		_ = b.api.ContainerRemove(ctx, id, true)
		return "", fmt.Errorf("backend: the docker-in-docker sidecar for %s was not running after %s; this host may not allow privileged containers, in which case the pool needs docker_mode none or the podman backend", spec.Name, dindStartTimeout)
	}
	b.log.Warn("docker-in-docker sidecar started: this runner has a privileged container",
		"runner", spec.Name, "pool", spec.PoolName, "container", shortID(id))
	return id, nil
}

func (b *DockerBackend) cleanupFailedCreate(ctx context.Context, dindID, workDir string, owned bool) {
	if dindID != "" {
		if err := b.api.ContainerRemove(ctx, dindID, true); err != nil && !errors.Is(err, ErrNotFound) {
			b.log.Warn("could not remove the docker-in-docker sidecar after a failed create", "container", shortID(dindID), "error", err)
		}
	}
	if owned && workDir != "" {
		_ = os.RemoveAll(workDir)
	}
}

// ensureImage applies the pull policy.
func (b *DockerBackend) ensureImage(ctx context.Context, image string) (bool, error) {
	if b.pull == PullAlways {
		if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
			return false, fmt.Errorf("backend: pulling %s: %w", image, err)
		}
		return true, nil
	}

	present, err := b.api.ImageInspect(ctx, image)
	if err != nil {
		return false, fmt.Errorf("backend: looking for image %s: %w", image, err)
	}
	if present {
		return false, nil
	}
	if b.pull == PullNever {
		return false, fmt.Errorf("backend: image %s is not on this host and the pull policy is %q; pull it here first (docker pull %s) or set the pull policy to if-missing", image, b.pull, image)
	}
	if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
		return false, fmt.Errorf("backend: pulling %s: %w", image, err)
	}
	return true, nil
}

func (b *DockerBackend) PrewarmImage(ctx context.Context, image string, policy store.PullPolicy) (string, error) {
	if policy == store.PullPinnedOnly && !isDigestReference(image) {
		return "", fmt.Errorf("backend: pinned-only requires an image digest")
	}
	if policy == store.PullAlways {
		if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
			return "", err
		}
	} else {
		present, err := b.api.ImageInspect(ctx, image)
		if err != nil {
			return "", err
		}
		if !present {
			if err := b.api.ImagePull(ctx, image, b.auth); err != nil {
				return "", err
			}
		}
	}
	return b.api.ImageDigest(ctx, image)
}

func isDigestReference(ref string) bool {
	parts := strings.Split(ref, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
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

// removeByName deletes a container by name if it exists, which is how Create
// stays idempotent.
func (b *DockerBackend) removeByName(ctx context.Context, name string) error {
	err := b.api.ContainerRemove(ctx, name, true)
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
			st.Message = "container was killed for exceeding its memory limit; raise the pool's memory_mb"
		}
	}
	return st
}

// Stats samples one container. A daemon that cannot answer yields a zero sample
// rather than an error, because a missing metric must not fail a heartbeat.
func (b *DockerBackend) Stats(ctx context.Context, h Handle) (Stats, error) {
	s, err := b.api.ContainerStats(ctx, string(h))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Stats{}, err
		}
		return Stats{}, nil
	}
	return Stats(s), nil
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
	var name, workDir string
	if insp, err := b.api.ContainerInspect(ctx, string(h)); err == nil && insp.Config != nil {
		name = insp.Config.Labels[LabelName]
		workDir = insp.Config.Labels[LabelWorkDir]
	}

	if name != "" {
		if err := b.removeByName(ctx, dindName(containerName(name))); err != nil {
			b.log.Warn("could not remove the docker-in-docker sidecar", "runner", name, "error", err)
		}
	}
	if err := b.api.ContainerRemove(ctx, string(h), true); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("backend: removing container %s: %w", shortID(string(h)), err)
	}
	if workDir != "" {
		if err := os.RemoveAll(workDir); err != nil {
			b.log.Warn("could not remove the runner work directory", "dir", workDir, "error", err)
		}
	}
	return nil
}

// List returns every runner container this backend owns.
//
// The filter is on our own labels, so a host that also runs unrelated
// containers is never touched -- an agent reaping orphans must not be able to
// delete somebody's database.
func (b *DockerBackend) List(ctx context.Context) ([]Workload, error) {
	summaries, err := b.api.ContainerList(ctx, map[string][]string{
		"label": {LabelManaged + "=true", LabelRole + "=" + roleRunner},
	})
	if err != nil {
		return nil, fmt.Errorf("backend: listing runner containers: %w", err)
	}

	out := make([]Workload, 0, len(summaries))
	for _, s := range summaries {
		w := Workload{
			Handle:   Handle(s.ID),
			Name:     s.Labels[LabelName],
			RunnerID: s.Labels[LabelRunnerID],
			PoolID:   s.Labels[LabelPoolID],
			Status:   Status{Handle: Handle(s.ID), Phase: phaseFromState(s.State)},
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
		out = append(out, w)
	}
	return out, nil
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
