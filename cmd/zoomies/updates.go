package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/installer"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

// updatesEUID is os.Geteuid, replaceable so that a test can be an account
// other than the one running it.
var updatesEUID = os.Geteuid

// updateHelperStateDir is root's directory for the helper, replaceable so that
// a test can stand one up of its own.
var updateHelperStateDir = installer.UpdateHelperStateDir

// stdTerminal says whether a file is a terminal; a variable so that a test can
// stand a pipe in for one.
var stdTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// canAskAt is whether somebody is at a terminal to be asked a question that is
// written to prompt: the input and the writer that shows the prompt are both
// terminals. It deliberately does not use canAsk, which also requires stdout to
// be a terminal that wants colour: NO_COLOR, or a redirected stdout, says
// nothing about whether anyone can see this prompt, and a redirected stderr
// would leave the command waiting on a question nobody can read.
func canAskAt(in io.Reader, prompt io.Writer) bool {
	i, ok := in.(*os.File)
	p, ok2 := prompt.(*os.File)
	return ok && ok2 && stdTerminal(i) && stdTerminal(p)
}

// helperStatusTailLines is how much of the last run's log tail status prints:
// the end, where the reason is, and not the whole run.
const helperStatusTailLines = 20

func runUpdates(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "updates", `Release updates: what the controller would take, asking it to check or to update itself, and the helper that applies them. To upgrade this host by hand, use "zoomies upgrade".`, []*subcommand{
		{"status", "", "What an update would take, the helper's state and the controller's last attempt", updatesStatusCmd},
		{"check", "", "Read the list of releases from GitHub now and say what it leaves", updatesCheck},
		{"apply", "[--version tag | --hosts | --host name|id] [--yes]", "Ask the controller to update itself, or start a rollout that updates its hosts", updatesApply},
		{"resume", "[--yes]", "Let a halted rollout of the hosts carry on", updatesResume},
		{"cancel", "", "Stop the rollout of the hosts; an update already handed to a host finishes", updatesCancel},
		{"helper", "<install|remove|run|status>", "The root-owned helper on this host that applies an update", runUpdatesHelper},
	}, args)
}

func runUpdatesHelper(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "updates helper", "The update helper on this host. It is local: it never talks to a controller.", []*subcommand{
		{"install", "[--config-dir path]", "Make this host ready to be updated from the controller: install the helper, as root", updatesHelperInstall},
		{"remove", "[--config-dir path]", "Stop the helper and remove it, its units and its files", updatesHelperRemove},
		{"run", "", "Answer the request in the update folder; the helper's unit runs it, as root", updatesHelperRun},
		{"status", "", "Where the update folder is, whether the helper is installed, and its last result", updatesHelperStatus},
	}, args)
}

func updatesStatusCmd(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates status",
		"What an update would take: the mode and soak, the build that is running, the release the mode would take and why, whether the update helper is installed on the controller's host, and the controller's open or last update attempt. It changes nothing. For the helper on this host, use \"zoomies updates helper status\".")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies updates status", "zoomies updates status --output json")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var st updatesStatus
	raw, err := client.get(ctx, "/updates", nil, &st)
	if err != nil {
		return plainAPIError(err)
	}
	if p.structured() {
		return p.emit(raw)
	}

	// Every string below came from the controller, and the attempt's error can be
	// a line of the helper's log, so each goes through plain before it is placed
	// in a row of the CLI's own.
	release := "a release build"
	if !st.Running.Release {
		release = "not from a release, so it is never offered an update"
	}
	rows := [][2]string{
		{"Mode", plain(st.Mode)},
		{"Soak", plain(st.Soak)},
		{"Running", fmt.Sprintf("%s (%s)", plain(st.Running.Version), release)},
	}
	if st.Latest != nil {
		rows = append(rows, [2]string{"Latest", fmt.Sprintf("%s, published %s", plain(st.Latest.Tag), p.relTime(st.Latest.PublishedAt))})
	}
	if st.Target != nil {
		target := plain(st.Target.Tag) + ", newer than the running build"
		if !st.Target.Newer {
			target = plain(st.Target.Tag) + ", not newer than the running build"
		}
		if st.Target.DueAt != nil {
			target += ", due " + p.relTime(*st.Target.DueAt)
		}
		rows = append(rows, [2]string{"Target", target})
	}
	rows = append(rows,
		[2]string{"Checked", p.relTimePtr(st.CheckedAt)},
		[2]string{"Reason", plain(st.Reason)},
		[2]string{"Helper", plain(st.Helper.State) + ": " + plain(st.Helper.Reason)},
	)
	if st.Helper.InstallCommand != "" {
		rows = append(rows, [2]string{"Install", plain(st.Helper.InstallCommand)})
	}
	if st.Helper.UpgradeCommand != "" {
		rows = append(rows, [2]string{"Upgrade", plain(st.Helper.UpgradeCommand)})
	}
	if ro := st.Rollout; ro != nil {
		rows = append(rows, [2]string{"Rollout", rolloutLine(ro)})
		// Only while it is halted: an older controller sends the sentence after a
		// cancel too, and "resume or cancel it" is then a press nobody can make.
		if ro.State == "halted" && ro.HaltedReason != "" {
			rows = append(rows, [2]string{"Halted", plain(ro.HaltedReason)})
		}
	}
	if a := st.Controller; a != nil {
		rows = append(rows, [2]string{"Attempt", fmt.Sprintf("%s %s: %s to %s (%s), requested %s",
			plain(a.ID), plain(a.State), plain(a.From), plain(a.To), plain(a.Trigger), p.relTime(a.RequestedAt))})
		if a.FinishedAt != nil {
			rows = append(rows, [2]string{"Finished", p.relTime(*a.FinishedAt)})
		}
		if a.Error != "" {
			rows = append(rows, [2]string{"Error", plain(a.Error)})
		}
	} else {
		rows = append(rows, [2]string{"Attempt", "none yet"})
	}
	p.keyValues(rows)
	return nil
}

func updatesCheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates check",
		"Read the list of releases from GitHub now instead of waiting for the scheduled check, and say what the controller makes of it. At most one request a minute goes to GitHub; asking again inside the minute answers the status as it stands. Needs the admin role.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies updates check")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var st updatesStatus
	raw, err := client.post(ctx, "/updates/check", nil, nil, &st)
	if err != nil {
		return plainAPIError(err)
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note("%s", plain(st.Reason))
	return nil
}

func updatesApply(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates apply [--version tag | --hosts | --host name|id] [--yes]",
		`Ask the controller to update ITSELF: it writes a request for the root-owned update helper on its own host, and the helper replaces the controller's binary and restarts the service. This is not "zoomies upgrade", which upgrades the host you run it on and needs no controller. Needs the platform role, an update mode other than off, and a helper installed on the controller's host ("zoomies updates status" says whether it is). The controller answers at once and the helper on its own time; follow the attempt with "zoomies updates status". Runners and jobs already running carry on through the restart. Without --version it takes the newest release that can be installed on the controller's system. `+
			`With --hosts or --host it updates the hosts instead, and not the controller: it starts a rollout that takes every host behind the controller's release (--hosts), or the hosts named (--host, by name or id, repeated or comma-separated), to that release, one host at a time, through each host's own update helper. A host's failed update halts the rollout until "zoomies updates resume" or "zoomies updates cancel". A rollout needs the admin role.`)
	cf := registerClientFlags(fs, false)
	version := fs.String("version", "", "the release to take, such as v1.3.5; left out, the newest the controller can install")
	allHosts := fs.Bool("hosts", false, "update every host behind the controller's release, one at a time, instead of the controller")
	var hostRefs listValue
	fs.Var(&hostRefs, "host", "update this host, by name or id, instead of the controller (repeatable, or comma-separated)")
	yes := fs.Bool("yes", false, "do not ask for confirmation; needed when there is no terminal to ask at")
	fs.example("zoomies updates apply", "zoomies updates apply --version v1.3.5 --yes",
		"zoomies updates apply --hosts", "zoomies updates apply --host vm-1 --host vm-2 --yes")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	said := 0
	for _, name := range []string{"version", "hosts", "host"} {
		if fs.changed(name) {
			said++
		}
	}
	if said > 1 {
		return usagef("updates apply", "--version, --hosts and --host say different things to update; give one. A rollout takes the hosts to the controller's own release, so it takes no --version")
	}
	if fs.changed("host") && len(hostRefs) == 0 {
		return usagef("updates apply", "--host needs a host's name or id, as zoomies hosts list shows; use --hosts for every host behind")
	}
	// --hosts=false is refused rather than read as no flag at all: falling
	// through would update the controller, which is the one thing the person
	// typing --hosts did not mean.
	if fs.changed("hosts") && !*allHosts {
		return usagef("updates apply", "--hosts=false names nothing to update; leave --hosts out to update the controller, or give --hosts to update every host behind")
	}
	if fs.changed("hosts") || fs.changed("host") {
		return applyRollout(ctx, e, cf, hostRefs, *yes)
	}
	// An untyped nil, so that a request without --version carries no body at all
	// and not the JSON null a nil map would encode to.
	var body any
	var tag string
	if fs.changed("version") {
		tag = strings.TrimSpace(*version)
		if tag == "" {
			return usagef("updates apply", "--version needs a release tag such as v1.3.5; leave it out to take the newest")
		}
		body = map[string]any{"tag": tag}
	}

	// Before the question, so that an address or a credential that cannot be used
	// is reported while the person can still put it right, and not after a yes.
	client, err := cf.client()
	if err != nil {
		return err
	}

	if !*yes {
		if !canAskAt(e.in, e.err) {
			return usagef("updates apply", "this asks the controller to update itself and restart, and there is no terminal to ask you at; run it again with --yes to say yes")
		}
		what := "the newest release the controller can install"
		if tag != "" {
			what = tag
		}
		fmt.Fprintf(e.err, "Ask the controller to update itself to %s? It restarts the controller's service through its root helper. [y/N] ", what)
		if !answeredYes(e.in) {
			fmt.Fprintln(e.out, "Nothing was asked of the controller.")
			return nil
		}
	}

	var st updatesStatus
	if _, err := client.post(ctx, "/updates/controller", nil, body, &st); err != nil {
		return plainAPIError(err)
	}
	a := st.Controller
	if a == nil {
		fmt.Fprintln(e.out, `The controller accepted the request. Follow it with "zoomies updates status".`)
		return nil
	}
	fmt.Fprintf(e.out, "The controller accepted attempt %s: %s to %s, %s. The helper answers on its own time; follow it with \"zoomies updates status\".\n",
		plain(a.ID), plain(a.From), plain(a.To), plain(a.State))
	return nil
}

