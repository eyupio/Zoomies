package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const KennelWorkflowBytes = 256 * 1024
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

// WorkflowInspection has no repository text, only the counts the evaluator needs.
type WorkflowInspection struct {
	NoTimeout, NoConcurrency, FirstPartyUnpinned, OtherUnpinned, PermissionsUnset int
}

var workflowSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var workflowDigest = regexp.MustCompile(`@sha256:[a-fA-F0-9]{64}$`)

// InspectKennelWorkflow deliberately rejects aliases and merge keys. Resolving
// arbitrary YAML graphs would spend unbounded work and obscure which declaration
// supplied a value. The caller records incomplete coverage for those files.
func InspectKennelWorkflow(data []byte) (WorkflowInspection, error) {
	var out WorkflowInspection
	bad := func() (WorkflowInspection, error) {
		return WorkflowInspection{}, fmt.Errorf("workflow is malformed or exceeds the supported YAML shape")
	}
	if len(data) == 0 || len(data) > KennelWorkflowBytes {
		return bad()
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 {
		return bad()
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return bad()
	}
	count := 0
	var bounded func(*yaml.Node, int) bool
	bounded = func(n *yaml.Node, depth int) bool {
		count++
		if count > 20000 || depth > 50 || n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!merge" {
			return false
		}
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			if len(n.Content)%2 != 0 {
				return false
			}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || keys[k.Value] {
					return false
				}
				keys[k.Value] = true
			}
		}
		for _, ch := range n.Content {
			if !bounded(ch, depth+1) {
				return false
			}
		}
		return true
	}
	if !bounded(&doc, 0) || doc.Content[0].Kind != yaml.MappingNode {
		return bad()
	}
	root := doc.Content[0]
	jobs := workflowField(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode || len(jobs.Content) == 0 {
		return bad()
	}
	if !workflowPermissions(workflowField(root, "permissions")) {
		return bad()
	}
	onlyPR := workflowOnlyPR(workflowField(root, "on"))
	allJobConcurrency := true
	for i := 1; i < len(jobs.Content); i += 2 {
		job := jobs.Content[i]
		if job.Kind != yaml.MappingNode {
			return bad()
		}
		if !workflowPermissions(workflowField(job, "permissions")) {
			return bad()
		}
		if !workflowDeclared(workflowField(root, "permissions")) && !workflowDeclared(workflowField(job, "permissions")) {
			out.PermissionsUnset++
		}
		if !workflowCancels(workflowField(job, "concurrency")) {
			allJobConcurrency = false
		}
		if uses := workflowField(job, "uses"); uses != nil {
			if uses.Kind != yaml.ScalarNode {
				return bad()
			}
			if !workflowInspectUses(uses.Value, &out) {
				return bad()
			}
			continue
		}
		steps := workflowField(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode || !workflowDeclared(workflowField(job, "runs-on")) {
			return bad()
		}
		if !workflowDeclared(workflowField(job, "timeout-minutes")) {
			out.NoTimeout++
		}
		for _, step := range steps.Content {
			if step.Kind != yaml.MappingNode {
				return bad()
			}
			if uses := workflowField(step, "uses"); uses != nil {
				if uses.Kind != yaml.ScalarNode {
					return bad()
				}
				if !workflowInspectUses(uses.Value, &out) {
					return bad()
				}
			}
		}
	}
	if onlyPR && !workflowCancels(workflowField(root, "concurrency")) && !allJobConcurrency {
		out.NoConcurrency = 1
	}
	return out, nil
}

func workflowField(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func workflowDeclared(n *yaml.Node) bool {
	return n != nil && n.Tag != "!!null" && (n.Kind != yaml.ScalarNode || strings.TrimSpace(n.Value) != "")
}
func workflowPermissions(n *yaml.Node) bool {
	if !workflowDeclared(n) {
		return true
	}
	if n.Kind == yaml.MappingNode {
		for i := 1; i < len(n.Content); i += 2 {
			v := n.Content[i]
			if v.Kind != yaml.ScalarNode || (v.Value != "read" && v.Value != "write" && v.Value != "none") {
				return false
			}
		}
		return true
	}
	return n.Kind == yaml.ScalarNode && (n.Value == "read-all" || n.Value == "write-all")
}
func workflowOnlyPR(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	events := []string{}
	switch n.Kind {
	case yaml.ScalarNode:
		events = append(events, n.Value)
	case yaml.SequenceNode:
		for _, e := range n.Content {
			if e.Kind != yaml.ScalarNode {
				return false
			}
			events = append(events, e.Value)
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			events = append(events, n.Content[i].Value)
		}
	default:
		return false
	}
	if len(events) == 0 {
		return false
	}
	for _, e := range events {
		if e != "pull_request" && e != "pull_request_target" {
			return false
		}
	}
	return true
}
func workflowCancels(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.MappingNode || !workflowDeclared(workflowField(n, "group")) {
		return false
	}
	cancel := workflowField(n, "cancel-in-progress")
	// Expressions cannot be evaluated statically; a declared expression is left
	// to its author rather than guessed to be false.
	return workflowDeclared(cancel) && (cancel.Value == "true" || strings.Contains(cancel.Value, "${{"))
}
func workflowInspectUses(uses string, out *WorkflowInspection) bool {
	if strings.Contains(uses, "${{") {
		return false
	}
	if strings.HasPrefix(uses, "./") {
		return true
	}
	if strings.HasPrefix(uses, "docker://") {
		if !workflowDigest.MatchString(uses) {
			out.OtherUnpinned++
		}
		return true
	}
	_, ref, ok := strings.Cut(uses, "@")
	if ok && workflowSHA.MatchString(ref) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(uses), "actions/") || strings.HasPrefix(strings.ToLower(uses), "github/") {
		out.FirstPartyUnpinned++
	} else {
		out.OtherUnpinned++
	}
	return true
}
