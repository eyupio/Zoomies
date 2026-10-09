package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// alice is the person pressing the button in these tests.
var alice = UpdateActor{ID: "usr_alice", Name: "alice"}

// installHelper makes the update folder as the installer leaves it, marker and
// all, at the place the controller was told to look.
func (h *harness) installHelper() string {
	h.t.Helper()
	if err := os.MkdirAll(h.updateDir, 0o750); err != nil {
		h.t.Fatalf("making the update folder: %v", err)
	}
	marker, err := json.Marshal(channel.Marker{
		V: updates.WireVersion, Version: "1.3.4", Binary: "/usr/local/bin/zoomies", InstalledAt: time.Now().UTC(),
	})
	if err != nil {
		h.t.Fatalf("encoding the marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.updateDir, channel.MarkerFile), marker, 0o644); err != nil {
		h.t.Fatalf("writing the marker: %v", err)
	}
	return h.updateDir
}

// readyToUpdate is a controller on 1.3.4, in manual mode, that has read a list
// offering v1.3.5 and has a helper: everything a request needs.
func (h *harness) readyToUpdate() {
	h.t.Helper()
	withVersion(h.t, "1.3.4")
	h.inMode("manual")
	h.readTheList(releaseEntry("v1.3.5", whenAgo(6*time.Hour), completeAssets(h.t)...))
	h.installHelper()
}

// request presses the button and fails the test if it is refused.
func (h *harness) request() store.UpdateAttempt {
	h.t.Helper()
	if _, err := h.c.RequestControllerUpdate(h.ctx, alice, ""); err != nil {
		h.t.Fatalf("RequestControllerUpdate: %v", err)
	}
	open := h.openAttempts()
	if len(open) != 1 {
		h.t.Fatalf("%d attempts open after a request, want 1", len(open))
	}
	return open[0]
}

func (h *harness) openAttempts() []store.UpdateAttempt {
	h.t.Helper()
	open, err := h.st.OpenUpdateAttempts(h.ctx)
	if err != nil {
		h.t.Fatalf("OpenUpdateAttempts: %v", err)
	}
	return open
}

// attempt re-reads one attempt, open or ended.
func (h *harness) attempt(id string) store.UpdateAttempt {
	h.t.Helper()
	all, err := h.st.ListUpdateAttempts(h.ctx, "", "", 500)
	if err != nil {
		h.t.Fatalf("ListUpdateAttempts: %v", err)
	}
	for _, a := range all {
		if a.ID == id {
			return a
		}
	}
	h.t.Fatalf("no attempt %s", id)
	return store.UpdateAttempt{}
}

// takeRequest is the helper's path unit firing: the request is read and removed.
func (h *harness) takeRequest() updates.Request {
	h.t.Helper()
	path := filepath.Join(h.updateDir, channel.RequestFile)
	body, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("reading the request: %v", err)
	}
	req, err := updates.ParseRequest(body)
	if err != nil {
		h.t.Fatalf("the request is one the helper would refuse: %v\n%s", err, body)
	}
	if err := os.Remove(path); err != nil {
		h.t.Fatalf("removing the request: %v", err)
	}
	return req
}

// helperAnswers writes result.json as the helper would.
func (h *harness) helperAnswers(r updates.Result) {
	h.t.Helper()
	r.V = updates.WireVersion
	body, err := json.Marshal(r)
	if err != nil {
		h.t.Fatalf("encoding the result: %v", err)
	}
	h.writeResult(body)
}

func (h *harness) writeResult(body []byte) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.updateDir, channel.ResultFile), body, 0o644); err != nil {
		h.t.Fatalf("writing the result: %v", err)
	}
}

// pass runs one pass of the update loop on c.
func (h *harness) pass(c *Controller) {
	h.t.Helper()
	if err := c.ReconcileUpdates(h.ctx); err != nil {
		h.t.Fatalf("ReconcileUpdates: %v", err)
	}
}

