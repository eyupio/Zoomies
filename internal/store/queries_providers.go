package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The providers table is written by two different authors, and they are kept
// apart on purpose: an operator edits the configuration through UpdateProvider,
// and every pass of the reconciler writes only what it observed. A single fat
// UPDATE would let a pass that read the row a minute ago put an operator's
// ceiling back to what it was, with nothing reported and nothing to see. That
// is the UpdateHost / SetHostReported / PatchHost discipline, applied here.
//
// updated_at follows the same split: it says when the provider's configuration
// last changed, so the observation writers below deliberately leave it alone.
// A health check every minute that moved it would make "changed just now"
// meaningless on the page an operator reads it from.

const providerCols = `id, kind, name, endpoint, ca_pem, insecure_skip_verify, settings,
	credentials_enc, machine_labels, machine_capacity, machine_backend, machine_platform,
	machine_cpus, machine_memory_mb, machine_disk_mb, pool_selector, max_machines,
	max_creates_in_flight, idle_timeout_ms, cost_per_machine_hour, enabled, paused,
	paused_reason, paused_until, consecutive_failures, last_check_at, last_check_error,
	last_sweep_at, created_at, updated_at, tailcat_address_enc, last_check_report`

func scanProvider(sc interface{ Scan(...any) error }) (*Provider, error) {
	var p Provider
	var insecure, enabled, paused int
	var idle, created, updated int64
	var platform string
	var pausedUntil, checked, swept sql.NullInt64
	err := sc.Scan(&p.ID, &p.Kind, &p.Name, &p.Endpoint, &p.CAPEM, &insecure, &p.Settings,
		&p.CredentialsEnc, &p.MachineLabels, &p.MachineCapacity, &p.MachineBackend, &platform,
		&p.MachineCPUs, &p.MachineMemoryMB, &p.MachineDiskMB, &p.PoolSelector, &p.MaxMachines,
		&p.MaxCreatesInFlight, &idle, &p.CostPerMachineHour, &enabled, &paused,
		&p.PausedReason, &pausedUntil, &p.ConsecutiveFailures, &checked, &p.LastCheckError,
		&swept, &created, &updated, &p.TailcatAddressEnc, &p.LastCheckReport)
	if err != nil {
		return nil, err
	}
	p.InsecureSkipVerify, p.Enabled, p.Paused = insecure == 1, enabled == 1, paused == 1
	p.IdleTimeout = Duration(time.Duration(idle) * time.Millisecond)
	p.CreatedAt, p.UpdatedAt = at(created), at(updated)
	p.PausedUntil, p.LastCheckAt, p.LastSweepAt = atp(pausedUntil), atp(checked), atp(swept)
	if err := unmarshalJSON(platform, &p.MachinePlatform); err != nil {
		return nil, fmt.Errorf("provider %s: decoding the machine platform: %w", p.ID, err)
	}
	return &p, nil
}

