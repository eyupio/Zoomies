package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eyupio/zoomies/internal/installer"
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

// candidateRelease publishes v9.9.9 with an asset that really runs, because the
// fetch starts the download once (`version --short`) before it is trusted.
func candidateRelease(t *testing.T) (asset string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("binaries are only replaced on linux and darwin")
	}
	asset = "#!/bin/sh\necho 9.9.9\n"
	sum := sha256.Sum256([]byte(asset))
	name := "zoomies_" + runtime.GOOS + "_" + runtime.GOARCH
	mux := http.NewServeMux()
	mux.HandleFunc("/download/v9.9.9/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(asset)) })
	mux.HandleFunc("/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("ZOOMIES_BASE_URL", server.URL)
	t.Setenv(installer.SelfUpdateEnv, "")
	return asset
}

type preflightCall struct {
	name      string
	args, env []string
	installed string
}

// stubUpgradeSeams records the pre-flight and the re-exec instead of running
// them, and reports what the installed binary held when each happened.
func stubUpgradeSeams(t *testing.T, binary string, preflightErr error) (calls *[]preflightCall, reexecs *[]string) {
	t.Helper()
	calls, reexecs = &[]preflightCall{}, &[]string{}
	oldRun, oldReexec := runCandidate, reexecBinary
	t.Cleanup(func() { runCandidate, reexecBinary = oldRun, oldReexec })
	runCandidate = func(_ context.Context, env []string, name string, args ...string) (string, error) {
		b, _ := os.ReadFile(binary)
		*calls = append(*calls, preflightCall{name: name, args: args, env: env, installed: string(b)})
		return "", preflightErr
	}
	reexecBinary = func(path string, _ []string, _ []string) error {
		*reexecs = append(*reexecs, path)
		return nil
	}
	return calls, reexecs
}

// The release that is about to be installed knows what it needs from a
// deployment better than the one that is running, so it is asked first, while
// the binary known to work is still the one in place.
func TestTheCandidateIsAskedToCheckTheDeploymentBeforeItReplacesTheBinary(t *testing.T) {
	e, _, _ := newTestEnv(t)
	release := candidateRelease(t)
	binary := filepath.Join(t.TempDir(), "zoomies")
	if err := os.WriteFile(binary, []byte("existing binary"), 0755); err != nil {
		t.Fatal(err)
	}
	calls, reexecs := stubUpgradeSeams(t, binary, nil)

	sel := deploymentSelector{ConfigDir: "/etc/zoomies", DockerHost: "unix:///run/docker.sock", Runtime: "podman", Image: "example.test/zoomies:custom", Mode: "agent"}
	if err := selfUpdate(context.Background(), e, binary, "v9.9.9", sel); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("the candidate was run %d times, want once: %+v", len(*calls), *calls)
	}
	call := (*calls)[0]
	if call.name == binary || !strings.Contains(filepath.Base(call.name), ".zoomies-update-") {
		t.Fatalf("the pre-flight ran %q, want the temporary candidate beside %q", call.name, binary)
	}
	want := []string{"upgrade", "--check", "--non-interactive", "--no-download", "--installed-binary", binary,
		"--config-dir", "/etc/zoomies", "--docker-host", "unix:///run/docker.sock", "--runtime", "podman",
		"--image", "example.test/zoomies:custom", "--mode", "agent"}
	if !reflect.DeepEqual(call.args, want) {
		t.Fatalf("argv = %q\nwant   %q", call.args, want)
	}
	if slices.Contains(call.args, "--yes") {
		t.Fatalf("an unattended pre-flight must never approve anything: %q", call.args)
	}
	if !slices.Contains(call.env, installer.SelfUpdateEnv+"=1") {
		t.Fatalf("the candidate may try to update itself: %q", call.env)
	}
	if call.installed != "existing binary" {
		t.Fatalf("the binary had already been replaced when the candidate was asked: %q", call.installed)
	}
	if b, _ := os.ReadFile(binary); string(b) != release {
		t.Fatalf("installed = %q", b)
	}
	if b, _ := os.ReadFile(binary + installer.PreviousSuffix); string(b) != "existing binary" {
		t.Fatalf("previous = %q", b)
	}
	if len(*reexecs) != 1 || (*reexecs)[0] != binary {
		t.Fatalf("re-exec = %q", *reexecs)
	}
}

func TestFlagsThatSelectNothingAreNotPassedToTheCandidate(t *testing.T) {
	e, _, _ := newTestEnv(t)
	candidateRelease(t)
	binary := filepath.Join(t.TempDir(), "zoomies")
	if err := os.WriteFile(binary, []byte("existing binary"), 0755); err != nil {
		t.Fatal(err)
	}
	calls, _ := stubUpgradeSeams(t, binary, nil)
	if err := selfUpdate(context.Background(), e, binary, "v9.9.9", deploymentSelector{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"upgrade", "--check", "--non-interactive", "--no-download", "--installed-binary", binary}
	if got := (*calls)[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// The case the split exists for: the new release refuses this deployment, and
// the host is left exactly as it was, without a re-exec into the refusal.
func TestACandidateThatRefusesTheDeploymentReplacesNothing(t *testing.T) {
	e, _, _ := newTestEnv(t)
	candidateRelease(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "zoomies")
	if err := os.WriteFile(binary, []byte("existing binary"), 0755); err != nil {
		t.Fatal(err)
	}
	_, reexecs := stubUpgradeSeams(t, binary, errors.New("the deployment needs a shared folder"))

	err := selfUpdate(context.Background(), e, binary, "v9.9.9", deploymentSelector{})
	if err == nil || !strings.Contains(err.Error(), "needs a shared folder") || !strings.Contains(err.Error(), "left in place") {
		t.Fatalf("error: %v", err)
	}
	if b, _ := os.ReadFile(binary); string(b) != "existing binary" {
		t.Fatalf("binary = %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the folder holds more than the binary: %v", entries)
	}
	if len(*reexecs) != 0 {
		t.Fatalf("re-exec into a refused release: %q", *reexecs)
	}
}
