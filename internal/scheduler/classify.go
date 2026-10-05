package scheduler

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// How a job's class is worked out from its runs. These are the rules rather
// than settings: what an operator chooses is how big each class is and which
// one an unknown job starts in, and the evidence it takes to move a job is
// part of what the feature means. A figure that could be tuned per fleet would
// be one more thing to be wrong about when a job sits in the wrong class and
// somebody asks why.
const (
	// ClassWindow is how far back a job's runs count. A job rewritten a month
	// ago is classed on what it is now.
	ClassWindow = 14 * 24 * time.Hour

	// ClassRuns is how many of a job's newest runs its memory percentile and its
	// CPU evidence are worked out from: what it needs now, as the history sizing
	// of a pool reads it too. It is not how many runs the evidence for moving
	// down is read from. A job that runs a few times an hour has twenty runs
	// inside a day, and ten runs over a week "since it last moved" can only be
	// seen by looking further back than the newest twenty.
	ClassRuns = store.JobUsageHistoryLimit

	// DownRuns and DownSpan are the evidence it takes to move a job to a smaller
	// class: this many measured runs, spread over at least this long, every one
	// of which fits the smaller class. Moving up is quick because a job that
	// does not fit is killed, and moving down is slow because one that does
	// fit is only a little slower for the machine being too large; a class
	// that swung with every run would put the same job on a different host each
	// time.
	DownRuns = 10
	DownSpan = 7 * 24 * time.Hour

	// ThrottledShare is the share of its CPU periods a run is held back in for
	// the run to count as CPU-bound. The peak CPU of a build is its limit -- it
	// uses every core it is given -- so the peak says nothing about whether it
	// wanted more; being held back by the quota does.
	ThrottledShare = 0.25
	// ThrottleRuns is how many sampled runs it takes to say anything about a
	// job's CPU, and ThrottleFraction how many of them must be CPU-bound.
	ThrottleRuns     = 3
	ThrottleFraction = 0.5
	// ThrottleMinDuration is how long the CPU-bound runs must typically be. A
	// job that runs for twenty seconds is throttled in its first second by
	// arithmetic, and moving it up a class would buy it nothing.
	ThrottleMinDuration = 3 * time.Minute
)

// ClassifyInput is everything Classify decides from.
type ClassifyInput struct {
	// Labels are the job's runs-on labels. A size label among them is the whole
	// answer.
	Labels []string
	// Pin is an operator's pin for this job or its repository, or nil.
	Pin *store.SizePin
	// Runs are the job's measured runs that finished inside ClassWindow, newest
	// first. There may be many more than ClassRuns of them: the newest ClassRuns
	// say what the job needs, and all of them say how long it has been fitting a
	// smaller class.
	Runs []store.JobRun
	// Current is the class kept from the last time the job was worked out, or
	// nil if it never was.
	Current *store.JobClass
	Now     time.Time
}

// Classification is the answer: the class, the sentence for why, how it was
// reached, and what the history says the job needs.
type Classification struct {
	Class store.SizeClass
	// Reason finishes "classed X because": a lower-case clause that says what
	// decided it, in the words of the measurement.
	Reason string
	// Basis is one of the store.SizeBasis* constants.
	Basis string
	// FloorMB is the memory the job is known to need, and zero when its class
	// did not come from its runs.
	FloorMB int64
	// Runs is how many measured runs the answer was worked out from.
	Runs int
}

// Classify puts a job in a class.
//
// The order is the order of authority. A size label in the job's own runs-on
// is the author saying where it goes, and it is honoured whatever anyone
// measured. An operator's pin comes next, because it is a decision that takes
// the place of the measurements. Only then is the history asked, and with no
// history at all the job starts in the default class.
func (c SizeConfig) Classify(in ClassifyInput) Classification {
	if out, ok := c.authority(in.Labels, in.Pin); ok {
		return out
	}
	return c.fromRuns(in)
}

// authority is the part of the answer that does not depend on any measurement:
// a size label in the job's own runs-on, and an operator's pin.
func (c SizeConfig) authority(labels []string, pin *store.SizePin) (Classification, bool) {
	if class, ok := RequestedClass(labels); ok {
		return Classification{Class: class, Basis: store.SizeBasisExplicit,
			Reason: fmt.Sprintf("its runs-on asks for %s", class.Label())}, true
	}
	if pin != nil && pin.Class.Valid() {
		what := "this job"
		if pin.ForRepository() {
			what = "every job in " + pin.Repo
		}
		return Classification{Class: pin.Class, Basis: store.SizeBasisPin,
			Reason: fmt.Sprintf("an operator pinned %s to %s", what, pin.Class)}, true
	}
	return Classification{}, false
}

// RequestedClass is the class a runs-on asks for by name, if it does. A job that
// asks for two -- which no pool answers -- is taken to want the larger.
func RequestedClass(labels []string) (store.SizeClass, bool) {
	var out store.SizeClass
	for _, l := range labels {
		if k, ok := store.ClassOfLabel(l); ok && k.Rank() > out.Rank() {
			out = k
		}
	}
	return out, out != ""
}

