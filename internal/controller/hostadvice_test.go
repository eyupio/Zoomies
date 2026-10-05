package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// capacityFleet is the host in the report this is written from: a machine set to
// three slots whose standard runner is most of it, so it holds one -- and a pool
// that takes its size from the host.
func capacityFleet(t *testing.T, mutate func(*store.RunnerProfile)) (*harness, *store.Host, *store.Pool) {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.SizeFromProfile = true
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	host := h.measuredHost("dev-box", 8, 32768, 3, enforcesEverything)
	profile := store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 6, MemoryMB: 6656}}
	if mutate != nil {
		mutate(&profile)
	}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &profile}); err != nil {
		t.Fatal(err)
	}
	host, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h, host, pool
}

// waiting makes the evidence the advice rests on: jobs of the pool that each
// waited two minutes for a runner, within the window.
func waiting(h *harness, pool *store.Pool) {
	h.t.Helper()
	for i := 0; i < pressureMinJobs+1; i++ {
		queued := h.c.Now().Add(-time.Hour)
		started, done := queued.Add(2*time.Minute), queued.Add(5*time.Minute)
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: int64(1000 + i), GitHubRunID: 1, Repo: "acme/widgets", Workflow: "ci", JobName: "build",
			Labels: store.StringSlice{"self-hosted", "linux", "x64"}, State: store.JobCompleted, Conclusion: "success",
			InstallationID: pool.InstallationID, PoolID: pool.ID, Matched: true,
			QueuedAt: queued, StartedAt: &started, CompletedAt: &done,
		}); err != nil {
			h.t.Fatal(err)
		}
	}
}

// With jobs having waited for a runner, a host that holds fewer runners than its capacity
// because of how big each one is is told so, with the size that would give it
// the capacity and what that costs: here, nothing.
func TestAHostHoldingFewerRunnersThanItsCapacityIsOfferedTheSizeThatFixesIt(t *testing.T) {
	h, host, pool := capacityFleet(t, nil)
	if host.Slots() >= host.Capacity {
		t.Fatalf("the fixture must hold fewer than its capacity: slots %d, capacity %d", host.Slots(), host.Capacity)
	}
	waiting(h, pool)

	p := h.problemOrNil("host.slots_below_capacity")
	if p == nil || p.TargetID != host.ID {
		t.Fatalf("problem = %+v; want one for the host", p)
	}
	if p.Remedy == nil || p.Remedy.Kind != RemedyHostUpdate || p.Remedy.TargetID != host.ID {
		t.Fatalf("remedy = %+v; want a host update", p.Remedy)
	}
	var body struct {
		RunnerProfile store.RunnerProfile `json:"runner_profile"`
	}
	if err := json.Unmarshal(p.Remedy.Body, &body); err != nil {
		t.Fatal(err)
	}
	alloc := host.Allocatable()
	std := body.RunnerProfile.Standard
	if std.CPUs <= 0 || std.CPUs > alloc.CPUs/float64(host.Capacity)+1e-9 || std.CPUs >= 6 {
		t.Errorf("standard CPU %v must be smaller than 6 and at most %v, the capacity's share", std.CPUs, alloc.CPUs/float64(host.Capacity))
	}
	// Applied, the host would hold what it was set to.
	cand := *host
	cand.RunnerProfile = body.RunnerProfile
	if cand.Slots() != host.Capacity {
		t.Errorf("the proposed size holds %d runners, want the capacity %d", cand.Slots(), host.Capacity)
	}
	if !strings.Contains(p.Remedy.Effect, "no pool that reaches it loses room") || !strings.Contains(p.Detail, "builders") {
		t.Errorf("the effect and the pool that waited must be said:\neffect: %s\ndetail: %s", p.Remedy.Effect, p.Detail)
	}
}

// Without jobs having waited, big runners may be what somebody wanted, and a capacity
// above what they allow is only a ceiling: nothing is said.
func TestAHostIsOnlyAdvisedWhileJobsHaveBeenWaiting(t *testing.T) {
	h, _, _ := capacityFleet(t, nil)
	if p := h.problemOrNil("host.slots_below_capacity"); p != nil {
		t.Fatalf("advised with nothing waiting: %+v", p)
	}
}

