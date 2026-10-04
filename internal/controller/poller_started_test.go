package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

var demoLabels = []string{"self-hosted", "linux", "x64", "demo"}

// startedBetweenSweeps is the race the poller loses by construction: GitHub
// queues a job and an idle runner takes it before any sweep has looked, so the
// first thing this controller can see is a job already in progress.
func (h *harness) startedBetweenSweeps(name string, runner *store.Runner) github.QueuedJob {
	h.t.Helper()
	q := h.gh.AddQueuedJob("acme/widgets", "CI", name, demoLabels)
	h.gh.StartJob(q.ID, runner.Name)
	return q
}

func (h *harness) polledJob(id int64) *store.Job {
	h.t.Helper()
	job, err := h.st.GetJobByGitHubID(h.ctx, id)
	if err != nil {
		h.t.Fatalf("the sweep did not record job %d: %v", id, err)
	}
	return job
}

// A polling-only fleet never sees a job queued when an idle runner takes it
// inside the sweep interval. The runner under it has to go busy all the same,
// or it stands in for the next queued job for the whole build and a pool with
// a free slot leaves that job waiting.
func TestASweepThatFirstSeesAJobRunningTakesItsRunnerBusy(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	running := h.startedBetweenSweeps("build", idle)
	waiting := h.gh.AddQueuedJob("acme/widgets", "CI", "test", demoLabels)

	before := len(h.gh.Requests())
	h.c.pollOnce(h.ctx)

	job := h.polledJob(running.ID)
	if job.State != store.JobInProgress || job.RunnerID != idle.ID || job.StartedAt == nil {
		t.Fatalf("job = %+v, want it in progress on %s with its start recorded", job, idle.ID)
	}
	if job.PoolID != pool.ID || !job.Matched {
		t.Fatalf("job = %+v, want it claimed by %s like any other job of the pool", job, pool.ID)
	}
	runner := h.runnerByID(t, idle.ID)
	if runner.State != store.RunnerBusy || runner.CurrentJobID != job.ID {
		t.Fatalf("runner = %s on job %q, want it busy on %s", runner.State, runner.CurrentJobID, job.ID)
	}
	events := h.timeline(job.ID)
	if last := events[len(events)-1]; last.Kind != store.JobEventStarted || last.Source != sourcePoller || last.RunnerID != idle.ID {
		t.Fatalf("last timeline entry = %+v, want the start, from the poller, on %s", last, idle.ID)
	}
	// It was on the page the sweep had already paid for.
	for _, req := range h.gh.Requests()[before:] {
		if strings.Contains(req, "/actions/jobs/") {
			t.Fatalf("recording a running job cost a lookup of its own: %s", req)
		}
	}

	// And the job still waiting gets a runner of its own, which is the whole
	// point: one busy and one wanted is two, and one is all there is.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(h.runners()); got != 2 {
		t.Fatalf("the fleet has %d runners for one running job and one waiting (%d), want 2", got, waiting.ID)
	}
}

// A sweep sees a running job on every pass until it finishes. Only the first
// may say so: a start restamped every thirty seconds would fill the timeline,
// and a write and a scheduling pass per running job is a cost with no end.
func TestASweepRecordsARunningJobOnce(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	running := h.startedBetweenSweeps("build", idle)

	h.c.pollOnce(h.ctx)
	job := h.polledJob(running.ID)
	events := len(h.timeline(job.ID))
	started := *job.StartedAt
	message := h.runnerByID(t, idle.ID).Message

	h.advance(time.Minute)
	h.c.pollOnce(h.ctx)

	job = h.polledJob(running.ID)
	if got := len(h.timeline(job.ID)); got != events {
		t.Fatalf("a second sweep wrote %d more timeline entries", got-events)
	}
	if !job.StartedAt.Equal(started) {
		t.Fatalf("a second sweep moved the start from %v to %v", started, *job.StartedAt)
	}
	runner := h.runnerByID(t, idle.ID)
	if runner.State != store.RunnerBusy || runner.CurrentJobID != job.ID || runner.Message != message {
		t.Fatalf("runner = %s on job %q (%q), want it left busy on %s", runner.State, runner.CurrentJobID, runner.Message, job.ID)
	}
}