// folder lists the update folder, so a test can say nothing else was written.
func (h *harness) folder() []string {
	h.t.Helper()
	entries, err := os.ReadDir(h.updateDir)
	if err != nil {
		h.t.Fatalf("reading the update folder: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRequestingAControllerUpdateRecordsTheAttemptAndWritesTheRequest(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	sub := h.listen(events.KindUpdates)

	view, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
	if err != nil {
		t.Fatalf("RequestControllerUpdate: %v", err)
	}

	open := h.openAttempts()
	if len(open) != 1 {
		t.Fatalf("%d attempts open, want 1", len(open))
	}
	a := open[0]
	if a.Scope != store.UpdateScopeController || a.HostID != "" || a.FromVersion != "1.3.4" || a.ToVersion != "v1.3.5" ||
		a.Trigger != store.UpdateTriggerManual || a.RequestedBy != "alice" {
		t.Errorf("attempt = %+v, want the controller from 1.3.4 to v1.3.5, asked for by alice by hand", a)
	}

	req := h.takeRequest()
	if req.ID != a.ID || req.Tag != "v1.3.5" || req.RequestedBy != "alice" || !req.RequestedAt.Equal(a.RequestedAt) {
		t.Errorf("request = %+v, want the attempt's id, tag, who and when (%+v)", req, a)
	}

	if view.Controller == nil || view.Controller.ID != a.ID || view.Controller.State != store.UpdateRequested || view.Controller.To != "v1.3.5" {
		t.Errorf("the status returned names %+v, want the open attempt", view.Controller)
	}
	frame := nextOfKind(t, sub, events.KindUpdates)
	if got, _ := frame["controller"].(map[string]any); got["id"] != a.ID || got["state"] != store.UpdateRequested {
		t.Errorf("the frame names %v, want the open attempt %s", frame["controller"], a.ID)
	}
}

// assertRefusedWithNothingWritten checks a refusal: the sentinel the API maps
// to a code, no attempt recorded, and no request in the folder.
func assertRefusedWithNothingWritten(t *testing.T, h *harness, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	all, lerr := h.st.ListUpdateAttempts(h.ctx, "", "", 10)
	if lerr != nil {
		t.Fatalf("ListUpdateAttempts: %v", lerr)
	}
	if len(all) != 0 {
		t.Errorf("a refused request recorded %d attempts: %+v", len(all), all)
	}
	if _, serr := os.Lstat(filepath.Join(h.updateDir, channel.RequestFile)); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("a refused request left a request in the folder (%v)", serr)
	}
}

func TestAControllerUpdateIsRefusedWhenTheModeIsOff(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	h.inMode("off")
	_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
	assertRefusedWithNothingWritten(t, h, err, ErrUpdateModeOff)
}

func TestAControllerUpdateIsRefusedWithoutAHelper(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness)
	}{
		{"no update folder", func(h *harness) {
			if err := os.RemoveAll(h.updateDir); err != nil {
				t.Fatal(err)
			}
		}},
		{"a folder without the marker", func(h *harness) {
			if err := os.Remove(filepath.Join(h.updateDir, channel.MarkerFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"a marker that is not one", func(h *harness) {
			if err := os.WriteFile(filepath.Join(h.updateDir, channel.MarkerFile), []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			tc.setup(h)
			_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
			assertRefusedWithNothingWritten(t, h, err, ErrUpdateHelperMissing)
			if !strings.Contains(err.Error(), helperInstallCommand) {
				t.Errorf("the refusal does not say how to install the helper: %v", err)
			}
		})
	}
}

// The suite runs on Windows and macOS runners too, where a controller would
// rightly say it cannot have a helper; every other test here is about one that
// can, so the platform is pinned unless a test names another.
func init() { helperPlatform = "linux" }

// withHelperPlatform makes the controller believe it runs on goos, so the
// answer for a host that cannot have a helper is tested wherever the suite runs.
func withHelperPlatform(t *testing.T, goos string) {
	t.Helper()
	prev := helperPlatform
	helperPlatform = goos
	t.Cleanup(func() { helperPlatform = prev })
}

// The helper is a pair of systemd units, so on any other platform the command
// that installs it refuses, and a status or a refusal that told the person to
// run it would send them to a dead end. They are told what does work.
func TestAHostThatCannotHaveTheHelperIsNotToldToInstallIt(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			withHelperPlatform(t, goos)
			h := newHarness(t)
			h.readyToUpdate()
			if err := os.Remove(filepath.Join(h.updateDir, channel.MarkerFile)); err != nil {
				t.Fatal(err)
			}

			helper := h.status().Helper
			if helper.State != HelperMissing || helper.InstallCommand != "" {
				t.Errorf("the status says %+v, want missing with no command to run", helper)
			}
			if !strings.Contains(helper.Reason, "systemd") || !strings.Contains(helper.Reason, "zoomies upgrade") {
				t.Errorf("the reason = %q, want it to say why and to name zoomies upgrade", helper.Reason)
			}

			_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
			assertRefusedWithNothingWritten(t, h, err, ErrUpdateHelperMissing)
			if strings.Contains(err.Error(), helperInstallCommand) || !strings.Contains(err.Error(), "zoomies upgrade") {
				t.Errorf("the refusal = %v, want it to leave out the install command and name zoomies upgrade", err)
			}
		})
	}
}

func TestAControllerUpdateIsRefusedOnABuildThatIsNotARelease(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	withVersion(t, "main-sha-abc1234")
	_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
	assertRefusedWithNothingWritten(t, h, err, ErrUpdateNotARelease)
}

func TestAControllerUpdateIsRefusedWhenNothingIsNewer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running string
		tag     string
	}{
		{"already on the newest", "1.3.5", ""},
		{"ahead of the newest", "1.3.6", ""},
		{"a tag that is older", "1.3.4", "v1.3.3"},
		{"a tag the list does not hold", "1.3.4", "v1.3.9"},
		{"a tag that is not one", "1.3.4", "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.4")
			h.inMode("manual")
			h.readTheList(
				releaseEntry("v1.3.5", whenAgo(6*time.Hour), completeAssets(t)...),
				releaseEntry("v1.3.3", whenAgo(60*time.Hour), completeAssets(t)...),
			)
			h.installHelper()
			withVersion(t, tc.running)
			_, err := h.c.RequestControllerUpdate(h.ctx, alice, tc.tag)
			assertRefusedWithNothingWritten(t, h, err, ErrUpdateNothingNewer)
		})
	}
}

