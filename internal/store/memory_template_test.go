package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// A test store is built from a template instead of by migrating, so the
// template has to be the migrated database exactly: a missing index or seed
// row would make every test that uses one test a database production never
// has. A file-backed database always migrates for real, so it is the
// reference.
func TestAnInMemoryStoreIsTheMigratedDatabaseExactly(t *testing.T) {
	newTestStore(t) // makes sure a template exists
	replayed := newTestStore(t)
	migrated, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "zoomies.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { migrated.Close() })

	want, got := dumpForComparison(t, migrated), dumpForComparison(t, replayed)
	if !reflect.DeepEqual(want, got) {
		for k := range want {
			if !reflect.DeepEqual(want[k], got[k]) {
				t.Errorf("%s differs:\nmigrated %v\nreplayed %v", k, want[k], got[k])
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("%s exists only in the replayed store", k)
			}
		}
	}
	// Foreign keys are back on once the template is in.
	var on int
	if err := replayed.write.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Fatal("foreign keys are off in a replayed store", err)
	}
}

func dumpForComparison(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	ctx := context.Background()
	out := map[string][]string{}
	rows, err := s.write.QueryContext(ctx, `SELECT type, name, COALESCE(sql,'') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var kind, name, def string
		if err := rows.Scan(&kind, &name, &def); err != nil {
			t.Fatal(err)
		}
		out[kind+" "+name] = []string{def}
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	for _, table := range tables {
		data, err := s.write.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := data.Columns()
		for data.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := data.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			for i, c := range cols {
				// When the migration ran is the one thing that is meant to differ.
				if table == "schema_migrations" && c == "applied_at" {
					values[i] = nil
				}
			}
			out["rows "+table] = append(out["rows "+table], fmt.Sprint(values))
		}
		data.Close()
	}
	return out
}
