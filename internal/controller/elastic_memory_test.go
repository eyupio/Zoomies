package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

const mb = int64(1) << 20

// valvePool is a pool of 4 GB runners with the memory valve in the given mode.
func (h *harness) valvePool(p *store.Pool, mode store.MemoryBurstMode, spillMB int64) {
	h.t.Helper()
	p.Resources = store.Resources{MemoryMB: 4096}
	p.MemoryBurst = store.MemoryBurstPolicy{Mode: mode, SpillMB: spillMB}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		h.t.Fatal(err)
	}
}

// valveRunner seeds a runner created with 4 GB of its pool's own.
func (h *harness) valveRunner(p *store.Pool, host *store.Host, state store.RunnerState) *store.Runner {
	h.t.Helper()
	r := h.runnerRow(p, host, state)
	r.AllocatedMemoryMB = 4096
	r.AllocationSource = store.AllocationFromPool
	if err := h.st.UpdateRunner(h.ctx, r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

// valveReport is what the agent says of a runner the valve is watching.
func valveReport(r *store.Runner, now time.Time, v backend.MemoryValveSample) agent.RunnerReport {
	at := now
	return agent.RunnerReport{RunnerID: r.ID, State: r.State, Stats: backend.Stats{SampledAt: &at, MemoryValve: &v}}
}

func memoryBeat(h *harness, host *store.Host, reports ...agent.RunnerReport) *agent.ElasticMemoryDirective {
	h.t.Helper()
	resp, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{
		Features: []string{agent.FeatureElasticMemory}, Runners: reports,
	})
	if err != nil {
		h.t.Fatalf("Heartbeat: %v", err)
	}
	return resp.ElasticMemory
}

func ruleFor(d *agent.ElasticMemoryDirective, id string) (agent.ElasticMemoryRunner, bool) {
	if d == nil {
		return agent.ElasticMemoryRunner{}, false
	}
	for _, r := range d.Runners {
		if r.RunnerID == id {
			return r, true
		}
	}
	return agent.ElasticMemoryRunner{}, false
}

// A fleet that has not turned the valve on is sent nothing: an upgrade changes
// no runner's limit and costs no heartbeat a byte.
func TestAHostWhosePoolsHaveNotTurnedTheValveOnIsSentNoRules(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valveRunner(pool, host, store.RunnerBusy)

	if got := memoryBeat(h, host); got != nil {
		t.Fatalf("directive = %+v, want none for a pool with the valve off", got)
	}
	if s := h.c.memoryState(host.ID); !s.At.IsZero() {
		t.Fatalf("a host the valve has nothing to do on has state %+v", s)
	}
}

// What the agent is given: the ceiling of each runner, the floor the host keeps,
// and how much the host may lend in all.
func TestAPoolWithTheValveOnIsSentItsRunnersCeilingsAndTheHostsCapacity(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 2048)
	busy := h.valveRunner(pool, host, store.RunnerBusy)
	idle := h.valveRunner(pool, host, store.RunnerIdle)

	got := memoryBeat(h, host)
	if got == nil {
		t.Fatal("no directive for a pool with the valve on")
	}
	rule, ok := ruleFor(got, busy.ID)
	if !ok || rule.Mode != store.MemoryBurstAutomatic || rule.GuaranteeMB != 4096 || rule.CeilingMB != 6144 || rule.SpillMB != 2048 {
		t.Fatalf("rule = %+v, want automatic, guaranteed 4096, ceiling 6144 (half as much again) and 2048 of swap", rule)
	}
	if _, ok := ruleFor(got, idle.ID); !ok {
		t.Fatal("an idle runner waiting for a job has no rule, though it may be handed one any moment")
	}

	// 64 GB machine, less its floor, less the busy runner and one idle guarantee.
	host, _ = h.st.GetHost(h.ctx, host.ID)
	floor := host.MemoryReserve() + host.MemoryMB/20
	want := host.MemoryMB - floor - 4096 - 4096
	if got.FloorMB != floor || got.CapacityMB != want {
		t.Fatalf("capacity %d and floor %d MB, want %d and %d", got.CapacityMB, got.FloorMB, want, floor)
	}
}

