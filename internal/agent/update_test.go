package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

// withUpdateFolder gives the agent an update folder, as a standalone agent
// whose installer recorded one has.
func withUpdateFolder(dir string) func(*Options) {
	return func(o *Options) { o.UpdateDir = func() (string, bool) { return dir, true } }
}

// readyUpdateFolder is an update folder the helper's installer has finished
// with: it holds the marker, which the installer writes last.
func readyUpdateFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeMarker(t, dir)
	return dir
}

func writeMarker(t *testing.T, dir string) {
	t.Helper()
	writeJSON(t, filepath.Join(dir, channel.MarkerFile), channel.Marker{
		V: updates.WireVersion, Version: "1.3.0", Binary: "/usr/local/libexec/zoomies-update",
		InstalledAt: time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC),
	})
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// runningBuild stands the agent on a build for one test. Release binaries report
// their version without the v, which is why the comparison is never equality.
func runningBuild(t *testing.T, v string) {
	t.Helper()
	was := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = was })
}

// beatOnce sends one heartbeat and returns what it carried.
func beatOnce(t *testing.T, a *Agent, tr *fakeTransport) (HeartbeatRequest, error) {
	t.Helper()
	err := a.heartbeat(context.Background())
	select {
	case req := <-tr.beats:
		return req, err
	default:
		t.Fatal("the agent sent no heartbeat")
		return HeartbeatRequest{}, err
	}
}

func joined(t *testing.T, tweaks ...func(*Options)) (*Agent, *fakeTransport, *testClock) {
	t.Helper()
	a, tr, _, clock := newAgent(t, 1, tweaks...)
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatal(err)
	}
	return a, tr, clock
}

func noRequestIn(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, channel.RequestFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is in the update folder (err %v); nothing should have been written", channel.RequestFile, err)
	}
}

// The controller sends an update only to an agent that says it can carry one
// out, and an older agent answers the task with a failure. So the flag follows
// the helper's marker beat by beat, and an agent built as the embedded one is,
// with no update folder, never offers it: it is updated with its controller.
func TestAnAgentAdvertisesSelfUpdateOnlyWhileItsHelperIsReady(t *testing.T) {
	dir := t.TempDir()
	a, tr, _ := joined(t, withUpdateFolder(dir))
	advertised := func() bool {
		t.Helper()
		req, err := beatOnce(t, a, tr)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(req.Features, FeatureSelfUpdate)
	}

	if advertised() {
		t.Fatal("self-update was advertised from a folder with no helper marker")
	}
	writeMarker(t, dir)
	if !advertised() {
		t.Fatal("self-update was not advertised once the helper's marker was in place")
	}
	if err := os.Remove(filepath.Join(dir, channel.MarkerFile)); err != nil {
		t.Fatal(err)
	}
	if advertised() {
		t.Fatal("self-update was still advertised after the helper's marker was removed")
	}

	embedded, etr, _ := joined(t)
	req, err := beatOnce(t, embedded, etr)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(req.Features, FeatureSelfUpdate) {
		t.Fatalf("an agent with no update folder advertised self-update: %v", req.Features)
	}

	// A folder that is a link reads its target's marker, but a request is never
	// written through one, so offering the flag there promises what every update
	// task would then refuse.
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(t.TempDir(), "update")
	if err := os.Symlink(readyUpdateFolder(t), link); err != nil {
		t.Fatal(err)
	}
	linked, ltr, _ := joined(t, withUpdateFolder(link))
	if req, err := beatOnce(t, linked, ltr); err != nil {
		t.Fatal(err)
	} else if slices.Contains(req.Features, FeatureSelfUpdate) {
		t.Fatalf("an agent whose update folder is a link advertised self-update: %v", req.Features)
	}
}

// onHelperHost stands the agent on a machine described by facts, so that what
// it says about the helper never depends on the machine the suite runs on.
func onHelperHost(facts updates.HelperHost) func(*Options) {
	return func(o *Options) { o.HelperHost = func() updates.HelperHost { return facts } }
}

