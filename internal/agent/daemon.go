package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// Both transports satisfy the same interface, which is what keeps the embedded
// and standalone agents from drifting apart.
var _ Transport = (*HTTPTransport)(nil)

// Tunables for the run loop. They are constants rather than options because an
// operator has no way to choose better values than these, and every one of them
// is a trade-off the code documents rather than exports.
const (
	defaultHeartbeatInterval = 30 * time.Second
	minHeartbeatInterval     = time.Second
	// defaultReconcileInterval is how often the host is compared against what
	// the agent believes. It is deliberately slower than the heartbeat: it
	// costs a listing per backend.
	defaultReconcileInterval = 30 * time.Second
	// reportTimeout bounds a result, report or heartbeat POST.
	reportTimeout = 30 * time.Second
	// createTimeout has to cover a cold image pull on a slow link, which is
	// minutes, not seconds.
	createTimeout = 15 * time.Minute
	// stopMargin is added to a task's stop timeout so the backend gets to run
	// its own kill path before the context expires.
	stopMargin    = 30 * time.Second
	removeTimeout = 2 * time.Minute
	// resolveTimeout bounds the backend listings used to find a runner the
	// agent has no record of.
	resolveTimeout = 30 * time.Second
	// minPollInterval keeps a controller that answers polls instantly from
	// turning the task loop into a spin.
	minPollInterval = 200 * time.Millisecond
	minPollBackoff  = time.Second
	maxPollBackoff  = 30 * time.Second
	// shutdownGrace bounds how long shutdown waits for in-flight tasks. A
	// create stuck on an image pull must not stop systemd from restarting the
	// unit.
	shutdownGrace = 30 * time.Second
	// orphanGrace is how long a workload nothing claims must stay unclaimed
	// before the agent reaps it.
	orphanGrace = 2 * time.Minute
	// missingGrace stops a runner created moments ago from being declared gone
	// because the backend has not listed it yet.
	missingGrace = time.Minute
)

// Options configures an Agent.
type Options struct {
	// Name is how this host appears in the UI and in `zoomies hosts`.
	Name string
	// WorkDir holds the agent's credentials and the runners' scratch space.
	WorkDir string
	// Capacity is the most runners this host will hold, and doubles as the
	// bound on how many lifecycle tasks may execute at once.
	Capacity int
	// Labels are matched against a pool's host selector.
	Labels   map[string]string
	Backends *backend.Registry
	// DefaultBackend handles tasks that do not name one.
	DefaultBackend store.BackendKind
	Transport      Transport
	// HeartbeatInterval defaults to 30s. The controller marks a host offline
	// after several missed beats, so shortening it makes failure detection
	// faster at the cost of more requests.
	HeartbeatInterval time.Duration
	Logger            *slog.Logger
	// Clock is injectable so tests do not have to sleep.
	Clock func() time.Time
}

// Agent is the half of Zoomies that runs on a host with a container runtime. It
// long-polls the controller for tasks, materialises runners through a backend,
// and reports what it sees back.
//
// Everything it does is outbound. Nothing dials an agent.
type Agent struct {
	opts     Options
	log      *slog.Logger
	tr       Transport
	clock    func() time.Time
	heartbtI time.Duration
	logs     *logRelay
	notify   *notifier

	// sem bounds concurrent lifecycle tasks at Capacity, so a burst of creates
	// from a busy morning cannot fork-bomb the host.
	sem   chan struct{}
	tasks sync.WaitGroup

	mu sync.Mutex
	// hostID is empty until Join or a restored state file provides one.
	hostID string
	// runners is what the agent believes about the workloads it started. The
	// host is the real truth; reconcile.go corrects this map from it.
	runners map[string]*tracked
	// inflight maps a runner ID to the ID of the task currently executing on
	// it, so two tasks for one runner never run at once. The task ID is held
	// rather than a bare flag because the claim is dropped in two places -- as
	// the result is reported, and again when the task's goroutine exits -- and
	// by then the next task for that runner may legitimately hold it.
	inflight map[string]string
	// orphans records when an unclaimed workload was first seen, which is how
	// "the controller has not mentioned it in a while" is measured.
	orphans map[backend.Handle]time.Time
	// backendInfo is the last probe, sent with heartbeats.
	backendInfo []backend.Info
	// cordoned mirrors the controller's flag, logged when it changes.
	cordoned bool
	// warnedSkew keeps a version-skew warning to one line per run.
	warnedSkew bool

	// polled records that at least one task poll has completed since start.
	// The reconciler will not delete anything until it has, so a controller
	// outage cannot be mistaken for "nobody owns these runners".
	polled atomic.Bool
	// ready records that the first heartbeat has been acknowledged.
	ready atomic.Bool
}

