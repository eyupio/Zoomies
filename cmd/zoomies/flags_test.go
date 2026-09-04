package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestListValueIsRepeatableAndCommaSeparated(t *testing.T) {
	var v listValue
	for _, arg := range []string{"busy", "idle,draining", " spaced , ", ""} {
		if err := v.Set(arg); err != nil {
			t.Fatalf("Set(%q): %v", arg, err)
		}
	}
	if !equalStrings(v, []string{"busy", "idle", "draining", "spaced"}) {
		t.Errorf("collected %v", v)
	}
	if v.String() != "busy,idle,draining,spaced" {
		t.Errorf("String() = %q", v.String())
	}
}

func TestKVValueParsesPairsAndRejectsNonsense(t *testing.T) {
	m := kvValue{}
	if err := m.Set("arch=arm64,tier=fast"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(" region = eu-west-1 "); err != nil {
		t.Fatal(err)
	}
	if m["arch"] != "arm64" || m["tier"] != "fast" || m["region"] != "eu-west-1" {
		t.Errorf("parsed %v", m)
	}
	if m.String() != "arch=arm64,region=eu-west-1,tier=fast" {
		t.Errorf("String() = %q; it should be stable so a default is printable", m.String())
	}
	if err := m.Set("justakey"); err == nil {
		t.Error("a value with no = was accepted")
	} else if !strings.Contains(err.Error(), "key=value") {
		t.Errorf("the error does not say what the form is: %v", err)
	}
}

func TestFlagsMayFollowPositionalArguments(t *testing.T) {
	// `zoomies runners drain run_a --url ...` has to work: the alternative is
	// a URL silently treated as a second runner ID.
	e, _, _ := newTestEnv(t)
	fs := newFlagSet(e, "zoomies test <id>...", "")
	name := fs.String("name", "", "")
	force := fs.Bool("force", false, "")
	count := fs.Int("count", 0, "")

	if err := fs.parse([]string{"first", "--name", "given", "second", "--force", "--count=3", "third"}); err != nil {
		t.Fatal(err)
	}
	if *name != "given" || !*force || *count != 3 {
		t.Errorf("flags = %q %v %d", *name, *force, *count)
	}
	if !equalStrings(fs.Args(), []string{"first", "second", "third"}) {
		t.Errorf("positional = %v", fs.Args())
	}
}

func TestDoubleDashEndsFlagParsing(t *testing.T) {
	e, _, _ := newTestEnv(t)
	fs := newFlagSet(e, "zoomies test", "")
	fs.String("name", "", "")

	if err := fs.parse([]string{"--name", "x", "--", "--not-a-flag"}); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(fs.Args(), []string{"--not-a-flag"}) {
		t.Errorf("positional = %v", fs.Args())
	}
}

func TestChangedReportsOnlyWhatWasTyped(t *testing.T) {
	e, _, _ := newTestEnv(t)
	fs := newFlagSet(e, "zoomies test", "")
	fs.Int("max", 4, "")
	fs.Int("min", 0, "")

	if err := fs.parse([]string{"--max", "4"}); err != nil {
		t.Fatal(err)
	}
	// Typing the default value still counts as typing it: the operator asked
	// for that value, and a PATCH should say so.
	if !fs.changed("max") {
		t.Error("--max was given and should count as changed")
	}
	if fs.changed("min") {
		t.Error("--min was never given")
	}
}

func TestPageFlagsSendOnlyWhatWasSet(t *testing.T) {
	e, _, _ := newTestEnv(t)
	fs := newFlagSet(e, "zoomies test", "")
	page := registerPageFlags(fs, 50)
	if err := fs.parse([]string{"--offset", "100", "--order", "asc"}); err != nil {
		t.Fatal(err)
	}

	q := url.Values{}
	page.apply(q)
	if q.Get("limit") != "50" || q.Get("offset") != "100" || q.Get("order") != "asc" {
		t.Errorf("query = %v", q)
	}
	if _, ok := q["sort"]; ok {
		t.Error("an empty sort must not be sent; the server would have to guess what it meant")
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "list", "--wibble"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "wibble") {
		t.Errorf("the unknown flag was not named:\n%s", errOut)
	}
}

func TestJobsListTranslatesSinceIntoATimestamp(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{
		"jobs", "list", "--url", srv.URL, "--repo", "acme/widgets", "--since", "24h", "--unmatched",
	})
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if !equalStrings(query["repo"], []string{"acme/widgets"}) {
		t.Errorf("repo = %v", query["repo"])
	}
	if query.Get("unmatched") != "true" {
		t.Errorf("unmatched = %q", query.Get("unmatched"))
	}
	when, err := time.Parse(time.RFC3339, query.Get("since"))
	if err != nil {
		t.Fatalf("since = %q, which is not RFC 3339: %v", query.Get("since"), err)
	}
	if age := time.Since(when); age < 23*time.Hour || age > 25*time.Hour {
		t.Errorf("--since 24h produced %s, which is %s ago", when, age)
	}
}

