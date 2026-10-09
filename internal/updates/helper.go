package updates

// HelperUnsupported is why the update helper cannot be installed beside a
// service, as a fixed word: the controller says it of itself, an agent sends it
// on its heartbeat, and the status keys its sentence on it. Empty is nothing
// known, which the status shows as a helper not yet installed.
//
// It is a word and not a sentence because it crosses the wire from an agent,
// and the controller does not show an agent's text where every role reads it.
type HelperUnsupported string

const (
	// HelperUnsupportedOS is a host that is not Linux: the helper is a pair of
	// systemd units, and `zoomies updates helper install` refuses anywhere else.
	HelperUnsupportedOS HelperUnsupported = "os"
	// HelperUnsupportedNoSystemd is a Linux host that systemd does not run, which
	// ResolveHelperInstall refuses.
	HelperUnsupportedNoSystemd HelperUnsupported = "no-systemd"
	// HelperUnsupportedRootless is a container under a rootless runtime, which
	// owns what the container writes under another uid than the image's; the
	// helper refuses every request it did not see written by the image's account.
	HelperUnsupportedRootless HelperUnsupported = "rootless"
	// HelperUnsupportedNoSharedFolder is a container that runs no runners, so it
	// does not mount the shared folder the helper on the host reads requests
	// from. The installer offers such a container no helper.
	HelperUnsupportedNoSharedFolder HelperUnsupported = "no-shared-folder"
)

// Known says whether u is one of the causes above. A word an agent sends that
// is not is read as nothing known.
func (u HelperUnsupported) Known() bool {
	switch u {
	case HelperUnsupportedOS, HelperUnsupportedNoSystemd, HelperUnsupportedRootless, HelperUnsupportedNoSharedFolder:
		return true
	}
	return false
}

// HelperHost is what a service can see of the machine it runs on that decides
// whether the update helper could be installed there. The caller gathers it, so
// the decision is a table test and never a look at the real host.
type HelperHost struct {
	// GOOS is the operating system the service runs on.
	GOOS string
	// InContainer says the service runs in a container. The helper is then
	// installed on the host around it, which the container cannot see.
	InContainer bool
	// Systemd says systemd runs this machine. It means nothing in a container,
	// whose host may run systemd whatever the container sees.
	Systemd bool
	// RunsRunners says the service creates runners: an agent always does, a
	// controller when its embedded agent is on. A container mounts the shared
	// folder only then.
	RunsRunners bool
	// RootlessRuntime says the container runtime this service's runners use runs
	// without root. In a container that is the runtime the container runs under.
	RootlessRuntime bool
}

// HelperSupport says why the update helper can never serve the service on h, or
// nothing when it could, or when the service cannot tell.
//
// Only its first two answers are refusals of the installer's, and only the
// systemd one is the installer's own look: `zoomies updates helper install`
// refuses where /run/systemd/system is missing (backend.HasSystemd, which
// agent.LocalHelperHost reads too) and is not built for a platform that is not
// Unix, and an operating system other than Linux always meets one of the two.
// The two container answers are not refusals at all. The installer would put
// the helper on the host around such a container, and it would never act on
// what the container writes: a container that runs no runners mounts no shared
// folder, and the installer only stops offering it one; one under a rootless
// runtime writes as an account the helper refuses. Anything the service cannot
// see (a service that runs as root, a user-namespace-remapped Docker daemon, a
// Linux host around a container that does not run systemd) is left to the
// installer, which says so itself, and the status keeps offering the command.
func HelperSupport(h HelperHost) HelperUnsupported {
	switch {
	case h.GOOS != "linux":
		return HelperUnsupportedOS
	case !h.InContainer && !h.Systemd:
		return HelperUnsupportedNoSystemd
	case h.InContainer && !h.RunsRunners:
		return HelperUnsupportedNoSharedFolder
	case h.InContainer && h.RootlessRuntime:
		return HelperUnsupportedRootless
	}
	return ""
}
