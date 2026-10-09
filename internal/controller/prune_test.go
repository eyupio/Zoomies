package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// The prune loop is the only caller of five delete queries, and its shape
// carries two rules that live nowhere else: a zero window means keep
// everything, and a pruned runner is announced so the Runners page stops
// showing rows that no longer exist. Neither was tested, and both fail
// silently: history that quietly disappears looks like history nobody wrote,
// and a stale page looks like a page nobody reloaded.
func TestPruningHonoursEachWindowAndKeepsEverythingWhenOneIsZero(t *testing.T) {
	h := newHarness(t)
	_, pool, _ := h.fleet()
	now := h.c.Now()

	seed := func() {
		t.Helper()
		if err := h.st.AppendScalingEvent(h.ctx, &store.ScalingEvent{
			PoolID: pool.ID, PoolName: pool.Name, To: 1, Reason: "old",
			CreatedAt: now.Add(-48 * time.Hour),
		}); err != nil {
			t.Fatalf("AppendScalingEvent: %v", err)
		}
		if err := h.st.RecordSample(h.ctx, store.FleetSample{At: now.Add(-48 * time.Hour), TotalRunners: 1}); err != nil {
			t.Fatalf("RecordSample: %v", err)
		}
		if err := h.st.RecordDelivery(h.ctx, &store.WebhookDelivery{
			DeliveryID: "old", Event: "workflow_job", Status: "accepted",
			ReceivedAt: now.Add(-48 * time.Hour),
		}); err != nil {
			t.Fatalf("RecordDelivery: %v", err)
		}
	}

	// A cleared window means "keep everything", which is what an operator who
	// emptied the setting meant -- not "delete everything older than now".
	h.c.UpdateConfig(func(c *config.Config) {
		c.Retention = config.Retention{}
	})
	seed()
	h.c.prune(h.ctx)

	events, err := h.st.ListScalingEvents(h.ctx, "", 10)
	if err != nil {
		t.Fatalf("ListScalingEvents: %v", err)
	}
	samples, err := h.st.ListSamples(h.ctx, now.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("ListSamples: %v", err)
	}
	deliveries, err := h.st.ListDeliveries(h.ctx, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(events) != 1 || len(samples) != 1 || len(deliveries) != 1 {
		t.Fatalf("a zero window deleted history: %d scaling events, %d samples, %d deliveries left, want 1 of each",
			len(events), len(samples), len(deliveries))
	}

	// And a window that is set takes exactly what is older than it.
	h.c.UpdateConfig(func(c *config.Config) {
		c.Retention = config.Retention{
			ScalingEvents: 24 * time.Hour, Samples: 24 * time.Hour, Webhooks: 24 * time.Hour,
			Jobs: 24 * time.Hour, Runners: 24 * time.Hour,
		}
	})
	h.c.prune(h.ctx)

	if events, err = h.st.ListScalingEvents(h.ctx, "", 10); err != nil || len(events) != 0 {
		t.Errorf("scaling events after the prune = %d (err %v), want none: they follow the scaling-events window", len(events), err)
	}
	if samples, err = h.st.ListSamples(h.ctx, now.Add(-72*time.Hour)); err != nil || len(samples) != 0 {
		t.Errorf("samples after the prune = %d (err %v), want none", len(samples), err)
	}
	if deliveries, err = h.st.ListDeliveries(h.ctx, "", 10); err != nil || len(deliveries) != 0 {
		t.Errorf("deliveries after the prune = %d (err %v), want none", len(deliveries), err)
	}
}

// Every pruned runner is announced. The delete happens in the database, which
// tells nobody, so without this the Runners page keeps showing runners that
// were deleted an hour ago until somebody reloads it.
func TestAPrunedRunnerIsAnnounced(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "removed"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}

	sub := h.listen(events.KindRunnerDeleted)

	// An hour of retention and an hour on the clock: the row is old enough to
	// go without the test having to wait for it.
	h.c.UpdateConfig(func(c *config.Config) {
		c.Retention = config.Retention{Runners: time.Hour}
	})
	h.advance(2 * time.Hour)
	h.c.prune(h.ctx)

	select {
	case <-sub.C:
	case <-time.After(2 * time.Second):
		t.Fatal("the pruned runner was never announced; every open Runners page still shows it")
	}
}

