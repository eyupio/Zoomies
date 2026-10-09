package updates

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// planNow is the instant every planner test decides at. Time is an argument, so
// a boundary can be placed to the second either side of it.
var planNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func planRelease(tag string, age time.Duration) Release {
	return Release{
		Tag:         tag,
		URL:         "https://github.com/eyupio/zoomies/releases/tag/" + tag,
		PublishedAt: planNow.Add(-age),
		Assets:      []string{"checksums.txt", "zoomies_linux_amd64", "zoomies_linux_arm64"},
	}
}

// planSnapshot is a controller on v1.3.5, the newest release, long public, in
// auto with the helper ready and nothing in flight. Each test changes what its
// behaviour turns on.
func planSnapshot() Snapshot {
	return Snapshot{
		Now:         planNow,
		Mode:        ModeAuto,
		Soak:        24 * time.Hour,
		Running:     "1.3.5",
		GOOS:        "linux",
		GOARCH:      "amd64",
		Releases:    []Release{planRelease("v1.3.4", 30*24*time.Hour), planRelease("v1.3.5", 10*24*time.Hour)},
		HelperReady: true,
	}
}

// planHost is a healthy linux host that can update itself, at version.
func planHost(id, name, version string, runners int) HostFacts {
	return HostFacts{
		ID: id, Name: name, Version: version, GOOS: "linux", GOARCH: "amd64",
		Healthy: true, CanSelfUpdate: true, ActiveRunners: runners,
	}
}

func runningRollout(target string) *Rollout {
	return &Rollout{ID: "rol_1", Target: target, State: "running"}
}

func openAttempt(id, scope, hostID, to string, age time.Duration) *Attempt {
	return &Attempt{ID: id, Scope: scope, HostID: hostID, To: to, State: "requested", RequestedAt: planNow.Add(-age)}
}

// decide runs the planner and checks what every plan must be, whatever it
// decided: every tag it names is a release tag, it starts at most one update, and
// what it says is for a person to read.
func decide(t *testing.T, s Snapshot) Plan {
	t.Helper()
	p := Decide(s)
	starts := 0
	for _, a := range p.Actions {
		if a.Tag != "" && !ValidTag(a.Tag) {
			t.Errorf("%s names the tag %q, which is not a release tag", a.Kind, a.Tag)
		}
		if a.Kind == ActionRequestController || a.Kind == ActionUpdateHost {
			starts++
		}
		if a.Reason == "" {
			t.Errorf("%s carries no reason", a.Kind)
		}
		checkWords(t, string(a.Kind)+"'s reason", a.Reason)
	}
	if starts > 1 {
		t.Errorf("the plan starts %d updates at once, want at most one: %+v", starts, p.Actions)
	}
	if p.Sentence == "" {
		t.Error("the plan has no sentence")
	}
	checkWords(t, "the sentence", p.Sentence)
	return p
}

// checkWords holds a sentence to the repository's voice and to what every role
// may read: no em dash or its stand-in, and no absolute path.
func checkWords(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"\u2014", " -- ", " /"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s contains %q: %q", what, bad, s)
		}
	}
}

func kinds(p Plan) []ActionKind {
	out := []ActionKind{}
	for _, a := range p.Actions {
		out = append(out, a.Kind)
	}
	return out
}

func wantKinds(t *testing.T, p Plan, want ...ActionKind) {
	t.Helper()
	if want == nil {
		want = []ActionKind{}
	}
	if got := kinds(p); !slices.Equal(got, want) {
		t.Fatalf("actions = %v, want %v (sentence %q)", got, want, p.Sentence)
	}
}

func wantSentence(t *testing.T, p Plan, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(p.Sentence, part) {
			t.Errorf("sentence %q does not say %q", p.Sentence, part)
		}
	}
}