// tracked is the agent's belief about one runner it started.
type tracked struct {
	runnerID  string
	name      string
	kind      store.BackendKind
	handle    backend.Handle
	ephemeral bool
	createdAt time.Time
	// stopping records that the agent asked this workload to exit, so a
	// non-zero exit code afterwards is Zoomies' own doing rather than a job
	// failure.
	stopping bool
	// terminal records that the end of this runner's life has been reported
	// once already; reporting it every reconcile would be noise the controller
	// has to reject.
	terminal bool

	state      store.RunnerState
	phase      backend.Phase
	stats      backend.Stats
	exitCode   int
	message    string
	observedAt time.Time
}

func (t *tracked) report() RunnerReport {
	return RunnerReport{
		RunnerID:   t.runnerID,
		State:      t.state,
		Handle:     t.handle,
		Phase:      t.phase,
		ExitCode:   t.exitCode,
		Message:    t.message,
		Stats:      t.stats,
		ObservedAt: t.observedAt,
	}
}

// New validates the options and builds an agent that has not yet joined.
func New(opts Options) (*Agent, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("agent: no host name; set agent.name in zoomies.yaml or pass --name, since it is how this host appears in the UI")
	}
	if strings.TrimSpace(opts.WorkDir) == "" {
		return nil, errors.New("agent: no work directory; set agent.work_dir, for example /var/lib/zoomies/work, where the agent keeps its credentials and runner scratch space")
	}
	if opts.Capacity < 1 {
		return nil, fmt.Errorf("agent: capacity %d would let this host run nothing; set agent.capacity to at least 1", opts.Capacity)
	}
	if opts.Backends == nil || len(opts.Backends.Kinds()) == 0 {
		return nil, errors.New("agent: no backends registered; the agent cannot start runners without Docker, Podman or the process backend -- install one and set agent.backend")
	}
	if opts.Transport == nil {
		return nil, errors.New("agent: no transport; build one with NewHTTPTransport for a standalone agent, or pass the controller's in-process transport for an embedded one")
	}

	kinds := opts.Backends.Kinds()
	slices.Sort(kinds)
	if opts.DefaultBackend == "" {
		if len(kinds) != 1 {
			return nil, fmt.Errorf("agent: no default backend and %d are registered (%s); set agent.backend to the one this host should use", len(kinds), kindList(kinds))
		}
		opts.DefaultBackend = kinds[0]
	}
	if !opts.DefaultBackend.Valid() {
		return nil, fmt.Errorf("agent: %q is not a backend kind; set agent.backend to docker, podman or process", opts.DefaultBackend)
	}
	if _, err := opts.Backends.Get(opts.DefaultBackend); err != nil {
		return nil, fmt.Errorf("agent: default backend %q is not registered on this host (registered: %s); set agent.backend to one of those: %w", opts.DefaultBackend, kindList(kinds), err)
	}

	interval := opts.HeartbeatInterval
	if interval == 0 {
		interval = defaultHeartbeatInterval
	}
	if interval < minHeartbeatInterval {
		return nil, fmt.Errorf("agent: heartbeat interval %s is too short to be useful; set agent.heartbeat_interval to at least %s", interval, minHeartbeatInterval)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "agent", "host_name", opts.Name)

	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}

	a := &Agent{
		opts:     opts,
		log:      log,
		tr:       opts.Transport,
		clock:    clock,
		heartbtI: interval,
		sem:      make(chan struct{}, opts.Capacity),
		runners:  make(map[string]*tracked),
		inflight: make(map[string]string),
		orphans:  make(map[backend.Handle]time.Time),
	}
	a.logs = newLogRelay(opts.Transport, log)
	return a, nil
}

func kindList(kinds []store.BackendKind) string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

func (a *Agent) now() time.Time { return a.clock() }

// HostID returns the identity the controller gave this host, or "" before it
// has joined.
func (a *Agent) HostID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hostID
}

// Runners returns the agent's current observation of every runner it tracks,
// which is what the heartbeat carries and what the controller merges.
func (a *Agent) Runners() []RunnerReport {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]RunnerReport, 0, len(a.runners))
	for _, r := range a.runners {
		out = append(out, r.report())
	}
	slices.SortFunc(out, func(x, y RunnerReport) int { return strings.Compare(x.RunnerID, y.RunnerID) })
	return out
}

