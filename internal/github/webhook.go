package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	gh "github.com/google/go-github/v88/github"

	"github.com/eyupio/zoomies/internal/store"
)

// Headers GitHub sets on every delivery. They are re-exported here so the API
// package does not need its own opinion about their spelling.
const (
	// SignatureHeader carries the HMAC-SHA256 of the body.
	SignatureHeader = gh.SHA256SignatureHeader
	// EventTypeHeader carries the event name, e.g. "workflow_job".
	EventTypeHeader = gh.EventTypeHeader
	// DeliveryIDHeader carries the delivery UUID, which is what an operator
	// quotes when comparing Zoomies' log with GitHub's redelivery page.
	DeliveryIDHeader = gh.DeliveryIDHeader
)

// ErrNoSecret is returned when Zoomies holds no webhook secret to verify a
// delivery against. It is a configuration fault, not an attack, and the API
// answers it differently from a bad signature.
var ErrNoSecret = errors.New("github: no webhook secret configured")

// ErrInvalidSignature is returned when a delivery is unsigned or its signature
// does not match. The API answers these with 401 and does not process the body.
var ErrInvalidSignature = errors.New("github: webhook signature invalid")

// ErrNotWorkflowJob is returned when a payload is not a workflow_job event.
var ErrNotWorkflowJob = errors.New("github: not a workflow_job payload")

// ValidateSignature checks the HMAC GitHub attached to a delivery.
//
// It refuses to pass an unsigned delivery under any circumstances: an endpoint
// that accepts unsigned webhooks lets anyone on the internet make Zoomies
// start runners.
func ValidateSignature(payload []byte, header, secret string) error {
	if secret == "" {
		return fmt.Errorf("%w: Zoomies has no webhook secret for this installation, so it cannot "+
			"tell a real delivery from a forged one: set one on the Installations page and copy the "+
			"same value into the App's webhook settings", ErrNoSecret)
	}
	if strings.TrimSpace(header) == "" {
		return fmt.Errorf("%w: the delivery carried no %s header, which means no webhook secret is "+
			"configured on the GitHub side: open the App's settings, set the webhook secret to the "+
			"value Zoomies holds, and redeliver", ErrInvalidSignature, SignatureHeader)
	}
	if err := gh.ValidateSignature(header, payload, []byte(secret)); err != nil {
		return fmt.Errorf("%w: the webhook secret on the GitHub App does not match the one Zoomies "+
			"holds for this installation, or the body was modified in transit", ErrInvalidSignature)
	}
	return nil
}

// ParseEventType normalises the X-GitHub-Event header. GitHub sends it
// lowercase, but proxies have been known to rewrite header values.
func ParseEventType(header string) string {
	return strings.ToLower(strings.TrimSpace(header))
}

// IsPing reports whether an event is GitHub's "does this endpoint exist"
// probe, which is sent once when a webhook is configured and carries no job.
func IsPing(event string) bool { return ParseEventType(event) == "ping" }

// WorkflowJobEvent is the part of a workflow_job delivery Zoomies acts on.
//
// It is a hand-written struct rather than go-github's event type because this
// payload is the load-bearing input of the whole scheduler: naming exactly the
// fields that matter keeps it obvious what a change to GitHub's payload would
// break.
type WorkflowJobEvent struct {
	// Action is "queued", "in_progress", "completed" or "waiting".
	Action string
	// InstallationID routes the delivery to one of Zoomies' installations.
	InstallationID int64

	JobID        int64
	RunID        int64
	Repo         string // "acme/widgets"
	WorkflowName string
	JobName      string
	// Labels are the workflow's runs-on values, including implicit ones.
	Labels     []string
	RunnerID   int64
	RunnerName string
	Status     string
	Conclusion string

	// QueuedAt is the job's created_at, i.e. when GitHub started looking for a
	// runner.
	QueuedAt    time.Time
	StartedAt   time.Time
	CompletedAt time.Time
	HTMLURL     string

	// HeadBranch, HeadSHA and RunAttempt say what the job ran for.
	HeadBranch string
	HeadSHA    string
	RunAttempt int
	// Steps are the job's steps as this delivery reports them. On a completed
	// delivery every step carries its conclusion, which is what lets a failed
	// job say where it failed.
	Steps []store.JobStep
}

