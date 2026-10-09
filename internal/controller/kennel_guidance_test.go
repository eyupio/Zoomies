package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

func TestGuidanceReadsRequireOptInAndKeepFileEvidenceWithoutRepositoryProse(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.AddFile("acme/api", "CLAUDE.md", "@AGENTS.md\n\nNever repeat hostile prose.\n")
	f.pass()
	if f.requestsTo("/git/blobs/") != 0 {
		t.Fatal("guidance read without opt-in")
	}
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.AgentGuidance = true })
	f.pass()
	v := f.view("acme/api")
	if v.State != kennel.StateAttention || len(v.Files) != 1 || v.Files[0].Path != "CLAUDE.md" {
		t.Fatalf("view=%+v", v)
	}
	for _, finding := range v.Findings {
		if finding.Code == kennel.CodeGuidanceBroken && !strings.Contains(finding.Prompt, "CLAUDE.md") {
			t.Fatal("guidance file missing from copied finding prompt")
		}
	}
	if raw := string(f.row("acme/api").Watermark); strings.Contains(raw, "hostile") {
		t.Fatal("repository prose persisted in the watermark")
	}
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.AgentGuidance = false })
	before := f.requestsTo("/git/blobs/")
	f.dueAgain()
	f.pass()
	if f.requestsTo("/git/blobs/") != before {
		t.Fatal("guidance read after switching off")
	}
}

func TestGuidanceProposalPreservesCustomTextRejectsStaleReviewAndRechecksAfterMerge(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.SetPermissions(map[string]string{"contents": "write", "pull_requests": "write"})
	f.gh.AddFile("acme/api", "AGENTS.md", "# Rules\n\nKeep existing rules.\n")
	f.gh.AddFile("acme/api", ".claude/CLAUDE.md", "@AGENTS.md\n\nKeep Claude-only rules.\n")
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.AgentGuidance = true })
	f.pass()
	id := f.row("acme/api").ID
	plan, err := f.c.PreviewKennelGuidance(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Content != "@../AGENTS.md\n\nKeep Claude-only rules.\n" {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := f.c.CreateKennelGuidancePR(f.ctx, id, KennelGuidanceApproval{PlanHash: "wrong"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale hash: %v", err)
	}
	result, err := f.c.CreateKennelGuidancePR(f.ctx, id, KennelGuidanceApproval{PlanHash: plan.PlanHash})
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.c.CreateKennelGuidancePR(f.ctx, id, KennelGuidanceApproval{PlanHash: plan.PlanHash})
	if err != nil || again.PullRequest.Number != result.PullRequest.Number {
		t.Fatalf("retry=%+v %v", again, err)
	}

	f.gh.AddFile("acme/api", "unrelated.go", "package unrelated\n")
	fresh, err := f.c.PreviewKennelGuidance(f.ctx, id)
	if err != nil || fresh.Branch != plan.Branch || fresh.PlanHash == plan.PlanHash {
		t.Fatalf("proposal identity after unrelated push: %+v %v", fresh, err)
	}
	if _, err := f.c.CreateKennelGuidancePR(f.ctx, id, KennelGuidanceApproval{PlanHash: fresh.PlanHash}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate proposal after base change: %v", err)
	}
	if !f.gh.MergeContextPull("acme/api", result.PullRequest.Number) {
		t.Fatal("merge failed")
	}
	f.dueAgain()
	f.pass()
	for _, finding := range f.view("acme/api").Findings {
		if finding.Code == kennel.CodeGuidanceBroken {
			t.Fatal("merged repair did not clear finding")
		}
	}
	if _, err := f.c.CreateKennelGuidancePR(f.ctx, id, KennelGuidanceApproval{PlanHash: plan.PlanHash}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed base accepted: %v", err)
	}
}

func TestGuidancePreviewNeedsOnlyReadPermissionAndNoChangesOnUntrackedRepositories(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.SetPermissions(map[string]string{"contents": "read"})
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.AgentGuidance = true })
	f.pass()
	id := f.row("acme/api").ID
	plan, err := f.c.PreviewKennelGuidance(f.ctx, id)
	if err != nil || len(plan.Files) != 2 {
		t.Fatalf("preview=%+v %v", plan, err)
	}
	if _, err := f.c.CreateKennelGuidancePR(f.ctx, id, KennelGuidanceApproval{PlanHash: plan.PlanHash}); err == nil {
		t.Fatal("read-only installation published a PR")
	}
	_, _, _, err = f.c.SetKennelTracking(f.ctx, id, KennelTrackingInput{Tracked: false, Reason: "This repository is deliberately excluded."}, KennelActor{CanUntrack: true})
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.gh.Requests())
	if _, err := f.c.PreviewKennelGuidance(f.ctx, id); !errors.Is(err, ErrKennelNotTracked) {
		t.Fatalf("untracked preview: %v", err)
	}
	if len(f.gh.Requests()) != before {
		t.Fatal("untracked repository read")
	}
}
