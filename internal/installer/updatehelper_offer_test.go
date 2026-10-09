//go:build unix

package installer

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

const addLater = "Add later: sudo zoomies updates helper install"

// offerHost is a systemd host with a native agent installed, as an upgrade sees
// it, and the update helper's paths under the same temporary folder.
type offerHost struct {
	*installHost
	opts    UpgradeOptions
	out     *bytes.Buffer
	systemd string
}

func newOfferHost(t *testing.T) *offerHost {
	t.Helper()
	h := &offerHost{installHost: newInstallHost(t, DeploymentNative), out: &bytes.Buffer{}, systemd: t.TempDir()}
	h.installHost.runner.answer = func(args []string) (string, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "LoadState"):
			return "loaded", nil
		case strings.Contains(line, "ExecStart"):
			return "{ path=" + h.binary + " ; argv[]=" + h.binary + " agent ; }", nil
		}
		return "", nil
	}
	shared := filepath.Join(h.base, "shared")
	if _, err := PrepareSharedDir(shared, -1, -1); err != nil {
		t.Fatal(err)
	}
	h.opts = UpgradeOptions{
		ConfigDir: h.configDir, Mode: ModeAgent, BinaryPath: h.binary, Out: h.out,
		run:    h.runner.run,
		shared: &sharedTarget{dir: shared, uid: -1, gid: -1},
		helperHost: &upgradeHelperHost{
			systemdDir: h.systemd, unitDir: h.installHost.opts.unitDir, stateDir: h.installHost.opts.helperStateDir,
			resolve: func(string) (InstallHelperOptions, error) { return h.installHost.opts, nil },
		},
	}
	return h
}

func (h *offerHost) upgrade(t *testing.T) error {
	t.Helper()
	return Upgrade(context.Background(), h.opts)
}

// installed is whether the helper's units are on the host, as root would see.
func (h *offerHost) installed() bool {
	return exists(filepath.Join(h.installHost.opts.unitDir, UpdatePathUnit))
}

func (h *offerHost) ranHelperCommands() bool {
	return slices.ContainsFunc(h.runner.lines(), func(l string) bool {
		return strings.Contains(l, "daemon-reload") || strings.Contains(l, "zoomies-update")
	})
}

func (h *offerHost) restarted() bool {
	return slices.ContainsFunc(h.runner.lines(), func(l string) bool { return strings.HasPrefix(l, "systemctl restart ") })
}

