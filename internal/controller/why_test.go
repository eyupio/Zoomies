package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/catalog"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// whyCase is one row of the class table: the facts a job carries, and the
// one answer those facts allow.
type whyCase struct {
	name       string
	snap       whySnapshot
	class      WhyClass
	confidence WhyConfidence
	code       string
	firstStep  string // the kind of next_steps[0]; "" means no steps
}

func whyCases(now time.Time) []whyCase {
	completed := func(conclusion string) *store.Job {
		started := now.Add(-10 * time.Minute)
		done := now.Add(-time.Minute)
		return &store.Job{ID: "job_1", State: store.JobCompleted, Conclusion: conclusion, QueuedAt: now.Add(-12 * time.Minute), StartedAt: &started, CompletedAt: &done, HTMLURL: "https://github.com/acme/widgets/actions/runs/1/job/2", PoolID: "pool_1", RunnerID: "run_1", HostID: "host_1"}
	}
	faulted := func(kind store.FaultKind, fault string) *store.Job {
		j := completed("failure")
		j.FaultKind, j.RunnerFault = kind, fault
		return j
	}
	queued := func() *store.Job {
		return &store.Job{ID: "job_1", State: store.JobQueued, QueuedAt: now.Add(-3 * time.Minute), Labels: store.StringSlice{"self-hosted", "linux"}, PoolID: "pool_1", Matched: true}
	}
	pool := &store.Pool{ID: "pool_1", Name: "zoomies-linux", MaxRunners: 2}
	oom := faulted(store.FaultOutOfMemory, "runner exited with code 137: the container was killed for exceeding its memory limit")
	oom.OOMKilled, oom.PeakMemoryMB, oom.GrantedMemoryMB = true, 7900, 8192
	tolerated := completed("success")
	tolerated.OOMKilled, tolerated.PeakMemoryMB = true, 7900
	held := &store.Job{ID: "job_1", State: store.JobWaiting, QueuedAt: now.Add(-18 * time.Minute), HTMLURL: "https://github.com/acme/widgets/actions/runs/1"}
	cancelling := queued()
	t := now.Add(-time.Minute)
	cancelling.CancelRequestedAt = &t
	running := &store.Job{ID: "job_1", State: store.JobInProgress, RunnerID: "run_1", RunnerName: "zoomies-linux-abcd", PoolID: "pool_1", QueuedAt: now.Add(-5 * time.Minute)}
	quiet := &store.Host{ID: "host_1", Name: "builder-1", LastHeartbeat: now.Add(-10 * time.Minute)}
	unmatched := queued()
	unmatched.PoolID, unmatched.Matched = "", false
	stale := now.Add(-5 * time.Minute)
	return []whyCase{
		{"a held job is GitHub's wait", whySnapshot{Job: held, Now: now}, WhyHeldByGitHub, WhyHigh, "", "read"},
		{"a cancelled conclusion", whySnapshot{Job: completed("cancelled"), Now: now}, WhyCancelled, WhyHigh, "", "rerun"},
		{"a cancellation in flight", whySnapshot{Job: cancelling, Now: now, Pool: pool}, WhyCancelled, WhyHigh, "", "rerun"},
		{"an out-of-memory kill with its limit recorded", whySnapshot{Job: oom, Now: now, Pool: pool}, WhyOOM, WhyHigh, "jobs.oom_killed", "change"},
		{"an out-of-memory kill the job tolerated is still an out-of-memory kill", whySnapshot{Job: tolerated, Now: now, Pool: pool}, WhyOOM, WhyMedium, "jobs.oom_killed", "change"},
		{"a runner that ran out of disk", whySnapshot{Job: faulted(store.FaultOutOfDisk, "runner exited with code 1: no space left on device"), Now: now, Pool: pool}, WhyDisk, WhyHigh, "pool.cache_above_disk", "change"},
		{"a host the fleet lost", whySnapshot{Job: faulted(store.FaultHostLost, "the host stopped reporting"), Now: now, Pool: pool}, WhyHostLost, WhyHigh, "jobs.runner_lost", "read"},
		{"a running job on a quiet host is inferred, not recorded", whySnapshot{Job: running, Now: now, Pool: pool, Host: quiet}, WhyHostLost, WhyMedium, "jobs.runner_lost", "read"},
		{"a runner GitHub would not register", whySnapshot{Job: faulted(store.FaultRegistration, "github: create jit config: 403 Forbidden"), Now: now, Pool: pool}, WhyRunnerStartupFailure, WhyHigh, "pool.runners_failing", "change"},
		{"a busy backend is a startup failure", whySnapshot{Job: faulted(store.FaultBackendBusy, "docker: too many requests"), Now: now, Pool: pool}, WhyRunnerStartupFailure, WhyHigh, "pool.runners_failing", "change"},
		{"a runner that exited for no recorded reason", whySnapshot{Job: faulted(store.FaultRunnerExited, "runner was killed (exit 137)"), Now: now, Pool: pool}, WhyUnknown, WhyLow, "", "read"},
		{"a job GitHub timed out", whySnapshot{Job: completed("timed_out"), Now: now}, WhyTimeout, WhyHigh, "", "change"},
		{"a workflow's own failure", whySnapshot{Job: completed("failure"), Now: now}, WhyWorkflowFailure, WhyHigh, "", "read"},
		{"a startup failure GitHub reports is the workflow's", whySnapshot{Job: completed("startup_failure"), Now: now}, WhyWorkflowFailure, WhyHigh, "", "read"},
		{"a success", whySnapshot{Job: completed("success"), Now: now}, WhySucceeded, WhyHigh, "", ""},
		{"a conclusion the fleet does not class", whySnapshot{Job: completed("action_required"), Now: now}, WhyUnknown, WhyLow, "", "read"},
		{"a running job", whySnapshot{Job: running, Now: now, Pool: pool, Host: &store.Host{ID: "host_1", LastHeartbeat: now}}, WhyRunning, WhyHigh, "", "read"},
		{"a job no pool claims", whySnapshot{Job: unmatched, Now: now, Unmatched: &scheduler.UnmatchedJob{Job: unmatched, Reason: "no enabled pool carries cuda12"}}, WhyQueuedUnmatched, WhyHigh, "jobs.unmatched", "change"},
		{"a job whose pool has gone", whySnapshot{Job: queued(), Now: now, PoolMissing: true}, WhyQueuedBlocked, WhyHigh, "pool.no_capacity", "change"},
		{"a pool the scheduler cannot place", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, PoolPlan: &scheduler.PoolPlan{PoolID: "pool_1", Blocked: "no host has 8 GB free", BlockedFix: "add a host"}}, WhyQueuedBlocked, WhyHigh, "pool.no_capacity", "change"},
		{"a pool with no eligible host names that code", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, PoolPlan: &scheduler.PoolPlan{PoolID: "pool_1", Blocked: "no host matches zone=nowhere", BlockedNoEligibleHost: true}}, WhyQueuedBlocked, WhyHigh, "pool.no_eligible_host", "change"},
		{"a stale plan lowers the confidence", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: stale, PoolPlan: &scheduler.PoolPlan{PoolID: "pool_1", Blocked: "no host has 8 GB free"}}, WhyQueuedBlocked, WhyMedium, "pool.no_capacity", "change"},
		{"a pool whose runners keep failing to start", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, PoolPlan: &scheduler.PoolPlan{PoolID: "pool_1", Failing: "3 runners failed to register", FailingFault: store.FaultRegistration}}, WhyRunnerStartupFailure, WhyHigh, "pool.runners_failing", "change"},
		{"a pool at its ceiling", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, Counts: &store.PoolCounts{Busy: 2}}, WhyQueuedCapacity, WhyHigh, "pool.no_capacity", "change"},
		{"a runner starting", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, Counts: &store.PoolCounts{Provisioning: 1}}, WhyQueuedCapacity, WhyHigh, "", "read"},
		{"a runner idle and waiting", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, Counts: &store.PoolCounts{Idle: 1}}, WhyQueued, WhyHigh, "", "read"},
		{"a pool at its ceiling with a runner idle is not at capacity", whySnapshot{Job: queued(), Now: now, Pool: pool, PlanAt: now, Counts: &store.PoolCounts{Idle: 1, Busy: 1}}, WhyQueued, WhyHigh, "", "read"},
		{"no scheduler pass yet is a gap, not a full answer", whySnapshot{Job: queued(), Now: now, Pool: pool, Counts: &store.PoolCounts{}}, WhyQueued, WhyMedium, "", "read"},
	}
}

