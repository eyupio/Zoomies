package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// usageDoc is a usage report with one row that carries a history, a cost and an
// allocation, and the envelope the page explains the figures with.
func usageDoc() string {
	return `{"from":"2026-09-10T00:00:00Z","to":"2026-10-10T00:00:00Z","group_by":"pool","costs_are_estimates":true,"allocation_attributable":true,
	  "history_from":{"jobs":"2026-09-10T00:00:00Z","runners":null},
	  "items":[{"key":"linux-small","history":[{"from":"2026-09-10T00:00:00Z","queued":3,"started":3,"succeeded":3,"failed":0,"cancelled":0,"unknown":0,"execution_seconds":120,"allocated_seconds":600,"capacity_samples":10,"capacity_reached":0}],
	    "succeeded":40,"failed":2,"cancelled":1,"unknown":0,"job_execution_seconds":8000,"allocated_runner_seconds":20000,"jobs":43,"jobs_started":43,"jobs_completed":43,"average_queue_wait_seconds":4.5,"peak_concurrency":3,"estimated_cost":12.5}]}`
}

// Usage and cost are what every hosted service narrates and what an agent had
// no tool for, though the ledger and the route exist. Without arguments the tool
// reads the last thirty days by pool; the per-bucket history is left out, because
// a month of it per row is the one part of the report a model has no use for and
// would be cut on.
func TestUsageReadsThirtyDaysByPoolWithoutTheHistory(t *testing.T) {
	api := &kennelAPI{body: usageDoc()}
	out, err := kennelCall(t, kennelTool(t, "get_usage").call, api, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(api.got, "GET /usage") {
		t.Errorf("asked %q", api.got)
	}
	from, err := time.Parse(time.RFC3339, api.query.Get("from"))
	if err != nil {
		t.Fatalf("from = %q: %v", api.query.Get("from"), err)
	}
	if ago := time.Since(from); ago < 29*24*time.Hour || ago > 31*24*time.Hour {
		t.Errorf("from is %s ago, want thirty days", ago)
	}
	if to, err := time.Parse(time.RFC3339, api.query.Get("to")); err != nil || time.Since(to) > time.Minute {
		t.Errorf("to = %q, want now", api.query.Get("to"))
	}
	if api.query.Get("group_by") != "pool" {
		t.Errorf("group_by = %q", api.query.Get("group_by"))
	}
	if len(out) != 1 {
		t.Fatalf("blocks = %+v", out)
	}
	text := out[0].Text
	for _, want := range []string{`"estimated_cost":12.5`, `"allocation_attributable":true`, `"costs_are_estimates":true`, `"history_from"`, `"key":"linux-small"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lost %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"history"`) || strings.Contains(text, "capacity_samples") {
		t.Errorf("the per-bucket history is in the answer: %s", text)
	}
}

// A grouping, a key and a window are passed on as the route takes them, and the
// history comes when it is asked for, with its bucket width.
func TestUsageTakesTheRoutesFiltersAndCanKeepTheHistory(t *testing.T) {
	api := &kennelAPI{body: usageDoc()}
	out, err := kennelCall(t, kennelTool(t, "get_usage").call, api, `{"group_by":"repository","key":"acme/app","since":"7d","interval":"day","include_history":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if api.query.Get("group_by") != "repository" || api.query.Get("key") != "acme/app" || api.query.Get("interval") != "day" {
		t.Errorf("query = %v", api.query)
	}
	if from, _ := time.Parse(time.RFC3339, api.query.Get("from")); time.Since(from) > 8*24*time.Hour {
		t.Errorf("from = %q, want a week ago", api.query.Get("from"))
	}
	if !strings.Contains(out[0].Text, "capacity_samples") {
		t.Errorf("the history was asked for and left out: %s", out[0].Text)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(out[0].Text), &doc) != nil {
		t.Errorf("not one JSON document: %s", out[0].Text)
	}
}

// A grouping the route would refuse is refused here, before anything is asked.
func TestUsageRefusesAGroupingTheRouteDoesNotHave(t *testing.T) {
	api := &kennelAPI{body: usageDoc()}
	if _, err := kennelCall(t, kennelTool(t, "get_usage").call, api, `{"group_by":"user"}`); err == nil || api.calls != 0 {
		t.Errorf("err %v, calls %d", err, api.calls)
	}
}
