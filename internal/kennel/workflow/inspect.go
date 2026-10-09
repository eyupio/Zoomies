// Package workflow reads a GitHub Actions workflow file and says, by position,
// what Kennel Club's checks want to know about it: which job has no timeout,
// which reference is not pinned, which step puts a secret on a command line.
//
// It is the one place repository text is parsed, and it keeps none of it: a
// fact is a job index and a line, so a finding can say where without saying
// what, and the raw file is discarded when Inspect returns. The parse is
// bounded and conservative, because the file is written by whoever can open a
// pull request against a served repository: a shape it does not understand is
// refused as a whole, never judged in part, and an expression it cannot read
// is unknown rather than wrong.
package workflow

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// MaxBytes is the largest file Inspect reads. The inventory skips a larger
// one, and the collector reports it as unreadable.
const MaxBytes = 256 * 1024

const (
	maxNodes = 20000
	maxDepth = 50
)

// Location says where a fact is: the job it belongs to, by its position in
// the file's `jobs` mapping, and the line of the thing itself. JobIndex is -1
// for a fact about the workflow as a whole. A job's line is the line of its
// key; a step's is the line of its first key; the workflow's is its `on` key.
type Location struct {
	JobIndex int `json:"job_index"`
	Line     int `json:"line"`
}

// RunsOn is one job's literal runs-on labels. They are a workflow author's
// text, so they are kept in memory for the controller's membership test and
// never marshalled; a job whose runs-on holds an expression or a runner
// group is left out, because it cannot be judged.
type RunsOn struct {
	Location
	Labels []string `json:"-"`
}

// Facts is what one workflow file says, as positions and counts.
type Facts struct {
	// Jobs is how many job declarations the file holds, executable and
	// reusable alike.
	Jobs int `json:"jobs"`
	// Pinned is how many external references are pinned to a commit or an
	// image digest, for the check that asks whether anything moves the pins.
	Pinned int `json:"pinned"`
	// NoTimeout is each executable job without a timeout-minutes.
	NoTimeout []Location `json:"no_timeout,omitempty"`
	// NoConcurrency is the `on` key of a pull-request-only workflow that
	// cancels nothing, at workflow level or on every job; nil otherwise.
	NoConcurrency *Location `json:"no_concurrency,omitempty"`
	// FirstPartyUnpinned and OtherUnpinned are each `uses` of an external
	// action, reusable workflow or Docker image that is not a commit or a
	// digest, by who publishes it.
	FirstPartyUnpinned []Location `json:"first_party_unpinned,omitempty"`
	OtherUnpinned      []Location `json:"other_unpinned,omitempty"`
	// PermissionsUnset is each job with no permissions block of its own or
	// its workflow's.
	PermissionsUnset []Location `json:"permissions_unset,omitempty"`
	// TargetCheckoutPRHead is each checkout step under pull_request_target
	// that takes the pull request's head.
	TargetCheckoutPRHead []Location `json:"target_checkout_pr_head,omitempty"`
	// SecretOnCommandLine is each step whose run line or args interpolates a
	// secret.
	SecretOnCommandLine []Location `json:"secret_on_command_line,omitempty"`
	// RunsOn is each job's literal labels, in memory only.
	RunsOn []RunsOn `json:"-"`
}

var (
	errUnsupported = errors.New("workflow is malformed or exceeds the supported YAML shape")
	commitSHA      = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	imageDigest    = regexp.MustCompile(`@sha256:[a-fA-F0-9]{64}$`)
)

