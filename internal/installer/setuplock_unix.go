//go:build unix

package installer

import (
	"os"
	"syscall"
)

// tryLockSetup takes the setup lock without waiting. The kernel drops the lock
// when the process exits however it exits, so an interrupted setup never
// leaves one behind.
func tryLockSetup(f *os.File) (release func(), err error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
