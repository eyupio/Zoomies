//go:build unix

package installer

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// testServiceUID is the account the update folder belongs to in these tests.
// The tests run as root, so every file they make is root's; the ownerOf seam
// is what says a file is the service's, and a test that wants a stranger's
// file says so through it rather than with chown.
const testServiceUID = 65532

func ownedByService(os.FileInfo) (int, bool) { return testServiceUID, true }

func requestBody(id, tag string) string {
	return `{"v":1,"id":"` + id + `","tag":"` + tag + `","requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z"}`
}

const wellFormedRequest = `{"v":1,"id":"upd_k3fqz2mx7abcd","tag":"v1.3.5","requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z"}`

// updateRoot is the update folder as the helper holds it: a directory and the
// root handle every read and write goes through.
func updateRoot(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "update")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return dir, root
}

func plant(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
}

func contents(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertRefused reads the folder's request and wants a refusal that says
// what it is about, and the request gone.
func assertRefused(t *testing.T, dir string, root *os.Root, ownerOf func(os.FileInfo) (int, bool), want string) {
	t.Helper()
	r, err := readRequest(root, testServiceUID, ownerOf)
	if err == nil {
		t.Fatalf("the request was accepted: %+v", r)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal should say %q, got: %v", want, err)
	}
	if r != (updates.Request{}) {
		t.Errorf("a refusal came back with a request to act on: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(dir, channel.RequestFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused request is still in the folder (%v), so the path unit would fire on it again", err)
	}
}

// The refusals below are only worth anything if a request that breaks none of
// the rules gets through, so this is the one they are all measured against.
func TestTheHelperReadsAWellFormedRequest(t *testing.T) {
	dir, root := updateRoot(t)
	plant(t, filepath.Join(dir, channel.RequestFile), wellFormedRequest)
	r, err := readRequest(root, testServiceUID, ownedByService)
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	if r.ID != "upd_k3fqz2mx7abcd" || r.Tag != "v1.3.5" || r.RequestedBy != "user:alice" {
		t.Fatalf("readRequest = %+v", r)
	}
}

// os.Root follows a link that stays inside the root even when the open asks
// for O_NOFOLLOW, so the link inside the folder is the case that proves the
// name itself is checked. A link out of the folder would let the service point
// root at any file on the host.
func TestTheHelperRefusesARequestThatIsASymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	plant(t, outside, wellFormedRequest)
	for name, target := range map[string]string{"outside": outside, "inside": "real.json", "dangling": "missing.json"} {
		t.Run(name, func(t *testing.T) {
			dir, root := updateRoot(t)
			plant(t, filepath.Join(dir, "real.json"), wellFormedRequest)
			if err := os.Symlink(target, filepath.Join(dir, channel.RequestFile)); err != nil {
				t.Fatal(err)
			}
			assertRefused(t, dir, root, ownedByService, "link")
			// Consuming a link removes the link and never what it pointed at.
			if got := contents(t, outside); got != wellFormedRequest {
				t.Errorf("the link's target outside the folder changed to %q", got)
			}
			if got := contents(t, filepath.Join(dir, "real.json")); got != wellFormedRequest {
				t.Errorf("the link's target inside the folder changed to %q", got)
			}
		})
	}
}

func TestTheHelperRefusesARequestThatIsADirectory(t *testing.T) {
	dir, root := updateRoot(t)
	if err := os.Mkdir(filepath.Join(dir, channel.RequestFile), 0o750); err != nil {
		t.Fatal(err)
	}
	plant(t, filepath.Join(dir, channel.RequestFile, "inner.json"), wellFormedRequest)
	assertRefused(t, dir, root, ownedByService, "not a plain file")
}