// A host the helper can never be installed on would otherwise be offered the
// command that installs it, which then refuses. The agent says why instead, as
// a word the controller keys its sentence on, and says nothing once the helper
// is there, where it does not know, and where it is the controller's own.
func TestAnAgentSaysWhyItsHostCannotHaveTheHelper(t *testing.T) {
	said := func(t *testing.T, a *Agent, tr *fakeTransport) string {
		t.Helper()
		req, err := beatOnce(t, a, tr)
		if err != nil {
			t.Fatal(err)
		}
		return req.UpdateUnsupported
	}
	native := updates.HelperHost{GOOS: "linux", Systemd: true}
	container := updates.HelperHost{GOOS: "linux", InContainer: true}

	t.Run("a host systemd does not run", func(t *testing.T) {
		dir := t.TempDir()
		a, tr, _ := joined(t, withUpdateFolder(dir), onHelperHost(updates.HelperHost{GOOS: "linux"}))
		if got := said(t, a, tr); got != string(updates.HelperUnsupportedNoSystemd) {
			t.Fatalf("the beat says %q, want %q", got, updates.HelperUnsupportedNoSystemd)
		}
		// A helper installed all the same is the better witness: it is there.
		writeMarker(t, dir)
		if got := said(t, a, tr); got != "" {
			t.Fatalf("with the helper's marker in place the beat still says %q", got)
		}
	})
	t.Run("a systemd host", func(t *testing.T) {
		a, tr, _ := joined(t, onHelperHost(native))
		if got := said(t, a, tr); got != "" {
			t.Fatalf("a host the helper can be installed on says %q", got)
		}
	})
	t.Run("a container under a rootless runtime", func(t *testing.T) {
		a, tr, be, _ := newAgent(t, 1, onHelperHost(container))
		be.rootless = true
		if err := a.Join(context.Background(), "join-token"); err != nil {
			t.Fatal(err)
		}
		if got := said(t, a, tr); got != string(updates.HelperUnsupportedRootless) {
			t.Fatalf("the beat says %q, want %q", got, updates.HelperUnsupportedRootless)
		}
	})
	t.Run("a container under a runtime that runs as root", func(t *testing.T) {
		// An agent always runs runners, so its container has the shared folder.
		a, tr, _ := joined(t, onHelperHost(container))
		if got := said(t, a, tr); got != "" {
			t.Fatalf("a container the helper can serve says %q", got)
		}
	})
	t.Run("an agent told nothing about its machine", func(t *testing.T) {
		a, tr, _ := joined(t)
		if got := said(t, a, tr); got != "" {
			t.Fatalf("an agent with no facts about its machine says %q", got)
		}
	})
}

