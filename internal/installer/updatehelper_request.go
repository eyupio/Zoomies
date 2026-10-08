//go:build unix

package installer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// The root side of the update folder. The service account writes the folder
// and may be compromised, and the helper runs as root, so everything in the
// folder is hostile input: read once through one descriptor, never followed
// through a link, and checked on what was read rather than on a path that can
// change between the check and the use. What limits the helper lives outside
// the folder, where the service cannot reset it.
const (
	// helperMaxAttemptsPerTag is where the helper stops and a person looks: two
	// failures of one release are not going to be fixed by a third.
	helperMaxAttemptsPerTag = 2
	// helperMinInterval spaces attempts on one host across every tag, so a loop
	// that names a new tag each time is still one attempt in ten minutes.
	helperMinInterval = 10 * time.Minute
	// helperSeenIDs is how many request ids, and how many tags' attempts, the
	// state remembers. A tag forgotten has had sixty-four others tried since,
	// which at one attempt in ten minutes is most of a day, and the state stays
	// small however long a looping service keeps asking.
	helperSeenIDs = 64

	// helperStateFile is the one file in the state directory.
	helperStateFile = "state.json"
	// maxHelperStateBytes bounds what loading the state reads. The real file is a
	// few kilobytes at its largest.
	maxHelperStateBytes = 64 << 10
	// maxResultErrorBytes and maxResultFieldBytes bound the rest of a result, so
	// that even text that JSON escapes to six bytes a character stays inside the
	// limit the service reads results through.
	maxResultErrorBytes = 2048
	maxResultFieldBytes = 128
)

// helperEUID is the uid the helper's state must belong to. It is a variable so
// that a test, whose files all belong to whoever runs it, can stand for a
// helper whose state somebody else owns without chown.
var helperEUID = os.Geteuid

// fileUID is the ownerOf a real helper passes: the file's owning uid.
func fileUID(fi os.FileInfo) (int, bool) {
	uid, _, ok := fileOwner(fi)
	return uid, ok
}

// readRequest reads the one request in the update folder and refuses anything
// but a plain file, owned by the account the folder belongs to, no larger than
// a request can be, holding exactly the documented shape.
//
// It consumes the request: the name is removed once it has been looked at,
// whatever the verdict, so the path unit fires once per request and a refused
// request is not read again. A refusal comes back with the zero Request and an
// error worth answering with. An empty folder is an error satisfying
// errors.Is(err, fs.ErrNotExist), and there is nobody to answer.
func readRequest(dir *os.Root, wantUID int, ownerOf func(os.FileInfo) (int, bool)) (updates.Request, error) {
	if _, err := dir.Lstat(channel.RequestFile); errors.Is(err, fs.ErrNotExist) {
		return updates.Request{}, fmt.Errorf("there is no %s in the update folder: %w", channel.RequestFile, err)
	}
	r, err := inspectRequest(dir, wantUID, ownerOf)
	// RemoveAll, because a directory planted under the name would otherwise
	// survive and fire the path unit again. It removes a link and never what the
	// link points at, and the root keeps it inside the folder.
	if rmErr := dir.RemoveAll(channel.RequestFile); rmErr != nil && err == nil {
		// A request that cannot be consumed would be read again, so it is not run
		// once either.
		return updates.Request{}, fmt.Errorf("cannot remove %s from the update folder after reading it: %w; the helper does not act on a request it cannot consume", channel.RequestFile, rmErr)
	}
	if err != nil {
		return updates.Request{}, err
	}
	return r, nil
}

