package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary stays static
)

// The migrations run in lexical order of their full file name, and the ledger
// that records what has been applied is keyed by that full name too. Two
// things follow, and a test holds both. A shipped file's name is immutable:
// renaming one re-applies its DDL on every existing database, which fails or
// worse. And a new file takes the next unused numeric prefix, alone: two files
// sharing a prefix sort on what follows the underscore, which is a fact about
// spelling and not about the order the schema needs. Two such pairs already
// shipped (0005 and 0006) and stay as they are, because of the first rule.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned by every Get* method when the row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a uniqueness constraint would be violated.
var ErrConflict = errors.New("already exists")

// ErrInvalidTransition is returned when a runner state change is not legal.
var ErrInvalidTransition = errors.New("invalid state transition")

// ErrSchemaNewer is returned by Open when the database's migration ledger
// names migrations this build does not have.
//
// It is a refusal rather than a warning because the alternative is worse than
// stopping: an older binary against a newer schema reads columns whose meaning
// it does not know and writes rows the newer one will not accept, and it does
// it silently. Rolling a release back is a thing operators do under pressure,
// and this is the moment to tell them the database went forward with it.
var ErrSchemaNewer = errors.New("database is newer than this build of Zoomies")

// ErrReadOnly is returned when a write is attempted on a store opened with
// Options.ReadOnly.
var ErrReadOnly = errors.New("store: this database was opened read-only")

// ErrJoinTokenUsed and ErrJoinTokenExpired are the two ways a join token that
// exists can still be refused. They are sentinels rather than prose so the
// caller can tell a refusal, which is the agent operator's to act on, from a
// failure of the database, which is not.
var (
	ErrJoinTokenUsed    = errors.New("join token has already been used")
	ErrJoinTokenExpired = errors.New("join token has expired")
)

// ErrOwnershipUnverified is a refusal, not a failure: the row and the resource
// disagree about who owns it, and the answer is a person looking rather than a
// retry.
var ErrOwnershipUnverified = errors.New("this machine's ownership could not be verified")

// ErrMachineBusy is a machine whose current step another pass already holds.
// It is a sentinel because the caller's answer is to come back on the next
// pass, which is not what it does with a database failure.
var ErrMachineBusy = errors.New("an operation is already in flight for this machine")

// ErrJoinTokenScope is a token redeemed by something other than the machine it
// was minted for. It is separate from ErrJoinTokenUsed and ErrJoinTokenExpired
// because it is the only one of the three that means somebody copied a
// credential out of a guest.
var ErrJoinTokenScope = errors.New("join token was issued for a different machine")

// walSizeLimit is the size the write-ahead log is truncated back to whenever a
// checkpoint resets it; see the journal_size_limit pragma in Open.
const walSizeLimit = 16 << 20

// Store is the single owner of the SQLite database.
//
// SQLite allows exactly one writer at a time. Rather than hope callers behave,
// writes are funnelled through a mutex and a single connection, while reads use
// a separate pooled connection in WAL mode. This keeps the "database is locked"
// failure mode -- the usual reason small SQLite services fall over -- out of
// the codebase entirely.
type Store struct {
	read  *sql.DB
	write *sql.DB
	wmu   sync.Mutex
	// writes, when set, is told how long each write waited for wmu and how
	// long it then held it; see ObserveWrites.
	writes atomic.Pointer[WriteObserver]
	path   string
	now    func() time.Time
	// readOnly refuses writes here rather than letting SQLite refuse them,
	// because "attempt to write a readonly database" names the file and not
	// the decision that made it read-only.
	readOnly bool
}

// Options configures Open.
type Options struct {
	// Path is the database file. ":memory:" is accepted for tests.
	Path string
	// Now overrides the clock; tests set this for deterministic timestamps.
	Now func() time.Time
	// ReadOnly opens the file without writing to it and without migrating.
	//
	// Migration is the reason this exists. Opening a database read-write
	// applies every pending migration as a side effect, so a command that only
	// wanted to look -- has this install finished, which runners does GitHub
	// still hold -- upgraded the schema on the way past. That is the wrong
	// moment for it in the ordinary case and the wrong thing entirely when the
	// file is a backup somebody is verifying, which must come back exactly as
	// it was written.
	ReadOnly bool
}

