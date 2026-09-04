package installer

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func sock(mode fs.FileMode, uid, gid int) socketFacts {
	return socketFacts{uid: uid, gid: gid, mode: fs.ModeSocket | mode}
}

func TestCanOpenFollowsTheKernelsRule(t *testing.T) {
	cases := []struct {
		name string
		s    socketFacts
		a    account
		want bool
	}{
		{"owner may", sock(0o600, 1001, 999), account{uid: 1001}, true},
		{"group member may", sock(0o660, 0, 999), account{uid: 1001, groups: []int{999}}, true},
		{"non-member may not", sock(0o660, 0, 999), account{uid: 1001, groups: []int{1001}}, false},
		// The rule is first match wins, not most permissive: an owner with no
		// owner bits is refused even when the group would have let them in.
		{"owner bits win over group bits", sock(0o060, 1001, 999), account{uid: 1001, groups: []int{999}}, false},
		{"world-writable socket", sock(0o666, 0, 0), account{uid: 1001}, true},
		{"root is exempt", sock(0o600, 0, 0), account{uid: 0}, true},
		// Read without write is not enough to drive a daemon.
		{"read-only group", sock(0o640, 0, 999), account{uid: 1001, groups: []int{999}}, false},
	}
	for _, c := range cases {
		if got := canOpen(c.s, c.a); got != c.want {
			t.Errorf("%s: canOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestJoinableOnlyWhenTheGroupWouldHelp(t *testing.T) {
	// The case the installer exists to catch: a mode with no group bits, where
	// usermod would succeed and change nothing.
	if joinable(sock(0o600, 0, 999), account{uid: 1001}) {
		t.Error("a socket with no group permissions is not fixed by joining its group")
	}
	if !joinable(sock(0o660, 0, 999), account{uid: 1001}) {
		t.Error("a group-writable socket is exactly what joining the group fixes")
	}
	// Already a member: whatever is wrong, it is not the membership.
	if joinable(sock(0o660, 0, 999), account{uid: 1001, groups: []int{999}}) {
		t.Error("an account already in the group has nothing to join")
	}
}

func TestSocketPathOfIgnoresEndpointsWithNoFile(t *testing.T) {
	cases := map[string]string{
		"unix:///var/run/docker.sock": "/var/run/docker.sock",
		"/var/run/docker.sock":        "/var/run/docker.sock",
		"tcp://10.0.0.5:2375":         "",
		"https://docker.example:2376": "",
		"":                            "",
		"  ":                          "",
	}
	for in, want := range cases {
		if got := SocketPathOf(in); got != want {
			t.Errorf("SocketPathOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The group to join comes from the socket, not from the name "docker": a Podman
// API socket, or a distribution that names the group something else, gave the
// old code the wrong gid and an install that looked like it had worked.
func TestSocketGroupGIDFallsBackWhenThereIsNoSocket(t *testing.T) {
	if got := SocketGroupGID("tcp://10.0.0.5:2375"); got != dockerGroupGID() {
		t.Errorf("SocketGroupGID = %d, want the docker group %d for an endpoint with no file", got, dockerGroupGID())
	}
}

// ensureSocketAccess reports rather than guesses. These drive the two branches
// that need no root and no second account on the machine: a socket that is not
// there yet, and an account that does not exist.
func TestEnsureSocketAccessOnAHostWithNoSocketYet(t *testing.T) {
	out := &bytes.Buffer{}
	i := &Installer{ui: newUI(out)}
	p := &Plan{Backend: store.BackendDocker, DockerHost: "unix://" + filepath.Join(t.TempDir(), "absent.sock"), ServiceUser: "zoomies"}

	i.ensureSocketAccess(context.Background(), p)

	if !strings.Contains(out.String(), "re-probes") {
		t.Errorf("a daemon that is not up yet is not a failure: %s", out)
	}
}

func TestEnsureSocketAccessNamesTheCommandForAnAccountThatDoesNotExistYet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	defer l.Close()

	out := &bytes.Buffer{}
	i := &Installer{ui: newUI(out)}
	p := &Plan{Backend: store.BackendDocker, DockerHost: "unix://" + path, ServiceUser: "no-such-account-here"}

	i.ensureSocketAccess(context.Background(), p)

	if !strings.Contains(out.String(), "usermod -aG") || !strings.Contains(out.String(), "no-such-account-here") {
		t.Errorf("want the command the operator will need, got: %s", out)
	}
	if p.DockerGID == 0 && os.Geteuid() != 0 {
		t.Errorf("the socket's own group must be recorded for the unit to name: %+v", p)
	}
}

// The process backend runs no containers, so nothing here applies to it.
func TestEnsureSocketAccessSkipsTheProcessBackend(t *testing.T) {
	out := &bytes.Buffer{}
	i := &Installer{ui: newUI(out)}
	i.ensureSocketAccess(context.Background(), &Plan{
		Backend: store.BackendProcess, DockerHost: "unix:///var/run/docker.sock", ServiceUser: "zoomies",
	})
	if out.Len() != 0 {
		t.Errorf("a process-backend install has no socket to talk about: %s", out)
	}
}
