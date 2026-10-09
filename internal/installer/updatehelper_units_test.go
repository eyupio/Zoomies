//go:build unix

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/updates/channel"
)

// installUID is the account these tests install the helper for. Run as root,
// that is an account other than root's, which root may give a folder to; run as
// anybody else, it is that account, the only one it may give a folder to.
func installUID() int {
	if uid := os.Geteuid(); uid != 0 {
		return uid
	}
	return 4242
}

// installHost is a host as the installer sees it, under one temporary folder
// that stands for /: a binary two folders down, a state directory, a
// configuration directory, a unit directory and root's folder for the helper,
// with systemctl recorded instead of run.
type installHost struct {
	base, binary, stateDir, configDir string
	runner                            *recordingRunner
	out                               *bytes.Buffer
	opts                              InstallHelperOptions
}

func newInstallHost(t *testing.T, deployment Deployment) *installHost {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	mustDo(t, err)
	h := &installHost{
		base:      base,
		binary:    filepath.Join(base, "opt", "bin", "zoomies"),
		stateDir:  filepath.Join(base, "var", "lib", "zoomies"),
		configDir: filepath.Join(base, "etc", "zoomies"),
		runner:    &recordingRunner{},
		out:       &bytes.Buffer{},
	}
	if deployment.Containerised() {
		h.stateDir = filepath.Join(base, "var", "lib", "zoomies", "shared")
	}
	mustDo(t, os.MkdirAll(filepath.Dir(h.binary), 0o755))
	mustDo(t, os.WriteFile(h.binary, []byte("#!/bin/sh\n"), 0o755))
	mustDo(t, os.MkdirAll(h.stateDir, 0o750))
	mustDo(t, os.MkdirAll(h.configDir, 0o755))
	// Where the temporary folder is must not decide whether an install is
	// refused: a CI runner's is under /home. The tests of that refusal say which
	// prefixes are protected.
	protectHomes(t)
	h.opts = InstallHelperOptions{
		Deployment: deployment, StateDir: h.stateDir, ConfigDir: h.configDir, BinaryPath: h.binary,
		ServiceUID: installUID(), Account: "zoomies", Out: h.out,
		unitDir:        filepath.Join(base, "etc", "systemd", "system"),
		helperStateDir: filepath.Join(base, "var", "lib", "zoomies-update"),
		binaryTop:      base,
		run:            h.runner.run,
		now:            func() time.Time { return t0 },
	}
	return h
}

// protectHomes sets the folders the helper's unit is taken to keep read-only
// for one test, and none by default.
func protectHomes(t *testing.T, prefixes ...string) {
	t.Helper()
	was := protectedHomes
	protectedHomes = prefixes
	t.Cleanup(func() { protectedHomes = was })
}

// The list is the unit's ProtectHome=read-only, which covers exactly these. A
// release that leaves it empty would let the install write a unit the upgrade
// then cannot use, and no other test would notice, since they set their own.
func TestTheFoldersTheInstallTreatsAsHomeDirectoriesAreTheUnitsOwn(t *testing.T) {
	if got, want := protectedHomes, []string{"/home", "/root", "/run/user"}; !slices.Equal(got, want) {
		t.Fatalf("protectedHomes = %v, want %v", got, want)
	}
	if err := checkUnitPath("/home/ada/zoomies"); err == nil {
		t.Error("a path under /home was accepted with the default list")
	}
}

func (h *installHost) folder() string { return filepath.Join(h.stateDir, "update") }

func (h *installHost) install(t *testing.T) {
	t.Helper()
	if err := InstallUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("InstallUpdateHelper: %v\n%s", err, h.out)
	}
}

// installedNothing fails the test if an install that was refused left anything
// behind: a refusal is only worth its sentence if nothing is half-installed.
func (h *installHost) installedNothing(t *testing.T) {
	t.Helper()
	for _, path := range []string{
		h.folder(), h.opts.helperStateDir, filepath.Join(h.configDir, channel.PointerFile),
		filepath.Join(h.opts.unitDir, UpdatePathUnit),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a refused install left %s behind (%v)", path, err)
		}
	}
	if slices.ContainsFunc(h.runner.lines(), func(l string) bool { return strings.Contains(l, "enable") }) {
		t.Errorf("a refused install enabled a unit: %v", h.runner.lines())
	}
}