// Every class the explanation can give comes from facts the fleet records, and
// each row here is one such set of facts: a reader who changes the table in
// why.go changes a row here, and a set of facts no row covers is a gap.
func TestEveryClassComesFromItsOwnFacts(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range whyCases(now) {
		t.Run(tc.name, func(t *testing.T) {
			v := classify(tc.snap)
			if v.Class != tc.class || v.Confidence != tc.confidence {
				t.Fatalf("class %s (%s), want %s (%s); reason %q", v.Class, v.Confidence, tc.class, tc.confidence, v.ConfidenceReason)
			}
			if v.ProblemCode != tc.code {
				t.Fatalf("problem_code %q, want %q", v.ProblemCode, tc.code)
			}
			switch {
			case tc.firstStep == "" && len(v.NextSteps) != 0:
				t.Fatalf("next_steps = %+v, want none", v.NextSteps)
			case tc.firstStep != "" && (len(v.NextSteps) == 0 || v.NextSteps[0].Kind != tc.firstStep):
				t.Fatalf("next_steps = %+v, want the first of kind %s", v.NextSteps, tc.firstStep)
			}
			if v.Class == WhyUnknown && v.ConfidenceReason == "" {
				t.Fatal("an unknown class must say what was missing")
			}
			if v.Confidence != WhyHigh && v.ConfidenceReason == "" {
				t.Fatal("a confidence below high must say what data is missing")
			}
			if tc.snap.Job.State == store.JobCompleted && !hasEvidence(v.Evidence, "conclusion") {
				t.Fatalf("a completed job carries its conclusion as evidence: %+v", v.Evidence)
			}
			// A held job's time is GitHub's, not the queue's: the sentence
			// says the wait has not started, so no figure may say it has.
			if tc.snap.Job.State == store.JobWaiting && hasEvidence(v.Evidence, "queue_wait") {
				t.Fatalf("a held job was charged a queue wait: %+v", v.Evidence)
			}
		})
	}
}

