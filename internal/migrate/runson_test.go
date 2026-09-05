package migrate

import (
	"strings"
	"testing"
)

// zoomies is the mapping a fleet with one Linux pool would produce.
var zoomies = Mapping{Labels: map[string]string{
	"ubuntu-latest": "zoomies-linux-x64",
	"ubuntu-22.04":  "zoomies-linux-x64",
	"ubuntu-24.04":  "zoomies-linux-x64",
}}

func TestFileRewritesTheCommonForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		job  string
	}{
		{
			name: "a scalar value",
			in:   "jobs:\n  build:\n    runs-on: ubuntu-latest\n",
			want: "jobs:\n  build:\n    runs-on: zoomies-linux-x64\n",
			job:  "build",
		},
		{
			name: "a single-item flow sequence",
			in:   "jobs:\n  test:\n    runs-on: [ubuntu-latest]\n",
			want: "jobs:\n  test:\n    runs-on: zoomies-linux-x64\n",
			job:  "test",
		},
		{
			name: "a quoted value",
			in:   "jobs:\n  test:\n    runs-on: \"ubuntu-22.04\"\n",
			want: "jobs:\n  test:\n    runs-on: zoomies-linux-x64\n",
			job:  "test",
		},
		{
			name: "a block sequence collapses",
			in:   "jobs:\n  lint:\n    runs-on:\n      - ubuntu-latest\n    steps: []\n",
			want: "jobs:\n  lint:\n    runs-on: zoomies-linux-x64\n    steps: []\n",
			job:  "lint",
		},
		{
			name: "a trailing comment survives",
			in:   "jobs:\n  build:\n    runs-on: ubuntu-latest # the cheap one\n",
			want: "jobs:\n  build:\n    runs-on: zoomies-linux-x64 # the cheap one\n",
			job:  "build",
		},
		{
			name: "an unusual key case",
			in:   "jobs:\n  build:\n    Runs-On: ubuntu-latest\n",
			want: "jobs:\n  build:\n    Runs-On: zoomies-linux-x64\n",
			job:  "build",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := File(c.in, zoomies)
			if got.Content != c.want {
				t.Fatalf("content =\n%q\nwant\n%q", got.Content, c.want)
			}
			if len(got.Rewrites) != 1 {
				t.Fatalf("rewrites = %+v, want exactly one", got.Rewrites)
			}
			if got.Rewrites[0].Job != c.job {
				t.Errorf("job = %q, want %q", got.Rewrites[0].Job, c.job)
			}
			if got.Rewrites[0].To != "zoomies-linux-x64" {
				t.Errorf("to = %q, want the mapped label", got.Rewrites[0].To)
			}
			if len(got.Skips) != 0 {
				t.Errorf("skips = %+v, want none", got.Skips)
			}
		})
	}
}

func TestFileLeavesEverythingElseByteForByte(t *testing.T) {
	in := `# Continuous integration.
#
# Kept deliberately boring.
name: CI

on:
  push:
    branches: [main]        # only main
  pull_request:

env:
  CGO_ENABLED: "0"

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Build
        run: |
          make build
          make test
  release:
    needs: [build]
    if: startsWith(github.ref, 'refs/tags/')
    runs-on: ubuntu-22.04
    steps:
      - run: echo "runs-on: not-a-key"
`
	got := File(in, zoomies)
	if len(got.Rewrites) != 2 {
		t.Fatalf("rewrites = %+v, want two", got.Rewrites)
	}
	// Only the two runs-on lines may differ.
	before, after := strings.Split(in, "\n"), strings.Split(got.Content, "\n")
	if len(before) != len(after) {
		t.Fatalf("the rewrite changed the line count: %d -> %d", len(before), len(after))
	}
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			changed++
			if !strings.Contains(after[i], "zoomies-linux-x64") {
				t.Errorf("line %d changed to something unexpected: %q", i+1, after[i])
			}
		}
	}
	if changed != 2 {
		t.Fatalf("%d lines changed, want exactly the two runs-on lines", changed)
	}
	// A step that merely mentions runs-on in a string is not a runs-on.
	if !strings.Contains(got.Content, `- run: echo "runs-on: not-a-key"`) {
		t.Error("a run: line that mentions runs-on was rewritten")
	}
}

