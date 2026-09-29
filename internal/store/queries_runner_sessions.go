package store

import (
	"context"
	"database/sql"
	"time"
)

// RunnerSession is one runner's life as the usage ledger keeps it, after the
// runner row itself may have been pruned. It is written once and never
// updated; see migration 0047 for why each column is there.
type RunnerSession struct {
	RunnerID          string     `json:"runner_id"`
	PoolID            string     `json:"pool_id"`
	HostID            string     `json:"host_id"`
	InstallationID    string     `json:"installation_id"`
	JobID             string     `json:"job_id,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	RegisteredAt      *time.Time `json:"registered_at,omitempty"`
	FinishedAt        time.Time  `json:"finished_at"`
	CleanedUpAt       *time.Time `json:"cleaned_up_at,omitempty"`
	CostPerRunnerHour *float64   `json:"cost_per_runner_hour,omitempty"`
	RecordedAt        time.Time  `json:"recorded_at"`
}

// insertRunnerSessionSQL copies what the ledger needs from a runner row. The
// caller supplies the WHERE clause's argument (the runner, or the prune
// cutoff) after recorded_at.
//
// ON CONFLICT DO NOTHING is the exactly-once guarantee: a confirmation that
// is replayed after a restart, or a prune of a runner whose session was
// already written, finds the row there and leaves it alone. The first write
// is the one the ledger keeps, because it was taken nearest the event.
//
// finished_at falls back to the cleanup stamps and then to recorded_at so a
// session always has an end: a runner row that never had finished_at set was
// still gone by the time anyone wrote this.
//
// create_task_issued_at and the job's eligible_at are the two ends the
// installation report's scheduling interval needs once the runner and job rows
// are gone (migration 0051).
const insertRunnerSessionSQL = `INSERT INTO runner_sessions (runner_id, pool_id, host_id, installation_id, job_id,
	started_at, registered_at, finished_at, cleaned_up_at, cost_per_runner_hour, recorded_at,
	create_task_issued_at, job_eligible_at)
SELECT r.id, r.pool_id, COALESCE(r.host_id, ''), COALESCE(p.installation_id, ''), x.job_id,
	r.created_at, r.registered_at, COALESCE(r.finished_at, r.host_removed_at, r.cleaned_up_at, ?1), r.cleaned_up_at,
	p.cost_per_runner_hour, ?1,
	r.create_task_issued_at, (SELECT j.eligible_at FROM jobs j WHERE j.id = x.job_id)
FROM runners r LEFT JOIN pools p ON p.id = r.pool_id
JOIN (SELECT r2.id AS runner_id, COALESCE(NULLIF(r2.current_job_id, ''),
		(SELECT j.id FROM jobs j WHERE j.runner_id = r2.id ORDER BY j.queued_at DESC LIMIT 1), '') AS job_id
	FROM runners r2) x ON x.runner_id = r.id
WHERE `

// recordRunnerSession writes the session of one runner inside the caller's
// transaction, so the session and the stamp that caused it commit together.
func recordRunnerSession(ctx context.Context, tx *sql.Tx, id string, now int64) error {
	_, err := tx.ExecContext(ctx, insertRunnerSessionSQL+`r.id = ?2 ON CONFLICT(runner_id) DO NOTHING`, now, id)
	return err
}

// recordSessionsWhere writes the sessions of every runner the predicate
// selects, for a delete that is about to take those rows with it. The
// predicate takes the scope's ID as ?2. It must run before the delete, and
// before the pool or installation row goes, because the session copies the
// installation and the rate from the pool.
//
// A runner that is still live, or removed but not yet confirmed clean, has no
// session and never will once its row is gone; without this the usage ledger
// would be short by exactly the runners a release, a re-join or a forced delete
// caught mid-life. Those already recorded are left alone by the conflict clause.
func recordSessionsWhere(ctx context.Context, tx *sql.Tx, now int64, where string, id string) error {
	_, err := tx.ExecContext(ctx, insertRunnerSessionSQL+where+` ON CONFLICT(runner_id) DO NOTHING`, now, id)
	return err
}

// RunnerSessions returns the sessions that overlap [from, to), oldest first.
func (s *Store) RunnerSessions(ctx context.Context, from, to time.Time) ([]*RunnerSession, error) {
	return querySessions(ctx, s.read, ms(from), ms(to))
}

// querySessions is RunnerSessions against either handle, so the roll-up can
// read the sessions inside the transaction that moves its watermark.
func querySessions(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, from, to int64) ([]*RunnerSession, error) {
	rows, err := q.QueryContext(ctx, `SELECT runner_id, pool_id, host_id, installation_id, job_id,
		started_at, registered_at, finished_at, cleaned_up_at, cost_per_runner_hour, recorded_at
		FROM runner_sessions WHERE started_at < ? AND finished_at > ? ORDER BY started_at, runner_id`, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunnerSession
	for rows.Next() {
		var x RunnerSession
		var started, finished, recorded int64
		var registered, cleaned sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&x.RunnerID, &x.PoolID, &x.HostID, &x.InstallationID, &x.JobID,
			&started, &registered, &finished, &cleaned, &cost, &recorded); err != nil {
			return nil, err
		}
		x.StartedAt, x.FinishedAt, x.RecordedAt = time.UnixMilli(started), time.UnixMilli(finished), time.UnixMilli(recorded)
		if registered.Valid {
			t := time.UnixMilli(registered.Int64)
			x.RegisteredAt = &t
		}
		if cleaned.Valid {
			t := time.UnixMilli(cleaned.Int64)
			x.CleanedUpAt = &t
		}
		if cost.Valid {
			c := cost.Float64
			x.CostPerRunnerHour = &c
		}
		out = append(out, &x)
	}
	return out, rows.Err()
}

// PruneRunnerSessions deletes sessions that finished before the cutoff.
//
// It never deletes one the usage roll-up has not yet absorbed: the session is
// the roll-up's only source, and a session pruned first would leave its days
// short however long the roll-up itself is kept. Before anything has been
// rolled up, nothing is pruned.
func (s *Store) PruneRunnerSessions(ctx context.Context, before time.Time) (int64, error) {
	r, err := s.exec(ctx, `DELETE FROM runner_sessions WHERE finished_at < MIN(?,
		COALESCE((SELECT rolled_until FROM usage_rollup WHERE id = 1), 0))`, ms(before))
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
