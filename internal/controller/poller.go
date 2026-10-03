package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// rateLimitBackoff is how long to stand down from an installation when GitHub
// says it is out of quota and does not say when that ends. Polling and reaping
// are background paths; spending the last of an installation's quota on them
// would take the webhook path's own API calls -- minting JIT configs -- down
// with it.
const rateLimitBackoff = 15 * time.Minute

const (
	// rateLimitSlack is added to a reset GitHub named. Resuming on the exact
	// second races its own accounting and buys another refusal, which costs
	// the call and starts the wait again.
	rateLimitSlack = 5 * time.Second

	// maxRateLimitBackoff caps a stand-down however far away the reset says it
	// is. GitHub's primary window is an hour, so anything beyond that is a
	// clock out of step or a proxy inventing a header -- and taking an
	// installation out of service for a day on either would be a far worse
	// failure than one more refused call.
	maxRateLimitBackoff = time.Hour
)

// rateLimitHold is when an installation refused for quota may be used again.
//
// GitHub sends the answer on the response, so the fixed backoff is only what
// to do when it did not: waiting a flat fifteen minutes for a quota that came
// back in two wastes the difference on every sweep, and for one that comes
// back in fifty spends the rest of the window discovering that again.
func rateLimitHold(err error, now time.Time) time.Time {
	until, ok := github.RetryAfterRateLimit(err, now)
	if !ok {
		return now.Add(rateLimitBackoff)
	}
	until = until.Add(rateLimitSlack)
	if capped := now.Add(maxRateLimitBackoff); until.After(capped) {
		return capped
	}
	return until
}

// pollLoop is the webhook fallback.
//
// A controller behind NAT, or one whose webhook secret was mistyped, would
// otherwise stop scaling silently, and a fleet that has quietly stopped
// scaling looks exactly like a quiet fleet.
func (c *Controller) pollLoop(ctx context.Context) {
	if !c.cfg().GitHub.PollFallback {
		c.log.Info("the fallback poller is off; scaling depends entirely on webhooks reaching this controller")
		return
	}
	interval := c.pollInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// The poller exists to find work this fleet should run. A fenced
			// fleet is not going to run any, and sweeping would spend the
			// installation's GitHub quota to learn something it cannot act on.
			if !c.Fenced().Fenced {
				c.discoverJobs(ctx)
				c.polls.Add(1)
			}
		case <-c.pollSettingsChanged:
			// github.poll_interval is a runtime setting. The timer was built
			// from the old value, so it is rebuilt here rather than left to
			// fire once more on the old interval.
		}
		if d := c.pollInterval(); d != interval {
			interval = d
			ticker.Reset(d)
		}
	}
}

func (c *Controller) pollInterval() time.Duration {
	if d := c.cfg().GitHub.PollInterval; d > 0 {
		return d
	}
	return 30 * time.Second
}

// pollOnce lists unfinished jobs for every installation and records what it
// finds.
//
// Queue discovery skips installations with recent webhooks. Known unfinished
// jobs are checked separately in bounded pages so a missing completion cannot
// be hidden by unrelated deliveries.
func (c *Controller) pollOnce(ctx context.Context) {
	c.reconcileKnownJobs(ctx, c.Now())
	c.discoverJobs(ctx)
}

