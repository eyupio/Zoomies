package scheduler

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// run is one measured, finished run of a job, daysAgo days before sizeEpoch, on a
// small runner unless the options say otherwise.
func run(daysAgo float64, memoryMB int64, opts ...func(*store.JobRun)) store.JobRun {
	r := store.JobRun{
		CompletedAt: sizeEpoch.Add(-time.Duration(daysAgo * float64(24*time.Hour))),
		Duration:    6 * time.Minute,
		PeakCPUs:    2, PeakMemoryMB: memoryMB,
		GrantedCPUs: 1, GrantedMemoryMB: 2048,
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func oomKilled(r *store.JobRun) { r.OOMKilled = true }
func throttled(share float64) func(*store.JobRun) {
	return func(r *store.JobRun) { r.CPUPeriods, r.CPUThrottledPeriods = 1000, int64(share*1000) }
}
func on(cpus float64, memoryMB int64) func(*store.JobRun) {
	return func(r *store.JobRun) { r.GrantedCPUs, r.GrantedMemoryMB = cpus, memoryMB }
}
func lasting(d time.Duration) func(*store.JobRun) { return func(r *store.JobRun) { r.Duration = d } }

// history is n runs spread evenly over the last spanDays days, newest first, each
// with the given peak memory.
func history(n int, spanDays float64, memoryMB int64, opts ...func(*store.JobRun)) []store.JobRun {
	out := make([]store.JobRun, n)
	for i := range out {
		days := 0.0
		if n > 1 {
			days = spanDays * float64(i) / float64(n-1)
		}
		out[i] = run(days, memoryMB, opts...)
	}
	return out
}

// newestFirst puts runs in the order the store returns them.
func newestFirst(runs ...[]store.JobRun) []store.JobRun {
	var out []store.JobRun
	for _, r := range runs {
		out = append(out, r...)
	}
	slices.SortStableFunc(out, func(a, b store.JobRun) int { return b.CompletedAt.Compare(a.CompletedAt) })
	return out
}

func classify(in ClassifyInput) Classification {
	in.Now = sizeEpoch
	return DefaultSizeConfig().Classify(in)
}

func current(class store.SizeClass, movedDaysAgo float64) *store.JobClass {
	return &store.JobClass{Class: class, MovedAt: sizeEpoch.Add(-time.Duration(movedDaysAgo * float64(24*time.Hour)))}
}

func TestASizeLabelInRunsOnIsTheWholeAnswer(t *testing.T) {
	// The author said where the job goes, and that is honoured over a pin and
	// over every measurement: it is the one path that is guaranteed.
	got := classify(ClassifyInput{
		Labels: []string{"self-hosted", "Linux", "Zoomies-Large"},
		Pin:    &store.SizePin{Repo: "acme/api", Class: store.SizeSmall},
		Runs:   history(10, 5, 500),
	})
	if got.Class != store.SizeLarge || got.Basis != store.SizeBasisExplicit || got.FloorMB != 0 ||
		got.Reason != "its runs-on asks for zoomies-large" {
		t.Fatalf("an explicit label was classed as %+v", got)
	}

	// A job asking for two, which no pool answers, is taken to want the larger.
	got = classify(ClassifyInput{Labels: []string{"zoomies-small", "zoomies-medium"}})
	if got.Class != store.SizeMedium || got.Basis != store.SizeBasisExplicit {
		t.Fatalf("two size labels were classed as %+v", got)
	}

	// Labels that only look like one are not.
	got = classify(ClassifyInput{Labels: []string{"zoomies", "large", "zoomies-large-arm64", "x64"}})
	if got.Basis == store.SizeBasisExplicit {
		t.Fatalf("a look-alike label was taken for a size label: %+v", got)
	}
}

func TestAPinTakesThePlaceOfTheMeasurements(t *testing.T) {
	heavy := history(12, 6, 30000, oomKilled)
	job := classify(ClassifyInput{
		Pin: &store.SizePin{Repo: "acme/api", Workflow: "CI", JobName: "build", Class: store.SizeSmall}, Runs: heavy,
	})
	if job.Class != store.SizeSmall || job.Basis != store.SizeBasisPin || job.Reason != "an operator pinned this job to small" {
		t.Fatalf("a job pin was classed as %+v", job)
	}
	repo := classify(ClassifyInput{Pin: &store.SizePin{Repo: "acme/api", Class: store.SizeLarge}})
	if repo.Class != store.SizeLarge || repo.Reason != "an operator pinned every job in acme/api to large" {
		t.Fatalf("a repository pin was classed as %+v", repo)
	}
}

func TestAJobWithNoHistoryStartsInTheDefaultClass(t *testing.T) {
	got := classify(ClassifyInput{})
	if got.Class != store.SizeMedium || got.Basis != store.SizeBasisDefault || got.FloorMB != 0 || got.Runs != 0 ||
		!strings.Contains(got.Reason, "no measured runs") {
		t.Fatalf("a new job was classed as %+v", got)
	}

	cfg := DefaultSizeConfig()
	cfg.DefaultClass = store.SizeSmall
	if got := cfg.Classify(ClassifyInput{Now: sizeEpoch}); got.Class != store.SizeSmall || !strings.Contains(got.Reason, "small") {
		t.Fatalf("the configured default was ignored: %+v", got)
	}
	cfg.DefaultClass = "huge"
	if got := cfg.Classify(ClassifyInput{Now: sizeEpoch}); got.Class != store.SizeSmall {
		t.Fatalf("a default that is not a class gave %+v, want small", got)
	}
}

// A job nobody has run for a fortnight has no evidence either way, and the best
// guess at what it needs is what it needed last: sending it back to the default
// would have it killed on the way to relearning it.
func TestAJobThatHasNotRunLatelyKeepsItsClass(t *testing.T) {
	got := classify(ClassifyInput{Current: current(store.SizeMedium, 30)})
	if got.Class != store.SizeMedium || got.Basis != store.SizeBasisHistory || !strings.Contains(got.Reason, "14 days") {
		t.Fatalf("a job with no recent runs was classed as %+v", got)
	}
}

func TestMemoryDecidesTheClassFromTheNinetiethPercentileWithAMargin(t *testing.T) {
	// A small runner has 2 GB, a medium one 4 and a large one 8, and a fifth is
	// added to what the runs needed. 1700 MB becomes 2040 and fits 2048; 1800
	// becomes 2160 and does not.
	cases := []struct {
		name     string
		peakMB   int64
		want     store.SizeClass
		wantText string
	}{
		{"well inside a small runner", 1000, store.SizeSmall, "which a small runner holds"},
		{"inside it with the margin", 1700, store.SizeSmall, "which a small runner holds"},
		{"over it once the margin is added", 1800, store.SizeMedium, "which a medium runner holds"},
		{"inside a medium runner with the margin", 3400, store.SizeMedium, "which a medium runner holds"},
		{"over it", 3500, store.SizeLarge, "which a large runner holds"},
		{"more than any class holds", 10000, store.SizeLarge, "more than a large runner has (8 GB)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(ClassifyInput{Runs: history(12, 6, tc.peakMB)})
			if got.Class != tc.want || got.Basis != store.SizeBasisHistory || got.Runs != 12 {
				t.Fatalf("12 runs at %d MB were classed as %+v, want %s", tc.peakMB, got, tc.want)
			}
			if !strings.Contains(got.Reason, tc.wantText) || !strings.Contains(got.Reason, "12 runs") {
				t.Errorf("the reason %q does not say %q and how many runs it was worked out from", got.Reason, tc.wantText)
			}
			if got.FloorMB <= 0 {
				t.Errorf("a class worked out from memory has no floor: %+v", got)
			}
		})
	}
}

// One run that pulled a cold cache should not size every run after it, and a
// single bad one among many is what the percentile is for.
func TestOneHeavyRunAmongManyDoesNotMoveAJob(t *testing.T) {
	runs := history(19, 10, 1000)
	runs = append([]store.JobRun{run(0, 7000)}, runs...)
	if got := classify(ClassifyInput{Runs: runs}); got.Class != store.SizeSmall {
		t.Fatalf("one heavy run among twenty classed the job %s: %s", got.Class, got.Reason)
	}
	// Two, in twenty, are still inside what the percentile leaves out; three are not.
	runs = append([]store.JobRun{run(0, 7000), run(0.1, 7000), run(0.2, 7000)}, history(17, 10, 1000)...)
	if got := classify(ClassifyInput{Runs: runs}); got.Class != store.SizeLarge {
		t.Fatalf("three heavy runs among twenty classed the job %s: %s", got.Class, got.Reason)
	}
}

// A kill is evidence that does not wait for a percentile: the 90th of twenty
// ignores the two largest runs, which is exactly where a single kill sits.
func TestAKillForMemoryMovesAJobUpAtOnce(t *testing.T) {
	// Killed on a small runner's 2 GB it needs more than that: half as much
	// again, and a fifth on top, is about 3.6 GB, which a medium runner holds.
	runs := append([]store.JobRun{run(1, 2000, oomKilled)}, history(19, 10, 1000)...)
	got := classify(ClassifyInput{Runs: runs})
	if got.Class != store.SizeMedium || !strings.Contains(got.Reason, "killed a run for memory at 2 GB") ||
		!strings.Contains(got.Reason, "need about 3.6 GB") {
		t.Fatalf("a job killed for memory at 2 GB was classed as %+v", got)
	}
	if got.FloorMB < 3600 {
		t.Fatalf("its floor %d MB is under what a kill implies", got.FloorMB)
	}

	// Killed on a medium runner's 4 GB it needs more than medium has.
	runs = append([]store.JobRun{run(1, 4000, oomKilled, on(2, 4096))}, history(19, 10, 1000)...)
	if got := classify(ClassifyInput{Runs: runs}); got.Class != store.SizeLarge {
		t.Fatalf("a job killed at 4 GB was classed as %s: %s", got.Class, got.Reason)
	}

	// Moving up from medium says so.
	got = classify(ClassifyInput{Runs: runs, Current: current(store.SizeMedium, 5)})
	if got.Class != store.SizeLarge || !strings.HasSuffix(got.Reason, "so it moved up from medium") {
		t.Fatalf("a move up was classed as %+v", got)
	}

	// A kill before the class last moved is what moved it, and moves it no
	// further. The case that matters is the one where the class has since moved
	// down: the kill is still inside the window, and counting it again would put
	// the job straight back where the evidence had just taken it from.
	runs = newestFirst(history(12, 6, 1000, on(2, 4096)), []store.JobRun{run(10, 4000, oomKilled, on(2, 4096))})
	got = classify(ClassifyInput{Runs: runs, Current: current(store.SizeMedium, 1)})
	if got.Class != store.SizeMedium {
		t.Fatalf("a kill from before the last move moved the job again: %+v", got)
	}
	// The same kill with no move since is evidence, and is acted on.
	got = classify(ClassifyInput{Runs: runs, Current: current(store.SizeMedium, 12)})
	if got.Class != store.SizeLarge {
		t.Fatalf("a kill since the last move did not move the job: %+v", got)
	}
}

func TestAJobHeldBackByItsCPULimitMovesUpAClass(t *testing.T) {
	bound := func(n int, share float64, d time.Duration, opts ...func(*store.JobRun)) []store.JobRun {
		return history(n, 5, 900, append([]func(*store.JobRun){throttled(share), lasting(d)}, opts...)...)
	}
	cases := []struct {
		name string
		runs []store.JobRun
		want store.SizeClass
	}{
		{"held back in every sampled run, on a small runner's one CPU", bound(6, 0.4, 5*time.Minute), store.SizeMedium},
		{"on a medium runner's two, it needs a large one", bound(6, 0.4, 5*time.Minute, on(2, 4096)), store.SizeLarge},
		{"already on the largest, there is nowhere further", bound(6, 0.4, 5*time.Minute, on(4, 8192)), store.SizeLarge},
		{"only a little throttled is just a busy job", bound(6, 0.1, 5*time.Minute), store.SizeSmall},
		{"too few sampled runs to say anything", bound(2, 0.4, 5*time.Minute), store.SizeSmall},
		{"runs too short for being held back to cost anything", bound(6, 0.4, time.Minute), store.SizeSmall},
		{"never sampled", history(6, 5, 900), store.SizeSmall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(ClassifyInput{Runs: tc.runs})
			if got.Class != tc.want {
				t.Fatalf("classed %s, want %s: %s", got.Class, tc.want, got.Reason)
			}
		})
	}

	// Held back in under half of the runs it was sampled in is not a pattern.
	mixed := append(bound(2, 0.4, 5*time.Minute), history(4, 5, 900, throttled(0.01), lasting(5*time.Minute))...)
	if got := classify(ClassifyInput{Runs: mixed}); got.Class != store.SizeSmall {
		t.Fatalf("2 CPU-bound runs in 6 classed the job %s", got.Class)
	}
	// Half is.
	half := append(bound(3, 0.4, 5*time.Minute), history(3, 5, 900, throttled(0.01), lasting(5*time.Minute))...)
	got := classify(ClassifyInput{Runs: half})
	if got.Class != store.SizeMedium || !strings.Contains(got.Reason, "3 of its 6 sampled runs were held back by their CPU limit (1 CPU)") {
		t.Fatalf("3 CPU-bound runs in 6 were classed as %+v", got)
	}
}

