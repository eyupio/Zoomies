package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

var (
	ErrWorkflowCancellationDisabled = errors.New("workflow cancellation is disabled")
	ErrJobAlreadyCompleted          = errors.New("the job has already completed")
	// ErrJobNotFinished and ErrJobDidNotFail are the two ways a re-run is
	// refused for the job's own sake rather than for a missing permission, and
	// they are separate because the advice differs: one is "wait", the other is
	// "you are looking at the wrong job".
	ErrJobNotFinished = errors.New("the job has not finished")
	ErrJobDidNotFail  = errors.New("the job did not fail")
)

// CancelJobWorkflow asks GitHub to cancel the workflow run containing jobID.
// GitHub remains authoritative for terminal job states, but once its
// run-scoped API accepts the request Zoomies immediately suppresses queued
// demand and tears down runners executing jobs from that run. Waiting for a
// completed webhook would let an arbitrary current step keep running.
//
// It exists beside CancelWorkflowRun because the Jobs page and JobDrawer
// already have a job in hand, not a run; it resolves the job to its run and
// delegates. The job's own already-completed check stays here rather than
// moving to the run-scoped method, so asking to cancel through a job that has
// itself finished is still refused even when a sibling job has not.
func (c *Controller) CancelJobWorkflow(ctx context.Context, jobID string, force bool) (*store.Job, error) {
	j, err := c.st.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if j.State == store.JobCompleted {
		return nil, ErrJobAlreadyCompleted
	}
	if j.InstallationID == "" || j.Repo == "" || j.GitHubRunID <= 0 {
		return nil, fmt.Errorf("job %s has no GitHub installation and workflow run to cancel", j.ID)
	}
	if _, err := c.CancelWorkflowRun(ctx, j.Repo, j.GitHubRunID, force); err != nil {
		return nil, err
	}
	return c.st.GetJob(ctx, j.ID)
}

// CancelWorkflowRun asks GitHub to cancel repo's workflow run runID, without
// needing one of its jobs' IDs. It is what the Workflows page's own run rows
// call directly, since a run there is only ever repo + GitHub's run ID -- see
// store.WorkflowRun for why it has no ID of its own.
//
// Every job of the run still in hand gets the timeline entry an operator's
// request produces, not only whichever job happened to be used to reach the
// run: nothing about the run names one job over another as the one this was
// "really" about.
func (c *Controller) CancelWorkflowRun(ctx context.Context, repo string, runID int64, force bool) ([]*store.Job, error) {
	if !c.cfg().GitHub.AllowWorkflowCancellation {
		return nil, ErrWorkflowCancellationDisabled
	}
	jobs, err := c.st.ListJobsForRun(ctx, repo, runID)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, store.ErrNotFound
	}
	var installationID string
	var live []*store.Job
	for _, j := range jobs {
		if j.InstallationID != "" {
			installationID = j.InstallationID
		}
		if j.State != store.JobCompleted {
			live = append(live, j)
		}
	}
	if len(live) == 0 {
		return nil, ErrJobAlreadyCompleted
	}
	if installationID == "" {
		return nil, fmt.Errorf("workflow run %d in %s has no GitHub installation to cancel", runID, repo)
	}
	client, err := c.ClientFor(ctx, installationID)
	if err != nil {
		return nil, err
	}
	if err := client.CancelWorkflowRun(ctx, repo, runID, force); err != nil {
		return nil, err
	}
	mode := "cancellation"
	if force {
		mode = "force cancellation"
	}
	message := fmt.Sprintf("an operator requested %s of GitHub workflow run %d; local queued work and runners were stopped immediately, while GitHub's completion events remain authoritative for the result", mode, runID)
	for _, j := range live {
		if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
			JobID: j.ID, Kind: store.JobEventCancelRequested, Source: sourceController,
			Message: message, At: c.Now(),
		}); err != nil {
			return nil, err
		}
	}
	if err := c.cancelWorkflowRunLocally(ctx, repo, runID, false); err != nil {
		// GitHub has already accepted the cancellation. Returning an error would
		// encourage an operator to repeat a request that succeeded; the webhook
		// and poller still provide the durable convergence path.
		c.log.Warn("GitHub accepted a workflow cancellation but local work could not all be stopped yet",
			"repo", repo, "run", runID, "error", err)
	}
	out := make([]*store.Job, 0, len(jobs))
	for _, j := range jobs {
		updated, getErr := c.st.GetJob(ctx, j.ID)
		if getErr != nil {
			continue
		}
		out = append(out, updated)
		c.publishJob(ctx, updated)
	}
	return out, nil
}

