package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/machine"
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
	// CreateTimeout has to cover a cold image pull on a slow link, which is
	// minutes, not seconds. It, StopMargin and RemoveTimeout are exported for
	// one reader: the controller's test that every task lease outlasts the
	// work the agent gives itself for that task.
	CreateTimeout = 15 * time.Minute
	// StopMargin is added to a task's stop timeout so the backend gets to run
	// its own kill path before the context expires.
	StopMargin    = 30 * time.Second
	RemoveTimeout = 2 * time.Minute
	// resolveTimeout bounds the backend listings used to find a runner the
	// agent has no record of.
	resolveTimeout = 30 * time.Second
	// minPollInterval keeps a controller that answers polls instantly from
	// turning the task loop into a spin.
	minPollInterval = 200 * time.Millisecond
	minPollBackoff  = time.Second
	maxPollBackoff  = 30 * time.Second
	// ShutdownTimeout covers an admitted create, failed-create cleanup and
	// its result plus the final inventory report. Supervisors must allow more
	// time than this: exiting halfway through a create strands its sidecar.
	// This is a maximum, not a delay, and never waits for running CI jobs.
	ShutdownTimeout = CreateTimeout + RemoveTimeout + 2*reportTimeout
	shutdownGrace   = ShutdownTimeout - reportTimeout
	// probeBudget bounds one round of capability probing, which is a ping per
	// registered backend.
	probeBudget = 15 * time.Second
	// backendProbeInterval is how often a healthy host re-checks what it can
	// run. Capabilities do change under a running agent -- a daemon is
	// upgraded, restarted, or stopped -- and a probe is a ping per backend, so
	// this is cheap enough to do regularly and slow enough to stay quiet.
	backendProbeInterval = 5 * time.Minute
	// orphanGrace is how long a workload nothing claims must stay unclaimed
	// before the agent reaps it.
	orphanGrace = 2 * time.Minute
	// removalSettle is how long the daemon may go on removing a runner's
	// workload before the agent calls the removal stuck.
	//
	// A removal the daemon has under way is not one that failed: Docker
	// finishes a removal it has started whether or not anyone is waiting,
	// and a docker-in-docker sidecar -- a whole nested image store to delete
	// -- can take minutes on a busy host. Reporting each attempt that found it
	// still going put a runner Zoomies "could not clean up" on the Runners
	// page for every slow removal, and counted every pass as a failed
	// attempt. Ten minutes is a daemon that has stopped making progress -- a
	// kill that never saw its exit, a filesystem it cannot release -- and
	// that is something an operator has to go and look at.
	removalSettle = 10 * time.Minute
	// removalMemory is how long the start of a removal is remembered once
	// nothing has asked about it, so an agent that runs for months does not
	// carry every runner whose removal was ever slow. A removal still being
	// asked about is never forgotten, however long it has been stuck.
	removalMemory = 24 * time.Hour
	// missingGrace stops a runner created moments ago from being declared gone
	// because the backend has not listed it yet.
	missingGrace = time.Minute
	// createBusyRetries bounds how many extra tries a create gets after a
	// FaultBackendBusy failure -- the one fault kind a retry can actually fix,
	// because the daemon is there and merely busier than
	// responseHeaderTimeout right now, not down or refusing the work. Every
	// other fault kind -- a bad image, a missing socket, a config the runner
	// refused -- fails on the first attempt, because nothing about trying
	// again would change the answer.
	//
	// createBusyBackoff is the base of the doubling wait between busy
	// retries, the same shape as scheduler.startBackoff: a daemon working
	// through a burst gets room to clear it rather than failing the runner on
	// the create that would have gone through a little later, all of it
	// spent inside this one task and well short of CreateTimeout. The tests
	// that exercise the loop's counting run it under testing/synctest, whose
	// virtual clock makes the real value here cost nothing to wait out.
	createBusyRetries = 3
	createBusyBackoff = 10 * time.Second
)

// Options configures an Agent.
type Options struct {
	// Doctor is enabled by production wiring; tests inject a fake or leave it nil.
	Doctor *hosttune.Monitor
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
	// FinishedRetention is how long a runner's workload stays on the host
	// after the controller has been told how the runner ended: the exited
	// container with its output, its docker-in-docker sidecar and any scratch
	// directory, or the process backend's runner directory. The window is
	// what gives an operator time to read a finished runner's output. Once
	// it has passed the agent deletes the workload itself, because nothing
	// else will: a clean exit is the normal end of an ephemeral runner's life
	// and the controller has no reason to send a task for a runner it already
	// considers gone. Zero removes a finished workload on the first pass
	// after it has been reported. The controller's retention.runners is a
	// different window -- it keeps the row, not the container.
	FinishedRetention time.Duration
	// PrewarmTimeout bounds background work separately from foreground starts.
	PrewarmTimeout time.Duration
	PrewarmJitter  time.Duration
	// BootstrapCPUGrace preserves normal quotas before host-pressure reductions.
	BootstrapCPUGrace time.Duration
	// DockerBuildCacheMB targets unused host Docker builder cache; 0 disables it.
	DockerBuildCacheMB int
	Logger             *slog.Logger
	// Clock is injectable so tests do not have to sleep.
	Clock func() time.Time
	// RandomFloat64 supplies jitter draws; nil uses the concurrency-safe system source.
	RandomFloat64 func() float64
	// Machine overrides what this agent reports about the host it runs on.
	// It exists for tests; leaving it nil makes the agent detect for itself.
	Machine *machine.Facts
	// SampleUsage replaces the local measurement for deterministic fixtures.
	// Nil uses the non-blocking whole-host sampler.
	SampleUsage func(int, int64) machine.Usage
	// ReadMemory replaces the look at the host's free memory and swap that the
	// memory valve takes before it lends anything, for the same reason. Nil
	// reads the host's procfs.
	ReadMemory func(totalMB int64) (machine.Memory, bool)
}