func TestAnObservingPoolIsSentRulesToo(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstObserve, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)

	rule, ok := ruleFor(memoryBeat(h, host), r.ID)
	if !ok || rule.Mode != store.MemoryBurstObserve {
		t.Fatalf("rule = %+v, %v; want the runner watched in observe mode", rule, ok)
	}
}

// A runner still being created has no container to watch and nothing to lend to.
func TestARunnerThatHasNoContainerYetIsNotWatched(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	starting := h.valveRunner(pool, host, store.RunnerProvisioning)
	busy := h.valveRunner(pool, host, store.RunnerBusy)

	got := memoryBeat(h, host)
	if _, ok := ruleFor(got, starting.ID); ok {
		t.Fatal("a provisioning runner was given a rule")
	}
	if _, ok := ruleFor(got, busy.ID); !ok {
		t.Fatal("a busy runner was not")
	}
}

// An agent that does not say it can lend is sent nothing it would ignore, and the
// decision is counted as the unsupported-agent one so an operator reading the
// metrics sees why a pool said automatic and nothing was lent.
func TestAnAgentThatCannotLendMemoryIsSentNothingAndTheMetricSaysSo(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	h.valveRunner(pool, host, store.RunnerBusy)

	resp, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ElasticMemory != nil {
		t.Fatalf("directive = %+v for an agent that cannot carry it out", resp.ElasticMemory)
	}
	labels := map[string]string{"pool": pool.Name, "mode": "automatic", "outcome": "unsupported_agent"}
	if n, _ := gatherValue(t, h.c, "zoomies_elastic_memory_decisions_total", labels); n != 1 {
		t.Fatalf("unsupported_agent decisions = %v, want 1", n)
	}
}

// A loan is written on the runner's row, where the placement ledger reads it, and
// the live UI is told: the badge on the Runners page must not wait for a reload.
func TestALoanIsWrittenOnTheRunnerAndAnnouncedToTheLiveUI(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)
	sub := h.listen(events.KindRunnerUpdated)
	now := h.c.Now()

	memoryBeat(h, host, valveReport(r, now, backend.MemoryValveSample{
		Mode: "automatic", Code: "raised", Reason: "raised the limit from 4096 to 5120 MB",
		LentBytes: 1024 * mb, Raises: 1,
	}))

	got, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil || got.LentMemoryMB != 1024 {
		t.Fatalf("runner lent %d MB, %v; want 1024", got.LentMemoryMB, err)
	}
	frame := nextOfKind(t, sub, events.KindRunnerUpdated)
	mem, _ := frame["memory_resource"].(map[string]any)
	if frame["id"] != r.ID || mem == nil || mem["state"] != "lent" || mem["lent_mb"] != float64(1024) || mem["current_mb"] != float64(5120) || mem["ceiling_mb"] != float64(6144) {
		t.Fatalf("runner.updated = %v, want a memory_resource that says 1024 MB lent of a 6144 MB ceiling", frame)
	}

	// The same report again changes nothing a person can see.
	memoryBeat(h, host, valveReport(r, now.Add(time.Second), backend.MemoryValveSample{
		Mode: "automatic", Code: "raised", Reason: "raised the limit from 4096 to 5120 MB", LentBytes: 1024 * mb, Raises: 1,
	}))
	nothingFor(t, sub)
}

// A limit only goes up while a runner lives, so a smaller figure is an older
// report overtaken by a newer one. Writing it would hand the runner's memory to
// the next placement as room.
func TestALoanIsNeverWrittenDownWardsByAnOlderReport(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)
	now := h.c.Now()

	memoryBeat(h, host, valveReport(r, now, backend.MemoryValveSample{Mode: "automatic", Code: "raised", LentBytes: 1024 * mb}))
	memoryBeat(h, host, valveReport(r, now, backend.MemoryValveSample{Mode: "automatic", Code: "raised", LentBytes: 256 * mb}))
	if got, _ := h.st.GetRunner(h.ctx, r.ID); got.LentMemoryMB != 1024 {
		t.Fatalf("lent = %d MB after an older report, want it left at 1024", got.LentMemoryMB)
	}
	memoryBeat(h, host, valveReport(r, now, backend.MemoryValveSample{Mode: "automatic", Code: "raised", LentBytes: 1536 * mb}))
	if got, _ := h.st.GetRunner(h.ctx, r.ID); got.LentMemoryMB != 1536 {
		t.Fatalf("lent = %d MB after a larger one, want 1536", got.LentMemoryMB)
	}
}

