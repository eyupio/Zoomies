package hosttune

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type fakeSystem struct {
	files    fstest.MapFS
	commands map[string]string
	failures map[string]error
	calls    []string
	writes   int
}

func fake() *fakeSystem {
	return &fakeSystem{files: fstest.MapFS{}, commands: map[string]string{}, failures: map[string]error{}}
}
func (f *fakeSystem) ReadFile(p string) ([]byte, error) {
	return f.files.ReadFile(strings.TrimPrefix(p, "/"))
}
func (f *fakeSystem) ReadDir(p string) ([]fs.DirEntry, error) {
	return f.files.ReadDir(strings.TrimPrefix(p, "/"))
}
func (f *fakeSystem) Stat(p string) (fs.FileInfo, error) {
	return fs.Stat(f.files, strings.TrimPrefix(p, "/"))
}
func (f *fakeSystem) WriteFile(p string, b []byte, m fs.FileMode) error {
	f.writes++
	f.files[strings.TrimPrefix(p, "/")] = &fstest.MapFile{Data: append([]byte(nil), b...), Mode: m}
	return nil
}
func (f *fakeSystem) WriteValue(p, v string) error { return f.WriteFile(p, []byte(v+"\n"), 0644) }
func (f *fakeSystem) Remove(p string) error        { delete(f.files, strings.TrimPrefix(p, "/")); return nil }
func (f *fakeSystem) Lock(string) (func(), error)  { return func() {}, nil }
func (f *fakeSystem) Run(_ context.Context, n string, a ...string) (string, error) {
	k := n + " " + strings.Join(a, " ")
	f.calls = append(f.calls, k)
	if n == "sysctl" && len(a) == 2 && a[0] == "-w" {
		key, val, _ := strings.Cut(a[1], "=")
		f.put("/proc/sys/"+strings.ReplaceAll(key, ".", "/"), val)
		return "", nil
	}
	if err := f.failures[k]; err != nil {
		return "", err
	}
	if v, ok := f.commands[k]; ok {
		return v, nil
	}
	return "", fs.ErrNotExist
}
func (f *fakeSystem) put(p, s string) {
	f.files[strings.TrimPrefix(p, "/")] = &fstest.MapFile{Data: []byte(s), Mode: 0644}
}
func fixture() (*Engine, *fakeSystem) {
	f := fake()
	f.put("/etc/os-release", "ID=ubuntu\nVERSION_ID=24.04\n")
	e := New(Options{System: f, OS: "linux", UID: 0, Now: func() time.Time { return time.Unix(1234, 0) }, WorkDir: "/work"})
	return e, f
}
func TestEveryBaseCheckOnlyReadsTheInjectedHost(t *testing.T) {
	for _, c := range baseChecks() {
		t.Run(c.ID, func(t *testing.T) {
			e, f := fixture()
			r := c.Detect(context.Background(), e)
			if r.Status == "" {
				t.Fatal("missing status")
			}
			if f.writes != 0 {
				t.Fatal("detection wrote files")
			}
			for _, cmd := range f.calls {
				if strings.Contains(cmd, " -w ") || strings.Contains(cmd, " restart ") {
					t.Fatalf("mutating detection: %s", cmd)
				}
			}
		})
	}
}
func TestSysctlPlansDoNotCompeteWithAnotherAdministrator(t *testing.T) {
	e, f := fixture()
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	f.put("/etc/sysctl.d/60-admin.conf", "fs.inotify.max_user_watches = 2048\n")
	c, _ := e.Check("inotify.watches")
	r := c.Detect(context.Background(), e)
	if r.Status != Warn || !strings.Contains(r.Reason, "60-admin") {
		t.Fatalf("%+v", r)
	}
	if _, err := e.Plan(context.Background(), Result{ID: c.ID, Recommended: "524288"}); err == nil {
		t.Fatal("applied managed setting")
	}
}
func TestContainerAndReadOnlySysctlsAreSkipped(t *testing.T) {
	for _, container := range []bool{true, false} {
		e, f := fixture()
		f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
		e.Container = container
		if !container {
			f.files["proc/sys/fs/inotify/max_user_watches"].Mode = 0444
		}
		c, _ := e.Check("inotify.watches")
		r := c.Detect(context.Background(), e)
		if r.Status != Skip || r.Reason == "" {
			t.Fatalf("%+v", r)
		}
	}
}
func TestUnprivilegedDoctorDoesNotOfferAnApply(t *testing.T) {
	e, f := fixture()
	e.UID = 1000
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	for _, r := range e.Run(context.Background(), Safe).Results {
		if r.Actionable {
			t.Fatal("unprivileged apply")
		}
	}
	if f.writes != 0 {
		t.Fatal("doctor writes")
	}
}
func TestDockerMergePreservesUnrelatedKeys(t *testing.T) {
	b, err := MergeDockerLogs([]byte(`{"data-root":"/disk/docker","log-opts":{"labels":"team"},"live-restore":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"/disk/docker", "team", "live-restore", "10m"} {
		if !strings.Contains(string(b), s) {
			t.Fatalf("lost %s", s)
		}
	}
	for _, s := range []string{`null`, `{"log-driver":"journald"}`, `{"log-opts":42}`, `{`} {
		if _, err = MergeDockerLogs([]byte(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestStateRoundTripRestoresExactBytesAndIsIdempotent(t *testing.T) {
	e, f := fixture()
	f.put(sysctlFile, "# own comment\n")
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	c, err := e.Plan(context.Background(), Result{ID: "inotify.watches", Recommended: "524288"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Apply(context.Background(), c, "test-user"); err != nil {
		t.Fatal(err)
	}
	writes := f.writes
	if err = e.Apply(context.Background(), c, "test-user"); err != nil || f.writes != writes {
		t.Fatalf("rerun mutated: %v", err)
	}
	s, err := e.LoadState()
	if err != nil || len(s.Changes) != 1 || s.Changes[0].Actor != "test-user" {
		t.Fatalf("state: %+v %v", s, err)
	}
	if _, err = e.Revert(context.Background(), nil, nil, false); err != nil {
		t.Fatal(err)
	}
	b, _ := f.ReadFile(sysctlFile)
	if string(b) != "# own comment\n" {
		t.Fatalf("lost original bytes: %s", b)
	}
	v, _ := read(e, "/proc/sys/fs/inotify/max_user_watches")
	if v != "1024" {
		t.Fatal("lost runtime value")
	}
}
func TestRevertRefusesToClobberSubsequentEdits(t *testing.T) {
	e, f := fixture()
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	c, _ := e.Plan(context.Background(), Result{ID: "inotify.watches", Recommended: "524288"})
	if err := e.Apply(context.Background(), c, "test"); err != nil {
		t.Fatal(err)
	}
	f.put(sysctlFile, "# newer admin change\n")
	if _, err := e.Revert(context.Background(), nil, nil, false); err == nil {
		t.Fatal("clobbered admin change")
	}
}
func TestInterruptedApplyKeepsTheReversalRecord(t *testing.T) {
	e, f := fixture()
	c := Change{ID: "broken", Operations: []Operation{{Do: []string{"fake", "apply"}, Undo: []string{"fake", "undo"}}}}
	f.failures["fake apply"] = errors.New("failed")
	if e.Apply(context.Background(), c, "test") == nil {
		t.Fatal("failure hidden")
	}
	s, _ := e.LoadState()
	if len(s.Changes) != 1 || s.Changes[0].Phase != "pending" {
		t.Fatal("no recovery record")
	}
}
func TestReportExitCodes(t *testing.T) {
	for _, tt := range []struct {
		s    Status
		code int
	}{{OK, 0}, {Skip, 0}, {Warn, 1}, {Error, 2}} {
		if got := (Report{Results: []Result{{Status: tt.s}}}).ExitCode(); got != tt.code {
			t.Fatalf("%s = %d", tt.s, got)
		}
	}
}

func TestSharedDropInDryRevertMatchesRealRevertAndPreservesMode(t *testing.T) {
	e, f := fixture()
	f.put(sysctlFile, "# original\n")
	f.files[strings.TrimPrefix(sysctlFile, "/")].Mode = 0600
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	f.put("/proc/sys/fs/inotify/max_user_instances", "128")
	for _, r := range []Result{{ID: "inotify.watches", Recommended: "524288"}, {ID: "inotify.instances", Recommended: "1024"}} {
		c, err := e.Plan(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if c.Files[0].Mode != 0600 {
			t.Fatal("file permissions changed")
		}
		if err := e.Apply(context.Background(), c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	writes := f.writes
	changes, err := e.Revert(context.Background(), nil, nil, true)
	if err != nil || len(changes) != 2 {
		t.Fatalf("dry revert: %v %v", changes, err)
	}
	if f.writes != writes {
		t.Fatal("dry revert wrote files")
	}
	if _, err = e.Revert(context.Background(), nil, nil, false); err != nil {
		t.Fatal(err)
	}
	b, _ := f.ReadFile(sysctlFile)
	if string(b) != "# original\n" {
		t.Fatal("original not restored")
	}
}

func platform(t *testing.T, osRelease string) *Engine {
	t.Helper()
	f := fake()
	f.put("/etc/os-release", osRelease)
	return New(Options{System: f, OS: "linux", UID: 0, Now: func() time.Time { return time.Unix(1234, 0) }, WorkDir: "/work"})
}

func TestTuningIsSupportedOnTheReleasesItWasWrittenFor(t *testing.T) {
	for _, tc := range []struct {
		osRelease string
		want      bool
	}{
		{"ID=ubuntu\nVERSION_ID=24.04\n", true},
		{"ID=ubuntu\nVERSION_ID=26.04\n", true},
		{"ID=debian\nVERSION_ID=13\n", true},
		{"ID=debian\nVERSION_ID=13.1\n", true},
		{"ID=ubuntu\nVERSION_ID=22.04\n", false},
		{"ID=ubuntu\nVERSION_ID=25.10\n", false},
		{"ID=debian\nVERSION_ID=12\n", false},
		{"ID=fedora\nVERSION_ID=42\n", false},
	} {
		if got := platform(t, tc.osRelease).Supported(); got != tc.want {
			t.Errorf("Supported() for %q = %v, want %v", tc.osRelease, got, tc.want)
		}
	}
	if got := SupportedPlatforms(); got != "Ubuntu 24.04, Ubuntu 26.04 or Debian 13" {
		t.Errorf("SupportedPlatforms() = %q", got)
	}
}

// A refusal has to say which part was unsupported. "this host is linux ubuntu"
// sent an operator on Ubuntu 26.04 to wonder whether Ubuntu was supported at all.
func TestARefusedHostIsNamedByDistributionAndRelease(t *testing.T) {
	err := platform(t, "ID=ubuntu\nVERSION_ID=22.04\n").Tune(context.Background(), TuneOptions{})
	if err == nil || !strings.Contains(err.Error(), "ubuntu 22.04") || !strings.Contains(err.Error(), "Ubuntu 26.04") {
		t.Errorf("the refusal must name the release the host reported and the ones supported: %v", err)
	}
}

// On a supported release nothing in the platform gate stops tune or tags the
// report with the distribution warning.
func TestASupportedReleaseIsNotToldItIsReportOnly(t *testing.T) {
	e := platform(t, "ID=ubuntu\nVERSION_ID=26.04\n")
	for _, r := range e.Run(context.Background(), Safe).Results {
		if r.ID == "environment" {
			t.Errorf("a supported release was told its distribution is unsupported: %+v", r)
		}
	}
	if err := e.Tune(context.Background(), TuneOptions{}); err != nil && strings.Contains(err.Error(), "tune supports") {
		t.Errorf("tune refused a supported release: %v", err)
	}
}

// The web UI prints each row's reason on the host page, under a Next step panel
// that says cordon and never drain: a drain stops a runner that is still busy
// after five minutes, which is the wrong advice for waiting on a job. Two
// reasons said "drain" until this was pinned, so the page contradicted itself
// on the one host that had a reboot pending. This walks every tier on a host
// that reaches both of them, and fails if any text a check reports uses the word.
func TestNoCheckTellsAnOperatorToDrainTheHost(t *testing.T) {
	e := rebootHost()
	f := e.System.(*fakeSystem)
	// Neither filesystem is ext4 or XFS with noatime, so both mount rows warn
	// and carry their advice.
	f.commands["findmnt -n -o FSTYPE,OPTIONS --target /work"] = "nfs4 rw"
	f.commands["docker info --format {{.DockerRootDir}}"] = "/var/lib/docker"
	f.commands["findmnt -n -o FSTYPE,OPTIONS --target /var/lib/docker"] = "nfs4 rw"

	reached := map[string]bool{}
	for _, tier := range []Tier{Safe, Aggressive, Dedicated} {
		for _, r := range e.Run(context.Background(), tier).Results {
			if r.Status == Warn && r.Reason != "" {
				reached[r.ID] = true
			}
			for field, text := range map[string]string{
				"title": r.Title, "current": r.Current, "recommended": r.Recommended,
				"rationale": r.Rationale, "reason": r.Reason,
			} {
				if strings.Contains(strings.ToLower(text), "drain") {
					t.Errorf("%s (%s tier) %s says drain, but the host page advises cordon: %q", r.ID, tier, field, text)
				}
			}
		}
	}
	for _, id := range []string{KernelPending, "work.filesystem", "docker.filesystem"} {
		if !reached[id] {
			t.Errorf("%s did not warn with a reason, so this test proves nothing about its text", id)
		}
	}
}
