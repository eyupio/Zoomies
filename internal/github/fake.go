package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// FakeGitHub is an in-memory stand-in for the GitHub REST API.
//
// It lives in the production package rather than a _test.go file on purpose:
// the controller, scheduler and API tests all need a GitHub to run the whole
// system against, and a second, subtly different fake in each of them would be
// worse than one honest fake here. "Honest" means it enforces the rules that
// actually bite -- runner names must be unique, a deleted runner is gone, a
// job only appears in a run's job list -- so a test that passes against it has
// some claim to passing against GitHub.
//
// It serves both the github.com layout (/app) and the GitHub Enterprise Server
// layout (/api/v3/app), so the same fake exercises both code paths.
//
// Pagination is not implemented: every list returns one page. Tests that need
// to exercise paging should drive appClient against their own handler.
type FakeGitHub struct {
	srv *httptest.Server

	mu             sync.Mutex
	appID          int64
	installationID int64
	appSlug        string
	appName        string
	appOwner       string
	permissions    map[string]string
	events         []string

	nextRunnerID int64
	runners      []*Runner

	nextJobID int64
	nextRunID int64
	jobs      []*fakeJob
	repos     []string
	// contents holds what the migration surface reads and writes: files,
	// branches and pull requests, per repository. It fills in lazily, so a
	// test that never migrates anything pays nothing for it.
	contents map[string]*fakeRepo

	groups []RunnerGroup

	rateLimit RateLimit

	failures []fakeFailure
	requests []string
}

// fakeJob is one queued/running/finished job. Every job gets its own workflow
// run, which is the common shape and keeps the run listing meaningful.
type fakeJob struct {
	QueuedJob
	runStatus string
}

// fakeFailure makes matching requests fail, so tests can exercise the error
// paths without a network.
type fakeFailure struct {
	pattern string
	status  int
	message string
}

// NewFake starts a fake GitHub and returns it. The caller must Close it.
func NewFake() *FakeGitHub {
	f := &FakeGitHub{
		appID:          12345,
		installationID: 42,
		appSlug:        "zoomies-fake",
		appName:        "Zoomies Fake",
		appOwner:       "acme",
		permissions: map[string]string{
			"actions":                          "read",
			"metadata":                         "read",
			"administration":                   "write",
			"organization_self_hosted_runners": "write",
		},
		events:       []string{"workflow_job"},
		nextRunnerID: 1,
		nextJobID:    1000,
		nextRunID:    5000,
		groups:       []RunnerGroup{{ID: 1, Name: "Default"}},
		rateLimit:    RateLimit{Limit: 5000, Remaining: 4999, ResetAt: time.Now().Add(time.Hour).UTC()},
	}
	f.srv = httptest.NewServer(f.handler())
	return f
}

// Server exposes the underlying test server, for tests that need its URL or
// its TLS-aware http.Client.
func (f *FakeGitHub) Server() *httptest.Server { return f.srv }

// URL is the API base URL to hand to an installation.
func (f *FakeGitHub) URL() string { return f.srv.URL }

// Close shuts the server down.
func (f *FakeGitHub) Close() { f.srv.Close() }

// AppID is the App ID the fake claims to be.
func (f *FakeGitHub) AppID() int64 { return f.appID }

// InstallationID is the installation ID the fake serves.
func (f *FakeGitHub) InstallationID() int64 { return f.installationID }

// Client returns a Client wired to this fake for the given target. It is the
// quickest way for a test to get a working github.Client.
func (f *FakeGitHub) Client(target string, kind store.TargetType) Client {
	base := f.srv.URL + "/"
	c, err := newGitHubClient(f.srv.Client(), base, base)
	if err != nil {
		// The URL comes from httptest, so this cannot happen; failing loudly
		// beats handing back a client that talks to the real GitHub.
		panic("github: fake: " + err.Error())
	}
	return newAppClient(c, c, target, kind, f.installationID, "https://github.com")
}

// SetPermissions replaces what the App reports being granted, so tests can
// exercise the "missing permission" reporting.
func (f *FakeGitHub) SetPermissions(p map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.permissions = p
}

// SetEvents replaces the App's event subscriptions.
func (f *FakeGitHub) SetEvents(events ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = events
}

// AddRepo makes "owner/name" visible to the installation. AddQueuedJob does
// this for you; call it directly for a repository with no jobs.
func (f *FakeGitHub) AddRepo(fullName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(fullName)
}

func (f *FakeGitHub) addRepoLocked(fullName string) {
	if fullName == "" || slices.Contains(f.repos, fullName) {
		return
	}
	f.repos = append(f.repos, fullName)
}

