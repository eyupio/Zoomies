package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// JobExplanation is the answer to "why is this job not running?", computed
// once, on the side that knows.
//
// The browser used to work this out for itself from the pool's live counts, and
// the CLI could only say "unmatched": two answers to one question, neither of
// which could see the scheduler's own reason for refusing to place a runner.
// This is that reason plus the four facts around it -- the last plan, the
// pool's warnings, the runner's stamps and its host's heartbeat -- so the
// drawer, the CLI and the support bundle read from one place and cannot
// disagree.
type JobExplanation struct {
	JobID string         `json:"job_id"`
	State store.JobState `json:"state"`
	// Summary is the sentence. It is always set, including for a job that is
	// running or finished: "nothing is wrong" is an answer to the question, and
	// a caller that has to special-case an empty string will print nothing on
	// the one page an operator opened to be told something.
	Summary string `json:"summary"`
	// Detail is what the summary leaves out, in the scheduler's own words
	// where it has any.
	Detail string `json:"detail,omitempty"`
	// Fix is what to do. Empty means there is nothing to do, which is the
	// normal case and is different from not knowing.
	Fix string `json:"fix,omitempty"`
	// Waiting is whether the job is still waiting on something. Blocked is
	// whether waiting will not on its own end it: a fleet that is merely busy
	// clears, and a pool nothing can place never will.
	Waiting bool `json:"waiting"`
	Blocked bool `json:"blocked"`
	// PoolID, RunnerID and HostID are what the explanation is about, so a
	// caller can link to them rather than parse them back out of the prose.
	PoolID     string    `json:"pool_id,omitempty"`
	RunnerID   string    `json:"runner_id,omitempty"`
	HostID     string    `json:"host_id,omitempty"`
	ComputedAt time.Time `json:"computed_at"`

	// Class is the same answer as the summary, in a form a caller can switch on.
	// It is always set: a job the explainer cannot narrow is ClassUnknown, and
	// says what was missing, rather than having no class.
	Class JobClass `json:"class"`
	// Confidence is how far to trust Class, and ConfidenceReason says what is
	// missing whenever it is not high.
	Confidence       Confidence `json:"confidence"`
	ConfidenceReason string     `json:"confidence_reason,omitempty"`
	// Evidence is what the explanation rests on, as facts a person can check.
	Evidence []Evidence `json:"evidence"`
	// ProblemCode and CheckCode name the catalog entry that says more, when one
	// is true of every job in the class. The catalog is catalog.json.
	ProblemCode string `json:"problem_code,omitempty"`
	CheckCode   string `json:"check_code,omitempty"`
	// NextSteps are what to do, in order. The first is Fix when there is one.
	NextSteps []NextStep `json:"next_steps"`
}

// ExplainJob works out why a job is where it is.
//
// It reads rather than decides: the scheduler has already said why it could not
// place a runner, and repeating that decision here would give an operator two
// answers that drift apart. What this adds is the surrounding facts, which the
// plan does not carry -- whose runner it is, and whether that runner's host is
// still alive.
func (c *Controller) ExplainJob(ctx context.Context, jobID string) (*JobExplanation, error) {
	job, err := c.st.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := &JobExplanation{
		JobID:      job.ID,
		State:      job.State,
		PoolID:     job.PoolID,
		RunnerID:   job.RunnerID,
		ComputedAt: c.Now(),
	}
	c.explain(ctx, job, out)
	out.finish(job)
	return out, nil
}

// explain is the half of ExplainJob that has an opinion, split off so that every
// way out of it passes through finish and none can skip it.
func (c *Controller) explain(ctx context.Context, job *store.Job, out *JobExplanation) {
	switch job.State {
	case store.JobCompleted:
		c.explainCompleted(ctx, job, out)
		return
	case store.JobInProgress:
		// Ahead of the running explanation, which would otherwise name a
		// runner this fleet took away when the cancellation landed.
		if job.Cancelling() {
			explainCancelling(job, out)
			return
		}
		c.explainRunning(ctx, job, out)
		return
	case store.JobWaiting:
		// Not this fleet's wait at all, and saying so is the point: a job
		// sitting still reads as a slow fleet until somebody says otherwise.
		out.Waiting = true
		out.Summary = "GitHub is holding this job for a deployment review."
		out.Detail = "Nothing here can start it until somebody approves it. The wait for a runner begins when they do, so the queue wait below has not started."
		out.Fix = "approve the deployment on GitHub, or leave it -- nothing in this fleet is wrong."
		out.classify(ClassHeldByGitHub, ConfidenceHigh, "")
		out.fact(EvidenceConclusion, "GitHub's state", string(job.State))
		return
	}

	out.Waiting = true
	if wait, ok := queueWait(job, c.Now()); ok {
		out.number(EvidenceQueueWait, "Has waited for a runner", int64(wait.Seconds()), "s")
	}
	c.where(ctx, job, out)
	c.explainQueued(ctx, job, out)
}

