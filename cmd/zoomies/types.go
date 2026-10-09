package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// The shapes below are the parts of api/openapi.yaml the tables read. They are
// deliberately partial: a field this CLI does not render is a field it does not
// declare, and `--output json` passes the server's bytes through untouched, so
// nothing is lost by leaving one out.

// listResponse is the envelope every list route returns.
type listResponse[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	// Next is the cursor for the following page, where the listing has one.
	Next string `json:"next"`
}

type poolCounts struct {
	Provisioning int `json:"provisioning"`
	Registering  int `json:"registering"`
	Idle         int `json:"idle"`
	Busy         int `json:"busy"`
	Draining     int `json:"draining"`
	Failed       int `json:"failed"`
	Live         int `json:"live"`
}

type poolItem struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	InstallationID     string            `json:"installation_id"`
	InstallationTarget string            `json:"installation_target"`
	Labels             []string          `json:"labels"`
	RunnerGroup        string            `json:"runner_group"`
	Backend            string            `json:"backend"`
	Platform           platformItem      `json:"platform"`
	Image              string            `json:"image"`
	EffectiveImage     string            `json:"effective_image"`
	PullPolicy         string            `json:"pull_policy"`
	RunnerVersion      string            `json:"runner_version"`
	MinRunners         int               `json:"min_runners"`
	MaxRunners         int               `json:"max_runners"`
	Priority           int               `json:"priority"`
	IdleTimeout        string            `json:"idle_timeout"`
	Ephemeral          bool              `json:"ephemeral"`
	DockerMode         string            `json:"docker_mode"`
	HostSelector       map[string]string `json:"host_selector"`
	Env                map[string]string `json:"env"`
	RunAsRoot          bool              `json:"run_as_root"`
	NoDefaultLabels    bool              `json:"no_default_labels"`
	Enabled            bool              `json:"enabled"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	Counts             poolCounts        `json:"counts"`
	QueuedJobs         int               `json:"queued_jobs"`
	Utilisation        float64           `json:"utilisation"`
	Warnings           []problemItem     `json:"warnings"`
	// Resources is the size this pool asks for on every host, and Sizing says
	// whether it asks for one at all: "automatic" is a pool whose runners are
	// each given one slot's share of the machine they land on.
	Resources poolResources `json:"resources"`
	Sizing    string        `json:"sizing"`
	// SizeFromProfile is a pool that takes each runner's size from the host it
	// lands on, as that host's runner profile says; FleetStandard is the size a
	// runner is on a host whose profile names none.
	SizeFromProfile bool          `json:"size_from_profile"`
	FleetStandard   poolResources `json:"fleet_standard"`
	// CPUBurst is the pool's elastic CPU policy: whether a busy runner may be
	// lent the host's spare CPU above its guaranteed share.
	CPUBurst poolCPUBurst `json:"cpu_burst"`
	// MemoryBurst is the pool's memory valve policy: whether a runner may be
	// given more memory than it was created with, out of memory the host has not
	// promised to any runner, while its job runs.
	MemoryBurst poolMemoryBurst `json:"memory_burst"`
	// Tmpfs is which of the runner's folders the pool keeps in memory.
	Tmpfs poolTmpfs `json:"tmpfs"`
	// Auto is present on a pool the controller keeps from the hosts it has.
	Auto *poolAuto `json:"auto"`
}

// poolAuto is what is particular to a pool the controller keeps: what the
// operator asked of it, and what its hosts give it.
type poolAuto struct {
	Key     string   `json:"key"`
	Arch    string   `json:"arch"`
	Class   string   `json:"class"`
	Warm    int      `json:"warm"`
	Cap     int      `json:"cap"`
	Paused  bool     `json:"paused"`
	Kept    bool     `json:"kept"`
	Hosts   []string `json:"hosts"`
	Slots   int      `json:"slots"`
	Summary string   `json:"summary"`
}

// poolTmpfs mirrors the API's TmpfsConfig. Every field is zero on a pool that
// has never heard of it, which keeps both folders on disk.
type poolTmpfs struct {
	Work poolTmpfsMount `json:"work"`
	Tmp  poolTmpfsMount `json:"tmp"`
	// Daemon is the Docker-in-Docker sidecar's image store.
	Daemon poolTmpfsMount `json:"daemon"`
}

type poolTmpfsMount struct {
	Enabled bool  `json:"enabled"`
	Auto    bool  `json:"auto,omitempty"`
	SizeMB  int64 `json:"size_mb,omitempty"`
}

// poolCPUBurst mirrors the API's CPUBurstPolicy. An empty mode is off, which
// is what every pool created before the policy existed has.
type poolCPUBurst struct {
	Mode    string  `json:"mode"`
	MaxCPUs float64 `json:"max_cpus"`
	// SizeForCeiling is absent on a pool that has never said, which is on.
	SizeForCeiling *bool `json:"size_for_ceiling,omitempty"`
}

// poolMemoryBurst mirrors the API's MemoryBurstPolicy. An empty mode is off,
// which is what every pool created before the valve existed has.
type poolMemoryBurst struct {
	Mode        string `json:"mode"`
	MaxMemoryMB int64  `json:"max_memory_mb,omitempty"`
	SpillMB     int64  `json:"spill_mb,omitempty"`
}

// poolResources is the size a pool asks for per runner. Every field is zero on
// a pool that leaves the answer to its host.
type poolResources struct {
	CPUs      float64 `json:"cpus"`
	MemoryMB  int64   `json:"memory_mb"`
	DiskGB    int64   `json:"disk_gb"`
	PidsLimit int64   `json:"pids_limit"`
	// MinCPUs and MinMemoryMB are the pool's smallest runner. They are carried
	// because the API replaces `resources` whole: an edit that left them out
	// would clear them, and the pool would quietly follow the fleet's minimum.
	MinCPUs     float64 `json:"min_cpus"`
	MinMemoryMB int64   `json:"min_memory_mb"`
	// DaemonSharePercent is how much of a host-sized slot a Docker-in-Docker pool
	// gives its daemon; zero is the even split.
	DaemonSharePercent       int `json:"daemon_share_percent"`
	DaemonCPUSharePercent    int `json:"daemon_cpu_share_percent"`
	DaemonMemorySharePercent int `json:"daemon_memory_share_percent"`
}

// platformItem is the machine a pool needs, or the machine a host is.
type platformItem struct {
	OS        string `json:"os"`
	OSVersion string `json:"os_version"`
	Arch      string `json:"arch"`
}

// String renders the platform the way the UI does: "ubuntu 24.04, arm64", or
// "any" when the pool promises nothing.
func (p platformItem) String() string {
	var parts []string
	if p.OS != "" {
		os := p.OS
		if p.OSVersion != "" {
			os += " " + p.OSVersion
		}
		parts = append(parts, os)
	}
	if p.Arch != "" {
		parts = append(parts, p.Arch)
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, ", ")
}

type runnerItem struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	PoolID        string     `json:"pool_id"`
	PoolName      string     `json:"pool_name"`
	HostID        string     `json:"host_id"`
	HostName      string     `json:"host_name"`
	State         string     `json:"state"`
	ContainerID   string     `json:"container_id"`
	Ephemeral     bool       `json:"ephemeral"`
	Labels        []string   `json:"labels"`
	Image         string     `json:"image"`
	RunnerVersion string     `json:"runner_version"`
	CurrentJobID  string     `json:"current_job_id"`
	CurrentJob    *jobItem   `json:"current_job"`
	Message       string     `json:"message"`
	JobsHandled   int        `json:"jobs_handled"`
	CPUPercent    float64    `json:"cpu_percent"`
	MemoryBytes   int64      `json:"memory_bytes"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

