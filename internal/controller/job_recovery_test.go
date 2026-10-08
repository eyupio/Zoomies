package controller

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

func TestMissedCompletionIsRecoveredDespiteFreshWebhooks(t *testing.T) {
	for _, conclusion := range []string{"cancelled", "success", "failure"} {
		t.Run(conclusion, func(t *testing.T) {
			h := newHarness(t)
			inst, _, _ := h.fleet()
			q := h.gh.AddQueuedJob("acme/widgets", "CI", "Images", []string{"self-hosted", "linux", "x64", "demo"})
			h.c.pollOnce(h.ctx)
			h.gh.CompleteJob(q.ID, conclusion)
			h.advance(3 * time.Minute)
			if err := h.st.RecordDelivery(h.ctx, &store.WebhookDelivery{
				DeliveryID: "unrelated", Event: "workflow_job", Repo: "acme/widgets", InstallationID: inst.ID,
				Status: "accepted", ReceivedAt: h.c.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			h.c.pollOnce(h.ctx)
			job, err := h.st.GetJobByGitHubID(h.ctx, q.ID)
			if err != nil {
				t.Fatal(err)
			}
			if job.State != store.JobCompleted || job.Conclusion != conclusion {
				t.Fatalf("job = %+v", job)
			}
			events := h.timeline(job.ID)
			last := events[len(events)-1]
			if last.Kind != store.JobEventCompleted || last.Source != sourcePoller {
				t.Fatalf("event = %+v", last)
			}
			h.c.pollOnce(h.ctx)
			if got := len(h.timeline(job.ID)); got != len(events) {
				t.Fatalf("duplicate completion: %d events", got)
			}
			queued, err := h.st.ListQueuedJobs(h.ctx)
			if err != nil || len(queued) != 0 {
				t.Fatalf("cancelled job still creates demand: %+v, %v", queued, err)
			}
		})
	}
}

// Hosted-elsewhere rows still feed the Jobs page. A missing completion must
// not leave them running for days just because they occupy no fleet capacity.
func TestMissedCompletionIsRecoveredForJobsHostedElsewhere(t *testing.T) {
	for _, conclusion := range []string{"success", "failure", "cancelled"} {
		t.Run(conclusion, func(t *testing.T) {
			h := newHarness(t)
			inst, _, _ := h.fleet()
			labels := []string{"blacksmith-2vcpu-ubuntu-2404"}
			q := h.gh.AddQueuedJob("acme/widgets", "CI", "shard (light)", labels)
			runnerName := "blacksmith-2vcpu-ubuntu-2404-Runner-example"
			for _, action := range []string{"queued", "in_progress"} {
				rec := h.deliverJob(jobEvent{
					Action: action, JobID: q.ID, RunID: q.RunID,
					Name: q.JobName, Workflow: q.WorkflowName, Labels: labels,
					RunnerName: runnerName,
				})
				if rec.Code != http.StatusAccepted {
					t.Fatalf("%s webhook: %d (%s)", action, rec.Code, rec.Body.String())
				}
			}
			job := h.polledJob(q.ID)
			if job.State != store.JobInProgress || job.Matched || job.RunnerID != "" {
				t.Fatalf("job = %+v, want it running elsewhere", job)
			}
			h.gh.StartJob(q.ID, runnerName)
			h.gh.CompleteJob(q.ID, conclusion)
			h.advance(6 * 24 * time.Hour)
			h.cfg.GitHub.PollFallback = false
			if err := h.st.RecordDelivery(h.ctx, &store.WebhookDelivery{
				DeliveryID: "unrelated", Event: "workflow_job", Repo: "acme/widgets", InstallationID: inst.ID,
				Status: "accepted", ReceivedAt: h.c.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			h.c.reconcileKnownJobs(h.ctx, h.c.Now())
			job = h.polledJob(q.ID)
			if job.State != store.JobCompleted || job.Conclusion != conclusion || job.CompletedAt == nil {
				t.Fatalf("job = %+v, want GitHub's completion", job)
			}
			events := h.timeline(job.ID)
			if last := events[len(events)-1]; last.Kind != store.JobEventCompleted || last.Source != sourcePoller {
				t.Fatalf("last event = %+v, want the recovered completion", last)
			}
			h.c.reconcileKnownJobs(h.ctx, h.c.Now())
			if got := len(h.timeline(job.ID)); got != len(events) {
				t.Fatalf("duplicate completion: %d events, want %d", got, len(events))
			}
			if got := len(h.runners()); got != 0 {
				t.Fatalf("recovery created %d fleet runners for work hosted elsewhere", got)
			}
		})
	}
}

func TestJobRecoveryDoesNotInventCancellationOnGitHubFailure(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		h := newHarness(t)
		h.fleet()
		q := h.gh.AddQueuedJob("acme/widgets", "CI", "Images", []string{"self-hosted", "linux", "x64", "demo"})
		h.c.pollOnce(h.ctx)
		h.advance(3 * time.Minute)
		h.gh.SetError("/actions/jobs/", status, "lookup failed")
		h.c.pollOnce(h.ctx)
		job, err := h.st.GetJobByGitHubID(h.ctx, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != store.JobQueued {
			t.Fatalf("HTTP %d changed job to %s", status, job.State)
		}
	}
}

func TestRunningContainerNeedsAnOnlineGitHubRunner(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRegistering)
	report := []agent.RunnerReport{{RunnerID: r.ID, Phase: backend.PhaseRunning}}
	if err := h.c.ReportRunners(h.ctx, host.ID, report); err != nil {
		t.Fatal(err)
	}
	got, _ := h.st.GetRunner(h.ctx, r.ID)
	if got.State != store.RunnerRegistering {
		t.Fatalf("unregistered container became %s", got.State)
	}
	h.gh.AddRunner(r.Name, pool.Labels)
	if err := h.c.ReportRunners(h.ctx, host.ID, report); err != nil {
		t.Fatal(err)
	}
	h.c.enrichOnce(h.ctx)
	got, _ = h.st.GetRunner(h.ctx, r.ID)
	if got.State != store.RunnerIdle || got.GitHubRunnerID == 0 {
		t.Fatalf("online runner = %+v", got)
	}
}

func TestJobRecoveryRotatesThroughBoundedPages(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	for i := 0; i < 13; i++ {
		q := h.gh.AddQueuedJob("acme/widgets", "CI", "Images", []string{"self-hosted", "linux", "x64", "demo"})
		h.c.pollOnce(h.ctx)
		_ = q
	}
	h.advance(3 * time.Minute)
	for pass := 0; pass < 2; pass++ {
		before := len(h.gh.Requests())
		h.c.reconcileKnownJobs(h.ctx, h.c.Now())
		n := 0
		for _, req := range h.gh.Requests()[before:] {
			if strings.Contains(req, "/actions/jobs/") {
				n++
			}
		}
		want := 10
		if pass == 1 {
			want = 3
		}
		if n != want {
			t.Fatalf("pass %d made %d checks, want %d", pass, n, want)
		}
	}
}

// Turning the fallback poller off used to turn off the only check that notices
// a completion whose webhook never arrived, leaving the job in progress for
// ever: counted against its repository's scale-up limit, and holding its
// runner's host back from cleanup. The job-recovery loop owns that check
// whatever github.poll_fallback says, so it must still find the completion
// with the poller off -- and the housekeeping pass must not run it a second
// time, because two callers double every GitHub call and race on the shared
// rotation offset.
func TestAMissedCompletionIsStillRecoveredWithThePollerOff(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	q := h.gh.AddQueuedJob("acme/widgets", "CI", "Images", []string{"self-hosted", "linux", "x64", "demo"})
	h.c.pollOnce(h.ctx)
	h.gh.CompleteJob(q.ID, "success")
	h.advance(3 * time.Minute)

	// The pass is run whole, as the loop runs it, with the two jobs that would
	// leave the fake switched off: the image refresh and the release check
	// both have nowhere to go here.
	h.cfg.GitHub.PollFallback = false
	h.cfg.Images.RefreshInterval = 0
	h.cfg.Updates.CheckInterval = 0
	h.c.housekeep(h.ctx, &housekeeping{})
	job, err := h.st.GetJobByGitHubID(h.ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State == store.JobCompleted {
		t.Fatal("the housekeeping loop checked known jobs, which the job-recovery loop already does and would double every GitHub call")
	}

	h.c.reconcileKnownJobs(h.ctx, h.c.Now())
	job, err = h.st.GetJobByGitHubID(h.ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != store.JobCompleted || job.Conclusion != "success" {
		t.Fatalf("job = %+v, want it completed by the job-recovery sweep", job)
	}
}