// The units are the whole of what root runs on the service's say, so what they
// watch and what they start are pinned, and so is every limit the service could
// otherwise exploit. A % in a path is a systemd specifier unless doubled.
func TestTheRenderedUnitsWatchTheRealFolderAndCallTheRealBinary(t *testing.T) {
	pathUnit, serviceUnit := RenderUpdateUnits("/opt/zoo%mies/bin/zoomies", "/var/lib/zoo%mies/update")
	for _, want := range []string{
		"\nPathExists=/var/lib/zoo%%mies/update/request.json\n",
		"\nUnit=zoomies-update.service\n",
	} {
		if !strings.Contains(pathUnit, want) {
			t.Errorf("the path unit should have %q:\n%s", want, pathUnit)
		}
	}
	for _, want := range []string{
		"\nType=oneshot\n",
		"\nUser=root\n",
		"\nExecStart=/opt/zoo%%mies/bin/zoomies updates helper run\n",
		"\nTimeoutStartSec=2h\n",
		"\nTimeoutStopSec=2min\n",
		"\nKillMode=mixed\n",
		"\nStartLimitIntervalSec=10min\n",
		"\nStartLimitBurst=5\n",
		"\nProtectHome=read-only\n",
		"\nNoNewPrivileges=yes\n",
		"\nPrivateTmp=yes\n",
	} {
		if !strings.Contains(serviceUnit, want) {
			t.Errorf("the service unit should have %q:\n%s", want, serviceUnit)
		}
	}
	for _, unit := range []string{pathUnit, serviceUnit} {
		if strings.Contains(unit, "zoo%m") {
			t.Errorf("a %% in a path was left single:\n%s", unit)
		}
		for _, line := range strings.Split(unit, "\n") {
			// The engine inherits the unit's environment, so a file the service
			// can write must never become it; and the upgrade has to write
			// outside a home directory, which ProtectSystem would forbid.
			if strings.HasPrefix(line, "EnvironmentFile=") || strings.HasPrefix(line, "ProtectSystem=") {
				t.Errorf("the units must not have %q", line)
			}
		}
	}
}

func TestInstallingTheHelperWritesTheUnitsThePointerAndTheMarker(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)

	info, err := os.Lstat(h.folder())
	mustDo(t, err)
	if uid, _, _ := fileOwner(info); !info.IsDir() || uid != installUID() || info.Mode().Perm() != 0o750 {
		t.Errorf("the update folder is %v owned by uid %d, want a folder 0750 owned by uid %d", info.Mode(), uid, installUID())
	}

	want := channel.Pointer{V: 1, Dir: h.folder(), Binary: h.binary, Account: "zoomies", UID: installUID(), ConfigDir: h.configDir}
	for path, mode := range map[string]os.FileMode{
		filepath.Join(h.configDir, channel.PointerFile):           0o644,
		filepath.Join(h.opts.helperStateDir, channel.PointerFile): 0o600,
	} {
		p, err := channel.ReadPointer(path)
		if err != nil {
			t.Errorf("the pointer %s: %v", path, err)
			continue
		}
		if p != want {
			t.Errorf("%s = %+v, want %+v", path, p, want)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s has mode %v, want %v (%v)", path, info.Mode().Perm(), mode, err)
		}
	}

	if m, found, err := channel.ReadMarker(h.folder()); err != nil || !found || m.Binary != h.binary || !m.InstalledAt.Equal(t0) {
		t.Errorf("the marker = %+v, found %v, err %v", m, found, err)
	}

	pathUnit, serviceUnit := RenderUpdateUnits(h.binary, h.folder())
	for name, body := range map[string]string{UpdatePathUnit: pathUnit, UpdateServiceUnit: serviceUnit} {
		if got, err := os.ReadFile(filepath.Join(h.opts.unitDir, name)); err != nil || string(got) != body {
			t.Errorf("%s was not written as rendered (%v)", name, err)
		}
	}

	lines := h.runner.lines()
	reload, enable := slices.Index(lines, "systemctl daemon-reload"), slices.Index(lines, "systemctl enable --now zoomies-update.path")
	if reload < 0 || enable < 0 || reload > enable {
		t.Errorf("systemctl should be reloaded and then the path unit enabled and started: %v", lines)
	}

	// The folder is given away only by root, so the account here is another
	// one only when the tests run as root; otherwise this skips and says why.
	t.Run("the folder is given to another account", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("only root can give a folder to another account; run the tests as root to check the chown")
		}
		h := newInstallHost(t, DeploymentNative)
		h.opts.ServiceUID = 4242
		h.install(t)
		info, err := os.Stat(h.folder())
		mustDo(t, err)
		if uid, _, _ := fileOwner(info); uid != 4242 {
			t.Errorf("the update folder is owned by uid %d, want 4242", uid)
		}
	})

	// What the installer wrote is what the helper accepts, read the way the
	// helper reads it.
	opts, err := helperOptionsFromPointer(h.opts.helperStateDir, fileOwner, h.base)
	if err != nil {
		t.Fatalf("the helper refuses the pointer the installer wrote: %v", err)
	}
	folder, err := openUpdateFolder(opts.Dir, opts.ServiceUID, fileUID)
	if err != nil {
		t.Fatalf("the helper refuses the folder the installer made: %v", err)
	}
	folder.Close()
}

