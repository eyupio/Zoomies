package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The layout one backup has on disk, shared so that a copy this package takes
// before a migration is restorable by exactly the command that restores a copy
// an operator took by hand. Two namings for one thing would mean a
// pre-migration copy nobody could put back without knowing it was special.
const (
	// BackupDirPrefix begins the name of one backup's directory; the rest is
	// the instant it was taken.
	BackupDirPrefix = "zoomies-"
	// BackupDBName is the database inside it.
	BackupDBName = "zoomies.db"
	// PreMigrationDir is where this package puts the copy it takes before
	// applying migrations, beside the database.
	//
	// Its own directory rather than the operator's: retention here deletes,
	// and the directory somebody points `zoomies backup --dir` at is often
	// shared. A rule that kept "the last two" in there would eventually take
	// one of theirs.
	PreMigrationDir = "pre-migration"
	// preMigrationKeep is how many of these are worth having. One is the
	// upgrade that just happened; two covers the upgrade before it, which is
	// the one an operator reaches for when the first went unnoticed. More
	// would be a copy of the whole database per release, kept forever, on the
	// disk the fleet also needs.
	preMigrationKeep = 2
)

// Backup writes a consistent copy of the database to dest.
//
// It is `VACUUM INTO` rather than a file copy, and that is the whole reason
// this exists in the store: a running instance keeps a write-ahead log, so
// copying the .db file alone produces something that is missing every recent
// commit, and copying all three files while a write is in flight produces
// something that is missing part of one. VACUUM INTO takes a consistent read snapshot, so
// what lands is one file, already checkpointed, with no WAL beside it -- a
// database an operator can move, verify and open anywhere.
//
// The documentation used to send operators to the sqlite3 command line for
// this. That binary is not in the container image, so the instructions could
// not be followed by most of the people reading them.
//
// It works on a store opened read-only, which is how `zoomies backup` uses it:
// VACUUM INTO only reads the source, and taking a backup must not be a reason
// to migrate the database being backed up.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if strings.TrimSpace(dest) == "" {
		return errors.New("store: a backup needs a destination path")
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("store: resolving %q: %w", dest, err)
	}
	// Refusing an existing file rather than overwriting it: the destination of
	// a backup is nearly always a name derived from the date, and the one time
	// it is not, the file already there is somebody's older backup. Losing that
	// to a typo is the failure this guards.
	if _, err := os.Stat(abs); err == nil {
		return fmt.Errorf("store: %s already exists; a backup never overwrites one", abs)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: checking %s: %w", abs, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return fmt.Errorf("store: creating %s: %w", filepath.Dir(abs), err)
	}

	if _, err := s.read.ExecContext(ctx, `VACUUM INTO ?`, abs); err != nil {
		return fmt.Errorf("store: copying the database to %s: %w", abs, err)
	}
	// A backup holds every sealed secret this instance has and every account's
	// password hash. VACUUM INTO creates the file with the process umask,
	// which on a default Debian image is world-readable.
	if err := os.Chmod(abs, 0o600); err != nil {
		return fmt.Errorf("store: tightening permissions on %s: %w", abs, err)
	}
	return nil
}

// IntegrityCheck asks SQLite whether the database is internally consistent.
//
// It is what makes a backup a backup rather than a file: a copy nobody has
// opened is a copy nobody knows about, and the moment to find out is while the
// original is still there. `PRAGMA integrity_check` walks every page, index
// and constraint, so it is not instant on a large database and is not run on
// the ordinary startup path.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	rows, err := s.read.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("store: checking the integrity of %s: %w", s.path, err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("store: checking the integrity of %s: %w", s.path, err)
		}
		// SQLite answers a healthy database with the single row "ok".
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: checking the integrity of %s: %w", s.path, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("store: %s is corrupt: %s", s.path, strings.Join(problems, "; "))
	}
	return nil
}

