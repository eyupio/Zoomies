// Package channel is the service's side of the update folder: finding it,
// writing a request into it, and reading what the helper left there.
//
// The folder is the whole conversation between the unprivileged service and the
// root-owned helper. The installer creates it with the service account's
// ownership when the helper is installed, and nothing here ever does, so a
// folder that is missing means there is no helper and says so. This package is
// the impure half of internal/updates, which stays free of files and clocks.
package channel

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/eyupio/zoomies/internal/updates"
)

// The names are fixed: the helper's units and the root side look for exactly
// these, so a name chosen here would be one the other side never opens.
const (
	// RequestFile is what the service writes and the helper's path unit waits for.
	RequestFile = "request.json"
	// ResultFile is what the helper writes beside the request.
	ResultFile = "result.json"
	// MarkerFile says the helper is installed; the installer writes it last.
	MarkerFile = "helper.json"
	// PointerFile records where the update folder is, beside the --config file.
	PointerFile = "update-helper.json"
)

const (
	// maxDocumentBytes bounds the marker and the pointer, which hold a few paths
	// and a name.
	maxDocumentBytes = 4096
	// maxResultBytes bounds a result. A log tail of MaxLogTailBytes can grow to six
	// times that once JSON has escaped every control character in it, and the
	// limit only has to refuse a file that is nothing like one.
	maxResultBytes = 8 * updates.MaxLogTailBytes
)

// ErrRequestPending is returned by WriteRequest while an earlier request is still
// in the folder. The helper removes a request once it has read it, so one that
// is still there has not been picked up, and a second would replace it unseen.
var ErrRequestPending = errors.New("an update request is already waiting for the helper")

// Marker is the helper's own record of itself, written by whoever installs it.
type Marker struct {
	V           int       `json:"v"`
	Version     string    `json:"version"`
	Binary      string    `json:"binary"`
	InstalledAt time.Time `json:"installed_at"`
}

// Pointer is where a native install records the update folder. The service and
// the installer cannot work the path out separately: a non-root service with no
// ZOOMIES_STATE_DIR resolves the state directory under its own home, and the
// installer, running as root, resolves /var/lib/zoomies. So the installer writes
// the answer down, root-owned, beside the configuration file.
type Pointer struct {
	V       int    `json:"v"`
	Dir     string `json:"dir"`
	Binary  string `json:"binary"`
	Account string `json:"account"`
	UID     int    `json:"uid"`
	// ConfigDir is where the deployment's upgrade.lock lives. The helper, which
	// does not read the service's configuration, needs it to see a manual upgrade
	// in progress.
	ConfigDir string `json:"config_dir"`
}

// Locator is what the service knows about where it is running.
type Locator struct {
	// ConfigPath is the --config file, whose directory holds the pointer. It is
	// empty when the service was started without one.
	ConfigPath string
	// SharedDir is the shared folder a container that runs runners mounts at the
	// same path on both sides.
	SharedDir   string
	InContainer bool
}

// Locate finds the update folder, or says there is none to find. It looks at
// nothing but the pointer and the arguments: whether the folder exists, and
// whether it belongs to the account, is what writing to it finds out.
//
// A native install is found only through its pointer. A container has no
// configuration directory of the installer's, and uses <shared>/update.
func Locate(l Locator) (dir string, ok bool) {
	if l.ConfigPath != "" {
		if p, err := ReadPointer(filepath.Join(filepath.Dir(l.ConfigPath), PointerFile)); err == nil {
			return p.Dir, true
		}
	}
	if l.InContainer && l.SharedDir != "" {
		return filepath.Join(l.SharedDir, "update"), true
	}
	return "", false
}

// ReadPointer reads and checks a pointer file. Whoever calls it as root reads a
// copy in a directory only root can write, never one from the update folder.
//
// An absent file is an error satisfying errors.Is(err, fs.ErrNotExist), so that
// "no helper here" can be told from "a pointer I cannot trust". The folder it
// names must be an absolute path: a relative one would be resolved against
// whatever the reader's working directory happens to be.
func ReadPointer(path string) (Pointer, error) {
	var p Pointer
	if err := readDocument(path, maxDocumentBytes, &p); err != nil {
		return Pointer{}, err
	}
	return checkPointer(path, p)
}

// DecodePointer is ReadPointer for a pointer already read, by a caller that
// opened and checked the file itself. name is what the refusals call it.
func DecodePointer(name string, body []byte) (Pointer, error) {
	if len(body) > maxDocumentBytes {
		return Pointer{}, fmt.Errorf("%s is %d bytes, over the limit of %d", name, len(body), maxDocumentBytes)
	}
	var p Pointer
	if err := decodeDocument(name, body, &p); err != nil {
		return Pointer{}, err
	}
	return checkPointer(name, p)
}

func checkPointer(path string, p Pointer) (Pointer, error) {
	if p.V != updates.WireVersion {
		return Pointer{}, fmt.Errorf("%s is version %d and this release reads version %d", path, p.V, updates.WireVersion)
	}
	if !filepath.IsAbs(p.Dir) {
		return Pointer{}, fmt.Errorf("%s names the update folder %q, which is not an absolute path", path, p.Dir)
	}
	return p, nil
}

// ReadResult reads what the helper answered. A result that is not there is not
// an error, because most of the time there is none: the second value says
// whether one was read. A file that is there and cannot be trusted is an error,
// for the caller to ignore or log, and is never half-read.
func ReadResult(dir string) (updates.Result, bool, error) {
	var r updates.Result
	found, err := readOptional(filepath.Join(dir, ResultFile), maxResultBytes, &r)
	if err != nil || !found {
		return updates.Result{}, false, err
	}
	if r.V != updates.WireVersion {
		return updates.Result{}, false, fmt.Errorf("%s is version %d and this release reads version %d", filepath.Join(dir, ResultFile), r.V, updates.WireVersion)
	}
	return r, true, nil
}

