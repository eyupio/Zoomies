//go:build !unix

package provider

import "os/exec"

// ownGroup is nothing where there are no process groups to use.
func ownGroup(cmd *exec.Cmd) {}

// killGroup ends the command.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