type runnerDetail struct {
	runnerItem
	Host          *hostItem       `json:"host"`
	Timeline      []timelineEntry `json:"timeline"`
	LogsAvailable bool            `json:"logs_available"`
}

type timelineEntry struct {
	State      string    `json:"state"`
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	Message    string    `json:"message"`
}

type rerunResponse struct {
	Accepted    bool   `json:"accepted"`
	RunID       int64  `json:"run_id"`
	FaultDomain string `json:"fault_domain"`
}

type jobItem struct {
	ID          string   `json:"id"`
	GitHubRunID int64    `json:"github_run_id"`
	Repo        string   `json:"repo"`
	Workflow    string   `json:"workflow"`
	JobName     string   `json:"job_name"`
	Labels      []string `json:"labels"`
	State       string   `json:"state"`
	Conclusion  string   `json:"conclusion"`
	// Provisioning is what an operator has done to this job's demand: empty,
	// "paused" or "deleted". GitHub goes on calling a stood-down job queued,
	// because Zoomies cannot unqueue one, so without this the CLI listed a job
	// somebody had taken out of the queue as work still waiting to run.
	Provisioning string `json:"provisioning"`
	ProvisionNow bool   `json:"provision_now"`
	// CancelRequestedAt is when GitHub accepted a cancellation of this job's
	// workflow run. GitHub's completion delivery settles the conclusion and
	// can be minutes behind, and for that window the job reads queued or
	// in_progress while being neither.
	CancelRequestedAt *time.Time `json:"cancel_requested_at"`
	// InstallationID is the GitHub App installation covering this job's
	// repository. It is read to tell the two reasons a queued job goes
	// unclaimed apart: no pool advertises its labels, or nothing here holds a
	// credential for its target at all.
	InstallationID string     `json:"installation_id"`
	PoolName       string     `json:"pool_name"`
	RunnerName     string     `json:"runner_name"`
	HTMLURL        string     `json:"html_url"`
	Matched        bool       `json:"matched"`
	HeadBranch     string     `json:"head_branch"`
	HeadSHA        string     `json:"head_sha"`
	RunAttempt     int        `json:"run_attempt"`
	Steps          []jobStep  `json:"steps"`
	FailedStep     *jobStep   `json:"failed_step"`
	RunnerFault    string     `json:"runner_fault"`
	FaultKind      string     `json:"fault_kind"`
	FaultDomain    string     `json:"fault_domain"`
	FaultFix       string     `json:"fault_fix"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	QueueWaitMS    int64      `json:"queue_wait_ms"`
	DurationMS     int64      `json:"duration_ms"`

	ControllerVersion string `json:"controller_version"`
	ControllerChannel string `json:"controller_channel"`
	AgentVersion      string `json:"agent_version"`
	HostID            string `json:"host_id"`

	// How the controller classed the job, where it sent it and which class of
	// host took it; all empty on a job nobody classed.
	SizeClass      string   `json:"size_class"`
	SizeBasis      string   `json:"size_basis"`
	SizeReason     string   `json:"size_reason"`
	RoutedClass    string   `json:"routed_class"`
	RoutedNote     string   `json:"routed_note"`
	RanClass       string   `json:"ran_class"`
	ThrottledShare *float64 `json:"throttled_share"`
}

// sizePinItem is an operator's pin of a job, or a repository, to a size class.
type sizePinItem struct {
	Repo      string    `json:"repo"`
	Workflow  string    `json:"workflow"`
	JobName   string    `json:"job_name"`
	Class     string    `json:"class"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// sanitise makes what an operator typed safe to print. A pin's workflow and job
// are copied from a workflow file, whose author may not be the person at the
// terminal.
func (p *sizePinItem) sanitise() {
	p.Repo, p.Workflow, p.JobName, p.CreatedBy = plain(p.Repo), plain(p.Workflow), plain(p.JobName), plain(p.CreatedBy)
}

// sanitise makes every field a workflow author controls safe to print, where the
// response is decoded: a job's name and its workflow's are theirs, and so are the
// labels it asked for, which the message and the fix quote.
func (a *labelAdviceItem) sanitise() {
	a.Repo, a.Workflow, a.JobName = plain(a.Repo), plain(a.Workflow), plain(a.JobName)
	for i, l := range a.Labels {
		a.Labels[i] = plain(l)
	}
	a.Message, a.Fix = plain(a.Message), plain(a.Fix)
}

// labelAdviceItem is one thing to change in one job's runs-on.
type labelAdviceItem struct {
	Repo     string   `json:"repo"`
	Workflow string   `json:"workflow"`
	JobName  string   `json:"job_name"`
	Kind     string   `json:"kind"`
	Asked    string   `json:"asked"`
	Class    string   `json:"class"`
	Runs     int      `json:"runs"`
	Labels   []string `json:"labels"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix"`
}

// autoPoolsItem is GET /auto-pools: the state of size routing and of the pools
// the controller keeps.
type autoPoolsItem struct {
	SizeRouting  string `json:"size_routing"`
	AutoPools    string `json:"auto_pools"`
	Installation string `json:"installation"`
	Problem      string `json:"problem"`
	Pools        []struct {
		Key    string   `json:"key"`
		Name   string   `json:"name"`
		PoolID string   `json:"pool_id"`
		Hosts  []string `json:"hosts"`
		Slots  int      `json:"slots"`
	} `json:"pools"`
	Findings []struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
	} `json:"findings"`
	Skipped []struct {
		Host    string `json:"host"`
		Message string `json:"message"`
	} `json:"skipped"`
	Pending []struct {
		Kind  string `json:"kind"`
		Pool  string `json:"pool"`
		Cause string `json:"cause"`
	} `json:"pending"`
	Classes []struct {
		Class           string  `json:"class"`
		Label           string  `json:"label"`
		HostMaxCPUs     float64 `json:"host_max_cpus"`
		HostMaxMemoryMB int64   `json:"host_max_memory_mb"`
		RunnerCPUs      float64 `json:"runner_cpus"`
		RunnerMemoryMB  int64   `json:"runner_memory_mb"`
	} `json:"classes"`
	DefaultClass string `json:"default_class"`
}

