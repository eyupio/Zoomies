package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
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

// helperHostOr is the host the question reads from: the real one, or the
// stand-in a test gave.
func helperHostOr(stand *upgradeHelperHost) upgradeHelperHost {
	if stand != nil {
		return *stand
	}
	return upgradeHelperHost{systemdDir: "/run/systemd/system", unitDir: systemdUnitDir, stateDir: UpdateHelperStateDir, resolve: ResolveHelperInstall}
}

// helperPresent is whether the update helper is on this host, judged on what
// only root can write: its units and root's folder for it. The pointer beside
// the configuration is the service's to replace, so it decides nothing, and
// root must never conclude from a file the service could have written that the
// helper is there, or that it is not.
func helperPresent(unitDir, stateDir string) bool {
	return exists(filepath.Join(unitDir, UpdatePathUnit)) || exists(filepath.Join(unitDir, UpdateServiceUnit)) || exists(stateDir)
}

// helperOffer is one asking of the update helper question. The upgrade, the
// install and the join all put it, in the same words and with the same
// refusals, so what each of them does with an answer is decided here once.
//
// It is not part of what --yes approves. --yes accepts the deployment's
// additions in advance, and this is a grant of root to a request the service
// writes; only the person at the host, or --update-helper typed by them (or the
// answer file's update_helper, which they wrote), says yes to it. The upgrade
// the helper itself runs is never interactive and never passes either, so with
// the helper installed it asks nothing and says nothing.
type helperOffer struct {
	Out io.Writer
	In  io.Reader
	// ConfigDir is the deployment's configuration directory, which the retry
	// command names when it is not the default.
	ConfigDir string
	// Interactive is whether somebody is at a terminal to read a hint and answer
	// the question.
	Interactive bool
	// Requested is the helper asked for by name, without a question.
	Requested bool
	host      upgradeHelperHost
	run       commandRunner
	// noSharedFolder, when set, says whether this is a container that runs no
	// runners. Such a container does not mount the shared folder, so the helper
	// could only refuse it: asked for by name it gets that refusal in the
	// helper's words, and offered unasked it would be a question with no answer.
	// It is a function because the upgrade reads the deployment's settings to
	// know, and only a container has anything to read.
	noSharedFolder func(context.Context) bool
	// hold, when set, is asked once the helper could be installed here, and a
	// true stops the offer there. It has said why itself.
	hold func() bool
}

// offer asks, as a question of its own, whether this host may be updated from
// the web UI, and installs the helper if the answer is yes.
//
// The question is asked only where it could be answered: on a systemd host,
// where the helper is not already installed, and for a service that can be
// resolved to an account other than root's. A refusal after a yes is reported
// and the caller goes on, since the host is no worse for not having the helper
// and the refusal is something to put right and retry.
func (o helperOffer) offer(ctx context.Context) {
	out, ui := o.Out, PaletteFor(o.Out)
	host := o.host
	if !exists(host.systemdDir) {
		if o.Requested {
			o.notAdded(noSystemdForHelper)
		}
		return
	}
	if helperPresent(host.unitDir, host.stateDir) {
		return
	}
	if !o.Requested && o.noSharedFolder != nil && o.noSharedFolder(ctx) {
		return
	}
	helper, err := host.resolve(o.ConfigDir)
	if err != nil {
		var nothingToServe helperNotApplicable
		switch {
		case o.Requested:
			o.notAdded(err)
		case o.Interactive && !errors.As(err, &nothingToServe):
			// Something the operator can put right, and somebody is there to read it.
			ui.Hint(out, "The update helper cannot be offered on this host: %v", err)
		}
		return
	}
	if o.hold != nil && o.hold() {
		return
	}
	switch {
	case o.Requested:
	case o.Interactive && o.In != nil:
		fmt.Fprintln(out, "Update this host from the web UI?")
		fmt.Fprintln(out, "  The update helper is a pair of systemd units ("+UpdatePathUnit+" and "+UpdateServiceUnit+")")
		fmt.Fprintln(out, "  that run `zoomies upgrade` as root for a validated request from the web UI; a")
		fmt.Fprintln(out, "  request names a release and nothing else. The account Zoomies runs as ("+helper.Account+")")
		fmt.Fprintln(out, "  can trigger an upgrade by writing a request. `sudo zoomies updates helper remove`")
		fmt.Fprintln(out, "  takes it away.")
		if !askDefaultNo(o.In, out, "Add the update helper? [y/N] ") {
			o.skipped()
			return
		}
	default:
		o.skipped()
		return
	}
	helper.Out, helper.run = out, o.run
	ui.Doing(out, "Adding the update helper")
	if err := InstallUpdateHelper(ctx, helper); err != nil {
		o.notAdded(err)
	}
}

