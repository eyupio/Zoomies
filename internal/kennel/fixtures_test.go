package kennel

import (
	"slices"
	"time"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func okCoverage() Coverage {
	return Coverage{
		SourceFleet:     {State: CoverageOK},
		SourceMetadata:  {State: CoverageOK},
		SourceRuns:      {State: CoverageOK},
		SourceSetup:     {State: CoverageOK},
		SourceWorkflows: {State: CoverageOK},
		SourceGuidance:  {State: CoverageOK},
	}
}

// publicRepo is a public repository the fleet has run five jobs for on a safe
// pool, with every source read and nothing wrong. Each test starts here and
// changes the one thing it is about.
func publicRepo() Snapshot {
	return Snapshot{
		At:   now,
		Repo: Repo{Visibility: VisibilityPublic},
		Fleet: Fleet{
			Window: 30 * 24 * time.Hour,
			Jobs:   JobFacts{Ran: 5},
			Pools:  []PoolFact{{ID: "pool_a1", Name: "zoomies-ubuntu-2404", JobsRun: 5}},
		},
		Runs:      &RunFacts{Window: 14 * 24 * time.Hour},
		Coverage:  okCoverage(),
		Setup:     completeSetup(),
		Workflows: &WorkflowFacts{},
		Guidance:  &GuidanceFacts{},
	}
}

// privateRepo is the same repository, private.
func privateRepo() Snapshot {
	s := publicRepo()
	s.Repo.Visibility = VisibilityPrivate
	s.Runs = nil
	return s
}

func withRuns(s Snapshot, runs ...Run) Snapshot {
	s.Runs = &RunFacts{Window: 14 * 24 * time.Hour, Runs: runs}
	return s
}

func withWeakPool(s Snapshot, dangers ...PoolDanger) Snapshot {
	s.Fleet.Pools = []PoolFact{{ID: "pool_b2", Name: "zoomies-persistent", JobsRun: 5, Dangers: dangers}}
	return s
}

func finding(ev Evaluation, c Code) (Finding, bool) {
	for _, f := range ev.Findings {
		if f.Code == c {
			return f, true
		}
	}
	return Finding{}, false
}

func openCodes(ev Evaluation) []Code {
	var out []Code
	for _, f := range ev.Findings {
		out = append(out, f.Code)
	}
	return out
}

func hasCode(codes []Code, c Code) bool { return slices.Contains(codes, c) }

// positives is one snapshot per check that makes that check fire, and
// TestEveryCheckCanFire holds it to covering the whole registry. A check no
// test can make fire is a check nobody has seen work.
var positives = map[Code]func() Snapshot{
	CodePublicRepoOnFleet: publicRepo,
	CodePublicRepoWeakPool: func() Snapshot {
		return withWeakPool(publicRepo(), DangerHostSocket, DangerRoot)
	},
	CodeForkCodeRan: func() Snapshot {
		return withRuns(publicRepo(), Run{ID: 101, Event: "pull_request", FromFork: true})
	},
	CodeTargetEventRan: func() Snapshot {
		return withRuns(publicRepo(), Run{ID: 102, Event: "pull_request_target", FromFork: true})
	},
	CodeUnservedLabel: func() Snapshot {
		s := privateRepo()
		s.Fleet.Jobs.Unserved = []Unserved{{Waited: 25 * time.Minute}}
		return s
	},
	CodeJobHitDefaultLimit: func() Snapshot {
		s := privateRepo()
		s.Fleet.Jobs.Long = []FinishedJob{{Duration: 360*time.Minute + 20*time.Second, Conclusion: "cancelled"}}
		return s
	},
	CodeMatrixExceedsPool: func() Snapshot {
		s := privateRepo()
		s.Fleet.Pools = []PoolFact{{ID: "pool_a1", Name: "zoomies-ubuntu-2404", JobsRun: 6, MaxRunners: 2}}
		s.Fleet.Jobs.Matrices = []Matrix{{PoolID: "pool_a1", Jobs: 6, Waited: 3 * time.Minute}}
		return s
	},
}
