package main

import (
	"bytes"
	"context"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/installer"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDoctorSummaryExplainsWarningsAndReboot(t *testing.T) {
	var b bytes.Buffer
	r := hosttune.Report{CheckedAt: time.Now(), OS: "linux", Distro: "ubuntu 24.04", RebootPending: true, Results: []hosttune.Result{{ID: "kernel.pending", Title: "Installed kernel", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "6.8", Recommended: "6.11", Rationale: "A newer kernel is installed."}}}
	printDoctor(&b, r, true)
	for _, s := range []string{"1 warning(s)", "Reboot pending", "kernel.pending", "Latest agent report", "6.11"} {
		if !strings.Contains(b.String(), s) {
			t.Errorf("missing %s", s)
		}
	}
}
func TestDoctorExitDoesNotPrintAFailureTwice(t *testing.T) {
	var b bytes.Buffer
	e := &env{out: &b, err: &b}
	for _, n := range []int{1, 2} {
		if got := report(e, "doctor", doctorExit(n)); got != n {
			t.Fatalf("exit %d", got)
		}
	}
	if b.Len() != 0 {
		t.Fatalf("duplicate output: %s", b.String())
	}
}
func TestDoctorTiersKeepKernelChecks(t *testing.T) {
	rs := []hosttune.Result{{ID: "kernel.running", Tier: hosttune.Safe}, {ID: "governor", Tier: hosttune.Aggressive}, {ID: "snapd", Tier: hosttune.Dedicated}}
	if len(filterDoctor(rs, hosttune.Safe)) != 1 || len(filterDoctor(rs, hosttune.Aggressive)) != 2 || len(filterDoctor(rs, hosttune.Dedicated)) != 3 {
		t.Fatal("incorrect tier filtering")
	}
}

func TestTheBriefReportListsOnlyWhatNeedsAttentionAndSaysWhatTuneCanFix(t *testing.T) {
	r := hosttune.Report{Results: []hosttune.Result{
		{Title: "File watches", Status: hosttune.OK, Current: "1"},
		{Title: "Docker log rotation", Status: hosttune.Warn, Current: "json-file", Recommended: "max-size 10m", Actionable: true},
		{Title: "Docker root filesystem", Status: hosttune.Warn, Current: "ext4", Recommended: "noatime"},
	}}
	var b strings.Builder
	printDoctorBrief(&b, r, 1)
	out := b.String()
	for _, want := range []string{"2 warnings, 0 errors", "1 new", "Docker log rotation", "[fixable]", "Docker root filesystem"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "File watches") || strings.Count(out, "[fixable]") != 1 || strings.Contains(out, "\x1b") {
		t.Errorf("unexpected content:\n%s", out)
	}
}

func TestTheFullReportWrapsToANarrowTerminalAndPutsFindingsFirst(t *testing.T) {
	t.Setenv("COLUMNS", "36")
	r := hosttune.Report{CheckedAt: time.Now(), Results: []hosttune.Result{
		{ID: "a.ok", Title: "Fine", Status: hosttune.OK, Current: "yes"},
		{ID: "b.warn", Title: "Logs", Status: hosttune.Warn, Current: "json", Recommended: "rotate", Rationale: "Unrotated container logs can fill the disk and stop every job on this host.", Actionable: true},
	}}
	var b strings.Builder
	printDoctor(&b, r, false)
	out := b.String()
	if strings.Index(out, "Logs") > strings.Index(out, "Fine") || !strings.Contains(out, "[fixable]") {
		t.Errorf("findings should come first and say what is fixable:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if len(l) > 56 && !strings.Contains(l, "checked") {
			t.Errorf("line too wide for a phone: %q", l)
		}
	}
}

func TestUpgradeHealthNeverTurnsFindingsIntoATuningConversation(t *testing.T) {
	var out strings.Builder
	r := hosttune.Report{Results: []hosttune.Result{{Title: "Logs", Status: hosttune.Warn, Actionable: true}}, RebootPending: true}
	printUpgradeHealth(&out, r)
	text := out.String()
	for _, want := range []string{"Host health: 1 warning, 0 errors", "Review: zoomies doctor", "Reboot pending"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	for _, unwanted := range []string{"What next", "[t]", "[y/N]", "Logs", "tune"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("upgrade includes %q: %s", unwanted, text)
		}
	}
}

func TestUnavailableHealthChecksAreNotReportedAsPassing(t *testing.T) {
	var out strings.Builder
	printUpgradeHealth(&out, hosttune.Report{Results: []hosttune.Result{{Status: hosttune.Skip}}})
	if !strings.Contains(out.String(), "checks unavailable") || strings.Contains(out.String(), "passed") {
		t.Fatal(out.String())
	}
}

