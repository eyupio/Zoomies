package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
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
		`SELECT COUNT(*) FROM jobs WHERE ` + transferActiveJobSQL,
		`SELECT COUNT(*) FROM machines WHERE ` + transferMachineSQL,
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

// transferActiveJobSQL names the jobs a cutover could interrupt: those GitHub
// says are in progress on a runner this fleet still has. GitHub reports every
// job in an installed repository, and on an organisation that also uses its
// hosted runners most of them run somewhere this controller cannot drain. The
// first version of this count waited for all of them, so a transfer sat at
// "11 running jobs" that nothing here could hurry or stop. A job on one of
// this fleet's runners that has since been removed or failed is not waited for
// either: GitHub will finish it on its own, and the fleet's part is over.
const transferActiveJobSQL = `jobs.state='in_progress' AND jobs.runner_id<>''
	AND jobs.runner_id IN (SELECT id FROM runners WHERE state NOT IN ('removed','failed'))`

// transferMachineSQL names the machines whose provider operation is still in
// flight, or whose outcome is not known: a clone the destination would find
// half-made, or a delete it would find done but unrecorded.
const transferMachineSQL = `machines.state IN ('planned','creating','starting','bootstrapping','enrolling','draining','deleting')
	OR machines.op_id<>'' OR machines.op_outcome_unknown=1`

// transferWaitLimit bounds how many of each kind of wait are named. The counts
// are exact; the list is what an operator reads, and a fleet with hundreds of
// runners to confirm gone needs the first few and the host they share.
const transferWaitLimit = 25

// TransferWait is one thing preparation is waiting for, named: a runner
// still live, a runner nobody has confirmed gone, a job on this fleet, or a
// machine operation. It carries the facts; the controller writes the sentence.
type TransferWait struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// Repo is the repository a job belongs to.
	Repo string `json:"repo,omitempty"`
	// State is the runner's or machine's state, as its own page shows it.
	State string `json:"state,omitempty"`
	// HostID and HostName are the host a runner or job is on. HostHealthy says
	// whether that host has sent a heartbeat lately, because a removed runner
	// is confirmed gone by its host and nobody else can do it for an agent
	// that has stopped talking. The controller sets it from HostLastHeartbeat
	// against its own clock, as it does for every host view.
	HostID            string    `json:"host_id,omitempty"`
	HostName          string    `json:"host,omitempty"`
	HostHealthy       bool      `json:"host_healthy"`
	HostLastHeartbeat time.Time `json:"-"`
	// HostConfirmed and RegistrationConfirmed are the two halves of a cleanup:
	// the host has seen the workload gone, and GitHub has seen the
	// registration gone. A cleanup wait has at least one of them false.
	HostConfirmed         bool `json:"host_confirmed"`
	RegistrationConfirmed bool `json:"registration_confirmed"`
	// Error is the cleanup failure recorded on the runner, if any.
	Error string `json:"error,omitempty"`
	// Since is when the wait began: the runner's finish, the job's start, the
	// machine's last change.
	Since *time.Time `json:"since,omitempty"`
	// Detail is the sentence the controller writes about this wait, for the
	// page and the CLI to show as they are.
	Detail string `json:"detail"`
}

// The kinds of TransferWait.
const (
	TransferWaitRunner  = "runner"
	TransferWaitCleanup = "cleanup"
	TransferWaitJob     = "job"
	TransferWaitMachine = "machine"
)

type TransferProgress struct {
	Draining          bool `json:"draining"`
	Ready             bool `json:"ready"`
	LiveRunners       int  `json:"live_runners"`
	BusyRunners       int  `json:"busy_runners"`
	PendingCleanup    int  `json:"pending_cleanup"`
	ActiveJobs        int  `json:"active_jobs"`
	MachineOperations int  `json:"machine_operations"`
	// The pending cleanup, split by what is outstanding. A runner can be
	// counted in more than one: both sides may be unconfirmed, and a failed
	// cleanup is unconfirmed on at least one of them.
	CleanupAwaitingHost   int `json:"cleanup_awaiting_host"`
	CleanupAwaitingGitHub int `json:"cleanup_awaiting_github"`
	CleanupFailed         int `json:"cleanup_failed"`
	// Waiting names what the counts count, up to transferWaitLimit of each
	// kind, so that "8 awaiting cleanup" is eight runners with a host each.
	Waiting []TransferWait `json:"waiting"`
	// Summary is the sentence the controller writes about where preparation
	// stands and what, if anything, an operator has to do about it.
	Summary string `json:"summary"`
}

func (p TransferProgress) Quiescent() bool {
	return p.LiveRunners == 0 && p.PendingCleanup == 0 && p.ActiveJobs == 0 && p.MachineOperations == 0
}

