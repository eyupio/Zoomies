// Package agent implements the half of Zoomies that runs runners: the agent
// daemon, its transport to the controller, and the reconciliation loop that
// keeps a host's workloads matching what the controller asked for.
//
// Agents only ever connect outbound. The controller never dials an agent, so a
// host behind NAT or a restrictive firewall needs no inbound rule -- which is
// the usual reason "multi-host" support turns into a VPN project.
package agent

import (
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// ProtocolVersion is bumped when the agent/controller wire format changes in a
// way an older peer cannot tolerate. The controller refuses a mismatched agent
// with a message telling the operator to upgrade it.
const ProtocolVersion = 1

// FeatureElasticCPU is advertised by an agent that can apply per-runner CPU
// targets. It lets a new controller coexist with older agents without
// claiming a live quota moved when that agent could not understand the order.
const FeatureElasticCPU = "elastic-cpu"

// FeatureToolCacheFill is advertised by an agent that understands
// TaskFillToolCache. An older agent refuses a kind it does not know, which is
// safe but would read on the controller as a fill that failed on every host.
const FeatureToolCacheFill = "tool-cache-fill"

// JoinRequest redeems a short-lived join token and enrols a new host.
type JoinRequest struct {
	Connection      string `json:"-"`
	ProtocolVersion int    `json:"protocol_version"`
	JoinToken       string `json:"join_token"`
	Name            string `json:"name"`
	Address         string `json:"address,omitempty"`
	Capacity        int    `json:"capacity"`
	OS              string `json:"os"`
	// Distro and OSVersion say which Linux this is. The controller cannot
	// place a pool that asks for Ubuntu 24.04 without them, and an agent too
	// old to send them is simply a host that makes no platform promise.
	Distro    string `json:"distro,omitempty"`
	OSVersion string `json:"os_version,omitempty"`
	Arch      string `json:"arch"`
	// CPUs and MemoryMB are how much machine this host is, which is not always
	// how much this agent may use. An agent in a container sees its cgroup's
	// share, but a Docker runner is a sibling on the host and outside that
	// cgroup, so the daemon's view of the machine is the one that decides what
	// can be placed here; the larger of the two is sent. A process-backend
	// runner is a child inside the cgroup, and there the two are the same.
	CPUs     int   `json:"cpus,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// DiskTotalMB and DiskFreeMB measure the filesystem holding the work
	// directory, which is where a runner's checkout and its caches land. Free
	// is what is available to a runner rather than what is unused: the two
	// differ by the reserve the filesystem keeps for root, and placing work
	// into space the job cannot write to is the failure that distinction
	// exists to prevent. Zero means the agent could not measure it, which is
	// not the same as a full disk.
	DiskTotalMB int64  `json:"disk_total_mb,omitempty"`
	DiskFreeMB  int64  `json:"disk_free_mb,omitempty"`
	Version     string `json:"version"`
	// Features is sent at join as well as on every heartbeat, so a host is
	// not shown as unable to lend CPU for the thirty seconds between the two.
	Features []string          `json:"features,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Backends []backend.Info    `json:"backends"`
	// PreviousToken is the agent token this host was issued the last time it
	// joined, sent when the credentials file still holds one. It is what lets
	// a rebuilt machine reclaim its own row: without it the controller refuses
	// to replace an existing host of the same name, because a join token on its
	// own must not be enough to take over somebody else's machine.
	PreviousToken string `json:"previous_token,omitempty"`
	// PreviousHostID is the row this host was last enrolled as, sent from the
	// same credentials file as PreviousToken.
	//
	// The token proves ownership; this says what is owned. Without it the
	// controller can only find the old row by name, and the name is not
	// stable: it is derived from what the machine is, so an agent that has
	// learnt to measure its own CPUs and memory computes a different one from
	// an agent that had not. Upgrading such a host therefore enrolled it a
	// second time -- a new row with the correct figures, beside the old row
	// still being kept alive by the process that had not been restarted yet.
	//
	// Empty from an agent too old to send it, and the name lookup still stands
	// behind it for exactly that case.
	PreviousHostID string `json:"previous_host_id,omitempty"`
}

// JoinResponse hands back the host's identity and its long-lived agent token.
// The token is shown exactly once; the controller stores only its hash.
type JoinResponse struct {
	HostID     string `json:"host_id"`
	AgentToken string `json:"agent_token"`
	// ControllerVersion lets the agent warn about a version skew.
	ControllerVersion string `json:"controller_version"`
	HeartbeatInterval string `json:"heartbeat_interval"`
}

// HeartbeatRequest is sent on every interval. It carries the agent's own view
// of its runners so the controller can detect drift without polling.
type HeartbeatRequest struct {
	Usage           *store.HostUsage `json:"usage,omitempty"`
	ProtocolVersion int              `json:"protocol_version"`
	// Capacity is the agent's configured value, sent for the log and for
	// older controllers. The controller does not write it: capacity is set
	// at join and belongs to the operator after that.
	Capacity int `json:"capacity"`
	// CPUs and MemoryMB are facts about the machine rather than the operator's
	// choice, so unlike Capacity the controller does record them: a host
	// resized in place must stop describing itself as the machine it used to be.
	CPUs     int   `json:"cpus,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// DiskTotalMB and DiskFreeMB are the work directory's filesystem, sent on
	// every beat because free space is the one of these that moves on its own.
	// The controller writes them under a tolerance rather than on every
	// change, or a fleet would take one row write per host per beat for a
	// figure that is never exactly the same twice.
	DiskTotalMB int64          `json:"disk_total_mb,omitempty"`
	DiskFreeMB  int64          `json:"disk_free_mb,omitempty"`
	Version     string         `json:"version"`
	Features    []string       `json:"features,omitempty"`
	Backends    []backend.Info `json:"backends,omitempty"`
	Runners     []RunnerReport `json:"runners,omitempty"`
	// Runtime is the container-runtime cooldown this agent is in, absent
	// when it is in none. It used to be a log line on the host and nothing
	// else, so a fleet whose daemon kept falling over saw only runners that
	// were slow to appear. Absent from an agent too old to send it, which the
	// controller reads the same way as a healthy runtime -- the only thing it
	// ever knew about such a host -- and an older controller ignores it.
	Runtime *RuntimeReport `json:"runtime,omitempty"`
}

// RuntimeReport is the agent's runtime cooldown as it stands at this beat.
type RuntimeReport struct {
	// Failures is how many runtime failures in a row, capped at five.
	Failures int `json:"failures"`
	// Kind is RuntimeUnavailable or RuntimeTimeout.
	Kind string `json:"kind"`
	// Error is the last failure as the backend worded it, which carries the
	// socket path or the start hint the operator needs.
	Error string `json:"error,omitempty"`
	// RetryIn is how long until the one recovery attempt, rather than when:
	// the controller turns it into a time on its own clock, so a host whose
	// clock has drifted cannot tell the Hosts page something untrue.
	RetryIn time.Duration `json:"retry_in"`
}

// The two kinds of runtime failure that open the cooldown.
const (
	// RuntimeUnavailable is a daemon the agent could not reach at all.
	RuntimeUnavailable = "unavailable"
	// RuntimeTimeout is a daemon that was reached and did not answer.
	RuntimeTimeout = "timeout"
)

// HeartbeatResponse tells the agent whether the controller still recognises it.
type HeartbeatResponse struct {
	// MutationsPaused preserves observed workloads during fenced recovery.
	MutationsPaused bool `json:"mutations_paused,omitempty"`
	OK              bool `json:"ok"`
	// Cordoned mirrors the host's cordon flag so the agent can stop asking for
	// work without waiting for the next task poll.
	Cordoned bool `json:"cordoned"`
	// ControllerVersion is echoed for skew detection.
	ControllerVersion string `json:"controller_version"`
	// Incompatible says this agent speaks a protocol the controller does not,
	// with IncompatibleReason as the sentence to log. It is separate from
	// Cordoned because they are separate facts -- one is an operator's
	// decision, the other this controller's conclusion about the binary --
	// even though the controller sets Cordoned too, so an agent that predates
	// these fields still stops asking for work.
	Incompatible       bool   `json:"incompatible,omitempty"`
	IncompatibleReason string `json:"incompatible_reason,omitempty"`
	// ProtocolVersion is what the controller speaks, so the agent can say
	// which way the gap runs rather than only that there is one.
	ProtocolVersion int `json:"protocol_version,omitempty"`
	// ResyncRequested asks the agent to send a full runner report next time,
	// which the controller sets after its own restart.
	ResyncRequested bool `json:"resync_requested"`
	// Throttle is the controller's current throttle for this host: how far
	// it has stepped the CPU quota of every runner here down, after the
	// host's own measurements said it was overwhelmed. The agent applies the
	// factor to each live container it can, and applies it again when the
	// factor moves. It is sent on every beat rather than only when it changes,
	// so an agent that restarted or missed the beat the throttle lifted on
	// still restores its runners. Nil from a controller too old to send it,
	// which an agent reads as no throttle at all.
	Throttle *ThrottleDirective `json:"throttle,omitempty"`
	// ElasticCPU is the per-runner CPU target set. Missing means restore every
	// previous boost to its base, so a controller downgrade cannot strand a
	// job at a stale quota. Older agents ignore this additive field.
	ElasticCPU []ElasticCPUDirective `json:"elastic_cpu,omitempty"`
	// UnknownRunners names the runners this host reported that the controller
	// has no live row for. They are the ones whose workloads may be removed.
	//
	// An agent adopts what it finds running when it starts, so that a restart
	// does not destroy the jobs on its host. That adoption is also what stops
	// it recognising genuine litter -- a workload whose runner the controller
	// deleted while the agent was down -- so the controller answers with the
	// ones it does not know, and only those are reaped. Additive: an older
	// controller sends nothing here, and an agent that receives nothing simply
	// keeps what it adopted, which is the safe direction.
	UnknownRunners []string `json:"unknown_runners,omitempty"`
}

// ElasticCPUDirective lends otherwise idle CPU to one runner. The factor is
// relative to its creation allocation, which also works for DinD: an
// automatic slot is split into two containers and both halves use the factor.
type ElasticCPUDirective struct {
	RunnerID   string  `json:"runner_id"`
	CPUFactor  float64 `json:"cpu_factor"`
	BaseCPUs   float64 `json:"base_cpus"`
	TargetCPUs float64 `json:"target_cpus"`
	Reason     string  `json:"reason,omitempty"`
}

// ThrottleDirective is what a throttled host's agent is told to do about it.
type ThrottleDirective struct {
	// Level is the rung the host is on, for the log line; zero is none.
	Level int `json:"level"`
	// CPUFactor is the share of its allocated CPU each runner is to be left:
	// 1 restores every runner to what it was created with.
	CPUFactor float64 `json:"cpu_factor"`
}

// RunnerReport is the agent's observation of one runner. The controller merges
// it into the runner's authoritative state.
type RunnerReport struct {
	// HostRemoved is positive confirmation that Remove completed, not merely exit.
	HostRemoved  bool              `json:"host_removed,omitempty"`
	CleanupError string            `json:"cleanup_error,omitempty"`
	RunnerID     string            `json:"runner_id"`
	State        store.RunnerState `json:"state"`
	Handle       backend.Handle    `json:"handle,omitempty"`
	Phase        backend.Phase     `json:"phase,omitempty"`
	ExitCode     int               `json:"exit_code,omitempty"`
	Message      string            `json:"message,omitempty"`
	// Fault categorises a failure, decided here rather than by the controller
	// because this is the side holding the evidence: the exit code, the
	// daemon's own reply, the entrypoint's reserved codes. By the time a
	// message has reached the controller it has been reworded twice and the
	// only thing left to classify from is prose. An agent older than this
	// field sends none, which the controller reads as unclassified -- honest,
	// and exactly what it knew before.
	Fault store.FaultKind `json:"fault,omitempty"`
	Stats backend.Stats   `json:"stats,omitempty"`
	// GitHubRunnerID is filled in once the runner has registered.
	GitHubRunnerID int64     `json:"github_runner_id,omitempty"`
	ObservedAt     time.Time `json:"observed_at"`
}

// TaskKind names one lifecycle command.
type TaskKind string

const (
	// TaskCreateRunner asks the agent to materialise a runner.
	TaskCreateRunner TaskKind = "create_runner"
	// TaskStopRunner asks the agent to let the runner finish its current job
	// and then exit. This is what a drain becomes on the host.
	TaskStopRunner TaskKind = "stop_runner"
	// TaskRemoveRunner tears the workload down immediately.
	TaskRemoveRunner TaskKind = "remove_runner"
	// TaskStreamLogs opens an outbound log relay for a UI viewer.
	TaskStreamLogs TaskKind = "stream_logs"
	// TaskCancelLogs closes one.
	TaskCancelLogs   TaskKind = "cancel_logs"
	TaskPrewarmImage TaskKind = "prewarm_image"
	// TaskFillToolCache puts the toolchain versions a pool's jobs ask for in
	// its kept tool cache on this host, ahead of the jobs.
	TaskFillToolCache TaskKind = "fill_tool_cache"
)

// Task is one unit of work handed to an agent. Tasks are idempotent: the
// controller may redeliver one after a restart, and applying it twice must
// leave the host in the same place.
type Task struct {
	startupWait *time.Duration
	ID          string   `json:"id"`
	Kind        TaskKind `json:"kind"`
	// RunnerID is the runner this task concerns.
	RunnerID string `json:"runner_id,omitempty"`
	// Spec is set for TaskCreateRunner and carries the credentials the runner
	// needs. It is the only place a JIT config crosses the wire, which is why
	// the agent transport requires TLS in any non-loopback deployment.
	Spec *backend.Spec `json:"spec,omitempty"`
	// Backend selects which registered backend handles this task.
	Backend    store.BackendKind `json:"backend,omitempty"`
	PoolID     string            `json:"pool_id,omitempty"`
	Image      string            `json:"image,omitempty"`
	PullPolicy store.PullPolicy  `json:"pull_policy,omitempty"`
	// StopTimeout bounds a graceful stop.
	StopTimeout time.Duration `json:"stop_timeout,omitempty"`
	// StreamID identifies a log relay for TaskStreamLogs and TaskCancelLogs.
	StreamID string `json:"stream_id,omitempty"`
	// LogOptions configures a log relay.
	LogOptions *backend.LogOptions `json:"log_options,omitempty"`
	// Tools is what a TaskFillToolCache installs; its Spec names the pool.
	Tools    []backend.ToolRequest `json:"tools,omitempty"`
	IssuedAt time.Time             `json:"issued_at"`
	// Attempt counts deliveries of this task, from 1. A create that cannot
	// establish whether its runner already exists reads it to tell the first
	// delivery -- when no workload of that runner's can exist yet, so failing
	// promptly costs nothing -- from a redelivery, where a workload may be
	// mid-job and only silence is safe. Zero is a controller from before the
	// field, which is treated like a redelivery.
	Attempt int `json:"attempt,omitempty"`
}

// TaskResult reports the outcome of a task back to the controller.
type TaskResult struct {
	StartupWait       *time.Duration `json:"startup_wait,omitempty"`
	DinDReadyDuration *time.Duration `json:"dind_ready_duration,omitempty"`
	// PrewarmCached distinguishes preparation reused on the host from work sent
	// to the runtime. The pointer keeps results from older agents identifiable
	// rather than silently calling every one a refresh.
	PrewarmCached   *bool          `json:"prewarm_cached,omitempty"`
	PrewarmDuration *time.Duration `json:"prewarm_duration,omitempty"`
	// ToolFills is what a TaskFillToolCache did with each request, including
	// when the fill as a whole failed part way.
	ToolFills []backend.ToolFill `json:"tool_fills,omitempty"`
	TaskID    string             `json:"task_id"`
	// Kind is the kind of the task this answers. The controller uses it to
	// tell a lifecycle task that failed -- which leaves the runner unusable --
	// from a log relay that could not be opened, which leaves it exactly as it
	// was. An agent from before this field is read from the controller's own
	// record of the task instead.
	Kind     TaskKind       `json:"kind,omitempty"`
	RunnerID string         `json:"runner_id,omitempty"`
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Handle   backend.Handle `json:"handle,omitempty"`
	// ImagePullDuration is nil when the backend cannot distinguish pulling
	// from creation. ContainerStartedAt is the end of workload creation.
	ImagePullDuration  *time.Duration `json:"image_pull_duration,omitempty"`
	CreateDuration     time.Duration  `json:"create_duration,omitempty"`
	ContainerStartedAt *time.Time     `json:"container_started_at,omitempty"`
	Digest             string         `json:"digest,omitempty"`
	// State is the runner state the agent believes the runner reached.
	State store.RunnerState `json:"state,omitempty"`
	// Fault categorises a lifecycle task that failed, for the same reason
	// RunnerReport carries one: the backend's answer is here and nowhere else.
	Fault store.FaultKind `json:"fault,omitempty"`
	// NotStarted says the agent gave the task back untouched -- it was
	// shutting down before the task began -- so the controller should offer
	// it again rather than fail the runner. Without it a routine upgrade
	// failed every create still waiting for a startup slot, with a message
	// that said it was safe to redeliver while the runner was marked failed.
	NotStarted bool `json:"not_started,omitempty"`
	// CleanupPending says a remove did not complete because the daemon is
	// still carrying out a removal of the workload -- one it will finish
	// without being asked again -- so the controller should record nothing
	// against the runner. It is only ever set with OK false. Without it every
	// attempt that found a slow removal still going was counted as a failed
	// cleanup, and the runner was reported as something left behind while
	// the daemon was deleting it. A controller from before the field reads
	// the result as the failure it always did.
	CleanupPending bool      `json:"cleanup_pending,omitempty"`
	CompletedAt    time.Time `json:"completed_at"`
}

// TaskBatch is the response to a task poll.
type TaskBatch struct {
	Tasks []Task `json:"tasks"`
	// Backoff asks the agent to wait before polling again, used when the
	// controller wants to shed load.
	Backoff time.Duration `json:"backoff,omitempty"`
}

// LogChunk is one frame of a relayed log stream.
type LogChunk struct {
	StreamID string `json:"stream_id"`
	Data     string `json:"data"`
	// EOF marks the final frame; Error explains an abnormal end.
	EOF   bool   `json:"eof,omitempty"`
	Error string `json:"error,omitempty"`
}

// Endpoints are the controller paths the agent uses. They live here so that the
// agent client and the controller's router cannot drift apart.
const (
	PathJoin      = "/api/v1/agent/join"
	PathHeartbeat = "/api/v1/agent/heartbeat"
	PathTasks     = "/api/v1/agent/tasks"
	PathResults   = "/api/v1/agent/results"
	PathReport    = "/api/v1/agent/report"
	PathLogs      = "/api/v1/agent/logs"
)

// DefaultPollWait is how long a task poll blocks before returning empty. It is
// short enough to keep proxies from timing the connection out and long enough
// that an idle agent makes very few requests.
const DefaultPollWait = 25 * time.Second

// DefaultStopTimeout is how long a graceful stop waits for a runner to finish
// its current job before the workload is killed.
const DefaultStopTimeout = 5 * time.Minute

// MaxRunnersPerReport is the most runners one heartbeat or runner report may
// carry. It lives here so both halves of the protocol agree on it.
//
// A host runs as many runners as it has slots, which is tens and never
// thousands, so the number is far above anything a working agent sends. What
// it bounds is the other case: every entry in a report is a store read and
// possibly a write behind the single writer, so an unbounded array is a way
// for one host to hold the writer for every other host.
const MaxRunnersPerReport = 1000