// Agent is the half of Zoomies that runs on a host with a container runtime. It
// long-polls the controller for tasks, materialises runners through a backend,
// and reports what it sees back.
//
// Everything it does is outbound. Nothing dials an agent.
type Agent struct {
	mutationsPaused atomic.Bool
	boostRenewedAt  time.Time
	usageSampler    machine.UsageSampler
	opts            Options
	log             *slog.Logger
	tr              Transport
	clock           func() time.Time
	heartbtI        time.Duration
	// retention is Options.FinishedRetention: how long a finished runner's
	// workload outlives its report before the reconciler deletes it.
	retention time.Duration
	logs      *logRelay
	notify    *notifier

	// sem bounds concurrent lifecycle tasks at Capacity, so a burst of creates
	// from a busy morning cannot fork-bomb the host.
	sem   chan struct{}
	tasks sync.WaitGroup

	// startup serialises creates and image prewarming across every pool on
	// this host. Capacity bounds running jobs, not concurrent image extraction
	// and DinD startup, which can overwhelm an otherwise healthy daemon.
	startup startupQueue
	warmed  map[warmKey]warmResult
	// fill runs tool cache fills one at a time: two pools' fills at once are
	// twice the bandwidth for the same finishing time.
	fill sync.Mutex

	// Runtime failures hold the next admission briefly without stopping jobs.
	runtimeFailures int
	runtimeRetryAt  time.Time
	// runtimeKind and runtimeError are the last failure, kept so the
	// heartbeat can say what the cooldown is about and not only that there
	// is one.
	runtimeKind  string
	runtimeError string
	// resolveRetry is how long a create waits between attempts to ask the
	// host whether its runner already exists; tests shorten it.
	resolveRetry []time.Duration

	mu sync.Mutex
	// hostID is empty until Join or a restored state file provides one.
	hostID   string
	reported Reported
	// runners is what the agent believes about the workloads it started. The
	// host is the real truth; reconcile.go corrects this map from it.
	runners map[string]*tracked
	// inflight keys the tasks currently executing on runner ID, so two tasks
	// for one runner never run at once. running records the kind of the
	// dispatched task holding each claim, and waiting the one task queued
	// behind it -- what makes a remove that overtakes a create run as soon as
	// the create is done rather than waiting out the task's lease.
	inflight map[string]bool
	running  map[string]TaskKind
	waiting  map[string]Task
	// taskCtx is the task loop's context, so a task handed a claim as it is
	// released runs under the same cancellation as one delivered by a poll.
	// It is never nil: an agent that is not running still releases claims --
	// the reconciler takes them, and so does a test -- and a handover that
	// had to check for a missing context would be a branch nothing exercises.
	taskCtx context.Context
	// orphans records when an unclaimed workload was first seen, which is how
	// "the controller has not mentioned it in a while" is measured.
	orphans          map[backend.Handle]time.Time
	inventoryPending map[store.BackendKind]bool
	// removing records, by runner ID, when the daemon was first found still
	// removing that runner's workload, and when it was last asked about. It is
	// what bounds how long a slow removal is waited out before it is reported
	// as stuck; see removalSettle.
	removing map[string]removal
	// backendInfo is the last probe, sent with heartbeats, and probedAt is
	// when it was taken. It is refreshed as the agent runs: what a host can do
	// is not a fact of its startup.
	backendInfo []backend.Info
	probedAt    time.Time
	// cordoned mirrors the controller's flag, logged when it changes.
	cordoned bool
	// incompatible and incompatibleReason are the controller's conclusion
	// about this binary, kept so a create arriving anyway can be refused with
	// the sentence the controller wrote rather than one invented here.
	incompatible       bool
	incompatibleReason string
	// warnedSkew keeps a version-skew warning to one line per run.
	warnedSkew bool
	// cpuFactor is the last heartbeat directive's throttle factor, the share
	// of its allocated CPU each runner here is to be left. A nil directive,
	// or a zero factor from one, is 1: an older controller never throttles.
	cpuFactor float64
	// elasticCPU is replaced as a set on every heartbeat. A runner omitted by
	// the controller therefore returns to its guaranteed creation allocation.
	elasticCPU map[string]float64
	// missedBeats counts heartbeats in a row the controller did not answer;
	// see expireBoosts.
	missedBeats int
	// memory is the memory valve's rules from the last heartbeat that carried
	// any, and memoryUnsupported the container runtimes that refused to change a
	// live limit, with the reason. Neither is expired by a missed heartbeat:
	// a raised limit cannot be taken back, so the rules are worked within, not
	// leased.
	memory            memoryRules
	memoryUnsupported map[store.BackendKind]string

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
	terminal    bool
	hostRemoved bool
	// terminalAt is when that end of life was observed, which is what the
	// retention window on its workload counts from.
	terminalAt time.Time
	// reported records that the controller accepted a report carrying the
	// terminal state. Until it has, the workload stays on the host: removing
	// it first would take the exit code with it, and leave the controller
	// holding a live row for a runner that no longer exists.
	reported bool

	state      store.RunnerState
	phase      backend.Phase
	stats      backend.Stats
	exitCode   int
	message    string
	observedAt time.Time

	// fault is the classification of a failed end of life. It is kept beside
	// the message because the heartbeat's copy of this runner is the retry when
	// the one-shot report is lost, and the controller cannot recover the
	// meaning of an exit code from the sentence alone.
	fault store.FaultKind

	// resources is the allocation the workload was created with: the base a
	// throttle scales from. It comes from the spec at create and from the
	// workload's own labels at adopt, because a restarted agent remembers
	// nothing about how a container was made.
	resources store.Resources
	// appliedCPUFactor is the throttle factor this runner's quota was last
	// set to, so the daemon is not asked again on every beat. Nil is
	// "unknown", which is what an adopted runner starts as: the agent that
	// made it may have throttled it and then died, so the first beat
	// reconciles it whichever way the controller now says.
	appliedCPUFactor *float64
	// failedCPUFactor is the factor the daemon last refused for this runner,
	// so the refusal is logged once and not on every beat the throttle
	// stands. The update is still retried: the daemon may have recovered.
	failedCPUFactor *float64
	// pendingCPUFactor is a factor an update is in flight for.
	//
	// applyThrottle chooses its candidates under the lock and then releases it
	// to talk to the daemon, so without this a second pass starting in that
	// window sees a runner whose applied factor has not been written yet and
	// asks the daemon for the same quota again. Two callers are all it takes:
	// a create throttling the runner it has just made, and the heartbeat that
	// lands while it is doing so.
	//
	// The duplicate changes nothing -- the update is idempotent, which is why
	// this went unnoticed -- but it is a second daemon call per create on
	// exactly the host that is already overwhelmed, which is the one place
	// Zoomies should be asking for less.
	pendingCPUFactor *float64
	// valve is the memory valve's record of this runner, nil until the
	// controller has a rule for it.
	valve *memoryValve
}

