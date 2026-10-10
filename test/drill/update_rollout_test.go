//go:build drill && linux

package drill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// The rollout drill: automatic updating takes two hosts to the controller's
// release, and the controller is killed between asking the first host and
// hearing back from it.
//
// A rollout is minutes of work spread over several processes, and the
// controller is the one most likely to restart in the middle of it: an operator
// restarts it, a host reboots, or its own update has just put it on the
// release the hosts are being taken to. The task that asks a host to update
// lives in memory and dies with the process; the attempt and the rollout are
// rows. So the claim is that the rows are enough: the controller that comes
// back asks the first host again, closes its attempt as succeeded when the host
// reports the release, and moves on to the second, and no runner on either host
// is restarted along the way.
//
// The window is made, not raced for. Both agents are stopped before the
// controller comes up on the new release, so the first host's task waits in
// the queue where no agent can take it, and the controller is killed while it
// is there. Both agents come back only after the second controller has
// started, so the task they are offered can only be the one it queued again.
//
// The test plays the helper on each host, as the update drill does: it takes
// the request, replaces the agent with the new binary and writes the result.
// What it does not cover is the same as there: systemd, the helper itself and
// zoomies upgrade replacing a file, which test/upgrade/helper-check.sh runs.
func TestAnAutomaticRolloutCarriesOnAcrossAControllerRestart(t *testing.T) {
	const older, newer, target = "1.0.0", "1.0.1", "v1.0.1"
	olderBin, newerBin := releaseBinary(t, older), releaseBinary(t, newer)
	f := newFleetWith(t, fleetOptions{
		// The fleet starts all on one release, so there is nothing to update until
		// the controller moves. Auto with no soak is the planner acting as soon as
		// a host is behind; the release check is off because a drill never talks
		// to github.com, and hosts follow the release the controller runs.
		controllerBin: olderBin,
		controllerEnv: []string{"ZOOMIES_UPDATE_MODE=auto", "ZOOMIES_UPDATE_SOAK=0", "ZOOMIES_UPDATE_CHECK_INTERVAL=0"},
		agentBin:      olderBin,
		updateHelper:  true,
	})
	rec := newRecord(t, "update-rollout")
	defer rec.write()

	ids := f.hostIDs()
	if len(ids) != 1 {
		t.Fatalf("the fleet has hosts %v before the second agent joins, want exactly one", ids)
	}
	first := &rolloutHost{id: ids[0], env: f.agentEnv, proc: f.agent, work: f.agentWork, updateDir: f.agentUpdateDir}
	first.runner, first.poolID, first.job = f.busyRunner(first, "drill-rollout-a", "drill-rollout-a", nil)

	second := f.joinRolloutHost("drill-rollout-b", "drill=rollout-b")
	second.runner, second.poolID, second.job = f.busyRunner(second, "drill-rollout-b", "drill-rollout-b", map[string]string{"drill": "rollout-b"})
	hosts := []*rolloutHost{first, second}
	for _, h := range hosts {
		h.pid = runnerPIDAt(t, h.runnerDir())
		rec.note("job running on "+h.id, "runner pid "+strconv.Itoa(h.pid))
	}
	waitFor(t, waitUpdate, "both hosts to report "+older+" with the helper installed", func() bool {
		for _, h := range hosts {
			if v := f.hostView(h.id); v.Version != older || v.Update == nil {
				return false
			}
		}
		return true
	})

	// Neither agent may take the first task: it has to be in the queue, and
	// only there, when the controller dies. A stopped agent's host still counts
	// as healthy for ninety seconds after its last beat, and the planner asks
	// only a healthy host, so what follows until the rollout asks has to fit in
	// that; it takes a few seconds.
	for _, h := range hosts {
		h.proc.kill()
		requireAlive(t, h, "an agent stopping must not take its job with it")
	}
	// The controller's own update, done by hand: the same database and address,
	// the next release. Auto then starts a rollout and asks the host running the
	// fewest jobs, by name among equals.
	f.controller.kill()
	f.controllerBin = newerBin
	f.startControllerOn(f.stateDir)

	var asked *rolloutHost
	var attempt string
	waitFor(t, waitUpdate, "automatic updating to start a rollout to "+target+" and ask its first host", func() bool {
		for _, h := range hosts {
			if v := f.hostView(h.id); v.Update != nil && v.Update.State == "requested" {
				asked, attempt = h, v.Update.AttemptID
				return true
			}
		}
		return false
	})
	next := second
	if asked == second {
		next = first
	}
	if v := f.hostView(next.id); v.Update == nil || v.Update.State != "none" {
		t.Fatalf("the second host shows %+v while the first is being updated, want no attempt: a rollout updates one host at a time", v.Update)
	}
	rec.note("rollout started", "first host "+asked.id+", attempt "+attempt)

	rec.faultInjected("killed the controller with the first host's update task queued and not yet taken")
	f.controller.kill()
	f.startControllerOn(f.stateDir)
	// The agents come back on the release they had, as the hosts they were. The
	// queue the first task was in is gone with the process that held it.
	for _, h := range hosts {
		h.proc = f.spawn(olderBin, "agent", []string{"agent"}, h.env)
	}

	f.actAsHelper(t, asked, attempt, target, newerBin, older, newer)
	waitFor(t, waitUpdate, "the first host to report "+newer+" and its attempt to end", func() bool {
		v := f.hostView(asked.id)
		return v.Version == newer && v.Update != nil && v.Update.State != "requested"
	})
	if v := f.hostView(asked.id); v.Update.State != "succeeded" || v.Update.AttemptID != attempt {
		t.Fatalf("the first host runs %s and its update shows %+v, want attempt %s succeeded: the attempt asked for before the restart is the one its report answers", v.Version, v.Update, attempt)
	}
	rec.note("first host after the restart", "runs "+newer+", attempt "+attempt+" succeeded")

	// The rollout carries on from its row: nobody asks for the second host.
	var nextAttempt string
	waitFor(t, waitUpdate, "the rollout to ask the second host", func() bool {
		v := f.hostView(next.id)
		if v.Update != nil && v.Update.State == "requested" {
			nextAttempt = v.Update.AttemptID
			return true
		}
		return false
	})
	f.actAsHelper(t, next, nextAttempt, target, newerBin, older, newer)
	waitFor(t, waitUpdate, "the second host to report "+newer+" and its attempt to end", func() bool {
		v := f.hostView(next.id)
		return v.Version == newer && v.Update != nil && v.Update.State != "requested"
	})
	if v := f.hostView(next.id); v.Update.State != "succeeded" || v.Update.AttemptID != nextAttempt {
		t.Fatalf("the second host runs %s and its update shows %+v, want attempt %s succeeded", v.Version, v.Update, nextAttempt)
	}
	rec.note("second host", "runs "+newer+", attempt "+nextAttempt+" succeeded")

	var status struct {
		Rollout *struct {
			Target string `json:"target"`
			State  string `json:"state"`
			Done   int    `json:"done"`
			Total  int    `json:"total"`
		} `json:"rollout"`
	}
	waitFor(t, waitUpdate, "the rollout to finish", func() bool {
		f.api.get("/updates", &status)
		return status.Rollout != nil && status.Rollout.State != "running"
	})
	if r := status.Rollout; r.State != "done" || r.Target != target || r.Done != 2 || r.Total != 2 {
		t.Fatalf("the rollout ended as %+v, want done to %s with both hosts updated", *r, target)
	}
	rec.recovered()
	rec.note("rollout", "done, 2 of 2 hosts")

	if after, want := sortedIDs(f.hostIDs()), sortedIDs([]string{first.id, second.id}); !slices.Equal(after, want) {
		t.Fatalf("the fleet has hosts %v after the rollout but had %v before; an updated agent must come back as the host it was", after, want)
	}
	// The same processes, not replacements that happen to carry the names: two
	// agent restarts, two controller restarts and two updates, and no job was
	// started again.
	for _, h := range hosts {
		requireAlive(t, h, "the rollout must leave the work running")
		if got := runnerPIDAt(t, h.runnerDir()); got != h.pid {
			t.Fatalf("the runner on %s has pid %d after the rollout, want %d: its job was restarted rather than kept", h.id, got, h.pid)
		}
	}
	rec.note("runners after the rollout", "same pids, still running")

	// The jobs finish under the updated agents, so nothing is left running.
	for _, h := range hosts {
		if err := finishJob(h.runnerDir()); err != nil {
			t.Fatalf("telling the stub runner on %s its job is over: %v", h.id, err)
		}
		f.gh.CompleteJob(h.job.ID, "success")
		f.deliverJob("completed", h.job, h.runner.Name, "success")
	}
	waitFor(t, waitRecovery, "both workloads to be gone from their hosts", func() bool {
		return !workloadAlive(first.runnerDir()) && !workloadAlive(second.runnerDir())
	})

	rec.pass("auto took two busy hosts to the controller's release one at a time; the controller killed with the first host's task queued asked it again after the restart, closed its attempt as succeeded on its report and went on to the second, and neither runner was restarted")
}

