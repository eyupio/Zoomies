package backend

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeEnv builds a hostEnv from a map of variables and a set of paths that
// exist, so the ordering can be checked on a machine with no container runtime.
func fakeEnv(vars map[string]string, uid int, present ...string) hostEnv {
	return hostEnv{
		getenv: func(k string) string { return vars[k] },
		uid:    uid,
		exists: func(p string) bool { return slices.Contains(present, p) },
	}
}

func TestDockerCandidateOrder(t *testing.T) {
	e := fakeEnv(map[string]string{
		"XDG_RUNTIME_DIR": "/run/user/1000",
		"HOME":            "/home/ops",
	}, 1000)

	got := dockerCandidates(e)
	want := []string{
		"unix:///run/user/1000/docker.sock",
		"unix:///home/ops/.docker/run/docker.sock",
		"unix:///var/run/docker.sock",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// The point of the ordering: the system socket is the last resort.
	if got[len(got)-1] != "unix:///var/run/docker.sock" {
		t.Fatal("the rootful socket must come last")
	}
}

func TestDockerCandidatePrefersExplicitHost(t *testing.T) {
	e := fakeEnv(map[string]string{
		"DOCKER_HOST":     "tcp://10.0.0.5:2375",
		"XDG_RUNTIME_DIR": "/run/user/1000",
	}, 1000)

	got := dockerCandidates(e)
	if got[0] != "tcp://10.0.0.5:2375" {
		t.Fatalf("DOCKER_HOST must win, got %v", got)
	}
}

func TestDockerCandidateDeduplicates(t *testing.T) {
	// XDG_RUNTIME_DIR normally is /run/user/<uid>, which would otherwise be
	// offered twice.
	e := fakeEnv(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, 1000)
	got := dockerCandidates(e)
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c] {
			t.Fatalf("duplicate candidate %s in %v", c, got)
		}
		seen[c] = true
	}
}

