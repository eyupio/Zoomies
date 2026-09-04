package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// UninstallOptions configures `zoomies uninstall`.
type UninstallOptions struct {
	ConfigDir string
	StateDir  string
	// BinaryPath is removed last, so that a failure part-way through still
	// leaves a binary that can finish the job.
	BinaryPath string
	// ServiceUser is the account to remove. Empty means "zoomies" when running
	// as root, and nothing otherwise.
	ServiceUser string

	// Yes skips the confirmation. It does not skip the summary: an operator
	// running this from a script still gets a record of what went.
	Yes bool
	// NonInteractive forbids prompting, which means Yes must be set for
	// anything to be removed.
	NonInteractive bool
	// Deregister decides the GitHub cleanup without asking. Nil asks.
	Deregister *bool
	// RemoveVolume decides whether a containerised deployment's data volume
	// goes with it. Nil asks, and a run that cannot ask keeps the volume: the
	// volume is the database, and an uninstaller that deletes one nobody
	// mentioned is not one anybody runs twice.
	RemoveVolume *bool
	// KeepConfig leaves zoomies.yaml in place, for an operator who edited it
	// and wants to keep their work.
	KeepConfig bool

	Out    io.Writer
	In     io.Reader
	Logger *slog.Logger
}

func (o UninstallOptions) configDir() string {
	if o.ConfigDir != "" {
		return o.ConfigDir
	}
	return config.ConfigDir()
}

func (o UninstallOptions) stateDir() string {
	if o.StateDir != "" {
		return o.StateDir
	}
	return config.StateDir()
}

// RemovalItem is one thing uninstall is about to do, listed before it does any
// of it. An uninstaller that surprises you is not one you run twice.
type RemovalItem struct {
	What string
	Path string
	// Present is false for something already gone, which is normal on a
	// re-run and is printed as "already gone" rather than hidden.
	Present bool
	// Note explains anything unobvious about this item.
	Note string
}

// UninstallItems lists what would be removed from these directories. It takes
// its paths as arguments so that it can be tested against a temporary
// directory rather than against the real /etc.
func UninstallItems(opts UninstallOptions) []RemovalItem {
	cfgDir, stateDir := opts.configDir(), opts.stateDir()
	var items []RemovalItem

	// A containerised deployment is listed first because it is the part an
	// operator would otherwise not see going: a compose project left running
	// keeps creating runners long after the files are gone.
	if rec, ok := ReadDeploymentRecord(cfgDir); ok {
		items = append(items, deploymentItems(rec, opts)...)
	}

	items = append(items, []RemovalItem{
		{What: "service", Path: SystemdUnitPath(UnitController), Present: exists(SystemdUnitPath(UnitController)),
			Note: "stopped and disabled first"},
		{What: "agent service", Path: SystemdUnitPath(UnitAgent), Present: exists(SystemdUnitPath(UnitAgent))},
		{What: "database", Path: filepath.Join(stateDir, "zoomies.db"), Present: exists(filepath.Join(stateDir, "zoomies.db")),
			Note: "pools, runners, job history and the audit log"},
		{What: "state directory", Path: stateDir, Present: exists(stateDir)},
		{What: "encryption key", Path: filepath.Join(cfgDir, "encryption.key"), Present: exists(filepath.Join(cfgDir, "encryption.key")),
			Note: "the GitHub App private key and webhook secrets cannot be decrypted without it"},
	}...)
	cfgFile := filepath.Join(cfgDir, "zoomies.yaml")
	if opts.KeepConfig {
		items = append(items, RemovalItem{What: "configuration", Path: cfgFile, Present: exists(cfgFile),
			Note: "kept, because you asked"})
	} else {
		items = append(items, RemovalItem{What: "configuration", Path: cfgFile, Present: exists(cfgFile)})
	}
	if opts.BinaryPath != "" {
		items = append(items, RemovalItem{What: "binary", Path: opts.BinaryPath, Present: exists(opts.BinaryPath),
			Note: "removed last"})
	}
	return items
}