func (t *tracked) report() RunnerReport {
	stats := t.stats
	factor := 1.0
	if t.appliedCPUFactor != nil && *t.appliedCPUFactor > 0 {
		factor = *t.appliedCPUFactor
	}
	stats.CPUAllocationFactor = factor
	stats.MemoryValve = t.valve.sample()
	return RunnerReport{
		RunnerID:    t.runnerID,
		HostRemoved: t.hostRemoved,
		State:       t.state,
		Handle:      t.handle,
		Phase:       t.phase,
		ExitCode:    t.exitCode,
		Fault:       t.fault,
		Message:     t.message,
		Stats:       stats,
		ObservedAt:  t.observedAt,
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
	if opts.BootstrapCPUGrace < 0 || opts.BootstrapCPUGrace > 10*time.Minute {
		return nil, fmt.Errorf("agent: agent.bootstrap_cpu_grace must be between 0s and 10m")
	}
	if opts.FinishedRetention < 0 {
		return nil, fmt.Errorf("agent: finished retention %s is negative; set agent.finished_retention to how long a finished runner's output should stay readable on the host, or to 0s to remove it as soon as the controller has been told", opts.FinishedRetention)
	}
	if opts.DockerBuildCacheMB < 0 || opts.DockerBuildCacheMB > 1048576 {
		return nil, fmt.Errorf("agent: Docker build cache target must be between 0 and 1048576 MiB")
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
		opts:             opts,
		log:              log,
		tr:               opts.Transport,
		clock:            clock,
		heartbtI:         interval,
		retention:        opts.FinishedRetention,
		sem:              make(chan struct{}, opts.Capacity),
		resolveRetry:     resolveRetryDelays,
		runners:          make(map[string]*tracked),
		inflight:         make(map[string]bool),
		running:          make(map[string]TaskKind),
		waiting:          make(map[string]Task),
		taskCtx:          context.Background(),
		orphans:          make(map[backend.Handle]time.Time),
		inventoryPending: make(map[store.BackendKind]bool),
		removing:         make(map[string]removal),
		cpuFactor:        1,
	}
	a.logs = newLogRelay(opts.Transport, log)
	// Built here rather than in Run, so the field is set before any goroutine
	// exists. Run's loop is not the only caller of heartbeat -- the tests call
	// it directly beside a running agent -- and an assignment made inside Run
	// raced with that read. newNotifier is a nil notifier when systemd is not
	// listening and every method on it is nil-safe, so nothing else changes;
	// Run still closes it on the way out.
	a.notify = newNotifier(log)
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

// Reported is what this agent told the controller about the machine it is on,
// and what the controller said it was.
//
// Kept so the installer can print it. An operator watching a join finish has no
// other way to see either: the figures go on the wire and the skew warning goes
// to a log, so a host that arrived on the wrong build or measured nothing
// looked exactly like one that did neither -- which is how a fleet ends up with
// a host nobody noticed was reporting no size at all.
type Reported struct {
	CPUs              int
	MemoryMB          int64
	DiskTotalMB       int64
	DiskFreeMB        int64
	ControllerVersion string
}

// Reported returns what the last join sent, and what it was answered with. The
// zero value means this agent has not joined in this process.
func (a *Agent) Reported() Reported {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reported
}

func (a *Agent) setJoined(r Reported) {
	a.mu.Lock()
	a.reported = r
	a.mu.Unlock()
}

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

// machine is what this agent reports about the host it runs on. Detection is
// re-run rather than cached so that a host resized in place -- a VM given more
// cores, a container's cgroup limit raised -- stops describing itself as the
// machine it used to be on the next heartbeat.
func (a *Agent) machine() machine.Facts {
	if a.opts.Machine != nil {
		return *a.opts.Machine
	}
	return machine.Detect()
}

// Join enrols this host with a controller, redeeming a short-lived join token
// for the long-lived agent token that every later call carries.
func (a *Agent) Join(ctx context.Context, joinToken string) error {
	if strings.TrimSpace(joinToken) == "" {
		return errors.New("agent: no join token; mint one in the UI under Hosts, or with `zoomies hosts join-token create`, and pass it as --token")
	}

	// Probe first so the controller learns what this host can actually do
	// before it is offered any work.
	infos := a.opts.Backends.Probe(ctx)
	a.mu.Lock()
	a.backendInfo = infos
	a.probedAt = a.now()
	a.mu.Unlock()

	m := a.machine()
	cpus, memoryMB := hostSize(infos, m)
	total, free := a.workDirSpace()
	req := JoinRequest{
		ProtocolVersion: ProtocolVersion,
		JoinToken:       joinToken,
		Name:            a.opts.Name,
		Capacity:        a.opts.Capacity,
		OS:              m.OS,
		Distro:          m.Distro,
		OSVersion:       m.OSVersion,
		Arch:            m.Arch,
		CPUs:            cpus,
		MemoryMB:        memoryMB,
		DiskTotalMB:     total,
		DiskFreeMB:      free,
		Version:         version.Version,
		Features:        a.features(),
		Labels:          a.opts.Labels,
		Backends:        infos,
		// A host that has joined before proves it is itself with the token it
		// still holds, which is what lets it reclaim its own row rather than
		// being refused as a name collision. A first join has none, and sending
		// an empty string is exactly right there.
		PreviousToken:  a.previousToken(),
		PreviousHostID: a.previousHostID(),
	}
	resp, err := a.tr.Join(ctx, req)
	if err != nil {
		return fmt.Errorf("agent: joining %s: %w", a.tr.Describe(), err)
	}

	creds := Credentials{HostID: resp.HostID, AgentToken: resp.AgentToken, Controller: a.tr.Describe()}
	if t, ok := a.tr.(interface{ TailcatAddress() string }); ok {
		creds.TailcatAddress = t.TailcatAddress()
	}
	path := StatePath(a.opts.WorkDir)
	if err := Save(path, creds); err != nil {
		// The token is shown exactly once, so a save failure has to be loud:
		// the host has joined but cannot prove it after a restart.
		return fmt.Errorf("agent: joined %s as host %s but could not persist the agent token to %s; the host will have to join again after a restart: %w", a.tr.Describe(), resp.HostID, path, err)
	}
	a.setCredentials(creds)
	a.setJoined(Reported{
		CPUs: cpus, MemoryMB: memoryMB, DiskTotalMB: total, DiskFreeMB: free,
		ControllerVersion: resp.ControllerVersion,
	})

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

// previousToken returns the agent token this host was last issued, or "" when
// it has never joined or the credentials file is gone.
//
// A missing or unreadable file is not an error here: it only means this join
// cannot claim an existing row of the same name, which the controller says in
// as many words if there is one.
func (a *Agent) previousToken() string {
	creds, _ := a.previousCredentials()
	return creds.AgentToken
}

// previousHostID returns the row this host was last enrolled as, or "" when it
// has never joined. Sent beside previousToken so the controller can find the
// row this machine already owns without relying on its name, which changes
// when the agent learns to describe the machine differently.
func (a *Agent) previousHostID() string {
	creds, _ := a.previousCredentials()
	return creds.HostID
}

func (a *Agent) previousCredentials() (Credentials, bool) {
	creds, err := Load(StatePath(a.opts.WorkDir))
	if err != nil {
		return Credentials{}, false
	}
	return creds, true
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
		"finished_retention", a.retention,
		"version", version.Short())

	// Adopt what is already running before anything can reap it.
	//
	// The agent's unit restarts always, and on a single-VM install the agent
	// lives inside the controller, so an agent starting over live workloads is
	// an everyday event rather than an edge case. A fresh agent knows nothing,
	// and the reconciler removes what nothing claims: without this, every job
	// running on the host is destroyed two minutes after the agent comes back.
	a.adoptExisting(ctx)

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	a.taskCtx = loopCtx
	a.mu.Unlock()

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
	run("stats", a.statsLoop)
	run("watchdog", a.watchdogLoop)
	run("native-logs", a.nativeLogLoop)
	run("boost-expiry", a.boostExpiryLoop)
	run("memory-guard", a.memoryGuardLoop)
	if a.opts.DockerBuildCacheMB > 0 {
		run("build-cache", a.buildCacheLoop)
	}

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

	if !a.waitForTasks(ctx, shutdownGrace) {
		a.log.Warn("shutdown grace expired with lifecycle tasks still unfinished", "grace", shutdownGrace)
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

	infos := a.refreshBackends(ctx)
	runners := a.Runners()
	m := a.machine()
	cpus, memoryMB := hostSize(infos, m)
	total, free := a.workDirSpace()
	resp, err := a.tr.Heartbeat(hctx, HeartbeatRequest{
		Doctor:          a.opts.Doctor.Latest(ctx),
		Usage:           a.hostUsage(infos, cpus, memoryMB),
		ProtocolVersion: ProtocolVersion,
		Features:        a.features(),
		Capacity:        a.opts.Capacity,
		Version:         version.Version,
		CPUs:            cpus,
		MemoryMB:        memoryMB,
		DiskTotalMB:     total,
		DiskFreeMB:      free,
		Backends:        infos,
		Runners:         runners,
		Runtime:         a.runtimeReport(),
	})
	if err != nil {
		a.expireBoosts(ctx)
		return err
	}
	a.mu.Lock()
	a.missedBeats = 0
	a.mu.Unlock()
	// The beat carried every runner the agent tracks, so the controller now
	// knows how each finished one ended, and its workload may go.
	a.mutationsPaused.Store(resp.MutationsPaused)
	a.markReported(runners)

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
	incompatibleChanged := a.incompatible != resp.Incompatible
	a.cordoned = resp.Cordoned
	a.incompatible = resp.Incompatible
	a.incompatibleReason = resp.IncompatibleReason
	a.mu.Unlock()
	if incompatibleChanged {
		if resp.Incompatible {
			// Error rather than warn: nothing is wrong with this machine and
			// nothing will fix itself. A person has to upgrade a binary, and
			// until they do this host is quietly out of the fleet.
			a.log.Error("this agent is not compatible with its controller",
				"reason", incompatibleSentence(resp),
				"fix", "upgrade this agent to the controller's release")
		} else {
			a.log.Info("this agent is compatible with its controller again; it may take new runners")
		}
	}
	if changed {
		if resp.Cordoned {
			a.log.Info("host cordoned; the controller will stop scheduling new runners here")
		} else {
			a.log.Info("host uncordoned; the controller may schedule runners here again")
		}
	}

	// Everything this agent adopted stays tracked, and therefore safe from the
	// reconciler, until the controller says it does not know it. Releasing
	// only what it names is what keeps a restart from destroying live jobs
	// while still clearing up a workload whose runner was deleted meanwhile.
	if !resp.MutationsPaused {
		a.releaseUnknown(resp.UnknownRunners)
	}

	// The throttle is applied after the release above, so a runner the
	// controller has just disowned is not the one whose quota is moved, and
	// under the heartbeat's own deadline: an update the daemon sits on must
	// not hold the beat open past the interval.
	if !resp.MutationsPaused {
		a.applyResourceDirectives(hctx, resp.Throttle, resp.ElasticCPU)
		a.applyMemoryDirective(resp.ElasticMemory)
	}

	if resp.ResyncRequested {
		// The controller restarted and lost its cache, so re-probe rather than
		// send it a stale capability list.
		a.mu.Lock()
		a.backendInfo = a.opts.Backends.Probe(hctx)
		a.probedAt = a.now()
		a.mu.Unlock()
		if err := a.tr.ReportRunners(hctx, runners); err != nil {
			a.log.Warn("resync report failed", "error", err)
		}
	}
	return nil
}

// refreshBackends returns the capability probe the next heartbeat should carry,
// re-running it when the last one is stale.
//
// A probe is not a fact about startup. An agent that came up before its Docker
// daemon -- the ordinary case on a rebooting host, and on one whose operator
// has just added the agent's user to the docker group -- reported no backend at
// join, and a host with no backends matches no pool: the pool looks healthy,
// its jobs queue forever, and nothing in the fleet ever says why. So while
// nothing is available the probe is retried on every heartbeat, which is the
// cheapest way for that host to become schedulable on its own; once something
// answers it settles down to backendProbeInterval, which is what notices a
// daemon that was later stopped or upgraded.
func (a *Agent) refreshBackends(ctx context.Context) []backend.Info {
	a.mu.Lock()
	last, at := a.backendInfo, a.probedAt
	a.mu.Unlock()

	if !a.probeDue(last, at) || ctx.Err() != nil {
		return last
	}
	// The probe gets its own budget rather than sharing the heartbeat's: a
	// daemon that hangs on a ping must not cost the controller the heartbeat
	// that keeps this host marked healthy.
	pctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	fresh := a.opts.Backends.Probe(pctx)
	if ctx.Err() != nil {
		// The agent is shutting down. A probe cut short says every daemon is
		// unreachable, which is a lie worth neither storing nor logging.
		return last
	}
	a.mu.Lock()
	a.backendInfo = fresh
	a.probedAt = a.now()
	a.mu.Unlock()

	if was, now := availableKinds(last), availableKinds(fresh); !slices.Equal(was, now) {
		a.log.Info("this host's backends changed",
			"was", strings.Join(was, ","), "now", strings.Join(now, ","))
		for _, i := range fresh {
			if !i.Available {
				a.log.Info("a backend is not usable on this host", "backend", i.Kind, "detail", i.Detail)
			}
		}
	}
	return fresh
}

// probeDue reports whether it is time to look again: always when the last probe
// found nothing usable, and otherwise once every backendProbeInterval.
func (a *Agent) probeDue(last []backend.Info, at time.Time) bool {
	if at.IsZero() {
		return true
	}
	if len(availableKinds(last)) == 0 {
		return true
	}
	return a.now().Sub(at) >= backendProbeInterval
}

// availableKinds is the sorted list of backends that answered, which is exactly
// what the controller stores and matches pools against.
func availableKinds(infos []backend.Info) []string {
	var out []string
	for _, i := range infos {
		if i.Available {
			out = append(out, string(i.Kind))
		}
	}
	slices.Sort(out)
	return out
}

func (a *Agent) warnSkew(controllerVersion string) {
	// Releases are compared, not commits.
	//
	// This used to compare version.Short(), which carries the commit, against
	// the controller's -- so two builds of one tag warned here while the host
	// row, which stores the bare version, said the fleet matched. The agent
	// and the Hosts page disagreed, and one of them had to be wrong. Two
	// builds of one tag are the same release: it is worth knowing in a bug
	// report and it is not skew.
	skew := version.CompareBuilds(version.Version, bareVersion(controllerVersion))
	if skew == version.SkewNone {
		return
	}
	a.mu.Lock()
	first := !a.warnedSkew
	a.warnedSkew = true
	a.mu.Unlock()
	if first {
		a.log.Warn("this agent is a different release from its controller",
			"skew", string(skew),
			"controller_version", controllerVersion, "agent_version", version.Short(),
			"fix", "upgrade the controller first, then its agents; an agent ahead of its controller is the direction nobody tests")
	}
}

// bareVersion strips the commit the controller sends beside its version, so
// the two sides compare the same thing: "v1.2.3 (abc1234)" is release v1.2.3.
func bareVersion(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// jitter takes a random slice off the end of a backoff, up to a quarter of it.
//
// Every agent in a fleet fails the same poll at the same instant when the
// controller goes down, and without this they all wait exactly the same
// second and reconnect together -- a thundering herd against a controller
// that has only just come back up.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d - time.Duration(rand.Int64N(int64(d)/4+1))
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
			wait := jitter(backoff)
			a.log.Warn("task poll failed; backing off", "error", err, "retry_in", wait)
			if !sleepCtx(ctx, wait) {
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

		if wait := pollWait(batch, a.now().Sub(started)); wait > 0 && !sleepCtx(ctx, wait) {
			return nil
		}
	}
}

// pollWait says how long to wait before polling again, given the batch just
// received and how long the poll that returned it took.
//
// A controller sheds load by asking every agent it is holding to wait, so the
// wait it asks for is jittered: without that the whole fleet would come back
// in the same instant and re-form the queue the backoff was spreading out.
func pollWait(batch *TaskBatch, elapsed time.Duration) time.Duration {
	if batch.Backoff > 0 {
		return jitter(batch.Backoff)
	}
	if len(batch.Tasks) > 0 {
		return 0
	}
	// Guard against a controller that answers polls instantly: without this
	// the loop would spin at whatever rate it can dial.
	if elapsed < minPollInterval {
		return minPollInterval - elapsed
	}
	return 0
}

// dispatch validates a task and starts it, or reports why it cannot run.
// Silence is the one outcome the controller cannot act on, so every task ends
// in a result -- including the ones that were malformed.
// incompatibleSentence prefers the controller's own words -- it is the side
// that knows both numbers -- and falls back to what this agent can say alone.
func incompatibleSentence(resp *HeartbeatResponse) string {
	if strings.TrimSpace(resp.IncompatibleReason) != "" {
		return resp.IncompatibleReason
	}
	return fmt.Sprintf("this agent speaks protocol version %d and the controller speaks %d",
		ProtocolVersion, resp.ProtocolVersion)
}

// refuseNewWork returns why this host may not take a new runner, or "" when it
// may. Incompatibility is named first: it is the one of the two an operator
// did not choose, and the one whose fix is a version rather than a decision.
func (a *Agent) refuseNewWork() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.incompatibleReason != "":
		return a.incompatibleReason
	case a.incompatible:
		return "this agent speaks a protocol version the controller does not; upgrade it to the controller's release"
	case a.cordoned:
		return "this host is cordoned, so it takes no new runners"
	}
	return ""
}

func (a *Agent) dispatch(ctx context.Context, task Task) {
	if err := validateTask(task); err != nil {
		a.log.Warn("rejecting task", "task", task.ID, "kind", task.Kind, "error", err)
		a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: false, Error: err.Error(), CompletedAt: a.now()})
		return
	}

	// A host the controller has told to take no new work refuses to create
	// one, and does everything else as normal.
	//
	// This is the belt to the controller's braces: the scheduler already
	// excludes a cordoned or incompatible host, so a create arriving here is a
	// controller that has not caught up -- a plan computed before the flag, or
	// one that does not know about the flag at all. Refusing only creates is
	// what keeps the rest true: a cordoned host still has runners to drain,
	// stop and stream logs from, and an agent that stopped polling would
	// strand every one of them.
	if task.Kind == TaskCreateRunner {
		if why := a.refuseNewWork(); why != "" {
			a.log.Warn("refusing to create a runner", "task", task.ID, "runner", task.RunnerID, "reason", why)
			a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: false, Error: why, CompletedAt: a.now()})
			return
		}
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
	case TaskFillToolCache:
		// Nor is a fill, which is minutes of downloading: in a lifecycle slot
		// or the startup queue it would hold back the very runners it exists
		// to speed up. Fills run one at a time instead, beside everything.
		a.tasks.Add(1)
		go func() {
			defer a.tasks.Done()
			a.handleToolFill(ctx, task)
		}()
		return
	}

	// The claim serialises work on one runner, so it says something only about
	// a task that names one. A prewarm names none, and keying it on the empty
	// string made every prewarm in a poll collide with every other: the first
	// took the key and the rest were dropped, silently and for good, because a
	// dropped task reports no result and its key stays outstanding on the
	// controller. Two Docker pools on one host meant one of them was never
	// prewarmed again.
	if task.RunnerID != "" {
		claimed, held := a.claimOrQueue(task)
		switch {
		case held:
			// A remove arriving while the create it cancels is still running
			// is the controller changing its mind, not a redelivery. Dropping
			// it left the workload on the host until the task's lease expired
			// minutes later, with the row already gone from the UI -- so it
			// waits for the claim instead and runs the moment it is free.
			a.log.Debug("holding a task until the one in flight for this runner finishes",
				"task", task.ID, "kind", task.Kind, "runner", task.RunnerID)
			return
		case !claimed:
			// The controller redelivers any task it has not seen a result for,
			// so a duplicate arriving while the first is still running is
			// expected. Skipping rather than queueing is what stops two
			// creates for one runner from racing each other on the host.
			a.log.Debug("skipping task; another task for this runner is already in flight",
				"task", task.ID, "kind", task.Kind, "runner", task.RunnerID)
			return
		}
	}

	a.start(ctx, task)
}