// AddRunnerGroup registers a runner group and returns it.
func (f *FakeGitHub) AddRunnerGroup(name string) RunnerGroup {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := RunnerGroup{ID: int64(len(f.groups) + 1), Name: name}
	f.groups = append(f.groups, g)
	return g
}

// AddRunner registers a runner that Zoomies did not create, which is how a
// test simulates a registration the controller has lost track of.
func (f *FakeGitHub) AddRunner(name string, labels []string) Runner {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.addRunnerLocked(name, labels, false)
	return *r
}

func (f *FakeGitHub) addRunnerLocked(name string, labels []string, ephemeral bool) *Runner {
	r := &Runner{
		ID:        f.nextRunnerID,
		Name:      name,
		OS:        "linux",
		Status:    "online",
		Labels:    append([]string{"self-hosted"}, labels...),
		Ephemeral: ephemeral,
	}
	f.nextRunnerID++
	f.runners = append(f.runners, r)
	return r
}

// Runners returns a snapshot of the registered runners.
func (f *FakeGitHub) Runners() []Runner {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Runner, 0, len(f.runners))
	for _, r := range f.runners {
		out = append(out, *r)
	}
	return out
}

// AddQueuedJob queues a job on repo ("owner/name") and returns it as the API
// will report it. The repository becomes visible to the installation.
func (f *FakeGitHub) AddQueuedJob(repo, workflow, jobName string, labels []string) QueuedJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	// A queued job usually follows a push; the poller sorts on it.
	f.repoLocked(repo).pushedAt = time.Now().UTC()
	j := &fakeJob{
		QueuedJob: QueuedJob{
			ID:           f.nextJobID,
			RunID:        f.nextRunID,
			Repo:         repo,
			WorkflowName: workflow,
			JobName:      jobName,
			Labels:       slices.Clone(labels),
			QueuedAt:     time.Now().UTC(),
			HTMLURL:      fmt.Sprintf("https://github.com/%s/actions/runs/%d/job/%d", repo, f.nextRunID, f.nextJobID),
			Status:       string(store.JobQueued),
		},
		runStatus: string(store.JobQueued),
	}
	f.nextJobID++
	f.nextRunID++
	f.jobs = append(f.jobs, j)
	return j.QueuedJob
}

// StartJob marks a queued job as picked up by runnerName.
func (f *FakeGitHub) StartJob(jobID int64, runnerName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j := f.findJobLocked(jobID); j != nil {
		now := time.Now().UTC()
		j.Status = string(store.JobInProgress)
		j.runStatus = string(store.JobInProgress)
		j.RunnerName = runnerName
		j.StartedAt = &now
	}
}

// CompleteJob finishes a job with the given conclusion ("success",
// "failure", "cancelled", ...).
func (f *FakeGitHub) CompleteJob(jobID int64, conclusion string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j := f.findJobLocked(jobID); j != nil {
		now := time.Now().UTC()
		if j.StartedAt == nil {
			j.StartedAt = &now
		}
		j.Status = string(store.JobCompleted)
		j.runStatus = string(store.JobCompleted)
		j.Conclusion = conclusion
		j.CompletedAt = &now
	}
}

func (f *FakeGitHub) findJobLocked(id int64) *fakeJob {
	for _, j := range f.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// SetError makes every request whose path contains pattern fail with status
// and message. An empty pattern matches every request. Calls accumulate; use
// ClearErrors to reset.
//
// A 403 carrying the rate-limit headers (see SetRateLimit) is how a test
// produces ErrRateLimited, because that is how GitHub signals it.
func (f *FakeGitHub) SetError(pattern string, status int, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, fakeFailure{pattern: pattern, status: status, message: message})
}

// ClearErrors removes every failure injected with SetError.
func (f *FakeGitHub) ClearErrors() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = nil
}

// SetRateLimit sets the quota reported by /rate_limit and by the rate-limit
// headers on every response.
func (f *FakeGitHub) SetRateLimit(limit, remaining int, reset time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rateLimit = RateLimit{Limit: limit, Remaining: remaining, ResetAt: reset.UTC()}
}

// Requests returns every request the fake has served, as "GET /path". Tests
// use it to assert that a poll stayed within its call budget.
func (f *FakeGitHub) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// ---------------------------------------------------------------------------
// HTTP surface
// ---------------------------------------------------------------------------