// Before the list has been read there is nothing to compare with, and nothing a
// request could name that the controller knows to be complete.
func TestAControllerUpdateIsRefusedBeforeTheListHasBeenRead(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.4")
	h.inMode("manual")
	h.installHelper()
	_, err := h.c.RequestControllerUpdate(h.ctx, alice, "v1.3.5")
	assertRefusedWithNothingWritten(t, h, err, ErrUpdateNothingNewer)
}

// A tag that is named is taken only when the list holds it, complete, and it is
// newer: the helper is never sent to a release this controller has not seen.
func TestAControllerUpdateTakesATagTheListOffers(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.4")
	h.inMode("manual")
	h.readTheList(
		releaseEntry("v1.3.6", whenAgo(2*time.Hour), completeAssets(t)...),
		releaseEntry("v1.3.5", whenAgo(6*time.Hour), completeAssets(t)...),
	)
	h.installHelper()
	if _, err := h.c.RequestControllerUpdate(h.ctx, alice, "v1.3.5"); err != nil {
		t.Fatalf("RequestControllerUpdate: %v", err)
	}
	if req := h.takeRequest(); req.Tag != "v1.3.5" {
		t.Errorf("the request names %s, want the tag asked for", req.Tag)
	}
}

func TestAControllerUpdateIsRefusedWhileFenced(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	h.fence("restored from a backup")
	_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
	assertRefusedWithNothingWritten(t, h, err, ErrUpdateFenced)
	if !strings.Contains(err.Error(), "fenced") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// Two people pressing the button, or one pressing it twice, make one attempt and
// one request: the store's rule is the lock, and the second is told so.
func TestASecondRequestWhileOneIsOpenWritesNoSecondFile(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	first := h.request()

	_, err := h.c.RequestControllerUpdate(h.ctx, UpdateActor{ID: "usr_bob", Name: "bob"}, "")
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("the second request: err = %v, want ErrUpdateInProgress", err)
	}
	if got, want := h.folder(), []string{channel.MarkerFile, channel.RequestFile}; !slices.Equal(got, want) {
		t.Errorf("the folder holds %v, want %v", got, want)
	}
	if req := h.takeRequest(); req.ID != first.ID || req.RequestedBy != "alice" {
		t.Errorf("the request in the folder is %+v, want the first, %s by alice", req, first.ID)
	}

	// And once the helper has taken the first, the folder is empty and a press
	// still writes nothing while the attempt is open.
	if _, err := h.c.RequestControllerUpdate(h.ctx, UpdateActor{ID: "usr_bob", Name: "bob"}, ""); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("a request after the helper took the first: err = %v, want ErrUpdateInProgress", err)
	}
	if got := h.folder(); !slices.Equal(got, []string{channel.MarkerFile}) {
		t.Errorf("the folder holds %v, want the marker alone", got)
	}
	if open := h.openAttempts(); len(open) != 1 || open[0].ID != first.ID {
		t.Errorf("open attempts = %+v, want only the first", open)
	}
}