// start runs a task whose runner's claim is already held, and gives the claim
// up -- or hands it to whatever is waiting behind it -- when the task is done.
func (a *Agent) start(ctx context.Context, task Task) {
	queuedAt := a.now()
	claimed := task.RunnerID != ""
	var admission *startupTicket
	if task.Kind == TaskCreateRunner {
		admission = a.startup.enqueue(true)
	}
	a.tasks.Add(1)
	go func() {
		defer a.tasks.Done()
		var releaseOnce sync.Once
		release := func() {
			releaseOnce.Do(func() {
				if claimed {
					a.release(task.RunnerID)
				}
			})
		}
		defer release()
		if task.Kind == TaskPrewarmImage {
			// Stagger background pulls without occupying startup admission or a
			// lifecycle slot; foreground work may pass during this delay.
			delay := time.Duration(a.randomFraction() * float64(a.opts.PrewarmJitter))
			if !sleepCtx(ctx, delay) {
				a.reportFailure(ctx, task, "agent shut down before background preparation", store.FaultBackend)
				return
			}
			admission = a.startup.enqueue(false)
		}
		// Wait before taking a lifecycle slot, so queued starts cannot crowd
		// out stops and removals. The backend's create timeout begins only
		// after admission, and shutdown still cancels work waiting here.
		if task.Kind == TaskCreateRunner || task.Kind == TaskPrewarmImage {
			a.log.Debug("waiting for this host's startup slot",
				"task", task.ID, "kind", task.Kind, "runner", task.RunnerID)
			defer a.startup.done(admission)
			select {
			case <-admission.ready:
				wait := a.now().Sub(queuedAt)
				task.startupWait = &wait
				a.log.Info("startup admitted", "kind", task.Kind, "queue_wait", wait)
			case <-ctx.Done():
				release()
				a.reportNotStarted(ctx, task)
				return
			}
			if !a.waitForRuntime(ctx) {
				release()
				// Nothing has been attempted on the runtime, so this is a task
				// handed back like any other shutdown before it started, not a
				// backend failure to be counted against the pool.
				a.reportNotStarted(ctx, task)
				return
			}
		}
		if ctx.Err() != nil {
			release()
			a.reportNotStarted(ctx, task)
			return
		}
		select {
		case a.sem <- struct{}{}:
		case <-ctx.Done():
			release()
			a.reportNotStarted(ctx, task)
			return
		}
		defer func() { <-a.sem }()
		if a.mutationsPaused.Load() {
			release()
			a.reportNotStarted(ctx, task)
			return
		}
		if ctx.Err() != nil {
			release()
			a.reportNotStarted(ctx, task)
			return
		}
		if task.Kind == TaskCreateRunner {
			// A cancellation or cordon may have arrived while this create
			// waited. Do not build a runner just to remove it immediately.
			a.mu.Lock()
			_, superseded := a.waiting[task.RunnerID]
			a.mu.Unlock()
			if superseded {
				release()
				a.report(ctx, TaskResult{
					TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID,
					OK: true, CompletedAt: a.now(),
				})
				return
			}
			if why := a.refuseNewWork(); why != "" {
				release()
				a.reportFailure(ctx, task, why, store.FaultBackend)
				return
			}
		}
		a.runTask(ctx, task, release)
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
	case TaskPrewarmImage:
		if task.PoolID == "" || task.Image == "" || !task.PullPolicy.Valid() {
			return errors.New("prewarm_image task needs a pool, image, and valid pull policy")
		}
	case TaskFillToolCache:
		if task.Spec == nil || task.Spec.PoolID == "" || task.Spec.Image == "" {
			return errors.New("fill_tool_cache task needs a spec naming the pool and its image")
		}
		if len(task.Tools) == 0 {
			return errors.New("fill_tool_cache task names no toolchains to fill")
		}
	default:
		return fmt.Errorf("unknown task kind %q; this agent speaks protocol version %d, so upgrade it to match the controller", task.Kind, ProtocolVersion)
	}
	return nil
}

