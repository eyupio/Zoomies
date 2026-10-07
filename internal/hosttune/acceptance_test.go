package hosttune

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var acceptNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func warnWith(id, current string) Result {
	return Result{ID: id, Title: id, Tier: Safe, Status: Warn, Current: current}
}

func heldFor(id, current string, expires time.Time) Held {
	return Held{CheckID: id, Current: current, Reason: "pinned on purpose", By: "Sam", At: acceptNow.Add(-24 * time.Hour), ExpiresAt: expires}
}

// Which checks may be accepted is decided here and nowhere else, so a refused
// one cannot be got round by a client that skips the dialog.
func TestOnlyACountedCatalogueCheckThatIsNotAMovingMeasurementIsAcceptable(t *testing.T) {
	for id, want := range map[string]bool{
		"docker.logs":     true,
		"work.filesystem": true,
		"environment":     true,
		KernelPending:     false,
		"disk.space":      false,
		"disk.inodes":     false,
		"cpu.governor":    false, // aggressive: a suggestion, nothing to accept
		"tmp.tmpfs":       false,
		"service.snapd":   false,
		"no.such.check":   false,
		"cpu.x$(reboot)":  false,
		"":                false,
	} {
		ok, why := Acceptable(id)
		if ok != want {
			t.Errorf("Acceptable(%q) = %v, want %v", id, ok, want)
		}
		if !ok && why == "" {
			t.Errorf("Acceptable(%q) refused without telling the person why", id)
		}
		if ok && why != "" {
			t.Errorf("Acceptable(%q) allowed but also said %q", id, why)
		}
	}
}

func TestEveryRefusalSentenceNamesWhatThePersonCanDo(t *testing.T) {
	_, unknown := Acceptable("nope")
	_, reboot := Acceptable(KernelPending)
	_, disk := Acceptable("disk.space")
	for _, s := range []string{unknown, reboot, disk, AcceptRefusal(Result{ID: "docker.logs", Tier: Safe, Status: Error}), AcceptRefusal(Result{ID: "docker.logs", Tier: Safe, Status: OK})} {
		if s == "" || !strings.HasSuffix(s, ".") {
			t.Errorf("refusal %q is not a sentence", s)
		}
	}
	if AcceptRefusal(warnWith("docker.logs", "x")) != "" {
		t.Error("a counted warning on a catalogue check was refused")
	}
}

// The stored report is shared with the heartbeat write path: judging must never
// leave a mark on it, or the next read would see an acceptance that was revoked.
func TestJudgingNeverMutatesTheStoredReport(t *testing.T) {
	r := Report{Results: []Result{warnWith("docker.logs", "none")}}
	got := r.Judged([]Held{heldFor("docker.logs", "none", acceptNow.Add(time.Hour))}, acceptNow)
	if got.Results[0].Accepted == nil {
		t.Fatal("the acceptance did not cover its result")
	}
	if r.Results[0].Accepted != nil || r.Results[0].Acceptable {
		t.Error("Judged wrote through to the report it was given")
	}
}

// A host must not be able to silence its own alarm by claiming an acceptance.
func TestAnAgentsOwnAcceptanceMarksAreCleared(t *testing.T) {
	x := warnWith("docker.logs", "none")
	x.Accepted = &Acceptance{Reason: "trust me"}
	x.Ended = &Ended{Why: "changed"}
	x.Acceptable = false
	for _, container := range []bool{false, true} {
		got := Report{Container: container, Results: []Result{x}}.Judged(nil, acceptNow)
		if got.Results[0].Accepted != nil || got.Results[0].Ended != nil {
			t.Errorf("container=%v: a mark the agent sent survived", container)
		}
		if got.Summary().Accepted != 0 {
			t.Errorf("container=%v: an agent's claim was counted as accepted", container)
		}
	}
}

func TestAnAcceptanceHoldsOnlyForTheValueAndTermItWasMadeFor(t *testing.T) {
	soon := acceptNow.Add(48 * time.Hour)
	cases := []struct {
		name    string
		result  Result
		held    Held
		now     time.Time
		covered bool
		ended   string
		offer   bool
	}{
		{"the same value inside the term is covered", warnWith("docker.logs", "none"), heldFor("docker.logs", "none", soon), acceptNow, true, "", false},
		{"a warning nobody accepted can be accepted", warnWith("docker.logs", "none"), Held{}, acceptNow, false, "", true},
		{"a changed value counts again and says so", warnWith("docker.logs", "10m"), heldFor("docker.logs", "none", soon), acceptNow, false, EndedChanged, true},
		{"the day it ends it counts again", warnWith("docker.logs", "none"), heldFor("docker.logs", "none", acceptNow), acceptNow, false, EndedExpired, true},
		{"an error is never covered and is not offered", Result{ID: "docker.logs", Tier: Safe, Status: Error, Current: "none"}, heldFor("docker.logs", "none", soon), acceptNow, false, EndedWorse, false},
		{"an acceptance on a passing check is inert", Result{ID: "docker.logs", Tier: Safe, Status: OK, Current: "none"}, heldFor("docker.logs", "none", soon), acceptNow, false, "", false},
		{"an acceptance on a skipped check is inert", Result{ID: "docker.logs", Tier: Safe, Status: Skip}, heldFor("docker.logs", "none", soon), acceptNow, false, "", false},
		{"a suggestion tier is never judged", Result{ID: "cpu.governor", Tier: Aggressive, Status: Warn, Current: "none"}, heldFor("cpu.governor", "none", soon), acceptNow, false, "", false},
		{"disk space is never accepted even if a row exists", warnWith("disk.space", "3% free"), heldFor("disk.space", "3% free", soon), acceptNow, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			held := []Held{c.held}
			if c.held.CheckID == "" {
				held = nil
			}
			got := Report{Results: []Result{c.result}}.Judged(held, c.now).Results[0]
			if (got.Accepted != nil) != c.covered {
				t.Errorf("covered = %v, want %v", got.Accepted != nil, c.covered)
			}
			why := ""
			if got.Ended != nil {
				why = got.Ended.Why
			}
			if why != c.ended {
				t.Errorf("ended = %q, want %q", why, c.ended)
			}
			if got.Acceptable != c.offer {
				t.Errorf("acceptable = %v, want %v", got.Acceptable, c.offer)
			}
		})
	}
}

