package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// quietLogger keeps test output readable; the backends log a warning on every
// dangerous create, which is the point but not something a test needs to see.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}

func jitSpec() Spec {
	return Spec{
		Name:        "zoomies-linux-x64-7f3a",
		RunnerID:    "run_123",
		PoolID:      "pool_1",
		PoolName:    "linux-x64",
		Image:       "ghcr.io/acme/runner:2",
		Ephemeral:   true,
		DockerMode:  store.DockerNone,
		Credentials: Credentials{JITConfig: "eyJhbGciOi"},
		Env:         map[string]string{"ZULU": "last", "ALPHA": "first"},
		Resources:   store.Resources{CPUs: 2.5, MemoryMB: 4096, PidsLimit: 512},
	}
}

func TestBuildRunnerConfigJIT(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cfg := buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{Now: now})

	env := envMap(cfg.Env)
	if env[EnvJITConfig] != "eyJhbGciOi" {
		t.Fatalf("%s = %q", EnvJITConfig, env[EnvJITConfig])
	}
	// The upstream name is set too, so a third-party image also comes up.
	if env[EnvUpstreamJITConfig] != "eyJhbGciOi" {
		t.Fatalf("%s = %q", EnvUpstreamJITConfig, env[EnvUpstreamJITConfig])
	}
	if env[EnvRunnerName] != "zoomies-linux-x64-7f3a" {
		t.Fatalf("%s = %q", EnvRunnerName, env[EnvRunnerName])
	}
	if env[EnvEphemeral] != "true" {
		t.Fatalf("%s = %q", EnvEphemeral, env[EnvEphemeral])
	}
	// A JIT config already encodes the URL, the labels and the group, so none of
	// the registration variables should be present to be mistaken for current.
	for _, k := range []string{EnvRunnerURL, EnvRunnerToken, EnvRunnerLabels, EnvRunnerGroup} {
		if _, ok := env[k]; ok {
			t.Fatalf("%s must not be set on the JIT path", k)
		}
	}
	if env["ALPHA"] != "first" || env["ZULU"] != "last" {
		t.Fatalf("pool environment missing: %v", env)
	}

	if cfg.Labels[LabelManaged] != "true" || cfg.Labels[LabelRunnerID] != "run_123" ||
		cfg.Labels[LabelPoolID] != "pool_1" || cfg.Labels[LabelPoolName] != "linux-x64" ||
		cfg.Labels[LabelName] != "zoomies-linux-x64-7f3a" {
		t.Fatalf("labels = %v", cfg.Labels)
	}
	if cfg.Labels[LabelRole] != roleRunner {
		t.Fatalf("role label = %q", cfg.Labels[LabelRole])
	}
	if cfg.Labels[LabelCreated] != now.Format(time.RFC3339) {
		t.Fatalf("created label = %q", cfg.Labels[LabelCreated])
	}

	if cfg.Tty {
		t.Fatal("a TTY would merge stderr into stdout and break log framing")
	}
	if cfg.HostConfig.AutoRemove {
		t.Fatal("auto-remove would delete the exit code and the logs")
	}
	if cfg.HostConfig.RestartPolicy.Name != "no" {
		t.Fatalf("restart policy = %q", cfg.HostConfig.RestartPolicy.Name)
	}
	if cfg.StopSignal != "SIGINT" {
		t.Fatalf("stop signal = %q, want SIGINT so a drain lets the job finish", cfg.StopSignal)
	}
}

func TestBuildRunnerConfigSecurityDefaults(t *testing.T) {
	cfg := buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{Now: time.Now()})

	if !slices.Contains(cfg.HostConfig.CapDrop, "ALL") {
		t.Fatalf("cap drop = %v, want ALL", cfg.HostConfig.CapDrop)
	}
	want := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID", "KILL"}
	if !slices.Equal(cfg.HostConfig.CapAdd, want) {
		t.Fatalf("cap add = %v, want %v", cfg.HostConfig.CapAdd, want)
	}
	if len(cfg.HostConfig.SecurityOpt) != 0 {
		t.Fatalf("security opt = %v, want none: \"no-new-privileges\" would disable the passwordless sudo the runner image grants", cfg.HostConfig.SecurityOpt)
	}
	if cfg.User != "runner" {
		t.Fatalf("user = %q, want the unprivileged account", cfg.User)
	}
	if cfg.HostConfig.ReadonlyRootfs {
		t.Fatal("builds need to write to the root filesystem")
	}
	if cfg.HostConfig.Privileged {
		t.Fatal("a runner is never privileged")
	}
}

func TestBuildRunnerConfigRunAsRoot(t *testing.T) {
	spec := jitSpec()
	spec.RunAsRoot = true
	cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()})
	if cfg.User != "" {
		t.Fatalf("user = %q, want the image default", cfg.User)
	}
}

func TestBuildRunnerConfigResources(t *testing.T) {
	cfg := buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{Now: time.Now()})
	if cfg.HostConfig.NanoCPUs != 2_500_000_000 {
		t.Fatalf("nano cpus = %d", cfg.HostConfig.NanoCPUs)
	}
	if cfg.HostConfig.Memory != 4096*1024*1024 {
		t.Fatalf("memory = %d", cfg.HostConfig.Memory)
	}
	if cfg.HostConfig.MemorySwap != cfg.HostConfig.Memory {
		t.Fatal("swap must be capped with memory, or the limit is advisory")
	}
	if cfg.HostConfig.PidsLimit == nil || *cfg.HostConfig.PidsLimit != 512 {
		t.Fatalf("pids limit = %v", cfg.HostConfig.PidsLimit)
	}

	// Zero means "the backend's own default", not "zero CPUs".
	bare := jitSpec()
	bare.Resources = store.Resources{}
	cfg = buildRunnerConfig(bare, dockerFlavor(), containerOptions{Now: time.Now()})
	if cfg.HostConfig.NanoCPUs != 0 || cfg.HostConfig.Memory != 0 || cfg.HostConfig.PidsLimit != nil {
		t.Fatalf("unset resources must stay unset: %+v", cfg.HostConfig)
	}
}

