package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseClientFlags builds the flags a fleet command declares and parses args
// into them, which is how the resolution order can be tested without going
// through a whole command.
func parseClientFlags(t *testing.T, e *env, args ...string) *clientFlags {
	t.Helper()
	fs := newFlagSet(e, "zoomies test", "")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	return cf
}

func writeCLIConfig(t *testing.T, body string) {
	t.Helper()
	path := cliConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsPreferFlagsThenEnvironmentThenFile(t *testing.T) {
	t.Run("the file is the last resort", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		writeCLIConfig(t, "url: https://from-file.example.com\ntoken: tok_file\n")

		client, err := parseClientFlags(t, e).client()
		if err != nil {
			t.Fatal(err)
		}
		if client.base != "https://from-file.example.com" || client.token != "tok_file" {
			t.Errorf("got %s / %s, want the file's values", client.base, client.token)
		}
	})

	t.Run("the environment beats the file", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		writeCLIConfig(t, "url: https://from-file.example.com\ntoken: tok_file\n")
		t.Setenv("ZOOMIES_URL", "https://from-env.example.com")
		t.Setenv("ZOOMIES_TOKEN", "tok_env")

		client, err := parseClientFlags(t, e).client()
		if err != nil {
			t.Fatal(err)
		}
		if client.base != "https://from-env.example.com" || client.token != "tok_env" {
			t.Errorf("got %s / %s, want the environment's values", client.base, client.token)
		}
	})

	t.Run("a flag beats both", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		writeCLIConfig(t, "url: https://from-file.example.com\ntoken: tok_file\n")
		t.Setenv("ZOOMIES_URL", "https://from-env.example.com")
		t.Setenv("ZOOMIES_TOKEN", "tok_env")

		client, err := parseClientFlags(t, e, "--url", "https://from-flag.example.com", "--token", "tok_flag").client()
		if err != nil {
			t.Fatal(err)
		}
		if client.base != "https://from-flag.example.com" || client.token != "tok_flag" {
			t.Errorf("got %s / %s, want the flags' values", client.base, client.token)
		}
	})

	t.Run("a bare host is assumed to be https", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		client, err := parseClientFlags(t, e, "--url", "zoomies.example.com:8080").client()
		if err != nil {
			t.Fatal(err)
		}
		if client.base != "https://zoomies.example.com:8080" {
			t.Errorf("base = %q; a missing scheme must not silently become http", client.base)
		}
	})
}

func TestMissingURLNamesAllThreePlaces(t *testing.T) {
	e, _, _ := newTestEnv(t)

	_, err := parseClientFlags(t, e).client()
	if err == nil {
		t.Fatal("no error for a missing controller URL")
	}
	for _, want := range []string{"--url", "ZOOMIES_URL", cliConfigPath()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%s", want, err)
		}
	}
}

func TestUnauthorizedSaysHowToMintAToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"sign in first"}}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "list", "--url", srv.URL}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{"--token", "ZOOMIES_TOKEN", cliConfigPath(), "zoomies tokens create"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("a 401 does not explain %q:\n%s", want, errOut)
		}
	}
}

func TestUnprocessableShowsEveryFieldError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"unprocessable","message":"that pool is not valid"},
			"errors":[{"field":"labels","message":"at least one label is needed"},
			          {"field":"max_runners","message":"must be at least 1"}]}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{
		"pools", "create", "--url", srv.URL,
		"--name", "x", "--labels", "y", "--installation", "i",
	})
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{"that pool is not valid", "labels: at least one label is needed", "max_runners: must be at least 1"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("missing %q in:\n%s", want, errOut)
		}
	}
}

func TestUnreachableControllerNamesTheAddress(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	// Port 1 is reserved and nothing listens on it, so this is a dial failure
	// rather than a timeout.
	if code := dispatch(context.Background(), e, []string{"pools", "list", "--url", "http://127.0.0.1:1"}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "http://127.0.0.1:1") || !strings.Contains(errOut.String(), "ZOOMIES_URL") {
		t.Errorf("a dial failure must name the address and how it was chosen:\n%s", errOut)
	}
}

