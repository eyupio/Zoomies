package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// classFleet is a fleet with one host of each class it is given and the
// automatic pools they call for, with both switches in the mode a test wants.
type classFleet struct {
	pools map[store.SizeClass]*store.Pool
	hosts map[store.SizeClass]*store.Host
}

func (h *harness) largeHost(name string) *store.Host {
	return h.measuredHost(name, 32, 131072, 8, enforcesEverything)
}

// classFleetOf builds the fleet. Automatic pools are always on, because the
// pools are what there is to route to; size routing is the mode under test.
func (h *harness) classFleetOf(routing string, classes ...store.SizeClass) classFleet {
	h.t.Helper()
	h.installation()
	h.c.UpdateConfig(func(cfg *config.Config) {
		cfg.Scheduler.SizeRouting = routing
		cfg.Scheduler.AutoPools = scheduler.SizeOn
		cfg.Scheduler.SizeClassHold = 0
		// A pass that creates runners in several pools would otherwise create
		// one, because credentials are minted one at a time by default, and the
		// tests are about where a runner goes and not about that limit.
		cfg.Scheduler.RegistrationConcurrency = 8
	})
	f := classFleet{pools: map[store.SizeClass]*store.Pool{}, hosts: map[store.SizeClass]*store.Host{}}
	for _, class := range classes {
		switch class {
		case store.SizeSmall:
			f.hosts[class] = h.smallHost("tiny-1")
		case store.SizeMedium:
			f.hosts[class] = h.mediumHost("build-1")
		case store.SizeLarge:
			f.hosts[class] = h.largeHost("big-1")
		}
	}
	h.autoPass()
	for _, class := range classes {
		f.pools[class] = h.poolNamed("zoomies-" + string(class))
	}
	return f
}

func (h *harness) classFleet(routing string) classFleet {
	h.t.Helper()
	return h.classFleetOf(routing, store.SizeSmall, store.SizeMedium, store.SizeLarge)
}

func (h *harness) jobByGitHubID(id int64) *store.Job {
	h.t.Helper()
	j, err := h.st.GetJobByGitHubID(h.ctx, id)
	if err != nil {
		h.t.Fatalf("job %d: %v", id, err)
	}
	return j
}

// runnersIn counts the live runners of a pool.
func (h *harness) runnersIn(pool *store.Pool) int {
	n := 0
	for _, r := range h.runners() {
		if r.PoolID == pool.ID && r.State.Live() {
			n++
		}
	}
	return n
}

func sizedEntries(events []*store.JobEvent) []string {
	var out []string
	for _, e := range events {
		if e.Kind == store.JobEventSized {
			out = append(out, e.Message)
		}
	}
	return out
}

var baseLabels = []string{"self-hosted", "zoomies"}

// ---------------------------------------------------------------------------
// Off, watching, acting
// ---------------------------------------------------------------------------

func TestNoJobIsClassedWhileSizeRoutingIsOff(t *testing.T) {
	h := newHarness(t)
	_, pool, _ := h.fleet()

	h.deliverJob(jobEvent{Action: "queued", JobID: 7101, Name: "build", Workflow: "CI", Labels: pool.Labels})
	job := h.jobByGitHubID(7101)
	if job.SizeClass != "" || job.SizeBasis != "" || job.SizeReason != "" || job.RoutedClass != "" {
		t.Fatalf("a job was classed with size routing off: %+v", job)
	}
	if job.PoolID != pool.ID {
		t.Fatalf("the job is claimed by %q, want the operator's pool", job.PoolID)
	}
	if got := sizedEntries(h.timeline(job.ID)); len(got) != 0 {
		t.Fatalf("the timeline says how the job was classed with size routing off: %v", got)
	}
}

// Watching is the way to find out what routing would do to a fleet before it
// does it: the class is on the job, and the job goes exactly where it always
// went.
func TestWhileSizeRoutingIsOnlyWatchedAJobIsClassedAndGoesWhereItAlwaysDid(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeShadow)

	h.deliverJob(jobEvent{Action: "queued", JobID: 7102, Name: "build", Workflow: "CI", Labels: baseLabels})
	job := h.jobByGitHubID(7102)
	if job.SizeClass != store.SizeMedium || job.SizeBasis != store.SizeBasisDefault || job.RoutedClass != "" {
		t.Fatalf("a watched job = class %q basis %q routed %q; want the default class, recorded, and no route",
			job.SizeClass, job.SizeBasis, job.RoutedClass)
	}
	pools, _ := h.st.ListPools(h.ctx)
	want := scheduler.BestPool(pools, &store.Job{Labels: job.Labels, InstallationID: job.InstallationID})
	if want == nil || job.PoolID != want.ID {
		t.Fatalf("the job is claimed by %q; with routing watched it should be the pool it would have had without it (%v)", job.PoolID, want)
	}
	entries := sizedEntries(h.timeline(job.ID))
	if len(entries) != 1 || !strings.Contains(entries[0], "classed medium because") || !strings.Contains(entries[0], "only watching") {
		t.Fatalf("the timeline says %v; it should say how the job was classed and that nothing was sent anywhere", entries)
	}
}

func TestAJobIsSentToTheClassItNeedsWhenRoutingIsOn(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)

	h.deliverJob(jobEvent{Action: "queued", JobID: 7201, Name: "build", Workflow: "CI", Labels: baseLabels})
	h.deliverJob(jobEvent{Action: "queued", JobID: 7202, Name: "e2e", Workflow: "CI", Labels: []string{"self-hosted", store.SizeLarge.Label()}})

	plain, asked := h.jobByGitHubID(7201), h.jobByGitHubID(7202)
	if plain.SizeClass != store.SizeMedium || plain.SizeBasis != store.SizeBasisDefault ||
		plain.RoutedClass != store.SizeMedium || plain.PoolID != f.pools[store.SizeMedium].ID {
		t.Fatalf("a job nothing is known about = %+v; it should start in the default class, medium, and be claimed there", plain)
	}
	if asked.SizeClass != store.SizeLarge || asked.SizeBasis != store.SizeBasisExplicit ||
		asked.RoutedClass != store.SizeLarge || asked.PoolID != f.pools[store.SizeLarge].ID {
		t.Fatalf("a job that asks for a class by name = %+v; it should be taken at its word", asked)
	}
	if entries := sizedEntries(h.timeline(plain.ID)); len(entries) != 1 || strings.Contains(entries[0], "only watching") ||
		!strings.Contains(entries[0], "default class") {
		t.Fatalf("the timeline says %v", entries)
	}

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if h.runnersIn(f.pools[store.SizeMedium]) != 1 || h.runnersIn(f.pools[store.SizeLarge]) != 1 || h.runnersIn(f.pools[store.SizeSmall]) != 0 {
		t.Fatalf("runners: medium %d, large %d, small %d; want one for each job, in its own class, and none for small",
			h.runnersIn(f.pools[store.SizeMedium]), h.runnersIn(f.pools[store.SizeLarge]), h.runnersIn(f.pools[store.SizeSmall]))
	}
}