func TestBuildRunnerConfigRegistrationToken(t *testing.T) {
	spec := jitSpec()
	spec.Ephemeral = false
	spec.Credentials = Credentials{
		RegistrationToken: "AABBCC",
		URL:               "https://github.com/acme",
		RunnerGroup:       "builders",
		Labels:            []string{"linux-x64", "gpu"},
	}
	cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()})

	env := envMap(cfg.Env)
	for k, want := range map[string]string{
		EnvRunnerURL:    "https://github.com/acme",
		EnvRunnerToken:  "AABBCC",
		EnvRunnerLabels: "linux-x64,gpu",
		EnvRunnerGroup:  "builders",
		EnvEphemeral:    "false",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env[EnvJITConfig]; ok {
		t.Error("the JIT variable must not be set on the registration path")
	}
	if _, ok := env[EnvNoDefaultLabels]; ok {
		t.Error("a pool that keeps the default labels must not ask the entrypoint to drop them")
	}
}

// The entrypoint turns this variable into config.sh --no-default-labels; it
// is the only way a container runner can register without self-hosted and
// its os and arch labels.
func TestARegistrationTokenRunnerCanLeaveOutTheDefaultLabels(t *testing.T) {
	spec := jitSpec()
	spec.Ephemeral = false
	spec.Credentials = Credentials{RegistrationToken: "AABBCC", URL: "https://github.com/acme", Labels: []string{"gpu"}, NoDefaultLabels: true}
	env := envMap(buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()}).Env)
	if env[EnvNoDefaultLabels] != "true" {
		t.Fatalf("%s = %q, want true", EnvNoDefaultLabels, env[EnvNoDefaultLabels])
	}
}

func TestBuildRunnerConfigDockerModes(t *testing.T) {
	t.Run("none mounts nothing", func(t *testing.T) {
		cfg := buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{Now: time.Now()})
		if len(cfg.HostConfig.Binds) != 0 {
			t.Fatalf("binds = %v, want none", cfg.HostConfig.Binds)
		}
		if _, ok := envMap(cfg.Env)["DOCKER_HOST"]; ok {
			t.Fatal("DOCKER_HOST must not be set when a pool asked for no docker")
		}
	})

	t.Run("host socket produces a bind mount", func(t *testing.T) {
		spec := jitSpec()
		spec.DockerMode = store.DockerHostSocket
		cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{
			Now: time.Now(), HostSocket: "/run/user/1000/docker.sock",
		})
		want := "/run/user/1000/docker.sock:/var/run/docker.sock"
		if !slices.Contains(cfg.HostConfig.Binds, want) {
			t.Fatalf("binds = %v, want %q", cfg.HostConfig.Binds, want)
		}
	})

	// Mounting the socket is only half of it. The runner is a non-root user
	// inside the container and the socket is root:docker on the host, so a
	// mount without the owning group is a socket the job cannot open -- and
	// the error it gets ("permission denied while trying to connect to the
	// Docker daemon socket") points at the host, where nothing is wrong.
	t.Run("host socket carries its owning group", func(t *testing.T) {
		spec := jitSpec()
		spec.DockerMode = store.DockerHostSocket
		cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{
			Now: time.Now(), HostSocket: "/var/run/docker.sock", SocketGID: 987,
		})
		if !slices.Contains(cfg.HostConfig.GroupAdd, "987") {
			t.Fatalf("group_add = %v, want it to contain the socket's gid 987", cfg.HostConfig.GroupAdd)
		}
	})

	// Root reaches the socket already; adding the group would only widen what a
	// root container can do, for nothing.
	t.Run("a root runner is not given the socket group", func(t *testing.T) {
		spec := jitSpec()
		spec.DockerMode = store.DockerHostSocket
		spec.RunAsRoot = true
		cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{
			Now: time.Now(), HostSocket: "/var/run/docker.sock", SocketGID: 987,
		})
		if len(cfg.HostConfig.GroupAdd) != 0 {
			t.Fatalf("group_add = %v, want none", cfg.HostConfig.GroupAdd)
		}
	})

	// A socket Zoomies could not stat is still mounted: the pool asked for it,
	// and a world-writable or root-run case works anyway. Failing the create
	// here would take away a mode that does work.
	t.Run("an unreadable socket owner still mounts", func(t *testing.T) {
		spec := jitSpec()
		spec.DockerMode = store.DockerHostSocket
		cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{
			Now: time.Now(), HostSocket: "/var/run/docker.sock",
		})
		if len(cfg.HostConfig.Binds) == 0 {
			t.Fatal("the socket must still be mounted when its owner is unknown")
		}
		if len(cfg.HostConfig.GroupAdd) != 0 {
			t.Fatalf("group_add = %v, want none", cfg.HostConfig.GroupAdd)
		}
	})

	t.Run("dind joins the sidecar's network namespace", func(t *testing.T) {
		spec := jitSpec()
		spec.DockerMode = store.DockerDinD
		cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{
			Now:         time.Now(),
			NetworkMode: "container:dind-abc",
			DockerHost:  "tcp://127.0.0.1:2375",
		})
		if cfg.HostConfig.NetworkMode != "container:dind-abc" {
			t.Fatalf("network mode = %q", cfg.HostConfig.NetworkMode)
		}
		if cfg.NetworkingConfig != nil {
			t.Fatal("a container in another's namespace cannot have its own endpoint")
		}
		if cfg.Hostname != "" {
			t.Fatalf("hostname = %q; the daemon refuses a hostname with a shared namespace", cfg.Hostname)
		}
		env := envMap(cfg.Env)
		if env["DOCKER_HOST"] != "tcp://127.0.0.1:2375" {
			t.Fatalf("DOCKER_HOST = %q", env["DOCKER_HOST"])
		}
		if len(cfg.HostConfig.Binds) != 0 {
			t.Fatalf("dind must not mount the host socket, binds = %v", cfg.HostConfig.Binds)
		}
	})
}

func TestBuildDinDConfig(t *testing.T) {
	spec := jitSpec()
	spec.DockerMode = store.DockerDinD
	cfg := buildDinDConfig(spec, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage, Network: "zoomies"})

	if !cfg.HostConfig.Privileged {
		t.Fatal("a nested daemon needs privileges; without them it silently fails")
	}
	if cfg.Labels[LabelRole] != roleDinD {
		t.Fatalf("role = %q", cfg.Labels[LabelRole])
	}
	if cfg.Labels[LabelDinDFor] != spec.Name {
		t.Fatalf("dind-for = %q", cfg.Labels[LabelDinDFor])
	}
	if cfg.Labels[LabelName] != dindName(spec.Name) {
		t.Fatalf("name label = %q", cfg.Labels[LabelName])
	}
	if cfg.HostConfig.NetworkMode != "zoomies" {
		t.Fatalf("the sidecar owns the network attachment, got %q", cfg.HostConfig.NetworkMode)
	}
}

