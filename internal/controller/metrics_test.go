package controller

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/eyupio/zoomies/internal/agent"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/eyupio/zoomies/internal/store"
)

// Every pool-labelled metric names the pool the same way.
//
// `zoomies_jobs_total` used the pool id while every other pool-labelled series
// used the name, so a PromQL query joining a pool's job count against its
// runner count on `pool` matched nothing at all -- silently, which is the worst
// way for a dashboard to be wrong. One helper decides what a pool label is now,
// and this holds it to names.
func TestPoolLabelsAreNamesEverywhere(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "zoomies-linux-x64")

	if got := h.c.poolLabel(pool.ID); got != pool.Name {
		t.Errorf("poolLabel(%q) = %q, want the pool's name %q", pool.ID, got, pool.Name)
	}

	// Work no pool claims is counted under one agreed literal rather than an
	// empty label, which Prometheus cannot tell from a bug.
	if got := h.c.poolLabel(""); got != UnmatchedPool {
		t.Errorf("poolLabel(\"\") = %q, want %q", got, UnmatchedPool)
	}

	// A pool deleted between the job finishing and the metric being written
	// still has to be counted somewhere, and its id is the only name left.
	if got := h.c.poolLabel("pool_goneaway"); got != "pool_goneaway" {
		t.Errorf("poolLabel of a missing pool = %q, want the id back", got)
	}
}

// The completion counter carries the pool's name, not its id.
func TestJobCompletionIsCountedUnderThePoolName(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "zoomies-linux-x64")

	h.c.observeJobCompletion(&store.Job{PoolID: pool.ID, Conclusion: "success"})

	if got := testutil.ToFloat64(h.c.metrics.jobsTotal.WithLabelValues(pool.Name, "success")); got != 1 {
		t.Errorf("zoomies_jobs_total{pool=%q} = %v, want 1", pool.Name, got)
	}
	if got := testutil.ToFloat64(h.c.metrics.jobsTotal.WithLabelValues(pool.ID, "success")); got != 0 {
		t.Errorf("zoomies_jobs_total is still counted under the pool id %q", pool.ID)
	}

	// And a job no pool claimed lands under the agreed literal.
	h.c.observeJobCompletion(&store.Job{Conclusion: "failure"})
	if got := testutil.ToFloat64(h.c.metrics.jobsTotal.WithLabelValues(UnmatchedPool, "failure")); got != 1 {
		t.Errorf("an unclaimed job was not counted under %q", UnmatchedPool)
	}
}

// gatherValue reads one sample from the controller's own registry, by metric
// name and label values, so a test asks the endpoint's question rather than
// the collector's.
func gatherValue(t *testing.T, c *Controller, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := c.Registry().Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metric:
		for _, m := range f.GetMetric() {
			for k, want := range labels {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == k && l.GetValue() == want {
						found = true
					}
				}
				if !found {
					continue metric
				}
			}
			switch {
			case m.Gauge != nil:
				return m.GetGauge().GetValue(), true
			case m.Counter != nil:
				return m.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}

func gatherHistogram(t *testing.T, c *Controller, name string, labels map[string]string) (uint64, float64, bool) {
	t.Helper()
	families, err := c.Registry().Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
	metric:
		for _, metric := range family.GetMetric() {
			for key, want := range labels {
				found := false
				for _, label := range metric.GetLabel() {
					if label.GetName() == key && label.GetValue() == want {
						found = true
					}
				}
				if !found {
					continue metric
				}
			}
			if metric.Histogram != nil {
				return metric.GetHistogram().GetSampleCount(), metric.GetHistogram().GetSampleSum(), true
			}
		}
	}
	return 0, 0, false
}

func TestImagePrewarmReportsCacheEfficiencyAndDuration(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	if n, err := h.c.PrewarmPool(h.ctx, pool); err != nil || n != 1 {
		t.Fatalf("PrewarmPool = %d, %v; want one host", n, err)
	}
	batch, err := h.c.PollTasks(h.ctx, host.ID, time.Millisecond)
	if err != nil || len(batch.Tasks) != 1 {
		t.Fatalf("PollTasks = %+v, %v; want the prewarm task", batch, err)
	}
	cached := true
	duration := 7 * time.Millisecond
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: batch.Tasks[0].ID, Kind: agent.TaskPrewarmImage, OK: true,
		PrewarmCached: &cached, PrewarmDuration: &duration,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	labels := map[string]string{"pool": pool.Name, "backend": string(pool.Backend), "outcome": "cache_hit"}
	if got, ok := gatherValue(t, h.c, "zoomies_image_prewarms_total", labels); !ok || got != 1 {
		t.Errorf("cache-hit prewarms = %v (present=%v), want 1", got, ok)
	}
	count, sum, ok := gatherHistogram(t, h.c, "zoomies_image_prewarm_duration_seconds", labels)
	if !ok || count != 1 || sum < duration.Seconds() || sum > duration.Seconds()+0.001 {
		t.Errorf("prewarm duration = count %d, sum %v (present=%v), want one %v observation", count, sum, ok, duration.Seconds())
	}
}

