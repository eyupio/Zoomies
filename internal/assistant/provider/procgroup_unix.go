//go:build unix

package provider

import (
	"os/exec"
	"syscall"
)

// ownGroup puts a command in a process group of its own, so that ending it ends
// what it started as well. Claude Code is a Node program that starts others, and
// a process that is killed while its children hold its output open is waited for
// until they let go.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
}

// killGroup ends the command and everything in its group.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
