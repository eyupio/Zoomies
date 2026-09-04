package controller

import (
	"context"
	"testing"
)

func TestIsDemoID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"ins_demoacme", true},
		{"pool_demolinux", true},
		{"host_demo1", true},
		{"run_demo09", true},
		{"pool_k3f9qz2mx7ab", false},
		{"ins_democracy", true}, // a real ID cannot collide: real IDs are random base32
		{"nounderscore", false},
		{"", false},
	} {
		if got := IsDemoID(tc.id); got != tc.want {
			t.Errorf("IsDemoID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// The demo fixtures have no GitHub behind them. If the credential prober were
// allowed to check them, every demo instance and every UI test run would open
// on a problems panel led by "this installation is not usable" -- a failure
// that says nothing about the operator's fleet and hides the ones that do.
func TestDemoInstallationIsNotProbed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.c.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}
	h.c.probeInstallations(ctx)

	inst, err := h.st.GetInstallation(ctx, demoInstallationID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if inst.LastError != "" {
		t.Errorf("the demo installation was probed and recorded %q; it must be skipped", inst.LastError)
	}

	problems, err := h.c.Problems(ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	for _, p := range problems {
		if p.Code == "installation.unhealthy" {
			t.Errorf("the demo fleet reports %q: %s", p.Code, p.Title)
		}
	}
}
