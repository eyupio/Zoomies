package hosttune

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

type TuneOptions struct {
	Tier      Tier
	Dedicated bool
	Yes       bool
	DryRun    bool
	Revert    bool
	// Force is the operator asking for the maintenance restart: take this host
	// out of service, wait for its jobs to finish, restart Docker, and bring the
	// host back (MaintainDocker). Without it a Docker restart is only ever done
	// on a host that is already idle, and is otherwise left pending.
	Force bool
	// Wait and KillRunning are Force's patience and what it does when that runs
	// out; see MaintenanceOptions. KillRunning without Force does nothing.
	Wait        time.Duration
	KillRunning bool
	// Background, with Force, hands the restart to a background task that keeps
	// trying until the host is quiet instead of waiting here (RestartWhenSafe);
	// BackgroundCommand is the command that task runs, which the caller builds
	// because only it knows where its own binary is. GiveUp is how long the task
	// keeps trying. RestartPending is that task: it does nothing but the Docker
	// restart a previous tune left pending, and never reviews or applies anything,
	// so a task started with approval cannot approve what nobody was asked about.
	Background        bool
	BackgroundCommand []string
	GiveUp            time.Duration
	RestartPending    bool
	Only, Skip        map[string]bool
	In                io.Reader
	Out               io.Writer
	Actor             string
}

func IDs(s string) map[string]bool {
	m := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			m[id] = true
		}
	}
	return m
}

