package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Workflow runs
// ---------------------------------------------------------------------------

// WorkflowRun is one workflow run as this fleet has seen it: the jobs GitHub
// reported under one run, summed up into the row the Workflows page lists. It
// is the "#1009" an operator finds in GitHub's Actions tab, so the two can be
// read side by side.
//
// It is derived, never stored. Only workflow_job deliveries are ingested, so
// everything known about a run is what its jobs said, and a row of its own
// would be one more thing for a late or missing delivery to leave stale. The
// summary is over the latest attempt of each job name, which is what GitHub's
// own run page shows: re-running the failed jobs writes new job rows under the
// same run, and a run whose second attempt passed is a run that passed.
type WorkflowRun struct {
	Repo        string
	GitHubRunID int64
	Workflow    string
	// RunNumber is GitHub's own sequential number for the run. Zero until a
	// job of the run has had it backfilled, since workflow_job deliveries do
	// not carry it.
	RunNumber int64
	// RunAttempt is the highest attempt any job of the run reported.
	RunAttempt     int
	HeadBranch     string
	HeadSHA        string
	InstallationID string
	// HTMLURL is the run's page on GitHub, cut from a job's own URL.
	HTMLURL string
	// State is the run's, worked out from its jobs: running while any job is,
	// then queued while any job waits for a runner, then waiting while any is
	// held for a review, and completed once every job is.
	State JobState
	// Conclusion is the run's once it is completed, worst outcome first: a
	// failure on either side, then cancelled, then action required, then
	// success. Empty while the run is not completed.
	Conclusion string
	// QueuedAt is when the first job was queued, StartedAt when the first job
	// started, and CompletedAt when the last job finished -- set only once
	// every job has, because a run with a job still to go has not completed.
	QueuedAt    time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Jobs        RunJobCounts
	// Managed is whether this fleet has a hand in any job of the run, in the
	// sense of JobFilter.ManagedOnly.
	Managed bool
	// Hosted is whether every job's labels name runners somebody else operates.
	Hosted bool
	// Cancelling is whether an operator's cancellation of the run has been
	// accepted by GitHub and not yet confirmed by its jobs completing.
	Cancelling bool
}

// RunJobCounts is how a run's jobs are getting on, counted over the latest
// attempt of each. Failed counts failures on either side, so Faulted -- the
// failures this fleet caused -- is always part of it, and a job whose runner
// stopped under it is a failure whatever conclusion GitHub recorded: the
// fault wins, as it does on the Overview, so the outcomes never count one
// job twice.
type RunJobCounts struct {
	Total      int `json:"total"`
	Waiting    int `json:"waiting"`
	Queued     int `json:"queued"`
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	Skipped    int `json:"skipped"`
	Faulted    int `json:"faulted"`
	// Unmatched counts the queued jobs no enabled pool claims and nobody else
	// is about to run: the ones the Jobs page's unmatched filter finds.
	Unmatched int `json:"unmatched"`
	// Expedited, Paused and Removed count what an operator has done to the
	// run's queued jobs' provisioning demand -- Run now, Pause and Delete
	// from queue on the Queue page. They are part of Queued, not beside it:
	// GitHub goes on calling a paused job queued, and so does the run. The
	// row needs them so a Pause pressed on the run can be refused, with a
	// reason, once every queued job of it is already paused.
	Expedited int `json:"expedited"`
	Paused    int `json:"paused"`
	Removed   int `json:"removed"`
}

// Controllable is how many of the run's queued jobs a run-level provisioning
// action reaches: the queued ones an operator has not removed from the queue.
// A removed job was stood down on purpose, and is restored from the Queue
// page's Removed view rather than swept back in by a Run now on its run.
func (c RunJobCounts) Controllable() int {
	return c.Queued - c.Removed
}

// QueueWait is how long the run waited before any of its jobs was picked up.
func (r *WorkflowRun) QueueWait() time.Duration {
	if r.StartedAt == nil {
		return 0
	}
	return r.StartedAt.Sub(r.QueuedAt)
}

// Duration is how long the run took from its first job starting to its last
// finishing, or 0 while it has not finished.
func (r *WorkflowRun) Duration() time.Duration {
	if r.StartedAt == nil || r.CompletedAt == nil {
		return 0
	}
	return r.CompletedAt.Sub(*r.StartedAt)
}

// runStateSQL and runConclusionSQL are the run's state and conclusion as SQL
// over its jobs' aggregates, so a page can be filtered by them rather than by
// its jobs' states -- "runs with a queued job" is not "queued runs", because a
// run with one job running and another queued is running.
const runStateSQL = `CASE
	WHEN SUM(state = 'in_progress') > 0 THEN 'in_progress'
	WHEN SUM(state = 'queued') > 0 THEN 'queued'
	WHEN SUM(state = 'waiting') > 0 THEN 'waiting'
	ELSE 'completed' END`