// Join enrols this host with a controller, redeeming a short-lived join token
// for the long-lived agent token that every later call carries.
func (a *Agent) Join(ctx context.Context, joinToken string) error {
	if strings.TrimSpace(joinToken) == "" {
		return errors.New("agent: no join token; mint one in the UI under Hosts, or with `zoomies hosts token`, and pass it as --token")
	}

	// Probe first so the controller learns what this host can actually do
	// before it is offered any work.
	infos := a.opts.Backends.Probe(ctx)
	a.mu.Lock()
	a.backendInfo = infos
	a.mu.Unlock()

	req := JoinRequest{
		ProtocolVersion: ProtocolVersion,
		JoinToken:       joinToken,
		Name:            a.opts.Name,
		Capacity:        a.opts.Capacity,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Version:         version.Version,
		Labels:          a.opts.Labels,
		Backends:        infos,
	}
	resp, err := a.tr.Join(ctx, req)
	if err != nil {
		return fmt.Errorf("agent: joining %s: %w", a.tr.Describe(), err)
	}

	creds := Credentials{HostID: resp.HostID, AgentToken: resp.AgentToken, Controller: a.tr.Describe()}
	path := StatePath(a.opts.WorkDir)
	if err := Save(path, creds); err != nil {
		// The token is shown exactly once, so a save failure has to be loud:
		// the host has joined but cannot prove it after a restart.
		return fmt.Errorf("agent: joined %s as host %s but could not persist the agent token to %s; the host will have to join again after a restart: %w", a.tr.Describe(), resp.HostID, path, err)
	}
	a.setCredentials(creds)

	if d, err := time.ParseDuration(resp.HeartbeatInterval); err == nil && d >= minHeartbeatInterval {
		a.heartbtI = d
	}
	a.warnSkew(resp.ControllerVersion)

	var available []string
	for _, i := range infos {
		if i.Available {
			available = append(available, string(i.Kind))
		}
	}
	a.log.Info("joined controller",
		"controller", a.tr.Describe(),
		"host_id", resp.HostID,
		"capacity", a.opts.Capacity,
		"backends", strings.Join(available, ","),
		"state_file", path)
	return nil
}

func (a *Agent) setCredentials(c Credentials) {
	a.mu.Lock()
	a.hostID = c.HostID
	a.mu.Unlock()
	a.tr.SetCredentials(c.HostID, c.AgentToken)
}

// ensureCredentials restores the identity a previous Join persisted, so that a
// restart does not need a new join token.
func (a *Agent) ensureCredentials() error {
	if a.HostID() != "" {
		return nil
	}
	creds, err := Load(StatePath(a.opts.WorkDir))
	if err != nil {
		return err
	}
	a.setCredentials(creds)
	return nil
}

// Run drives the agent until ctx is cancelled or the controller says something
// only an operator can fix.
//
// Cancelling ctx is a graceful shutdown: no new tasks are started, in-flight
// ones are finished, a last runner report is flushed, and the runners
// themselves are left alone. Restarting an agent must never kill a job.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureCredentials(); err != nil {
		return err
	}

	a.notify = newNotifier(a.log)
	defer a.notify.close()

	kinds := a.opts.Backends.Kinds()
	slices.Sort(kinds)
	a.log.Info("agent starting",
		"host_id", a.HostID(),
		"controller", a.tr.Describe(),
		"capacity", a.opts.Capacity,
		"backends", kindList(kinds),
		"default_backend", a.opts.DefaultBackend,
		"heartbeat", a.heartbtI,
		"version", version.Short())

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A fatal error from any loop stops the others: there is no useful work to
	// do once the controller has disowned this host.
	fatal := make(chan error, 3)
	var loops sync.WaitGroup
	run := func(name string, fn func(context.Context) error) {
		loops.Add(1)
		go func() {
			defer loops.Done()
			if err := fn(loopCtx); err != nil {
				a.log.Error("agent loop stopped", "loop", name, "error", err)
				fatal <- err
				cancel()
			}
		}()
	}
	run("heartbeat", a.heartbeatLoop)
	run("tasks", a.taskLoop)
	run("reconcile", a.reconcileLoop)
	run("watchdog", a.watchdogLoop)

	var err error
	select {
	case <-ctx.Done():
	case err = <-fatal:
		cancel()
	}
	loops.Wait()
	a.shutdown(ctx)
	return err
}

