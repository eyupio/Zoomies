package aicontext

import (
	"strings"
	"testing"
)

func setupInputs() (RepositoryKey, Config) {
	return RepositoryKey{GitHubHost: "github.com", InstallationID: "1", RepositoryID: 42}, DefaultConfig("main")
}

func TestAssistantInstructionsDescribeTheSelectedDestinationAndConnection(t *testing.T) {
	key, config := setupInputs()
	for _, destination := range []Destination{Repository, Zoomies, Both} {
		config.Destination = destination
		body := AssistantInstructions(key, "owner/repo", config)
		if strings.Contains(body, "Repository context lives") != (destination != Zoomies) || strings.Contains(body, "context_overview") != (destination != Repository) {
			t.Fatalf("incorrect destination guidance for %s", destination)
		}
		for _, required := range []string{"owner/repo", "source_commit", "Other assistants", "untrusted"} {
			if required == "source_commit" && destination == Zoomies {
				continue
			}
			if !strings.Contains(body, required) {
				t.Fatalf("missing %s for %s", required, destination)
			}
		}
	}
}

func TestRepositoryInstructionsPreferPreparedContextWithoutRequiringMCP(t *testing.T) {
	key, config := setupInputs()
	for _, destination := range []Destination{Repository, Both} {
		config.Destination = destination
		body := AssistantInstructions(key, "owner/repo", config)
		for _, required := range []string{
			"Use repository context first — no MCP required",
			"first source reference before browsing individual source files",
			"https://github.com/owner/repo/blob/zoomies-ai-context/.zoomies/ai-context/manifest.json",
			"https://github.com/owner/repo/blob/zoomies-ai-context/.zoomies/ai-context/snapshot.json",
			"source_commit", "same generated-branch commit", "too large for your tools",
			"say why before falling back", "JSON source pack", "not automatic without MCP",
		} {
			if !strings.Contains(body, required) {
				t.Fatalf("%s instructions missing %q", destination, required)
			}
		}
		if destination == Both && strings.Index(body, "Use repository context first") > strings.Index(body, "Read through Zoomies MCP") {
			t.Fatal("repository guidance must come before the optional MCP route")
		}
	}
	config.Destination = Zoomies
	body := AssistantInstructions(key, "owner/repo", config)
	if strings.Contains(body, "/blob/zoomies-ai-context/") || strings.Contains(body, "Use repository context first") {
		t.Fatal("Zoomies-only guidance invents a repository pack")
	}
	if !strings.Contains(body, "publishes no repository context branch") {
		t.Fatal("Zoomies-only guidance must explain how to enable repository output")
	}
}

func TestPreviousAssistantGuidanceUpgradesWithoutOverwritingCustomEdits(t *testing.T) {
	key, config := setupInputs()
	for _, destination := range []Destination{Repository, Both, Zoomies} {
		config.Destination = destination
		config.UploadURL = ""
		if destination == Zoomies {
			config.UploadURL = "https://zoomies.example.com/api/v1/ai-context/uploads"
		}
		for _, newline := range []string{"\n", "\r\n"} {
			prefix := "# My guidance" + newline + "Keep this." + newline + newline
			suffix := newline + "Keep this too." + newline
			old := previousAssistantInstructions(key, "owner/repo", config)
			block := tightManagedBlock(old, newline)
			files := []SetupFile{
				{Path: "AGENTS.md", SHA: strings.Repeat("a", 40), Content: prefix + block + suffix},
				{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: prefix + block + suffix},
			}
			changes, err := PlanSetupFiles(key, "owner/repo", config, files)
			if err != nil {
				t.Fatal(err)
			}
			var saved []SetupFile
			upgraded := 0
			for _, change := range changes {
				saved = append(saved, SetupFile{Path: change.Path, SHA: strings.Repeat("b", 40), Content: change.Content})
				if change.Path == "AGENTS.md" || change.Path == "CLAUDE.md" {
					upgraded++
					want := prefix + managedBlock(AssistantInstructions(key, "owner/repo", config), newline) + suffix
					if change.Content != want || change.PreviousSHA != files[0].SHA {
						t.Fatal("upgrade lost user text, line endings or original blob identity")
					}
				}
			}
			if upgraded != 2 {
				t.Fatal("both managed guidance files must be upgraded")
			}
			retry, err := PlanSetupFiles(key, "owner/repo", config, saved)
			if err != nil || len(retry) != 0 {
				t.Fatalf("upgraded instructions are not idempotent: %v", err)
			}
			files[0].Content = prefix + strings.Replace(block, "Repomix generates context", "Custom instruction", 1) + suffix
			if _, err := PlanSetupFiles(key, "owner/repo", config, files); err == nil {
				t.Fatal("custom edits to previous managed instructions were overwritten")
			}
		}
	}
}

