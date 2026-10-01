//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachFromTerminal puts a child in a process group of its own.
//
// Ctrl-C is delivered to the whole foreground group of a terminal, and a
// browser started by the demo is in it unless it is moved out: stopping the
// demo would then be a signal to the browser too, which is somebody's other
// tabs. A group of its own means Ctrl-C reaches the demo and nothing else.
func detachFromTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
