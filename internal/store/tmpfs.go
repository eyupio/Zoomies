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
}

// Any reports whether any folder is kept in memory.
func (c TmpfsConfig) Any() bool { return c.Work.Enabled || c.Tmp.Enabled }

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

// RecommendedMemoryMB is the limit that leaves a runner capMB's worth of room
// for its job on top of whatever the tmpfs mounts may fill. Zero when there is
// nothing to propose: no limit to raise, or no mount in memory.
func (c TmpfsConfig) RecommendedMemoryMB(capMB int64) int64 {
	if capMB <= 0 || !c.Any() {
		return 0
	}
	return capMB + c.ReserveMB()
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
	workMB, workAuto := want(c.Work, DefaultTmpfsWorkMB)
	tmpMB, tmpAuto := want(c.Tmp, DefaultTmpfsTmpMB)
	if capMB <= 0 || auto == 0 {
		return workMB, tmpMB
	}
	room := capMB/2 - explicit
	if room >= auto {
		return workMB, tmpMB
	}
	fit := func(def int64) int64 { return max(room*def/auto, MinTmpfsMB) }
	if workAuto {
		workMB = fit(DefaultTmpfsWorkMB)
	}
	if tmpAuto {
		tmpMB = fit(DefaultTmpfsTmpMB)
	}
	return workMB, tmpMB
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
	}{{"work", c.Work}, {"tmp", c.Tmp}} {
		name := "tmpfs." + m.name + ".size_mb"
		if m.mount.SizeMB < 0 {
			return name, "a size cannot be negative; use 0 to size it from the memory limit"
		}
		if !m.mount.Enabled {
			continue
		}
		if m.mount.SizeMB > 0 && m.mount.SizeMB < MinTmpfsMB {
			return name, fmt.Sprintf("a size must be at least %d MB, or 0 to size it from the memory limit", MinTmpfsMB)
		}
		if m.mount.SizeMB > 0 {
			explicit += m.mount.SizeMB
			last = name
		}
	}
	if capMB > 0 && explicit >= capMB {
		return last, fmt.Sprintf("the sizes total %d MB and the runner's memory limit is %d MB; a tmpfs is charged to that limit, "+
			"so raise the limit (to at least %d MB) or shrink the folders", explicit, capMB, capMB+explicit)
	}
	return "", ""
}