// rolloutHost is one of the rollout drill's agents and the job running on it.
type rolloutHost struct {
	id        string
	env       []string
	proc      *process
	work      string
	updateDir string

	poolID string
	job    github.QueuedJob
	runner runnerView
	pid    int
}

func (h *rolloutHost) runnerDir() string { return filepath.Join(h.work, "runners", h.runner.Name) }

// joinRolloutHost joins a second agent on the controller's release, with the
// update helper installed beside it and a label a pool can keep its runner to.
// It is named because both agents are on this machine and would otherwise
// claim the same host row.
func (f *fleet) joinRolloutHost(name, label string) *rolloutHost {
	t := f.t
	t.Helper()
	var token struct {
		Token string `json:"token"`
	}
	f.api.post("/join-tokens", map[string]any{"capacity": 2}, &token)
	if token.Token == "" {
		t.Fatal("the join token came back empty")
	}
	dir := t.TempDir()
	h := &rolloutHost{work: filepath.Join(dir, "work")}
	if err := os.MkdirAll(h.work, 0o750); err != nil {
		t.Fatalf("creating the work directory: %v", err)
	}
	if err := stageStubRunner(h.work); err != nil {
		t.Fatalf("staging the stub runner: %v", err)
	}
	h.updateDir = installUpdateFolder(t, dir)
	writeHelperMarker(t, h.updateDir)
	h.env = append(baseEnv(dir),
		"ZOOMIES_CONTROLLER_URL="+f.baseURL,
		"ZOOMIES_AGENT_NAME="+name,
		"ZOOMIES_AGENT_LABELS="+label,
		"ZOOMIES_AGENT_BACKEND=process",
		"ZOOMIES_AGENT_CAPACITY=2",
		"ZOOMIES_WORK_DIR="+h.work,
		"ZOOMIES_AGENT_ALLOW_INSECURE_HTTP=true",
		"ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL=http://127.0.0.1:1/never",
	)
	h.proc = f.spawn(f.agentBin, "agent "+name, []string{"agent"}, append(h.env, "ZOOMIES_JOIN_TOKEN="+token.Token))
	waitFor(t, waitProcessUp, "the agent "+name+" to join", func() bool {
		for _, v := range f.hostViews() {
			if v.Name == name {
				h.id = v.ID
				return true
			}
		}
		return false
	})
	return h
}

