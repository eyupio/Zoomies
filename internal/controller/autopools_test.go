package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// autoPoolsOn turns the reconciler on, and the hold off so that a test is about
// the lifecycle and not about waiting: the hold has its own test.
func (h *harness) autoPoolsOn(mode string) {
	h.t.Helper()
	h.c.UpdateConfig(func(cfg *config.Config) {
		cfg.Scheduler.AutoPools = mode
		cfg.Scheduler.SizeClassHold = 0
	})
}

func (h *harness) autoPass() {
	h.t.Helper()
	if err := h.c.ReconcileAutoPools(h.ctx); err != nil {
		h.t.Fatalf("ReconcileAutoPools: %v", err)
	}
}

func (h *harness) poolNamed(name string) *store.Pool {
	h.t.Helper()
	p, err := h.st.GetPoolByName(h.ctx, name)
	if err != nil {
		h.t.Fatalf("pool %s: %v", name, err)
	}
	return p
}

func (h *harness) hasPool(name string) bool {
	_, err := h.st.GetPoolByName(h.ctx, name)
	return err == nil
}

// audits is the audit rows of one action, oldest first.
func (h *harness) audits(action string) []*store.AuditEvent {
	h.t.Helper()
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{action}}, store.Page{Limit: 100})
	if err != nil {
		h.t.Fatalf("ListAudit: %v", err)
	}
	slices.Reverse(rows)
	return rows
}

func causeOf(t *testing.T, e *store.AuditEvent) string {
	t.Helper()
	var after map[string]any
	if err := json.Unmarshal([]byte(e.After), &after); err != nil {
		t.Fatalf("audit row %s has no readable after: %v (%q)", e.Action, err, e.After)
	}
	cause, _ := after["cause"].(string)
	return cause
}

// mediumHost is the machine of the worked example: twelve cores and 32 GB, which
// is medium, and holds five runners of the medium class's size.
func (h *harness) mediumHost(name string) *store.Host {
	return h.measuredHost(name, 12, 32768, 8, enforcesEverything)
}

func (h *harness) smallHost(name string) *store.Host {
	return h.measuredHost(name, 4, 16384, 8, enforcesEverything)
}

