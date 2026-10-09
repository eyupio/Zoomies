package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// kennelSettingsEnabled is whether any check still on reads a repository's
// settings. It follows the checks and not the switch, as the other sources do,
// so a check the operator turned off by name is not read for.
func kennelSettingsEnabled(p kennel.Policy) bool {
	return kennelSourceWanted(p, kennel.SourceSettings)
}

// kennelSettingsDue is whether the settings are to be read on this pass because
// nothing has read them: a source first enabled is read on the next pass and not
// after a day.
func kennelSettingsDue(row *store.KennelRepository, in kennelPassInput) bool {
	return kennelSettingsEnabled(in.policy) && parseKennelWatermark(row.Watermark).SettingsState == ""
}

// kennelApprovalPolicy is the word GitHub sent for a fork pull request approval
// policy, if it is one of the three this version knows, and "" if it is not. An
// unknown word is left unjudged by the evaluator rather than taken for a strong
// policy, and is never repeated in a sentence.
func kennelApprovalPolicy(word string) kennel.ForkApproval {
	switch p := kennel.ForkApproval(word); p {
	case kennel.ApprovalNewToGitHub, kennel.ApprovalFirstTime, kennel.ApprovalAll:
		return p
	}
	return ""
}

func (c *Controller) kennelReadSettings(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm.Settings = nil
	wm.SettingsState = kennel.CoverageNotRead
	if l == nil {
		return wm.SettingsState
	}
	if l.State != kennel.CoverageOK {
		wm.SettingsState = l.State
		return wm.SettingsState
	}
	reader, ok := l.Client.(github.KennelSettingsReader)
	if !ok {
		wm.SettingsState = kennel.CoverageUnavailable
		return wm.SettingsState
	}
	repo, ok := l.Repos[strings.ToLower(row.FullName)]
	if !ok {
		wm.SettingsState = kennel.CoverageUnavailable
		return wm.SettingsState
	}
	if ctx.Err() != nil || !c.mayAct() || c.githubHeld(inst.ID, in.now) || !c.kennelTake(inst.ID, in, l.Limit) {
		wm.SettingsState = kennel.CoverageHeld
		return wm.SettingsState
	}
	got, err := reader.KennelSettings(ctx, row.FullName, repo.Visibility == string(kennel.VisibilityPublic))
	c.observeKennel(inst.ID, err)
	c.holdIfRateLimited(inst.ID, err, in.now, "reading repository settings for Kennel Club")
	if err != nil {
		wm.SettingsState = kennelState(err)
		// A repository GitHub will not show, and an endpoint an older GitHub lacks,
		// are both a 404. Neither is evidence about a setting.
		if errors.Is(err, github.ErrNotFound) {
			wm.SettingsState = kennel.CoverageUnavailable
		}
		return wm.SettingsState
	}
	if got == nil {
		wm.SettingsState = kennel.CoverageError
		return wm.SettingsState
	}
	facts := &kennel.SettingsFacts{
		DefaultTokenWrite: got.DefaultTokenWrite,
		ForkApproval:      kennelApprovalPolicy(got.ForkApproval),
	}
	if got.PrivateFork != nil {
		facts.PrivateFork = &kennel.PrivateForkFacts{Runs: got.PrivateFork.Runs, Secrets: got.PrivateFork.Secrets, WriteToken: got.PrivateFork.WriteToken}
	}
	wm.Settings = facts
	wm.SettingsState = kennel.CoverageOK
	if got.Partial {
		wm.SettingsState = kennel.CoveragePartial
	}
	return wm.SettingsState
}