// shutdown finishes what is in flight and leaves the host's runners running.
func (a *Agent) shutdown(ctx context.Context) {
	a.notify.send("STOPPING=1")
	a.log.Info("agent shutting down; runners on this host are left running")

	if !waitFor(&a.tasks, shutdownGrace) {
		a.log.Warn("shutting down with tasks still running; their results will be reported if they finish", "grace", shutdownGrace)
	}
	a.logs.stopAll()

	// The controller cannot see this host again until it restarts, so the last
	// thing the agent does is tell it what the runners looked like.
	reports := a.Runners()
	if len(reports) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
	defer cancel()
	if err := a.tr.ReportRunners(rctx, reports); err != nil {
		a.log.Warn("could not flush a final runner report", "runners", len(reports), "error", err)
	}
}

// heartbeatLoop keeps the host marked online and carries the agent's view of
// its runners.
func (a *Agent) heartbeatLoop(ctx context.Context) error {
	ticker := time.NewTicker(a.heartbtI)
	defer ticker.Stop()
	for {
		if err := a.heartbeat(ctx); err != nil {
			switch {
			case ctx.Err() != nil:
				return nil
			case errors.Is(err, ErrHostGone):
				return fmt.Errorf("agent: the controller no longer has a record of host %s, so this agent can do nothing until it is re-joined: run `zoomies agent join %s --token <join-token>` with a token minted in the UI under Hosts: %w", a.HostID(), a.tr.Describe(), err)
			case errors.Is(err, ErrUnauthorized):
				return fmt.Errorf("agent: the controller rejected this agent's token, so it has been revoked or the host was recreated: re-join with `zoomies agent join %s --token <join-token>`: %w", a.tr.Describe(), err)
			default:
				a.log.Warn("heartbeat failed; retrying on the next interval", "error", err, "interval", a.heartbtI)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (a *Agent) heartbeat(ctx context.Context) error {
	hctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()

	a.mu.Lock()
	infos := a.backendInfo
	a.mu.Unlock()

	runners := a.Runners()
	resp, err := a.tr.Heartbeat(hctx, HeartbeatRequest{
		ProtocolVersion: ProtocolVersion,
		Capacity:        a.opts.Capacity,
		Version:         version.Version,
		Backends:        infos,
		Runners:         runners,
	})
	if err != nil {
		return err
	}

	if !a.ready.Swap(true) {
		// systemd holds dependent units until this arrives, so it is sent only
		// once the controller has actually answered: "started" and "working"
		// are different claims.
		a.notify.send("READY=1")
		a.log.Info("controller acknowledged this host", "host_id", a.HostID())
	}
	a.notify.send(fmt.Sprintf("STATUS=%d runner(s), capacity %d, controller %s", len(runners), a.opts.Capacity, a.tr.Describe()))

	if resp == nil {
		return nil
	}
	a.warnSkew(resp.ControllerVersion)

	a.mu.Lock()
	changed := a.cordoned != resp.Cordoned
	a.cordoned = resp.Cordoned
	a.mu.Unlock()
	if changed {
		if resp.Cordoned {
			a.log.Info("host cordoned; the controller will stop scheduling new runners here")
		} else {
			a.log.Info("host uncordoned; the controller may schedule runners here again")
		}
	}

	if resp.ResyncRequested {
		// The controller restarted and lost its cache, so re-probe rather than
		// send it a stale capability list.
		a.mu.Lock()
		a.backendInfo = a.opts.Backends.Probe(hctx)
		a.mu.Unlock()
		if err := a.tr.ReportRunners(hctx, runners); err != nil {
			a.log.Warn("resync report failed", "error", err)
		}
	}
	return nil
}

func (a *Agent) warnSkew(controllerVersion string) {
	if controllerVersion == "" || controllerVersion == version.Version {
		return
	}
	a.mu.Lock()
	first := !a.warnedSkew
	a.warnedSkew = true
	a.mu.Unlock()
	if first {
		a.log.Warn("controller and agent versions differ; upgrade both to the same release before reporting a bug",
			"controller_version", controllerVersion, "agent_version", version.Version)
	}
}

// taskLoop long-polls for work and dispatches it.
func (a *Agent) taskLoop(ctx context.Context) error {
	backoff := minPollBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		started := a.now()
		batch, err := a.tr.PollTasks(ctx, DefaultPollWait)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ErrUnauthorized) {
				return fmt.Errorf("agent: the controller rejected this agent's token while polling for tasks: re-join with `zoomies agent join %s --token <join-token>`: %w", a.tr.Describe(), err)
			}
			a.log.Warn("task poll failed; backing off", "error", err, "retry_in", backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, maxPollBackoff)
			continue
		}
		backoff = minPollBackoff
		// A completed poll is the proof that the controller is reachable, and
		// the reconciler removes nothing until it has seen one.
		a.polled.Store(true)

		for _, task := range batch.Tasks {
			a.dispatch(ctx, task)
		}

		wait := batch.Backoff
		if wait <= 0 && len(batch.Tasks) == 0 {
			// Guard against a controller that answers polls instantly: without
			// this the loop would spin at whatever rate it can dial.
			if elapsed := a.now().Sub(started); elapsed < minPollInterval {
				wait = minPollInterval - elapsed
			}
		}
		if wait > 0 && !sleepCtx(ctx, wait) {
			return nil
		}
	}
}

// dispatch validates a task and starts it, or reports why it cannot run.
// Silence is the one outcome the controller cannot act on, so every task ends
// in a result -- including the ones that were malformed.
func (a *Agent) dispatch(ctx context.Context, task Task) {
	if err := validateTask(task); err != nil {
		a.log.Warn("rejecting task", "task", task.ID, "kind", task.Kind, "error", err)
		a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: false, Error: err.Error(), CompletedAt: a.now()})
		return
	}

	switch task.Kind {
	case TaskStreamLogs, TaskCancelLogs:
		// Log relays are not lifecycle work and must not hold a capacity slot:
		// one operator watching a log would otherwise stop this host creating
		// runners for as long as the browser tab is open.
		a.tasks.Add(1)
		go func() {
			defer a.tasks.Done()
			a.runLogTask(ctx, task)
		}()
		return
	}

	if !a.claim(task.RunnerID, task.ID) {
		// The controller redelivers any task it has not seen a result for, so
		// a duplicate arriving while the first is still running is expected.
		// Skipping rather than queueing is what stops two creates for one
		// runner from racing each other on the host.
		a.log.Debug("skipping task; another task for this runner is already in flight",
			"task", task.ID, "kind", task.Kind, "runner", task.RunnerID)
		return
	}

	a.tasks.Add(1)
	go func() {
		defer a.tasks.Done()
		defer a.release(task.RunnerID, task.ID)
		select {
		case a.sem <- struct{}{}:
		case <-ctx.Done():
			a.report(ctx, TaskResult{
				TaskID:      task.ID,
				RunnerID:    task.RunnerID,
				OK:          false,
				Error:       "agent shut down before this task started; it is safe to redeliver",
				CompletedAt: a.now(),
			})
			return
		}
		defer func() { <-a.sem }()
		a.runTask(ctx, task)
	}()
}

