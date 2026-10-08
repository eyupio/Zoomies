package hosttune

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() Report {
	return Report{
		CheckedAt: time.Unix(1000, 0).UTC(),
		OS:        "linux", Distro: "ubuntu", WorkDir: "/var/lib/zoomies",
		Results: []Result{
			{ID: "disk.space", Title: "Free space", Tier: Safe, Status: OK, Current: "61% free (210.0 GiB)"},
			{ID: "sysctl.somaxconn", Title: "Backlog", Tier: Safe, Status: Warn, Current: "128", Recommended: "4096", Rationale: "why", Actionable: true},
		},
	}
}

func TestSameFindingsIgnoresWhenAReportWasTaken(t *testing.T) {
	a, b := sampleReport(), sampleReport()
	b.CheckedAt = a.CheckedAt.Add(time.Hour)
	if !a.SameFindings(b) {
		t.Fatal("two reports that differ only in time should say the same thing")
	}
}

func TestSameFindingsIgnoresDriftingDiskFiguresWhileTheStatusHolds(t *testing.T) {
	a, b := sampleReport(), sampleReport()
	b.Results[0].Current = "60% free (209.0 GiB)"
	if !a.SameFindings(b) {
		t.Fatal("free space moving by a point is not news; a busy host would rewrite on every report")
	}
	b.Results[0].Status = Warn
	if a.SameFindings(b) {
		t.Fatal("a disk check changing status is news")
	}
}

func TestSameFindingsNoticesEveryOtherChange(t *testing.T) {
	edits := map[string]func(*Report){
		"os":          func(r *Report) { r.OS = "darwin" },
		"distro":      func(r *Report) { r.Distro = "debian" },
		"work dir":    func(r *Report) { r.WorkDir = "/srv" },
		"container":   func(r *Report) { r.Container = true },
		"reboot":      func(r *Report) { r.RebootPending = true },
		"added":       func(r *Report) { r.Results = append(r.Results, Result{ID: "x", Title: "x", Tier: Safe, Status: OK}) },
		"removed":     func(r *Report) { r.Results = r.Results[:1] },
		"reordered":   func(r *Report) { r.Results[0], r.Results[1] = r.Results[1], r.Results[0] },
		"id":          func(r *Report) { r.Results[1].ID = "other" },
		"title":       func(r *Report) { r.Results[1].Title = "other" },
		"tier":        func(r *Report) { r.Results[1].Tier = Aggressive },
		"status":      func(r *Report) { r.Results[1].Status = OK },
		"current":     func(r *Report) { r.Results[1].Current = "129" },
		"recommended": func(r *Report) { r.Results[1].Recommended = "1" },
		"rationale":   func(r *Report) { r.Results[1].Rationale = "other" },
		"reason":      func(r *Report) { r.Results[1].Reason = "other" },
		"actionable":  func(r *Report) { r.Results[1].Actionable = false },
		"optional":    func(r *Report) { r.Results[1].Optional = true },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			a, b := sampleReport(), sampleReport()
			edit(&b)
			if a.SameFindings(b) {
				t.Fatalf("a change to %s should count as news", name)
			}
		})
	}
}

func TestSameFindingsDoesNotCompareTheOperatorsMarks(t *testing.T) {
	a, b := sampleReport(), sampleReport()
	b.Results[1].Acceptable = true
	b.Results[1].Accepted = &Acceptance{}
	if !a.SameFindings(b) {
		t.Fatal("marks are stamped at read time and must not make reports differ")
	}
}

// The two lists exist for the same reason, so they must not drift apart: a
// check whose figure moves should neither be acceptable nor count as news.
func TestEveryDriftingCheckIsOneThatCannotBeAccepted(t *testing.T) {
	for id := range driftingCurrent {
		ok, why := Acceptable(id)
		if ok || !strings.Contains(why, "Free space moves") {
			t.Errorf("%s drifts but Acceptable says (%v, %q)", id, ok, why)
		}
	}
}
