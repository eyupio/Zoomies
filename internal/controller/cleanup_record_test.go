package controller

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

// The host and GitHub finish independently. One side succeeding must not
// erase the other side's failure, including when both failed first.
func TestHostCleanupDoesNotHideARegistrationThatStillExists(t *testing.T) {
	for _, hostFailsFirst := range []bool{false, true} {
		name := "registration failure only"
		if hostFailsFirst {
			name = "both sides failed"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			r := h.runnerRow(pool, host, store.RunnerIdle)
			if err := h.st.SetRunnerGitHubID(h.ctx, r.ID, 4242); err != nil {
				t.Fatal(err)
			}
			h.gh.SetError("/actions/runners/4242", 403, "Resource not accessible by integration")
			if _, err := h.c.removeRunner(h.ctx, h.runnerByID(t, r.ID), "scaling down", pool); err != nil {
				t.Fatal(err)
			}
			if hostFailsFirst {
				if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
					TaskID: "remove_failed", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
					OK: false, Error: "container is in use",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
				TaskID: "remove_succeeded", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
				OK: true, State: store.RunnerRemoved,
			}); err != nil {
				t.Fatal(err)
			}
			got := h.runnerByID(t, r.ID)
			if !strings.Contains(got.CleanupError, "registration") || got.CleanupFailedAt == nil {
				t.Fatalf("host success hid the registration failure: error=%q failed_at=%v", got.CleanupError, got.CleanupFailedAt)
			}
			if got.CleanedUpAt != nil {
				t.Fatal("cleanup was marked complete while GitHub still refuses deletion")
			}
			if !contains(h.problemCodes(), "runners.cleanup_failed") {
				t.Fatal("the unfinished registration cleanup disappeared from Problems")
			}
			h.gh.ClearErrors()
			h.c.deleteRegistration(h.ctx, got, pool)
			got = h.runnerByID(t, r.ID)
			if got.CleanupError != "" || got.CleanupFailedAt != nil {
				t.Fatalf("successful registration retry did not clear its own failure: %q", got.CleanupError)
			}
		})
	}
}

func TestRegistrationReapingDoesNotHideAFailedHostRemoval(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRemoved)
	h.gh.AddRunner(r.Name, pool.Labels)
	if err := h.st.RecordRegistrationCleanupFailure(h.ctx, r.ID, "registration deletion refused"); err != nil {
		t.Fatal(err)
	}
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "remove_failed", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: false, Error: "container is in use",
	}); err != nil {
		t.Fatal(err)
	}
	h.c.reap(h.ctx)
	if len(h.gh.Runners()) != 0 {
		t.Fatal("the reaper did not delete the registration")
	}
	got := h.runnerByID(t, r.ID)
	if !strings.Contains(got.CleanupError, "container is in use") || strings.Contains(got.CleanupError, "registration") {
		t.Fatalf("the reaper must clear only its own error: %q", got.CleanupError)
	}
	if got.CleanedUpAt != nil || !contains(h.problemCodes(), "runners.cleanup_failed") {
		t.Fatal("the reaper hid the host's unfinished cleanup")
	}
}

// A remove that fails on a runner the fleet has already finished with used to
// vanish.
//
// The result arrives for a terminal row, the state machine refuses to move a
// removed runner to failed -- correctly, it cannot -- and before this the
// refusal was logged at debug and that was the end of it. Meanwhile the
// container is still on the host, holding its writable layer and, on a
// docker-in-docker pool, a privileged sidecar. Nobody was told.
func TestARemoveThatFailsOnATerminalRunnerIsRecorded(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "removed"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}

	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_1", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: false, Error: "the daemon refused: container is in use",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	got := h.runnerByID(t, r.ID)
	if got.CleanupError == "" {
		t.Fatal("the failed remove left no record on the row; the container is on the host and nobody is told")
	}
	if !strings.Contains(got.CleanupError, "container is in use") {
		t.Errorf("cleanup_error = %q, want it to carry what the agent said", got.CleanupError)
	}
	if got.CleanupAttempts != 1 {
		t.Errorf("cleanup_attempts = %d, want 1", got.CleanupAttempts)
	}
	if got.CleanupFailedAt == nil {
		t.Error("cleanup_failed_at is unset, so the problem has no age")
	}
	// The row must not be dragged back through the state machine: its slot is
	// free and the scheduler has moved on.
	if got.State != store.RunnerRemoved {
		t.Errorf("state = %q, want it to stay removed: recording a tidying problem must not change capacity", got.State)
	}
}

