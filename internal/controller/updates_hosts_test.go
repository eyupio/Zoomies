package controller

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// hostsCanUpdate is a controller on the release 1.3.5 with updating on, which
// is everything a host behind it needs from the controller's side.
func (h *harness) hostsCanUpdate() {
	h.t.Helper()
	withVersion(h.t, "1.3.5")
	h.inMode("manual")
}

// agentHost seeds a connected host whose agent reports a version and features.
func (h *harness) agentHost(name, ver string, features ...string) *store.Host {
	h.t.Helper()
	host := h.host(name)
	host.Version = ver
	host.Features = features
	host.LastHeartbeat = h.c.Now()
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		h.t.Fatalf("SetHostReported: %v", err)
	}
	return host
}

// updatableHost is a host on 1.3.4 whose agent offers to update itself.
func (h *harness) updatableHost(name string) *store.Host {
	h.t.Helper()
	return h.agentHost(name, "1.3.4", agent.FeatureSelfUpdate)
}

// requestHost presses a host's Update button and fails the test if it is refused.
func (h *harness) requestHost(host *store.Host) store.UpdateAttempt {
	h.t.Helper()
	if _, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID); err != nil {
		h.t.Fatalf("RequestHostUpdate: %v", err)
	}
	latest, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeHost, host.ID, 1)
	if err != nil || len(latest) != 1 || latest[0].State != store.UpdateRequested {
		h.t.Fatalf("no open attempt for the host after a request (%v): %+v", err, latest)
	}
	return latest[0]
}

// poll is the host's agent taking what is queued for it.
func (h *harness) poll(host *store.Host) []agent.Task {
	h.t.Helper()
	return h.c.queues.get(host.ID).take(maxTasksPerPoll, h.c.Now())
}

// updateTasks is the update tasks among those an agent took.
func updateTasks(tasks []agent.Task) []agent.Task {
	var out []agent.Task
	for _, t := range tasks {
		if t.Kind == agent.TaskUpdateAgent {
			out = append(out, t)
		}
	}
	return out
}

// agentBeat is one heartbeat from the host's agent, reporting a version and, when
// it has one, the helper's result.
func (h *harness) agentBeat(host *store.Host, ver string, rep *agent.UpdateReport) {
	h.t.Helper()
	_, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion, Version: ver, Features: []string{agent.FeatureSelfUpdate}, Update: rep,
	})
	if err != nil {
		h.t.Fatalf("Heartbeat: %v", err)
	}
}

func (h *harness) hostAttempts() []store.UpdateAttempt {
	h.t.Helper()
	all, err := h.st.ListUpdateAttempts(h.ctx, store.UpdateScopeHost, "", 50)
	if err != nil {
		h.t.Fatalf("ListUpdateAttempts: %v", err)
	}
	return all
}

func TestAHostBehindTheControllerGetsAnUpdateTask(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	sub := h.listen(events.KindHostUpdated)

	view, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
	if err != nil {
		t.Fatalf("RequestHostUpdate: %v", err)
	}
	all := h.hostAttempts()
	if len(all) != 1 {
		t.Fatalf("%d attempts recorded, want 1", len(all))
	}
	a := all[0]
	if a.Scope != store.UpdateScopeHost || a.HostID != host.ID || a.FromVersion != "1.3.4" || a.ToVersion != "v1.3.5" ||
		a.Trigger != store.UpdateTriggerManual || a.RequestedBy != "alice" || a.State != store.UpdateRequested {
		t.Errorf("attempt = %+v, want the host from 1.3.4 to v1.3.5, asked for by alice by hand", a)
	}

	tasks := h.poll(host)
	if len(tasks) != 1 {
		t.Fatalf("the agent was handed %d tasks, want the one update", len(tasks))
	}
	if task := tasks[0]; task.Kind != agent.TaskUpdateAgent || task.UpdateID != a.ID || task.UpdateTag != "v1.3.5" || task.RunnerID != "" {
		t.Errorf("task = %+v, want an update to v1.3.5 for attempt %s naming no runner", task, a.ID)
	}

	if u := view.Update; u == nil || u.State != store.UpdateRequested || u.AttemptID != a.ID || u.CanUpdate {
		t.Errorf("the host returned has update %+v, want the open attempt and no second press", view.Update)
	}
	frame := nextOfKind(t, sub, events.KindHostUpdated)
	if got, _ := frame["update"].(map[string]any); got["state"] != store.UpdateRequested || got["attempt_id"] != a.ID {
		t.Errorf("the frame's update is %v, want the open attempt", frame["update"])
	}

	// A second press while the first is open is refused, and sends nothing.
	if _, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID); !errors.Is(err, ErrUpdateInProgress) {
		t.Errorf("a second press: err = %v, want ErrUpdateInProgress", err)
	}
	if got := h.poll(host); len(got) != 0 {
		t.Errorf("a second press queued %+v", got)
	}
}

