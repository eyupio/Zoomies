package kennel

import "time"

// Waiver is a decision, recorded by a person with a reason, that a finding is
// acceptable here. It is not a dismissal: a dismissal hides a notification for
// one person, and a waiver changes what the fleet's record says about a
// repository, so it carries a reason, an owner and an end.
type Waiver struct {
	ID      string `json:"id"`
	Code    Code   `json:"code"`
	Subject string `json:"subject"`
	// Severity is the finding's severity when the waiver was made. A finding
	// that has since got worse is not covered by a decision about a milder one.
	Severity Severity  `json:"severity"`
	Reason   string    `json:"reason"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
	// ExpiresAt is mandatory. A waiver that never ends is a decision nobody is
	// asked to make again.
	ExpiresAt time.Time `json:"expires_at"`
}

// covers says whether the waiver still speaks for this finding at this moment.
func (w Waiver) covers(f Finding, now time.Time) bool {
	return w.Code == f.Code &&
		w.Subject == f.Subject &&
		now.Before(w.ExpiresAt) &&
		w.Severity.rank() >= f.Severity.rank()
}

// matches is whether the waiver is about this finding at all, whether or not it
// is still in force.
func (w Waiver) matches(f Finding) bool { return w.Code == f.Code && w.Subject == f.Subject }

// WaivedFinding is a finding a waiver covers.
type WaivedFinding struct {
	Finding Finding `json:"finding"`
	Waiver  Waiver  `json:"waiver"`
}