func TestBuildRunnerConfigNetwork(t *testing.T) {
	cfg := buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{Now: time.Now(), Network: "zoomies"})
	if cfg.HostConfig.NetworkMode != "zoomies" {
		t.Fatalf("network mode = %q", cfg.HostConfig.NetworkMode)
	}
	if cfg.NetworkingConfig == nil || cfg.NetworkingConfig.EndpointsConfig["zoomies"] == nil {
		t.Fatalf("networking config = %+v", cfg.NetworkingConfig)
	}
}

func TestBuildRunnerConfigWorkDirMount(t *testing.T) {
	cfg := buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{
		Now: time.Now(), WorkDirMount: "/srv/zoomies/runners/r1", WorkDirOwned: true,
	})
	want := "/srv/zoomies/runners/r1:" + RunnerWorkMount
	if !slices.Contains(cfg.HostConfig.Binds, want) {
		t.Fatalf("binds = %v, want %q", cfg.HostConfig.Binds, want)
	}
	if cfg.Labels[LabelWorkDir] != "/srv/zoomies/runners/r1" {
		t.Fatal("a directory Zoomies created must be recorded so Remove can delete it")
	}

	// A directory that already existed is mounted but not claimed.
	cfg = buildRunnerConfig(jitSpec(), dockerFlavor(), containerOptions{
		Now: time.Now(), WorkDirMount: "/srv/existing",
	})
	if _, ok := cfg.Labels[LabelWorkDir]; ok {
		t.Fatal("Zoomies must not claim a directory it did not create")
	}
}

func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"no name", Spec{Credentials: Credentials{JITConfig: "x"}}, "Name is required"},
		{"no credentials", Spec{Name: "r1"}, "JIT config or a registration token"},
		{"token without url", Spec{Name: "r1", Credentials: Credentials{RegistrationToken: "t"}}, "URL to register against"},
		{"bad docker mode", Spec{Name: "r1", Credentials: Credentials{JITConfig: "x"}, DockerMode: "sockets-please"}, "is not a docker mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Validate()
			if err == nil {
				t.Fatal("accepted an invalid spec")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %q, want it to mention %q", err, c.want)
			}
		})
	}

	ok := Spec{Name: "r1", Credentials: Credentials{JITConfig: "x"}, DockerMode: store.DockerNone}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestContainerNaming(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zoomies-linux-x64-7f3a", "zoomies-linux-x64-7f3a"},
		{"pool/runner", "pool-runner"},
		{"-leading", "z-leading"},
		{"", "zoomies-runner"},
	}
	for _, c := range cases {
		if got := containerName(c.in); got != c.want {
			t.Errorf("containerName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := dindName("runner_1"); got != "runner_1-dind" {
		t.Errorf("dindName = %q", got)
	}
	if got := sanitizeHostname("Runner_1.Big"); got != "runner-1-big" {
		t.Errorf("sanitizeHostname = %q", got)
	}
	if got := sanitizeHostname(strings.Repeat("a", 80)); len(got) != 63 {
		t.Errorf("hostname length = %d, want 63", len(got))
	}
}

func TestStatusFromInspect(t *testing.T) {
	h := Handle("abc")
	cases := []struct {
		name  string
		state *ContainerState
		want  Phase
		code  int
	}{
		{"created", &ContainerState{Status: "created"}, PhaseStarting, 0},
		{"running", &ContainerState{Status: "running", Running: true}, PhaseRunning, 0},
		{"clean exit", &ContainerState{Status: "exited", ExitCode: 0}, PhaseExited, 0},
		{"failed exit", &ContainerState{Status: "exited", ExitCode: 137}, PhaseFailed, 137},
		{"oom", &ContainerState{Status: "exited", ExitCode: 0, OOMKilled: true}, PhaseFailed, 0},
		{"removing", &ContainerState{Status: "removing"}, PhaseGone, 0},
	}
	for _, c := range cases {
		got := statusFromInspect(h, &ContainerInspect{State: c.state})
		if got.Phase != c.want || got.ExitCode != c.code {
			t.Errorf("%s: phase %q code %d, want %q %d", c.name, got.Phase, got.ExitCode, c.want, c.code)
		}
	}

	oom := statusFromInspect(h, &ContainerInspect{State: &ContainerState{Status: "exited", OOMKilled: true}})
	if !strings.Contains(oom.Message, "memory_mb") {
		t.Fatalf("an OOM kill must tell the operator what to change: %q", oom.Message)
	}

	timed := statusFromInspect(h, &ContainerInspect{State: &ContainerState{
		Status: "exited", StartedAt: "2026-01-02T03:04:05Z", FinishedAt: "2026-01-02T03:09:05Z",
	}})
	if timed.ExitedAt.Sub(timed.StartedAt) != 5*time.Minute {
		t.Fatalf("timestamps = %v .. %v", timed.StartedAt, timed.ExitedAt)
	}
}

func TestPhaseFromState(t *testing.T) {
	for state, want := range map[string]Phase{
		"created": PhaseStarting, "running": PhaseRunning, "paused": PhaseRunning,
		"exited": PhaseExited, "dead": PhaseFailed, "removing": PhaseGone,
	} {
		if got := phaseFromState(state); got != want {
			t.Errorf("%s -> %s, want %s", state, got, want)
		}
	}
}

func TestNewDockerRejectsABadPullPolicy(t *testing.T) {
	_, err := NewDocker(DockerOptions{Host: "unix:///var/run/docker.sock", PullPolicy: "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "if-missing") {
		t.Fatalf("got %v, want the valid policies listed", err)
	}
}

func TestDockerProbeUnavailable(t *testing.T) {
	b, err := NewDocker(DockerOptions{Host: "unix:///nonexistent/zoomies/docker.sock", Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	info := b.Probe(context.Background())
	if info.Available {
		t.Fatal("a missing socket must not report as available")
	}
	if info.Kind != store.BackendDocker {
		t.Fatalf("kind = %q", info.Kind)
	}
	if !strings.Contains(info.Detail, "/nonexistent/zoomies/docker.sock") {
		t.Fatalf("detail must name the socket: %q", info.Detail)
	}
	if info.SupportsDinD {
		t.Fatal("an unavailable backend supports nothing")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle against a fake daemon
// ---------------------------------------------------------------------------

func dockerBackendFor(t *testing.T, f *fakeEngine, opts DockerOptions) *DockerBackend {
	t.Helper()
	opts.Host = "tcp://" + f.Listener.Addr().String()
	if opts.Logger == nil {
		opts.Logger = quietLogger()
	}
	b, err := NewDocker(opts)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	return b
}

func TestDockerProbeAvailable(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/_ping":   func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK")) },
		"GET " + v + "/version": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, VersionInfo{Version: "27.1.1"}) },
		"GET " + v + "/info": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, SystemInfo{ServerVersion: "27.1.1", SecurityOptions: []string{"name=rootless"}})
		},
	})
	info := dockerBackendFor(t, f, DockerOptions{}).Probe(context.Background())

	if !info.Available || info.Version != "27.1.1" || !info.Rootless || !info.SupportsDinD {
		t.Fatalf("info = %+v", info)
	}
	if !strings.Contains(info.Detail, "rootless") {
		t.Fatalf("detail = %q", info.Detail)
	}
}

