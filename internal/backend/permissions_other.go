//go:build !unix

package backend

import "io/fs"

// statOwner has no answer off unix, where sockets have no owning group. The
// diagnosis falls back to what the agent knows about itself.
func statOwner(string) (fs.FileMode, int, bool) { return 0, 0, false }