// Tune is shared by the CLI and installer. Upgrade has no call to this method.
func (e *Engine) Tune(ctx context.Context, o TuneOptions) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.In == nil {
		o.In = strings.NewReader("")
	}
	if o.Tier == "" {
		o.Tier = Safe
	}
	if !e.Supported() {
		return fmt.Errorf("tune supports %s on Linux; this host is %s on %s, so it is report-only", SupportedPlatforms(), e.Platform(), e.OS)
	}
	if e.Container {
		return fmt.Errorf("run tune on the outer host, not inside a container or LXC")
	}
	if e.UID != 0 {
		return fmt.Errorf("root is required; review with doctor, then run sudo zoomies tune")
	}
	if o.RestartPending {
		return e.RestartWhenSafe(ctx, WhenSafeOptions{GiveUp: o.GiveUp, Out: o.Out})
	}
	for id := range o.Only {
		if c, ok := e.Check(id); ok && !o.Revert && ((c.Tier == Dedicated && !o.Dedicated) || (c.Tier == Aggressive && o.Tier == Safe && !o.Dedicated)) {
			return fmt.Errorf("%s requires --tier aggressive or --dedicated as appropriate", id)
		}
		if _, ok := e.Check(id); !ok {
			return fmt.Errorf("unknown check ID %q", id)
		}
	}
	for id := range o.Skip {
		if _, ok := e.Check(id); !ok {
			return fmt.Errorf("unknown check ID %q", id)
		}
	}
	scanner := bufio.NewScanner(o.In)
	ask := func(prompt string) bool {
		fmt.Fprint(o.Out, prompt)
		if !scanner.Scan() {
			fmt.Fprintln(o.Out, "No answer; left unchanged.")
			return false
		}
		return strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") || strings.EqualFold(strings.TrimSpace(scanner.Text()), "yes")
	}
	if o.Dedicated && !o.Revert && !o.DryRun && !o.Yes {
		fmt.Fprintln(o.Out, "Dedicated tuning is only for hosts that run nothing but Zoomies. It disables services and can affect other software.")
		fmt.Fprint(o.Out, "Type THIS HOST RUNS ONLY ZOOMIES to continue: ")
		if !scanner.Scan() || scanner.Text() != "THIS HOST RUNS ONLY ZOOMIES" {
			return fmt.Errorf("dedicated host confirmation was not given; no changes applied")
		}
	}
	restart := false
	applied, declined, reviewed, advice := 0, 0, 0, 0
	if o.Revert {
		changes, err := e.Revert(ctx, o.Only, o.Skip, true)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			fmt.Fprintln(o.Out, "No recorded changes to revert.")
			return nil
		}
		for _, c := range changes {
			fmt.Fprintf(o.Out, "Revert %s (applied by %s at %s)\n", c.ID, c.Actor, c.At)
			for _, f := range c.Files {
				reverse := Change{Files: []FileChange{{Path: f.Path, Before: Snapshot{Exists: true, Data: f.After}, After: f.Before.Data}}}
				fmt.Fprint(o.Out, reverse.Preview())
				if !f.Before.Exists {
					fmt.Fprintln(o.Out, "Remove the Zoomies-created file:", f.Path)
				}
			}
			for _, op := range c.Operations {
				fmt.Fprintf(o.Out, "restore runtime value or command: %s %q\n", op.Previous, op.Undo)
			}
			restart = restart || c.DockerRestart
		}
		if o.DryRun {
			fmt.Fprintf(o.Out, "\nPreview complete: %d recorded changes to restore. No changes made.\n", len(changes))
			return nil
		}
		if !o.Yes && !ask("Restore these recorded changes? [y/N] ") {
			return nil
		}
		if _, err = e.Revert(ctx, o.Only, o.Skip, false); err != nil {
			return err
		}
		applied = len(changes)
	} else {
		tier := o.Tier
		if o.Dedicated {
			tier = Dedicated
		}
		report := e.Run(ctx, tier)
		for _, r := range report.Results {
			if o.Skip[r.ID] || (len(o.Only) > 0 && !o.Only[r.ID]) {
				continue
			}
			if r.Status != Warn {
				continue
			}
			if r.Optional && !o.Only[r.ID] {
				advice++
				continue
			}
			if !r.Actionable {
				advice++
				continue
			}
			c, err := e.Plan(ctx, r)
			if err != nil {
				return err
			}
			reviewed++
			fmt.Fprintf(o.Out, "\n%d. %s (%s)\n   %s -> %s\n   %s\n", reviewed, r.Title, r.ID, c.Previous, c.New, r.Rationale)
			fmt.Fprint(o.Out, c.Preview())
			if o.DryRun {
				continue
			}
			if !o.Yes && !ask("Apply this change? [y/N] ") {
				declined++
				fmt.Fprintln(o.Out, "Left unchanged.")
				continue
			}
			if err = e.Apply(ctx, c, o.Actor); err != nil {
				return fmt.Errorf("%s: %w; the recorded change can be recovered with --revert", r.ID, err)
			}
			restart = restart || c.DockerRestart
			applied++
			fmt.Fprintln(o.Out, "Applied; reversal recorded.")
		}
	}
	summarise := func() {
		if o.DryRun {
			fmt.Fprintf(o.Out, "\nPreview complete: %d eligible changes. No changes made.\n", reviewed)
		} else if o.Revert {
			fmt.Fprintf(o.Out, "\nRestored %d recorded changes.\n", applied)
		} else {
			fmt.Fprintf(o.Out, "\nTuning complete: %d applied, %d left unchanged.\n", applied, declined)
		}
		if advice > 0 {
			fmt.Fprintf(o.Out, "%d findings need manual review or explicit selection; see zoomies doctor --verbose.\n", advice)
		}
	}
	// A re-run can finish a restart the operator previously deferred.
	if !restart && !o.DryRun {
		s, err := e.LoadState()
		if err != nil {
			return err
		}
		restart = s.DockerRestartPending
	}
	if restart && !o.DryRun && o.Force && o.Background {
		fmt.Fprintln(o.Out, "Docker needs a restart. New log settings apply to newly created containers.")
		fmt.Fprintf(o.Out, "Background restart: a task that keeps this host in service and keeps looking for a moment with no containers running, then stops this host's Zoomies services, restarts Docker and starts them again. It gives up after %s and the change stays pending. It never stops a job.\n", giveUp(o.GiveUp))
		if o.Yes || ask("Start the background task? [y/N] ") {
			if err := e.StartBackground(ctx, o.BackgroundCommand); err != nil {
				summarise()
				return err
			}
			fmt.Fprintf(o.Out, "Started %s. Follow it with: journalctl -u %s -f\nStop it with: systemctl stop %s\n", BackgroundUnit, BackgroundUnit, BackgroundUnit)
		}
	} else if restart && !o.DryRun && o.Force {
		fmt.Fprintln(o.Out, "Docker needs a restart. New log settings apply to newly created containers.")
		fmt.Fprintf(o.Out, "Maintenance restart: stop this host's Zoomies services so nothing new starts, wait up to %s for running jobs to finish, restart Docker, then start the services again.\n", maintenanceWait(o.Wait))
		if o.KillRunning {
			fmt.Fprintln(o.Out, "--kill-running: containers still running when that time is up are stopped, which ends their jobs.")
		} else {
			fmt.Fprintln(o.Out, "Jobs are never stopped without --kill-running: a host still busy afterwards is put back and the restart stays pending.")
		}
		if o.Yes || ask("Take this host out of service for the restart? [y/N] ") {
			if err := e.MaintainDocker(ctx, MaintenanceOptions{Wait: o.Wait, KillRunning: o.KillRunning, Out: o.Out}); err != nil {
				summarise()
				return fmt.Errorf("maintenance restart: %w", err)
			}
			fmt.Fprintln(o.Out, "Maintenance complete; the host is back in service.")
		}
	} else if restart && !o.DryRun {
		fmt.Fprintln(o.Out, "Docker needs a restart. New log settings apply to newly created containers.")
		if o.Yes || ask("Restart Docker now, if this host is idle? [y/N] ") {
			if err := e.RestartDocker(ctx); err != nil {
				fmt.Fprintln(o.Out, "Docker was not restarted:", err)
				fmt.Fprintln(o.Out, "Drain the host, stop its Zoomies agent, and restart Docker during maintenance.")
				summarise()
				return nil
			}
			fmt.Fprintln(o.Out, "Docker restarted.")
		}
	}
	summarise()
	return nil
}

