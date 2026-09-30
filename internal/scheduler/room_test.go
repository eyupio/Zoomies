package scheduler

import (
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The room is the machine, not the slot count -- and where the two disagree,
// which one wins is the whole answer.
//
// A host set to more slots than its machine can back is the failure that looks
// like health: the free slots are counted on every page, and every create for
// one of them is refused for want of memory. A host set to fewer is an
// operator's own ceiling and is honoured as one.
func TestHostRoomIsTheSmallerOfTheMachineAndTheSlots(t *testing.T) {
	tests := []struct {
		name      string
		host      *store.Host
		pool      *store.Pool
		wantFits  int
		wantRoom  int
		wantLimit string
	}{
		{
			name: "the machine runs out first",
			// Ten cores less the half-core floor is 9.5, which is four
			// two-core runners; twenty gigabytes less 512 MB is four more.
			host:      sized("host_a", 8, 10, 20*1024, 500*1024),
			pool:      limited("standard", 2, 4096),
			wantFits:  4,
			wantRoom:  4,
			wantLimit: "cpu",
		},
		{
			name:      "the operator's ceiling is lower and is honoured",
			host:      sized("host_b", 2, 10, 20*1024, 500*1024),
			pool:      limited("standard", 2, 4096),
			wantFits:  4,
			wantRoom:  2,
			wantLimit: "slots",
		},
		{
			name:      "memory is the tighter of the two",
			host:      sized("host_c", 8, 16, 8*1024+store.MinHostReserveMemoryMB, 500*1024),
			pool:      limited("hungry", 1, 4096),
			wantFits:  2,
			wantRoom:  2,
			wantLimit: "memory",
		},
		{
			name: "a machine too small for one runner has room for none",
			host: sized("host_d", 4, 2, 2*1024, 500*1024),
			pool: limited("huge", 8, 16384),
			// Not a negative, and not the slot count either: a host that
			// cannot hold one is a host with no room, and the count has to
			// say so rather than promising four.
			wantFits:  0,
			wantRoom:  0,
			wantLimit: "cpu",
		},
		{
			// The pass refuses every runner on a host at its disk reserve,
			// however little the pool asks for, so a pool that states no disk
			// must not be told the host has room. The pool page and the
			// pool.max_above_room warning add these figures up.
			name: "a host at its disk reserve has room for none, whatever the pool asks for",
			host: func() *store.Host {
				h := sized("host_e", 4, 8, 32*1024, 500*1024)
				h.DiskFreeMB = 0
				return h
			}(),
			pool:      limited("light", 1, 1024),
			wantFits:  0,
			wantRoom:  0,
			wantLimit: "disk",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HostRoomFor(tc.host, tc.pool)
			if got.Fits != tc.wantFits || got.Room != tc.wantRoom || got.LimitedBy != tc.wantLimit {
				t.Errorf("HostRoomFor = %+v, want fits %d, room %d, limited by %s",
					got, tc.wantFits, tc.wantRoom, tc.wantLimit)
			}
		})
	}
}

// A docker-in-docker slot is two containers, so the room it leaves is half what
// the pool's own figures suggest. An operator reading "2 CPU" against a ten-core
// host and being told there is room for two has to be told why, and this is the
// arithmetic the sentence comes from.
func TestARunnerWithASidecarTakesTwiceTheRoom(t *testing.T) {
	h := sized("host_a", 8, 10, 20*1024, 500*1024)
	p := limited("dind", 2, 4096)
	p.DockerMode = store.DockerDinD

	got := HostRoomFor(h, p)
	if got.Fits != 2 {
		t.Errorf("fits = %d, want 2: the pair is charged 4 CPU and 8 GB on a machine with 9.5 and 19.5", got.Fits)
	}
}

// A host that has measured nothing places by its slots alone, exactly as it did
// before any of this existed -- which is what stops an upgrade emptying a fleet
// of agents too old to report their machines.
func TestAnUnmeasuredHostPlacesBySlots(t *testing.T) {
	h := testHost("host_old", 6, 0)
	p := limited("standard", 2, 4096)

	got := HostRoomFor(h, p)
	if got.Room != 6 || got.LimitedBy != "" {
		t.Errorf("HostRoomFor = %+v, want all six slots and nothing named as the limit", got)
	}
}
