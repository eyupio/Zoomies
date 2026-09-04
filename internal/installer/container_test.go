package installer

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// containerPlan is a plan as resolvePlan would hand it over: every question
// answered, and already translated to the inside of a container.
func containerPlan(t *testing.T) Plan {
	t.Helper()
	dir := t.TempDir()
	p := Plan{
		Mode:           ModeSingle,
		Deployment:     DeploymentCompose,
		ConfigDir:      dir,
		StateDir:       dir,
		DeployDir:      dir,
		Backend:        store.BackendDocker,
		DockerHost:     "unix:///var/run/docker.sock",
		Capacity:       4,
		Embedded:       true,
		Listen:         ListenProxy,
		Bind:           "0.0.0.0:8080",
		TLSMode:        config.TLSOff,
		TrustedProxies: []string{"10.0.0.0/8"},
		ExternalURL:    "https://zoomies.example.com",
		Image:          DefaultImage,
		ComposeCommand: []string{"docker", "compose"},
		DockerGID:      998,
		GitHub:         GitHubPlan{APIBaseURL: "https://api.github.com"},
	}
	return containerise(p)
}

// composeRef finds every ${VARIABLE} the compose file interpolates.
var composeRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)`)

func composeReferences(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range composeRef.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func TestContaineriseMovesTheListenerInside(t *testing.T) {
	p := Plan{Deployment: DeploymentCompose, Bind: "127.0.0.1:9090", ConfigDir: "/etc/zoomies"}
	got := containerise(p)

	if got.Bind != "0.0.0.0:8080" {
		t.Errorf("inside the container Zoomies binds every interface on %d, got %q", ContainerPort, got.Bind)
	}
	if got.PublishedPort != 9090 {
		t.Errorf("the port the operator chose becomes the published port, got %d", got.PublishedPort)
	}
	if got.PublishAddr != "127.0.0.1" {
		t.Errorf("a loopback answer must stay on loopback, got %q", got.PublishAddr)
	}
	if got.DBPath != ContainerDBPath || got.WorkDir != ContainerWorkDir {
		t.Errorf("state must move into the volume, got db %q work %q", got.DBPath, got.WorkDir)
	}
	if got.DeployDir != "/etc/zoomies" {
		t.Errorf("the files default to the config directory, got %q", got.DeployDir)
	}

	public := containerise(Plan{Deployment: DeploymentDocker, Bind: "0.0.0.0:8080"})
	if public.PublishAddr != "0.0.0.0" {
		t.Errorf("a listener the operator opened must stay open, got %q", public.PublishAddr)
	}

	native := Plan{Deployment: DeploymentNative, Bind: "127.0.0.1:8080", DBPath: "/var/lib/zoomies/zoomies.db"}
	left := containerise(native)
	if left.Bind != native.Bind || left.DBPath != native.DBPath || left.PublishedPort != 0 {
		t.Errorf("a native deployment must be left exactly as it is, got %+v", left)
	}
}

func TestRenderComposeFileIsValidYAML(t *testing.T) {
	body, err := RenderComposeFile(ComposeFileSpecFor(containerPlan(t)))
	if err != nil {
		t.Fatalf("RenderComposeFile: %v", err)
	}
	if strings.Contains(body, "{{") {
		t.Fatalf("unrendered template directive left in the compose file:\n%s", body)
	}

	var doc struct {
		Name     string `yaml:"name"`
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Restart     string            `yaml:"restart"`
			Command     []string          `yaml:"command"`
			Environment map[string]string `yaml:"environment"`
			Ports       []string          `yaml:"ports"`
			Volumes     []string          `yaml:"volumes"`
			GroupAdd    []string          `yaml:"group_add"`
			Networks    []string          `yaml:"networks"`
			Healthcheck struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
		Volumes  map[string]any `yaml:"volumes"`
		Networks map[string]any `yaml:"networks"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the generated compose file is not valid YAML: %v\n%s", err, body)
	}

	svc, ok := doc.Services["zoomies"]
	if !ok {
		t.Fatalf("no zoomies service in:\n%s", body)
	}
	if svc.Restart != "unless-stopped" {
		t.Errorf("restart = %q; a controller must come back after a reboot", svc.Restart)
	}
	if len(svc.Command) != 1 || svc.Command[0] != "controller" {
		t.Errorf("command = %v", svc.Command)
	}
	if len(svc.Healthcheck.Test) == 0 {
		t.Error("the service must carry the healthcheck the repository's own compose file has")
	}
	if _, ok := doc.Volumes[VolumeName]; !ok {
		t.Errorf("the %s volume must be declared, or the database lives in the container", VolumeName)
	}
	if _, ok := doc.Networks["zoomies"]; !ok {
		t.Error("runner containers are attached to the zoomies network, which must exist")
	}
	// The socket comment is the one an operator most needs to not misread.
	if !strings.Contains(body, "pool.docker_mode=host-socket") {
		t.Error("the compose file must say that the socket is Zoomies' own access, not something handed to jobs")
	}
	if !containsSuffix(svc.Volumes, "/var/run/docker.sock:/var/run/docker.sock") {
		t.Errorf("an embedded agent needs the socket, got %v", svc.Volumes)
	}
	if len(svc.GroupAdd) != 1 {
		t.Errorf("a root socket needs the docker group, got %v", svc.GroupAdd)
	}
}

