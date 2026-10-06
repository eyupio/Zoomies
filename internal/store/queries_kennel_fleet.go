package store

import (
	"context"
	"sort"
	"time"
)

// What Kennel Club asks the jobs table. They are questions about this fleet's
// own record -- no GitHub call -- and they reuse managedJobSQL and hostedJobSQL,
// so "a job this fleet had a hand in" means here exactly what it means on the
// Jobs page.

// KennelServed is a repository this fleet has served, and the installation the
// jobs were attributed to.
type KennelServed struct {
	Repo           string
	InstallationID string
	Jobs           int
}

// KennelServedRepos lists the repositories the fleet had a hand in a job for
// since the given moment, or has a job waiting for that no pool serves. A
// repository served under two installations appears twice, and the caller
// chooses between them by the same precedence everything else uses.
func (s *Store) KennelServedRepos(ctx context.Context, since time.Time) ([]KennelServed, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT repo, installation_id, COUNT(*) FROM jobs
		WHERE queued_at >= ? AND repo <> '' AND installation_id <> '' AND `+managedJobSQL("jobs")+`
		GROUP BY repo, installation_id ORDER BY repo, installation_id`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KennelServed
	for rows.Next() {
		var k KennelServed
		if err := rows.Scan(&k.Repo, &k.InstallationID, &k.Jobs); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// KennelFleetQuery says what to count and how far back.
type KennelFleetQuery struct {
	// Repo is the repository's full name as GitHub reports it.
	Repo string
	// Since is the start of the window the counts reach back to.
	Since time.Time
	// UnservedSince is how far back to look for jobs that waited for a label no
	// pool serves, which is a shorter memory than the window.
	UnservedSince time.Time
	// LongFloor is the shortest job to report in Long.
	LongFloor time.Duration
}

// KennelPoolJobs is how many of a repository's jobs one pool ran.
type KennelPoolJobs struct {
	PoolID string
	Jobs   int
}

// KennelLongJob is a finished job that held a runner for a long time.
type KennelLongJob struct {
	Duration   time.Duration
	Conclusion string
}

// Caps on what a fleet-facts query returns, so a repository with a great many
// stuck jobs cannot make a snapshot large. They are well above what a finding
// needs: a count and the longest.
const (
	kennelMaxUnserved = 200
	kennelMaxLong     = 500
)

// KennelFleetFacts is what the fleet observed about one repository.
type KennelFleetFacts struct {
	// Ran is the jobs a runner of this fleet executed inside the window.
	Ran int
	// Queued is the jobs a pool has claimed that are waiting right now.
	Queued int
	Pools  []KennelPoolJobs
	// Unserved is how long each job waited for a label no pool serves, longest
	// first. A job still queued has waited until now; one that was cancelled
	// without ever starting waited until it was cancelled. A job that started
	// is not here: something ran it, and "another fleet, a vendor, GitHub
	// itself" is not this fleet's fault -- the rule JobFilter.UnmatchedOnly keeps.
	Unserved []time.Duration
	Long     []KennelLongJob
}

// KennelFleetFacts answers the five questions the exposure and capacity checks
// ask of the jobs table.
func (s *Store) KennelFleetFacts(ctx context.Context, q KennelFleetQuery) (*KennelFleetFacts, error) {
	out := &KennelFleetFacts{}
	since := ms(q.Since)
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE repo = ? AND queued_at >= ? AND runner_id <> ''`,
		q.Repo, since).Scan(&out.Ran); err != nil {
		return nil, err
	}
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE repo = ? AND state = ? AND matched = 1`,
		q.Repo, string(JobQueued)).Scan(&out.Queued); err != nil {
		return nil, err
	}

	pools, err := s.read.QueryContext(ctx, `SELECT pool_id, COUNT(*) FROM jobs
		WHERE repo = ? AND queued_at >= ? AND runner_id <> '' AND pool_id <> ''
		GROUP BY pool_id ORDER BY COUNT(*) DESC, pool_id`, q.Repo, since)
	if err != nil {
		return nil, err
	}
	defer pools.Close()
	for pools.Next() {
		var p KennelPoolJobs
		if err := pools.Scan(&p.PoolID, &p.Jobs); err != nil {
			return nil, err
		}
		out.Pools = append(out.Pools, p)
	}
	if err := pools.Err(); err != nil {
		return nil, err
	}

	now := s.Now()
	waits, err := s.read.QueryContext(ctx, `SELECT state, queued_at, completed_at FROM jobs
		WHERE repo = ? AND matched = 0 AND NOT `+hostedJobSQL("jobs")+` AND queued_at >= ?
		  AND (state = ? OR (state = ? AND started_at IS NULL AND completed_at IS NOT NULL))
		ORDER BY queued_at LIMIT ?`,
		q.Repo, ms(q.UnservedSince), string(JobQueued), string(JobCompleted), kennelMaxUnserved)
	if err != nil {
		return nil, err
	}
	defer waits.Close()
	for waits.Next() {
		var state string
		var queued int64
		var completed *int64
		if err := waits.Scan(&state, &queued, &completed); err != nil {
			return nil, err
		}
		end := now
		if state == string(JobCompleted) && completed != nil {
			end = at(*completed)
		}
		out.Unserved = append(out.Unserved, max(end.Sub(at(queued)), 0))
	}
	if err := waits.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out.Unserved, func(i, j int) bool { return out.Unserved[i] > out.Unserved[j] })

	long, err := s.read.QueryContext(ctx, `SELECT completed_at - started_at, conclusion FROM jobs
		WHERE repo = ? AND state = ? AND runner_id <> '' AND started_at IS NOT NULL AND completed_at IS NOT NULL
		  AND completed_at >= ? AND completed_at - started_at >= ?
		ORDER BY completed_at DESC LIMIT ?`,
		q.Repo, string(JobCompleted), since, q.LongFloor.Milliseconds(), kennelMaxLong)
	if err != nil {
		return nil, err
	}
	defer long.Close()
	for long.Next() {
		var d int64
		var j KennelLongJob
		if err := long.Scan(&d, &j.Conclusion); err != nil {
			return nil, err
		}
		j.Duration = time.Duration(d) * time.Millisecond
		out.Long = append(out.Long, j)
	}
	return out, long.Err()
}

// KennelRun is a workflow run the fleet ran jobs of, as the jobs table knows
// it: its GitHub ID and when its first job was queued.
type KennelRun struct {
	ID       int64
	QueuedAt time.Time
}

// KennelRunsAfter lists the runs this fleet ran jobs of for a repository, newest
// first, whose GitHub run ID is above the watermark. The newest are taken when
// there are more than the limit, which is what makes the read a sample on a
// repository too busy to catch up with, and is why the controller says so.
func (s *Store) KennelRunsAfter(ctx context.Context, repo string, after int64, since time.Time, limit int) ([]KennelRun, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT github_run_id, MIN(queued_at) FROM jobs
		WHERE repo = ? AND queued_at >= ? AND runner_id <> '' AND github_run_id > ?
		GROUP BY github_run_id ORDER BY github_run_id DESC LIMIT ?`,
		repo, ms(since), after, min(max(limit, 1), 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KennelRun
	for rows.Next() {
		var r KennelRun
		var queued int64
		if err := rows.Scan(&r.ID, &queued); err != nil {
			return nil, err
		}
		r.QueuedAt = at(queued)
		out = append(out, r)
	}
	return out, rows.Err()
}
