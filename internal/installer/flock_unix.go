//go:build unix

package installer

import (
	"os"
	"syscall"
)

// TryLockExclusive takes an advisory exclusive lock on f without waiting, so a
// gateway whose setup is still running is left alone rather than upgraded
// under it, and a second setup is refused rather than run alongside the
// first. It is released by UnlockFile or when the file closes.
func TryLockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func UnlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
