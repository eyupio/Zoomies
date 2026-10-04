package installer

import (
	"strings"
	"testing"
)

func TestHostHealthServiceOnlyRunsTheNativeReadOnlyReporter(t *testing.T) {
	s := RenderHostHealthService("/usr/local/bin/zoomies", "/disk/work", "unix:///var/run/docker.sock")
	for _, want := range []string{"doctor --watch", "--tier dedicated", "--work-dir", "/disk/work", "ProtectSystem=strict", "ProtectKernelTunables=yes", "ReadWritePaths=" + SharedHostDir + "/host-health"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, bad := range []string{" tune ", "--interactive", "--yes", " restart docker", "PrivateTmp=yes"} {
		if strings.Contains(s, bad) {
			t.Fatalf("unsafe health service: %s", bad)
		}
	}
}
