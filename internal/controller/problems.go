package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// Problem is one thing an operator should know about, in the shape the API's
// /problems endpoint returns.
//
// It carries the same four sentences a config.Finding does -- what is true,
// why it matters, and what to change -- because an operator reading the
// problems panel should never have to go and look up what a code means.
type Problem struct {
	// Code is a stable identifier such as "host.unhealthy", suitable for
	// grouping or suppressing in an alerting rule.
	Code     string          `json:"code"`
	Severity config.Severity `json:"severity"`
	// Setting names the configuration key involved, when there is one.
	Setting string `json:"setting,omitempty"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
	// TargetKind and TargetID let the UI link a problem to the pool, host,
	// runner or installation it is about.
	TargetKind string `json:"target_kind,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	// Since is when the situation started, where that is knowable.
	Since *time.Time `json:"since,omitempty"`
}

// problemWindow is how far back rejected webhook deliveries are counted. An
// hour is long enough to catch a secret that was changed on one side only, and
// short enough that yesterday's fixed problem is not still on the panel.
const problemWindow = time.Hour

// Problems aggregates everything currently wrong and every dangerous setting
// in effect, worst first.
//
// It returns an empty slice rather than nil when there is nothing to say,
// because the UI renders "nothing needs your attention" from exactly that.
func (c *Controller) Problems(ctx context.Context) ([]Problem, error) {
	out := make([]Problem, 0, 8)

	// Configuration: every warning and error the validator produced. These are
	// the settings that trade safety for convenience, and they are listed
	// whether or not anything has gone wrong yet.
	for _, f := range c.cfg.Validate() {
		if f.Severity != config.SeverityError && f.Severity != config.SeverityWarning {
			continue
		}
		out = append(out, Problem{
			Code: f.Code, Severity: f.Severity, Setting: f.Setting,
			Title: f.Title, Detail: f.Detail, Fix: f.Fix,
		})
	}

	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	for _, p := range pools {
		for _, d := range p.Dangerous() {
			out = append(out, Problem{
				Code:       "pool.dangerous",
				Severity:   config.SeverityWarning,
				Title:      fmt.Sprintf("pool %s: %s", p.Name, d),
				Detail:     "this pool was configured to weaken the isolation between a workflow job and the host it runs on.",
				Fix:        fmt.Sprintf("edit the %s pool if this was not deliberate.", p.Name),
				TargetKind: "pool", TargetID: p.ID,
			})
		}
	}

	if err := c.hostProblems(ctx, &out); err != nil {
		return nil, err
	}
	if err := c.installationProblems(ctx, &out); err != nil {
		return nil, err
	}
	if err := c.webhookProblems(ctx, &out); err != nil {
		return nil, err
	}
	if err := c.jobProblems(ctx, &out); err != nil {
		return nil, err
	}
	if err := c.runnerProblems(ctx, &out); err != nil {
		return nil, err
	}

	// Errors first, then warnings, then a stable order so the panel does not
	// reshuffle itself between refreshes.
	slices.SortStableFunc(out, func(a, b Problem) int {
		if r := severityRank(a.Severity) - severityRank(b.Severity); r != 0 {
			return r
		}
		if r := strings.Compare(a.Code, b.Code); r != 0 {
			return r
		}
		return strings.Compare(a.Title, b.Title)
	})
	return out, nil
}

func severityRank(s config.Severity) int {
	switch s {
	case config.SeverityError:
		return 0
	case config.SeverityWarning:
		return 1
	default:
		return 2
	}
}

