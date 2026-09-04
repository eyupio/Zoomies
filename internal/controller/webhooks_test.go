package controller

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

// This is the product's core promise: a job is queued on GitHub, and a runner
// appears on a host to run it.
func TestQueuedJobWebhookProducesARunnerAndACreateTask(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()

	rec := h.deliverJob(jobEvent{
		Action: "queued", JobID: 1001, RunID: 5001,
		Name: "build", Workflow: "CI",
		Labels: []string{"self-hosted", "linux", "x64", "demo"},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want %d (%s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	job, err := h.st.GetJobByGitHubID(h.ctx, 1001)
	if err != nil {
		t.Fatalf("the delivery did not produce a job row: %v", err)
	}
	if job.State != store.JobQueued || !job.Matched || job.PoolID != pool.ID {
		t.Fatalf("job = %+v, want queued and matched to pool %s", job, pool.ID)
	}

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	r := h.onlyRunner()
	if r.State != store.RunnerProvisioning {
		t.Fatalf("runner state = %q, want %q", r.State, store.RunnerProvisioning)
	}
	if r.HostID != host.ID || r.PoolID != pool.ID {
		t.Fatalf("runner placed on host %s in pool %s, want %s/%s", r.HostID, r.PoolID, host.ID, pool.ID)
	}
	if !store.IsRunnerName(r.Name) {
		t.Fatalf("runner name %q does not carry the %q prefix", r.Name, store.RunnerNamePrefix)
	}
	if r.GitHubRunnerID == 0 {
		t.Fatal("the runner has no GitHub runner ID, so no JIT config was minted for it")
	}

	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.RunnerID != r.ID {
		t.Fatalf("create task is for runner %s, want %s", task.RunnerID, r.ID)
	}
	if task.Spec == nil || task.Spec.Credentials.JITConfig == "" {
		t.Fatal("the create task carries no JIT configuration, so the agent could not register the runner")
	}
	if task.Spec.Name != r.Name || task.Backend != pool.Backend {
		t.Fatalf("spec = %+v, want name %s on the %s backend", task.Spec, r.Name, pool.Backend)
	}
}

func TestWebhookWithABadSignatureIsRejectedAndRecorded(t *testing.T) {
	h := newHarness(t)
	h.fleet()

	body := jobEvent{
		Action: "queued", JobID: 2002,
		Labels: []string{"self-hosted", "linux", "x64", "demo"},
	}.body()
	rec := h.deliver("workflow_job", body, "the-wrong-secret")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if _, err := h.st.GetJobByGitHubID(h.ctx, 2002); err == nil {
		t.Fatal("a delivery that failed verification created a job row")
	}

	ds := h.deliveries()
	if len(ds) != 1 {
		t.Fatalf("recorded %d deliveries, want 1", len(ds))
	}
	if ds[0].Status != "rejected" {
		t.Fatalf("delivery status = %q, want rejected", ds[0].Status)
	}
	if ds[0].Error == "" {
		t.Fatal("a rejected delivery was recorded without saying why")
	}
}

func TestUnsignedWebhookIsRejected(t *testing.T) {
	h := newHarness(t)
	h.fleet()

	rec := h.deliver("workflow_job", jobEvent{Action: "queued", JobID: 3003}.body(), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; an unsigned delivery lets anyone start runners", rec.Code, http.StatusUnauthorized)
	}
}

// A delivery for a repository no installation covers is still verified against
// every secret Zoomies holds, and the delivery records that it had to.
func TestWebhookForAnUnknownRepositoryTriesEverySecret(t *testing.T) {
	h := newHarness(t)
	h.fleet()

	rec := h.deliverJob(jobEvent{
		Action: "queued", JobID: 4004, Repo: "other-org/thing",
		Labels: []string{"self-hosted", "linux", "x64", "demo"},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	ds := h.deliveries()
	if len(ds) != 1 || ds[0].Status != "accepted" {
		t.Fatalf("deliveries = %+v, want one accepted", ds)
	}
	if !strings.Contains(ds[0].Error, "no installation covers") {
		t.Fatalf("the delivery does not record that no installation matched: %q", ds[0].Error)
	}
}

func TestPingIsAccepted(t *testing.T) {
	h := newHarness(t)
	h.installation()

	rec := h.deliver("ping", []byte(`{"zen":"Anything added dilutes everything else.","hook_id":1,"repository":{"full_name":"acme/widgets"}}`), testWebhookSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	ds := h.deliveries()
	if len(ds) != 1 || ds[0].Status != "accepted" || ds[0].Event != "ping" {
		t.Fatalf("deliveries = %+v, want one accepted ping", ds)
	}
	if h.c.PollingOnly() {
		t.Fatal("a delivery arrived, so the controller is no longer polling-only")
	}
}

func TestOnlyPostIsAccepted(t *testing.T) {
	h := newHarness(t)
	req := newGetRequest()
	rec := recorder()
	h.c.HandleWebhook(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// Deliveries are at-least-once and can arrive out of order. A late "queued"
// must not resurrect a job that has already finished, or the scheduler would
// create a runner for work that is over.
func TestLateQueuedDeliveryDoesNotRewindAJob(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	queued := time.Now().Add(-5 * time.Minute)

	h.deliverJob(jobEvent{Action: "completed", JobID: 5005, Labels: labels, Conclusion: "success", QueuedAt: queued})
	h.deliverJob(jobEvent{Action: "queued", JobID: 5005, Labels: labels, QueuedAt: queued})

	job, err := h.st.GetJobByGitHubID(h.ctx, 5005)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if job.State != store.JobCompleted {
		t.Fatalf("job state = %q, want %q; the late queued delivery rewound it", job.State, store.JobCompleted)
	}
	if job.Conclusion != "success" {
		t.Fatalf("conclusion = %q, want success", job.Conclusion)
	}

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rs := h.runners(); len(rs) != 0 {
		t.Fatalf("created %d runners for a job that had already completed", len(rs))
	}
}

// An in_progress delivery is what links a job to the runner GitHub gave it to.
func TestInProgressDeliveryLinksTheRunner(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}

	h.deliverJob(jobEvent{Action: "queued", JobID: 6006, Labels: labels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r := h.onlyRunner()
	// The agent brings it up and stops asserting a state once it is running.
	mustReport(t, h, host.ID, r.ID, store.RunnerRegistering)
	mustReportRunning(t, h, host.ID, r.ID)

	h.deliverJob(jobEvent{Action: "in_progress", JobID: 6006, Labels: labels, RunnerName: r.Name})

	job, err := h.st.GetJobByGitHubID(h.ctx, 6006)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if job.RunnerID != r.ID {
		t.Fatalf("job.RunnerID = %q, want %q", job.RunnerID, r.ID)
	}
	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerBusy {
		t.Fatalf("runner state = %q, want %q", after.State, store.RunnerBusy)
	}
	if after.CurrentJobID != job.ID {
		t.Fatalf("runner.CurrentJobID = %q, want %q", after.CurrentJobID, job.ID)
	}
}