// A pipe opened for reading waits for a writer, and the service would never
// be one: the helper would hang until systemd killed it.
func TestTheHelperRefusesAFIFOWithoutBlocking(t *testing.T) {
	dir, root := updateRoot(t)
	path := filepath.Join(dir, channel.RequestFile)
	if err := syscall.Mkfifo(path, 0o640); err != nil {
		t.Skipf("cannot make a FIFO here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readRequest(root, testServiceUID, ownedByService)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a plain file") {
			t.Errorf("the refusal should say the request is not a plain file, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		// Opening the pipe for writing releases the blocked reader, so the test
		// ends instead of leaking a goroutine stuck in open.
		if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		t.Fatal("reading a FIFO blocked")
	}
}

// The look and the open are two steps, and a hostile service gets the moment
// between them. Whatever it swaps in, the helper neither hangs nor reads a file
// other than the one it looked at.
func TestTheHelperRefusesARequestSwappedAfterItWasLookedAt(t *testing.T) {
	for name, swap := range map[string]func(t *testing.T, dir, path string){
		"for a FIFO": func(t *testing.T, dir, path string) {
			if err := syscall.Mkfifo(path, 0o640); err != nil {
				t.Skipf("cannot make a FIFO here: %v", err)
			}
		},
		"for a link inside the folder": func(t *testing.T, dir, path string) {
			if err := os.Symlink("real.json", path); err != nil {
				t.Fatal(err)
			}
		},
		"for another file": func(t *testing.T, dir, path string) {
			if err := os.Rename(filepath.Join(dir, "real.json"), path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, root := updateRoot(t)
			path := filepath.Join(dir, channel.RequestFile)
			plant(t, path, wellFormedRequest)
			plant(t, filepath.Join(dir, "real.json"), requestBody("upd_bbbb", "v1.3.6"))
			was := betweenLookAndOpen
			betweenLookAndOpen = func(string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				swap(t, dir, path)
			}
			t.Cleanup(func() { betweenLookAndOpen = was })
			done := make(chan error, 1)
			go func() {
				_, err := readRequest(root, testServiceUID, ownedByService)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "changed while it was being opened") {
					t.Errorf("the refusal should say the request changed, got: %v", err)
				}
			case <-time.After(5 * time.Second):
				if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
					w.Close()
				}
				t.Fatal("reading a request swapped for a FIFO blocked")
			}
		})
	}
}

func TestTheHelperRefusesAnOversizedRequest(t *testing.T) {
	dir, root := updateRoot(t)
	// Leading spaces keep it a valid request, so its size is all that is wrong.
	body := strings.Repeat(" ", updates.MaxRequestBytes+1-len(wellFormedRequest)) + wellFormedRequest
	plant(t, filepath.Join(dir, channel.RequestFile), body)
	assertRefused(t, dir, root, ownedByService, "larger than a request can be")
}

// The folder is the service's, so a file in it owned by anyone else is not
// one the service wrote. Above all it may be root's: a hard link to a file
// only root can read, which the helper must not read for it, not even far
// enough to quote a field name back in its answer.
func TestTheHelperRefusesARequestOwnedBySomeoneElse(t *testing.T) {
	for name, ownerOf := range map[string]func(os.FileInfo) (int, bool){
		"root":       func(os.FileInfo) (int, bool) { return 0, true },
		"a stranger": func(os.FileInfo) (int, bool) { return 1000, true },
		// The uid it would have answered is the right one, so only the
		// unanswered question refuses it.
		"nobody can tell": func(os.FileInfo) (int, bool) {
			return testServiceUID, false
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, root := updateRoot(t)
			plant(t, filepath.Join(dir, channel.RequestFile), `{"root_secret":"hunter2"}`)
			r, err := readRequest(root, testServiceUID, ownerOf)
			if err == nil {
				t.Fatalf("the request was accepted: %+v", r)
			}
			if !strings.Contains(err.Error(), "owned by") && !strings.Contains(err.Error(), "who owns") {
				t.Errorf("the refusal should be about the owner, got: %v", err)
			}
			if strings.Contains(err.Error(), "root_secret") || strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the refusal quotes the content of a file the service does not own: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, channel.RequestFile)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the refused request is still in the folder: %v", err)
			}
		})
	}
}

func TestTheHelperRefusesAnUnknownField(t *testing.T) {
	dir, root := updateRoot(t)
	body := strings.TrimSuffix(wellFormedRequest, "}") + `,"url":"https://example.com/zoomies"}`
	plant(t, filepath.Join(dir, channel.RequestFile), body)
	assertRefused(t, dir, root, ownedByService, "url")
}