type jobStep struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type jobEventItem struct {
	Kind       string    `json:"kind"`
	Source     string    `json:"source"`
	Message    string    `json:"message"`
	RunnerName string    `json:"runner_name"`
	At         time.Time `json:"at"`
}

// explanationItem is GET /jobs/{id}/explanation: why this job is where it is,
// worked out on the controller. The CLI renders it rather than reasoning for
// itself, so it and the web UI cannot give an operator two different answers.
type explanationItem struct {
	Summary          string          `json:"summary"`
	Detail           string          `json:"detail"`
	Fix              string          `json:"fix"`
	Waiting          bool            `json:"waiting"`
	Blocked          bool            `json:"blocked"`
	Class            string          `json:"class"`
	Confidence       string          `json:"confidence"`
	ConfidenceReason string          `json:"confidence_reason"`
	Evidence         []evidenceItem  `json:"evidence"`
	LogExcerpt       *logExcerptItem `json:"log_excerpt"`
	ProblemCode      string          `json:"problem_code"`
	NextSteps        []nextStepItem  `json:"next_steps"`
}

type evidenceItem struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
	Ref   string `json:"ref"`
}

type logExcerptItem struct {
	Lines []logLineItem `json:"lines"`
	Note  string        `json:"note"`
}

type logLineItem struct {
	N        int    `json:"n"`
	Text     string `json:"text"`
	Decisive bool   `json:"decisive"`
}

