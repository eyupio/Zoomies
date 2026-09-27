package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestCleanupRetriesOnlySettleTheirOwnFailure(t *testing.T) {
	for _, hostFirst := range []bool{false, true} {
		name := "GitHub recovers first"
		if hostFirst {
			name = "host recovers first"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			_, pool, host := seedPool(t, s)
			r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "cleanup-test"}
			if err := s.CreateRunner(ctx, r); err != nil {
				t.Fatal(err)
			}
			// A later failure invalidates an earlier completion stamp too.
			if err := s.ClearCleanupFailure(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordCleanupFailure(ctx, r.ID, "container is in use"); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordRegistrationCleanupFailure(ctx, r.ID, "registration deletion refused"); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetRunner(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.CleanupError, "container") || !strings.Contains(got.CleanupError, "registration") || got.CleanedUpAt != nil {
				t.Fatalf("both failures must remain visible: %+v", got)
			}
			// And each is readable on its own, so what is said about the row can
			// name the half that needs doing rather than whichever keyword the
			// joined sentence happens to contain.
			if got.HostCleanupError != "container is in use" || got.RegistrationCleanupError != "registration deletion refused" {
				t.Fatalf("halves = %q / %q", got.HostCleanupError, got.RegistrationCleanupError)
			}
			first, last := s.RecordRegistrationDeleted, s.ClearCleanupFailure
			remaining := "container is in use"
			if hostFirst {
				first, last = last, first
				remaining = "registration deletion refused"
			}
			// Duplicate success must also leave the other failure standing.
			for range 2 {
				if err := first(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			got, err = s.GetRunner(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.CleanupError != remaining || got.CleanupFailedAt == nil || got.CleanedUpAt != nil {
				t.Fatalf("first recovery hid the remaining failure: %+v", got)
			}
			if err := last(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetRunner(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.CleanupError != "" || got.CleanupFailedAt != nil || got.CleanupAttempts != 2 {
				t.Fatalf("both recoveries must clear errors and keep the attempt count: %+v", got)
			}
		})
	}
}

func TestCleanupMigrationPreservesExistingFailureSources(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// The columns before 0022, with a stale completion stamp on a failed
	// cleanup: that is an existing database the migration must repair.
	_, err = db.Exec(`CREATE TABLE runners (id TEXT PRIMARY KEY, cleanup_error TEXT NOT NULL, cleaned_up_at INTEGER);
		INSERT INTO runners VALUES ('host', 'remove_runner failed: daemon unavailable', 123),
		('github', 'the GitHub runner registration could not be deleted: denied', 123),
		('clean', '', 123);`)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrationFS.ReadFile("migrations/0022_cleanup_failure_sources.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"host", "github", "clean"} {
		var combined, host, registration string
		var cleaned sql.NullInt64
		if err := db.QueryRow(`SELECT cleanup_error, host_cleanup_error, registration_cleanup_error, cleaned_up_at FROM runners WHERE id=?`, id).Scan(&combined, &host, &registration, &cleaned); err != nil {
			t.Fatal(err)
		}
		switch id {
		case "host":
			if host != combined || registration != "" || cleaned.Valid {
				t.Fatalf("host failure was not preserved: %q, %q, %v", host, registration, cleaned)
			}
		case "github":
			if registration != combined || host != "" || cleaned.Valid {
				t.Fatalf("registration failure was not preserved: %q, %q, %v", host, registration, cleaned)
			}
		case "clean":
			if host != "" || registration != "" || !cleaned.Valid || cleaned.Int64 != 123 {
				t.Fatal("migration changed a row with no recorded failure")
			}
		}
	}
}

// A host failure that arrives after the host confirmed the removal is about
// an attempt the confirmation overtook, and the two travel on separate
// requests, so the store refuses it as it writes rather than trusting a
// caller's copy of the row that may predate the confirmation.
func TestAHostFailureAfterTheHostConfirmedRemovalIsRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "confirmed", State: RunnerRemoved}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}

	err := s.RecordCleanupFailure(ctx, r.ID, "removal of container confirmed-dind is already in progress")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RecordCleanupFailure on a confirmed removal = %v, want ErrConflict", err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostRemovedAt == nil || got.HostCleanupError != "" || got.CleanupError != "" || got.CleanupAttempts != 0 {
		t.Fatalf("a late failure re-opened a confirmed removal: %+v", got)
	}

	// A runner with no row is still not an error: there is nothing to record.
	if err := s.RecordCleanupFailure(ctx, "run_gone", "x"); err != nil {
		t.Fatalf("RecordCleanupFailure on a missing runner = %v, want nil", err)
	}
}
