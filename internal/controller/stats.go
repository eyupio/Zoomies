package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// defaultStatsWindow is the period this summarises when the caller does not ask
// for one.
//
// It is statsEventWindow, and has to be: the same numbers arrive by fetch and
// by `stats` frame, and while these disagreed -- a day for the fetch, an hour
// for the frame -- an Overview showed a day of completed jobs and a day's wait
// percentiles for the second or two before its first frame arrived, then
// silently replaced them with an hour's. Nobody watching could tell which
// window they were looking at, and the documented default said an hour
// throughout.
const defaultStatsWindow = statsEventWindow

// Stats is the Overview payload: what the queue is doing, what the fleet is
// doing, and how long jobs are waiting.
type Stats struct {
	// Window is the period the completed counts and the wait percentiles
	// cover. Completed is Succeeded + Failed + Cancelled + Unknown; a rate
	// computed from Completed and Failed alone would count a job GitHub
	// stopped reporting as a success, which is why the split is here.
	Window      string `json:"window"`
	QueuedJobs  int    `json:"queued_jobs"`
	RunningJobs int    `json:"running_jobs"`
	Completed   int    `json:"completed"`
	Succeeded   int    `json:"succeeded"`
	Failed      int    `json:"failed"`
	// FleetFailed is how many of Failed this deployment caused rather than the
	// workflows: a runner that never started, or one that stopped under the
	// job. It is a subset rather than a fifth outcome -- GitHub's answer for
	// such a job is still "failure", and a split that did not add up would be
	// a worse lie than no split.
	//
	// It is the number the taxonomy exists for. "Eleven failures this hour" is
	// a question; "eleven, nine of them ours" is an answer, and it sends a
	// different person to a different page.
	FleetFailed int `json:"fleet_failed"`
	// Faults counts those failures by category, so the Overview can say which
	// of the fleet's problems this is rather than only that it has one. Absent
	// categories are absent rather than zero.
	Faults map[store.FaultKind]int `json:"faults,omitempty"`
	// RunnerStartFaults counts the runners that failed before taking a job,
	// also by category. These are in no job count anywhere, because the jobs
	// they were meant for are still queued -- which is exactly why a fleet
	// that cannot start a container reads as a fleet that is merely busy.
	RunnerStartFaults map[store.FaultKind]int `json:"runner_start_faults,omitempty"`
	Cancelled         int                     `json:"cancelled"`
	Unknown           int                     `json:"unknown"`
	MedianWaitMS      int64                   `json:"median_wait_ms"`
	P95WaitMS         int64                   `json:"p95_wait_ms"`
	P50StartupMS      int64                   `json:"p50_startup_ms"`
	P95StartupMS      int64                   `json:"p95_startup_ms"`
	P50RegistrationMS int64                   `json:"p50_registration_ms"`
	P95RegistrationMS int64                   `json:"p95_registration_ms"`

	// Fleet is every job figure above, narrowed to the jobs this fleet has a
	// hand in. Both are carried in one payload rather than chosen by a query
	// parameter because the same numbers arrive over the event stream, which
	// is one frame for every viewer: a per-request scope would be correct
	// until the next frame overwrote it, a second or two later.
	Fleet ScopedJobStats `json:"fleet"`

	Runners RunnerStats `json:"runners"`
	Hosts   HostStats   `json:"hosts"`
	Pools   []PoolStats `json:"pools"`
}

// ScopedJobStats is the job half of Stats over one scope.
//
// GitHub reports every job in an installed repository, and on an organisation
// that also uses hosted runners most of them are somebody else's. A queue depth
// that counts those answers "why is my fleet slow?" with a number nobody here
// can act on, and a median wait computed from them is somebody else's queue.
type ScopedJobStats struct {
	QueuedJobs  int `json:"queued_jobs"`
	RunningJobs int `json:"running_jobs"`
	Completed   int `json:"completed"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	// FleetFailed is the part of Failed this fleet caused. See Stats.
	FleetFailed  int   `json:"fleet_failed"`
	Cancelled    int   `json:"cancelled"`
	Unknown      int   `json:"unknown"`
	MedianWaitMS int64 `json:"median_wait_ms"`
	P95WaitMS    int64 `json:"p95_wait_ms"`
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

	js, err := c.st.StatsSince(ctx, since, false)
	if err != nil {
		return nil, fmt.Errorf("computing job statistics: %w", err)
	}
	fleet, err := c.st.StatsSince(ctx, since, true)
	if err != nil {
		return nil, fmt.Errorf("computing this fleet's job statistics: %w", err)
	}
	out := &Stats{
		Fleet: ScopedJobStats{
			QueuedJobs:   fleet.Queued,
			RunningJobs:  fleet.Running,
			Completed:    fleet.CompletedLast,
			Succeeded:    fleet.Succeeded,
			Failed:       fleet.Failed,
			FleetFailed:  fleet.FleetFailed,
			Cancelled:    fleet.Cancelled,
			Unknown:      fleet.Unknown,
			MedianWaitMS: fleet.MedianWaitMS,
			P95WaitMS:    fleet.P95WaitMS,
		},
		Window:       window.String(),
		QueuedJobs:   js.Queued,
		RunningJobs:  js.Running,
		Completed:    js.CompletedLast,
		Succeeded:    js.Succeeded,
		Failed:       js.Failed,
		FleetFailed:  js.FleetFailed,
		Cancelled:    js.Cancelled,
		Unknown:      js.Unknown,
		MedianWaitMS: js.MedianWaitMS,
		P95WaitMS:    js.P95WaitMS,
	}

	// The two category breakdowns behind the split. A window with no fleet
	// failures in it produces no map at all, which is the shape the Overview
	// wants: nothing to draw rather than nine zeroes to draw nothing with.
	if out.FleetFailed > 0 {
		faults, err := c.st.JobFaultCountsSince(ctx, since, false)
		if err != nil {
			return nil, fmt.Errorf("counting job failures by category: %w", err)
		}
		out.Faults = faults
	}
	startFaults, err := c.st.RunnerFaultCountsSince(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("counting runner failures by category: %w", err)
	}
	if len(startFaults) > 0 {
		out.RunnerStartFaults = startFaults
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
	// The samples come from a query over the window rather than a page of
	// runner rows: the page was capped at 500, so the percentiles described
	// the newest few hundred starts and called it a day.
	startup, registration, err := c.st.StartupSamples(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("sampling runner start-up times: %w", err)
	}
	out.P50StartupMS, out.P95StartupMS = percentile(startup, .50), percentile(startup, .95)
	out.P50RegistrationMS, out.P95RegistrationMS = percentile(registration, .50), percentile(registration, .95)

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
		// is left out rather than flattering the total. A throttled host's
		// slots are counted as the throttle leaves them, for the same reason:
		// the Overview's "capacity" is what the fleet will take right now.
		// Used is counted over the same hosts: a runner still finishing on a
		// cordoned host occupies a slot capacity does not offer, and
		// counting it made "used of capacity" read as full, or over full,
		// while the healthy hosts beside it sat free. Runners.Total still
		// counts every live runner.
		if h.Healthy(now) && !h.Cordoned {
			out.Hosts.Capacity += h.EffectiveCapacity()
			out.Hosts.Used += h.ActiveRunners
		}
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

// HostSamples returns every host's minute samples since a cutoff, for the
// Hosts page's capacity map. An empty hostID means every host.
func (c *Controller) HostSamples(ctx context.Context, since time.Time, hostID string) ([]store.HostSample, error) {
	return c.st.ListHostSamples(ctx, since, hostID)
}