// And it must be somewhere an operator looks, not only in a column.
func TestARunnerThatWillNotCleanUpRaisesAProblem(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "removed"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}
	if contains(h.problemCodes(), "runners.cleanup_failed") {
		t.Fatalf("problems = %v; a runner that cleaned up fine is not a problem", h.problemCodes())
	}

	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_1", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: false, Error: "the daemon refused",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if !contains(h.problemCodes(), "runners.cleanup_failed") {
		t.Fatalf("problems = %v, want runners.cleanup_failed", h.problemCodes())
	}
}

// A retry that works clears the complaint, and closes the runner's life.
// Otherwise the problems panel would keep naming a runner that is long gone,
// and an operator would learn to ignore it.
func TestACleanupThatEventuallyWorksClearsTheRecord(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerDraining, "draining"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}
	if err := h.st.RecordCleanupFailure(h.ctx, r.ID, "remove_runner failed: the daemon refused"); err != nil {
		t.Fatalf("RecordCleanupFailure: %v", err)
	}

	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_2", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: true, State: store.RunnerRemoved,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	before := h.runnerByID(t, r.ID)
	if before.HostRemovedAt == nil {
		t.Fatal("host cleanup was not durable before ACK")
	}
	// Remote registration deletion now runs after ingestion has acknowledged.
	h.c.enrichOnce(h.ctx)

	got := h.runnerByID(t, r.ID)
	if got.CleanupError != "" {
		t.Errorf("cleanup_error = %q, want it cleared once the removal worked", got.CleanupError)
	}
	if got.CleanedUpAt == nil {
		t.Error("cleaned_up_at is unset; the cleanup interval never closed")
	}
	// The count survives. How many tries it took is the difference between a
	// blip and a host worth looking at.
	if got.CleanupAttempts != 1 {
		t.Errorf("cleanup_attempts = %d, want the history kept", got.CleanupAttempts)
	}
	if contains(h.problemCodes(), "runners.cleanup_failed") {
		t.Fatalf("problems = %v; the problem should clear with the record", h.problemCodes())
	}
}

// A log task that fails says nothing about cleanup: the container is where it
// was, doing what it was doing. Recording it would send an operator looking for
// litter that is not there.
//
// The runner is terminal on purpose. That is the case where the guard is
// load-bearing -- on a live runner the failure would be an ordinary transition
// to failed and never reach the cleanup record at all.
func TestAFailedLogTaskIsNotACleanupFailure(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if _, err := h.st.TransitionRunner(h.ctx, r.ID, store.RunnerRemoved, "removed"); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}

	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_3", Kind: agent.TaskStreamLogs, RunnerID: r.ID,
		OK: false, Error: "the viewer went away",
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if got := h.runnerByID(t, r.ID); got.CleanupError != "" {
		t.Errorf("cleanup_error = %q after a failed log relay, want none", got.CleanupError)
	}
}

// A registration GitHub will not delete is the other way something is left
// behind, and the one the roadmap named: it fills an organisation's runner
// list with dead entries. Before this it was a log line and a counter, so an
// operator's first sign of it was the list itself.
func TestARegistrationThatWillNotDeleteIsRecordedOnTheRunner(t *testing.T) {
	h := newHarness(t)
	inst, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if err := h.st.SetRunnerGitHubID(h.ctx, r.ID, 4242); err != nil {
		t.Fatalf("SetRunnerGitHubID: %v", err)
	}
	h.gh.AddRunner(r.Name, pool.Labels)
	// The fake matches on the path alone, so the runner's own id is what makes
	// this refuse the delete and nothing else.
	h.gh.SetError("/actions/runners/4242", 403, "Resource not accessible by integration")

	fresh := h.runnerByID(t, r.ID)
	if _, err := h.c.removeRunner(h.ctx, fresh, "scaling down", pool); err != nil {
		t.Fatalf("removeRunner: %v", err)
	}

	got := h.runnerByID(t, r.ID)
	if got.CleanupError == "" {
		t.Fatal("a refused registration delete left no record; the ghost is invisible until somebody reads the runner list")
	}
	if !strings.Contains(got.CleanupError, "registration") {
		t.Errorf("cleanup_error = %q, want it to name the registration", got.CleanupError)
	}
	if got.RegistrationDeletedAt != nil {
		t.Error("registration_deleted_at is set for a deletion that was refused")
	}
	if !contains(h.problemCodes(), "runners.cleanup_failed") {
		t.Fatalf("problems = %v, want runners.cleanup_failed", h.problemCodes())
	}
	_ = inst
}

