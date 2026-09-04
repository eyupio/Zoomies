package backend

// The bare-process backend: the actions/runner binary, run directly on the
// host, with no container and no systemd unit.
//
// It exists for the two cases containers cannot serve -- a macOS host, and a
// build that needs hardware the daemon will not hand to a container -- and it
// makes a trade the operator must understand: a job on this backend runs as the
// agent's user, on the agent's filesystem, with the agent's network. Isolation
// comes from the ephemeral lifecycle and nothing else.
//
// Each runner gets its own directory under <WorkDir>/runners, laid out the way
// the runner expects, so two runners on one host never share credentials or a
// work folder.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// Layout of <WorkDir>.
const (
	toolsDirName   = "_tools"
	runnersDirName = "runners"

	runnerLogFile  = "runner.log"
	runnerPIDFile  = "runner.pid"
	runnerExitFile = "runner.exit"
	runnerMetaFile = "runner.json"
	runnerWorkDir  = "_work"

	// listenerPath is the binary run.sh eventually execs. Zoomies runs it
	// directly: run.sh is a wrapper that spawns the listener as a child, so a
	// SIGINT sent to the wrapper does not reliably reach the process that has
	// to act on it, and the pid we record would not be the pid we need.
	listenerPath = "bin/Runner.Listener"
	configScript = "config.sh"
)

// DefaultRunnerVersion is used when neither the pool nor the agent pins one.
const DefaultRunnerVersion = "2.328.0"

// defaultRunnerDownloadURL is where actions/runner releases live.
const defaultRunnerDownloadURL = "https://github.com/actions/runner/releases/download"

// configureTimeout bounds config.sh, which talks to GitHub and can hang.
const configureTimeout = 3 * time.Minute

// tailPoll is how often a followed log file is re-read. See followReader.
const tailPoll = 250 * time.Millisecond

// knownRunnerSHA256 holds digests Zoomies ships for the runner releases it has
// been tested against, keyed "<version>/<asset file name>".
//
// It is deliberately empty in the source tree rather than filled with digests
// that would go stale: an operator who pins a version supplies its digest with
// ProcessOptions.RunnerSHA256, and a release of Zoomies that pins a default
// runner version adds the entry here at the same time.
var knownRunnerSHA256 = map[string]string{}

// ProcessOptions configures the process backend.
type ProcessOptions struct {
	// WorkDir is the root of the layout above. It must be writable.
	WorkDir string
	// RunnerVersion is the actions/runner release used when a pool does not pin
	// one. Empty means DefaultRunnerVersion.
	RunnerVersion string
	// RunnerSHA256 is the expected digest of the runner archive for
	// RunnerVersion on this host's OS and architecture. Setting it is how an
	// operator gets a verified download for a version Zoomies does not ship a
	// digest for.
	RunnerSHA256 string
	// AllowUnverifiedDownload permits installing a runner archive whose digest
	// Zoomies cannot check. It is off by default: the alternative is executing
	// whatever the network handed us.
	AllowUnverifiedDownload bool
	// DownloadBaseURL overrides where releases are fetched from, for hosts that
	// mirror them internally.
	DownloadBaseURL string
	HTTPClient      *http.Client
	Logger          *slog.Logger
}

// ProcessBackend runs runners as ordinary processes on the host.
type ProcessBackend struct {
	root            string
	version         string
	sha256          string
	allowUnverified bool
	baseURL         string
	http            *http.Client
	log             *slog.Logger

	// mu guards running, which holds the children this agent started so that
	// exactly one goroutine waits on each of them.
	mu      sync.Mutex
	running map[string]*exec.Cmd
	// installing serialises release downloads so two concurrent creates do not
	// fetch the same archive twice.
	installing sync.Mutex
}

var _ Backend = (*ProcessBackend)(nil)

