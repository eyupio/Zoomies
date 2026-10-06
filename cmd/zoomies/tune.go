package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/installer"
)

// untilHangup ends a context when the terminal goes away. A dropped SSH session sends
// SIGHUP, and Go's default for it is to exit without running a defer -- so a maintenance
// restart that had stopped the agent and the controller was left holding them stopped,
// and a lock behind. A hung-up terminal is treated as ctrl-C, which already unwinds
// through the restore; a closed output pipe must not kill it either.
func untilHangup(ctx context.Context) (context.Context, context.CancelFunc) {
	signal.Ignore(syscall.SIGPIPE)
	return signal.NotifyContext(ctx, syscall.SIGHUP)
}

func runTune(ctx context.Context, e *env, args []string) error {
	ctx, stopHup := untilHangup(ctx)
	defer stopHup()
	fs := newFlagSet(e, "zoomies tune [flags]", "Review and apply host tuning with recorded reversals. Defaults to safe changes with per-item confirmation.")
	tier := fs.String("tier", "safe", "safe or aggressive")
	dedicated := fs.Bool("dedicated", false, "include dedicated-host checks; requires typed confirmation or --yes")
	dry := fs.Bool("dry-run", false, "print exact file changes and commands without writing anything")
	yes := fs.Bool("yes", false, "explicitly approve selected changes without prompts")
	only := fs.String("only", "", "comma-separated check IDs; optional changes must be selected explicitly")
	skip := fs.String("skip", "", "comma-separated check IDs to leave unchanged")
	revert := fs.Bool("revert", false, "restore recorded previous values and files")
	force := fs.Bool("force", false, "maintenance restart: where a change needs Docker restarted, stop this host's Zoomies services, wait for running jobs to finish (--wait), restart Docker, and start the services again. Never overrides dependency or managed-configuration guards")
	wait := fs.Duration("wait", hosttune.DefaultMaintenanceWait, "with --force, how long running jobs are given to finish before the restart is given up")
	background := fs.Bool("background", false, "with --force: do not wait here; start a background task that keeps this host in service and keeps trying until a moment with no containers running, then restarts Docker (it gives up after --give-up-after, and never stops a job)")
	giveUp := fs.Duration("give-up-after", hosttune.DefaultGiveUp, "with --background or --restart-pending, how long to keep looking for a quiet moment")
	restartPending := fs.Bool("restart-pending", false, "do nothing but the Docker restart a previous tune left pending, retrying until the host is quiet; this is what --background runs, and can be run in a terminal multiplexer on a host without systemd")
	killRunning := fs.Bool("kill-running", false, "with --force, stop containers still running when --wait is over and restart Docker anyway, which ends their jobs")
	cfg := fs.String("config", "", "local Zoomies configuration file")
	work := fs.String("work-dir", "", "host path to runner work directory")
	dockerHost := fs.String("docker-host", "", "Docker socket on this host")
	fs.example("sudo zoomies tune", "sudo zoomies tune --dry-run", "sudo zoomies tune --revert", "sudo zoomies tune --dedicated --only service.snapd")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if (fs.changed("wait") || *killRunning) && !*force {
		return usagef("tune", "--wait and --kill-running only apply to a maintenance restart; add --force")
	}
	if *background && !*force {
		return usagef("tune", "--background hands a maintenance restart to a background task; add --force")
	}
	if (*background || *restartPending) && (*killRunning || fs.changed("wait")) {
		return usagef("tune", "a background restart waits for a quiet host and never stops a job, so --kill-running and --wait do not apply to it")
	}
	if fs.changed("give-up-after") && !*background && !*restartPending {
		return usagef("tune", "--give-up-after applies to --background and --restart-pending")
	}
	if *restartPending && (*revert || *dedicated || *force || *background || *only != "" || *skip != "") {
		return usagef("tune", "--restart-pending only restarts Docker for a change already made; it cannot be combined with --revert, --dedicated, --force, --background, --only or --skip")
	}
	if (*force || *background || *restartPending) && !*dry {
		// A maintenance restart takes a host out of service by stopping the Zoomies
		// systemd units, and a container deployment has none: the controller is a
		// container, which the wait counted as running work and --kill-running stopped and
		// never started again, and a pending restart could never find a quiet moment.
		if rec, ok := installer.ReadDeploymentRecord(config.ConfigDir()); ok && rec.Deployment.Containerised() {
			name := firstNonBlank(rec.Container, "the zoomies container")
			return fmt.Errorf("this host runs Zoomies in a container (%s), and a maintenance restart takes a host out of service by stopping systemd units that it does not have; "+
				"with --kill-running it would stop that container and not start it again. Restart Docker yourself when the host is quiet: stop the container (docker stop %s), "+
				"restart Docker (systemctl restart docker), then start it again (docker start %s)", name, name, name)
		}
	}
	t, err := tierValue(*tier)
	if err != nil || t == hosttune.Dedicated {
		return usagef("tune", "use --tier safe|aggressive, and --dedicated for dedicated hosts")
	}
	engine, err := localDoctor(*cfg)
	if err != nil {
		return err
	}
	if *work != "" {
		engine.WorkDir = *work
	}
	if *dockerHost != "" {
		engine.DockerHost = *dockerHost
	}
	actor := "root"
	if u, err := user.Current(); err == nil {
		actor = u.Username
		if id := os.Getenv("SUDO_UID"); id != "" {
			if caller, err := user.LookupId(id); err == nil {
				actor = caller.Username + " (sudo as " + u.Username + ")"
			}
		}
	}
	ui := installer.PaletteFor(e.out)
	action := "Review changes"
	if *dry {
		action = "Preview only"
	} else if *revert {
		action = "Restore recorded settings"
	}
	label := string(t)
	if *dedicated {
		label = "dedicated"
	}
	ui.Title(e.out, "Host tuning", label+" -- "+action)
	ui.Hint(e.out, "Every applied change has a reversal record: zoomies tune --revert")
	err = engine.Tune(ctx, hosttune.TuneOptions{Tier: t, Dedicated: *dedicated, DryRun: *dry, Yes: *yes, Only: hosttune.IDs(*only), Skip: hosttune.IDs(*skip), Revert: *revert, Force: *force, Wait: *wait, KillRunning: *killRunning,
		Background: *background, GiveUp: *giveUp, RestartPending: *restartPending, BackgroundCommand: backgroundCommand(*cfg, *work, *dockerHost, *giveUp),
		In: e.in, Out: e.out, Actor: actor})
	if err == nil && !*dry {
		r := engine.Run(ctx, hosttune.Dedicated)
		if _, statErr := engine.System.Stat(engine.WorkDir); statErr == nil {
			if b, encodeErr := json.Marshal(r); encodeErr == nil {
				_ = engine.System.WriteFile(filepath.Join(engine.WorkDir, hosttune.ReportFile), b, 0640)
			}
		}
		path := filepath.Join(config.SharedDir(), "host-health", "report.json")
		if _, statErr := engine.System.Stat(path); statErr == nil {
			if b, encodeErr := json.Marshal(r); encodeErr == nil {
				if writeErr := engine.System.WriteFile(path, append(b, '\n'), 0644); writeErr != nil {
					fmt.Fprintln(e.err, "Health report will refresh at the next daemon check:", writeErr)
				}
			}
		}
	}
	return err
}

// backgroundCommand is what the background task runs: this binary, asked for
// nothing but the pending restart. It carries the settings that say which host
// and which Docker, and never --yes, because the task has nothing to approve.
func backgroundCommand(cfg, work, dockerHost string, giveUp time.Duration) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = "zoomies"
	}
	args := []string{exe, "tune", "--restart-pending", "--give-up-after", giveUp.String()}
	for _, f := range [][2]string{{"--config", cfg}, {"--work-dir", work}, {"--docker-host", dockerHost}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	return args
}
