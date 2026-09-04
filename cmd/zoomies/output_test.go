package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// fixedNow is the clock the rendering tests measure ages against, so that
// "3m ago" is a fact and not a race with the test runner.
var fixedNow = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

func testPrinter(format string) (*printer, *bytes.Buffer) {
	var buf bytes.Buffer
	return &printer{out: &buf, format: format, colour: false, now: func() time.Time { return fixedNow }}, &buf
}

func TestTableIsAlignedAndUncoloured(t *testing.T) {
	p, buf := testPrinter(outputTable)

	p.table([]string{"name", "state", "age"}, [][]string{
		{"zoomies-linux-x64-a", "busy", "3m ago"},
		{"z", "idle", "1h ago"},
	})

	want := strings.Join([]string{
		"NAME                 STATE  AGE",
		"zoomies-linux-x64-a  busy   3m ago",
		"z                    idle   1h ago",
		"",
	}, "\n")
	if buf.String() != want {
		t.Errorf("table =\n%q\nwant\n%q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Error("a table written to something that is not a terminal must carry no escape codes")
	}
}

func TestTableWidthsIgnoreColourCodes(t *testing.T) {
	// A coloured cell is wider in bytes than it is on screen. Counting the
	// escape codes is exactly how a column comes out ragged when somebody is
	// looking at it.
	p, buf := testPrinter(outputTable)
	p.colour = true

	p.table([]string{"state", "id"}, [][]string{
		{p.state("busy"), "run_a"},
		{p.state("idle"), "run_b"},
	})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	seconds := []string{"ID", "run_a", "run_b"}
	for i, line := range lines {
		stripped := stripANSI(line)
		if got := strings.Index(stripped, seconds[i]); got != 7 {
			t.Errorf("second column of %q starts at %d, want 7", stripped, got)
		}
	}
}

// stripANSI removes escape sequences so a test can measure what a terminal
// would actually show.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		case r == '\x1b':
			inEscape = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestNoTrailingWhitespaceInTables(t *testing.T) {
	p, buf := testPrinter(outputTable)
	p.table([]string{"a", "b"}, [][]string{{"long-value", "x"}, {"y", "another"}})
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line %q has trailing spaces; copying it out of a terminal would pick them up", line)
		}
	}
}

func TestJSONOutputIsTheServersOwnBytes(t *testing.T) {
	p, buf := testPrinter(outputJSON)
	// The field below is one this CLI's structs do not declare. It must still
	// reach the operator: --output json is a passthrough, not a re-encoding.
	raw := []byte(`{"items":[{"id":"pool_1","something_new":42}],"total":1}`)

	if err := p.emit(raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	items := got["items"].([]any)
	if items[0].(map[string]any)["something_new"] != float64(42) {
		t.Errorf("a field this client does not know about was dropped: %v", got)
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Error("JSON output should be indented for a person to read")
	}
}

func TestYAMLOutputKeepsLargeIntegersExact(t *testing.T) {
	// A GitHub run ID decoded as a float64 renders as 1.234567891e+09, which is
	// not a number anybody can paste into a URL.
	p, buf := testPrinter(outputYAML)
	if err := p.emit([]byte(`{"github_run_id":12345678901,"name":"x"}`)); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, buf)
	}
	if fmt.Sprint(got["github_run_id"]) != "12345678901" {
		t.Errorf("github_run_id = %v (%T)", got["github_run_id"], got["github_run_id"])
	}
}

func TestRelativeTimes(t *testing.T) {
	p, _ := testPrinter(outputTable)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "-"},
		{fixedNow, "just now"},
		{fixedNow.Add(-45 * time.Second), "45s ago"},
		{fixedNow.Add(-3 * time.Minute), "3m ago"},
		{fixedNow.Add(-90 * time.Minute), "2h ago"},
		{fixedNow.Add(-72 * time.Hour), "3d ago"},
		{fixedNow.Add(5 * time.Minute), "in 5m"},
		{fixedNow.Add(-365 * 24 * time.Hour), "2025-04-01"},
	}
	for _, c := range cases {
		if got := p.relTime(c.at); got != c.want {
			t.Errorf("relTime(%s) = %q, want %q", c.at, got, c.want)
		}
	}
	if p.relTimePtr(nil) != "-" {
		t.Error("a null timestamp should render as a dash, not an empty cell")
	}
}

func TestMillisAndDisplayWidth(t *testing.T) {
	cases := map[int64]string{0: "-", 250: "250ms", 1500: "1.5s", 95000: "2m"}
	for ms, want := range cases {
		if got := millis(ms); got != want {
			t.Errorf("millis(%d) = %q, want %q", ms, got, want)
		}
	}
	if got := displayWidth(colourise(colourRed, "busy")); got != 4 {
		t.Errorf("displayWidth of a coloured word = %d, want 4", got)
	}
	if got := displayWidth("café"); got != 4 {
		t.Errorf("displayWidth(café) = %d, want 4", got)
	}
}