// A fenced controller may be one of two pointed at one database, or one restored
// from a backup that has not been checked. Either way an update it started could
// be the second of two, so it starts, closes and halts nothing, and says why.
func TestDecideDoesNothingWhenFenced(t *testing.T) {
	due := planSnapshot()
	due.Releases = append(due.Releases, planRelease("v1.4.0", 48*time.Hour))

	rollout := planSnapshot()
	rollout.Rollout = runningRollout("v1.3.5")
	rollout.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}

	stale := planSnapshot()
	stale.Controller = openAttempt("upd_1", "controller", "", "v1.4.0", 3*time.Hour)

	off := planSnapshot()
	off.Mode = ModeOff
	off.Rollout = runningRollout("v1.3.5")

	failed := planSnapshot()
	failed.Rollout = runningRollout("v1.3.5")
	failed.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	failed.Hosts[0].FailedInRollout = "the download failed"

	for name, s := range map[string]Snapshot{
		"a controller due an update": due, "a running rollout": rollout, "an attempt past its time": stale,
		"updates off with a rollout": off, "a failure in the rollout": failed,
	} {
		t.Run(name, func(t *testing.T) {
			s.Fenced = true
			p := decide(t, s)
			wantKinds(t, p)
			wantSentence(t, p, "fenced")
		})
	}
}

// Switching updates off is the platform withdrawing the capability. A rollout
// still waiting to move would otherwise sit open until someone turned updates on
// again and be picked up as if nothing had happened, so it is cancelled; an
// update already handed to a helper is not this planner's to stop, and finishes
// by itself.
func TestDecideDoesNothingWhenOffExceptCancelAPendingRollout(t *testing.T) {
	behind := []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	for _, tc := range []struct {
		name    string
		mode    Mode
		rollout *Rollout
		want    []ActionKind
	}{
		{"a running rollout", ModeOff, runningRollout("v1.3.5"), []ActionKind{ActionCancelRollout}},
		{"a halted rollout", ModeOff, &Rollout{ID: "rol_1", Target: "v1.3.5", State: "halted"}, []ActionKind{ActionCancelRollout}},
		{"no rollout", ModeOff, nil, nil},
		{"a mode nothing knows, read as off", Mode("sometimes"), runningRollout("v1.3.5"), []ActionKind{ActionCancelRollout}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := planSnapshot()
			s.Mode = tc.mode
			s.Releases = append(s.Releases, planRelease("v1.4.0", 48*time.Hour))
			s.Hosts = behind
			s.Rollout = tc.rollout
			p := decide(t, s)
			wantKinds(t, p, tc.want...)
			if tc.rollout != nil {
				wantSentence(t, p, "off", "cancelled")
			} else if p.Sentence != Choose(ChooseInput{Mode: ModeOff}).Reason {
				t.Errorf("sentence = %q, want Choose's for off", p.Sentence)
			}
		})
	}

	// A time-out is bookkeeping, not a decision to update: an attempt asked for
	// before updates were switched off still has to be recorded as ended.
	t.Run("an attempt past its time is still timed out", func(t *testing.T) {
		s := planSnapshot()
		s.Mode = ModeOff
		s.Rollout = runningRollout("v1.3.5")
		s.Hosts = behind
		s.Hosts[0].Open = openAttempt("upd_h1", "host", "h1", "v1.3.5", 91*time.Minute)
		p := decide(t, s)
		wantKinds(t, p, ActionTimeOut, ActionCancelRollout)
		if p.Actions[0].AttemptID != "upd_h1" || p.Actions[0].HostID != "h1" {
			t.Errorf("time-out = %+v, want the host's attempt", p.Actions[0])
		}
	})
}

func soakingSnapshot(age time.Duration) Snapshot {
	s := planSnapshot()
	s.Releases = append(s.Releases, planRelease("v1.4.0", age))
	return s
}

func chooseFor(s Snapshot) Target {
	return Choose(ChooseInput{Mode: s.Mode, Soak: s.Soak, Now: s.Now, Running: s.Running, Releases: s.Releases, GOOS: s.GOOS, GOARCH: s.GOARCH})
}

func TestDecideRequestsTheControllerOnceTheSoakHasPassedInAuto(t *testing.T) {
	for _, age := range []time.Duration{24 * time.Hour, 25 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			s := soakingSnapshot(age)
			p := decide(t, s)
			wantKinds(t, p, ActionRequestController)
			if p.Actions[0].Tag != "v1.4.0" {
				t.Errorf("tag = %q, want v1.4.0", p.Actions[0].Tag)
			}
			if want := chooseFor(s).Reason; p.Sentence != want {
				t.Errorf("sentence = %q, want Choose's %q", p.Sentence, want)
			}
		})
	}
}

