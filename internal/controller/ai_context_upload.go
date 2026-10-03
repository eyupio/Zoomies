package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// ErrAIContextUploadRefused is a valid Actions token that does not entitle
// this upload: the wrong workflow, branch, event or commit. The wrapped text
// says which, for the workflow log; it never carries source.
var ErrAIContextUploadRefused = errors.New("this upload is not from the repository's managed workflow on its trusted branch")

// ErrAIContextUploadSuperseded is an upload for a commit that is no longer the
// head of the trusted branch, or a token that has already been used. Either
// way a later run is the one that counts.
var ErrAIContextUploadSuperseded = errors.New("this upload has been superseded")

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// SetActionsIssuer points upload verification at another OIDC issuer. Tests
// use it with github.NewFakeActionsIssuer; production keeps GitHub's.
func (c *Controller) SetActionsIssuer(issuer string) {
	c.actionsTokens = github.NewActionsTokenVerifier(issuer)
}

// CheckAIContextUploadToken verifies an upload's token alone, so the API can
// refuse a stranger before reading a body of up to 32 MiB. The full check runs
// again in IngestAIContextUpload; the keys are cached, so that costs nothing.
func (c *Controller) CheckAIContextUploadToken(ctx context.Context, rawToken, audience string) error {
	_, err := c.actionsTokens.Verify(ctx, rawToken, audience)
	return err
}

// IngestAIContextUpload admits a Zoomies-only snapshot. The token proves which
// repository, branch, commit and workflow file ran; everything the Both mode
// checks on a generated branch is then checked here too, against GitHub.
// Nothing about the request body is trusted until all of that holds.
func (c *Controller) IngestAIContextUpload(ctx context.Context, rawToken, audience string, body []byte) (*store.AIContextRepository, error) {
	claims, err := c.actionsTokens.Verify(ctx, rawToken, audience)
	if err != nil {
		return nil, err
	}
	numericID, err := strconv.ParseInt(claims.RepositoryID, 10, 64)
	if err != nil || numericID <= 0 {
		return nil, fmt.Errorf("%w: the token names no repository", ErrAIContextUploadRefused)
	}
	r, err := c.st.FindAIContextUploadTarget(ctx, "github.com", numericID)
	if err != nil {
		return nil, err
	}
	// The configuration fixes the audience. A token minted for this address
	// still has to belong to a repository whose reviewed workflow names it.
	if r.Config.UploadURL != audience {
		return nil, fmt.Errorf("%w: the repository's configuration names another upload address", ErrAIContextUploadRefused)
	}
	branch := r.Config.SourceBranch
	trustedRef := "refs/heads/" + branch
	workflow := "/.github/workflows/zoomies-ai-context.yml@" + trustedRef
	switch {
	case claims.Ref != trustedRef:
		return nil, fmt.Errorf("%w: it ran on %q, not the trusted branch %q", ErrAIContextUploadRefused, claims.Ref, branch)
	case claims.EventName != "push" && claims.EventName != "workflow_dispatch":
		return nil, fmt.Errorf("%w: it was started by %q; only a push or a manual run of the trusted branch may upload", ErrAIContextUploadRefused, claims.EventName)
	// Repository names are case-insensitive on GitHub; the ref after them is not.
	case !strings.HasSuffix(claims.JobWorkflowRef, workflow) ||
		!strings.EqualFold(strings.TrimSuffix(claims.JobWorkflowRef, workflow), r.FullName):
		return nil, fmt.Errorf("%w: it came from %q, not the managed workflow", ErrAIContextUploadRefused, claims.JobWorkflowRef)
	case !commitSHA.MatchString(claims.SHA):
		return nil, fmt.Errorf("%w: the token names no commit", ErrAIContextUploadRefused)
	}
	if err := c.st.ClaimAIContextUploadToken(ctx, claims.ID, r.ID, claims.Expiry); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, fmt.Errorf("%w: this token has already been used", ErrAIContextUploadSuperseded)
		}
		return nil, err
	}
	snapshot, err := aicontext.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: the snapshot is not valid: %v", ErrAIContextUploadRefused, err)
	}
	if snapshot.Manifest.SourceCommit != claims.SHA {
		return nil, fmt.Errorf("%w: the snapshot is for another commit than the run that sent it", ErrAIContextUploadRefused)
	}

	select {
	case c.aiContextChecks <- struct{}{}:
		defer func() { <-c.aiContextChecks }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	client, source, err := c.trustedAIContext(ctx, r)
	if err != nil {
		_ = c.failAIContextCheck(ctx, r.ID, "unavailable", claims.SHA, "Repository setup or GitHub access could not be verified.")
		return nil, err
	}
	if source.Commit != claims.SHA {
		return nil, fmt.Errorf("%w: the trusted branch has moved on to a newer commit", ErrAIContextUploadSuperseded)
	}
	hash, _ := r.Config.Hash()
	err = snapshot.Match(r.Key, branch, source.Commit, hash)
	if err == nil {
		err = r.Config.CheckSourceFiles(snapshot.Files)
	}
	if err == nil && snapshot.Manifest.Generator != "repomix@1.18.1" {
		err = fmt.Errorf("context generator identity does not match its managed workflow")
	}
	if err == nil {
		err = client.VerifyContextSnapshot(ctx, r.FullName, branch, source.Commit, snapshot)
	}
	if err == nil {
		err = c.st.PublishAIContextSnapshot(ctx, r.ID, r.Revision, snapshot)
	}
	if err != nil {
		_ = c.failAIContextCheck(ctx, r.ID, "stale", source.Commit, "The uploaded context could not be verified. The previous snapshot is retained.")
		return nil, fmt.Errorf("%w: %v", ErrAIContextUploadRefused, err)
	}
	return c.st.GetAIContextRepository(ctx, r.ID)
}
