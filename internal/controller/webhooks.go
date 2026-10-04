package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// maxWebhookBody caps an inbound delivery. GitHub's own limit is 25 MB, but a
// workflow_job payload is tens of kilobytes; anything approaching this is
// either not from GitHub or not something Zoomies should be parsing. The body
// is read into memory before its signature can be checked -- the repository in
// it chooses the secret -- so this is what one request on a public endpoint
// costs the controller whoever sends it, and a megabyte is still some twenty
// times the largest delivery the fleet acts on.
const maxWebhookBody = 1 << 20

// What a delivery is allowed to say about itself before its signature has been
// checked.
//
// Everything below is read off an unauthenticated request: two headers, and
// three fields of a body that is parsed before verification because the
// repository is what chooses the secret. All of it is then written down --
// a database row that is kept, a line in the log, and a frame on the event
// stream every watching browser holds in memory -- because a rejected delivery
// is worth recording. Unbounded, that is an open invitation: a megabyte of
// "repository.full_name" per request, from anyone who can reach the endpoint,
// with no credential of any kind.
//
// The real values are far smaller. A GitHub repository's full name cannot
// exceed 100 characters either side of the slash, a delivery ID is a UUID, and
// an event name and an action are single words. These leave room and still
// bound the cost.
const (
	maxDeliveryRepo   = 256
	maxDeliveryAction = 64
	maxDeliveryID     = 64
	maxDeliveryEvent  = 64
)

// probeKey is who a rejected delivery is counted against: the address it came
// from, without its port, since a prober gets a new port every connection.
func probeKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clampField bounds one such value. It reports what was cut rather than
// truncating silently, so an operator reading the row sees a probe for what it
// is instead of a repository name that looks merely unfamiliar.
func clampField(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("... (%d bytes, truncated)", len(s))
}

// HandleWebhook is the endpoint GitHub delivers to, mounted by the API at
// config.GitHub.WebhookPath.
//
// It answers quickly on purpose. GitHub gives a webhook ten seconds before it
// records the delivery as failed, so this verifies, writes one row and returns;
// the scheduling it triggers happens on the reconcile loop, which nothing here
// waits for.
func (c *Controller) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "webhook deliveries are POSTed; this endpoint accepts nothing else", http.StatusMethodNotAllowed)
		return
	}

	event := clampField(github.ParseEventType(r.Header.Get(github.EventTypeHeader)), maxDeliveryEvent)
	d := &store.WebhookDelivery{
		DeliveryID: clampField(r.Header.Get(github.DeliveryIDHeader), maxDeliveryID),
		Event:      event,
	}

	// A delivery with no signature header cannot verify, so it is refused
	// before its body is read: reading, parsing and checking a body against
	// every installation's secret is the expensive part of this handler, and
	// it is the part a probe of the endpoint would otherwise get for free.
	// The refusal is recorded through the same bounded path as a delivery
	// whose signature was wrong, so a burst of them is still visible.
	signature := r.Header.Get(github.SignatureHeader)
	if strings.TrimSpace(signature) == "" {
		c.rejectDelivery(ctx, w, r, d, event, "",
			fmt.Errorf("the delivery carries no %s header, so it cannot have come from GitHub with the webhook secret Zoomies holds", github.SignatureHeader))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		c.recordDelivery(ctx, d, "error", fmt.Sprintf("could not read the delivery body (limit %d bytes): %v", maxWebhookBody, err))
		http.Error(w, fmt.Sprintf("the delivery body could not be read, or was larger than %d MiB", maxWebhookBody>>20), http.StatusRequestEntityTooLarge)
		return
	}

	// The envelope is read before the signature is checked, because the
	// repository is what selects the secret to check the signature with.
	// Nothing from it is acted on until the signature verifies.
	env := parseEnvelope(body)
	d.Repo, d.Action = env.Repo, env.Action

	inst, note, err := c.verifyDelivery(ctx, body, signature, env.Repo)
	if err != nil {
		c.rejectDelivery(ctx, w, r, d, event, env.Repo, err)
		return
	}

	// A delivery that verified proves webhooks reach this controller.
	c.pollingOnly.Store(false)
	d.InstallationID = installationID(inst)

	switch {
	case github.IsPing(event):
		c.recordDelivery(ctx, d, "accepted", note)
		c.log.Info("GitHub's webhook ping arrived; deliveries can reach this controller",
			"installation", installationID(inst), "repo", env.Repo)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "Zoomies received the ping; webhook delivery to this controller works",
		})
	case event == "workflow_job":
		if err := c.handleWorkflowJob(ctx, body); err != nil {
			if errors.Is(err, errMalformedDelivery) {
				// A body that verified but is not a workflow_job event is
				// GitHub's problem or a mistaken sender's, not a failure on
				// this side, and a 500 would have GitHub redeliver it for
				// ever. A 400 is recorded and ends there.
				c.recordDelivery(ctx, d, "rejected", err.Error())
				c.log.Warn("rejected a workflow_job delivery that could not be read", "delivery", d.DeliveryID, "error", err)
				http.Error(w, "the delivery body is not a workflow_job event this controller can read", http.StatusBadRequest)
				return
			}
			c.recordDelivery(ctx, d, "error", err.Error())
			c.log.Error("could not apply a workflow_job delivery", "delivery", d.DeliveryID, "error", err)
			// A 500 makes GitHub's redelivery button useful: this one failed
			// on our side and is worth sending again.
			http.Error(w, "the delivery could not be processed", http.StatusInternalServerError)
			return
		}
		c.recordDelivery(ctx, d, "accepted", note)
		w.WriteHeader(http.StatusAccepted)
	default:
		// Zoomies subscribes to workflow_job only; anything else is recorded
		// so the delivery log explains what is arriving and ignored.
		c.recordDelivery(ctx, d, "accepted", note)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Zoomies does not act on %q events; only workflow_job", event),
		})
	}
}

