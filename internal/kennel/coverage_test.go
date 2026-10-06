package kennel

import (
	"strings"
	"testing"
)

var allSources = []Source{SourceFleet, SourceMetadata, SourceRuns}

var allStates = []CoverageState{
	CoverageOK, CoveragePartial, CoverageDenied, CoverageUnavailable,
	CoverageHeld, CoverageError, CoverageNotRead,
}

// A page that says a source could not be read says one sentence about it, in the
// house voice, and a source read in full says nothing: a sentence beside a
// healthy source teaches an operator to skim them.
func TestEveryCoverageStateHasASentenceExceptBeingRead(t *testing.T) {
	for _, src := range allSources {
		for _, st := range allStates {
			got := Explain(src, st)
			if st == CoverageOK {
				if got != "" {
					t.Errorf("%s read in full says %q", src, got)
				}
				continue
			}
			if !strings.HasSuffix(got, ".") {
				t.Errorf("%s %s does not end like a sentence: %q", src, st, got)
			}
			if american.MatchString(got) {
				t.Errorf("%s %s uses an American spelling: %q", src, st, got)
			}
			if strings.ContainsAny(got, "—–") {
				t.Errorf("%s %s contains a dash character: %q", src, st, got)
			}
		}
	}
}

// A refusal names the permission to grant, because "denied" alone is a sentence
// somebody has to open the documentation to act on. The permission is the one
// the registry already names, so the page, the grant flow and the documentation
// read one answer.
func TestADeniedSourceNamesThePermissionThatWouldFixIt(t *testing.T) {
	for _, src := range allSources {
		got := Explain(src, CoverageDenied)
		if need := src.Permission(); need != "" && !strings.Contains(got, need) {
			t.Errorf("%s: %q does not name %q", src, got, need)
		}
	}
	// The fleet's own record needs no permission, so a refusal of it says only
	// that: never a sentence with a permission missing from it.
	if got := Explain(SourceFleet, CoverageDenied); got != "GitHub refused the read." {
		t.Errorf("the fleet needs no permission, but its refusal says %q", got)
	}
}

func TestEverySourceHasADistinctLabelAnOperatorCanRead(t *testing.T) {
	seen := map[string]Source{}
	for _, src := range allSources {
		label := src.Label()
		if label == "" || label == "Something else" {
			t.Errorf("%s has no label", src)
		}
		if other, dup := seen[label]; dup {
			t.Errorf("%s and %s are both called %q", src, other, label)
		}
		seen[label] = src
		if american.MatchString(label) || strings.ContainsAny(label, "—–") {
			t.Errorf("label %q breaks the house voice", label)
		}
	}
}
