package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A rollout is running until it ends, halted while it waits for a person, and
// done or cancelled for good. Running and halted are the open states.
const (
	RolloutRunning   = "running"
	RolloutHalted    = "halted"
	RolloutDone      = "done"
	RolloutCancelled = "cancelled"
)

// UpdateRollout is one walk of the fleet to one release. Target is the tag it
// was started for and does not change. FinishedAt is nil while it is open.
type UpdateRollout struct {
	ID           string
	Target       string
	Trigger      string
	State        string
	StartedBy    string
	HaltedReason string
	StartedAt    time.Time
	FinishedAt   *time.Time
}

const updateRolloutCols = `id, target, trigger, state, started_by, halted_reason, started_at, finished_at`

func scanUpdateRollout(sc interface{ Scan(...any) error }) (UpdateRollout, error) {
	var r UpdateRollout
	var started int64
	var finished sql.NullInt64
	err := sc.Scan(&r.ID, &r.Target, &r.Trigger, &r.State, &r.StartedBy, &r.HaltedReason, &started, &finished)
	r.StartedAt, r.FinishedAt = at(started), atp(finished)
	return r, err
}

// CreateUpdateRollout starts a rollout and sets its ID, state and StartedAt.
// While another is running or halted it answers ErrConflict: the partial
// unique index is the rule, so two callers racing cannot both succeed.
func (s *Store) CreateUpdateRollout(ctx context.Context, r *UpdateRollout) error {
	switch {
	case strings.TrimSpace(r.Target) == "":
		return fmt.Errorf("an update rollout must name the release it is for")
	case r.Trigger != UpdateTriggerManual && r.Trigger != UpdateTriggerAuto:
		return fmt.Errorf("update rollout trigger %q is neither %q nor %q", r.Trigger, UpdateTriggerManual, UpdateTriggerAuto)
	}
	r.ID = NewID(PrefixUpdateRollout)
	r.State, r.HaltedReason, r.FinishedAt = RolloutRunning, "", nil
	r.StartedAt = s.Now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO update_rollouts (`+updateRolloutCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
			r.ID, r.Target, r.Trigger, r.State, r.StartedBy, r.HaltedReason, ms(r.StartedAt))
		return wrapWrite(err)
	})
}

// OpenUpdateRollout returns the rollout that is running or halted, or
// ErrNotFound when the fleet is not being rolled out.
func (s *Store) OpenUpdateRollout(ctx context.Context) (*UpdateRollout, error) {
	r, err := scanUpdateRollout(s.read.QueryRowContext(ctx, `SELECT `+updateRolloutCols+`
		FROM update_rollouts WHERE state IN (?, ?)`, RolloutRunning, RolloutHalted))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// HaltUpdateRollout stops a running rollout for a person to look at, with the
// sentence they will read, and reports whether it did. It is false when the
// rollout was not running (halted already, ended, or unknown): the first
// reason stands.
func (s *Store) HaltUpdateRollout(ctx context.Context, id, reason string) (bool, error) {
	return s.moveUpdateRollout(ctx, `UPDATE update_rollouts SET state = ?, halted_reason = ?
		WHERE id = ? AND state = ?`, RolloutHalted, reason, id, RolloutRunning)
}

// ResumeUpdateRollout lets a halted rollout carry on, forgetting why it had
// stopped, and reports whether it did. It is false when the rollout was not
// halted.
func (s *Store) ResumeUpdateRollout(ctx context.Context, id string) (bool, error) {
	return s.moveUpdateRollout(ctx, `UPDATE update_rollouts SET state = ?, halted_reason = ''
		WHERE id = ? AND state = ?`, RolloutRunning, id, RolloutHalted)
}

// FinishUpdateRollout ends an open rollout, running or halted, as done or
// cancelled, and reports whether it did. It is false when the rollout had
// already ended or is unknown: the first ending stands, so a late cancel
// cannot rewrite how a rollout went. A halted rollout keeps the reason it
// stopped for.
func (s *Store) FinishUpdateRollout(ctx context.Context, id, state string) (bool, error) {
	if state != RolloutDone && state != RolloutCancelled {
		return false, fmt.Errorf("%q does not end an update rollout", state)
	}
	return s.moveUpdateRollout(ctx, `UPDATE update_rollouts SET state = ?, finished_at = ?
		WHERE id = ? AND state IN (?, ?)`, state, ms(s.Now()), id, RolloutRunning, RolloutHalted)
}

// moveUpdateRollout runs one guarded UPDATE, whose WHERE names the state the
// move is allowed from, and reports whether a row moved.
func (s *Store) moveUpdateRollout(ctx context.Context, query string, args ...any) (bool, error) {
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// PruneUpdateRollouts deletes rollouts that ended before the cutoff and
// returns how many went. An open rollout is never pruned however old: it is
// the only record that the fleet is mid-update, and an operator or the
// planner ends it, after which it ages like any other.
func (s *Store) PruneUpdateRollouts(ctx context.Context, before time.Time) (int, error) {
	n, err := pruneInBatches(ctx, pruneRowBatch, pruneMaxBatches, func(ctx context.Context) (int64, error) {
		res, err := s.exec(ctx, `DELETE FROM update_rollouts WHERE id IN
			(SELECT id FROM update_rollouts WHERE state IN (?, ?) AND finished_at < ? LIMIT ?)`,
			RolloutDone, RolloutCancelled, ms(before), pruneRowBatch)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
	return int(n), err
}
