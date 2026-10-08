package controller

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/catalog"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// WhyClass is the one-word answer to "why is this job where it is". The set
// is closed on purpose: a class is something a person can act on and a
// skill can match, and a new one is a change to both.
type WhyClass string

const (
	WhyOOM                  WhyClass = "oom"
	WhyTimeout              WhyClass = "timeout"
	WhyCancelled            WhyClass = "cancelled"
	WhyQueuedUnmatched      WhyClass = "queued-unmatched"
	WhyQueuedBlocked        WhyClass = "queued-blocked"
	WhyQueuedCapacity       WhyClass = "queued-capacity"
	WhyQueued               WhyClass = "queued"
	WhyRunnerStartupFailure WhyClass = "runner-startup-failure"
	WhyHostLost             WhyClass = "host-lost"
	WhyDisk                 WhyClass = "disk"
	WhyWorkflowFailure      WhyClass = "workflow-failure"
	WhyHeldByGitHub         WhyClass = "held-by-github"
	WhyRunning              WhyClass = "running"
	WhySucceeded            WhyClass = "succeeded"
	WhyUnknown              WhyClass = "unknown"
)

// WhyConfidence says how far the class rests on a recorded fact. High is a
// fact the fleet wrote down; medium is an inference or a fact with a gap in
// it, and the reason says which; low is a guess, and unknown is always low.
type WhyConfidence string

const (
	WhyHigh   WhyConfidence = "high"
	WhyMedium WhyConfidence = "medium"
	WhyLow    WhyConfidence = "low"
)

// Evidence is one fact a person can check for themselves: where it came
// from, what it said, and where to look.
type Evidence struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
	// Ref is where the evidence lives: a path in this UI, or a URL.
	Ref string `json:"ref,omitempty"`
}

// NextStep is one thing to do, in the order to do them. Kind says whether it
// is something to read, something to change, or the re-run that follows a
// change, so a surface can offer a button where one applies.
type NextStep struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
	Link string `json:"link,omitempty"`
}

// LogExcerpt is the few lines of runner output around the one that decided
// the class. Note says why there are none, when there are none.
type LogExcerpt struct {
	Lines []LogLine `json:"lines"`
	Note  string    `json:"note,omitempty"`
}

// LogLine is one numbered line, numbered from the first line the fleet kept.
type LogLine struct {
	N        int    `json:"n"`
	Text     string `json:"text"`
	Decisive bool   `json:"decisive,omitempty"`
}

// whySnapshot is everything classify reads. ExplainJob assembles it from the
// store and the last plan, so the classing itself is a pure function and
// every class is a table-test row.
type whySnapshot struct {
	Job *store.Job
	Now time.Time
	// Pool is the pool that claimed the job; PoolMissing says it was claimed
	// by a pool that is no longer here.
	Pool        *store.Pool
	PoolMissing bool
	// PoolPlan and Unmatched are the scheduler's last words about this pool
	// and this job, from a pass made at PlanAt.
	PoolPlan  *scheduler.PoolPlan
	Unmatched *scheduler.UnmatchedJob
	PlanAt    time.Time
	// Counts is the pool's live runners, when they were loaded.
	Counts *store.PoolCounts
	// Host is the host of a running job's runner, when there is one.
	Host *store.Host
}

// whyVerdict is what classify decides.
type whyVerdict struct {
	Class            WhyClass
	Confidence       WhyConfidence
	ConfidenceReason string
	Evidence         []Evidence
	ProblemCode      string
	NextSteps        []NextStep
}

// stalePlan is how old a scheduler pass may be before an explanation that
// rests on it says so. A pass runs every few seconds on a healthy
// controller; two minutes means something is in the way of the loop itself.
const stalePlan = 2 * time.Minute

var exitCodeRE = regexp.MustCompile(`exited with code (\d+)`)