// beatAll keeps every host alive as of the controller's clock, which a test that
// moves the clock needs for the hosts it does not mean to be quiet.
func (h *harness) beatAll() {
	h.t.Helper()
	hosts, err := h.st.ListHosts(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, host := range hosts {
		if err := h.st.Heartbeat(h.ctx, host.ID, h.c.Now()); err != nil {
			h.t.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Off, and watching
// ---------------------------------------------------------------------------

// A fleet that has not asked for any of this sees none of it: no host is given
// a class, no pool is made, an operator's pool is not touched, and nothing is
// written that the upgrade did not already have.
func TestNothingChangesWhileSizeRoutingAndAutomaticPoolsAreOff(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	operator := h.pool(inst, "mine")
	host := h.mediumHost("build-1")

	h.autoPass()
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pools, _ := h.st.ListPools(h.ctx)
	if len(pools) != 1 || pools[0].ID != operator.ID || pools[0].FromHosts() {
		t.Fatalf("pools = %+v; the operator's pool alone should exist", pools)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Set() {
		t.Fatalf("a host was given a class with both switches off: %+v", got.SizeClass)
	}
	if st := h.c.AutoPoolStatus(); st.Mode != scheduler.SizeOff || len(st.Pending) != 0 {
		t.Fatalf("status = %+v", st)
	}
	if n := len(h.audits("host.size_class")) + len(h.audits("pool.auto_create")); n != 0 {
		t.Fatalf("%d audit rows were written with the feature off", n)
	}
}

func TestShadowSaysWhatAutomaticPoolsWouldDoAndDoesNone(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("shadow")
	host := h.mediumHost("build-1")

	h.autoPass()
	if h.hasPool("zoomies-medium") {
		t.Fatal("shadow mode made a pool")
	}
	st := h.c.AutoPoolStatus()
	if st.Mode != scheduler.SizeShadow || len(st.Pending) != 1 || st.Pending[0].Kind != "create" ||
		st.Pending[0].Pool != "zoomies-medium" || !strings.Contains(st.Pending[0].Cause, "build-1") {
		t.Fatalf("status = %+v; want one pending create of zoomies-medium naming the host", st)
	}
	// The class is observational and is recorded, so the Hosts page shows it.
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Class != store.SizeMedium {
		t.Fatalf("the host's class under shadow is %q", got.SizeClass.Class)
	}
}

// ---------------------------------------------------------------------------
// The lifecycle
// ---------------------------------------------------------------------------

func TestTheFirstHostOfAClassMakesItsPoolWithItsCauseOnRecord(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.autoPoolsOn("on")
	host := h.mediumHost("build-1")

	h.autoPass()
	p := h.poolNamed("zoomies-medium")
	if !p.FromHosts() || p.AutoKey != "amd64/medium" || p.InstallationID != inst.ID || !p.SizeFromProfile ||
		!p.Enabled || p.MaxRunners != 5 || p.MinRunners != 0 {
		t.Fatalf("the pool is %+v; want an enabled medium pool of five for the installation, sized from the host", p)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Class != store.SizeMedium {
		t.Fatalf("the host's class is %q", got.SizeClass.Class)
	}

	creates := h.audits("pool.auto_create")
	if len(creates) != 1 || creates[0].ActorKind != "system" || creates[0].TargetID != p.ID ||
		!strings.Contains(causeOf(t, creates[0]), "build-1") || !strings.Contains(causeOf(t, creates[0]), "5 slots") {
		t.Fatalf("the creation is on record as %+v", creates)
	}
	if classes := h.audits("host.size_class"); len(classes) != 1 || !strings.Contains(causeOf(t, classes[0]), "medium") {
		t.Fatalf("the host's class is on record as %+v", classes)
	}

	// The next pass finds nothing to do and writes nothing: no audit row, and a
	// pool whose updated_at is when it last changed.
	before := p.UpdatedAt
	h.autoPass()
	h.autoPass()
	if n := len(h.audits("pool.auto_create")) + len(h.audits("pool.auto_resize")) + len(h.audits("host.size_class")); n != 2 {
		t.Fatalf("%d audit rows after three passes over an unchanged fleet, want the 2 from the first", n)
	}
	if !h.poolNamed("zoomies-medium").UpdatedAt.Equal(before) {
		t.Fatal("an unchanged pass moved the pool's updated_at")
	}
}

// Queued jobs should start using a new host straight away: the pass that raises
// the maximum wakes the scheduler, which has demand waiting for exactly that.
func TestAHostJoiningRaisesThePoolsMaximumAndWakesTheScheduler(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.mediumHost("build-1")
	h.autoPass()
	select {
	case <-h.c.nudges:
	default:
	}

	h.mediumHost("build-2")
	h.autoPass()
	p := h.poolNamed("zoomies-medium")
	if p.MaxRunners != 10 {
		t.Fatalf("the maximum is %d after a second host, want 10", p.MaxRunners)
	}
	resizes := h.audits("pool.auto_resize")
	if len(resizes) != 1 || causeOf(t, resizes[0]) != "maximum runners 5 → 10: host build-2 joined" {
		t.Fatalf("the resize is on record as %+v", resizes)
	}
	select {
	case <-h.c.nudges:
	default:
		t.Fatal("the scheduler was not woken: queued jobs would wait for the next tick to use the new host")
	}
}

// A cordoned host takes no new runners, so its slots stop being promised. The
// runners already on it are not touched: the plan changes a pool's figures and
// never a runner, which is the scheduler's to drain or let finish.
func TestACordonedHostLowersTheMaximumAndLeavesItsRunnersAlone(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	a, b := h.mediumHost("build-1"), h.mediumHost("build-2")
	h.autoPass()
	pool := h.poolNamed("zoomies-medium")
	running := h.runnerRow(pool, b, store.RunnerBusy)

	if err := h.st.SetHostCordoned(h.ctx, b.ID, true); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if got := h.poolNamed("zoomies-medium"); got.MaxRunners != 5 || !got.Enabled {
		t.Fatalf("after a cordon the pool is %+v, want a maximum of 5, still in use", got)
	}
	resizes := h.audits("pool.auto_resize")
	if len(resizes) != 1 || !strings.Contains(causeOf(t, resizes[0]), "host build-2 was cordoned") {
		t.Fatalf("the cordon is on record as %+v", resizes)
	}
	if got := h.runnerByID(t, running.ID); got.State != store.RunnerBusy {
		t.Fatalf("the runner on the cordoned host is %s; the plan must not touch it", got.State)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.runnerByID(t, running.ID); got.State != store.RunnerBusy {
		t.Fatalf("a scheduling pass after the cordon took the running job's runner: %s", got.State)
	}
	_ = a
}

// A brief drop in the network reshuffles nothing: a host counts for as long as
// the grace says. Past it the host is out, and when it is heard from again the
// pool is back.
func TestAHostThatGoesQuietIsOnlyLostAfterTheGraceAndComesBack(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.mediumHost("build-1")
	h.autoPass()

	h.advance(9 * time.Minute) // longer than the fleet's own five, inside the grace
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); p.MaxRunners != 5 || !p.Enabled {
		t.Fatalf("after nine silent minutes the pool is %+v; a brief drop must reshuffle nothing", p)
	}

	h.advance(2 * time.Minute)
	h.autoPass()
	p := h.poolNamed("zoomies-medium")
	if p.Enabled || p.MaxRunners != 0 {
		t.Fatalf("after eleven silent minutes the last host's pool is %+v, want it out of use", p)
	}
	disables := h.audits("pool.auto_disable")
	if len(disables) != 1 || !strings.Contains(causeOf(t, disables[0]), "host build-1 has not been heard from for too long") {
		t.Fatalf("the disable is on record as %+v", disables)
	}
	if h.poolNamed("zoomies-medium").ID != p.ID {
		t.Fatal("the pool's record was not kept")
	}

	h.beatAll()
	h.autoPass()
	back := h.poolNamed("zoomies-medium")
	if !back.Enabled || back.MaxRunners != 5 || back.ID != p.ID {
		t.Fatalf("a host heard from again left the pool at %+v", back)
	}
	if enables := h.audits("pool.auto_enable"); len(enables) != 1 || !strings.Contains(causeOf(t, enables[0]), "host build-1 is back") {
		t.Fatalf("the return is on record as %+v", enables)
	}
}

// A controller that was down for longer than the grace has heard from no host for
// that long, which says nothing about the hosts. The first pass after it starts
// counts silence from when it began listening, and would otherwise drop every
// host, and disable every pool, until the agents had reported again.
func TestTheFirstPassAfterALongOutageDoesNotDropEveryHost(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	host := h.mediumHost("build-1")
	// The host last reported three hours ago, before this controller started.
	if err := h.st.Heartbeat(h.ctx, host.ID, h.c.Now().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.c.startedAt = h.c.Now()

	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); !p.Enabled || p.MaxRunners != 5 {
		t.Fatalf("the first pass after an outage left the pool at %+v; the host was not heard from because nobody was listening", p)
	}
	if n := len(h.audits("pool.auto_disable")); n != 0 {
		t.Fatalf("%d pools were put out of use by an outage that was the controller's", n)
	}

	// It is counted from when it began listening, not for ever: a host that has
	// said nothing since is lost once the grace has run from then.
	h.advance(11 * time.Minute)
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); p.Enabled {
		t.Fatalf("a host silent for the grace since the controller started still counts: %+v", p)
	}
}

func TestADeletedHostLowersTheMaximumAndTheLastOneDisablesThePoolAndKeepsIt(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	a, b := h.mediumHost("build-1"), h.mediumHost("build-2")
	h.autoPass()
	id := h.poolNamed("zoomies-medium").ID

	if _, err := h.st.DeleteHost(h.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if got := h.poolNamed("zoomies-medium"); got.MaxRunners != 5 || !strings.Contains(causeOf(t, h.audits("pool.auto_resize")[0]), "host build-2 was removed") {
		t.Fatalf("after one host went the pool is %+v", got)
	}
	if _, err := h.st.DeleteHost(h.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	got := h.poolNamed("zoomies-medium")
	if got.ID != id || got.Enabled || got.MaxRunners != 0 {
		t.Fatalf("after the last host went the pool is %+v, want the same pool, out of use", got)
	}
}

// What an operator asks of an automatic pool is kept beside what the controller
// works out, and the controller's pass never writes one over the other.
func TestAnOperatorsPauseCapAndWarmCountAreKeptAndAppliedByTheController(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.mediumHost("build-1")
	h.autoPass()

	pool := h.poolNamed("zoomies-medium")
	pool.AutoCap, pool.AutoMin = 3, 1
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	got := h.poolNamed("zoomies-medium")
	if got.MaxRunners != 3 || got.MinRunners != 1 || got.AutoCap != 3 || got.AutoMin != 1 || !got.Enabled {
		t.Fatalf("the pool is %+v; want a maximum of 3 and one kept warm, with what the operator asked for still on it", got)
	}

	got.AutoPaused = true
	if err := h.st.UpdatePool(h.ctx, got); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); p.Enabled || p.MaxRunners != 3 {
		t.Fatalf("a paused pool is %+v; it should be out of use and still say what room its hosts give it", p)
	}
	disables := h.audits("pool.auto_disable")
	if len(disables) != 1 || !strings.Contains(causeOf(t, disables[0]), "an operator paused the pool") {
		t.Fatalf("the pause is on record as %+v", disables)
	}

	got = h.poolNamed("zoomies-medium")
	got.AutoPaused = false
	if err := h.st.UpdatePool(h.ctx, got); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); !p.Enabled {
		t.Fatalf("a pool whose pause was lifted is %+v", p)
	}
	if enables := h.audits("pool.auto_enable"); len(enables) != 1 || !strings.Contains(causeOf(t, enables[0]), "an operator resumed the pool") {
		t.Fatalf("the resume is on record as %+v", enables)
	}
}

// The fixed side of a pool follows the settings, and an operator's side of it is
// never part of what is brought back in line: the idle timeout, the cap and the
// warm count are theirs.
func TestAPoolIsBroughtBackInLineWithTheSettingsAndKeepsWhatTheOperatorAsked(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.mediumHost("build-1")
	h.autoPass()

	pool := h.poolNamed("zoomies-medium")
	pool.AutoCap, pool.AutoMin, pool.IdleTimeout = 4, 1, store.Duration(20*time.Minute)
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.autoPass()

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsDockerMode = "dind" })
	h.autoPass()
	got := h.poolNamed("zoomies-medium")
	if got.DockerMode != store.DockerDinD {
		t.Fatalf("the pool's docker mode is %q after the setting changed to dind", got.DockerMode)
	}
	if got.AutoCap != 4 || got.AutoMin != 1 || got.IdleTimeout.Duration() != 20*time.Minute || got.MaxRunners != 4 || got.MinRunners != 1 {
		t.Fatalf("bringing the pool back in line changed what the operator asked for: %+v", got)
	}
	reshapes := h.audits("pool.auto_reshape")
	if len(reshapes) != 1 || !strings.Contains(causeOf(t, reshapes[0]), "brought back in line") {
		t.Fatalf("the reshape is on record as %+v", reshapes)
	}
	var after map[string]any
	if err := json.Unmarshal([]byte(reshapes[0].After), &after); err != nil || after["docker_mode"] != "dind" {
		t.Fatalf("the audit row does not say what the pool was brought in line to: %q (%v)", reshapes[0].After, err)
	}
}

// A pool that needs both its limits and its fixed settings corrected is
// corrected in the pass that finds it, not in one pass for each.
func TestAPoolThatNeedsItsLimitsAndItsSettingsCorrectedIsCorrectedInOnePass(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.mediumHost("build-1")
	h.autoPass()

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsDockerMode = "dind" })
	h.mediumHost("build-2")
	h.autoPass()

	got := h.poolNamed("zoomies-medium")
	if got.DockerMode != store.DockerDinD || got.MaxRunners != 10 {
		t.Fatalf("the pool is %+v after one pass; want docker mode dind and a maximum of 10 together", got)
	}
	resizes := h.audits("pool.auto_resize")
	if len(resizes) != 1 || !strings.Contains(causeOf(t, resizes[0]), "build-2 joined") ||
		!strings.Contains(causeOf(t, resizes[0]), "also brought back in line") {
		t.Fatalf("the change is on record as %+v; want one row that says both", resizes)
	}
	if n := len(h.audits("pool.auto_reshape")); n != 0 {
		t.Fatalf("%d separate reshape rows; the same pass already did it", n)
	}
}

// A pool made by hand on an organisation goes in Zoomies' own runner group, which
// keeps its runners out of the Default group every repository can use. The pools
// the controller makes are held to the same boundary, and a repository's
// installation has no groups to put them in.
func TestAnAutomaticPoolIsMadeInTheManagedRunnerGroupOnAnOrganisationAndNoneOnARepository(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		kind   store.TargetType
		want   string
	}{
		{"an organisation", "acme", store.TargetOrg, ManagedRunnerGroupName},
		{"a repository", "acme/widgets", store.TargetRepo, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.installationOn(tc.target, tc.kind)
			h.autoPoolsOn("on")
			h.mediumHost("build-1")
			h.autoPass()
			if got := h.poolNamed("zoomies-medium").RunnerGroup; got != tc.want {
				t.Fatalf("the pool for %s is in runner group %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Classes that move
// ---------------------------------------------------------------------------

// A host that measures one side of a threshold and then the other must not hop
// between pools. It moves when the new class has held for the whole hold, and
// not before.
func TestAHostMovesBetweenPoolsOnlyAfterTheHoldAndRunningJobsAreUntouched(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeClassHold = 10 * time.Minute })
	host := h.mediumHost("build-1")
	h.autoPass()
	pool := h.poolNamed("zoomies-medium")
	running := h.runnerRow(pool, host, store.RunnerBusy)

	// The machine is resized: it now measures large.
	grow := func() {
		got, _ := h.st.GetHost(h.ctx, host.ID)
		got.CPUs, got.MemoryMB = 32, 131072
		if err := h.st.SetHostReported(h.ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	grow()
	h.autoPass()
	got, _ := h.st.GetHost(h.ctx, host.ID)
	if got.SizeClass.Class != store.SizeMedium || got.SizeClass.Pending != store.SizeLarge {
		t.Fatalf("at once the host is %+v; it should be medium with large pending", got.SizeClass)
	}
	if h.hasPool("zoomies-large") {
		t.Fatal("a pool was made for a class the host has not held")
	}

	// It goes back before the hold is out: nothing moves, and the wait is gone.
	h.advance(6 * time.Minute)
	h.beatAll()
	shrink := func() {
		got, _ := h.st.GetHost(h.ctx, host.ID)
		got.CPUs, got.MemoryMB = 12, 32768
		if err := h.st.SetHostReported(h.ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	shrink()
	h.autoPass()
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Pending != "" || got.SizeClass.Class != store.SizeMedium {
		t.Fatalf("a reading of the held class left %+v", got.SizeClass)
	}

	// Now it grows and stays grown for the whole hold.
	grow()
	h.autoPass()
	h.advance(9 * time.Minute)
	h.beatAll()
	h.autoPass()
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Class != store.SizeMedium {
		t.Fatalf("nine minutes in, the host is %q", got.SizeClass.Class)
	}
	h.advance(2 * time.Minute)
	h.beatAll()
	h.autoPass()
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Class != store.SizeLarge {
		t.Fatalf("after the hold the host is %q", got.SizeClass.Class)
	}
	if medium := h.poolNamed("zoomies-medium"); medium.Enabled {
		t.Fatalf("the class it left is still in use: %+v", medium)
	}
	if large := h.poolNamed("zoomies-large"); !large.Enabled || large.MaxRunners != 7 {
		t.Fatalf("the class it joined is %+v, want an enabled pool of seven", large)
	}
	if moves := h.audits("host.size_class"); len(moves) != 2 || !strings.Contains(causeOf(t, moves[1]), "moved") {
		t.Fatalf("the move is on record as %+v", moves)
	}

	// The job running on the host never noticed.
	if got := h.runnerByID(t, running.ID); got.State != store.RunnerBusy || got.PoolID != pool.ID {
		t.Fatalf("the runner that was working when the host changed class is %+v", got)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.runnerByID(t, running.ID); got.State != store.RunnerBusy {
		t.Fatalf("a scheduling pass after the move took the running job's runner: %s", got.State)
	}
}

func TestAnOperatorsSizeTagMovesAHostAtOnceAndDoesNotChangeWhatTheControllerHolds(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	host := h.smallHost("tiny-1")
	h.autoPass()
	if !h.hasPool("zoomies-small") {
		t.Fatal("no pool for the small host")
	}

	labels := store.StringMap{"size": "large"}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{Labels: &labels}); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if p := h.poolNamed("zoomies-large"); !p.Enabled {
		t.Fatalf("the pool for the class the operator chose is %+v", p)
	}
	if p := h.poolNamed("zoomies-small"); p.Enabled {
		t.Fatalf("the class the host left is still in use: %+v", p)
	}
	got, _ := h.st.GetHost(h.ctx, host.ID)
	if got.SizeClass.Class != store.SizeSmall || got.Labels["size"] != "large" {
		t.Fatalf("host = class %q, labels %v; the machine's own class is not the operator's to overwrite", got.SizeClass.Class, got.Labels)
	}

	// Taking the tag off puts the host back where its machine says.
	none := store.StringMap{}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{Labels: &none}); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if p := h.poolNamed("zoomies-small"); !p.Enabled {
		t.Fatalf("with the tag off the small pool is %+v", p)
	}
}

// ---------------------------------------------------------------------------
// Coexistence and refusal
// ---------------------------------------------------------------------------

func TestAutomaticPoolsNeverTouchAnOperatorsPoolAndSayWhenTheyCannotMakeTheirOwn(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.autoPoolsOn("on")
	mine := h.pool(inst, "zoomies-medium", "self-hosted", "zoomies-medium", "mine")
	before, _ := h.st.GetPool(h.ctx, mine.ID)
	h.mediumHost("build-1")
	h.smallHost("tiny-1")

	h.autoPass()
	after, _ := h.st.GetPool(h.ctx, mine.ID)
	if after.FromHosts() || after.MaxRunners != before.MaxRunners || !after.Enabled || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("the operator's pool was changed: %+v", after)
	}
	if !h.hasPool("zoomies-small") {
		t.Fatal("the unrelated class was not made")
	}
	st := h.c.AutoPoolStatus()
	if len(st.Findings) != 1 || st.Findings[0].Code != scheduler.FindingNameTaken {
		t.Fatalf("findings = %+v; the name taken by the operator's pool should be one", st.Findings)
	}
	if prob := h.problem(t, "pool.auto_blocked"); !strings.Contains(prob.Title, "zoomies-medium") || prob.Fix == "" {
		t.Fatalf("the problem is %+v", prob)
	}
}

func TestSeveralInstallationsNeedOneToBeChosen(t *testing.T) {
	h := newHarness(t)
	h.installationOn("acme", store.TargetOrg)
	other := h.installationOn("globex", store.TargetOrg)
	h.autoPoolsOn("on")
	h.mediumHost("build-1")

	h.autoPass()
	if h.hasPool("zoomies-medium") {
		t.Fatal("a pool was made for an installation nobody chose")
	}
	st := h.c.AutoPoolStatus()
	if !strings.Contains(st.Problem, "scheduler.auto_pools_installation") {
		t.Fatalf("status = %+v", st)
	}
	if prob := h.problem(t, "pool.auto_blocked"); !strings.Contains(prob.Detail, "several GitHub App installations") {
		t.Fatalf("problem = %+v", prob)
	}

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsInstallation = "globex" })
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); p.InstallationID != other.ID {
		t.Fatalf("the pool belongs to %s, want the installation that was named", p.InstallationID)
	}
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsInstallation = "nobody" })
	h.autoPass()
	if st := h.c.AutoPoolStatus(); !strings.Contains(st.Problem, `"nobody"`) {
		t.Fatalf("an installation nobody manages gave %+v", st)
	}
}

