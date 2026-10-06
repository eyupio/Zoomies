package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
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
	return minimumFleetMeasured(t, peakMB, jobs, jobs, oom)
}

// minimumFleetMeasured is minimumFleet where only the first measured of the jobs had
// a peak recorded: a job too short to be sampled has none.
func minimumFleetMeasured(t *testing.T, peakMB int64, jobs, measured int, oom bool) (*harness, *store.Pool) {
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
		if i < measured {
			if err := h.st.RecordJobUsage(h.ctx, runner.ID, 1, peakMB); err != nil {
				t.Fatal(err)
			}
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
	// The halves the jobs used: most of the peak in the runner, a fifth in the
	// daemon, which is inside the share it is given.
	seedPairs(h, pool, peakMB*8/10, peakMB*2/10)
	return h, pool
}

// seedPairs is the window of what each container of the pool's pairs used, from
// enough runners to count as the pool's. The limits are the slot a 15% sidecar
// divides, which is all a sample's use is judged against.
func seedPairs(h *harness, pool *store.Pool, runnerMB, daemonMB int64) {
	h.c.pairMu.Lock()
	defer h.c.pairMu.Unlock()
	h.c.pairs = map[string][]pairSample{}
	for i := 0; i < pairMinSamples+10; i++ {
		h.c.pairs[pool.ID] = append(h.c.pairs[pool.ID], pairSample{at: h.c.Now(), sampled: h.c.Now().Add(time.Duration(i) * 30 * time.Second), runner: fmt.Sprintf("r%d", i%pairMinRunners+1), halves: backend.PairHalves{
			Runner: backend.HalfUse{MemoryBytes: runnerMB << 20, MemoryLimit: 8704 << 20},
			Daemon: backend.HalfUse{MemoryBytes: daemonMB << 20, MemoryLimit: 1536 << 20},
		}})
	}
}

// A job's peak is the two containers added together, but a thin sidecar has a limit
// of its own: a pool whose jobs fit the slot in sum can still keep nearly all of
// its peak in a daemon holding 15%, and a floor sized from the sum would have it
// killed. So the floor follows the half that needs the most, and a pool whose
// daemon wants more than it is charged now is not told to charge less.
func TestPoolMinimumAdviceIsSizedByTheThinnerContainerNotJustTheSum(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	seedPairs(h, pool, 600, 1448)
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("advised a floor the sidecar would be killed under: %+v", p)
	}
}

// Without what each container used there is nothing to size the halves by, and the
// sum alone is the assumption this advice does not rest on.
func TestPoolMinimumAdviceWaitsForWhatEachContainerUsed(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	h.c.pairMu.Lock()
	delete(h.c.pairs, pool.ID)
	h.c.pairMu.Unlock()
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("advised on the sum alone: %+v", p)
	}
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

// A host the controller has stepped down is held back by load, and changing its
// runner profile lifts the step-down in the same write. Offering the change would
// price a gain the throttle was withholding and remove the protection with it; the
// throttled-host notice already says what to do about it.
func TestAThrottledHostIsNotOfferedARunnerSizeThatWouldLiftItsStepDown(t *testing.T) {
	h, host, pool := capacityFleet(t, nil)
	waiting(h, pool)
	if h.problemOrNil("host.slots_below_capacity") == nil {
		t.Fatal("the fixture must be advised before it is throttled")
	}
	now := h.c.Now()
	if err := h.st.SetHostThrottle(h.ctx, host.ID, store.HostThrottle{Level: 1, Since: &now, ChangedAt: &now, Reason: "load"}, 0); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("host.slots_below_capacity"); p != nil {
		t.Errorf("a throttled host was offered a runner size: %+v", p)
	}
}

// Two pools that reach one machine each have room for what it holds. The sum of
// their rooms is not a number of runners the host can run, and printed as one it
// promised more than its capacity.
func TestAHostRemedyCountsTheHostsRunnersAndNotTheSumOfThePoolsThatReachIt(t *testing.T) {
	h, host, pool := capacityFleet(t, nil)
	other := h.pool(&store.Installation{ID: pool.InstallationID}, "tests")
	other.SizeFromProfile = true
	if err := h.st.UpdatePool(h.ctx, other); err != nil {
		t.Fatal(err)
	}
	waiting(h, pool)
	p := h.problemOrNil("host.slots_below_capacity")
	if p == nil || p.Remedy == nil {
		t.Fatalf("problem = %+v; want a remedy", p)
	}
	if !strings.Contains(p.Remedy.Effect, fmt.Sprintf("holds %s instead of %s", plural(host.Capacity, "runner"), plural(host.Slots(), "runner"))) {
		t.Errorf("the effect must be the host's own count (%d from %d): %s", host.Capacity, host.Slots(), p.Remedy.Effect)
	}
}