// inspectRequest is readRequest without the removal.
//
// The checks run in the order that gives away least. The owner is checked
// before the size and long before the content, so that nothing about a file
// the service does not own (a hard link to one only root can read, say) ends up
// in an answer the service reads.
func inspectRequest(dir *os.Root, wantUID int, ownerOf func(os.FileInfo) (int, bool)) (updates.Request, error) {
	// The owner check is what keeps a hard link to a root-only file out, and it
	// keeps nothing out if the service is root. A service that runs as root can
	// upgrade itself and needs no helper.
	if wantUID == 0 {
		return updates.Request{}, fmt.Errorf("the update folder is recorded as belonging to uid 0, and the helper does not serve root, because every file root can read would then pass as a request; a service that runs as root needs no helper, and otherwise run \"sudo zoomies updates helper install\" again so it records the account zoomies runs as")
	}
	f, info, err := openPlainFile(dir, channel.RequestFile)
	if err != nil {
		return updates.Request{}, err
	}
	defer f.Close()
	uid, ok := ownerOf(info)
	if !ok {
		return updates.Request{}, fmt.Errorf("cannot tell who owns %s, so the helper does not trust it", channel.RequestFile)
	}
	if uid != wantUID {
		return updates.Request{}, fmt.Errorf("%s is owned by uid %d and not by uid %d, the account the update folder belongs to; a rootless or remapped container host owns files under another uid, and the helper does not serve one", channel.RequestFile, uid, wantUID)
	}
	if info.Size() > updates.MaxRequestBytes {
		return updates.Request{}, fmt.Errorf("%s is %d bytes, larger than a request can be (%d)", channel.RequestFile, info.Size(), updates.MaxRequestBytes)
	}
	// One byte more than the limit, so a file that grew since the stat is still
	// refused by the parser and never read whole.
	body, err := io.ReadAll(io.LimitReader(f, updates.MaxRequestBytes+1))
	if err != nil {
		return updates.Request{}, fmt.Errorf("cannot read %s: %w", channel.RequestFile, err)
	}
	return updates.ParseRequest(body)
}

// openPlainFile opens name for reading and hands it back only if the name is a
// plain file, and the file opened is the one the name was.
//
// The name is looked at first, without following it, so that root never opens
// anything but a plain file on the service's say: opening a device can do
// something, and opening a pipe waits for a writer. os.Root follows a link that
// stays inside the root even when the open asks for O_NOFOLLOW, so the flag
// alone refuses only a link out of the folder, and it is the look that refuses
// the rest. The open does not block and the file opened must be the one looked
// at, so a name swapped in between costs an error and not a hang, a terminal or
// a read of something else.
func openPlainFile(dir *os.Root, name string) (*os.File, os.FileInfo, error) {
	named, err := dir.Lstat(name)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot look at %s: %w", name, err)
	}
	if !named.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is a %s and not a plain file", name, fileKind(named.Mode()))
	}
	betweenLookAndOpen(name)
	// O_NOCTTY because the helper is a session leader with no terminal, which a
	// terminal opened without it would become the controlling terminal of.
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open %s: %v", name, err)
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("cannot look at %s once open: %v", name, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(named, opened) {
		f.Close()
		return nil, nil, fmt.Errorf("%s changed while it was being opened", name)
	}
	return f, opened, nil
}

// betweenLookAndOpen runs after openPlainFile has looked at a name and before
// it opens it. It does nothing; it is a variable so that a test can swap the
// name in the one moment a hostile service would.
var betweenLookAndOpen = func(name string) {}

// fileKind says what something that is not a plain file is, for the sentence
// that refuses it.
func fileKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "link"
	case m.IsDir():
		return "folder"
	case m&fs.ModeNamedPipe != 0:
		return "pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	}
	return "special file"
}

// helperState is what the helper remembers between runs: the request ids it
// has seen, newest last, and when each tag was attempted. It is root's, in a
// directory outside the update folder, because state the service could write
// is state the service could reset.
type helperState struct {
	Seen     []string               `json:"seen"`
	Attempts map[string][]time.Time `json:"attempts"`

	dir string
}

// stateRefusal is the end of every sentence that refuses the state: the helper
// fails closed, and the operator decides whether the limits are reset.
const stateRefusal = "the helper refuses every request until it is fixed, because these are the limits on how often root updates this host"

