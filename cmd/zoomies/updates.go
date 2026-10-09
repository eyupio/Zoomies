package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/installer"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// updatesEUID is os.Geteuid, replaceable so that a test can be an account
// other than the one running it.
var updatesEUID = os.Geteuid

// updateHelperStateDir is root's directory for the helper, replaceable so that
// a test can stand one up of its own.
var updateHelperStateDir = installer.UpdateHelperStateDir

// helperStatusTailLines is how much of the last run's log tail status prints:
// the end, where the reason is, and not the whole run.
const helperStatusTailLines = 20

func runUpdates(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "updates", `Release updates: what the controller would take, asking it to check or to update itself, and the helper that applies them. To upgrade this host by hand, use "zoomies upgrade".`, []*subcommand{
		{"status", "", "What an update would take, the helper's state and the controller's last attempt", updatesStatusCmd},
		{"check", "", "Read the list of releases from GitHub now and say what it leaves", updatesCheck},
		{"apply", "[--version tag] [--yes]", "Ask the controller to update itself, through the root helper", updatesApply},
		{"helper", "<install|remove|run|status>", "The root-owned helper on this host that applies an update", runUpdatesHelper},
	}, args)
}

func runUpdatesHelper(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "updates helper", "The update helper on this host. It is local: it never talks to a controller.", []*subcommand{
		{"install", "[--config-dir path]", "Make this host ready to be updated from the controller: install the helper, as root", updatesHelperInstall},
		{"remove", "[--config-dir path]", "Stop the helper and remove it, its units and its files", updatesHelperRemove},
		{"run", "", "Answer the request in the update folder; the helper's unit runs it, as root", updatesHelperRun},
		{"status", "", "Where the update folder is, whether the helper is installed, and its last result", updatesHelperStatus},
	}, args)
}

func updatesStatusCmd(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates status",
		"What an update would take: the mode and soak, the build that is running, the release the mode would take and why, whether the update helper is installed on the controller's host, and the controller's open or last update attempt. It changes nothing. For the helper on this host, use \"zoomies updates helper status\".")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies updates status", "zoomies updates status --output json")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var st updatesStatus
	raw, err := client.get(ctx, "/updates", nil, &st)
	if err != nil {
		return plainAPIError(err)
	}
	if p.structured() {
		return p.emit(raw)
	}

	// Every string below came from the controller, and the attempt's error can be
	// a line of the helper's log, so each goes through plain before it is placed
	// in a row of the CLI's own.
	release := "a release build"
	if !st.Running.Release {
		release = "not from a release, so it is never offered an update"
	}
	rows := [][2]string{
		{"Mode", plain(st.Mode)},
		{"Soak", plain(st.Soak)},
		{"Running", fmt.Sprintf("%s (%s)", plain(st.Running.Version), release)},
	}
	if st.Latest != nil {
		rows = append(rows, [2]string{"Latest", fmt.Sprintf("%s, published %s", plain(st.Latest.Tag), p.relTime(st.Latest.PublishedAt))})
	}
	if st.Target != nil {
		target := plain(st.Target.Tag) + ", newer than the running build"
		if !st.Target.Newer {
			target = plain(st.Target.Tag) + ", not newer than the running build"
		}
		if st.Target.DueAt != nil {
			target += ", due " + p.relTime(*st.Target.DueAt)
		}
		rows = append(rows, [2]string{"Target", target})
	}
	rows = append(rows,
		[2]string{"Checked", p.relTimePtr(st.CheckedAt)},
		[2]string{"Reason", plain(st.Reason)},
		[2]string{"Helper", plain(st.Helper.State) + ": " + plain(st.Helper.Reason)},
	)
	if st.Helper.InstallCommand != "" {
		rows = append(rows, [2]string{"Install", plain(st.Helper.InstallCommand)})
	}
	if st.Helper.UpgradeCommand != "" {
		rows = append(rows, [2]string{"Upgrade", plain(st.Helper.UpgradeCommand)})
	}
	if a := st.Controller; a != nil {
		rows = append(rows, [2]string{"Attempt", fmt.Sprintf("%s %s: %s to %s (%s), requested %s",
			plain(a.ID), plain(a.State), plain(a.From), plain(a.To), plain(a.Trigger), p.relTime(a.RequestedAt))})
		if a.FinishedAt != nil {
			rows = append(rows, [2]string{"Finished", p.relTime(*a.FinishedAt)})
		}
		if a.Error != "" {
			rows = append(rows, [2]string{"Error", plain(a.Error)})
		}
	} else {
		rows = append(rows, [2]string{"Attempt", "none yet"})
	}
	p.keyValues(rows)
	return nil
}

func updatesCheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates check",
		"Read the list of releases from GitHub now instead of waiting for the scheduled check, and say what the controller makes of it. At most one request a minute goes to GitHub; asking again inside the minute answers the status as it stands. Needs the admin role.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies updates check")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var st updatesStatus
	raw, err := client.post(ctx, "/updates/check", nil, nil, &st)
	if err != nil {
		return plainAPIError(err)
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note("%s", plain(st.Reason))
	return nil
}

func updatesApply(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates apply [--version tag] [--yes]",
		`Ask the controller to update ITSELF: it writes a request for the root-owned update helper on its own host, and the helper replaces the controller's binary and restarts the service. This is not "zoomies upgrade", which upgrades the host you run it on and needs no controller. Needs the platform role, an update mode other than off, and a helper installed on the controller's host ("zoomies updates status" says whether it is). The controller answers at once and the helper on its own time; follow the attempt with "zoomies updates status". Runners and jobs already running carry on through the restart. Without --version it takes the newest release that can be installed on the controller's system.`)
	cf := registerClientFlags(fs, false)
	version := fs.String("version", "", "the release to take, such as v1.3.5; left out, the newest the controller can install")
	yes := fs.Bool("yes", false, "do not ask for confirmation; needed when there is no terminal to ask at")
	fs.example("zoomies updates apply", "zoomies updates apply --version v1.3.5 --yes")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	// An untyped nil, so that a request without --version carries no body at all
	// and not the JSON null a nil map would encode to.
	var body any
	var tag string
	if fs.changed("version") {
		tag = strings.TrimSpace(*version)
		if tag == "" {
			return usagef("updates apply", "--version needs a release tag such as v1.3.5; leave it out to take the newest")
		}
		body = map[string]any{"tag": tag}
	}

	// Before the question, so that an address or a credential that cannot be used
	// is reported while the person can still put it right, and not after a yes.
	client, err := cf.client()
	if err != nil {
		return err
	}

	if !*yes {
		if !canAsk(e, false) {
			return usagef("updates apply", "this asks the controller to update itself and restart, and there is no terminal to ask you at; run it again with --yes to say yes")
		}
		what := "the newest release the controller can install"
		if tag != "" {
			what = tag
		}
		fmt.Fprintf(e.err, "Ask the controller to update itself to %s? It restarts the controller's service through its root helper. [y/N] ", what)
		line, _ := bufio.NewReader(e.in).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Fprintln(e.out, "Nothing was asked of the controller.")
			return nil
		}
	}

	var st updatesStatus
	if _, err := client.post(ctx, "/updates/controller", nil, body, &st); err != nil {
		return plainAPIError(err)
	}
	a := st.Controller
	if a == nil {
		fmt.Fprintln(e.out, `The controller accepted the request. Follow it with "zoomies updates status".`)
		return nil
	}
	fmt.Fprintf(e.out, "The controller accepted attempt %s: %s to %s, %s. The helper answers on its own time; follow it with \"zoomies updates status\".\n",
		plain(a.ID), plain(a.From), plain(a.To), plain(a.State))
	return nil
}

// plainAPIError makes a refusal safe to print and says which code it was. The
// message is the controller's sentence for the person to act on, so it is kept
// whole; what changes is that terminal control in it, or in its detail, is
// replaced, and that the stable code is named beside it, because a script and a
// support thread both go by the code. A 401 and a 403 are left to apiError,
// which knows what to say about a credential and a role.
func plainAPIError(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	safe := *ae
	safe.message, safe.detail, safe.field = plain(ae.message), plain(ae.detail), plain(ae.field)
	if ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden || ae.code == "" {
		return &safe
	}
	return fmt.Errorf("%w (%s)", &safe, plain(ae.code))
}

