//go:build unix

package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

// UpdateHelperStateDir is root's own directory for the update helper: its
// limits, and its copy of the pointer saying where the update folder is and
// whose it is. Only root can write it, which is the point: what decides what
// root does must not be anything the service can change.
const UpdateHelperStateDir = "/var/lib/zoomies-update"

const (
	// helperVersionTimeout bounds asking the installed binary its version, which
	// answers at once or is broken.
	helperVersionTimeout = time.Minute
	// helperOutputKept is how much of the engine's output the helper holds; the
	// result's log tail is cut from the end of it.
	helperOutputKept = 64 << 10
	// helperLogLineBytes bounds one line of the helper's log, so one line of the
	// engine's output with no end cannot be a log of its own.
	helperLogLineBytes = 4096
	// helperStopGrace is how long the engine is given to remove upgrade.lock and
	// stop once the helper is told to stop. Killed at once, it would leave the
	// lock behind and every later request refused over it.
	helperStopGrace = 30 * time.Second
	// maxVersionBytes bounds the version the installed binary reports, which is a
	// few characters for any real build.
	maxVersionBytes = 128
)

// HelperOptions is what one run of the helper needs. In production it comes
// from root's copy of the pointer (HelperOptionsFromPointer) and never from a
// flag or the update folder; the unexported fields are seams for tests.
type HelperOptions struct {
	// Dir is the update folder.
	Dir string
	// StateDir is where the helper keeps its limits, UpdateHelperStateDir.
	StateDir string
	// BinaryPath is the installed zoomies, which is asked its version and runs
	// the upgrade.
	BinaryPath string
	// LockPath is the deployment's upgrade.lock. While it exists the helper runs
	// nothing.
	LockPath string
	// ServiceUID is the account the folder and the request must belong to. It is
	// root's record of that account, never the folder's owner, which is what is
	// being checked.
	ServiceUID int
	Now        func() time.Time
	// Out is the helper's log: stdout, which the unit sends to the journal.
	Out io.Writer

	ownerOf          func(os.FileInfo) (int, bool)
	installedVersion func(ctx context.Context) (string, error)
	upgrade          func(ctx context.Context, tag string) (output string, err error)
}

// pointerHint is the end of every refusal of root's pointer: reinstalling the
// helper writes it afresh.
const pointerHint = `run "sudo zoomies updates helper install" again to write it afresh`

// HelperOptionsFromPointer reads root's copy of the pointer in stateDir and
// checks every field the helper uses.
func HelperOptionsFromPointer(stateDir string) (HelperOptions, error) {
	return helperOptionsFromPointer(stateDir, fileUID)
}

func helperOptionsFromPointer(stateDir string, ownerOf func(os.FileInfo) (int, bool)) (HelperOptions, error) {
	// The directory is root's and writable by root alone, which openStateDir
	// checks, so nobody else can swap the pointer between the look below and the
	// read after it.
	root, err := openStateDir(stateDir)
	if err != nil {
		return HelperOptions{}, err
	}
	defer root.Close()
	path := filepath.Join(stateDir, channel.PointerFile)
	if _, err := root.Lstat(channel.PointerFile); errors.Is(err, fs.ErrNotExist) {
		return HelperOptions{}, fmt.Errorf(`the update helper is not installed on this host: %s is missing; run "sudo zoomies updates helper install" to install it`, path)
	}
	f, info, err := openPlainFile(root, channel.PointerFile)
	if err != nil {
		return HelperOptions{}, fmt.Errorf("the update helper's pointer %s cannot be used: %w; %s", path, err, pointerHint)
	}
	f.Close()
	if err := checkRootOwns("the update helper's pointer", path, info, ownerOf); err != nil {
		return HelperOptions{}, err
	}
	p, err := channel.ReadPointer(path)
	if err != nil {
		return HelperOptions{}, fmt.Errorf("the update helper's pointer cannot be used: %w; %s", err, pointerHint)
	}
	if err := checkPointer(path, p, ownerOf); err != nil {
		return HelperOptions{}, err
	}
	return HelperOptions{
		Dir:        p.Dir,
		StateDir:   stateDir,
		BinaryPath: p.Binary,
		LockPath:   filepath.Join(p.ConfigDir, "upgrade.lock"),
		ServiceUID: p.UID,
	}, nil
}

