package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

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

// actionsVerifiers holds a token verifier per issuer, made on first use so
// each keeps its own cached key set.
type actionsVerifiers struct {
	mu        sync.Mutex
	issuerFor func(host string) string
	byIssuer  map[string]*github.ActionsTokenVerifier
}

func (v *actionsVerifiers) issuer(host string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return strings.TrimRight(v.issuerFor(host), "/")
}

func (v *actionsVerifiers) verifier(issuer string) *github.ActionsTokenVerifier {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.byIssuer == nil {
		v.byIssuer = map[string]*github.ActionsTokenVerifier{}
	}
	if v.byIssuer[issuer] == nil {
		v.byIssuer[issuer] = github.NewActionsTokenVerifier(issuer)
	}
	return v.byIssuer[issuer]
}

// SetActionsIssuer points GitHub.com upload verification at another OIDC
// issuer. Tests use it with github.NewFakeActionsIssuer; production keeps
// GitHub's.
func (c *Controller) SetActionsIssuer(issuer string) {
	c.SetActionsIssuerFor(func(host string) string {
		if host == "github.com" {
			return issuer
		}
		return github.ActionsIssuerFor(host)
	})
}

// SetActionsIssuerFor replaces how a host's Actions issuer is found, for tests
// that stand in for an Enterprise Server's.
func (c *Controller) SetActionsIssuerFor(issuerFor func(host string) string) {
	c.actionsTokens.mu.Lock()
	defer c.actionsTokens.mu.Unlock()
	c.actionsTokens.issuerFor, c.actionsTokens.byIssuer = issuerFor, nil
}

// verifyUploadToken checks a token against the issuer of the GitHub host it
// claims to come from, and returns that host. The claim picks which keys to
// check against and nothing more: it is accepted only when it names
// GitHub.com's issuer or that of an Enterprise Server one of this controller's
// Zoomies-only repositories lives on, so a stranger's token cannot send the
// controller to fetch keys from an address of its choosing.
func (c *Controller) verifyUploadToken(ctx context.Context, rawToken, audience string) (*github.ActionsClaims, string, error) {
	claimed, err := github.UnverifiedActionsIssuer(rawToken)
	if err != nil {
		return nil, "", err
	}
	hosts, err := c.st.AIContextUploadHosts(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, host := range append([]string{"github.com"}, hosts...) {
		if issuer := c.actionsTokens.issuer(host); issuer == claimed {
			claims, err := c.actionsTokens.verifier(issuer).Verify(ctx, rawToken, audience)
			return claims, host, err
		}
	}
	return nil, "", fmt.Errorf("%w: it was issued by %q, which is neither GitHub.com nor an Enterprise Server with a Zoomies-only repository here", github.ErrActionsToken, claimed)
}

// CheckAIContextUploadToken verifies an upload's token alone, so the API can
// refuse a stranger before reading a body of up to 32 MiB. The full check runs
// again in IngestAIContextUpload; the keys are cached, so that costs nothing.
func (c *Controller) CheckAIContextUploadToken(ctx context.Context, rawToken, audience string) error {
	_, _, err := c.verifyUploadToken(ctx, rawToken, audience)
	return err
}

// IngestAIContextUpload admits a Zoomies-only snapshot. The token proves which
// repository, branch, commit and workflow file ran; everything the Both mode
// checks on a generated branch is then checked here too, against GitHub.
// Nothing about the request body is trusted until all of that holds.
func (c *Controller) IngestAIContextUpload(ctx context.Context, rawToken, audience string, body []byte) (*store.AIContextRepository, error) {
	claims, host, err := c.verifyUploadToken(ctx, rawToken, audience)
	if err != nil {
		return nil, err
	}
	numericID, err := strconv.ParseInt(claims.RepositoryID, 10, 64)
	if err != nil || numericID <= 0 {
		return nil, fmt.Errorf("%w: the token names no repository", ErrAIContextUploadRefused)
	}
	r, err := c.st.FindAIContextUploadTarget(ctx, host, numericID)
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
		err = r.Config.CheckSnapshotFiles(snapshot)
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
