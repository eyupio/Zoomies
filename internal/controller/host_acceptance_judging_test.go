package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// accept stores an operator's acceptance of a check as the operation does,
// without the operation, so these tests are about judging and not about the
// door the acceptance came in by.
func (h *harness) accept(t *testing.T, host *store.Host, checkID, current string, ends time.Time) {
	t.Helper()
	a := &store.HostCheckAcceptance{HostID: host.ID, CheckID: checkID, Current: current,
		Reason: "pinned on purpose", By: "usr_1", ByName: "Sam", ExpiresAt: ends}
	if err := h.st.UpsertHostCheckAcceptance(h.ctx, a); err != nil {
		t.Fatalf("UpsertHostCheckAcceptance: %v", err)
	}
}

// The failure this is designed out is two surfaces disagreeing: a page that
// calls a warning accepted while the problems list, the metrics or the CLI
// still count it. All of them read the judged report, so one acceptance has to
// move every one of them together.
func TestAnAcceptedWarningLeavesTheProblemTheMetricsAndTheViewTogether(t *testing.T) {
	h := newHarness(t)
	host := h.host("accepting")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	if got := h.problemsOf(t, "host.os_health"); len(got) != 1 {
		t.Fatalf("before the acceptance there are %d host.os_health problems, want 1", len(got))
	}

	h.accept(t, host, "docker.logs", "AGENT CURRENT docker.logs", h.c.Now().Add(24*time.Hour))

	if got := h.problemsOf(t, "host.os_health"); len(got) != 0 {
		t.Errorf("an accepted warning still raises host.os_health: %+v", got)
	}
	for state, want := range map[string]float64{"warning": 0, "accepted": 1, "error": 0} {
		got, ok := gatherValue(t, h.c, "zoomies_host_os_checks", map[string]string{"host": host.ID, "state": state})
		if !ok || got != want {
			t.Errorf("zoomies_host_os_checks{state=%q} = %v (present=%v), want %v", state, got, ok, want)
		}
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := h.c.HostView(got).Doctor
	if v == nil || v.Summary.Accepted != 1 || v.Summary.Warnings != 0 || v.Summary.Counted != 1 {
		t.Fatalf("the view counts %+v, want one counted check, accepted and not a warning", v)
	}
	if r := v.Results[0]; r.Accepted == nil || r.Accepted.By != "Sam" || r.Accepted.Reason != "pinned on purpose" {
		t.Errorf("the view's result carries no acceptance: %+v", r)
	}
	// The stored report is the agent's and stays what it sent.
	if got.Doctor.Results[0].Accepted != nil {
		t.Error("judging wrote the acceptance into the stored report")
	}
}

// Accepting a warning must not hide a different one. The problem is raised for
// what is still unaccepted, and its words name only that.
func TestAnAcceptedWarningDoesNotHideAnotherOnTheSameHost(t *testing.T) {
	h := newHarness(t)
	host := h.host("two-findings")
	h.reports(t, host, report(h.c.Now(),
		check("docker.logs", hosttune.Safe, hosttune.Warn, false),
		check("inotify.watches", hosttune.Safe, hosttune.Warn, false)))
	h.accept(t, host, "docker.logs", "AGENT CURRENT docker.logs", h.c.Now().Add(time.Hour))

	ps := h.problemsOf(t, "host.os_health")
	if len(ps) != 1 || ps[0].Title != "host two-findings has an OS health check that needs attention" {
		t.Fatalf("problems = %+v, want one for the single unaccepted warning", ps)
	}
}

// An acceptance ends when the value moves or the date passes. Either way the
// check counts again straight away, without anyone revoking anything, which is
// what makes an acceptance safe to grant.
func TestAnAcceptanceThatNoLongerHoldsCountsAgain(t *testing.T) {
	h := newHarness(t)
	host := h.host("lapsing")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	h.accept(t, host, "docker.logs", "it read something else", h.c.Now().Add(24*time.Hour))
	if got := h.problemsOf(t, "host.os_health"); len(got) != 1 {
		t.Errorf("a value that changed since the acceptance left %d problems, want 1", len(got))
	}

	h.accept(t, host, "docker.logs", "AGENT CURRENT docker.logs", h.c.Now().Add(2*time.Second))
	if got := h.problemsOf(t, "host.os_health"); len(got) != 0 {
		t.Fatalf("a live acceptance left %d problems, want 0", len(got))
	}
	// Short enough that the host is still connected when the term is up.
	h.advance(5 * time.Second)
	if got := h.problemsOf(t, "host.os_health"); len(got) != 1 {
		t.Errorf("an expired acceptance left %d problems, want 1", len(got))
	}
}

// The reboot is a fact about the machine and not a setting someone can
// decide to live with. Even a stored acceptance for it (which the operation
// refuses to make) must leave the problem, the metric and the flag alone.
func TestARebootIsNeverSilencedByAnAcceptance(t *testing.T) {
	h := newHarness(t)
	host := h.host("rebooting")
	r := report(h.c.Now(), check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
	r.RebootPending = true
	h.reports(t, host, r)
	h.accept(t, host, hosttune.KernelPending, "AGENT CURRENT "+hosttune.KernelPending, h.c.Now().Add(24*time.Hour))

	if got := h.problemsOf(t, "host.reboot_pending"); len(got) != 1 {
		t.Errorf("host.reboot_pending problems = %d, want 1", len(got))
	}
	if got, ok := gatherValue(t, h.c, "zoomies_host_reboot_pending", map[string]string{"host": host.ID}); !ok || got != 1 {
		t.Errorf("zoomies_host_reboot_pending = %v (present=%v), want 1", got, ok)
	}
	if got, ok := gatherValue(t, h.c, "zoomies_host_os_checks", map[string]string{"host": host.ID, "state": "accepted"}); !ok || got != 0 {
		t.Errorf("accepted = %v (present=%v), want 0", got, ok)
	}
}

// A heartbeat replaces the stored report and publishes the host straight from
// memory. If that in-memory host lacked its acceptances the frame would
// un-accept every row on the page for a beat, and the next beat would put it
// back, so the table would flicker at the heartbeat rate.
func TestAHeartbeatFrameKeepsAnAcceptedRowAccepted(t *testing.T) {
	h := newHarness(t)
	host := h.host("beating")
	h.reports(t, host, report(h.c.Now().Add(-time.Minute), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	h.accept(t, host, "docker.logs", "AGENT CURRENT docker.logs", h.c.Now().Add(24*time.Hour))

	sub := h.c.Events().Subscribe(h.ctx, events.SubscribeOptions{Kinds: []events.Kind{events.KindHostUpdated}})
	defer sub.Close()
	next := report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false))
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: next}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-sub.C:
		var live HostView
		if err := json.Unmarshal(e.Data, &live); err != nil {
			t.Fatal(err)
		}
		if live.Doctor == nil || live.Doctor.Summary.Accepted != 1 || live.Doctor.Summary.Warnings != 0 || live.Doctor.Results[0].Accepted == nil {
			t.Errorf("the heartbeat frame un-accepted the row: %+v", live.Doctor)
		}
	case <-time.After(time.Second):
		t.Fatal("no host.updated frame after the heartbeat")
	}
}