type nextStepItem struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
	Link string `json:"link"`
}

type backendInfo struct {
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Rootless  bool   `json:"rootless"`
	Endpoint  string `json:"endpoint"`
	Detail    string `json:"detail"`
}

type hostItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Address       string `json:"address"`
	Embedded      bool   `json:"embedded"`
	Capacity      int    `json:"capacity"`
	ActiveRunners int    `json:"active_runners"`
	Free          int    `json:"free"`
	// EffectiveCapacity is the slots the host takes right now, which is
	// Capacity stepped down by a throttle; ThrottleReason is the sentence
	// explaining the throttle, empty when there is none. A controller older
	// than either sends neither, and zero here is read as "not throttled".
	EffectiveCapacity int               `json:"effective_capacity"`
	ThrottleReason    string            `json:"throttle_reason"`
	Backends          []string          `json:"backends"`
	BackendInfo       []backendInfo     `json:"backend_info"`
	Labels            map[string]string `json:"labels"`
	OS                string            `json:"os"`
	Distro            string            `json:"distro"`
	OSVersion         string            `json:"os_version"`
	Arch              string            `json:"arch"`
	CPUs              int               `json:"cpus"`
	MemoryMB          int64             `json:"memory_mb"`
	Platform          platformItem      `json:"platform"`
	PlatformLabel     string            `json:"platform_label"`
	CanonicalName     string            `json:"canonical_name"`
	Version           string            `json:"version"`
	Cordoned          bool              `json:"cordoned"`
	Healthy           bool              `json:"healthy"`
	LastHeartbeat     time.Time         `json:"last_heartbeat"`
	CreatedAt         time.Time         `json:"created_at"`

	// Doctor is the host's report on its own operating system, with the
	// controller's count of it. A host that has sent no report, and a controller
	// older than the field, send none, and the list then says nothing about
	// health rather than guessing.
	Doctor *hostDoctor `json:"doctor"`

	// Slots is what the host takes before any throttle -- its capacity, or what
	// its machine holds of its standard runner size -- and SlotsLimitedBy says
	// what sets it. RunnerProfile is what the operator wrote, nil when nothing,
	// and EffectiveProfile what is in force with the fleet's figures standing in
	// for the rest. A controller older than the fields sends none of them.
	Slots          int            `json:"slots"`
	SlotsLimitedBy string         `json:"slots_limited_by"`
	RunnerProfile  *runnerProfile `json:"runner_profile"`
	// Tags, SizeClass and AutoPool are what the host says about its place in the
	// size classes: its tags with which are the controller's, the class it is in
	// and why, and the automatic pool it counts towards or the reason it counts
	// towards none. A controller older than the fields sends none of them.
	Tags             []hostTag      `json:"tags"`
	SizeClass        *hostSizeClass `json:"size_class"`
	AutoPool         *hostAutoPool  `json:"auto_pool"`
	EffectiveProfile struct {
		Standard struct {
			CPUs           float64 `json:"cpus"`
			MemoryMB       int64   `json:"memory_mb"`
			CPUsSource     string  `json:"cpus_source"`
			MemoryMBSource string  `json:"memory_mb_source"`
		} `json:"standard"`
	} `json:"effective_profile"`
}

