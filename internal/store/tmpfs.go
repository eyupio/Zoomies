package store

import "fmt"

// A pool's RAM-backed scratch space. A runner's work folder and /tmp normally
// live on its container's writable layer, which is on the host's Docker data
// root; on a host with slow disks and spare memory that layer is where every
// checkout, install and build waits. Mounting a tmpfs over them moves that
// traffic into RAM.
//
// It is opt-in, and the reason is the trade it makes: tmpfs pages are charged
// to the runner's own memory cgroup, so the size below is room taken out of the
// pool's memory limit, not added to it. A pool that turns this on without
// raising its limit has a job that can run out of memory sooner, which is why
// the controller proposes the raise (ReserveMB) rather than making it quietly.

const (
	// DefaultTmpfsWorkMB and DefaultTmpfsTmpMB are what an enabled mount with no
	// size of its own is given when the runner has no memory limit to fit them to.
	DefaultTmpfsWorkMB int64 = 4096
	DefaultTmpfsTmpMB  int64 = 1024
	// DefaultTmpfsDaemonMB is the same for the docker-in-docker sidecar's image
	// store. It is larger because what lives there -- every image a job pulls and
	// every layer it builds -- is larger than a checkout, and a pull that does not
	// fit fails the job rather than slowing it.
	DefaultTmpfsDaemonMB int64 = 8192
	// AutoMinWorkMB, AutoMinTmpMB and AutoMinDaemonMB are the least an automatic
	// folder is worth. An auto folder that would be fitted below its floor is kept
	// on disk instead: a checkout, a build's temporary files or an image pull
	// that does not fit fails with "no space left on device", which names neither
	// the mount nor the setting, and a folder that small saves little disk traffic
	// anyway. A size an operator typed lowers the floor to itself, because they
	// asked for exactly that much.
	AutoMinWorkMB   int64 = 2048
	AutoMinTmpMB    int64 = 1024
	AutoMinDaemonMB int64 = 4096
	// MinTmpfsMB is the least a tmpfs may be. Below it a checkout fails in a way
	// that reads as a broken runner rather than a small mount.
	MinTmpfsMB int64 = 64
)

// TmpfsMount is one RAM-backed folder.
type TmpfsMount struct {
	Enabled bool `json:"enabled"`
	// SizeMB is the mount's ceiling. Zero lets the runner's memory limit pick it
	// (TmpfsConfig.Sizes), which is what a pool that has not thought about it
	// wants: a size that fits whatever machine share the runner was given.
	SizeMB int64 `json:"size_mb,omitempty"`
	// Auto lets each runner decide: the folder is in memory where the runner has
	// room for it to be useful (see the AutoMin* floors) and on disk where it has
	// not. Without it an enabled folder is always in memory, fitted as small as
	// the limit demands, which is the choice for an operator who knows better than
	// the arithmetic. Meaningless unless Enabled.
	Auto bool `json:"auto,omitempty"`
}

// TmpfsConfig says which of a runner's folders are kept in memory. The zero
// value is nothing, so an upgrade never changes an existing pool.
type TmpfsConfig struct {
	// Work is the runner's _work folder: the checkout, build output and the
	// runner's own temporary files. It is the one worth having, and the one the
	// editor offers first.
	Work TmpfsMount `json:"work"`
	// Tmp is /tmp. It is separate because some toolchains put their heaviest
	// traffic there and some jobs leave gigabytes in it, and an operator should
	// choose that cost knowingly.
	Tmp TmpfsMount `json:"tmp"`
	// Daemon is the docker-in-docker sidecar's image store, /var/lib/docker in
	// the daemon's container: where every image a job pulls and every layer it
	// builds is written. It is a different container from the two above, with a
	// memory limit of its own -- the daemon's half of the pair for a pool sized by
	// its host, the full typed limit otherwise -- and the tmpfs is charged to that
	// limit, not the runner's. It is its own choice, and only a pool with
	// docker_mode dind has the container to mount it on, because filling it is
	// the one way this setting can fail a job that used to pass: an image bigger
	// than the mount does not pull.
	Daemon TmpfsMount `json:"daemon"`
}

// Any reports whether any folder is kept in memory.
func (c TmpfsConfig) Any() bool { return c.Work.Enabled || c.Tmp.Enabled || c.Daemon.Enabled }

