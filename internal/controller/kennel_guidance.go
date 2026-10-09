package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/eyupio/zoomies/internal/agentguidance"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

func kennelGuidanceEnabled(p kennel.Policy) bool { return kennelSourceWanted(p, kennel.SourceGuidance) }
func kennelGuidanceDue(row *store.KennelRepository, in kennelPassInput) bool {
	return kennelGuidanceEnabled(in.policy) && parseKennelWatermark(row.Watermark).GuidanceState == ""
}

func guidanceFacts(issues []agentguidance.Issue) *kennel.GuidanceFacts {
	out := &kennel.GuidanceFacts{}
	for _, issue := range issues {
		loc := kennel.GuidanceLocation{SHA: issue.SHA, Line: issue.Line}
		switch issue.Kind {
		case "missing":
			out.Missing = true
		case "broken_reference":
			out.Broken = append(out.Broken, loc)
		case "duplicated":
			out.Duplicated = append(out.Duplicated, loc)
		case "unreadable":
			out.Unreadable = append(out.Unreadable, loc)
		}
	}
	return out
}

func (c *Controller) kennelReadGuidance(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm.Guidance = nil
	wm.GuidanceFiles = nil
	wm.GuidanceState = kennel.CoverageNotRead
	if l == nil {
		return wm.GuidanceState
	}
	if l.State != kennel.CoverageOK {
		wm.GuidanceState = l.State
		return wm.GuidanceState
	}
	reader, ok := l.Client.(github.KennelGuidanceReader)
	if !ok {
		wm.GuidanceState = kennel.CoverageUnavailable
		return wm.GuidanceState
	}
	repo, ok := l.Repos[strings.ToLower(row.FullName)]
	if !ok {
		wm.GuidanceState = kennel.CoverageUnavailable
		return wm.GuidanceState
	}
	take := func() bool {
		return c.cfg().Kennel.Enabled && c.cfg().Kennel.AgentGuidance && ctx.Err() == nil && c.mayAct() && !c.githubHeld(inst.ID, in.now) && c.kennelTake(inst.ID, in, l.Limit)
	}
	failed := func(err error) kennel.CoverageState {
		c.observeKennel(inst.ID, err)
		c.holdIfRateLimited(inst.ID, err, in.now, "reading agent guidance for Kennel Club")
		state := kennelState(err)
		if errors.Is(err, github.ErrNotFound) {
			state = kennel.CoverageUnavailable
		}
		return state
	}
	if !take() {
		wm.GuidanceState = kennel.CoverageHeld
		return wm.GuidanceState
	}
	inv, err := reader.KennelGuidanceInventory(ctx, row.FullName, repo.DefaultBranch)
	if err != nil {
		wm.GuidanceState = failed(err)
		return wm.GuidanceState
	}
	if inv == nil {
		wm.GuidanceState = kennel.CoverageError
		return wm.GuidanceState
	}
	c.observeKennel(inst.ID, nil)
	state := kennel.CoverageOK
	if inv.Partial {
		state = kennel.CoveragePartial
	}
	var files []agentguidance.File
	var issues []agentguidance.Issue
	remember := func(ref github.WorkflowFileRef) {
		p := ""
		if agentguidance.SafePath(ref.Path) {
			p = ref.Path
		}
		wm.GuidanceFiles = append(wm.GuidanceFiles, kennelWorkflowRef{SHA: ref.SHA, Path: p})
	}
	for _, ref := range inv.Skipped {
		remember(ref)
		issues = append(issues, agentguidance.Issue{Kind: "unreadable", SHA: ref.SHA})
	}
	for _, ref := range inv.Files {
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
		remember(ref)
		files = append(files, agentguidance.File{Path: ref.Path, SHA: ref.SHA, Content: string(data)})
	}
	outcome := state
	if len(files) > 0 && state != kennel.CoverageOK {
		state = kennel.CoveragePartial
	}
	if state == kennel.CoverageOK || state == kennel.CoveragePartial {
		issues = append(issues, agentguidance.Inspect(files, inv.Paths, !inv.TreePartial)...)
		wm.Guidance = guidanceFacts(issues)
	}
	wm.GuidanceState = state
	return outcome
}