func (f *FakeGitHub) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /app", f.getApp)
	mux.HandleFunc("GET /app/installations/{id}", f.getInstallation)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", f.createInstallationToken)
	mux.HandleFunc("GET /installation/repositories", f.listInstallationRepos)
	mux.HandleFunc("GET /rate_limit", f.getRateLimit)

	mux.HandleFunc("POST /orgs/{org}/actions/runners/generate-jitconfig", f.generateJITConfig)
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runners/generate-jitconfig", f.generateJITConfig)
	mux.HandleFunc("POST /orgs/{org}/actions/runners/registration-token", f.createToken)
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runners/registration-token", f.createToken)
	mux.HandleFunc("POST /orgs/{org}/actions/runners/remove-token", f.createToken)
	mux.HandleFunc("POST /repos/{owner}/{repo}/actions/runners/remove-token", f.createToken)
	mux.HandleFunc("GET /orgs/{org}/actions/runners", f.listRunners)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runners", f.listRunners)
	mux.HandleFunc("DELETE /orgs/{org}/actions/runners/{id}", f.deleteRunner)
	mux.HandleFunc("DELETE /repos/{owner}/{repo}/actions/runners/{id}", f.deleteRunner)
	mux.HandleFunc("GET /orgs/{org}/actions/runner-groups", f.listRunnerGroups)

	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", f.listWorkflowRuns)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{run}/jobs", f.listWorkflowJobs)

	f.registerMigrationRoutes(mux)

	return f.middleware(mux)
}

// middleware records the request, strips the GitHub Enterprise Server /api/v3
// prefix so one set of routes serves both layouts, stamps the rate-limit
// headers GitHub always sends, and applies any injected failure.
func (f *FakeGitHub) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The raw path is what gets recorded, so a test can tell a GHES-shaped
		// call from a github.com-shaped one.
		raw := r.URL.Path
		path := raw
		if rest, ok := strings.CutPrefix(path, "/api/v3"); ok && strings.HasPrefix(rest, "/") {
			r = r.Clone(r.Context())
			r.URL.Path = rest
			path = rest
		}

		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+raw)
		rate := f.rateLimit
		var hit *fakeFailure
		for i := range f.failures {
			if f.failures[i].pattern == "" || strings.Contains(path, f.failures[i].pattern) {
				hit = &f.failures[i]
				break
			}
		}
		f.mu.Unlock()

		h := w.Header()
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("X-Ratelimit-Limit", strconv.Itoa(rate.Limit))
		h.Set("X-Ratelimit-Remaining", strconv.Itoa(rate.Remaining))
		h.Set("X-Ratelimit-Reset", strconv.FormatInt(rate.ResetAt.Unix(), 10))

		if hit != nil {
			writeError(w, hit.status, hit.message)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	if message == "" {
		message = http.StatusText(status)
	}
	writeJSON(w, status, map[string]any{
		"message":           message,
		"documentation_url": "https://docs.github.com/rest",
	})
}

// target reconstructs the target a request addresses, so one handler can serve
// both halves of each org/repo endpoint pair.
func target(r *http.Request) (full string, kind store.TargetType) {
	if org := r.PathValue("org"); org != "" {
		return org, store.TargetOrg
	}
	return r.PathValue("owner") + "/" + r.PathValue("repo"), store.TargetRepo
}

func (f *FakeGitHub) getApp(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          f.appID,
		"slug":        f.appSlug,
		"name":        f.appName,
		"owner":       map[string]any{"login": f.appOwner},
		"permissions": f.permissions,
		"events":      f.events,
	})
}

func (f *FakeGitHub) getInstallation(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.installationID {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          f.installationID,
		"app_id":      f.appID,
		"app_slug":    f.appSlug,
		"account":     map[string]any{"login": f.appOwner},
		"target_type": "Organization",
		"permissions": f.permissions,
		"events":      f.events,
	})
}

func (f *FakeGitHub) createInstallationToken(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.installationID {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":       "ghs_fake_installation_token",
		"expires_at":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"permissions": f.permissions,
	})
}

