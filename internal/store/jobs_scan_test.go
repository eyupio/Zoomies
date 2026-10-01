package store

import (
	"context"
	"strings"
	"testing"
)

// explain returns the query plan for a statement, joined into one string.
func explain(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.read.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	return strings.Join(plan, "; ")
}

// The problems list asks for the fleet's own failures on every pass, twice. A
// partial index only serves a query whose WHERE it can prove implies its own,
// so rewording fleetFailedJobSQL -- adding a column to the fault test, say --
// would quietly send both reads back to scanning every job. This is the test
// that notices, rather than an operator with a slow Overview.
func TestFaultedJobsAreReadFromTheirOwnIndex(t *testing.T) {
	s := newTestStore(t)
	where, args := jobWhere(JobFilter{FaultedOnly: true})
	for name, query := range map[string]string{
		"the count": `SELECT COUNT(*) FROM jobs ` + where,
		"the page":  `SELECT id FROM jobs ` + where + ` ORDER BY queued_at DESC, id ASC LIMIT 100`,
	} {
		plan := explain(t, s, query, args...)
		if !strings.Contains(plan, "idx_jobs_faulted") {
			t.Errorf("%s is not read from idx_jobs_faulted: %s", name, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s sorts in a temporary b-tree: %s", name, plan)
		}
	}
}

// The default listing order has a tie-break on id. An index that stops at
// queued_at leaves SQLite sorting the ties itself, which is what made a deep
// page cost half a second; the keyset reader needs the same index to seek.
func TestTheJobListingIsReadInIndexOrder(t *testing.T) {
	s := newTestStore(t)
	plan := explain(t, s, `SELECT id FROM jobs ORDER BY queued_at DESC, id ASC LIMIT 50 OFFSET 1000`)
	if !strings.Contains(plan, "idx_jobs_queued_id") || strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("the default job order is not served by idx_jobs_queued_id: %s", plan)
	}
}
