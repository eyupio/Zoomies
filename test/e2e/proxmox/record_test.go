package proxmox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A phase that took no time did not happen: two timestamps arrived out of
// order. Averaging that in drags a percentile towards a figure no machine ever
// achieved, and the whole point of these numbers is that somebody can plan
// against them.
func TestATimingThatCannotHaveHappenedIsDroppedRatherThanAveragedIn(t *testing.T) {
	ts := newTimings()
	ts.add("create to resource running", 0)
	ts.add("create to resource running", -5*time.Second)
	if _, _, n := ts.stat("create to resource running"); n != 0 {
		t.Errorf("a zero and a negative sample were counted: n = %d", n)
	}

	ts.add("create to resource running", 30*time.Second)
	if p50, p95, n := ts.stat("create to resource running"); n != 1 || p50 != 30*time.Second || p95 != 30*time.Second {
		t.Errorf("one sample reads as p50=%s p95=%s n=%d", p50, p95, n)
	}
}

// Nearest-rank, not interpolated. With twenty samples an interpolated p95 is a
// number halfway between two machines, and the question being asked is "how
// slow was the slow one".
func TestThePercentilesAreMachinesThatExistedRatherThanNumbersBetweenThem(t *testing.T) {
	ts := newTimings()
	for i := 1; i <= 20; i++ {
		ts.add("create to resource running", time.Duration(i)*time.Second)
	}
	p50, p95, n := ts.stat("create to resource running")
	if n != 20 {
		t.Fatalf("n = %d, want 20", n)
	}
	if p50 != 10*time.Second {
		t.Errorf("p50 = %s, want the tenth of twenty", p50)
	}
	if p95 != 19*time.Second {
		t.Errorf("p95 = %s, want the nineteenth of twenty", p95)
	}
	// Every reported figure is one somebody actually waited.
	for _, d := range []time.Duration{p50, p95} {
		if d%time.Second != 0 {
			t.Errorf("%s is not one of the samples; the percentile was interpolated", d)
		}
	}
}

// A phase nobody managed to time is an empty row rather than an absent one, and
// it says so in words.
//
// A run that managed four cycles must not be readable as having done twenty,
// and a table that silently omits what it could not measure is exactly how that
// happens.
func TestTheTimingsTableShowsEveryPhaseAndSaysWhichWereNeverMeasured(t *testing.T) {
	r := newRecord("run_abc")
	r.timings.add(phaseOrder[0], 2*time.Second)

	md := r.Markdown()
	for _, phase := range phaseOrder {
		if !strings.Contains(md, "| "+phase+" |") {
			t.Errorf("the timings table has no row for %q", phase)
		}
	}
	if !strings.Contains(md, "0 (never measured)") {
		t.Error("a phase with no samples does not say it was never measured")
	}
	if !strings.Contains(md, "a percentile without its denominator is not a measurement") {
		t.Error("the table does not say why every figure carries its count")
	}
}

// A run that stopped before the reconciliation has not qualified anything, and
// the evidence says so rather than reading as a run with a blank section.
func TestEvidenceFromARunThatNeverReconciledRefusesToLookLikeAPass(t *testing.T) {
	r := newRecord("run_abc")
	md := r.Markdown()
	if !strings.Contains(md, "**The run never reached the reconciliation.**") {
		t.Error("a run with no closing count does not say so")
	}
	if !strings.Contains(md, "Nothing here is qualified") {
		t.Error("a run with no closing count does not say what that means")
	}
	if !strings.Contains(md, "go run ./test/e2e/proxmox/verify") {
		t.Error("it does not say what to run against the cluster first")
	}
}

// The full evidence: the setup that produced the numbers, the cases, the
// closing count, and anything this run could not remove.
func TestTheEvidenceCarriesTheSetupTheCasesTheCountAndWhatWasLeftBehind(t *testing.T) {
	r := newRecord("run_abc")
	r.setup("Proxmox VE version", "  pve-manager/8.2.4  ")
	r.setup("Node(s)", "")
	r.addCase(CaseRow{
		Number: 1, Case: "scale from zero", Outcome: "pass",
		Timings: "4m 12s", Observed: "one machine, one job", Human: "none",
	})
	r.Reconciliation = &Reconciliation{
		Range: Range{Min: 9000, Max: 9099}, Storage: "local-lvm",
		VMsAtStart: 1, Created: 20, Confirmed: 20, VMsAtEnd: 1,
		Findings: []Finding{{Verdict: Accounted, VMID: 9001, Detail: "was here before the run"}},
	}
	r.Residual = []string{"machine mach_9 (VM 9042 on pve1)"}

	md := r.Markdown()
	for _, want := range []string{
		"evidence from run run_abc",
		"| Proxmox VE version | pve-manager/8.2.4 |",
		// A setup row nobody filled in is a dash, so a missing fact is visible
		// rather than an empty cell somebody reads past.
		"| Node(s) | - |",
		"| 1 | scale from zero | pass | 4m 12s | one machine, one job | none |",
		"| Machines confirmed deleted | 20 |",
		"| Unexplained owned resources | 0 | **must be zero** |",
		"## Left behind by this run",
		"machine mach_9 (VM 9042 on pve1)",
		"accounted | 9001",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the evidence does not contain %q:\n%s", want, md)
		}
	}
	// It is evidence to be read and copied, never a thing that edits the record
	// it fills in -- a harness that rewrote the procedure could make the
	// procedure describe whatever happened.
	if !strings.Contains(md, "nothing edits it automatically") {
		t.Error("the evidence does not say it does not rewrite the qualification record")
	}
}

