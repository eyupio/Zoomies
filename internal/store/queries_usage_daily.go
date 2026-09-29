package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const dayMS = int64(24 * time.Hour / time.Millisecond)

// utcDay is the UTC midnight at or before an instant in milliseconds.
func utcDay(at int64) int64 { return at - ((at%dayMS)+dayMS)%dayMS }

// UsageDay is one row of the daily roll-up: a UTC day's runner allocation for
// one pool on one host under one installation.
type UsageDay struct {
	Day              time.Time `json:"day"`
	PoolID           string    `json:"pool_id"`
	HostID           string    `json:"host_id"`
	InstallationID   string    `json:"installation_id"`
	AllocatedSeconds int64     `json:"allocated_seconds"`
	// CostMinor is in hundredths of whatever currency the pool's rate is in,
	// and nil when nothing that day had a rate.
	CostMinor *int64 `json:"cost_minor,omitempty"`
}

type usageDayKey struct {
	day                  int64
	pool, host, instance string
}

type usageDayAcc struct {
	ms     int64
	cost   float64 // rate * milliseconds, summed, rounded once at the end
	priced bool
}

// RollUpSessions is the roll-up's arithmetic, apart from the database: the
// days in [from, until) cut from the given sessions. It is exported so the
// claim that the roll-up is reproducible from the sessions alone is one a
// test, or an operator's script, can check.
//
// Allocation is summed in milliseconds and cost as rate times milliseconds,
// each rounded once per row, so a row is the nearest whole second and cent to
// the exact figure rather than the sum of per-session roundings.
func RollUpSessions(sessions []*RunnerSession, from, until time.Time) []UsageDay {
	lo, hi := utcDay(from.UnixMilli()), utcDay(until.UnixMilli())
	acc := map[usageDayKey]*usageDayAcc{}
	for _, x := range sessions {
		start, end := max64(x.StartedAt.UnixMilli(), lo), min64(x.FinishedAt.UnixMilli(), hi)
		for at := start; at < end; {
			day := utcDay(at)
			next := min64(end, day+dayMS)
			k := usageDayKey{day, x.PoolID, x.HostID, x.InstallationID}
			a := acc[k]
			if a == nil {
				a = &usageDayAcc{}
				acc[k] = a
			}
			a.ms += next - at
			if x.CostPerRunnerHour != nil {
				a.priced = true
				a.cost += float64(next-at) * *x.CostPerRunnerHour
			}
			at = next
		}
	}
	out := make([]UsageDay, 0, len(acc))
	for k, a := range acc {
		d := UsageDay{Day: time.UnixMilli(k.day).UTC(), PoolID: k.pool, HostID: k.host, InstallationID: k.instance,
			AllocatedSeconds: a.ms / 1000}
		if a.priced {
			c := int64(math.Round(a.cost / float64(time.Hour/time.Millisecond) * 100))
			d.CostMinor = &c
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Day.Equal(b.Day) {
			return a.Day.Before(b.Day)
		}
		if a.PoolID != b.PoolID {
			return a.PoolID < b.PoolID
		}
		if a.HostID != b.HostID {
			return a.HostID < b.HostID
		}
		return a.InstallationID < b.InstallationID
	})
	return out
}