func validateTask(task Task) error {
	if task.ID == "" {
		return errors.New("task has no ID, so its result cannot be matched to it; the controller must set one")
	}
	switch task.Kind {
	case TaskCreateRunner:
		if task.RunnerID == "" {
			return errors.New("create_runner task has no runner ID")
		}
		if task.Spec == nil {
			return fmt.Errorf("create_runner task for runner %s has no spec, so there is nothing to create", task.RunnerID)
		}
		if err := task.Spec.Validate(); err != nil {
			return fmt.Errorf("create_runner task for runner %s has an unusable spec: %w", task.RunnerID, err)
		}
	case TaskStopRunner, TaskRemoveRunner:
		if task.RunnerID == "" {
			return fmt.Errorf("%s task has no runner ID", task.Kind)
		}
	case TaskStreamLogs:
		if task.StreamID == "" {
			return errors.New("stream_logs task has no stream ID, so there is nowhere to send the output")
		}
		if task.RunnerID == "" {
			return errors.New("stream_logs task has no runner ID")
		}
	case TaskCancelLogs:
		if task.StreamID == "" {
			return errors.New("cancel_logs task has no stream ID")
		}
	default:
		return fmt.Errorf("unknown task kind %q; this agent speaks protocol version %d, so upgrade it to match the controller", task.Kind, ProtocolVersion)
	}
	return nil
}