// classify is the class table. First match wins, and the order is the order
// a reader would want: what GitHub holds, what was cancelled, what the fleet
// broke, what the workflow did, and only then the queue.
func classify(s whySnapshot) whyVerdict {
	job := s.Job
	v := whyVerdict{Confidence: WhyHigh}
	v.Evidence = baseEvidence(s)
	rerun := NextStep{Text: "Re-run the job once the cause is addressed.", Kind: "rerun"}
	poolLink := ""
	if job.PoolID != "" {
		poolLink = "/pools/" + job.PoolID
	}

	switch {
	case job.State == store.JobWaiting:
		v.Class = WhyHeldByGitHub
		v.NextSteps = []NextStep{{Text: "Approve the deployment on GitHub, or leave it: nothing in this fleet is wrong.", Kind: "read", Link: job.HTMLURL}}

	case job.Cancelling() || (job.State == store.JobCompleted && job.Conclusion == "cancelled"):
		v.Class = WhyCancelled
		v.NextSteps = []NextStep{{Text: "Re-run the workflow on GitHub if the work is still wanted.", Kind: "rerun"}}

	case job.OOMKilled || job.FaultKind == store.FaultOutOfMemory:
		v.Class = WhyOOM
		v.ProblemCode = "jobs.oom_killed"
		if job.GrantedMemoryMB <= 0 {
			v.Confidence, v.ConfidenceReason = WhyMedium, "the memory limit was not recorded"
		}
		v.NextSteps = []NextStep{{Text: "Give this job more memory: raise the pool's memory per runner, or move the job to a larger size class.", Kind: "change", Link: poolLink}, rerun}

	case job.FaultKind == store.FaultOutOfDisk:
		v.Class = WhyDisk
		v.ProblemCode = "pool.cache_above_disk"
		v.NextSteps = []NextStep{{Text: "Free disk on the host: prune images and caches, or lower the pool's cache size.", Kind: "change", Link: hostLink(job.HostID)}, rerun}

	case job.FaultKind == store.FaultHostLost:
		v.Class = WhyHostLost
		v.ProblemCode = "jobs.runner_lost"
		v.NextSteps = []NextStep{{Text: "Open the host and check that its agent is running and can reach this controller.", Kind: "read", Link: hostLink(job.HostID)}, rerun}

	case isStartupFault(job.FaultKind):
		v.Class = WhyRunnerStartupFailure
		v.ProblemCode = "pool.runners_failing"
		v.NextSteps = []NextStep{{Text: capitalise(startFailureFix(job.FaultKind)), Kind: "change", Link: poolLink}, rerun}

	case job.FaultKind == store.FaultRemoved || job.FaultKind == store.FaultRunnerExited:
		v.Class = WhyUnknown
		v.Confidence = WhyLow
		v.ConfidenceReason = fmt.Sprintf("the fleet lost the runner (%s) without recording why", job.FaultKind)
		v.NextSteps = []NextStep{{Text: "Open the runner's record for what it said as it went.", Kind: "read", Link: runnerLink(job.RunnerID)}}

	case job.State == store.JobCompleted:
		classifyConclusion(s, &v)

	case job.State == store.JobInProgress:
		if s.Host != nil && !s.Host.Healthy(s.Now) {
			v.Class = WhyHostLost
			v.ProblemCode = "jobs.runner_lost"
			v.Confidence = WhyMedium
			v.ConfidenceReason = fmt.Sprintf("inferred from a heartbeat %s old; the fleet has not yet recorded the runner as lost", formatAge(s.Now.Sub(s.Host.LastHeartbeat)))
			v.NextSteps = []NextStep{{Text: "Open the host and check that its agent is running and can reach this controller.", Kind: "read", Link: hostLink(s.Host.ID)}, rerun}
			break
		}
		v.Class = WhyRunning
		v.NextSteps = []NextStep{{Text: "Open the runner to follow its output.", Kind: "read", Link: runnerLink(job.RunnerID)}}

	default:
		classifyQueued(s, &v)
	}
	return v
}

