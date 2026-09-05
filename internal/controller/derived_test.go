package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

	before := h.c.Events().LastID()
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.c.Events().LastID(); got != before {
		t.Errorf("a pass with no subscribers published %d event(s)", got-before)
	}
	if h.c.lastStats != nil || h.c.lastProblems != nil {
		t.Error("a pass with no subscribers still computed the derived payloads")
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
