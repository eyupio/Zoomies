package installer

// Whether the account the service will run as can actually open the container
// socket, decided before the service is started rather than discovered by an
// operator reading a stalled pool an hour later.
//
// The old rule was "if a group called docker exists, add the service user to
// it". It is right on a stock Docker install and wrong everywhere else: a
// Podman API socket, a distribution that names the group something else, a
// socket that was chgrp'd, or a mode with no group bits at all. In each of
// those the installer reported success and the fleet came up with a host that
// could run nothing. The socket itself knows who may open it, so it is asked.

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"strings"
)

// socketFacts is what the filesystem says about a runtime socket.
type socketFacts struct {
	uid  int
	gid  int
	mode fs.FileMode
}

// statSocket reads the owner and mode of a socket path, following the symlink
// /var/run/docker.sock usually is.
func statSocket(path string) (socketFacts, bool) {
	p := SocketPathOf(path)
	if p == "" {
		return socketFacts{}, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return socketFacts{}, false
	}
	uid, gid, ok := fileOwner(fi)
	if !ok {
		return socketFacts{}, false
	}
	return socketFacts{uid: uid, gid: gid, mode: fi.Mode()}, true
}

// SocketPathOf turns an endpoint into a filesystem path, or "" when there is
// none to have: a TCP daemon has no mode and no group, and nothing here applies
// to it.
func SocketPathOf(endpoint string) string {
	e := strings.TrimSpace(endpoint)
	switch {
	case e == "":
		return ""
	case strings.HasPrefix(e, "tcp://"), strings.HasPrefix(e, "http://"), strings.HasPrefix(e, "https://"):
		return ""
	}
	return strings.TrimPrefix(e, "unix://")
}

// account is the identity the service will have: its uid, and every group the
// user database gives it.
type account struct {
	uid    int
	groups []int
}

// lookupAccount reads an account from the user database. It is deliberately not
// the running process's identity: the installer is usually root, and root can
// open anything, so asking about root would answer the wrong question.
func lookupAccount(name string) (account, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return account{}, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return account{}, fmt.Errorf("installer: %s has a non-numeric uid %q", name, u.Uid)
	}
	acct := account{uid: uid}
	ids, err := u.GroupIds()
	if err != nil {
		return account{}, err
	}
	for _, id := range ids {
		if gid, err := strconv.Atoi(id); err == nil {
			acct.groups = append(acct.groups, gid)
		}
	}
	return acct, nil
}

// canOpen reports whether an account may read and write a socket, by the same
// rule the kernel uses: owner, then group, then other, first match wins.
//
// Root is exempt, which is a fact rather than a recommendation: an installer
// that runs the service as root would pass this check and still be the thing
// the security model asks operators not to do.
func canOpen(s socketFacts, a account) bool {
	if a.uid == 0 {
		return true
	}
	const readWrite = 0o6
	var bits fs.FileMode
	switch {
	case a.uid == s.uid:
		bits = (s.mode.Perm() >> 6) & 0o7
	case containsInt(a.groups, s.gid):
		bits = (s.mode.Perm() >> 3) & 0o7
	default:
		bits = s.mode.Perm() & 0o7
	}
	return bits&readWrite == readWrite
}

// joinable reports whether adding the account to the socket's owning group
// would be enough. A mode of 0600 says no group membership can help, and
// telling somebody to run usermod anyway is how an install ends in a host that
// can run nothing.
func joinable(s socketFacts, a account) bool {
	if containsInt(a.groups, s.gid) {
		return false
	}
	const readWrite = 0o6
	return (s.mode.Perm()>>3)&readWrite == readWrite
}

// socketGroupName names the group that owns a socket, falling back to the gid,
// which usermod and `--group-add` both accept.
func socketGroupName(gid int) string {
	if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil && g != nil && g.Name != "" {
		return g.Name
	}
	return strconv.Itoa(gid)
}

// SocketGroupGID returns the group that owns the socket this host will use, or
// the docker group when the socket cannot be examined -- a container deployment
// on a host where the installer cannot see the socket still has to write some
// gid into `--group-add`, and the docker group is the best guess available.
func SocketGroupGID(endpoint string) int {
	if s, ok := statSocket(endpoint); ok {
		return s.gid
	}
	return dockerGroupGID()
}

func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