// assertNothingSent says a refused request recorded no attempt and queued no task.
func assertNothingSent(t *testing.T, h *harness, host *store.Host, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if all := h.hostAttempts(); len(all) != 0 {
		t.Errorf("a refused request recorded %d attempts: %+v", len(all), all)
	}
	if got := h.poll(host); len(got) != 0 {
		t.Errorf("a refused request queued %+v", got)
	}
}

// An older agent does not ignore a task kind it has never heard of: it reports
// the task failed. So the task goes only to an agent that says it can do it, on
// the button and on every pass after it.
func TestAHostBehindTheControllerGetsAnUpdateTaskOnlyWhenItAdvertisesSelfUpdate(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	old := h.agentHost("vm-old", "1.3.4")

	_, err := h.c.RequestHostUpdate(h.ctx, alice, old.ID)
	assertNothingSent(t, h, old, err, ErrUpdateHostCannotUpdate)
	if !strings.Contains(err.Error(), "helper install") {
		t.Errorf("the refusal does not say how to make the host able to: %v", err)
	}
	if u := h.view(old.ID).Update; u.CanUpdate || !strings.Contains(u.Reason, "does not offer") {
		t.Errorf("the card says %+v, want it unable to update and saying why", u)
	}

	// An agent that stopped offering it after the press, an agent rolled back or
	// a helper removed, is sent nothing more.
	host := h.updatableHost("vm-1")
	h.requestHost(host)
	h.c.queues.forget(host.ID)
	h.agentHost("vm-1-again", "1.3.4") // a second host, to show the pass ran over hosts
	host.Features = nil
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	h.pass(h.c)
	if got := updateTasks(h.poll(host)); len(got) != 0 {
		t.Errorf("a host that no longer offers to update itself was sent %+v", got)
	}
	host.Features = []string{agent.FeatureSelfUpdate}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	h.pass(h.c)
	if got := updateTasks(h.poll(host)); len(got) != 1 {
		t.Errorf("the host offers it again and was sent %d update tasks, want 1", len(got))
	}
}

