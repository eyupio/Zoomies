package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

func (c *Controller) kennelReadSetup(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm.Setup = nil
	wm.SetupState = kennel.CoverageNotRead
	if l == nil {
		return wm.SetupState
	}
	if l.State != kennel.CoverageOK {
		wm.SetupState = l.State
		return wm.SetupState
	}
	reader, ok := l.Client.(github.KennelSetupReader)
	if !ok {
		wm.SetupState = kennel.CoverageUnavailable
		return wm.SetupState
	}
	repo, ok := l.Repos[strings.ToLower(row.FullName)]
	if !ok {
		wm.SetupState = kennel.CoverageUnavailable
		return wm.SetupState
	}
	if ctx.Err() != nil || !c.mayAct() || c.githubHeld(inst.ID, in.now) || !c.kennelTake(inst.ID, in, l.Limit) {
		wm.SetupState = kennel.CoverageHeld
		return wm.SetupState
	}
	got, err := reader.KennelSetup(ctx, row.FullName, repo.DefaultBranch)
	c.observeKennel(inst.ID, err)
	c.holdIfRateLimited(inst.ID, err, in.now, "reading repository setup for Kennel Club")
	if err != nil {
		wm.SetupState = kennelState(err)
		// A hidden or empty repository and a missing permission can all be a 404.
		// None is evidence that a particular setup file is missing.
		if errors.Is(err, github.ErrNotFound) {
			wm.SetupState = kennel.CoverageUnavailable
		}
		return wm.SetupState
	}
	if got == nil {
		wm.SetupState = kennel.CoverageError
		return wm.SetupState
	}
	wm.Setup = &kennel.SetupFacts{Present: map[kennel.Code]bool{}, HasDependencies: got.HasDependencies}
	for _, ck := range kennel.Checks() {
		if ck.Area == kennel.AreaSetup {
			wm.Setup.Present[ck.Code] = got.Present[string(ck.Code)[len("setup."):]]
		}
	}
	wm.SetupState = kennel.CoverageOK
	if got.Partial {
		wm.SetupState = kennel.CoveragePartial
	}
	return wm.SetupState
}

// A source first enabled after the previous refresh is read on the next pass,
// rather than waiting a day. The same predicate drives listing and collection.
func kennelSetupDue(row *store.KennelRepository, in kennelPassInput) bool {
	return kennelSetupEnabled(in.policy) && parseKennelWatermark(row.Watermark).SetupState == ""
}

func kennelSetupEnabled(p kennel.Policy) bool {
	return kennelSourceWanted(p, kennel.SourceSetup)
}
