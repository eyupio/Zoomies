package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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
	got, recorded, err := s.SetJobRunnerFault(ctx, j.ID, "runner exited with code 137: out of memory", FaultRunnerExited)
	if err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}
	if got.RunnerFault != "runner exited with code 137: out of memory" || !recorded {
		t.Fatalf("fault = %q, recorded = %v; want the first report kept and flagged", got.RunnerFault, recorded)
	}
	got, recorded, err = s.SetJobRunnerFault(ctx, j.ID, "the agent could not complete task t1", FaultRunnerExited)
	if err != nil {
		t.Fatalf("second SetJobRunnerFault: %v", err)
	}
	if got.RunnerFault != "runner exited with code 137: out of memory" {
		t.Fatalf("a second report replaced the first fault: %q", got.RunnerFault)
	}
	if recorded {
		t.Fatal("a second report was flagged as the one that recorded the fault")
	}
	if _, _, err := s.SetJobRunnerFault(ctx, "job_missing", "x", FaultRunnerExited); err == nil {
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
	if _, _, err := s.SetJobRunnerFault(ctx, orphan.ID, "runner exited with code 137", FaultRunnerExited); err != nil {
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

// Only the state and the steps used to be gated on the delivery being current.
// A late "queued" after "completed" replaced the labels, the runner's name and
// the run URL with what they were before the job moved on, which is exactly
// the "never backwards" rule the upsert exists for. A stale delivery may fill
// in what the row does not have; it may not change what it does.
func TestAStaleDeliveryFillsGapsButChangesNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	done := now.Add(time.Minute)

	if _, _, err := s.ApplyJob(ctx, &Job{
		GitHubJobID: 9, GitHubRunID: 90, Repo: "acme/widgets", State: JobCompleted, Conclusion: "success",
		Labels: StringSlice{"self-hosted", "linux"}, RunnerName: "zoomies-final", HTMLURL: "https://github.com/acme/widgets/runs/final",
		RunnerID: "run_final", PoolID: "pool_a", QueuedAt: now, CompletedAt: &done,
	}); err != nil {
		t.Fatalf("completed: %v", err)
	}

	earlier := now.Add(-time.Minute)
	got, change, err := s.ApplyJob(ctx, &Job{
		GitHubJobID: 9, State: JobQueued, Labels: StringSlice{"self-hosted", "windows"}, RunnerName: "zoomies-earlier",
		HTMLURL: "https://github.com/acme/widgets/runs/earlier", RunnerID: "run_earlier", PoolID: "pool_b",
		Conclusion: "cancelled", CompletedAt: &earlier, HeadBranch: "main", RunAttempt: 1,
	})
	if err != nil {
		t.Fatalf("stale queued: %v", err)
	}
	if change.StateChanged || got.State != JobCompleted || got.Conclusion != "success" {
		t.Fatalf("a stale delivery moved the job: %+v", got)
	}
	if !slices.Equal(got.Labels, StringSlice{"linux", "self-hosted"}) || got.RunnerName != "zoomies-final" ||
		got.HTMLURL != "https://github.com/acme/widgets/runs/final" || got.RunnerID != "run_final" || got.PoolID != "pool_a" {
		t.Fatalf("a stale delivery changed what the row already knew: %+v", got)
	}
	if !got.CompletedAt.Equal(done) {
		t.Fatalf("completed_at moved to %s on a stale delivery", got.CompletedAt)
	}
	// What the row lacked, it may learn from any delivery.
	if got.HeadBranch != "main" || got.RunAttempt != 1 {
		t.Fatalf("a stale delivery could not fill in the branch and attempt: %+v", got)
	}
}

// A job that never finished used to be kept for ever: PruneJobs deleted only
// completed rows, so a lost completed delivery left a row that outlived the
// history around it. Aged from when it was queued, it goes with the rest.
func TestPruneJobsAlsoDropsAJobThatNeverFinished(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	stuck, err := s.UpsertJob(ctx, &Job{GitHubJobID: 11, State: JobQueued, QueuedAt: now.Add(-72 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendJobEvent(ctx, &JobEvent{JobID: stuck.ID, Kind: JobEventQueued, Source: "webhook", Message: "queued", At: now.Add(-72 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 12, State: JobQueued, QueuedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	pruned, err := s.PruneJobs(ctx, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d jobs, want the one stuck for three days", pruned)
	}
	if _, err := s.GetJob(ctx, stuck.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the stuck job is still there: %v", err)
	}
	if left, _ := s.ListJobEvents(ctx, stuck.ID); len(left) != 0 {
		t.Fatalf("the pruned job's timeline survived: %+v", left)
	}
	if queued, _ := s.ListQueuedJobs(ctx); len(queued) != 1 {
		t.Fatalf("queued jobs after pruning = %d, want the recent one", len(queued))
	}
}

// The Overview's failed tile and the Jobs page's failed filter used to have
// different ideas of what failed: one counted "failure" alone, the other added
// the timeouts and the runner faults, so the tile said 1 while the page it
// linked to showed 3. Both now come from FailedConclusions, as does Job.Failed.
func TestEveryPlaceThatCountsFailedJobsAgrees(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started, done := now.Add(time.Second), now.Add(time.Minute)

	for i, c := range []string{"success", "failure", "timed_out", "startup_failure", "cancelled", "skipped", "action_required", "neutral", "stale"} {
		j := &Job{GitHubJobID: int64(100 + i), JobName: c, State: JobCompleted, Conclusion: c, QueuedAt: now, StartedAt: &started, CompletedAt: &done}
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatalf("seeding %s: %v", c, err)
		}
	}
	// A job GitHub called a success whose runner stopped under it is the
	// fleet's failure even though it is not the workflow's.
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 200, JobName: "faulted", State: JobCompleted, Conclusion: "success", QueuedAt: now, StartedAt: &started, CompletedAt: &done}); err != nil {
		t.Fatalf("seeding faulted: %v", err)
	}
	faulted, err := s.GetJobByGitHubID(ctx, 200)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if _, _, err := s.SetJobRunnerFault(ctx, faulted.ID, "runner exited with code 137", FaultRunnerExited); err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}

	want := map[string]bool{"failure": true, "timed_out": true, "startup_failure": true, "faulted": true}
	listed, total, err := s.ListJobs(ctx, JobFilter{FailedOnly: true}, Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != len(want) {
		t.Fatalf("FailedOnly total = %d, want %d", total, len(want))
	}
	for _, j := range listed {
		if !want[j.JobName] {
			t.Errorf("the filter counted %s as failed", j.JobName)
		}
	}

	stats, err := s.StatsSince(ctx, now.Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if stats.Failed != total {
		t.Fatalf("the Overview counts %d failed jobs, the filter %d", stats.Failed, total)
	}

	all, _, err := s.ListJobs(ctx, JobFilter{}, Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	inGo := 0
	for _, j := range all {
		if j.Failed() != want[j.JobName] {
			t.Errorf("Job.Failed() = %v for %s, want %v", j.Failed(), j.JobName, want[j.JobName])
		}
		if j.Failed() {
			inGo++
		}
	}
	if inGo != total {
		t.Fatalf("Job.Failed says %d, the store says %d", inGo, total)
	}
}

// The failed step says where a job stopped, not whether it failed: a cancelled
// job's completion message names the step the cancel landed in, and a step
// GitHub marks neutral is not a stop.
func TestTheFailedStepIsWhereAJobStoppedWhateverItsConclusion(t *testing.T) {
	cancelled := &Job{State: JobCompleted, Conclusion: "cancelled", Steps: JobSteps{
		{Number: 1, Name: "Checkout", Conclusion: "success"},
		{Number: 2, Name: "Lint", Conclusion: "neutral"},
		{Number: 3, Name: "Build", Conclusion: "cancelled"},
		{Number: 4, Name: "Test", Conclusion: "skipped"},
	}}
	if st := cancelled.FailedStep(); st == nil || st.Number != 3 {
		t.Fatalf("FailedStep = %+v, want step 3, where the cancel landed", st)
	}
	if cancelled.Failed() {
		t.Fatal("a cancelled job counted as failed")
	}
	running := &Job{State: JobInProgress, Steps: JobSteps{{Number: 1, Name: "Checkout", Status: "in_progress"}}}
	if st := running.FailedStep(); st != nil {
		t.Fatalf("a running job has a failed step: %+v", st)
	}
	green := &Job{State: JobCompleted, Conclusion: "success", Steps: JobSteps{{Number: 1, Conclusion: "success"}, {Number: 2, Conclusion: "skipped"}}}
	if st := green.FailedStep(); st != nil {
		t.Fatalf("a green job has a failed step: %+v", st)
	}
}

// TestStatsCanBeNarrowedToTheFleetsOwnJobs is the Overview's side of the
// distinction the Jobs page already makes.
//
// GitHub tells Zoomies about every job in an installed repository, and on an
// organisation that also uses hosted runners most of them are somebody else's.
// A queue depth that counts those answers "why is my fleet slow?" with a number
// nobody here can act on, and a median wait computed from them is a measurement
// of somebody else's queue.
func TestStatsCanBeNarrowedToTheFleetsOwnJobs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started, done := now.Add(time.Second), now.Add(time.Minute)
	slow := now.Add(time.Hour)

	// Somebody else's: GitHub reported it, no pool claimed it, and no runner
	// here touched it. Its wait is an hour, which would drag the percentiles
	// somewhere no operator could act on.
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 900, JobName: "hosted-running",
		State: JobInProgress, QueuedAt: now, StartedAt: &slow}); err != nil {
		t.Fatalf("seeding a hosted running job: %v", err)
	}
	// Three of them, so that they outnumber ours and the unscoped median lands
	// on an hour: with an equal split the two medians coincide by accident and
	// the assertion below would pass without the scoping doing anything.
	for id, name := range map[int64]string{901: "hosted-done", 902: "hosted-done-again"} {
		if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: id, JobName: name,
			State: JobCompleted, Conclusion: "success", QueuedAt: now, StartedAt: &slow, CompletedAt: &done}); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}

	// This fleet's, three ways of being ours.
	ours := []*Job{
		{GitHubJobID: 910, JobName: "claimed", State: JobInProgress, Matched: true,
			QueuedAt: now, StartedAt: &started},
		{GitHubJobID: 911, JobName: "ran-here", State: JobCompleted, Conclusion: "success",
			RunnerID: "run_ours", QueuedAt: now, StartedAt: &started, CompletedAt: &done},
		// Queued and unclaimed. It stays in: nothing ran it, so it is this
		// fleet's problem to see rather than somebody else's job.
		{GitHubJobID: 912, JobName: "unclaimed", State: JobQueued, QueuedAt: now},
	}
	for _, j := range ours {
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatalf("seeding %s: %v", j.JobName, err)
		}
	}

	all, err := s.StatsSince(ctx, now.Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("StatsSince(all): %v", err)
	}
	fleet, err := s.StatsSince(ctx, now.Add(-time.Hour), true)
	if err != nil {
		t.Fatalf("StatsSince(fleet): %v", err)
	}

	if all.Running != 2 || all.Queued != 1 || all.CompletedLast != 3 {
		t.Errorf("unscoped: running=%d queued=%d completed=%d, want 2/1/3",
			all.Running, all.Queued, all.CompletedLast)
	}
	if fleet.Running != 1 {
		t.Errorf("fleet running = %d, want 1: the hosted job is somebody else's", fleet.Running)
	}
	if fleet.CompletedLast != 1 {
		t.Errorf("fleet completed = %d, want 1: the hosted job is somebody else's", fleet.CompletedLast)
	}
	if fleet.Queued != 1 {
		t.Errorf("fleet queued = %d, want 1: an unclaimed queued job is this fleet's to show", fleet.Queued)
	}

	// The percentiles are narrowed too, which is the half a count-only filter
	// would miss: an hour-long hosted wait beside a one-second one here makes
	// the fleet's own median an hour.
	if fleet.MedianWaitMS != time.Second.Milliseconds() {
		t.Errorf("fleet median wait = %dms, want %dms -- somebody else's queue is in the percentile",
			fleet.MedianWaitMS, time.Second.Milliseconds())
	}
	if all.MedianWaitMS <= fleet.MedianWaitMS {
		t.Errorf("unscoped median wait = %dms, fleet = %dms; the fixture is not distinguishing them",
			all.MedianWaitMS, fleet.MedianWaitMS)
	}
}