// The agent inside the controller is updated with the controller, so it is
// never sent the task, whether a person asks or an attempt is somehow open.
func TestAHostBehindTheControllerGetsAnUpdateTaskNeverWhenEmbedded(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := &store.Host{
		Name: "controller", Capacity: 4, Embedded: true, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	host.Version, host.Features = "1.3.4", []string{agent.FeatureSelfUpdate}
	if err := h.st.SetHostReported(h.ctx, host); err != nil {
		t.Fatal(err)
	}

	_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
	assertNothingSent(t, h, host, err, ErrUpdateHostCannotUpdate)
	if u := h.view(host.ID).Update; u.CanUpdate || !strings.Contains(u.Reason, "updated with the controller") {
		t.Errorf("the card says %+v, want it to say the controller updates it", u)
	}

	if err := h.st.CreateUpdateAttempt(h.ctx, &store.UpdateAttempt{
		Scope: store.UpdateScopeHost, HostID: host.ID, FromVersion: "1.3.4", ToVersion: "v1.3.5", Trigger: store.UpdateTriggerManual,
	}); err != nil {
		t.Fatal(err)
	}
	h.pass(h.c)
	if got := updateTasks(h.poll(host)); len(got) != 0 {
		t.Errorf("the embedded agent was sent %+v", got)
	}
}

// An agent is never taken backwards, and one on the controller's own release has
// nothing to take. Two builds of one release are the same release.
func TestAHostBehindTheControllerGetsAnUpdateTaskNeverWhenAheadOfTheController(t *testing.T) {
	for _, running := range []string{"1.3.6", "1.3.5", "v1.3.5", "2.0.0"} {
		t.Run(running, func(t *testing.T) {
			h := newHarness(t)
			h.hostsCanUpdate()
			host := h.agentHost("vm-1", running, agent.FeatureSelfUpdate)
			_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
			assertNothingSent(t, h, host, err, ErrUpdateHostCannotUpdate)
			if u := h.view(host.ID).Update; u.CanUpdate || !strings.Contains(u.Reason, "already runs") {
				t.Errorf("the card says %+v, want it to say there is nothing to update", u)
			}
		})
	}
}

// A restored copy must not replace binaries in a live deployment.
func TestAHostBehindTheControllerGetsAnUpdateTaskNotWhileFenced(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	open := h.updatableHost("vm-2")
	h.requestHost(open)
	h.c.queues.forget(open.ID)
	h.fence("restored from a backup")

	_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
	if !errors.Is(err, ErrUpdateFenced) {
		t.Fatalf("err = %v, want ErrUpdateFenced", err)
	}
	if got := h.poll(host); len(got) != 0 {
		t.Errorf("a fenced controller queued %+v", got)
	}
	h.pass(h.c)
	if got := h.poll(open); len(got) != 0 {
		t.Errorf("a fenced controller queued the open attempt's task again: %+v", got)
	}
}

func TestAHostUpdateIsRefusedWhenTheModeIsOff(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	h.inMode("off")
	host := h.updatableHost("vm-1")
	_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
	assertNothingSent(t, h, host, err, ErrUpdateModeOff)
}

// A controller built from main has no release to take its hosts to, and the
// command by hand is what still works.
func TestAHostCannotBeUpdatedWhenTheControllerIsNotARelease(t *testing.T) {
	h := newHarness(t)
	h.inMode("manual")
	withVersion(t, "main-sha-abc1234")
	host := h.updatableHost("vm-1")
	command, _, _ := hostUpgrade(host, version.Version)

	_, err := h.c.RequestHostUpdate(h.ctx, alice, host.ID)
	assertNothingSent(t, h, host, err, ErrUpdateNotARelease)
	view := h.view(host.ID)
	if view.Update.CanUpdate || !strings.Contains(view.Update.Reason, "not running a release") {
		t.Errorf("the card says %+v, want it to say the controller is not a release", view.Update)
	}
	if view.UpgradeCommand != command {
		t.Errorf("upgrade_command = %q, want it unchanged as %q", view.UpgradeCommand, command)
	}
}

// A failed update says nothing about any runner on the host, and must never be
// counted against one: a failed runner is counted against its pool.
func TestAnUpdateTaskIsNotLifecycleWork(t *testing.T) {
	if lifecycleTask(agent.TaskUpdateAgent) {
		t.Fatal("an update task counts as lifecycle work")
	}
	h := newHarness(t)
	h.hostsCanUpdate()
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.updatableHost("vm-1")
	r := h.seedRunner(t, pool, host, store.RunnerIdle)
	a := h.requestHost(host)
	task := h.poll(host)[0]

	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: task.ID, Kind: agent.TaskUpdateAgent, RunnerID: r.ID, OK: false,
		Error: "the update helper is not installed on this host, so nothing would read the request", CompletedAt: h.c.Now(),
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if got := h.runnerByID(t, r.ID); got.State != store.RunnerIdle {
		t.Errorf("the runner is %s after a failed update, want it left idle", got.State)
	}
	if got := h.attempt(a.ID); got.State != store.UpdateFailed || !strings.Contains(got.Error, "the update helper is not installed") {
		t.Errorf("attempt = %s %q, want failed with the agent's sentence", got.State, got.Error)
	}
}

// A kind with no lease stays in flight for ever, and enqueue refuses a task
// whose key is in flight, so an agent that went away holding one would have
// left its host's attempt to time out with the task never offered again.
func TestAnUpdateTaskHasALease(t *testing.T) {
	if requeueAfter(agent.TaskUpdateAgent) <= 0 {
		t.Fatal("an update task has no lease")
	}
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	h.requestHost(host)
	if got := h.poll(host); len(got) != 1 {
		t.Fatalf("the agent took %d tasks, want 1", len(got))
	}
	q := h.c.queues.get(host.ID)
	if requeued, _ := q.sweep(h.c.Now().Add(requeueAfter(agent.TaskUpdateAgent) + time.Second)); requeued != 1 {
		t.Errorf("after the lease the sweep offered %d tasks again, want the update", requeued)
	}
}

// The queue is in memory, so a restart, or a task lost with its agent, loses it.
// The pass sends it again until the agent says it took it, and not after.
func TestTheUpdateTaskIsSentAgainUntilTheAgentHasTakenIt(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	h.c.queues.forget(host.ID)

	h.pass(h.c)
	tasks := updateTasks(h.poll(host))
	if len(tasks) != 1 || tasks[0].UpdateID != a.ID {
		t.Fatalf("after the queue was lost the pass sent %+v, want the attempt's task", tasks)
	}
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{TaskID: tasks[0].ID, Kind: agent.TaskUpdateAgent, OK: true, CompletedAt: h.c.Now()}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	h.c.queues.forget(host.ID)
	h.pass(h.c)
	if got := updateTasks(h.poll(host)); len(got) != 0 {
		t.Errorf("the agent wrote the request and was sent the task again: %+v", got)
	}
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Errorf("the agent's answer moved the attempt to %s; only the host's version says it arrived", got.State)
	}
}