// The daemon knows the machine the runners will be on, and until now the probe
// read that from /info and threw it away. It is the honest size of the host: an
// agent in a container is held to its cgroup, and the runners it starts through
// this daemon are siblings on the machine rather than children inside it.
func TestTheProbeCarriesTheMachineTheDaemonIsOn(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/_ping":   func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK")) },
		"GET " + v + "/version": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, VersionInfo{Version: "27.1.1"}) },
		"GET " + v + "/info": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, SystemInfo{ServerVersion: "27.1.1", NCPU: 64, MemTotal: 274_877_906_944})
		},
	})

	info := dockerBackendFor(t, f, DockerOptions{}).Probe(context.Background())

	if info.CPUs != 64 {
		t.Fatalf("cpus = %d, want the 64 the daemon reported", info.CPUs)
	}
	// 256 GiB in megabytes, because that is the unit the host row speaks in.
	if info.MemoryMB != 262_144 {
		t.Fatalf("memory = %d MB, want 262144", info.MemoryMB)
	}
}

// A daemon that says nothing about its machine says nothing, rather than
// saying zero -- an older one, or one behind a proxy that trims /info.
func TestTheProbeSaysNothingAboutAMachineTheDaemonDidNotDescribe(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/_ping":   func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK")) },
		"GET " + v + "/version": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, VersionInfo{Version: "27.1.1"}) },
		"GET " + v + "/info": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, SystemInfo{ServerVersion: "27.1.1"})
		},
	})

	info := dockerBackendFor(t, f, DockerOptions{}).Probe(context.Background())

	if info.CPUs != 0 || info.MemoryMB != 0 {
		t.Fatalf("info = %d cpus, %d MB; nothing said must stay nothing", info.CPUs, info.MemoryMB)
	}
}

func TestDockerCreate(t *testing.T) {
	var created ContainerCreateRequest
	started := false
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
		},
		"GET " + v + "/images/{ref...}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"Id": "sha256:cached"})
		},
		"POST " + v + "/containers/create": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&created)
			writeJSON(w, http.StatusCreated, map[string]any{"Id": "c1"})
		},
		"POST " + v + "/containers/c1/start": func(w http.ResponseWriter, r *http.Request) {
			started = true
			w.WriteHeader(http.StatusNoContent)
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	result, err := b.CreateWithResult(context.Background(), jitSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h := result.Handle
	if result.ImagePullDuration != nil {
		t.Fatalf("cached image reported a pull duration: %v", *result.ImagePullDuration)
	}
	if h != "c1" {
		t.Fatalf("handle = %q", h)
	}
	if !started {
		t.Fatal("the container was created but never started")
	}
	if created.Labels[LabelRunnerID] != "run_123" {
		t.Fatalf("labels = %v", created.Labels)
	}
	// Names are inspected before any deletion; absence is already clean.
	if f.request(http.MethodGet, v+"/containers/zoomies-linux-x64-7f3a/json") == nil {
		t.Fatal("create must inspect ownership before replacing a container")
	}
}

func TestDockerCreateUsesPoolPullPolicyAndResolvedDigest(t *testing.T) {
	const first = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const second = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	current, pulls, creates := first, 0, 0
	var used []string
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "absent"})
		},
		"GET " + v + "/images/{ref...}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"Id": current, "RepoDigests": []string{"ghcr.io/acme/runner@" + current}})
		},
		"POST " + v + "/images/create": func(w http.ResponseWriter, r *http.Request) {
			pulls++
			if pulls > 1 {
				current = second
			}
			_, _ = w.Write([]byte("{}\n"))
		},
		"POST " + v + "/containers/create": func(w http.ResponseWriter, r *http.Request) {
			var req ContainerCreateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			used = append(used, req.Image)
			creates++
			writeJSON(w, http.StatusCreated, map[string]any{"Id": fmt.Sprintf("c%d", creates)})
		},
		"POST " + v + "/containers/c1/start": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"POST " + v + "/containers/c2/start": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	b := dockerBackendFor(t, f, DockerOptions{PullPolicy: PullNever})
	spec := jitSpec()
	spec.PullPolicy = store.PullAlways
	firstResult, err := b.CreateWithResult(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := b.CreateWithResult(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if pulls != 2 || firstResult.ImagePullDuration == nil || secondResult.ImagePullDuration == nil {
		t.Fatalf("always policy pulled %d times; durations %v, %v", pulls, firstResult.ImagePullDuration, secondResult.ImagePullDuration)
	}
	if firstResult.Digest != first || secondResult.Digest != second || !slices.Equal(used, []string{"ghcr.io/acme/runner@" + first, "ghcr.io/acme/runner@" + second}) {
		t.Fatalf("results %q/%q and create refs %v do not track the moving tag by a reference the daemon resolves", firstResult.Digest, secondResult.Digest, used)
	}
}

func TestDockerCreateIfNotPresentAndPinnedOnly(t *testing.T) {
	const digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	pulls := 0
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 404, map[string]string{"message": "absent"})
		},
		"GET " + v + "/images/{ref...}":      func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"Id": digest}) },
		"POST " + v + "/images/create":       func(w http.ResponseWriter, r *http.Request) { pulls++; _, _ = w.Write([]byte("{}\n")) },
		"POST " + v + "/containers/create":   func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 201, map[string]string{"Id": "c1"}) },
		"POST " + v + "/containers/c1/start": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) },
	})
	b := dockerBackendFor(t, f, DockerOptions{PullPolicy: PullAlways})
	spec := jitSpec()
	spec.PullPolicy = store.PullIfNotPresent
	result, err := b.CreateWithResult(context.Background(), spec)
	if err != nil || result.Digest != digest || pulls != 0 || result.ImagePullDuration != nil {
		t.Fatalf("if-not-present result=%+v pulls=%d err=%v", result, pulls, err)
	}
	spec.PullPolicy = store.PullPinnedOnly
	if _, err := b.Create(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "pinned-only") {
		t.Fatalf("pinned-only accepted tag: %v", err)
	}
}

