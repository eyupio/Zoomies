//go:build unix

package installer

import (
	"errors"
	"os"
	"syscall"
)

var ErrSetupLocked = errors.New("installer: setup lock is held")

// LockSetup takes the advisory lock a running Proxmox setup holds, without
// waiting: an upgrade that would overwrite a gateway binary mid-setup should
// say so and stop. The kernel drops it when the process exits, so a crashed
// setup cannot leave a lock behind.
func LockSetup(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrSetupLocked
		}
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