func TestTheHelperRefusesABadTag(t *testing.T) {
	for _, tag := range []string{`v1.3`, `1.3.5`, `v1.3.5-rc1`, `v1.3.5\n`, `../v1.3.5`} {
		t.Run(tag, func(t *testing.T) {
			dir, root := updateRoot(t)
			plant(t, filepath.Join(dir, channel.RequestFile), requestBody("upd_k3fqz2mx7abcd", tag))
			assertRefused(t, dir, root, ownedByService, "release tag")
		})
	}
}

// The path unit fires while request.json exists, so a request left behind
// would run the helper again and again; and a refused one is no different.
func TestTheHelperConsumesTheRequestItReads(t *testing.T) {
	for name, body := range map[string]string{"accepted": wellFormedRequest, "refused": `{"v":2}`} {
		t.Run(name, func(t *testing.T) {
			dir, root := updateRoot(t)
			plant(t, filepath.Join(dir, channel.RequestFile), body)
			_, _ = readRequest(root, testServiceUID, ownedByService)
			if _, err := os.Lstat(filepath.Join(dir, channel.RequestFile)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the request is still in the folder after it was read: %v", err)
			}
		})
	}
	// No request is not a refusal: the path unit can fire on a file that has gone
	// by the time the helper looks, and there is nobody to answer.
	_, root := updateRoot(t)
	if _, err := readRequest(root, testServiceUID, ownedByService); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an empty folder should read as no request, got: %v", err)
	}
}

func testRequest(id, tag string) updates.Request {
	return updates.Request{V: updates.WireVersion, ID: id, Tag: tag, RequestedBy: "user:alice", RequestedAt: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
}

// stateDir is where the helper keeps its limits, made fresh for each test so
// the first load starts from nothing.
func stateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "zoomies-update")
}

func loadState(t *testing.T, dir string) *helperState {
	t.Helper()
	s, err := loadHelperState(dir)
	if err != nil {
		t.Fatalf("loadHelperState: %v", err)
	}
	return s
}

var t0 = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

// A request is acted on once. A replayed one -- the service writing back a
// request it saw before -- would otherwise start the same attempt again.
func TestTheHelperRefusesARepeatedRequestID(t *testing.T) {
	s := loadState(t, stateDir(t))
	if err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0); err != nil {
		t.Fatalf("the first request was refused: %v", err)
	}
	// Late enough and for a tag tried once, so the id is the only thing wrong.
	err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "seen before") {
		t.Fatalf("a repeated id should be refused as seen before, got: %v", err)
	}
}

// Two failures of one tag is where the helper stops and a person looks. The
// third attempt is refused however long ago the others were, and another tag
// is not held up by it.
func TestTheHelperRefusesATagTriedTwiceAlready(t *testing.T) {
	s := loadState(t, stateDir(t))
	for i, id := range []string{"upd_aaaa", "upd_bbbb"} {
		if err := s.admit(testRequest(id, "v1.3.5"), t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("attempt %d was refused: %v", i+1, err)
		}
	}
	err := s.admit(testRequest("upd_cccc", "v1.3.5"), t0.Add(30*24*time.Hour))
	if err == nil || !strings.Contains(err.Error(), "tried 2 times") {
		t.Fatalf("a third attempt at one tag should be refused, got: %v", err)
	}
	if err := s.admit(testRequest("upd_dddd", "v1.3.6"), t0.Add(31*24*time.Hour)); err != nil {
		t.Fatalf("another tag was refused: %v", err)
	}
}

// The interval is across tags: a loop that names a new tag every time is still
// one attempt per ten minutes.
func TestTheHelperRefusesAnAttemptWithinTenMinutesOfTheLast(t *testing.T) {
	s := loadState(t, stateDir(t))
	if err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0); err != nil {
		t.Fatal(err)
	}
	err := s.admit(testRequest("upd_bbbb", "v1.3.6"), t0.Add(helperMinInterval-time.Second))
	if err == nil || !strings.Contains(err.Error(), "at most one every") {
		t.Fatalf("an attempt within ten minutes of the last should be refused, got: %v", err)
	}
	if err := s.admit(testRequest("upd_cccc", "v1.3.6"), t0.Add(helperMinInterval)); err != nil {
		t.Fatalf("an attempt ten minutes after the last was refused: %v", err)
	}
}