func (a *Agent) runTask(ctx context.Context, task Task) {
	switch task.Kind {
	case TaskCreateRunner:
		a.handleCreate(ctx, task)
	case TaskStopRunner:
		a.handleStop(ctx, task)
	case TaskRemoveRunner:
		a.handleRemove(ctx, task)
	}
}

func (a *Agent) handleCreate(ctx context.Context, task Task) {
	kind := task.Backend
	if kind == "" {
		kind = a.opts.DefaultBackend
	}
	b, err := a.opts.Backends.Get(kind)
	if err != nil {
		a.reportFailure(ctx, task, fmt.Sprintf("this host has no %s backend (registered: %s); point the pool at a backend this host runs, or set agent.backend: %v", kind, kindList(a.opts.Backends.Kinds()), err))
		return
	}

	spec := *task.Spec
	if spec.RunnerID == "" {
		spec.RunnerID = task.RunnerID
	}
	if spec.WorkDir == "" {
		spec.WorkDir = a.opts.WorkDir
	}

	// Tasks are given a context that shutdown does not cancel: a create that is
	// half done is worse than one that finishes and is reported.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	defer cancel()

	start := a.now()
	handle, err := b.Create(cctx, spec)
	if err != nil {
		a.log.Error("creating runner failed", "runner", task.RunnerID, "name", spec.Name, "backend", kind, "error", err)
		a.reportFailure(ctx, task, fmt.Sprintf("the %s backend could not create runner %s: %v", kind, spec.Name, err))
		return
	}

	now := a.now()
	a.mu.Lock()
	a.runners[task.RunnerID] = &tracked{
		runnerID:   task.RunnerID,
		name:       spec.Name,
		kind:       kind,
		handle:     handle,
		ephemeral:  spec.Ephemeral,
		createdAt:  now,
		state:      store.RunnerRegistering,
		phase:      backend.PhaseStarting,
		observedAt: now,
	}
	delete(a.orphans, handle)
	a.mu.Unlock()

	a.log.Info("runner created", "runner", task.RunnerID, "name", spec.Name, "backend", kind, "handle", handle, "took", now.Sub(start))
	a.report(ctx, TaskResult{
		TaskID:      task.ID,
		RunnerID:    task.RunnerID,
		OK:          true,
		Handle:      handle,
		State:       store.RunnerRegistering,
		CompletedAt: now,
	})
}

func (a *Agent) handleStop(ctx context.Context, task Task) {
	b, handle, ok, err := a.resolve(ctx, task.RunnerID)
	if !ok {
		if err != nil {
			a.reportUnsearchable(ctx, task, err)
			return
		}
		// Nothing to stop is the outcome the controller wanted, not an error.
		a.log.Info("stop task for a runner with no workload on this host; reporting it removed", "runner", task.RunnerID)
		a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: true, State: store.RunnerRemoved, CompletedAt: a.now()})
		return
	}

	timeout := task.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	a.markStopping(task.RunnerID)

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+stopMargin)
	defer cancel()

	if err := b.Stop(sctx, handle, timeout); err != nil && !errors.Is(err, backend.ErrNotFound) {
		a.log.Error("stopping runner failed", "runner", task.RunnerID, "handle", handle, "error", err)
		a.report(ctx, TaskResult{
			TaskID:      task.ID,
			RunnerID:    task.RunnerID,
			OK:          false,
			Handle:      handle,
			Error:       fmt.Sprintf("the %s backend could not stop runner %s within %s: %v", b.Kind(), task.RunnerID, timeout, err),
			CompletedAt: a.now(),
		})
		return
	}

	a.log.Info("runner stopped", "runner", task.RunnerID, "handle", handle, "timeout", timeout)
	// No state is claimed here on purpose: the runner's end of life is reported
	// from the workload's actual exit by the reconciler, which knows whether it
	// finished its job or died.
	a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: true, Handle: handle, CompletedAt: a.now()})
}

func (a *Agent) handleRemove(ctx context.Context, task Task) {
	b, handle, ok, err := a.resolve(ctx, task.RunnerID)
	if !ok {
		if err != nil {
			a.reportUnsearchable(ctx, task, err)
			return
		}
		// A workload that is already gone is exactly what this task asked for.
		a.untrack(task.RunnerID)
		a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: true, State: store.RunnerRemoved, CompletedAt: a.now()})
		return
	}

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()

	if err := b.Remove(rctx, handle); err != nil && !errors.Is(err, backend.ErrNotFound) {
		a.log.Error("removing runner failed", "runner", task.RunnerID, "handle", handle, "error", err)
		a.report(ctx, TaskResult{
			TaskID:      task.ID,
			RunnerID:    task.RunnerID,
			OK:          false,
			Handle:      handle,
			Error:       fmt.Sprintf("the %s backend could not remove runner %s (%s): %v", b.Kind(), task.RunnerID, handle, err),
			CompletedAt: a.now(),
		})
		return
	}

	a.untrack(task.RunnerID)
	a.log.Info("runner removed", "runner", task.RunnerID, "handle", handle)
	a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: true, Handle: handle, State: store.RunnerRemoved, CompletedAt: a.now()})
}

