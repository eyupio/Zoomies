package config

import (
	"testing"
	"time"
)

// A fleet that has not asked for size routing sees nothing about it: every mode
// is off, and every figure the defaults carry is one the validator accepts, so
// there is no finding on a fresh install.
func TestSizeRoutingIsOffAndItsDefaultsAreAcceptedByDefault(t *testing.T) {
	c := Default()
	if c.Scheduler.SizeRouting != "off" || c.Scheduler.AutoPools != "off" {
		t.Fatalf("size routing is %q and automatic pools %q by default; both must be off", c.Scheduler.SizeRouting, c.Scheduler.AutoPools)
	}
	for _, code := range []string{
		"scheduler.size_routing", "scheduler.auto_pools", "scheduler.auto_pools_docker_mode", "scheduler.size_default_class",
		"scheduler.size_class_limits", "runners.class_sizes", "scheduler.auto_pools_host_grace",
		"scheduler.size_wait_negative", "scheduler.size_routing_without_pools",
	} {
		if hasCode(c.Validate(), code) {
			t.Errorf("the defaults raise %s", code)
		}
	}
}

func TestEverySizeRoutingSettingIsRefusedWithTheCodeThatNamesIt(t *testing.T) {
	cases := []struct {
		name     string
		change   func(*Config)
		code     string
		severity Severity
	}{
		{"an unknown size routing mode", func(c *Config) { c.Scheduler.SizeRouting = "auto" }, "scheduler.size_routing", SeverityError},
		{"an unknown automatic pools mode", func(c *Config) { c.Scheduler.AutoPools = "true" }, "scheduler.auto_pools", SeverityError},
		{"the host socket for automatic pools", func(c *Config) { c.Scheduler.AutoPoolsDockerMode = "host-socket" }, "scheduler.auto_pools_docker_mode", SeverityError},
		{"an unknown default class", func(c *Config) { c.Scheduler.SizeDefaultClass = "huge" }, "scheduler.size_default_class", SeverityError},
		{"a medium host limit below the small one, in CPUs", func(c *Config) { c.Scheduler.SizeMediumMaxCPUs = 2 }, "scheduler.size_class_limits", SeverityError},
		{"a medium host limit below the small one, in memory", func(c *Config) { c.Scheduler.SizeMediumMaxMemoryMB = 1024 }, "scheduler.size_class_limits", SeverityError},
		{"a limit of nothing", func(c *Config) { c.Scheduler.SizeSmallMaxCPUs = 0 }, "scheduler.size_class_limits", SeverityError},
		{"a large runner smaller than a medium one", func(c *Config) { c.Runners.LargeCPUs = 1 }, "runners.class_sizes", SeverityError},
		{"a medium runner with less memory than a small one", func(c *Config) { c.Runners.MediumMemoryMB = 512 }, "runners.class_sizes", SeverityError},
		{"a runner of nothing", func(c *Config) { c.Runners.SmallMemoryMB = 0 }, "runners.class_sizes", SeverityError},
		{"a grace inside the fleet's own", func(c *Config) { c.Scheduler.AutoPoolsHostGrace = 5 * time.Minute }, "scheduler.auto_pools_host_grace", SeverityError},
		{"a grace of nothing", func(c *Config) { c.Scheduler.AutoPoolsHostGrace = 0 }, "scheduler.auto_pools_host_grace", SeverityError},
		{"a negative hold", func(c *Config) { c.Scheduler.SizeClassHold = -time.Minute }, "scheduler.size_wait_negative", SeverityError},
		{"a negative wait", func(c *Config) { c.Scheduler.SizeFallbackWait = -time.Second }, "scheduler.size_wait_negative", SeverityError},
		{"routing with no pools to route to", func(c *Config) { c.Scheduler.SizeRouting = "on" }, "scheduler.size_routing_without_pools", SeverityInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.change(c)
			f := find(c.Validate(), tc.code)
			if f.Code != tc.code || f.Severity != tc.severity || f.Fix == "" {
				t.Fatalf("finding = %+v, want %s at %s with a fix", f, tc.code, tc.severity)
			}
		})
	}

	// A hold and a wait of nothing are answers: a host moves at once, and no
	// job waits for room.
	c := Default()
	c.Scheduler.SizeClassHold, c.Scheduler.SizeFallbackWait = 0, 0
	if hasCode(c.Validate(), "scheduler.size_wait_negative") {
		t.Error("zero is refused as a negative wait")
	}
	// Routing with pools to route to has nothing to say.
	c = Default()
	c.Scheduler.SizeRouting, c.Scheduler.AutoPools = "on", "on"
	if hasCode(c.Validate(), "scheduler.size_routing_without_pools") {
		t.Error("routing with automatic pools on still raises the note")
	}
}

// The settings are in the registry, live, and carry their environment
// variable, so the Settings page offers them and a change is in force on the
// next pass.
func TestTheSizeRoutingSettingsAreLiveAndOnTheSettingsPage(t *testing.T) {
	want := map[string]string{
		"scheduler.size_routing": "ZOOMIES_SIZE_ROUTING", "scheduler.auto_pools": "ZOOMIES_AUTO_POOLS",
		"scheduler.auto_pools_installation": "ZOOMIES_AUTO_POOLS_INSTALLATION", "scheduler.size_class_hold": "ZOOMIES_SIZE_CLASS_HOLD",
		"scheduler.size_fallback_wait": "ZOOMIES_SIZE_FALLBACK_WAIT", "runners.large_memory_mb": "ZOOMIES_RUNNER_LARGE_MEMORY_MB",
	}
	found := 0
	for _, s := range Settings() {
		if env, ok := want[s.Key]; ok {
			found++
			if s.Env != env || !s.Live || s.Summary == "" {
				t.Errorf("%s = %+v", s.Key, s)
			}
		}
	}
	if found != len(want) {
		t.Fatalf("found %d of the %d settings asked about", found, len(want))
	}
}
