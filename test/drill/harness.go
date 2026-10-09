//go:build drill

package drill

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// fleet is one drill's world: a fake GitHub, a controller process, and a
// remote agent process, all real except GitHub.
//
// The two Zoomies processes are the built binary, started the way an operator
// starts them. That is the point of this tier: every other test in the
// repository exercises the code inside the test process, where a restart is a
// struct being rebuilt and an agent is a goroutine. Here a restart is a
// process dying, the agent is on the other end of a socket having joined with
// a token, and the backend puts something on the host.
type fleet struct {
	t   *testing.T
	gh  *github.FakeGitHub
	api *apiClient

	controller *process
	agent      *process
	// agentEnv is what the agent was started with, so a drill can kill it and
	// start it again as the same host: its credentials are in the work
	// directory, so a restart needs no second join token.
	agentEnv []string

	baseURL string
	// port and stateDir are kept so a drill can stop the controller and start
	// another on the same address -- against the same database, or against one
	// it has just restored.
	port     int
	stateDir string
	// agentWork is the directory inside the agent's state directory where the
	// backend lays out runners. The drill watches it to see a workload appear.
	agentWork string

	installationID string

	// controllerBin and agentBin are the binaries the two processes run, and
	// controllerEnv is added to the controller's environment on every start, so
	// a drill that restarts the controller gets the one it asked for.
	controllerBin string
	agentBin      string
	controllerEnv []string
	// agentUpdateDir is the agent's update folder, when the fleet was asked to
	// install the update helper beside it; empty otherwise.
	agentUpdateDir string
}

// fleetOptions is what a drill changes about the fleet newFleet stands up. The
// zero value is newFleet's: the binary make built, run as both processes.
type fleetOptions struct {
	controllerBin string
	agentBin      string
	controllerEnv []string
	// updateHelper lays out on the agent's host what installing the update helper
	// leaves there, marker included, so that the agent offers to update itself.
	updateHelper bool
}

// process is one child binary, with its output kept for the failure message.
type process struct {
	name string
	cmd  *exec.Cmd
	stop context.CancelFunc

	mu   sync.Mutex
	logs bytes.Buffer
	done chan struct{}
}

func (p *process) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logs.String()
}

// kill stops the process abruptly, which is what a drill wants when it is
// testing what a crash does. Waiting for it to exit is deliberate: a drill
// that carried on while the old process still held its port or its database
// lock would be testing the wrong thing.
func (p *process) kill() {
	p.stop()
	<-p.done
}

// newFleet stands up the whole thing and tears it down on cleanup.
func newFleet(t *testing.T) *fleet {
	t.Helper()
	return newFleetWith(t, fleetOptions{})
}

// newFleetWith is newFleet with the binaries, the controller's environment or
// the agent's update helper changed.
func newFleetWith(t *testing.T, opts fleetOptions) *fleet {
	t.Helper()
	requireBinary(t)

	gh := github.NewFake()
	t.Cleanup(gh.Close)
	gh.AddRepo("acme/api")

	f := &fleet{
		t: t, gh: gh,
		controllerBin: cmp.Or(opts.controllerBin, builtBinary()),
		agentBin:      cmp.Or(opts.agentBin, builtBinary()),
		controllerEnv: opts.controllerEnv,
	}
	f.startController()
	f.connectInstallation()
	f.startAgent(opts.updateHelper)
	return f
}

// startController runs the built binary as a controller pointed at the fake.
func (f *fleet) startController() {
	t := f.t
	t.Helper()
	f.port = freePort(t)
	f.stateDir = t.TempDir()
	f.startControllerOn(f.stateDir)
}

