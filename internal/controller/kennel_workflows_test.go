package controller

import (
	"fmt"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/kennel"
)

func TestWorkflowBestPracticesReadContentsOnlyAfterOptingIn(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.AddWorkflow("acme/api", ".github/workflows/ci.yml", "on: pull_request\njobs: {build: {runs-on: self-hosted, steps: [{uses: stranger/action@main}]}}")
	f.pass()
	if f.requestsTo("/git/blobs/") != 0 {
		t.Fatal("workflow contents read without opt-in")
	}
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.WorkflowChecks = true })
	f.pass()
	if n := f.requestsTo("/git/blobs/"); n != 1 {
		t.Fatalf("blob reads=%d", n)
	}
	wm := parseKennelWatermark(f.row("acme/api").Watermark)
	if wm.WorkflowState != kennel.CoverageOK || wm.Workflows == nil || wm.Workflows.NoTimeout != 1 {
		t.Fatalf("watermark=%+v", wm)
	}
	v := f.view("acme/api")
	if v.State != kennel.StateAttention {
		t.Errorf("standing=%s", v.State)
	}
	codes := map[kennel.Code]bool{}
	for _, finding := range v.Findings {
		codes[finding.Code] = true
	}
	for _, code := range []kennel.Code{kennel.CodeNoTimeout, kennel.CodeNoConcurrency, kennel.CodeActionNotPinned, kennel.CodePermissionsUnset} {
		if !codes[code] {
			t.Errorf("missing %s", code)
		}
	}
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.WorkflowChecks = false })
	before := f.requestsTo("/git/blobs/")
	f.dueAgain()
	f.pass()
	if f.requestsTo("/git/blobs/") != before {
		t.Fatal("content read after switching off")
	}
}

func TestMalformedWorkflowDoesNotHideFindingsInOtherFilesOrEarnAnAllClear(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.AddWorkflow("acme/api", ".github/workflows/a.yml", "on: push\njobs: {build: {runs-on: self-hosted, steps: []}}")
	f.gh.AddWorkflow("acme/api", ".github/workflows/z.yml", "jobs: [invalid]")
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.WorkflowChecks = true })
	f.pass()
	wm := parseKennelWatermark(f.row("acme/api").Watermark)
	if wm.WorkflowState != kennel.CoveragePartial || wm.Workflows == nil || wm.Workflows.NoTimeout != 1 {
		t.Fatalf("watermark=%+v", wm)
	}
	found := false
	for _, finding := range f.view("acme/api").Findings {
		if finding.Code == kennel.CodeNoTimeout {
			found = true
		}
	}
	if !found {
		t.Fatal("positive evidence was discarded")
	}
}

func TestDisablingBothWorkflowAreasStopsContentReads(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.c.UpdateConfig(func(c *config.Config) {
		c.Kennel.WorkflowChecks = true
		c.Kennel.DisabledChecks = []string{"ci", "token"}
	})
	f.pass()
	if f.requestsTo("/git/trees/") != 0 || f.requestsTo("/git/blobs/") != 0 {
		t.Fatal("disabled checks read repository contents")
	}
}

func TestWorkflowFailuresAreCoverageGapsAndNeverMissingConfiguration(t *testing.T) {
	for _, tt := range []struct {
		status int
		state  kennel.CoverageState
	}{{403, kennel.CoverageDenied}, {404, kennel.CoverageUnavailable}, {500, kennel.CoverageError}, {429, kennel.CoverageHeld}} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			f := newKennelFixture(t)
			f.repo("acme/api", "private")
			f.ran("acme/api", 1, f.pool)
			f.c.UpdateConfig(func(c *config.Config) { c.Kennel.WorkflowChecks = true })
			f.gh.SetError("/git/trees/main", tt.status, "read refused")
			f.pass()
			wm := parseKennelWatermark(f.row("acme/api").Watermark)
			if wm.WorkflowState != tt.state || wm.Workflows != nil {
				t.Fatalf("watermark=%+v", wm)
			}
			if v := f.view("acme/api"); len(v.Findings) != 0 || v.State != kennel.StatePartial {
				t.Fatalf("failed workflow read gave findings or an all-clear: %+v", v)
			}
		})
	}
}