// cancelWorkflowRunLocally aligns every locally known job in a run with a
// run-scoped cancellation. Before GitHub confirms the terminal run state it
// pauses queued demand without inventing job conclusions. Once confirmed it
// also closes every remaining local job as cancelled.
func (c *Controller) cancelWorkflowRunLocally(ctx context.Context, repo string, runID int64, confirmed bool) error {
	jobs, err := c.st.ListJobsForRun(ctx, repo, runID)
	if err != nil {
		return fmt.Errorf("listing jobs in cancelled workflow run: %w", err)
	}
	now := c.Now()
	var queued, live []string
	var errs []error
	for _, j := range jobs {
		wasActive := j.State != store.JobCompleted
		if !confirmed && j.State == store.JobQueued {
			queued = append(queued, j.ID)
		}
		// Every job the run still owns, queued or running. Pausing the queued
		// half stops it asking for runners, but neither half stops being
		// reported as work in hand until the row says the cancellation was
		// asked for -- GitHub's completion delivery is what ends them, and it
		// is not always prompt.
		if !confirmed && wasActive {
			live = append(live, j.ID)
		}
		if confirmed && wasActive {
			update := *j
			update.State = store.JobCompleted
			update.Conclusion = "cancelled"
			update.CompletedAt = &now
			saved, change, applyErr := c.st.ApplyJob(ctx, &update)
			if applyErr != nil {
				errs = append(errs, fmt.Errorf("cancelling job %s with its workflow run: %w", j.ID, applyErr))
			} else {
				c.recordJobChange(ctx, saved, change, sourceController, nil)
				if change.StateChanged {
					c.observeJobCompletion(saved)
				}
				c.publishJob(ctx, saved)
			}
		}

		// Only a job that was still running when the run-wide cancellation
		// arrived owns live work. Completed siblings may have had their runner
		// reused by another run and must never be torn down here.
		if j.State == store.JobInProgress && j.RunnerID != "" {
			if err := c.stopCancelledJobRunner(ctx, j); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(queued) > 0 {
		if _, err := c.st.ControlProvisioning(ctx, queued, "pause"); err != nil {
			errs = append(errs, fmt.Errorf("suppressing queued jobs in cancelled workflow run: %w", err))
		}
	}
	// Stamped after the pause, so one publish carries both: the frame is the
	// job's GET shape and the UI drops it straight into its cache, so sending
	// it twice would paint the job as merely paused for a moment first.
	if len(live) > 0 {
		stamped, err := c.st.MarkCancelRequested(ctx, live, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("recording the cancellation against the run's jobs: %w", err))
		} else if len(stamped) > 0 {
			c.log.Info("a cancelled workflow run's jobs are waiting on GitHub to confirm",
				"repo", repo, "run", runID, "jobs", len(stamped))
		}
	}
	// `live` is every job `queued` holds and the running ones besides, so one
	// pass over it announces both changes once each.
	for _, id := range live {
		if j, getErr := c.st.GetJob(ctx, id); getErr == nil {
			c.publishJob(ctx, j)
		}
	}
	c.Nudge()
	return errors.Join(errs...)
}

// stopCancelledJobRunner detaches the job before forcing removal. That avoids
// recording a fleet fault: GitHub cancelled this work; the runner did not fail
// underneath it.
func (c *Controller) stopCancelledJobRunner(ctx context.Context, j *store.Job) error {
	r, err := c.st.GetRunner(ctx, j.RunnerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if r.CurrentJobID != j.ID {
		return nil
	}
	finished, changed, err := c.st.CompleteRunnerJob(ctx, r.ID, j.ID,
		fmt.Sprintf("workflow run %d was cancelled", j.GitHubRunID))
	if err != nil {
		return fmt.Errorf("releasing runner %s from cancelled workflow run: %w", r.ID, err)
	}
	if changed {
		c.publishRunner(ctx, events.KindRunnerUpdated, finished)
	}
	if finished.State == store.RunnerRemoved {
		c.enqueueLifecycle(ctx, finished.HostID, agent.Task{
			Kind: agent.TaskRemoveRunner, RunnerID: finished.ID,
			Backend: c.backendKind(ctx, finished, nil),
		})
		return nil
	}
	return c.removeRunnerID(ctx, finished.ID,
		fmt.Sprintf("GitHub workflow run %d was cancelled", j.GitHubRunID), nil)
}

// RerunJobWorkflow asks GitHub to run the failed jobs of this job's run again.
//
// This is what Zoomies can actually do about a failure it caused. A job the
// fleet broke did not fail on its merits -- nothing about the workflow has
// changed, and the ordinary remedy is to run it again -- but until now an
// operator had to notice the fault, work out that it was the fleet's, find the
// run on GitHub and press the button there.
//
// Three things it deliberately does not do:
//
// It does not re-run by itself. GitHub minutes are the operator's to spend, a
// fleet that re-runs its own failures can loop on a fault it is causing every
// time, and a job that failed may have had side effects the person who wrote
// it knows about and this does not.
//
// It does not restrict itself to fleet faults. The check is that the job
// failed, not whose fault it was: an operator who has looked at a failure and
// decided to run it again is entitled to, and a button that refused on a
// judgement Zoomies made about blame would be a button people learn to route
// around. What the fault category does is decide where the button is offered.
//
// And it does not touch the local row. The re-run arrives as new deliveries
// with a higher run attempt, through the same webhook path as everything else;
// writing an outcome here would be the fleet inventing one.
func (c *Controller) RerunJobWorkflow(ctx context.Context, jobID string) (*store.Job, error) {
	j, err := c.st.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if j.State != store.JobCompleted {
		return nil, ErrJobNotFinished
	}
	if !j.Failed() {
		return nil, ErrJobDidNotFail
	}
	if j.InstallationID == "" || j.Repo == "" || j.GitHubRunID <= 0 {
		return nil, fmt.Errorf("job %s has no GitHub installation and workflow run to re-run", j.ID)
	}
	if err := c.askGitHubToRerun(ctx, j, rerunByOperator); err != nil {
		return nil, err
	}
	return j, nil
}

// Who asked for a re-run. It is the metric's label and the difference between
// the two sentences a timeline entry can carry, and the two are deliberately
// not interchangeable: one is a person who looked at the failure and decided,
// the other is a setting somebody turned on weeks ago.
const (
	rerunByOperator = "operator"
	rerunByFault    = "fleet_fault"
)

// askGitHubToRerun makes the call, writes the timeline entry and counts the
// metric. It is shared by the button and by the automatic path so the two
// cannot drift into asking GitHub for different things.
func (c *Controller) askGitHubToRerun(ctx context.Context, j *store.Job, trigger string) error {
	client, err := c.ClientFor(ctx, j.InstallationID)
	if err != nil {
		return err
	}
	who := "an operator"
	source := sourceController
	if trigger == rerunByFault {
		who = "this fleet"
		source = sourceRecovery
	}
	// One job where we can, the whole run's failures only where we cannot.
	// GitHub's job-level re-run takes the job and whatever names it in
	// `needs`; the run-level one takes every failure in the run, which for
	// somebody who asked about one job is three other teams' jobs running
	// again on their account.
	var message string
	if j.GitHubJobID > 0 {
		if err := client.RerunWorkflowJob(ctx, j.Repo, j.GitHubJobID); err != nil {
			return err
		}
		message = fmt.Sprintf("%s asked GitHub to run job %d again; GitHub runs it together with any job that needs it, and they arrive as a new run attempt", who, j.GitHubJobID)
	} else {
		// A job recorded before this fleet kept GitHub's job ID, or one whose
		// workflow_job delivery never arrived. The wider call is still better
		// than refusing the button.
		if err := client.RerunFailedWorkflowJobs(ctx, j.Repo, j.GitHubRunID); err != nil {
			return err
		}
		message = fmt.Sprintf("%s asked GitHub to run the failed jobs of workflow run %d again, because this job has no GitHub job ID recorded; GitHub reruns them together, and they arrive as a new run attempt", who, j.GitHubRunID)
	}
	switch {
	case trigger == rerunByFault:
		message += fmt.Sprintf(", because a runner stopped under it and scheduler.auto_rerun is on. This is attempt %d of the %d this run may be given", c.runAttempt(j)+1, c.cfg().Scheduler.AutoRerunLimit+1)
	case j.FleetFailed():
		message += ". This job's failure was the fleet's rather than the workflow's"
	}
	if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
		JobID: j.ID, Kind: store.JobEventRerunRequested, Source: source,
		Message: message, At: c.Now(),
	}); err != nil {
		return err
	}
	c.metrics.jobReruns.WithLabelValues(c.poolLabel(j.PoolID), trigger).Inc()
	c.log.Info("asked GitHub to re-run a run's failed jobs",
		"job", j.ID, "repo", j.Repo, "run", j.GitHubRunID, "fault", j.FaultKind, "trigger", trigger)
	c.publishJob(ctx, j)
	return nil
}

