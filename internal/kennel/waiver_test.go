package kennel

import (
	"testing"
	"time"
)

func waiverFor(c Code, sev Severity, expires time.Time) Waiver {
	return Waiver{ID: "kcw_1", Code: c, Severity: sev, Reason: "isolated hosts, reviewed", By: "usr_1", At: now.Add(-time.Hour), ExpiresAt: expires}
}

func forkRun() Snapshot {
	return withRuns(publicRepo(), Run{ID: 1, Event: "pull_request", FromFork: true})
}

// A waiver is how a person says "I have looked, and this is acceptable here".
// It must take the finding off the open list without making it disappear.
func TestAWaiverMovesAFindingToWaivedAndKeepsItVisible(t *testing.T) {
	w := waiverFor(CodeForkCodeRan, SeverityError, now.Add(24*time.Hour))
	ev := Evaluate(forkRun(), Policy{Waivers: []Waiver{w}})
	if _, open := finding(ev, CodeForkCodeRan); open {
		t.Error("a waived finding is still open")
	}
	if len(ev.Waived) != 1 || ev.Waived[0].Finding.Code != CodeForkCodeRan || ev.Waived[0].Waiver.Reason != w.Reason {
		t.Errorf("waived = %+v, want the finding with its waiver beside it", ev.Waived)
	}
	if ev.Counts().Waived != 1 {
		t.Errorf("counts = %+v: the badge has to be able to say how many are waived", ev.Counts())
	}
}

// A decision nobody is asked to make again is a decision that has been
// forgotten. At its end the finding is open again, and the evaluation says a
// waiver lapsed so the page can say so.
func TestAWaiverEndsWhenItExpires(t *testing.T) {
	w := waiverFor(CodeForkCodeRan, SeverityError, now) // expires exactly now
	ev := Evaluate(forkRun(), Policy{Waivers: []Waiver{w}})
	if _, open := finding(ev, CodeForkCodeRan); !open {
		t.Error("an expired waiver still covers the finding")
	}
	if len(ev.Lapsed) != 1 {
		t.Errorf("lapsed = %+v, want the expired waiver", ev.Lapsed)
	}
}

// A decision about a warning is not a decision about the error it became.
func TestAWaiverDoesNotCoverAFindingThatGotWorse(t *testing.T) {
	// The public-repository finding was a warning when it was waived, and a fork's
	// code running since has made it an error.
	w := waiverFor(CodePublicRepoOnFleet, SeverityWarning, now.Add(time.Hour))
	ev := Evaluate(forkRun(), Policy{Waivers: []Waiver{w}})
	f, open := finding(ev, CodePublicRepoOnFleet)
	if !open || f.Severity != SeverityError {
		t.Fatalf("finding open=%v severity=%s, want an open error", open, f.Severity)
	}
	if len(ev.Lapsed) != 1 {
		t.Errorf("lapsed = %+v, want the outranked waiver", ev.Lapsed)
	}
}

func TestAWaiverMadeForAnErrorCoversTheSameFindingWhenItIsMilder(t *testing.T) {
	w := waiverFor(CodePublicRepoOnFleet, SeverityError, now.Add(time.Hour))
	ev := Evaluate(publicRepo(), Policy{Waivers: []Waiver{w}}) // a warning on its own
	if _, open := finding(ev, CodePublicRepoOnFleet); open {
		t.Error("a waiver for an error did not cover the same finding at warning")
	}
}

func TestAWaiverIsAboutOneFindingAndOneSubject(t *testing.T) {
	other := waiverFor(CodeTargetEventRan, SeverityError, now.Add(time.Hour))
	subject := waiverFor(CodeForkCodeRan, SeverityError, now.Add(time.Hour))
	subject.Subject = "workflow:1234"
	ev := Evaluate(forkRun(), Policy{Waivers: []Waiver{other, subject}})
	if _, open := finding(ev, CodeForkCodeRan); !open {
		t.Error("a waiver for another check or another subject covered the finding")
	}
}

// A finding that stopped being reported has no use for its waiver, and the
// controller retires it: an old decision must not quietly cover the same
// problem if it comes back months later.
func TestAWaiverForAFindingThatIsGoneIsStale(t *testing.T) {
	w := waiverFor(CodeForkCodeRan, SeverityError, now.Add(time.Hour))
	ev := Evaluate(publicRepo(), Policy{Waivers: []Waiver{w}})
	if len(ev.Stale) != 1 || ev.Stale[0].ID != w.ID {
		t.Errorf("stale = %+v, want the waiver", ev.Stale)
	}
	if len(ev.Waived) != 0 {
		t.Errorf("waived = %+v", ev.Waived)
	}
}

// Where two waivers could cover a finding, the answer must not depend on the
// order they were read from the database in.
func TestTheLongestWaiverCoversAndTheOrderTheyWereStoredInDoesNotMatter(t *testing.T) {
	short := waiverFor(CodeForkCodeRan, SeverityError, now.Add(time.Hour))
	short.ID = "kcw_a" // sorts first, so an ID tie-break cannot stand in for the expiry order
	long := waiverFor(CodeForkCodeRan, SeverityError, now.Add(48*time.Hour))
	long.ID = "kcw_z"
	for _, order := range [][]Waiver{{short, long}, {long, short}} {
		ev := Evaluate(forkRun(), Policy{Waivers: order})
		if len(ev.Waived) != 1 || ev.Waived[0].Waiver.ID != "kcw_z" {
			t.Errorf("waived by %v, want the longer waiver", ev.Waived)
		}
	}
}

// Waiving the milder finding must not silently cover the worse one it escalates
// to: escalation happens before waivers are applied.
func TestEscalationHappensBeforeWaiversAreApplied(t *testing.T) {
	warn := waiverFor(CodePublicRepoOnFleet, SeverityWarning, now.Add(time.Hour))
	ev := Evaluate(withWeakPool(publicRepo(), DangerRoot), Policy{Waivers: []Waiver{warn}})
	if _, open := finding(ev, CodePublicRepoOnFleet); !open {
		t.Error("a waiver for the warning covered the finding after it became an error")
	}
}
