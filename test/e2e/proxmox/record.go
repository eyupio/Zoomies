package proxmox

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The evidence one run produces, in the shape roadmap/validation/proxmox-qualification.md
// asks for.
//
// It is written whatever the outcome, because a run that failed is the one worth
// reading, and it is written to a file of its own rather than into the record:
// the record states the procedure, and a harness that edited it could quietly
// make the procedure describe whatever happened. A person copies these tables
// in, having read them.
//
// The vocabulary is the drill record's -- what was expected, what was observed,
// and the last column, "human action needed", which is the one the record is
// read for. A run that recovers on its own and a run that leaves somebody a VM
// to delete are different findings, and only the row says which.

// phaseOrder is the seven timings the qualification record asks for, in the
// order it asks for them. Written here rather than derived from whatever the
// run happened to measure, so that a phase nobody managed to time is an empty
// row rather than an absent one.
var phaseOrder = []string{
	"queue to create issued",
	"create to resource running",
	"running to guest agent answering",
	"bootstrap to host joined",
	"host joined to first job started",
	"drain requested to last runner finished",
	"delete issued to resource confirmed gone",
}

// Timings collects one phase's durations across the cycles.
//
// Every figure it reports carries the count it came from. That is not
// decoration: "p95" of three samples is a sentence about three machines, and the
// record says "p95 of 20" precisely so that a run which managed four cycles
// cannot be read as having done twenty.
type Timings struct {
	samples map[string][]time.Duration
}

func newTimings() *Timings { return &Timings{samples: map[string][]time.Duration{}} }

// add records one observation. A negative or zero duration is dropped rather
// than averaged in: it means two timestamps arrived out of order, and a phase
// that took no time would drag a percentile towards a figure no machine ever
// achieved.
func (t *Timings) add(phase string, d time.Duration) {
	if d <= 0 {
		return
	}
	t.samples[phase] = append(t.samples[phase], d)
}