// The limits are only limits if a restart of the helper -- which is every
// request, since each run is a oneshot -- does not forget them.
func TestTheHelperStateSurvivesARestart(t *testing.T) {
	dir := stateDir(t)
	s := loadState(t, dir)
	if err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	again := loadState(t, dir)
	if err := again.admit(testRequest("upd_aaaa", "v1.3.5"), t0.Add(time.Hour)); err == nil {
		t.Error("a restart forgot the ids already seen")
	}
	if err := again.admit(testRequest("upd_bbbb", "v1.3.6"), t0.Add(time.Minute)); err == nil {
		t.Error("a restart forgot when the last attempt was")
	}
	if got := again.Attempts["v1.3.5"]; len(got) != 1 || !got[0].Equal(t0) {
		t.Errorf("the attempts at v1.3.5 after a restart are %v, want [%v]", got, t0)
	}
}

// root owns the state, and nobody else may read or write it: the directory is
// made 0700 and the file 0600 whatever the umask.
func TestTheHelperKeepsItsStatePrivate(t *testing.T) {
	dir := stateDir(t)
	s := loadState(t, dir)
	if err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]fs.FileMode{dir: 0o700, filepath.Join(dir, helperStateFile): 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s is mode %o, want %o", path, got, want)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the state directory holds %d entries, want only %s", len(entries), helperStateFile)
	}
}

// The state is bounded however long a host runs, or however many tags a
// looping service names.
func TestTheHelperRemembersABoundedNumberOfRequests(t *testing.T) {
	s := loadState(t, stateDir(t))
	for i := range helperSeenIDs + 5 {
		id := "upd_" + strings.Repeat("a", i+1)
		tag := "v1.0." + strings.Repeat("1", i+1)
		if err := s.admit(testRequest(id, tag), t0.Add(time.Duration(i)*helperMinInterval)); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if len(s.Seen) != helperSeenIDs {
		t.Errorf("%d ids are remembered, want %d", len(s.Seen), helperSeenIDs)
	}
	if len(s.Attempts) != helperSeenIDs {
		t.Errorf("%d tags are remembered, want %d", len(s.Attempts), helperSeenIDs)
	}
	newest := "upd_" + strings.Repeat("a", helperSeenIDs+5)
	if !slices.Contains(s.Seen, newest) || slices.Contains(s.Seen, "upd_a") {
		t.Error("the oldest id should be the one forgotten, and the newest kept")
	}
}

// The state is what stops a compromised service from asking root for update
// after update, so anything that might let the service have written it is
// refused and not used: an owner other than the helper's, a mode that lets a
// group or the world write, or a link.
func TestTheHelperRefusesStateItDoesNotControl(t *testing.T) {
	saved := func(t *testing.T) string {
		dir := stateDir(t)
		s := loadState(t, dir)
		if err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0); err != nil {
			t.Fatal(err)
		}
		if err := s.save(); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("owned by another uid", func(t *testing.T) {
		dir := saved(t)
		// The files are root's; pretending the helper runs as someone else makes
		// them another account's without chown.
		was := helperEUID
		helperEUID = func() int { return 4242 }
		t.Cleanup(func() { helperEUID = was })
		if _, err := loadHelperState(dir); err == nil || !strings.Contains(err.Error(), "owned by") {
			t.Errorf("state owned by another account was used: %v", err)
		}
	})
	t.Run("a group-writable directory", func(t *testing.T) {
		dir := saved(t)
		if err := os.Chmod(dir, 0o770); err != nil {
			t.Fatal(err)
		}
		if _, err := loadHelperState(dir); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Errorf("a group-writable state directory was used: %v", err)
		}
	})
	t.Run("a world-writable file", func(t *testing.T) {
		dir := saved(t)
		if err := os.Chmod(filepath.Join(dir, helperStateFile), 0o602); err != nil {
			t.Fatal(err)
		}
		if _, err := loadHelperState(dir); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Errorf("a world-writable state file was used: %v", err)
		}
	})
	t.Run("a linked directory", func(t *testing.T) {
		real := saved(t)
		link := filepath.Join(t.TempDir(), "zoomies-update")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadHelperState(link); err == nil || !strings.Contains(err.Error(), "not a folder") {
			t.Errorf("a linked state directory was used: %v", err)
		}
	})
	t.Run("a linked file", func(t *testing.T) {
		dir := stateDir(t)
		loadState(t, dir)
		elsewhere := filepath.Join(dir, "elsewhere.json")
		plant(t, elsewhere, `{"seen":[],"attempts":{}}`)
		if err := os.Chmod(elsewhere, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("elsewhere.json", filepath.Join(dir, helperStateFile)); err != nil {
			t.Fatal(err)
		}
		if _, err := loadHelperState(dir); err == nil || !strings.Contains(err.Error(), "link") {
			t.Errorf("a linked state file was used: %v", err)
		}
	})
}

