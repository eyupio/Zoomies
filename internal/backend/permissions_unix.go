//go:build unix

package backend

import (
	"io/fs"
	"os"
	"syscall"
)

// statOwner reports a path's mode and owning gid.
//
// os.Stat follows the symlink /var/run/docker.sock usually is, which is what we
// want: the mode that matters belongs to the socket the connection lands on.
func statOwner(path string) (fs.FileMode, int, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fi.Mode(), 0, false
	}
	return fi.Mode(), int(st.Gid), true
}