// A job's timeline is the answer to "what happened to my job?", told in the
// order it happened and in sentences an operator can read without knowing how
// Zoomies works. The jobs row holds the current truth; the timeline holds how
// it got there, and it is the only place a fault on the fleet's side -- a runner
// that died under a job -- is written down next to the job it cost.
//
// Entries are written from what the store says changed, never from what a
// delivery said. GitHub delivers at least once and out of order, so "a queued
// delivery arrived" is not the same fact as "the job was queued".

// Where a timeline entry came from.
const (
	sourceWebhook    = "webhook"
	sourcePoller     = "poller"
	sourceAgent      = "agent"
	sourceController = "controller"
	// A re-run the fleet decided on rather than a person. It is its own source
	// so a timeline reads "via recovery" and nobody has to work out which
	// operator pressed a button that was never pressed.
	sourceRecovery = "recovery"
)

// recordJobChange writes the timeline entries a job change earned.
//
// runner is this fleet's row for the runner the delivery named, when there is
// one; a job that started on a runner Zoomies did not start is recorded as such,
// because "it ran somewhere else" is the whole explanation of why the fleet's
// numbers do not include it.
func (c *Controller) recordJobChange(ctx context.Context, j *store.Job, change store.JobChange, source string, runner *store.Runner) {
	if j == nil {
		return
	}
	c.stampJobVersions(ctx, j, runner)
	c.stampJobGranted(ctx, j, runner)
	c.stampJobRan(ctx, j, runner)
	c.observeScheduling(ctx, j, change, runner)
	at := c.Now()
	add := func(kind store.JobEventKind, message string) {
		e := &store.JobEvent{JobID: j.ID, Kind: kind, Source: source, Message: message, At: at}
		if runner != nil && (kind == store.JobEventStarted || kind == store.JobEventCompleted) {
			e.RunnerID, e.RunnerName = runner.ID, runner.Name
		} else if kind == store.JobEventStarted || kind == store.JobEventCompleted {
			e.RunnerID, e.RunnerName = j.RunnerID, j.RunnerName
		}
		if err := c.st.AppendJobEvent(ctx, e); err != nil {
			c.log.Warn("could not record a job timeline entry", "job", j.ID, "kind", kind, "error", err)
		}
	}

	if change.Created && j.State == store.JobWaiting {
		// Held by GitHub for a deployment review. The time it spends here is
		// GitHub's, not the queue's, so the first line says so rather than
		// calling the job queued; the claim line waits for the approval,
		// which is when it becomes true.
		add(store.JobEventWaiting, fmt.Sprintf("GitHub announced %s in %s, asking for [%s], and is holding it for a deployment review; nothing here can start it until it is approved",
			jobTitle(j), j.Repo, strings.Join(j.Labels, ", ")))
	} else if change.Created {
		add(store.JobEventQueued, fmt.Sprintf("GitHub queued %s in %s, asking for [%s]",
			jobTitle(j), j.Repo, strings.Join(j.Labels, ", ")))
		// Whether a pool answers the labels matters while the job is waiting.
		// A job first seen already running was answered by whatever runs it,
		// and the "started" line below says who that was.
		if j.State == store.JobQueued {
			add(c.claimKind(j), c.claimMessage(ctx, j))
		}
		// How the controller classed it, where it did, is part of why a pool
		// claims it and which one.
		if j.SizeBasis != "" {
			add(store.JobEventSized, c.sizeMessage(j))
		}
	} else if change.StateChanged && change.PreviousState == store.JobWaiting && j.State == store.JobQueued {
		// The approval arrives as an ordinary queued delivery. This is when
		// the queue wait starts, and when whether a pool claims it matters.
		add(store.JobEventApproved, "GitHub approved it, and it is queued for a runner from now")
		add(c.claimKind(j), c.claimMessage(ctx, j))
	} else if change.Claimed && j.State == store.JobQueued {
		add(store.JobEventClaimed, c.claimMessage(ctx, j))
	}

	if !change.StateChanged && !change.Created {
		return
	}
	// A job whose first delivery is already in progress or complete skipped
	// straight past "queued" here: the controller was down, or the webhook
	// never arrived. Each state it reached still gets its line.
	if j.State == store.JobInProgress || (j.State == store.JobCompleted && j.StartedAt != nil &&
		(change.Created || change.PreviousState == store.JobQueued)) {
		add(store.JobEventStarted, c.startMessage(ctx, j, runner))
	}
	if j.State == store.JobCompleted {
		add(store.JobEventCompleted, completionMessage(j))
		c.autoRerunFleetFailure(ctx, j)
		// Another run of this job has now been measured, and what it says about
		// the job's class is kept for the next time it is queued.
		c.refreshJobClass(ctx, j)
	}
}

