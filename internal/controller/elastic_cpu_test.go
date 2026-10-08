package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

func TestElasticCPUDemandUsesBusyCPUOrNewCgroupThrottling(t *testing.T) {
	now := time.Now()
	previous, _ := json.Marshal(backend.Stats{CPUThrottling: &backend.CPUThrottling{Periods: 10, ThrottledPeriods: 2}})
	r := &store.Runner{ResourceSample: previous}

	busy := backend.Stats{SampledAt: &now, CPUPercent: 175}
	if !elasticCPUDemanding(r, busy, 2, now) {
		t.Fatal("a runner using at least 80% of its 2 CPU guarantee was not demanding")
	}
	throttled := backend.Stats{SampledAt: &now, CPUPercent: 20, CPUThrottling: &backend.CPUThrottling{Periods: 12, ThrottledPeriods: 3}}
	if !elasticCPUDemanding(r, throttled, 2, now) {
		t.Fatal("new cgroup throttling was not treated as CPU demand")
	}
	stale := now.Add(-store.HostUsageMaxAge - time.Second)
	if elasticCPUDemanding(r, backend.Stats{SampledAt: &stale, CPUPercent: 200}, 2, now) {
		t.Fatal("a stale sample requested elastic CPU")
	}
}

func TestElasticCPUControllerLendsOnlyOnAFreshCalmCapableHost(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	runner := h.runnerRow(pool, host, store.RunnerBusy)
	runner.AllocatedCPUs = 3.87
	runner.AllocationSource = store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	now := h.c.Now()
	cpu := 20.0
	host.Usage = store.HostUsage{CPUPercent: &cpu, SampledAt: now}
	sampled := now

	directives := h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
		Features: []string{agent.FeatureElasticCPU},
		Runners: []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{
			SampledAt: &sampled, CPUPercent: 400,
		}}},
	}, now)
	if len(directives) != 1 || directives[0].RunnerID != runner.ID || directives[0].BaseCPUs != runner.AllocatedCPUs || directives[0].TargetCPUs <= directives[0].BaseCPUs {
		t.Fatalf("directives = %+v, want one safe CPU boost", directives)
	}

	cpu = 90
	if got := h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
		Features: []string{agent.FeatureElasticCPU},
		Runners:  []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{SampledAt: &sampled, CPUPercent: 400}}},
	}, now); len(got) != 0 {
		t.Fatalf("busy host received elastic CPU: %+v", got)
	}
}

func TestCPUResourceStatusSaysWhatTheNumbersMean(t *testing.T) {
	h := &store.Host{CPUs: 8, Capacity: 2}
	p := &store.Pool{CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}}
	sample, _ := json.Marshal(backend.Stats{CPUAllocationFactor: 2})
	r := &store.Runner{AllocatedCPUs: 2, ResourceSample: sample}

	got := cpuResourceView(r, p, h)
	if got == nil || got.State != "maximum_zoomies" || got.Label != "Squirrel spotted, maximum zoomies" || got.CurrentCPUs != 4 || got.GuaranteedCPUs != 2 {
		t.Fatalf("resource status = %+v, want a truthful maximum-zoomies label over 2 -> 4 CPUs", got)
	}

	sample, _ = json.Marshal(backend.Stats{CPUAllocationFactor: .5})
	r.ResourceSample = sample
	p.CPUBurst.Mode = store.CPUBurstOff
	got = cpuResourceView(r, p, h)
	if got.State != "throttled" || got.Label != "Leash tightened, host under pressure" || got.CurrentCPUs != 1 {
		t.Fatalf("resource status = %+v, want a host-pressure throttle at 1 CPU", got)
	}

	p.DockerMode = store.DockerDinD
	r.AllocationSource = store.AllocationFromPool
	got = cpuResourceView(r, p, h)
	if got.GuaranteedCPUs != 4 || got.CurrentCPUs != 2 {
		t.Fatalf("fixed DinD resource status = %+v, want both 2-CPU containers represented", got)
	}

	// A pool with elasticity off holds its runner at its share, and a runner
	// doing exactly that has a state of its own rather than none: no state at
	// all reads as a quota nobody measured, which a held one is not.
	sample, _ = json.Marshal(backend.Stats{CPUAllocationFactor: 1})
	r.ResourceSample = sample
	got = cpuResourceView(r, p, h)
	if got == nil || got.State != "sit_and_stay" || got.Label != "Sit and stay, CPU held at its share" || got.Reason != "elastic_off" {
		t.Fatalf("resource status = %+v, want a runner held at its share to say so", got)
	}
	if got.CurrentCPUs != got.GuaranteedCPUs || got.CeilingCPUs != got.GuaranteedCPUs {
		t.Fatalf("resource status = %+v, want current and ceiling both at the guarantee", got)
	}
	// A runner nothing has sampled yet says the same: no sample is a factor
	// of one, not an unknown.
	r.ResourceSample = nil
	if got = cpuResourceView(r, p, h); got == nil || got.State != "sit_and_stay" {
		t.Fatalf("resource status = %+v, want sit_and_stay before the first sample", got)
	}
}