// deploymentItems lists what a containerised deployment leaves on this host,
// in the words the operator will recognise from setup.
func deploymentItems(rec DeploymentRecord, opts UninstallOptions) []RemovalItem {
	what := "compose project"
	if rec.Deployment == DeploymentDocker {
		what = "container"
	}
	target := rec.ComposeFile()
	note := "brought down with `" + strings.Join(rec.ComposeCommand, " ") + " down`"
	if rec.Deployment == DeploymentDocker {
		target = containerOr(rec)
		note = "stopped and removed"
	}
	items := []RemovalItem{
		{What: what, Path: target, Present: true, Note: note},
	}
	if rec.EnvFile != "" {
		items = append(items, RemovalItem{What: "environment file", Path: rec.EnvFile, Present: exists(rec.EnvFile),
			Note: "it holds the encryption key"})
	}
	volumeNote := "the database; kept unless you say otherwise"
	if opts.RemoveVolume != nil && *opts.RemoveVolume {
		volumeNote = "the database -- removing it destroys every pool, runner, job and audit row"
	}
	items = append(items, RemovalItem{What: "data volume", Path: volumeOr(rec), Present: true, Note: volumeNote})
	items = append(items, RemovalItem{What: "deployment record", Path: DeploymentRecordPath(opts.configDir()),
		Present: exists(DeploymentRecordPath(opts.configDir()))})
	return items
}

// Uninstall removes Zoomies from this host.
//
// The order is the point. Runners are deregistered from GitHub first, while
// the credentials to do it still exist: after the database is gone those
// registrations become orphans that somebody has to delete by hand, one at a
// time, in a web UI. Everything else is removed in an order that leaves a
// half-finished run recoverable, and every step tolerates having been done
// already, so this is safe to run twice.
func Uninstall(ctx context.Context, opts UninstallOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	u := newUI(opts.Out)
	log := opts.Logger.With("component", "installer.uninstall")

	items := UninstallItems(opts)
	u.step("This will remove Zoomies from this host")
	anything := false
	for _, it := range items {
		if !it.Present {
			u.note(fmt.Sprintf("%-16s %s -- already gone", it.What, it.Path))
			continue
		}
		anything = true
		line := fmt.Sprintf("%-16s %s", it.What, it.Path)
		if it.Note != "" {
			line += "  (" + it.Note + ")"
		}
		u.note(line)
	}
	if !anything {
		u.ok("nothing to remove; Zoomies is not installed here.")
		return nil
	}
	u.blank()

	if !opts.Yes {
		if opts.NonInteractive {
			return errors.New("installer: refusing to remove anything without confirmation; re-run with --yes")
		}
		ok, err := askYesNo(opts.In, opts.Out, "Remove all of this? Type yes to confirm: ")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("installer: nothing was removed")
		}
	}

	var done []string
	var left []string
	record := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		done = append(done, line)
		u.ok(line)
	}
	keep := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		left = append(left, line)
		u.note("left in place: " + line)
	}

	// --- Stop the service first, so nothing creates new runners while we are
	// deregistering the old ones. -----------------------------------------
	for _, unit := range []string{UnitController, UnitAgent} {
		if !exists(SystemdUnitPath(unit)) {
			continue
		}
		mgr, err := NewServiceManager(ServiceSystemd, unit)
		if err != nil {
			u.warn(err.Error())
			continue
		}
		if err := mgr.Stop(ctx); err != nil {
			u.warn("could not stop " + unit + ": " + err.Error())
		}
		if err := mgr.Disable(ctx); err != nil {
			u.warn("could not disable " + unit + ": " + err.Error())
		}
		record("stopped and disabled %s", unit)
	}

	// --- Bring a containerised deployment down, for the same reason: a
	// running container would carry on creating runners while we deregister
	// the ones it has already made. -----------------------------------------
	if rec, ok := ReadDeploymentRecord(opts.configDir()); ok {
		removeVolume, err := wantsVolumeRemoved(opts, u, rec)
		if err != nil {
			return err
		}
		tearDownDeployment(ctx, rec, removeVolume, u, record)
	}

	// --- Deregister runners from GitHub, before the credentials go. -------
	if err := maybeDeregister(ctx, opts, u, log, record); err != nil {
		// A GitHub failure must not strand the operator with a half-removed
		// install; it is reported and the removal continues.
		u.warn("could not deregister runners: " + err.Error())
		u.note("delete any leftover self-hosted runners by hand under your organisation's Actions settings.")
	}

	// --- Unit files -------------------------------------------------------
	for _, unit := range []string{UnitController, UnitAgent} {
		path := SystemdUnitPath(unit)
		if !exists(path) {
			continue
		}
		mgr, err := NewServiceManager(ServiceSystemd, unit)
		if err == nil {
			if err := mgr.Remove(ctx); err != nil {
				u.warn(err.Error())
				continue
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			u.warn("could not remove " + path + ": " + err.Error())
			continue
		}
		record("removed %s", path)
	}
	for _, label := range []string{"sh.zoomies.controller", "sh.zoomies.agent"} {
		for _, root := range []bool{true, false} {
			path := LaunchdPlistPath(label, root)
			if !exists(path) {
				continue
			}
			if err := os.Remove(path); err != nil {
				u.warn("could not remove " + path + ": " + err.Error())
				continue
			}
			record("removed %s", path)
		}
	}

	// --- State ------------------------------------------------------------
	stateDir := opts.stateDir()
	if exists(stateDir) {
		if err := os.RemoveAll(stateDir); err != nil {
			u.warn("could not remove " + stateDir + ": " + err.Error())
		} else {
			record("removed %s, including the database", stateDir)
		}
	}

	// --- Configuration ----------------------------------------------------
	cfgDir := opts.configDir()
	cfgFile := filepath.Join(cfgDir, "zoomies.yaml")
	if opts.KeepConfig && exists(cfgFile) {
		keep("%s", cfgFile)
	} else if exists(cfgFile) {
		if err := os.Remove(cfgFile); err != nil {
			u.warn("could not remove " + cfgFile + ": " + err.Error())
		} else {
			record("removed %s", cfgFile)
		}
	}
	if rec, ok := ReadDeploymentRecord(cfgDir); ok {
		paths := []string{rec.EnvFile, rec.ComposeFile(), DeploymentRecordPath(cfgDir)}
		// The backups this installer made hold the encryption key too, so they
		// go with the file they are copies of. Leaving one behind would undo
		// the care taken to remove the key at all.
		if rec.EnvFile != "" {
			backups, _ := filepath.Glob(rec.EnvFile + ".bak.*")
			paths = append(paths, backups...)
		}
		for _, path := range paths {
			if path == "" || !exists(path) {
				continue
			}
			if err := os.Remove(path); err != nil {
				u.warn("could not remove " + path + ": " + err.Error())
				continue
			}
			record("removed %s", path)
		}
	}
	keyFile := filepath.Join(cfgDir, "encryption.key")
	if exists(keyFile) {
		if err := os.Remove(keyFile); err != nil {
			u.warn("could not remove " + keyFile + ": " + err.Error())
		} else {
			record("removed %s", keyFile)
		}
	}
	// Anything else in the config directory belongs to the operator --
	// certificates they put there, a backup this installer made -- so the
	// directory is removed only when it is empty, and what is left is named.
	if exists(cfgDir) {
		if remaining, err := os.ReadDir(cfgDir); err == nil {
			if len(remaining) == 0 {
				if err := os.Remove(cfgDir); err == nil {
					record("removed %s", cfgDir)
				}
			} else {
				names := make([]string, 0, len(remaining))
				for _, e := range remaining {
					names = append(names, e.Name())
				}
				sort.Strings(names)
				keep("%s, which still holds %s", cfgDir, strings.Join(names, ", "))
			}
		}
	}

	// --- The service account ---------------------------------------------
	if name := serviceUserToRemove(opts); name != "" {
		if err := removeServiceUser(ctx, name); err != nil {
			u.warn("could not remove the " + name + " user: " + err.Error())
		} else {
			record("removed the %s system user", name)
		}
	}

	// --- The binary, last -------------------------------------------------
	if opts.BinaryPath != "" && exists(opts.BinaryPath) {
		if err := os.Remove(opts.BinaryPath); err != nil {
			u.warn("could not remove " + opts.BinaryPath + ": " + err.Error())
			u.note("remove it by hand: sudo rm " + opts.BinaryPath)
		} else {
			record("removed %s", opts.BinaryPath)
		}
	}

	u.blank()
	u.step("Done")
	for _, line := range done {
		u.note(line)
	}
	for _, line := range left {
		u.note("left: " + line)
	}
	if len(left) == 0 {
		u.note("nothing was left behind.")
	}
	return nil
}