// RollUpUsage absorbs every whole UTC day before until that can no longer
// change into usage_daily, and returns how many days it rolled up.
//
// A day can no longer change once every runner alive in it has a session. A
// runner row without one -- still running, or removed and waiting for its
// cleanup to be confirmed -- holds the watermark at the start of the day it
// was created, because its session will arrive later and must not find its
// days already closed. The prune writes a session for any runner it deletes,
// so the hold lasts no longer than retention.runners.
//
// The rows, the sessions and the watermark are read and written in one
// transaction, so a controller that stops half way has rolled up nothing, and
// one that runs twice rolls up a day once.
func (s *Store) RollUpUsage(ctx context.Context, until time.Time) (int, error) {
	days := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		target := utcDay(until.UnixMilli())
		var held sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MIN(created_at) FROM runners r
			WHERE NOT EXISTS (SELECT 1 FROM runner_sessions rs WHERE rs.runner_id = r.id)`).Scan(&held); err != nil {
			return err
		}
		if held.Valid {
			target = min64(target, utcDay(held.Int64))
		}
		var from, rolled int64
		err := tx.QueryRowContext(ctx, `SELECT rolled_from, rolled_until FROM usage_rollup WHERE id = 1`).Scan(&from, &rolled)
		if errors.Is(err, sql.ErrNoRows) {
			var first sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT MIN(started_at) FROM runner_sessions`).Scan(&first); err != nil {
				return err
			}
			if !first.Valid {
				return nil
			}
			from, rolled = utcDay(first.Int64), utcDay(first.Int64)
		} else if err != nil {
			return err
		}
		if target <= rolled {
			return nil
		}
		sessions, err := querySessions(ctx, tx, rolled, target)
		if err != nil {
			return err
		}
		for _, d := range RollUpSessions(sessions, time.UnixMilli(rolled), time.UnixMilli(target)) {
			var cost any
			if d.CostMinor != nil {
				cost = *d.CostMinor
			}
			// An imported archive carries days the source had already rolled up
			// but not the watermark, so this pass meets them again. Both figures
			// come from the same sessions, so they agree unless the source had
			// pruned some, and then the imported figure is the fuller one: keep
			// the larger rather than abort every roll-up on the primary key. A
			// NULL cost means "not priced", which is not the same as free, so it
			// only yields to a real figure.
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_daily (day, pool_id, host_id, installation_id, allocated_seconds, cost_minor)
				VALUES (?,?,?,?,?,?)
				ON CONFLICT(day, pool_id, host_id, installation_id) DO UPDATE SET
					allocated_seconds = MAX(usage_daily.allocated_seconds, excluded.allocated_seconds),
					cost_minor = CASE WHEN usage_daily.cost_minor IS NULL THEN excluded.cost_minor
						WHEN excluded.cost_minor IS NULL THEN usage_daily.cost_minor
						ELSE MAX(usage_daily.cost_minor, excluded.cost_minor) END`,
				d.Day.UnixMilli(), d.PoolID, d.HostID, d.InstallationID, d.AllocatedSeconds, cost); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_rollup (id, rolled_from, rolled_until) VALUES (1, ?, ?)
			ON CONFLICT(id) DO UPDATE SET rolled_until = excluded.rolled_until`, from, target); err != nil {
			return err
		}
		days = int((target - rolled) / dayMS)
		return nil
	})
	return days, err
}

