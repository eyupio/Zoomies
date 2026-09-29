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
		{"rerun", "<job-id>", "Ask GitHub to run this run's failed jobs again", jobsRerun},
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
	fs.Var(states, "state", "waiting, queued, in_progress or completed (repeatable)")
	fs.Var(conclusions, "conclusion", "success, failure, cancelled or skipped (repeatable)")
	query := fs.String("q", "", "substring match on repository, workflow or job name")
	since := fs.String("since", "", "only jobs queued since then: a duration like 24h, or an RFC 3339 timestamp")
	until := fs.String("until", "", "only jobs queued before then")
	unmatched := fs.Bool("unmatched", false, "only jobs no enabled pool claims; these will never run")
	failed := fs.Bool("failed", false, "only jobs that went wrong: a failing conclusion, or a runner that stopped under the job")
	faulted := fs.Bool("ours", false, "only the failures this fleet caused, not the workflows' own")
	theirs := fs.Bool("theirs", false, "only the failures the workflows caused, with nothing wrong on this side")
	faults := &listValue{}
	fs.Var(faults, "fault", "only this fault category: out_of_memory, host_lost, image, registration, backend, config, out_of_disk, removed, runner_exited (repeatable)")
	fs.example(
		"zoomies jobs list --repo acme/widgets --since 24h",
		"zoomies jobs list --unmatched",
		"zoomies jobs list --failed --since 1h",
		"zoomies jobs list --ours --since 24h",
		"zoomies jobs list --fault out_of_memory",
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
	if *failed {
		q.Set("failed", "true")
	}
	if *faulted && *theirs {
		return usagef("jobs list", "--ours and --theirs ask for opposite halves of the same list; pass one")
	}
	if *faulted {
		q.Set("faulted", "true")
	}
	if *theirs {
		q.Set("workflow_failed", "true")
	}
	addList(q, "fault", *faults)
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
		j.sanitise()
		outcome := j.Conclusion
		if outcome == "" {
			outcome = j.State
		}
		name := j.JobName
		if !j.Matched {
			name += p.paint(colourYellow, " (no pool)")
		}
		if note := queueNote(j); note != "" {
			name += p.paint(colourYellow, " ("+note+")")
		}
		rows = append(rows, []string{
			truncate(j.Repo, 28),
			truncate(j.Workflow, 20),
			truncate(name, 28),
			p.state(outcome),
			truncate(dash(failureWhy(j)), 32),
			dash(j.PoolName),
			millis(j.QueueWaitMS),
			millis(j.DurationMS),
			p.relTime(j.QueuedAt),
		})
	}
	p.table([]string{"repo", "workflow", "job", "result", "why", "pool", "waited", "ran for", "queued"}, rows)
	p.footer(len(out.Items), out.Total, out.Offset)
	return nil
}

// queueNote is the short form for a listing: what an operator did to this
// job's demand, or nothing at all for the ordinary case. Only a job still
// queued carries one -- once something has run it, what was done to its demand
// is history rather than status.
func queueNote(j jobItem) string {
	// A cancelled run pauses its queued jobs, so this has to come first or an
	// operator's own cancellation is reported back to them as a pause.
	if j.CancelRequestedAt != nil && j.State != "completed" {
		return "cancelling"
	}
	if j.State != "queued" {
		return ""
	}
	switch j.Provisioning {
	case "paused":
		return "paused"
	case "deleted":
		return "removed from queue"
	}
	return ""
}

