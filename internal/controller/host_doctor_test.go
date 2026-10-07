package controller

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/hosttune"
)

func TestHealthChangesPublishTheSameHostViewAsGET(t *testing.T) {
	h := newHarness(t)
	host := h.host("health")
	sub := h.c.Events().Subscribe(h.ctx, events.SubscribeOptions{Kinds: []events.Kind{events.KindHostUpdated}})
	defer sub.Close()
	r := &hosttune.Report{CheckedAt: h.c.Now(), OS: "linux", Results: []hosttune.Result{{ID: "kernel.pending", Tier: hosttune.Safe, Status: hosttune.Warn}}, RebootPending: true}
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub.C:
		var v HostView
		if err := json.Unmarshal(event.Data, &v); err != nil {
			t.Fatal(err)
		}
		if v.Doctor == nil || !v.Doctor.RebootPending {
			t.Fatal("live view lost report")
		}
	case <-time.After(time.Second):
		t.Fatal("no host health event")
	}
}
func TestDoctorRejectsUnboundedOrDuplicateChecks(t *testing.T) {
	r := &hosttune.Report{CheckedAt: time.Now(), Results: make([]hosttune.Result, 65)}
	if validateDoctor(r, time.Now()) == nil {
		t.Fatal("unbounded report")
	}
	r.Results = []hosttune.Result{{ID: "same", Tier: hosttune.Safe, Status: hosttune.OK}, {ID: "same", Tier: hosttune.Safe, Status: hosttune.OK}}
	if validateDoctor(r, time.Now()) == nil {
		t.Fatal("duplicate IDs")
	}
}

