package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

func tagOf(tags []scheduler.Tag, key string) (scheduler.Tag, bool) {
	for _, t := range tags {
		if t.Key == key {
			return t, true
		}
	}
	return scheduler.Tag{}, false
}

// ---------------------------------------------------------------------------
// Hosts
// ---------------------------------------------------------------------------

func TestAHostViewSaysWhatItsTagsAreWhichClassItIsInAndWhichPoolItCountsTowards(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeShadow)
	labels := store.StringMap{"rack": "b4"}
	if err := h.st.PatchHost(h.ctx, f.hosts[store.SizeMedium].ID, store.HostChanges{Labels: &labels}); err != nil {
		t.Fatal(err)
	}
	h.autoPass()

	host, _ := h.st.GetHost(h.ctx, f.hosts[store.SizeMedium].ID)
	view := h.c.HostView(host)
	if rack, ok := tagOf(view.Tags, "rack"); !ok || rack.Value != "b4" || rack.Source != scheduler.TagOperator {
		t.Fatalf("the operator's tag is %+v", rack)
	}
	for _, key := range []string{"os", "arch", "size"} {
		if tag, ok := tagOf(view.Tags, key); !ok || tag.Source != scheduler.TagAutomatic {
			t.Fatalf("the %s tag is %+v; it should be listed, and marked as the controller's", key, tag)
		}
	}
	if size, _ := tagOf(view.Tags, "size"); size.Value != "medium" {
		t.Fatalf("the automatic size tag is %q", size.Value)
	}
	sc := view.SizeClass
	if sc == nil || sc.Class != store.SizeMedium || sc.Source != "measured" || sc.Measured != "" ||
		!strings.Contains(sc.Reason, "put it in the medium class") || !strings.Contains(sc.Reason, "11.4 CPUs") {
		t.Fatalf("the class is %+v", sc)
	}
	if view.AutoPool == nil || !view.AutoPool.Counted || view.AutoPool.Pool != "zoomies-medium" || view.AutoPool.PoolID != f.pools[store.SizeMedium].ID {
		t.Fatalf("the automatic pool is %+v", view.AutoPool)
	}
	// The same shape goes down the event stream and out of the API.
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"tags":[`, `"size_class":{"class":"medium"`, `"auto_pool":{"counted":true,"pool":"zoomies-medium"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the view has no %s: %s", want, raw)
		}
	}
}

func TestAnOperatorsSizeTagReplacesTheAutomaticOneAndTheViewSaysSo(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	labels := store.StringMap{"size": "large"}
	if err := h.st.PatchHost(h.ctx, f.hosts[store.SizeMedium].ID, store.HostChanges{Labels: &labels}); err != nil {
		t.Fatal(err)
	}
	host, _ := h.st.GetHost(h.ctx, f.hosts[store.SizeMedium].ID)
	view := h.c.HostView(host)

	if tag, _ := tagOf(view.Tags, "size"); tag.Value != "large" || tag.Source != scheduler.TagOperator || tag.Overrides != "medium" {
		t.Fatalf("the size tag is %+v; it is the operator's, and says what it replaced", tag)
	}
	sc := view.SizeClass
	if sc == nil || sc.Class != store.SizeLarge || sc.Source != "tag" || sc.Measured != store.SizeMedium ||
		!strings.Contains(sc.Reason, "would be medium") {
		t.Fatalf("the class is %+v", sc)
	}

	// A tag that is not a class puts the host in none, and says why in its pool.
	bad := store.StringMap{"size": "huge"}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{Labels: &bad}); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	host, _ = h.st.GetHost(h.ctx, host.ID)
	view = h.c.HostView(host)
	if view.SizeClass != nil {
		t.Fatalf("a host with a size tag that is not a class has the class %+v", view.SizeClass)
	}
	if view.AutoPool == nil || view.AutoPool.Counted || view.AutoPool.ReasonCode != scheduler.SkipBadSize ||
		!strings.Contains(view.AutoPool.Reason, "not exactly small, medium or large") {
		t.Fatalf("the automatic pool is %+v", view.AutoPool)
	}
}

