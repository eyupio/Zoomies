package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAnInvalidDeploymentStopsBeforeTheBinaryDownload(t *testing.T) {
	e, _, _ := newTestEnv(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected download", 500)
	}))
	defer server.Close()
	t.Setenv("ZOOMIES_BASE_URL", server.URL)
	t.Setenv("ZOOMIES_NO_SELF_UPDATE", "")
	t.Setenv("ZOOMIES_UPGRADE_STARTED", "")
	dir := t.TempDir()
	binary := filepath.Join(dir, "zoomies")
	if err := os.WriteFile(binary, []byte("existing binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deployment.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runUpgrade(context.Background(), e, []string{"--config-dir", dir, "--installed-binary", binary})
	if err == nil || !strings.Contains(err.Error(), "record is invalid") {
		t.Fatalf("error: %v", err)
	}
	body, readErr := os.ReadFile(binary)
	if readErr != nil || string(body) != "existing binary" || requests.Load() != 0 {
		t.Fatalf("binary=%q requests=%d error=%v", body, requests.Load(), readErr)
	}
}

func TestEveryUpgradeSpellingAcceptsTheSameMaintenanceOptions(t *testing.T) {
	for _, args := range [][]string{{"upgrade", "--help"}, {"update", "--help"}, {"deployment", "update", "--help"}} {
		e, _, out := newTestEnv(t)
		if code := dispatch(context.Background(), e, args); code != 0 {
			t.Fatalf("%v exited %d", args, code)
		}
		for _, flag := range []string{"--version", "--no-download", "--check", "--yes", "--non-interactive", "--image"} {
			if !strings.Contains(out.String(), flag) {
				t.Fatalf("%v does not accept %s: %s", args, flag, out.String())
			}
		}
	}
}