// holding is the smallest class whose runner has at least mb of memory, and the
// largest class for a need that none does.
func (c SizeConfig) holding(mb int64) store.SizeClass {
	for _, k := range store.SizeClasses() {
		if c.Runner(k).MemoryMB >= mb {
			return k
		}
	}
	return store.SizeLarge
}

// defaultClass is the class of a job nothing is known about: the configured
// one, and small where that is not a class.
func (c SizeConfig) defaultClass() store.SizeClass {
	if c.DefaultClass.Valid() {
		return c.DefaultClass
	}
	return store.SizeSmall
}

func (c SizeConfig) fromRuns(in ClassifyInput) Classification {
	var current store.SizeClass
	var movedAt time.Time
	if in.Current != nil && in.Current.Class.Valid() {
		current, movedAt = in.Current.Class, in.Current.MovedAt
	}
	runs := in.Runs
	if len(runs) == 0 {
		if current != "" {
			return Classification{Class: current, Basis: store.SizeBasisHistory,
				Reason: fmt.Sprintf("it has had no measured run in the last %s, so it keeps the class it had", longSpan(ClassWindow))}
		}
		def := c.defaultClass()
		return Classification{Class: def, Basis: store.SizeBasisDefault,
			Reason: fmt.Sprintf("it has no measured runs yet, so it starts in the default class, %s", def)}
	}

	// What the job needs now is read from its newest runs; how long it has been
	// fitting a smaller class, below, from every run since it last moved.
	recent := runs[:min(len(runs), ClassRuns)]
	peaks := make([]store.JobPeak, len(recent))
	for i, r := range recent {
		peaks[i] = store.JobPeak{CPUs: r.PeakCPUs, MemoryMB: r.PeakMemoryMB, OOMKilled: r.OOMKilled}
	}
	req := Profile(peaks)
	memClass := c.holding(req.MemoryMB)

	// A run the kernel killed for memory is evidence that does not wait for a
	// percentile: its peak is the limit it hit, and the 90th percentile of
	// twenty runs ignores the two largest of them, which is exactly where a
	// single kill sits. Only kills since the class last moved count, because an
	// earlier one is what moved it.
	oomNeed, oomLimit := int64(0), int64(0)
	for _, r := range runs {
		if r.OOMKilled && r.CompletedAt.After(movedAt) {
			limit := max(r.PeakMemoryMB, r.GrantedMemoryMB)
			if need := roundUp(float64(limit)*ProfileOOMGrowth*ProfileMemoryMargin, profileMemoryStep); need > oomNeed {
				oomNeed, oomLimit = need, limit
			}
		}
	}
	oomClass := store.SizeSmall
	if oomNeed > 0 {
		oomClass = c.holding(oomNeed)
	}

	cpuClass, cpuNote := c.cpuClass(recent)

	proposed := memClass
	for _, k := range []store.SizeClass{oomClass, cpuClass} {
		if k.Rank() > proposed.Rank() {
			proposed = k
		}
	}
	floor := max(req.MemoryMB, oomNeed)
	out := Classification{Basis: store.SizeBasisHistory, FloorMB: floor, Runs: len(recent)}

	switch {
	case current == "" || proposed.Rank() > current.Rank():
		out.Class = proposed
		var because []string
		switch {
		case oomNeed > 0 && oomClass == proposed:
			because = append(because, fmt.Sprintf("the kernel killed a run for memory at %s, so it is taken to need about %s",
				formatMB(oomLimit), formatMB(oomNeed)))
		case memClass == proposed:
			because = append(because, c.memoryClause(req, len(recent), proposed))
		}
		if cpuClass == proposed && cpuNote != "" {
			because = append(because, cpuNote)
		}
		if len(because) == 0 {
			because = append(because, c.memoryClause(req, len(recent), proposed))
		}
		out.Reason = strings.Join(because, "; and ")
		if current != "" {
			out.Reason += fmt.Sprintf(", so it moved up from %s", current)
		}
	default:
		out.Class = current
		if lower, hasLower := current.Smaller(); hasLower {
			since := runsSince(runs, movedAt)
			fits, n, span, most := c.fitsBelow(since, lower)
			switch {
			// The runs since it moved fit the smaller class, and so does what
			// the newest runs say it needs. Without the second half a job whose
			// recent runs were light and whose earlier ones were heavy would move
			// down on the first, and be moved straight back up on the second the
			// next time it finished: two moves, and a job queued in between sent
			// to a host it does not fit.
			case fits && proposed.Rank() <= lower.Rank():
				out.Class, out.FloorMB = lower, most
				out.Reason = fmt.Sprintf("its last %d runs, over %s, all fit a %s runner (the most needed about %s), so it moved down from %s",
					n, longSpan(span), lower, formatMB(most), current)
				return out
			case proposed.Rank() < current.Rank():
				out.Reason = fmt.Sprintf("its runs would fit a %s runner, but moving down takes %d runs over %s since it moved to %s, and there have been %d over %s",
					proposed, DownRuns, longSpan(DownSpan), current, n, longSpan(span))
				return out
			}
		}
		out.Reason = c.memoryClause(req, len(recent), current)
		if cpuClass == current && cpuNote != "" {
			out.Reason += "; and " + cpuNote
		}
	}
	return out
}

