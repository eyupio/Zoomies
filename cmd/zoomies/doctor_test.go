package main

import (
	"bytes"
	"github.com/eyupio/zoomies/internal/hosttune"
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