// where adds the pool, runner and host a job is tied to, each with the page that
// shows it. They are the places an operator goes next, so they are evidence and
// not decoration.
func (c *Controller) where(ctx context.Context, job *store.Job, out *JobExplanation) {
	if job.PoolID != "" {
		name := job.PoolID
		if pool, err := c.st.GetPool(ctx, job.PoolID); err == nil {
			name = pool.Name
		}
		out.thing(EvidencePool, "Pool", name, "/pools/"+job.PoolID)
	}
	if job.RunnerID != "" {
		name := job.RunnerName
		if name == "" {
			name = job.RunnerID
		}
		out.thing(EvidenceRunner, "Runner", name, "/runners/"+job.RunnerID)
	}
	if job.HostID != "" {
		name := job.HostID
		if host, err := c.st.GetHost(ctx, job.HostID); err == nil {
			name = host.Name
		}
		out.thing(EvidenceHost, "Host", name, "/hosts/"+job.HostID)
	}
}

func (c *Controller) explainCompleted(ctx context.Context, job *store.Job, out *JobExplanation) {
	defer explainOOM(job, out)
	if wait, ok := queueWait(job, c.Now()); ok {
		out.number(EvidenceQueueWait, "Waited for a runner", int64(wait.Seconds()), "s")
	}
	if ran, ok := ranFor(job, c.Now()); ok {
		out.number(EvidenceDuration, "Ran for", int64(ran.Seconds()), "s")
	}
	if job.Conclusion != "" {
		out.fact(EvidenceConclusion, "GitHub's conclusion", job.Conclusion)
	}
	c.where(ctx, job, out)
	switch {
	case job.FleetFailed():
		out.Summary = "The runner this job was on stopped before the job finished."
		out.Detail = job.RunnerFault
		// The category's own remedy where there is one. It is the difference
		// between being told the fleet broke the job and being told what to go
		// and change, and the second is why anybody opened this page.
		out.Fix = job.FaultKind.Fix()
		if out.Fix == "" {
			out.Fix = "this is the fleet's failure rather than the workflow's: the runner's page has what it said as it went."
		}
		classifyFault(job, out)
	case store.IsFailedConclusion(job.Conclusion):
		out.Summary = "This job ran and " + job.Conclusion + "."
		out.Detail = "The fleet did its part: a conclusion is the workflow's own outcome."
		if step := job.FailedStep(); step != nil {
			out.outside(EvidenceStep, "Step it stopped at", step.Name)
		}
		if job.Conclusion == "timed_out" {
			out.Detail = "The fleet did its part: GitHub stopped this job at its time limit, which is the workflow's own."
			out.Fix = "open the step the job was in when the limit was reached; raise the job's timeout-minutes if the work needs longer, or fix what hung."
			out.classify(ClassTimeout, ConfidenceHigh, "")
			break
		}
		out.classify(ClassWorkflowFailure, ConfidenceHigh, "")
	case job.Conclusion == "cancelled":
		out.Summary = "This job ran and " + job.Conclusion + "."
		out.Detail = "Somebody or something cancelled it on GitHub; the fleet did not."
		out.classify(ClassCancelled, ConfidenceHigh, "")
	case job.Conclusion == "success" || job.Conclusion == "skipped" || job.Conclusion == "neutral":
		out.Summary = "This job ran and " + job.Conclusion + "."
		out.classify(ClassSucceeded, ConfidenceHigh, "")
	default:
		out.Summary = "This job ran and " + job.Conclusion + "."
		// Stale is Zoomies' own word for a job GitHub stopped talking about, and the
		// others are conclusions this build has no rule for. Either way the honest
		// class is the one that says so.
		out.classify(ClassUnknown, ConfidenceLow, "GitHub's conclusion for this job, "+quoteConclusion(job.Conclusion)+", is not one the explainer has a rule for, and the fleet recorded no fault against it")
	}
}

