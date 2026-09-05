package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// The embedded agent talks to the controller through the same four calls a
// remote one makes over HTTP, so the single-process case exercises the same
// code rather than a shortcut around it.
func TestEmbeddedTransportRoundTrip(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "embedded-1",
		Capacity:        2,
		OS:              "linux",
		Arch:            "amd64",
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true}},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if resp.HostID == "" || resp.AgentToken == "" {
		t.Fatalf("join response = %+v, want a host ID and a token", resp)
	}
	tr.SetCredentials(resp.HostID, resp.AgentToken)

	host, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !host.Embedded || host.Capacity != 2 || len(host.Backends) != 1 {
		t.Fatalf("host = %+v, want an embedded host with capacity 2 and one backend", host)
	}

	hb, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Capacity: 2})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !hb.OK || hb.Cordoned {
		t.Fatalf("heartbeat = %+v, want ok and uncordoned", hb)
	}

	h.c.enqueue(resp.HostID, agent.Task{Kind: agent.TaskCreateRunner, RunnerID: "run_example"})
	batch, err := tr.PollTasks(h.ctx, time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(batch.Tasks) != 1 || batch.Tasks[0].RunnerID != "run_example" {
		t.Fatalf("batch = %+v, want the one queued task", batch)
	}

	if err := tr.ReportResult(h.ctx, agent.TaskResult{TaskID: batch.Tasks[0].ID, OK: true}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if pending, inflight := h.c.queues.get(resp.HostID).depth(); pending != 0 || inflight != 0 {
		t.Fatalf("queue depth = %d pending, %d in flight; want both zero after a result", pending, inflight)
	}
}

// A poll that arrives before there is work must not spin or sleep out its full
// wait: the task has to reach the agent the moment it is queued.
func TestPollTasksWakesOnEnqueue(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()

	go func() {
		time.Sleep(20 * time.Millisecond)
		h.c.enqueue(host.ID, agent.Task{Kind: agent.TaskRemoveRunner, RunnerID: "run_late"})
	}()

	started := time.Now()
	batch, err := h.c.PollTasks(h.ctx, host.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(batch.Tasks) != 1 {
		t.Fatalf("batch = %+v, want one task", batch)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the poll took %s to notice a task queued after 20ms", elapsed)
	}
}

// A poll with nothing to do returns empty rather than erroring, which is the
// normal idle case for every agent in a quiet fleet.
func TestPollTasksReturnsEmptyWhenIdle(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(h.ctx, 50*time.Millisecond)
	defer cancel()

	batch, err := h.c.PollTasks(ctx, "host_idle", 5*time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(batch.Tasks) != 0 {
		t.Fatalf("batch = %+v, want no tasks", batch)
	}
}

// Enqueueing is idempotent per kind and runner, so a reconcile that reaches
// the same conclusion every ten seconds leaves one task, not a backlog.
func TestEnqueueDeduplicates(t *testing.T) {
	h := newHarness(t)
	for range 5 {
		h.c.enqueue("host_x", agent.Task{Kind: agent.TaskRemoveRunner, RunnerID: "run_1"})
	}
	h.c.enqueue("host_x", agent.Task{Kind: agent.TaskRemoveRunner, RunnerID: "run_2"})
	h.c.enqueue("host_x", agent.Task{Kind: agent.TaskStopRunner, RunnerID: "run_1"})

	if pending, _ := h.c.queues.get("host_x").depth(); pending != 3 {
		t.Fatalf("queue holds %d tasks, want 3 (two runners, one duplicated kind)", pending)
	}
}

// Delivery is at-least-once: a task whose agent never reported back is offered
// again rather than lost, and given up on eventually so it cannot loop forever.
func TestUnansweredTasksAreRequeuedThenDropped(t *testing.T) {
	h := newHarness(t)
	h.c.enqueue("host_x", agent.Task{Kind: agent.TaskCreateRunner, RunnerID: "run_1"})
	q := h.c.queues.get("host_x")

	now := time.Now()
	for attempt := 1; attempt < maxTaskAttempts; attempt++ {
		if got := q.take(10, now); len(got) != 1 {
			t.Fatalf("attempt %d: took %d tasks, want 1", attempt, len(got))
		}
		requeued, dropped := q.sweep(now.Add(createLease + time.Minute))
		if requeued != 1 || len(dropped) != 0 {
			t.Fatalf("attempt %d: requeued %d, dropped %d; want 1 and 0", attempt, requeued, len(dropped))
		}
	}

	if got := q.take(10, now); len(got) != 1 {
		t.Fatal("the final attempt was not offered")
	}
	requeued, dropped := q.sweep(now.Add(createLease + time.Minute))
	if requeued != 0 || len(dropped) != 1 {
		t.Fatalf("requeued %d, dropped %d; want 0 and 1 after %d attempts", requeued, len(dropped), maxTaskAttempts)
	}
}

// A log task belongs to a browser that has since gone away, so it is dropped
// rather than redelivered to open a stream nobody is reading.
func TestLogTasksAreNeverRequeued(t *testing.T) {
	h := newHarness(t)
	h.c.enqueue("host_x", agent.Task{Kind: agent.TaskStreamLogs, RunnerID: "run_1", StreamID: "log_1"})
	q := h.c.queues.get("host_x")

	now := time.Now()
	q.take(10, now)
	requeued, dropped := q.sweep(now.Add(24 * time.Hour))
	if requeued != 0 || len(dropped) != 0 {
		t.Fatalf("requeued %d, dropped %d; a log task should simply sit until its result arrives", requeued, len(dropped))
	}
}

// A task the agent could not carry out has to leave a mark: a runner nobody
// can explain is worse than a failed one.
func TestFailedTaskResultFailsTheRunner(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerProvisioning)

	h.c.enqueue(host.ID, agent.Task{Kind: agent.TaskCreateRunner, RunnerID: r.ID})
	batch, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	err = h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID:   batch.Tasks[0].ID,
		RunnerID: r.ID,
		OK:       false,
		Error:    "docker: no space left on device",
	})
	if err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerFailed {
		t.Fatalf("state = %q, want %q", after.State, store.RunnerFailed)
	}
	if after.Message != "docker: no space left on device" {
		t.Fatalf("message = %q, want the agent's error", after.Message)
	}
}