// A success rate computed from completed and failed alone counts a job GitHub
// stopped reporting as a success. The Overview's stats split the completed
// count four ways instead, and the four have to add up.
func TestStatsSplitCompletedJobsFourWays(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started, done := now.Add(time.Second), now.Add(time.Minute)

	for i, c := range []string{"success", "failure", "timed_out", "startup_failure", "cancelled", "skipped", "action_required", "neutral", "stale", ""} {
		j := &Job{GitHubJobID: int64(300 + i), JobName: "job-" + c, State: JobCompleted, Conclusion: c, QueuedAt: now, StartedAt: &started, CompletedAt: &done}
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatalf("seeding %q: %v", c, err)
		}
	}
	// A success whose runner stopped under it is the fleet's failure, and a
	// cancellation whose runner died is one too: the fault wins.
	for id, c := range map[int64]string{400: "success", 401: "cancelled"} {
		if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: id, JobName: "faulted-" + c, State: JobCompleted, Conclusion: c, QueuedAt: now, StartedAt: &started, CompletedAt: &done}); err != nil {
			t.Fatalf("seeding faulted %s: %v", c, err)
		}
		j, err := s.GetJobByGitHubID(ctx, id)
		if err != nil {
			t.Fatalf("GetJobByGitHubID: %v", err)
		}
		if _, _, err := s.SetJobRunnerFault(ctx, j.ID, "runner exited with code 137", FaultRunnerExited); err != nil {
			t.Fatalf("SetJobRunnerFault: %v", err)
		}
	}

	stats, err := s.StatsSince(ctx, now.Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	want := JobStats{CompletedLast: 12, Succeeded: 1, Failed: 5, Cancelled: 2, Unknown: 4}
	got := JobStats{CompletedLast: stats.CompletedLast, Succeeded: stats.Succeeded, Failed: stats.Failed, Cancelled: stats.Cancelled, Unknown: stats.Unknown}
	if got != want {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
	if stats.Succeeded+stats.Failed+stats.Cancelled+stats.Unknown != stats.CompletedLast {
		t.Fatalf("the four outcomes do not add up to the completed count: %+v", stats)
	}
}

// The upgrade has to attribute the work already on the queue. Until it does,
// every unfinished job is eligible for no pool at all, so a fleet that upgrades
// mid-flight would stop scaling for the jobs it was already running.
func TestTheInstallationBackfillAttributesUnfinishedWorkAndUndoesAWrongMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.db")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	org := &Installation{AppID: 1, InstallationID: 10, Target: "acme", TargetType: TargetOrg}
	repo := &Installation{AppID: 1, InstallationID: 11, Target: "acme/widgets", TargetType: TargetRepo}
	other := &Installation{AppID: 1, InstallationID: 12, Target: "globex", TargetType: TargetOrg}
	for _, i := range []*Installation{org, repo, other} {
		if err := s.CreateInstallation(ctx, i); err != nil {
			t.Fatalf("CreateInstallation: %v", err)
		}
	}
	theirs := &Pool{Name: "theirs", InstallationID: other.ID, Labels: StringSlice{"linux"},
		Backend: BackendDocker, MaxRunners: 2, Ephemeral: true, DockerMode: DockerNone, Enabled: true}
	if err := s.CreatePool(ctx, theirs); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}

	// Wind the column back off the database, so that reopening runs the
	// backfill over rows written before it existed.
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE name = '0012_job_installation.sql'`,
		`ALTER TABLE jobs DROP COLUMN installation_id`,
		`ALTER TABLE webhook_deliveries DROP COLUMN installation_id`,
		// A queued job in the organisation, matched to a pool belonging to
		// another installation: exactly what the old label-only rule did.
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, pool_id, matched, queued_at)
			VALUES ('job_cross', 601, 'acme/gadgets', '["linux"]', 'queued', '%s', 1, %d)`, theirs.ID, now.UnixMilli()),
		// A queued job in a repository its own installation covers, correctly
		// matched to nothing yet.
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, queued_at)
			VALUES ('job_repo', 602, 'acme/widgets', '["linux"]', 'queued', %d)`, now.UnixMilli()),
		// Work already running, and history. The first is attributed; the
		// second is left exactly as it was.
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, started_at, queued_at)
			VALUES ('job_running', 603, 'globex/thing', '["linux"]', 'in_progress', %d, %d)`, now.UnixMilli(), now.UnixMilli()),
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, conclusion, completed_at, queued_at)
			VALUES ('job_done', 604, 'acme/widgets', '["linux"]', 'completed', 'success', %d, %d)`, now.UnixMilli(), now.UnixMilli()),
		// And a repository no installation covers.
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, pool_id, matched, queued_at)
			VALUES ('job_orphan', 605, 'nobody/here', '["linux"]', 'queued', '%s', 1, %d)`, theirs.ID, now.UnixMilli()),
		// A job GitHub is holding for a deployment review: not demand yet, but
		// it becomes demand the moment somebody approves it, so it is
		// attributed with the rest of the unfinished work.
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, queued_at)
			VALUES ('job_waiting', 606, 'globex/thing', '["linux"]', 'waiting', %d)`, now.UnixMilli()),
		// And work already running on the wrong installation's pool, which is
		// the evidence of the bug and is left exactly as it is.
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, pool_id, matched, started_at, queued_at)
			VALUES ('job_ran_there', 607, 'acme/widgets', '["linux"]', 'in_progress', '%s', 1, %d, %d)`,
			theirs.ID, now.UnixMilli(), now.UnixMilli()),
	} {
		if _, err := s.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("winding the schema back: %v: %s", err, stmt)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open after the backfill: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })

	for _, tc := range []struct {
		id   string
		want string
		why  string
	}{
		{"job_cross", org.ID, "an organisation installation covers acme/gadgets"},
		{"job_repo", repo.ID, "a repository installation beats the organisation one"},
		{"job_running", other.ID, "work already running is attributed too"},
		{"job_done", "", "history is left alone"},
		{"job_orphan", "", "no installation covers nobody/here"},
		{"job_waiting", other.ID, "a job held for review is attributed with the rest"},
		{"job_ran_there", repo.ID, "a running job is attributed too"},
	} {
		j, err := migrated.GetJob(ctx, tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		if j.InstallationID != tc.want {
			t.Errorf("%s installation = %q, want %q: %s", tc.id, j.InstallationID, tc.want, tc.why)
		}
	}

	// The cross-installation match is the one the upgrade invalidates: that
	// pool will never run that job, and the match is sticky, so the migration
	// is the only thing that can take it back.
	cross, err := migrated.GetJob(ctx, "job_cross")
	if err != nil {
		t.Fatal(err)
	}
	if cross.Matched || cross.PoolID != "" {
		t.Errorf("job_cross = %+v, want it unclaimed: its pool belongs to another installation", cross)
	}

	// A job nothing covers is unclaimed by the same rule: no pool's
	// installation can equal its empty one, and none of them can run it.
	orphan, err := migrated.GetJob(ctx, "job_orphan")
	if err != nil {
		t.Fatal(err)
	}
	if orphan.Matched || orphan.PoolID != "" {
		t.Errorf("job_orphan = %+v, want it unclaimed: nothing here can run it", orphan)
	}

	// The running job keeps its pool even though that pool is on the wrong
	// installation. Its runner exists, and where the job actually ran is the
	// evidence that this was happening.
	ran, err := migrated.GetJob(ctx, "job_ran_there")
	if err != nil {
		t.Fatal(err)
	}
	if !ran.Matched || ran.PoolID != theirs.ID {
		t.Errorf("job_ran_there = %+v, want its pool kept: it is the record of where the job ran", ran)
	}
}