// The binary is recorded where it really is: the helper refuses a link above
// it, and on a host whose /bin is a link to /usr/bin that is every path through
// /bin.
func TestTheHelperIsInstalledForTheBinaryByItsRealPath(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	link := filepath.Join(h.base, "bin")
	mustDo(t, os.Symlink(filepath.Join("opt", "bin"), link))
	h.opts.BinaryPath = filepath.Join(link, "zoomies")
	h.install(t)
	p, err := channel.ReadPointer(filepath.Join(h.opts.helperStateDir, channel.PointerFile))
	mustDo(t, err)
	if p.Binary != h.binary {
		t.Errorf("the pointer names %s, want the real path %s", p.Binary, h.binary)
	}
}

// Installing is how a refused folder is put right, so it runs again over an
// installed helper; it must then change nothing, the marker's time included.
func TestInstallingAgainChangesNothing(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	before := snapshot(t, h.base)
	h.opts.now = func() time.Time { return t0.Add(time.Hour) }
	h.install(t)
	after := snapshot(t, h.base)
	if !slices.Equal(before, after) {
		t.Errorf("installing again changed the host:\nbefore %v\nafter  %v", before, after)
	}
}

// A folder already there, from an earlier install or made by hand, is given
// the owner and mode the helper expects rather than refused later.
func TestInstallingPutsRightAFolderAlreadyThere(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	mustDo(t, os.Mkdir(h.folder(), 0o700))
	mustDo(t, os.Chmod(h.folder(), 0o777))
	h.install(t)
	if info, err := os.Stat(h.folder()); err != nil || info.Mode().Perm() != 0o750 {
		t.Errorf("the update folder has mode %v, want 0750 (%v)", info.Mode().Perm(), err)
	}
}

// snapshot is every file and folder under base, with its mode and contents.
func snapshot(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	mustDo(t, filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		line := path + " " + info.Mode().String()
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			line += " " + string(body)
		}
		out = append(out, line)
		return nil
	}))
	return out
}

// A unit of that name the installer did not write, or one written for another
// binary, is the operator's to look at before anything replaces it.
func TestInstallRefusesToReplaceAUnitWithDifferentContents(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	mustDo(t, os.MkdirAll(h.opts.unitDir, 0o755))
	unit := filepath.Join(h.opts.unitDir, UpdateServiceUnit)
	mustDo(t, os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/true\n"), 0o644))
	err := InstallUpdateHelper(context.Background(), h.opts)
	if err == nil || !strings.Contains(err.Error(), "already exists with different contents") {
		t.Fatalf("want a refusal naming the unit, got: %v", err)
	}
	if body, _ := os.ReadFile(unit); string(body) != "[Service]\nExecStart=/bin/true\n" {
		t.Errorf("the unit was replaced: %q", body)
	}
	h.installedNothing(t)
}

// A refusal the helper would make later, as a request nobody answers and a
// timeout ninety minutes after, is made at install instead, in the helper's
// words, and nothing is left half-installed.
func TestInstallRefusesWhatTheHelperWouldRefuse(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, h *installHost)
		want   string
	}{
		{"a service that runs as root", func(t *testing.T, h *installHost) { h.opts.ServiceUID = 0 }, "zoomies runs as uid 0 here"},
		{"an account that is not a name", func(t *testing.T, h *installHost) { h.opts.Account = "zoomies\nroot" }, "not an account name"},
		{"a binary under a folder anyone can write", func(t *testing.T, h *installHost) {
			mustDo(t, os.Chmod(filepath.Join(h.base, "opt"), 0o777|os.ModeSticky))
		}, "opt is writable by the world"},
		{"a binary its group can write", func(t *testing.T, h *installHost) { mustDo(t, os.Chmod(h.binary, 0o775)) }, "writable by its group or the world"},
		{"a configuration directory that is not there", func(t *testing.T, h *installHost) {
			h.opts.ConfigDir = filepath.Join(h.base, "etc", "missing")
		}, "configuration directory"},
		{"a state directory others can write", func(t *testing.T, h *installHost) { mustDo(t, os.Chmod(h.stateDir, 0o770)) }, "writable by its group or the world"},
		{"a state directory with spaces", func(t *testing.T, h *installHost) {
			spaced := filepath.Join(h.base, "var", "lib", "zoo mies")
			mustDo(t, os.Mkdir(spaced, 0o750))
			h.opts.StateDir = spaced
		}, "systemd reads as something other than part of a path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newInstallHost(t, DeploymentNative)
			c.change(t, h)
			err := InstallUpdateHelper(context.Background(), h.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal saying %q, got: %v", c.want, err)
			}
			h.installedNothing(t)
		})
	}
}