// A host may only speak for its own runners.
func TestAHostCannotReportOnAnotherHostsRunner(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	other := h.host("vm-2")
	r := h.runnerRow(pool, host, store.RunnerRegistering)

	mustReport(t, h, other.ID, r.ID, store.RunnerFailed)

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerRegistering {
		t.Fatalf("state = %q; a report from the wrong host changed a runner", after.State)
	}
}

// A host going quiet is not a state anybody polls for: the flip publishes an
// event and shows up in Problems.
func TestSilentHostBecomesAProblem(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	h.runnerRow(pool, host, store.RunnerIdle)

	host.LastHeartbeat = time.Now().Add(-2 * store.HeartbeatTimeout)
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}

	codes := h.problemCodes()
	if !contains(codes, "host.unhealthy") {
		t.Fatalf("problems = %v, want one about the silent host", codes)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// A host that came up before its container daemon joins with nothing usable,
// and its agent re-probes as it runs. The controller has to act on that second
// answer: until it does, every pool on that backend matches no host, looks
// perfectly healthy, and quietly starts nothing.
func TestHeartbeatRecordsABackendThatBecameAvailable(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "vm-1",
		Capacity:        2,
		Backends: []backend.Info{{
			Kind:   store.BackendDocker,
			Detail: "cannot connect to /var/run/docker.sock: permission denied",
		}},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.SetCredentials(resp.HostID, resp.AgentToken)

	host, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(host.Backends) != 0 {
		t.Fatalf("backends = %v, want none while the daemon is unreachable", host.Backends)
	}
	// The reason is kept even though the kind is not, because it is the only
	// thing that tells an operator what to fix.
	if info, ok := host.BackendInfo.Find(store.BackendDocker); !ok || info.Available ||
		!strings.Contains(info.Detail, "permission denied") {
		t.Fatalf("backend info = %+v, want docker recorded as unavailable with its reason", host.BackendInfo)
	}

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Capacity:        2,
		Backends: []backend.Info{{
			Kind: store.BackendDocker, Available: true,
			Version: "27.1.1", Endpoint: "unix:///var/run/docker.sock", SupportsDinD: true,
		}},
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	host, err = h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(host.Backends) != 1 || host.Backends[0] != "docker" {
		t.Fatalf("backends = %v, want docker once the daemon answered", host.Backends)
	}
	info, ok := host.BackendInfo.Find(store.BackendDocker)
	if !ok || !info.Available || info.Version != "27.1.1" || !info.SupportsDinD {
		t.Fatalf("backend info = %+v, want the fresh probe in full", host.BackendInfo)
	}
	if info.Detail != "" {
		t.Fatalf("detail = %q, want the stale failure gone", info.Detail)
	}
}

// A heartbeat that carries no probe at all -- an older agent, or one that has
// not probed yet -- must not wipe what the host is known to be able to do.
func TestHeartbeatWithoutABackendProbeKeepsWhatIsKnown(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "vm-1",
		Capacity:        2,
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true, Version: "27.1.1"}},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.SetCredentials(resp.HostID, resp.AgentToken)

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Capacity:        3,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	host, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(host.Backends) != 1 || len(host.BackendInfo) != 1 {
		t.Fatalf("host = %+v, want the backends it joined with left alone", host)
	}
}

