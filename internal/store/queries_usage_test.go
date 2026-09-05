package store

import (
	"context"
	"testing"
	"time"
)

var usageEpoch = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// seedUsageJob writes one job whose lifecycle timestamps are offsets in minutes
// from usageEpoch. A nil offset leaves that timestamp unset, which is how a job
// that never started or never finished is expressed.
func seedUsageJob(t *testing.T, s *Store, poolID, repo string, queued int, started, completed *int) {
	t.Helper()
	at := func(m int) time.Time { return usageEpoch.Add(time.Duration(m) * time.Minute) }
	j := &Job{
		GitHubJobID: int64(len(repo)*1000 + queued),
		Repo:        repo,
		Workflow:    "build",
		State:       JobQueued,
		PoolID:      poolID,
		QueuedAt:    at(queued),
	}
	if started != nil {
		v := at(*started)
		j.StartedAt, j.State = &v, JobInProgress
	}
	if completed != nil {
		v := at(*completed)
		j.CompletedAt, j.State = &v, JobCompleted
	}
	if _, err := s.UpsertJob(context.Background(), j); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
}

func usageAt(m int) time.Time { return usageEpoch.Add(time.Duration(m) * time.Minute) }

func mins(m int) *int { return &m }

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
	t.Fatalf("Usage returned no row for %q; got %+v", key, rows)
	return UsageRow{}
}

// The average is the mean of the jobs that actually started. Dividing one
// observed 60s wait by ten jobs, nine of which are still queued, reported six
// seconds during exactly the incident an operator is looking at the page for.
func TestAverageQueueWaitCountsOnlyJobsThatStarted(t *testing.T) {
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)
	seedUsageJob(t, s, pool.ID, "acme/one", 0, mins(1), mins(5))
	for i := 1; i < 10; i++ {
		seedUsageJob(t, s, pool.ID, "acme/one", i, nil, nil)
	}
	row := usageRow(t, s, usageAt(-10), usageAt(60), UsageByPool, pool.ID)
	if row.AverageQueueWaitSeconds == nil {
		t.Fatal("average queue wait is nil, but one job started")
	}
	if got := *row.AverageQueueWaitSeconds; got != 60 {
		t.Fatalf("average queue wait = %vs, want 60s over the one job with an observed wait", got)
	}
	if row.Jobs != 10 || row.JobsStarted != 1 {
		t.Fatalf("jobs = %d, started = %d, want 10 and 1", row.Jobs, row.JobsStarted)
	}
}

// An interval in which nothing got off the queue has no average to report, and
// saying so is the difference between "fast" and "nothing is moving".
func TestAverageQueueWaitIsAbsentWhenNothingStarted(t *testing.T) {
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)
	seedUsageJob(t, s, pool.ID, "acme/one", 0, nil, nil)
	row := usageRow(t, s, usageAt(-10), usageAt(60), UsageByPool, pool.ID)
	if row.AverageQueueWaitSeconds != nil {
		t.Fatalf("average queue wait = %v, want nil when no job started", *row.AverageQueueWaitSeconds)
	}
}

// Counting a job once per lifecycle event, rather than once per interval it
// spans, is what lets an operator add two months together.
func TestJobCountsAreAdditiveAcrossAdjacentIntervals(t *testing.T) {
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)
	// Queued in the first hour, started and finished in the second.
	seedUsageJob(t, s, pool.ID, "acme/one", 10, mins(70), mins(80))
	// Wholly inside the first hour.
	seedUsageJob(t, s, pool.ID, "acme/one", 20, mins(25), mins(30))

	first := usageRow(t, s, usageAt(0), usageAt(60), UsageByPool, pool.ID)
	second := usageRow(t, s, usageAt(60), usageAt(120), UsageByPool, pool.ID)
	whole := usageRow(t, s, usageAt(0), usageAt(120), UsageByPool, pool.ID)

	if first.Jobs+second.Jobs != whole.Jobs || whole.Jobs != 2 {
		t.Fatalf("queued counts %d + %d != %d", first.Jobs, second.Jobs, whole.Jobs)
	}
	if first.JobsStarted+second.JobsStarted != whole.JobsStarted || whole.JobsStarted != 2 {
		t.Fatalf("started counts %d + %d != %d", first.JobsStarted, second.JobsStarted, whole.JobsStarted)
	}
	if first.JobsCompleted+second.JobsCompleted != whole.JobsCompleted || whole.JobsCompleted != 2 {
		t.Fatalf("completed counts %d + %d != %d", first.JobsCompleted, second.JobsCompleted, whole.JobsCompleted)
	}
	if first.Jobs != 2 || first.JobsCompleted != 1 {
		t.Fatalf("first hour = %d queued, %d completed, want 2 and 1", first.Jobs, first.JobsCompleted)
	}
}

// A job that spans a whole interval without a lifecycle event in it still
// contributes the time it spent running, which is what the interval is about.
func TestExecutionSecondsAreClippedToTheInterval(t *testing.T) {
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)
	seedUsageJob(t, s, pool.ID, "acme/one", 0, mins(5), mins(125))
	row := usageRow(t, s, usageAt(60), usageAt(120), UsageByPool, pool.ID)
	if row.JobExecutionSeconds != 3600 {
		t.Fatalf("execution seconds = %v, want the full hour it ran inside the window", row.JobExecutionSeconds)
	}
	if row.Jobs != 0 || row.JobsStarted != 0 || row.JobsCompleted != 0 {
		t.Fatalf("a job with no lifecycle event in the window was counted: %+v", row)
	}
}

// Runner-hours belong to the pool that kept the runner alive; pretending a
// repository owns a share of an idle runner would be an invented number.
func TestAllocationIsAbsentForGroupingsThatCannotAttributeIt(t *testing.T) {
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "runner-1", State: RunnerIdle}
	if err := s.CreateRunner(context.Background(), r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	seedUsageJob(t, s, pool.ID, "acme/one", 0, mins(1), mins(5))

	byPool := usageRow(t, s, usageAt(-10), usageAt(60), UsageByPool, pool.ID)
	if byPool.AllocatedRunnerSeconds == nil {
		t.Fatal("allocated runner seconds is nil for a pool, which can attribute it")
	}
	byRepo := usageRow(t, s, usageAt(-10), usageAt(60), UsageByRepository, "acme/one")
	if byRepo.AllocatedRunnerSeconds != nil {
		t.Fatalf("allocated runner seconds = %v by repository, want nil", *byRepo.AllocatedRunnerSeconds)
	}
	if !UsageAllocationAttributable(UsageByInstallation) || UsageAllocationAttributable(UsageByWorkflow) {
		t.Fatal("UsageAllocationAttributable disagrees with what Usage actually fills in")
	}
}

// A job GitHub ran on its own hosted runners has no pool, and the installation
// grouping reaches the installation through the pool. That NULL used to fail
// the whole query, so a window with a single hosted-runner job in it -- which
// is most windows on a fleet still migrating -- answered with an error.
func TestUsageByInstallationTolerantOfJobsWithoutAPool(t *testing.T) {
	s := newTestStore(t)
	seedUsageJob(t, s, "", "acme/widgets", 0, mins(1), mins(2))

	rows, err := s.Usage(context.Background(), usageAt(0), usageAt(10), UsageByInstallation)
	if err != nil {
		t.Fatalf("Usage by installation with a pool-less job: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "" || rows[0].Jobs != 1 {
		t.Fatalf("got %+v, want one row under the empty key counting the job", rows)
	}
}
