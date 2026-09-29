package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// runnerLife walks a runner through a job to removed on the injected clock:
// created at start, idle a minute later, busy with job, removed at finish.
func runnerLife(t *testing.T, s *Store, now *time.Time, pool *Pool, host *Host, name string, start, finish time.Time, job int64) *Runner {
	t.Helper()
	ctx := context.Background()
	*now = start
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: name}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRegistering, ""); err != nil {
		t.Fatalf("registering: %v", err)
	}
	*now = start.Add(time.Minute)
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerIdle, ""); err != nil {
		t.Fatalf("idle: %v", err)
	}
	if job != 0 {
		queued := start
		if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: job, Repo: "acme/app", Workflow: "build", State: JobInProgress,
			PoolID: pool.ID, RunnerID: r.ID, QueuedAt: queued, StartedAt: now}); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
		if _, err := s.TransitionRunner(ctx, r.ID, RunnerBusy, ""); err != nil {
			t.Fatalf("busy: %v", err)
		}
	}
	*now = finish
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRemoved, ""); err != nil {
		t.Fatalf("removed: %v", err)
	}
	return r
}

func sessionsFor(t *testing.T, s *Store, id string) []*RunnerSession {
	t.Helper()
	all, err := s.RunnerSessions(context.Background(), time.Unix(0, 0), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RunnerSessions: %v", err)
	}
	var out []*RunnerSession
	for _, x := range all {
		if x.RunnerID == id {
			out = append(out, x)
		}
	}
	return out
}

func sessionCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.read.QueryRow(`SELECT COUNT(*) FROM runner_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The session is the ledger's copy of a runner, taken at the moment nothing of
// it is left anywhere. It has to carry the rate the pool charged then: a price
// change made next month must not reprice this month's runner-hours, which are
// what an operator has already reconciled against the cloud bill.
func TestConfirmedCleanupWritesOneSessionWithTheRateAtTheTime(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	inst, pool, host := seedPool(t, s)
	rate := 0.36
	pool.CostPerRunnerHour = &rate
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	start := now
	r := runnerLife(t, s, &now, pool, host, "session-once", start, start.Add(30*time.Minute), 4401)

	if n := len(sessionsFor(t, s, r.ID)); n != 0 {
		t.Fatalf("a removed runner whose cleanup is unconfirmed already has %d sessions", n)
	}
	now = now.Add(time.Minute)
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := len(sessionsFor(t, s, r.ID)); n != 0 {
		t.Fatalf("one side of cleanup wrote %d sessions", n)
	}
	now = now.Add(time.Minute)
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, false); err != nil {
		t.Fatal(err)
	}

	newRate := 9.0
	pool.CostPerRunnerHour = &newRate
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, side := range []bool{true, false} {
		if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, side); err != nil {
			t.Fatal(err)
		}
	}
	// The prune writes sessions too, and must leave this one as it found it.
	saved := now
	now = now.Add(30 * 24 * time.Hour)
	if _, err := s.PruneRunners(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	now = saved

	got := sessionsFor(t, s, r.ID)
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want exactly one", len(got))
	}
	x := got[0]
	if x.PoolID != pool.ID || x.HostID != host.ID || x.InstallationID != inst.ID {
		t.Fatalf("session placed the runner wrongly: %+v", x)
	}
	if x.JobID == "" {
		t.Fatal("session lost the job the runner ran")
	}
	if !x.StartedAt.Equal(start) || !x.FinishedAt.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("session lifetime = %v..%v, want %v..%v", x.StartedAt, x.FinishedAt, start, start.Add(30*time.Minute))
	}
	if x.RegisteredAt == nil || !x.RegisteredAt.Equal(start.Add(time.Minute)) {
		t.Fatalf("registered = %v, want a minute after start", x.RegisteredAt)
	}
	if x.CleanedUpAt == nil || !x.CleanedUpAt.Equal(now) {
		t.Fatalf("cleaned up = %v, want the completing confirmation %v", x.CleanedUpAt, now)
	}
	if x.CostPerRunnerHour == nil || *x.CostPerRunnerHour != rate {
		t.Fatalf("rate = %v, want the %v the pool charged at the time", x.CostPerRunnerHour, rate)
	}
}

// A controller stopped between the two confirmations, and one stopped after
// both whose agent replays its results on reconnect, must still leave the
// ledger with one session. Two would double a runner's hours on every report.
func TestARunnerSessionIsWrittenExactlyOnceAcrossARestartMidCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.db")
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ctx := context.Background()
	open := func() *Store {
		t.Helper()
		s, err := Open(ctx, Options{Path: path, Now: clock})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s
	}

	s := open()
	_, pool, host := seedPool(t, s)
	r := runnerLife(t, s, &now, pool, host, "restart-mid-cleanup", now, now.Add(10*time.Minute), 0)
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open()
	now = now.Add(time.Minute)
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, false); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open()
	defer s.Close()
	now = now.Add(time.Minute)
	for _, side := range []bool{false, true, false} {
		if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, side); err != nil {
			t.Fatal(err)
		}
	}
	// The prune writes sessions for unconfirmed runners; it must not add one
	// for a runner that already has its own.
	now = now.Add(30 * 24 * time.Hour)
	if _, err := s.PruneRunners(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := sessionCount(t, s); n != 1 {
		t.Fatalf("sessions after a restart mid-cleanup and a prune = %d, want 1", n)
	}
	if x := sessionsFor(t, s, r.ID); len(x) != 1 || x[0].CleanedUpAt == nil {
		t.Fatalf("the surviving session is not the confirmed one: %+v", x)
	}
}

// A runner whose cleanup was never confirmed -- a host that went away, a
// registration GitHub would not delete -- still ran, and still cost. Pruning
// its row without a session would make the ledger short by exactly the
// runners an operator is most likely to be asked about.
func TestPruningARunnerWhoseCleanupNeverConfirmedStillRecordsItsSession(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	start := now
	r := runnerLife(t, s, &now, pool, host, "ghost", start, start.Add(20*time.Minute), 0)
	live := runnerLife(t, s, &now, pool, host, "recent", start.Add(9*24*time.Hour), start.Add(9*24*time.Hour+time.Minute), 0)

	now = start.Add(10 * 24 * time.Hour)
	ids, err := s.PruneRunners(ctx, now.Add(-7*24*time.Hour))
	if err != nil || len(ids) != 1 || ids[0] != r.ID {
		t.Fatalf("pruned %v, %v; want just %s", ids, err, r.ID)
	}
	got := sessionsFor(t, s, r.ID)
	if len(got) != 1 {
		t.Fatalf("sessions for the pruned runner = %d, want 1", len(got))
	}
	if got[0].CleanedUpAt != nil || !got[0].FinishedAt.Equal(start.Add(20*time.Minute)) {
		t.Fatalf("session = %+v, want an unconfirmed session ending when the runner did", got[0])
	}
	if n := len(sessionsFor(t, s, live.ID)); n != 0 {
		t.Fatalf("a runner the prune kept got %d sessions", n)
	}
}

// The migration records the runners an existing database has already cleaned
// up, so the ledger begins at the oldest row the upgrade found rather than at
// the upgrade, and it does so without touching the runners table.
func TestTheSessionsMigrationRecordsRunnersAlreadyCleanedUp(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	done := runnerLife(t, s, &now, pool, host, "before-upgrade", now, now.Add(5*time.Minute), 0)
	for _, side := range []bool{true, false} {
		if _, err := s.ConfirmRunnerCleanup(ctx, done.ID, side); err != nil {
			t.Fatal(err)
		}
	}
	pending := runnerLife(t, s, &now, pool, host, "still-cleaning", now, now.Add(5*time.Minute), 0)
	if _, err := s.write.Exec(`DROP TABLE runner_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.Exec(`DELETE FROM schema_migrations WHERE name='0047_runner_sessions.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 0047 recreates the table as it was; the columns 0060 added to it are not
	// re-run, because that migration's own ledger row was never removed.
	for _, col := range []string{"controller_version", "controller_channel", "agent_version"} {
		if _, err := s.write.Exec(`ALTER TABLE runner_sessions ADD COLUMN ` + col + ` TEXT`); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(sessionsFor(t, s, done.ID)); n != 1 {
		t.Fatalf("an already cleaned-up runner has %d sessions after the migration, want 1", n)
	}
	if n := len(sessionsFor(t, s, pending.ID)); n != 0 {
		t.Fatalf("a runner still being cleaned up has %d sessions after the migration, want 0", n)
	}
}

// retention.runner_sessions prunes by when the session ended.
func TestPruneRunnerSessionsDeletesOnlyThoseThatEndedBeforeTheCutoff(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	start := now
	old := runnerLife(t, s, &now, pool, host, "old", start, start.Add(time.Hour), 0)
	recent := runnerLife(t, s, &now, pool, host, "recent", start.Add(48*time.Hour), start.Add(49*time.Hour), 0)
	// Started before the cutoff and finished after it: still inside the window.
	spanning := runnerLife(t, s, &now, pool, host, "spanning", start.Add(23*time.Hour), start.Add(25*time.Hour), 0)
	for _, id := range []string{old.ID, recent.ID, spanning.ID} {
		for _, side := range []bool{true, false} {
			if _, err := s.ConfirmRunnerCleanup(ctx, id, side); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Before the roll-up has absorbed them, the sessions are its only source
	// and nothing may go.
	if n, err := s.PruneRunnerSessions(ctx, start.Add(24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("pruned %d, %v before the roll-up; want 0", n, err)
	}
	if _, err := s.RollUpUsage(ctx, start.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneRunnerSessions(ctx, start.Add(24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	if len(sessionsFor(t, s, old.ID)) != 0 || len(sessionsFor(t, s, recent.ID)) != 1 || len(sessionsFor(t, s, spanning.ID)) != 1 {
		t.Fatal("the prune deleted the wrong session")
	}
}