// Only a container runtime's rootlessness moves what a container writes to
// another uid. A process backend in a container runs as the image's account,
// which is never root, and that is not a rootless runtime; a host with a root
// runtime beside a rootless one may run its deployment under either, and is
// left to the installer.
func TestARootlessRuntimeIsOnlyOneWhereEveryRuntimeIsRootless(t *testing.T) {
	for _, tc := range []struct {
		name  string
		infos []backend.Info
		want  bool
	}{
		{"none", nil, false},
		{"one rootless daemon", []backend.Info{{Kind: store.BackendPodman, Available: true, Rootless: true}}, true},
		{"one root daemon", []backend.Info{{Kind: store.BackendDocker, Available: true}}, false},
		{"a root daemon beside a rootless one", []backend.Info{
			{Kind: store.BackendDocker, Available: true}, {Kind: store.BackendPodman, Available: true, Rootless: true}}, false},
		{"a rootless daemon beside one that did not answer", []backend.Info{
			{Kind: store.BackendDocker}, {Kind: store.BackendPodman, Available: true, Rootless: true}}, true},
		{"the process backend", []backend.Info{{Kind: store.BackendProcess, Available: true, Rootless: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rootlessRuntime(tc.infos); got != tc.want {
				t.Errorf("rootlessRuntime = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnUpdateTaskWritesTheRequestAndReportsItWritten(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	// The host's clock need not be in UTC, and the request must be all the same.
	localTime := func(o *Options) {
		utc := o.Clock
		o.Clock = func() time.Time { return utc().In(time.FixedZone("CEST", 2*60*60)) }
	}
	h := newHarness(t, 1, withUpdateFolder(dir), localTime)

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	res := h.nextResult()
	if res.TaskID != "task-1" || res.Kind != TaskUpdateAgent || !res.OK || res.Error != "" {
		t.Fatalf("the update task reported %+v, want OK", res)
	}
	body, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
	if err != nil {
		t.Fatalf("no request was written: %v", err)
	}
	req, err := updates.ParseRequest(body)
	if err != nil {
		t.Fatalf("the request is one the helper would refuse: %v", err)
	}
	if req.ID != "upd_abc123" || req.Tag != "v1.3.5" {
		t.Errorf("the request asks for %s as %s, want v1.3.5 as upd_abc123", req.Tag, req.ID)
	}
	if !req.RequestedAt.Equal(h.clock.Now()) || req.RequestedAt.Location() != time.UTC {
		t.Errorf("requested_at = %v, want the agent's clock in UTC (%v)", req.RequestedAt, h.clock.Now())
	}
	if req.RequestedBy == "" {
		t.Error("requested_by is empty, so the helper's log cannot say who asked")
	}
}

// A comparison of strings would take a host on 1.3.5 to v1.3.5 again, because
// a release binary reports its version without the v, and would take a host on
// 1.4.0 back to v1.3.5. Neither is an update; both are already done.
func TestAnUpdateTaskForADowngradeWritesNothing(t *testing.T) {
	for _, tc := range []struct{ running, tag string }{
		{"1.4.0", "v1.3.5"},
		{"1.3.5", "v1.3.5"},
	} {
		t.Run(tc.running+" asked for "+tc.tag, func(t *testing.T) {
			runningBuild(t, tc.running)
			dir := readyUpdateFolder(t)
			h := newHarness(t, 1, withUpdateFolder(dir))

			h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", tc.tag)}
			if res := h.nextResult(); !res.OK || res.Error != "" {
				t.Fatalf("the update task reported %+v, want OK as already done", res)
			}
			noRequestIn(t, dir)
		})
	}
}

// While the controller recovers from a fence it pauses every change an agent
// would make, and an update that restarts the agent is the largest of them.
func TestAnUpdateTaskIsRefusedWhileMutationsArePaused(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	h := newHarness(t, 1, withUpdateFolder(dir))
	h.tr.mu.Lock()
	h.tr.beatResp = &HeartbeatResponse{OK: true, MutationsPaused: true}
	h.tr.mu.Unlock()
	if err := h.agent.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	res := h.nextResult()
	if res.OK || !res.NotStarted || res.Error == "" {
		t.Fatalf("the update task reported %+v, want a refusal that is safe to send again", res)
	}
	noRequestIn(t, dir)
}

// Review Focus 1. The sentence goes to the controller and on to a host's page
// that every role reads, so it names the fix and not the folder's path.
func TestAnUpdateTaskReportsAFailureWhenTheFolderCannotBeWritten(t *testing.T) {
	runningBuild(t, "1.3.0")
	tests := []struct {
		name string
		// folder makes the update folder the agent is given under root, and
		// returns it and where a request would land if one were written.
		folder func(t *testing.T, root string) (dir, landing string)
		none   bool
	}{
		{name: "no update folder recorded", none: true},
		{name: "the folder is missing", folder: func(t *testing.T, root string) (string, string) {
			dir := filepath.Join(root, "update")
			return dir, dir
		}},
		{name: "the folder is a file", folder: func(t *testing.T, root string) (string, string) {
			dir := filepath.Join(root, "update")
			if err := os.WriteFile(dir, []byte("not a folder"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir, root
		}},
		{name: "the folder is a link", folder: func(t *testing.T, root string) (string, string) {
			if runtime.GOOS == "windows" {
				t.Skip("making a link needs a privilege Windows does not give a test")
			}
			real := filepath.Join(root, "elsewhere")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			writeMarker(t, real)
			dir := filepath.Join(root, "update")
			if err := os.Symlink(real, dir); err != nil {
				t.Fatal(err)
			}
			return dir, real
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tweak := func(o *Options) { o.UpdateDir = func() (string, bool) { return "", false } }
			landing := root
			if !tc.none {
				var dir string
				dir, landing = tc.folder(t, root)
				tweak = withUpdateFolder(dir)
			}
			h := newHarness(t, 1, tweak)

			h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
			res := h.nextResult()
			if res.OK || res.NotStarted {
				t.Fatalf("the update task reported %+v, want a failure", res)
			}
			if !strings.Contains(res.Error, "sudo zoomies updates helper install") {
				t.Errorf("the failure does not say how to fix it: %q", res.Error)
			}
			if strings.Contains(res.Error, root) {
				t.Errorf("the failure names a path on the host: %q", res.Error)
			}
			noRequestIn(t, landing)
		})
	}
}

// A request still in the folder is one the helper has not read. Replacing it
// would lose it unseen, so the agent leaves it and says what to look at.
func TestAnUpdateTaskLeavesAnEarlierRequestAlone(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	earlier := []byte(`{"v":1,"id":"upd_earlier","tag":"v1.3.4"}`)
	if err := os.WriteFile(filepath.Join(dir, channel.RequestFile), earlier, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, 1, withUpdateFolder(dir))

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	res := h.nextResult()
	if res.OK || !strings.Contains(res.Error, "zoomies updates helper status") {
		t.Fatalf("the update task reported %+v, want a failure that says how to look at the helper", res)
	}
	if strings.Contains(res.Error, dir) {
		t.Errorf("the failure names a path on the host: %q", res.Error)
	}
	got, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
	if err != nil || string(got) != string(earlier) {
		t.Fatalf("the earlier request is now %q (err %v), want it untouched", got, err)
	}
}

// The update restarts the agent, so its outcome cannot ride the task's result.
// It rides the heartbeat until one is answered, and then stops: the controller
// treats a repeat as nothing, but every beat carrying it for an hour would be
// noise on a page somebody reads.
func TestTheHeartbeatCarriesTheHelpersResultUntilOneSucceeds(t *testing.T) {
	dir := readyUpdateFolder(t)
	a, tr, clock := joined(t, withUpdateFolder(dir))
	// The helper's clock is the host's, and nothing promises it writes UTC.
	finished := clock.Now().Add(-5 * time.Minute).In(time.FixedZone("CEST", 2*60*60))
	writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: "upd_abc123", OK: false, Tag: "v1.3.5", From: "1.3.0",
		Error:     "the download failed:\x1b[31m checksum mismatch\nfor zoomies_linux_amd64\u202e",
		StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
	})

	tr.mu.Lock()
	tr.beatErr = errors.New("the controller is restarting")
	tr.mu.Unlock()
	first, err := beatOnce(t, a, tr)
	if err == nil {
		t.Fatal("the first beat was meant to fail")
	}
	if first.Update == nil || first.Update.ID != "upd_abc123" {
		t.Fatalf("the first beat carried %+v, want the helper's result", first.Update)
	}

	tr.mu.Lock()
	tr.beatErr = nil
	tr.mu.Unlock()
	second, err := beatOnce(t, a, tr)
	if err != nil {
		t.Fatal(err)
	}
	got := second.Update
	if got == nil || got.ID != "upd_abc123" || got.OK || got.Tag != "v1.3.5" || got.From != "1.3.0" {
		t.Fatalf("the second beat carried %+v, want the helper's result again", got)
	}
	if got.FinishedAt.Location() != time.UTC || !got.FinishedAt.Equal(finished) {
		t.Errorf("finished_at = %v, want %v in UTC", got.FinishedAt, finished)
	}
	if strings.ContainsFunc(got.Error, func(r rune) bool { return !unicode.IsGraphic(r) }) {
		t.Errorf("the helper's error reached the controller with characters a page must not show: %q", got.Error)
	}
	if !strings.Contains(got.Error, "checksum mismatch") {
		t.Errorf("the helper's sentence was lost: %q", got.Error)
	}

	third, err := beatOnce(t, a, tr)
	if err != nil {
		t.Fatal(err)
	}
	if third.Update != nil {
		t.Fatalf("the third beat carried %+v after the second was answered", third.Update)
	}
}

// A result outside the attempt's lifetime belongs to an attempt the controller
// has already closed by its timeout, and a new process has no way to know it was
// sent. A finish far in the future is a clock or a file that cannot be trusted.
func TestAResultOutsideTheAttemptWindowIsNotReported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{"finished 99 minutes ago", -99 * time.Minute, true},
		{"finished 101 minutes ago", -101 * time.Minute, false},
		{"dated 99 minutes ahead", 99 * time.Minute, true},
		{"dated 101 minutes ahead", 101 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := readyUpdateFolder(t)
			a, tr, clock := joined(t, withUpdateFolder(dir))
			finished := clock.Now().Add(tc.offset)
			writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
				V: updates.WireVersion, ID: "upd_abc123", OK: true, Tag: "v1.3.5", From: "1.3.0", To: "1.3.5",
				StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
			})
			req, err := beatOnce(t, a, tr)
			if err != nil {
				t.Fatal(err)
			}
			if got := req.Update != nil; got != tc.want {
				t.Fatalf("the beat carried %+v, want a report %v", req.Update, tc.want)
			}
		})
	}
}

// The process that took the task is the one the update replaces. What it wrote
// down about delivery dies with it, so the new process sends the result once.
func TestARestartedAgentSendsTheResultOnce(t *testing.T) {
	dir := readyUpdateFolder(t)
	before, btr, clock := joined(t, withUpdateFolder(dir))
	writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: "upd_abc123", OK: true, Tag: "v1.3.5", From: "1.3.0", To: "1.3.5",
		StartedAt: clock.Now().Add(-2 * time.Minute), FinishedAt: clock.Now().Add(-time.Minute),
	})
	btr.mu.Lock()
	btr.beatErr = errors.New("the controller is restarting")
	btr.mu.Unlock()
	if _, err := beatOnce(t, before, btr); err == nil {
		t.Fatal("the old process's beat was meant to fail")
	}

	after, atr, _ := joined(t, withUpdateFolder(dir))
	first, err := beatOnce(t, after, atr)
	if err != nil {
		t.Fatal(err)
	}
	if first.Update == nil || first.Update.ID != "upd_abc123" || !first.Update.OK || first.Update.To != "1.3.5" {
		t.Fatalf("the new process's first beat carried %+v, want the helper's result", first.Update)
	}
	second, err := beatOnce(t, after, atr)
	if err != nil {
		t.Fatal(err)
	}
	if second.Update != nil {
		t.Fatalf("the new process sent the result again: %+v", second.Update)
	}
}