func TestAHostSucceedsWhenItHeartbeatsTheTargetVersion(t *testing.T) {
	for _, reported := range []string{"1.3.5", "1.3.6"} {
		t.Run(reported, func(t *testing.T) {
			h := newHarness(t)
			h.hostsCanUpdate()
			host := h.updatableHost("vm-1")
			a := h.requestHost(host)
			h.poll(host)

			h.agentBeat(host, "1.3.4", nil)
			if got := h.attempt(a.ID); got.State != store.UpdateRequested {
				t.Fatalf("a beat from the old build closed the attempt as %s", got.State)
			}

			sub := h.listen(events.KindHostUpdated)
			// The release binary reports the version without the tag's v, and an
			// agent past the tag has arrived too.
			h.agentBeat(host, reported, nil)
			if got := h.attempt(a.ID); got.State != store.UpdateSucceeded {
				t.Fatalf("attempt = %s after the host reported %s, want succeeded", got.State, reported)
			}
			frame := nextOfKind(t, sub, events.KindHostUpdated)
			if got, _ := frame["update"].(map[string]any); got["state"] != store.UpdateSucceeded || frame["version"] != reported {
				t.Errorf("the frame says version %v, update %v; want the new version and the attempt succeeded", frame["version"], frame["update"])
			}
			if n, ok := gatherValue(t, h.c, "zoomies_update_attempts_total", map[string]string{"kind": "host", "result": "succeeded"}); !ok || n != 1 {
				t.Errorf("succeeded host attempts counted = %v (%v), want 1", n, ok)
			}
		})
	}
}

