package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// aiContextDiagnosis is what the controller last read about the newest run of a
// repository's managed workflow for one commit. It is kept so that the five
// minute loop does not download the same log again and again: a run that has
// finished does not change, so it is read once and looked up after that.
type aiContextDiagnosis struct {
	commit string
	// runKey changes whenever the run does -- a new run, or the same one moving
	// from queued to finished -- and is what makes a cached reading still true.
	runKey    string
	outcome   aicontext.Outcome
	diagnosis *aicontext.Diagnosis
	// finished is when the run ended, which is what waiting before the next
	// attempt is counted from.
	finished time.Time
}

// diagnoseAIContext reads the newest run for commit and says why it failed, if it
// did. It returns nil when nothing is known -- the repository has no run for that
// commit, or GitHub could not be asked -- and the caller then does what it always
// did. A diagnosis only ever adds to what Zoomies knew; it is never required.
func (c *Controller) diagnoseAIContext(ctx context.Context, r *store.AIContextRepository, commit string) *aiContextDiagnosis {
	client, err := c.ClientFor(ctx, r.Key.InstallationID)
	if err != nil {
		return nil
	}
	reader, ok := client.(github.ContextRunReader)
	if !ok {
		return nil
	}
	run, err := reader.LatestContextRun(ctx, r.FullName, commit)
	if err != nil {
		// Reading is a courtesy: the check that found the context stale already
		// reported what it could, and a log line per pass would bury it.
		c.log.Debug("could not read the AI Context workflow's runs", "repository", r.ID, "error", err)
		return nil
	}
	if run == nil {
		c.forgetAIContextDiagnosis(r.ID)
		return nil
	}
	key := strconv.FormatInt(run.ID, 10) + "/" + run.Facts.Status + "/" + run.Facts.Conclusion + "/" + strconv.FormatInt(run.UpdatedAt.Unix(), 10)
	c.aiContextRunsMu.Lock()
	cached := c.aiContextDiagnoses[r.ID]
	c.aiContextRunsMu.Unlock()
	if cached != nil && cached.commit == commit && cached.runKey == key {
		return cached
	}

	facts := run.Facts
	// Only the job Diagnose will look at is worth a download, and only if it ran:
	// a job that never started has no log, and the answer is already in its shape.
	if i := aicontext.FirstFailedJob(facts.Jobs); facts.Status == "completed" && i >= 0 && facts.Jobs[i].Started() {
		if lines, err := reader.ContextJobErrors(ctx, r.FullName, run.JobIDs[i]); err == nil {
			facts.Jobs[i].Errors = lines
		}
	}
	outcome, d := aicontext.Diagnose(facts)
	entry := &aiContextDiagnosis{commit: commit, runKey: key, outcome: outcome, diagnosis: d, finished: run.UpdatedAt}
	if d != nil {
		d.Commit, d.RunURL, d.FailedAt, d.ObservedAt = commit, run.URL, run.UpdatedAt, c.Now()
		if d.Cause == aicontext.CauseArtifactQuota {
			// "Full" is half an answer; what holds it is the other half.
			if usage, err := reader.ContextArtifactUsage(ctx, r.FullName); err == nil {
				*d = d.WithArtifactUsage(*usage)
			}
		}
	}
	c.aiContextRunsMu.Lock()
	c.aiContextDiagnoses[r.ID] = entry
	c.aiContextRunsMu.Unlock()
	return entry
}

// noteAIContextStart records that the workflow was just started, by Zoomies or
// by a person pressing Regenerate.
//
// GitHub can take a few seconds to list a run it has just accepted. In that gap
// the next pass would read the run before it -- still failed, and long enough ago
// -- and start another, which the workflow's concurrency group would answer by
// cancelling the first. Counting the start towards spacing closes the gap, and
// puts the old failure out of the card until the new run can be read. It is not
// counted as an attempt: those are Zoomies' own, and a person asking is not one.
func (c *Controller) noteAIContextStart(ctx context.Context, id string) {
	f, err := c.st.GetAIContextFreshness(ctx, id)
	if err != nil || f.DesiredCommit == "" {
		c.forgetAIContextDiagnosis(id)
		return
	}
	now := c.Now()
	c.aiContextRunsMu.Lock()
	defer c.aiContextRunsMu.Unlock()
	run := c.aiContextRuns[id]
	if run == nil || run.commit != f.DesiredCommit {
		run = &aiContextRun{commit: f.DesiredCommit, since: now}
		c.aiContextRuns[id] = run
	}
	run.lastStart = now
	c.aiContextDiagnoses[id] = &aiContextDiagnosis{commit: f.DesiredCommit, runKey: "started", outcome: aicontext.OutcomeRunning}
}

