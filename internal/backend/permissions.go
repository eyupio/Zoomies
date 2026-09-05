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
// So the denial is diagnosed against four facts the agent can read for itself:
// who it is, whether it is in a container, what owns the socket, and which
// groups the process actually holds as opposed to which ones the user database
// lists.
//
// The container case is the one that makes the group advice actively wrong. An
// agent in the published image runs as a user that exists only inside that
// image, so `usermod -aG 987 nonroot` on the host answers "user does not exist"
// and there is nothing the operator can do to make it work. A container is
// given a group when it is created, not afterwards.

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// agentIdentity is the agent's view of itself, injected so that every branch of
// the diagnosis can be tested on a machine with one user and no docker group.
type agentIdentity struct {
	uid int
	// gid is the primary group, which is what separates "the groups this
	// process was given" from the one it would have anyway.
	gid      int
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
	// containerized reports that this agent is itself in a container, which
	// changes the fix completely: its account is the image's, not the host's,
	// and the group has to be granted when the container is created.
	containerized bool
	// envDockerGID is DOCKER_GID from the environment, if the deployment put
	// it there. The compose files do: it is the value group_add was given, so
	// when it is present the advice can say which line to change rather than
	// which file to look in.
	envDockerGID string
}

func realIdentity() agentIdentity {
	id := agentIdentity{
		uid:          os.Geteuid(),
		gid:          os.Getegid(),
		envDockerGID: strings.TrimSpace(os.Getenv("DOCKER_GID")),
		groupName: func(gid int) string {
			g, err := user.LookupGroupId(strconv.Itoa(gid))
			if err != nil || g == nil {
				return ""
			}
			return g.Name
		},
		stat:          statOwner,
		unit:          agentUnit(),
		containerized: inContainer(),
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
	case ok && id.containerized && !id.holdsGroup(gid):
		// Nothing an operator does on the host to a user that only exists in
		// the image will help. The container needs the host gid at creation.
		return id.containerDenied(path, gid, perm)

	case !ok && id.containerized:
		// The socket cannot even be examined from in here, which usually means
		// it was never mounted.
		return fmt.Sprintf("permission denied on %s: this agent runs in a container as %s and cannot open that socket. "+
			"Mount the host's socket into the container and give the container the group that owns it -- "+
			"`stat -c '%%g' %s` on the host says which gid; with docker compose put DOCKER_GID=<gid> in .env (the compose file's group_add reads it) "+
			"and run `docker compose up -d`, with docker run recreate the container with --group-add <gid>.",
			path, id.name(), path)

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

// containerDenied is the advice for a container that was not given the group
// owning the socket, written as the two steps a compose operator actually has
// to take.
//
// The earlier version of this sentence offered `group_add: ["987"]` as a
// copyable snippet, and the UI dutifully gave it a copy button -- so it was
// pasted into a shell, which answered "command not found", and the container
// was recreated with the same wrong group as before. The compose file already
// has group_add; what it reads is DOCKER_GID in .env, and that line is the
// only thing to change. Naming the group the container currently holds is
// what makes the mismatch obvious at a glance: "holds 999, socket is 987".
//
// Nothing here is a `docker compose down`: `up -d` recreates a container whose
// configuration changed, and the destructive variant, `down -v`, is what an
// operator reaches for when told to "recreate" -- and it deletes the volume
// with the database in it.
func (id agentIdentity) containerDenied(path string, gid int, perm string) string {
	socketGID := strconv.Itoa(gid)
	var b strings.Builder
	fmt.Fprintf(&b, "permission denied on %s: this agent runs in a container as %s, and the host socket mounted into it is group-owned by %s (mode %s)",
		path, id.name(), socketGID, perm)

	if gid == 0 {
		// The only gid that would reach this socket is root's, and the two
		// numbered steps below would put the container in it. That is not a
		// fix Zoomies will suggest; the socket needs a group of its own.
		b.WriteString(", which is root's group: the only DOCKER_GID that would open it is 0, and a container in the root group is not something to run jobs from. ")
		fmt.Fprintf(&b, "Give the socket a group of its own on the host -- `sudo groupadd docker`, then restart the daemon so it takes the group, "+
			"and put that group's gid (`stat -c '%%g' %s`) in DOCKER_GID -- or run a rootless daemon and point ZOOMIES_DOCKER_HOST at its socket.", path)
		return b.String()
	}

	switch extra := id.extraGroups(); {
	case len(extra) == 1:
		fmt.Fprintf(&b, ", but the container holds group %d, not %s", extra[0], socketGID)
	case len(extra) > 1:
		fmt.Fprintf(&b, ", but the container holds groups %s, not %s", joinInts(extra), socketGID)
	default:
		b.WriteString(", and the container was given no extra group at all")
	}
	if id.envDockerGID != "" {
		if slices.Contains(id.extraGroups(), atoiOr(id.envDockerGID, -1)) {
			fmt.Fprintf(&b, " -- DOCKER_GID=%s in its environment is where that came from", id.envDockerGID)
		} else {
			fmt.Fprintf(&b, " (its environment says DOCKER_GID=%s, but no group_add carried that into the container)", id.envDockerGID)
		}
	}
	b.WriteString(". A usermod on the host cannot help: that account exists only inside the image, and a container is given its groups when it is created. ")

	b.WriteString("With docker compose, in the directory that holds docker-compose.yml: ")
	if id.envDockerGID != "" && id.envDockerGID != socketGID {
		fmt.Fprintf(&b, "1. change the DOCKER_GID line in .env from %s to `DOCKER_GID=%s`", id.envDockerGID, socketGID)
	} else {
		fmt.Fprintf(&b, "1. put `DOCKER_GID=%s` in .env, which is the value the compose file's group_add reads", socketGID)
	}
	fmt.Fprintf(&b, " (`stat -c '%%g' %s` on the host confirms the gid); ", path)
	b.WriteString("2. run `docker compose up -d`, which recreates the container with that group -- there is no need to bring it down first, and down -v would delete the volume with the database in it. ")
	fmt.Fprintf(&b, "With docker run, recreate the container with `--group-add %s`.", socketGID)
	return b.String()
}

// extraGroups are the groups the process holds beyond its primary one: for a
// container, exactly what group_add or --group-add gave it.
func (id agentIdentity) extraGroups() []int {
	var out []int
	for _, g := range id.processGroups {
		if g != id.gid && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return n
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

// inContainer reports whether this agent is itself running in a container.
//
// Docker writes /.dockerenv and Podman /run/.containerenv; the cgroup line is
// the fallback for runtimes that write neither, and for a container started
// with an unusual entrypoint. A false negative only costs the sharper wording,
// so none of these checks needs to be certain.
func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	for _, needle := range []string{"docker", "containerd", "libpod", "kubepods"} {
		if strings.Contains(string(cgroup), needle) {
			return true
		}
	}
	return false
}
