package installer

import (
	"context"
	"net"
	"strings"

	"github.com/eyupio/zoomies/internal/cliconfig"
	"github.com/eyupio/zoomies/internal/config"
)

// The `zoomies` command on the host that runs the controller has to be told
// where the controller is. Installed by hand it never was: `zoomies pools edit`
// answered "no controller URL" on the one machine where the answer is known to
// the installer, and an operator had to write a file nobody had told them
// existed -- in a directory that did not exist either.
//
// So an install writes it, and an upgrade offers to. What is written is the
// address and nothing else. A token is authority; the installer does not mint
// one on an operator's behalf, and says where to make one.

// connectionURL is where this host's own CLI should reach the controller.
//
// The external URL is preferred wherever it is set: it is what the operator
// actually reaches, it carries the right scheme and name for a certificate, and
// a container's published port is not something the controller's own settings
// know. Without one, a controller serving plain HTTP is on its listener,
// reached on loopback when that listener is open to every address. A controller
// serving TLS with no external URL has no name its certificate would match, so
// nothing is guessed.
func connectionURL(external, bind string, tls config.TLSMode) string {
	if u := strings.TrimRight(strings.TrimSpace(external), "/"); u != "" {
		return u
	}
	if tls != config.TLSOff {
		return ""
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(bind))
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// cliConfigChanges is the CLI's connection file, offered where this host runs
// the controller and has none naming one.
func (p *upgradePlan) cliConfigChanges(s deploymentSettings) []layoutChange {
	if p.record.Mode == ModeAgent {
		return nil
	}
	url := connectionURL(s.cfg.Server.ExternalURL, s.cfg.Server.Bind, s.cfg.Server.TLS.Mode)
	if url == "" {
		return nil
	}
	path := cliconfig.Path()
	if existing, err := cliconfig.Load(path); err != nil || strings.TrimSpace(existing.URL) != "" {
		// A file naming a controller is the operator's; one that does not parse
		// is not ours to rewrite.
		return nil
	}
	return []layoutChange{{
		what: "the CLI's connection file " + path + ", naming this controller at " + url + " (no token), so `zoomies pools` and the rest work on this host",
		apply: func(context.Context) error {
			_, err := cliconfig.EnsureURL(path, url)
			return err
		},
	}}
}

// stepCLIConfig writes the same file at install, and says so. It is create-only
// and never fatal: a controller that is installed and running is not made a
// failure by a home directory it could not write to.
func (i *Installer) stepCLIConfig(external, bind string, tls config.TLSMode) {
	url := connectionURL(external, bind, tls)
	if url == "" {
		return
	}
	path := cliconfig.Path()
	outcome, err := cliconfig.EnsureURL(path, url)
	switch {
	case err != nil:
		i.ui.warn("could not write the CLI's connection file " + path + ": " + err.Error())
		i.ui.note("create it with: url: " + url)
	case outcome == cliconfig.Kept:
		// Already names a controller; saying nothing is the right amount.
	default:
		i.ui.ok("wrote " + path + " so `zoomies` on this host finds the controller at " + url)
		i.ui.note("it holds no token: create one in the UI under your account's API tokens and add it as `token:` to change anything from the CLI.")
	}
}
