package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// faultTotals reads the split counter back through the registry, the way a
// scrape would, keyed by domain and category.
func (h *harness) faultTotals() map[string]float64 {
	h.t.Helper()
	families, err := h.c.metrics.reg.Gather()
	if err != nil {
		h.t.Fatalf("Gather: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range families {
		name := mf.GetName()
		if name != "zoomies_job_failures_total" && name != "zoomies_runner_start_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			key := name
			for _, l := range m.GetLabel() {
				if l.GetName() == "domain" || l.GetName() == "fault" {
					key += "|" + l.GetValue()
				}
			}
			out[key] += m.GetCounter().GetValue()
		}
	}
	return out
}

// The category is the whole point: "the runner died" is a bad afternoon, and
// "the runner died because it ran out of memory" is a number somebody can
// raise. The exit code is the evidence and only the agent has it, so the
// classification has to survive the trip.
func TestAnExitCodeReachesTheJobAsACategoryAndAFix(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 9101, labels)

	err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, State: store.RunnerFailed, ExitCode: 137,
		Message:    "runner exited with code 137: the container was killed for exceeding its memory limit",
		Fault:      store.FaultOutOfMemory,
		ObservedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}

	after, err := h.st.GetJob(h.ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if after.FaultKind != store.FaultOutOfMemory {
		t.Fatalf("fault kind = %q, want out_of_memory: the agent said so and nothing above it should have second-guessed it", after.FaultKind)
	}
	if after.FaultDomain() != store.FaultDomainFleet {
		t.Fatalf("fault domain = %q, want fleet", after.FaultDomain())
	}
	// The runner row carries it too, because the runner's page is where
	// somebody goes next and it must not have to ask the job.
	failed := h.runnerByID(t, r.ID)
	if failed.FaultKind != store.FaultOutOfMemory {
		t.Fatalf("the runner row's fault kind = %q, want out_of_memory", failed.FaultKind)
	}

	// The explanation offers the category's own remedy rather than the generic
	// "go and look", which is the difference between being told the fleet
	// broke the job and being told what to change.
	why, err := h.c.ExplainJob(h.ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("ExplainJob: %v", err)
	}
	if !strings.Contains(why.Fix, "memory limit") {
		t.Fatalf("explanation fix = %q, want the memory limit named", why.Fix)
	}

	totals := h.faultTotals()
	if got := totals["zoomies_job_failures_total|fleet|out_of_memory"]; got != 1 {
		t.Fatalf("fleet/out_of_memory failures = %v, want 1 (all: %v)", got, totals)
	}
}

// An agent older than the category field sends none. The controller must read
// that as "we could not narrow it" rather than as "nothing is wrong": the
// second would hand the failure back to a workflow that did nothing.
func TestAFaultWithNoCategoryIsUnclassifiedRatherThanAbsent(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 9102, labels)

	err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, State: store.RunnerFailed, ExitCode: 1,
		Message: "runner exited with code 1", ObservedAt: time.Now(),
	}})
	if err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}
	after, err := h.st.GetJob(h.ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if after.FaultKind != store.FaultRunnerExited {
		t.Fatalf("fault kind = %q, want runner_exited for a report that carried none", after.FaultKind)
	}
	if after.FaultDomain() != store.FaultDomainFleet {
		t.Fatalf("fault domain = %q, want fleet: an uncategorised fault is still ours", after.FaultDomain())
	}
}

