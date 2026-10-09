package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
)

// secretColumns is deliberately closed. A new encrypted column needs a transfer
// rule before a snapshot may be advertised as portable.
var secretColumns = map[string][]string{
	"installations":       {"private_key_enc", "webhook_secret_enc"},
	"providers":           {"credentials_enc", "tailcat_address_enc"},
	"backup_remotes":      {"secret_key_enc", "passphrase_enc"},
	"user_two_step":       {"secret_enc"},
	"provider_setups":     {"payload_enc"},
	"assistant_providers": {"key_enc"},
}

func quoteTransferIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// TransformSecrets re-seals a private snapshot in one transaction. The caller
// owns the keys; the store owns the complete inventory and the SQL.
func (s *Store) TransformSecrets(ctx context.Context, transform func([]byte) ([]byte, error)) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type='table' AND p.name LIKE '%\_enc' ESCAPE '\'`)
		if err != nil {
			return err
		}
		var columns [][2]string
		for rows.Next() {
			var table, col string
			if err := rows.Scan(&table, &col); err != nil {
				rows.Close()
				return err
			}
			known := false
			for _, name := range secretColumns[table] {
				if name == col {
					known = true
				}
			}
			if !known {
				rows.Close()
				return fmt.Errorf("transfer has no secret rule for %s.%s", table, col)
			}
			columns = append(columns, [2]string{table, col})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, pair := range columns {
			table, col := quoteTransferIdentifier(pair[0]), quoteTransferIdentifier(pair[1])
			if err := transformTransferColumn(ctx, tx, table, "rowid", col, false, "", transform); err != nil {
				return err
			}
		}
		if err := transformTransferColumn(ctx, tx, "instance_settings", "key", "value", true, "secret=1 AND", transform); err != nil {
			return err
		}
		return transformTransferColumn(ctx, tx, "settings", "key", "value", true, "key='tailcat.identity.v1' AND", transform)
	})
}

func transformTransferColumn(ctx context.Context, tx *sql.Tx, table, id, col string, encoded bool, predicate string, transform func([]byte) ([]byte, error)) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+id+", "+col+" FROM "+table+" WHERE "+predicate+" "+col+" IS NOT NULL AND LENGTH("+col+")>0")
	if err != nil {
		return err
	}
	type value struct {
		id     any
		sealed []byte
	}
	var values []value
	for rows.Next() {
		var v value
		if err := rows.Scan(&v.id, &v.sealed); err != nil {
			rows.Close()
			return err
		}
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range values {
		raw := v.sealed
		if encoded {
			raw, err = base64.StdEncoding.DecodeString(string(raw))
			if err != nil {
				return fmt.Errorf("transfer cannot decode %s.%s", table, col)
			}
		}
		sealed, err := transform(raw)
		if err != nil {
			return fmt.Errorf("transfer cannot open %s.%s; check the source encryption key", table, col)
		}
		var stored any = sealed
		if encoded {
			stored = base64.StdEncoding.EncodeToString(sealed)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET "+col+"=? WHERE "+id+"=?", stored, v.id); err != nil {
			return err
		}
	}
	return nil
}

// StripTransferOperatorSecrets removes process-owned credentials from the
// private export copy. Historical identities remain for audit attribution.
func (s *Store) StripTransferOperatorSecrets(ctx context.Context, clearSettings []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var unknown int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE secret=1 AND key<>'tailcat.identity.v1' AND LENGTH(value)>0`).Scan(&unknown); err != nil {
			return err
		}
		if unknown != 0 {
			return fmt.Errorf("transfer has no rule for a legacy secret setting")
		}
		highest := Roles()[len(Roles())-1]
		for _, query := range []string{
			`DELETE FROM user_two_step WHERE user_id IN (SELECT id FROM users WHERE role=?)`,
			`DELETE FROM user_recovery_codes WHERE user_id IN (SELECT id FROM users WHERE role=?)`,
			`UPDATE users SET password_hash='', disabled=1 WHERE role=?`,
		} {
			if _, err := tx.ExecContext(ctx, query, highest); err != nil {
				return err
			}
		}
		for _, query := range []string{
			`DELETE FROM provider_setups`,
			`DELETE FROM assistant_providers`,
			`DELETE FROM settings WHERE key='tailcat.identity.v1'`,
			`UPDATE backup_remotes SET enabled=0, secret_key_enc=NULL, passphrase_enc=NULL`,
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		for _, key := range clearSettings {
			if _, err := tx.ExecContext(ctx, `DELETE FROM instance_settings WHERE key=?`, key); err != nil {
				return err
			}
		}
		return nil
	})
}

// TransferInventory describes retained data without copying it into memory.
func (s *Store) TransferInventory(ctx context.Context) (map[string]int64, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, name := range names {
		var n int64
		if err := s.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteTransferIdentifier(name)).Scan(&n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, nil
}

// TransferDestinationEmpty permits authentication bootstrap, but never merging
// another fleet's records with the incoming database.
func (s *Store) TransferDestinationEmpty(ctx context.Context) error {
	var users int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role<>?`, Roles()[len(Roles())-1]).Scan(&users); err != nil {
		return err
	}
	if users > 0 {
		return fmt.Errorf("destination already holds team accounts; import into an empty instance")
	}

	for _, table := range []string{"installations", "pools", "hosts", "providers", "machines", "jobs", "runners", "runner_sessions", "usage_daily", "usage_capacity_samples", "installation_daily", "ai_context_repositories"} {
		var n int
		if err := s.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteTransferIdentifier(table)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("destination already holds %s; import into an empty instance", table)
		}
	}
	return nil
}