func runConclusionSQL() string {
	return `CASE
	WHEN SUM(state != 'completed') > 0 THEN ''
	WHEN SUM(` + failedJobSQL() + `) > 0 THEN 'failure'
	WHEN SUM(conclusion = 'cancelled') > 0 THEN 'cancelled'
	WHEN SUM(conclusion = 'action_required') > 0 THEN 'action_required'
	WHEN SUM(conclusion = 'success') > 0 THEN 'success'
	WHEN SUM(conclusion = 'skipped') > 0 THEN 'skipped'
	ELSE MAX(conclusion) END`
}

var workflowRunSortCols = map[string]string{
	"queued_at":    "queued_at",
	"started_at":   "started_at",
	"completed_at": "completed_at",
	"repo":         "repo",
	"workflow":     "workflow",
	"run_number":   "run_number",
	"state":        "run_state",
	"jobs":         "total",
	"duration":     "(COALESCE(completed_at,0) - COALESCE(started_at,0))",
	"queue_wait":   "(COALESCE(started_at,0) - queued_at)",
}

// ListWorkflowRuns returns a filtered page of workflow runs and the matching
// total, newest first by default.
//
// The filter is a job filter, read two ways. Its status half -- states,
// conclusions, the failed and faulted switches, cancelling -- is applied to
// the run's own derived status, so a run is listed under the state it is in
// rather than under every state one of its jobs is in. Everything else names
// a job, and keeps a run whenever any job of it matches: a run in a
// repository, a run with a job on this pool, a run with a job still unmatched.
func (s *Store) ListWorkflowRuns(ctx context.Context, f JobFilter, p Page) ([]*WorkflowRun, int, error) {
	if runs, total, ok, err := s.listWorkflowRunsNewestFirst(ctx, f, p); err != nil || ok {
		return runs, total, err
	}
	from, args := workflowRunsFrom(f)
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) `+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + workflowRunCols + ` ` + from +
		` ORDER BY ` + p.orderBy(workflowRunSortCols, "queued_at DESC") + `, repo ASC, github_run_id ASC LIMIT ? OFFSET ?`
	args = append(args, p.limit(50, 500), max(p.Offset, 0))
	return s.queryWorkflowRuns(ctx, q, args, total)
}

const workflowRunCols = `repo, github_run_id, workflow, run_number, run_attempt, head_branch, head_sha,
		installation_id, html_url, run_state, run_conclusion, queued_at, started_at, completed_at,
		total, waiting, queued, in_progress, completed, succeeded, failed, cancelled, skipped,
		faulted, unmatched, expedited, paused, removed, managed, hosted, cancelling`

func (s *Store) queryWorkflowRuns(ctx context.Context, q string, args []any, total int) ([]*WorkflowRun, int, error) {
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*WorkflowRun
	for rows.Next() {
		r, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// listWorkflowRunsNewestFirst answers the listing's default question -- the
// newest runs, with no condition on a run's own status -- without summing
// every job in the table to answer it. ok is false when the question is not
// that one, or the shortcut cannot prove its answer, and the caller does it
// the long way.
//
// The long way windows and aggregates every job row before it can sort, so its
// cost is the whole retained history however small the page: nineteen seconds
// at four hundred thousand jobs. This takes the runs that have a job among the
// newest few thousand, sums only those, and keeps the answer only when it can
// show that no other run could have ranked above it.
//
// The proof is the one line that makes it exact rather than approximate. A
// run's queued_at is the earliest of its latest-attempt jobs. Let M be the
// queued_at of the newest-but-Jth job in the table. Every run with a job at or
// after M is a candidate and is summed in full; every other run has all of its
// jobs, of every attempt, before M, so its own queued_at is before M too. If
// the page's last row is at or after M, nothing outside the candidates can
// outrank it, and the page is the page the long way would have returned. If it
// is not, J grows fourfold; and if it will not settle, the long way runs.
func (s *Store) listWorkflowRunsNewestFirst(ctx context.Context, f JobFilter, p Page) ([]*WorkflowRun, int, bool, error) {
	// Only the default order, and only a filter that says nothing about a run's
	// status: those are applied to the sums, which is what this avoids.
	if _, known := workflowRunSortCols[p.Sort]; known && !(p.Sort == "queued_at" && p.Desc) {
		return nil, 0, false, nil
	}
	if len(f.States) > 0 || len(f.Conclusions) > 0 || f.FailedOnly || f.FaultedOnly ||
		f.WorkflowFailedOnly || f.Cancelling != nil {
		return nil, 0, false, nil
	}
	limit, offset := p.limit(50, 500), max(p.Offset, 0)
	need := offset + limit

	where, whereArgs := jobWhere(f)
	var total int
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (SELECT 1 FROM jobs `+where+` GROUP BY repo, github_run_id)`, whereArgs...).Scan(&total); err != nil {
		return nil, 0, false, err
	}
	if total <= need {
		// Everything is on this page, so there is nothing to rank against.
		return nil, 0, false, nil
	}

	for newest := max(need*20, 1000); newest <= 200_000; newest *= 4 {
		var bound int64
		err := s.read.QueryRowContext(ctx,
			`SELECT queued_at FROM jobs ORDER BY queued_at DESC, id ASC LIMIT 1 OFFSET ?`, newest-1).Scan(&bound)
		if errors.Is(err, sql.ErrNoRows) {
			// Fewer jobs than that: the whole table is cheap to sum.
			return nil, 0, false, nil
		}
		if err != nil {
			return nil, 0, false, err
		}
		restrict := ` WHERE (repo, github_run_id) IN (SELECT repo, github_run_id FROM jobs WHERE queued_at >= ?)`
		from, args := workflowRunsFromRestricted(f, restrict, []any{bound})
		q := `SELECT ` + workflowRunCols + ` ` + from +
			` ORDER BY queued_at DESC, repo ASC, github_run_id ASC LIMIT ?`
		runs, _, err := s.queryWorkflowRuns(ctx, q, append(args, need), total)
		if err != nil {
			return nil, 0, false, err
		}
		if len(runs) < need || ms(runs[need-1].QueuedAt) < bound {
			continue
		}
		return runs[offset:need], total, true, nil
	}
	return nil, 0, false, nil
}

