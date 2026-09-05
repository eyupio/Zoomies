package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

const jobCols = `id, github_job_id, github_run_id, repo, workflow, job_name, labels, state,
	conclusion, pool_id, runner_id, runner_name, html_url, queued_at, started_at,
	completed_at, matched, head_branch, head_sha, run_attempt, steps, runner_fault`

func scanJob(sc interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var queued int64
	var started, completed sql.NullInt64
	var matched int
	err := sc.Scan(&j.ID, &j.GitHubJobID, &j.GitHubRunID, &j.Repo, &j.Workflow, &j.JobName,
		&j.Labels, &j.State, &j.Conclusion, &j.PoolID, &j.RunnerID, &j.RunnerName,
		&j.HTMLURL, &queued, &started, &completed, &matched,
		&j.HeadBranch, &j.HeadSHA, &j.RunAttempt, &j.Steps, &j.RunnerFault)
	if err != nil {
		return nil, err
	}
	j.QueuedAt = at(queued)
	j.StartedAt, j.CompletedAt = atp(started), atp(completed)
	j.Matched = matched == 1
	return &j, nil
}

// UpsertJob inserts or updates a job keyed on its GitHub job ID.
//
// Webhook deliveries are at-least-once and can arrive out of order, so this
// merges rather than overwrites: a late "queued" delivery must not resurrect a
// job that has already completed.
func (s *Store) UpsertJob(ctx context.Context, j *Job) (*Job, error) {
	out, _, err := s.ApplyJob(ctx, j)
	return out, err
}

// ApplyJob is UpsertJob that also says what changed.
//
// The change is worked out inside the write transaction, against the row as it
// was, so two deliveries for the same job arriving together cannot both be
// told they moved it: exactly one of them did. That is what lets the caller
// write a job's timeline from deliveries GitHub may send twice.
func (s *Store) ApplyJob(ctx context.Context, j *Job) (*Job, JobChange, error) {
	var out *Job
	var change JobChange
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE github_job_id = ?`, j.GitHubJobID)
		existing, err := scanJob(row)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if j.ID == "" {
				j.ID = NewID(PrefixJob)
			}
			if j.QueuedAt.IsZero() {
				j.QueuedAt = s.Now()
			}
			j.Labels = NormalizeLabels(j.Labels)
			_, err := tx.ExecContext(ctx, `INSERT INTO jobs (`+jobCols+`)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				j.ID, j.GitHubJobID, j.GitHubRunID, j.Repo, j.Workflow, j.JobName, j.Labels,
				string(j.State), j.Conclusion, j.PoolID, j.RunnerID, j.RunnerName, j.HTMLURL,
				ms(j.QueuedAt), msp(j.StartedAt), msp(j.CompletedAt), boolInt(j.Matched),
				j.HeadBranch, j.HeadSHA, j.RunAttempt, j.Steps, j.RunnerFault)
			if err != nil {
				return err
			}
			out = j
			change = JobChange{Created: true, StateChanged: true, Claimed: j.Matched, RunnerLinked: j.RunnerID != ""}
			return nil
		case err != nil:
			return err
		}

		// Merge: never move a job backwards through its lifecycle.
		merged := *existing
		current := jobRank(j.State) >= jobRank(existing.State)
		if current {
			merged.State = j.State
			// The steps travel with the delivery that reports them, and a
			// stale delivery's steps are as stale as its state: an
			// "in_progress" that arrives after "completed" must not replace
			// every step's conclusion with "queued".
			if len(j.Steps) > 0 {
				merged.Steps = j.Steps
			}
		}
		if j.HeadBranch != "" {
			merged.HeadBranch = j.HeadBranch
		}
		if j.HeadSHA != "" {
			merged.HeadSHA = j.HeadSHA
		}
		if j.RunAttempt != 0 {
			merged.RunAttempt = j.RunAttempt
		}
		if j.RunnerFault != "" {
			merged.RunnerFault = j.RunnerFault
		}
		if j.Conclusion != "" {
			merged.Conclusion = j.Conclusion
		}
		if j.GitHubRunID != 0 {
			merged.GitHubRunID = j.GitHubRunID
		}
		if j.Repo != "" {
			merged.Repo = j.Repo
		}
		if j.Workflow != "" {
			merged.Workflow = j.Workflow
		}
		if j.JobName != "" {
			merged.JobName = j.JobName
		}
		if len(j.Labels) > 0 {
			merged.Labels = NormalizeLabels(j.Labels)
		}
		if j.PoolID != "" {
			merged.PoolID = j.PoolID
		}
		if j.RunnerID != "" {
			merged.RunnerID = j.RunnerID
		}
		if j.RunnerName != "" {
			merged.RunnerName = j.RunnerName
		}
		if j.HTMLURL != "" {
			merged.HTMLURL = j.HTMLURL
		}
		if j.StartedAt != nil && merged.StartedAt == nil {
			merged.StartedAt = j.StartedAt
		}
		if j.CompletedAt != nil {
			merged.CompletedAt = j.CompletedAt
		}
		merged.Matched = merged.Matched || j.Matched

		_, err = tx.ExecContext(ctx, `UPDATE jobs SET github_run_id=?, repo=?, workflow=?,
			job_name=?, labels=?, state=?, conclusion=?, pool_id=?, runner_id=?, runner_name=?,
			html_url=?, started_at=?, completed_at=?, matched=?, head_branch=?, head_sha=?,
			run_attempt=?, steps=?, runner_fault=? WHERE id=?`,
			merged.GitHubRunID, merged.Repo, merged.Workflow, merged.JobName, merged.Labels,
			string(merged.State), merged.Conclusion, merged.PoolID, merged.RunnerID,
			merged.RunnerName, merged.HTMLURL, msp(merged.StartedAt), msp(merged.CompletedAt),
			boolInt(merged.Matched), merged.HeadBranch, merged.HeadSHA, merged.RunAttempt,
			merged.Steps, merged.RunnerFault, merged.ID)
		if err != nil {
			return err
		}
		out = &merged
		change = JobChange{
			PreviousState: existing.State,
			StateChanged:  merged.State != existing.State,
			Claimed:       !existing.Matched && merged.Matched,
			RunnerLinked:  existing.RunnerID == "" && merged.RunnerID != "",
		}
		return nil
	})
	return out, change, err
}