// CreateProvider inserts a provider. The credential is not part of it:
// SetProviderCredentials writes the sealed bytes, so the one place that has to
// think about the instance key is the one place that holds it.
func (s *Store) CreateProvider(ctx context.Context, p *Provider) error {
	if p.ID == "" {
		p.ID = NewID(PrefixProvider)
	}
	now := s.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	p.MachinePlatform = p.MachinePlatform.Normalized()
	if p.MachineBackend == "" {
		p.MachineBackend = BackendDocker
	}
	platform, err := marshalJSON(p.MachinePlatform)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO providers (`+providerCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, string(p.Kind), p.Name, p.Endpoint, p.CAPEM, boolInt(p.InsecureSkipVerify),
		p.Settings, p.CredentialsEnc, p.MachineLabels, p.MachineCapacity,
		string(p.MachineBackend), platform, p.MachineCPUs, p.MachineMemoryMB, p.MachineDiskMB,
		p.PoolSelector, p.MaxMachines, p.MaxCreatesInFlight,
		p.IdleTimeout.Duration().Milliseconds(), p.CostPerMachineHour,
		boolInt(p.Enabled), boolInt(p.Paused), p.PausedReason, msp(p.PausedUntil),
		p.ConsecutiveFailures, msp(p.LastCheckAt), p.LastCheckError, msp(p.LastSweepAt),
		ms(p.CreatedAt), ms(p.UpdatedAt), p.TailcatAddressEnc, p.LastCheckReport)
	return wrapWrite(err)
}

// GetProvider returns one provider by ID.
func (s *Store) GetProvider(ctx context.Context, id string) (*Provider, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+providerCols+` FROM providers WHERE id = ?`, id)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("provider %s: %w", id, ErrNotFound)
	}
	return p, err
}

// ListProviders returns every provider, by name. It is deliberately
// unpaginated: a fleet has tens of these, not thousands, and every pass of the
// reconciler reads the lot.
func (s *Store) ListProviders(ctx context.Context) ([]*Provider, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+providerCols+` FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProvider persists the operator's half of a provider row.
//
// The credential, the pause, the breaker and every observation are absent on
// purpose. They have their own writers below, so a form submitted from a page
// that was opened before the breaker tripped cannot un-trip it.
func (s *Store) UpdateProvider(ctx context.Context, p *Provider) error {
	p.UpdatedAt = s.Now()
	p.MachinePlatform = p.MachinePlatform.Normalized()
	if p.MachineBackend == "" {
		p.MachineBackend = BackendDocker
	}
	platform, err := marshalJSON(p.MachinePlatform)
	if err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE providers SET name=?, endpoint=?, ca_pem=?,
		insecure_skip_verify=?, settings=?, machine_labels=?, machine_capacity=?,
		machine_backend=?, machine_platform=?, machine_cpus=?, machine_memory_mb=?,
		machine_disk_mb=?, pool_selector=?, max_machines=?, max_creates_in_flight=?,
		idle_timeout_ms=?, cost_per_machine_hour=?, enabled=?, updated_at=? WHERE id=?`,
		p.Name, p.Endpoint, p.CAPEM, boolInt(p.InsecureSkipVerify), p.Settings,
		p.MachineLabels, p.MachineCapacity, string(p.MachineBackend), platform,
		p.MachineCPUs, p.MachineMemoryMB, p.MachineDiskMB, p.PoolSelector, p.MaxMachines,
		p.MaxCreatesInFlight, p.IdleTimeout.Duration().Milliseconds(), p.CostPerMachineHour,
		boolInt(p.Enabled), ms(p.UpdatedAt), p.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "provider", p.ID)
}

// SetProviderCredentials replaces the sealed credential.
//
// It is its own writer because rotating a credential is the one edit that must
// not carry the rest of a form with it: an operator pasting a new API token is
// not also re-submitting the ceilings from the page they opened yesterday.
func (s *Store) SetProviderCredentials(ctx context.Context, id string, enc []byte) error {
	res, err := s.exec(ctx, `UPDATE providers SET credentials_enc=?, updated_at=? WHERE id=?`,
		enc, ms(s.Now()), id)
	if err != nil {
		return err
	}
	return affected(res, "provider", id)
}

// SetProviderTailcatAddress replaces the sealed private connection address, or
// clears it when enc is empty, which puts the provider back on a direct
// connection.
//
// Its own writer for the credential's reason: switching a provider between a
// direct and a private connection is one deliberate act, and an edit to the
// ceilings from a stale page must not undo it. It moves updated_at because
// the controller's cache of built clients is keyed on it, and a client that
// kept dialling the old way after the connection changed would be exactly the
// kind of silent failure this column exists to avoid.
func (s *Store) SetProviderTailcatAddress(ctx context.Context, id string, enc []byte) error {
	if len(enc) == 0 {
		enc = nil
	}
	res, err := s.exec(ctx, `UPDATE providers SET tailcat_address_enc=?, updated_at=? WHERE id=?`,
		enc, ms(s.Now()), id)
	if err != nil {
		return err
	}
	return affected(res, "provider", id)
}

// SetProviderChecked records the outcome of a credential probe. An empty
// checkErr is what a healthy probe writes, which is what clears the last one.
//
// report is the whole result as the caller serialised it. The store keeps it
// without reading it, so a new field on a finding never needs a migration.
func (s *Store) SetProviderChecked(ctx context.Context, id string, at time.Time, checkErr, report string) error {
	_, err := s.exec(ctx, `UPDATE providers SET last_check_at=?, last_check_error=?, last_check_report=? WHERE id=?`,
		ms(at), checkErr, report, id)
	return err
}

// SetProviderPaused is the kill switch: a provider paused buys nothing, and
// keeps every machine it already has.
//
// The reason is stored with it because "paused" on its own is the thing an
// operator finds months later with no idea whether it is safe to undo.
//
// It leaves updated_at alone: pausing changes nothing a connection check would
// verify, and the problems list reads a later updated_at as "settings changed
// since the last check". Moving it would also rebuild a provider client whose
// credentials and address had not changed.
func (s *Store) SetProviderPaused(ctx context.Context, id string, paused bool, reason string) error {
	if !paused {
		reason = ""
	}
	res, err := s.exec(ctx, `UPDATE providers SET paused=?, paused_reason=? WHERE id=?`,
		boolInt(paused), reason, id)
	if err != nil {
		return err
	}
	return affected(res, "provider", id)
}

// SetProviderBreaker records the automatic half of the pause: how many calls in
// a row have failed, and how long to leave the provider alone for.
//
// It never touches `paused`, which is a person's decision. A breaker that
// expires must not release a switch somebody pressed on purpose.
func (s *Store) SetProviderBreaker(ctx context.Context, id string, failures int, until *time.Time) error {
	_, err := s.exec(ctx, `UPDATE providers SET consecutive_failures=?, paused_until=? WHERE id=?`,
		failures, msp(until), id)
	return err
}

// SetProviderSwept records when this provider's resources were last listed and
// compared against the rows. It is the clock the ownership sweep paces itself
// by, so it is written even when the sweep found nothing.
func (s *Store) SetProviderSwept(ctx context.Context, id string, at time.Time) error {
	_, err := s.exec(ctx, `UPDATE providers SET last_sweep_at=? WHERE id=?`, ms(at), id)
	return err
}

// DeleteProvider removes a provider, refusing while any of its machines still
// believes it has a resource.
//
// The refusal is the point. The foreign key is RESTRICT rather than CASCADE
// precisely so that deleting the row cannot be the thing that loses track of a
// running VM: the rows are the only record of what was rented, and without them
// nothing knows what to go and clean up. The machines whose resources were
// confirmed gone are history, and go with the provider they belonged to.
func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines
			WHERE provider_id=? AND resource_id <> '' AND deleted_at IS NULL`, id).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			return fmt.Errorf("%w: provider %s still has %d machine(s) with a resource behind them; "+
				"delete those machines first, or release each one if the resource is already gone",
				ErrConflict, id, live)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM machines WHERE provider_id=?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return affected(res, "provider", id)
	})
}
