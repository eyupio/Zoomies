package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

const testSecret = "s3cr3t-webhook"

func sign(t *testing.T, payload []byte, secret string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature(t *testing.T) {
	payload := []byte(`{"action":"queued"}`)
	good := sign(t, payload, testSecret)

	t.Run("valid", func(t *testing.T) {
		if err := ValidateSignature(payload, good, testSecret); err != nil {
			t.Fatalf("valid signature rejected: %v", err)
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		err := ValidateSignature(payload, sign(t, payload, "other"), testSecret)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("got %v, want ErrInvalidSignature", err)
		}
	})

	t.Run("missing header", func(t *testing.T) {
		err := ValidateSignature(payload, "  ", testSecret)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("got %v, want ErrInvalidSignature", err)
		}
		// The operator needs to be sent to the GitHub side, not ours.
		if !strings.Contains(err.Error(), "GitHub side") || !strings.Contains(err.Error(), SignatureHeader) {
			t.Fatalf("unhelpful message for a missing signature: %v", err)
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		err := ValidateSignature([]byte(`{"action":"completed"}`), good, testSecret)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("got %v, want ErrInvalidSignature", err)
		}
	})

	t.Run("no secret configured", func(t *testing.T) {
		err := ValidateSignature(payload, good, "")
		if !errors.Is(err, ErrNoSecret) {
			t.Fatalf("got %v, want ErrNoSecret", err)
		}
	})
}

func TestParseEventType(t *testing.T) {
	if got := ParseEventType("  Workflow_Job "); got != "workflow_job" {
		t.Fatalf("ParseEventType = %q", got)
	}
	if !IsPing("Ping") {
		t.Fatal("IsPing did not recognise a ping")
	}
	if IsPing("workflow_job") {
		t.Fatal("IsPing matched a workflow_job")
	}
}

// jobPayload renders a workflow_job delivery for one action.
func jobPayload(action, status, conclusion string, started, completed bool) []byte {
	body := `{
	  "action": "` + action + `",
	  "installation": {"id": 42},
	  "repository": {"full_name": "acme/widgets"},
	  "workflow_job": {
	    "id": 998877,
	    "run_id": 5544,
	    "workflow_name": "CI",
	    "name": "build (linux)",
	    "labels": ["self-hosted", "Linux", "gpu"],
	    "runner_id": 7,
	    "runner_name": "zoomies-linux-abcd",
	    "status": "` + status + `",
	    "conclusion": ` + jsonOrNull(conclusion) + `,
	    "created_at": "2024-05-01T10:00:00Z",
	    "started_at": ` + timeOrNull("2024-05-01T10:00:30Z", started) + `,
	    "completed_at": ` + timeOrNull("2024-05-01T10:05:00Z", completed) + `,
	    "html_url": "https://github.com/acme/widgets/actions/runs/5544/job/998877",
	    "head_branch": "main",
	    "head_sha": "0123456789abcdef0123456789abcdef01234567",
	    "run_attempt": 2,
	    "steps": [
	      {"number": 1, "name": "Set up job", "status": "completed", "conclusion": "success",
	       "started_at": "2024-05-01T10:00:30Z", "completed_at": "2024-05-01T10:00:35Z"},
	      {"number": 2, "name": "Run tests", "status": "` + stepStatus(completed) + `", "conclusion": ` + jsonOrNull(stepConclusion(conclusion, completed)) + `,
	       "started_at": "2024-05-01T10:00:35Z", "completed_at": ` + timeOrNull("2024-05-01T10:04:50Z", completed) + `},
	      {"number": 3, "name": "Upload", "status": "` + stepStatus(completed) + `", "conclusion": ` + jsonOrNull(skippedIf(completed)) + `}
	    ]
	  }
	}`
	return []byte(body)
}

// stepStatus, stepConclusion and skippedIf render the steps the way GitHub
// does: all of them "completed" with a conclusion once the job is over, and
// mid-flight before that.
func stepStatus(completed bool) string {
	if completed {
		return "completed"
	}
	return "in_progress"
}

func stepConclusion(conclusion string, completed bool) string {
	if !completed {
		return ""
	}
	return conclusion
}

func skippedIf(completed bool) string {
	if completed {
		return "skipped"
	}
	return ""
}

