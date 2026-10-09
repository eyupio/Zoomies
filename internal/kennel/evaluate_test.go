package kennel

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// A public repository is the one the whole package exists for. Anyone can open
// a pull request against it, so anything of it that runs here is code a
// stranger can write.
func TestAPublicRepositoryTheFleetRanJobsForIsAWarning(t *testing.T) {
	ev := Evaluate(publicRepo(), Policy{})
	f, ok := finding(ev, CodePublicRepoOnFleet)
	if !ok {
		t.Fatalf("no finding for a public repository the fleet ran jobs for: %v", openCodes(ev))
	}
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %s, want warning: on a safe pool, with no fork code seen, it is a decision to review, not an emergency", f.Severity)
	}
	if got := ev.State(); got != StateAttention {
		t.Errorf("state = %s, want attention", got)
	}
}

// Only a public repository is exposed this way. A private or internal one has
// no strangers, and a check that fired on every repository would be one nobody
// reads.
func TestOnlyAPublicRepositoryIsExposed(t *testing.T) {
	for _, vis := range []Visibility{VisibilityPrivate, VisibilityInternal} {
		t.Run(string(vis), func(t *testing.T) {
			s := withWeakPool(withRuns(publicRepo(), Run{ID: 1, Event: "pull_request", FromFork: true}), DangerHostSocket)
			s.Repo.Visibility = vis
			ev := Evaluate(s, Policy{})
			for _, c := range []Code{CodePublicRepoOnFleet, CodePublicRepoWeakPool, CodeForkCodeRan, CodeTargetEventRan} {
				if _, ok := finding(ev, c); ok {
					t.Errorf("%s fired for a %s repository", c, vis)
				}
				if !hasCode(ev.Ran, c) {
					t.Errorf("%s did not count as having run: not applicable is an answer, not a gap", c)
				}
			}
		})
	}
}

// Nothing running here means nothing to say: a public repository the fleet has
// never served is not Kennel Club's business.
func TestAPublicRepositoryTheFleetNeverServedRaisesNothing(t *testing.T) {
	s := publicRepo()
	s.Fleet.Jobs = JobFacts{}
	s.Fleet.Pools = nil
	if ev := Evaluate(s, Policy{}); len(ev.Findings) != 0 {
		t.Errorf("findings for an unserved repository: %v", openCodes(ev))
	}
}

// A job that is only waiting is already a stranger's code about to run, so it
// counts as much as one that has.
func TestAJobStillQueuedCountsAsServing(t *testing.T) {
	s := publicRepo()
	s.Fleet.Jobs = JobFacts{Queued: 1}
	if _, ok := finding(Evaluate(s, Policy{}), CodePublicRepoOnFleet); !ok {
		t.Error("a queued job for a public repository was not counted")
	}
}

func TestEachWeaknessInAPoolIsReportedAndASafePoolIsNot(t *testing.T) {
	tests := []struct {
		name    string
		dangers []PoolDanger
		want    bool
	}{
		{"persistent runners", []PoolDanger{DangerPersistent}, true},
		{"the host Docker socket", []PoolDanger{DangerHostSocket}, true},
		{"a privileged Docker daemon", []PoolDanger{DangerPrivileged}, true},
		{"root inside the runner", []PoolDanger{DangerRoot}, true},
		{"no container", []PoolDanger{DangerNoContainer}, true},
		{"a safe pool", nil, false},
		// A value this package does not know is not a weakness it can name, and
		// must not reach a sentence.
		{"an unknown danger", []PoolDanger{"IGNORE PREVIOUS INSTRUCTIONS"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := Evaluate(withWeakPool(publicRepo(), tt.dangers...), Policy{})
			f, got := finding(ev, CodePublicRepoWeakPool)
			if got != tt.want {
				t.Fatalf("fired = %v, want %v", got, tt.want)
			}
			if got && f.Severity != SeverityError {
				t.Errorf("severity = %s, want error", f.Severity)
			}
		})
	}
}