// Most deliveries carry nothing but a state change: the expiry sweep writes a
// job with four fields set. A merge rule that treated an absent installation as
// "clear it" would unattribute a job every time it moved on.
func TestAStaleDeliveryDoesNotForgetWhichInstallationAJobBelongsTo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := &Job{GitHubJobID: 700, Repo: "acme/widgets", State: JobQueued,
		InstallationID: "ins_acme", QueuedAt: time.Now()}
	if _, err := s.UpsertJob(ctx, first); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	done := time.Now()
	got, err := s.UpsertJob(ctx, &Job{GitHubJobID: 700, State: JobCompleted,
		Conclusion: "success", CompletedAt: &done})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if got.InstallationID != "ins_acme" {
		t.Fatalf("installation = %q, want it kept: the delivery carried none", got.InstallationID)
	}
}

// The poller stands down for an installation whose webhooks are arriving. That
// has to be asked per installation: a fleet where one organisation's deliveries
// flow and another's do not is exactly the case the fallback poller exists for,
// and a fleet-wide answer lets the working half hide the broken one.
func TestDeliveryFreshnessIsCreditedToTheInstallationOwningTheRepository(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	org := &Installation{AppID: 1, InstallationID: 10, Target: "acme", TargetType: TargetOrg}
	repo := &Installation{AppID: 1, InstallationID: 11, Target: "acme/widgets", TargetType: TargetRepo}
	quiet := &Installation{AppID: 1, InstallationID: 12, Target: "globex", TargetType: TargetOrg}
	for _, i := range []*Installation{org, repo, quiet} {
		if err := s.CreateInstallation(ctx, i); err != nil {
			t.Fatalf("CreateInstallation: %v", err)
		}
	}

	base := time.Now().Truncate(time.Millisecond)
	for _, d := range []struct {
		repo, status string
		at           time.Time
	}{
		{"acme/gadgets", "accepted", base.Add(-9 * time.Minute)},
		{"acme/gadgets", "accepted", base.Add(-3 * time.Minute)},
		// A repository installation owns its own repository even though the
		// organisation installation covers it too.
		{"acme/widgets", "accepted", base.Add(-7 * time.Minute)},
		// Rejected: it started no runner, so it is no evidence webhooks work.
		{"globex/thing", "rejected", base.Add(-1 * time.Minute)},
		// No installation covers it, so it is nobody's freshness.
		{"nobody/here", "accepted", base.Add(-2 * time.Minute)},
		// An organisation-wide ping carries the organisation, not a repository.
		{"acme", "accepted", base.Add(-30 * time.Second)},
	} {
		if err := s.RecordDelivery(ctx, &WebhookDelivery{
			DeliveryID: d.repo + d.at.String(), Event: "workflow_job",
			Repo: d.repo, Status: d.status, ReceivedAt: d.at,
		}); err != nil {
			t.Fatalf("RecordDelivery: %v", err)
		}
	}

	// A window wide enough for every delivery above.
	got, err := s.InstallationsFreshSince(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("InstallationsFreshSince: %v", err)
	}
	if !got[org.ID] {
		t.Error("the organisation's own deliveries are arriving, so it is fresh")
	}
	if !got[repo.ID] {
		t.Error("the repository installation owns its repository's delivery, so it is fresh")
	}
	if got[quiet.ID] {
		t.Error("globex has only a rejected delivery, so it must look silent")
	}
	if len(got) != 2 {
		t.Errorf("got %d fresh installations, want 2: %v", len(got), got)
	}

	// And the cutoff is what the caller means by fresh: narrow it past the
	// repository installation's delivery and only the organisation, whose ping
	// arrived thirty seconds ago, is still arriving.
	recent, err := s.InstallationsFreshSince(ctx, base.Add(-time.Minute))
	if err != nil {
		t.Fatalf("InstallationsFreshSince: %v", err)
	}
	if !recent[org.ID] || recent[repo.ID] {
		t.Errorf("inside the last minute = %v, want the organisation alone", recent)
	}
}

