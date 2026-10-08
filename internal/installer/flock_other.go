//go:build !unix

package installer

import "os"

// The gateway units these locks guard are systemd units, and
// upgradeProxmoxGateways returns before touching anything on any other
// operating system; these exist so the package compiles there.
func tryLockExclusive(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
