package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/updates/channel"
)

// withHelperStateDir points the helper's commands at a state directory of the
// test's instead of root's.
func withHelperStateDir(t *testing.T, dir string) {
	t.Helper()
	was := updateHelperStateDir
	updateHelperStateDir = dir
	t.Cleanup(func() { updateHelperStateDir = was })
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// When the new controller never comes up, nothing in the UI can say what
// happened, and this is what an operator on the host reads instead. What it
// prints from the folder was written by the service, so it is printed as text
// and never as terminal control.
func TestHelperStatusNamesTheFolderAndTheLastResult(t *testing.T) {
	stateDir := t.TempDir()
	withHelperStateDir(t, stateDir)
	folder := filepath.Join(t.TempDir(), "update")
	if err := os.Mkdir(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(stateDir, channel.PointerFile), channel.Pointer{
		V: 1, Dir: folder, Binary: "/usr/local/bin/zoomies", Account: "zoomies", UID: 999, ConfigDir: "/etc/zoomies",
	})
	if err := os.WriteFile(filepath.Join(folder, channel.MarkerFile),
		[]byte(`{"v":1,"version":"1.3.4","binary":"/usr/local/bin/zoomies","installed_at":"2026-10-01T09:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, channel.ResultFile), []byte(`{"v":1,"id":"upd_k3fqz2mx7abcd","ok":false,"tag":"v1.3.5","from":"1.3.4","to":"",`+
		`"error":"the upgrade cannot go on without this: the shared folder\u001b[2K","log_tail":"line one\nline two\u001b]0;pwned\u0007",`+
		`"started_at":"2026-10-08T09:00:00Z","finished_at":"2026-10-08T09:02:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := runCLI(t, "updates", "helper", "status")
	for _, want := range []string{folder, "upd_k3fqz2mx7abcd", "v1.3.5", "the upgrade cannot go on without this: the shared folder", "1.3.4", "line two"} {
		if !strings.Contains(out, want) {
			t.Errorf("status does not say %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("status printed terminal control from the folder:\n%q", out)
	}
}

func TestHelperStatusSaysWhenTheHelperIsNotInstalled(t *testing.T) {
	withHelperStateDir(t, filepath.Join(t.TempDir(), "zoomies-update"))
	t.Setenv("ZOOMIES_CONFIG_DIR", t.TempDir())
	out, _ := runCLI(t, "updates", "helper", "status")
	if !strings.Contains(out, "not installed") || !strings.Contains(out, "updates helper install") {
		t.Errorf("status should say the helper is not installed and how to install it:\n%s", out)
	}
}

// The helper acts as root on what the service asked. Run by anybody else it
// can do nothing it is for, and it never reads its settings from a flag,
// because a flag is whatever the caller typed.
func TestHelperRunRefusesToRunAsAnUnprivilegedUser(t *testing.T) {
	was := updatesEUID
	updatesEUID = func() int { return 1000 }
	t.Cleanup(func() { updatesEUID = was })
	stateDir := filepath.Join(t.TempDir(), "zoomies-update")
	withHelperStateDir(t, stateDir)

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"updates", "helper", "run"}); code != exitError {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitError, errOut)
	}
	// Where the helper cannot run at all, that is the sentence worth reading,
	// and not one about which account asked.
	want := "runs as root"
	if runtime.GOOS == "windows" {
		want = "not on this platform"
	}
	if !strings.Contains(errOut.String(), want) {
		t.Errorf("the refusal should say %q:\n%s", want, errOut)
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the helper touched its state directory before refusing: %v", err)
	}

	e, _, errOut = newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"updates", "helper", "run", "--dir", "/tmp"}); code != exitUsage {
		t.Errorf("helper run took a flag: exit code = %d\n%s", code, errOut)
	}
}

// Installing and removing the helper write root's units and folders, so both
// refuse anybody else, before reading anything, and say how to run them.
func TestHelperInstallAndRemoveRefuseAnUnprivilegedUser(t *testing.T) {
	was := updatesEUID
	updatesEUID = func() int { return 1000 }
	t.Cleanup(func() { updatesEUID = was })
	for _, verb := range []string{"install", "remove"} {
		e, _, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"updates", "helper", verb, "--config-dir", t.TempDir()}); code != exitError {
			t.Fatalf("helper %s: exit code = %d, want %d\n%s", verb, code, exitError, errOut)
		}
		want := `run "sudo zoomies updates helper ` + verb + `"`
		if runtime.GOOS == "windows" {
			want = "not on this platform"
		}
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("helper %s should say %q:\n%s", verb, want, errOut)
		}
	}
}

