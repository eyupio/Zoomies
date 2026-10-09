package kennel

import (
	"slices"
	"strings"
	"time"
)

// Finding is one thing wrong, in the three sentences an operator needs: what
// is true, why it matters, and what to change.
type Finding struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	// Subject tells two findings of one code apart, and is what a waiver is
	// about. It is empty for a finding about the whole repository.
	Subject string `json:"subject"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Fix     string `json:"fix"`
	// Evidence is the typed, gated place where a name or an ID may appear.
	Evidence []Evidence `json:"evidence"`
}

// Policy is what the operator has decided, as opposed to what GitHub or the
// fleet said.
type Policy struct {
	// Disabled names check codes and areas the operator turned off.
	Disabled map[string]bool
	Waivers  []Waiver
}

func (p Policy) disables(c *Check) bool {
	return p.Disabled[string(c.Code)] || p.Disabled[string(c.Area)]
}

// Evaluation is what Evaluate made of one snapshot.
type Evaluation struct {
	Version int `json:"version"`
	// Findings are the open ones, worst first.
	Findings []Finding       `json:"findings"`
	Waived   []WaivedFinding `json:"waived"`
	// Ran are the checks that were judged, including those that did not apply
	// to this repository: clear and not applicable are both an answer.
	Ran []Code `json:"ran"`
	// Skipped are checks whose source could not be read.
	Skipped []Skipped `json:"skipped"`
	// Incomplete are checks that ran against a partial read and found nothing,
	// which is not the same as being clear.
	Incomplete []Code `json:"incomplete"`
	// Disabled are checks the operator turned off. They are not a gap.
	Disabled []Code `json:"disabled"`
	// Lapsed are waivers that match an open finding but no longer cover it,
	// because they ended or the finding got worse.
	Lapsed []Waiver `json:"lapsed"`
	// Stale are waivers that match no finding at all, which the controller
	// retires: a finding that stopped being reported has no use for its waiver.
	Stale []Waiver `json:"stale"`
	// Complete is whether every enabled check ran against everything it needs.
	Complete bool `json:"complete"`
}

// State is a repository's standing, which is all the Overview's counts and the
// badge are built from.
type State string

const (
	// StatePending: nothing has evaluated it yet. Evaluate never returns it; the
	// store holds it until the first evaluation lands.
	StatePending State = "pending"
	// StatePartial: nothing is open, but something could not be checked.
	StatePartial State = "partial"
	// StateAttention: an error or a warning is open. It outranks partial, so an
	// open error is never hidden behind "we could not read everything".
	StateAttention State = "attention"
	// StateBestInShow: every enabled check ran against everything it needs and
	// nothing worse than an info is open. A repository that could not be fully
	// read is not the best of anything.
	StateBestInShow State = "best_in_show"
)

// State works the standing out. Info findings and waived ones do not stop a
// repository being best in show; the Overview shows how many are waived beside
// the badge, so it cannot hide them.
func (e Evaluation) State() State {
	for _, f := range e.Findings {
		if f.Severity.rank() >= SeverityWarning.rank() {
			return StateAttention
		}
	}
	if !e.Complete {
		return StatePartial
	}
	return StateBestInShow
}

// Counts is how many findings of each severity are open, and how many waived.
type Counts struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Info    int `json:"info"`
	Waived  int `json:"waived"`
}

// Counts tallies the evaluation.
func (e Evaluation) Counts() Counts {
	c := Counts{Waived: len(e.Waived)}
	for _, f := range e.Findings {
		switch f.Severity {
		case SeverityError:
			c.Error++
		case SeverityWarning:
			c.Warning++
		case SeverityInfo:
			c.Info++
		}
	}
	return c
}

// Evaluate judges a snapshot. It is deterministic: the same snapshot and policy
// give the same Evaluation byte for byte, whatever order the snapshot's slices
// arrived in.
func Evaluate(s Snapshot, p Policy) Evaluation {
	ev := Evaluation{Version: Version}
	var open []Finding
	for i := range checks {
		c := &checks[i]
		if p.disables(c) {
			ev.Disabled = append(ev.Disabled, c.Code)
			continue
		}
		if sk, ok := unreadable(c.Code, s.Coverage, c.Needs); !ok {
			ev.Skipped = append(ev.Skipped, sk)
			continue
		}
		r := c.eval(&s)
		if !r.applies {
			ev.Ran = append(ev.Ran, c.Code)
			continue
		}
		if sk, ok := unreadable(c.Code, s.Coverage, r.extra); !ok {
			ev.Skipped = append(ev.Skipped, sk)
			continue
		}
		ev.Ran = append(ev.Ran, c.Code)
		open = append(open, r.findings...)
		if r.incomplete || (len(r.findings) == 0 && (anyPartial(s.Coverage, c.Needs) || anyPartial(s.Coverage, r.extra))) {
			ev.Incomplete = append(ev.Incomplete, c.Code)
		}
	}

	escalate(open)
	ev.Findings, ev.Waived, ev.Lapsed, ev.Stale = applyWaivers(open, p.Waivers, s.At)
	for i := range ev.Findings {
		ev.Findings[i].Evidence = nonNil(ev.Findings[i].Evidence)
	}
	sortFindings(ev.Findings)
	slices.SortFunc(ev.Waived, func(a, b WaivedFinding) int { return compareFindings(a.Finding, b.Finding) })

	// Nothing checked is not "best in show": an operator who turned every check
	// off has asked not to be told, which is different from being clear.
	ev.Complete = len(ev.Skipped) == 0 && len(ev.Incomplete) == 0 && len(ev.Ran) > 0
	ev.Ran, ev.Skipped, ev.Incomplete, ev.Disabled = nonNil(ev.Ran), nonNil(ev.Skipped), nonNil(ev.Incomplete), nonNil(ev.Disabled)
	ev.Findings, ev.Waived, ev.Lapsed, ev.Stale = nonNil(ev.Findings), nonNil(ev.Waived), nonNil(ev.Lapsed), nonNil(ev.Stale)
	return ev
}

// unreadable returns the first source that cannot give facts, as a Skipped. ok
// is true when every source can.
func unreadable(code Code, cov Coverage, need []Source) (Skipped, bool) {
	for _, src := range need {
		if st := cov.state(src); !st.readable() {
			return Skipped{Code: code, Source: src, State: st}, false
		}
	}
	return Skipped{}, true
}

func anyPartial(cov Coverage, need []Source) bool {
	for _, src := range need {
		if cov.state(src) == CoveragePartial {
			return true
		}
	}
	return false
}

// escalate raises the public-exposure warning to an error when either of the
// two findings that make it urgent is open beside it, and raises a weak fork
// approval to an error when a fork's code has already run here. It does so
// before waivers, so a waiver made about the milder finding does not silently
// cover the worse one.
func escalate(open []Finding) {
	var worse, forkRan bool
	for _, f := range open {
		if f.Code == CodeForkCodeRan || f.Code == CodePublicRepoWeakPool {
			worse = true
		}
		if f.Code == CodeForkCodeRan {
			forkRan = true
		}
	}
	for i := range open {
		switch open[i].Code {
		case CodePublicRepoOnFleet:
			if worse {
				open[i].Severity = SeverityError
			}
		case CodeForkApprovalWeak:
			if forkRan {
				open[i].Severity = SeverityError
			}
		}
	}
}

// applyWaivers splits findings into those still open and those a waiver covers,
// and reports the waivers that matched nothing or no longer cover what they
// matched. Where two waivers could cover one finding the one that lasts longest
// does, so the answer never depends on the order they were stored in.
func applyWaivers(open []Finding, waivers []Waiver, now time.Time) (still []Finding, waived []WaivedFinding, lapsed, stale []Waiver) {
	ws := slices.Clone(waivers)
	slices.SortFunc(ws, func(a, b Waiver) int {
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			if a.ExpiresAt.After(b.ExpiresAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	used := make([]bool, len(ws))
	for _, f := range open {
		covered := false
		for i, w := range ws {
			if w.covers(f, now) {
				waived = append(waived, WaivedFinding{Finding: f, Waiver: w})
				used[i], covered = true, true
				break
			}
		}
		if covered {
			continue
		}
		still = append(still, f)
		for i, w := range ws {
			if w.matches(f) {
				lapsed = append(lapsed, w)
				used[i] = true
			}
		}
	}
	for i, w := range ws {
		if !used[i] {
			stale = append(stale, w)
		}
	}
	return still, waived, lapsed, stale
}

func sortFindings(fs []Finding) { slices.SortFunc(fs, compareFindings) }

func compareFindings(a, b Finding) int {
	if a.Severity.rank() != b.Severity.rank() {
		return b.Severity.rank() - a.Severity.rank()
	}
	if c := strings.Compare(string(a.Code), string(b.Code)); c != 0 {
		return c
	}
	return strings.Compare(a.Subject, b.Subject)
}

// nonNil keeps an empty list an empty JSON array rather than null, which is
// what the UI and the generated client expect.
func nonNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
