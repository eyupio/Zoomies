package cliconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureURLCreatesThePrivateFileAndItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config", "zoomies", "cli.yaml")
	got, err := EnsureURL(path, "https://zoomies.example.com")
	if err != nil || got != Created {
		t.Fatalf("EnsureURL = %v, %v; want Created", got, err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.URL != "https://zoomies.example.com" || cfg.Token != "" {
		t.Errorf("the file must name the controller and hold no credential: %+v (%v)", cfg, err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v (%v), want %v: a token goes in this file next", p, fi.Mode().Perm(), err, want)
		}
	}
}

// What an operator put in the file is theirs: a url they chose is never replaced,
// and one added beside their token leaves the token and their comments alone.
func TestEnsureURLNeverReplacesWhatTheOperatorWrote(t *testing.T) {
	dir := t.TempDir()
	chosen := filepath.Join(dir, "chosen.yaml")
	if err := os.WriteFile(chosen, []byte("url: https://mine.example\ntoken: zoo_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := EnsureURL(chosen, "https://other.example"); err != nil || got != Kept {
		t.Fatalf("EnsureURL over a chosen url = %v, %v; want Kept", got, err)
	}
	if raw, _ := os.ReadFile(chosen); !strings.Contains(string(raw), "mine.example") || strings.Contains(string(raw), "other.example") {
		t.Errorf("the operator's url must stay: %s", raw)
	}

	tokenOnly := filepath.Join(dir, "token.yaml")
	if err := os.WriteFile(tokenOnly, []byte("# mine\ntoken: zoo_y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := EnsureURL(tokenOnly, "https://zoomies.example.com"); err != nil || got != Added {
		t.Fatalf("EnsureURL beside a token = %v, %v; want Added", got, err)
	}
	cfg, err := Load(tokenOnly)
	if err != nil || cfg.URL != "https://zoomies.example.com" || cfg.Token != "zoo_y" {
		t.Errorf("the url is added and the token kept: %+v (%v)", cfg, err)
	}
	if raw, _ := os.ReadFile(tokenOnly); !strings.Contains(string(raw), "# mine") {
		t.Errorf("the operator's comment must survive: %s", raw)
	}
}

func TestEnsureURLLeavesAFileItCannotReadAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(path, []byte("url: [not, a, string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureURL(path, "https://zoomies.example.com"); err == nil {
		t.Error("a file that does not parse must be reported, not rewritten")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "url: [not, a, string\n" {
		t.Errorf("an unreadable file must be left as it was: %q", raw)
	}
}

func TestPathHonoursTheOverridesInOrder(t *testing.T) {
	t.Setenv("ZOOMIES_CLI_CONFIG", "/x/cli.yaml")
	if Path() != "/x/cli.yaml" {
		t.Errorf("Path = %s", Path())
	}
	t.Setenv("ZOOMIES_CLI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if Path() != filepath.Join("/xdg", "zoomies", "cli.yaml") {
		t.Errorf("Path = %s", Path())
	}
}