// GitHub's own bookkeeping can still call a runner busy for a few seconds
// after the same completion webhook that told Zoomies the job was done, and
// the immediate delete Zoomies fires off the back of that webhook can lose
// the race. That must not read like a stuck job or a lost App permission --
// it is neither, and the operator-facing text used to send whoever read it
// to check permissions for a timing race that clears itself.
func TestARegistrationRefusedForBeingBusyIsNotReadAsAPermissionProblem(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRemoved)
	h.gh.AddRunner(r.Name, pool.Labels)
	h.gh.SetRunnerBusy(r.Name, true)

	fresh := h.runnerByID(t, r.ID)
	h.c.deleteRegistration(h.ctx, fresh, pool)

	got := h.runnerByID(t, r.ID)
	if got.CleanupError == "" {
		t.Fatal("a busy-refused delete left no record")
	}
	if strings.Contains(got.CleanupError, "registration") {
		t.Errorf("cleanup_error = %q, must not read like a genuinely orphaned registration", got.CleanupError)
	}
	if !strings.Contains(got.CleanupError, "still reports") || !strings.Contains(got.CleanupError, "running a job") {
		t.Errorf("cleanup_error = %q, want it to say GitHub still reports the runner busy", got.CleanupError)
	}
	if len(h.gh.Runners()) != 1 {
		t.Fatal("a runner GitHub explicitly refused to delete must stay registered")
	}

	prob := h.problem(t, "runners.cleanup_failed")
	if strings.Contains(prob.Fix, "permission the App has lost") {
		t.Errorf("fix = %q, must not send an operator chasing a permission over a timing race", prob.Fix)
	}
	if !strings.Contains(prob.Fix, "ten minutes") {
		t.Errorf("fix = %q, want it to say Zoomies rechecks this automatically", prob.Fix)
	}
}

// And the ordinary case: a deletion that worked is stamped, so an unstamped
// terminal runner is exactly the set worth asking about.
func TestADeletedRegistrationIsStamped(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerIdle)
	if err := h.st.SetRunnerGitHubID(h.ctx, r.ID, 4243); err != nil {
		t.Fatalf("SetRunnerGitHubID: %v", err)
	}
	h.gh.AddRunner(r.Name, pool.Labels)

	fresh := h.runnerByID(t, r.ID)
	if _, err := h.c.removeRunner(h.ctx, fresh, "scaling down", pool); err != nil {
		t.Fatalf("removeRunner: %v", err)
	}

	got := h.runnerByID(t, r.ID)
	if got.RegistrationDeletedAt == nil {
		t.Fatal("registration_deleted_at is unset after a successful delete")
	}
	if got.CleanupError != "" {
		t.Errorf("cleanup_error = %q after a clean removal, want none", got.CleanupError)
	}
	if got.CleanedUpAt != nil {
		t.Error("registration deletion marked cleanup complete before the host confirmed removal")
	}
}

// A reap retry that fails again must not go quiet. Before this fix only the
// very first failure -- deleteRegistration's own immediate attempt -- was
// ever recorded; every retry after that only logged, so cleanup_attempts and
// cleanup_failed_at froze at that first failure and the problems panel kept
// reading "after 1 attempt" no matter how long the reap loop had actually
// been retrying behind it.
func TestReapRecordsARetryThatFailsAgain(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRemoved)
	h.gh.AddRunner(r.Name, pool.Labels)
	if err := h.st.RecordRegistrationCleanupFailure(h.ctx, r.ID,
		"the GitHub runner registration could not be deleted: an earlier refusal"); err != nil {
		t.Fatal(err)
	}
	first := h.runnerByID(t, r.ID)

	h.gh.SetMethodError(http.MethodDelete, "/actions/runners/", http.StatusForbidden, "Resource not accessible by integration")
	h.c.reap(h.ctx)

	got := h.runnerByID(t, r.ID)
	if got.CleanupAttempts != first.CleanupAttempts+1 {
		t.Errorf("cleanup_attempts = %d, want %d after the reap loop's own retry failed too",
			got.CleanupAttempts, first.CleanupAttempts+1)
	}
	if got.CleanupFailedAt == nil || !got.CleanupFailedAt.After(*first.CleanupFailedAt) {
		t.Errorf("cleanup_failed_at did not move, so the panel still reads as the first failure: %v -> %v",
			first.CleanupFailedAt, got.CleanupFailedAt)
	}
	if len(h.gh.Runners()) != 1 {
		t.Fatal("a genuinely refused delete must leave the registration in place")
	}
}

