// Package offline runs Kennel Club's workflow checks over a checkout on disk:
// the same parser and the same evaluator the controller uses, with a laptop's
// coverage. Nothing is sent anywhere. What a laptop cannot know, the fleet,
// the run history and the repository's tree as GitHub lists it, is reported
// as not checked, never silently absent, so "nothing found" is read for
// exactly what it is.
//
// It also owns the conversion from the parser's facts to the evaluator's,
// and the gate a path passes before it is kept, because the controller and
// the offline check must agree on both or a waiver made on one would not be
// about the other.
package offline

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/kennel/workflow"
)

// PathGrammar is what a workflow's path may look like before it is kept
// beside an evaluation or shown to a person. A path is a stranger's text; one
// that is not a plain name under .github/workflows is kept as "" and said to
// be unusual.
var PathGrammar = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.ya?ml$`)

// GatePath is p when it passes the grammar, else "".
func GatePath(p string) string {
	if PathGrammar.MatchString(p) {
		return p
	}
	return ""
}

// ErrNoWorkflows is the error for a root that is neither a checkout with
// workflow files nor a workflows directory holding some.
var ErrNoWorkflows = errors.New("no .github/workflows directory with workflow files")

// Options are what the person running the check decided.
type Options struct {
	// Public says the repository is public, which makes some findings worse.
	// A laptop cannot tell, so private, the milder reading, is the default.
	Public bool
	// Disabled names checks and areas turned off, as kennel.disabled_checks.
	Disabled map[string]bool
	// Prompts renders a prompt on each finding.
	Prompts bool
}

// File is one workflow file as read, by Git's blob SHA, with its path as the
// gate let it through.
type File struct {
	SHA  string `json:"sha"`
	Path string `json:"path"`
}

// NotChecked is a check that did not run here, and why.
type NotChecked struct {
	Code   kennel.Code `json:"code"`
	Reason string      `json:"reason"`
}

// Finding is an open finding, with its prompt when one was asked for, and
// where it came from when a controller contributed it.
type Finding struct {
	kennel.Finding
	Prompt string `json:"prompt,omitempty"`
	From   string `json:"from,omitempty"`
}

// Report is what the check found.
type Report struct {
	Version    int          `json:"version"`
	Root       string       `json:"root"`
	Visibility string       `json:"visibility"`
	Files      []File       `json:"files"`
	Unreadable []File       `json:"unreadable"`
	Findings   []Finding    `json:"findings"`
	NotChecked []NotChecked `json:"not_checked"`
}

// Convert turns the parser's facts about one file into the evaluator's. The
// label check needs the fleet's pools and the store's label rule, so
// LabelUnserved is left for the controller to fill.
func Convert(sha string, f workflow.Facts) kennel.WorkflowFile {
	file := kennel.WorkflowFile{
		SHA:                  sha,
		NoTimeout:            locations(f.NoTimeout),
		FirstPartyUnpinned:   locations(f.FirstPartyUnpinned),
		OtherUnpinned:        locations(f.OtherUnpinned),
		PermissionsUnset:     locations(f.PermissionsUnset),
		TargetCheckoutPRHead: locations(f.TargetCheckoutPRHead),
		SecretOnCommandLine:  locations(f.SecretOnCommandLine),
		Pinned:               f.Pinned,
	}
	if f.NoConcurrency != nil {
		file.NoConcurrency = &kennel.Location{JobIndex: f.NoConcurrency.JobIndex, Line: f.NoConcurrency.Line}
	}
	return file
}

func locations(in []workflow.Location) []kennel.Location {
	if len(in) == 0 {
		return nil
	}
	out := make([]kennel.Location, len(in))
	for i, l := range in {
		out[i] = kennel.Location{JobIndex: l.JobIndex, Line: l.Line}
	}
	return out
}

// Check reads the workflow files under root, which is a checkout or its
// .github/workflows directory, and evaluates them.
func Check(root string, o Options) (Report, error) {
	dir := filepath.Join(root, ".github", "workflows")
	if filepath.Base(root) == "workflows" && filepath.Base(filepath.Dir(root)) == ".github" {
		dir = root
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Report{}, ErrNoWorkflows
		}
		return Report{}, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return Report{}, ErrNoWorkflows
	}
	sort.Strings(names)

	r := Report{Version: kennel.Version, Root: root, Visibility: "private", Files: []File{}, Unreadable: []File{}, Findings: []Finding{}, NotChecked: []NotChecked{}}
	if o.Public {
		r.Visibility = "public"
	}
	facts := &kennel.WorkflowFacts{Files: []kennel.WorkflowFile{}, Unreadable: []string{}}
	state := kennel.CoverageOK
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return Report{}, fmt.Errorf("reading %s: %w", name, err)
		}
		file := File{SHA: blobSHA(data), Path: GatePath(".github/workflows/" + name)}
		r.Files = append(r.Files, file)
		parsed, err := workflow.Inspect(data)
		if len(data) > workflow.MaxBytes || err != nil {
			r.Unreadable = append(r.Unreadable, file)
			facts.Unreadable = append(facts.Unreadable, file.SHA)
			state = kennel.CoveragePartial
			continue
		}
		facts.Files = append(facts.Files, Convert(file.SHA, parsed))
	}

	visibility := kennel.VisibilityPrivate
	if o.Public {
		visibility = kennel.VisibilityPublic
	}
	snap := kennel.Snapshot{
		Repo:      kennel.Repo{Visibility: visibility},
		Workflows: facts,
		Coverage: kennel.Coverage{
			kennel.SourceMetadata:  {State: kennel.CoverageOK},
			kennel.SourceWorkflows: {State: state},
		},
	}
	disabled := map[string]bool{string(kennel.CodeLabelUnserved): true}
	for k, v := range o.Disabled {
		disabled[k] = v
	}
	ev := kennel.Evaluate(snap, kennel.Policy{Disabled: disabled})

	paths := make(map[string]string, len(r.Files))
	for _, f := range r.Files {
		paths[f.SHA] = f.Path
	}
	for _, f := range ev.Findings {
		out := Finding{Finding: f}
		if o.Prompts {
			out.Prompt = kennel.Prompt(f, paths)
		}
		r.Findings = append(r.Findings, out)
	}
	for _, code := range ev.Disabled {
		if code == kennel.CodeLabelUnserved && !o.Disabled[string(code)] && !o.Disabled[string(code.Area())] {
			r.NotChecked = append(r.NotChecked, NotChecked{Code: code, Reason: "the pools are known only to a controller"})
		}
	}
	for _, sk := range ev.Skipped {
		if sk.Source == kennel.SourceGuidance {
			r.NotChecked = append(r.NotChecked, NotChecked{Code: sk.Code, Reason: "agent guidance files are outside this workflow-only check"})
			continue
		}
		r.NotChecked = append(r.NotChecked, NotChecked{Code: sk.Code, Reason: "needs what only a controller knows: " + sourceWords(sk.Source)})
	}
	return r, nil
}

// blobSHA is Git's name for the file's content, so a file checked here and
// the same file read from GitHub are known by one name.
func blobSHA(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func sourceWords(s kennel.Source) string {
	switch s {
	case kennel.SourceFleet:
		return "what the fleet observed"
	case kennel.SourceRuns:
		return "the workflow runs"
	case kennel.SourceSetup:
		return "the repository's tree as GitHub lists it"
	case kennel.SourceMetadata:
		return "the repository's record"
	case kennel.SourceWorkflows:
		return "the workflow files"
	case kennel.SourceSettings:
		return "the repository's Actions settings"
	case kennel.SourceProtection:
		return "the status checks the default branch requires"
	}
	return string(s)
}
