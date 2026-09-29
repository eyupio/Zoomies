package migrate

import (
	"slices"
	"strings"
	"testing"
)

// A script is text, not workflow structure. A job that generates or lints
// workflow files carries a line that looks exactly like a runs-on key inside a
// `run: |` body, and the migration used to rewrite it: the pull request then
// changed what the script prints, and the job was counted as a rewrite. The
// package promises to touch only what it can be sure is a real runs-on.
const scriptWithRunsOnInside = `name: ci
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - name: Write a workflow
        run: |
          cat > .github/workflows/x.yml <<EOF
          jobs:
            fake:
              runs-on: ubuntu-latest
          EOF

          echo done
      - name: Folded
        run: >-
          runs-on: ubuntu-22.04
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/github-script@v7
        with:
          script: |
            const y = ` + "`" + `
            runs-on: ubuntu-latest
            ` + "`" + `
`

func TestFileLeavesRunsOnTextInsideABlockScalarAlone(t *testing.T) {
	got := File(scriptWithRunsOnInside, zoomies)
	if len(got.Rewrites) != 2 {
		t.Fatalf("rewrites = %+v, want only the two real keys (lint and test)", got.Rewrites)
	}
	for _, rw := range got.Rewrites {
		if rw.Job != "lint" && rw.Job != "test" {
			t.Errorf("unexpected rewrite %+v", rw)
		}
	}
	before, after := strings.Split(scriptWithRunsOnInside, "\n"), strings.Split(got.Content, "\n")
	if len(before) != len(after) {
		t.Fatalf("the line count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] && i+1 != 4 && i+1 != 19 {
			t.Errorf("line %d changed but is inside a script: %q -> %q", i+1, before[i], after[i])
		}
	}
}

func TestHostedLabelsInIgnoresRunsOnTextInsideABlockScalar(t *testing.T) {
	in := `jobs:
  a:
    runs-on: self-hosted
    steps:
      - run: |
          runs-on: ubuntu-latest
`
	if got := HostedLabelsIn(in); len(got) != 0 {
		t.Errorf("HostedLabelsIn = %v, want none: the only hosted label is inside a script", got)
	}
	if UsesAnyLabel(in, map[string]bool{"ubuntu-latest": true}) {
		t.Error("UsesAnyLabel counted a label that only appears inside a script")
	}
}

func TestARealRunsOnAfterABlockScalarIsStillRewritten(t *testing.T) {
	in := `jobs:
  a:
    steps:
      - run: |
          echo hi
    runs-on: ubuntu-latest
  b:
    steps:
      - run: echo "a |"
    runs-on: ubuntu-22.04
`
	got := File(in, zoomies)
	if len(got.Rewrites) != 2 {
		t.Fatalf("rewrites = %+v, want both", got.Rewrites)
	}
	if !slices.Equal(HostedLabelsIn(in), []string{"ubuntu-latest", "ubuntu-22.04"}) {
		t.Errorf("HostedLabelsIn = %v", HostedLabelsIn(in))
	}
}
