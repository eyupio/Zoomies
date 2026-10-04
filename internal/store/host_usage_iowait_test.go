package store

import (
	"testing"
	"time"
)

func ioWaitUsage(v float64) HostUsage {
	cpu := 50.0
	return HostUsage{CPUPercent: &cpu, IOWaitPercent: &v}
}

// Disk-bound is a state a host has been in for a while, not a reading: a
// checkout is a burst of I/O, and the advice resting on this is persistent, so
// it must not be raised by a spike or lost to a dip.
func TestAHostIsDiskBoundOnlyAfterWaitingOnDiskForTheWholeWindow(t *testing.T) {
	start := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	step := func(prev HostUsage, wait float64, at time.Duration) HostUsage {
		return ObserveHostUsage(prev, ioWaitUsage(wait), 0, 16384, start.Add(at))
	}

	u := step(HostUsage{}, 35, 0)
	if u.IOWaitHighSince == nil || !u.IOWaitHighSince.Equal(start) {
		t.Fatalf("a first reading above the threshold should start the clock at once: %+v", u)
	}
	if u.DiskBound(start) {
		t.Fatal("a host is not disk-bound the moment I/O wait rises")
	}

	// Heartbeats every 30s, wait wandering between 12 and 38: above the calm
	// line throughout, so it is one stretch even though it dips under the
	// start line.
	waits := []float64{38, 12, 25, 14, 30, 11, 22}
	at := time.Duration(0)
	for i := 0; i < 24; i++ {
		at += 30 * time.Second
		u = step(u, waits[i%len(waits)], at)
	}
	if !u.DiskBound(start.Add(at)) {
		t.Fatalf("a host that has waited on disk for %s should be disk-bound: %+v", at, u)
	}
	if !u.IOWaitHighSince.Equal(start) {
		t.Fatalf("the clock moved to %v; it should stay where the stretch began", u.IOWaitHighSince)
	}

	// Falling below the calm line ends it, and the next rise starts a new one.
	u = step(u, 4, at+30*time.Second)
	if u.IOWaitHighSince != nil || u.DiskBound(start.Add(at+30*time.Second)) {
		t.Fatalf("a host whose disk has calmed is still called bound: %+v", u)
	}
	u = step(u, 40, at+time.Minute)
	if u.IOWaitHighSince == nil || !u.IOWaitHighSince.Equal(start.Add(at+time.Minute)) {
		t.Fatalf("a new stretch should start its own clock: %+v", u)
	}
}

// A stale reading proves nothing about a host that has gone quiet, and neither
// does a clock a silent agent could have left running.
func TestAStaleHostIsNotDiskBoundAndAStaleClockIsNotCarried(t *testing.T) {
	start := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	u := ObserveHostUsage(HostUsage{}, ioWaitUsage(40), 0, 16384, start)
	if u.DiskBound(start.Add(HostUsageMaxAge + time.Hour)) {
		t.Fatal("a reading from an hour ago was taken as the host's state now")
	}
	// The agent comes back after a silence: the old clock must not make the
	// first new reading look like ten minutes of waiting.
	later := start.Add(time.Hour)
	back := ObserveHostUsage(u, ioWaitUsage(40), 0, 16384, later)
	if back.IOWaitHighSince == nil || !back.IOWaitHighSince.Equal(later) {
		t.Fatalf("clock = %v after a silence, want it restarted at %v", back.IOWaitHighSince, later)
	}
}

// What an agent sends is checked: a wait outside 0..100 is an agent that read
// something other than /proc/stat, and is dropped rather than recorded.
func TestAnImpossibleIOWaitIsNotRecorded(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, v := range []float64{-1, 101, 1e9} {
		if u := ObserveHostUsage(HostUsage{}, ioWaitUsage(v), 0, 16384, now); u.IOWaitPercent != nil || u.IOWaitHighSince != nil {
			t.Errorf("io wait %v was recorded: %+v", v, u)
		}
	}
}
