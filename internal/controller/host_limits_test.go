package controller

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// A host given more slots than its machine has cores, or than its memory has
// 2 GB slots, is the shape the owner's report described: eight runners each
// promised a share under a core, and a daemon that stops answering. The
// warning names the machine, the capacity and the share each runner gets, and
// the fix names the largest capacity that would fit.
func TestAnOverprovisionedHostIsNamedWithTheCapacityThatFits(t *testing.T) {
	h := newHarness(t)
	// Four cores less the half-core floor is 3.5, so three slots fit; the
	// memory would take seven. Eight slots is more than either.
	host := h.measuredHost("crowded", 4, 16384, 8, enforcesEverything)

	p := h.problem(t, "host.overprovisioned")
	if p.Severity != config.SeverityWarning || p.TargetKind != "host" || p.TargetID != host.ID {
		t.Fatalf("problem = %+v; want a warning pointing at the host", p)
	}
	for _, want := range []string{"crowded", "capacity 8", "3.5 allocatable CPUs", "4 CPUs", "default share is 0.43 CPUs", "1945 MB"} {
		if !strings.Contains(p.Detail, want) {
			t.Errorf("detail %q does not say %q", p.Detail, want)
		}
	}
	if !strings.Contains(p.Fix, "capacity to 3") || !strings.Contains(p.Fix, host.ID) {
		t.Errorf("fix %q does not name the capacity that fits and where to set it", p.Fix)
	}
	if strings.Contains(p.Fix, "too small") {
		t.Errorf("fix %q calls a machine that fits three runners too small", p.Fix)
	}

	// With defaults off the runners are not merely small: nothing limits
	// them at all, which is the shape that stops Docker answering, and the
	// detail has to say so rather than quote a share nobody is given.
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.DefaultRunnerLimits = false })
	p = h.problem(t, "host.overprovisioned")
	if !strings.Contains(p.Detail, "nothing limits its runners") || strings.Contains(p.Detail, "default share") {
		t.Errorf("with defaults off the detail is %q", p.Detail)
	}
}

// When even one runner would not fit -- under a core, or under 2 GB, after
// the reserve -- lowering the capacity is not the whole answer, and the fix
// says the machine is too small rather than pretending a capacity of one is.
// The named capacity still never goes below one: zero is a cordon, and the
// operator has a cordon.
func TestAHostTooSmallForOneRunnerIsToldSo(t *testing.T) {
	h := newHarness(t)
	// One core less the half-core floor leaves half a core, and 2 GB less
	// the 512 MB floor leaves 1536 MB: neither is a runner's worth.
	h.measuredHost("tiny", 1, 2048, 2, enforcesEverything)

	p := h.problem(t, "host.overprovisioned")
	if !strings.Contains(p.Fix, "too small") || !strings.Contains(p.Fix, "capacity to 1") {
		t.Errorf("fix %q should say the machine is too small and still name capacity 1", p.Fix)
	}
}

// The warning is about hosts that do not fit, and only those: a host whose
// slots fit its cores and its memory raises nothing, a cordoned host is
// somebody's problem already, and the demo fleet is sized so that none of
// its hosts trips it.
func TestAHostWhoseSlotsFitRaisesNoOverprovisioningWarning(t *testing.T) {
	h := newHarness(t)
	h.host("roomy")
	h.measuredHost("exact", 4, 8192, 3, enforcesEverything)
	crowded := h.measuredHost("crowded-but-cordoned", 4, 16384, 8, enforcesEverything)
	if err := h.st.SetHostCordoned(h.ctx, crowded.ID, true); err != nil {
		t.Fatal(err)
	}
	if codes := h.problemCodes(); contains(codes, "host.overprovisioned") {
		t.Fatalf("problems = %v; every uncordoned host here fits", codes)
	}

	demo := newHarness(t)
	if err := demo.c.SeedDemo(demo.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}
	for _, code := range []string{"host.overprovisioned", "host.limits_unverified", "host.limits_unenforceable", "host.resources_unknown"} {
		if contains(demo.problemCodes(), code) {
			t.Errorf("the demo fleet raises %s; it exists to look like a fleet with nothing wrong", code)
		}
	}
}

