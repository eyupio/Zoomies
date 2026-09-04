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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary stays static
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned by every Get* method when the row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a uniqueness constraint would be violated.
var ErrConflict = errors.New("already exists")

// ErrInvalidTransition is returned when a runner state change is not legal.
var ErrInvalidTransition = errors.New("invalid state transition")

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
	path  string
	now   func() time.Time
}

// Options configures Open.
type Options struct {
	// Path is the database file. ":memory:" is accepted for tests.
	Path string
	// Now overrides the clock; tests set this for deterministic timestamps.
	Now func() time.Time
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
	var dsn string
	if !memory {
		abs, err := filepath.Abs(opts.Path)
		if err != nil {
			return nil, fmt.Errorf("store: resolving %q: %w", opts.Path, err)
		}
		// _txlock=immediate makes write transactions take the write lock up
		// front, which turns a would-be mid-transaction "database is locked"
		// into a clean, retryable start-of-transaction wait.
		dsn = "file:" + abs + "?" + url.Values{
			"_pragma": []string{
				"journal_mode(WAL)",
				"busy_timeout(10000)",
				"foreign_keys(1)",
				"synchronous(NORMAL)",
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
		return nil, fmt.Errorf("store: connecting to %s: %w", opts.Path, err)
	}

	s := &Store{read: read, write: write, path: opts.Path, now: opts.Now}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
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

func (s *Store) migrate(ctx context.Context) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

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

	migs, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("store: loading embedded migrations: %w", err)
	}
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
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.write.ExecContext(ctx, query, args...)
}

// tx runs fn inside a write transaction under the single-writer lock.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
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
