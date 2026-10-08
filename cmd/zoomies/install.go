package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/installer"
	"github.com/eyupio/zoomies/internal/logging"
	"github.com/eyupio/zoomies/internal/version"
	"golang.org/x/term"
)

// runInit is `zoomies init`: the interactive setup install.sh hands off to.
//
// Every --detected-* flag is something the shell script already worked out on
// this host moments ago. They are accepted rather than re-derived because two
// answers to the same question is worse than one, even when the second one is
// arrived at honestly.
func runInit(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies init [flags]",
		"Set this host up: how it runs, backend, listener, GitHub App and the first administrator.")

	tune := fs.Bool("tune", false, "explicitly approve recommended safe tuning after a fresh install")
	noTune := fs.Bool("no-tune", false, "do not offer or apply tuning")
	mode := fs.String("mode", "", "single (a controller with an embedded agent), controller, or agent; empty asks")
	deployment := fs.String("deployment", "", "native (the binary under systemd or launchd), compose (a docker-compose.yml and a populated .env) or docker (one container); empty asks, offering only what this host can run")
	controllerURL := fs.String("controller", "", "for --mode agent: the controller to join")
	joinToken := fs.String("join-token", "", "for --mode agent: a join token from the UI")
	externalURL := fs.String("external-url", "", "for a controller: the public hostname or URL browsers and GitHub use; a bare hostname implies https://")
	port := fs.Int("port", 0, "host port for the controller; 0 asks interactively or uses the detected default")
	answers := fs.String("answers", "", "a YAML answer file for unattended setup; implies --non-interactive")
	nonInteractive := fs.Bool("non-interactive", false, "never prompt; a missing answer is an error naming the key")
	noAnimation := fs.Bool("no-animation", false, "skip the short branded terminal splash")
	assumeYes := fs.Bool("yes", false, "accept the confirmations that are not destructive")
	printAnswers := fs.Bool("print-answers", false, "write an annotated example answer file to stdout and exit")

	configDir := fs.String("config-dir", "", "where zoomies.yaml and the encryption key go (default: "+config.ConfigDir()+")")
	stateDir := fs.String("state-dir", "", "where the database and runner scratch space go (default: "+config.StateDir()+")")
	installedBinary := fs.String("installed-binary", "", "where the binary lives, which is what the service unit will exec")

	detectedOS := fs.String("detected-os", "", "what install.sh found: the operating system")
	detectedArch := fs.String("detected-arch", "", "what install.sh found: the CPU architecture")
	detectedDistro := fs.String("detected-distro", "", "what install.sh found: the distribution")
	detectedInit := fs.String("detected-init", "", "what install.sh found: the init system")
	detectedRuntime := fs.String("detected-runtime", "", "what install.sh found: the container runtime")
	detectedSocket := fs.String("detected-socket", "", "what install.sh found: the runtime's socket")
	detectedRootless := fs.Bool("detected-rootless", false, "what install.sh found: the runtime is rootless")
	detectedCompose := fs.String("detected-compose", "", "what install.sh found: the compose command, e.g. \"docker compose\"")

	fs.example(
		"zoomies init",
		"zoomies init --deployment compose",
		"zoomies init --mode agent --controller https://zoomies.example.com --join-token zoojoin_...",
		"zoomies init --print-answers > answers.yaml",
		"zoomies init --non-interactive --answers answers.yaml",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	if *tune && *noTune {
		return usagef("init", "--tune and --no-tune cannot be combined")
	}
	// Printing the example is not setup; it is the thing an operator does
	// before setup, and it must not touch the host.
	if *printAnswers {
		return installer.WriteExample(e.out)
	}

	parsedMode, err := installer.ParseMode(*mode)
	if err != nil {
		return err
	}
	parsedDeployment, err := installer.ParseDeployment(*deployment)
	if err != nil {
		return err
	}

	// Text at warn level: the installer talks to the operator in prose, and a
	// stream of JSON in the middle of it would be unreadable.
	log := logging.Setup(logging.Options{Level: "warn", Format: "text"})

	interactive := false
	if f, ok := e.in.(*os.File); ok {
		interactive = term.IsTerminal(int(f.Fd())) && !*nonInteractive && *answers == ""
	}
	inst, err := installer.New(installer.Options{
		AfterHostSetup: func(ctx context.Context, fresh bool, work string) error {
			return afterHostSetup(ctx, e, fresh, *tune, *noTune, interactive, work)
		},
		DetectedOS:       *detectedOS,
		DetectedArch:     *detectedArch,
		DetectedDistro:   *detectedDistro,
		DetectedInit:     *detectedInit,
		DetectedRuntime:  *detectedRuntime,
		DetectedSocket:   *detectedSocket,
		DetectedRootless: *detectedRootless,
		DetectedCompose:  *detectedCompose,
		InstalledBinary:  *installedBinary,
		Mode:             parsedMode,
		Deployment:       parsedDeployment,
		ControllerURL:    *controllerURL,
		JoinToken:        *joinToken,
		ExternalURL:      *externalURL,
		Port:             *port,
		AnswersFile:      *answers,
		NonInteractive:   *nonInteractive || *answers != "",
		AssumeYes:        *assumeYes,
		ConfigDir:        *configDir,
		StateDir:         *stateDir,
		Out:              e.out,
		In:               e.in,
		Logger:           log,
	})
	if err != nil {
		return err
	}
	if !*noAnimation && !*nonInteractive && *answers == "" {
		if err := installer.Splash(ctx, e.out, "Install"); err != nil {
			return err
		}
	}
	return inst.Run(ctx)
}

