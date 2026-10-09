package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// A restore is the one operation here that touches the live database, and
// every refusal in it is one an operator would otherwise discover after the
// controller was running on the restored data, which is the worst moment: the
// fleet is live, the original may already be gone, and the symptom -- a
// decryption error, a schema nobody understands -- says nothing about the
// restore that caused it. So the checks run before anything is moved, and the
// same checks run whether the restore was asked for on the command line or
// staged from the settings page.

// ErrControllerRunning is a restore attempted under a live controller.
var ErrControllerRunning = errors.New("backup: a controller is running on this database")

// RestoreOptions is what a restore may be asked to do beyond putting the file
// back.
type RestoreOptions struct {
	// SourceStopped acknowledges that cutover will not leave two live controllers.
	SourceStopped bool
	// TransferOperator establishes local destination access when no operator exists yet.
	TransferOperator *store.User
	// Replace allows a database already at database.path to be moved aside.
	// Without it, an existing database is refused.
	Replace bool
	// RevokeAPITokens and ResetAgentTokens go further than the defaults,
	// because each has a cost only the operator can weigh: revoking API
	// tokens breaks whatever automation holds them, and resetting agent
	// tokens means walking every host.
	RevokeAPITokens  bool
	ResetAgentTokens bool
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time
}

// RestoreReport is what a restore did, in the order it did it, written for
// the person who asked for it.
type RestoreReport struct {
	Source string `json:"source"`
	// MovedAside is where the database that was there went, if there was one.
	MovedAside string `json:"moved_aside,omitempty"`
	// MovedLogs names a write-ahead log that had outlived its database and
	// was kept rather than replayed.
	MovedLogs []string `json:"moved_logs,omitempty"`
	// KeyChecked says the manifest's fingerprint was compared with this
	// host's key, and matched.
	KeyChecked bool `json:"key_checked"`
	// ManifestProblem says why the manifest could not be checked, when it
	// could not. The database itself was still verified.
	ManifestProblem string `json:"manifest_problem,omitempty"`
	// Invalidated is what the restore took away, one line each.
	Invalidated []string `json:"invalidated"`
	// Fenced is the reason recorded on the fence.
	Fenced string `json:"fenced"`
}

// Check runs every pre-flight a restore makes and moves nothing: is the copy
// sound, can this build read it, and is the key on this host the one that
// sealed it. It returns the manifest, which may be nil when the backup has
// none, and the reason the manifest could not be read when that is so.
func Check(ctx context.Context, cfg *config.Config, dir string) (*Manifest, string, error) {
	dbPath := filepath.Join(dir, DBName)
	if _, err := os.Stat(dbPath); err != nil {
		return nil, "", fmt.Errorf("%s does not look like a Zoomies backup: %w", dir, err)
	}
	m, merr := ReadManifest(dir)
	problem := ""
	if merr != nil {
		problem = merr.Error()
		m = nil
	}
	// Checked in the order an operator would want to be stopped: is this
	// file sound, can this binary read it, and is the key on this host the
	// one that opens it?
	if err := checkCopy(ctx, dbPath); err != nil {
		return m, problem, err
	}
	if m != nil {
		if m.Transfer != nil {
			v, err := verifyDir(ctx, &Entry{Dir: dir, Manifest: m})
			if err != nil {
				return m, problem, err
			}
			if !v.OK {
				return m, problem, fmt.Errorf("transfer snapshot is not usable: %s", strings.Join(v.Problems, "; "))
			}
		}
		if m.Transfer != nil && (m.Transfer.Version != TransferVersion || !m.Transfer.Prepared) {
			return m, problem, errors.New("prepare this portable snapshot with an instance transfer import first")
		}
		if err := checkKey(cfg, m); err != nil {
			return m, problem, err
		}
	}
	return m, problem, nil
}

// checkCopy opens the backup's database and asks it two questions: is it
// sound, and can this binary read it?
//
// The second is the one that catches a rollback. The store refuses a ledger
// naming migrations this build does not have, so restoring a backup from a
// newer release onto an older binary stops here with the names, rather than
// after the controller has been started on it.
func checkCopy(ctx context.Context, path string) error {
	st, err := store.Open(ctx, store.Options{Path: path, ReadOnly: true})
	if err != nil {
		if errors.Is(err, store.ErrSchemaNewer) {
			return fmt.Errorf("this backup was written by a newer release than %s: %w", version.Short(), err)
		}
		return fmt.Errorf("opening the backup's database: %w", err)
	}
	defer func() { _ = st.Close() }()
	return st.IntegrityCheck(ctx)
}

