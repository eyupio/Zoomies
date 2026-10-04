package machine

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Usage describes the whole machine, including work not owned by Zoomies.
// A missing value means unmeasured, never idle. Linux procfs is the only
// source for now; other platforms retain reservation-based placement.
type Usage struct {
	CPUPercent        *float64
	MemoryAvailableMB *int64
	// LoadAverage1 is the kernel's one-minute load average: the mean number
	// of runnable or uninterruptible tasks. It is the figure that keeps
	// climbing after CPU occupancy has pinned at 100%, so it is what says how
	// far past its cores a host has been pushed rather than merely that it is
	// busy. Whole-machine, like the other two, so it is only reported where
	// the CPU count is the machine's.
	LoadAverage1 *float64
	// IOWaitPercent is the share of CPU time the machine spent idle with
	// something waiting on disk, between two samples. CPUPercent counts that
	// time as busy, which is right for deciding whether a host has room and
	// wrong for asking why a job is slow: a build waiting on a saturated disk
	// reads as a machine hard at work. This is the figure that tells the two
	// apart, and the reason a pool is told its scratch folders could live in
	// memory.
	IOWaitPercent *float64
}

// UsageSampler takes CPU deltas between heartbeats without sleeping or asking
// Docker to do more work while it may already be overloaded.
type UsageSampler struct {
	mu          sync.Mutex
	root        string
	total, idle uint64
	iowait      uint64
	cpus        int
}

func (s *UsageSampler) Sample(cpus int, memoryMB int64) Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Usage
	stat, err := os.ReadFile(filepath.Join(s.root, "/proc/stat"))
	if err == nil {
		total, idle, count, ok := cpuTicks(string(stat))
		iowait, ioOK := ioWaitTicks(string(stat))
		if ok && count == cpus && count == s.cpus && total > s.total && idle >= s.idle && idle-s.idle <= total-s.total {
			percent := 100 * (1 - float64(idle-s.idle)/float64(total-s.total))
			out.CPUPercent = &percent
			// Only beside a CPU figure: both are shares of the same interval,
			// and a counter that went backwards is not the machine sampled
			// last time.
			if ioOK && iowait >= s.iowait && iowait-s.iowait <= total-s.total {
				wait := 100 * float64(iowait-s.iowait) / float64(total-s.total)
				out.IOWaitPercent = &wait
			}
		}
		if ok && count == cpus {
			s.total, s.idle, s.iowait, s.cpus = total, idle, iowait, count
		} else {
			s.total, s.idle, s.iowait, s.cpus = 0, 0, 0, 0
		}
	} else {
		s.total, s.idle, s.iowait, s.cpus = 0, 0, 0, 0
	}
	mem, err := os.ReadFile(filepath.Join(s.root, "/proc/meminfo"))
	if err == nil {
		total, available, ok := availableMemory(string(mem))
		// Never label an agent cgroup's figures as its sibling runners' host,
		// or a remote daemon's machine as the one this process can see.
		if ok && total == memoryMB {
			out.MemoryAvailableMB = &available
		}
	}
	// The load average is only meaningful against the CPU count it is read
	// beside, so it is subject to the same test the CPU sample is: procfs has
	// to be describing the machine the caller believes it is on.
	if la, err := os.ReadFile(filepath.Join(s.root, "/proc/loadavg")); err == nil && s.cpus == cpus && cpus > 0 {
		if v, ok := loadAverage(string(la)); ok {
			out.LoadAverage1 = &v
		}
	}
	return out
}

// loadAverage reads the one-minute figure from /proc/loadavg, whose first
// three fields are the 1, 5 and 15 minute averages.
func loadAverage(raw string) (float64, bool) {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// ioWaitTicks reads the iowait counter from the aggregate cpu line of
// /proc/stat: the fifth number after the name, after user, nice, system and
// idle. A kernel too old to report it has a shorter line, which is not
// measured rather than zero.
func ioWaitTicks(raw string) (uint64, bool) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] != "cpu" {
			continue
		}
		v, err := strconv.ParseUint(fields[5], 10, 64)
		return v, err == nil
	}
	return 0, false
}

func cpuTicks(raw string) (total, idle uint64, cpus int, ok bool) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "cpu" {
			if len(fields) < 5 {
				return 0, 0, 0, false
			}
			// Guest time is already included in user/nice. Count only the first
			// eight counters. I/O wait counts as unavailable CPU headroom here.
			for i := 1; i < len(fields) && i <= 8; i++ {
				v, err := strconv.ParseUint(fields[i], 10, 64)
				if err != nil || total+v < total {
					return 0, 0, 0, false
				}
				total += v
				if i == 4 {
					idle = v
				}
			}
			ok = true
		} else if strings.HasPrefix(fields[0], "cpu") {
			if _, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu")); err == nil {
				cpus++
			}
		}
	}
	return total, idle, cpus, ok && cpus > 0
}

func availableMemory(raw string) (total, available int64, ok bool) {
	total, available = -1, -1
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		if fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || v < 0 {
			return 0, 0, false
		}
		if fields[0] == "MemTotal:" {
			total = v
		} else {
			available = v
		}
	}
	// MemAvailable includes reclaimable caches; MemFree alone would call a
	// healthy build host full just because Linux put idle memory to use.
	return total / 1024, available / 1024, total >= 1024 && available >= 0 && available <= total
}
