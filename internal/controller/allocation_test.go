package controller

import (
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// measuredHost seeds a host whose agent has measured the machine and whose
// daemon has said it can apply every limit: the host a default allocation is
// computed for.
func (h *harness) measuredHost(name string, cpus int, memoryMB int64, capacity int, limits store.LimitSupport) *store.Host {
	h.t.Helper()
	host := &store.Host{
		Name:     name,
		Capacity: capacity,
		Backends: store.StringSlice{"docker"},
		BackendInfo: store.HostBackends{
			{Kind: store.BackendDocker, Available: true, Version: "27.1.1", Limits: limits},
		},
		Labels:        store.StringMap{},
		OS:            "linux",
		Arch:          "amd64",
		CPUs:          cpus,
		MemoryMB:      memoryMB,
		DiskTotalMB:   500 * 1024,
		DiskFreeMB:    400 * 1024,
		LastHeartbeat: h.c.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		h.t.Fatalf("CreateHost: %v", err)
	}
	return host
}

var enforcesEverything = store.LimitSupport{Known: true, CPU: true, Memory: true, Pids: true}

// A pool that sets no limits used to start runners with no limits at all, so
// eight of them on one host could each take every core. Now such a runner is
// given one slot's share of the host it lands on, the row says where the
// figure came from, and the task the agent receives carries the same share --
// the row and the container must never disagree about what a runner was
// given, because the row is what an operator reads after an OOM kill.
func TestAPoolWithNoLimitsGetsOneSlotsShareOfItsHost(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 1
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	// Eight cores less the half-core floor is 7.5, and four slots of that is
	// 1.875, floored to the hundredth the pool form takes. Memory is the
	// machine less its reserve -- a twentieth of 16 GB, which is above the
	// flat floor -- in four.
	host := h.measuredHost("measured", 8, 16384, 4, enforcesEverything)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	r := h.onlyRunner()
	if r.AllocatedCPUs != 1.87 || r.AllocatedMemoryMB != 3891 || r.AllocationSource != store.AllocationFromHost {
		t.Fatalf("row allocation = %v CPUs, %d MB from %q; want 1.87, 3891 from %q",
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource, store.AllocationFromHost)
	}
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil {
		t.Fatal("the create task carries no spec")
	}
	if task.Spec.Resources.CPUs != 1.87 || task.Spec.Resources.MemoryMB != 3891 || task.Spec.ResourcesSource != store.AllocationFromHost {
		t.Fatalf("task spec = %+v from %q; the agent would apply something other than what the row records",
			task.Spec.Resources, task.Spec.ResourcesSource)
	}
	view := h.c.runnerView(h.ctx, r)
	if view.AllocatedCPUs != 1.87 || view.AllocatedMemoryMB != 3891 || view.AllocationSource != store.AllocationFromHost {
		t.Fatalf("runner view = %v CPUs, %d MB from %q; the Runners page cannot say what the runner was given",
			view.AllocatedCPUs, view.AllocatedMemoryMB, view.AllocationSource)
	}
}

// With defaults switched off the fleet behaves exactly as it did before they
// existed: a pool's own limits are the whole answer, and a pool with none
// starts an unlimited runner whose row says nothing about an allocation.
func TestDefaultLimitsOffLeavesARunnerUnlimited(t *testing.T) {
	h := newHarness(t)
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.DefaultRunnerLimits = false })
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 1
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	host := h.measuredHost("measured", 8, 16384, 4, enforcesEverything)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	r := h.onlyRunner()
	if r.AllocatedCPUs != 0 || r.AllocatedMemoryMB != 0 || r.AllocationSource != "" {
		t.Fatalf("row allocation = %v CPUs, %d MB from %q; want none with defaults off",
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource)
	}
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec.Resources != (store.Resources{}) || task.Spec.ResourcesSource != "" {
		t.Fatalf("task spec = %+v from %q; want no limits with defaults off", task.Spec.Resources, task.Spec.ResourcesSource)
	}
	// And the host counts it among the runners nothing binds, which is the
	// count the CPU hold reads before it means anything.
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnlimitedRunners != 1 || h.c.HostView(got).UnlimitedRunners != 1 {
		t.Fatalf("unlimited runners = %d (view %d), want 1", got.UnlimitedRunners, h.c.HostView(got).UnlimitedRunners)
	}
}

