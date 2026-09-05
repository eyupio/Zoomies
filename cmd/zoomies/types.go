package main

import "time"

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
	Image              string            `json:"image"`
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
	Enabled            bool              `json:"enabled"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	Counts             poolCounts        `json:"counts"`
	QueuedJobs         int               `json:"queued_jobs"`
	Utilisation        float64           `json:"utilisation"`
	Warnings           []problemItem     `json:"warnings"`
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

type jobItem struct {
	ID          string     `json:"id"`
	GitHubRunID int64      `json:"github_run_id"`
	Repo        string     `json:"repo"`
	Workflow    string     `json:"workflow"`
	JobName     string     `json:"job_name"`
	Labels      []string   `json:"labels"`
	State       string     `json:"state"`
	Conclusion  string     `json:"conclusion"`
	PoolName    string     `json:"pool_name"`
	RunnerName  string     `json:"runner_name"`
	HTMLURL     string     `json:"html_url"`
	Matched     bool       `json:"matched"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	QueueWaitMS int64      `json:"queue_wait_ms"`
	DurationMS  int64      `json:"duration_ms"`
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
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Address       string            `json:"address"`
	Embedded      bool              `json:"embedded"`
	Capacity      int               `json:"capacity"`
	ActiveRunners int               `json:"active_runners"`
	Free          int               `json:"free"`
	Backends      []string          `json:"backends"`
	BackendInfo   []backendInfo     `json:"backend_info"`
	Labels        map[string]string `json:"labels"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Version       string            `json:"version"`
	Cordoned      bool              `json:"cordoned"`
	Healthy       bool              `json:"healthy"`
	LastHeartbeat time.Time         `json:"last_heartbeat"`
	CreatedAt     time.Time         `json:"created_at"`
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
	Failed       int    `json:"failed"`
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
