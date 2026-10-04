package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// memoryTemplate is the migrated schema and seed rows of an in-memory store,
// captured from the first one a process opens and replayed into every one
// after it.
//
// In-memory stores are what tests open -- hundreds of them per package -- and
// applying every migration to each was most of what those tests spent their
// time on: three seconds an Open under the race detector, where replaying the
// result takes a small fraction of that. The first store still runs the real
// migrations, and file-backed databases always do, so the migrations
// themselves are exercised exactly as before; what is skipped is running the
// same ones again to arrive at the same schema.
var memoryTemplate struct {
	mu    sync.Mutex
	stmts []templateStmt
}

type templateStmt struct {
	sql  string
	args []any
}

// restoreMemoryTemplate builds the schema from the captured template, and
// reports whether there was one to build it from.
func (s *Store) restoreMemoryTemplate(ctx context.Context) (bool, error) {
	memoryTemplate.mu.Lock()
	stmts := memoryTemplate.stmts
	memoryTemplate.mu.Unlock()
	if stmts == nil {
		return false, nil
	}
	defer s.lockWriter()()
	conn, err := s.write.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// Rows are copied in table order, not dependency order, and a pragma cannot
	// change inside a transaction.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return false, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.sql, st.args...); err != nil {
			_ = tx.Rollback()
			return false, fmt.Errorf("store: replaying the in-memory schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	return true, err
}

// captureMemoryTemplate records a freshly migrated in-memory store as the
// template, unless one has been recorded already. Failing to capture costs
// only speed, so it is not an error.
func (s *Store) captureMemoryTemplate(ctx context.Context) {
	memoryTemplate.mu.Lock()
	defer memoryTemplate.mu.Unlock()
	if memoryTemplate.stmts != nil {
		return
	}
	stmts, err := s.schemaTemplate(ctx)
	if err == nil {
		memoryTemplate.stmts = stmts
	}
}

// schemaTemplate is the database as statements: tables, then their rows,
// then indexes, triggers and views, so no trigger fires on a copied row.
// sqlite_master's rowid order is creation order, and its text is each
// object's definition after every ALTER, which is the schema migrating to
// head arrives at.
func (s *Store) schemaTemplate(ctx context.Context) ([]templateStmt, error) {
	type object struct{ kind, name, sql string }
	rows, err := s.write.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return nil, err
		}
		objects = append(objects, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var tables, rest []templateStmt
	var data []string
	for _, o := range objects {
		switch {
		// SQLite makes sqlite_sequence itself when an AUTOINCREMENT table is
		// created; its rows are copied with the rest.
		case o.kind == "table" && o.name == "sqlite_sequence":
			data = append(data, o.name)
		case o.kind == "table" && !strings.HasPrefix(o.name, "sqlite_"):
			tables = append(tables, templateStmt{sql: o.sql})
			data = append(data, o.name)
		case o.kind != "table":
			rest = append(rest, templateStmt{sql: o.sql})
		}
	}
	out := tables
	for _, name := range data {
		copied, err := s.tableRows(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, copied...)
	}
	return append(out, rest...), nil
}

func (s *Store) tableRows(ctx context.Context, table string) ([]templateStmt, error) {
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := s.write.QueryContext(ctx, `SELECT * FROM `+quoted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	insert := `INSERT INTO ` + quoted + ` VALUES (` + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `)`
	var out []templateStmt
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, templateStmt{sql: insert, args: values})
	}
	return out, rows.Err()
}