// runAttempt is GitHub's attempt number for this job's run, reading an
// unrecorded one as the first. A job from before the column existed, or one
// whose delivery carried no attempt, is on its first attempt as far as anyone
// can tell, and treating it as a later one would refuse the re-run that is the
// whole point.
func (c *Controller) runAttempt(j *store.Job) int {
	if j.RunAttempt < 1 {
		return 1
	}
	return j.RunAttempt
}

// autoRerunFleetFailure sends a job back to GitHub when this fleet is what
// broke it.
//
// A runner that dies under a job fails it in a way indistinguishable, on
// GitHub, from a test failure -- and the ordinary remedy for a job that did
// not fail on its merits is to run it again. Until this, somebody had to
// notice, work out that the fault was the fleet's, and press the button.
//
// It is off by default and narrow when on:
//
// Only the fleet's own failures. `FleetFailed` is the same predicate the Jobs
// page splits on, so what is re-run automatically is exactly what the fleet
// has already confessed to. A test that failed is never touched: re-running
// somebody's red build for them would be the fleet overruling their result.
//
// Only while GitHub's attempt number is under the limit. That is what stops a
// fault the fleet causes every time -- a host out of disk, an image that will
// not pull -- from re-running the same job until somebody notices the bill.
// The count comes from GitHub rather than from us, so a controller restart
// does not reset it and a re-run an operator asked for by hand counts too.
//
// Only once per completion. Deliveries are at-least-once, and a second
// "completed" for a job already completed does not change its state, so it
// does not reach here: the caller only runs this branch on a change.
//
// A failure to ask is logged and dropped. The button is still there, the job
// is still failed, and a fleet that cannot reach GitHub has a louder problem
// than this one already on the drawer.
func (c *Controller) autoRerunFleetFailure(ctx context.Context, j *store.Job) {
	cfg := c.cfg()
	if !cfg.Scheduler.AutoRerun || !j.FleetFailed() || !j.Failed() {
		return
	}
	if j.InstallationID == "" || j.Repo == "" || j.GitHubRunID <= 0 {
		return
	}
	if attempt := c.runAttempt(j); attempt > cfg.Scheduler.AutoRerunLimit {
		c.log.Info("not re-running a job the fleet broke: this run has had its automatic re-runs",
			"job", j.ID, "run", j.GitHubRunID, "attempt", attempt, "limit", cfg.Scheduler.AutoRerunLimit)
		return
	}
	if err := c.askGitHubToRerun(ctx, j, rerunByFault); err != nil {
		c.log.Warn("could not re-run a job the fleet broke",
			"job", j.ID, "run", j.GitHubRunID, "error", err)
	}
}

