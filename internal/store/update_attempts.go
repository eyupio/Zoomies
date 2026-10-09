package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// What an attempt was for: the controller's own binary, or one host's.
const (
	UpdateScopeController = "controller"
	UpdateScopeHost       = "host"
)

// Who asked: a person pressing the button, or the planner acting on the
// update mode.
const (
	UpdateTriggerManual = "manual"
	UpdateTriggerAuto   = "auto"
)

// An attempt is requested until it ends, and ends once, in one of four ways.
const (
	UpdateRequested = "requested"
	UpdateSucceeded = "succeeded"
	UpdateFailed    = "failed"
	UpdateTimedOut  = "timed_out"
	UpdateCancelled = "cancelled"
)

// hostGoneReason is what an attempt says when the host it was for was deleted
// before the host reported. It is written for the person reading the history.
const hostGoneReason = "The host was removed before the update finished, so nothing will report on it. Enrol the host again to update it."

// UpdateAttempt is one request to change one target's version. HostID is empty
// for the controller's own attempts. It is not a foreign key: a host that
// joins again under the same id keeps its history (see migration 0086).
type UpdateAttempt struct {
	ID          string
	Scope       string
	HostID      string
	FromVersion string
	ToVersion   string
	Trigger     string
	RequestedBy string
	State       string
	Error       string
	RequestedAt time.Time
	FinishedAt  *time.Time
}

const updateAttemptCols = `id, scope, host_id, from_version, to_version, trigger, requested_by, state, error, requested_at, finished_at`

func scanUpdateAttempt(sc interface{ Scan(...any) error }) (UpdateAttempt, error) {
	var a UpdateAttempt
	var requested int64
	var finished sql.NullInt64
	err := sc.Scan(&a.ID, &a.Scope, &a.HostID, &a.FromVersion, &a.ToVersion, &a.Trigger,
		&a.RequestedBy, &a.State, &a.Error, &requested, &finished)
	a.RequestedAt, a.FinishedAt = at(requested), atp(finished)
	return a, err
}

// CreateUpdateAttempt records a request and sets its ID, state and RequestedAt.
// A target with an open attempt answers ErrConflict: the partial unique index
// is the rule, so two callers racing cannot both succeed.
func (s *Store) CreateUpdateAttempt(ctx context.Context, a *UpdateAttempt) error {
	switch {
	case a.Scope != UpdateScopeController && a.Scope != UpdateScopeHost:
		return fmt.Errorf("update attempt scope %q is neither %q nor %q", a.Scope, UpdateScopeController, UpdateScopeHost)
	case a.Scope == UpdateScopeHost && a.HostID == "":
		return fmt.Errorf("an update attempt for a host must name the host")
	case a.Scope == UpdateScopeController && a.HostID != "":
		return fmt.Errorf("an update attempt for the controller cannot name a host (%s)", a.HostID)
	case a.Trigger != UpdateTriggerManual && a.Trigger != UpdateTriggerAuto:
		return fmt.Errorf("update attempt trigger %q is neither %q nor %q", a.Trigger, UpdateTriggerManual, UpdateTriggerAuto)
	case strings.TrimSpace(a.ToVersion) == "":
		// Every ending is judged against the release the attempt asked for, so a
		// row without one could never be seen to have arrived.
		return fmt.Errorf("an update attempt must name the release it is for")
	}
	a.ID = NewID(PrefixUpdateAttempt)
	a.State, a.Error, a.FinishedAt = UpdateRequested, "", nil
	a.RequestedAt = s.Now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO update_attempts (`+updateAttemptCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			a.ID, a.Scope, a.HostID, a.FromVersion, a.ToVersion, a.Trigger, a.RequestedBy,
			a.State, a.Error, ms(a.RequestedAt))
		return wrapWrite(err)
	})
}

// FinishUpdateAttempt ends an open attempt in state, one of the four end
// states, and reports whether it did. It is false when the attempt was not
// open (already finished, or unknown): the first ending stands, so a late
// result or a timeout racing the helper's answer cannot rewrite it.
func (s *Store) FinishUpdateAttempt(ctx context.Context, id, state, errText string) (bool, error) {
	switch state {
	case UpdateSucceeded, UpdateFailed, UpdateTimedOut, UpdateCancelled:
	default:
		return false, fmt.Errorf("%q does not end an update attempt", state)
	}
	res, err := s.exec(ctx, `UPDATE update_attempts SET state = ?, error = ?, finished_at = ?
		WHERE id = ? AND state = ?`, state, errText, ms(s.Now()), id, UpdateRequested)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// OpenUpdateAttempts returns every attempt still waiting on its target,
// oldest first.
func (s *Store) OpenUpdateAttempts(ctx context.Context) ([]UpdateAttempt, error) {
	return s.queryUpdateAttempts(ctx, `SELECT `+updateAttemptCols+` FROM update_attempts
		WHERE state = ? ORDER BY requested_at, id`, UpdateRequested)
}

// ListUpdateAttempts returns attempts newest first. An empty scope matches
// every scope and an empty hostID every target; limit is capped at 500, and
// 50 stands for a limit of 0 or less.
func (s *Store) ListUpdateAttempts(ctx context.Context, scope, hostID string, limit int) ([]UpdateAttempt, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	return s.queryUpdateAttempts(ctx, `SELECT `+updateAttemptCols+` FROM update_attempts
		WHERE (?1 = '' OR scope = ?1) AND (?2 = '' OR host_id = ?2)
		ORDER BY requested_at DESC, id DESC LIMIT ?3`, scope, hostID, limit)
}

func (s *Store) queryUpdateAttempts(ctx context.Context, query string, args ...any) ([]UpdateAttempt, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpdateAttempt
	for rows.Next() {
		a, err := scanUpdateAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneUpdateAttempts deletes attempts that finished before the cutoff and
// returns how many went. An open attempt is never pruned however old: it is
// the only record that something is in flight, and the controller's timeout
// closes it, after which it ages like any other.
func (s *Store) PruneUpdateAttempts(ctx context.Context, before time.Time) (int, error) {
	n, err := pruneInBatches(ctx, pruneRowBatch, pruneMaxBatches, func(ctx context.Context) (int64, error) {
		res, err := s.exec(ctx, `DELETE FROM update_attempts WHERE id IN
			(SELECT id FROM update_attempts WHERE state <> ? AND finished_at < ? LIMIT ?)`,
			UpdateRequested, ms(before), pruneRowBatch)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
	return int(n), err
}

// cancelUpdateAttemptsForHost closes a host's open attempts inside the
// transaction that deletes the host, so no moment exists in which the host is
// gone and its attempt still reads as in flight.
func cancelUpdateAttemptsForHost(ctx context.Context, tx *sql.Tx, now time.Time, hostID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE update_attempts SET state = ?, error = ?, finished_at = ?
		WHERE scope = ? AND host_id = ? AND state = ?`,
		UpdateCancelled, hostGoneReason, ms(now), UpdateScopeHost, hostID, UpdateRequested)
	return err
}