// HasSealedSecrets reports whether anything in this database is encrypted with
// the instance key.
//
// It answers one question, asked at startup: may a new key be generated? A
// fresh install has nothing sealed and a generated key is right; a database
// with installations in it has a GitHub App private key and a webhook secret
// that only the original key opens, and generating a new one there produces an
// instance that starts, looks healthy, and fails inside its first GitHub call.
//
// A provider's credential is the same trap one layer out: a controller that
// generated a fresh key would start, show its hypervisor as configured, and
// fail inside the first call that tried to rent a machine.
//
// So are an account's authenticator secret and an offsite backup remote's
// credentials: an instance with no installation yet can still have both, and a
// fresh key would fail every enrolled sign-in and orphan every archive already
// uploaded under a passphrase. A two-step enrolment nobody confirmed is not
// counted; nothing depends on it and starting again replaces it.
func (s *Store) HasSealedSecrets(ctx context.Context) (bool, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM installations
		WHERE (private_key_enc IS NOT NULL AND LENGTH(private_key_enc) > 0)
		   OR (webhook_secret_enc IS NOT NULL AND LENGTH(webhook_secret_enc) > 0)
		) + (SELECT COUNT(*) FROM settings WHERE key='tailcat.identity.v1' AND value<>'')
		  + (SELECT COUNT(*) FROM providers
		WHERE (credentials_enc IS NOT NULL AND LENGTH(credentials_enc) > 0)
		   OR (tailcat_address_enc IS NOT NULL AND LENGTH(tailcat_address_enc) > 0))
		  + (SELECT COUNT(*) FROM user_two_step
		WHERE enabled_at IS NOT NULL AND LENGTH(secret_enc) > 0)
		  + (SELECT COUNT(*) FROM backup_remotes
		WHERE (secret_key_enc IS NOT NULL AND LENGTH(secret_key_enc) > 0)
		   OR (passphrase_enc IS NOT NULL AND LENGTH(passphrase_enc) > 0))
		  + (SELECT COUNT(*) FROM instance_settings WHERE secret=1 AND value<>'')`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: counting sealed secrets: %w", err)
	}
	return n > 0, nil
}

// backupBeforeMigrate copies the database before any migration touches it.
//
// It is the one moment a rollback is both most likely to be wanted and least
// likely to have been prepared for: an operator upgrading is not thinking
// about backups, migrations are one-way, and an older binary will now refuse
// to open a database that has moved on. This costs a file copy once per
// release and buys the only rollback there is.
//
// It runs only when there is something to lose. A database being created has
// no rows and no pending-migration risk worth a copy, and an in-memory one has
// nowhere to put it.
func (s *Store) backupBeforeMigrate(ctx context.Context, pending []string) (string, error) {
	if len(pending) == 0 || s.path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(s.path)
	if err != nil {
		return "", err
	}
	root := filepath.Join(filepath.Dir(abs), PreMigrationDir)
	dest := filepath.Join(root, BackupDirPrefix+s.Now().Format("20060102-150405"))
	if err := s.Backup(ctx, filepath.Join(dest, BackupDBName)); err != nil {
		return "", err
	}
	// Pruning after rather than before: a failure to tidy up must not stop an
	// upgrade, and the copy that matters most is the one just taken.
	if err := pruneDirs(root, preMigrationKeep); err != nil {
		slog.Warn("could not prune old pre-migration copies", "dir", root, "error", err)
	}
	return dest, nil
}

// pruneDirs keeps the newest keep directories under root and removes the rest.
//
// It only considers directories this package names, and sorts by that name:
// the timestamp is in it, so the order does not depend on a modification time
// that copying between machines rewrites.
func pruneDirs(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var ours []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), BackupDirPrefix) {
			ours = append(ours, e.Name())
		}
	}
	sort.Strings(ours)
	if len(ours) <= keep {
		return nil
	}
	var errs []error
	for _, name := range ours[:len(ours)-keep] {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