// A pool's own limits win over the default, and the row says they are the
// pool's: an operator who set memory_mb and sees an OOM kill should be sent
// to the pool, not to the host's capacity.
func TestAPoolsOwnLimitsAreRecordedAsThePools(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 1
	pool.Resources = store.Resources{CPUs: 2, MemoryMB: 4096}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	h.measuredHost("measured", 8, 16384, 4, enforcesEverything)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r := h.onlyRunner()
	if r.AllocatedCPUs != 2 || r.AllocatedMemoryMB != 4096 || r.AllocationSource != store.AllocationFromPool {
		t.Fatalf("row allocation = %v CPUs, %d MB from %q; want the pool's 2 and 4096 from %q",
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource, store.AllocationFromPool)
	}
}

// A pool with a minimum runs on a host a little short of its standard size,
// and the runner, the task and the view all say what it was actually given.
// The row is the one place the reduced size is written down: its host is
// charged from it, and an operator reading a slow job needs it.
func TestARunnerOnAShortHostIsGivenWhatItCanSpareAndSaysSo(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 1
	pool.Resources = store.Resources{CPUs: 2, MemoryMB: 32 * 1024, MinCPUs: 1, MinMemoryMB: 8 * 1024}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	// 16 GB less its reserve is well short of 32 GB and well above 8.
	host := h.measuredHost("short", 8, 16384, 1, enforcesEverything)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	r := h.onlyRunner()
	if r.AllocationSource != store.AllocationReduced {
		t.Fatalf("allocation source = %q, want %q", r.AllocationSource, store.AllocationReduced)
	}
	if r.AllocatedCPUs != 2 {
		t.Errorf("CPU = %v, want the standard 2: the host has room for it", r.AllocatedCPUs)
	}
	if r.AllocatedMemoryMB >= 32*1024 || r.AllocatedMemoryMB < 8*1024 {
		t.Errorf("memory = %d MB, want between the 8 GB minimum and the 32 GB standard", r.AllocatedMemoryMB)
	}
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil || task.Spec.Resources.MemoryMB != r.AllocatedMemoryMB {
		t.Fatalf("task spec = %+v; the agent would apply something other than what the row records", task.Spec)
	}
	if task.Spec.Resources.MinCPUs != 0 || task.Spec.Resources.MinMemoryMB != 0 {
		t.Errorf("task spec carries the pool's minimum %+v; the agent is told a size, not a policy", task.Spec.Resources)
	}
}

// The report that found this: an automatic pool, a host with a free slot and
// most of a slot's worth idle, and no runner, because the pool only ever asked
// for a whole share. With a minimum it runs there, and the row, the task and
// the host's charge all carry what it was actually given.
func TestAnAutomaticPoolWithAMinimumRunsOnWhatIsLeftOfAHost(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	// 8 cores and 16 GB over two slots; a fixed pool's runner takes 5 cores
	// and 10 GB of it first, which is more than a slot's share.
	host := h.measuredHost("shared", 8, 16384, 2, enforcesEverything)
	big := h.pool(inst, "big", "big")
	big.MinRunners = 1
	big.Resources = store.Resources{CPUs: 5, MemoryMB: 10 * 1024}
	if err := h.st.UpdatePool(h.ctx, big); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	auto := h.pool(inst, "auto", "auto")
	auto.MinRunners = 1
	auto.Resources = store.Resources{MinCPUs: 1, MinMemoryMB: 2048}
	if err := h.st.UpdatePool(h.ctx, auto); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	runners, _, err := h.st.ListRunners(h.ctx, store.RunnerFilter{PoolIDs: []string{auto.ID}}, store.Page{})
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	if len(runners) != 1 {
		t.Fatalf("the automatic pool has %d runners, want 1 on the host's free slot", len(runners))
	}
	r := runners[0]
	if r.AllocationSource != store.AllocationReduced {
		t.Fatalf("allocation source = %q, want %q", r.AllocationSource, store.AllocationReduced)
	}
	if r.AllocatedMemoryMB < 2048 || r.AllocatedMemoryMB >= 8*1024 {
		t.Errorf("memory = %d MB, want at least the 2 GB minimum and less than a whole 8 GB share", r.AllocatedMemoryMB)
	}
	var task *agent.Task
	for _, tk := range h.tasksFor(host.ID) {
		if tk.Kind == agent.TaskCreateRunner && tk.RunnerID == r.ID {
			task = &tk
		}
	}
	if task == nil || task.Spec == nil || task.Spec.Resources.MemoryMB != r.AllocatedMemoryMB || task.Spec.Resources.CPUs != r.AllocatedCPUs {
		t.Fatalf("task = %+v; the agent would apply something other than what the row records", task)
	}
}