// An idle machine is idle for a reason when the busy host's work could never run on
// it, and naming it would send an operator to rebalance towards a host the pool
// cannot use.
func TestAnIdleHostThatTheBusyOnesWorkCouldNotRunOnIsNotCounted(t *testing.T) {
	h, big := concentratedFleet(t, true)
	if p := h.problemOrNil("host.work_concentrated"); p == nil {
		t.Fatal("an idle host the work could run on must be counted")
	}
	// The pool now asks for the busy host's label, which the idle one does not carry.
	if err := h.st.PatchHost(h.ctx, big.ID, store.HostChanges{Labels: &store.StringMap{"tier": "big"}}); err != nil {
		t.Fatal(err)
	}
	pools, err := h.st.ListPools(h.ctx)
	if err != nil || len(pools) != 1 {
		t.Fatalf("pools = %d, %v", len(pools), err)
	}
	pool := pools[0]
	pool.HostSelector = store.StringMap{"tier": "big"}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("host.work_concentrated"); p != nil {
		t.Errorf("an idle host the pool's selector excludes was counted: %+v", p)
	}
}

// What a pool's hosts can hold is a fact about the machines. A hold on new starts
// is load that lifts within minutes, so a room priced while one is on would flap
// with the queue pressure that makes the price worth taking.
func TestARoomIsCountedWhileAHostIsHeldBackByLoad(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	before, err := h.c.poolRoom(h.ctx, pool)
	if err != nil || before.Runners == 0 {
		t.Fatalf("room before = %+v, %v", before, err)
	}
	host, err := h.st.GetHostByName(h.ctx, "box-1")
	if err != nil {
		t.Fatal(err)
	}
	cpu := 99.0
	if err := h.st.SetHostUsage(h.ctx, host.ID, store.HostUsage{CPUPercent: &cpu, CPUHeld: true, SampledAt: h.c.Now()}); err != nil {
		t.Fatal(err)
	}
	after, err := h.c.poolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if after.Runners != before.Runners {
		t.Errorf("a host held back by load lost the pool its room: %d from %d", after.Runners, before.Runners)
	}
}

// A pool that spans two sizes of host has a different daemon limit on each and one
// share. The evidence must come from both whichever host reported last: the small,
// floor-bound host is where the minimum sizes the slot, and a sidecar there at 91%
// of its limit is the one thing that must stop a lower minimum.
func TestMinimumEvidenceKeepsTheSmallHostWhicheverReportedLast(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	seed := func(runnerLimit, daemonLimit, runnerMB, daemonMB int64, n int, id string) {
		h.c.pairMu.Lock()
		defer h.c.pairMu.Unlock()
		for i := 0; i < n; i++ {
			h.c.pairs[pool.ID] = append(h.c.pairs[pool.ID], pairSample{at: h.c.Now(), sampled: h.c.Now().Add(time.Duration(i) * 30 * time.Second), runner: fmt.Sprintf("%s%d", id, i%pairMinRunners), halves: backend.PairHalves{
				Runner: backend.HalfUse{MemoryBytes: runnerMB << 20, MemoryLimit: runnerLimit << 20},
				Daemon: backend.HalfUse{MemoryBytes: daemonMB << 20, MemoryLimit: daemonLimit << 20},
			}})
		}
	}
	h.c.pairMu.Lock()
	h.c.pairs = map[string][]pairSample{}
	h.c.pairMu.Unlock()
	seed(8704, 1536, 600, 1400, pairMinSamples+10, "small") // the floor-bound host, daemon at 91%
	seed(37100, 6550, 1000, 300, pairMinSamples+10, "big")  // a large host reported last
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("a lower minimum was proposed that the small host's sidecar would be killed under: %+v", p)
	}
}

// Stale evidence must stop speaking for a pool: a window nothing has trimmed since
// the pool went quiet is days old, and the notice claims the last six hours.
func TestSamplesOlderThanTheWindowStopSpeakingForAPool(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	if h.problemOrNil("pool.minimum_overcharges") == nil {
		t.Fatal("the fixture must be advised while its samples are fresh")
	}
	h.c.pairMu.Lock()
	for i := range h.c.pairs[pool.ID] {
		h.c.pairs[pool.ID][i].at = h.c.Now().Add(-pairWindow - time.Hour)
	}
	h.c.pairMu.Unlock()
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("advised on samples older than the window: %+v", p)
	}
}