func (a *Agent) runTask(ctx context.Context, task Task, release func()) {
	switch task.Kind {
	case TaskCreateRunner:
		a.handleCreate(ctx, task, release)
	case TaskStopRunner:
		a.handleStop(ctx, task, release)
	case TaskRemoveRunner:
		a.handleRemove(ctx, task, release)
	case TaskPrewarmImage:
		a.handlePrewarm(ctx, task, release)
	}
}

func (a *Agent) handlePrewarm(ctx context.Context, task Task, release func()) {
	b, err := a.opts.Backends.Get(task.Backend)
	if err != nil {
		release()
		a.reportFailure(ctx, task, err.Error(), backend.Fault(err))
		return
	}
	p, ok := b.(backend.ImagePrewarmer)
	if !ok {
		release()
		a.reportFailure(ctx, task, fmt.Sprintf("the %s backend does not support image prewarming", task.Backend), store.FaultBackend)
		return
	}
	budget := a.opts.PrewarmTimeout
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	warmCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	warm, err := a.prewarm(warmCtx, b, p, task)
	a.runtimeResult(err)
	release()
	cached := warm.cached
	duration := warm.duration
	res := TaskResult{
		TaskID: task.ID, Kind: task.Kind, OK: err == nil, Digest: warm.digest,
		PrewarmCached: &cached, PrewarmDuration: &duration, CompletedAt: a.now(),
	}
	if err != nil {
		res.Error = err.Error()
		// Classified here for the same reason a create is: an image the host
		// cannot pull is found by the prewarm first, and the controller can
		// only name the registry if it is told this was the image.
		res.Fault = backend.Fault(err)
	}
	a.report(ctx, res)
}