// stat is the p50, the p95 and the count they came from.
//
// Nearest-rank, not interpolated: with twenty samples an interpolated p95 is a
// number halfway between two machines, and the question being asked is "how slow
// was the slow one".
func (t *Timings) stat(phase string) (p50, p95 time.Duration, n int) {
	xs := append([]time.Duration(nil), t.samples[phase]...)
	if len(xs) == 0 {
		return 0, 0, 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	at := func(q float64) time.Duration {
		i := int(math.Ceil(q*float64(len(xs)))) - 1
		return xs[max(i, 0)]
	}
	return at(0.5), at(0.95), len(xs)
}

// CaseRow is one row of the record's "The runs" table.
type CaseRow struct {
	Number   int    `json:"number"`
	Case     string `json:"case"`
	Outcome  string `json:"outcome"`
	Timings  string `json:"timings"`
	Observed string `json:"observed"`
	// Human is what a person has to do about this row. "none" is the good
	// value and the only one that needs no explanation.
	Human string `json:"human_action_needed"`
}

// SetupRow is one row of the record's setup table: a number without the setup
// that produced it is not evidence.
type SetupRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Record is the whole of one run's evidence.
type Record struct {
	RunID      string     `json:"run_id"`
	Commit     string     `json:"commit"`
	RunLink    string     `json:"run_link,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	Setup      []SetupRow `json:"setup"`
	Cases      []CaseRow  `json:"cases"`
	// Reconciliation is the closing count. Absent means the run never reached
	// it, which is itself the finding: the reconciliation is the step that
	// decides qualification.
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
	// Residual is what this run created and could not remove. Empty is the only
	// good value.
	Residual []string `json:"residual_cleanup,omitempty"`

	timings *Timings
}

func newRecord(runID string) *Record {
	return &Record{RunID: runID, Commit: commit(), RunLink: runLink(), StartedAt: time.Now().UTC(), timings: newTimings()}
}

func (r *Record) setup(label, value string) {
	r.Setup = append(r.Setup, SetupRow{Label: label, Value: strings.TrimSpace(value)})
}

func (r *Record) addCase(row CaseRow) { r.Cases = append(r.Cases, row) }

// Markdown renders the tables the qualification record holds, filled in.
func (r *Record) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Proxmox VE qualification, evidence from run %s\n\n", r.RunID)
	fmt.Fprintf(&b, "Produced by `test/e2e/proxmox` at %s, on commit %s",
		r.FinishedAt.Format(time.RFC3339), r.Commit)
	if r.RunLink != "" {
		fmt.Fprintf(&b, ", from %s", r.RunLink)
	}
	b.WriteString(".\n\nThese are the tables `roadmap/validation/proxmox-qualification.md` asks for. " +
		"Read them, then copy them into that record; nothing edits it automatically, because the record " +
		"states the procedure and a harness that rewrote it could make the procedure describe whatever happened.\n\n")

	b.WriteString("## Setup\n\n| | |\n| --- | --- |\n")
	for _, row := range r.Setup {
		fmt.Fprintf(&b, "| %s | %s |\n", row.Label, orDash(row.Value))
	}

	b.WriteString("\n## The runs\n\n")
	b.WriteString("| # | Case | Outcome | Timings | What was observed | Human action needed |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, c := range r.Cases {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s |\n",
			c.Number, c.Case, c.Outcome, orDash(c.Timings), orDash(c.Observed), orDash(c.Human))
	}

	b.WriteString("\n## Timings\n\n")
	b.WriteString("Each with the count it came from: a percentile without its denominator is not a measurement.\n\n")
	b.WriteString("| Phase | p50 | p95 | n |\n| --- | --- | --- | --- |\n")
	for _, phase := range phaseOrder {
		p50, p95, n := r.timings.stat(phase)
		if n == 0 {
			fmt.Fprintf(&b, "| %s | - | - | 0 (never measured) |\n", phase)
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d |\n", phase, round(p50), round(p95), n)
	}

	b.WriteString("\n## The closing reconciliation\n\n")
	if r.Reconciliation == nil {
		b.WriteString("**The run never reached the reconciliation.** Nothing here is qualified: " +
			"the closing count is the step that decides it, and a run that stopped before it has not " +
			"established that it left nothing behind. Run `go run ./test/e2e/proxmox/verify` " +
			"against the cluster before anything else.\n")
	} else {
		rec := r.Reconciliation
		b.WriteString("| | Count | Notes |\n| --- | --- | --- |\n")
		fmt.Fprintf(&b, "| VMs in the range at the start | %d | range %s on %s |\n", rec.VMsAtStart, rec.Range, rec.Storage)
		fmt.Fprintf(&b, "| Machines created during the runs | %d | |\n", rec.Created)
		fmt.Fprintf(&b, "| Machines confirmed deleted | %d | confirmed by an inspect that could not find the resource |\n", rec.Confirmed)
		fmt.Fprintf(&b, "| VMs in the range at the end | %d | |\n", rec.VMsAtEnd)
		fmt.Fprintf(&b, "| Disks left on the storage | %d | |\n", rec.DisksLeft)
		fmt.Fprintf(&b, "| Unexplained owned resources | %d | **must be zero** |\n", rec.Unexplained)
		if len(rec.Findings) > 0 {
			b.WriteString("\nEach resource in the range, and what became of it:\n\n")
			b.WriteString("| Verdict | VMID | Node | Name | Detail |\n| --- | --- | --- | --- | --- |\n")
			for _, f := range rec.Findings {
				fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n",
					f.Verdict, f.VMID, orDash(f.Node), orDash(f.Name), f.Detail)
			}
		}
	}

	if len(r.Residual) > 0 {
		b.WriteString("\n## Left behind by this run\n\n")
		b.WriteString("This run created these and could not remove them. They are on somebody's cluster " +
			"or organisation now, and a person has to deal with them.\n\n")
		for _, line := range r.Residual {
			fmt.Fprintf(&b, "* %s\n", line)
		}
	}
	return b.String()
}

// Write saves the evidence as Markdown, with a JSON sibling so that a later run
// can be compared with this one without anybody parsing a table.
func (r *Record) Write(path string) error {
	r.FinishedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating the evidence directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(r.Markdown()), 0o644); err != nil {
		return fmt.Errorf("writing the evidence: %w", err)
	}
	raw, err := json.MarshalIndent(struct {
		*Record
		Timings map[string]map[string]any `json:"timings"`
	}{Record: r, Timings: r.timingsJSON()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the evidence: %w", err)
	}
	return os.WriteFile(strings.TrimSuffix(path, ".md")+".json", append(raw, '\n'), 0o644)
}

func (r *Record) timingsJSON() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, phase := range phaseOrder {
		p50, p95, n := r.timings.stat(phase)
		out[phase] = map[string]any{"p50_ms": p50.Milliseconds(), "p95_ms": p95.Milliseconds(), "n": n}
	}
	return out
}

// recordPath is where the evidence goes: beside the qualification record it
// fills in, named for the commit and the run, so two runs never overwrite one
// another and a figure can always be taken back to the code that produced it.
func recordPath(runID string) string {
	if p := strings.TrimSpace(os.Getenv("ZOOMIES_PROXMOX_RECORD")); p != "" {
		return p
	}
	root := filepath.Dir(builtBinary())
	return filepath.Join(root, "roadmap", "validation",
		fmt.Sprintf("proxmox-qualification-%s-%s.md", commit(), runID))
}

// runLink is where this evidence came from, when it came from CI. A figure is
// worth keeping only if it can be taken back to the run that produced it.
func runLink() string {
	server, repo, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, id)
}

// round is a duration a person reads: a qualification timing in nanoseconds
// says nothing a hundred milliseconds does not.
func round(d time.Duration) string { return d.Round(100 * time.Millisecond).String() }

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