func updatesHelperInstall(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper install [--config-dir path]",
		`Install the update helper, which lets the controller update this host when an administrator asks or the update mode says so. `+installer.UpdateHelperControllerOnlyYet+` It makes the update folder, owned by the account zoomies runs as, writes root's pointer to it in `+installer.UpdateHelperStateDir+`, and installs and starts the zoomies-update units, which run "zoomies updates helper run" as root when the service writes a request. Everything the helper would refuse is refused here first. Nothing installs the helper but its owner: the controller's update mode cannot.`)
	configDir := flags.String("config-dir", "", "the deployment's configuration directory, where zoomies.yaml and deployment.json are (default: "+config.ConfigDir()+")")
	flags.example("sudo zoomies updates helper install")
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	if err := installer.CheckUpdateHelperPlatform(); err != nil {
		return err
	}
	if uid := updatesEUID(); uid != 0 {
		return fmt.Errorf(`installing the update helper writes systemd units and a folder only root may, and this is uid %d; run "sudo zoomies updates helper install"`, uid)
	}
	opts, err := installer.ResolveHelperInstall(orConfigDir(*configDir))
	if err != nil {
		return err
	}
	opts.Out = e.out
	return installer.InstallUpdateHelper(ctx, opts)
}

func updatesHelperRemove(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper remove [--config-dir path]",
		`Stop the update helper and remove it: its units, root's state and pointer in `+installer.UpdateHelperStateDir+`, the pointer beside the configuration, and the files it and the installer put in the update folder. The folder goes too when nothing else is in it; which folder that is comes from root's copy of the pointer, or from the installed unit, and never from the copy the service can write. The trigger is turned off first, so no new update starts, and an update the helper is already running is let finish: remove refuses until it has, and is run again then. The controller stops offering updates for this host once the helper is gone.`)
	configDir := flags.String("config-dir", "", "the deployment's configuration directory (default: "+config.ConfigDir()+")")
	flags.example("sudo zoomies updates helper remove")
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	if err := installer.CheckUpdateHelperPlatform(); err != nil {
		return err
	}
	if uid := updatesEUID(); uid != 0 {
		return fmt.Errorf(`removing the update helper changes systemd units and folders only root may, and this is uid %d; run "sudo zoomies updates helper remove"`, uid)
	}
	// Where the update folder is comes from root's copy of the pointer; failing
	// that, from what root installed. A unit that is gone, or that runs as root,
	// leaves only root's copy to go by.
	opts, err := installer.ResolveHelperInstall(orConfigDir(*configDir))
	if err != nil {
		opts = installer.InstallHelperOptions{ConfigDir: orConfigDir(*configDir)}
	}
	opts.Out = e.out
	return installer.RemoveUpdateHelper(ctx, opts)
}

// orConfigDir is the configuration directory a flag names, or the default.
func orConfigDir(dir string) string {
	if dir != "" {
		return dir
	}
	return config.ConfigDir()
}

func updatesHelperRun(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper run",
		`Answer the request in the update folder: check it against the helper's limits, run "zoomies upgrade --version <tag> --non-interactive --config-dir <dir>" for it, with the deployment's configuration directory, and write result.json saying how it went. The zoomies-update unit runs it as root when a request arrives. It takes no flags: where the folder is, whose it is and which binary to run come from root's copy of the pointer in `+installer.UpdateHelperStateDir+`, which only root can write.`)
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	if err := installer.CheckUpdateHelperPlatform(); err != nil {
		return err
	}
	if uid := updatesEUID(); uid != 0 {
		return fmt.Errorf(`the update helper runs as root, started by the zoomies-update unit when the service asks for an update, and this is uid %d; to update this host by hand, run "sudo zoomies upgrade"`, uid)
	}
	opts, err := installer.HelperOptionsFromPointer(updateHelperStateDir)
	if err != nil {
		return err
	}
	opts.Out = e.out
	return installer.RunUpdateHelper(ctx, opts)
}

