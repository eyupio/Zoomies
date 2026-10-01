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
	UsageByHost         UsageGroup = "host"
)

// UsageRow is one aggregate over a bounded half-open [from,to) interval.
//
// The three job counts are deliberately additive: every job contributes to
// exactly one interval per count, so adjacent reports sum. A job present but
// neither queued, started nor completed inside the window -- one that was
// already running when the window opened and is running still -- contributes
// its execution seconds and its concurrency without being counted again.
type UsageRow struct {
	Key                 string        `json:"key"`
	History             []UsageBucket `json:"history"`
	Succeeded           int           `json:"succeeded"`
	Failed              int           `json:"failed"`
	Cancelled           int           `json:"cancelled"`
	Unknown             int           `json:"unknown"`
	JobExecutionSeconds float64       `json:"job_execution_seconds"`
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

// UsageInterval is the width of one history bucket.
type UsageInterval string

const (
	// UsageAutoInterval is the width a caller gets without asking: hourly for
	// a window of two days or less, daily beyond, which keeps a month's
	// report to thirty buckets a row and a day's to twenty-four.
	UsageAutoInterval UsageInterval = ""
	UsageHourly       UsageInterval = "hour"
	UsageDaily        UsageInterval = "day"
)

// usageIntervalHours are the widths a caller may name, in hours: an hour, a
// day, and every whole number of hours a day divides into. Not five or seven:
// a calendar cuts each day into equal squares, and a bucket that straddled
// midnight would put part of one square on the wrong day.
var usageIntervalHours = map[UsageInterval]int{
	UsageHourly: 1,
	"2h":        2,
	"3h":        3,
	"4h":        4,
	"6h":        6,
	"8h":        8,
	"12h":       12,
	UsageDaily:  24,
}

// Known reports whether the interval is a width a caller may ask for, or the
// empty one that leaves the choice to the window's length.
func (i UsageInterval) Known() bool {
	_, ok := usageIntervalHours[i]
	return ok || i == UsageAutoInterval
}

// Width is the bucket this interval cuts from a window.
func (i UsageInterval) Width(from, to time.Time) time.Duration {
	if h, ok := usageIntervalHours[i]; ok {
		return time.Duration(h) * time.Hour
	}
	if to.Sub(from) <= 48*time.Hour {
		return time.Hour
	}
	return 24 * time.Hour
}

// UsageAllocationAttributable reports whether runner allocation, and therefore
// cost, can be attributed to the given grouping at all.
func UsageAllocationAttributable(group UsageGroup) bool {
	return group == UsageByPool || group == UsageByInstallation || group == UsageByHost
}

// Usage aggregates jobs and allocated runner lifetime without assuming a
// cloud price. Pool costs, when configured by an administrator, are estimates.
//
// Only this fleet's jobs are counted. GitHub reports every job in an installed
// repository, and a job it ran on one of its own hosted runners consumed no
// runner-hour here, waited in no queue of ours and cost this fleet nothing --
// counting it would inflate every figure on the page and, grouped by
// installation, invent a row for a pool that does not exist. The predicate is
// the one the Jobs page and the Overview use, so a repository's runner-hours
// here and its job list there are about the same jobs.
func (s *Store) Usage(ctx context.Context, from, to time.Time, group UsageGroup) ([]UsageRow, error) {
	return s.UsageWithInterval(ctx, from, to, group, UsageAutoInterval)
}

// UsageWithInterval is Usage with the history cut into buckets of the given
// width rather than the width the window's length would choose. A week of
// hours is what the activity matrix draws as a punch card; the caller is
// trusted to keep the bucket count sensible, and the API does.
func (s *Store) UsageWithInterval(ctx context.Context, from, to time.Time, group UsageGroup, interval UsageInterval) ([]UsageRow, error) {
	if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		return nil, fmt.Errorf("usage range must have from before to")
	}
	var expr string
	switch group {
	case UsageByInstallation:
		// COALESCE survives the LEFT JOIN even now the hosted jobs are gone:
		// an unclaimed queued job is this fleet's to see and still has no
		// pool, and scanning NULL into a string fails the whole query.
		expr = "COALESCE(p.installation_id, '')"
	case UsageByRepository:
		expr = "j.repo"
	case UsageByWorkflow:
		expr = "j.workflow"
	case UsageByHost:
		// A job whose runner row has been pruned is still on the host its
		// session names; without the session it would move to the blank host
		// a week after it ran, and a host's month would shrink as it aged.
		expr = "COALESCE(r.host_id, rs.host_id, '')"
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
	observed := min64(hi, ms(s.Now()))
	width := int64(interval.Width(from, to) / time.Millisecond)
	bucket := func(x *acc, at int64) *UsageBucket {
		if at < lo || at >= hi {
			return nil
		}
		i := int((at - lo) / width)
		if x.row.History == nil {
			x.row.History = make([]UsageBucket, (hi-lo+width-1)/width)
			for n := range x.row.History {
				x.row.History[n].From = from.Add(time.Duration(int64(n)*width) * time.Millisecond)
			}
		}
		return &x.row.History[i]
	}
	// "Was this job around in the window" is queued before its end and not
	// finished before its start, which is a condition on two columns and so
	// served by neither index: written as one WHERE it reads every job ever kept
	// to return the week's, 800 ms of a 3 s report at four hundred thousand
	// rows. Cut by state it is three ranges the indexes serve -- finished in the
	// window, finished with no stamp (which the old condition counted as still
	// going), and not finished at all -- and returns the same rows.
	rows, err := s.read.QueryContext(ctx, `SELECT `+expr+`, j.queued_at, j.started_at, j.completed_at, j.conclusion, j.runner_fault
		FROM `+usageJobsInWindowSQL+` AS j LEFT JOIN pools p ON p.id=j.pool_id LEFT JOIN runners r ON r.id=j.runner_id
		LEFT JOIN runner_sessions rs ON rs.runner_id=j.runner_id
		WHERE `+managedJobSQL("j"), usageWindowArgs(from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var q int64
		var st, done *int64
		var conclusion, fault string
		if err := rows.Scan(&key, &q, &st, &done, &conclusion, &fault); err != nil {
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
			bucket(x, q).Queued++
		}
		if st != nil && *st >= lo && *st < hi {
			x.row.JobsStarted++
			bucket(x, *st).Started++
			x.wait += float64(max64(0, *st-q)) / 1000
		}
		if done != nil && *done >= lo && *done < hi {
			x.row.JobsCompleted++
			b := bucket(x, *done)
			switch {
			case fault != "" || IsFailedConclusion(conclusion):
				x.row.Failed++
				b.Failed++
			case conclusion == "success":
				x.row.Succeeded++
				b.Succeeded++
			case conclusion == "cancelled" || conclusion == "skipped":
				x.row.Cancelled++
				b.Cancelled++
			default:
				x.row.Unknown++
				b.Unknown++
			}
		}
		if st != nil {
			end := observed
			if done != nil {
				end = min64(*done, observed)
			}
			from, to := max64(*st, lo), end
			if to > from {
				x.row.JobExecutionSeconds += float64(to-from) / 1000
				x.events = append(x.events, event{from, 1}, event{to, -1})
				for at := from; at < to; {
					next := min64(to, lo+((at-lo)/width+1)*width)
					bucket(x, at).ExecutionSeconds += float64(next-at) / 1000
					at = next
				}
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
		err := s.usageAllocation(ctx, group, lo, hi, observed, width, func(key string, at int64, secs float64, cost *float64) {
			x := a[key]
			if x == nil {
				x = &acc{row: UsageRow{Key: key}}
				a[key] = x
			}
			x.allocated += secs
			bucket(x, at).AllocatedSeconds += secs
			if cost != nil {
				if x.row.EstimatedCost == nil {
					x.row.EstimatedCost = new(float64)
				}
				*x.row.EstimatedCost += *cost
			}
		})
		if err != nil {
			return nil, err
		}
	}
	// Capacity observations belong to pools, not repositories that share them.
	if group == UsageByPool {
		cr, err := s.read.QueryContext(ctx, `SELECT pool_id, ? + ((at-?)/?)*?, COUNT(*), SUM(blocked) FROM usage_capacity_samples WHERE at >= ? AND at < ? GROUP BY pool_id, (at-?)/?`, lo, lo, width, width, lo, hi, lo, width)
		if err != nil {
			return nil, err
		}
		for cr.Next() {
			var key string
			var at int64
			var blocked, samples int
			if err := cr.Scan(&key, &at, &samples, &blocked); err != nil {
				cr.Close()
				return nil, err
			}
			x := a[key]
			if x == nil {
				x = &acc{row: UsageRow{Key: key}}
				a[key] = x
			}
			b := bucket(x, at)
			b.CapacitySamples += samples
			b.CapacityReached += blocked
		}
		err = cr.Err()
		cr.Close()
		if err != nil {
			return nil, err
		}
	}
	out := make([]UsageRow, 0, len(a))
	for _, x := range a {
		// A key can reach this map without anything to report -- a job queued
		// before the window and still queued now is present, but it is not this
		// window's job. Reporting it as a row of zeroes would only look broken.
		if x.row.Jobs == 0 && x.row.JobsStarted == 0 && x.row.JobsCompleted == 0 &&
			x.row.JobExecutionSeconds == 0 && x.allocated == 0 && len(x.row.History) == 0 {
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

// usageJobsInWindowSQL is the jobs that were around in a window, as a
// subquery; usageWindowArgs are its six placeholders. See UsageWithInterval for
// why it is three ranges and not one condition.
const usageJobsInWindowSQL = `(
			SELECT ` + usageJobCols + ` FROM jobs WHERE state = 'completed' AND completed_at >= ? AND queued_at < ?
			UNION ALL
			SELECT ` + usageJobCols + ` FROM jobs WHERE state = 'completed' AND completed_at IS NULL AND queued_at < ?
			UNION ALL
			SELECT ` + usageJobCols + ` FROM jobs WHERE state IN ('` + string(JobWaiting) + `','` + string(JobQueued) + `','` + string(JobInProgress) + `')
				AND queued_at < ? AND COALESCE(completed_at, ?) >= ?
		)`

func usageWindowArgs(from, to time.Time) []any {
	return []any{ms(from), ms(to), ms(to), ms(to), ms(to), ms(from)}
}

// usageJobCols is what the usage report reads of a job, and what the label and
// ownership predicates it is filtered by need.
const usageJobCols = `id, pool_id, runner_id, repo, workflow, matched, state, labels, queued_at, started_at, completed_at, conclusion, runner_fault`

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