// quoteConclusion says a conclusion in a sentence, including the empty one.
func quoteConclusion(conclusion string) string {
	if conclusion == "" {
		return "nothing yet"
	}
	return `"` + conclusion + `"`
}

// classifyFault classes a job the fleet itself failed, from the closed set of
// fault kinds. The kinds already carry the distinction that matters, and this
// only names it for a caller that switches on a value.
func classifyFault(job *store.Job, out *JobExplanation) {
	kind := job.FaultKind.Normalise()
	if kind != "" {
		out.fact(EvidenceFault, "Fault the fleet recorded", string(kind))
	}
	if job.RunnerFault != "" {
		// What a runner printed as it failed can carry text from anywhere: an
		// image's name, a registry's reply, a step's own output.
		out.outside(EvidenceFaultDetail, "What the runner said", job.RunnerFault)
	}
	switch kind {
	case store.FaultOutOfMemory:
		out.classify(ClassOOM, ConfidenceHigh, "")
	case store.FaultHostLost:
		out.classify(ClassHostLost, ConfidenceHigh, "")
	case store.FaultOutOfDisk:
		out.classify(ClassDisk, ConfidenceHigh, "")
	case store.FaultRemoved:
		out.classify(ClassCancelled, ConfidenceHigh, "")
	case store.FaultImage, store.FaultRegistration, store.FaultBackend, store.FaultBackendBusy,
		store.FaultContainerConflict, store.FaultConfig:
		out.classify(ClassRunnerStartupFailure, ConfidenceHigh, "")
	case store.FaultRunnerExited:
		out.classify(ClassUnknown, ConfidenceLow, "the runner stopped and the agent could not narrow why, so the fleet recorded no more than that")
	default:
		out.classify(ClassUnknown, ConfidenceLow, "the fleet recorded a message for the runner's failure but no fault kind, which is what a job from before fault kinds looks like")
	}
}

// explainOOM adds an out-of-memory kill to a finished job's explanation. A
// step killed with exit 137 can leave a runner that finishes the job, and
// GitHub then reports a failed step like any test failure; the kill is the
// part the workflow's author cannot see, so it is said whatever else was.
func explainOOM(job *store.Job, out *JobExplanation) {
	if !job.OOMKilled {
		return
	}
	peak := ""
	if job.PeakMemoryMB > 0 {
		peak = fmt.Sprintf(", having used up to %s", formatJobMB(job.PeakMemoryMB))
	}
	where := ""
	if job.HostID != "" {
		where = " on host " + job.HostID
	}
	sentence := fmt.Sprintf("The kernel killed this job's runner, or one of its steps, for its memory limit%s%s.", where, peak)
	// The kill is the summary: it is the one fact about this job the
	// conclusion GitHub recorded cannot tell anybody.
	out.Summary = sentence
	out.Fix = store.FaultOutOfMemory.Fix() + " With scheduler.history_sizing set to on, the next run of this job is placed on a host with room for what it needed."
	// A kill is the cause whatever the conclusion says, so it outranks a class the
	// rest of the explanation reached from GitHub's side of the story.
	out.classify(ClassOOM, ConfidenceHigh, "")
	if !out.has(EvidenceFault) {
		out.fact(EvidenceFault, "Fault the fleet recorded", string(store.FaultOutOfMemory))
	}
	if job.PeakMemoryMB > 0 {
		out.number(EvidenceMemoryPeak, "Most memory it was measured using", job.PeakMemoryMB, "MB")
	}
	if job.GrantedMemoryMB > 0 {
		out.number(EvidenceMemoryLimit, "Memory it was given", job.GrantedMemoryMB, "MB")
	}
}

