package store

import (
	"context"
	"database/sql"
	"errors"
)

// JobUsageKey names one job the way its history is kept: the same job of the
// same workflow in the same repository, run by the same pool. The pool is part
// of it because the same job on a docker-in-docker pool measures its daemon
// too, and on a pool with a smaller limit is capped by it.
type JobUsageKey struct {
	Repo     string `json:"repo"`
	Workflow string `json:"workflow"`
	JobName  string `json:"job_name"`
	PoolID   string `json:"pool_id"`
}

// JobPeak is one finished run of a job: the most it was measured using, and
// whether the kernel killed it for memory.
type JobPeak struct {
	CPUs      float64 `json:"cpus"`
	MemoryMB  int64   `json:"memory_mb"`
	OOMKilled bool    `json:"oom_killed"`
}

// JobUsageHistoryLimit is how many recent runs of one job a profile is taken
// from. Enough for a ninetieth percentile to mean something, few enough that a
// job whose needs changed a fortnight ago is profiled on what it is now.
const JobUsageHistoryLimit = 20

// RecordJobUsage raises the peaks of the job in progress on a runner to one
// sample of that runner's usage.
//
// Only a job GitHub says is in progress is touched, so the samples of a runner
// still starting or already finished are never charged to a job, and the
// state predicate is what lets the planner find the row through the queued and
// in-progress index rather than walking every job -- this runs on every
// heartbeat that carries a sample. MAX keeps a late or repeated sample from
// lowering a peak.
func (s *Store) RecordJobUsage(ctx context.Context, runnerID string, cpus float64, memoryMB int64) error {
	if runnerID == "" || (cpus <= 0 && memoryMB <= 0) {
		return nil
	}
	_, err := s.exec(ctx, `UPDATE jobs SET peak_cpus = MAX(peak_cpus, ?), peak_memory_mb = MAX(peak_memory_mb, ?)
		WHERE state = 'in_progress' AND runner_id = ?`, max(cpus, 0), max(memoryMB, 0), runnerID)
	return err
}

// MarkJobOOMKilled records that the kernel killed something in a runner for
// its memory limit, on the job that runner ran last, and returns that job and
// whether this call is the one that marked it.
//
// The job may already be over. A step killed with exit 137 leaves a runner
// that finishes the job, and the container reports the kill only when it
// exits -- after GitHub has already said the job failed, like any test. The
// fault is recorded all the same, because the whole point is that it was not
// the workflow's: the category is set when the job has none, and its sentence
// only then, so a runner fault recorded first keeps its own words.
func (s *Store) MarkJobOOMKilled(ctx context.Context, runnerID, message string) (*Job, bool, error) {
	if runnerID == "" {
		return nil, false, nil
	}
	var out *Job
	var marked bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE runner_id = ?
			ORDER BY queued_at DESC LIMIT 1`, runnerID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if j.OOMKilled {
			out = j
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET oom_killed = 1,
			fault_kind = CASE WHEN fault_kind = '' THEN ? ELSE fault_kind END,
			runner_fault = CASE WHEN runner_fault = '' THEN ? ELSE runner_fault END
			WHERE id = ?`, FaultOutOfMemory, message, j.ID); err != nil {
			return err
		}
		marked = true
		out, err = scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, j.ID))
		return err
	})
	return out, marked, err
}

// JobUsageHistory returns the most recent measured runs of each job named,
// newest first, split by the pool that ran them. Keys are matched on their
// repository, workflow and job name; the pool in the answer's keys is the
// pool each run was on.
func (s *Store) JobUsageHistory(ctx context.Context, keys []JobUsageKey) (map[JobUsageKey][]JobPeak, error) {
	out := map[JobUsageKey][]JobPeak{}
	type triple struct{ repo, workflow, job string }
	seen := map[triple]bool{}
	for _, k := range keys {
		t := triple{k.Repo, k.Workflow, k.JobName}
		if seen[t] {
			continue
		}
		seen[t] = true
		if err := s.jobUsageHistory(ctx, t.repo, t.workflow, t.job, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// jobUsageHistorySQL is a const so a query-plan test can pin the statement
// that ships: it must seek idx_jobs_usage_profile, not scan the table, because
// it runs for every distinct queued job on every scheduling pass.
const jobUsageHistorySQL = `SELECT pool_id, peak_cpus, peak_memory_mb, oom_killed FROM jobs
	WHERE repo = ? AND workflow = ? AND job_name = ? AND state = 'completed'
	AND (peak_memory_mb > 0 OR peak_cpus > 0 OR oom_killed = 1)
	ORDER BY completed_at DESC LIMIT ?`

func (s *Store) jobUsageHistory(ctx context.Context, repo, workflow, job string, out map[JobUsageKey][]JobPeak) error {
	// Several pools can share a job's history, so a little more than one
	// profile's worth is read and each pool keeps its own newest.
	rows, err := s.read.QueryContext(ctx, jobUsageHistorySQL, repo, workflow, job, JobUsageHistoryLimit*3)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p JobPeak
		var pool string
		var oom int
		if err := rows.Scan(&pool, &p.CPUs, &p.MemoryMB, &oom); err != nil {
			return err
		}
		p.OOMKilled = oom == 1
		k := JobUsageKey{Repo: repo, Workflow: workflow, JobName: job, PoolID: pool}
		if len(out[k]) < JobUsageHistoryLimit {
			out[k] = append(out[k], p)
		}
	}
	return rows.Err()
}
