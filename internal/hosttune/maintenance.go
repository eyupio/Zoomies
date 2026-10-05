package hosttune

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

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
			return fmt.Errorf("%d container(s) were still running after %s, so Docker was not restarted and its change stays pending; "+
				"run again when the host is quieter, or add --kill-running to stop them", len(running), o.Wait)
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
