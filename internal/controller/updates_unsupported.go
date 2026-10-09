package controller

import (
	"strings"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
)

// controllerUpgradeCommand is what updates this controller by hand on its host,
// for a person to copy where the helper cannot be installed. With no version it
// takes the newest release, which is what the button would have taken.
const controllerUpgradeCommand = "sudo zoomies upgrade"

// helperUnsupported is why the helper cannot be installed beside this
// controller, or nothing. A container has the shared folder only when its
// embedded agent is on, which is the rule the installer mounts it by, and the
// runtime it runs under is the one that agent found.
func (c *Controller) helperUnsupported() updates.HelperUnsupported {
	look := c.helperHost
	if look == nil {
		look = agent.LocalHelperHost
	}
	host := look()
	host.RunsRunners = c.cfg().Agent.Embedded
	c.mu.Lock()
	embedded := c.embedded
	c.mu.Unlock()
	if embedded != nil && embedded.RootlessRuntime() {
		host.RootlessRuntime = true
	}
	return updates.HelperSupport(host)
}

// helperUnsupportedWhy is why the helper cannot be installed, for who, as the
// middle of a sentence. It names no path and nothing an agent wrote, because
// every role reads it.
func helperUnsupportedWhy(cause updates.HelperUnsupported, who string) string {
	switch cause {
	case updates.HelperUnsupportedOS:
		return who + " runs on an operating system other than Linux, and the update helper is a pair of systemd units, which need Linux"
	case updates.HelperUnsupportedNoSystemd:
		return who + " runs on a host that systemd does not run, and the update helper is a pair of systemd units"
	case updates.HelperUnsupportedNoSharedFolder:
		return who + " runs in a container that runs no runners, so the container does not mount the shared folder the update helper would read a request from"
	case updates.HelperUnsupportedRootless:
		return who + " runs in a container under a rootless runtime, which makes what the container writes belong to another account than the one the update helper serves"
	}
	return ""
}

// hostHelperUnsupported is why the helper cannot be installed on a host: what
// its agent said on its last beat, or, from an agent too old to say, an
// operating system the helper never runs on, which the host told the
// controller when it joined.
func (c *Controller) hostHelperUnsupported(h *store.Host) updates.HelperUnsupported {
	if os := strings.TrimSpace(h.OS); os != "" && os != "linux" {
		return updates.HelperUnsupportedOS
	}
	c.updates.hostMu.RLock()
	defer c.updates.hostMu.RUnlock()
	return c.updates.hostUnsupported[h.ID]
}

// noteHelperUnsupported keeps what a host's agent said on this beat about its
// helper. It is memory only: after a restart the next beat says it again, and
// until then the card offers the command that installs the helper, as it
// would for an agent too old to say. A word nobody defined is nothing said.
func (c *Controller) noteHelperUnsupported(hostID, said string) {
	cause := updates.HelperUnsupported(said)
	c.updates.hostMu.Lock()
	defer c.updates.hostMu.Unlock()
	if !cause.Known() {
		delete(c.updates.hostUnsupported, hostID)
		return
	}
	if c.updates.hostUnsupported == nil {
		c.updates.hostUnsupported = make(map[string]updates.HelperUnsupported)
	}
	c.updates.hostUnsupported[hostID] = cause
}