// classifyConclusion is the completed-job half of the table: by now the fleet
// has said it recorded no fault, so the conclusion is the workflow's own.
func classifyConclusion(s whySnapshot, v *whyVerdict) {
	job := s.Job
	rerun := NextStep{Text: "Re-run the job once the cause is addressed.", Kind: "rerun"}
	switch job.Conclusion {
	case "timed_out":
		v.Class = WhyTimeout
		v.NextSteps = []NextStep{{Text: "Raise the job's timeout-minutes, or split the job: the runner was healthy for the whole of it.", Kind: "change", Link: job.HTMLURL}, rerun}
	case "failure", "startup_failure":
		v.Class = WhyWorkflowFailure
		v.NextSteps = []NextStep{{Text: "Open the failed step on GitHub: the fleet did its part, and the cause is in the workflow's own output.", Kind: "read", Link: job.HTMLURL}, rerun}
	case "success":
		v.Class = WhySucceeded
	default:
		v.Class = WhyUnknown
		v.Confidence = WhyLow
		v.ConfidenceReason = fmt.Sprintf("GitHub concluded it %s, which the fleet does not class", job.Conclusion)
		v.NextSteps = []NextStep{{Text: "Open the run on GitHub for what that conclusion meant.", Kind: "read", Link: job.HTMLURL}}
	}
}

// classifyQueued is the queued half: nothing claims it, nothing can place it,
// nothing is free for it, or it is simply next.
func classifyQueued(s whySnapshot, v *whyVerdict) {
	job := s.Job
	poolLink := ""
	if job.PoolID != "" {
		poolLink = "/pools/" + job.PoolID
	}
	switch {
	case !job.Matched:
		v.Class = WhyQueuedUnmatched
		v.ProblemCode = "jobs.unmatched"
		if s.Unmatched != nil && s.Unmatched.Reason != "" {
			v.Evidence = append(v.Evidence, Evidence{Kind: "plan_reason", Label: "scheduler", Value: s.Unmatched.Reason})
		}
		v.NextSteps = []NextStep{{Text: "Add these labels to a pool, or change the workflow's runs-on to labels a pool here carries.", Kind: "change", Link: "/pools"}}

	case s.PoolMissing:
		v.Class = WhyQueuedBlocked
		v.ProblemCode = "pool.no_capacity"
		v.Evidence = append(v.Evidence, Evidence{Kind: "pool_missing", Label: "pool", Value: job.PoolID})
		v.NextSteps = []NextStep{{Text: "The pool that claimed this job is gone: choose or create a pool for these labels.", Kind: "change", Link: "/pools"}}

	case s.PoolPlan != nil && s.PoolPlan.Blocked != "":
		v.Class = WhyQueuedBlocked
		v.ProblemCode = "pool.no_capacity"
		if s.PoolPlan.BlockedNoEligibleHost {
			v.ProblemCode = "pool.no_eligible_host"
		}
		v.Evidence = append(v.Evidence, Evidence{Kind: "plan_reason", Label: "scheduler", Value: s.PoolPlan.Blocked})
		if age := s.Now.Sub(s.PlanAt); age > stalePlan {
			v.Confidence, v.ConfidenceReason = WhyMedium, fmt.Sprintf("the scheduler's last pass is %s old", formatAge(age))
		}
		fix := s.PoolPlan.BlockedFix
		if fix == "" {
			fix = "add a host, raise a host's capacity, uncordon one, or relax the pool's host selector."
		}
		v.NextSteps = []NextStep{{Text: capitalise(fix), Kind: "change", Link: poolLink}}

	case s.PoolPlan != nil && s.PoolPlan.Failing != "":
		v.Class = WhyRunnerStartupFailure
		v.ProblemCode = "pool.runners_failing"
		v.Evidence = append(v.Evidence, Evidence{Kind: "plan_reason", Label: "scheduler", Value: s.PoolPlan.Failing})
		if s.PoolPlan.FailingFault != "" {
			v.Evidence = append(v.Evidence, Evidence{Kind: "fault_kind", Label: "fault", Value: string(s.PoolPlan.FailingFault)})
		}
		v.NextSteps = []NextStep{{Text: capitalise(startFailureFix(s.PoolPlan.FailingFault)), Kind: "change", Link: poolLink}}

	case s.Counts != nil && s.Pool != nil && s.Pool.MaxRunners > 0 && s.Counts.Live() >= s.Pool.MaxRunners:
		v.Class = WhyQueuedCapacity
		v.ProblemCode = "pool.no_capacity"
		v.Evidence = append(v.Evidence, Evidence{Kind: "plan_reason", Label: "pool", Value: fmt.Sprintf("at its ceiling of %s, all busy", plural(s.Pool.MaxRunners, "runner"))})
		v.NextSteps = []NextStep{{Text: "Raise this pool's max_runners if the fleet has room for more.", Kind: "change", Link: poolLink}}

	case s.Counts != nil && s.Counts.Provisioning+s.Counts.Registering > 0:
		v.Class = WhyQueuedCapacity
		v.Evidence = append(v.Evidence, Evidence{Kind: "plan_reason", Label: "pool", Value: fmt.Sprintf("%s starting", runnersAre(s.Counts.Provisioning+s.Counts.Registering))})
		v.NextSteps = []NextStep{{Text: "Wait: the job goes to the first runner GitHub sees.", Kind: "read", Link: poolLink}}

	default:
		v.Class = WhyQueued
		v.NextSteps = []NextStep{{Text: "Open the pool: a runner is idle or on its way, and GitHub chooses which takes the job.", Kind: "read", Link: poolLink}}
	}
}

