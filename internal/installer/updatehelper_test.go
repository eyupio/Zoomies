//go:build unix

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// helperHost is one host as the helper sees it: an update folder the service
// owns, a state directory of root's, an installed version, and an engine that
// records what it was asked instead of upgrading anything.
type helperHost struct {
	opts      HelperOptions
	dir       string
	log       *bytes.Buffer
	installed string
	upgrades  []string
	// engine is what the fake engine prints and returns; by default it moves
	// the installed version to the tag and succeeds.
	engine func(tag string) (string, error)
}

func newHelperHost(t *testing.T, installed string) *helperHost {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "update")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	h := &helperHost{dir: dir, log: &bytes.Buffer{}, installed: installed}
	h.engine = func(tag string) (string, error) {
		h.installed = strings.TrimPrefix(tag, "v")
		return "Upgraded to " + tag + "\n", nil
	}
	h.opts = HelperOptions{
		Dir:        dir,
		StateDir:   filepath.Join(base, "zoomies-update"),
		BinaryPath: filepath.Join(base, "zoomies"),
		LockPath:   filepath.Join(base, "upgrade.lock"),
		ServiceUID: testServiceUID,
		Now:        func() time.Time { return t0 },
		Out:        h.log,
		ownerOf:    ownedByService,
		installedVersion: func(context.Context) (string, error) {
			return h.installed, nil
		},
		upgrade: func(_ context.Context, tag string) (string, error) {
			h.upgrades = append(h.upgrades, tag)
			return h.engine(tag)
		},
	}
	return h
}

func (h *helperHost) ask(t *testing.T, body string) {
	t.Helper()
	plant(t, filepath.Join(h.dir, channel.RequestFile), body)
}

// answer runs the helper once and returns the result it wrote.
func (h *helperHost) answer(t *testing.T) updates.Result {
	t.Helper()
	if err := RunUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RunUpdateHelper: %v\nlog:\n%s", err, h.log)
	}
	r, found, err := channel.ReadResult(h.dir)
	if err != nil || !found {
		t.Fatalf("no result the service can read: found %v, err %v\nlog:\n%s", found, err, h.log)
	}
	return r
}

func (h *helperHost) ranNothing(t *testing.T) {
	t.Helper()
	if len(h.upgrades) != 0 {
		t.Errorf("the engine was run for %v", h.upgrades)
	}
}

// fakeZoomies writes a shell script that answers `version --short` and
// `upgrade` the way the real binary does, recording the upgrade's argv and
// printing upgradeOutput.
func fakeZoomies(t *testing.T, path, upgradeOutput string) {
	t.Helper()
	fakeZoomiesScript(t, path, fakeVersion, `printf '`+upgradeOutput+`'`)
}

// fakeVersion is what the fake binary answers to `version --short`: the
// release before the tag until an upgrade has run, the tag after.
const fakeVersion = `if [ -f "$here/upgraded" ]; then echo "1.3.5 (abc1234)"; else echo "1.3.4 (abc1234)"; fi`