func TestAHostThatCountsTowardsNoPoolSaysWhy(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	if err := h.st.SetHostCordoned(h.ctx, f.hosts[store.SizeSmall].ID, true); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	host, _ := h.st.GetHost(h.ctx, f.hosts[store.SizeSmall].ID)
	view := h.c.HostView(host)
	if view.AutoPool == nil || view.AutoPool.Counted || view.AutoPool.ReasonCode != scheduler.SkipCordoned ||
		!strings.Contains(view.AutoPool.Reason, "It is cordoned") {
		t.Fatalf("a cordoned host's automatic pool is %+v", view.AutoPool)
	}
	// It is still in its class, which is about the machine and not its state.
	if view.SizeClass == nil || view.SizeClass.Class != store.SizeSmall {
		t.Fatalf("a cordoned host's class is %+v", view.SizeClass)
	}
}

// A class that is on its way carries the times, not a sentence with them written
// in, so the page can count down against the viewer's clock.
func TestAHostWaitingToMoveClassCarriesWhenItWillMove(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeClassHold = 10 * time.Minute })
	host := f.hosts[store.SizeMedium]
	grown, _ := h.st.GetHost(h.ctx, host.ID)
	grown.CPUs, grown.MemoryMB = 32, 131072
	if err := h.st.SetHostReported(h.ctx, grown); err != nil {
		t.Fatal(err)
	}
	h.autoPass()

	stored, _ := h.st.GetHost(h.ctx, host.ID)
	view := h.c.HostView(stored)
	sc := view.SizeClass
	if sc == nil || sc.Class != store.SizeMedium || sc.Pending != store.SizeLarge || sc.PendingSince == nil || sc.PendingUntil == nil ||
		sc.PendingUntil.Sub(*sc.PendingSince) != 10*time.Minute || !strings.Contains(sc.Reason, "it moves when they have done so for 10m") {
		t.Fatalf("the class is %+v", sc)
	}
	if sc.Measured != store.SizeLarge {
		t.Fatalf("the measured class is %q, want large: the machine measures larger than the class it is held in", sc.Measured)
	}
}