// If the value drifts back inside the term the decision applies again: it was a
// decision about that value, and the docs say so.
func TestAValueThatReturnsToTheAcceptedTextHoldsAgainInsideTheTerm(t *testing.T) {
	held := []Held{heldFor("docker.logs", "none", acceptNow.Add(time.Hour))}
	changed := Report{Results: []Result{warnWith("docker.logs", "10m")}}.Judged(held, acceptNow)
	back := Report{Results: []Result{warnWith("docker.logs", "none")}}.Judged(held, acceptNow)
	if changed.Results[0].Accepted != nil || back.Results[0].Accepted == nil {
		t.Error("the acceptance did not follow the value")
	}
}

// The ended stamp must not move between reads, or every pass would publish a
// frame that differs from the last.
func TestAnEndedNoteIsTheSameOnEveryRead(t *testing.T) {
	r := Report{Results: []Result{warnWith("docker.logs", "10m")}}
	held := []Held{heldFor("docker.logs", "none", acceptNow.Add(time.Hour))}
	a, _ := json.Marshal(r.Judged(held, acceptNow))
	b, _ := json.Marshal(r.Judged(held, acceptNow.Add(10*time.Minute)))
	if string(a) != string(b) {
		t.Errorf("the judged report moved with the clock:\n%s\n%s", a, b)
	}
}

func TestAnAcceptedWarningLeavesTheCountsAndStaysInTheTotal(t *testing.T) {
	r := Report{Results: []Result{
		warnWith("docker.logs", "none"),
		warnWith("cgroup.version", "v1"),
		{ID: "docker.storage", Tier: Safe, Status: OK},
	}}.Judged([]Held{heldFor("docker.logs", "none", acceptNow.Add(time.Hour))}, acceptNow)
	s := r.Summary()
	if want := (Summary{Counted: 3, Warnings: 1, Accepted: 1}); s != want {
		t.Errorf("summary = %+v, want %+v", s, want)
	}
	if got := ids(r.Findings()); !slices.Equal(got, []string{"cgroup.version"}) {
		t.Errorf("findings = %v, want only the unaccepted warning", got)
	}
	if n := len(r.Findings()); n != s.Warnings+s.Errors {
		t.Errorf("findings %d != warnings+errors %d", n, s.Warnings+s.Errors)
	}
	if w, e, _ := r.Counts(); w != 1 || e != 0 {
		t.Errorf("Counts = %d warnings, %d errors; doctor must agree with the page", w, e)
	}
}

// A host whose only warning was accepted exits 0, as the page says it is well.
func TestAHostWhoseOnlyWarningWasAcceptedExitsZero(t *testing.T) {
	r := Report{Results: []Result{warnWith("docker.logs", "none")}}
	if r.ExitCode() != 1 {
		t.Fatal("setup: an unaccepted warning must exit 1")
	}
	j := r.Judged([]Held{heldFor("docker.logs", "none", acceptNow.Add(time.Hour))}, acceptNow)
	if j.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", j.ExitCode())
	}
}

// The reboot is counted once, as the reboot; it can never be accepted, even by
// a stored row for it.
func TestThePendingRebootIsNeverAcceptedAway(t *testing.T) {
	r := Report{RebootPending: true, Results: []Result{warnWith(KernelPending, "5.15")}}.
		Judged([]Held{heldFor(KernelPending, "5.15", acceptNow.Add(time.Hour))}, acceptNow)
	if r.Results[0].Accepted != nil || r.Results[0].Acceptable {
		t.Error("the reboot row was judged")
	}
	if r.Summary().Accepted != 0 {
		t.Error("the reboot was counted as accepted")
	}
}

func TestTheResultMarksAreOmittedWhenThereIsNothingToSay(t *testing.T) {
	b, _ := json.Marshal(warnWith("docker.logs", "x"))
	for _, k := range []string{"accepted", "ended", "acceptable"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("%s present on a plain result: %s", k, b)
		}
	}
}
