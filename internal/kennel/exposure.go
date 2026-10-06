package kennel

import (
	"slices"
	"strings"
)

// strangerEvents are the triggers whose workflow runs from the default branch
// whenever someone outside the project acts: a pull request opened against it,
// a comment, an issue. They are safe until the workflow handles what the
// stranger sent, which is a question for the workflow's file, so on its own
// this is a warning that never raises a problem.
var strangerEvents = []string{"pull_request_target", "workflow_run", "issue_comment", "issues"}

// weakWords are what each pool danger is called in a sentence. A danger not in
// this list is ignored, so a value a snapshot should not hold cannot reach a
// sentence.
var weakWords = []struct {
	danger PoolDanger
	words  string
}{
	{DangerPersistent, "persistent runners"},
	{DangerHostSocket, "the host Docker socket"},
	{DangerPrivileged, "a privileged Docker daemon"},
	{DangerRoot, "root inside the runner"},
	{DangerNoContainer, "no container at all"},
}

func isPublic(s *Snapshot) bool { return s.Repo.Visibility == VisibilityPublic }

func evalPublicRepoOnFleet(s *Snapshot) result {
	if !isPublic(s) {
		return result{}
	}
	n := s.Fleet.Jobs.Ran + s.Fleet.Jobs.Queued
	if n == 0 {
		return result{applies: true}
	}
	detail := "Anyone who can open a pull request against a public repository can ask GitHub to run code on a self-hosted runner. " +
		"In the last " + forDays(s.Fleet.Window) + ", this fleet ran or queued " + count(n, "job", "jobs") + " for it."
	if s.Fleet.RunnerGroupAllowsPublic == TriYes {
		detail += " The runner group these runners join is set to allow public repositories."
	}
	return result{applies: true, findings: []Finding{{
		Code:     CodePublicRepoOnFleet,
		Severity: SeverityWarning,
		Title:    "A public repository is running jobs on this fleet",
		Detail:   detail,
		Fix: "Move this repository's jobs to GitHub-hosted runners, or serve it only from a pool that is ephemeral, " +
			"has no Docker socket and runs on hosts you are willing to lose. " +
			"If you have decided this is acceptable, waive this finding and say why.",
	}}}
}

func evalPublicRepoWeakPool(s *Snapshot) result {
	if !isPublic(s) {
		return result{}
	}
	var weak []PoolFact
	for _, p := range s.Fleet.Pools {
		if p.JobsRun > 0 && len(knownDangers(p.Dangers)) > 0 {
			weak = append(weak, p)
		}
	}
	if len(weak) == 0 {
		return result{applies: true}
	}
	// The most-used pool first, so the evidence names the one that matters most.
	slices.SortStableFunc(weak, func(a, b PoolFact) int {
		if a.JobsRun != b.JobsRun {
			return b.JobsRun - a.JobsRun
		}
		return strings.Compare(a.ID, b.ID)
	})
	var words []string
	for _, w := range weakWords {
		for _, p := range weak {
			if slices.Contains(p.Dangers, w.danger) {
				words = append(words, w.words)
				break
			}
		}
	}
	f := Finding{
		Code:     CodePublicRepoWeakPool,
		Severity: SeverityError,
		Title:    "A public repository's jobs ran on a pool with weak isolation",
		Detail: count(len(weak), "pool that ran", "pools that ran") + " this repository's jobs " +
			isOrAre(len(weak)) + " set up with less isolation than the default: " + strings.Join(words, ", ") + ". " +
			"A pull request from a stranger can reach whatever those settings expose.",
		Fix: "Change the pool, or stop serving this repository from it. The pool's page says what each setting exposes. " +
			"This finding clears by itself once the pools that run this repository are ephemeral, " +
			"have no Docker socket, drop root and run in a container.",
	}
	for i, p := range weak {
		if i == maxEvidence {
			break
		}
		f.Evidence = append(f.Evidence, poolEvidence(p.ID, p.Name))
	}
	return result{applies: true, findings: []Finding{f}}
}

func evalForkCodeRan(s *Snapshot) result {
	if !isPublic(s) {
		return result{}
	}
	r := result{applies: true, extra: []Source{SourceRuns}}
	var ids []int64
	for _, run := range runsOf(s) {
		if run.Event == "pull_request" && run.FromFork {
			ids = append(ids, run.ID)
		}
	}
	if len(ids) == 0 {
		return r
	}
	detail := "A pull request from a fork runs code written by whoever opened it. " +
		count(len(ids), "such run", "such runs") + " executed on your runners in the last " + forDays(s.Runs.Window)
	detail += partialRuns(s) + "."
	r.findings = []Finding{{
		Code:     CodeForkCodeRan,
		Severity: SeverityError,
		Title:    "Code from a fork's pull request ran on this fleet",
		Detail:   detail,
		Fix: "Move this repository's jobs to GitHub-hosted runners, or serve it only from a pool that is ephemeral, " +
			"has no Docker socket and runs on hosts you are willing to lose. " +
			"If you have decided this is acceptable, waive this finding and say why.",
		Evidence: newestRuns(ids),
	}}
	return r
}

func evalTargetEventRan(s *Snapshot) result {
	if !isPublic(s) {
		return result{}
	}
	r := result{applies: true, extra: []Source{SourceRuns}}
	var ids []int64
	for _, run := range runsOf(s) {
		if slices.Contains(strangerEvents, run.Event) {
			ids = append(ids, run.ID)
		}
	}
	if len(ids) == 0 {
		return r
	}
	detail := "The pull_request_target, workflow_run, issue_comment and issues events run a workflow from the default branch " +
		"whenever someone outside the project acts. That is safe until the workflow handles what they sent. " +
		count(len(ids), "such run", "such runs") + " executed on your runners in the last " + forDays(s.Runs.Window)
	detail += partialRuns(s) + "."
	r.findings = []Finding{{
		Code:     CodeTargetEventRan,
		Severity: SeverityWarning,
		Title:    "A workflow a stranger can trigger ran on this fleet",
		Detail:   detail,
		Fix: "Check that these workflows never check out or run the pull request's code, and never put its title, branch name or comments into a shell command. " +
			"If they only read it, waive this finding and say why.",
		Evidence: newestRuns(ids),
	}}
	return r
}

// maxEvidence caps the evidence a finding carries.
const maxEvidence = 5

// runsOf is the runs read, or none if they were not.
func runsOf(s *Snapshot) []Run {
	if s.Runs == nil {
		return nil
	}
	return s.Runs.Runs
}

// partialRuns says so, in the sentence, when the runs were a sample: "none seen"
// and "found some" must not read the same.
func partialRuns(s *Snapshot) string {
	if s.Coverage.state(SourceRuns) == CoveragePartial {
		return ", counting only the newest runs Zoomies read"
	}
	return ""
}

// newestRuns is the evidence for a run finding: the three newest run IDs, which
// are numbers and nothing else.
func newestRuns(ids []int64) []Evidence {
	slices.SortFunc(ids, func(a, b int64) int {
		switch {
		case a > b:
			return -1
		case a < b:
			return 1
		}
		return 0
	})
	var out []Evidence
	for i, id := range ids {
		if i == 3 {
			break
		}
		out = append(out, runEvidence(id))
	}
	return out
}

func knownDangers(in []PoolDanger) []PoolDanger {
	var out []PoolDanger
	for _, d := range in {
		for _, w := range weakWords {
			if d == w.danger {
				out = append(out, d)
			}
		}
	}
	return out
}

func isOrAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