func TestRepeatableFiltersAreSentAsRepeatedKeys(t *testing.T) {
	// The OpenAPI document declares these as style: form, explode: true. A
	// comma-joined value would be one filter the server does not recognise.
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total":0,"limit":50,"offset":0}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{
		"runners", "list", "--url", srv.URL,
		"--state", "busy", "--state", "idle,draining",
		"--pool", "pool_a",
	})
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if want := []string{"busy", "idle", "draining"}; !equalStrings(got["state"], want) {
		t.Errorf("state = %v, want %v", got["state"], want)
	}
	if !equalStrings(got["pool_id"], []string{"pool_a"}) {
		t.Errorf("pool_id = %v", got["pool_id"])
	}
}

func TestBearerTokenIsSentAndNoOriginIsNeeded(t *testing.T) {
	var auth, contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pool_1","name":"x"}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{
		"pools", "create", "--url", srv.URL, "--token", "zoo_secret",
		"--name", "x", "--labels", "l", "--installation", "i",
	})
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if auth != "Bearer zoo_secret" {
		t.Errorf("Authorization = %q", auth)
	}
	if contentType != "application/json" {
		t.Errorf("mutating requests must declare their content type, got %q", contentType)
	}
}

func TestEditSendsOnlyTheFlagsThatWereTyped(t *testing.T) {
	// A PATCH built from flag defaults would quietly reset every setting the
	// operator did not mention, which is the difference between "raise the
	// maximum" and "rebuild the pool".
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		decodeJSON(t, r, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pool_1","name":"x"}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "edit", "pool_1", "--url", srv.URL, "--max", "12"}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if len(body) != 1 {
		t.Fatalf("PATCH body = %v, want only max_runners", body)
	}
	if body["max_runners"] != float64(12) {
		t.Errorf("max_runners = %v", body["max_runners"])
	}
}

func TestSSEFramesAreParsed(t *testing.T) {
	stream := strings.Join([]string{
		": connected",
		"",
		"retry: 2000",
		"",
		"id: 7",
		"event: log",
		`data: "first line\n"`,
		"",
		"event: multi",
		"data: {",
		`data:   "a": 1`,
		"data: }",
		"",
		"event: end",
		`data: {"reason":"the runner's output ended"}`,
		"",
	}, "\n")

	var kinds []string
	var payloads []string
	err := readSSE(strings.NewReader(stream), func(f sseFrame) error {
		kinds = append(kinds, f.event)
		payloads = append(payloads, string(f.data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(kinds, []string{"log", "multi", "end"}) {
		t.Fatalf("events = %v", kinds)
	}
	if payloads[0] != `"first line\n"` {
		t.Errorf("log payload = %q", payloads[0])
	}
	if payloads[1] != "{\n  \"a\": 1\n}" {
		t.Errorf("a payload split over several data: lines was not rejoined: %q", payloads[1])
	}
}

func TestFollowedLogsPrintChunksAndStopAtTheEndEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("follow") != "true" || q.Get("tail") != "5" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: log\ndata: \"building...\\n\"\n\nevent: log\ndata: \"done\\n\"\n\nevent: end\ndata: {}\n\n"))
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"runners", "logs", "run_1", "--url", srv.URL, "--follow", "--tail", "5"})
	if code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if out.String() != "building...\ndone\n" {
		t.Errorf("output = %q", out)
	}
}

func TestLogsWithoutFollowUsesTheDownloadRoute(t *testing.T) {
	// A snapshot has to terminate. The live stream never does on its own, so
	// asking for one and guessing when to stop would truncate the file.
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("the whole log\n"))
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"runners", "logs", "run_1", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if path != "/api/v1/runners/run_1/logs/download" {
		t.Errorf("path = %q", path)
	}
	if out.String() != "the whole log\n" {
		t.Errorf("output = %q", out)
	}
}

func TestBulkRunnerActionsReportEachID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/bulk" {
			t.Errorf("path = %q; several IDs must use the bulk route", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"run_a","ok":true},{"id":"run_b","ok":false,"error":"already removed"}]}`))
	}))
	defer srv.Close()

	e, out, _ := newTestEnv(t)
	// A partial failure is a failure: the exit code has to say so.
	if code := dispatch(context.Background(), e, []string{"runners", "drain", "run_a", "run_b", "--url", srv.URL}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{"run_a", "run_b", "already removed", "1 of 2 succeeded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// decodeJSON reads a request body in a test handler, failing the test rather
// than the request when it is not what this CLI promised to send.
func decodeJSON(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("decoding the request body: %v", err)
	}
}