func TestDockerCreateWithoutImage(t *testing.T) {
	f := newFakeEngine(t, nil)
	b := dockerBackendFor(t, f, DockerOptions{})
	spec := jitSpec()
	spec.Image = ""
	_, err := b.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "has no image") {
		t.Fatalf("got %v", err)
	}
}

func TestDockerCreatePullNever(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
		},
		"GET " + v + "/images/{ref...}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such image"})
		},
		"POST " + v + "/images/create": func(w http.ResponseWriter, r *http.Request) {
			t.Error("the never policy must not reach the registry")
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{PullPolicy: PullNever})

	_, err := b.Create(context.Background(), jitSpec())
	if err == nil || !strings.Contains(err.Error(), "pull policy") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "docker pull ghcr.io/acme/runner:2") {
		t.Fatalf("the message must say what to run: %v", err)
	}
}

func TestDockerListOnlyReturnsOurRunners(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			var filters map[string][]string
			if err := json.Unmarshal([]byte(r.Form.Get("filters")), &filters); err != nil {
				t.Errorf("filters not JSON: %v", err)
			}
			if !slices.Contains(filters["label"], LabelManaged+"=true") {
				t.Errorf("list must filter on the managed label, got %v", filters)
			}
			writeJSON(w, 200, []ContainerSummary{{
				ID: "c1", State: "running",
				Labels: map[string]string{LabelName: "runner-1", LabelRunnerID: "run_1", LabelPoolID: "pool_1"},
			}})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].RunnerID != "run_1" || got[0].Status.Phase != PhaseRunning {
		t.Fatalf("workloads = %+v", got)
	}
}

// A live pool with docker-in-docker has two containers, and only one of them
// is the runner. Returning both would give the caller two workloads carrying
// one runner id, and whichever it looked at first would decide which container
// a stop task killed.
func TestDockerListLeavesALiveRunnersSidecarAlone(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{
				{
					ID: "dind1", State: "running",
					Labels: map[string]string{
						LabelRole: roleDinD, LabelDinDFor: "runner-1",
						LabelName: "runner-1-dind", LabelRunnerID: "run_1",
					},
				},
				{
					ID: "c1", State: "running",
					Labels: map[string]string{
						LabelRole: roleRunner, LabelName: "runner-1", LabelRunnerID: "run_1",
					},
				},
			})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("only the runner belongs in the listing, got %+v", got)
	}
	if got[0].Handle != "c1" || got[0].Sidecar {
		t.Fatalf("workload = %+v", got[0])
	}
}

// A runner container removed out of band -- docker rm, or a daemon restart
// that cleaned up -- takes nothing with it, so its privileged sidecar keeps
// running for a job that ended. Listing it is what lets the agent reap it.
func TestDockerListReturnsTheSidecarOfARunnerThatHasGone(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{{
				ID: "dind1", State: "running",
				Labels: map[string]string{
					LabelRole: roleDinD, LabelDinDFor: "runner-1",
					LabelName: "runner-1-dind", LabelRunnerID: "run_1", LabelPoolID: "pool_1",
				},
			}})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the abandoned sidecar must be listed, got %+v", got)
	}
	if !got[0].Sidecar {
		t.Fatal("the sidecar must say what it is, or the agent will manage it as a runner")
	}
	// It keeps the runner id: that is the only thing that says whose
	// leftovers these are when an operator reads the log line.
	if got[0].RunnerID != "run_1" || got[0].Handle != "dind1" {
		t.Fatalf("workload = %+v", got[0])
	}
}

// A sidecar built before the name label was written, or by a version that
// named it differently, still shares its runner's id. That is enough to know
// the runner is alive, and leaving a live pool without its Docker daemon is a
// far worse mistake than leaving a dead pool's daemon behind.
func TestDockerListPairsASidecarByRunnerIdWhenTheNameIsMissing(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{
				{
					ID: "dind1", State: "running",
					Labels: map[string]string{LabelRole: roleDinD, LabelRunnerID: "run_1"},
				},
				{
					ID: "c1", State: "running",
					Labels: map[string]string{LabelName: "runner-1", LabelRunnerID: "run_1"},
				},
			})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Handle != "c1" {
		t.Fatalf("the sidecar of a runner that is still here must not be listed, got %+v", got)
	}
}

// The name is the purpose-built link between a sidecar and its runner -- it is
// what Remove uses -- and it is the only one left when neither container
// carries a runner id, as one built before the id was assigned does not.
func TestDockerListPairsASidecarByNameWhenThereIsNoRunnerId(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{
				{
					ID: "dind1", State: "running",
					Labels: map[string]string{LabelRole: roleDinD, LabelDinDFor: "runner-1"},
				},
				{ID: "c1", State: "running", Labels: map[string]string{LabelName: "runner-1"}},
			})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Handle != "c1" {
		t.Fatalf("the sidecar of a runner that is still here must not be listed, got %+v", got)
	}
}

