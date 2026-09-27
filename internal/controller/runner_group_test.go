package controller

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// A pool names a runner group to keep its runners away from work that is not
// meant for them. When that group cannot be resolved the runners register in
// GitHub's Default group, which every repository the installation covers can
// reach -- so the pool is running its jobs somewhere wider than its operator
// asked for. That was a log line and nothing else.

// runnerGroupWarning returns the pool.runner_group_unresolved entry, or nil.
func runnerGroupWarning(h *harness, poolID string) *Problem {
	h.t.Helper()
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		h.t.Fatalf("Problems: %v", err)
	}
	for i, p := range ps {
		if p.Code == "pool.runner_group_unresolved" && p.TargetID == poolID {
			return &ps[i]
		}
	}
	return nil
}

// groupPool is a fleet whose pool asks for a named runner group and has a job
// waiting, so that a reconcile pass actually mints a runner for it.
func groupPool(h *harness, group string) *store.Pool {
	h.t.Helper()
	inst, pool, _ := h.fleet()
	pool.RunnerGroup = group
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		h.t.Fatalf("UpdatePool: %v", err)
	}
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 9001, Repo: "acme/widgets", Workflow: "CI", JobName: "build",
		Labels: store.StringSlice{"self-hosted", "linux", "x64", "demo"},
		State:  store.JobQueued, InstallationID: inst.ID,
	}); err != nil {
		h.t.Fatalf("UpsertJob: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		h.t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	return pool
}

func TestAPoolNamingAGroupTheTargetDoesNotHaveSaysSoInsteadOfUsingDefaultQuietly(t *testing.T) {
	h := newHarness(t)
	h.gh.AddRunnerGroup("builders")
	pool := groupPool(h, "gpu")

	if got := len(h.runners()); got != 1 {
		t.Fatalf("created %d runners, want 1: the pool still gets its runner", got)
	}
	w := runnerGroupWarning(h, pool.ID)
	if w == nil {
		t.Fatal("a pool whose runner group does not exist raised no warning")
	}
	if !strings.Contains(w.Detail, "gpu") || !strings.Contains(w.Detail, "Default") {
		t.Errorf("detail = %q, want it to name the group asked for and the one used", w.Detail)
	}
	if w.Title == "" || w.Fix == "" {
		t.Errorf("warning = %+v, want a title and a fix as well as a detail", w)
	}
}

func TestAPoolWhoseRunnerGroupsCannotBeListedSaysThatRatherThanNaminTheGroup(t *testing.T) {
	h := newHarness(t)
	h.gh.SetError("/actions/runner-groups", http.StatusForbidden, "Resource not accessible by integration")
	pool := groupPool(h, "gpu")

	w := runnerGroupWarning(h, pool.ID)
	if w == nil {
		t.Fatal("a pool whose groups could not be listed raised no warning")
	}
	// The two cases need different fixes, so they must not read the same: this
	// one is a permission, not a missing group.
	if !strings.Contains(w.Detail, "would not say") {
		t.Errorf("detail = %q, want it to say GitHub would not answer rather than that the group is missing", w.Detail)
	}
}

// Runner groups belong to an organisation. A pool on a repository target that
// names one is misconfigured in the opposite direction: nothing to create, the
// group has to come off the pool.
func TestAPoolOnARepositoryTargetIsToldRunnerGroupsAreAnOrganisationThing(t *testing.T) {
	h := newHarness(t)
	inst := h.installationOn("acme/widgets", store.TargetRepo)
	h.host("vm-1")
	pool := h.pool(inst, "linux-x64")
	pool.RunnerGroup = "gpu"
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 9002, Repo: "acme/widgets", Workflow: "CI", JobName: "build",
		Labels: store.StringSlice{"self-hosted", "linux", "x64", "demo"},
		State:  store.JobQueued, InstallationID: inst.ID,
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// The warning is noted while the runner's credential is minted, which
	// runs detached from the pass; reading problems before it finishes read
	// them before the warning existed, and failed whenever it lost the race.
	h.c.lifecycleCalls.Wait()

	w := runnerGroupWarning(h, pool.ID)
	if w == nil {
		t.Fatal("a repository-target pool naming a runner group raised no warning")
	}
	if !strings.Contains(w.Detail, "organisation") {
		t.Errorf("detail = %q, want it to say runner groups belong to an organisation", w.Detail)
	}
}

// The warning has to go away by itself, or an operator who fixes it is left
// looking at a drawer that still says they have not.
func TestTheRunnerGroupWarningClearsOnceTheGroupExists(t *testing.T) {
	h := newHarness(t)
	pool := groupPool(h, "gpu")
	if runnerGroupWarning(h, pool.ID) == nil {
		t.Fatal("the warning was never raised, so clearing it proves nothing")
	}

	// The operator creates the group, and the fleet makes its next runner.
	h.gh.AddRunnerGroup("gpu")
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 9003, Repo: "acme/widgets", Workflow: "CI", JobName: "build2",
		Labels: store.StringSlice{"self-hosted", "linux", "x64", "demo"},
		State:  store.JobQueued, InstallationID: pool.InstallationID,
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Registration -- where the group is resolved and the warning cleared --
	// runs on its own goroutine after the pass. Asking before it finishes
	// read whichever it happened to be, and failed about one run in three.
	h.c.lifecycleCalls.Wait()
	if w := runnerGroupWarning(h, pool.ID); w != nil {
		t.Fatalf("the warning survived the group being created: %+v", w)
	}
}

// A pool that names no group, or names Default, is not misconfigured and must
// not be warned about -- the drawer is only read while it stays short.
func TestAPoolWithNoRunnerGroupIsNotWarnedAbout(t *testing.T) {
	h := newHarness(t)
	for _, group := range []string{"", "Default", "default"} {
		t.Run("group "+group, func(t *testing.T) {
			h := newHarness(t)
			pool := groupPool(h, group)
			if w := runnerGroupWarning(h, pool.ID); w != nil {
				t.Fatalf("a pool asking for %q was warned about: %+v", group, w)
			}
		})
	}
	_ = h
}

func TestAPoolWhoseRunnerGroupBlocksPublicRepositoriesSaysJobsCanStayQueued(t *testing.T) {
	h := newHarness(t)
	h.gh.SetRunnerGroupPublicAccess("Default", false)
	pool := groupPool(h, "")

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	for _, p := range ps {
		if p.Code != "pool.runner_group_public_repositories_blocked" || p.TargetID != pool.ID {
			continue
		}
		if !strings.Contains(p.Detail, "remain queued") || !strings.Contains(p.Fix, "Allow public repositories") {
			t.Fatalf("warning = %+v, want the symptom and the GitHub setting that fixes it", p)
		}
		return
	}
	t.Fatal("a pool whose runner group blocks public repositories raised no warning")
}

func TestInstallationProbeCreatesAndAdoptsManagedRunnerGroup(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	h.gh.SetRunnerGroupPublicAccess("Default", false)
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	busy := h.runnerRow(pool, host, store.RunnerBusy)

	if _, err := h.c.ProbeInstallation(h.ctx, inst.ID); err != nil {
		t.Fatalf("ProbeInstallation: %v", err)
	}
	got, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.RunnerGroup != ManagedRunnerGroupName {
		t.Fatalf("runner group = %q, want %q", got.RunnerGroup, ManagedRunnerGroupName)
	}
	if got := h.runnerByID(t, idle.ID).State; got != store.RunnerDraining {
		t.Fatalf("idle legacy runner state = %q, want draining so its replacement joins the managed group", got)
	}
	if got := h.runnerByID(t, busy.ID).State; got != store.RunnerBusy {
		t.Fatalf("busy legacy runner state = %q, want busy so its job can finish", got)
	}
	groups, err := h.gh.Client(inst.Target, inst.TargetType).ListRunnerGroups(h.ctx)
	if err != nil {
		t.Fatalf("ListRunnerGroups: %v", err)
	}
	if !slices.ContainsFunc(groups, func(g github.RunnerGroup) bool {
		return g.Name == ManagedRunnerGroupName && g.Visibility == "all" && g.AllowsPublicRepositories
	}) {
		t.Fatalf("runner groups = %+v, want the public organisation-wide %q group", groups, ManagedRunnerGroupName)
	}

	if _, err := h.c.ProbeInstallation(h.ctx, inst.ID); err != nil {
		t.Fatalf("second ProbeInstallation: %v", err)
	}
	creates := 0
	for _, request := range h.gh.Requests() {
		if strings.Contains(request, "POST /orgs/acme/actions/runner-groups") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("runner-group create requests = %d, want exactly one after two probes", creates)
	}
}

func TestInstallationProbePreservesAnExplicitPoolRunnerGroup(t *testing.T) {
	h := newHarness(t)
	inst, pool, _ := h.fleet()
	h.gh.AddRunnerGroup("security-reviewed")
	pool.RunnerGroup = "security-reviewed"
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	if _, err := h.c.ProbeInstallation(h.ctx, inst.ID); err != nil {
		t.Fatalf("ProbeInstallation: %v", err)
	}
	got, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.RunnerGroup != "security-reviewed" {
		t.Fatalf("runner group = %q, want the administrator's explicit choice preserved", got.RunnerGroup)
	}
}

func TestInstallationProbeDoesNotWidenAnExistingIncompatibleZoomiesGroup(t *testing.T) {
	h := newHarness(t)
	inst, pool, _ := h.fleet()
	h.gh.AddRunnerGroup(ManagedRunnerGroupName)
	h.gh.SetRunnerGroupPublicAccess(ManagedRunnerGroupName, false)

	if _, err := h.c.ProbeInstallation(h.ctx, inst.ID); err == nil || !strings.Contains(err.Error(), "Allow public repositories") {
		t.Fatalf("ProbeInstallation error = %v, want actionable incompatible-group error", err)
	}
	got, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.RunnerGroup != "" {
		t.Fatalf("runner group = %q, want the pool untouched after provisioning failed", got.RunnerGroup)
	}
}

// Losing the lease means another controller is running against the same
// database and both are scheduling. It is the one condition this process
// cannot recover from on its own, so it says so and keeps saying so.
func TestLosingTheLeaseIsReportedAsAnErrorThatNamesTheNewHolder(t *testing.T) {
	h := newHarness(t)
	if got := h.problemCodes(); contains(got, "controller.lease_lost") {
		t.Fatalf("a controller that holds its lease reported losing it: %v", got)
	}

	h.c.leaseLost.Store(&store.ControllerLease{
		Holder: "ctl_rival", Host: "vm-b", PID: 991,
	})

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var found *Problem
	for i, p := range ps {
		if p.Code == "controller.lease_lost" {
			found = &ps[i]
		}
	}
	if found == nil {
		t.Fatalf("problems = %v, want the lost-lease error", h.problemCodes())
	}
	if found.Severity != config.SeverityError {
		t.Errorf("severity = %q, want an error: two controllers are scheduling", found.Severity)
	}
	if !strings.Contains(found.Detail, "vm-b") || !strings.Contains(found.Detail, "991") {
		t.Errorf("detail = %q, want it to name the machine and process now holding it", found.Detail)
	}
	if !strings.Contains(found.Fix, "--takeover") {
		t.Errorf("fix = %q, want it to name the flag that resolves this", found.Fix)
	}
}

// The agent adopts what it finds running, so the controller is the only party
// that knows a runner was deleted while the agent was down. It answers with
// the ones it does not know, and the agent reaps only those.
func TestTheHeartbeatNamesOnlyTheRunnersThisControllerDoesNotKnow(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	live := h.runnerRow(pool, host, store.RunnerBusy)
	other := h.host("vm-other")
	theirs := h.runnerRow(pool, other, store.RunnerBusy)

	resp, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Runners: []agent.RunnerReport{
			{RunnerID: live.ID, State: store.RunnerBusy},
			{RunnerID: "run_deletedwhiledown", State: store.RunnerBusy},
			{RunnerID: theirs.ID, State: store.RunnerBusy},
		},
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if len(resp.UnknownRunners) != 1 || resp.UnknownRunners[0] != "run_deletedwhiledown" {
		t.Fatalf("unknown = %v, want only the runner with no row", resp.UnknownRunners)
	}
	// A runner belonging to another host is a different fault, already logged
	// where reports are applied. Answering "unknown" would invite one host to
	// remove another's work.
	for _, id := range resp.UnknownRunners {
		if id == theirs.ID {
			t.Error("a runner owned by another host was named as unknown")
		}
	}
}
