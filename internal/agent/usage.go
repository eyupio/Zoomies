package agent

import (
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

func (a *Agent) hostUsage(infos []backend.Info, cpus int, memoryMB int64) *store.HostUsage {
	if remoteDaemon(infos) {
		// A remote daemon's load cannot be measured through this process's
		// procfs, even if the two machines happen to be the same size.
		return nil
	}
	sample := a.opts.SampleUsage
	if sample == nil {
		sample = a.usageSampler.Sample
	}
	u := sample(cpus, memoryMB)
	if u.CPUPercent == nil && u.MemoryAvailableMB == nil && u.LoadAverage1 == nil {
		return nil
	}
	return &store.HostUsage{CPUPercent: u.CPUPercent, MemoryAvailableMB: u.MemoryAvailableMB, LoadAverage1: u.LoadAverage1, IOWaitPercent: u.IOWaitPercent}
}