// NewProcess builds a process backend rooted at opts.WorkDir.
func NewProcess(opts ProcessOptions) (*ProcessBackend, error) {
	root := strings.TrimSpace(opts.WorkDir)
	if root == "" {
		return nil, errors.New("backend: the process backend needs agent.work_dir set to a writable directory")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("backend: resolving the work directory %s: %w", root, err)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	version := strings.TrimPrefix(strings.TrimSpace(opts.RunnerVersion), "v")
	if version == "" {
		version = DefaultRunnerVersion
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	base := strings.TrimSuffix(strings.TrimSpace(opts.DownloadBaseURL), "/")
	if base == "" {
		base = defaultRunnerDownloadURL
	}

	return &ProcessBackend{
		root:            abs,
		version:         version,
		sha256:          strings.ToLower(strings.TrimSpace(opts.RunnerSHA256)),
		allowUnverified: opts.AllowUnverifiedDownload,
		baseURL:         base,
		http:            client,
		log:             log.With("backend", string(store.BackendProcess)),
		running:         make(map[string]*exec.Cmd),
	}, nil
}

// Kind identifies the implementation.
func (b *ProcessBackend) Kind() store.BackendKind { return store.BackendProcess }

// Probe reports whether this host can actually run the runner binary.
//
// "Available" here means more than "the code compiled": the runner is a .NET
// application, and on Linux it refuses to start without ICU. That single
// missing library is the most common cause of a runner that dies seconds after
// launch with a stack trace nobody reads, so it is checked here and named.
func (b *ProcessBackend) Probe(ctx context.Context) Info {
	info := Info{
		Kind:     store.BackendProcess,
		Version:  b.version,
		Endpoint: b.root,
		Rootless: os.Geteuid() != 0,
	}

	if _, err := runnerAsset(runtime.GOOS, runtime.GOARCH, b.version); err != nil {
		info.Detail = err.Error()
		return info
	}
	if err := b.checkWritable(); err != nil {
		info.Detail = err.Error()
		return info
	}
	if err := checkICU(); err != nil {
		info.Detail = err.Error()
		return info
	}

	info.Available = true
	info.Detail = fmt.Sprintf("actions/runner %s on %s/%s, work directory %s", b.version, runtime.GOOS, runtime.GOARCH, b.root)
	if !info.Rootless {
		info.Detail += "; the agent is running as root, so every job runs as root on this host"
	}
	return info
}

func (b *ProcessBackend) checkWritable() error {
	if err := os.MkdirAll(b.root, 0o750); err != nil {
		return fmt.Errorf("work directory %s cannot be created: %v; point agent.work_dir somewhere this user can write", b.root, err)
	}
	f, err := os.CreateTemp(b.root, ".zoomies-write-check-*")
	if err != nil {
		return fmt.Errorf("work directory %s is not writable by this user: %v; fix its ownership or point agent.work_dir elsewhere", b.root, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// checkICU looks for the ICU libraries the runner's .NET runtime needs.
func checkICU() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if os.Getenv("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT") == "1" {
		return nil
	}
	globs := []string{
		"/usr/lib/libicuuc.so*",
		"/usr/lib64/libicuuc.so*",
		"/usr/lib/*/libicuuc.so*",
		"/lib/*/libicuuc.so*",
		"/usr/local/lib/libicuuc.so*",
		"/usr/local/lib/*/libicuuc.so*",
	}
	for _, g := range globs {
		if m, err := filepath.Glob(g); err == nil && len(m) > 0 {
			return nil
		}
	}
	return errors.New("libicu is not installed, and the actions/runner will exit at startup without it: install it (apt-get install -y libicu-dev, dnf install -y libicu) or set DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1 in the agent's environment")
}

// Create lays out one runner directory, registers it and starts it.
func (b *ProcessBackend) Create(ctx context.Context, spec Spec) (Handle, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.DockerMode == store.DockerDinD {
		return "", fmt.Errorf("backend: pool %q asks for docker-in-docker, which the process backend cannot provide; move the pool to the docker backend", spec.PoolName)
	}

	version := strings.TrimPrefix(strings.TrimSpace(spec.RunnerVersion), "v")
	if version == "" {
		version = b.version
	}
	tools, err := b.ensureRelease(ctx, version)
	if err != nil {
		return "", err
	}

	dir := b.runnerDir(spec.Name)
	if err := b.wipe(ctx, dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, runnerWorkDir), 0o750); err != nil {
		return "", fmt.Errorf("backend: creating the runner directory %s: %w", dir, err)
	}
	// Each runner needs its own copy of the tree, because the runner keeps its
	// credentials and its state next to the binary. Files are hard-linked where
	// the filesystem allows it, so the copy costs inodes rather than gigabytes.
	if err := cloneTree(tools, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("backend: laying out the runner in %s: %w", dir, err)
	}

	env := b.childEnv(spec, dir)
	args := []string{"run"}
	if jit := spec.Credentials.JITConfig; jit != "" {
		// The JIT config goes on the command line because that is the interface
		// the runner offers. It is single-use and expires in minutes, which is
		// what makes a value visible in ps acceptable here; a registration token
		// is not, and neither is anything else Zoomies holds.
		args = append(args, "--jitconfig", jit)
	} else if err := b.configure(ctx, dir, spec, env); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}

	if err := b.start(dir, args, env, spec, version); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	b.log.Info("runner process started", "runner", spec.Name, "pool", spec.PoolName, "dir", dir, "runner_version", version)
	return Handle(dir), nil
}

// configure runs config.sh for the registration-token path, which is how a
// non-ephemeral pool joins a runner to GitHub.
func (b *ProcessBackend) configure(ctx context.Context, dir string, spec Spec, env []string) error {
	script := filepath.Join(dir, configScript)
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("backend: %s is missing from the runner release in %s; delete %s and let Zoomies download it again", configScript, dir, dir)
	}

	args := []string{
		"--unattended", "--replace", "--disableupdate",
		"--url", spec.Credentials.URL,
		"--token", spec.Credentials.RegistrationToken,
		"--name", spec.Name,
		"--work", runnerWorkDir,
	}
	if labels := strings.Join(spec.Credentials.Labels, ","); labels != "" {
		args = append(args, "--labels", labels)
	}
	if spec.Credentials.RunnerGroup != "" {
		args = append(args, "--runnergroup", spec.Credentials.RunnerGroup)
	}
	if spec.Ephemeral {
		args = append(args, "--ephemeral")
	}

	ctx, cancel := context.WithTimeout(ctx, configureTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	// The output is appended to the runner's own log so that a failed
	// registration is visible in the same place as everything else.
	appendLog(filepath.Join(dir, runnerLogFile), out)
	if err != nil {
		return fmt.Errorf("backend: registering runner %s with GitHub failed: %w; the last output was: %s", spec.Name, err, lastLine(out))
	}
	return nil
}

// start launches the listener detached and records enough on disk for a future
// agent process to find it again.
func (b *ProcessBackend) start(dir string, args, env []string, spec Spec, version string) error {
	bin := filepath.Join(dir, listenerPath)
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("backend: the runner binary %s is missing: %w", bin, err)
	}

	logFile, err := os.OpenFile(filepath.Join(dir, runnerLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("backend: opening the runner log in %s: %w", dir, err)
	}

	// The command deliberately does not carry a context: the runner must outlive
	// the create call, and the agent's own shutdown must not kill a job.
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("backend: starting the runner in %s: %w", dir, err)
	}

	now := time.Now().UTC()
	meta := processMeta{
		Name:      spec.Name,
		RunnerID:  spec.RunnerID,
		PoolID:    spec.PoolID,
		PoolName:  spec.PoolName,
		Version:   version,
		Ephemeral: spec.Ephemeral,
		PID:       cmd.Process.Pid,
		CreatedAt: now,
		StartedAt: now,
	}
	if err := writeMeta(dir, meta); err != nil {
		abandon(cmd, logFile)
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, runnerPIDFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o640); err != nil {
		abandon(cmd, logFile)
		return fmt.Errorf("backend: writing the pid file in %s: %w", dir, err)
	}

	b.mu.Lock()
	b.running[dir] = cmd
	b.mu.Unlock()

	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		code := 0
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			code = -1
		}
		writeExit(dir, code)
		b.mu.Lock()
		delete(b.running, dir)
		b.mu.Unlock()
		b.log.Info("runner process exited", "runner", meta.Name, "dir", dir, "exit_code", code)
	}()
	return nil
}

