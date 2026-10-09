package channel

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/updates"
)

func testRequest(id string) updates.Request {
	return updates.Request{
		V: updates.WireVersion, ID: id, Tag: "v1.3.5", RequestedBy: "user:alice",
		RequestedAt: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC),
	}
}

// updateFolder makes the folder the installer would have made, inside a parent
// the test can list afterwards to prove nothing else was left beside it.
func updateFolder(t *testing.T) (parent, dir string) {
	t.Helper()
	parent = t.TempDir()
	dir = filepath.Join(parent, "update")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return parent, dir
}

// names lists a folder's entries, so that a test can say "this and nothing
// else" and catch a stray temporary file by what it is called.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The folder belongs to the installer, which gives it the account's ownership.
// A service that made it would own a folder root then trusts nothing in, and a
// container without the mount would quietly grow one on its own disk where the
// helper never looks.
func TestWriteRequestIsAtomicAndNeverCreatesTheFolder(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "update")
	err := WriteRequest(missing, testRequest("upd_aaaa"))
	if err == nil {
		t.Fatal("a missing folder was not an error")
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "helper install") {
		t.Errorf("the error should name the folder and say to install the helper, got: %v", err)
	}
	if got := names(t, parent); len(got) != 0 {
		t.Fatalf("a failed write left %v behind", got)
	}

	_, dir := updateFolder(t)
	if err := WriteRequest(dir, testRequest("upd_aaaa")); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{RequestFile}) {
		t.Fatalf("the folder holds %v, want only %s", got, RequestFile)
	}
	body, err := os.ReadFile(filepath.Join(dir, RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	got, err := updates.ParseRequest(body)
	if err != nil {
		t.Fatalf("the file written is not a request the helper would accept: %v", err)
	}
	if got.ID != "upd_aaaa" || got.Tag != "v1.3.5" || got.RequestedBy != "user:alice" {
		t.Errorf("request read back as %+v", got)
	}
	info, err := os.Stat(filepath.Join(dir, RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("request mode = %o, want 640", perm)
	}
}

// The helper would refuse a request with a bad tag, but only after the service
// had told a person it was on its way. Refusing here keeps the folder clean and
// the answer immediate.
func TestWriteRequestRefusesARequestTheHelperWouldRefuse(t *testing.T) {
	_, dir := updateFolder(t)
	bad := testRequest("upd_aaaa")
	bad.Tag = "latest"
	if err := WriteRequest(dir, bad); err == nil {
		t.Fatal("a request with a bad tag was written")
	}
	if got := names(t, dir); len(got) != 0 {
		t.Fatalf("a refused request left %v behind", got)
	}
}

func TestWriteRequestRefusesWhileAnEarlierOneIsWaiting(t *testing.T) {
	_, dir := updateFolder(t)
	if err := WriteRequest(dir, testRequest("upd_first")); err != nil {
		t.Fatal(err)
	}
	err := WriteRequest(dir, testRequest("upd_second"))
	if !errors.Is(err, ErrRequestPending) {
		t.Fatalf("second write = %v, want ErrRequestPending", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "upd_first") || strings.Contains(string(body), "upd_second") {
		t.Errorf("the waiting request was replaced: %s", body)
	}
	if got := names(t, dir); !slices.Equal(got, []string{RequestFile}) {
		t.Errorf("the folder holds %v, want only %s", got, RequestFile)
	}
}

// A request.json that is a link is looked at without being followed, so a link
// planted there (even a dangling one, which a following stat would call absent)
// counts as an earlier request and is neither replaced nor written through.
func TestWriteRequestCountsAPlantedLinkAsAWaitingRequest(t *testing.T) {
	_, dir := updateFolder(t)
	planted := filepath.Join(dir, RequestFile)
	if err := os.Symlink(filepath.Join(dir, "nowhere"), planted); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	err := WriteRequest(dir, testRequest("upd_aaaa"))
	if !errors.Is(err, ErrRequestPending) {
		t.Fatalf("WriteRequest = %v, want ErrRequestPending", err)
	}
	if info, err := os.Lstat(planted); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the planted link was replaced: %v, %v", info, err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{RequestFile}) {
		t.Errorf("the folder holds %v, want only the planted link", got)
	}
}

// The helper's path unit fires on request.json existing, so the document has to
// be complete before that name appears. The flush is the last step before the
// rename: if the name is already there by then, the request is being written in
// place and the helper could read half of it.
func TestWriteRequestDoesNotShowTheNameUntilTheDocumentIsComplete(t *testing.T) {
	_, dir := updateFolder(t)
	original := syncRequest
	t.Cleanup(func() { syncRequest = original })
	syncRequest = func(f *os.File) error {
		if _, err := os.Lstat(filepath.Join(dir, RequestFile)); err == nil {
			t.Error("request.json existed before the document was flushed")
		}
		return original(f)
	}
	if err := WriteRequest(dir, testRequest("upd_aaaa")); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
}

// Review Focus 1: a folder that has become a file. The button has to end in an
// error that names the folder, with nothing written, not in a half-made file
// the helper might one day find.
func TestWriteRequestFailsCleanlyWhenTheFolderIsAFile(t *testing.T) {
	parent := t.TempDir()
	notAFolder := filepath.Join(parent, "update")
	write(t, notAFolder, "in the way")

	err := WriteRequest(notAFolder, testRequest("upd_aaaa"))
	if err == nil {
		t.Fatal("writing into a file was not an error")
	}
	if !strings.Contains(err.Error(), notAFolder) {
		t.Errorf("the error should name the folder, got: %v", err)
	}
	if got := names(t, parent); !slices.Equal(got, []string{"update"}) {
		t.Errorf("the parent holds %v, want only the file that was in the way", got)
	}
	if body, _ := os.ReadFile(notAFolder); string(body) != "in the way" {
		t.Errorf("the file in the way was changed to %q", body)
	}
}

// Review Focus 1: a folder replaced by a link. Whatever it points at is not the
// folder the installer made and gave the account, so nothing is written there.
func TestWriteRequestFailsCleanlyWhenTheFolderIsReplacedByALink(t *testing.T) {
	parent := t.TempDir()
	elsewhere := filepath.Join(parent, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "update")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	err := WriteRequest(link, testRequest("upd_aaaa"))
	if err == nil {
		t.Fatal("writing through a link was not an error")
	}
	if !strings.Contains(err.Error(), link) {
		t.Errorf("the error should name the folder, got: %v", err)
	}
	if got := names(t, elsewhere); len(got) != 0 {
		t.Errorf("the link's target holds %v, want nothing", got)
	}
}

// Review Focus 1: the wrong owner, or a full disk. A folder the account cannot
// write into fails on creating the temporary file; a disk that fills fails
// later, on the flush, with a file already made. Both must leave nothing.
func TestWriteRequestFailsCleanlyWhenTheFolderCannotBeWrittenTo(t *testing.T) {
	t.Run("the account may not write there", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write to any folder, so there is no refusal to see")
		}
		_, dir := updateFolder(t)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })
		err := WriteRequest(dir, testRequest("upd_aaaa"))
		if err == nil {
			t.Fatal("writing to a folder the account cannot write was not an error")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("the error should name the folder, got: %v", err)
		}
		if got := names(t, dir); len(got) != 0 {
			t.Errorf("the folder holds %v, want nothing", got)
		}
	})
	t.Run("the disk fills after the file is made", func(t *testing.T) {
		_, dir := updateFolder(t)
		full := errors.New("no space left on device")
		original := syncRequest
		syncRequest = func(*os.File) error { return full }
		t.Cleanup(func() { syncRequest = original })

		err := WriteRequest(dir, testRequest("upd_aaaa"))
		if !errors.Is(err, full) {
			t.Fatalf("WriteRequest = %v, want the disk's error", err)
		}
		if got := names(t, dir); len(got) != 0 {
			t.Errorf("a failed write left %v behind", got)
		}
	})
}

