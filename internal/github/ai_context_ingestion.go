package github

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/eyupio/zoomies/internal/aicontext"
	gh "github.com/google/go-github/v88/github"
)

// ContextIngestionClient is read-only and does not widen runner permissions.
type ContextIngestionClient interface {
	ReadContextPublication(context.Context, string, string) (*ContextPublication, error)
	ContextSetupMerged(context.Context, string, int, string) (bool, error)
	VerifyContextSnapshot(ctx context.Context, repo, branch, commit string, snapshot *aicontext.Snapshot) error
}

// ErrContextMismatch is a published snapshot that no longer describes the
// trusted branch: the branch moved on, or the snapshot was made for another
// commit. It is the ordinary state between a push and the workflow finishing,
// so a caller can tell it apart from a publication that is genuinely broken.
var ErrContextMismatch = errors.New("github: published context does not match the trusted branch")

// ContextWorkflowDispatcher starts the managed workflow by hand. It is a
// separate interface because it is the one AI Context call that writes to
// GitHub Actions, and so needs the Actions write permission the rest of this
// file never asks for.
type ContextWorkflowDispatcher interface {
	DispatchContextWorkflow(ctx context.Context, repo, branch string) error
}

type ContextPublication struct {
	Source       *ContextSetupSource
	OutputCommit string
	Snapshot     *aicontext.Snapshot
}

type ContextSetupStatusClient interface {
	ContextSetupStatus(context.Context, string, int, string) (string, error)
}

func (c *appClient) ContextSetupStatus(ctx context.Context, repo string, number int, branch string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	pr, resp, err := c.asInstallation.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return "", c.fail("check context setup outcome", resp, err)
	}
	if pr.GetBase().GetRef() != branch {
		return "", ErrSetupConflict
	}
	if pr.GetMerged() && pr.GetMergeCommitSHA() != "" {
		return "merged", nil
	}
	return pr.GetState(), nil
}

// DispatchContextWorkflow runs the managed workflow on the trusted branch. The
// caller has already checked that the workflow on that branch is the reviewed
// one; dispatching an edited file would run whatever it says.
func (c *appClient) DispatchContextWorkflow(ctx context.Context, repo, branch string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, resp, err := c.asInstallation.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner, name, path.Base(aicontext.WorkflowPath), gh.CreateWorkflowDispatchEventRequest{Ref: branch})
	// GitHub answers 204, and 200 once it returns run details; either is a start.
	if err == nil || (resp != nil && (resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK)) {
		return nil
	}
	e := classify(resp, err)
	if errors.Is(e, ErrForbidden) {
		return fmt.Errorf("github: run context workflow: %w; check the App installation on %s: it needs \"Actions\" (actions) read and write, and changed permissions must be accepted on the installation", e, c.target)
	}
	return errorf("run context workflow", e)
}

func (c *appClient) ContextSetupMerged(ctx context.Context, repo string, number int, branch string) (bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	pr, resp, err := c.asInstallation.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return false, c.fail("verify context setup merge", resp, err)
	}
	return pr.GetMerged() && pr.GetBase().GetRef() == branch && pr.GetMergeCommitSHA() != "", nil
}