// Taking routing back to watching has to take effect for the jobs already
// waiting, which were routed while it was on: the class stays on them, and they
// are claimed as they would have been without it.
func TestSwitchingRoutingBackToWatchedStopsRoutingTheJobsAlreadyQueued(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	h.deliverJob(jobEvent{Action: "queued", JobID: 7205, Name: "build", Workflow: "CI", Labels: baseLabels})
	if got := h.jobByGitHubID(7205); got.RoutedClass != store.SizeMedium {
		t.Fatalf("with routing on the job is routed to %q", got.RoutedClass)
	}

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = scheduler.SizeShadow })
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pools, _ := h.st.ListPools(h.ctx)
	job := h.jobByGitHubID(7205)
	want := scheduler.BestPool(pools, &store.Job{Labels: job.Labels, InstallationID: job.InstallationID})
	if job.SizeClass != store.SizeMedium {
		t.Fatalf("the class was taken off the job when routing was watched: %+v", job)
	}
	if want == nil || want.ID == f.pools[store.SizeMedium].ID {
		t.Fatalf("the pool the job would have had without routing is %v, which is the one routing sends it to, so this test cannot tell the two apart", want)
	}
	if h.runnersIn(want) != 1 || h.runnersIn(f.pools[store.SizeMedium]) != 0 {
		t.Fatalf("with routing watched the job should be fed by %s, the pool it would have had without routing; runners: %s %d, medium %d",
			want.Name, want.Name, h.runnersIn(want), h.runnersIn(f.pools[store.SizeMedium]))
	}
}

// Deliveries are at least once and out of order, so a job that has been classed
// is not classed again: a later delivery for it cannot send it somewhere else
// than its first did.
func TestARedeliveredJobKeepsTheClassItWasFirstGiven(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeOn)

	h.deliverJob(jobEvent{Action: "queued", JobID: 7203, Name: "build", Workflow: "CI", Labels: baseLabels})
	first := h.jobByGitHubID(7203)
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Class: store.SizeLarge}); err != nil {
		t.Fatal(err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 7203, Name: "build", Workflow: "CI", Labels: baseLabels})
	again := h.jobByGitHubID(7203)
	if again.SizeClass != first.SizeClass || again.SizeBasis != first.SizeBasis || again.RoutedClass != first.RoutedClass {
		t.Fatalf("a redelivery reclassed the job from %q/%q to %q/%q", first.SizeClass, first.SizeBasis, again.SizeClass, again.SizeBasis)
	}
	// The claim follows the stored route: a redelivery that classed the job
	// again, and claimed it for what it made of the pin, would list the job under
	// one pool while everything else on the row says another.
	if again.PoolID != first.PoolID {
		t.Fatalf("a redelivery moved the job's claim from %q to %q", first.PoolID, again.PoolID)
	}
	if got := sizedEntries(h.timeline(first.ID)); len(got) != 1 {
		t.Fatalf("a redelivery added timeline entries: %v", got)
	}
}

// The deliveries that follow a queued one -- the job started, the job finished --
// are claimed afresh like any other, and a claim made without the route would
// list the job under whichever pool of its labels sorts first. The pool the job
// was routed to and ran in is the one the Jobs page, the per-pool counters and
// the usage history have to name for the whole of its life.
func TestALaterDeliveryOfARoutedJobStaysInThePoolItWasRoutedTo(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	medium, large := f.pools[store.SizeMedium], f.pools[store.SizeLarge]
	// Large sorts before medium, which is what a claim that has forgotten the
	// route would pick.
	if !(large.Name < medium.Name) {
		t.Fatalf("the pools sort %q before %q; this test needs the one it must not pick to come first", medium.Name, large.Name)
	}

	h.deliverJob(jobEvent{Action: "queued", JobID: 7210, Name: "build", Workflow: "CI", Labels: baseLabels})
	queued := h.jobByGitHubID(7210)
	if queued.RoutedClass != store.SizeMedium || queued.PoolID != medium.ID {
		t.Fatalf("the job was queued as %+v; want it routed to and claimed by the medium pool", queued)
	}
	for _, action := range []string{"in_progress", "completed"} {
		e := jobEvent{Action: action, JobID: 7210, Name: "build", Workflow: "CI", Labels: baseLabels, RunnerName: "somebody-elses"}
		if action == "completed" {
			e.Conclusion = "success"
		}
		h.deliverJob(e)
		got := h.jobByGitHubID(7210)
		if got.PoolID != medium.ID || got.RoutedClass != store.SizeMedium {
			t.Fatalf("after the %s delivery the job is claimed by %q and routed to %q; it was routed to the medium pool",
				action, got.PoolID, got.RoutedClass)
		}
	}
}

