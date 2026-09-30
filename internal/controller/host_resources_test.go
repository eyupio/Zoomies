package controller

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// An operator can see what a host is and what is left on it; without this they
// could not see what the fleet has already promised away, which is the number
// that explains a host with free slots taking nothing.
//
// It is the scheduler's own sum rather than a second one computed for the page:
// a figure on screen that disagreed with the one the pass placed against would
// be worse than no figure, because it would be believed.
func TestTheHostViewCarriesWhatTheFleetHasPromisedAway(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.Resources = store.Resources{CPUs: 2, MemoryMB: 4096}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	host := h.host("vm-1")
	h.runnerRow(pool, host, store.RunnerIdle)
	h.runnerRow(pool, host, store.RunnerBusy)
	// A failed runner is not charged for, exactly as it does not hold a slot.
	failed := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, failed.ID, store.RunnerFailed, "failed"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}

	// Before a pass has run there is no answer, and saying zero would read as
	// an idle machine rather than as a question nobody has asked yet.
	if view := h.c.HostView(host); view.ReservedKnown {
		t.Error("a controller that has decided nothing reported a reservation anyway")
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	view := h.c.HostView(host)
	if !view.ReservedKnown {
		t.Fatal("a pass has run and the host still reports no reservation")
	}
	if view.ReservedCPUs != 4 || view.ReservedMemoryMB != 8192 {
		t.Errorf("reserved = %v CPUs / %d MB, want the two live runners' 4 and 8192 (the failed one is not charged)",
			view.ReservedCPUs, view.ReservedMemoryMB)
	}
	if !view.ResourcesKnown {
		t.Error("a host that reported its machine says its resources are unknown")
	}
	// The memory reserve scales with the machine, so a 64 GB host holds back a
	// twentieth rather than the flat floor a small one does.
	if want := int64(64*1024) - (&store.Host{MemoryMB: 64 * 1024}).MemoryReserve(); view.AllocatableMemoryMB != want {
		t.Errorf("allocatable memory = %d, want the machine less its reserve, %d", view.AllocatableMemoryMB, want)
	}
}

// A host whose agent predates the resource fields is placed by slots alone,
// which is what keeps an upgrade from emptying a fleet -- and the page has to
// say so, because a card with every figure missing reads as broken rather than
// as old.
//
// How loudly it says so depends on what the fleet does with a measurement.
// With default limits on, an unmeasured host is the one host whose runners
// get no default -- their share of a machine nobody has measured cannot be
// computed -- so a pool with no limits of its own runs unlimited there, and
// that is a warning. With defaults off nothing is different about the host and
// it stays a note.
func TestAHostThatHasNotMeasuredItselfSaysSo(t *testing.T) {
	h := newHarness(t)
	silent := &store.Host{Name: "old-agent", Capacity: 2, Backends: store.StringSlice{"docker"},
		Labels: store.StringMap{}, OS: "linux", Arch: "amd64", LastHeartbeat: h.c.Now()}
	if err := h.st.CreateHost(h.ctx, silent); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if h.c.HostView(silent).ResourcesKnown {
		t.Error("a host that has reported nothing claims its resources are known")
	}
	for _, tc := range []struct {
		defaults bool
		want     string
		because  string
	}{
		{true, "warning", "its runners are given no default limit, so a pool without limits runs unlimited there"},
		{false, "info", "nothing is wrong with an old agent, it is just unmeasured"},
	} {
		h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.DefaultRunnerLimits = tc.defaults })
		p := h.problem(t, "host.resources_unknown")
		if !strings.Contains(p.Detail, "old-agent") {
			t.Errorf("the note does not name the host: %q", p.Detail)
		}
		if string(p.Severity) != tc.want {
			t.Errorf("defaults=%v: severity = %q, want %s: %s", tc.defaults, p.Severity, tc.want, tc.because)
		}
		if tc.defaults && !strings.Contains(p.Detail, "no default") {
			t.Errorf("with defaults on the detail does not say the runners get none: %q", p.Detail)
		}
	}
}

