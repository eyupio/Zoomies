package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

var sam = HostActor{ID: "usr_1", Name: "Sam"}

const goodReason = "pinned below the baseline on purpose"

func (h *harness) acceptIn(check, seen string) AcceptInput {
	return AcceptInput{CheckID: check, SeenCurrent: seen, Reason: goodReason, ExpiresAt: h.c.Now().Add(30 * 24 * time.Hour)}
}

func acceptFieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var bad *HostAcceptInvalidError
	if !errors.As(err, &bad) {
		t.Fatalf("error = %v, want a HostAcceptInvalidError", err)
	}
	m := map[string]string{}
	for _, f := range bad.Fields {
		m[f.Field] = f.Message
	}
	return m
}

// The value that is stored is the report's, because an acceptance that could be
// made to cover text the host never reported would be a way to pre-accept a
// future warning.
func TestAcceptingACheckStoresTheReportsValueAndPublishesTheHost(t *testing.T) {
	h := newHarness(t)
	host := h.host("accepting-op")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))

	v, a, err := h.c.AcceptHostCheck(h.ctx, host.ID, h.acceptIn("docker.logs", "AGENT CURRENT docker.logs"), sam)
	if err != nil {
		t.Fatalf("AcceptHostCheck: %v", err)
	}
	if a.Current != "AGENT CURRENT docker.logs" || a.ByName != "Sam" || a.Reason != goodReason {
		t.Errorf("stored %+v", a)
	}
	if v.Doctor == nil || v.Doctor.Summary.Accepted != 1 || v.Doctor.Summary.Warnings != 0 {
		t.Errorf("the answer counts %+v, want one accepted and no warning", v.Doctor)
	}
	if got := h.problemsOf(t, "host.os_health"); len(got) != 0 {
		t.Errorf("an accepted warning raises %+v", got)
	}
}

// Everything wrong that can be said without the host is said at once, so a form
// does not send a person round it three times.
func TestAcceptingReportsEveryFieldErrorAtOnce(t *testing.T) {
	h := newHarness(t)
	host := h.host("accept-fields")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	now := h.c.Now()

	cases := []struct {
		name  string
		edit  func(*AcceptInput)
		field string
	}{
		{"a reason of nine characters", func(in *AcceptInput) { in.Reason = "123456789" }, "reason"},
		{"a reason of 501 characters", func(in *AcceptInput) { in.Reason = strings.Repeat("a", 501) }, "reason"},
		{"a reason with a direction override", func(in *AcceptInput) { in.Reason = goodReason + "‮" }, "reason"},
		{"no end", func(in *AcceptInput) { in.ExpiresAt = time.Time{} }, "expires_at"},
		{"an end in the past", func(in *AcceptInput) { in.ExpiresAt = now.Add(-time.Hour) }, "expires_at"},
		{"an end past 365 days", func(in *AcceptInput) { in.ExpiresAt = now.Add(hosttune.MaxAcceptance + time.Hour) }, "expires_at"},
		{"no check", func(in *AcceptInput) { in.CheckID = "" }, "check_id"},
	}
	for _, tc := range cases {
		in := h.acceptIn("docker.logs", "AGENT CURRENT docker.logs")
		tc.edit(&in)
		_, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, in, sam)
		if _, ok := acceptFieldsOf(t, err)[tc.field]; !ok {
			t.Errorf("%s: no error on %s: %v", tc.name, tc.field, err)
		}
	}

	in := h.acceptIn("docker.logs", "x")
	in.Reason, in.ExpiresAt = "short", time.Time{}
	_, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, in, sam)
	if got := acceptFieldsOf(t, err); len(got) != 2 {
		t.Errorf("fields = %v, want reason and expires_at together", got)
	}

	// Exactly 365 days is the last instant that is allowed.
	in = h.acceptIn("docker.logs", "AGENT CURRENT docker.logs")
	in.ExpiresAt = now.Add(hosttune.MaxAcceptance)
	if _, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, in, sam); err != nil {
		t.Errorf("an end exactly 365 days away is refused: %v", err)
	}
}

// Only a counted Warn on a catalogue check may be accepted. Each refusal names
// what the person can do instead, and none of them writes a row.
func TestOnlyACountedWarnOnACatalogueCheckCanBeAccepted(t *testing.T) {
	h := newHarness(t)
	host := h.host("accept-refusals")
	h.reports(t, host, report(h.c.Now(),
		check("docker.logs", hosttune.Safe, hosttune.Error, false),
		check("files.service", hosttune.Safe, hosttune.Skip, false),
		check("cgroup.version", hosttune.Safe, hosttune.OK, false),
		check("disk.space", hosttune.Safe, hosttune.Warn, false),
		check("disk.inodes", hosttune.Safe, hosttune.Warn, false),
		check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false),
		check("vm.swappiness", hosttune.Safe, hosttune.Warn, false),
		check("tmp.tmpfs", hosttune.Aggressive, hosttune.Warn, false),
	))
	for _, tc := range []struct{ id, want string }{
		{"docker.logs", "could not run"},
		{"files.service", "not a warning"},
		{"cgroup.version", "not a warning"},
		{"disk.space", "Free space moves"},
		{"disk.inodes", "Free space moves"},
		{hosttune.KernelPending, "pending reboot"},
		{"vm.swappiness", "does not know a check"},
		{"tmp.tmpfs", "suggestion"},
		{"never.reported", "does not know a check"},
	} {
		_, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, h.acceptIn(tc.id, "AGENT CURRENT "+tc.id), sam)
		if msg := acceptFieldsOf(t, err)["check_id"]; !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %q does not say %q", tc.id, msg, tc.want)
		}
	}
	got, _ := h.st.GetHost(h.ctx, host.ID)
	if len(got.Acceptances) != 0 {
		t.Errorf("a refused request left %+v", got.Acceptances)
	}
}