func TestDecideRequestsTheControllerNotBeforeTheSoakHasPassed(t *testing.T) {
	s := soakingSnapshot(24*time.Hour - time.Second)
	p := decide(t, s)
	wantKinds(t, p)
	if want := chooseFor(s).Reason; p.Sentence != want {
		t.Errorf("sentence = %q, want Choose's %q", p.Sentence, want)
	}
}

// In manual a person is the only one who moves anything: a release long past
// any soak, hosts behind and a ready helper are all still left for a click.
func TestDecideNeverRequestsTheControllerInManual(t *testing.T) {
	s := soakingSnapshot(30 * 24 * time.Hour)
	s.Mode = ModeManual
	p := decide(t, s)
	wantKinds(t, p)
	if want := chooseFor(s).Reason; p.Sentence != want {
		t.Errorf("sentence = %q, want Choose's %q", p.Sentence, want)
	}
}

func TestDecideNeverActsInManualMode(t *testing.T) {
	s := soakingSnapshot(30 * 24 * time.Hour)
	s.Mode = ModeManual
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0), planHost("h2", "runner-2", "1.3.3", 0)}
	p := decide(t, s)
	wantKinds(t, p)
	if want := chooseFor(s).Reason; p.Sentence != want {
		t.Errorf("sentence = %q, want Choose's %q", p.Sentence, want)
	}
}

// A person who presses "update every host that is behind" in manual has made
// the one click manual waits for. The rollout it starts moves one host at a
// time, so the planner carries it on; it never starts one of its own.
func TestDecideCarriesOnARolloutSomeoneStartedInManual(t *testing.T) {
	s := planSnapshot()
	s.Mode = ModeManual
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	p := decide(t, s)
	wantKinds(t, p, ActionUpdateHost)

	s.Hosts[0].Version = "1.3.5"
	wantKinds(t, decide(t, s), ActionFinishRollout)
}

func TestDecideWaitsForTheHelperAndSaysSo(t *testing.T) {
	s := soakingSnapshot(48 * time.Hour)
	s.HelperReady = false
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	p := decide(t, s)
	wantKinds(t, p)
	wantSentence(t, p, "v1.4.0", "sudo zoomies updates helper install")
}

func TestDecideStartsNoHostWhileTheControllerAttemptIsOpen(t *testing.T) {
	for _, rollout := range []*Rollout{nil, runningRollout("v1.3.5")} {
		t.Run(fmt.Sprintf("rollout %v", rollout != nil), func(t *testing.T) {
			s := planSnapshot()
			s.Rollout = rollout
			s.Controller = openAttempt("upd_c", "controller", "", "v1.4.0", 10*time.Minute)
			s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
			p := decide(t, s)
			wantKinds(t, p)
			wantSentence(t, p, "controller", "v1.4.0")
		})
	}
}

// Updating the controller first means a host is never updated twice in a row,
// once to the old release and again minutes later, and never left on a release
// the controller has just moved off. It also comes before the next host of a
// rollout already running.
func TestDecideUpdatesTheControllerBeforeAnyHost(t *testing.T) {
	s := soakingSnapshot(48 * time.Hour)
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	wantKinds(t, decide(t, s), ActionRequestController)

	s.Rollout = nil
	wantKinds(t, decide(t, s), ActionRequestController)

	// First, but not on top of a host's update already under way: restarting
	// the controller then would leave two machines changing at once.
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts[0].Open = openAttempt("upd_h1", "host", "h1", "v1.3.5", 5*time.Minute)
	p := decide(t, s)
	wantKinds(t, p)
	wantSentence(t, p, "runner-1", "nothing else starts")
}

func TestDecideStartsARolloutInAutoWhenAHostIsBehindTheControllersRelease(t *testing.T) {
	s := planSnapshot()
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0), planHost("h2", "runner-2", "1.3.5", 0)}
	p := decide(t, s)
	wantKinds(t, p, ActionStartRollout)
	if p.Actions[0].Tag != "v1.3.5" {
		t.Errorf("tag = %q, want v1.3.5", p.Actions[0].Tag)
	}
	wantSentence(t, p, "v1.3.5", "1 host is behind")
}

