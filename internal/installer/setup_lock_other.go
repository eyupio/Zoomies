//go:build !unix && !windows

package installer

import "errors"

var ErrSetupLocked = errors.New("installer: setup lock is held")

// LockSetup is the fallback for a platform this project does not release
// binaries for: no lock, so a concurrent setup is not detected there.
func LockSetup(string) (func(), error) { return func() {}, nil }