// runUninstall is `zoomies uninstall`: take Zoomies off this host, having first
// deregistered its runners from GitHub while the credentials to do it still
// exist.
func runUninstall(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies uninstall [--yes]",
		"Remove Zoomies from this host: the service or container, the database, the encryption key and the configuration.")
	yes := fs.Bool("yes", false, "do not ask for confirmation; the summary is printed either way")
	nonInteractive := fs.Bool("non-interactive", false, "never prompt, which means nothing is removed without --yes")
	keepConfig := fs.Bool("keep-config", false, "leave zoomies.yaml in place")
	deregister := fs.Bool("deregister", true, "deregister this instance's runners from GitHub first; without it they become orphans somebody deletes by hand")
	volumes := fs.Bool("volumes", false, "for a compose or docker deployment: delete the data volume too, which destroys the database; asked when not given")
	binary := fs.String("binary", "", "also remove the binary at this path")
	serviceUser := fs.String("service-user", "", "the service account to remove (default: zoomies, when running as root)")
	configDir := fs.String("config-dir", "", "where zoomies.yaml and the encryption key live (default: "+config.ConfigDir()+")")
	stateDir := fs.String("state-dir", "", "where the database lives (default: "+config.StateDir()+")")
	fs.example(
		"zoomies uninstall",
		"zoomies uninstall --yes --keep-config",
		"zoomies uninstall --yes --volumes",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	// Deregistration is a tri-state: asked about unless the operator said.
	// flag.Bool cannot express that, so "was it typed" is the third state.
	var wanted *bool
	if fs.changed("deregister") {
		wanted = deregister
	}
	// The volume holds the database, so "not mentioned" must mean "ask", never
	// "delete it".
	var wantVolume *bool
	if fs.changed("volumes") {
		wantVolume = volumes
	}

	return installer.Uninstall(ctx, installer.UninstallOptions{
		ConfigDir:      *configDir,
		StateDir:       *stateDir,
		BinaryPath:     *binary,
		ServiceUser:    *serviceUser,
		Yes:            *yes,
		NonInteractive: *nonInteractive,
		Deregister:     wanted,
		RemoveVolume:   wantVolume,
		KeepConfig:     *keepConfig,
		Out:            e.out,
		In:             e.in,
		Logger:         logging.Setup(logging.Options{Level: "warn", Format: "text"}),
	})
}

