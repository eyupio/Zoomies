package installer

import (
	"context"
	"os"
	"strings"
	"testing"
)

func installerWith(mgr ServiceManager, existing ExistingInstall) *Installer {
	i := &Installer{ui: newUI(&strings.Builder{})}
	i.det.Existing = existing
	i.newManager = func(ServiceKind, string) (ServiceManager, error) { return mgr, nil }
	return i
}

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

type failingStop struct{ fakeManager }

func (m *failingStop) Stop(context.Context) error { return os.ErrPermission }

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
