package backend

import (
	"strings"

	"github.com/eyupio/zoomies/internal/store"
)

// PlanScratch is which of a runner's folders the container backends will keep
// in memory, and at what size, worked out ahead of the create from the same spec
// the create will be made from. The controller writes the answer on the runner's
// row so the Runners page can say what a runner was given -- recorded, not
// recomputed, because a pool edited since would give a different answer than the
// one the running job was started with.
//
// asked is the pool's own configuration. spec.Tmpfs is already what the host's
// say left of it, and a folder the pool wanted in memory that the spec no longer
// carries is one the host's owner turned off, which is worth saying apart from
// a folder that was merely too small for the runner.
//
// It uses the placement the mounts use (tmpfsMounts and daemonTmpfsMounts), the
// same limits for each container (pairLimits), and the same rule that a work
// folder bound from a host directory is left to that bind, so what an operator
// reads is what the daemon is asked for.
func PlanScratch(spec Spec, asked store.TmpfsConfig) store.RunnerScratch {
	if !asked.Any() {
		return store.RunnerScratch{}
	}
	host := hostTmpfs(spec)
	runner, daemon := spec.Resources, spec.Resources
	dind := spec.DockerMode == store.DockerDinD
	if dind {
		runner, daemon = pairLimits(spec)
	}
	workBound := strings.TrimSpace(spec.WorkDir) != ""
	work, tmp := spec.Tmpfs.PlaceRunner(runner.MemoryMB, host)
	var image int64
	if dind {
		image = spec.Tmpfs.PlaceDaemon(daemon.MemoryMB, host)
	}

	var out store.RunnerScratch
	add := func(kind store.ScratchKind, want, effective store.TmpfsMount, standard, def, got int64, bound bool) {
		if !want.Enabled {
			return
		}
		f := store.ScratchFolder{Kind: kind, AskedMB: want.AskedMB(standard, def), Auto: want.Auto}
		switch {
		case bound:
			f.Why = store.ScratchBound
		case !effective.Enabled:
			f.Why = store.ScratchHostOff
		case got <= 0:
			f.Why = store.ScratchAutoTooSmall
		default:
			f.SizeMB = got
		}
		out.Folders = append(out.Folders, f)
	}
	add(store.ScratchWork, asked.Work, spec.Tmpfs.Work, host.WorkMB, store.DefaultTmpfsWorkMB, work, workBound)
	add(store.ScratchTmp, asked.Tmp, spec.Tmpfs.Tmp, host.TmpMB, store.DefaultTmpfsTmpMB, tmp, false)
	if dind {
		add(store.ScratchDaemon, asked.Daemon, spec.Tmpfs.Daemon, host.DaemonMB, store.DefaultTmpfsDaemonMB, image, false)
	}
	return out
}
