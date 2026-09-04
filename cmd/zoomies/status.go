package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// runStatus renders the Overview page in a terminal.
//
// It is four calls to the same routes the UI uses, arranged so that the answer
// to "is my fleet all right?" is the last thing on the screen. When there is
// nothing wrong it says so in one line, because an operator who runs this every
// morning needs the quiet case to be unmistakable.
func runStatus(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies status [--window 1h]", "The Overview, in a terminal: counts, pools, recent scaling and anything wrong.")
	cf := registerClientFlags(fs, true)
	window := fs.String("window", "1h", "the window the completed/failed counts and queue waits cover")
	scalingLimit := fs.Int("scaling", 5, "how many recent scaling decisions to show")
	fs.example("zoomies status", "zoomies status --window 24h", "zoomies status --output json")
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

	var meta metaResponse
	metaRaw, err := client.get(ctx, "/meta", nil, &meta)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("window", *window)
	var stats statsResponse
	statsRaw, err := client.get(ctx, "/stats", q, &stats)
	if err != nil {
		return err
	}

	var problems problemsResponse
	problemsRaw, err := client.get(ctx, "/problems", nil, &problems)
	if err != nil {
		return err
	}

	scalingQuery := url.Values{}
	scalingQuery.Set("limit", fmt.Sprint(max(*scalingLimit, 1)))
	var scaling listResponse[scalingItem]
	scalingRaw, err := client.get(ctx, "/scaling-events", scalingQuery, &scaling)
	if err != nil {
		return err
	}

	if p.structured() {
		// One document rather than four, so `zoomies status --output json`
		// can be piped somewhere without four separate invocations.
		combined, err := json.Marshal(map[string]json.RawMessage{
			"meta":           metaRaw,
			"stats":          statsRaw,
			"problems":       problemsRaw,
			"scaling_events": scalingRaw,
		})
		if err != nil {
			return err
		}
		return p.emit(combined)
	}

	printStatus(p, client.base, meta, stats, scaling.Items, problems)
	return nil
}

func printStatus(p *printer, base string, meta metaResponse, stats statsResponse, scaling []scalingItem, problems problemsResponse) {
	fmt.Fprintf(p.out, "\n%s   %s\n\n", base, p.paint(colourDim, meta.Version))

	window := prettyWindow(stats.Window)
	p.keyValues([][2]string{
		{"jobs", fmt.Sprintf("%d queued, %d running   (%d completed, %d failed in %s)",
			stats.QueuedJobs, stats.RunningJobs, stats.Completed, stats.Failed, window)},
		{"queue wait", fmt.Sprintf("median %s, p95 %s", millis(stats.MedianWaitMS), millis(stats.P95WaitMS))},
		{"runners", fmt.Sprintf("%d live: %d busy, %d idle, %d starting, %d draining, %d failed",
			stats.Runners.Total, stats.Runners.Busy, stats.Runners.Idle,
			stats.Runners.Provisioning+stats.Runners.Registering, stats.Runners.Draining, stats.Runners.Failed)},
		{"hosts", fmt.Sprintf("%d of %d healthy, %d cordoned, %d of %d slots used",
			stats.Hosts.Healthy, stats.Hosts.Total, stats.Hosts.Cordoned, stats.Hosts.Used, stats.Hosts.Capacity)},
	})

	if meta.PollingOnly {
		fmt.Fprintf(p.out, "\n%s\n", p.paint(colourYellow,
			"No webhook has ever arrived: this fleet is scaling on the fallback poller, which is slower."))
	}

	if len(stats.Pools) > 0 {
		fmt.Fprintln(p.out, "\nPools")
		rows := make([][]string, 0, len(stats.Pools))
		for _, pool := range stats.Pools {
			rows = append(rows, []string{
				"  " + pool.PoolName,
				p.bar(pool.Utilisation, 16),
				fmt.Sprintf("%3.0f%%", pool.Utilisation*100),
				fmt.Sprintf("%d/%d runners", pool.Live, pool.Max),
				fmt.Sprintf("%d busy", pool.Busy),
				queuedNote(pool.Queued),
			})
		}
		p.table(nil, rows)
	}

	if len(scaling) > 0 {
		fmt.Fprintln(p.out, "\nRecent scaling")
		for _, ev := range scaling {
			fmt.Fprintf(p.out, "  %-9s %s\n", p.relTime(ev.CreatedAt), ev.Reason)
		}
	}

	fmt.Fprintln(p.out)
	if problems.OK || len(problems.Items) == 0 {
		fmt.Fprintln(p.out, p.paint(colourGreen, "Nothing needs your attention."))
		return
	}
	fmt.Fprintf(p.out, "%d thing(s) need your attention:\n", len(problems.Items))
	for _, item := range problems.Items {
		label := item.Severity
		switch item.Severity {
		case "error":
			label = p.paint(colourRed, label)
		case "warning":
			label = p.paint(colourYellow, label)
		default:
			label = p.paint(colourDim, label)
		}
		setting := ""
		if item.Setting != "" {
			setting = "  (" + item.Setting + ")"
		}
		fmt.Fprintf(p.out, "  [%s] %s%s\n", label, item.Title, setting)
		if item.Detail != "" {
			fmt.Fprintf(p.out, "          %s\n", item.Detail)
		}
		if item.Fix != "" {
			fmt.Fprintf(p.out, "          fix: %s\n", item.Fix)
		}
	}
}

// prettyWindow tidies the duration the server echoes back. Go renders an hour
// as "1h0m0s", which is correct and reads like a stack trace; the operator
// asked for "1h" and should see "1h".
func prettyWindow(raw string) string {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		if raw == "" {
			return "the window"
		}
		return raw
	}
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

// queuedNote keeps the quiet case quiet: a pool with nothing waiting should not
// have a column shouting zero at the operator.
func queuedNote(queued int) string {
	if queued == 0 {
		return ""
	}
	return fmt.Sprintf("%d queued", queued)
}