// envelope is the part of any delivery that routes it, read before the
// signature has been checked.
type envelope struct {
	Repo           string
	Action         string
	InstallationID int64
}

func parseEnvelope(body []byte) envelope {
	var p struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
		// The installation GitHub says sent this. It is read for the record
		// only: it is GitHub's numeric identifier rather than the store's, and
		// a delivery asserting which installation it belongs to would be the
		// delivery choosing its own scope. The repository decides instead.
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return envelope{}
	}
	repo := p.Repository.FullName
	if repo == "" {
		// A ping for an org-wide App carries no repository, but the org is
		// enough to find the installation that should have signed it.
		repo = p.Organization.Login
	}
	return envelope{
		Repo:           clampField(repo, maxDeliveryRepo),
		Action:         clampField(strings.ToLower(strings.TrimSpace(p.Action)), maxDeliveryAction),
		InstallationID: p.Installation.ID,
	}
}

// rejectDelivery answers a delivery that could not be verified, and records it
// within bounds.
//
// A burst of these means somebody is probing the endpoint, which is why they
// are written down rather than only counted -- but written down is what costs,
// so one address gets a bounded number of them.
//
// The limit is on the rejected path alone, deliberately. GitHub sends in bursts
// from a range of addresses and a real delivery has a valid signature, so
// nothing that verifies is ever slowed by this; only something that could not
// have come from GitHub is. Rate-limiting the endpoint itself would throttle
// the deliveries the fleet scales on.
//
// The refusal is unchanged either way: what is dropped is the record, not the
// rejection.
func (c *Controller) rejectDelivery(ctx context.Context, w http.ResponseWriter, r *http.Request, d *store.WebhookDelivery, event, repo string, err error) {
	if c.webhookProbes.Allow(probeKey(r)) {
		c.recordDelivery(ctx, d, "rejected", err.Error())
		c.log.Warn("rejected a webhook delivery",
			"delivery", d.DeliveryID, "event", event, "repo", repo, "reason", err)
	} else {
		c.metrics.webhookDeliveries.WithLabelValues("rejected").Inc()
	}
	http.Error(w, "the delivery signature could not be verified", http.StatusUnauthorized)
}

