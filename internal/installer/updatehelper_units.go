//go:build unix

package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

// The helper's two units. The path unit waits for a request; the service it
// starts is the helper, as root, once per request. Nothing else starts either.
const (
	UpdatePathUnit    = "zoomies-update.path"
	UpdateServiceUnit = "zoomies-update.service"
)

const (
	// systemdUnitDir is where the helper's units are written.
	systemdUnitDir = "/etc/systemd/system"
	// updateFolderName is the update folder's name, under the state directory on
	// a native install and under the shared folder in a container.
	updateFolderName = "update"
	// imageAccount is what the published image calls ImageUID (distroless'
	// "nonroot"). The host has no such account; the helper only repeats the name
	// in its sentences.
	imageAccount = "nonroot"
)

// RenderUpdateUnits renders the helper's path and service units for the update
// folder dir and the installed binary. Both paths are the host's real ones, and
// a % in either is doubled, since systemd reads one as a specifier.
func RenderUpdateUnits(binary, dir string) (pathUnit, serviceUnit string) {
	esc := func(s string) string { return strings.ReplaceAll(s, "%", "%%") }
	pathUnit = `# Written by "zoomies updates helper install"; "zoomies updates helper remove"
# takes it away. It starts the update helper when the service asks for an update.
[Unit]
Description=Zoomies update helper: waits for the service to ask for an update
Documentation=https://zoomies.sh/security/#what-the-agent-owns-on-a-host

[Path]
PathExists=` + esc(filepath.Join(dir, channel.RequestFile)) + `
Unit=` + UpdateServiceUnit + `

[Install]
WantedBy=paths.target
`
	serviceUnit = `# Written by "zoomies updates helper install"; "zoomies updates helper remove"
# takes it away. ` + UpdatePathUnit + ` starts it, once per request; it is never
# enabled on its own.
[Unit]
Description=Zoomies update helper: applies an update the service asked for
Documentation=https://zoomies.sh/security/#what-the-agent-owns-on-a-host
# A request the helper cannot consume, in a folder it refuses and so leaves
# alone, starts it again the moment it exits. Requests answered one at a time
# never need five starts in ten minutes, so systemd stops that loop here, and
# "sudo zoomies updates helper install" starts it again once the folder is fixed.
StartLimitIntervalSec=10min
StartLimitBurst=5

[Service]
Type=oneshot
User=root
# No EnvironmentFile: the engine runs with this unit's environment, and a file
# the service could write would choose the release root installs.
ExecStart=` + esc(binary) + ` updates helper run
# A backstop. The engine's worst case is fifty minutes (twenty to stop the old
# service, thirty for the new one to answer), and a limit shorter than the run
# would kill it midway and leave upgrade.lock behind.
TimeoutStartSec=2h
# Longer than the thirty seconds the helper gives the engine to remove
# upgrade.lock once told to stop, and to write down that it was stopped.
TimeoutStopSec=2min
# Only the helper is told to stop. It stops the engine itself, asking first and
# killing it thirty seconds later, so that the engine can remove upgrade.lock;
# the default would signal the engine at the same moment, past that order.
KillMode=mixed
# No ProtectSystem: the upgrade writes the binary, the unit files and the
# deployment's Compose and environment files, and pulls images with the
# credentials root holds. Home directories stay read-only, which is why the
# installer refuses a deployment that keeps any of these in one.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
UMask=0022
`
	return pathUnit, serviceUnit
}

// InstallHelperOptions is what installing or removing the update helper needs.
// Every field is root's own record of the deployment, which
// ResolveHelperInstall reads from the installed unit or the deployment record
// and never from the update folder or anything else the service could write.
type InstallHelperOptions struct {
	Deployment Deployment
	// StateDir is the folder the update folder is made in: the state directory
	// the installed unit names on a native install, the shared folder in a
	// container.
	StateDir string
	// ConfigDir is the deployment's configuration directory: where upgrade.lock
	// and the deployment record are, and on a native install where the service
	// looks for its pointer to the update folder.
	ConfigDir string
	// BinaryPath is the installed zoomies the helper runs. It is recorded by its
	// real path, because the helper refuses a link anywhere above the binary.
	BinaryPath string
	// ServiceUID and Account are the account zoomies runs as, which owns the
	// update folder and every request in it.
	ServiceUID int
	Account    string
	// Out is where what was done is said; nil says nothing.
	Out io.Writer

	unitDir        string
	helperStateDir string
	// binaryTop is where the walk up from the binary stops: / on a host.
	binaryTop string
	run       commandRunner
	now       func() time.Time
}