// A listing is a snapshot, and one taken before a job finished can be applied
// after the completion was. It must not bring the job back, and it must not
// take a runner busy on work that is over.
func TestAStaleRunningSnapshotDoesNotReopenAFinishedJob(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	running := h.startedBetweenSweeps("build", idle)
	h.c.pollOnce(h.ctx)

	h.gh.CompleteJob(running.ID, "success")
	h.advance(3 * time.Minute)
	h.c.pollOnce(h.ctx)
	job := h.polledJob(running.ID)
	if job.State != store.JobCompleted {
		t.Fatalf("job = %s, want the known-job check to have completed it", job.State)
	}
	after := h.runnerByID(t, idle.ID)
	events := len(h.timeline(job.ID))

	stale := running
	stale.Status = string(store.JobInProgress)
	stale.RunnerName = idle.Name
	if _, err := h.c.ingestQueuedJobs(h.ctx, inst, []github.QueuedJob{stale}); err != nil {
		t.Fatalf("ingestQueuedJobs: %v", err)
	}

	if got := h.polledJob(running.ID); got.State != store.JobCompleted || got.Conclusion != "success" {
		t.Fatalf("a stale snapshot moved the job back to %s (%q)", got.State, got.Conclusion)
	}
	if got := len(h.timeline(job.ID)); got != events {
		t.Fatalf("a stale snapshot wrote %d timeline entries", got-events)
	}
	if got := h.runnerByID(t, idle.ID); got.State != after.State || got.CurrentJobID != after.CurrentJobID {
		t.Fatalf("a stale snapshot moved the runner from %s to %s", after.State, got.State)
	}
}

// A runner is on one job. A snapshot naming it for a second -- a name reused,
// a listing out of step -- must not take it off the job it has. The refusal is
// the store's (StartRunnerJob keeps the job a busy runner has), so this pins
// that ingestStartedJob leaves it in charge rather than testing a guard of its
// own.
func TestASweepDoesNotMoveABusyRunnerToAnotherJob(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	first := h.startedBetweenSweeps("build", idle)
	h.c.pollOnce(h.ctx)
	held := h.polledJob(first.ID)

	second := h.gh.AddQueuedJob("acme/widgets", "CI", "test", demoLabels)
	second.Status = string(store.JobInProgress)
	second.RunnerName = idle.Name
	if _, err := h.c.ingestQueuedJobs(h.ctx, inst, []github.QueuedJob{second}); err != nil {
		t.Fatalf("ingestQueuedJobs: %v", err)
	}

	if got := h.runnerByID(t, idle.ID); got.State != store.RunnerBusy || got.CurrentJobID != held.ID {
		t.Fatalf("runner = %s on job %q, want it still busy on %s", got.State, got.CurrentJobID, held.ID)
	}
}

// Most running jobs in an installed repository are on somebody else's runners.
// They occupy nothing here, and a row for one would sit in progress for ever:
// nothing reconciles a job this fleet has no hand in.
func TestASweepLeavesAJobRunningElsewhereAlone(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	elsewhere := h.gh.AddQueuedJob("acme/widgets", "CI", "build", []string{"ubuntu-latest"})
	h.gh.StartJob(elsewhere.ID, "GitHub Actions 7")

	h.c.pollOnce(h.ctx)

	if job, err := h.st.GetJobByGitHubID(h.ctx, elsewhere.ID); err == nil {
		t.Fatalf("the sweep recorded a job running on a runner that is not this fleet's: %+v", job)
	}
}