// ReserveMB is the memory this configuration may take, which is what a memory
// limit sized for the job alone should be raised by. A mount with no size of
// its own counts at its default, because that is what it would be given on a
// runner with room.
func (c TmpfsConfig) ReserveMB() int64 {
	var total int64
	for _, m := range []struct {
		mount TmpfsMount
		def   int64
	}{{c.Work, DefaultTmpfsWorkMB}, {c.Tmp, DefaultTmpfsTmpMB}} {
		switch {
		case !m.mount.Enabled:
		case m.mount.SizeMB > 0:
			total += m.mount.SizeMB
		default:
			total += m.def
		}
	}
	return total
}

// DaemonReserveMB is the memory the sidecar's image store may take: what was
// typed, or the default for one left to size itself, and nothing when it is off.
// It is counted apart from ReserveMB because it is charged to another container.
func (c TmpfsConfig) DaemonReserveMB() int64 {
	switch {
	case !c.Daemon.Enabled:
		return 0
	case c.Daemon.SizeMB > 0:
		return c.Daemon.SizeMB
	default:
		return DefaultTmpfsDaemonMB
	}
}

// RecommendedMemoryMB is the limit that leaves a runner capMB's worth of room
// for its job on top of whatever the tmpfs mounts may fill. Zero when there is
// nothing to propose: no limit to raise, or no mount in memory.
//
// A typed limit is given to the runner and to the daemon alike, and each
// container is charged for its own folders, so the limit has to cover whichever
// needs more: the sum would pay for room neither container uses.
func (c TmpfsConfig) RecommendedMemoryMB(capMB int64) int64 {
	if capMB <= 0 || !c.Any() {
		return 0
	}
	reserve := max(c.ReserveMB(), c.DaemonReserveMB())
	// At least twice what the folders may take, as well as the limit plus them:
	// folders are fitted into half a limit, so a proposal that left them more than
	// half would still be tight when it was taken, and the next would follow it
	// upward. On a limit smaller than the folders the sum alone falls short of that.
	return max(capMB+reserve, 2*reserve)
}

// DaemonSize returns what the image store is given on a daemon whose memory
// limit is capMB (zero is no limit): a typed size as it stands, otherwise the
// default fitted into half the limit, for the reason Sizes fits the others -- a
// store filling to its ceiling while the build also wants memory is the
// out-of-memory this setting must not cause by itself.
func (c TmpfsConfig) DaemonSize(capMB int64) int64 {
	return c.daemonSize(capMB, DefaultTmpfsDaemonMB)
}

// daemonSize is DaemonSize with the size an untyped store is asked for, which a
// host's own standard replaces.
func (c TmpfsConfig) daemonSize(capMB, def int64) int64 {
	switch {
	case !c.Daemon.Enabled:
		return 0
	case c.Daemon.SizeMB > 0 && capMB > 0 && c.Daemon.SizeMB >= capMB:
		// The same reduction Sizes makes: a typed size the daemon's real limit cannot
		// hold would OOM-kill it as the store filled.
		return max(capMB/2, MinTmpfsMB)
	case c.Daemon.SizeMB > 0:
		return c.Daemon.SizeMB
	case capMB <= 0:
		return def
	default:
		return min(def, max(capMB/2, MinTmpfsMB))
	}
}

// Sizes returns what each mount is actually given on a runner whose memory
// limit is capMB (zero is no limit).
//
// A size an operator typed is used as it stands. A mount left to the limit
// shares half of it, which is the most that leaves a job room to use the
// folder it asked for: a tmpfs filling to its ceiling while the build also
// wants memory is the OOM this feature must not cause on its own. With no limit
// there is nothing to fit to, and the defaults apply.
func (c TmpfsConfig) Sizes(capMB int64) (workMB, tmpMB int64) {
	return c.sizes(capMB, DefaultTmpfsWorkMB, DefaultTmpfsTmpMB)
}