// Moving up is quick because a job that does not fit is killed, and moving down
// is slow because one that does fit is only a little slower for the machine
// being too large. A class that swung with every run would put the same job on
// a different host each time.
func TestAJobMovesDownOnlyOnSustainedEvidence(t *testing.T) {
	light := func(n int, spanDays float64, opts ...func(*store.JobRun)) []store.JobRun {
		return history(n, spanDays, 1000, append([]func(*store.JobRun){on(2, 4096)}, opts...)...)
	}
	cases := []struct {
		name string
		runs []store.JobRun
		cur  *store.JobClass
		want store.SizeClass
		text string
	}{
		{"ten runs over nine days, all small", light(10, 9), current(store.SizeMedium, 12), store.SizeSmall, "all fit a small runner"},
		{"nine runs is not enough", light(9, 9), current(store.SizeMedium, 12), store.SizeMedium, "moving down takes 10 runs over 7 days"},
		{"ten runs over five days is not enough", light(10, 5), current(store.SizeMedium, 12), store.SizeMedium, "there have been 10 over 5 days"},
		{"runs from before the move are the evidence for it, not against it", light(12, 9), current(store.SizeMedium, 2), store.SizeMedium, "since it moved to medium"},
		{"one run that needs more than a small runner", newestFirst(light(11, 9), []store.JobRun{run(3, 3000, on(2, 4096))}), current(store.SizeMedium, 12), store.SizeMedium, ""},
		// A kill on a runner so small that it still fits the smaller class says
		// nothing about moving up, and everything about moving down.
		{"a kill among them", newestFirst(light(11, 9), []store.JobRun{run(3, 700, oomKilled, on(1, 512))}), current(store.SizeMedium, 12), store.SizeMedium, ""},
		// Held back at the smaller class's own CPU count is evidence the job
		// wants more than that, which is a reason not to move it there.
		{"held back by the CPU limit", light(12, 9, on(1, 4096), throttled(0.5), lasting(8*time.Minute)), current(store.SizeMedium, 12), store.SizeMedium, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(ClassifyInput{Runs: tc.runs, Current: tc.cur})
			if got.Class != tc.want {
				t.Fatalf("classed %s, want %s: %s", got.Class, tc.want, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.text) {
				t.Errorf("the reason %q does not say %q", got.Reason, tc.text)
			}
		})
	}

	// It moves down a class, not to wherever the runs fit: a job in large whose
	// every run fits small goes to medium and has to earn the next step too.
	got := classify(ClassifyInput{Runs: light(12, 9), Current: current(store.SizeLarge, 20)})
	if got.Class != store.SizeMedium || !strings.Contains(got.Reason, "so it moved down from large") {
		t.Fatalf("a job in large was classed as %+v", got)
	}
}