// A folder that cannot take a request refuses the press with the folder named,
// and leaves nothing in flight. Some faults are found before an attempt is
// recorded (the folder replaced by a file hides the helper's marker too); the
// rest only when the request is written, after the attempt exists, and that
// attempt has to be closed again before the refusal returns.
func TestAnUnwritableFolderRefusesAndLeavesNoAttempt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// recorded says whether the fault is found after the attempt was made, so
		// that a closed attempt is what is left.
		recorded bool
		spoil    func(h *harness)
	}{
		{"the folder replaced by a file", false, func(h *harness) {
			if err := os.RemoveAll(h.updateDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.updateDir, []byte("not a folder"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a folder where the request goes", true, func(h *harness) {
			if err := os.Mkdir(filepath.Join(h.updateDir, channel.RequestFile), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			tc.spoil(h)

			_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
			if !errors.Is(err, ErrUpdateHelperMissing) {
				t.Fatalf("err = %v, want ErrUpdateHelperMissing", err)
			}
			if !strings.Contains(err.Error(), h.updateDir) {
				t.Errorf("the refusal does not name the folder %s: %v", h.updateDir, err)
			}
			if open := h.openAttempts(); len(open) != 0 {
				t.Errorf("open attempts = %+v, want none", open)
			}
			all, _ := h.st.ListUpdateAttempts(h.ctx, "", "", 10)
			switch {
			case tc.recorded && (len(all) != 1 || all[0].State != store.UpdateFailed):
				t.Errorf("attempts = %+v, want the one recorded, closed as failed", all)
			case !tc.recorded && len(all) != 0:
				t.Errorf("attempts = %+v, want none recorded", all)
			}
		})
	}
}

// A request the helper has not taken is still in the folder, so the write is
// refused after the attempt was recorded. The attempt it opened is closed before
// the refusal returns, or every press for the next 90 minutes would be told an
// update is in flight when nothing is.
func TestARequestThatCannotBeWrittenClosesTheAttemptItOpened(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	leftover := []byte(`{"left":"by somebody"}`)
	if err := os.WriteFile(filepath.Join(h.updateDir, channel.RequestFile), leftover, 0o640); err != nil {
		t.Fatal(err)
	}

	_, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
	if !errors.Is(err, ErrUpdateHelperMissing) || !strings.Contains(err.Error(), h.updateDir) {
		t.Fatalf("err = %v, want ErrUpdateHelperMissing naming %s", err, h.updateDir)
	}
	if open := h.openAttempts(); len(open) != 0 {
		t.Fatalf("open attempts = %+v, want none", open)
	}
	all, _ := h.st.ListUpdateAttempts(h.ctx, "", "", 10)
	if len(all) != 1 || all[0].State != store.UpdateFailed || !strings.Contains(all[0].Error, "could not be handed to the update helper") {
		t.Errorf("attempts = %+v, want one failed, saying the request was not handed over", all)
	}
	if body, _ := os.ReadFile(filepath.Join(h.updateDir, channel.RequestFile)); !bytes.Equal(body, leftover) {
		t.Errorf("the request in the folder was replaced: %s", body)
	}
	if got, ok := gatherValue(t, h.c, "zoomies_update_attempts_total", map[string]string{"kind": "controller", "result": "failed"}); !ok || got != 1 {
		t.Errorf("failed controller attempts counted = %v (%v), want 1", got, ok)
	}
}

// Finding an earlier request still waiting is the ordinary reason a write is
// refused, and the earlier request is not this attempt's to take back. The
// refusal already says so to the person; a warning to the log as well, about a
// request that is not ours, would read as something gone wrong with the folder.
func TestARefusalBecauseARequestIsPendingIsNotLoggedAsAFolderProblem(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	if err := os.WriteFile(filepath.Join(h.updateDir, channel.RequestFile), []byte(`{"left":"by somebody"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	h.c.log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := h.c.RequestControllerUpdate(h.ctx, alice, ""); !errors.Is(err, ErrUpdateHelperMissing) {
		t.Fatalf("err = %v, want ErrUpdateHelperMissing", err)
	}
	if strings.Contains(logged.String(), "request.json that is not this attempt's") {
		t.Errorf("an ordinary pending request was logged as a problem:\n%s", logged.String())
	}
}

// The helper logs requested_by as root, and refuses a name it could not log
// safely. A name from single sign-on can be long, and can carry characters that
// would reorder or break the line; refused, it would fail the button for that
// person every time, so it is cut to what the helper accepts.
func TestTheRequestNamesWhoAskedWithinWhatTheHelperAccepts(t *testing.T) {
	long := strings.Repeat("é", 100) + "@sso.example.com"
	for _, tc := range []struct {
		name string
		by   UpdateActor
		want string
	}{
		{"a plain name", UpdateActor{ID: "usr_1", Name: "alice"}, "alice"},
		{"a long name is cut at a whole character", UpdateActor{ID: "usr_1", Name: long}, strings.Repeat("é", 64)},
		{"control characters and an override are dropped", UpdateActor{ID: "usr_1", Name: "mal\x1b[31mlory\u202e\n "}, "mal[31mlory"},
		{"a name with nothing printable gives way to the id", UpdateActor{ID: "usr_1", Name: "\u202e\x00"}, "usr_1"},
		{"nobody at all", UpdateActor{}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestedBy(tc.by)
			if got != tc.want {
				t.Errorf("requestedBy(%q) = %q, want %q", tc.by.Name, got, tc.want)
			}
			body, err := json.Marshal(updates.Request{
				V: updates.WireVersion, ID: "upd_abc", Tag: "v1.3.5", RequestedBy: got, RequestedAt: time.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := updates.ParseRequest(body); err != nil {
				t.Errorf("the helper would refuse %q: %v", got, err)
			}
		})
	}

	// End to end: the request written for such a person is one the helper reads,
	// and the attempt records the same name.
	h := newHarness(t)
	h.readyToUpdate()
	if _, err := h.c.RequestControllerUpdate(h.ctx, UpdateActor{ID: "usr_2", Name: long + "\u202e"}, ""); err != nil {
		t.Fatalf("RequestControllerUpdate: %v", err)
	}
	req := h.takeRequest()
	if open := h.openAttempts(); len(open) != 1 || open[0].RequestedBy != req.RequestedBy || len(req.RequestedBy) > 128 {
		t.Errorf("request names %q (%d bytes), attempt %+v", req.RequestedBy, len(req.RequestedBy), open)
	}
}

// A result.json the controller did not ask for is ignored: one from before a
// restart, one for an attempt already closed, one for nobody, or a file that is
// not a result. None of them may close the open attempt or touch a closed one.
func TestAStaleOrUnknownResultIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(h *harness, closed, open store.UpdateAttempt)
	}{
		{"the id of a closed attempt", func(h *harness, closed, _ store.UpdateAttempt) {
			h.helperAnswers(updates.Result{ID: closed.ID, OK: false, Error: "the old answer", FinishedAt: time.Now().Add(time.Minute)})
		}},
		{"an unknown id", func(h *harness, _, _ store.UpdateAttempt) {
			h.helperAnswers(updates.Result{ID: "upd_somebodyelse", OK: false, Error: "not yours", FinishedAt: time.Now().Add(time.Minute)})
		}},
		{"malformed JSON", func(h *harness, _, _ store.UpdateAttempt) {
			h.writeResult([]byte(`{"v":1,"id":`))
		}},
		{"an unknown field", func(h *harness, _, open store.UpdateAttempt) {
			h.writeResult([]byte(`{"v":1,"id":"` + open.ID + `","ok":false,"error":"x","extra":true}`))
		}},
		{"another version of the document", func(h *harness, _, open store.UpdateAttempt) {
			h.writeResult([]byte(`{"v":2,"id":"` + open.ID + `","ok":false,"error":"x"}`))
		}},
		{"an oversized file", func(h *harness, _, open store.UpdateAttempt) {
			// A document the reader would otherwise accept, refused for its size
			// alone.
			h.helperAnswers(updates.Result{ID: open.ID, OK: false, Error: strings.Repeat("a", 1<<20), FinishedAt: time.Now()})
		}},
		{"a folder where the result goes", func(h *harness, _, _ store.UpdateAttempt) {
			if err := os.Mkdir(filepath.Join(h.updateDir, channel.ResultFile), 0o750); err != nil {
				h.t.Fatal(err)
			}
		}},
		{"a refusal without an id from before the attempt", func(h *harness, _, open store.UpdateAttempt) {
			h.helperAnswers(updates.Result{OK: false, Error: "an old refusal", FinishedAt: open.RequestedAt.Add(-time.Second)})
		}},
		{"a success without an id", func(h *harness, _, open store.UpdateAttempt) {
			h.helperAnswers(updates.Result{OK: true, FinishedAt: open.RequestedAt.Add(time.Second)})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			first := h.request()
			h.takeRequest()
			if ok, err := h.st.FinishUpdateAttempt(h.ctx, first.ID, store.UpdateCancelled, "cancelled by the test"); err != nil || !ok {
				t.Fatalf("closing the first attempt: %v, %v", ok, err)
			}
			second := h.request()
			h.takeRequest()

			tc.write(h, h.attempt(first.ID), second)
			h.pass(h.c)

			if got := h.attempt(second.ID); got.State != store.UpdateRequested || got.Error != "" {
				t.Errorf("the open attempt is now %s (%q), want it still requested", got.State, got.Error)
			}
			if got := h.attempt(first.ID); got.State != store.UpdateCancelled || got.Error != "cancelled by the test" {
				t.Errorf("the closed attempt is now %s (%q), want it as it was", got.State, got.Error)
			}
		})
	}
}

func TestAFailedResultClosesTheOpenAttemptWithTheHelpersSentence(t *testing.T) {
	const sentence = "the upgrade cannot go on without this: the shared folder; run zoomies upgrade --yes"
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()
	sub := h.listen(events.KindUpdates)

	h.helperAnswers(updates.Result{ID: a.ID, OK: false, Tag: "v1.3.5", From: "1.3.4", Error: sentence,
		StartedAt: time.Now(), FinishedAt: time.Now()})
	h.pass(h.c)

	got := h.attempt(a.ID)
	if got.State != store.UpdateFailed || got.Error != sentence || got.FinishedAt == nil {
		t.Errorf("attempt = %s %q finished %v, want failed with the helper's sentence", got.State, got.Error, got.FinishedAt)
	}
	frame := nextOfKind(t, sub, events.KindUpdates)
	if c, _ := frame["controller"].(map[string]any); c["state"] != store.UpdateFailed || c["error"] != sentence {
		t.Errorf("the frame says %v, want the failure and its sentence", frame["controller"])
	}
	if n, ok := gatherValue(t, h.c, "zoomies_update_attempts_total", map[string]string{"kind": "controller", "result": "failed"}); !ok || n != 1 {
		t.Errorf("failed controller attempts counted = %v (%v), want 1", n, ok)
	}

	// A second pass over the same file changes nothing and counts nothing.
	h.pass(h.c)
	if n, _ := gatherValue(t, h.c, "zoomies_update_attempts_total", map[string]string{"kind": "controller", "result": "failed"}); n != 1 {
		t.Errorf("after a second pass, failed attempts counted = %v, want still 1", n)
	}
}

// A request the helper cannot trust is refused without an id, because the id in
// it is not believed. Written after the open attempt was asked for, it can only
// be that attempt's answer, and closing it with the helper's reason beats a
// silent 90-minute wait.
func TestARefusalWithoutAnIdAfterTheRequestClosesTheOpenAttempt(t *testing.T) {
	const sentence = "the request is owned by uid 1000 and the helper serves uid 65532 only"
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()

	h.helperAnswers(updates.Result{OK: false, Error: sentence, FinishedAt: a.RequestedAt.Add(time.Second)})
	h.pass(h.c)

	if got := h.attempt(a.ID); got.State != store.UpdateFailed || got.Error != sentence {
		t.Errorf("attempt = %s %q, want failed with the helper's sentence", got.State, got.Error)
	}
}

// The helper can answer done without having run anything, when the binary on
// disk is already the release. If this process is still the old build, the
// release is not the one running, whatever the helper says.
func TestAnOKResultWhileTheOldBuildStillRunsIsNotASuccess(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()

	h.helperAnswers(updates.Result{ID: a.ID, OK: true, Tag: "v1.3.5", From: "1.3.5", To: "1.3.5", FinishedAt: time.Now()})
	h.pass(h.c)

	got := h.attempt(a.ID)
	if got.State != store.UpdateFailed || !strings.Contains(got.Error, "still runs 1.3.4") {
		t.Errorf("attempt = %s %q, want failed, saying the old build still runs", got.State, got.Error)
	}
}

// The new process is the proof. It reads 1.3.5 where the tag says v1.3.5, so it
// is CompareBuilds that says they are one release; a later build has arrived too.
func TestTheNewProcessClosesTheAttemptAsSucceededWhenItRunsTheTarget(t *testing.T) {
	for _, running := range []string{"1.3.5", "v1.3.5", "1.3.6"} {
		t.Run(running, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			a := h.request()
			h.takeRequest()
			h.helperAnswers(updates.Result{ID: a.ID, OK: true, Tag: "v1.3.5", From: "1.3.4", To: "1.3.5", FinishedAt: time.Now()})

			restarted := h.restart()
			withVersion(t, running)
			h.pass(restarted)

			if got := h.attempt(a.ID); got.State != store.UpdateSucceeded || got.Error != "" {
				t.Errorf("attempt = %s %q, want succeeded", got.State, got.Error)
			}
			if n, ok := gatherValue(t, restarted, "zoomies_update_attempts_total", map[string]string{"kind": "controller", "result": "succeeded"}); !ok || n != 1 {
				t.Errorf("succeeded controller attempts counted = %v (%v), want 1", n, ok)
			}
		})
	}
}

// A build can arrive while the request is still waiting: somebody ran
// zoomies upgrade by hand. The attempt then ends as a success, and the request
// nobody will read must go with it, or the next press is refused as an earlier
// request still waiting.
func TestAnAttemptThatSucceedsTakesBackItsUnreadRequest(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()

	restarted := h.restart()
	withVersion(t, "1.3.5")
	h.pass(restarted)

	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
		t.Fatalf("attempt = %s %q, want succeeded", got.State, got.Error)
	}
	if got := h.folder(); !slices.Equal(got, []string{channel.MarkerFile}) {
		t.Errorf("the folder holds %v, want the marker and no request nobody will read", got)
	}
}

// Only this attempt's own request goes with it, as for every other ending.
func TestAnAttemptThatSucceedsLeavesARequestThatIsNotItsOwn(t *testing.T) {
	foreign, err := json.Marshal(updates.Request{V: updates.WireVersion, ID: "upd_someoneelse", Tag: "v1.3.6",
		RequestedBy: "bob", RequestedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	path := filepath.Join(h.updateDir, channel.RequestFile)
	if err := os.WriteFile(path, foreign, 0o640); err != nil {
		t.Fatal(err)
	}

	restarted := h.restart()
	withVersion(t, "1.3.5")
	h.pass(restarted)

	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
		t.Fatalf("attempt = %s %q, want succeeded", got.State, got.Error)
	}
	if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, foreign) {
		t.Errorf("request.json is now %q (%v), want it as it was", body, err)
	}
}

// The new process is the proof, so a helper that reports a failure after the
// binary was replaced does not make a failure of an update that took. The
// words "still runs" would be false of a controller on the target.
func TestAFailedResultReadAfterTheNewBuildIsRunningIsStillASuccess(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()
	h.helperAnswers(updates.Result{ID: a.ID, OK: false, Error: "the service did not answer its health check in time", FinishedAt: time.Now()})

	restarted := h.restart()
	withVersion(t, "1.3.5")
	h.pass(restarted)

	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded || got.Error != "" {
		t.Errorf("attempt = %s %q, want succeeded with nothing to apologise for", got.State, got.Error)
	}
}

// The helper writes its answer once the new process answers its health check, so
// the new process often starts before there is any result to read. It closes the
// attempt on its first pass all the same, which runs as it starts.
func TestTheNewProcessClosesTheAttemptAsSucceededEvenWithoutAResultFile(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()

	restarted := h.restart()
	restarted.httpClient = &http.Client{Transport: harnessTransport{}}
	withVersion(t, "1.3.5")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := restarted.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Stop(context.Background()) })

	eventually(t, 10*time.Second, "the attempt to close", func() bool {
		return h.attempt(a.ID).State != store.UpdateRequested
	})
	if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
		t.Errorf("attempt = %s %q, want succeeded", got.State, got.Error)
	}
}

func TestAnAttemptTimesOutAfterNinetyMinutes(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()

	h.advance(89 * time.Minute)
	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Fatalf("after 89 minutes the attempt is %s, want it still requested", got.State)
	}

	h.advance(2 * time.Minute)
	h.pass(h.c)
	got := h.attempt(a.ID)
	if got.State != store.UpdateTimedOut || !strings.Contains(got.Error, "90 minutes") {
		t.Errorf("after 91 minutes the attempt is %s %q, want timed_out, saying how long it waited", got.State, got.Error)
	}
	if n, ok := gatherValue(t, h.c, "zoomies_update_attempts_total", map[string]string{"kind": "controller", "result": "timed_out"}); !ok || n != 1 {
		t.Errorf("timed-out controller attempts counted = %v (%v), want 1", n, ok)
	}
}

// Switching updating off stops new requests and nothing else: an attempt already
// in flight is the helper's to finish, and is recorded however it ends.
func TestSwitchingTheModeOffLetsAnInFlightAttemptFinish(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()
	h.inMode("off")

	h.helperAnswers(updates.Result{ID: a.ID, OK: false, Error: "the download failed", FinishedAt: time.Now()})
	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateFailed || got.Error != "the download failed" {
		t.Errorf("attempt = %s %q, want failed with the helper's sentence", got.State, got.Error)
	}
	if _, err := h.c.RequestControllerUpdate(h.ctx, alice, ""); !errors.Is(err, ErrUpdateModeOff) {
		t.Errorf("a request after the switch: err = %v, want ErrUpdateModeOff", err)
	}
	if view := h.status(); view.Controller == nil || view.Controller.State != store.UpdateFailed {
		t.Errorf("the status names %+v, want the failed attempt even with updating off", view.Controller)
	}
}

// A time-out needs no mode either, so an attempt that never hears back is not
// left open for ever because somebody switched updating off.
func TestAnAttemptStillTimesOutWithTheModeOff(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.inMode("off")

	h.advance(91 * time.Minute)
	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateTimedOut {
		t.Errorf("attempt = %s, want timed_out", got.State)
	}
}

// A restored copy must not write over what the live controller is recording, so a
// controller that may not act closes nothing, whatever the folder says.
func TestAFencedControllerClosesNoAttempt(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()
	h.helperAnswers(updates.Result{ID: a.ID, OK: false, Error: "the download failed", FinishedAt: time.Now()})
	h.fence("restored from a backup")

	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Errorf("attempt = %s, want it left open while fenced", got.State)
	}
}

// Turning updating on would otherwise leave the status saying the list has not
// been read until the scheduled check, a day away by default.
func TestTurningUpdatingOnReadsTheReleaseListAtOnce(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.4")
	stub := h.stubGitHub(http.StatusOK, releaseList(releaseEntry("v1.3.5", whenAgo(6*time.Hour), completeAssets(t)...)))

	h.c.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	if got := len(h.c.updates.kick); got != 1 {
		t.Fatalf("turning updating on left %d wake-ups for the loop, want 1", got)
	}
	h.pass(h.c)
	if !slices.Contains(stub.asked, releaseListURL) {
		t.Fatalf("the pass after turning updating on asked %v, want the release list", stub.asked)
	}
	if view := h.status(); view.CheckedAt == nil || view.Target == nil || view.Target.Tag != "v1.3.5" {
		t.Errorf("the status says checked %v, target %+v; want the list read and v1.3.5 named", view.CheckedAt, view.Target)
	}

	// Another change to the section wakes the loop, and asks nothing: the list
	// has been read, and only switching updating on asks for it.
	<-h.c.updates.kick
	stub.asked = nil
	h.c.UpdateConfig(func(c *config.Config) { c.Updates.Soak = 48 * time.Hour })
	if got := len(h.c.updates.kick); got != 1 {
		t.Errorf("changing the soak left %d wake-ups, want 1", got)
	}
	h.pass(h.c)
	if len(stub.asked) != 0 {
		t.Errorf("changing the soak asked GitHub %v, want nothing", stub.asked)
	}

	// A change to another section does not wake it at all.
	<-h.c.updates.kick
	h.c.UpdateConfig(func(c *config.Config) { c.Log.Level = "debug" })
	if got := len(h.c.updates.kick); got != 0 {
		t.Errorf("changing the log level left %d wake-ups, want 0", got)
	}
}

func TestTheStatusSaysWhetherTheHelperIsReady(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.4")

	missing := h.status().Helper
	if missing.State != HelperMissing || missing.InstallCommand != helperInstallCommand || missing.Reason == "" {
		t.Errorf("without a helper the status says %+v, want missing with the command to install it", missing)
	}
	if strings.Contains(missing.Reason, h.updateDir) {
		t.Errorf("every role reads the reason, and it names the folder: %q", missing.Reason)
	}

	h.installHelper()
	ready := h.status().Helper
	if ready.State != HelperReady || ready.InstallCommand != "" || ready.Reason == "" {
		t.Errorf("with a helper the status says %+v, want ready and no command", ready)
	}
}

// An attempt that ends without the helper having read its request takes the
// request back. Left there, it would refuse every later press as an earlier
// request still waiting, for a helper that is not going to read it.
func TestAnAttemptThatEndsTakesBackItsUnreadRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(h *harness, a store.UpdateAttempt)
	}{
		{"timed out", func(h *harness, _ store.UpdateAttempt) { h.advance(91 * time.Minute) }},
		{"refused", func(h *harness, a store.UpdateAttempt) {
			h.helperAnswers(updates.Result{ID: a.ID, OK: false, Error: "the helper could not run", FinishedAt: time.Now()})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			a := h.request()
			tc.end(h, a)
			h.pass(h.c)

			if got := h.attempt(a.ID); got.State == store.UpdateRequested {
				t.Fatalf("the attempt is still open")
			}
			if got := slices.DeleteFunc(h.folder(), func(n string) bool { return n == channel.ResultFile }); !slices.Equal(got, []string{channel.MarkerFile}) {
				t.Errorf("the folder holds %v, want the marker and nothing the attempt wrote", got)
			}
			if err := os.Remove(filepath.Join(h.updateDir, channel.ResultFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			next := h.request()
			if req := h.takeRequest(); req.ID != next.ID {
				t.Errorf("the next press wrote %s, want its own %s", req.ID, next.ID)
			}
		})
	}
}

// Only the attempt's own request is taken back. Anything else at request.json
// may be a person's, or another writer's, and is left for a person to look at.
func TestAnAttemptThatEndsLeavesARequestThatIsNotItsOwn(t *testing.T) {
	foreign, err := json.Marshal(updates.Request{V: updates.WireVersion, ID: "upd_someoneelse", Tag: "v1.3.5",
		RequestedBy: "bob", RequestedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"another attempt's request", foreign},
		{"a file that is not a request", []byte(`{"id":`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.readyToUpdate()
			a := h.request()
			path := filepath.Join(h.updateDir, channel.RequestFile)
			if err := os.WriteFile(path, tc.body, 0o640); err != nil {
				t.Fatal(err)
			}
			h.advance(91 * time.Minute)
			h.pass(h.c)

			if got := h.attempt(a.ID); got.State != store.UpdateTimedOut {
				t.Fatalf("attempt = %s, want timed_out", got.State)
			}
			if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, tc.body) {
				t.Errorf("request.json is now %q (%v), want it as it was", body, err)
			}
		})
	}
}

// A result file can carry a long error, and the attempt's row is read on every
// status. What it keeps is the start of the sentence, cut where a character
// ends so that the page never shows half of one.
func TestTheHelpersSentenceIsKeptToTwoKilobytes(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	a := h.request()
	h.takeRequest()
	long := "x" + strings.Repeat("é", 3000)
	h.helperAnswers(updates.Result{ID: a.ID, OK: false, Error: long, FinishedAt: time.Now()})
	h.pass(h.c)

	got := h.attempt(a.ID).Error
	if len(got) > 2048 || len(got) < 2040 || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Errorf("the attempt keeps %d bytes (valid UTF-8: %v), want the start of the sentence, at most 2048 bytes, cut at a character",
			len(got), utf8.ValidString(got))
	}
}

// The button's once-a-minute limit is shared with the read that turning
// updating on asks for. A press just before the switch, which read only the
// latest release while updating was off, must not cost the list its read.
func TestTurningUpdatingOnInsideTheButtonsMinuteStillReadsTheList(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.4")
	stub := h.stubGitHub(http.StatusOK, `{"tag_name":"v1.3.5","html_url":"https://example.invalid/r"}`)
	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Fatalf("CheckForReleases: %v", err)
	}

	stub.body = releaseList(releaseEntry("v1.3.5", whenAgo(6*time.Hour), completeAssets(t)...))
	h.c.UpdateConfig(func(c *config.Config) { c.Updates.Mode = "manual" })
	h.pass(h.c)
	if slices.Contains(stub.asked, releaseListURL) {
		t.Fatalf("the list was asked for inside the minute: %v", stub.asked)
	}

	h.advance(61 * time.Second)
	h.pass(h.c)
	if !slices.Contains(stub.asked, releaseListURL) {
		t.Fatalf("after the minute the pass asked %v, want the release list", stub.asked)
	}
	if view := h.status(); view.CheckedAt == nil {
		t.Error("the status still says the list has not been read")
	}
}

// Once the request is in the folder the helper may already be acting on it, so
// a status that cannot be worked out is not a refusal: the person is answered
// with the attempt they made, rather than told to press again and be refused.
func TestARequestThatWasWrittenIsAnsweredEvenWhenTheStatusCannotBeRead(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	render := renderUpdates
	t.Cleanup(func() { renderUpdates = render })
	renderUpdates = func(*Controller, context.Context) (*UpdatesView, error) {
		return nil, errors.New("the database is busy")
	}

	view, err := h.c.RequestControllerUpdate(h.ctx, alice, "")
	if err != nil {
		t.Fatalf("RequestControllerUpdate: %v", err)
	}
	open := h.openAttempts()
	if len(open) != 1 || view == nil || view.Controller == nil || view.Controller.ID != open[0].ID || view.Controller.State != store.UpdateRequested {
		t.Fatalf("view = %+v, open = %+v; want the open attempt in the answer", view, open)
	}
	if view.Mode != "manual" || view.Helper.State != HelperReady || view.Reason == "" {
		t.Errorf("view = %+v, want the mode, the helper and a sentence", view)
	}
}
