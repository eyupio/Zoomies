package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// usageJob records a job on a runner in the given state.
func usageJob(t *testing.T, s *Store, n int, name, pool, runner string, state JobState, queued time.Time) *Job {
	t.Helper()
	j := &Job{
		GitHubJobID: int64(5000 + n), Repo: "eyupio/zoomies", Workflow: "CI", JobName: name,
		Labels: StringSlice{"self-hosted"}, State: state, QueuedAt: queued, Matched: true,
		PoolID: pool, RunnerID: runner,
	}
	if state == JobCompleted {
		done := queued.Add(time.Hour)
		j.StartedAt, j.CompletedAt, j.Conclusion = &queued, &done, "success"
	}
	out, _, err := s.ApplyJob(context.Background(), j)
	if err != nil {
		t.Fatalf("recording job %d: %v", n, err)
	}
	return out
}

// Samples arrive on every heartbeat, out of order after a reconnect, and
// before and after the job. Only the job GitHub says is running is charged,
// and a later, smaller sample must not lower what was already seen.
func TestRecordJobUsageRaisesOnlyTheRunningJobsPeaks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	running := usageJob(t, s, 1, "Go (controller)", "pool_a", "run_1", JobInProgress, versionsEpoch)
	done := usageJob(t, s, 2, "Go (controller)", "pool_a", "run_1", JobCompleted, versionsEpoch.Add(-time.Hour))

	for _, sample := range []struct {
		cpus float64
		mb   int64
	}{{1.5, 900}, {3.9, 2800}, {0.2, 100}, {0, 0}} {
		if err := s.RecordJobUsage(ctx, "run_1", sample.cpus, sample.mb); err != nil {
			t.Fatalf("RecordJobUsage: %v", err)
		}
	}
	got, err := s.GetJob(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PeakCPUs != 3.9 || got.PeakMemoryMB != 2800 {
		t.Fatalf("running job's peaks = %v CPU, %d MB, want 3.9 and 2800", got.PeakCPUs, got.PeakMemoryMB)
	}
	other, err := s.GetJob(ctx, done.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.PeakCPUs != 0 || other.PeakMemoryMB != 0 {
		t.Fatalf("a finished job was charged a later sample: %v CPU, %d MB", other.PeakCPUs, other.PeakMemoryMB)
	}
}

// A step killed with exit 137 leaves a runner that finishes the job, so the
// kill is reported after GitHub has closed it. It is recorded all the same,
// once, and a fault the runner reported first keeps its own words.
func TestMarkJobOOMKilledMarksTheRunnersLastJobOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	usageJob(t, s, 1, "older", "pool_a", "run_1", JobCompleted, versionsEpoch.Add(-2*time.Hour))
	last := usageJob(t, s, 2, "Go (controller)", "pool_a", "run_1", JobCompleted, versionsEpoch)

	j, marked, err := s.MarkJobOOMKilled(ctx, "run_1", "runner r1 was killed for memory")
	if err != nil || !marked {
		t.Fatalf("MarkJobOOMKilled = %v, %v; want the job marked", marked, err)
	}
	if j.ID != last.ID || !j.OOMKilled || j.FaultKind != FaultOutOfMemory || j.RunnerFault != "runner r1 was killed for memory" {
		t.Fatalf("marked %+v, want the runner's last job, out of memory", j)
	}
	if _, again, err := s.MarkJobOOMKilled(ctx, "run_1", "a second report"); err != nil || again {
		t.Fatalf("a second report marked it again (%v, %v)", again, err)
	}

	// A fault recorded first keeps its sentence and category.
	lost := usageJob(t, s, 3, "lost", "pool_a", "run_2", JobInProgress, versionsEpoch)
	if _, _, err := s.SetJobRunnerFault(ctx, lost.ID, "runner exited with code 137", FaultOutOfMemory); err != nil {
		t.Fatal(err)
	}
	j, marked, err = s.MarkJobOOMKilled(ctx, "run_2", "ignored")
	if err != nil || !marked || j.RunnerFault != "runner exited with code 137" {
		t.Fatalf("MarkJobOOMKilled = %+v, %v, %v", j, marked, err)
	}

	if j, marked, err := s.MarkJobOOMKilled(ctx, "run_none", "x"); j != nil || marked || err != nil {
		t.Fatalf("a runner with no job marked %+v, %v, %v", j, marked, err)
	}
}

