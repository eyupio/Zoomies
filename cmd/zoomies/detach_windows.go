//go:build windows

package main

import "os/exec"

// detachFromTerminal is nothing on Windows: the opener there is a handler for
// the URL, which hands the page to a browser that is already running or starts
// one outside this console, and a console's Ctrl-C does not reach it.
func detachFromTerminal(*exec.Cmd) {}