// hostTag is one thing a pool's host selector can ask of a host. Source is
// "operator" for a label stored on it and "automatic" for one the controller
// derives from the machine.
type hostTag struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Source    string `json:"source"`
	Overrides string `json:"overrides"`
}

type hostSizeClass struct {
	Class    string `json:"class"`
	Source   string `json:"source"`
	Measured string `json:"measured"`
	Pending  string `json:"pending"`
	Reason   string `json:"reason"`
}

type hostAutoPool struct {
	Counted    bool   `json:"counted"`
	Pool       string `json:"pool"`
	Reason     string `json:"reason"`
	ReasonCode string `json:"reason_code"`
}

// hostDoctor is the part of a host's OS report the list reads: when it was
// taken, whether it is only the container's view, whether a reboot is due, and
// the controller's own count. The report's results, distribution and work
// folder are deliberately not decoded: the host's agent wrote them, they are not
// something a table that cannot wrap or truncate could hold, and `--output json`
// carries them for whoever wants them.
type hostDoctor struct {
	CheckedAt     time.Time          `json:"checked_at"`
	Container     bool               `json:"container"`
	RebootPending bool               `json:"reboot_pending"`
	Summary       *hostDoctorSummary `json:"summary"` // nil from a controller older than the field
}

// hostDoctorSummary is the controller's count of a report: the same six
// numbers the host's page, the problems and the metrics read. Suggestions are
// not decoded because they never reach a cell. Accepted does: a warning an
// operator has accepted is silent everywhere else, so the table says so.
type hostDoctorSummary struct {
	Counted  int `json:"counted"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
	Skipped  int `json:"skipped"`
	Accepted int `json:"accepted"`
}

// runnerProfile is a host's runner profile as the API writes and reads it.
type runnerProfile struct {
	Minimum struct {
		CPUs     float64 `json:"cpus,omitempty"`
		MemoryMB int64   `json:"memory_mb,omitempty"`
	} `json:"minimum"`
	Standard struct {
		CPUs         float64 `json:"cpus,omitempty"`
		MemoryMB     int64   `json:"memory_mb,omitempty"`
		BurstMaxCPUs float64 `json:"burst_max_cpus,omitempty"`
		// BurstMaxMemoryMB is the most memory one runner here may hold, lent
		// memory included.
		BurstMaxMemoryMB int64 `json:"burst_max_memory_mb,omitempty"`
	} `json:"standard"`
	// Tmpfs is the host's say over pools' in-memory folders.
	Tmpfs struct {
		Disabled bool  `json:"disabled,omitempty"`
		MaxMB    int64 `json:"max_mb,omitempty"`
		WorkMB   int64 `json:"work_mb,omitempty"`
		TmpMB    int64 `json:"tmp_mb,omitempty"`
		DaemonMB int64 `json:"daemon_mb,omitempty"`
	} `json:"tmpfs"`
}

type joinTokenItem struct {
	ID        string            `json:"id"`
	Prefix    string            `json:"prefix"`
	Capacity  int               `json:"capacity"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Usable    bool              `json:"usable"`
	// Token and Command come back exactly once, from the create call.
	Token   string `json:"token"`
	Command string `json:"command"`
}