// Open opens (creating if necessary) the database at path and applies all
// pending migrations.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("store: database path is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	memory := opts.Path == ":memory:" || strings.HasPrefix(opts.Path, "file::memory:")
	if opts.ReadOnly && memory {
		return nil, errors.New("store: a read-only in-memory database has nothing in it to read")
	}
	var dsn string
	if opts.ReadOnly {
		abs, err := filepath.Abs(opts.Path)
		if err != nil {
			return nil, fmt.Errorf("store: resolving %q: %w", opts.Path, err)
		}
		// No journal_mode and no synchronous: both are writes to the file, and
		// setting them is how a "read-only" open silently touches the copy it
		// was asked to leave alone.
		dsn = "file:" + abs + "?" + url.Values{
			"mode":    []string{"ro"},
			"_pragma": []string{"busy_timeout(10000)", "foreign_keys(1)"},
		}.Encode()
	} else if !memory {
		abs, err := filepath.Abs(opts.Path)
		if err != nil {
			return nil, fmt.Errorf("store: resolving %q: %w", opts.Path, err)
		}
		// "Creating if necessary" has to include the directory. SQLite will not
		// make one, and what it reports when the directory is missing or not
		// writable is a bare "unable to open database file (14)" that says
		// nothing about which of the two it was.
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return nil, fmt.Errorf("store: creating %s: %w", filepath.Dir(abs), err)
		}
		// _txlock=immediate makes write transactions take the write lock up
		// front, which turns a would-be mid-transaction "database is locked"
		// into a clean, retryable start-of-transaction wait. Both pools share
		// this DSN, so a transaction begun on the read pool takes the write lock
		// too: reads are single statements, and must stay so.
		//
		// journal_size_limit is what gives the write-ahead log's space back. A
		// reader holding an old snapshot stops checkpoints reaching the end of
		// the log, so it grows while the reader lasts -- that is SQLite working
		// as designed -- but without a limit the file then stays at its
		// high-water mark for the life of the process: seventy-five megabytes
		// after one long read, in the test that pins this, on the disk the
		// runners' images and caches share. With the limit, the next checkpoint
		// that resets the log truncates it back. Sixteen megabytes is four times
		// the log a thousand-page automatic checkpoint leaves in steady state.
		dsn = "file:" + abs + "?" + url.Values{
			"_pragma": []string{
				"journal_mode(WAL)",
				"busy_timeout(10000)",
				"foreign_keys(1)",
				"synchronous(NORMAL)",
				"journal_size_limit(" + strconv.Itoa(walSizeLimit) + ")",
			},
			"_txlock": []string{"immediate"},
		}.Encode()
	} else {
		// An in-memory database is per-connection unless it is shared, and the
		// read/write split below needs both handles to see the same data.
		dsn = "file:zoomies-test-" + NewSecret(6) + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	}

	write, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: opening database: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)

	read, err := sql.Open("sqlite", dsn)
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("store: opening database for reads: %w", err)
	}
	if memory {
		// A shared in-memory database vanishes when the last connection closes,
		// so keep the reader pool pinned open too.
		read.SetMaxOpenConns(1)
		read.SetMaxIdleConns(1)
		read.SetConnMaxLifetime(0)
	} else {
		read.SetMaxOpenConns(8)
		read.SetMaxIdleConns(4)
	}

	if err := write.PingContext(ctx); err != nil {
		read.Close()
		write.Close()
		switch {
		case opts.ReadOnly:
			// A read-only open is not asking for a writable directory, so the
			// hint below would send the reader to check the wrong thing. What
			// it is usually looking at is a file that is not a database, or is
			// one that has been damaged -- which is precisely the case
			// `zoomies restore` opens a backup to find out about.
			return nil, fmt.Errorf("store: reading %s (is it a Zoomies database, and is it intact?): %w", opts.Path, err)
		case !memory:
			// The overwhelmingly common cause is a directory the running user
			// cannot write to -- a container volume owned by root, say. SQLite
			// will not say so, so say it here.
			return nil, fmt.Errorf("store: connecting to %s (is the directory writable by the user running zoomies?): %w", opts.Path, err)
		}
		return nil, fmt.Errorf("store: connecting to %s: %w", opts.Path, err)
	}

	s := &Store{read: read, write: write, path: opts.Path, now: opts.Now, readOnly: opts.ReadOnly}
	if opts.ReadOnly {
		// Not migrating is the point, so the ledger check that migrate would
		// have made is made here instead: a caller reading a database from a
		// newer build is reading rows whose meaning it does not know.
		if err := s.checkLedger(ctx); err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	}
	if memory {
		if restored, err := s.restoreMemoryTemplate(ctx); err != nil {
			s.Close()
			return nil, err
		} else if restored {
			return s, nil
		}
	}
	// Before migrating, not during: migrate holds the write lock and so does
	// Backup, and unlocking around the copy to get past that would be a
	// deadlock waiting for the first caller who does not read the comment.
	if err := s.backupIfMigrationsPending(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if memory {
		s.captureMemoryTemplate(ctx)
	}
	return s, nil
}

