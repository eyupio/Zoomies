//go:build !unix

package main

import "os"

// tryLockSetup does nothing off Unix. The Proxmox gateway runs as a systemd
// unit, so the upgrade that takes this lock only ever finds one on Linux; this
// exists so the package still builds for Windows, where the installer is
// otherwise unused.
func tryLockSetup(*os.File) (release func(), err error) { return func() {}, nil }