// The helper is a grant of root, so it is a question of its own: the
// deployment's additions can be approved in advance with --yes, and this cannot.
func TestAnUpgradeAsksAboutTheHelperAsItsOwnQuestionThatDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(o *UpgradeOptions)
		answer    string
		installed bool
		asks      bool
		says      string
	}{
		{name: "enter", configure: func(o *UpgradeOptions) { o.Interactive = true }, answer: "\n", asks: true, says: addLater},
		{name: "end of input", configure: func(o *UpgradeOptions) { o.Interactive = true }, answer: "", asks: true, says: addLater},
		{name: "n", configure: func(o *UpgradeOptions) { o.Interactive = true }, answer: "n\n", asks: true, says: addLater},
		{name: "y", configure: func(o *UpgradeOptions) { o.Interactive = true }, answer: "y\n", installed: true, asks: true},
		{name: "yes", configure: func(o *UpgradeOptions) { o.Interactive = true }, answer: "yes\n", installed: true, asks: true},
		{name: "--yes alone", configure: func(o *UpgradeOptions) { o.AssumeYes = true }, says: addLater},
		{name: "--yes at the terminal and enter", configure: func(o *UpgradeOptions) { o.AssumeYes, o.Interactive = true, true }, answer: "\n", asks: true, says: addLater},
		{name: "--yes at the terminal and no", configure: func(o *UpgradeOptions) { o.AssumeYes, o.Interactive = true, true }, answer: "n\n", asks: true, says: addLater},
		{name: "--update-helper", configure: func(o *UpgradeOptions) { o.UpdateHelper = true }, installed: true},
		{name: "--update-helper without a terminal", configure: func(o *UpgradeOptions) { o.UpdateHelper, o.NonInteractive = true, true }, installed: true},
		{name: "unattended", configure: func(o *UpgradeOptions) { o.NonInteractive = true }, says: addLater},
		{name: "unattended with --yes", configure: func(o *UpgradeOptions) { o.NonInteractive, o.AssumeYes = true, true }, says: addLater},
		// The upgrade the helper itself runs: --non-interactive and never --yes.
		{name: "unattended with a terminal's answer waiting", configure: func(o *UpgradeOptions) { o.NonInteractive = true }, answer: "y\n", says: addLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHost(t)
			tc.configure(&h.opts)
			h.opts.In = strings.NewReader(tc.answer)

			// Declining is not a failure: the upgrade goes on without it.
			if err := h.upgrade(t); err != nil {
				t.Fatalf("%v\n%s", err, h.out)
			}
			out := h.out.String()
			if asked := strings.Contains(out, "[y/N]"); asked != tc.asks {
				t.Errorf("asked = %v, want %v:\n%s", asked, tc.asks, out)
			}
			if tc.says != "" && !strings.Contains(out, tc.says) {
				t.Errorf("output does not say %q:\n%s", tc.says, out)
			}
			// The question and the hint that follows a no each say what the helper is
			// for, once: a person declining is told what they declined.
			wantSaid := 0
			if tc.asks {
				wantSaid++
			}
			if tc.says == addLater {
				wantSaid++
			}
			if got := strings.Count(out, UpdateHelperExplained); got != wantSaid {
				t.Errorf("%q is said %d times, want %d:\n%s", UpdateHelperExplained, got, wantSaid, out)
			}
			if tc.asks {
				for _, want := range []string{"sudo zoomies updates helper remove", "as root", "validated request", "by writing a request"} {
					if !strings.Contains(out, want) {
						t.Errorf("the question does not say %q:\n%s", want, out)
					}
				}
				if strings.Contains(out, "You install it") {
					t.Errorf("the question claims who is installing it:\n%s", out)
				}
			}
			if got := h.installed(); got != tc.installed {
				t.Fatalf("installed = %v, want %v:\n%s", got, tc.installed, out)
			}
			if !tc.installed {
				h.installedNothing(t)
				if h.ranHelperCommands() {
					t.Errorf("a helper that was not added still ran %v", h.runner.lines())
				}
			} else if !h.runner.ran("systemctl", "daemon-reload") {
				t.Errorf("the units were written without a daemon-reload: %v", h.runner.lines())
			}
			if !h.restarted() {
				t.Errorf("the upgrade did not go on to restart the service: %v", h.runner.lines())
			}
		})
	}
}

// The question comes after the layout review and before anything is pulled or
// restarted, so a "no" or a refusal can never leave a half-upgraded host.
func TestTheHelperQuestionComesBeforeTheServiceIsRestarted(t *testing.T) {
	h := newOfferHost(t)
	h.opts.Interactive = true
	h.opts.In = strings.NewReader("n\n")
	askedFirst := false
	inner := h.runner.answer
	h.runner.answer = func(args []string) (string, error) {
		if len(args) > 0 && args[0] == "restart" {
			askedFirst = strings.Contains(h.out.String(), "[y/N]")
		}
		return inner(args)
	}
	if err := h.upgrade(t); err != nil {
		t.Fatal(err)
	}
	if !askedFirst {
		t.Errorf("the service was restarted before the helper question was asked:\n%s", h.out)
	}
}

// A host that systemd does not run cannot have the helper's units, and a
// question whose answer could only be a refusal is not asked.
func TestTheHelperIsOfferedOnlyOnASystemdHost(t *testing.T) {
	for _, flag := range []bool{false, true} {
		name := map[bool]string{false: "asked", true: "--update-helper"}[flag]
		t.Run(name, func(t *testing.T) {
			h := newOfferHost(t)
			h.opts.helperHost.systemdDir = filepath.Join(h.base, "no-systemd")
			h.opts.Interactive = true
			h.opts.UpdateHelper = flag
			h.opts.In = strings.NewReader("y\n")
			if err := h.upgrade(t); err != nil {
				t.Fatalf("%v\n%s", err, h.out)
			}
			out := h.out.String()
			if strings.Contains(out, "[y/N]") || strings.Contains(out, addLater) {
				t.Errorf("a host without systemd was offered the helper:\n%s", out)
			}
			if flag && !strings.Contains(out, "does not run systemd") {
				t.Errorf("--update-helper was ignored without a word:\n%s", out)
			}
			if h.installed() {
				t.Error("the helper's units were written on a host without systemd")
			}
			h.installedNothing(t)
			if h.ranHelperCommands() {
				t.Errorf("ran %v", h.runner.lines())
			}
		})
	}
}