// Inspect reads one workflow file. It refuses aliases, anchors and merge keys
// on purpose: resolving an arbitrary graph is unbounded work, and it obscures
// which declaration supplied a value. The caller records an unreadable file.
func Inspect(data []byte) (Facts, error) {
	var out Facts
	if len(data) == 0 || len(data) > MaxBytes || !utf8.Valid(data) {
		return Facts{}, errUnsupported
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&doc) != nil || len(doc.Content) != 1 {
		return Facts{}, errUnsupported
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return Facts{}, errUnsupported
	}
	if !bounded(&doc) || doc.Content[0].Kind != yaml.MappingNode {
		return Facts{}, errUnsupported
	}
	root := doc.Content[0]
	jobs := field(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode || len(jobs.Content) == 0 {
		return Facts{}, errUnsupported
	}
	if !validPermissions(field(root, "permissions")) {
		return Facts{}, errUnsupported
	}
	onKey, onValue := keyAndValue(root, "on")
	events, ok := eventsOf(onValue)
	if !ok {
		return Facts{}, errUnsupported
	}
	onlyPR := len(events) > 0 && every(events, func(e string) bool { return e == "pull_request" || e == "pull_request_target" })
	target := contains(events, "pull_request_target")
	allJobsCancel := true
	for i := 0; i < len(jobs.Content); i += 2 {
		index := i / 2
		job := jobs.Content[i+1]
		at := Location{JobIndex: index, Line: jobs.Content[i].Line}
		if job.Kind != yaml.MappingNode {
			return Facts{}, errUnsupported
		}
		out.Jobs++
		if !validPermissions(field(job, "permissions")) {
			return Facts{}, errUnsupported
		}
		if !declared(field(root, "permissions")) && !declared(field(job, "permissions")) {
			out.PermissionsUnset = append(out.PermissionsUnset, at)
		}
		if !cancels(field(job, "concurrency")) {
			allJobsCancel = false
		}
		if usesKey, uses := keyAndValue(job, "uses"); uses != nil {
			// A reusable job's reference is its own line, as a step's is.
			if uses.Kind != yaml.ScalarNode || !out.reference(uses.Value, Location{JobIndex: index, Line: usesKey.Line}) {
				return Facts{}, errUnsupported
			}
			continue
		}
		steps := field(job, "steps")
		runsOn := field(job, "runs-on")
		if steps == nil || steps.Kind != yaml.SequenceNode || !declared(runsOn) {
			return Facts{}, errUnsupported
		}
		if !declared(field(job, "timeout-minutes")) {
			out.NoTimeout = append(out.NoTimeout, at)
		}
		if labels, ok := literalLabels(runsOn); ok {
			out.RunsOn = append(out.RunsOn, RunsOn{Location: Location{JobIndex: index, Line: runsOnLine(job)}, Labels: labels})
		}
		for _, step := range steps.Content {
			if step.Kind != yaml.MappingNode || len(step.Content) == 0 {
				return Facts{}, errUnsupported
			}
			stepAt := Location{JobIndex: index, Line: step.Content[0].Line}
			uses := field(step, "uses")
			if uses != nil {
				if uses.Kind != yaml.ScalarNode || !out.reference(uses.Value, stepAt) {
					return Facts{}, errUnsupported
				}
				if target && isCheckout(uses.Value) && takesPullRequestHead(field(step, "with")) {
					out.TargetCheckoutPRHead = append(out.TargetCheckoutPRHead, stepAt)
				}
			}
			if secretOnCommandLine(field(step, "run")) || secretOnCommandLine(field(field(step, "with"), "args")) {
				out.SecretOnCommandLine = append(out.SecretOnCommandLine, stepAt)
			}
		}
	}
	if onlyPR && !cancels(field(root, "concurrency")) && !allJobsCancel {
		out.NoConcurrency = &Location{JobIndex: -1, Line: onKey.Line}
	}
	return out, nil
}

// bounded walks the document once, refusing what the walker will not read:
// too many nodes, too deep, an alias, an anchor, a merge key, a duplicate key.
func bounded(doc *yaml.Node) bool {
	count := 0
	var walk func(*yaml.Node, int) bool
	walk = func(n *yaml.Node, depth int) bool {
		count++
		if count > maxNodes || depth > maxDepth || n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!merge" {
			return false
		}
		if n.Kind == yaml.MappingNode {
			if len(n.Content)%2 != 0 {
				return false
			}
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || keys[k.Value] {
					return false
				}
				keys[k.Value] = true
			}
		}
		for _, ch := range n.Content {
			if !walk(ch, depth+1) {
				return false
			}
		}
		return true
	}
	return walk(doc, 0)
}