func (o InstallHelperOptions) withDefaults() InstallHelperOptions {
	if o.unitDir == "" {
		o.unitDir = systemdUnitDir
	}
	if o.helperStateDir == "" {
		o.helperStateDir = UpdateHelperStateDir
	}
	if o.binaryTop == "" {
		o.binaryTop = "/"
	}
	if o.run == nil {
		o.run = runCommand
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	return o
}

// ResolveHelperInstall works out the helper's options for the deployment whose
// configuration is in configDir, from what root installed: the deployment
// record for a container, and otherwise the zoomies unit, whose User= is the
// account, whose WorkingDirectory= is the state directory and whose ExecStart=
// is the binary.
func ResolveHelperInstall(configDir string) (InstallHelperOptions, error) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return InstallHelperOptions{}, errors.New(`the update helper is a pair of systemd units, and this host does not run systemd; update it by hand with "sudo zoomies upgrade"`)
	}
	executable, err := os.Executable()
	if err != nil {
		return InstallHelperOptions{}, fmt.Errorf("cannot tell where this zoomies binary is: %w", err)
	}
	return resolveHelperInstall(configDir, systemdUnitDir, user.Lookup, executable)
}

// rootNeedsNoHelper is the refusal of a service that runs as root: it can
// upgrade itself, and the helper's owner checks keep nothing out for it.
const rootNeedsNoHelper = `a service that runs as root can upgrade itself and needs no helper; update this host with "sudo zoomies upgrade"`

func resolveHelperInstall(configDir, unitDir string, lookup func(string) (*user.User, error), executable string) (InstallHelperOptions, error) {
	if rec, ok := ReadDeploymentRecord(configDir); ok && rec.Deployment.Containerised() {
		return InstallHelperOptions{
			Deployment: rec.Deployment, StateDir: SharedHostDir, ConfigDir: configDir,
			BinaryPath: executable, ServiceUID: ImageUID, Account: imageAccount,
		}, nil
	}
	for _, unit := range []string{UnitController, UnitAgent} {
		path := filepath.Join(unitDir, unit+".service")
		body, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return InstallHelperOptions{}, fmt.Errorf("cannot read %s, the unit the helper would update: %w", path, err)
		}
		return nativeHelperInstall(path, string(body), configDir, lookup)
	}
	return InstallHelperOptions{}, fmt.Errorf("there is no %s.service or %s.service in %s and no container deployment recorded in %s, so there is no Zoomies service here for the helper to update; install Zoomies first, or pass --config-dir with the deployment's configuration directory", UnitController, UnitAgent, unitDir, configDir)
}

// nativeHelperInstall reads the options from the unit a native install wrote.
func nativeHelperInstall(path, body, configDir string, lookup func(string) (*user.User, error)) (InstallHelperOptions, error) {
	name, _, _ := ReadUnitIdentity(path)
	if name == "" || name == "root" {
		return InstallHelperOptions{}, fmt.Errorf("%s runs zoomies as root, and %s", path, rootNeedsNoHelper)
	}
	account, err := lookup(name)
	if err != nil {
		return InstallHelperOptions{}, fmt.Errorf("%s runs zoomies as %s, and that account cannot be found on this host (%v); run \"sudo zoomies init\" again to put the service right", path, name, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return InstallHelperOptions{}, fmt.Errorf("%s runs zoomies as %s, whose uid %q is not a number", path, name, account.Uid)
	}
	if uid == 0 {
		return InstallHelperOptions{}, fmt.Errorf("%s runs zoomies as %s, which is uid 0, and %s", path, name, rootNeedsNoHelper)
	}
	workDir := unitValue(body, "WorkingDirectory")
	if workDir == "" {
		return InstallHelperOptions{}, fmt.Errorf("%s names no WorkingDirectory, which is the state directory the update folder is made in; run \"sudo zoomies init\" again to write the unit afresh", path)
	}
	args := strings.Fields(unitValue(body, "ExecStart"))
	if len(args) == 0 {
		return InstallHelperOptions{}, fmt.Errorf("%s has no ExecStart, so there is no binary for the helper to run; run \"sudo zoomies init\" again to write the unit afresh", path)
	}
	for i, arg := range args {
		file, ok := strings.CutPrefix(arg, "--config=")
		if !ok && arg == "--config" && i+1 < len(args) {
			file, ok = args[i+1], true
		}
		// The service finds its pointer beside its --config file, so a pointer
		// written anywhere else would be one it never reads.
		if ok && filepath.Dir(strings.Trim(file, `"`)) != filepath.Clean(configDir) {
			return InstallHelperOptions{}, fmt.Errorf("%s starts zoomies with --config %s, and the service looks for its pointer to the update folder beside that file; pass --config-dir %s", path, file, filepath.Dir(strings.Trim(file, `"`)))
		}
	}
	return InstallHelperOptions{
		Deployment: DeploymentNative, StateDir: strings.Trim(workDir, `"`), ConfigDir: configDir,
		BinaryPath: strings.Trim(args[0], `"`), ServiceUID: uid, Account: name,
	}, nil
}

// unitValue is the last value a unit gives key, as systemd takes the last.
func unitValue(body, key string) string {
	value := ""
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			value = strings.TrimSpace(v)
		}
	}
	return value
}