func (c *Controller) forgetAIContextDiagnosis(id string) {
	c.aiContextRunsMu.Lock()
	delete(c.aiContextDiagnoses, id)
	c.aiContextRunsMu.Unlock()
}

// aiContextRunDue is whether Zoomies should start the workflow now.
//
// What it knows decides how long it waits. A run still working is left alone --
// the workflow's concurrency group would cancel it -- and a failure is judged by
// its cause: one a new run could not change is not retried, one GitHub needs
// time to clear is waited out for as long as GitHub says, and the rest are tried
// at the pace Zoomies has always used. With no reading at all -- no run exists
// for the commit, or the last one succeeded and the context is still behind --
// it is the original rule: a grace for the push-triggered run, then a bounded
// number of attempts.
func aiContextRunDue(now time.Time, grace time.Duration, run *aiContextRun, known *aiContextDiagnosis) bool {
	if known != nil {
		switch {
		case known.outcome == aicontext.OutcomeRunning:
			return false
		case known.diagnosis != nil:
			p := known.diagnosis.Cause.Policy()
			return p.Automatic && run.attempts < p.Attempts &&
				!now.Before(known.finished.Add(p.After)) &&
				(run.lastStart.IsZero() || now.Sub(run.lastStart) >= p.After)
		}
	}
	return now.Sub(run.since) >= grace && run.attempts < aiContextRunAttempts &&
		(run.lastStart.IsZero() || now.Sub(run.lastStart) >= aiContextRunSpacing)
}

// AIContextDiagnosis is what the repository's card and the API show about the
// failed run, with what Zoomies means to do about it. It is nil unless the
// context is behind and a run for the commit it is waiting for failed.
func (c *Controller) AIContextDiagnosis(r *store.AIContextRepository) *aicontext.Diagnosis {
	if r == nil || r.Freshness == nil || r.Freshness.State != "stale" || r.Config.Disabled {
		return nil
	}
	c.aiContextRunsMu.Lock()
	defer c.aiContextRunsMu.Unlock()
	known := c.aiContextDiagnoses[r.ID]
	if known == nil || known.diagnosis == nil || known.commit != r.Freshness.DesiredCommit {
		return nil
	}
	out := *known.diagnosis
	out.Retry = c.aiContextRetryPlanLocked(r.ID, known)
	return &out
}