// An update is the host's, not a runner's. A result that named a runner or a
// runner state would be counted against a pool, and could fail a runner that
// is running a job.
func TestAnUpdateTaskNeverReportsRunnerFailure(t *testing.T) {
	runningBuild(t, "1.3.0")
	for _, tc := range []struct {
		name  string
		tweak func(*Options)
	}{
		{"written", withUpdateFolder(readyUpdateFolder(t))},
		{"refused", func(o *Options) { o.UpdateDir = func() (string, bool) { return "", false } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, tc.tweak)
			h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
			res := h.nextResult()
			if res.RunnerID != "" || res.State != "" || res.Fault != "" {
				t.Fatalf("the update task reported %+v, which names a runner's failure", res)
			}
		})
	}
}

// deliveredOnce writes a result, and checks that the next beat carries it and
// the one after does not.
func deliveredOnce(t *testing.T, a *Agent, tr *fakeTransport, dir string, res updates.Result) *UpdateReport {
	t.Helper()
	writeJSON(t, filepath.Join(dir, channel.ResultFile), res)
	first, err := beatOnce(t, a, tr)
	if err != nil {
		t.Fatal(err)
	}
	if first.Update == nil {
		t.Fatalf("the result %q finished at %v was not carried", res.ID, res.FinishedAt)
	}
	second, err := beatOnce(t, a, tr)
	if err != nil {
		t.Fatal(err)
	}
	if second.Update != nil {
		t.Fatalf("the result %q was carried again after a beat that carried it was answered", res.ID)
	}
	return first.Update
}

