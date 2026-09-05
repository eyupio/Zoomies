//go:build windows

package backend

import (
	"os"
	"os/exec"
	"syscall"
)

// detachRunner is a no-op on Windows, which has no process groups in the POSIX
// sense; the process backend is Linux-only in practice (Probe says so).
func detachRunner(*exec.Cmd) {}

// signalRunner can only kill on Windows: there is no interrupt to send to an
// arbitrary process, and the caller falls back to killing when this fails.
func signalRunner(proc *os.Process, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return proc.Kill()
	}
	return syscall.EWINDOWS
}
