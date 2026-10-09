package updates

import "testing"

// Each row is a host the installer would refuse the helper on, or one it would
// not, described by what the service can see of it. The status says "manual,
// with the reason" only where the install is certain to refuse; anywhere the
// service cannot tell, it says nothing, and the host is shown as missing a
// helper as before, with the command that installs one.
func TestTheHelperIsUnsupportedOnlyWhereTheInstallerWouldRefuseIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		host HelperHost
		want HelperUnsupported
	}{
		{"a native systemd host", HelperHost{GOOS: "linux", Systemd: true, RunsRunners: true}, ""},
		{"a native systemd controller that runs no runners", HelperHost{GOOS: "linux", Systemd: true}, ""},
		{"macOS", HelperHost{GOOS: "darwin", Systemd: true, RunsRunners: true}, HelperUnsupportedOS},
		{"Windows", HelperHost{GOOS: "windows", RunsRunners: true}, HelperUnsupportedOS},
		{"an operating system nobody named", HelperHost{RunsRunners: true}, HelperUnsupportedOS},
		{"a Linux host that systemd does not run", HelperHost{GOOS: "linux", RunsRunners: true}, HelperUnsupportedNoSystemd},
		// Inside a container the host's systemd cannot be seen, so its absence
		// says nothing about the host the helper would be installed on.
		{"a container that runs runners", HelperHost{GOOS: "linux", InContainer: true, RunsRunners: true}, ""},
		{"a container that runs no runners", HelperHost{GOOS: "linux", InContainer: true}, HelperUnsupportedNoSharedFolder},
		{"a container under a rootless runtime", HelperHost{GOOS: "linux", InContainer: true, RunsRunners: true, RootlessRuntime: true}, HelperUnsupportedRootless},
		// A native service is unaffected by how the runtime its runners use is
		// run: the helper serves the service's own account, not the runtime's.
		{"a native host whose runners use a rootless runtime", HelperHost{GOOS: "linux", Systemd: true, RunsRunners: true, RootlessRuntime: true}, ""},
		// The shared folder is the first thing missing: without it there is
		// nowhere to write a request, whatever owns what the container writes.
		{"a rootless container that runs no runners", HelperHost{GOOS: "linux", InContainer: true, RootlessRuntime: true}, HelperUnsupportedNoSharedFolder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HelperSupport(tc.host); got != tc.want {
				t.Errorf("HelperSupport(%+v) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// The cause crosses the wire from an agent, and the controller keys a sentence
// on it, so a word it does not know has to read as nothing known rather than as
// a reason nobody wrote.
func TestOnlyTheNamedCausesAreKnown(t *testing.T) {
	for _, c := range []HelperUnsupported{HelperUnsupportedOS, HelperUnsupportedNoSystemd, HelperUnsupportedRootless, HelperUnsupportedNoSharedFolder} {
		if !c.Known() {
			t.Errorf("%q is not known", c)
		}
	}
	for _, c := range []HelperUnsupported{"", "OS", "no systemd", "because I said so"} {
		if c.Known() {
			t.Errorf("%q is known", c)
		}
	}
}