// TransferOperatorAccess captures only the destination's process operators.
// Team identities and history come from the source, never this bootstrap.
func (s *Store) TransferOperatorAccess(ctx context.Context) ([]ArchiveTable, error) {
	highest := Roles()[len(Roles())-1]
	var out []ArchiveTable
	for _, spec := range []struct{ table, where string }{
		{"users", "role=?"},
		{"api_tokens", "(role=? OR owner_role=?) AND (user_id IS NULL OR user_id='' OR user_id IN (SELECT id FROM users WHERE role=?))"},
		{"user_two_step", "user_id IN (SELECT id FROM users WHERE role=?)"},
		{"user_recovery_codes", "user_id IN (SELECT id FROM users WHERE role=?)"},
		{"assistant_providers", "1=1"},
	} {
		args := make([]any, strings.Count(spec.where, "?"))
		for i := range args {
			args[i] = highest
		}
		rows, err := s.read.QueryContext(ctx, "SELECT * FROM "+spec.table+" WHERE "+spec.where, args...)
		if err != nil {
			return nil, err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		at := ArchiveTable{Table: spec.table, Columns: cols}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			row := make([]ArchiveValue, len(cols))
			for i, v := range vals {
				row[i] = ArchiveValue{V: v}
			}
			at.Rows = append(at.Rows, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, nil
}

// AdaptTransferredInstance removes source operator authority without deleting
// historical identities, and installs the destination's own access instead.
func (s *Store) AdaptTransferredInstance(ctx context.Context, access []ArchiveTable, clearSettings []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		highest := Roles()[len(Roles())-1]
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked=1 WHERE role=? OR owner_role=? OR user_id IN (SELECT id FROM users WHERE role=?)`, highest, highest, highest); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled=1, role=? WHERE role=?`, RoleAdmin, highest); err != nil {
			return err
		}
		for _, query := range []string{
			`UPDATE users SET oidc_subject='' WHERE oidc_subject<>''`,
			`DELETE FROM sessions`, `DELETE FROM sign_in_challenges`,
			`DELETE FROM join_tokens WHERE used_at IS NULL`, `DELETE FROM oauth_requests`,
			`DELETE FROM oauth_tokens`,
			`DELETE FROM provider_setups`, `DELETE FROM controller_lease`,
			`DELETE FROM assistant_providers`,
			`DELETE FROM settings WHERE key IN ('tailcat.identity.v1','transfer.draining')`,
			`UPDATE backup_remotes SET enabled=0`,
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,?), revoked_reason='instance transferred'`, ms(s.Now())); err != nil {
			return err
		}
		for _, key := range clearSettings {
			if _, err := tx.ExecContext(ctx, `DELETE FROM instance_settings WHERE key=?`, key); err != nil {
				return err
			}
		}
		allowed := map[string]bool{"users": true, "api_tokens": true, "user_two_step": true, "user_recovery_codes": true, "assistant_providers": true}
		for _, at := range access {
			if !allowed[at.Table] {
				return fmt.Errorf("unsupported destination access table %s", at.Table)
			}
			cols := make([]string, len(at.Columns))
			for i, col := range at.Columns {
				cols[i] = quoteTransferIdentifier(col)
			}
			for _, row := range at.Rows {
				if len(row) != len(cols) {
					return fmt.Errorf("invalid destination access row")
				}
				vals := make([]any, len(row))
				for i, v := range row {
					vals[i] = v.V
				}
				if at.Table == "users" {
					for i, col := range at.Columns {
						if col != "username" {
							continue
						}
						// A former operator may already be a disabled historical
						// identity after an earlier move. Keep its ID and attribution
						// while freeing a common operator name for this destination.
						if _, err := tx.ExecContext(ctx, `UPDATE users SET display_name=CASE WHEN display_name='' THEN username ELSE display_name END, username='transferred-'||id WHERE username=? AND disabled=1 AND password_hash='' AND oidc_subject=''`, vals[i]); err != nil {
							return err
						}
					}
				}
				query := "INSERT INTO " + quoteTransferIdentifier(at.Table) + " (" + strings.Join(cols, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
				if _, err := tx.ExecContext(ctx, query, vals...); err != nil {
					return fmt.Errorf("destination operator conflicts with an incoming identity; choose a distinct operator identity: %w", err)
				}
			}
		}
		return nil
	})
}

// TransferReady refuses to freeze work whose external side effects are still
// in flight. The fence must stay raised until the old controller is retired.
func (s *Store) TransferReady(ctx context.Context) error {
	fence, err := s.RecoveryFenced(ctx)
	if err != nil {
		return err
	}
	if !fence.Fenced {
		return fmt.Errorf("drain the fleet and raise its recovery fence before exporting a complete instance")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM runners WHERE state NOT IN ('removed','failed') OR (state IN ('removed','failed') AND cleaned_up_at IS NULL)`,
		`SELECT COUNT(*) FROM jobs WHERE state='in_progress'`,
		`SELECT COUNT(*) FROM machines WHERE state IN ('planned','creating','starting','bootstrapping','enrolling','draining','deleting') OR op_id<>'' OR op_outcome_unknown=1`,
	} {
		var n int
		if err := s.read.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("work is still in flight; finish runner cleanup and machine operations before exporting")
		}
	}
	return nil
}