func TestSetupAlwaysAddsTheReadmeBadgeAndPreservesExistingText(t *testing.T) {
	key, config := setupInputs()
	include := false
	config.ReadmeBadge = &include
	changes, err := PlanSetupFiles(key, "owner/repo", config, []SetupFile{{Path: "README.md", SHA: strings.Repeat("a", 40), Content: "# Project\nUser badge stays here.\n"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		if change.Path == "README.md" {
			found = true
			if !strings.Contains(change.Content, "User badge stays here.") || !strings.Contains(change.Content, BadgeMarkdown(key, "owner/repo")) {
				t.Fatal("automatic badge lost user text")
			}
		}
	}
	if !found {
		t.Fatal("automatic README badge missing")
	}
	if !strings.Contains(BadgeMarkdown(key, "owner/repo"), "zoomies-ai-context.yml/badge.svg") {
		t.Fatal("badge uses the wrong workflow")
	}
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

// Prettier puts a blank line between top-level Markdown blocks, so a managed
// section with the markers jammed against the text, or with extra blank lines
// before it, fails the format check of any repository that runs one.
func TestSetupWritesFormatterCleanMarkdown(t *testing.T) {
	key, config := setupInputs()
	cases := map[string]string{
		"trailing newline": "# Guide\n\nText.\n",
		"no newline":       "# Guide\n\nText.",
		"blank lines":      "# Guide\n\nText.\n\n\n",
		"empty":            "",
	}
	for name, original := range cases {
		files := []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: original}}
		if original == "" {
			files[0].SHA = ""
		}
		changes, err := PlanSetupFiles(key, "owner/repo", config, files)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range changes {
			if change.Path != "CLAUDE.md" {
				continue
			}
			got := change.Content
			if strings.Contains(got, managedStart+"\n##") || strings.Contains(got, ".\n"+managedEnd) {
				t.Fatalf("%s: markers touch the text", name)
			}
			if strings.Contains(got, "\n\n\n") && !strings.Contains(original, "\n\n\n") {
				t.Fatalf("%s: setup added more than one blank line", name)
			}
			if !strings.HasSuffix(got, managedEnd+"\n") {
				t.Fatalf("%s: missing final newline", name)
			}
		}
	}
}

func TestBadgeSectionIsFormatterCleanAfterATitle(t *testing.T) {
	key, config := setupInputs()
	for _, original := range []string{"# Project\n\nText.\n", "# Project\nText.\n", "# Project\n"} {
		changes, err := PlanSetupFiles(key, "owner/repo", config, []SetupFile{{Path: "README.md", SHA: strings.Repeat("a", 40), Content: original}})
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range changes {
			if change.Path == "README.md" && (strings.Contains(change.Content, "\n\n\n") || strings.Contains(change.Content, managedStart+"\n[")) {
				t.Fatalf("badge section is not formatter-clean for %q:\n%s", original, change.Content)
			}
		}
	}
}

func TestEarlierTightMarkersUpgradeToFormatterCleanOnes(t *testing.T) {
	key, config := setupInputs()
	body := AssistantInstructions(key, "owner/repo", config)
	files := []SetupFile{{Path: "CLAUDE.md", SHA: strings.Repeat("a", 40), Content: "# X\n\n" + tightManagedBlock(body, "\n") + "\n"}}
	changes, err := PlanSetupFiles(key, "owner/repo", config, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if change.Path == "CLAUDE.md" && change.Content != "# X\n\n"+managedBlock(body, "\n")+"\n" {
			t.Fatal("tight markers were not upgraded")
		}
	}
}
