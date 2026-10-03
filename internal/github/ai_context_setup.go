package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/eyupio/zoomies/internal/aicontext"
	gh "github.com/google/go-github/v88/github"
)

var ErrSetupConflict = errors.New("github: repository setup changed; review it again")

// ContextSetupClient keeps source setup apart from the runner-only interface.
type ContextSetupClient interface {
	ReadContextSetup(context.Context, string, string) (*ContextSetupSource, error)
	OpenContextSetup(context.Context, ContextSetupRequest) (*PullRequest, error)
}

type ContextSetupSource struct {
	Repository Repository
	Commit     string
	Files      []aicontext.SetupFile
}

type ContextSetupRequest struct {
	Repo       string
	Base       string
	BaseCommit string
	Head       string
	PlanHash   string
	Files      []aicontext.SetupChange
}

func (c *appClient) ReadContextSetup(ctx context.Context, repo, branch string) (*ContextSetupSource, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	r, resp, err := c.asInstallation.Repositories.Get(ctx, owner, name)
	if err != nil {
		return nil, c.fail("verify context repository", resp, err)
	}
	ref, resp, err := c.asInstallation.Git.GetRef(ctx, owner, name, "heads/"+branch)
	if err != nil {
		return nil, c.fail("pin context source branch", resp, err)
	}
	commit := ref.GetObject().GetSHA()
	out := &ContextSetupSource{Repository: repositoryOf(r), Commit: commit, Files: []aicontext.SetupFile{}}
	gitCommit, resp, err := c.asInstallation.Git.GetCommit(ctx, owner, name, commit)
	if err != nil {
		return nil, c.fail("read context source commit", resp, err)
	}
	trees := map[string]*gh.Tree{}
	fileAt := func(p string) (*gh.TreeEntry, error) {
		treeSHA := gitCommit.GetTree().GetSHA()
		parts := strings.Split(p, "/")
		for i, part := range parts {
			tree := trees[treeSHA]
			if tree == nil {
				var resp *gh.Response
				var err error
				tree, resp, err = c.asInstallation.Git.GetTree(ctx, owner, name, treeSHA, false)
				if err != nil {
					return nil, c.fail("verify setup file modes", resp, err)
				}
				if tree.GetTruncated() || len(tree.Entries) > 10000 {
					return nil, fmt.Errorf("%w: repository tree exceeds setup preview limits", ErrSetupConflict)
				}
				trees[treeSHA] = tree
			}
			var entry *gh.TreeEntry
			for _, e := range tree.Entries {
				if e.GetPath() == part {
					entry = e
					break
				}
			}
			if entry == nil {
				return nil, nil
			}
			if i == len(parts)-1 {
				if entry.GetType() != "blob" || entry.GetMode() != "100644" && entry.GetMode() != "100755" {
					return nil, fmt.Errorf("%w: %s must be a regular file", ErrSetupConflict, p)
				}
				return entry, nil
			}
			if entry.GetType() != "tree" || entry.GetMode() != "040000" {
				return nil, fmt.Errorf("%w: %s crosses a symlink or non-directory", ErrSetupConflict, p)
			}
			treeSHA = entry.GetSHA()
		}
		return nil, nil
	}
	read := func(p string) error {
		entry, err := fileAt(p)
		if err != nil {
			return err
		}
		if entry == nil {
			return nil
		}
		if entry.GetSize() > 512<<10 {
			return fmt.Errorf("%w: %s exceeds setup preview limits", ErrSetupConflict, p)
		}
		blob, resp, err := c.asInstallation.Git.GetBlob(ctx, owner, name, entry.GetSHA())
		if err != nil {
			return c.fail("read pinned setup file", resp, err)
		}
		if blob.GetEncoding() != "base64" || blob.GetSize() > 512<<10 || len(blob.GetContent()) > 1<<20 {
			return fmt.Errorf("%w: %s has unsupported content", ErrSetupConflict, p)
		}
		content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.GetContent(), "\n", ""))
		if err != nil {
			return err
		}
		out.Files = append(out.Files, aicontext.SetupFile{Mode: entry.GetMode(), Path: p, SHA: entry.GetSHA(), Content: string(content)})
		return nil
	}
	for _, p := range []string{aicontext.ConfigPath, "CLAUDE.md", "AGENTS.md", aicontext.WorkflowPath, aicontext.GeneratorPackagePath, aicontext.GeneratorLockPath} {
		if err := read(p); err != nil {
			return nil, err
		}
	}
	readme, resp, err := c.asInstallation.Repositories.GetReadme(ctx, owner, name, &gh.RepositoryContentGetOptions{Ref: commit})
	if err != nil && !errors.Is(classify(resp, err), ErrNotFound) {
		return nil, c.fail("locate pinned README", resp, err)
	}
	if err == nil && (strings.EqualFold(path.Base(readme.GetPath()), "README.md") || strings.EqualFold(path.Base(readme.GetPath()), "README.markdown")) && !strings.Contains(readme.GetPath(), "/") {
		if err := read(readme.GetPath()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *appClient) OpenContextSetup(ctx context.Context, req ContextSetupRequest) (*PullRequest, error) {
	owner, name, err := splitRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	if len(req.Files) == 0 || !aicontext.ValidBranch(req.Head) || !aicontext.ValidBranch(req.Base) || len(req.PlanHash) != 64 {
		return nil, fmt.Errorf("%w: setup has no valid reviewed changes", ErrSetupConflict)
	}
	// The exact base is checked before creating Git objects and again before
	// publishing a ref. User edits never get rebased or overwritten silently.
	ref, resp, err := c.asInstallation.Git.GetRef(ctx, owner, name, "heads/"+req.Base)
	if err != nil {
		return nil, c.fail("verify setup base", resp, err)
	}
	existing, existingResp, existingErr := c.asInstallation.Git.GetRef(ctx, owner, name, "heads/"+req.Head)
	if existingErr != nil && !errors.Is(classify(existingResp, existingErr), ErrNotFound) {
		return nil, c.fail("reconcile setup branch", existingResp, existingErr)
	}
	if existingErr != nil && ref.GetObject().GetSHA() != req.BaseCommit {
		return nil, ErrSetupConflict
	}
	base, resp, err := c.asInstallation.Git.GetCommit(ctx, owner, name, req.BaseCommit)
	if err != nil {
		return nil, c.fail("read setup base", resp, err)
	}
	entries := make([]*gh.TreeEntry, 0, len(req.Files))
	for _, f := range req.Files {
		entries = append(entries, &gh.TreeEntry{Path: gh.Ptr(f.Path), Mode: gh.Ptr(contextSetupFileMode(f.Mode)), Type: gh.Ptr("blob"), Content: gh.Ptr(f.Content)})
	}
	tree, resp, err := c.asInstallation.Git.CreateTree(ctx, owner, name, base.GetTree().GetSHA(), entries)
	if err != nil {
		return nil, c.migrationError("prepare complete setup tree", classify(resp, err))
	}
	message := "Enable Zoomies AI Context\n\nZoomies setup: " + req.PlanHash

	if existingErr == nil {
		previous, resp, err := c.asInstallation.Git.GetCommit(ctx, owner, name, existing.GetObject().GetSHA())
		if err != nil {
			return nil, c.fail("verify existing setup branch", resp, err)
		}
		if previous.GetTree().GetSHA() != tree.GetSHA() || previous.GetMessage() != message || len(previous.Parents) != 1 || previous.Parents[0].GetSHA() != req.BaseCommit {
			return nil, fmt.Errorf("%w: existing setup branch was edited; it has been preserved", ErrSetupConflict)
		}
	} else {
		commit, resp, err := c.asInstallation.Git.CreateCommit(ctx, owner, name, gh.Commit{Message: gh.Ptr(message), Tree: tree, Parents: []*gh.Commit{{SHA: gh.Ptr(req.BaseCommit)}}}, nil)
		if err != nil {
			return nil, c.migrationError("prepare atomic setup commit", classify(resp, err))
		}
		ref, resp, err = c.asInstallation.Git.GetRef(ctx, owner, name, "heads/"+req.Base)
		if err != nil {
			return nil, c.fail("recheck setup base", resp, err)
		}
		if ref.GetObject().GetSHA() != req.BaseCommit {
			return nil, ErrSetupConflict
		}
		_, resp, err = c.asInstallation.Git.CreateRef(ctx, owner, name, gh.CreateRef{Ref: "refs/heads/" + req.Head, SHA: commit.GetSHA()})
		if err != nil {
			return nil, c.migrationError("publish complete setup branch", classify(resp, err))
		}
	}
	body := "Prepare this repository for AI coding assistants with Zoomies-managed Repomix context.\n\nReview the workflow, exclusions, generated source copy and instruction/badge sections. Merge runs generation; it does not grant assistant connections source access. The workflow uses a read-only generation job and a separate write-only publication job.\n\n<!-- zoomies-ai-context:setup " + req.PlanHash + " -->"
	// Closed PRs are included: a retry must not reopen a deliberately declined
	// proposal or produce a duplicate after an uncertain successful response.
	pulls, resp, err := c.asInstallation.PullRequests.List(ctx, owner, name, &gh.PullRequestListOptions{State: "all", Head: owner + ":" + req.Head, Base: req.Base, ListOptions: gh.ListOptions{PerPage: 100}})
	if err != nil {
		return nil, c.fail("reconcile setup pull request", resp, err)
	}
	if resp != nil && resp.NextPage != 0 {
		return nil, fmt.Errorf("%w: too many setup pull requests to reconcile safely", ErrSetupConflict)
	}
	if len(pulls) > 0 {
		if len(pulls) != 1 || pulls[0].GetBody() != body || pulls[0].GetTitle() != "Enable Zoomies AI Context" {
			return nil, fmt.Errorf("%w: existing pull request differs from the reviewed setup", ErrSetupConflict)
		}
		pr := pulls[0]
		if pr.GetState() != "open" {
			return nil, fmt.Errorf("%w: the previous setup PR is closed; check its outcome before another setup", ErrSetupConflict)
		}
		return &PullRequest{Number: pr.GetNumber(), HTMLURL: pr.GetHTMLURL(), Branch: req.Head}, nil
	}
	pr, resp, err := c.asInstallation.PullRequests.Create(ctx, owner, name, &gh.NewPullRequest{Title: gh.Ptr("Enable Zoomies AI Context"), Body: gh.Ptr(body), Head: gh.Ptr(req.Head), Base: gh.Ptr(req.Base)})
	if err != nil {
		// Keep the complete branch even on a timeout: the request may have opened
		// a PR. The same durable head is reconciled on the next explicit retry.
		if resp == nil || resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return nil, fmt.Errorf("%w; setup branch retained for safe retry", err)
		}
		return nil, c.migrationError("open setup pull request", classify(resp, err))
	}
	return &PullRequest{Number: pr.GetNumber(), HTMLURL: pr.GetHTMLURL(), Branch: req.Head}, nil
}

func contextSetupFileMode(mode string) string {
	if mode == "100755" {
		return mode
	}
	return "100644"
}
