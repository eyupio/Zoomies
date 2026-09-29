package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// runJobs is `zoomies jobs ...`.
func runJobs(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "jobs", "Job history, queue waits and outcomes.", []*subcommand{
		{"list", "", "Recent jobs, with filters", jobsList},
		{"stats", "", "Counts and percentiles over a period, grouped by release, day, host, pool or job", jobsStats},
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
	jobNames, versions, hostIDs := &exactList{}, &listValue{}, &listValue{}
	fs.Var(jobNames, "job-name", "only jobs with exactly this name; -q matches a substring instead (repeatable)")
	fs.Var(versions, "controller-version", "only jobs claimed by this controller version; \"unknown\" is the jobs never stamped (repeatable)")
	fs.Var(hostIDs, "host-id", "only jobs that ran on this host, by ID (repeatable)")
	hosted := fs.String("hosted", "", "true: only jobs on somebody else's hosted runners; false: only jobs that are not")
	before := fs.String("before", "", "continue after the previous page: the cursor it printed, or the next field of --output json")
	steps := fs.Bool("include-steps", true, "with --output json, include each job's steps; --include-steps=false returns short summaries")
	faults := &listValue{}
	fs.Var(faults, "fault", "only this fault category: out_of_memory, host_lost, image, registration, backend, config, out_of_disk, removed, runner_exited (repeatable)")
	fs.example(
		"zoomies jobs list --repo acme/widgets --since 24h",
		"zoomies jobs list --unmatched",
		"zoomies jobs list --failed --since 1h",
		"zoomies jobs list --ours --since 24h",
		"zoomies jobs list --fault out_of_memory",
		"zoomies jobs list --controller-version v1.3.3 --since 30d --output json --include-steps=false",
		"zoomies jobs list --since 30d --until 7d --limit 100 --before <cursor>",
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
	addList(q, "job_name", *jobNames)
	addList(q, "controller_version", *versions)
	addList(q, "host_id", *hostIDs)
	if err := setHostedFlag(q, "jobs list", *hosted); err != nil {
		return err
	}
	if *before != "" {
		q.Set("before", *before)
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
	// The table reads fields a summary leaves out -- whether a pool claimed the
	// job, what was done to its demand -- so the choice is only offered where
	// somebody else's program is the reader.
	if p.structured() && !*steps {
		q.Set("include_steps", "false")
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
	if out.Next != "" && *before != "" {
		fmt.Fprintf(p.out, "More: repeat the command with --before %s\n", out.Next)
	} else if out.Next != "" {
		fmt.Fprintf(p.out, "To page by cursor instead of offset: --before %s\n", out.Next)
	}
	return nil
}

// setHostedFlag validates --hosted before it is sent: a typo the server
// silently read as absent would list every job and look like an answer.
func setHostedFlag(q url.Values, command, raw string) error {
	switch raw {
	case "":
	case "true", "false":
		q.Set("hosted", raw)
	default:
		return usagef(command, "--hosted %q: use true or false", raw)
	}
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
		fmt.Fprintln(p.out, why.Summary)
		if why.Detail != "" {
			fmt.Fprintln(p.out, why.Detail)
		}
		if why.Fix != "" {
			fmt.Fprintln(p.out, p.paint(colourYellow, "Fix: ")+why.Fix)
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
			eventRows = append(eventRows, []string{p.relTime(e.At), e.Source, e.Message})
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
	if d, ok := parseAgo(raw); ok {
		return time.Now().Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not a duration like 24h or 30d nor a timestamp like 2026-01-30 or 2026-01-30T12:00:00Z")
}

// parseAgo reads how long ago: a Go duration such as 90m or 24h, or whole days
// or weeks as 30d and 2w, because release comparisons are made in those and
// Go's own parser stops at hours.
func parseAgo(raw string) (time.Duration, bool) {
	if n := len(raw) - 1; n > 0 {
		var unit time.Duration
		switch raw[n] {
		case 'd':
			unit = 24 * time.Hour
		case 'w':
			unit = 7 * 24 * time.Hour
		}
		if unit != 0 {
			if count, err := strconv.Atoi(raw[:n]); err == nil {
				return time.Duration(max(count, -count)) * unit, true
			}
		}
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	if d < 0 {
		d = -d
	}
	return d, true
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

// jobStatsResponse is GET /jobs/stats, as far as the table needs it.
type jobStatsResponse struct {
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
	GroupBy   []string  `json:"group_by"`
	Notes     []string  `json:"notes"`
	Truncated bool      `json:"truncated"`
	Groups    []struct {
		Keys             map[string]string `json:"keys"`
		Count            int               `json:"count"`
		Succeeded        int               `json:"succeeded"`
		Failed           int               `json:"failed"`
		Cancelled        int               `json:"cancelled"`
		FleetFailed      int               `json:"fleet_failed"`
		FleetFailureRate float64           `json:"fleet_failure_rate"`
		Duration         percentileItem    `json:"duration"`
		QueueWait        percentileItem    `json:"queue_wait"`
		Startup          percentileItem    `json:"startup"`
	} `json:"groups"`
}

type percentileItem struct {
	Samples int    `json:"samples"`
	P50MS   *int64 `json:"p50_ms"`
	P95MS   *int64 `json:"p95_ms"`
}

// ms renders a figure that may be absent: "-" is "nothing to measure", which
// is not the same statement as 0ms.
func (p percentileItem) p50() string { return optionalMillis(p.P50MS) }
func (p percentileItem) p95() string { return optionalMillis(p.P95MS) }

// optionalMillis keeps the seconds of a duration that is minutes long, which
// millis does not: two releases a few seconds apart are the whole point of this
// table, and "1m" for both would hide it.
func optionalMillis(v *int64) string {
	if v == nil {
		return "-"
	}
	if *v <= 0 {
		return "0ms"
	}
	d := time.Duration(*v) * time.Millisecond
	if d < time.Minute {
		return millis(*v)
	}
	return d.Round(time.Second).String()
}

func jobsStats(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies jobs stats [filters]",
		"Count and time completed jobs over a period, grouped by up to two keys.\n\n"+
			"Each group shows how many jobs finished and how, how many were lost to the\n"+
			"fleet rather than the workflow, and p50 and p95 of duration, queue wait and\n"+
			"runner startup. Duration leaves out cancelled and skipped jobs. Jobs that\n"+
			"were never stamped with a release are the group \"unknown\".")
	cf := registerClientFlags(fs, true)
	groupBy, repos, workflows, jobNames := &listValue{}, &listValue{}, &listValue{}, &exactList{}
	fs.Var(groupBy, "group-by", "controller_version, day, host, pool or job_name; at most two (repeatable)")
	fs.Var(repos, "repo", "only this repository, e.g. acme/widgets (repeatable)")
	fs.Var(workflows, "workflow", "only this workflow (repeatable)")
	fs.Var(jobNames, "job-name", "only jobs with exactly this name (repeatable)")
	hosted := fs.String("hosted", "", "true: only jobs on somebody else's hosted runners; false: only jobs that are not")
	since := fs.String("since", "", "start of the window, included: a duration like 30d, or an RFC 3339 timestamp (default 7d)")
	until := fs.String("until", "", "end of the window, not included (default now)")
	fs.example(
		"zoomies jobs stats --group-by controller_version --since 30d",
		"zoomies jobs stats --group-by day --job-name build --since 14d",
		"zoomies jobs stats --group-by controller_version --group-by pool --hosted=false --output json",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	q := url.Values{}
	addList(q, "group_by", *groupBy)
	addList(q, "repo", *repos)
	addList(q, "workflow", *workflows)
	addList(q, "job_name", *jobNames)
	if err := setHostedFlag(q, "jobs stats", *hosted); err != nil {
		return err
	}
	for flagName, raw := range map[string]string{"since": *since, "until": *until} {
		if raw == "" {
			continue
		}
		when, err := parseWhen(raw)
		if err != nil {
			return usagef("jobs stats", "--%s %q: %v", flagName, raw, err)
		}
		q.Set(flagName, when.UTC().Format(time.RFC3339))
	}

	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var out jobStatsResponse
	raw, err := client.get(ctx, "/jobs/stats", q, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Groups) == 0 {
		p.note("No completed jobs in that window.")
		return nil
	}

	header := append([]string{}, out.GroupBy...)
	if len(header) == 0 {
		header = []string{"window"}
	}
	header = append(header, "jobs", "ok", "failed", "cancelled", "fleet", "fleet %",
		"run p50", "run p95", "wait p50", "wait p95", "start p50", "start p95")
	rows := make([][]string, 0, len(out.Groups))
	for _, g := range out.Groups {
		var row []string
		for _, k := range out.GroupBy {
			row = append(row, g.Keys[k])
		}
		if len(out.GroupBy) == 0 {
			row = append(row, out.Since.Format("2006-01-02")+" to "+out.Until.Format("2006-01-02"))
		}
		row = append(row, strconv.Itoa(g.Count), strconv.Itoa(g.Succeeded), strconv.Itoa(g.Failed),
			strconv.Itoa(g.Cancelled), strconv.Itoa(g.FleetFailed), fmt.Sprintf("%.1f", g.FleetFailureRate*100),
			g.Duration.p50(), g.Duration.p95(), g.QueueWait.p50(), g.QueueWait.p95(), g.Startup.p50(), g.Startup.p95())
		rows = append(rows, row)
	}
	p.table(header, rows)
	if out.Truncated {
		p.note("More groups matched than are shown; narrow the window or the filters.")
	}
	for _, n := range out.Notes {
		fmt.Fprintln(p.out, n)
	}
	return nil
}