func TestPodmanCandidateOrder(t *testing.T) {
	e := fakeEnv(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, 1000)
	got := podmanCandidates(e)
	want := []string{
		"unix:///run/user/1000/podman/podman.sock",
		"unix:///run/podman/podman.sock",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	e = fakeEnv(map[string]string{"CONTAINER_HOST": "unix:///tmp/podman.sock"}, 1000)
	if got := podmanCandidates(e); got[0] != "unix:///tmp/podman.sock" {
		t.Fatalf("CONTAINER_HOST must win, got %v", got)
	}
}

func TestDetectHostFiltersToWhatExists(t *testing.T) {
	t.Run("only existing sockets", func(t *testing.T) {
		e := fakeEnv(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, 1000, "/var/run/docker.sock")
		got := detectHost(e, dockerCandidates)
		if !slices.Equal(got, []string{"unix:///var/run/docker.sock"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("rootless first when both exist", func(t *testing.T) {
		e := fakeEnv(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, 1000,
			"/var/run/docker.sock", "/run/user/1000/docker.sock")
		got := detectHost(e, dockerCandidates)
		if got[0] != "unix:///run/user/1000/docker.sock" {
			t.Fatalf("the rootless socket must be preferred, got %v", got)
		}
	})

	t.Run("nothing exists still names the candidates", func(t *testing.T) {
		e := fakeEnv(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, 1000)
		got := detectHost(e, dockerCandidates)
		if len(got) != len(dockerCandidates(e)) {
			t.Fatalf("got %v, want every candidate so the error can name them", got)
		}
	})

	t.Run("a tcp endpoint is never filtered out", func(t *testing.T) {
		e := fakeEnv(map[string]string{"DOCKER_HOST": "tcp://10.0.0.5:2375"}, 1000)
		got := detectHost(e, dockerCandidates)
		if got[0] != "tcp://10.0.0.5:2375" {
			t.Fatalf("got %v", got)
		}
	})
}

func TestIsRootless(t *testing.T) {
	cases := []struct {
		name string
		info SystemInfo
		want bool
	}{
		{"docker rootless security option", SystemInfo{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"}}, true},
		{"podman flag", SystemInfo{Rootless: true}, true},
		{"root dir under /run/user", SystemInfo{DockerRootDir: "/run/user/1000/docker"}, true},
		{"rootful", SystemInfo{SecurityOptions: []string{"name=seccomp,profile=builtin"}, DockerRootDir: "/var/lib/docker"}, false},
		{"empty", SystemInfo{}, false},
	}
	for _, c := range cases {
		if got := IsRootless(c.info); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsRootlessEndpoint(t *testing.T) {
	if !IsRootlessEndpoint("unix:///run/user/1000/podman/podman.sock") {
		t.Error("a /run/user socket is rootless")
	}
	if IsRootlessEndpoint("/var/run/docker.sock") {
		t.Error("the system socket is not rootless")
	}
	if IsRootlessEndpoint("") {
		t.Error("an empty path is not rootless")
	}
}

func TestCanUseDockerSocket(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing", func(t *testing.T) {
		err := CanUseDockerSocket(filepath.Join(dir, "absent.sock"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v", err)
		}
		if !strings.Contains(err.Error(), "no socket at") || !strings.Contains(err.Error(), "absent.sock") {
			t.Fatalf("the message must name the path and the fix: %v", err)
		}
	})

	t.Run("not a socket", func(t *testing.T) {
		path := filepath.Join(dir, "regular-file")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := CanUseDockerSocket(path)
		if err == nil || !strings.Contains(err.Error(), "is not a socket") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("listening", func(t *testing.T) {
		path := filepath.Join(dir, "live.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Skipf("unix sockets unavailable here: %v", err)
		}
		defer l.Close()
		if err := CanUseDockerSocket(path); err != nil {
			t.Fatalf("a listening socket must be usable: %v", err)
		}
		// The unix:// form must work too, since that is what config carries.
		if err := CanUseDockerSocket("unix://" + path); err != nil {
			t.Fatalf("unix:// form rejected: %v", err)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		path := filepath.Join(dir, "dead.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Skipf("unix sockets unavailable here: %v", err)
		}
		// Closing the listener without unlinking leaves the socket file behind,
		// which is exactly the state a crashed daemon leaves.
		addr := l.Addr().(*net.UnixAddr)
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
		if _, err := os.Stat(addr.Name); err != nil {
			t.Skipf("socket file did not survive close: %v", err)
		}
		err = CanUseDockerSocket(path)
		if err == nil || !strings.Contains(err.Error(), "nothing is listening") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if err := CanUseDockerSocket(""); err == nil {
			t.Fatal("an empty path must be an error")
		}
	})

	t.Run("tcp is left to the ping", func(t *testing.T) {
		if err := CanUseDockerSocket("tcp://10.0.0.5:2375"); err != nil {
			t.Fatalf("got %v", err)
		}
	})
}

func TestCanUseDockerSocketPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permissions, so this check cannot fail here")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "locked")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "docker.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	defer l.Close()
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	err = CanUseDockerSocket(path)
	if err == nil || !strings.Contains(err.Error(), "docker group") {
		t.Fatalf("a permission failure must name the group to join, got %v", err)
	}
}

func TestPickEndpoint(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.sock")
	l, err := net.Listen("unix", live)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	defer l.Close()

	got := pickEndpoint([]string{filepath.Join(dir, "absent.sock"), live}, "/var/run/docker.sock")
	if got != live {
		t.Fatalf("got %q, want the socket that answers", got)
	}

	// With nothing reachable, the first candidate is returned so that Probe
	// reports a concrete failure rather than an empty one.
	got = pickEndpoint([]string{"unix:///a.sock", "unix:///b.sock"}, "/var/run/docker.sock")
	if got != "unix:///a.sock" {
		t.Fatalf("got %q", got)
	}
	if got := pickEndpoint(nil, "/var/run/docker.sock"); got != "/var/run/docker.sock" {
		t.Fatalf("got %q", got)
	}
}
