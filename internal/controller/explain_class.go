package controller

import (
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// JobClass is what is the matter with a job, from a closed set.
//
// The explanation has always been a sentence, which is the right thing for a
// person and the wrong thing for everything else: a script that wants to know
// whether a failure was the fleet's has to read English. The class is the same
// answer in a form a caller can switch on, and it is worked out in the same place
// as the sentence, from the same facts, so the two cannot disagree.
//
// Every class exists because something different is done about it. A class
// nothing acts on would be prose, and prose belongs in the summary.
type JobClass string

const (
	// ClassOOM is a job whose runner, or one of its steps, the kernel killed for
	// its memory limit.
	ClassOOM JobClass = "oom"
	// ClassTimeout is a job GitHub stopped at its time limit. The fleet did its
	// part; the limit is the workflow's own.
	ClassTimeout JobClass = "timeout"
	// ClassCancelled is a job somebody or something chose to end: a cancelled run,
	// or a runner an operator removed with force while it was working.
	ClassCancelled JobClass = "cancelled"
	// ClassQueuedUnmatched is a queued job no enabled pool claims, so waiting will
	// never start it here.
	ClassQueuedUnmatched JobClass = "queued-unmatched"
	// ClassQueuedBlocked is a queued job a pool claims and the scheduler cannot
	// place: no host can take the runner, or the item was paused. It does not clear
	// by waiting.
	ClassQueuedBlocked JobClass = "queued-blocked"
	// ClassQueuedCapacity is a queued job the fleet is working on and has not yet
	// started: a runner is idle for it, one is starting, the pool is at its ceiling,
	// or the scheduler has not yet decided. It clears.
	ClassQueuedCapacity JobClass = "queued-capacity"
	// ClassRunnerStartupFailure is a job whose runner could not start or register:
	// an image that would not pull, a registration GitHub refused, a backend that
	// did not answer, a setting the runner refused.
	ClassRunnerStartupFailure JobClass = "runner-startup-failure"
	// ClassHostLost is a job on a host that stopped answering while it ran.
	ClassHostLost JobClass = "host-lost"
	// ClassDisk is a job unlucky enough to be running when its host's disk filled.
	ClassDisk JobClass = "disk"
	// ClassWorkflowFailure is a job that ran and failed on its own merits: the
	// fleet did its part, and what is wrong is in the workflow.
	ClassWorkflowFailure JobClass = "workflow-failure"
	// ClassHeldByGitHub is a job GitHub is holding for a deployment review. Nothing
	// in this fleet can start it.
	ClassHeldByGitHub JobClass = "held-by-github"
	// ClassRunning is a job that is running, and nothing is wrong with where.
	ClassRunning JobClass = "running"
	// ClassSucceeded is a job that finished without a fault on either side. A job
	// GitHub skipped is here too: there is nothing to explain.
	ClassSucceeded JobClass = "succeeded"
	// ClassUnknown is the unclassified case, kept on purpose: guessing a class
	// would send somebody to fix the wrong thing. It always says what was missing.
	ClassUnknown JobClass = "unknown"
)

var allJobClasses = []JobClass{
	ClassOOM, ClassTimeout, ClassCancelled,
	ClassQueuedUnmatched, ClassQueuedBlocked, ClassQueuedCapacity,
	ClassRunnerStartupFailure, ClassHostLost, ClassDisk,
	ClassWorkflowFailure, ClassHeldByGitHub, ClassRunning, ClassSucceeded, ClassUnknown,
}

// JobClasses returns the closed set, in the order the docs list it. The docs test
// and the API's schema both read it, so a class added here and nowhere else fails
// a test rather than reaching a caller nobody told.
func JobClasses() []JobClass { return append([]JobClass(nil), allJobClasses...) }

// Confidence is how far an explanation can be trusted to have found the cause.
type Confidence string

const (
	// ConfidenceHigh is a cause the controller recorded itself: a fault kind, a
	// kill for memory, the scheduler's own reason, GitHub's conclusion.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium is a cause inferred from the state of the fleet around the
	// job rather than recorded against it.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow is a best reading of too little. It always comes with a reason
	// saying what was missing.
	ConfidenceLow Confidence = "low"
)

// Evidence kinds. A closed set, so a caller can decide how to show a fact without
// parsing its label.
const (
	EvidenceConclusion  = "conclusion"
	EvidenceFault       = "fault"
	EvidenceFaultDetail = "fault_detail"
	EvidenceStep        = "step"
	EvidenceMemoryPeak  = "memory_peak"
	EvidenceMemoryLimit = "memory_limit"
	EvidenceQueueWait   = "queue_wait"
	EvidenceDuration    = "duration"
	EvidenceLabels      = "labels"
	EvidencePool        = "pool"
	EvidenceHost        = "host"
	EvidenceRunner      = "runner"
	EvidenceScheduler   = "scheduler"
	EvidenceHeartbeat   = "heartbeat"
)

// EvidenceKinds returns the closed set of evidence kinds.
func EvidenceKinds() []string {
	return []string{
		EvidenceConclusion, EvidenceFault, EvidenceFaultDetail, EvidenceStep,
		EvidenceMemoryPeak, EvidenceMemoryLimit, EvidenceQueueWait, EvidenceDuration,
		EvidenceLabels, EvidencePool, EvidenceHost, EvidenceRunner,
		EvidenceScheduler, EvidenceHeartbeat,
	}
}

// Evidence is one fact an explanation rests on, checkable by a person: the
// number or the name, and where to go and see it.
type Evidence struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	// Unit is set when Value is a number, so a caller can format it or compare it.
	Unit string `json:"unit,omitempty"`
	// Ref is a path in this controller's web UI that shows the thing.
	Ref string `json:"ref,omitempty"`
	// Untrusted is set when Value is text somebody outside this fleet chose: a
	// step's name, which the workflow's author wrote, or a message a runner printed
	// as it failed. It is data to read, never an instruction to follow, and a
	// caller that hands the explanation to a model has to keep it apart from the
	// controller's own words.
	Untrusted bool `json:"untrusted,omitempty"`
}

