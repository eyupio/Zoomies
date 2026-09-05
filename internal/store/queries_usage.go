package store

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// UsageGroup is an allowed reporting dimension.
type UsageGroup string

const (
	UsageByInstallation UsageGroup = "installation"
	UsageByRepository   UsageGroup = "repository"
	UsageByWorkflow     UsageGroup = "workflow"
	UsageByPool         UsageGroup = "pool"
)

// UsageRow is one aggregate over a bounded half-open [from,to) interval.
//
// The three job counts are deliberately additive: every job contributes to
// exactly one interval per count, so adjacent reports sum. A job present but
// neither queued, started nor completed inside the window -- one that was
// already running when the window opened and is running still -- contributes
// its execution seconds and its concurrency without being counted again.
type UsageRow struct {
	Key                 string  `json:"key"`
	JobExecutionSeconds float64 `json:"job_execution_seconds"`
	// AllocatedRunnerSeconds is nil when the grouping cannot attribute runner
	// lifetime honestly -- a runner idles on behalf of a pool, never on behalf
	// of a repository or a workflow -- which is not the same as an observed
	// zero, and is rendered as null rather than 0.
	AllocatedRunnerSeconds *float64 `json:"allocated_runner_seconds"`
	// Jobs counts jobs queued within the interval, which is what makes it
	// additive across adjacent reports.
	Jobs int `json:"jobs"`
	// JobsStarted and JobsCompleted count jobs whose start and completion fell
	// within the interval. During an incident the three diverge, and that
	// divergence is the signal.
	JobsStarted   int `json:"jobs_started"`
	JobsCompleted int `json:"jobs_completed"`
	// AverageQueueWaitSeconds is the mean wait of the JobsStarted jobs, so the
	// denominator is the population that has an observed wait. It is nil when
	// nothing started in the interval; a fleet with a hundred jobs stuck in the
	// queue reports no average rather than a flattering one.
	AverageQueueWaitSeconds *float64 `json:"average_queue_wait_seconds"`
	PeakConcurrency         int      `json:"peak_concurrency"`
	EstimatedCost           *float64 `json:"estimated_cost,omitempty"`
}

// UsageAllocationAttributable reports whether runner allocation, and therefore
// cost, can be attributed to the given grouping at all.
func UsageAllocationAttributable(group UsageGroup) bool {
	return group == UsageByPool || group == UsageByInstallation
}

