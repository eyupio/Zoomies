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

// JoinRequest redeems a short-lived join token and enrols a new host.
type JoinRequest struct {
	ProtocolVersion int               `json:"protocol_version"`
	JoinToken       string            `json:"join_token"`
	Name            string            `json:"name"`
	Address         string            `json:"address,omitempty"`
	Capacity        int               `json:"capacity"`
	OS              string            `json:"os"`
	Arch            string            `json:"arch"`
	Version         string            `json:"version"`
	Labels          map[string]string `json:"labels,omitempty"`
	Backends        []backend.Info    `json:"backends"`
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
	ProtocolVersion int `json:"protocol_version"`
	// Capacity is the agent's configured value, sent for the log and for
	// older controllers. The controller does not write it: capacity is set
	// at join and belongs to the operator after that.
	Capacity int            `json:"capacity"`
	Version  string         `json:"version"`
	Backends []backend.Info `json:"backends,omitempty"`
	Runners  []RunnerReport `json:"runners,omitempty"`
}

// HeartbeatResponse tells the agent whether the controller still recognises it.
type HeartbeatResponse struct {
	OK bool `json:"ok"`
	// Cordoned mirrors the host's cordon flag so the agent can stop asking for
	// work without waiting for the next task poll.
	Cordoned bool `json:"cordoned"`
	// ControllerVersion is echoed for skew detection.
	ControllerVersion string `json:"controller_version"`
	// ResyncRequested asks the agent to send a full runner report next time,
	// which the controller sets after its own restart.
	ResyncRequested bool `json:"resync_requested"`
}

// RunnerReport is the agent's observation of one runner. The controller merges
// it into the runner's authoritative state.
type RunnerReport struct {
	RunnerID string            `json:"runner_id"`
	State    store.RunnerState `json:"state"`
	Handle   backend.Handle    `json:"handle,omitempty"`
	Phase    backend.Phase     `json:"phase,omitempty"`
	ExitCode int               `json:"exit_code,omitempty"`
	Message  string            `json:"message,omitempty"`
	Stats    backend.Stats     `json:"stats,omitempty"`
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
)

// Task is one unit of work handed to an agent. Tasks are idempotent: the
// controller may redeliver one after a restart, and applying it twice must
// leave the host in the same place.
type Task struct {
	ID   string   `json:"id"`
	Kind TaskKind `json:"kind"`
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
	IssuedAt   time.Time           `json:"issued_at"`
}

// TaskResult reports the outcome of a task back to the controller.
type TaskResult struct {
	TaskID string `json:"task_id"`
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
	Digest   string         `json:"digest,omitempty"`
	// State is the runner state the agent believes the runner reached.
	State       store.RunnerState `json:"state,omitempty"`
	CompletedAt time.Time         `json:"completed_at"`
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