// A job that finishes every few hours has twenty runs inside a day or two, so
// "ten runs over a week since it moved" cannot be read from its newest twenty.
// The controller reads far more than that, and the classifier takes the
// percentile from the newest twenty and the evidence for moving down from all of
// them: otherwise every busy job could only ever move up.
func TestABusyJobMovesDownOnAWeekOfRunsNotOnTheNewestTwenty(t *testing.T) {
	// Seven runs a day for two weeks, every one light, in a class it moved to
	// thirteen days ago.
	busy := history(98, 13, 700, on(2, 4096))
	if got := classify(ClassifyInput{Runs: busy, Current: current(store.SizeMedium, 13)}); got.Class != store.SizeSmall ||
		!strings.Contains(got.Reason, "so it moved down from medium") {
		t.Fatalf("98 light runs over 13 days classed the job %+v", got)
	}
	// The newest twenty alone span less than three days: no evidence of a week,
	// which is the answer a caller that read only those would have got for ever.
	if got := classify(ClassifyInput{Runs: busy[:ClassRuns], Current: current(store.SizeMedium, 13)}); got.Class != store.SizeMedium {
		t.Fatalf("the newest %d runs alone moved the job to %s", ClassRuns, got.Class)
	}
	// And what it needs is still read from the newest twenty: a run that needed
	// a great deal, twelve days ago among ninety-eight, is not what it needs now.
	old := append(append([]store.JobRun{}, busy...), run(12.5, 9000, on(2, 4096)))
	if got := classify(ClassifyInput{Runs: newestFirst(old), Current: current(store.SizeMedium, 13)}); got.Class != store.SizeMedium {
		t.Fatalf("a heavy run among ninety-nine light ones should keep the job in medium, got %+v", got)
	}
}