// checkKey compares the key this host is configured with against the one that
// sealed the backup.
//
// A mismatch is refused rather than warned about. The alternative is a
// controller that starts, reports itself healthy, and fails inside its first
// GitHub call -- and by then the original database may be gone.
func checkKey(cfg *config.Config, m *Manifest) error {
	if m.Key.Fingerprint == "" {
		return nil
	}
	key, err := ConfiguredKey(cfg)
	if err != nil {
		return fmt.Errorf("this backup needs the encryption key whose fingerprint is %s, and this host has none: %w "+
			"(put it where security.encryption_key_file points, or pass it in ZOOMIES_ENCRYPTION_KEY)", m.Key.Fingerprint, err)
	}
	if got := key.Fingerprint(); got != m.Key.Fingerprint {
		return fmt.Errorf("the encryption key on this host (%s) is not the one that sealed this backup (%s). "+
			"Restoring anyway would produce a fleet that starts and cannot authenticate to GitHub; "+
			"put the right key in place first", got, m.Key.Fingerprint)
	}
	return nil
}

// ConfiguredKey resolves the key this host is configured with, without
// generating one: a restore is never a first run.
func ConfiguredKey(cfg *config.Config) (*cryptox.Key, error) {
	if raw := strings.TrimSpace(cfg.Security.EncryptionKey); raw != "" {
		return cryptox.ParseKey(raw)
	}
	if path := strings.TrimSpace(cfg.Security.EncryptionKeyFile); path != "" {
		return cryptox.LoadKeyFile(path)
	}
	return nil, cryptox.ErrNoKey
}

// Restore puts the backup at dir in place at cfg.Database.Path.
//
// The caller holds the database lock, or is certain nothing else does: Lock
// is the same lock a controller takes before it opens the database, and
// holding it for the whole swap both proves none is running now and stops one
// starting halfway through. Renaming a file does not reach a process that
// already has it open, so a restore under a live controller loses whatever
// the fleet did while the file was being replaced, silently.
//
// It restores the database and nothing else. The encryption key, the
// configuration and the service unit are the operator's to put back, because
// each of them is a decision about this host rather than a copy of the data.
func Restore(ctx context.Context, cfg *config.Config, dir string, opts RestoreOptions) (*RestoreReport, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	m, problem, err := Check(ctx, cfg, dir)
	if err != nil {
		return nil, err
	}
	if m != nil && m.Transfer != nil {
		prepared, cleanup, err := prepareTransferRestore(ctx, cfg, dir, opts)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		opts.Replace = true
		report, err := Restore(ctx, cfg, prepared, opts)
		if report != nil {
			report.Source = dir
			report.Invalidated = append(report.Invalidated, "Replaced source operator access and sign-in configuration with the destination's; external sign-in needs a destination mapping.")
		}
		return report, err
	}
	report := &RestoreReport{Source: dir, ManifestProblem: problem, KeyChecked: m != nil && m.Key.Fingerprint != "", Invalidated: []string{}}

	live := cfg.Database.Path
	backupDB := filepath.Join(dir, DBName)
	if err := os.MkdirAll(filepath.Dir(live), 0o750); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(live), err)
	}

	// Restoring a backup over itself would move the source aside and then
	// copy from a path that is no longer there, so it is refused while both
	// still exist. It is an easy mistake to make: the argument is a
	// directory, and pointing it at the state directory reads as "restore
	// what is here".
	if same, err := sameFile(backupDB, live); err != nil {
		return nil, err
	} else if same {
		return nil, fmt.Errorf("%s is the database being restored to, not a backup of it. "+
			"Point restore at a directory `zoomies backup` wrote", backupDB)
	}

	stamp := now().UTC().Format("20060102-150405")
	switch _, err := os.Stat(live); {
	case err == nil:
		if !opts.Replace {
			return nil, fmt.Errorf("%s already exists, and restoring over it would lose whatever is in it. "+
				"Pass --replace to overwrite it; a copy of it is taken first, beside it", live)
		}
		kept, err := setAsideLiveDatabase(live, stamp)
		if err != nil {
			return nil, err
		}
		report.MovedAside = kept

	case errors.Is(err, os.ErrNotExist):
		// The database is gone; its write-ahead log may not be. That pair is
		// exactly what a corrupt-database recovery leaves behind, and it is
		// the state a restore is most often run in.
		//
		// SQLite replays a log it finds beside a database, whatever database
		// wrote it. A restored database opened on top of somebody else's log
		// comes back "database disk image is malformed", and the replay
		// consumes and unlinks the log doing it: the committed transactions
		// that only existed in that log are gone, destroyed by a command that
		// then reported failure. So the log is moved aside rather than
		// removed. It is the last copy of whatever those transactions were,
		// and an operator who came here to recover data should not lose more
		// of it to the recovery.
		if !hasOrphanedLogs(live) {
			break
		}
		if !opts.Replace {
			return nil, fmt.Errorf("%s is gone but its write-ahead log is still there, and SQLite would replay "+
				"that log into whatever is restored, corrupting it and destroying the log in the process. "+
				"Pass --replace to move the log aside first; it is kept, not deleted, beside the database", live)
		}
		kept, err := setAsideOrphanedLogs(live, stamp)
		if err != nil {
			return nil, err
		}
		report.MovedLogs = kept

	default:
		return nil, fmt.Errorf("checking %s: %w", live, err)
	}

	if err := copyFile(backupDB, live); err != nil {
		return nil, err
	}
	if err := invalidate(ctx, live, dir, m, opts, report); err != nil {
		return nil, restoreUnfinished(err, live, report)
	}
	return report, nil
}

