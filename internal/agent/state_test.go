package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatePathIsInsideTheWorkDir(t *testing.T) {
	if got, want := StatePath("/var/lib/zoomies/work"), filepath.Join("/var/lib/zoomies/work", StateFile); got != want {
		t.Fatalf("StatePath = %q, want %q", got, want)
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	path := StatePath(filepath.Join(t.TempDir(), "work"))
	want := Credentials{HostID: "host-1", AgentToken: "secret-token", Controller: "https://controller:8080"}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials written as %04o, want 0600", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state directory created as %04o, want 0700", perm)
	}
}

func TestLoadRefusesAWorldReadableFile(t *testing.T) {
	path := StatePath(t.TempDir())
	if err := Save(path, Credentials{HostID: "host-1", AgentToken: "secret-token"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a credentials file every local user can read")
	}
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("error does not say how to fix it: %v", err)
	}
}

func TestLoadReportsNotJoined(t *testing.T) {
	_, err := Load(StatePath(t.TempDir()))
	if !errors.Is(err, ErrNotJoined) {
		t.Fatalf("error = %v, want ErrNotJoined", err)
	}

	path := StatePath(t.TempDir())
	if err := os.WriteFile(path, []byte(`{"host_id":"h"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("error = %v, want ErrNotJoined for credentials with no token", err)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	path := StatePath(t.TempDir())
	if err := os.WriteFile(path, []byte("host_id: h"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil || errors.Is(err, ErrNotJoined) {
		t.Fatalf("error = %v, want a parse failure", err)
	}
	if !strings.Contains(err.Error(), "re-join") {
		t.Fatalf("error does not tell the operator what to do: %v", err)
	}
}

func TestSaveRefusesIncompleteCredentials(t *testing.T) {
	if err := Save(StatePath(t.TempDir()), Credentials{HostID: "host-1"}); err == nil {
		t.Fatal("Save accepted credentials with no agent token")
	}
}