// reference classes one `uses`: pinned, unpinned by publisher, or local. An
// expression in it is a shape the walker refuses, because the reference could
// be anything.
func (f *Facts) reference(uses string, at Location) bool {
	if strings.Contains(uses, "${{") {
		return false
	}
	if strings.HasPrefix(uses, "./") {
		return true
	}
	if strings.HasPrefix(uses, "docker://") {
		if imageDigest.MatchString(uses) {
			f.Pinned++
		} else {
			f.OtherUnpinned = append(f.OtherUnpinned, at)
		}
		return true
	}
	if _, ref, ok := strings.Cut(uses, "@"); ok && commitSHA.MatchString(ref) {
		f.Pinned++
		return true
	}
	lower := strings.ToLower(uses)
	if strings.HasPrefix(lower, "actions/") || strings.HasPrefix(lower, "github/") {
		f.FirstPartyUnpinned = append(f.FirstPartyUnpinned, at)
	} else {
		f.OtherUnpinned = append(f.OtherUnpinned, at)
	}
	return true
}

func field(n *yaml.Node, key string) *yaml.Node {
	_, v := keyAndValue(n, key)
	return v
}

func keyAndValue(n *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}

func runsOnLine(job *yaml.Node) int {
	k, _ := keyAndValue(job, "runs-on")
	if k == nil {
		return 0
	}
	return k.Line
}

func declared(n *yaml.Node) bool {
	return n != nil && n.Tag != "!!null" && (n.Kind != yaml.ScalarNode || strings.TrimSpace(n.Value) != "")
}

// validPermissions is whether a permissions block has a shape the walker
// understands: absent, a mapping of read/write/none, or read-all/write-all.
func validPermissions(n *yaml.Node) bool {
	if !declared(n) {
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

// eventsOf lists the events an `on` names; ok is false for a shape it does
// not understand, and an absent `on` is no events.
func eventsOf(n *yaml.Node) ([]string, bool) {
	if n == nil {
		return nil, true
	}
	var events []string
	switch n.Kind {
	case yaml.ScalarNode:
		events = append(events, n.Value)
	case yaml.SequenceNode:
		for _, e := range n.Content {
			if e.Kind != yaml.ScalarNode {
				return nil, false
			}
			events = append(events, e.Value)
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			events = append(events, n.Content[i].Value)
		}
	default:
		return nil, false
	}
	return events, true
}

func cancels(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.MappingNode || !declared(field(n, "group")) {
		return false
	}
	cancel := field(n, "cancel-in-progress")
	// An expression cannot be evaluated statically; a declared one is left to
	// its author rather than guessed to be false.
	return declared(cancel) && (cancel.Value == "true" || strings.Contains(cancel.Value, "${{"))
}

// literalLabels is a job's runs-on as labels, when every one is literal.
func literalLabels(n *yaml.Node) ([]string, bool) {
	switch n.Kind {
	case yaml.ScalarNode:
		if strings.Contains(n.Value, "${{") {
			return nil, false
		}
		return []string{n.Value}, true
	case yaml.SequenceNode:
		var labels []string
		for _, e := range n.Content {
			if e.Kind != yaml.ScalarNode || strings.Contains(e.Value, "${{") {
				return nil, false
			}
			labels = append(labels, e.Value)
		}
		return labels, len(labels) > 0
	}
	return nil, false
}

func isCheckout(uses string) bool {
	return strings.HasPrefix(strings.ToLower(uses), "actions/checkout@")
}

// takesPullRequestHead is whether a checkout's `ref` names the pull request's
// head, which under pull_request_target is the stranger's code checked out
// beside the repository's token.
func takesPullRequestHead(with *yaml.Node) bool {
	ref := field(with, "ref")
	if ref == nil || ref.Kind != yaml.ScalarNode {
		return false
	}
	return strings.Contains(ref.Value, "github.event.pull_request.head") || strings.Contains(ref.Value, "github.head_ref")
}

func secretOnCommandLine(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${{ secrets.")
}

func every(xs []string, f func(string) bool) bool {
	for _, x := range xs {
		if !f(x) {
			return false
		}
	}
	return true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