// claimKind is "claimed" or "unmatched", the two answers to "will anything
// here run it?".
func (c *Controller) claimKind(j *store.Job) store.JobEventKind {
	if j.Matched {
		return store.JobEventClaimed
	}
	return store.JobEventUnmatched
}

func (c *Controller) claimMessage(ctx context.Context, j *store.Job) string {
	if !j.Matched {
		// A job this fleet holds no credential for is refused for a reason its
		// labels cannot explain, and the timeline is read by the same person
		// the problems drawer is; telling them to check their runs-on here
		// while the drawer says the App is not installed would be worse than
		// saying nothing.
		if j.InstallationID == "" {
			return "no GitHub App installation here covers " + j.Repo +
				", so no pool can claim it whatever its labels say"
		}
		return "no enabled pool claims these labels, so nothing in this fleet will start it"
	}
	name := j.PoolID
	if p, err := c.st.GetPool(ctx, j.PoolID); err == nil {
		name = p.Name
	}
	return fmt.Sprintf("pool %s claims it; the scheduler will start a runner if none is free", name)
}

func (c *Controller) startMessage(ctx context.Context, j *store.Job, runner *store.Runner) string {
	if runner == nil && j.RunnerID != "" {
		if r, err := c.st.GetRunner(ctx, j.RunnerID); err == nil {
			runner = r
		}
	}
	switch {
	case runner != nil:
		host := runner.HostID
		if h, err := c.st.GetHost(ctx, runner.HostID); err == nil {
			host = naming.ForSentence(h.Name)
		}
		return fmt.Sprintf("started on runner %s, on host %s, after %s in the queue",
			runner.Name, host, roundDuration(j.QueueWait()))
	case j.RunnerName != "":
		return fmt.Sprintf("started on %s, a runner this fleet does not manage, after %s in the queue",
			j.RunnerName, roundDuration(j.QueueWait()))
	}
	return fmt.Sprintf("started after %s in the queue", roundDuration(j.QueueWait()))
}

