//go:build drill

package drill

import "testing"

// The first fault drill: the controller is killed in the middle of a job.
//
// A control plane is allowed to die. A job is not. The runner is a process on
// somebody's machine doing somebody's work, and the only thing that should
// follow from the controller going away is that nothing new gets decided until
// it comes back -- not that the work is torn down, not that the fleet forgets
// it, and not that the agent gives up and has to be re-joined by hand.
//
// Every layer of this is already tested in process: the agent's poll loop
// backs off and retries, the task queue is re-derived from the rows on start,
// and adoption reattaches what is already running. What no in-process test can
// show is the three of them surviving the same event, with a real process
// holding a real job while the thing that decided to start it is not there.
func TestKillingTheControllerMidJobLeavesTheWorkRunning(t *testing.T) {
	f := newFleet(t)
	rec := newRecord(t, "controller-restart")
	defer rec.write()

	label := "drill-controller-restart"
	poolID := f.createPool("drill-restart", label)
	job := f.gh.AddQueuedJob("acme/api", "CI", "build", []string{"self-hosted", label})

	var runner runnerView
	waitFor(t, waitRunnerCreated, "a runner to be created for the pool", func() bool {
		for _, r := range f.runners(poolID) {
			runner = r
			return true
		}
		return false
	})
	// The stub can publish its started marker before the backend has written
	// runner.pid. Wait for that durable record too: the survival check below
	// reads it, and without it a live process looks like a dead one.
	waitFor(t, waitWorkloadUp, "a workload and its persisted PID to appear on the host", func() bool {
		return len(f.liveWorkloads()) == 1 && f.workloadRunning(runner.Name)
	})
	rec.note("workload on host", f.liveWorkloads()[0])

	f.gh.StartJob(job.ID, runner.Name)
	f.deliverJob("in_progress", job, runner.Name, "")
	waitFor(t, waitJobDone, "the fleet to see the job start", func() bool {
		for _, r := range f.runners(poolID) {
			if r.ID == runner.ID && (r.State == "busy" || r.State == "draining") {
				return true
			}
		}
		return false
	})
	rec.note("job in progress on", runner.Name)

	// The fault. kill() waits for the process to be gone, so whatever the
	// controller does on its way out has already happened by the next line.
	rec.faultInjected("killed the controller while the job was running")
	f.controller.kill()

	f.requireStillRunning(runner.Name, "a control plane that takes the work down with it is worse than one that simply stops")
	rec.note("workload after the controller died", "still running")

	// The same address and the same database: this is a restart, not a new
	// fleet. The agent is not told; it has to notice on its own.
	f.startControllerOn(f.stateDir)
	survived := f.runnerByID(runner.ID, poolID)
	if survived.State == "failed" || survived.State == "removed" {
		t.Fatalf("the runner came back from the restart in state %q, but its job is still running on this host; the outage was the controller's, not the runner's", survived.State)
	}
	rec.note("runner state after the restart", survived.State)

	// The job finishes while the new controller is the one watching. Nothing
	// re-joined the agent and nothing re-created the runner: if the fleet
	// cleans this up, the agent found its way back on its own.
	f.gh.CompleteJob(job.ID, "success")
	f.deliverJob("completed", job, runner.Name, "success")
	if err := finishJob(f.runnerDir(runner.Name)); err != nil {
		t.Fatalf("telling the stub runner its job is over: %v", err)
	}

	waitFor(t, waitRecovery, "the runner to reach a terminal state under the new controller", func() bool {
		for _, r := range f.runners(poolID) {
			if r.ID == runner.ID {
				return r.State == "removed" || r.State == "failed"
			}
		}
		return false
	})
	final := f.runnerByID(runner.ID, poolID)
	if final.State != "removed" {
		t.Errorf("runner ended in state %q, want removed", final.State)
	}
	waitFor(t, waitRecovery, "the workload to be gone from the host", func() bool {
		return len(f.liveWorkloads()) == 0
	})
	// Recovery is measured to here rather than to the controller answering
	// again: a fleet is not recovered when its API is up, it is recovered when
	// work is moving through it.
	rec.recovered()
	rec.note("host workloads after the restart finished the job", "0")

	// The usage ledger: the runner's session is written once, when its
	// cleanup is confirmed, and a second restart -- with an agent that may
	// report its results again -- must not write another. Two would double
	// this runner's hours in every report that covers them.
	waitFor(t, waitPromptCleanup, "the runner's usage session to be written", func() bool {
		return f.runnerSessions(runner.ID) == 1
	})
	f.controller.kill()
	f.startControllerOn(f.stateDir)
	f.runnerByID(runner.ID, poolID)
	if n := f.runnerSessions(runner.ID); n != 1 {
		t.Errorf("the runner has %d usage sessions after a second restart, want exactly 1", n)
	}
	rec.note("usage sessions for the runner after a second restart", "1")
	rec.pass("a controller killed mid-job left the work running, and the fleet finished and cleaned it up after the restart")
}