// SetJobRunnerFault records what the runner executing a job said when it
// stopped before GitHub reported the job over, and returns the job as it now
// is.
//
// Only the first fault is kept. The agent may report the same exit more than
// once -- a runner report and then a task result -- and the first message is
// the one closest to the event.
func (s *Store) SetJobRunnerFault(ctx context.Context, jobID, fault string) (*Job, error) {
	var out *Job
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET runner_fault=? WHERE id=? AND runner_fault=''`, fault, jobID); err != nil {
			return err
		}
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, jobID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job %s: %w", jobID, ErrNotFound)
		}
		out = j
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Job timeline
// ---------------------------------------------------------------------------

const jobEventCols = `id, job_id, at, kind, source, message, runner_id, runner_name`

// AppendJobEvent adds one entry to a job's timeline.
func (s *Store) AppendJobEvent(ctx context.Context, e *JobEvent) error {
	if e.ID == "" {
		e.ID = NewID(PrefixJobEvent)
	}
	if e.At.IsZero() {
		e.At = s.Now()
	}
	_, err := s.exec(ctx, `INSERT INTO job_events (`+jobEventCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		e.ID, e.JobID, ms(e.At), string(e.Kind), e.Source, e.Message, e.RunnerID, e.RunnerName)
	return err
}

// ListJobEvents returns a job's timeline, oldest first.
func (s *Store) ListJobEvents(ctx context.Context, jobID string) ([]*JobEvent, error) {
	// Two entries written in the same millisecond -- "queued" and then
	// "claimed", every time -- keep the order they were written in, which
	// the rowid records and a random ID would not.
	rows, err := s.read.QueryContext(ctx, `SELECT `+jobEventCols+` FROM job_events
		WHERE job_id = ? ORDER BY at, rowid`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobEvent
	for rows.Next() {
		var e JobEvent
		var at64 int64
		if err := rows.Scan(&e.ID, &e.JobID, &at64, &e.Kind, &e.Source, &e.Message, &e.RunnerID, &e.RunnerName); err != nil {
			return nil, err
		}
		e.At = at(at64)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// jobRank orders job states so a stale webhook cannot rewind one.
func jobRank(s JobState) int {
	switch s {
	case JobQueued:
		return 1
	case JobInProgress:
		return 2
	case JobCompleted:
		return 3
	}
	return 0
}

// GetJob returns one job by internal ID.
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("job %s: %w", id, ErrNotFound)
	}
	return j, err
}

// GetJobByGitHubID returns one job by the ID GitHub assigned it.
func (s *Store) GetJobByGitHubID(ctx context.Context, ghID int64) (*Job, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE github_job_id = ?`, ghID)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("job %d: %w", ghID, ErrNotFound)
	}
	return j, err
}