// What is lent comes off the host's pool at once, in the same heartbeat that
// carried the report, and never comes back while the runner lives.
func TestWhatIsLentComesOffTheHostsPoolInTheSameHeartbeat(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)
	now := h.c.Now()

	first := memoryBeat(h, host)
	before := h.c.memoryState(host.ID)
	second := memoryBeat(h, host, valveReport(r, now, backend.MemoryValveSample{Mode: "automatic", Code: "raised", LentBytes: 2048 * mb}))
	after := h.c.memoryState(host.ID)

	if after.Pool.LentMB != 2048 || before.Pool.LentMB != 0 {
		t.Fatalf("lent %d then %d MB, want 0 then 2048", before.Pool.LentMB, after.Pool.LentMB)
	}
	if after.Pool.PoolMB != before.Pool.PoolMB-2048 {
		t.Fatalf("pool %d then %d MB, want 2048 less once lent", before.Pool.PoolMB, after.Pool.PoolMB)
	}
	// The capacity the agent compares its own running total with is the same,
	// loans included.
	if first.CapacityMB != second.CapacityMB {
		t.Fatalf("capacity %d then %d, want the total to stay what the host can lend in all", first.CapacityMB, second.CapacityMB)
	}
}

// The valve is what observe mode produces evidence about, so each decision is
// counted, and a runner that came near a limit is counted once however long it
// stays there.
func TestObserveModeCountsWhatItWouldHaveDone(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstObserve, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)
	now := h.c.Now()
	would := backend.MemoryValveSample{Mode: "observe", Code: "raised", WouldLendBytes: 1024 * mb, NearLimit: true}

	memoryBeat(h, host, valveReport(r, now, would))
	memoryBeat(h, host, valveReport(r, now.Add(time.Second), would))

	labels := map[string]string{"pool": pool.Name, "mode": "observe", "outcome": "raised"}
	if n, _ := gatherValue(t, h.c, "zoomies_elastic_memory_decisions_total", labels); n != 2 {
		t.Errorf("raised decisions = %v, want one per heartbeat", n)
	}
	if n, _ := gatherValue(t, h.c, "zoomies_elastic_memory_near_limit_total", map[string]string{"pool": pool.Name, "mode": "observe"}); n != 1 {
		t.Errorf("near-limit count = %v, want the runner counted once", n)
	}
}

func problemsWith(t *testing.T, h *harness, code string) []Problem {
	t.Helper()
	all, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []Problem
	for _, p := range all {
		if p.Code == code {
			out = append(out, p)
		}
	}
	return out
}

// A host that ran out of memory to lend is a standing problem for a while after
// the refusal, and clears itself: a refusal is a fact about a moment, but one an
// operator can only act on if it is still on the list when they open it.
func TestAHostThatRanOutOfMemoryToLendSaysSoAndThenClears(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)

	memoryBeat(h, host, valveReport(r, h.c.Now(), backend.MemoryValveSample{Mode: "automatic", Code: "pool_empty", Reason: "the host has no spare memory left to lend"}))
	got := problemsWith(t, h, "host.memory_pool_exhausted")
	if len(got) != 1 || got[0].TargetKind != "host" || got[0].TargetID != host.ID || !strings.Contains(got[0].Detail, "no spare memory left to lend") {
		t.Fatalf("problems = %+v, want one about the host", got)
	}

	h.advance(memoryBlockedFor + time.Minute)
	if got := problemsWith(t, h, "host.memory_pool_exhausted"); len(got) != 0 {
		t.Fatalf("problems = %+v after the window, want it cleared", got)
	}
}

func TestAPoolWhoseRunnersReachedTheirCeilingSaysSo(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)

	memoryBeat(h, host, valveReport(r, h.c.Now(), backend.MemoryValveSample{Mode: "automatic", Code: "at_ceiling", LentBytes: 2048 * mb}))
	got := problemsWith(t, h, "pool.memory_ceiling_reached")
	if len(got) != 1 || got[0].TargetID != pool.ID || !strings.Contains(got[0].Fix, "--memory-burst-max") {
		t.Fatalf("problems = %+v, want one about the pool that says how to raise the ceiling", got)
	}
}