// A controller built from main has no release to name, so there is nothing a
// host could be taken to that the helper would accept.
func TestDecideStartsARolloutInAutoNotWhenTheControllerHasNoTarget(t *testing.T) {
	s := planSnapshot()
	s.Running = "main-sha-abc1234"
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	p := decide(t, s)
	wantKinds(t, p)
	if want := chooseFor(s).Reason; p.Sentence != want {
		t.Errorf("sentence = %q, want Choose's %q", p.Sentence, want)
	}
}

// The host running the fewest jobs goes first, because an update pulls images
// and restarts the agent, and the fewer jobs share that host, the less is slowed.
func TestDecideUpdatesTheHostWithTheFewestRunnersFirst(t *testing.T) {
	s := planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{
		planHost("h1", "alpha", "1.3.4", 3),
		planHost("h2", "charlie", "1.3.4", 0),
		planHost("h3", "bravo", "1.3.4", 0),
	}
	p := decide(t, s)
	wantKinds(t, p, ActionUpdateHost)
	if got := p.Actions[0]; got.HostID != "h3" || got.Tag != "v1.3.5" {
		t.Errorf("updated %+v, want bravo (h3) to v1.3.5", got)
	}
	wantSentence(t, p, "bravo")
}

// Names are not unique across a re-join, so two hosts can tie on both; the id
// still puts one first, the same one every pass.
func TestDecideOrdersHostsEqualInRunnersAndNameByID(t *testing.T) {
	s := planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h9", "runner", "1.3.4", 1), planHost("h2", "runner", "1.3.4", 1)}
	p := decide(t, s)
	wantKinds(t, p, ActionUpdateHost)
	if p.Actions[0].HostID != "h2" {
		t.Errorf("updated %s, want h2", p.Actions[0].HostID)
	}
}

func TestDecideStartsOnlyOneHostAtATime(t *testing.T) {
	s := planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0), planHost("h2", "runner-2", "1.3.4", 0)}
	p := decide(t, s)
	wantKinds(t, p, ActionUpdateHost)

	// The second host has more runners, but its attempt being open is what keeps
	// the first from starting beside it.
	s.Hosts[1].ActiveRunners = 4
	s.Hosts[1].Open = openAttempt("upd_h2", "host", "h2", "v1.3.5", 5*time.Minute)
	p = decide(t, s)
	wantKinds(t, p)
	wantSentence(t, p, "runner-2", "v1.3.5")

	// A host updated by a button outside the rollout counts as well.
	s.Rollout = nil
	wantKinds(t, decide(t, s))
}

func TestDecideSkipsEmbeddedAheadUnhealthyAndCannotUpdateHosts(t *testing.T) {
	why := "This host's agent does not offer to update itself, which it does only once the update helper is installed on the host."
	for _, tc := range []struct {
		name   string
		host   func(h *HostFacts)
		want   ActionKind // what the rollout does with only this host behind
		saying string
	}{
		{"embedded", func(h *HostFacts) { h.Embedded = true }, ActionFinishRollout, "updated with the controller"},
		{"ahead", func(h *HostFacts) { h.Version = "1.4.0" }, ActionFinishRollout, "never taken back"},
		{"not a release", func(h *HostFacts) { h.Version = "main-sha-abc1234" }, ActionFinishRollout, "not a release"},
		{"version unknown", func(h *HostFacts) { h.Version = "" }, ActionFinishRollout, "has not said which version"},
		{"cannot update itself", func(h *HostFacts) { h.CanSelfUpdate, h.WhyNot = false, why }, ActionFinishRollout, why},
		{"no binary for its system", func(h *HostFacts) { h.GOOS, h.GOARCH = "linux", "riscv64" }, ActionFinishRollout, "carries no binary"},
		{"unhealthy", func(h *HostFacts) { h.Healthy = false }, "", "not answering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := planSnapshot()
			s.Rollout = runningRollout("v1.3.5")
			h := planHost("h1", "runner-1", "1.3.4", 0)
			tc.host(&h)
			s.Hosts = []HostFacts{h}
			p := decide(t, s)
			if tc.want == "" {
				wantKinds(t, p)
			} else {
				wantKinds(t, p, tc.want)
			}
			wantSentence(t, p, "runner-1", tc.saying)

			// Skipped, it does not hold up a host that can be updated, though that
			// host runs more jobs.
			s.Hosts = append(s.Hosts, planHost("h2", "runner-2", "1.3.4", 5))
			p = decide(t, s)
			wantKinds(t, p, ActionUpdateHost)
			if p.Actions[0].HostID != "h2" {
				t.Errorf("updated %s, want h2", p.Actions[0].HostID)
			}

			// And it is never the reason a rollout starts.
			s.Rollout = nil
			s.Hosts = s.Hosts[:1]
			p = decide(t, s)
			wantKinds(t, p)
			wantSentence(t, p, tc.saying)
		})
	}
}