// Close releases both connection pools.
func (s *Store) Close() error {
	var errs []error
	if s.read != nil {
		errs = append(errs, s.read.Close())
	}
	if s.write != nil {
		errs = append(errs, s.write.Close())
	}
	return errors.Join(errs...)
}

// Path returns the database file this store was opened from.
func (s *Store) Path() string { return s.path }

// Now returns the store's clock. Everything that stamps a timestamp uses this
// so tests can freeze time.
func (s *Store) Now() time.Time { return s.now().UTC() }

// ---------------------------------------------------------------------------
// Migrations
// ---------------------------------------------------------------------------

type migration struct {
	name string
	sql  string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// checkLedger refuses a database written by a newer build.
//
// A missing ledger table is not a newer database, it is an empty one, so it
// passes: Open creates the table a moment later.
func (s *Store) checkLedger(ctx context.Context) error {
	rows, err := s.read.QueryContext(ctx, `SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		// The table does not exist yet on a database this build is about to
		// create, and a real failure to read it will be reported by the next
		// query rather than swallowed: nothing here depends on having read it.
		return nil
	}
	defer rows.Close()
	var applied []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return fmt.Errorf("store: reading migration ledger: %w", err)
		}
		applied = append(applied, n)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: reading migration ledger: %w", err)
	}
	return unknownMigrations(applied)
}

// unknownMigrations names the applied migrations this build does not embed.
func unknownMigrations(applied []string) error {
	migs, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("store: loading embedded migrations: %w", err)
	}
	known := make(map[string]bool, len(migs))
	for _, m := range migs {
		known[m.name] = true
	}
	var unknown []string
	for _, name := range applied {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("store: %w: its ledger names %s, which this build does not have; "+
		"run the release that wrote it, or restore a backup taken with this one",
		ErrSchemaNewer, strings.Join(unknown, ", "))
}

// backupIfMigrationsPending copies the database before any migration touches
// it, when there is both something pending and something to lose.
//
// Migrations are one-way and the binary that wrote this database will refuse
// to open it afterwards, so this is the one moment a rollback is most likely
// to be wanted and least likely to have been prepared for: an operator
// upgrading is not thinking about backups. It costs a file copy once per
// release.
//
// A database being created has no ledger to read, which is how "nothing to
// lose" is recognised: a fresh install gets no empty copy beside it.
func (s *Store) backupIfMigrationsPending(ctx context.Context) error {
	if s.readOnly || s.path == "" {
		return nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		// No ledger: this database is being created.
		return nil
	}
	applied := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return fmt.Errorf("store: reading migration ledger: %w", err)
		}
		applied[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: reading migration ledger: %w", err)
	}
	if len(applied) == 0 {
		return nil
	}

	migs, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("store: loading embedded migrations: %w", err)
	}
	var pending []string
	for _, m := range migs {
		if !applied[m.name] {
			pending = append(pending, m.name)
		}
	}
	dest, err := s.backupBeforeMigrate(ctx, pending)
	if err != nil {
		return fmt.Errorf("store: copying the database before migrating it: %w "+
			"(the upgrade has not started; free space beside the database, or move the old copies out of the way)", err)
	}
	if dest != "" {
		slog.Info("copied the database before migrating it",
			"copy", dest, "migrations", len(pending),
			"detail", "migrations are one-way; this is what an older release could be restored onto")
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	defer s.lockWriter()()

	if _, err := s.write.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: creating migration ledger: %w", err)
	}

	applied := map[string]bool{}
	rows, err := s.write.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: reading migration ledger: %w", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		applied[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Before applying anything: a database that has been further than this
	// build can go is not one to migrate forward.
	names := make([]string, 0, len(applied))
	for n := range applied {
		names = append(names, n)
	}
	sort.Strings(names)
	if err := unknownMigrations(names); err != nil {
		return err
	}

	migs, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("store: loading embedded migrations: %w", err)
	}

	return s.applyMigrations(ctx, migs, applied)
}

// applyMigrations runs the ones not already in the ledger, each in its own
// transaction.
//
// It is separate from migrate so a test can hand it a migration that fails:
// what happens then is the behaviour that matters, and it cannot be exercised
// through the embedded set, every member of which works. The rule it holds is
// that a failure stops startup and leaves the ledger clean, so the same
// migration is tried again on the next start rather than being skipped as
// though it had worked.
func (s *Store) applyMigrations(ctx context.Context, migs []migration, applied map[string]bool) error {
	for _, m := range migs {
		if applied[m.name] {
			continue
		}
		tx, err := s.write.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: applying migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			m.name, ms(s.Now())); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: recording migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: committing migration %s: %w", m.name, err)
		}
		slog.Info("applied database migration", "migration", m.name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Small helpers shared by every query file
// ---------------------------------------------------------------------------

// exec runs a write statement under the single-writer lock.
// AppliedMigration is one row of the ledger: which migration, and when this
// database took it.
type AppliedMigration struct {
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"applied_at"`
}

// AppliedMigrations lists the ledger in the order the migrations apply, which
// is the lexical order of their file names. It is what a readiness probe, a
// bug report or a backup manifest means by "schema version": there is no
// number, only the names, and the last one is the one that matters.
func (s *Store) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT name, applied_at FROM schema_migrations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: reading migration ledger: %w", err)
	}
	defer rows.Close()
	var out []AppliedMigration
	for rows.Next() {
		var m AppliedMigration
		var applied int64
		if err := rows.Scan(&m.Name, &applied); err != nil {
			return nil, fmt.Errorf("store: reading migration ledger: %w", err)
		}
		m.AppliedAt = at(applied)
		out = append(out, m)
	}
	return out, rows.Err()
}