// offerUpdateHelper is the upgrade's asking. It comes after the layout review
// and before anything is pulled or restarted, and not in the run that is about
// to give the container its shared mount.
func (p *upgradePlan) offerUpdateHelper(ctx context.Context) {
	if !p.managesZoomiesUnits() {
		return
	}
	helperOffer{
		Out: p.opts.Out, In: p.opts.In, ConfigDir: p.opts.ConfigDir,
		Interactive: p.opts.Interactive, Requested: p.opts.UpdateHelper,
		host: helperHostOr(p.opts.helperHost), run: p.opts.run, hold: p.holdForSharedMount,
		noSharedFolder: func(ctx context.Context) bool {
			return p.record.Deployment.Containerised() && !p.settings(ctx).runsRunners(p.record.Mode)
		},
	}.offer(ctx)
}

// holdForSharedMount is whether the offer must wait for the restart that gives
// the running container its shared folder. The install would refuse it now with
// a sentence about runner-less containers; it gets the mount when it is
// recreated below, and then the install works.
func (p *upgradePlan) holdForSharedMount() bool {
	if !p.sharedMountComing {
		return false
	}
	ui, command := PaletteFor(p.opts.Out), helperInstallCommand(p.opts.ConfigDir)
	if p.sharedMountApplied {
		ui.Hint(p.opts.Out, "The web UI update helper needs the shared folder mounted, which the restart below adds; run %s afterwards.", command)
	} else {
		ui.Hint(p.opts.Out, "The web UI update helper needs the shared folder mounted, which the deployment additions above would give it; once they are added (sudo zoomies upgrade --yes), run %s.", command)
	}
	return true
}

// stepUpdateHelper is init's asking. It comes last, once the service is
// installed (a container: once it is up, and so has its shared mount) and the
// account it runs as can be read back from what was installed, and before the
// summary, which stays the last thing the operator reads.
func (i *Installer) stepUpdateHelper(ctx context.Context, p Plan) {
	helperOffer{
		Out: i.out, In: i.in, ConfigDir: p.ConfigDir,
		Interactive: i.interactive, Requested: i.updateHelperRequested(),
		host: helperHostOr(i.opts.helperHost), run: i.opts.run,
		noSharedFolder: func(context.Context) bool { return p.Deployment.Containerised() && !p.runsRunners() },
	}.offer(ctx)
}

// updateHelperRequested is the helper asked for by name: --update-helper, or an
// answer file that says update_helper: true. Nothing else is, --yes included.
func (i *Installer) updateHelperRequested() bool {
	return i.opts.UpdateHelper || (i.answers != nil && i.answers.UpdateHelper != nil && *i.answers.UpdateHelper)
}

// offerUpdateHelper is the join's asking, once the agent's service is
// installed. A join is a single-use token already spent and a host already
// enrolled, so nothing here may fail it.
func (o JoinOptions) offerUpdateHelper(ctx context.Context) {
	helperOffer{
		Out: o.Out, In: o.In, ConfigDir: o.configDir(),
		Interactive: o.Interactive && !o.NonInteractive, Requested: o.UpdateHelper,
		host: helperHostOr(o.helperHost), run: o.run,
	}.offer(ctx)
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

// helperInstallCommand is the command that adds the helper later. It names the
// configuration directory when the run was against another than the default,
// because the bare command would look at the wrong deployment.
func helperInstallCommand(configDir string) string {
	command := "sudo zoomies updates helper install"
	if configDir != "" && configDir != config.ConfigDir() {
		if strings.ContainsAny(configDir, " \t") {
			configDir = strconv.Quote(configDir)
		}
		command += " --config-dir " + configDir
	}
	return command
}

func (o helperOffer) skipped() {
	PaletteFor(o.Out).Hint(o.Out, "The update helper was not added, so the web UI cannot update this host.")
	fmt.Fprintln(o.Out, "Add later: "+helperInstallCommand(o.ConfigDir))
}

// notAdded reports a refusal in the helper's own words. It is a warning and not
// an error: the host is no worse for it, and what was being done goes on.
func (o helperOffer) notAdded(reason any) {
	ui := PaletteFor(o.Out)
	ui.Warn(o.Out, "The update helper was not added: %v", reason)
	ui.Hint(o.Out, "Everything else goes on without it. Once that is put right, add it with: %s", helperInstallCommand(o.ConfigDir))
}

// helperNotApplicable marks a host the helper has nothing to serve: a service
// that runs as root, or no zoomies service at all. The upgrade says nothing
// about the helper there, where any other reason it cannot be resolved is
// something to tell the person at the terminal.
type helperNotApplicable struct{ error }

// askDefaultNo reads one answer to a [y/N] question: only y or yes is a yes,
// and an empty line or an input that ends first is a no.
func askDefaultNo(in io.Reader, out io.Writer, question string) bool {
	answer, ok := readAnswer(in, out, question)
	if !ok {
		return false
	}
	return slices.Contains([]string{"y", "yes"}, strings.ToLower(answer))
}