// The first failure stops the walk: a release that broke one host is likely to
// break the next, and an operator should see the first before there is a second.
func TestDecideHaltsAfterAFailureAndStartsNothing(t *testing.T) {
	s := planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0), planHost("h2", "runner-2", "1.3.4", 1)}
	s.Hosts[1].FailedInRollout = "the download failed"
	p := decide(t, s)
	wantKinds(t, p, ActionHalt)
	if p.Actions[0].HostID != "h2" {
		t.Errorf("halted on %s, want h2", p.Actions[0].HostID)
	}
	if !strings.Contains(p.Actions[0].Reason, "runner-2") {
		t.Errorf("halt reason %q does not name the host", p.Actions[0].Reason)
	}
	wantSentence(t, p, "runner-2", "resume", "cancel")

	// Halted, nothing moves: not the next host, and not the controller either,
	// though a newer release is due.
	s.Rollout.State = "halted"
	s.Hosts[1].FailedInRollout = ""
	s.Releases = append(s.Releases, planRelease("v1.4.0", 48*time.Hour))
	p = decide(t, s)
	wantKinds(t, p)
	wantSentence(t, p, "halted", "resume", "cancel")
}

// The engine's own worst case is fifty minutes, so ninety is past any update
// still working. Exactly ninety is still waiting, as the controller's own closer
// has it, so the two never disagree about the minute.
func TestDecideTimesOutAnAttemptAfterNinetyMinutes(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want bool
	}{{89 * time.Minute, false}, {90 * time.Minute, false}, {90*time.Minute + time.Second, true}, {91 * time.Minute, true}} {
		for _, mode := range []Mode{ModeAuto, ModeManual} {
			t.Run(fmt.Sprintf("controller %s %s", tc.age, mode), func(t *testing.T) {
				s := planSnapshot()
				s.Mode = mode
				s.Controller = openAttempt("upd_c", "controller", "", "v1.4.0", tc.age)
				p := decide(t, s)
				checkTimedOut(t, p, tc.want, "upd_c", "")
			})
			t.Run(fmt.Sprintf("host %s %s", tc.age, mode), func(t *testing.T) {
				s := planSnapshot()
				s.Mode = mode
				s.Rollout = runningRollout("v1.3.5")
				s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0), planHost("h2", "runner-2", "1.3.4", 0)}
				s.Hosts[0].Open = openAttempt("upd_h1", "host", "h1", "v1.3.5", tc.age)
				p := decide(t, s)
				checkTimedOut(t, p, tc.want, "upd_h1", "h1")
			})
		}
	}
}

// checkTimedOut says the plan times out the one attempt or none, and, either
// way, starts nothing beside it: an attempt being closed is still open this pass.
func checkTimedOut(t *testing.T, p Plan, want bool, attemptID, hostID string) {
	t.Helper()
	if !want {
		wantKinds(t, p)
		return
	}
	wantKinds(t, p, ActionTimeOut)
	if got := p.Actions[0]; got.AttemptID != attemptID || got.HostID != hostID {
		t.Errorf("timed out %+v, want attempt %s on host %q", got, attemptID, hostID)
	}
	wantSentence(t, p, "90 minutes")
}

