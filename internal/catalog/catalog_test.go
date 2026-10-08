package catalog

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/kennel"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/problem-codes.md")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func byID(entries []Entry) map[string]Entry {
	out := map[string]Entry{}
	for _, e := range entries {
		out[e.ID] = e
	}
	return out
}

// A problem code is documented once, in one table, and the status-page table
// repeats some of them with a second sentence. The catalog must hold each code
// once, or an agent that counts entries would count the same problem twice.
func TestEveryDocumentedTableRowBecomesOneEntry(t *testing.T) {
	entries, err := ParseProblemTables(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	want := []string{"bind.empty", "host.unhealthy", "jobs.label_advice", "jobs.unmatched", "provider.unreachable", "security.disable_auth"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	m := byID(entries)
	if m["bind.empty"].Setting != "server.bind" {
		t.Errorf("config row setting = %q", m["bind.empty"].Setting)
	}
	if m["host.unhealthy"].Setting != "" {
		t.Errorf("runtime row has a setting: %q", m["host.unhealthy"].Setting)
	}
	if m["host.unhealthy"].StatusSentence == nil || *m["host.unhealthy"].StatusSentence != "A runner host has stopped answering." {
		t.Errorf("status sentence not attached: %v", m["host.unhealthy"].StatusSentence)
	}
	if m["bind.empty"].StatusSentence != nil {
		t.Errorf("a code the status page never shows got a sentence")
	}
	// An escaped pipe inside a cell is part of the sentence, not a column.
	if !strings.Contains(m["jobs.unmatched"].Detects, "will run | or a pool") {
		t.Errorf("escaped pipe lost: %q", m["jobs.unmatched"].Detects)
	}
	if m["provider.unreachable"].Fix != "Check the address." || m["provider.unreachable"].Detects != "The provider's API did not answer." {
		t.Errorf("four-column runtime row: detects=%q fix=%q", m["provider.unreachable"].Detects, m["provider.unreachable"].Fix)
	}
	if m["jobs.unmatched"].Verify == nil || *m["jobs.unmatched"].Verify != "`zoomies jobs list --unmatched` is empty." {
		t.Errorf("verify column not read: %v", m["jobs.unmatched"].Verify)
	}
	if m["host.unhealthy"].Verify != nil {
		t.Errorf("a table with no verify column must give nil, got %q", *m["host.unhealthy"].Verify)
	}
}

// A few severities are sentences, because the code changes severity with the
// circumstances. The catalog keeps the sentence rather than guessing one of its
// halves, so it never claims a severity the page does not.
func TestAProseSeverityCellIsKeptVerbatim(t *testing.T) {
	entries, err := ParseProblemTables(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	m := byID(entries)
	if got := m["host.unhealthy"].Severity; got != "error with runners on it, warning without" {
		t.Errorf("severity = %q", got)
	}
	if got := m["bind.empty"].Severity; got != "error" {
		t.Errorf("severity = %q", got)
	}
}

func TestCategoriesComeFromTheSectionAndTheOverrides(t *testing.T) {
	entries, err := ParseProblemTables(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	m := byID(entries)
	for id, want := range map[string][2]string{
		"bind.empty":            {"configuration", "static"},
		"host.unhealthy":        {"reliability", "runtime"},
		"security.disable_auth": {"security", "static"},
		"jobs.label_advice":     {"cost", "runtime"},
		"jobs.unmatched":        {"capacity", "runtime"},
		"provider.unreachable":  {"reliability", "runtime"},
	} {
		if string(m[id].Category) != want[0] || string(m[id].Detection) != want[1] {
			t.Errorf("%s: category/detection = %s/%s, want %s/%s", id, m[id].Category, m[id].Detection, want[0], want[1])
		}
	}
	if got := m["host.unhealthy"].DocsHTML; got != "https://zoomies.sh/problem-codes/#runtime-hosts-and-installations" {
		t.Errorf("docs_html = %q", got)
	}
	if got := m["host.unhealthy"].DocsMD; got != "https://github.com/eyupio/zoomies/blob/main/docs/problem-codes.md#runtime-hosts-and-installations" {
		t.Errorf("docs_md = %q", got)
	}
}

func TestChecksAreEntriesWithTheRegistrysWords(t *testing.T) {
	c, err := Build(fixture(t), kennel.Checks(), "")
	if err != nil {
		t.Fatal(err)
	}
	m := byID(c.Entries)
	for _, k := range kennel.Checks() {
		e, ok := m[string(k.Code)]
		if !ok {
			t.Errorf("%s: no entry", k.Code)
			continue
		}
		if e.Kind != "check" || e.Area != string(k.Area) || e.Detects != k.Detects || e.Severity != string(k.Severity) {
			t.Errorf("%s: %+v", k.Code, e)
		}
		if !strings.HasSuffix(e.DocsHTML, "kennel-club/#checks") || !strings.HasSuffix(e.DocsMD, "kennel-club.md#checks") {
			t.Errorf("%s: docs = %q %q", k.Code, e.DocsHTML, e.DocsMD)
		}
		wantDetection := "runtime"
		if k.Area == kennel.AreaSetup || k.Area == kennel.AreaCI || k.Area == kennel.AreaToken {
			wantDetection = "static"
		}
		if string(e.Detection) != wantDetection {
			t.Errorf("%s: detection = %s", k.Code, e.Detection)
		}
	}
	if c.Version != 1 {
		t.Errorf("version = %d", c.Version)
	}
}

func TestEntriesAreSortedAndIDsUnique(t *testing.T) {
	c, err := Build(fixture(t), kennel.Checks(), "abc1234")
	if err != nil {
		t.Fatal(err)
	}
	if c.GeneratedFrom != "abc1234" {
		t.Errorf("generated_from = %q", c.GeneratedFrom)
	}
	seen := map[string]bool{}
	for i, e := range c.Entries {
		if seen[e.ID] {
			t.Errorf("%s twice", e.ID)
		}
		seen[e.ID] = true
		if i > 0 {
			p := c.Entries[i-1]
			rank := func(k Kind) int {
				if k == KindProblem {
					return 0
				}
				return 1
			}
			if rank(p.Kind) > rank(e.Kind) || (p.Kind == e.Kind && p.ID >= e.ID) {
				t.Errorf("not sorted at %d: %s/%s after %s/%s", i, e.Kind, e.ID, p.Kind, p.ID)
			}
		}
	}
	// Checks sort after problems, so the human-facing codes lead the file.
	if c.Entries[0].Kind != "problem" || c.Entries[len(c.Entries)-1].Kind != "check" {
		t.Errorf("kinds not ordered problem then check")
	}
}
