package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// The detail and the fix are what an operator acts on; the title on the card is
// only what they are about. A stored check that dropped them would send the
// operator back to pressing Check on every page load.
func TestAStoredCheckKeepsEachFindingsDetailAndFix(t *testing.T) {
	when := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	view := ProviderCheckView{
		ProviderID: "prov_1", OK: false, Reachable: true, Version: "9.1.4", CheckedAt: when,
		Findings: []config.Finding{{
			Code: "proxmox.bridge_missing", Severity: config.SeverityError,
			Title:  `node pve1 has no bridge called "vmbr0"`,
			Detail: "It offers vmbr1.", Fix: "use a bridge that exists on every node.",
		}},
	}
	row := &store.Provider{ID: "prov_1", LastCheckAt: &when, LastCheckReport: encodeCheckReport(view)}

	got := decodeCheckReport(row)
	if got == nil {
		t.Fatal("a stored report read back as nothing")
	}
	if len(got.Findings) != 1 || got.Findings[0].Detail != "It offers vmbr1." || got.Findings[0].Fix == "" {
		t.Errorf("the finding lost its detail or its fix: %+v", got.Findings)
	}
	if got.Version != "9.1.4" || !got.Reachable || got.OK || !got.CheckedAt.Equal(when) {
		t.Errorf("the verdict changed on the way through: %+v", got)
	}
}

// A row last checked before the report was kept, or one holding text that is
// not a report, must read as "no report" and leave the card on its sentence.
func TestAProviderWithNoUsableStoredCheckReadsAsNone(t *testing.T) {
	when := time.Now()
	for name, row := range map[string]*store.Provider{
		"never checked":             {ID: "p"},
		"checked before the report": {ID: "p", LastCheckAt: &when},
		"not json":                  {ID: "p", LastCheckAt: &when, LastCheckReport: "{"},
	} {
		if got := decodeCheckReport(row); got != nil {
			t.Errorf("%s: read back %+v", name, got)
		}
	}
}