// restoreUnfinished says what state a restore that failed after the copy left
// behind. The copy has replaced the database by then, so "the restore failed"
// on its own reads as "nothing changed" -- and the controller would then start
// on the restored database believing it was the old one. Nothing is rolled
// back: the fence is what makes the restored copy safe to leave in place, and
// undoing a file swap on the strength of a failing disk is a second way to
// lose the database.
func restoreUnfinished(err error, live string, report *RestoreReport) error {
	var before string
	switch {
	case report.MovedAside != "":
		before = "The database that was there before is kept at " + report.MovedAside + "."
	case len(report.MovedLogs) > 0:
		before = "The write-ahead log that was left behind is kept at " + strings.Join(report.MovedLogs, ", ") + "."
	default:
		before = "There was no database there before."
	}
	if report.Fenced != "" {
		return fmt.Errorf("the backup was copied into place but the restore did not finish: %w. "+
			"The restored database is now in place at %s and its recovery fence is up, so this fleet will not act on "+
			"rented machines until an administrator lifts it, but some of the credentials a restore ends may still be live. %s "+
			"Put right what the error names and run the restore again", err, live, before)
	}
	return fmt.Errorf("the backup was copied into place but the restore did not finish: %w. "+
		"The restored database is now in place at %s and its recovery fence could not be set, so do not start a controller on it. %s "+
		"Put right what the error names and run the restore again, or move the kept database back", err, live, before)
}

// invalidate does the part that is not copying a file: it fences the fleet, and
// takes away the credentials the backup froze.
//
// The fence is written first. It is the one step whose absence is dangerous --
// a restored copy that has not been fenced may delete rented machines created
// after the backup -- so a failure part-way through leaves the fleet fenced
// rather than half-invalidated and free to act.
func invalidate(ctx context.Context, live, src string, m *Manifest, opts RestoreOptions, report *RestoreReport) error {
	st, err := store.Open(ctx, store.Options{Path: live})
	if err != nil {
		return fmt.Errorf("opening the restored database: %w", err)
	}
	defer func() { _ = st.Close() }()

	reason := "restored from " + src
	if m != nil && !m.TakenAt.IsZero() {
		reason = fmt.Sprintf("restored from %s, a backup taken %s", src, m.TakenAt.Format(time.RFC3339))
	}
	if err := st.SetRecoveryFence(ctx, true, reason); err != nil {
		return err
	}
	report.Fenced = reason

	sessions, err := st.DeleteAllSessions(ctx)
	if err != nil {
		return err
	}
	report.Invalidated = append(report.Invalidated, fmt.Sprintf("Ended %s: a cookie from the day of the backup would otherwise still be signed in.", countOf(int(sessions), "session")))

	joins, err := st.DeleteUnusedJoinTokens(ctx)
	if err != nil {
		return err
	}
	report.Invalidated = append(report.Invalidated, fmt.Sprintf("Removed %s that had never been redeemed; the redeemed ones are history and were kept.", countOf(int(joins), "join token")))

	machines, err := st.MarkMachinesUnverified(ctx)
	if err != nil {
		return err
	}
	report.Invalidated = append(report.Invalidated, fmt.Sprintf("Marked %s as unverified: this copy proves it still owns a rented machine before it may delete one.", countOf(int(machines), "machine")))

	if opts.RevokeAPITokens {
		n, err := st.RevokeAllAPITokens(ctx)
		if err != nil {
			return err
		}
		report.Invalidated = append(report.Invalidated, fmt.Sprintf("Revoked %s. Anything automated that held one needs a new one.", countOf(int(n), "API token")))
	} else {
		report.Invalidated = append(report.Invalidated, "Left the API tokens working. If this backup may have been read by anyone else, revoke them.")
	}
	if opts.ResetAgentTokens {
		n, err := st.ResetAgentTokens(ctx)
		if err != nil {
			return err
		}
		report.Invalidated = append(report.Invalidated, fmt.Sprintf("Forgot the agent credential on %s; each will exit with the command to join again.", countOf(int(n), "host")))
	}

	// The audit row is written to the restored database, which is where
	// anyone asking "why is this fleet fenced?" will look.
	return st.AppendAudit(ctx, &store.AuditEvent{
		ActorKind: "system", ActorName: "zoomies restore",
		Action: "instance.restore", TargetKind: "instance",
		After: reason,
	})
}