func (a *Agent) runLogTask(ctx context.Context, task Task) {
	if task.Kind == TaskCancelLogs {
		a.logs.cancel(task.StreamID)
		a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: true, CompletedAt: a.now()})
		return
	}

	b, handle, ok, err := a.resolve(ctx, task.RunnerID)
	if !ok {
		if err != nil {
			a.reportUnsearchable(ctx, task, err)
			return
		}
		a.report(ctx, TaskResult{
			TaskID:      task.ID,
			RunnerID:    task.RunnerID,
			OK:          false,
			Error:       fmt.Sprintf("no workload for runner %s on this host, so its logs are gone; an ephemeral runner's output is only available while its container exists", task.RunnerID),
			CompletedAt: a.now(),
		})
		return
	}

	opts := backend.LogOptions{Follow: true}
	if task.LogOptions != nil {
		opts = *task.LogOptions
	}
	if err = a.logs.start(ctx, task.StreamID, handle, b, opts); err != nil {
		a.report(ctx, TaskResult{
			TaskID:      task.ID,
			RunnerID:    task.RunnerID,
			OK:          false,
			Handle:      handle,
			Error:       fmt.Sprintf("could not relay logs for runner %s: %v", task.RunnerID, err),
			CompletedAt: a.now(),
		})
		return
	}
	a.report(ctx, TaskResult{TaskID: task.ID, RunnerID: task.RunnerID, OK: true, Handle: handle, CompletedAt: a.now()})
}

// report sends a task result. It uses a context shutdown does not cancel,
// because a result that never arrives leaves the controller waiting on a task
// it will redeliver forever.
func (a *Agent) report(ctx context.Context, res TaskResult) {
	if res.CompletedAt.IsZero() {
		res.CompletedAt = a.now()
	}
	// The claim on this runner is dropped before the result goes out, not
	// after the task's goroutine finishes unwinding. Reporting is the moment
	// the controller learns it may send the next task for this runner -- a
	// remove straight after a create -- and a task that arrives while the
	// finished one still holds the claim is skipped as a duplicate and
	// silently dropped, left waiting on redelivery.
	a.release(res.RunnerID, res.TaskID)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
	defer cancel()
	if err := a.tr.ReportResult(rctx, res); err != nil {
		a.log.Error("could not report a task result; the controller will redeliver the task",
			"task", res.TaskID, "runner", res.RunnerID, "ok", res.OK, "error", err)
	}
}

// reportUnsearchable reports a task the agent could not even look up, which is
// a backend that would not answer rather than a runner that has gone.
func (a *Agent) reportUnsearchable(ctx context.Context, task Task, err error) {
	a.log.Error("could not find the workload for a task", "task", task.ID, "kind", task.Kind, "runner", task.RunnerID, "error", err)
	a.report(ctx, TaskResult{
		TaskID:      task.ID,
		RunnerID:    task.RunnerID,
		OK:          false,
		Error:       fmt.Sprintf("could not tell whether runner %s is still on this host because its backend would not answer, so nothing was changed: %v", task.RunnerID, err),
		CompletedAt: a.now(),
	})
}

func (a *Agent) reportFailure(ctx context.Context, task Task, msg string) {
	a.report(ctx, TaskResult{
		TaskID:      task.ID,
		RunnerID:    task.RunnerID,
		OK:          false,
		Error:       msg,
		State:       store.RunnerFailed,
		CompletedAt: a.now(),
	})
}