func (a *Agent) handleCreate(ctx context.Context, task Task, release func()) {
	kind := task.Backend
	if kind == "" {
		kind = a.opts.DefaultBackend
	}
	b, err := a.opts.Backends.Get(kind)
	if err != nil {
		release()
		a.reportFailure(ctx, task, fmt.Sprintf("this host has no %s backend (registered: %s); point the pool at a backend this host runs, or set agent.backend: %v", kind, kindList(a.opts.Backends.Kinds()), err), store.FaultConfig)
		return
	}

	spec := *task.Spec
	if spec.RunnerID == "" {
		spec.RunnerID = task.RunnerID
	}

	// Delivery is at-least-once. A create whose result never reached the
	// controller comes round again once its lease expires, and the backend's
	// Create begins by removing any workload of the runner's name -- so a
	// redelivery used to destroy a runner that may have been mid-job and
	// rebuild it with a JIT configuration GitHub had already consumed. A
	// workload this host already has for the runner is the answer to the task.
	existing, handle, ok, err := a.resolveForCreate(ctx, task.RunnerID)
	if err != nil {
		// An uncertain inventory is not permission to replace by name: the
		// backend's Create begins by removing any workload of the runner's
		// name, and a redelivered create over a live job would destroy it.
		//
		// On the first delivery no workload of this runner's can exist yet,
		// so there is nothing to protect and everything to gain from saying
		// so: the controller fails the runner now and makes another, rather
		// than in five minutes when the provision timeout notices. A
		// redelivery, or a controller too old to say which this is, gets the
		// silence the lease was designed for.
		a.runtimeResult(err)
		release()
		if task.Attempt == 1 {
			a.log.Error("could not establish whether this runner already exists; nothing was created",
				"task", task.ID, "runner", task.RunnerID, "backend", kind, "error", err)
			a.reportFailure(ctx, task, fmt.Sprintf("this host could not ask its %s backend whether runner %s already exists, so nothing was created; check that the daemon is running and answering: %v", kind, spec.Name, err), store.FaultBackend)
			return
		}
		a.log.Warn("could not establish whether this runner already exists; leaving its create task for redelivery without changing workloads",
			"task", task.ID, "runner", task.RunnerID, "attempt", task.Attempt, "error", err)
		return
	}
	if ok {
		state := store.RunnerRegistering
		a.mu.Lock()
		if r := a.runners[task.RunnerID]; r != nil && r.state != "" {
			state = r.state
		}
		a.mu.Unlock()
		a.log.Info("a create task came again for a runner this host already has; reporting the existing workload",
			"runner", task.RunnerID, "backend", existing.Kind(), "handle", handle)
		release()
		now := a.now()
		a.report(ctx, TaskResult{
			TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true,
			Handle: handle, State: state, CompletedAt: now,
		})
		return
	}

	// The agent's own work directory is deliberately not handed to the
	// backend as the runner's. For a container backend a spec WorkDir is bind
	// mounted over the runner's _work, and the agent's directory is the wrong
	// thing to mount three times over: it is one directory shared by every
	// concurrent runner on the host; it belongs to the agent's account, which
	// is not the image's runner uid, so the runner cannot write to it; and
	// when the agent is itself a container -- the compose deployment -- the
	// path names a place inside the agent's container, which the host daemon
	// resolves on the host instead, mounting an empty root-owned directory
	// that fails the first job. A runner container's own filesystem is the
	// right scratch space for an ephemeral runner. The process backend keeps
	// its own per-runner directories under the agent's work directory and
	// never read this field.

	// Tasks are given a context that shutdown does not cancel: a create that is
	// half done is worse than one that finishes and is reported.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CreateTimeout)
	defer cancel()

	start := a.now()
	var created backend.CreateResult
	for retry := 0; ; retry++ {
		// Check only after adoption: an expired task is not permission to disturb
		// a workload that already consumed its one-use registration.
		if (!spec.StartBefore.IsZero() && !a.now().Before(spec.StartBefore)) ||
			(!spec.Credentials.ExpiresAt.IsZero() && !a.now().Before(spec.Credentials.ExpiresAt)) {
			release()
			a.reportFailure(ctx, task, "the runner's provisioning deadline or registration credential expired while waiting to start; nothing was created and queued demand will be retried with fresh credentials", store.FaultRegistration)
			return
		}

		if timed, ok := b.(backend.TimedCreator); ok {
			created, err = timed.CreateWithResult(cctx, spec)
		} else {
			created.Handle, err = b.Create(cctx, spec)
		}
		if err == nil || backend.Fault(err) != store.FaultBackendBusy || retry >= createBusyRetries {
			break
		}
		wait := createBusyBackoff << retry
		a.log.Warn("the backend was busy creating a runner; trying again shortly",
			"runner", task.RunnerID, "name", spec.Name, "backend", kind,
			"retry", retry+1, "of", createBusyRetries, "retry_in", wait, "error", err)
		if !sleepCtx(cctx, wait) {
			break
		}
	}
	a.runtimeResult(err)
	if err != nil {
		a.log.Error("creating runner failed", "runner", task.RunnerID, "name", spec.Name, "backend", kind, "error", err)
		release()
		a.reportFailure(ctx, task, fmt.Sprintf("the %s backend could not create runner %s: %v", kind, spec.Name, err), backend.Fault(err))
		return
	}
	now := a.now()
	resources := spec.Resources
	if (kind == store.BackendDocker || kind == store.BackendPodman) && spec.DockerMode == store.DockerDinD && spec.ResourcesSource == store.AllocationFromHost {
		// Match the per-container labels used after adoption. Keeping the
		// whole slot here doubled both quotas when a throttle was restored.
		resources, _ = resources.SplitWithDaemonShares(spec.DaemonShares())
	}
	handle = created.Handle
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
		resources:  resources,
		// The container was created with its full allocation, which is a
		// factor of 1 applied; recording it saves the next beat a request.
		appliedCPUFactor: new(float64(1)),
	}
	delete(a.orphans, handle)
	throttled := a.cpuFactor < 1
	a.mu.Unlock()

	a.log.Info("runner created", "runner", task.RunnerID, "name", spec.Name, "backend", kind, "handle", handle, "took", now.Sub(start))
	if throttled {
		// Existing runners still receive the pressure directive. The new
		// runner keeps its normal allocation for the bounded bootstrap grace.
		a.applyThrottle(cctx, false)
	}
	release()
	a.report(ctx, TaskResult{
		TaskID:             task.ID,
		Kind:               task.Kind,
		RunnerID:           task.RunnerID,
		OK:                 true,
		Handle:             handle,
		ImagePullDuration:  created.ImagePullDuration,
		StartupWait:        task.startupWait,
		DinDReadyDuration:  created.DinDReadyDuration,
		CreateDuration:     created.CreateDuration,
		ContainerStartedAt: &now,
		Digest:             created.Digest,
		State:              store.RunnerRegistering,
		CompletedAt:        now,
	})
}

func (a *Agent) handleStop(ctx context.Context, task Task, release func()) {
	b, handle, ok, err := a.resolve(ctx, task.RunnerID)
	if !ok {
		if err != nil {
			release()
			a.reportUnsearchable(ctx, task, err)
			return
		}
		// Nothing to stop is the outcome the controller wanted, not an error.
		a.log.Info("stop task for a runner with no workload on this host; reporting it removed", "runner", task.RunnerID)
		release()
		a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true, State: store.RunnerRemoved, CompletedAt: a.now()})
		return
	}

	timeout := task.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	a.markStopping(task.RunnerID)

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+StopMargin)
	defer cancel()

	if err := b.Stop(sctx, handle, timeout); err != nil && !errors.Is(err, backend.ErrNotFound) {
		a.log.Error("stopping runner failed", "runner", task.RunnerID, "handle", handle, "error", err)
		release()
		a.report(ctx, TaskResult{
			TaskID:      task.ID,
			Kind:        task.Kind,
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
	release()
	a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true, Handle: handle, CompletedAt: a.now()})
}

func (a *Agent) handleRemove(ctx context.Context, task Task, release func()) {
	b, handle, ok, err := a.resolve(ctx, task.RunnerID)

	// The same budget the reconciler gives a removal, whichever branch this
	// takes: a sidecar's DELETE is held open until the daemon has finished
	// it, and without a deadline of its own it would get only the client's
	// one-minute default.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RemoveTimeout)
	defer cancel()

	if !ok {
		if err != nil {
			release()
			a.reportUnsearchable(ctx, task, err)
			return
		}
		for _, kind := range a.opts.Backends.Kinds() {
			be, err := a.opts.Backends.Get(kind)
			if err == nil {
				err = removeRunnerWorkload(rctx, be, task.RunnerID, "")
			}
			if err != nil && errors.Is(err, backend.ErrRemovalInProgress) {
				release()
				a.reportRemoveFailure(ctx, task, kind, "", err)
				return
			}
			if err != nil {
				release()
				a.reportUnsearchable(ctx, task, err)
				return
			}
		}
		// A workload that is already gone is exactly what this task asked for.
		a.removalPending(task.RunnerID, nil, a.now())
		a.untrack(task.RunnerID)
		release()
		a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true, State: store.RunnerRemoved, CompletedAt: a.now()})
		return
	}

	if err := removeRunnerWorkload(rctx, b, task.RunnerID, handle); err != nil {
		release()
		a.reportRemoveFailure(ctx, task, b.Kind(), handle, err)
		return
	}

	a.removalPending(task.RunnerID, nil, a.now())
	a.untrack(task.RunnerID)
	a.log.Info("runner removed", "runner", task.RunnerID, "handle", handle)
	release()
	a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true, Handle: handle, State: store.RunnerRemoved, CompletedAt: a.now()})
}

