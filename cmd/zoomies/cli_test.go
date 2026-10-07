package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// The password never appears in argv: it is typed at a prompt, or piped in.
// A --password flag would put a live credential in /proc/<pid>/cmdline, where
// every account on the host can read it, and in the shell history after that.
func TestUsersPasswdTakesTheSecretFromStandardInputNotAFlag(t *testing.T) {
	var body map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		decodeJSON(t, r, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	e.in = strings.NewReader("a-long-enough-password\n")
	if code := dispatch(context.Background(), e, []string{"users", "passwd", "usr_7f3a", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if path != "/api/v1/users/usr_7f3a/password" {
		t.Errorf("path = %q", path)
	}
	if body["new_password"] != "a-long-enough-password" {
		t.Errorf("body = %v, want the password read from stdin", body)
	}
	if !strings.Contains(out.String(), "usr_7f3a") {
		t.Errorf("output = %q, want it to name the account", out)
	}

	// Nothing piped in and no terminal to prompt on is a usage error, not an
	// empty password sent to the server.
	e2, _, _ := newTestEnv(t)
	if code := dispatch(context.Background(), e2, []string{"users", "passwd", "usr_7f3a", "--url", srv.URL}); code != exitUsage {
		t.Errorf("exit code with no password = %d, want a usage error", code)
	}
}

// A lost phone is fixed from a terminal as well as from the users page,
// because the administrator who needs to fix it may be the one who lost it
// and still have an API token.
func TestUsersResetTwoStepDeletesTheAccountsSecondFactor(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"users", "reset-two-step", "usr_7f3a", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if method != http.MethodDelete || path != "/api/v1/users/usr_7f3a/two-step" {
		t.Errorf("request = %s %s", method, path)
	}
	if !strings.Contains(out.String(), "usr_7f3a") {
		t.Errorf("output = %q, want it to name the account", out)
	}
}

// `zoomies jobs get` prints the controller's explanation rather than reasoning
// its way to one.
//
// It used to have a paragraph of its own for a queued job nothing claims, which
// could see the job row and nothing else -- not the scheduler's reason for
// failing to place a runner, and not the host under the runner. The web UI had
// a different paragraph for the same question. This replaces that test: the two
// cases it covered are still told apart, and now by the side that can see the
// fleet, so the two surfaces cannot disagree.
func TestJobsGetPrintsTheControllersExplanation(t *testing.T) {
	tests := []struct {
		name        string
		explanation string
		want        []string
		notWant     string
	}{
		{
			name: "no installation covers the repository",
			explanation: `{"summary":"No pool in this fleet claims this job.",
				"detail":"No GitHub App installation here covers acme/widgets.",
				"fix":"install the App on that organisation, then add it.","waiting":true,"blocked":true}`,
			want:    []string{"No pool in this fleet claims this job.", "No GitHub App installation here covers acme/widgets.", "install the App on that organisation"},
			notWant: "runs-on",
		},
		{
			name: "the scheduler cannot place a runner",
			explanation: `{"summary":"The scheduler wants a runner for this job and cannot place one.",
				"detail":"no host can take a new docker runner (2 not matching the pool's host selector)",
				"fix":"relax the pool's host selector.","waiting":true,"blocked":true}`,
			want:    []string{"cannot place one", "not matching the pool's host selector", "relax the pool's host selector"},
			notWant: "No enabled pool claims",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/explanation"):
					_, _ = w.Write([]byte(tc.explanation))
				case strings.HasSuffix(r.URL.Path, "/events"):
					_, _ = w.Write([]byte(`{"items":[],"total":0,"limit":50,"offset":0}`))
				default:
					_, _ = fmt.Fprint(w, `{"id":"job_1","repo":"acme/widgets","workflow":"CI","job_name":"build",
						"labels":["self-hosted","linux","x64"],"state":"queued","matched":false,
						"queued_at":"2026-04-01T11:00:00Z","steps":[]}`)
				}
			}))
			defer srv.Close()

			e, out, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, []string{
				"jobs", "get", "job_1", "--url", srv.URL,
			}); code != exitOK {
				t.Fatalf("exit code = %d\n%s", code, errOut)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output does not carry %q from the explanation:\n%s", want, out)
				}
			}
			// The CLI must not still be writing its own answer beside the
			// controller's; two answers is what this replaced.
			if strings.Contains(out.String(), tc.notWant) {
				t.Errorf("output still carries the CLI's own reasoning %q:\n%s", tc.notWant, out)
			}
		})
	}
}