func TestDockerRemoveTakesTheSidecarWithIt(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, ContainerInspect{ID: "c1", Config: &ContainerConfig{
				Labels: map[string]string{LabelName: "runner-1"},
			}})
		},
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	if err := b.Remove(context.Background(), "c1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if f.request(http.MethodDelete, v+"/containers/runner-1-dind") == nil {
		t.Fatal("the docker-in-docker sidecar was left behind")
	}
	if f.request(http.MethodDelete, v+"/containers/c1") == nil {
		t.Fatal("the runner container was not removed")
	}
}

func TestDockerRemoveOfSomethingGone(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
		},
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	if err := b.Remove(context.Background(), "gone"); err != nil {
		t.Fatalf("removing what is already gone must succeed, got %v", err)
	}
}

func TestDockerRemovalDoesNotHideASidecarFailure(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, ContainerInspect{ID: "c1", Config: &ContainerConfig{
				Labels: map[string]string{LabelName: "runner-1"},
			}})
		},
		"DELETE " + v + "/containers/runner-1-dind": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "sidecar is in use"})
		},
		"DELETE " + v + "/containers/c1": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	if err := b.Remove(context.Background(), "c1"); err == nil || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("sidecar failure must prevent confirmation: %v", err)
	}
	if f.request(http.MethodDelete, v+"/containers/c1") != nil {
		t.Fatal("parent labels were deleted before its sidecar could be removed")
	}
}

// sidecarRemovalEngine is a daemon whose sidecar DELETE answers with each of
// replies in turn, and with the last one for ever after. It counts the
// sidecar's DELETEs and records whether the runner container was deleted.
func sidecarRemovalEngine(t *testing.T, replies ...func(w http.ResponseWriter)) (*fakeEngine, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, ContainerInspect{ID: "c1", Config: &ContainerConfig{
				Labels: map[string]string{LabelName: "runner-1"},
			}})
		},
		"DELETE " + v + "/containers/runner-1-dind": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			i := min(calls, len(replies)-1)
			calls++
			mu.Unlock()
			replies[i](w)
		},
		"DELETE " + v + "/containers/c1": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	return f, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func removalInProgress(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]string{"message": "removal of container runner-1-dind is already in progress"})
}

func TestARemoveWaitsOutASidecarRemovalTheDaemonAlreadyHasUnderWay(t *testing.T) {
	// The shape behind "removal of container ...-dind is already in progress"
	// on the Runners page: an earlier DELETE the daemon is still carrying
	// out. It is not a failure -- the daemon finishes it whoever waits -- so
	// the removal that meets it waits for the 404 and carries on.
	gone := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
	}
	f, sidecarDeletes := sidecarRemovalEngine(t, removalInProgress, removalInProgress, gone)
	b := dockerBackendFor(t, f, DockerOptions{})

	if err := b.Remove(context.Background(), "c1"); err != nil {
		t.Fatalf("a removal the daemon finished while we waited was reported as a failure: %v", err)
	}
	if n := sidecarDeletes(); n != 3 {
		t.Fatalf("sidecar DELETEs = %d, want 3: two answered 'in progress', the third found it gone", n)
	}
	if f.request(http.MethodDelete, v+"/containers/c1") == nil {
		t.Fatal("the runner container was left behind once its sidecar had gone")
	}
}

func TestARemovalStillUnderWayKeepsTheRunnerAndSaysSo(t *testing.T) {
	f, _ := sidecarRemovalEngine(t, removalInProgress)
	b := dockerBackendFor(t, f, DockerOptions{})
	b.removalWait = 300 * time.Millisecond

	err := b.Remove(context.Background(), "c1")
	if !errors.Is(err, ErrRemovalInProgress) {
		t.Fatalf("a removal still under way must be told apart from a failed one, got %v", err)
	}
	// It is the busy fault, not a generic backend one: the daemon is doing
	// the work, and the only thing wrong is how long it is taking.
	if got := Fault(err); got != store.FaultBackendBusy {
		t.Fatalf("fault = %q, want %q", got, store.FaultBackendBusy)
	}
	if !strings.Contains(err.Error(), "runner-1-dind") || !strings.Contains(err.Error(), "still removing") {
		t.Fatalf("the error should name the container and say it is still being removed: %v", err)
	}
	// The runner's labels are how a retry finds its sidecar, so it stays until
	// the sidecar has gone.
	if f.request(http.MethodDelete, v+"/containers/c1") != nil {
		t.Fatal("the runner container was deleted while its sidecar was still being removed")
	}
}

func TestAnEarlierRemovalThatFailedIsReportedForItsOwnReason(t *testing.T) {
	// A removal that fails part way leaves the container dead and the flag
	// cleared, so the next DELETE is a fresh attempt -- and its refusal is
	// the daemon's real reason, which must not be dressed up as a wait.
	failed := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "unable to remove filesystem: device or resource busy"})
	}
	f, _ := sidecarRemovalEngine(t, removalInProgress, failed)
	b := dockerBackendFor(t, f, DockerOptions{})

	err := b.Remove(context.Background(), "c1")
	if err == nil || errors.Is(err, ErrRemovalInProgress) {
		t.Fatalf("a removal the daemon gave up on must fail as itself, got %v", err)
	}
	if !strings.Contains(err.Error(), "unable to remove filesystem") {
		t.Fatalf("the daemon's reason was lost: %v", err)
	}
	if f.request(http.MethodDelete, v+"/containers/c1") != nil {
		t.Fatal("the runner container was deleted although its sidecar could not be")
	}
}