// TestAJobIsStampedTheMomentThisFleetCouldFirstActOnIt records the start of the
// scheduling-latency interval the measurement contract defines.
//
// Without it the only figure available runs from queued_at, which charges the
// platform for time it had no say in: a job whose labels no pool claimed yet,
// one GitHub held for a deployment review, one waiting on a scale-up delay the
// operator configured. None of those are the fleet being slow.
func TestAJobIsStampedTheMomentThisFleetCouldFirstActOnIt(t *testing.T) {
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return clock })
	ctx := context.Background()

	// Nothing claims it yet, so there is no moment to record.
	unclaimed, err := s.UpsertJob(ctx, &Job{GitHubJobID: 700, Repo: "acme/widgets",
		JobName: "build", State: JobQueued, QueuedAt: clock})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if unclaimed.EligibleAt != nil {
		t.Fatalf("a job no pool claims is already eligible at %v", unclaimed.EligibleAt)
	}

	// A pool is created, the next poll matches it, and that is the moment.
	clock = clock.Add(90 * time.Second)
	claimed, err := s.UpsertJob(ctx, &Job{GitHubJobID: 700, Repo: "acme/widgets",
		JobName: "build", State: JobQueued, QueuedAt: unclaimed.QueuedAt, Matched: true})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if claimed.EligibleAt == nil {
		t.Fatal("a job a pool claimed carries no eligible time, so its latency cannot be measured")
	}
	if !claimed.EligibleAt.Equal(clock) {
		t.Errorf("eligible at %v, want the moment it was claimed (%v)", claimed.EligibleAt, clock)
	}
	// Not the moment it was queued: the ninety seconds it spent with nothing
	// able to run it are not this fleet's to answer for.
	if claimed.EligibleAt.Equal(claimed.QueuedAt) {
		t.Error("eligibility was stamped at queue time, which is the figure this column exists to replace")
	}

	// A later delivery finds it matched again and must not move the stamp: it
	// is the first moment that matters, not the most recent one saying so.
	clock = clock.Add(30 * time.Second)
	started := clock
	again, err := s.UpsertJob(ctx, &Job{GitHubJobID: 700, Repo: "acme/widgets",
		JobName: "build", State: JobInProgress, QueuedAt: unclaimed.QueuedAt,
		Matched: true, StartedAt: &started})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if !again.EligibleAt.Equal(*claimed.EligibleAt) {
		t.Errorf("eligible at %v after a second delivery, want the first moment (%v)",
			again.EligibleAt, claimed.EligibleAt)
	}
}

