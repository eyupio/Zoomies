package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The keys JobStats can group by. Each is also the name of the filter that
// takes the same value back, so a row of the answer can be opened as a listing.
const (
	GroupByControllerVersion = "controller_version"
	GroupByDay               = "day"
	GroupByHost              = "host"
	GroupByPool              = "pool"
	GroupByJobName           = "job_name"
)

// JobStatsGroupKeys lists the accepted group_by values, in the order the
// documentation names them.
var JobStatsGroupKeys = []string{GroupByControllerVersion, GroupByDay, GroupByHost, GroupByPool, GroupByJobName}

// MaxJobStatsGroups caps the groups one answer carries. Grouping by job name
// across a busy fleet can name thousands; an answer that large is not one a
// person or a model reads, so the rest are dropped and the answer says so.
const MaxJobStatsGroups = 500

// groupExpr is the SQL that names a job's group for one key. The four that can
// be absent say "unknown" rather than leaving a NULL group, which most
// clients would drop or render as nothing.
var groupExpr = map[string]string{
	GroupByControllerVersion: `COALESCE(controller_version, '` + UnknownVersion + `')`,
	GroupByDay:               `strftime('%Y-%m-%d', queued_at / 1000, 'unixepoch')`,
	GroupByHost:              `COALESCE(host_id, '` + UnknownVersion + `')`,
	GroupByPool:              `COALESCE(NULLIF(pool_id, ''), '` + UnknownVersion + `')`,
	GroupByJobName:           `job_name`,
}

// JobPercentiles is one measurement's spread over a group. Samples is how many
// jobs it was computed from, and the two figures are nil when that is none:
// zero milliseconds is an answer, and "nothing to measure" is not.
type JobPercentiles struct {
	Samples int    `json:"samples"`
	P50MS   *int64 `json:"p50_ms"`
	P95MS   *int64 `json:"p95_ms"`
}

