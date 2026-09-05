package config

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		" warn ":  slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLogLevel(in); got != want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// The configuration is read from every loop and every request while
// PATCH /settings writes to it. A snapshot behind one pointer is what makes
// that safe without a lock in every reader; this is the race detector's test
// of exactly that.
func TestLiveConfigurationCanBeReadWhileItIsChanged(t *testing.T) {
	live := NewLive(Default())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := live.Load()
				_ = c.Validate()
				_ = c.Log.Level + c.Scheduler.Interval.String()
			}
		}()
	}
	for i := range 200 {
		live.Update(func(c *Config) {
			c.Log.Level = []string{"debug", "info", "warn"}[i%3]
			c.Scheduler.Interval = time.Duration(i+1) * time.Second
		})
	}
	close(stop)
	wg.Wait()
	if got := live.Load().Scheduler.Interval; got != 200*time.Second {
		t.Fatalf("interval = %s after 200 updates, want 200s", got)
	}
}

// Two writers copying the same snapshot would each lose the other's change;
// Update serialises them, so every write survives.
func TestConcurrentUpdatesDoNotLoseEachOther(t *testing.T) {
	live := NewLive(Default())
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live.Update(func(c *Config) { c.Scheduler.MaxCreatesPerTick++ })
		}()
	}
	wg.Wait()
	want := Default().Scheduler.MaxCreatesPerTick + 50
	if got := live.Load().Scheduler.MaxCreatesPerTick; got != want {
		t.Fatalf("max_creates_per_tick = %d, want %d", got, want)
	}
}

// The pointer a process started with stays the first snapshot, so a caller
// that kept it -- a test that tweaks the configuration after building a
// controller -- sees the running values until something changes them.
func TestTheStartingConfigurationIsTheFirstSnapshot(t *testing.T) {
	c := Default()
	live := NewLive(c)
	if live.Load() != c {
		t.Fatal("NewLive copied the configuration it was given")
	}
	before, after := live.Update(func(c *Config) { c.Log.Level = "debug" })
	if before != c || after == c || after.Log.Level != "debug" || c.Log.Level == "debug" {
		t.Fatal("Update did not leave the old snapshot alone and publish a new one")
	}
}