// The apply refuses a remedy whose base is not what it is about to replace, so a base
// worked out from a different read of the same row would refuse every apply.
func TestARemedysBaseIsWhatTheTargetIsReadAsAtApply(t *testing.T) {
	h, host, pool := capacityFleet(t, nil)
	waiting(h, pool)
	p := h.problemOrNil("host.slots_below_capacity")
	if p == nil || p.Remedy == nil {
		t.Fatalf("problem = %+v", p)
	}
	fresh, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Remedy.Base == "" || p.Remedy.Base != RemedyBase(fresh.RunnerProfile) {
		t.Errorf("host remedy base %q is not the host's runner profile as it is read", p.Remedy.Base)
	}

	hm, pm := minimumFleet(t, 2048, 25, false)
	q := hm.problemOrNil("pool.minimum_overcharges")
	if q == nil || q.Remedy == nil {
		t.Fatalf("problem = %+v", q)
	}
	freshPool, err := hm.st.GetPool(hm.ctx, pm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if q.Remedy.Base != RemedyBase(freshPool.Resources) {
		t.Errorf("pool remedy base %q is not the pool's resources as they are read", q.Remedy.Base)
	}
}

// Sixty samples from three runners that arrive within minutes of a restart are the
// first phase of three jobs, not a pool. The advice that can get a daemon killed
// waits until the samples span half an hour, and until no one runner is most of them.
func TestPoolMinimumAdviceWaitsForEvidenceThatSpansTimeAndRunners(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	if h.problemOrNil("pool.minimum_overcharges") == nil {
		t.Fatal("the fixture must be advised with a half hour of samples")
	}
	reseed := func(step time.Duration, runnerOf func(i int) string) {
		h.c.pairMu.Lock()
		defer h.c.pairMu.Unlock()
		h.c.pairs = map[string][]pairSample{}
		for i := 0; i < pairMinSamples+10; i++ {
			h.c.pairs[pool.ID] = append(h.c.pairs[pool.ID], pairSample{at: h.c.Now(), sampled: h.c.Now().Add(time.Duration(i) * step), runner: runnerOf(i), halves: backend.PairHalves{
				Runner: backend.HalfUse{MemoryBytes: 600 << 20, MemoryLimit: 8704 << 20},
				Daemon: backend.HalfUse{MemoryBytes: 300 << 20, MemoryLimit: 1536 << 20},
			}})
		}
	}
	reseed(5*time.Second, func(i int) string { return fmt.Sprintf("r%d", i%pairMinRunners) })
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("advised on samples that span minutes: %+v", p)
	}
	reseed(30*time.Second, func(i int) string {
		if i%10 == 0 {
			return fmt.Sprintf("other%d", i%3)
		}
		return "one-long-job"
	})
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("advised on a window that one runner is most of: %+v", p)
	}
}

// Count is every completed job, and a job too short to be sampled has no peak. A pool
// of 25 jobs of which three were measured has shown three jobs' use, not twenty-five's,
// and is told nothing: the evidence the notice names is the evidence it rests on.
func TestPoolMinimumAdviceCountsJobsThatWereMeasuredNotJobsThatCompleted(t *testing.T) {
	h, _ := minimumFleetMeasured(t, 2048, 25, 3, false)
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Errorf("advised on three measured jobs of 25: %+v", p)
	}
	h, _ = minimumFleetMeasured(t, 2048, 40, 22, false)
	p := h.problemOrNil("pool.minimum_overcharges")
	if p == nil || !strings.Contains(p.Detail, "at least 20 with a measured peak") {
		t.Errorf("with 22 measured the notice must say what it rests on: %+v", p)
	}
}