func TestComposeFileReferencesOnlyVariablesTheEnvSets(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Plan)
	}{
		{"the reference deployment", func(p *Plan) {}},
		{"a controller with no embedded agent", func(p *Plan) { p.Embedded = false }},
		{"terminating TLS in the container", func(p *Plan) {
			p.TLSMode = config.TLSFiles
			p.TLSCertFile = "/etc/zoomies/tls/fullchain.pem"
			p.TLSKeyFile = "/etc/zoomies/tls/privkey.pem"
		}},
		{"the process backend, which needs no socket", func(p *Plan) {
			p.Backend = store.BackendProcess
			p.DockerHost = ""
		}},
		{"a host with no docker group", func(p *Plan) { p.DockerGID = 0 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := containerPlan(t)
			tc.mutate(&p)

			spec := EnvSpecFor(p)
			spec.EncryptionKey = "c2VjcmV0LWtleS10aGF0LWlzLXRoaXJ0eS10d28h"
			env, err := RenderEnv(spec)
			if err != nil {
				t.Fatalf("RenderEnv: %v", err)
			}
			defined, _ := parseEnv(t, env)

			body, err := RenderComposeFile(ComposeFileSpecFor(p))
			if err != nil {
				t.Fatalf("RenderComposeFile: %v", err)
			}
			for _, ref := range composeReferences(body) {
				if _, ok := defined[ref]; !ok {
					t.Errorf("the compose file reads ${%s}, which the environment file does not set", ref)
				}
			}
		})
	}
}

func TestRenderComposeFileForAnAgent(t *testing.T) {
	p := containerPlan(t)
	p.Mode = ModeAgent
	p.Embedded = false

	body, err := RenderComposeFile(ComposeFileSpecFor(p))
	if err != nil {
		t.Fatalf("RenderComposeFile: %v", err)
	}
	if !strings.Contains(body, `command: ["agent"]`) {
		t.Errorf("an agent project must run the agent:\n%s", body)
	}
	if strings.Contains(body, "ports:") {
		t.Error("an agent serves nothing, so it publishes no port")
	}
	if strings.Contains(body, "ZOOMIES_ENCRYPTION_KEY") {
		t.Error("an agent stores nothing, so it must not be handed the instance key")
	}
	if !strings.Contains(body, "ZOOMIES_AGENT_TOKEN") {
		t.Error("an agent needs the credential the join returned")
	}
	// It still creates runner containers, so it still needs the socket.
	if !strings.Contains(body, "/var/run/docker.sock:/var/run/docker.sock") {
		t.Errorf("an agent needs the runtime's socket:\n%s", body)
	}
}