func jsonOrNull(s string) string {
	if s == "" {
		return "null"
	}
	return `"` + s + `"`
}

func timeOrNull(s string, present bool) string {
	if !present {
		return "null"
	}
	return `"` + s + `"`
}

func TestParseWorkflowJobQueued(t *testing.T) {
	e, err := ParseWorkflowJob(jobPayload("queued", "queued", "", true, false))
	if err != nil {
		t.Fatalf("ParseWorkflowJob: %v", err)
	}
	if e.Action != "queued" || e.JobID != 998877 || e.RunID != 5544 {
		t.Fatalf("unexpected event: %+v", e)
	}
	if e.Repo != "acme/widgets" || e.WorkflowName != "CI" || e.JobName != "build (linux)" {
		t.Fatalf("unexpected identity: %+v", e)
	}
	if e.InstallationID != 42 || e.RunnerID != 7 || e.RunnerName != "zoomies-linux-abcd" {
		t.Fatalf("unexpected routing fields: %+v", e)
	}
	if !slices.Equal(e.Labels, []string{"self-hosted", "Linux", "gpu"}) {
		t.Fatalf("labels = %v", e.Labels)
	}
	if e.HTMLURL == "" || !e.CompletedAt.IsZero() {
		t.Fatalf("unexpected timestamps: %+v", e)
	}

	j := e.ToJob()
	if j.State != store.JobQueued {
		t.Fatalf("state = %q", j.State)
	}
	if j.ID != "" {
		t.Fatalf("ToJob minted an ID (%q); the store owns that", j.ID)
	}
	// GitHub stamps started_at on queued jobs too; trusting it would report a
	// zero queue wait for every job.
	if j.StartedAt != nil {
		t.Fatalf("queued job has StartedAt = %v", *j.StartedAt)
	}
	if !j.QueuedAt.Equal(time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("queued_at = %v", j.QueuedAt)
	}
	if !slices.Equal(j.Labels, []string{"gpu", "linux", "self-hosted"}) {
		t.Fatalf("labels not normalised: %v", j.Labels)
	}
	if j.Conclusion != "" {
		t.Fatalf("queued job has a conclusion: %q", j.Conclusion)
	}
}

func TestParseWorkflowJobInProgress(t *testing.T) {
	e, err := ParseWorkflowJob(jobPayload("in_progress", "in_progress", "", true, false))
	if err != nil {
		t.Fatalf("ParseWorkflowJob: %v", err)
	}
	j := e.ToJob()
	if j.State != store.JobInProgress {
		t.Fatalf("state = %q", j.State)
	}
	if j.StartedAt == nil || !j.StartedAt.Equal(time.Date(2024, 5, 1, 10, 0, 30, 0, time.UTC)) {
		t.Fatalf("started_at = %v", j.StartedAt)
	}
	if j.CompletedAt != nil {
		t.Fatalf("running job has CompletedAt = %v", *j.CompletedAt)
	}
	if j.RunnerName != "zoomies-linux-abcd" {
		t.Fatalf("runner name lost: %q", j.RunnerName)
	}
	if got := j.QueueWait(); got != 30*time.Second {
		t.Fatalf("queue wait = %v, want 30s", got)
	}
}

