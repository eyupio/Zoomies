package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// Every cause the status names, with the words its sentence has to carry for a
// person to know which one it is.
var unsupportedCauses = []struct {
	name string
	// facts is this controller's machine with the cause; embedded is whether its
	// agent is on, which is what decides whether a container has a shared folder.
	facts    updates.HelperHost
	embedded bool
	// says is what the controller's sentence and refusal name.
	says string
	// command is the upgrade by hand the status offers to copy, and byHand what
	// the refusal says to do instead. Windows has no sudo, and zoomies upgrade
	// drives systemd or launchd and no Windows service, so it is given no
	// command, only what to do.
	command, byHand string
}{
	{"macOS", updates.HelperHost{GOOS: "darwin", Systemd: true}, true, "Linux", "sudo zoomies upgrade", "zoomies upgrade"},
	{"Windows", updates.HelperHost{GOOS: "windows"}, true, "Linux", "", "zoomies.exe"},
	{"a Linux host without systemd", updates.HelperHost{GOOS: "linux"}, true, "systemd", "sudo zoomies upgrade", "zoomies upgrade"},
	{"a controller-only container", updates.HelperHost{GOOS: "linux", InContainer: true}, false, "shared folder", "sudo zoomies upgrade", "zoomies upgrade"},
	{"a container under a rootless runtime", updates.HelperHost{GOOS: "linux", InContainer: true, RootlessRuntime: true}, true, "rootless", "sudo zoomies upgrade", "zoomies upgrade"},
}

// A controller the helper can never be installed beside is shown so, with the
// reason, and is never told to run a command that would only refuse. It is
// given the upgrade by hand that works there instead, and a missing helper is
// not a problem it can do anything about.
func TestAControllerThatCannotHaveTheHelperSaysWhyAndOffersNoInstall(t *testing.T) {
	for _, tc := range unsupportedCauses {
		t.Run(tc.name, func(t *testing.T) {
			withHelperHost(t, tc.facts)
			h := newHarness(t)
			h.c.live.Update(func(c *config.Config) { c.Agent.Embedded = tc.embedded })
			h.readyToUpdate()
			if err := os.Remove(filepath.Join(h.updateDir, channel.MarkerFile)); err != nil {
				t.Fatal(err)
			}
			h.pass(h.c)

			helper := h.status().Helper
			if helper.State != HelperUnsupported {
				t.Fatalf("the status says %+v, want unsupported", helper)
			}
			if helper.InstallCommand != "" || strings.Contains(helper.Reason, helperInstallCommand) {
				t.Errorf("the status still suggests installing the helper: %+v", helper)
			}
			if !strings.Contains(helper.Reason, tc.says) || helper.UpgradeCommand != tc.command {
				t.Errorf("the status says %+v, want a reason naming %q and the upgrade command %q", helper, tc.says, tc.command)
			}
			if tc.command == "" && (strings.Contains(helper.Reason, "command below") || !strings.Contains(helper.Reason, tc.byHand)) {
				t.Errorf("with no command to copy the status says %q, want what to do by hand and no command below", helper.Reason)
			}
			if strings.Contains(helper.Reason, h.updateDir) || strings.Contains(helper.Reason, "/") {
				t.Errorf("every role reads the reason, and it names a path: %q", helper.Reason)
			}

			_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
			assertRefusedWithNothingWritten(t, h, err, ErrUpdateHelperMissing)
			if strings.Contains(err.Error(), helperInstallCommand) || !strings.Contains(err.Error(), tc.says) ||
				!strings.Contains(err.Error(), tc.byHand) || (tc.command == "" && strings.Contains(err.Error(), "zoomies upgrade")) {
				t.Errorf("the refusal = %v, want the reason and %s, and no install command", err, tc.byHand)
			}

			if codes := h.problemCodes(); contains(codes, "controller.update_helper_missing") {
				t.Errorf("problems = %v, want no missing-helper problem for a host that cannot have one", codes)
			}
			if fix := h.problem(t, "controller.update_available").Fix; strings.Contains(fix, "Settings → Updates") {
				t.Errorf("the new-release notice sends the operator to a button that is not there: %q", fix)
			}
		})
	}
}

// Only a cause the installer is certain to refuse is shown as one. A machine
// the controller cannot see well enough to say, or one the helper could be
// installed on, keeps the command that installs it.
func TestAControllerTheHelperCouldBeInstalledBesideIsStillOfferedTheCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		facts    updates.HelperHost
		embedded bool
	}{
		{"a native systemd host", updates.HelperHost{GOOS: "linux", Systemd: true}, false},
		{"a container that runs runners", updates.HelperHost{GOOS: "linux", InContainer: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHelperHost(t, tc.facts)
			h := newHarness(t)
			h.c.live.Update(func(c *config.Config) { c.Agent.Embedded = tc.embedded })
			withVersion(t, "1.3.4")
			if helper := h.status().Helper; helper.State != HelperMissing || helper.InstallCommand != helperInstallCommand || helper.UpgradeCommand != "" {
				t.Errorf("the status says %+v, want missing with the install command", helper)
			}
		})
	}
}

