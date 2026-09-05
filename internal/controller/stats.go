package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// defaultStatsWindow is the period the Overview summarises when the caller
// does not ask for one. A day is long enough for the completed and failed
// counts to mean something on a fleet that only runs during working hours.
const defaultStatsWindow = 24 * time.Hour

// Stats is the Overview payload: what the queue is doing, what the fleet is
// doing, and how long jobs are waiting.
type Stats struct {
	// Window is the period Completed, Failed and the wait percentiles cover.
	Window            string `json:"window"`
	QueuedJobs        int    `json:"queued_jobs"`
	RunningJobs       int    `json:"running_jobs"`
	Completed         int    `json:"completed"`
	Failed            int    `json:"failed"`
	MedianWaitMS      int64  `json:"median_wait_ms"`
	P95WaitMS         int64  `json:"p95_wait_ms"`
	P50StartupMS      int64  `json:"p50_startup_ms"`
	P95StartupMS      int64  `json:"p95_startup_ms"`
	P50RegistrationMS int64  `json:"p50_registration_ms"`
	P95RegistrationMS int64  `json:"p95_registration_ms"`

	Runners RunnerStats `json:"runners"`
	Hosts   HostStats   `json:"hosts"`
	Pools   []PoolStats `json:"pools"`
}

// RunnerStats counts the fleet by runner state.
type RunnerStats struct {
	Provisioning int `json:"provisioning"`
	Registering  int `json:"registering"`
	Idle         int `json:"idle"`
	Busy         int `json:"busy"`
	Draining     int `json:"draining"`
	Failed       int `json:"failed"`
	Total        int `json:"total"`
}

// HostStats summarises the agents and the room they have left.
type HostStats struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Cordoned int `json:"cordoned"`
	Capacity int `json:"capacity"`
	Used     int `json:"used"`
}

// PoolStats is one row of the Overview's per-pool utilisation.
type PoolStats struct {
	PoolID   string `json:"pool_id"`
	PoolName string `json:"pool_name"`
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Live     int    `json:"live"`
	Busy     int    `json:"busy"`
	Idle     int    `json:"idle"`
	// Queued is the number of queued jobs this pool has claimed.
	Queued      int     `json:"queued"`
	Utilisation float64 `json:"utilisation"`
}

// Stats builds the Overview payload over a rolling window.
func (c *Controller) Stats(ctx context.Context, window time.Duration) (*Stats, error) {
	if window <= 0 {
		window = defaultStatsWindow
	}
	since := c.Now().Add(-window)

	js, err := c.st.StatsSince(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("computing job statistics: %w", err)
	}
	out := &Stats{
		Window:       window.String(),
		QueuedJobs:   js.Queued,
		RunningJobs:  js.Running,
		Completed:    js.CompletedLast,
		Failed:       js.Failed,
		MedianWaitMS: js.MedianWaitMS,
		P95WaitMS:    js.P95WaitMS,
	}

	counts, err := c.st.CountRunnersByPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting runners: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing queued jobs: %w", err)
	}
	queuedByPool := map[string]int{}
	for _, j := range queued {
		queuedByPool[j.PoolID]++
	}
	runners, _, err := c.st.ListRunners(ctx, store.RunnerFilter{IncludeRemoved: true}, store.Page{Limit: 10000})
	if err == nil {
		var startup, registration []int64
		for _, r := range runners {
			if r.CreatedAt.Before(since) {
				continue
			}
			if r.ContainerStartedAt != nil && !r.ContainerStartedAt.Before(r.CreatedAt) {
				startup = append(startup, r.ContainerStartedAt.Sub(r.CreatedAt).Milliseconds())
			}
			if r.ContainerStartedAt != nil && r.RegisteredAt != nil && !r.RegisteredAt.Before(*r.ContainerStartedAt) {
				registration = append(registration, r.RegisteredAt.Sub(*r.ContainerStartedAt).Milliseconds())
			}
		}
		out.P50StartupMS, out.P95StartupMS = percentile(startup, .50), percentile(startup, .95)
		out.P50RegistrationMS, out.P95RegistrationMS = percentile(registration, .50), percentile(registration, .95)
	}

	out.Pools = make([]PoolStats, 0, len(pools))
	for _, p := range pools {
		pc := counts[p.ID]
		out.Runners.Provisioning += pc.Provisioning
		out.Runners.Registering += pc.Registering
		out.Runners.Idle += pc.Idle
		out.Runners.Busy += pc.Busy
		out.Runners.Draining += pc.Draining
		out.Runners.Failed += pc.Failed
		out.Pools = append(out.Pools, PoolStats{
			PoolID:      p.ID,
			PoolName:    p.Name,
			Min:         p.MinRunners,
			Max:         p.MaxRunners,
			Live:        pc.Live(),
			Busy:        pc.Busy,
			Idle:        pc.Idle,
			Queued:      queuedByPool[p.ID],
			Utilisation: pc.Utilisation(),
		})
	}
	out.Runners.Total = out.Runners.Provisioning + out.Runners.Registering + out.Runners.Idle +
		out.Runners.Busy + out.Runners.Draining + out.Runners.Failed

	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}
	now := c.Now()
	for _, h := range hosts {
		out.Hosts.Total++
		if h.Healthy(now) {
			out.Hosts.Healthy++
		}
		if h.Cordoned {
			out.Hosts.Cordoned++
		}
		// An unhealthy host's capacity is not capacity anyone can use, so it
		// is left out rather than flattering the total.
		if h.Healthy(now) && !h.Cordoned {
			out.Hosts.Capacity += h.Capacity
		}
		out.Hosts.Used += h.ActiveRunners
	}
	return out, nil
}

func percentile(values []int64, q float64) int64 {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	i := int(float64(len(values)-1)*q + .5)
	return values[i]
}

// Samples returns the Overview's sparkline points since a cutoff.
func (c *Controller) Samples(ctx context.Context, since time.Time) ([]store.FleetSample, error) {
	return c.st.ListSamples(ctx, since)
}
