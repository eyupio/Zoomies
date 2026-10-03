package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/aicontext"
	gh "github.com/google/go-github/v88/github"
)

func TestContextSetupReadsPinnedRegularFilesAndPublishesOneCompleteProposal(t *testing.T) {
	fake, client := migrationFake(t)
	fake.AddFile("acme/widgets", "README.md", "# Widgets\n\nMy README.\n")
	setup := client.(ContextSetupClient)
	source, err := setup.ReadContextSetup(context.Background(), "acme/widgets", "main")
	if err != nil {
		t.Fatal(err)
	}
	files, err := aicontext.PlanManagedSetup(aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: "1", RepositoryID: source.Repository.ID}, "acme/widgets", aicontext.DefaultConfig("main"), source.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("%d files", len(files))
	}
	req := ContextSetupRequest{Repo: "acme/widgets", Base: "main", BaseCommit: source.Commit, Head: "zoomies-ai-context-setup-test", PlanHash: strings.Repeat("a", 64), Files: files}
	pr, err := setup.OpenContextSetup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := setup.OpenContextSetup(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != second.Number || len(fake.Branches("acme/widgets")) != 2 {
		t.Fatalf("duplicate PR or branch: %+v %+v", pr, second)
	}
	original, _ := fake.FileContent("acme/widgets", "README.md")
	if original != "# Widgets\n\nMy README.\n" {
		t.Fatal("setup changed the default branch")
	}
	fake.mu.Lock()
	repo := fake.repoLocked("acme/widgets")
	g := repo.contextGit()
	commit := g.Commits[repo.branches[req.Head]]
	all := map[string]*gh.TreeEntry{}
	g.flatten(commit.GetTree().GetSHA(), "", all)
	fake.mu.Unlock()
	for _, file := range files {
		entry := all[file.Path]
		if entry == nil || g.Blobs[entry.GetSHA()] != file.Content {
			t.Fatalf("atomic commit missing %s", file.Path)
		}
	}
}

func TestContextSetupRefusesChangedBaseAndSymlinksWithoutWrites(t *testing.T) {
	fake, client := migrationFake(t)
	setup := client.(ContextSetupClient)
	source, err := setup.ReadContextSetup(context.Background(), "acme/widgets", "main")
	if err != nil {
		t.Fatal(err)
	}
	fake.AddFile("acme/widgets", "new.go", "package new\n")
	_, err = setup.OpenContextSetup(context.Background(), ContextSetupRequest{Repo: "acme/widgets", Base: "main", BaseCommit: source.Commit, Head: "setup", PlanHash: strings.Repeat("a", 64), Files: []aicontext.SetupChange{{Path: "AGENTS.md", Content: "guidance"}}})
	if !errors.Is(err, ErrSetupConflict) || len(fake.Branches("acme/widgets")) != 1 {
		t.Fatalf("changed base: %v", err)
	}
	fake.AddFile("acme/widgets", "CLAUDE.md", "/etc/passwd")
	fake.SetFileMode("acme/widgets", "CLAUDE.md", "120000")
	if _, err = setup.ReadContextSetup(context.Background(), "acme/widgets", "main"); !errors.Is(err, ErrSetupConflict) {
		t.Fatalf("symlink: %v", err)
	}
}

func TestContextSetupRecoversAPRAfterItsSuccessfulResponseWasLost(t *testing.T) {
	fake, client := migrationFake(t)
	setup := client.(ContextSetupClient)
	source, err := setup.ReadContextSetup(t.Context(), "acme/widgets", "main")
	if err != nil {
		t.Fatal(err)
	}
	req := ContextSetupRequest{Repo: "acme/widgets", Base: "main", BaseCommit: source.Commit, Head: "context-lost-response", PlanHash: strings.Repeat("a", 64), Files: []aicontext.SetupChange{{Path: "AGENTS.md", Mode: "100644", Content: "guidance"}}}
	fake.LoseNextContextPullResponse(req.Repo)
	if _, err := setup.OpenContextSetup(t.Context(), req); err == nil {
		t.Fatal("lost response unexpectedly succeeded")
	}
	fake.AddFile(req.Repo, "unrelated.go", "package unrelated")
	pr, err := setup.OpenContextSetup(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 1 {
		t.Fatal("retry duplicated the PR")
	}
	fake.mu.Lock()
	repo := fake.repoLocked(req.Repo)
	repo.contextGit().Pulls[0].State = gh.Ptr("closed")
	fake.mu.Unlock()
	if _, err := setup.OpenContextSetup(t.Context(), req); !errors.Is(err, ErrSetupConflict) {
		t.Fatalf("closed PR was reopened: %v", err)
	}
	fake.mu.Lock()
	g := repo.contextGit()
	head := g.Commits[repo.branches[req.Head]]
	head.Message = gh.Ptr("User edited this branch")
	fake.mu.Unlock()
	if _, err := setup.OpenContextSetup(t.Context(), req); !errors.Is(err, ErrSetupConflict) {
		t.Fatalf("edited branch overwritten: %v", err)
	}
}
