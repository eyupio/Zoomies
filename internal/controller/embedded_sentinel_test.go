package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
)

// A heartbeat for a host this controller has never seen must carry
// agent.ErrHostGone. The HTTP transport derives that sentinel from a 404, but
// the embedded agent has no status code to derive it from, so if the controller
// does not wrap it the in-process agent retries a host that will never exist
// again instead of stopping and telling the operator to re-join.
func TestHeartbeatForUnknownHostIsErrHostGone(t *testing.T) {
	h := newHarness(t)

	_, err := h.c.Heartbeat(context.Background(), "host_doesnotexist", agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Capacity:        1,
	})
	if err == nil {
		t.Fatal("heartbeat for an unknown host succeeded; it must fail")
	}
	if !errors.Is(err, agent.ErrHostGone) {
		t.Fatalf("error = %v, want it to wrap agent.ErrHostGone so the embedded agent stops rather than retrying", err)
	}
}