// abandon kills a child we have decided not to keep and reaps it, so that a
// half-failed create leaves no zombie behind.
func abandon(cmd *exec.Cmd, log *os.File) {
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_ = log.Close()
}

// childEnv builds the runner's environment. It is assembled rather than
// inherited so that a job cannot read whatever happened to be in the agent's
// environment, which on a controller host includes its database path and its
// GitHub credentials.
func (b *ProcessBackend) childEnv(spec Spec, dir string) []string {
	env := []string{
		"HOME=" + dir,
		"PATH=" + firstNonEmpty(os.Getenv("PATH"), "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"),
		EnvRunnerName + "=" + spec.Name,
		EnvEphemeral + "=" + boolString(spec.Ephemeral),
		// The runner refuses to start as root unless told that is intended, and
		// an agent running as a system service often is root.
		"RUNNER_ALLOW_RUNASROOT=1",
	}
	for _, k := range []string{"LANG", "LC_ALL", "TZ", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}

	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+spec.Env[k])
	}
	return env
}

// Status reads the recorded pid and exit file.
func (b *ProcessBackend) Status(ctx context.Context, h Handle) (Status, error) {
	dir := string(h)
	meta, err := readMeta(dir)
	if err != nil {
		return Status{}, err
	}

	st := Status{Handle: h, StartedAt: meta.StartedAt}
	if ex, ok := readExit(dir); ok {
		st.ExitedAt = ex.ExitedAt
		st.ExitCode = ex.Code
		if ex.Code == 0 {
			st.Phase = PhaseExited
		} else {
			st.Phase = PhaseFailed
			st.Message = fmt.Sprintf("runner exited with code %d; see %s", ex.Code, filepath.Join(dir, runnerLogFile))
		}
		return st, nil
	}

	pid := readPID(dir)
	switch {
	case pid <= 0:
		st.Phase = PhaseStarting
	case processAlive(pid):
		st.Phase = PhaseRunning
	default:
		// No exit file and no process: the agent was restarted while the runner
		// died, so nobody recorded the code.
		st.Phase = PhaseFailed
		st.ExitCode = -1
		st.Message = fmt.Sprintf("runner process %d is gone and recorded no exit code; see %s", pid, filepath.Join(dir, runnerLogFile))
	}
	return st, nil
}