// A catalogue check the host has not reported at all is refused too: there is
// no value to hold the acceptance to.
func TestAcceptingACheckTheHostDidNotReportIsRefused(t *testing.T) {
	h := newHarness(t)
	host := h.host("accept-absent")
	h.reports(t, host, report(h.c.Now(), check("cgroup.version", hosttune.Safe, hosttune.Warn, false)))
	_, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, h.acceptIn("docker.logs", "x"), sam)
	if !strings.Contains(acceptFieldsOf(t, err)["check_id"], "not a warning") {
		t.Errorf("error = %v", err)
	}
}

func TestAHostWithoutAFullReportHasNothingToAccept(t *testing.T) {
	h := newHarness(t)
	none := h.host("accept-no-report")
	_, _, err := h.c.AcceptHostCheck(h.ctx, none.ID, h.acceptIn("docker.logs", "x"), sam)
	if _, ok := acceptFieldsOf(t, err)["check_id"]; !ok {
		t.Errorf("no report: %v", err)
	}
	box := h.host("accept-container")
	r := report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false))
	r.Container = true
	h.reports(t, box, r)
	_, _, err = h.c.AcceptHostCheck(h.ctx, box.ID, h.acceptIn("docker.logs", "AGENT CURRENT docker.logs"), sam)
	if !strings.Contains(acceptFieldsOf(t, err)["check_id"], "partial") {
		t.Errorf("container: %v", err)
	}
	if _, _, err := h.c.AcceptHostCheck(h.ctx, "host_missing", h.acceptIn("docker.logs", "x"), sam); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown host: %v, want not found", err)
	}
}

// The person decided about what they read. If an agent has reported since, the
// acceptance would be about a value they did not see.
func TestAcceptingAValueThatChangedWhileDecidingIsRefusedWithTheNewValue(t *testing.T) {
	h := newHarness(t)
	host := h.host("accept-stale")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	_, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, h.acceptIn("docker.logs", "something older"), sam)
	var changed *HostCheckChangedError
	if !errors.As(err, &changed) || changed.Now != "AGENT CURRENT docker.logs" {
		t.Fatalf("error = %v, want a HostCheckChangedError naming the current value", err)
	}
}

// Deciding again renews: one row, the new reason and end.
func TestAcceptingTwiceReplacesTheFirstDecision(t *testing.T) {
	h := newHarness(t)
	host := h.host("accept-twice")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	in := h.acceptIn("docker.logs", "AGENT CURRENT docker.logs")
	_, first, err := h.c.AcceptHostCheck(h.ctx, host.ID, in, sam)
	if err != nil {
		t.Fatal(err)
	}
	in.Reason = "a different reason, given later"
	_, second, err := h.c.AcceptHostCheck(h.ctx, host.ID, in, HostActor{ID: "usr_2", Name: "Alex"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := h.st.GetHost(h.ctx, host.ID)
	if len(got.Acceptances) != 1 || got.Acceptances[0].Reason != in.Reason || got.Acceptances[0].ByName != "Alex" || second.ID != first.ID {
		t.Errorf("acceptances = %+v", got.Acceptances)
	}
}

// Two operators pressing Revoke want the same outcome, so the second is not told
// it failed; only a revoke that ended something has an acceptance to audit.
func TestRevokingIsIdempotentAndCountsTheCheckAgain(t *testing.T) {
	h := newHarness(t)
	host := h.host("revoking")
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	if _, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, h.acceptIn("docker.logs", "AGENT CURRENT docker.logs"), sam); err != nil {
		t.Fatal(err)
	}
	v, ended, err := h.c.RevokeHostCheck(h.ctx, host.ID, "docker.logs")
	if err != nil || ended == nil || ended.Reason != goodReason {
		t.Fatalf("revoke = %+v, %v", ended, err)
	}
	if v.Doctor.Summary.Warnings != 1 || v.Doctor.Summary.Accepted != 0 {
		t.Errorf("after revoking the summary is %+v", v.Doctor.Summary)
	}
	if got := h.problemsOf(t, "host.os_health"); len(got) != 1 {
		t.Errorf("after revoking there are %d problems, want 1", len(got))
	}
	v, ended, err = h.c.RevokeHostCheck(h.ctx, host.ID, "docker.logs")
	if err != nil || ended != nil || v.ID != host.ID {
		t.Errorf("second revoke = %v, %v, %v", v.ID, ended, err)
	}
	if _, _, err := h.c.RevokeHostCheck(h.ctx, "host_missing", "docker.logs"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}
}

func TestTheSixtyFifthAcceptanceOnAHostIsRefused(t *testing.T) {
	h := newHarness(t)
	host := h.host("accept-limit")
	for i := 0; i < store.MaxHostCheckAcceptances; i++ {
		h.accept(t, host, "fake.check"+string(rune('a'+i%26))+string(rune('a'+i/26)), "x", h.c.Now().Add(time.Hour))
	}
	h.reports(t, host, report(h.c.Now(), check("docker.logs", hosttune.Safe, hosttune.Warn, false)))
	_, _, err := h.c.AcceptHostCheck(h.ctx, host.ID, h.acceptIn("docker.logs", "AGENT CURRENT docker.logs"), sam)
	if !errors.Is(err, ErrHostAcceptLimit) {
		t.Errorf("error = %v, want ErrHostAcceptLimit", err)
	}
}
