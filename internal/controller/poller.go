package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// rateLimitBackoff is how long the poller stands down after GitHub says the
// installation is out of quota. Polling is the fallback path; spending the
// last of an installation's quota on it would take the webhook path's own API
// calls -- minting JIT configs -- down with it.
const rateLimitBackoff = 15 * time.Minute

// pollLoop is the webhook fallback.
//
// A controller behind NAT, or one whose webhook secret was mistyped, would
// otherwise stop scaling silently, and a fleet that has quietly stopped
// scaling looks exactly like a quiet fleet.
func (c *Controller) pollLoop(ctx context.Context) {
	if !c.cfg.GitHub.PollFallback {
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
			c.pollOnce(ctx)
		}
	}
}

func (c *Controller) pollInterval() time.Duration {
	if d := c.cfg.GitHub.PollInterval; d > 0 {
		return d
	}
	return 30 * time.Second
}

// pollOnce lists queued jobs for every installation and records what it finds.
//
// It skips entirely when a webhook has arrived within the last two poll
// intervals. That single check is what makes the fallback cheap: on a working
// installation the poller costs one query against the local database per
// interval and no GitHub calls at all, so leaving it on by default does not
// spend an organisation's API quota.
func (c *Controller) pollOnce(ctx context.Context) {
	now := c.Now()
	if until := c.pollPausedUntil.Load(); until > 0 && now.UnixNano() < until {
		return
	}

	last, err := c.st.LastDeliveryAt(ctx)
	if err != nil {
		c.log.Error("could not tell when the last webhook arrived", "error", err)
		return
	}
	c.pollingOnly.Store(last.IsZero())
	if !last.IsZero() && now.Sub(last) < 2*c.pollInterval() {
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
		client, err := c.clients.get(ctx, inst)
		if err != nil {
			c.log.Warn("skipping an installation while polling", "installation", inst.ID, "error", err)
			continue
		}
		jobs, err := client.ListQueuedJobs(ctx)
		c.observeGitHub(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrRateLimited) {
				c.pollPausedUntil.Store(now.Add(rateLimitBackoff).UnixNano())
				c.log.Warn("GitHub rate-limited the fallback poller; standing down",
					"installation", inst.ID, "backoff", rateLimitBackoff, "error", err)
				return
			}
			c.log.Warn("could not poll for queued jobs", "installation", inst.ID, "error", err)
			continue
		}
		found += len(jobs)
		n, err := c.ingestQueuedJobs(ctx, jobs)
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
}

// ingestQueuedJobs folds polled jobs into the same rows the webhook path
// writes, so both paths feed one scheduler and one history.
func (c *Controller) ingestQueuedJobs(ctx context.Context, jobs []github.QueuedJob) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing pools to match polled jobs: %w", err)
	}

	changed := 0
	for _, q := range jobs {
		job := &store.Job{
			GitHubJobID: q.ID,
			GitHubRunID: q.RunID,
			Repo:        q.Repo,
			Workflow:    q.WorkflowName,
			JobName:     q.JobName,
			Labels:      store.NormalizeLabels(q.Labels),
			State:       store.JobQueued,
			RunnerName:  q.RunnerName,
			HTMLURL:     q.HTMLURL,
			QueuedAt:    q.QueuedAt,
		}
		if job.QueuedAt.IsZero() {
			job.QueuedAt = c.Now()
		}
		if p := scheduler.BestPool(pools, job.Labels); p != nil {
			job.PoolID = p.ID
			job.Matched = true
		}
		saved, err := c.st.UpsertJob(ctx, job)
		if err != nil {
			c.log.Warn("could not record a polled job", "github_job_id", q.ID, "error", err)
			continue
		}
		if saved.State == store.JobQueued {
			changed++
		}
		c.publish(events.KindJobUpdated, "job:"+saved.ID, saved)
	}
	return changed, nil
}