func (c *appClient) ReadContextPublication(ctx context.Context, repo, branch string) (*ContextPublication, error) {
	source, err := c.ReadContextSetup(ctx, repo, branch)
	if err != nil {
		return nil, err
	}
	owner, name, _ := splitRepo(repo)
	ref, resp, err := c.asInstallation.Git.GetRef(ctx, owner, name, "heads/"+aicontext.OutputBranch)
	if err != nil {
		return nil, c.fail("read context output branch", resp, err)
	}
	commit, resp, err := c.asInstallation.Git.GetCommit(ctx, owner, name, ref.GetObject().GetSHA())
	if err != nil {
		return nil, c.fail("read context output commit", resp, err)
	}
	tree, resp, err := c.asInstallation.Git.GetTree(ctx, owner, name, commit.GetTree().GetSHA(), true)
	if err != nil {
		return nil, c.fail("read context output tree", resp, err)
	}
	if tree.GetTruncated() || len(tree.Entries) > 8 {
		return nil, fmt.Errorf("context output tree exceeds its bounded managed layout")
	}
	blobs := map[string][]byte{}
	for _, entry := range tree.Entries {
		if entry.GetType() == "tree" && (entry.GetPath() == ".zoomies" || entry.GetPath() == aicontext.OutputDirectory) {
			continue
		}
		p := strings.TrimPrefix(entry.GetPath(), aicontext.OutputDirectory+"/")
		if entry.GetMode() != "100644" || entry.GetType() != "blob" || p == entry.GetPath() || (p != "snapshot.json" && p != "manifest.json" && p != "NOTICE.md") || blobs[p] != nil {
			return nil, fmt.Errorf("context output contains an unowned or non-regular file")
		}
		limit := aicontext.MaxSnapshotBytes
		if p != "snapshot.json" {
			limit = 16384
		}
		if entry.GetSize() < 0 || entry.GetSize() > limit {
			return nil, fmt.Errorf("context output exceeds its managed file limit")
		}
		b, err := c.contextBlob(ctx, owner, name, entry.GetSHA(), limit)
		if err != nil {
			return nil, err
		}
		blobs[p] = b
	}
	if len(blobs) != 3 || string(blobs["NOTICE.md"]) != "Managed by Zoomies AI Context. Generated by Repomix. Do not edit.\n" {
		return nil, fmt.Errorf("context output is incomplete or lacks its managed notice")
	}
	var publication struct {
		SnapshotSHA string `json:"snapshot_sha256"`
	}
	if json.Unmarshal(blobs["manifest.json"], &publication) != nil || publication.SnapshotSHA != aicontext.Hash(blobs["snapshot.json"]) {
		return nil, fmt.Errorf("context publication fails integrity validation")
	}
	snapshot, err := aicontext.Decode(bytes.NewReader(blobs["snapshot.json"]))
	if err != nil {
		return nil, fmt.Errorf("context snapshot fails validation")
	}
	if err := c.verifyContextSnapshot(ctx, owner, name, branch, source.Commit, snapshot); err != nil {
		return nil, err
	}
	return &ContextPublication{source, ref.GetObject().GetSHA(), snapshot}, nil
}

// VerifyContextSnapshot checks a snapshot that did not come from a branch --
// a Zoomies-only upload -- against the repository itself: every file must be
// the regular blob at that path in the commit, and the commit must still be
// the head of the trusted branch.
func (c *appClient) VerifyContextSnapshot(ctx context.Context, repo, branch, commit string, snapshot *aicontext.Snapshot) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	return c.verifyContextSnapshot(ctx, owner, name, branch, commit, snapshot)
}

func (c *appClient) verifyContextSnapshot(ctx context.Context, owner, name, branch, commit string, snapshot *aicontext.Snapshot) error {
	// File digests alone do not prove source authenticity. Match original Git
	// blob identities against the pinned trusted source tree before admitting it.
	sourceCommit, resp, err := c.asInstallation.Git.GetCommit(ctx, owner, name, commit)
	if err != nil {
		return c.fail("read trusted context commit", resp, err)
	}
	sourceTree, resp, err := c.asInstallation.Git.GetTree(ctx, owner, name, sourceCommit.GetTree().GetSHA(), true)
	if err != nil {
		return c.fail("read trusted context tree", resp, err)
	}
	if sourceTree.GetTruncated() || len(sourceTree.Entries) > 100000 {
		return fmt.Errorf("trusted context tree exceeds its validation limit")
	}
	entries := map[string]*gh.TreeEntry{}
	for _, e := range sourceTree.Entries {
		entries[e.GetPath()] = e
	}
	for _, file := range snapshot.Files {
		e := entries[file.Path]
		if e == nil || e.GetType() != "blob" || (e.GetMode() != "100644" && e.GetMode() != "100755") || e.GetSHA() != contextGitBlobSHA(file.Content) {
			return fmt.Errorf("%w: context content does not match regular files in the trusted commit", ErrContextMismatch)
		}
	}
	latest, resp, err := c.asInstallation.Git.GetRef(ctx, owner, name, "heads/"+branch)
	if err != nil {
		return c.fail("recheck trusted context branch", resp, err)
	}
	if latest.GetObject().GetSHA() != commit {
		return fmt.Errorf("%w: context source changed during validation; retry its latest generation", ErrContextMismatch)
	}
	return nil
}

func contextGitBlobSHA(content string) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}
func (c *appClient) contextBlob(ctx context.Context, owner, name, sha string, limit int) ([]byte, error) {
	blob, resp, err := c.asInstallation.Git.GetBlob(ctx, owner, name, sha)
	if err != nil {
		return nil, c.fail("read bounded context blob", resp, err)
	}
	if blob.GetSize() > limit || blob.GetEncoding() != "base64" || len(blob.GetContent()) > (limit*4/3)+4096 {
		return nil, fmt.Errorf("context blob exceeds its size or encoding limit")
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.GetContent(), "\n", ""))
	if err != nil || len(b) > limit || len(b) != blob.GetSize() || contextGitBlobSHA(string(b)) != sha {
		return nil, fmt.Errorf("context blob fails decoding")
	}
	return b, nil
}
