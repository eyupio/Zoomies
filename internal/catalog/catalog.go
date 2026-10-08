// Package catalog is the one machine-readable list of everything Zoomies can
// say is wrong: every problem code the validator and the controller raise, and
// every check Kennel Club makes, with what each means, what to do and how to
// see that it worked.
//
// It is built from the two places those facts are already written down -- the
// tables in docs/problem-codes.md, which a test keeps in step with the code
// that raises each code, and the Kennel registry -- rather than from a third
// copy. A problem code's facts are prose for a person first; this package
// reads that prose into a shape an agent can fetch once and quote from.
package catalog

import (
	"errors"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/kennel"
)

// Version is the catalog document's own version, bumped when a field changes
// meaning. Adding an entry is not a version change: the entries are the point.
const Version = 1

// Kind says which family an entry belongs to.
type Kind string

const (
	KindProblem Kind = "problem"
	KindCheck   Kind = "check"
)

// Category is what an entry is about, for an agent choosing what to read first.
type Category string

const (
	CategoryCapacity      Category = "capacity"
	CategoryReliability   Category = "reliability"
	CategorySecurity      Category = "security"
	CategoryConfiguration Category = "configuration"
	CategoryCost          Category = "cost"
)

// Detection says whether an entry is found by reading configuration or files
// (static) or by watching the fleet run (runtime).
type Detection string

const (
	DetectionStatic  Detection = "static"
	DetectionRuntime Detection = "runtime"
)

// Entry is one problem code or one check.
type Entry struct {
	ID       string   `json:"id"`
	Kind     Kind     `json:"kind"`
	Title    string   `json:"title"`
	Category Category `json:"category"`
	// Severity is the documented cell verbatim, lower-cased. A few are
	// sentences, because the code's severity depends on the circumstances, and
	// the catalog repeats the sentence rather than choosing one of its halves.
	Severity  string    `json:"severity"`
	Detection Detection `json:"detection"`
	// Detects is what the entry means: the table's sentence, or a check's.
	Detects string `json:"detects"`
	// Fix is what to do. For a problem documented with one sentence that both
	// explains and advises, it is that sentence again.
	Fix string `json:"fix"`
	// Verify is how to see the fix worked, or null where the page has no such
	// column yet. Null is honest; an invented sentence would not be.
	Verify         *string `json:"verify"`
	StatusSentence *string `json:"status_sentence,omitempty"`
	// Area is a check's registry area; Setting is a configuration code's key.
	Area     string `json:"area,omitempty"`
	Setting  string `json:"setting,omitempty"`
	DocsHTML string `json:"docs_html"`
	DocsMD   string `json:"docs_md"`
}

// Catalog is the whole document.
type Catalog struct {
	Version       int     `json:"version"`
	GeneratedFrom string  `json:"generated_from"`
	Entries       []Entry `json:"entries"`
}

const (
	siteBase = "https://zoomies.sh"
	repoBase = "https://github.com/eyupio/zoomies/blob/main/docs"
)

// Build reads the problem-codes page and the Kennel registry into one catalog.
// commit is recorded as generated_from and may be empty.
func Build(problemsMarkdown []byte, checks []kennel.Check, commit string) (*Catalog, error) {
	entries, err := ParseProblemTables(problemsMarkdown)
	if err != nil {
		return nil, err
	}
	for _, k := range checks {
		entries = append(entries, checkEntry(k))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			// Problems first: they are the codes a person meets most.
			return entries[i].Kind == KindProblem
		}
		return entries[i].ID < entries[j].ID
	})
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ID] {
			return nil, errors.New("catalog: " + e.ID + " appears twice")
		}
		seen[e.ID] = true
	}
	return &Catalog{Version: Version, GeneratedFrom: commit, Entries: entries}, nil
}

func checkEntry(k kennel.Check) Entry {
	detection := DetectionRuntime
	switch k.Area {
	case kennel.AreaSetup, kennel.AreaCI, kennel.AreaToken:
		detection = DetectionStatic
	}
	return Entry{
		ID:        string(k.Code),
		Kind:      KindCheck,
		Title:     k.Detects,
		Category:  checkCategory(k),
		Severity:  string(k.Severity),
		Detection: detection,
		Detects:   k.Detects,
		Fix:       k.Fix,
		Verify:    nonEmpty(k.Verify),
		Area:      string(k.Area),
		DocsHTML:  siteBase + "/kennel-club/#checks",
		DocsMD:    repoBase + "/kennel-club.md#checks",
	}
}

func checkCategory(k kennel.Check) Category {
	switch k.Area {
	case kennel.AreaExposure, kennel.AreaToken:
		return CategorySecurity
	case kennel.AreaCapacity:
		return CategoryCapacity
	case kennel.AreaCI:
		return CategoryCost
	}
	return CategoryConfiguration
}

func nonEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