// sizes is Sizes with the sizes untyped folders are asked for, which a host's
// own standards replace.
func (c TmpfsConfig) sizes(capMB, defWork, defTmp int64) (workMB, tmpMB int64) {
	var explicit, auto int64
	want := func(m TmpfsMount, def int64) (size int64, isAuto bool) {
		switch {
		case !m.Enabled:
			return 0, false
		case m.SizeMB > 0:
			explicit += m.SizeMB
			return m.SizeMB, false
		default:
			auto += def
			return def, true
		}
	}
	workMB, workAuto := want(c.Work, defWork)
	tmpMB, tmpAuto := want(c.Tmp, defTmp)
	if capMB <= 0 {
		return workMB, tmpMB
	}
	if auto == 0 {
		// Typed sizes were validated against the pool's stored limit, but the limit
		// a runner is really given can be smaller: an automatic pool takes its host's
		// share, and a fixed one is reduced on a small machine. Folders that together
		// reach that limit would OOM-kill the runner the moment a job filled them, so
		// they are scaled into half of it, in proportion, as auto sizes are.
		if explicit >= capMB {
			shrink := func(size int64) int64 {
				if size == 0 {
					return 0
				}
				return max(capMB/2*size/explicit, MinTmpfsMB)
			}
			return shrink(workMB), shrink(tmpMB)
		}
		return workMB, tmpMB
	}
	room := capMB/2 - explicit
	if room >= auto {
		return workMB, tmpMB
	}
	fit := func(def int64) int64 { return max(room*def/auto, MinTmpfsMB) }
	if workAuto {
		workMB = fit(defWork)
	}
	if tmpAuto {
		tmpMB = fit(defTmp)
	}
	return workMB, tmpMB
}

// HostTmpfs is a host's say over a pool's in-memory folders: some machines have
// memory to spare for them and some do not, and the pool, which is one setting
// for every host it lands on, cannot know which is which.
//
// It belongs to the host's runner profile, beside how big a runner is there,
// because it is the same kind of answer -- what a runner on this machine is
// built like -- and the operator's alone: an agent reports what it measures and
// never writes it. Zero is "not said", which leaves the pool's setting as it
// stands, so a host that says nothing changes nothing.
type HostTmpfs struct {
	// Disabled keeps every in-memory folder off this host, whatever a pool asks
	// for: its runners use disk, as they did before the setting existed. It is
	// for a machine with less memory than the pools that land on it assume.
	Disabled bool `json:"disabled,omitempty"`
	// MaxMB is the most any single in-memory folder may be on this host, applied
	// after a pool's own size is fitted to the runner's limit. Zero is no host
	// ceiling. A pool's size can lower it and never raise it, so a machine's
	// owner has the last word on how much of it a folder may take.
	MaxMB int64 `json:"max_mb,omitempty"`
	// WorkMB, TmpMB and DaemonMB are the sizes a folder is asked for on this
	// host when the pool leaves it to size itself, in place of the built-in
	// defaults: the folder-sized counterpart of a host's standard runner size. A
	// machine with 256 GB of memory can offer a work folder far larger than the
	// default, and one with 16 GB less. They are what is asked for, so the fit to
	// the runner's limit and MaxMB still apply; a size the pool typed is the
	// pool's and is not replaced. Zero is the default.
	WorkMB   int64 `json:"work_mb,omitempty"`
	TmpMB    int64 `json:"tmp_mb,omitempty"`
	DaemonMB int64 `json:"daemon_mb,omitempty"`
}

// Apply is the configuration a runner on this host is created with, and the
// ceiling its agent applies to each folder after fitting it to the runner's
// limit. A disabled host gets no folders, and no ceiling, since there is
// nothing to cap.
func (h HostTmpfs) Apply(c TmpfsConfig) (TmpfsConfig, int64) {
	if h.Disabled {
		return TmpfsConfig{}, 0
	}
	return c, h.MaxMB
}

// Cap lowers a folder's size to the host's ceiling, if it has one.
func Cap(sizeMB, maxMB int64) int64 {
	if maxMB > 0 && sizeMB > maxMB {
		return maxMB
	}
	return sizeMB
}

// pick is the host's standard where it has one, the built-in default otherwise.
func pick(standard, def int64) int64 {
	if standard > 0 {
		return standard
	}
	return def
}

// floorFor is the least an automatic folder is worth: its floor, or a typed
// size when that is smaller, since an operator who typed 1 GB asked for 1 GB.
func floorFor(m TmpfsMount, floor int64) int64 {
	if m.SizeMB > 0 {
		return min(floor, m.SizeMB)
	}
	return floor
}

