package backend

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

const stubVersion = "9.9.9"

// stubListener stands in for Runner.Listener: it traps the SIGINT that Stop
// sends, announces itself, and then exits cleanly when interrupted, the way the
// real runner finishes its job and leaves.
//
// The trap comes before the announcement, and that order is the whole contract
// with waitForLog: a shell that has not installed its trap yet is killed
// outright by SIGINT, so a test that signals on the strength of the
// announcement must not be able to arrive in between the two. It could -- the
// announcement was first -- and CI found the window often enough to turn the
// clean exit this test is about into a runner "killed with code -1".
const stubListener = `#!/bin/sh
trap 'echo "interrupted"; exit 0' INT
echo "$@" > listener-args.txt
printf '%s' "${ACTIONS_RUNNER_INPUT_JITCONFIG:-}" > listener-jitconfig.txt
printf '%s' "${ACTIONS_RUNNER_INPUT_TOKEN:-}" > listener-token.txt
echo "listener started with $1"
i=0
while [ $i -lt 60 ]; do
  sleep 1
  i=$((i + 1))
done
echo "gave up waiting"
exit 9
`

const stubConfigOK = `#!/bin/sh
echo "$@" > config-args.txt
printf '%s' "${ACTIONS_RUNNER_INPUT_TOKEN:-}" > config-token.txt
echo "runner registered"
exit 0
`

const stubConfigFails = `#!/bin/sh
echo "Http response code: NotFound from 'POST https://api.github.com/actions/runner-registration'"
exit 1
`

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d/stat", os.Getpid())); errors.Is(err, os.ErrNotExist) {
			t.Skip("native integration needs a proc mount matching its PID namespace")
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("the process backend signals with SIGINT and runs shell stubs, neither of which exists on Windows")
	}
}

// requirePOSIX skips a test whose subject is a POSIX fact: a Unix socket
// path, a uid, a file mode, an absolute path that starts with a slash. The
// Docker and Podman backends are not offered on Windows, and the Windows
// half of the process backend has tests of its own, so nothing here is left
// unproved by the skip.
func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this test is about a POSIX fact (socket paths, uids, modes or rooted paths) with no Windows counterpart")
	}
}

// installStubRunner lays out a fake actions/runner release so that Create finds
// one already installed and never reaches the network.
func installStubRunner(t *testing.T, root, version, listener, config string) {
	t.Helper()
	dir := filepath.Join(root, toolsDirName, version)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, listenerPath), []byte(listener), 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, configScript), []byte(config), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func newStubProcessBackend(t *testing.T) (*ProcessBackend, string) {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d/stat", os.Getpid())); errors.Is(err, os.ErrNotExist) {
			t.Skip("native process integration requires /proc mounted for this PID namespace")
		}
	}

	t.Helper()
	root := t.TempDir()
	installStubRunner(t, root, stubVersion, stubListener, stubConfigOK)
	b, err := NewProcess(ProcessOptions{WorkDir: root, RunnerVersion: stubVersion, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	return b, root
}

func processSpec() Spec {
	return Spec{
		Name:        "zoomies-host-a1b2",
		RunnerID:    "run_9",
		PoolID:      "pool_9",
		PoolName:    "hosted",
		Ephemeral:   true,
		DockerMode:  store.DockerNone,
		Credentials: Credentials{JITConfig: "eyJhbGciOi"},
	}
}

// waitForPhase polls a status until it reaches want, which beats sleeping.
func waitForPhase(t *testing.T, b *ProcessBackend, h Handle, want Phase, within time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(within)
	var last Status
	for time.Now().Before(deadline) {
		st, err := b.Status(context.Background(), h)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		last = st
		if st.Phase == want {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("phase is %q after %s, want %q (%s)", last.Phase, within, want, last.Message)
	return last
}

// waitForLog blocks until the runner has written something recognisable, which
// is how a test knows the stub is past its own startup. Signalling a shell
// before it has installed its trap kills it outright -- a real race that Stop
// covers with SIGKILL, but not the behaviour under test here.
func waitForLog(t *testing.T, dir, want string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, runnerLogFile))
		if err == nil && strings.Contains(string(b), want) {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared in %s", want, filepath.Join(dir, runnerLogFile))
	return ""
}

func TestProcessCreateLayout(t *testing.T) {
	requireUnix(t)
	b, root := newStubProcessBackend(t)
	ctx := context.Background()

	result, err := b.CreateWithResult(ctx, processSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if result.ImagePullDuration != nil {
		t.Fatalf("process backend invented an image pull duration: %v", *result.ImagePullDuration)
	}
	h := result.Handle
	t.Cleanup(func() { _ = b.Remove(context.Background(), h) })

	dir := string(h)
	if dir != filepath.Join(root, runnersDirName, "zoomies-host-a1b2") {
		t.Fatalf("handle = %q", dir)
	}
	for _, name := range []string{runnerPIDFile, runnerMetaFile, runnerLogFile, listenerPath, runnerWorkDir} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}

	meta, err := readMeta(dir)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta.RunnerID != "run_9" || meta.PoolID != "pool_9" || meta.Version != stubVersion {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.PID != readPID(dir) {
		t.Fatalf("pid file %d disagrees with the metadata %d", readPID(dir), meta.PID)
	}

	st := waitForPhase(t, b, h, PhaseRunning, 5*time.Second)
	if st.StartedAt.IsZero() {
		t.Fatal("a running runner needs a start time")
	}

	// The listener is invoked with the JIT flag, which is what makes it
	// ephemeral and registration-free.
	waitForLog(t, dir, "listener started", 5*time.Second)
	logs, err := b.Logs(ctx, h, LogOptions{})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	defer logs.Close()
	out, _ := io.ReadAll(logs)
	if !strings.Contains(string(out), "listener started with run") {
		t.Fatalf("log = %q", out)
	}

	list, err := b.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].RunnerID != "run_9" || list[0].Handle != h {
		t.Fatalf("list = %+v", list)
	}
}

func TestProcessStopSignalsTheRunner(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()

	h, err := b.Create(ctx, processSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForPhase(t, b, h, PhaseRunning, 5*time.Second)
	waitForLog(t, string(h), "listener started", 5*time.Second)

	if err := b.Stop(ctx, h, 10*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	st := waitForPhase(t, b, h, PhaseExited, 10*time.Second)
	if st.ExitCode != 0 {
		t.Fatalf("exit code = %d; SIGINT must let the runner finish cleanly", st.ExitCode)
	}
	if st.ExitedAt.IsZero() {
		t.Fatal("an exited runner needs an exit time")
	}

	logs, _ := b.Logs(ctx, h, LogOptions{})
	out, _ := io.ReadAll(logs)
	_ = logs.Close()
	if !strings.Contains(string(out), "interrupted") {
		t.Fatalf("the runner was not interrupted: %q", out)
	}

	// Stopping something that has already stopped is not an error.
	if err := b.Stop(ctx, h, time.Second); err != nil {
		t.Fatalf("second stop: %v", err)
	}

	if err := b.Remove(ctx, h); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(string(h)); !os.IsNotExist(err) {
		t.Fatal("remove left the runner directory behind")
	}
	if err := b.Remove(ctx, h); err != nil {
		t.Fatalf("removing what is already gone must succeed: %v", err)
	}
	if _, err := b.Status(ctx, h); err == nil {
		t.Fatal("status of a removed runner must fail")
	}
}

func TestProcessCreateReplacesAnExistingRunner(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()

	h1, err := b.Create(ctx, processSpec())
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	waitForPhase(t, b, h1, PhaseRunning, 5*time.Second)
	waitForLog(t, string(h1), "listener started", 5*time.Second)
	first := readPID(string(h1))

	h2, err := b.Create(ctx, processSpec())
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), h2) })
	if h2 != h1 {
		t.Fatalf("a redelivered create must converge on the same handle: %q then %q", h1, h2)
	}
	waitForPhase(t, b, h2, PhaseRunning, 5*time.Second)
	if second := readPID(string(h2)); second == first {
		t.Fatal("the old process was reused instead of replaced")
	}
	if processAlive(first) {
		t.Fatal("the replaced runner is still running")
	}
}

