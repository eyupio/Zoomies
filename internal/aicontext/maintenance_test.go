package aicontext

import (
	"strings"
	"testing"
)

func TestMaintenancePreservesOutsideTextAndRefusesAmbiguousOwnership(t *testing.T) {
	key, config := setupInputs()
	files, err := PlanManagedSetup(key, "owner/repo", config, []SetupFile{{Path: "ReadMe.MD", SHA: strings.Repeat("a", 40), Content: "# Project\nKeep this.\n"}})
	if err != nil {
		t.Fatal(err)
	}
	installed := []SetupFile{}
	for _, f := range files {
		installed = append(installed, SetupFile{Path: f.Path, SHA: strings.Repeat("b", 40), Mode: "100644", Content: f.Content})
	}
	removed, err := PlanMaintenance(key, "owner/repo", config, config, installed, files, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range removed {
		if f.Path == "ReadMe.MD" {
			if f.Delete || !strings.Contains(f.Content, "Keep this.") || strings.Contains(f.Content, "Zoomies AI Context") {
				t.Fatal("README removal destroyed user text or left badge")
			}
		}
	}
	original := installed[0].Content
	installed[0].Content = "custom file"
	if _, err := PlanMaintenance(key, "owner/repo", config, config, installed, nil, true); err == nil {
		t.Fatal("unknown operational file accepted")
	}
	installed[0].Content = original
	for _, text := range []string{managedStart, managedEnd + managedStart, managedStart + managedEnd + managedStart + managedEnd, "\x00"} {
		broken := append([]SetupFile{}, installed...)
		broken = append(broken, SetupFile{Path: "readme.markdown", SHA: strings.Repeat("c", 40), Content: text})
		if _, err := PlanMaintenance(key, "owner/repo", config, config, broken, files, true); err == nil {
			t.Fatal("ambiguous/binary document accepted")
		}
	}
}

// Installed workflows are verified byte for byte, so a template change must
// leave the previous one recognised: otherwise every enabled repository closes
// its access gate until somebody runs a repair.
func TestPreviousWorkflowIsRecognisedAsOlderAndNotAsAnEdit(t *testing.T) {
	key, config := setupInputs()
	current, err := SetupWorkflow(key, config)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := PreviousSetupWorkflow(key, config)
	if err != nil {
		t.Fatal(err)
	}
	if previous == current {
		t.Fatal("the previous template must differ from the current one, or there is nothing to recognise")
	}
	if !IsOlderSetupWorkflow(key, config, previous) {
		t.Fatal("the previous template was not recognised")
	}
	if legacy, _ := LegacySetupWorkflow(key, config); !IsOlderSetupWorkflow(key, config, legacy) {
		t.Fatal("the initial template stopped being recognised")
	}
	for name, content := range map[string]string{"current": current, "edited": previous + "# edited\n", "empty": ""} {
		if IsOlderSetupWorkflow(key, config, content) {
			t.Errorf("%s workflow was treated as an older template", name)
		}
	}
}

func TestRepairUpgradesAPreviousWorkflowInsteadOfRefusingIt(t *testing.T) {
	key, config := setupInputs()
	files, err := PlanManagedSetup(key, "owner/repo", config, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := PreviousSetupWorkflow(key, config)
	installed := []SetupFile{}
	for _, f := range files {
		content := f.Content
		if f.Path == WorkflowPath {
			content = previous
		}
		installed = append(installed, SetupFile{Path: f.Path, SHA: strings.Repeat("b", 40), Mode: "100644", Content: content})
	}
	changes, err := PlanMaintenance(key, "owner/repo", config, config, installed, nil, false)
	if err != nil {
		t.Fatalf("an older Zoomies workflow was refused as unowned: %v", err)
	}
	current, _ := SetupWorkflow(key, config)
	upgraded := false
	for _, c := range changes {
		if c.Path == WorkflowPath {
			upgraded = c.Content == current
		}
	}
	if !upgraded {
		t.Fatal("repair did not move the workflow to the current generator")
	}
}

// Every generation of script a release has shipped must stay recognised, in both
// hosting flavours and for the Zoomies-only workflow, which has no publish job.
func TestEveryOlderWorkflowGenerationStaysRecognisedAndDistinct(t *testing.T) {
	for name, edit := range map[string]func(*RepositoryKey, *Config){
		"github.com": func(*RepositoryKey, *Config) {},
		"enterprise": func(k *RepositoryKey, _ *Config) { k.GitHubHost = "github.example.org" },
		"zoomies-only": func(_ *RepositoryKey, c *Config) {
			c.Destination, c.UploadURL = Zoomies, "https://zoomies.example.org"+UploadPath
		},
		"a custom branch":  func(_ *RepositoryKey, c *Config) { c.SourceBranch = "release/1.x" },
		"custom exclusion": func(_ *RepositoryKey, c *Config) { c.Exclude = append(c.Exclude, "docs/**") },
	} {
		t.Run(name, func(t *testing.T) {
			key, config := setupInputs()
			edit(&key, &config)
			current, err := SetupWorkflow(key, config)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]string{"current": current}
			for i := range olderGenerations {
				older, err := olderSetupWorkflow(key, config, i)
				if err != nil {
					t.Fatal(err)
				}
				label := olderGenerations[i].generate
				for other, w := range seen {
					if w == older {
						t.Errorf("%s is identical to %s: nothing to recognise", label, other)
					}
				}
				seen[label] = older
				if !IsOlderSetupWorkflow(key, config, older) {
					t.Errorf("%s was not recognised", label)
				}
			}
			legacy, err := LegacySetupWorkflow(key, config)
			if err != nil {
				t.Fatal(err)
			}
			if legacy != current && !IsOlderSetupWorkflow(key, config, legacy) {
				t.Error("the initial template stopped being recognised")
			}
			if IsOlderSetupWorkflow(key, config, current) {
				t.Error("the current workflow must not count as older")
			}
		})
	}
}

func TestMaintenanceAmendsAndRemovesTheGuideWhilePreservingCustomText(t *testing.T) {
	key, config := setupInputs()
	changes, err := PlanManagedSetup(key, "owner/repo", config, nil)
	if err != nil {
		t.Fatal(err)
	}
	var installed []SetupFile
	for _, change := range changes {
		content := change.Content
		if change.Path == ContextGuidePath {
			content = "Keep my guide introduction.\n\n" + content
		}
		installed = append(installed, SetupFile{Path: change.Path, Mode: "100644", SHA: strings.Repeat("a", 40), Content: content})
	}
	amended := config
	amended.Destination = Repository
	updated, err := PlanMaintenance(key, "owner/repo", config, amended, installed, changes, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range updated {
		if change.Path == ContextGuidePath {
			found = strings.HasPrefix(change.Content, "Keep my guide introduction.\n\n") && !strings.Contains(change.Content, "context_overview")
		}
	}
	if !found {
		t.Fatal("amendment did not update the guide and preserve custom text")
	}
	removed, err := PlanMaintenance(key, "owner/repo", config, config, installed, changes, true)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, change := range removed {
		if change.Path == ContextGuidePath {
			found = !change.Delete && change.Content == "Keep my guide introduction.\n\n"
		}
	}
	if !found {
		t.Fatal("removal destroyed custom guide text or left managed instructions")
	}
}

func TestRepairMigratesAnOlderSetupToTheSharedGuide(t *testing.T) {
	key, config := setupInputs()
	changes, err := PlanManagedSetup(key, "owner/repo", config, nil)
	if err != nil {
		t.Fatal(err)
	}
	var installed []SetupFile
	for _, change := range changes {
		if change.Path == ContextGuidePath {
			continue
		}
		content := change.Content
		if assistantDocument(change.Path) {
			content = "Keep my instructions.\r\n\r\n" + managedBlock(repositoryFirstAssistantInstructions(key, "owner/repo", config), "\r\n") + "\r\n"
		}
		installed = append(installed, SetupFile{Path: change.Path, Mode: "100644", SHA: strings.Repeat("a", 40), Content: content})
	}
	updated, err := PlanMaintenance(key, "owner/repo", config, config, installed, changes, false)
	if err != nil {
		t.Fatal(err)
	}
	guide, entries := false, 0
	for _, change := range updated {
		if change.Path == ContextGuidePath {
			guide = strings.Contains(change.Content, "manifest.json")
		}
		if assistantDocument(change.Path) {
			if !strings.HasPrefix(change.Content, "Keep my instructions.\r\n\r\n") || !strings.Contains(change.Content, ContextGuidePath) || strings.Contains(change.Content, "snapshot.json") {
				t.Fatal("repair lost custom instructions or retained the long injected prompt")
			}
			entries++
		}
	}
	if !guide || entries != 2 {
		t.Fatal("repair did not migrate the old setup to a shared guide")
	}
}

func TestAmendingAnEntirelyManagedGuidePreservesCRLF(t *testing.T) {
	key, config := setupInputs()
	old := managedBlock(AssistantInstructions(key, "owner/repo", config), "\r\n") + "\r\n"
	amended := config
	amended.Destination = Repository
	changes, err := PlanMaintenance(key, "owner/repo", config, amended, []SetupFile{{Path: ContextGuidePath, Mode: "100644", SHA: strings.Repeat("a", 40), Content: old}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if change.Path == ContextGuidePath {
			if strings.Contains(strings.ReplaceAll(change.Content, "\r\n", ""), "\n") || strings.Contains(change.Content, "context_overview") {
				t.Fatal("guide amendment lost CRLF or retained the old destination")
			}
			return
		}
	}
	t.Fatal("guide amendment missing")
}
