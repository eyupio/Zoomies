package github

import (
	"context"
	"fmt"

	"github.com/eyupio/zoomies/internal/store"
)

// RepoReader is everything Kennel Club asks GitHub, and it is read-only by
// construction: there is no method on it that writes, so no change to the
// controller can make Kennel Club write through it. It is not part of Client,
// for the reason ContextRunReader is not: a capability one feature uses does not
// belong on the interface every fake and every demo client has to satisfy, so
// the controller asks for it by type assertion and a client without it simply
// has no Kennel Club coverage to report.
type RepoReader interface {
	// KennelRepositories lists the repositories the installation can see, through
	// the conditional client: Kennel Club asks again every refresh, and a listing
	// GitHub says is unchanged costs nothing against the rate limit.
	KennelRepositories(ctx context.Context, limit int) ([]Repository, error)
	// KennelRun reads four fields of one workflow run, and no others.
	KennelRun(ctx context.Context, repo string, runID int64) (*KennelRun, error)
}

var _ RepoReader = (*appClient)(nil)

// KennelEndpoints are the GitHub endpoints Kennel Club may call, in
// the form its documentation lists them. A test runs the reader against the fake
// and fails on any request that is not one of these, so a new call is a visible
// change to this list in review rather than something found in a log.
var KennelEndpoints = []string{
	"GET /installation/repositories",
	"GET /repos/{owner}/{repo}",
	"GET /repos/{owner}/{repo}/actions/runs/{run}",
	"GET /orgs/{org}/actions/runner-groups",
	// The budget is a share of the limit the installation reports, and this is
	// how it reports it. GitHub does not count the request against the limit.
	"GET /rate_limit",
	// Optional repository setup reads only the default-branch file inventory.
	"GET /repos/{owner}/{repo}/git/trees/{tree}",
	"GET /repos/{owner}/{repo}/git/blobs/{blob}",
	// The optional settings reads. A public repository has an approval policy for
	// fork pull requests and a private one has the rules for them, so each
	// repository makes one of the two. Rules need only Metadata; everything else
	// here needs the App's Administration read permission.
	"GET /repos/{owner}/{repo}/actions/permissions/workflow",
	"GET /repos/{owner}/{repo}/actions/permissions/fork-pr-contributor-approval",
	"GET /repos/{owner}/{repo}/actions/permissions/fork-pr-workflows-private-repos",
	"GET /repos/{owner}/{repo}/branches/{branch}/protection",
	"GET /repos/{owner}/{repo}/rules/branches/{branch}",
}

// KennelRun is the four fields of a workflow run that the exposure checks read.
// Everything else on a run -- who triggered it, its branch, its title, the path
// of its workflow -- is written by whoever opened the pull request, and is never
// read, so there is nothing of theirs to leak.
type KennelRun struct {
	ID int64
	// Event is GitHub's name for what triggered the run, bounded to 64 bytes. The
	// controller passes it through an allow-list before anything sees it.
	Event string
	// RepositoryID is the repository the run ran in; HeadRepositoryID is the one
	// its head commit is in. They differ for a pull request from a fork, and a
	// fork that has since been deleted has no head repository at all, which reads
	// as zero.
	RepositoryID     int64
	HeadRepositoryID int64
}

// FromFork says whether the run's head is somewhere other than where it ran. A
// deleted fork is still a fork: the code in it was written by somebody else.
func (r KennelRun) FromFork() bool { return r.HeadRepositoryID != r.RepositoryID }

// maxKennelEvent bounds the event name a run may carry into the controller.
const maxKennelEvent = 64

func (c *appClient) KennelRepositories(ctx context.Context, limit int) ([]Repository, error) {
	return c.listRepositories(ctx, c.readClient(), limit)
}

func (c *appClient) KennelRun(ctx context.Context, repo string, runID int64) (*KennelRun, error) {
	owner, name, kind := SplitTarget(repo)
	if kind != store.TargetRepo || runID <= 0 {
		return nil, fmt.Errorf("github: invalid workflow run %q/%d", repo, runID)
	}
	run, resp, err := c.readClient().Actions.GetWorkflowRunByID(ctx, owner, name, runID)
	if err != nil {
		return nil, c.fail("read workflow run", resp, err)
	}
	base := run.GetRepository().GetID()
	if base == 0 {
		// A run whose repository GitHub did not name cannot be compared with
		// its head, and guessing would be reporting a fork from nothing.
		return nil, fmt.Errorf("github: read workflow run %s/%d: GitHub did not say which repository it ran in", repo, runID)
	}
	event := run.GetEvent()
	if len(event) > maxKennelEvent {
		event = event[:maxKennelEvent]
	}
	return &KennelRun{
		ID:               runID,
		Event:            event,
		RepositoryID:     base,
		HeadRepositoryID: run.GetHeadRepository().GetID(),
	}, nil
}