// The agent sends the helper's result until a beat has been answered, and a new
// agent process sends it once more, so the same report arrives more than once.
func TestARepeatedHeartbeatResultIsANoOp(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	rep := &agent.UpdateReport{ID: a.ID, OK: false, Tag: "v1.3.5", From: "1.3.4", Error: "the download failed", FinishedAt: h.c.Now().UTC()}

	h.agentBeat(host, "1.3.4", rep)
	first := h.attempt(a.ID)
	if first.State != store.UpdateFailed {
		t.Fatalf("attempt = %s, want failed", first.State)
	}
	h.advance(time.Minute)
	h.agentBeat(host, "1.3.4", rep)
	if again := h.attempt(a.ID); again.State != first.State || again.Error != first.Error || !again.FinishedAt.Equal(*first.FinishedAt) {
		t.Errorf("the repeat changed the attempt: %+v, was %+v", again, first)
	}
	if n, _ := gatherValue(t, h.c, "zoomies_update_attempts_total", map[string]string{"kind": "host", "result": "failed"}); n != 1 {
		t.Errorf("failed host attempts counted = %v, want 1", n)
	}

	// A press after the failure opens a new attempt, which the old report does
	// not answer.
	b := h.requestHost(host)
	h.agentBeat(host, "1.3.4", rep)
	if got := h.attempt(b.ID); got.State != store.UpdateRequested {
		t.Errorf("the old report closed the new attempt as %s", got.State)
	}
}

func TestAFailedResultOnTheHeartbeatClosesTheAttempt(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	h.poll(host)

	// The agent cleans the helper's text, but the controller does not take its
	// word for it.
	said := "the checksum did not match\x1b[31m‮" + strings.Repeat("é", 3000) + "\xff"
	h.agentBeat(host, "1.3.4", &agent.UpdateReport{ID: a.ID, OK: false, Tag: "v1.3.5", Error: said, FinishedAt: h.c.Now().UTC()})
	got := h.attempt(a.ID)
	if got.State != store.UpdateFailed || !strings.HasPrefix(got.Error, "the checksum did not match") {
		t.Fatalf("attempt = %s %q, want failed with the helper's sentence", got.State, cutAt(got.Error, 80))
	}
	if len(got.Error) > maxHelperSentence || !utf8.ValidString(got.Error) {
		t.Errorf("the sentence kept is %d bytes, valid UTF-8 %v; want at most %d, valid", len(got.Error), utf8.ValidString(got.Error), maxHelperSentence)
	}
	for _, r := range got.Error {
		if unicode.IsControl(r) || isBidiControl(r) {
			t.Errorf("the sentence kept carries %U", r)
			break
		}
	}
	if q, ok := h.c.queues.all()[host.ID]; ok {
		if pending, _ := q.depth(); pending != 0 {
			t.Errorf("%d tasks still wait for a host whose attempt has ended", pending)
		}
	}
}

// The helper refuses a request it cannot trust without echoing an id. Written
// after this attempt was asked for, it can only be the answer to it.
func TestARefusalWithoutAnIDAfterTheRequestClosesTheHostsAttempt(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)

	h.agentBeat(host, "1.3.4", &agent.UpdateReport{OK: false, Error: "a refusal from before this request", FinishedAt: a.RequestedAt.Add(-time.Minute).UTC()})
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Fatalf("a refusal older than the request closed the attempt as %s", got.State)
	}
	h.agentBeat(host, "1.3.4", &agent.UpdateReport{OK: false, Error: "the request is owned by another account", FinishedAt: a.RequestedAt.Add(time.Second).UTC()})
	if got := h.attempt(a.ID); got.State != store.UpdateFailed || got.Error != "the request is owned by another account" {
		t.Errorf("attempt = %s %q, want failed with the helper's refusal", got.State, got.Error)
	}
}