// Something other than a folder where the update folder goes, here a link to a
// folder of root's, is refused and not followed: making the folder through it
// would give a folder of root's to the service.
func TestInstallRefusesSomethingElseWhereTheUpdateFolderGoes(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	mustDo(t, os.Symlink(h.opts.unitDir, h.folder()))
	err := InstallUpdateHelper(context.Background(), h.opts)
	if err == nil || !strings.Contains(err.Error(), "is a link and not a folder") {
		t.Fatalf("want a refusal of the link, got: %v", err)
	}
	if slices.ContainsFunc(h.runner.lines(), func(l string) bool { return strings.Contains(l, "enable") }) {
		t.Errorf("a refused install enabled a unit: %v", h.runner.lines())
	}
}

// The upgrade the helper runs cannot write a home directory under the unit's
// ProtectHome, and systemd reads some characters in a path as something else,
// so either is refused at install with what to do.
func TestTheHelperIsRefusedPathsItsUnitCouldNotUse(t *testing.T) {
	for path, want := range map[string]string{
		"/home/ada/zoomies/update":  "/home, which the helper's unit keeps read-only",
		"/root/zoomies":             "/root, which the helper's unit keeps read-only",
		"/run/user/1000/zoomies":    "/run/user, which the helper's unit keeps read-only",
		"/var/lib/zoo mies/update":  "has ' '",
		"/opt/$HOME/zoomies":        "has '$'",
		`/opt/zoo"mies/zoomies`:     `has '"'`,
		"/opt/zoo\x1bmies/zoomies":  "has '\\x1b'",
		"/var/lib/homework/zoomies": "",
		"/homework/zoomies":         "",
		"/var/lib/zoo%mies/update":  "",
	} {
		err := checkUnitPath(path)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s was refused: %v", path, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: want a refusal saying %q, got: %v", path, want, err)
		}
	}
}

// A link out of a home directory is a path in one, wherever the link is.
func TestTheHelperIsRefusedAPathThatALinkPutsInAHomeDirectory(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	mustDo(t, os.Mkdir(home, 0o755))
	protectHomes(t, home)
	link := filepath.Join(base, "state")
	mustDo(t, os.Symlink(home, link))
	err := checkUnitPath(filepath.Join(link, "zoomies-missing", "update"))
	if err == nil || !strings.Contains(err.Error(), "which the helper's unit keeps read-only") {
		t.Errorf("want a refusal of a path that is really in %s, got: %v", home, err)
	}
}

// A link whose target does not exist yet still leads into the home directory it
// names: the install would create the folder there.
func TestTheHelperIsRefusedAPathThatADanglingLinkPutsInAHomeDirectory(t *testing.T) {
	dir := t.TempDir()
	absolute, relative := filepath.Join(dir, "absolute"), filepath.Join(dir, "relative")
	mustDo(t, os.Symlink("/home/zoomies-nobody/update", absolute))
	mustDo(t, os.Symlink("../../../../../../../../../../../../home/zoomies-nobody/update", relative))
	for name, path := range map[string]string{"absolute": absolute, "relative": relative} {
		err := checkUnitPath(path)
		if err == nil || !strings.Contains(err.Error(), "which the helper's unit keeps read-only") {
			t.Errorf("%s: want a refusal of a path that is really in /home, got: %v", name, err)
		}
	}
}

// A loop of links with nothing at the end of it must end, not be followed for
// ever.
func TestResolvingALoopOfDanglingLinksEnds(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mustDo(t, os.Symlink(b, a))
	mustDo(t, os.Symlink(a, b))
	if got := resolvedPath(a); got == "" {
		t.Error("resolvedPath of a loop returned nothing")
	}
}

// Root's own folder for the helper is checked with everything else, so one the
// helper would refuse stops the install before the update folder is made.
func TestInstallRefusesRootsFolderForTheHelperBeforeWritingAnything(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	mustDo(t, os.Mkdir(h.opts.helperStateDir, 0o700))
	mustDo(t, os.Chmod(h.opts.helperStateDir, 0o777))
	err := InstallUpdateHelper(context.Background(), h.opts)
	if err == nil || !strings.Contains(err.Error(), "writable by its group or the world") {
		t.Fatalf("want a refusal of root's folder for the helper, got: %v", err)
	}
	for _, path := range []string{h.folder(), filepath.Join(h.configDir, channel.PointerFile), filepath.Join(h.opts.unitDir, UpdatePathUnit)} {
		if exists(path) {
			t.Errorf("a refused install wrote %s", path)
		}
	}
}

