package store

import (
	"context"
	"testing"
	"time"
)

// usageFixture is a store with one pool, frozen at a known instant so that
// runner rows, whose creation time comes from the store's clock, land where
// a test expects them.
func usageFixture(t *testing.T, now time.Time) (*Store, *Pool, *Host) {
	t.Helper()
	s, err := Open(context.Background(), Options{Path: ":memory:", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	_, pool, host := seedPool(t, s)
	return s, pool, host
}

func usageJob(t *testing.T, s *Store, pool *Pool, id int64, queued time.Time, started, completed *time.Time) {
	t.Helper()
	state := JobQueued
	if started != nil {
		state = JobInProgress
	}
	if completed != nil {
		state = JobCompleted
	}
	j := &Job{
		GitHubJobID: id, Repo: "acme/api", Workflow: "ci", JobName: "build",
		Labels: pool.Labels, State: state, PoolID: pool.ID, Matched: true,
		QueuedAt: queued, StartedAt: started, CompletedAt: completed,
	}
	if _, err := s.UpsertJob(context.Background(), j); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
}

func usageRow(t *testing.T, s *Store, from, to time.Time, group UsageGroup, key string) UsageRow {
	t.Helper()
	rows, err := s.Usage(context.Background(), from, to, group)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no usage row for %q; got %+v", key, rows)
	return UsageRow{}
}

// A queue that is backing up is exactly when an operator reads this number,
// and counting the jobs still waiting as zero-second waits would make the
// average fall as the incident got worse. The mean is over the jobs that
// actually reached a runner; the waiting ones still count as jobs.
func TestUsageAveragesQueueWaitOverJobsThatReachedARunner(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s, pool, _ := usageFixture(t, now)
	from, to := now.Add(-time.Hour), now

	started := now.Add(-30 * time.Minute)
	completed := started.Add(5 * time.Minute)
	usageJob(t, s, pool, 1, started.Add(-60*time.Second), &started, &completed)
	for i := int64(2); i <= 10; i++ {
		usageJob(t, s, pool, i, now.Add(-10*time.Minute), nil, nil)
	}

	row := usageRow(t, s, from, to, UsageByPool, pool.ID)
	if row.Jobs != 10 {
		t.Errorf("jobs = %d, want 10: the waiting jobs are still jobs", row.Jobs)
	}
	if row.AverageQueueWaitSeconds != 60 {
		t.Errorf("average queue wait = %.1fs, want 60s over the one job that started", row.AverageQueueWaitSeconds)
	}
}

// Report periods are read side by side, so their job counts have to add up.
// A job belongs to the period it was queued in, whole, while the time it spent
// running is split between the periods it ran in.
func TestUsageCountsAJobOnceInThePeriodItWasQueued(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	s, pool, _ := usageFixture(t, now)
	midnight := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	dayBefore, dayAfter := midnight.Add(-24*time.Hour), midnight.Add(24*time.Hour)

	// Queued and started before midnight, finished ten minutes after it.
	started := midnight.Add(-20 * time.Minute)
	completed := midnight.Add(10 * time.Minute)
	usageJob(t, s, pool, 1, started.Add(-15*time.Second), &started, &completed)

	first := usageRow(t, s, dayBefore, midnight, UsageByPool, pool.ID)
	second := usageRow(t, s, midnight, dayAfter, UsageByPool, pool.ID)
	whole := usageRow(t, s, dayBefore, dayAfter, UsageByPool, pool.ID)

	if first.Jobs != 1 || second.Jobs != 0 {
		t.Errorf("jobs = %d then %d, want 1 then 0: a job is counted once, where it was queued", first.Jobs, second.Jobs)
	}
	if first.Jobs+second.Jobs != whole.Jobs {
		t.Errorf("the two days count %d jobs but the whole range counts %d", first.Jobs+second.Jobs, whole.Jobs)
	}
	if first.AverageQueueWaitSeconds != 15 || second.AverageQueueWaitSeconds != 0 {
		t.Errorf("queue wait = %.0fs then %.0fs, want 15s then 0s: the wait goes with the job", first.AverageQueueWaitSeconds, second.AverageQueueWaitSeconds)
	}
	if first.JobExecutionSeconds != 20*60 || second.JobExecutionSeconds != 10*60 {
		t.Errorf("execution = %.0fs then %.0fs, want 1200s then 600s: running time is split at the boundary", first.JobExecutionSeconds, second.JobExecutionSeconds)
	}
	if first.JobExecutionSeconds+second.JobExecutionSeconds != whole.JobExecutionSeconds {
		t.Errorf("the two days sum to %.0fs of execution but the whole range has %.0fs", first.JobExecutionSeconds+second.JobExecutionSeconds, whole.JobExecutionSeconds)
	}
}

// A runner's idle time is the pool's, not any repository's or workflow's, so
// those groupings cannot honestly carry runner-hours. They must leave the
// figure out rather than print a zero that reads as "used nothing"; and a pool
// that really had no runners must still say zero rather than nothing.
func TestUsageOmitsRunnerTimeWhereItCannotBeAttributed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s, pool, host := usageFixture(t, now)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	started := now.Add(-30 * time.Minute)
	completed := started.Add(5 * time.Minute)
	usageJob(t, s, pool, 1, started.Add(-10*time.Second), &started, &completed)

	byRepo := usageRow(t, s, from, to, UsageByRepository, "acme/api")
	if byRepo.AllocatedRunnerSeconds != nil {
		t.Errorf("repository grouping reports %.0f allocated seconds; it should report none", *byRepo.AllocatedRunnerSeconds)
	}
	byWorkflow := usageRow(t, s, from, to, UsageByWorkflow, "ci")
	if byWorkflow.AllocatedRunnerSeconds != nil {
		t.Errorf("workflow grouping reports %.0f allocated seconds; it should report none", *byWorkflow.AllocatedRunnerSeconds)
	}

	byPool := usageRow(t, s, from, to, UsageByPool, pool.ID)
	if byPool.AllocatedRunnerSeconds == nil || *byPool.AllocatedRunnerSeconds != 0 {
		t.Errorf("pool grouping with no runners = %v, want a known zero", byPool.AllocatedRunnerSeconds)
	}

	// A runner created at `now` and still alive counts from then to the end of
	// the range.
	if err := s.CreateRunner(context.Background(), &Runner{PoolID: pool.ID, HostID: host.ID, Name: "zoomies-usage01", Labels: pool.Labels}); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	byPool = usageRow(t, s, from, to, UsageByPool, pool.ID)
	if byPool.AllocatedRunnerSeconds == nil || *byPool.AllocatedRunnerSeconds != 3600 {
		t.Errorf("pool grouping with one live runner = %v, want 3600s", byPool.AllocatedRunnerSeconds)
	}
}