// baseEvidence is what every class carries: the conclusion, the fault and
// its exit code, the memory figures, and how long the job waited.
func baseEvidence(s whySnapshot) []Evidence {
	job := s.Job
	var out []Evidence
	if job.State == store.JobCompleted {
		out = append(out, Evidence{Kind: "conclusion", Label: "GitHub's conclusion", Value: job.Conclusion, Ref: job.HTMLURL})
	}
	if job.FaultKind != "" {
		out = append(out, Evidence{Kind: "fault_kind", Label: "fault", Value: string(job.FaultKind), Ref: runnerLink(job.RunnerID)})
	}
	if m := exitCodeRE.FindStringSubmatch(job.RunnerFault); m != nil {
		out = append(out, Evidence{Kind: "exit_code", Label: "exit code", Value: m[1]})
		if m[1] == "137" {
			out = append(out, Evidence{Kind: "signal", Label: "signal", Value: "SIGKILL"})
		}
	}
	if job.OOMKilled || job.FaultKind == store.FaultOutOfMemory {
		if job.PeakMemoryMB > 0 {
			out = append(out, Evidence{Kind: "memory_peak", Label: "memory peak", Value: strconv.FormatInt(job.PeakMemoryMB, 10), Unit: "MB"})
		}
		if job.GrantedMemoryMB > 0 {
			out = append(out, Evidence{Kind: "memory_limit", Label: "memory limit", Value: strconv.FormatInt(job.GrantedMemoryMB, 10), Unit: "MB"})
		}
	}
	if !job.QueuedAt.IsZero() {
		end := s.Now
		if job.StartedAt != nil {
			end = *job.StartedAt
		}
		if wait := end.Sub(job.QueuedAt); wait >= 0 {
			out = append(out, Evidence{Kind: "queue_wait", Label: "queue wait", Value: strconv.Itoa(int(wait.Seconds())), Unit: "s"})
		}
	}
	if s.Host != nil {
		state := "healthy"
		if !s.Host.Healthy(s.Now) {
			state = fmt.Sprintf("quiet for %s", formatAge(s.Now.Sub(s.Host.LastHeartbeat)))
		}
		out = append(out, Evidence{Kind: "host_state", Label: "host", Value: state, Ref: hostLink(s.Host.ID)})
	}
	if !job.Matched && job.State == store.JobQueued {
		out = append(out, Evidence{Kind: "labels", Label: "runs-on", Value: joinLabels(job.Labels)})
	}
	return out
}