// The backlog's depth and its age are different questions, and only the second
// one distinguishes a fleet that is working from a fleet that has stopped: ten
// jobs queued for four seconds and one job queued for forty minutes are the
// same number in `zoomies_jobs_queued`.
func TestTheQueueAgeGaugeIsTheOldestWaitInThePool(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "zoomies-linux-x64")

	// Two waiting jobs. The gauge is about the one that has waited longest,
	// which is the one somebody is complaining about.
	waiting := func(ago time.Duration) {
		t.Helper()
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: time.Now().UnixNano(),
			Repo:        "acme/widgets",
			Workflow:    "CI",
			JobName:     "build",
			Labels:      store.NormalizeLabels(pool.Labels),
			State:       store.JobQueued,
			PoolID:      pool.ID,
			Matched:     true,
			QueuedAt:    h.c.Now().Add(-ago),
		}); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	waiting(20 * time.Minute)
	waiting(time.Minute)

	got, ok := gatherValue(t, h.c, "zoomies_job_queue_age_seconds", map[string]string{"pool": pool.Name})
	if !ok {
		t.Fatal("zoomies_job_queue_age_seconds was not reported for the pool")
	}
	// The harness runs on a real clock, so the assertion is that this is the
	// twenty-minute wait and not the one-minute one, not that it is 1200.0.
	if want := (20 * time.Minute).Seconds(); got < want || got > want+30 {
		t.Errorf("queue age = %v, want the oldest wait, about %v", got, want)
	}
}

// A pool with nothing waiting reports zero rather than nothing at all: a series
// that disappears when the fleet is idle cannot carry an alert, because the
// rule stops matching exactly when it would otherwise fire.
func TestAnEmptyQueueReportsZeroRatherThanNothing(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "zoomies-linux-x64")

	got, ok := gatherValue(t, h.c, "zoomies_job_queue_age_seconds", map[string]string{"pool": pool.Name})
	if !ok {
		t.Fatal("an idle pool reported no queue age at all")
	}
	if got != 0 {
		t.Errorf("queue age on an idle pool = %v, want 0", got)
	}
}

// Rate-limit backoff is per installation, so the gauge is too: a fleet with two
// installations, one of them held, is a fleet half working, and the fleet-wide
// flag the plan described could not say which half.
func TestTheGitHubPauseGaugeNamesTheInstallationItIsHolding(t *testing.T) {
	h := newHarness(t)
	held := h.installationOn("acme", store.TargetOrg)
	free := h.installationOn("globex", store.TargetOrg)

	h.c.holdGitHub(held.ID, h.c.Now().Add(10*time.Minute))

	if got, ok := gatherValue(t, h.c, "zoomies_github_paused", map[string]string{"installation": held.ID}); !ok || got != 1 {
		t.Errorf("the held installation reported %v (present=%v), want 1", got, ok)
	}
	if got, ok := gatherValue(t, h.c, "zoomies_github_paused", map[string]string{"installation": free.ID}); !ok || got != 0 {
		t.Errorf("the installation that is not held reported %v (present=%v), want 0", got, ok)
	}
}

// A pass that fails observes no duration, so without this the difference
// between a controller deciding nothing and a controller with nothing to decide
// is invisible: the duration series goes quiet either way.
func TestAFailedReconcilePassIsCounted(t *testing.T) {
	h := newHarness(t)
	// A closed store fails the snapshot, which is the first thing a pass does.
	if err := h.st.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	h.c.reconcileNow(h.ctx)

	if got, _ := gatherValue(t, h.c, "zoomies_reconcile_errors_total", nil); got != 1 {
		t.Errorf("zoomies_reconcile_errors_total = %v after a failed pass, want 1", got)
	}
}

