package kennel

import "time"

const (
	// UnservedWait is how long a job must have waited for a label no pool
	// serves before it counts. A job that is simply waiting for its pool to
	// scale up is not an unserved one, and the existing jobs.unmatched problem
	// already gives that case its own short grace.
	UnservedWait = 10 * time.Minute

	// DefaultJobLimit is how long GitHub lets a job run when it sets no
	// timeout-minutes.
	DefaultJobLimit = 360 * time.Minute

	// A job that GitHub stops at its limit is cancelled a moment after the
	// limit is reached, counted from when the job started. These bound "a
	// moment": a minute early for clock skew, two late for the cancellation.
	limitEarly = 1 * time.Minute
	limitLate  = 2 * time.Minute

	// LongJobFloor is the shortest job the collector hands over in
	// JobFacts.Long. It is well below the limit, so the evaluator, not the
	// collector, decides which jobs hit it.
	LongJobFloor = 5 * time.Hour

	// MatrixWait is how long a job of a matrix must have waited for a runner
	// before the matrix counts as held up by its pool. A pool that is merely
	// busy for a moment is not a pool that is too small.
	MatrixWait = 2 * time.Minute
)

func evalUnservedLabel(s *Snapshot) result {
	var n int
	var longest time.Duration
	for _, u := range s.Fleet.Jobs.Unserved {
		if u.Waited >= UnservedWait {
			n++
			longest = max(longest, u.Waited)
		}
	}
	if n == 0 {
		return result{applies: true}
	}
	return result{applies: true, findings: []Finding{{
		Code:     CodeUnservedLabel,
		Severity: SeverityWarning,
		Title:    "Jobs waited for a label no pool serves",
		Detail: count(n, "job", "jobs") + " waited more than " + span(UnservedWait) + " in the last 7 days for runners no pool here provides, " +
			"the longest for " + span(longest) + ". Such a job stays queued until a pool matches it or somebody cancels it.",
		Fix: "Add a pool that offers the label, or change the workflow's runs-on to a label a pool serves. " +
			"The Jobs page, filtered to unmatched jobs, lists each one.",
	}}}
}

func evalJobHitDefaultLimit(s *Snapshot) result {
	var n int
	for _, j := range s.Fleet.Jobs.Long {
		if j.Conclusion == "cancelled" &&
			j.Duration >= DefaultJobLimit-limitEarly && j.Duration <= DefaultJobLimit+limitLate {
			n++
		}
	}
	if n == 0 {
		return result{applies: true}
	}
	return result{applies: true, findings: []Finding{{
		Code:     CodeJobHitDefaultLimit,
		Severity: SeverityWarning,
		Title:    "A job ran until GitHub stopped it at six hours",
		Detail: "GitHub cancels a job that sets no timeout-minutes after 360 minutes, and the job holds its runner for all of that time. " +
			count(n, "job", "jobs") + " did this in the last " + forDays(s.Fleet.Window) + ".",
		Fix: "Set timeout-minutes on the job, a little above its longest healthy run, so a hung job frees its runner sooner.",
	}}}
}

func evalMatrixExceedsPool(s *Snapshot) result {
	type held struct {
		matrices, widest int
		waited           time.Duration
	}
	byPool := map[string]*held{}
	for _, m := range s.Fleet.Jobs.Matrices {
		if m.Waited < MatrixWait {
			continue
		}
		for _, p := range s.Fleet.Pools {
			if p.ID != m.PoolID || p.MaxRunners <= 0 || p.MaxRunners >= m.Jobs {
				continue
			}
			h := byPool[p.ID]
			if h == nil {
				h = &held{}
				byPool[p.ID] = h
			}
			h.matrices++
			h.widest = max(h.widest, m.Jobs)
			h.waited = max(h.waited, m.Waited)
		}
	}
	var findings []Finding
	for _, p := range s.Fleet.Pools {
		h := byPool[p.ID]
		if h == nil {
			continue
		}
		findings = append(findings, Finding{
			Code:     CodeMatrixExceedsPool,
			Severity: SeverityInfo,
			Subject:  p.ID,
			Title:    "A matrix is wider than the pool that runs it",
			Detail: count(h.matrices, "matrix", "matrices") + " of up to " + count(h.widest, "job", "jobs") + " ran on a pool of " + count(p.MaxRunners, "runner", "runners") +
				" in the last " + forDays(s.Fleet.Window) + ", and jobs of it waited up to " + span(h.waited) + " for a runner. A matrix wider than its pool runs in waves.",
			Fix: "Raise the pool's max_runners to the matrix's width, spread the matrix over more than one pool with runs-on, " +
				"or cap it with max-parallel so the wait is chosen and not suffered.",
			Evidence: []Evidence{poolEvidence(p.ID, p.Name)},
		})
	}
	return result{applies: true, findings: findings}
}
