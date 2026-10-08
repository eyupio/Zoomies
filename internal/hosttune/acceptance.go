package hosttune

import (
	"fmt"
	"sync"
	"time"
)

// Acceptance is an operator's decision that a warning is deliberate. The
// controller stamps it on a COPY of the report when it reads one; anything an
// agent sent in its place is discarded, so a host can never silence its own
// alarm.
type Acceptance struct {
	Reason    string    `json:"reason"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Ended says why a held acceptance no longer covers a result, so the row that
// came back to the list does not look like a bug. At is the acceptance's own
// time -- when it was made for "changed" and "worse", when it lapsed for
// "expired" -- and never the moment of reading: a stamp that moved on every
// pass would make every frame differ from the last.
type Ended struct {
	By  string    `json:"by"`
	At  time.Time `json:"at"`
	Was string    `json:"was"`
	Why string    `json:"why"` // "changed" | "expired" | "worse"
}

// Why an acceptance ended.
const (
	EndedChanged = "changed"
	EndedExpired = "expired"
	EndedWorse   = "worse"
)

// Held is one stored acceptance as the controller loads it.
type Held struct {
	CheckID, Current, Reason, By string
	At, ExpiresAt                time.Time
}

// MaxAcceptance is the longest an acceptance may last. Every one must end: an
// acceptance with no end is how a kernel pinned below the baseline is still
// "deliberate" three years after the person who chose it left.
const MaxAcceptance = 365 * 24 * time.Hour

// catalogue is every check this build can emit, by id, built from the same
// constructors the engine runs (closures with no I/O, as titles does).
var catalogue = sync.OnceValue(func() map[string]Check {
	m := map[string]Check{"environment": {ID: "environment", Tier: Safe}}
	for _, list := range [][]Check{baseChecks(), kernelChecks(), dedicatedChecks()} {
		for _, c := range list {
			m[c.ID] = c
		}
	}
	return m
})

// Acceptable reports whether a check may ever be accepted and, if not, the
// sentence to tell the person. Only a counted catalogue check: never the
// reboot, which ends when the host reboots, and never the two disk checks,
// because free space moves and an acceptance could not tell 12% from 2%.
func Acceptable(id string) (ok bool, why string) {
	c, known := catalogue()[id]
	switch {
	case !known:
		return false, fmt.Sprintf("This build does not know a check called %q.", id)
	case id == KernelPending:
		return false, "A pending reboot cannot be accepted: it ends when the host reboots. Cordon the host and reboot it when it is idle."
	case id == "disk.space" || id == "disk.inodes":
		return false, "Free space moves, so an acceptance could not tell 12% free from 2%. Make room on the work directory's filesystem, or give the host a larger disk."
	case c.Tier != Safe || c.Optional:
		return false, "That check is a suggestion and does not count, so there is nothing to accept."
	}
	return true, ""
}

// AcceptRefusal is why a result as it stands cannot be accepted, or "" when it
// can. It judges the id first and then the outcome, so the sentence names the
// thing the person can change.
func AcceptRefusal(x Result) string {
	if ok, why := Acceptable(x.ID); !ok {
		return why
	}
	switch x.Status {
	case Warn:
		return ""
	case Error:
		return "This check could not run, so there is nothing to accept. Fix what stops it running; the Why column says what."
	}
	return "That check is not a warning on this host right now."
}

// Judged returns a copy of r with Accepted, Ended and Acceptable stamped, and
// never touches r.Results: the stored report is shared with the heartbeat write
// path. It clears whatever an agent put in those fields first.
//
// An acceptance covers a result only while the check is a counted Warn that
// still reads what it read when the operator decided, inside its term. If the
// value returns to the accepted text within the term it holds again, on purpose:
// the decision was about that value.
func (r Report) Judged(held []Held, now time.Time) Report {
	out := r
	out.Results = make([]Result, len(r.Results))
	byCheck := make(map[string]Held, len(held))
	for _, h := range held {
		if _, dup := byCheck[h.CheckID]; !dup {
			byCheck[h.CheckID] = h
		}
	}
	for i, x := range r.Results {
		x.Accepted, x.Ended, x.Acceptable = nil, nil, false
		out.Results[i] = x
		// A container sees only its own corner of the host, and the reboot row
		// is the reboot: neither is ever judged.
		if r.Container || r.rebootRow(x) || !x.Counted() {
			continue
		}
		if ok, _ := Acceptable(x.ID); !ok || (x.Status != Warn && x.Status != Error) {
			continue
		}
		h, has := byCheck[x.ID]
		switch {
		case !has:
		case x.Status == Error:
			x.Ended = &Ended{By: h.By, At: h.At, Was: h.Current, Why: EndedWorse}
		case !now.Before(h.ExpiresAt):
			x.Ended = &Ended{By: h.By, At: h.ExpiresAt, Was: h.Current, Why: EndedExpired}
		case x.Current != h.Current:
			x.Ended = &Ended{By: h.By, At: h.At, Was: h.Current, Why: EndedChanged}
		default:
			x.Accepted = &Acceptance{Reason: h.Reason, By: h.By, At: h.At, ExpiresAt: h.ExpiresAt}
		}
		x.Acceptable = x.Status == Warn && x.Accepted == nil
		out.Results[i] = x
	}
	return out
}

// Accepting reports whether an operator's acceptance covers this result.
func (x Result) Accepting() bool { return x.Accepted != nil }