// A prune that takes thousands of rows must not take every open tab with it.
//
// A subscriber's queue is 256 deep and the bus drops a subscriber that falls
// behind. The hourly prune deletes everything past the retention window in one
// pass, and announcing each row filled that queue several times over: every
// tab was cut off, showed itself as disconnected, reconnected and refetched six
// endpoints. The storm was the announcement rather than the deletion, so the
// announcement is bounded.
func TestABulkPruneAnnouncesOnceRatherThanDroppingEverySubscriber(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()

	for i := range announceEach + 5 {
		r := h.runnerRow(pool, host, store.RunnerIdle)
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "removed"); err != nil {
			t.Fatalf("TransitionRunner %d: %v", i, err)
		}
	}

	sub := h.listen(events.KindRunnerDeleted, events.KindResync)
	h.c.UpdateConfig(func(c *config.Config) {
		c.Retention = config.Retention{Runners: time.Hour}
	})
	h.advance(2 * time.Hour)
	h.c.prune(h.ctx)

	// One frame, and it is the one that means "fetch the resources again".
	select {
	case ev := <-sub.C:
		if ev.Kind != events.KindResync {
			t.Fatalf("the first frame was %q; a row-by-row announcement of a bulk prune fills every subscriber's queue and cuts it off", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a bulk prune announced nothing at all, so every open page keeps showing rows that are gone")
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a second frame (%q) followed the resync; one is the whole point", ev.Kind)
	case <-time.After(200 * time.Millisecond):
	}
}

// A machine's row outlives its resource on purpose: it is the only record of
// what was rented, when, and what it cost, and an operator reconciling a
// hypervisor bill against the fleet is reading exactly that. What retention
// takes is the confirmed-gone rows, and never one that still names a resource.
func TestPruningKeepsEveryMachineThatMayStillHaveAResource(t *testing.T) {
	h := newHarness(t)
	row := h.providerRow(t, "lab")
	now := h.c.Now()

	gone := &store.Machine{ProviderID: row.ID, State: store.MachinePlanned}
	if err := h.st.CreateMachine(h.ctx, gone); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	if err := h.st.SetMachineResource(h.ctx, gone.ID, "zone-a", "143", "fp", "ctl_test"); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}
	if _, err := h.st.ConfirmMachineDeleted(h.ctx, gone.ID, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("ConfirmMachineDeleted: %v", err)
	}
	for _, to := range []store.MachineState{store.MachineFailed, store.MachineDeleted} {
		if _, err := h.st.TransitionMachine(h.ctx, gone.ID, to, ""); err != nil {
			t.Fatalf("TransitionMachine(%s): %v", to, err)
		}
	}

	// Failed, old, and still naming a resource: the one row a prune must never
	// take, because deleting it is how a fleet loses a machine it is paying for.
	kept := &store.Machine{ProviderID: row.ID, State: store.MachinePlanned}
	if err := h.st.CreateMachine(h.ctx, kept); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	if err := h.st.SetMachineResource(h.ctx, kept.ID, "zone-a", "144", "fp", "ctl_test"); err != nil {
		t.Fatalf("SetMachineResource: %v", err)
	}
	if _, err := h.st.TransitionMachine(h.ctx, kept.ID, store.MachineFailed, "the guest never joined"); err != nil {
		t.Fatalf("TransitionMachine: %v", err)
	}

	h.c.UpdateConfig(func(c *config.Config) { c.Retention = config.Retention{Machines: 24 * time.Hour} })
	h.c.prune(h.ctx)

	if _, err := h.st.GetMachine(h.ctx, gone.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a machine confirmed gone 48 hours ago survived a 24-hour window: %v", err)
	}
	if _, err := h.st.GetMachine(h.ctx, kept.ID); err != nil {
		t.Errorf("a failed machine that still names a resource was pruned: %v", err)
	}
}