// aiContextRetryPlanLocked is what Zoomies will do about a failure unprompted.
func (c *Controller) aiContextRetryPlanLocked(id string, known *aiContextDiagnosis) *aicontext.RetryPlan {
	p := known.diagnosis.Cause.Policy()
	plan := &aicontext.RetryPlan{}
	if !p.Automatic {
		return plan
	}
	attempts, last := 0, time.Time{}
	if run := c.aiContextRuns[id]; run != nil && run.commit == known.commit {
		attempts, last = run.attempts, run.lastStart
	}
	plan.AttemptsLeft = max(p.Attempts-attempts, 0)
	if plan.AttemptsLeft == 0 {
		return plan
	}
	next := known.finished.Add(p.After)
	if !last.IsZero() && last.Add(p.After).After(next) {
		next = last.Add(p.After)
	}
	// A time already gone means the next pass, not a date in the past.
	next = later(next, c.Now())
	plan.Automatic, plan.NextAt = true, &next
	return plan
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// aiContextStaleReason is the sentence the card carries while the context is
// behind. With a diagnosis for the commit it is that, and otherwise the generic
// reason the check produced; the suffix says what became of the old snapshot.
func (c *Controller) aiContextStaleReason(id, commit, generic, suffix string) string {
	c.aiContextRunsMu.Lock()
	known := c.aiContextDiagnoses[id]
	c.aiContextRunsMu.Unlock()
	if known != nil && known.diagnosis != nil && known.commit == commit {
		return known.diagnosis.Summary() + suffix
	}
	return generic
}

// aiContextProblemShapes is each cause as a problem: its code and how loudly it
// should be said. The codes are literal here, and not built from the cause, so
// that the documentation test can find every one.
//
// A cause that needs a person before a run can succeed is an error, because the
// context stays stale until somebody acts. One Zoomies expects to clear by
// itself is a warning: worth knowing, not worth waking anybody.
var aiContextProblemShapes = map[aicontext.Cause]Problem{
	aicontext.CauseArtifactQuota:      {Code: "ai_context.artifact_quota", Severity: config.SeverityError},
	aicontext.CauseArtifactUpload:     {Code: "ai_context.artifact_upload_failed", Severity: config.SeverityWarning},
	aicontext.CauseRunnerUnavailable:  {Code: "ai_context.runner_unavailable", Severity: config.SeverityWarning},
	aicontext.CauseStartupFailed:      {Code: "ai_context.startup_failed", Severity: config.SeverityError},
	aicontext.CauseSetupFailed:        {Code: "ai_context.setup_failed", Severity: config.SeverityWarning},
	aicontext.CauseOversizedFile:      {Code: "ai_context.oversized_file", Severity: config.SeverityError},
	aicontext.CauseTooMuchSource:      {Code: "ai_context.too_much_source", Severity: config.SeverityError},
	aicontext.CauseGenerationRefused:  {Code: "ai_context.generation_refused", Severity: config.SeverityError},
	aicontext.CausePublicationRefused: {Code: "ai_context.publication_refused", Severity: config.SeverityError},
	aicontext.CauseDeliveryFailed:     {Code: "ai_context.delivery_failed", Severity: config.SeverityError},
	aicontext.CauseTimedOut:           {Code: "ai_context.timed_out", Severity: config.SeverityWarning},
	aicontext.CauseUnknown:            {Code: "ai_context.run_failed", Severity: config.SeverityWarning},
}

// aiContextProblems raises one problem per repository whose context is behind
// because the managed workflow failed. The card already says it; this is the same
// sentence where an operator who is not looking at that page will see it, and
// where an assistant asking for problems will.
func (c *Controller) aiContextProblems(ctx context.Context, out *[]Problem) error {
	c.aiContextRunsMu.Lock()
	ids := make([]string, 0, len(c.aiContextDiagnoses))
	for id, known := range c.aiContextDiagnoses {
		if known.diagnosis != nil {
			ids = append(ids, id)
		}
	}
	c.aiContextRunsMu.Unlock()
	for _, id := range ids {
		r, err := c.st.GetAIContextRepository(ctx, id)
		if err != nil {
			// A repository that has gone has no problem; one that could not be
			// read is a database fault the other sections will report.
			continue
		}
		d := c.AIContextDiagnosis(r)
		if d == nil {
			continue
		}
		shape, ok := aiContextProblemShapes[d.Cause]
		if !ok {
			shape = aiContextProblemShapes[aicontext.CauseUnknown]
		}
		failed := d.FailedAt
		*out = append(*out, Problem{
			Code: shape.Code, Severity: shape.Severity,
			Title:      d.Title + " (" + r.FullName + ")",
			Detail:     d.Detail,
			Fix:        d.Fix + " " + aiContextRetrySentence(d),
			TargetKind: "ai_context", TargetID: r.ID, Since: &failed,
		})
	}
	return nil
}

// aiContextRetrySentence says what Zoomies will do next, so that "it will fix
// itself" and "it will not" are never left for the reader to guess.
func aiContextRetrySentence(d *aicontext.Diagnosis) string {
	switch {
	case d.Retry != nil && d.Retry.Automatic && d.Retry.NextAt != nil:
		return fmt.Sprintf("Zoomies will start the workflow again from %s (%s left for this commit).",
			d.Retry.NextAt.UTC().Format("2 Jan 15:04 MST"), plural(d.Retry.AttemptsLeft, "attempt"))
	case d.Cause.Policy().Automatic:
		return "Zoomies has started the workflow as often as it will for this commit; use Regenerate once the cause is fixed."
	default:
		return "Zoomies will not start the workflow again until this is fixed, because it would fail the same way."
	}
}