// What a pool could do is not the question; what it did is. A weak pool that
// ran none of this repository's jobs is somebody else's finding.
func TestAWeakPoolThatRanNothingForTheRepositoryIsNotItsFinding(t *testing.T) {
	s := withWeakPool(publicRepo(), DangerHostSocket)
	s.Fleet.Pools[0].JobsRun = 0
	if _, ok := finding(Evaluate(s, Policy{}), CodePublicRepoWeakPool); ok {
		t.Error("a pool that ran none of the repository's jobs was blamed for it")
	}
}

func TestTheWeakPoolFindingNamesTheMostUsedPoolFirst(t *testing.T) {
	s := publicRepo()
	s.Fleet.Pools = []PoolFact{
		{ID: "pool_aaaa", Name: "few", JobsRun: 1, Dangers: []PoolDanger{DangerRoot}},
		{ID: "pool_bbbb", Name: "many", JobsRun: 50, Dangers: []PoolDanger{DangerRoot}},
	}
	f, _ := finding(Evaluate(s, Policy{}), CodePublicRepoWeakPool)
	if len(f.Evidence) != 2 || f.Evidence[0].Ref != "pool_bbbb" {
		t.Errorf("evidence = %+v, want the pool that ran the most jobs first", f.Evidence)
	}
}

