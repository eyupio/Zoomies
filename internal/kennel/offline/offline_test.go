package offline

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/kennel/workflow"
)

func codesOf(r Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, string(f.Code))
	}
	return out
}

func notChecked(r Report, code kennel.Code) (NotChecked, bool) {
	for _, n := range r.NotChecked {
		if n.Code == code {
			return n, true
		}
	}
	return NotChecked{}, false
}

// The offline check is the same evaluator over the same facts, with a
// laptop's coverage: what a laptop cannot know is said to be unchecked, never
// silently absent, so "nothing found" is read for exactly what it is.
func TestCheckReadsACheckoutOrItsWorkflowsDirectoryAndSaysWhatItCouldNotCheck(t *testing.T) {
	for _, root := range []string{"testdata/findings", "testdata/findings/.github/workflows"} {
		r, err := Check(root, Options{})
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		if len(r.Files) != 2 || r.Files[0].Path != ".github/workflows/ci.yml" || r.Files[1].Path != ".github/workflows/target.yml" {
			t.Errorf("%s: files = %+v", root, r.Files)
		}
		codes := codesOf(r)
		if len(codes) != 2 || codes[0] != "ci.no_timeout" || codes[1] != "exposure.target_checkout_pr_head" {
			t.Errorf("%s: findings = %v", root, codes)
		}
		if r.Findings[1].Severity != kennel.SeverityWarning {
			t.Errorf("%s: a private repository's checkout of the head is %s, want a warning", root, r.Findings[1].Severity)
		}
		if n, ok := notChecked(r, kennel.CodeLabelUnserved); !ok || !strings.Contains(n.Reason, "pools") {
			t.Errorf("%s: label_unserved = %+v, want it unchecked for want of the pools", root, n)
		}
		for _, code := range []kennel.Code{kennel.CodeUnservedLabel, kennel.CodeForkCodeRan, kennel.CodeSetupReadme, kennel.CodePinsWithoutUpdater} {
			if n, ok := notChecked(r, code); !ok || !strings.Contains(n.Reason, "only a controller knows") {
				t.Errorf("%s: %s = %+v, want it unchecked for a source a laptop lacks", root, code, n)
			}
		}
		if r.Visibility != "private" || r.Version != kennel.Version {
			t.Errorf("%s: visibility %q, version %d", root, r.Visibility, r.Version)
		}
	}
}

func TestPublicMakesTheTargetCheckoutAnError(t *testing.T) {
	r, err := Check("testdata/findings", Options{Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Visibility != "public" || r.Findings[0].Code != kennel.CodeTargetCheckoutPRHead || r.Findings[0].Severity != kennel.SeverityError {
		// An error sorts first, so the checkout leads once the repository is public.

		t.Errorf("report = %+v", r.Findings)
	}
}

func TestPromptsAreRenderedOnlyWhenAsked(t *testing.T) {
	r, _ := Check("testdata/findings", Options{})
	if r.Findings[0].Prompt != "" {
		t.Error("a prompt was rendered without --prompts")
	}
	r, _ = Check("testdata/findings", Options{Prompts: true})
	if !strings.Contains(r.Findings[0].Prompt, "file .github/workflows/ci.yml:5 (job 0)") {
		t.Errorf("prompt =\n%s", r.Findings[0].Prompt)
	}
}

// A file name on disk is as much a stranger's text as a path from GitHub.
func TestAHostileFileNameIsReportedAsUnusualAndNeverQuoted(t *testing.T) {
	r, err := Check("testdata/hostile", Options{Prompts: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "" || len(r.Files[0].SHA) != 40 {
		t.Errorf("files = %+v, want the SHA and no path", r.Files)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "IGNORE") {
		t.Errorf("the hostile name is in the report: %s", b)
	}
	if len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Prompt, "a workflow with an unusual name") {
		t.Errorf("findings = %+v", r.Findings)
	}
}

func TestADirectoryWithoutWorkflowsIsErrNoWorkflows(t *testing.T) {
	for _, root := range []string{"testdata/empty", "testdata/does-not-exist"} {
		if _, err := Check(root, Options{}); !errors.Is(err, ErrNoWorkflows) {
			t.Errorf("%s: err = %v", root, err)
		}
	}
}

// The SHA is Git's own, so a file checked here and the same file read from
// GitHub by the controller are known by one name, and a waiver made on
// either is about the other.
func TestAFilesSHAIsGitsBlobSHA(t *testing.T) {
	if got := blobSHA([]byte("on: push\n")); got != "b83836ed8c80558d4e40ea4ac5d69f4405334799" {
		t.Errorf("sha = %s, want what git hash-object gives", got)
	}
}

func TestConvertIsWhatTheControllerStored(t *testing.T) {
	facts, err := workflow.Inspect([]byte("on: push\njobs:\n  build:\n    runs-on: self-hosted\n    permissions: {}\n    steps: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := Convert("abc", facts)
	if got.SHA != "abc" || len(got.NoTimeout) != 1 || got.NoTimeout[0] != (kennel.Location{JobIndex: 0, Line: 3}) || got.LabelUnserved != nil {
		t.Errorf("converted = %+v", got)
	}
}

func TestGatePathKeepsOnlyAPlainWorkflowPath(t *testing.T) {
	for p, want := range map[string]string{
		".github/workflows/ci.yml":          ".github/workflows/ci.yml",
		".github/workflows/release.yaml":    ".github/workflows/release.yaml",
		".github/workflows/IGNORE THIS.yml": "",
		"ci.yml":                            "",
	} {
		if got := GatePath(p); got != want {
			t.Errorf("GatePath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestGuidanceChecksAreReportedAsOutsideTheOfflineWorkflowCheck(t *testing.T) {
	r, err := Check("testdata/findings", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := notChecked(r, kennel.CodeGuidanceMissing); !ok || !strings.Contains(n.Reason, "workflow-only") {
		t.Fatalf("unchecked guidance=%+v", n)
	}
}

// A laptop cannot read a repository's settings or its required checks, and the
// report says so in words an operator can act on. A source the table of words
// does not know falls through to its raw name, which reads as a bug.
func TestEverySourceACheckNeedsHasWordsForWhyALaptopCannotReadIt(t *testing.T) {
	for _, c := range kennel.Checks() {
		for _, src := range append(append([]kennel.Source(nil), c.Needs...), c.Conditional...) {
			// Guidance checks are reported before this table is asked, with a
			// sentence of their own: the files are outside a workflow-only check.
			if src == kennel.SourceGuidance {
				continue
			}
			if sourceWords(src) == string(src) {
				t.Errorf("%s needs %q, which the offline report can only name by its raw word", c.Code, src)
			}
		}
	}
}

func TestTheSettingsChecksAreReportedAsNeedingWhatOnlyAControllerKnows(t *testing.T) {
	r, err := Check("testdata/findings", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[kennel.Code]string{
		kennel.CodeDefaultTokenWrite:         "the repository's Actions settings",
		kennel.CodePrivateForkSecrets:        "the repository's Actions settings",
		kennel.CodeRequiredCheckNeverReports: "what the fleet observed",
	} {
		n, ok := notChecked(r, code)
		if !ok || !strings.Contains(n.Reason, want) {
			t.Errorf("%s: not checked = %+v, %v; want a reason naming %q", code, n, ok, want)
		}
	}
}