// resolve finds the backend and handle for a runner, falling back to listing
// the host when the agent has no record -- which is the situation after an
// agent restart, and exactly when a stop or remove task matters most.
//
// The error return matters: "the runner is not here" and "this host could not
// be asked" look the same to a caller that only gets a bool, and reporting the
// second as the first would tell the controller a runner is gone while its job
// is still running.
func (a *Agent) resolve(ctx context.Context, runnerID string) (backend.Backend, backend.Handle, bool, error) {
	a.mu.Lock()
	r, ok := a.runners[runnerID]
	var kind store.BackendKind
	var handle backend.Handle
	if ok {
		kind, handle = r.kind, r.handle
	}
	a.mu.Unlock()

	if ok {
		if b, err := a.opts.Backends.Get(kind); err == nil {
			return b, handle, true, nil
		}
	}

	// Listing must survive shutdown for the same reason the lifecycle calls do:
	// a stop task that gives up half way tells nobody anything useful.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resolveTimeout)
	defer cancel()

	kinds := a.opts.Backends.Kinds()
	slices.Sort(kinds)
	var errs []error
	for _, k := range kinds {
		b, err := a.opts.Backends.Get(k)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		workloads, err := b.List(lctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("listing %s workloads: %w", k, err))
			continue
		}
		for _, w := range workloads {
			if w.RunnerID == runnerID {
				a.adopt(runnerID, k, w)
				return b, w.Handle, true, nil
			}
		}
	}
	return nil, "", false, errors.Join(errs...)
}

// adopt records a workload the agent found on the host but had no memory of,
// so that a restarted agent can manage runners it did not start.
func (a *Agent) adopt(runnerID string, kind store.BackendKind, w backend.Workload) {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.runners[runnerID]; ok {
		return
	}
	a.runners[runnerID] = &tracked{
		runnerID:   runnerID,
		name:       w.Name,
		kind:       kind,
		handle:     w.Handle,
		createdAt:  w.Status.StartedAt,
		phase:      w.Status.Phase,
		observedAt: now,
	}
	if a.runners[runnerID].createdAt.IsZero() {
		a.runners[runnerID].createdAt = now
	}
	delete(a.orphans, w.Handle)
}

func (a *Agent) claim(runnerID, taskID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, held := a.inflight[runnerID]; held {
		return false
	}
	a.inflight[runnerID] = taskID
	return true
}

// release drops taskID's claim on a runner, and does nothing if the claim has
// already moved on to another task. Naming the task is what makes releasing
// twice, or releasing one this task never held, harmless.
func (a *Agent) release(runnerID, taskID string) {
	a.mu.Lock()
	if a.inflight[runnerID] == taskID {
		delete(a.inflight, runnerID)
	}
	a.mu.Unlock()
}

func (a *Agent) markStopping(runnerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.runners[runnerID]; ok {
		r.stopping = true
	}
}

func (a *Agent) untrack(runnerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.runners[runnerID]; ok {
		delete(a.orphans, r.handle)
	}
	delete(a.runners, runnerID)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// waitFor waits on wg for at most d, reporting whether it finished.
func waitFor(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// notifier speaks systemd's sd_notify protocol, which is a datagram to a unix
// socket. That is a dozen lines here, against a dependency and its transitive
// tree for the same one Write.
type notifier struct {
	conn *net.UnixConn
}

func newNotifier(log *slog.Logger) *notifier {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	// A leading "@" means the abstract namespace, where the name starts with a
	// NUL byte.
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		log.Warn("NOTIFY_SOCKET is set but could not be opened, so systemd will not see this agent become ready; use Type=simple in the unit if this persists",
			"socket", addr, "error", err)
		return nil
	}
	return &notifier{conn: conn}
}

// send delivers one sd_notify line. Failures are ignored: the notification is
// an optimisation for systemd, never a reason to stop running runners.
func (n *notifier) send(state string) {
	if n == nil || n.conn == nil {
		return
	}
	_, _ = n.conn.Write([]byte(state))
}

func (n *notifier) close() {
	if n == nil || n.conn == nil {
		return
	}
	_ = n.conn.Close()
}

// watchdogLoop pets systemd's watchdog at half the interval it asked for, which
// is the margin sd_notify(3) recommends so a slow scheduling moment does not
// get the agent killed.
func (a *Agent) watchdogLoop(ctx context.Context) error {
	interval := watchdogInterval()
	if interval <= 0 {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			a.notify.send("WATCHDOG=1")
		}
	}
}

func watchdogInterval() time.Duration {
	raw := os.Getenv("WATCHDOG_USEC")
	if raw == "" {
		return 0
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	// WATCHDOG_PID names the process systemd expects the pings from; a child
	// that inherited the environment must not answer for its parent.
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	return time.Duration(usec) * time.Microsecond / 2
}
