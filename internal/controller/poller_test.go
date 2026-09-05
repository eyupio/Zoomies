package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The poller is what stops a misconfigured webhook from silently ending the
// fleet's scaling. It feeds the same rows the webhook path writes.
func TestPollerDiscoversQueuedJobs(t *testing.T) {
	h := newHarness(t)
	_, pool, _ := h.fleet()
	queued := h.gh.AddQueuedJob("acme/widgets", "CI", "build", []string{"self-hosted", "linux", "x64", "demo"})

	h.c.pollOnce(h.ctx)

	job, err := h.st.GetJobByGitHubID(h.ctx, queued.ID)
	if err != nil {
		t.Fatalf("the poller did not record the queued job: %v", err)
	}
	if job.State != store.JobQueued || job.PoolID != pool.ID || !job.Matched {
		t.Fatalf("job = %+v, want it queued and matched to %s", job, pool.ID)
	}
	if !h.c.PollingOnly() {
		t.Fatal("no webhook has ever arrived, so the controller is polling-only and should say so")
	}

	// And the pass that follows creates a runner for it, exactly as the
	// webhook path would.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(h.runners()); got != 1 {
		t.Fatalf("created %d runners for the polled job, want 1", got)
	}
}

// When webhooks are working the poller must cost nothing at all -- one local
// query and no GitHub calls -- which is what makes leaving it on by default
// defensible.
func TestPollerStandsDownWhenWebhooksAreRecent(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	h.gh.AddQueuedJob("acme/widgets", "CI", "build", []string{"self-hosted", "linux", "x64", "demo"})

	if err := h.st.RecordDelivery(h.ctx, &store.WebhookDelivery{
		DeliveryID: "recent", Event: "workflow_job", Status: "accepted", ReceivedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	before := len(h.gh.Requests())

	h.c.pollOnce(h.ctx)

	if after := len(h.gh.Requests()); after != before {
		t.Fatalf("the poller made %d GitHub calls despite a webhook arriving seconds ago", after-before)
	}
	if h.c.PollingOnly() {
		t.Fatal("a delivery has arrived, so the controller is not polling-only")
	}
}

// A rejected delivery -- a mistyped webhook secret, say -- records a job for
// nobody. The poller must not take it as proof that webhooks work, or a fleet
// with the wrong secret never starts a runner and never says why.
func TestPollerKeepsGoingWhenDeliveriesAreRejected(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	h.gh.AddQueuedJob("acme/widgets", "CI", "build", []string{"self-hosted", "linux", "x64", "demo"})

	if err := h.st.RecordDelivery(h.ctx, &store.WebhookDelivery{
		DeliveryID: "bad-secret", Event: "workflow_job", Status: "rejected", ReceivedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	before := len(h.gh.Requests())

	h.c.pollOnce(h.ctx)

	if after := len(h.gh.Requests()); after == before {
		t.Fatal("the poller stood down on a delivery whose signature did not verify")
	}
	if !h.c.PollingOnly() {
		t.Fatal("no delivery has verified, so the controller is still polling-only")
	}
}

// A rate-limited installation must make the poller stand down rather than
// spend the quota the webhook path's own calls need.
func TestPollerBacksOffWhenRateLimited(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	reset := time.Now().Add(time.Hour)
	h.gh.SetRateLimit(5000, 0, reset)
	h.gh.SetError("/installation/repositories", 403, "API rate limit exceeded")

	h.c.pollOnce(h.ctx)

	if h.c.pollPausedUntil.Load() == 0 {
		t.Fatal("the poller did not back off after GitHub reported a rate limit")
	}

	before := len(h.gh.Requests())
	h.c.pollOnce(h.ctx)
	if after := len(h.gh.Requests()); after != before {
		t.Fatalf("the poller made %d more calls while backed off", after-before)
	}
}
