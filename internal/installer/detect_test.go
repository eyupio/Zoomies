package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingInstallAtFindsNothingInAnEmptyDirectory(t *testing.T) {
	e := ExistingInstallAt(t.TempDir(), t.TempDir(), "")
	if e.Present() {
		t.Fatalf("nothing is installed here, got %+v", e)
	}
	if e.HasState() {
		t.Fatal("no key and no database means no state")
	}
	if len(e.Items()) != 0 {
		t.Fatalf("nothing to list, got %v", e.Items())
	}
}

func TestExistingInstallAtFindsEachPiece(t *testing.T) {
	cfgDir, stateDir := t.TempDir(), t.TempDir()
	writeFile(t, cfgDir, "zoomies.yaml", "server:\n  bind: 127.0.0.1:8080\n")
	writeFile(t, cfgDir, "encryption.key", "not a real key\n")
	writeFile(t, stateDir, "zoomies.db", "")
	if err := os.MkdirAll(filepath.Join(stateDir, "work"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stateDir, "work"), "agent.json", "{}")

	e := ExistingInstallAt(cfgDir, stateDir, "")
	if !e.Present() || !e.HasState() {
		t.Fatalf("want a present install with state, got %+v", e)
	}
	if e.ConfigFile == "" || e.KeyFile == "" || e.Database == "" || e.AgentState == "" {
		t.Fatalf("a piece was missed: %+v", e)
	}

	items := strings.Join(e.Items(), "\n")
	for _, want := range []string{"config", "encryption key", "database", "agent creds"} {
		if !strings.Contains(items, want) {
			t.Errorf("the summary should name %q:\n%s", want, items)
		}
	}
}

func TestExistingInstallStateIsWhatNeedsAConfirmation(t *testing.T) {
	cfgDir, stateDir := t.TempDir(), t.TempDir()
	writeFile(t, cfgDir, "zoomies.yaml", "")

	e := ExistingInstallAt(cfgDir, stateDir, "")
	if !e.Present() {
		t.Fatal("a config file is an existing install")
	}
	// Only the key and the database are irreplaceable, and only they trigger
	// the typed confirmation.
	if e.HasState() {
		t.Fatalf("a config file alone is not state: %+v", e)
	}
}

func TestExistingInstallReportsTheBinary(t *testing.T) {
	dir := t.TempDir()
	binary := writeFile(t, dir, "zoomies", "#!/bin/sh\nexit 1\n")
	e := ExistingInstallAt(t.TempDir(), t.TempDir(), binary)
	if e.Binary != binary {
		t.Fatalf("binary = %q, want %q", e.Binary, binary)
	}
	if !strings.Contains(strings.Join(e.Items(), "\n"), "binary") {
		t.Fatalf("the summary should name the binary: %v", e.Items())
	}
}

func TestDetectionLinesDescribeTheHost(t *testing.T) {
	d := Detection{
		OS: "linux", Arch: "amd64", Distro: "ubuntu", Init: InitSystemd,
		User: "root", UID: 0, Root: true,
		ConfigDir: "/etc/zoomies", StateDir: "/var/lib/zoomies",
		Docker: RuntimeInfo{Kind: "docker", Available: true, Rootless: true, Version: "27.1.1", Endpoint: "unix:///run/user/1000/docker.sock"},
		Podman: RuntimeInfo{Kind: "podman", Installed: true, Detail: "no socket at /run/podman/podman.sock"},
		Ports:  []PortStatus{{Port: 8080, Free: false, Detail: "address already in use"}},
	}
	lines := strings.Join(d.Lines(), "\n")

	for _, want := range []string{
		"linux/amd64 (ubuntu)",
		"systemd",
		"root",
		"rootless, 27.1.1 -- unix:///run/user/1000/docker.sock",
		"installed but its socket is not reachable",
		"port 8080 is not available",
		"no terminal attached",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("detection summary is missing %q:\n%s", want, lines)
		}
	}
}

func TestSocketHintOnlyGoesToItsOwnRuntime(t *testing.T) {
	opts := Options{DetectedRuntime: "docker", DetectedSocket: "/run/user/1000/docker.sock"}
	if got := socketHintFor(opts, "docker"); got != "unix:///run/user/1000/docker.sock" {
		t.Fatalf("docker hint = %q", got)
	}
	// Handing a Docker socket to the Podman probe would report a Podman daemon
	// that is not there.
	if got := socketHintFor(opts, "podman"); got != "" {
		t.Fatalf("podman hint = %q, want empty", got)
	}

	unavailable := Options{DetectedRuntime: "docker-unavailable", DetectedSocket: ""}
	if got := socketHintFor(unavailable, "docker"); got != "" {
		t.Fatalf("no socket means no hint, got %q", got)
	}
}

func TestFirstNonEmptyPrefersTheScriptsAnswer(t *testing.T) {
	if got := firstNonEmpty("", "  ", "fedora"); got != "fedora" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty(" alpine ", "debian"); got != "alpine" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
}