func TestProcessRegistrationTokenPath(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()

	spec := processSpec()
	spec.Ephemeral = false
	spec.Credentials = Credentials{
		RegistrationToken: "AABBCC",
		URL:               "https://github.com/acme",
		RunnerGroup:       "builders",
		Labels:            []string{"linux-x64"},
	}
	h, err := b.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), h) })

	args, err := os.ReadFile(filepath.Join(string(h), "config-args.txt"))
	if err != nil {
		t.Fatalf("config.sh did not run: %v", err)
	}
	for _, want := range []string{"--unattended", "--url https://github.com/acme", "--name zoomies-host-a1b2", "--labels linux-x64", "--runnergroup builders", "--disableupdate"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("config.sh missing %q, got %q", want, args)
		}
	}
	if strings.Contains(string(args), "--ephemeral") {
		t.Error("a non-ephemeral pool must not be configured as ephemeral")
	}
	if strings.Contains(string(args), "--no-default-labels") {
		t.Error("a pool that keeps the default labels must not drop them")
	}
	waitForPhase(t, b, h, PhaseRunning, 5*time.Second)
}

// The registration token registers a runner into the organisation, and argv is
// readable by every local account through ps and /proc/<pid>/cmdline for as
// long as config.sh runs. The environment is readable by this account and root
// only, which is how the JIT config already travels.
func TestProcessKeepsTheRegistrationTokenOffTheCommandLine(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)

	spec := processSpec()
	spec.Ephemeral = false
	spec.Credentials = Credentials{RegistrationToken: "AABBCC", URL: "https://github.com/acme"}
	h, err := b.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), h) })
	dir := string(h)

	args, err := os.ReadFile(filepath.Join(dir, "config-args.txt"))
	if err != nil {
		t.Fatalf("config.sh did not run: %v", err)
	}
	if strings.Contains(string(args), "AABBCC") || strings.Contains(string(args), "--token") {
		t.Fatalf("the registration token is on config.sh's command line, where ps can read it: %q", args)
	}
	token, err := os.ReadFile(filepath.Join(dir, "config-token.txt"))
	if err != nil {
		t.Fatalf("reading config.sh's environment: %v", err)
	}
	if string(token) != "AABBCC" {
		t.Fatalf("%s = %q, want the registration token", EnvUpstreamRegistrationToken, token)
	}

	// The token has done its work once config.sh exits; the listener lives for
	// hours and must not carry it in its environment.
	waitForLog(t, dir, "listener started", 5*time.Second)
	got, err := os.ReadFile(filepath.Join(dir, "listener-token.txt"))
	if err != nil {
		t.Fatalf("reading the listener's environment: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("the long-lived listener inherited the registration token: %q", got)
	}
}

