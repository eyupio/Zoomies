//go:build !windows

package main

import (
	"os"
	"syscall"
)

// reexecBinary replaces this process with the binary just installed, carrying
// the same arguments, so one `zoomies upgrade` runs the new release's upgrade
// -- its checks, its prompts, its host-health report -- rather than the old
// one's. On success it does not return.
var reexecBinary = func(path string, args []string, env []string) error {
	return syscall.Exec(path, append([]string{path}, args...), env)
}

func processArgs() []string { return os.Args[1:] }
