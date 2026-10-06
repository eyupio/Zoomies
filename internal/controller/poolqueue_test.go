package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// queueFleet is a pool that keeps nothing warm and a hundred jobs of which most
// started at once: the shape the median hides. The last few waited the given time.
func queueFleet(t *testing.T, tail time.Duration, slow int) (*harness, *store.Pool) {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.MaxRunners = 8
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.measuredHost("box-1", 8, 16384, 4, enforcesEverything)
	for i := 0; i < 100; i++ {
		wait := 5 * time.Second
		if i < slow {
			wait = tail
		}
		queued := h.c.Now().Add(-time.Hour)
		started, done := queued.Add(wait), queued.Add(wait+3*time.Minute)
		job := &store.Job{
			GitHubJobID: int64(9000 + i), GitHubRunID: 1, Repo: "acme/widgets", Workflow: "ci", JobName: fmt.Sprintf("build-%d", i),
			Labels: store.StringSlice{"self-hosted", "linux", "x64"}, State: store.JobCompleted, Conclusion: "success",
			InstallationID: pool.InstallationID, PoolID: pool.ID, Matched: true, QueuedAt: queued, StartedAt: &started, CompletedAt: &done,
		}
		if _, err := h.st.UpsertJob(h.ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	return h, pool
}

// The slowest twentieth waiting minutes while the median starts at once is the
// cold-start shape, and the cure is a runner already running: so the problem says
// it, and offers a minimum that applies as the pool's own partial update.
func TestAPoolWhoseSlowestJobsWaitMinutesIsOfferedAWarmRunner(t *testing.T) {
	h, pool := queueFleet(t, 12*time.Minute, 10)
	p := h.problemOrNil("pool.queue_wait_high")
	if p == nil || p.TargetID != pool.ID {
		t.Fatalf("problem = %+v; want one for the pool", p)
	}
	if p.Remedy == nil || p.Remedy.Kind != RemedyPoolUpdate || p.Remedy.TargetID != pool.ID {
		t.Fatalf("remedy = %+v; want a pool update", p.Remedy)
	}
	if got := string(p.Remedy.Body); got != `{"min_runners":2}` {
		t.Errorf("the request must change the minimum and nothing else, got %s", got)
	}
	if p.Remedy.Base != "" {
		t.Errorf("a request that replaces none of the pool's resources has no base to compare, or every apply is refused as out of date: %q", p.Remedy.Base)
	}
}

func TestQueueWaitIsNotRaisedWithoutATail(t *testing.T) {
	h, _ := queueFleet(t, 12*time.Minute, 2)
	if p := h.problemOrNil("pool.queue_wait_high"); p != nil {
		t.Errorf("two slow jobs in a hundred are not a pattern: %+v", p)
	}
}

// A pool that already keeps a runner warm has decided how warm it wants to be.
func TestQueueWaitIsNotRaisedForAPoolThatAlreadyKeepsARunnerWarm(t *testing.T) {
	h, pool := queueFleet(t, 12*time.Minute, 10)
	pool.MinRunners = 1
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("pool.queue_wait_high"); p != nil {
		t.Errorf("advised a minimum on a pool that has one: %+v", p)
	}
}
