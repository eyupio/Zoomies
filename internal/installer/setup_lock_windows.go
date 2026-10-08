//go:build windows

package installer

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var ErrSetupLocked = errors.New("installer: setup lock is held")

// LockSetup is the Windows form of the same non-blocking exclusive lock, which
// Windows also releases when the handle closes however the process dies.
func LockSetup(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrSetupLocked
		}
		return nil, err
	}
	return func() { windows.UnlockFileEx(h, 0, 1, 0, ol); f.Close() }, nil
}