// completionMessage is the one line a finished job is summarised by: what
// GitHub concluded, and where it went wrong when it did.
func completionMessage(j *store.Job) string {
	took := roundDuration(j.Duration())
	switch j.Conclusion {
	case "success":
		return fmt.Sprintf("succeeded after %s", took)
	case "cancelled":
		if step := j.FailedStep(); step != nil {
			return fmt.Sprintf("cancelled after %s, during step %d, %s", took, step.Number, step.Name)
		}
		return fmt.Sprintf("cancelled after %s", took)
	case "skipped":
		return "skipped: a condition on the job was false"
	case "":
		return fmt.Sprintf("finished after %s", took)
	}
	verb := "failed"
	if j.Conclusion != "failure" {
		verb = strings.ReplaceAll(j.Conclusion, "_", " ")
	}
	if step := j.FailedStep(); step != nil {
		return fmt.Sprintf("%s at step %d, %s, after %s", verb, step.Number, step.Name, took)
	}
	if j.RunnerFault != "" {
		return fmt.Sprintf("%s after %s; the runner had stopped under it", verb, took)
	}
	return fmt.Sprintf("%s after %s", verb, took)
}

// noteOOMKilled marks the job a runner ran last as killed for memory, writes
// its timeline entry and publishes it. It is idempotent for the reason
// noteRunnerLost is: the same exit reaches here from a report and again from
// a task result, and only the call that marked the job writes the entry.
func (c *Controller) noteOOMKilled(ctx context.Context, r *store.Runner, message string) {
	if message == "" {
		message = "the kernel killed a process in it for its memory limit"
	}
	sentence := fmt.Sprintf("runner %s was killed for memory on host %s: %s", r.Name, r.HostID, message)
	pool, _ := c.st.GetPool(ctx, r.PoolID)
	sentence += memoryValveEpilogue(pool, r)
	j, marked, err := c.st.MarkJobOOMKilled(ctx, r.ID, sentence)
	if err != nil {
		c.log.Warn("could not record an out-of-memory kill on its job", "runner", r.ID, "error", err)
		return
	}
	if j == nil || !marked {
		return
	}
	if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
		JobID: j.ID, Kind: store.JobEventOOMKilled, Source: sourceAgent,
		Message: sentence, RunnerID: r.ID, RunnerName: r.Name, At: c.Now(),
	}); err != nil {
		c.log.Warn("could not record a job timeline entry", "job", j.ID, "kind", store.JobEventOOMKilled, "error", err)
	}
	// A kill is the one piece of evidence the class does not wait for.
	c.refreshJobClass(ctx, j)
	c.publishJob(ctx, j)
}

