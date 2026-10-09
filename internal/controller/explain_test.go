package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// seedRunner puts one runner of a pool on a host, in a state.
func (h *harness) seedRunner(t *testing.T, pool *store.Pool, host *store.Host, state store.RunnerState) *store.Runner {
	t.Helper()
	r := &store.Runner{
		PoolID: pool.ID, HostID: host.ID,
		Name:      "zoomies-" + pool.Name + "-" + store.NewSecret(4),
		State:     store.RunnerProvisioning,
		Ephemeral: true, Labels: pool.Labels,
	}
	if err := h.st.CreateRunner(h.ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	for _, to := range []store.RunnerState{store.RunnerRegistering, store.RunnerIdle, store.RunnerBusy} {
		out, err := h.st.TransitionRunner(h.ctx, r.ID, to, "")
		if err != nil {
			t.Fatalf("TransitionRunner(%s): %v", to, err)
		}
		r = out
		if to == state {
			break
		}
	}
	return r
}

// queuedJob puts one queued job in the store, claimed or not.
func (h *harness) queuedJob(t *testing.T, pool *store.Pool, labels []string) *store.Job {
	t.Helper()
	j := &store.Job{
		GitHubJobID: time.Now().UnixNano(),
		Repo:        "acme/widgets",
		Workflow:    "CI",
		JobName:     "build",
		Labels:      store.NormalizeLabels(labels),
		State:       store.JobQueued,
		QueuedAt:    h.c.Now().Add(-2 * time.Minute),
	}
	if pool != nil {
		j.PoolID, j.Matched = pool.ID, true
	}
	saved, err := h.st.UpsertJob(h.ctx, j)
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	return saved
}

// The question this endpoint exists for has four answers that need different
// advice, and the difference between them is what an operator does next. A
// fleet that is merely busy clears itself; a pool nothing can place never will;
// a job GitHub is holding is not this fleet's at all; and a job no pool claims
// will not run here however long anybody waits.
func TestTheExplanationTellsWaitingApartFromBlocked(t *testing.T) {
	t.Run("a job nothing claims is blocked, not waiting for capacity", func(t *testing.T) {
		h := newHarness(t)
		h.fleet()
		job := h.queuedJob(t, nil, []string{"self-hosted", "cuda12"})

		got, err := h.c.ExplainJob(h.ctx, job.ID, 0)
		if err != nil {
			t.Fatalf("ExplainJob: %v", err)
		}
		if !got.Blocked {
			t.Error("a job no pool claims was not reported as blocked; waiting will never start it")
		}
		if !strings.Contains(got.Summary, "No pool") {
			t.Errorf("summary = %q, want it to say nothing claims the job", got.Summary)
		}
		if !strings.Contains(got.Detail, "cuda12") {
			t.Errorf("the detail does not name the labels that went unanswered: %q", got.Detail)
		}
		if got.Fix == "" {
			t.Error("a blocked job with no fix leaves the operator nowhere to go")
		}
	})

	t.Run("a held job is GitHub's wait, and says so", func(t *testing.T) {
		h := newHarness(t)
		h.fleet()
		// Held from the moment it was first seen, which is how GitHub reports
		// one: the jobs upsert refuses to move a job backwards, so a job
		// cannot be queued and then become held.
		job, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: 9911, Repo: "acme/widgets", Workflow: "Deploy", JobName: "deploy",
			Labels: store.NormalizeLabels([]string{"self-hosted", "linux"}),
			State:  store.JobWaiting, QueuedAt: h.c.Now().Add(-10 * time.Minute),
		})
		if err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}

		got, err := h.c.ExplainJob(h.ctx, job.ID, 0)
		if err != nil {
			t.Fatalf("ExplainJob: %v", err)
		}
		if !got.Waiting {
			t.Error("a held job is waiting on something")
		}
		if got.Blocked {
			t.Error("a held job was called blocked; an approval will start it and nothing here is wrong")
		}
		if !strings.Contains(got.Summary, "deployment review") {
			t.Errorf("summary = %q, want it to name the review", got.Summary)
		}
	})

	t.Run("the scheduler's own words are what a blocked pool says", func(t *testing.T) {
		h := newHarness(t)
		_, pool, _ := h.fleet()
		job := h.queuedJob(t, pool, []string{"self-hosted", "linux"})

		// The plan is the only place that knows a runner cannot be placed:
		// it is a question about hosts, and no count on the pool answers it.
		h.c.mu.Lock()
		h.c.lastPlan = &scheduler.Plan{Pools: []scheduler.PoolPlan{{
			PoolID:     pool.ID,
			PoolName:   pool.Name,
			Blocked:    "no host can take a new docker runner (2 not matching the pool's host selector)",
			BlockedFix: "add a host, uncordon one, or relax the pool's host selector",
		}}}
		h.c.lastPlanAt = h.c.Now()
		h.c.mu.Unlock()

		got, err := h.c.ExplainJob(h.ctx, job.ID, 0)
		if err != nil {
			t.Fatalf("ExplainJob: %v", err)
		}
		if !got.Blocked {
			t.Fatal("a pool the scheduler cannot place was not reported as blocked")
		}
		if !strings.Contains(got.Detail, "not matching the pool's host selector") {
			t.Errorf("the detail is not the scheduler's own sentence: %q", got.Detail)
		}
		if !strings.Contains(got.Fix, "host selector") {
			t.Errorf("the fix is not the scheduler's own: %q", got.Fix)
		}
	})

	t.Run("a job with an idle runner waiting for it is neither", func(t *testing.T) {
		h := newHarness(t)
		_, pool, host := h.fleet()
		h.seedRunner(t, pool, host, store.RunnerIdle)
		job := h.queuedJob(t, pool, []string{"self-hosted", "linux"})

		got, err := h.c.ExplainJob(h.ctx, job.ID, 0)
		if err != nil {
			t.Fatalf("ExplainJob: %v", err)
		}
		if got.Blocked {
			t.Error("a job with an idle runner ready for it was called blocked")
		}
		if !strings.Contains(got.Summary, "idle") {
			t.Errorf("summary = %q, want it to say a runner is idle and waiting", got.Summary)
		}
	})
}

// A job that is running is not usually interesting -- except when the host
// under it has gone quiet, which looks exactly like a healthy long job and is
// the case an operator most wants found for them.
func TestTheExplanationCatchesARunningJobOnASilentHost(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	runner := h.seedRunner(t, pool, host, store.RunnerBusy)

	job := h.queuedJob(t, pool, []string{"self-hosted", "linux"})
	started := h.c.Now().Add(-time.Minute)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: job.GitHubJobID, State: store.JobInProgress, Repo: job.Repo,
		RunnerID: runner.ID, RunnerName: runner.Name, StartedAt: &started,
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	// While the host is heartbeating there is nothing to say beyond where it is.
	got, err := h.c.ExplainJob(h.ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("ExplainJob: %v", err)
	}
	if got.Blocked || !strings.Contains(got.Summary, "running") {
		t.Fatalf("a healthy running job reported %+v", got)
	}

	// Once it has gone quiet the job is in trouble and nothing else says so.
	h.advance(store.HeartbeatTimeout + time.Minute)
	got, err = h.c.ExplainJob(h.ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("ExplainJob: %v", err)
	}
	if !got.Blocked {
		t.Error("a job on a host that has gone quiet was not flagged")
	}
	if !strings.Contains(got.Summary, "gone quiet") {
		t.Errorf("summary = %q, want it to name the silent host", got.Summary)
	}
	if got.HostID != host.ID {
		t.Errorf("host_id = %q, want the host the operator has to go and look at", got.HostID)
	}
}
