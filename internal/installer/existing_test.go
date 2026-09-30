package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stopWatcher lets a test look at the host at the moment the service is asked
// to stop, which is the only way to prove what happened before or after it.
type stopWatcher struct {
	fakeManager
	onStop func()
}

func (m *stopWatcher) Stop(ctx context.Context) error {
	if m.onStop != nil {
		m.onStop()
	}
	return m.fakeManager.Stop(ctx)
}

func existingInstall(t *testing.T) (Plan, string) {
	t.Helper()
	dir := t.TempDir()
	p := Plan{
		KeyFile:    filepath.Join(dir, "encryption.key"),
		DBPath:     filepath.Join(dir, "zoomies.db"),
		ConfigFile: filepath.Join(dir, "zoomies.yaml"),
		Service:    ServiceSystemd,
	}
	for _, path := range []string{p.KeyFile, p.DBPath, p.ConfigFile} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p, dir
}

func installerWith(mgr ServiceManager, existing ExistingInstall) *Installer {
	i := &Installer{ui: newUI(&strings.Builder{})}
	i.det.Existing = existing
	i.newManager = func(ServiceKind, string) (ServiceManager, error) { return mgr, nil }
	return i
}

// A controller that crashed, or was killed, leaves its write-ahead log next to
// the database. Left in place while a fresh database is created at the same
// path, SQLite would try to replay the old database's log into the new one.
func TestArchivingAnInstallMovesTheDatabaseSidecarsWithIt(t *testing.T) {
	p, dir := existingInstall(t)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(p.DBPath+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	i := installerWith(&fakeManager{}, ExistingInstall{})
	if err := i.archiveExisting(p); err != nil {
		t.Fatalf("archiveExisting: %v", err)
	}

	for _, path := range []string{p.KeyFile, p.DBPath, p.ConfigFile, p.DBPath + "-wal", p.DBPath + "-shm"} {
		if exists(path) {
			t.Errorf("%s is still where the new install will write", path)
		}
	}
	// Each sidecar keeps the name of the database it belongs to, so opening the
	// archived database directly still finds its own log.
	moved, err := filepath.Glob(filepath.Join(dir, "zoomies.db.*.bak-wal"))
	if err != nil || len(moved) != 1 {
		t.Errorf("the write-ahead log should be archived beside its database, found %v (%v)", moved, err)
	}
	moved, _ = filepath.Glob(filepath.Join(dir, "zoomies.db.*.bak-shm"))
	if len(moved) != 1 {
		t.Errorf("the shared-memory file should be archived beside its database, found %v", moved)
	}
}

// Renaming a database a live controller has open leaves that controller
// writing into the archive while the installer builds a new one nobody serves.
// The service has to be stopped -- which also checkpoints its log -- while the
// files are still where it expects them, and nothing may move if it will not
// stop.
func TestStartingAgainStopsTheControllerBeforeMovingItsFiles(t *testing.T) {
	p, _ := existingInstall(t)
	p.StartOver = true

	var presentAtStop bool
	mgr := &stopWatcher{}
	mgr.onStop = func() { presentAtStop = exists(p.DBPath) && exists(p.KeyFile) && exists(p.ConfigFile) }
	i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service", Database: p.DBPath})

	if err := i.startOver(context.Background(), p); err != nil {
		t.Fatalf("startOver: %v", err)
	}
	if !contains(mgr.calls, "stop") {
		t.Fatalf("the running controller was never stopped: %v", mgr.calls)
	}
	if !presentAtStop {
		t.Error("the files were moved before the controller stopped, while it still had the database open")
	}
	if exists(p.DBPath) || exists(p.KeyFile) || exists(p.ConfigFile) {
		t.Error("the old key, database and configuration should have been moved aside")
	}
}

func TestStartingAgainMovesNothingWhenTheControllerWillNotStop(t *testing.T) {
	p, _ := existingInstall(t)
	p.StartOver = true

	mgr := &failingStop{}
	i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})

	err := i.startOver(context.Background(), p)
	if err == nil {
		t.Fatal("a controller that will not stop must stop the run")
	}
	if !strings.Contains(err.Error(), "systemctl stop") {
		t.Errorf("the error should say how to stop it by hand: %v", err)
	}
	if !exists(p.DBPath) || !exists(p.KeyFile) || !exists(p.ConfigFile) {
		t.Error("nothing may be renamed under a controller that is still running")
	}
}