// reportRemoveFailure answers a remove task whose removal did not complete.
//
// A removal the daemon still has under way is answered as pending rather than
// failed, for as long as removalSettle allows: the controller records nothing
// against the runner, and the removal is confirmed by whichever attempt next
// finds the workload gone -- the reconciler's, or this task's redelivery.
// Answered as a failure, every one of those attempts was counted against the
// runner and the Runners page said something was left behind while the daemon
// was finishing the job. A removal still under way past the bound is stuck,
// and is reported as the failure it has become.
func (a *Agent) reportRemoveFailure(ctx context.Context, task Task, kind store.BackendKind, handle backend.Handle, err error) {
	now := a.now()
	since, pending := a.removalPending(task.RunnerID, err, now)
	if pending {
		a.log.Info("the daemon is still removing this runner's workload; it will be confirmed once the daemon has finished",
			"runner", task.RunnerID, "handle", handle, "backend", kind, "removing_for", now.Sub(since), "error", err)
		a.report(ctx, TaskResult{
			TaskID:         task.ID,
			Kind:           task.Kind,
			RunnerID:       task.RunnerID,
			OK:             false,
			CleanupPending: true,
			Handle:         handle,
			Error:          fmt.Sprintf("the %s backend is still removing runner %s: %v", kind, task.RunnerID, err),
			CompletedAt:    now,
		})
		return
	}
	if !since.IsZero() {
		err = stuckRemoval(now.Sub(since), err)
	}
	a.log.Error("removing runner failed", "runner", task.RunnerID, "handle", handle, "error", err)
	which := task.RunnerID
	if handle != "" {
		which += " (" + string(handle) + ")"
	}
	a.report(ctx, TaskResult{
		TaskID:      task.ID,
		Kind:        task.Kind,
		RunnerID:    task.RunnerID,
		OK:          false,
		Handle:      handle,
		Error:       fmt.Sprintf("the %s backend could not remove runner %s: %v", kind, which, err),
		CompletedAt: now,
	})
}

// removal is one removal the daemon has been found carrying out.
type removal struct {
	// since is when it was first found under way, which removalSettle is
	// measured from.
	since time.Time
	// asked is when anything last found it still under way, which
	// removalMemory is measured from.
	asked time.Time
}

// removalPending reports whether a removal that ended with err should be
// waited for rather than reported, and since when the daemon has been found
// still removing this runner's workload. Only a removal still under way
// counts; any other outcome, success included, forgets it, so a removal that
// fails for another reason is reported at once and a later slow one gets a
// window of its own.
func (a *Agent) removalPending(runnerID string, err error, now time.Time) (since time.Time, pending bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !errors.Is(err, backend.ErrRemovalInProgress) {
		delete(a.removing, runnerID)
		return time.Time{}, false
	}
	r, ok := a.removing[runnerID]
	if !ok {
		for id, other := range a.removing {
			if now.Sub(other.asked) >= removalMemory {
				delete(a.removing, id)
			}
		}
		r.since = now
	}
	r.asked = now
	a.removing[runnerID] = r
	return r.since, now.Sub(r.since) < removalSettle
}

// StuckRemoval opens the report of a removal still under way past
// removalSettle. It is the agent's classification, made where the typed
// evidence is, and exported so the controller can give it its own advice
// without reading the daemon's words back out of the sentence: a removal whose
// every DELETE ran out of time never contains them.
const StuckRemoval = "the daemon on this host has been removing this runner's workload for"

// stuckRemoval says what a removal still under way past removalSettle has
// become, and what to do about it: the daemon is not going to finish it.
func stuckRemoval(waited time.Duration, err error) error {
	return fmt.Errorf("%s %s and has not finished; a removal stuck this long usually needs the daemon restarted: %w",
		StuckRemoval, waited.Round(time.Second), err)
}

func (a *Agent) runLogTask(ctx context.Context, task Task) {
	if task.Kind == TaskCancelLogs {
		a.logs.cancel(task.StreamID)
		a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true, CompletedAt: a.now()})
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
			Kind:        task.Kind,
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
			Kind:        task.Kind,
			RunnerID:    task.RunnerID,
			OK:          false,
			Handle:      handle,
			Error:       fmt.Sprintf("could not relay logs for runner %s: %v", task.RunnerID, err),
			CompletedAt: a.now(),
		})
		return
	}
	a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, RunnerID: task.RunnerID, OK: true, Handle: handle, CompletedAt: a.now()})
}

// report sends a task result. It uses a context shutdown does not cancel,
// because a result that never arrives leaves the controller waiting on a task
// it will redeliver forever.
func (a *Agent) report(ctx context.Context, res TaskResult) {
	if res.CompletedAt.IsZero() {
		res.CompletedAt = a.now()
	}
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
		Kind:        task.Kind,
		RunnerID:    task.RunnerID,
		OK:          false,
		Error:       fmt.Sprintf("could not tell whether runner %s is still on this host because its backend would not answer, so nothing was changed: %v", task.RunnerID, err),
		Fault:       store.FaultBackend,
		CompletedAt: a.now(),
	})
}

// reportFailure says a lifecycle task failed, and what category the failure
// belongs to. The kind is a parameter rather than something derived from msg
// because every caller knows it from the code path it took, and a classifier
// reading the sentence back would be guessing at what the caller already had.
func (a *Agent) reportFailure(ctx context.Context, task Task, msg string, fault store.FaultKind) {
	a.report(ctx, TaskResult{
		TaskID:      task.ID,
		Kind:        task.Kind,
		RunnerID:    task.RunnerID,
		OK:          false,
		Error:       msg,
		State:       store.RunnerFailed,
		Fault:       fault,
		CompletedAt: a.now(),
	})
}

// reportNotStarted gives a task back to the controller untouched. It carries
// no runner state: nothing happened to the runner, and a failed state here is
// what turned every upgrade into a column of "Runner stopped" failures.
func (a *Agent) reportNotStarted(ctx context.Context, task Task) {
	a.report(ctx, TaskResult{
		TaskID:      task.ID,
		Kind:        task.Kind,
		RunnerID:    task.RunnerID,
		OK:          false,
		NotStarted:  true,
		Error:       "agent shut down before this task started; it is safe to redeliver",
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
			// A sidecar shares its runner's id and would answer here first
			// often enough to matter, sending the stop to the daemon and
			// leaving the runner holding the job.
			if w.RunnerID == runnerID && !w.Sidecar {
				a.adopt(runnerID, k, w)
				return b, w.Handle, true, nil
			}
		}
	}
	return nil, "", false, errors.Join(errs...)
}

// resolveRetryDelays is how long a create waits between attempts to learn
// whether its runner already exists. A daemon that is restarting, or one that
// missed a call while busy, answers again within a few seconds; the schedule
// covers that and no more, because every second of it is spent holding the
// host's startup slot.
var resolveRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// resolveForCreate is resolve with a short retry. A create that could not ask
// the host once used to be left for the lease to redeliver twenty minutes
// later, by which time the controller's provision timeout had failed the
// runner -- so one missed answer from the daemon cost five minutes of a
// runner that was never going to start.
func (a *Agent) resolveForCreate(ctx context.Context, runnerID string) (backend.Backend, backend.Handle, bool, error) {
	for i := 0; ; i++ {
		b, h, ok, err := a.resolve(ctx, runnerID)
		if err == nil || i >= len(a.resolveRetry) {
			return b, h, ok, err
		}
		a.log.Warn("could not list this host's workloads; asking again before deciding about the create",
			"runner", runnerID, "attempt", i+1, "retry_in", a.resolveRetry[i], "error", err)
		if !sleepCtx(ctx, a.resolveRetry[i]) {
			return b, h, ok, err
		}
	}
}

