package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// A reserve that takes the last machine a pool could run on away is the whole
// point of the check: the fleet stays green, the scheduler keeps deciding
// correctly, and the only symptom is a job that queues for ever.
func TestRaisingAReserveUntilNoRunnerFitsStrandsThePool(t *testing.T) {
	h := newHarness(t)
	_, p, host := h.fleet()
	p.Resources = store.Resources{CPUs: 4, MemoryMB: 16 * 1024}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *host
	proposed.ReserveMemoryMB = 56 * 1024 // 64 GB machine, 8 GB left, 16 GB wanted

	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatalf("HostStrandings: %v", err)
	}
	if len(stranded) != 1 {
		t.Fatalf("expected the pool to be stranded, got %#v", stranded)
	}
	if stranded[0].Pool != p.Name || stranded[0].Host != host.Name {
		t.Fatalf("expected %s on %s, got %#v", p.Name, host.Name, stranded[0])
	}
	if stranded[0].Code != ExcludedSize {
		t.Fatalf("expected a size refusal, got %q", stranded[0].Code)
	}
	// The numbers are the fix: an operator who is only told "too small" has to
	// work out which slider to move and by how much.
	if !strings.Contains(stranded[0].Reason, "16 GB") || !strings.Contains(stranded[0].Reason, "memory") {
		t.Fatalf("the reason should quote the memory the pool is charged, got %q", stranded[0].Reason)
	}
}

// The check is narrow on purpose: an edit that leaves a pool somewhere else to
// go is not a conflict, and refusing it would make the fleet unmanageable.
func TestAReserveIsNotStrandingWhenAnotherHostStillFits(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	first := h.host("vm-1")
	h.host("vm-2")
	p.Resources = store.Resources{CPUs: 4, MemoryMB: 16 * 1024}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *first
	proposed.ReserveMemoryMB = 56 * 1024

	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatalf("HostStrandings: %v", err)
	}
	if len(stranded) != 0 {
		t.Fatalf("vm-2 can still run the pool, so nothing is stranded; got %#v", stranded)
	}
}

// A pool the controller keeps has the hosts of its class and none else, so the
// edit that moves its last host to another class -- a size tag -- is how the pool
// is meant to lose it, and is not a conflict. The same edit is refused for a pool
// an operator made that selects hosts the same way.
func TestMovingTheLastHostOutOfAClassDoesNotStrandThePoolTheControllerKeepsForIt(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.autoPoolsOn("on")
	host := h.mediumHost("build-1")
	h.autoPass()
	auto := h.poolNamed("zoomies-medium")
	if !auto.FromHosts() || !auto.Enabled {
		t.Fatalf("the pool is %+v; this test needs one the controller keeps, in use", auto)
	}

	proposed := *host
	proposed.Labels = store.StringMap{store.LabelSize: string(store.SizeLarge)}
	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(stranded) != 0 {
		t.Fatalf("moving the host to another class was refused over the pool the controller keeps: %#v", stranded)
	}

	// An operator's pool that selects the same hosts is still guarded.
	mine := h.pool(inst, "mine-medium")
	mine.HostSelector = store.StringMap{store.LabelSize: string(store.SizeMedium)}
	if err := h.st.UpdatePool(h.ctx, mine); err != nil {
		t.Fatal(err)
	}
	stranded, err = h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(stranded) != 1 || stranded[0].Pool != mine.Name {
		t.Fatalf("stranded = %#v; want only the operator's pool", stranded)
	}
}

// A pool that already had nowhere to run is not made worse by the next edit,
// and refusing one would trap an operator halfway through fixing the fleet.
func TestAPoolThatAlreadyHasNowhereToRunDoesNotRefuseAHostEdit(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	// Nothing in this fleet offers podman, so the pool is stranded already.
	p.Backend = store.BackendPodman
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *host
	proposed.ReserveMemoryMB = 60 * 1024

	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatalf("HostStrandings: %v", err)
	}
	if len(stranded) != 0 {
		t.Fatalf("the pool was already homeless, so this edit strands nothing; got %#v", stranded)
	}
}