func TestProcessRegistrationCanLeaveOutTheDefaultLabels(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	spec := processSpec()
	spec.Ephemeral = false
	spec.Credentials = Credentials{
		RegistrationToken: "AABBCC",
		URL:               "https://github.com/acme",
		Labels:            []string{"gpu"},
		NoDefaultLabels:   true,
	}
	h, err := b.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), h) })
	args, err := os.ReadFile(filepath.Join(string(h), "config-args.txt"))
	if err != nil {
		t.Fatalf("config.sh did not run: %v", err)
	}
	if !strings.Contains(string(args), "--no-default-labels") {
		t.Fatalf("config.sh was not told to leave out the default labels: %q", args)
	}
}

func TestProcessRegistrationFailureIsExplained(t *testing.T) {
	requireUnix(t)
	root := t.TempDir()
	installStubRunner(t, root, stubVersion, stubListener, stubConfigFails)
	b, err := NewProcess(ProcessOptions{WorkDir: root, RunnerVersion: stubVersion, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	spec := processSpec()
	spec.Credentials = Credentials{RegistrationToken: "expired", URL: "https://github.com/acme"}
	_, err = b.Create(context.Background(), spec)
	if err == nil {
		t.Fatal("a failed registration must not look like success")
	}
	if !strings.Contains(err.Error(), "runner-registration") {
		t.Fatalf("the runner's own output is what an operator needs: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, runnersDirName, "zoomies-host-a1b2")); statErr == nil {
		t.Fatal("a failed create left its directory behind")
	}
}

func TestProcessCreateRefusesDinD(t *testing.T) {
	b, _ := newStubProcessBackend(t)
	spec := processSpec()
	spec.DockerMode = store.DockerDinD
	if _, err := b.Create(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "docker backend") {
		t.Fatalf("got %v", err)
	}
}

func TestProcessStatusOfAnUnreapedRunner(t *testing.T) {
	// An agent that restarted while its runner died records no exit code; the
	// status has to say so rather than reporting a healthy runner.
	dir := t.TempDir()
	if err := writeMeta(dir, processMeta{Name: "r", PID: 1 << 30, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runnerPIDFile), []byte("1073741824"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := newStubProcessBackend(t)

	st, err := b.Status(context.Background(), Handle(dir))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Phase != PhaseExitUnknown {
		t.Fatalf("phase = %q, want exit_unknown", st.Phase)
	}
	if !strings.Contains(st.Message, runnerLogFile) {
		t.Fatalf("the message must point at the log: %q", st.Message)
	}
	// The same observation while this agent still owns the waiter is not an
	// unknown exit: the exit record has not landed yet.
	b.running[dir] = &child{}
	st, err = b.Status(context.Background(), Handle(dir))
	if err != nil || st.Phase != PhaseRunning {
		t.Fatalf("the waiter must finish recording before classification: %+v, %v", st, err)
	}
	writeExit(dir, 23)
	delete(b.running, dir)
	st, err = b.Status(context.Background(), Handle(dir))
	if err != nil || st.Phase != PhaseFailed || st.ExitCode != 23 {
		t.Fatalf("a known failure must not be replaced by unknown: %+v, %v", st, err)
	}
}

func TestProcessStatusOfSomethingGone(t *testing.T) {
	b, _ := newStubProcessBackend(t)
	_, err := b.Status(context.Background(), Handle(filepath.Join(t.TempDir(), "absent")))
	if err == nil {
		t.Fatal("status of a missing runner must fail")
	}
	if !isNotFound(err) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func isNotFound(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") }

func TestProcessProbe(t *testing.T) {
	b, root := newStubProcessBackend(t)
	info := b.Probe(context.Background())

	if info.Kind != store.BackendProcess {
		t.Fatalf("kind = %q", info.Kind)
	}
	if info.Endpoint != root {
		t.Fatalf("endpoint = %q, want the work directory", info.Endpoint)
	}
	if info.SupportsDinD {
		t.Fatal("the process backend has no container to nest in")
	}
	if info.Detail == "" {
		t.Fatal("a probe must always explain itself")
	}
	// Availability depends on the host: on Linux without libicu the runner
	// cannot start, and the detail has to say which of the two it is.
	if !info.Available && !strings.Contains(info.Detail, "libicu") && !strings.Contains(info.Detail, "does not support") && !strings.Contains(info.Detail, "work directory") && !strings.Contains(info.Detail, "shell") {
		t.Fatalf("unavailable for an unexplained reason: %q", info.Detail)
	}
}

// The published image is distroless. An agent in it probing the process
// backend used to be told to apt-get install libicu -- into an image with no
// apt, no shell and no way to run the runner at all -- when the honest answer
// is that this backend is not for containers.
func TestProcessProbeWithoutAShellSaysSoBeforeAnythingElse(t *testing.T) {
	requirePOSIX(t)
	b, _ := newStubProcessBackend(t)
	t.Setenv("PATH", t.TempDir())

	info := b.Probe(context.Background())
	if info.Available {
		t.Fatal("a host with no shell cannot run the runner")
	}
	if !strings.Contains(info.Detail, "no shell is installed") || !strings.Contains(info.Detail, "docker or podman backend") {
		t.Fatalf("detail = %q, want the missing shell and the backend to use instead", info.Detail)
	}
	if strings.Contains(info.Detail, "apt-get") {
		t.Fatalf("a package manager is no use in an image without one: %q", info.Detail)
	}
}

func TestRunnerAsset(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "actions-runner-linux-x64-2.3.4.tar.gz"},
		{"linux", "arm64", "actions-runner-linux-arm64-2.3.4.tar.gz"},
		{"linux", "arm", "actions-runner-linux-arm-2.3.4.tar.gz"},
		{"darwin", "arm64", "actions-runner-osx-arm64-2.3.4.tar.gz"},
		// Windows releases are zips, and the extension is what picks the
		// unpacker, so the name has to carry it.
		{"windows", "amd64", "actions-runner-win-x64-2.3.4.zip"},
		{"windows", "arm64", "actions-runner-win-arm64-2.3.4.zip"},
	}
	for _, c := range cases {
		got, err := runnerAsset(c.goos, c.goarch, "2.3.4")
		if err != nil || got != c.want {
			t.Errorf("%s/%s -> %q, %v; want %q", c.goos, c.goarch, got, err, c.want)
		}
	}

	for _, c := range []struct{ goos, goarch string }{
		{"linux", "riscv64"},
		{"windows", "arm"},
		{"darwin", "arm"},
		{"freebsd", "amd64"},
	} {
		if _, err := runnerAsset(c.goos, c.goarch, "2.3.4"); err == nil {
			t.Errorf("%s/%s must be refused: actions/runner ships no such build", c.goos, c.goarch)
		}
	}
}

// A Windows release is a zip, and a zip is remote input with the same escape
// routes a tarball has: a name that climbs out, an absolute name, a symlink
// to write through. Each is refused, and a well-formed archive lands with the
// listener where ensureRelease looks for it.
func TestExtractZip(t *testing.T) {
	write := func(t *testing.T, entries map[string]string) string {
		t.Helper()
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range entries {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(t.TempDir(), "runner.zip")
		if err := os.WriteFile(src, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return src
	}

	dest := filepath.Join(t.TempDir(), "tools")
	src := write(t, map[string]string{
		"bin/Runner.Listener.exe": "MZ",
		"config.cmd":              "@echo off\r\n",
		"docs/":                   "",
		"docs/readme.txt":         "hello",
	})
	if err := extractZip(src, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, name := range []string{"bin/Runner.Listener.exe", "config.cmd", "docs/readme.txt"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s missing after extraction: %v", name, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dest, "docs", "readme.txt")); err != nil || string(b) != "hello" {
		t.Errorf("readme = %q, %v", b, err)
	}

	for name, entries := range map[string]map[string]string{
		"a climbing name":  {"../evil.txt": "x"},
		"an absolute name": {"/etc/evil.txt": "x"},
	} {
		src := write(t, entries)
		if err := extractZip(src, filepath.Join(t.TempDir(), "tools")); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// Windows refuses to delete a file another process still has open, and a
// runner's handles can outlive its termination by a moment. The removal keeps
// trying for a bounded grace and then reports the last error, so that the
// usual case converges and the unusual one is still recorded rather than
// retried forever.
func TestRemoveAllRetryKeepsTryingWithinItsGrace(t *testing.T) {
	calls := 0
	flaky := func(string) error {
		calls++
		if calls < 3 {
			return errors.New("The process cannot access the file because it is being used by another process.")
		}
		return nil
	}
	if err := removeAllRetry(context.Background(), "dir", 5*time.Second, flaky); err != nil {
		t.Fatalf("a removal that succeeds on the third try must succeed: %v", err)
	}
	if calls != 3 {
		t.Errorf("remove was called %d times, want 3", calls)
	}

	// Without a grace the first answer is the answer, which is what POSIX
	// gets: there is nothing to wait for there.
	calls = 0
	if err := removeAllRetry(context.Background(), "dir", 0, flaky); err == nil {
		t.Fatal("with no grace, the first failure must be returned")
	}
	if calls != 1 {
		t.Errorf("remove was called %d times with no grace, want 1", calls)
	}

	// A grace that runs out returns the last error rather than nil.
	always := func(string) error { return errors.New("still open") }
	if err := removeAllRetry(context.Background(), "dir", 150*time.Millisecond, always); err == nil {
		t.Fatal("a removal that never succeeds must report the last error")
	}
}

func TestVerifyRunnerDownload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.tar.gz")
	payload := []byte("pretend this is a runner release")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	t.Run("no digest and no opt-in is refused", func(t *testing.T) {
		b := &ProcessBackend{log: quietLogger()}
		err := b.verify(path, "2.3.4", "asset.tar.gz")
		if err == nil {
			t.Fatal("an unverifiable download was accepted")
		}
		if !strings.Contains(err.Error(), "runner_sha256") {
			t.Fatalf("the message must name the setting that fixes it: %v", err)
		}
	})

	t.Run("opt-in accepts it", func(t *testing.T) {
		b := &ProcessBackend{log: quietLogger(), allowUnverified: true}
		if err := b.verify(path, "2.3.4", "asset.tar.gz"); err != nil {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("matching digest", func(t *testing.T) {
		b := &ProcessBackend{log: quietLogger(), sha256: digest}
		if err := b.verify(path, "2.3.4", "asset.tar.gz"); err != nil {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("mismatched digest is refused even with the opt-in", func(t *testing.T) {
		b := &ProcessBackend{log: quietLogger(), sha256: strings.Repeat("0", 64), allowUnverified: true}
		err := b.verify(path, "2.3.4", "asset.tar.gz")
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("got %v", err)
		}
		if !strings.Contains(err.Error(), "has not been installed") {
			t.Fatalf("the message must say what happened to the download: %v", err)
		}
	})
}

func TestExtractTarGz(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name string
		mode int64
		body string
	}{
		{listenerPath, 0o755, "#!/bin/sh\n"},
		{configScript, 0o755, "#!/bin/sh\n"},
		{"docs/readme.txt", 0o644, "hello"},
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "runner.tar.gz")
	if err := os.WriteFile(src, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "tools")
	if err := extractTarGz(src, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dest, listenerPath))
	if err != nil {
		t.Fatalf("listener missing: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the listener must stay executable, mode %v", fi.Mode())
	}
}

func TestSafeJoinRejectsEscapes(t *testing.T) {
	requirePOSIX(t)
	root := "/srv/tools"
	if _, err := safeJoin(root, "../../etc/passwd"); err == nil {
		t.Fatal("a traversing entry was accepted")
	}
	if _, err := safeJoin(root, "bin/../../../etc/passwd"); err == nil {
		t.Fatal("a traversing entry was accepted")
	}
	// An absolute entry is confined rather than refused: it names a path inside
	// the archive, not on the host.
	if got, err := safeJoin(root, "/etc/passwd"); err != nil || got != "/srv/tools/etc/passwd" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err := safeJoin(root, "bin/Runner.Listener")
	if err != nil || got != "/srv/tools/bin/Runner.Listener" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCloneTree(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "runner")
	if err := os.MkdirAll(filepath.Join(src, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "bin/tool"), []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cloneTree(src, dst); err != nil {
		t.Fatalf("clone: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "bin/tool"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("got %q, %v", got, err)
	}
	fi, err := os.Stat(filepath.Join(dst, "bin/tool"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode = %v, want the executable bit preserved", fi.Mode())
	}
}

func TestSeekToLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := seekToLastLines(f, 2); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got, _ := io.ReadAll(f)
	if string(got) != "three\nfour\n" {
		t.Fatalf("got %q", got)
	}

	// Asking for more lines than the file holds yields the whole file.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := seekToLastLines(f, 100); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got, _ = io.ReadAll(f)
	if string(got) != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFollowReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.log")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	var exited atomic.Bool
	fr := &followReader{ctx: context.Background(), f: f, poll: 10 * time.Millisecond, finished: exited.Load}
	go func() {
		time.Sleep(50 * time.Millisecond)
		w, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = w.WriteString("two\n")
		_ = w.Close()
		exited.Store(true)
	}()

	got, err := io.ReadAll(fr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "one\ntwo\n" {
		t.Fatalf("got %q, want the lines written after the reader started", got)
	}
	if err := fr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestFollowReaderStopsWithTheContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.log")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fr := &followReader{ctx: ctx, f: f, poll: 10 * time.Millisecond}
	defer fr.Close()

	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(fr)
		done <- b
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case got := <-done:
		if string(got) != "one\n" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled follow must end the stream")
	}
}

func TestNewProcessNeedsAWorkDir(t *testing.T) {
	_, err := NewProcess(ProcessOptions{})
	if err == nil || !strings.Contains(err.Error(), "work_dir") {
		t.Fatalf("got %v, want the setting named", err)
	}
}

// tarGzWithListener builds a minimal actions/runner release archive.
func tarGzWithListener(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := stubListener
	if err := tw.WriteHeader(&tar.Header{Name: listenerPath, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestProcessEnsureRelease(t *testing.T) {
	requirePOSIX(t)
	if _, err := runnerAsset(runtime.GOOS, runtime.GOARCH, "2.3.4"); err != nil {
		t.Skipf("no actions/runner release for this host: %v", err)
	}
	archive := tarGzWithListener(t)
	sum := sha256.Sum256(archive)

	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "actions-runner-") || !strings.HasSuffix(r.URL.Path, ".tar.gz") {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	root := t.TempDir()
	b, err := NewProcess(ProcessOptions{
		WorkDir:         root,
		RunnerVersion:   "2.3.4",
		RunnerSHA256:    hex.EncodeToString(sum[:]),
		DownloadBaseURL: srv.URL,
		Logger:          quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	dir, err := b.ensureRelease(context.Background(), "2.3.4")
	if err != nil {
		t.Fatalf("ensureRelease: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, listenerPath)); err != nil {
		t.Fatalf("the release was not installed: %v", err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloads = %d", downloads.Load())
	}

	// A second create must reuse what is already on disk.
	if _, err := b.ensureRelease(context.Background(), "2.3.4"); err != nil {
		t.Fatalf("second ensureRelease: %v", err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("the cached release was downloaded again (%d times)", downloads.Load())
	}

	// Nothing partial is left behind for the next run to trip over.
	entries, err := os.ReadDir(filepath.Join(root, toolsDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download") || strings.HasSuffix(e.Name(), ".incoming") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestProcessEnsureReleaseRejectsATamperedArchive(t *testing.T) {
	if _, err := runnerAsset(runtime.GOOS, runtime.GOARCH, "2.3.4"); err != nil {
		t.Skipf("no actions/runner release for this host: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not the release you asked for"))
	}))
	defer srv.Close()

	root := t.TempDir()
	b, err := NewProcess(ProcessOptions{
		WorkDir: root, RunnerVersion: "2.3.4",
		RunnerSHA256:    strings.Repeat("a", 64),
		DownloadBaseURL: srv.URL, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	if _, err := b.ensureRelease(context.Background(), "2.3.4"); err == nil {
		t.Fatal("a tampered archive was installed")
	} else if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, toolsDirName, "2.3.4")); err == nil {
		t.Fatal("a failed verification must leave nothing installed")
	}
}

func TestProcessDownloadMissingVersion(t *testing.T) {
	if _, err := runnerAsset(runtime.GOOS, runtime.GOARCH, "9.9.9"); err != nil {
		t.Skipf("no actions/runner release for this host: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer srv.Close()

	b, err := NewProcess(ProcessOptions{
		WorkDir: t.TempDir(), DownloadBaseURL: srv.URL,
		AllowUnverifiedDownload: true, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	_, err = b.ensureRelease(context.Background(), "0.0.1")
	if err == nil || !strings.Contains(err.Error(), "pinned runner version") {
		t.Fatalf("got %v, want a message about the pinned version", err)
	}
}

// The runner image verifies its download against digests written into the
// Dockerfile, and the process backend against the generated table. They are
// the same release, so they had better be the same numbers; the bump workflow
// copies them from the table, and this is what catches a hand edit of one.
func TestTheRunnerImagePinsTheSameDigestsAsTheProcessBackend(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "deploy", "Dockerfile.runner"))
	if err != nil {
		t.Fatalf("reading the runner Dockerfile: %v", err)
	}
	for arch, arg := range map[string]string{"x64": "RUNNER_SHA256_X64", "arm64": "RUNNER_SHA256_ARM64"} {
		want := knownRunnerSHA256[DefaultRunnerVersion+"/actions-runner-linux-"+arch+"-"+DefaultRunnerVersion+".tar.gz"]
		if want == "" {
			t.Fatalf("the digest table has no linux-%s entry for %s", arch, DefaultRunnerVersion)
		}
		if !strings.Contains(string(dockerfile), "ARG "+arg+"="+want) {
			t.Fatalf("deploy/Dockerfile.runner does not pin %s to the table's digest %s for %s", arg, want, DefaultRunnerVersion)
		}
	}
	if !strings.Contains(string(dockerfile), "ARG RUNNER_VERSION="+DefaultRunnerVersion) {
		t.Fatalf("deploy/Dockerfile.runner pins a different runner version from DefaultRunnerVersion %s", DefaultRunnerVersion)
	}
}

// The JIT config is a credential. On the command line it is in
// /proc/<pid>/cmdline, which every account on the host can read; the runner
// takes it from the environment just as happily, and the container backends
// already hand it over that way.
func TestProcessKeepsTheJITConfigOffTheCommandLine(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()

	spec := processSpec()
	h, err := b.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(context.Background(), h) })

	dir := string(h)
	waitForLog(t, dir, "listener started", 5*time.Second)

	args, err := os.ReadFile(filepath.Join(dir, "listener-args.txt"))
	if err != nil {
		t.Fatalf("reading the listener's arguments: %v", err)
	}
	if got := strings.TrimSpace(string(args)); got != "run" {
		t.Fatalf("listener arguments = %q, want just \"run\"", got)
	}
	if strings.Contains(string(args), spec.Credentials.JITConfig) {
		t.Fatal("the JIT config is on the command line, where ps can read it")
	}

	jit, err := os.ReadFile(filepath.Join(dir, "listener-jitconfig.txt"))
	if err != nil {
		t.Fatalf("reading the listener's environment: %v", err)
	}
	if string(jit) != spec.Credentials.JITConfig {
		t.Fatalf("%s = %q, want the JIT config", EnvUpstreamJITConfig, jit)
	}
}

// stubDeaf ignores the interrupt Stop sends first, so that stopping it has to
// go through the kill, which is the path this file's other tests never take.
//
// Ignoring a signal is installed the same way as handling one, so the order
// here matters for the same reason it does in stubListener: announce after the
// trap, or a SIGINT that lands in between kills the shell and the kill path
// this test exists for is never reached.
const stubDeaf = `#!/bin/sh
trap '' INT
echo "listener started with $1"
i=0
while [ $i -lt 120 ]; do
  sleep 1
  i=$((i + 1))
done
`

// SIGKILL is delivered, not applied: the process is still there, with its files
// still open, for as long as the kernel takes to tear it down. Stop used to
// return the moment the signal was sent, and what the caller does next is
// delete the directory the runner is running in -- so a create replacing a
// runner could fail on a directory that would not stay empty, and a remove
// could half-succeed against a process still writing into it.
func TestStoppingARunnerThatIgnoresTheInterruptWaitsForItToDie(t *testing.T) {
	requireUnix(t)
	root := t.TempDir()
	installStubRunner(t, root, stubVersion, stubDeaf, stubConfigOK)
	b, err := NewProcess(ProcessOptions{WorkDir: root, RunnerVersion: stubVersion, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	ctx := context.Background()

	h, err := b.Create(ctx, processSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForPhase(t, b, h, PhaseRunning, 5*time.Second)
	waitForLog(t, string(h), "listener started", 5*time.Second)
	pid := readPID(string(h))
	if pid <= 0 || !processAlive(pid) {
		t.Fatalf("the runner is not running to begin with, pid = %d", pid)
	}

	// A short interrupt timeout, so the kill is reached quickly. The runner
	// ignores the interrupt, so this is the only thing that ends it.
	if err := b.Stop(ctx, h, 300*time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if processAlive(pid) {
		t.Fatal("Stop returned while the runner it killed was still running")
	}
	// And the directory it was running in can now be removed, which is what
	// the caller does next.
	if err := b.Remove(ctx, h); err != nil {
		t.Fatalf("removing a runner that has just been killed: %v", err)
	}
}

// The process being gone is not the end of the writing: the goroutine reaping
// it records the exit code in the runner's own directory, and it gets there a
// moment after the process disappears -- a moment the removal is already
// spending inside RemoveAll. The walk deletes what it found, the exit record
// lands behind it, and the rmdir meets a directory that is not empty. CI found
// this as "directory not empty" from a create replacing an existing runner,
// which is a fleet failing to start a runner rather than a test being unlucky.
//
// Staged rather than raced: a reaper that never finishes, so that whether the
// removal waits for one is the only thing being measured. Waiting for a real
// exit to be recorded takes microseconds, and a test that hopes to land inside
// those is a test that passes whatever the code does.
func TestRemovingARunnerWaitsForItsExitToBeRecorded(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)

	dir := b.runnerDir("zoomies-host-a1b2")
	if err := os.MkdirAll(filepath.Join(dir, runnerWorkDir), 0o750); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.running[dir] = &child{reaped: make(chan struct{})}
	b.mu.Unlock()

	// The context is what ends the wait, since this reaper never will. Without
	// a wait at all the removal returns in microseconds.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := b.Remove(ctx, Handle(dir)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if waited := time.Since(started); waited < 200*time.Millisecond {
		t.Fatalf("Remove returned after %s, so it deleted the directory while the exit was still being recorded into it", waited)
	}
	// And it still removes the directory: a reaper that will not finish is a
	// reason to wait, not a reason to leave a runner on the host for ever.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the runner directory is still there: %v", err)
	}
}

// The same sequence the CI failure came from, run enough times that the window
// between the process exiting and its exit record landing is hit rather than
// hoped past.
func TestReplacingARunnerRepeatedlyNeverTripsOverItsOwnExitRecord(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()

	for i := range 12 {
		h, err := b.Create(ctx, processSpec())
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		waitForPhase(t, b, h, PhaseRunning, 5*time.Second)
		waitForLog(t, string(h), "listener started", 5*time.Second)
	}
	if err := b.Remove(ctx, Handle(b.runnerDir(processSpec().Name))); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

// TestProcessChildEnvironmentIsBuiltNotInherited is the process backend's
// side of the isolation the container backends get from the container. A
// job's steps run as ordinary processes on the agent's machine, so anything
// left in the agent's environment would be readable by every workflow the
// fleet runs -- and on a single-VM install the agent is inside the controller,
// whose environment holds the database path, the encryption key and the GitHub
// App's private key.
//
// The parent is poisoned with the things that would actually be there.
func TestProcessChildEnvironmentIsBuiltNotInherited(t *testing.T) {
	b := &ProcessBackend{}

	poison := map[string]string{
		"ZOOMIES_ENCRYPTION_KEY":        "3d5f0a1b-the-key-that-decrypts-every-installation",
		"ZOOMIES_GITHUB_PRIVATE_KEY":    "-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n",
		"ZOOMIES_GITHUB_WEBHOOK_SECRET": "the-webhook-hmac-secret",
		"ZOOMIES_DATABASE":              "/var/lib/zoomies/zoomies.db",
		"ZOOMIES_AGENT_TOKEN":           "zag_this_hosts_own_credential",
		"GITHUB_TOKEN":                  "ghp_a_token_the_operator_exported",
		"AWS_SECRET_ACCESS_KEY":         "an-unrelated-secret-that-happened-to-be-there",
		"SSH_AUTH_SOCK":                 "/tmp/ssh-agent.sock",
	}
	for k, v := range poison {
		t.Setenv(k, v)
	}
	// The agent's own home must not become the runner's: the runner writes its
	// credentials and state next to it.
	t.Setenv("HOME", "/root")

	dir := t.TempDir()
	spec := Spec{Name: "zoomies-runner-1", Ephemeral: true, Env: map[string]string{
		"RUNNER_ALLOW_RUNASROOT": "1",
		"ZOOMIES_POOL":           "linux-x64",
	}}
	env := b.childEnv(spec, dir)

	got := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("childEnv produced %q, which is not a KEY=VALUE pair", kv)
		}
		got[k] = v
	}

	for k, v := range poison {
		if _, present := got[k]; present {
			t.Errorf("the runner's environment carries %s from the agent's", k)
		}
		// Not merely absent under that name: absent as a value, so a rename
		// or a well-meaning "pass the proxy settings through" that widened
		// the allowlist would still be caught.
		for _, kv := range env {
			if strings.Contains(kv, v) {
				t.Errorf("%s's value reached the runner as %q", k, kv)
			}
		}
	}

	if got["HOME"] != dir {
		t.Errorf("HOME = %q, want the runner's own directory %q", got["HOME"], dir)
	}
	if got["ZOOMIES_POOL"] != "linux-x64" {
		t.Errorf("the controller's own spec.Env did not reach the runner: %v", got)
	}
}

// TestProcessChildEnvironmentPassesTheAllowlistThrough is the other half: the
// allowlist exists because a runner behind a corporate proxy or in a non-UTC
// timezone needs those settings, and dropping them would make the backend
// unusable rather than safe. Both halves have to be asserted, or "return nil"
// would pass the test above.
func TestProcessChildEnvironmentPassesTheAllowlistThrough(t *testing.T) {
	b := &ProcessBackend{}

	allowed := map[string]string{
		"LANG":                                  "en_GB.UTF-8",
		"LC_ALL":                                "en_GB.UTF-8",
		"TZ":                                    "Europe/London",
		"HTTP_PROXY":                            "http://proxy.example.com:3128",
		"HTTPS_PROXY":                           "http://proxy.example.com:3128",
		"NO_PROXY":                              "localhost,10.0.0.0/8",
		"http_proxy":                            "http://proxy.example.com:3128",
		"https_proxy":                           "http://proxy.example.com:3128",
		"no_proxy":                              "localhost,10.0.0.0/8",
		"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1",
	}
	for k, v := range allowed {
		t.Setenv(k, v)
	}
	t.Setenv("PATH", "/opt/toolcache/bin:/usr/bin")

	env := b.childEnv(Spec{Name: "zoomies-runner-1"}, t.TempDir())
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}

	for k, want := range allowed {
		if got[k] != want {
			t.Errorf("%s = %q, want %q -- a runner needs the agent's proxy and locale settings", k, got[k], want)
		}
	}
	if got["PATH"] != "/opt/toolcache/bin:/usr/bin" {
		t.Errorf("PATH = %q, want the agent's -- it is how the runner finds its toolchain", got["PATH"])
	}

	// A variable on the allowlist that the agent does not have is left unset
	// rather than set empty: an empty TZ is not the same as no TZ, and the
	// runner's own defaulting is better than a blank.
	t.Setenv("TZ", "Europe/London") // registers the cleanup that restores it
	os.Unsetenv("TZ")
	withoutTZ := map[string]string{}
	for _, kv := range b.childEnv(Spec{Name: "zoomies-runner-1"}, t.TempDir()) {
		k, v, _ := strings.Cut(kv, "=")
		withoutTZ[k] = v
	}
	if v, present := withoutTZ["TZ"]; present {
		t.Errorf("TZ = %q was invented for a runner whose agent has no TZ set", v)
	}

	// And PATH is the one variable with a fallback, because a runner with no
	// PATH finds no tools at all -- not even the ones it downloaded.
	t.Setenv("PATH", "")
	for _, kv := range b.childEnv(Spec{Name: "zoomies-runner-1"}, t.TempDir()) {
		if kv == "PATH=" {
			t.Error("an empty PATH was passed through instead of the fallback")
		}
	}
}

// TestAnAbandonedRunnerDirectoryIsListedSoItCanBeReaped closes a leak with
// nothing else watching it.
//
// Create makes the runner's directory and clones the tools tree into it --
// hundreds of megabytes -- before it writes the metadata. An agent killed in
// that window leaves the tree behind, and List used to skip a directory whose
// metadata would not read. Nothing else walks this tree, so the copy stayed on
// the host until somebody went looking for the disk.
func TestAnAbandonedRunnerDirectoryIsListedSoItCanBeReaped(t *testing.T) {
	b, workDir := newStubProcessBackend(t)
	root := filepath.Join(workDir, runnersDirName)

	// The shape Create leaves behind when it dies between MkdirAll and
	// writeMeta: a directory, some of the tree, and no runner.json.
	dir := filepath.Join(root, "zoomies-abandoned")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatalf("staging the abandoned directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "Runner.Listener"), []byte("stub"), 0o750); err != nil {
		t.Fatalf("staging the tools copy: %v", err)
	}

	// Still being written: nothing may touch it yet.
	fresh, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, w := range fresh {
		if string(w.Handle) == dir {
			t.Fatal("a directory written moments ago was reported as abandoned; " +
				"a create in progress would be removed out from under itself")
		}
	}

	// Nothing has written to it for longer than a create could take.
	old := time.Now().Add(-abandonedGrace - time.Minute)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("ageing the directory: %v", err)
	}

	listed, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Workload
	for i, w := range listed {
		if string(w.Handle) == dir {
			found = &listed[i]
		}
	}
	if found == nil {
		t.Fatal("an abandoned runner directory was not listed, so nothing will ever remove it")
	}
	if found.Status.Phase != PhaseGone {
		t.Errorf("phase = %q, want %q: there is no process here to be anything else",
			found.Status.Phase, PhaseGone)
	}
	// No runner ID, because there is nothing to read one from. The agent's
	// orphan path removes the workload and reports nothing, so no row moves on
	// the strength of a directory nobody can identify.
	if found.RunnerID != "" {
		t.Errorf("runner = %q, want none: the metadata is what carries it and it is unreadable", found.RunnerID)
	}

	// And it can actually be removed, which is the whole point of listing it.
	if err := b.Remove(context.Background(), found.Handle); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the directory is still there after Remove: %v", err)
	}
}