func TestDockerRunArgs(t *testing.T) {
	base := DockerRunSpec{
		Image:         "ghcr.io/eyupio/zoomies:v1.2.3",
		Container:     "zoomies",
		EnvFile:       "/etc/zoomies/zoomies.env",
		Volume:        "zoomies-data",
		Network:       "zoomies",
		Command:       "controller",
		Publish:       true,
		PublishAddr:   "127.0.0.1",
		PublishedPort: 9090,
		ContainerPort: 8080,
		MountSocket:   true,
		SocketPath:    "/var/run/docker.sock",
		DockerGID:     998,
	}

	cases := []struct {
		name   string
		mutate func(*DockerRunSpec)
		want   []string
		unwant []string
	}{
		{
			name:   "the reference deployment",
			mutate: func(s *DockerRunSpec) {},
			want: []string{
				"run", "--detach", "--name", "zoomies", "--restart", "unless-stopped",
				"--env-file", "/etc/zoomies/zoomies.env",
				"--publish", "127.0.0.1:9090:8080",
				"--volume", "zoomies-data:/var/lib/zoomies",
				"--volume", "/var/run/docker.sock:/var/run/docker.sock",
				"--group-add", "998",
				"--network", "zoomies",
				"ghcr.io/eyupio/zoomies:v1.2.3", "controller",
			},
		},
		{
			name:   "published on every interface",
			mutate: func(s *DockerRunSpec) { s.PublishAddr = "0.0.0.0" },
			want:   []string{"--publish", "0.0.0.0:9090:8080"},
		},
		{
			name: "the process backend takes no socket and no group",
			mutate: func(s *DockerRunSpec) {
				s.MountSocket = false
			},
			unwant: []string{"/var/run/docker.sock:/var/run/docker.sock", "--group-add"},
		},
		{
			name:   "a rootless socket needs no docker group",
			mutate: func(s *DockerRunSpec) { s.DockerGID = 0; s.SocketPath = "/run/user/1000/docker.sock" },
			want:   []string{"--volume", "/run/user/1000/docker.sock:/run/user/1000/docker.sock"},
			unwant: []string{"--group-add"},
		},
		{
			name: "a certificate is mounted read-only at the path the env names",
			mutate: func(s *DockerRunSpec) {
				s.ReadOnlyMounts = []string{"/etc/tls/fullchain.pem", "/etc/tls/privkey.pem"}
			},
			want: []string{"--volume", "/etc/tls/fullchain.pem:/etc/tls/fullchain.pem:ro"},
		},
		{
			name:   "an agent publishes nothing",
			mutate: func(s *DockerRunSpec) { s.Publish = false; s.Command = "agent" },
			want:   []string{"agent"},
			unwant: []string{"--publish"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			tc.mutate(&spec)
			args := DockerRunArgs(spec)
			line := strings.Join(args, " ")

			if tc.name == "the reference deployment" {
				if line != strings.Join(tc.want, " ") {
					t.Fatalf("argv =\n  %s\nwant\n  %s", line, strings.Join(tc.want, " "))
				}
				return
			}
			for _, want := range tc.want {
				if !contains(args, want) {
					t.Errorf("argv is missing %q: %s", want, line)
				}
			}
			for _, unwanted := range tc.unwant {
				if contains(args, unwanted) {
					t.Errorf("argv should not contain %q: %s", unwanted, line)
				}
			}
		})
	}
}

func TestDockerRunSpecForFollowsThePlan(t *testing.T) {
	p := containerPlan(t)
	p.Deployment = DeploymentDocker
	p.TLSMode = config.TLSFiles
	p.TLSCertFile, p.TLSKeyFile = "/etc/tls/cert.pem", "/etc/tls/key.pem"

	args := DockerRunArgs(DockerRunSpecFor(p, "/etc/zoomies/zoomies.env"))
	line := strings.Join(args, " ")
	for _, want := range []string{
		"--env-file /etc/zoomies/zoomies.env",
		"--publish 0.0.0.0:8080:8080",
		"--volume /etc/tls/cert.pem:/etc/tls/cert.pem:ro",
		"--group-add 998",
		DefaultImage + " controller",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the run command is missing %q:\n%s", want, line)
		}
	}
}

