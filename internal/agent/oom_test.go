package agent

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// An out-of-memory kill is the fleet's failure however the runner ended, and
// the controller can only put it on the job if the agent says so: exit 137,
// the daemon's OOMKilled on any other code, and OOMKilled on a clean exit --
// the step killed under a runner that went on to finish the job.
func TestAnOutOfMemoryKillIsClassifiedWhateverTheRunnerDidNext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    backend.Status
		stopping  bool
		wantState store.RunnerState
		wantFault store.FaultKind
	}{
		{"exit 137", backend.Status{Phase: backend.PhaseFailed, ExitCode: 137}, false, store.RunnerFailed, store.FaultOutOfMemory},
		// The daemon was asked and said no: docker stop after its grace period, an operator, or
		// a maintenance restart's --kill-running. Not the job's memory, and not evidence.
		{"exit 137 the daemon says was not memory", backend.Status{Phase: backend.PhaseFailed, ExitCode: 137, OOMReported: true}, false, store.RunnerFailed, store.FaultRunnerExited},
		{"exit 137 the daemon says was memory", backend.Status{Phase: backend.PhaseFailed, ExitCode: 137, OOMReported: true, OOMKilled: true}, false, store.RunnerFailed, store.FaultOutOfMemory},
		{"killed with another code", backend.Status{Phase: backend.PhaseFailed, ExitCode: 1, OOMKilled: true}, false, store.RunnerFailed, store.FaultOutOfMemory},
		{"a step killed under a runner that finished", backend.Status{Phase: backend.PhaseFailed, OOMKilled: true}, false, store.RunnerRemoved, store.FaultOutOfMemory},
		{"a clean exit is no fault", backend.Status{Phase: backend.PhaseExited}, false, store.RunnerRemoved, ""},
		{"an ordinary failure is not memory", backend.Status{Phase: backend.PhaseFailed, ExitCode: 1}, false, store.RunnerFailed, store.FaultRunnerExited},
		// A kill Zoomies asked for is not the job's, and not memory.
		{"a stop is not a kill", backend.Status{Phase: backend.PhaseFailed, ExitCode: 137}, true, store.RunnerRemoved, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, msg, fault := terminalOutcome(tracked{ephemeral: true, stopping: tc.stopping}, tc.status)
			if state != tc.wantState || fault != tc.wantFault {
				t.Fatalf("terminalOutcome = %s, %q (%s); want %s, %q", state, fault, msg, tc.wantState, tc.wantFault)
			}
		})
	}
}

// A build killed in the pool's Docker sidecar leaves a runner that finishes its
// job, and the sentence the controller files on the job has to name the
// container that was short of room: saying "a process in it" sends an operator
// to the runner's limit when it was the daemon's half that ran out.
func TestAKillInTheSidecarSaysSoWhenTheRunnerFinishedItsJob(t *testing.T) {
	status := backend.Status{
		Phase: backend.PhaseFailed, OOMKilled: true, SidecarOOMKilled: true,
		Message: "the Docker sidecar was killed for exceeding its memory limit; raise the pool's memory_mb",
	}
	state, msg, fault := terminalOutcome(tracked{ephemeral: true}, status)
	if state != store.RunnerRemoved || fault != store.FaultOutOfMemory {
		t.Fatalf("terminalOutcome = %s, %q (%s); want a clean end with an out-of-memory fault", state, fault, msg)
	}
	if want := "runner exited after its job, but the Docker sidecar was killed for exceeding its memory limit"; !strings.HasPrefix(msg, want) {
		t.Fatalf("message = %q, want it to begin %q", msg, want)
	}
	// The runner's own kill keeps the sentence it always had.
	_, own, _ := terminalOutcome(tracked{ephemeral: true}, backend.Status{Phase: backend.PhaseFailed, OOMKilled: true})
	if strings.Contains(own, "sidecar") {
		t.Fatalf("a kill in the runner itself named the sidecar: %q", own)
	}
}