func TestAWaitForARemovalUnderWayEndsWithItsCaller(t *testing.T) {
	f, _ := sidecarRemovalEngine(t, removalInProgress)
	b := dockerBackendFor(t, f, DockerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := b.Remove(ctx, "c1")
	if !errors.Is(err, ErrRemovalInProgress) {
		t.Fatalf("got %v, want a removal still in progress", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the wait outlived its caller's deadline by %s", waited)
	}
}

func TestARemovalCutOffByItsDeadlineSaysHowLongItWaited(t *testing.T) {
	// The first DELETE is held until the daemon answers or the caller's
	// deadline passes, so one cut off by the deadline has already waited all
	// of it. Measured from after it, the error told an operator a removal held
	// for two minutes had been going "after 0s".
	release := make(chan struct{})
	slow := func(w http.ResponseWriter) { <-release }
	f, _ := sidecarRemovalEngine(t, slow)
	// Registered after the engine, so it runs before the server's Close,
	// which waits for this handler to return.
	t.Cleanup(func() { close(release) })
	b := dockerBackendFor(t, f, DockerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := b.Remove(ctx, "c1")
	if !errors.Is(err, ErrRemovalInProgress) {
		t.Fatalf("a DELETE cut off by its deadline = %v, want a removal still in progress", err)
	}
	if strings.Contains(err.Error(), "after 0s") {
		t.Fatalf("the wait left out the DELETE it was held on: %v", err)
	}
}

func TestDockerRemovalDoesNotIgnoreAnInspectionFailure(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "daemon unavailable"})
		},
		"DELETE " + v + "/containers/c1": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	if err := b.Remove(context.Background(), "c1"); err == nil {
		t.Fatal("unreadable cleanup metadata was treated as successful removal")
	}
	if f.request(http.MethodDelete, v+"/containers/c1") != nil {
		t.Fatal("container was deleted without reading its cleanup metadata")
	}
}

func TestDockerStopKillsWhatIgnoresTheStop(t *testing.T) {
	killed := false
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"POST " + v + "/containers/c1/stop": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			// Still running after the stop returned.
			writeJSON(w, 200, ContainerInspect{ID: "c1", State: &ContainerState{Status: "running", Running: true}})
		},
		"POST " + v + "/containers/c1/kill": func(w http.ResponseWriter, r *http.Request) {
			killed = true
			if got := r.Form.Get("signal"); got != "SIGKILL" {
				t.Errorf("signal = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	if err := b.Stop(context.Background(), "c1", 2*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !killed {
		t.Fatal("a container that ignored the graceful stop must be killed")
	}
}

func TestDockerStatsReportsSamplingFailureToTheAgent(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/stats": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cgroup unavailable"})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.Stats(context.Background(), "c1")
	if StatusCode(err) != http.StatusInternalServerError {
		t.Fatalf("the sampler needs the failure to retain its previous reading: %v", err)
	}
	if got != (Stats{}) {
		t.Fatalf("stats = %+v", got)
	}
}

// With docker_mode: dind the builds run inside the sidecar, so a pool's limits
// were worth nothing while only the runner carried them.
func TestBuildDinDConfigCarriesThePoolsResourceLimits(t *testing.T) {
	spec := jitSpec()
	spec.DockerMode = store.DockerDinD
	spec.Resources = store.Resources{CPUs: 2, MemoryMB: 4096, PidsLimit: 512}
	cfg := buildDinDConfig(spec, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage, Network: "zoomies"})
	hc := cfg.HostConfig
	if hc.NanoCPUs != 2e9 || hc.Memory != 4096*1024*1024 || hc.MemorySwap != hc.Memory || hc.PidsLimit == nil || *hc.PidsLimit != 512 {
		t.Fatalf("the sidecar carries cpus=%d memory=%d swap=%d pids=%v; want the pool's limits", hc.NanoCPUs, hc.Memory, hc.MemorySwap, hc.PidsLimit)
	}
}

// A conflict over the runner's name protects the contested occupant from the
// by-name cleanup, and it used to protect the sidecar this very call had
// created, started and health-checked along with it. Nothing else can ever
// bind to that sidecar, no remove task is queued for a runner that failed at
// create, and a privileged daemon should not idle on an overloaded host until
// the orphan sweep reaches it -- so it goes, by the ID this call was given, and
// so does the scratch directory this call made. The foreign occupant is not
// touched, and an operator's own directory is never removed.
func TestAConflictOverTheRunnersNameStillRemovesTheSidecarThisCreateStarted(t *testing.T) {
	const sidecarID = "5a3d9e1c7b2f4a6d8c0e2f4a6b8d0c2e4f6a8b0d2c4e6f8a0b2d4c6e8f0a2b4d"
	const foreignID = "9f1e3d5c7b9a1c3e5f7a9b1d3f5c7e9a1b3d5f7c9e1a3b5d7f9c1e3a5b7d9f1c"
	for _, preexisting := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned scratch", true: "operator directory"}[preexisting], func(t *testing.T) {
			spec := jitSpec()
			spec.DockerMode = store.DockerDinD
			spec.WorkDir = filepath.Join(t.TempDir(), "scratch")
			if preexisting {
				if err := os.Mkdir(spec.WorkDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			runner := containerName(spec.Name)
			sidecar := dindName(runner)
			var (
				mu      sync.Mutex
				deletes []string
				started bool
			)
			f := newFakeEngine(t, map[string]http.HandlerFunc{
				"GET " + v + "/images/{ref...}": func(w http.ResponseWriter, r *http.Request) {
					writeJSON(w, 200, map[string]string{"Id": "sha256:cached"})
				},
				"POST " + v + "/containers/create": func(w http.ResponseWriter, r *http.Request) {
					switch r.Form.Get("name") {
					case sidecar:
						writeJSON(w, 201, map[string]string{"Id": sidecarID})
					case runner:
						writeJSON(w, 409, conflict409(runner, foreignID))
					default:
						w.WriteHeader(500)
					}
				},
				"GET " + v + "/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					up := started
					mu.Unlock()
					switch r.PathValue("id") {
					case sidecarID:
						state := &ContainerState{Status: "created"}
						if up {
							state = &ContainerState{Status: "running", Running: true, Health: &ContainerHealth{Status: "healthy"}}
						}
						writeJSON(w, 200, ContainerInspect{ID: sidecarID, Name: "/" + sidecar, State: state, Config: &ContainerConfig{}})
					case foreignID:
						writeJSON(w, 200, ContainerInspect{ID: foreignID, Name: "/" + runner, State: &ContainerState{Status: "running", Running: true}, Config: &ContainerConfig{Labels: map[string]string{
							LabelManaged: "true", LabelRunnerID: "another-runner", LabelPoolID: spec.PoolID, LabelRole: roleRunner, LabelName: spec.Name,
						}}})
					default:
						w.WriteHeader(404)
					}
				},
				"POST " + v + "/containers/{id}/start": func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					started = started || r.PathValue("id") == sidecarID
					mu.Unlock()
					w.WriteHeader(204)
				},
				"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					deletes = append(deletes, r.PathValue("id"))
					mu.Unlock()
					w.WriteHeader(204)
				},
			})
			b := dockerBackendFor(t, f, DockerOptions{})

			_, err := b.CreateWithResult(context.Background(), spec)
			if !errors.Is(err, ErrContainerConflict) || Fault(err) != store.FaultContainerConflict {
				t.Fatalf("a foreign occupant of the runner's name must still be a conflict: %v", err)
			}
			mu.Lock()
			got := slices.Clone(deletes)
			mu.Unlock()
			if !slices.Equal(got, []string{sidecarID}) {
				t.Fatalf("deletes=%v; want exactly the sidecar this call started, by ID, and never the foreign occupant", got)
			}
			_, serr := os.Stat(spec.WorkDir)
			if preexisting && serr != nil {
				t.Fatalf("operator directory was removed: %v", serr)
			}
			if !preexisting && !os.IsNotExist(serr) {
				t.Fatalf("scratch this call created survived the conflict: %v", serr)
			}
		})
	}
}