// Installed is judged on what only root can write. The pointer beside the
// configuration is the service's to write, so a service could otherwise
// silence the question, or make root believe a helper was there.
func TestTheHelperIsNotOfferedAgainOnceInstalled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h *offerHost)
		asks  bool
	}{
		{name: "installed", setup: func(t *testing.T, h *offerHost) { h.install(t) }},
		{name: "only the path unit", setup: func(t *testing.T, h *offerHost) {
			writeOffer(t, filepath.Join(h.installHost.opts.unitDir, UpdatePathUnit))
		}},
		{name: "only the service unit", setup: func(t *testing.T, h *offerHost) {
			writeOffer(t, filepath.Join(h.installHost.opts.unitDir, UpdateServiceUnit))
		}},
		{name: "only root's folder", setup: func(t *testing.T, h *offerHost) {
			mustDo(t, os.MkdirAll(h.installHost.opts.helperStateDir, 0o700))
		}},
		{name: "only the service's pointer beside the configuration", asks: true, setup: func(t *testing.T, h *offerHost) {
			writeOffer(t, filepath.Join(h.configDir, channel.PointerFile))
		}},
	} {
		for _, flag := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "", true: " with --update-helper"}[flag], func(t *testing.T) {
				h := newOfferHost(t)
				tc.setup(t, h)
				before := len(h.runner.calls)
				h.opts.Interactive = true
				h.opts.UpdateHelper = flag
				h.opts.In = strings.NewReader("y\n")
				if err := h.upgrade(t); err != nil {
					t.Fatalf("%v\n%s", err, h.out)
				}
				if asked := strings.Contains(h.out.String(), "[y/N]"); asked != (tc.asks && !flag) {
					t.Errorf("asked = %v:\n%s", asked, h.out)
				}
				ran := strings.Join(h.runner.lines()[before:], "\n")
				if !tc.asks && (strings.Contains(ran, "daemon-reload") || strings.Contains(ran, "enable")) {
					t.Errorf("an installed helper was installed again:\n%s", ran)
				}
				if tc.asks && !h.installed() {
					t.Errorf("the pointer the service wrote kept the helper from being offered:\n%s", h.out)
				}
			})
		}
	}
}

func writeOffer(t *testing.T, path string) {
	t.Helper()
	mustDo(t, os.MkdirAll(filepath.Dir(path), 0o755))
	mustDo(t, os.WriteFile(path, []byte("x\n"), 0o644))
}

// What the upgrade's checks refuse is the helper's own sentence, and it is not
// the upgrade's failure: the host is upgraded and the helper can be added once
// the refusal is put right.
func TestAUpgradeWithTheHelperInstalledStillUpgradesWhenTheQuestionIsDeclined(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		h := newOfferHost(t)
		h.opts.Interactive = true
		h.opts.In = strings.NewReader("n\n")
		if err := h.upgrade(t); err != nil {
			t.Fatal(err)
		}
		if !h.restarted() || h.installed() {
			t.Errorf("restarted = %v, installed = %v", h.restarted(), h.installed())
		}
		h.installedNothing(t)
	})
	for _, mode := range []string{"asked", "--update-helper"} {
		t.Run("refused when "+mode, func(t *testing.T) {
			h := newOfferHost(t)
			// Anybody can write the folder above the binary, so the helper would
			// refuse to run it as root.
			mustDo(t, os.Chmod(filepath.Dir(h.binary), 0o777))
			h.opts.Interactive = true
			h.opts.UpdateHelper = mode == "--update-helper"
			h.opts.In = strings.NewReader("y\n")
			if err := h.upgrade(t); err != nil {
				t.Fatalf("a refused helper failed the upgrade: %v\n%s", err, h.out)
			}
			out := h.out.String()
			for _, want := range []string{"writable by the world", "The update helper was not added", "sudo zoomies updates helper install", "Upgrade complete"} {
				if !strings.Contains(out, want) {
					t.Errorf("output does not say %q:\n%s", want, out)
				}
			}
			if !h.restarted() {
				t.Errorf("the upgrade stopped at the refusal: %v", h.runner.lines())
			}
			h.installedNothing(t)
		})
	}
}