// A state file that cannot be read is not a fresh start: treating it as one
// would reset the limits, which is exactly what corrupting it would be for.
func TestTheHelperRefusesCorruptStateRatherThanResettingIt(t *testing.T) {
	for name, body := range map[string]string{
		"empty":         "",
		"truncated":     `{"seen":["upd_aaaa"],"attem`,
		"not JSON":      "not json",
		"unknown field": `{"seen":[],"attempts":{},"reset":true}`,
		"two documents": `{"seen":[],"attempts":{}}{"seen":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := stateDir(t)
			loadState(t, dir)
			path := filepath.Join(dir, helperStateFile)
			plant(t, path, body)
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadHelperState(dir)
			if err == nil {
				t.Fatal("corrupt state was used")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the refusal should name the state file, got: %v", err)
			}
		})
	}
	t.Run("oversized", func(t *testing.T) {
		dir := stateDir(t)
		loadState(t, dir)
		path := filepath.Join(dir, helperStateFile)
		plant(t, path, strings.Repeat(" ", maxHelperStateBytes)+`{"seen":[],"attempts":{}}`)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		// Refused on its size, before any of it is read.
		if _, err := loadHelperState(dir); err == nil || !strings.Contains(err.Error(), "bytes, over the limit") {
			t.Fatalf("an oversized state file should be refused on its size, got: %v", err)
		}
	})
}

// The service can plant anything in the folder, including a link named
// result.json pointing at a file root would then overwrite for it. The answer
// replaces the link with a file of its own and leaves the target alone.
func TestTheResultIsWrittenWithoutFollowingAPlantedLink(t *testing.T) {
	// A hardened unit may set UMask=0077, which must not leave the service with
	// an answer it cannot read.
	was := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(was) })
	victim := filepath.Join(t.TempDir(), "shadow")
	for name, target := range map[string]string{"outside": victim, "inside": "victim.json"} {
		t.Run(name, func(t *testing.T) {
			plant(t, victim, "untouched")
			dir, root := updateRoot(t)
			inside := filepath.Join(dir, "victim.json")
			plant(t, inside, "untouched")
			resultPath := filepath.Join(dir, channel.ResultFile)
			if err := os.Symlink(target, resultPath); err != nil {
				t.Fatal(err)
			}
			if err := writeResult(root, updates.Result{ID: "upd_aaaa", OK: true, Tag: "v1.3.5"}); err != nil {
				t.Fatalf("writeResult: %v", err)
			}
			info, err := os.Lstat(resultPath)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() {
				t.Fatalf("result.json is %v, want a plain file", info.Mode())
			}
			// The service reads the answer, so it has to be able to.
			if got := info.Mode().Perm(); got != 0o644 {
				t.Errorf("result.json is mode %o, want 644", got)
			}
			for _, path := range []string{victim, inside} {
				if got := contents(t, path); got != "untouched" {
					t.Errorf("%s was written through the link: %q", path, got)
				}
			}
			got, found, err := channel.ReadResult(dir)
			if err != nil || !found || got.ID != "upd_aaaa" || !got.OK {
				t.Fatalf("the service read back %+v, found %v, err %v", got, found, err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 2 {
				t.Errorf("the folder holds %d entries, want the result and the planted file only", len(entries))
			}
		})
	}
}

// A result is a message for the UI and not a log store, and the service reads
// it through a limit. Whatever the engine printed, the answer stays within the
// bound, is valid UTF-8, and keeps the end of the output, where the reason is.
func TestTheResultIsBoundedAndValidUTF8(t *testing.T) {
	dir, root := updateRoot(t)
	// Each invalid byte becomes a three-byte replacement once encoded, so they
	// have to be counted after the replacement and not before; and the output is
	// longer than the bound, so the cut has to keep the end.
	tail := strings.Repeat("\xffa", 10<<10) + "the last line says why"
	worst := strings.Repeat("\x01", 20<<10) // six bytes each once escaped
	// The error's bound falls inside a three-byte character, which has to be
	// dropped whole rather than cut.
	errorText := strings.Repeat("\x01", maxResultErrorBytes-1) + "€€\xffend"
	r := updates.Result{ID: "upd_aaaa", Tag: "v1.3.5", From: worst, To: worst, Error: errorText, LogTail: tail}
	if err := writeResult(root, r); err != nil {
		t.Fatalf("writeResult: %v", err)
	}
	raw := contents(t, filepath.Join(dir, channel.ResultFile))
	if !utf8.ValidString(raw) {
		t.Error("result.json is not valid UTF-8")
	}
	var got updates.Result
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.LogTail) > updates.MaxLogTailBytes {
		t.Errorf("the log tail is %d bytes, over %d", len(got.LogTail), updates.MaxLogTailBytes)
	}
	if !strings.HasSuffix(got.LogTail, "the last line says why") {
		t.Error("the log tail lost the end of the output")
	}
	for name, s := range map[string]string{"error": got.Error, "log tail": got.LogTail, "from": got.From} {
		if !utf8.ValidString(s) {
			t.Errorf("the %s is not valid UTF-8", name)
		}
	}
	if len(got.Error) > maxResultErrorBytes || len(got.From) > maxResultFieldBytes {
		t.Errorf("the error is %d bytes and from %d, over their bounds", len(got.Error), len(got.From))
	}
	if got.V != updates.WireVersion {
		t.Errorf("the result is version %d, want %d", got.V, updates.WireVersion)
	}
	// The service's reader is the one that has to accept it.
	if _, found, err := channel.ReadResult(dir); err != nil || !found {
		t.Errorf("the service could not read the bounded result: found %v, err %v", found, err)
	}
}

// A refusal that went unanswered would leave the controller waiting out the
// attempt's whole timeout for a run that never started.
func TestARefusedRequestIsAnsweredWithItsReason(t *testing.T) {
	dir, root := updateRoot(t)
	s := loadState(t, stateDir(t))
	if err := s.admit(testRequest("upd_aaaa", "v1.3.5"), t0); err != nil {
		t.Fatal(err)
	}
	plant(t, filepath.Join(dir, channel.RequestFile), requestBody("upd_bbbb", "v1.3.6"))
	r, err := readRequest(root, testServiceUID, ownedByService)
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	refusal := s.admit(r, t0.Add(time.Minute))
	if refusal == nil {
		t.Fatal("an attempt a minute after the last was admitted")
	}
	at := t0.Add(time.Minute)
	if err := writeResult(root, refusedResult(r, refusal, at)); err != nil {
		t.Fatalf("writeResult: %v", err)
	}
	got, found, err := channel.ReadResult(dir)
	if err != nil || !found {
		t.Fatalf("the service found no answer: %v", err)
	}
	if got.ID != "upd_bbbb" || got.Tag != "v1.3.6" || got.OK || got.Error != refusal.Error() || !got.FinishedAt.Equal(at) {
		t.Errorf("the answer is %+v, want a failure for upd_bbbb saying %q", got, refusal)
	}

	// A request that could not be read has no id to echo, and is still answered.
	plant(t, filepath.Join(dir, channel.RequestFile), `{"v":2}`)
	r, err = readRequest(root, testServiceUID, ownedByService)
	if err == nil {
		t.Fatal("a version 2 request was accepted")
	}
	if err := writeResult(root, refusedResult(r, err, at)); err != nil {
		t.Fatalf("writeResult: %v", err)
	}
	got, _, _ = channel.ReadResult(dir)
	if got.OK || got.ID != "" || !strings.Contains(got.Error, "version 2") {
		t.Errorf("the answer to an unreadable request is %+v", got)
	}
}
