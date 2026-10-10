package drill

import "time"

// The drills' waits.
//
// These are much shorter than the end-to-end harness's, and for a good reason:
// nothing here crosses a network or pulls an image. GitHub is a fake in this
// process, the runner is a stub already on disk, and the two Zoomies binaries
// are starting locally. A drill that needs minutes is a drill that has hung.
//
// The file carries no build tag so budget_test.go can check these against the
// Makefile's timeout in ordinary CI, the same way the e2e harness does.
const (
	waitProcessUp     = 30 * time.Second
	waitRunnerCreated = 60 * time.Second
	waitWorkloadUp    = 60 * time.Second
	waitJobDone       = 60 * time.Second
	waitRunnerGone    = 60 * time.Second
	// waitPromptCleanup is how long the removal path itself gets to delete a
	// GitHub registration. It is deliberately far shorter than the rest: the
	// reaper's first sweep is a minute after the controller starts, and an
	// assertion looser than that would be satisfied by the backstop instead of
	// by the code under test.
	waitPromptCleanup = 15 * time.Second
	// waitStaysUp is how long a fault drill watches a runner it expects to have
	// survived. Short, because it is watching for a death that has already been
	// set in motion rather than for one that might happen later.
	waitStaysUp = 3 * time.Second
	// waitRecovery is what a fault drill allows for the fleet to come back
	// after something is killed. It is the longest wait here because a
	// restarted controller has to re-derive its task queue from the rows.
	waitRecovery = 90 * time.Second
	// waitBuild bounds building one release-stamped binary for an update drill.
	// Every package is already in the build cache from the binary make built, so
	// this is a link of a few seconds, or a recompile of what changed since; a
	// build that needs a minute is fetching modules, which a drill should fail on
	// by name rather than wait out.
	waitBuild = time.Minute
	// waitUpdate is how long an update drill gives each step that crosses the
	// agent's task poll or its first heartbeat: the request reaching the update
	// folder, and the host reporting the release it was taken to. Both happen at
	// once on a healthy fleet, because the poll is held open and an agent beats as
	// it starts. Thirty seconds is one heartbeat interval, so a step that had to
	// wait for the next beat still fits.
	waitUpdate = 30 * time.Second
)

// everyWaitOnce is a drill that meets each of the waits above one time.
const everyWaitOnce = waitProcessUp + waitRunnerCreated + waitWorkloadUp +
	waitJobDone + waitRunnerGone + waitPromptCleanup + waitRecovery + waitStaysUp +
	waitBuild + waitUpdate

// updateDrillWaits is the update drill counted as it runs, which meets some
// waits more than once: two release builds; the controller, its agent and the
// agent without the helper's marker each starting; the runner, its workload and
// the job; the agent offering to update, writing the request and reporting the
// release; and the fleet seeing the adopted runner exit and its workload go.
const updateDrillWaits = 2*waitBuild + 3*waitProcessUp +
	waitRunnerCreated + waitWorkloadUp + waitJobDone +
	3*waitUpdate + 2*waitRecovery

// rolloutDrillWaits is the rollout drill counted the same way: two release
// builds; the controller starting three times and two agents joining; a runner,
// its workload and its job on each host; the hosts reporting their release, the
// rollout asking each host, each agent writing its request and each host
// reporting the new release, and the rollout finishing; each runner watched
// through its agent's two stops and once at the end; and the workloads going.
const rolloutDrillWaits = 2*waitBuild + 5*waitProcessUp +
	2*(waitRunnerCreated+waitWorkloadUp+waitJobDone) +
	8*waitUpdate + 6*waitStaysUp + waitRecovery

// drillBudget is the longest one drill may legitimately take.
const drillBudget = max(everyWaitOnce, updateDrillWaits, rolloutDrillWaits)