// An agent relays whatever is in its update folder. A result for another
// attempt, the controller's own where the two share a folder, or one long
// closed, answers nothing here.
func TestAResultForAnotherAttemptIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	other := h.updatableHost("vm-2")
	b := h.requestHost(other)
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)

	for _, rep := range []agent.UpdateReport{
		{ID: "upd_controllers0wn", OK: false, Error: "the controller's own update failed", FinishedAt: h.c.Now().UTC()},
		{ID: "upd_controllers0wn", OK: true, Tag: "v1.3.5", To: "1.3.5", FinishedAt: h.c.Now().UTC()},
		{ID: "upd_someotherhost", OK: false, Error: "another host's update failed", FinishedAt: h.c.Now().UTC()},
	} {
		h.agentBeat(host, "1.3.4", &rep)
		if got := h.attempt(a.ID); got.State != store.UpdateRequested {
			t.Fatalf("a report for %s closed this host's attempt as %s %q", rep.ID, got.State, got.Error)
		}
	}

	// And a report for this attempt, the newest of all, reaching another host
	// closes nothing, there or here.
	h.agentBeat(other, "1.3.4", &agent.UpdateReport{ID: a.ID, OK: false, Error: "the download failed", FinishedAt: h.c.Now().UTC()})
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Errorf("another host's beat closed this host's attempt as %s", got.State)
	}
	if got := h.attempt(b.ID); got.State != store.UpdateRequested {
		t.Errorf("a report for another host's attempt closed this one as %s", got.State)
	}
}

func TestAHostAttemptTimesOutAfterNinetyMinutes(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)

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
	// The task still waiting is taken back, so a host that comes back later is
	// not updated with nothing recording it.
	if got := updateTasks(h.poll(host)); len(got) != 0 {
		t.Errorf("a timed-out attempt's task is still queued: %+v", got)
	}
}

// The agent may still hold the task of an attempt that has ended. The next
// attempt's task is its own, and is not held back behind it.
func TestATaskLeftInFlightDoesNotHoldBackTheNextAttempt(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	if got := updateTasks(h.poll(host)); len(got) != 1 || got[0].UpdateID != a.ID {
		t.Fatalf("the agent took %+v, want the first attempt's task", got)
	}
	h.advance(91 * time.Minute)
	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateTimedOut {
		t.Fatalf("the first attempt is %s, want timed_out", got.State)
	}
	b := h.requestHost(host)
	if got := updateTasks(h.poll(host)); len(got) != 1 || got[0].UpdateID != b.ID {
		t.Errorf("the agent was handed %+v, want the new attempt's task", got)
	}
}

// An agent that keeps giving the task back unstarted, because it keeps being
// restarted, has not said anything about the update. The attempt stays open
// for the pass to send again, and ends only as every attempt does.
func TestAnUpdateTaskGivenBackUnstartedLeavesTheAttemptOpen(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	for range maxTaskAttempts {
		tasks := updateTasks(h.poll(host))
		if len(tasks) != 1 {
			t.Fatalf("the agent was handed %d update tasks, want 1", len(tasks))
		}
		if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
			TaskID: tasks[0].ID, Kind: agent.TaskUpdateAgent, NotStarted: true,
			Error: "the agent is shutting down", CompletedAt: h.c.Now(),
		}); err != nil {
			t.Fatalf("ReportResult: %v", err)
		}
	}
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Errorf("a task given back unstarted closed the attempt as %s %q", got.State, got.Error)
	}
	h.pass(h.c)
	if got := updateTasks(h.poll(host)); len(got) != 1 {
		t.Errorf("the pass sent %d update tasks after the agent gave the last one back, want 1", len(got))
	}
}

// An update restarts the agent, so a host going quiet is what an update in
// progress looks like. Only the attempt's own time-out ends it.
func TestAnUnhealthyHostKeepsItsOpenAttemptUntilTheTimeout(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)

	h.advance(30 * time.Minute)
	h.c.checkHostHealth(h.ctx)
	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateRequested {
		t.Fatalf("a host silent for 30 minutes had its attempt closed as %s", got.State)
	}
	if u := h.view(host.ID).Update; u.State != store.UpdateRequested {
		t.Errorf("the card of a silent host says %+v, want the attempt still open", u)
	}
	h.advance(61 * time.Minute)
	h.pass(h.c)
	if got := h.attempt(a.ID); got.State != store.UpdateTimedOut {
		t.Errorf("after 91 minutes the attempt is %s, want timed_out", got.State)
	}
}

