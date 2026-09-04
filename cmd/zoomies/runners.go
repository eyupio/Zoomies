package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// runRunners is `zoomies runners ...`.
func runRunners(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "runners", "The runners that exist right now.", []*subcommand{
		{"list", "", "List runners, with filters", runnersList},
		{"get", "<runner-id>", "One runner in full, with its timeline", runnersGet},
		{"drain", "<runner-id>...", "Finish the current job, then exit; never interrupts work", runnersDrain},
		{"delete", "<runner-id>...", "Remove a runner and deregister it from GitHub", runnersDelete},
		{"logs", "<runner-id>", "The runner's output, optionally followed", runnersLogs},
	}, args)
}

func runnersList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies runners list [filters]", "List runners. Terminal ones are hidden unless you ask for them.")
	cf := registerClientFlags(fs, true)
	page := registerPageFlags(fs, 50)
	pools := &listValue{}
	hosts := &listValue{}
	states := &listValue{}
	fs.Var(pools, "pool", "only runners in this pool (repeatable)")
	fs.Var(hosts, "host", "only runners on this host (repeatable)")
	fs.Var(states, "state", "provisioning, registering, idle, busy, draining, removed or failed (repeatable)")
	query := fs.String("q", "", "substring match on name, ID or container ID")
	includeRemoved := fs.Bool("include-removed", false, "include runners that have finished; a busy fleet accumulates thousands")
	fs.example(
		"zoomies runners list --state busy",
		"zoomies runners list --pool pool_k3f9qz2m --output json",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
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

	q := url.Values{}
	addList(q, "pool_id", *pools)
	addList(q, "host_id", *hosts)
	addList(q, "state", *states)
	if *query != "" {
		q.Set("q", *query)
	}
	if *includeRemoved {
		q.Set("include_removed", "true")
	}
	page.apply(q)

	var out listResponse[runnerItem]
	raw, err := client.get(ctx, "/runners", q, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No runners match. Without --include-removed, only live runners are listed.")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, r := range out.Items {
		job := "-"
		if r.CurrentJob != nil {
			job = truncate(r.CurrentJob.Repo+" "+r.CurrentJob.JobName, 36)
		}
		rows = append(rows, []string{
			r.Name,
			r.ID,
			p.state(r.State),
			dash(r.PoolName),
			dash(r.HostName),
			job,
			p.relTime(r.CreatedAt),
		})
	}
	p.table([]string{"name", "id", "state", "pool", "host", "job", "age"}, rows)
	p.footer(len(out.Items), out.Total, out.Offset)
	return nil
}

func runnersGet(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies runners get <runner-id>", "Show one runner, its current job and how it got here.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a runner ID, as shown by `zoomies runners list`")
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

	var r runnerDetail
	raw, err := client.get(ctx, "/runners/"+url.PathEscape(id), nil, &r)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	rows := [][2]string{
		{"name", r.Name},
		{"id", r.ID},
		{"state", p.state(r.State)},
		{"pool", dash(r.PoolName)},
		{"host", dash(r.HostName)},
		{"ephemeral", p.yesNo(r.Ephemeral, false)},
		{"labels", dash(strings.Join(r.Labels, ", "))},
		{"image", dash(r.Image)},
		{"container", dash(r.ContainerID)},
		{"jobs handled", strconv.Itoa(r.JobsHandled)},
		{"created", p.relTime(r.CreatedAt)},
		{"started", p.relTimePtr(r.StartedAt)},
		{"finished", p.relTimePtr(r.FinishedAt)},
		{"logs available", p.yesNo(r.LogsAvailable, false)},
	}
	if r.CurrentJob != nil {
		rows = append(rows, [2]string{"current job", r.CurrentJob.Repo + " / " + r.CurrentJob.Workflow + " / " + r.CurrentJob.JobName})
	}
	if r.Message != "" {
		rows = append(rows, [2]string{"message", r.Message})
	}
	p.keyValues(rows)

	if len(r.Timeline) > 0 {
		fmt.Fprintln(p.out, "\nTimeline")
		tl := make([][]string, 0, len(r.Timeline))
		for _, entry := range r.Timeline {
			tl = append(tl, []string{p.state(entry.State), p.relTime(entry.At), millis(entry.DurationMS), truncate(entry.Message, 48)})
		}
		p.table([]string{"state", "when", "for", "message"}, tl)
	}
	return nil
}