// runUpgrade is the second half of install.sh --upgrade. The script verifies
// and installs the new binary before this applies it to the existing service.
func runUpgrade(ctx context.Context, e *env, args []string) error {
	return runUpgradeNamed(ctx, e, args, "upgrade")
}

// Older scripts keep working, but every update spelling uses one upgrade flow.
func runUpdate(ctx context.Context, e *env, args []string) error {
	return runUpgradeNamed(ctx, e, args, "update")
}

func runUpgradeNamed(ctx context.Context, e *env, args []string, name string) error {
	fs := newFlagSet(e, "zoomies "+name+" [flags]", "Upgrade this host: binary, controller or agent, and cached runner images.")
	configDir := fs.String("config-dir", "", "directory containing zoomies.yaml and deployment.json")
	binary := fs.String("installed-binary", "", "the binary path used by the existing service")
	dockerHost := fs.String("docker-host", "", "the existing container runtime endpoint")
	runtime := fs.String("runtime", "", "docker or podman; empty uses the saved deployment")
	image := fs.String("image", "", "replacement image for a custom container deployment")
	mode := fs.String("mode", "", "select agent, controller or single; empty detects installed services")
	check := fs.Bool("check", false, "check the deployment without changing or restarting anything")
	wantVersion := fs.String("version", "", "target a published tag such as v1.4.0, or dev")
	noDownload := fs.Bool("no-download", false, "apply the binary that is already installed; do not look for a newer one")
	yes := fs.Bool("yes", false, "approve deployment additions and settings migration; never OS tuning")
	nonInteractive := fs.Bool("non-interactive", false, "never prompt; optional deployment changes require --yes")
	noAnimation := fs.Bool("no-animation", false, "skip the short branded terminal splash")
	fs.example("sudo zoomies upgrade", "zoomies upgrade --check", "sudo zoomies upgrade --yes", "sudo zoomies upgrade --mode agent --version v1.4.0")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if *wantVersion != "" && *noDownload {
		return usagef(name, "--version requires downloading; remove --no-download")
	}
	parsed, err := installer.ParseMode(*mode)
	if err != nil {
		return err
	}
	interactive := false
	if f, ok := e.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		interactive = !*nonInteractive && isTerminal(e.out)
	}
	opts := installer.UpgradeOptions{
		Doctor:    func(ctx context.Context, cfg *config.Config) { upgradeDoctor(ctx, e, cfg) },
		ConfigDir: *configDir, BinaryPath: *binary, DockerHost: *dockerHost, Runtime: *runtime, Image: *image,
		Mode: parsed, Check: *check, Out: e.out, Continuation: true,
		In: e.in, Interactive: interactive, NonInteractive: !interactive, AssumeYes: *yes,
	}
	ui := installer.PaletteFor(e.out)
	if os.Getenv("ZOOMIES_UPGRADE_STARTED") == "" {
		if !*noAnimation && !*nonInteractive && !*check {
			if err := installer.Splash(ctx, e.out, "Upgrade"); err != nil {
				return err
			}
		}
		title := "Zoomies upgrade"
		if *check {
			title = "Zoomies upgrade preview"
		}
		ui.Title(e.out, title, "this host")
	}
	if !*check && !*noDownload && os.Getenv(installer.SelfUpdateEnv) == "" {
		// Validate the deployment before replacing the executable. An invalid
		// record or service must leave the installed binary untouched.
		preview := opts
		preview.Check, preview.Out, preview.Doctor = true, io.Discard, nil
		if err := installer.Upgrade(ctx, preview); err != nil {
			return err
		}
		ui.Rule(e.out, "1/4 Binary")
		selector := newDeploymentSelector(*configDir, *dockerHost, *runtime, *image, *mode, *yes)
		if err := selfUpdate(ctx, e, *binary, *wantVersion, selector); err != nil {
			return err
		}
	} else if !*check && os.Getenv("ZOOMIES_UPGRADE_STARTED") == "" {
		ui.Rule(e.out, "1/4 Binary")
		ui.Done(e.out, "Using the installed binary")
	}
	return installer.Upgrade(ctx, opts)
}