// PlaceRunner is what the runner's work folder and /tmp are given on a runner
// whose memory limit is capMB, on a host with policy h: zero for a folder that
// stays on disk. It is the one answer the agent mounts from and the controller
// warns from, so what an operator is told is what a runner is given.
//
// The folders are fitted to the limit (Sizes) from the host's standards,
// lowered to the host's ceiling, and then each automatic folder that came out
// below its floor is put on disk -- the one furthest below first, since giving
// that one up frees room for the other -- and the rest refitted, until what is
// left is worth having. A folder that is not automatic is never dropped.
func (c TmpfsConfig) PlaceRunner(capMB int64, h HostTmpfs) (workMB, tmpMB int64) {
	cc := c
	defWork, defTmp := pick(h.WorkMB, DefaultTmpfsWorkMB), pick(h.TmpMB, DefaultTmpfsTmpMB)
	for range 3 {
		workMB, tmpMB = cc.sizes(capMB, defWork, defTmp)
		workMB, tmpMB = Cap(workMB, h.MaxMB), Cap(tmpMB, h.MaxMB)
		// How far each automatic folder is from being worth having, as a part of
		// its floor; the worst below one goes.
		worst, ratio := "", 1.0
		if cc.Work.Enabled && cc.Work.Auto {
			if r := float64(workMB) / float64(floorFor(cc.Work, AutoMinWorkMB)); r < ratio {
				worst, ratio = "work", r
			}
		}
		if cc.Tmp.Enabled && cc.Tmp.Auto {
			if r := float64(tmpMB) / float64(floorFor(cc.Tmp, AutoMinTmpMB)); r < ratio {
				worst, ratio = "tmp", r
			}
		}
		switch worst {
		case "work":
			cc.Work.Enabled = false
		case "tmp":
			cc.Tmp.Enabled = false
		default:
			return workMB, tmpMB
		}
	}
	workMB, tmpMB = cc.sizes(capMB, defWork, defTmp)
	return Cap(workMB, h.MaxMB), Cap(tmpMB, h.MaxMB)
}

// PlaceDaemon is PlaceRunner for the sidecar's image store, on a daemon whose
// memory limit is capMB. Zero is on disk.
func (c TmpfsConfig) PlaceDaemon(capMB int64, h HostTmpfs) int64 {
	size := Cap(c.daemonSize(capMB, pick(h.DaemonMB, DefaultTmpfsDaemonMB)), h.MaxMB)
	if c.Daemon.Enabled && c.Daemon.Auto && size < floorFor(c.Daemon, AutoMinDaemonMB) {
		return 0
	}
	return size
}

// Validate checks the configuration against the pool's memory limit (zero is
// none). It refuses what cannot work rather than what is merely unwise: sizes
// below the floor, and sizes that together take the whole limit, because a
// tmpfs counts against the cgroup and a job could never run in what is left.
//
// It returns the dotted name of the field to blame, so a form can put the
// message beside the control, and what is wrong; both are empty when the
// configuration is valid.
func (c TmpfsConfig) Validate(capMB int64) (field, problem string) {
	var explicit int64
	last := ""
	for _, m := range []struct {
		name  string
		mount TmpfsMount
	}{{"work", c.Work}, {"tmp", c.Tmp}, {"daemon", c.Daemon}} {
		name := "tmpfs." + m.name + ".size_mb"
		if m.mount.SizeMB < 0 {
			return name, "a size cannot be negative; use 0 to size it from the memory limit"
		}
		if m.mount.Auto && !m.mount.Enabled {
			return "tmpfs." + m.name + ".auto", "auto decides where an in-memory folder lives, so the folder has to be turned on for it to mean anything"
		}
		if !m.mount.Enabled {
			continue
		}
		if m.mount.SizeMB > 0 && m.mount.SizeMB < MinTmpfsMB {
			return name, fmt.Sprintf("a size must be at least %d MB, or 0 to size it from the memory limit", MinTmpfsMB)
		}
		// The image store is charged to the daemon's container, whose limit is
		// checked on its own below; only the runner's folders add up against the
		// runner's.
		if m.mount.SizeMB > 0 && m.name != "daemon" {
			explicit += m.mount.SizeMB
			last = name
		}
	}
	if capMB > 0 && c.Daemon.Enabled && c.Daemon.SizeMB >= capMB {
		return "tmpfs.daemon.size_mb", fmt.Sprintf("the image store is %d MB and the memory limit is %d MB; the daemon's tmpfs is charged to that limit, "+
			"so raise the limit (to at least %d MB) or shrink the store", c.Daemon.SizeMB, capMB, capMB+c.Daemon.SizeMB)
	}
	if capMB > 0 && explicit >= capMB {
		return last, fmt.Sprintf("the sizes total %d MB and the runner's memory limit is %d MB; a tmpfs is charged to that limit, "+
			"so raise the limit (to at least %d MB) or shrink the folders", explicit, capMB, capMB+explicit)
	}
	return "", ""
}