// A pool set to automatic on a host whose agent cannot do it is a pool that is
// quietly not automatic. It is said where the pool is edited, with the hosts
// named -- and for observe too, because an agent that does not carry the valve
// observes nothing.
func TestAPoolOnAnAgentThatCannotLendMemoryIsToldSo(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstObserve, 0)
	host.Features = store.StringSlice{agent.FeatureElasticCPU}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}

	got := problemsWith(t, h, "pool.elastic_memory_unsupported")
	if len(got) != 1 || !strings.Contains(got[0].Title, "watch memory") || !strings.Contains(got[0].Detail, host.Name) {
		t.Fatalf("problems = %+v, want one that names %s", got, host.Name)
	}

	host.Features = store.StringSlice{agent.FeatureElasticMemory}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	if got := problemsWith(t, h, "pool.elastic_memory_unsupported"); len(got) != 0 {
		t.Fatalf("problems = %+v for an agent that can, want none", got)
	}
}

// What the Runners page badge reads: the runner's memory_resource, in numbers.
func TestARunnersMemoryResourceSaysWhatItWasGivenAndWhatItMayHave(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 2048)
	r := h.valveRunner(pool, host, store.RunnerBusy)

	// Nothing has happened: the valve is on and watching.
	view := h.c.runnerView(h.ctx, r)
	if view.MemoryResource == nil || view.MemoryResource.State != MemoryWatching || view.MemoryResource.CeilingMB != 6144 || view.MemoryResource.GuaranteedMB != 4096 {
		t.Fatalf("memory_resource = %+v, want a watching runner with a 6144 MB ceiling", view.MemoryResource)
	}

	sample, _ := json.Marshal(backend.Stats{MemoryValve: &backend.MemoryValveSample{
		Mode: "automatic", Code: "spilled", Reason: "allowed 2048 MB of swap", LentBytes: 1024 * mb, SpillBytes: 2048 * mb, Raises: 2, NearLimit: true,
	}})
	if err := h.st.SetRunnerResourceSample(h.ctx, r.ID, 0, 0, sample); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRunnerLentMemory(h.ctx, r.ID, 1024); err != nil {
		t.Fatal(err)
	}
	r, _ = h.st.GetRunner(h.ctx, r.ID)
	got := h.c.runnerView(h.ctx, r).MemoryResource
	if got == nil || got.State != MemorySpilled || got.LentMB != 1024 || got.CurrentMB != 5120 || got.SpillMB != 2048 || got.SpillAllowedMB != 2048 || !got.NearLimit || got.Raises != 2 || got.Reason == "" {
		t.Fatalf("memory_resource = %+v, want spilled, with 1024 MB lent and 2048 MB of swap", got)
	}

	// A runner whose pool has no valve and holds no loan has nothing to say.
	pool.MemoryBurst = store.MemoryBurstPolicy{}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	other := h.valveRunner(pool, host, store.RunnerBusy)
	if got := h.c.runnerView(h.ctx, other).MemoryResource; got != nil {
		t.Fatalf("memory_resource = %+v for a pool with the valve off, want none", got)
	}
	// But a loan that was made stands and is told, even once the valve is off.
	if got := h.c.runnerView(h.ctx, r).MemoryResource; got == nil || got.State != MemorySpilled || got.Mode != "automatic" {
		t.Fatalf("memory_resource = %+v for a runner holding a loan, want it reported", got)
	}
}

// What the host's card shows: the pool, and the promises it was taken after.
func TestAHostsViewCarriesItsMemoryPool(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)
	memoryBeat(h, host, valveReport(r, h.c.Now(), backend.MemoryValveSample{Mode: "automatic", Code: "pool_empty", LentBytes: 512 * mb}))

	host, _ = h.st.GetHost(h.ctx, host.ID)
	view := h.c.HostView(host)
	pv := view.MemoryPool
	if pv == nil || !pv.Supported || pv.LentMB != 512 || pv.CommittedMB != 4096 || pv.Enforcing != 1 || pv.Binding != "ledger" || pv.ShortCode != "pool_empty" || pv.ShortAt == nil {
		t.Fatalf("memory_pool = %+v, want the pool with 512 MB lent, 4096 committed and the refusal remembered", pv)
	}

	other := h.host("quiet")
	if got := h.c.HostView(other).MemoryPool; got != nil {
		t.Fatalf("memory_pool = %+v for a host the valve has nothing to do on, want none", got)
	}
}