// An update that is still in flight is the only record that one is, and the
// controller's timeout is what closes it, not its age. The finished ones are
// history and follow retention.update_attempts.
func TestPruningTakesOldUpdateAttemptsButNeverAnOpenOne(t *testing.T) {
	h := newHarness(t)
	finished := &store.UpdateAttempt{Scope: store.UpdateScopeHost, HostID: "host_a", ToVersion: "v1.3.5", Trigger: store.UpdateTriggerManual}
	open := &store.UpdateAttempt{Scope: store.UpdateScopeController, ToVersion: "v1.3.5", Trigger: store.UpdateTriggerManual}
	for _, a := range []*store.UpdateAttempt{finished, open} {
		if err := h.st.CreateUpdateAttempt(h.ctx, a); err != nil {
			t.Fatalf("CreateUpdateAttempt: %v", err)
		}
	}
	if ok, err := h.st.FinishUpdateAttempt(h.ctx, finished.ID, store.UpdateSucceeded, ""); err != nil || !ok {
		t.Fatalf("FinishUpdateAttempt = %v, %v", ok, err)
	}

	h.c.UpdateConfig(func(c *config.Config) { c.Retention = config.Retention{UpdateAttempts: 24 * time.Hour} })
	h.advance(12 * time.Hour)
	h.c.prune(h.ctx)
	if got, err := h.st.ListUpdateAttempts(h.ctx, "", "", 10); err != nil || len(got) != 2 {
		t.Fatalf("inside the window %d attempts remain (%v), want both", len(got), err)
	}

	h.advance(48 * time.Hour)
	h.c.prune(h.ctx)
	got, err := h.st.ListUpdateAttempts(h.ctx, "", "", 10)
	if err != nil || len(got) != 1 || got[0].ID != open.ID {
		t.Fatalf("after the window %+v remain (%v), want only the open attempt", got, err)
	}
}

// A halted rollout is a fleet waiting for a person, and the only record that
// it is. It follows retention.update_attempts once it has ended, like the
// attempts it was made of, and not before.
func TestPruningTakesOldRolloutsButNeverAnOpenOne(t *testing.T) {
	h := newHarness(t)
	finished := &store.UpdateRollout{Target: "v1.3.5", Trigger: store.UpdateTriggerManual}
	if err := h.st.CreateUpdateRollout(h.ctx, finished); err != nil {
		t.Fatalf("CreateUpdateRollout: %v", err)
	}
	if ok, err := h.st.FinishUpdateRollout(h.ctx, finished.ID, store.RolloutDone); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v", ok, err)
	}
	open := &store.UpdateRollout{Target: "v1.3.6", Trigger: store.UpdateTriggerAuto}
	if err := h.st.CreateUpdateRollout(h.ctx, open); err != nil {
		t.Fatalf("CreateUpdateRollout: %v", err)
	}
	if ok, err := h.st.HaltUpdateRollout(h.ctx, open.ID, "a host did not come back"); err != nil || !ok {
		t.Fatalf("HaltUpdateRollout = %v, %v", ok, err)
	}

	h.c.UpdateConfig(func(c *config.Config) { c.Retention = config.Retention{UpdateAttempts: 24 * time.Hour} })
	h.advance(12 * time.Hour)
	h.c.prune(h.ctx)
	if got, err := h.st.OpenUpdateRollout(h.ctx); err != nil || got.ID != open.ID {
		t.Fatalf("inside the window the open rollout = %+v (%v), want it kept", got, err)
	}

	h.advance(48 * time.Hour)
	h.c.prune(h.ctx)
	if got, err := h.st.OpenUpdateRollout(h.ctx); err != nil || got.ID != open.ID || got.State != store.RolloutHalted {
		t.Fatalf("after the window the open rollout = %+v (%v), want the halted one kept", got, err)
	}
	// Cancelling the open one makes it prunable, so a cutoff a year out takes
	// exactly one row: the finished rollout went in the pass above.
	if ok, err := h.st.FinishUpdateRollout(h.ctx, open.ID, store.RolloutCancelled); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v", ok, err)
	}
	if n, err := h.st.PruneUpdateRollouts(h.ctx, time.Now().Add(365*24*time.Hour)); err != nil || n != 1 {
		t.Errorf("PruneUpdateRollouts = %d, %v, want only the rollout just cancelled to remain", n, err)
	}
}