type installationItem struct {
	ID             string     `json:"id"`
	AppID          int64      `json:"app_id"`
	InstallationID int64      `json:"installation_id"`
	Target         string     `json:"target"`
	TargetType     string     `json:"target_type"`
	APIBaseURL     string     `json:"api_base_url"`
	AppSlug        string     `json:"app_slug"`
	Enterprise     bool       `json:"enterprise"`
	Healthy        bool       `json:"healthy"`
	LastError      string     `json:"last_error"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`
	PoolCount      int        `json:"pool_count"`
	CreatedAt      time.Time  `json:"created_at"`
}

type installationHealth struct {
	OK                 bool              `json:"ok"`
	AppSlug            string            `json:"app_slug"`
	AppName            string            `json:"app_name"`
	Message            string            `json:"message"`
	Permissions        map[string]string `json:"permissions"`
	Events             []string          `json:"events"`
	MissingPermissions []string          `json:"missing_permissions"`
	MissingEvents      []string          `json:"missing_events"`
	RateLimitRemaining int               `json:"rate_limit_remaining"`
}

type auditItem struct {
	ID         string    `json:"id"`
	ActorName  string    `json:"actor_name"`
	ActorKind  string    `json:"actor_kind"`
	Action     string    `json:"action"`
	TargetKind string    `json:"target_kind"`
	TargetID   string    `json:"target_id"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
}

type userItem struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	Email              string     `json:"email"`
	DisplayName        string     `json:"display_name"`
	Role               string     `json:"role"`
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	LastLoginAt        *time.Time `json:"last_login_at"`
}

type tokenItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	Scopes     []string   `json:"scopes"`
	Prefix     string     `json:"prefix"`
	Revoked    bool       `json:"revoked"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// Token comes back exactly once, from the create call.
	Token string `json:"token"`
}

// providerItem is one place machines are rented from. The credential is not
// here because it is not in the response: the server reports whether one is
// configured and never what it is.
type providerItem struct {
	ID                    string            `json:"id"`
	Kind                  string            `json:"kind"`
	Name                  string            `json:"name"`
	Endpoint              string            `json:"endpoint"`
	Settings              map[string]string `json:"settings"`
	CredentialsConfigured bool              `json:"credentials_configured"`
	MaxMachines           int               `json:"max_machines"`
	Enabled               bool              `json:"enabled"`
	Paused                bool              `json:"paused"`
	PausedReason          string            `json:"paused_reason"`
	Held                  string            `json:"held"`
	LastCheckAt           *time.Time        `json:"last_check_at"`
	LastCheckError        string            `json:"last_check_error"`
	LastSweepAt           *time.Time        `json:"last_sweep_at"`
	Machines              map[string]int    `json:"machines"`
	Owned                 int               `json:"owned"`
}

// providerCheckItem is a preflight result. Its findings are config.Finding so
// that printFindings renders them exactly as it renders a configuration's.
type providerCheckItem struct {
	ProviderID string          `json:"provider_id"`
	OK         bool            `json:"ok"`
	Reachable  bool            `json:"reachable"`
	Version    string          `json:"version"`
	Findings   config.Findings `json:"findings"`
}

