package backend

import (
	"io/fs"
	"strings"
	"testing"
)

// fakeIdentity is an agent on a host we invent: a service account, a socket
// owned by a group, and whichever of the two group lists the test needs.
func fakeIdentity(processGroups, userGroups []int, mode fs.FileMode, gid int, statOK bool) agentIdentity {
	return agentIdentity{
		uid:           65532,
		username:      "zoomies",
		processGroups: processGroups,
		userGroups:    userGroups,
		groupName: func(g int) string {
			if g == 998 {
				return "docker"
			}
			return ""
		},
		stat: func(string) (fs.FileMode, int, bool) {
			return mode, gid, statOK
		},
		unit: "zoomies-agent",
	}
}

const socket = "/var/run/docker.sock"

func TestDeniedDetailNamesTheAgentsOwnAccount(t *testing.T) {
	// The whole point: $USER would be the operator's login shell, and the agent
	// runs as somebody else entirely.
	id := fakeIdentity(nil, nil, fs.ModeSocket|0o660, 998, true)

	got := deniedDetail(id, socket)
	for _, want := range []string{
		"zoomies",
		"uid 65532",
		"group-owned by docker",
		"sudo usermod -aG docker zoomies",
		"systemctl restart zoomies-agent",
		"/run/user/65532/docker.sock",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "$USER") {
		t.Errorf("the command must be copyable, not templated: %s", got)
	}
}

func TestDeniedDetailSaysRestartWhenTheGroupIsAlreadyGranted(t *testing.T) {
	// usermod has been run, the user database agrees, and the running process
	// still predates the change. Running usermod again would do nothing.
	id := fakeIdentity(nil, []int{998}, fs.ModeSocket|0o660, 998, true)

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "is in the docker group") {
		t.Errorf("the membership must be acknowledged: %s", got)
	}
	if !strings.Contains(got, "systemctl restart zoomies-agent") {
		t.Errorf("the fix is a restart: %s", got)
	}
	if strings.Contains(got, "usermod") {
		t.Errorf("usermod would be a no-op here and must not be suggested: %s", got)
	}
}

func TestDeniedDetailLooksPastTheGroupWhenItIsHeld(t *testing.T) {
	// The process holds the group and was still refused, so the group is not
	// the problem and sending the operator back to usermod would waste a round.
	id := fakeIdentity([]int{998}, []int{998}, fs.ModeSocket|0o600, 998, true)

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "already holds group docker") {
		t.Errorf("the held group must be stated: %s", got)
	}
	if !strings.Contains(got, "0600") {
		t.Errorf("the mode is the evidence and must be shown: %s", got)
	}
	if strings.Contains(got, "usermod") {
		t.Errorf("usermod cannot help here: %s", got)
	}
}

func TestDeniedDetailFallsBackWhenTheSocketCannotBeExamined(t *testing.T) {
	// A directory above the socket that the agent may not traverse: nothing can
	// be read about the socket itself, and the advice still has to be usable.
	id := fakeIdentity(nil, nil, 0, 0, false)

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "docker group") || !strings.Contains(got, "usermod -aG docker zoomies") {
		t.Errorf("the usual cause must still be named: %s", got)
	}
	if !strings.Contains(got, "/run/user/65532/docker.sock") {
		t.Errorf("the rootless escape hatch must still be offered: %s", got)
	}
}

func TestDeniedDetailBlamesMandatoryAccessControlForRoot(t *testing.T) {
	// Root refused by DAC is not a thing, so a group is the wrong answer.
	id := fakeIdentity(nil, nil, 0, 0, false)
	id.uid = 0
	id.username = "root"

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "SELinux") && !strings.Contains(got, "AppArmor") {
		t.Errorf("root's denial must point at LSM policy: %s", got)
	}
	if strings.Contains(got, "usermod") {
		t.Errorf("no group can help root: %s", got)
	}
}