// Pool names are unique across the whole instance, so the pool kept for the
// installation automatic pools used to belong to holds the name of the one the
// new installation needs. It is put out of use and left, as a pool with no hosts
// is, and the operator is told which pool to remove -- which they can, because
// nothing would make it again. Silence here was a pool that was never made.
func TestMovingAutomaticPoolsToAnotherInstallationSaysWhichOldPoolStandsInTheWay(t *testing.T) {
	h := newHarness(t)
	acme := h.installationOn("acme", store.TargetOrg)
	globex := h.installationOn("globex", store.TargetOrg)
	h.autoPoolsOn("on")
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsInstallation = "acme" })
	h.mediumHost("build-1")
	h.autoPass()
	old := h.poolNamed("zoomies-medium")
	if old.InstallationID != acme.ID || !h.c.AutoPoolKept(old) {
		t.Fatalf("the first pool is %+v, kept %t; want it acme's and kept", old, h.c.AutoPoolKept(old))
	}

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsInstallation = "globex" })
	h.autoPass()
	stranded := h.poolNamed("zoomies-medium")
	if stranded.ID != old.ID || stranded.InstallationID != acme.ID || stranded.Enabled || stranded.MaxRunners != 0 {
		t.Fatalf("the old pool is %+v; it should be out of use and still acme's", stranded)
	}
	if h.c.AutoPoolKept(stranded) {
		t.Fatal("a pool for another installation is said to be kept: deleting it would be refused, though nothing would make it again")
	}
	st := h.c.AutoPoolStatus()
	if len(st.Findings) != 1 || st.Findings[0].Code != scheduler.FindingNameTaken || !strings.Contains(st.Findings[0].Fix, "delete pool zoomies-medium") {
		t.Fatalf("findings = %+v; want the name held by the old installation's pool, and what to delete", st.Findings)
	}
	if prob := h.problem(t, "pool.auto_blocked"); !strings.Contains(prob.Fix, "delete pool zoomies-medium") {
		t.Fatalf("the problem is %+v", prob)
	}

	// Once it is gone the pool for the new installation is made.
	if _, _, err := h.st.DeletePool(h.ctx, stranded.ID); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); p.InstallationID != globex.ID || !p.Enabled || p.MaxRunners != 5 {
		t.Fatalf("the new pool is %+v; want globex's, in use, with room for five", p)
	}
	if n := len(h.c.AutoPoolStatus().Findings); n != 0 {
		t.Fatalf("%d findings once the old pool was gone", n)
	}
}

