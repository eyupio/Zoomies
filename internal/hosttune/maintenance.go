package hosttune

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrHostBusy is a restart that did not happen because containers were still
// running. It is the one failure that is only a matter of time, which is what
// lets RestartWhenSafe retry it and nothing else.
var ErrHostBusy = errors.New("host busy")

// A change to the Docker daemon's configuration takes effect when the daemon
// restarts, and restarting it ends every container it is running: a runner's job
// is one of them. So tuning never restarted Docker on a host that was doing
// anything, and left the restart pending for an operator to find a quiet moment
// for -- which, on a host that is never quiet, is never.
//
// MaintainDocker is that quiet moment, made on purpose. It takes the host out of
// service so nothing new starts, waits for what is running to finish, restarts
// Docker, and puts the host back as it found it. It is the operator's explicit
// request (--force), never a side effect of tuning or of an upgrade, and it
// never ends a job unless asked to by name (--kill-running).

// MaintenanceOptions says how patient to be and what to do about work that does
// not finish.
type MaintenanceOptions struct {
	// Wait is how long running containers are given to finish once the host is
	// out of service. Zero is DefaultMaintenanceWait.
	Wait time.Duration
	// KillRunning stops what is still running when Wait is over, and restarts
	// Docker anyway. Without it a host that is still busy is put back and the
	// restart is left pending.
	KillRunning bool
	// Poll is how often the wait looks, and Sleep how it waits; both are for tests.
	Poll  time.Duration
	Sleep func(context.Context, time.Duration)
	Out   io.Writer
}

// DefaultMaintenanceWait is long enough for most CI jobs and short enough that an
// operator at a terminal does not walk away from a hung command.
const DefaultMaintenanceWait = 10 * time.Minute

