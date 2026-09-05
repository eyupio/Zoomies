package controller

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// PATCH /settings used to write the level into the configuration struct and
// nothing else, so the settings page said "debug" while the process went on
// logging at info. The level's gate is moved with the snapshot now.
func TestASettingChangedAtRuntimeMovesTheLogLevel(t *testing.T) {
	h := newHarness(t)
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	h.c.logLevel = level

	h.c.UpdateConfig(func(c *config.Config) { c.Log.Level = "debug" })

	if level.Level() != slog.LevelDebug {
		t.Fatalf("the process is still logging at %s after log.level was set to debug", level.Level())
	}
	if got := h.c.Config().Log.Level; got != "debug" {
		t.Fatalf("Config().Log.Level = %q, want debug", got)
	}
	// Copy on write: the configuration the controller was built with is left
	// as it was, which is what makes reading it from every loop safe while
	// this write happens.
	if h.cfg.Log.Level == "debug" {
		t.Fatal("UpdateConfig wrote into the caller's configuration instead of publishing a new snapshot")
	}
}

// The scheduler's ticker was built once from the interval at startup, so an
// operator who set scheduler.interval to 2s to watch a scale-up was told it
// had worked and saw nothing change until a restart.
func TestANewSchedulerIntervalTakesEffectWithoutARestart(t *testing.T) {
	h := newHarness(t)
	h.cfg.Scheduler.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { h.c.Stop(context.Background()) })
	eventually(t, 2*time.Second, "the first reconcile pass", func() bool {
		return h.c.passes.Load() >= 1
	})

	h.c.UpdateConfig(func(c *config.Config) { c.Scheduler.Interval = 20 * time.Millisecond })

	// The change itself nudges one pass; anything beyond that within a second
	// can only be the retuned timer, since the old one is an hour out.
	base := h.c.passes.Load()
	eventually(t, 3*time.Second, "passes on the new interval", func() bool {
		return h.c.passes.Load() >= base+5
	})
}

// The same for the poller, whose loop has no nudge of its own: the change
// wakes it so the timer is rebuilt at once rather than after one more hour.
func TestANewPollIntervalTakesEffectWithoutARestart(t *testing.T) {
	h := newHarness(t)
	h.cfg.GitHub.PollFallback = true
	h.cfg.GitHub.PollInterval = time.Hour
	h.cfg.Scheduler.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { h.c.Stop(context.Background()) })

	h.c.UpdateConfig(func(c *config.Config) { c.GitHub.PollInterval = 20 * time.Millisecond })

	eventually(t, 3*time.Second, "poller sweeps on the new interval", func() bool {
		return h.c.polls.Load() >= 3
	})
}
