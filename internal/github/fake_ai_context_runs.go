package github

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

// What the fake knows about the managed AI Context workflow's runs. Unlike the
// fleet's jobs, which a test queues and the controller picks up, these are
// history: a test says what a run did, and the controller reads it back to
// explain why a repository's context is stale.

// FakeContextRun is one run of a repository's managed workflow.
type FakeContextRun struct {
	// Commit is the head commit the run was for.
	Commit string
	// Status is queued, in_progress or completed; empty means completed.
	Status     string
	Conclusion string
	// Finished is when the run ended; empty means now. A test that needs a
	// failure from hours ago says so here rather than waiting for it.
	Finished time.Time
	Jobs     []FakeContextJob
}

// FakeContextJob is one job of a run.
type FakeContextJob struct {
	Name       string
	Conclusion string
	Steps      []FakeContextStep
	// Log is the job's log. Empty means it has none, which GitHub answers with a
	// 404 for a job that never started.
	Log string
}

// FakeContextStep is one step of a job.
type FakeContextStep struct{ Name, Conclusion string }

// FakeArtifact is one artifact in a repository.
type FakeArtifact struct {
	Name    string
	Bytes   int64
	Expired bool
}

type fakeContextRun struct {
	id      int64
	repo    string
	created time.Time
	FakeContextRun
	jobIDs []int64
}

type fakeContextRuns struct {
	runs      []*fakeContextRun
	logs      map[int64]string
	artifacts map[string][]FakeArtifact
	// logAuth records the Authorization header of every log download, so a test
	// can show the installation's token is never sent to the log's address.
	logAuth []string
}

// AddContextRun records a run of repo's managed workflow and returns its id.
// Runs are listed newest first by id, so the order they were added is their age.
func (f *FakeGitHub) AddContextRun(repo string, run FakeContextRun) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRunID++
	if run.Status == "" {
		run.Status = "completed"
	}
	if run.Finished.IsZero() {
		run.Finished = time.Now()
	}
	r := &fakeContextRun{id: f.nextRunID, repo: repo, created: run.Finished, FakeContextRun: run}
	if f.contextRuns.logs == nil {
		f.contextRuns.logs = map[int64]string{}
	}
	for _, j := range run.Jobs {
		f.nextJobID++
		r.jobIDs = append(r.jobIDs, f.nextJobID)
		if j.Log != "" {
			f.contextRuns.logs[f.nextJobID] = j.Log
		}
	}
	f.contextRuns.runs = append(f.contextRuns.runs, r)
	return r.id
}

// ContextRunJobID is the id of the i'th job of a run added with AddContextRun.
func (f *FakeGitHub) ContextRunJobID(runID int64, i int) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.contextRuns.runs {
		if r.id == runID && i < len(r.jobIDs) {
			return r.jobIDs[i]
		}
	}
	return 0
}

// AddArtifacts adds artifacts to a repository.
func (f *FakeGitHub) AddArtifacts(repo string, artifacts ...FakeArtifact) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.contextRuns.artifacts == nil {
		f.contextRuns.artifacts = map[string][]FakeArtifact{}
	}
	f.contextRuns.artifacts[repo] = append(f.contextRuns.artifacts[repo], artifacts...)
}

// ContextLogAuthorizations is the Authorization header of each log download.
func (f *FakeGitHub) ContextLogAuthorizations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.contextRuns.logAuth...)
}

func (f *FakeGitHub) registerContextRunRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/workflows/{file}/runs", f.contextListRuns)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/jobs/{job}/logs", f.contextJobLogRedirect)
	mux.HandleFunc("GET /_logs/{job}", f.contextJobLog)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/artifacts", f.contextListArtifacts)
}

// canReadActionsLocked is the Actions read permission, which write includes.
func (f *FakeGitHub) canReadActionsLocked() bool {
	level := f.permissions["actions"]
	return level == "read" || level == "write"
}

func (f *FakeGitHub) contextListRuns(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canReadActionsLocked() {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	full, head := fullName(r), r.URL.Query().Get("head_sha")
	var runs []*fakeContextRun
	for _, run := range f.contextRuns.runs {
		if run.repo == full && (head == "" || run.Commit == head) {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].id > runs[j].id })
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		entry := map[string]any{
			"id": run.id, "status": run.Status, "head_sha": run.Commit, "event": "push",
			"html_url":   "https://github.example/" + full + "/actions/runs/" + strconv.FormatInt(run.id, 10),
			"created_at": run.created.UTC().Format(time.RFC3339), "updated_at": run.created.UTC().Format(time.RFC3339),
		}
		if run.Conclusion != "" {
			entry["conclusion"] = run.Conclusion
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(out), "workflow_runs": out})
}

// writeContextRunJobsLocked answers the jobs of a managed-workflow run, and
// reports whether the run was one.
func (f *FakeGitHub) writeContextRunJobsLocked(w http.ResponseWriter, full string, runID int64) bool {
	for _, run := range f.contextRuns.runs {
		if run.id != runID || run.repo != full {
			continue
		}
		if !f.canReadActionsLocked() {
			writeError(w, http.StatusForbidden, "Resource not accessible by integration")
			return true
		}
		jobs := make([]map[string]any, 0, len(run.Jobs))
		for i, j := range run.Jobs {
			steps := make([]map[string]any, 0, len(j.Steps))
			for n, s := range j.Steps {
				step := map[string]any{"name": s.Name, "number": n + 1, "status": "completed"}
				if s.Conclusion != "" {
					step["conclusion"] = s.Conclusion
				}
				steps = append(steps, step)
			}
			job := map[string]any{"id": run.jobIDs[i], "run_id": run.id, "name": j.Name, "status": "completed", "steps": steps}
			if j.Conclusion != "" {
				job["conclusion"] = j.Conclusion
			}
			jobs = append(jobs, job)
		}
		writeJSON(w, http.StatusOK, map[string]any{"total_count": len(jobs), "jobs": jobs})
		return true
	}
	return false
}

// contextJobLogRedirect answers as GitHub does: a 302 to a pre-signed address
// on another host, not the log.
func (f *FakeGitHub) contextJobLogRedirect(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canReadActionsLocked() {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	job, _ := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if _, ok := f.contextRuns.logs[job]; !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	http.Redirect(w, r, f.srv.URL+"/_logs/"+r.PathValue("job"), http.StatusFound)
}

func (f *FakeGitHub) contextJobLog(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contextRuns.logAuth = append(f.contextRuns.logAuth, r.Header.Get("Authorization"))
	job, _ := strconv.ParseInt(r.PathValue("job"), 10, 64)
	log, ok := f.contextRuns.logs[job]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(log))
}

func (f *FakeGitHub) contextListArtifacts(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canReadActionsLocked() {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	all := f.contextRuns.artifacts[fullName(r)]
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 30
	}
	lo, hi := min((page-1)*size, len(all)), min(page*size, len(all))
	out := make([]map[string]any, 0, hi-lo)
	for i, a := range all[lo:hi] {
		out = append(out, map[string]any{"id": lo + i + 1, "name": a.Name, "size_in_bytes": a.Bytes, "expired": a.Expired})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(all), "artifacts": out})
}