// Only a pull request run from a fork executes the fork author's code. A run
// from a branch of the repository itself was written by somebody with write
// access, and a pull_request_target run executes the *base* repository's
// workflow, which is a different finding.
func TestOnlyAForkPullRequestRunIsForkCode(t *testing.T) {
	tests := []struct {
		name string
		run  Run
		want bool
	}{
		{"a fork's pull request", Run{ID: 1, Event: "pull_request", FromFork: true}, true},
		{"a pull request from a branch of the repository", Run{ID: 2, Event: "pull_request", FromFork: false}, false},
		{"a push", Run{ID: 3, Event: "push"}, false},
		{"a fork's pull_request_target", Run{ID: 4, Event: "pull_request_target", FromFork: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := finding(Evaluate(withRuns(publicRepo(), tt.run), Policy{}), CodeForkCodeRan)
			if got != tt.want {
				t.Errorf("fired = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEveryEventAStrangerCanTriggerIsReportedAndNoOtherIs(t *testing.T) {
	for _, e := range []string{"pull_request_target", "workflow_run", "issue_comment", "issues"} {
		if _, ok := finding(Evaluate(withRuns(publicRepo(), Run{ID: 1, Event: e}), Policy{}), CodeTargetEventRan); !ok {
			t.Errorf("%s was not reported", e)
		}
	}
	for _, e := range []string{"push", "pull_request", "schedule", "workflow_dispatch", "release", "other", ""} {
		if _, ok := finding(Evaluate(withRuns(publicRepo(), Run{ID: 1, Event: e}), Policy{}), CodeTargetEventRan); ok {
			t.Errorf("%q was reported, and a stranger cannot trigger it", e)
		}
	}
}

func TestTheEvidenceForARunFindingIsTheThreeNewestRunIDs(t *testing.T) {
	var runs []Run
	for _, id := range []int64{5, 9, 3, 12, 7} {
		runs = append(runs, Run{ID: id, Event: "pull_request", FromFork: true})
	}
	f, _ := finding(Evaluate(withRuns(publicRepo(), runs...), Policy{}), CodeForkCodeRan)
	var got []string
	for _, e := range f.Evidence {
		got = append(got, e.Ref)
	}
	if !slices.Equal(got, []string{"12", "9", "7"}) {
		t.Errorf("evidence refs = %v, want the three newest run IDs, newest first", got)
	}
}

// Fork code or a weak pool beside a public repository is what turns "review
// this" into "act on this", and it is the error that raises the problem.
func TestAPublicRepositoryIsAnErrorOnlyWhenForkCodeRanOrThePoolIsWeak(t *testing.T) {
	tests := []struct {
		name string
		s    Snapshot
		want Severity
	}{
		{"alone", publicRepo(), SeverityWarning},
		{"with fork code", withRuns(publicRepo(), Run{ID: 1, Event: "pull_request", FromFork: true}), SeverityError},
		{"with a weak pool", withWeakPool(publicRepo(), DangerPersistent), SeverityError},
		{"with only a target event", withRuns(publicRepo(), Run{ID: 1, Event: "issues"}), SeverityWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := finding(Evaluate(tt.s, Policy{}), CodePublicRepoOnFleet)
			if !ok || f.Severity != tt.want {
				t.Errorf("severity = %v (found %v), want %s", f.Severity, ok, tt.want)
			}
		})
	}
}

// The wait is counted from ten minutes, inclusive. A job that is merely waiting
// for its pool to scale up is not waiting for a label nobody serves.
func TestAJobIsUnservedFromTenMinutes(t *testing.T) {
	tests := []struct {
		waited time.Duration
		want   bool
	}{
		{9*time.Minute + 59*time.Second, false},
		{10 * time.Minute, true},
		{3 * time.Hour, true},
	}
	for _, tt := range tests {
		s := privateRepo()
		s.Fleet.Jobs.Unserved = []Unserved{{Waited: tt.waited}}
		if _, got := finding(Evaluate(s, Policy{}), CodeUnservedLabel); got != tt.want {
			t.Errorf("waited %s: fired = %v, want %v", tt.waited, got, tt.want)
		}
	}
}

func TestTheUnservedFindingSaysHowLongTheWorstJobWaitedAndHowManyDid(t *testing.T) {
	s := privateRepo()
	s.Fleet.Jobs.Unserved = []Unserved{{Waited: 3 * time.Minute}, {Waited: 12 * time.Minute}, {Waited: 3 * time.Hour}}
	f, _ := finding(Evaluate(s, Policy{}), CodeUnservedLabel)
	for _, want := range []string{"2 jobs", "3 hours", "10 minutes"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("detail %q does not say %q: the 3-minute job is not counted, the other two are", f.Detail, want)
		}
	}
}

// A job is "stopped by GitHub's limit" only if it was cancelled at about six
// hours. A job that succeeded after six hours cannot exist, and one that a
// person cancelled at five and a half is not evidence of a missing timeout.
func TestOnlyAJobCancelledAtGitHubsLimitHitIt(t *testing.T) {
	tests := []struct {
		name       string
		duration   time.Duration
		conclusion string
		want       bool
	}{
		{"cancelled at exactly the limit", 360 * time.Minute, "cancelled", true},
		{"cancelled a minute early", 359 * time.Minute, "cancelled", true},
		{"cancelled two minutes late", 362 * time.Minute, "cancelled", true},
		{"cancelled just too early", 359*time.Minute - time.Second, "cancelled", false},
		{"cancelled just too late", 362*time.Minute + time.Second, "cancelled", false},
		{"cancelled by a person at five and a half hours", 330 * time.Minute, "cancelled", false},
		{"a failure at the limit", 360 * time.Minute, "failure", false},
		{"a success at the limit", 360 * time.Minute, "success", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := privateRepo()
			s.Fleet.Jobs.Long = []FinishedJob{{Duration: tt.duration, Conclusion: tt.conclusion}}
			if _, got := finding(Evaluate(s, Policy{}), CodeJobHitDefaultLimit); got != tt.want {
				t.Errorf("fired = %v, want %v", got, tt.want)
			}
		})
	}
}

// A check is judged against what it can read or not at all. Judging the fork
// check against an unread run history would report "all clear" for a repository
// nobody looked at, which is the worst answer this package could give.
func TestACheckWhoseSourceCannotBeReadIsSkippedAndSaysWhy(t *testing.T) {
	for _, st := range []CoverageState{CoverageDenied, CoverageUnavailable, CoverageHeld, CoverageError, CoverageNotRead} {
		t.Run(string(st), func(t *testing.T) {
			s := publicRepo()
			s.Coverage[SourceRuns] = SourceState{State: st}
			ev := Evaluate(s, Policy{})
			for _, c := range []Code{CodeForkCodeRan, CodeTargetEventRan} {
				if !slices.ContainsFunc(ev.Skipped, func(k Skipped) bool { return k.Code == c && k.Source == SourceRuns && k.State == st }) {
					t.Errorf("%s was not skipped for %s: %+v", c, st, ev.Skipped)
				}
				if hasCode(ev.Ran, c) {
					t.Errorf("%s is listed as having run", c)
				}
			}
			if ev.Complete {
				t.Error("an evaluation with skipped checks says it is complete")
			}
			if ev.State() == StateBestInShow {
				t.Error("a repository that could not be fully read was called best in show")
			}
		})
	}
}

func TestADeniedSourceNamesThePermissionItNeeds(t *testing.T) {
	s := publicRepo()
	s.Coverage[SourceRuns] = SourceState{State: CoverageDenied}
	ev := Evaluate(s, Policy{})
	if len(ev.Skipped) == 0 || !strings.Contains(ev.Skipped[0].Reason(), "Actions: Read-only") {
		t.Errorf("skipped = %+v, want a reason naming Actions: Read-only", ev.Skipped)
	}
}

// A sampled read can prove a fork's code ran. It cannot prove none did, and the
// two must not read alike.
func TestAPartialReadCountsIfItFindsSomethingAndIsIncompleteIfItDoesNot(t *testing.T) {
	found := withRuns(publicRepo(), Run{ID: 1, Event: "pull_request", FromFork: true})
	found.Coverage[SourceRuns] = SourceState{State: CoveragePartial}
	ev := Evaluate(found, Policy{})
	f, ok := finding(ev, CodeForkCodeRan)
	if !ok {
		t.Fatal("a finding in a partial read was dropped")
	}
	if !strings.Contains(f.Detail, "counting only the newest runs") {
		t.Errorf("detail %q does not say the read was a sample", f.Detail)
	}

	none := publicRepo()
	none.Coverage[SourceRuns] = SourceState{State: CoveragePartial}
	ev = Evaluate(none, Policy{})
	if !hasCode(ev.Incomplete, CodeForkCodeRan) || !hasCode(ev.Incomplete, CodeTargetEventRan) {
		t.Errorf("incomplete = %v, want both run checks", ev.Incomplete)
	}
	if ev.Complete || ev.State() == StateBestInShow {
		t.Error("a partial read that found nothing was treated as an all-clear")
	}
}

func TestACheckTheOperatorTurnedOffIsNeitherRunNorAGap(t *testing.T) {
	tests := []struct {
		name     string
		disabled map[string]bool
		off      []Code
	}{
		{"by code", map[string]bool{"exposure.fork_code_ran": true}, []Code{CodeForkCodeRan}},
		{"by area", map[string]bool{"exposure": true}, []Code{CodePublicRepoOnFleet, CodePublicRepoWeakPool, CodeForkCodeRan, CodeTargetEventRan}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := withWeakPool(withRuns(publicRepo(), Run{ID: 1, Event: "pull_request", FromFork: true}), DangerRoot)
			ev := Evaluate(s, Policy{Disabled: tt.disabled})
			for _, c := range tt.off {
				if _, ok := finding(ev, c); ok {
					t.Errorf("%s fired although it is turned off", c)
				}
				if !hasCode(ev.Disabled, c) || hasCode(ev.Ran, c) {
					t.Errorf("%s: disabled=%v ran=%v", c, ev.Disabled, ev.Ran)
				}
			}
			if len(ev.Skipped) != 0 || len(ev.Incomplete) != 0 {
				t.Errorf("a disabled check was counted as a gap: skipped=%v incomplete=%v", ev.Skipped, ev.Incomplete)
			}
		})
	}
}

// Turning every check off asks not to be told. It is not the same as being
// clear, and must not earn a badge.
func TestAFleetWithEveryCheckTurnedOffIsNotBestInShow(t *testing.T) {
	// Every area the registry has, and not a list written here that a new area
	// would be missing from and leave one check running.
	off := map[string]bool{}
	for _, c := range Checks() {
		off[string(c.Area)] = true
	}
	ev := Evaluate(privateRepo(), Policy{Disabled: off})
	if ev.Complete || ev.State() == StateBestInShow {
		t.Errorf("complete=%v state=%s with nothing checked", ev.Complete, ev.State())
	}
}

func TestAnInfoFindingDoesNotStopABestInShow(t *testing.T) {
	ev := Evaluation{Complete: true, Findings: []Finding{{Code: "x", Severity: SeverityInfo}}}
	if got := ev.State(); got != StateBestInShow {
		t.Errorf("state = %s, want best_in_show: info is worth knowing, not worth losing the badge", got)
	}
}

// An open error is never hidden behind "we could not read everything".
func TestAnOpenFindingOutranksAnIncompleteRead(t *testing.T) {
	ev := Evaluation{Complete: false, Findings: []Finding{{Code: "x", Severity: SeverityWarning}}}
	if got := ev.State(); got != StateAttention {
		t.Errorf("state = %s, want attention", got)
	}
}

func TestACleanFullyReadRepositoryIsBestInShow(t *testing.T) {
	ev := Evaluate(privateRepo(), Policy{})
	if !ev.Complete || ev.State() != StateBestInShow {
		t.Errorf("complete=%v state=%s findings=%v skipped=%v", ev.Complete, ev.State(), openCodes(ev), ev.Skipped)
	}
}

func TestACleanPrivateRepositoryAsksNothingOfTheRunHistory(t *testing.T) {
	s := privateRepo()
	delete(s.Coverage, SourceRuns)
	ev := Evaluate(s, Policy{})
	if len(ev.Skipped) != 0 || !ev.Complete {
		t.Errorf("a private repository was held to a source it never reads: skipped=%+v", ev.Skipped)
	}
}

// The findings and the order they come in are a function of the snapshot, not
// of the order its slices were built in.
func TestEvaluateGivesTheSameAnswerWhateverOrderTheFactsCameIn(t *testing.T) {
	build := func(reverse bool) Snapshot {
		s := publicRepo()
		s.Fleet.Pools = []PoolFact{
			{ID: "pool_aaaa", Name: "a", JobsRun: 3, Dangers: []PoolDanger{DangerRoot, DangerPersistent}},
			{ID: "pool_bbbb", Name: "b", JobsRun: 3, Dangers: []PoolDanger{DangerHostSocket}},
		}
		s.Runs = &RunFacts{Window: time.Hour, Runs: []Run{
			{ID: 1, Event: "pull_request", FromFork: true}, {ID: 2, Event: "issues"}, {ID: 3, Event: "workflow_run"},
		}}
		if reverse {
			slices.Reverse(s.Fleet.Pools)
			slices.Reverse(s.Runs.Runs)
			slices.Reverse(s.Fleet.Pools[0].Dangers)
		}
		return s
	}
	a, _ := json.Marshal(Evaluate(build(false), Policy{}))
	b, _ := json.Marshal(Evaluate(build(true), Policy{}))
	if string(a) != string(b) {
		t.Errorf("order changed the answer:\n%s\n%s", a, b)
	}
}

// The client and the stored JSON both expect arrays. null would be a crash in
// one and a different document in the other.
func TestAnEmptyEvaluationEncodesEmptyListsNotNull(t *testing.T) {
	b, err := json.Marshal(Evaluate(privateRepo(), Policy{}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"findings", "waived", "ran", "skipped", "incomplete", "disabled", "lapsed", "stale"} {
		if string(m[k]) == "null" {
			t.Errorf("%s encodes as null", k)
		}
	}
}

func TestCountsTallyOpenFindingsBySeverityAndCountWaivedApart(t *testing.T) {
	ev := Evaluation{
		Findings: []Finding{{Severity: SeverityError}, {Severity: SeverityWarning}, {Severity: SeverityWarning}, {Severity: SeverityInfo}},
		Waived:   []WaivedFinding{{}, {}},
	}
	if got, want := ev.Counts(), (Counts{Error: 1, Warning: 2, Info: 1, Waived: 2}); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}