func (c *Controller) discoverJobs(ctx context.Context) {
	now := c.Now()

	// Accepted deliveries only: one that was rejected recorded a job for
	// nobody, and a run of them is the mistyped-secret case this poller is
	// the safety net for.
	//
	// The fleet-wide answer is only used to say whether a webhook has ever
	// arrived, which the Overview reports about the deployment rather than
	// about any one installation. What to poll is decided per installation
	// below.
	last, err := c.st.LastAcceptedDeliveryAt(ctx)
	if err != nil {
		c.log.Error("could not tell when the last webhook arrived", "error", err)
		return
	}
	c.pollingOnly.Store(last.IsZero())

	fresh, err := c.st.InstallationsFreshSince(ctx, now.Add(-2*c.pollInterval()))
	if err != nil {
		c.log.Error("could not tell which installations webhooks are arriving for", "error", err)
		return
	}

	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		c.log.Error("could not list installations to poll", "error", err)
		return
	}

	found, changed := 0, 0
	for _, inst := range insts {
		if ctx.Err() != nil {
			return
		}
		if IsDemoID(inst.ID) {
			// The demo fixtures are already in the database; there is nothing
			// to poll for, and trying would log a credential failure on every
			// tick of a demo instance.
			continue
		}
		if c.githubHeld(inst.ID, now) {
			continue
		}
		// Skip discovery for this installation when its deliveries are fresh.
		// Known job reconciliation above remains independent of that signal.
		if fresh[inst.ID] {
			continue
		}
		client, err := c.clients.get(ctx, inst)
		if err != nil {
			c.log.Warn("skipping an installation while polling", "installation", inst.ID, "error", err)
			continue
		}
		jobs, err := client.ListQueuedJobs(ctx)
		c.observeGitHub(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrRateLimited) {
				// Hold this installation only. Its quota is its own, and the
				// sweep abandoning the rest would let one organisation out of
				// quota stop every other one from scaling.
				until := rateLimitHold(err, now)
				c.holdGitHub(inst.ID, until)
				c.log.Warn("GitHub rate-limited the fallback poller for an installation; standing down for it",
					"installation", inst.ID, "until", until.UTC().Format(time.RFC3339), "error", err)
				continue
			}
			c.log.Warn("could not poll for queued jobs", "installation", inst.ID, "error", err)
			continue
		}
		found += len(jobs)
		n, err := c.ingestQueuedJobs(ctx, inst, jobs)
		if err != nil {
			c.log.Error("could not record polled jobs", "installation", inst.ID, "error", err)
			continue
		}
		changed += n
	}

	if found > 0 {
		c.log.Debug("polled GitHub for queued jobs", "found", found, "new_or_changed", changed)
	}
	if changed > 0 {
		c.Nudge()
	}

	// Stamped here, at the end, rather than in the loop that calls this: what
	// an operator wants to know is when the poller last got all the way round,
	// and a sweep that returned early because it could not read its own
	// database did not. The early returns above deliberately leave the stamp
	// where it was, so the interval keeps growing and poller.stale fires.
	c.lastPollAt.Store(c.Now().UnixNano())
}

// ingestQueuedJobs folds polled jobs into the same rows the webhook path
// writes, so both paths feed one scheduler and one history.
//
// A job is attributed to the installation covering its repository, resolved
// the same way the webhook path resolves it, rather than to the installation
// this poll happened to come from. The two are usually the same and are not
// always: an organisation installation and a repository one can both cover a
// repository, and if each path picked its own answer the job's installation
// would flip with every delivery. The polled installation is the fallback,
// because it did report the job.
//
// The count returned is of jobs left waiting, which is what the caller wakes
// the scheduler for. A job found already running is handed to
// ingestStartedJob, which wakes it itself.
func (c *Controller) ingestQueuedJobs(ctx context.Context, polled *store.Installation, jobs []github.QueuedJob) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing pools to match polled jobs: %w", err)
	}

	changed := 0
	for _, q := range jobs {
		if store.JobState(q.Status) == store.JobInProgress {
			c.ingestStartedJob(ctx, q)
			continue
		}
		job := &store.Job{
			GitHubJobID:    q.ID,
			GitHubRunID:    q.RunID,
			RunNumber:      q.RunNumber,
			Repo:           q.Repo,
			Workflow:       q.WorkflowName,
			JobName:        q.JobName,
			Labels:         store.NormalizeLabels(q.Labels),
			State:          store.JobQueued,
			InstallationID: c.owningInstallation(ctx, q.Repo, polled),
			RunnerName:     q.RunnerName,
			HTMLURL:        q.HTMLURL,
			QueuedAt:       q.QueuedAt,
		}
		if job.QueuedAt.IsZero() {
			job.QueuedAt = c.Now()
		}
		if p := scheduler.BestPool(pools, job); p != nil {
			job.PoolID = p.ID
			job.Matched = true
		}
		saved, change, err := c.st.ApplyJob(ctx, job)
		if err != nil {
			c.log.Warn("could not record a polled job", "github_job_id", q.ID, "error", err)
			continue
		}
		c.recordJobChange(ctx, saved, change, sourcePoller, nil)
		if saved.State == store.JobQueued {
			changed++
		}
		c.publishJob(ctx, saved)
	}
	return changed, nil
}