// A daemon that has said it cannot apply a CPU quota refuses a runner that
// asks for one, so a pool with an explicit limit fails every create there
// and the default is withheld. The fix is the cgroup delegation a rootless
// daemon needs, spelled out.
func TestADaemonThatCannotApplyLimitsIsAWarningWithTheDelegationFix(t *testing.T) {
	h := newHarness(t)
	host := h.measuredHost("userns", 8, 16384, 4, store.LimitSupport{Known: true, CPU: false, Memory: true, Pids: true})
	// The probe says rootless, which picks the drop-in fix.
	host.BackendInfo[0].Rootless = true
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}

	p := h.problem(t, "host.limits_unenforceable")
	if p.Severity != config.SeverityWarning || p.TargetID != host.ID {
		t.Fatalf("problem = %+v", p)
	}
	if !strings.Contains(p.Detail, "rootless docker daemon on userns") || !strings.Contains(p.Detail, "CPU quota") || strings.Contains(p.Detail, "memory limit") {
		t.Errorf("detail %q should name the daemon and the one limit it cannot apply", p.Detail)
	}
	if !strings.Contains(p.Fix, "Delegate=cpu cpuset io memory pids") {
		t.Errorf("fix %q does not name the systemd delegation", p.Fix)
	}
	if contains(h.problemCodes(), "host.limits_unverified") {
		t.Error("a daemon that answered the probe is also reported as not having answered")
	}
}

// An agent too old to ask its daemon about limits is read as "default
// nothing", which is what every host did before defaults existed. That is
// worth a note while defaults are on, because it is the one reason a pool
// with no limits is still running unlimited there; with defaults off nothing
// is different about the host and there is nothing to say.
func TestAnUnverifiedProbeIsANoteOnlyWhileDefaultsAreOn(t *testing.T) {
	h := newHarness(t)
	h.measuredHost("old-probe", 8, 16384, 4, store.LimitSupport{})

	p := h.problem(t, "host.limits_unverified")
	if p.Severity != config.SeverityInfo || !strings.Contains(p.Detail, "old-probe") || !strings.Contains(p.Detail, "no default") {
		t.Fatalf("problem = %+v", p)
	}
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.DefaultRunnerLimits = false })
	if contains(h.problemCodes(), "host.limits_unverified") {
		t.Error("with defaults off an unverified probe changes nothing and should say nothing")
	}
}

// A host with room to spare in every dimension and a daemon that can apply
// everything is the host the resource model was written for, and it raises
// none of the model's problems: the drawer is for the three things wrong
// among the twenty that are right.
func TestAFullyInQuotaHostRaisesNoResourceProblem(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 4
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.measuredHost("well-sized", 16, 65536, 4, enforcesEverything)
	// Registration admission may defer demand to the next pass. Fill the
	// host before checking its resource accounting, without depending on
	// how quickly the fake GitHub answers detached credential requests.
	for range pool.MinRunners {
		if err := h.c.Reconcile(h.ctx); err != nil {
			t.Fatal(err)
		}
		h.c.lifecycleCalls.Wait()
		if len(h.runners()) == pool.MinRunners {
			break
		}
	}
	if got := len(h.runners()); got != 4 {
		t.Fatalf("created %d runners, want the host full at 4", got)
	}
	for _, code := range h.problemCodes() {
		if strings.HasPrefix(code, "host.") {
			t.Errorf("a full, in-quota host raised %s", code)
		}
	}
}