// releaseUnknown stops tracking the runners the controller says it has no row
// for, which hands their workloads back to the reconciler's orphan path.
//
// It is the only route out of the tracked set that removal depends on, and it
// is driven by the controller rather than by anything the agent can decide for
// itself: the agent cannot tell "the controller deleted this runner" from "the
// agent has forgotten it", and only one of those should cost somebody a job.
func (a *Agent) releaseUnknown(runnerIDs []string) {
	if len(runnerIDs) == 0 {
		return
	}
	a.mu.Lock()
	released := make([]string, 0, len(runnerIDs))
	for _, id := range runnerIDs {
		if r, ok := a.runners[id]; ok {
			if r.terminal {
				// Retain the workload through local retention and confirm removal.
				continue
			}
			delete(a.runners, id)
			released = append(released, id)
		}
	}
	a.mu.Unlock()
	if len(released) > 0 {
		a.log.Info("the controller has no record of these runners; their workloads will be cleaned up",
			"runners", strings.Join(released, ","))
	}
}

// adoptExisting takes over every runner workload already on this host.
//
// It runs before the loops, so the reconciler's first pass sees a tracked set
// that matches the host rather than an empty one. A backend that cannot be
// listed is logged and skipped: the reconciler already refuses to conclude
// anything about a backend it could not list, and starting without adopting
// from it is the same position an agent is in a moment before its first
// successful list.
//
// Only runner workloads carrying a runner id are adopted. One without is not
// this controller's to manage under an identity it can name, and the
// reconciler's orphan path is still the right home for it. A sidecar is
// skipped although it does carry one: it is not the runner, and adopting it
// would point that runner's slot at the wrong container for the rest of the
// agent's life.
func (a *Agent) adoptExisting(ctx context.Context) {
	kinds := a.opts.Backends.Kinds()
	slices.Sort(kinds)
	adopted := 0
	for _, kind := range kinds {
		a.mu.Lock()
		a.inventoryPending[kind] = true
		a.mu.Unlock()
		b, err := a.opts.Backends.Get(kind)
		if err != nil {
			a.log.Warn("could not reach a backend to adopt what it is running", "backend", kind, "error", err)
			continue
		}
		workloads, err := b.List(ctx)
		if err != nil {
			a.log.Warn("could not list a backend's workloads to adopt them; runners it holds will be adopted "+
				"when the controller next asks about them", "backend", kind, "error", err)
			continue
		}
		for _, w := range workloads {
			if w.RunnerID == "" || w.Sidecar {
				continue
			}
			a.adopt(w.RunnerID, kind, w)
			adopted++
		}
		a.mu.Lock()
		delete(a.inventoryPending, kind)
		a.mu.Unlock()
	}
	if adopted > 0 {
		a.log.Info("adopted runners already on this host", "runners", adopted)
	}
}

// hostSize is how much machine this host has for runners, preferring what a
// container daemon says over what the agent can see of itself.
//
// The two differ, and which is right depends on where the runners end up. The
// agent's own view is clamped to its cgroup, deliberately: an agent in a
// two-core container should not claim the host's sixty-four. But a runner
// started through Docker or Podman is a sibling on the host, outside that
// cgroup entirely, so the agent's share is not the bound on what it can start
// -- and a containerised controller, which is the usual deployment, would
// otherwise report a fleet a fraction of its real size and place accordingly.
// A runner the process backend starts is a child of the agent and is held to
// that cgroup, so where no daemon answers, the agent's own view is the honest
// one and is what this falls back to.
func hostSize(infos []backend.Info, self machine.Facts) (cpus int, memoryMB int64) {
	// The largest machine any daemon reports, and the agent's own view only
	// where no daemon reports one. A daemon's figure is taken even when it
	// is smaller than the agent's: Docker Desktop's VM on a ten-core laptop
	// has four cores, the runners run inside it, and a CPU share sized from
	// the laptop is a quota the VM's daemon refuses.
	for _, info := range infos {
		if !info.Available {
			continue
		}
		if info.CPUs > cpus {
			cpus = info.CPUs
		}
		if info.MemoryMB > memoryMB {
			memoryMB = info.MemoryMB
		}
	}
	if cpus <= 0 {
		cpus = self.CPUs
	}
	if memoryMB <= 0 {
		memoryMB = self.MemoryMB
	}
	return cpus, memoryMB
}

// workDirSpace measures the filesystem the runners' scratch space lives on, in
// megabytes, and reports zeroes when it cannot be measured.
//
// Megabytes rather than bytes because that is the unit the host row and every
// other size on the Hosts page already use, and because a figure this coarse is
// what a placement decision needs -- nobody is choosing a host on the strength
// of one megabyte.
//
// Zero is "not measured" and never "full". A platform with no portable answer,
// or a work directory that has not been created yet, must not read as a host
// with no room, or upgrading would empty a fleet.
func (a *Agent) workDirSpace() (totalMB, freeMB int64) {
	total, avail, ok := diskSpace(a.opts.WorkDir)
	if !ok {
		return 0, 0
	}
	const mb = 1 << 20
	return total / mb, avail / mb
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
		resources:  w.Resources,
	}
	if a.runners[runnerID].createdAt.IsZero() {
		a.runners[runnerID].createdAt = now
	}
	delete(a.orphans, w.Handle)
}

func (a *Agent) claim(runnerID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inflight[runnerID] {
		return false
	}
	a.inflight[runnerID] = true
	return true
}

// taskRank orders the lifecycle tasks by how far along a runner's life they
// take it. A task that ranks above the one in flight for its runner supersedes
// it -- the controller has decided something later about that runner -- and is
// worth waiting for the claim rather than dropping. An equal rank is a
// redelivery of work already being done, which is exactly what the claim is
// there to drop.
func taskRank(kind TaskKind) int {
	switch kind {
	case TaskCreateRunner:
		return 1
	case TaskStopRunner:
		return 2
	case TaskRemoveRunner:
		return 3
	}
	return 0
}

// claimOrQueue takes the runner's claim for a dispatched task, or leaves the
// task waiting behind the one that holds it when it supersedes that one.
//
// Only a claim held by another dispatched task is queued behind: the
// reconciler takes one for the length of its own work and hands nothing on, so
// a task arriving then is better left to the controller's redelivery than held
// by a goroutine that will not start it.
func (a *Agent) claimOrQueue(task Task) (claimed, held bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.inflight[task.RunnerID] {
		a.inflight[task.RunnerID] = true
		a.running[task.RunnerID] = task.Kind
		return true, false
	}
	running, dispatched := a.running[task.RunnerID]
	if !dispatched || taskRank(task.Kind) <= taskRank(running) {
		return false, false
	}
	if waiting, ok := a.waiting[task.RunnerID]; ok {
		if taskRank(waiting.Kind) >= taskRank(task.Kind) {
			return false, false
		}
		// Two supersede the one in flight and only one may wait: the later of
		// the two does everything the earlier would, and the controller
		// redelivers the one dropped here if it still wants it.
		a.log.Debug("a later task replaced the one waiting for this runner",
			"runner", task.RunnerID, "dropped", waiting.ID, "kind", task.Kind)
	}
	a.waiting[task.RunnerID] = task
	return false, true
}

// release gives up a runner's claim, or hands it straight to the task waiting
// behind this one. The claim is never dropped in between: a third task
// arriving in that gap would otherwise run beside the one that was waiting.
func (a *Agent) release(runnerID string) {
	a.mu.Lock()
	next, ok := a.waiting[runnerID]
	if ok {
		delete(a.waiting, runnerID)
		a.running[runnerID] = next.Kind
	} else {
		delete(a.inflight, runnerID)
		delete(a.running, runnerID)
	}
	ctx := a.taskCtx
	a.mu.Unlock()
	if !ok {
		return
	}
	a.start(ctx, next)
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

// waitForTasks stops accepting tasks but keeps reporting while admitted work
// finishes. Otherwise a slow upgrade of a remote agent would make its live
// runners look lost to a controller that is still running.
func (a *Agent) waitForTasks(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		a.tasks.Wait()
		close(done)
	}()
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	defer cancel()
	ticker := time.NewTicker(a.heartbtI)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return true
		case <-waitCtx.Done():
			return false
		case <-ticker.C:
			if err := a.heartbeat(waitCtx); err != nil {
				a.log.Debug("heartbeat during shutdown failed", "error", err)
			}
		}
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