// The upgrade the helper runs is "upgrade --version <tag> --non-interactive
// --config-dir <dir>". Whatever it finds on the host, it must not add, change
// or remove anything of the helper's own, since it is the helper that is
// running and the service chose the tag.
func TestANonInteractiveUpgradeMakesNoChangeToTheHelpersUnitsOrFiles(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not installed", true: "installed"}[installed], func(t *testing.T) {
			h := newOfferHost(t)
			if installed {
				h.install(t)
			}
			paths := []string{h.installHost.opts.unitDir, h.installHost.opts.helperStateDir, h.folder(), filepath.Join(h.configDir, channel.PointerFile)}
			before := offerSnapshot(t, paths)
			calls := len(h.runner.calls)
			h.opts.NonInteractive = true
			h.opts.In = strings.NewReader("y\n")
			if err := h.upgrade(t); err != nil {
				t.Fatalf("%v\n%s", err, h.out)
			}
			if after := offerSnapshot(t, paths); !slices.Equal(before, after) {
				t.Errorf("the upgrade changed the helper's files:\nbefore %q\nafter  %q", before, after)
			}
			ran := strings.Join(h.runner.lines()[calls:], "\n")
			for _, banned := range []string{"daemon-reload", "enable", "disable", "zoomies-update"} {
				if strings.Contains(ran, banned) {
					t.Errorf("the upgrade ran %q against the helper:\n%s", banned, ran)
				}
			}
			if strings.Contains(h.out.String(), "[y/N]") {
				t.Errorf("an unattended upgrade asked:\n%s", h.out)
			}
		})
	}
}

// offerSnapshot is every file under the paths, with its mode and contents.
func offerSnapshot(t *testing.T, paths []string) []string {
	t.Helper()
	var out []string
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			body := ""
			if info.Mode().IsRegular() {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				body = string(b)
			}
			out = append(out, path+" "+info.Mode().String()+" "+body)
			return nil
		})
		mustDo(t, err)
	}
	return out
}

// --check is what install.sh runs before the binary is replaced. It changes
// nothing and asks nothing.
func TestAnUpgradeCheckNeverOffersTheHelper(t *testing.T) {
	h := newOfferHost(t)
	h.opts.Check, h.opts.Interactive, h.opts.UpdateHelper = true, true, true
	h.opts.In = strings.NewReader("y\n")
	if err := h.upgrade(t); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "[y/N]") || h.installed() || h.ranHelperCommands() {
		t.Errorf("a check touched the helper:\n%s\n%v", h.out, h.runner.lines())
	}
}

// A container deployment is offered the helper too: the gate is the deployment
// record, since a native install is the one that names a unit.
func TestAContainerDeploymentIsOfferedTheHelperToo(t *testing.T) {
	opts, rec := upgradeFixture(t, DeploymentCompose)
	host := newInstallHost(t, DeploymentCompose)
	host.opts.ConfigDir = rec.Directory
	var out bytes.Buffer
	pulledAfterTheQuestion := false
	host.runner.answer = func(args []string) (string, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "config --images"):
			return opts.Image, nil
		case len(args) > 1 && args[0] == "inspect" && args[1] == "--type":
			return host.stateDir, nil
		case len(args) > 0 && args[0] == "inspect":
			return "true", nil
		case strings.Contains(line, " pull "):
			pulledAfterTheQuestion = pulledAfterTheQuestion || strings.Contains(out.String(), "[y/N]")
		}
		return "", nil
	}
	opts.Out, opts.Interactive, opts.In, opts.run = &out, true, strings.NewReader("y\n"), host.runner.run
	opts.helperHost = &upgradeHelperHost{
		systemdDir: t.TempDir(), unitDir: host.opts.unitDir, stateDir: host.opts.helperStateDir,
		resolve: func(string) (InstallHelperOptions, error) { return host.opts, nil },
	}
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "[y/N]") || !exists(filepath.Join(host.opts.unitDir, UpdatePathUnit)) {
		t.Errorf("a container deployment was not offered the helper:\n%s", &out)
	}
	if !pulledAfterTheQuestion {
		t.Errorf("the image was pulled before the helper question was asked:\n%s", &out)
	}
}

