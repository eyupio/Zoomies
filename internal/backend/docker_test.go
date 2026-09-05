package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
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
	if !slices.Contains(cfg.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Fatalf("security opt = %v", cfg.HostConfig.SecurityOpt)
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

	h, err := b.Create(context.Background(), jitSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
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
	// Idempotence: an existing container of the same name is removed first.
	if f.request(http.MethodDelete, v+"/containers/zoomies-linux-x64-7f3a") == nil {
		t.Fatal("create must replace an existing container of the same name")
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
			if !slices.Contains(filters["label"], LabelRole+"="+roleRunner) {
				t.Errorf("list must exclude sidecars, got %v", filters)
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

func TestDockerStatsNeverFailsAHeartbeat(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/stats": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cgroup unavailable"})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.Stats(context.Background(), "c1")
	if err != nil {
		t.Fatalf("an unmeasurable container must not be an error: %v", err)
	}
	if got != (Stats{}) {
		t.Fatalf("stats = %+v", got)
	}
}
