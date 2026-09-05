package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The Playwright suite and a demo instance both need a fleet with something in
// it. This is that fixture, and it has to be the same fixture every time.
func TestSeedDemoBuildsAFleet(t *testing.T) {
	h := newHarness(t)

	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}

	pools, err := h.st.ListPools(h.ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("seeded %d pools, want 2", len(pools))
	}

	hosts, err := h.st.ListHosts(h.ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("seeded %d hosts, want 3", len(hosts))
	}

	runners := h.runners()
	if len(runners) != 12 {
		t.Fatalf("seeded %d runners, want 12", len(runners))
	}
	states := map[store.RunnerState]int{}
	for _, r := range runners {
		states[r.State]++
	}
	for _, want := range []store.RunnerState{
		store.RunnerProvisioning, store.RunnerRegistering, store.RunnerIdle,
		store.RunnerBusy, store.RunnerDraining, store.RunnerFailed, store.RunnerRemoved,
	} {
		if states[want] == 0 {
			t.Fatalf("no seeded runner is %q; the UI has nothing to render for that state", want)
		}
	}

	jobs, total, err := h.st.ListJobs(h.ctx, store.JobFilter{}, store.Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != 50 {
		t.Fatalf("seeded %d jobs, want 50", total)
	}
	var completed, queued, running, unmatched int
	for _, j := range jobs {
		switch j.State {
		case store.JobCompleted:
			completed++
		case store.JobQueued:
			queued++
		case store.JobInProgress:
			running++
		}
		if !j.Matched {
			unmatched++
		}
	}
	if completed == 0 || queued == 0 || running == 0 {
		t.Fatalf("job mix = %d completed, %d queued, %d running; the Overview needs all three",
			completed, queued, running)
	}
	if unmatched == 0 {
		t.Fatal("no seeded job is unmatched, so the problems drawer has nothing to show")
	}

	// The Overview's history and the audit page both need rows.
	evs, err := h.st.ListScalingEvents(h.ctx, "", 50)
	if err != nil {
		t.Fatalf("ListScalingEvents: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("no scaling history was seeded")
	}
	audit, auditTotal, err := h.st.ListAudit(h.ctx, store.AuditFilter{}, store.Page{Limit: 50})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if auditTotal == 0 || len(audit) == 0 {
		t.Fatal("no audit rows were seeded")
	}

	// Stats has to work over the fixture, since that is what the Overview does.
	if _, err := h.c.Stats(h.ctx, 24*time.Hour); err != nil {
		t.Fatalf("Stats over the fixture: %v", err)
	}
}

// Seeding twice must not double the fixture: the Playwright suite restarts the
// binary and would otherwise accumulate a fleet.
func TestSeedDemoIsIdempotent(t *testing.T) {
	h := newHarness(t)
	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("first SeedDemo: %v", err)
	}
	before := len(h.runners())

	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("second SeedDemo: %v", err)
	}
	if got := len(h.runners()); got != before {
		t.Fatalf("runners went from %d to %d on a second seed", before, got)
	}
}

// Fixtures appearing in somebody's real fleet would be indistinguishable from
// a compromise, so an instance that is already in use is refused.
func TestSeedDemoRefusesARealFleet(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.pool(inst, "production-linux")

	err := h.c.SeedDemo(h.ctx)
	if err == nil {
		t.Fatal("SeedDemo seeded an instance that already had a real pool")
	}
	if !strings.Contains(err.Error(), "production-linux") || !strings.Contains(err.Error(), SeedEnvVar) {
		t.Fatalf("error = %q, want it to name the pool and how to turn seeding off", err)
	}
	if got := len(h.runners()); got != 0 {
		t.Fatalf("the refused seed still wrote %d runners", got)
	}
}

// The Jobs page's drawer, its failed filter and the problems drawer's "runner
// lost" entry all need a fixture behind them, and the timeline needs one job
// whose story it can tell in full.
func TestSeedDemoGivesJobsStepsATimelineAndOneLostRunner(t *testing.T) {
	h := newHarness(t)
	if err := h.c.SeedDemo(h.ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}

	failed, total, err := h.st.ListJobs(h.ctx, store.JobFilter{FailedOnly: true}, store.Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total == 0 {
		t.Fatal("the seed has no failed job for the failed filter to find")
	}
	var lost *store.Job
	for _, j := range failed {
		if j.Conclusion == "failure" && j.FailedStep() == nil {
			t.Errorf("failed job %s names no failed step", j.ID)
		}
		if j.RunnerFault != "" {
			lost = j
		}
	}
	if lost == nil {
		t.Fatal("no seeded job lost its runner, so the runner_lost badge has no fixture")
	}

	events, err := h.c.JobEvents(h.ctx, lost.ID)
	if err != nil {
		t.Fatalf("JobEvents: %v", err)
	}
	kinds := kindsOfEvents(events)
	for _, want := range []store.JobEventKind{store.JobEventQueued, store.JobEventClaimed, store.JobEventStarted, store.JobEventRunnerLost, store.JobEventCompleted} {
		found := false
		for _, k := range kinds {
			found = found || k == want
		}
		if !found {
			t.Errorf("the lost-runner job's timeline %v lacks %s", kinds, want)
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].At.Before(events[i-1].At) {
			t.Errorf("timeline out of order at %d: %v after %v", i, events[i].At, events[i-1].At)
		}
	}

	running, _, err := h.st.ListJobs(h.ctx, store.JobFilter{States: []store.JobState{store.JobInProgress}}, store.Page{})
	if err != nil {
		t.Fatalf("ListJobs running: %v", err)
	}
	for _, j := range running {
		if len(j.Steps) == 0 || j.FailedStep() != nil {
			t.Errorf("running job %s has steps %+v, want some in flight and none failed", j.ID, j.Steps)
		}
	}
}
