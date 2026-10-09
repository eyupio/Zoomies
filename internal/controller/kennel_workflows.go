package controller

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/kennel/workflow"
	"github.com/eyupio/zoomies/internal/store"
)

// workflowPathGrammar is what a workflow's path may look like before it is
// kept beside the evaluation. A path is a stranger's text; one that is not a
// plain name under .github/workflows is kept as "" and shown as unusual.
var workflowPathGrammar = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.ya?ml$`)

func kennelWorkflowsEnabled(p kennel.Policy) bool {
	for _, ck := range kennel.Checks() {
		if (ck.Area == kennel.AreaCI || ck.Area == kennel.AreaToken) && !p.Disabled[string(ck.Area)] && !p.Disabled[string(ck.Code)] {
			return true
		}
	}
	return false
}

// kennelWorkflowsDue is whether the workflow files are to be read on this
// pass: when nothing has read them, and once after an upgrade whose watermark
// holds counts the evaluator can no longer judge.
func kennelWorkflowsDue(row *store.KennelRepository, in kennelPassInput) bool {
	wm := parseKennelWatermark(row.Watermark)
	return kennelWorkflowsEnabled(in.policy) && (wm.WorkflowState == "" || wm.WorkflowFormat < kennelWorkflowFormat)
}

// kennelServedLabels is every label a pool of the installation serves, with
// the ones every runner carries and the brand label, so a job's runs-on can
// be tested here and its labels never leave the controller.
func kennelServedLabels(installationID string, pools map[string]*store.Pool) map[string]bool {
	served := map[string]bool{store.BrandLabel: true}
	for l := range store.ImplicitLabels {
		served[l] = true
	}
	for _, p := range pools {
		if p.InstallationID != installationID || !p.Enabled {
			continue
		}
		for _, l := range store.NormalizeLabels(p.Labels) {
			served[l] = true
		}
	}
	return served
}

// kennelUnservedLabels is each job whose literal runs-on names a label no pool
// serves. A job whose every label is served is covered.
func kennelUnservedLabels(runsOn []workflow.RunsOn, served map[string]bool) []kennel.Location {
	var out []kennel.Location
	for _, job := range runsOn {
		covered := true
		for _, l := range store.NormalizeLabels(job.Labels) {
			if !served[l] {
				covered = false
			}
		}
		if !covered {
			out = append(out, kennel.Location{JobIndex: job.JobIndex, Line: job.Line})
		}
	}
	return out
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

func kennelWorkflowFile(sha string, f workflow.Facts, served map[string]bool) kennel.WorkflowFile {
	file := kennel.WorkflowFile{
		SHA:                  sha,
		NoTimeout:            locations(f.NoTimeout),
		FirstPartyUnpinned:   locations(f.FirstPartyUnpinned),
		OtherUnpinned:        locations(f.OtherUnpinned),
		PermissionsUnset:     locations(f.PermissionsUnset),
		TargetCheckoutPRHead: locations(f.TargetCheckoutPRHead),
		SecretOnCommandLine:  locations(f.SecretOnCommandLine),
		LabelUnserved:        kennelUnservedLabels(f.RunsOn, served),
		Pinned:               f.Pinned,
	}
	if f.NoConcurrency != nil {
		file.NoConcurrency = &kennel.Location{JobIndex: f.NoConcurrency.JobIndex, Line: f.NoConcurrency.Line}
	}
	return file
}

func gatedPath(p string) string {
	if workflowPathGrammar.MatchString(p) {
		return p
	}
	return ""
}

func (c *Controller) kennelReadWorkflows(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm.Workflows = nil
	wm.WorkflowFiles = nil
	wm.WorkflowFormat = kennelWorkflowFormat
	wm.WorkflowState = kennel.CoverageNotRead
	if l == nil {
		return wm.WorkflowState
	}
	if l.State != kennel.CoverageOK {
		wm.WorkflowState = l.State
		return wm.WorkflowState
	}
	reader, ok := l.Client.(github.KennelWorkflowReader)
	if !ok {
		wm.WorkflowState = kennel.CoverageUnavailable
		return wm.WorkflowState
	}
	repo, ok := l.Repos[strings.ToLower(row.FullName)]
	if !ok {
		wm.WorkflowState = kennel.CoverageUnavailable
		return wm.WorkflowState
	}
	take := func() bool {
		return ctx.Err() == nil && c.mayAct() && !c.githubHeld(inst.ID, in.now) && c.kennelTake(inst.ID, in, l.Limit)
	}
	if !take() {
		wm.WorkflowState = kennel.CoverageHeld
		return wm.WorkflowState
	}
	inventory, err := reader.KennelWorkflowInventory(ctx, row.FullName, repo.DefaultBranch)
	failed := func(err error) kennel.CoverageState {
		c.observeKennel(inst.ID, err)
		c.holdIfRateLimited(inst.ID, err, in.now, "reading workflow best practices for Kennel Club")
		state := kennelState(err)
		if errors.Is(err, github.ErrNotFound) {
			state = kennel.CoverageUnavailable
		}
		return state
	}
	if err != nil {
		wm.WorkflowState = failed(err)
		return wm.WorkflowState
	}
	c.observeKennel(inst.ID, nil)
	if inventory == nil {
		wm.WorkflowState = kennel.CoverageError
		return wm.WorkflowState
	}
	served := kennelServedLabels(inst.ID, in.pools)
	facts := &kennel.WorkflowFacts{Files: []kennel.WorkflowFile{}, Unreadable: []string{}}
	var files []kennelWorkflowRef
	state := kennel.CoverageOK
	if inventory.Partial {
		state = kennel.CoveragePartial
	}
	// A file over the limits was never read, and the finding it may hold is
	// not an all-clear: it is named as unreadable, with its path for the page.
	for _, ref := range inventory.Skipped {
		facts.Unreadable = append(facts.Unreadable, ref.SHA)
		files = append(files, kennelWorkflowRef{SHA: ref.SHA, Path: gatedPath(ref.Path)})
	}
	read := 0
	for _, ref := range inventory.Files {
		if !take() {
			state = kennel.CoverageHeld
			break
		}
		data, err := reader.KennelWorkflowBlob(ctx, row.FullName, ref)
		if err != nil {
			state = failed(err)
			break
		}
		c.observeKennel(inst.ID, nil)
		files = append(files, kennelWorkflowRef{SHA: ref.SHA, Path: gatedPath(ref.Path)})
		read++
		parsed, err := workflow.Inspect(data)
		if err != nil {
			// The parser refused a shape it will not judge in part. The file is
			// named, the source stays partial, and nothing in it is an all-clear.
			facts.Unreadable = append(facts.Unreadable, ref.SHA)
			state = kennel.CoveragePartial
			continue
		}
		facts.Files = append(facts.Files, kennelWorkflowFile(ref.SHA, parsed, served))
	}
	// Positive evidence from successfully read files remains useful. Missing
	// evidence is incomplete, so it can never earn an all-clear.
	if read > 0 || state == kennel.CoverageOK {
		wm.Workflows = facts
		wm.WorkflowFiles = files
	}
	outcome := state
	if read > 0 && state != kennel.CoverageOK {
		state = kennel.CoveragePartial
	}
	wm.WorkflowState = state
	return outcome
}
