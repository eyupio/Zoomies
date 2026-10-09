package github

import (
	"errors"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/agentguidance"
	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/store"
)

func TestGuidanceInventoryReadsOnlyBoundedImmutableInstructionBlobs(t *testing.T) {
	f := newFake(t)
	f.AddFile("acme/api", "AGENTS.md", "# Rules\n")
	f.AddFile("acme/api", ".claude/CLAUDE.md", "@../AGENTS.md\n")
	f.AddFile("acme/api", "src/AGENTS.md", strings.Repeat("x", agentguidance.MaxBytes+1))
	f.AddFile("acme/api", "empty/CLAUDE.md", "")
	f.AddFile("acme/api", "link/AGENTS.md", "elsewhere")
	f.SetFileMode("acme/api", "link/AGENTS.md", "120000")
	reader := f.Client("acme", store.TargetOrg).(KennelGuidanceReader)
	inv, err := reader.KennelGuidanceInventory(t.Context(), "acme/api", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Partial || len(inv.Files) != 2 || len(inv.Skipped) != 3 {
		t.Fatalf("inventory=%+v", inv)
	}
	if _, err := reader.KennelWorkflowBlob(t.Context(), "acme/api", inv.Files[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Client("acme", store.TargetOrg).(AgentGuidanceClient).ReadAgentGuidance(t.Context(), "acme/api", ""); !errors.Is(err, ErrSetupConflict) {
		t.Fatalf("incomplete proposal accepted: %v", err)
	}
}

func TestGuidanceProposalsAreDraftAndNeedNoWorkflowWritePermission(t *testing.T) {
	f := newFake(t)
	f.AddFile("acme/api", "AGENTS.md", "# Rules\n")
	f.AddFile("acme/api", "CLAUDE.md", "# Rules\n")
	f.SetPermissions(map[string]string{"contents": "write", "pull_requests": "write"})
	client := f.Client("acme", store.TargetOrg).(AgentGuidanceClient)
	source, err := client.ReadAgentGuidance(t.Context(), "acme/api", "")
	if err != nil {
		t.Fatal(err)
	}
	req := ContextSetupRequest{Action: "agent-guidance", Repo: "acme/api", Base: "main", BaseCommit: source.Commit, Head: "zoomies-agent-guidance-test", PlanHash: strings.Repeat("a", 64), Files: []aicontext.SetupChange{{Path: "CLAUDE.md", Mode: "100644", Content: "@AGENTS.md\n"}}}
	pr, err := client.OpenContextSetup(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	pull := f.repoLocked("acme/api").contextGit().Pulls[0]
	isDraft := pull.GetDraft()
	f.mu.Unlock()
	if !isDraft {
		t.Fatal("guidance proposal was not a draft")
	}
	again, err := client.OpenContextSetup(t.Context(), req)
	if err != nil || pr.Number != again.Number {
		t.Fatalf("retry=%+v %v", again, err)
	}
}
