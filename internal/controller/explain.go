package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

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

	switch job.State {
	case store.JobCompleted:
		c.explainCompleted(job, out)
		return out, nil
	case store.JobInProgress:
		// Ahead of the running explanation, which would otherwise name a
		// runner this fleet took away when the cancellation landed.
		if job.Cancelling() {
			explainCancelling(job, out)
			return out, nil
		}
		c.explainRunning(ctx, job, out)
		return out, nil
	case store.JobWaiting:
		// Not this fleet's wait at all, and saying so is the point: a job
		// sitting still reads as a slow fleet until somebody says otherwise.
		out.Waiting = true
		out.Summary = "GitHub is holding this job for a deployment review."
		out.Detail = "Nothing here can start it until somebody approves it. The wait for a runner begins when they do, so the queue wait below has not started."
		out.Fix = "approve the deployment on GitHub, or leave it -- nothing in this fleet is wrong."
		return out, nil
	}

	out.Waiting = true
	c.explainQueued(ctx, job, out)
	return out, nil
}

func (c *Controller) explainCompleted(job *store.Job, out *JobExplanation) {
	defer explainOOM(job, out)
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
	case store.IsFailedConclusion(job.Conclusion):
		out.Summary = "This job ran and " + job.Conclusion + "."
		out.Detail = "The fleet did its part: a conclusion is the workflow's own outcome."
	default:
		out.Summary = "This job ran and " + job.Conclusion + "."
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
	if job.FleetFailed() {
		out.Blocked = true
		out.Summary = "The runner this job was on has stopped, and GitHub has not noticed yet."
		out.Detail = job.RunnerFault + ". GitHub will report the job failed once the runner's absence is noticed, and it will look like any other failure."
		out.Fix = job.FaultKind.Fix()
		if out.Fix == "" {
			out.Fix = "this is the fleet's failure rather than the workflow's: the runner's page has what it said as it went."
		}
		return
	}
	out.Summary = "This job is running."
	if job.RunnerName != "" {
		out.Summary = "This job is running on " + job.RunnerName + "."
	}
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
		host.Name, formatAge(c.Now().Sub(host.LastHeartbeat)), store.HeartbeatTimeout)
	out.Fix = "check that the zoomies agent is running on that host and can reach this controller."
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
		if plan != nil {
			for _, u := range plan.Unmatched {
				if u.Job != nil && u.Job.ID == job.ID && u.Reason != "" {
					out.Detail = u.Reason
					break
				}
			}
		}
		out.Fix = "create or enable a pool advertising those labels, or change the workflow's runs-on. If another runner provider takes these jobs, nothing needs doing."
		return
	}

	pool, err := c.st.GetPool(ctx, job.PoolID)
	if err != nil {
		out.Summary = "A pool claimed this job, and that pool is no longer here."
		out.Fix = "the job will be re-matched on the next pass; if it is not, its labels no longer name a pool."
		return
	}
	out.Summary = "A runner is on its way for this job."
	out.Detail = "It is claimed by " + pool.Name + "."

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
	case pc.Provisioning+pc.Registering > 0:
		out.Summary = fmt.Sprintf("%s starting for %s.", runnersAre(pc.Provisioning+pc.Registering), pool.Name)
		out.Detail = "The job goes to the first one GitHub sees."
	case pool.MaxRunners > 0 && pc.Live() >= pool.MaxRunners:
		out.Waiting = true
		out.Summary = fmt.Sprintf("%s is at its ceiling of %s, all busy.", pool.Name, plural(pool.MaxRunners, "runner"))
		out.Detail = "The job waits for one of them to finish."
		out.Fix = "raise this pool's max_runners if the fleet has room for more."
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