// deploymentSelector is the part of `zoomies upgrade`'s command line that says
// which deployment is meant, as opposed to what to do with it. The downloaded
// release is handed exactly these, so it checks the deployment this run would
// have upgraded and not whichever one it would find on its own.
type deploymentSelector struct {
	ConfigDir, DockerHost, Runtime, Image, Mode string
	// AssumeYes is the operator's own --yes. The in-process preview has
	// already honoured it, and a check changes nothing, so the candidate is
	// given it too; otherwise it would refuse a change the operator approved.
	// Nothing sets it for an unattended caller that never passes --yes.
	AssumeYes bool
}

func newDeploymentSelector(configDir, dockerHost, runtime, image, mode string, assumeYes bool) deploymentSelector {
	return deploymentSelector{ConfigDir: configDir, DockerHost: dockerHost, Runtime: runtime, Image: image, Mode: mode, AssumeYes: assumeYes}
}

func (d deploymentSelector) args() []string {
	var out []string
	for _, f := range []struct{ flag, value string }{
		{"--config-dir", d.ConfigDir}, {"--docker-host", d.DockerHost}, {"--runtime", d.Runtime},
		{"--image", d.Image}, {"--mode", d.Mode},
	} {
		if f.value != "" {
			out = append(out, f.flag, f.value)
		}
	}
	return out
}

// candidateTimeout bounds the pre-flight. The candidate under --check also runs
// the doctor's probes, and a container runtime that accepts a connection and
// never answers would otherwise hold an upgrade for ever -- with nobody at an
// unattended run to press Ctrl-C. Five minutes is far more than any check takes
// and far less than the helper's own attempt limit.
var candidateTimeout = 5 * time.Minute

// candidateOutputLimit is how much of the candidate's output an error carries.
// What matters is the end -- the refusal comes last -- and an error is read by
// a person, not archived.
const candidateOutputLimit = 4096

// candidateRefusal is the downloaded release exiting non-zero from its check:
// it ran, and it will not take this deployment as it stands. That differs from
// a candidate that hung or could not start, which says nothing about the
// deployment, and the advice for the two is not the same.
type candidateRefusal struct{ err error }

func (r *candidateRefusal) Error() string { return r.err.Error() }
func (r *candidateRefusal) Unwrap() error { return r.err }

// tailBuffer keeps the last max bytes written to it and remembers that it
// dropped some, so a chatty child costs bounded memory.
type tailBuffer struct {
	max     int
	b       []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		n := copy(t.b, t.b[len(t.b)-t.max:])
		t.b, t.dropped = t.b[:n], true
	}
	return len(p), nil
}

// String is the kept tail, cut on a character boundary and marked with "..."
// when the beginning was dropped.
func (t *tailBuffer) String() string {
	start := 0
	if t.dropped {
		for start < len(t.b) && !utf8.RuneStart(t.b[start]) {
			start++
		}
	}
	text := strings.TrimSpace(string(t.b[start:]))
	if t.dropped {
		return "..." + text
	}
	return text
}

// runCandidate runs the downloaded release with an explicit environment and
// returns an error that carries the end of what it printed. It is a variable so
// a test can see what the candidate was asked without running it.
var runCandidate = func(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	// A grandchild that outlives the candidate holds the output pipe open, so
	// Wait is given a limit rather than waiting on it for ever.
	cmd.WaitDelay = 10 * time.Second
	out := &tailBuffer{max: candidateOutputLimit}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err != nil {
		if text := out.String(); text != "" {
			return fmt.Errorf("%w: %s", err, text)
		}
	}
	return err
}