// Labels are the one thing about a metric that cannot be fixed later: a
// repository or workflow name here multiplies every series by the number of
// repositories in the organisation, and a scraper that has ingested them keeps
// them for its retention window whatever the next release does. The allowed set
// is small and deliberate, and this holds the whole registry to it rather than
// one family at a time -- the older test covered the startup histograms alone,
// and every series added since was on trust.
//
// It reads the registry's *descriptors* rather than a scrape. Written the
// obvious way -- gather, then look at the labels on what came back -- it passed
// with a `repository` label added to a counter, because a vector with no
// observations yet reports nothing at all, and a bad label would only be caught
// on the day something happened to increment it.
func TestNoMetricDeclaresAnUnboundedLabel(t *testing.T) {
	h := newHarness(t)

	allowed := map[string]bool{
		"pool": true, "state": true, "conclusion": true, "direction": true,
		"status": true, "installation": true, "result": true, "backend": true,
		"outcome": true, "version": true, "commit": true,
		// Elastic CPU mode is the closed off/observe/automatic policy set.
		"mode": true,
		// Registered hosts are operator-managed fleet entities like pools;
		// ephemeral runner/container IDs remain excluded.
		"host": true,
		// A provider is a configuration row somebody wrote, like a pool, and
		// there are a handful of them. "kind" is the five operations the
		// provider contract defines, which is a closed set in the source --
		// neither grows with the fleet's work, which is what this test is
		// about; a machine id would, and is deliberately absent.
		"provider": true, "kind": true,
		// "domain" is two values and "fault" is the closed set in
		// store.FaultKinds. Both are constants in the source rather than
		// anything the fleet's work produces, which is the distinction this
		// test draws: a job id would multiply every series by the day's work,
		// and nine categories do not.
		"domain": true, "fault": true,
		// "trigger" is the two ways a re-run is asked for -- the button and
		// the setting -- and both are constants in the source. What it must
		// never become is who asked, which would grow with the people.
		"trigger": true,
		// "limit" is the three per-host agent limits -- rate, poll and
		// runners -- constants in the source. The host that met one is in the
		// log line instead, because refusals are exactly where an unbounded
		// label would do its damage.
		"limit": true,
		// "class" and "ran_class" are the three size classes, "basis" the four
		// ways a job's class is decided, and "from" and "to" two of those
		// classes. All are closed sets written in the source. What they must
		// never become is the job, the workflow or the repository a class was
		// worked out for, which is what the job's own page is for.
		"class": true, "ran_class": true, "basis": true, "from": true, "to": true,
		// "code" is a Kennel Club check's code -- exposure.fork_code_ran and its
		// few siblings -- from the closed registry in internal/kennel, a list
		// somebody writes in the source and a test holds to the documentation. It
		// must never become what a finding is about, which is a repository or a
		// run: that is what the repository's own page is for, and a label of
		// repositories would multiply every series by the fleet's size.
		"code": true,
	}

	descs := make(chan *prometheus.Desc, 256)
	go func() {
		h.c.Registry().Describe(descs)
		close(descs)
	}()

	labels := regexp.MustCompile(`variableLabels: \{([^}]*)\}`)
	name := regexp.MustCompile(`fqName: "([^"]*)"`)
	var checked int
	for d := range descs {
		s := d.String()
		m := name.FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("could not read a metric name out of %s", s)
		}
		// The Go runtime and process collectors are the library's, and their
		// label names are not ours to police.
		if strings.HasPrefix(m[1], "go_") || strings.HasPrefix(m[1], "process_") {
			continue
		}
		checked++
		v := labels.FindStringSubmatch(s)
		if v == nil || v[1] == "" {
			continue
		}
		for _, l := range strings.Split(v[1], ",") {
			if l = strings.TrimSpace(l); l != "" && !allowed[l] {
				t.Errorf("%s declares the label %q, which is not in the bounded set: a label whose values grow with the fleet's work multiplies every series by it",
					m[1], l)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Zoomies metric descriptors were collected; the test is looking in the wrong place")
	}
}

// Cleanup is the half of a runner's life that fails on the host rather than in
// the fleet, so it is invisible in every other series here: those count what
// the fleet decided, and this counts what the host managed. The row already
// carries the failure and the problems drawer already names it; neither is a
// rate, and "one host has been failing to remove containers all week" is a rate
// question.
func TestCleanupOutcomesAreCounted(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	// The row is finished with, which is the case the record exists for: the
	// slot is already free, so a failed remove leaves a container on the host
	// and no state to move.
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "removed"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}

	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_1", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: false, Error: "the daemon refused: container is in use",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if got, _ := gatherValue(t, h.c, "zoomies_runner_cleanups_total", map[string]string{"outcome": "failed"}); got != 1 {
		t.Errorf("failed cleanups = %v after one refusal, want 1", got)
	}

	// And the retry that works is counted as the success it is, rather than
	// leaving the failure standing alone as though the runner were still there.
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_2", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: true, State: store.RunnerRemoved,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if got, _ := gatherValue(t, h.c, "zoomies_runner_cleanups_total", map[string]string{"outcome": "succeeded"}); got != 1 {
		t.Errorf("successful cleanups = %v after the retry worked, want 1", got)
	}
}

