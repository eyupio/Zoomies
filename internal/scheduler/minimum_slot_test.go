package scheduler

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The report that found this: a runner of a pool that left its size to the
// host was killed for want of memory, and raising the pool's minimum could not
// help -- a minimum above the slot share left the host out altogether, so the
// only way to give a runner more was to lower the host's capacity. The minimum
// is the least a runner may have, so a share under it is raised to it: each
// runner is given and charged the minimum, and the host holds as many as its
// machine covers rather than none.
func TestAMinimumAboveTheShareIsGivenRatherThanRefused(t *testing.T) {
	// 16 GB over eight slots is a 2 GB share; the pool will take no less
	// than 4 GB.
	h := sized("h", 8, 16, hostFor(16*1024), 100000)
	p := automatic("auto", 0, 4096)

	if field := ShareTooSmall(h, p); field != "" {
		t.Fatalf("ShareTooSmall = %q, want a share under the minimum to be raised to it", field)
	}
	if !HostFits(h, p) {
		t.Fatalf("the host was refused: %s", HostShortfall(h, p))
	}
	if got := Reserve(p, h).MemoryMB; got != 4096 {
		t.Errorf("charge = %d MB, want the 4096 MB minimum", got)
	}
	got, source := Allocation(p, h, true)
	if got.MemoryMB != 4096 || source != store.AllocationFromHost {
		t.Errorf("Allocation = %+v from %q, want 4096 MB from the host", got, source)
	}

	// The slots are eight and the machine covers four runners of 4 GB: the
	// room says four, and the pass places four -- all of them whole, not
	// reduced.
	room := HostRoomFor(h, p)
	if room.Room != 4 || room.LimitedBy != "memory" {
		t.Errorf("HostRoomFor = %+v, want room for 4, limited by memory", room)
	}
	hs := newHostSet([]*store.Host{h}, []*store.Pool{p}, nil, now)
	placed := hs.placeAvoiding(p, 8, nil)
	if len(placed) != 4 {
		t.Fatalf("the pass placed %d runners, want the 4 the room promises", len(placed))
	}
	for _, pl := range placed {
		if pl.size != nil {
			t.Errorf("a runner was placed reduced to %+v, want every one at the minimum", *pl.size)
		}
	}

	note := HostReduction(h, p)
	for _, w := range []string{"2 GB of memory", "4 GB", "fewer of them than its 8 slots"} {
		if !strings.Contains(note, w) {
			t.Errorf("HostReduction = %q, want it to contain %q", note, w)
		}
	}
}

// The minimum is the floor, never the size: where the share is larger, the
// runner is given all of it.
func TestAShareAboveTheMinimumIsGivenWhole(t *testing.T) {
	h := sized("h", 2, 16, hostFor(16*1024), 100000)
	p := automatic("auto", 1, 2048)

	got, _ := Allocation(p, h, true)
	share := HostShare(h)
	if got.MemoryMB != share.MemoryMB || got.CPUs != share.CPUs {
		t.Errorf("Allocation = %+v, want the whole share %+v rather than the minimum", got, share)
	}
	if HostReduction(h, p) != "" {
		t.Errorf("HostReduction = %q on a host whose share covers the minimum", HostReduction(h, p))
	}
}

// A docker-in-docker slot carries a runner and its daemon, and the minimum is
// per container, so a share under it is raised to twice the minimum -- which
// the backend then splits, giving each container the minimum.
func TestADinDSlotUnderItsMinimumIsGivenTwiceIt(t *testing.T) {
	h := sized("h", 8, 16, hostFor(16*1024), 100000)
	p := automatic("auto", 1, 2048)
	p.DockerMode = store.DockerDinD

	got, _ := Allocation(p, h, true)
	if got.MemoryMB != 4096 || got.CPUs != 2 {
		t.Errorf("Allocation = %+v, want the pair's 2 CPU and 4096 MB", got)
	}
	runner, daemon := got.SplitWithDaemon()
	if runner.MemoryMB != 2048 || daemon.MemoryMB != 2048 {
		t.Errorf("split = %+v and %+v, want the 2048 MB minimum each", runner, daemon)
	}
	if r := Reserve(p, h); r.MemoryMB != 4096 || r.CPUs != 2 {
		t.Errorf("charge = %+v, want one slot of the pair's 2 CPU and 4096 MB", r)
	}
}

// A pool with no minimum is exactly what it was: a share too thin to run a
// runner refuses the host, because nobody said the runner should be larger.
func TestAThinShareWithNoMinimumIsStillRefused(t *testing.T) {
	h := sized("h", 32, 8, hostFor(16*1024), 100000)
	p := automatic("auto", 0, 0)
	if field := ShareTooSmall(h, p); field != "cpu" {
		t.Errorf("ShareTooSmall = %q, want the 0.2 CPU share refused", field)
	}
}
