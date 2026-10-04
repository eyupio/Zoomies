package api

import (
	"fmt"
	"math"

	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// validateRunnerProfile checks a host's runner profile the way the pool form
// checks a pool's sizes, and for the same reasons: below a quarter of a core or
// 512 MB a runner cannot be a runner, a minimum above the standard says
// something that cannot be true, and a figure the machine could never give is
// refused when the machine has said what it has.
//
// Zero is not an error and not a value: a field left out follows the fleet's
// setting, which is how every host starts. The measured limits are only applied
// where the agent has measured, because a host that has not reported its
// machine has nothing to compare a size with, and the profile is still saved --
// the scheduler places by slots alone on such a host, exactly as before.
func validateRunnerProfile(h *store.Host, p store.RunnerProfile) []fieldError {
	var errs []fieldError
	add := func(field, msg string) { errs = append(errs, fieldError{"runner_profile." + field, msg}) }
	alloc := h.Allocatable()

	cpuField := func(field string, v float64, what string) {
		switch {
		case math.IsNaN(v) || math.IsInf(v, 0):
			add(field, what+" has to be a number of CPUs")
		case v < 0:
			add(field, what+" cannot be negative; leave it out to follow the fleet's setting")
		case v > 0 && v < store.MinRunnerCPUs:
			add(field, what+" cannot be below a quarter of a core; under that the runner binary cannot keep up with its own job")
		}
	}
	memoryField := func(field string, v int64, what string) {
		switch {
		case v < 0:
			add(field, what+" cannot be negative; leave it out to follow the fleet's setting")
		case v > 0 && v < store.MinRunnerMemoryMB:
			add(field, what+" cannot be below 512 MB; under that the runner binary is killed before it takes a job")
		}
	}
	cpuField("minimum.cpus", p.Minimum.CPUs, "a minimum")
	memoryField("minimum.memory_mb", p.Minimum.MemoryMB, "a minimum")
	cpuField("standard.cpus", p.Standard.CPUs, "a standard size")
	memoryField("standard.memory_mb", p.Standard.MemoryMB, "a standard size")
	cpuField("standard.burst_max_cpus", p.Standard.BurstMaxCPUs, "a burst ceiling")

	// The in-memory folders' policy. A folder smaller than the floor is not a
	// folder a checkout fits in, and a host that keeps them off has nothing to cap,
	// so saying both is a contradiction rather than a belt and braces.
	switch {
	case p.Tmpfs.MaxMB < 0:
		add("tmpfs.max_mb", "a ceiling cannot be negative; leave it at 0 to set no host ceiling")
	case p.Tmpfs.MaxMB > 0 && p.Tmpfs.MaxMB < store.MinTmpfsMB:
		add("tmpfs.max_mb", fmt.Sprintf("a ceiling below %d MB leaves no folder a checkout fits in; leave it at 0, or keep the folders off here instead", store.MinTmpfsMB))
	case p.Tmpfs.Disabled && p.Tmpfs.MaxMB > 0:
		add("tmpfs.max_mb", "this host keeps in-memory folders off, so there is no folder to put a ceiling on; clear one of the two")
	}

	// A minimum above the standard is a floor above the size it floors, which
	// the pool form refuses in the same words.
	if p.Minimum.CPUs > 0 && p.Standard.CPUs > 0 && p.Minimum.CPUs > p.Standard.CPUs {
		add("minimum.cpus", fmt.Sprintf("the minimum (%s CPU) is above the standard size (%s CPU); a runner is never given less than the minimum, so it has to be the smaller of the two",
			scheduler.FormatCPUs(p.Minimum.CPUs), scheduler.FormatCPUs(p.Standard.CPUs)))
	}
	if p.Minimum.MemoryMB > 0 && p.Standard.MemoryMB > 0 && p.Minimum.MemoryMB > p.Standard.MemoryMB {
		add("minimum.memory_mb", fmt.Sprintf("the minimum (%d MB) is above the standard size (%d MB); a runner is never given less than the minimum, so it has to be the smaller of the two",
			p.Minimum.MemoryMB, p.Standard.MemoryMB))
	}
	// The ceiling is the most a runner may use, its guaranteed share included.
	if p.Standard.BurstMaxCPUs > 0 && p.Standard.CPUs > 0 && p.Standard.BurstMaxCPUs < p.Standard.CPUs {
		add("standard.burst_max_cpus", fmt.Sprintf("the burst ceiling (%s CPU) is below the standard size (%s CPU); it is the most a runner may use, its own share included, so it has to be at least the standard",
			scheduler.FormatCPUs(p.Standard.BurstMaxCPUs), scheduler.FormatCPUs(p.Standard.CPUs)))
	}

	// What the machine can give. A standard or a minimum above all of it could
	// never run one runner, which is the host refusing every pool, so it is
	// refused here rather than left to look like a quiet fleet.
	if alloc.CPUsKnown {
		if p.Standard.CPUs > alloc.CPUs+1e-6 {
			add("standard.cpus", fmt.Sprintf("this host has %s allocatable CPUs once its reserve is held back; a standard size of %s CPU could never run a single runner here",
				scheduler.FormatCPUs(alloc.CPUs), scheduler.FormatCPUs(p.Standard.CPUs)))
		}
		if p.Minimum.CPUs > alloc.CPUs+1e-6 {
			add("minimum.cpus", fmt.Sprintf("this host has %s allocatable CPUs once its reserve is held back; a minimum of %s CPU is more than it can give a runner",
				scheduler.FormatCPUs(alloc.CPUs), scheduler.FormatCPUs(p.Minimum.CPUs)))
		}
	}
	if alloc.MemoryKnown {
		if p.Standard.MemoryMB > alloc.MemoryMB {
			add("standard.memory_mb", fmt.Sprintf("this host has %d MB of allocatable memory once its reserve is held back; a standard size of %d MB could never run a single runner here",
				alloc.MemoryMB, p.Standard.MemoryMB))
		}
		if p.Minimum.MemoryMB > alloc.MemoryMB {
			add("minimum.memory_mb", fmt.Sprintf("this host has %d MB of allocatable memory once its reserve is held back; a minimum of %d MB is more than it can give a runner",
				alloc.MemoryMB, p.Minimum.MemoryMB))
		}
	}
	return errs
}