// JobFilter narrows a job history listing. Every field is optional; the UI's
// filter bar maps one-to-one onto it.
type JobFilter struct {
	Repos       []string
	Workflows   []string
	PoolIDs     []string
	RunnerIDs   []string
	States      []JobState
	Conclusions []string
	Labels      []string
	Search      string
	Since       *time.Time
	Until       *time.Time
	// UnmatchedOnly surfaces queued jobs that no pool claims. It is deliberately
	// narrower than "matched = 0": a job that has already started or finished
	// was run by something -- another fleet, a hosted-runner vendor, GitHub
	// itself -- and calling it unmatched would report a fleet-wide fault every
	// time somebody kept one repository on runners this controller does not own.
	UnmatchedOnly bool
	// ManagedOnly narrows the list to jobs this controller has a hand in: one an
	// enabled pool claimed, or one that ran on a runner this fleet started.
	// GitHub tells us about every job in an installed repository, most of which
	// are somebody else's hosted runners, and a Jobs page that mixes the two
	// answers "why is my fleet slow?" with somebody else's numbers. Queued jobs
	// no pool claims stay in: nothing ran them, so they are this fleet's
	// problem to see, which is also why UnmatchedOnly wins over this flag.
	ManagedOnly bool
	// FailedOnly keeps the jobs that went wrong, on either side: a conclusion
	// GitHub counts as a failure, or a runner that stopped under the job. A
	// job whose runner died while GitHub still thinks it is running is
	// included -- that is the case an operator most wants to find.
	FailedOnly bool
	// FaultedOnly keeps only the jobs whose runner stopped under them. This is
	// the fleet's own failure rate, as distinct from the workflows'.
	FaultedOnly bool
}

var jobSortCols = map[string]string{
	"queued_at":    "queued_at",
	"started_at":   "started_at",
	"completed_at": "completed_at",
	"repo":         "repo",
	"workflow":     "workflow",
	"state":        "state",
	"duration":     "(COALESCE(completed_at,0) - COALESCE(started_at,0))",
	"queue_wait":   "(COALESCE(started_at,0) - queued_at)",
}

