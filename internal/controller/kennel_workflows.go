package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

func kennelWorkflowsEnabled(p kennel.Policy) bool {
	for _, ck := range kennel.Checks() {
		if (ck.Area == kennel.AreaCI || ck.Area == kennel.AreaToken) && !p.Disabled[string(ck.Area)] && !p.Disabled[string(ck.Code)] {
			return true
		}
	}
	return false
}
func kennelWorkflowsDue(row *store.KennelRepository, in kennelPassInput) bool {
	return kennelWorkflowsEnabled(in.policy) && parseKennelWatermark(row.Watermark).WorkflowState == ""
}
func (c *Controller) kennelReadWorkflows(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm.Workflows = nil
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
	facts := &kennel.WorkflowFacts{}
	state := kennel.CoverageOK
	if inventory.Partial {
		state = kennel.CoveragePartial
	}
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
		inspection, err := github.InspectKennelWorkflow(data)
		if err != nil {
			state = kennel.CoveragePartial
			continue
		}
		facts.Files++
		facts.NoTimeout += inspection.NoTimeout
		facts.NoConcurrency += inspection.NoConcurrency
		facts.FirstPartyUnpinned += inspection.FirstPartyUnpinned
		facts.OtherUnpinned += inspection.OtherUnpinned
		facts.PermissionsUnset += inspection.PermissionsUnset
	}
	// Positive evidence from successfully read files remains useful. Missing
	// evidence is incomplete, so it can never earn an all-clear.
	if facts.Files > 0 || state == kennel.CoverageOK {
		wm.Workflows = facts
	}
	outcome := state
	if facts.Files > 0 && state != kennel.CoverageOK {
		state = kennel.CoveragePartial
	}
	wm.WorkflowState = state
	return outcome
}