func hasEvidence(rows []Evidence, kind string) bool {
	for _, e := range rows {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func findEvidence(rows []Evidence, kind string) Evidence {
	for _, e := range rows {
		if e.Kind == kind {
			return e
		}
	}
	return Evidence{}
}

// The memory valve does not change what happened; it changes how sure the
// explanation can be about the figure that matters. A kill with its limit
// recorded is a complete story; one without says so rather than guessing.
func TestTheMemoryValveChangesTheReasonNotTheClass(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	done := now.Add(-time.Minute)
	job := &store.Job{ID: "job_1", State: store.JobCompleted, Conclusion: "failure", CompletedAt: &done, QueuedAt: now.Add(-time.Hour), FaultKind: store.FaultOutOfMemory, RunnerFault: "runner exited with code 137", OOMKilled: true, PeakMemoryMB: 7900, PoolID: "pool_1"}

	v := classify(whySnapshot{Job: job, Now: now})
	if v.Class != WhyOOM || v.Confidence != WhyMedium || v.ConfidenceReason != "the memory limit was not recorded" {
		t.Fatalf("without a limit: %s %s %q", v.Class, v.Confidence, v.ConfidenceReason)
	}
	if hasEvidence(v.Evidence, "memory_limit") {
		t.Fatal("no limit was recorded, so none may be quoted")
	}
	if e := findEvidence(v.Evidence, "exit_code"); e.Value != "137" {
		t.Fatalf("exit_code evidence = %+v, want 137 parsed from the fault", e)
	}
	if e := findEvidence(v.Evidence, "signal"); e.Value != "SIGKILL" {
		t.Fatalf("signal evidence = %+v, want SIGKILL for 137", e)
	}

	job.GrantedMemoryMB = 4096
	v = classify(whySnapshot{Job: job, Now: now})
	if v.Class != WhyOOM || v.Confidence != WhyHigh {
		t.Fatalf("with a limit: %s %s %q", v.Class, v.Confidence, v.ConfidenceReason)
	}
	if e := findEvidence(v.Evidence, "memory_limit"); e.Value != "4096" || e.Unit != "MB" {
		t.Fatalf("memory_limit evidence = %+v", e)
	}
	if e := findEvidence(v.Evidence, "memory_peak"); e.Value != "7900" || e.Unit != "MB" {
		t.Fatalf("memory_peak evidence = %+v", e)
	}
}

// A code the explanation cites is a promise that the catalog explains it;
// a code that is not there sends a reader to an anchor that does not exist.
func TestEveryProblemCodeAnExplanationCitesIsInTheCatalog(t *testing.T) {
	cat, err := catalog.Current()
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, e := range cat.Entries {
		known[e.ID] = true
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range whyCases(now) {
		if v := classify(tc.snap); v.ProblemCode != "" && !known[v.ProblemCode] {
			t.Errorf("%s cites %s, which is not in the catalog", tc.name, v.ProblemCode)
		}
	}
}
