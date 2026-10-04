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