// verifyDelivery finds the secret this delivery should have been signed with
// and checks the signature in constant time (github.ValidateSignature uses
// hmac.Equal).
//
// The installation that owns the repository decides it. Only when no
// installation covers the repository at all -- which happens when a repository
// moved between organisations -- is every other configured secret tried, and
// the note that comes back says which one worked, because "your secrets are out
// of step with your installations" is a different problem from "this delivery
// was forged".
//
// The fallback is deliberately not reached when an owner exists and its secret
// fails. Every configured secret is held by somebody: on a controller serving
// two organisations, each one's GitHub admin pasted their own. Trying them all
// against a delivery that names somebody else's repository let the holder of
// one organisation's secret sign a workflow_job for another's, and be believed
// -- the job is then scheduled on the named repository's pools and a runner is
// registered in its organisation with its own App credentials. Nor did the
// wider net ever help the case it was written for: a stale secret for this
// installation cannot be recovered by matching a different installation's.
func (c *Controller) verifyDelivery(ctx context.Context, body []byte, signature, repo string) (*store.Installation, string, error) {
	var firstErr error

	var owner *store.Installation
	if repo != "" {
		if inst, err := c.st.FindInstallationByTarget(ctx, repo); err == nil {
			owner = inst
			secret, serr := c.unsealString(inst.WebhookSecretEnc, "webhook secret for installation "+inst.ID)
			if serr != nil {
				firstErr = serr
			} else if err := github.ValidateSignature(body, signature, secret); err != nil {
				firstErr = err
			} else {
				return inst, "", nil
			}
		}
	}

	// An owner that did not verify is the end of it. Anything further would be
	// checking this delivery against a secret its own repository's installation
	// does not use.
	if owner != nil {
		if firstErr != nil {
			return nil, "", firstErr
		}
		return nil, "", fmt.Errorf("installation %s covers %q, and this delivery was not signed with its webhook secret",
			owner.ID, repo)
	}

	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("could not read the configured installations to verify this delivery: %w", err)
	}
	tried := 0
	for _, inst := range insts {
		secret, serr := c.unsealString(inst.WebhookSecretEnc, "webhook secret for installation "+inst.ID)
		if serr != nil || secret == "" {
			continue
		}
		tried++
		if err := github.ValidateSignature(body, signature, secret); err == nil {
			return inst, fmt.Sprintf("no installation covers %q, so this delivery was verified with the webhook secret of installation %s (%s)",
				repo, inst.ID, inst.Target), nil
		}
	}

	if firstErr != nil {
		return nil, "", firstErr
	}
	if len(insts) == 0 {
		return nil, "", errors.New("this controller has no GitHub App installation configured, so it holds no webhook secret to verify deliveries with; add one on the Installations page")
	}
	return nil, "", fmt.Errorf("no installation covers %q and none of the %d configured webhook secrets verified this delivery; "+
		"check that the App's webhook secret matches the one Zoomies holds", repo, tried)
}

// handleWorkflowJob is the path that actually scales the fleet.
// errMalformedDelivery marks a verified delivery whose body is not a
// workflow_job event, which is answered with a 400 rather than a 500 so that
// GitHub does not redeliver it for ever.
var errMalformedDelivery = errors.New("malformed workflow_job delivery")

func (c *Controller) handleWorkflowJob(ctx context.Context, body []byte) error {
	e, err := github.ParseWorkflowJob(body)
	if err != nil {
		return fmt.Errorf("%w: %v", errMalformedDelivery, err)
	}
	return c.applyWorkflowJob(ctx, e, sourceWebhook)
}

