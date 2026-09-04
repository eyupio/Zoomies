package migrate

import (
	"path"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/store"
)

// Workflow is one workflow file as it exists on GitHub today.
type Workflow struct {
	// Path is repository-relative: ".github/workflows/ci.yml".
	Path string
	// SHA is the blob SHA, which GitHub requires to update the file without
	// clobbering a change somebody pushed while the wizard was open.
	SHA string
	// Content is the file as it is now.
	Content string
}

// WorkflowPlan is what the migration would do to one workflow file.
type WorkflowPlan struct {
	Path     string    `json:"path"`
	SHA      string    `json:"sha"`
	Rewrites []Rewrite `json:"rewrites"`
	Skips    []Skip    `json:"skips"`
	// Diff is the unified diff of the change, for the review step.
	Diff string `json:"diff"`
	// After is the rewritten file. It is not serialised: the browser has no
	// use for it, and shipping every workflow in an organisation through the
	// review screen would be a lot of bytes for something only the commit
	// needs.
	After string `json:"-"`
}

// Changed reports whether this file would be committed.
func (p WorkflowPlan) Changed() bool { return len(p.Rewrites) > 0 }

// RepoPlan is what the migration would do to one repository.
type RepoPlan struct {
	Repo          string         `json:"repo"`
	DefaultBranch string         `json:"default_branch"`
	Workflows     []WorkflowPlan `json:"workflows"`
	// HostedLabels are the GitHub-hosted labels this repository's workflows
	// ask for, whether or not they are mapped.
	HostedLabels []string `json:"hosted_labels"`
	// Error is set when the repository could not be read at all. The rest of
	// the plan is still returned: one unreadable repository must not cost the
	// operator the whole scan.
	Error string `json:"error,omitempty"`
}

// Changed reports whether this repository would get a pull request.
func (p RepoPlan) Changed() bool {
	for _, w := range p.Workflows {
		if w.Changed() {
			return true
		}
	}
	return false
}

// Counts summarises a plan for the wizard's headline.
type Counts struct {
	Repos     int `json:"repos"`
	Workflows int `json:"workflows"`
	Jobs      int `json:"jobs"`
	Skipped   int `json:"skipped"`
}

// Count totals what a set of repository plans would change.
func Count(plans []RepoPlan) Counts {
	var c Counts
	for _, p := range plans {
		if p.Changed() {
			c.Repos++
		}
		for _, w := range p.Workflows {
			if w.Changed() {
				c.Workflows++
			}
			c.Jobs += len(w.Rewrites)
			c.Skipped += len(w.Skips)
		}
	}
	return c
}

// PlanRepo applies a mapping to one repository's workflows.
func PlanRepo(repo, defaultBranch string, workflows []Workflow, m Mapping) RepoPlan {
	out := RepoPlan{Repo: repo, DefaultBranch: defaultBranch}
	seen := map[string]bool{}
	for _, w := range workflows {
		res := File(w.Content, m)
		plan := WorkflowPlan{
			Path:     w.Path,
			SHA:      w.SHA,
			Rewrites: res.Rewrites,
			Skips:    res.Skips,
			After:    res.Content,
		}
		if plan.Changed() {
			plan.Diff = Diff(w.Path, w.Content, res.Content)
		}
		out.Workflows = append(out.Workflows, plan)
		for _, l := range HostedLabelsIn(w.Content) {
			if !seen[l] {
				seen[l] = true
				out.HostedLabels = append(out.HostedLabels, l)
			}
		}
	}
	return out
}

// IsWorkflowPath reports whether a repository path is a workflow file GitHub
// would actually run.
//
// Only the top level of .github/workflows counts: GitHub does not read
// subdirectories, and a "workflows/archive/old.yml" that no longer runs is not
// something to open a pull request about.
func IsWorkflowPath(p string) bool {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	dir, file := path.Split(p)
	if strings.Trim(dir, "/") != ".github/workflows" {
		return false
	}
	ext := strings.ToLower(path.Ext(file))
	return ext == ".yml" || ext == ".yaml"
}

// ---------------------------------------------------------------------------
// Suggesting a mapping
// ---------------------------------------------------------------------------

