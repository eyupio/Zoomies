package scheduler

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

var sizeEpoch = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// The class is the lower of what a host's CPU and its memory each say: a machine
// with a large host's cores and a small host's memory runs out of memory at a
// small host's pace, and calling it large would send it work it cannot hold. The
// figures are allocatable ones, after the reserve -- a 4-CPU box has 3.5 -- which
// is what makes "up to 4" mean what an operator expects of a four-core machine.
func TestAHostIsInTheLowerClassOfWhatItsCPUAndMemoryEachSay(t *testing.T) {
	cfg := DefaultSizeConfig()
	cases := []struct {
		name string
		host store.Host
		want store.SizeClass
		ok   bool
	}{
		{"a four-core box is small", store.Host{CPUs: 4, MemoryMB: 16384}, store.SizeSmall, true},
		{"twelve cores and 32 GB is medium", store.Host{CPUs: 12, MemoryMB: 32768}, store.SizeMedium, true},
		{"sixteen cores and 64 GB is large", store.Host{CPUs: 16, MemoryMB: 65536}, store.SizeLarge, true},
		{"many cores and little memory is as small as its memory", store.Host{CPUs: 16, MemoryMB: 8192}, store.SizeSmall, true},
		{"little CPU and a great deal of memory is as small as its CPU", store.Host{CPUs: 4, MemoryMB: 131072}, store.SizeSmall, true},
		{"a host that measured only its memory is classed on that", store.Host{MemoryMB: 32768}, store.SizeMedium, true},
		{"a host that measured only its CPUs is classed on that", store.Host{CPUs: 32}, store.SizeLarge, true},
		{"a host that measured nothing has no class", store.Host{}, "", false},
		// The limit is inclusive: 5 CPUs less a reserve of one is exactly four.
		{"exactly at the limit is still the smaller class", store.Host{CPUs: 5, MemoryMB: 16384 + 2048, ReserveCPUs: 1, ReserveMemoryMB: 2048}, store.SizeSmall, true},
		{"a hair over it is the larger", store.Host{CPUs: 6, MemoryMB: 32768, ReserveCPUs: 1}, store.SizeMedium, true},
		// What an operator holds back is not the machine they are describing.
		{"a reserve makes a host smaller", store.Host{CPUs: 16, MemoryMB: 65536, ReserveCPUs: 13, ReserveMemoryMB: 50000}, store.SizeSmall, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := cfg.HostClass(&tc.host)
			if got != tc.want || ok != tc.ok {
				t.Errorf("HostClass = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := cfg.HostClass(nil); ok {
		t.Error("a host that is not there has a class")
	}
}

func TestTheClassLimitsAreSettings(t *testing.T) {
	cfg := DefaultSizeConfig()
	cfg.SmallMax = HostLimit{CPUs: 8, MemoryMB: 32768}
	cfg.MediumMax = HostLimit{CPUs: 16, MemoryMB: 65536}
	if got, _ := cfg.HostClass(&store.Host{CPUs: 8, MemoryMB: 32768}); got != store.SizeSmall {
		t.Errorf("with the limit raised to 8, an 8-CPU host is %q, want small", got)
	}
	if got, _ := cfg.HostClass(&store.Host{CPUs: 12, MemoryMB: 65536}); got != store.SizeMedium {
		t.Errorf("a 12-CPU host with 64 GB is %q, want medium", got)
	}
}

// A host's measurements wobble. Moving it between pools on each wobble would
// reshuffle the fleet around a figure nobody meant to change, so a class moves
// only after the measurements have named the new one, without a break, for the
// whole hold.
func TestAHostMovesClassOnlyAfterTheMeasurementHasHeldForTheWholeHold(t *testing.T) {
	const hold = 10 * time.Minute
	at := func(m int) time.Time { return sizeEpoch.Add(time.Duration(m) * time.Minute) }

	// The first look puts a host in its class at once: a host that has just
	// joined should be taking work, not waiting out a hold.
	held, changed := NextHostClass(store.HostSizeClass{}, store.SizeSmall, at(0), hold)
	if !changed || held.Class != store.SizeSmall || held.Pending != "" || held.ChangedAt == nil || !held.ChangedAt.Equal(at(0)) {
		t.Fatalf("first look = %+v, %v; want small at once", held, changed)
	}
	// The same measurement again changes nothing, and says so.
	if again, changed := NextHostClass(held, store.SizeSmall, at(1), hold); changed || !sameHostClass(again, held) {
		t.Fatalf("a repeated reading reported a change: %+v", again)
	}

	// The measurement names medium: the wait starts, the class does not move.
	held, changed = NextHostClass(held, store.SizeMedium, at(5), hold)
	if !changed || held.Class != store.SizeSmall || held.Pending != store.SizeMedium || !held.PendingSince.Equal(at(5)) {
		t.Fatalf("a new class = %+v, %v; want a pending medium from minute 5", held, changed)
	}
	held, changed = NextHostClass(held, store.SizeMedium, at(14), hold)
	if changed || held.Class != store.SizeSmall {
		t.Fatalf("nine minutes in, the host moved: %+v, %v", held, changed)
	}
	held, changed = NextHostClass(held, store.SizeMedium, at(15), hold)
	if !changed || held.Class != store.SizeMedium || held.Pending != "" || held.PendingSince != nil || !held.ChangedAt.Equal(at(15)) {
		t.Fatalf("ten minutes in, the host did not move: %+v, %v", held, changed)
	}
}

// The case the hold exists for: a host measuring one side of a threshold and
// then the other. However long it goes on, the wait never completes.
func TestAHostOnAThresholdNeverMoves(t *testing.T) {
	const hold = 10 * time.Minute
	held, _ := NextHostClass(store.HostSizeClass{}, store.SizeSmall, sizeEpoch, hold)
	for i := 1; i <= 60; i++ {
		now := sizeEpoch.Add(time.Duration(i) * 4 * time.Minute)
		measured := store.SizeMedium
		if i%2 == 0 {
			measured = store.SizeSmall
		}
		held, _ = NextHostClass(held, measured, now, hold)
		if held.Class != store.SizeSmall {
			t.Fatalf("after %d readings across the threshold the host is %q", i, held.Class)
		}
	}
	// Going back to the held class clears the wait.
	if held, _ = NextHostClass(held, store.SizeSmall, sizeEpoch.Add(5*time.Hour), hold); held.Pending != "" || held.PendingSince != nil {
		t.Fatalf("a reading of the held class left a wait running: %+v", held)
	}
}

func TestAHostThatNamesAThirdClassRestartsTheWait(t *testing.T) {
	const hold = 10 * time.Minute
	held, _ := NextHostClass(store.HostSizeClass{}, store.SizeSmall, sizeEpoch, hold)
	held, _ = NextHostClass(held, store.SizeMedium, sizeEpoch.Add(time.Minute), hold)
	held, _ = NextHostClass(held, store.SizeLarge, sizeEpoch.Add(9*time.Minute), hold)
	if held.Pending != store.SizeLarge || !held.PendingSince.Equal(sizeEpoch.Add(9*time.Minute)) {
		t.Fatalf("a different class did not restart the wait: %+v", held)
	}
	held, _ = NextHostClass(held, store.SizeLarge, sizeEpoch.Add(12*time.Minute), hold)
	if held.Class != store.SizeSmall {
		t.Fatalf("the wait was counted from the first class's start: %+v", held)
	}
}

func TestAHostThatCouldNotBeMeasuredKeepsWhatItHad(t *testing.T) {
	held := store.HostSizeClass{Class: store.SizeLarge}
	got, changed := NextHostClass(held, "", sizeEpoch, 10*time.Minute)
	if changed || got.Class != store.SizeLarge {
		t.Fatalf("an unmeasured host lost its class: %+v, %v", got, changed)
	}
	if got, changed := NextHostClass(store.HostSizeClass{}, "", sizeEpoch, time.Minute); changed || got.Set() {
		t.Fatalf("an unmeasured new host was given a class: %+v, %v", got, changed)
	}
	// A hold of nothing is a move at once, for an operator who wants no wait.
	if got, _ := NextHostClass(store.HostSizeClass{Class: store.SizeSmall}, store.SizeLarge, sizeEpoch, 0); got.Class != store.SizeLarge {
		t.Fatalf("with no hold the host did not move: %+v", got)
	}
}

// Operator tags are the host's labels and the automatic ones are the machine's
// own facts; where an operator has labelled the same key the label is what is
// shown, with what it replaced, and no key is ever listed twice.
func TestHostTagsListWhatAnOperatorSaidAndWhatTheMachineIs(t *testing.T) {
	h := &store.Host{
		OS: "linux", Arch: "amd64",
		Labels:    store.StringMap{"rack": "b4", "gpu": "true", "arch": "legacy"},
		SizeClass: store.HostSizeClass{Class: store.SizeMedium},
	}
	want := []Tag{
		{Key: "arch", Value: "legacy", Source: TagOperator, Overrides: "amd64"},
		{Key: "gpu", Value: "true", Source: TagOperator},
		{Key: "os", Value: "linux", Source: TagAutomatic},
		{Key: "rack", Value: "b4", Source: TagOperator},
		{Key: "size", Value: "medium", Source: TagAutomatic},
	}
	got := HostTags(h, true)
	if len(got) != len(want) {
		t.Fatalf("HostTags = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The class is a tag only while the feature is on: a fleet that has not
	// turned it on sees nothing new on any host.
	for _, tag := range HostTags(h, false) {
		if tag.Key == "size" {
			t.Fatalf("size is listed with the feature off: %+v", tag)
		}
	}
	// An operator's own size tag replaces the class, and says what it replaced.
	h.Labels["size"] = "large"
	for _, tag := range HostTags(h, true) {
		if tag.Key == "size" && (tag.Value != "large" || tag.Source != TagOperator || tag.Overrides != "medium") {
			t.Fatalf("an operator's size tag = %+v", tag)
		}
	}
	// An operator tag that says what the machine already says overrides nothing.
	h.Labels["os"] = "linux"
	for _, tag := range HostTags(h, true) {
		if tag.Key == "os" && (tag.Source != TagOperator || tag.Overrides != "") {
			t.Fatalf("a redundant os tag = %+v", tag)
		}
	}
	// A host that has reported nothing has no automatic tags to list.
	if tags := HostTags(&store.Host{}, true); len(tags) != 0 {
		t.Fatalf("a blank host has tags: %+v", tags)
	}
}

func TestSizeModesAreOffShadowAndOn(t *testing.T) {
	for _, m := range []string{"", "off", "shadow", "on"} {
		if !ValidSizeMode(m) {
			t.Errorf("%q is not accepted", m)
		}
	}
	for _, m := range []string{"auto", "ON", "true", "enabled"} {
		if ValidSizeMode(m) {
			t.Errorf("%q is accepted", m)
		}
	}
}

func TestTheRunnerOfAClassIsWhatTheConfigSays(t *testing.T) {
	cfg := DefaultSizeConfig()
	if got := cfg.Runner(store.SizeMedium); got.CPUs != 2 || got.MemoryMB != 4096 {
		t.Fatalf("medium's runner = %+v", got)
	}
	if got := cfg.Runner("huge"); got != (store.RunnerSize{}) {
		t.Fatalf("a class that is not one has a runner: %+v", got)
	}
}
