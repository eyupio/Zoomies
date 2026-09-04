package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/eyupio/zoomies/internal/github"
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

// The demo installation must answer every read the UI makes without reaching
// GitHub. Before this, the Installations page 500'd on the rate limit and the
// poller logged a credential failure every thirty seconds, which makes a demo
// instance read as broken software rather than as a working fleet.
func TestDemoInstallationAnswersReadsWithoutGitHub(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.c.SeedDemo(ctx); err != nil {
		t.Fatalf("SeedDemo: %v", err)
	}

	client, err := h.c.ClientFor(ctx, demoInstallationID)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}

	if _, err := client.Probe(ctx); err != nil {
		t.Errorf("Probe: %v", err)
	}
	rl, err := client.RateLimit(ctx)
	if err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	if rl.Remaining <= 0 || rl.Remaining > rl.Limit {
		t.Errorf("rate limit = %d/%d, want a plausible pair", rl.Remaining, rl.Limit)
	}
	if _, err := client.ListRunners(ctx); err != nil {
		t.Errorf("ListRunners: %v", err)
	}
	if _, err := client.ListRunnerGroups(ctx); err != nil {
		t.Errorf("ListRunnerGroups: %v", err)
	}
	if _, err := client.ListQueuedJobs(ctx); err != nil {
		t.Errorf("ListQueuedJobs: %v", err)
	}
	if err := client.DeleteRunner(ctx, 42); err != nil {
		t.Errorf("DeleteRunner on a registration that never existed should succeed: %v", err)
	}

	// Writes must refuse, and say why. Silently pretending to mint a runner
	// credential would leave runners stuck in provisioning with no explanation.
	if _, err := client.CreateJITConfig(ctx, github.JITRequest{Name: "x"}); err == nil {
		t.Error("CreateJITConfig succeeded on a demo fixture; it must refuse")
	} else if !errors.Is(err, ErrDemoFixture) {
		t.Errorf("CreateJITConfig error = %v, want it to wrap ErrDemoFixture", err)
	}
}