// ListJobs returns a filtered page of jobs and the matching total.
func (s *Store) ListJobs(ctx context.Context, f JobFilter, p Page) ([]*Job, int, error) {
	where, args := jobWhere(f)
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + jobCols + ` FROM jobs ` + where +
		` ORDER BY ` + p.orderBy(jobSortCols, "queued_at DESC") + ` LIMIT ? OFFSET ?`
	args = append(args, p.limit(50, 500), max(p.Offset, 0))
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

func jobWhere(f JobFilter) (string, []any) {
	var cond []string
	var args []any
	inClause := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		ph := make([]string, len(vals))
		for i, v := range vals {
			ph[i] = "?"
			args = append(args, v)
		}
		cond = append(cond, col+` IN (`+strings.Join(ph, ",")+`)`)
	}
	inClause("repo", f.Repos)
	inClause("workflow", f.Workflows)
	inClause("pool_id", f.PoolIDs)
	inClause("runner_id", f.RunnerIDs)
	inClause("conclusion", f.Conclusions)
	if len(f.States) > 0 {
		ph := make([]string, len(f.States))
		for i, st := range f.States {
			ph[i] = "?"
			args = append(args, string(st))
		}
		cond = append(cond, `state IN (`+strings.Join(ph, ",")+`)`)
	}
	// Labels are stored as a JSON array; a LIKE on the quoted label is exact
	// enough because NormalizeLabels guarantees no embedded quotes.
	for _, l := range NormalizeLabels(f.Labels) {
		cond = append(cond, `labels LIKE ?`)
		args = append(args, `%"`+l+`"%`)
	}
	if f.Since != nil {
		cond = append(cond, `queued_at >= ?`)
		args = append(args, ms(*f.Since))
	}
	if f.Until != nil {
		cond = append(cond, `queued_at <= ?`)
		args = append(args, ms(*f.Until))
	}
	if f.UnmatchedOnly {
		cond = append(cond, `matched = 0 AND state = ?`)
		args = append(args, string(JobQueued))
	} else if f.ManagedOnly {
		// A queued job no pool claims is kept: it is unclaimed rather than
		// somebody else's, and hiding it would hide the fault the page exists
		// to show.
		cond = append(cond, `(matched = 1 OR pool_id != '' OR runner_id != '' OR (matched = 0 AND state = ?))`)
		args = append(args, string(JobQueued))
	}
	if f.FaultedOnly {
		cond = append(cond, `runner_fault != ''`)
	} else if f.FailedOnly {
		cond = append(cond, `(conclusion IN ('failure','timed_out','startup_failure') OR runner_fault != '')`)
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		cond = append(cond, `(repo LIKE ? OR workflow LIKE ? OR job_name LIKE ? OR runner_name LIKE ?)`)
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	if len(cond) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(cond, " AND "), args
}

// ListQueuedJobs returns jobs still waiting for a runner, oldest first. This is
// the scheduler's demand signal.
func (s *Store) ListQueuedJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs
		WHERE state = 'queued' ORDER BY queued_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobDistinct returns the distinct values of a filterable column, for
// populating the job filter's dropdowns without a full table scan client-side.
func (s *Store) JobDistinct(ctx context.Context, column string, limit int) ([]string, error) {
	allowed := map[string]bool{"repo": true, "workflow": true, "conclusion": true}
	if !allowed[column] {
		return nil, fmt.Errorf("store: %q is not a filterable job column", column)
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT DISTINCT `+column+` FROM jobs WHERE `+column+` != '' ORDER BY 1 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// JobStats summarises a time window for the Overview cards.
type JobStats struct {
	Queued        int           `json:"queued"`
	Running       int           `json:"running"`
	CompletedLast int           `json:"completed"`
	Failed        int           `json:"failed"`
	MedianWait    time.Duration `json:"-"`
	P95Wait       time.Duration `json:"-"`
	MedianWaitMS  int64         `json:"median_wait_ms"`
	P95WaitMS     int64         `json:"p95_wait_ms"`
}

// StatsSince computes queue and outcome statistics over a rolling window.
func (s *Store) StatsSince(ctx context.Context, since time.Time) (JobStats, error) {
	var st JobStats
	err := s.read.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM jobs WHERE state='queued'),
		(SELECT COUNT(*) FROM jobs WHERE state='in_progress'),
		(SELECT COUNT(*) FROM jobs WHERE state='completed' AND completed_at >= ?),
		(SELECT COUNT(*) FROM jobs WHERE state='completed' AND conclusion='failure' AND completed_at >= ?)`,
		ms(since), ms(since)).Scan(&st.Queued, &st.Running, &st.CompletedLast, &st.Failed)
	if err != nil {
		return st, err
	}
	waits, err := s.queueWaits(ctx, since)
	if err != nil {
		return st, err
	}
	st.MedianWait = percentile(waits, 0.50)
	st.P95Wait = percentile(waits, 0.95)
	st.MedianWaitMS = st.MedianWait.Milliseconds()
	st.P95WaitMS = st.P95Wait.Milliseconds()
	return st, nil
}

func (s *Store) queueWaits(ctx context.Context, since time.Time) ([]time.Duration, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT started_at - queued_at FROM jobs
		WHERE started_at IS NOT NULL AND queued_at >= ? ORDER BY 1`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Duration
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v < 0 {
			v = 0
		}
		out = append(out, time.Duration(v)*time.Millisecond)
	}
	return out, rows.Err()
}

// percentile returns the p-th percentile of a pre-sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

// PruneJobs deletes completed jobs older than the cutoff, and their timelines
// with them: an event whose job is gone answers nothing.
func (s *Store) PruneJobs(ctx context.Context, before time.Time) (int64, error) {
	var pruned int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE job_id IN
			(SELECT id FROM jobs WHERE state='completed' AND completed_at < ?)`, ms(before)); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE state='completed' AND completed_at < ?`, ms(before))
		if err != nil {
			return err
		}
		pruned, err = res.RowsAffected()
		return err
	})
	return pruned, err
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

const auditCols = `id, actor_id, actor_name, actor_kind, action, target_kind, target_id,
	before, after, ip, created_at`

// AppendAudit records one mutating action.
func (s *Store) AppendAudit(ctx context.Context, e *AuditEvent) error {
	if e.ID == "" {
		e.ID = NewID(PrefixAudit)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.Now()
	}
	if e.ActorKind == "" {
		e.ActorKind = "system"
	}
	_, err := s.exec(ctx, `INSERT INTO audit_events (`+auditCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ActorID, e.ActorName, e.ActorKind, e.Action, e.TargetKind, e.TargetID,
		e.Before, e.After, e.IP, ms(e.CreatedAt))
	return err
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	ActorIDs    []string
	Actions     []string
	TargetKinds []string
	TargetID    string
	Search      string
	Since       *time.Time
	Until       *time.Time
}

