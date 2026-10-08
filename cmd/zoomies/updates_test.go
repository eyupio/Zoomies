package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	if !strings.Contains(errOut.String(), "root") {
		t.Errorf("the refusal should say the helper runs as root:\n%s", errOut)
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the helper touched its state directory before refusing: %v", err)
	}

	e, _, errOut = newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"updates", "helper", "run", "--dir", "/tmp"}); code != exitUsage {
		t.Errorf("helper run took a flag: exit code = %d\n%s", code, errOut)
	}
}