// Every job in every repository an installation covers is delivered here, and
// most of a busy organisation's are for GitHub's own runners. A class on one is a
// badge on a job nobody here is placing, a line on its timeline saying why, and a
// count in a metric that is meant to be about this fleet's decisions.
func TestAJobNoPoolAnswersIsNotClassed(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeOn)
	for id, labels := range map[int64][]string{
		7241: {"ubuntu-latest"},
		7242: {"self-hosted", "gpu"}, // another provider's
	} {
		h.deliverJob(jobEvent{Action: "queued", JobID: id, Name: "build", Workflow: "CI", Labels: labels})
		job := h.jobByGitHubID(id)
		if job.Matched || job.SizeClass != "" || job.SizeBasis != "" || job.RoutedClass != "" {
			t.Errorf("a job asking for %v was matched %t and classed %q/%q, routed to %q", labels, job.Matched, job.SizeClass, job.SizeBasis, job.RoutedClass)
		}
		if got := sizedEntries(h.timeline(job.ID)); len(got) != 0 {
			t.Errorf("a job asking for %v has timeline entries about how it was classed: %v", labels, got)
		}
	}
	if n := testutil.CollectAndCount(h.c.metrics.jobsSized); n != 0 {
		t.Fatalf("%d series of jobs classed, from jobs no pool answers", n)
	}

	// One that a pool answers is classed as it always was.
	h.deliverJob(jobEvent{Action: "queued", JobID: 7243, Name: "build", Workflow: "CI", Labels: baseLabels})
	if job := h.jobByGitHubID(7243); !job.Matched || job.SizeBasis == "" {
		t.Fatalf("a job a pool answers was matched %t and classed %q", job.Matched, job.SizeBasis)
	}
}

// A job first seen already running was answered by whatever is running it: it
// has no class to be sent to, and none is made up after the fact.
func TestAJobFirstSeenAlreadyRunningIsNotClassed(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeOn)

	h.deliverJob(jobEvent{Action: "in_progress", JobID: 7204, Name: "build", Workflow: "CI", Labels: baseLabels, RunnerName: "somebody-elses"})
	job := h.jobByGitHubID(7204)
	if job.SizeBasis != "" || job.RoutedClass != "" {
		t.Fatalf("a job first seen running was classed: %+v", job)
	}
}

// ---------------------------------------------------------------------------
// Where a job cannot go
// ---------------------------------------------------------------------------

// A job whose class has no hosts is not made to wait for ones that will never
// come: it is sent to the nearest class that has a pool, at once, and it says so.
func TestAJobWhoseClassHasNoPoolIsSentToTheNextSizeUpAtOnce(t *testing.T) {
	h := newHarness(t)
	f := h.classFleetOf(scheduler.SizeOn, store.SizeMedium, store.SizeLarge)
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Class: store.SizeSmall}); err != nil {
		t.Fatal(err)
	}

	h.deliverJob(jobEvent{Action: "queued", JobID: 7301, Name: "lint", Workflow: "CI", Labels: baseLabels})
	job := h.jobByGitHubID(7301)
	if job.SizeClass != store.SizeSmall || job.SizeBasis != store.SizeBasisPin || job.RoutedClass != store.SizeSmall {
		t.Fatalf("a pinned job = %+v", job)
	}

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	job = h.jobByGitHubID(7301)
	if job.RoutedClass != store.SizeMedium || job.SizeClass != store.SizeSmall ||
		!strings.Contains(job.RoutedNote, "there is no small pool") || !strings.Contains(job.RoutedNote, "the next size up") {
		t.Fatalf("the job is routed to %q with the note %q; it should have gone to medium and said why", job.RoutedClass, job.RoutedNote)
	}
	if job.PoolID != f.pools[store.SizeMedium].ID {
		t.Fatalf("the job is listed under %q, want the medium pool it was sent to", job.PoolID)
	}
	entries := sizedEntries(h.timeline(job.ID))
	if len(entries) != 2 || !strings.Contains(entries[0], "pinned") || !strings.Contains(entries[1], "sent to the medium pool") {
		t.Fatalf("the timeline says %v", entries)
	}
}

// A pin for something else in the repository changes nothing about a job that
// already has its own, and must not send a job that was moved on because its
// class has no pool back to wait where it came from. The job is not counted as
// changed, its timeline gains nothing, and it is not moved a second time by the
// next pass.
func TestAPinThatChangesNothingAboutAJobDoesNotUndoItsFallback(t *testing.T) {
	h := newHarness(t)
	f := h.classFleetOf(scheduler.SizeOn, store.SizeMedium, store.SizeLarge)
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Workflow: "CI", JobName: "lint", Class: store.SizeSmall}); err != nil {
		t.Fatal(err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 7302, Name: "lint", Workflow: "CI", Labels: baseLabels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	job := h.jobByGitHubID(7302)
	if job.RoutedClass != store.SizeMedium || job.PoolID != f.pools[store.SizeMedium].ID {
		t.Fatalf("the job was not sent on to medium: %+v", job)
	}
	before := sizedEntries(h.timeline(job.ID))

	// A pin for the whole repository: this job's own is the one that decides it.
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Class: store.SizeLarge}); err != nil {
		t.Fatal(err)
	}
	changed, err := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("a pin that changes nothing about the job reported %d jobs changed", changed)
	}
	again := h.jobByGitHubID(7302)
	if again.RoutedClass != store.SizeMedium || again.RoutedNote != job.RoutedNote || again.PoolID != job.PoolID {
		t.Fatalf("the job was sent back: routed to %q (%q) and claimed by %q", again.RoutedClass, again.RoutedNote, again.PoolID)
	}
	if after := sizedEntries(h.timeline(job.ID)); len(after) != len(before) {
		t.Fatalf("the timeline gained entries: %v then %v", before, after)
	}
}

