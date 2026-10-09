package controller

import (
	"context"
	"errors"
	"github.com/eyupio/zoomies/internal/kennel/offline"
	"slices"
	"strings"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/kennel/workflow"
	"github.com/eyupio/zoomies/internal/store"
)

func kennelWorkflowsEnabled(p kennel.Policy) bool {
	return kennelSourceWanted(p, kennel.SourceWorkflows)
}

func kennelSourceWanted(p kennel.Policy, src kennel.Source) bool {
	for _, ck := range kennel.Checks() {
		reads := slices.Contains(ck.Needs, src) || slices.Contains(ck.Conditional, src)
		if reads && !p.Disabled[string(ck.Area)] && !p.Disabled[string(ck.Code)] {
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

// kennelWorkflowFile is the parser's facts as the evaluator reads them, with
// the label check the controller alone can make, since it knows the pools.
func kennelWorkflowFile(sha string, f workflow.Facts, served map[string]bool) kennel.WorkflowFile {
	file := offline.Convert(sha, f)
	file.LabelUnserved = kennelUnservedLabels(f.RunsOn, served)
	return file
}

func gatedPath(p string) string { return offline.GatePath(p) }

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
	// Positive evidence from successfully read files remains useful, and so
	// is the name of a file that could not be read, which may be the only
	// thing there is to say. Missing evidence is incomplete, so it can never
	// earn an all-clear.
	if read > 0 || len(facts.Unreadable) > 0 || state == kennel.CoverageOK {
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
