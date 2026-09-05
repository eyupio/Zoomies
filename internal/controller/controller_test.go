package controller

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// New refuses to build a controller that cannot work, and says what to do
// about it rather than failing later in a loop nobody is watching.
func TestNewValidatesItsOptions(t *testing.T) {
	h := newHarness(t)
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"no store", Options{Config: h.cfg, Key: key}, "store.Open"},
		{"no config", Options{Store: h.st, Key: key}, "config.Load"},
		{"no key", Options{Store: h.st, Config: h.cfg}, "encryption key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.opts)
			if err == nil {
				t.Fatal("New succeeded without a required collaborator")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Starting and stopping must be quick and must leave the fleet alone: a
// controller restart is not a reason for a job to die.
func TestStartAndStopLeaveRunnersAlone(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerBusy)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.c.Start(ctx); err == nil {
		t.Fatal("Start succeeded twice; the second call should say it is already running")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := h.c.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerBusy {
		t.Fatalf("runner state = %q after a controller stop, want %q", after.State, store.RunnerBusy)
	}
	if err := h.c.Stop(stopCtx); err != nil {
		t.Fatalf("stopping an already-stopped controller: %v", err)
	}
}

// Losing the encryption key is a real operational situation. The message has
// to name it, and name the installation it affects, or an operator is left
// with an authentication failure and no idea which of the two it is.
func TestClientForNamesAKeyMismatch(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()

	other, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	bus := events.New()
	c, err := New(Options{
		Store:  h.st,
		Config: h.cfg,
		Key:    other,
		Auth:   auth.New(h.st, h.cfg, bus),
		Events: bus,
		GitHub: h.factory,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.ClientFor(h.ctx, inst.ID)
	if err == nil {
		t.Fatal("a client was built with the wrong encryption key")
	}
	for _, want := range []string{"does not match the one this data was written with", inst.ID, inst.Target} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

// The health prober is what turns "nothing is scaling" into a sentence on the
// Installations page.
func TestProbeInstallationRecordsHealth(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()

	if _, err := h.c.ProbeInstallation(h.ctx, inst.ID); err != nil {
		t.Fatalf("ProbeInstallation: %v", err)
	}
	after, err := h.st.GetInstallation(h.ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if !after.Healthy() || after.LastCheckedAt == nil {
		t.Fatalf("installation = %+v, want a successful check recorded", after)
	}

	h.gh.SetError("/app", 401, "A JSON web token could not be decoded")
	if _, err := h.c.ProbeInstallation(h.ctx, inst.ID); err == nil {
		t.Fatal("ProbeInstallation succeeded against a GitHub that rejects the credentials")
	}
	after, err = h.st.GetInstallation(h.ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if after.Healthy() {
		t.Fatal("the failed probe was not recorded")
	}
	if !contains(h.problemCodes(), "installation.unhealthy") {
		t.Fatalf("problems = %v, want the unhealthy installation", h.problemCodes())
	}
}

// An App that authenticates but is missing a permission is recorded as
// unusable now, rather than discovered the first time a runner is needed.
func TestProbeReportsMissingPermissions(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.gh.SetPermissions(map[string]string{"metadata": "read"})

	_, err := h.c.ProbeInstallation(h.ctx, inst.ID)
	if err == nil {
		t.Fatal("ProbeInstallation succeeded for an App with no runner permission")
	}
	if !strings.Contains(err.Error(), "Self-hosted runners") {
		t.Fatalf("error = %q, want it to name the missing permission", err)
	}
}

// The metrics the API serves have to include the fleet gauges, which are read
// from the database at scrape time rather than cached.
func TestRegistryGathersFleetMetrics(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.runnerRow(pool, host, store.RunnerBusy)

	families, err := h.c.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		seen[f.GetName()] = true
	}
	for _, want := range []string{
		"zoomies_runners", "zoomies_jobs_queued", "zoomies_hosts",
		"zoomies_host_capacity", "zoomies_host_capacity_used", "zoomies_build_info",
	} {
		if !seen[want] {
			t.Fatalf("%s is missing from the metrics endpoint", want)
		}
	}
}

// The controller defaults the collaborators an embedded caller may not have,
// so a minimal setup still gets a working event bus and auth service.
func TestNewFillsInDefaults(t *testing.T) {
	h := newHarness(t)
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	c, err := New(Options{Store: h.st, Config: config.Default(), Key: key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Events() == nil || c.Auth() == nil || c.Store() == nil || c.Config() == nil {
		t.Fatal("New left one of the accessors nil")
	}
	if c.Registry() == nil {
		t.Fatal("New built no metrics registry")
	}
}

// A loop that panicked used to stay dead while /readyz went on answering 200:
// one bad snapshot in the scheduler and every job queued for good, with
// nothing on the problems drawer to say why. The loop is restarted and the
// crash is reported.
func TestAPanickingLoopIsRestartedAndReported(t *testing.T) {
	h := newHarness(t)
	h.c.loopRestartDelay = time.Millisecond
	var runs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.c.spawn("flaky", ctx, func(ctx context.Context) {
		if runs.Add(1) < 3 {
			panic("a nil pointer where a pool should be")
		}
		<-ctx.Done()
	})

	eventually(t, 5*time.Second, "the loop to be restarted twice", func() bool {
		return runs.Load() >= 3
	})
	panics := h.c.LoopPanics()
	if panics["flaky"].Count != 2 || !strings.Contains(panics["flaky"].Last, "nil pointer") {
		t.Fatalf("recorded panics = %+v, want two with the last message", panics["flaky"])
	}
	problems, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var found *Problem
	for i := range problems {
		if problems[i].Code == "controller.loop_panicked" {
			found = &problems[i]
		}
	}
	if found == nil {
		t.Fatalf("problems = %v, want the crashed loop reported", codesOf(problems))
	}
	if found.Severity != config.SeverityError || !strings.Contains(found.Title, "flaky") || !strings.Contains(found.Title, "2 times") {
		t.Fatalf("problem = %+v, want an error naming the loop and the count", found)
	}

	cancel()
	done := make(chan struct{})
	go func() { h.c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the restarted loop did not stop with its context")
	}
}

func codesOf(problems []Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.Code)
	}
	return out
}