var auditSortCols = map[string]string{
	"created_at": "created_at",
	"action":     "action",
	"actor":      "actor_name",
}

// ListAudit returns a filtered page of audit events, newest first.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter, p Page) ([]*AuditEvent, int, error) {
	var cond []string
	var args []any
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		ph := make([]string, len(vals))
		for i, v := range vals {
			ph[i] = "?"
			args = append(args, v)
		}
		cond = append(cond, col+` IN (`+strings.Join(ph, ",")+`)`)
	}
	in("actor_id", f.ActorIDs)
	in("action", f.Actions)
	in("target_kind", f.TargetKinds)
	if f.TargetID != "" {
		cond = append(cond, `target_id = ?`)
		args = append(args, f.TargetID)
	}
	if f.Since != nil {
		cond = append(cond, `created_at >= ?`)
		args = append(args, ms(*f.Since))
	}
	if f.Until != nil {
		cond = append(cond, `created_at <= ?`)
		args = append(args, ms(*f.Until))
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		cond = append(cond, `(actor_name LIKE ? OR action LIKE ? OR target_id LIKE ?)`)
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	where := ""
	if len(cond) > 0 {
		where = "WHERE " + strings.Join(cond, " AND ")
	}
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + auditCols + ` FROM audit_events ` + where +
		` ORDER BY ` + p.orderBy(auditSortCols, "created_at DESC") + ` LIMIT ? OFFSET ?`
	args = append(args, p.limit(50, 500), max(p.Offset, 0))
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*AuditEvent
	for rows.Next() {
		var e AuditEvent
		var created int64
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.ActorKind, &e.Action,
			&e.TargetKind, &e.TargetID, &e.Before, &e.After, &e.IP, &created); err != nil {
			return nil, 0, err
		}
		e.CreatedAt = at(created)
		out = append(out, &e)
	}
	return out, total, rows.Err()
}

// AuditActions returns the distinct action names present, for the filter menu.
func (s *Store) AuditActions(ctx context.Context) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT action FROM audit_events ORDER BY action LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Scaling events
// ---------------------------------------------------------------------------

// AppendScalingEvent records one scheduler decision.
func (s *Store) AppendScalingEvent(ctx context.Context, e *ScalingEvent) error {
	if e.ID == "" {
		e.ID = NewID(PrefixScaling)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.Now()
	}
	_, err := s.exec(ctx, `INSERT INTO scaling_events
		(id, pool_id, pool_name, from_count, to_count, reason, created_at) VALUES (?,?,?,?,?,?,?)`,
		e.ID, e.PoolID, e.PoolName, e.From, e.To, e.Reason, ms(e.CreatedAt))
	return err
}

// ListScalingEvents returns recent scaling decisions, newest first.
func (s *Store) ListScalingEvents(ctx context.Context, poolID string, limit int) ([]*ScalingEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, pool_id, pool_name, from_count, to_count, reason, created_at FROM scaling_events`
	var args []any
	if poolID != "" {
		q += ` WHERE pool_id = ?`
		args = append(args, poolID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ScalingEvent
	for rows.Next() {
		var e ScalingEvent
		var created int64
		if err := rows.Scan(&e.ID, &e.PoolID, &e.PoolName, &e.From, &e.To, &e.Reason, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = at(created)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// PruneScalingEvents deletes scaling history older than the cutoff.
func (s *Store) PruneScalingEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM scaling_events WHERE created_at < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Webhook deliveries
// ---------------------------------------------------------------------------

// RecordDelivery stores the outcome of one inbound webhook.
func (s *Store) RecordDelivery(ctx context.Context, d *WebhookDelivery) error {
	if d.ID == "" {
		d.ID = NewID(PrefixDelivery)
	}
	if d.ReceivedAt.IsZero() {
		d.ReceivedAt = s.Now()
	}
	_, err := s.exec(ctx, `INSERT INTO webhook_deliveries
		(id, delivery_id, event, action, repo, status, error, received_at) VALUES (?,?,?,?,?,?,?,?)`,
		d.ID, d.DeliveryID, d.Event, d.Action, d.Repo, d.Status, d.Error, ms(d.ReceivedAt))
	return err
}

// ListDeliveries returns recent webhook deliveries, newest first. A non-empty
// status filters to just that outcome.
func (s *Store) ListDeliveries(ctx context.Context, status string, limit int) ([]*WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, delivery_id, event, action, repo, status, error, received_at FROM webhook_deliveries`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY received_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		var recv int64
		if err := rows.Scan(&d.ID, &d.DeliveryID, &d.Event, &d.Action, &d.Repo,
			&d.Status, &d.Error, &recv); err != nil {
			return nil, err
		}
		d.ReceivedAt = at(recv)
		out = append(out, &d)
	}
	return out, rows.Err()
}

