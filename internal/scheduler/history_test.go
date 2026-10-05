package scheduler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestProfileIsTheNinetiethPercentileOfRecentPeaksWithAMarginOnMemory(t *testing.T) {
	ten := func(mb int64) []store.JobPeak {
		var out []store.JobPeak
		for i := range 10 {
			out = append(out, store.JobPeak{CPUs: float64(i + 1), MemoryMB: mb + int64(i)*100})
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		peaks []store.JobPeak
		want  Requirement
	}{
		{"no runs say nothing", nil, Requirement{}},
		{
			// One run is its own percentile. 2000 MB with a fifth more is
			// 2400, rounded up to the next 64 MB.
			"one run", []store.JobPeak{{CPUs: 1.5, MemoryMB: 2000}},
			Requirement{CPUs: 1.5, MemoryMB: 2432, Samples: 1},
		},
		{
			// Of ten runs the ninth is the ninetieth percentile: the one
			// heaviest run does not size every run after it. 1800 MB with a
			// fifth more is 2160, rounded up to 2176.
			"ten runs", ten(1000),
			Requirement{CPUs: 9, MemoryMB: 2176, Samples: 10},
		},
		{
			// A killed run's peak is the limit it hit, so it counts as having
			// needed half as much again: 3000 * 1.5 * 1.2 = 5400, to 5440.
			"a run killed for memory", []store.JobPeak{{CPUs: 2, MemoryMB: 3000, OOMKilled: true}},
			Requirement{CPUs: 2, MemoryMB: 5440, Samples: 1, OOMKilled: 1},
		},
		{
			// CPU gets no margin: a build uses what it is given, and a margin
			// would ratchet it up on every run.
			"cpu only", []store.JobPeak{{CPUs: 4}},
			Requirement{CPUs: 4, Samples: 1},
		},
		{
			"a kill with no sample is counted and constrains nothing", []store.JobPeak{{OOMKilled: true}},
			Requirement{Samples: 1, OOMKilled: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Profile(tc.peaks); got != tc.want {
				t.Fatalf("Profile = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// historyFleet is the shape of the failure this exists for: an automatic pool
// with a 1 CPU / 1 GB minimum, a small host that the plain policy picks for
// the next runner, and a larger one with room left. The small host sorts
// first so a tie in headroom goes to it, as it did on the day.
func historyFleet(t *testing.T, mode string, peakMB int64) (Snapshot, *store.Pool) {
	t.Helper()
	p := testPool("linux", "self-hosted")
	p.Resources = store.Resources{MinCPUs: 1, MinMemoryMB: 1024}
	small := sized("a-small", 1, 4, hostFor(3072), 100000)
	big := sized("b-big", 2, 16, hostFor(16384), 100000)
	busy := testRunner("r_busy", p, store.RunnerBusy, 0)
	busy.HostID = "b-big"
	big.ActiveRunners = 1
	job := queued("go-controller", 0, "self-hosted")
	job.Workflow = "CI"
	s := snap([]*store.Pool{p}, []*store.Runner{busy}, []*store.Job{job}, []*store.Host{small, big})
	s.HistorySizing = mode
	if peakMB > 0 {
		key := store.JobUsageKey{Repo: job.Repo, Workflow: job.Workflow, JobName: job.JobName, PoolID: p.ID}
		s.JobHistory = map[store.JobUsageKey][]store.JobPeak{key: {{CPUs: 2, MemoryMB: peakMB}, {CPUs: 2, MemoryMB: peakMB}}}
	}
	return s, p
}

func TestHistorySizingPlacesAHeavyJobAwayFromTheSmallHost(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		peakMB      int64
		wantHost    string
		wantHistory string
	}{
		// Without history, and with history off, the fleet places exactly as
		// it always did -- which is on the small host.
		{"no history places as before", HistoryOn, 0, "a-small", ""},
		{"history off places as before", HistoryOff, 5000, "a-small", ""},
		// Shadow works the answer out and changes nothing.
		{"shadow records and changes nothing", HistoryShadow, 5000, "a-small",
			"shadow: history sizing would have held off a-small: jobs waiting need ~5.9 GB, it has 3 GB"},
		{"on holds the small host off", HistoryOn, 5000, "b-big",
			"placed for the jobs waiting, which need ~5.9 GB and ~2 CPU; held off a-small: jobs waiting need ~5.9 GB, it has 3 GB"},
		// A light job fits both, so nothing is held off.
		{"a light job goes where it always went", HistoryOn, 500, "a-small", "placed for the jobs waiting, which need ~1 GB and ~2 CPU"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := historyFleet(t, tc.mode, tc.peakMB)
			pp := only(t, Decide(s))
			creates := actionsOf(pp.Actions, ActionCreate)
			if len(creates) != 1 {
				t.Fatalf("creates = %d (%s), want 1", len(creates), pp.Reason)
			}
			if creates[0].HostID != tc.wantHost {
				t.Fatalf("HostID = %q, want %q (%s)", creates[0].HostID, tc.wantHost, pp.Reason)
			}
			if pp.History != tc.wantHistory {
				t.Fatalf("History = %q, want %q", pp.History, tc.wantHistory)
			}
			if tc.mode == HistoryOn && tc.wantHistory != "" && !strings.Contains(creates[0].Reason, tc.wantHistory) {
				t.Fatalf("the create's reason %q does not say what history did", creates[0].Reason)
			}
		})
	}
}

// Shadow is meant to be safe to leave on: the plan it produces has to be the
// plan off produces, action for action, or it is not a shadow.
func TestShadowHistorySizingChangesNoAction(t *testing.T) {
	off, _ := historyFleet(t, HistoryOff, 5000)
	shadow, _ := historyFleet(t, HistoryShadow, 5000)
	if a, b := Decide(off).Actions, Decide(shadow).Actions; !reflect.DeepEqual(a, b) {
		t.Fatalf("shadow changed the plan:\noff    %+v\nshadow %+v", a, b)
	}
}

// A host whose runner share is smaller than the job's known need is given a
// runner sized to it, where the pool leaves its size to the host. Without it
// the heavy job is placed on a big host and still killed at a slot's share.
func TestHistorySizingRaisesAnAutomaticRunnerToTheKnownNeed(t *testing.T) {
	p := testPool("linux", "self-hosted")
	h := sized("big", 4, 16, hostFor(16384), 100000)
	job := queued("go-controller", 0, "self-hosted")
	s := snap([]*store.Pool{p}, nil, []*store.Job{job}, []*store.Host{h})
	s.HistorySizing = HistoryOn
	s.JobHistory = map[store.JobUsageKey][]store.JobPeak{
		{Repo: job.Repo, JobName: job.JobName, PoolID: p.ID}: {{CPUs: 3, MemoryMB: 5000}},
	}
	pp := only(t, Decide(s))
	creates := actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 || creates[0].Size == nil || !creates[0].SizedFromHistory {
		t.Fatalf("creates = %+v, want one sized from history", creates)
	}
	if got, share := creates[0].Size, Reserve(p, h); got.MemoryMB != 6016 || got.CPUs != share.CPUs {
		// A quarter of the host's CPU is already above the 3 CPU need, so it
		// stays the share; memory's share is 4 GB, below the 6016 MB need,
		// so memory alone is raised.
		t.Fatalf("Size = %+v, want 6016 MB and the host's %s CPU share", got, formatCPUs(share.CPUs))
	}
	if !strings.Contains(creates[0].Reason, "sized to") {
		t.Fatalf("reason %q does not say the runner was sized up", creates[0].Reason)
	}

	// A pool that states its size is never given more than it: the need then
	// chooses the host and nothing else.
	fixed := limited("fixed", 2, 2048)
	fixed.Labels = p.Labels
	s = snap([]*store.Pool{fixed}, nil, []*store.Job{job}, []*store.Host{h})
	s.HistorySizing = HistoryOn
	s.JobHistory = map[store.JobUsageKey][]store.JobPeak{
		{Repo: job.Repo, JobName: job.JobName, PoolID: fixed.ID}: {{CPUs: 3, MemoryMB: 5000}},
	}
	pp = only(t, Decide(s))
	creates = actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 || creates[0].Size != nil {
		t.Fatalf("creates = %+v, want one at the pool's own size", creates)
	}
}

// Every host that could take the runner is short of what the job needs, and
// a larger one will have room when its work finishes: that is a wait, said in
// the words the operator reads, not a create onto the host that will kill it.
func TestHistorySizingHoldsOffEveryHostTooSmallForNow(t *testing.T) {
	s, _ := historyFleet(t, HistoryOn, 8400) // ~9.9 GB; b-big has 8 GB left, 16 GB in all
	pp := only(t, Decide(s))
	if n := len(actionsOf(pp.Actions, ActionCreate)); n != 0 {
		t.Fatalf("creates = %d, want none", n)
	}
	want := "held off a-small: jobs waiting need ~9.9 GB, it has 3 GB; held off b-big: jobs waiting need ~9.9 GB, it has 8 GB"
	if pp.Blocked != want {
		t.Fatalf("Blocked = %q, want %q", pp.Blocked, want)
	}
	if !pp.BlockedAtCapacity || pp.HistoryUnfit != "" {
		t.Fatalf("BlockedAtCapacity = %t, HistoryUnfit = %q: a wait, not a misfit", pp.BlockedAtCapacity, pp.HistoryUnfit)
	}
	if !strings.Contains(pp.Reason, want) {
		t.Fatalf("Reason %q does not carry why", pp.Reason)
	}
}

// A job no host could ever hold is not allowed to hold every other job back:
// it is left out of the requirement and reported, because only a larger host
// helps it.
func TestAJobNoHostCanEverFitIsReportedAndDoesNotBlockThePool(t *testing.T) {
	s, _ := historyFleet(t, HistoryOn, 64*1024)
	pp := only(t, Decide(s))
	creates := actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 || creates[0].HostID != "a-small" {
		t.Fatalf("creates = %+v, want one placed as before", creates)
	}
	if !strings.Contains(pp.HistoryUnfit, "no host that can run the pool has that much to give") {
		t.Fatalf("HistoryUnfit = %q", pp.HistoryUnfit)
	}
}

// A run killed by a spike between two 30-second samples can read well under the
// limit it hit, and the limit is the one figure known to have been too little. Of
// twelve runs the single kill sits in the ignored tail of the percentile, so
// placement must size from the kill, or the next run is sized below the size that
// already failed it.
func TestAKilledRunIsPlacedFromTheLimitItHitNotTheSampleThatMissedTheSpike(t *testing.T) {
	var peaks []store.JobPeak
	for range 11 {
		peaks = append(peaks, store.JobPeak{MemoryMB: 2000})
	}
	peaks = append(peaks, store.JobPeak{MemoryMB: 2000, OOMKilled: true, GrantedMemoryMB: 4096})
	// The profile alone is the 90th percentile and misses it, as the size classes
	// expect: they count kills themselves.
	if got := Profile(peaks); got.MemoryMB >= 4096 {
		t.Fatalf("the profile asks for %d MB; the percentile is meant to ignore the tail", got.MemoryMB)
	}
	// 4096 * 1.5 * 1.2 = 7372.8, rounded up to the next 64 MB.
	if got := killedNeed(peaks); got != 7424 {
		t.Errorf("a run killed at 4096 MB needed %d MB, want 7424", got)
	}
	// A kill with no granted size on its row is sized from its peak.
	if got := killedNeed([]store.JobPeak{{MemoryMB: 2000, OOMKilled: true}}); got != 3648 {
		t.Errorf("a kill with no granted size needed %d MB, want 3648", got)
	}
	if got := killedNeed([]store.JobPeak{{MemoryMB: 9000}}); got != 0 {
		t.Errorf("a run that was not killed implies a need of %d MB", got)
	}
}

// Placement reads the same history, and a job killed once in twelve runs is held
// off a host that has the memory it was killed at: with the kill in the ignored
// tail of the percentile the small host would otherwise take it again.
func TestHistorySizingHoldsAJobOffTheHostItWasKilledOn(t *testing.T) {
	s, p := historyFleet(t, HistoryOn, 0)
	job := s.Jobs[0]
	key := store.JobUsageKey{Repo: job.Repo, Workflow: job.Workflow, JobName: job.JobName, PoolID: p.ID}
	var peaks []store.JobPeak
	for range 11 {
		peaks = append(peaks, store.JobPeak{CPUs: 2, MemoryMB: 500})
	}
	// Killed at a 2 GB limit the sample put at 500 MB: the small host's 3 GB cannot
	// hold 2 GB * 1.5 * 1.2.
	peaks = append(peaks, store.JobPeak{CPUs: 2, MemoryMB: 500, OOMKilled: true, GrantedMemoryMB: 2048})
	s.JobHistory = map[store.JobUsageKey][]store.JobPeak{key: peaks}
	pp := only(t, Decide(s))
	creates := actionsOf(pp.Actions, ActionCreate)
	if len(creates) != 1 || creates[0].HostID != "b-big" {
		t.Fatalf("creates = %+v (%s); want the job placed on the big host, away from the one that killed it", creates, pp.Reason)
	}
}
