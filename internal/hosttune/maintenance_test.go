package hosttune

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
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
	// failStop is the units whose stop returns an error although systemd goes on to
	// finish stopping them: a client that gave up waiting.
	failStop map[string]bool
	// psFail is how many of the first `docker ps` calls fail, as a daemon too slow to
	// answer within the probe's budget does.
	psFail int
	// failWrites makes the state file unwritable, as a full or read-only disk does.
	failWrites bool
	// managed is what `docker ps` says of the containers this fleet owns, one line each as
	// the format asks for them, and withWorker is the containers a job is running in.
	managed    []string
	withWorker map[string]bool
	log        []string
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
		if s.failStop[a[1]] {
			return "", errors.New("signal: killed")
		}
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
		if s.psCalls < s.psFail {
			s.psCalls++
			return "", errors.New("context deadline exceeded")
		}
		if s.killed {
			return "", nil
		}
		i := min(s.psCalls, len(s.ps)-1)
		s.psCalls++
		return strings.Join(s.ps[i], "\n"), nil
	case n == "docker" && len(a) > 1 && a[0] == "ps" && a[1] == "--filter":
		return strings.Join(s.managed, "\n"), nil
	case n == "docker" && len(a) > 1 && a[0] == "top":
		if s.withWorker[a[1]] {
			return "ARGS\n/runner/bin/Runner.Listener\n/runner/bin/Runner.Worker spawnclient", nil
		}
		return "ARGS\n/runner/bin/Runner.Listener run", nil
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
	// The state file, three times: the units the restart owes, the pending flag cleared, and the
	// owed units cleared. Nothing else on the host.
	if got := s.writes - before; got != 3 {
		t.Errorf("--restart-pending applied changes to the host: %d writes", got)
	}
	if v, _ := s.ReadFile("/proc/sys/fs/inotify/max_user_watches"); string(v) != "1024" {
		t.Errorf("a setting was changed: %s", v)
	}
}

// A stop that outlasts the client leaves systemd finishing it, and a unit that was not
// recorded as stopped is never started again: the agent stayed down for good. The unit
// is on the list before the stop is asked for.
func TestAStopThatTimesOutStillStartsTheUnitAgain(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.failStop = map[string]bool{"zoomies-agent.service": true}
	o, out := opts(false)
	err := e.MaintainDocker(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "may still be stopping") {
		t.Fatalf("a stop that failed must be an error that says the unit may still be stopping: %v", err)
	}
	if s.active["zoomies-agent.service"] != "active" {
		t.Errorf("the agent was left stopped after a stop that timed out: %v\n%s", s.active, out)
	}
	if s.index("systemctl start zoomies-agent.service") < 0 {
		t.Errorf("the restore never started the agent: %v", s.log)
	}
}

// A failed look at what is running is not a quiet moment, and not the end of a task that
// has a day to find one: it used to return at once, and the background restart vanished
// with its change still pending and nobody told.
func TestWhenSafeLooksAgainAfterAFailedLookAtWhatIsRunning(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.psFail = 2
	o, out := whenSafe(time.Hour)
	if err := e.RestartWhenSafe(context.Background(), o); err != nil {
		t.Fatalf("two failed looks ended the wait: %v\n%s", err, out)
	}
	if s.did("systemctl restart docker") != 1 {
		t.Errorf("Docker was not restarted once it answered: %v", s.log)
	}
}

// A Docker that is really down still ends the task, and stops nothing on the way.
func TestWhenSafeGivesUpOnADockerThatNeverAnswers(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.psFail = 1000
	o, _ := whenSafe(time.Hour)
	err := e.RestartWhenSafe(context.Background(), o)
	if err == nil || !errors.Is(err, errDockerUnreadable) {
		t.Fatalf("a Docker that never answers must be reported, got %v", err)
	}
	if s.did("systemctl stop") != 0 || s.did("systemctl restart docker") != 0 {
		t.Errorf("a host whose Docker could not be read was taken out of service: %v", s.log)
	}
}