// A daemon that maps the container's uid to another on the host owns every
// request under that other uid, which the helper refuses; that is said at
// install and not found out from a request nobody answers.
func TestAContainerOnARemappingDaemonIsRefusedWithTheReason(t *testing.T) {
	for _, option := range []string{"name=userns", "name=rootless"} {
		h := newInstallHost(t, DeploymentCompose)
		writeContainerRecord(t, h)
		h.runner.answer = func(args []string) (string, error) {
			switch {
			case len(args) > 0 && args[0] == "inspect":
				return h.stateDir + "\n", nil
			case len(args) > 0 && args[0] == "info":
				return "[name=seccomp,profile=builtin " + option + "]\n", nil
			}
			return "", nil
		}
		err := InstallUpdateHelper(context.Background(), h.opts)
		if err == nil || !strings.Contains(err.Error(), "maps the container's account") {
			t.Errorf("%s: want a refusal giving the reason, got: %v", option, err)
		}
		h.installedNothing(t)
	}
}

func writeContainerRecord(t *testing.T, h *installHost) {
	t.Helper()
	if _, err := WriteDeploymentRecord(h.configDir, DeploymentRecord{Deployment: DeploymentCompose, Container: "zoomies", Directory: h.configDir}); err != nil {
		t.Fatal(err)
	}
}

// A container's controller finds <shared>/update by itself, so the folder is
// made there and no pointer is written beside the configuration; root's copy
// is still the helper's only source.
func TestAContainerInstallUsesTheSharedFolderAndWritesNoPointer(t *testing.T) {
	h := newInstallHost(t, DeploymentCompose)
	writeContainerRecord(t, h)
	h.runner.answer = func(args []string) (string, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return h.stateDir + "\n", nil
		}
		return "", nil
	}
	h.install(t)
	if info, err := os.Stat(h.folder()); err != nil || !info.IsDir() {
		t.Fatalf("the update folder was not made under the shared folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.configDir, channel.PointerFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a container install wrote a pointer beside the configuration (%v)", err)
	}
	p, err := channel.ReadPointer(filepath.Join(h.opts.helperStateDir, channel.PointerFile))
	if err != nil || p.Dir != h.folder() {
		t.Errorf("root's pointer = %+v (%v), want the folder %s", p, err, h.folder())
	}
	if !slices.ContainsFunc(h.runner.lines(), func(l string) bool {
		return strings.HasPrefix(l, "docker inspect") && strings.Contains(l, SharedHostDir) && strings.HasSuffix(l, " zoomies")
	}) {
		t.Errorf("the container was not asked what it mounts at %s: %v", SharedHostDir, h.runner.lines())
	}
}

// The upgrade the helper runs rewrites a container deployment's Compose file
// and environment file, and the unit keeps /home read-only, so a deployment that
// keeps either there would fail at the first request. That is refused at install.
func TestAContainerDeploymentKeptInAHomeDirectoryIsRefusedAtInstall(t *testing.T) {
	for _, name := range []string{"its directory", "its environment file", "a link out of a home path"} {
		t.Run(name, func(t *testing.T) {
			h := newInstallHost(t, DeploymentCompose)
			home := filepath.Join(h.base, "home")
			mustDo(t, os.Mkdir(home, 0o755))
			protectHomes(t, home)
			rec := DeploymentRecord{Deployment: DeploymentCompose, Container: "zoomies", Directory: h.configDir}
			switch name {
			case "its directory":
				rec.Directory = filepath.Join(home, "zoomies")
			case "its environment file":
				rec.EnvFile = filepath.Join(home, ".env")
			default:
				link := filepath.Join(h.base, "env-link")
				mustDo(t, os.Symlink(home, link))
				rec.EnvFile = filepath.Join(link, "zoomies.env")
			}
			if _, err := WriteDeploymentRecord(h.configDir, rec); err != nil {
				t.Fatal(err)
			}
			// The container does mount the shared folder, so only the paths can refuse it.
			h.runner.answer = func(args []string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return h.stateDir + "\n", nil
				}
				return "", nil
			}
			err := InstallUpdateHelper(context.Background(), h.opts)
			if err == nil || !strings.Contains(err.Error(), "which the helper's unit keeps read-only") {
				t.Fatalf("want a refusal of a deployment kept in a home directory, got: %v", err)
			}
			h.installedNothing(t)
		})
	}
}

// A controller-only container does not mount the shared folder, so a request
// written in it would never reach the host; it is refused with the reason
// before anything is made.
func TestAControllerOnlyContainerIsRefusedWithTheReason(t *testing.T) {
	h := newInstallHost(t, DeploymentCompose)
	writeContainerRecord(t, h)
	err := InstallUpdateHelper(context.Background(), h.opts)
	if err == nil || !strings.Contains(err.Error(), "does not mount the shared folder") || !strings.Contains(err.Error(), "controller-only") {
		t.Fatalf("want a refusal giving the reason, got: %v", err)
	}
	h.installedNothing(t)
}