// Capacity in the right class is worth a wait, and a short one: a job that has
// waited the configured time for room in its own class may take another's, the
// larger first, and only the jobs there is no runner for are moved.
func TestAJobThatCannotGetRoomInItsClassIsMovedOnlyAfterTheWaitAndNeverWhenItAskedForTheClass(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	medium := f.pools[store.SizeMedium]
	medium.AutoCap = 1
	if err := h.st.UpdatePool(h.ctx, medium); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	if got := h.poolNamed("zoomies-medium"); got.MaxRunners != 1 {
		t.Fatalf("the cap left the pool at %d runners", got.MaxRunners)
	}
	h.seedRunner(t, h.poolNamed("zoomies-medium"), f.hosts[store.SizeMedium], store.RunnerBusy)

	h.deliverJob(jobEvent{Action: "queued", JobID: 7401, Name: "build", Workflow: "CI", Labels: baseLabels})
	h.deliverJob(jobEvent{Action: "queued", JobID: 7402, Name: "asks", Workflow: "CI", Labels: []string{"self-hosted", store.SizeMedium.Label()}})

	// Not yet: the job has waited a minute and the wait is two.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.jobByGitHubID(7401); got.RoutedClass != store.SizeMedium {
		t.Fatalf("a job that had waited a minute was moved to %q", got.RoutedClass)
	}

	h.advance(3 * time.Minute)
	h.beatAll()
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	moved, asked := h.jobByGitHubID(7401), h.jobByGitHubID(7402)
	if moved.RoutedClass != store.SizeLarge || moved.SizeClass != store.SizeMedium ||
		!strings.Contains(moved.RoutedNote, "no medium host had room for it for") || !strings.Contains(moved.RoutedNote, "was allowed on large") {
		t.Fatalf("after the wait the job is routed to %q with the note %q; want large, and why", moved.RoutedClass, moved.RoutedNote)
	}
	if moved.PoolID != f.pools[store.SizeLarge].ID {
		t.Fatalf("the moved job is listed under %q, want the large pool", moved.PoolID)
	}
	if asked.RoutedClass != store.SizeMedium || asked.RoutedNote != "" {
		t.Fatalf("a job that asked for medium by name was moved to %q (%s); asking for a class is a guarantee", asked.RoutedClass, asked.RoutedNote)
	}

	// The next pass feeds the pool it was sent to.
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if h.runnersIn(f.pools[store.SizeLarge]) != 1 {
		t.Fatalf("the large pool has %d runners; the job that was sent to it should have one", h.runnersIn(f.pools[store.SizeLarge]))
	}
	if entries := sizedEntries(h.timeline(moved.ID)); len(entries) != 2 || !strings.Contains(entries[1], "sent to the large pool") {
		t.Fatalf("the timeline says %v", entries)
	}
}

// Moving a job is a scheduling decision that changes what runs where, and a
// controller that may not act does not make one.
func TestAFencedControllerDoesNotSendAJobToAnotherClass(t *testing.T) {
	h := newHarness(t)
	h.classFleetOf(scheduler.SizeOn, store.SizeMedium, store.SizeLarge)
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Class: store.SizeSmall}); err != nil {
		t.Fatal(err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 7403, Name: "lint", Workflow: "CI", Labels: baseLabels})

	h.fence("restored from a backup")
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := h.jobByGitHubID(7403); got.RoutedClass != store.SizeSmall || got.RoutedNote != "" {
		t.Fatalf("a fenced controller moved a job to %q", got.RoutedClass)
	}
}

// ---------------------------------------------------------------------------
// What a job's runs say about it
// ---------------------------------------------------------------------------

// The whole loop, through the doors a real run uses: the job is classed with
// nothing known, runs and is measured, and the next time it is queued the class
// that measurement says is the one it is sent to.
func TestAFinishedRunClassesTheNextOneAndRecordsWhereItRan(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	mediumHost := f.hosts[store.SizeMedium]

	h.deliverJob(jobEvent{Action: "queued", JobID: 7501, Name: "build", Workflow: "CI", Labels: baseLabels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var r *store.Runner
	for _, cand := range h.runners() {
		if cand.PoolID == f.pools[store.SizeMedium].ID {
			r = cand
		}
	}
	if r == nil {
		t.Fatal("no runner was made in the medium pool for a job that started in it")
	}
	mustReport(t, h, mediumHost.ID, r.ID, store.RunnerRegistering)
	mustReportRunning(t, h, mediumHost.ID, r.ID)
	h.deliverJob(jobEvent{Action: "in_progress", JobID: 7501, Name: "build", Workflow: "CI", Labels: baseLabels, RunnerName: r.Name})

	// It used more memory than a medium runner has, which is what a kill looks
	// like from the sampler's side before the kernel gets to it.
	at := h.c.Now()
	if err := h.c.ReportRunners(h.ctx, mediumHost.ID, []agent.RunnerReport{{
		RunnerID: r.ID, Stats: backend.Stats{SampledAt: &at, CPUPercent: 150, MemoryBytes: 5000 << 20},
	}}); err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}
	h.deliverJob(jobEvent{Action: "completed", JobID: 7501, Name: "build", Workflow: "CI", Labels: baseLabels, RunnerName: r.Name, Conclusion: "success"})

	ran := h.jobByGitHubID(7501)
	if ran.RanClass != store.SizeMedium {
		t.Fatalf("the job ran on a %q host; the row says %q", store.SizeMedium, ran.RanClass)
	}
	// The poller sees the same job start, and finds where it ran already
	// recorded: the count of jobs by where they ran is raised once.
	h.c.stampJobRan(h.ctx, &store.Job{ID: ran.ID, RunnerID: ran.RunnerID, SizeClass: ran.SizeClass, InstallationID: ran.InstallationID}, nil)
	if got := testutil.ToFloat64(h.c.metrics.jobsRan.WithLabelValues(string(store.SizeMedium), string(store.SizeMedium), scheduler.SizeOn)); got != 1 {
		t.Fatalf("a job that ran once was counted %v times by where it ran", got)
	}
	kept, err := h.st.GetJobClass(h.ctx, "acme/widgets", "CI", "build")
	if err != nil {
		t.Fatalf("no class was kept for the job: %v", err)
	}
	if kept.Class != store.SizeLarge || kept.Basis != store.SizeBasisHistory || kept.FloorMB < 5000 {
		t.Fatalf("the kept class is %+v; 5 GB of memory wants a large runner and the floor says so", kept)
	}
	moves := h.audits("size_class.move")
	if len(moves) != 1 || !strings.Contains(causeOf(t, moves[0]), "build was in no class, and is now large") {
		t.Fatalf("the move is on record as %+v", moves)
	}

	h.deliverJob(jobEvent{Action: "queued", JobID: 7502, Name: "build", Workflow: "CI", Labels: baseLabels})
	next := h.jobByGitHubID(7502)
	if next.SizeClass != store.SizeLarge || next.SizeBasis != store.SizeBasisHistory || next.SizeFloorMB < 5000 ||
		next.RoutedClass != store.SizeLarge || next.PoolID != f.pools[store.SizeLarge].ID {
		t.Fatalf("the next run = %+v; it should be sent to large, for its measured memory", next)
	}
	if !strings.Contains(next.SizeReason, "its memory needs about") {
		t.Fatalf("the reason is %q; it should say what was measured", next.SizeReason)
	}
}

// The webhook and the poller both see a queued job, and whichever is second finds
// it already classed. The count of jobs classed is for the delivery that did it,
// and a late delivery for a job that has started, which classes nothing, counts
// nothing: the second used to add a job to the total, and the third a series with
// no class at all.
func TestAJobIsCountedAsClassedOnceHoweverManyDeliveriesSeeIt(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeOn)
	h.deliverJob(jobEvent{Action: "queued", JobID: 7230, Name: "build", Workflow: "CI", Labels: baseLabels})
	job := h.jobByGitHubID(7230)
	counted := func(basis, class string) float64 {
		return testutil.ToFloat64(h.c.metrics.jobsSized.WithLabelValues(basis, class, scheduler.SizeOn))
	}
	if got := counted(store.SizeBasisDefault, string(store.SizeMedium)); got != 1 {
		t.Fatalf("a job classed once was counted %v times", got)
	}

	// The second observer read the job before it was stamped, and stamps it now.
	late := &store.JobClassing{Class: store.SizeMedium, Reason: "it has no measured runs yet", Basis: store.SizeBasisDefault, Route: true}
	h.c.stampSize(h.ctx, job, late)
	if got := counted(store.SizeBasisDefault, string(store.SizeMedium)); got != 1 {
		t.Fatalf("a second observer of the same job brought the count to %v", got)
	}

	// A job that is running was not classed by a queued delivery that arrived
	// after it, and is not counted as if it had been.
	h.deliverJob(jobEvent{Action: "in_progress", JobID: 7231, Name: "build", Workflow: "CI", Labels: baseLabels, RunnerName: "somebody-elses"})
	started := h.jobByGitHubID(7231)
	h.c.stampSize(h.ctx, started, late)
	if got := testutil.CollectAndCount(h.c.metrics.jobsSized); got != 1 {
		t.Fatalf("%d series of jobs classed; a job that was never classed added one", got)
	}
}