// applyWorkflowJob gives polling and webhooks the same monotonic state updates.
func (c *Controller) applyWorkflowJob(ctx context.Context, e *github.WorkflowJobEvent, source string) error {
	job := e.ToJob()

	// Which installation owns this work is the job's repository's question,
	// not the delivery's. Verification deliberately falls back to any secret
	// that answers, so the installation that signed a delivery may be one that
	// has nothing to do with the repository named in it; scoping the job by
	// that would mint runners in the wrong GitHub target. A repository no
	// installation covers is recorded with none, and the eligibility rule says
	// so rather than the delivery being rejected.
	if owner, err := c.st.FindInstallationByTarget(ctx, job.Repo); err == nil {
		job.InstallationID = owner.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("resolving the installation covering %s: %w", job.Repo, err)
	}

	// Which pool claims this job is decided here rather than at reconcile time
	// so that "no pool wants this job" is visible on the Jobs page the moment
	// it arrives, instead of only in a scheduler decision nobody is watching.
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools to match job %d: %w", e.JobID, err)
	}
	// The class comes first, because the claim below is made for it: a job sent
	// to the large pool has to be claimed by the large pool at the door, or its
	// row names one pool and the scheduler feeds another.
	classing := c.sizeOnArrival(ctx, job, pools)
	if p := c.bestPool(pools, job); p != nil {
		job.PoolID = p.ID
		job.Matched = true
	}

	var runner *store.Runner
	if e.RunnerName != "" {
		if r, err := c.st.GetRunnerByName(ctx, e.RunnerName); err == nil {
			job.RunnerID = r.ID
			runner = r
		}
	}

	saved, change, err := c.st.ApplyJob(ctx, job)
	if err != nil {
		return fmt.Errorf("recording job %d: %w", e.JobID, err)
	}
	saved = c.stampSize(ctx, saved, classing)
	if saved.RunNumber == 0 && saved.GitHubRunID > 0 {
		if n, err := c.st.RunNumberForRun(ctx, saved.GitHubRunID); err == nil {
			c.recordRunNumber(ctx, saved, n)
		}
	}
	// A recovery response may omit runner_name even though the earlier
	// in-progress event linked this job. Use the durable link as the fallback,
	// or a completed job can still miss its runner cleanup.
	if runner == nil && saved.RunnerID != "" {
		if r, err := c.st.GetRunner(ctx, saved.RunnerID); err == nil {
			runner = r
		}
	}
	if change.Created || change.StateChanged {
		c.placementVersion.Add(1)
		c.placement.Store(nil)
	}
	c.recordJobChange(ctx, saved, change, source, runner)
	if saved.StartedAt != nil && (change.Created || change.StateChanged) && change.PreviousState != store.JobInProgress {
		poolName, backendName := UnmatchedPool, "unknown"
		if p, e := c.st.GetPool(ctx, saved.PoolID); e == nil {
			poolName, backendName = p.Name, string(p.Backend)
		}
		observeDuration(c.metrics.queuedToStarted, poolName, backendName, saved.QueuedAt, *saved.StartedAt)
	}

	if runner != nil {
		switch saved.State {
		case store.JobInProgress:
			// GitHub's start is authoritative, like its completion below: it
			// means the runner registered and took the job, whatever the row
			// still says. Linked and taken busy in one statement, because the
			// runner read at the top of this handler is already several writes
			// old by the time it gets here -- long enough for the agent's
			// create result to land in between and for a check made against
			// that copy to refuse provisioning -> busy on a row that had since
			// reached registering.
			started, changed, err := c.st.StartRunnerJob(ctx, runner.ID, saved.ID,
				fmt.Sprintf("running %s / %s", saved.Workflow, saved.JobName))
			if err != nil {
				c.log.Warn("could not start a runner on the job GitHub says is running on it",
					"runner", runner.ID, "job", saved.ID, "error", err)
			} else if changed {
				c.observeRunnerReady(ctx, started)
				c.publishRunner(ctx, events.KindRunnerUpdated, started)
			}
		case store.JobCompleted:
			// GitHub's completion is authoritative. An ephemeral runner normally
			// exits and is reported gone by its agent, but relying on that second
			// observation alone leaves the row busy forever when one report is
			// missed. Release it atomically and explicitly remove its workload.
			finished, changed, err := c.st.CompleteRunnerJob(ctx, runner.ID, saved.ID, "finished "+saved.JobName)
			if err != nil {
				c.log.Warn("could not release a runner from its completed job",
					"runner", runner.ID, "job", saved.ID, "error", err)
			} else if changed {
				c.publishRunner(ctx, events.KindRunnerUpdated, finished)
				if finished.Ephemeral {
					c.enqueueLifecycle(ctx, finished.HostID, agent.Task{
						Kind: agent.TaskRemoveRunner, RunnerID: finished.ID,
						Backend: c.backendKind(ctx, finished, nil),
					})
					c.Nudge()
				}
			}
			if change.Created || change.StateChanged {
				c.observeJobCompletion(saved)
			}
		}
	} else if saved.State == store.JobCompleted && (change.Created || change.StateChanged) {
		c.observeJobCompletion(saved)
	}

	// Run metadata and cancellation verification can wait; the job event cannot.
	if saved.GitHubRunID > 0 && saved.InstallationID != "" && (saved.RunNumber == 0 || saved.State == store.JobCompleted && saved.Conclusion == "cancelled") {
		if err := c.st.QueueEnrichment(ctx, "job", saved.ID); err != nil {
			return err
		}
	}

	if !saved.Matched && saved.State == store.JobQueued && !hostedJob(saved.Labels) {
		// Not a warning: next to another runner provider this is every one of
		// its jobs. The problems drawer says so once the job has waited long
		// enough to mean something.
		c.log.Info("no enabled pool here claims a queued job; if it is meant for this fleet, its labels match no pool",
			"job", saved.ID, "repo", saved.Repo, "labels", strings.Join(saved.Labels, ","))
	}

	c.publishJob(ctx, saved)
	// Wake the reconcile loop rather than scheduling inline: GitHub is holding
	// this connection open, and a reconcile can take as long as GitHub's API
	// does to answer.
	c.Nudge()
	return nil
}

