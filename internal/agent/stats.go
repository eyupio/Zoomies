package agent

import (
	"context"
	"sync"
	"time"
)

// Sampling has its own bounded budget. A busy host must not spend a second per
// container in the same loop that notices exited jobs and releases disk space.
//
// A non-streaming Docker stats call takes a second at least: the daemon reads
// the cgroup twice, a second apart, to have a CPU rate to report. Two seconds
// left a loaded daemon no headroom and every sample on the host went stale
// together, which read as a fleet of idle runners.
const (
	statsInterval = 30 * time.Second
	statsBudget   = 20 * time.Second
	statsTimeout  = 5 * time.Second
	statsWorkers  = 4
	// statsMissesSaid is how many samples in a row may fail before the agent says so at Info:
	// five is two and a half minutes, which a loaded daemon can cost without anything being
	// wrong and a stuck one cannot.
	statsMissesSaid = 5
)

func (a *Agent) statsLoop(ctx context.Context) error {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()
	for {
		a.sampleStats(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (a *Agent) sampleStats(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, statsBudget)
	defer cancel()
	jobs := make(chan tracked)
	var workers sync.WaitGroup
	for range statsWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for r := range jobs {
				if ctx.Err() != nil {
					continue
				}
				b, err := a.opts.Backends.Get(r.kind)
				if err != nil {
					continue
				}
				sctx, stop := context.WithTimeout(ctx, statsTimeout)
				stats, err := b.Stats(sctx, r.handle)
				stop()
				if err != nil {
					a.log.Debug("runner stats are unavailable; retaining the last sample",
						"runner", r.runnerID, "error", err)
					a.noteStatsMiss(r)
					continue
				}
				sampledAt := a.now()
				stats.SampledAt = &sampledAt
				a.mu.Lock()
				if current := a.runners[r.runnerID]; current != nil &&
					current.handle == r.handle && !current.terminal && !current.hostRemoved {
					current.stats = stats
					current.statsMisses = 0
				}
				a.mu.Unlock()
			}
		}()
	}
send:
	for _, r := range a.trackedRunners() {
		if r.terminal || r.hostRemoved || r.handle == "" {
			continue
		}
		select {
		case jobs <- r:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	workers.Wait()
}

// noteStatsMiss counts a failed sample and says so once when they have run on. The failure
// itself is logged at Debug, and with the last reading retained nothing shows; but readings
// that stop coming leave the controller sizing from a stale picture, and "not enough
// samples" is all it can say for it.
func (a *Agent) noteStatsMiss(r tracked) {
	a.mu.Lock()
	current := a.runners[r.runnerID]
	if current == nil || current.handle != r.handle {
		a.mu.Unlock()
		return
	}
	current.statsMisses++
	said := current.statsMisses == statsMissesSaid
	a.mu.Unlock()
	if said {
		a.log.Info("runner stats have been unavailable for several samples in a row; the last reading is being kept, and sizing evidence for this runner is going stale",
			"runner", r.runnerID, "samples", statsMissesSaid)
	}
}