// A controller container with no embedded agent mounts no shared folder, so the
// helper could only refuse it. Init has never asked it; the upgrade used to ask
// and then print the refusal, which left an operator answering yes to nothing.
// Asked for by name it still goes ahead, and gets the refusal in the helper's words.
func TestAControllerContainerThatRunsNoRunnersIsNotOfferedTheHelper(t *testing.T) {
	for _, tc := range []struct {
		name          string
		requested     bool
		wantQuestion  bool
		wantInstalled bool
	}{
		{name: "unasked", wantQuestion: false, wantInstalled: false},
		{name: "asked for by name", requested: true, wantInstalled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, rec := upgradeFixture(t, DeploymentCompose)
			rec.Mode = ModeController
			if _, err := WriteDeploymentRecord(rec.Directory, rec); err != nil {
				t.Fatal(err)
			}
			env, err := os.OpenFile(rec.EnvFile, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.WriteString("ZOOMIES_AGENT_EMBEDDED=false\n"); err != nil {
				t.Fatal(err)
			}
			_ = env.Close()
			opts.Mode = ModeController

			host := newInstallHost(t, DeploymentCompose)
			host.opts.ConfigDir = rec.Directory
			var out bytes.Buffer
			host.runner.answer = func(args []string) (string, error) {
				line := strings.Join(args, " ")
				switch {
				case strings.Contains(line, "config --images"):
					return opts.Image, nil
				case len(args) > 1 && args[0] == "inspect" && args[1] == "--type":
					return host.stateDir, nil
				case len(args) > 0 && args[0] == "inspect":
					return "true", nil
				}
				return "", nil
			}
			in := strings.NewReader("y\n")
			opts.Out, opts.Interactive, opts.In, opts.run = &out, true, in, host.runner.run
			// --yes answers the deployment's own questions, so that the only
			// thing left to read the input is the helper's.
			opts.AssumeYes, opts.UpdateHelper = true, tc.requested
			opts.helperHost = &upgradeHelperHost{
				systemdDir: t.TempDir(), unitDir: host.opts.unitDir, stateDir: host.opts.helperStateDir,
				resolve: func(string) (InstallHelperOptions, error) { return host.opts, nil },
			}
			if err := Upgrade(context.Background(), opts); err != nil {
				t.Fatalf("%v\n%s", err, &out)
			}
			if asked := strings.Contains(out.String(), "[y/N]") || in.Len() != len("y\n"); asked != tc.wantQuestion {
				t.Errorf("asked = %v, want %v:\n%s", asked, tc.wantQuestion, &out)
			}
			if got := exists(filepath.Join(host.opts.unitDir, UpdatePathUnit)); got != tc.wantInstalled {
				t.Errorf("helper installed = %v, want %v:\n%s", got, tc.wantInstalled, &out)
			}
			if !tc.requested && strings.Contains(out.String(), "update helper") {
				t.Errorf("an unasked upgrade of a controller with no runners spoke of the helper:\n%s", &out)
			}
		})
	}
}