// A host that goes away mid-update has nothing left to report, so its attempt is
// closed rather than left for the time-out, and nothing is sent after it.
func TestADeletedHostsOpenAttemptIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)

	if err := h.c.DeleteHost(h.ctx, host.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if got := h.attempt(a.ID); got.State != store.UpdateCancelled {
		t.Errorf("the deleted host's attempt is %s, want cancelled", got.State)
	}
	h.pass(h.c)
	if q, ok := h.c.queues.all()[host.ID]; ok {
		if pending, inflight := q.depth(); pending+inflight != 0 {
			t.Errorf("a deleted host has %d tasks queued for it", pending+inflight)
		}
	}

	// A host that went another way, re-joined under a new id, say, leaves an
	// open attempt naming a host that is not there; the pass closes it.
	gone := &store.UpdateAttempt{Scope: store.UpdateScopeHost, HostID: "hst_gone", FromVersion: "1.3.4", ToVersion: "v1.3.5", Trigger: store.UpdateTriggerAuto}
	if err := h.st.CreateUpdateAttempt(h.ctx, gone); err != nil {
		t.Fatal(err)
	}
	h.pass(h.c)
	if got := h.attempt(gone.ID); got.State != store.UpdateCancelled || !strings.Contains(got.Error, "removed") {
		t.Errorf("the attempt of a host that is gone is %s %q, want cancelled, saying why", got.State, got.Error)
	}
}

// Every card is diffed and sent when its bytes move, so anything that changes
// with the clock would send every host's card on every pass.
func TestTheHostViewCarriesNoElapsedTime(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	h.requestHost(host)

	first, err := json.Marshal(h.view(host.ID))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(h.view(host.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("two renders of the same host differ:\n%s\n%s", first, second)
	}

	before, _ := json.Marshal(h.view(host.ID).Update)
	h.advance(45 * time.Minute)
	after, _ := json.Marshal(h.view(host.ID).Update)
	if string(before) != string(after) {
		t.Errorf("the update block moved with the clock:\n%s\n%s", before, after)
	}
}

// The helper's sentence can name a path on the host, and the host card is read
// by every role and sent on the stream to all of them. Only a caller that knows
// its reader holds the platform role gets the text.
func TestTheHelpersSentenceOnAHostIsShownOnlyToThePlatform(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	sub := h.listen(events.KindHostUpdated)
	const said = "could not replace /usr/local/bin/zoomies: text file busy"
	h.agentBeat(host, "1.3.4", &agent.UpdateReport{ID: a.ID, OK: false, Error: said, FinishedAt: h.c.Now().UTC()})

	view := h.view(host.ID)
	if view.Update.State != store.UpdateFailed || view.Update.Reason != withheldAttemptError(store.UpdateFailed) {
		t.Errorf("the card says %+v, want failed with the fixed sentence", view.Update)
	}
	if got := view.For(false).Update.Reason; strings.Contains(got, "/usr/local") {
		t.Errorf("below the platform the card says %q", got)
	}
	if got := view.For(true).Update.Reason; got != said {
		t.Errorf("the platform reads %q, want the helper's sentence", got)
	}
	if view.Update.Reason != withheldAttemptError(store.UpdateFailed) {
		t.Error("For(true) changed the view it was called on")
	}
	frame := nextOfKind(t, sub, events.KindHostUpdated)
	if raw, _ := json.Marshal(frame); strings.Contains(string(raw), "/usr/local") {
		t.Errorf("the frame on the stream carries the helper's text: %s", raw)
	}
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if strings.Contains(p.Title+p.Detail+p.Fix, "/usr/local") {
			t.Errorf("%s carries the helper's text to the fleet's list", p.Code)
		}
	}
}

func TestAFailedHostUpdateIsAProblemUntilTheHostReachesTheRelease(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	a := h.requestHost(host)
	h.agentBeat(host, "1.3.4", &agent.UpdateReport{ID: a.ID, OK: false, Error: "the download failed", FinishedAt: h.c.Now().UTC()})

	var p Problem
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ps {
		if item.Code == "host.update_failed" {
			p = item
		}
	}
	if p.Code == "" || p.Severity != config.SeverityError || p.TargetID != host.ID || p.Remedy != nil || !strings.Contains(p.Title, "vm-1") {
		t.Fatalf("problem = %+v, want an error about vm-1 with no remedy", p)
	}
	h.agentBeat(host, "1.3.5", nil)
	if contains(h.problemCodes(), "host.update_failed") {
		t.Error("the failure is still a problem after the host reached the release by hand")
	}
}