// A kill is the one piece of evidence the class does not wait for: its peak is
// the limit it hit, and a percentile of a few runs would ignore it.
func TestAKillForMemoryMovesAJobUpWithoutWaitingForMoreRuns(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	host, pool := f.hosts[store.SizeMedium], f.pools[store.SizeMedium]
	r := h.seedRunner(t, pool, host, store.RunnerBusy)
	started := h.c.Now().Add(-time.Hour)
	job, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 7601, Repo: "acme/widgets", Workflow: "CI", JobName: "build", Labels: pool.Labels,
		State: store.JobInProgress, PoolID: pool.ID, Matched: true, RunnerID: r.ID, RunnerName: r.Name,
		QueuedAt: started, StartedAt: &started, HostID: host.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	at := h.c.Now()
	if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, Stats: backend.Stats{SampledAt: &at, CPUPercent: 120, MemoryBytes: 2900 << 20},
	}}); err != nil {
		t.Fatal(err)
	}
	done := h.c.Now()
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{GitHubJobID: 7601, State: store.JobCompleted, Conclusion: "failure",
		Repo: job.Repo, StartedAt: &started, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, State: store.RunnerRemoved, Fault: store.FaultOutOfMemory,
		Message: "runner exited after its job, but the kernel killed a process in it for its memory limit",
	}}); err != nil {
		t.Fatal(err)
	}

	kept, err := h.st.GetJobClass(h.ctx, "acme/widgets", "CI", "build")
	if err != nil {
		t.Fatalf("a run killed for memory kept no class: %v", err)
	}
	if kept.Class != store.SizeLarge || !strings.Contains(kept.Reason, "the kernel killed a run for memory") {
		t.Fatalf("the kept class is %q, %q; a kill should move the job up, saying so", kept.Class, kept.Reason)
	}
}

// With size routing off nothing is kept either: a fleet that never asked is not
// accumulating a table nobody reads.
func TestNothingIsKeptAboutAJobsClassWhileSizeRoutingIsOff(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	job, r := startJobOnRunner(t, h, host.ID, 7701, pool.Labels)
	at := h.c.Now()
	if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, Stats: backend.Stats{SampledAt: &at, CPUPercent: 150, MemoryBytes: 5000 << 20},
	}}); err != nil {
		t.Fatal(err)
	}
	h.deliverJob(jobEvent{Action: "completed", JobID: 7701, Name: "test", Workflow: "CI", Labels: pool.Labels, RunnerName: r.Name, Conclusion: "success"})
	if _, err := h.st.GetJobClass(h.ctx, job.Repo, "CI", "test"); err == nil {
		t.Fatal("a class was kept with size routing off")
	}
	if got := h.jobByGitHubID(7701); got.RanClass != "" {
		t.Fatalf("the class of the host that ran the job was recorded with both switches off: %q", got.RanClass)
	}
}

