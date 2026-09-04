package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// The Overview renders "nothing needs your attention" from an empty list, so
// an instance with nothing wrong must return exactly that -- and an empty
// slice, not nil, because the API marshals it straight to JSON.
func TestProblemsIsEmptyOnACleanInstance(t *testing.T) {
	h := newHarness(t)

	got, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	if got == nil {
		t.Fatal("Problems returned nil; the UI needs an empty slice to render its quiet state")
	}
	if len(got) != 0 {
		t.Fatalf("a clean instance reported %d problems: %+v", len(got), got)
	}
}

// Every category the panel aggregates, provoked one at a time.
func TestProblemsReportsEachCategory(t *testing.T) {
	t.Run("configuration warning", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Security.DisableAuth = true
		if !contains(h.problemCodes(), "auth.disabled") {
			t.Fatalf("problems = %v, want the disabled-auth warning", h.problemCodes())
		}
	})

	t.Run("dangerous pool", func(t *testing.T) {
		h := newHarness(t)
		inst := h.installation()
		p := h.pool(inst, "risky")
		p.DockerMode = store.DockerHostSocket
		p.Ephemeral = false
		if err := h.st.UpdatePool(h.ctx, p); err != nil {
			t.Fatalf("UpdatePool: %v", err)
		}
		if !contains(h.problemCodes(), "pool.dangerous") {
			t.Fatalf("problems = %v, want the dangerous-pool warning", h.problemCodes())
		}
	})

	t.Run("unusable installation", func(t *testing.T) {
		h := newHarness(t)
		inst := h.installation()
		if err := h.st.SetInstallationHealth(h.ctx, inst.ID, "the App is not installed on acme"); err != nil {
			t.Fatalf("SetInstallationHealth: %v", err)
		}
		ps, err := h.c.Problems(h.ctx)
		if err != nil {
			t.Fatalf("Problems: %v", err)
		}
		found := false
		for _, p := range ps {
			if p.Code == "installation.unhealthy" {
				found = true
				if p.Severity != config.SeverityError {
					t.Fatalf("severity = %q, want error", p.Severity)
				}
				if p.Detail != "the App is not installed on acme" {
					t.Fatalf("detail = %q, want the probe's own message", p.Detail)
				}
			}
		}
		if !found {
			t.Fatalf("problems = %+v, want one about the installation", ps)
		}
	})

	t.Run("rejected webhooks", func(t *testing.T) {
		h := newHarness(t)
		h.fleet()
		h.deliver("workflow_job", jobEvent{Action: "queued", JobID: 1}.body(), "wrong")
		if !contains(h.problemCodes(), "webhook.rejected") {
			t.Fatalf("problems = %v, want the rejected-delivery warning", h.problemCodes())
		}
	})

	t.Run("no webhook has ever arrived", func(t *testing.T) {
		h := newHarness(t)
		h.installation()
		if !contains(h.problemCodes(), "webhook.never_received") {
			t.Fatalf("problems = %v, want the polling-only warning", h.problemCodes())
		}
	})

	t.Run("cordoned host with queued work", func(t *testing.T) {
		h := newHarness(t)
		_, _, host := h.fleet()
		if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
			t.Fatalf("SetHostCordoned: %v", err)
		}
		h.deliverJob(jobEvent{Action: "queued", JobID: 2, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
		if !contains(h.problemCodes(), "host.cordoned_with_work") {
			t.Fatalf("problems = %v, want the cordoned-host warning", h.problemCodes())
		}
	})

	t.Run("failed runners", func(t *testing.T) {
		h := newHarness(t)
		_, pool, host := h.fleet()
		r := h.runnerRow(pool, host, store.RunnerProvisioning)
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerFailed, "the image could not be pulled"); err != nil {
			t.Fatalf("TransitionRunner: %v", err)
		}
		if !contains(h.problemCodes(), "runners.failed") {
			t.Fatalf("problems = %v, want the failed-runner warning", h.problemCodes())
		}
	})
}

// A job whose labels no pool advertises will never run, and saying so is the
// only way an operator finds out.
func TestUnmatchedJobIsRecordedAndReported(t *testing.T) {
	h := newHarness(t)
	h.fleet()

	h.deliverJob(jobEvent{
		Action: "queued", JobID: 909,
		Labels: []string{"self-hosted", "linux", "gpu", "cuda12"},
	})

	job, err := h.st.GetJobByGitHubID(h.ctx, 909)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if job.Matched || job.PoolID != "" {
		t.Fatalf("job = %+v, want it recorded as unmatched", job)
	}

	// Before any reconcile, the flag on the row is what answers.
	if !contains(h.problemCodes(), "jobs.unmatched") {
		t.Fatalf("problems = %v, want the unmatched-job warning", h.problemCodes())
	}

	// And after one, the scheduler's own Unmatched list does.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !contains(h.problemCodes(), "jobs.unmatched") {
		t.Fatalf("problems after a reconcile = %v, want the unmatched-job warning", h.problemCodes())
	}
	if rs := h.runners(); len(rs) != 0 {
		t.Fatalf("created %d runners for a job no pool claims", len(rs))
	}
}

// Errors come first: an operator scanning the panel should meet the things
// that are broken before the things that are merely risky.
func TestProblemsAreSortedErrorsFirst(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "risky")
	p.DockerMode = store.DockerHostSocket
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if err := h.st.SetInstallationHealth(h.ctx, inst.ID, "credentials rejected"); err != nil {
		t.Fatalf("SetInstallationHealth: %v", err)
	}

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	if len(ps) < 2 {
		t.Fatalf("problems = %+v, want at least two", ps)
	}
	if ps[0].Severity != config.SeverityError {
		t.Fatalf("first problem is %q, want the error", ps[0].Severity)
	}
	seenWarning := false
	for _, p := range ps {
		if p.Severity == config.SeverityWarning {
			seenWarning = true
		} else if p.Severity == config.SeverityError && seenWarning {
			t.Fatalf("an error appears after a warning: %+v", ps)
		}
	}
}

// Stats is what the Overview's cards are built from.
func TestStatsSummarisesTheFleet(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.runnerRow(pool, host, store.RunnerBusy)
	h.runnerRow(pool, host, store.RunnerIdle)
	h.deliverJob(jobEvent{Action: "queued", JobID: 11, Labels: []string{"self-hosted", "linux", "x64", "demo"}})

	s, err := h.c.Stats(h.ctx, time.Hour)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.QueuedJobs != 1 {
		t.Fatalf("queued jobs = %d, want 1", s.QueuedJobs)
	}
	if s.Runners.Busy != 1 || s.Runners.Idle != 1 || s.Runners.Total != 2 {
		t.Fatalf("runner counts = %+v, want one busy and one idle", s.Runners)
	}
	if s.Hosts.Total != 1 || s.Hosts.Healthy != 1 || s.Hosts.Capacity != 4 || s.Hosts.Used != 2 {
		t.Fatalf("host counts = %+v, want one healthy host with 4 slots and 2 used", s.Hosts)
	}
	if len(s.Pools) != 1 || s.Pools[0].Queued != 1 || s.Pools[0].Live != 2 {
		t.Fatalf("pool stats = %+v, want one pool with one queued job and two live runners", s.Pools)
	}
	if s.Pools[0].Utilisation != 0.5 {
		t.Fatalf("utilisation = %v, want 0.5", s.Pools[0].Utilisation)
	}
}
