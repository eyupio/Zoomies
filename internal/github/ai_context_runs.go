package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/config"
	gh "github.com/google/go-github/v88/github"
)

// ContextRunReader reads what the managed workflow did, so that a context that
// has not been published can be explained instead of only noticed. It is
// read-only and needs the Actions read permission the App already has for the
// fleet; starting a run is ContextWorkflowDispatcher's, and separate.
type ContextRunReader interface {
	// LatestContextRun is the most informative run of the managed workflow for a
	// commit, or nil when it has none.
	LatestContextRun(ctx context.Context, repo, commit string) (*ContextRun, error)
	// ContextJobErrors is the error lines of one job's log, bounded. It is nil,
	// with no error, when the job has no log: it never started, or the log has
	// expired.
	ContextJobErrors(ctx context.Context, repo string, jobID int64) ([]string, error)
	// ContextArtifactUsage is what the repository's unexpired artifacts hold.
	ContextArtifactUsage(ctx context.Context, repo string) (*aicontext.ArtifactUsage, error)
}

// ContextRun is one run, in the terms aicontext.Diagnose reads.
type ContextRun struct {
	ID        int64
	URL       string
	CreatedAt time.Time
	UpdatedAt time.Time
	Facts     aicontext.RunFacts
	// JobIDs are GitHub's ids for Facts.Jobs, in the same order.
	JobIDs []int64
}

const (
	// maxContextLog bounds how much of one job's log is read. The managed
	// workflow's logs are small -- the generator prints paths and sizes, never
	// source -- so this is a ceiling against a log that is not that, not a
	// working size.
	maxContextLog = 8 << 20
	// maxContextErrorLines and maxContextErrorLine bound what is kept of it.
	maxContextErrorLines = 12
	maxContextErrorLine  = 300
	// maxArtifactPages bounds the artifact listing: a thousand artifacts is
	// enough to say what is using the storage, and past it the totals are
	// reported as at least.
	maxArtifactPages = 10
	artifactPageSize = 100
)

// LatestContextRun lists the runs for a commit, newest first, and returns the
// first that says something. A run cancelled by a newer one is not an answer --
// the managed workflow's concurrency group cancels the previous run on purpose
// -- so it is skipped for an older one, and returned only when every run was
// cancelled, so that the caller can still tell nothing has happened since.
func (c *appClient) LatestContextRun(ctx context.Context, repo, commit string) (*ContextRun, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	runs, resp, err := c.asInstallation.Actions.ListWorkflowRunsByFileName(ctx, owner, name, path.Base(aicontext.WorkflowPath),
		&gh.ListWorkflowRunsOptions{HeadSHA: commit, ListOptions: gh.ListOptions{PerPage: 10}})
	if err != nil {
		return nil, c.fail("list context workflow runs", resp, err)
	}
	var pick, cancelled *gh.WorkflowRun
	for _, r := range runs.WorkflowRuns {
		if r.GetStatus() == "completed" && r.GetConclusion() == "cancelled" {
			if cancelled == nil {
				cancelled = r
			}
			continue
		}
		pick = r
		break
	}
	if pick == nil {
		pick = cancelled
	}
	if pick == nil {
		return nil, nil
	}
	run := &ContextRun{
		ID: pick.GetID(), URL: pick.GetHTMLURL(),
		CreatedAt: pick.GetCreatedAt().Time, UpdatedAt: pick.GetUpdatedAt().Time,
		Facts: aicontext.RunFacts{Status: pick.GetStatus(), Conclusion: pick.GetConclusion()},
	}
	// A run that has not finished is reported as it is; its jobs are not needed
	// to know that it must be left alone, and a run that was cancelled or never
	// started has none worth reading.
	if pick.GetStatus() != "completed" || pick.GetConclusion() == "cancelled" || pick.GetConclusion() == "startup_failure" {
		return run, nil
	}
	jobs, resp, err := c.asInstallation.Actions.ListWorkflowJobs(ctx, owner, name, pick.GetID(),
		&gh.ListWorkflowJobsOptions{Filter: "latest", ListOptions: gh.ListOptions{PerPage: 20}})
	if err != nil {
		return nil, c.fail("list context workflow jobs", resp, err)
	}
	for _, j := range jobs.Jobs {
		job := aicontext.JobFacts{Name: j.GetName(), Conclusion: j.GetConclusion()}
		for _, s := range j.Steps {
			job.Steps = append(job.Steps, aicontext.StepFacts{Name: s.GetName(), Conclusion: s.GetConclusion()})
		}
		run.Facts.Jobs = append(run.Facts.Jobs, job)
		run.JobIDs = append(run.JobIDs, j.GetID())
	}
	return run, nil
}