// applyRollout is "updates apply --hosts" and "--host": start a rollout of every
// host behind, or of the hosts named. Like the controller's update it builds the
// client before anything else and asks before it sends; the names are resolved
// after the terminal is known to be there, so that a script without --yes is
// refused without a request, and before the question, so that a name that is no
// host is reported while the person can still put it right.
func applyRollout(ctx context.Context, e *env, cf *clientFlags, refs []string, yes bool) error {
	client, err := cf.client()
	if err != nil {
		return err
	}
	if !yes && !canAskAt(e.in, e.err) {
		return usagef("updates apply", "this starts a rollout that restarts each host's agent as it updates it, and there is no terminal to ask you at; run it again with --yes to say yes")
	}
	// An untyped nil for every host behind, so that the request carries no body,
	// which is what the controller reads as every host.
	var body any
	what := "every host behind the controller's release"
	if len(refs) > 0 {
		ids, names, err := resolveHosts(ctx, client, refs)
		if err != nil {
			return err
		}
		body = map[string]any{"host_ids": ids}
		what = strings.Join(names, ", ")
	}
	if !yes {
		fmt.Fprintf(e.err, "Start a rollout that updates %s to the controller's release, one host at a time? Each host's agent restarts through its root helper. [y/N] ", what)
		if !answeredYes(e.in) {
			fmt.Fprintln(e.out, "Nothing was asked of the controller.")
			return nil
		}
	}
	var st updatesStatus
	if _, err := client.post(ctx, "/updates/hosts", nil, body, &st); err != nil {
		return plainAPIError(err)
	}
	if st.Rollout == nil {
		fmt.Fprintln(e.out, `The controller started the rollout. Follow it with "zoomies updates status".`)
		return nil
	}
	fmt.Fprintf(e.out, "The controller started rollout %s. It updates one host at a time and halts if one fails; follow it with \"zoomies updates status\".\n", rolloutLine(st.Rollout))
	return nil
}

// resolveHosts turns what an operator typed into host ids, each once, accepting
// the name they know a host by or the id a log line quoted. The names it returns
// are for the question, and safe to print.
//
// An id, or a name typed exactly as one host's, is that host. Otherwise the name
// is matched without regard to case, and two hosts that it matches are refused
// rather than guessed between, because the guess restarts a host's agent.
func resolveHosts(ctx context.Context, client *apiClient, refs []string) (ids, names []string, err error) {
	var out listResponse[hostItem]
	if _, err := client.get(ctx, "/hosts", nil, &out); err != nil {
		return nil, nil, plainAPIError(err)
	}
	for _, ref := range refs {
		h, err := resolveHost(out.Items, ref)
		if err != nil {
			return nil, nil, err
		}
		if !slices.Contains(ids, h.ID) {
			ids, names = append(ids, h.ID), append(names, plain(h.Name))
		}
	}
	return ids, names, nil
}

