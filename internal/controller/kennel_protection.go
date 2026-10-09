package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// kennelProtectionEnabled is whether any check still on reads a branch's
// required status checks. Like the settings, it follows the checks and not the
// switch.
func kennelProtectionEnabled(p kennel.Policy) bool {
	return kennelSourceWanted(p, kennel.SourceProtection)
}

// kennelProtectionDue is whether the required checks are to be read on this
// pass because nothing has read them.
func kennelProtectionDue(row *store.KennelRepository, in kennelPassInput) bool {
	return kennelProtectionEnabled(in.policy) && parseKennelWatermark(row.Watermark).ProtectionState == ""
}

// kennelProtection is what was read of the default branch's required status
// checks.
//
// The names are a repository's own text. They are kept here, in the stored
// document, only so that the next evaluation can ask the jobs table whether a
// job by that name was seen, and they go no further: the snapshot holds counts,
// and so nothing that is shown, logged or sent to an assistant can repeat one.
type kennelProtection struct {
	// Required are the checks pinned to the GitHub Actions app, sorted.
	Required []string `json:"required,omitempty"`
	// Unpinned counts those any app may post, which are never judged.
	Unpinned int `json:"unpinned,omitempty"`
}

func (c *Controller) kennelReadProtection(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm.Protection = nil
	wm.ProtectionState = kennel.CoverageNotRead
	if l == nil {
		return wm.ProtectionState
	}
	if l.State != kennel.CoverageOK {
		wm.ProtectionState = l.State
		return wm.ProtectionState
	}
	reader, ok := l.Client.(github.KennelSettingsReader)
	if !ok {
		wm.ProtectionState = kennel.CoverageUnavailable
		return wm.ProtectionState
	}
	repo, ok := l.Repos[strings.ToLower(row.FullName)]
	if !ok || repo.DefaultBranch == "" {
		// A repository nothing has been pushed to has no branch to protect, and
		// that is not evidence about a branch.
		wm.ProtectionState = kennel.CoverageUnavailable
		return wm.ProtectionState
	}
	if ctx.Err() != nil || !c.mayAct() || c.githubHeld(inst.ID, in.now) || !c.kennelTake(inst.ID, in, l.Limit) {
		wm.ProtectionState = kennel.CoverageHeld
		return wm.ProtectionState
	}
	got, err := reader.KennelProtection(ctx, row.FullName, repo.DefaultBranch)
	c.observeKennel(inst.ID, err)
	c.holdIfRateLimited(inst.ID, err, in.now, "reading branch protection for Kennel Club")
	if err != nil {
		wm.ProtectionState = kennelState(err)
		// Branch protection that does not exist is answered as a success by the
		// reader, so a 404 that reaches here is a repository GitHub will not show
		// or an endpoint it lacks, and neither says anything about a check.
		if errors.Is(err, github.ErrNotFound) {
			wm.ProtectionState = kennel.CoverageUnavailable
		}
		return wm.ProtectionState
	}
	if got == nil {
		wm.ProtectionState = kennel.CoverageError
		return wm.ProtectionState
	}
	wm.Protection = &kennelProtection{Required: got.Required, Unpinned: got.Unpinned}
	wm.ProtectionState = kennel.CoverageOK
	if got.Partial {
		wm.ProtectionState = kennel.CoveragePartial
	}
	return wm.ProtectionState
}

// kennelProtectionFacts turns the required checks that were read into the counts
// the evaluator judges, against what the fleet's own record of jobs holds now.
//
// The jobs change between reads of GitHub, so this is worked out for every
// evaluation and not once when the checks were read. A repository that requires
// nothing asks the table nothing: most repositories have no required checks of
// this kind, and none should cost a query a tick for it.
func (c *Controller) kennelProtectionFacts(ctx context.Context, repo string, wm kennelWatermark, in kennelPassInput) (*kennel.ProtectionFacts, error) {
	p := wm.Protection
	if p == nil {
		return nil, nil
	}
	facts := &kennel.ProtectionFacts{Required: len(p.Required), Unpinned: p.Unpinned}
	if len(p.Required) == 0 {
		return facts, nil
	}
	seen, reported, err := c.st.KennelJobsSeen(ctx, repo, in.now.Add(-in.window), p.Required)
	if err != nil {
		return nil, err
	}
	facts.JobsSeen = seen
	for _, name := range p.Required {
		if !reported[name] {
			facts.NeverReported++
		}
	}
	return facts, nil
}
