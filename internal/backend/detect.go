package backend

// Autodetection for container sockets.
//
// The ordering in here encodes a policy decision rather than a preference:
// Zoomies looks for a rootless daemon first and only falls back to the system
// one. A rootless socket means a compromised job gets the agent's user account,
// not the machine.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// hostEnv is the small slice of the world autodetection depends on, injected so
// the ordering can be tested without a container runtime on the machine.
type hostEnv struct {
	getenv func(string) string
	uid    int
	// exists reports whether a path is present on the host.
	exists func(string) bool
}

func realHostEnv() hostEnv {
	return hostEnv{
		getenv: os.Getenv,
		uid:    os.Geteuid(),
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
	}
}

// DetectDockerHost returns Docker endpoints to try, most preferred first.
//
// DOCKER_HOST always comes first when set, because an operator who configured
// it must see the error from their own choice rather than a silent fallback.
// After that come the rootless sockets, then the system socket.
//
// Only endpoints that exist are returned. When none do, the full candidate list
// comes back anyway so that the caller can name what it looked for.
func DetectDockerHost() []string { return detectHost(realHostEnv(), dockerCandidates) }

// DetectPodmanHost returns Podman endpoints to try, most preferred first.
// Podman is rootless by default, so its user socket is the normal case and the
// system socket the exception.
func DetectPodmanHost() []string { return detectHost(realHostEnv(), podmanCandidates) }

func detectHost(e hostEnv, candidates func(hostEnv) []string) []string {
	all := candidates(e)
	var live []string
	for _, c := range all {
		// A non-unix endpoint cannot be stat'd, and an explicitly configured one
		// is never filtered out.
		if !strings.HasPrefix(c, "unix://") || e.exists(strings.TrimPrefix(c, "unix://")) {
			live = append(live, c)
		}
	}
	if len(live) > 0 {
		return live
	}
	return all
}

func dockerCandidates(e hostEnv) []string {
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, have := range out {
			if have == s {
				return
			}
		}
		out = append(out, s)
	}

	add(strings.TrimSpace(e.getenv("DOCKER_HOST")))
	if xdg := strings.TrimSpace(e.getenv("XDG_RUNTIME_DIR")); xdg != "" {
		add("unix://" + filepath.Join(xdg, "docker.sock"))
	}
	add("unix://" + filepath.Join("/run/user", strconv.Itoa(e.uid), "docker.sock"))
	if home := strings.TrimSpace(e.getenv("HOME")); home != "" {
		// Docker Desktop on macOS puts a user-owned socket here.
		add("unix://" + filepath.Join(home, ".docker/run/docker.sock"))
	}
	add("unix:///var/run/docker.sock")
	return out
}

func podmanCandidates(e hostEnv) []string {
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, have := range out {
			if have == s {
				return
			}
		}
		out = append(out, s)
	}

	add(strings.TrimSpace(e.getenv("CONTAINER_HOST")))
	if xdg := strings.TrimSpace(e.getenv("XDG_RUNTIME_DIR")); xdg != "" {
		add("unix://" + filepath.Join(xdg, "podman/podman.sock"))
	}
	add("unix://" + filepath.Join("/run/user", strconv.Itoa(e.uid), "podman/podman.sock"))
	add("unix:///run/podman/podman.sock")
	return out
}

// IsRootless reports whether a daemon described by info runs without root.
//
// Docker advertises it in SecurityOptions as "name=rootless"; Podman's
// compatibility endpoint sets the Rootless field. A root directory under
// /run/user or in a home directory is the third tell, for daemons that report
// neither.
func IsRootless(info SystemInfo) bool {
	if info.Rootless {
		return true
	}
	for _, o := range info.SecurityOptions {
		if strings.Contains(strings.ToLower(o), "rootless") {
			return true
		}
	}
	return IsRootlessEndpoint(info.DockerRootDir)
}

// IsRootlessEndpoint reports whether a socket path or data directory belongs to
// a per-user daemon. It is the check that works before /info has been read.
func IsRootlessEndpoint(path string) bool {
	p := strings.TrimPrefix(path, "unix://")
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/run/user/") || strings.HasPrefix(p, "/var/run/user/") {
		return true
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	return home != "" && home != "/" && home != "/root" && strings.HasPrefix(p, home+string(filepath.Separator))
}

// HasSystemd reports whether this host is running systemd, which decides
// whether the installer can offer to write a unit file.
func HasSystemd() bool {
	fi, err := os.Stat("/run/systemd/system")
	return err == nil && fi.IsDir()
}

// CanUseDockerSocket explains, in terms the operator can act on, why a socket
// cannot be used. It returns nil when the socket accepts a connection.
//
// This exists because the three failures look identical in a bare dial error
// and have three completely different remedies.
func CanUseDockerSocket(path string) error {
	p := strings.TrimSpace(path)
	switch {
	case p == "":
		return errors.New("backend: no Docker socket configured; set agent.docker_host or install Docker")
	case strings.HasPrefix(p, "tcp://"), strings.HasPrefix(p, "http://"), strings.HasPrefix(p, "https://"):
		// A TCP endpoint has nothing to stat; reachability is the Ping's job.
		return nil
	}
	p = strings.TrimPrefix(p, "unix://")

	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: no socket at %s; install Docker or Podman, start it (systemctl --user start docker, or systemctl start docker), or point agent.docker_host at the right socket", ErrUnavailable, p)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: cannot look at %s: permission denied; add your user to the docker group (sudo usermod -aG docker $USER, then log in again) or use the rootless socket at /run/user/%d/docker.sock", ErrUnavailable, p, os.Geteuid())
	case err != nil:
		return fmt.Errorf("%w: cannot look at %s: %w", ErrUnavailable, p, err)
	case fi.Mode()&fs.ModeSocket == 0:
		return fmt.Errorf("%w: %s is not a socket; agent.docker_host must point at a Docker or Podman API socket", ErrUnavailable, p)
	}

	conn, err := net.DialTimeout("unix", p, dialTimeout)
	if err == nil {
		_ = conn.Close()
		return nil
	}
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM), errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: permission denied on %s; add your user to the docker group (sudo usermod -aG docker $USER, then log in again), or run a rootless daemon and use /run/user/%d/docker.sock", ErrUnavailable, p, os.Geteuid())
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("%w: %s exists but nothing is listening; start the daemon with systemctl --user start docker (rootless), systemctl start docker, or systemctl --user start podman.socket", ErrUnavailable, p)
	default:
		return fmt.Errorf("%w: cannot connect to %s: %w", ErrUnavailable, p, err)
	}
}

// pickEndpoint chooses the first candidate that answers a connection, falling
// back to the first candidate so that a later Probe reports a concrete failure
// against a concrete socket rather than against nothing at all.
func pickEndpoint(candidates []string, fallback string) string {
	for _, c := range candidates {
		if CanUseDockerSocket(c) == nil {
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return fallback
}
