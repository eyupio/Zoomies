package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// waitForLogTail is waitForStreamTask for a test that cares what backlog the
// agent was told to send, which is what makes a download of a huge log cheap.
func waitForLogTail(t *testing.T, h *harness, agentToken string) (streamID string, tail int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/agent/tasks?wait=1", token: agentToken})
		if resp.status != http.StatusOK {
			t.Fatalf("agent task poll: %d %s", resp.status, resp.body)
		}
		var batch struct {
			Tasks []struct {
				Kind       string `json:"kind"`
				StreamID   string `json:"stream_id"`
				LogOptions struct {
					Tail int
				} `json:"log_options"`
			} `json:"tasks"`
		}
		resp.into(t, &batch)
		for _, task := range batch.Tasks {
			if task.Kind == "stream_logs" && task.StreamID != "" {
				return task.StreamID, task.LogOptions.Tail
			}
		}
	}
	t.Fatal("no stream_logs task was queued within the deadline")
	return "", 0
}

// A download used to be the whole log from its first byte. A caller that wants
// the end of a long build -- an MCP tool diagnosing a failure -- had to read
// everything and keep whatever fitted, which past a size limit is the wrong
// end. The route now takes tail, and the agent is told to send only that.
func TestDownloadingRunnerLogsPassesTheRequestedTailToTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"no tail is the whole log, as it always was", "", 0},
		{"a tail is the last lines only", "?tail=50", 50},
		{"a negative tail is the whole log rather than an error", "?tail=-3", 0},
		{"something that is not a number is the whole log", "?tail=lots", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			inst := h.installation()
			pool := h.pool(inst, "linux-x64")
			hostID, agentToken := h.agentToken("vm-1")
			host, err := h.st.GetHost(h.ctx, hostID)
			if err != nil {
				t.Fatalf("GetHost: %v", err)
			}
			run := h.runner(pool, host, store.RunnerBusy)
			u, _ := h.user("viewer", store.RoleViewer)

			done := make(chan *response, 1)
			go func() {
				done <- h.do(request{method: http.MethodGet,
					path: "/api/v1/runners/" + run.ID + "/logs/download" + tc.query, cookie: h.session(u)})
			}()

			streamID, tail := waitForLogTail(t, h, agentToken)
			if tail != tc.want {
				t.Errorf("the agent was told to send tail=%d, want %d", tail, tc.want)
			}
			h.do(request{method: http.MethodPost, path: "/api/v1/agent/logs/" + streamID,
				token: agentToken, headers: map[string]string{"Content-Type": "application/octet-stream"},
				rawBody: "the last line\n"})
			if resp := <-done; resp.status != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.status, resp.body)
			}
		})
	}
}