func TestRemovingTheHelperStopsDisablesAndDeletesItsUnitsAndFiles(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	plant(t, filepath.Join(h.folder(), channel.ResultFile), `{"v":1}`)
	h.runner.calls = nil
	if err := RemoveUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RemoveUpdateHelper: %v", err)
	}
	for _, argv := range [][]string{
		{"systemctl", "disable", "--now", UpdatePathUnit},
		{"systemctl", "daemon-reload"},
	} {
		if !h.runner.ran(argv...) {
			t.Errorf("remove did not run %v: %v", argv, h.runner.lines())
		}
	}
	h.neverStoppedTheService(t)
	for _, path := range []string{
		filepath.Join(h.opts.unitDir, UpdatePathUnit), filepath.Join(h.opts.unitDir, UpdateServiceUnit),
		h.opts.helperStateDir, filepath.Join(h.configDir, channel.PointerFile), h.folder(),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("remove left %s (%v)", path, err)
		}
	}
	if !exists(h.stateDir) || !exists(h.binary) {
		t.Error("remove took the state directory or the binary, which are not the helper's")
	}
}

// Anything in the update folder that is not the helper's is not the helper's
// to delete, so the folder stays and the operator is told what is in it.
func TestRemovingTheHelperLeavesAFolderWithSomethingElseInItAndSaysSo(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	plant(t, filepath.Join(h.folder(), "notes.txt"), "mine")
	if err := RemoveUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RemoveUpdateHelper: %v", err)
	}
	if !exists(filepath.Join(h.folder(), "notes.txt")) || exists(filepath.Join(h.folder(), channel.MarkerFile)) {
		t.Error("remove should take the marker and leave the folder with what else is in it")
	}
	if !strings.Contains(h.out.String(), "still holds notes.txt") {
		t.Errorf("remove should say what it left:\n%s", h.out)
	}
}

// Root's folder for the helper is removed only when it is the folder the
// helper made; a link in its place is left, and said to be.
func TestRemovingTheHelperLeavesAStateFolderThatIsNotItsOwn(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	moved := h.opts.helperStateDir + "-real"
	mustDo(t, os.Rename(h.opts.helperStateDir, moved))
	mustDo(t, os.Symlink(moved, h.opts.helperStateDir))
	if err := RemoveUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RemoveUpdateHelper: %v", err)
	}
	if _, err := os.Lstat(h.opts.helperStateDir); err != nil {
		t.Errorf("the link in place of root's folder was removed: %v", err)
	}
	if !strings.Contains(h.out.String(), "is a link and not the helper's folder") {
		t.Errorf("remove should say what it left:\n%s", h.out)
	}
}

// neverStoppedTheService fails the test if zoomies-update.service was ever
// stopped: that kills the upgrade it is running and leaves upgrade.lock behind.
func (h *installHost) neverStoppedTheService(t *testing.T) {
	t.Helper()
	if slices.ContainsFunc(h.runner.calls, func(c []string) bool {
		return len(c) > 1 && c[0] == "systemctl" && c[1] == "stop" && slices.Contains(c, UpdateServiceUnit)
	}) {
		t.Errorf("remove stopped %s: %v", UpdateServiceUnit, h.runner.lines())
	}
}

// updateRunning makes systemctl say the helper is running an update.
func (h *installHost) updateRunning() {
	h.runner.answer = func(args []string) (string, error) {
		if slices.Equal(args, []string{"is-active", UpdateServiceUnit}) {
			return "activating", errors.New("exit status 3")
		}
		return "", nil
	}
}

