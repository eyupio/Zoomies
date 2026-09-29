package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The usage ledger exists so runner-hours outlive the runner row, and a
// runner's session is normally written when its cleanup is confirmed. A host,
// pool or installation that goes first takes the runner rows with it, and a
// runner still live or not yet confirmed clean would then have no session and
// drop out of the usage report for good -- short by exactly the hours an
// operator reconciles against the cloud bill. The delete writes the session as
// the row goes, as PruneRunners does.
func TestDeletingAHostPoolOrInstallationKeepsItsRunnersInTheUsageLedger(t *testing.T) {
	for _, tc := range []struct {
		name string
		del  func(t *testing.T, s *Store, inst *Installation, pool *Pool, host *Host)
	}{
		{"host", func(t *testing.T, s *Store, _ *Installation, _ *Pool, host *Host) {
			if _, err := s.DeleteHost(context.Background(), host.ID); err != nil {
				t.Fatalf("DeleteHost: %v", err)
			}
		}},
		{"pool", func(t *testing.T, s *Store, _ *Installation, pool *Pool, _ *Host) {
			if _, _, err := s.DeletePool(context.Background(), pool.ID); err != nil {
				t.Fatalf("DeletePool: %v", err)
			}
		}},
		{"installation", func(t *testing.T, s *Store, inst *Installation, _ *Pool, _ *Host) {
			if _, err := s.DeleteInstallation(context.Background(), inst.ID); err != nil {
				t.Fatalf("DeleteInstallation: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			day := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
			now := day
			s := newTestStoreAt(t, func() time.Time { return now })
			ctx := context.Background()
			inst, pool, host := seedPool(t, s)
			pricedPool(t, s, pool, 0.36)

			// Removed, cleanup unconfirmed: no session yet.
			unconfirmed := runnerLife(t, s, &now, pool, host, "unconfirmed", day.Add(time.Hour), day.Add(3*time.Hour), 0)
			// Confirmed: its session was written at the time and must stay as it was.
			done := confirmed(t, s, &now, pool, host, "done", day.Add(4*time.Hour), day.Add(5*time.Hour))
			recorded := sessionsFor(t, s, done.ID)
			if len(recorded) != 1 {
				t.Fatalf("a confirmed runner has %d sessions, want 1", len(recorded))
			}
			// Still live when the delete comes.
			now = day.Add(6 * time.Hour)
			live := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "live", State: RunnerIdle}
			if err := s.CreateRunner(ctx, live); err != nil {
				t.Fatal(err)
			}
			now = day.Add(7 * time.Hour)

			tc.del(t, s, inst, pool, host)

			for _, r := range []*Runner{unconfirmed, done, live} {
				if _, err := s.GetRunner(ctx, r.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("runner %s survived the delete: %v", r.Name, err)
				}
			}
			for _, r := range []*Runner{unconfirmed, live} {
				got := sessionsFor(t, s, r.ID)
				if len(got) != 1 {
					t.Fatalf("runner %s has %d sessions after the delete, want 1", r.Name, len(got))
				}
				if got[0].PoolID != pool.ID || got[0].HostID != host.ID || got[0].InstallationID != inst.ID {
					t.Fatalf("the session of %s lost its owners: %+v", r.Name, got[0])
				}
				if got[0].CostPerRunnerHour == nil || *got[0].CostPerRunnerHour != 0.36 {
					t.Fatalf("the session of %s was not priced at the rate when it ran: %v", r.Name, got[0].CostPerRunnerHour)
				}
			}
			if got := sessionsFor(t, s, unconfirmed.ID)[0].FinishedAt; !got.Equal(day.Add(3 * time.Hour)) {
				t.Fatalf("the removed runner's session ends %v, want when it was removed", got)
			}
			if got := sessionsFor(t, s, live.ID)[0].FinishedAt; !got.Equal(now) {
				t.Fatalf("the live runner's session ends %v, want when the delete took it", got)
			}
			if got := sessionsFor(t, s, done.ID); len(got) != 1 || !got[0].RecordedAt.Equal(recorded[0].RecordedAt) {
				t.Fatalf("a session already recorded was rewritten: %+v", got)
			}
			if n := sessionCount(t, s); n != 3 {
				t.Fatalf("%d sessions after the delete, want one per runner", n)
			}
		})
	}
}
