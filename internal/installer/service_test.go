package installer

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controllerSpec() ServiceSpec {
	return ServiceSpec{
		Unit:       UnitController,
		ExecPath:   "/usr/local/bin/zoomies",
		ConfigFile: "/etc/zoomies/zoomies.yaml",
		User:       "zoomies",
		Group:      "zoomies",
		StateDir:   "/var/lib/zoomies",
		ConfigDir:  "/etc/zoomies",
		Bind:       "127.0.0.1:8080",
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	out, err := RenderSystemdUnit(controllerSpec())
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	for _, want := range []string{
		"Description=Zoomies GitHub Actions runner fleet controller",
		"User=zoomies",
		"Group=zoomies",
		"ExecStart=/usr/local/bin/zoomies controller --config /etc/zoomies/zoomies.yaml",
		"WorkingDirectory=/var/lib/zoomies",
		"ReadWritePaths=/var/lib/zoomies",
		"StateDirectory=zoomies",
		"ConfigurationDirectory=zoomies",
		"TimeoutStopSec=60s",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unrendered template directive left in the unit:\n%s", out)
	}
	if strings.Contains(out, "SupplementaryGroups") {
		t.Error("no group was asked for, so the unit must not join one")
	}
	// A loopback listener on 8080 needs no capability at all.
	if !strings.Contains(out, "CapabilityBoundingSet=\n") {
		t.Errorf("expected an empty CapabilityBoundingSet:\n%s", out)
	}
	if strings.Contains(out, "CAP_NET_BIND_SERVICE") {
		t.Error("port 8080 does not need CAP_NET_BIND_SERVICE")
	}
}

func TestRenderSystemdUnitPrivilegedPort(t *testing.T) {
	spec := controllerSpec()
	spec.Bind = "0.0.0.0:443"
	out, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	if !strings.Contains(out, "AmbientCapabilities=CAP_NET_BIND_SERVICE") {
		t.Errorf("a listener on 443 needs CAP_NET_BIND_SERVICE:\n%s", out)
	}
}

func TestRenderSystemdUnitNonStandardDirectories(t *testing.T) {
	spec := controllerSpec()
	spec.StateDir = "/srv/zoomies/state"
	spec.ConfigDir = "/srv/zoomies/etc"
	spec.ConfigFile = "/srv/zoomies/etc/zoomies.yaml"
	out, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	// StateDirectory= would create /var/lib/zoomies, which is not where this
	// install lives; ReadWritePaths has to do the work instead.
	if strings.Contains(out, "StateDirectory=") {
		t.Errorf("StateDirectory must be left out for a custom path:\n%s", out)
	}
	if !strings.Contains(out, "ReadWritePaths=/srv/zoomies/state") {
		t.Errorf("ReadWritePaths must name the real state directory:\n%s", out)
	}
	if !strings.Contains(out, "WorkingDirectory=/srv/zoomies/state") {
		t.Errorf("WorkingDirectory must name the real state directory:\n%s", out)
	}
}

func TestRenderSystemdUnitDockerGroup(t *testing.T) {
	spec := controllerSpec()
	spec.SupplementaryGroups = []string{"docker"}
	spec.RuntimeName = "docker"
	spec.WantsDocker = true
	out, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	if !strings.Contains(out, "SupplementaryGroups=docker") {
		t.Errorf("the root socket needs the group:\n%s", out)
	}
	if !strings.Contains(out, "Wants=docker.service") {
		t.Errorf("a Docker-backed agent must start after the daemon:\n%s", out)
	}
}

func TestRenderAgentUnit(t *testing.T) {
	spec := controllerSpec()
	spec.Unit = UnitAgent
	spec.Command = ""
	spec.Description = ""
	spec.StopTimeout = 0

	out, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	if !strings.Contains(out, "ExecStart=/usr/local/bin/zoomies agent --config /etc/zoomies/zoomies.yaml") {
		t.Errorf("the agent unit must run the agent:\n%s", out)
	}
	if !strings.Contains(out, "Restart=always") {
		t.Errorf("an agent restarts always:\n%s", out)
	}
	// A graceful agent stop waits for in-flight jobs, which is minutes.
	if !strings.Contains(out, "TimeoutStopSec=600s") {
		t.Errorf("the agent's stop timeout must be generous:\n%s", out)
	}
}

func TestRenderSystemdUnitRequiresPaths(t *testing.T) {
	for name, mutate := range map[string]func(*ServiceSpec){
		"no binary": func(s *ServiceSpec) { s.ExecPath = "" },
		"no config": func(s *ServiceSpec) { s.ConfigFile = "" },
		"no user":   func(s *ServiceSpec) { s.User = "" },
		"no state":  func(s *ServiceSpec) { s.StateDir = "" },
	} {
		spec := controllerSpec()
		mutate(&spec)
		if _, err := RenderSystemdUnit(spec); err == nil {
			t.Errorf("%s: want an error rather than a unit that cannot start", name)
		}
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	spec := controllerSpec()
	spec.User = "ada"
	spec.Group = "staff"
	spec.StateDir = "/Users/ada/Library/Application Support/zoomies"
	spec.ConfigDir = spec.StateDir
	spec.ConfigFile = spec.StateDir + "/zoomies.yaml"

	out, err := RenderLaunchdPlist(spec)
	if err != nil {
		t.Fatalf("RenderLaunchdPlist: %v", err)
	}
	for _, want := range []string{
		"<string>sh.zoomies.controller</string>",
		"<string>/usr/local/bin/zoomies</string>",
		"<string>controller</string>",
		"<string>--config</string>",
		"<key>RunAtLoad</key>",
		"zoomies.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unrendered template directive left in the plist:\n%s", out)
	}
}

func TestRenderLaunchdPlistAgentLabel(t *testing.T) {
	spec := controllerSpec()
	spec.Unit = UnitAgent
	spec.Command = ""
	out, err := RenderLaunchdPlist(spec)
	if err != nil {
		t.Fatalf("RenderLaunchdPlist: %v", err)
	}
	if !strings.Contains(out, "sh.zoomies.agent") {
		t.Errorf("the agent job needs its own label:\n%s", out)
	}
}

func TestRenderCompose(t *testing.T) {
	var buf bytes.Buffer
	err := RenderCompose(&buf, ComposeSpec{
		ExternalURL: "https://zoomies.example.com",
		Port:        9090,
		Backend:     "docker",
		DockerHost:  "unix:///var/run/docker.sock",
		Capacity:    3,
		Embedded:    true,
		DockerGID:   998,
	})
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"ZOOMIES_EXTERNAL_URL: ${ZOOMIES_EXTERNAL_URL",
		"ZOOMIES_ENCRYPTION_KEY: ${ZOOMIES_ENCRYPTION_KEY",
		`ZOOMIES_BIND: "0.0.0.0:9090"`,
		`"127.0.0.1:9090:9090"`,
		"/var/run/docker.sock:/var/run/docker.sock",
		`- "998"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compose file is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unrendered template directive left in the compose file:\n%s", out)
	}
}

func TestServiceSpecDefaults(t *testing.T) {
	s := ServiceSpec{ExecPath: "/x", ConfigFile: "/y", User: "u", StateDir: "/s"}
	if err := s.defaults(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if s.Unit != UnitController || s.Command != "controller" {
		t.Fatalf("wrong defaults: %+v", s)
	}
	if s.Group != "u" {
		t.Fatalf("group should default to the user, got %q", s.Group)
	}
	if s.StopTimeout != 60*time.Second {
		t.Fatalf("controller stop timeout = %s", s.StopTimeout)
	}
	if !strings.HasSuffix(s.LogFile, "zoomies.log") {
		t.Fatalf("log file = %q", s.LogFile)
	}
}

func TestDetectServiceKind(t *testing.T) {
	cases := []struct {
		name string
		det  Detection
		want ServiceKind
	}{
		{"systemd wins", Detection{HasSystemd: true, HasLaunchd: true}, ServiceSystemd},
		{"launchd on macOS", Detection{HasLaunchd: true}, ServiceLaunchd},
		{"compose when docker is there", Detection{Docker: RuntimeInfo{Available: true}}, ServiceCompose},
		{"nothing at all", Detection{}, ServiceNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectServiceKind(tc.det); got != tc.want {
				t.Fatalf("DetectServiceKind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewServiceManagerRefusesWhatItCannotDrive(t *testing.T) {
	for _, kind := range []ServiceKind{ServiceCompose, ServiceNone, "sysvinit"} {
		if _, err := NewServiceManager(kind, UnitController); err == nil {
			t.Errorf("%q: want an error rather than a manager that shells out to nothing", kind)
		}
	}
}

func TestReadUnitIdentityKeepsAnUpgradeOnTheSameAccount(t *testing.T) {
	spec := controllerSpec()
	spec.User = "zoomies-svc"
	spec.Group = "zoomies-grp"
	spec.SupplementaryGroups = []string{"docker"}
	body, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	path := writeFile(t, t.TempDir(), "zoomies.service", body)

	user, group, groups := ReadUnitIdentity(path)
	if user != "zoomies-svc" || group != "zoomies-grp" {
		t.Fatalf("ReadUnitIdentity = %q/%q, want zoomies-svc/zoomies-grp", user, group)
	}
	if len(groups) != 1 || groups[0] != "docker" {
		t.Fatalf("supplementary groups = %v", groups)
	}

	if u, g, gs := ReadUnitIdentity(filepath.Join(t.TempDir(), "absent.service")); u != "" || g != "" || gs != nil {
		t.Fatalf("a missing unit has no identity, got %q/%q/%v", u, g, gs)
	}
}