// hasOrphanedLogs reports whether a write-ahead log or shared-memory file is
// sitting beside a database that is not there.
func hasOrphanedLogs(live string) bool {
	for _, ext := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(live + ext); err == nil {
			return true
		}
	}
	return false
}

// setAsideOrphanedLogs moves that pair out of the way, under the same name the
// database itself would have been kept under. Renamed and never removed: the
// log may hold committed transactions that exist nowhere else.
func setAsideOrphanedLogs(live, stamp string) ([]string, error) {
	kept := live + ".before-restore-" + stamp
	var moved []string
	for _, ext := range []string{"-wal", "-shm"} {
		if err := os.Rename(live+ext, kept+ext); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("moving %s aside: %w", live+ext, err)
		}
		moved = append(moved, kept+ext)
	}
	return moved, nil
}

// setAsideLiveDatabase moves the database that is already there out of the way
// and returns where it went.
//
// It is a raw file move of all three files rather than a store backup, and it
// has to be: the live database may be the newer one -- an operator restoring
// an old copy after a bad upgrade -- and the store refuses to open a database
// whose ledger it does not know. A pre-restore copy that only worked when it
// was not needed would be worse than none.
func setAsideLiveDatabase(live, stamp string) (string, error) {
	kept := live + ".before-restore-" + stamp
	if err := os.Rename(live, kept); err != nil {
		return "", fmt.Errorf("moving %s aside: %w", live, err)
	}
	// The write-ahead log and shared-memory file are part of that database.
	// Leaving them beside the restored copy would be worse than losing them:
	// SQLite would replay another database's log into this one.
	for _, ext := range []string{"-wal", "-shm"} {
		if err := os.Rename(live+ext, kept+ext); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("moving %s aside: %w", live+ext, err)
		}
	}
	return kept, nil
}

// sameFile reports whether two paths are the same file on disk. A path that is
// not there is not the same file as anything.
func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s: %w", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s: %w", b, err)
	}
	return os.SameFile(fa, fb), nil
}

// copyFile writes src to dest at owner-only permissions, refusing to leave a
// half-written database behind.
func copyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}

// countOf renders "1 backup" and "3 backups".
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// ---------------------------------------------------------------------------
// A staged restore
// ---------------------------------------------------------------------------

// A controller cannot restore under itself: the database is open, every loop
// holds the store, and a file swapped under a process that has it open is a
// process reading a file nobody else can see. So a restore asked for while the
// controller is running is staged -- checked, then written down beside the
// database -- and applied by the next controller to start, before it opens
// anything. The settings page asks for the restart; a service manager brings
// the process back; the restore happens in the gap.

// The two files beside the database that carry a staged restore and what
// became of the last one.
const (
	stagedName  = "restore-staged.json"
	outcomeName = "restore-outcome.json"
)

// Staged is a restore that has been checked and is waiting for a restart.
type Staged struct {
	SourceStopped bool `json:"source_stopped,omitempty"`
	// BackupID and Dir name the backup; the id is for people and the
	// directory is what the restore opens.
	BackupID string `json:"backup_id"`
	Dir      string `json:"dir"`
	// TakenAt is when the backup was taken, for the page that shows what is
	// about to be put back.
	TakenAt time.Time `json:"taken_at,omitempty"`
	// RequestedAt and RequestedBy say who asked, and when.
	RequestedAt time.Time `json:"requested_at"`
	RequestedBy string    `json:"requested_by,omitempty"`
	// The options the operator chose.
	RevokeAPITokens  bool `json:"revoke_api_tokens"`
	ResetAgentTokens bool `json:"reset_agent_tokens"`
}

// Outcome is what the last staged restore did, kept so that the settings page
// can say so after the restart rather than leaving the operator to infer it
// from the fence.
type Outcome struct {
	BackupID    string    `json:"backup_id"`
	AttemptedAt time.Time `json:"attempted_at"`
	OK          bool      `json:"ok"`
	// Error is why it did not happen, and the sentence the page shows. When
	// the failure came after the copy, it says which database is in place and
	// whether it is fenced.
	Error  string         `json:"error,omitempty"`
	Report *RestoreReport `json:"report,omitempty"`
}