// Stopping the helper mid-run would kill the upgrade it is running and leave
// upgrade.lock behind, so an update in flight is let finish. The trigger goes
// first, so that one starting between the look and the refusal is seen, and no
// other starts while the operator waits.
func TestRemovingTheHelperWaitsForAnUpdateItIsRunning(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	h.runner.calls = nil
	h.updateRunning()
	err := RemoveUpdateHelper(context.Background(), h.opts)
	if err == nil || !strings.Contains(err.Error(), "running an update now") || !strings.Contains(err.Error(), "trigger is off") {
		t.Fatalf("want a refusal while an update runs, saying the trigger is off, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo zoomies updates helper install") {
		t.Errorf("the refusal leaves the trigger off and should say how to turn it back on, got: %v", err)
	}
	lines := h.runner.lines()
	disable, look := slices.Index(lines, "systemctl disable --now "+UpdatePathUnit), slices.Index(lines, "systemctl is-active "+UpdateServiceUnit)
	if disable < 0 || look < 0 || disable > look {
		t.Errorf("the trigger should be turned off before the service is looked at: %v", lines)
	}
	h.neverStoppedTheService(t)
	if !exists(filepath.Join(h.opts.unitDir, UpdatePathUnit)) || !exists(filepath.Join(h.folder(), channel.MarkerFile)) {
		t.Error("a refused remove removed something")
	}
}

// foreignFolder is a folder that is not the helper's, holding files named as
// the helper's are, and named by a pointer planted beside the configuration,
// which the service can write. Its uid is the folder's real owner (root, when
// the tests run as root), which is what would let the helper's own folder
// check pass if root believed the planted pointer.
func foreignFolder(t *testing.T, configDir string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root-only")
	mustDo(t, os.Mkdir(dir, 0o750))
	plant(t, filepath.Join(dir, channel.MarkerFile), "root's")
	plant(t, filepath.Join(dir, channel.ResultFile), "root's")
	writeJSONFile(t, filepath.Join(configDir, channel.PointerFile), channel.Pointer{V: 1, Dir: dir, Binary: "/usr/local/bin/zoomies", Account: "root", UID: os.Geteuid(), ConfigDir: configDir})
	return dir
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	mustDo(t, err)
	mustDo(t, os.WriteFile(path, body, 0o644))
}

func untouched(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{channel.MarkerFile, channel.ResultFile} {
		if !exists(filepath.Join(dir, name)) {
			t.Errorf("root acted on the pointer the service can write and removed %s", filepath.Join(dir, name))
		}
	}
}

// The pointer beside the configuration is the service's to read, and the
// service can replace it; root deciding what to delete from it would be the
// service choosing a folder of root's to empty. Only root's own copy says
// where the folder is, and without it the folder is left, and said to be.
func TestRemovingTheHelperNeverActsOnThePointerTheServiceCanWrite(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	mustDo(t, os.RemoveAll(h.opts.helperStateDir))
	dir := foreignFolder(t, h.configDir)
	h.opts.StateDir, h.opts.ServiceUID = "", 0
	if err := RemoveUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RemoveUpdateHelper: %v", err)
	}
	untouched(t, dir)
	if exists(filepath.Join(h.configDir, channel.PointerFile)) {
		t.Error("the pointer beside the configuration should be removed by name")
	}
	if out := h.out.String(); strings.Contains(out, "nothing to remove") || !strings.Contains(out, "Left the update folder") {
		t.Errorf("remove should say it left the update folder, and not that there was nothing to remove:\n%s", out)
	}
}

// Without root's copy, the folder is the one the installed unit says, which is
// what the command line passes.
func TestRemovingTheHelperWithoutRootsPointerUsesWhatRootInstalled(t *testing.T) {
	h := newInstallHost(t, DeploymentNative)
	h.install(t)
	mustDo(t, os.RemoveAll(h.opts.helperStateDir))
	if err := RemoveUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RemoveUpdateHelper: %v", err)
	}
	if exists(h.folder()) {
		t.Errorf("the update folder the installed unit names was left:\n%s", h.out)
	}
}

func fakeLookup(name string) (*user.User, error) {
	switch name {
	case "zoomies":
		return &user.User{Username: name, Uid: "999"}, nil
	case "toor":
		return &user.User{Username: name, Uid: "0"}, nil
	}
	return nil, user.UnknownUserError(name)
}