// accountName is the shape of an account name the helper will repeat in its
// sentences, which is every name useradd makes.
var accountName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}\$?$`)

// checkPointer checks the fields ReadPointer leaves alone. ReadPointer only
// knows the document; what the paths are on this host is the helper's to ask.
func checkPointer(path string, p channel.Pointer, ownerOf func(os.FileInfo) (int, bool)) error {
	switch {
	case p.UID <= 0:
		return fmt.Errorf("%s records uid %d as the account zoomies runs as, and the helper serves only an unprivileged account; a service that runs as root needs no helper, and otherwise %s", path, p.UID, pointerHint)
	case !accountName.MatchString(p.Account):
		return fmt.Errorf("%s records the account %q, which is not an account name; %s", path, p.Account, pointerHint)
	case !cleanAbs(p.Dir):
		return fmt.Errorf("%s names the update folder %q, which is not a clean absolute path; %s", path, p.Dir, pointerHint)
	case !cleanAbs(p.Binary):
		return fmt.Errorf("%s names the binary %q, which is not a clean absolute path; %s", path, p.Binary, pointerHint)
	case !cleanAbs(p.ConfigDir):
		return fmt.Errorf("%s names the configuration directory %q, which is not a clean absolute path; %s", path, p.ConfigDir, pointerHint)
	}
	if err := checkHelperBinary(p.Binary, ownerOf); err != nil {
		return err
	}
	// The configuration directory is only where the helper looks for the lock.
	// A lock planted there stops updates and nothing else, which the service
	// could do anyway by not asking, so it needs to be a folder and no more.
	if info, err := os.Stat(p.ConfigDir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s names the configuration directory %s, which is not a folder on this host (%v); the helper looks there for upgrade.lock, so %s", path, p.ConfigDir, err, pointerHint)
	}
	return nil
}

func cleanAbs(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }

// checkHelperBinary refuses a binary that anybody but root could change: root
// runs it on the service's say, so a binary the service could replace would be
// the service running as root. The folder it is in counts as well, since
// whoever can write that can put another file under the name.
func checkHelperBinary(binary string, ownerOf func(os.FileInfo) (int, bool)) error {
	info, err := os.Lstat(binary)
	if err != nil {
		return fmt.Errorf("cannot look at the zoomies binary %s: %w; %s", binary, err, pointerHint)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the zoomies binary %s is a %s and not a plain file, and the helper runs only the file it was given, not whatever a link points at now; %s", binary, fileKind(info.Mode()), pointerHint)
	}
	if err := checkRootOwns("the zoomies binary", binary, info, ownerOf); err != nil {
		return err
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("the zoomies binary %s is not executable (mode %o); %s", binary, info.Mode().Perm(), pointerHint)
	}
	dir := filepath.Dir(binary)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("cannot look at %s, the folder the zoomies binary is in: %w", dir, err)
	}
	return checkRootOwns("the zoomies binary's folder", dir, dirInfo, ownerOf)
}

// checkRootOwns refuses something root relies on that root does not own, or
// that a group or the world can write.
func checkRootOwns(what, path string, info os.FileInfo, ownerOf func(os.FileInfo) (int, bool)) error {
	uid, ok := ownerOf(info)
	if !ok {
		return fmt.Errorf("cannot tell who owns %s %s, so the helper does not use it", what, path)
	}
	if want := helperEUID(); uid != want {
		return fmt.Errorf("%s %s is owned by uid %d and not by uid %d, which the helper runs as; the helper uses only what root alone can change, so give it to root or %s", what, path, uid, want, pointerHint)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s %s is writable by its group or the world (mode %o); make it writable by root alone", what, path, info.Mode().Perm())
	}
	return nil
}

// RunUpdateHelper answers the one request in the update folder: it reads it,
// admits it against its limits and saves them, checks for a manual upgrade and
// for the release already being installed, runs the engine as a child process,
// and writes down how it went.
//
// The error is nil whenever a result was written, whatever it says: the answer
// is result.json and the log. An error means the helper could not answer at
// all, because the folder is not one it will write in, or the write failed.
func RunUpdateHelper(ctx context.Context, opts HelperOptions) error {
	h := newUpdateHelper(opts)
	dir, err := openUpdateFolder(h.opts.Dir, h.opts.ServiceUID, h.opts.ownerOf)
	if err != nil {
		h.logf("cannot answer: %v", err)
		return err
	}
	defer dir.Close()
	started := h.opts.Now()
	r, err := readRequest(dir, h.opts.ServiceUID, h.opts.ownerOf)
	if errors.Is(err, fs.ErrNotExist) {
		h.logf("there is no request in %s, so there is nothing to answer", h.opts.Dir)
		return nil
	}
	if err != nil {
		return h.answer(dir, refusedResult(r, err, started))
	}
	// ParseRequest admits only an id, a tag and a requested_by of printable
	// characters; the log line is made printable all the same.
	h.logf("request %s asks for %s, from %s at %s", r.ID, r.Tag, r.RequestedBy, r.RequestedAt.UTC().Format(time.RFC3339))
	return h.answer(dir, h.act(ctx, r, started))
}

// updateHelper is one run of the helper with its defaults filled in.
type updateHelper struct{ opts HelperOptions }

func newUpdateHelper(opts HelperOptions) *updateHelper {
	h := &updateHelper{opts: opts}
	if h.opts.Now == nil {
		h.opts.Now = time.Now
	}
	if h.opts.Out == nil {
		h.opts.Out = io.Discard
	}
	if h.opts.ownerOf == nil {
		h.opts.ownerOf = fileUID
	}
	if h.opts.installedVersion == nil {
		h.opts.installedVersion = h.runVersion
	}
	if h.opts.upgrade == nil {
		h.opts.upgrade = h.runEngine
	}
	return h
}

// act decides and runs. The order is the design's: the limits are consulted
// and saved before anything else, so an attempt is counted however it ends,
// and nothing runs that the saved state does not know about.
func (h *updateHelper) act(ctx context.Context, r updates.Request, started time.Time) updates.Result {
	refuse := func(err error) updates.Result { return refusedResult(r, err, started) }

	state, err := loadHelperState(h.opts.StateDir)
	if err != nil {
		// The full reason goes to root's log. The answer is read by the service,
		// so it says where to look and quotes nothing of the state.
		h.logf("%v", err)
		return refuse(fmt.Errorf("the update helper's state in %s cannot be used, so the helper refuses every request until it is fixed; the reason is in the helper's log (journalctl -u zoomies-update)", h.opts.StateDir))
	}
	admitted := state.admit(r, started)
	if err := state.save(); err != nil {
		h.logf("%v", err)
		return refuse(fmt.Errorf("the update helper cannot record this attempt in %s, and does not act on what it could not write down; the reason is in the helper's log (journalctl -u zoomies-update)", h.opts.StateDir))
	}
	if admitted != nil {
		return refuse(admitted)
	}

	// Looked for and never taken: the engine takes it, and a lock taken here
	// would be one the engine then refuses over.
	if _, err := os.Stat(h.opts.LockPath); err == nil {
		return refuse(fmt.Errorf("%s exists, so an upgrade is already running on this host or one was interrupted, and the helper does not run over it; if no upgrade is running, check that it has stopped, remove %s and ask again", h.opts.LockPath, h.opts.LockPath))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return refuse(fmt.Errorf("cannot look for %s (%v), and the helper does not run while it cannot tell whether an upgrade is", h.opts.LockPath, err))
	}

	installed, err := h.installedRelease(ctx, r.Tag)
	if err != nil {
		return refuse(err)
	}
	result := updates.Result{ID: r.ID, Tag: r.Tag, From: installed, StartedAt: started}
	switch version.CompareBuilds(installed, r.Tag) {
	case version.SkewNone, version.SkewAhead:
		// A downgrade is a no-op, and the engine would restart the service and
		// pull images even for the release already installed.
		h.logf("%s is already installed (%s), so there is nothing to run", r.Tag, installed)
		result.OK = true
		return result
	case version.SkewDiffers:
		return refuse(fmt.Errorf("the helper cannot tell whether %s is newer than %s, which this host runs; update it by hand with \"sudo zoomies upgrade --version %s\"", r.Tag, installed, r.Tag))
	}

	h.logf("running %s %s", h.opts.BinaryPath, strings.Join(upgradeArgs(r.Tag), " "))
	output, err := h.opts.upgrade(ctx, r.Tag)
	result.LogTail = output
	if err != nil {
		result.Error = engineSentence(ctx, output, err, r.Tag)
		return result
	}
	// The engine's word is checked against the binary's: a run that exited 0
	// and left the old release in place is not an update.
	after, err := h.installedRelease(ctx, r.Tag)
	if err != nil {
		result.Error = fmt.Sprintf("zoomies upgrade finished, but then %v", err)
		return result
	}
	switch version.CompareBuilds(after, r.Tag) {
	case version.SkewNone, version.SkewAhead:
		result.OK, result.To = true, after
	default:
		result.Error = fmt.Sprintf("zoomies upgrade finished, but %s still reports %s and not %s; see the log tail", h.opts.BinaryPath, after, r.Tag)
	}
	return result
}

// installedRelease is the installed binary's version, refused unless it is a
// release: CompareBuilds cannot order a build from main against a tag, and a
// wrong order is a downgrade.
func (h *updateHelper) installedRelease(ctx context.Context, tag string) (string, error) {
	v, err := h.opts.installedVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot tell which release %s is: %v", h.opts.BinaryPath, err)
	}
	if len(v) > maxVersionBytes {
		return "", fmt.Errorf("%s reported a version of %d bytes, longer than any release's, so the helper cannot tell what is installed", h.opts.BinaryPath, len(v))
	}
	if _, ok := version.Release(v); !ok {
		return "", fmt.Errorf("this host runs %q, which is not a release build, so the helper cannot tell whether %s is newer; update it by hand with \"sudo zoomies upgrade --version %s\"", v, tag, tag)
	}
	return v, nil
}

// upgradeArgs is the whole of what the helper asks of the engine. Never --yes:
// that approves deployment additions, and only an operator at the host may.
func upgradeArgs(tag string) []string {
	return []string{"upgrade", "--version", tag, "--non-interactive"}
}

// runEngine runs the installed binary's upgrade as a child process. It cannot
// run in this one, because the engine re-executes itself into the new binary.
// The environment is the helper's own, root's, which is where the release
// source (ZOOMIES_BASE_URL, ZOOMIES_REPO) comes from, and never the request.
func (h *updateHelper) runEngine(ctx context.Context, tag string) (string, error) {
	cmd := exec.CommandContext(ctx, h.opts.BinaryPath, upgradeArgs(tag)...)
	cmd.Env = os.Environ()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = helperStopGrace
	kept := &outputTail{max: helperOutputKept}
	log := &logLines{out: h.opts.Out}
	// One writer for both, so exec writes to it from one goroutine at a time.
	both := io.MultiWriter(kept, log)
	cmd.Stdout, cmd.Stderr = both, both
	err := cmd.Run()
	log.flush()
	return kept.String(), err
}

// runVersion asks the installed binary which release it is. It prints
// "1.3.5 (abc1234)", and the first word is the version.
func (h *updateHelper) runVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, helperVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.opts.BinaryPath, "version", "--short")
	cmd.WaitDelay = 10 * time.Second
	out := &outputTail{max: 4096}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s version --short: %w", h.opts.BinaryPath, err)
	}
	fields := strings.Fields(out.String())
	if len(fields) == 0 {
		return "", fmt.Errorf("%s version --short printed nothing", h.opts.BinaryPath)
	}
	return fields[0], nil
}

// engineSentence is what a failed run is answered with: the engine's own last
// sentence, which is where it says why (a required addition's refusal, a
// failed pre-flight, a service that did not come back), without the prefixes
// the command line puts on it.
func engineSentence(ctx context.Context, output string, err error, tag string) string {
	if ctx.Err() != nil {
		return fmt.Sprintf("the helper was stopped while \"zoomies upgrade --version %s\" was running; check whether it finished with \"zoomies version\", and whether it left upgrade.lock behind", tag)
	}
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		for _, prefix := range []string{"zoomies upgrade: ", "zoomies update: ", "installer: "} {
			line = strings.TrimPrefix(line, prefix)
		}
		return line
	}
	return fmt.Sprintf("\"zoomies upgrade --version %s\" failed (%v) and printed nothing; run it by hand on the host to see why", tag, err)
}

// answer writes the result and logs what it says.
func (h *updateHelper) answer(dir *os.Root, r updates.Result) error {
	r.FinishedAt = h.opts.Now()
	if err := writeResult(dir, r); err != nil {
		h.logf("cannot answer in %s: %v", h.opts.Dir, err)
		return fmt.Errorf("the update helper cannot write its answer in %s: %w", h.opts.Dir, err)
	}
	switch {
	case r.OK && r.To != "":
		h.logf("answered %s: updated from %s to %s", r.ID, r.From, r.To)
	case r.OK:
		h.logf("answered %s: %s was already installed", r.ID, r.Tag)
	default:
		h.logf("answered %s: %s", r.ID, r.Error)
	}
	return nil
}

// logf writes one line of the helper's log. Everything in it is bounded and
// printable: some of it was written by the service, and a log root keeps is no
// place for a line the service forged or an escape that repaints a terminal.
func (h *updateHelper) logf(format string, a ...any) {
	fmt.Fprintln(h.opts.Out, headOf(printable(fmt.Sprintf(format, a...), false), helperLogLineBytes))
}

// printable is s with nothing in it that is not text: invalid bytes, control
// characters, and the formatting characters that reorder what a reader sees or
// end a line where none was written. The test is the one ParseRequest applies
// to requested_by. In lines, newlines and tabs are kept and a carriage return
// is dropped; otherwise both become spaces, so the text stays one line.
func printable(s string, lines bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case lines && (r == '\n' || r == '\t'):
			return r
		case lines && r == '\r':
			return -1
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case !unicode.IsGraphic(r):
			return '�'
		}
		return r
	}, s)
}

// outputTail keeps the last max bytes written to it, so a chatty engine costs
// bounded memory.
type outputTail struct {
	max int
	b   []byte
}

func (t *outputTail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[:copy(t.b, t.b[len(t.b)-t.max:])]
	}
	return len(p), nil
}

func (t *outputTail) String() string { return string(t.b) }

// logLines passes the engine's output to the helper's log a line at a time,
// each made printable and bounded.
type logLines struct {
	out     io.Writer
	pending []byte
}

func (l *logLines) Write(p []byte) (int, error) {
	l.pending = append(l.pending, p...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			break
		}
		l.emit(l.pending[:i])
		l.pending = l.pending[i+1:]
	}
	if len(l.pending) > helperLogLineBytes {
		l.flush()
	}
	return len(p), nil
}

func (l *logLines) flush() {
	if len(l.pending) > 0 {
		l.emit(l.pending)
		l.pending = nil
	}
}

func (l *logLines) emit(line []byte) {
	fmt.Fprintln(l.out, headOf(printable(string(line), false), helperLogLineBytes))
}

// openUpdateFolder opens the update folder for the helper, and refuses any
// folder but the one the installer gave the service.
//
// The rule: the folder is a real folder, never a link, owned by ServiceUID and
// writable by no group and not the world; its parent belongs to root or to
// the service, and is writable by no group and not the world either. The
// service may rename its own folder and put another of its own in place, which
// gets it nothing it did not have. Anybody else who could write the parent
// could do the same with a folder of theirs, and a link could point root at a
// folder of root's, where the result would be written as root.
//
// The folder is looked at without following a link and must be the folder
// that was opened, so a swap between the two costs an error.
func openUpdateFolder(dir string, serviceUID int, ownerOf func(os.FileInfo) (int, bool)) (*os.Root, error) {
	const hint = `run "sudo zoomies updates helper install" again, which makes the folder as the helper expects it`
	parent := filepath.Dir(dir)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("cannot look at %s, the folder the update folder is in: %w", parent, err)
	}
	uid, ok := ownerOf(parentInfo)
	if !ok {
		return nil, fmt.Errorf("cannot tell who owns %s, the folder the update folder is in, so the helper does not use it", parent)
	}
	if uid != helperEUID() && uid != serviceUID {
		return nil, fmt.Errorf("%s, which holds the update folder, is owned by uid %d, which is neither root nor the account zoomies runs as (uid %d), and that account could have its folder replaced; %s", parent, uid, serviceUID, hint)
	}
	if parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s, which holds the update folder, is writable by its group or the world (mode %o), so somebody else could put another folder in its place; make it writable by its owner alone", parent, parentInfo.Mode().Perm())
	}
	named, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("the update folder %s does not exist; %s", dir, hint)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot look at the update folder %s: %w", dir, err)
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("the update folder %s is a %s and not a folder, and the helper does not follow one, because a link could point it at a folder of root's; %s", dir, fileKind(named.Mode()), hint)
	}
	betweenLookAndOpen(dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot open the update folder %s: %w", dir, err)
	}
	opened, err := root.Stat(".")
	switch {
	case err != nil:
		err = fmt.Errorf("cannot look at the update folder %s once open: %w", dir, err)
	case !os.SameFile(named, opened):
		err = fmt.Errorf("the update folder %s changed while it was being opened, so the helper does not use it", dir)
	default:
		if uid, ok := ownerOf(opened); !ok {
			err = fmt.Errorf("cannot tell who owns the update folder %s, so the helper does not use it", dir)
		} else if uid != serviceUID {
			err = fmt.Errorf("the update folder %s is owned by uid %d and not by uid %d, the account the helper was installed for; %s", dir, uid, serviceUID, hint)
		} else if opened.Mode().Perm()&0o022 != 0 {
			err = fmt.Errorf("the update folder %s is writable by its group or the world (mode %o), and only the account zoomies runs as may write it; make it writable by its owner alone", dir, opened.Mode().Perm())
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}