// startControllerOn runs the built binary as a controller against one state
// directory, on this fleet's address.
//
// The directory is a parameter because a restore drill's whole point is that
// the second controller comes up on a different one: the database it was given
// rather than the database it wrote.
func (f *fleet) startControllerOn(dir string) {
	t := f.t
	t.Helper()
	port := f.port
	f.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	f.controller = f.spawn(f.controllerBin, "controller", []string{"controller"}, append(append(baseEnv(dir),
		fmt.Sprintf("ZOOMIES_BIND=127.0.0.1:%d", port),
		"ZOOMIES_DISABLE_AUTH=true", // loopback only; the validator allows it there
		"ZOOMIES_DB_PATH="+filepath.Join(dir, "zoomies.db"),
		// The whole reason this tier can run without credentials.
		"ZOOMIES_GITHUB_API_BASE_URL="+f.gh.URL(),
		// A remote agent is the point; the embedded one would hide the join.
		"ZOOMIES_AGENT_EMBEDDED=false",
		// Webhooks cannot reach a test process, so the poller is what notices
		// a queued job. Short, because a drill's patience is its runtime.
		"ZOOMIES_POLL_FALLBACK=true",
		"ZOOMIES_POLL_INTERVAL=2s",
	), f.controllerEnv...))
	f.api = &apiClient{t: t, base: f.baseURL + "/api/v1"}
	waitFor(t, waitProcessUp, "the controller to become healthy", func() bool {
		resp, err := http.Get(f.baseURL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// run executes the built binary as a one-shot command against a state
// directory, the way an operator runs `zoomies backup` on the machine holding
// the data. It returns what the command printed.
func (f *fleet) run(dir string, args ...string) string {
	t := f.t
	t.Helper()
	cmd := exec.Command(builtBinary(), args...)
	cmd.Env = append(baseEnv(dir), "ZOOMIES_DB_PATH="+filepath.Join(dir, "zoomies.db"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("zoomies %s: %v\noutput: %q", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

// connectInstallation registers the fake's app with the controller.
func (f *fleet) connectInstallation() {
	t := f.t
	t.Helper()
	var inst struct {
		ID string `json:"id"`
	}
	f.api.post("/installations", map[string]any{
		"app_id":          f.gh.AppID(),
		"installation_id": f.gh.InstallationID(),
		"target":          "acme",
		"target_type":     "org",
		"private_key":     generateAppKey(t),
		"webhook_secret":  drillWebhookSecret,
		// Named on the installation rather than left to the global setting, so
		// the row itself says which GitHub this fleet is talking to.
		"api_base_url": f.gh.URL(),
	}, &inst)
	if inst.ID == "" {
		t.Fatal("the installation came back without an ID")
	}
	f.installationID = inst.ID
}

// startAgent joins a second binary as a remote agent, with a join token, and
// with the update helper installed beside it when updateHelper is set.
func (f *fleet) startAgent(updateHelper bool) {
	t := f.t
	t.Helper()
	var token struct {
		Token string `json:"token"`
	}
	f.api.post("/join-tokens", map[string]any{"capacity": 2}, &token)
	if token.Token == "" {
		t.Fatal("the join token came back empty")
	}

	dir := t.TempDir()
	f.agentWork = filepath.Join(dir, "work")
	if err := os.MkdirAll(f.agentWork, 0o750); err != nil {
		t.Fatalf("creating the agent work directory: %v", err)
	}
	// Before the agent starts: the backend looks for the runner tree the first
	// time it is asked to create anything.
	if err := stageStubRunner(f.agentWork); err != nil {
		t.Fatalf("staging the stub runner: %v", err)
	}
	if updateHelper {
		f.agentUpdateDir = installUpdateFolder(t, dir)
		writeHelperMarker(t, f.agentUpdateDir)
	}

	f.agentEnv = append(baseEnv(dir),
		"ZOOMIES_CONTROLLER_URL="+f.baseURL,
		"ZOOMIES_AGENT_BACKEND=process",
		"ZOOMIES_AGENT_CAPACITY=2",
		"ZOOMIES_WORK_DIR="+f.agentWork,
		// Plain HTTP to a loopback controller is what a drill has; the agent
		// refuses it otherwise, and rightly.
		"ZOOMIES_AGENT_ALLOW_INSECURE_HTTP=true",
		// Nothing should be downloaded: the stub tree is staged and the pool
		// pins its version. Saying so means a drill that somehow reaches for
		// the network fails here rather than hanging.
		"ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL=http://127.0.0.1:1/never",
	)
	// The join token is passed to the first start only. A restart that carried
	// one would prove nothing about a restart: an agent has to come back as the
	// host it already is, from the credentials it wrote.
	f.agent = f.spawn(f.agentBin, "agent", []string{"agent"},
		append(f.agentEnv, "ZOOMIES_JOIN_TOKEN="+token.Token))

	waitFor(t, waitProcessUp, "the agent to join and appear as a host", func() bool {
		var out struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		f.api.get("/hosts", &out)
		return len(out.Items) > 0
	})
}

// brokenDockerLabel is how a pool asks for the host whose Docker socket is
// missing, and it exists because the drill's own machine is in the fleet.
// ZOOMIES_AGENT_BACKEND names an agent's default backend rather than its only
// one -- every agent probes for all of them and reports what it finds -- so on
// a CI runner with Docker installed the drill's first agent offers docker too.
// A docker pool with no selector then places there quite happily, and a drill
// about a host that cannot run anything silently tests a host that can.
const brokenDockerLabel = "drill=docker-broken"

// startBrokenDockerAgent joins a second agent whose Docker socket is not there.
//
// It is a second machine rather than a reconfigured first one, because that is
// the shape of the fault in a real fleet: one host's daemon is down and the
// others are fine, and what matters is whether the fleet says which. The name
// is set explicitly because both agents are on this machine and would
// otherwise claim the same host row.
func (f *fleet) startBrokenDockerAgent(name string) *process {
	t := f.t
	t.Helper()
	var token struct {
		Token string `json:"token"`
	}
	f.api.post("/join-tokens", map[string]any{"capacity": 2}, &token)
	if token.Token == "" {
		t.Fatal("the join token came back empty")
	}
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatalf("creating the work directory: %v", err)
	}
	return f.spawn(builtBinary(), "docker-agent", []string{"agent"}, append(baseEnv(dir),
		"ZOOMIES_CONTROLLER_URL="+f.baseURL,
		"ZOOMIES_JOIN_TOKEN="+token.Token,
		"ZOOMIES_AGENT_NAME="+name,
		"ZOOMIES_AGENT_BACKEND=docker",
		// The label a pool selects it by, so the drill's own machine cannot
		// answer for it.
		"ZOOMIES_AGENT_LABELS="+brokenDockerLabel,
		// A socket that is not there is what a stopped daemon looks like from
		// the outside, and it is the one version of this fault a tier with no
		// daemon can produce honestly.
		"ZOOMIES_DOCKER_HOST=unix:///nonexistent/zoomies-drill-docker.sock",
		"ZOOMIES_AGENT_CAPACITY=2",
		"ZOOMIES_WORK_DIR="+work,
		"ZOOMIES_AGENT_ALLOW_INSECURE_HTTP=true",
		"ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL=http://127.0.0.1:1/never",
	))
}

// hostViews is the fleet's hosts as the API renders them, which is where a
// backend says whether it is usable and why not.
func (f *fleet) hostViews() []hostView {
	f.t.Helper()
	var out struct {
		Items []hostView `json:"items"`
	}
	f.api.get("/hosts", &out)
	return out.Items
}

type hostView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Labels      map[string]string `json:"labels"`
	Update      *hostUpdateView   `json:"update"`
	BackendInfo []struct {
		Kind      string `json:"kind"`
		Available bool   `json:"available"`
		Endpoint  string `json:"endpoint"`
		Detail    string `json:"detail"`
	} `json:"backend_info"`
}

// hostUpdateView is a host's part in updating, as its card shows it.
type hostUpdateView struct {
	State     string `json:"state"`
	Reason    string `json:"reason"`
	CanUpdate bool   `json:"can_update"`
	AttemptID string `json:"attempt_id"`
}

// problems is the problems drawer's own list, which is where an operator meets
// a pool that cannot place anything.
func (f *fleet) problems() []problemView {
	f.t.Helper()
	var out struct {
		Items []problemView `json:"items"`
	}
	f.api.get("/problems", &out)
	return out.Items
}

type problemView struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
}

// restartAgent starts the agent again as the host it already is, which is what
// an operator's `systemctl restart zoomies-agent` does.
func (f *fleet) restartAgent() {
	f.t.Helper()
	f.agent = f.spawn(f.agentBin, "agent", []string{"agent"}, f.agentEnv)
}

// spawn starts a Zoomies binary and keeps its output for the failure message.
// The binary is named because an update drill runs two releases side by side.
func (f *fleet) spawn(bin, name string, args, env []string) *process {
	t := f.t
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	p := &process{name: name, cmd: cmd, stop: cancel, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &syncWriter{p: p}, &syncWriter{p: p}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting the %s: %v", name, err)
	}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-p.done
		if t.Failed() {
			t.Logf("%s output:\n%s", name, p.output())
		}
	})
	return p
}

type syncWriter struct{ p *process }

func (w *syncWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.logs.Write(b)
}

// baseEnv is what both binaries need: a state and config directory of their
// own, and nothing inherited that could point them somewhere real.
func baseEnv(dir string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"ZOOMIES_STATE_DIR=" + dir,
		"ZOOMIES_CONFIG_DIR=" + dir,
		"ZOOMIES_LOG_FORMAT=text",
		"ZOOMIES_LOG_LEVEL=debug",
		// The fake GitHub a drill writes onto its installation is on loopback,
		// which the API refuses unless told this is a network the operator
		// owns -- as it is.
		"ZOOMIES_ALLOW_PRIVATE_EGRESS=true",
	}
}

// deliverJob posts a signed workflow_job webhook, the way GitHub drives a
// controller in production.
//
// The fallback poller only finds *queued* jobs -- it is the webhook's backstop,
// not a second channel -- so a drill that only added jobs to the fake would
// watch a runner sit idle until its timeout and never see the job it was made
// for. Delivering the webhook is what makes the drill exercise the path a real
// installation uses, and it is what puts the timing in the drill's hands: a
// fault drill needs to choose the moment a job starts.
func (f *fleet) deliverJob(action string, job github.QueuedJob, runnerName, conclusion string) {
	f.t.Helper()
	payload := map[string]any{
		"id":            job.ID,
		"run_id":        job.RunID,
		"name":          job.JobName,
		"workflow_name": job.WorkflowName,
		"labels":        job.Labels,
		"status":        action,
		"runner_name":   runnerName,
		"created_at":    job.QueuedAt.Format(time.RFC3339),
		"html_url":      job.HTMLURL,
	}
	if action == "in_progress" || action == "completed" {
		payload["started_at"] = job.QueuedAt.Add(time.Second).Format(time.RFC3339)
	}
	if action == "completed" {
		payload["completed_at"] = job.QueuedAt.Add(2 * time.Second).Format(time.RFC3339)
		payload["conclusion"] = conclusion
	}
	body, err := json.Marshal(map[string]any{
		"action":       action,
		"workflow_job": payload,
		"repository":   map[string]any{"full_name": job.Repo},
		"installation": map[string]any{"id": f.gh.InstallationID()},
	})
	if err != nil {
		f.t.Fatalf("encoding the webhook body: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, f.baseURL+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		f.t.Fatalf("building the webhook request: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(drillWebhookSecret))
	mac.Write(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(github.EventTypeHeader, "workflow_job")
	req.Header.Set(github.DeliveryIDHeader, fmt.Sprintf("drill-%d-%s", job.ID, action))
	req.Header.Set(github.SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("delivering the %s webhook: %v", action, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		f.t.Fatalf("delivering the %s webhook: %s\n%s", action, resp.Status, raw)
	}
}

// drillWebhookSecret signs the drill's deliveries. It is a fixed string
// because both ends of the signature are this test.
const drillWebhookSecret = "drill-webhook-secret"

// drillRunnerSize is what one drill runner is charged on the host it lands on.
//
// Every drill says it, rather than taking the fleet's default, because the
// default is two cores and four gigabytes -- the whole of the 2 vCPU, 4 GB
// machine CI runs these on. A host with no room for one runner of a pool
// places nothing, so an unsized drill pool fails every drill in the same
// unhelpful way: a timeout waiting for a runner that the fleet was right to
// refuse. These are the smallest figures a pool is allowed to ask for, which
// is the right size for a runner that sleeps: what the drills are about is the
// lifecycle, not the arithmetic, and both of the agent's slots have to fit on
// the smallest machine any of this runs on.
var drillRunnerSize = map[string]any{"cpus": 0.25, "memory_mb": 512}

// createPoolOn makes a pool on a named backend, kept to the hosts a selector
// picks out. The runner version is pinned to the staged stub either way: a pool
// that went looking for a download would hang rather than say what is wrong
// with the host.
//
// The selector is not optional decoration. A drill about one host's fault has
// to be able to say which host it means, or the fleet's other machine answers
// for it and the fault is never met.
func (f *fleet) createPoolOn(name, backend string, selector map[string]string, labels ...string) string {
	f.t.Helper()
	var pool struct {
		ID string `json:"id"`
	}
	f.api.post("/pools", map[string]any{
		"installation_id": f.installationID,
		"name":            name,
		"labels":          labels,
		"backend":         backend,
		"host_selector":   selector,
		"resources":       drillRunnerSize,
		"min_runners":     0,
		"max_runners":     1,
		"idle_timeout":    "1m",
		"ephemeral":       true,
		"runner_version":  stubVersion,
	}, &pool)
	if pool.ID == "" {
		f.t.Fatal("the pool came back without an ID")
	}
	return pool.ID
}

// createPool makes a pool on the process backend, pinned to the staged stub.
func (f *fleet) createPool(name string, labels ...string) string {
	f.t.Helper()
	var pool struct {
		ID string `json:"id"`
	}
	f.api.post("/pools", map[string]any{
		"installation_id": f.installationID,
		"name":            name,
		"labels":          labels,
		"backend":         "process",
		"resources":       drillRunnerSize,
		"min_runners":     0,
		"max_runners":     1,
		"idle_timeout":    "1m",
		"ephemeral":       true,
		"runner_version":  stubVersion,
	}, &pool)
	if pool.ID == "" {
		f.t.Fatal("the pool came back without an ID")
	}
	return pool.ID
}

// runners is the controller's view, including the ones it has finished with.
func (f *fleet) runners(poolID string) []runnerView {
	f.t.Helper()
	var out struct {
		Items []runnerView `json:"items"`
	}
	f.api.get("/runners?include_removed=true&pool_id="+poolID, &out)
	return out.Items
}

type runnerView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	JobsHandled int    `json:"jobs_handled"`
	ContainerID string `json:"container_id"`
}

// registeredWithGitHub reports whether the fake holds a registration under one
// name. Naming the runner matters: a drill that counted registrations would be
// racing whatever replacement the scheduler is entitled to create.
func (f *fleet) registeredWithGitHub(name string) bool {
	for _, r := range f.gh.Runners() {
		if r.Name == name {
			return true
		}
	}
	return false
}

// runnerDir is where the process backend laid a runner out. The backend's
// handle is the directory itself, so this is the same path it reports.
func (f *fleet) runnerDir(name string) string {
	return filepath.Join(f.agentWork, "runners", name)
}

// liveWorkloads is what is actually on the host: the process backend's handle
// is the runner's directory, so a directory with the stub's marker in it is a
// workload that really started.
func (f *fleet) liveWorkloads() []string {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.agentWork, "runners"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatalf("reading the agent's runner directory: %v", err)
	}
	var live []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(f.agentWork, "runners", e.Name())
		if _, err := os.Stat(startedMarker(dir)); err == nil {
			live = append(live, e.Name())
		}
	}
	return live
}

// workloadRunning reports whether the runner's process is actually alive,
// which is a different question from whether its directory is still there.
//
// A fault drill has to ask the sharper one: the marker file the stub writes
// outlives the process that wrote it, so a runner killed along with its agent
// leaves a directory that looks exactly like a running one.
func (f *fleet) workloadRunning(name string) bool {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.runnerDir(name), "runner.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 asks the kernel whether the process is there without touching
	// it, which is the only way to tell a live runner from a dead one's
	// leftovers.
	return proc.Signal(syscall.Signal(0)) == nil
}

// requireStillRunning fails unless the runner's process is alive now and stays
// alive for a moment.
//
// The moment is the point. A death that follows the agent's own -- a parent
// death signal, a killed process group -- arrives a beat later, so an
// assertion made the instant after the fault would be satisfied by a process
// that is already doomed.
func (f *fleet) requireStillRunning(name, why string) {
	f.t.Helper()
	deadline := time.Now().Add(waitStaysUp)
	for time.Now().Before(deadline) {
		if !f.workloadRunning(name) {
			f.t.Fatalf("the runner process is gone: %s", why)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

type apiClient struct {
	t    *testing.T
	base string
	// token is sent as a bearer credential when set. Most drills run with
	// authentication off; the bootstrap drill is the one whose point is that
	// it is on and the provisioner still gets in.
	token string
}

func (c *apiClient) do(method, path string, body, out any) {
	c.t.Helper()
	status, raw := c.try(method, path, body)
	if status >= 300 {
		c.t.Fatalf("%s %s: %d %s\n%s", method, path, status, http.StatusText(status), raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decoding the response: %v\n%s", method, path, err, raw)
		}
	}
}

// try sends one request and returns the status and the body, for a drill whose
// point is that the request is refused.
func (c *apiClient) try(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshalling the request body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		c.t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (c *apiClient) get(path string, out any)        { c.do(http.MethodGet, path, nil, out) }
func (c *apiClient) post(path string, body, out any) { c.do(http.MethodPost, path, body, out) }

func builtBinary() string {
	dir, err := os.Getwd()
	if err != nil {
		return "zoomies"
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "zoomies")
		}
		dir = filepath.Dir(dir)
	}
	return "zoomies"
}

// releaseBinary builds Zoomies again, stamped with a release's version, for a
// drill about what one release does to another. The binary make built reports
// something like 1.3.5-24-gc1890b3d, which is not a release, and a controller
// running one has no release to take its hosts to.
//
// It is built per drill rather than shared: only the link is new, because every
// package was compiled with the same flags for the binary make built and comes
// from the build cache, so a build takes seconds.
func releaseBinary(t *testing.T, ver string) string {
	t.Helper()
	requireBinary(t)
	out := filepath.Join(t.TempDir(), "zoomies-"+ver)
	ctx, cancel := context.WithTimeout(context.Background(), waitBuild)
	defer cancel()
	// go test puts its own toolchain first on PATH, so this is the go that is
	// running the drill.
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath",
		"-ldflags", "-s -w -X github.com/eyupio/zoomies/internal/version.Version="+ver,
		"-o", out, "./cmd/zoomies")
	cmd.Dir = filepath.Dir(builtBinary())
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building zoomies %s for the drill: %v\n%s", ver, err, b)
	}
	return out
}

// installUpdateFolder lays out in an agent's state directory what installing
// the update helper leaves on a native install, short of the marker: a
// configuration file, the pointer beside it naming the update folder, and the
// folder. An agent finds the folder only through that pointer, so this is the
// whole of what makes a host one the update helper could serve.
//
// The marker is left to writeHelperMarker. A folder without one is a helper
// whose install never finished, which is a host of its own to drill.
func installUpdateFolder(t *testing.T, stateDir string) string {
	t.Helper()
	dir := filepath.Join(stateDir, "update")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the update folder: %v", err)
	}
	// Empty, and there only so that the agent has a configuration file to look
	// beside: baseEnv points ZOOMIES_CONFIG_DIR here.
	if err := os.WriteFile(filepath.Join(stateDir, "zoomies.yaml"), nil, 0o640); err != nil {
		t.Fatalf("writing the agent's configuration file: %v", err)
	}
	writeJSONFile(t, filepath.Join(stateDir, channel.PointerFile), channel.Pointer{
		V: updates.WireVersion, Dir: dir, ConfigDir: stateDir,
	})
	return dir
}

// writeHelperMarker is the update helper's install finishing: the marker is its
// last step, and an agent offers to update itself only once it is there.
func writeHelperMarker(t *testing.T, updateDir string) {
	t.Helper()
	writeJSONFile(t, filepath.Join(updateDir, channel.MarkerFile), channel.Marker{
		V: updates.WireVersion, InstalledAt: time.Now().UTC(),
	})
}

// writeJSONFile writes a document under a temporary name and renames it into
// place, as the helper does, so that an agent reading on its own schedule never
// sees half of one.
func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding %s: %v", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o640); err != nil {
		t.Fatalf("writing %s: %v", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("putting %s in place: %v", filepath.Base(path), err)
	}
}

