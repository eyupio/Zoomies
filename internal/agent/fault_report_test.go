package agent

import (
	"context"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The one-shot report is not the only way the controller learns a runner has
// failed: when it is not delivered, the heartbeat's copy of the runner is the
// retry, and delivering it marks the report as sent. If that copy dropped the
// classification the controller would file an OOM kill or a refused
// configuration as a generic "runner exited", and nothing could correct it.
func TestAFailedRunnersHeartbeatCopyKeepsItsFaultClassification(t *testing.T) {
	tests := []struct {
		name string
		code int
		want store.FaultKind
	}{
		{"an out-of-memory kill", 137, store.FaultOutOfMemory},
		{"a runner that could not register", 64, store.FaultRegistration},
		{"a daemon that never became ready", 69, store.FaultBackend},
		{"a refused configuration", 78, store.FaultConfig},
		{"an exit nobody has a meaning for", 1, store.FaultRunnerExited},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, be, _ := newAgent(t, 2)
			track(a, "runner-1", "wl-1", true)
			be.setWorkloads(exited("wl-1", "runner-1", tc.code))
			reports, err := a.ReconcileOnce(context.Background())
			if err != nil || len(reports) != 1 || reports[0].Fault != tc.want {
				t.Fatalf("observe report = %+v, %v; want fault %q", reports, err, tc.want)
			}

			// The report is never delivered, so the heartbeat is all the
			// controller will ever hear.
			beat := a.Runners()
			if len(beat) != 1 || beat[0].State != store.RunnerFailed {
				t.Fatalf("heartbeat copy = %+v", beat)
			}
			if beat[0].Fault != tc.want {
				t.Fatalf("heartbeat copy fault = %q, want %q", beat[0].Fault, tc.want)
			}
		})
	}
}