func TestDeniedDetailWithoutAKnownUnitDoesNotInventACommand(t *testing.T) {
	id := fakeIdentity(nil, []int{998}, fs.ModeSocket|0o660, 998, true)
	id.unit = ""

	got := deniedDetail(id, socket)
	if strings.Contains(got, "systemctl") {
		t.Errorf("a host with no systemd must not be told to use systemctl: %s", got)
	}
	if !strings.Contains(got, "Restart the agent") {
		t.Errorf("it must still say what to do: %s", got)
	}
}

func TestDeniedDetailWithoutAUsernameUsesTheUID(t *testing.T) {
	// A container image with no passwd entry for the uid it runs as, which is
	// how the distroless agent image is built.
	id := fakeIdentity(nil, nil, fs.ModeSocket|0o660, 998, true)
	id.username = ""

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "uid 65532") {
		t.Errorf("the uid must stand in for the missing name: %s", got)
	}
}

func TestRealIdentityDescribesThisProcess(t *testing.T) {
	// Not a claim about the machine, only that the fields are populated: an
	// identity with no groups at all would silently degrade every message.
	id := realIdentity()
	if id.stat == nil || id.groupName == nil {
		t.Fatal("realIdentity must be able to look at the host")
	}
	if len(id.processGroups) == 0 {
		t.Error("the process always has at least its own primary group")
	}
}

// A group the user database cannot name is still actionable: usermod takes a
// gid, and "the group that owns it" is not a command.
func TestDeniedDetailFallsBackToTheGidWhenTheGroupHasNoName(t *testing.T) {
	id := fakeIdentity(nil, nil, fs.ModeSocket|0o660, 4242, true)

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "sudo usermod -aG 4242 zoomies") {
		t.Errorf("want the numeric group in the command: %s", got)
	}
}

// The failure this whole diagnosis exists for, in the deployment it is most
// often hit in: the agent is a container, its account belongs to the image, and
// the host has no such user to add to anything. Telling an operator to run
// usermod there costs them a round trip and a `user does not exist`.
func TestDeniedDetailGivesContainerAdviceInAContainer(t *testing.T) {
	id := fakeIdentity(nil, nil, fs.ModeSocket|0o660, 987, true)
	id.username = "nonroot"
	id.containerized = true

	got := deniedDetail(id, socket)
	if strings.Contains(got, "usermod -aG 987 nonroot") {
		t.Errorf("a host usermod cannot reach a user inside the image: %s", got)
	}
	for _, want := range []string{
		"runs in a container",
		"group-owned by 987",
		"--group-add 987",
		`group_add: ["987"]`,
		"DOCKER_GID=987",
		"recreate the container",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

// A container that was never given the socket at all: the advice has to say how
// to find the gid, since it cannot be read from in there.
func TestDeniedDetailInAContainerWithNoSocketToLookAt(t *testing.T) {
	id := fakeIdentity(nil, nil, 0, 0, false)
	id.containerized = true

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "Mount the host's socket") || !strings.Contains(got, "stat -c '%g'") {
		t.Errorf("want the mount and the way to find the gid: %s", got)
	}
	if strings.Contains(got, "usermod") {
		t.Errorf("no usermod belongs in a container's advice: %s", got)
	}
}

// A container that already holds the group and is still refused is the mode
// case, not the group case, exactly as on a host.
func TestDeniedDetailInAContainerThatAlreadyHoldsTheGroup(t *testing.T) {
	id := fakeIdentity([]int{987}, nil, fs.ModeSocket|0o600, 987, true)
	id.containerized = true

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "already holds group 987") {
		t.Errorf("the held group must still win: %s", got)
	}
	if strings.Contains(got, "--group-add") {
		t.Errorf("adding a group it already has would be no advice at all: %s", got)
	}
}

// On a host, nothing changes.
func TestDeniedDetailOutsideAContainerKeepsTheUsermod(t *testing.T) {
	id := fakeIdentity(nil, nil, fs.ModeSocket|0o660, 998, true)

	got := deniedDetail(id, socket)
	if !strings.Contains(got, "sudo usermod -aG docker zoomies") {
		t.Errorf("a host install still gets the host fix: %s", got)
	}
	if strings.Contains(got, "--group-add") {
		t.Errorf("container advice must not leak onto a host: %s", got)
	}
}
