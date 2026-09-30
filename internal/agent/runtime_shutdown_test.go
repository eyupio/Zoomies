package agent

import (
	"strings"
	"testing"
	"time"
)

// A create waiting out the container runtime's cooldown has not touched its
// runner, so an agent restarted at that moment must hand it back. Reporting a
// failure instead charges the pool a backend start failure, fails a runner
// nothing happened to, and tells the operator to look at the runtime when the
// cause was an upgrade.
func TestACreateWaitingOnTheRuntimeCooldownIsHandedBackOnShutdown(t *testing.T) {
	h := newHarness(t, 1)
	h.agent.mu.Lock()
	h.agent.runtimeRetryAt = h.clock.Now().Add(time.Hour)
	h.agent.mu.Unlock()

	h.tr.tasks <- []Task{createTask("task-cooling", "runner-1")}
	// Long enough for the create to have been admitted and be sleeping on the
	// cooldown rather than still queued.
	time.Sleep(200 * time.Millisecond)
	go h.stop()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case res := <-h.tr.results:
			if res.TaskID != "task-cooling" {
				continue
			}
			if res.OK || !res.NotStarted || !strings.Contains(res.Error, "safe to redeliver") {
				t.Fatalf("result = %+v, want the waiting create reported as redeliverable", res)
			}
			if res.State != "" || res.Fault != "" {
				t.Fatalf("state = %q, fault = %q, want neither: the runner was never touched", res.State, res.Fault)
			}
			return
		case <-deadline:
			t.Fatal("the create that never started was never reported")
		}
	}
}
