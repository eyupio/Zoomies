package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// runAudit is `zoomies audit ...`.
func runAudit(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "audit", "Who did what.", []*subcommand{
		{"list", "", "Recent audit rows, with filters", auditList},
		{"tail", "", "Print recent rows and then follow the live stream", auditTail},
	}, args)
}

func auditList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies audit list [filters]", "List audit rows, newest first.")
	cf := registerClientFlags(fs, true)
	page := registerPageFlags(fs, 50)
	actors, actions, kinds := &listValue{}, &listValue{}, &listValue{}
	fs.Var(actors, "actor", "only this actor's rows (repeatable)")
	fs.Var(actions, "action", "only this action, e.g. pool.create (repeatable)")
	fs.Var(kinds, "target-kind", "only this kind of target, e.g. pool (repeatable)")
	target := fs.String("target", "", "only rows about this target ID")
	query := fs.String("q", "", "substring match")
	since := fs.String("since", "", "only rows since then: a duration like 24h, or a timestamp")
	until := fs.String("until", "", "only rows before then")
	fs.example("zoomies audit list --action pool.create", "zoomies audit list --since 24h --output json")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	q := url.Values{}
	addList(q, "actor_id", *actors)
	addList(q, "action", *actions)
	addList(q, "target_kind", *kinds)
	if *target != "" {
		q.Set("target_id", *target)
	}
	if *query != "" {
		q.Set("q", *query)
	}
	for name, raw := range map[string]string{"since": *since, "until": *until} {
		if raw == "" {
			continue
		}
		when, err := parseWhen(raw)
		if err != nil {
			return usagef("audit list", "--%s %q: %v", name, raw, err)
		}
		q.Set(name, when.UTC().Format(time.RFC3339))
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

	var out listResponse[auditItem]
	raw, err := client.get(ctx, "/audit", q, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No audit rows match.")
		return nil
	}
	p.table([]string{"when", "actor", "action", "target", "from"}, auditRows(p, out.Items))
	p.footer(len(out.Items), out.Total, out.Offset)
	return nil
}

// auditLine renders one row at fixed widths.
//
// A tail cannot use the table renderer: that measures every row before it
// prints any of them, and a stream has no last row. Fixed columns keep the
// backlog and the live rows in the same shape.
func auditLine(p *printer, a auditItem) string {
	actor := a.ActorName
	if a.ActorKind != "" && a.ActorKind != "user" {
		actor += " (" + a.ActorKind + ")"
	}
	target := strings.TrimSpace(a.TargetKind + " " + a.TargetID)
	return fmt.Sprintf("%-10s  %-22s  %-24s  %s",
		p.relTime(a.CreatedAt), truncate(dash(actor), 22), truncate(a.Action, 24), truncate(dash(target), 34))
}

func auditRows(p *printer, items []auditItem) [][]string {
	rows := make([][]string, 0, len(items))
	for _, a := range items {
		actor := a.ActorName
		if a.ActorKind != "" && a.ActorKind != "user" {
			actor += " (" + a.ActorKind + ")"
		}
		target := a.TargetKind
		if a.TargetID != "" {
			target = strings.TrimSpace(target + " " + a.TargetID)
		}
		rows = append(rows, []string{
			p.relTime(a.CreatedAt),
			dash(actor),
			a.Action,
			dash(target),
			dash(a.IP),
		})
	}
	return rows
}

// auditTail prints the recent rows and then follows the live event stream.
//
// It is `tail -f` for the audit log: the backlog first, so the screen is not
// empty while nothing is happening, then every new row as it is written.
func auditTail(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies audit tail [--limit 10]",
		"Print the most recent audit rows, then follow the live stream until interrupted.")
	cf := registerClientFlags(fs, false)
	limit := fs.Int("limit", 10, "how many recent rows to print before following")
	fs.example("zoomies audit tail", "zoomies audit tail --limit 50")
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
	p, err := newPrinter(e, fs.Name(), outputTable)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%-10s  %-22s  %-24s  %s\n", "WHEN", "ACTOR", "ACTION", "TARGET")

	if *limit > 0 {
		q := url.Values{}
		q.Set("limit", fmt.Sprint(*limit))
		var out listResponse[auditItem]
		if _, err := client.get(ctx, "/audit", q, &out); err != nil {
			return err
		}
		// The list arrives newest first; a tail reads downwards in time.
		items := make([]auditItem, 0, len(out.Items))
		for i := len(out.Items) - 1; i >= 0; i-- {
			items = append(items, out.Items[i])
		}
		for _, a := range items {
			fmt.Fprintln(e.out, auditLine(p, a))
		}
	}

	q := url.Values{}
	q.Set("kinds", "audit")
	resp, err := client.stream(ctx, "/events", q, "text/event-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	err = readSSE(resp.Body, func(f sseFrame) error {
		if f.event != "audit" {
			return nil
		}
		var a auditItem
		if err := json.Unmarshal(f.data, &a); err != nil {
			// One unreadable frame must not end the tail.
			return nil
		}
		fmt.Fprintln(e.out, auditLine(p, a))
		return nil
	})
	// Ending a tail with ctrl-C is how a tail ends, not a failure.
	if stopped(ctx, err) {
		return nil
	}
	return err
}
