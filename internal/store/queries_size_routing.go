package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Pools the controller keeps
// ---------------------------------------------------------------------------

// ApplyAutoPool writes what the controller worked out for a pool it keeps --
// how many runners to keep warm, how many it may have, whether it is in use, and
// the fixed settings a pool of its class is made with -- and says whether it
// wrote anything. from is the pool as the pass read it and to is the pool as it
// should be.
//
// It is a statement of its own rather than an UpdatePool for three reasons. The
// first is the guard: it touches only a pool with an auto_key, so a bug in the
// reconciler cannot rewrite a pool an operator made, however it came to hold
// the wrong ID. The second is that an operator's edit of the same pool reads the
// row, changes a few fields and writes it back whole, and the reconciler writes
// every minute: a whole-row write from here would put back an idle timeout, a
// warm count or a pause the operator saved while the pass was working, so this
// names only the columns the controller owns. The third is that what it writes
// was worked out from the operator's warm count, cap and pause as the pass read
// them. If one of those has moved since, the write is refused -- false and no
// error -- because a maximum worked out under a cap that is no longer there is
// worse than none, and the next pass starts again from the pool as it now is.
//
// It also writes nothing, and moves no updated_at, for a pool that already is
// what it should be, so that a pass which found nothing to do leaves nothing
// behind it.
func (s *Store) ApplyAutoPool(ctx context.Context, from, to *Pool) (bool, error) {
	resources, err := marshalJSON(to.Resources)
	if err != nil {
		return false, err
	}
	platform := to.Platform.Normalized()
	r, err := s.exec(ctx, `UPDATE pools SET min_runners=?1, max_runners=?2, enabled=?3, labels=?4, os=?5, os_version=?6,
			arch=?7, size_from_profile=?8, docker_mode=?9, ephemeral=?10, resources=?11, host_selector=?12, updated_at=?13
		WHERE id=?14 AND auto_key != '' AND auto_min=?15 AND auto_cap=?16 AND auto_paused=?17
			AND (min_runners != ?1 OR max_runners != ?2 OR enabled != ?3 OR labels != ?4 OR os != ?5 OR os_version != ?6
				OR arch != ?7 OR size_from_profile != ?8 OR docker_mode != ?9 OR ephemeral != ?10 OR resources != ?11
				OR host_selector != ?12)`,
		to.MinRunners, to.MaxRunners, boolInt(to.Enabled), StringSlice(NormalizeLabels(to.Labels)),
		platform.OS, platform.OSVersion, platform.Arch,
		boolInt(to.SizeFromProfile), string(to.DockerMode), boolInt(to.Ephemeral), resources, to.HostSelector,
		ms(s.Now()), from.ID, from.AutoMin, from.AutoCap, boolInt(from.AutoPaused))
	if err != nil {
		return false, wrapWrite(err)
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// ---------------------------------------------------------------------------
// What a job is classed as
// ---------------------------------------------------------------------------

// JobClass is the class a job is in now, kept between runs. It is state
// rather than something worked out again on every read because moving a job
// down has to be slower than moving it up: see migration 0072.
type JobClass struct {
	Repo     string    `json:"repo"`
	Workflow string    `json:"workflow"`
	JobName  string    `json:"job_name"`
	Class    SizeClass `json:"class"`
	Reason   string    `json:"reason"`
	Basis    string    `json:"basis"`
	// FloorMB is the memory the runs behind the class showed the job needing,
	// and zero when none did.
	FloorMB    int64     `json:"floor_mb,omitempty"`
	Runs       int       `json:"runs"`
	ComputedAt time.Time `json:"computed_at"`
	MovedAt    time.Time `json:"moved_at"`
}

// GetJobClass returns the class kept for one job, or ErrNotFound for a job
// that has never been classed from its history.
func (s *Store) GetJobClass(ctx context.Context, repo, workflow, job string) (*JobClass, error) {
	var c JobClass
	var computed, moved int64
	err := s.read.QueryRowContext(ctx, `SELECT repo, workflow, job_name, class, reason, basis, floor_mb, runs, computed_at, moved_at
		FROM job_classes WHERE repo=? AND workflow=? AND job_name=?`, repo, workflow, job).
		Scan(&c.Repo, &c.Workflow, &c.JobName, &c.Class, &c.Reason, &c.Basis, &c.FloorMB, &c.Runs, &computed, &moved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("job class %s/%s/%s: %w", repo, workflow, job, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	c.ComputedAt, c.MovedAt = at(computed), at(moved)
	return &c, nil
}

// PutJobClass stores the class worked out for a job and returns the class it
// replaced, "" for a job that had none. MovedAt is set when the class changed,
// or the row is new, and is otherwise left where it was, so that it answers
// "since when has this job been in this class" rather than "when did we last
// look".
func (s *Store) PutJobClass(ctx context.Context, c *JobClass) (SizeClass, error) {
	if !c.Class.Valid() {
		return "", fmt.Errorf("%q is not a size class", c.Class)
	}
	var previous SizeClass
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var moved int64
		err := tx.QueryRowContext(ctx, `SELECT class, moved_at FROM job_classes WHERE repo=? AND workflow=? AND job_name=?`,
			c.Repo, c.Workflow, c.JobName).Scan(&previous, &moved)
		known := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		c.ComputedAt = s.Now()
		c.MovedAt = c.ComputedAt
		if known && previous == c.Class {
			c.MovedAt = at(moved)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO job_classes (repo, workflow, job_name, class, reason, basis, floor_mb, runs, computed_at, moved_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(repo, workflow, job_name) DO UPDATE SET class=excluded.class, reason=excluded.reason,
				basis=excluded.basis, floor_mb=excluded.floor_mb, runs=excluded.runs, computed_at=excluded.computed_at,
				moved_at=excluded.moved_at`,
			c.Repo, c.Workflow, c.JobName, string(c.Class), c.Reason, c.Basis, max(c.FloorMB, 0), c.Runs, ms(c.ComputedAt), ms(c.MovedAt))
		return err
	})
	return previous, err
}

// JobRun is one finished, measured run of a job, as the classifier reads it.
type JobRun struct {
	CompletedAt time.Time
	// Duration is how long the job ran, from the moment GitHub said it started
	// to the moment it said it finished. Zero when the start was never seen.
	Duration time.Duration
	// PeakCPUs and PeakMemoryMB are the most the runner was measured using,
	// and GrantedCPUs and GrantedMemoryMB the size of the runner it had. A peak
	// is capped by the runner's limit, so the pair is what says whether a job
	// used a runner's whole size or only some of it.
	PeakCPUs        float64
	PeakMemoryMB    int64
	GrantedCPUs     float64
	GrantedMemoryMB int64
	OOMKilled       bool
	// CPUPeriods and CPUThrottledPeriods are the last sample of the runner's
	// CPU enforcement counters; see Job.Throttled.
	CPUPeriods          int64
	CPUThrottledPeriods int64
	// RanClass is the class of the host that ran it, "" when none was recorded.
	RanClass SizeClass
}

// Throttled is the share of its CPU periods the run was held back in, and
// false when the counters were never sampled.
func (r JobRun) Throttled() (float64, bool) {
	if r.CPUPeriods <= 0 {
		return 0, false
	}
	return min(1, float64(r.CPUThrottledPeriods)/float64(r.CPUPeriods)), true
}

// JobClassHistoryLimit is the most runs of one job the controller reads to class
// it. Twenty of them say what the job needs; the rest say how long it has been
// fitting a smaller class, which for a job that finishes every few minutes is a
// week of runs and not twenty. A job that runs more than about three hundred
// times a day shows less than a week inside this many, and keeps its class until
// it is pinned or its runs grow.
const JobClassHistoryLimit = 2000

// jobClassHistorySQL is a const so a query-plan test can pin the statement that
// ships. It carries the same predicate as the usage index it is served from,
// because a partial index is only used for a query that says what the index
// says: leave one term out and the planner walks the table instead, for every
// job that finishes.
const jobClassHistorySQL = `SELECT completed_at, started_at, peak_cpus, peak_memory_mb, granted_cpus,
	granted_memory_mb, oom_killed, cpu_periods, cpu_throttled_periods, ran_class FROM jobs
	WHERE repo = ? AND workflow = ? AND job_name = ? AND state = 'completed'
	AND (peak_memory_mb > 0 OR peak_cpus > 0 OR oom_killed = 1)
	AND completed_at >= ?
	ORDER BY completed_at DESC LIMIT ?`

// JobClassHistory returns the most recent measured runs of one job that
// finished since the given time, newest first, at most limit of them.
//
// Unlike JobUsageHistory it is not split by pool: a job's class is about what
// it needs, not about where it last ran, and a job that moved between pools
// after an operator rearranged them has the same needs it had.
func (s *Store) JobClassHistory(ctx context.Context, repo, workflow, job string, since time.Time, limit int) ([]JobRun, error) {
	rows, err := s.read.QueryContext(ctx, jobClassHistorySQL, repo, workflow, job, ms(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRun
	for rows.Next() {
		var r JobRun
		var completed int64
		var started sql.NullInt64
		var oom int
		if err := rows.Scan(&completed, &started, &r.PeakCPUs, &r.PeakMemoryMB, &r.GrantedCPUs,
			&r.GrantedMemoryMB, &oom, &r.CPUPeriods, &r.CPUThrottledPeriods, &r.RanClass); err != nil {
			return nil, err
		}
		r.CompletedAt = at(completed)
		if started.Valid && completed > started.Int64 {
			r.Duration = time.Duration(completed-started.Int64) * time.Millisecond
		}
		r.OOMKilled = oom == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// JobClassAsked is a job's kept class beside what the job asked for: the
// runs-on of its latest measured run. It is what label advice compares the class
// the runs call for to, and the one place the two sit side by side.
type JobClassAsked struct {
	JobClass
	Labels StringSlice `json:"labels"`
}

// jobClassesAskedSQL is a const so a query-plan test can pin the statement that
// ships. The labels come from the latest measured run of each job, through the
// same partial index the history is read from, so the subquery is a seek for
// every kept class and not a walk of a jobs table that grows without bound.
const jobClassesAskedSQL = `SELECT c.repo, c.workflow, c.job_name, c.class, c.reason, c.basis, c.floor_mb, c.runs,
	c.computed_at, c.moved_at,
	COALESCE((SELECT j.labels FROM jobs j
		WHERE j.repo = c.repo AND j.workflow = c.workflow AND j.job_name = c.job_name
		AND j.state = 'completed' AND (j.peak_memory_mb > 0 OR j.peak_cpus > 0 OR j.oom_killed = 1)
		ORDER BY j.completed_at DESC LIMIT 1), '[]')
	FROM job_classes c ORDER BY c.repo, c.workflow, c.job_name`

// ListJobClassesAsked returns every class kept for a job with the labels its
// latest measured run asked for, by repository, workflow and job.
func (s *Store) ListJobClassesAsked(ctx context.Context) ([]*JobClassAsked, error) {
	rows, err := s.read.QueryContext(ctx, jobClassesAskedSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobClassAsked
	for rows.Next() {
		var c JobClassAsked
		var computed, moved int64
		if err := rows.Scan(&c.Repo, &c.Workflow, &c.JobName, &c.Class, &c.Reason, &c.Basis, &c.FloorMB, &c.Runs,
			&computed, &moved, &c.Labels); err != nil {
			return nil, err
		}
		c.ComputedAt, c.MovedAt = at(computed), at(moved)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// PruneJobClasses deletes the classes kept for jobs that have not finished a
// measured run since before. The runs a class was worked out from are pruned on
// the same window, so what goes has nothing left behind it to be worked out
// again, and a job that comes back after that long starts, as a new one does, in
// the default class. Without it every workflow job a repository ever had keeps a
// row, and a row is read by every pass that works out label advice.
func (s *Store) PruneJobClasses(ctx context.Context, before time.Time) (int64, error) {
	return pruneInBatches(ctx, pruneRowBatch, pruneMaxBatches, func(ctx context.Context) (int64, error) {
		res, err := s.exec(ctx, `DELETE FROM job_classes WHERE (repo, workflow, job_name) IN
			(SELECT repo, workflow, job_name FROM job_classes WHERE computed_at < ? LIMIT ?)`, ms(before), pruneRowBatch)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// JobClassing is what the controller decided about how big a job's host should
// be: the class, the sentence that says why, how it got there (the SizeBasis*
// constants), and the memory the job is known to need, 0 when it is not.
type JobClassing struct {
	Class   SizeClass
	Reason  string
	Basis   string
	FloorMB int64
	// Route sends the job to its class, which is what makes the scheduler claim
	// it for a pool of that class. It is false while size routing is only being
	// watched: the class is recorded and the job goes where it always did.
	Route bool
}

// Valid reports whether there is something here worth recording.
func (c JobClassing) Valid() bool { return c.Class.Valid() && c.Basis != "" }

// routed is the class the job is routed to, which is none unless asked.
func (c JobClassing) routed() string {
	if c.Route {
		return string(c.Class)
	}
	return ""
}

// StampJobClass records the class a job was put in, why, and how that was
// decided, and returns the job as it now is.
//
// It is written once: the statement only fills a job whose size_basis is still
// empty, so a webhook and the poller that both see the same queued job leave
// the first one's decision, and a job the history moved on from since cannot
// have the class it was queued under rewritten. It fills only a job that is
// still waiting for a runner, like every other write here that is about where a
// job goes: a `queued` delivery that arrives late, after the job has run, would
// otherwise stamp a finished job with a class it was never queued under, and the
// record and the count of jobs classed would both say it was. A job that is
// routed starts in its own class; SetJobRouted moves it later.
//
// The boolean says whether this call was the one that wrote the class, so that a
// count of jobs classed is not raised twice for a job two deliveries both saw.
func (s *Store) StampJobClass(ctx context.Context, jobID string, c JobClassing) (*Job, bool, error) {
	var out *Job
	var wrote bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if c.Valid() {
			res, err := tx.ExecContext(ctx, `UPDATE jobs SET size_class=?1, size_reason=?2, size_basis=?3, size_floor_mb=?4,
				routed_class=?5, routed_note='' WHERE id=?6 AND size_basis='' AND state IN ('queued', 'waiting')`,
				string(c.Class), c.Reason, c.Basis, max(c.FloorMB, 0), c.routed(), jobID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			wrote = n > 0
		}
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, jobID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job %s: %w", jobID, ErrNotFound)
		}
		out = j
		return err
	})
	return out, wrote && err == nil, err
}

// SetQueuedJobClass replaces the class of a job that is still waiting, and
// reports whether it did. It is for the one thing that legitimately changes a
// queued job's class after it was stamped: an operator's pin, which is meant to
// reach the jobs already in the queue and not only the ones that arrive next. A
// job GitHub is holding for a deployment review is waiting too, and is stamped
// when it arrives, so it is reached as well: a pin made while it waits for an
// approver is what it should be queued by when the approval comes. A job that
// has started is left as it was -- what it was taken to need is part of what
// happened to it.
//
// A job whose class and the sentence for it have not changed is left alone, with
// the route it has. A job sent on to another class because its own had no room
// is routed there by a decision of its own, and a pin for another job in the same
// repository, which changes nothing about this one, must not send it back to wait
// where it was moved from. The route is reset only when the class is, or when
// routing has been switched on or off since the job was stamped.
func (s *Store) SetQueuedJobClass(ctx context.Context, jobID string, c JobClassing) (bool, error) {
	if !c.Valid() {
		return false, fmt.Errorf("a job's class needs a class and a basis, got %q and %q", c.Class, c.Basis)
	}
	res, err := s.exec(ctx, `UPDATE jobs SET size_class=?1, size_reason=?2, size_basis=?3, size_floor_mb=?4,
		routed_class=?5, routed_note=''
		WHERE id=?6 AND state IN ('queued', 'waiting') AND (size_class != ?1 OR size_reason != ?2 OR size_basis != ?3
			OR size_floor_mb != ?4 OR (?5 = '' AND routed_class != '') OR (?5 != '' AND routed_class = ''))`,
		string(c.Class), c.Reason, c.Basis, max(c.FloorMB, 0), c.routed(), jobID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetJobRouted moves a queued job from the class it was routed to, which the
// caller read, to another, with the sentence that says why, and reports whether
// it did. It only moves a job still in the queue: a job that has been taken has
// been routed. And it only moves a job that is still where the decision found it:
// a pass that decided to send a job on and an operator who pinned it in the
// meantime are two decisions about one job, and the older of them must not
// overwrite the newer.
func (s *Store) SetJobRouted(ctx context.Context, jobID string, from, class SizeClass, note string) (bool, error) {
	if !class.Valid() {
		return false, fmt.Errorf("%q is not a size class", class)
	}
	res, err := s.exec(ctx, `UPDATE jobs SET routed_class=?1, routed_note=?2
		WHERE id=?3 AND state='queued' AND size_basis != '' AND routed_class = ?4 AND (routed_class != ?1 OR routed_note != ?2)`,
		string(class), note, jobID, string(from))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetJobClaim points a job that is still waiting at the pool that now claims it,
// "" for none, and reports whether the row changed.
//
// It is not what places the job: every scheduling pass works the claim out afresh
// from the job's labels and its route. It is what the Jobs page and a pool's
// queue count read, and a job sent to another class that went on being listed
// under the pool it left would have those pages say the opposite of what the
// scheduler did.
func (s *Store) SetJobClaim(ctx context.Context, jobID, poolID string) (bool, error) {
	res, err := s.exec(ctx, `UPDATE jobs SET pool_id=?1, matched=?2 WHERE id=?3 AND state='queued'
		AND (pool_id != ?1 OR matched != ?2)`, poolID, boolInt(poolID != ""), jobID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// StampJobRan records the class of the host that took a job, once, and returns
// the job as it now is and whether this call was the one that wrote it. Like
// StampJobGranted it fills only a job that has none, so the same assignment seen
// twice -- by a webhook and by the poller -- leaves the first observation, and a
// host that is reclassed afterwards cannot change where an old job ran.
func (s *Store) StampJobRan(ctx context.Context, jobID string, class SizeClass) (*Job, bool, error) {
	var out *Job
	var wrote bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if class.Valid() {
			res, err := tx.ExecContext(ctx, `UPDATE jobs SET ran_class=? WHERE id=? AND ran_class=''`, string(class), jobID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			wrote = n > 0
		}
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, jobID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job %s: %w", jobID, ErrNotFound)
		}
		out = j
		return err
	})
	return out, wrote && err == nil, err
}

// RecordJobThrottle raises the CPU throttling counters of the job in progress
// on a runner to one sample of that runner's.
//
// The counters are cumulative for the runner's life, so MAX is the right merge:
// a late or repeated sample cannot lower them, and a job is on one runner for
// the whole of its run. Like RecordJobUsage it touches only a job GitHub says
// is in progress, which is what keeps it on the queued-and-in-progress index.
func (s *Store) RecordJobThrottle(ctx context.Context, runnerID string, periods, throttledPeriods int64) error {
	if runnerID == "" || periods <= 0 {
		return nil
	}
	_, err := s.exec(ctx, `UPDATE jobs SET cpu_periods = MAX(cpu_periods, ?1), cpu_throttled_periods = MAX(cpu_throttled_periods, ?2)
		WHERE state = 'in_progress' AND runner_id = ?3`, periods, max(throttledPeriods, 0), runnerID)
	return err
}

// ---------------------------------------------------------------------------
// An operator's word on a class
// ---------------------------------------------------------------------------

// SizePin fixes the class of one job, or of every job in a repository.
// Workflow and JobName are both empty for a repository's pin and both set for
// a job's; a pin on a workflow alone is not a thing the table can say.
type SizePin struct {
	Repo      string    `json:"repo"`
	Workflow  string    `json:"workflow,omitempty"`
	JobName   string    `json:"job_name,omitempty"`
	Class     SizeClass `json:"class"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ForRepository reports whether the pin covers a whole repository.
func (p SizePin) ForRepository() bool { return p.Workflow == "" && p.JobName == "" }

// MaxPinText bounds each part of a pin. GitHub allows a repository's owner at
// most 39 characters and its name 100, and no workflow or job anyone would pin
// comes near 255; the bound is there so that a pin cannot be made out of a
// paragraph, which would sit in every pin lookup and every advice row for ever.
const MaxPinText = 255

// PinProblem is one thing wrong with a pin, by the field it is about.
type PinProblem struct {
	Field   string
	Message string
}

// Problems says everything wrong with a pin, once for each field, as a sentence
// an operator can act on. checkClass is false where only the pin's key is in
// question, which is when one is being removed.
//
// A repository is owner/name: one slash with something either side of it and no
// spaces, which is what GitHub reports and what a lookup compares. A workflow or
// job name is kept exactly as written, because that is how a job's own are
// compared, so one with a space at either end is refused rather than saved as a
// pin that could never match what GitHub reports.
func (p SizePin) Problems(checkClass bool) []PinProblem {
	var out []PinProblem
	repo := strings.TrimSpace(p.Repo)
	owner, name, slash := strings.Cut(repo, "/")
	switch {
	case !slash || owner == "" || name == "" || strings.Contains(name, "/") || strings.ContainsFunc(repo, unicode.IsSpace):
		out = append(out, PinProblem{"repo", "a pin needs a repository, written owner/name, with one slash and no spaces"})
	case len(repo) > MaxPinText:
		out = append(out, PinProblem{"repo", fmt.Sprintf("a repository is at most %d characters", MaxPinText)})
	}
	if checkClass && !p.Class.Valid() {
		out = append(out, PinProblem{"class", fmt.Sprintf("%q is not a size class; use small, medium or large", string(p.Class))})
	}
	if (p.Workflow == "") != (p.JobName == "") {
		out = append(out, PinProblem{"job_name", "name both the workflow and the job to pin one job, or neither to pin the whole repository"})
		return out
	}
	for _, part := range []struct{ field, label, value string }{
		{"workflow", "workflow", p.Workflow}, {"job_name", "job name", p.JobName},
	} {
		switch {
		case part.value != strings.TrimSpace(part.value):
			out = append(out, PinProblem{part.field, "the " + part.label + " has a space at one end, so it could never match the name GitHub reports; write it as it appears on the Jobs page"})
		case len(part.value) > MaxPinText:
			out = append(out, PinProblem{part.field, fmt.Sprintf("a %s is at most %d characters", part.label, MaxPinText)})
		case strings.ContainsFunc(part.value, unicode.IsControl):
			out = append(out, PinProblem{part.field, "the " + part.label + " contains a control character, which no name GitHub reports does"})
		}
	}
	return out
}

// Validate says what is wrong with a pin, as one sentence an operator can act on.
func (p SizePin) Validate() error {
	if problems := p.Problems(true); len(problems) > 0 {
		return errors.New(problems[0].Message)
	}
	return nil
}

// SetSizePin creates or replaces a pin. The repository is compared the way
// GitHub compares it, without regard to case, so a pin typed "Acme/Widgets"
// reaches jobs that GitHub reports as "acme/widgets".
func (s *Store) SetSizePin(ctx context.Context, p *SizePin) error {
	if err := p.Validate(); err != nil {
		return err
	}
	p.Repo = strings.ToLower(strings.TrimSpace(p.Repo))
	p.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO size_pins (repo, workflow, job_name, class, created_by, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(repo, workflow, job_name) DO UPDATE SET class=excluded.class, created_by=excluded.created_by,
			created_at=excluded.created_at`,
		p.Repo, p.Workflow, p.JobName, string(p.Class), p.CreatedBy, ms(p.CreatedAt))
	return wrapWrite(err)
}

// DeleteSizePin removes a pin, and returns ErrNotFound for one that is not there.
func (s *Store) DeleteSizePin(ctx context.Context, repo, workflow, job string) error {
	res, err := s.exec(ctx, `DELETE FROM size_pins WHERE repo=? AND workflow=? AND job_name=?`,
		strings.ToLower(strings.TrimSpace(repo)), workflow, job)
	if err != nil {
		return err
	}
	return affected(res, "size pin", repo)
}

// ListSizePins returns every pin, repositories first and jobs within them.
func (s *Store) ListSizePins(ctx context.Context) ([]*SizePin, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT repo, workflow, job_name, class, created_by, created_at
		FROM size_pins ORDER BY repo, workflow, job_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SizePin
	for rows.Next() {
		var p SizePin
		var created int64
		if err := rows.Scan(&p.Repo, &p.Workflow, &p.JobName, &p.Class, &p.CreatedBy, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = at(created)
		out = append(out, &p)
	}
	return out, rows.Err()
}

// SizePinFor returns the pin that applies to a job: its own, and failing that
// its repository's. ErrNotFound means nothing has been pinned.
//
// Two lookups by primary key rather than one query ordered so that the job's
// wins: this runs for every job that is queued, and the plain shape is the one
// the planner cannot get wrong.
func (s *Store) SizePinFor(ctx context.Context, repo, workflow, job string) (*SizePin, error) {
	repo = strings.ToLower(strings.TrimSpace(repo))
	if workflow != "" && job != "" {
		if p, err := s.sizePin(ctx, repo, workflow, job); p != nil || err != nil {
			return p, err
		}
	}
	if p, err := s.sizePin(ctx, repo, "", ""); p != nil || err != nil {
		return p, err
	}
	return nil, fmt.Errorf("size pin for %s: %w", repo, ErrNotFound)
}

// sizePin reads one pin by its key, and returns nil, nil for one that is not there.
func (s *Store) sizePin(ctx context.Context, repo, workflow, job string) (*SizePin, error) {
	var p SizePin
	var created int64
	err := s.read.QueryRowContext(ctx, `SELECT repo, workflow, job_name, class, created_by, created_at
		FROM size_pins WHERE repo=? AND workflow=? AND job_name=?`, repo, workflow, job).
		Scan(&p.Repo, &p.Workflow, &p.JobName, &p.Class, &p.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = at(created)
	return &p, nil
}
