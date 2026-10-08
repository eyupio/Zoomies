package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	return runGroup(ctx, e, "updates", `Release updates, and the helper that applies them. To upgrade this host by hand, use "zoomies upgrade".`, []*subcommand{
		{"helper", "<run|status>", "The root-owned helper on this host that applies an update", runUpdatesHelper},
	}, args)
}

func runUpdatesHelper(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "updates helper", "The update helper on this host. It is local: it never talks to a controller.", []*subcommand{
		{"run", "", "Answer the request in the update folder; the helper's unit runs it, as root", updatesHelperRun},
		{"status", "", "Where the update folder is, whether the helper is installed, and its last result", updatesHelperStatus},
	}, args)
}

func updatesHelperRun(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper run",
		`Answer the request in the update folder: check it against the helper's limits, run "zoomies upgrade --version <tag> --non-interactive" for it, and write result.json saying how it went. The zoomies-update unit runs it as root when a request arrives. It takes no flags: where the folder is, whose it is and which binary to run come from root's copy of the pointer in `+installer.UpdateHelperStateDir+`, which only root can write.`)
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
