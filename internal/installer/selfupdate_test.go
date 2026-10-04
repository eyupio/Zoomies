package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateServer publishes one release whose asset is body, with a checksums file
// that says it is sum (so a test can lie about it).
func updateServer(t *testing.T, tag, body, sum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/download/"+tag+"/zoomies_linux_amd64", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sum + "  zoomies_linux_amd64\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sumOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func updateFixture(t *testing.T, srv *httptest.Server, current string) (SelfUpdateOptions, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "zoomies")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return SelfUpdateOptions{BinaryPath: bin, Current: current, BaseURL: srv.URL, goos: "linux", goarch: "amd64",
		run: func(context.Context, string, ...string) (string, error) { return "", nil }}, bin
}

func TestUpgradeReplacesAnOlderBinaryWithTheVerifiedRelease(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	res, err := SelfUpdate(context.Background(), opts)
	if err != nil || !res.Updated || res.Tag != "v1.4.0" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new build" {
		t.Fatalf("binary = %q", b)
	}
}

// A mirror or a truncated transfer must never replace a working binary.
func TestUpgradeKeepsTheInstalledBinaryWhenTheChecksumDoesNotMatch(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "tampered", sumOf("what was published"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	if _, err := SelfUpdate(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("binary was replaced: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), ".zoomies-update-*")); len(left) != 0 {
		t.Fatalf("temporary download left behind: %v", left)
	}
}

func TestUpgradeDoesNothingWhenTheInstalledBinaryIsTheRelease(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "old", sumOf("old"))
	opts, _ := updateFixture(t, srv, "v1.3.0")
	if res, err := SelfUpdate(context.Background(), opts); err != nil || res.Updated || res.Newer {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// An older release than the one installed is a downgrade, and a database the
// newer build migrated may not be readable by it.
func TestUpgradeNeverDowngrades(t *testing.T) {
	srv := updateServer(t, "v1.2.0", "older", sumOf("older"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	if res, err := SelfUpdate(context.Background(), opts); err != nil || res.Updated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("binary = %q", b)
	}
}

func TestACheckSaysANewerBuildExistsWithoutChangingAnything(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	opts.Check = true
	if res, err := SelfUpdate(context.Background(), opts); err != nil || !res.Newer || res.Updated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("binary = %q", b)
	}
}

func TestALocalBuildHasNoChannelToFollow(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, _ := updateFixture(t, srv, "v0.9.0-12-gabc1234-dirty")
	if res, err := SelfUpdate(context.Background(), opts); err != nil || res.Updated || res.Tag != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
