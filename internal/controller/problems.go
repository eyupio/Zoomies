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
// problems drawer should never have to go and look up what a code means.
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
	// Alternatives are the choices the fix leaves open, when the fix is a
	// choice: for a pool no host can run, the backends its hosts do offer. They
	// are carried apart from the prose so the UI can put the change one click
	// away rather than leaving an operator to find the pool's edit form.
	Alternatives []string `json:"alternatives,omitempty"`
	// Since is when the situation started, where that is knowable.
	Since *time.Time `json:"since,omitempty"`
}

// problemWindow is how far back rejected webhook deliveries are counted. An
// hour is long enough to catch a secret that was changed on one side only, and
// short enough that yesterday's fixed problem is not still on the list.
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
	// whether or not anything has gone wrong yet. ForUI drops the handful that
	// only the CLI says, because they are expected in a normal deployment and
	// a list that is never clear stops being read.
	for _, f := range c.cfg.Validate().ForUI() {
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
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing installations: %w", err)
	}
	installations := make(map[string]*store.Installation, len(insts))
	for _, i := range insts {
		installations[i.ID] = i
	}
	for _, p := range pools {
		// The same sentences the pool's own page shows, from the same place.
		out = append(out, PoolWarnings(p, installations[p.InstallationID])...)
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
	out = append(out, c.PoolCapacityProblems()...)
	if err := c.runnerProblems(ctx, &out); err != nil {
		return nil, err
	}
	if err := c.capacityDeliveryProblems(ctx, &out); err != nil {
		return nil, err
	}

	// Errors first, then warnings, then a stable order so the list does not
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

func (c *Controller) capacityDeliveryProblems(ctx context.Context, out *[]Problem) error {
	rows, err := c.st.ListCapacityDemandDeliveries(ctx)
	if err != nil {
		return fmt.Errorf("listing capacity-demand deliveries: %w", err)
	}
	for _, d := range rows {
		if d.DeliveredAt != nil || d.LastError == "" {
			continue
		}
		since := d.AttemptedAt
		*out = append(*out, Problem{Code: "capacity_demand.delivery_failed", Severity: config.SeverityWarning, Title: "external capacity provisioner did not accept the latest event", Detail: fmt.Sprintf("%s after %d attempts (HTTP %d): %s", d.EventType, d.Attempts, d.StatusCode, d.LastError), Fix: "check capacity_demand.destination_url, receiver availability, and the shared signing secret; Zoomies will retry on reconciliation.", TargetKind: "pool", TargetID: d.PoolID, Since: since})
	}
	return nil
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

// PoolCapacityProblems reports the pools the scheduler wanted to grow and could
// not place anywhere. It is exported because the pool's own page shows it too:
// "why is this pool not running anything?" is asked on the pool, not only on
// the Overview.
//
// This is the failure that looks exactly like health: the pool is enabled, its
// labels match the queue, every host is connected, and no runner is ever
// created because none of those hosts offers the pool's backend or matches its
// host selector. Nothing else in the product says so -- a scaling event is only
// written when the size actually moved -- so a fleet in this state answers
// "why is nothing running?" with silence unless it is reported here.
func (c *Controller) PoolCapacityProblems() []Problem {
	plan, at := c.getLastPlan()
	if plan == nil || at.IsZero() {
		return nil
	}
	var out []Problem
	for _, pp := range plan.Pools {
		if pp.QuotaDeferredJobs > 0 {
			repositories := strings.Join(pp.QuotaDeferredRepositories, ", ")
			out = append(out, Problem{
				Code:     "pool.repository_scale_up_deferred",
				Severity: config.SeverityWarning,
				Title: fmt.Sprintf("pool %s deferred %s from scaling", pp.PoolName,
					plural(pp.QuotaDeferredJobs, "job")),
				Detail: fmt.Sprintf("The best-effort repository scale-up limit deferred runner creation for %s (%s). Compatible idle runners may still accept these jobs because GitHub controls assignment.",
					plural(len(pp.QuotaDeferredRepositories), "repository"), repositories),
				Fix:        "increase the pool repository scale-up limit or wait for that repository's active jobs to finish; use repository-specific pools and workflow labels if strict isolation is required",
				TargetKind: "pool", TargetID: pp.PoolID,
			})
		}
		if pp.Blocked == "" {
			continue
		}
		// Jobs already waiting make this an outage rather than a warning about
		// a pool that is merely unable to reach its minimum -- unless the fleet
		// is simply full, which is the system working and which the next
		// finished job clears on its own.
		severity := config.SeverityWarning
		title := fmt.Sprintf("pool %s cannot start the runners it wants", pp.PoolName)
		switch {
		case pp.BlockedAtCapacity && pp.QueuedMatched > 0:
			title = fmt.Sprintf("pool %s has %s waiting for a host with room",
				pp.PoolName, plural(pp.QueuedMatched, "job"))
		case pp.QueuedMatched > 0:
			severity = config.SeverityError
			title = fmt.Sprintf("pool %s has %s waiting and nowhere to run them",
				pp.PoolName, plural(pp.QueuedMatched, "job"))
		}
		out = append(out, Problem{
			Code:         "pool.no_capacity",
			Severity:     severity,
			Title:        title,
			Detail:       pp.Blocked,
			Fix:          pp.BlockedFix,
			Alternatives: pp.BlockedAlternatives,
			TargetKind:   "pool", TargetID: pp.PoolID,
		})
	}
	return out
}

// plural writes "1 job" and "3 jobs".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func (c *Controller) jobProblems(ctx context.Context, out *[]Problem) error {
	if err := c.lostRunnerProblems(ctx, out); err != nil {
		return err
	}
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

// lostRunnerProblems reports jobs whose runner stopped under them in the last
// hour. GitHub records these as failures like any test failure, and a team
// that sees "CI is flaky" when the fleet is killing their jobs will blame the
// wrong thing; this is where the fleet owns up.
func (c *Controller) lostRunnerProblems(ctx context.Context, out *[]Problem) error {
	faulted, _, err := c.st.ListJobs(ctx, store.JobFilter{FaultedOnly: true},
		store.Page{Limit: 100, Sort: "queued_at", Desc: true})
	if err != nil {
		return fmt.Errorf("listing jobs that lost their runner: %w", err)
	}
	since := c.Now().Add(-problemWindow)
	recent := faulted[:0]
	for _, j := range faulted {
		// Filtered here rather than in SQL because the moment that matters
		// is when the runner went, and the nearest stored stamp to that is
		// the job's completion -- or, for a job GitHub still thinks is
		// running, now.
		if j.CompletedAt == nil || j.CompletedAt.After(since) {
			recent = append(recent, j)
		}
	}
	if len(recent) == 0 {
		return nil
	}
	example := recent[0]
	at := example.StartedAt
	if at == nil {
		at = &example.QueuedAt
	}
	title := fmt.Sprintf("%d jobs lost the runners they were running on in the last hour", len(recent))
	if len(recent) == 1 {
		title = "1 job lost the runner it was running on in the last hour"
	}
	*out = append(*out, Problem{
		Code:     "jobs.runner_lost",
		Severity: config.SeverityWarning,
		Title:    title,
		Detail: fmt.Sprintf("GitHub records these as ordinary failures. The most recent is %s in %s: %s.",
			example.JobName, example.Repo, example.RunnerFault),
		Fix:        "open the job for its timeline and the runner for its last output; a runner that dies mid-job has usually run out of memory or disk, or was removed with force. Re-run the workflow once the cause is fixed.",
		TargetKind: "job", TargetID: example.ID, Since: at,
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