// providerValidation is the dry run's verdict: every field that is wrong, and
// the driver's own warnings about answers that are legal and still cost
// something.
type providerValidation struct {
	Valid  bool `json:"valid"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
	Warnings config.Findings `json:"warnings"`
}

// providerKindItem is one driver this build ships and the questions its form
// asks, which is what --setting needs a list of.
type providerKindItem struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Settings []struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		Kind     string `json:"kind"`
		Required bool   `json:"required"`
		Advanced bool   `json:"advanced"`
		Help     string `json:"help"`
		Default  string `json:"default"`
	} `json:"settings"`
}

// machineItem is one rented machine.
type machineItem struct {
	ID              string    `json:"id"`
	ProviderID      string    `json:"provider_id"`
	ProviderName    string    `json:"provider_name"`
	Name            string    `json:"name"`
	State           string    `json:"state"`
	Message         string    `json:"message"`
	PoolName        string    `json:"pool_name"`
	ResourceZone    string    `json:"resource_zone"`
	ResourceID      string    `json:"resource_id"`
	Address         string    `json:"address"`
	HostName        string    `json:"host_name"`
	OwnershipError  string    `json:"ownership_error"`
	Operation       string    `json:"operation"`
	OperationHandle string    `json:"operation_handle"`
	ProviderError   string    `json:"provider_error"`
	BootstrapError  string    `json:"bootstrap_error"`
	SafeToDeleteWhy string    `json:"safe_to_delete_why"`
	CreatedAt       time.Time `json:"created_at"`
}

// orphanReport is the review page: the three ways a row and a resource can
// fail to agree.
type orphanReport struct {
	ProviderID   string        `json:"provider_id"`
	ProviderName string        `json:"provider_name"`
	LastSweepAt  *time.Time    `json:"last_sweep_at"`
	Untracked    []orphanItem  `json:"untracked"`
	NoResource   []machineItem `json:"no_resource"`
	Unverified   []machineItem `json:"unverified"`
}

type orphanItem struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

type problemItem struct {
	Code       string    `json:"code"`
	Severity   string    `json:"severity"`
	Setting    string    `json:"setting"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	Fix        string    `json:"fix"`
	TargetKind string    `json:"target_kind"`
	TargetID   string    `json:"target_id"`
	Since      time.Time `json:"since"`
	// Remedy is the change the controller proposes for it, when it has priced one.
	Remedy *problemRemedy `json:"remedy"`
}

// problemRemedy is a change a problem proposes; see `zoomies problems apply`.
type problemRemedy struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Effect   string          `json:"effect"`
	Kind     string          `json:"kind"`
	TargetID string          `json:"target_id"`
	Body     json.RawMessage `json:"body"`
}

// sanitise makes a problem safe to print where the response is decoded. Most of a
// problem is the controller's own words, but a few -- jobs that matched nothing, a
// runner that was lost, a job killed for memory -- quote the name and the labels a
// workflow author wrote, and `zoomies status` is what an operator reads in the middle
// of an incident on exactly those.
func (p *problemItem) sanitise() {
	p.Setting, p.Title, p.Detail, p.Fix = plain(p.Setting), plain(p.Title), plain(p.Detail), plain(p.Fix)
	if p.Remedy != nil {
		p.Remedy.Label, p.Remedy.Effect = plain(p.Remedy.Label), plain(p.Remedy.Effect)
	}
}

func (r *problemsResponse) sanitise() {
	for i := range r.Items {
		r.Items[i].sanitise()
	}
}

type problemsResponse struct {
	OK    bool          `json:"ok"`
	Items []problemItem `json:"items"`
}

