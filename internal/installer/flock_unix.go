//go:build unix

package installer

import (
	"os"
	"syscall"
)

// tryLockExclusive takes an advisory exclusive lock on f without waiting, so a
// gateway whose setup is still running is left alone rather than upgraded
// under it. It is released by unlockFile or when the file closes.
func tryLockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