// workflowRunsFrom renders the FROM and WHERE of a run listing: the jobs
// reduced to the latest attempt of each, summed per run, and narrowed by the
// filter's two halves.
func workflowRunsFrom(f JobFilter) (string, []any) {
	return workflowRunsFromRestricted(f, "", nil)
}

// workflowRunsFromRestricted is workflowRunsFrom over only the jobs a WHERE
// clause on the jobs table keeps, applied before the window is computed so that
// the window sees those rows and no others. restrictArgs are that clause's
// placeholders.
func workflowRunsFromRestricted(f JobFilter, restrict string, restrictArgs []any) (string, []any) {
	// The job half of the filter keeps a run whenever any of its jobs matches.
	// The status half is cleared here and applied to the run's own aggregates
	// below, since a job's state says nothing about its run's.
	jobs := f
	jobs.States, jobs.Conclusions = nil, nil
	jobs.FailedOnly, jobs.FaultedOnly, jobs.WorkflowFailedOnly = false, false, false
	jobs.Cancelling = nil
	where, args := jobWhere(jobs)
	inner := `WHERE run_attempt = latest_attempt`
	if where != "" {
		inner += ` AND (repo, github_run_id) IN (SELECT repo, github_run_id FROM jobs ` + where + `)`
	}

	var cond []string
	if len(f.States) > 0 {
		ph := make([]string, len(f.States))
		for i, st := range f.States {
			ph[i] = "?"
			args = append(args, string(st))
		}
		cond = append(cond, `run_state IN (`+strings.Join(ph, ",")+`)`)
	}
	if len(f.Conclusions) > 0 {
		ph := make([]string, len(f.Conclusions))
		for i, c := range f.Conclusions {
			ph[i] = "?"
			args = append(args, c)
		}
		cond = append(cond, `run_conclusion IN (`+strings.Join(ph, ",")+`)`)
	}
	switch {
	case f.FaultedOnly:
		cond = append(cond, `faulted > 0`)
	case f.WorkflowFailedOnly:
		cond = append(cond, `workflow_failed > 0`)
	case f.FailedOnly:
		cond = append(cond, `failed > 0`)
	}
	if f.Cancelling != nil {
		if *f.Cancelling {
			cond = append(cond, `cancelling > 0`)
		} else {
			cond = append(cond, `cancelling = 0`)
		}
	}
	outer := ""
	if len(cond) > 0 {
		outer = ` WHERE ` + strings.Join(cond, " AND ")
	}

	// The window finds, for each job name within a run, the newest attempt
	// any job of that name reported, and a job is kept when it belongs to
	// that attempt: re-running the failed jobs writes new rows under the same
	// names, and those are the rows GitHub's run page shows. It is the
	// attempt that is compared rather than a rank, because two jobs of one
	// attempt may share a display name -- GitHub does not require job names
	// to be unique -- and ranking them would keep one and lose the other.
	// hosted and managed are worked out per job here, where the label
	// predicates can see the row, and only summed up below.
	from := `FROM (
	SELECT repo, github_run_id, MAX(workflow) AS workflow, MAX(run_number) AS run_number,
		MAX(run_attempt) AS run_attempt, MAX(head_branch) AS head_branch, MAX(head_sha) AS head_sha,
		MAX(installation_id) AS installation_id, MAX(html_url) AS html_url,
		MIN(queued_at) AS queued_at, MIN(started_at) AS started_at,
		CASE WHEN SUM(state != 'completed') = 0 THEN MAX(completed_at) END AS completed_at,
		COUNT(*) AS total,
		SUM(state = 'waiting') AS waiting, SUM(state = 'queued') AS queued,
		SUM(state = 'in_progress') AS in_progress, SUM(state = 'completed') AS completed,
		SUM(conclusion = 'success' AND NOT ` + fleetFailedJobSQL() + `) AS succeeded,
		SUM(` + failedJobSQL() + `) AS failed,
		SUM(conclusion = 'cancelled' AND NOT ` + fleetFailedJobSQL() + `) AS cancelled,
		SUM(conclusion = 'skipped' AND NOT ` + fleetFailedJobSQL() + `) AS skipped,
		SUM(` + fleetFailedJobSQL() + `) AS faulted, SUM(` + workflowFailedJobSQL() + `) AS workflow_failed,
		SUM(matched = 0 AND state = 'queued' AND NOT hosted) AS unmatched,
		SUM(state = 'queued' AND provisioning = '' AND provision_now = 1) AS expedited,
		SUM(state = 'queued' AND provisioning = '` + ProvisioningPaused + `') AS paused,
		SUM(state = 'queued' AND provisioning = '` + ProvisioningDeleted + `') AS removed,
		MAX(managed) AS managed, MIN(hosted) AS hosted,
		SUM(cancel_requested_at IS NOT NULL AND state != 'completed') AS cancelling,
		` + runStateSQL + ` AS run_state, ` + runConclusionSQL() + ` AS run_conclusion
	FROM (
		SELECT jobs.*, ` + hostedJobSQL("jobs") + ` AS hosted, ` + managedJobSQL("jobs") + ` AS managed,
			MAX(run_attempt) OVER (PARTITION BY repo, github_run_id, job_name) AS latest_attempt
		FROM jobs` + restrict + `
	) AS jobs
	` + inner + `
	GROUP BY repo, github_run_id
) AS runs` + outer
	// The restriction's placeholders come first in the statement, inside the
	// subquery, ahead of the filter's.
	return from, append(append([]any{}, restrictArgs...), args...)
}