// The helper refuses a request it cannot trust without echoing an id, and the
// controller closes the open attempt with that refusal. A second refusal is a
// second answer, and swallowing it would leave its attempt to time out with no
// reason given.
func TestEveryRefusalWithoutAnIDIsDelivered(t *testing.T) {
	dir := readyUpdateFolder(t)
	a, tr, clock := joined(t, withUpdateFolder(dir))
	for _, ago := range []time.Duration{20 * time.Minute, 5 * time.Minute} {
		got := deliveredOnce(t, a, tr, dir, updates.Result{
			V: updates.WireVersion, OK: false, Error: "the request is not the document the helper reads",
			StartedAt: clock.Now().Add(-ago), FinishedAt: clock.Now().Add(-ago),
		})
		if got.ID != "" || got.OK {
			t.Fatalf("the refusal was carried as %+v", got)
		}
	}
}

// What is sent is cleaned and cut, but what was delivered is remembered by what
// the helper wrote, so two results that look alike once cleaned are two results.
func TestResultsWhoseIDsLookAlikeAreEachDelivered(t *testing.T) {
	long := "upd_" + strings.Repeat("a", 80)
	for name, ids := range map[string][2]string{
		"differing in an unprintable character": {"upd_abc\x01", "upd_abc\x02"},
		"differing past the cut":                {long + "1", long + "2"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := readyUpdateFolder(t)
			a, tr, clock := joined(t, withUpdateFolder(dir))
			finished := clock.Now().Add(-5 * time.Minute)
			for _, id := range ids {
				deliveredOnce(t, a, tr, dir, updates.Result{
					V: updates.WireVersion, ID: id, OK: true, Tag: "v1.3.5",
					StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
				})
			}
		})
	}
}

