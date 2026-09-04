package github

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	gh "github.com/google/go-github/v88/github"
)

// Bounds on what one migration scan will read.
//
// A scan walks an organisation's repositories and reads every workflow file in
// each one, so it is by far the most expensive thing Zoomies asks GitHub for.
// These caps keep one operator's curiosity from spending the installation's
// hourly quota, which the scheduler shares.
const (
	// maxScanRepos is the most repositories one scan will list.
	maxScanRepos = 500
	// maxWorkflowsPerRepo is the most workflow files read from one repository.
	// A repository with more than this has a generated .github/workflows and
	// is not a candidate for a hand-reviewed pull request.
	maxWorkflowsPerRepo = 50
	// maxWorkflowBytes is the largest workflow file that will be read. GitHub's
	// contents API stops inlining at 1MB anyway, and a workflow of this size is
	// generated.
	maxWorkflowBytes = 512 << 10
	// workflowDir is the only directory GitHub runs workflows from.
	workflowDir = ".github/workflows"
)

// ErrNoWorkflows is returned when a repository has no .github/workflows at all,
// which is not a failure: most repositories in a large organisation do not.
var ErrNoWorkflows = errors.New("github: the repository has no .github/workflows directory")

// ListRepositories returns the repositories this installation can see.
//
// For a repo-scoped installation that is the one repository, which is looked up
// so that its default branch is known: a pull request has to be opened against
// something, and assuming "main" is how a migration silently fails on a
// repository that still uses "master".
func (c *appClient) ListRepositories(ctx context.Context, limit int) ([]Repository, error) {
	if limit <= 0 || limit > maxScanRepos {
		limit = maxScanRepos
	}
	if !c.isOrg() {
		repo, resp, err := c.asInstallation.Repositories.Get(ctx, c.owner, c.repo)
		if err != nil {
			return nil, c.fail(fmt.Sprintf("read repository %s", c.target), resp, err)
		}
		return []Repository{repositoryOf(repo)}, nil
	}

	opts := &gh.ListOptions{PerPage: pollPerPage}
	var out []Repository
	for len(out) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, resp, err := c.asInstallation.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, c.fail("list repositories visible to the installation", resp, err)
		}
		for _, r := range page.Repositories {
			if r.GetFullName() == "" {
				continue
			}
			out = append(out, repositoryOf(r))
			if len(out) == limit {
				break
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func repositoryOf(r *gh.Repository) Repository {
	return Repository{
		FullName:      r.GetFullName(),
		DefaultBranch: r.GetDefaultBranch(),
		Private:       r.GetPrivate(),
		Archived:      r.GetArchived(),
		HTMLURL:       r.GetHTMLURL(),
	}
}

// ListWorkflows reads the workflow files at the top of a repository's
// .github/workflows on its default branch.
//
// Only the top level: GitHub does not run workflows from subdirectories, and
// opening a pull request against a file that never runs would be noise in
// somebody's review queue.
func (c *appClient) ListWorkflows(ctx context.Context, repo string) ([]WorkflowFile, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	_, dir, resp, err := c.asInstallation.Repositories.GetContents(ctx, owner, name, workflowDir, nil)
	if err != nil {
		e := classify(resp, err)
		if errors.Is(e, ErrNotFound) {
			return nil, fmt.Errorf("%s: %w", repo, ErrNoWorkflows)
		}
		return nil, c.decorate("list "+workflowDir+" in "+repo, e)
	}

	var out []WorkflowFile
	for _, entry := range dir {
		if len(out) == maxWorkflowsPerRepo {
			break
		}
		if entry.GetType() != "file" || !isWorkflowFile(entry.GetName()) {
			continue
		}
		if size := entry.GetSize(); size > maxWorkflowBytes {
			continue
		}
		content, err := c.fileContent(ctx, owner, name, entry.GetPath())
		if err != nil {
			return nil, err
		}
		out = append(out, WorkflowFile{Path: entry.GetPath(), SHA: entry.GetSHA(), Content: content})
	}
	return out, nil
}

// fileContent fetches one file's decoded contents.
func (c *appClient) fileContent(ctx context.Context, owner, repo, filePath string) (string, error) {
	file, _, resp, err := c.asInstallation.Repositories.GetContents(ctx, owner, repo, filePath, nil)
	if err != nil {
		return "", c.fail(fmt.Sprintf("read %s in %s/%s", filePath, owner, repo), resp, err)
	}
	if file == nil {
		return "", fmt.Errorf("github: read %s in %s/%s: GitHub returned a directory where a file was expected", filePath, owner, repo)
	}
	decoded, err := file.GetContent()
	if err != nil {
		return "", fmt.Errorf("github: read %s in %s/%s: %w", filePath, owner, repo, err)
	}
	return decoded, nil
}

func isWorkflowFile(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}

// OpenPullRequest creates a branch, commits the files onto it and opens a pull
// request.
//
// It is deliberately the long way round -- a ref, then one commit per file,
// then the pull request -- rather than a tree built by hand. A migration
// touches one or two workflow files in a repository, the contents API is the
// only one that needs no local knowledge of the repository's tree, and a commit
// per file is a history somebody can read.
//
// Nothing here force-pushes or reuses a branch: the branch name carries a
// timestamp, so re-running the wizard opens a second pull request beside the
// first rather than rewriting one somebody may already be reviewing.
func (c *appClient) OpenPullRequest(ctx context.Context, req PullRequestRequest) (*PullRequest, error) {
	owner, name, err := splitRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("github: open pull request on %s: there is nothing to commit", req.Repo)
	}
	head := strings.TrimSpace(req.Head)
	if head == "" {
		return nil, fmt.Errorf("github: open pull request on %s: a branch name is required", req.Repo)
	}

	base := strings.TrimSpace(req.Base)
	if base == "" {
		repo, resp, err := c.asInstallation.Repositories.Get(ctx, owner, name)
		if err != nil {
			return nil, c.fail("read repository "+req.Repo, resp, err)
		}
		base = repo.GetDefaultBranch()
	}

	baseRef, resp, err := c.asInstallation.Git.GetRef(ctx, owner, name, "refs/heads/"+base)
	if err != nil {
		return nil, c.fail(fmt.Sprintf("read branch %s of %s", base, req.Repo), resp, err)
	}
	_, resp, err = c.asInstallation.Git.CreateRef(ctx, owner, name, gh.CreateRef{
		Ref: "refs/heads/" + head,
		SHA: baseRef.GetObject().GetSHA(),
	})
	if err != nil {
		return nil, c.decorate(fmt.Sprintf("create branch %s on %s", head, req.Repo), c.writeError(classify(resp, err)))
	}

	message := strings.TrimSpace(req.CommitMessage)
	if message == "" {
		message = "Move CI onto Zoomies runners"
	}
	for _, f := range req.Files {
		opts := &gh.RepositoryContentFileOptions{
			Message: gh.Ptr(message),
			Content: []byte(f.Content),
			Branch:  gh.Ptr(head),
		}
		if f.SHA != "" {
			opts.SHA = gh.Ptr(f.SHA)
		}
		_, resp, err := c.asInstallation.Repositories.UpdateFile(ctx, owner, name, f.Path, opts)
		if err != nil {
			return nil, c.decorate(fmt.Sprintf("commit %s to %s on %s", f.Path, head, req.Repo), c.writeError(classify(resp, err)))
		}
	}

	pr, resp, err := c.asInstallation.PullRequests.Create(ctx, owner, name, &gh.NewPullRequest{
		Title: gh.Ptr(req.Title),
		Body:  gh.Ptr(req.Body),
		Head:  gh.Ptr(head),
		Base:  gh.Ptr(base),
	})
	if err != nil {
		return nil, c.decorate("open a pull request on "+req.Repo, c.writeError(classify(resp, err)))
	}
	return &PullRequest{Number: pr.GetNumber(), HTMLURL: pr.GetHTMLURL(), Branch: head}, nil
}

// writeError turns a 403 on one of the migration calls into the sentence that
// actually fixes it.
//
// The permission hint the rest of this package uses names the runner
// permissions, which is the right answer everywhere except here: these three
// calls need permissions Zoomies' own App manifest deliberately does not ask
// for, and an operator sent to check "Self-hosted runners" would find it
// correctly set and conclude Zoomies was broken.
func (c *appClient) writeError(e error) error {
	if e == nil || !errors.Is(e, ErrForbidden) {
		return e
	}
	return fmt.Errorf("%w; %s", e, MigrationPermissionHint)
}

// MigrationPermissionHint names what an App needs before it can open a
// migration pull request, in the words GitHub's own settings page uses.
const MigrationPermissionHint = `the migration wizard needs three repository permissions the rest of Zoomies does not: ` +
	`"Contents" (contents) read and write, "Pull requests" (pull_requests) read and write, and ` +
	`"Workflows" (workflows) write, which GitHub requires specifically to change files under .github/workflows. ` +
	`Add them under the App's Permissions & events, then accept the change on the installation`

// MigrationPermissions is the same requirement as data: permission name to the
// level needed. The API compares it against what an installation was granted so
// the wizard can say what is missing before it tries.
var MigrationPermissions = map[string]string{
	"contents":      "write",
	"pull_requests": "write",
	"workflows":     "write",
}

// MissingForMigration lists, in words, the permissions this App still lacks
// before it can open a pull request. It is empty when the App can.
//
// A permission GitHub does not report at all is treated as missing: an older
// GHES that omits a key from the installation's permissions is not a reason to
// try the write and fail halfway through, having already created a branch.
func (a *AppInfo) MissingForMigration() []string {
	var out []string
	for _, perm := range []string{"contents", "pull_requests", "workflows"} {
		want := MigrationPermissions[perm]
		if got := a.Permissions[perm]; got == want {
			continue
		}
		out = append(out, fmt.Sprintf("permission %q (%s) is %s", migrationPermissionLabels[perm], want, describeLevel(a.Permissions[perm])))
	}
	return out
}

// migrationPermissionLabels are the names GitHub's settings page shows, which
// are not the names its API uses.
var migrationPermissionLabels = map[string]string{
	"contents":      "Contents",
	"pull_requests": "Pull requests",
	"workflows":     "Workflows",
}

// BranchName builds the branch a migration commits to.
//
// The timestamp is what keeps two runs of the wizard from colliding: creating a
// ref that already exists fails, and the alternative -- reusing the branch --
// would rewrite a pull request somebody might be reviewing.
func BranchName(now time.Time) string {
	return "zoomies/migrate-runners-" + now.UTC().Format("20060102-150405")
}

func splitRepo(full string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(full), "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("github: %q is not a repository; write it owner/name", full)
	}
	return owner, name, nil
}
