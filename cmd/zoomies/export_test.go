package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The passphrase travels in the request body, read from a file, and the
// archive lands where --out says with nothing but its owner able to read it:
// with a passphrase in it, it is a credential.
func TestExportSendsThePassphraseFromItsFileAndWritesTheArchive(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/installations/ins_1/export" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"kind":"zoomies-installation","tables":[]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	pass := filepath.Join(dir, "move.pass")
	if err := os.WriteFile(pass, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "archive.json")
	out, _ := runCLI(t, "export", "--installation", "ins_1", "--passphrase-file", pass, "--out", dest, "--url", srv.URL)

	if got["passphrase"] != "correct horse" {
		t.Errorf("the passphrase sent was %q, want the file's contents without its newline", got["passphrase"])
	}
	body, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(body), "zoomies-installation") {
		t.Fatalf("the archive was not written: %q, %v", body, err)
	}
	// Windows has no mode bits to check; the directory's ACL is what keeps
	// the archive private there, as for the backup's copied key.
	if st, _ := os.Stat(dest); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("the archive is mode %v, want 0600", st.Mode().Perm())
	}
	if !strings.Contains(out, "sealed under the passphrase") {
		t.Errorf("the output does not say the credentials are in it:\n%s", out)
	}
}

// A refused export must not leave a file behind under the name an operator
// would later import from.
func TestARefusedExportWritesNoArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"installation ins_1: not found"}}`))
	}))
	t.Cleanup(srv.Close)
	dest := filepath.Join(t.TempDir(), "archive.json")
	e, _, _ := newTestEnv(t)
	if code := dispatch(t.Context(), e, []string{"export", "--installation", "ins_1", "--out", dest, "--url", srv.URL}); code == exitOK {
		t.Fatal("a refused export exited cleanly")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a refused export left %s behind", dest)
	}
}

// Import sends the archive as it was written, not re-encoded, with the
// passphrase beside it.
func TestImportSendsTheArchiveAndItsPassphrase(t *testing.T) {
	var got struct {
		Archive    json.RawMessage `json:"archive"`
		Passphrase string          `json:"passphrase"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/installations/import" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"installation":{"id":"ins_1","target":"acme"},"rows":{"jobs":3},"skipped":{"runners":2}}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.json")
	_ = os.WriteFile(archive, []byte(`{"kind":"zoomies-installation","tables":[]}`), 0o600)
	pass := filepath.Join(dir, "p")
	_ = os.WriteFile(pass, []byte("correct horse"), 0o600)

	out, _ := runCLI(t, "import", archive, "--passphrase-file", pass, "--url", srv.URL)
	if got.Passphrase != "correct horse" || !strings.Contains(string(got.Archive), "zoomies-installation") {
		t.Errorf("sent %+v", got)
	}
	for _, want := range []string{"acme", "3 rows", "2 runner rows", "installations verify ins_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
}

// An archive is a year of history and can be hundreds of megabytes, so the
// thirty seconds that suit a list call would cut a large import off on a slow
// link. The operator's own --timeout still wins, because it is the only way to
// ask for less.
func TestImportWaitsLongerThanAnOrdinaryCallUnlessToldOtherwise(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want time.Duration
	}{
		{"nothing given", nil, importRequestTimeout},
		{"an explicit shorter timeout", []string{"--timeout", "5s"}, 5 * time.Second},
		{"an explicit timeout equal to the default", []string{"--timeout", "30s"}, defaultRequestTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _ := newTestEnv(t)
			fs := newFlagSet(e, "zoomies import", "test")
			cf := registerClientFlags(fs, true)
			if err := fs.parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cf.defaultTimeoutTo(fs, importRequestTimeout)
			if *cf.timeout != tc.want {
				t.Fatalf("timeout = %s, want %s", *cf.timeout, tc.want)
			}
		})
	}
}