func updatesHelperStatus(_ context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper status",
		"Where this host's update folder is, whether the helper is installed, and the last result it wrote with the end of its log. It reads root's copy of the pointer in "+installer.UpdateHelperStateDir+" when it can and the one beside the configuration file when it cannot, so it works without root wherever those can be read.")
	flags.example("sudo zoomies updates helper status")
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	p, source, err := helperPointer()
	if err != nil {
		return err
	}
	if source == "" {
		fmt.Fprintf(e.out, "The update helper is not installed on this host: there is no %s in %s or %s. Install it with \"sudo zoomies updates helper install\".\n",
			channel.PointerFile, updateHelperStateDir, config.ConfigDir())
		return nil
	}

	// Everything below the pointer was written by the service, or could have
	// been, so it is printed as text and never as terminal control.
	row := func(label, value string) { fmt.Fprintf(e.out, "%-14s %s\n", label, plain(value)) }
	row("Update folder", p.Dir)
	row("Recorded in", source)
	row("Account", fmt.Sprintf("%s (uid %d)", p.Account, p.UID))
	row("Binary", p.Binary)

	switch m, found, err := channel.ReadMarker(p.Dir); {
	case err != nil:
		row("Helper", "cannot read "+channel.MarkerFile+": "+err.Error())
	case !found:
		row("Helper", "not installed: there is no "+channel.MarkerFile+" in the folder; install it with \"sudo zoomies updates helper install\"")
	default:
		row("Helper", fmt.Sprintf("installed %s, by zoomies %s", m.InstalledAt.UTC().Format(time.RFC3339), m.Version))
	}

	r, found, err := channel.ReadResult(p.Dir)
	switch {
	case err != nil:
		row("Last result", "cannot read "+channel.ResultFile+": "+err.Error())
		return nil
	case !found:
		row("Last result", "none yet")
		return nil
	}
	var outcome string
	switch {
	case r.OK && r.To != "":
		outcome = fmt.Sprintf("updated from %s to %s", r.From, r.To)
	case r.OK:
		outcome = fmt.Sprintf("%s was already installed (%s), so nothing ran", r.Tag, r.From)
	default:
		outcome = "failed: " + r.Error
	}
	label := r.Tag
	if r.ID != "" {
		label = r.ID + " for " + r.Tag
	}
	if label == "" {
		label = "a request the helper could not read"
	}
	row("Last result", label+": "+outcome)
	row("Finished", r.FinishedAt.UTC().Format(time.RFC3339))
	if tail := strings.TrimRight(r.LogTail, "\n"); tail != "" {
		lines := strings.Split(tail, "\n")
		if len(lines) > helperStatusTailLines {
			lines = lines[len(lines)-helperStatusTailLines:]
		}
		fmt.Fprintln(e.out, "Log tail:")
		for _, line := range lines {
			fmt.Fprintf(e.out, "  %s\n", plain(line))
		}
	}
	return nil
}

// helperPointer finds the pointer to the update folder and says which file it
// read. Root's copy comes first, because only root can have written it; an
// account that cannot read it reads the copy beside the configuration file,
// which the installer writes too. No pointer at all is an empty source and no
// error: the helper is not installed.
func helperPointer() (channel.Pointer, string, error) {
	var denied error
	for _, dir := range []string{updateHelperStateDir, config.ConfigDir()} {
		path := filepath.Join(dir, channel.PointerFile)
		p, err := channel.ReadPointer(path)
		switch {
		case err == nil:
			return p, path, nil
		case errors.Is(err, fs.ErrNotExist):
		case errors.Is(err, fs.ErrPermission):
			denied = err
		default:
			return channel.Pointer{}, "", fmt.Errorf("cannot read %s: %w", path, err)
		}
	}
	if denied != nil {
		return channel.Pointer{}, "", fmt.Errorf(`%w; run "sudo zoomies updates helper status" to read root's copy`, denied)
	}
	return channel.Pointer{}, "", nil
}
