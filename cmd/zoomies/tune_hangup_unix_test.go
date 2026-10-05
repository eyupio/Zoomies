//go:build unix

package main

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// Go exits on an uncaught SIGHUP without running a defer, which is how a dropped SSH
// session left a maintenance restart's agent and controller stopped. The context that
// the restore unwinds through ends instead.
func TestAHungUpTerminalEndsTheTuneContextInsteadOfKillingTheProcess(t *testing.T) {
	ctx, stop := untilHangup(context.Background())
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not end the context")
	}
}