// maxHostsNamed is how many hosts a refusal lists before it counts the rest: a
// fleet of hundreds in one line would bury the sentence that says what was wrong.
const maxHostsNamed = 10

func resolveHost(hosts []hostItem, ref string) (hostItem, error) {
	if i := slices.IndexFunc(hosts, func(h hostItem) bool { return h.ID == ref || h.Name == ref }); i >= 0 {
		return hosts[i], nil
	}
	var like []hostItem
	for _, h := range hosts {
		if strings.EqualFold(h.Name, ref) {
			like = append(like, h)
		}
	}
	switch {
	case len(like) == 1:
		return like[0], nil
	case len(like) > 1:
		var each []string
		for _, h := range like {
			each = append(each, fmt.Sprintf("%s (%s)", plain(h.Name), plain(h.ID)))
		}
		return hostItem{}, fmt.Errorf("%q could be any of %s; name the host by its id, or exactly as it is written", plain(ref), strings.Join(each, ", "))
	case len(hosts) == 0:
		return hostItem{}, fmt.Errorf("there are no hosts on this controller, so %q is not one", plain(ref))
	}
	var some []string
	for _, h := range hosts[:min(len(hosts), maxHostsNamed)] {
		some = append(some, plain(h.Name))
	}
	list := strings.Join(some, ", ")
	if more := len(hosts) - len(some); more > 0 {
		list += fmt.Sprintf(", and %d more (zoomies hosts list shows them all)", more)
	}
	return hostItem{}, fmt.Errorf("no host is called %q or has that id; this controller has: %s", plain(ref), list)
}

// answeredYes reads one line of the answer to a [y/N] question.
func answeredYes(in io.Reader) bool {
	line, _ := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// rolloutLine is a rollout in one line of the CLI's: which, to what, where it
// stands and how far it has got. Every part came from the controller.
func rolloutLine(ro *updatesRollout) string {
	line := fmt.Sprintf("%s to %s, %s: %d of %d hosts updated", plain(ro.ID), plain(ro.Target), plain(ro.State), ro.Done, ro.Total)
	if ro.Current != "" {
		line += ", now " + plain(ro.Current)
	}
	return line
}

func updatesResume(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates resume [--yes]",
		`Let a halted rollout of the hosts carry on. The host whose update halted it keeps its failure and waits out its retry, and the rollout goes on to the next host, so a host's agent restarts soon after. A rollout that is already running is left as it is. Needs the admin role. Follow it with "zoomies updates status".`)
	cf := registerClientFlags(fs, false)
	yes := fs.Bool("yes", false, "do not ask for confirmation; needed when there is no terminal to ask at")
	fs.example("zoomies updates resume", "zoomies updates resume --yes")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	if !*yes {
		if !canAskAt(e.in, e.err) {
			return usagef("updates resume", "this lets a rollout restart the next host's agent, and there is no terminal to ask you at; run it again with --yes to say yes")
		}
		fmt.Fprint(e.err, "Let the halted rollout carry on? The next host's agent restarts through its root helper. [y/N] ")
		if !answeredYes(e.in) {
			fmt.Fprintln(e.out, "Nothing was asked of the controller.")
			return nil
		}
	}
	var st updatesStatus
	if _, err := client.post(ctx, "/updates/rollout/resume", nil, nil, &st); err != nil {
		return plainAPIError(err)
	}
	if st.Rollout == nil {
		fmt.Fprintln(e.out, `The controller accepted the request. Follow the rollout with "zoomies updates status".`)
		return nil
	}
	fmt.Fprintf(e.out, "Rollout %s. Follow it with \"zoomies updates status\".\n", rolloutLine(st.Rollout))
	return nil
}

func updatesCancel(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies updates cancel",
		`Stop the rollout of the hosts, running or halted. Nothing new starts for it; an update a host's helper has already been handed finishes by itself and is recorded. In auto mode the controller does not start that release's rollout again by itself. It does not ask first, because it stops work and starts none. Needs the admin role.`)
	cf := registerClientFlags(fs, false)
	fs.example("zoomies updates cancel")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	var st updatesStatus
	if _, err := client.del(ctx, "/updates/rollout", nil, &st); err != nil {
		return plainAPIError(err)
	}
	if st.Rollout == nil {
		fmt.Fprintln(e.out, "The rollout is cancelled.")
		return nil
	}
	fmt.Fprintf(e.out, "Rollout %s. An update a host's helper was already handed finishes by itself; nothing new starts.\n", rolloutLine(st.Rollout))
	return nil
}

