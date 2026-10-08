package controller

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// listen subscribes to the bus the way the SSE endpoint does, for the kinds a
// test cares about.
func (h *harness) listen(kinds ...events.Kind) *events.Subscription {
	h.t.Helper()
	ctx, cancel := context.WithCancel(h.ctx)
	h.t.Cleanup(cancel)
	return h.c.Events().Subscribe(ctx, events.SubscribeOptions{Kinds: kinds})
}

// nextOfKind returns the next event of a kind, skipping the others, or fails.
func nextOfKind(t *testing.T, sub *events.Subscription, kind events.Kind) map[string]any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				t.Fatalf("the subscription closed before a %s event arrived", kind)
			}
			if e.Kind != kind {
				continue
			}
			var out map[string]any
			if err := json.Unmarshal(e.Data, &out); err != nil {
				t.Fatalf("%s payload is not a JSON object: %v (%s)", kind, err, e.Data)
			}
			return out
		case <-deadline:
			t.Fatalf("no %s event arrived", kind)
		}
	}
}

// nothingFor asserts the subscription stays quiet for a moment. The bus hands
// events to a subscriber synchronously, so a pass that published something
// has done so before it returns and a short wait is not a race.
func nothingFor(t *testing.T, sub *events.Subscription) {
	t.Helper()
	select {
	case e := <-sub.C:
		t.Fatalf("an event arrived when nothing had changed: %s %s", e.Kind, e.Data)
	case <-time.After(100 * time.Millisecond):
	}
}

// The Overview's numbers and the problems bell are computed, not stored, so no
// row change can announce them. A reconcile pass has to, and it has to keep
// quiet when nothing moved -- a frame every ten seconds saying the same thing
// is noise the browser would repaint for.
func TestAReconcilePassTellsTheOverviewWhatChanged(t *testing.T) {
	h := newHarness(t)
	inst, _, _ := h.fleet()
	sub := h.listen(events.KindStats, events.KindProblems)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	stats := nextOfKind(t, sub, events.KindStats)
	if got := stats["queued_jobs"]; got != float64(0) {
		t.Errorf("first stats frame queued_jobs = %v, want 0", got)
	}
	// A fleet that has never received a webhook has one thing to say, and the
	// frame carries the list in the shape GET /problems returns.
	problems := nextOfKind(t, sub, events.KindProblems)
	if problems["ok"] != false || len(itemsOf(problems)) != 1 {
		t.Errorf("first problems frame = %v, want the one warning a fleet without a webhook has", problems)
	}

	// Nothing changed, so nothing is said.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	nothingFor(t, sub)

	// A job arriving changes the queue depth; the next pass says so.
	h.deliverJob(jobEvent{Action: "queued", JobID: 1, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile with a job: %v", err)
	}
	stats = nextOfKind(t, sub, events.KindStats)
	if got := stats["queued_jobs"]; got != float64(1) {
		t.Errorf("stats frame after a job queued: queued_jobs = %v, want 1", got)
	}
	// That delivery was the first webhook, so the warning about not having one
	// is gone, and the bell has to be told that too.
	problems = nextOfKind(t, sub, events.KindProblems)
	if problems["ok"] != true {
		t.Errorf("problems frame after the first webhook = %v, want ok", problems)
	}

	// A dangerous setting is a problem the moment it exists, and the frame
	// carries the whole list in the shape GET /problems returns.
	h.st.CreatePool(h.ctx, &store.Pool{
		Name: "root", InstallationID: inst.ID, Labels: []string{"root"},
		Backend: store.BackendDocker, Image: "img", MaxRunners: 1,
		IdleTimeout: store.Duration(time.Minute), Ephemeral: true,
		DockerMode: store.DockerNone, RunAsRoot: true, Enabled: true,
	})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile with a dangerous pool: %v", err)
	}
	problems = nextOfKind(t, sub, events.KindProblems)
	if problems["ok"] != false {
		t.Fatalf("problems frame after a dangerous pool = %v, want ok=false", problems)
	}
	items := itemsOf(problems)
	found := false
	for _, raw := range items {
		if item, _ := raw.(map[string]any); item["code"] == "pool.dangerous" {
			found = true
		}
	}
	if !found {
		t.Errorf("problems frame does not name the dangerous pool: %v", items)
	}
}

func itemsOf(problems map[string]any) []any {
	items, _ := problems["items"].([]any)
	return items
}

// A controller nobody is watching should not do the watchers' work: with no
// stream open, a pass computes neither payload and publishes nothing.
func TestNothingIsComputedForNobody(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	// With updating on, so that the status has something to say and there is a
	// reason to render it for somebody.
	withVersion(t, "1.3.0")
	h.inMode("manual")

	before := h.c.Events().LastID()
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.c.Events().LastID(); got != before {
		t.Errorf("a pass with no subscribers published %d event(s)", got-before)
	}
	if h.c.lastStats != nil || h.c.lastProblems != nil || h.c.lastHosts != nil {
		t.Error("a pass with no subscribers still computed the derived payloads")
	}
	if h.c.lastUpdates != nil {
		t.Error("a pass with no subscribers still rendered the update status")
	}
}