// A disabled pool creates no runners, so nothing queues behind it and there is
// nothing to refuse an edit over.
func TestADisabledPoolDoesNotRefuseAHostEdit(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	p.Resources = store.Resources{CPUs: 4, MemoryMB: 16 * 1024}
	p.Enabled = false
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *host
	proposed.ReserveMemoryMB = 56 * 1024

	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatalf("HostStrandings: %v", err)
	}
	if len(stranded) != 0 {
		t.Fatalf("a disabled pool has nothing waiting on it; got %#v", stranded)
	}
}

// Taking a label off a host is the same mistake made with a different field,
// and the reason has to name the label rather than say "the selector".
func TestRemovingTheLabelAPoolSelectsOnStrandsIt(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	host.Labels = store.StringMap{"tier": "gpu"}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{Labels: &host.Labels}); err != nil {
		t.Fatalf("PatchHost: %v", err)
	}
	p.HostSelector = store.StringMap{"tier": "gpu"}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *host
	proposed.Labels = store.StringMap{}

	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatalf("HostStrandings: %v", err)
	}
	if len(stranded) != 1 || stranded[0].Code != ExcludedSelector {
		t.Fatalf("expected a selector refusal, got %#v", stranded)
	}
	if !strings.Contains(stranded[0].Reason, "tier=gpu") {
		t.Fatalf("the reason should name the label and the value, got %q", stranded[0].Reason)
	}
}

// The other half: the same conflict typed on the pool's form rather than the
// host's, and answered in the same words.
func TestAskingForMoreThanAnyHostHasStrandsThePool(t *testing.T) {
	h := newHarness(t)
	_, p, host := h.fleet()

	proposed := *p
	proposed.Resources = store.Resources{CPUs: 4, MemoryMB: 128 * 1024}

	stranded, err := h.c.PoolStranding(h.ctx, p, &proposed)
	if err != nil {
		t.Fatalf("PoolStranding: %v", err)
	}
	if len(stranded) != 1 {
		t.Fatalf("expected the pool to be stranded, got %#v", stranded)
	}
	if stranded[0].Host != host.Name || stranded[0].Code != ExcludedSize {
		t.Fatalf("expected a size refusal quoting %s, got %#v", host.Name, stranded[0])
	}
}

// A pool already asking for more than the fleet has is being edited by an
// operator who has been told so, and stopping them changing anything else
// about it would be a trap rather than a guard.
func TestEditingAPoolThatAlreadyFitsNowhereIsNotRefused(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	p := h.pool(inst, "linux-x64")
	p.Resources = store.Resources{CPUs: 64, MemoryMB: 128 * 1024}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *p
	proposed.MaxRunners = 8

	stranded, err := h.c.PoolStranding(h.ctx, p, &proposed)
	if err != nil {
		t.Fatalf("PoolStranding: %v", err)
	}
	if len(stranded) != 0 {
		t.Fatalf("the pool fitted nowhere already; got %#v", stranded)
	}
}

// Health is deliberately not consulted: a host waiting for its agent to check
// in is still where a pool lives, and an edit refused because some other
// machine happened to be rebooting would be a rule nobody could predict.
func TestAHostThatIsNotHeartbeatingStillCountsAsAPoolsHome(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	p := h.pool(inst, "linux-x64")
	first := h.host("vm-1")
	stale := h.host("vm-2")
	if err := h.st.Heartbeat(h.ctx, stale.ID, h.c.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	p.Resources = store.Resources{CPUs: 4, MemoryMB: 16 * 1024}
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	proposed := *first
	proposed.ReserveMemoryMB = 56 * 1024

	stranded, err := h.c.HostStrandings(h.ctx, &proposed)
	if err != nil {
		t.Fatalf("HostStrandings: %v", err)
	}
	if len(stranded) != 0 {
		t.Fatalf("vm-2 is the pool's home whether or not it is heartbeating; got %#v", stranded)
	}
}
