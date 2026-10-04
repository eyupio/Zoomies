package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

var errAIContextAwaitingMerge = errors.New("merge the reviewed setup PR before generating context")

var errAIContextAwaitingUpload = errors.New("the workflow has not uploaded context for the current commit yet")

// The refusals below are the ways trustedAIContext can say no for a reason the
// operator can act on. Each is a sentinel so the card can name the fix; the
// error text itself never reaches it, because a GitHub error can carry
// repository content.
var (
	errAIContextRemoved       = errors.New("AI Context has been removed; reinstall it to resume source access")
	errAIContextNoContents    = errors.New("restore GitHub Contents read permission")
	errAIContextIdentity      = errors.New("repository identity or trusted branch changed")
	errAIContextManagedDrifts = errors.New("managed workflow or configuration changed; review a repair before ingestion")
)

// aiContextAccessFailure turns a failed setup or access check into the state and
// the sentence the repository card shows. "Could not be verified" is true of all
// of these and useful for none: an operator facing a closed gate needs to know
// whether to fix a permission, repair the setup or simply wait.
func aiContextAccessFailure(err error) (state, reason string) {
	switch {
	case errors.Is(err, errAIContextAwaitingMerge):
		return "awaiting_merge", "Merge the reviewed setup PR before generating context."
	case errors.Is(err, errAIContextRemoved):
		return "unavailable", "AI Context was removed for this repository. Reinstall it to resume source access."
	case errors.Is(err, errAIContextNoContents):
		return "unavailable", "The GitHub App has lost Contents read permission. Restore it on the installation."
	case errors.Is(err, errAIContextIdentity):
		return "unavailable", "The repository was renamed, archived or had its default branch changed. Use Reinstall / repair to review the setup again."
	case errors.Is(err, errAIContextManagedDrifts):
		return "unavailable", "The managed workflow or configuration was edited after review. Use Reinstall / repair to restore it."
	case errors.Is(err, store.ErrNotFound):
		return "unavailable", "No reviewed setup is recorded for this repository. Open the setup and submit it."
	case errors.Is(err, github.ErrRateLimited):
		return "unavailable", "GitHub's rate limit was reached. Zoomies checks again on its next pass."
	case errors.Is(err, github.ErrForbidden):
		return "unavailable", "GitHub refused access to the repository. Check the App installation still includes it and has Contents read permission."
	case errors.Is(err, github.ErrNotFound):
		return "unavailable", "GitHub could not find the repository or its setup PR. Check it still exists and the App installation includes it."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "unavailable", "GitHub did not answer in time. Zoomies checks again on its next pass."
	}
	return "unavailable", "Repository setup or GitHub access could not be verified."
}

// aiContextPublicationFailure explains why the generated context was not
// admitted. The commonest cause is not a fault at all -- the branch moved and
// the workflow has not caught up -- and the card should say so.
func aiContextPublicationFailure(err error, branch string, transient bool) string {
	var reason string
	switch {
	case errors.Is(err, github.ErrContextMismatch):
		reason = "The published context is for an older commit of " + branch + ". It refreshes when the workflow finishes; use Regenerate to run it now."
	case errors.Is(err, github.ErrNotFound):
		reason = "No generated context was found. Run the Zoomies AI Context workflow, or use Regenerate."
	case errors.Is(err, errAIContextGenerator):
		reason = "The published context came from a different generator than the setup pins. Use Reinstall / repair to restore the workflow."
	default:
		reason = "The generated context failed verification. Check the latest Zoomies AI Context workflow run in the repository."
	}
	if transient {
		return reason + " Repository-only output keeps no copy."
	}
	return reason + " The previous snapshot is retained."
}

var errAIContextGenerator = errors.New("context generator identity does not match its managed workflow")