// A job GitHub failed with nothing wrong on this side belongs to the workflow,
// and the fleet must say so plainly. This is the half that used to be
// indistinguishable from the half above.
func TestAWorkflowFailureIsNotCountedAgainstTheFleet(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 9103, labels)
	h.deliverJob(jobEvent{Action: "completed", JobID: 9103, Name: "test", Workflow: "CI",
		Labels: labels, RunnerName: r.Name, Conclusion: "failure"})

	after, err := h.st.GetJob(h.ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !after.Failed() {
		t.Fatal("the job did not count as failed")
	}
	if after.FaultDomain() != store.FaultDomainWorkflow {
		t.Fatalf("fault domain = %q, want workflow", after.FaultDomain())
	}
	if after.FleetFailed() {
		t.Fatal("a test failure was recorded as the fleet's")
	}

	stats, err := h.c.Stats(h.ctx, time.Hour)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Failed != 1 || stats.FleetFailed != 0 {
		t.Fatalf("failed = %d, of them ours = %d; want 1 and 0", stats.Failed, stats.FleetFailed)
	}

	// And the two filters are genuinely opposite halves of one list.
	ours, n, err := h.st.ListJobs(h.ctx, store.JobFilter{FaultedOnly: true}, store.Page{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if n != 0 {
		t.Fatalf("faulted jobs = %d (%+v), want none", n, ours)
	}
	theirs, n, err := h.st.ListJobs(h.ctx, store.JobFilter{WorkflowFailedOnly: true}, store.Page{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if n != 1 || theirs[0].ID != job.ID {
		t.Fatalf("workflow failures = %d (%+v), want the one job", n, theirs)
	}
}

// failPoolRunner plays a pool that cannot start a container: a runner is
// placed, the create fails on the host, and it never registers. The second
// reconcile is what makes the scheduler see it, which is where the pool's own
// "my runners keep failing" sentence comes from.
func failPoolRunner(t *testing.T, h *harness, kind store.FaultKind, message string) *store.Runner {
	t.Helper()
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var starting *store.Runner
	for _, r := range h.runners() {
		if r.State == store.RunnerProvisioning || r.State == store.RunnerRegistering {
			starting = r
			break
		}
	}
	if starting == nil {
		t.Fatal("the scheduler placed no runner to fail")
	}
	if err := h.c.failRunnerID(h.ctx, starting.ID, message, kind); err != nil {
		t.Fatalf("failRunnerID: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile after the failure: %v", err)
	}
	return h.runnerByID(t, starting.ID)
}

// The failure that reached nothing. A runner that dies before it registers has
// no job to be recorded on, so from the Jobs page a pool whose containers will
// not start looked exactly like a pool that was merely busy -- and the
// operator watching a queue that never moved had nowhere to find out which.
func TestAFailedRunnerStartReachesTheJobsWaitingForIt(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	h.deliverJob(jobEvent{Action: "queued", JobID: 9201, Name: "test", Workflow: "CI", Labels: labels})
	job, err := h.st.GetJobByGitHubID(h.ctx, 9201)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}

	failed := failPoolRunner(t, h, store.FaultImage,
		"the docker backend could not create runner zoomies-x: pulling ghcr.io/acme/runner:v9: manifest unknown")
	if failed.FaultKind != store.FaultImage {
		t.Fatalf("the failed runner's category = %q, want image", failed.FaultKind)
	}

	// The job is told, and deliberately not failed: it is still queued, the
	// next runner may well run it, and concluding a job GitHub still has open
	// would be the fleet inventing an outcome.
	again, err := h.st.GetJob(h.ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if again.State != store.JobQueued || again.Failed() {
		t.Fatalf("the job was moved by a failed runner start: state %q, failed %v", again.State, again.Failed())
	}
	events := h.timeline(job.ID)
	last := events[len(events)-1]
	if last.Kind != store.JobEventRunnerStartFailed {
		t.Fatalf("last timeline entry = %v, want runner_start_failed", kindsOfEvents(events))
	}
	if !strings.Contains(last.Message, "manifest unknown") {
		t.Fatalf("the entry dropped the backend's own words: %q", last.Message)
	}

	// A pool stuck in a start loop tries again every pass. One entry per job
	// per run of failures: a timeline is opened to avoid reading a log file,
	// not to read one. The report is replayed directly because the scheduler
	// has, correctly, stopped placing runners for this pool -- which is the
	// state the guard has to survive.
	before := *failed
	before.State = store.RunnerProvisioning
	h.c.noteRunnerStartFailure(h.ctx, &before, failed)
	if got := h.timeline(job.ID); len(got) != len(events) {
		t.Fatalf("a second failed start added another entry: %v", kindsOfEvents(got))
	}
}

// Two failed starts is a pool that will not recover on its own, and everything
// that looks at the fleet has to say so: the problems drawer with the category's
// own remedy, and the job's own page instead of the cheerful "a runner is on
// its way" that was equally true of a pool that had stopped being able to
// produce one.
func TestAPoolThatCannotStartARunnerSaysSoEverywhere(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	h.deliverJob(jobEvent{Action: "queued", JobID: 9202, Name: "test", Workflow: "CI", Labels: labels})
	job, err := h.st.GetJobByGitHubID(h.ctx, 9202)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}

	failPoolRunner(t, h, store.FaultBackend,
		"the docker backend would not answer: dial unix /var/run/docker.sock: no such file")

	// The problems drawer, with the category's own remedy rather than the list
	// of three possibilities it used to offer every pool. Being handed three
	// when the fleet already knows which one it is is how an operator learns
	// not to read the fix line.
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var failing *Problem
	for i := range ps {
		if ps[i].Code == "pool.runners_failing" {
			failing = &ps[i]
		}
	}
	if failing == nil {
		t.Fatalf("problems = %v, want pool.runners_failing", h.problemCodes())
	}
	if !strings.Contains(failing.Fix, "container backend") {
		t.Fatalf("the problem's fix = %q, want the backend category's own remedy", failing.Fix)
	}

	// And the job's own page.
	why, err := h.c.ExplainJob(h.ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("ExplainJob: %v", err)
	}
	if !why.Blocked || !strings.Contains(why.Summary, "failing to start") {
		t.Fatalf("explanation = %+v, want a blocked job whose pool cannot start a runner", why)
	}
	if !strings.Contains(why.Fix, "container backend") {
		t.Fatalf("explanation fix = %q, want the backend category's own remedy", why.Fix)
	}

	// None of this reaches a job count, which is the blind spot the runner
	// counter exists to cover: the job is still queued and nothing is failed.
	stats, err := h.c.Stats(h.ctx, time.Hour)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("failed jobs = %d, want 0: the job is still queued", stats.Failed)
	}
	if stats.RunnerStartFaults[store.FaultBackend] != 1 {
		t.Fatalf("runner start faults = %v, want one under backend", stats.RunnerStartFaults)
	}
}

// The one thing Zoomies can do about a failure it caused. It is offered on the
// strength of the category and allowed on the strength of the failure, which
// are deliberately different questions: an operator who has looked at a failure
// and decided to run it again is entitled to, and a button that refused on a
// judgement Zoomies made about blame is a button people route around.
func TestRerunAsksGitHubOnlyForAJobThatFailed(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	job, r := startJobOnRunner(t, h, host.ID, 9301, labels)

	// A job still running has nothing to run again.
	if _, err := h.c.RerunJobWorkflow(h.ctx, job.ID); !errors.Is(err, ErrJobNotFinished) {
		t.Fatalf("re-running a running job = %v, want ErrJobNotFinished", err)
	}
	if n := h.gh.Reruns() + h.gh.JobReruns(); n != 0 {
		t.Fatalf("GitHub was asked %d times about a job that had not finished", n)
	}

	// A job that succeeded is the other refusal, and a different sentence:
	// GitHub only reruns the failed jobs of a run.
	h.deliverJob(jobEvent{Action: "completed", JobID: 9301, RunID: 4301, Name: "test", Workflow: "CI",
		Labels: labels, RunnerName: r.Name, Conclusion: "success"})
	if _, err := h.c.RerunJobWorkflow(h.ctx, job.ID); !errors.Is(err, ErrJobDidNotFail) {
		t.Fatalf("re-running a successful job = %v, want ErrJobDidNotFail", err)
	}
	if n := h.gh.Reruns() + h.gh.JobReruns(); n != 0 {
		t.Fatalf("GitHub was asked %d times about a job that succeeded", n)
	}

	// A workflow's own failure: allowed, because whose fault it was is not
	// this call's question.
	h.deliverJob(jobEvent{Action: "queued", JobID: 9302, RunID: 4302, Name: "test", Workflow: "CI", Labels: labels})
	h.deliverJob(jobEvent{Action: "completed", JobID: 9302, RunID: 4302, Name: "test", Workflow: "CI",
		Labels: labels, Conclusion: "failure", Steps: failingSteps()})
	theirs, err := h.st.GetJobByGitHubID(h.ctx, 9302)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if _, err := h.c.RerunJobWorkflow(h.ctx, theirs.ID); err != nil {
		t.Fatalf("RerunJobWorkflow on a workflow failure: %v", err)
	}
	// Job-level, because this job has a GitHub job ID: the operator asked
	// about one job and gets that job, not every failure sharing its run.
	if n := h.gh.JobReruns(); n != 1 {
		t.Fatalf("GitHub was asked for %d job re-runs, want one", n)
	}
	if n := h.gh.Reruns(); n != 0 {
		t.Fatalf("the run-level re-run was used %d times for a job that has its own ID; that re-runs every failure in the run", n)
	}

	// Nothing local is written beyond the record that somebody asked: the
	// re-run arrives as new deliveries with a higher run attempt, and an
	// outcome invented here would be the fleet answering for GitHub.
	after, err := h.st.GetJob(h.ctx, theirs.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if after.Conclusion != "failure" || after.State != store.JobCompleted {
		t.Fatalf("the re-run changed the local row: %q/%q", after.State, after.Conclusion)
	}
	events := h.timeline(theirs.ID)
	last := events[len(events)-1]
	if last.Kind != store.JobEventRerunRequested {
		t.Fatalf("last timeline entry = %v, want rerun_requested", kindsOfEvents(events))
	}
	// The blast radius is still said out loud, it is just smaller now: the
	// job, and whatever declares it in `needs`. An operator reading the
	// timeline should not have to go to GitHub to find out what else ran.
	if !strings.Contains(last.Message, "any job that needs it") {
		t.Fatalf("the entry does not say what else goes with it: %q", last.Message)
	}
}

// A job whose GitHub job ID this fleet never recorded -- an old row, or one
// whose workflow_job delivery never arrived -- still gets its button. GitHub's
// job-level re-run needs that ID, so the wider run-level call is the fallback,
// and the timeline says so rather than leaving an operator to discover from
// the billing page that three other jobs ran again.
func TestRerunFallsBackToTheRunWhenAJobHasNoGitHubID(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	_ = host

	// Written straight to the store with no GitHub job ID, which is the state
	// this path exists for: a row from before the fleet recorded one, or a job
	// whose workflow_job delivery never arrived.
	inst, err := h.st.ListInstallations(h.ctx)
	if err != nil || len(inst) == 0 {
		t.Fatalf("the harness has no installation to attribute the job to: %v", err)
	}
	job, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 0, GitHubRunID: 4401, Repo: "acme/widgets", InstallationID: inst[0].ID,
		JobName: "test", Workflow: "CI", Labels: labels,
		State: store.JobCompleted, Conclusion: "failure",
	})
	if err != nil {
		t.Fatalf("seeding a job with no GitHub job ID: %v", err)
	}

	if _, err := h.c.RerunJobWorkflow(h.ctx, job.ID); err != nil {
		t.Fatalf("RerunJobWorkflow: %v", err)
	}
	if n := h.gh.Reruns(); n != 1 {
		t.Errorf("the run-level re-run was used %d times, want once: it is the only one that works without a job ID", n)
	}
	if n := h.gh.JobReruns(); n != 0 {
		t.Errorf("a job-level re-run was attempted %d times with no job ID to name", n)
	}

	events := h.timeline(job.ID)
	last := events[len(events)-1]
	if !strings.Contains(last.Message, "failed jobs") {
		t.Errorf("the entry does not say the whole run's failures went with it: %q", last.Message)
	}
}

// Auto-recovery is the same call the button makes, decided by a setting rather
// than by a person. What matters is that it is narrow: the fleet's own
// failures, bounded by GitHub's attempt number, and nothing at all while the
// setting is off -- which is the default, because each one of these spends
// somebody's GitHub minutes.
func TestAJobTheFleetBrokeIsReRunOnlyWhenTheSettingSaysSo(t *testing.T) {
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	for _, tc := range []struct {
		name       string
		on         bool
		limit      int
		attempt    int
		fleetFault bool
		want       int
	}{
		{"off by default", false, 1, 1, true, 0},
		{"on, first attempt, the fleet's fault", true, 1, 1, true, 1},
		{"on, but the workflow's own failure", true, 1, 1, false, 0},
		{"on, but this run has had its re-run", true, 1, 2, true, 0},
		{"a higher limit lets a second attempt through", true, 2, 2, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, _, host := h.fleet()
			h.c.live.Update(func(c *config.Config) {
				c.Scheduler.AutoRerun, c.Scheduler.AutoRerunLimit = tc.on, tc.limit
			})

			job, r := startJobOnRunner(t, h, host.ID, 9501, labels)
			if tc.fleetFault {
				// The order the fleet actually sees it in: the runner goes
				// first, GitHub reports the job over afterwards.
				before, err := h.st.GetRunner(h.ctx, r.ID)
				if err != nil {
					t.Fatalf("GetRunner: %v", err)
				}
				h.c.noteRunnerLost(h.ctx, before, sourceAgent,
					"runner exited with code 137: the container was killed for exceeding its memory limit",
					store.FaultOutOfMemory)
			}
			h.deliverJob(jobEvent{Action: "completed", JobID: 9501, RunID: 4501, Name: "test", Workflow: "CI",
				Labels: labels, RunnerName: r.Name, Conclusion: "failure", RunAttempt: tc.attempt,
				Steps: failingSteps()})

			if n := h.gh.JobReruns(); n != tc.want {
				t.Fatalf("GitHub was asked to re-run %d times, want %d", n, tc.want)
			}
			if tc.want == 0 {
				return
			}
			// The timeline says the fleet asked, not an operator: somebody
			// reading it must not go looking for the colleague who pressed a
			// button nobody pressed.
			events := h.timeline(job.ID)
			last := events[len(events)-1]
			if last.Kind != store.JobEventRerunRequested || last.Source != sourceRecovery {
				t.Fatalf("last entry = %s via %s, want a re-run via recovery", last.Kind, last.Source)
			}
			if !strings.Contains(last.Message, "scheduler.auto_rerun") {
				t.Errorf("the entry does not name the setting that did it: %q", last.Message)
			}
		})
	}
}

// A duplicate delivery must not buy a second re-run. GitHub delivers at least
// once, and a completion that arrives twice for a job already completed is not
// a state change -- so it never reaches the re-run. This pins that, because
// the cost of getting it wrong is a bill rather than a wrong pixel.
func TestARepeatedCompletionDoesNotReRunTwice(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	labels := []string{"self-hosted", "linux", "x64", "demo"}
	h.c.live.Update(func(c *config.Config) {
		c.Scheduler.AutoRerun, c.Scheduler.AutoRerunLimit = true, 3
	})

	_, r := startJobOnRunner(t, h, host.ID, 9601, labels)
	before, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	h.c.noteRunnerLost(h.ctx, before, sourceAgent, "the runner stopped", store.FaultRunnerExited)

	done := jobEvent{Action: "completed", JobID: 9601, RunID: 4601, Name: "test", Workflow: "CI",
		Labels: labels, RunnerName: r.Name, Conclusion: "failure", RunAttempt: 1, Steps: failingSteps()}
	h.deliverJob(done)
	h.deliverJob(done)
	h.deliverJob(done)

	if n := h.gh.JobReruns(); n != 1 {
		t.Fatalf("GitHub was asked to re-run %d times for three deliveries of one completion, want once", n)
	}
}