// result.json is in a folder the service account can write, so what it says is
// untrusted text on its way to a page. Every field is cut to the length it has
// when the helper wrote it, and nothing a terminal or a browser would act on
// survives.
func TestTheHelpersResultIsCleanedAndBoundedBeforeItIsSent(t *testing.T) {
	dir := readyUpdateFolder(t)
	a, tr, clock := joined(t, withUpdateFolder(dir))
	long := strings.Repeat("x", 300)
	hostile := "1.3.0\x1b[2J\nroot:\u202eevil\x07"
	finished := clock.Now().Add(-5 * time.Minute)
	writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: long, OK: false, Tag: long, From: hostile, To: long + hostile,
		Error: strings.Repeat("é", 5000), StartedAt: finished, FinishedAt: finished,
	})
	req, err := beatOnce(t, a, tr)
	if err != nil {
		t.Fatal(err)
	}
	got := req.Update
	if got == nil {
		t.Fatal("the result was not carried")
	}
	if n := utf8.RuneCountInString(got.Error); n > maxUpdateErrorRunes+3 || n < maxUpdateErrorRunes {
		t.Errorf("the error is %d characters, want it cut to %d", n, maxUpdateErrorRunes)
	}
	for field, v := range map[string]string{"id": got.ID, "tag": got.Tag, "from": got.From, "to": got.To} {
		if n := utf8.RuneCountInString(v); n > maxUpdateFieldRunes+3 {
			t.Errorf("%s is %d characters, want at most %d", field, n, maxUpdateFieldRunes)
		}
		if strings.ContainsFunc(v, func(r rune) bool { return !unicode.IsGraphic(r) }) {
			t.Errorf("%s carries characters a page must not show: %q", field, v)
		}
	}
	if !strings.HasPrefix(got.From, "1.3.0") {
		t.Errorf("from lost the version in front of the noise: %q", got.From)
	}
}