type scalingItem struct {
	ID        string    `json:"id"`
	PoolID    string    `json:"pool_id"`
	PoolName  string    `json:"pool_name"`
	From      int       `json:"from"`
	To        int       `json:"to"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

type statsResponse struct {
	Window       string `json:"window"`
	QueuedJobs   int    `json:"queued_jobs"`
	RunningJobs  int    `json:"running_jobs"`
	Completed    int    `json:"completed"`
	Succeeded    int    `json:"succeeded"`
	Failed       int    `json:"failed"`
	Cancelled    int    `json:"cancelled"`
	Unknown      int    `json:"unknown"`
	MedianWaitMS int64  `json:"median_wait_ms"`
	P95WaitMS    int64  `json:"p95_wait_ms"`
	Runners      struct {
		Provisioning int `json:"provisioning"`
		Registering  int `json:"registering"`
		Idle         int `json:"idle"`
		Busy         int `json:"busy"`
		Draining     int `json:"draining"`
		Failed       int `json:"failed"`
		Total        int `json:"total"`
	} `json:"runners"`
	Hosts struct {
		Total    int `json:"total"`
		Healthy  int `json:"healthy"`
		Cordoned int `json:"cordoned"`
		Capacity int `json:"capacity"`
		Used     int `json:"used"`
	} `json:"hosts"`
	Pools []struct {
		PoolID      string  `json:"pool_id"`
		PoolName    string  `json:"pool_name"`
		Min         int     `json:"min"`
		Max         int     `json:"max"`
		Live        int     `json:"live"`
		Busy        int     `json:"busy"`
		Idle        int     `json:"idle"`
		Queued      int     `json:"queued"`
		Utilisation float64 `json:"utilisation"`
	} `json:"pools"`
}

type metaResponse struct {
	Version           string `json:"version"`
	BootstrapRequired bool   `json:"bootstrap_required"`
	AuthDisabled      bool   `json:"auth_disabled"`
	ExternalURL       string `json:"external_url"`
	WebhookURL        string `json:"webhook_url"`
	PollingOnly       bool   `json:"polling_only"`
}

// bundleResponse is GET /diagnostics/bundle, decoded only as far as the
// summary needs.
//
// The document itself is written to the file byte for byte from the server's
// response, so a section added to the bundle tomorrow reaches a support case
// today; this type exists so the terminal can say what went in it and what did
// not, which is the one thing an operator has to check before attaching it.
type bundleResponse struct {
	BundleVersion int       `json:"bundle_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Instance      struct {
		Version    string `json:"version"`
		OS         string `json:"os"`
		Arch       string `json:"arch"`
		Goroutines int    `json:"goroutines"`
	} `json:"instance"`
	Findings      []problemItem     `json:"findings"`
	Problems      problemsResponse  `json:"problems"`
	Installations []json.RawMessage `json:"installations"`
	Pools         []json.RawMessage `json:"pools"`
	Hosts         []json.RawMessage `json:"hosts"`
	Runners       []json.RawMessage `json:"runners"`
	Jobs          []json.RawMessage `json:"jobs"`
	Explanations  []json.RawMessage `json:"explanations"`
	ScalingEvents []json.RawMessage `json:"scaling_events"`
	Logs          struct {
		Runners []json.RawMessage `json:"runners"`
	} `json:"logs"`
	Errors []struct {
		Section string `json:"section"`
		Error   string `json:"error"`
	} `json:"errors"`
	Truncated []struct {
		Section string `json:"section"`
		Kept    int    `json:"kept"`
		Reason  string `json:"reason"`
	} `json:"truncated"`
}

// updatesStatus is GET /updates, which is also what POST /updates/check and
// POST /updates/controller answer with.
type updatesStatus struct {
	Mode    string `json:"mode"`
	Soak    string `json:"soak"`
	Running struct {
		Version string `json:"version"`
		Release bool   `json:"release"`
	} `json:"running"`
	Latest *struct {
		Tag         string    `json:"tag"`
		PublishedAt time.Time `json:"published_at"`
	} `json:"latest"`
	Target *struct {
		Tag   string     `json:"tag"`
		Newer bool       `json:"newer"`
		DueAt *time.Time `json:"due_at"`
	} `json:"target"`
	Reason    string     `json:"reason"`
	CheckedAt *time.Time `json:"checked_at"`
	Helper    struct {
		State          string `json:"state"`
		Reason         string `json:"reason"`
		InstallCommand string `json:"install_command"`
	} `json:"helper"`
	Controller *updatesAttempt `json:"controller"`
}

// updatesAttempt is the controller's own update attempt, open or ended.
type updatesAttempt struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Trigger     string     `json:"trigger"`
	RequestedAt time.Time  `json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Error       string     `json:"error"`
}
