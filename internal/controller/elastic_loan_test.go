package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// A runner lent twice its guarantee that uses only a sliver of it used to
// keep the loan for as long as it stayed above 60% of its guarantee -- which a
// build sized to its guarantee always is. The loan is now judged against
// itself, taken back after three unused samples, and not lent again on the
// next heartbeat just because the runner fills its guarantee once more.
func TestElasticCPUReclaimsALoanARunnerDoesNotUse(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	runner := h.runnerRow(pool, host, store.RunnerBusy)
	runner.AllocatedCPUs = 2
	runner.AllocationSource = store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	start := h.c.Now()
	beat := func(i int, factor, percent float64) []agent.ElasticCPUDirective {
		now := start.Add(time.Duration(i) * 30 * time.Second)
		cpu := 20.0
		host.Usage = store.HostUsage{CPUPercent: &cpu, SampledAt: now}
		sampled := now
		return h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
			Features: []string{agent.FeatureElasticCPU},
			Runners: []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{
				SampledAt: &sampled, CPUPercent: percent, CPUAllocationFactor: factor,
			}}},
		}, now)
	}

	if d := beat(0, 1, 195); len(d) != 1 {
		t.Fatalf("a runner filling its guarantee was not lent: %+v", d)
	}
	for i := 1; i < scheduler.LoanReclaimSamples; i++ {
		if d := beat(i, 2, 215); len(d) != 1 {
			t.Fatalf("heartbeat %d: the loan was taken back before %d unused samples: %+v", i, scheduler.LoanReclaimSamples, d)
		}
	}
	if d := beat(scheduler.LoanReclaimSamples, 2, 215); len(d) != 0 {
		t.Fatalf("an unused loan was kept after %d samples: %+v", scheduler.LoanReclaimSamples, d)
	}
	// Back at its guarantee and filling it, as a build sized for it does.
	for i := scheduler.LoanReclaimSamples + 1; i < scheduler.LoanReclaimSamples+5; i++ {
		if d := beat(i, 1, 199); len(d) != 0 {
			t.Fatalf("heartbeat %d: lent again inside the backoff: %+v", i, d)
		}
	}
	// A runner that finishes its job and takes another starts afresh.
	runner.State = store.RunnerIdle
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	beat(20, 1, 0)
	runner.State = store.RunnerBusy
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	if d := beat(21, 1, 195); len(d) != 1 {
		t.Fatalf("a new job on the same runner was held to the last job's backoff: %+v", d)
	}
}

func TestBuildSizingIsForAutomaticPoolsAndTheirCeiling(t *testing.T) {
	off := false
	host := &store.Host{CPUs: 16}
	alloc := host.Allocatable().CPUs
	cases := []struct {
		name string
		pool store.Pool
		guar float64
		want int
	}{
		{"automatic sizes for the host ceiling", store.Pool{Backend: store.BackendDocker, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}}, 3.5, int(alloc)},
		{"a pool ceiling is honoured, in whole cores", store.Pool{Backend: store.BackendPodman, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic, MaxCPUs: 6.5}}, 3.5, 6},
		{"observe lends nothing, so sizes nothing", store.Pool{Backend: store.BackendDocker, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstObserve}}, 3.5, 0},
		{"off sizes nothing", store.Pool{Backend: store.BackendDocker}, 3.5, 0},
		{"turned off on the pool", store.Pool{Backend: store.BackendDocker, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic, SizeForCeiling: &off}}, 3.5, 0},
		{"a fixed size has no loan to grow into", store.Pool{Backend: store.BackendDocker, Resources: store.Resources{CPUs: 2}, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}}, 2, 0},
		{"a ceiling at the guarantee says nothing new", store.Pool{Backend: store.BackendDocker, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic, MaxCPUs: 4}}, 4, 0},
		{"the process backend has no quota", store.Pool{Backend: store.BackendProcess, CPUBurst: store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}}, 3.5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildSizedCPUs(&tc.pool, host, tc.guar); got != tc.want {
				t.Fatalf("sized for %d CPUs, want %d", got, tc.want)
			}
		})
	}
}