// A build from main is stamped main-sha-..., which nothing can order against a
// release, and it is usually ahead of the newest one. Asking for the release
// could be a downgrade, so the agent says why it will not.
func TestADevelopmentBuildDoesNotAskForAnUpdate(t *testing.T) {
	runningBuild(t, "dev")
	dir := readyUpdateFolder(t)
	h := newHarness(t, 1, withUpdateFolder(dir))

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	res := h.nextResult()
	if res.OK || !strings.Contains(res.Error, "not a release") || !strings.Contains(res.Error, "zoomies upgrade") {
		t.Fatalf("the update task reported %+v, want a refusal that says the build is not a release", res)
	}
	noRequestIn(t, dir)
}

// The id is the one field of the request the task supplies unchecked by
// validateTask. A request the helper would refuse is the controller's mistake,
// and saying so beats a sentence about the folder that sends somebody to
// reinstall a helper that is fine.
func TestAnUpdateTaskWithAnIDTheHelperWouldRefuseWritesNothing(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	h := newHarness(t, 1, withUpdateFolder(dir))

	h.tr.tasks <- []Task{updateTask("task-1", "x", "v1.3.5")}
	res := h.nextResult()
	if res.OK || !strings.Contains(res.Error, "would refuse") {
		t.Fatalf("the update task reported %+v, want a refusal that blames the request", res)
	}
	noRequestIn(t, dir)
}

// requested_by goes into a log root owns, so the helper refuses one that is long
// or carries anything unprintable. The host's name is the operator's to choose
// and may be either, and that must not cost the host its update.
func TestTheRequestSaysWhoAskedInWhatTheHelperAccepts(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	name := "build\x1b[31m\u202e01\n" + strings.Repeat("é", 100)
	h := newHarness(t, 1, withUpdateFolder(dir), func(o *Options) { o.Name = name })

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("the update task reported %+v, want the request written", res)
	}
	body, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
	if err != nil {
		t.Fatalf("no request was written: %v", err)
	}
	req, err := updates.ParseRequest(body)
	if err != nil {
		t.Fatalf("the request is one the helper would refuse: %v", err)
	}
	by := req.RequestedBy
	if len(by) > maxRequestedBy || !utf8.ValidString(by) || strings.ContainsRune(by, utf8.RuneError) {
		t.Errorf("requested_by = %q (%d bytes), want at most %d bytes cut at a character", by, len(by), maxRequestedBy)
	}
	if !strings.Contains(by, "build") || !strings.Contains(by, "é") {
		t.Errorf("requested_by = %q lost the host's name", by)
	}
}

// The controller redelivers a task whose result it did not see. The request
// from the first delivery is still waiting for the helper, which is the answer
// the controller asked for, not a reason to fail the attempt.
func TestARedeliveredUpdateTaskIsAnsweredAgainWithoutASecondRequest(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	h := newHarness(t, 1, withUpdateFolder(dir))

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("the first delivery reported %+v", res)
	}
	first, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute)

	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	if res := h.nextResult(); !res.OK || res.Error != "" {
		t.Fatalf("the redelivery reported %+v, want OK again", res)
	}
	again, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
	if err != nil || string(again) != string(first) {
		t.Fatalf("the request is now %q (err %v), want the first one untouched", again, err)
	}
}

