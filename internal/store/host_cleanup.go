package store

import (
	"context"
	"time"
)

// PendingHostCleanup recovers removal intent from completed jobs, failed host
// removals, and finished runners nobody has confirmed gone since before
// unconfirmedBefore. A keyset cursor prevents offline hosts starving later rows.
//
// The last of the three is the one a lost report leaves behind. A runner
// withdrawn while idle ran no job, so its row carried nothing that said the
// host still owed a confirmation, and the agent's one report of the removal
// (sent after it has kept the container for its logs) was the only thing
// that could ever stamp it. A restart on either side in that window left the
// row waiting for ever, with no error to show and nothing left on the host.
// Asking again is safe: a removal the host already did is answered as done.
// The grace is the caller's, and outlasts the agent's own removal window so
// that a container kept for its logs is not taken early.
func (s *Store) PendingHostCleanup(ctx context.Context, afterID string, limit int, unconfirmedBefore time.Time) ([]*Runner, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+runnerCols+` FROM runners
		WHERE id > ? AND state IN ('removed', 'failed') AND host_removed_at IS NULL
		AND (host_cleanup_error != '' OR EXISTS
			(SELECT 1 FROM jobs WHERE jobs.runner_id=runners.id AND jobs.state='completed')
			OR COALESCE(finished_at, created_at) < ?)
		AND NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.runner_id=runners.id AND jobs.state='in_progress')
		ORDER BY id LIMIT ?`, afterID, ms(unconfirmedBefore), min(max(limit, 1), 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
