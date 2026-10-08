package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/controller"
)

// The page that says what the explanation's words mean is held to the code that
// says them, in both directions. A class the page does not describe is a word a
// reader meets with no way to find out what it means; a class the page describes
// that the code no longer says is a promise about something that cannot happen.
func TestThePageOnJobExplanationsDescribesEveryWordAndOnlyThose(t *testing.T) {
	raw, err := os.ReadFile("../../docs/why.md")
	if err != nil {
		t.Fatalf("reading docs/why.md: %v", err)
	}
	page := string(raw)

	// A table row that opens with a word in backticks describes that word.
	described := func(section string) map[string]bool {
		t.Helper()
		_, rest, ok := strings.Cut(page, "\n"+section+"\n")
		if !ok {
			t.Fatalf("docs/why.md has no %q section", section)
		}
		if i := strings.Index(rest, "\n## "); i >= 0 {
			rest = rest[:i]
		}
		if i := strings.Index(rest, "\n### "); i >= 0 {
			rest = rest[:i]
		}
		words := map[string]bool{}
		for _, line := range strings.Split(rest, "\n") {
			if strings.HasPrefix(line, "| `") {
				if word, _, ok := strings.Cut(strings.TrimPrefix(line, "| `"), "`"); ok {
					words[word] = true
				}
			}
		}
		return words
	}

	var classes []string
	for _, c := range controller.JobClasses() {
		classes = append(classes, string(c))
	}
	for _, c := range []struct {
		section string
		code    []string
	}{
		{"## The class", classes},
		{"## The evidence", controller.EvidenceKinds()},
		{"## What to do next", []string{string(controller.StepRead), string(controller.StepChange), string(controller.StepRerun)}},
	} {
		got := described(c.section)
		for _, word := range c.code {
			if !got[word] {
				t.Errorf("docs/why.md does not describe %q under %q", word, c.section)
			}
			delete(got, word)
		}
		for word := range got {
			t.Errorf("docs/why.md describes %q under %q, which the code does not say", word, c.section)
		}
	}

	for _, level := range []controller.Confidence{controller.ConfidenceHigh, controller.ConfidenceMedium, controller.ConfidenceLow} {
		if !strings.Contains(page, "* **`"+string(level)+"`**") {
			t.Errorf("docs/why.md does not describe the %q confidence level", level)
		}
	}
}