// ReadMarker reads the helper's marker. Absent means the helper is not
// installed, which is an answer and not an error.
func ReadMarker(dir string) (Marker, bool, error) {
	var m Marker
	found, err := readOptional(filepath.Join(dir, MarkerFile), maxDocumentBytes, &m)
	if err != nil || !found {
		return Marker{}, false, err
	}
	if m.V != updates.WireVersion {
		return Marker{}, false, fmt.Errorf("%s is version %d and this release reads version %d", filepath.Join(dir, MarkerFile), m.V, updates.WireVersion)
	}
	return m, true, nil
}

// syncRequest flushes the temporary file before it is renamed into place. It is
// a variable so that a test can make the flush fail, which is how a full disk
// shows itself when the write was only buffered.
var syncRequest = func(f *os.File) error { return f.Sync() }

// installHint is the end of every refusal that means the folder is not there as
// the installer leaves it.
const installHint = `run "sudo zoomies updates helper install" on this host, which creates the folder with the right owner`

// WriteRequest hands the helper one request.
//
// It is called by the service account, into a folder the installer made, and it
// never makes that folder: a folder created here would be owned by the wrong
// account, or would appear on a disk the helper does not watch. The request is
// written under a temporary name inside the folder and renamed, so the helper's
// path unit, which fires on request.json existing, never sees half a document.
// On any failure the temporary file is removed, so a failed button leaves the
// folder as it was.
func WriteRequest(dir string, r updates.Request) error {
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("cannot encode the update request: %w", err)
	}
	// What the helper would refuse is refused here, before anything touches the
	// disk, so the answer is immediate and the folder stays clean.
	if _, err := updates.ParseRequest(body); err != nil {
		return fmt.Errorf("refusing to write an update request the helper would refuse: %w", err)
	}

	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("the update folder %s does not exist, so there is no update helper on this host; %s", dir, installHint)
	case err != nil:
		return fmt.Errorf("cannot look at the update folder %s: %w; %s", dir, err, installHint)
	case !info.IsDir():
		// A link is refused with a file: what it points at is not the folder the
		// installer made and gave to this account.
		return fmt.Errorf("the update folder %s is not a folder (it is a %s); remove it and %s", dir, kind(info.Mode()), installHint)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("cannot open the update folder %s: %w; %s", dir, err, installHint)
	}
	defer root.Close()

	if _, err := root.Lstat(RequestFile); err == nil {
		return fmt.Errorf("%w: %s is still in %s", ErrRequestPending, RequestFile, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot look for an earlier request in %s: %w; %s", dir, err, installHint)
	}

	tmp, f, err := createTemp(root)
	if err != nil {
		return fmt.Errorf("cannot write in the update folder %s: %w; it must be owned by the account zoomies runs as, so %s", dir, err, installHint)
	}
	// The name is removed on every path that does not end in the rename, so a
	// failure never leaves a file for the next look to puzzle over.
	renamed := false
	defer func() {
		if !renamed {
			_ = root.Remove(tmp)
		}
	}()

	// Explicit rather than left to the umask: the helper reads this as root, and
	// the group is the one thing that may need to.
	if err := f.Chmod(0o640); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot set the mode of the update request in %s: %w", dir, err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot write the update request in %s: %w", dir, err)
	}
	if err := syncRequest(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("cannot flush the update request in %s: %w", dir, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot finish the update request in %s: %w", dir, err)
	}
	if err := root.Rename(tmp, RequestFile); err != nil {
		return fmt.Errorf("cannot put the update request in place in %s: %w", dir, err)
	}
	renamed = true
	return nil
}

// createTemp makes a new file with a name nobody else is using. The name starts
// with a dot and is not request.json, so the path unit does not mistake it for a
// request.
func createTemp(root *os.Root) (string, *os.File, error) {
	var lastErr error
	for range 8 {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		name := ".request-" + hex.EncodeToString(b[:]) + ".tmp"
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
		if err == nil {
			return name, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
		lastErr = err
	}
	return "", nil, lastErr
}

// kind says what a non-folder is, for the sentence that names it.
func kind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "link"
	case m.IsRegular():
		return "file"
	}
	return "special file"
}

// readOptional is readDocument for a file whose absence is an answer.
func readOptional(path string, limit int64, into any) (found bool, err error) {
	if err := readDocument(path, limit, into); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// readDocument reads one small JSON file written by somebody else and decodes it
// strictly. The file must be a regular file, which is checked on the open
// descriptor and not only on the path, and is read through a limit, so a link, a
// device, a pipe or a large file costs an error and not a hang or the memory.
// An absent file is an error satisfying errors.Is(err, fs.ErrNotExist).
func readDocument(path string, limit int64, into any) error {
	// The name is looked at first and without following a link, because opening a
	// pipe would wait for a writer that may never come.
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("%s is a %s and not a plain file", path, kind(before.Mode()))
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return fmt.Errorf("%s changed while it was being opened", path)
	}
	if after.Size() > limit {
		return fmt.Errorf("%s is %d bytes, over the limit of %d", path, after.Size(), limit)
	}
	// One more byte than the limit, so that a file that grew since the stat is
	// still caught.
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("%s is over the limit of %d bytes", path, limit)
	}
	return decodeDocument(path, body, into)
}

// decodeDocument decodes one document strictly: no unknown field, nothing
// after it.
func decodeDocument(path string, body []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%s is not a document this release reads: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s has more in it than one document", path)
	}
	return nil
}