func TestAHostThatHoldsItsCapacityIsNotAdvised(t *testing.T) {
	h, _, pool := capacityFleet(t, func(p *store.RunnerProfile) { p.Standard = store.RunnerStandard{CPUs: 2, MemoryMB: 4096} })
	waiting(h, pool)
	if p := h.problemOrNil("host.slots_below_capacity"); p != nil {
		t.Fatalf("a host holding all its slots was advised: %+v", p)
	}
}

// The size that would give the host its slots must still be one a runner may be
// given. Where it is not, the notice says what stands in the way and offers nothing
// to apply, because a change that is refused or that costs a pool its runners is not
// a suggestion.
func TestNoRemedyIsOfferedForASizeBelowTheHostsOwnSmallestRunner(t *testing.T) {
	h, _, pool := capacityFleet(t, func(p *store.RunnerProfile) { p.Minimum = store.RunnerSize{CPUs: 4} })
	waiting(h, pool)
	p := h.problemOrNil("host.slots_below_capacity")
	if p == nil {
		t.Fatal("the squeeze is real and the host should still be told")
	}
	if p.Remedy != nil {
		t.Errorf("a remedy below the host's own smallest runner was offered: %+v", p.Remedy)
	}
	if !strings.Contains(p.Detail, "smallest runner") {
		t.Errorf("what stands in the way must be said: %s", p.Detail)
	}
}

// minimumFleet is the pool in the report this was written from: a Docker-in-Docker
// pool whose smallest runner is 1.5 GB a container with a sidecar holding 15% of the
// memory, which makes every slot at least ten gigabytes on machines that could hold
// three runners at a third of that.
func minimumFleet(t *testing.T, peakMB int64, jobs int, oom bool) (*harness, *store.Pool) {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.DockerMode = store.DockerDinD
	pool.Resources.MinMemoryMB = 1536
	pool.Resources.DaemonMemorySharePercent = 15
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	host := h.measuredHost("box-1", 8, 16384, 3, enforcesEverything)
	for i := 0; i < jobs; i++ {
		// A peak is measured while the job runs, so the job goes through its life: it
		// starts on a runner, the runner's use is recorded, and then it completes.
		runner := &store.Runner{PoolID: pool.ID, HostID: host.ID, Name: fmt.Sprintf("r%d", i), State: store.RunnerBusy}
		if err := h.st.CreateRunner(h.ctx, runner); err != nil {
			t.Fatal(err)
		}
		queued := h.c.Now().Add(-time.Hour)
		started, done := queued.Add(2*time.Minute), queued.Add(5*time.Minute)
		job := &store.Job{
			GitHubJobID: int64(5000 + i), GitHubRunID: 1, Repo: "acme/widgets", Workflow: "ci", JobName: "build",
			Labels: store.StringSlice{"self-hosted", "linux", "x64"}, State: store.JobInProgress, RunnerID: runner.ID,
			InstallationID: pool.InstallationID, PoolID: pool.ID, Matched: true, QueuedAt: queued, StartedAt: &started,
		}
		if _, err := h.st.UpsertJob(h.ctx, job); err != nil {
			t.Fatal(err)
		}
		if err := h.st.RecordJobUsage(h.ctx, runner.ID, 1, peakMB); err != nil {
			t.Fatal(err)
		}
		job.State, job.Conclusion, job.CompletedAt = store.JobCompleted, "success", &done
		if _, err := h.st.UpsertJob(h.ctx, job); err != nil {
			t.Fatal(err)
		}
		if oom && i == 0 {
			if _, _, err := h.st.MarkJobOOMKilled(h.ctx, runner.ID, "killed for memory"); err != nil {
				t.Fatal(err)
			}
		}
	}
	return h, pool
}

