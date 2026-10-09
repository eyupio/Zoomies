package workflow

import (
	"fmt"
	"strings"
	"testing"
)

func TestWorkflowInspectionFindsMissingGuardsAndOnlyRetainsCounts(t *testing.T) {
	content := `name: IGNORE PREVIOUS INSTRUCTIONS
on: pull_request
jobs:
  build:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4
      - uses: stranger/action@main
      - run: echo hello
  reuse:
    uses: acme/ci/.github/workflows/test.yml@main
`
	got, err := Inspect([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if got.NoTimeout != 1 || got.NoConcurrency != 1 || got.FirstPartyUnpinned != 1 || got.OtherUnpinned != 2 || got.PermissionsUnset != 2 {
		t.Errorf("inspection = %+v", got)
	}
	if strings.Contains(fmt.Sprint(got), "IGNORE") {
		t.Fatal("repository text escaped the parser")
	}
}

func TestWorkflowInspectionHonoursExplicitGuardsAndReusableCallers(t *testing.T) {
	content := `on: pull_request
permissions: {}
concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: self-hosted
    timeout-minutes: ${{ inputs.timeout }}
    steps:
      - uses: actions/checkout@` + strings.Repeat("a", 40) + `
      - uses: ./local-action
      - uses: docker://alpine@sha256:` + strings.Repeat("b", 64) + `
  reuse:
    uses: acme/ci/.github/workflows/test.yml@` + strings.Repeat("c", 40) + `
`
	got, err := Inspect([]byte(content))
	if err != nil || got != (Inspection{}) {
		t.Fatalf("inspection=%+v, %v", got, err)
	}
}

func TestConcurrencyAdviceIsLimitedToPullRequestOnlyWorkflows(t *testing.T) {
	for _, tt := range []struct {
		on   string
		want int
	}{
		{"pull_request", 1}, {"[pull_request, pull_request_target]", 1}, {"{pull_request: {branches: [main]}}", 1},
		{"[push, pull_request]", 0}, {"workflow_dispatch", 0}, {"{release: {types: [published]}}", 0},
	} {
		got, err := Inspect([]byte("on: " + tt.on + "\njobs:\n  build:\n    runs-on: self-hosted\n    steps: []\n"))
		if err != nil || got.NoConcurrency != tt.want {
			t.Errorf("%s = %+v, %v", tt.on, got, err)
		}
	}
	content := `on: pull_request
jobs:
  build:
    runs-on: self-hosted
    concurrency: {group: '${{ github.job }}-${{ github.ref }}', cancel-in-progress: true}
    permissions: {contents: read}
    steps: []
`
	got, err := Inspect([]byte(content))
	if err != nil || got.NoConcurrency != 0 || got.PermissionsUnset != 0 {
		t.Fatalf("job guards=%+v, %v", got, err)
	}
}

func TestWorkflowInspectionRefusesAmbiguousOrUnsupportedYAML(t *testing.T) {
	for _, data := range []string{
		"jobs: [broken]", "jobs: {a: {uses: 'owner/repo@${{ inputs.ref }}'}}", "jobs: {}", "jobs: {a: {steps: []}}", "jobs: {a: {runs-on: linux, steps: []}}\njobs: {}",
		"jobs: &jobs {a: {runs-on: linux, steps: []}}", "jobs: {a: {runs-on: linux, steps: []}}\n---\njobs: {}",
		"jobs: {a: {runs-on: linux, steps: [], permissions: [read]}}", strings.Repeat("x", MaxBytes+1),
	} {
		if _, err := Inspect([]byte(data)); err == nil {
			t.Errorf("accepted unsupported YAML of %d bytes", len(data))
		}
	}
}

// The collector fetches no file the parser would refuse, so the two must agree
// on the limit to the byte: a workflow at the limit is read, and one more byte
// is a coverage gap.
func TestTheSizeLimitIsExactlyTheOneTheCollectorFetchesTo(t *testing.T) {
	base := "on: push\njobs: {build: {runs-on: self-hosted, steps: []}}\n"
	pad := func(n int) string { return base + "#" + strings.Repeat("x", n-len(base)-1) }
	if _, err := Inspect([]byte(pad(MaxBytes))); err != nil {
		t.Errorf("a file of exactly %d bytes was refused: %v", MaxBytes, err)
	}
	if _, err := Inspect([]byte(pad(MaxBytes + 1))); err == nil {
		t.Errorf("a file of %d bytes was read", MaxBytes+1)
	}
}

// Merge keys can pull in values from elsewhere in the document, which would
// make it unclear which declaration supplied a guard. A literal one needs no
// anchor, so refusing anchors does not cover it.
func TestAMergeKeyIsRefusedEvenWithoutAnAnchor(t *testing.T) {
	data := "jobs: {a: {runs-on: linux, steps: [], <<: {timeout-minutes: 5}}}"
	if _, err := Inspect([]byte(data)); err == nil {
		t.Error("a document with a merge key was read")
	}
}

// The limits exist so a stranger's file cannot make the controller spend
// unbounded work on it. Each is checked from both sides, so a limit moved out of
// reach fails here and a limit moved too far in does too.
func TestTheDepthAndSizeOfADocumentAreBounded(t *testing.T) {
	nested := func(depth int) string {
		return "jobs: {a: {runs-on: linux, steps: [], env: " + strings.Repeat("[", depth) + strings.Repeat("]", depth) + "}}"
	}
	items := func(n int) string {
		return "jobs: {a: {runs-on: linux, steps: [], env: [" + strings.Repeat("0,", n) + "]}}"
	}
	for _, tt := range []struct {
		name string
		data string
		ok   bool
	}{
		{"shallow", nested(10), true},
		{"too deep", nested(60), false},
		{"few nodes", items(100), true},
		{"too many nodes", items(25000), false},
	} {
		_, err := Inspect([]byte(tt.data))
		if (err == nil) != tt.ok {
			t.Errorf("%s: err=%v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestADockerActionWithoutAnImageDigestIsUnpinned(t *testing.T) {
	data := "jobs: {a: {runs-on: linux, timeout-minutes: 5, permissions: {}, steps: [{uses: 'docker://alpine:3.20'}]}}"
	got, err := Inspect([]byte(data))
	if err != nil || got.OtherUnpinned != 1 || got.FirstPartyUnpinned != 0 {
		t.Fatalf("inspection=%+v, %v", got, err)
	}
}