// A pool's resources become cgroup limits on the container backends and
// nothing at all on the process backend. The reservation still holds the room,
// so the fleet does not oversubscribe -- but the room is bookkeeping, and
// saying so is the difference between a limit and a wish.
func TestAProcessPoolWithLimitsIsWarnedAbout(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "on-metal")
	pool.Backend = store.BackendProcess
	pool.Resources = store.Resources{MemoryMB: 4096}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	if !contains(h.problemCodes(), "pool.resources_unenforced") {
		t.Fatalf("problems = %v, want pool.resources_unenforced", h.problemCodes())
	}

	// The same pool on a backend that enforces them raises nothing: the
	// warning is about the backend, not about setting limits.
	pool.Backend = store.BackendDocker
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if contains(h.problemCodes(), "pool.resources_unenforced") {
		t.Errorf("problems = %v; a docker pool enforces its limits and needs no warning", h.problemCodes())
	}
}

// The UI tells two problems apart by code, target and setting, and dismisses
// or snoozes by that identity. Two process pools with limits used to raise two
// problems that shared all three (with no target at all), so muting one pool's
// warning muted the other's and neither got an "Open the pool" link.
func TestTwoProcessPoolsWithLimitsAreTwoProblemsWithTheirOwnTargets(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	want := map[string]string{}
	for _, name := range []string{"on-metal", "on-metal-too"} {
		pool := h.pool(inst, name)
		pool.Backend = store.BackendProcess
		pool.Resources = store.Resources{MemoryMB: 4096}
		if err := h.st.UpdatePool(h.ctx, pool); err != nil {
			t.Fatalf("UpdatePool: %v", err)
		}
		want[pool.ID] = name
	}

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Code != "pool.resources_unenforced" {
			continue
		}
		name, ok := want[p.TargetID]
		if !ok || p.TargetKind != "pool" {
			t.Errorf("problem targets %s %q, want one of the pools %v", p.TargetKind, p.TargetID, want)
			continue
		}
		if seen[p.TargetID] {
			t.Errorf("two problems target pool %s", p.TargetID)
		}
		seen[p.TargetID] = true
		if !strings.Contains(p.Title, name) {
			t.Errorf("title %q does not name pool %s, so the drawer cannot tell its rows apart", p.Title, name)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("problems cover pools %v, want %v", seen, want)
	}
}

// A host whose docker and podman daemons both have something wrong raises one
// problem per backend for the same host. The UI keys a drawer row by code,
// target and title, so the titles have to differ or the two rows collide.
func TestAHostWithTwoAffectedBackendsRaisesProblemsWithDistinctTitles(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	for _, kind := range []store.BackendKind{store.BackendDocker, store.BackendPodman} {
		p := h.pool(inst, "cached-"+string(kind))
		p.Backend = kind
		p.Cache = store.CacheConfig{Enabled: true, Tools: true, Scope: store.CacheScopePool}
		if err := h.st.UpdatePool(h.ctx, p); err != nil {
			t.Fatalf("UpdatePool: %v", err)
		}
	}
	limits := store.LimitSupport{Known: true, CPU: false, Memory: true, Pids: true}
	host := h.measuredHost("both", 16, 65536, 4, limits)
	host.Backends = store.StringSlice{"docker", "podman"}
	host.BackendInfo = store.HostBackends{
		{Kind: store.BackendDocker, Available: true, SharedFolder: "the shared folder is not mounted", Limits: limits},
		{Kind: store.BackendPodman, Available: true, SharedFolder: "the shared folder is not mounted", Limits: limits},
	}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	for _, code := range []string{"host.limits_unenforceable", "host.shared_folder_unmounted"} {
		titles := map[string]bool{}
		n := 0
		for _, p := range ps {
			if p.Code == code {
				n++
				titles[p.Title] = true
			}
		}
		if n != 2 || len(titles) != 2 {
			t.Errorf("%s: %d problems with %d distinct titles, want two of each", code, n, len(titles))
		}
	}
}
