package hosttune

import (
	"context"
	"strings"
	"testing"
)

func TestDryRunDoesNotCreateStateOrChangeFiles(t *testing.T) {
	e, f := fixture()
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	var out strings.Builder
	err := e.Tune(context.Background(), TuneOptions{DryRun: true, Only: IDs("inotify.watches"), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if f.writes != 0 || !strings.Contains(out.String(), "+++ /etc/sysctl.d/90-zoomies.conf") {
		t.Fatalf("dry run: %s writes=%d", out.String(), f.writes)
	}
}
func TestConsentDefaultsToNo(t *testing.T) {
	e, f := fixture()
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	if err := e.Tune(context.Background(), TuneOptions{Only: IDs("inotify.watches"), In: strings.NewReader("n\n")}); err != nil {
		t.Fatal(err)
	}
	if f.writes != 0 {
		t.Fatal("changed without consent")
	}
}
func TestDockerRestartRefusesBusyAndUnknownHosts(t *testing.T) {
	for _, busy := range []bool{true, false} {
		e, f := fixture()
		f.commands["systemctl show zoomies.service --property=ActiveState --value"] = "inactive"
		f.commands["systemctl show zoomies-agent.service --property=ActiveState --value"] = "inactive"
		if busy {
			f.commands["docker ps -q"] = "running-container"
		}
		if e.RestartDocker(context.Background()) == nil {
			t.Fatal("unsafe restart")
		}
		for _, c := range f.calls {
			if c == "systemctl restart docker.service" {
				t.Fatal("restarted")
			}
		}
	}
}
func TestNativeAgentPreventsRestartRace(t *testing.T) {
	e, f := fixture()
	f.commands["systemctl show zoomies.service --property=ActiveState --value"] = "active"
	if e.RestartDocker(context.Background()) == nil {
		t.Fatal("agent accepts work during restart")
	}
}

func TestTuningReportsPreviewAndDeclinedChangesWithoutWriting(t *testing.T) {
	for _, dry := range []bool{false, true} {
		e, f := fixture()
		f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
		var out strings.Builder
		if err := e.Tune(context.Background(), TuneOptions{DryRun: dry, Only: IDs("inotify.watches"), In: strings.NewReader("n\n"), Out: &out}); err != nil {
			t.Fatal(err)
		}
		want := "Tuning complete: 0 applied, 1 left unchanged"
		if dry {
			want = "Preview complete: 1 eligible changes. No changes made."
		}
		if f.writes != 0 || !strings.Contains(out.String(), want) {
			t.Fatalf("dry=%v: %s writes=%d", dry, out.String(), f.writes)
		}
	}
}
