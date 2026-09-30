package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The token in agent.json is shown once and the controller keeps only its hash,
// so a rename that reaches the disk before the data does -- a power cut in the
// writeback window right after joining -- leaves an empty file and no way back
// except a new join token. Power loss cannot be staged here; what can be shown
// is the order, which is what makes the rename safe: the data is forced out
// before the file is put in place, and the directory entry after.
func TestSaveForcesTheDataToDiskBeforeAndTheRenameAfterInstallingIt(t *testing.T) {
	path := StatePath(filepath.Join(t.TempDir(), "work"))
	want := Credentials{HostID: "host-1", AgentToken: "secret-token", Controller: "https://controller:8080"}

	var events []string
	origFile, origDir := syncFile, syncDir
	t.Cleanup(func() { syncFile, syncDir = origFile, origDir })
	syncFile = func(f *os.File) error {
		raw, err := os.ReadFile(f.Name())
		if err != nil || !strings.Contains(string(raw), "secret-token") {
			t.Errorf("the temporary file was synced before it held the credentials: %q, %v", raw, err)
		}
		if _, err := os.Stat(path); err == nil {
			t.Error("the credentials were installed before their data was synced")
		}
		if info, err := f.Stat(); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Errorf("the temporary file's mode = %v, %v; want 0600", info, err)
		}
		events = append(events, "file")
		return nil
	}
	syncDir = func(dir string) error {
		if _, err := Load(path); err != nil {
			t.Errorf("the directory was synced before the credentials were installed: %v", err)
		}
		if dir != filepath.Dir(path) {
			t.Errorf("synced %s, want %s", dir, filepath.Dir(path))
		}
		events = append(events, "dir")
		return nil
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A directory cannot be opened for syncing on Windows, so Save skips it there.
	wantOrder := "file,dir"
	if runtime.GOOS == "windows" {
		wantOrder = "file"
	}
	if got := strings.Join(events, ","); got != wantOrder {
		t.Fatalf("sync order = %q, want %q", got, wantOrder)
	}
}

// A failed sync means the data may not be on disk, so the file must not replace
// the credentials the host is already running on.
func TestSaveKeepsTheExistingCredentialsWhenTheDataCannotBeSynced(t *testing.T) {
	path := StatePath(filepath.Join(t.TempDir(), "work"))
	old := Credentials{HostID: "host-1", AgentToken: "old-token", Controller: "https://controller:8080"}
	if err := Save(path, old); err != nil {
		t.Fatalf("Save: %v", err)
	}

	origFile := syncFile
	t.Cleanup(func() { syncFile = origFile })
	syncFile = func(*os.File) error { return errors.New("input/output error") }

	err := Save(path, Credentials{HostID: "host-1", AgentToken: "new-token", Controller: "https://controller:8080"})
	if err == nil || !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("Save = %v, want the sync failure reported", err)
	}
	got, err := Load(path)
	if err != nil || got != old {
		t.Fatalf("Load = %+v, %v; want the previous credentials untouched", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %v, %v; want only %s", entries, err, StateFile)
	}
}