func scanWorkflowRun(sc interface{ Scan(...any) error }) (*WorkflowRun, error) {
	var r WorkflowRun
	var queued int64
	var started, completed sql.NullInt64
	var managed, hosted, cancelling int
	err := sc.Scan(&r.Repo, &r.GitHubRunID, &r.Workflow, &r.RunNumber, &r.RunAttempt, &r.HeadBranch,
		&r.HeadSHA, &r.InstallationID, &r.HTMLURL, &r.State, &r.Conclusion, &queued, &started, &completed,
		&r.Jobs.Total, &r.Jobs.Waiting, &r.Jobs.Queued, &r.Jobs.InProgress, &r.Jobs.Completed,
		&r.Jobs.Succeeded, &r.Jobs.Failed, &r.Jobs.Cancelled, &r.Jobs.Skipped, &r.Jobs.Faulted,
		&r.Jobs.Unmatched, &r.Jobs.Expedited, &r.Jobs.Paused, &r.Jobs.Removed, &managed, &hosted, &cancelling)
	if err != nil {
		return nil, err
	}
	r.QueuedAt = at(queued)
	r.StartedAt, r.CompletedAt = atp(started), atp(completed)
	r.Managed, r.Hosted, r.Cancelling = managed == 1, hosted == 1, cancelling > 0
	r.HTMLURL = runURL(r.HTMLURL)
	return &r, nil
}

// runURL is the run's page on GitHub, given a job's. A job's URL is the run's
// with "/job/<id>" on the end, so the run's is what comes before that; a URL
// that already names the run is left as it is.
func runURL(jobURL string) string {
	if i := strings.Index(jobURL, "/job/"); i >= 0 {
		return jobURL[:i]
	}
	return jobURL
}