func isStartupFault(k store.FaultKind) bool {
	switch k {
	case store.FaultImage, store.FaultRegistration, store.FaultBackend, store.FaultBackendBusy, store.FaultConfig, store.FaultContainerConflict:
		return true
	}
	return false
}

func hostLink(id string) string {
	if id == "" {
		return ""
	}
	return "/hosts/" + id
}

func runnerLink(id string) string {
	if id == "" {
		return ""
	}
	return "/runners/" + id
}

var (
	catalogDocsOnce sync.Once
	catalogDocs     map[string]string
)

// catalogDocsFor is the human page for a problem code, from the embedded
// catalog, so the explanation's "read on" step points at the same anchor the
// problems drawer does.
func catalogDocsFor(code string) string {
	catalogDocsOnce.Do(func() {
		catalogDocs = map[string]string{}
		if cat, err := catalog.Current(); err == nil {
			for _, e := range cat.Entries {
				catalogDocs[e.ID] = e.DocsHTML
			}
		}
	})
	return catalogDocs[code]
}

// decisiveLine is, per class, what the line that decided it looks like, the
// strongest sign first: the kernel's own "Killed process" line outranks the
// entrypoint echoing "exit code 137" on its way out, which is the last line
// of nearly every killed runner and says nothing the kill did not. A class
// with no pattern, or a tail with no match, decides on its last line: the
// end of the output is where a runner says why it stopped.
var decisiveLine = map[WhyClass][]*regexp.Regexp{
	WhyOOM: {
		regexp.MustCompile(`(?i)killed process|out of memory|oom[- ]?kill`),
		regexp.MustCompile(`(?i)exit code 137|memory limit`),
	},
	WhyDisk: {
		regexp.MustCompile(`(?i)no space left|enospc|disk full`),
	},
}

// decisiveIndex finds the line a class decided on, trying each of its
// patterns in turn from the end of the tail, and the last line when none hits.
func decisiveIndex(tail []string, class WhyClass) int {
	for _, re := range decisiveLine[class] {
		for i := len(tail) - 1; i >= 0; i-- {
			if re.MatchString(tail[i]) {
				return i
			}
		}
	}
	return len(tail) - 1
}

// excerptFrom picks the n lines of a kept tail that lead up to the one that
// decided the class, numbered from the first line kept, each scrubbed the
// way a problem's sentence is: a workflow wrote them, and whoever can open a
// pull request writes workflows. n of 0 is no excerpt at all, which a
// caller that did not ask for one gets as null; a tail of nothing is an
// excerpt with a note, because "there is none" is itself an answer.
func excerptFrom(tail []string, class WhyClass, n int) *LogExcerpt {
	if n <= 0 {
		return nil
	}
	if len(tail) == 0 {
		return &LogExcerpt{Lines: []LogLine{}, Note: "The runner's output was not kept: it ran on an agent older than this release, or it ended before this fault was recorded."}
	}
	if n > store.OutputTailLines {
		n = store.OutputTailLines
	}
	decisive := decisiveIndex(tail, class)
	start := decisive - n + 1
	if start < 0 {
		start = 0
	}
	out := &LogExcerpt{Lines: make([]LogLine, 0, decisive-start+1)}
	for i := start; i <= decisive; i++ {
		out.Lines = append(out.Lines, LogLine{N: i + 1, Text: workflowTextN(tail[i], store.OutputTailRunes), Decisive: i == decisive})
	}
	return out
}