// Context verification has its own bounded loop so a repository pack cannot
// hold the fleet's scheduling lock. Polling also recovers missed push events.
func (c *Controller) aiContextLoop(ctx context.Context) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			ids, err := c.st.ListAIContextVerificationCandidates(ctx, 20, offset)
			if err == nil {
				for _, id := range ids {
					checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
					_ = c.SyncAIContext(checkCtx, id, aiContextRunGrace)
					cancel()
					if ctx.Err() != nil {
						return
					}
				}
				offset += len(ids)
				if len(ids) < 20 {
					offset = 0
				}
			}
			timer.Reset(5 * time.Minute)
		}
	}
}
func (c *Controller) trustedAIContext(ctx context.Context, r *store.AIContextRepository) (github.ContextIngestionClient, *github.ContextSetupSource, error) {
	if r.Config.Disabled {
		return nil, nil, errAIContextRemoved
	}
	setup, err := c.st.GetAIContextSetup(ctx, r.ID)
	if err != nil {
		return nil, nil, err
	}
	if setup.State != "awaiting_merge" || setup.PRNumber < 1 {
		return nil, nil, errAIContextAwaitingMerge
	}
	client, err := c.ClientFor(ctx, r.Key.InstallationID)
	if err != nil {
		return nil, nil, err
	}
	info, err := client.Probe(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !info.CanReadContents() {
		return nil, nil, errAIContextNoContents
	}
	ingestion, ok := client.(github.ContextIngestionClient)
	if !ok {
		return nil, nil, fmt.Errorf("this client cannot verify generated context")
	}
	merged, err := ingestion.ContextSetupMerged(ctx, r.FullName, setup.PRNumber, r.Config.SourceBranch)
	if err != nil {
		return nil, nil, err
	}
	if !merged {
		return nil, nil, errAIContextAwaitingMerge
	}
	sourceClient, ok := client.(github.ContextSetupClient)
	if !ok {
		return nil, nil, fmt.Errorf("this client cannot verify managed context files")
	}
	source, err := sourceClient.ReadContextSetup(ctx, r.FullName, r.Config.SourceBranch)
	if err != nil {
		return nil, nil, err
	}
	if source.Repository.ID != r.Key.RepositoryID || source.Repository.FullName != r.FullName || source.Repository.Archived || source.Repository.DefaultBranch != r.Config.SourceBranch {
		return nil, nil, errAIContextIdentity
	}
	desired, err := aicontext.PlanManagedSetup(r.Key, r.FullName, r.Config, nil)
	if err != nil {
		return nil, nil, err
	}
	files := map[string]string{}
	for _, file := range source.Files {
		files[file.Path] = file.Content
	}
	for _, file := range desired {
		switch file.Path {
		case aicontext.ConfigPath, aicontext.WorkflowPath, aicontext.GeneratorPackagePath, aicontext.GeneratorLockPath:
			if files[file.Path] != file.Content {
				if file.Path == aicontext.WorkflowPath && aicontext.IsOlderSetupWorkflow(r.Key, r.Config, files[file.Path]) {
					continue
				}
				return nil, nil, errAIContextManagedDrifts
			}
		}
	}
	return ingestion, source, nil
}
func (c *Controller) RefreshAIContext(ctx context.Context, id string) error {
	_, _, err := c.verifyAIContext(ctx, id)
	return err
}

// verifyAIContext runs the live access, setup and publication checks. For
// repository-only output it returns the verified snapshot in memory and records
// freshness without storing a byte of source; for Both the snapshot lives in the
// database and the returned one is nil.
func (c *Controller) verifyAIContext(ctx context.Context, id string) (*aicontext.Snapshot, string, error) {
	select {
	case c.aiContextChecks <- struct{}{}:
		defer func() { <-c.aiContextChecks }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}

	r, err := c.st.GetAIContextRepository(ctx, id)
	if err != nil {
		return nil, "", err
	}
	// Close the access gate on every failed verification, including transient
	// network failures. Source never falls back to an unverified retained pack.
	client, source, err := c.trustedAIContext(ctx, r)
	if err != nil {
		state, reason := aiContextAccessFailure(err)
		_ = c.failAIContextCheck(ctx, id, state, "", reason)
		return nil, "", err
	}
	if r.Config.Destination == aicontext.Zoomies {
		// Nothing to fetch: the workflow uploads, and the upload is verified
		// when it arrives. A check only confirms the stored snapshot is still
		// the current commit's, with everything above still true.
		hash, _ := r.Config.Hash()
		if f, _ := c.st.GetAIContextFreshness(ctx, id); f != nil && f.PublishedCommit == source.Commit && f.Digest != "" {
			if _, err := c.st.GetAIContextSnapshot(ctx, id, f.Digest); err == nil {
				if err = c.st.ConfirmAIContextSnapshot(ctx, id, r.Revision, f.Digest, source.Commit, hash); err == nil {
					return nil, f.Digest, nil
				}
			}
		}
		_ = c.failAIContextCheck(ctx, id, "stale", source.Commit, "Waiting for the workflow to upload context for the current commit. The previous snapshot is retained.")
		return nil, "", errAIContextAwaitingUpload
	}
	transient := r.Config.Destination == aicontext.Repository
	hash, _ := r.Config.Hash()
	if !transient {
		freshness, _ := c.st.GetAIContextFreshness(ctx, id)
		if freshness != nil && freshness.PublishedCommit == source.Commit && freshness.Digest != "" {
			if _, err := c.st.GetAIContextSnapshot(ctx, id, freshness.Digest); err == nil {
				if err = c.st.ConfirmAIContextSnapshot(ctx, id, r.Revision, freshness.Digest, source.Commit, hash); err == nil {
					return nil, freshness.Digest, nil
				}
			}
		}
	}
	publication, err := client.ReadContextPublication(ctx, r.FullName, r.Config.SourceBranch)
	var digest string
	if err == nil {
		err = publication.Snapshot.Match(r.Key, r.Config.SourceBranch, source.Commit, hash)
		if err != nil && publication.Snapshot.Validate() == nil {
			err = fmt.Errorf("%w: %v", github.ErrContextMismatch, err)
		}
		if err == nil {
			err = r.Config.CheckSourceFiles(publication.Snapshot.Files)
		}
		if err == nil && publication.Snapshot.Manifest.Generator != "repomix@1.18.1" {
			err = errAIContextGenerator
		}
	}
	if err == nil {
		if transient {
			var body []byte
			if body, err = json.Marshal(publication.Snapshot); err == nil {
				digest = aicontext.Hash(body)
				err = c.st.ConfirmAIContextTransient(ctx, id, r.Revision, source.Commit, digest, hash)
			}
		} else {
			err = c.st.PublishAIContextSnapshot(ctx, id, r.Revision, publication.Snapshot)
		}
	}
	if err != nil {
		_ = c.failAIContextCheck(ctx, id, "stale", source.Commit, aiContextPublicationFailure(err, r.Config.SourceBranch, transient))
		return nil, "", err
	}
	if transient {
		return publication.Snapshot, digest, nil
	}
	return nil, digest, nil
}

// VerifiedAIContextSnapshot repeats live access and workflow checks before
// returning source. Background polling alone is not an access gate.
func (c *Controller) VerifiedAIContextSnapshot(ctx context.Context, id string) (*aicontext.Snapshot, *store.AIContextFreshness, error) {
	transient, _, err := c.verifyAIContext(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	f, err := c.st.GetAIContextFreshness(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if transient != nil {
		return transient, f, nil
	}
	snapshot, err := c.st.GetAIContextSnapshot(ctx, id, f.Digest)
	return snapshot, f, err
}

func (c *Controller) failAIContextCheck(ctx context.Context, id, state, desired, reason string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.st.FailAIContextCheck(cleanup, id, state, desired, reason)
}

// AIContextRefusal is a regeneration Zoomies declined for a reason the
// operator can fix. The message is written to be shown as it is.
type AIContextRefusal struct{ Message string }

func (e *AIContextRefusal) Error() string { return e.Message }

// RegenerateAIContext runs the managed workflow on the trusted branch, for the
// repository whose context has gone stale and will not catch up on its own --
// the push that should have started it was missed, or the run failed. It asks
// GitHub to start the workflow and nothing else: the result arrives the way any
// other generation does, through verification, so a regeneration can neither
// open the source gate nor write a snapshot itself.
//
// trustedAIContext runs first because dispatching runs the workflow file as it
// is on the branch today; that is only safe while it is still the reviewed one.
func (c *Controller) RegenerateAIContext(ctx context.Context, id string) error {
	r, err := c.st.GetAIContextRepository(ctx, id)
	if err != nil {
		return err
	}
	client, _, err := c.trustedAIContext(ctx, r)
	if err != nil {
		if errors.Is(err, errAIContextAwaitingMerge) || errors.Is(err, errAIContextRemoved) || errors.Is(err, errAIContextManagedDrifts) || errors.Is(err, errAIContextIdentity) || errors.Is(err, errAIContextNoContents) {
			_, reason := aiContextAccessFailure(err)
			return &AIContextRefusal{Message: reason}
		}
		return err
	}
	dispatcher, ok := client.(github.ContextWorkflowDispatcher)
	if !ok {
		return &AIContextRefusal{Message: "This GitHub client cannot start the context workflow."}
	}
	if err := dispatcher.DispatchContextWorkflow(ctx, r.FullName, r.Config.SourceBranch); err != nil {
		if errors.Is(err, github.ErrForbidden) {
			return &AIContextRefusal{Message: "GitHub refused to start the workflow. Grant the App \"Actions\" read and write permission and accept it on the installation, or run the Zoomies AI Context workflow from the repository's Actions tab."}
		}
		if errors.Is(err, github.ErrNotFound) || errors.Is(err, github.ErrInvalid) {
			return &AIContextRefusal{Message: "GitHub could not start the Zoomies AI Context workflow on " + r.Config.SourceBranch + ". Use Reinstall / repair to restore it."}
		}
		return err
	}
	return nil
}

// How long the controller gives a push-triggered run to publish before it starts
// one itself, how many runs it will start for one commit, and how far apart.
// The grace covers the whole pipeline -- the workflow allows fifteen minutes to
// generate and ten to publish -- because its concurrency group cancels a run in
// progress: starting one while a healthy run is still working would throw that
// run's work away. The cap stops a workflow that fails every time from being
// started for ever; only a dispatch GitHub accepted counts against it.
const (
	aiContextRunGrace    = 30 * time.Minute
	aiContextRunAttempts = 2
	aiContextRunSpacing  = 30 * time.Minute
)

type aiContextRun struct {
	commit    string
	since     time.Time
	attempts  int
	lastStart time.Time
}

// aiContextNeedsRun is whether a failed verification is the kind a workflow run
// fixes: nothing published yet, or what is published is for another commit. A
// lost permission, a drifted workflow or a broken publication is not, and
// starting the workflow would only repeat the failure.
func aiContextNeedsRun(err error) bool {
	return errors.Is(err, github.ErrContextMismatch) || errors.Is(err, github.ErrNotFound) || errors.Is(err, errAIContextAwaitingUpload)
}

// SyncAIContext verifies a repository and, when the only thing wrong is that
// the published context is behind the trusted branch, starts the managed
// workflow itself once the push-triggered run has had grace to finish. The
// workflow already runs on every push, so this is the safety net under it: a
// push GitHub did not deliver, a run that was cancelled by a later one, or a
// run that failed all leave the same stale card, and this is what clears it
// without anybody pressing a button.
//
// It never acts on a verification that failed for any other reason, and it
// starts a given commit's workflow at most aiContextRunAttempts times.
func (c *Controller) SyncAIContext(ctx context.Context, id string, grace time.Duration) error {
	verifyErr := c.RefreshAIContext(ctx, id)
	if verifyErr == nil {
		c.aiContextRunsMu.Lock()
		delete(c.aiContextRuns, id)
		c.aiContextRunsMu.Unlock()
		return nil
	}
	if !aiContextNeedsRun(verifyErr) {
		return verifyErr
	}
	f, err := c.st.GetAIContextFreshness(ctx, id)
	if err != nil || f.State != "stale" || f.DesiredCommit == "" {
		return verifyErr
	}
	now := c.Now()
	c.aiContextRunsMu.Lock()
	run := c.aiContextRuns[id]
	if run == nil || run.commit != f.DesiredCommit {
		run = &aiContextRun{commit: f.DesiredCommit, since: now}
		c.aiContextRuns[id] = run
	}
	due := now.Sub(run.since) >= grace && run.attempts < aiContextRunAttempts && (run.lastStart.IsZero() || now.Sub(run.lastStart) >= aiContextRunSpacing)
	if due {
		// Spacing applies to a failed request too, so an outage or a missing
		// permission is retried every half hour rather than every pass; the count
		// is given back below, so it never uses up the attempts a working
		// dispatch would have had.
		run.attempts++
		run.lastStart = now
	}
	c.aiContextRunsMu.Unlock()
	if !due {
		return verifyErr
	}
	if err := c.RegenerateAIContext(ctx, id); err != nil {
		c.aiContextRunsMu.Lock()
		if c.aiContextRuns[id] == run {
			run.attempts--
		}
		c.aiContextRunsMu.Unlock()
		c.log.Warn("could not start the AI Context workflow", "repository", id, "error", err)
	}
	return verifyErr
}