// InstallUpdateHelper installs the helper: it makes the update folder, owned by
// the service's account, writes root's pointer to it (and on a native install
// the copy the service reads), writes and starts the units, and writes the
// marker last, which is what tells the controller the helper is there.
//
// Everything the helper would refuse is refused here first, before anything is
// written, in the helper's own words: a refusal discovered by the helper is a
// request nobody answers and a timeout ninety minutes later.
func InstallUpdateHelper(ctx context.Context, opts InstallHelperOptions) error {
	o := opts.withDefaults()
	plan, err := o.planInstall(ctx)
	if err != nil {
		return err
	}
	return o.applyInstall(ctx, plan)
}

// helperInstall is what an install writes, worked out and checked.
type helperInstall struct {
	dir     string
	pointer []byte
	binary  string
	units   [][2]string
}

func (o InstallHelperOptions) planInstall(ctx context.Context) (helperInstall, error) {
	if o.ServiceUID <= 0 {
		return helperInstall{}, fmt.Errorf("zoomies runs as uid %d here, and %s", o.ServiceUID, rootNeedsNoHelper)
	}
	if !cleanAbs(o.StateDir) {
		return helperInstall{}, fmt.Errorf("the folder to make the update folder in, %q, is not a clean absolute path", o.StateDir)
	}
	binary, err := filepath.EvalSymlinks(o.BinaryPath)
	if err != nil {
		return helperInstall{}, fmt.Errorf("cannot find the zoomies binary %s: %w", o.BinaryPath, err)
	}
	dir := filepath.Join(o.StateDir, updateFolderName)
	p := channel.Pointer{V: updates.WireVersion, Dir: dir, Binary: binary, Account: o.Account, UID: o.ServiceUID, ConfigDir: o.ConfigDir}
	if err := checkPointer(filepath.Join(o.helperStateDir, channel.PointerFile), p, fileOwner, o.binaryTop); err != nil {
		return helperInstall{}, err
	}
	for _, path := range []string{dir, binary, o.ConfigDir} {
		if err := checkUnitPath(path); err != nil {
			return helperInstall{}, err
		}
	}
	if o.Deployment.Containerised() {
		if err := o.checkSharedMount(ctx); err != nil {
			return helperInstall{}, err
		}
	}
	pathUnit, serviceUnit := RenderUpdateUnits(binary, dir)
	units := [][2]string{{UpdatePathUnit, pathUnit}, {UpdateServiceUnit, serviceUnit}}
	for _, u := range units {
		path := filepath.Join(o.unitDir, u[0])
		if old, err := os.ReadFile(path); err == nil && string(old) != u[1] {
			return helperInstall{}, fmt.Errorf("%s already exists with different contents; inspect it, and if it should go, run \"sudo zoomies updates helper remove\" and install again", path)
		}
	}
	if err := checkUpdateFolderParent(o.StateDir, o.ServiceUID, fileUID); err != nil {
		return helperInstall{}, err
	}
	// Root's folder for the helper is made at the first write, if it is not
	// there yet; one that is there and that the helper would refuse is refused
	// now, with everything else.
	if info, err := os.Lstat(o.helperStateDir); err == nil {
		if !info.IsDir() {
			return helperInstall{}, fmt.Errorf("the update helper's state directory %s is not a folder (it is a %s); remove it so the helper can make its own; %s", o.helperStateDir, fileKind(info.Mode()), stateRefusal)
		}
		if err := checkStateOwner(o.helperStateDir, info); err != nil {
			return helperInstall{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return helperInstall{}, fmt.Errorf("cannot look at the update helper's state directory %s: %w", o.helperStateDir, err)
	}
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return helperInstall{}, fmt.Errorf("cannot encode the update helper's pointer: %w", err)
	}
	return helperInstall{dir: dir, pointer: append(body, '\n'), binary: binary, units: units}, nil
}

func (o InstallHelperOptions) applyInstall(ctx context.Context, plan helperInstall) error {
	folder, err := makeUpdateFolder(o.StateDir, plan.dir, o.ServiceUID)
	if err != nil {
		return err
	}
	defer folder.Close()
	fmt.Fprintf(o.Out, "The update folder is %s, owned by %s (uid %d).\n", plan.dir, o.Account, o.ServiceUID)

	// Root's copy, which is the one the helper reads, in a folder only root can
	// write.
	state, err := openStateDir(o.helperStateDir)
	if err != nil {
		return err
	}
	state.Close()
	written := []string{filepath.Join(o.helperStateDir, channel.PointerFile)}
	if err := writeFileAtomic(written[0], plan.pointer, 0o600); err != nil {
		return fmt.Errorf("cannot write the update helper's pointer %s: %w", written[0], err)
	}
	// A container finds its folder under the shared folder and has no
	// configuration directory of the installer's to read a pointer from.
	if !o.Deployment.Containerised() {
		path := filepath.Join(o.ConfigDir, channel.PointerFile)
		if err := writeFileAtomic(path, plan.pointer, 0o644); err != nil {
			return fmt.Errorf("cannot write %s, where the service looks for the update folder: %w", path, err)
		}
		written = append(written, path)
	}
	fmt.Fprintf(o.Out, "Wrote %s.\n", strings.Join(written, " and "))

	for _, u := range plan.units {
		path := filepath.Join(o.unitDir, u[0])
		if err := writeFileAtomic(path, []byte(u[1]), 0o644); err != nil {
			return fmt.Errorf("cannot write %s: %w", path, err)
		}
	}
	if _, err := o.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	// A helper systemd stopped for starting too often stays stopped until this.
	_, _ = o.run(ctx, "systemctl", "reset-failed", UpdateServiceUnit, UpdatePathUnit)
	if _, err := o.run(ctx, "systemctl", "enable", "--now", UpdatePathUnit); err != nil {
		return err
	}
	if err := o.writeMarker(folder, plan); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Installed and started %s, which runs %s as root when the service asks for an update; remove it with \"sudo zoomies updates helper remove\".\n", UpdatePathUnit, UpdateServiceUnit)
	return nil
}