// updatesStatusBody is the status a controller in manual mode with a helper and
// a release waiting answers with.
const updatesStatusBody = `{"mode":"manual","soak":"24h","running":{"version":"1.3.4","release":true},
	"latest":{"tag":"v1.3.5","url":"https://github.com/eyupio/zoomies/releases/tag/v1.3.5","published_at":"2026-10-07T09:00:00Z"},
	"target":{"tag":"v1.3.5","newer":true,"due_at":null},
	"reason":"v1.3.5 is ready to install. Press update when you want it.","checked_at":"2026-10-08T09:00:00Z",
	"helper":{"state":"ready","reason":"The update helper is installed, so this controller can update itself.","install_command":""},
	"controller":{"id":"upd_k3fqz2mx7abcd","state":"requested","from":"1.3.4","to":"v1.3.5","trigger":"manual",
		"requested_at":"2026-10-08T09:30:00Z","finished_at":null,"error":""}}`

// updatesRecorder serves one status for every route and remembers what the
// last request was, which is what the acting commands are judged on.
type updatesRecorder struct {
	srv    *httptest.Server
	method string
	path   string
	body   string
	calls  int
}

func newUpdatesRecorder(t *testing.T, status int, body string) *updatesRecorder {
	t.Helper()
	rec := &updatesRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.method, rec.path, rec.body = r.Method, r.URL.Path, string(raw)
		rec.calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func runCLIFailing(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	e, out, errOut := newTestEnv(t)
	code = dispatch(context.Background(), e, args)
	return out.String(), errOut.String(), code
}

func TestUpdatesStatusPrintsWhatTheModeWouldDoAndWhatTheHelperIs(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/updates": updatesStatusBody})
	out, _ := runCLI(t, "updates", "status", "--url", srv.URL)
	for _, want := range []string{
		"manual", "24h", "1.3.4", "v1.3.5",
		"v1.3.5 is ready to install. Press update when you want it.",
		"The update helper is installed, so this controller can update itself.",
		"upd_k3fqz2mx7abcd", "requested",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status does not say %q:\n%s", want, out)
		}
	}
}

