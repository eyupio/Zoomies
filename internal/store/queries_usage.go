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
type UsageRow struct {
	Key                     string   `json:"key"`
	JobExecutionSeconds     float64  `json:"job_execution_seconds"`
	AllocatedRunnerSeconds  float64  `json:"allocated_runner_seconds"`
	Jobs                    int      `json:"jobs"`
	AverageQueueWaitSeconds float64  `json:"average_queue_wait_seconds"`
	PeakConcurrency         int      `json:"peak_concurrency"`
	EstimatedCost           *float64 `json:"estimated_cost,omitempty"`
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
		row    UsageRow
		wait   float64
		events []event
	}
	a := map[string]*acc{}
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
		x.row.Jobs++
		if st != nil {
			x.wait += float64(max64(0, *st-q)) / 1000
		}
		if st != nil && done != nil {
			lo, hi := max64(*st, ms(from)), min64(*done, ms(to))
			if hi > lo {
				x.row.JobExecutionSeconds += float64(hi-lo) / 1000
				x.events = append(x.events, event{lo, 1}, event{hi, -1})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Runner allocation belongs naturally to pools/installations. Repository and
	// workflow allocation cannot be attributed while a runner is idle.
	if group == UsageByPool || group == UsageByInstallation {
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
			secs := float64(min64(end, ms(to))-max64(start, ms(from))) / 1000
			if secs < 0 {
				secs = 0
			}
			x.row.AllocatedRunnerSeconds += secs
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
		if x.row.Jobs > 0 {
			x.row.AverageQueueWaitSeconds = x.wait / float64(x.row.Jobs)
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