// UsageRollupRange is the span the roll-up has absorbed, [from, until). ok is
// false before anything has been rolled up.
func (s *Store) UsageRollupRange(ctx context.Context) (from, until time.Time, ok bool, err error) {
	var f, u int64
	err = s.read.QueryRowContext(ctx, `SELECT rolled_from, rolled_until FROM usage_rollup WHERE id = 1`).Scan(&f, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	return time.UnixMilli(f).UTC(), time.UnixMilli(u).UTC(), true, nil
}

// UsageDays returns the roll-up's rows for the days in [from, to).
func (s *Store) UsageDays(ctx context.Context, from, to time.Time) ([]UsageDay, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT day, pool_id, host_id, installation_id, allocated_seconds, cost_minor
		FROM usage_daily WHERE day >= ? AND day < ? ORDER BY day, pool_id, host_id, installation_id`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDay
	for rows.Next() {
		var d UsageDay
		var day int64
		var cost sql.NullInt64
		if err := rows.Scan(&day, &d.PoolID, &d.HostID, &d.InstallationID, &d.AllocatedSeconds, &cost); err != nil {
			return nil, err
		}
		d.Day = time.UnixMilli(day).UTC()
		if cost.Valid {
			c := cost.Int64
			d.CostMinor = &c
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// usageAllocation feeds the usage report's runner allocation, clipped to
// [lo, observed) and cut at bucket and UTC-day boundaries, to add.
//
// A day comes from the roll-up when the roll-up has absorbed it and it sits
// whole inside the window and inside one bucket; that is what keeps a report
// complete for days whose runner rows -- and even whose sessions -- have been
// pruned. Every other moment comes from the rows: runners still in the table,
// and the sessions of runners that are not. A day the roll-up cannot serve
// whole (an hourly bucket, a window that starts at 06:30) falls back to the
// sessions, which are kept for a year, rather than to a share of the day's
// total that would be a guess.
//
// A runner with a session is priced at the session's rate, the rate when it
// ran, so a report across a price change agrees with the roll-up about which
// rate applied.
func (s *Store) usageAllocation(ctx context.Context, group UsageGroup, lo, hi, observed, width int64,
	add func(key string, at int64, secs float64, cost *float64)) error {
	var until int64
	err := s.read.QueryRowContext(ctx, `SELECT rolled_until FROM usage_rollup WHERE id = 1`).Scan(&until)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rolled := func(day int64) bool {
		end := day + dayMS
		return day >= lo && end <= hi && end <= until && end <= observed && (day-lo)/width == (end-1-lo)/width
	}
	var col, rowExpr string
	switch group {
	case UsageByPool:
		col, rowExpr = "pool_id", "r.pool_id"
	case UsageByHost:
		col, rowExpr = "host_id", "r.host_id"
	case UsageByInstallation:
		col, rowExpr = "installation_id", "CASE WHEN rs.runner_id IS NULL THEN COALESCE(p.installation_id, '') ELSE rs.installation_id END"
	default:
		return fmt.Errorf("usage group %q has no runner allocation", group)
	}

	if until > lo {
		// col is one of three constants above, never caller input.
		rows, err := s.read.QueryContext(ctx, `SELECT day, `+col+`, SUM(allocated_seconds), SUM(cost_minor)
			FROM usage_daily WHERE day >= ? AND day < ? GROUP BY day, `+col, lo, min64(hi, until))
		if err != nil {
			return err
		}
		for rows.Next() {
			var day, secs int64
			var key string
			var minor sql.NullInt64
			if err := rows.Scan(&day, &key, &secs, &minor); err != nil {
				rows.Close()
				return err
			}
			if !rolled(day) {
				continue
			}
			var cost *float64
			if minor.Valid {
				c := float64(minor.Int64) / 100
				cost = &c
			}
			add(key, day, float64(secs), cost)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}

	rows, err := s.read.QueryContext(ctx, `SELECT `+rowExpr+`, r.created_at,
			CASE WHEN rs.runner_id IS NULL THEN COALESCE(r.finished_at, ?1) ELSE rs.finished_at END AS ended,
			CASE WHEN rs.runner_id IS NULL THEN p.cost_per_runner_hour ELSE rs.cost_per_runner_hour END
		FROM runners r JOIN pools p ON p.id = r.pool_id LEFT JOIN runner_sessions rs ON rs.runner_id = r.id
		WHERE r.created_at < ?2 AND ended > ?3
		UNION ALL
		SELECT rs.`+col+`, rs.started_at, rs.finished_at, rs.cost_per_runner_hour FROM runner_sessions rs
		WHERE rs.started_at < ?2 AND rs.finished_at > ?3
		AND NOT EXISTS (SELECT 1 FROM runners r WHERE r.id = rs.runner_id)`, observed, hi, lo)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var start, end int64
		var rate *float64
		if err := rows.Scan(&key, &start, &end, &rate); err != nil {
			return err
		}
		for at, end := max64(start, lo), min64(end, observed); at < end; {
			day := utcDay(at)
			next := min64(end, min64(lo+((at-lo)/width+1)*width, day+dayMS))
			if !rolled(day) {
				secs := float64(next-at) / 1000
				var cost *float64
				if rate != nil {
					c := secs / 3600 * *rate
					cost = &c
				}
				add(key, at, secs, cost)
			}
			at = next
		}
	}
	return rows.Err()
}

// UsageLedgerFrom is the earliest instant the usage ledger can still account
// for runner allocation: the start of the roll-up, or of the oldest session
// not yet rolled up. It is nil on a database with neither, which is one that
// has not yet seen a runner go.
func (s *Store) UsageLedgerFrom(ctx context.Context) (*time.Time, error) {
	var from sql.NullInt64
	err := s.read.QueryRowContext(ctx, `SELECT MIN(at) FROM (
		SELECT rolled_from AS at FROM usage_rollup WHERE id = 1
		UNION ALL SELECT MIN(started_at) FROM runner_sessions)`).Scan(&from)
	if err != nil || !from.Valid {
		return nil, err
	}
	t := time.UnixMilli(from.Int64).UTC()
	return &t, nil
}