func (s *Store) TransferProgress(ctx context.Context) (TransferProgress, error) {
	p := TransferProgress{Waiting: []TransferWait{}}
	raw, err := s.GetSetting(ctx, SettingTransferDraining)
	if err != nil {
		return p, err
	}
	p.Draining = raw == "true"
	err = s.read.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM runners WHERE state NOT IN ('removed','failed')),
  (SELECT COUNT(*) FROM runners WHERE state='busy'),
  (SELECT COUNT(*) FROM runners WHERE state IN ('removed','failed') AND cleaned_up_at IS NULL),
  (SELECT COUNT(*) FROM jobs WHERE `+transferActiveJobSQL+`),
  (SELECT COUNT(*) FROM machines WHERE `+transferMachineSQL+`),
  (SELECT COUNT(*) FROM runners WHERE state IN ('removed','failed') AND cleaned_up_at IS NULL AND host_removed_at IS NULL),
  (SELECT COUNT(*) FROM runners WHERE state IN ('removed','failed') AND cleaned_up_at IS NULL AND registration_deleted_at IS NULL),
  (SELECT COUNT(*) FROM runners WHERE state IN ('removed','failed') AND cleaned_up_at IS NULL AND cleanup_error<>'')`).Scan(
		&p.LiveRunners, &p.BusyRunners, &p.PendingCleanup, &p.ActiveJobs, &p.MachineOperations,
		&p.CleanupAwaitingHost, &p.CleanupAwaitingGitHub, &p.CleanupFailed)
	if err != nil {
		return p, err
	}
	if p.Draining && !p.Quiescent() {
		if p.Waiting, err = s.transferWaits(ctx); err != nil {
			return p, err
		}
	}
	f, err := s.RecoveryFenced(ctx)
	p.Ready = p.Draining && p.Quiescent() && f.Fenced
	return p, err
}

// transferWaits names what TransferProgress counts. Each kind is read with
// the host it is on, oldest first, because the oldest wait is the one most
// likely to be stuck rather than merely slow.
func (s *Store) transferWaits(ctx context.Context) ([]TransferWait, error) {
	beatAt := func(beat sql.NullInt64) time.Time {
		if !beat.Valid {
			return time.Time{}
		}
		return time.UnixMilli(beat.Int64).UTC()
	}
	out := []TransferWait{}
	collect := func(query string, scan func(*sql.Rows) (TransferWait, error)) error {
		rows, err := s.read.QueryContext(ctx, query, transferWaitLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	}
	runnerWait := func(kind string) func(*sql.Rows) (TransferWait, error) {
		return func(rows *sql.Rows) (TransferWait, error) {
			w := TransferWait{Kind: kind}
			var hostID, hostName sql.NullString
			var beat, since, hostDone, githubDone sql.NullInt64
			if err := rows.Scan(&w.ID, &w.Name, &w.State, &hostID, &hostName, &beat, &since, &hostDone, &githubDone, &w.Error); err != nil {
				return w, err
			}
			w.HostID, w.HostName, w.HostLastHeartbeat = hostID.String, hostName.String, beatAt(beat)
			w.Since = atp(since)
			w.HostConfirmed, w.RegistrationConfirmed = hostDone.Valid, githubDone.Valid
			return w, nil
		}
	}
	const runnerSelect = `SELECT r.id, r.name, r.state, r.host_id, h.name, h.last_heartbeat,
		COALESCE(r.finished_at, r.started_at, r.created_at), r.host_removed_at, r.registration_deleted_at, r.cleanup_error
		FROM runners r LEFT JOIN hosts h ON h.id=r.host_id `
	if err := collect(runnerSelect+`WHERE r.state NOT IN ('removed','failed') ORDER BY r.created_at LIMIT ?`,
		runnerWait(TransferWaitRunner)); err != nil {
		return nil, err
	}
	if err := collect(runnerSelect+`WHERE r.state IN ('removed','failed') AND r.cleaned_up_at IS NULL ORDER BY COALESCE(r.finished_at, r.created_at) LIMIT ?`,
		runnerWait(TransferWaitCleanup)); err != nil {
		return nil, err
	}
	if err := collect(`SELECT jobs.id, jobs.job_name, jobs.repo, jobs.runner_id, r.host_id, h.name, h.last_heartbeat, jobs.started_at
		FROM jobs LEFT JOIN runners r ON r.id=jobs.runner_id LEFT JOIN hosts h ON h.id=r.host_id
		WHERE `+transferActiveJobSQL+` ORDER BY jobs.started_at LIMIT ?`, func(rows *sql.Rows) (TransferWait, error) {
		w := TransferWait{Kind: TransferWaitJob, State: string(JobInProgress)}
		var runnerID string
		var hostID, hostName sql.NullString
		var beat, since sql.NullInt64
		if err := rows.Scan(&w.ID, &w.Name, &w.Repo, &runnerID, &hostID, &hostName, &beat, &since); err != nil {
			return w, err
		}
		w.HostID, w.HostName, w.HostLastHeartbeat = hostID.String, hostName.String, beatAt(beat)
		w.Since = atp(since)
		return w, nil
	}); err != nil {
		return nil, err
	}
	if err := collect(`SELECT machines.id, machines.name, machines.state, machines.updated_at FROM machines
		WHERE `+transferMachineSQL+` ORDER BY machines.updated_at LIMIT ?`, func(rows *sql.Rows) (TransferWait, error) {
		w := TransferWait{Kind: TransferWaitMachine}
		var since sql.NullInt64
		if err := rows.Scan(&w.ID, &w.Name, &w.State, &since); err != nil {
			return w, err
		}
		w.Since = atp(since)
		return w, nil
	}); err != nil {
		return nil, err
	}
	return out, nil
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