// The prune pass is what rolls usage up. Without it the roll-up never moves,
// the report falls back to rows a week's retention has already taken, and last
// month's runner-hours quietly disappear.
func TestPruningRollsUsageUpSoARunnerOutlivesItsRow(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := &store.Runner{PoolID: pool.ID, HostID: host.ID, Name: "rolled-up"}
	if err := h.st.CreateRunner(h.ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	for _, to := range []store.RunnerState{store.RunnerRegistering, store.RunnerRemoved} {
		if _, err := h.st.TransitionRunner(h.ctx, r.ID, to, ""); err != nil {
			t.Fatalf("TransitionRunner(%s): %v", to, err)
		}
	}
	for _, side := range []bool{true, false} {
		if _, err := h.st.ConfirmRunnerCleanup(h.ctx, r.ID, side); err != nil {
			t.Fatalf("ConfirmRunnerCleanup: %v", err)
		}
	}

	h.advance(10 * 24 * time.Hour)
	h.c.UpdateConfig(func(c *config.Config) { c.Retention = config.Retention{Runners: 7 * 24 * time.Hour} })
	h.c.prune(h.ctx)

	if _, err := h.st.GetRunner(h.ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a runner ten days gone survived a seven-day window: %v", err)
	}
	_, until, ok, err := h.st.UsageRollupRange(h.ctx)
	if err != nil || !ok || until.Before(h.c.Now().Add(-24*time.Hour)) {
		t.Fatalf("the prune did not roll usage up to today: until %v, ok %v, %v", until, ok, err)
	}
}

// A job the jobs retention takes is counted in the installation report's
// roll-up by the same pass, so a month's report still has it. The pass that
// could prune it before counting it -- a roll-up that failed, or one that has
// not reached its day -- prunes nothing instead.
func TestPruningCountsAJobForTheInstallationReportBeforeDeletingIt(t *testing.T) {
	h := newHarness(t)
	inst, pool, _ := h.fleet()
	queued := h.c.Now()
	done := queued.Add(time.Minute)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{GitHubJobID: 77, Repo: "acme/app", State: store.JobCompleted,
		Matched: true, InstallationID: inst.ID, PoolID: pool.ID, QueuedAt: queued, StartedAt: &done,
		CompletedAt: &done, RunnerName: "GitHub Actions 1"}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	h.advance(10 * 24 * time.Hour)
	h.c.UpdateConfig(func(c *config.Config) { c.Retention = config.Retention{Jobs: 7 * 24 * time.Hour} })
	h.c.prune(h.ctx)

	if _, n, err := h.st.ListJobs(h.ctx, store.JobFilter{}, store.Page{Limit: 10}); err != nil || n != 0 {
		t.Fatalf("%d jobs left after a seven-day window, %v; want the ten-day-old one pruned", n, err)
	}
	rep, err := h.st.InstallationReport(h.ctx, inst.ID, queued, h.c.Now())
	if err != nil {
		t.Fatalf("InstallationReport: %v", err)
	}
	if rep.Counts.Observed != 1 || rep.Counts.RanElsewhere != 1 {
		t.Fatalf("after the prune the report counts %+v; want the pruned job, from the roll-up", rep.Counts)
	}
}