// Observe mode is how an operator decides whether to switch a boost on: the
// ratio of burst to everything else. A heartbeat the host was too busy to lend
// on is part of that answer, so it has to be counted -- recording only the
// calm heartbeats reported a boost as available far more often than it was.
func TestABusyHostRecordsThatItCouldNotLend(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstObserve}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	runner := h.runnerRow(pool, host, store.RunnerBusy)
	runner.AllocatedCPUs = 1.87
	runner.AllocationSource = store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	now := h.c.Now()
	busy := 95.0
	host.Usage = store.HostUsage{CPUPercent: &busy, SampledAt: now}
	sampled := now

	h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
		Runners: []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{SampledAt: &sampled, CPUPercent: 187}}},
	}, now)

	labels := map[string]string{"pool": pool.Name, "mode": string(store.CPUBurstObserve), "outcome": "host_busy"}
	if got, _ := gatherValue(t, h.c, "zoomies_elastic_cpu_decisions_total", labels); got != 1 {
		t.Errorf("host_busy decisions = %v, want 1: a heartbeat the host was too busy to lend on must be counted", got)
	}
	if n, sum, _ := gatherHistogram(t, h.c, "zoomies_elastic_cpu_target_factor", map[string]string{"pool": pool.Name, "mode": string(store.CPUBurstObserve)}); n != 1 || sum != 1 {
		t.Errorf("factor histogram = %d samples summing to %v, want one sample of 1.0: the runner got its guarantee", n, sum)
	}

	// Nothing measured is no decision, and is not recorded as one.
	host.Usage = store.HostUsage{}
	h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{}, now)
	if got, _ := gatherValue(t, h.c, "zoomies_elastic_cpu_decisions_total", labels); got != 1 {
		t.Errorf("host_busy decisions = %v after an unmeasured heartbeat, want still 1", got)
	}
}

// The docs' own 8-core host: two runners' worth of guarantee is 1.87 CPUs
// each, and a busy one lent twice that uses 3.75. A host doing exactly that is
// well over 85% busy -- because of the boost. Judged on the raw figure, the
// next heartbeat withdrew the boost, the host fell quiet, and the one after
// lent it again, every other heartbeat for as long as the job ran.
//
// What the line is for is CPU the plan did not lend, and that still stops it:
// the same host at full CPU with the runner back at its guarantee is busy with
// something else, and gets nothing.
func TestABoostBeingUsedDoesNotTripItsOwnGuard(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	runner := h.runnerRow(pool, host, store.RunnerBusy)
	runner.AllocatedCPUs = 1.87
	runner.AllocationSource = store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	host.CPUs, host.ReserveCPUs = 8, 0
	now := h.c.Now()
	sampled := now
	beat := func(hostCPU, runnerCPU, factor float64) []agent.ElasticCPUDirective {
		host.Usage = store.HostUsage{CPUPercent: &hostCPU, SampledAt: now}
		return h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
			Features: []string{agent.FeatureElasticCPU},
			Runners: []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{
				SampledAt: &sampled, CPUPercent: runnerCPU, CPUAllocationFactor: factor,
			}}},
		}, now)
	}

	if got := beat(94, 375, 2); len(got) != 1 || got[0].TargetCPUs <= got[0].BaseCPUs {
		t.Fatalf("a host at 94%% because its runner is using a boost got %+v; want the boost kept, not withdrawn by its own success", got)
	}
	if got := beat(100, 187, 2); len(got) != 0 {
		t.Fatalf("a host at full CPU with its runner back at its guarantee got %+v; that CPU is someone else's, and nothing should be lent", got)
	}
}

// "The next job keeps its room": a compatible job waiting for this host holds
// one runner's share back before any CPU is lent, so a fast job cannot crowd
// the next one off the machine. Deciding that costs a read of the fleet's
// queued jobs on every heartbeat, which is only worth paying when there is a
// runner to lend to -- and has to be paid then.
func TestAQueuedJobKeepsItsRoomFromABoost(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	runner := h.runnerRow(pool, host, store.RunnerBusy)
	runner.AllocatedCPUs = 1.87
	runner.AllocationSource = store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	host.CPUs, host.ReserveCPUs = 8, 0
	now := h.c.Now()
	sampled := now
	calm := 30.0
	host.Usage = store.HostUsage{CPUPercent: &calm, SampledAt: now}
	lent := func() float64 {
		t.Helper()
		got := h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
			Features: []string{agent.FeatureElasticCPU},
			Runners:  []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{SampledAt: &sampled, CPUPercent: 187}}},
		}, now)
		if len(got) != 1 {
			t.Fatalf("directives = %+v, want one boost for a demanding runner on a calm host", got)
		}
		return got[0].TargetCPUs
	}

	alone := lent()
	h.queuedJob(t, pool, pool.Labels)
	withJob := lent()
	if withJob >= alone {
		t.Errorf("a runner was lent %.2f CPUs with a job queued for this host and %.2f without; the queued job's share must be held back", withJob, alone)
	}
}

// A reduced runner of an automatic docker-in-docker pool was given a smaller
// slot, and a slot is shared by the pair rather than given to each half, so
// its guarantee is what the row says -- not twice that.
func TestAReducedAutomaticDinDRunnerIsNotDoubled(t *testing.T) {
	p := &store.Pool{DockerMode: store.DockerDinD, Resources: store.Resources{MinCPUs: 1}}
	r := &store.Runner{AllocatedCPUs: 2.5, AllocationSource: store.AllocationReduced}
	got := cpuResourceView(r, p, nil)
	if got == nil || got.GuaranteedCPUs != 2.5 {
		t.Fatalf("resource view = %+v, want 2.5 guaranteed CPUs", got)
	}
}