// An operator's own value always wins, and JAVA_TOOL_OPTIONS -- which carries
// every other JVM flag -- is added to rather than replaced.
func TestBuildSizingNeverOverridesWhatThePoolSays(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want map[string]string
	}{
		{"nothing set", nil, map[string]string{
			EnvCargoBuildJobs: "8", EnvDotnetProcessors: "8", EnvJavaToolOptions: "-XX:ActiveProcessorCount=8",
		}},
		{"the pool's own values stay", map[string]string{EnvCargoBuildJobs: "2", EnvDotnetProcessors: "3"}, map[string]string{
			EnvCargoBuildJobs: "2", EnvDotnetProcessors: "3", EnvJavaToolOptions: "-XX:ActiveProcessorCount=8",
		}},
		{"JVM flags are appended to", map[string]string{EnvJavaToolOptions: "-Xmx2g"}, map[string]string{
			EnvCargoBuildJobs: "8", EnvDotnetProcessors: "8", EnvJavaToolOptions: "-Xmx2g -XX:ActiveProcessorCount=8",
		}},
		{"a JVM processor count of the pool's own stays", map[string]string{EnvJavaToolOptions: "-XX:ActiveProcessorCount=2"}, map[string]string{
			EnvCargoBuildJobs: "8", EnvDotnetProcessors: "8", EnvJavaToolOptions: "-XX:ActiveProcessorCount=2",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withBuildSizing(tc.env, 8)
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			if _, set := got["GOMAXPROCS"]; set {
				t.Error("GOMAXPROCS was pinned, which switches off Go's own quota tracking")
			}
		})
	}
	if got := withBuildSizing(nil, 0); got != nil {
		t.Fatalf("a runner told nothing got %v", got)
	}
}

// End to end: an automatic pool's create task carries the sizing, and the
// runner row records it so the runner page can say what the job was given.
func TestACreateTaskOfAnAutomaticPoolSizesBuildsForTheCeiling(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "builders")
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic, MaxCPUs: 6}
	pool.Env = map[string]string{EnvJavaToolOptions: "-Xmx1g"}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	host := h.host("vm-1")

	h.deliverJob(jobEvent{Action: "queued", JobID: 11, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil {
		t.Fatal("no create task")
	}
	if task.Spec.Resources.CPUs >= 6 {
		t.Fatalf("the harness host's share is %.2f CPUs, at or above the ceiling this test needs", task.Spec.Resources.CPUs)
	}
	if task.Spec.Env[EnvCargoBuildJobs] != "6" || task.Spec.Env[EnvJavaToolOptions] != "-Xmx1g -XX:ActiveProcessorCount=6" {
		t.Fatalf("create task env = %v", task.Spec.Env)
	}
	r, err := h.st.GetRunner(h.ctx, task.Spec.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if r.SizedForCPUs != 6 {
		t.Fatalf("the runner row says it was sized for %d CPUs, want 6", r.SizedForCPUs)
	}
}

// Codex review: an ephemeral runner goes from busy to removed without ever
// being idle, and its loan memory was forgotten only for a runner seen idle.
// A fleet of short jobs grew the map by one entry per job, for as long as the
// controller ran.
func TestLoanMemoryDoesNotOutliveEphemeralRunners(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	start := h.c.Now()
	for i := range 50 {
		runner := h.runnerRow(pool, host, store.RunnerBusy)
		runner.AllocatedCPUs = 2
		runner.AllocationSource = store.AllocationFromHost
		if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
			t.Fatal(err)
		}
		now := start.Add(time.Duration(i) * 30 * time.Second)
		cpu := 20.0
		host.Usage = store.HostUsage{CPUPercent: &cpu, SampledAt: now}
		sampled := now
		h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
			Features: []string{agent.FeatureElasticCPU},
			Runners: []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{
				SampledAt: &sampled, CPUPercent: 195, CPUAllocationFactor: 1,
			}}},
		}, now)
		// Straight from busy to removed, as an ephemeral runner goes.
		runner.State = store.RunnerRemoved
		if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
			t.Fatal(err)
		}
	}
	h.c.loansMu.Lock()
	n := len(h.c.loans)
	h.c.loansMu.Unlock()
	if n > 1 {
		t.Fatalf("loan memory holds %d entries after 50 ephemeral runners came and went, want at most the last", n)
	}
}

// Codex review: a docker-in-docker pair's loan goes to its busier half, and
// judged on the pair's sum a daemon using its lent cores was taken back.
func TestADinDDaemonUsingItsLoanKeepsIt(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.DockerMode = store.DockerDinD
	pool.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstAutomatic}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	runner := h.runnerRow(pool, host, store.RunnerBusy)
	runner.AllocatedCPUs = 2
	runner.AllocationSource = store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, runner); err != nil {
		t.Fatal(err)
	}
	start := h.c.Now()
	for i := range 6 {
		now := start.Add(time.Duration(i) * 30 * time.Second)
		cpu := 20.0
		host.Usage = store.HostUsage{CPUPercent: &cpu, SampledAt: now}
		sampled := now
		// Lent two cores above a pair guarantee of two: the daemon, on a
		// one-core half, uses 2.3 of its 3; the runner half idles.
		d := h.c.elasticCPUTargets(h.ctx, host, agent.HeartbeatRequest{
			Features: []string{agent.FeatureElasticCPU},
			Runners: []agent.RunnerReport{{RunnerID: runner.ID, Stats: backend.Stats{
				SampledAt: &sampled, CPUPercent: 240, CPUAllocationFactor: 2, BusiestHalfPercent: 230,
			}}},
		}, now)
		if len(d) != 1 {
			t.Fatalf("heartbeat %d: a daemon using its loan lost it", i)
		}
	}
}