// Moving down looks at the runs since the job moved, and moving up at its newest
// runs, so a job whose light runs came after its heavy ones used to be moved down
// on the first reading and back up on the second. A move down now has to be one
// the newest runs agree with, and once made it is not undone by the runs it was
// made on.
func TestAJobIsNotMovedDownOnlyToBeMovedStraightBackUp(t *testing.T) {
	runs := newestFirst(
		history(11, 8, 1000, on(2, 4096)),
		// Heavy runs from before it moved, still among its newest.
		history(3, 1, 3000, on(2, 4096)),
	)
	// The heavy ones are eleven and twelve days ago.
	for i := range runs {
		if runs[i].PeakMemoryMB == 3000 {
			runs[i].CompletedAt = runs[i].CompletedAt.Add(-11 * 24 * time.Hour)
		}
	}
	runs = newestFirst(runs)
	first := classify(ClassifyInput{Runs: runs, Current: current(store.SizeMedium, 9)})
	if first.Class != store.SizeMedium {
		t.Fatalf("the job moved down to %s although its newest runs still say it needs about 3.6 GB: %s", first.Class, first.Reason)
	}
	// Nothing about a second look changes the answer.
	if again := classify(ClassifyInput{Runs: runs, Current: current(first.Class, 9)}); again.Class != store.SizeMedium {
		t.Fatalf("the second reading moved the job to %s", again.Class)
	}
}

func TestAJobStaysWhereItIsWhenItsRunsFitThere(t *testing.T) {
	got := classify(ClassifyInput{Runs: history(12, 6, 3000), Current: current(store.SizeMedium, 9)})
	if got.Class != store.SizeMedium || got.Basis != store.SizeBasisHistory || !strings.Contains(got.Reason, "which a medium runner holds") {
		t.Fatalf("a job that fits where it is was classed as %+v", got)
	}
}

// Classifying the same history twice gives the same answer, because the
// controller does it on every job that finishes.
func TestClassifyingTheSameHistoryTwiceGivesTheSameAnswer(t *testing.T) {
	in := ClassifyInput{Runs: history(14, 8, 2500), Current: current(store.SizeMedium, 3)}
	first := classify(in)
	for range 3 {
		if again := classify(in); again != first {
			t.Fatalf("classification is not stable: %+v then %+v", first, again)
		}
	}
}
