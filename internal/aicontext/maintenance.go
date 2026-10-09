package aicontext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Maintenance always returns an explicit diff. It touches only paths already
// managed by Zoomies and preserves text outside the marked document sections.
func PlanMaintenance(key RepositoryKey, name string, previous Config, config Config, files []SetupFile, approved []SetupChange, remove bool) ([]SetupChange, error) {
	owned := map[string]bool{}
	for _, f := range approved {
		owned[f.Path] = true
	}
	known, err := PlanManagedSetup(key, name, previous, nil)
	if err != nil {
		return nil, err
	}
	canonical := map[string]string{}
	for _, f := range known {
		canonical[f.Path] = f.Content
	}
	actual := map[string]SetupFile{}
	var documents []SetupFile
	var changes []SetupChange
	for _, f := range files {
		if _, duplicate := actual[f.Path]; duplicate {
			return nil, fmt.Errorf("duplicate managed path")
		}
		actual[f.Path] = f
		if f.Mode != "" && f.Mode != "100644" && f.Mode != "100755" {
			return nil, fmt.Errorf("managed files must be regular files")
		}
		if len(f.Content) > maxSetupFileBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || f.SHA == "" && f.Content != "" || f.SHA != "" && !commitPattern.MatchString(f.SHA) {
			return nil, fmt.Errorf("managed files need bounded content and valid blob identity")
		}
		switch {
		case f.Path == WorkflowPath || f.Path == ConfigPath || f.Path == GeneratorPackagePath || f.Path == GeneratorLockPath:
			if f.SHA != "" && !owned[f.Path] && f.Content != canonical[f.Path] && !(f.Path == WorkflowPath && IsOlderSetupWorkflow(key, previous, f.Content)) {
				return nil, fmt.Errorf("%s has no recognised Zoomies ownership", f.Path)
			}
			if remove && f.SHA != "" {
				changes = append(changes, SetupChange{Path: f.Path, Mode: f.Mode, PreviousSHA: f.SHA, Delete: true})
			}
		case assistantDocument(f.Path) || f.Path == ContextGuidePath || markdownReadme(f.Path):
			if f.Path == ContextGuidePath && f.SHA != "" && !strings.Contains(f.Content, managedStart) {
				return nil, fmt.Errorf("%s has no recognised Zoomies ownership", f.Path)
			}
			clean, err := removeManagedSection(f.Content)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Path, err)
			}
			if remove {
				if clean != f.Content {
					changes = append(changes, SetupChange{Path: f.Path, Mode: f.Mode, PreviousSHA: f.SHA, Content: clean, Delete: clean == ""})
				}
			} else {
				f.Content = clean
				if f.Path == ContextGuidePath {
					newline := "\n"
					if strings.Contains(actual[f.Path].Content, "\r\n") {
						newline = "\r\n"
					}
					f.Content += blankLineBefore(clean, newline) + managedBlock(AssistantInstructions(key, name, config), newline) + newline
					if f.Content != actual[f.Path].Content {
						changes = append(changes, SetupChange{Path: f.Path, Mode: f.Mode, PreviousSHA: f.SHA, Content: f.Content})
					}
				}
				documents = append(documents, f)
			}
		default:
			return nil, fmt.Errorf("unsupported managed path %s", f.Path)
		}
	}
	if remove {
		return changes, nil
	}
	config.ReadmeBadge = nil
	wanted, err := PlanManagedSetup(key, name, config, documents)
	if err != nil {
		return nil, err
	}
	for _, f := range wanted {
		old := actual[f.Path]
		if old.Content == f.Content {
			continue
		}
		f.PreviousSHA = old.SHA
		if old.Mode != "" {
			f.Mode = old.Mode
		}
		changes = append(changes, f)
	}
	return changes, nil
}

func removeManagedSection(content string) (string, error) {
	starts, ends := strings.Count(content, managedStart), strings.Count(content, managedEnd)
	if starts == 0 && ends == 0 {
		if strings.Contains(content, "<!-- zoomies-ai-context:") {
			return "", fmt.Errorf("repair incomplete ownership markers first")
		}
		return content, nil
	}
	if starts != 1 || ends != 1 {
		return "", fmt.Errorf("repair duplicate or incomplete ownership markers first")
	}
	start, end := strings.Index(content, managedStart), strings.Index(content, managedEnd)
	if end < start {
		return "", fmt.Errorf("repair reversed ownership markers first")
	}
	end += len(managedEnd)
	// The separator following the block belongs to the block; other text,
	// including whitespace before it, is preserved exactly.
	if strings.HasPrefix(content[end:], "\r\n") {
		end += 2
	} else if strings.HasPrefix(content[end:], "\n") {
		end++
	}
	return content[:start] + content[end:], nil
}
