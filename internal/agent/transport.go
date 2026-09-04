package agent

import (
	"context"
	"io"
	"time"
)

// Transport is how an agent reaches its controller.
//
// There are two implementations. The HTTP one is what a standalone agent uses:
// it long-polls for tasks and POSTs results, always outbound, so a host behind
// NAT needs no inbound rule. The direct one is used by the agent embedded in
// the controller process and skips the network entirely, which is what lets the
// single-VM case be exactly one process.
//
// Every method must respect context cancellation; the agent cancels in flight
// when it is shutting down.
type Transport interface {
	// Join redeems a join token and returns the host's identity and its
	// long-lived agent token. It is called once, the first time an agent runs
	// on a host.
	Join(ctx context.Context, req JoinRequest) (*JoinResponse, error)

	// Heartbeat reports liveness, capacity and the agent's own view of its
	// runners.
	Heartbeat(ctx context.Context, req HeartbeatRequest) (*HeartbeatResponse, error)

	// PollTasks blocks for up to wait, returning as soon as there is work. An
	// empty batch after the full wait is the normal idle case, not an error.
	PollTasks(ctx context.Context, wait time.Duration) (*TaskBatch, error)

	// ReportResult reports the outcome of one task.
	ReportResult(ctx context.Context, res TaskResult) error

	// ReportRunners pushes runner observations outside the heartbeat cycle,
	// so a state change is visible in the UI immediately rather than up to one
	// heartbeat later.
	ReportRunners(ctx context.Context, reports []RunnerReport) error

	// OpenLogStream returns a writer for one relayed log stream. The agent
	// copies a runner's output into it; the controller fans it out to whoever
	// is watching in the browser. Closing the writer ends the stream.
	OpenLogStream(ctx context.Context, streamID string) (io.WriteCloser, error)

	// SetCredentials installs the identity returned by Join, or restored from
	// the agent's config, for use on subsequent calls.
	SetCredentials(hostID, agentToken string)

	// Describe returns a short string naming where this transport points, for
	// log lines and the agent's startup banner.
	Describe() string
}

// Credentials are what an agent persists between runs so that a restart does
// not need a new join token.
type Credentials struct {
	HostID     string `json:"host_id" yaml:"host_id"`
	AgentToken string `json:"agent_token" yaml:"agent_token"`
	Controller string `json:"controller_url" yaml:"controller_url"`
}

// Valid reports whether the credentials are complete enough to use.
func (c Credentials) Valid() bool { return c.HostID != "" && c.AgentToken != "" }