func TestReadResultIsAbsentNotAnError(t *testing.T) {
	_, dir := updateFolder(t)
	if _, ok, err := ReadResult(dir); ok || err != nil {
		t.Errorf("ReadResult in an empty folder = ok %v, err %v, want absent", ok, err)
	}
	if _, ok, err := ReadResult(filepath.Join(dir, "missing")); ok || err != nil {
		t.Errorf("ReadResult in a missing folder = ok %v, err %v, want absent", ok, err)
	}
}

func TestReadResultReadsWhatTheHelperWrote(t *testing.T) {
	_, dir := updateFolder(t)
	write(t, filepath.Join(dir, ResultFile), `{"v":1,"id":"upd_aaaa","ok":false,"tag":"v1.3.5","from":"v1.3.4","to":"",
 "error":"the upgrade cannot go on without this","log_tail":"line one\nline two",
 "started_at":"2026-10-08T09:00:05Z","finished_at":"2026-10-08T09:04:05Z"}`)
	got, ok, err := ReadResult(dir)
	if err != nil || !ok {
		t.Fatalf("ReadResult = ok %v, err %v", ok, err)
	}
	if got.ID != "upd_aaaa" || got.OK || got.Tag != "v1.3.5" || got.From != "v1.3.4" ||
		got.Error != "the upgrade cannot go on without this" || got.LogTail != "line one\nline two" ||
		!got.FinishedAt.Equal(time.Date(2026, 10, 8, 9, 4, 5, 0, time.UTC)) {
		t.Errorf("result read back as %+v", got)
	}
}

