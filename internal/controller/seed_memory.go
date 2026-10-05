package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// seedMemory gives the demo fleet something to show for the memory valve and for
// in-memory folders: what a runner's row says when its pool uses either.
//
// The folders are worked out by the function the backend mounts them from, so a
// demo runner says what a real one would; the valve's figures are the agent's to
// report, which a fixture has no agent to do, so they are written the way a
// report would write them. Each runner below is a state worth seeing -- a loan,
// a loan and swap, a loan that has reached its ceiling, a valve watching, a valve
// only observing -- and none of them is a failure.
func (c *Controller) seedMemory(ctx context.Context, now time.Time, pools []*store.Pool, hosts []*store.Host, runners []*store.Runner) error {
	poolByID := make(map[string]*store.Pool, len(pools))
	for _, p := range pools {
		poolByID[p.ID] = p
	}
	// What the valve has done for the runner at each position of the fixture's
	// runner list. Positions are the fixture's own, as they are in seedRunners.
	type valve struct {
		sample   backend.MemoryValveSample
		memoryMB int64
	}
	mb := int64(1) << 20
	states := map[int]valve{
		0: {backend.MemoryValveSample{Mode: "automatic", Code: string(agent.MemoryRaised), Raises: 2, LentBytes: 1536 * mb,
			Reason: "raised the limit from 5120 to 5632 MB: it was using 4300 MB"}, 5000},
		1: {backend.MemoryValveSample{Mode: "automatic", Code: string(agent.MemorySpilled), Raises: 3, LentBytes: 2048 * mb, SpillBytes: 2048 * mb, NearLimit: true,
			Reason: "that is the most this runner may have (6144 MB); allowed 2048 MB of swap beyond its 6144 MB limit"}, 5900},
		2: {backend.MemoryValveSample{Mode: "automatic", Code: string(agent.MemoryHealthy)}, 1800},
		3: {backend.MemoryValveSample{Mode: "automatic", Code: string(agent.MemoryHealthy)}, 400},
		4: {backend.MemoryValveSample{Mode: "automatic", Code: string(agent.MemoryAtCeiling), Raises: 4, LentBytes: 2048 * mb, NearLimit: true,
			Reason: "it holds 6144 MB, the most this runner may have"}, 6000},
		5: {backend.MemoryValveSample{Mode: "observe", Code: string(agent.MemoryRaised), NearLimit: true, WouldLendBytes: 1024 * mb,
			Reason: "raised the limit from 8192 to 9216 MB: it was using 7400 MB"}, 7400},
		8: {backend.MemoryValveSample{Mode: "automatic", Code: string(agent.MemoryRaised), Raises: 1, LentBytes: 512 * mb,
			Reason: "raised the limit from 4096 to 4608 MB: it was using 3500 MB"}, 4000},
	}
	for i, r := range runners {
		p := poolByID[r.PoolID]
		if p == nil || r.State.Terminal() {
			continue
		}
		if v, ok := states[i]; ok {
			at := now
			raw, err := json.Marshal(backend.Stats{SampledAt: &at, CPUPercent: r.CPUPercent, MemoryBytes: v.memoryMB * mb, MemoryValve: &v.sample})
			if err != nil {
				return fmt.Errorf("encoding the demo memory sample for %s: %w", r.Name, err)
			}
			if err := c.st.SetRunnerResourceSample(ctx, r.ID, r.CPUPercent, v.memoryMB*mb, raw); err != nil {
				return fmt.Errorf("seeding the demo memory sample for %s: %w", r.Name, err)
			}
			if lent := v.sample.LentBytes / mb; lent > 0 {
				if err := c.st.SetRunnerLentMemory(ctx, r.ID, lent); err != nil {
					return fmt.Errorf("seeding the memory lent to %s: %w", r.Name, err)
				}
			}
		}
		if p.Tmpfs.Any() {
			spec := backend.Spec{
				Resources: p.Resources, ResourcesSource: store.AllocationFromPool,
				Tmpfs: p.Tmpfs, DockerMode: p.DockerMode,
			}
			if scratch := backend.PlanScratch(spec, p.Tmpfs); scratch.Any() {
				if err := c.st.SetRunnerScratch(ctx, r.ID, scratch); err != nil {
					return fmt.Errorf("seeding the in-memory folders of %s: %w", r.Name, err)
				}
			}
		}
	}
	// Each host's pool, from the same plan a heartbeat makes, so the Hosts page
	// shows what the controller would have worked out rather than a number
	// written here.
	for _, h := range hosts {
		c.elasticMemoryDirective(ctx, h, agent.HeartbeatRequest{Features: []string{agent.FeatureElasticMemory}}, now)
	}
	return nil
}