// Stats samples the runner's memory on Linux, where /proc makes it free. CPU is
// left at zero: sampling it properly means holding state between calls, and the
// host's own tooling already does that better.
func (b *ProcessBackend) Stats(ctx context.Context, h Handle) (Stats, error) {
	pid := readPID(string(h))
	if pid <= 0 || runtime.GOOS != "linux" {
		return Stats{}, nil
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "statm"))
	if err != nil {
		return Stats{}, nil
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return Stats{}, nil
	}
	rss, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return Stats{}, nil
	}
	return Stats{MemoryBytes: rss * int64(os.Getpagesize())}, nil
}

// Logs streams the runner's log file.
//
// Following is done by polling rather than with inotify because inotify is
// Linux-only and this backend's whole reason to exist is the hosts that are not
// Linux containers. A quarter-second poll on one small file is cheaper than a
// dependency that does not build on macOS.
//
// Since and Timestamps are ignored here: unlike a container's log stream, this
// is the runner's own file, and the runner already timestamps every line.
func (b *ProcessBackend) Logs(ctx context.Context, h Handle, opts LogOptions) (io.ReadCloser, error) {
	dir := string(h)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("%w: no runner directory at %s", ErrNotFound, dir)
	}
	path := filepath.Join(dir, runnerLogFile)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The runner has not written anything yet, which is not an error.
			if f, err = os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o640); err != nil {
				return nil, fmt.Errorf("backend: opening %s: %w", path, err)
			}
		} else {
			return nil, fmt.Errorf("backend: opening %s: %w", path, err)
		}
	}

	if opts.Tail > 0 {
		if err := seekToLastLines(f, opts.Tail); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if !opts.Follow {
		return f, nil
	}
	return &followReader{
		ctx:  ctx,
		f:    f,
		poll: tailPoll,
		// Following stops once the runner has exited and its log is drained;
		// otherwise a viewer on a finished runner would hang forever.
		finished: func() bool { _, done := readExit(dir); return done },
	}, nil
}