func TestSinceRejectsNonsenseBeforeCallingTheServer(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"jobs", "list", "--url", "http://127.0.0.1:1", "--since", "yesterday"})
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "duration like 24h") {
		t.Errorf("the error does not say what is accepted:\n%s", errOut)
	}
}

func TestJoinTokenCreateSendsTTLAndPrintsTheCommandOnce(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "jt_1",
			"prefix":     "zoojoin_abc",
			"capacity":   8,
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"token":      "zoojoin_abc_secret",
			"command":    "curl -fsSL https://zoomies.sh/install.sh | sh -s -- --join-token zoojoin_abc_secret",
		})
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{
		"hosts", "join-token", "create", "--url", srv.URL, "--ttl", "1h", "--capacity", "8", "--labels", "arch=arm64",
	})
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if body["ttl"] != "1h0m0s" || body["capacity"] != float64(8) {
		t.Errorf("request body = %v", body)
	}
	labels, _ := body["labels"].(map[string]any)
	if labels["arch"] != "arm64" {
		t.Errorf("labels = %v", body["labels"])
	}
	for _, want := range []string{"zoojoin_abc_secret", "install.sh", "only time the token is shown"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestUsersCreateOmitsAnEmptyPassword(t *testing.T) {
	// An account meant for single sign-on must not be sent an empty password
	// field, which the server would have to interpret.
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"usr_1","username":"alex","role":"operator"}`))
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"users", "create", "--url", srv.URL, "--username", "alex", "--role", "operator"})
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if _, present := body["password"]; present {
		t.Errorf("an empty password was sent: %v", body)
	}
	if !strings.Contains(out.String(), "single sign-on") {
		t.Errorf("the operator was not told the account has no password:\n%s", out)
	}
}

func TestDeleteUsesTheRightMethodAndQuery(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantPath   string
		wantMethod string
		wantQuery  string
	}{
		{"runner", []string{"runners", "delete", "run_1", "--force"}, "/api/v1/runners/run_1", http.MethodDelete, "force=true"},
		{"host", []string{"hosts", "delete", "host_1"}, "/api/v1/hosts/host_1", http.MethodDelete, ""},
		{"token", []string{"tokens", "revoke", "tok_1"}, "/api/v1/tokens/tok_1", http.MethodDelete, ""},
		{"user", []string{"users", "delete", "usr_1"}, "/api/v1/users/usr_1", http.MethodDelete, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath, gotMethod, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod, gotQuery = r.URL.Path, r.Method, r.URL.RawQuery
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			e, _, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, append(c.args, "--url", srv.URL)); code != exitOK {
				t.Fatalf("exit code = %d\n%s", code, errOut)
			}
			if gotPath != c.wantPath || gotMethod != c.wantMethod || gotQuery != c.wantQuery {
				t.Errorf("%s %s?%s, want %s %s?%s", gotMethod, gotPath, gotQuery, c.wantMethod, c.wantPath, c.wantQuery)
			}
		})
	}
}

func TestAuditTailPrintsTheBacklogThenFollows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/audit":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[
			  {"id":"a2","actor_name":"bob","actor_kind":"user","action":"runner.drain","target_kind":"runner","target_id":"run_2","created_at":"2026-04-01T11:59:00Z"},
			  {"id":"a1","actor_name":"alice","actor_kind":"user","action":"pool.create","target_kind":"pool","target_id":"pool_1","created_at":"2026-04-01T11:58:00Z"}
			],"total":2}`))
		case "/api/v1/events":
			if kinds := r.URL.Query().Get("kinds"); kinds != "audit" {
				t.Errorf("kinds = %q; the tail should ask for audit events only", kinds)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: audit\ndata: {\"id\":\"a3\",\"actor_name\":\"carol\",\"action\":\"pool.delete\",\"target_kind\":\"pool\",\"target_id\":\"pool_9\",\"created_at\":\"2026-04-01T12:00:00Z\"}\n\n"))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"audit", "tail", "--url", srv.URL, "--limit", "2"}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	body := out.String()
	// Oldest first: a tail reads downwards in time.
	if i, j := strings.Index(body, "pool.create"), strings.Index(body, "runner.drain"); i < 0 || j < 0 || i > j {
		t.Errorf("the backlog is not oldest-first:\n%s", body)
	}
	if !strings.Contains(body, "pool.delete") || !strings.Contains(body, "carol") {
		t.Errorf("the live event was not printed:\n%s", body)
	}
	if !strings.HasPrefix(body, "WHEN") {
		t.Errorf("no header:\n%s", body)
	}
}

func TestInstallationVerifyFailsLoudlyAndNamesMissingPermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"message":"the App cannot register runners",
		  "missing_permissions":["administration:write"],"missing_events":["workflow_job"]}`))
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"installations", "verify", "ins_1", "--url", srv.URL}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{"administration:write", "workflow_job"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut.String(), "cannot be used") {
		t.Errorf("the failure was not reported:\n%s", errOut)
	}
}
