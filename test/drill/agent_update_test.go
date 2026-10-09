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
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// The update drill: an agent is updated from the fleet while a job is running
// on its host.
//
// An update is the agent restart an operator asks for by pressing a button,
// and pressing it on a busy fleet is the whole point of having one: a fleet
// that had to be emptied first would be updated by hand, as it is today. So
// the claims are the restart drill's, with a new binary on the far side of the
// restart. The runner is the same process before and after, the agent comes back as the
// host it was, and the job finishes. On top of those, the fleet has to say the
// update worked because the host now runs the release, which is the only proof
// it accepts.
//
// The test plays the root half of the update. It installs the helper's marker,
// takes the request the agent writes, and does what `zoomies upgrade` does to
// an agent: stops it, puts the new binary in its place and starts it with the
// same environment. Then it writes the helper's result.
//
// What it does not cover: systemd, the helper itself, and `zoomies upgrade`
// replacing a file on disk. The agent is killed outright, which is harsher than
// a unit's stop, and here the runner outlives it because it is in a process
// group of its own; whether it outlives the unit's stop depends on the unit's
// KillMode, which only a real host can show. It is Linux only because the
// helper is a pair of systemd units, so an agent anywhere else never offers to
// update itself.
func TestUpdatingAnAgentMidJobLeavesTheWorkRunning(t *testing.T) {
	const older, newer, target = "1.0.0", "1.0.1", "v1.0.1"
	olderBin, newerBin := releaseBinary(t, older), releaseBinary(t, newer)
	f := newFleetWith(t, fleetOptions{
		// The controller is the release the hosts are taken to. Manual is the
		// mode that lets a person press the button, and the release check is
		// off because a drill never talks to github.com: updating a host needs
		// only the release the controller runs.
		controllerBin: newerBin,
		controllerEnv: []string{"ZOOMIES_UPDATE_MODE=manual", "ZOOMIES_UPDATE_CHECK_INTERVAL=0"},
		agentBin:      olderBin,
		updateHelper:  true,
	})
	rec := newRecord(t, "agent-update")
	defer rec.write()

	label := "drill-agent-update"
	poolID := f.createPool("drill-update", label)
	job := f.gh.AddQueuedJob("acme/api", "CI", "build", []string{"self-hosted", label})

	var runner runnerView
	waitFor(t, waitRunnerCreated, "a runner to be created for the pool", func() bool {
		for _, r := range f.runners(poolID) {
			runner = r
			return true
		}
		return false
	})
	waitFor(t, waitWorkloadUp, "a workload and its persisted PID to appear on the host", func() bool {
		return len(f.liveWorkloads()) == 1 && f.workloadRunning(runner.Name)
	})
	f.gh.StartJob(job.ID, runner.Name)
	f.deliverJob("in_progress", job, runner.Name, "")
	waitFor(t, waitJobDone, "the fleet to see the job start", func() bool {
		r := f.runnerByID(runner.ID, poolID)
		return r.State == "busy" || r.State == "draining"
	})
	pid := f.runnerPID(runner.Name)
	rec.note("job running on the host", "runner pid "+strconv.Itoa(pid))

	hosts := f.hostIDs()
	if len(hosts) != 1 {
		t.Fatalf("the fleet has hosts %v before the second agent joins, want exactly one", hosts)
	}
	hostID := hosts[0]

	// A second host whose helper never finished installing: the folder is there
	// and the marker is not. It joins only now, with the pool at its one runner
	// and nothing else queued, so nothing is placed on it.
	bareID, bareDir := f.startAgentWithoutHelperMarker("drill-no-helper")
	bare := f.hostView(bareID)
	if bare.Update == nil || bare.Update.CanUpdate {
		t.Fatalf("a host whose agent does not offer to update itself shows %+v, want an update block that cannot update", bare.Update)
	}
	// Refused before anything is recorded or queued: the agent would answer a
	// task it was never offered with a failure, and the attempt is the record of
	// a request somebody made, not of one the fleet knew would fail.
	status, body := f.api.try("POST", "/hosts/"+bareID+"/update", nil)
	if status != 409 || !strings.Contains(string(body), `"update.host_cannot_update"`) {
		t.Fatalf("asking to update a host whose agent does not offer it answered %d %s, want 409 update.host_cannot_update", status, body)
	}
	rec.note("update asked of the host without the helper's marker", "refused: "+bare.Update.Reason)

	waitFor(t, waitUpdate, "the agent to offer to update itself to "+target, func() bool {
		h := f.hostView(hostID)
		return h.Version == older && h.Update != nil && h.Update.CanUpdate
	})
	var asked hostView
	f.api.post("/hosts/"+hostID+"/update", nil, &asked)
	if asked.Update == nil || asked.Update.State != "requested" || asked.Update.AttemptID == "" {
		t.Fatalf("the update was accepted with %+v, want an attempt that is requested", asked.Update)
	}
	attempt := asked.Update.AttemptID
	rec.note("update asked for", attempt+" to "+target)

	// The helper's half, done by hand. The real one waits for request.json with
	// a path unit and removes it once read.
	var req updates.Request
	waitFor(t, waitUpdate, "the agent to hand the update helper its request", func() bool {
		r, found, err := channel.PendingRequest(f.agentUpdateDir)
		if err != nil {
			t.Fatalf("the agent wrote a request the helper would not read: %v", err)
		}
		req = r
		return found
	})
	if req.ID != attempt || req.Tag != target {
		t.Fatalf("the agent asked the helper for attempt %q and %q, want %q and %q", req.ID, req.Tag, attempt, target)
	}
	if err := os.Remove(filepath.Join(f.agentUpdateDir, channel.RequestFile)); err != nil {
		t.Fatalf("taking the request as the helper does: %v", err)
	}

	startedAt := time.Now().UTC()
	rec.faultInjected("replaced the agent's binary and restarted it while the job was running, as the update helper does")
	f.agent.kill()
	f.requireStillRunning(runner.Name, "the agent stopping for its update must not take the job with it")
	f.agent = f.spawn(newerBin, "agent", []string{"agent"}, f.agentEnv)
	writeJSONFile(t, filepath.Join(f.agentUpdateDir, channel.ResultFile), updates.Result{
		V: updates.WireVersion, ID: attempt, OK: true, Tag: target, From: older, To: newer,
		StartedAt: startedAt, FinishedAt: time.Now().UTC(),
	})

	waitFor(t, waitUpdate, "the host to report "+newer+" and its update to end", func() bool {
		h := f.hostView(hostID)
		return h.Version == newer && h.Update != nil && h.Update.State != "requested"
	})
	if h := f.hostView(hostID); h.Update.State != "succeeded" || h.Update.AttemptID != attempt {
		t.Fatalf("the host runs %s and its update shows %+v, want attempt %s succeeded", h.Version, h.Update, attempt)
	}
	rec.note("host after the update", "runs "+newer+", attempt succeeded")

	// The same host, not a second one: an updated agent that joined afresh would
	// leave the first host's runners to be reaped.
	if after, want := sortedIDs(f.hostIDs()), sortedIDs([]string{hostID, bareID}); !slices.Equal(after, want) {
		t.Fatalf("the fleet has hosts %v after the update but had %v before; the updated agent must come back as the host it was", after, want)
	}
	// The same process, not a replacement that happens to carry the name.
	f.requireStillRunning(runner.Name, "the updated agent must keep the runner it found")
	if got := f.runnerPID(runner.Name); got != pid {
		t.Fatalf("the runner's pid is %d after the update, want %d: the job was restarted rather than kept", got, pid)
	}
	rec.note("runner after the update", "same pid "+strconv.Itoa(pid)+", still running")

	// The runner exits after its one job before GitHub's webhook says the job is
	// over, so the only thing that can notice is the updated agent watching the
	// runner it adopted when it started. In the other order the controller's
	// removal finds the workload by itself, and the drill passed with the
	// adoption taken out; without it, the runner sits untracked until the orphan
	// sweep removes it two minutes later, which on a longer job is the job.
	if err := finishJob(f.runnerDir(runner.Name)); err != nil {
		t.Fatalf("telling the stub runner its job is over: %v", err)
	}
	waitFor(t, waitRecovery, "the updated agent to report that the runner it adopted has exited", func() bool {
		r := f.runnerByID(runner.ID, poolID)
		return r.State == "removed" || r.State == "failed"
	})
	rec.note("runner after its job", f.runnerByID(runner.ID, poolID).State+", reported by the updated agent")
	f.gh.CompleteJob(job.ID, "success")
	f.deliverJob("completed", job, runner.Name, "success")
	waitFor(t, waitRecovery, "the workload to be gone from the host", func() bool {
		return len(f.liveWorkloads()) == 0
	})
	rec.recovered()
	if final := f.runnerByID(runner.ID, poolID); final.State != "removed" {
		t.Errorf("runner ended in state %q after a successful job, want removed", final.State)
	}

	// Nothing reached the host without the marker while the other was updated.
	if _, found, err := channel.PendingRequest(bareDir); err != nil || found {
		t.Errorf("the host without the helper's marker has a request in its update folder (found %v, error %v); no task should have been sent to it", found, err)
	}
	if h := f.hostView(bareID); h.Update == nil || h.Update.State != "none" {
		t.Errorf("the host without the helper's marker shows %+v, want no attempt", h.Update)
	}

	rec.pass("an agent updated from the fleet mid-job came back on the new release as the same host, kept the same runner process, and finished and cleaned up the job; a host without the helper's marker was refused and sent nothing")
}

