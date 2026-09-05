package controller

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
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

	t.Run("repository cache under an organisation installation", func(t *testing.T) {
		h := newHarness(t)
		inst := h.installation()
		p := h.pool(inst, "widgets")
		p.Cache = store.CacheConfig{Enabled: true, Scope: store.CacheScopeRepository, Repository: "acme/widgets"}
		if err := h.st.UpdatePool(h.ctx, p); err != nil {
			t.Fatalf("UpdatePool: %v", err)
		}
		if !contains(h.problemCodes(), "pool.cache_shared") {
			t.Fatalf("problems = %v, want the shared-cache warning", h.problemCodes())
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

// A pool nothing can run is the failure that looks like health: the pool is
// enabled, the job matched it, every host is connected, and no runner is ever
// created. Nothing else in the product reports it -- a scaling event is written
// only when the size actually moved -- so the panel has to.
func TestPoolWithNoHostToRunItIsReported(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	p.Backend = store.BackendPodman
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	// One connected, healthy host -- which offers docker, not podman.
	host := h.host("vm-1")
	host.BackendInfo = store.HostBackends{{
		Kind: store.BackendPodman, Available: false,
		Detail: "podman.sock is not readable by this agent",
	}}
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 4242, Labels: p.Labels})

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rs := h.runners(); len(rs) != 0 {
		t.Fatalf("created %d runners with no host able to run them", len(rs))
	}

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var found *Problem
	for i, p := range ps {
		if p.Code == "pool.no_capacity" {
			found = &ps[i]
		}
	}
	if found == nil {
		t.Fatalf("problems = %v, want one saying the pool has nowhere to run", h.problemCodes())
	}
	// A queued job makes this an outage, not a warning about a pool that
	// merely cannot reach its minimum.
	if found.Severity != config.SeverityError {
		t.Fatalf("severity = %q, want error while a job is waiting", found.Severity)
	}
	if !strings.Contains(found.Detail, "podman") {
		t.Fatalf("detail = %q, want it to name the backend no host offers", found.Detail)
	}
	// The agent's own explanation is the whole fix, so it must survive the
	// trip from the probe to the panel.
	if !strings.Contains(found.Detail, "podman.sock is not readable") {
		t.Fatalf("detail = %q, want the host's own explanation", found.Detail)
	}
	if found.Fix == "" || found.TargetID != p.ID {
		t.Fatalf("problem = %+v, want a fix and a link to the pool", found)
	}
	// "point this pool at a backend they already offer" is only a fix if the
	// panel says which one, and hands the UI enough to make the change.
	if !slices.Contains(found.Alternatives, string(store.BackendDocker)) {
		t.Fatalf("alternatives = %v, want the docker backend this host does offer", found.Alternatives)
	}
	if !strings.Contains(found.Fix, "docker") {
		t.Fatalf("fix = %q, want it to name the backend to switch to", found.Fix)
	}
}

func TestRepositoryScaleUpDeferralIsAccurateInProblemsDrawer(t *testing.T) {
	h := newHarness(t)
	h.c.setLastPlan(scheduler.Plan{Pools: []scheduler.PoolPlan{{
		PoolID: "pool_shared", PoolName: "shared", QueuedMatched: 3,
		QuotaDeferredJobs: 2, QuotaDeferredRepositories: []string{"acme/api", "acme/web"},
	}}})

	ps := h.c.PoolCapacityProblems()
	if len(ps) != 1 || ps[0].Code != "pool.repository_scale_up_deferred" {
		t.Fatalf("problems = %+v, want one scale-up deferral", ps)
	}
	if ps[0].Severity != config.SeverityWarning || !strings.Contains(ps[0].Detail, "Compatible idle runners may still accept") {
		t.Fatalf("problem = %+v, want best-effort GitHub assignment caveat", ps[0])
	}
	if !strings.Contains(ps[0].Detail, "acme/api, acme/web") || !strings.Contains(ps[0].Fix, "repository-specific pools") {
		t.Fatalf("problem = %+v, want affected repositories and strict-isolation guidance", ps[0])
	}
}

// The same pool, once a host can run it, drops off the panel: a problem that
// never clears is one an operator learns to ignore.
func TestPoolProblemClearsWhenAHostCanRunIt(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	h.deliverJob(jobEvent{Action: "queued", JobID: 4243, Labels: p.Labels})

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !contains(h.problemCodes(), "pool.no_capacity") {
		t.Fatalf("problems = %v, want the blocked pool with no hosts at all", h.problemCodes())
	}

	h.host("vm-1")
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if contains(h.problemCodes(), "pool.no_capacity") {
		t.Fatalf("problems = %v, want the blocked pool gone once a host can run it", h.problemCodes())
	}
}

// A fleet that is simply full is the system working: the jobs are waiting for a
// runner to finish, not for an operator. It is still worth saying, but it is
// not an outage.
func TestAFullFleetIsAWarningRatherThanAnOutage(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	host.Capacity = 1
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	// The one slot is taken, and a second job is queued behind it.
	h.runnerRow(p, host, store.RunnerBusy)
	h.deliverJob(jobEvent{Action: "queued", JobID: 4244, Labels: p.Labels})

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	found := false
	for _, pr := range ps {
		if pr.Code != "pool.no_capacity" {
			continue
		}
		found = true
		if pr.Severity != config.SeverityWarning {
			t.Errorf("severity = %q, want a warning: the fleet is busy, not broken", pr.Severity)
		}
		if !strings.Contains(pr.Detail, "at capacity") {
			t.Errorf("detail = %q, want it to say the fleet is full", pr.Detail)
		}
	}
	if !found {
		t.Fatalf("problems = %v, want the pool waiting on capacity", h.problemCodes())
	}
}