// A result is a file somebody else wrote, left from before a restart or planted
// by the account itself. A huge one must cost a stat, not the memory to read it.
func TestReadResultRefusesAnOversizedFile(t *testing.T) {
	_, dir := updateFolder(t)
	body := `{"v":1,"id":"upd_aaaa","ok":true,"log_tail":"` + strings.Repeat("x", maxResultBytes) + `"}`
	write(t, filepath.Join(dir, ResultFile), body)
	_, ok, err := ReadResult(dir)
	if err == nil || ok {
		t.Fatalf("ReadResult = ok %v, err %v, want an error", ok, err)
	}
}

func TestReadResultRefusesInvalidJSON(t *testing.T) {
	for name, body := range map[string]string{
		"not json":                        "this is not json",
		"truncated":                       `{"v":1,"id":"upd_aaaa"`,
		"a field it does not know":        `{"v":1,"id":"upd_aaaa","ok":true,"run":"rm -rf /"}`,
		"a wire version it does not know": `{"v":2,"id":"upd_aaaa","ok":true}`,
		"no wire version":                 `{"id":"upd_aaaa","ok":true}`,
		"two documents":                   `{"v":1,"id":"upd_aaaa","ok":true}{"v":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, dir := updateFolder(t)
			write(t, filepath.Join(dir, ResultFile), body)
			_, ok, err := ReadResult(dir)
			if err == nil || ok {
				t.Errorf("ReadResult = ok %v, err %v, want an error", ok, err)
			}
		})
	}
}

func TestReadResultRefusesAResultThatIsNotAFile(t *testing.T) {
	_, dir := updateFolder(t)
	if err := os.Mkdir(filepath.Join(dir, ResultFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadResult(dir); err == nil || ok {
		t.Errorf("ReadResult on a folder = ok %v, err %v, want an error", ok, err)
	}
}

func TestReadMarkerSaysWhetherTheHelperIsInstalled(t *testing.T) {
	_, dir := updateFolder(t)
	if _, ok, err := ReadMarker(dir); ok || err != nil {
		t.Errorf("ReadMarker without a marker = ok %v, err %v, want absent", ok, err)
	}
	write(t, filepath.Join(dir, MarkerFile), `{"v":1,"version":"1.3.4","binary":"/usr/local/bin/zoomies","installed_at":"2026-10-01T08:00:00Z"}`)
	got, ok, err := ReadMarker(dir)
	if err != nil || !ok {
		t.Fatalf("ReadMarker = ok %v, err %v", ok, err)
	}
	if got.Version != "1.3.4" || got.Binary != "/usr/local/bin/zoomies" || !got.InstalledAt.Equal(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("marker read back as %+v", got)
	}
}

func TestReadMarkerRefusesWhatItCannotTrust(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":      "{",
		"an unknown field":  `{"v":1,"version":"1.3.4","extra":true}`,
		"another version":   `{"v":9,"version":"1.3.4"}`,
		"an oversized file": `{"v":1,"version":"` + strings.Repeat("x", MaxDocumentBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, dir := updateFolder(t)
			write(t, filepath.Join(dir, MarkerFile), body)
			if _, ok, err := ReadMarker(dir); err == nil || ok {
				t.Errorf("ReadMarker = ok %v, err %v, want an error", ok, err)
			}
		})
	}
}

func writePointer(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, PointerFile)
	write(t, path, body)
	return path
}

func TestReadPointerReadsWhatTheInstallerRecorded(t *testing.T) {
	dir := t.TempDir()
	path := writePointer(t, dir, `{"v":1,"dir":"/var/lib/zoomies/update","binary":"/usr/local/bin/zoomies","account":"zoomies","uid":998,"config_dir":"/etc/zoomies"}`)
	got, err := ReadPointer(path)
	if err != nil {
		t.Fatalf("ReadPointer: %v", err)
	}
	want := Pointer{V: 1, Dir: "/var/lib/zoomies/update", Binary: "/usr/local/bin/zoomies", Account: "zoomies", UID: 998, ConfigDir: "/etc/zoomies"}
	if got != want {
		t.Errorf("pointer = %+v, want %+v", got, want)
	}
}

func TestReadPointerSaysAbsentInAWayACallerCanTest(t *testing.T) {
	_, err := ReadPointer(filepath.Join(t.TempDir(), PointerFile))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadPointer of a missing file = %v, want an error satisfying fs.ErrNotExist", err)
	}
}