// wantsVolumeRemoved settles whether the data volume goes too.
//
// It is a separate question from the rest of uninstall because it is the one
// irreversible answer: the volume is the database, and nothing about the
// container being gone implies the operator wanted their job history, pools
// and audit log gone with it. Silence keeps it.
func wantsVolumeRemoved(opts UninstallOptions, u *ui, rec DeploymentRecord) (bool, error) {
	if opts.RemoveVolume != nil {
		return *opts.RemoveVolume, nil
	}
	if opts.NonInteractive || opts.Yes {
		u.note("keeping the " + volumeOr(rec) + " volume, which is the database; pass --volumes to delete it too.")
		return false, nil
	}
	u.blank()
	u.step("Delete the " + volumeOr(rec) + " volume as well?")
	u.note("that volume is the database: pools, runners, job history and the audit log. Deleting it")
	u.note("cannot be undone, and keeping it lets a later install pick up exactly where this left off.")
	return askYesNo(opts.In, opts.Out, "Delete the volume? [y/N]: ")
}

// maybeDeregister offers, and then performs, the GitHub cleanup. It is offered
// first and done first because the credentials that make it possible are about
// to be deleted.
func maybeDeregister(ctx context.Context, opts UninstallOptions, u *ui, log *slog.Logger, record func(string, ...any)) error {
	dbPath := filepath.Join(opts.stateDir(), "zoomies.db")
	keyPath := filepath.Join(opts.configDir(), "encryption.key")
	if !exists(dbPath) || !exists(keyPath) {
		return nil
	}

	want := true
	switch {
	case opts.Deregister != nil:
		want = *opts.Deregister
	case opts.NonInteractive || opts.Yes:
		// Deregistering is the helpful default: leaving orphaned registrations
		// behind creates manual work for somebody later.
		want = true
	default:
		u.blank()
		u.step("Deregister this fleet's runners from GitHub first?")
		u.note("after the database is removed, the credentials are gone and every registration GitHub still")
		u.note("holds becomes an orphan that has to be deleted by hand in the Actions settings.")
		ok, err := askYesNo(opts.In, opts.Out, "Deregister them now? [Y/n]: ")
		if err != nil {
			return err
		}
		want = ok
	}
	if !want {
		u.note("leaving the GitHub registrations alone; delete them under your organisation's Actions settings.")
		return nil
	}

	key, err := cryptox.LoadKeyFile(keyPath)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, store.Options{Path: dbPath})
	if err != nil {
		return fmt.Errorf("opening %s: %w", dbPath, err)
	}
	defer st.Close()

	installations, err := st.ListInstallations(ctx)
	if err != nil {
		return err
	}
	factory := github.NewAppFactory(nil)
	removed := 0
	for _, inst := range installations {
		pem, err := key.OpenString(inst.PrivateKeyEnc)
		if err != nil {
			u.warn(fmt.Sprintf("cannot decrypt the private key for %s: %s", inst.Target, err))
			continue
		}
		client, err := factory.For(ctx, inst, []byte(pem))
		if err != nil {
			u.warn(fmt.Sprintf("cannot authenticate for %s: %s", inst.Target, err))
			continue
		}
		runners, err := client.ListRunners(ctx)
		if err != nil {
			u.warn(fmt.Sprintf("cannot list runners for %s: %s", inst.Target, err))
			continue
		}
		for _, r := range runners {
			// Only runners this fleet created are touched. Everything Zoomies
			// registers is named by github.RunnerName, which is where the
			// prefix comes from; a runner somebody else set up by hand keeps
			// working.
			if !strings.HasPrefix(r.Name, "zoomies-") {
				continue
			}
			if err := client.DeleteRunner(ctx, r.ID); err != nil {
				u.warn(fmt.Sprintf("could not deregister %s: %s", r.Name, err))
				continue
			}
			log.Info("deregistered runner", "name", r.Name, "target", inst.Target)
			removed++
		}
	}
	if removed > 0 {
		record("deregistered %d runner(s) from GitHub", removed)
	} else {
		u.note("no runners of this fleet were still registered with GitHub.")
	}
	return nil
}