// memoryClause is the sentence for a class chosen on memory.
func (c SizeConfig) memoryClause(req Requirement, runs int, class store.SizeClass) string {
	if req.MemoryMB <= 0 {
		return fmt.Sprintf("its last %d runs used almost no memory, which a %s runner holds", runs, class)
	}
	if limit := c.Runner(store.SizeLarge).MemoryMB; req.MemoryMB > limit && class == store.SizeLarge {
		return fmt.Sprintf("its memory needs about %s (the 90th percentile of %d runs, with a fifth added), which is more than a large runner has (%s)",
			formatMB(req.MemoryMB), runs, formatMB(limit))
	}
	return fmt.Sprintf("its memory needs about %s (the 90th percentile of %d runs, with a fifth added), which a %s runner holds",
		formatMB(req.MemoryMB), runs, class)
}

// cpuClass is the class a job's CPU evidence calls for, and the sentence for it:
// the smallest class whose runner has more CPU than the runs that were held
// back had. It is small, and says nothing, unless the job was sampled in enough
// runs, held back in enough of them, and long-running enough for being held
// back to cost it anything.
func (c SizeConfig) cpuClass(runs []store.JobRun) (store.SizeClass, string) {
	sampled, bound := 0, []store.JobRun{}
	for _, r := range runs {
		share, ok := r.Throttled()
		if !ok {
			continue
		}
		sampled++
		if share >= ThrottledShare {
			bound = append(bound, r)
		}
	}
	if sampled < ThrottleRuns || float64(len(bound)) < ThrottleFraction*float64(sampled) {
		return store.SizeSmall, ""
	}
	durations := make([]time.Duration, len(bound))
	cpus := []float64{}
	for i, r := range bound {
		durations[i] = r.Duration
		if r.GrantedCPUs > 0 {
			cpus = append(cpus, r.GrantedCPUs)
		}
	}
	slices.Sort(durations)
	if durations[len(durations)/2] < ThrottleMinDuration {
		return store.SizeSmall, ""
	}
	// The class that gives more CPU than the runs had. Runs that never said
	// what they were given are taken to have had the smallest class's runner,
	// which is what an unmeasured job lands on.
	had := c.Runner(store.SizeSmall).CPUs
	if len(cpus) > 0 {
		slices.Sort(cpus)
		had = cpus[len(cpus)/2]
	}
	class := store.SizeLarge
	for _, k := range store.SizeClasses() {
		if c.Runner(k).CPUs > had+cpuEpsilon {
			class = k
			break
		}
	}
	return class, fmt.Sprintf("%d of its %d sampled runs were held back by their CPU limit (%s CPU) for at least a quarter of their time",
		len(bound), sampled, FormatCPUs(had))
}

// runsSince is the runs that finished after t, newest first.
func runsSince(runs []store.JobRun, t time.Time) []store.JobRun {
	var out []store.JobRun
	for _, r := range runs {
		if r.CompletedAt.After(t) {
			out = append(out, r)
		}
	}
	return out
}

// fitsBelow reports whether the runs are enough evidence to move a job down to
// lower: at least DownRuns of them, spread over at least DownSpan, none killed
// for memory, none CPU-bound, and every one needing no more memory than lower's
// runner has. It also returns how many runs there were, how long they spanned,
// and the most memory any needed, for the sentence that says why not.
func (c SizeConfig) fitsBelow(runs []store.JobRun, lower store.SizeClass) (ok bool, n int, span time.Duration, most int64) {
	n = len(runs)
	if n == 0 {
		return false, 0, 0, 0
	}
	newest, oldest := runs[0].CompletedAt, runs[0].CompletedAt
	for _, r := range runs {
		if r.CompletedAt.After(newest) {
			newest = r.CompletedAt
		}
		if r.CompletedAt.Before(oldest) {
			oldest = r.CompletedAt
		}
	}
	span = newest.Sub(oldest)
	fits := true
	for _, r := range runs {
		need := roundUp(float64(r.PeakMemoryMB)*ProfileMemoryMargin, profileMemoryStep)
		most = max(most, need)
		if r.OOMKilled || need > c.Runner(lower).MemoryMB {
			fits = false
		}
	}
	if class, _ := c.cpuClass(runs); class.Rank() > store.SizeSmall.Rank() {
		fits = false
	}
	return fits && n >= DownRuns && span >= DownSpan, n, span, most
}

// roundUp rounds v up to a multiple of step.
func roundUp(v float64, step int64) int64 {
	return int64(math.Ceil(v/float64(step))) * step
}

// longSpan says a stretch of time the way an operator would in a sentence about
// days of history: "14 days", "6 hours", "40m".
func longSpan(d time.Duration) string {
	switch {
	case d >= 36*time.Hour:
		return plural(int(math.Round(d.Hours()/24)), "day")
	case d >= 90*time.Minute:
		return plural(int(math.Round(d.Hours())), "hour")
	}
	return formatDuration(d.Round(time.Minute))
}