// giveUp is what an unset GiveUp means, said the same way everywhere.
func giveUp(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultGiveUp
	}
	return d
}

// maintenanceWait is what an unset Wait means, said the same way everywhere.
func maintenanceWait(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultMaintenanceWait
	}
	return d
}

// RestartDocker refuses all running containers (including unrelated ones),
// and active native Zoomies services, so no new jobs can be admitted between
// checking container activity and restarting the daemon. --force never bypasses
// this guard; MaintainDocker is how an operator asks for a restart on a host that
// is not idle, by taking it out of service first. An inaccessible runtime is unknown, not evidence of an idle host.
func (e *Engine) RestartDocker(ctx context.Context) error {
	if e.DockerHost != "" && e.DockerHost != "unix:///var/run/docker.sock" {
		return fmt.Errorf("custom or rootless Docker endpoint; restart it manually after draining")
	}
	for _, u := range []string{"zoomies.service", "zoomies-agent.service"} {
		v, err := command(ctx, e, "systemctl", "show", u, "--property=ActiveState", "--value")
		if err != nil {
			return fmt.Errorf("cannot establish whether %s is accepting jobs", u)
		}
		if v != "inactive" && v != "failed" && v != "" {
			return fmt.Errorf("%s is still active; drain and stop it first", u)
		}
	}
	v, err := command(ctx, e, "docker", "ps", "-q")
	if err != nil {
		return fmt.Errorf("cannot check running containers")
	}
	if strings.TrimSpace(v) != "" {
		return fmt.Errorf("containers are running; restart is refused even with --force")
	}
	_, err = command(ctx, e, "systemctl", "restart", "docker.service")
	if err != nil {
		return err
	}
	unlock, err := e.System.Lock(StateDir + "/lock")
	if err != nil {
		return err
	}
	defer unlock()
	s, err := e.LoadState()
	if err != nil {
		return err
	}
	s.DockerRestartPending = false
	return e.saveState(s)
}