func (c *Controller) hostProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return fmt.Errorf("listing queued jobs: %w", err)
	}
	now := c.Now()

	for _, h := range hosts {
		if !h.Healthy(now) {
			since := h.LastHeartbeat
			severity := config.SeverityWarning
			detail := fmt.Sprintf("no heartbeat for %s; the agent process may be stopped, or it cannot reach this controller.",
				now.Sub(h.LastHeartbeat).Round(time.Second))
			if h.ActiveRunners > 0 {
				// Runners on a silent host are unaccounted for, which is worse
				// than a spare host being down.
				severity = config.SeverityError
				detail = fmt.Sprintf("no heartbeat for %s, and %d runner(s) are recorded on it, so their state is unknown.",
					now.Sub(h.LastHeartbeat).Round(time.Second), h.ActiveRunners)
			}
			*out = append(*out, Problem{
				Code:       "host.unhealthy",
				Severity:   severity,
				Title:      fmt.Sprintf("host %s has stopped sending heartbeats", h.Name),
				Detail:     detail,
				Fix:        fmt.Sprintf("check that the zoomies agent is running on %s and can reach %s.", h.Name, c.controllerAddress()),
				TargetKind: "host", TargetID: h.ID, Since: &since,
			})
			continue
		}
		if h.Cordoned && len(queued) > 0 {
			*out = append(*out, Problem{
				Code:       "host.cordoned_with_work",
				Severity:   config.SeverityWarning,
				Title:      fmt.Sprintf("host %s is cordoned while %d job(s) are queued", h.Name, len(queued)),
				Detail:     "a cordoned host keeps its runners but accepts no new ones, so its capacity is not available to the queue.",
				Fix:        fmt.Sprintf("uncordon %s on the Hosts page if the maintenance it was cordoned for is over.", h.Name),
				TargetKind: "host", TargetID: h.ID,
			})
		}
	}
	return nil
}

func (c *Controller) installationProblems(ctx context.Context, out *[]Problem) error {
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}
	for _, i := range insts {
		if i.Healthy() {
			continue
		}
		*out = append(*out, Problem{
			Code:       "installation.unhealthy",
			Severity:   config.SeverityError,
			Title:      fmt.Sprintf("the GitHub App installation for %s is not usable", i.Target),
			Detail:     i.LastError,
			Fix:        "check the App's installation, permissions and private key on the Installations page; no runner can be created for this target until it works.",
			TargetKind: "installation", TargetID: i.ID, Since: i.LastCheckedAt,
		})
	}
	return nil
}

func (c *Controller) webhookProblems(ctx context.Context, out *[]Problem) error {
	since := c.Now().Add(-problemWindow)
	rejected, err := c.st.CountFailedDeliveries(ctx, since)
	if err != nil {
		return fmt.Errorf("counting failed webhook deliveries: %w", err)
	}
	if rejected > 0 {
		*out = append(*out, Problem{
			Code:     "webhook.rejected",
			Severity: config.SeverityWarning,
			Setting:  "github.webhook_path",
			Title:    fmt.Sprintf("%d webhook deliveries were rejected in the last hour", rejected),
			Detail:   "a rejected delivery is one whose signature did not verify. Either the App's webhook secret no longer matches the one Zoomies holds, or something other than GitHub is posting to this endpoint.",
			Fix:      "compare the webhook secret on the GitHub App with the one on the Installations page, then use GitHub's Redeliver button.",
			Since:    &since,
		})
	}

	last, err := c.st.LastDeliveryAt(ctx)
	if err != nil {
		return fmt.Errorf("reading the last webhook delivery time: %w", err)
	}
	// An instance with no installation is not yet configured, and telling its
	// operator that no webhook has arrived would be noise on top of the setup
	// they have not finished. Once an installation exists, silence is a fault.
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}
	if last.IsZero() && len(insts) > 0 {
		p := Problem{
			Code:     "webhook.never_received",
			Severity: config.SeverityWarning,
			Setting:  "server.external_url",
			Title:    "no webhook has ever arrived, so scaling is running on the poller",
			Detail: fmt.Sprintf("Zoomies has never received a delivery, so it is discovering queued jobs by polling GitHub every %s "+
				"instead of within a second of them being queued.", c.pollInterval()),
			Fix: fmt.Sprintf("point the App's webhook at %s and check that GitHub can reach it.", c.webhookURLOrPath()),
		}
		if !c.cfg.GitHub.PollFallback {
			// With no webhooks and no poller, nothing will ever start a runner.
			p.Severity = config.SeverityError
			p.Title = "no webhook has ever arrived and the fallback poller is off, so nothing is scaling"
			p.Detail = "Zoomies has never received a delivery and github.poll_fallback is false, so no queued job will ever be noticed."
			p.Fix = fmt.Sprintf("point the App's webhook at %s, or set github.poll_fallback to true.", c.webhookURLOrPath())
		}
		*out = append(*out, p)
	}
	return nil
}