type failingStop struct{ fakeManager }

func (m *failingStop) Stop(context.Context) error { return os.ErrPermission }

// `systemctl start` on a unit that is already active does nothing, so a
// reconfigure that only started the service left the old process serving the
// old bind, TLS and key while the summary said it was running.
func TestReconfiguringStopsTheRunningControllerSoTheStartIsAFreshOne(t *testing.T) {
	cases := []struct {
		name     string
		existing ExistingInstall
		service  ServiceKind
		stopped  bool
	}{
		{"a native install with a unit", ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"}, ServiceSystemd, true},
		{"a first install has nothing to stop", ExistingInstall{}, ServiceSystemd, false},
		{"an install run by hand has no service to stop", ExistingInstall{ConfigFile: "/etc/zoomies/zoomies.yaml"}, ServiceNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeManager{}
			i := installerWith(mgr, tc.existing)
			if err := i.stopRunningController(context.Background(), Plan{Service: tc.service}); err != nil {
				t.Fatalf("stopRunningController: %v", err)
			}
			if got := contains(mgr.calls, "stop"); got != tc.stopped {
				t.Errorf("stopped = %v, want %v (calls %v)", got, tc.stopped, mgr.calls)
			}
		})
	}
}

// The wiring is what the audit finding is about: a helper that stops the
// controller is no fix if the install never calls it, or calls it after the
// start.
func TestReconfiguringAnInstallReplacesTheRunningControllerBeforeStartingTheUnit(t *testing.T) {
	cases := []struct {
		name  string
		plan  Plan
		calls []string
	}{
		{"a reconfigure stops the old process, then starts a fresh one",
			Plan{Service: ServiceSystemd, StartService: true, EnableService: true},
			[]string{"stop", "install", "enable", "start"}},
		{"a service that is not to be started is not stopped either",
			Plan{Service: ServiceSystemd, StartService: false, EnableService: true},
			[]string{"install", "enable"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeManager{}
			i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})
			if _, err := i.restartService(context.Background(), tc.plan); err != nil {
				t.Fatalf("restartService: %v", err)
			}
			if got, want := strings.Join(mgr.calls, ","), strings.Join(tc.calls, ","); got != want {
				t.Errorf("service calls = %s, want %s", got, want)
			}
		})
	}
}

// A stop that fails must not abandon the install: the start may still work.
// It has to say what the old process may still be serving, though, or the
// summary's "running" is a lie about which settings are live.
func TestAControllerThatWillNotStopIsAWarningAndTheStartStillHappens(t *testing.T) {
	mgr := &failingStop{}
	i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})
	var out strings.Builder
	i.ui = newUI(&out)

	if _, err := i.restartService(context.Background(), Plan{Service: ServiceSystemd, StartService: true}); err != nil {
		t.Fatalf("restartService: %v", err)
	}
	if !contains(mgr.calls, "start") {
		t.Errorf("the unit was never started after the failed stop: %v", mgr.calls)
	}
	if !strings.Contains(out.String(), "old process keeps serving the old settings") {
		t.Errorf("the operator was not warned that the old process may still be running:\n%s", out.String())
	}
}

func filesInPlace(p Plan) bool { return exists(p.DBPath) && exists(p.KeyFile) && exists(p.ConfigFile) }