func TestDoctorBriefCapsAndWrapsFindings(t *testing.T) {
	t.Setenv("COLUMNS", "36")
	r := hosttune.Report{}
	for range 8 {
		r.Results = append(r.Results, hosttune.Result{Title: "Container log rotation", Recommended: "Use bounded logs for newly created containers", Status: hosttune.Warn})
	}
	var out strings.Builder
	printDoctorBrief(&out, r, 0)
	if strings.Count(out.String(), "Container log rotation") != 3 || !strings.Contains(out.String(), "5 more") {
		t.Fatal(out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > 36 {
			t.Fatalf("too wide: %q", line)
		}
	}
}

// A maintenance restart changes the host, so its flags mean nothing without the
// thing they modify, and a typo that quietly did nothing would leave an operator
// believing their host had been restarted.
func TestMaintenanceFlagsNeedTheirParents(t *testing.T) {
	for _, args := range [][]string{
		{"tune", "--kill-running"},
		{"tune", "--wait", "5m"},
		{"doctor", "--force"},
		{"doctor", "--interactive", "--kill-running"},
		{"doctor", "--interactive", "--wait", "5m"},
		{"tune", "--background"},
		{"tune", "--give-up-after", "1h"},
		{"tune", "--force", "--background", "--kill-running"},
		{"tune", "--force", "--background", "--wait", "5m"},
		{"tune", "--restart-pending", "--kill-running"},
		{"tune", "--restart-pending", "--revert"},
		{"tune", "--restart-pending", "--force"},
		{"doctor", "--interactive", "--background"},
		{"doctor", "--interactive", "--force", "--background", "--kill-running"},
		{"doctor", "--interactive", "--give-up-after", "1h"},
	} {
		e, _, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, args); code != exitUsage {
			t.Errorf("%v: exit code = %d, want %d (usage)\n%s", args, code, exitUsage, errOut.String())
		}
	}
}

// A container deployment has no systemd units to stop, so a maintenance restart counted
// the controller's own container as running work and --kill-running stopped it and never
// started it again. It is refused, with what to do by hand.
func TestAMaintenanceRestartIsRefusedOnAHostThatRunsZoomiesInAContainer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZOOMIES_CONFIG_DIR", dir)
	record := `{"deployment":"docker","container":"zoomies-ctr"}`
	if err := os.WriteFile(installer.DeploymentRecordPath(dir), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"tune", "--force"}, {"tune", "--force", "--background"}, {"tune", "--restart-pending"}, {"tune", "--revert", "--force", "--yes", "--kill-running"}} {
		e, _, errOut := newTestEnv(t)
		t.Setenv("ZOOMIES_CONFIG_DIR", dir)
		if code := dispatch(context.Background(), e, args); code == exitOK {
			t.Errorf("%v on a container deployment succeeded", args)
		}
		if !strings.Contains(errOut.String(), "zoomies-ctr") || !strings.Contains(errOut.String(), "docker stop zoomies-ctr") {
			t.Errorf("%v: the refusal must name the container and say what to do:\n%s", args, errOut)
		}
	}
}

// Reading a host's report through the controller needs a token, and the one
// thing a person at a terminal on that host cannot be expected to have is one.
// The page for the host makes one for exactly this, so a refusal says so, rather
// than leaving them with a generic instruction to go and mint one by hand.
func TestARefusedHostReportPointsAtTheHostsPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"sign in first"}}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"doctor", "--host", "host_abc", "--url", srv.URL}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{"--token", "the host's page in the web UI", "15 minutes"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("a refused doctor --host does not say %q:\n%s", want, errOut)
		}
	}
}

// An acceptance is a decision made on the controller, so the report fetched
// with --host must show where the warning went: its own section, who decided
// and why, and a summary line the sections add up to.
func TestAnAcceptedWarningHasItsOwnSectionAndLeavesTheCounts(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	r := hosttune.Report{CheckedAt: time.Now(), Results: []hosttune.Result{
		{ID: "docker.logs", Title: "Docker log rotation", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "json-file", Actionable: true,
			Accepted: &hosttune.Acceptance{Reason: "rotated by the log shipper", By: "Sam", At: at, ExpiresAt: at.AddDate(0, 6, 0)}},
		{ID: "cgroup.version", Title: "Control groups", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "v1"},
	}}
	var b strings.Builder
	printDoctor(&b, r, true)
	out := b.String()
	for _, want := range []string{"1 warning(s), 0 error(s), 0 skipped check(s), 1 accepted", "Needs attention (1)", "Accepted (1)", "[accepted]", "by Sam on 1 Oct 2026, until 1 Apr 2027: rotated by the log"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[fixable]") {
		t.Errorf("an accepted row is still offered as fixable:\n%s", out)
	}
	if strings.Index(out, "Accepted (1)") < strings.Index(out, "Needs attention (1)") {
		t.Errorf("the accepted section should follow what needs attention:\n%s", out)
	}
	var brief strings.Builder
	printDoctorBrief(&brief, r, 0)
	if strings.Contains(brief.String(), "Docker log rotation") || !strings.Contains(brief.String(), "1 warning, 0 errors") || !strings.Contains(brief.String(), "1 check accepted") {
		t.Errorf("the brief report should list only what needs attention and mention the accepted check:\n%s", brief.String())
	}
}

func TestAHostWithOnlyAcceptedWarningsExitsZero(t *testing.T) {
	r := hosttune.Report{Results: []hosttune.Result{{ID: "docker.logs", Tier: hosttune.Safe, Status: hosttune.Warn, Accepted: &hosttune.Acceptance{}}}}
	if r.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", r.ExitCode())
	}
}

func TestAWarningWithACommandTellsTheOperatorWhatToRun(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	r := hosttune.Report{CheckedAt: time.Now(), Results: []hosttune.Result{
		{ID: "kernel.hwe", Title: "HWE kernel", Status: hosttune.Warn, Command: "sudo apt-get install linux-generic-hwe-24.04"},
		{ID: "kernel.ok", Title: "Fine", Status: hosttune.OK, Command: "sudo true"},
	}}
	var b strings.Builder
	printDoctor(&b, r, true)
	out := b.String()
	if !strings.Contains(out, "run: sudo apt-get install linux-generic-hwe-24.04") || strings.Contains(out, "sudo true") {
		t.Errorf("the command belongs on the warning only:\n%s", out)
	}
}