func TestFileSkipsWhatItCannotBeSureOf(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantReason string
	}{
		{
			name:       "a matrix expression",
			in:         "jobs:\n  build:\n    runs-on: ${{ matrix.os }}\n",
			wantReason: "expression",
		},
		{
			name:       "already self-hosted",
			in:         "jobs:\n  build:\n    runs-on: [self-hosted, linux, x64]\n",
			wantReason: "already runs on a self-hosted runner",
		},
		{
			name:       "a label the organisation invented",
			in:         "jobs:\n  build:\n    runs-on: acme-bigbox\n",
			wantReason: "already pointed somewhere deliberate",
		},
		{
			name:       "a hosted label nobody mapped",
			in:         "jobs:\n  build:\n    runs-on: windows-latest\n",
			wantReason: `"windows-latest" is not mapped to a pool`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := File(c.in, zoomies)
			if got.Content != c.in {
				t.Fatalf("a skipped file was modified:\n%q", got.Content)
			}
			if got.Changed() {
				t.Fatal("Changed() is true for a file nothing was rewritten in")
			}
			if len(got.Skips) != 1 {
				t.Fatalf("skips = %+v, want exactly one", got.Skips)
			}
			if !strings.Contains(got.Skips[0].Reason, c.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", got.Skips[0].Reason, c.wantReason)
			}
			if got.Skips[0].Job != "build" {
				t.Errorf("job = %q, want build", got.Skips[0].Job)
			}
		})
	}
}

// A file with no mapping at all must be left completely alone: this is the
// state the wizard is in before the operator has chosen anything.
func TestFileWithAnEmptyMappingChangesNothing(t *testing.T) {
	in := "jobs:\n  build:\n    runs-on: ubuntu-latest\n"
	got := File(in, Mapping{})
	if got.Content != in || got.Changed() {
		t.Fatalf("an empty mapping rewrote the file:\n%q", got.Content)
	}
	if len(got.Skips) != 1 || !strings.Contains(got.Skips[0].Reason, "not mapped") {
		t.Fatalf("skips = %+v, want one that says the label is unmapped", got.Skips)
	}
}

func TestFileKeepsCRLFAndAMissingFinalNewline(t *testing.T) {
	in := "jobs:\r\n  build:\r\n    runs-on: ubuntu-latest"
	got := File(in, zoomies)
	want := "jobs:\r\n  build:\r\n    runs-on: zoomies-linux-x64"
	if got.Content != want {
		t.Fatalf("content = %q, want %q", got.Content, want)
	}
}

func TestFileRewritesEveryJob(t *testing.T) {
	in := `jobs:
  build:
    runs-on: ubuntu-latest
  test:
    runs-on: ubuntu-24.04
  windows:
    runs-on: windows-latest
`
	got := File(in, zoomies)
	if len(got.Rewrites) != 2 {
		t.Fatalf("rewrites = %+v, want the two Ubuntu jobs", got.Rewrites)
	}
	jobs := []string{got.Rewrites[0].Job, got.Rewrites[1].Job}
	if jobs[0] != "build" || jobs[1] != "test" {
		t.Errorf("jobs = %v, want [build test]", jobs)
	}
	if len(got.Skips) != 1 || got.Skips[0].Job != "windows" {
		t.Errorf("skips = %+v, want the unmapped windows job", got.Skips)
	}
}

// A `runs-on` inside a composite action or a reusable workflow sits at a
// different depth. The job name may be wrong there, but the rewrite must not
// be.
func TestFileRewritesOutsideAJobsBlock(t *testing.T) {
	in := "on:\n  workflow_call:\njobs:\n  call:\n    uses: ./.github/workflows/inner.yml\n"
	got := File(in, zoomies)
	if got.Changed() {
		t.Fatalf("nothing to rewrite, but got %+v", got.Rewrites)
	}
}

func TestHostedLabelsIn(t *testing.T) {
	in := `jobs:
  a:
    runs-on: ubuntu-latest
  b:
    runs-on: [macos-14]
  c:
    runs-on:
      - windows-latest
  d:
    runs-on: ubuntu-latest
  e:
    runs-on: [self-hosted, linux]
  f:
    runs-on: ${{ matrix.os }}
`
	got := HostedLabelsIn(in)
	want := []string{"ubuntu-latest", "macos-14", "windows-latest"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("HostedLabelsIn = %v, want %v", got, want)
	}
}