func TestDecideWaitsThirtyMinutesAfterAFailure(t *testing.T) {
	for _, tc := range []struct {
		since time.Duration
		want  bool
	}{{29 * time.Minute, false}, {30*time.Minute - time.Second, false}, {30 * time.Minute, true}} {
		t.Run("controller "+tc.since.String(), func(t *testing.T) {
			s := soakingSnapshot(48 * time.Hour)
			s.ControllerFailures = 1
			s.ControllerLastFailedAt = planNow.Add(-tc.since)
			p := decide(t, s)
			if tc.want {
				wantKinds(t, p, ActionRequestController)
				return
			}
			wantKinds(t, p)
			wantSentence(t, p, "again in")
		})
		t.Run("host "+tc.since.String(), func(t *testing.T) {
			s := planSnapshot()
			s.Rollout = runningRollout("v1.3.5")
			s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
			s.Hosts[0].Failures = 1
			s.Hosts[0].LastFailedAt = planNow.Add(-tc.since)
			p := decide(t, s)
			if tc.want {
				wantKinds(t, p, ActionUpdateHost)
				return
			}
			// Waiting is not finishing: the host is still behind and will be tried.
			wantKinds(t, p)
			wantSentence(t, p, "runner-1", "again in")
		})
	}
}

// Two failures of one release on one machine is a pattern, not bad luck, and a
// third try would only fill the log. A person looks first.
func TestDecideWaitsForAnOperatorAfterTwoFailuresOfOneTag(t *testing.T) {
	t.Run("controller", func(t *testing.T) {
		s := soakingSnapshot(48 * time.Hour)
		s.ControllerFailures = 2
		s.ControllerLastFailedAt = planNow.Add(-5 * time.Hour)
		s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
		p := decide(t, s)
		wantKinds(t, p)
		wantSentence(t, p, "v1.4.0", "an operator must act")

		s.ControllerFailures = 1
		wantKinds(t, decide(t, s), ActionRequestController)
	})
	t.Run("host", func(t *testing.T) {
		s := planSnapshot()
		s.Rollout = runningRollout("v1.3.5")
		s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
		s.Hosts[0].Failures = 2
		s.Hosts[0].LastFailedAt = planNow.Add(-5 * time.Hour)
		p := decide(t, s)
		wantKinds(t, p, ActionFinishRollout)
		wantSentence(t, p, "runner-1", "an operator must act")

		s.Rollout = nil
		p = decide(t, s)
		wantKinds(t, p)
		wantSentence(t, p, "an operator must act")

		s.Rollout = runningRollout("v1.3.5")
		s.Hosts[0].Failures = 1
		wantKinds(t, decide(t, s), ActionUpdateHost)
	})
}

// A host follows its controller, never "latest": an agent ahead of its
// controller is the direction of skew nothing tests.
func TestDecideTargetsTheControllersReleaseNotLatest(t *testing.T) {
	s := soakingSnapshot(time.Hour)
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	p := decide(t, s)
	wantKinds(t, p, ActionStartRollout)
	if p.Actions[0].Tag != "v1.3.5" {
		t.Errorf("rollout to %q, want the controller's v1.3.5", p.Actions[0].Tag)
	}

	s.Rollout = runningRollout("v1.3.5")
	p = decide(t, s)
	wantKinds(t, p, ActionUpdateHost)
	if p.Actions[0].Tag != "v1.3.5" {
		t.Errorf("host to %q, want the controller's v1.3.5", p.Actions[0].Tag)
	}
}

func TestDecideFinishesARolloutWhenNoHostIsBehind(t *testing.T) {
	for name, hosts := range map[string][]HostFacts{
		"every host on the release": {planHost("h1", "runner-1", "1.3.5", 0), planHost("h2", "runner-2", "v1.3.5", 2)},
		"no hosts at all":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			s := planSnapshot()
			s.Rollout = runningRollout("v1.3.5")
			s.Hosts = hosts
			p := decide(t, s)
			wantKinds(t, p, ActionFinishRollout)
			wantSentence(t, p, "v1.3.5", "finished")
		})
	}
	t.Run("not while a host is still updating", func(t *testing.T) {
		s := planSnapshot()
		s.Rollout = runningRollout("v1.3.5")
		s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.5", 0)}
		s.Hosts[0].Open = openAttempt("upd_h1", "host", "h1", "v1.3.5", time.Minute)
		wantKinds(t, decide(t, s))
	})
}

