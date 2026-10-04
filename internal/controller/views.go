package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// The views are the resources as the API documents them: a host with its
// health worked out, a runner with its pool and host named, a pool with its
// counts, a job with its waits measured.
//
// They live here rather than in internal/api because two transports render
// them. The REST handlers do, and so does the event stream -- and the stream
// is fed from this package, at the moment a row changes. When the two rendered
// different shapes, a `host.updated` frame carried a store row with no
// `healthy` field, so the one event that exists to say "this agent has gone
// quiet" repainted the host as healthy. One renderer, used by both, is what
// stops the cache the UI keeps from being wrong the moment an event lands.
// controller.Stats and controller.Problem already worked this way; these
// follow them.

// ---------------------------------------------------------------------------
// Hosts
// ---------------------------------------------------------------------------

// BackendInfoView describes one backend a host offers.
type BackendInfoView struct {
	Kind      store.BackendKind `json:"kind"`
	Available bool              `json:"available"`
	Version   string            `json:"version,omitempty"`
	Rootless  bool              `json:"rootless"`
	Endpoint  string            `json:"endpoint,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	DinD      bool              `json:"supports_dind"`
	// Limits is what the daemon said it can enforce, which is what the
	// controller defaults a runner's limits from and what
	// host.limits_unenforceable reasons about; the API carries it so a
	// reader of the problem can see the fact behind it.
	Limits store.LimitSupport `json:"limits"`
	// SharedFolder is what is wrong with the host's shared folder, as the
	// agent said; see host.shared_folder_unmounted.
	SharedFolder string `json:"shared_folder,omitempty"`
}

// HostView is one agent host and the room it has left.
type HostView struct {
	Doctor          *hosttune.Report `json:"doctor,omitempty"`
	Usage           *store.HostUsage `json:"usage,omitempty"`
	UsageFresh      bool             `json:"usage_fresh"`
	AdmissionReason string           `json:"admission_reason,omitempty"`
	// Throttle is the rung the controller has stepped this host down to after
	// sustained pressure, absent when it is on none, and ThrottleReason is the
	// same thing as the sentence the host card shows: what it took, why, what
	// it is doing to the jobs already running, and how it ends. Both are the
	// controller's alone; a heartbeat carries the measurements and never the
	// decision.
	Throttle       *store.HostThrottle `json:"throttle,omitempty"`
	ThrottleReason string              `json:"throttle_reason"`
	// RuntimeRecovering is the container-runtime cooldown the agent last
	// reported, absent when there is none, and RuntimeReason the card's
	// sentence for it. The times are carried rather than written into the
	// sentence, so the card can count down to the retry and say how old the
	// report is against the viewer's own clock.
	RuntimeRecovering *store.RuntimeIncident `json:"runtime_recovering,omitempty"`
	RuntimeReason     string                 `json:"runtime_reason,omitempty"`
	// ImagePullFailed is the last start or prewarm here that could not make
	// its pool's image ready, naming the registry, absent when there is none.
	ImagePullFailed *store.ImagePullIncident `json:"image_pull_failed,omitempty"`
	// EffectiveCapacity is the slots the host takes right now: Capacity
	// stepped down by the throttle, and Capacity itself when there is none.
	// Free is measured against it, so a throttled host's card does not
	// promise slots the next pass will refuse.
	EffectiveCapacity int `json:"effective_capacity"`
	// UnlimitedRunners is how many of the live runners here were created
	// with no CPU quota -- a pool with none and defaults off, a process pool,
	// a daemon that cannot apply one, or a row from before allocations were
	// recorded. They are the runners a CPU hold can mean something about.
	UnlimitedRunners int               `json:"unlimited_runners,omitempty"`
	Connection       string            `json:"connection"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Address          string            `json:"address,omitempty"`
	Embedded         bool              `json:"embedded"`
	Capacity         int               `json:"capacity"`
	ActiveRunners    int               `json:"active_runners"`
	Free             int               `json:"free"`
	Backends         []string          `json:"backends"`
	BackendInfo      []BackendInfoView `json:"backend_info"`
	Labels           map[string]string `json:"labels"`
	OS               string            `json:"os,omitempty"`
	Distro           string            `json:"distro,omitempty"`
	OSVersion        string            `json:"os_version,omitempty"`
	Arch             string            `json:"arch,omitempty"`
	// CPUs and MemoryMB are how much machine this host is, as its agent
	// reported it: the daemon's view of the machine where that is larger than
	// the agent's own share, because a container runner runs beside the agent
	// rather than inside its cgroup.
	CPUs     int   `json:"cpus,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// DiskTotalMB and DiskFreeMB are the filesystem holding the agent's work
	// directory, which is where a runner's checkout and its caches land -- so
	// it is the disk that decides whether a job has anywhere to go, and the
	// answer to "the host has slots free, why did nothing start?".
	//
	// Free is what a runner may write to rather than what is unused; the two
	// differ by the reserve the filesystem keeps for root, and a runner is not
	// root. Zero on both is "not measured" -- an agent too old to report it,
	// or a platform with no portable way to ask -- and is not a full disk.
	DiskTotalMB int64 `json:"disk_total_mb,omitempty"`
	DiskFreeMB  int64 `json:"disk_free_mb,omitempty"`
	// ReserveCPUs, ReserveMemoryMB and ReserveDiskMB are what the operator has
	// held back from placement, for the machine's own sake rather than for any
	// pool's. They are the operator's alone: a heartbeat never writes them,
	// exactly as it never writes capacity.
	ReserveCPUs     int   `json:"reserve_cpus,omitempty"`
	ReserveMemoryMB int64 `json:"reserve_memory_mb,omitempty"`
	ReserveDiskMB   int64 `json:"reserve_disk_mb,omitempty"`
	// AllocatableCPUs, AllocatableMemoryMB and AllocatableDiskMB are the
	// machine less its reserve: what the scheduler may actually place onto,
	// which is the figure a "how full is this host" question is asked against.
	// The floors under the reserve -- half a core or 5% of the CPUs, 512 MB
	// of memory and 2 GB of disk -- are applied here too, so what is shown is
	// what is used. The CPU floor is what keeps the daemon, the agent and the
	// kernel a core the runners' quotas can never take.
	AllocatableCPUs     float64 `json:"allocatable_cpus,omitempty"`
	AllocatableMemoryMB int64   `json:"allocatable_memory_mb,omitempty"`
	AllocatableDiskMB   int64   `json:"allocatable_disk_mb,omitempty"`
	// ReservedCPUs and ReservedMemoryMB are what the runners already on this
	// host have promised away, as of the last scheduling pass and from the
	// snapshot that pass decided on. Disk is deliberately absent: free disk is
	// a measurement of the filesystem as it is now, so what those runners have
	// written is in DiskFreeMB already, and adding their reservations would
	// charge the same bytes twice.
	//
	// ReservedKnown is false until a pass has run, because zero would read as
	// an idle machine rather than as an unanswered question.
	ReservedCPUs     float64 `json:"reserved_cpus,omitempty"`
	ReservedMemoryMB int64   `json:"reserved_memory_mb,omitempty"`
	ReservedKnown    bool    `json:"reserved_known"`
	// ResourcesKnown is whether this host has reported what machine it is at
	// all. An agent too old to say is placed by slots alone, which is what
	// keeps an upgrade from emptying a fleet -- and the page has to say so,
	// because a host showing no figures looks broken rather than old.
	ResourcesKnown bool `json:"resources_known"`
	// Platform is what this host is in the terms a pool asks in, and
	// PlatformLabel is the same thing as a sentence: "Ubuntu 24.04, arm64".
	Platform      store.Platform `json:"platform"`
	PlatformLabel string         `json:"platform_label,omitempty"`
	// CanonicalName is the name this machine would be given today. It is shown
	// beside a host called something that says nothing, so an operator can see
	// what renaming it would buy them.
	CanonicalName  string `json:"canonical_name,omitempty"`
	Version        string `json:"version,omitempty"`
	VersionChannel string `json:"version_channel,omitempty"`
	// Features is what the agent advertises it can do, and ElasticCPU the one
	// answer a pool cares about: whether a runner placed here can be lent CPU
	// at all. Rendered as a flag rather than left for the browser to find in
	// the list, so the card and the pool wizard cannot disagree about which
	// feature name means what.
	Features   []string `json:"features"`
	ElasticCPU bool     `json:"elastic_cpu"`
	Cordoned   bool     `json:"cordoned"`
	// ProtocolVersion is the agent protocol this host reported, and
	// Incompatible whether this controller can work with it. An incompatible
	// host is excluded from placement exactly as a cordoned one is, so the
	// Hosts page has to say which of the two it is: nothing is broken, the
	// host is up and heartbeating, and it has quietly stopped taking work.
	ProtocolVersion    int    `json:"protocol_version,omitempty"`
	Incompatible       bool   `json:"incompatible"`
	IncompatibleReason string `json:"incompatible_reason,omitempty"`
	// VersionSkew is how this host's release stands to the controller's:
	// "behind", "ahead", "differs", or absent when they match. It is computed
	// here rather than in the browser because the controller is the side that
	// knows its own version, and because the agent's own warning comes from
	// the same comparison -- the two used to disagree for two builds of one
	// tag, and a badge that contradicts a log line is worse than neither.
	VersionSkew    string    `json:"version_skew,omitempty"`
	UpgradeCommand string    `json:"upgrade_command,omitempty"`
	UpgradeVersion string    `json:"upgrade_version,omitempty"`
	UpgradeNote    string    `json:"upgrade_note,omitempty"`
	Healthy        bool      `json:"healthy"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	CreatedAt      time.Time `json:"created_at"`
}