// A job already claimed when it is first seen -- the ordinary case, since
// ingest matches before it writes -- is eligible from that moment. There was
// no earlier one to record.
func TestAJobClaimedOnArrivalIsEligibleFromArrival(t *testing.T) {
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return clock })

	j, err := s.UpsertJob(context.Background(), &Job{GitHubJobID: 701, Repo: "acme/widgets",
		JobName: "build", State: JobQueued, QueuedAt: clock, Matched: true})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if j.EligibleAt == nil {
		t.Fatal("a job claimed on arrival carries no eligible time")
	}
	if !j.EligibleAt.Equal(clock) {
		t.Errorf("eligible at %v, want %v", j.EligibleAt, clock)
	}
}

// GitHub owner and repository names are case-insensitive, and a delivery
// carries the canonical case whatever an operator typed into the form. An
// installation saved as "Acme" that matched nothing was a fleet that scaled
// for nobody with every delivery recorded as "no installation covers".
func TestInstallationTargetsMatchWhateverCaseTheyWereTypedIn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	inst := &Installation{AppID: 1, InstallationID: 20, Target: " Acme ", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	if inst.Target != "acme" {
		t.Fatalf("stored target = %q, want it folded to acme", inst.Target)
	}
	for _, repo := range []string{"acme/widgets", "ACME/Widgets", "Acme"} {
		got, err := s.FindInstallationByTarget(ctx, repo)
		if err != nil {
			t.Fatalf("FindInstallationByTarget(%q): %v", repo, err)
		}
		if got.ID != inst.ID {
			t.Fatalf("FindInstallationByTarget(%q) = %s, want %s", repo, got.ID, inst.ID)
		}
	}

	// Rows written before targets were folded are matched too, which is why
	// the comparison folds the column and not only the argument.
	if _, err := s.exec(ctx, `UPDATE installations SET target = 'Acme' WHERE id = ?`, inst.ID); err != nil {
		t.Fatalf("unfolding the stored target: %v", err)
	}
	if got, err := s.FindInstallationByTarget(ctx, "acme/gadgets"); err != nil || got.ID != inst.ID {
		t.Fatalf("an older mixed-case row is not matched: %v, %v", got, err)
	}

	// Delivery freshness is credited by the same comparison.
	if err := s.RecordDelivery(ctx, &WebhookDelivery{DeliveryID: "d1", Event: "workflow_job",
		Repo: "ACME/widgets", Status: "accepted", ReceivedAt: s.Now()}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	fresh, err := s.InstallationsFreshSince(ctx, s.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("InstallationsFreshSince: %v", err)
	}
	if !fresh[inst.ID] {
		t.Fatalf("a delivery in canonical case was not credited to the mixed-case installation: %v", fresh)
	}
}

// Two installations may cover one target -- the unique index is on the App as
// well -- and the one a delivery resolves to has to be the same on every
// call, or a job's installation changes between the webhook and the poller.
func TestTheOlderOfTwoInstallationsOnOneTargetWins(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return clock })
	ctx := context.Background()
	first := &Installation{AppID: 1, InstallationID: 30, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, first); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	clock = clock.Add(time.Minute)
	second := &Installation{AppID: 2, InstallationID: 31, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, second); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	for range 5 {
		got, err := s.FindInstallationByTarget(ctx, "acme/widgets")
		if err != nil {
			t.Fatalf("FindInstallationByTarget: %v", err)
		}
		if got.ID != first.ID {
			t.Fatalf("resolved to %s, want the older installation %s every time", got.ID, first.ID)
		}
	}
}

