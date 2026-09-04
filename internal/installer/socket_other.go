//go:build !unix

package installer

import "io/fs"

// fileOwner has no answer off unix, where a socket has no owning group and
// none of the group handling around it applies.
func fileOwner(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