// A host the helper has nothing to serve (a service that runs as root, no
// zoomies unit) is not asked and not told; one where something is wrong that the
// operator can put right is told, at a terminal, in the helper's own sentence.
func TestTheHelperIsNotOfferedWhereItCannotBeResolved(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		interactive bool
		says        string
	}{
		{name: "runs as root", err: helperNotApplicable{errors.New("runs zoomies as root")}, interactive: true},
		{name: "no zoomies unit", err: helperNotApplicable{errors.New("there is no zoomies.service here")}, interactive: true},
		{name: "unreadable unit at a terminal", err: errors.New("cannot read zoomies.service: permission denied"), interactive: true, says: "cannot read zoomies.service: permission denied"},
		{name: "unreadable unit unattended", err: errors.New("cannot read zoomies.service: permission denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHost(t)
			h.opts.helperHost.resolve = func(string) (InstallHelperOptions, error) { return InstallHelperOptions{}, tc.err }
			h.opts.Interactive = tc.interactive
			h.opts.NonInteractive = !tc.interactive
			h.opts.In = strings.NewReader("y\n")
			if err := h.upgrade(t); err != nil {
				t.Fatal(err)
			}
			out := h.out.String()
			if strings.Contains(out, "[y/N]") || strings.Contains(out, addLater) || h.installed() {
				t.Errorf("asked about a helper that cannot be installed:\n%s", out)
			}
			if tc.says == "" && strings.Contains(out, "update helper") {
				t.Errorf("said something about a helper this host has no use for:\n%s", out)
			}
			if tc.says != "" && !strings.Contains(out, tc.says) {
				t.Errorf("did not pass on the helper's sentence %q:\n%s", tc.says, out)
			}
		})
	}
}

// ResolveHelperInstall marks the hosts that have nothing for the helper to
// serve, so that the upgrade can stay quiet about them and speak about the rest.
func TestResolvingTheHelperSaysWhichHostsHaveNothingForItToServe(t *testing.T) {
	unitDir := t.TempDir()
	lookup := func(name string) (*user.User, error) {
		if name == "zoomies" {
			return &user.User{Uid: "4242", Username: name}, nil
		}
		return nil, errors.New("unknown user " + name)
	}
	write := func(unit, body string) {
		t.Helper()
		mustDo(t, os.WriteFile(filepath.Join(unitDir, unit+".service"), []byte(body), 0o644))
	}
	resolve := func() error {
		_, err := resolveHelperInstall(t.TempDir(), unitDir, lookup, "/usr/local/bin/zoomies")
		return err
	}
	var na helperNotApplicable
	if err := resolve(); !errors.As(err, &na) {
		t.Errorf("no unit at all: %v, want one marked as nothing to serve", err)
	}
	write(UnitController, "[Service]\nUser=root\nWorkingDirectory=/var/lib/zoomies\nExecStart=/usr/local/bin/zoomies controller\n")
	if err := resolve(); !errors.As(err, &na) {
		t.Errorf("a service that runs as root: %v, want one marked as nothing to serve", err)
	}
	write(UnitController, "[Service]\nUser=ghost\nWorkingDirectory=/var/lib/zoomies\nExecStart=/usr/local/bin/zoomies controller\n")
	if err := resolve(); err == nil || errors.As(err, &na) {
		t.Errorf("a missing account: %v, want a refusal the operator can act on", err)
	}
	write(UnitController, "[Service]\nUser=zoomies\nExecStart=/usr/local/bin/zoomies controller\n")
	if err := resolve(); err == nil || errors.As(err, &na) {
		t.Errorf("no WorkingDirectory: %v, want a refusal the operator can act on", err)
	}
}

// What is printed to add the helper later names the deployment it was run for
// when that is not the default one, because a bare command would look for the
// wrong configuration.
func TestTheCommandToAddTheHelperLaterNamesANonDefaultConfigDir(t *testing.T) {
	for _, tc := range []struct{ name, dir, want string }{
		{"default", config.ConfigDir(), "sudo zoomies updates helper install"},
		{"non-default", "/srv/zoomies/etc", "sudo zoomies updates helper install --config-dir /srv/zoomies/etc"},
		{"with a space", "/srv/my zoomies", `sudo zoomies updates helper install --config-dir "/srv/my zoomies"`},
	} {
		if got := helperInstallCommand(tc.dir); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	h := newOfferHost(t)
	h.opts.NonInteractive = true
	if err := h.upgrade(t); err != nil {
		t.Fatal(err)
	}
	if want := addLater + " --config-dir " + h.configDir; !strings.Contains(h.out.String(), want) {
		t.Errorf("the upgrade did not say %q:\n%s", want, h.out)
	}
}

// Only a service whose deployment the helper can update is offered it: not a
// private provider's own unit, which a zoomies unit on the same host would
// otherwise stand in for, and not launchd.
func TestTheHelperIsNotOfferedForAProviderOnlyUnitOrLaunchd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		units   []string
		launchd bool
	}{
		{name: "a provider-only unit", units: []string{"zoomies-proxmox-ab12.service"}},
		{name: "launchd", units: []string{UnitAgent}, launchd: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHost(t)
			var out bytes.Buffer
			p := &upgradePlan{
				opts: UpgradeOptions{ConfigDir: h.configDir, Out: &out, Interactive: true, UpdateHelper: true,
					In: strings.NewReader("y\n"), run: h.runner.run, helperHost: h.opts.helperHost},
				nativeUnits: tc.units, unit: tc.units[0], launchd: tc.launchd,
			}
			p.offerUpdateHelper(context.Background())
			if out.Len() != 0 || h.installed() || h.ranHelperCommands() {
				t.Errorf("offered the helper:\n%s\n%v", &out, h.runner.lines())
			}
		})
	}
}