// The production incident, end to end, as the agent's busy retry drives it: a
// host too slow to answer a sidecar create inside the header timeout, twice.
// The first call ends busy -- not as a conflict, so the agent retries and the
// scheduler steers the replacement to another host -- and removes nothing. The
// retry meets the daemon's 409 for the container it finished late, waits for it
// to become inspectable, adopts it, and binds the runner to it. No container is
// removed at any point, and nobody is sent after a duplicate agent.
func TestASlowSidecarCreateIsAdoptedByTheAgentsRetryRatherThanFailedAsAConflict(t *testing.T) {
	const sidecarID = "446231582fb9a5d3c2e1f0b9a8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9"
	const runnerID = "7c2b4d6e8f0a2c4e6f8a0b2d4f6a8c0e2f4a6b8d0c2e4f6a8b0d2c4e6f8a0b2d"
	spec := jitSpec()
	spec.DockerMode = store.DockerDinD
	runner := containerName(spec.Name)
	sidecar := dindName(runner)
	var (
		mu              sync.Mutex
		sidecarCreates  int
		sidecarInspects int
		sidecarBody     ContainerCreateRequest
		runnerBodies    []ContainerCreateRequest
		deletes, starts []string
	)
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/images/{ref...}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]string{"Id": "sha256:cached"})
		},
		"POST " + v + "/containers/create": func(w http.ResponseWriter, r *http.Request) {
			// Drained before the handler may block; see slowCreateDaemon.
			var body ContainerCreateRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			name := r.Form.Get("name")
			mu.Lock()
			var attempt int
			switch name {
			case sidecar:
				sidecarCreates++
				attempt = sidecarCreates
				sidecarBody = body
			case runner:
				runnerBodies = append(runnerBodies, body)
			}
			mu.Unlock()
			switch {
			case name == runner:
				writeJSON(w, 201, map[string]string{"Id": runnerID})
			case name != sidecar:
				w.WriteHeader(500)
			case attempt <= 2:
				// The two sidecar creates the daemon is too slow to answer.
				<-r.Context().Done()
			default:
				writeJSON(w, 409, conflict409(sidecar, sidecarID))
			}
		},
		"GET " + v + "/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
			ref := r.PathValue("id")
			mu.Lock()
			var (
				answer any
				status = 404
			)
			switch ref {
			case sidecarID, sidecar:
				// Unregistered until the third sidecar create has been served,
				// and for two inspects after that.
				if sidecarCreates >= 3 {
					if sidecarInspects++; sidecarInspects > 2 {
						insp := echoInspect(sidecarID, sidecar, sidecarBody, "")
						if slices.Contains(starts, sidecarID) {
							insp.State = &ContainerState{Status: "running", Running: true, Health: &ContainerHealth{Status: "healthy"}}
						}
						answer, status = insp, 200
					}
				}
			case runnerID:
				answer, status = ContainerInspect{ID: runnerID, State: &ContainerState{Status: "running", Running: true}}, 200
			}
			mu.Unlock()
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			writeJSON(w, 200, answer)
		},
		"POST " + v + "/containers/{id}/start": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			starts = append(starts, r.PathValue("id"))
			mu.Unlock()
			w.WriteHeader(204)
		},
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			deletes = append(deletes, r.PathValue("id"))
			mu.Unlock()
			w.WriteHeader(204)
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	shortenHeaderTimeout(t, b, 300*time.Millisecond)
	b.nameRelease = 10 * time.Millisecond
	b.nameSettle = 5 * time.Second

	// The first call, as the agent makes it.
	_, err := b.CreateWithResult(context.Background(), spec)
	if err == nil {
		t.Fatal("the first call succeeded against a daemon that answered nothing")
	}
	if Fault(err) != store.FaultBackendBusy || errors.Is(err, ErrContainerConflict) {
		t.Fatalf("the first call failed as %q, want backend_busy so the agent retries and the scheduler steers away: %v", Fault(err), err)
	}
	mu.Lock()
	removed := slices.Clone(deletes)
	mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("the first call's cleanup removed %v", removed)
	}

	// The retry the agent makes after createBusyBackoff, with the same spec.
	result, err := b.CreateWithResult(context.Background(), spec)
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if result.Handle != Handle(runnerID) {
		t.Fatalf("handle = %q, want the runner %s", result.Handle, shortID(runnerID))
	}
	if len(deletes) != 0 {
		t.Fatalf("removed %v; the incident must cost no container", deletes)
	}
	if sidecarCreates != 5 {
		t.Fatalf("sidecar creates = %d; want the two unanswered, then one 409 per 404 inspect and one for the inspect that adopted", sidecarCreates)
	}
	if len(runnerBodies) != 1 || runnerBodies[0].HostConfig == nil || runnerBodies[0].HostConfig.NetworkMode != "container:"+sidecarID {
		t.Fatalf("runner creates = %+v; want exactly one, bound to the adopted sidecar's namespace", runnerBodies)
	}
	if !slices.Equal(starts, []string{sidecarID, runnerID}) {
		t.Fatalf("starts = %v; want the adopted sidecar started, then the runner", starts)
	}
	if _, live := b.abandonedCreateAt(sidecar); live {
		t.Fatal("the abandoned-create record outlived the adoption")
	}
}
