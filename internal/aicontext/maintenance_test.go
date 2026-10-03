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