// addMeasuredJob is one completed, measured job of a given name on the pool.
func addMeasuredJob(t *testing.T, h *harness, pool *store.Pool, n int, name string, peakMB int64) {
	t.Helper()
	hosts, err := h.st.ListHosts(h.ctx)
	if err != nil || len(hosts) == 0 {
		t.Fatalf("hosts = %d, %v", len(hosts), err)
	}
	host := hosts[0]
	runner := &store.Runner{PoolID: pool.ID, HostID: host.ID, Name: fmt.Sprintf("x%d", n), State: store.RunnerBusy}
	if err := h.st.CreateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	queued := h.c.Now().Add(-time.Hour)
	started, done := queued.Add(2*time.Minute), queued.Add(5*time.Minute)
	job := &store.Job{
		GitHubJobID: int64(9000 + n), GitHubRunID: 2, Repo: "acme/widgets", Workflow: "release", JobName: name,
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
}

// One heavy job among many silences the minimum advice, because the pool's floor has
// to hold it. That is the operator's report: a weekly job at 7.3 GB with everything
// else under 2 GB. The notice names the job and says what the rest would fit in, and
// proposes nothing, because lowering the minimum first is what gets that job killed.
func TestOneHeavyJobHoldingAPoolsFloorIsNamedAndNothingIsProposed(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	for i := 0; i < 2; i++ {
		addMeasuredJob(t, h, pool, i, "weekly-release", 7300)
	}
	// The per-half evidence has to cover the heavy job too, or the advice waits.
	seedPairs(h, pool, 5800, 1500)
	if p := h.problemOrNil("pool.minimum_overcharges"); p != nil {
		t.Fatalf("the floor is sized for the weekly job and must not be lowered: %+v", p)
	}
	p := h.problemOrNil("pool.minimum_held_by_job")
	if p == nil || p.TargetID != pool.ID {
		t.Fatalf("problem = %+v; want one naming the job that holds the floor", p)
	}
	if p.Remedy != nil {
		t.Errorf("a remedy was proposed that would get the heavy job killed: %+v", p.Remedy)
	}
	for _, want := range []string{"weekly-release", "7.1 GB", "instead of"} {
		if !strings.Contains(p.Title+" "+p.Detail, want) {
			t.Errorf("the notice must mention %q:\n%s\n%s", want, p.Title, p.Detail)
		}
	}
	if !strings.Contains(p.Fix, "size_routing") || !strings.Contains(p.Fix, "pool of its own") {
		t.Errorf("the notice must name the ways to move the job:\n%s", p.Fix)
	}
}

// Without the heavy job the ordinary notice is raised, and with only heavy jobs
// there is nothing to move: neither is the held-by-job notice.
func TestTheHeldByJobNoticeNeedsOrdinaryJobsToBeHeld(t *testing.T) {
	h, _ := minimumFleet(t, 2048, 25, false)
	if p := h.problemOrNil("pool.minimum_held_by_job"); p != nil {
		t.Errorf("named a job in a pool with none holding it: %+v", p)
	}
	h, pool := minimumFleet(t, 7300, 25, false)
	addMeasuredJob(t, h, pool, 1, "weekly-release", 7400)
	if p := h.problemOrNil("pool.minimum_held_by_job"); p != nil {
		t.Errorf("named a job in a pool whose jobs are all heavy: %+v", p)
	}
}

// The problems list is diffed by its bytes, and a notice that stands is not to be
// republished every pass. A count that moves with every heartbeat or every completed
// job -- samples taken, jobs run -- in the text of advice that stands made every pass
// a full problems.updated frame to every open browser. The text states the bar the
// evidence cleared, not the live figures.
func TestStandingAdviceReadsTheSameWhileTheEvidenceGrows(t *testing.T) {
	h, pool := minimumFleet(t, 2048, 25, false)
	first := h.problemOrNil("pool.minimum_overcharges")
	if first == nil {
		t.Fatal("the fixture must be advised")
	}
	// More jobs finish and more samples arrive.
	for i := 0; i < 3; i++ {
		addMeasuredJob(t, h, pool, 100+i, "build", 2048)
	}
	h.c.pairMu.Lock()
	more := h.c.pairs[pool.ID]
	h.c.pairs[pool.ID] = append(more, more[len(more)-1])
	h.c.pairMu.Unlock()
	second := h.problemOrNil("pool.minimum_overcharges")
	if second == nil || first.Detail != second.Detail || first.Title != second.Title {
		t.Errorf("standing advice changed with its evidence:\n%s\n%s", first.Detail, func() string {
			if second == nil {
				return "(gone)"
			}
			return second.Detail
		}())
	}
}

// The gain is counted in runners, and a smaller runner holds more of them, which is no use to a
// job that needed the memory. The host's runner size is never proposed smaller than what
// a week of the pool's jobs used, with the headroom every floor here carries.
func TestAHostSizeIsNotProposedThatTheJobsOfAPoolThatTakesItWouldNotFitIn(t *testing.T) {
	// A runner here is 16 GB on a 32 GB host set to three slots; the size that gives it
	// its slots is about 10 GB.
	big := func(p *store.RunnerProfile) { p.Standard = store.RunnerStandard{CPUs: 2, MemoryMB: 16384} }

	h, host, pool := capacityFleet(t, big)
	waiting(h, pool)
	for i := 0; i < 5; i++ {
		addMeasuredJob(t, h, pool, i, "build", 12000)
	}
	p := h.problemOrNil("host.slots_below_capacity")
	if p == nil {
		t.Fatalf("the squeeze is real and the host should still be told (slots %d of %d)", host.Slots(), host.Capacity)
	}
	if p.Remedy != nil {
		t.Errorf("a size was proposed that the pool's 12 GB jobs would not fit in: %+v", p.Remedy)
	}
	if !strings.Contains(p.Detail, "used up to") {
		t.Errorf("what stands in the way must be said: %s", p.Detail)
	}

	h, _, pool = capacityFleet(t, big)
	waiting(h, pool)
	for i := 0; i < 5; i++ {
		addMeasuredJob(t, h, pool, i, "build", 3000)
	}
	if p := h.problemOrNil("host.slots_below_capacity"); p == nil || p.Remedy == nil {
		t.Errorf("jobs that fit in the smaller runner must not hold the advice back: %+v", p)
	}
}
