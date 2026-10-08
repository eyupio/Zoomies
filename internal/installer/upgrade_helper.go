package installer

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// noSystemdForHelper is the refusal on a host that systemd does not run: the
// helper is a pair of systemd units, and nothing else could start it.
const noSystemdForHelper = `the update helper is a pair of systemd units, and this host does not run systemd; update it by hand with "sudo zoomies upgrade"`

// upgradeHelperHost is what the update helper question reads from the host.
type upgradeHelperHost struct {
	// systemdDir exists only while systemd is the init system.
	systemdDir string
	// unitDir and stateDir are where the helper's units and root's folder for
	// it are, which is what decides that the helper is installed.
	unitDir, stateDir string
	resolve           func(configDir string) (InstallHelperOptions, error)
}

func (p *upgradePlan) helperHost() upgradeHelperHost {
	h := upgradeHelperHost{systemdDir: "/run/systemd/system", unitDir: systemdUnitDir, stateDir: UpdateHelperStateDir, resolve: ResolveHelperInstall}
	if t := p.opts.helperHost; t != nil {
		h = *t
	}
	return h
}

// helperPresent is whether the update helper is on this host, judged on what
// only root can write: its units and root's folder for it. The pointer beside
// the configuration is the service's to replace, so it decides nothing, and
// root must never conclude from a file the service could have written that the
// helper is there, or that it is not.
func helperPresent(unitDir, stateDir string) bool {
	return exists(filepath.Join(unitDir, UpdatePathUnit)) || exists(filepath.Join(unitDir, UpdateServiceUnit)) || exists(stateDir)
}

// offerUpdateHelper asks, as a question of its own, whether this host may be
// updated from the web UI, and installs the helper if the answer is yes.
//
// It is not part of what --yes approves. --yes accepts the deployment's
// additions in advance, and this is a grant of root to a request the service
// writes; only the person at the host, or --update-helper typed by them, says
// yes to it. The upgrade the helper itself runs is never interactive and never
// passes either, so it can only ever print the line saying how to add it.
//
// The question is asked only where it could be answered: on a systemd host,
// where the helper is not already installed, for a service that can be resolved
// to an account other than root's. A refusal after a yes is reported and the
// upgrade goes on, since the host is no worse for not having the helper and the
// refusal is something to put right and retry.
func (p *upgradePlan) offerUpdateHelper(ctx context.Context) {
	if p.opts.Check || !p.managesZoomiesUnits() {
		return
	}
	out, ui := p.opts.Out, PaletteFor(p.opts.Out)
	host := p.helperHost()
	if !exists(host.systemdDir) {
		if p.opts.UpdateHelper {
			p.helperNotAdded(noSystemdForHelper)
		}
		return
	}
	if helperPresent(host.unitDir, host.stateDir) {
		return
	}
	helper, err := host.resolve(p.opts.ConfigDir)
	if err != nil {
		if p.opts.UpdateHelper {
			p.helperNotAdded(err)
		}
		return
	}
	switch {
	case p.opts.UpdateHelper:
	case p.opts.Interactive && p.opts.In != nil:
		fmt.Fprintln(out, "Update this host from the web UI?")
		fmt.Fprintln(out, "  The update helper is a pair of root-owned systemd units ("+UpdatePathUnit+" and "+UpdateServiceUnit+")")
		fmt.Fprintln(out, "  that run `zoomies upgrade` for a request from the web UI, once the helper has")
		fmt.Fprintln(out, "  validated it; the request names a release and nothing else. You install it as")
		fmt.Fprintln(out, "  this host's owner, and `sudo zoomies updates helper remove` takes it away.")
		if !askDefaultNo(p.opts.In, out, "Add the update helper? [y/N] ") {
			p.updateHelperSkipped()
			return
		}
	default:
		p.updateHelperSkipped()
		return
	}
	helper.Out, helper.run = out, p.opts.run
	ui.Doing(out, "Adding the update helper")
	if err := InstallUpdateHelper(ctx, helper); err != nil {
		p.helperNotAdded(err)
	}
}

// managesZoomiesUnits is whether the upgrade is of a deployment the helper can
// update: a container, or the zoomies controller or agent unit. A private
// provider's executable upgrades through its own unit, which the helper does
// not serve.
func (p *upgradePlan) managesZoomiesUnits() bool {
	if p.record.Deployment.Containerised() {
		return true
	}
	return !p.launchd && (slices.Contains(p.nativeUnits, UnitController) || slices.Contains(p.nativeUnits, UnitAgent))
}

func (p *upgradePlan) updateHelperSkipped() {
	PaletteFor(p.opts.Out).Hint(p.opts.Out, "The update helper was not added, so the web UI cannot update this host.")
	fmt.Fprintln(p.opts.Out, "Add later: sudo zoomies updates helper install")
}

// helperNotAdded reports a refusal in the helper's own words. It is a warning
// and not an error: the upgrade is not worse for it.
func (p *upgradePlan) helperNotAdded(reason any) {
	ui := PaletteFor(p.opts.Out)
	ui.Warn(p.opts.Out, "The update helper was not added: %v", reason)
	ui.Hint(p.opts.Out, "The upgrade goes on without it. Once that is put right, add it with: sudo zoomies updates helper install")
}

// askDefaultNo reads one answer to a [y/N] question: only y or yes is a yes,
// and an empty line or an input that ends first is a no.
func askDefaultNo(in io.Reader, out io.Writer, question string) bool {
	answer, ok := readAnswer(in, out, question)
	if !ok {
		return false
	}
	return slices.Contains([]string{"y", "yes"}, strings.ToLower(answer))
}
