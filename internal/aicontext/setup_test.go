package aicontext

import (
	"strings"
	"testing"
)

func setupInputs() (RepositoryKey, Config) {
	return RepositoryKey{GitHubHost: "github.com", InstallationID: "1", RepositoryID: 42}, DefaultConfig("main")
}

func TestSetupPreservesUserTextAndRetriesWithoutChanges(t *testing.T) {
	key, config := setupInputs()
	original := "# Existing guidance\r\n\r\nKeep my instructions.\r\n"
	files := []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: original}, {Path: "readme.markdown", SHA: strings.Repeat("b", 40), Content: "# Project\n"}}
	changes, err := PlanSetupFiles(key, "owner/repo", config, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 4 {
		t.Fatalf("got %d changes", len(changes))
	}
	saved := make([]SetupFile, 0, len(changes))
	for _, change := range changes {
		saved = append(saved, SetupFile{Path: change.Path, SHA: strings.Repeat("c", 40), Content: change.Content})
		if change.Path == "CLAUDE.md" {
			if !strings.HasPrefix(change.Content, original) || strings.Contains(strings.ReplaceAll(change.Content, "\r\n", ""), "\n") {
				t.Fatal("user content or CRLF changed")
			}
			if change.PreviousSHA != strings.Repeat("a", 40) {
				t.Fatal("lost original blob SHA")
			}
		}
		if change.Path == "readme.markdown" && !strings.Contains(change.Content, "https://github.com/owner/repo/actions/workflows/zoomies-ai-context.yml/badge.svg") {
			t.Fatal("missing repository badge")
		}
	}
	retry, err := PlanSetupFiles(key, "owner/repo", config, saved)
	if err != nil || len(retry) != 0 {
		t.Fatalf("retry: %v, %d changes", err, len(retry))
	}
}

func TestSetupRefusesAmbiguousOwnershipAndCustomFiles(t *testing.T) {
	key, config := setupInputs()
	cases := []struct {
		name  string
		files []SetupFile
	}{
		{"unowned configuration", []SetupFile{{Path: ConfigPath, SHA: strings.Repeat("a", 40), Content: "{}"}}},
		{"missing end", []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: managedStart}}},
		{"reversed", []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: managedEnd + managedStart}}},
		{"duplicate", []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: managedStart + managedStart + managedEnd}}},
		{"custom managed content", []SetupFile{{Path: "AGENTS.md", SHA: strings.Repeat("a", 40), Content: managedStart + "mine" + managedEnd}}},
		{"partial marker", []SetupFile{{Path: "AGENTS.md", SHA: strings.Repeat("a", 40), Content: "<!-- zoomies-ai-context:other -->"}}},
		{"no blob identity", []SetupFile{{Path: "CLAUDE.md", Content: "mine"}}},
		{"symlink or subdirectory path", []SetupFile{{Path: "../CLAUDE.md"}}},
		{"two readmes", []SetupFile{{Path: "README.md"}, {Path: "readme.markdown"}}},
		{"oversized", []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: strings.Repeat("x", maxSetupFileBytes+1)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes, err := PlanSetupFiles(key, "owner/repo", config, tc.files)
			if err == nil || changes != nil {
				t.Fatalf("must refuse whole plan: %v", err)
			}
		})
	}
}

func TestSetupRefusesCustomConfigurationInsteadOfSilentlyReplacingIt(t *testing.T) {
	key, config := setupInputs()
	changes, err := PlanSetupFiles(key, "owner/repo", config, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := changes[0].Content
	for _, edit := range []string{strings.Replace(content, `"keep_snapshots": 3`, `"keep_snapshots": 4`, 1), strings.Replace(content, `"manager":`, `"custom": true, "manager":`, 1), strings.Replace(content, `"template_version": 1`, `"template_version": 2`, 1)} {
		_, err := PlanSetupFiles(key, "owner/repo", config, []SetupFile{{Path: ConfigPath, SHA: strings.Repeat("a", 40), Content: edit}})
		if err == nil {
			t.Fatal("custom configuration was replaced")
		}
	}
}

func TestSetupUsesEnterpriseBadgeAndDoesNotCreateAMissingReadme(t *testing.T) {
	key, config := setupInputs()
	key.GitHubHost = "github.example.org"
	changes, err := PlanSetupFiles(key, "owner/repo", config, nil)
	if err != nil || len(changes) != 3 {
		t.Fatalf("%v: %d changes", err, len(changes))
	}
	changes, err = PlanSetupFiles(key, "owner/repo", config, []SetupFile{{Path: "README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changes[3].Content, "https://github.example.org/owner/repo/") {
		t.Fatal("wrong badge host")
	}
	config.Destination = Zoomies
	if _, err = PlanSetupFiles(key, "owner/repo", config, nil); err == nil {
		t.Fatal("Zoomies-only setup must remain unavailable")
	}
}