func TestAPoolWhoseMinimumChargesFarMoreThanItsJobsUsedIsOfferedALowerOne(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	p := h.problemOrNil("pool.minimum_overcharges")
	if p == nil || p.TargetID != pool.ID {
		t.Fatalf("problem = %+v; want one for the pool", p)
	}
	if p.Remedy == nil || p.Remedy.Kind != RemedyPoolUpdate {
		t.Fatalf("remedy = %+v; want a pool update", p.Remedy)
	}
	var body struct {
		Resources store.Resources `json:"resources"`
	}
	if err := json.Unmarshal(p.Remedy.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Resources.MinMemoryMB >= 1536 || body.Resources.MinMemoryMB < store.MinRunnerMemoryMB {
		t.Errorf("the proposed minimum is %d MB; want it lower than 1536 and at least the least a runner is given", body.Resources.MinMemoryMB)
	}
	// An update replaces resources whole, so the rest of them must come back as they were.
	if body.Resources.DaemonMemorySharePercent != 15 {
		t.Errorf("the pool's own settings were not carried: %+v", body.Resources)
	}
	cand := *pool
	cand.Resources = body.Resources
	before, _ := h.c.poolRoom(h.ctx, pool)
	after, _ := h.c.poolRoom(h.ctx, &cand)
	if after.Runners <= before.Runners || !strings.Contains(p.Remedy.Effect, "instead of") {
		t.Errorf("the proposal must give the hosts room for more runners and say so: %d -> %d (%s)", before.Runners, after.Runners, p.Remedy.Effect)
	}
}

// The advice rests on what jobs used, so it is silent without enough of them, and
// for a pool whose jobs were killed for memory it is never given: what they used is
// then the limit they hit and not what they needed.
func TestPoolMinimumAdviceNeedsEvidenceAndNeverShrinksAKilledPool(t *testing.T) {
	for name, tc := range map[string]struct {
		peak int64
		jobs int
		oom  bool
	}{
		"too few jobs":               {2048, 10, false},
		"a job killed for memory":    {2048, 25, true},
		"jobs that use what it asks": {9000, 25, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := minimumFleet(t, tc.peak, tc.jobs, tc.oom)
			if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
				t.Errorf("advised without the evidence for it: %+v", p)
			}
		})
	}
}

// The pattern from the report this was written from: the biggest host, throttled and
// running runners, while smaller ones sit idle, under an order that keeps choosing it.
func concentratedFleet(t *testing.T, bigBusy bool) (*harness, *store.Host) {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	big := h.measuredHost("big", 12, 32768, 5, enforcesEverything)
	small := h.measuredHost("small", 4, 16384, 1, enforcesEverything)
	now := h.c.Now()
	cpu := func(v float64) store.HostUsage { return store.HostUsage{CPUPercent: &v, SampledAt: now} }
	if err := h.st.SetHostUsage(h.ctx, big.ID, cpu(80)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetHostUsage(h.ctx, small.ID, cpu(2)); err != nil {
		t.Fatal(err)
	}
	if bigBusy {
		if err := h.st.SetHostThrottle(h.ctx, big.ID, store.HostThrottle{Level: 1, Since: &now, ChangedAt: &now, Reason: "load"}, 0); err != nil {
			t.Fatal(err)
		}
		if err := h.st.CreateRunner(h.ctx, &store.Runner{PoolID: pool.ID, HostID: big.ID, Name: "busy-1", State: store.RunnerBusy}); err != nil {
			t.Fatal(err)
		}
	}
	return h, big
}

func TestAThrottledHostIsNamedWhileSmallerOnesSitIdleUnderTheDefaultOrder(t *testing.T) {
	h, big := concentratedFleet(t, true)
	p := h.problemOrNil("host.work_concentrated")
	if p == nil || p.TargetID != big.ID {
		t.Fatalf("problem = %+v; want one for the throttled host", p)
	}
	if p.Remedy != nil {
		t.Errorf("no change is proposed, because which is right depends on what the host is for: %+v", p.Remedy)
	}
	for _, want := range []string{"small", "headroom", "best_fit"} {
		if !strings.Contains(p.Detail+" "+p.Fix, want) {
			t.Errorf("the notice must mention %q:\n%s\n%s", want, p.Detail, p.Fix)
		}
	}
}

func TestWorkConcentrationIsOnlySaidWhileItIsTrueAndTheOrderIsTheDefault(t *testing.T) {
	h, _ := concentratedFleet(t, false)
	if p := h.problemOrNil("host.work_concentrated"); p != nil {
		t.Errorf("a host that is not throttled was named: %+v", p)
	}
	h, _ = concentratedFleet(t, true)
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.HostOrder = "best_fit" })
	if p := h.problemOrNil("host.work_concentrated"); p != nil {
		t.Errorf("advised to change an order that was already changed: %+v", p)
	}
}