// A restart killed after it stopped the units -- SIGKILL, a crash -- left them stopped, and a
// rerun saw both inactive, concluded there was nothing to restore, and reported the host back in
// service. The units it owes are on the record, and the next restart starts them.
func TestARestartKilledAfterStoppingTheUnitsIsHealedByTheNextOne(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.active["zoomies.service"], s.active["zoomies-agent.service"] = "inactive", "inactive"
	if err := e.setMaintenanceOwed([]string{"zoomies.service", "zoomies-agent.service"}); err != nil {
		t.Fatal(err)
	}
	o, out := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err != nil {
		t.Fatalf("MaintainDocker: %v\n%s", err, out)
	}
	if s.active["zoomies.service"] != "active" || s.active["zoomies-agent.service"] != "active" {
		t.Errorf("the units a killed restart left stopped were not started: %v\n%s", s.active, out)
	}
	if !strings.Contains(out.String(), "was interrupted") {
		t.Errorf("it must say what it found:\n%s", out)
	}
	owed, err := e.maintenanceOwed()
	if err != nil || len(owed) != 0 {
		t.Errorf("the record was not cleared once the units were back: %v, %v", owed, err)
	}
}

// The record is written before the first stop, and if it cannot be written nothing is stopped.
func TestAHostIsLeftAsItIsWhenWhatItIsAboutToStopCannotBeRecorded(t *testing.T) {
	e, s := maintenanceHost(nil)
	s.failWrites = true
	o, out := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err == nil {
		t.Fatalf("a restart that could not record what it stops went ahead\n%s", out)
	}
	if s.did("systemctl stop") != 0 {
		t.Errorf("a unit was stopped with no record of how to start it again: %v", s.log)
	}
}

func (s *scriptedHost) WriteFile(p string, b []byte, m fs.FileMode) error {
	if s.failWrites {
		return errors.New("read-only file system")
	}
	return s.fakeSystem.WriteFile(p, b, m)
}

// A pool with a minimum keeps idle runners up between jobs and never reaps below it, so
// counting them as work made the quiet moment unreachable: the restart waited out its
// whole budget, with the host out of service, to refuse at the end. An idle runner and
// its sidecar are stopped for the restart instead; one with a job in it is still waited
// for.
func TestWarmRunnersAreNotWorkButARunnerWithAJobIs(t *testing.T) {
	warm := []string{"r1|runner-1|runner|", "d1|dind-1|dind|runner-1"}
	e, s := maintenanceHost([]string{"r1", "d1"})
	s.managed = warm
	o, _ := opts(false)
	if err := e.MaintainDocker(context.Background(), o); err != nil {
		t.Fatalf("a host holding only warm runners was refused: %v", err)
	}
	if s.did("docker stop r1 d1") != 1 || s.did("systemctl restart docker") != 1 {
		t.Errorf("the idle runner and its sidecar were not stopped before the restart: %v", s.log)
	}

	e, s = maintenanceHost([]string{"r1", "d1"})
	s.managed = warm
	s.withWorker = map[string]bool{"r1": true}
	o, _ = opts(false)
	if err := e.MaintainDocker(context.Background(), o); !errors.Is(err, ErrHostBusy) {
		t.Fatalf("err = %v; a runner with a job in it is work and must be waited for", err)
	}
	if s.did("systemctl restart docker") != 0 {
		t.Errorf("Docker was restarted under a running job: %v", s.log)
	}
}

// The background task's quiet moment is the same test: a host kept warm by a pool
// minimum is quiet, where before it never was.
func TestWhenSafeFindsAQuietMomentOnAHostThatKeepsWarmRunners(t *testing.T) {
	e, s := maintenanceHost([]string{"r1"})
	s.managed = []string{"r1|runner-1|runner|"}
	o, _ := whenSafe(time.Hour)
	if err := e.RestartWhenSafe(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if s.did("systemctl restart docker") != 1 {
		t.Errorf("Docker was not restarted: %v", s.log)
	}
}

// What cannot be established counts as work: a wrong "idle" ends someone's job.
func TestARunnerWhoseProcessesCannotBeReadIsTreatedAsWork(t *testing.T) {
	e, s := maintenanceHost([]string{"r1"})
	s.managed = []string{"r1|runner-1|runner|"}
	s.withWorker = nil
	e.System = &topFails{s}
	busy, idle, err := e.workContainers(context.Background())
	if err != nil || len(busy) != 1 || len(idle) != 0 {
		t.Fatalf("busy = %v, idle = %v, err = %v; want the container counted as work", busy, idle, err)
	}
}

type topFails struct{ *scriptedHost }

func (t *topFails) Run(ctx context.Context, n string, a ...string) (string, error) {
	if n == "docker" && len(a) > 0 && a[0] == "top" {
		return "", errors.New("no such container")
	}
	return t.scriptedHost.Run(ctx, n, a...)
}
