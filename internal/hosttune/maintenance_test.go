package hosttune

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedHost is a host whose services and containers behave: stopping a unit
// makes it inactive, starting it makes it active, and `docker ps` answers from a
// script so a test can have a job finish after a few looks.
type scriptedHost struct {
	*fakeSystem
	active      map[string]string
	ps          [][]string
	psCalls     int
	failRestart bool
	unitActive  bool
	killed      bool
	log         []string
}

func (s *scriptedHost) Run(_ context.Context, n string, a ...string) (string, error) {
	k := n + " " + strings.Join(a, " ")
	s.log = append(s.log, k)
	switch {
	case n == "systemctl" && len(a) >= 2 && a[0] == "show":
		v, ok := s.active[a[1]]
		if !ok {
			return "", errors.New("unit state unknown")
		}
		return v, nil
	case n == "systemctl" && len(a) == 2 && a[0] == "stop":
		s.active[a[1]] = "inactive"
	case n == "systemctl" && len(a) == 2 && a[0] == "start":
		s.active[a[1]] = "active"
	case n == "systemctl" && len(a) == 2 && a[0] == "restart" && a[1] == "docker.service":
		if s.failRestart {
			return "", errors.New("docker failed to start")
		}
	case k == "systemctl is-active "+BackgroundUnit:
		if s.unitActive {
			return "active", nil
		}
		return "inactive", errors.New("inactive")
	case k == "docker ps -q":
		if s.killed {
			return "", nil
		}
		i := min(s.psCalls, len(s.ps)-1)
		s.psCalls++
		return strings.Join(s.ps[i], "\n"), nil
	case n == "docker" && len(a) > 0 && a[0] == "stop":
		s.killed = true
	case n == "docker" && len(a) > 0 && a[0] == "info":
		return "27.1.1", nil
	}
	return "", nil
}