// The account, the state directory and the binary are the installed unit's,
// which is what the service really runs as and from; a container's are the
// image's. Root, and a unit pointing its --config elsewhere, are refused.
func TestTheHelperIsInstalledForWhatTheInstalledUnitRuns(t *testing.T) {
	const unit = "[Service]\nUser=%s\nWorkingDirectory=/var/lib/zoomies\nExecStart=/usr/local/bin/zoomies %s --config %s\n"
	write := func(t *testing.T, dir, name, user, command, config string) {
		t.Helper()
		body := unit
		for _, v := range []string{user, command, config} {
			body = strings.Replace(body, "%s", v, 1)
		}
		mustDo(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	t.Run("a native controller", func(t *testing.T) {
		units := t.TempDir()
		write(t, units, "zoomies.service", "zoomies", "controller", "/etc/zoomies/zoomies.yaml")
		got, err := resolveHelperInstall("/etc/zoomies", units, fakeLookup, "/tmp/elsewhere/zoomies")
		want := InstallHelperOptions{Deployment: DeploymentNative, StateDir: "/var/lib/zoomies", ConfigDir: "/etc/zoomies", BinaryPath: "/usr/local/bin/zoomies", ServiceUID: 999, Account: "zoomies"}
		if err != nil || got.Deployment != want.Deployment || got.StateDir != want.StateDir || got.ConfigDir != want.ConfigDir ||
			got.BinaryPath != want.BinaryPath || got.ServiceUID != want.ServiceUID || got.Account != want.Account {
			t.Errorf("got %+v (%v), want %+v", got, err, want)
		}
	})
	t.Run("a native agent", func(t *testing.T) {
		units := t.TempDir()
		write(t, units, "zoomies-agent.service", "zoomies", "agent", "/etc/zoomies/agent.yaml")
		got, err := resolveHelperInstall("/etc/zoomies", units, fakeLookup, "/tmp/elsewhere/zoomies")
		if err != nil || got.ServiceUID != 999 || got.BinaryPath != "/usr/local/bin/zoomies" {
			t.Errorf("got %+v (%v)", got, err)
		}
	})
	t.Run("a container", func(t *testing.T) {
		config := t.TempDir()
		_, err := WriteDeploymentRecord(config, DeploymentRecord{Deployment: DeploymentDocker})
		mustDo(t, err)
		got, err := resolveHelperInstall(config, t.TempDir(), fakeLookup, "/usr/local/bin/zoomies")
		if err != nil || got.StateDir != SharedHostDir || got.ServiceUID != ImageUID || got.BinaryPath != "/usr/local/bin/zoomies" || got.Deployment != DeploymentDocker {
			t.Errorf("got %+v (%v)", got, err)
		}
	})
	for _, c := range []struct{ name, user, config, want string }{
		{"a service that runs as root", "root", "/etc/zoomies/zoomies.yaml", "needs no helper"},
		{"an account that is uid 0", "toor", "/etc/zoomies/zoomies.yaml", "needs no helper"},
		{"an account that is not there", "ada", "/etc/zoomies/zoomies.yaml", "cannot be found"},
		{"a configuration elsewhere", "zoomies", "/srv/zoomies/zoomies.yaml", "pass --config-dir /srv/zoomies"},
	} {
		t.Run(c.name, func(t *testing.T) {
			units := t.TempDir()
			write(t, units, "zoomies.service", c.user, "controller", c.config)
			_, err := resolveHelperInstall("/etc/zoomies", units, fakeLookup, "/usr/local/bin/zoomies")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want a refusal saying %q, got: %v", c.want, err)
			}
		})
	}
	t.Run("no service at all", func(t *testing.T) {
		_, err := resolveHelperInstall(t.TempDir(), t.TempDir(), fakeLookup, "/usr/local/bin/zoomies")
		if err == nil || !strings.Contains(err.Error(), "no Zoomies service") {
			t.Errorf("want a refusal, got: %v", err)
		}
	})
}

// The pointer beside the configuration is the service's to replace, so it
// does not make the helper look installed, and the folder it names is not
// root's to empty.
func TestUninstallNeverActsOnTheHelperPointerTheServiceCanWrite(t *testing.T) {
	opts := uninstallOpts(t)
	opts.Yes, opts.NonInteractive = true, true
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "")
	dir := foreignFolder(t, opts.ConfigDir)
	for _, it := range UninstallItems(opts) {
		if it.What == "update helper" && it.Present {
			t.Errorf("a pointer the service can write made the helper look installed: %+v", it)
		}
	}
	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	untouched(t, dir)
}

// Uninstalling under a running upgrade would take the binary and the units
// away from it midway, so the whole uninstall waits, with the trigger off so
// that no other update starts meanwhile.
func TestUninstallRefusesWhileTheHelperIsRunningAnUpdate(t *testing.T) {
	opts := uninstallOpts(t)
	runner := &recordingRunner{answer: func(args []string) (string, error) {
		if slices.Equal(args, []string{"is-active", UpdateServiceUnit}) {
			return "activating", errors.New("exit status 3")
		}
		return "", nil
	}}
	opts.run = runner.run
	opts.Yes, opts.NonInteractive = true, true
	config := writeFile(t, opts.ConfigDir, "zoomies.yaml", "")
	writeFile(t, opts.helperUnitDir, UpdatePathUnit, "[Path]\n")
	writeFile(t, opts.helperUnitDir, UpdateServiceUnit, "[Service]\n")
	err := Uninstall(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "running an update now") {
		t.Fatalf("want the uninstall refused while an update runs, got: %v", err)
	}
	if !exists(config) || !exists(filepath.Join(opts.helperUnitDir, UpdatePathUnit)) {
		t.Error("a refused uninstall removed something")
	}
	if !runner.ran("systemctl", "disable", "--now", UpdatePathUnit) {
		t.Errorf("the trigger should be off while the operator waits: %v", runner.lines())
	}
	if runner.ran("systemctl", "stop", UpdateServiceUnit) {
		t.Errorf("the running update was stopped: %v", runner.lines())
	}
}
