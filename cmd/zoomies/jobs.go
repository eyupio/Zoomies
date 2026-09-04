package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// runJobs is `zoomies jobs ...`.
func runJobs(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "jobs", "Job history, queue waits and outcomes.", []*subcommand{
		{"list", "", "Recent jobs, with filters", jobsList},
		{"get", "<job-id>", "One job in full", jobsGet},
	}, args)
}

func jobsList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies jobs list [filters]", "List jobs, newest first.")
	cf := registerClientFlags(fs, true)
	page := registerPageFlags(fs, 50)
	repos, workflows, pools, states, conclusions := &listValue{}, &listValue{}, &listValue{}, &listValue{}, &listValue{}
	fs.Var(repos, "repo", "only this repository, e.g. acme/widgets (repeatable)")
	fs.Var(workflows, "workflow", "only this workflow (repeatable)")
	fs.Var(pools, "pool", "only jobs that ran in this pool (repeatable)")
	fs.Var(states, "state", "queued, in_progress or completed (repeatable)")
	fs.Var(conclusions, "conclusion", "success, failure, cancelled or skipped (repeatable)")
	query := fs.String("q", "", "substring match on repository, workflow or job name")
	since := fs.String("since", "", "only jobs queued since then: a duration like 24h, or an RFC 3339 timestamp")
	until := fs.String("until", "", "only jobs queued before then")
	unmatched := fs.Bool("unmatched", false, "only jobs no enabled pool claims; these will never run")
	fs.example(
		"zoomies jobs list --repo acme/widgets --since 24h",
		"zoomies jobs list --unmatched",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	q := url.Values{}
	addList(q, "repo", *repos)
	addList(q, "workflow", *workflows)
	addList(q, "pool_id", *pools)
	addList(q, "state", *states)
	addList(q, "conclusion", *conclusions)
	if *query != "" {
		q.Set("q", *query)
	}
	if *unmatched {
		q.Set("unmatched", "true")
	}
	for flagName, raw := range map[string]string{"since": *since, "until": *until} {
		if raw == "" {
			continue
		}
		when, err := parseWhen(raw)
		if err != nil {
			return usagef("jobs list", "--%s %q: %v", flagName, raw, err)
		}
		q.Set(flagName, when.UTC().Format(time.RFC3339))
	}
	page.apply(q)

	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	var out listResponse[jobItem]
	raw, err := client.get(ctx, "/jobs", q, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No jobs match.")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, j := range out.Items {
		outcome := j.Conclusion
		if outcome == "" {
			outcome = j.State
		}
		name := j.JobName
		if !j.Matched {
			name += p.paint(colourYellow, " (no pool)")
		}
		rows = append(rows, []string{
			truncate(j.Repo, 28),
			truncate(j.Workflow, 20),
			truncate(name, 28),
			p.state(outcome),
			dash(j.PoolName),
			millis(j.QueueWaitMS),
			millis(j.DurationMS),
			p.relTime(j.QueuedAt),
		})
	}
	p.table([]string{"repo", "workflow", "job", "result", "pool", "waited", "ran for", "queued"}, rows)
	p.footer(len(out.Items), out.Total, out.Offset)
	return nil
}

func jobsGet(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies jobs get <job-id>", "Show one job.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a job ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	var j jobItem
	raw, err := client.get(ctx, "/jobs/"+url.PathEscape(id), nil, &j)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	rows := [][2]string{
		{"id", j.ID},
		{"repo", j.Repo},
		{"workflow", j.Workflow},
		{"job", j.JobName},
		{"state", p.state(j.State)},
		{"conclusion", p.state(dash(j.Conclusion))},
		{"labels", dash(strings.Join(j.Labels, ", "))},
		{"matched a pool", p.yesNo(j.Matched, false)},
		{"pool", dash(j.PoolName)},
		{"runner", dash(j.RunnerName)},
		{"queued", p.relTime(j.QueuedAt)},
		{"started", p.relTimePtr(j.StartedAt)},
		{"completed", p.relTimePtr(j.CompletedAt)},
		{"queue wait", millis(j.QueueWaitMS)},
		{"duration", millis(j.DurationMS)},
	}
	if j.HTMLURL != "" {
		rows = append(rows, [2]string{"on github", j.HTMLURL})
	}
	p.keyValues(rows)
	if !j.Matched {
		fmt.Fprintln(p.out, "\nNo enabled pool claims this job's labels, so it will never start.")
		fmt.Fprintln(p.out, "Check the runs-on in the workflow against `zoomies pools list`.")
	}
	return nil
}

// parseWhen accepts either a duration ago ("24h") or an absolute RFC 3339
// timestamp, because both are what people reach for and neither is surprising.
func parseWhen(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			d = -d
		}
		return time.Now().Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not a duration like 24h nor a timestamp like 2026-01-30 or 2026-01-30T12:00:00Z")
}
