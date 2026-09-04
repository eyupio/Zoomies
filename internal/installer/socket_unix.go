//go:build unix

package installer

import (
	"io/fs"
	"syscall"
)

// fileOwner reads a file's uid and gid.
func fileOwner(fi fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