// workflowJobPayload mirrors the wire format. Timestamps are pointers because
// GitHub sends null for the ones that have not happened yet.
type workflowJobPayload struct {
	Action      string `json:"action"`
	WorkflowJob struct {
		ID           int64      `json:"id"`
		RunID        int64      `json:"run_id"`
		WorkflowName string     `json:"workflow_name"`
		Name         string     `json:"name"`
		Labels       []string   `json:"labels"`
		RunnerID     int64      `json:"runner_id"`
		RunnerName   string     `json:"runner_name"`
		Status       string     `json:"status"`
		Conclusion   string     `json:"conclusion"`
		CreatedAt    *time.Time `json:"created_at"`
		StartedAt    *time.Time `json:"started_at"`
		CompletedAt  *time.Time `json:"completed_at"`
		HTMLURL      string     `json:"html_url"`
		HeadBranch   string     `json:"head_branch"`
		HeadSHA      string     `json:"head_sha"`
		RunAttempt   int        `json:"run_attempt"`
		Steps        []struct {
			Number      int        `json:"number"`
			Name        string     `json:"name"`
			Status      string     `json:"status"`
			Conclusion  string     `json:"conclusion"`
			StartedAt   *time.Time `json:"started_at"`
			CompletedAt *time.Time `json:"completed_at"`
		} `json:"steps"`
	} `json:"workflow_job"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ParseWorkflowJob decodes a workflow_job delivery.
func ParseWorkflowJob(payload []byte) (*WorkflowJobEvent, error) {
	var p workflowJobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("github: parse workflow_job: %w", err)
	}
	if p.WorkflowJob.ID == 0 {
		return nil, fmt.Errorf("%w: the body has no workflow_job.id; check that the App is "+
			"subscribed to the \"workflow_job\" event and not to \"workflow_run\"", ErrNotWorkflowJob)
	}
	e := &WorkflowJobEvent{
		Action:         strings.ToLower(strings.TrimSpace(p.Action)),
		InstallationID: p.Installation.ID,
		JobID:          p.WorkflowJob.ID,
		RunID:          p.WorkflowJob.RunID,
		Repo:           p.Repository.FullName,
		WorkflowName:   p.WorkflowJob.WorkflowName,
		JobName:        p.WorkflowJob.Name,
		Labels:         p.WorkflowJob.Labels,
		RunnerID:       p.WorkflowJob.RunnerID,
		RunnerName:     p.WorkflowJob.RunnerName,
		Status:         strings.ToLower(strings.TrimSpace(p.WorkflowJob.Status)),
		Conclusion:     strings.ToLower(strings.TrimSpace(p.WorkflowJob.Conclusion)),
		HTMLURL:        p.WorkflowJob.HTMLURL,
		HeadBranch:     p.WorkflowJob.HeadBranch,
		HeadSHA:        p.WorkflowJob.HeadSHA,
		RunAttempt:     p.WorkflowJob.RunAttempt,
	}
	for _, st := range p.WorkflowJob.Steps {
		step := store.JobStep{
			Number:     st.Number,
			Name:       st.Name,
			Status:     strings.ToLower(strings.TrimSpace(st.Status)),
			Conclusion: strings.ToLower(strings.TrimSpace(st.Conclusion)),
		}
		if st.StartedAt != nil {
			t := st.StartedAt.UTC()
			step.StartedAt = &t
		}
		if st.CompletedAt != nil {
			t := st.CompletedAt.UTC()
			step.CompletedAt = &t
		}
		e.Steps = append(e.Steps, step)
	}
	if t := p.WorkflowJob.CreatedAt; t != nil {
		e.QueuedAt = t.UTC()
	}
	if t := p.WorkflowJob.StartedAt; t != nil {
		e.StartedAt = t.UTC()
	}
	if t := p.WorkflowJob.CompletedAt; t != nil {
		e.CompletedAt = t.UTC()
	}
	return e, nil
}

// State maps the delivery's action onto the job state machine.
//
// "waiting" means the job is held for a deployment review, which can take
// hours or days, and GitHub sends a real "queued" delivery once it is approved.
// It is its own state, ahead of queued in the lifecycle so the approval moves
// the job forward, and one the scheduler does not count: recording it as
// queued started a runner that idled out and was started again on the next
// pass, for as long as the review took.
func (e *WorkflowJobEvent) State() store.JobState {
	switch e.Action {
	case "waiting":
		return store.JobWaiting
	case "queued":
		return store.JobQueued
	case "in_progress":
		return store.JobInProgress
	case "completed":
		return store.JobCompleted
	}
	// Some GHES versions send actions Zoomies has not seen; the job's own
	// status field is then the better source.
	if s := store.JobState(e.Status); s.Valid() {
		return s
	}
	return store.JobQueued
}

// ToJob maps the event onto the row the store keeps, stamping the timestamps
// that this transition is the first evidence of.
//
// The returned Job has no ID: Store.UpsertJob keys on GitHubJobID and mints one
// for a job it has not seen before.
func (e *WorkflowJobEvent) ToJob() *store.Job {
	state := e.State()
	j := &store.Job{
		GitHubJobID: e.JobID,
		GitHubRunID: e.RunID,
		Repo:        e.Repo,
		Workflow:    e.WorkflowName,
		JobName:     e.JobName,
		Labels:      store.NormalizeLabels(e.Labels),
		State:       state,
		RunnerName:  e.RunnerName,
		HTMLURL:     e.HTMLURL,
		QueuedAt:    e.QueuedAt,
		HeadBranch:  e.HeadBranch,
		HeadSHA:     e.HeadSHA,
		RunAttempt:  e.RunAttempt,
		Steps:       e.Steps,
	}
	if j.QueuedAt.IsZero() {
		// A delivery without created_at still has to sort somewhere in the
		// queue-wait histogram; now is the closest honest answer.
		j.QueuedAt = time.Now().UTC()
	}
	// GitHub stamps started_at on a queued job too (equal to created_at), so
	// trusting it while queued would report a zero queue wait for every job.
	if state != store.JobQueued && !e.StartedAt.IsZero() {
		t := e.StartedAt
		j.StartedAt = &t
	}
	if state == store.JobCompleted {
		t := e.CompletedAt
		if t.IsZero() {
			t = time.Now().UTC()
		}
		j.CompletedAt = &t
		j.Conclusion = e.Conclusion
	}
	return j
}
