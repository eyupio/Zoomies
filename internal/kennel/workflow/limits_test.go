package workflow

import (
	"strings"
	"testing"
)

// The parser refuses what it cannot judge with bounded work, and a refusal
// is an error the collector turns into an unreadable file: never a panic,
// never a partial judgement.
func TestInspectionRefusesAmbiguousOrUnsupportedYAML(t *testing.T) {
	for _, data := range []string{
		"jobs: [broken]", "jobs: {a: {uses: 'owner/repo@${{ inputs.ref }}'}}", "jobs: {}", "jobs: {a: {steps: []}}",
		"jobs: {a: {runs-on: linux, steps: []}}\njobs: {}",
		"jobs: &jobs {a: {runs-on: linux, steps: []}}", "jobs: {a: {runs-on: linux, steps: []}}\n---\njobs: {}",
		"jobs: {a: {runs-on: linux, steps: [], permissions: [read]}}", strings.Repeat("x", MaxBytes+1),
		"", "on: push\njobs: {a: {runs-on: x, steps: []}}\n# \xff\xfe",
	} {
		if _, err := Inspect([]byte(data)); err == nil {
			t.Errorf("accepted unsupported YAML of %d bytes: %.40q", len(data), data)
		}
	}
}

func TestDeepNestingIsRefusedWithoutExhaustingTheStack(t *testing.T) {
	data := "jobs:\n  a:\n    runs-on: x\n    steps: []\n    deep: " + strings.Repeat("[", 400) + strings.Repeat("]", 400) + "\n"
	if _, err := Inspect([]byte(data)); err == nil {
		t.Fatal("accepted 400 levels of nesting")
	}
}