func (f *FakeGitHub) listInstallationRepos(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repos := make([]map[string]any, 0, len(f.repos))
	for _, full := range f.repos {
		owner, name, _ := SplitTarget(full)
		repos = append(repos, map[string]any{
			"full_name": full,
			"name":      name,
			"owner":     map[string]any{"login": owner},
			// The migration scan reads the default branch from here: a pull
			// request has to be opened against one, and guessing "main" is how
			// a migration fails silently on a repository still using "master".
			"default_branch": f.repoLocked(full).defaultBranch,
			"private":        true,
			"archived":       false,
			"html_url":       "https://github.com/" + full,
			"pushed_at":      f.repoLocked(full).pushedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": repos})
}

func (f *FakeGitHub) getRateLimit(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	core := map[string]any{
		"limit":     f.rateLimit.Limit,
		"remaining": f.rateLimit.Remaining,
		"used":      f.rateLimit.Limit - f.rateLimit.Remaining,
		"reset":     f.rateLimit.ResetAt.Unix(),
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": map[string]any{"core": core}})
}

func (f *FakeGitHub) generateJITConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string   `json:"name"`
		RunnerGroupID int64    `json:"runner_group_id"`
		WorkFolder    string   `json:"work_folder"`
		Labels        []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	if req.Name == "" || len(req.Labels) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed: name and labels are required")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// GitHub refuses to reuse a runner name within a target; a fake that
	// allowed it would hide the bug where a pool mints colliding names.
	for _, existing := range f.runners {
		if existing.Name == req.Name {
			writeError(w, http.StatusConflict,
				fmt.Sprintf("A runner with the name %q already exists.", req.Name))
			return
		}
	}
	if !slices.ContainsFunc(f.groups, func(g RunnerGroup) bool { return g.ID == req.RunnerGroupID }) {
		writeError(w, http.StatusNotFound, "Runner group not found")
		return
	}

	runner := f.addRunnerLocked(req.Name, req.Labels, true)
	blob, _ := json.Marshal(map[string]any{
		"name":        req.Name,
		"labels":      req.Labels,
		"work_folder": req.WorkFolder,
		"runner_id":   runner.ID,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"runner":             runnerJSON(runner),
		"encoded_jit_config": base64.StdEncoding.EncodeToString(blob),
	})
}

func (f *FakeGitHub) createToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      "AABF3JGZDX3P5PMEXLND6TS6FCWO6",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

func (f *FakeGitHub) listRunners(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, 0, len(f.runners))
	for _, r := range f.runners {
		out = append(out, runnerJSON(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(out), "runners": out})
}

func runnerJSON(r *Runner) map[string]any {
	labels := make([]map[string]any, 0, len(r.Labels))
	for i, l := range r.Labels {
		labels = append(labels, map[string]any{"id": i + 1, "name": l, "type": "custom"})
	}
	return map[string]any{
		"id":        r.ID,
		"name":      r.Name,
		"os":        r.OS,
		"status":    r.Status,
		"busy":      r.Busy,
		"ephemeral": r.Ephemeral,
		"labels":    labels,
	}
}

func (f *FakeGitHub) deleteRunner(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.runners, func(x *Runner) bool { return x.ID == id })
	if i < 0 {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	f.runners = slices.Delete(f.runners, i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

func (f *FakeGitHub) listRunnerGroups(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, map[string]any{"id": g.ID, "name": g.Name, "default": g.ID == 1})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(out), "runner_groups": out})
}

func (f *FakeGitHub) listWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	want := r.URL.Query().Get("status")

	f.mu.Lock()
	defer f.mu.Unlock()
	runs := make([]map[string]any, 0)
	for _, j := range f.jobs {
		if j.Repo != full {
			continue
		}
		if want != "" && j.runStatus != want {
			continue
		}
		runs = append(runs, map[string]any{
			"id":         j.RunID,
			"name":       j.WorkflowName,
			"status":     j.runStatus,
			"created_at": j.QueuedAt.Format(time.RFC3339),
			"html_url":   j.HTMLURL,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "workflow_runs": runs})
}

func (f *FakeGitHub) listWorkflowJobs(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	runID, _ := strconv.ParseInt(r.PathValue("run"), 10, 64)

	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := make([]map[string]any, 0)
	for _, j := range f.jobs {
		if j.Repo != full || j.RunID != runID {
			continue
		}
		job := map[string]any{
			"id":            j.ID,
			"run_id":        j.RunID,
			"name":          j.JobName,
			"workflow_name": j.WorkflowName,
			"status":        j.Status,
			"labels":        j.Labels,
			"created_at":    j.QueuedAt.Format(time.RFC3339),
			"html_url":      j.HTMLURL,
			"runner_name":   j.RunnerName,
		}
		if j.Conclusion != "" {
			job["conclusion"] = j.Conclusion
		}
		if j.StartedAt != nil {
			job["started_at"] = j.StartedAt.Format(time.RFC3339)
		}
		if j.CompletedAt != nil {
			job["completed_at"] = j.CompletedAt.Format(time.RFC3339)
		}
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(jobs), "jobs": jobs})
}