func TestComposeArgsUseTheCommandThisHostHas(t *testing.T) {
	// A host with only the v1 binary must never be handed a v2 command line.
	name, args := ComposeArgs([]string{"docker-compose"}, "/etc/zoomies/docker-compose.yml", "up", "-d")
	if name != "docker-compose" {
		t.Fatalf("command = %q", name)
	}
	// The env file is named rather than left to be found: compose v1 looked in
	// the working directory, and would otherwise interpolate every value to
	// empty when the operator ran this from their home directory.
	want := "-f /etc/zoomies/docker-compose.yml --env-file /etc/zoomies/.env up -d"
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}

	line := ComposeLine([]string{"docker", "compose"}, "/etc/zoomies/docker-compose.yml", "logs", "-f")
	if line != "docker compose -f /etc/zoomies/docker-compose.yml --env-file /etc/zoomies/.env logs -f" {
		t.Fatalf("line = %q", line)
	}
	// An empty command still produces something runnable rather than a crash.
	if name, _ := ComposeArgs(nil, "x.yml", "down"); name != "docker" {
		t.Fatalf("command = %q", name)
	}
}

func TestTeardownArgs(t *testing.T) {
	rec := DeploymentRecord{
		Deployment:     DeploymentCompose,
		Directory:      "/etc/zoomies",
		ComposeCommand: []string{"docker", "compose"},
		Container:      "zoomies",
		Volume:         "zoomies-data",
	}
	name, args := ComposeDownArgs(rec, false)
	want := "docker compose -f /etc/zoomies/docker-compose.yml --env-file /etc/zoomies/.env down"
	if got := name + " " + strings.Join(args, " "); got != want {
		t.Fatalf("down = %q, want %q", got, want)
	}
	_, args = ComposeDownArgs(rec, true)
	if !contains(args, "-v") {
		t.Fatalf("removing the volume must pass -v, got %v", args)
	}

	rec.Deployment = DeploymentDocker
	if got := strings.Join(DockerStopArgs(rec), " "); got != "stop zoomies" {
		t.Fatalf("stop = %q", got)
	}
	if got := strings.Join(DockerRemoveArgs(rec), " "); got != "rm zoomies" {
		t.Fatalf("rm = %q", got)
	}
	if got := strings.Join(DockerVolumeRemoveArgs(rec), " "); got != "volume rm zoomies-data" {
		t.Fatalf("volume rm = %q", got)
	}
	// A record from an older install names nothing; the defaults still have to
	// take down what that install actually made.
	empty := DeploymentRecord{Deployment: DeploymentDocker}
	if got := strings.Join(DockerStopArgs(empty), " "); got != "stop "+ContainerName {
		t.Fatalf("stop = %q", got)
	}
}

func TestDeploymentCommandsAreRunnable(t *testing.T) {
	i := &Installer{ui: newUI(&strings.Builder{})}
	p := containerPlan(t)

	lines := i.deploymentCommands(p)
	if len(lines) != 4 {
		t.Fatalf("want logs, restart, upgrade and stop; got %d", len(lines))
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"logs", "restart", "pull", "down"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the summary must include %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, filepath.Join(p.DeployDir, ComposeFileName)) {
		t.Errorf("every command must name the file it acts on:\n%s", joined)
	}

	p.Deployment = DeploymentDocker
	joined = strings.Join(i.deploymentCommands(p), "\n")
	for _, want := range []string{"docker logs -f zoomies", "docker restart zoomies", "docker pull", "docker stop zoomies"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the summary must include %q:\n%s", want, joined)
		}
	}
}

// containsSuffix reports whether any entry ends with want, which is how a
// volume list is checked without pinning the order.
func containsSuffix(list []string, want string) bool {
	for _, v := range list {
		if strings.HasSuffix(v, want) {
			return true
		}
	}
	return false
}