// startAgentWithoutHelperMarker joins a second agent on the older release whose
// update folder is there and whose helper's marker is not. It is named because
// both agents are on this machine and would otherwise claim the same host row.
func (f *fleet) startAgentWithoutHelperMarker(name string) (hostID, updateDir string) {
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
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatalf("creating the work directory: %v", err)
	}
	updateDir = installUpdateFolder(t, dir)
	f.spawn(f.agentBin, "agent-without-helper", []string{"agent"}, append(baseEnv(dir),
		"ZOOMIES_CONTROLLER_URL="+f.baseURL,
		"ZOOMIES_JOIN_TOKEN="+token.Token,
		"ZOOMIES_AGENT_NAME="+name,
		"ZOOMIES_AGENT_BACKEND=process",
		"ZOOMIES_AGENT_CAPACITY=2",
		"ZOOMIES_WORK_DIR="+work,
		"ZOOMIES_AGENT_ALLOW_INSECURE_HTTP=true",
		"ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL=http://127.0.0.1:1/never",
	))
	waitFor(t, waitProcessUp, "the agent without the helper's marker to join and say which version it runs", func() bool {
		for _, h := range f.hostViews() {
			if h.Name == name && h.Version != "" && h.Update != nil {
				hostID = h.ID
				return true
			}
		}
		return false
	})
	return hostID, updateDir
}

// hostView is one host as the API renders it.
func (f *fleet) hostView(id string) hostView {
	f.t.Helper()
	var h hostView
	f.api.get("/hosts/"+id, &h)
	return h
}

// runnerPID is the process the backend recorded for a runner, which is how the
// drill tells the same runner from a new one with the same name.
func (f *fleet) runnerPID(name string) int {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.runnerDir(name), "runner.pid"))
	if errors.Is(err, fs.ErrNotExist) {
		f.t.Fatalf("runner %s has no pid file, so its process cannot be followed", name)
	} else if err != nil {
		f.t.Fatalf("reading runner %s's pid file: %v", name, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		f.t.Fatalf("runner %s's pid file holds %q, which is not a pid", name, raw)
	}
	return pid
}

func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