// loadHelperState reads the state in dir, creating the directory, private, if
// there is none yet. No file is a fresh start. A file that is there and cannot
// be trusted or read is an error and never a fresh start: treating it as one
// would reset the limits, which is what corrupting it would be for.
func loadHelperState(dir string) (*helperState, error) {
	root, err := openStateDir(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	path := filepath.Join(dir, helperStateFile)
	s := &helperState{Attempts: map[string][]time.Time{}, dir: dir}
	if _, err := root.Lstat(helperStateFile); errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	f, info, err := openPlainFile(root, helperStateFile)
	if err != nil {
		return nil, fmt.Errorf("the update helper's state %s cannot be used: %w; %s", path, err, stateRefusal)
	}
	defer f.Close()
	if err := checkStateOwner(path, info); err != nil {
		return nil, err
	}
	if info.Size() > maxHelperStateBytes {
		return nil, fmt.Errorf("the update helper's state %s is %d bytes, over the limit of %d; %s", path, info.Size(), maxHelperStateBytes, stateRefusal)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxHelperStateBytes+1))
	if err == nil && len(body) > maxHelperStateBytes {
		err = fmt.Errorf("it is over the limit of %d bytes", maxHelperStateBytes)
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err = dec.Decode(s); err == nil {
			if _, tok := dec.Token(); !errors.Is(tok, io.EOF) {
				err = errors.New("it has more in it than one document")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("the update helper's state %s cannot be read: %v; inspect it, and remove it only if you mean to reset the limits; %s", path, err, stateRefusal)
	}
	if s.Attempts == nil {
		s.Attempts = map[string][]time.Time{}
	}
	return s, nil
}

// openStateDir opens the state directory, making it if it is not there, and
// refuses one the helper does not control. The directory is looked at without
// following a link and must be the one that was opened.
func openStateDir(dir string) (*os.Root, error) {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("cannot create the update helper's state directory %s: %w", dir, err)
	}
	named, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot look at the update helper's state directory %s: %w", dir, err)
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("the update helper's state directory %s is not a folder (it is a %s); remove it so the helper can make its own; %s", dir, fileKind(named.Mode()), stateRefusal)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot open the update helper's state directory %s: %w", dir, err)
	}
	opened, err := root.Stat(".")
	if err != nil {
		err = fmt.Errorf("cannot look at the update helper's state directory %s once open: %w", dir, err)
	} else if !os.SameFile(named, opened) {
		err = fmt.Errorf("the update helper's state directory %s changed while it was being opened; %s", dir, stateRefusal)
	}
	if err == nil {
		err = checkStateOwner(dir, opened)
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// checkStateOwner refuses state the helper's own account does not own, or that
// a group or the world can write, since either could let someone else rewrite
// the limits.
func checkStateOwner(path string, info os.FileInfo) error {
	uid, ok := fileUID(info)
	if !ok {
		return fmt.Errorf("cannot tell who owns the update helper's state %s; %s", path, stateRefusal)
	}
	if want := helperEUID(); uid != want {
		return fmt.Errorf("the update helper's state %s is owned by uid %d and not by uid %d, which the helper runs as; %s", path, uid, want, stateRefusal)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("the update helper's state %s is writable by its group or the world (mode %o); make it writable by its owner alone; %s", path, info.Mode().Perm(), stateRefusal)
	}
	return nil
}

// admit decides whether the helper may act on r at now. It records what it saw
// whatever it decides, so the caller saves the state either way: a refused id
// is still one that has been seen. Only an admitted request counts as an
// attempt.
func (s *helperState) admit(r updates.Request, now time.Time) error {
	if slices.Contains(s.Seen, r.ID) {
		return fmt.Errorf("the request %s has been seen before, and the helper acts on a request once; the controller makes a new request for every attempt", r.ID)
	}
	s.Seen = append(s.Seen, r.ID)
	if over := len(s.Seen) - helperSeenIDs; over > 0 {
		s.Seen = slices.Delete(s.Seen, 0, over)
	}
	tried := s.Attempts[r.Tag]
	if len(tried) >= helperMaxAttemptsPerTag {
		return fmt.Errorf("%s has been tried %d times on this host already, which is as many as the helper allows; find out why it failed, then run \"sudo zoomies upgrade --version %s\" on the host", r.Tag, len(tried), r.Tag)
	}
	if last, ok := s.lastAttempt(now); ok && now.Sub(last) < helperMinInterval {
		return fmt.Errorf("the last update attempt on this host was at %s, and the helper starts at most one every %d minutes; ask again after %s", last.UTC().Format(time.RFC3339), int(helperMinInterval/time.Minute), last.Add(helperMinInterval).UTC().Format(time.RFC3339))
	}
	s.Attempts[r.Tag] = append(tried, now)
	for len(s.Attempts) > helperSeenIDs {
		delete(s.Attempts, s.tagToForget())
	}
	return nil
}

// lastAttempt is the newest attempt at any tag, leaving out any more than the
// interval ahead of now. One that far ahead was recorded before the clock was
// set back, and waiting for the clock to catch up could lock the helper for as
// long as the clock was wrong; it still counts against its own tag.
func (s *helperState) lastAttempt(now time.Time) (time.Time, bool) {
	var last time.Time
	for _, times := range s.Attempts {
		for _, at := range times {
			if at.After(last) && !at.After(now.Add(helperMinInterval)) {
				last = at
			}
		}
	}
	return last, !last.IsZero()
}

// tagToForget is the tag the state can most afford to lose: of the tags that
// can still be tried, the one tried longest ago, and only when every tag has
// reached its limit, the one that reached it longest ago.
//
// Forgetting a tag at its limit hands it two fresh attempts, so that happens
// only once every remembered tag is at its limit too: a service that wants a
// capped tag back has to spend two attempts on each of sixty-four others first,
// which at one attempt in ten minutes is most of a day. That is the residue the
// bound on the state leaves.
func (s *helperState) tagToForget() string {
	if tag, ok := s.stalestTag(func(times []time.Time) bool { return len(times) < helperMaxAttemptsPerTag }); ok {
		return tag
	}
	tag, _ := s.stalestTag(func([]time.Time) bool { return true })
	return tag
}

// stalestTag is the tag, among those include accepts, whose newest attempt is
// the oldest.
func (s *helperState) stalestTag(include func([]time.Time) bool) (string, bool) {
	var stalest string
	var stalestAt time.Time
	found := false
	for tag, times := range s.Attempts {
		if !include(times) {
			continue
		}
		// A tag with no times, which only a hand-edited file holds, counts as the
		// oldest rather than as a reason to stop.
		var newest time.Time
		for _, at := range times {
			if at.After(newest) {
				newest = at
			}
		}
		if !found || newest.Before(stalestAt) {
			stalest, stalestAt, found = tag, newest, true
		}
	}
	return stalest, found
}

// save writes the state atomically, private to root. A helper that cannot save
// must not act on what it admitted, or a restart would forget the attempt.
func (s *helperState) save() error {
	root, err := openStateDir(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("cannot encode the update helper's state: %w", err)
	}
	return replaceFile(root, helperStateFile, body, 0o600)
}

// refusedResult is the answer to a request the helper would not act on. A
// request that could not be read has no id to echo, and is answered all the
// same, so that whoever looks at the folder can see why.
func refusedResult(r updates.Request, err error, at time.Time) updates.Result {
	return updates.Result{V: updates.WireVersion, ID: r.ID, Tag: r.Tag, Error: err.Error(), StartedAt: at, FinishedAt: at}
}

// writeResult answers in the update folder. It never opens result.json: it
// makes a new file under a random name, which cannot already be a link, and
// renames it over the old name, which replaces a planted link rather than
// writing through it. The file is world-readable, since the service has to
// read it and it holds nothing secret.
//
// The text is made valid UTF-8 and bounded first, so the answer stays within
// the limit the service reads results through whatever the engine printed.
func writeResult(dir *os.Root, r updates.Result) error {
	r.V = updates.WireVersion
	r.ID = headOf(r.ID, maxResultFieldBytes)
	r.Tag = headOf(r.Tag, maxResultFieldBytes)
	r.From = headOf(r.From, maxResultFieldBytes)
	r.To = headOf(r.To, maxResultFieldBytes)
	r.Error = headOf(r.Error, maxResultErrorBytes)
	// The reason a run failed is at the end of its output.
	r.LogTail = tailOf(r.LogTail, updates.MaxLogTailBytes)
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("cannot encode the update result: %w", err)
	}
	return replaceFile(dir, channel.ResultFile, body, 0o644)
}

// replaceFile puts body at name inside root without ever opening name: a new
// file under a random name is written, flushed and renamed over it. The
// temporary name is removed on every path that does not end in the rename.
func replaceFile(root *os.Root, name string, body []byte, mode fs.FileMode) error {
	tmp := "." + strings.TrimSuffix(name, filepath.Ext(name)) + "-" + rand.Text() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return fmt.Errorf("cannot create a file beside %s: %w", name, err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = root.Remove(tmp)
		}
	}()
	// Explicit rather than left to the umask, which could make a result the
	// service cannot read.
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("cannot put %s in place: %w", name, err)
	}
	renamed = true
	// The rename is durable only once the directory is; until then a power cut
	// could bring back the old state, and with it limits already used up.
	if d, err := root.Open("."); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// headOf is s as valid UTF-8, cut to at most n bytes on a character boundary.
// Invalid bytes are replaced before the cut and not left to encoding/json,
// which would turn each one into a three-byte replacement after it and take the
// text past n.
func headOf(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailOf is headOf for the end of s.
func tailOf(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