// sizeSentence says how a job was classed and where it was sent, as part of an
// explanation of why it is where it is.
func sizeSentence(job *store.Job) string {
	out := fmt.Sprintf("It is in the %s class because %s.", job.SizeClass, job.SizeReason)
	switch {
	case job.RoutedClass == "":
		out += " Size routing is only watching, so nothing was sent anywhere on its account."
	case job.RoutedNote != "":
		out += fmt.Sprintf(" It was sent to the %s class: %s.", job.RoutedClass, job.RoutedNote)
	case job.SizeBasis != store.SizeBasisExplicit:
		out += fmt.Sprintf(" It is routed to the %s class on a best-effort basis: GitHub decides which waiting job a runner takes.", job.RoutedClass)
	}
	return out
}

// formatJobMB says a memory figure the way the rest of the explanation does.
func formatJobMB(mb int64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func (c *Controller) explainRunning(ctx context.Context, job *store.Job, out *JobExplanation) {
	// A runner that died under a job is recorded the moment the fleet sees it,
	// which is often minutes before GitHub closes the job. Until it does, this
	// is the function that answers -- and it used to answer "this job is
	// running on zoomies-x", about a runner the fleet had already written off.
	if ran, ok := ranFor(job, c.Now()); ok {
		out.number(EvidenceDuration, "Has run for", int64(ran.Seconds()), "s")
	}
	c.where(ctx, job, out)
	if job.FleetFailed() {
		out.Blocked = true
		out.Summary = "The runner this job was on has stopped, and GitHub has not noticed yet."
		out.Detail = job.RunnerFault + ". GitHub will report the job failed once the runner's absence is noticed, and it will look like any other failure."
		out.Fix = job.FaultKind.Fix()
		if out.Fix == "" {
			out.Fix = "this is the fleet's failure rather than the workflow's: the runner's page has what it said as it went."
		}
		classifyFault(job, out)
		return
	}
	out.Summary = "This job is running."
	if job.RunnerName != "" {
		out.Summary = "This job is running on " + job.RunnerName + "."
	}
	out.classify(ClassRunning, ConfidenceHigh, "")
	// A runner whose host has gone quiet is the case worth catching here: the
	// job looks healthy and nothing is watching it.
	if job.RunnerID == "" {
		return
	}
	runner, err := c.st.GetRunner(ctx, job.RunnerID)
	if err != nil {
		return
	}
	out.HostID = runner.HostID
	host, err := c.st.GetHost(ctx, runner.HostID)
	if err != nil || host.Healthy(c.Now()) {
		return
	}
	out.Blocked = true
	out.Summary = "This job is on a runner whose host has gone quiet."
	out.Detail = fmt.Sprintf("%s last checked in %s ago, and a host silent for %s is presumed gone -- the job will be marked as lost by the fleet.",
		naming.ForSentence(host.Name), formatAge(c.Now().Sub(host.LastHeartbeat)), store.HeartbeatTimeout)
	out.Fix = "check that the zoomies agent is running on that host and can reach this controller."
	// The host has been quiet, and nothing says the job has stopped: the fleet
	// presumes it gone, which is a reading of silence and not a recorded fault.
	out.classify(ClassHostLost, ConfidenceMedium, "the host has been silent for longer than the heartbeat timeout, but nothing has reported the job stopped")
	out.thing(EvidenceHeartbeat, "Host last checked in", formatAge(c.Now().Sub(host.LastHeartbeat))+" ago", "/hosts/"+host.ID)
}

// explainCancelling covers the window between GitHub accepting a cancellation
// and GitHub reporting the job over, which can be minutes. The fleet has
// already stopped: there is nothing for an operator to do but wait, and the
// one thing they must not be told is that the job is still queued or running.
func explainCancelling(job *store.Job, out *JobExplanation) {
	out.Blocked = true
	out.Summary = "This job's workflow run was cancelled."
	out.Detail = "GitHub accepted the cancellation; this fleet stopped its queued demand and any runner working on it at that moment. " +
		"GitHub reports the conclusion in its own time, and until it does the job is recorded as neither waiting nor running."
	out.Fix = "nothing here: the conclusion arrives with GitHub's completion event. Re-run the workflow on GitHub if the work is still wanted."
	out.classify(ClassCancelled, ConfidenceHigh, "")
	if job.CancelRequestedAt != nil {
		out.fact(EvidenceConclusion, "Cancellation accepted by GitHub", job.CancelRequestedAt.UTC().Format(time.RFC3339))
	}
}

// explainQueued is the case the endpoint exists for.
func (c *Controller) explainQueued(ctx context.Context, job *store.Job, out *JobExplanation) {
	// Before the provisioning branch: a cancelled run pauses its queued jobs,
	// so an operator who cancelled one would otherwise be told their own
	// cancellation was a pause, and offered a Resume that would undo it.
	if job.Cancelling() {
		explainCancelling(job, out)
		return
	}
	if job.Provisioning != "" {
		out.Blocked = true
		out.Summary = "Provisioning is paused for this item."
		if job.Provisioning == "deleted" {
			out.Summary = "This item was deleted from the provisioning queue."
		}
		out.Detail = "It contributes no new runner demand. Existing runners and the pool's warm capacity can still pick up the GitHub job."
		out.Fix = "Resume the item in Queue to restore its provisioning demand."
		out.classify(ClassQueuedBlocked, ConfidenceHigh, "")
		out.fact(EvidenceScheduler, "Provisioning for this item", job.Provisioning)
		return
	}
	plan, planAt := c.getLastPlan()

	// Nothing claims it. The scheduler records why when the reason is not the
	// obvious one, and the obvious one still needs saying.
	if !job.Matched {
		out.Blocked = true
		out.Summary = "No pool in this fleet claims this job."
		out.Detail = fmt.Sprintf("It asks for [%s], and no enabled pool advertises those labels for the installation covering %s.",
			joinLabels(job.Labels), job.Repo)
		reasoned := false
		if plan != nil {
			for _, u := range plan.Unmatched {
				if u.Job != nil && u.Job.ID == job.ID && u.Reason != "" {
					out.Detail, reasoned = u.Reason, true
					break
				}
			}
		}
		out.Fix = "create or enable a pool advertising those labels, or change the workflow's runs-on. If another runner provider takes these jobs, nothing needs doing."
		out.classify(ClassQueuedUnmatched, ConfidenceHigh, "")
		// The labels are the workflow's own runs-on, which its author wrote.
		out.outside(EvidenceLabels, "Labels it asks for", joinLabels(job.Labels))
		// The scheduler's own reason is the one that is true of this job -- a pool
		// that carries the label does exist, on another installation -- and the
		// sentence about a class nobody answers would say the opposite.
		if class, ok := scheduler.RequestedClass(job.Labels); ok && !reasoned {
			// The label is this fleet's own, and a job that names it is not
			// waiting for another provider: it is waiting for a pool of a class.
			why, remedy := c.namedClassWhy(class)
			out.Detail = fmt.Sprintf("It asks for %s by name and no enabled pool carries that label: %s. A job that names its class is never moved to another.", class.Label(), why)
			out.Fix = remedy
		}
		return
	}

	pool, err := c.st.GetPool(ctx, job.PoolID)
	if err != nil {
		out.Summary = "A pool claimed this job, and that pool is no longer here."
		out.Fix = "the job will be re-matched on the next pass; if it is not, its labels no longer name a pool."
		out.classify(ClassUnknown, ConfidenceLow, "the pool that claimed this job has been deleted, so what it was waiting for is gone with it")
		return
	}
	out.Summary = "A runner is on its way for this job."
	out.Detail = "It is claimed by " + pool.Name + "."
	// Where the controller put it by size, and why, where it did: the class is
	// part of why this pool and not another claimed it. After every answer below,
	// because each of them sets the detail.
	defer func() {
		if job.SizeBasis != "" {
			out.Detail = strings.TrimSpace(out.Detail + " " + sizeSentence(job))
		}
	}()

	// The scheduler's own sentence for this pool, which is the one thing the
	// browser could never work out for itself: whether a runner can be placed
	// at all is a question about hosts, not about counts.
	if plan != nil {
		for _, pp := range plan.Pools {
			if pp.PoolID != pool.ID {
				continue
			}
			if pp.Blocked != "" {
				out.Blocked = true
				out.Summary = "The scheduler wants a runner for this job and cannot place one."
				out.Detail = pp.Blocked
				out.Fix = pp.BlockedFix
				if out.Fix == "" {
					out.Fix = "add a host, raise a host's capacity, uncordon one, or relax the pool's host selector."
				}
				out.classify(ClassQueuedBlocked, ConfidenceHigh, "")
				out.fact(EvidenceScheduler, "The scheduler's reason", pp.Blocked)
				return
			}
			// A pool whose runners keep dying before they register is the case
			// that read as a pool that was merely slow. It has to be said
			// before the counts below, because those describe a fleet working:
			// "a runner is starting for this job" is true of a pool on its
			// twentieth failed attempt, and saying it to somebody watching a
			// queue that has not moved in an hour is worse than saying
			// nothing.
			if pp.Failing != "" {
				out.Blocked = true
				out.Summary = pool.Name + " keeps failing to start a runner for this job."
				out.Detail = pp.Failing
				out.Fix = startFailureFix(pp.FailingFault)
				out.classify(ClassRunnerStartupFailure, ConfidenceHigh, "")
				out.fact(EvidenceScheduler, "The scheduler's reason", pp.Failing)
				return
			}
			// What the jobs waiting are known to need, and what that did to
			// where the runner goes -- or, in shadow, what it would have.
			if pp.History != "" {
				out.Detail += " Job history: " + pp.History + "."
			}
			if pp.HistoryUnfit != "" {
				out.Detail += " " + capitalise(pp.HistoryUnfit) + "."
			}
			break
		}
	}

	// Until the counts say more, the scheduler has not yet decided: the one answer
	// that is true of a claimed job nothing has blocked.
	out.classify(ClassQueuedCapacity, ConfidenceMedium, "the scheduler has not yet decided what to do for this job")
	counts, err := c.st.CountRunnersByPool(ctx)
	if err != nil {
		// The counts are the nicety; the claim above is the answer.
		return
	}
	pc := counts[pool.ID]
	switch {
	case pc.Idle > 0:
		out.Summary = fmt.Sprintf("%s idle in %s, so GitHub should hand this job over any moment.",
			runnersAre(pc.Idle), pool.Name)
		out.Detail = "Which runner takes it is GitHub's choice, not this fleet's."
		out.classify(ClassQueuedCapacity, ConfidenceMedium, "a runner is idle for it, and GitHub, not this fleet, decides which runner takes a job")
		out.number(EvidenceRunner, "Idle runners in the pool", int64(pc.Idle), "runners")
	case pc.Provisioning+pc.Registering > 0:
		out.Summary = fmt.Sprintf("%s starting for %s.", runnersAre(pc.Provisioning+pc.Registering), pool.Name)
		out.Detail = "The job goes to the first one GitHub sees."
		out.classify(ClassQueuedCapacity, ConfidenceHigh, "")
		out.number(EvidenceRunner, "Runners starting in the pool", int64(pc.Provisioning+pc.Registering), "runners")
	case pool.MaxRunners > 0 && pc.Live() >= pool.MaxRunners:
		out.Waiting = true
		out.Summary = fmt.Sprintf("%s is at its ceiling of %s, all busy.", pool.Name, plural(pool.MaxRunners, "runner"))
		out.Detail = "The job waits for one of them to finish."
		out.Fix = "raise this pool's max_runners if the fleet has room for more."
		out.classify(ClassQueuedCapacity, ConfidenceHigh, "")
		out.number(EvidenceRunner, "Runners in the pool, all busy", int64(pc.Live()), "runners")
		out.number(EvidencePool, "The pool's ceiling", int64(pool.MaxRunners), "runners")
	default:
		out.Summary = "No runner is free for this job yet, and none is starting."
		out.Detail = "The scheduler decides on its next pass."
		if !planAt.IsZero() {
			out.Detail = fmt.Sprintf("The scheduler last decided %s ago and will decide again on its next pass.",
				formatAge(c.Now().Sub(planAt)))
		}
	}
}

// runnersAre agrees the verb with the count, which the general plural helper
// cannot: it pluralises the noun it is given, and "1 runner is" pluralises to
// "2 runner iss".
func runnersAre(n int) string {
	if n == 1 {
		return "1 runner is"
	}
	return fmt.Sprintf("%d runners are", n)
}

func joinLabels(labels store.StringSlice) string {
	if len(labels) == 0 {
		return "no labels"
	}
	return strings.Join(labels, ", ")
}
