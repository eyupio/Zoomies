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