// A container that does not mount the shared folder yet is about to get the
// mount from the restart this upgrade is about to do, and the helper install
// would refuse it now. So the question is not asked, and the install is not
// tried, in the same run that adds the mount; the operator is told to add the
// helper afterwards.
func TestTheHelperIsNotOfferedInTheRunThatAddsTheSharedMount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(o *UpgradeOptions)
		answers   string
		says      string
	}{
		{name: "approved at the terminal", configure: func(o *UpgradeOptions) { o.Interactive = true }, answers: "y\ny\n", says: "which the restart below adds"},
		{name: "--update-helper", configure: func(o *UpgradeOptions) { o.AssumeYes, o.UpdateHelper = true, true }, says: "which the restart below adds"},
		{name: "only reported", configure: func(o *UpgradeOptions) { o.NonInteractive = true }, says: "zoomies upgrade --yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, rec := upgradeFixture(t, DeploymentCompose)
			withoutSharedMount(t, &opts, rec)
			host := newInstallHost(t, DeploymentCompose)
			host.opts.ConfigDir = rec.Directory
			var out bytes.Buffer
			host.runner.answer = func(args []string) (string, error) {
				line := strings.Join(args, " ")
				switch {
				case strings.Contains(line, "config --images"):
					return opts.Image, nil
				case len(args) > 1 && args[0] == "inspect" && args[1] == "--type":
					// The running container has no shared mount yet.
					return "", nil
				case len(args) > 0 && args[0] == "inspect":
					return "true", nil
				}
				return "", nil
			}
			in := strings.NewReader(tc.answers)
			opts.Out, opts.In, opts.run = &out, in, host.runner.run
			tc.configure(&opts)
			opts.helperHost = &upgradeHelperHost{
				systemdDir: t.TempDir(), unitDir: host.opts.unitDir, stateDir: host.opts.helperStateDir,
				resolve: func(string) (InstallHelperOptions, error) { return host.opts, nil },
			}
			if err := Upgrade(context.Background(), opts); err != nil {
				t.Fatalf("%v\n%s", err, &out)
			}
			text := out.String()
			// The layout question took its "y\n"; the rest was not read.
			unread := 0
			if tc.answers != "" {
				unread = len(tc.answers) - len("y\n")
			}
			if strings.Contains(text, "[y/N]") || in.Len() != unread {
				t.Errorf("asked about the helper (%d bytes of the answers left, want %d):\n%s", in.Len(), unread, text)
			}
			if !strings.Contains(text, "shared folder") || !strings.Contains(text, tc.says) || !strings.Contains(text, "sudo zoomies updates helper install") {
				t.Errorf("did not say why the helper waits and how to add it:\n%s", text)
			}
			if review, said := strings.Index(text, "Deployment additions to review"), strings.Index(text, "update helper"); review < 0 || said < review {
				t.Errorf("the helper was spoken of before the deployment's additions were reviewed:\n%s", text)
			}
			if exists(filepath.Join(host.opts.unitDir, UpdatePathUnit)) || host.runner.ran("systemctl", "daemon-reload") {
				t.Errorf("tried to install the helper: %v", host.runner.lines())
			}
			if !slices.ContainsFunc(host.runner.lines(), func(l string) bool { return strings.Contains(l, " up ") }) {
				t.Errorf("the upgrade did not go on: %v", host.runner.lines())
			}
		})
	}
}