// WriteObserver is told, for every write, how long it waited for the single
// writer and how long it then held it.
type WriteObserver func(waited, held time.Duration)

// ObserveWrites reports every write's wait and hold to fn.
//
// One writer is the design: it is what keeps "database is locked" out of the
// codebase. It is also the one place every heartbeat, webhook, scheduling pass
// and API call queues, so it is where a busy instance slows down first -- and
// until this, nothing said how long the queue was. A heartbeat that took four
// seconds and a webhook GitHub gave up on after ten looked like a slow host
// and a lost delivery, not like writes waiting their turn.
//
// fn runs after the writer is released, so an observer that is slow to record
// cannot lengthen the hold it is measuring.
func (s *Store) ObserveWrites(fn WriteObserver) {
	if fn == nil {
		s.writes.Store(nil)
		return
	}
	s.writes.Store(&fn)
}

// lockWriter takes the single writer and returns what releases it, timing
// both halves for ObserveWrites. Every write goes through here, so a lock
// taken anywhere else is a write the measurement cannot see.
func (s *Store) lockWriter() (release func()) {
	asked := time.Now()
	s.wmu.Lock()
	got := time.Now()
	return func() {
		s.wmu.Unlock()
		if fn := s.writes.Load(); fn != nil {
			(*fn)(got.Sub(asked), time.Since(got))
		}
	}
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.readOnly {
		return nil, ErrReadOnly
	}
	defer s.lockWriter()()
	return s.write.ExecContext(ctx, query, args...)
}

// tx runs fn inside a write transaction under the single-writer lock.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.readOnly {
		return ErrReadOnly
	}
	defer s.lockWriter()()
	t, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}

// ms converts a time to the Unix-millisecond integers the schema stores.
func ms(t time.Time) int64 { return t.UTC().UnixMilli() }

// msp converts an optional time.
func msp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().UnixMilli()
}

// at converts stored milliseconds back to a time.
func at(v int64) time.Time { return time.UnixMilli(v).UTC() }

// atp converts an optional stored timestamp.
func atp(v sql.NullInt64) *time.Time {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// isUnique reports whether err is a SQLite uniqueness violation.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// wrapWrite converts driver-level constraint errors into the package's sentinels.
func wrapWrite(err error) error {
	if isUnique(err) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

// boolInt renders a bool for a SQLite INTEGER column.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// KnownMigrations names every migration this build carries, in the order they
// apply. It is what lets a backup's manifest be compared with this binary
// without opening the backup: a ledger naming a migration not in this list was
// written by a newer release, and Open would refuse it.
func KnownMigrations() []string {
	migs, err := loadMigrations()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(migs))
	for _, m := range migs {
		out = append(out, m.name)
	}
	return out
}