// The folders a runner was given in memory are written when its create task is
// built, from the same spec the agent mounts from, and read back as they were.
func TestARunnersInMemoryFoldersAreRecordedWhenItsCreateTaskIsBuilt(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.Resources = store.Resources{MemoryMB: 16384}
	pool.Tmpfs = store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}, Tmp: store.TmpfsMount{Enabled: true, SizeMB: 2048}}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	host.Features = store.StringSlice{agent.FeatureTmpfs}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	host, _ = h.st.GetHost(h.ctx, host.ID)
	r := h.valveRunner(pool, host, store.RunnerProvisioning)
	spec := backend.Spec{Resources: store.Resources{MemoryMB: 16384}, ResourcesSource: store.AllocationFromPool, Tmpfs: pool.Tmpfs}

	h.c.recordScratch(h.ctx, r, pool, host, spec)

	got, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil || got.Scratch.InMemory() != 2 {
		t.Fatalf("scratch = %+v, %v; want the work folder and /tmp in memory", got.Scratch, err)
	}
	view := h.c.runnerView(h.ctx, got).Scratch
	if view == nil || view.InMemory != 2 || len(view.Folders) != 2 || view.Folders[0].Path != backend.RunnerWorkMount || view.Folders[1].Path != backend.RunnerTmpMount {
		t.Fatalf("scratch view = %+v, want the two folders with their paths", view)
	}

	// An agent too old to mount a tmpfs starts the runner on disk, and the row says
	// that rather than what the pool asked for.
	host.Features = nil
	r2 := h.valveRunner(pool, host, store.RunnerProvisioning)
	h.c.recordScratch(h.ctx, r2, pool, host, spec)
	got, _ = h.st.GetRunner(h.ctx, r2.ID)
	if got.Scratch.InMemory() != 0 || len(got.Scratch.Folders) != 2 || got.Scratch.Folders[0].Why != store.ScratchUnsupported {
		t.Fatalf("scratch = %+v, want both folders on disk for an agent too old to keep them in memory", got.Scratch)
	}
	view = h.c.runnerView(h.ctx, got).Scratch
	if view == nil || view.Folders[0].InMemory || !strings.Contains(view.Folders[0].Note, "too old") {
		t.Fatalf("scratch view = %+v, want a note that says why", view)
	}

	// A pool that keeps nothing in memory records nothing.
	pool.Tmpfs = store.TmpfsConfig{}
	r3 := h.valveRunner(pool, host, store.RunnerProvisioning)
	h.c.recordScratch(h.ctx, r3, pool, host, backend.Spec{})
	if got, _ := h.st.GetRunner(h.ctx, r3.ID); got.Scratch.Any() {
		t.Fatalf("scratch = %+v for a pool with none", got.Scratch)
	}
}