func TestAFencedControllerWorksOutWhatItWouldDoAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	host := h.mediumHost("build-1")
	h.fence("restored from a backup")

	h.autoPass()
	if h.hasPool("zoomies-medium") {
		t.Fatal("a fenced controller made a pool")
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.SizeClass.Set() {
		t.Fatal("a fenced controller wrote a host's class")
	}
	if st := h.c.AutoPoolStatus(); len(st.Pending) != 1 {
		t.Fatalf("status = %+v; a fenced controller should still say what it would do", st)
	}
}

// ---------------------------------------------------------------------------
// Beside scheduling
// ---------------------------------------------------------------------------

// The reconciler and the scheduling pass run on their own loops, so they run at
// the same time. Neither may wait for the other, and neither may leave the
// other a state it cannot read: the figures are narrow writes on columns the
// scheduler only reads.
func TestReconcilingPoolsBesideSchedulingIsSafe(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.RegistrationConcurrency = 8 })
	h.mediumHost("build-1")
	h.autoPass()
	h.queuedJob(t, h.poolNamed("zoomies-medium"), []string{"self-hosted", "zoomies-medium"})

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 6 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 5 {
				if err := h.c.Reconcile(h.ctx); err != nil {
					errs <- err
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 5 {
				if err := h.c.ReconcileAutoPools(h.ctx); err != nil {
					errs <- err
				}
			}
		}()
		if i == 2 {
			h.mediumHost("build-2") // a host joins in the middle of it
		}
	}
	wg.Wait()
	h.c.lifecycleCalls.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a pass failed beside the other: %v", err)
	}
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); p.MaxRunners != 10 || !p.Enabled {
		t.Fatalf("the pool ended at %+v, want the two hosts' ten slots", p)
	}
}