// ingestStartedJob records a job a sweep found running on one of this fleet's
// runners before anything here had recorded it as started.
//
// Without webhooks that is the ordinary case, not a rare one: an idle runner
// takes a job within a second or two of GitHub queuing it, far inside the
// sweep interval, so the job is never seen queued at all. Nothing else moves a
// runner to busy, and reconcileKnownJobs only rechecks rows that exist, so the
// runner stayed idle on paper for the whole build and stood in for the next
// queued job, which then waited for a runner the pool had room to start.
//
// It goes through applyWorkflowJob because that is what a delivered
// in_progress would have gone through: one owner for the job row, the runner
// link and the busy transition, with the store refusing to move either
// backwards. Two cases are left where they were. A job this controller already
// has as started or finished belongs to reconcileKnownJobs, and applying it
// again on every sweep would cost a write and a scheduling pass per running
// job for nothing. A job running somewhere else occupies nothing here, and a
// row for it would never be reconciled to a completion.
func (c *Controller) ingestStartedJob(ctx context.Context, q github.QueuedJob) {
	if q.RunnerName == "" {
		return
	}
	known, err := c.st.GetJobByGitHubID(ctx, q.ID)
	switch {
	case err == nil:
		if known.State == store.JobInProgress || known.State == store.JobCompleted {
			return
		}
	case !errors.Is(err, store.ErrNotFound):
		c.log.Warn("could not look up a polled running job", "github_job_id", q.ID, "error", err)
		return
	}
	if _, err := c.st.GetRunnerByName(ctx, q.RunnerName); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("could not look up the runner of a polled running job",
				"github_job_id", q.ID, "runner", q.RunnerName, "error", err)
		}
		return
	}
	started := &github.WorkflowJobEvent{
		Action:       string(store.JobInProgress),
		JobID:        q.ID,
		RunID:        q.RunID,
		Repo:         q.Repo,
		WorkflowName: q.WorkflowName,
		JobName:      q.JobName,
		Labels:       q.Labels,
		RunnerName:   q.RunnerName,
		Status:       q.Status,
		QueuedAt:     q.QueuedAt,
		HTMLURL:      q.HTMLURL,
	}
	if q.StartedAt != nil {
		started.StartedAt = *q.StartedAt
	}
	if err := c.applyWorkflowJob(ctx, started, sourcePoller); err != nil {
		c.log.Warn("could not record a polled running job", "github_job_id", q.ID, "error", err)
	}
}

// holdRateLimited stands every background sweep down from one installation for
// as long as GitHub asked, and says so once.
func (c *Controller) holdRateLimited(id string, err error, now time.Time, doing string) {
	until := rateLimitHold(err, now)
	c.holdGitHub(id, until)
	c.log.Warn("GitHub rate-limited this installation; standing down from it",
		"installation", id, "while", doing, "until", until.UTC().Format(time.RFC3339), "error", err)
}

// githubHeld reports whether an installation is inside its rate-limit backoff,
// clearing the hold once it has expired so the map does not grow with the
// installations that have long since recovered.
func (c *Controller) githubHeld(id string, now time.Time) bool {
	c.githubMu.Lock()
	defer c.githubMu.Unlock()
	until, ok := c.githubPaused[id]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(c.githubPaused, id)
		return false
	}
	return true
}

// heldInstallations lists the installations currently inside a rate-limit
// backoff, with the moment each may be used again.
//
// Expired holds are dropped here as they are in githubHeld, so a caller that
// only reads never leaves the map growing with installations that recovered
// hours ago.
func (c *Controller) heldInstallations(now time.Time) map[string]time.Time {
	c.githubMu.Lock()
	defer c.githubMu.Unlock()
	held := make(map[string]time.Time, len(c.githubPaused))
	for id, until := range c.githubPaused {
		if !now.Before(until) {
			delete(c.githubPaused, id)
			continue
		}
		held[id] = until
	}
	return held
}

// holdGitHub stands the poller down for one installation until a moment.
func (c *Controller) holdGitHub(id string, until time.Time) {
	c.githubMu.Lock()
	defer c.githubMu.Unlock()
	if c.githubPaused == nil {
		c.githubPaused = map[string]time.Time{}
	}
	c.githubPaused[id] = until
}