// plainAPIError makes a refusal safe to print and says which code it was. The
// message is the controller's sentence for the person to act on, so it is kept
// whole; what changes is that terminal control in it, or in its detail, is
// replaced, and that the stable code is named beside it, because a script and a
// support thread both go by the code. A 401 and a 403 are left to apiError,
// which knows what to say about a credential and a role.
func plainAPIError(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	safe := *ae
	safe.message, safe.detail, safe.field = plain(ae.message), plain(ae.detail), plain(ae.field)
	if ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden || ae.code == "" {
		return &safe
	}
	return fmt.Errorf("%w (%s)", &safe, plain(ae.code))
}

func updatesHelperInstall(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper install [--config-dir path]",
		`Install the update helper on this host. `+installer.UpdateHelperExplained+` It makes the update folder, owned by the account zoomies runs as, writes root's pointer to it in `+installer.UpdateHelperStateDir+`, and installs and starts the zoomies-update units, which run "zoomies updates helper run" as root when the service writes a request. Everything the helper would refuse is refused here first. Nothing installs the helper but its owner: the controller's update mode cannot.`)
	configDir := flags.String("config-dir", "", "the deployment's configuration directory, where zoomies.yaml and deployment.json are (default: "+config.ConfigDir()+")")
	flags.example("sudo zoomies updates helper install")
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	if err := installer.CheckUpdateHelperPlatform(); err != nil {
		return err
	}
	if uid := updatesEUID(); uid != 0 {
		return fmt.Errorf(`installing the update helper writes systemd units and a folder only root may, and this is uid %d; run "sudo zoomies updates helper install"`, uid)
	}
	opts, err := installer.ResolveHelperInstall(orConfigDir(*configDir))
	if err != nil {
		return err
	}
	opts.Out = e.out
	return installer.InstallUpdateHelper(ctx, opts)
}

func updatesHelperRemove(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper remove [--config-dir path]",
		`Stop the update helper and remove it: its units, root's state and pointer in `+installer.UpdateHelperStateDir+`, the pointer beside the configuration, and the files it and the installer put in the update folder. The folder goes too when nothing else is in it; which folder that is comes from root's copy of the pointer, or from the installed unit, and never from the copy the service can write. The trigger is turned off first, so no new update starts, and an update the helper is already running is let finish: remove refuses until it has, and is run again then. The controller stops offering updates for this host once the helper is gone.`)
	configDir := flags.String("config-dir", "", "the deployment's configuration directory (default: "+config.ConfigDir()+")")
	flags.example("sudo zoomies updates helper remove")
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	if err := installer.CheckUpdateHelperPlatform(); err != nil {
		return err
	}
	if uid := updatesEUID(); uid != 0 {
		return fmt.Errorf(`removing the update helper changes systemd units and folders only root may, and this is uid %d; run "sudo zoomies updates helper remove"`, uid)
	}
	// Where the update folder is comes from root's copy of the pointer; failing
	// that, from what root installed. A unit that is gone, or that runs as root,
	// leaves only root's copy to go by.
	opts, err := installer.ResolveHelperInstall(orConfigDir(*configDir))
	if err != nil {
		opts = installer.InstallHelperOptions{ConfigDir: orConfigDir(*configDir)}
	}
	opts.Out = e.out
	return installer.RemoveUpdateHelper(ctx, opts)
}

// orConfigDir is the configuration directory a flag names, or the default.
func orConfigDir(dir string) string {
	if dir != "" {
		return dir
	}
	return config.ConfigDir()
}

func updatesHelperRun(ctx context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper run",
		`Answer the request in the update folder: check it against the helper's limits, run "zoomies upgrade --version <tag> --non-interactive --config-dir <dir>" for it, with the deployment's configuration directory, and write result.json saying how it went. The zoomies-update unit runs it as root when a request arrives. It takes no flags: where the folder is, whose it is and which binary to run come from root's copy of the pointer in `+installer.UpdateHelperStateDir+`, which only root can write.`)
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	if err := installer.CheckUpdateHelperPlatform(); err != nil {
		return err
	}
	if uid := updatesEUID(); uid != 0 {
		return fmt.Errorf(`the update helper runs as root, started by the zoomies-update unit when the service asks for an update, and this is uid %d; to update this host by hand, run "sudo zoomies upgrade"`, uid)
	}
	opts, err := installer.HelperOptionsFromPointer(updateHelperStateDir)
	if err != nil {
		return err
	}
	opts.Out = e.out
	return installer.RunUpdateHelper(ctx, opts)
}