// ---------------------------------------------------------------------------
// A host that joins again
// ---------------------------------------------------------------------------

// The join builds a host from what its agent declares and what its token pins,
// and neither knows about an edit an operator made afterwards. The comment on
// the code said the row kept its labels, and it did not.
func TestAHostThatJoinsAgainKeepsTheLabelsAnOperatorEditedAndItsClass(t *testing.T) {
	h := newHarness(t)
	h.installation()
	h.autoPoolsOn("on")

	first := joinRequest("build-1", h.joinToken(t, map[string]string{"zone": "eu"}, 8))
	first.CPUs, first.MemoryMB = 12, 32768
	joined, err := h.c.Join(h.ctx, first, "10.0.0.5")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	h.autoPass()
	before, err := h.st.GetHost(h.ctx, joined.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if before.SizeClass.Class != store.SizeMedium {
		t.Fatalf("the host's class is %q before it re-joins, want medium", before.SizeClass.Class)
	}

	edited := store.StringMap{"zone": "eu", "rack": "b4", "size": "large"}
	if err := h.st.PatchHost(h.ctx, joined.HostID, store.HostChanges{Labels: &edited}); err != nil {
		t.Fatal(err)
	}

	again := joinRequest("build-1", h.joinToken(t, map[string]string{"zone": "eu"}, 8))
	again.CPUs, again.MemoryMB = 12, 32768
	again.PreviousToken = joined.AgentToken
	resp, err := h.c.Join(h.ctx, again, "10.0.0.5")
	if err != nil {
		t.Fatalf("Join again: %v", err)
	}
	if resp.HostID != joined.HostID {
		t.Fatalf("the re-joined host is %s, want %s", resp.HostID, joined.HostID)
	}
	after, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Labels["rack"] != "b4" || after.Labels["size"] != "large" || after.Labels["zone"] != "eu" {
		t.Fatalf("the re-joined host's labels are %v; the operator's edits and the token's label should both be there", after.Labels)
	}
	if after.SizeClass.Class != store.SizeMedium {
		t.Fatalf("the re-joined host's class is %q, want the medium it held", after.SizeClass.Class)
	}
}

// An agent whose configuration says something new about a label it declares has
// that heard at its next join, over the value stored for it: carrying the stored
// one over everything made an edit of the agent's configuration do nothing for
// as long as the row lived. A label the agent never declared is still the
// operator's, and is kept.
func TestAnAgentsCurrentDeclarationBeatsAStoredLabelItDeclaresOnReJoin(t *testing.T) {
	h := newHarness(t)
	h.installation()

	first := joinRequest("build-1", h.joinToken(t, nil, 4))
	first.Labels = map[string]string{"rack": "b4", "zone": "eu"}
	joined, err := h.c.Join(h.ctx, first, "10.0.0.5")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	// An operator adds a tag of their own, and the agent's configuration moves
	// the rack and drops the zone.
	stored := store.StringMap{"rack": "b4", "zone": "eu", "gpu": "a100"}
	if err := h.st.PatchHost(h.ctx, joined.HostID, store.HostChanges{Labels: &stored}); err != nil {
		t.Fatal(err)
	}
	again := joinRequest("build-1", h.joinToken(t, nil, 4))
	again.Labels = map[string]string{"rack": "b5"}
	again.PreviousToken = joined.AgentToken
	if _, err := h.c.Join(h.ctx, again, "10.0.0.5"); err != nil {
		t.Fatalf("Join again: %v", err)
	}
	after, _ := h.st.GetHost(h.ctx, joined.HostID)
	if after.Labels["rack"] != "b5" {
		t.Errorf("rack = %q after a re-join; the agent's configuration now says b5", after.Labels["rack"])
	}
	if after.Labels["gpu"] != "a100" {
		t.Errorf("gpu = %q after a re-join; the agent never declared it, so it is the operator's and stays", after.Labels["gpu"])
	}
	if after.Labels["zone"] != "eu" {
		t.Errorf("zone = %q after a re-join; a label dropped from the agent's configuration stays until it is removed by hand", after.Labels["zone"])
	}
}

// The token's label is the later decision and beats an operator's edit of the
// same key: that is what "the token pins it" has always meant.
func TestAJoinTokensLabelStillBeatsAnOperatorsEditOnReJoin(t *testing.T) {
	h := newHarness(t)
	h.installation()

	first := joinRequest("build-1", h.joinToken(t, map[string]string{"tier": "untrusted"}, 4))
	joined, err := h.c.Join(h.ctx, first, "10.0.0.5")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	edited := store.StringMap{"tier": "release"}
	if err := h.st.PatchHost(h.ctx, joined.HostID, store.HostChanges{Labels: &edited}); err != nil {
		t.Fatal(err)
	}
	again := joinRequest("build-1", h.joinToken(t, map[string]string{"tier": "untrusted"}, 4))
	again.PreviousToken = joined.AgentToken
	if _, err := h.c.Join(h.ctx, again, "10.0.0.5"); err != nil {
		t.Fatalf("Join again: %v", err)
	}
	after, _ := h.st.GetHost(h.ctx, joined.HostID)
	if after.Labels["tier"] != "untrusted" {
		t.Fatalf("tier = %q after a re-join; the token pinned it", after.Labels["tier"])
	}
}