// writeMarker writes helper.json, unless the one there already says the same
// thing, so that installing again changes nothing.
func (o InstallHelperOptions) writeMarker(folder *os.Root, plan helperInstall) error {
	m := channel.Marker{V: updates.WireVersion, Version: version.Version, Binary: plan.binary, InstalledAt: o.now().UTC()}
	// Read through the folder's handle, never by path: the folder is the
	// service's, and a pipe put in the marker's place must not hang root.
	if f, _, err := openPlainFile(folder, channel.MarkerFile); err == nil {
		var old channel.Marker
		decodeErr := json.NewDecoder(io.LimitReader(f, maxPointerBytes)).Decode(&old)
		f.Close()
		if decodeErr == nil && old.V == m.V && old.Version == m.Version && old.Binary == m.Binary {
			return nil
		}
	}
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("cannot encode the update helper's marker: %w", err)
	}
	if err := replaceFile(folder, channel.MarkerFile, body, 0o644); err != nil {
		return fmt.Errorf("cannot write the update helper's marker in %s: %w", plan.dir, err)
	}
	return nil
}

// makeUpdateFolder makes the update folder, gives it to the service's account,
// and opens it as the helper will, through the helper's own check. This is the
// only place the folder is made.
func makeUpdateFolder(parent, dir string, uid int) (*os.Root, error) {
	base, err := os.OpenRoot(parent)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s to make the update folder in: %w", parent, err)
	}
	defer base.Close()
	if err := base.Mkdir(updateFolderName, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("cannot make the update folder %s: %w", dir, err)
	}
	named, err := base.Lstat(updateFolderName)
	if err != nil {
		return nil, fmt.Errorf("cannot look at the update folder %s: %w", dir, err)
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("%s is a %s and not a folder; remove it, and run \"sudo zoomies updates helper install\" again to make the update folder", dir, fileKind(named.Mode()))
	}
	folder, err := base.OpenRoot(updateFolderName)
	if err != nil {
		return nil, fmt.Errorf("cannot open the update folder %s: %w", dir, err)
	}
	opened, err := folder.Stat(".")
	switch {
	case err != nil:
		err = fmt.Errorf("cannot look at the update folder %s once open: %w", dir, err)
	case !os.SameFile(named, opened):
		err = fmt.Errorf("the update folder %s changed while it was being made; run this again", dir)
	default:
		// Through the open folder, so what is given away is the folder that was
		// looked at. The group is left alone: only the owner may write.
		if err = folder.Chown(".", uid, -1); err == nil {
			err = folder.Chmod(".", 0o750)
		}
		if err != nil {
			err = fmt.Errorf("cannot give the update folder %s to uid %d: %w", dir, uid, err)
		}
	}
	folder.Close()
	if err != nil {
		return nil, err
	}
	return openUpdateFolder(dir, uid, fileUID)
}