// Opening the log viewer on a runner whose container is not there yet -- or
// whose backend would not answer -- comes back as a failed stream_logs task.
// That says nothing about the runner, and must not fail it: the next reconcile
// would otherwise tear down a container that may be mid-job.
func TestFailedLogTaskLeavesTheRunnerAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind agent.TaskKind // what the agent puts in its result; "" is an older agent
	}{
		{"agent names the kind", agent.TaskStreamLogs},
		{"older agent, controller's record decides", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			r := h.runnerRow(pool, host, store.RunnerProvisioning)

			h.c.enqueue(host.ID, agent.Task{Kind: agent.TaskStreamLogs, RunnerID: r.ID, StreamID: "log_1"})
			batch, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
			if err != nil {
				t.Fatalf("PollTasks: %v", err)
			}
			err = h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
				TaskID:   batch.Tasks[0].ID,
				Kind:     tc.kind,
				RunnerID: r.ID,
				OK:       false,
				Error:    "no workload for runner " + r.ID + " on this host, so its logs are gone",
			})
			if err != nil {
				t.Fatalf("ReportResult: %v", err)
			}

			after, err := h.st.GetRunner(h.ctx, r.ID)
			if err != nil {
				t.Fatalf("GetRunner: %v", err)
			}
			if after.State != store.RunnerProvisioning {
				t.Fatalf("state = %q after a failed log task, want it left at %q", after.State, store.RunnerProvisioning)
			}
		})
	}
}

// Capacity belongs to the operator once a host has joined: the Hosts API lets
// them set it to 0 to stop placement, and a heartbeat must not put the agent's
// configured number back.
func TestHeartbeatLeavesCapacityToTheOperator(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "vm-1",
		Capacity:        4,
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true}},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.SetCredentials(resp.HostID, resp.AgentToken)

	host, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	host.Capacity = 0
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Capacity: 4}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	after, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if after.Capacity != 0 {
		t.Fatalf("capacity = %d after a heartbeat, want the operator's 0 kept", after.Capacity)
	}
	if after.LastHeartbeat.IsZero() {
		t.Fatalf("last heartbeat was not recorded")
	}
}