// The review's text promises that stopping there leaves the host as it was.
// Choosing Start again records the intention on the plan, and nothing may act
// on it until the review has been confirmed.
func TestStoppingAtTheReviewAfterChoosingStartAgainLeavesTheHostAsItWas(t *testing.T) {
	p, _ := existingInstall(t)
	p.StartOver = true
	mgr := &fakeManager{}
	i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})

	installed := false
	err := i.reviewAndCarryOut(context.Background(), p,
		func(context.Context, Plan) (bool, error) { return false, nil },
		func(context.Context, Plan) error { installed = true; return nil })
	if err != nil {
		t.Fatalf("reviewAndCarryOut: %v", err)
	}
	if installed {
		t.Error("the install ran although the operator stopped at the review")
	}
	if len(mgr.calls) != 0 {
		t.Errorf("the service was touched although the operator stopped at the review: %v", mgr.calls)
	}
	if !filesInPlace(p) {
		t.Error("the key, database or configuration was moved although the operator stopped at the review")
	}
}

// The order is the fix: review, then stop the controller while its files are
// still where it expects them, then archive, then install. Archiving after the
// install has begun would take the new database with it.
func TestStartingAgainStopsArchivesAndThenInstallsOnceTheReviewIsConfirmed(t *testing.T) {
	p, _ := existingInstall(t)
	p.StartOver = true
	p.StartService = true

	var events []string
	mgr := &stopWatcher{}
	mgr.onStop = func() { events = append(events, "stop (files in place: "+yesNo(filesInPlace(p))+")") }
	i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})

	err := i.reviewAndCarryOut(context.Background(), p,
		func(context.Context, Plan) (bool, error) {
			events = append(events, "review (files in place: "+yesNo(filesInPlace(p))+")")
			return true, nil
		},
		func(ctx context.Context, p Plan) error {
			events = append(events, "install (files in place: "+yesNo(filesInPlace(p))+")")
			// The tail of a real install. It must not stop the controller a
			// second time: startOver already did.
			_, err := i.restartService(ctx, p)
			return err
		})
	if err != nil {
		t.Fatalf("reviewAndCarryOut: %v", err)
	}
	want := "review (files in place: yes),stop (files in place: yes),install (files in place: no)"
	if got := strings.Join(events, ","); got != want {
		t.Errorf("sequence = %s, want %s", got, want)
	}
	if got, want := strings.Join(mgr.calls, ","), "stop,install,start"; got != want {
		t.Errorf("service calls = %s, want %s (exactly one stop)", got, want)
	}
}

func TestAControllerThatWillNotStopEndsAStartAgainBeforeTheInstall(t *testing.T) {
	p, _ := existingInstall(t)
	p.StartOver = true
	i := installerWith(&failingStop{}, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})

	installed := false
	err := i.reviewAndCarryOut(context.Background(), p,
		func(context.Context, Plan) (bool, error) { return true, nil },
		func(context.Context, Plan) error { installed = true; return nil })
	if err == nil {
		t.Fatal("a controller that will not stop must end the run")
	}
	if installed {
		t.Error("the install ran under a controller that is still serving the old database")
	}
	if !filesInPlace(p) {
		t.Error("nothing may be renamed under a controller that is still running")
	}
}

func TestAConfirmedRunThatIsNotAStartAgainArchivesNothing(t *testing.T) {
	p, _ := existingInstall(t)
	mgr := &fakeManager{}
	i := installerWith(mgr, ExistingInstall{Unit: "/etc/systemd/system/zoomies.service"})

	installed := false
	err := i.reviewAndCarryOut(context.Background(), p,
		func(context.Context, Plan) (bool, error) { return true, nil },
		func(context.Context, Plan) error { installed = true; return nil })
	if err != nil {
		t.Fatalf("reviewAndCarryOut: %v", err)
	}
	if !installed {
		t.Error("a confirmed review must go on to install")
	}
	if !filesInPlace(p) || len(mgr.calls) != 0 {
		t.Errorf("a reconfigure keeps the key and database and leaves the stop to the install (calls %v)", mgr.calls)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