// How often a runner was held back by its CPU quota is what tells a job that
// wants more CPU from one that is merely busy, and the controller keeps it only
// where something reads it.
func TestCPUThrottlingIsRecordedAgainstTheJobWhenSizeRoutingIsWatchingOrOn(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want int64
	}{{scheduler.SizeOff, 0}, {scheduler.SizeShadow, 1000}, {scheduler.SizeOn, 1000}} {
		t.Run(tc.mode, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = tc.mode })
			_, r := startJobOnRunner(t, h, host.ID, 7801, pool.Labels)
			at := h.c.Now()
			if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
				RunnerID: r.ID, Stats: backend.Stats{SampledAt: &at, CPUPercent: 190, MemoryBytes: 1000 << 20,
					CPUThrottling: &backend.CPUThrottling{Periods: 1000, ThrottledPeriods: 400}},
			}}); err != nil {
				t.Fatal(err)
			}
			got := h.jobByGitHubID(7801)
			if got.CPUPeriods != tc.want || (tc.want > 0 && got.CPUThrottledPeriods != 400) {
				t.Fatalf("the job has %d/%d throttled periods, want %d periods", got.CPUThrottledPeriods, got.CPUPeriods, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// An operator's word
// ---------------------------------------------------------------------------

// A pin made to fix a job that is stuck in the wrong class has to fix that one,
// and not only the jobs that arrive afterwards.
func TestAPinReachesJobsAlreadyWaitingAndTheyAreClaimedForItsClass(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	h.deliverJob(jobEvent{Action: "queued", JobID: 7901, Name: "build", Workflow: "CI", Labels: baseLabels})
	h.deliverJob(jobEvent{Action: "queued", JobID: 7902, Repo: "acme/other", Name: "build", Workflow: "CI", Labels: baseLabels})

	if n, err := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "", ""); err != nil || n != 0 {
		t.Fatalf("with no pin ReclassifyQueuedJobs = %d, %v; nothing should change", n, err)
	}
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "Acme/Widgets", Class: store.SizeLarge}); err != nil {
		t.Fatal(err)
	}
	n, err := h.c.ReclassifyQueuedJobs(h.ctx, "Acme/Widgets", "", "")
	if err != nil || n != 1 {
		t.Fatalf("ReclassifyQueuedJobs = %d, %v; want the one job in the repository", n, err)
	}
	got := h.jobByGitHubID(7901)
	if got.SizeClass != store.SizeLarge || got.SizeBasis != store.SizeBasisPin || got.RoutedClass != store.SizeLarge ||
		got.PoolID != f.pools[store.SizeLarge].ID {
		t.Fatalf("the pinned job = %+v; it should be large, on the pin's authority, and claimed by the large pool", got)
	}
	if entries := sizedEntries(h.timeline(got.ID)); len(entries) != 2 ||
		!strings.Contains(entries[1], "classed large because an operator pinned every job in acme/widgets to large") {
		t.Fatalf("the timeline says %v", entries)
	}
	if other := h.jobByGitHubID(7902); other.SizeClass != store.SizeMedium {
		t.Fatalf("a job in another repository was reclassed: %+v", other)
	}
	if n, _ := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "", ""); n != 0 {
		t.Fatalf("restating the pin changed %d jobs", n)
	}

	// With size routing off there is nothing to reclass.
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = scheduler.SizeOff })
	if n, err := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "", ""); err != nil || n != 0 {
		t.Fatalf("with routing off ReclassifyQueuedJobs = %d, %v", n, err)
	}
}

// A job GitHub is holding for a deployment review was classed when it arrived,
// and a pin made while it waits for an approver is what it is queued by when the
// approval comes, not the class it had before.
func TestAPinReachesAJobHeldForAReviewAndItIsQueuedByIt(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	h.deliverJob(jobEvent{Action: "waiting", JobID: 7911, Name: "deploy", Workflow: "Release", Labels: baseLabels})
	held := h.jobByGitHubID(7911)
	if held.State != store.JobWaiting || held.SizeClass != store.SizeMedium {
		t.Fatalf("the held job is %+v; want it waiting and classed medium", held)
	}

	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Workflow: "Release", JobName: "deploy", Class: store.SizeLarge}); err != nil {
		t.Fatal(err)
	}
	n, err := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "Release", "deploy")
	if err != nil || n != 1 {
		t.Fatalf("ReclassifyQueuedJobs = %d, %v; want the held job reached", n, err)
	}
	got := h.jobByGitHubID(7911)
	if got.State != store.JobWaiting || got.SizeClass != store.SizeLarge || got.RoutedClass != store.SizeLarge {
		t.Fatalf("the held job is %+v after the pin; want it still waiting, and large", got)
	}

	// The approval arrives as an ordinary queued delivery, and it is claimed by
	// the pool of the class the pin gave it.
	h.deliverJob(jobEvent{Action: "queued", JobID: 7911, Name: "deploy", Workflow: "Release", Labels: baseLabels})
	approved := h.jobByGitHubID(7911)
	if approved.State != store.JobQueued || approved.SizeClass != store.SizeLarge || approved.PoolID != f.pools[store.SizeLarge].ID {
		t.Fatalf("the approved job is %+v; want it queued, large and claimed by the large pool", approved)
	}
}

// A pin for a repository reaches the jobs the fleet's pools answer and no others:
// the repository's jobs for GitHub's own runners are not this fleet's work, and a
// class stamped on one would put a badge, a timeline line and a count in the
// metric on a decision nobody was making about it.
func TestAPinDoesNotClassAJobNoPoolOfTheFleetAnswers(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeOn)
	h.deliverJob(jobEvent{Action: "queued", JobID: 7921, Name: "build", Workflow: "CI", Labels: []string{"ubuntu-latest"}})
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Class: store.SizeLarge}); err != nil {
		t.Fatal(err)
	}
	n, err := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "", "")
	if err != nil || n != 0 {
		t.Fatalf("ReclassifyQueuedJobs = %d, %v; a job for GitHub's runners is not the fleet's to class", n, err)
	}
	if got := h.jobByGitHubID(7921); got.SizeClass != "" || got.SizeBasis != "" || got.RoutedClass != "" {
		t.Fatalf("a job no pool answers was classed: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// The switch itself
// ---------------------------------------------------------------------------

// The mode is read as a job arrives, so a job that was already waiting when
// routing was turned on would otherwise have no route for as long as it waited:
// it would sit in the pool its labels sort to first, and routing would reach only
// the jobs that came after the switch.
func TestTurningSizeRoutingOnSendsTheJobsAlreadyWaitingToTheirClass(t *testing.T) {
	for _, from := range []string{scheduler.SizeOff, scheduler.SizeShadow} {
		t.Run(from, func(t *testing.T) {
			h := newHarness(t)
			f := h.classFleet(from)
			h.deliverJob(jobEvent{Action: "queued", JobID: 8001, Name: "build", Workflow: "CI", Labels: baseLabels})
			h.deliverJob(jobEvent{Action: "waiting", JobID: 8002, Name: "deploy", Workflow: "Release", Labels: baseLabels})
			h.deliverJob(jobEvent{Action: "queued", JobID: 8003, Name: "hosted", Workflow: "CI", Labels: []string{"ubuntu-latest"}})
			if was := h.jobByGitHubID(8001); was.RoutedClass != "" || was.PoolID == f.pools[store.SizeMedium].ID {
				t.Fatalf("before the switch the job is %+v; it should have no route and not be in the medium pool already", was)
			}

			h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = scheduler.SizeOn })
			n, err := h.c.ReclassifyWaitingJobs(h.ctx)
			if err != nil || n != 2 {
				t.Fatalf("ReclassifyWaitingJobs = %d, %v; want the queued job and the held one", n, err)
			}
			queued, held := h.jobByGitHubID(8001), h.jobByGitHubID(8002)
			if queued.SizeClass != store.SizeMedium || queued.RoutedClass != store.SizeMedium || queued.PoolID != f.pools[store.SizeMedium].ID {
				t.Fatalf("the queued job is %+v; want it medium, routed to medium and claimed by the medium pool", queued)
			}
			if held.State != store.JobWaiting || held.SizeClass != store.SizeMedium || held.RoutedClass != store.SizeMedium {
				t.Fatalf("the held job is %+v; want it still waiting, medium and routed to medium", held)
			}
			entries := sizedEntries(h.timeline(queued.ID))
			if last := entries[len(entries)-1]; !strings.Contains(last, "classed medium because") || strings.Contains(last, "only watching") {
				t.Fatalf("the timeline says %v; its last line should say the job was classed and sent on", entries)
			}
			if hosted := h.jobByGitHubID(8003); hosted.SizeClass != "" || hosted.RoutedClass != "" {
				t.Fatalf("a job for GitHub's runners was classed by the switch: %+v", hosted)
			}
			if n, err := h.c.ReclassifyWaitingJobs(h.ctx); err != nil || n != 0 {
				t.Fatalf("a second ReclassifyWaitingJobs = %d, %v; the queue is already in the mode", n, err)
			}
		})
	}
}

