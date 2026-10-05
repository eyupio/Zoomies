package backend

import (
	"strconv"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func scratchSpec(memoryMB int64, tmpfs store.TmpfsConfig) Spec {
	return Spec{Resources: store.Resources{MemoryMB: memoryMB}, ResourcesSource: store.AllocationFromPool, Tmpfs: tmpfs}
}

func folder(s store.RunnerScratch, kind store.ScratchKind) (store.ScratchFolder, bool) {
	for _, f := range s.Folders {
		if f.Kind == kind {
			return f, true
		}
	}
	return store.ScratchFolder{}, false
}

// A pool that keeps nothing in memory says nothing, and a runner that asks for
// nothing records nothing.
func TestAPoolThatKeepsNothingInMemoryHasNoScratchToPlan(t *testing.T) {
	if got := PlanScratch(scratchSpec(8192, store.TmpfsConfig{}), store.TmpfsConfig{}); got.Any() {
		t.Fatalf("scratch = %+v, want none", got)
	}
}

// What the runner is told it has is what the mount is asked for: the plan reads
// the placement the daemon is given, not a copy of its arithmetic.
func TestThePlanSaysWhatTheMountsAreAskedFor(t *testing.T) {
	pool := store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}, Tmp: store.TmpfsMount{Enabled: true, SizeMB: 2048}}
	spec := scratchSpec(16384, pool)

	got := PlanScratch(spec, pool)
	mounts := tmpfsMounts(spec, 16384, false)

	work, ok := folder(got, store.ScratchWork)
	if !ok || !work.InMemory() || !work.Auto || work.Why != "" {
		t.Fatalf("work = %+v, want it in memory", work)
	}
	if want := "size=" + strconv.FormatInt(work.SizeMB, 10) + "m"; mounts[RunnerWorkMount][:len(want)] != want {
		t.Fatalf("the mount is %q, the plan says %d MB", mounts[RunnerWorkMount], work.SizeMB)
	}
	tmp, ok := folder(got, store.ScratchTmp)
	if !ok || tmp.SizeMB != 2048 || tmp.AskedMB != 2048 || tmp.Auto {
		t.Fatalf("tmp = %+v, want the typed 2048 MB", tmp)
	}
	if _, listed := folder(got, store.ScratchDaemon); listed {
		t.Fatal("a pool that did not ask for the image store has one in its plan")
	}
}

// An automatic folder is where the runner has room for it to be useful, and on
// disk where it has not: the same pool, a smaller runner, a different answer --
// with the reason, which is what an operator comparing two runners wants.
func TestAnAutomaticFolderStaysOnDiskOnARunnerTooSmallForIt(t *testing.T) {
	pool := store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}}
	got := PlanScratch(scratchSpec(2048, pool), pool)
	work, ok := folder(got, store.ScratchWork)
	if !ok || work.InMemory() || work.Why != store.ScratchAutoTooSmall || work.AskedMB != store.DefaultTmpfsWorkMB {
		t.Fatalf("work = %+v, want it on disk because the runner is too small, having asked for the default", work)
	}
	if got.InMemory() != 0 {
		t.Fatalf("%d folders in memory on a runner too small for any", got.InMemory())
	}
}

// The host's owner has the last word, and what the pool wanted is still said.
func TestAFolderTheHostTurnedOffIsRecordedAsWanted(t *testing.T) {
	pool := store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, SizeMB: 4096}}
	spec := scratchSpec(16384, store.TmpfsConfig{}) // what the host's policy left of it
	got := PlanScratch(spec, pool)
	work, ok := folder(got, store.ScratchWork)
	if !ok || work.InMemory() || work.Why != store.ScratchHostOff || work.AskedMB != 4096 {
		t.Fatalf("work = %+v, want it on disk because the host said no", work)
	}
}

// A host's own standard replaces the default for a folder the pool leaves to
// size itself, and a ceiling lowers what is given and not what was asked.
func TestAHostsStandardAndCeilingShapeWhatIsAskedAndGiven(t *testing.T) {
	pool := store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}
	spec := scratchSpec(64*1024, pool)
	spec.TmpfsHost = store.HostTmpfs{WorkMB: 16384, MaxMB: 8192}
	work, _ := folder(PlanScratch(spec, pool), store.ScratchWork)
	if work.AskedMB != 16384 || work.SizeMB != 8192 {
		t.Fatalf("work = %+v, want 16384 asked (the host's standard) and 8192 given (its ceiling)", work)
	}
}

// A work folder bound from a host directory is left to that bind, and says so.
func TestABoundWorkFolderIsNotAMemoryFolder(t *testing.T) {
	pool := store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, SizeMB: 4096}, Tmp: store.TmpfsMount{Enabled: true, SizeMB: 1024}}
	spec := scratchSpec(16384, pool)
	spec.WorkDir = "/srv/runner-work"
	got := PlanScratch(spec, pool)
	work, _ := folder(got, store.ScratchWork)
	tmp, _ := folder(got, store.ScratchTmp)
	if work.InMemory() || work.Why != store.ScratchBound || !tmp.InMemory() {
		t.Fatalf("work = %+v, tmp = %+v, want the bind to win the work folder and /tmp to be in memory", work, tmp)
	}
}

// The image store is the sidecar's, charged to its own limit, which for a pair
// sized by its host is half of the slot.
func TestTheImageStoreIsPlacedOnTheSidecarsLimitNotTheRunners(t *testing.T) {
	pool := store.TmpfsConfig{Daemon: store.TmpfsMount{Enabled: true, Auto: true}}
	spec := scratchSpec(16384, pool)
	spec.DockerMode = store.DockerDinD
	spec.ResourcesSource = store.AllocationFromHost // one slot, split between the two
	spec.Resources = store.Resources{MemoryMB: 32768}
	got := PlanScratch(spec, pool)
	image, ok := folder(got, store.ScratchDaemon)
	if !ok || !image.InMemory() || image.AskedMB != store.DefaultTmpfsDaemonMB {
		t.Fatalf("image store = %+v, want it in memory on a 16 GB daemon half", image)
	}

	// Without docker-in-docker there is no sidecar to keep it in.
	spec.DockerMode = store.DockerNone
	if _, listed := folder(PlanScratch(spec, pool), store.ScratchDaemon); listed {
		t.Fatal("a runner with no sidecar has an image store in its plan")
	}
}
