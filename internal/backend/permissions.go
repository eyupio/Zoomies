package backend

// Why a socket that exists cannot be opened.
//
// "permission denied on /var/run/docker.sock; add your user to the docker group
// (sudo usermod -aG docker $USER, then log in again)" is true often enough to be
// dangerous. $USER is the operator's login shell, not the account the agent runs
// under, and an agent started by systemd as a service user is the normal case --
// so the copied command adds the wrong person to the group and the pool stays
// stuck. Worse, the commonest state after somebody has already run usermod is a
// process that predates the change: the membership is real, the running agent
// does not hold it, and no amount of repeating the same command helps.
//
// So the denial is diagnosed against three facts the agent can read for itself:
// who it is, what owns the socket, and which groups the process actually holds
// as opposed to which ones the user database lists.

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
)

// agentIdentity is the agent's view of itself, injected so that every branch of
// the diagnosis can be tested on a machine with one user and no docker group.
type agentIdentity struct {
	uid      int
	username string
	// processGroups are the groups this process holds. A group added with
	// usermod does not appear here until the process is restarted, and that gap
	// is the difference between "join the group" and "restart the agent".
	processGroups []int
	// userGroups are the groups the user database says the account belongs to.
	userGroups []int
	// groupName resolves a gid to a name, returning "" when it cannot.
	groupName func(gid int) string
	// stat reports the mode and owning gid of a path. ok is false when the path
	// cannot be examined at all, which is itself common here: a socket inside a
	// directory the agent may not traverse.
	stat func(path string) (mode fs.FileMode, gid int, ok bool)
	// unit is the systemd unit the agent runs under, so the restart hint names
	// a command that exists on this host. Empty means no systemd, or no unit
	// this agent could recognise, and the hint stays generic.
	unit string
}

func realIdentity() agentIdentity {
	id := agentIdentity{
		uid: os.Geteuid(),
		groupName: func(gid int) string {
			g, err := user.LookupGroupId(strconv.Itoa(gid))
			if err != nil || g == nil {
				return ""
			}
			return g.Name
		},
		stat: statOwner,
		unit: agentUnit(),
	}
	// The effective gid is not guaranteed to appear in the supplementary list,
	// so it is added explicitly: a socket owned by the agent's own primary group
	// is usable, and must not be reported as a group it needs to join.
	id.processGroups = append(id.processGroups, os.Getegid())
	if groups, err := os.Getgroups(); err == nil {
		id.processGroups = append(id.processGroups, groups...)
	}
	if u, err := user.Current(); err == nil && u != nil {
		id.username = u.Username
		if ids, err := u.GroupIds(); err == nil {
			for _, s := range ids {
				if n, err := strconv.Atoi(s); err == nil {
					id.userGroups = append(id.userGroups, n)
				}
			}
		}
	}
	return id
}

// deniedDetail explains a permission denial on one socket, in a sentence whose
// commands can be pasted without editing.
//
// path is the socket itself; it may be unreadable, in which case the advice
// falls back to what is still knowable -- who the agent is, and where a rootless
// daemon would put a socket it could use.
func deniedDetail(id agentIdentity, path string) string {
	who := id.who()
	mode, gid, ok := fs.FileMode(0), 0, false
	if id.stat != nil {
		mode, gid, ok = id.stat(path)
	}
	// The group is named where the system can name it and numbered where it
	// cannot; usermod takes either, so the command works both ways.
	group := ""
	if ok {
		group = strconv.Itoa(gid)
		if id.groupName != nil {
			if name := id.groupName(gid); name != "" {
				group = name
			}
		}
	}
	perm := fmt.Sprintf("%04o", mode.Perm())

	switch {
	case ok && id.holdsGroup(gid):
		// The agent is in the group and was still refused, so the group is not
		// what is in the way: the mode, or a directory above the socket.
		return fmt.Sprintf("permission denied on %s: %s and already holds group %s, "+
			"so the denial is the socket's own mode (%s) or a directory above it. "+
			"Check `ls -l %s`, or %s",
			path, who, group, perm, path, id.rootlessAlternative())

	case ok && slices.Contains(id.userGroups, gid):
		// usermod has already been run; the process simply predates it.
		return fmt.Sprintf("permission denied on %s: %s is in the %s group that owns the socket, "+
			"but this agent process started before it was added and does not hold the group yet. %s",
			path, id.name(), group, id.restart())

	case ok:
		return fmt.Sprintf("permission denied on %s: %s, and the socket is group-owned by %s (mode %s). "+
			"Run `sudo usermod -aG %s %s` and %s, or %s",
			path, who, group, perm, group, id.name(), id.restartClause(), id.rootlessAlternative())

	case id.uid == 0:
		// Root was refused, which no group can explain.
		return fmt.Sprintf("permission denied on %s even though the agent is running as root, "+
			"which usually means SELinux or AppArmor is blocking it; check `dmesg` or `ausearch -m avc` on this host",
			path)

	default:
		// The socket could not be examined -- typically a directory above it
		// that this agent may not traverse -- so name the usual cause and the
		// escape hatch rather than inventing a group.
		return fmt.Sprintf("permission denied on %s: %s. "+
			"Add that user to the docker group (`sudo usermod -aG docker %s`) and %s, or %s",
			path, who, id.name(), id.restartClause(), id.rootlessAlternative())
	}
}

// who names the account the agent runs as, which is the fact an operator most
// often gets wrong: it is a service user far more often than their own login.
func (id agentIdentity) who() string {
	return fmt.Sprintf("the agent runs as %s (uid %d)", id.name(), id.uid)
}

// name is the agent's username, falling back to the uid when the user database
// has nothing to say about it -- a container with no /etc/passwd entry, say.
func (id agentIdentity) name() string {
	if id.username != "" {
		return id.username
	}
	return "uid " + strconv.Itoa(id.uid)
}

func (id agentIdentity) holdsGroup(gid int) bool {
	return slices.Contains(id.processGroups, gid)
}

// agentUnit names the systemd unit this agent is most likely running under: its
// own on a dedicated host, the controller's where the agent is embedded. A host
// with neither unit file gets no command, because a command that does not exist
// is worse than none.
func agentUnit() string {
	if !HasSystemd() {
		return ""
	}
	for _, unit := range []string{"zoomies-agent", "zoomies"} {
		if _, err := os.Stat(filepath.Join("/etc/systemd/system", unit+".service")); err == nil {
			return unit
		}
	}
	return ""
}

// restart is the whole instruction, for the case where restarting is the only
// thing left to do.
func (id agentIdentity) restart() string {
	if id.unit != "" {
		return fmt.Sprintf("Restart it with `sudo systemctl restart %s` and it will pick the group up.", id.unit)
	}
	return "Restart the agent process and it will pick the group up."
}

// restartClause is the same instruction as a fragment, for the case where it
// follows a usermod in one sentence.
func (id agentIdentity) restartClause() string {
	if id.unit != "" {
		return fmt.Sprintf("restart the agent (`sudo systemctl restart %s`)", id.unit)
	}
	return "restart the agent"
}

// rootlessAlternative points at the other way out of a socket the agent may not
// use: a per-user daemon, which is the arrangement Zoomies prefers anyway.
func (id agentIdentity) rootlessAlternative() string {
	return fmt.Sprintf("run a rootless daemon and set agent.docker_host to %s",
		filepath.Join("/run/user", strconv.Itoa(id.uid), "docker.sock"))
}