// "Why did the valve not save this job?" is the question a kill on a pool that
// has it raises, and the kill's own sentence is where it is answered.
func TestAKillSaysWhatTheMemoryValveHadDoneForTheRunner(t *testing.T) {
	sample := func(v backend.MemoryValveSample) json.RawMessage {
		raw, _ := json.Marshal(backend.Stats{MemoryValve: &v})
		return raw
	}
	pool := &store.Pool{Resources: store.Resources{MemoryMB: 4096}}
	runner := func(lentMB int64, v *backend.MemoryValveSample) *store.Runner {
		r := &store.Runner{AllocatedMemoryMB: 4096, AllocationSource: store.AllocationFromPool, LentMemoryMB: lentMB}
		if v != nil {
			r.ResourceSample = sample(*v)
		}
		return r
	}

	if got := memoryValveEpilogue(pool, runner(0, nil)); got != "" {
		t.Fatalf("epilogue = %q for a runner the valve had nothing to do with, want none", got)
	}
	got := memoryValveEpilogue(pool, runner(1536, &backend.MemoryValveSample{Mode: "automatic", Code: "at_ceiling", Reason: "it holds 6144 MB, the most this runner may have"}))
	for _, want := range []string{"lent it 1.5 GB", "on top of its 4 GB", "held 5.5 GB", "the most this runner may have"} {
		if !strings.Contains(got, want) {
			t.Errorf("epilogue = %q, want it to say %q", got, want)
		}
	}
	got = memoryValveEpilogue(pool, runner(0, &backend.MemoryValveSample{Mode: "automatic", Code: "pool_empty", Reason: "the host has no spare memory left to lend"}))
	if !strings.Contains(got, "lent it nothing: the host has no spare memory left to lend") {
		t.Errorf("epilogue = %q, want the reason it could not lend", got)
	}
	got = memoryValveEpilogue(pool, runner(0, &backend.MemoryValveSample{Mode: "observe", Code: "raised", WouldLendBytes: 2048 * mb}))
	if !strings.Contains(got, "only observing") || !strings.Contains(got, "up to 2 GB more") {
		t.Errorf("epilogue = %q, want an observing pool told what it would have done", got)
	}

	// A docker-in-docker pair with a typed size holds that size in each of its two
	// containers, and the loan is the pair's: the sentence adds them up as the
	// agent does, not from the one figure on the row.
	pair := &store.Pool{DockerMode: store.DockerDinD, Resources: store.Resources{MemoryMB: 4096}}
	got = memoryValveEpilogue(pair, runner(1024, nil))
	for _, want := range []string{"lent it 1 GB", "on top of its 8 GB", "held 9 GB"} {
		if !strings.Contains(got, want) {
			t.Errorf("epilogue = %q for a typed docker-in-docker pair, want it to say %q", got, want)
		}
	}
}

// What a runner was launched with is on its row, and that is the guarantee its
// rule, its capacity and its view are worked out from. The pool as it is now
// says what a runner of it would cost; it is not what this one holds once the
// pool or the host has been edited, and the raw pool a view is rendered with
// cannot say it at all.
func TestAGuaranteeIsWhatTheRunnerWasLaunchedWithAndNotWhatItsPoolWouldChargeNow(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	// A pool that leaves its size to its hosts, and a runner that was given one
	// slot of 7782 MB when it started.
	pool.Resources = store.Resources{}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := h.valveRunner(pool, host, store.RunnerBusy)
	r.AllocatedMemoryMB, r.AllocationSource = 7782, store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, r); err != nil {
		t.Fatal(err)
	}

	got := memoryBeat(h, host)
	rule, ok := ruleFor(got, r.ID)
	if !ok || rule.GuaranteeMB != 7782 || rule.CeilingMB != 11673 {
		t.Fatalf("rule = %+v, %v; want guaranteed 7782 and a ceiling of half as much again, 11673", rule, ok)
	}
	host, _ = h.st.GetHost(h.ctx, host.ID)
	if want := host.MemoryMB - (host.MemoryReserve() + host.MemoryMB/20) - 7782; got.CapacityMB != want {
		t.Fatalf("capacity = %d, want %d: the host's books carry what the runner holds", got.CapacityMB, want)
	}

	// The view is rendered from the raw pool, with no fleet standard to size it by.
	row, _ := h.st.GetRunner(h.ctx, r.ID)
	raw, _ := h.st.GetPool(h.ctx, pool.ID)
	view := memoryResourceView(row, raw, host)
	if view == nil || view.GuaranteedMB != 7782 || view.CurrentMB != 7782 || view.CeilingMB != 11673 {
		t.Fatalf("view = %+v, want it to say it was created with 7782 MB", view)
	}
}