// The root helper reads its own copy of this file, so whatever it refuses here
// is something it refuses as root: the cases are the ones a planted file would
// need to get through.
func TestReadPointerRefusesWhatItCannotTrust(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":      "{",
		"an unknown field":  `{"v":1,"dir":"/var/lib/zoomies/update","run":"x"}`,
		"another version":   `{"v":2,"dir":"/var/lib/zoomies/update"}`,
		"no version":        `{"dir":"/var/lib/zoomies/update"}`,
		"no folder":         `{"v":1}`,
		"a relative folder": `{"v":1,"dir":"update"}`,
		"an oversized file": `{"v":1,"dir":"/var/lib/zoomies/update","account":"` + strings.Repeat("x", MaxDocumentBytes) + `"}`,
		"two documents":     `{"v":1,"dir":"/a"}{"v":1,"dir":"/b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writePointer(t, t.TempDir(), body)
			if got, err := ReadPointer(path); err == nil {
				t.Errorf("ReadPointer accepted %q as %+v", body, got)
			}
			// The helper decodes root's copy from a descriptor it has checked, and
			// has to refuse exactly what ReadPointer refuses.
			if got, err := DecodePointer(PointerFile, []byte(body)); err == nil {
				t.Errorf("DecodePointer accepted %q as %+v", body, got)
			}
		})
	}
	t.Run("decoded from bytes already read", func(t *testing.T) {
		dir := t.TempDir()
		body, _ := json.Marshal(Pointer{V: 1, Dir: dir, Binary: "/usr/local/bin/zoomies", Account: "zoomies", UID: 999})
		if got, err := DecodePointer(PointerFile, body); err != nil || got.Dir != dir || got.UID != 999 {
			t.Errorf("DecodePointer = %+v, %v", got, err)
		}
	})

	t.Run("a folder in its place", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, PointerFile), 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPointer(filepath.Join(dir, PointerFile)); err == nil {
			t.Error("a folder was read as a pointer")
		}
	})
	t.Run("a link in its place", func(t *testing.T) {
		dir := t.TempDir()
		real := writePointer(t, t.TempDir(), `{"v":1,"dir":"/var/lib/zoomies/update"}`)
		if err := os.Symlink(real, filepath.Join(dir, PointerFile)); err != nil {
			t.Skipf("cannot make a symbolic link here: %v", err)
		}
		if _, err := ReadPointer(filepath.Join(dir, PointerFile)); err == nil {
			t.Error("a link was read as a pointer")
		}
	})
}

// The service and the installer run as different users, so the service's own
// idea of where the state directory is can differ from the installer's. The copy
// the installer wrote beside the configuration file is the only thing both agree on.
func TestLocatePrefersThePointerBesideTheConfigFile(t *testing.T) {
	configDir := t.TempDir()
	writePointer(t, configDir, `{"v":1,"dir":"/var/lib/zoomies/update","binary":"/usr/local/bin/zoomies","account":"zoomies","uid":998,"config_dir":"`+configDir+`"}`)

	dir, ok := Locate(Locator{ConfigPath: filepath.Join(configDir, "zoomies.yaml"), SharedDir: "/srv/shared", InContainer: true})
	if !ok || dir != "/var/lib/zoomies/update" {
		t.Errorf("Locate = %q, %v, want the pointer's folder", dir, ok)
	}
	dir, ok = Locate(Locator{ConfigPath: filepath.Join(configDir, "zoomies.yaml")})
	if !ok || dir != "/var/lib/zoomies/update" {
		t.Errorf("Locate on a native install = %q, %v, want the pointer's folder", dir, ok)
	}
}

func TestLocateFallsBackToTheSharedFolderInAContainer(t *testing.T) {
	configDir := t.TempDir() // no pointer in it
	dir, ok := Locate(Locator{ConfigPath: filepath.Join(configDir, "zoomies.yaml"), SharedDir: "/srv/shared", InContainer: true})
	if !ok || dir != filepath.Join("/srv/shared", "update") {
		t.Errorf("Locate = %q, %v, want /srv/shared/update", dir, ok)
	}
}

func TestLocateFindsNothingOtherwise(t *testing.T) {
	configDir := t.TempDir()
	config := filepath.Join(configDir, "zoomies.yaml")
	for name, in := range map[string]Locator{
		"a native install with no pointer":       {ConfigPath: config, SharedDir: "/srv/shared"},
		"a container with no shared folder":      {ConfigPath: config, InContainer: true},
		"no configuration file and no container": {},
		"a container with nothing at all":        {InContainer: true},
	} {
		if dir, ok := Locate(in); ok {
			t.Errorf("%s: Locate = %q, want nothing", name, dir)
		}
	}

	// A pointer that cannot be trusted is not a place to write to, and a native
	// install has nowhere else to look.
	writePointer(t, configDir, `{"v":1,"dir":"relative/update"}`)
	if dir, ok := Locate(Locator{ConfigPath: config, SharedDir: "/srv/shared"}); ok {
		t.Errorf("an unusable pointer was followed to %q", dir)
	}
}
