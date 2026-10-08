package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/kennel"
)

func TestSetupReadStopsWhenCollectorLosesAuthority(t *testing.T) {
	for _, reason := range []string{"cancelled", "fenced"} {
		t.Run(reason, func(t *testing.T) {
			f := newKennelFixture(t)
			f.repo("acme/api", "private")
			f.ran("acme/api", 1, f.pool)
			f.pass()
			in, err := f.c.kennelInput(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			listing := f.c.kennelList(f.ctx, f.inst, in)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			if reason == "cancelled" {
				cancel()
			} else {
				f.fence("restore completed after listing")
			}
			before := f.requestsTo("/git/trees/")
			var wm kennelWatermark
			if state := f.c.kennelReadSetup(ctx, f.inst, f.row("acme/api"), &wm, listing, in); state != kennel.CoverageHeld {
				t.Fatalf("state = %s, want held", state)
			}
			if f.requestsTo("/git/trees/") != before || wm.Setup != nil {
				t.Fatal("stopped collector read repository setup")
			}
		})
	}
}

func TestRepositorySetupReadsOnlyAfterOptingInAndReportsAdvisoryFindings(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.AddFile("acme/api", "go.mod", "module acme/api")
	f.pass()
	if n := f.requestsTo("/git/trees/"); n != 0 {
		t.Fatalf("%d inventory reads without opt-in", n)
	}
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.RepositorySetup = true })
	f.pass()
	if n := f.requestsTo("/git/trees/"); n != 1 {
		t.Fatalf("inventory reads = %d, want 1", n)
	}
	wm := parseKennelWatermark(f.row("acme/api").Watermark)
	if wm.SetupState != kennel.CoverageOK || wm.Setup == nil || !wm.Setup.HasDependencies {
		t.Fatalf("watermark = %+v", wm)
	}
	v := f.view("acme/api")
	found := false
	for _, finding := range v.Findings {
		if finding.Code == kennel.CodeSetupReadme {
			found = true
		}
	}
	if !found {
		t.Fatal("missing README was not reported")
	}
	if v.Coverage[len(v.Coverage)-1].Source != kennel.SourceSetup {
		t.Fatal("setup coverage is not last")
	}
	if v.State != kennel.StateBestInShow {
		t.Errorf("advice changed standing: %s", v.State)
	}
	f.gh.AddFile("acme/api", "README.md", "Build and test instructions")
	f.dueAgain()
	f.pass()
	if !parseKennelWatermark(f.row("acme/api").Watermark).Setup.Present[kennel.CodeSetupReadme] {
		t.Fatal("new README was not seen")
	}
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.RepositorySetup = false })
	before := f.requestsTo("/git/trees/")
	f.dueAgain()
	f.pass()
	if f.requestsTo("/git/trees/") != before {
		t.Fatal("inventory read after switching setup off")
	}
}

func TestUnreadableRepositorySetupNeverReportsMissingFiles(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.RepositorySetup = true })
	f.gh.SetPermissions(map[string]string{"metadata": "read", "actions": "read"})
	f.pass()
	wm := parseKennelWatermark(f.row("acme/api").Watermark)
	if wm.SetupState != kennel.CoverageUnavailable || wm.Setup != nil {
		t.Errorf("watermark = %+v", wm)
	}
	v := f.view("acme/api")
	if v.State != kennel.StatePartial {
		t.Errorf("unreadable setup standing = %s", v.State)
	}
}

func TestTurningOffEverySetupCheckStopsTheInventoryRead(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.c.UpdateConfig(func(c *config.Config) {
		c.Kennel.RepositorySetup = true
		for _, ck := range kennel.Checks() {
			if ck.Area == kennel.AreaSetup {
				c.Kennel.DisabledChecks = append(c.Kennel.DisabledChecks, string(ck.Code))
			}
		}
	})
	f.pass()
	if n := f.requestsTo("/git/trees/"); n != 0 {
		t.Fatalf("%d reads with every setup check off", n)
	}
}

func TestSetupReadFailuresRemainCoverageGaps(t *testing.T) {
	for _, tt := range []struct {
		status int
		state  kennel.CoverageState
	}{
		{403, kennel.CoverageDenied}, {404, kennel.CoverageUnavailable}, {500, kennel.CoverageError}, {429, kennel.CoverageHeld},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			f := newKennelFixture(t)
			f.repo("acme/api", "private")
			f.ran("acme/api", 1, f.pool)
			f.c.UpdateConfig(func(c *config.Config) { c.Kennel.RepositorySetup = true })
			f.gh.SetError("/git/trees/main", tt.status, "read refused")
			f.pass()
			wm := parseKennelWatermark(f.row("acme/api").Watermark)
			if wm.SetupState != tt.state || wm.Setup != nil {
				t.Fatalf("got %+v, want %s without facts", wm, tt.state)
			}
			for _, finding := range f.view("acme/api").Findings {
				if finding.Code.Area() == kennel.AreaSetup {
					t.Errorf("failed read raised %s", finding.Code)
				}
			}
		})
	}
}