// A fleet minimum above a host's slot share is what a runner of a pool that
// follows it is given, row and task alike: the minimum is the least a runner
// may have, and a thinner share is raised to it rather than handed out. The
// fleet figure is not copied into the pool to get there -- the pool keeps
// following the setting.
func TestAFleetMinimumAboveTheShareIsWhatTheRunnerIsGiven(t *testing.T) {
	h := newHarness(t)
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Runners.MinimumMemoryMB = 6144 })
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 1
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	// The same machine as TestAPoolWithNoLimitsGetsOneSlotsShareOfItsHost: a
	// share of 3891 MB, under the 6 GB the fleet says a runner may not go
	// below.
	host := h.measuredHost("measured", 8, 16384, 4, enforcesEverything)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	r := h.onlyRunner()
	if r.AllocatedMemoryMB != 6144 || r.AllocatedCPUs != 1.87 || r.AllocationSource != store.AllocationFromHost {
		t.Fatalf("row allocation = %v CPUs, %d MB from %q; want the 1.87 CPU share and the 6144 MB minimum from %q",
			r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource, store.AllocationFromHost)
	}
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil || task.Spec.Resources.MemoryMB != 6144 {
		t.Fatalf("task spec = %+v; the agent would apply something other than the row's 6144 MB", task.Spec)
	}
	stored, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if stored.Resources.MinMemoryMB != 0 {
		t.Errorf("the pool's own minimum is %d MB, want 0: it follows the fleet", stored.Resources.MinMemoryMB)
	}
}

// A reduced size is chosen only where a host has little to spare, so a pair given
// it must split it: the ledger charges one slot, and a runner and a daemon each
// handed the whole of that little would hold twice what the host was charged. The
// agent splits only a size it is told came from the host.
func TestAReducedDockerInDockerPairIsSplitAndNotGivenTwiceItsCharge(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	host := h.measuredHost("shared", 8, 16384, 2, enforcesEverything)
	big := h.pool(inst, "big", "big")
	big.MinRunners = 1
	big.Resources = store.Resources{CPUs: 5, MemoryMB: 10 * 1024}
	if err := h.st.UpdatePool(h.ctx, big); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	auto := h.pool(inst, "auto", "auto")
	auto.MinRunners = 1
	auto.DockerMode = store.DockerDinD
	auto.Resources = store.Resources{MinCPUs: 1, MinMemoryMB: 1024}
	if err := h.st.UpdatePool(h.ctx, auto); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	runners, _, err := h.st.ListRunners(h.ctx, store.RunnerFilter{PoolIDs: []string{auto.ID}}, store.Page{})
	if err != nil || len(runners) != 1 {
		t.Fatalf("runners = %d, %v; want the pair on the host's free slot", len(runners), err)
	}
	r := runners[0]
	if r.AllocationSource != store.AllocationReduced {
		t.Fatalf("allocation source = %q, want %q", r.AllocationSource, store.AllocationReduced)
	}
	var task *agent.Task
	for _, tk := range h.tasksFor(host.ID) {
		if tk.Kind == agent.TaskCreateRunner && tk.RunnerID == r.ID {
			task = &tk
		}
	}
	if task == nil || task.Spec == nil {
		t.Fatal("no create task for the pair")
	}
	if task.Spec.ResourcesSource != store.AllocationFromHost {
		t.Fatalf("the agent is told %q; it splits only a size that came from the host", task.Spec.ResourcesSource)
	}
	runnerHalf, daemonHalf := task.Spec.Resources.SplitWithDaemonShares(task.Spec.DaemonShares())
	if got := runnerHalf.MemoryMB + daemonHalf.MemoryMB; got < r.AllocatedMemoryMB-1 || got > r.AllocatedMemoryMB+1 {
		t.Errorf("the two containers hold %d MB between them, but the row charges %d", got, r.AllocatedMemoryMB)
	}

	// A pool that types a figure keeps its source: a typed limit goes to both in full.
	typed := &store.Pool{DockerMode: store.DockerDinD, Resources: store.Resources{CPUs: 2}}
	if got := wireSource(store.AllocationReduced, typed); got != store.AllocationReduced {
		t.Errorf("a pool that types its CPUs is sent %q", got)
	}
}