// A runner's last lines are evidence, not a log: forty lines is enough to
// show what killed it and little enough that every failed job can keep them.
// Longer lines are cut, because one 10 MB line of base64 is not evidence.
func TestAJobKeepsAtMostFortyLinesOfOutput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j, err := s.UpsertJob(ctx, &Job{GitHubJobID: 1, Repo: "acme/widgets", Workflow: "CI", JobName: "build", State: JobInProgress, RunnerID: "run_1", QueuedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprintf("%02d ", i+1) + strings.Repeat("x", 500)
	}
	jobID, err := s.SetJobOutputTail(ctx, "run_1", lines)
	if err != nil || jobID != j.ID {
		t.Fatalf("SetJobOutputTail = %q, %v; want %s", jobID, err, j.ID)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OutputTail) != 40 || !strings.HasPrefix(got.OutputTail[0], "11 ") || !strings.HasPrefix(got.OutputTail[39], "50 ") {
		t.Fatalf("kept %d lines, first %q", len(got.OutputTail), got.OutputTail[0][:3])
	}
	for _, l := range got.OutputTail {
		if n := len([]rune(l)); n != 400 {
			t.Fatalf("a kept line is %d runes, want 400", n)
		}
	}
	if id, err := s.SetJobOutputTail(ctx, "run_nobody", lines); err != nil || id != "" {
		t.Fatalf("a runner with no job: %q, %v; want nothing and no error", id, err)
	}
}