// A compose deployment joined under whatever hostname Docker gave its first
// container, before the compose file set one, and kept that random name for
// life: the identity is the persisted credential, so the row is renamed rather
// than joined again. Unless something else already has the name.
func TestAdoptingEmbeddedCredentialsRenamesTheHostToItsConfiguredName(t *testing.T) {
	h := newHarness(t)
	host := h.host("7096d9a9b798")

	h.c.renameEmbeddedHost(h.ctx, host, "zoomies")
	got, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.Name != "zoomies" {
		t.Fatalf("name = %q, want the configured one", got.Name)
	}

	// Two hosts called zoomies would be worse than one called by its
	// container ID, so a name that is taken stays where it is.
	other := h.host("build-box")
	h.c.renameEmbeddedHost(h.ctx, other, "zoomies")
	if got, _ := h.st.GetHost(h.ctx, other.ID); got.Name != "build-box" {
		t.Fatalf("name = %q; a taken name must not be duplicated", got.Name)
	}
}

// A host that has been silent past the grace is presumed gone, and the runners
// recorded on it with it: until they are failed they count as capacity, so a
// pool pinned at its maximum by a dead host would create nothing for ever
// while looking healthy. The job a busy one was running is the fleet's
// failure, and is marked as such.
func TestRunnersOnAHostSilentPastTheGraceAreFailedAndTheirJobMarkedLost(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	idle := h.runnerRow(pool, host, store.RunnerIdle)
	busy := h.runnerRow(pool, host, store.RunnerRegistering)
	mustReportRunning(t, h, host.ID, busy.ID)
	h.deliverJob(jobEvent{Action: "in_progress", JobID: 7007, Labels: []string{"linux-x64"}, RunnerName: busy.Name})

	host.LastHeartbeat = time.Now().Add(-hostLostAfter - time.Minute)
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	h.c.checkHostHealth(h.ctx)

	for _, id := range []string{idle.ID, busy.ID} {
		after, err := h.st.GetRunner(h.ctx, id)
		if err != nil {
			t.Fatalf("GetRunner: %v", err)
		}
		if after.State != store.RunnerFailed {
			t.Fatalf("runner %s is %q after its host went silent, want %q", id, after.State, store.RunnerFailed)
		}
		if !strings.Contains(after.Message, "vm-1") || !strings.Contains(after.Message, "heartbeat") {
			t.Fatalf("message %q does not say which host went quiet", after.Message)
		}
	}
	job, err := h.st.GetJobByGitHubID(h.ctx, 7007)
	if err != nil {
		t.Fatalf("GetJobByGitHubID: %v", err)
	}
	if job.RunnerFault == "" {
		t.Fatal("the job the busy runner was running was not marked as lost by the fleet")
	}
}

// Unhealthy is not gone. An agent restarting, a daemon being upgraded or a
// network blip all outlast the health timeout, and a host in that state keeps
// its runners until the longer grace has passed.
func TestAHostThatIsMerelyLateKeepsItsRunners(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	r := h.runnerRow(pool, host, store.RunnerIdle)

	host.LastHeartbeat = time.Now().Add(-2 * store.HeartbeatTimeout)
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	h.c.checkHostHealth(h.ctx)

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerIdle {
		t.Fatalf("runner is %q on a host that is late but not lost, want %q", after.State, store.RunnerIdle)
	}
}

// A stop the host never confirms would leave the runner draining for ever,
// counted against its pool's maximum and holding a slot on its host. Once the
// task has been given up on, the runner is too.
func TestAStopTheHostNeverConfirmedFailsTheRunner(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.c.DrainRunner(h.ctx, r.ID, "operator asked"); err != nil {
		t.Fatalf("DrainRunner: %v", err)
	}
	if !h.hasTaskOfKind(host.ID, agent.TaskStopRunner) {
		t.Fatal("draining did not queue a stop task")
	}

	q := h.c.queues.get(host.ID)
	now := time.Now()
	for attempt := 1; attempt <= maxTaskAttempts; attempt++ {
		if got := q.take(10, now); len(got) != 1 {
			t.Fatalf("attempt %d: took %d tasks, want the stop", attempt, len(got))
		}
		now = now.Add(stopLease + time.Minute)
		h.c.sweepTasks(h.ctx, now)
	}

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerFailed {
		t.Fatalf("runner is %q after its stop was given up on, want %q", after.State, store.RunnerFailed)
	}
	if !strings.Contains(after.Message, "never confirmed") {
		t.Fatalf("message %q does not say the stop was never confirmed", after.Message)
	}
}