// queueStatus is the long form for `jobs get`, which says the ordinary case
// out loud rather than leaving a blank row to be read as "nothing is known".
func queueStatus(j jobItem) string {
	if j.CancelRequestedAt != nil && j.State != "completed" {
		return "cancelling -- GitHub accepted the cancellation and has yet to report the conclusion"
	}
	if j.State != "queued" {
		return ""
	}
	switch j.Provisioning {
	case "paused":
		return "paused -- no new runner demand until it is resumed"
	case "deleted":
		return "removed from the queue -- it no longer counts as work this fleet is waiting on"
	}
	if j.ProvisionNow {
		return "expedited"
	}
	return "ready"
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
	var timeline listResponse[jobEventItem]
	// The timeline is a second call so that a structured `jobs get` stays the
	// job document alone, as it always was; the table view adds the story.
	if _, terr := client.get(ctx, "/jobs/"+url.PathEscape(id)+"/events", nil, &timeline); terr != nil && !p.structured() {
		return terr
	}
	if p.structured() {
		return p.emit(raw)
	}
	j.sanitise()

	rows := [][2]string{
		{"id", j.ID},
		{"repo", j.Repo},
		{"workflow", j.Workflow},
		{"job", j.JobName},
		{"branch", dash(j.HeadBranch)},
		{"attempt", attempt(j.RunAttempt)},
		{"state", p.state(j.State)},
		{"conclusion", p.state(dash(j.Conclusion))},
		{"labels", dash(strings.Join(j.Labels, ", "))},
		{"matched a pool", p.yesNo(j.Matched, false)},
		{"queue status", dash(queueStatus(j))},
		{"pool", dash(j.PoolName)},
		{"runner", dash(j.RunnerName)},
		{"queued", p.relTime(j.QueuedAt)},
		{"started", p.relTimePtr(j.StartedAt)},
		{"completed", p.relTimePtr(j.CompletedAt)},
		{"queue wait", millis(j.QueueWaitMS)},
		{"duration", millis(j.DurationMS)},
	}
	if j.FailedStep != nil {
		rows = append(rows, [2]string{"failed at", fmt.Sprintf("step %d, %s", j.FailedStep.Number, j.FailedStep.Name)})
	}
	if j.FaultDomain != "" {
		// Whose it was, first: it is the one thing somebody reading a failure
		// wants settled before anything else on the page.
		blame := "the workflow's own"
		if j.FaultDomain == "fleet" {
			blame = p.paint(colourRed, "this fleet's")
		}
		rows = append(rows, [2]string{"failure is", blame})
	}
	if j.FaultKind != "" {
		rows = append(rows, [2]string{"fault", p.paint(colourRed, strings.ReplaceAll(j.FaultKind, "_", " "))})
	}
	if j.RunnerFault != "" {
		rows = append(rows, [2]string{"runner lost", p.paint(colourRed, j.RunnerFault)})
	}
	if j.HTMLURL != "" {
		rows = append(rows, [2]string{"on github", j.HTMLURL})
	}
	p.keyValues(rows)

	// Why the job is where it is, from the controller rather than worked out
	// here. This used to be a paragraph the CLI reasoned its way to on its own,
	// which could see the job row and nothing else -- not the scheduler's own
	// reason for failing to place a runner, and not the host under the runner.
	// The drawer had a different paragraph for the same question.
	var why explanationItem
	if _, eerr := client.get(ctx, "/jobs/"+url.PathEscape(id)+"/explanation", nil, &why); eerr == nil && why.Summary != "" {
		fmt.Fprintln(p.out)
		fmt.Fprintln(p.out, plain(why.Summary))
		if why.Detail != "" {
			fmt.Fprintln(p.out, plain(why.Detail))
		}
		if why.Fix != "" {
			fmt.Fprintln(p.out, p.paint(colourYellow, "Fix: ")+plain(why.Fix))
		}
	}

	if len(j.Steps) > 0 {
		fmt.Fprintln(p.out, "\nSteps")
		stepRows := make([][]string, 0, len(j.Steps))
		for _, st := range j.Steps {
			outcome := st.Conclusion
			if outcome == "" {
				outcome = st.Status
			}
			took := "--"
			if st.StartedAt != nil && st.CompletedAt != nil {
				took = millis(st.CompletedAt.Sub(*st.StartedAt).Milliseconds())
			}
			stepRows = append(stepRows, []string{fmt.Sprintf("%d", st.Number), truncate(st.Name, 40), p.state(outcome), took})
		}
		p.table([]string{"#", "step", "result", "took"}, stepRows)
	}

	if len(timeline.Items) > 0 {
		fmt.Fprintln(p.out, "\nTimeline")
		eventRows := make([][]string, 0, len(timeline.Items))
		for _, e := range timeline.Items {
			eventRows = append(eventRows, []string{p.relTime(e.At), e.Source, plain(e.Message)})
		}
		p.table([]string{"when", "source", "what happened"}, eventRows)
	}
	return nil
}

// failureWhy is the one phrase the list has room for on a job that went wrong:
// the step it failed at, or the fact that its runner stopped under it.
// failureWhy is the "why" column: the fleet's category where the fleet is at
// fault, and the step where the workflow is.
//
// The category rather than "runner lost" for every one of them, because the
// column is read down: a list where nine rows say the same two words says only
// that the fleet is unwell, and one where six say "out of memory" says what to
// do on Monday.
func failureWhy(j jobItem) string {
	switch {
	case j.FaultKind != "":
		return strings.ReplaceAll(j.FaultKind, "_", " ")
	case j.RunnerFault != "":
		return "runner lost"
	case j.FailedStep != nil:
		return j.FailedStep.Name
	}
	return ""
}

func attempt(n int) string {
	if n <= 0 {
		return "--"
	}
	return fmt.Sprintf("%d", n)
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

// jobsRerun is the remedy for a job the fleet broke, at the terminal.
func jobsRerun(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies jobs rerun <job-id>",
		"Ask GitHub to run the failed jobs of this job's workflow run again.\n\n"+
			"GitHub has no job-level rerun, so this re-runs every failed job in the run,\n"+
			"not only this one. Nothing local changes: the rerun arrives as a new run\n"+
			"attempt through the ordinary webhook path.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies jobs rerun job_2fq8xk3m")
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
	var out rerunResponse
	raw, err := client.post(ctx, "/jobs/"+url.PathEscape(id)+"/rerun", nil, nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note(fmt.Sprintf("GitHub accepted the request; run %d will re-run its failed jobs.", out.RunID))
	if out.FaultDomain == "fleet" {
		p.note("This job's failure was the fleet's rather than the workflow's.")
	}
	return nil
}
