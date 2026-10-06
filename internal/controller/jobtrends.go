package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// Two things an operator looking at the numbers sees that nothing in the controller
// said: a workflow that fails at the same step every time it runs, and a job that
// has got much slower than it was. Neither is the fleet's fault by itself, and
// neither has a change the controller can make -- so both are information, with no
// remedy, and both say whose to fix and what to look at. They exist because the
// person who owns the workflow is not always the person watching the fleet, and the
// fleet is the one place both the failures and the timings are already recorded.

const (
	// failingWindow is how far back a failure counts; failingRuns is how many at
	// the same step make it a pattern rather than a bad commit.
	failingWindow = 6 * time.Hour
	failingRuns   = 3

	// slowerRecent is the window a job's typical time is read over, and slowerBase
	// the week before it that it is compared with. A day against a week, so one
	// busy morning on a shared host is not a regression.
	slowerRecent = 24 * time.Hour
	slowerBase   = 7 * 24 * time.Hour
	// slowerMinJobs is how many measured runs each side needs: a median of three is
	// not evidence. slowerRatio and slowerMinGain are both required, so a one-second
	// job that doubled and a long job that crept up a few percent are both left alone.
	slowerMinJobs = 10
	slowerRatio   = 1.5
	slowerMinGain = 60 * time.Second
)

// workflowFailingProblems is jobs.workflow_failing: a job that failed at the same
// step failingRuns times or more in the window and did not succeed once.
func (c *Controller) workflowFailingProblems(ctx context.Context, out *[]Problem) error {
	since := c.Now().Add(-failingWindow)
	base := store.JobFilter{Since: &since, States: []store.JobState{store.JobCompleted}}

	failed := base
	failed.Conclusions = []string{"failure"}
	bad, _, err := c.st.ListJobs(ctx, failed, store.Page{Limit: 500, Sort: "completed_at", Desc: true})
	if err != nil {
		return fmt.Errorf("listing failed jobs: %w", err)
	}
	if len(bad) < failingRuns {
		return nil
	}
	succeeded := base
	succeeded.Conclusions = []string{"success"}
	good, _, err := c.st.ListJobs(ctx, succeeded, store.Page{Limit: 500})
	if err != nil {
		return fmt.Errorf("listing jobs that passed: %w", err)
	}
	passed := map[string]bool{}
	for _, j := range good {
		passed[jobKey(j)] = true
	}

	type pattern struct {
		jobs []*store.Job
		step string
	}
	byStep := map[string]*pattern{}
	for _, j := range bad {
		if passed[jobKey(j)] {
			continue
		}
		step := "(no step recorded)"
		if st := j.FailedStep(); st != nil {
			step = st.Name
		}
		k := jobKey(j) + "\x00" + step
		if byStep[k] == nil {
			byStep[k] = &pattern{step: step}
		}
		byStep[k].jobs = append(byStep[k].jobs, j)
	}
	var patterns []*pattern
	for _, p := range byStep {
		if len(p.jobs) >= failingRuns {
			patterns = append(patterns, p)
		}
	}
	// Most failures first, then by name, so the list does not reorder between passes.
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i].jobs) != len(patterns[j].jobs) {
			return len(patterns[i].jobs) > len(patterns[j].jobs)
		}
		return jobKey(patterns[i].jobs[0]) < jobKey(patterns[j].jobs[0])
	})
	for _, p := range patterns {
		latest := p.jobs[0]
		where := "on this fleet's runners"
		fix := "open the failing step's log in the run, which is the workflow's own: the fleet did nothing wrong here."
		if hostedJob(latest.Labels) {
			where = "on GitHub's hosted runners, not this fleet's"
			fix = "open the failing step's log in the run: this fleet only saw the job, because GitHub runs it. If its output is a missing file or an expired token the fix is in the workflow or the repository's settings."
		}
		*out = append(*out, Problem{
			Code:     "jobs.workflow_failing",
			Severity: config.SeverityInfo,
			Title: fmt.Sprintf("%s in %s has failed at %s %d times in a row",
				workflowText(latest.JobName), workflowText(latest.Repo), workflowText(p.step), len(p.jobs)),
			Detail: fmt.Sprintf("%d runs in the last %s failed at that step and none of the job's runs passed, %s. Workflow %s.",
				len(p.jobs), failingWindow, where, workflowText(latest.Workflow)),
			Fix:        fix,
			TargetKind: "job", TargetID: latest.ID, Since: latest.CompletedAt,
		})
	}
	return nil
}

func jobKey(j *store.Job) string { return j.Repo + "\x00" + j.Workflow + "\x00" + j.JobName }

// jobsSlowerProblems is jobs.duration_regressed: a job whose typical run over the
// last day took at least half as long again, and a minute more, than over the week
// before, on the jobs this fleet ran.
func (c *Controller) jobsSlowerProblems(ctx context.Context, out *[]Problem) error {
	now := c.Now()
	recentFrom, baseFrom := now.Add(-slowerRecent), now.Add(-slowerRecent-slowerBase)
	read := func(from, until time.Time) (map[string]store.JobStatsGroup, error) {
		res, err := c.st.JobStats(ctx, store.JobFilter{Since: &from, Until: &until, ManagedOnly: true}, []string{store.GroupByJobName})
		if err != nil {
			return nil, fmt.Errorf("reading how long jobs took: %w", err)
		}
		byName := map[string]store.JobStatsGroup{}
		for _, g := range res.Groups {
			byName[g.Keys[store.GroupByJobName]] = g
		}
		return byName, nil
	}
	recent, err := read(recentFrom, now.Add(time.Minute))
	if err != nil {
		return err
	}
	baseline, err := read(baseFrom, recentFrom)
	if err != nil {
		return err
	}
	var names []string
	for name := range recent {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r, b := recent[name], baseline[name]
		if name == "" || r.Duration.Samples < slowerMinJobs || b.Duration.Samples < slowerMinJobs || r.Duration.P50MS == nil || b.Duration.P50MS == nil {
			continue
		}
		now, was := time.Duration(*r.Duration.P50MS)*time.Millisecond, time.Duration(*b.Duration.P50MS)*time.Millisecond
		if was <= 0 || float64(now) < slowerRatio*float64(was) || now-was < slowerMinGain {
			continue
		}
		*out = append(*out, Problem{
			Code:     "jobs.duration_regressed",
			Severity: config.SeverityInfo,
			Title: fmt.Sprintf("%s now takes %s, up from %s",
				workflowText(name), now.Round(time.Second), was.Round(time.Second)),
			Detail: fmt.Sprintf("the typical run of this job over the last %s took %s across %d runs, against %s across %d runs in the %s before. "+
				"The fleet's own failures and memory use are on the Jobs page; this is the job taking longer, whatever the cause.",
				slowerRecent, now.Round(time.Second), r.Duration.Samples, was.Round(time.Second), b.Duration.Samples, slowerBase),
			Fix: "compare a recent run with an older one on the Jobs page: a step that grew points at the workflow or its dependencies, and every step slower together points at the host, " +
				"so group the job's runs by host and by controller version (job_stats, or the Jobs page's filters) to tell which.",
		})
	}
	return nil
}