// fakeZoomiesScript is fakeZoomies with the shell each command runs, for an
// engine that has to behave in a particular way.
func fakeZoomiesScript(t *testing.T, path, versionCmd, upgrade string) {
	t.Helper()
	script := `#!/bin/sh
here=$(dirname "$0")
case "$1" in
version)
	` + versionCmd + ` ;;
upgrade)
	printf '%s\n' "$@" > "$here/argv"
	touch "$here/upgraded"
	` + upgrade + ` ;;
*)
	exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// shorten sets a duration variable for one test.
func shorten(t *testing.T, v *time.Duration, to time.Duration) {
	t.Helper()
	was := *v
	*v = to
	t.Cleanup(func() { *v = was })
}

// The engine re-executes itself into the new binary, so it cannot run inside
// the helper; and --yes would approve, unattended, the deployment additions
// only an operator may agree to. The argv is the whole of what root is asked
// to do, so it is pinned exactly.
func TestTheHelperRunsTheUpgradeForTheRequestedTagAsAChildProcess(t *testing.T) {
	t.Run("the engine is asked for the tag", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		h.ask(t, wellFormedRequest)
		r := h.answer(t)
		if !slices.Equal(h.upgrades, []string{"v1.3.5"}) {
			t.Errorf("the engine was asked for %v, want [v1.3.5]", h.upgrades)
		}
		if !r.OK || r.ID != "upd_k3fqz2mx7abcd" || r.Tag != "v1.3.5" || r.From != "1.3.4" || r.To != "1.3.5" || r.Error != "" {
			t.Errorf("result = %+v", r)
		}
	})
	t.Run("as a child process", func(t *testing.T) {
		h := newHelperHost(t, "")
		h.opts.installedVersion, h.opts.upgrade = nil, nil
		fakeZoomies(t, h.opts.BinaryPath, `Upgraded to v1.3.5\n`)
		h.ask(t, wellFormedRequest)
		r := h.answer(t)
		argv, err := os.ReadFile(filepath.Join(filepath.Dir(h.opts.BinaryPath), "argv"))
		if err != nil {
			t.Fatalf("the engine never ran: %v\nlog:\n%s", err, h.log)
		}
		got := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n")
		want := []string{"upgrade", "--version", "v1.3.5", "--non-interactive", "--config-dir", filepath.Dir(h.opts.LockPath)}
		if !slices.Equal(got, want) {
			t.Errorf("argv = %q, want %q", got, want)
		}
		if slices.Contains(got, "--yes") {
			t.Error("the helper approved deployment additions with --yes")
		}
		if !r.OK || r.From != "1.3.4" || r.To != "1.3.5" {
			t.Errorf("result = %+v", r)
		}
		if !strings.Contains(r.LogTail, "Upgraded to v1.3.5") || !strings.Contains(h.log.String(), "Upgraded to v1.3.5") {
			t.Errorf("the engine's output is in neither the log tail (%q) nor the helper's log:\n%s", r.LogTail, h.log)
		}
	})
}

// A downgrade is a no-op, and a tag already installed is done: running the
// engine anyway would restart the service and pull images for nothing.
// Release binaries say 1.3.5 without the v, so this is a comparison of
// releases, never of strings.
func TestTheHelperAnswersAnInstalledTagAsDoneWithoutRunningAnything(t *testing.T) {
	for _, installed := range []string{"1.3.5", "1.4.0"} {
		t.Run(installed, func(t *testing.T) {
			h := newHelperHost(t, installed)
			h.ask(t, wellFormedRequest)
			r := h.answer(t)
			h.ranNothing(t)
			if !r.OK || r.From != installed || r.To != "" || r.ID != "upd_k3fqz2mx7abcd" {
				t.Errorf("result = %+v", r)
			}
		})
	}
}

// A build from main cannot be ordered against a tag, and the helper does not
// guess: a wrong guess is a downgrade.
func TestTheHelperRefusesABuildThatIsNotFromARelease(t *testing.T) {
	h := newHelperHost(t, "main-sha-abc1234")
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	h.ranNothing(t)
	if r.OK || !strings.Contains(r.Error, "main-sha-abc1234") || !strings.Contains(r.Error, "not a release") {
		t.Errorf("result = %+v", r)
	}
}

// Somebody is upgrading this host by hand, or an upgrade was interrupted; the
// helper does not run over either, and never takes the lock itself.
func TestTheHelperRefusesWhileTheUpgradeLockExists(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	plant(t, h.opts.LockPath, "")
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	h.ranNothing(t)
	if r.OK || !strings.Contains(r.Error, h.opts.LockPath) || !strings.Contains(r.Error, "an upgrade is already running") {
		t.Errorf("the refusal should name the lock and say what it means, got %+v", r)
	}
	if _, err := os.Stat(h.opts.LockPath); err != nil {
		t.Errorf("the lock is gone: %v", err)
	}
}

// What an operator needs from a failed run is the engine's own refusal, which
// is the last thing it prints; the rest is the log tail.
func TestAFailedUpgradeIsAnsweredWithTheEnginesLastSentence(t *testing.T) {
	const sentence = "the upgrade cannot go on without this: the shared folder /var/lib/zoomies/shared; run `zoomies upgrade --yes` to add it, or `zoomies init` to set the deployment up again"
	h := newHelperHost(t, "1.3.4")
	h.engine = func(string) (string, error) {
		return "Downloading v1.3.5\nDeployment additions to review:\n  - the shared folder /var/lib/zoomies/shared\nzoomies upgrade: " + sentence + "\n\n", errors.New("exit status 1")
	}
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	if r.OK || r.Error != sentence {
		t.Errorf("error = %q, want the engine's sentence", r.Error)
	}
	if !strings.Contains(r.LogTail, "Deployment additions to review") {
		t.Errorf("the log tail lost the output: %q", r.LogTail)
	}
	if r.From != "1.3.4" || r.To != "" {
		t.Errorf("from %q to %q, want from 1.3.4 and nothing changed", r.From, r.To)
	}
}

// The service reads the result, and the controller shows it to a person, so
// whatever the engine printed arrives bounded, valid and without anything that
// could move a terminal's cursor or forge a line.
func TestTheLogTailIsBoundedAndValidUTF8(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	output := strings.Repeat("progress \x1b[2K\r line\xff\n", 20<<10/20) + "zoomies upgrade: it broke \x1b]0;pwned\x07\u202ehere\n"
	h.engine = func(string) (string, error) { return output, errors.New("exit status 1") }
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	if len(r.LogTail) > updates.MaxLogTailBytes {
		t.Errorf("the log tail is %d bytes, over %d", len(r.LogTail), updates.MaxLogTailBytes)
	}
	if !strings.Contains(r.LogTail, "it broke") {
		t.Error("the log tail lost the end of the output")
	}
	for name, s := range map[string]string{"log tail": r.LogTail, "error": r.Error} {
		if !utf8.ValidString(s) {
			t.Errorf("the %s is not valid UTF-8", name)
		}
		for _, c := range s {
			if (c != '\n' && c != '\t' && unicode.IsControl(c)) || c == '\u202e' {
				t.Errorf("the %s carries %U: %q", name, c, s)
				break
			}
		}
	}
	if strings.Contains(r.Error, "\n") {
		t.Errorf("the error is more than one line: %q", r.Error)
	}
}

// The engine's exit status is checked against the binary: a run that said it
// succeeded and left the old release installed has not updated anything.
func TestTheHelperDoesNotCallARunThatChangedNothingAnUpdate(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	h.engine = func(string) (string, error) { return "nothing to do\n", nil }
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	if r.OK || r.To != "" || !strings.Contains(r.Error, "still reports 1.3.4") {
		t.Errorf("result = %+v", r)
	}
}

// systemd stops the helper with SIGTERM at the unit's time limit, and the
// answer has to say so rather than quote whatever the engine was printing when
// it was stopped.
func TestTheHelperSaysItWasStoppedWhenItWasStopped(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.engine = func(string) (string, error) {
		cancel()
		return "Waiting for the controller to answer\n", errors.New("signal: terminated")
	}
	h.ask(t, wellFormedRequest)
	if err := RunUpdateHelper(ctx, h.opts); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	r, _, err := channel.ReadResult(h.dir)
	if err != nil || r.OK || !strings.Contains(r.Error, "stopped") || !strings.Contains(r.Error, "upgrade.lock") {
		t.Errorf("result = %+v (%v)", r, err)
	}
}

// The engine's output reaches root's journal through the helper, and none of
// it may reach a terminal that later shows that journal as anything but text.
func TestTheHelperLogsTheEnginesOutputWithoutControlCharacters(t *testing.T) {
	h := newHelperHost(t, "")
	h.opts.installedVersion, h.opts.upgrade = nil, nil
	fakeZoomies(t, h.opts.BinaryPath, `step one\033[2K\rstep two\n`)
	h.ask(t, wellFormedRequest)
	h.answer(t)
	log := h.log.String()
	// Prefixed, so a line the engine printed cannot pass for one of the
	// helper's, or begin with the <N> a journal reads as a priority.
	if !strings.Contains(log, "\nengine: step one") || !strings.Contains(log, "step two") {
		t.Fatalf("the engine's output is not in the log as the engine's:\n%s", log)
	}
	for _, c := range log {
		if c != '\n' && c != '\t' && unicode.IsControl(c) {
			t.Fatalf("the log carries %U:\n%q", c, log)
		}
	}
}

// The ids it has seen and the attempts per tag are what limit root; an
// attempt the helper had not written down before the engine ran would be
// forgotten by a helper killed during the run, and tried again.
func TestTheHelperRecordsTheAttemptBeforeTheEngineRuns(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	h.engine = func(tag string) (string, error) {
		s, err := loadHelperState(h.opts.StateDir)
		if err != nil {
			t.Fatalf("loadHelperState during the run: %v", err)
		}
		if !slices.Contains(s.Seen, "upd_k3fqz2mx7abcd") || len(s.Attempts["v1.3.5"]) != 1 {
			t.Errorf("the state during the run does not hold this attempt: %+v", s)
		}
		return "", nil
	}
	h.ask(t, wellFormedRequest)
	h.answer(t)
	if len(h.upgrades) != 1 {
		t.Fatalf("the engine ran %d times", len(h.upgrades))
	}
}

// The limits are only limits if the helper asks them: a second request within
// ten minutes is answered with the reason and nothing runs.
func TestTheHelperAnswersARequestItsLimitsRefuse(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	h.ask(t, requestBody("upd_aaaa", "v1.3.5"))
	h.engine = func(string) (string, error) { return "", errors.New("exit status 1") }
	h.answer(t)
	h.ask(t, requestBody("upd_bbbb", "v1.3.5"))
	r := h.answer(t)
	if len(h.upgrades) != 1 {
		t.Errorf("the engine ran %d times, want once", len(h.upgrades))
	}
	if r.OK || r.ID != "upd_bbbb" || !strings.Contains(r.Error, "at most one every 10 minutes") {
		t.Errorf("result = %+v", r)
	}
}

// A request the helper will not even read is answered too, so that whoever
// looks at the folder sees why, and the request is consumed so the path unit
// does not fire on it again.
func TestTheHelperAnswersARequestItWillNotRead(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	h.opts.ownerOf = func(fi os.FileInfo) (int, bool) {
		if fi.Name() == channel.RequestFile {
			return 4242, true
		}
		return testServiceUID, true
	}
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	h.ranNothing(t)
	if r.OK || r.ID != "" || !strings.Contains(r.Error, "owned by uid 4242") {
		t.Errorf("result = %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(h.dir, channel.RequestFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused request is still there: %v", err)
	}
}

// The path unit fires on a request; finding none (it was consumed by a run
// that started first) is nothing to answer.
func TestTheHelperDoesNothingWithoutARequest(t *testing.T) {
	h := newHelperHost(t, "1.3.4")
	if err := RunUpdateHelper(context.Background(), h.opts); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	h.ranNothing(t)
	if _, err := os.Lstat(filepath.Join(h.dir, channel.ResultFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a result was written with nothing asked: %v", err)
	}
}

// State that cannot be trusted fails closed, and the request is still
// answered, or the controller would wait out its whole timeout. The answer is
// read by the service, so it says where to look and quotes nothing of the
// state, whose text is root's.
func TestTheHelperAnswersWhenItsStateCannotBeUsedWithoutQuotingIt(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		loadState(t, h.opts.StateDir)
		path := filepath.Join(h.opts.StateDir, helperStateFile)
		plant(t, path, `{"seen":["root-only-secret"],"attempts":{"v1.3.5":"root-only-secret"}}`)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		h.ask(t, wellFormedRequest)
		r := h.answer(t)
		h.ranNothing(t)
		if r.OK || r.ID != "upd_k3fqz2mx7abcd" || !strings.Contains(r.Error, h.opts.StateDir) {
			t.Errorf("result = %+v", r)
		}
		if strings.Contains(r.Error, "secret") || strings.Contains(r.Error, "string") {
			t.Errorf("the answer quotes the state: %q", r.Error)
		}
	})
	t.Run("owned by another account", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		loadState(t, h.opts.StateDir)
		was := helperEUID
		helperEUID = func() int { return 4242 }
		t.Cleanup(func() { helperEUID = was })
		h.ask(t, wellFormedRequest)
		r := h.answer(t)
		h.ranNothing(t)
		if r.OK || !strings.Contains(r.Error, h.opts.StateDir) {
			t.Errorf("result = %+v", r)
		}
	})
}

// The folder is the service's, and ServiceUID comes from root's own record of
// it. A folder that is not the one the installer gave the service is not
// answered at all: writing into it would be writing where the service pointed.
func TestTheHelperRefusesAnUpdateFolderThatIsNotTheServices(t *testing.T) {
	refused := func(t *testing.T, h *helperHost, want string) {
		t.Helper()
		err := RunUpdateHelper(context.Background(), h.opts)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the folder was used, or refused for another reason (want %q): %v", want, err)
		}
		h.ranNothing(t)
		// The error is the caller's to print, and it goes to the same journal.
		if strings.Contains(h.log.String(), want) {
			t.Errorf("the refusal was logged as well as returned, so the journal has it twice:\n%s", h.log)
		}
	}
	t.Run("a link to a folder of the service's", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Mkdir(elsewhere, 0o750); err != nil {
			t.Fatal(err)
		}
		plant(t, filepath.Join(elsewhere, channel.RequestFile), wellFormedRequest)
		if err := os.Remove(h.dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, h.dir); err != nil {
			t.Fatal(err)
		}
		refused(t, h, "link")
		if _, err := os.Lstat(filepath.Join(elsewhere, channel.RequestFile)); err != nil {
			t.Errorf("the request behind the link was consumed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(elsewhere, channel.ResultFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a result was written behind the link: %v", err)
		}
	})
	t.Run("a link to a folder of root's", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		roots := t.TempDir()
		rootsInfo, err := os.Stat(roots)
		if err != nil {
			t.Fatal(err)
		}
		h.opts.ownerOf = func(fi os.FileInfo) (int, bool) {
			if os.SameFile(fi, rootsInfo) {
				return 0, true
			}
			return testServiceUID, true
		}
		if err := os.Remove(h.dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(roots, h.dir); err != nil {
			t.Fatal(err)
		}
		refused(t, h, "link")
	})
	t.Run("owned by another account", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		info, err := os.Stat(h.dir)
		if err != nil {
			t.Fatal(err)
		}
		h.opts.ownerOf = func(fi os.FileInfo) (int, bool) {
			if os.SameFile(fi, info) {
				return 4242, true
			}
			return testServiceUID, true
		}
		h.ask(t, wellFormedRequest)
		refused(t, h, "owned by uid 4242")
		if _, err := os.Lstat(filepath.Join(h.dir, channel.RequestFile)); err != nil {
			t.Errorf("the request in a folder the helper refused was consumed: %v", err)
		}
	})
	t.Run("swapped for a link after it was looked at", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Mkdir(elsewhere, 0o750); err != nil {
			t.Fatal(err)
		}
		was := betweenLookAndOpen
		betweenLookAndOpen = func(string) {
			if err := os.Rename(h.dir, h.dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, h.dir); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { betweenLookAndOpen = was })
		refused(t, h, "changed while it was being opened")
	})
	t.Run("writable by its group", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		if err := os.Chmod(h.dir, 0o770); err != nil {
			t.Fatal(err)
		}
		refused(t, h, "writable by its group or the world")
	})
	t.Run("missing", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		if err := os.Remove(h.dir); err != nil {
			t.Fatal(err)
		}
		refused(t, h, "does not exist")
	})
}

// Whoever can write the folder's parent can put another folder in its place,
// so the parent has to be root's or the service's and nobody else's.
func TestTheHelperRefusesAnUpdateFolderInAFolderOthersCanWrite(t *testing.T) {
	t.Run("world-writable", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		parent := filepath.Dir(h.dir)
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(parent, 0o700) })
		if err := RunUpdateHelper(context.Background(), h.opts); err == nil || !strings.Contains(err.Error(), "writable by its group or the world") {
			t.Errorf("a folder in a world-writable folder was used: %v", err)
		}
	})
	t.Run("owned by another account", func(t *testing.T) {
		h := newHelperHost(t, "1.3.4")
		info, err := os.Stat(filepath.Dir(h.dir))
		if err != nil {
			t.Fatal(err)
		}
		h.opts.ownerOf = func(fi os.FileInfo) (int, bool) {
			if os.SameFile(fi, info) {
				return 4242, true
			}
			return testServiceUID, true
		}
		if err := RunUpdateHelper(context.Background(), h.opts); err == nil || !strings.Contains(err.Error(), "uid 4242") {
			t.Errorf("a folder in a stranger's folder was used: %v", err)
		}
	})
}

// A grandchild of the engine can hold its output open after the engine has
// exited 0. Go reports that as ErrWaitDelay, which is not a failed upgrade;
// what the binary reports afterwards is what decides.
func TestAnEngineWhoseChildKeepsItsOutputOpenIsJudgedByTheReleaseItLeft(t *testing.T) {
	shorten(t, &helperStopGrace, 200*time.Millisecond)
	h := newHelperHost(t, "")
	h.opts.installedVersion, h.opts.upgrade = nil, nil
	fakeZoomiesScript(t, h.opts.BinaryPath, fakeVersion, `echo upgraded; sleep 5 &`)
	h.ask(t, wellFormedRequest)
	r := h.answer(t)
	if !r.OK || r.To != "1.3.5" {
		t.Errorf("result = %+v", r)
	}
}

// Stopped by systemd, the engine is asked to stop first, so that it can remove
// upgrade.lock; killed outright, it would leave the lock and every later
// request refused over it. One that will not stop is killed after the grace.
func TestTheEngineIsAskedToStopBeforeItIsKilled(t *testing.T) {
	stopWhenStarted := func(t *testing.T, h *helperHost) context.Context {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		started := filepath.Join(filepath.Dir(h.opts.BinaryPath), "started")
		go func() {
			for range 1000 {
				if _, err := os.Stat(started); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
		}()
		return ctx
	}
	t.Run("it is sent SIGTERM", func(t *testing.T) {
		shorten(t, &helperStopGrace, 10*time.Second)
		h := newHelperHost(t, "")
		h.opts.installedVersion, h.opts.upgrade = nil, nil
		lock := filepath.Join(filepath.Dir(h.opts.BinaryPath), "engine.lock")
		fakeZoomiesScript(t, h.opts.BinaryPath, fakeVersion,
			`trap 'rm -f "$here/engine.lock"; exit 1' TERM; touch "$here/engine.lock" "$here/started"; sleep 30 >/dev/null 2>&1 & wait`)
		h.ask(t, wellFormedRequest)
		if err := RunUpdateHelper(stopWhenStarted(t, h), h.opts); err != nil {
			t.Fatalf("RunUpdateHelper: %v", err)
		}
		if _, err := os.Stat(lock); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the engine never ran its handler for SIGTERM, so it was killed outright: %v", err)
		}
		r, _, _ := channel.ReadResult(h.dir)
		if r.OK || !strings.Contains(r.Error, "stopped") {
			t.Errorf("result = %+v", r)
		}
	})
	t.Run("one that ignores it is killed after the grace", func(t *testing.T) {
		shorten(t, &helperStopGrace, 300*time.Millisecond)
		h := newHelperHost(t, "")
		h.opts.installedVersion, h.opts.upgrade = nil, nil
		fakeZoomiesScript(t, h.opts.BinaryPath, fakeVersion,
			`trap '' TERM; touch "$here/started"; sleep 20 >/dev/null 2>&1`)
		h.ask(t, wellFormedRequest)
		began := time.Now()
		if err := RunUpdateHelper(stopWhenStarted(t, h), h.opts); err != nil {
			t.Fatalf("RunUpdateHelper: %v", err)
		}
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("an engine that ignored SIGTERM held the helper for %s", took)
		}
	})
}

// The version is asked of the installed binary, which answers at once or is
// broken; a broken one must not hold the helper, and an answer no release
// could give is not compared with anything.
func TestTheHelperBoundsWhatItAsksTheInstalledBinary(t *testing.T) {
	t.Run("a binary that does not answer", func(t *testing.T) {
		shorten(t, &helperVersionTimeout, 200*time.Millisecond)
		h := newHelperHost(t, "")
		h.opts.installedVersion, h.opts.upgrade = nil, nil
		fakeZoomiesScript(t, h.opts.BinaryPath, `exec sleep 30`, `true`)
		h.ask(t, wellFormedRequest)
		began := time.Now()
		r := h.answer(t)
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("a binary that never answered held the helper for %s", took)
		}
		if r.OK || !strings.Contains(r.Error, "cannot tell which release") {
			t.Errorf("result = %+v", r)
		}
	})
	t.Run("a version longer than any release's", func(t *testing.T) {
		h := newHelperHost(t, "1."+strings.Repeat("9", 130))
		h.ask(t, wellFormedRequest)
		r := h.answer(t)
		h.ranNothing(t)
		if r.OK || !strings.Contains(r.Error, "longer than any release") {
			t.Errorf("result = %+v", r)
		}
	})
}

// The lock the helper looks for and the deployment the engine upgrades have
// to be the same one, and both come from root's pointer: an engine left to
// find its own configuration directory could upgrade a deployment whose lock
// the helper never looked at.
func TestTheEngineUpgradesTheDeploymentThePointerNames(t *testing.T) {
	p := newPointerHost(t)
	p.pointer.ConfigDir = filepath.Join(p.base, "srv", "zoomies-config")
	p.pointer.UID = testServiceUID
	for _, d := range []string{filepath.Join(p.base, "srv"), p.pointer.ConfigDir, filepath.Join(p.base, "var")} {
		mustDo(t, os.Mkdir(d, 0o700))
	}
	mustDo(t, os.Mkdir(p.pointer.Dir, 0o750))
	fakeZoomies(t, p.pointer.Binary, `Upgraded to v1.3.5\n`)
	p.write(t)
	opts, err := helperOptionsFromPointer(p.stateDir, fileOwner, p.base)
	if err != nil {
		t.Fatalf("helperOptionsFromPointer: %v", err)
	}
	opts.ownerOf, opts.Now = ownedByService, func() time.Time { return t0 }
	plant(t, filepath.Join(p.pointer.Dir, channel.RequestFile), wellFormedRequest)
	if err := RunUpdateHelper(context.Background(), opts); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	argv, err := os.ReadFile(filepath.Join(filepath.Dir(p.pointer.Binary), "argv"))
	if err != nil {
		t.Fatalf("the engine never ran: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n")
	if i := slices.Index(got, "--config-dir"); i < 0 || i+1 >= len(got) || got[i+1] != p.pointer.ConfigDir {
		t.Errorf("the engine was not pointed at %s: argv %q", p.pointer.ConfigDir, got)
	}
	if opts.LockPath != filepath.Join(p.pointer.ConfigDir, "upgrade.lock") {
		t.Errorf("the lock is looked for at %s", opts.LockPath)
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// pointerHost is root's state directory holding a copy of the pointer, and the
// binary and configuration directory it names. The binary is two folders down,
// in opt/bin, so that a grandparent can be made to fail on its own; the walk
// up from it stops at base, which stands for / in these tests.
type pointerHost struct {
	base     string
	stateDir string
	pointer  channel.Pointer
}

func newPointerHost(t *testing.T) *pointerHost {
	t.Helper()
	base := t.TempDir()
	p := &pointerHost{base: base, stateDir: filepath.Join(base, "zoomies-update")}
	for _, d := range []string{p.stateDir, filepath.Join(base, "opt"), filepath.Join(base, "opt", "bin"), filepath.Join(base, "etc")} {
		mustDo(t, os.Mkdir(d, 0o700))
	}
	binary := filepath.Join(base, "opt", "bin", "zoomies")
	mustDo(t, os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755))
	p.pointer = channel.Pointer{V: 1, Dir: filepath.Join(base, "var", "update"), Binary: binary, Account: "zoomies", UID: 999, ConfigDir: filepath.Join(base, "etc")}
	return p
}

func (p *pointerHost) write(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(p.pointer)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.stateDir, channel.PointerFile)
	mustDo(t, os.WriteFile(path, body, 0o600))
	return path
}

// The helper takes nothing from the folder about itself: where the folder is,
// whose it is, which binary to run and where the lock lives all come from
// root's copy of the pointer.
func TestTheHelperTakesItsSettingsFromRootsCopyOfThePointer(t *testing.T) {
	p := newPointerHost(t)
	p.write(t)
	opts, err := helperOptionsFromPointer(p.stateDir, fileOwner, p.base)
	if err != nil {
		t.Fatalf("helperOptionsFromPointer: %v", err)
	}
	if opts.Dir != p.pointer.Dir || opts.StateDir != p.stateDir || opts.BinaryPath != p.pointer.Binary ||
		opts.LockPath != filepath.Join(p.pointer.ConfigDir, "upgrade.lock") || opts.ServiceUID != 999 {
		t.Errorf("options = %+v", opts)
	}
}

// ownedBy says the file or folder called name belongs to uid and gid, and
// leaves everything else to its real owner.
func ownedBy(name string, uid, gid int) func(os.FileInfo) (int, int, bool) {
	return func(fi os.FileInfo) (int, int, bool) {
		if fi.Name() == name {
			return uid, gid, true
		}
		return fileOwner(fi)
	}
}

// Everything in the pointer decides what root does, so each field the helper
// uses is checked by the helper, and a pointer anyone but root could have
// written is not read at all.
func TestTheHelperRefusesAPointerItCannotTrust(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, p *pointerHost, path string)
		owner  func(os.FileInfo) (int, int, bool)
		want   string
	}{
		{name: "missing", change: func(t *testing.T, p *pointerHost, path string) { mustDo(t, os.Remove(path)) }, want: "updates helper install"},
		{name: "owned by another account", owner: ownedBy(channel.PointerFile, 4242, 0), want: "owned by uid 4242"},
		{name: "writable by its group", change: func(t *testing.T, p *pointerHost, path string) { mustDo(t, os.Chmod(path, 0o620)) }, want: "writable by its group or the world"},
		{name: "a link", change: func(t *testing.T, p *pointerHost, path string) {
			mustDo(t, os.Rename(path, path+".real"))
			mustDo(t, os.Symlink(path+".real", path))
		}, want: "link"},
		{name: "uid 0", change: func(t *testing.T, p *pointerHost, path string) { p.pointer.UID = 0; p.write(t) }, want: "uid 0"},
		{name: "an account that is not a name", change: func(t *testing.T, p *pointerHost, path string) { p.pointer.Account = "zoomies\nroot"; p.write(t) }, want: "account"},
		{name: "a relative binary", change: func(t *testing.T, p *pointerHost, path string) { p.pointer.Binary = "zoomies"; p.write(t) }, want: "absolute"},
		{name: "a binary that is a link", change: func(t *testing.T, p *pointerHost, path string) {
			mustDo(t, os.Rename(p.pointer.Binary, p.pointer.Binary+"-real"))
			mustDo(t, os.Symlink(p.pointer.Binary+"-real", p.pointer.Binary))
		}, want: "link"},
		{name: "a binary root does not own", owner: ownedBy("zoomies", 4242, 0), want: "owned by uid 4242"},
		{name: "a binary its group can write", change: func(t *testing.T, p *pointerHost, path string) { mustDo(t, os.Chmod(p.pointer.Binary, 0o775)) }, want: "writable by its group or the world"},
		{name: "a binary in a folder root does not own", owner: ownedBy("bin", 4242, 0), want: "owned by uid 4242"},
		{name: "a binary under a folder the service owns", owner: ownedBy("opt", 999, 999), want: "opt is owned by uid 999"},
		{name: "a binary under a folder staff can write", change: func(t *testing.T, p *pointerHost, path string) {
			mustDo(t, os.Chmod(filepath.Join(p.base, "opt"), 0o775))
		}, owner: ownedBy("opt", os.Geteuid(), 50), want: "opt is writable by its group (gid 50)"},
		{name: "a binary under a link", change: func(t *testing.T, p *pointerHost, path string) {
			opt := filepath.Join(p.base, "opt")
			mustDo(t, os.Rename(opt, opt+"-real"))
			mustDo(t, os.Symlink(opt+"-real", opt))
		}, want: "opt is a link"},
		{name: "a binary under a folder anyone can write", change: func(t *testing.T, p *pointerHost, path string) {
			mustDo(t, os.Chmod(filepath.Join(p.base, "opt"), 0o777|os.ModeSticky))
		}, want: "opt is writable by the world"},
		{name: "a binary that cannot run", change: func(t *testing.T, p *pointerHost, path string) { mustDo(t, os.Chmod(p.pointer.Binary, 0o644)) }, want: "not executable"},
		{name: "a relative configuration directory", change: func(t *testing.T, p *pointerHost, path string) { p.pointer.ConfigDir = "etc"; p.write(t) }, want: "absolute"},
		{name: "a configuration directory that is not there", change: func(t *testing.T, p *pointerHost, path string) {
			p.pointer.ConfigDir = filepath.Join(p.pointer.ConfigDir, "missing")
			p.write(t)
		}, want: "configuration directory"},
		{name: "a configuration directory that is a file", change: func(t *testing.T, p *pointerHost, path string) {
			p.pointer.ConfigDir = p.pointer.Binary
			p.write(t)
		}, want: "is a file and not a folder"},
		{name: "a relative update folder", change: func(t *testing.T, p *pointerHost, path string) { p.pointer.Dir = "update"; p.write(t) }, want: "absolute"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPointerHost(t)
			path := p.write(t)
			if c.change != nil {
				c.change(t, p, path)
			}
			owner := c.owner
			if owner == nil {
				owner = fileOwner
			}
			_, err := helperOptionsFromPointer(p.stateDir, owner, p.base)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want a refusal saying %q, got: %v", c.want, err)
			}
			if err != nil && strings.Contains(err.Error(), "<nil>") {
				t.Errorf("the refusal prints a nil error: %v", err)
			}
		})
	}
}

// A group-write bit on a folder above the binary is tolerated only for the
// root group, which on a host is root's anyway; staff on an older Debian is
// not, and the refusal says so at install rather than as a timeout later.
func TestTheHelperAcceptsAFolderAboveTheBinaryThatOnlyTheRootGroupCanWrite(t *testing.T) {
	p := newPointerHost(t)
	p.write(t)
	mustDo(t, os.Chmod(filepath.Join(p.base, "opt"), 0o775))
	if _, err := helperOptionsFromPointer(p.stateDir, ownedBy("opt", os.Geteuid(), 0), p.base); err != nil {
		t.Errorf("a folder only the root group can write was refused: %v", err)
	}
}

// A host that never had the helper has no state directory, and the unit that
// runs this is only there if somebody installed it. Running it anyway (by hand,
// or from a unit left behind) must say the helper is not installed and leave the
// filesystem as it was, not make root's folder for a helper that is not there.
func TestReadingRootsPointerNeverMakesTheStateDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "zoomies-update")
	_, err := helperOptionsFromPointer(missing, fileOwner, "/")
	if err == nil || !strings.Contains(err.Error(), "not installed") || !strings.Contains(err.Error(), "helper install") {
		t.Fatalf("want a refusal saying the helper is not installed, got: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reading the pointer made the state directory (%v)", err)
	}
}
