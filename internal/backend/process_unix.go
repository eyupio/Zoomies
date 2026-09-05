//go:build !windows

package backend

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detachRunner starts the runner as the leader of a process group of its own.
//
// Two things follow. A signal meant for the runner reaches its whole worker
// tree -- Runner.Listener spawns Runner.Worker per job, and interrupting the
// listener alone orphaned the worker with the job still running. And a signal
// meant for the agent does not reach the runner: a terminal's Ctrl-C, or a
// service manager stopping the unit, stays with the agent's own group.
func detachRunner(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalRunner delivers sig to the runner's process group. A group that is
// already gone answers os.ErrProcessDone, so callers treat it as the other
// "already exited" answers.
func signalRunner(proc *os.Process, sig syscall.Signal) error {
	err := syscall.Kill(-proc.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
