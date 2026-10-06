package hosttune

import (
	"sync"
	"time"
)

// ReportStaleAfter is how long a connected host may go without a newer OS
// report before Zoomies says its collector has stopped. It is later than the
// three minutes the host page greys a badge after -- that is a display, this
// raises a problem -- and later than the monitor's one-minute interval plus its
// 45s timeout. CheckedAt is the host's clock and the comparison is the
// controller's, so the slack also absorbs a clock that is a little off.
const ReportStaleAfter = 10 * time.Minute

// KernelPending is the check whose warning is the reboot flag: Run sets
// Report.RebootPending from it, so the one fact is already carried twice in a
// report. Summary and Findings count it once, as the reboot, and leave the row
// out of the checks that need attention.
const KernelPending = "kernel.pending"

// Counted is whether a result counts towards a host's health: the safe tier,
// and not an optional suggestion. That is what zoomies doctor counts by
// default. The other tiers are choices an operator opts into, so a host that
// has not made them is not unwell. A result with no tier is not counted.
func (x Result) Counted() bool { return x.Tier == Safe && !x.Optional }

// Summary is the one count every surface agrees on -- the host payload, the
// problems, the metrics, the feed, hosts list and MCP. It is worked out from
// the results on every read and is never stored or sent by an agent, so it
// cannot outlive the report it counts or be claimed by a host.
//
// Counts, which the CLI uses for the tier it ran and for exit codes, still
// counts every result on purpose: `doctor --tier aggressive` must exit 1 on an
// aggressive warning, and TestReportExitCodes builds results with no tier.
//
// A pending reboot is counted once. When the report says RebootPending, the
// kernel.pending warning is the same fact and is left out of every number here,
// so a host whose only finding is a reboot raises the reboot and not a failing
// check. Counts does not make that exception, which makes zoomies doctor's own
// warning count one higher than Warnings for exactly that check; a person
// reading the doctor's rows sees the row, and the reboot hint beside it, rather
// than a total that silently omits one.
type Summary struct {
	// Counted is how many counted checks there are, whatever they found, so a
	// reader can tell "every check passed" from "every check was skipped".
	Counted  int `json:"counted"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
	Skipped  int `json:"skipped"`
	// Suggestions are the warnings that do not count: other tiers, and
	// anything optional. An uncounted error or skip is in no number; it is
	// still in the results.
	Suggestions int `json:"suggestions"`
}

// rebootRow is the kernel.pending warning of a report that already says a
// reboot is pending. Only a warning: a check that could not run, or one that
// passed, is not the reboot, and a report that contradicts itself is judged by
// its rows.
func (r Report) rebootRow(x Result) bool {
	return r.RebootPending && x.ID == KernelPending && x.Status == Warn
}

// Summary counts the report once, for every surface to read.
func (r Report) Summary() Summary {
	var s Summary
	for _, x := range r.Results {
		if r.rebootRow(x) {
			continue
		}
		if !x.Counted() {
			if x.Status == Warn {
				s.Suggestions++
			}
			continue
		}
		s.Counted++
		switch x.Status {
		case Warn:
			s.Warnings++
		case Error:
			s.Errors++
		case Skip:
			s.Skipped++
		}
	}
	return s
}

// Findings is what Summary counts as warnings and errors, errors first and the
// engine's own order within each, so a problem and a page name the same
// checks. len(Findings()) == Warnings + Errors, by construction and by test.
func (r Report) Findings() []Result {
	var errs, warns []Result
	for _, x := range r.Results {
		if !x.Counted() || r.rebootRow(x) {
			continue
		}
		switch x.Status {
		case Error:
			errs = append(errs, x)
		case Warn:
			warns = append(warns, x)
		}
	}
	return append(errs, warns...)
}

// titles is the catalogue of check names this build knows, built from the same
// constructors the engine runs. They are closures with no I/O, so building the
// list reads nothing from the machine the controller is on.
var titles = sync.OnceValue(func() map[string]string {
	// environment is not a Check: Run adds it by hand, so it is named here. The
	// engine gives it a second title when the operating system is not Linux,
	// but that result is a skip and is never a finding.
	m := map[string]string{"environment": "Distribution"}
	for _, list := range [][]Check{baseChecks(), kernelChecks(), dedicatedChecks()} {
		for _, c := range list {
			m[c.ID] = c.Title
		}
	}
	return m
})

// Title is this build's own name for a check, or false for one it has not heard
// of. A problem names checks with it and never with the report's own text,
// which an agent wrote.
func Title(id string) (string, bool) {
	t, ok := titles()[id]
	return t, ok
}