// A docker-in-docker slot is two containers whatever the pool typed, and the
// machine has to be sized for both either way.
//
// This is the host from the report that started the work: twelve slots' worth
// of cores on paper, every one of them a pair in practice, a Hosts page reading
// half committed, and a daemon that stopped answering creates. A pool that
// typed its own figures was always charged for the sidecar a second time; a
// pool sized by its host used to be let off the count entirely, on the theory
// that the pair splitting one slot between them made a slot worth one runner
// like everybody else's -- but stability over performance means each half of
// that split still has to be a runner's worth, not half of one, so an
// automatic pool now makes a slot a pair too. See scheduler.ShareFloor for the
// other half of that: a slot too thin for the split is refused outright rather
// than merely counted here.
func TestADockerInDockerPoolMakesEachSlotAPairInTheOverprovisioningCount(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	// Eight cores less the floor is 7.5, so six plain runners fit and this
	// host raises nothing at all.
	h.measuredHost("builders", 8, 32768, 6, enforcesEverything)
	if codes := h.problemCodes(); contains(codes, "host.overprovisioned") {
		t.Fatalf("problems = %v; six plain slots fit this machine", codes)
	}

	p := h.pool(inst, "dind-builders")
	p.DockerMode = store.DockerDinD
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	// Sized by its host: the pair still splits one slot between them, but
	// stability over performance means each half of that slot has to clear a
	// runner's own comfortable floor, and six slots of 7.5 allocatable cores
	// does not.
	auto := h.problem(t, "host.overprovisioned")
	for _, want := range []string{"dind-builders", "docker in docker", "two containers", "without limits of its own"} {
		if !strings.Contains(auto.Detail, want) {
			t.Errorf("detail %q does not mention %q", auto.Detail, want)
		}
	}
	if strings.Contains(auto.Detail, "each given what the pool asked for") {
		t.Errorf("detail %q describes a typed charge for a pool that set none", auto.Detail)
	}
	// 7.5 cores in pairs is three, and 31.5 GB in pairs of 2 GB slots is
	// seven: the cores run out first, as they did on the real host, and the
	// same figure applies whether or not the pool typed its own limits --
	// the fix is the same slot count either way.
	if !strings.Contains(auto.Fix, "capacity to 3") {
		t.Errorf("fix %q does not name the capacity the pairs fit in", auto.Fix)
	}

	p.Resources = store.Resources{CPUs: 1, MemoryMB: 2048}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	got := h.problem(t, "host.overprovisioned")
	for _, want := range []string{"dind-builders", "docker in docker", "two containers", "each given what the pool asked for"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %q", got.Detail, want)
		}
	}
	if !strings.Contains(got.Fix, "capacity to 3") {
		t.Errorf("fix %q does not name the capacity the pairs fit in", got.Fix)
	}
}

// The count follows the pools, not the host alone: a dind pool that cannot
// place here says nothing about how big a slot on this host is. A pool for
// another platform is the case an operator hits first -- one arm64 dind pool
// should not re-size every amd64 host in the fleet.
func TestAPoolThatCannotPlaceHereDoesNotMakeItsSlotsPairs(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.measuredHost("builders", 8, 32768, 6, enforcesEverything)

	p := h.pool(inst, "dind-arm")
	p.DockerMode = store.DockerDinD
	p.HostSelector = store.StringMap{"arch": "arm64"}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	if codes := h.problemCodes(); contains(codes, "host.overprovisioned") {
		t.Fatalf("problems = %v; the dind pool cannot place on an amd64 host", codes)
	}
}

// agent.capacity is read when the embedded host first enrols and not again, so a
// single-VM operator who is sent to edit it and restart finds the warning unchanged.
// The fix names what does move an enrolled host's capacity, and no setting is
// offered as the place to change it.
func TestTheEmbeddedHostsCapacityWarningDoesNotSendAnOperatorToASettingThatDoesNothing(t *testing.T) {
	host := &store.Host{
		ID: "host_embedded", Name: "controller", Embedded: true, Capacity: 8, CPUs: 4, MemoryMB: 16384,
		Backends: store.StringSlice{"docker"},
	}
	p, ok := overprovisionedProblem(host, nil, true)
	if !ok {
		t.Fatal("eight slots on four cores must be warned about")
	}
	if p.Setting != "" {
		t.Errorf("the warning offers the setting %q, which does nothing once the host has enrolled", p.Setting)
	}
	if strings.Contains(p.Fix, "zoomies.yaml") || !strings.Contains(p.Fix, "zoomies hosts edit host_embedded --capacity 2") {
		t.Errorf("the fix must name the command that changes an enrolled host's capacity: %q", p.Fix)
	}
}