// Release binaries say 1.3.5 where the tag says v1.3.5, and 1.3.10 sorts before
// 1.3.9 as text. Equality of strings would update a host already there, and an
// order of strings would take a host backwards.
func TestDecideComparesVersionsAsReleasesNotText(t *testing.T) {
	s := planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.5", 0)}
	wantKinds(t, decide(t, s), ActionFinishRollout)

	s.Running = "1.3.9"
	s.Releases = append(s.Releases, planRelease("v1.3.9", 5*24*time.Hour))
	s.Rollout = runningRollout("v1.3.9")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.10", 0)}
	p := decide(t, s)
	wantKinds(t, p, ActionFinishRollout)
	wantSentence(t, p, "never taken back")
}

// The controller can move while a rollout is under way: auto updates it between
// two hosts once a newer release has soaked, or a person updates it by hand. A
// rollout to the release it has left is finished rather than carried on, since
// every host it would still update would at once be behind again; the next pass
// starts a rollout to where the controller is now. A rollout aimed past the
// controller is cancelled, because finishing it would send hosts ahead of it.
func TestDecideFinishesOrCancelsARolloutTheControllerHasMovedPast(t *testing.T) {
	for _, tc := range []struct {
		name, running, target string
		want                  ActionKind
		saying                string
	}{
		{"the controller moved on", "1.4.0", "v1.3.5", ActionFinishRollout, "v1.4.0"},
		{"the controller went back", "1.3.5", "v1.4.0", ActionCancelRollout, "never taken past"},
		{"the controller is not a release", "main-sha-abc1234", "v1.3.5", ActionCancelRollout, "no longer runs a release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := planSnapshot()
			// Still inside its soak, so a controller behind it is not due to move
			// and the rollout is all this pass decides about.
			s.Releases = append(s.Releases, planRelease("v1.4.0", time.Hour))
			s.Running = tc.running
			s.Rollout = runningRollout(tc.target)
			s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
			p := decide(t, s)
			wantKinds(t, p, tc.want)
			wantSentence(t, p, tc.saying)
		})
	}

	// With the old rollout finished, the next pass takes the hosts to the new one.
	s := planSnapshot()
	s.Releases = append(s.Releases, planRelease("v1.4.0", 5*24*time.Hour))
	s.Running = "1.4.0"
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	p := decide(t, s)
	wantKinds(t, p, ActionStartRollout)
	if p.Actions[0].Tag != "v1.4.0" {
		t.Errorf("rollout to %q, want v1.4.0", p.Actions[0].Tag)
	}
}

// A host deleted or re-joined under a new id leaves its attempt behind, and the
// store closes it. Until then the planner must not wait on it, time it out under
// another host's name, or read it as the controller's own.
func TestDecideIgnoresAnAttemptForAHostThatIsNotInTheSnapshot(t *testing.T) {
	gone := openAttempt("upd_gone", "host", "h_gone", "v1.3.5", 3*time.Hour)

	s := soakingSnapshot(48 * time.Hour)
	s.Controller = gone
	wantKinds(t, decide(t, s), ActionRequestController)

	s = planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
	s.Hosts[0].Open = gone
	p := decide(t, s)
	wantKinds(t, p, ActionUpdateHost)

	// An attempt that has already ended is not open either.
	s.Hosts[0].Open = openAttempt("upd_h1", "host", "h1", "v1.3.5", 3*time.Hour)
	s.Hosts[0].Open.State = "failed"
	wantKinds(t, decide(t, s), ActionUpdateHost)
}

// Every tag the planner names goes into a request a root helper reads, so it is
// one the snapshot held and ValidTag passed, never one made up or passed through.
func TestDecideNamesOnlyReleaseTags(t *testing.T) {
	for _, tag := range []string{"v1.3.5; reboot", "latest", "V1.3.5", "v1.3.5\n"} {
		t.Run(fmt.Sprintf("%q", tag), func(t *testing.T) {
			s := planSnapshot()
			s.Rollout = runningRollout(tag)
			s.Hosts = []HostFacts{planHost("h1", "runner-1", "1.3.4", 0)}
			p := decide(t, s)
			wantKinds(t, p)
			wantSentence(t, p, "not a release tag", "Cancel it")
			if strings.Contains(p.Sentence, tag) {
				t.Errorf("sentence %q repeats the tag", p.Sentence)
			}
		})
	}
	t.Run("a release whose tag is not one", func(t *testing.T) {
		s := planSnapshot()
		for _, tag := range []string{"V1.4.0", "v1.4.0-rc1", "v1.4.0 "} {
			s.Releases = append(s.Releases, planRelease(tag, 48*time.Hour))
		}
		p := decide(t, s)
		wantKinds(t, p)
		if want := chooseFor(s).Reason; p.Sentence != want {
			t.Errorf("sentence = %q, want Choose's %q", p.Sentence, want)
		}
	})
}