// The scheduler ignores a route while routing is not on, but the row is what the
// Jobs page and the API read, and a job that went on saying it had been sent to a
// class would be saying something that is no longer being done. It keeps its
// class, which is what it was taken to need, and is claimed as it was before.
func TestTurningSizeRoutingDownTakesTheRouteOffTheJobsAlreadyWaiting(t *testing.T) {
	for _, tc := range []struct{ to, line string }{
		{scheduler.SizeShadow, "only watching"},
		{scheduler.SizeOff, "size routing was turned off"},
	} {
		t.Run(tc.to, func(t *testing.T) {
			h := newHarness(t)
			f := h.classFleet(scheduler.SizeOn)
			h.deliverJob(jobEvent{Action: "queued", JobID: 8101, Name: "build", Workflow: "CI", Labels: baseLabels})
			if was := h.jobByGitHubID(8101); was.RoutedClass != store.SizeMedium || was.PoolID != f.pools[store.SizeMedium].ID {
				t.Fatalf("before the switch the job is %+v; want it routed to medium", was)
			}

			h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = tc.to })
			if n, err := h.c.ReclassifyWaitingJobs(h.ctx); err != nil || n != 1 {
				t.Fatalf("ReclassifyWaitingJobs = %d, %v; want the one job", n, err)
			}
			got := h.jobByGitHubID(8101)
			if got.RoutedClass != "" || got.RoutedNote != "" {
				t.Fatalf("the job still says it was sent to %q (%q)", got.RoutedClass, got.RoutedNote)
			}
			if got.SizeClass != store.SizeMedium || got.SizeBasis == "" {
				t.Fatalf("the job lost its class: %+v", got)
			}
			pools, _ := h.st.ListPools(h.ctx)
			if want := scheduler.BestPool(pools, &store.Job{Labels: got.Labels, InstallationID: got.InstallationID}); want == nil || got.PoolID != want.ID {
				t.Fatalf("the job is claimed by %q; it should be the pool it would have had without routing (%v)", got.PoolID, want)
			}
			entries := sizedEntries(h.timeline(got.ID))
			if last := entries[len(entries)-1]; !strings.Contains(last, tc.line) {
				t.Fatalf("the timeline says %v; its last line should say %q", entries, tc.line)
			}
			if n, err := h.c.ReclassifyWaitingJobs(h.ctx); err != nil || n != 0 {
				t.Fatalf("a second ReclassifyWaitingJobs = %d, %v; the queue is already in the mode", n, err)
			}
		})
	}
}

// The wake is for the switch and only for it: every other setting passes through
// UpdateConfig too, and a value this build does not know reads as off, which is
// what it already was.
func TestOnlyAChangeOfSizeRoutingWakesTheLoopThatReclassesTheQueue(t *testing.T) {
	h := newHarness(t)
	woken := func() bool {
		select {
		case <-h.c.sizeModeChanged:
			return true
		default:
			return false
		}
	}
	set := func(mode string) {
		h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = mode })
	}
	woken() // Whatever building the harness did.

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeFallbackWait = 3 * time.Minute })
	set(scheduler.SizeOff)
	set("no such mode")
	if woken() {
		t.Fatal("the loop was woken though the mode did not change")
	}
	set(scheduler.SizeShadow)
	if !woken() {
		t.Fatal("the loop was not woken when routing went from off to watched")
	}
	set(scheduler.SizeOn)
	set(scheduler.SizeOff)
	if !woken() {
		t.Fatal("the loop was not woken when routing was turned on and off again")
	}
	if woken() {
		t.Fatal("two changes left two wakes; a wake is a flag, and the loop reads the mode as it then is")
	}
}

// Changing the setting is a request, and it must not wait for every job in a long
// queue to be read: the loop does it afterwards, and the job has its route by the
// time the loop has gone round.
func TestTheSizeRoutingLoopPutsTheQueueThroughTheNewModeWhenTheSettingChanges(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOff)
	h.deliverJob(jobEvent{Action: "queued", JobID: 8301, Name: "build", Workflow: "CI", Labels: baseLabels})

	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.sizeModeLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = scheduler.SizeOn })
	// The loop writes the job's route and then, in a statement of its own, the
	// pool that claims it now, so the route appearing is not the end of its pass.
	// Waiting for the route alone and then reading the claim raced the second
	// write, and failed on a runner slow enough to show it.
	medium := f.pools[store.SizeMedium].ID
	eventually(t, 5*time.Second, "the job already waiting to be routed and claimed for its class", func() bool {
		got := h.jobByGitHubID(8301)
		return got.RoutedClass == store.SizeMedium && got.PoolID == medium
	})
}

