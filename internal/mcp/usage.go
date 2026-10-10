package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// Usage and cost are what every hosted service narrates, and what an agent of
// this fleet had no tool for, though the ledger and GET /usage exist. The route
// wants an explicit window and answers with a history of buckets per row; the
// tool supplies the window the way job_stats does and leaves the history out
// unless asked, because a month of buckets per pool is the one part of the
// report a model has no use for and would be cut on.

// usageDefaultWindow is what the tool reads when it is not told: a month, the
// period a bill covers.
const usageDefaultWindow = 30 * 24 * time.Hour

var usageGroups = []string{"pool", "host", "installation", "repository", "workflow"}
var usageIntervals = []string{"hour", "2h", "3h", "4h", "6h", "8h", "12h", "day"}

func usageTools() []*tool {
	return []*tool{
		{
			Name:  "get_usage",
			Title: "Usage and cost",
			Description: "What the fleet did and what it cost over a period, from GET /usage: per pool (or host, installation, repository or workflow) " +
				"the jobs queued, started and completed, their outcomes, execution seconds, the runner seconds allocated to them, the mean queue wait, " +
				"the peak concurrency and estimated_cost. Cost is an estimate from the cost_per_runner_hour an administrator set on each pool, " +
				"and is absent where no rate is set; Zoomies never embeds prices. allocated_runner_seconds and cost are null for the repository and workflow groupings, " +
				"because a runner idles for a pool and never for a repository, so compare repositories by execution and jobs, not by cost. " +
				"history_from says where the job and runner history begins; a window that starts earlier is complete only from there. " +
				"The last thirty days unless since and until say otherwise, and at most 366 days. " +
				"Each row's per-bucket history is left out unless include_history is true; then interval chooses the bucket width. " +
				"Prefer this to job_stats for any question about cost or runner time; job_stats has the percentiles.",
			InputSchema: object(nil, map[string]any{
				"since":           str("start of the window, included: a duration such as 30d or 24h ago, or an RFC 3339 timestamp (default 30d)"),
				"until":           str("end of the window, not included, in the same forms (default now)"),
				"group_by":        enum("what each row is (default pool)", usageGroups...),
				"key":             str("only the row with exactly this key: a pool or host id, an installation, a repository as owner/name or a workflow"),
				"include_history": boolean("true: keep each row's history of buckets over the window (default false)"),
				"interval":        enum("the bucket width when include_history is true; left out, an hour for two days or less and a day beyond", usageIntervals...),
			}),
			Annotations: readOnly,
			call:        getUsage,
		},
	}
}

func getUsage(ctx context.Context, c API, raw json.RawMessage) ([]Content, error) {
	var a struct {
		Since          string `json:"since"`
		Until          string `json:"until"`
		GroupBy        string `json:"group_by"`
		Key            string `json:"key"`
		IncludeHistory bool   `json:"include_history"`
		Interval       string `json:"interval"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.GroupBy != "" && !slices.Contains(usageGroups, a.GroupBy) {
		return nil, fmt.Errorf("group_by %q is not one of pool, host, installation, repository or workflow", a.GroupBy)
	}
	if a.Interval != "" && !slices.Contains(usageIntervals, a.Interval) {
		return nil, fmt.Errorf("interval %q is not one of hour, 2h, 3h, 4h, 6h, 8h, 12h or day", a.Interval)
	}
	q := url.Values{}
	if a.Since == "" {
		a.Since = strconv.Itoa(int(usageDefaultWindow.Hours()/24)) + "d"
	}
	if a.Until == "" {
		a.Until = "0h"
	}
	if err := setUsageWindow(q, a.Since, a.Until); err != nil {
		return nil, err
	}
	q.Set("group_by", "pool")
	if a.GroupBy != "" {
		q.Set("group_by", a.GroupBy)
	}
	if a.Key != "" {
		q.Set("key", a.Key)
	}
	if a.Interval != "" {
		q.Set("interval", a.Interval)
	}
	body, err := c.Call(ctx, "GET", "/usage", q)
	if err != nil {
		return nil, err
	}
	if a.IncludeHistory {
		return jsonContent(body), nil
	}
	return withoutUsageHistory(body)
}

// setUsageWindow is setWindow with the names the usage route takes.
func setUsageWindow(q url.Values, since, until string) error {
	for key, raw := range map[string]string{"from": since, "to": until} {
		when, err := parseWhen(raw)
		if err != nil {
			return fmt.Errorf("%s %q: %w", key, raw, err)
		}
		q.Set(key, when.UTC().Format(time.RFC3339))
	}
	return nil
}

// errUsageShape is a report the tool cannot take apart, refused rather than
// passed on, as the Kennel tools refuse an answer they cannot read.
var errUsageShape = errors.New("the controller's usage report is not in the shape this tool expects; upgrade the controller and the CLI together")

// withoutUsageHistory drops each row's history and keeps everything else as the
// route wrote it, so the envelope the page explains the figures with
// (costs_are_estimates, allocation_attributable, history_from) reaches the
// model untouched.
func withoutUsageHistory(body []byte) ([]Content, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errUsageShape
	}
	var items []map[string]json.RawMessage
	if raw, ok := doc["items"]; !ok || json.Unmarshal(raw, &items) != nil {
		return nil, errUsageShape
	}
	for _, item := range items {
		delete(item, "history")
	}
	trimmed, err := json.Marshal(items)
	if err != nil {
		return nil, errUsageShape
	}
	doc["items"] = trimmed
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, errUsageShape
	}
	return jsonContent(out), nil
}
