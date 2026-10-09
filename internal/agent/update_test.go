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
}

func TestAnUpdateTaskWritesTheRequestAndReportsItWritten(t *testing.T) {
	runningBuild(t, "1.3.0")
	dir := readyUpdateFolder(t)
	h := newHarness(t, 1, withUpdateFolder(dir))

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
		Error:     "the download failed:\x1b[31m checksum mismatch\nfor zoomies_linux_amd64‮",
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

// A result from long ago belongs to an attempt the controller closed long ago,
// most likely by its timeout, and a new process has no way to know it was sent.
func TestAResultFromMoreThanAnHourAgoIsNotReported(t *testing.T) {
	dir := readyUpdateFolder(t)
	a, tr, clock := joined(t, withUpdateFolder(dir))
	finished := clock.Now().Add(-61 * time.Minute)
	writeJSON(t, filepath.Join(dir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: "upd_abc123", OK: true, Tag: "v1.3.5", From: "1.3.0", To: "1.3.5",
		StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
	})
	req, err := beatOnce(t, a, tr)
	if err != nil {
		t.Fatal(err)
	}
	if req.Update != nil {
		t.Fatalf("the beat carried %+v, a result finished more than an hour ago", req.Update)
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