// ContextJobErrors downloads one job's log and keeps only the lines that explain
// a failure: every ##[error] annotation, and the line before it. The generator and
// the upload and publication scripts end by raising SystemExit with their reason,
// which Actions prints as an ordinary line immediately before its own "Process
// completed with exit code 1" annotation, so the reason is the line before.
//
// The log lives at a pre-signed address on another host. The request for it is
// made without the installation's token for exactly that reason, and only to an
// address on the scheme of the API itself.
func (c *appClient) ContextJobErrors(ctx context.Context, repo string, jobID int64) ([]string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	u, resp, err := c.asInstallation.Actions.GetWorkflowJobLogs(ctx, owner, name, jobID, 0)
	if err != nil {
		// A log that is gone -- expired, or never written for a job that did not
		// start -- is an answer ("no log"), not a failure to ask.
		if resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
			return nil, nil
		}
		return nil, c.fail("read context job log", resp, err)
	}
	if !fetchableLogAddress(c.asInstallation.BaseURL(), u) {
		return nil, fmt.Errorf("github: the job log address is not one to fetch")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: read context job log: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: read context job log: unexpected status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxContextLog))
	if err != nil {
		return nil, fmt.Errorf("github: read context job log: %w", err)
	}
	return errorLines(string(body)), nil
}

// fetchableLogAddress is whether this process may fetch a log from where GitHub
// said it is. The address is GitHub's to name, but it arrives in a response
// header and is about to be requested from the controller's own network, so it
// is held to what an operator-supplied URL is: it is on the API's scheme, names a
// host and no credentials, and does not point at this machine or a private
// network -- unless the API itself does, as an Enterprise Server on a LAN, or a
// test, legitimately does.
func fetchableLogAddress(apiBase string, log *url.URL) bool {
	api, err := url.Parse(apiBase)
	if err != nil || log == nil || log.Host == "" || log.User != nil || log.Scheme != api.Scheme {
		return false
	}
	apiIsPrivate := config.CheckOutboundURL("", apiBase, false) != nil
	return config.CheckOutboundURL("the job log address", log.String(), apiIsPrivate) == nil
}

// errorLines picks the lines of a log that explain why a job failed.
func errorLines(log string) []string {
	lines := strings.Split(log, "\n")
	var out []string
	for i, line := range lines {
		line = strings.TrimRight(stripLogTimestamp(line), "\r")
		if !strings.HasPrefix(line, "##[error]") {
			continue
		}
		if i > 0 {
			if prev := strings.TrimRight(stripLogTimestamp(lines[i-1]), "\r"); prev != "" && !strings.HasPrefix(prev, "##[") {
				out = append(out, clip(prev))
			}
		}
		out = append(out, clip(strings.TrimPrefix(line, "##[error]")))
	}
	// The last of them are the ones that ended the job.
	if len(out) > maxContextErrorLines {
		out = out[len(out)-maxContextErrorLines:]
	}
	return out
}

// stripLogTimestamp removes the "2026-10-06T06:47:48.2010869Z " Actions puts in
// front of every line.
func stripLogTimestamp(line string) string {
	if head, rest, ok := strings.Cut(line, " "); ok && strings.HasSuffix(head, "Z") && strings.Contains(head, "T") {
		return rest
	}
	return line
}

func clip(s string) string {
	if len(s) > maxContextErrorLine {
		return s[:maxContextErrorLine]
	}
	return s
}

// ContextArtifactUsage totals the repository's unexpired artifacts and names the
// biggest groups by name. It reads at most maxArtifactPages pages.
func (c *appClient) ContextArtifactUsage(ctx context.Context, repo string) (*aicontext.ArtifactUsage, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	usage := &aicontext.ArtifactUsage{}
	groups := map[string]*aicontext.ArtifactGroup{}
	for page := 1; ; page++ {
		list, resp, err := c.asInstallation.Actions.ListArtifacts(ctx, owner, name, &gh.ListArtifactsOptions{ListOptions: gh.ListOptions{PerPage: artifactPageSize, Page: page}})
		if err != nil {
			return nil, c.fail("list artifacts", resp, err)
		}
		for _, a := range list.Artifacts {
			if a.GetExpired() {
				continue
			}
			usage.Count++
			usage.Bytes += a.GetSizeInBytes()
			g := groups[a.GetName()]
			if g == nil {
				g = &aicontext.ArtifactGroup{Name: a.GetName()}
				groups[a.GetName()] = g
			}
			g.Count++
			g.Bytes += a.GetSizeInBytes()
		}
		if len(list.Artifacts) < artifactPageSize {
			break
		}
		if page == maxArtifactPages {
			usage.Partial = true
			break
		}
	}
	for _, g := range groups {
		usage.Largest = append(usage.Largest, *g)
	}
	sort.Slice(usage.Largest, func(i, j int) bool {
		if usage.Largest[i].Bytes != usage.Largest[j].Bytes {
			return usage.Largest[i].Bytes > usage.Largest[j].Bytes
		}
		return usage.Largest[i].Name < usage.Largest[j].Name
	})
	if len(usage.Largest) > 3 {
		usage.Largest = usage.Largest[:3]
	}
	return usage, nil
}

var _ ContextRunReader = (*appClient)(nil)