// A host that finds the daemon still carrying out a removal says so, and that
// is not a cleanup that failed: the daemon finishes it, and the agent confirms
// the removal once it has. Counting each such answer as a failure -- and the
// controller re-sends a remove on every pass while a host's cleanup is
// outstanding -- put a runner that was merely slow to delete on the Runners
// page as something left behind, with an attempt count that kept climbing.
func TestARemovalTheDaemonIsStillCarryingOutIsNotAFailedCleanup(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRemoved)

	for i := range 3 {
		if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
			TaskID: fmt.Sprintf("task_%d", i), Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
			OK: false, CleanupPending: true,
			Error: "the docker backend is still removing runner " + r.ID + ": 409: removal of container " + r.Name + "-dind is already in progress",
		}); err != nil {
			t.Fatalf("ReportResult: %v", err)
		}
	}
	got := h.runnerByID(t, r.ID)
	if got.CleanupError != "" || got.CleanupAttempts != 0 || got.CleanupFailedAt != nil {
		t.Fatalf("a removal still under way was recorded as a failure: error %q, attempts %d", got.CleanupError, got.CleanupAttempts)
	}
	if contains(h.problemCodes(), "runners.cleanup_failed") {
		t.Fatalf("problems = %v; a removal still under way is not something left behind", h.problemCodes())
	}
	if got.State != store.RunnerRemoved {
		t.Fatalf("state = %q, want it left alone", got.State)
	}

	// And the removal is confirmed by the answer that finds it done.
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_done", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: true, State: store.RunnerRemoved,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if h.runnerByID(t, r.ID).HostRemovedAt == nil {
		t.Fatal("the finished removal was not confirmed")
	}
}

// Once the host has confirmed a runner's workload gone, a failure that arrives
// afterwards is about an attempt the confirmation overtook -- a report built
// before it, a redelivered task -- and must not re-open the cleanup.
func TestAFailureArrivingAfterTheHostConfirmedRemovalDoesNotReopenIt(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.runnerRow(pool, host, store.RunnerRemoved)
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_1", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: true, State: store.RunnerRemoved,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	late := "backend: removing docker-in-docker sidecar: docker api: DELETE /containers/x-dind: 409: removal of container x-dind is already in progress"
	if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{RunnerID: r.ID, State: store.RunnerRemoved, CleanupError: late}}); err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: "task_2", Kind: agent.TaskRemoveRunner, RunnerID: r.ID,
		OK: false, Error: late,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	got := h.runnerByID(t, r.ID)
	if got.HostRemovedAt == nil || got.HostCleanupError != "" || got.CleanupAttempts != 0 {
		t.Fatalf("a late failure re-opened a confirmed removal: host_removed_at %v, host error %q, attempts %d",
			got.HostRemovedAt, got.HostCleanupError, got.CleanupAttempts)
	}
}

// A row can carry both halves at once, and the container is the one that
// needs somebody. The fix used to be chosen by searching the joined sentence
// for "running a job", so a busy registration was all an operator was told
// about while a container sat on their host.
func TestAFixForBothHalvesNamesTheContainerFirst(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		want       []string
	}{
		{
			name: "a removal Docker still has under way",
			host: "remove_runner failed: docker api: DELETE /containers/x-dind: 409: removal of container x-dind is already in progress",
			want: []string{"still removing", "restarting the daemon"},
		},
		{
			name: "a container the daemon would not remove",
			host: "remove_runner failed: the daemon refused: container is in use",
			want: []string{"If the container is still on", "remove it there"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			r := h.runnerRow(pool, host, store.RunnerRemoved)
			if err := h.st.RecordCleanupFailure(h.ctx, r.ID, tc.host); err != nil {
				t.Fatal(err)
			}
			h.c.deferBusyRegistration(h.ctx, r)

			prob := h.problem(t, "runners.cleanup_failed")
			for _, w := range tc.want {
				if !strings.Contains(prob.Fix, w) {
					t.Errorf("fix = %q, want it to say %q", prob.Fix, w)
				}
			}
			if !strings.Contains(prob.Fix, host.Name) {
				t.Errorf("fix = %q, want it to name the host the container is on", prob.Fix)
			}
			if strings.HasPrefix(prob.Fix, "GitHub still reports") {
				t.Errorf("fix = %q, leads with GitHub while a container is left on the host", prob.Fix)
			}
			if !strings.Contains(prob.Fix, "GitHub's side is rechecked") {
				t.Errorf("fix = %q, want it to say the registration looks after itself", prob.Fix)
			}
		})
	}
}