// Suggest proposes, for each hosted label, the `runs-on` value that should
// replace it.
//
// The proposal is a starting point the operator edits, never a decision. It
// matches on what the hosted label promises -- an operating system and an
// architecture -- and offers the pool that promises the same thing. A label
// with no plausible pool is left out of the map entirely rather than mapped to
// something approximate: an unmapped label is reported as a skip the operator
// can see, while a wrong mapping is a workflow that hangs.
func Suggest(pools []*store.Pool, hosted []string) map[string]string {
	out := map[string]string{}
	for _, label := range hosted {
		os, arch := describeHosted(label)
		if os == "" {
			continue
		}
		if p := bestPool(pools, os, arch); p != nil {
			out[strings.ToLower(label)] = store.RunsOn(p.Labels)
		}
	}
	return out
}

// describeHosted reads the operating system and architecture out of one of
// GitHub's runner labels: "ubuntu-24.04-arm" is linux on arm64,
// "windows-latest" is windows on x64.
func describeHosted(label string) (os, arch string) {
	l := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.HasPrefix(l, "ubuntu-"):
		os = "linux"
	case strings.HasPrefix(l, "windows-"):
		os = "windows"
	case strings.HasPrefix(l, "macos-"):
		os = "macos"
	default:
		return "", ""
	}
	// GitHub's Apple silicon images carry no "-arm" suffix: macos-14 and later
	// are arm64 unless the label says "large", which is the Intel image.
	arch = "x64"
	switch {
	case strings.Contains(l, "-arm"):
		arch = "arm64"
	case os == "macos" && !strings.Contains(l, "large"):
		if v := macosVersion(l); v >= 14 {
			arch = "arm64"
		}
	}
	return os, arch
}

// macosVersion pulls the major version out of "macos-14" and "macos-15-large".
// "macos-latest" has no number and returns the version GitHub currently points
// it at.
func macosVersion(label string) int {
	rest := strings.TrimPrefix(label, "macos-")
	if strings.HasPrefix(rest, "latest") {
		return 14
	}
	n := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// bestPool picks the pool whose labels promise this operating system and
// architecture, preferring an exact promise over a pool that promises nothing.
//
// A pool that declares no os or arch is a candidate, because most single-host
// fleets never declare either -- but only after every pool that says so
// outright has been considered.
func bestPool(pools []*store.Pool, os, arch string) *store.Pool {
	var exact, partial, silent []*store.Pool
	for _, p := range pools {
		if p == nil || !p.Enabled {
			continue
		}
		labels := store.NormalizeLabels(p.Labels)
		poolOS, poolArch := declared(labels, osNames), declared(labels, archNames)
		switch {
		case poolOS != "" && poolOS != os, poolArch != "" && poolArch != arch:
			// The pool promises a different machine. Never.
		case poolOS == os && poolArch == arch:
			exact = append(exact, p)
		case poolOS == os || poolArch == arch:
			partial = append(partial, p)
		default:
			silent = append(silent, p)
		}
	}
	for _, tier := range [][]*store.Pool{exact, partial, silent} {
		if len(tier) == 0 {
			continue
		}
		// Ties break on name, so the same scan proposes the same mapping every
		// time it is run.
		sort.Slice(tier, func(i, j int) bool {
			if tier[i].Name != tier[j].Name {
				return tier[i].Name < tier[j].Name
			}
			return tier[i].ID < tier[j].ID
		})
		return tier[0]
	}
	return nil
}

// The two dimensions a pool can promise something about. They are ordered
// slices rather than sets because the suffix scan below has to try "arm64"
// before "arm": a map would answer "zoomies-linux-arm64" differently on
// different runs, and a mapping that changes between two scans of the same
// fleet is one nobody can review.
var (
	osNames   = []string{"linux", "windows", "macos"}
	archNames = []string{"arm64", "x64", "arm"}
)

// declared returns the label a pool carries from one dimension, or "" when it
// makes no promise about it.
func declared(labels []string, dim []string) string {
	for _, l := range labels {
		for _, name := range dim {
			if l == name {
				return name
			}
		}
	}
	// A branded label often carries the promise in its own name --
	// "zoomies-linux-arm64" -- which is the shape the installer suggests and
	// the shape most fleets end up with.
	for _, l := range labels {
		for _, name := range dim {
			if strings.HasSuffix(l, "-"+name) || strings.Contains(l, "-"+name+"-") {
				return name
			}
		}
	}
	return ""
}