func TestBarClampsAndFills(t *testing.T) {
	p, _ := testPrinter(outputTable)
	if got := p.bar(0, 4); got != "░░░░" {
		t.Errorf("empty bar = %q", got)
	}
	if got := p.bar(1, 4); got != "████" {
		t.Errorf("full bar = %q", got)
	}
	if got := p.bar(2, 4); got != "████" {
		t.Errorf("a fraction above 1 must clamp, got %q", got)
	}
	if got := p.bar(-1, 4); got != "░░░░" {
		t.Errorf("a negative fraction must clamp, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// The three output modes, end to end
// ---------------------------------------------------------------------------

func poolFixture() string {
	return `{"items":[{"id":"pool_k3f9qz2m","name":"linux-x64","installation_id":"ins_1",
	  "labels":["self-hosted","linux-x64"],"backend":"docker","min_runners":0,"max_runners":8,
	  "idle_timeout":"5m0s","ephemeral":true,"docker_mode":"none","enabled":true,
	  "counts":{"idle":1,"busy":3,"live":4},"queued_jobs":2,"utilisation":0.75}]}`
}

func TestPoolsListInEveryOutputMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pools" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(poolFixture()))
	}))
	defer srv.Close()

	t.Run("table", func(t *testing.T) {
		e, out, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"pools", "list", "--url", srv.URL}); code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, errOut)
		}
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("want a header and one row, got:\n%s", out)
		}
		if !strings.HasPrefix(lines[0], "NAME") {
			t.Errorf("header = %q", lines[0])
		}
		for _, want := range []string{"linux-x64", "pool_k3f9qz2m", "docker", "4/8", "yes"} {
			if !strings.Contains(lines[1], want) {
				t.Errorf("row is missing %q: %q", want, lines[1])
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		e, out, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"pools", "list", "--url", srv.URL, "--output", "json"}); code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, errOut)
		}
		var got listResponse[poolItem]
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		if len(got.Items) != 1 || got.Items[0].Name != "linux-x64" {
			t.Errorf("decoded = %+v", got)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		e, out, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"pools", "list", "--url", srv.URL, "--output", "yaml"}); code != exitOK {
			t.Fatalf("exit code = %d\n%s", code, errOut)
		}
		var got map[string]any
		if err := yaml.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("not YAML: %v\n%s", err, out)
		}
		items, ok := got["items"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("items = %v", got["items"])
		}
	})
}

func TestEmptyListsSayWhatToDoNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	cases := map[string][]string{
		"pools":  {"pools", "list"},
		"hosts":  {"hosts", "list"},
		"tokens": {"tokens", "list"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			e, out, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, append(args, "--url", srv.URL)); code != exitOK {
				t.Fatalf("exit code = %d\n%s", code, errOut)
			}
			if strings.Contains(out.String(), "\n\n") || out.Len() == 0 {
				t.Errorf("an empty list should be one helpful sentence, got:\n%q", out)
			}
		})
	}
}

func TestStatusIsQuietWhenNothingIsWrong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/meta":
			_, _ = w.Write([]byte(`{"version":"1.0.0 (abc1234)","bootstrap_required":false}`))
		case "/api/v1/stats":
			_, _ = w.Write([]byte(`{"window":"1h0m0s","queued_jobs":0,"running_jobs":2,"completed":9,"failed":0,
			  "median_wait_ms":4200,"p95_wait_ms":31000,
			  "runners":{"idle":2,"busy":2,"total":4},"hosts":{"total":2,"healthy":2,"capacity":8,"used":4},
			  "pools":[{"pool_id":"pool_1","pool_name":"linux-x64","max":8,"live":4,"busy":2,"utilisation":0.5}]}`))
		case "/api/v1/problems":
			_, _ = w.Write([]byte(`{"ok":true,"items":[]}`))
		case "/api/v1/scaling-events":
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"status", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	for _, want := range []string{"Nothing needs your attention.", "0 queued, 2 running", "linux-x64", "1h"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "1h0m0s") {
		t.Errorf("the window should be tidied to 1h:\n%s", out)
	}
}

func TestStatusListsProblemsWithTheirFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/problems":
			_, _ = w.Write([]byte(`{"ok":false,"items":[{"code":"bind.public_no_tls","severity":"warning",
			  "setting":"server.bind","title":"listening on 0.0.0.0:8080 without TLS",
			  "detail":"credentials cross the network in cleartext.","fix":"put a reverse proxy in front."}]}`))
		case "/api/v1/scaling-events":
			_, _ = w.Write([]byte(`{"items":[{"id":"s1","pool_name":"linux-x64","from":2,"to":4,
			  "reason":"scaled linux-x64 2 -> 4: 3 jobs queued > 30s","created_at":"2026-04-01T11:57:00Z"}]}`))
		case "/api/v1/meta":
			_, _ = w.Write([]byte(`{"version":"1.0.0","polling_only":true}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"status", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	for _, want := range []string{
		"1 thing(s) need your attention",
		"listening on 0.0.0.0:8080 without TLS",
		"server.bind",
		"fix: put a reverse proxy in front.",
		"scaled linux-x64 2 -> 4",
		"fallback poller",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "Nothing needs your attention") {
		t.Error("status claimed all was well while listing a problem")
	}
}
