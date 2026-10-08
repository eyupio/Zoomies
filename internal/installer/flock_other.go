//go:build !unix

package installer

import "os"

// The gateway units these locks guard are systemd units, and
// upgradeProxmoxGateways returns before touching anything on any other
// operating system; these exist so the package compiles there.
func TryLockExclusive(*os.File) error { return nil }

func UnlockFile(*os.File) error { return nil }
