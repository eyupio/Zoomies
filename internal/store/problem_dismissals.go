package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrTooManyDismissals is what ApplyProblemDismissals returns when the change
// would leave an account holding more than the limit it was given. Nothing of
// the change is applied.
var ErrTooManyDismissals = errors.New("too many dismissed problems")

// ProblemDismissal is one problem, or one kind of problem, an account has put
// away. The store treats Key as opaque: what makes two reports "the same
// problem" is the UI's definition, and a second one here would only drift.
type ProblemDismissal struct {
	Key         string
	Severity    string
	DismissedAt time.Time
	// Until is when a snooze ends. Nil is a dismissal that lasts until the
	// client sees the problem resolved.
	Until *time.Time
}

// ProblemDismissals returns what the account has put away and that still
// stands. A snooze whose time has passed is spent, so it is deleted here rather
// than returned: reading is the one moment every client passes through, which
// keeps the table from collecting rows nobody will ever look at again.
func (s *Store) ProblemDismissals(ctx context.Context, userID string) ([]ProblemDismissal, error) {
	if !s.readOnly {
		if _, err := s.exec(ctx,
			`DELETE FROM problem_dismissals WHERE user_id = ? AND until IS NOT NULL AND until <= ?`,
			userID, ms(s.Now())); err != nil {
			return nil, err
		}
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT key, severity, dismissed_at, until FROM problem_dismissals
		 WHERE user_id = ? ORDER BY dismissed_at, key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProblemDismissal{}
	for rows.Next() {
		var (
			d     ProblemDismissal
			dAt   int64
			until sql.NullInt64
		)
		if err := rows.Scan(&d.Key, &d.Severity, &dAt, &until); err != nil {
			return nil, err
		}
		d.DismissedAt = at(dAt)
		d.Until = atp(until)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ApplyProblemDismissals adds, replaces and removes an account's dismissals as
// one change. A set for a key already held replaces it, so snoozing a problem
// again with a different length is the same call as the first time. The batch
// is atomic because "dismiss all" is one gesture: half of it applying would
// leave the drawer showing a state nobody chose.
//
// limit bounds what the account holds afterwards, and is checked on the rows
// the transaction actually leaves rather than on arithmetic over the request: a
// removal of a key never held frees nothing, and two requests that each looked
// small beside the count they read would otherwise both fit. The single writer
// is what makes the count inside the transaction the count that is kept.
func (s *Store) ApplyProblemDismissals(ctx context.Context, userID string, set []ProblemDismissal, remove []string, limit int) error {
	return wrapWrite(s.tx(ctx, func(t *sql.Tx) error {
		for _, key := range remove {
			if _, err := t.ExecContext(ctx,
				`DELETE FROM problem_dismissals WHERE user_id = ? AND key = ?`, userID, key); err != nil {
				return err
			}
		}
		for _, d := range set {
			if _, err := t.ExecContext(ctx,
				`INSERT INTO problem_dismissals (user_id, key, severity, dismissed_at, until)
				 VALUES (?, ?, ?, ?, ?) ON CONFLICT(user_id, key) DO UPDATE SET
				 severity=excluded.severity, dismissed_at=excluded.dismissed_at, until=excluded.until`,
				userID, d.Key, d.Severity, ms(d.DismissedAt), msp(d.Until)); err != nil {
				return err
			}
		}
		var held int
		if err := t.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM problem_dismissals WHERE user_id = ?`, userID).Scan(&held); err != nil {
			return err
		}
		if limit > 0 && held > limit {
			return ErrTooManyDismissals
		}
		return nil
	}))
}

// CountProblemDismissals is how many an account holds, for the API to bound
// what one account can keep.
func (s *Store) CountProblemDismissals(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM problem_dismissals WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}