// On the controller's own host, a CLI told nothing finds the controller installed
// there, says where it got the address, and never prefers it over something the
// operator did say.
func TestAHostWithAnInstalledControllerNeedsNoURL(t *testing.T) {
	configDir := func(t *testing.T, yaml string) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "zoomies.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZOOMIES_CONFIG_DIR", dir)
	}

	t.Run("it falls back to the installed controller", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		configDir(t, "server:\n  external_url: https://zoomies.example.com\n")
		client, err := parseClientFlags(t, e).client()
		if err != nil {
			t.Fatal(err)
		}
		if client.base != "https://zoomies.example.com" {
			t.Errorf("base = %q, want the installed controller's address", client.base)
		}
	})

	t.Run("a flag, the environment and the file all beat it", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		configDir(t, "server:\n  external_url: https://installed.example.com\n")
		writeCLIConfig(t, "url: https://from-file.example.com\n")
		if c, _ := parseClientFlags(t, e).client(); c == nil || c.base != "https://from-file.example.com" {
			t.Errorf("the CLI file must beat the installed controller, got %+v", c)
		}
		t.Setenv("ZOOMIES_URL", "https://from-env.example.com")
		if c, _ := parseClientFlags(t, e).client(); c == nil || c.base != "https://from-env.example.com" {
			t.Errorf("the environment must beat it, got %+v", c)
		}
		if c, _ := parseClientFlags(t, e, "--url", "https://from-flag.example.com").client(); c == nil || c.base != "https://from-flag.example.com" {
			t.Errorf("a flag must beat it, got %+v", c)
		}
	})

	t.Run("no installed controller is no guess", func(t *testing.T) {
		e, _, _ := newTestEnv(t) // an empty configuration directory
		_, err := parseClientFlags(t, e).client()
		if err == nil || !strings.Contains(err.Error(), "no controller configuration was found") {
			t.Errorf("a host with no controller must be told so, not sent to a default address: %v", err)
		}
	})
}

// A certificate the CLI does not trust, and an https address answered in plain HTTP, both used
// to read as "check that it is running", which sent an operator to a controller that was up.
func TestTransportErrorNamesTheFlagThatFixesACertificateOrSchemeMistake(t *testing.T) {
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tls.Close()
	e, outBuf, errBuf := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"problems", "list", "--url", tls.URL})
	out, errOut := outBuf.String(), errBuf.String()
	if code == exitOK {
		t.Fatalf("a self-signed controller was trusted:\n%s", out)
	}
	for _, want := range []string{"certificate is not trusted", "--ca-file", "ca_file"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the certificate error does not say %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "check that it is running") {
		t.Errorf("a controller that answered was described as not running:\n%s", errOut)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	e, _, errBuf = newTestEnv(t)
	code = dispatch(context.Background(), e, []string{"problems", "list", "--url", "https://" + strings.TrimPrefix(plain.URL, "http://")})
	errOut = errBuf.String()
	if code == exitOK {
		t.Fatal("plain HTTP was accepted for an https address")
	}
	for _, want := range []string{"plain HTTP", "http://"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the scheme error does not say %q:\n%s", want, errOut)
		}
	}
}

// A host that joined a controller as an agent has a zoomies.yaml too, and what it
// names is the controller it joined. The CLI used to read it for a listener of its
// own, found the default one on loopback, and sent `zoomies doctor --host` there:
// to an address nothing on that host answers.
func TestAnAgentHostFindsTheControllerItJoined(t *testing.T) {
	configDir := func(t *testing.T, yaml string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "zoomies.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZOOMIES_CONFIG_DIR", dir)
		return filepath.Join(dir, "zoomies.yaml")
	}

	t.Run("it uses the controller the agent joined, and says where that came from", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		file := configDir(t, "agent:\n  embedded: false\n  controller_url: https://zoomies.example.com\n")
		client, err := parseClientFlags(t, e).client()
		if err != nil {
			t.Fatal(err)
		}
		if client.base != "https://zoomies.example.com" {
			t.Errorf("base = %q, want the controller this host joined and not a listener on loopback", client.base)
		}
		if client.source != file {
			t.Errorf("source = %q, want the file it was read from, %q", client.source, file)
		}
	})

	t.Run("a private connection is said to be one, not guessed at", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		file := configDir(t, "agent:\n  embedded: false\n  controller_url: tailcat://controller\n")
		_, err := parseClientFlags(t, e).client()
		if err == nil {
			t.Fatal("a controller reached through a tunnel was given an address")
		}
		for _, want := range []string{file, "private connection", "--url"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not mention %q:\n%s", want, err)
			}
		}
		if strings.Contains(err.Error(), "no controller configuration was found") {
			t.Errorf("the file is there, and the error says it is not:\n%s", err)
		}
	})
}

// "Cannot reach the controller" is the error an operator meets when the address is
// wrong, and an address they never typed is the one they cannot fix, so it says
// where it came from.
func TestTheUnreachableErrorSaysWhereTheAddressCameFrom(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := gone.URL
	gone.Close()

	t.Run("a flag", func(t *testing.T) {
		e, _, errOut := newTestEnv(t)
		dispatch(context.Background(), e, []string{"pools", "list", "--url", address})
		if !strings.Contains(errOut.String(), "the address came from --url") {
			t.Errorf("the error does not say where the address came from:\n%s", errOut)
		}
	})
	t.Run("the environment", func(t *testing.T) {
		e, _, errOut := newTestEnv(t)
		t.Setenv("ZOOMIES_URL", address)
		dispatch(context.Background(), e, []string{"pools", "list"})
		if !strings.Contains(errOut.String(), "the address came from ZOOMIES_URL") {
			t.Errorf("the error does not say where the address came from:\n%s", errOut)
		}
	})
	t.Run("the connection file", func(t *testing.T) {
		e, _, errOut := newTestEnv(t)
		writeCLIConfig(t, "url: "+address+"\n")
		dispatch(context.Background(), e, []string{"pools", "list"})
		if !strings.Contains(errOut.String(), "the address came from "+cliConfigPath()) {
			t.Errorf("the error does not say where the address came from:\n%s", errOut)
		}
	})
}