func runnersDrain(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies runners drain <runner-id>...",
		"Ask runners to finish their current job and then exit. A job in flight is never interrupted.")
	cf := registerClientFlags(fs, false)
	fs.example("zoomies runners drain run_k3f9qz2m", "zoomies runners drain run_a run_b run_c")
	if err := fs.parse(args); err != nil {
		return err
	}
	ids, err := fs.atLeastOneArg("runner ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}

	if len(ids) == 1 {
		var r runnerItem
		if _, err := client.post(ctx, "/runners/"+url.PathEscape(ids[0])+"/drain", nil, nil, &r); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Draining %s; it will exit once its current job finishes.\n", dash(r.Name))
		return nil
	}
	return bulkRunners(ctx, e, client, "drain", ids, false)
}

func runnersDelete(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies runners delete <runner-id>... [--force]",
		"Remove runners and deregister them from GitHub. Without --force this drains first.")
	cf := registerClientFlags(fs, false)
	force := fs.Bool("force", false, "destroy the runner now, interrupting any job it is running")
	fs.example("zoomies runners delete run_k3f9qz2m", "zoomies runners delete run_k3f9qz2m --force")
	if err := fs.parse(args); err != nil {
		return err
	}
	ids, err := fs.atLeastOneArg("runner ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}

	if len(ids) == 1 {
		q := url.Values{}
		if *force {
			q.Set("force", "true")
		}
		if _, err := client.del(ctx, "/runners/"+url.PathEscape(ids[0]), q, nil); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Removing %s.\n", ids[0])
		return nil
	}
	return bulkRunners(ctx, e, client, "delete", ids, *force)
}

// bulkRunners uses the bulk route, which answers per ID. Reporting each one is
// the point: a partial failure across twenty runners must not look like either
// a complete success or a complete failure.
func bulkRunners(ctx context.Context, e *env, client *apiClient, action string, ids []string, force bool) error {
	var out struct {
		Results []struct {
			ID    string `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}
	body := map[string]any{"action": action, "ids": ids, "force": force}
	if _, err := client.post(ctx, "/runners/bulk", nil, body, &out); err != nil {
		return err
	}

	failed := 0
	for _, r := range out.Results {
		if r.OK {
			fmt.Fprintf(e.out, "  %-24s ok\n", r.ID)
			continue
		}
		failed++
		fmt.Fprintf(e.out, "  %-24s %s\n", r.ID, r.Error)
	}
	fmt.Fprintf(e.out, "%s: %d of %d succeeded.\n", action, len(out.Results)-failed, len(out.Results))
	if failed > 0 {
		return fmt.Errorf("%d runner(s) were not %sed", failed, strings.TrimSuffix(action, "e"))
	}
	return nil
}

func runnersLogs(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies runners logs <runner-id> [--follow]",
		"Print a runner's output. With --follow, keep printing it as the job produces it.")
	cf := registerClientFlags(fs, false)
	follow := fs.Bool("follow", false, "keep the stream open and print output as it arrives")
	tail := fs.Int("tail", 1000, "lines of backlog before following; 0 means everything")
	fs.example("zoomies runners logs run_k3f9qz2m", "zoomies runners logs run_k3f9qz2m --follow --tail 50")
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a runner ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}

	// Without --follow this is a snapshot, and the API has a route that ends by
	// itself. Reading the live stream and guessing when to stop would give a
	// truncated file and no way to tell.
	if !*follow {
		resp, err := client.stream(ctx, "/runners/"+url.PathEscape(id)+"/logs/download", nil, "text/plain")
		if err != nil {
			return explainLogError(err, id)
		}
		defer resp.Body.Close()
		if _, err := io.Copy(e.out, resp.Body); err != nil && !stopped(ctx, err) {
			return err
		}
		return nil
	}

	q := url.Values{}
	q.Set("follow", "true")
	q.Set("tail", strconv.Itoa(max(*tail, 0)))
	resp, err := client.stream(ctx, "/runners/"+url.PathEscape(id)+"/logs", q, "text/event-stream")
	if err != nil {
		return explainLogError(err, id)
	}
	defer resp.Body.Close()

	// The relay sends each chunk as a JSON string, and one "end" event when
	// the runner's output finishes -- which, for an ephemeral runner, is the
	// job ending. Stopping there is what makes this command terminate.
	err = readSSE(resp.Body, func(f sseFrame) error {
		switch f.event {
		case "log":
			var chunk string
			if err := json.Unmarshal(f.data, &chunk); err != nil {
				// A frame this client cannot read is not a reason to drop the
				// rest of the job's output.
				return nil
			}
			_, err := io.WriteString(e.out, chunk)
			return err
		case "end":
			return io.EOF
		default:
			return nil
		}
	})
	if errors.Is(err, io.EOF) || stopped(ctx, err) {
		return nil
	}
	return err
}

// explainLogError turns "404" into the two things that actually cause it.
func explainLogError(err error, id string) error {
	if notFound(err) {
		return fmt.Errorf("no output for %s: either there is no runner with that ID, or its host is not reachable, so nothing can be relayed", id)
	}
	return err
}