// recordRunNumber writes a run's number to every job of the run that lacks
// one, the job in hand included.
func (c *Controller) recordRunNumber(ctx context.Context, saved *store.Job, n int64) {
	saved.RunNumber = n
	if err := c.st.SetRunNumberForRun(ctx, saved.Repo, saved.GitHubRunID, n); err != nil {
		c.log.Warn("could not record a workflow run's number on its jobs",
			"job", saved.ID, "run", saved.GitHubRunID, "error", err)
	}
}

// observeJobCompletion feeds the histograms the Overview's percentiles and the
// Prometheus endpoint are built from.
func (c *Controller) observeJobCompletion(j *store.Job) {
	pool := c.poolLabel(j.PoolID)
	conclusion := j.Conclusion
	if conclusion == "" {
		conclusion = "unknown"
	}
	c.metrics.jobsTotal.WithLabelValues(pool, conclusion).Inc()
	// The fleet's own half is counted when the fault is recorded, which
	// happens before GitHub closes the job and sometimes long before. Counting
	// it again here would double every runner that died mid-job.
	if j.WorkflowFailed() {
		c.metrics.jobFailures.WithLabelValues(pool, store.FaultDomainWorkflow, "").Inc()
	}
	if w := j.QueueWait(); w > 0 {
		c.metrics.queueWait.Observe(w.Seconds())
	}
	if d := j.Duration(); d > 0 {
		c.metrics.jobDuration.Observe(d.Seconds())
	}
}

// recordDelivery writes one delivery down and tells the UI about it. Every
// delivery is recorded, accepted or not: a run of rejections is how an
// operator sees that something is probing the endpoint or that a secret has
// drifted.
//
// errMsg doubles as a note on an accepted delivery -- the store has one free
// text column and "this verified against a different installation's secret" is
// exactly the sort of thing the delivery log exists to say.
func (c *Controller) recordDelivery(ctx context.Context, d *store.WebhookDelivery, status, errMsg string) {
	d.Status = status
	d.Error = errMsg
	if d.ReceivedAt.IsZero() {
		d.ReceivedAt = c.Now()
	}
	// The request's context is cancelled the moment the response is written,
	// which must not take the row with it.
	if err := c.st.RecordDelivery(context.WithoutCancel(ctx), d); err != nil {
		c.log.Error("could not record a webhook delivery", "delivery", d.DeliveryID, "error", err)
	}
	c.metrics.webhookDeliveries.WithLabelValues(status).Inc()
	c.publish(events.KindWebhook, "webhook:"+d.ID, d)
}

func installationID(inst *store.Installation) string {
	if inst == nil {
		return ""
	}
	return inst.ID
}

// writeJSON is the one response helper this package needs; the API package has
// its own for everything else.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