// HostView renders a host as the API returns it.
//
// backend_info is the agent's own probe, which includes the backends it could
// not use and the sentence explaining why: that sentence is the whole answer to
// "this host is connected, so why is nothing running on it?". A host that
// joined an older controller has no probe stored, so its available kinds are
// rendered as the bare list they are, and nothing is invented about the
// backends it never reported on.
func (c *Controller) HostView(h *store.Host) HostView {
	out := HostView{
		Doctor:             h.Doctor.Report,
		ID:                 h.ID,
		Name:               h.Name,
		Address:            h.Address,
		Embedded:           h.Embedded,
		Connection:         h.Connection,
		Capacity:           h.Capacity,
		ActiveRunners:      h.ActiveRunners,
		Free:               h.Free(),
		EffectiveCapacity:  h.EffectiveCapacity(),
		UnlimitedRunners:   h.UnlimitedRunners,
		ThrottleReason:     scheduler.ThrottleReason(h),
		Backends:           emptySlice(h.Backends),
		Labels:             emptyMap(h.Labels),
		OS:                 h.OS,
		Distro:             h.Distro,
		OSVersion:          h.OSVersion,
		Arch:               h.Arch,
		CPUs:               h.CPUs,
		MemoryMB:           h.MemoryMB,
		UsageFresh:         h.Usage.Fresh(c.Now()),
		AdmissionReason:    scheduler.HostAdmissionReason(h, c.Now()),
		DiskTotalMB:        h.DiskTotalMB,
		DiskFreeMB:         h.DiskFreeMB,
		ReserveCPUs:        h.ReserveCPUs,
		ReserveMemoryMB:    h.ReserveMemoryMB,
		ReserveDiskMB:      h.ReserveDiskMB,
		Platform:           h.Platform(),
		PlatformLabel:      h.Platform().Describe(),
		CanonicalName:      h.CanonicalName(),
		Version:            h.Version,
		VersionChannel:     version.Channel(h.Version),
		Features:           emptySlice(h.Features),
		ElasticCPU:         h.Supports(agent.FeatureElasticCPU),
		Cordoned:           h.Cordoned,
		ProtocolVersion:    h.ProtocolVersion,
		Incompatible:       h.Incompatible,
		IncompatibleReason: incompatibleReason(h),
		VersionSkew:        string(version.CompareBuilds(h.Version, version.Version)),
		Healthy:            h.Healthy(c.Now()),
		LastHeartbeat:      h.LastHeartbeat,
		CreatedAt:          h.CreatedAt,
	}
	out.UpgradeCommand, out.UpgradeVersion, out.UpgradeNote = hostUpgrade(h, version.Version)
	if !h.Usage.SampledAt.IsZero() {
		usage := h.Usage
		out.Usage = &usage
	}
	if h.Throttle.Active() {
		throttle := h.Throttle
		out.Throttle = &throttle
	}
	if inc := h.Incidents.Runtime; inc != nil {
		runtime := *inc
		out.RuntimeRecovering = &runtime
		out.RuntimeReason = runtimeReason(inc)
	}
	if inc := h.Incidents.ImagePull; inc != nil {
		pull := *inc
		out.ImagePullFailed = &pull
	}
	alloc := h.Allocatable()
	out.AllocatableCPUs = alloc.CPUs
	out.AllocatableMemoryMB = alloc.MemoryMB
	out.AllocatableDiskMB = alloc.DiskMB
	out.ResourcesKnown = alloc.CPUsKnown || alloc.MemoryKnown || alloc.DiskKnown
	if res, ok := c.reservedOn(h.ID); ok {
		out.ReservedCPUs = res.CPUs
		out.ReservedMemoryMB = res.MemoryMB
		out.ReservedKnown = true
	}
	if len(h.BackendInfo) > 0 {
		out.BackendInfo = make([]BackendInfoView, 0, len(h.BackendInfo))
		for _, b := range h.BackendInfo {
			out.BackendInfo = append(out.BackendInfo, BackendInfoView{
				Kind:         b.Kind,
				Available:    b.Available,
				Version:      b.Version,
				Rootless:     b.Rootless,
				Endpoint:     b.Endpoint,
				Detail:       b.Detail,
				DinD:         b.SupportsDinD,
				Limits:       b.Limits,
				SharedFolder: b.SharedFolder,
			})
		}
		return out
	}
	out.BackendInfo = make([]BackendInfoView, 0, len(h.Backends))
	for _, kind := range h.Backends {
		out.BackendInfo = append(out.BackendInfo, BackendInfoView{
			Kind:      store.BackendKind(kind),
			Available: true,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// JobView is one job with its pool named and its waits measured.
type JobView struct {
	Provisioning string         `json:"provisioning"`
	ProvisionNow bool           `json:"provision_now"`
	ID           string         `json:"id"`
	GitHubJobID  int64          `json:"github_job_id"`
	GitHubRunID  int64          `json:"github_run_id"`
	Repo         string         `json:"repo"`
	Workflow     string         `json:"workflow"`
	JobName      string         `json:"job_name"`
	Labels       []string       `json:"labels"`
	State        store.JobState `json:"state"`
	Conclusion   string         `json:"conclusion,omitempty"`
	// InstallationID is the GitHub App installation covering this job's
	// repository. It is read-only and derived at ingest; a pool only ever runs
	// work in its own installation's target, so this is half of why a job is
	// or is not claimed.
	InstallationID string `json:"installation_id,omitempty"`
	PoolID         string `json:"pool_id,omitempty"`
	PoolName       string `json:"pool_name,omitempty"`
	RunnerID       string `json:"runner_id,omitempty"`
	RunnerName     string `json:"runner_name,omitempty"`
	HTMLURL        string `json:"html_url,omitempty"`
	Matched        bool   `json:"matched"`
	// EligibleAt is when this fleet could first have acted on the job: the
	// moment an enabled pool claimed its labels. It is the start of the
	// scheduling-latency interval, and it is not queued_at -- the wait
	// before anything could run the job is not this fleet's to answer for.
	// Absent on a job nothing has claimed, and on one recorded before the
	// column existed.
	EligibleAt *time.Time `json:"eligible_at,omitempty"`
	// Hosted is true when every label names a runner somebody else operates:
	// GitHub's own or a hosted-runner vendor's. Such a job is theirs to run,
	// so it being unmatched here is expected rather than a job going nowhere.
	Hosted     bool   `json:"hosted"`
	HeadBranch string `json:"head_branch,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
	RunAttempt int    `json:"run_attempt,omitempty"`
	// RunNumber is GitHub's own sequential number for this workflow run -- the
	// "#1009" its Actions UI shows next to the workflow name -- so an operator
	// can find the same run there. Zero until a workflow_run lookup backfills
	// it, since the webhook that recorded the job never carries it.
	RunNumber int64           `json:"run_number,omitempty"`
	Steps     []store.JobStep `json:"steps"`
	// FailedStep is the step a failed job stopped at, worked out here from the
	// steps so that the grid and the drawer name the same one. Null when the
	// job did not fail on a step it ran.
	FailedStep *store.JobStep `json:"failed_step"`
	// RunnerFault is set when the runner executing this job stopped before
	// GitHub reported the job over: the fleet's own explanation of a failure
	// GitHub records like any other.
	RunnerFault string `json:"runner_fault,omitempty"`
	// FaultKind is the same failure as a category, and FaultDomain says whose
	// the failure is at all: "fleet" when this deployment is the reason, and
	// "workflow" when the job failed on its own merits and the fleet did its
	// part. Both are empty on a job that did not fail.
	//
	// The domain is rendered here rather than worked out in the browser
	// because the same judgement is made by the Overview's counts, the
	// problems panel and the CLI, and four places deciding "is this ours"
	// separately is four places to disagree about the number on the tile.
	FaultKind store.FaultKind `json:"fault_kind,omitempty"`
	// FaultFix is what to do about a fault of this kind, so a page showing the
	// failure can show the remedy without keeping its own copy of the table.
	FaultDomain string     `json:"fault_domain,omitempty"`
	FaultFix    string     `json:"fault_fix,omitempty"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	QueueWaitMS int64      `json:"queue_wait_ms"`
	DurationMS  int64      `json:"duration_ms"`
	// ControllerVersion and ControllerChannel are the controller build that
	// claimed the job, AgentVersion the agent of the host that ran it, and
	// HostID that host. Each is stamped once and never rewritten, and absent
	// on a job nothing here claimed or one recorded before they existed --
	// those are not guessed at.
	ControllerVersion string `json:"controller_version,omitempty"`
	ControllerChannel string `json:"controller_channel,omitempty"`
	AgentVersion      string `json:"agent_version,omitempty"`
	HostID            string `json:"host_id,omitempty"`
	// PeakCPUs and PeakMemoryMB are the most the job's runner was measured
	// using while the job ran, absent when it was never measured. OOMKilled
	// says the kernel killed the runner or one of its steps for memory, which
	// is the fleet's failure and the one history sizing corrects for.
	PeakCPUs     float64 `json:"peak_cpus,omitempty"`
	PeakMemoryMB int64   `json:"peak_memory_mb,omitempty"`
	OOMKilled    bool    `json:"oom_killed,omitempty"`
}

// JobSummaryView is a job without its steps: what GET /jobs returns under
// include_steps=false. A job's step list is most of its weight -- a dozen
// entries, each with two timestamps -- and a caller comparing a hundred jobs
// for their durations has no use for any of it. This is the shape that fits a
// hundred of them in a model's context; get_job or GET /jobs/{id} has the rest.
//
// It names the pool, host and runner rather than carrying their IDs and the
// job's labels, and it is not a subset of JobView by field name for that
// reason: it is the columns somebody comparing jobs reads, in the words they
// would use.
type JobSummaryView struct {
	ID          string         `json:"id"`
	Repo        string         `json:"repo"`
	Workflow    string         `json:"workflow"`
	JobName     string         `json:"job_name"`
	State       store.JobState `json:"state"`
	Conclusion  string         `json:"conclusion,omitempty"`
	FaultDomain string         `json:"fault_domain,omitempty"`
	QueuedAt    time.Time      `json:"queued_at"`
	StartedAt   *time.Time     `json:"started_at"`
	CompletedAt *time.Time     `json:"completed_at"`
	QueueWaitMS int64          `json:"queue_wait_ms"`
	DurationMS  int64          `json:"duration_ms"`
	// Pool is the pool's name, Host the host's ID as stamped when the job was
	// assigned, and Runner the runner's name.
	Pool              string `json:"pool,omitempty"`
	Host              string `json:"host,omitempty"`
	Runner            string `json:"runner,omitempty"`
	ControllerVersion string `json:"controller_version,omitempty"`
	ControllerChannel string `json:"controller_channel,omitempty"`
	AgentVersion      string `json:"agent_version,omitempty"`
	// PeakMemoryMB and OOMKilled are JobView's, so a caller comparing jobs can
	// see which of them were heavy without opening each one.
	PeakMemoryMB int64 `json:"peak_memory_mb,omitempty"`
	OOMKilled    bool  `json:"oom_killed,omitempty"`
}

// NewJobSummaryView renders a job as its summary, given its pool's name.
func NewJobSummaryView(j *store.Job, poolName string) JobSummaryView {
	return JobSummaryView{
		ID: j.ID, Repo: j.Repo, Workflow: j.Workflow, JobName: j.JobName,
		State: j.State, Conclusion: j.Conclusion, FaultDomain: j.FaultDomain(),
		QueuedAt: j.QueuedAt, StartedAt: j.StartedAt, CompletedAt: j.CompletedAt,
		QueueWaitMS: millis(j.QueueWait()), DurationMS: millis(j.Duration()),
		Pool: poolName, Host: j.HostID, Runner: j.RunnerName,
		ControllerVersion: j.ControllerVersion, ControllerChannel: j.ControllerChannel, AgentVersion: j.AgentVersion,
		PeakMemoryMB: j.PeakMemoryMB, OOMKilled: j.OOMKilled,
	}
}

// hostedJob reports whether a job's labels all name runners somebody else
// operates -- GitHub's own, or a hosted-runner vendor's. The installation's
// webhooks cover every job in its repositories, most of which this fleet never
// touches, and calling one of those "unmatched" as though it were stuck was
// alarming and false: it runs where its labels say.
//
// It is store.HostedJob so that the badge on the row and the predicate that
// decides whether the row is on the page at all cannot answer differently.
func hostedJob(labels []string) bool { return store.HostedJob(labels) }

// NewJobView renders a job, given the name of the pool that claimed it.
func NewJobView(j *store.Job, poolName string) JobView {
	return JobView{
		Provisioning:   j.Provisioning,
		ProvisionNow:   j.ProvisionNow,
		ID:             j.ID,
		GitHubJobID:    j.GitHubJobID,
		GitHubRunID:    j.GitHubRunID,
		Repo:           j.Repo,
		Workflow:       j.Workflow,
		JobName:        j.JobName,
		Labels:         emptySlice(j.Labels),
		State:          j.State,
		Conclusion:     j.Conclusion,
		InstallationID: j.InstallationID,
		PoolID:         j.PoolID,
		PoolName:       poolName,
		RunnerID:       j.RunnerID,
		RunnerName:     j.RunnerName,
		HTMLURL:        j.HTMLURL,
		Matched:        j.Matched,
		EligibleAt:     j.EligibleAt,
		Hosted:         hostedJob(j.Labels),
		HeadBranch:     j.HeadBranch,
		HeadSHA:        j.HeadSHA,
		RunAttempt:     j.RunAttempt,
		RunNumber:      j.RunNumber,
		Steps:          emptySlice([]store.JobStep(j.Steps)),
		FailedStep:     j.FailedStep(),
		RunnerFault:    j.RunnerFault,
		FaultKind:      j.FaultKind,
		FaultDomain:    j.FaultDomain(),
		FaultFix:       j.FaultKind.Fix(),
		QueuedAt:       j.QueuedAt,
		StartedAt:      j.StartedAt,
		CompletedAt:    j.CompletedAt,
		QueueWaitMS:    millis(j.QueueWait()),
		DurationMS:     millis(j.Duration()),

		ControllerVersion: j.ControllerVersion,
		ControllerChannel: j.ControllerChannel,
		AgentVersion:      j.AgentVersion,
		HostID:            j.HostID,
		PeakCPUs:          j.PeakCPUs,
		PeakMemoryMB:      j.PeakMemoryMB,
		OOMKilled:         j.OOMKilled,
	}
}

// JobRenderer names pools without a query per job.
type JobRenderer struct {
	pools map[string]string
}

// JobRenderer builds the index a page of jobs is rendered from.
func (c *Controller) JobRenderer(ctx context.Context) (*JobRenderer, error) {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	return &JobRenderer{pools: poolNames(pools)}, nil
}

// View renders one job.
func (v *JobRenderer) View(j *store.Job) JobView {
	return NewJobView(j, v.pools[j.PoolID])
}

// jobView renders a single job for the event stream. The pool is looked up on
// its own: an event is one job, and listing every pool to name one of them
// would cost the busiest moment of a webhook burst the most.
func (c *Controller) jobView(ctx context.Context, j *store.Job) JobView {
	name := ""
	if j.PoolID != "" {
		if p, err := c.st.GetPool(ctx, j.PoolID); err == nil {
			name = p.Name
		}
	}
	return NewJobView(j, name)
}

// ---------------------------------------------------------------------------
// Workflow runs
// ---------------------------------------------------------------------------

// WorkflowRunView is one workflow run -- the "#1009" in GitHub's Actions tab
// -- with its jobs summed up and its waits measured: the shape
// GET /workflow-runs returns and the Workflows page lists.
type WorkflowRunView struct {
	Repo           string `json:"repo"`
	Workflow       string `json:"workflow"`
	GitHubRunID    int64  `json:"github_run_id"`
	RunNumber      int64  `json:"run_number,omitempty"`
	RunAttempt     int    `json:"run_attempt,omitempty"`
	HeadBranch     string `json:"head_branch,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
	HTMLURL        string `json:"html_url,omitempty"`
	// State and Conclusion are the run's own, worked out from its jobs the
	// way GitHub's run page does it: see store.WorkflowRun.
	State      store.JobState `json:"state"`
	Conclusion string         `json:"conclusion,omitempty"`
	// Managed is whether this fleet has a hand in any job of the run, and
	// Hosted whether every job runs on somebody else's runners -- the same
	// two things a job says about itself, lifted to the run.
	Managed bool `json:"managed"`
	Hosted  bool `json:"hosted"`
	// Cancelling is whether an operator's cancellation of the run has been
	// accepted by GitHub and not yet confirmed by its jobs completing.
	Cancelling  bool               `json:"cancelling"`
	Jobs        store.RunJobCounts `json:"jobs"`
	QueuedAt    time.Time          `json:"queued_at"`
	StartedAt   *time.Time         `json:"started_at"`
	CompletedAt *time.Time         `json:"completed_at"`
	QueueWaitMS int64              `json:"queue_wait_ms"`
	DurationMS  int64              `json:"duration_ms"`
}

// NewWorkflowRunView renders a run. Nothing is looked up: every field is the
// store's own aggregate, so a page of runs costs one query.
func NewWorkflowRunView(r *store.WorkflowRun) WorkflowRunView {
	return WorkflowRunView{
		Repo:           r.Repo,
		Workflow:       r.Workflow,
		GitHubRunID:    r.GitHubRunID,
		RunNumber:      r.RunNumber,
		RunAttempt:     r.RunAttempt,
		HeadBranch:     r.HeadBranch,
		HeadSHA:        r.HeadSHA,
		InstallationID: r.InstallationID,
		HTMLURL:        r.HTMLURL,
		State:          r.State,
		Conclusion:     r.Conclusion,
		Managed:        r.Managed,
		Hosted:         r.Hosted,
		Cancelling:     r.Cancelling,
		Jobs:           r.Jobs,
		QueuedAt:       r.QueuedAt,
		StartedAt:      r.StartedAt,
		CompletedAt:    r.CompletedAt,
		QueueWaitMS:    millis(r.QueueWait()),
		DurationMS:     millis(r.Duration()),
	}
}

// ---------------------------------------------------------------------------
// Runners
// ---------------------------------------------------------------------------

// RunnerView is one runner, with the pool and host named rather than only
// referenced: a runner grid that shows two opaque IDs per row is a grid nobody
// can read.
type RunnerView struct {
	ResourceSample json.RawMessage   `json:"resource_sample,omitempty"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	PoolID         string            `json:"pool_id"`
	PoolName       string            `json:"pool_name,omitempty"`
	HostID         string            `json:"host_id"`
	HostName       string            `json:"host_name,omitempty"`
	State          store.RunnerState `json:"state"`
	GitHubRunnerID int64             `json:"github_runner_id,omitempty"`
	ContainerID    string            `json:"container_id,omitempty"`
	Ephemeral      bool              `json:"ephemeral"`
	Labels         []string          `json:"labels"`
	Image          string            `json:"image,omitempty"`
	ImageDigest    string            `json:"image_digest,omitempty"`
	RunnerVersion  string            `json:"runner_version,omitempty"`
	CurrentJobID   string            `json:"current_job_id,omitempty"`
	CurrentJob     *JobView          `json:"current_job,omitempty"`
	Message        string            `json:"message,omitempty"`
	// FaultKind categorises why a failed runner failed, and FaultFix is what
	// to do about it. Empty on a runner that did not fail. A runner that never
	// took a job is the case these exist for: nothing else in the system says
	// why a pool's containers will not start.
	FaultKind   store.FaultKind `json:"fault_kind,omitempty"`
	FaultFix    string          `json:"fault_fix,omitempty"`
	JobsHandled int             `json:"jobs_handled"`
	CPUPercent  float64         `json:"cpu_percent,omitempty"`
	MemoryBytes int64           `json:"memory_bytes,omitempty"`
	// AllocatedCPUs and AllocatedMemoryMB are the limits this runner's
	// workload was created with, and AllocationSource says whether they are
	// the pool's own ("pool") or one slot's share of the host it landed on
	// ("host"). Absent on a runner created with no limit at all. They are on
	// the view so that an OOM kill on a defaulted limit points an operator at
	// the host's capacity rather than at a pool field nobody set.
	AllocatedCPUs     float64          `json:"allocated_cpus,omitempty"`
	AllocatedMemoryMB int64            `json:"allocated_memory_mb,omitempty"`
	AllocationSource  string           `json:"allocation_source,omitempty"`
	CPUResource       *CPUResourceView `json:"cpu_resource,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	// ContainerStartedAt and RegisteredAt are the two halves of coming up, and
	// they are on the view because the gap between them is the whole diagnosis
	// of a runner stuck in `registering`: a container that never started is a
	// backend or image problem on the host, and a container that started and
	// never registered is a credential, network or GitHub one. Without both, a
	// stuck runner has one symptom and two unrelated causes.
	CreateTaskIssuedAt    *time.Time `json:"create_task_issued_at,omitempty"`
	HostRemovedAt         *time.Time `json:"host_removed_at,omitempty"`
	RegistrationDeletedAt *time.Time `json:"registration_deleted_at,omitempty"`
	CleanupEstimatedAt    *time.Time `json:"cleanup_estimated_at,omitempty"`
	ContainerStartedAt    *time.Time `json:"container_started_at,omitempty"`
	RegisteredAt          *time.Time `json:"registered_at,omitempty"`
	StartedAt             *time.Time `json:"started_at"`
	LastIdleAt            *time.Time `json:"last_idle_at"`
	FinishedAt            *time.Time `json:"finished_at"`
	// CleanupError and its companions describe a runner Zoomies could not
	// finish taking away. An empty error is the normal case; a non-empty one
	// means something is still on a host or on GitHub.
	CleanupError    string     `json:"cleanup_error,omitempty"`
	CleanupFailedAt *time.Time `json:"cleanup_failed_at,omitempty"`
	CleanupAttempts int        `json:"cleanup_attempts,omitempty"`
	// CleanedUpAt is the end of the runner's life: nothing of it left, on the
	// host or on GitHub.
	CleanedUpAt *time.Time `json:"cleaned_up_at,omitempty"`
}

// CPUResourceView keeps the playful fleet voice beside exact numbers and a
// stable state. Operators can scan the label; clients and alerts use State.
type CPUResourceView struct {
	State          string  `json:"state"`
	Label          string  `json:"label"`
	Reason         string  `json:"reason"`
	GuaranteedCPUs float64 `json:"guaranteed_cpus"`
	CurrentCPUs    float64 `json:"current_cpus"`
	CeilingCPUs    float64 `json:"ceiling_cpus"`
	Factor         float64 `json:"factor"`
	// SizedForCPUs is the CPU count the runner's toolchains were told to size
	// their workers for when it started; zero when they were told nothing.
	SizedForCPUs int `json:"sized_for_cpus,omitempty"`
}

// RunnerRenderer names pools and hosts without a query per runner.
type RunnerRenderer struct {
	pools map[string]*store.Pool
	hosts map[string]*store.Host
	jobs  map[string]*store.Job
}

// RunnerRenderer builds the index a page of runners is rendered from.
func (c *Controller) RunnerRenderer(ctx context.Context, runners []*store.Runner) (*RunnerRenderer, error) {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}
	v := &RunnerRenderer{pools: map[string]*store.Pool{}, hosts: map[string]*store.Host{}, jobs: map[string]*store.Job{}}
	for _, p := range pools {
		v.pools[p.ID] = p
	}
	for _, h := range hosts {
		v.hosts[h.ID] = h
	}
	// Only the runners that are actually executing something need a job, which
	// on an idle fleet is none of them.
	for _, run := range runners {
		if run.CurrentJobID == "" {
			continue
		}
		if _, done := v.jobs[run.CurrentJobID]; done {
			continue
		}
		j, err := c.st.GetJob(ctx, run.CurrentJobID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("reading the job a runner is executing: %w", err)
		}
		v.jobs[run.CurrentJobID] = j
	}
	return v, nil
}

// View renders one runner.
func (v *RunnerRenderer) View(r *store.Runner) RunnerView {
	pool := v.pools[r.PoolID]
	host := v.hosts[r.HostID]
	poolName, hostName := "", ""
	if pool != nil {
		poolName = pool.Name
	}
	if host != nil {
		hostName = host.Name
	}
	out := RunnerView{
		ID:                    r.ID,
		Name:                  r.Name,
		PoolID:                r.PoolID,
		PoolName:              poolName,
		HostID:                r.HostID,
		HostName:              hostName,
		State:                 r.State,
		GitHubRunnerID:        r.GitHubRunnerID,
		ContainerID:           r.ContainerID,
		Ephemeral:             r.Ephemeral,
		Labels:                emptySlice(r.Labels),
		Image:                 r.Image,
		ImageDigest:           r.ImageDigest,
		RunnerVersion:         r.RunnerVersion,
		CurrentJobID:          r.CurrentJobID,
		Message:               r.Message,
		FaultKind:             r.FaultKind,
		FaultFix:              r.FaultKind.Fix(),
		JobsHandled:           r.JobsHandled,
		ResourceSample:        r.ResourceSample,
		CPUPercent:            r.CPUPercent,
		MemoryBytes:           r.MemoryBytes,
		AllocatedCPUs:         r.AllocatedCPUs,
		AllocatedMemoryMB:     r.AllocatedMemoryMB,
		AllocationSource:      r.AllocationSource,
		CPUResource:           cpuResourceView(r, pool, host),
		CreatedAt:             r.CreatedAt,
		CreateTaskIssuedAt:    r.CreateTaskIssuedAt,
		HostRemovedAt:         r.HostRemovedAt,
		RegistrationDeletedAt: r.RegistrationDeletedAt,
		CleanupEstimatedAt:    r.CleanupEstimatedAt,
		ContainerStartedAt:    r.ContainerStartedAt,
		RegisteredAt:          r.RegisteredAt,
		StartedAt:             r.StartedAt,
		LastIdleAt:            r.LastIdleAt,
		FinishedAt:            r.FinishedAt,
		CleanupError:          r.CleanupError,
		CleanupFailedAt:       r.CleanupFailedAt,
		CleanupAttempts:       r.CleanupAttempts,
		CleanedUpAt:           r.CleanedUpAt,
	}
	if j := v.jobs[r.CurrentJobID]; j != nil {
		jobPool := ""
		if p := v.pools[j.PoolID]; p != nil {
			jobPool = p.Name
		}
		job := NewJobView(j, jobPool)
		out.CurrentJob = &job
	}
	return out
}

func cpuResourceView(r *store.Runner, p *store.Pool, h *store.Host) *CPUResourceView {
	if r == nil || p == nil || r.AllocatedCPUs <= 0 {
		return nil
	}
	guaranteed := r.AllocatedCPUs
	if p.DockerMode == store.DockerDinD && (r.AllocationSource == store.AllocationFromPool ||
		((r.AllocationSource == store.AllocationReduced || r.AllocationSource == store.AllocationHistory) && p.Resources.CPUs > 0)) {
		// A fixed DinD allocation is per container and the host ledger charges
		// both halves. An automatic allocation -- reduced or not -- is already
		// the logical runner's whole slot and is split between them, so only
		// the former doubles.
		guaranteed *= 2
	}
	factor := 1.0
	var sample backend.Stats
	if len(r.ResourceSample) > 0 && json.Unmarshal(r.ResourceSample, &sample) == nil && sample.CPUAllocationFactor > 0 {
		factor = sample.CPUAllocationFactor
	}
	ceiling := p.CPUBurst.MaxCPUs
	if ceiling <= 0 && h != nil {
		ceiling = h.Allocatable().CPUs
	}
	if !p.CPUBurst.Observes() {
		ceiling = guaranteed
	}
	// A policy edited below an already-running runner's guarantee cannot
	// reduce that guarantee. Keep the displayed ceiling truthful while the
	// next runner creation adopts the new policy.
	ceiling = max(ceiling, guaranteed)
	state, label, reason := "guaranteed", "Steady paws — guaranteed pace", "base_allocation"
	switch {
	case factor < .99:
		state, label, reason = "throttled", "Leash tightened — host under pressure", "host_pressure"
	case factor >= 1.75:
		state, label, reason = "maximum_zoomies", "Squirrel spotted — maximum zoomies", "spare_cpu_lent"
	case factor > 1.01:
		state, label, reason = "zoomies", "Rabbit spotted — extra zoomies", "spare_cpu_lent"
	case p.CPUBurst.Mode == store.CPUBurstObserve:
		state, label, reason = "observing", "Nose to the wind — watching spare CPU", "observe_only"
	case !p.CPUBurst.Observes():
		// A pool with elasticity off holds every runner at its share, and a
		// runner doing exactly that is not at "guaranteed pace" -- that is the
		// elastic pool's word for a runner that may yet be lent something. It
		// is sitting where it was put, and it says so with a state of its own
		// rather than with nothing: a runner with no CPU state at all reads
		// as a quota nobody measured, which a held one is not. Host-pressure
		// throttling still applies to any limited container, so the cases
		// above keep the lead on a runner of this pool visible too.
		state, label, reason = "sit_and_stay", "Sit and stay — CPU held at its share", "elastic_off"
	}
	return &CPUResourceView{
		State: state, Label: label, Reason: reason, GuaranteedCPUs: guaranteed,
		CurrentCPUs: math.Round(guaranteed*factor*100) / 100,
		CeilingCPUs: ceiling, Factor: factor, SizedForCPUs: r.SizedForCPUs,
	}
}

// runnerView renders a single runner for the event stream: three point reads
// rather than two list queries, because a reconcile pass publishes one event
// per runner it touched and the fleet may be large.
func (c *Controller) runnerView(ctx context.Context, r *store.Runner) RunnerView {
	v := &RunnerRenderer{pools: map[string]*store.Pool{}, hosts: map[string]*store.Host{}, jobs: map[string]*store.Job{}}
	if p, err := c.st.GetPool(ctx, r.PoolID); err == nil {
		v.pools[p.ID] = p
	}
	if h, err := c.st.GetHost(ctx, r.HostID); err == nil {
		v.hosts[h.ID] = h
	}
	if r.CurrentJobID != "" {
		if j, err := c.st.GetJob(ctx, r.CurrentJobID); err == nil {
			v.jobs[j.ID] = j
		}
	}
	return v.View(r)
}

// ---------------------------------------------------------------------------
// Pools
// ---------------------------------------------------------------------------

// PoolCountsView is a pool's live runner tally, in the shape the OpenAPI
// document's Pool.counts has.
type PoolCountsView struct {
	Provisioning int `json:"provisioning"`
	Registering  int `json:"registering"`
	Idle         int `json:"idle"`
	Busy         int `json:"busy"`
	Draining     int `json:"draining"`
	Failed       int `json:"failed"`
	Live         int `json:"live"`
}

// PoolView is a pool plus what an operator needs to see next to it: which
// installation it belongs to, how many runners it has in each state, how much
// of itself it is using, and every dangerous setting it has in effect.
type PoolView struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	InstallationID     string            `json:"installation_id"`
	InstallationTarget string            `json:"installation_target,omitempty"`
	Labels             []string          `json:"labels"`
	RunnerGroup        string            `json:"runner_group,omitempty"`
	Backend            store.BackendKind `json:"backend"`
	// Platform is the machine this pool's runners need. It picks the runner
	// image and restricts which hosts the scheduler may place them on.
	Platform store.Platform `json:"platform"`
	Image    string         `json:"image"`
	// EffectiveImage is the image runners will actually boot: Image when the
	// pool names one, otherwise the variant its platform selects, otherwise
	// the instance default. A pool page that showed a blank image field and
	// nothing else would leave an operator guessing at the single most
	// important thing about their runners.
	EffectiveImage string           `json:"effective_image"`
	PullPolicy     store.PullPolicy `json:"pull_policy"`
	RunnerVersion  string           `json:"runner_version,omitempty"`
	MinRunners     int              `json:"min_runners"`
	MaxRunners     int              `json:"max_runners"`
	Priority       int              `json:"priority"`
	// RepositoryScaleUpLimit and CostPerRunnerHour are accepted on the way in,
	// so they are rendered on the way out: a field the API takes but never
	// shows again is a field an operator cannot check, edit or explain.
	RepositoryScaleUpLimit int                  `json:"repository_scale_up_limit"`
	CostPerRunnerHour      *float64             `json:"cost_per_runner_hour"`
	IdleTimeout            store.Duration       `json:"idle_timeout"`
	Ephemeral              bool                 `json:"ephemeral"`
	DockerMode             store.DockerMode     `json:"docker_mode"`
	Resources              store.Resources      `json:"resources"`
	CPUBurst               store.CPUBurstPolicy `json:"cpu_burst"`
	// Sizing is how this pool decides what one runner gets: "automatic", one
	// slot's share of whichever host it lands on, or "fixed", the figures in
	// Resources. It is derived from Resources rather than stored beside it,
	// so the two can never disagree -- but it is rendered, because a browser
	// reading "no CPU limit" has no way to tell "the host decides" from
	// "nobody has set one", and those used to be the same thing.
	Sizing string `json:"sizing"`
	// EffectiveMinimum is the minimum the fleet holds this pool's runners to:
	// Resources' own minimum where the pool set one, and the fleet's
	// runners.minimum_* where it did not. Resources keeps the pool's own
	// figures so that saving the pool never turns an inherited minimum into
	// one of its own; this is where an operator sees what is inherited.
	EffectiveMinimum PoolMinimumView `json:"effective_minimum"`
	// RunnerSettings is what this pool overrides of the fleet's runner
	// timings. Every field is absent on a pool that follows the fleet, which
	// is what an unedited pool does.
	RunnerSettings store.RunnerSettings `json:"runner_settings"`
	Cache          store.CacheConfig    `json:"cache"`
	Tmpfs          store.TmpfsConfig    `json:"tmpfs"`
	HostSelector   map[string]string    `json:"host_selector"`
	Env            map[string]string    `json:"env"`
	RunAsRoot      bool                 `json:"run_as_root"`
	// NoDefaultLabels says the pool's runners advertise only Labels, without
	// self-hosted and the operating-system and architecture labels.
	NoDefaultLabels bool           `json:"no_default_labels"`
	Enabled         bool           `json:"enabled"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Counts          PoolCountsView `json:"counts"`
	QueuedJobs      int            `json:"queued_jobs"`
	Utilisation     float64        `json:"utilisation"`
	Warnings        []Problem      `json:"warnings,omitempty"`
}

// PoolRenderer is everything needed to render pools without one query per
// pool.
type PoolRenderer struct {
	counts        map[string]store.PoolCounts
	installations map[string]*store.Installation
	queued        map[string]int
	// blocked holds, per pool, the scheduler's reason for not placing the
	// runners that pool wanted. It is the answer to the question the pool page
	// is opened to ask.
	blocked map[string][]Problem
	// defaultImage is what a pool that names neither an image nor a platform
	// will boot, which the renderer needs to resolve EffectiveImage.
	defaultImage string
	// cfg is the fleet's own settings, which a pool's warnings are measured
	// against: a pool that overrides a runner timing is only right or wrong
	// relative to what the fleet would otherwise have done.
	cfg *config.Config
}

// PoolRenderer gathers the per-pool counts, installation targets and queue
// depths in three queries rather than three per pool.
func (c *Controller) PoolRenderer(ctx context.Context) (*PoolRenderer, error) {
	counts, err := c.st.CountRunnersByPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting runners by pool: %w", err)
	}
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing installations: %w", err)
	}
	installations := make(map[string]*store.Installation, len(insts))
	for _, i := range insts {
		installations[i.ID] = i
	}
	jobs, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing queued jobs: %w", err)
	}
	queued := map[string]int{}
	for _, j := range jobs {
		if j.PoolID != "" {
			queued[j.PoolID]++
		}
	}
	blocked := map[string][]Problem{}
	for _, p := range c.PoolCapacityProblems() {
		blocked[p.TargetID] = append(blocked[p.TargetID], p)
	}
	// The pool's own page carries the same sentences the drawer does: an
	// operator who opens a pool because its jobs are going somewhere strange
	// should not have to find the drawer to be told why.
	for _, p := range c.PoolRunnerGroupProblems() {
		blocked[p.TargetID] = append(blocked[p.TargetID], p)
	}
	cfg := c.cfg()
	return &PoolRenderer{counts: counts, installations: installations, queued: queued,
		blocked: blocked, defaultImage: cfg.GitHub.RunnerImage, cfg: cfg}, nil
}

// image is the image this pool's runners will actually boot, resolved the same
// way the controller resolves it when it makes one -- including the Docker
// variant a pool that gives its jobs a daemon gets.
func (v *PoolRenderer) image(p *store.Pool) string {
	return config.ResolvePoolRunnerImage(p.Image, p.Platform.OS, p.Platform.OSVersion,
		v.defaultImage, p.DockerMode.GivesDaemon())
}

// PoolMinimumView is a pool's minimum in force, and which half of it came
// from the fleet rather than from the pool.
type PoolMinimumView struct {
	CPUs              float64 `json:"cpus"`
	MemoryMB          int64   `json:"memory_mb"`
	CPUsInherited     bool    `json:"cpus_inherited"`
	MemoryMBInherited bool    `json:"memory_mb_inherited"`
}

func (v *PoolRenderer) minimum(p *store.Pool) PoolMinimumView {
	var fleet config.Runners
	if v.cfg != nil {
		fleet = v.cfg.Runners
	}
	cpus, memoryMB := EffectiveMinimum(p.Resources, fleet)
	return PoolMinimumView{
		CPUs: cpus, MemoryMB: memoryMB,
		CPUsInherited:     cpus != p.Resources.MinCPUs,
		MemoryMBInherited: memoryMB != p.Resources.MinMemoryMB,
	}
}

// View renders one pool.
func (v *PoolRenderer) View(p *store.Pool) PoolView {
	cnt := v.counts[p.ID]
	inst := v.installations[p.InstallationID]
	target := ""
	if inst != nil {
		target = inst.Target
	}
	return PoolView{
		ID:                     p.ID,
		Name:                   p.Name,
		InstallationID:         p.InstallationID,
		InstallationTarget:     target,
		Labels:                 emptySlice(p.Labels),
		RunnerGroup:            p.RunnerGroup,
		Backend:                p.Backend,
		Platform:               p.Platform,
		Image:                  p.Image,
		EffectiveImage:         v.image(p),
		PullPolicy:             p.PullPolicy,
		RunnerVersion:          p.RunnerVersion,
		MinRunners:             p.MinRunners,
		MaxRunners:             p.MaxRunners,
		Priority:               p.Priority,
		RepositoryScaleUpLimit: p.RepositoryScaleUpLimit,
		CostPerRunnerHour:      p.CostPerRunnerHour,
		IdleTimeout:            p.IdleTimeout,
		Ephemeral:              p.Ephemeral,
		DockerMode:             p.DockerMode,
		Resources:              p.Resources,
		CPUBurst:               p.CPUBurst,
		Sizing:                 PoolSizing(p),
		EffectiveMinimum:       v.minimum(p),
		RunnerSettings:         p.RunnerSettings,
		Cache:                  p.Cache,
		Tmpfs:                  p.Tmpfs,
		HostSelector:           emptyMap(p.HostSelector),
		Env:                    emptyMap(p.Env),
		RunAsRoot:              p.RunAsRoot,
		NoDefaultLabels:        p.NoDefaultLabels,
		Enabled:                p.Enabled,
		CreatedAt:              p.CreatedAt,
		UpdatedAt:              p.UpdatedAt,
		Counts: PoolCountsView{
			Provisioning: cnt.Provisioning, Registering: cnt.Registering,
			Idle: cnt.Idle, Busy: cnt.Busy, Draining: cnt.Draining, Failed: cnt.Failed,
			Live: cnt.Live(),
		},
		QueuedJobs:  v.queued[p.ID],
		Utilisation: cnt.Utilisation(),
		Warnings:    append(PoolWarnings(p, inst, v.cfg), v.blocked[p.ID]...),
	}
}

// WithoutEnvValues returns the view with every environment value blanked and
// every key left in place.
//
// Env is injected into every runner a pool creates, which makes it the one
// field on this view an operator may reasonably have put a registry password or
// a proxy credential in -- and reading a pool is a viewer action while setting
// one is an operator action, so the two were not the same audience. Viewer is
// documented as reading everything except secrets; this is what makes that true
// of pools.
//
// The keys stay because they are what the pool page renders -- it lists names
// and never values -- so a viewer still sees which variables a pool sets, and
// only the operator who could have written them can read them back.
func (p PoolView) WithoutEnvValues() PoolView {
	if len(p.Env) == 0 {
		return p
	}
	env := make(map[string]string, len(p.Env))
	for k := range p.Env {
		env[k] = ""
	}
	p.Env = env
	return p
}

// PoolWarnings renders a pool's dangerous settings as problems.
//
// They are the same sentences the UI's problems drawer shows, because an
// operator should not have to learn that "host-socket" on the pool page and
// "any job on this pool can become root on the host" on the Overview are the
// same fact.
//
// The installation is there for the one risk a pool cannot see in itself,
// the repository cache below. Nil when it is unknown, which validation
// reports on its own.
// The two ways a pool decides how much machine one of its runners gets.
const (
	// SizingAutomatic is one slot's guaranteed share of whichever host the runner lands
	// on: charged by scheduler.Reserve and applied as a real cgroup limit by
	// scheduler.Allocation, so the books and the cgroups say the same thing.
	// It is what a pool means by naming no size, and it keeps fitting when a
	// bigger machine joins the fleet -- which a figure typed once does not.
	SizingAutomatic = "automatic"
	// SizingElastic is the same guaranteed host share, with unused CPU lent
	// to busy runners while fresh host measurements say it is safe.
	SizingElastic = "elastic"
	// SizingFixed is the figures on the pool, the same on every host.
	SizingFixed = "fixed"
)

// PoolSizing says which of the two a pool is doing.
func PoolSizing(p *store.Pool) string {
	if p.Automatic() && p.CPUBurst.Enforces() {
		return SizingElastic
	}
	if p.Automatic() {
		return SizingAutomatic
	}
	return SizingFixed
}

func PoolWarnings(p *store.Pool, inst *store.Installation, cfg *config.Config) []Problem {
	var out []Problem
	if cfg != nil && p.Automatic() && (p.Backend == store.BackendDocker || p.Backend == store.BackendPodman) {
		for _, f := range cfg.Validate() {
			switch f.Code {
			case "scheduler.default_runner_limits_off", "scheduler.host_throttling_off", "agent.bootstrap_cpu_grace_short", "scheduler.provision_timeout_short":
			case "runners.docker_wait_short":
				if p.DockerMode != store.DockerDinD {
					continue
				}
			default:
				continue
			}
			out = append(out, Problem{Code: f.Code, Severity: f.Severity, Setting: f.Setting,
				Title: f.Title, Detail: f.Detail, Fix: f.Fix, TargetKind: "pool", TargetID: p.ID})
		}
	}
	if w, ok := poolStartLadderWarning(p, cfg); ok {
		out = append(out, w)
	}
	for _, d := range p.Dangerous() {
		// A fleet that builds container images for a living has decided about
		// the privileged sidecar once, and a row per pool per pass about a
		// decision already taken is what makes an operator stop reading the
		// list. Only that sentence is silenced, and only by asking: the host
		// socket hands a job root on the host, and says so either way.
		if cfg != nil && cfg.Security.DockerInDockerExpected && d == store.DinDDanger {
			continue
		}
		out = append(out, Problem{
			Code:       "pool.dangerous",
			Severity:   config.SeverityWarning,
			Title:      fmt.Sprintf("pool %s: %s", p.Name, d),
			Detail:     "this pool was configured to weaken the isolation between a workflow job and the host it runs on.",
			Fix:        fmt.Sprintf("edit the %s pool if this was not deliberate.", p.Name),
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}
	if w, ok := cacheSharingWarning(p, inst); ok {
		out = append(out, w)
	}
	if w, ok := tmpfsMemoryWarning(p); ok {
		out = append(out, w)
	}
	if w, ok := cacheInMemoryWarning(p); ok {
		out = append(out, w)
	}
	if w, ok := dockerClientWarning(p, cfg); ok {
		out = append(out, w)
	}
	return out
}

// dockerClientWarning is the pool that was promised a Docker daemon it has no
// way to reach.
//
// A docker_mode gives a job a daemon; the client comes from the image, and the
// stock runner image carries none. config.RunnerImageFor moves such a pool onto
// the Docker variant wherever it knows the variant exists, which is every tag
// this build publishes -- but not a digest, which names one exact image, and
// not a pin from some other build, which may name a run whose second image was
// never pushed. What is left is a pool whose daemon comes up unused and whose
// every job dies at its first Docker step with an error naming a missing binary
// and not the reason. The runner says so in its own log before any job runs;
// this says it where an operator is already looking.
func dockerClientWarning(p *store.Pool, cfg *config.Config) (Problem, bool) {
	if p == nil || cfg == nil || !p.DockerMode.GivesDaemon() {
		return Problem{}, false
	}
	image := config.ResolvePoolRunnerImage(p.Image, p.Platform.OS, p.Platform.OSVersion,
		cfg.GitHub.RunnerImage, true)
	pin, missing := config.MissingDockerClient(image)
	if !missing {
		return Problem{}, false
	}
	// Where the reference came from decides what there is to change: a pool
	// that named the image owns it, and a pool that named none is running the
	// fleet's default, which no edit to the pool can correct.
	// The wording avoids apostrophes: the pool name can arrive in an imported
	// document, and a quote character beside it reads to a scanner as a
	// string that could be broken out of, even though this is only prose.
	fix := fmt.Sprintf("set the image of pool %s to %s, or clear it so the pool follows the default for the fleet.", p.Name, pin)
	if strings.TrimSpace(p.Image) == "" {
		fix = fmt.Sprintf("set github.runner_image to %s, which fixes every pool following the default for the fleet, or give pool %s that image of its own.", pin, p.Name)
	}
	return Problem{
		Code:     "pool.docker_client_missing",
		Severity: config.SeverityError,
		Title: fmt.Sprintf("pool %s: its jobs are given a Docker daemon and no client to reach it with",
			p.Name),
		Detail: fmt.Sprintf("this pool's runners boot %s, the stock runner image, which carries no Docker client "+
			"on purpose -- most pools never build an image. A pool that asks for a daemon is moved onto %s, the "+
			"same image plus a client, wherever that image is known to exist; this one is a reference the swap "+
			"cannot follow -- a digest names one exact image, and a tag from another build may name a run whose "+
			"variant was never published. So the daemon comes up unused, and every job on this pool fails at its "+
			"first Docker step with \"Unable to locate executable file: docker\", which names the missing binary "+
			"and not the reason.",
			image, config.DefaultRunnerDockerImage),
		Fix:        fix,
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// PoolEffectiveDockerWait is how long this pool's runners actually wait for
// their daemon: the pool's own override where it has one, the fleet's figure
// where it does not, and the runner image's own two minutes where neither
// says. Zero from either means "the image chooses", never "no wait".
func PoolEffectiveDockerWait(p *store.Pool, cfg *config.Config) time.Duration {
	if d := p.RunnerSettings.DockerWait; d != nil {
		if d.Duration() > 0 {
			return d.Duration()
		}
		return config.ImageDockerWait
	}
	if cfg == nil {
		return config.ImageDockerWait
	}
	return cfg.Runners.EffectiveDockerWait()
}

// poolStartLadderWarning is scheduler.provision_timeout_short asked of one
// pool, because a pool may now answer both halves of it for itself.
//
// The fleet's own defaults are held in the right order by an invariant test,
// and the validator says so when an operator sets them otherwise. Neither
// reaches a pool that overrides the provision timeout, the Docker wait, or one
// without the other -- and a pool that fails its runners inside the wait it
// configured them to do is the same outage on a smaller scale: the runner
// still coming up is condemned, and its replacement pulls the same image over
// the link that was slow to begin with.
//
// Only a pool with a daemon counts the wait, because only that pool does it.
func poolStartLadderWarning(p *store.Pool, cfg *config.Config) (Problem, bool) {
	if p == nil || cfg == nil || !p.Enabled {
		return Problem{}, false
	}
	// Only a pool that answered one of the two halves for itself. A pool
	// following the fleet into a bad order is the fleet's own finding --
	// scheduler.provision_timeout_short, which names the setting to change --
	// and repeating it once per pool would bury that one sentence under a row
	// for every pool in the fleet, all of them pointing at the same fix.
	if p.RunnerSettings.ProvisionTimeout == nil && p.RunnerSettings.DockerWait == nil {
		return Problem{}, false
	}
	timeout := cfg.Scheduler.ProvisionTimeout
	if d := p.RunnerSettings.ProvisionTimeout; d != nil {
		timeout = d.Duration()
	}
	// Zero is "never give up", which cannot condemn anything.
	if timeout <= 0 {
		return Problem{}, false
	}
	wait := time.Duration(0)
	if p.DockerMode.GivesDaemon() {
		wait = PoolEffectiveDockerWait(p, cfg)
	}
	start := config.RunnerCreateBudget + wait
	if timeout > start {
		return Problem{}, false
	}
	detail := fmt.Sprintf("an agent gives itself %s for a create, because a cold image pull on a slow link is minutes rather than seconds",
		config.RunnerCreateBudget)
	if wait > 0 {
		detail += fmt.Sprintf(", and a runner of this pool then waits up to %s for the Docker daemon it was promised before it registers", wait)
	}
	detail += fmt.Sprintf(". A provision timeout of %s fails runners that are still coming up, and the replacement pulls the same image over the same link.", timeout)
	return Problem{
		Code:     "pool.provision_timeout_short",
		Severity: config.SeverityWarning,
		Title: fmt.Sprintf("pool %s: runners are failed after %s but may legitimately take %s to start",
			p.Name, timeout, start),
		Detail:     detail,
		Fix:        fmt.Sprintf("set this pool's provision timeout above %s, or clear it to follow the fleet's %s.", start, cfg.Scheduler.ProvisionTimeout),
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// cacheSharingWarning names the way a repository cache stops being one.
//
// Under an organisation installation a pool's runners register to the
// organisation, and GitHub gives a runner any queued job whose runs-on matches
// its labels; Zoomies has no say in which repository that job comes from. A
// cache keyed to one repository is therefore only that repository's for as
// long as no other repository's workflow asks for this pool's labels -- a
// discipline kept in other people's workflow files, which is why it is a
// standing warning rather than a line in the docs. A repository-targeted
// installation registers runners only that repository's jobs can reach, so
// there the cache is as private as it looks.
func cacheSharingWarning(p *store.Pool, inst *store.Installation) (Problem, bool) {
	if inst == nil || inst.TargetType == store.TargetRepo {
		return Problem{}, false
	}
	if !p.Cache.Enabled || p.Cache.Scope != store.CacheScopeRepository {
		return Problem{}, false
	}
	repo := strings.TrimSpace(p.Cache.Repository)
	if repo == "" {
		repo = "the repository"
	}
	return Problem{
		Code:     "pool.cache_shared",
		Severity: config.SeverityWarning,
		Title:    fmt.Sprintf("pool %s: the %s cache is only as private as the pool's labels", p.Name, repo),
		Detail: fmt.Sprintf("runners in this pool register to %s, so GitHub can hand them any repository's job whose runs-on asks for this pool's labels, "+
			"and that job reads and writes the cache. Nothing in Zoomies stops another repository's workflow doing so.", inst.Target),
		Fix:        fmt.Sprintf("give the %s pool a branded label that only %s's workflows use in runs-on, and keep it that way.", p.Name, repo),
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// ---------------------------------------------------------------------------
// Installations
// ---------------------------------------------------------------------------

// InstallationView is a GitHub App installation as the API returns it.
//
// The private key and the webhook secret are absent, and there is no field they
// could be put in: the store keeps them sealed and tagged `json:"-"`, and this
// type names every field explicitly so that adding one to the domain model
// cannot leak it here by accident.
type InstallationView struct {
	ID             string           `json:"id"`
	AppID          int64            `json:"app_id"`
	InstallationID int64            `json:"installation_id"`
	Target         string           `json:"target"`
	TargetType     store.TargetType `json:"target_type"`
	APIBaseURL     string           `json:"api_base_url"`
	AppSlug        string           `json:"app_slug,omitempty"`
	WebURL         string           `json:"web_url,omitempty"`
	// SettingsURL is the App's own settings page on GitHub. It is carried on
	// every installation, not only on the one the connect flow just created,
	// because the one thing a manifest cannot do is set the App's avatar --
	// GitHub takes it as an upload -- and an operator who missed that step
	// during setup has nowhere else to be told about it. Empty when the slug
	// is unknown, which is what a hand-added installation looks like.
	SettingsURL   string     `json:"settings_url,omitempty"`
	Enterprise    bool       `json:"enterprise"`
	Healthy       bool       `json:"healthy"`
	LastError     string     `json:"last_error,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	PoolCount     int        `json:"pool_count"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// NewInstallationView renders an installation, given how many pools depend on
// it.
func NewInstallationView(i *store.Installation, pools int) InstallationView {
	return InstallationView{
		ID:             i.ID,
		AppID:          i.AppID,
		InstallationID: i.InstallationID,
		Target:         i.Target,
		TargetType:     i.TargetType,
		APIBaseURL:     i.APIBaseURL,
		AppSlug:        i.AppSlug,
		WebURL:         github.WebURLForAPI(i.APIBaseURL),
		SettingsURL:    github.SettingsURL(i.APIBaseURL, i.AppSlug, settingsOrgOf(i)),
		Enterprise:     github.IsEnterprise(i.APIBaseURL),
		Healthy:        i.Healthy(),
		LastError:      i.LastError,
		LastCheckedAt:  i.LastCheckedAt,
		PoolCount:      pools,
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      i.UpdatedAt,
	}
}

// settingsOrgOf names the organisation an App's settings live under, which is
// the target for an org App and nothing at all for a repo App: GitHub answers
// the wrong one with a 404 rather than a redirect.
func settingsOrgOf(i *store.Installation) string {
	if i.TargetType == store.TargetOrg {
		return i.Target
	}
	return ""
}

// PoolCountsByInstallation answers "how much depends on this installation?",
// which is what makes the delete confirmation honest.
func (c *Controller) PoolCountsByInstallation(ctx context.Context) (map[string]int, error) {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	out := map[string]int{}
	for _, p := range pools {
		out[p.InstallationID]++
	}
	return out, nil
}

// installationView renders one installation for the event stream.
func (c *Controller) installationView(ctx context.Context, inst *store.Installation) InstallationView {
	counts, err := c.PoolCountsByInstallation(ctx)
	if err != nil {
		counts = nil
	}
	return NewInstallationView(inst, counts[inst.ID])
}

// ---------------------------------------------------------------------------
// Problems
// ---------------------------------------------------------------------------

// ProblemsView carries the drawer's own "nothing is wrong" flag rather than
// leaving the UI to infer it from an empty array, so that "we checked and all
// is well" and "we have not looked yet" cannot be rendered the same way.
type ProblemsView struct {
	OK    bool      `json:"ok"`
	Items []Problem `json:"items"`
}

// NewProblemsView wraps a problem list. A nil list renders as an empty array,
// never as null, because the UI reads "nothing needs your attention" from
// exactly that.
func NewProblemsView(items []Problem) ProblemsView {
	if items == nil {
		items = []Problem{}
	}
	return ProblemsView{OK: len(items) == 0, Items: items}
}

// NewProblemsViewFor is NewProblemsView for one of the two audiences.
//
// OK is computed after the filter, not before it. A fleet whose own runners
// are fine, on an instance whose backups are failing, is a fleet with nothing
// to do: showing it "something needs your attention" above an empty drawer
// would send somebody looking for a problem they are not allowed to see.
func NewProblemsViewFor(items []Problem, platform bool) ProblemsView {
	kept := make([]Problem, 0, len(items))
	for _, p := range items {
		if p.Audience.For(platform) {
			kept = append(kept, p)
		}
	}
	return ProblemsView{OK: len(kept) == 0, Items: kept}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func poolNames(pools []*store.Pool) map[string]string {
	out := make(map[string]string, len(pools))
	for _, p := range pools {
		out[p.ID] = p.Name
	}
	return out
}

// emptySlice renders a nil slice as [] rather than null: a client should be
// able to iterate a list field without a nil check.
func emptySlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func emptyMap(in store.StringMap) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

func millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// ---------------------------------------------------------------------------
// Providers and machines
// ---------------------------------------------------------------------------

// ProviderView is one place machines are rented from, as the API returns it.
//
// It carries no credential and has no field one could be put in. What it says
// about the credential is whether there is one, which is the only thing a page
// needs: an audit row and a screenshot both outlive the person who took them.
type ProviderView struct {
	ID   string             `json:"id"`
	Kind store.ProviderKind `json:"kind"`
	Name string             `json:"name"`
	// Endpoint is where this controller reaches the provider. It is not a
	// secret -- it is the address an operator typed -- and showing it is how
	// somebody tells two clusters apart on a page listing both.
	Endpoint           string `json:"endpoint,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	// Connection is how the endpoint is reached: "direct" over the network,
	// or "tailcat" through a gateway beside the provider. The gateway's
	// address is sealed on the row and, like the credential, has no field
	// here: it is a lasting capability to open connections to the
	// hypervisor's API, and this view is on every page and in every audit row.
	Connection string            `json:"connection"`
	Settings   map[string]string `json:"settings"`
	// CredentialsConfigured says a credential is sealed in the row. The value
	// never leaves this process, so this is what the form renders instead.
	CredentialsConfigured bool `json:"credentials_configured"`

	MachineLabels   map[string]string `json:"machine_labels"`
	MachineCapacity int               `json:"machine_capacity"`
	MachineBackend  store.BackendKind `json:"machine_backend"`
	MachinePlatform store.Platform    `json:"machine_platform"`
	MachineCPUs     float64           `json:"machine_cpus,omitempty"`
	MachineMemoryMB int64             `json:"machine_memory_mb,omitempty"`
	MachineDiskMB   int64             `json:"machine_disk_mb,omitempty"`

	PoolSelector       map[string]string `json:"pool_selector"`
	MaxMachines        int               `json:"max_machines"`
	MaxCreatesInFlight int               `json:"max_creates_in_flight"`
	IdleTimeoutMS      int64             `json:"idle_timeout_ms,omitempty"`
	CostPerMachineHour float64           `json:"cost_per_machine_hour,omitempty"`
	Enabled            bool              `json:"enabled"`

	Paused       bool       `json:"paused"`
	PausedReason string     `json:"paused_reason,omitempty"`
	PausedUntil  *time.Time `json:"paused_until,omitempty"`
	// Held is why no new machine may be bought right now, in one sentence, or
	// empty when one may. It is computed rather than stored because it answers
	// for three switches at once -- the fence, the configuration and the row --
	// and a page that showed only the row's would say a fenced fleet was fine.
	Held                string     `json:"held,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures,omitempty"`
	LastCheckAt         *time.Time `json:"last_check_at,omitempty"`
	LastCheckError      string     `json:"last_check_error,omitempty"`
	LastSweepAt         *time.Time `json:"last_sweep_at,omitempty"`

	// Machines is how many this provider has, by state, so the card can show
	// the band without a second request.
	Machines map[string]int `json:"machines"`
	// Owned is how many still believe they have a resource, which is the
	// number the ceiling and the bill are both counted in.
	Owned     int       `json:"owned"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// providerConnection names how a provider is reached, from the one fact the
// row holds about it.
func providerConnection(p *store.Provider) string {
	if len(p.TailcatAddressEnc) > 0 {
		return "tailcat"
	}
	return "direct"
}

// ProviderView renders a provider with its machine counts and whatever is
// currently holding it back.
func (c *Controller) ProviderView(p *store.Provider, machines []*store.Machine) ProviderView {
	out := ProviderView{
		ID:                    p.ID,
		Kind:                  p.Kind,
		Name:                  p.Name,
		Endpoint:              p.Endpoint,
		InsecureSkipVerify:    p.InsecureSkipVerify,
		Connection:            providerConnection(p),
		Settings:              emptyMap(p.Settings),
		CredentialsConfigured: len(p.CredentialsEnc) > 0,
		MachineLabels:         emptyMap(p.MachineLabels),
		MachineCapacity:       p.MachineCapacity,
		MachineBackend:        p.MachineBackend,
		MachinePlatform:       p.MachinePlatform,
		MachineCPUs:           p.MachineCPUs,
		MachineMemoryMB:       p.MachineMemoryMB,
		MachineDiskMB:         p.MachineDiskMB,
		PoolSelector:          emptyMap(p.PoolSelector),
		MaxMachines:           p.MaxMachines,
		MaxCreatesInFlight:    p.MaxCreatesInFlight,
		IdleTimeoutMS:         millis(time.Duration(p.IdleTimeout)),
		CostPerMachineHour:    p.CostPerMachineHour,
		Enabled:               p.Enabled,
		Paused:                p.Paused,
		PausedReason:          p.PausedReason,
		PausedUntil:           p.PausedUntil,
		Held:                  c.provisioningHeld(p, c.Now()),
		ConsecutiveFailures:   p.ConsecutiveFailures,
		LastCheckAt:           p.LastCheckAt,
		LastCheckError:        p.LastCheckError,
		LastSweepAt:           p.LastSweepAt,
		Machines:              map[string]int{},
		CreatedAt:             p.CreatedAt,
		UpdatedAt:             p.UpdatedAt,
	}
	for _, m := range machines {
		if m == nil || m.ProviderID != p.ID {
			continue
		}
		out.Machines[string(m.State)]++
		if m.Owns() {
			out.Owned++
		}
	}
	return out
}

// MachineView is one rented machine, as the API returns it.
//
// The ownership fingerprint is deliberately absent from the JSON. It is not a
// secret, but it is the mark a delete is checked against, and putting it on a
// page invites somebody to write it onto a resource by hand.
type MachineView struct {
	ID           string             `json:"id"`
	ProviderID   string             `json:"provider_id"`
	ProviderName string             `json:"provider_name,omitempty"`
	Kind         store.ProviderKind `json:"provider_kind,omitempty"`
	Name         string             `json:"name"`
	State        store.MachineState `json:"state"`
	Message      string             `json:"message,omitempty"`
	PoolID       string             `json:"pool_id,omitempty"`
	PoolName     string             `json:"pool_name,omitempty"`

	ResourceZone string `json:"resource_zone,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	Address      string `json:"address,omitempty"`
	// OwnerFingerprint is carried on the type so the orphan page's server side
	// can compare, and never rendered.
	OwnerFingerprint    string     `json:"-"`
	OwnershipVerifiedAt *time.Time `json:"ownership_verified_at,omitempty"`
	OwnershipError      string     `json:"ownership_error,omitempty"`

	HostID   string            `json:"host_id,omitempty"`
	HostName string            `json:"host_name,omitempty"`
	Capacity int               `json:"capacity,omitempty"`
	Labels   map[string]string `json:"labels"`

	// Operation and OperationHandle are what an operator pastes into the
	// provider's own task log when they want to see the other half of a step
	// that is taking too long.
	Operation       store.MachineOpKind    `json:"operation,omitempty"`
	OperationID     string                 `json:"operation_id,omitempty"`
	OperationHandle string                 `json:"operation_handle,omitempty"`
	OperationHolder string                 `json:"operation_holder,omitempty"`
	OperationSince  *time.Time             `json:"operation_since,omitempty"`
	OutcomeUnknown  bool                   `json:"outcome_unknown,omitempty"`
	Attempts        int                    `json:"attempts,omitempty"`
	NextAttemptAt   *time.Time             `json:"next_attempt_at,omitempty"`
	ProviderError   string                 `json:"provider_error,omitempty"`
	BootstrapError  string                 `json:"bootstrap_error,omitempty"`
	SafeToDelete    bool                   `json:"safe_to_delete"`
	SafeToDeleteWhy string                 `json:"safe_to_delete_why,omitempty"`
	Timeline        []MachineTimelineEntry `json:"timeline"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	ReadyAt         *time.Time             `json:"ready_at,omitempty"`
	IdleSince       *time.Time             `json:"idle_since,omitempty"`
	DeletedAt       *time.Time             `json:"deleted_at,omitempty"`
}

// MachineTimelineEntry is one phase a machine has reached, so the detail page
// reads as a life rather than as a row of timestamps.
type MachineTimelineEntry struct {
	Phase string    `json:"phase"`
	At    time.Time `json:"at"`
}

// MachineRenderer renders a page of machines with their provider, pool and
// host names filled in, taking the lookups once rather than per row.
type MachineRenderer struct {
	providers map[string]*store.Provider
	pools     map[string]string
	hosts     map[string]string
	now       time.Time
	c         *Controller
}

// MachineRenderer gathers what the machine views need. A list endpoint would
// otherwise be N+1 in three directions at once.
func (c *Controller) MachineRenderer(ctx context.Context) (*MachineRenderer, error) {
	providers, err := c.st.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}
	r := &MachineRenderer{
		providers: make(map[string]*store.Provider, len(providers)),
		pools:     poolNames(pools),
		hosts:     make(map[string]string, len(hosts)),
		now:       c.Now(),
		c:         c,
	}
	for _, p := range providers {
		r.providers[p.ID] = p
	}
	for _, h := range hosts {
		r.hosts[h.ID] = h.Name
	}
	return r, nil
}

// View renders one machine.
func (r *MachineRenderer) View(m *store.Machine) MachineView {
	out := MachineView{
		ID:                  m.ID,
		ProviderID:          m.ProviderID,
		Name:                m.Name,
		State:               m.State,
		Message:             m.Message,
		PoolID:              m.PoolID,
		PoolName:            r.pools[m.PoolID],
		ResourceZone:        m.ResourceZone,
		ResourceID:          m.ResourceID,
		Address:             m.Address,
		OwnerFingerprint:    m.OwnerFingerprint,
		OwnershipVerifiedAt: m.OwnershipVerifiedAt,
		OwnershipError:      m.OwnershipError,
		HostID:              m.HostID,
		HostName:            r.hosts[m.HostID],
		Capacity:            m.Capacity,
		Labels:              emptyMap(m.Labels),
		Operation:           m.OpKind,
		OperationID:         m.OpID,
		OperationHandle:     m.OpHandle,
		OperationHolder:     m.OpHolder,
		OperationSince:      m.OpStartedAt,
		OutcomeUnknown:      m.OpOutcomeUnknown,
		Attempts:            m.Attempts,
		NextAttemptAt:       m.NextAttemptAt,
		ProviderError:       m.ProviderError,
		BootstrapError:      m.BootstrapError,
		Timeline:            machineTimeline(m),
		CreatedAt:           m.CreatedAt,
		UpdatedAt:           m.UpdatedAt,
		ReadyAt:             m.ReadyAt,
		IdleSince:           m.IdleSince,
		DeletedAt:           m.DeletedAt,
	}
	if p := r.providers[m.ProviderID]; p != nil {
		out.ProviderName, out.Kind = p.Name, p.Kind
	}
	out.SafeToDelete, out.SafeToDeleteWhy = machineSafeToDelete(m, r.now)
	return out
}

// machineSafeToDelete answers the question the orphan page asks before it
// offers a button, and says why when the answer is no.
//
// It is a reading of the row alone. The delete itself asks the provider again
// -- an observation older than a minute is not evidence -- so this is what an
// operator is shown rather than what the reconciler acts on.
func machineSafeToDelete(m *store.Machine, now time.Time) (bool, string) {
	switch {
	case m.DeletedAt != nil:
		return false, "this machine's resource has already been confirmed gone"
	case m.ResourceID == "":
		return false, "this machine never got as far as a resource, so there is nothing to delete"
	case m.OwnershipError != "":
		return false, m.OwnershipError
	case m.OwnershipVerifiedAt == nil:
		return false, "nothing has confirmed this resource is ours since the last check; the next ownership sweep will"
	case now.Sub(*m.OwnershipVerifiedAt) > observationMaxAge:
		return false, "the last time anything confirmed this resource is ours is too old to act on; the next ownership sweep will refresh it"
	}
	return true, ""
}

// machineTimeline is the phases this machine actually reached, in order. A
// phase it skipped -- a provider whose create leaves the guest running skips
// starting -- is absent rather than zero, because a zero timestamp in a
// timeline reads as 1970.
func machineTimeline(m *store.Machine) []MachineTimelineEntry {
	out := make([]MachineTimelineEntry, 0, 8)
	for _, e := range []struct {
		phase string
		at    *time.Time
	}{
		{"planned", &m.CreatedAt},
		{"creating", m.CreateStartedAt},
		{"created", m.CreatedOKAt},
		{"starting", m.StartedAt},
		{"bootstrapped", m.BootstrappedAt},
		{"enrolled", m.EnrolledAt},
		{"ready", m.ReadyAt},
		{"draining", m.DrainingAt},
		{"deleting", m.DeleteStartedAt},
		{"deleted", m.DeletedAt},
	} {
		if e.at == nil || e.at.IsZero() {
			continue
		}
		out = append(out, MachineTimelineEntry{Phase: e.phase, At: *e.at})
	}
	return out
}
