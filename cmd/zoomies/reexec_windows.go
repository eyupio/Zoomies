//go:build windows

package main

import (
	"errors"
	"os"
)

// Windows cannot replace a running process image, and SelfUpdate does not
// download for it, so this is only here to keep the package building.
var reexecBinary = func(string, []string, []string) error {
	return errors.New("restarting into an updated binary is not supported on Windows")
}

func processArgs() []string { return os.Args[1:] }