func TestAHostThatCannotBeUpdatedFromHereIsANote(t *testing.T) {
	h := newHarness(t)
	h.hostsCanUpdate()
	old := h.agentHost("vm-old", "1.3.4")
	h.updatableHost("vm-ready")
	h.agentHost("vm-current", "1.3.5")

	var raised []Problem
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Code == "host.update_unavailable" {
			raised = append(raised, p)
		}
	}
	if len(raised) != 1 || raised[0].TargetID != old.ID || raised[0].Severity != config.SeverityInfo || raised[0].Remedy != nil {
		t.Fatalf("raised %+v, want one note about vm-old and no remedy", raised)
	}
	if !strings.Contains(raised[0].Fix, helperInstallCommand) {
		t.Errorf("the fix does not say how to make it able to: %q", raised[0].Fix)
	}
	h.inMode("off")
	if contains(h.problemCodes(), "host.update_unavailable") {
		t.Error("with updating off a host that cannot be updated from here is still a problem")
	}
}

// A host whose update failed still runs the release it had and places work on
// it, so a public status page turned "blocked" by it would be untrue.
func TestHostUpdateProblemsAreExemptFromThePublicStatusPage(t *testing.T) {
	for _, code := range []string{"host.update_failed", "host.update_unavailable"} {
		if audienceFor(code) != AudienceFleet {
			t.Errorf("%s is %q, want the fleet's", code, audienceFor(code))
		}
		if !statusExempt(code) {
			t.Errorf("%s is not exempt from the status page", code)
		}
		if _, has := publicSentences[code]; has {
			t.Errorf("%s has a public sentence it can never show", code)
		}
	}

	h := newHarness(t)
	h.hostsCanUpdate()
	host := h.updatableHost("vm-1")
	h.agentHost("vm-old", "1.3.4")
	a := h.requestHost(host)
	h.agentBeat(host, "1.3.4", &agent.UpdateReport{ID: a.ID, OK: false, Error: "the download failed", FinishedAt: h.c.Now().UTC()})
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if codes := h.problemCodes(); !contains(codes, "host.update_failed") || !contains(codes, "host.update_unavailable") {
		t.Fatalf("raised %v, want both host update codes, or the rest proves nothing", codes)
	}
	st := ProjectStatus(ps, nil)
	body, _ := json.Marshal(st)
	if strings.Contains(string(body), "host.update_") {
		t.Errorf("the status carries a host update problem: %s", body)
	}
	// The rest of the list (the hosts behind the controller are a warning of their
	// own) moves the state; these two alone must not.
	var ours []Problem
	for _, p := range ps {
		if strings.HasPrefix(p.Code, "host.update_") {
			ours = append(ours, p)
		}
	}
	if alone := ProjectStatus(ours, nil); alone.State != FleetHealthy || len(alone.Reasons) != 0 {
		t.Errorf("status = %+v, want healthy: a failed host update moves nothing a waiting job cares about", alone)
	}
}

// A folder that is a link reads its target's marker, and every request is then
// refused for writing through a link, so it is not a ready helper.
func TestAnUpdateFolderThatIsALinkIsNotReady(t *testing.T) {
	h := newHarness(t)
	h.readyToUpdate()
	real := h.updateDir + "-real"
	if err := os.Rename(h.updateDir, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, h.updateDir); err != nil {
		t.Skipf("cannot make a link here: %v", err)
	}
	if got := h.status().Helper.State; got != HelperMissing {
		t.Errorf("a linked update folder reads as %s, want missing", got)
	}
	if _, err := h.c.RequestControllerUpdate(h.ctx, alice, ""); !errors.Is(err, ErrUpdateHelperMissing) {
		t.Errorf("err = %v, want ErrUpdateHelperMissing", err)
	}
}