// Usage aggregates jobs and allocated runner lifetime without assuming a
// cloud price. Pool costs, when configured by an administrator, are estimates.
func (s *Store) Usage(ctx context.Context, from, to time.Time, group UsageGroup) ([]UsageRow, error) {
	if !from.Before(to) {
		return nil, fmt.Errorf("usage range must have from before to")
	}
	var expr string
	switch group {
	case UsageByInstallation:
		expr = "p.installation_id"
	case UsageByRepository:
		expr = "j.repo"
	case UsageByWorkflow:
		expr = "j.workflow"
	case UsageByPool:
		expr = "j.pool_id"
	default:
		return nil, fmt.Errorf("unknown usage group %q", group)
	}
	type event struct {
		at    int64
		delta int
	}
	type acc struct {
		row       UsageRow
		wait      float64
		allocated float64
		events    []event
	}
	a := map[string]*acc{}
	lo, hi := ms(from), ms(to)
	rows, err := s.read.QueryContext(ctx, `SELECT `+expr+`, j.queued_at, j.started_at, j.completed_at
		FROM jobs j LEFT JOIN pools p ON p.id=j.pool_id
		WHERE j.queued_at < ? AND COALESCE(j.completed_at, ?) >= ?`, ms(to), ms(to), ms(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var q int64
		var st, done *int64
		if err := rows.Scan(&key, &q, &st, &done); err != nil {
			return nil, err
		}
		x := a[key]
		if x == nil {
			x = &acc{row: UsageRow{Key: key}}
			a[key] = x
		}
		// Each count asks "did this happen here?", never "was this job around?",
		// so a job queued in one window and started in the next is one queued
		// job and one started job rather than two of each.
		if q >= lo && q < hi {
			x.row.Jobs++
		}
		if st != nil && *st >= lo && *st < hi {
			x.row.JobsStarted++
			x.wait += float64(max64(0, *st-q)) / 1000
		}
		if done != nil && *done >= lo && *done < hi {
			x.row.JobsCompleted++
		}
		if st != nil && done != nil {
			from, to := max64(*st, lo), min64(*done, hi)
			if to > from {
				x.row.JobExecutionSeconds += float64(to-from) / 1000
				x.events = append(x.events, event{from, 1}, event{to, -1})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Runner allocation belongs naturally to pools/installations. Repository and
	// workflow allocation cannot be attributed while a runner is idle, so those
	// groupings report no figure at all rather than an honest-looking zero.
	attributable := UsageAllocationAttributable(group)
	if attributable {
		rExpr := "r.pool_id"
		if group == UsageByInstallation {
			rExpr = "p.installation_id"
		}
		rr, err := s.read.QueryContext(ctx, `SELECT `+rExpr+`, r.created_at, COALESCE(r.finished_at, ?), p.cost_per_runner_hour FROM runners r JOIN pools p ON p.id=r.pool_id WHERE r.created_at < ? AND COALESCE(r.finished_at, ?) > ?`, ms(to), ms(to), ms(to), ms(from))
		if err != nil {
			return nil, err
		}
		defer rr.Close()
		for rr.Next() {
			var key string
			var start, end int64
			var cost *float64
			if err := rr.Scan(&key, &start, &end, &cost); err != nil {
				return nil, err
			}
			x := a[key]
			if x == nil {
				x = &acc{row: UsageRow{Key: key}}
				a[key] = x
			}
			secs := float64(min64(end, hi)-max64(start, lo)) / 1000
			if secs < 0 {
				secs = 0
			}
			x.allocated += secs
			if cost != nil {
				if x.row.EstimatedCost == nil {
					x.row.EstimatedCost = new(float64)
				}
				*x.row.EstimatedCost += secs / 3600 * *cost
			}
		}
		if err := rr.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]UsageRow, 0, len(a))
	for _, x := range a {
		// A key can reach this map without anything to report -- a job queued
		// before the window and still queued now is present, but it is not this
		// window's job. Reporting it as a row of zeroes would only look broken.
		if x.row.Jobs == 0 && x.row.JobsStarted == 0 && x.row.JobsCompleted == 0 &&
			x.row.JobExecutionSeconds == 0 && x.allocated == 0 {
			continue
		}
		if attributable {
			allocated := x.allocated
			x.row.AllocatedRunnerSeconds = &allocated
		}
		if x.row.JobsStarted > 0 {
			mean := x.wait / float64(x.row.JobsStarted)
			x.row.AverageQueueWaitSeconds = &mean
		}
		sort.Slice(x.events, func(i, j int) bool {
			if x.events[i].at == x.events[j].at {
				return x.events[i].delta < x.events[j].delta
			}
			return x.events[i].at < x.events[j].at
		})
		cur := 0
		for _, e := range x.events {
			cur += e.delta
			if cur > x.row.PeakConcurrency {
				x.row.PeakConcurrency = cur
			}
		}
		out = append(out, x.row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// RepositoryJobCounts returns the live workload split used by repository
// concurrency policy. It deliberately includes unmatched jobs in queued.
func (s *Store) RepositoryJobCounts(ctx context.Context) (active, queued map[string]int, err error) {
	active, queued = map[string]int{}, map[string]int{}
	rows, err := s.read.QueryContext(ctx, `SELECT pool_id || char(0) || repo, state, COUNT(*) FROM jobs WHERE state IN ('queued','in_progress') GROUP BY pool_id,repo,state`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var repo, state string
		var n int
		if err = rows.Scan(&repo, &state, &n); err != nil {
			return nil, nil, err
		}
		if state == "in_progress" {
			active[repo] = n
		} else {
			queued[repo] = n
		}
	}
	return active, queued, rows.Err()
}