// An observing pool's runners draw on a pool their own virtual loans drain, so a
// runner that "would have been refused" is evidence about the pool, not a host
// that ran out of memory to lend and a runner that "was not given it" -- nothing
// was ever lent. The decision is still counted, which is where the evidence is.
func TestAnObservingPoolThatWouldHaveBeenRefusedDoesNotMakeTheHostAProblem(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstObserve, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)

	memoryBeat(h, host, valveReport(r, h.c.Now(), backend.MemoryValveSample{Mode: "observe", Code: "pool_empty", Reason: "the host has no spare memory left to lend"}))
	if got := problemsWith(t, h, "host.memory_pool_exhausted"); len(got) != 0 {
		t.Fatalf("problems = %+v for a runner that was only ever observed, want none", got)
	}
	labels := map[string]string{"pool": pool.Name, "mode": "observe", "outcome": "pool_empty"}
	if n, _ := gatherValue(t, h.c, "zoomies_elastic_memory_decisions_total", labels); n != 1 {
		t.Fatalf("pool_empty decisions = %v, want the evidence counted", n)
	}
}

// A refusal is held for its window so that an operator can open the problem, and
// the runner that met it is usually an ephemeral one that has finished and gone
// by the host's next heartbeat -- which must not be what clears it.
func TestARefusalIsHeldForItsWholeWindowAfterTheRunnerThatMetItIsGone(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)

	memoryBeat(h, host, valveReport(r, h.c.Now(), backend.MemoryValveSample{Mode: "automatic", Code: "pool_empty", Reason: "the host has no spare memory left to lend"}))
	if got := problemsWith(t, h, "host.memory_pool_exhausted"); len(got) != 1 {
		t.Fatalf("problems = %+v, want the refusal raised", got)
	}

	r.State = store.RunnerRemoved
	if err := h.st.UpdateRunner(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	h.advance(30 * time.Second)
	memoryBeat(h, host) // the next heartbeat: no live runner, nothing for the valve to do
	if got := problemsWith(t, h, "host.memory_pool_exhausted"); len(got) != 1 {
		t.Fatalf("problems = %+v thirty seconds on, want the refusal still held", got)
	}
	if s := h.c.memoryState(host.ID); !s.At.IsZero() {
		t.Fatalf("state = %+v, want the plan forgotten and only the refusal kept", s)
	}

	h.advance(memoryBlockedFor)
	memoryBeat(h, host)
	if got := problemsWith(t, h, "host.memory_pool_exhausted"); len(got) != 0 {
		t.Fatalf("problems = %+v after the window, want it cleared", got)
	}
}

// What a host last worked out stands for the hosts there are and are heard from:
// a deleted host, or one that has gone silent, must not keep its last pool in the
// metrics for ever.
func TestAHostThatIsGoneTakesItsMemoryPoolOutOfTheMetrics(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	h.valveRunner(pool, host, store.RunnerBusy)
	memoryBeat(h, host)
	if _, ok := gatherValue(t, h.c, "zoomies_host_memory_pool_bytes", map[string]string{"host": host.ID}); !ok {
		t.Fatal("a host with a pool to lend reports none")
	}

	// Silent, its figures are a heartbeat old at best and are not reported.
	h.advance(10 * time.Minute)
	if v, ok := gatherValue(t, h.c, "zoomies_host_memory_pool_bytes", map[string]string{"host": host.ID}); ok {
		t.Fatalf("a host that has gone silent still reports a pool of %v bytes", v)
	}

	if err := h.c.DeleteHost(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	if v, ok := gatherValue(t, h.c, "zoomies_host_memory_pool_bytes", map[string]string{"host": host.ID}); ok {
		t.Fatalf("a deleted host still reports a pool of %v bytes", v)
	}
	if s := h.c.memoryState(host.ID); !s.At.IsZero() || len(s.CeilingAt) != 0 {
		t.Fatalf("a deleted host kept state %+v", s)
	}
}

// A label is a series for every value it takes, and the outcome is an agent's
// word: one that is not a code the agent defines is "unknown".
func TestADecisionCodeFromAnAgentIsAMetricLabelOnlyIfItIsOneWeDefine(t *testing.T) {
	for _, code := range []string{"healthy", "raised", "spilled", "at_ceiling", "pool_empty", "host_floor", "unmeasured", "unsupported", "failed"} {
		if got := valveOutcome(code); got != code {
			t.Errorf("valveOutcome(%q) = %q, want it kept", code, got)
		}
	}
	for _, code := range []string{"", "RAISED", "raised ", "rm -rf /", strings.Repeat("x", 500)} {
		if got := valveOutcome(code); got != "unknown" {
			t.Errorf("valveOutcome(%q) = %q, want unknown", code, got)
		}
	}
}
