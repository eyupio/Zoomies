package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The documentation names the figures size advice is computed from, and this
// holds the page to the constants: a changed percentile, margin, growth or
// minimum that left the page as it was would have the page lying in the one
// place an operator checks the rule.
func TestTheFiguresSectionNamesTheConstants(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "auto-pools.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	section := between(t, page, "### How the figures are computed", "### What is deliberately not inferred")
	for name, want := range map[string]string{
		"the percentile":      fmt.Sprintf("%.0fth percentile", ProfilePercentile*100),
		"the memory margin":   fractionWords(t, ProfileMemoryMargin-1) + " added",
		"the kill growth":     fractionWords(t, ProfileOOMGrowth) + " times",
		"the minimum":         numberWords(t, AdviceMinRuns) + " measured runs",
		"ProfilePercentile":   "`ProfilePercentile`",
		"ProfileMemoryMargin": "`ProfileMemoryMargin`",
		"ProfileOOMGrowth":    "`ProfileOOMGrowth`",
		"AdviceMinRuns":       "`AdviceMinRuns`",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the figures section never says %s as %q", name, want)
		}
	}
	notInferred := between(t, page, "### What is deliberately not inferred", "\n## ")
	for _, want := range []string{"single run", "pinned", "retention", "cordoned"} {
		if !strings.Contains(notInferred, want) {
			t.Errorf("the section on what is not inferred never mentions %q", want)
		}
	}
}

// between is the text from one heading to the next, so a sentence is held to
// the section it belongs in and not found by chance elsewhere on the page.
func between(t *testing.T, page, from, to string) string {
	t.Helper()
	i := strings.Index(page, from)
	if i < 0 {
		t.Fatalf("docs/auto-pools.md has no %q", from)
	}
	rest := page[i+len(from):]
	if j := strings.Index(rest, to); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// fractionWords is how the prose says a ratio: the page is for people, and
// "a fifth added" is what ProfileMemoryMargin means to one.
func fractionWords(t *testing.T, v float64) string {
	t.Helper()
	words := map[string]string{"0.20": "a fifth", "0.25": "a quarter", "0.50": "half", "1.50": "one and a half", "2.00": "two"}
	w, ok := words[fmt.Sprintf("%.2f", v)]
	if !ok {
		t.Fatalf("no words for %v; teach fractionWords the new constant and write it into the page", v)
	}
	return w
}

func numberWords(t *testing.T, n int) string {
	t.Helper()
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if n < 0 || n >= len(words) {
		t.Fatalf("no words for %d; teach numberWords the new constant and write it into the page", n)
	}
	return words[n]
}