// noteRunnerLost records that a runner stopped while it was still executing a
// job. GitHub will report the job as failed in its own time, indistinguishable
// from a test failure; this is what tells the operator the fleet did it.
//
// before is the runner as it was before the transition, because the store
// clears CurrentJobID when a runner goes terminal and the job's identity would
// otherwise be gone by the time anyone asked. source says who saw it go: the
// agent, when the container died on its own; the controller, when an operator
// removed a busy runner with force or the reconcile loop gave up on it.
//
// fault is the category, decided by whoever saw the runner stop: the agent
// from an exit code or the daemon's own reply, the controller from its own
// decision to give up on a host. It is a parameter rather than something read
// back out of message, because the evidence is gone by the time the sentence
// is written.
//
// It is idempotent. The same exit can reach here more than once -- a runner
// report and then the task result that carries it -- and only the report that
// records the fault writes the timeline entry and counts the metric.
func (c *Controller) noteRunnerLost(ctx context.Context, before *store.Runner, source, message string, fault store.FaultKind) {
	if before == nil || before.CurrentJobID == "" {
		return
	}
	j, err := c.st.GetJob(ctx, before.CurrentJobID)
	if err != nil {
		return
	}
	if j.State == store.JobCompleted {
		// GitHub already closed the job; the runner exiting afterwards is the
		// normal end of an ephemeral runner's life, whatever its exit code.
		return
	}
	if message == "" {
		message = "the runner stopped without saying why"
	}
	sentence := fmt.Sprintf("runner %s stopped while this job was running: %s", before.Name, message)
	updated, recorded, err := c.st.SetJobRunnerFault(ctx, j.ID, sentence, fault)
	if err != nil {
		c.log.Warn("could not record a lost runner on its job", "job", j.ID, "runner", before.ID, "error", err)
		return
	}
	if !recorded {
		return
	}
	if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
		JobID: j.ID, Kind: store.JobEventRunnerLost, Source: source,
		Message:  sentence + "; GitHub will report the job failed once the runner's absence is noticed",
		RunnerID: before.ID, RunnerName: before.Name, At: c.Now(),
	}); err != nil {
		c.log.Warn("could not record a job timeline entry", "job", j.ID, "kind", store.JobEventRunnerLost, "error", err)
	}
	c.metrics.jobsRunnerLost.WithLabelValues(c.poolLabel(before.PoolID)).Inc()
	c.metrics.jobFailures.WithLabelValues(c.poolLabel(before.PoolID), store.FaultDomainFleet, string(updated.FaultKind)).Inc()
	c.log.Warn("a runner stopped while running a job",
		"job", j.ID, "runner", before.ID, "name", before.Name, "fault", updated.FaultKind, "message", message)
	c.publishJob(ctx, updated)
}

// startFailureFanout caps how many waiting jobs one failed start is written
// onto. A pool that cannot start a container fails every attempt, and a fleet
// with three hundred jobs queued against it would otherwise spend a reconcile
// pass writing three hundred timeline rows that all say the same sentence.
// The problems panel carries the scale; the timeline carries the news.
const startFailureFanout = 20

// noteRunnerStartFailure tells the jobs waiting on a pool that a runner meant
// for work like theirs died before it could take any.
//
// This is the failure that reached nothing before. A runner that fails while
// executing a job has a job to be recorded on; a runner that fails on the way
// up has none -- it was created for a pool, not for a job -- so from the Jobs
// page a pool whose containers will not start was indistinguishable from a
// pool that was merely busy, and the operator watching a queue that never
// moved had nowhere to find out which.
//
// It deliberately does not fail the jobs. They are still queued, the next
// runner may well run them, and Zoomies concluding a job GitHub still has open
// would be the fleet inventing an outcome. What it writes is a note.
func (c *Controller) noteRunnerStartFailure(ctx context.Context, before, failed *store.Runner) {
	if failed == nil || failed.PoolID == "" {
		return
	}
	// A runner that had a job is the other case entirely, and noteRunnerLost
	// has already recorded it on the job it was running.
	if before != nil && before.CurrentJobID != "" {
		return
	}
	// Registering counts as never having started: the container may exist, but
	// nothing has ever handed it a job and nothing now will.
	if before != nil && before.State != store.RunnerProvisioning && before.State != store.RunnerRegistering {
		return
	}
	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		c.log.Warn("could not list queued jobs to note a failed runner start", "runner", failed.ID, "error", err)
		return
	}
	written := 0
	for _, j := range queued {
		if written >= startFailureFanout {
			break
		}
		if j.PoolID != failed.PoolID {
			continue
		}
		// One entry per job per run of failures. A pool stuck in a start loop
		// tries again every pass, and a timeline is opened to avoid reading a
		// log file rather than to read one.
		last, err := c.st.LastJobEventKind(ctx, j.ID)
		if err != nil {
			c.log.Warn("could not read a job's last timeline entry", "job", j.ID, "error", err)
			continue
		}
		if last == store.JobEventRunnerStartFailed {
			continue
		}
		if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
			JobID: j.ID, Kind: store.JobEventRunnerStartFailed, Source: sourceController,
			Message: fmt.Sprintf("a runner this pool started for work like this one never took a job: %s. "+
				"This job is still queued and the next runner may run it", failed.Message),
			RunnerID: failed.ID, RunnerName: failed.Name, At: c.Now(),
		}); err != nil {
			c.log.Warn("could not record a job timeline entry", "job", j.ID, "kind", store.JobEventRunnerStartFailed, "error", err)
			continue
		}
		written++
	}
	c.metrics.runnerStartFailures.WithLabelValues(c.poolLabel(failed.PoolID), string(failed.FaultKind)).Inc()
}