// The plan is recomputed every few seconds from rows read in whatever order the
// database and a map hand them over. Were the answer to follow that order, two
// passes over one fleet could pick different hosts, and the sentence on the page
// would flicker between them.
func TestDecideIsDeterministic(t *testing.T) {
	waiting := planSnapshot()
	waiting.Rollout = runningRollout("v1.3.5")
	waiting.Releases = append(waiting.Releases, planRelease("v1.4.0", time.Hour), planRelease("v1.3.6", 2*time.Hour), planRelease("v1.3.6", 3*time.Hour))
	waiting.Hosts = []HostFacts{
		planHost("h1", "alpha", "1.3.4", 2), planHost("h2", "bravo", "1.3.4", 0), planHost("h3", "bravo", "1.3.4", 0),
		planHost("h4", "charlie", "1.3.5", 0), planHost("h5", "delta", "1.3.4", 1),
	}
	waiting.Hosts[1].Healthy = false
	waiting.Hosts[4].CanSelfUpdate, waiting.Hosts[4].WhyNot = false, "No helper."

	starting := waiting
	starting.Rollout = nil

	halting := waiting
	halting.Hosts = slices.Clone(waiting.Hosts)
	halting.Hosts[0].FailedInRollout = "it broke"
	halting.Hosts[3].FailedInRollout = "it broke too"

	finishing := waiting
	finishing.Hosts = slices.Clone(waiting.Hosts)
	for i := range finishing.Hosts {
		finishing.Hosts[i].CanSelfUpdate, finishing.Hosts[i].WhyNot = false, fmt.Sprintf("Reason %d.", i)
	}

	timing := waiting
	timing.Hosts = slices.Clone(waiting.Hosts)
	timing.Hosts[0].Open = openAttempt("upd_1", "host", "h1", "v1.3.5", 2*time.Hour)
	timing.Hosts[2].Open = openAttempt("upd_3", "host", "h3", "v1.3.5", 3*time.Hour)

	rng := rand.New(rand.NewPCG(1, 2))
	for name, s := range map[string]Snapshot{
		"waiting": waiting, "starting": starting, "halting": halting, "finishing": finishing, "timing out": timing,
	} {
		t.Run(name, func(t *testing.T) {
			want := fmt.Sprintf("%#v", decide(t, s))
			for range 50 {
				shuffled := s
				shuffled.Hosts = slices.Clone(s.Hosts)
				shuffled.Releases = slices.Clone(s.Releases)
				rng.Shuffle(len(shuffled.Hosts), func(i, j int) { shuffled.Hosts[i], shuffled.Hosts[j] = shuffled.Hosts[j], shuffled.Hosts[i] })
				rng.Shuffle(len(shuffled.Releases), func(i, j int) {
					shuffled.Releases[i], shuffled.Releases[j] = shuffled.Releases[j], shuffled.Releases[i]
				})
				if got := fmt.Sprintf("%#v", Decide(shuffled)); got != want {
					t.Fatalf("a shuffled snapshot planned\n%s\nwant\n%s", got, want)
				}
			}
		})
	}
}

// The caller's slices are the controller's own; sorting them in place would
// reorder what it shows.
func TestDecideLeavesTheSnapshotAsItWasGiven(t *testing.T) {
	s := planSnapshot()
	s.Rollout = runningRollout("v1.3.5")
	s.Hosts = []HostFacts{planHost("h2", "bravo", "1.3.4", 3), planHost("h1", "alpha", "1.3.4", 0)}
	s.Releases = []Release{planRelease("v1.3.5", 48*time.Hour), planRelease("v1.3.4", 96*time.Hour)}
	hosts, releases := slices.Clone(s.Hosts), slices.Clone(s.Releases)
	Decide(s)
	if !reflect.DeepEqual(s.Hosts, hosts) || !reflect.DeepEqual(s.Releases, releases) {
		t.Error("Decide reordered the snapshot it was given")
	}
}