func (s *Store) TransferHasOperator(ctx context.Context) (bool, error) {
	highest := Roles()[len(Roles())-1]
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users WHERE role=? AND disabled=0 AND (password_hash<>'' OR oidc_subject<>'')) + (SELECT COUNT(*) FROM api_tokens WHERE role=? AND revoked=0 AND (expires_at IS NULL OR expires_at>?) AND (user_id IS NULL OR user_id='' OR user_id IN (SELECT id FROM users WHERE role=? AND disabled=0)))`, highest, highest, ms(s.now()), highest).Scan(&n)
	return n > 0, err
}

const SettingTransferDraining = "transfer.draining"

type TransferProgress struct {
	Draining          bool `json:"draining"`
	Ready             bool `json:"ready"`
	LiveRunners       int  `json:"live_runners"`
	BusyRunners       int  `json:"busy_runners"`
	PendingCleanup    int  `json:"pending_cleanup"`
	ActiveJobs        int  `json:"active_jobs"`
	MachineOperations int  `json:"machine_operations"`
}

func (p TransferProgress) Quiescent() bool {
	return p.LiveRunners == 0 && p.PendingCleanup == 0 && p.ActiveJobs == 0 && p.MachineOperations == 0
}

func (s *Store) TransferProgress(ctx context.Context) (TransferProgress, error) {
	var p TransferProgress
	raw, err := s.GetSetting(ctx, SettingTransferDraining)
	if err != nil {
		return p, err
	}
	p.Draining = raw == "true"
	err = s.read.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM runners WHERE state NOT IN ('removed','failed')),
  (SELECT COUNT(*) FROM runners WHERE state='busy'),
  (SELECT COUNT(*) FROM runners WHERE state IN ('removed','failed') AND cleaned_up_at IS NULL),
  (SELECT COUNT(*) FROM jobs WHERE state='in_progress'),
  (SELECT COUNT(*) FROM machines WHERE state IN ('planned','creating','starting','bootstrapping','enrolling','draining','deleting') OR op_id<>'' OR op_outcome_unknown=1)`).Scan(&p.LiveRunners, &p.BusyRunners, &p.PendingCleanup, &p.ActiveJobs, &p.MachineOperations)
	if err != nil {
		return p, err
	}
	f, err := s.RecoveryFenced(ctx)
	p.Ready = p.Draining && p.Quiescent() && f.Fenced
	return p, err
}

// Only unissued reservations are cancelled. A create whose outcome is unknown
// must still be observed; assuming it never happened could strand a resource.
func (s *Store) CancelUnstartedTransferMachines(ctx context.Context) ([]*Machine, error) {
	var out []*Machine
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `UPDATE machines SET state='failed', message='cancelled before creation while preparing instance transfer', updated_at=?
   WHERE state='planned' AND op_id='' AND op_outcome_unknown=0 AND resource_id='' AND create_started_at IS NULL RETURNING `+machineCols, ms(s.Now()))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMachine(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}
