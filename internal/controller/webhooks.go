package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// maxWebhookBody caps an inbound delivery. GitHub's own limit is 25 MB, but a
// workflow_job payload is a few kilobytes; anything approaching this is either
// not from GitHub or not something Zoomies should be parsing.
const maxWebhookBody = 5 << 20

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

	event := github.ParseEventType(r.Header.Get(github.EventTypeHeader))
	d := &store.WebhookDelivery{
		DeliveryID: r.Header.Get(github.DeliveryIDHeader),
		Event:      event,
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		c.recordDelivery(ctx, d, "error", fmt.Sprintf("could not read the delivery body (limit %d bytes): %v", maxWebhookBody, err))
		http.Error(w, "the delivery body could not be read, or was larger than 5 MiB", http.StatusRequestEntityTooLarge)
		return
	}

	// The envelope is read before the signature is checked, because the
	// repository is what selects the secret to check the signature with.
	// Nothing from it is acted on until the signature verifies.
	env := parseEnvelope(body)
	d.Repo, d.Action = env.Repo, env.Action

	inst, note, err := c.verifyDelivery(ctx, body, r.Header.Get(github.SignatureHeader), env.Repo)
	if err != nil {
		c.recordDelivery(ctx, d, "rejected", err.Error())
		// A burst of these means somebody is probing the endpoint, which is
		// why every one of them is written down rather than only counted.
		c.log.Warn("rejected a webhook delivery",
			"delivery", d.DeliveryID, "event", event, "repo", env.Repo, "reason", err)
		http.Error(w, "the delivery signature could not be verified", http.StatusUnauthorized)
		return
	}

	// A delivery that verified proves webhooks reach this controller.
	c.pollingOnly.Store(false)

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
	return envelope{Repo: repo, Action: strings.ToLower(strings.TrimSpace(p.Action)), InstallationID: p.Installation.ID}
}

// verifyDelivery finds the secret this delivery should have been signed with
// and checks the signature in constant time (github.ValidateSignature uses
// hmac.Equal).
//
// The installation that owns the repository is tried first. When that does not
// verify -- which happens when an App was re-installed, or a repository moved
// between organisations -- every configured secret is tried before rejecting,
// and the note that comes back says which one worked, because "your secrets
// are out of step with your installations" is a different problem from "this
// delivery was forged".
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

	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("could not read the configured installations to verify this delivery: %w", err)
	}
	tried := 0
	for _, inst := range insts {
		if owner != nil && inst.ID == owner.ID {
			continue
		}
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
func (c *Controller) handleWorkflowJob(ctx context.Context, body []byte) error {
	e, err := github.ParseWorkflowJob(body)
	if err != nil {
		return err
	}
	job := e.ToJob()

	// Which pool claims this job is decided here rather than at reconcile time
	// so that "no pool wants this job" is visible on the Jobs page the moment
	// it arrives, instead of only in a scheduler decision nobody is watching.
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools to match job %d: %w", e.JobID, err)
	}
	if p := scheduler.BestPool(pools, job.Labels); p != nil {
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

	saved, err := c.st.UpsertJob(ctx, job)
	if err != nil {
		return fmt.Errorf("recording job %d: %w", e.JobID, err)
	}

	if runner != nil {
		switch saved.State {
		case store.JobInProgress:
			if err := c.st.AssignRunnerJob(ctx, runner.ID, saved.ID); err != nil {
				c.log.Warn("could not link a job to its runner", "runner", runner.ID, "job", saved.ID, "error", err)
			}
			c.applyRunnerState(ctx, runner, store.RunnerBusy,
				fmt.Sprintf("running %s / %s", saved.Workflow, saved.JobName))
		case store.JobCompleted:
			// An ephemeral runner exits by itself and its agent reports it
			// gone; a persistent one goes back to waiting for work.
			if !runner.Ephemeral && runner.State == store.RunnerBusy {
				c.applyRunnerState(ctx, runner, store.RunnerIdle, "finished "+saved.JobName)
			}
			c.observeJobCompletion(saved)
		}
	} else if saved.State == store.JobCompleted {
		c.observeJobCompletion(saved)
	}

	if !saved.Matched && saved.State == store.JobQueued {
		c.log.Warn("a queued job matches no enabled pool, so nothing will run it",
			"job", saved.ID, "repo", saved.Repo, "labels", strings.Join(saved.Labels, ","))
	}

	c.publishJob(ctx, saved)
	// Wake the reconcile loop rather than scheduling inline: GitHub is holding
	// this connection open, and a reconcile can take as long as GitHub's API
	// does to answer.
	c.Nudge()
	return nil
}

// observeJobCompletion feeds the histograms the Overview's percentiles and the
// Prometheus endpoint are built from.
func (c *Controller) observeJobCompletion(j *store.Job) {
	pool := j.PoolID
	if pool == "" {
		pool = "unmatched"
	}
	conclusion := j.Conclusion
	if conclusion == "" {
		conclusion = "unknown"
	}
	c.metrics.jobsTotal.WithLabelValues(pool, conclusion).Inc()
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
