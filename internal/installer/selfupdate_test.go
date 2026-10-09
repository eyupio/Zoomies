package installer

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

// Fetching is the half of an update that can be undone by doing nothing: the
// installed binary has to be exactly what it was, however the download went.
func TestFetchLeavesTheInstalledBinaryUntouched(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	cand, res, err := FetchRelease(context.Background(), opts)
	if err != nil || cand == nil || !res.Newer || res.Updated || res.Tag != "v1.4.0" || cand.Tag != "v1.4.0" {
		t.Fatalf("cand=%+v res=%+v err=%v", cand, res, err)
	}
	defer cand.Discard()
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("installed binary = %q", b)
	}
	if b, _ := os.ReadFile(cand.Path); string(b) != "new build" {
		t.Fatalf("candidate = %q", b)
	}
	if _, err := os.Stat(bin + PreviousSuffix); !os.IsNotExist(err) {
		t.Fatalf("a previous binary exists before anything was installed: %v", err)
	}
}

// A rollback is only as good as the file it goes back to, so the previous
// binary must be the bytes that were running and a second update must move on.
func TestInstallKeepsThePreviousBinaryBesideIt(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	install := func() {
		t.Helper()
		cand, _, err := FetchRelease(context.Background(), opts)
		if err != nil || cand == nil {
			t.Fatalf("cand=%+v err=%v", cand, err)
		}
		if err := cand.Install(opts); err != nil {
			t.Fatal(err)
		}
		cand.Discard()
	}
	install()
	if b, _ := os.ReadFile(bin); string(b) != "new build" {
		t.Fatalf("binary = %q", b)
	}
	if b, _ := os.ReadFile(bin + PreviousSuffix); string(b) != "old" {
		t.Fatalf("previous = %q", b)
	}
	// Windows has no POSIX mode bits to compare.
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(bin + PreviousSuffix); err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("previous is not executable: %v %v", info, err)
		}
	}

	// The next release replaces the installed one, and the previous moves on.
	srv2 := updateServer(t, "v1.5.0", "newer build", sumOf("newer build"))
	opts.BaseURL = srv2.URL
	opts.Current = "v1.4.0"
	install()
	if b, _ := os.ReadFile(bin); string(b) != "newer build" {
		t.Fatalf("binary = %q", b)
	}
	if b, _ := os.ReadFile(bin + PreviousSuffix); string(b) != "new build" {
		t.Fatalf("previous = %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), "*.new")); len(left) != 0 {
		t.Fatalf("staging link left behind: %v", left)
	}
}

// An unattended run that swaps the binary and then stops on something the new
// release needs leaves a service nobody can start; refusing first costs nothing.
func TestAFailedPreflightLeavesTheBinaryInPlace(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	refusal := errors.New("the deployment record needs a newer layout")
	opts.Preflight = func(context.Context, string) error { return refusal }
	res, err := SelfUpdate(context.Background(), opts)
	if !errors.Is(err, refusal) || res.Updated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Fatalf("binary was replaced: %q", b)
	}
	if _, err := os.Stat(bin + PreviousSuffix); !os.IsNotExist(err) {
		t.Fatalf("a previous binary was kept for an update that did not happen: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), ".zoomies-update-*")); len(left) != 0 {
		t.Fatalf("temporary download left behind: %v", left)
	}
}

// The order is the point of the split: the pre-flight must see the candidate
// on disk and the old binary still installed.
func TestThePreflightRunsBetweenTheDownloadAndTheReplace(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	var events []string
	opts.Preflight = func(_ context.Context, candidate string) error {
		got, _ := os.ReadFile(candidate)
		installed, _ := os.ReadFile(bin)
		events = append(events, "preflight candidate="+string(got)+" installed="+string(installed))
		return nil
	}
	if res, err := SelfUpdate(context.Background(), opts); err != nil || !res.Updated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	want := []string{"preflight candidate=new build installed=old"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new build" {
		t.Fatalf("binary = %q", b)
	}
}

func TestThePreflightIsNotRunWhenNothingIsNewer(t *testing.T) {
	for name, tc := range map[string]struct{ tag, body, current string }{
		"the installed binary is the release": {"v1.4.0", "old", "v1.3.0"},
		"the release is older":                {"v1.2.0", "older", "v1.3.0"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := updateServer(t, tc.tag, tc.body, sumOf(tc.body))
			opts, _ := updateFixture(t, srv, tc.current)
			opts.Preflight = func(context.Context, string) error {
				t.Error("the pre-flight ran although there was nothing to install")
				return nil
			}
			cand, _, err := FetchRelease(context.Background(), opts)
			if err != nil || cand != nil {
				t.Fatalf("cand=%+v err=%v", cand, err)
			}
			if res, err := SelfUpdate(context.Background(), opts); err != nil || res.Updated {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

// Some filesystems refuse hard links, and so does a kernel with
// fs.protected_hardlinks set for a file the user does not own. A rollback file
// that is a copy is still a rollback file.
func TestInstallFallsBackToACopyWhenTheLinkCannotBeMade(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	if err := os.Chmod(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	old := linkFile
	linkFile = func(string, string) error { return errors.New("operation not permitted") }
	t.Cleanup(func() { linkFile = old })

	cand, _, err := FetchRelease(context.Background(), opts)
	if err != nil || cand == nil {
		t.Fatalf("cand=%+v err=%v", cand, err)
	}
	defer cand.Discard()
	if err := cand.Install(opts); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new build" {
		t.Fatalf("binary = %q", b)
	}
	if b, _ := os.ReadFile(bin + PreviousSuffix); string(b) != "old" {
		t.Fatalf("previous = %q", b)
	}
	// Windows has no POSIX mode bits to compare.
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(bin + PreviousSuffix); err != nil || info.Mode().Perm() != 0o750 {
			t.Fatalf("the copy lost the binary's mode: %v %v", info, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), "*.new")); len(left) != 0 {
		t.Fatalf("staging file left behind: %v", left)
	}
}

// Failing closed is for the case where there is no way to keep the old binary
// at all: replacing it then would leave nothing to go back to.
func TestInstallRefusesToReplaceWhenThePreviousBinaryCannotBeKept(t *testing.T) {
	srv := updateServer(t, "v1.4.0", "new build", sumOf("new build"))
	opts, bin := updateFixture(t, srv, "v1.3.0")
	old := linkFile
	t.Cleanup(func() { linkFile = old })
	// The link fails and the installed binary vanishes under it, so the copy
	// that follows has nothing to read either.
	linkFile = func(string, string) error {
		_ = os.Remove(bin)
		return errors.New("operation not permitted")
	}
	cand, _, err := FetchRelease(context.Background(), opts)
	if err != nil || cand == nil {
		t.Fatalf("cand=%+v err=%v", cand, err)
	}
	defer cand.Discard()
	err = cand.Install(opts)
	if err == nil || !strings.Contains(err.Error(), "left in place") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("the error hides why the link failed: %v", err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("the candidate was renamed over a binary that could not be kept: %v", err)
	}
	if _, err := os.Stat(cand.Path); err != nil {
		t.Fatalf("the candidate was consumed: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), "*"+PreviousSuffix+"*")); len(left) != 0 {
		t.Fatalf("a partial previous binary was left behind: %v", left)
	}
}

func TestDiscardingNothingIsHarmless(t *testing.T) {
	var cand *Candidate
	cand.Discard()
}
