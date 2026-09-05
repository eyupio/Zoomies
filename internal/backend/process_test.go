package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

const stubVersion = "9.9.9"

// stubListener stands in for Runner.Listener: it announces itself, then waits
// for the SIGINT that Stop sends and exits cleanly, the way the real runner
// finishes its job and leaves.
const stubListener = `#!/bin/sh
echo "listener started with $1"
trap 'echo "interrupted"; exit 0' INT
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
echo "runner registered"
exit 0
`

const stubConfigFails = `#!/bin/sh
echo "Http response code: NotFound from 'POST https://api.github.com/actions/runner-registration'"
exit 1
`

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the process backend signals with SIGINT and runs shell stubs, neither of which exists on Windows")
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
	waitForPhase(t, b, h, PhaseRunning, 5*time.Second)
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
	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want failed", st.Phase)
	}
	if !strings.Contains(st.Message, runnerLogFile) {
		t.Fatalf("the message must point at the log: %q", st.Message)
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
	}
	for _, c := range cases {
		got, err := runnerAsset(c.goos, c.goarch, "2.3.4")
		if err != nil || got != c.want {
			t.Errorf("%s/%s -> %q, %v; want %q", c.goos, c.goarch, got, err, c.want)
		}
	}

	if _, err := runnerAsset("windows", "amd64", "2.3.4"); err == nil || !strings.Contains(err.Error(), "docker backend") {
		t.Errorf("windows should be refused with an alternative, got %v", err)
	}
	if _, err := runnerAsset("linux", "riscv64", "2.3.4"); err == nil {
		t.Error("an unsupported architecture must be refused")
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
		{"bin/Runner.Listener", 0o755, "#!/bin/sh\n"},
		{"config.sh", 0o755, "#!/bin/sh\n"},
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

// The runner leads a process group of its own. Without that, interrupting the
// listener orphaned the worker running the job, and a service manager stopping
// the agent's unit took every runner in the cgroup down with it -- the
// opposite of "restarting an agent must never kill a job".
func TestProcessRunnerLeadsItsOwnProcessGroup(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()

	h, err := b.Create(ctx, processSpec())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = b.Remove(ctx, h) })
	waitForPhase(t, b, h, PhaseRunning, 5*time.Second)

	pid := readPID(string(h))
	if pid <= 0 {
		t.Fatal("no pid recorded")
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	if pgid != pid {
		t.Fatalf("the runner's process group is %d, want its own pid %d; it is sharing the agent's group", pgid, pid)
	}
}
