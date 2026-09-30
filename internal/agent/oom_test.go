package agent

import (
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