// A controller that restarts asks again for every attempt still open, and the
// agent that wrote the request may have been restarted too, so it does not
// remember writing it. The request still waiting is this attempt's, for this
// release, which is what was asked for: answered as done, and left as it is.
// A waiting request for anything else is still a reason to refuse.
func TestAnUpdateTaskFindingItsOwnRequestWaitingIsAnsweredAsWritten(t *testing.T) {
	runningBuild(t, "1.3.0")
	for _, tc := range []struct {
		name    string
		id, tag string
		ok      bool
	}{
		{"its own", "upd_abc123", "v1.3.5", true},
		{"another attempt's", "upd_other1", "v1.3.5", false},
		{"its own id for another release", "upd_abc123", "v1.3.4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := readyUpdateFolder(t)
			waiting := updates.Request{V: updates.WireVersion, ID: tc.id, Tag: tc.tag, RequestedBy: "an earlier agent", RequestedAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
			if err := channel.WriteRequest(dir, waiting); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, 1, withUpdateFolder(dir))
			h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
			res := h.nextResult()
			if res.OK != tc.ok {
				t.Errorf("the update task reported %+v, want OK %v", res, tc.ok)
			}
			after, err := os.ReadFile(filepath.Join(dir, channel.RequestFile))
			if err != nil || string(after) != string(before) {
				t.Errorf("the waiting request is now %q (err %v), want it untouched", after, err)
			}
		})
	}
}

// The helper may already have answered this attempt: an agent process before
// this one wrote the request, the helper took it and wrote its result, and the
// update failed before the agent was replaced. Writing the request again would
// ask for the same update twice; the answer is already on its way on the
// heartbeat.
func TestAnUpdateTaskTheHelperHasAlreadyAnsweredWritesNothing(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: "upd_abc123", OK: false, Tag: "v1.3.5", Error: "the download failed",
		FinishedAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
	})
	h := newHarness(t, 1, withUpdateFolder(dir))
	h.tr.tasks <- []Task{updateTask("task-1", "upd_abc123", "v1.3.5")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("the update task reported %+v, want OK", res)
	}
	noRequestIn(t, dir)

	// A result for another attempt says nothing about this one.
	h.tr.tasks <- []Task{updateTask("task-2", "upd_def456", "v1.3.5")}
	if res := h.nextResult(); !res.OK {
		t.Fatalf("the second update task reported %+v, want OK", res)
	}
	if _, err := os.Stat(filepath.Join(dir, channel.RequestFile)); err != nil {
		t.Errorf("a result for another attempt stopped the request being written: %v", err)
	}
}

// A controller that is fenced, or whose store failed, cannot record the result
// a beat carried, and says so. Taken as delivered, the result would never be
// sent again and the attempt would end only at its time-out, with the helper's
// sentence lost. A beat answered without the result held is the delivery.
func TestAResultTheControllerHeldIsSentAgainUntilItIsRecorded(t *testing.T) {
	dir := readyUpdateFolder(t)
	a, tr, clock := joined(t, withUpdateFolder(dir))
	finished := clock.Now().Add(-5 * time.Minute)
	writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: "upd_abc123", OK: false, Tag: "v1.3.5", Error: "the download failed",
		StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
	})
	tr.mu.Lock()
	tr.beatResp = &HeartbeatResponse{OK: true, UpdateHeld: true}
	tr.mu.Unlock()
	for i := range 2 {
		req, err := beatOnce(t, a, tr)
		if err != nil {
			t.Fatal(err)
		}
		if req.Update == nil || req.Update.ID != "upd_abc123" {
			t.Fatalf("beat %d after the controller held the result carried %+v, want the result again", i+1, req.Update)
		}
	}

	tr.mu.Lock()
	tr.beatResp = &HeartbeatResponse{OK: true}
	tr.mu.Unlock()
	if req, err := beatOnce(t, a, tr); err != nil || req.Update == nil {
		t.Fatalf("the beat the controller records carried %+v (%v), want the result", req.Update, err)
	}
	if req, err := beatOnce(t, a, tr); err != nil || req.Update != nil {
		t.Errorf("after the controller recorded the result a beat carried %+v (%v), want nothing", req.Update, err)
	}
}