func (s *scriptedHost) did(prefix string) int {
	n := 0
	for _, c := range s.log {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (s *scriptedHost) index(call string) int {
	for i, c := range s.log {
		if c == call {
			return i
		}
	}
	return -1
}

func maintenanceHost(ps ...[]string) (*Engine, *scriptedHost) {
	f := fake()
	f.put("/etc/os-release", "ID=ubuntu\nVERSION_ID=24.04\n")
	s := &scriptedHost{fakeSystem: f, ps: ps, active: map[string]string{"zoomies.service": "active", "zoomies-agent.service": "active"}}
	e := New(Options{System: s, OS: "linux", UID: 0, Now: func() time.Time { return time.Unix(1234, 0) }, WorkDir: "/work"})
	_ = e.saveState(State{Version: 1, DockerRestartPending: true, Changes: []Change{}})
	return e, s
}

func opts(kill bool) (MaintenanceOptions, *bytes.Buffer) {
	var out bytes.Buffer
	return MaintenanceOptions{Wait: 3 * time.Minute, Poll: time.Minute, KillRunning: kill, Sleep: func(context.Context, time.Duration) {}, Out: &out}, &out
}

func pending(t *testing.T, e *Engine) bool {
	t.Helper()
	s, err := e.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return s.DockerRestartPending
}

// The whole sequence, in order: nothing new is admitted before the wait, Docker is
// restarted only once the host is quiet, and the host comes back the way it was --
// the services started in the reverse of the order they were stopped.
func TestMaintenanceTakesTheHostOutRestartsDockerAndPutsItBack(t *testing.T) {
	e, s := maintenanceHost([]string{"job-1"}, []string{"job-1"}, nil)
	o, out := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err != nil {
		t.Fatalf("MaintainDocker: %v\n%s", err, out)
	}
	order := []string{
		"systemctl stop zoomies-agent.service", "systemctl stop zoomies.service",
		"systemctl restart docker.service",
		"systemctl start zoomies.service", "systemctl start zoomies-agent.service",
	}
	last := -1
	for _, c := range order {
		i := s.index(c)
		if i <= last {
			t.Fatalf("%q ran at %d, want after %d; calls: %v", c, i, last, s.log)
		}
		last = i
	}
	if s.active["zoomies.service"] != "active" || s.active["zoomies-agent.service"] != "active" {
		t.Errorf("the host was not put back: %v", s.active)
	}
	if pending(t, e) {
		t.Error("a restart that happened is still recorded as pending")
	}
}

// A job is never ended unless the operator said so by name. A host still busy when
// the wait is over is put back, and the restart stays pending for another try.
func TestMaintenanceNeverEndsJobsUnlessAsked(t *testing.T) {
	e, s := maintenanceHost([]string{"job-1"})
	o, _ := opts(false)
	err := e.MaintainDocker(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "--kill-running") {
		t.Fatalf("a busy host must be refused with the way forward, got %v", err)
	}
	if s.did("systemctl restart docker") != 0 || s.did("docker stop") != 0 {
		t.Errorf("Docker was touched on a busy host: %v", s.log)
	}
	if s.active["zoomies.service"] != "active" || s.active["zoomies-agent.service"] != "active" {
		t.Errorf("a refused maintenance left the host out of service: %v", s.active)
	}
	if !pending(t, e) {
		t.Error("a restart that did not happen must stay pending")
	}
}

func TestKillRunningStopsWhatIsLeftAndStillPutsTheHostBack(t *testing.T) {
	e, s := maintenanceHost([]string{"job-1", "job-2"})
	o, out := opts(true)
	if err := e.MaintainDocker(context.Background(), o); err != nil {
		t.Fatalf("MaintainDocker: %v", err)
	}
	if s.did("docker stop job-1 job-2") != 1 || s.did("systemctl restart docker") != 1 {
		t.Errorf("the running containers must be stopped and Docker restarted: %v", s.log)
	}
	if !strings.Contains(out.String(), "job-1 job-2") {
		t.Errorf("the containers it stops must be named:\n%s", out)
	}
	if s.active["zoomies.service"] != "active" || s.active["zoomies-agent.service"] != "active" {
		t.Errorf("the host was not put back: %v", s.active)
	}
}

// A host left out of service by a failed restart is worse than a restart that did
// not happen, so the services come back whatever went wrong.
func TestAFailedRestartStillBringsTheServicesBack(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.failRestart = true
	o, _ := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err == nil {
		t.Fatal("a failed restart must be an error")
	}
	if s.active["zoomies.service"] != "active" || s.active["zoomies-agent.service"] != "active" {
		t.Errorf("the host was left out of service: %v", s.active)
	}
	if !pending(t, e) {
		t.Error("a restart that failed must stay pending")
	}
}

// Only what was running is started again: an agent-only host, or a controller
// whose agent an operator had stopped, is not given services it did not have.
func TestOnlyWhatWasRunningIsStartedAgain(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.active["zoomies-agent.service"] = "inactive"
	o, _ := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if s.did("systemctl stop zoomies-agent") != 0 || s.did("systemctl start zoomies-agent") != 0 {
		t.Errorf("an agent that was not running was stopped or started: %v", s.log)
	}
	if s.active["zoomies.service"] != "active" {
		t.Error("the controller that was running must be running again")
	}
}

// A service whose state cannot be read might be admitting jobs, and the host is
// not taken out of service on a guess.
func TestMaintenanceChangesNothingWhenAServiceCannotBeRead(t *testing.T) {
	e, s := maintenanceHost(nil)
	delete(s.active, "zoomies.service")
	o, _ := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err == nil {
		t.Fatal("an unreadable service must stop the maintenance before it starts")
	}
	if s.did("systemctl stop") != 0 || s.did("systemctl restart") != 0 {
		t.Errorf("something was changed on a guess: %v", s.log)
	}
}

// --force is the opt-in, and only it: the same pending restart on a busy host is
// refused as it always was when it is not given.
func TestTuneOnlyTakesTheHostOutOfServiceWithForce(t *testing.T) {
	for _, force := range []bool{false, true} {
		e, s := maintenanceHost(nil)
		var out bytes.Buffer
		err := e.Tune(context.Background(), TuneOptions{Yes: true, Force: force, Wait: time.Minute, Only: IDs("inotify.watches"), Out: &out, In: strings.NewReader(""), Actor: "test"})
		if err != nil && !force {
			t.Fatalf("tune without --force: %v", err)
		}
		if force {
			if err != nil {
				t.Fatalf("tune --force: %v\n%s", err, out.String())
			}
			if s.did("systemctl stop zoomies") != 2 || s.did("systemctl start zoomies") != 2 || s.did("systemctl restart docker") != 1 {
				t.Errorf("--force must run the maintenance restart: %v", s.log)
			}
		} else if s.did("systemctl stop") != 0 {
			t.Errorf("a plain tune stopped the host's services: %v", s.log)
		}
	}
}

func whenSafe(giveUp time.Duration) (WhenSafeOptions, *bytes.Buffer) {
	var out bytes.Buffer
	return WhenSafeOptions{GiveUp: giveUp, Poll: time.Minute, Settle: time.Minute, Sleep: func(context.Context, time.Duration) {}, Out: &out}, &out
}

// The background task leaves the host in service while it waits: nothing is
// stopped until there is a moment with nothing running, and then only for as long
// as the restart takes.
func TestWhenSafeWaitsInServiceForAQuietMoment(t *testing.T) {
	e, s := maintenanceHost([]string{"job-1"}, []string{"job-1"}, nil)
	o, _ := whenSafe(time.Hour)
	if err := e.RestartWhenSafe(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if s.did("systemctl restart docker") != 1 {
		t.Fatalf("Docker was not restarted once the host was quiet: %v", s.log)
	}
	if stop, ps := s.index("systemctl stop zoomies-agent.service"), s.index("docker ps -q"); stop < ps {
		t.Errorf("the services were stopped before the host was seen to be quiet: %v", s.log)
	}
	if pending(t, e) {
		t.Error("the restart that happened is still pending")
	}
}

// A job that starts in the moment the services are being stopped puts everything
// back, and the loop carries on instead of giving up or forcing the restart.
func TestWhenSafeBacksOffWhenAJobArrivesInTheWindow(t *testing.T) {
	e, s := maintenanceHost(nil, []string{"job-1"}, []string{"job-1"}, nil, nil)
	o, out := whenSafe(time.Hour)
	if err := e.RestartWhenSafe(context.Background(), o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if s.did("systemctl stop zoomies-agent.service") != 2 || s.did("systemctl start zoomies.service") != 2 {
		t.Errorf("the first window must have been abandoned and a second one taken: %v", s.log)
	}
	if s.did("systemctl restart docker") != 1 || s.did("docker stop") != 0 {
		t.Errorf("Docker is restarted once, and nothing is stopped to make room: %v", s.log)
	}
	if !strings.Contains(out.String(), "A job started in that moment") {
		t.Errorf("the backing off must be said:\n%s", out)
	}
}

func TestWhenSafeGivesUpWithoutEverTakingTheHostOutOfService(t *testing.T) {
	e, s := maintenanceHost([]string{"job-1"})
	o, _ := whenSafe(5 * time.Minute)
	err := e.RestartWhenSafe(context.Background(), o)
	if !errors.Is(err, ErrHostBusy) || !strings.Contains(err.Error(), "stays pending") {
		t.Fatalf("a host that is never quiet must give up and say the change stays pending, got %v", err)
	}
	if s.did("systemctl stop") != 0 || s.did("systemctl restart") != 0 {
		t.Errorf("a busy host was touched: %v", s.log)
	}
	if !pending(t, e) {
		t.Error("an unattempted restart must stay pending")
	}
}

func TestWhenSafeDoesNothingWhenNothingIsPending(t *testing.T) {
	e, s := maintenanceHost(nil)
	_ = e.saveState(State{Version: 1, Changes: []Change{}})
	o, out := whenSafe(time.Hour)
	if err := e.RestartWhenSafe(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if s.did("systemctl") != 0 || s.did("docker") != 0 {
		t.Errorf("something was done with nothing to do: %v", s.log)
	}
	if !strings.Contains(out.String(), "No Docker restart is pending") {
		t.Errorf("it must say so:\n%s", out)
	}
}

// Anything but a busy host is not a matter of time, so it is not retried.
func TestWhenSafeDoesNotRetryAFailure(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.failRestart = true
	o, _ := whenSafe(time.Hour)
	if err := e.RestartWhenSafe(context.Background(), o); err == nil || errors.Is(err, ErrHostBusy) {
		t.Fatalf("a failed restart must be reported as one, got %v", err)
	}
	if s.did("systemctl restart docker") != 1 {
		t.Errorf("a failed restart was retried: %v", s.log)
	}
}

// The task is a unit of its own, so it outlives the services it stops, and there
// is only ever one.
func TestTheBackgroundTaskIsATransientUnitAndThereIsOnlyOne(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.put("/run/systemd/system/.keep", "")
	cmd := []string{"/usr/local/bin/zoomies", "tune", "--restart-pending"}
	if err := e.StartBackground(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	var run string
	for _, c := range s.log {
		if strings.HasPrefix(c, "systemd-run ") {
			run = c
		}
	}
	for _, want := range []string{"--unit zoomies-docker-restart", "--collect", "/usr/local/bin/zoomies tune --restart-pending"} {
		if !strings.Contains(run, want) {
			t.Errorf("systemd-run call %q lacks %q", run, want)
		}
	}
	s.unitActive = true
	if err := e.StartBackground(context.Background(), cmd); err == nil || !strings.Contains(err.Error(), "already waiting") {
		t.Errorf("a second task beside a waiting one must be refused: %v", err)
	}

	bare, _ := maintenanceHost(nil) // no /run/systemd/system
	if err := bare.StartBackground(context.Background(), cmd); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Errorf("a host without systemd must be told where to run it instead: %v", err)
	}
}

// --force --background starts the task and stops nothing itself.
func TestTuneBackgroundHandsTheRestartToTheTask(t *testing.T) {
	e, s := maintenanceHost([]string{"job-1"})
	s.put("/run/systemd/system/.keep", "")
	var out bytes.Buffer
	err := e.Tune(context.Background(), TuneOptions{Yes: true, Force: true, Background: true, BackgroundCommand: []string{"/bin/zoomies", "tune", "--restart-pending"},
		Only: IDs("inotify.watches"), Out: &out, In: strings.NewReader(""), Actor: "test"})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if s.did("systemd-run") != 1 {
		t.Errorf("the background task was not started: %v", s.log)
	}
	if s.did("systemctl stop") != 0 || s.did("systemctl restart") != 0 {
		t.Errorf("--background must not touch the host itself: %v", s.log)
	}
	if !pending(t, e) {
		t.Error("the restart is still pending until the task does it")
	}
}

// The task reviews nothing and applies nothing: it only finishes a restart.
func TestRestartPendingDoesNothingButTheRestart(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	var out bytes.Buffer
	before := s.writes
	if err := e.Tune(context.Background(), TuneOptions{RestartPending: true, GiveUp: time.Hour, Yes: true, Out: &out, In: strings.NewReader(""), Actor: "test"}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := s.writes - before; got != 1 { // the state file, once, when the restart cleared the pending flag
		t.Errorf("--restart-pending applied changes to the host: %d writes", got)
	}
	if v, _ := s.ReadFile("/proc/sys/fs/inotify/max_user_watches"); string(v) != "1024" {
		t.Errorf("a setting was changed: %s", v)
	}
}