// JobStatsGroup is the completed jobs sharing one value of each group_by key.
type JobStatsGroup struct {
	// Keys maps each group_by key to this group's value. Empty when the
	// request grouped by nothing and the answer is a single total.
	Keys map[string]string `json:"keys"`
	// Count is every completed job in the group; the next four split it.
	// Failed includes fleet_failed, as it does on the Overview: a job the
	// fleet lost is a failed job whoever's fault it was.
	Count       int `json:"count"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Cancelled   int `json:"cancelled"`
	FleetFailed int `json:"fleet_failed"`
	// FleetFailedByKind splits FleetFailed by the fault category the runner
	// reported. A failure recorded before categories existed is
	// "unclassified".
	FleetFailedByKind map[string]int `json:"fleet_failed_by_kind"`
	// FleetFailureRate is FleetFailed over Count, so releases with different
	// volumes can be compared on one column.
	FleetFailureRate float64        `json:"fleet_failure_rate"`
	Duration         JobPercentiles `json:"duration"`
	QueueWait        JobPercentiles `json:"queue_wait"`
	Startup          JobPercentiles `json:"startup"`
}

// JobStatsResult is JobStats' answer.
type JobStatsResult struct {
	Groups []JobStatsGroup
	// Truncated is true when more than MaxJobStatsGroups groups matched and
	// the rest were left out.
	Truncated bool
}

// JobStats aggregates the completed jobs f selects, grouped by up to two keys.
//
// Everything is computed in SQL, on the window the caller bounded: the counts
// with GROUP BY, and each percentile with a window function that numbers a
// group's samples in order and keeps the ones at the p50 and p95 positions.
// SQLite has no percentile aggregate, and reading every sample into Go to
// sort it would make the cost of a request the size of the window rather than
// of the answer.
//
// The position is the one the Overview's percentiles use -- index
// round((n-1)*q) into the sorted samples -- so a figure here and the same one
// on the Overview cannot differ by a rounding rule.
//
// Duration leaves out cancelled and skipped jobs, which stop when somebody
// says so rather than when the work is done, and would drag a release's median
// towards however impatient its users were. Queue wait keeps them: a job
// cancelled after it started still waited that long. Startup is the runner's
// container start less its creation, so it exists only for jobs whose runner
// row is still kept (retention.runners) and is measured from the same two
// stamps as the Overview's startup figures.
func (s *Store) JobStats(ctx context.Context, f JobFilter, groupBy []string) (*JobStatsResult, error) {
	if len(groupBy) > 2 {
		return nil, fmt.Errorf("store: at most two group_by keys, got %d", len(groupBy))
	}
	keys := [2]string{`''`, `''`}
	for i, g := range groupBy {
		e, ok := groupExpr[g]
		if !ok {
			return nil, fmt.Errorf("store: %q is not a job statistics key", g)
		}
		keys[i] = e
	}

	where, args := jobWhere(f)
	scope := `state = 'completed'`
	if where != "" {
		scope += ` AND ` + strings.TrimPrefix(where, "WHERE ")
	}
	base := `WITH base AS (SELECT ` + keys[0] + ` AS k1, ` + keys[1] + ` AS k2, queued_at AS q,
			conclusion, runner_fault, fault_kind,
			CASE WHEN started_at IS NOT NULL THEN MAX(started_at - queued_at, 0) END AS wait,
			CASE WHEN started_at IS NOT NULL AND completed_at IS NOT NULL AND conclusion NOT IN ('cancelled','skipped')
				THEN MAX(completed_at - started_at, 0) END AS dur,
			(SELECT r.container_started_at - r.created_at FROM runners r
				WHERE r.id = jobs.runner_id AND r.container_started_at >= r.created_at) AS startup
		FROM jobs WHERE ` + scope + `) `

	type gk struct{ a, b string }
	groups := map[gk]*JobStatsGroup{}
	first := map[gk]int64{}
	keyMap := func(k gk) map[string]string {
		m := map[string]string{}
		if len(groupBy) > 0 {
			m[groupBy[0]] = k.a
		}
		if len(groupBy) > 1 {
			m[groupBy[1]] = k.b
		}
		return m
	}

	rows, err := s.read.QueryContext(ctx, base+`SELECT k1, k2, COUNT(*), MIN(q),
		SUM(CASE WHEN conclusion = 'success' AND runner_fault = '' THEN 1 ELSE 0 END),
		SUM(CASE WHEN `+failedJobSQL()+` THEN 1 ELSE 0 END),
		SUM(CASE WHEN conclusion IN ('cancelled','skipped') AND runner_fault = '' THEN 1 ELSE 0 END),
		SUM(CASE WHEN `+fleetFailedJobSQL()+` THEN 1 ELSE 0 END)
		FROM base GROUP BY k1, k2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k gk
		var g JobStatsGroup
		var firstQueued int64
		if err := rows.Scan(&k.a, &k.b, &g.Count, &firstQueued, &g.Succeeded, &g.Failed, &g.Cancelled, &g.FleetFailed); err != nil {
			return nil, err
		}
		g.Keys, g.FleetFailedByKind = keyMap(k), map[string]int{}
		if g.Count > 0 {
			g.FleetFailureRate = float64(g.FleetFailed) / float64(g.Count)
		}
		groups[k], first[k] = &g, firstQueued
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	kinds, err := s.read.QueryContext(ctx, base+`SELECT k1, k2, COALESCE(NULLIF(fault_kind, ''), 'unclassified'), COUNT(*)
		FROM base WHERE `+fleetFailedJobSQL()+` GROUP BY 1, 2, 3`, args...)
	if err != nil {
		return nil, err
	}
	defer kinds.Close()
	for kinds.Next() {
		var k gk
		var kind string
		var n int
		if err := kinds.Scan(&k.a, &k.b, &kind, &n); err != nil {
			return nil, err
		}
		if g := groups[k]; g != nil {
			g.FleetFailedByKind[kind] = n
		}
	}
	if err := kinds.Err(); err != nil {
		return nil, err
	}

	for _, m := range []struct {
		col  string
		into func(*JobStatsGroup) *JobPercentiles
	}{
		{"dur", func(g *JobStatsGroup) *JobPercentiles { return &g.Duration }},
		{"wait", func(g *JobStatsGroup) *JobPercentiles { return &g.QueueWait }},
		{"startup", func(g *JobStatsGroup) *JobPercentiles { return &g.Startup }},
	} {
		// col is one of the three literals above, never caller input.
		pr, err := s.read.QueryContext(ctx, base+`SELECT k1, k2, MAX(n),
			MAX(CASE WHEN rn = CAST((n - 1) * 0.5 + 0.5 AS INTEGER) + 1 THEN v END),
			MAX(CASE WHEN rn = CAST((n - 1) * 0.95 + 0.5 AS INTEGER) + 1 THEN v END)
			FROM (SELECT k1, k2, `+m.col+` AS v,
					ROW_NUMBER() OVER (PARTITION BY k1, k2 ORDER BY `+m.col+`) AS rn,
					COUNT(*) OVER (PARTITION BY k1, k2) AS n
				FROM base WHERE `+m.col+` IS NOT NULL)
			GROUP BY k1, k2`, args...)
		if err != nil {
			return nil, err
		}
		for pr.Next() {
			var k gk
			var n int
			var p50, p95 int64
			if err := pr.Scan(&k.a, &k.b, &n, &p50, &p95); err != nil {
				pr.Close()
				return nil, err
			}
			if g := groups[k]; g != nil {
				*m.into(g) = JobPercentiles{Samples: n, P50MS: &p50, P95MS: &p95}
			}
		}
		err = pr.Err()
		pr.Close()
		if err != nil {
			return nil, err
		}
	}

	keyed := make([]gk, 0, len(groups))
	for k := range groups {
		keyed = append(keyed, k)
	}
	// Releases read in the order they were first seen running, because "v1.10"
	// sorts before "v1.9"; every other key reads in name order.
	chronological := len(groupBy) > 0 && groupBy[0] == GroupByControllerVersion
	sort.Slice(keyed, func(i, j int) bool {
		a, b := keyed[i], keyed[j]
		if chronological && first[a] != first[b] {
			return first[a] < first[b]
		}
		if a.a != b.a {
			return a.a < b.a
		}
		return a.b < b.b
	})
	out := &JobStatsResult{}
	for _, k := range keyed {
		if len(out.Groups) == MaxJobStatsGroups {
			out.Truncated = true
			break
		}
		out.Groups = append(out.Groups, *groups[k])
	}
	return out, nil
}