// Stop sends SIGINT, which the runner treats as "finish this job and exit",
// then escalates.
func (b *ProcessBackend) Stop(ctx context.Context, h Handle, timeout time.Duration) error {
	dir := string(h)
	pid := readPID(dir)
	if pid <= 0 || !processAlive(pid) {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := proc.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		b.log.Warn("could not interrupt the runner; killing it", "dir", dir, "pid", pid, "error", err)
		return b.kill(proc, dir)
	}
	if waitFor(ctx, func() bool { return !processAlive(pid) }, timeout, 200*time.Millisecond) {
		return nil
	}

	b.log.Warn("runner did not exit after SIGINT; killing it", "dir", dir, "pid", pid, "timeout", timeout)
	return b.kill(proc, dir)
}

func (b *ProcessBackend) kill(proc *os.Process, dir string) error {
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("backend: killing the runner process %d in %s: %w", proc.Pid, dir, err)
	}
	return nil
}

// Remove kills the runner and deletes its directory. Removing what is already
// gone is success.
func (b *ProcessBackend) Remove(ctx context.Context, h Handle) error {
	return b.wipe(ctx, string(h))
}

func (b *ProcessBackend) wipe(ctx context.Context, dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if pid := readPID(dir); pid > 0 && processAlive(pid) {
		if err := b.Stop(ctx, Handle(dir), 10*time.Second); err != nil {
			b.log.Warn("could not stop the runner before removing it", "dir", dir, "error", err)
		}
	}
	b.mu.Lock()
	delete(b.running, dir)
	b.mu.Unlock()

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("backend: removing the runner directory %s: %w", dir, err)
	}
	return nil
}

// List walks the runners directory, so an agent that restarted still finds the
// runners it started before.
func (b *ProcessBackend) List(ctx context.Context) ([]Workload, error) {
	root := filepath.Join(b.root, runnersDirName)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backend: listing runner directories in %s: %w", root, err)
	}

	var out []Workload
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		meta, err := readMeta(dir)
		if err != nil {
			continue
		}
		st, err := b.Status(ctx, Handle(dir))
		if err != nil {
			st = Status{Handle: Handle(dir), Phase: PhaseGone}
		}
		out = append(out, Workload{
			Handle:   Handle(dir),
			Name:     meta.Name,
			RunnerID: meta.RunnerID,
			PoolID:   meta.PoolID,
			Status:   st,
		})
	}
	return out, nil
}

func (b *ProcessBackend) runnerDir(name string) string {
	return filepath.Join(b.root, runnersDirName, containerName(name))
}

// ---------------------------------------------------------------------------
// Runner release management
// ---------------------------------------------------------------------------

// ensureRelease returns the directory holding an extracted runner release,
// downloading it once per version.
func (b *ProcessBackend) ensureRelease(ctx context.Context, version string) (string, error) {
	dir := filepath.Join(b.root, toolsDirName, version)
	if _, err := os.Stat(filepath.Join(dir, listenerPath)); err == nil {
		return dir, nil
	}

	b.installing.Lock()
	defer b.installing.Unlock()
	// Another create may have installed it while we waited for the lock.
	if _, err := os.Stat(filepath.Join(dir, listenerPath)); err == nil {
		return dir, nil
	}

	asset, err := runnerAsset(runtime.GOOS, runtime.GOARCH, version)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/v%s/%s", b.baseURL, version, asset)

	if err := os.MkdirAll(filepath.Join(b.root, toolsDirName), 0o750); err != nil {
		return "", fmt.Errorf("backend: creating the tools directory: %w", err)
	}
	archive, err := b.download(ctx, url, asset)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)

	if err := b.verify(archive, version, asset); err != nil {
		return "", err
	}

	tmp := dir + ".incoming"
	_ = os.RemoveAll(tmp)
	if err := extractTarGz(archive, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("backend: unpacking %s: %w", asset, err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("backend: installing the runner release into %s: %w", dir, err)
	}
	b.log.Info("installed actions/runner release", "version", version, "dir", dir)
	return dir, nil
}