// What an update would take changes with the clock as well as with the list: a
// soak ends, and nothing is written when it does, so nothing but the pass could
// say so. It says it once. A frame every few seconds repeating itself would
// repaint a panel nobody has touched, and the panel repainting is the only
// reason to send one.
func TestUpdatesUpdatedIsSentOnlyWhenTheStatusChanges(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	withVersion(t, "1.3.0")
	h.inMode("auto")
	h.readTheList(releaseEntry("v1.3.2", whenAgo(6*time.Hour+30*time.Minute), completeAssets(t)...))
	sub := h.listen(events.KindUpdates)

	pass := func(doing string) {
		t.Helper()
		if err := h.c.Reconcile(h.ctx); err != nil {
			t.Fatalf("Reconcile %s: %v", doing, err)
		}
	}

	// The first pass with somebody watching says what there is, because nothing
	// has been sent yet.
	pass("the first time")
	first := nextOfKind(t, sub, events.KindUpdates)
	// The frame is the GET shape, which is the fields of the view and not a row
	// of anything: the panel drops it straight into what it holds.
	if first["mode"] != "auto" || first["soak"] != "24h" {
		t.Errorf("first frame mode %v and soak %v, want auto and 24h", first["mode"], first["soak"])
	}
	if target, _ := first["target"].(map[string]any); target["tag"] != "v1.3.2" || target["newer"] != true {
		t.Errorf("first frame target = %v, want v1.3.2, newer", first["target"])
	}
	if reason, _ := first["reason"].(string); !strings.HasPrefix(reason, "Waiting: v1.3.2 ") {
		t.Errorf("first frame reason = %q, want the sentence that says v1.3.2 is waiting", reason)
	}

	// Nothing has moved, so nothing is said.
	pass("with nothing changed")
	nothingFor(t, sub)

	// The soak ends. The release is the same, and the list is the same, and the
	// sentence is not.
	h.advance(18 * time.Hour)
	pass("after the soak")
	if reason, _ := nextOfKind(t, sub, events.KindUpdates)["reason"].(string); !strings.HasPrefix(reason, "Ready: v1.3.2 ") {
		t.Errorf("frame after the soak: reason = %q, want the sentence that says it can be taken now", reason)
	}
	pass("with the soak over and nothing else changed")
	nothingFor(t, sub)

	// An operator changing the mode is announced by the next pass as well, and
	// takes the offer away with it.
	h.inMode("off")
	pass("after the mode was switched off")
	off := nextOfKind(t, sub, events.KindUpdates)
	if off["mode"] != "off" || off["target"] != nil {
		t.Errorf("frame after switching off: mode %v, target %v, want off and no target", off["mode"], off["target"])
	}
}

// The two numbers an operator reads off the Hosts page are the two nothing
// writes a row for: the slots in use are counted from the runners table when a
// host is read, and the heartbeat is one column no publisher watches. Starting
// a runner therefore moved the bar on the card with nothing to announce it,
// and the page sat still while the fleet moved under it.
func TestAPassAnnouncesTheSlotsAHostHasFilled(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	sub := h.listen(events.KindHostUpdated)

	// The first pass introduces the host, because nothing has been sent yet.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	first := nextOfKind(t, sub, events.KindHostUpdated)
	if first["active_runners"] != float64(0) {
		t.Fatalf("first host frame active_runners = %v, want 0", first["active_runners"])
	}

	// A queued job puts a runner on that host. The runner's own frame says the
	// runner exists; only the host's says the machine has one fewer slot.
	h.deliverJob(jobEvent{Action: "queued", JobID: 1, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile with a job: %v", err)
	}
	got := nextOfKind(t, sub, events.KindHostUpdated)
	if got["active_runners"] != float64(1) {
		t.Errorf("host frame after a runner started: active_runners = %v, want 1", got["active_runners"])
	}
	if got["free"] != float64(host.Capacity-1) {
		t.Errorf("host frame free = %v, want %d", got["free"], host.Capacity-1)
	}
}

// A frame every ten seconds saying what the last one said is noise the browser
// repaints for, and there is one of these per host.
func TestAPassSaysNothingAboutAHostThatHasNotMoved(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	sub := h.listen(events.KindHostUpdated)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	nextOfKind(t, sub, events.KindHostUpdated)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	nothingFor(t, sub)
}

// An operator's change is announced the moment it is made. The pass that comes
// along a few seconds later must not say it again: every publisher records what
// it sent, and the diff is against that record rather than against the last
// pass.
func TestAChangeAlreadyAnnouncedIsNotAnnouncedAgain(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	sub := h.listen(events.KindHostUpdated)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	nextOfKind(t, sub, events.KindHostUpdated)

	// Exactly what the API handler does when somebody cordons a host: read it,
	// write the change, then announce the row it read.
	if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}
	cordoned, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	h.c.PublishHost(cordoned)
	if got := nextOfKind(t, sub, events.KindHostUpdated); got["cordoned"] != true {
		t.Fatalf("host frame after cordoning = %v, want cordoned", got)
	}

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile after cordoning: %v", err)
	}
	nothingFor(t, sub)
}