// The history a profile is taken from is the job's own measured runs, per
// pool, newest first, at most JobUsageHistoryLimit of them -- and a run that
// was never measured is not in it at all, because a zero is "unknown", not
// "needed nothing".
func TestJobUsageHistoryIsEachPoolsRecentMeasuredRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	n := 0
	record := func(pool string, mb int64, oom bool, at time.Time) {
		n++
		runner := "run_" + strings.Repeat("x", n)
		usageJob(t, s, n, "Go (controller)", pool, runner, JobInProgress, at)
		if mb > 0 {
			if err := s.RecordJobUsage(ctx, runner, 2, mb); err != nil {
				t.Fatal(err)
			}
		}
		done := at.Add(time.Hour)
		if _, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: int64(5000 + n), State: JobCompleted, Conclusion: "failure",
			Repo: "eyupio/zoomies", Workflow: "CI", JobName: "Go (controller)", StartedAt: &at, CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
		if oom {
			if _, _, err := s.MarkJobOOMKilled(ctx, runner, "killed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range JobUsageHistoryLimit + 5 {
		record("pool_a", int64(1000+i), false, versionsEpoch.Add(time.Duration(i)*time.Hour))
	}
	record("pool_b", 3000, true, versionsEpoch)
	record("pool_b", 0, false, versionsEpoch.Add(time.Hour)) // never measured

	got, err := s.JobUsageHistory(ctx, []JobUsageKey{{Repo: "eyupio/zoomies", Workflow: "CI", JobName: "Go (controller)"}})
	if err != nil {
		t.Fatalf("JobUsageHistory: %v", err)
	}
	a := got[JobUsageKey{Repo: "eyupio/zoomies", Workflow: "CI", JobName: "Go (controller)", PoolID: "pool_a"}]
	if len(a) != JobUsageHistoryLimit || a[0].MemoryMB != int64(1000+JobUsageHistoryLimit+4) {
		t.Fatalf("pool_a history = %d runs, newest %+v; want %d, newest first", len(a), a[0], JobUsageHistoryLimit)
	}
	b := got[JobUsageKey{Repo: "eyupio/zoomies", Workflow: "CI", JobName: "Go (controller)", PoolID: "pool_b"}]
	if len(b) != 1 || !b[0].OOMKilled || b[0].MemoryMB != 3000 {
		t.Fatalf("pool_b history = %+v, want the one measured, killed run", b)
	}
}

// The history is read for every distinct queued job on every pass, so it must
// seek its index rather than walk a jobs table that grows without bound.
func TestJobUsageHistorySeeksItsIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+jobUsageHistorySQL, "r", "w", "j", 10)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if joined := strings.Join(plan, "\n"); !strings.Contains(joined, "idx_jobs_usage_profile") {
		t.Fatalf("the history lookup does not use its index:\n%s", joined)
	}
}

// job_stats carries the heaviest job in each group and how many were killed
// for memory, so "which of our jobs need big hosts" is one call.
func TestJobStatsCarryPeakUsageAndMemoryKills(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	usageJob(t, s, 1, "light", "pool_a", "run_1", JobInProgress, versionsEpoch)
	usageJob(t, s, 2, "heavy", "pool_a", "run_2", JobInProgress, versionsEpoch)
	for _, u := range []struct {
		runner string
		cpus   float64
		mb     int64
	}{{"run_1", 0.5, 400}, {"run_2", 3.5, 5200}} {
		if err := s.RecordJobUsage(ctx, u.runner, u.cpus, u.mb); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 2; n++ {
		done := versionsEpoch.Add(time.Hour)
		if _, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: int64(5000 + n), State: JobCompleted, Conclusion: "failure",
			Repo: "eyupio/zoomies", StartedAt: &versionsEpoch, CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.MarkJobOOMKilled(ctx, "run_2", "killed"); err != nil {
		t.Fatal(err)
	}
	res, err := s.JobStats(ctx, JobFilter{}, []string{GroupByJobName})
	if err != nil {
		t.Fatalf("JobStats: %v", err)
	}
	by := map[string]JobStatsGroup{}
	for _, g := range res.Groups {
		by[g.Keys[GroupByJobName]] = g
	}
	heavy := by["heavy"]
	if heavy.PeakCPUs == nil || *heavy.PeakCPUs != 3.5 || heavy.PeakMemoryMB == nil || *heavy.PeakMemoryMB != 5200 || heavy.OOMKilled != 1 {
		t.Fatalf("heavy = %+v", heavy)
	}
	if light := by["light"]; light.OOMKilled != 0 || light.PeakMemoryMB == nil || *light.PeakMemoryMB != 400 {
		t.Fatalf("light = %+v", light)
	}
	// Both were measured, so the coverage is the whole group.
	if heavy.MeasuredMemory != 1 || by["light"].MeasuredMemory != 1 {
		t.Fatalf("measured = %d and %d, want 1 each", heavy.MeasuredMemory, by["light"].MeasuredMemory)
	}
}

// A job too short to be sampled has no peak, so a group's measured count is
// smaller than its job count: the figure advice rests on is how many were measured.
func TestJobStatsSayHowManyJobsWereMeasured(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	usageJob(t, s, 1, "build", "pool_a", "run_1", JobInProgress, versionsEpoch)
	usageJob(t, s, 2, "build", "pool_a", "run_2", JobInProgress, versionsEpoch)
	if err := s.RecordJobUsage(ctx, "run_1", 1, 900); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		done := versionsEpoch.Add(time.Hour)
		if _, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: int64(5000 + n), State: JobCompleted, Conclusion: "success",
			Repo: "eyupio/zoomies", StartedAt: &versionsEpoch, CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.JobStats(ctx, JobFilter{}, []string{GroupByPool})
	if err != nil || len(res.Groups) != 1 {
		t.Fatalf("JobStats = %+v, %v", res, err)
	}
	if g := res.Groups[0]; g.Count != 2 || g.MeasuredMemory != 1 {
		t.Fatalf("count %d, measured %d; want 2 completed and 1 measured", g.Count, g.MeasuredMemory)
	}
}
