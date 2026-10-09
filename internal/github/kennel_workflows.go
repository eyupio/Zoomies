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

// WorkflowFileRef carries only immutable Git object references to the collector.
// Paths are used to select files here and never enter a stored snapshot.
type WorkflowFileRef struct {
	SHA  string
	Size int
}
type KennelWorkflowInventory struct {
	Files   []WorkflowFileRef
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
		if e.GetType() != "blob" || (e.GetMode() != "100644" && e.GetMode() != "100755") || e.GetSize() <= 0 || e.GetSize() > KennelWorkflowBytes || !workflowSHA.MatchString(e.GetSHA()) {
			out.Partial = true
			continue
		}
		if len(out.Files) == KennelWorkflowFiles {
			out.Partial = true
			break
		}
		out.Files = append(out.Files, WorkflowFileRef{SHA: e.GetSHA(), Size: e.GetSize()})
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

// WorkflowInspection is the counts the controller still reads until it takes
// workflow.Facts itself; it is reduced from them here so the tree builds
// while that move is made.
type WorkflowInspection struct {
	NoTimeout, NoConcurrency, FirstPartyUnpinned, OtherUnpinned, PermissionsUnset int
}

var workflowSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

// InspectKennelWorkflow reads one workflow file through the kennel's parser
// and keeps its counts.
func InspectKennelWorkflow(data []byte) (WorkflowInspection, error) {
	facts, err := workflow.Inspect(data)
	if err != nil {
		return WorkflowInspection{}, err
	}
	out := WorkflowInspection{
		NoTimeout: len(facts.NoTimeout), FirstPartyUnpinned: len(facts.FirstPartyUnpinned),
		OtherUnpinned: len(facts.OtherUnpinned), PermissionsUnset: len(facts.PermissionsUnset),
	}
	if facts.NoConcurrency != nil {
		out.NoConcurrency = 1
	}
	return out, nil
}