func (b *ProcessBackend) download(ctx context.Context, url, asset string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("backend: building the download request for %s: %w", url, err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("backend: downloading %s: %w; this host needs outbound access to the runner release, or set agent.runner_download_url to an internal mirror", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("backend: downloading %s: %s; check that the pinned runner version exists", url, resp.Status)
	}

	f, err := os.CreateTemp(filepath.Join(b.root, toolsDirName), ".download-*-"+asset)
	if err != nil {
		return "", fmt.Errorf("backend: creating a temporary file for %s: %w", asset, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("backend: downloading %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("backend: writing %s: %w", asset, err)
	}
	return f.Name(), nil
}

// verify checks the archive against a digest we know, and refuses to install an
// unverified one unless the operator has said that is acceptable.
func (b *ProcessBackend) verify(path, version, asset string) error {
	want := b.sha256
	if want == "" {
		want = knownRunnerSHA256[version+"/"+asset]
	}
	if want == "" {
		if !b.allowUnverified {
			return fmt.Errorf("backend: no SHA-256 is known for %s, so Zoomies will not install it; publish the digest with agent.runner_sha256 (it is in the actions/runner release notes) or set agent.allow_unverified_runner_download to accept the risk", asset)
		}
		b.log.Warn("INSTALLING AN UNVERIFIED RUNNER RELEASE: no SHA-256 was available to check this download against, and it will be executed on this host",
			"asset", asset, "version", version)
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("backend: reading %s to verify it: %w", asset, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("backend: hashing %s: %w", asset, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("backend: %s does not match its expected SHA-256 (got %s, want %s); the download was corrupted or tampered with -- it has not been installed", asset, got, want)
	}
	return nil
}

// runnerAsset names the release archive for a host, or explains why there is
// not one.
func runnerAsset(goos, goarch, version string) (string, error) {
	var osPart string
	switch goos {
	case "linux":
		osPart = "linux"
	case "darwin":
		osPart = "osx"
	default:
		return "", fmt.Errorf("backend: the process backend does not support %s; actions/runner ships for Linux and macOS, so use the docker backend on this host", goos)
	}

	var archPart string
	switch goarch {
	case "amd64":
		archPart = "x64"
	case "arm64":
		archPart = "arm64"
	case "arm":
		archPart = "arm"
	default:
		return "", fmt.Errorf("backend: the process backend does not support %s/%s; actions/runner ships for x64, arm64 and arm", goos, goarch)
	}
	if osPart == "osx" && archPart == "arm" {
		return "", errors.New("backend: actions/runner does not ship a 32-bit macOS build")
	}
	return fmt.Sprintf("actions-runner-%s-%s-%s.tar.gz", osPart, archPart, version), nil
}

// extractTarGz unpacks an archive, refusing entries that would write outside
// the destination -- a tarball is remote input, however trusted its origin.
func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if _, err := safeJoin(dest, filepath.Join(filepath.Dir(hdr.Name), hdr.Linkname)); err != nil {
				return fmt.Errorf("archive entry %q links outside the archive", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// safeJoin resolves name under root, rejecting anything that escapes it. A
// tarball is remote input even when it comes from a trusted release, and an
// entry called ../../etc/cron.d/anything is the classic way that stops being
// true.
func safeJoin(root, name string) (string, error) {
	target := filepath.Join(root, name)
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q would write outside %s", name, root)
	}
	return target, nil
}

// cloneTree copies a directory tree, hard-linking regular files where the
// filesystem allows it. The runner never writes to its own program files, so
// sharing inodes between runners is safe and turns a 400MB copy per runner into
// a handful of directory entries.
func cloneTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		default:
			if err := os.Link(path, target); err == nil {
				return nil
			}
			return copyFile(path, target)
		}
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ---------------------------------------------------------------------------
// On-disk records
// ---------------------------------------------------------------------------

// processMeta is what a restarted agent needs to recognise a directory as one
// of its runners.
type processMeta struct {
	Name      string    `json:"name"`
	RunnerID  string    `json:"runner_id"`
	PoolID    string    `json:"pool_id"`
	PoolName  string    `json:"pool_name"`
	Version   string    `json:"runner_version"`
	Ephemeral bool      `json:"ephemeral"`
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at"`
}

func writeMeta(dir string, m processMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("backend: encoding runner metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, runnerMetaFile), b, 0o640); err != nil {
		return fmt.Errorf("backend: writing runner metadata in %s: %w", dir, err)
	}
	return nil
}

func readMeta(dir string) (processMeta, error) {
	var m processMeta
	b, err := os.ReadFile(filepath.Join(dir, runnerMetaFile))
	if errors.Is(err, fs.ErrNotExist) {
		return m, fmt.Errorf("%w: no runner in %s", ErrNotFound, dir)
	}
	if err != nil {
		return m, fmt.Errorf("backend: reading runner metadata in %s: %w", dir, err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("backend: runner metadata in %s is corrupt: %w", dir, err)
	}
	return m, nil
}

// exitRecord is written once the runner's process has been reaped.
type exitRecord struct {
	Code     int       `json:"exit_code"`
	ExitedAt time.Time `json:"exited_at"`
}

func writeExit(dir string, code int) {
	b, err := json.Marshal(exitRecord{Code: code, ExitedAt: time.Now().UTC()})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, runnerExitFile), b, 0o640)
}

func readExit(dir string) (exitRecord, bool) {
	var r exitRecord
	b, err := os.ReadFile(filepath.Join(dir, runnerExitFile))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}

func readPID(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, runnerPIDFile))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// waitFor polls until done returns true, the context ends or the deadline
// passes. It is how "stop, then kill" is implemented without a busy loop.
func waitFor(ctx context.Context, done func() bool, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return done()
		case <-time.After(interval):
		}
	}
}

// processAlive reports whether a pid is still running. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func appendLog(path string, data []byte) {
	if len(data) == 0 {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "(no output)"
}

// ---------------------------------------------------------------------------
// Log tailing
// ---------------------------------------------------------------------------

// followReader turns a log file into a stream that keeps producing as the
// runner writes.
type followReader struct {
	ctx      context.Context
	f        *os.File
	poll     time.Duration
	finished func() bool
	// drained records that the runner had already exited when we last hit the
	// end of the file, so one more read confirms there is nothing left.
	drained bool
}

func (t *followReader) Read(p []byte) (int, error) {
	for {
		n, err := t.f.Read(p)
		if n > 0 {
			t.drained = false
			return n, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if t.finished != nil && t.finished() {
			if t.drained {
				return 0, io.EOF
			}
			// One more pass, because the runner may have written its last lines
			// between our read and its exit.
			t.drained = true
			continue
		}
		select {
		case <-t.ctx.Done():
			return 0, io.EOF
		case <-time.After(t.poll):
		}
	}
}

// Close releases the file.
func (t *followReader) Close() error { return t.f.Close() }

// seekToLastLines positions f at the start of its last n lines, reading
// backwards in blocks so that a large log is not loaded to show its tail.
func seekToLastLines(f *os.File, n int) error {
	const block = 8 << 10
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("backend: reading the size of %s: %w", f.Name(), err)
	}

	size := info.Size()
	var (
		found int
		off   = size
		buf   = make([]byte, block)
	)
	for off > 0 {
		read := int64(block)
		if off < read {
			read = off
		}
		off -= read
		if _, err := f.ReadAt(buf[:read], off); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("backend: reading %s: %w", f.Name(), err)
		}
		chunk := buf[:read]
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] != '\n' {
				continue
			}
			// The final newline ends the last line rather than starting one.
			if off+int64(i) == size-1 {
				continue
			}
			found++
			if found >= n {
				_, err := f.Seek(off+int64(i)+1, io.SeekStart)
				return err
			}
		}
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}