// The marker is the installer's last step, so a helper that is there is ready
// whatever the controller worked out about its machine; only an operating
// system that cannot run it at all is certain enough to overrule it.
func TestAReadyHelperIsReadyWhateverTheControllerWorkedOut(t *testing.T) {
	withHelperHost(t, updates.HelperHost{GOOS: "linux", InContainer: true})
	h := newHarness(t)
	h.c.live.Update(func(c *config.Config) { c.Agent.Embedded = false })
	withVersion(t, "1.3.4")
	h.installHelper()
	if got := h.status().Helper.State; got != HelperReady {
		t.Errorf("a controller-only container with the helper's marker in its folder is %q, want ready", got)
	}
}

// beatSaying is one heartbeat from a host's agent that offers no self-update and
// says why its host cannot have the helper.
func (h *harness) beatSaying(host *store.Host, ver, unsupported string) {
	h.t.Helper()
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion, Version: ver, UpdateUnsupported: unsupported,
	}); err != nil {
		h.t.Fatalf("Heartbeat: %v", err)
	}
}

// A host the helper can never be installed on is shown so, with the reason, on
// its card and in its note, and nothing tells anyone to install it there. The
// command beneath the card is the way, and the note says so.
func TestAHostThatCannotHaveTheHelperSaysWhyAndOffersNoInstall(t *testing.T) {
	for _, tc := range []struct {
		name, word, says string
	}{
		{"no systemd", string(updates.HelperUnsupportedNoSystemd), "systemd"},
		{"rootless", string(updates.HelperUnsupportedRootless), "rootless"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.hostsCanUpdate()
			host := h.agentHost("vm-plain", "1.3.4")
			h.beatSaying(host, "1.3.4", tc.word)

			u := h.c.HostView(host).Update
			if u.State != HostUpdateUnsupported || u.CanUpdate || !strings.Contains(u.Reason, tc.says) {
				t.Fatalf("the card says %+v, want unsupported, not updatable, naming %q", u, tc.says)
			}
			if strings.Contains(u.Reason, "helper install") {
				t.Errorf("the card still suggests installing the helper: %q", u.Reason)
			}
			_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
			assertNothingSent(t, h, host, err, ErrUpdateHostCannotUpdate)
			if err.Error() != u.Reason {
				t.Errorf("the refusal = %q, want the card's own sentence %q", err, u.Reason)
			}

			p := h.problem(t, "host.update_unavailable")
			if p.Severity != config.SeverityInfo || p.TargetID != host.ID || !strings.Contains(p.Detail, tc.says) {
				t.Errorf("the note is %+v, want an info note about the host naming %q", p, tc.says)
			}
			if strings.Contains(p.Fix, "helper install") || !strings.Contains(p.Fix, "command on its card") {
				t.Errorf("the note's fix = %q, want the command on the card and no install", p.Fix)
			}
		})
	}
}

// An agent too old to say why still told the controller its operating system
// when it joined, and the helper never runs on any but Linux. A word nobody
// defined is read as nothing said, and so is an agent that stops saying it,
// which is what installing the helper there looks like.
func TestWhatTheControllerKnowsOfAHostsHelperFollowsTheHost(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()

	mac := &store.Host{Name: "mac-mini", Capacity: 2, Backends: store.StringSlice{"process"}, Labels: store.StringMap{},
		OS: "darwin", Arch: "arm64", Version: "1.3.4", LastHeartbeat: h.c.Now()}
	if err := h.st.CreateHost(h.ctx, mac); err != nil {
		t.Fatal(err)
	}
	if u := h.c.HostView(mac).Update; u.State != HostUpdateUnsupported || !strings.Contains(u.Reason, "Linux") {
		t.Errorf("a macOS host that said nothing shows %+v, want unsupported naming Linux", u)
	}

	host := h.agentHost("vm-said", "1.3.4")
	h.beatSaying(host, "1.3.4", "because I said so")
	if u := h.c.HostView(host).Update; u.State != HostUpdateNone || !strings.Contains(u.Reason, "helper install") {
		t.Errorf("a word nobody defined shows %+v, want the card as before, with the install command", u)
	}

	h.beatSaying(host, "1.3.4", string(updates.HelperUnsupportedNoSystemd))
	if u := h.c.HostView(host).Update; u.State != HostUpdateUnsupported {
		t.Fatalf("after the agent said why the card shows %+v", u)
	}
	h.agentBeat(host, "1.3.4", nil)
	offering, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u := h.c.HostView(offering).Update; u.State != HostUpdateNone || !u.CanUpdate {
		t.Errorf("once the agent offers to update itself the card shows %+v, want it updatable", u)
	}

	// A host on the controller's release has nothing to update, whatever its
	// helper: the card says so, not why it could not.
	current := h.agentHost("vm-current", "1.3.5")
	h.beatSaying(current, "1.3.5", string(updates.HelperUnsupportedNoSystemd))
	if u := h.c.HostView(current).Update; u.State != HostUpdateNone || !strings.Contains(u.Reason, "already runs") {
		t.Errorf("a host on the release shows %+v, want nothing to update", u)
	}
	if codes := h.problemCodes(); count(codes, "host.update_unavailable") != 1 {
		t.Errorf("problems = %v, want one note, for the macOS host", codes)
	}

	// A deleted host is forgotten with its row.
	h.beatSaying(host, "1.3.4", string(updates.HelperUnsupportedNoSystemd))
	h.c.forgetHostUpdates(host.ID)
	h.c.updates.hostMu.RLock()
	_, kept := h.c.updates.hostUnsupported[host.ID]
	h.c.updates.hostMu.RUnlock()
	if kept {
		t.Error("what an agent said of its helper outlived its host")
	}
}

func count(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}
