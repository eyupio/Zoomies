package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/installer"
)

func runTune(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies tune [flags]", "Review and apply host tuning with recorded reversals. Defaults to safe changes with per-item confirmation.")
	tier := fs.String("tier", "safe", "safe or aggressive")
	dedicated := fs.Bool("dedicated", false, "include dedicated-host checks; requires typed confirmation or --yes")
	dry := fs.Bool("dry-run", false, "print exact file changes and commands without writing anything")
	yes := fs.Bool("yes", false, "explicitly approve selected changes without prompts")
	only := fs.String("only", "", "comma-separated check IDs; optional changes must be selected explicitly")
	skip := fs.String("skip", "", "comma-separated check IDs to leave unchanged")
	revert := fs.Bool("revert", false, "restore recorded previous values and files")
	force := fs.Bool("force", false, "does not override active-job, dependency or managed-configuration guards")
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
	err = engine.Tune(ctx, hosttune.TuneOptions{Tier: t, Dedicated: *dedicated, DryRun: *dry, Yes: *yes, Only: hosttune.IDs(*only), Skip: hosttune.IDs(*skip), Revert: *revert, Force: *force, In: e.in, Out: e.out, Actor: actor})
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