// controllerAddress is how an agent reaches this controller, phrased for a
// message even when the external URL has not been set.
func (c *Controller) controllerAddress() string {
	if u := c.cfg.Server.ExternalURL; u != "" {
		return u
	}
	return "this controller (server.external_url is not set, so Zoomies cannot name the address)"
}

// webhookURLOrPath names the delivery target, falling back to the path when
// the external URL has not been configured -- which is itself one of the
// findings above, so the fix stays actionable either way.
func (c *Controller) webhookURLOrPath() string {
	if u := c.cfg.WebhookURL(); u != "" {
		return u
	}
	return c.cfg.GitHub.WebhookPath + " (set server.external_url so Zoomies can tell you the full URL)"
}

func (c *Controller) jobProblems(ctx context.Context, out *[]Problem) error {
	unmatched, err := c.unmatchedQueuedJobs(ctx)
	if err != nil {
		return err
	}
	if len(unmatched) == 0 {
		return nil
	}
	example := unmatched[0]
	labels := strings.Join(example.Labels, ", ")
	*out = append(*out, Problem{
		Code:     "jobs.unmatched",
		Severity: config.SeverityWarning,
		Title:    fmt.Sprintf("%d queued job(s) match no enabled pool", len(unmatched)),
		Detail: fmt.Sprintf("nothing will run them. The oldest is %s in %s, asking for [%s].",
			example.JobName, example.Repo, labels),
		Fix:        "create or enable a pool advertising those labels, or change the workflow's runs-on.",
		TargetKind: "job", TargetID: example.ID, Since: &example.QueuedAt,
	})
	return nil
}

// unmatchedQueuedJobs prefers the last scheduler decision, which is computed
// against the pools as they are now; it falls back to the flag stored on each
// job when no pass has run yet, so a fresh controller still reports honestly.
func (c *Controller) unmatchedQueuedJobs(ctx context.Context) ([]*store.Job, error) {
	if plan, at := c.getLastPlan(); plan != nil && !at.IsZero() {
		return plan.Unmatched, nil
	}
	jobs, _, err := c.st.ListJobs(ctx, store.JobFilter{
		States:        []store.JobState{store.JobQueued},
		UnmatchedOnly: true,
	}, store.Page{Limit: 100, Sort: "queued_at"})
	if err != nil {
		return nil, fmt.Errorf("listing unmatched jobs: %w", err)
	}
	return jobs, nil
}

func (c *Controller) runnerProblems(ctx context.Context, out *[]Problem) error {
	failed, _, err := c.st.ListRunners(ctx, store.RunnerFilter{
		States: []store.RunnerState{store.RunnerFailed},
	}, store.Page{Limit: 100})
	if err != nil {
		return fmt.Errorf("listing failed runners: %w", err)
	}
	if len(failed) == 0 {
		return nil
	}
	example := failed[0]
	*out = append(*out, Problem{
		Code:       "runners.failed",
		Severity:   config.SeverityWarning,
		Title:      fmt.Sprintf("%d runner(s) are in the failed state", len(failed)),
		Detail:     fmt.Sprintf("the most recent is %s: %s", example.Name, example.Message),
		Fix:        "look at the runner's logs on the Runners page; failed runners are cleaned up automatically but the cause is not.",
		TargetKind: "runner", TargetID: example.ID, Since: &example.CreatedAt,
	})
	return nil
}