// A runner's startup timings are read off the row the store handed back, not
// off whatever the caller was holding.
//
// The create result is what stamps container_started_at, and it is also what
// races GitHub's in_progress delivery -- which is how a job start came to be
// dropped in the first place. The webhook handler's copy of the runner is
// several writes old by the time the start is applied, so taking the container
// time from it would lose the container-to-registered timing for exactly the
// runners whose startup was slow enough to race.
func TestRunnerStartupTimingsAreReadOffTheRowTheStoreReturned(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")

	started := time.Now().Add(-30 * time.Second)
	registered := started.Add(20 * time.Second)
	row := &store.Runner{
		PoolID:             pool.ID,
		State:              store.RunnerBusy,
		ContainerStartedAt: &started,
		RegisteredAt:       &registered,
	}

	if n := testutil.CollectAndCount(h.c.metrics.containerToRegistered); n != 0 {
		t.Fatalf("the container timing already has %d series before anything was observed", n)
	}
	h.c.observeRunnerReady(h.ctx, row)
	if n := testutil.CollectAndCount(h.c.metrics.containerToRegistered); n != 1 {
		t.Errorf("container-to-registered has %d series, want 1: the timing was taken from a copy that did not have it", n)
	}
	if n := testutil.CollectAndCount(h.c.metrics.registeredToReady); n != 1 {
		t.Errorf("registered-to-ready has %d series, want 1", n)
	}
}

// A controller's writes reach its histograms. The store measures, but it is
// the controller that owns the registry, and a histogram registered and never
// observed would scrape as a writer that is never used -- the one reading
// that says everything is fine.
func TestTheWritersQueueIsOnTheScrape(t *testing.T) {
	h := newHarness(t)
	beforeWait, _, _ := gatherHistogram(t, h.c, "zoomies_store_write_wait_seconds", nil)
	beforeHeld, _, _ := gatherHistogram(t, h.c, "zoomies_store_write_held_seconds", nil)

	h.fleet() // installs, pools and a host: writes, all through the one writer

	wait, _, ok := gatherHistogram(t, h.c, "zoomies_store_write_wait_seconds", nil)
	if !ok || wait <= beforeWait {
		t.Errorf("zoomies_store_write_wait_seconds counted %d writes after the fleet was seeded, and %d before; the writer's queue never reached the scrape", wait, beforeWait)
	}
	held, _, ok := gatherHistogram(t, h.c, "zoomies_store_write_held_seconds", nil)
	if !ok || held <= beforeHeld {
		t.Errorf("zoomies_store_write_held_seconds counted %d writes after the fleet was seeded, and %d before; every write has a hold as well as a wait", held, beforeHeld)
	}
}

// Queued-to-create is how long a job waited before the fleet started a runner
// for it -- the first figure anyone reads when the queue feels slow. Nothing
// tested it, so this pins it before the read that feeds it moves: one
// observation per runner created, measured from the job the pool was serving.
func TestEveryRunnerCreatedForAWaitingJobRecordsHowLongItWaited(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	// Two runners wanted outright, so the pass creates whatever the demand
	// arithmetic makes of the jobs; the metric reads the job the pool is
	// serving either way.
	pool.MinRunners = 2
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	h.measuredHost("measured", 8, 16384, 4, enforcesEverything)
	h.queuedJob(t, pool, pool.Labels) // queued two minutes ago

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	created := 0
	rs, err := h.st.ListRunnersForPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.State.Live() || r.State == store.RunnerFailed {
			created++
		}
	}
	if created == 0 {
		t.Fatal("the pass created no runner; the fixture is not exercising a create")
	}
	n, sum, ok := gatherHistogram(t, h.c, "zoomies_runner_queued_to_create_seconds", map[string]string{"pool": pool.Name})
	if !ok || int(n) != created {
		t.Fatalf("queued-to-create has %d observation(s) for %d runner(s) created; want one per runner", n, created)
	}
	if mean := sum / float64(n); mean < 100 || mean > 200 {
		t.Errorf("queued-to-create averaged %.0fs; the jobs were queued two minutes before the pass", mean)
	}
}