func updatesHelperStatus(_ context.Context, e *env, args []string) error {
	flags := newFlagSet(e, "zoomies updates helper status",
		"Where this host's update folder is, whether the helper is installed, and the last result it wrote with the end of its log. It reads root's copy of the pointer in "+installer.UpdateHelperStateDir+" when it can and the one beside the configuration file when it cannot, so it works without root wherever those can be read.")
	flags.example("sudo zoomies updates helper status")
	if err := flags.parse(args); err != nil {
		return err
	}
	if err := flags.noMoreArgs(); err != nil {
		return err
	}
	p, source, err := helperPointer()
	if err != nil {
		return err
	}
	if source == "" {
		fmt.Fprintf(e.out, "The update helper is not installed on this host: there is no %s in %s or %s. Install it with \"sudo zoomies updates helper install\".\n",
			channel.PointerFile, updateHelperStateDir, config.ConfigDir())
		return nil
	}

	// Everything below the pointer was written by the service, or could have
	// been, so it is printed as text and never as terminal control.
	row := func(label, value string) { fmt.Fprintf(e.out, "%-14s %s\n", label, plain(value)) }
	row("Update folder", p.Dir)
	row("Recorded in", source)
	row("Account", fmt.Sprintf("%s (uid %d)", p.Account, p.UID))
	row("Binary", p.Binary)

	switch m, found, err := channel.ReadMarker(p.Dir); {
	case err != nil:
		row("Helper", "cannot read "+channel.MarkerFile+": "+err.Error())
	case !found:
		row("Helper", "not installed: there is no "+channel.MarkerFile+" in the folder; install it with \"sudo zoomies updates helper install\"")
	default:
		row("Helper", fmt.Sprintf("installed %s, by zoomies %s", m.InstalledAt.UTC().Format(time.RFC3339), m.Version))
	}

	r, found, err := channel.ReadResult(p.Dir)
	switch {
	case err != nil:
		row("Last result", "cannot read "+channel.ResultFile+": "+err.Error())
		return nil
	case !found:
		row("Last result", "none yet")
		return nil
	}
	var outcome string
	switch {
	case r.OK && r.To != "":
		outcome = fmt.Sprintf("updated from %s to %s", r.From, r.To)
	case r.OK:
		outcome = fmt.Sprintf("%s was already installed (%s), so nothing ran", r.Tag, r.From)
	default:
		outcome = "failed: " + r.Error
	}
	label := r.Tag
	if r.ID != "" {
		label = r.ID + " for " + r.Tag
	}
	if label == "" {
		label = "a request the helper could not read"
	}
	row("Last result", label+": "+outcome)
	row("Finished", r.FinishedAt.UTC().Format(time.RFC3339))
	if tail := strings.TrimRight(r.LogTail, "\n"); tail != "" {
		lines := strings.Split(tail, "\n")
		if len(lines) > helperStatusTailLines {
			lines = lines[len(lines)-helperStatusTailLines:]
		}
		fmt.Fprintln(e.out, "Log tail:")
		for _, line := range lines {
			fmt.Fprintf(e.out, "  %s\n", plain(line))
		}
	}
	return nil
}

// helperPointer finds the pointer to the update folder and says which file it
// read. Root's copy comes first, because only root can have written it; an
// account that cannot read it reads the copy beside the configuration file,
// which the installer writes too. No pointer at all is an empty source and no
// error: the helper is not installed.
func helperPointer() (channel.Pointer, string, error) {
	var denied error
	for _, dir := range []string{updateHelperStateDir, config.ConfigDir()} {
		path := filepath.Join(dir, channel.PointerFile)
		p, err := channel.ReadPointer(path)
		switch {
		case err == nil:
			return p, path, nil
		case errors.Is(err, fs.ErrNotExist):
		case errors.Is(err, fs.ErrPermission):
			denied = err
		default:
			return channel.Pointer{}, "", fmt.Errorf("cannot read %s: %w", path, err)
		}
	}
	if denied != nil {
		return channel.Pointer{}, "", fmt.Errorf(`%w; run "sudo zoomies updates helper status" to read root's copy`, denied)
	}
	return channel.Pointer{}, "", nil
}
