package store

import (
	"context"
	"testing"
	"time"
)

// The timeline is written from what an upsert changed, not from what a
// delivery said, because GitHub delivers at least once: a "queued" that arrives
// twice must move the job once and say so once.
func TestApplyJobReportsWhatChangedAndOnlyOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(10 * time.Second)
	done := now.Add(time.Minute)

	_, change, err := s.ApplyJob(ctx, &Job{
		GitHubJobID: 7, Repo: "acme/widgets", State: JobQueued, QueuedAt: now,
		Labels: StringSlice{"self-hosted", "linux"}, PoolID: "pool_a", Matched: true,
	})
	if err != nil {
		t.Fatalf("first queued: %v", err)
	}
	if !change.Created || !change.StateChanged || !change.Claimed || change.RunnerLinked {
		t.Fatalf("first delivery change = %+v, want created, moved and claimed", change)
	}

	_, change, err = s.ApplyJob(ctx, &Job{GitHubJobID: 7, State: JobQueued, PoolID: "pool_a", Matched: true})
	if err != nil {
		t.Fatalf("redelivered queued: %v", err)
	}
	if change.Created || change.StateChanged || change.Claimed || change.RunnerLinked {
		t.Fatalf("a redelivery reported a change: %+v", change)
	}

	_, change, err = s.ApplyJob(ctx, &Job{
		GitHubJobID: 7, State: JobInProgress, StartedAt: &started, RunnerID: "run_x", RunnerName: "zoomies-x",
		Steps: JobSteps{{Number: 1, Name: "Checkout", Status: "in_progress"}},
	})
	if err != nil {
		t.Fatalf("in_progress: %v", err)
	}
	if change.Created || !change.StateChanged || change.PreviousState != JobQueued || !change.RunnerLinked {
		t.Fatalf("in_progress change = %+v, want moved from queued with a runner linked", change)
	}

	got, change, err := s.ApplyJob(ctx, &Job{
		GitHubJobID: 7, State: JobCompleted, Conclusion: "failure", StartedAt: &started, CompletedAt: &done,
		HeadBranch: "main", HeadSHA: "abc123", RunAttempt: 2,
		Steps: JobSteps{
			{Number: 1, Name: "Checkout", Status: "completed", Conclusion: "success"},
			{Number: 2, Name: "Run tests", Status: "completed", Conclusion: "failure"},
			{Number: 3, Name: "Upload", Status: "completed", Conclusion: "skipped"},
		},
	})
	if err != nil {
		t.Fatalf("completed: %v", err)
	}
	if !change.StateChanged || change.PreviousState != JobInProgress || change.RunnerLinked {
		t.Fatalf("completed change = %+v, want moved from in_progress only", change)
	}
	if got.HeadBranch != "main" || got.HeadSHA != "abc123" || got.RunAttempt != 2 {
		t.Fatalf("run context lost: %+v", got)
	}
	if step := got.FailedStep(); step == nil || step.Number != 2 || step.Name != "Run tests" {
		t.Fatalf("FailedStep = %+v, want step 2 'Run tests'", step)
	}

	// A late in_progress redelivery carries the steps as they were mid-run.
	// Letting it through would turn every conclusion back into "in_progress".
	got, change, err = s.ApplyJob(ctx, &Job{
		GitHubJobID: 7, State: JobInProgress,
		Steps: JobSteps{{Number: 1, Name: "Checkout", Status: "in_progress"}},
	})
	if err != nil {
		t.Fatalf("late in_progress: %v", err)
	}
	if change.StateChanged || got.State != JobCompleted {
		t.Fatalf("a stale delivery moved the job: %+v (state %s)", change, got.State)
	}
	if len(got.Steps) != 3 || got.Steps[1].Conclusion != "failure" {
		t.Fatalf("a stale delivery replaced the final steps: %+v", got.Steps)
	}
}

// A queued job that nothing claimed is claimed the moment a pool with its
// labels is created, and the change has to be visible so the timeline can say
// what changed the operator's luck.
func TestApplyJobNoticesAJobBeingClaimedLater(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, change, err := s.ApplyJob(ctx, &Job{GitHubJobID: 8, State: JobQueued, Labels: StringSlice{"gpu"}}); err != nil {
		t.Fatalf("queued: %v", err)
	} else if change.Claimed {
		t.Fatalf("an unmatched job reported itself claimed: %+v", change)
	}
	_, change, err := s.ApplyJob(ctx, &Job{GitHubJobID: 8, State: JobQueued, Labels: StringSlice{"gpu"}, PoolID: "pool_gpu", Matched: true})
	if err != nil {
		t.Fatalf("re-matched: %v", err)
	}
	if !change.Claimed || change.StateChanged {
		t.Fatalf("change = %+v, want claimed without a state change", change)
	}
}

