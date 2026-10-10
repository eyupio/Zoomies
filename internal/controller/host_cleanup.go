package controller

import (
	"context"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
)

// hostCleanupGrace is how long a finished runner that ran no job may go
// without its host confirming it gone before the host is asked to remove it.
// It outlasts the agent's own removal window (removalSettle, ten minutes) so
// that a container the agent is still keeping for its logs is not taken from
// under it, and is short enough that a lost report costs a transfer a quarter
// of an hour rather than for ever.
const hostCleanupGrace = 15 * time.Minute

// Called under reconcileMu. A removal is not complete until the host says it
// is: recording a terminal job, restarting, or exhausting a lease is not proof.
func (c *Controller) recoverHostCleanup(ctx context.Context) error {
	if !c.mayAct() {
		return nil
	}
	const batch = 100
	runners, err := c.st.PendingHostCleanup(ctx, c.cleanupCursor, batch, c.Now().Add(-hostCleanupGrace))
	if err != nil {
		return err
	}
	for _, r := range runners {
		c.enqueueLifecycle(ctx, r.HostID, agent.Task{
			Kind: agent.TaskRemoveRunner, RunnerID: r.ID, Backend: c.backendKind(ctx, r, nil),
		})
		c.cleanupCursor = r.ID
	}
	if len(runners) < batch {
		c.cleanupCursor = ""
	}
	return nil
}
