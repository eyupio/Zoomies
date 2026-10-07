package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/hosttune"
)

// MaxHostCheckAcceptances bounds what one host holds. The catalogue is far
// smaller, so the cap only ever stops a script; it is counted in the same
// transaction as the write so two requests cannot each fit.
const MaxHostCheckAcceptances = 64

// ErrTooManyHostAcceptances is what UpsertHostCheckAcceptance returns when a
// new acceptance would take a host past MaxHostCheckAcceptances.
var ErrTooManyHostAcceptances = errors.New("too many accepted checks on this host")

// HostCheckAcceptance is one operator's decision that a host's warning is
// deliberate. Current is the check's text when it was made: the acceptance
// covers the check only while it still reads that.
type HostCheckAcceptance struct {
	ID        string
	HostID    string
	CheckID   string
	Current   string
	Reason    string
	By        string
	ByName    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

const hostAcceptanceCols = `id, host_id, check_id, accepted_current, reason, created_by, created_by_name, created_at, expires_at`

func scanHostAcceptance(sc interface{ Scan(...any) error }) (HostCheckAcceptance, error) {
	var a HostCheckAcceptance
	var created, expires int64
	if err := sc.Scan(&a.ID, &a.HostID, &a.CheckID, &a.Current, &a.Reason, &a.By, &a.ByName, &created, &expires); err != nil {
		return a, err
	}
	a.CreatedAt, a.ExpiresAt = at(created), at(expires)
	return a, nil
}

// UpsertHostCheckAcceptance records an acceptance. Accepting a check that
// already has one renews it: the value, reason, owner and end are replaced and
// the first row's ID is kept. a.ID and a.CreatedAt are filled in. An end that
// is not in the future is refused, because it would be an acceptance that never
// held.
func (s *Store) UpsertHostCheckAcceptance(ctx context.Context, a *HostCheckAcceptance) error {
	now := s.Now()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if !a.ExpiresAt.After(now) {
		return errors.New("an acceptance must end in the future")
	}
	err := s.tx(ctx, func(t *sql.Tx) error {
		var held int
		if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_check_acceptances WHERE host_id = ? AND check_id <> ?`,
			a.HostID, a.CheckID).Scan(&held); err != nil {
			return err
		}
		if held+1 > MaxHostCheckAcceptances {
			return ErrTooManyHostAcceptances
		}
		_, err := t.ExecContext(ctx, `INSERT INTO host_check_acceptances (`+hostAcceptanceCols+`) VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(host_id, check_id) DO UPDATE SET
				accepted_current = excluded.accepted_current, reason = excluded.reason,
				created_by = excluded.created_by, created_by_name = excluded.created_by_name,
				created_at = excluded.created_at, expires_at = excluded.expires_at`,
			NewID(PrefixHostAcceptance), a.HostID, a.CheckID, a.Current, a.Reason, a.By, a.ByName, ms(a.CreatedAt), ms(a.ExpiresAt))
		return err
	})
	if err != nil {
		return wrapWrite(err)
	}
	kept, err := scanHostAcceptance(s.read.QueryRowContext(ctx, `SELECT `+hostAcceptanceCols+
		` FROM host_check_acceptances WHERE host_id = ? AND check_id = ?`, a.HostID, a.CheckID))
	if err != nil {
		return err
	}
	*a = kept
	return nil
}

// DeleteHostCheckAcceptance removes one host's acceptance of a check, or
// returns ErrNotFound when there was none.
func (s *Store) DeleteHostCheckAcceptance(ctx context.Context, hostID, checkID string) error {
	res, err := s.exec(ctx, `DELETE FROM host_check_acceptances WHERE host_id = ? AND check_id = ?`, hostID, checkID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("acceptance of %q on host %q: %w", checkID, hostID, ErrNotFound)
	}
	return nil
}

// fillHostAcceptances loads every given host's acceptances with one query. An
// expired row is still returned: judging reads the clock, and the row is what
// lets the report say "it ended on" instead of silently counting again.
func (s *Store) fillHostAcceptances(ctx context.Context, hosts []*Host) error {
	byID := make(map[string]*Host, len(hosts))
	args := make([]any, 0, len(hosts))
	for _, h := range hosts {
		h.Acceptances = nil
		if _, dup := byID[h.ID]; dup {
			continue
		}
		byID[h.ID] = h
		args = append(args, h.ID)
	}
	if len(args) == 0 {
		return nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT `+hostAcceptanceCols+` FROM host_check_acceptances WHERE host_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`) ORDER BY host_id, check_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanHostAcceptance(rows)
		if err != nil {
			return err
		}
		byID[a.HostID].Acceptances = append(byID[a.HostID].Acceptances, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Hosts sharing an ID (the same pointer twice) share the slice.
	for _, h := range hosts {
		h.Acceptances = byID[h.ID].Acceptances
	}
	return nil
}

// JudgedDoctor is the host's report with the operator's acceptances applied, on
// a copy. It is the one place that decision is made: problems, metrics, the
// views and the summary all read it, so none can disagree about what counts.
// Doctor stays the raw stored report. A host with no report, or a container
// agent's partial one, has nothing to judge.
func (h *Host) JudgedDoctor(now time.Time) *hosttune.Report {
	r := h.Doctor.Report
	if r == nil || r.Container {
		return r
	}
	held := make([]hosttune.Held, len(h.Acceptances))
	for i, a := range h.Acceptances {
		held[i] = hosttune.Held{CheckID: a.CheckID, Current: a.Current, Reason: a.Reason, By: a.ByName, At: a.CreatedAt, ExpiresAt: a.ExpiresAt}
	}
	j := r.Judged(held, now)
	return &j
}