// CountFailedDeliveries returns how many deliveries failed since a cutoff.
func (s *Store) CountFailedDeliveries(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries
		WHERE status != 'accepted' AND received_at >= ?`, ms(since)).Scan(&n)
	return n, err
}

// LastDeliveryAt returns when a webhook was last received, whether or not it
// verified, or the zero time. The webhook health check uses it to tell
// "webhooks are quiet" from "GitHub has never reached this address".
func (s *Store) LastDeliveryAt(ctx context.Context) (time.Time, error) {
	return s.lastDeliveryAt(ctx, "")
}

// LastAcceptedDeliveryAt returns when a webhook last verified, or the zero
// time. This is the one the poller stands down on: a delivery whose signature
// did not verify started no runner, so a stream of them -- a mistyped secret,
// say -- is exactly the case the fallback poller exists for.
func (s *Store) LastAcceptedDeliveryAt(ctx context.Context) (time.Time, error) {
	return s.lastDeliveryAt(ctx, "accepted")
}

func (s *Store) lastDeliveryAt(ctx context.Context, status string) (time.Time, error) {
	var v sql.NullInt64
	var err error
	if status == "" {
		err = s.read.QueryRowContext(ctx, `SELECT MAX(received_at) FROM webhook_deliveries`).Scan(&v)
	} else {
		err = s.read.QueryRowContext(ctx, `SELECT MAX(received_at) FROM webhook_deliveries WHERE status = ?`, status).Scan(&v)
	}
	if err != nil || !v.Valid {
		return time.Time{}, err
	}
	return at(v.Int64), nil
}

// PruneDeliveries deletes webhook history older than the cutoff.
func (s *Store) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM webhook_deliveries WHERE received_at < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Fleet samples (Overview sparklines)
// ---------------------------------------------------------------------------

// FleetSample is one point on the Overview's sparklines.
type FleetSample struct {
	At           time.Time `json:"at"`
	QueuedJobs   int       `json:"queued_jobs"`
	RunningJobs  int       `json:"running_jobs"`
	IdleRunners  int       `json:"idle_runners"`
	BusyRunners  int       `json:"busy_runners"`
	TotalRunners int       `json:"total_runners"`
}

// RecordSample stores one fleet sample, replacing any sample for the same
// minute so a restart mid-minute cannot double-count.
func (s *Store) RecordSample(ctx context.Context, f FleetSample) error {
	minute := f.At.UTC().Truncate(time.Minute)
	_, err := s.exec(ctx, `INSERT INTO fleet_samples
		(at, queued_jobs, running_jobs, idle_runners, busy_runners, total_runners)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(at) DO UPDATE SET queued_jobs=excluded.queued_jobs,
			running_jobs=excluded.running_jobs, idle_runners=excluded.idle_runners,
			busy_runners=excluded.busy_runners, total_runners=excluded.total_runners`,
		ms(minute), f.QueuedJobs, f.RunningJobs, f.IdleRunners, f.BusyRunners, f.TotalRunners)
	return err
}

// ListSamples returns fleet samples since a cutoff, oldest first.
func (s *Store) ListSamples(ctx context.Context, since time.Time) ([]FleetSample, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT at, queued_jobs, running_jobs, idle_runners,
		busy_runners, total_runners FROM fleet_samples WHERE at >= ? ORDER BY at`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetSample
	for rows.Next() {
		var f FleetSample
		var t int64
		if err := rows.Scan(&t, &f.QueuedJobs, &f.RunningJobs, &f.IdleRunners,
			&f.BusyRunners, &f.TotalRunners); err != nil {
			return nil, err
		}
		f.At = at(t)
		out = append(out, f)
	}
	return out, rows.Err()
}

// PruneSamples deletes fleet samples older than the cutoff.
func (s *Store) PruneSamples(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM fleet_samples WHERE at < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