// busyRunner puts one job on h and waits for its runner to be busy, so that the
// pool's idle timeout cannot take the runner away while the drill watches it.
func (f *fleet) busyRunner(h *rolloutHost, name, label string, selector map[string]string) (runnerView, string, github.QueuedJob) {
	t := f.t
	t.Helper()
	poolID := f.createPoolOn(name, "process", selector, label)
	job := f.gh.AddQueuedJob("acme/api", "CI", name, []string{"self-hosted", label})
	var runner runnerView
	waitFor(t, waitRunnerCreated, "a runner to be created for "+name, func() bool {
		for _, r := range f.runners(poolID) {
			runner = r
			return true
		}
		return false
	})
	h.runner = runner
	waitFor(t, waitWorkloadUp, "the workload of "+name+" and its persisted PID to appear on its host", func() bool {
		return workloadAlive(h.runnerDir())
	})
	f.gh.StartJob(job.ID, runner.Name)
	f.deliverJob("in_progress", job, runner.Name, "")
	waitFor(t, waitJobDone, "the fleet to see the job of "+name+" start", func() bool {
		r := f.runnerByID(runner.ID, poolID)
		return r.State == "busy" || r.State == "draining"
	})
	return runner, poolID, job
}

// actAsHelper does what the update helper and zoomies upgrade do for one host:
// takes the request its agent wrote, stops the agent, starts the new binary in
// its place with the same environment, and writes the result.
func (f *fleet) actAsHelper(t *testing.T, h *rolloutHost, attempt, target, newerBin, from, to string) {
	t.Helper()
	var req updates.Request
	waitFor(t, waitUpdate, "the agent on "+h.id+" to hand the update helper its request", func() bool {
		r, found, err := channel.PendingRequest(h.updateDir)
		if err != nil {
			t.Fatalf("the agent on %s wrote a request the helper would not read: %v", h.id, err)
		}
		req = r
		return found
	})
	if req.ID != attempt || req.Tag != target {
		t.Fatalf("the agent on %s asked the helper for attempt %q and %q, want %q and %q", h.id, req.ID, req.Tag, attempt, target)
	}
	if err := os.Remove(filepath.Join(h.updateDir, channel.RequestFile)); err != nil {
		t.Fatalf("taking the request as the helper does: %v", err)
	}
	startedAt := time.Now().UTC()
	h.proc.kill()
	requireAlive(t, h, "the agent stopping for its update must not take the job with it")
	h.proc = f.spawn(newerBin, "agent", []string{"agent"}, h.env)
	writeJSONFile(t, filepath.Join(h.updateDir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: attempt, OK: true, Tag: target, From: from, To: to,
		StartedAt: startedAt, FinishedAt: time.Now().UTC(),
	})
}

// requireAlive is requireStillRunning for a runner on any of the drill's hosts.
func requireAlive(t *testing.T, h *rolloutHost, why string) {
	t.Helper()
	deadline := time.Now().Add(waitStaysUp)
	for time.Now().Before(deadline) {
		if !workloadAlive(h.runnerDir()) {
			t.Fatalf("the runner process on %s is gone: %s", h.id, why)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// workloadAlive is workloadRunning for a runner directory on any host: the pid
// the backend recorded, asked of the kernel, because the stub's marker file
// outlives the process that wrote it.
func workloadAlive(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "runner.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// runnerPIDAt is runnerPID for a runner directory on any host.
func runnerPIDAt(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "runner.pid"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the runner in %s has no pid file, so its process cannot be followed", dir)
	} else if err != nil {
		t.Fatalf("reading the pid file in %s: %v", dir, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("the pid file in %s holds %q, which is not a pid", dir, raw)
	}
	return pid
}