func TestNothingNewIsOnAHostWhileBothSwitchesAreOff(t *testing.T) {
	h := newHarness(t)
	host := h.mediumHost("build-1")
	view := h.c.HostView(host)
	if view.SizeClass != nil || view.AutoPool != nil {
		t.Fatalf("a host on a fleet with the feature off has a class or a pool: %+v %+v", view.SizeClass, view.AutoPool)
	}
	if _, ok := tagOf(view.Tags, "size"); ok {
		t.Fatalf("the size tag is listed with both switches off: %+v", view.Tags)
	}
	if os, ok := tagOf(view.Tags, "os"); !ok || os.Source != scheduler.TagAutomatic {
		t.Fatalf("the os tag is %+v", os)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "size_class") || strings.Contains(string(raw), "auto_pool") {
		t.Fatalf("the host view carries the feature's fields with it off: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// Pools
// ---------------------------------------------------------------------------

func TestAPoolViewSaysItIsKeptFromHostsAndWhatTheyGiveIt(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeOn)
	mine := h.pool(&store.Installation{ID: f.pools[store.SizeMedium].InstallationID}, "mine")

	render := func(p *store.Pool) PoolView {
		t.Helper()
		r, err := h.c.PoolRenderer(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return r.View(p)
	}
	if got := render(mine); got.Auto != nil {
		t.Fatalf("a pool an operator made says it is kept by the controller: %+v", got.Auto)
	}
	medium, _ := h.st.GetPool(h.ctx, f.pools[store.SizeMedium].ID)
	auto := render(medium).Auto
	if auto == nil || auto.Key != "amd64/medium" || auto.Arch != "amd64" || auto.Class != store.SizeMedium ||
		auto.Slots != 5 || len(auto.Hosts) != 1 || auto.Hosts[0] != "build-1" || auto.Paused || auto.Cap != 0 {
		t.Fatalf("the automatic pool is %+v", auto)
	}
	if !strings.Contains(auto.Summary, "medium x64 hosts") || !strings.Contains(auto.Summary, "1 host (build-1) hold 5 runners") {
		t.Fatalf("the summary is %q", auto.Summary)
	}

	medium.AutoCap, medium.AutoMin, medium.AutoPaused = 3, 1, true
	if err := h.st.UpdatePool(h.ctx, medium); err != nil {
		t.Fatal(err)
	}
	h.autoPass()
	medium, _ = h.st.GetPool(h.ctx, medium.ID)
	auto = render(medium).Auto
	if auto == nil || auto.Cap != 3 || auto.Warm != 1 || !auto.Paused {
		t.Fatalf("what the operator asked is %+v", auto)
	}
	for _, want := range []string{"You capped it at 3 runners", "You asked for 1 runner kept warm", "You paused it"} {
		if !strings.Contains(auto.Summary, want) {
			t.Errorf("the summary %q does not say %q", auto.Summary, want)
		}
	}
}

func TestTheAutoPoolsViewSaysWhatEachSwitchAndEachClassIs(t *testing.T) {
	h := newHarness(t)
	f := h.classFleet(scheduler.SizeShadow)
	h.autoPass()
	v := h.c.AutoPools()
	if v.SizeRouting != scheduler.SizeShadow || v.AutoPools != scheduler.SizeOn || v.Installation != "acme" || v.At == nil ||
		v.DefaultClass != store.SizeMedium || time.Duration(v.Hold) != 0 || time.Duration(v.FallbackWait) != 2*time.Minute ||
		time.Duration(v.HostGrace) != 10*time.Minute {
		t.Fatalf("the view is %+v", v)
	}
	if len(v.Classes) != 3 {
		t.Fatalf("%d classes", len(v.Classes))
	}
	small, medium, large := v.Classes[0], v.Classes[1], v.Classes[2]
	if small.Class != store.SizeSmall || small.Label != "zoomies-small" || small.HostMaxCPUs != 4 || small.HostMaxMemoryMB != 16384 ||
		small.RunnerCPUs != 1 || small.RunnerMemoryMB != 2048 {
		t.Fatalf("small is %+v", small)
	}
	if medium.HostMaxCPUs != 12 || medium.HostMaxMemoryMB != 49152 || medium.RunnerCPUs != 2 || medium.RunnerMemoryMB != 4096 {
		t.Fatalf("medium is %+v", medium)
	}
	if large.Label != "zoomies-large" || large.HostMaxCPUs != 0 || large.HostMaxMemoryMB != 0 || large.RunnerCPUs != 4 || large.RunnerMemoryMB != 8192 {
		t.Fatalf("large is %+v; it has no limit above", large)
	}
	if len(v.Pools) != 3 || v.Pools[1].Name != "zoomies-medium" || v.Pools[1].PoolID != f.pools[store.SizeMedium].ID || v.Pools[1].Slots != 5 {
		t.Fatalf("the pools are %+v", v.Pools)
	}
	// Nothing is null: a client iterates the lists without a check.
	raw, _ := json.Marshal(h.c.AutoPools())
	for _, bad := range []string{`"findings":null`, `"skipped":null`, `"pending":null`, `"pools":null`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("the view has %s: %s", bad, raw)
		}
	}
	off := newHarness(t)
	if v := off.c.AutoPools(); v.SizeRouting != scheduler.SizeOff || v.AutoPools != scheduler.SizeOff || v.At != nil || len(v.Pools) != 0 {
		t.Fatalf("a fleet that has not turned it on has %+v", v)
	}
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

func TestAJobViewCarriesHowItWasClassedAndWhereItRan(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeOn)
	h.deliverJob(jobEvent{Action: "queued", JobID: 8101, Name: "build", Workflow: "CI", Labels: baseLabels})
	job := h.jobByGitHubID(8101)
	view := NewJobView(job, "zoomies-medium")
	if view.SizeClass != store.SizeMedium || view.SizeBasis != store.SizeBasisDefault || view.RoutedClass != store.SizeMedium ||
		view.RanClass != "" || view.ThrottledShare != nil || !strings.Contains(view.SizeReason, "default class") {
		t.Fatalf("the view is %+v", view)
	}
	raw, _ := json.Marshal(view)
	for _, want := range []string{`"size_class":"medium"`, `"size_basis":"default"`, `"routed_class":"medium"`, `"throttled_share":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the job view has no %s: %s", want, raw)
		}
	}
	if sum := NewJobSummaryView(job, "zoomies-medium"); sum.SizeClass != store.SizeMedium {
		t.Fatalf("the summary has class %q", sum.SizeClass)
	}

	job.CPUPeriods, job.CPUThrottledPeriods, job.RanClass = 1000, 400, store.SizeLarge
	view = NewJobView(job, "")
	if view.ThrottledShare == nil || *view.ThrottledShare != 0.4 || view.RanClass != store.SizeLarge {
		t.Fatalf("the throttled share is %v and the class it ran in %q", view.ThrottledShare, view.RanClass)
	}

	// With the feature off none of it is on the job, and none of it is in the JSON.
	plain := newHarness(t)
	_, pool, _ := plain.fleet()
	plain.deliverJob(jobEvent{Action: "queued", JobID: 8102, Labels: pool.Labels})
	raw, _ = json.Marshal(NewJobView(plain.jobByGitHubID(8102), pool.Name))
	for _, bad := range []string{"size_class", "size_basis", "size_reason", "routed_class", "ran_class"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("the job view carries %s with the feature off: %s", bad, raw)
		}
	}
}

func TestTheExplanationSaysHowAJobWasClassedAndWhereItWasSent(t *testing.T) {
	h := newHarness(t)
	h.classFleetOf(scheduler.SizeOn, store.SizeMedium, store.SizeLarge)
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Class: store.SizeSmall}); err != nil {
		t.Fatal(err)
	}
	h.deliverJob(jobEvent{Action: "queued", JobID: 8201, Name: "lint", Workflow: "CI", Labels: baseLabels})
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	job := h.jobByGitHubID(8201)
	ex, err := h.c.ExplainJob(h.ctx, job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"It is in the small class because an operator pinned every job in acme/widgets to small",
		"It was sent to the medium class: there is no small pool"} {
		if !strings.Contains(ex.Detail, want) {
			t.Errorf("the explanation %q does not say %q", ex.Detail, want)
		}
	}

	// A job that names a class nothing answers is not waiting for another provider.
	h.deliverJob(jobEvent{Action: "queued", JobID: 8202, Name: "huge", Workflow: "CI", Labels: []string{"self-hosted", "zoomies-small"}})
	named := h.jobByGitHubID(8202)
	ex, err = h.c.ExplainJob(h.ctx, named.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.Blocked || !strings.Contains(ex.Detail, "asks for zoomies-small by name") || !strings.Contains(ex.Detail, "no enabled pool for the small class") ||
		!strings.Contains(ex.Fix, "enrol a small host") {
		t.Fatalf("the explanation of a job nothing answers is %+v", ex)
	}
	if strings.Contains(ex.Fix, "another runner provider") {
		t.Fatalf("a job for this fleet's own class label was told to look at another provider: %q", ex.Fix)
	}
}

// A pool that carries the label exists, and belongs to another installation: that
// is what the scheduler says, and it is the true thing. The sentence about a class
// that no pool answers would send the operator to enrol a host for a pool they
// already have.
func TestAJobThatNamesAClassWhosePoolIsAnotherInstallationsIsToldThatAndNotThatNoneExists(t *testing.T) {
	h := newHarness(t)
	h.installationOn("acme", store.TargetOrg)
	h.installationOn("globex", store.TargetOrg)
	h.autoPoolsOn("on")
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPoolsInstallation = "globex" })
	h.mediumHost("build-1")
	h.autoPass()
	if p := h.poolNamed("zoomies-medium"); !p.Enabled {
		t.Fatalf("the pool for globex is %+v; this test needs one that exists", p)
	}

	h.deliverJob(jobEvent{Action: "queued", JobID: 8210, Name: "build", Workflow: "CI", Labels: []string{"self-hosted", "zoomies-medium"}})
	h.advance(10 * time.Minute)
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	job := h.jobByGitHubID(8210)
	ex, err := h.c.ExplainJob(h.ctx, job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.Blocked || !strings.Contains(ex.Detail, "belongs to installation") || strings.Contains(ex.Detail, "no enabled pool carries that label") {
		t.Fatalf("the explanation is %+v; want the pool on another installation named, and no claim that none exists", ex)
	}
	if strings.Contains(ex.Fix, "enrol a medium host") {
		t.Fatalf("the fix sends an operator to enrol a host for a pool that exists: %q", ex.Fix)
	}

	prob := h.problem(t, "jobs.unmatched")
	if !strings.Contains(prob.Detail, "belongs to installation") || strings.Contains(prob.Detail, "no enabled pool carries that label") ||
		strings.Contains(prob.Fix, "enrol a medium host") {
		t.Fatalf("the problem is %+v; it should say what the scheduler said", prob)
	}
}

// ---------------------------------------------------------------------------
// Label advice
// ---------------------------------------------------------------------------

var measuredRuns atomic.Int64

// measuredRun records one finished, measured run of a job with the labels it
// asked for, the way the controller would have: in progress on a runner, sampled,
// then completed. i says how long ago, in hours before an hour ago, and the
// completed job comes back so the class its runs call for can be kept.
func (h *harness) measuredRun(i int, job string, labels []string, memoryMB int64) *store.Job {
	h.t.Helper()
	return h.measuredRunAt(h.c.Now().Add(-time.Duration(20-i)*time.Hour), job, labels, memoryMB)
}

// measuredRunAt is measuredRun for a run that started at a given time.
func (h *harness) measuredRunAt(at time.Time, job string, labels []string, memoryMB int64) *store.Job {
	h.t.Helper()
	id := measuredRuns.Add(1)
	done := at.Add(5 * time.Minute)
	runner := fmt.Sprintf("run_advice_%d", id)
	base := store.Job{GitHubJobID: 90000 + id, Repo: "acme/widgets", Workflow: "CI", JobName: job, Labels: labels, QueuedAt: at}
	running := base
	running.State, running.RunnerID, running.StartedAt = store.JobInProgress, runner, &at
	if _, _, err := h.st.ApplyJob(h.ctx, &running); err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.RecordJobUsage(h.ctx, runner, 1, memoryMB); err != nil {
		h.t.Fatal(err)
	}
	finished := base
	finished.State, finished.Conclusion, finished.StartedAt, finished.CompletedAt = store.JobCompleted, "success", &at, &done
	saved, _, err := h.st.ApplyJob(h.ctx, &finished)
	if err != nil {
		h.t.Fatal(err)
	}
	return saved
}

// A job that finishes four times a day has twenty runs inside five days, and
// moving down takes ten runs over a week since it last moved. If the controller
// read only the newest twenty runs to class it, a job that busy could only ever
// move up: it would be shown five days of evidence however long it had fitted.
func TestABusyJobIsMovedDownOnAWeekOfRunsAndNotOnTheNewestTwenty(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeShadow)
	labels := []string{"self-hosted", "zoomies"}
	// It goes into medium now, which is when the store notes it moved there, and
	// then thirteen days go by on the controller's clock.
	if _, err := h.st.PutJobClass(h.ctx, &store.JobClass{Repo: "acme/widgets", Workflow: "CI", JobName: "build",
		Class: store.SizeMedium, Basis: store.SizeBasisHistory, Reason: "it needed more once"}); err != nil {
		t.Fatal(err)
	}
	h.advance(13 * 24 * time.Hour)
	var last *store.Job
	for i := range 40 {
		last = h.measuredRunAt(h.c.Now().Add(-time.Duration(i*6)*time.Hour), "build", labels, 700)
	}

	h.c.refreshJobClass(h.ctx, last)
	kept, err := h.st.GetJobClass(h.ctx, "acme/widgets", "CI", "build")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Class != store.SizeSmall || !strings.Contains(kept.Reason, "so it moved down from medium") {
		t.Fatalf("forty light runs over ten days, in a class held for thirteen, left the job in %s: %s", kept.Class, kept.Reason)
	}
}

func TestLabelAdviceIsWorkedOutFromWhatRunsCallForAndWhatTheJobAsksAndRaisesOneEntry(t *testing.T) {
	h := newHarness(t)
	h.classFleet(scheduler.SizeShadow)
	// Twelve runs each: "e2e" asks for medium and needs 6 GB, "docs" asks for
	// large and needs 300 MB, "build" asks for nothing and needs 6 GB, and "fine"
	// asks for large and needs it.
	asks := map[string]struct {
		labels []string
		mb     int64
	}{
		"e2e":   {[]string{"self-hosted", "zoomies-medium"}, 6000},
		"docs":  {[]string{"self-hosted", "zoomies-large"}, 300},
		"build": {[]string{"self-hosted", "zoomies"}, 6000},
		"fine":  {[]string{"self-hosted", "zoomies-large"}, 6000},
	}
	for name, a := range asks {
		var last *store.Job
		for i := 1; i <= 12; i++ {
			last = h.measuredRun(i, name, a.labels, a.mb)
		}
		// Finishing a job is what keeps the class its runs call for.
		h.c.refreshJobClass(h.ctx, last)
	}
	got, window, err := h.c.LabelAdvice(h.ctx, AdviceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byJob := map[string]*scheduler.Advice{}
	for _, a := range got {
		byJob[a.JobName] = a
	}
	if len(got) != 3 || byJob["fine"] != nil {
		t.Fatalf("advice = %d entries %+v; the job that asks for what it needs has none", len(got), byJob)
	}
	if byJob["e2e"].Kind != scheduler.AdviceTooSmall || byJob["build"].Kind != scheduler.AdviceUnguaranteed || byJob["docs"].Kind != scheduler.AdviceTooLarge {
		t.Fatalf("kinds: e2e %s, build %s, docs %s", byJob["e2e"].Kind, byJob["build"].Kind, byJob["docs"].Kind)
	}
	if got[0].JobName != "e2e" || got[2].JobName != "docs" {
		t.Fatalf("advice is not in order of cost: %s, %s, %s", got[0].JobName, got[1].JobName, got[2].JobName)
	}
	// Every row carries the figures its runs showed, over the default window,
	// and whether the fleet has a host of the class it recommends.
	if window.Applied.Duration() != scheduler.ClassWindow || window.Bound != AdviceBoundAsked {
		t.Fatalf("window = %+v, want the class window, bound by what was asked", window)
	}
	for _, a := range got {
		if a.Observed == nil || a.Observed.Runs != 12 || a.Fits == nil || !a.Fits.OK || a.Fits.Missing != "" {
			t.Fatalf("%s: observed %+v, fits %+v", a.JobName, a.Observed, a.Fits)
		}
	}
	if o := byJob["e2e"].Observed; o.MemoryMB.P95 != 6000 || o.MemoryMB.Max != 6000 || o.CPU.P50 != 1 {
		t.Fatalf("e2e observed %+v, want twelve runs of 6000 MB and 1 CPU", o)
	}
	// A window reaches back only as far as it says: an hour holds none of
	// runs that finished eight hours ago and more, while the class's own count
	// is what it was kept with.
	short, window, err := h.c.LabelAdvice(h.ctx, AdviceOptions{Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if window.Applied.Duration() != time.Hour || len(short) != 3 || short[0].Observed.Runs != 0 || short[0].Runs != 12 {
		t.Fatalf("an hour's window: %+v, first row observed %+v runs %d", window, short[0].Observed, short[0].Runs)
	}
	// The repository filter is by the job's repository, as GitHub compares one.
	if mine, _, _ := h.c.LabelAdvice(h.ctx, AdviceOptions{Repo: "Acme/Widgets"}); len(mine) != 3 {
		t.Fatalf("%d rows for the repository that has them", len(mine))
	}
	if other, _, _ := h.c.LabelAdvice(h.ctx, AdviceOptions{Repo: "acme/docs"}); len(other) != 0 {
		t.Fatalf("%d rows for a repository with no jobs", len(other))
	}

	probs, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, p := range probs {
		if p.Code == "jobs.label_advice" {
			found++
			if p.Severity != config.SeverityInfo || !strings.Contains(p.Title, "3 workflow jobs") ||
				!strings.Contains(p.Detail, "1 job asks for a class too small") || !strings.Contains(p.Fix, "zoomies jobs advice") {
				t.Fatalf("the problem is %+v", p)
			}
		}
	}
	if found != 1 {
		t.Fatalf("%d label advice problems; there is one however many jobs there is advice for", found)
	}

	// A pin is the operator's decision, so the job it covers is left alone.
	if err := h.st.SetSizePin(h.ctx, &store.SizePin{Repo: "acme/widgets", Workflow: "CI", JobName: "e2e", Class: store.SizeMedium}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = h.c.LabelAdvice(h.ctx, AdviceOptions{}); len(got) != 2 {
		t.Fatalf("a pinned job is still advised on: %d entries", len(got))
	}

	// With routing off nothing is compared, and the entry goes.
	h.c.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = scheduler.SizeOff })
	if got, _, _ = h.c.LabelAdvice(h.ctx, AdviceOptions{}); len(got) != 0 {
		t.Fatalf("%d entries of advice with size routing off", len(got))
	}
	probs, _ = h.c.Problems(h.ctx)
	for _, p := range probs {
		if p.Code == "jobs.label_advice" {
			t.Fatalf("the problem outlived the switch: %+v", p)
		}
	}
}

// Advice that recommends a class no host carries is advice to buy a machine,
// and the row says so by name rather than sending a reader to the Hosts page
// to find out; a sparse job is a row with its count, not an absence, and it
// is not counted as advice in the problems list.
func TestAdviceSaysWhenNoHostCarriesTheClassAndWhenRunsAreTooFew(t *testing.T) {
	h := newHarness(t)
	h.classFleetOf(scheduler.SizeShadow, store.SizeSmall, store.SizeMedium)
	var last *store.Job
	for i := 1; i <= 12; i++ {
		last = h.measuredRun(i, "e2e", []string{"self-hosted", "zoomies-medium"}, 6000)
	}
	h.c.refreshJobClass(h.ctx, last)
	for i := 1; i <= scheduler.AdviceMinRuns-1; i++ {
		last = h.measuredRun(i, "new", []string{"self-hosted", "zoomies-medium"}, 6000)
	}
	h.c.refreshJobClass(h.ctx, last)

	got, _, err := h.c.LabelAdvice(h.ctx, AdviceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].JobName != "e2e" || got[1].JobName != "new" {
		t.Fatalf("advice = %+v, want the advised job then the sparse one", got)
	}
	if f := got[0].Fits; f == nil || f.OK || f.Missing != store.SizeLarge {
		t.Fatalf("fits = %+v, want the missing large class named", f)
	}
	sparse := got[1]
	if sparse.State != scheduler.AdviceStateNotEnoughData || sparse.Observed == nil || sparse.Observed.Runs != scheduler.AdviceMinRuns-1 || sparse.Observed.MemoryMB.P95 != 6000 {
		t.Fatalf("the sparse row = %+v, observed %+v", sparse, sparse.Observed)
	}
	counts, total, err := h.c.labelAdviceCounts(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || counts[scheduler.AdviceStateNotEnoughData] != 1 || counts[scheduler.AdviceTooSmall] != 1 {
		t.Fatalf("counts = %v, total %d: a sparse row is counted as such and is not advice", counts, total)
	}
}

// A window longer than the history the fleet keeps would promise figures
// over runs that have been pruned, so retention bounds it and the answer
// says which bound applied.
func TestTheAdviceWindowIsBoundedByRetention(t *testing.T) {
	h := newHarness(t)
	day := 24 * time.Hour
	cases := []struct {
		retention, asked, applied time.Duration
		bound                     string
	}{
		{7 * day, 14 * day, 7 * day, AdviceBoundRetention},
		{7 * day, 3 * day, 3 * day, AdviceBoundAsked},
		{0, 400 * day, 400 * day, AdviceBoundAsked},
		{7 * day, 0, 7 * day, AdviceBoundRetention},
		{0, 0, scheduler.ClassWindow, AdviceBoundAsked},
	}
	for _, tc := range cases {
		h.c.UpdateConfig(func(cfg *config.Config) { cfg.Retention.Jobs = tc.retention })
		got := h.c.adviceWindow(tc.asked)
		if got.Applied.Duration() != tc.applied || got.Bound != tc.bound {
			t.Errorf("retention %s, asked %s: got %+v, want applied %s bound %s", tc.retention, tc.asked, got, tc.applied, tc.bound)
		}
		want := tc.asked
		if want == 0 {
			want = scheduler.ClassWindow
		}
		if got.Asked.Duration() != want {
			t.Errorf("asked %s is reported as %s", tc.asked, got.Asked)
		}
	}
}
