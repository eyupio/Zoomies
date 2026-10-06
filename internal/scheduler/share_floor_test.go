package scheduler

import (
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// A slot's floor is each half's own floor scaled by how thin that half is, and
// CPU and memory are split on their own, so each resource has its own scale: an
// 80% CPU daemon needs five times the comfortable CPU in the slot while an even
// memory split needs twice the memory.
func TestTheShareFloorScalesEachResourceByItsOwnSplit(t *testing.T) {
	p := &store.Pool{DockerMode: store.DockerDinD, Resources: store.Resources{DaemonCPUSharePercent: 80, DaemonMemorySharePercent: 50}}
	got := ShareFloor(p)
	if want := comfortableRunnerCPUs * 5; got.CPUs != want {
		t.Errorf("CPU floor = %v, want %v for an 80%% daemon", got.CPUs, want)
	}
	if want := int64(comfortableRunnerMemoryMB * 2); got.MemoryMB != want {
		t.Errorf("memory floor = %d, want %d for an even memory split", got.MemoryMB, want)
	}
	// A minimum the operator typed is scaled the same way, per resource.
	p.Resources.MinCPUs, p.Resources.MinMemoryMB = 1, 1024
	got = ShareFloor(p)
	if got.CPUs != 5 || got.MemoryMB != 2048 {
		t.Errorf("floor with minimums = %v CPU, %d MB; want 5 and 2048", got.CPUs, got.MemoryMB)
	}
}

// Pausing a host (capacity zero) makes it full for every pool alike. An automatic
// pool read the paused host as too small, which is what the machine provisioner
// treats as "no host could ever run this pool".
func TestAPausedHostIsFullAndNotTooSmallForAnAutomaticPool(t *testing.T) {
	h := sized("paused", 0, 16, hostFor(32*1024), 100000)
	if got := ShareTooSmall(h, testPool("auto", "a")); got != "" {
		t.Errorf("a host with no slots is %q for an automatic pool, want it left to the capacity check", got)
	}
}