func TestUpdatesStatusSaysHowToInstallAMissingHelper(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/updates": `{"mode":"off","soak":"24h","running":{"version":"dev","release":false},
		"latest":null,"target":null,"reason":"Updating is off.","checked_at":null,
		"helper":{"state":"missing","reason":"No update helper is installed on the controller's host.","install_command":"sudo zoomies updates helper install"},
		"controller":null}`})
	out, _ := runCLI(t, "updates", "status", "--url", srv.URL)
	for _, want := range []string{"Updating is off.", "missing", "sudo zoomies updates helper install"} {
		if !strings.Contains(out, want) {
			t.Errorf("status does not say %q:\n%s", want, out)
		}
	}
}

func TestUpdatesStatusAsJSONEmitsTheServersBodyWhole(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/updates": updatesStatusBody})
	out, _ := runCLI(t, "updates", "status", "--output", "json", "--url", srv.URL)
	var got, want map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--output json did not print JSON: %v\n%s", err, out)
	}
	_ = json.Unmarshal([]byte(updatesStatusBody), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("--output json is not the controller's body:\n%s", out)
	}
}

// The reason, the helper's sentence, a tag and the attempt's error all arrive
// from the controller, and the attempt's error can be a helper's log line.
func TestUpdatesStatusPrintsHostileServerTextWithoutTerminalControl(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/updates": `{"mode":"manual","soak":"` + shortHostileJSON + `",
		"running":{"version":"` + shortHostileJSON + `","release":true},
		"latest":{"tag":"` + shortHostileJSON + `","published_at":"2026-10-07T09:00:00Z"},
		"target":{"tag":"` + shortHostileJSON + `","newer":true,"due_at":null},
		"reason":"` + hostileJSON + `","checked_at":"2026-10-08T09:00:00Z",
		"helper":{"state":"missing","reason":"` + hostileJSON + `","install_command":"` + hostileJSON + `"},
		"controller":{"id":"` + shortHostileJSON + `","state":"failed","from":"` + shortHostileJSON + `","to":"` + shortHostileJSON + `","trigger":"manual",
			"requested_at":"2026-10-08T09:30:00Z","finished_at":"2026-10-08T09:31:00Z","error":"` + hostileJSON + `"}}`})
	out, _ := runCLI(t, "updates", "status", "--url", srv.URL)
	assertNoTerminalControl(t, "updates status", out, strings.Count(out, "\n"))
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "acme/widgets  ci  deploy") || strings.HasPrefix(line, "fake") {
			t.Errorf("a newline in the controller's text started a forged line: %q", line)
		}
	}
}

func TestUpdatesCheckPostsAndPrintsTheNewSentence(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusOK, updatesStatusBody)
	out, _ := runCLI(t, "updates", "check", "--url", rec.srv.URL)
	if rec.method != http.MethodPost || rec.path != "/api/v1/updates/check" {
		t.Errorf("check sent %s %s, want POST /api/v1/updates/check", rec.method, rec.path)
	}
	if !strings.Contains(out, "v1.3.5 is ready to install. Press update when you want it.") {
		t.Errorf("check does not print the status sentence it left:\n%s", out)
	}
}

func TestUpdatesCheckAsJSONEmitsTheNewStatus(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusOK, updatesStatusBody)
	out, _ := runCLI(t, "updates", "check", "--output", "json", "--url", rec.srv.URL)
	if !strings.Contains(out, `"mode": "manual"`) {
		t.Errorf("check --output json is not the status:\n%s", out)
	}
}

func TestUpdatesCheckPrintsTheReasonWithoutTerminalControl(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusOK, `{"mode":"manual","soak":"24h","running":{"version":"1.3.4","release":true},
		"latest":null,"target":null,"reason":"`+hostileJSON+`","checked_at":null,
		"helper":{"state":"ready","reason":"","install_command":""},"controller":null}`)
	out, _ := runCLI(t, "updates", "check", "--url", rec.srv.URL)
	assertNoTerminalControl(t, "updates check", out, 1)
}

// With no tag the controller takes the newest release that can be installed
// here, and a body naming one would pin the answer the person never saw.
func TestUpdatesApplyWithoutAVersionPostsAnEmptyBody(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusAccepted, updatesStatusBody)
	runCLI(t, "updates", "apply", "--yes", "--url", rec.srv.URL)
	if rec.method != http.MethodPost || rec.path != "/api/v1/updates/controller" {
		t.Errorf("apply sent %s %s, want POST /api/v1/updates/controller", rec.method, rec.path)
	}
	if rec.body != "" {
		t.Errorf("apply without --version sent a body: %q", rec.body)
	}
}

func TestUpdatesApplyWithAVersionPostsThatTag(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusAccepted, updatesStatusBody)
	runCLI(t, "updates", "apply", "--version", "v1.3.5", "--yes", "--url", rec.srv.URL)
	var got map[string]any
	if err := json.Unmarshal([]byte(rec.body), &got); err != nil {
		t.Fatalf("apply --version sent %q, which is not JSON: %v", rec.body, err)
	}
	if !reflect.DeepEqual(got, map[string]any{"tag": "v1.3.5"}) {
		t.Errorf("apply --version sent %q, want only the tag", rec.body)
	}
}

func TestUpdatesApplyPrintsTheAcceptedAttemptAndHowToFollowIt(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusAccepted, updatesStatusBody)
	out, _ := runCLI(t, "updates", "apply", "--yes", "--url", rec.srv.URL)
	for _, want := range []string{"upd_k3fqz2mx7abcd", "v1.3.5", "zoomies updates status"} {
		if !strings.Contains(out, want) {
			t.Errorf("apply does not say %q:\n%s", want, out)
		}
	}
}

// A script's input is whatever it happens to be and is never taken for an
// answer, so without a terminal the only yes is the flag.
func TestUpdatesApplyRefusesWithoutYesWhenThereIsNoTerminal(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusAccepted, updatesStatusBody)
	e, out, errOut := newTestEnv(t)
	e.in = strings.NewReader("yes\n")
	code := dispatch(context.Background(), e, []string{"updates", "apply", "--url", rec.srv.URL})
	if code == exitOK {
		t.Fatalf("apply without a terminal or --yes exited 0:\n%s%s", out, errOut)
	}
	if rec.calls != 0 {
		t.Errorf("apply asked the controller to update %d time(s) with no yes", rec.calls)
	}
	if !strings.Contains(errOut.String(), "--yes") {
		t.Errorf("the refusal does not name --yes:\n%s", errOut)
	}
}

// A controller address that cannot be used is the first thing wrong, and the
// person should hear it before being asked whether to go on: a yes given to a
// question about a request that can never be sent is a yes wasted. Here there is
// no terminal either, and the address is still what is reported.
func TestUpdatesApplyReportsAnUnusableControllerBeforeItAsksAnything(t *testing.T) {
	e, out, errOut := newTestEnv(t)
	e.in = strings.NewReader("yes\n")
	code := dispatch(context.Background(), e, []string{"updates", "apply", "--url", "http://"})
	if code == exitOK {
		t.Fatalf("apply against an address with no host exited 0:\n%s%s", out, errOut)
	}
	if !strings.Contains(errOut.String(), "is not a controller URL") {
		t.Errorf("the error does not say the address is unusable:\n%s", errOut)
	}
	if strings.Contains(errOut.String(), "--yes") || strings.Contains(out.String()+errOut.String(), "[y/N]") {
		t.Errorf("apply asked, or told the person to say yes, before the address was checked:\n%s%s", out, errOut)
	}
}

func TestUpdatesApplyExitsNonZeroOnARefusalAndSaysWhyAndWhichCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		msg    string
	}{
		{"a mode that is off", http.StatusConflict, "update.mode_off", "Updating is off. Turn it on at Settings."},
		{"an update already open", http.StatusConflict, "update.in_progress", "An update is already in flight."},
		{"a release check GitHub would not finish", http.StatusBadGateway, "update.check_failed", "GitHub did not answer. Try again in a minute."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newUpdatesRecorder(t, tc.status, `{"error":{"code":"`+tc.code+`","message":"`+tc.msg+`"}}`)
			out, errOut, code := runCLIFailing(t, "updates", "apply", "--yes", "--url", rec.srv.URL)
			if code == exitOK {
				t.Fatalf("a %d exited 0:\n%s%s", tc.status, out, errOut)
			}
			for _, want := range []string{tc.msg, tc.code} {
				if !strings.Contains(errOut, want) {
					t.Errorf("the refusal does not say %q:\n%s", want, errOut)
				}
			}
		})
	}
}

func TestUpdatesApplyPrintsARefusalWithoutTerminalControl(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusConflict, `{"error":{"code":"update.in_progress","message":"`+hostileJSON+`","detail":"`+hostileJSON+`"}}`)
	_, errOut, code := runCLIFailing(t, "updates", "apply", "--yes", "--url", rec.srv.URL)
	if code == exitOK {
		t.Fatal("a refusal exited 0")
	}
	assertNoTerminalControl(t, "updates apply", errOut, strings.Count(errOut, "\n"))
	for _, line := range strings.Split(errOut, "\n") {
		if strings.HasPrefix(line, "acme/widgets  ci  deploy") {
			t.Errorf("a newline in the refusal started a forged line: %q", line)
		}
	}
}

func TestUpdatesApplyHelpSaysItIsNotZoomiesUpgrade(t *testing.T) {
	out, errOut := runCLI(t, "updates", "apply", "--help")
	for _, want := range []string{"ITSELF", "root-owned update helper", "platform role", `not "zoomies upgrade"`} {
		if !strings.Contains(out+errOut, want) {
			t.Errorf("apply --help does not say %q:\n%s%s", want, out, errOut)
		}
	}
}

func TestUpdatesGroupListsStatusCheckApplyAndHelper(t *testing.T) {
	out, _ := runCLI(t, "updates", "--help")
	for _, want := range []string{"status", "check", "apply", "helper"} {
		if !strings.Contains(out, want) {
			t.Errorf("the updates group does not list %q:\n%s", want, out)
		}
	}
}

func TestUpdatesApplyRefusesAnEmptyVersionRatherThanTakingTheNewest(t *testing.T) {
	rec := newUpdatesRecorder(t, http.StatusAccepted, updatesStatusBody)
	_, errOut, code := runCLIFailing(t, "updates", "apply", "--version", "", "--yes", "--url", rec.srv.URL)
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d\n%s", code, exitUsage, errOut)
	}
	if rec.calls != 0 {
		t.Error("an empty --version still asked the controller to update")
	}
}