// The problems list asks for the count of label advice after every pass, and it
// reads every class kept for every job to say it, so the count is kept for a
// minute -- and not past a change that alters it, which is a pin.
func TestTheCountOfLabelAdviceIsKeptForAMinuteAndForgottenWhenAPinChangesIt(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeShadow)
	for i := 1; i <= 12; i++ {
		last := h.measuredRun(i, "build", []string{"self-hosted", "zoomies"}, 6000)
		if i == 12 {
			h.c.refreshJobClass(h.ctx, last)
		}
	}
	counts := func() int {
		t.Helper()
		got, total, err := h.c.labelAdviceCounts(h.ctx)
		if err != nil || got == nil {
			t.Fatalf("labelAdviceCounts = %v, %d, %v", got, total, err)
		}
		return total
	}
	if n := counts(); n != 1 {
		t.Fatalf("%d jobs with advice; want the one that needs more than the default and asks for none", n)
	}

	// A pin made behind the controller's back is not seen inside the minute...
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Workflow: "CI", JobName: "build", Class: store.SizeMedium}); err != nil {
		t.Fatal(err)
	}
	if n := counts(); n != 1 {
		t.Fatalf("the count was worked out again inside the minute: %d", n)
	}
	// ...and is once the minute is up,
	h.advance(labelAdviceMemoFor + time.Second)
	if n := counts(); n != 0 {
		t.Fatalf("%d jobs with advice a minute after the job was pinned", n)
	}
	// ...and at once when a pin goes through the controller.
	if err := h.st.DeleteSizePin(h.ctx, "acme/widgets", "CI", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ReclassifyQueuedJobs(h.ctx, "acme/widgets", "CI", "build"); err != nil {
		t.Fatal(err)
	}
	if n := counts(); n != 1 {
		t.Fatalf("%d jobs with advice just after the pin was removed", n)
	}
}

// ---------------------------------------------------------------------------
// The figures
// ---------------------------------------------------------------------------

// The figures a class is made of are written down in two places, the
// scheduler's defaults and the settings' own, and nothing else keeps them
// together.
func TestTheSettingsDefaultsAreTheSchedulersDefaults(t *testing.T) {
	h := newHarness(t)
	if got, want := h.c.sizeConfig(), scheduler.DefaultSizeConfig(); got != want {
		t.Fatalf("the settings default to %+v, but the scheduler's own defaults are %+v", got, want)
	}
}

func TestAnAutomaticPoolIsSizedByItsClassAndAnOperatorsProfilePoolByTheFleetsDefault(t *testing.T) {
	fleet := config.Default().Runners
	for _, class := range store.SizeClasses() {
		auto := &store.Pool{SizeFromProfile: true, AutoKey: store.AutoKeyFor("amd64", class)}
		cpus, memoryMB := fleet.ClassRunnerSize(string(class))
		if got := sizingPool(auto, fleet).FleetStandard; got.CPUs != cpus || got.MemoryMB != memoryMB {
			t.Errorf("the %s pool's runner is %+v, want %v CPUs and %d MB", class, got, cpus, memoryMB)
		}
	}
	own := &store.Pool{SizeFromProfile: true}
	cpus, memoryMB := fleet.DefaultRunnerSize()
	if got := sizingPool(own, fleet).FleetStandard; got.CPUs != cpus || got.MemoryMB != memoryMB {
		t.Errorf("an operator's profile pool is sized %+v, want the fleet's default %v CPUs and %d MB", got, cpus, memoryMB)
	}
	fixed := &store.Pool{AutoKey: store.AutoKeyFor("amd64", store.SizeLarge)}
	if got := sizingPool(fixed, fleet).FleetStandard; got != (store.RunnerSize{}) {
		t.Errorf("a pool that is not sized from its host was given a standard: %+v", got)
	}

	// What the pool's page says a runner will be is the figure the scheduler
	// sizes it by, for either kind of pool.
	cfg := config.Default()
	r := &PoolRenderer{cfg: cfg}
	for _, class := range store.SizeClasses() {
		cpus, memoryMB := fleet.ClassRunnerSize(string(class))
		view := r.View(&store.Pool{SizeFromProfile: true, AutoKey: store.AutoKeyFor("amd64", class)})
		if view.FleetStandard == nil || view.FleetStandard.CPUs != cpus || view.FleetStandard.MemoryMB != memoryMB {
			t.Errorf("the %s pool's page says a runner is %+v; the scheduler sizes it at %v CPUs and %d MB", class, view.FleetStandard, cpus, memoryMB)
		}
	}
	cpus, memoryMB = fleet.DefaultRunnerSize()
	if view := r.View(own); view.FleetStandard == nil || view.FleetStandard.CPUs != cpus || view.FleetStandard.MemoryMB != memoryMB {
		t.Errorf("an operator's profile pool's page says %+v, want the fleet's default", view.FleetStandard)
	}
}

// A pool that takes its size from its host is noted when a host names no size of
// its own, because the figure it falls back to is the same on a small machine
// and a large one. A pool the controller keeps for a class falls back to that
// class's runner, which is the size of the host's class: the note is not for it,
// and was being raised for every one the moment a host had no profile.
func TestAnAutomaticPoolIsNotToldItsHostsNameNoStandardSize(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.autoPoolsOn("on")
	h.mediumHost("build-1")
	h.autoPass()
	auto := h.poolNamed("zoomies-medium")
	if !auto.SizeFromProfile || !auto.FromHosts() {
		t.Fatalf("the pool is %+v; this test needs one the controller keeps that takes its size from its hosts", auto)
	}
	notes := func() []Problem {
		t.Helper()
		all, err := h.c.Problems(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []Problem
		for _, p := range all {
			if p.Code == "pool.profile_default" {
				out = append(out, p)
			}
		}
		return out
	}
	if got := notes(); len(got) != 0 {
		t.Fatalf("a fleet with only an automatic pool was told its hosts name no standard size: %+v", got)
	}

	// The same host under a pool of the operator's own is still noted, and the
	// note is about that pool and not the other.
	own := h.profilePool(inst, "own", 0)
	got := notes()
	if len(got) != 1 || got[0].TargetID != own.ID {
		t.Fatalf("the notes are %+v; want exactly one, about the operator's pool", got)
	}
}
