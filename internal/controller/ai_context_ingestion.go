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
					_ = c.RefreshAIContext(checkCtx, id)
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
		return nil, nil, fmt.Errorf("AI Context has been removed; reinstall it to resume source access")
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
		return nil, nil, fmt.Errorf("restore GitHub Contents read permission")
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
		return nil, nil, fmt.Errorf("repository identity or trusted branch changed")
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
				if file.Path == aicontext.WorkflowPath {
					legacy, legacyErr := aicontext.LegacySetupWorkflow(r.Key, r.Config)
					if legacyErr == nil && files[file.Path] == legacy {
						continue
					}
				}
				return nil, nil, fmt.Errorf("managed workflow or configuration changed; review a repair before ingestion")
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
		state, reason := "unavailable", "Repository setup or GitHub access could not be verified."
		if errors.Is(err, errAIContextAwaitingMerge) {
			state, reason = "awaiting_merge", "Merge the reviewed setup PR before generating context."
		}
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
		if err == nil {
			err = r.Config.CheckSourceFiles(publication.Snapshot.Files)
		}
		if err == nil && publication.Snapshot.Manifest.Generator != "repomix@1.18.1" {
			err = fmt.Errorf("context generator identity does not match its managed workflow")
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
		reason := "Current generation could not be verified. The previous snapshot is retained."
		if transient {
			reason = "Current generation could not be verified. Repository-only output keeps no copy."
		}
		_ = c.failAIContextCheck(ctx, id, "stale", source.Commit, reason)
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