// owningInstallation is the installation covering a repository, with a
// fallback for the caller that already knows one. A lookup failure is not
// worth abandoning an ingest over: the poller's own installation is a worse
// answer than the repository's and a much better one than none.
func (c *Controller) owningInstallation(ctx context.Context, repo string, fallback *store.Installation) string {
	if owner, err := c.st.FindInstallationByTarget(ctx, repo); err == nil {
		return owner.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		c.log.Warn("could not resolve the installation covering a polled repository",
			"repo", repo, "error", err)
	}
	return installationID(fallback)
}

// A healthy stream of unrelated webhooks does not prove every completion was
// delivered. Check a rotating, bounded page of older unfinished jobs even when
// queue discovery stands down. A 404 or API failure is not a cancellation.
func (c *Controller) reconcileKnownJobs(ctx context.Context, now time.Time) {
	const batch = 10
	cutoff := now.Add(-2 * time.Minute)
	offset := int(c.jobPollOffset.Load())
	jobs, total, err := c.st.ListJobs(ctx, store.JobFilter{
		States: []store.JobState{store.JobQueued, store.JobInProgress}, Until: &cutoff, ManagedOnly: true,
	}, store.Page{Limit: batch, Offset: offset})
	if err != nil {
		c.log.Warn("could not list jobs to reconcile", "error", err)
		return
	}
	next := offset + len(jobs)
	if next >= total {
		next = 0
	}
	c.jobPollOffset.Store(int64(next))
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		if j.InstallationID == "" || IsDemoID(j.InstallationID) || c.githubHeld(j.InstallationID, now) {
			continue
		}
		inst, err := c.st.GetInstallation(ctx, j.InstallationID)
		if err != nil {
			continue
		}
		client, err := c.clients.get(ctx, inst)
		if err != nil {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		remote, err := client.GetWorkflowJob(checkCtx, j.Repo, j.GitHubJobID)
		cancel()
		c.observeGitHub(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrRateLimited) {
				c.holdRateLimited(inst.ID, err, now, "reconciling jobs")
			}
			c.log.Warn("could not reconcile workflow job", "job", j.ID, "error", err)
			continue
		}
		if remote.State() != store.JobCompleted && remote.State() != store.JobInProgress {
			continue
		}
		if err := c.applyWorkflowJob(ctx, remote, sourcePoller); err != nil {
			c.log.Warn("could not update reconciled workflow job", "job", j.ID, "error", err)
		}
	}
}

// jobRecoveryLoop is the single owner of checking known unfinished jobs against
// GitHub, and it runs whether or not the fallback poller is on.
//
// That check is not about discovering queued work; it is the only thing that
// notices a completion whose webhook never arrived, and an in-progress job
// nothing ever completes counts against its repository's scale-up limit for
// ever and keeps its runner's host from being cleaned up. Nothing else may
// call it on a timer: a second caller doubles every GitHub call and races on
// the rotation offset. A fleet that has lost its lease is left alone for the
// same reason the poller leaves it, since it cannot act on what it would learn.
func (c *Controller) jobRecoveryLoop(ctx context.Context) {
	interval := c.pollInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if c.mayAct() {
			c.reconcileKnownJobs(ctx, c.Now())
			c.recoveries.Add(1)
		}
		if !c.awaitRecoveryTick(ctx, ticker, &interval) {
			return
		}
	}
}

// awaitRecoveryTick blocks until the next sweep is due and reports false when
// the context ended first.
//
// A settings change wakes it too, since this loop spends the most installation
// quota of any and an operator who raises github.poll_interval to relieve a
// rate limit needs it retuned now, not at the next restart. Any setting wakes
// it, so it retunes and keeps waiting rather than sweeping: a change to
// something unrelated must not cost ten GitHub calls.
func (c *Controller) awaitRecoveryTick(ctx context.Context, ticker *time.Ticker, interval *time.Duration) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			return true
		case <-c.recoverySettingsChanged:
			if d := c.pollInterval(); d != *interval {
				*interval = d
				ticker.Reset(d)
			}
		}
	}
}