// requireBinary refuses to run rather than skipping: this tier exists to be
// the thing that cannot quietly not happen.
func requireBinary(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(builtBinary()); err != nil {
		t.Fatalf("the zoomies binary is not at %s; run `make build-nogui` first", builtBinary())
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, what)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// generateAppKey mints an RSA key for the fake's App.
//
// It is generated rather than checked in: the API parses the key to mint a JWT
// with it, so a placeholder string would be refused at the door, and a real
// key in the repository is a key somebody has to explain.
func generateAppKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating an app key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

var _ = store.RunnerIdle // the state names the drills assert on

// runnerSessions counts the usage ledger's sessions for one runner, read
// straight from the controller's database. There is no route for the
// sessions, and a drill asserting on the ledger should not need one: opening
// read-only neither migrates nor writes, so it cannot disturb the controller
// that owns the file.
func (f *fleet) runnerSessions(id string) int {
	f.t.Helper()
	s, err := store.Open(context.Background(), store.Options{Path: filepath.Join(f.stateDir, "zoomies.db"), ReadOnly: true})
	if err != nil {
		f.t.Fatalf("opening the controller's database read-only: %v", err)
	}
	defer s.Close()
	all, err := s.RunnerSessions(context.Background(), time.Unix(0, 0), time.Now().Add(time.Hour))
	if err != nil {
		f.t.Fatalf("reading runner sessions: %v", err)
	}
	n := 0
	for _, x := range all {
		if x.RunnerID == id {
			n++
		}
	}
	return n
}
