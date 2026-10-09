package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/kennel/workflow"
)

const KennelWorkflowBytes = workflow.MaxBytes
const KennelWorkflowFiles = 50

// WorkflowFileRef is one workflow file as the tree lists it. The SHA is the
// immutable object the collector reads; the path is a stranger's text, which
// the collector gates before it keeps it beside the evaluation and which never
// enters a stored finding.
type WorkflowFileRef struct {
	SHA  string
	Size int
	Path string
}

// KennelWorkflowInventory is the default branch's workflow files: those the
// collector may read, and those it may not, over the size or the file limit,
// so a file that is not judged is named rather than silently absent.
type KennelWorkflowInventory struct {
	Files   []WorkflowFileRef
	Skipped []WorkflowFileRef
	Partial bool
}
type KennelWorkflowReader interface {
	KennelWorkflowInventory(context.Context, string, string) (*KennelWorkflowInventory, error)
	KennelWorkflowBlob(context.Context, string, WorkflowFileRef) ([]byte, error)
}

func (c *appClient) KennelWorkflowInventory(ctx context.Context, repo, branch string) (*KennelWorkflowInventory, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("github: repository has no default branch")
	}
	tree, resp, err := c.readClient().Git.GetTree(ctx, owner, name, branch, true)
	if err != nil {
		return nil, c.fail("list default-branch workflow objects", resp, err)
	}
	if tree == nil || tree.GetSHA() == "" {
		return nil, fmt.Errorf("github: workflow tree has no identity")
	}
	out := &KennelWorkflowInventory{Partial: tree.GetTruncated()}
	entries := tree.Entries
	if len(entries) > 10000 {
		entries = entries[:10000]
		out.Partial = true
	}
	// GitHub's tree order is not a promise. Sorting keeps capped reads repeatable.
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	for _, e := range entries {
		p := e.GetPath()
		if path.Dir(p) != ".github/workflows" || (!strings.HasSuffix(p, ".yml") && !strings.HasSuffix(p, ".yaml")) {
			continue
		}
		if e.GetType() != "blob" || (e.GetMode() != "100644" && e.GetMode() != "100755") || !workflowSHA.MatchString(e.GetSHA()) {
			out.Partial = true
			continue
		}
		ref := WorkflowFileRef{SHA: e.GetSHA(), Size: e.GetSize(), Path: p}
		if e.GetSize() <= 0 || e.GetSize() > KennelWorkflowBytes || len(out.Files) == KennelWorkflowFiles {
			out.Partial = true
			out.Skipped = append(out.Skipped, ref)
			continue
		}
		out.Files = append(out.Files, ref)
	}
	return out, nil
}

func (c *appClient) KennelWorkflowBlob(ctx context.Context, repo string, ref WorkflowFileRef) ([]byte, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if !workflowSHA.MatchString(ref.SHA) || ref.Size <= 0 || ref.Size > KennelWorkflowBytes {
		return nil, fmt.Errorf("github: workflow object exceeds its limits")
	}
	blob, resp, err := c.readClient().Git.GetBlob(ctx, owner, name, ref.SHA)
	if err != nil {
		return nil, c.fail("read bounded workflow object", resp, err)
	}
	if blob.GetEncoding() != "base64" || blob.GetSize() != ref.Size || len(blob.GetContent()) > KennelWorkflowBytes*4/3+4096 {
		return nil, fmt.Errorf("github: workflow object has an unexpected size or encoding")
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.GetContent(), "\n", ""))
	if err != nil || len(b) != ref.Size || contextGitBlobSHA(string(b)) != ref.SHA {
		return nil, fmt.Errorf("github: workflow object failed integrity validation")
	}
	return b, nil
}

var workflowSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