// The other half of the same race: the sweep did see the job queued, and a
// runner took it before the next one. The known-job check gets there too, but
// not until the job is two minutes old and its turn comes round, and until
// then the runner is idle on paper and free to be stopped as one.
func TestASweepStartsAKnownQueuedJobWithoutWaitingForRecovery(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	q := h.gh.AddQueuedJob("acme/widgets", "CI", "build", demoLabels)
	h.c.pollOnce(h.ctx)
	if job := h.polledJob(q.ID); job.State != store.JobQueued {
		t.Fatalf("job = %s, want it queued before anything took it", job.State)
	}

	h.gh.StartJob(q.ID, idle.Name)
	h.c.pollOnce(h.ctx)

	job := h.polledJob(q.ID)
	if job.State != store.JobInProgress || job.RunnerID != idle.ID {
		t.Fatalf("job = %s on %q, want it in progress on %s", job.State, job.RunnerID, idle.ID)
	}
	if got := h.runnerByID(t, idle.ID); got.State != store.RunnerBusy || got.CurrentJobID != job.ID {
		t.Fatalf("runner = %s on job %q, want it busy on %s", got.State, got.CurrentJobID, job.ID)
	}
	queued, err := h.st.ListQueuedJobs(h.ctx)
	if err != nil || len(queued) != 0 {
		t.Fatalf("a started job still creates demand: %+v, %v", queued, err)
	}
}

// A job the sweep found running has to end the way any other does. Its run
// leaves the listing when it finishes, so the completion can only come from
// the known-job check -- which finds the row because the runner link makes it
// this fleet's -- and that has to release the runner and remove its workload.
func TestAJobFoundRunningIsCompletedAndItsRunnerReleased(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	running := h.startedBetweenSweeps("build", idle)
	h.c.pollOnce(h.ctx)

	h.gh.CompleteJob(running.ID, "success")
	h.advance(3 * time.Minute)
	h.c.pollOnce(h.ctx)

	job := h.polledJob(running.ID)
	if job.State != store.JobCompleted || job.Conclusion != "success" {
		t.Fatalf("job = %s (%q), want it completed", job.State, job.Conclusion)
	}
	runner := h.runnerByID(t, idle.ID)
	if runner.State == store.RunnerBusy || runner.State == store.RunnerIdle {
		t.Fatalf("runner = %s, want an ephemeral runner finished with its one job", runner.State)
	}
	if !h.hasTaskOfKind(host.ID, agent.TaskRemoveRunner) {
		t.Fatal("nothing was asked to remove the finished runner's workload")
	}
}

// A snapshot can name a runner this controller has already finished with: the
// job ended and the runner was removed between the listing and the ingest, and
// the job was never recorded as started. The job row is written -- GitHub did
// say it was running -- but the runner must stay where it is, since a removed
// runner brought back to busy would be counted as capacity that is not there,
// and the row has to end like any other once the known-job check reaches it.
func TestASweepDoesNotBringARemovedRunnerBackForAJobItNeverSawStart(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	gone := h.runnerRow(pool, host, store.RunnerRemoved)
	running := h.startedBetweenSweeps("build", gone)

	h.c.pollOnce(h.ctx)

	if got := h.runnerByID(t, gone.ID); got.State != store.RunnerRemoved || got.CurrentJobID != "" {
		t.Fatalf("runner = %s on job %q, want it left removed and unlinked", got.State, got.CurrentJobID)
	}

	h.gh.CompleteJob(running.ID, "success")
	h.advance(3 * time.Minute)
	h.c.pollOnce(h.ctx)

	if job := h.polledJob(running.ID); job.State != store.JobCompleted || job.Conclusion != "success" {
		t.Fatalf("job = %s (%q), want the known-job check to have completed it", job.State, job.Conclusion)
	}
	if got := h.runnerByID(t, gone.ID); got.State != store.RunnerRemoved {
		t.Fatalf("runner = %s after the job completed, want it still removed", got.State)
	}
}
