package controller

import (
	"fmt"
	"strings"

	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// hostUpgrade tells the operator how to align a remote host. An ahead agent
// needs its controller upgraded, never a command that silently downgrades it.
func hostUpgrade(host *store.Host, controllerVersion string) (command, target, note string) {
	if host.Embedded {
		return "", "", ""
	}
	skew := version.CompareBuilds(host.Version, controllerVersion)
	if skew == version.SkewNone && !host.Incompatible {
		return "", "", ""
	}
	if skew == version.SkewAhead {
		return "", "", "This agent is newer than the controller. Upgrade the controller first."
	}
	tag, ok := version.InstallTag(controllerVersion)
	if !ok {
		return "", "", fmt.Sprintf("The controller is %s, a local or unpublished build. Install the matching agent from that build; there is no published upgrade command.", controllerVersion)
	}
	if host.OS == "windows" {
		// Windows has no sudo, and zoomies upgrade replaces a service it finds
		// under systemd or launchd and knows no Windows one, so a command offered
		// here is one that fails. The steps are what works.
		return "", controllerVersion, "zoomies upgrade cannot update a Windows agent, so update this one by hand from an elevated prompt: " +
			"put the zoomies.exe of " + tag + " in place of the one the agent runs, then run sc.exe stop zoomies-agent and sc.exe start zoomies-agent. " +
			"Running runner processes are adopted when the agent starts again."
	}
	quoted := "'" + strings.ReplaceAll(tag, "'", "'\"'\"'") + "'"
	command = "sudo zoomies upgrade --mode agent --version " + quoted
	note = "Run this on the host. It keeps the existing configuration and credentials, refreshes stock runner images and restarts the agent."
	if tag == "dev" {
		note += " The dev channel moves with main, so it may contain a newer build than this controller."
	}
	return command, controllerVersion, note
}