// JobEvents returns a job's timeline, oldest first.
func (c *Controller) JobEvents(ctx context.Context, jobID string) ([]*store.JobEvent, error) {
	if _, err := c.st.GetJob(ctx, jobID); err != nil {
		return nil, err
	}
	return c.st.ListJobEvents(ctx, jobID)
}

func jobTitle(j *store.Job) string {
	switch {
	case j.Workflow != "" && j.JobName != "":
		return j.Workflow + " / " + j.JobName
	case j.JobName != "":
		return j.JobName
	case j.Workflow != "":
		return j.Workflow
	}
	return fmt.Sprintf("job %d", j.GitHubJobID)
}

// roundDuration renders a duration the way a person would say it: "42s",
// "3m 10s", "1h 05m". Sub-second precision is noise in a sentence.
func roundDuration(d time.Duration) string {
	if d <= 0 {
		return "no time"
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// staleQueuedAfter is how long a job may sit queued before the controller
// stops believing GitHub still has it. GitHub itself cancels a job that no
// runner has picked up within a day, and it is the completed delivery for a
// job like that -- or one whose repository was deleted, or whose run was
// cancelled while the poller was rate-limited -- that goes missing.
const staleQueuedAfter = 24 * time.Hour

// expireStaleQueuedJobs retires queued jobs older than staleQueuedAfter.
//
// A queued row is scheduler demand for as long as it exists: it keeps a pool's
// desired count up, so a runner is created for it, idles out, and is created
// again, for ever. Nothing else reconciles a row GitHub no longer lists, so
// this marks it completed with the conclusion GitHub uses for the same thing,
// "stale", and the timeline says why.
func (c *Controller) expireStaleQueuedJobs(ctx context.Context, now time.Time) {
	// Asked for by age rather than filtered here, because this is the one
	// sweep that wants the jobs an operator removed from the queue as well:
	// the removal stopped them counting as demand, and this is what stops them
	// sitting in the Queue's removed view for ever.
	queued, err := c.st.ListStaleQueuedJobs(ctx, now.Add(-staleQueuedAfter))
	if err != nil {
		c.log.Warn("could not list queued jobs to retire stale ones", "error", err)
		return
	}
	retired := 0
	for _, j := range queued {
		age := now.Sub(j.QueuedAt)
		done := now
		saved, change, err := c.st.ApplyJob(ctx, &store.Job{
			GitHubJobID: j.GitHubJobID, State: store.JobCompleted, Conclusion: "stale", CompletedAt: &done,
		})
		if err != nil {
			c.log.Warn("could not retire a stale queued job", "job", j.ID, "error", err)
			continue
		}
		if !change.StateChanged {
			continue
		}
		message := fmt.Sprintf("queued for %s with no word from GitHub that it started; GitHub gives up on a job after a day, so this one is presumed cancelled or lost and is no longer counted as demand",
			age.Truncate(time.Minute))
		if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
			JobID: saved.ID, Kind: store.JobEventCompleted, Source: sourceController, Message: message, At: now,
		}); err != nil {
			c.log.Warn("could not record a job timeline entry", "job", saved.ID, "kind", store.JobEventCompleted, "error", err)
		}
		c.log.Warn("retired a queued job GitHub never started", "job", saved.ID, "repo", saved.Repo, "queued_for", age.Truncate(time.Minute))
		c.publishJob(ctx, saved)
		retired++
	}
	if retired > 0 {
		// Demand has dropped, and a runner created for the retired job may
		// now be surplus.
		c.Nudge()
	}
}