// checkSharedMount refuses a container that does not mount the shared folder.
// Only a container that runs runners does, and the update folder has to be
// where the controller inside can write a request the host can see.
func (o InstallHelperOptions) checkSharedMount(ctx context.Context) error {
	rec, ok := ReadDeploymentRecord(o.ConfigDir)
	if !ok {
		return fmt.Errorf("there is no deployment record in %s, so the helper cannot tell which container it would update; pass --config-dir with the deployment's configuration directory", o.ConfigDir)
	}
	container := containerOr(rec)
	format := `{{range .Mounts}}{{if eq .Destination "` + SharedHostDir + `"}}{{.Source}}{{end}}{{end}}`
	out, err := o.run(ctx, deploymentRuntime(rec, ""), "inspect", "--type", "container", "--format", format, container)
	if err != nil {
		return fmt.Errorf("cannot ask the %s container whether it mounts the shared folder %s: %w; start the deployment and run this again", container, SharedHostDir, err)
	}
	source := strings.TrimSpace(out)
	switch {
	case source == "":
		return fmt.Errorf("the %s container does not mount the shared folder %s, which a container mounts only when it runs runners, so the controller in it could not write a request the helper would see; a controller-only container is not offered the helper, so update it by hand with \"sudo zoomies upgrade\"", container, SharedHostDir)
	case filepath.Clean(source) != o.StateDir:
		return fmt.Errorf("the %s container mounts %s at the shared folder and not %s, so a request it wrote would not be in the folder the helper watches; mount %s at the same path, as \"sudo zoomies upgrade --yes\" does", container, source, o.StateDir, o.StateDir)
	}
	// A daemon that remaps user namespaces, or runs rootless, owns what the
	// container writes under another uid than the image's, and the helper
	// refuses every request that is not the image's. Docker says so in its
	// security options; Podman is left to the helper's own check.
	if deploymentRuntime(rec, "") == "docker" {
		info, err := o.run(ctx, "docker", "info", "--format", "{{.SecurityOptions}}")
		if err != nil {
			return fmt.Errorf("cannot ask Docker how it runs containers: %w; start it and run this again", err)
		}
		for _, option := range []string{"name=userns", "name=rootless"} {
			if strings.Contains(info, option) {
				return fmt.Errorf("the Docker daemon on this host runs with %s, which maps the container's account to another uid on the host, so every request would be owned by an account the helper does not serve; update this host by hand with \"sudo zoomies upgrade\"", option)
			}
		}
	}
	return nil
}

// protectedHomes are the folders the helper's unit keeps read-only
// (ProtectHome=read-only).
var protectedHomes = []string{"/home", "/root", "/run/user"}

// checkUnitPath refuses a path the helper's unit could not use: one in a home
// directory, which the upgrade could not write under ProtectHome, and one
// systemd would read as something other than a path.
func checkUnitPath(path string) error {
	resolved := resolvedPath(path)
	for _, home := range protectedHomes {
		if resolved == home || strings.HasPrefix(resolved, home+"/") {
			where := path
			if resolved != path {
				where = path + " (" + resolved + ", once its links are followed)"
			}
			return fmt.Errorf("%s is in %s, which the helper's unit keeps read-only so that an upgrade it runs cannot change a home directory, and the upgrade would need to write there; install Zoomies outside a home directory to use the helper", where, home)
		}
	}
	if i := strings.IndexFunc(path, func(r rune) bool {
		return unicode.IsSpace(r) || !unicode.IsGraphic(r) || strings.ContainsRune(`"'\$`, r)
	}); i >= 0 {
		return fmt.Errorf("%s has %q in it, which systemd reads as something other than part of a path; move it to a path without one to use the helper", path, []rune(path[i:])[0])
	}
	return nil
}