// maintainedServices are the units that admit jobs on this host, in the order
// they are stopped. They are started again in the reverse order.
var maintainedServices = []string{"zoomies-agent.service", "zoomies.service"}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// MaintainDocker restarts Docker on a host taken out of service for the purpose.
//
// Whatever happens after the services are stopped, they are started again if they
// were running: a host left out of service by a failed restart is a worse outcome
// than the restart not happening. It returns an error describing what did not
// work, and says in Out what state the host is in.
func (e *Engine) MaintainDocker(ctx context.Context, o MaintenanceOptions) (err error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Wait <= 0 {
		o.Wait = DefaultMaintenanceWait
	}
	if o.Poll <= 0 {
		o.Poll = 15 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if e.DockerHost != "" && e.DockerHost != "unix:///var/run/docker.sock" {
		return fmt.Errorf("custom or rootless Docker endpoint; restart it manually after draining")
	}
	if e.UID != 0 {
		return fmt.Errorf("root is required")
	}

	// What is running now has to be known before anything is stopped: a service
	// whose state cannot be read is one that might be admitting jobs, and the
	// host is not taken out of service on a guess.
	var was []string
	for _, u := range maintainedServices {
		v, qerr := command(ctx, e, "systemctl", "show", u, "--property=ActiveState", "--value")
		if qerr != nil {
			return fmt.Errorf("cannot establish whether %s is running, so the host is left as it is", u)
		}
		if strings.TrimSpace(v) == "active" || strings.TrimSpace(v) == "activating" {
			was = append(was, u)
		}
	}

	stopped := []string{}
	// Restoring is the one thing that must happen on every path out, including a
	// cancelled context, so it does not use the caller's.
	defer func() {
		rctx := context.WithoutCancel(ctx)
		for i := len(stopped) - 1; i >= 0; i-- {
			u := stopped[i]
			if _, serr := command(rctx, e, "systemctl", "start", u); serr != nil {
				fmt.Fprintf(o.Out, "Could not start %s again: %v. Start it with: systemctl start %s\n", u, serr, u)
				if err == nil {
					err = fmt.Errorf("%s was stopped for maintenance and did not start again", u)
				}
				continue
			}
			fmt.Fprintf(o.Out, "Started %s again.\n", u)
		}
	}()

	for _, u := range was {
		fmt.Fprintf(o.Out, "Stopping %s so nothing new starts.\n", u)
		if _, serr := command(ctx, e, "systemctl", "stop", u); serr != nil {
			return fmt.Errorf("could not stop %s: %w", u, serr)
		}
		stopped = append(stopped, u)
	}

	running, rerr := e.runningContainers(ctx)
	if rerr != nil {
		return rerr
	}
	for polls := int(o.Wait / o.Poll); len(running) > 0 && polls > 0; polls-- {
		fmt.Fprintf(o.Out, "Waiting for %d running container(s) to finish; %s left.\n", len(running), (time.Duration(polls) * o.Poll).Round(time.Second))
		o.Sleep(ctx, o.Poll)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if running, rerr = e.runningContainers(ctx); rerr != nil {
			return rerr
		}
	}
	if len(running) > 0 {
		if !o.KillRunning {
			return fmt.Errorf("%w: %d container(s) were still running after %s, so Docker was not restarted and its change stays pending; "+
				"run again when the host is quieter, or add --kill-running to stop them", ErrHostBusy, len(running), o.Wait)
		}
		fmt.Fprintf(o.Out, "Stopping %d container(s) still running, as asked: %s\n", len(running), strings.Join(running, " "))
		if _, serr := command(ctx, e, "docker", append([]string{"stop"}, running...)...); serr != nil {
			return fmt.Errorf("could not stop the running containers: %w", serr)
		}
	}

	fmt.Fprintln(o.Out, "Restarting Docker.")
	if _, serr := command(ctx, e, "systemctl", "restart", "docker.service"); serr != nil {
		return fmt.Errorf("docker did not restart: %w", serr)
	}
	// Up means answering, which is what a runner needs, not merely a unit that
	// says it started.
	answered := false
	for tries := 0; tries < 12 && !answered; tries++ {
		if _, derr := command(ctx, e, "docker", "info", "--format", "{{.ServerVersion}}"); derr == nil {
			answered = true
			break
		}
		o.Sleep(ctx, 5*time.Second)
	}
	if !answered {
		return fmt.Errorf("docker restarted but is not answering; check it with: systemctl status docker.service")
	}
	fmt.Fprintln(o.Out, "Docker is answering.")

	unlock, lerr := e.System.Lock(StateDir + "/lock")
	if lerr != nil {
		return lerr
	}
	defer unlock()
	s, lerr := e.LoadState()
	if lerr != nil {
		return lerr
	}
	s.DockerRestartPending = false
	return e.saveState(s)
}

// runningContainers lists every running container's ID, the host's own and other
// people's alike: a restart ends them all, so all of them are waited for.
func (e *Engine) runningContainers(ctx context.Context) ([]string, error) {
	v, err := command(ctx, e, "docker", "ps", "-q")
	if err != nil {
		return nil, fmt.Errorf("cannot check running containers, so Docker is not restarted")
	}
	return strings.Fields(v), nil
}

// WhenSafeOptions is how RestartWhenSafe goes about waiting for a quiet host.
type WhenSafeOptions struct {
	// GiveUp is how long to keep trying. Zero is DefaultGiveUp.
	GiveUp time.Duration
	// Poll is how often to look for a quiet host; Settle is how long the brief
	// out-of-service window is given to confirm it. Both are for tests as much
	// as for operators.
	Poll, Settle time.Duration
	Sleep        func(context.Context, time.Duration)
	Out          io.Writer
}

// DefaultGiveUp is a day: long enough for a host with a quiet night, short enough
// that a background task nobody remembers does not run for ever.
const DefaultGiveUp = 24 * time.Hour

// RestartWhenSafe keeps trying to restart Docker until the host is quiet, and
// leaves the host in service while it waits.
//
// That is the difference from MaintainDocker, which takes the host out of service
// and waits for the work to drain. On a host that is busy for hours, draining is
// hours of queued jobs; waiting for the gaps that ephemeral runners leave between
// jobs costs nothing until one appears. When it does, the host is taken out of
// service for as long as the restart needs, a job that arrived in that moment puts
// everything back and the loop carries on, and nothing is ever stopped to make
// room. It restarts only what a previous tune left pending, and does nothing when
// nothing is.
func (e *Engine) RestartWhenSafe(ctx context.Context, o WhenSafeOptions) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.GiveUp <= 0 {
		o.GiveUp = DefaultGiveUp
	}
	if o.Poll <= 0 {
		o.Poll = 30 * time.Second
	}
	if o.Settle <= 0 {
		o.Settle = 30 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	attempts := max(int(o.GiveUp/o.Poll), 1)
	for i := 0; i < attempts; i++ {
		s, err := e.LoadState()
		if err != nil {
			return err
		}
		if !s.DockerRestartPending {
			fmt.Fprintln(o.Out, "No Docker restart is pending.")
			return nil
		}
		running, err := e.runningContainers(ctx)
		if err != nil {
			return err
		}
		if len(running) == 0 {
			fmt.Fprintln(o.Out, "The host is quiet; restarting Docker.")
			err := e.MaintainDocker(ctx, MaintenanceOptions{Wait: o.Settle, Poll: min(o.Poll, o.Settle), Out: o.Out, Sleep: o.Sleep})
			if err == nil {
				return nil
			}
			if !errors.Is(err, ErrHostBusy) {
				return err
			}
			fmt.Fprintln(o.Out, "A job started in that moment; the host is back in service and the restart will be tried again.")
		} else if i%20 == 0 {
			// Said now and then, not at every poll: a day of "still busy" is a log
			// nobody reads.
			fmt.Fprintf(o.Out, "Waiting for a quiet moment: %d container(s) running.\n", len(running))
		}
		o.Sleep(ctx, o.Poll)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("%w: no quiet moment in %s, so Docker was not restarted; its change stays pending and the next tune can try again", ErrHostBusy, o.GiveUp)
}

// BackgroundUnit is the transient unit a background restart runs as. One name, so
// a second request while one is waiting is refused instead of started beside it.
const BackgroundUnit = "zoomies-docker-restart.service"

// StartBackground runs a command as a transient systemd unit of its own, which is
// the only way it can outlive both the terminal and the services it will stop: a
// child of zoomies-agent.service would be stopped with it.
func (e *Engine) StartBackground(ctx context.Context, args []string) error {
	if _, err := e.System.Stat("/run/systemd/system"); err != nil {
		return fmt.Errorf("this host does not run systemd, so there is nowhere to keep a background task; run `zoomies tune --restart-pending` in a terminal multiplexer instead")
	}
	if v, err := command(ctx, e, "systemctl", "is-active", BackgroundUnit); err == nil && strings.TrimSpace(v) == "active" {
		return fmt.Errorf("a background restart is already waiting (%s); follow it with: journalctl -u %s -f", BackgroundUnit, BackgroundUnit)
	}
	run := append([]string{"--quiet", "--collect", "--unit", strings.TrimSuffix(BackgroundUnit, ".service"),
		"--description", "Restart Docker for a Zoomies host tuning change, when the host is quiet"}, args...)
	if _, err := command(ctx, e, "systemd-run", run...); err != nil {
		return fmt.Errorf("could not start the background task: %w", err)
	}
	return nil
}