func TestParseWorkflowJobCompleted(t *testing.T) {
	e, err := ParseWorkflowJob(jobPayload("completed", "completed", "success", true, true))
	if err != nil {
		t.Fatalf("ParseWorkflowJob: %v", err)
	}
	j := e.ToJob()
	if j.State != store.JobCompleted {
		t.Fatalf("state = %q", j.State)
	}
	if j.Conclusion != "success" {
		t.Fatalf("conclusion = %q", j.Conclusion)
	}
	if j.CompletedAt == nil || !j.CompletedAt.Equal(time.Date(2024, 5, 1, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("completed_at = %v", j.CompletedAt)
	}
	if got := j.Duration(); got != 4*time.Minute+30*time.Second {
		t.Fatalf("duration = %v", got)
	}
}

// The steps are what let a failed job say where it failed without the operator
// leaving for GitHub, and the branch and attempt are what make "CI failed" mean
// something.
func TestParseWorkflowJobCarriesTheStepsAndTheRunContext(t *testing.T) {
	e, err := ParseWorkflowJob(jobPayload("completed", "completed", "failure", true, true))
	if err != nil {
		t.Fatalf("ParseWorkflowJob: %v", err)
	}
	if e.HeadBranch != "main" || e.HeadSHA != "0123456789abcdef0123456789abcdef01234567" || e.RunAttempt != 2 {
		t.Fatalf("run context = %q %q %d", e.HeadBranch, e.HeadSHA, e.RunAttempt)
	}
	if len(e.Steps) != 3 {
		t.Fatalf("steps = %+v, want 3", e.Steps)
	}
	j := e.ToJob()
	if j.HeadBranch != "main" || j.RunAttempt != 2 || len(j.Steps) != 3 {
		t.Fatalf("ToJob dropped the run context or the steps: %+v", j)
	}
	step := j.FailedStep()
	if step == nil || step.Number != 2 || step.Name != "Run tests" || step.Conclusion != "failure" {
		t.Fatalf("FailedStep = %+v, want step 2 'Run tests' failed", step)
	}
	if step.StartedAt == nil || step.CompletedAt == nil || step.CompletedAt.Sub(*step.StartedAt) != 4*time.Minute+15*time.Second {
		t.Fatalf("failed step timestamps = %v .. %v", step.StartedAt, step.CompletedAt)
	}
	// The step after the failure was skipped as a consequence, and must not be
	// the one reported.
	if j.Steps[2].Conclusion != "skipped" {
		t.Fatalf("step 3 conclusion = %q, want skipped", j.Steps[2].Conclusion)
	}

	// A job still running has steps too, but none of them has failed yet.
	running, err := ParseWorkflowJob(jobPayload("in_progress", "in_progress", "", true, false))
	if err != nil {
		t.Fatalf("ParseWorkflowJob in_progress: %v", err)
	}
	if rj := running.ToJob(); rj.FailedStep() != nil || len(rj.Steps) != 3 || rj.Steps[1].Status != "in_progress" {
		t.Fatalf("running job steps = %+v", rj.Steps)
	}
	// And a success has no failed step to name.
	ok, _ := ParseWorkflowJob(jobPayload("completed", "completed", "success", true, true))
	if ok.ToJob().FailedStep() != nil {
		t.Fatal("a successful job named a failed step")
	}
}

func TestParseWorkflowJobWaitingIsItsOwnState(t *testing.T) {
	// A job held for a deployment review is not demand: recorded as queued it
	// had the scheduler starting a runner nobody could use for as long as the
	// review took. It is "waiting", which sits before queued in the lifecycle
	// so that the approval's queued delivery still moves it forward.
	e, err := ParseWorkflowJob(jobPayload("waiting", "waiting", "", false, false))
	if err != nil {
		t.Fatalf("ParseWorkflowJob: %v", err)
	}
	if got := e.ToJob().State; got != store.JobWaiting {
		t.Fatalf("state = %q, want %q", got, store.JobWaiting)
	}
}

func TestParseWorkflowJobCompletedWithoutTimestamp(t *testing.T) {
	before := time.Now().UTC()
	e, err := ParseWorkflowJob(jobPayload("completed", "completed", "cancelled", false, false))
	if err != nil {
		t.Fatalf("ParseWorkflowJob: %v", err)
	}
	j := e.ToJob()
	if j.CompletedAt == nil || j.CompletedAt.Before(before) {
		t.Fatalf("completed_at should be stamped now, got %v", j.CompletedAt)
	}
}

func TestParseWorkflowJobRejectsOtherEvents(t *testing.T) {
	_, err := ParseWorkflowJob([]byte(`{"action":"completed","workflow_run":{"id":1}}`))
	if !errors.Is(err, ErrNotWorkflowJob) {
		t.Fatalf("got %v, want ErrNotWorkflowJob", err)
	}
	if _, err := ParseWorkflowJob([]byte(`not json`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestToJobStampsQueuedAtWhenMissing(t *testing.T) {
	e := &WorkflowJobEvent{Action: "queued", JobID: 1}
	j := e.ToJob()
	if j.QueuedAt.IsZero() {
		t.Fatal("QueuedAt left zero; the queue-wait histogram cannot sort it")
	}
}