// resolvedPath is path with every link in it resolved: a link out of a home
// directory leads into one all the same. Where the end of the path does not
// exist yet, the part that does is resolved and the rest kept.
func resolvedPath(path string) string {
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if dir == filepath.Dir(dir) {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// RemoveUpdateHelper stops the helper and removes it: its units, root's state
// and pointer, the pointer beside the configuration, and what it and the
// installer wrote in the update folder. The folder itself goes only when that
// leaves it empty, since anything else in it is not the helper's.
//
// Which folder that is, and whose, comes from root's copy of the pointer, or
// failing that from StateDir and ServiceUID, which the caller takes from what
// root installed. Never from the copy beside the configuration: the service can
// replace that one, and would then choose a folder for root to empty.
func RemoveUpdateHelper(ctx context.Context, opts InstallHelperOptions) error {
	o := opts.withDefaults()
	removed, left, err := o.remove(ctx)
	for _, line := range removed {
		fmt.Fprintf(o.Out, "Removed %s.\n", line)
	}
	for _, line := range left {
		fmt.Fprintf(o.Out, "Left %s.\n", line)
	}
	if err == nil && len(removed) == 0 && len(left) == 0 {
		fmt.Fprintln(o.Out, "The update helper is not installed on this host; there was nothing to remove.")
	}
	return err
}

// removeUpdateHelper is RemoveUpdateHelper for a caller that reports what was
// removed and left in its own words, which is uninstall.
func removeUpdateHelper(ctx context.Context, opts InstallHelperOptions) (removed, left []string, err error) {
	return opts.withDefaults().remove(ctx)
}

// stopUpdateTrigger turns the path unit off, so that no update starts from now
// on, and then refuses if one is running. In that order, an update that
// started a moment before is seen; and the running one is never stopped, which
// would kill the upgrade midway and leave upgrade.lock behind.
func stopUpdateTrigger(ctx context.Context, opts InstallHelperOptions) error {
	o := opts.withDefaults()
	if exists(filepath.Join(o.unitDir, UpdatePathUnit)) {
		if _, err := o.run(ctx, "systemctl", "disable", "--now", UpdatePathUnit); err != nil {
			return err
		}
	}
	state, _ := o.run(ctx, "systemctl", "is-active", UpdateServiceUnit)
	if state = strings.TrimSpace(state); slices.Contains([]string{"active", "activating", "deactivating", "reloading"}, state) {
		return fmt.Errorf("the update helper is running an update now (%s is %s); its trigger is off, so no other will start, and removing it now would stop this one midway: wait for it to finish (journalctl -u zoomies-update -f) and run this again", UpdateServiceUnit, state)
	}
	return nil
}

func (o InstallHelperOptions) remove(ctx context.Context) (removed, left []string, err error) {
	if err := stopUpdateTrigger(ctx, o); err != nil {
		return nil, nil, err
	}
	// Read before root's copy goes: it says where the folder is and whose.
	p, havePointer := o.rootPointer()
	configPointer := ""
	if o.ConfigDir != "" {
		configPointer = filepath.Join(o.ConfigDir, channel.PointerFile)
	}
	installed := havePointer || exists(o.helperStateDir) || (configPointer != "" && exists(configPointer))

	pathUnit, serviceUnit := filepath.Join(o.unitDir, UpdatePathUnit), filepath.Join(o.unitDir, UpdateServiceUnit)
	for _, path := range []string{pathUnit, serviceUnit} {
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
			installed = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return removed, left, fmt.Errorf("cannot remove %s: %w", path, err)
		}
	}
	if len(removed) > 0 {
		_, _ = o.run(ctx, "systemctl", "daemon-reload")
		_, _ = o.run(ctx, "systemctl", "reset-failed", UpdateServiceUnit, UpdatePathUnit)
	}

	switch {
	case havePointer:
		r, l := emptyUpdateFolder(p.Dir, p.UID)
		removed, left = append(removed, r...), append(left, l...)
	case o.StateDir != "" && o.ServiceUID > 0:
		r, l := emptyUpdateFolder(filepath.Join(o.StateDir, updateFolderName), o.ServiceUID)
		removed, left = append(removed, r...), append(left, l...)
	case installed:
		left = append(left, fmt.Sprintf("the update folder as it is: root's pointer in %s is gone, and nothing else root wrote says which folder was the helper's; look in the state directory, or the shared folder for a container, and remove update/ by hand if it is there", o.helperStateDir))
	}

	// By name only: what it says is never read.
	if configPointer != "" {
		if err := os.Remove(configPointer); err == nil {
			removed = append(removed, configPointer)
		} else if !errors.Is(err, fs.ErrNotExist) {
			left = append(left, fmt.Sprintf("%s, which could not be removed: %v", configPointer, err))
		}
	}

	// Root's state goes only when it is the folder the helper made: root's, and
	// a folder rather than a link to one.
	if info, err := os.Lstat(o.helperStateDir); err == nil {
		owner, _, ok := fileOwner(info)
		switch {
		case !info.IsDir():
			left = append(left, fmt.Sprintf("%s, which is a %s and not the helper's folder", o.helperStateDir, fileKind(info.Mode())))
		case !ok || owner != helperEUID():
			left = append(left, fmt.Sprintf("%s, which is owned by uid %d and not the helper's folder", o.helperStateDir, owner))
		default:
			if err := os.RemoveAll(o.helperStateDir); err != nil {
				left = append(left, fmt.Sprintf("%s, which could not be removed: %v", o.helperStateDir, err))
			} else {
				removed = append(removed, o.helperStateDir+", with the helper's limits and its pointer")
			}
		}
	}
	return removed, left, nil
}

// rootPointer is root's copy of the pointer, read only from a state directory
// that is root's and only if root owns the file, as the helper reads it. It
// creates nothing: an absent directory is no pointer.
func (o InstallHelperOptions) rootPointer() (channel.Pointer, bool) {
	if _, err := os.Lstat(o.helperStateDir); err != nil {
		return channel.Pointer{}, false
	}
	root, err := openStateDir(o.helperStateDir)
	if err != nil {
		return channel.Pointer{}, false
	}
	defer root.Close()
	f, info, err := openPlainFile(root, channel.PointerFile)
	if err != nil {
		return channel.Pointer{}, false
	}
	defer f.Close()
	if checkRootOwns("the update helper's pointer", filepath.Join(o.helperStateDir, channel.PointerFile), info, fileUID) != nil {
		return channel.Pointer{}, false
	}
	body, err := io.ReadAll(io.LimitReader(f, maxPointerBytes+1))
	if err != nil {
		return channel.Pointer{}, false
	}
	p, err := channel.DecodePointer(channel.PointerFile, body)
	if err != nil || p.UID <= 0 || !cleanAbs(p.Dir) {
		return channel.Pointer{}, false
	}
	return p, true
}

// emptyUpdateFolder removes from the update folder what the helper, the
// installer and the service put there for the helper, and the folder too once
// nothing else is in it. A folder the helper would not open is left untouched.
func emptyUpdateFolder(dir string, uid int) (removed, left []string) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	folder, err := openUpdateFolder(dir, uid, fileUID)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s untouched, because %v", dir, err)}
	}
	defer folder.Close()
	for _, name := range []string{channel.MarkerFile, channel.ResultFile, channel.RequestFile} {
		if err := folder.Remove(name); err == nil {
			removed = append(removed, filepath.Join(dir, name))
		} else if !errors.Is(err, fs.ErrNotExist) {
			left = append(left, fmt.Sprintf("%s, which could not be removed: %v", filepath.Join(dir, name), err))
		}
	}
	d, err := folder.Open(".")
	if err != nil {
		return removed, append(left, fmt.Sprintf("%s, which could not be read: %v", dir, err))
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return removed, append(left, fmt.Sprintf("%s, which could not be read: %v", dir, err))
	}
	if len(names) > 0 {
		slices.Sort(names)
		return removed, append(left, fmt.Sprintf("%s, which still holds %s", dir, strings.Join(names, ", ")))
	}
	if err := os.Remove(dir); err != nil {
		return removed, append(left, fmt.Sprintf("%s, which could not be removed: %v", dir, err))
	}
	return append(removed, dir), left
}
