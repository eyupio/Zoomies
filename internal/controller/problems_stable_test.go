package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/store"
)

// problemJSON is a problem as the event stream would send it.
func problemJSON(t *testing.T, h *harness, code string) string {
	t.Helper()
	b, err := json.Marshal(findProblem(t, h, code))
	if err != nil {
		t.Fatalf("marshalling %s: %v", code, err)
	}
	return string(b)
}

// The problems list is sent to every open tab only when its JSON differs from
// the last one sent. A problem that carries a count of elapsed seconds, or a
// "since" computed from the clock, differs on every pass for as long as the
// fault stands, so a fleet with one silent host pushed the whole list to every
// subscriber once a second -- and told the operator a warning had been there
// "for an hour" for ever. Each of these is a fault that stands while time
// passes, and time passing must not change what is said about it.
func TestAProblemThatStandsDoesNotChangeWhileTimePasses(t *testing.T) {
	const later = 90 * time.Second

	tests := []struct {
		name  string
		code  string
		setup func(t *testing.T, h *harness) (advance func(time.Duration))
	}{
		{"rejected webhook deliveries", "webhook.rejected", func(t *testing.T, h *harness) func(time.Duration) {
			h.fleet()
			h.deliver("workflow_job", jobEvent{Action: "queued", JobID: 1}.body(), "wrong")
			return h.advance
		}},
		{"a silent host with nothing on it", "host.unhealthy", func(t *testing.T, h *harness) func(time.Duration) {
			_, _, host := h.fleet()
			host.LastHeartbeat = time.Now().Add(-2 * store.HeartbeatTimeout)
			if err := h.st.UpdateHost(h.ctx, host); err != nil {
				t.Fatalf("UpdateHost: %v", err)
			}
			return h.advance
		}},
		{"a silent host with runners on it", "host.unhealthy", func(t *testing.T, h *harness) func(time.Duration) {
			_, pool, host := h.fleet()
			h.runnerRow(pool, host, store.RunnerIdle)
			host.LastHeartbeat = time.Now().Add(-2 * store.HeartbeatTimeout)
			if err := h.st.UpdateHost(h.ctx, host); err != nil {
				t.Fatalf("UpdateHost: %v", err)
			}
			return h.advance
		}},
		{"a queued job no pool claims", "jobs.unmatched", func(t *testing.T, h *harness) func(time.Duration) {
			h.fleet()
			h.deliverJob(jobEvent{
				Action: "queued", JobID: 909,
				Labels:   []string{"self-hosted", "linux", "gpu", "cuda12"},
				QueuedAt: time.Now().Add(-unmatchedGrace - time.Minute),
			})
			return h.advance
		}},
		{"a runner stuck starting up", "runners.not_progressing", func(t *testing.T, h *harness) func(time.Duration) {
			_, pool, host := h.fleet()
			r := h.runnerRow(pool, host, store.RunnerProvisioning)
			at := h.stuck(r.CreatedAt)
			h.c.clock = func() time.Time { return at }
			return func(d time.Duration) { at = at.Add(d) }
		}},
		{"a poller that has stopped sweeping", "poller.stale", func(t *testing.T, h *harness) func(time.Duration) {
			h.cfg.GitHub.PollFallback = true
			h.cfg.GitHub.PollInterval = 30 * time.Second
			h.c.lastPollAt.Store(h.c.Now().UnixNano())
			h.advance(3 * time.Minute)
			return h.advance
		}},
		{"a delete the provider has not confirmed", "provider.delete_pending", func(t *testing.T, h *harness) func(time.Duration) {
			_, row := h.machineFleet(t)
			m := h.readyMachine(t, row)
			h.beginDrainFor(t, m)
			h.fake.SetFailure("delete", provider.FailureUnreachable, "dial tcp: i/o timeout")
			h.machinePass(t)
			h.advance(h.cfg.Provider.DeleteTimeout + time.Minute)
			return h.advance
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			advance := tc.setup(t, h)
			before := problemJSON(t, h, tc.code)
			advance(later)
			if after := problemJSON(t, h, tc.code); after != before {
				t.Errorf("%s changed as time passed:\n before: %s\n  after: %s", tc.code, before, after)
			}
		})
	}
}