// A host that has gone is not something the pass should keep a record of, or
// the map grows for the life of the process on a fleet that churns machines.
func TestADeletedHostIsForgotten(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	sub := h.listen(events.KindHostUpdated, events.KindHostDeleted)

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	nextOfKind(t, sub, events.KindHostUpdated)

	if err := h.c.DeleteHost(h.ctx, host.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	nextOfKind(t, sub, events.KindHostDeleted)

	h.c.derivedMu.Lock()
	_, remembered := h.c.lastHosts[host.ID]
	h.c.derivedMu.Unlock()
	if remembered {
		t.Error("a deleted host is still remembered as something to diff against")
	}
}

// The runner grid shows pool and host names, and the fleet cache takes each
// runner event as the row to show. An event carrying only the ids would blank
// those two columns until the next full fetch.
func TestRunnerEventsNameThePoolAndTheHost(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	sub := h.listen(events.KindRunnerCreated)

	h.deliverJob(jobEvent{Action: "queued", JobID: 1, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := nextOfKind(t, sub, events.KindRunnerCreated)
	if got["pool_name"] != pool.Name || got["host_name"] != host.Name {
		t.Errorf("runner.created names pool %v and host %v, want %s and %s",
			got["pool_name"], got["host_name"], pool.Name, host.Name)
	}
	if _, ok := got["labels"].([]any); !ok {
		t.Errorf("runner.created labels = %v, want a list", got["labels"])
	}
}

// The one thing a host event exists to say is whether the agent is still
// there, and that is worked out from the heartbeat rather than stored. A frame
// without it repainted a silent host as healthy.
func TestHostEventsCarryTheHealthTheCardShows(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	sub := h.listen(events.KindHostUpdated)

	h.c.PublishHost(host)
	got := nextOfKind(t, sub, events.KindHostUpdated)
	if got["healthy"] != true {
		t.Errorf("host.updated healthy = %v, want true for a host that just heartbeat", got["healthy"])
	}
	if got["free"] != float64(host.Capacity) {
		t.Errorf("host.updated free = %v, want %d", got["free"], host.Capacity)
	}
	if _, ok := got["backend_info"].([]any); !ok {
		t.Errorf("host.updated backend_info = %v, want a list", got["backend_info"])
	}
}

// A runner row that goes with its pool, host or installation, or is pruned, used
// to go without a frame, so the Runners page kept showing runners that no
// longer existed until it was reloaded. Each is announced now, before the thing
// that took it.
func TestRunnerRowsThatVanishAreAnnounced(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	var ids []string
	for _, name := range []string{"one", "two"} {
		r := &store.Runner{PoolID: pool.ID, HostID: host.ID, Name: name, State: store.RunnerIdle}
		if err := h.st.CreateRunner(h.ctx, r); err != nil {
			t.Fatalf("CreateRunner: %v", err)
		}
		ids = append(ids, r.ID)
	}
	sub := h.listen(events.KindRunnerDeleted, events.KindPoolDeleted, events.KindInstallationDeleted)

	if err := h.c.DeletePool(h.ctx, pool.ID); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	var got []string
	for range ids {
		got = append(got, nextOfKind(t, sub, events.KindRunnerDeleted)["id"].(string))
	}
	slices.Sort(got)
	slices.Sort(ids)
	if !slices.Equal(got, ids) {
		t.Fatalf("runner.deleted for %v, want %v", got, ids)
	}
	if gone := nextOfKind(t, sub, events.KindPoolDeleted); gone["id"] != pool.ID {
		t.Fatalf("pool.deleted = %v, want %s after its runners", gone, pool.ID)
	}

	// The installation route goes through its pools.
	other := h.pool(inst, "other")
	r := &store.Runner{PoolID: other.ID, HostID: host.ID, Name: "three", State: store.RunnerFailed}
	if err := h.st.CreateRunner(h.ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if err := h.c.DeleteInstallation(h.ctx, inst.ID); err != nil {
		t.Fatalf("DeleteInstallation: %v", err)
	}
	if e := nextOfKind(t, sub, events.KindRunnerDeleted); e["id"] != r.ID {
		t.Fatalf("runner.deleted = %v, want %s", e, r.ID)
	}
	if e := nextOfKind(t, sub, events.KindPoolDeleted); e["id"] != other.ID {
		t.Fatalf("pool.deleted = %v, want %s", e, other.ID)
	}
	if e := nextOfKind(t, sub, events.KindInstallationDeleted); e["id"] != inst.ID {
		t.Fatalf("installation.deleted = %v, want %s", e, inst.ID)
	}
}

// Deleting a pool takes its queued demand with it too, and the Jobs and Queue
// pages learn about that job.updated frame the same way they learn about
// everything else -- no refresh needed to see it drop off the pool it just
// lost.
func TestDeletingAPoolAnnouncesTheQueuedJobsItTakesWithIt(t *testing.T) {
	h := newHarness(t)
	_, pool, _ := h.fleet()
	job, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 900, Repo: "acme/widgets", State: store.JobQueued,
		Matched: true, PoolID: pool.ID, QueuedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	sub := h.listen(events.KindJobUpdated, events.KindPoolDeleted)

	if err := h.c.DeletePool(h.ctx, pool.ID); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	got := nextOfKind(t, sub, events.KindJobUpdated)
	if got["id"] != job.ID {
		t.Fatalf("job.updated = %v, want id %s", got, job.ID)
	}
	if pid, present := got["pool_id"]; present && pid != "" {
		t.Fatalf("job.updated for a job whose pool was deleted = %v, want no pool_id", got)
	}
	if got["matched"] != false {
		t.Fatalf("job.updated matched = %v, want false", got["matched"])
	}
	if got["provisioning"] != store.ProvisioningDeleted {
		t.Fatalf("job.updated provisioning = %v, want %q", got["provisioning"], store.ProvisioningDeleted)
	}
	if gone := nextOfKind(t, sub, events.KindPoolDeleted); gone["id"] != pool.ID {
		t.Fatalf("pool.deleted = %v, want %s after its queued jobs", gone, pool.ID)
	}
}

// The prune is the quiet one: hourly, and a row it deletes is one the page
// may well be showing under "include removed".
func TestPrunedRunnersAreAnnounced(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	old := &store.Runner{PoolID: pool.ID, HostID: host.ID, Name: "old", State: store.RunnerRemoved}
	if err := h.st.CreateRunner(h.ctx, old); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	finished := time.Now().Add(-48 * time.Hour)
	old.FinishedAt = &finished
	if err := h.st.UpdateRunner(h.ctx, old); err != nil {
		t.Fatalf("UpdateRunner: %v", err)
	}
	h.c.UpdateConfig(func(c *config.Config) { c.Retention.Runners = 24 * time.Hour })

	sub := h.listen(events.KindRunnerDeleted)
	h.c.prune(h.ctx)
	if e := nextOfKind(t, sub, events.KindRunnerDeleted); e["id"] != old.ID {
		t.Fatalf("runner.deleted = %v, want the pruned runner %s", e, old.ID)
	}
}

// A machine spends most of its life waiting -- for a clone, for a guest to
// boot, for an agent to join -- and what moves on the page while it waits is
// elapsed time and the counts around it, neither of which writes a row. The
// pass's diff is what repaints it; without that, the one screen an operator
// watches while a machine is being built would be the one screen that did not
// move.
func TestAMachineWhoseViewMovedIsRepublishedWithoutAnyoneAskingFor(t *testing.T) {
	h := newHarness(t)
	_, row := h.machineFleet(t)
	h.machinePass(t)
	m := h.onlyMachine(t)

	sub := h.listen(events.KindMachineUpdated)
	// The first pass with somebody watching sends what it has -- the machine
	// moved while nobody was subscribed -- and the second says nothing, because
	// a frame per machine per pass would cost every open tab a repaint of a row
	// it already has.
	h.c.publishDerived(h.ctx)
	nextOfKind(t, sub, events.KindMachineUpdated)
	h.c.publishDerived(h.ctx)
	nothingFor(t, sub)

	// A column written by something that does not publish -- here the sweep's
	// own ownership stamp -- still reaches the page.
	if err := h.st.SetMachineOwnershipError(h.ctx, m.ID, "the resource says it belongs to another machine"); err != nil {
		t.Fatalf("SetMachineOwnershipError: %v", err)
	}
	h.c.publishDerived(h.ctx)
	frame := nextOfKind(t, sub, events.KindMachineUpdated)
	if frame["id"] != m.ID {
		t.Fatalf("the frame is about %v, want machine %s", frame["id"], m.ID)
	}
	if frame["provider_name"] != row.Name {
		t.Fatalf("the frame names provider %v, want %s: it has to be the shape the API returns",
			frame["provider_name"], row.Name)
	}
}