// serviceUserToRemove decides whether there is an account to remove.
//
// Three conditions have to hold, and each one exists to stop a different way of
// deleting the wrong account: it must be the dedicated account this installer
// creates, this must be a root install in the system directories (a per-user
// install never created an account, and a run pointed at some other directory
// is not the one that owns /etc/zoomies), and the account must still exist.
func serviceUserToRemove(opts UninstallOptions) string {
	name := opts.ServiceUser
	if name == "" {
		name = "zoomies"
	}
	if name != "zoomies" {
		return ""
	}
	if os.Geteuid() != 0 {
		return ""
	}
	if opts.configDir() != "/etc/zoomies" || opts.stateDir() != "/var/lib/zoomies" {
		return ""
	}
	if !userExists(name) {
		return ""
	}
	return name
}

func removeServiceUser(ctx context.Context, name string) error {
	switch {
	case lookPath("userdel") != "":
		_, err := runCommand(ctx, "userdel", name)
		return err
	case lookPath("deluser") != "":
		_, err := runCommand(ctx, "deluser", name)
		return err
	default:
		return fmt.Errorf("no userdel or deluser on this host; remove the %s account by hand", name)
	}
}

// askYesNo reads a plain confirmation. It deliberately does not use the TUI:
// uninstall often runs over a pipe, in a script, or on a host where the
// terminal is barely a terminal.
func askYesNo(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return false, fmt.Errorf("installer: reading your answer: %w", err)
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	switch answer {
	case "y", "yes":
		return true, nil
	case "":
		// An empty line takes the prompt's default, which every prompt here
		// writes out in full rather than relying on capitalisation.
		return strings.Contains(prompt, "[Y/n]"), nil
	default:
		return false, nil
	}
}