func TestOlderDoctorReportDoesNotUndoANewerHealthChange(t *testing.T) {
	h := newHarness(t)
	host := h.host("health-ordered")
	newer := &hosttune.Report{CheckedAt: h.c.Now(), Results: []hosttune.Result{{ID: "disk.space", Tier: hosttune.Safe, Status: hosttune.OK}}}
	older := &hosttune.Report{CheckedAt: newer.CheckedAt.Add(-time.Minute), Results: []hosttune.Result{{ID: "disk.space", Tier: hosttune.Safe, Status: hosttune.Warn}}}
	for _, r := range []*hosttune.Report{newer, older} {
		if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Doctor.Results[0].Status != hosttune.OK {
		t.Fatal("older report replaced latest health")
	}
}

// doctorTestReport is a report with one of everything the count has to tell
// apart: a warning, an error and a skip that count, a pass, two warnings that
// are suggestions (another tier, and an optional one), and the kernel warning
// that is also the reboot flag. Counted is 4, and the reboot is in no number.
func doctorTestReport(now time.Time) *hosttune.Report {
	return &hosttune.Report{
		CheckedAt: now, OS: "linux", Distro: "ubuntu 24.04", WorkDir: "/var/lib/zoomies",
		RebootPending: true,
		Results: []hosttune.Result{
			{ID: "disk.space", Title: "Disk space", Tier: hosttune.Safe, Status: hosttune.Warn},
			{ID: "docker.logs", Title: "Docker log rotation", Tier: hosttune.Safe, Status: hosttune.Error},
			{ID: "cpu.governor", Title: "CPU governor", Tier: hosttune.Safe, Status: hosttune.Skip},
			{ID: "files.watches", Title: "File watches", Tier: hosttune.Safe, Status: hosttune.OK},
			{ID: "tmp.tmpfs", Title: "Temporary files", Tier: hosttune.Aggressive, Status: hosttune.Warn},
			{ID: "kernel.hwe-install", Title: "HWE kernel", Tier: hosttune.Safe, Optional: true, Status: hosttune.Warn},
			{ID: hosttune.KernelPending, Title: "Pending reboot", Tier: hosttune.Safe, Status: hosttune.Warn},
		},
	}
}

// What a client reads is today's report with the count beside it. The report is
// embedded by pointer and its Summary method is shadowed by the field, so this
// is the one place that proves encoding/json flattens the one and keeps the
// other: a nested "Report" key, or a summary that went missing, would leave the
// UI and every other reader of the host payload looking at the wrong thing.
func TestAHostViewRendersTheReportAsItWasWithTheControllersCountBesideIt(t *testing.T) {
	h := newHarness(t)
	host := h.host("counted")
	sent := doctorTestReport(h.c.Now())
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: sent}); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(h.c.HostView(got))
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Doctor map[string]json.RawMessage `json:"doctor"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}

	// Exactly today's keys and the one new one: nothing nested, nothing renamed.
	want := []string{"checked_at", "os", "distro", "work_dir", "container", "results", "reboot_pending", "summary"}
	var keys []string
	for k := range view.Doctor {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("doctor has the keys %v, want %v", keys, want)
	}

	// The report is as the agent wrote it, whole: the count adds to it and
	// takes nothing away.
	var results []hosttune.Result
	if err := json.Unmarshal(view.Doctor["results"], &results); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(results, sent.Results) {
		t.Errorf("results = %+v, want the report's own %+v", results, sent.Results)
	}
	var pending bool
	if err := json.Unmarshal(view.Doctor["reboot_pending"], &pending); err != nil || !pending {
		t.Errorf("reboot_pending = %s, want true", view.Doctor["reboot_pending"])
	}

	var summary map[string]int
	if err := json.Unmarshal(view.Doctor["summary"], &summary); err != nil {
		t.Fatalf("summary is not an object of counts: %s", view.Doctor["summary"])
	}
	wantSummary := map[string]int{"counted": 4, "warnings": 1, "errors": 1, "skipped": 1, "suggestions": 2, "accepted": 0}
	if !maps.Equal(summary, wantSummary) {
		t.Errorf("summary = %v, want %v", summary, wantSummary)
	}

	// Compiles only while Summary is the field and not the promoted method, and
	// is the count a reader of the Go type gets.
	if field := h.c.HostView(got).Doctor.Summary; field != sent.Summary() {
		t.Errorf("Summary field = %+v, want the report's own count %+v", field, sent.Summary())
	}
}

// A host that has never reported has no doctor at all. A view wrapped round
// nothing would render {"summary":{zeros}}, which a client reads as a host with
// no checks to fail -- healthy -- rather than one nobody has looked at.
func TestAHostWithNoReportRendersNoDoctorKeyAtAll(t *testing.T) {
	h := newHarness(t)
	host := h.host("silent")
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	view := h.c.HostView(got)
	if view.Doctor != nil {
		t.Fatalf("a host with no report has a doctor view: %+v", view.Doctor)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if v, ok := keys["doctor"]; ok {
		t.Errorf("a host with no report has doctor = %s, want the key absent", v)
	}
	if doctorView(nil) != nil {
		t.Error("doctorView(nil) is not nil, so a nil report would render as an object of zeros")
	}
}

// The count is the controller's, worked out on each read, so it cannot be
// stored and read back wrong and cannot outlive the results it counted. A host
// that has fixed its disk reads zero warnings on the next report with nothing
// else to clear.
func TestTheCountIsNeverStoredAndFollowsTheLatestReport(t *testing.T) {
	h := newHarness(t)
	host := h.host("recounted")
	beat := func(r *hosttune.Report) {
		t.Helper()
		if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
			t.Fatal(err)
		}
	}
	warningsNow := func() int {
		t.Helper()
		got, err := h.st.GetHost(h.ctx, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		return h.c.HostView(got).Doctor.Summary.Warnings
	}

	now := h.c.Now()
	beat(&hosttune.Report{CheckedAt: now, OS: "linux", Results: []hosttune.Result{{ID: "disk.space", Tier: hosttune.Safe, Status: hosttune.Warn}}})
	if got := warningsNow(); got != 1 {
		t.Fatalf("warnings after a failing report = %d, want 1", got)
	}

	// What is written to the hosts table is the report and nothing the
	// controller worked out from it.
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := got.Doctor.Value()
	if err != nil {
		t.Fatal(err)
	}
	var column map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored.(string)), &column); err != nil {
		t.Fatal(err)
	}
	if _, ok := column["summary"]; ok {
		t.Errorf("the stored report carries a summary: %s", stored)
	}

	beat(&hosttune.Report{CheckedAt: now.Add(time.Second), OS: "linux", Results: []hosttune.Result{{ID: "disk.space", Tier: hosttune.Safe, Status: hosttune.OK}}})
	if got := warningsNow(); got != 0 {
		t.Errorf("warnings after the host fixed it = %d, want 0", got)
	}
}

// A host.updated frame is the resource's GET shape, summary included, because
// the UI drops the frame straight into its cache. A frame without the count
// would leave a badge counting the previous report until the next refetch.
func TestHostUpdatedCarriesTheSameDoctorCountAsGET(t *testing.T) {
	h := newHarness(t)
	host := h.host("live-count")
	sub := h.c.Events().Subscribe(h.ctx, events.SubscribeOptions{Kinds: []events.Kind{events.KindHostUpdated}})
	defer sub.Close()
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: doctorTestReport(h.c.Now())}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub.C:
		var live HostView
		if err := json.Unmarshal(event.Data, &live); err != nil {
			t.Fatal(err)
		}
		got, err := h.st.GetHost(h.ctx, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		fetched := h.c.HostView(got)
		if live.Doctor == nil || fetched.Doctor == nil {
			t.Fatalf("a view lost the report: live=%v fetched=%v", live.Doctor, fetched.Doctor)
		}
		if live.Doctor.Summary != fetched.Doctor.Summary {
			t.Errorf("host.updated counted %+v, GET counts %+v", live.Doctor.Summary, fetched.Doctor.Summary)
		}
		if live.Doctor.Summary.Counted == 0 {
			t.Errorf("the frame carried an empty count: %+v", live.Doctor.Summary)
		}
	case <-time.After(time.Second):
		t.Fatal("no host health event")
	}
}

// Only an operator can accept a check. An agent that sends the marks itself --
// to silence its own alarm -- has them dropped before the report is stored, so
// they are neither counted nor ever read back.
func TestAnAgentCannotClaimToHaveAcceptedItsOwnWarning(t *testing.T) {
	h := newHarness(t)
	host := h.host("claimant")
	claim := hosttune.Result{ID: "docker.logs", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "none",
		Accepted:   &hosttune.Acceptance{Reason: "trust me", By: "the host"},
		Ended:      &hosttune.Ended{Why: "changed"},
		Acceptable: true}
	r := &hosttune.Report{CheckedAt: h.c.Now(), OS: "linux", Results: []hosttune.Result{claim}}
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := got.Doctor.Value()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"accepted"`, `"ended"`, `"acceptable"`} {
		if strings.Contains(stored.(string), k) {
			t.Errorf("the stored report kept %s: %s", k, stored)
		}
	}
	s := h.c.HostView(got).Doctor.Summary
	if s.Warnings != 1 || s.Accepted != 0 {
		t.Errorf("summary = %+v, want the warning counted and nothing accepted", s)
	}
}
