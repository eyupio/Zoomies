package github

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/agentguidance"
)

type KennelGuidanceReader interface {
	KennelGuidanceInventory(context.Context, string, string) (*KennelGuidanceInventory, error)
	KennelWorkflowBlob(context.Context, string, WorkflowFileRef) ([]byte, error)
}

type KennelGuidanceInventory struct {
	Paths       []string
	Files       []WorkflowFileRef
	Skipped     []WorkflowFileRef
	Partial     bool
	TreePartial bool
}

func (c *appClient) KennelGuidanceInventory(ctx context.Context, repo, branch string) (*KennelGuidanceInventory, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("github: repository has no default branch")
	}
	tree, resp, err := c.readClient().Git.GetTree(ctx, owner, name, branch, true)
	if err != nil {
		return nil, c.fail("read agent guidance inventory", resp, err)
	}
	if tree == nil || !workflowSHA.MatchString(tree.GetSHA()) {
		return nil, fmt.Errorf("github: guidance tree has no identity")
	}
	out := &KennelGuidanceInventory{Partial: tree.GetTruncated(), TreePartial: tree.GetTruncated()}
	entries := slices.Clone(tree.Entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	if len(entries) > 10000 {
		entries = entries[:10000]
		out.Partial = true
		out.TreePartial = true
	}
	for _, e := range entries {
		p := e.GetPath()

		regular := e.GetType() == "blob" && (e.GetMode() == "100644" || e.GetMode() == "100755")
		if regular || e.GetType() == "tree" && e.GetMode() == "040000" {
			out.Paths = append(out.Paths, p)
		}
		if !agentguidance.Recognised(p) {
			continue
		}
		ref := WorkflowFileRef{Path: p, SHA: strings.ToLower(e.GetSHA()), Size: e.GetSize()}
		if !agentguidance.SafePath(p) || !regular || !workflowSHA.MatchString(ref.SHA) || ref.Size <= 0 || ref.Size > agentguidance.MaxBytes || len(out.Files) >= agentguidance.MaxFiles {
			out.Skipped = append(out.Skipped, ref)
			out.Partial = true
			continue
		}
		out.Files = append(out.Files, ref)
	}
	return out, nil
}
