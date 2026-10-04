package hosttune

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestContainerConsumesNativeHostReportWithoutRunningHostCommands(t *testing.T) {
	e, f := fixture()
	e.Container = true
	e.HostReportPath = "/shared/host-health/report.json"
	r := Report{CheckedAt: e.Now(), OS: "linux", Distro: "ubuntu 24.04", Results: []Result{{ID: "disk.space", Status: OK, Tier: Safe}}}
	b, _ := json.Marshal(r)
	f.put(e.HostReportPath, string(b))
	m := NewMonitor(e)
	got := m.Latest(context.Background())
	if got == nil || got.Container {
		t.Fatal("did not use native report")
	}
	if len(f.calls) > 0 || f.writes > 0 {
		t.Fatal("container unnecessarily inspected the OS")
	}
}
func TestNewWarningComparisonUsesCheckIDs(t *testing.T) {
	old := &Report{Results: []Result{{ID: "same", Status: Warn}, {ID: "fixed", Status: Warn}}}
	next := Report{Results: []Result{{ID: "same", Status: Warn}, {ID: "fixed", Status: OK}, {ID: "new", Status: Warn}}}
	if NewWarnings(old, next) != 1 {
		t.Fatal("warning comparison")
	}
	if NewWarnings(nil, next) != 2 {
		t.Fatal("missing baseline")
	}
}
func TestNativeMonitorCanImportAnImmediatePostTuneReport(t *testing.T) {
	e, f := fixture()
	r := Report{CheckedAt: e.Now(), Results: []Result{{ID: "disk.space", Tier: Safe, Status: OK}}}
	b, _ := json.Marshal(r)
	f.put(e.WorkDir+"/"+ReportFile, string(b))
	m := NewMonitor(e)
	m.due = e.Now().Add(time.Hour)
	got := m.Latest(context.Background())
	if got == nil || len(got.Results) != 1 {
		t.Fatal("post-tune cache not imported")
	}
}
