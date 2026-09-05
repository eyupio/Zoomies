package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
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
}

// HostView is one agent host and the room it has left.
type HostView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Address       string            `json:"address,omitempty"`
	Embedded      bool              `json:"embedded"`
	Capacity      int               `json:"capacity"`
	ActiveRunners int               `json:"active_runners"`
	Free          int               `json:"free"`
	Backends      []string          `json:"backends"`
	BackendInfo   []BackendInfoView `json:"backend_info"`
	Labels        map[string]string `json:"labels"`
	OS            string            `json:"os,omitempty"`
	Arch          string            `json:"arch,omitempty"`
	Version       string            `json:"version,omitempty"`
	Cordoned      bool              `json:"cordoned"`
	Healthy       bool              `json:"healthy"`
	LastHeartbeat time.Time         `json:"last_heartbeat"`
	CreatedAt     time.Time         `json:"created_at"`
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
		ID:            h.ID,
		Name:          h.Name,
		Address:       h.Address,
		Embedded:      h.Embedded,
		Capacity:      h.Capacity,
		ActiveRunners: h.ActiveRunners,
		Free:          h.Free(),
		Backends:      emptySlice(h.Backends),
		Labels:        emptyMap(h.Labels),
		OS:            h.OS,
		Arch:          h.Arch,
		Version:       h.Version,
		Cordoned:      h.Cordoned,
		Healthy:       h.Healthy(c.Now()),
		LastHeartbeat: h.LastHeartbeat,
		CreatedAt:     h.CreatedAt,
	}
	if len(h.BackendInfo) > 0 {
		out.BackendInfo = make([]BackendInfoView, 0, len(h.BackendInfo))
		for _, b := range h.BackendInfo {
			out.BackendInfo = append(out.BackendInfo, BackendInfoView{
				Kind:      b.Kind,
				Available: b.Available,
				Version:   b.Version,
				Rootless:  b.Rootless,
				Endpoint:  b.Endpoint,
				Detail:    b.Detail,
				DinD:      b.SupportsDinD,
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
	ID          string         `json:"id"`
	GitHubJobID int64          `json:"github_job_id"`
	GitHubRunID int64          `json:"github_run_id"`
	Repo        string         `json:"repo"`
	Workflow    string         `json:"workflow"`
	JobName     string         `json:"job_name"`
	Labels      []string       `json:"labels"`
	State       store.JobState `json:"state"`
	Conclusion  string         `json:"conclusion,omitempty"`
	PoolID      string         `json:"pool_id,omitempty"`
	PoolName    string         `json:"pool_name,omitempty"`
	RunnerID    string         `json:"runner_id,omitempty"`
	RunnerName  string         `json:"runner_name,omitempty"`
	HTMLURL     string         `json:"html_url,omitempty"`
	Matched     bool           `json:"matched"`
	QueuedAt    time.Time      `json:"queued_at"`
	StartedAt   *time.Time     `json:"started_at"`
	CompletedAt *time.Time     `json:"completed_at"`
	QueueWaitMS int64          `json:"queue_wait_ms"`
	DurationMS  int64          `json:"duration_ms"`
}

// NewJobView renders a job, given the name of the pool that claimed it.
func NewJobView(j *store.Job, poolName string) JobView {
	return JobView{
		ID:          j.ID,
		GitHubJobID: j.GitHubJobID,
		GitHubRunID: j.GitHubRunID,
		Repo:        j.Repo,
		Workflow:    j.Workflow,
		JobName:     j.JobName,
		Labels:      emptySlice(j.Labels),
		State:       j.State,
		Conclusion:  j.Conclusion,
		PoolID:      j.PoolID,
		PoolName:    poolName,
		RunnerID:    j.RunnerID,
		RunnerName:  j.RunnerName,
		HTMLURL:     j.HTMLURL,
		Matched:     j.Matched,
		QueuedAt:    j.QueuedAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		QueueWaitMS: millis(j.QueueWait()),
		DurationMS:  millis(j.Duration()),
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
// Runners
// ---------------------------------------------------------------------------

// RunnerView is one runner, with the pool and host named rather than only
// referenced: a runner grid that shows two opaque IDs per row is a grid nobody
// can read.
type RunnerView struct {
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
	RunnerVersion  string            `json:"runner_version,omitempty"`
	CurrentJobID   string            `json:"current_job_id,omitempty"`
	CurrentJob     *JobView          `json:"current_job,omitempty"`
	Message        string            `json:"message,omitempty"`
	JobsHandled    int               `json:"jobs_handled"`
	CPUPercent     float64           `json:"cpu_percent,omitempty"`
	MemoryBytes    int64             `json:"memory_bytes,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	StartedAt      *time.Time        `json:"started_at"`
	LastIdleAt     *time.Time        `json:"last_idle_at"`
	FinishedAt     *time.Time        `json:"finished_at"`
}

// RunnerRenderer names pools and hosts without a query per runner.
type RunnerRenderer struct {
	pools map[string]string
	hosts map[string]string
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
	v := &RunnerRenderer{pools: poolNames(pools), hosts: map[string]string{}, jobs: map[string]*store.Job{}}
	for _, h := range hosts {
		v.hosts[h.ID] = h.Name
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
	out := RunnerView{
		ID:             r.ID,
		Name:           r.Name,
		PoolID:         r.PoolID,
		PoolName:       v.pools[r.PoolID],
		HostID:         r.HostID,
		HostName:       v.hosts[r.HostID],
		State:          r.State,
		GitHubRunnerID: r.GitHubRunnerID,
		ContainerID:    r.ContainerID,
		Ephemeral:      r.Ephemeral,
		Labels:         emptySlice(r.Labels),
		Image:          r.Image,
		RunnerVersion:  r.RunnerVersion,
		CurrentJobID:   r.CurrentJobID,
		Message:        r.Message,
		JobsHandled:    r.JobsHandled,
		CPUPercent:     r.CPUPercent,
		MemoryBytes:    r.MemoryBytes,
		CreatedAt:      r.CreatedAt,
		StartedAt:      r.StartedAt,
		LastIdleAt:     r.LastIdleAt,
		FinishedAt:     r.FinishedAt,
	}
	if j := v.jobs[r.CurrentJobID]; j != nil {
		job := NewJobView(j, v.pools[j.PoolID])
		out.CurrentJob = &job
	}
	return out
}

// runnerView renders a single runner for the event stream: three point reads
// rather than two list queries, because a reconcile pass publishes one event
// per runner it touched and the fleet may be large.
func (c *Controller) runnerView(ctx context.Context, r *store.Runner) RunnerView {
	v := &RunnerRenderer{pools: map[string]string{}, hosts: map[string]string{}, jobs: map[string]*store.Job{}}
	if p, err := c.st.GetPool(ctx, r.PoolID); err == nil {
		v.pools[p.ID] = p.Name
	}
	if h, err := c.st.GetHost(ctx, r.HostID); err == nil {
		v.hosts[h.ID] = h.Name
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
	Image              string            `json:"image"`
	RunnerVersion      string            `json:"runner_version,omitempty"`
	MinRunners         int               `json:"min_runners"`
	MaxRunners         int               `json:"max_runners"`
	IdleTimeout        store.Duration    `json:"idle_timeout"`
	Ephemeral          bool              `json:"ephemeral"`
	DockerMode         store.DockerMode  `json:"docker_mode"`
	Resources          store.Resources   `json:"resources"`
	Cache              store.CacheConfig `json:"cache"`
	HostSelector       map[string]string `json:"host_selector"`
	Env                map[string]string `json:"env"`
	RunAsRoot          bool              `json:"run_as_root"`
	Enabled            bool              `json:"enabled"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	Counts             PoolCountsView    `json:"counts"`
	QueuedJobs         int               `json:"queued_jobs"`
	Utilisation        float64           `json:"utilisation"`
	Warnings           []Problem         `json:"warnings,omitempty"`
}

// PoolRenderer is everything needed to render pools without one query per
// pool.
type PoolRenderer struct {
	counts  map[string]store.PoolCounts
	targets map[string]string
	queued  map[string]int
	// blocked holds, per pool, the scheduler's reason for not placing the
	// runners that pool wanted. It is the answer to the question the pool page
	// is opened to ask.
	blocked map[string][]Problem
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
	targets := make(map[string]string, len(insts))
	for _, i := range insts {
		targets[i.ID] = i.Target
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
	return &PoolRenderer{counts: counts, targets: targets, queued: queued, blocked: blocked}, nil
}

// View renders one pool.
func (v *PoolRenderer) View(p *store.Pool) PoolView {
	cnt := v.counts[p.ID]
	return PoolView{
		ID:                 p.ID,
		Name:               p.Name,
		InstallationID:     p.InstallationID,
		InstallationTarget: v.targets[p.InstallationID],
		Labels:             emptySlice(p.Labels),
		RunnerGroup:        p.RunnerGroup,
		Backend:            p.Backend,
		Image:              p.Image,
		RunnerVersion:      p.RunnerVersion,
		MinRunners:         p.MinRunners,
		MaxRunners:         p.MaxRunners,
		IdleTimeout:        p.IdleTimeout,
		Ephemeral:          p.Ephemeral,
		DockerMode:         p.DockerMode,
		Resources:          p.Resources,
		Cache:              p.Cache,
		HostSelector:       emptyMap(p.HostSelector),
		Env:                emptyMap(p.Env),
		RunAsRoot:          p.RunAsRoot,
		Enabled:            p.Enabled,
		CreatedAt:          p.CreatedAt,
		UpdatedAt:          p.UpdatedAt,
		Counts: PoolCountsView{
			Provisioning: cnt.Provisioning, Registering: cnt.Registering,
			Idle: cnt.Idle, Busy: cnt.Busy, Draining: cnt.Draining, Failed: cnt.Failed,
			Live: cnt.Live(),
		},
		QueuedJobs:  v.queued[p.ID],
		Utilisation: cnt.Utilisation(),
		Warnings:    append(PoolWarnings(p), v.blocked[p.ID]...),
	}
}

// PoolWarnings renders a pool's dangerous settings as problems.
//
// They are the same sentences the UI's problems drawer shows, because an
// operator should not have to learn that "host-socket" on the pool page and
// "any job on this pool can become root on the host" on the Overview are the
// same fact.
func PoolWarnings(p *store.Pool) []Problem {
	dangers := p.Dangerous()
	if len(dangers) == 0 {
		return nil
	}
	out := make([]Problem, 0, len(dangers))
	for _, d := range dangers {
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
	return out
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
