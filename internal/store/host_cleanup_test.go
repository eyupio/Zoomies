package store

import (
	"context"
	"testing"
	"time"
)

// A runner withdrawn while idle ran no job, so nothing on its row said the
// host still owed a confirmation that it was gone: only runners with a
// finished job or a recorded failure were ever asked about again. Lose the
// agent's one removal report (a restart on either side) and the row waited
// for ever, which is what held an instance transfer at "awaiting cleanup"
// with nothing left on any host. After a grace that outlasts the agent's own
// removal window the host is asked again; a removal it already did is
// answered as done.
func TestAStoppedRunnerNobodyConfirmedGoneIsAskedAboutAgainAfterAGrace(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "pool-a-aaaaaaaa", State: RunnerIdle}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRemoved, "withdrawn while idle"); err != nil {
		t.Fatal(err)
	}

	// Inside the grace the agent may still be keeping the container for its
	// logs, and asking would cut that short.
	pending, err := s.PendingHostCleanup(ctx, "", 10, now.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("asked about a runner still inside the grace: %+v", pending)
	}

	now = now.Add(16 * time.Minute)
	pending, err = s.PendingHostCleanup(ctx, "", 10, now.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != r.ID {
		t.Fatalf("a runner nobody confirmed gone was not asked about: %+v", pending)
	}

	// Once the host has said so, there is nothing to ask.
	if _, err := s.ConfirmRunnerCleanup(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	pending, err = s.PendingHostCleanup(ctx, "", 10, now.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a confirmed removal was asked about again: %+v", pending)
	}
}

// A runner whose job is still in progress is not asked about, whatever its
// age: the controller's row may be behind the host's.
func TestARunnerWhoseJobIsStillRunningIsNotAskedAboutHoweverOldItIs(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "pool-a-aaaaaaaa", State: RunnerBusy}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 1, Repo: "acme/widgets", JobName: "build", State: JobInProgress, RunnerID: r.ID, RunnerName: r.Name}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerFailed, "lost"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingHostCleanup(ctx, "", 10, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a runner with a job still running was asked about: %+v", pending)
	}
}