// The first message is the one nearest the event; a second report of the same
// exit must not overwrite it.
func TestSetJobRunnerFaultKeepsTheFirstMessage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j, err := s.UpsertJob(ctx, &Job{GitHubJobID: 9, State: JobInProgress})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	got, recorded, err := s.SetJobRunnerFault(ctx, j.ID, "runner exited with code 137: out of memory")
	if err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}
	if got.RunnerFault != "runner exited with code 137: out of memory" || !recorded {
		t.Fatalf("fault = %q, recorded = %v; want the first report kept and flagged", got.RunnerFault, recorded)
	}
	got, recorded, err = s.SetJobRunnerFault(ctx, j.ID, "the agent could not complete task t1")
	if err != nil {
		t.Fatalf("second SetJobRunnerFault: %v", err)
	}
	if got.RunnerFault != "runner exited with code 137: out of memory" {
		t.Fatalf("a second report replaced the first fault: %q", got.RunnerFault)
	}
	if recorded {
		t.Fatal("a second report was flagged as the one that recorded the fault")
	}
	if _, _, err := s.SetJobRunnerFault(ctx, "job_missing", "x"); err == nil {
		t.Fatal("a fault on a job that does not exist was accepted")
	}
}

// "Failed" on the Jobs page has to mean failed on either side: GitHub's
// conclusion, or a runner that stopped under the job -- including a runner that
// died while GitHub still believes the job is running, which is the case the
// operator most wants to find.
func TestFailedOnlyFindsBothKindsOfFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(time.Second)
	done := now.Add(time.Minute)

	seed := []*Job{
		{GitHubJobID: 1, JobName: "green", State: JobCompleted, Conclusion: "success", QueuedAt: now, StartedAt: &started, CompletedAt: &done},
		{GitHubJobID: 2, JobName: "red", State: JobCompleted, Conclusion: "failure", QueuedAt: now, StartedAt: &started, CompletedAt: &done},
		{GitHubJobID: 3, JobName: "slow", State: JobCompleted, Conclusion: "timed_out", QueuedAt: now, StartedAt: &started, CompletedAt: &done},
		{GitHubJobID: 4, JobName: "orphaned", State: JobInProgress, QueuedAt: now, StartedAt: &started},
		{GitHubJobID: 5, JobName: "cancelled", State: JobCompleted, Conclusion: "cancelled", QueuedAt: now, StartedAt: &started, CompletedAt: &done},
	}
	for _, j := range seed {
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatalf("seeding %s: %v", j.JobName, err)
		}
	}
	orphan, err := s.GetJobByGitHubID(ctx, 4)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if _, _, err := s.SetJobRunnerFault(ctx, orphan.ID, "runner exited with code 137"); err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}

	got, total, err := s.ListJobs(ctx, JobFilter{FailedOnly: true}, Page{Sort: "queued_at"})
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	names := map[string]bool{}
	for _, j := range got {
		names[j.JobName] = true
	}
	if total != 3 || !names["red"] || !names["slow"] || !names["orphaned"] {
		t.Fatalf("failed jobs = %v (total %d), want red, slow and orphaned", names, total)
	}

	got, total, err = s.ListJobs(ctx, JobFilter{FaultedOnly: true}, Page{})
	if err != nil {
		t.Fatalf("ListJobs faulted: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].JobName != "orphaned" {
		t.Fatalf("faulted jobs = %+v (total %d), want only the orphaned one", got, total)
	}
}

// A timeline whose job has been pruned answers nothing, so it goes with it.
func TestJobTimelineIsKeptInOrderAndPrunedWithItsJob(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)
	oldStart := old.Add(time.Second)
	oldDone := old.Add(time.Minute)

	stale, err := s.UpsertJob(ctx, &Job{GitHubJobID: 1, State: JobCompleted, Conclusion: "success", QueuedAt: old, StartedAt: &oldStart, CompletedAt: &oldDone})
	if err != nil {
		t.Fatalf("stale job: %v", err)
	}
	fresh, err := s.UpsertJob(ctx, &Job{GitHubJobID: 2, State: JobQueued, QueuedAt: now})
	if err != nil {
		t.Fatalf("fresh job: %v", err)
	}
	for _, e := range []*JobEvent{
		{JobID: stale.ID, Kind: JobEventQueued, Source: "webhook", Message: "queued", At: old},
		{JobID: stale.ID, Kind: JobEventCompleted, Source: "webhook", Message: "done", At: oldDone},
		{JobID: fresh.ID, Kind: JobEventUnmatched, Source: "poller", Message: "nothing claims it", At: now.Add(time.Second)},
		{JobID: fresh.ID, Kind: JobEventQueued, Source: "poller", Message: "queued", At: now},
	} {
		if err := s.AppendJobEvent(ctx, e); err != nil {
			t.Fatalf("AppendJobEvent: %v", err)
		}
		if e.ID == "" || !HasPrefix(e.ID, PrefixJobEvent) {
			t.Fatalf("event ID %q was not minted with the %s prefix", e.ID, PrefixJobEvent)
		}
	}

	got, err := s.ListJobEvents(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	if len(got) != 2 || got[0].Kind != JobEventQueued || got[1].Kind != JobEventUnmatched {
		t.Fatalf("timeline = %+v, want queued then unmatched, oldest first", got)
	}

	pruned, err := s.PruneJobs(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d jobs, want 1", pruned)
	}
	if left, _ := s.ListJobEvents(ctx, stale.ID); len(left) != 0 {
		t.Fatalf("the pruned job's timeline survived: %+v", left)
	}
	if kept, _ := s.ListJobEvents(ctx, fresh.ID); len(kept) != 2 {
		t.Fatalf("the live job's timeline was pruned too: %+v", kept)
	}
}