// candidatePreflight asks the downloaded release to check this deployment with
// its own expectations, while the binary that is known to work is still the one
// installed. A release can need something the running one does not know to
// ask for -- a folder, a setting -- and finding that out after the swap leaves
// a host on a binary that refuses to upgrade it.
//
// --installed-binary is what makes this work: without it the candidate would
// take os.Executable() for the binary the service runs, which is its own
// temporary path, and refuse the deployment for pointing at the wrong file.
// --check and --non-interactive keep it from changing or asking anything.
// --yes is passed only when the operator typed it (see AssumeYes), so the
// unattended helper, which never does, never approves anything this way.
//
// These flags are hard-coded here, in the older binary, for a newer one: a
// release that renames or removes --installed-binary, --non-interactive or
// --no-download has to keep accepting them, or every upgrade from a version
// that has this code stops at its pre-flight.
func candidatePreflight(installed string, sel deploymentSelector) func(context.Context, string) error {
	return func(ctx context.Context, candidate string) error {
		args := []string{"upgrade", "--check", "--non-interactive", "--no-download", "--installed-binary", installed}
		if sel.AssumeYes {
			args = append(args, "--yes")
		}
		args = append(args, sel.args()...)
		// The marker keeps the candidate from printing its own title: what it
		// prints is only shown if it refuses.
		env := append(os.Environ(), installer.SelfUpdateEnv+"=1", "ZOOMIES_UPGRADE_STARTED=1")
		tctx, cancel := context.WithTimeout(ctx, candidateTimeout)
		defer cancel()
		err := runCandidate(tctx, env, candidate, args...)
		if err == nil {
			return nil
		}
		if tctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return fmt.Errorf("the downloaded release did not finish checking this deployment within %s; check that the container runtime answers, then run `zoomies upgrade` again: %w", candidateTimeout, err)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &candidateRefusal{err}
		}
		return err
	}
}

// selfUpdate brings the installed binary up to date and, if it changed,
// restarts this command inside the new one. Upgrade used to apply whatever was
// already on disk, so a host that ran `zoomies upgrade` rather than install.sh
// kept the old build -- and with it the old upgrade, doctor and tune.
//
// A failed download stops the upgrade; an offline host can explicitly use
// --no-download rather than report a partial upgrade as successful.
func selfUpdate(ctx context.Context, e *env, binary, wantVersion string, sel deploymentSelector) error {
	if binary == "" {
		binary, _ = os.Executable()
	}
	if abs, err := filepath.Abs(binary); err == nil {
		binary = abs
	}
	ui := installer.PaletteFor(e.out)
	ui.Doing(e.out, "Checking for a newer Zoomies")
	res, err := installer.SelfUpdate(ctx, installer.SelfUpdateOptions{
		BinaryPath: binary, Version: wantVersion, Current: version.Version, Out: e.out,
		Preflight: candidatePreflight(binary, sel),
	})
	var refusal *candidateRefusal
	switch {
	case errors.As(err, &refusal) && sel.AssumeYes:
		return fmt.Errorf("binary update stopped: %w; the new release needs a change this version could not see. Follow the advice quoted above, then run `zoomies upgrade` again", err)
	case errors.As(err, &refusal):
		return fmt.Errorf("binary update stopped: %w; the new release needs a change this version could not see. Run `zoomies upgrade --yes` to approve it, or follow the advice quoted above", err)
	case err != nil:
		return fmt.Errorf("binary update failed: %w; retry, or use --no-download to apply the installed binary", err)
	}
	if !res.Updated {
		if res.Tag != "" {
			ui.Done(e.out, "Already on the latest build %s", ui.Dim("("+res.Tag+")"))
		}
		return nil
	}
	ui.Done(e.out, "Downloaded %s, checksum verified; continuing with it", res.Tag)
	fmt.Fprintln(e.out)
	env := append(os.Environ(), installer.SelfUpdateEnv+"=1", "ZOOMIES_UPGRADE_STARTED=1")
	if err := reexecBinary(binary, processArgs(), env); err != nil {
		return fmt.Errorf("the binary was updated to %s but could not be started: %w; run `zoomies upgrade` again", res.Tag, err)
	}
	return nil
}