func TestIsHostedLabel(t *testing.T) {
	hosted := []string{"ubuntu-latest", "ubuntu-22.04", "ubuntu-24.04-arm", "windows-2022", "macos-14", "macOS-latest", "ubuntu-latest-4-cores"}
	for _, l := range hosted {
		if !IsHostedLabel(l) {
			t.Errorf("IsHostedLabel(%q) = false, want true", l)
		}
	}
	notHosted := []string{"", "self-hosted", "linux", "x64", "zoomies-linux-x64", "gpu", "buildjet-4vcpu-ubuntu-2204"}
	for _, l := range notHosted {
		if IsHostedLabel(l) {
			t.Errorf("IsHostedLabel(%q) = true, want false", l)
		}
	}
}

func TestSplitFlowItemsKeepsQuotedCommas(t *testing.T) {
	got := splitFlowItems(`ubuntu-latest, "a,b", 'c'`)
	want := []string{"ubuntu-latest", `"a,b"`, `'c'`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("splitFlowItems = %v, want %v", got, want)
	}
}

func TestEmptyFile(t *testing.T) {
	got := File("", zoomies)
	if got.Content != "" || got.Changed() {
		t.Fatalf("an empty file produced %+v", got)
	}
}

// A repository on a hosted-runner vendor is the one this fleet most wants to
// take over, so its labels have to be migratable. Before this, every one of
// them was read as "already pointed somewhere deliberate" and an operator was
// shown a wizard with nothing in it and no reason why.
func TestFileRewritesVendorHostedLabels(t *testing.T) {
	m := Mapping{Labels: map[string]string{
		"blacksmith-4vcpu-ubuntu-2404":  "zoomies-linux-x64",
		"warp-ubuntu-latest-x64-4x":     "zoomies-linux-x64",
		"nscloud-ubuntu-22.04-amd64-4x": "zoomies-linux-x64",
	}}
	for _, label := range []string{"blacksmith-4vcpu-ubuntu-2404", "warp-ubuntu-latest-x64-4x", "nscloud-ubuntu-22.04-amd64-4x"} {
		t.Run(label, func(t *testing.T) {
			got := File("jobs:\n  build:\n    runs-on: "+label+"\n", m)
			if !got.Changed() {
				t.Fatalf("%q was not rewritten; skips = %+v", label, got.Skips)
			}
			if !strings.Contains(got.Content, "runs-on: zoomies-linux-x64") {
				t.Fatalf("content = %q", got.Content)
			}
		})
	}
}

// The mapping step is built from this, so a vendor label has to reach it or an
// operator has nothing to map.
func TestHostedLabelsInFindsVendorLabels(t *testing.T) {
	in := "jobs:\n  a:\n    runs-on: blacksmith-4vcpu-ubuntu-2404\n  b:\n    runs-on: ubuntu-latest\n  c:\n    runs-on: acme-bigbox\n"
	got := HostedLabelsIn(in)
	want := []string{"blacksmith-4vcpu-ubuntu-2404", "ubuntu-latest"}
	if len(got) != len(want) {
		t.Fatalf("HostedLabelsIn = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HostedLabelsIn = %v, want %v", got, want)
		}
	}
}

func TestIsManagedLabelSeparatesRentedFromOurs(t *testing.T) {
	rented := []string{"ubuntu-latest", "macOS-13", "blacksmith", "blacksmith-8vcpu-ubuntu-2404",
		"buildjet-4vcpu-ubuntu-2204", "warp-ubuntu-latest-x64-2x", "namespace-profile-default",
		"nscloud-ubuntu-22.04-amd64-4x", "depot-ubuntu-24.04", "ubicloud-standard-4"}
	for _, l := range rented {
		if !IsManagedLabel(l) {
			t.Errorf("IsManagedLabel(%q) = false, want true", l)
		}
	}
	ours := []string{"self-hosted", "zoomies-linux-x64", "acme-bigbox", "linux", "x64", ""}
	for _, l := range ours {
		if IsManagedLabel(l) {
			t.Errorf("IsManagedLabel(%q) = true, want false", l)
		}
	}
}