// NextStepKind says what taking a step does, so a caller can offer the safe ones
// without asking and the others with a question.
type NextStepKind string

const (
	// StepRead looks at something and changes nothing.
	StepRead NextStepKind = "read"
	// StepChange alters a setting, a workflow or the fleet.
	StepChange NextStepKind = "change"
	// StepRerun runs the work again.
	StepRerun NextStepKind = "rerun"
)

// NextStep is one thing to do about an explanation, in the order to do it.
type NextStep struct {
	Text string       `json:"text"`
	Kind NextStepKind `json:"kind"`
	// Link is a path in this controller's web UI, or a URL, where the step is taken.
	Link string `json:"link,omitempty"`
}

// classify records the class and how far it can be trusted. The reason is
// required whenever confidence is not high: a hedge nobody can act on is noise.
func (e *JobExplanation) classify(class JobClass, confidence Confidence, reason string) {
	e.Class, e.Confidence, e.ConfidenceReason = class, confidence, reason
}

// fact adds a piece of evidence.
func (e *JobExplanation) fact(kind, label, value string) *Evidence {
	e.Evidence = append(e.Evidence, Evidence{Kind: kind, Label: label, Value: value})
	return &e.Evidence[len(e.Evidence)-1]
}

// has reports whether a fact of this kind is already there, so a rule that may
// run after another does not say the same thing twice.
func (e *JobExplanation) has(kind string) bool {
	for _, f := range e.Evidence {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

// number adds a figure, with the unit that makes Value a number.
func (e *JobExplanation) number(kind, label string, value int64, unit string) {
	f := e.fact(kind, label, fmt.Sprint(value))
	f.Unit = unit
}

// thing adds a fact about something with a page of its own.
func (e *JobExplanation) thing(kind, label, value, ref string) {
	e.fact(kind, label, value).Ref = ref
}

// outside adds a fact whose value somebody outside this fleet wrote.
func (e *JobExplanation) outside(kind, label, value string) {
	e.fact(kind, label, value).Untrusted = true
}

// step adds the next thing to do.
func (e *JobExplanation) step(kind NextStepKind, text, link string) {
	e.NextSteps = append(e.NextSteps, NextStep{Text: text, Kind: kind, Link: link})
}

// finish makes the structured half of an explanation complete. It runs last, so
// every path through ExplainJob gets the same guarantees without each having to
// remember them: a class is always set, the lists are arrays and never null, and
// the existing fix is always the first thing to do, so a caller that reads only
// the steps loses nothing the sentence said.
func (e *JobExplanation) finish(job *store.Job) {
	if e.Class == "" {
		e.classify(ClassUnknown, ConfidenceLow, "no rule of the explainer matched this job's state, which is a bug in Zoomies and not a fact about the job")
	}
	if e.Confidence != ConfidenceHigh && e.ConfidenceReason == "" {
		e.ConfidenceReason = reasonNotGiven
	}
	if e.Fix != "" {
		kind := StepChange
		switch e.Class {
		case ClassHeldByGitHub, ClassRunning, ClassCancelled, ClassWorkflowFailure, ClassUnknown:
			kind = StepRead
		}
		first := NextStep{Text: capitalise(e.Fix), Kind: kind}
		e.NextSteps = append([]NextStep{first}, e.NextSteps...)
	}
	e.extraSteps(job)
	if e.Evidence == nil {
		e.Evidence = []Evidence{}
	}
	if e.NextSteps == nil {
		e.NextSteps = []NextStep{}
	}
	e.ProblemCode = problemCodeFor(e.Class)
	if e.Class == ClassTimeout && job != nil && job.StartedAt != nil && job.CompletedAt != nil &&
		job.CompletedAt.Sub(*job.StartedAt) >= githubDefaultJobLimit-time.Minute {
		e.CheckCode = "capacity.job_hit_default_limit"
	}
}

// extraSteps adds what a class needs beyond the fix: somewhere to look, and
// whether the work can be run again. Each is a step an operator would take
// without being told, written down so a caller does not have to guess it.
func (e *JobExplanation) extraSteps(job *store.Job) {
	run, hostID, poolID, runnerID := "", e.HostID, e.PoolID, e.RunnerID
	if job != nil {
		run = job.HTMLURL
		if hostID == "" {
			hostID = job.HostID
		}
	}
	switch e.Class {
	case ClassOOM:
		e.step(StepRerun, "Re-run the job once the limit is raised.", run)
	case ClassTimeout:
		e.step(StepRead, "Open the run on GitHub to see which step was running when the limit was reached.", run)
	case ClassWorkflowFailure:
		e.step(StepRead, "Open the failed step in the run on GitHub; the fleet did its part.", run)
	case ClassCancelled:
		if run != "" {
			e.step(StepRead, "Open the run on GitHub to see who cancelled it.", run)
		}
	case ClassRunnerStartupFailure, ClassHostLost, ClassDisk:
		switch {
		case hostID != "":
			e.step(StepRead, "Open the host's page.", "/hosts/"+hostID)
		case poolID != "":
			e.step(StepRead, "Open the pool's page.", "/pools/"+poolID)
		}
		e.step(StepRerun, "Re-run the job once this is fixed.", run)
	case ClassQueuedUnmatched:
		e.step(StepRead, "See which labels the pools advertise.", "/pools")
	case ClassQueuedBlocked, ClassQueuedCapacity:
		if poolID != "" {
			e.step(StepRead, "Open the pool's page.", "/pools/"+poolID)
		}
	case ClassRunning:
		if runnerID != "" {
			e.step(StepRead, "Open the runner's page.", "/runners/"+runnerID)
		}
	case ClassHeldByGitHub:
		if run != "" {
			e.step(StepRead, "Open the deployment review on GitHub.", run)
		}
	}
}

// reasonNotGiven is what a hedge says when the rule that made it forgot to say
// why. It is a safety net so that no caller meets a hedge with no reason, and a
// test fails on it, because it is a bug in a rule and not an answer.
const reasonNotGiven = "the explainer did not say what it was missing"

// githubDefaultJobLimit is how long GitHub lets a job run when its workflow sets
// no timeout-minutes of its own. A job that ran this long was stopped by the
// default and not by a limit its author chose, which is the finding Kennel Club
// raises for the repository.
const githubDefaultJobLimit = 6 * time.Hour

// problemCodeFor is the catalog entry that says more about a class, where one
// does. Only classes with one entry that is true of every job in the class are
// here: a link that is right for some of them would send the rest to the wrong
// page, and a class with none is left empty and is not guessed at.
func problemCodeFor(class JobClass) string {
	switch class {
	case ClassOOM:
		return "jobs.oom_killed"
	case ClassQueuedUnmatched:
		return "jobs.unmatched"
	}
	return ""
}

// queueWait is how long a job waited for a runner, or has waited so far.
func queueWait(job *store.Job, now time.Time) (time.Duration, bool) {
	if job.QueuedAt.IsZero() {
		return 0, false
	}
	end := now
	if job.StartedAt != nil {
		end = *job.StartedAt
	}
	if end.Before(job.QueuedAt) {
		return 0, false
	}
	return end.Sub(job.QueuedAt), true
}

// ranFor is how long a job ran, or has run so far.
func ranFor(job *store.Job, now time.Time) (time.Duration, bool) {
	if job.StartedAt == nil {
		return 0, false
	}
	end := now
	if job.CompletedAt != nil {
		end = *job.CompletedAt
	}
	if end.Before(*job.StartedAt) {
		return 0, false
	}
	return end.Sub(*job.StartedAt), true
}