func stagedPath(dbPath string) string  { return filepath.Join(filepath.Dir(dbPath), stagedName) }
func outcomePath(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), outcomeName) }

// Stage checks a backup as a restore would and, when every check passes,
// records it as the restore to apply at the next start.
//
// The checks are run now rather than at the restart, because now is when the
// operator is looking: a refusal at startup lands in a log they may not be
// reading, and the fleet then starts on the old database with nothing on the
// page to say the restore did not happen.
func Stage(ctx context.Context, cfg *config.Config, entry *Entry, s Staged) (*Staged, error) {
	m, _, err := Check(ctx, cfg, entry.Dir)
	if err != nil {
		return nil, err
	}
	if m != nil && m.Transfer != nil {
		prepared, cleanup, err := prepareTransferRestore(ctx, cfg, entry.Dir, RestoreOptions{SourceStopped: s.SourceStopped})
		if err != nil {
			return nil, err
		}
		_ = prepared
		cleanup()
	}
	s.BackupID = entry.ID
	s.Dir = entry.Dir
	s.TakenAt = entry.TakenAt
	if m != nil && !m.TakenAt.IsZero() {
		s.TakenAt = m.TakenAt
	}
	if s.RequestedAt.IsZero() {
		s.RequestedAt = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(stagedPath(cfg.Database.Path), append(raw, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("backup: recording the staged restore: %w", err)
	}
	return &s, nil
}

// LoadStaged returns the staged restore, or nil when none is waiting.
func LoadStaged(dbPath string) (*Staged, error) {
	raw, err := os.ReadFile(stagedPath(dbPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: reading the staged restore: %w", err)
	}
	var s Staged
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("backup: %s is not a staged restore: %w", stagedPath(dbPath), err)
	}
	return &s, nil
}

// CancelStaged forgets a staged restore. Cancelling when none is staged is
// not an error: the operator asked for there to be none, and there is none.
func CancelStaged(dbPath string) error {
	err := os.Remove(stagedPath(dbPath))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: cancelling the staged restore: %w", err)
	}
	return nil
}

// LastOutcome returns what became of the last staged restore, or nil when
// there has never been one.
func LastOutcome(dbPath string) (*Outcome, error) {
	raw, err := os.ReadFile(outcomePath(dbPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: reading the last restore's outcome: %w", err)
	}
	var o Outcome
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("backup: %s is not a restore outcome: %w", outcomePath(dbPath), err)
	}
	return &o, nil
}

// ClearOutcome forgets the last outcome, which is how the page dismisses it.
func ClearOutcome(dbPath string) error {
	err := os.Remove(outcomePath(dbPath))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: clearing the last restore's outcome: %w", err)
	}
	return nil
}

// ApplyStaged performs the staged restore, if there is one, and records what
// happened. It returns the outcome, or nil when nothing was staged.
//
// The caller holds the database lock and has not opened the store: this runs
// in the gap between one controller stopping and the next opening the file.
//
// The staged file is removed whatever happens. A restore that failed is not
// retried at every start -- the failure is recorded for the page to show. A
// restore that was refused before anything moved leaves the controller on the
// database it had; one that failed after the copy has already replaced it, and
// the recorded error says so and whether the fence is up. Either way the
// operator can still reach the page through what starts.
func ApplyStaged(ctx context.Context, cfg *config.Config, now func() time.Time) (*Outcome, error) {
	if now == nil {
		now = time.Now
	}
	s, err := LoadStaged(cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	_ = CancelStaged(cfg.Database.Path)

	out := &Outcome{BackupID: s.BackupID, AttemptedAt: now().UTC()}
	report, err := Restore(ctx, cfg, s.Dir, RestoreOptions{
		Replace:          true,
		SourceStopped:    s.SourceStopped,
		RevokeAPITokens:  s.RevokeAPITokens,
		ResetAgentTokens: s.ResetAgentTokens,
		Now:              now,
	})
	if err != nil {
		out.Error = err.Error()
	} else {
		out.OK = true
		out.Report = report
	}
	raw, merr := json.MarshalIndent(out, "", "  ")
	if merr == nil {
		if werr := os.WriteFile(outcomePath(cfg.Database.Path), append(raw, '\n'), 0o600); werr != nil {
			return out, fmt.Errorf("backup: recording the restore's outcome: %w", werr)
		}
	}
	return out, nil
}