// Written as Markdown for a person and JSON for the next run, side by side.
func TestWritingTheEvidenceLeavesBothSomethingToReadAndSomethingToCompare(t *testing.T) {
	r := newRecord("run_abc")
	r.timings.add(phaseOrder[0], 2500*time.Millisecond)
	path := filepath.Join(t.TempDir(), "nested", "proxmox-qualification-run_abc.md")

	if err := r.Write(path); err != nil {
		t.Fatalf("writing the evidence: %v", err)
	}
	if r.FinishedAt.IsZero() {
		t.Error("the evidence does not say when it finished")
	}

	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the Markdown back: %v", err)
	}
	if !strings.Contains(string(md), "run_abc") {
		t.Error("the Markdown does not name the run")
	}

	raw, err := os.ReadFile(strings.TrimSuffix(path, ".md") + ".json")
	if err != nil {
		t.Fatalf("the JSON sibling was not written: %v", err)
	}
	var out struct {
		RunID   string                    `json:"run_id"`
		Timings map[string]map[string]any `json:"timings"`
		Cases   []CaseRow                 `json:"cases"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the JSON sibling does not parse: %v", err)
	}
	if out.RunID != "run_abc" {
		t.Errorf("run id in the JSON = %q", out.RunID)
	}
	// Every phase is present with its count, so two runs can be compared
	// without anybody parsing a table -- and a phase measured in one run and
	// not the other is visible as n=0 rather than as a missing key.
	if len(out.Timings) != len(phaseOrder) {
		t.Errorf("the JSON carries %d phases, want all %d", len(out.Timings), len(phaseOrder))
	}
	first := out.Timings[phaseOrder[0]]
	if first["n"].(float64) != 1 || first["p50_ms"].(float64) != 2500 {
		t.Errorf("the measured phase came back as %v", first)
	}
	if out.Timings[phaseOrder[1]]["n"].(float64) != 0 {
		t.Error("an unmeasured phase is missing its zero count")
	}
}

// Two runs never overwrite one another, and a figure can always be taken back
// to the code that produced it.
func TestEvidenceIsNamedForItsRunAndItsCommitUnlessToldOtherwise(t *testing.T) {
	t.Setenv("ZOOMIES_PROXMOX_RECORD", "")
	got := recordPath("run_abc")
	if !strings.Contains(got, "run_abc") {
		t.Errorf("recordPath = %q, does not name the run", got)
	}
	if !strings.Contains(got, commit()) {
		t.Errorf("recordPath = %q, does not name the commit", got)
	}
	if filepath.Base(filepath.Dir(got)) != "validation" {
		t.Errorf("recordPath = %q, want it beside the qualification record it fills in", got)
	}

	t.Setenv("ZOOMIES_PROXMOX_RECORD", "/tmp/somewhere/else.md")
	if got := recordPath("run_abc"); got != "/tmp/somewhere/else.md" {
		t.Errorf("the override was ignored: %q", got)
	}
}

// Where the evidence came from, when it came from CI -- and nothing at all when
// it did not, rather than a link that goes nowhere.
func TestEvidenceLinksBackToTheRunThatProducedItOnlyWhenThereWasOne(t *testing.T) {
	for _, v := range []string{"GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_RUN_ID"} {
		t.Setenv(v, "")
	}
	if got := runLink(); got != "" {
		t.Errorf("runLink() off CI = %q, want nothing rather than a broken link", got)
	}
	if r := newRecord("run_abc"); r.RunLink != "" {
		t.Errorf("a record made off CI carries a run link: %q", r.RunLink)
	}

	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "eyupio/zoomies")
	t.Setenv("GITHUB_RUN_ID", "12345")
	want := "https://github.com/eyupio/zoomies/actions/runs/12345"
	if got := runLink(); got != want {
		t.Errorf("runLink() = %q, want %q", got, want)
	}
	r := newRecord("run_abc")
	if r.RunLink != want {
		t.Errorf("the record did not pick the link up: %q", r.RunLink)
	}
	if !strings.Contains(r.Markdown(), want) {
		t.Error("the evidence does not say which CI run produced it")
	}

	// One of the three missing is not a link: a URL assembled from two of them
	// points at the wrong thing, which is worse than pointing at nothing.
	t.Setenv("GITHUB_RUN_ID", "")
	if got := runLink(); got != "" {
		t.Errorf("runLink() with no run id = %q", got)
	}
}

// Durations are rounded to something a person reads, and a fact nobody filled
// in shows as a dash rather than as an empty cell.
func TestAFigureIsRoundedForReadingAndAMissingOneIsVisible(t *testing.T) {
	if got := round(2*time.Minute + 34*time.Second + 567*time.Millisecond); got != "2m34.6s" {
		t.Errorf("round() = %q", got)
	}
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(%q) = %q, want a dash", "", got)
	}
	if got := orDash("pve1"); got != "pve1" {
		t.Errorf("orDash(%q) = %q", "pve1", got)
	}
}
