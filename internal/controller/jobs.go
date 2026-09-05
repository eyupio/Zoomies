package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

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

	if change.Created {
		add(store.JobEventQueued, fmt.Sprintf("GitHub queued %s in %s, asking for [%s]",
			jobTitle(j), j.Repo, strings.Join(j.Labels, ", ")))
		// Whether a pool answers the labels matters while the job is waiting.
		// A job first seen already running was answered by whatever runs it,
		// and the "started" line below says who that was.
		if j.State == store.JobQueued {
			add(c.claimKind(j), c.claimMessage(ctx, j))
		}
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
			host = h.Name
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
// It is idempotent. The same exit can reach here more than once -- a runner
// report and then the task result that carries it -- and only the report that
// records the fault writes the timeline entry and counts the metric.
func (c *Controller) noteRunnerLost(ctx context.Context, before *store.Runner, source, message string) {
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
	fault := fmt.Sprintf("runner %s stopped while this job was running: %s", before.Name, message)
	updated, recorded, err := c.st.SetJobRunnerFault(ctx, j.ID, fault)
	if err != nil {
		c.log.Warn("could not record a lost runner on its job", "job", j.ID, "runner", before.ID, "error", err)
		return
	}
	if !recorded {
		return
	}
	if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
		JobID: j.ID, Kind: store.JobEventRunnerLost, Source: source,
		Message:  fault + "; GitHub will report the job failed once the runner's absence is noticed",
		RunnerID: before.ID, RunnerName: before.Name, At: c.Now(),
	}); err != nil {
		c.log.Warn("could not record a job timeline entry", "job", j.ID, "kind", store.JobEventRunnerLost, "error", err)
	}
	pool := before.PoolID
	if p, err := c.st.GetPool(ctx, before.PoolID); err == nil {
		pool = p.Name
	}
	c.metrics.jobsRunnerLost.WithLabelValues(pool).Inc()
	c.log.Warn("a runner stopped while running a job", "job", j.ID, "runner", before.ID, "name", before.Name, "message", message)
	c.publishJob(ctx, updated)
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
