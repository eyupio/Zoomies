package installer

import (
	"os"
	"path/filepath"
	"testing"
)

// An install and an upgrade write the CLI's connection file, and a test run
// must never leave one in the home directory of whoever ran it. Pointing the
// file at a folder of its own for the whole package settles that once, rather
// than trusting every test that reaches an install or an upgrade to remember.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "zoomies-installer-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("ZOOMIES_CLI_CONFIG", filepath.Join(dir, "cli.yaml"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
