package installer

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/naming"
)

// UpgradeOptions selects an existing deployment. Upgrade never enrols a host,
// generates credentials, or rewrites its configuration from installer defaults.
type UpgradeOptions struct {
	// Doctor reports only; it has no tuning path.
	Doctor     func(context.Context, *config.Config)
	ConfigDir  string
	BinaryPath string
	DockerHost string
	Runtime    string
	Image      string
	Mode       Mode
	Check      bool
	// Continuation keeps the binary-download half and service half under one heading.
	Continuation bool
	Out          io.Writer
	// In is where an answer to "add them now?" is read from, and Interactive
	// says somebody is there to give one. AssumeYes is --yes: approval given
	// in advance. With neither, what this release expects and the deployment
	// lacks is reported and left alone. NonInteractive says nobody will be
	// there on the real run either, which lets --check refuse an upgrade that
	// could only stop half way.
	In             io.Reader
	Interactive    bool
	NonInteractive bool
	AssumeYes      bool
	proxmoxRoot    string // injected gateway root for tests
	run            commandRunner
	runInput       inputRunner
	// shared stands in for the host's shared folder and the account it
	// belongs to, so a test does not reach for /var/lib/zoomies.
	shared *sharedTarget
	// socketGroup stands in for reading the group that owns the runtime's
	// socket, which is the test host's own otherwise.
	socketGroup func(path string) int
	// serveTimeout, servePoll and progressEvery shorten the wait for the
	// controller to answer, which is minutes long outside a test.
	serveTimeout, servePoll, progressEvery time.Duration
}

type upgradePlan struct {
	opts        UpgradeOptions
	record      DeploymentRecord
	image       string
	unit        string
	nativeUnits []string
	launchd     bool
	client      *backend.APIClient
	replacement *backend.ContainerReplacement
	// move is the settings the operator agreed to take out of the
	// container's environment and into its database; see envsettings.go.
	move []movedSetting
}

// Upgrade applies the binary already downloaded by install.sh to the running
// deployment, and refreshes its stock runner images without removing workloads.
func Upgrade(ctx context.Context, opts UpgradeOptions) error {
	if opts.Runtime != "" && opts.Runtime != "docker" && opts.Runtime != "podman" {
		return fmt.Errorf("installer: --runtime must be docker or podman")
	}
	if opts.ConfigDir == "" {
		opts.ConfigDir = config.ConfigDir()
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.run == nil {
		opts.run = runCommand
	}
	if opts.runInput == nil {
		opts.runInput = runCommandInput
	}
	if opts.BinaryPath == "" {
		opts.BinaryPath, _ = os.Executable()
	}
	opts.BinaryPath = upgradeBinaryPath(opts.BinaryPath)
	if !opts.Check {
		lockPath := filepath.Join(opts.ConfigDir, "upgrade.lock")
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("installer: cannot reserve this deployment for upgrade: %w; if an earlier upgrade was interrupted, remove %s only after checking it has stopped", err, lockPath)
		}
		_ = lock.Close()
		defer os.Remove(lockPath)
	}
	p, err := prepareUpgrade(ctx, opts)
	if err != nil {
		return err
	}
	ui := PaletteFor(opts.Out)
	if !opts.Continuation {
		ui.Title(opts.Out, "Zoomies upgrade", p.describe())
	}
	if opts.Check {
		ui.Rule(opts.Out, "Deployment preview")
	} else {
		ui.Rule(opts.Out, "2/4 Deployment")
	}
	ui.Hint(opts.Out, "Target: %s", p.describe())
	if err := p.settleLayout(ctx); err != nil {
		return err
	}
	p.settleSettings(ctx)
	if opts.Check {
		if err := p.upgradeProxmoxGateways(ctx); err != nil {
			return err
		}
		ui.Done(opts.Out, "Deployment checks passed; no changes made")
		if opts.Doctor != nil {
			opts.Doctor(ctx, p.settings(ctx).cfg)
		}
		return nil
	}
	beforeImage := p.localImageID(ctx)
	// The tag can have been pulled already -- by an earlier `docker pull`, a
	// check, or an upgrade whose restart failed -- while the service still
	// runs something older. Comparing the tag alone then says nothing changed
	// on an upgrade that moved the service and migrated its database.
	beforeRunning := p.runningImageID(ctx)
	serving := p.serveCheck(ctx)
	if err := p.pullRunnerImages(ctx); err != nil {
		return err
	}
	switch p.record.Deployment {
	case DeploymentCompose:
		err = p.upgradeCompose(ctx)
	case DeploymentDocker:
		err = p.upgradeDocker(ctx)
	default:
		for _, unit := range p.nativeUnits {
			ui.Doing(opts.Out, "Restarting %s", shortUnit(unit))
			if p.launchd {
				_, err = opts.run(ctx, "launchctl", "kickstart", "-k", unit)
			} else {
				_, err = opts.run(ctx, "systemctl", "restart", unit)
				if err == nil {
					_, err = opts.run(ctx, "systemctl", "is-active", "--quiet", unit)
				}
			}
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("installer: the upgrade did not finish: %w", err)
	}
	if err := p.upgradeProxmoxGateways(ctx); err != nil {
		return err
	}
	if err := p.restartHealthReporter(ctx); err != nil {
		return err
	}
	ui.Rule(opts.Out, "3/4 Verify")
	if err := p.waitServing(ctx, serving); err != nil {
		return fmt.Errorf("installer: %w", err)
	}
	if !p.record.Deployment.Containerised() {
		ui.Done(opts.Out, "Native services are active")
	}
	if p.record.Deployment.Containerised() {
		afterImage := p.localImageID(ctx)
		reportImage(opts.Out, p.image, beforeImage, afterImage, beforeRunning, p.runningImageID(ctx))
	}
	ui.Rule(opts.Out, "4/4 Host health")
	if opts.Doctor != nil {
		opts.Doctor(ctx, p.settings(ctx).cfg)
	} else {
		ui.Hint(opts.Out, "Review host health with: zoomies doctor")
	}
	fmt.Fprintln(opts.Out)
	ui.Done(opts.Out, "%s", ui.Bold("Upgrade complete."))
	return nil
}

// A container's read-only reporter is a separate native process. Replacing its
// executable alone leaves the old checks running until the service restarts.
func (p *upgradePlan) restartHealthReporter(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	state, err := p.opts.run(ctx, "systemctl", "show", HostHealthUnit, "--property=ActiveState", "--value")
	if err != nil || strings.TrimSpace(state) != "active" {
		return nil
	}
	start, err := p.opts.run(ctx, "systemctl", "show", HostHealthUnit, "--property=ExecStart", "--value")
	if err != nil {
		return fmt.Errorf("installer: cannot inspect the running host health reporter: %w", err)
	}
	if !strings.Contains(start, "path="+p.opts.BinaryPath+" ;") && !strings.Contains(start, "path="+p.opts.BinaryPath+";") {
		PaletteFor(p.opts.Out).Warn(p.opts.Out, "Host health reporter uses another binary; upgrade it separately")
		return nil
	}
	PaletteFor(p.opts.Out).Doing(p.opts.Out, "Restarting the read-only host health reporter")
	if _, err := p.opts.run(ctx, "systemctl", "restart", HostHealthUnit); err != nil {
		return fmt.Errorf("installer: service updated, but the host health reporter could not restart: %w", err)
	}
	return nil
}

// describe names what is being upgraded in the words an operator would use.
func (p *upgradePlan) describe() string {
	switch {
	case p.record.Deployment == DeploymentCompose:
		return "the Compose deployment"
	case p.record.Deployment == DeploymentDocker:
		return "the Docker deployment"
	case len(p.nativeUnits) > 1:
		return "native controller and agent"
	case p.unit != "":
		return shortUnit(p.unit)
	}
	return "this host"
}

// shortUnit drops the launchd domain prefix, which means nothing to a reader.
func shortUnit(unit string) string {
	if i := strings.LastIndex(unit, "/"); i >= 0 {
		return unit[i+1:]
	}
	return unit
}

// settleLayout says what this release expects that the deployment lacks, and
// adds it when the operator approves: --yes, or an answer at the terminal.
// Unattended, it adds nothing and says how to; see layout.go.
func (p *upgradePlan) settleLayout(ctx context.Context) error {
	changes, err := p.layoutChanges(ctx)
	if err != nil || len(changes) == 0 {
		return err
	}
	out := p.opts.Out
	fmt.Fprintln(out, "Deployment additions to review:")
	required := ""
	for _, c := range changes {
		fmt.Fprintln(out, "  - "+c.what)
		if c.required && required == "" {
			required = c.what
		}
	}
	refuse := func() error {
		return fmt.Errorf("installer: the upgrade cannot go on without this: %s; run `zoomies upgrade --yes` to add it, or `zoomies init` to set the deployment up again", required)
	}
	approved := false
	switch {
	case p.opts.Check:
		// install.sh replaces the binary between the check and the real
		// run, so a run that is bound to stop has to stop here, while the
		// old binary is still the installed one.
		if required != "" && p.opts.NonInteractive && !p.opts.AssumeYes {
			return refuse()
		}
		fmt.Fprintln(out, "The upgrade will offer to add these; approval is required.")
		return nil
	case p.opts.AssumeYes:
		approved = true
	case p.opts.Interactive && p.opts.In != nil:
		approved = askApproval(p.opts.In, out, "Add them now? [Y/n] ")
	}
	if !approved {
		if required != "" {
			return refuse()
		}
		fmt.Fprintln(out, "Optional additions skipped. Continuing the upgrade.")
		fmt.Fprintln(out, "Add later: sudo zoomies upgrade --yes")
		return nil
	}
	for _, c := range changes {
		if err := c.apply(ctx); err != nil {
			return fmt.Errorf("installer: could not %s: %w", c.what, err)
		}
	}
	fmt.Fprintf(out, "Added %d change(s) this release expects.\n", len(changes))
	return nil
}

// askApproval reads one answer. An empty line is yes, as the [Y/n] says; an
// input that ends before anything is typed is no, because nobody said yes.
func askApproval(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprint(out, question)
	var line strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line.WriteByte(buf[0])
		}
		if err != nil {
			if line.Len() == 0 {
				fmt.Fprintln(out)
				return false
			}
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(line.String())) {
	case "", "y", "yes":
		return true
	}
	return false
}

func (p *upgradePlan) localImageID(ctx context.Context) string {
	if !p.record.Deployment.Containerised() || p.image == "" {
		return ""
	}
	out, err := p.docker(ctx, "image", "inspect", "--format", "{{.Id}}", p.image)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// runningImageID is the image the service's container was created from, which
// is what an upgrade actually changes; the tag is only what it will be
// created from next.
func (p *upgradePlan) runningImageID(ctx context.Context) string {
	if !p.record.Deployment.Containerised() {
		return ""
	}
	out, err := p.docker(ctx, "inspect", "--format", "{{.Image}}", containerOr(p.record))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Compare the pulled image with the container, not the tag before pulling:
// an earlier pull may already have advanced the tag without updating the
// service. Only a running container can make this upgrade a no-op.
func (p *upgradePlan) keepCurrentContainer(ctx context.Context) (bool, error) {
	image := p.localImageID(ctx)
	if image == "" || image != p.runningImageID(ctx) {
		return false, nil
	}
	running, err := p.docker(ctx, "inspect", "--format", "{{.State.Running}}", containerOr(p.record))
	if err != nil || strings.TrimSpace(running) != "true" {
		return false, nil
	}
	// An explicit image reference can change while resolving to the same
	// build. Remember it for subsequent upgrades without disrupting service.
	if _, _, err := replaceEnvImage(p.record.EnvFile, p.image); err != nil {
		return true, err
	}
	p.record.Image = p.image
	if _, err := WriteDeploymentRecord(p.opts.ConfigDir, p.record); err != nil {
		return true, err
	}
	PaletteFor(p.opts.Out).Done(p.opts.Out, "Service image unchanged; keeping the running container")
	return true, nil
}

// reportImage says what the upgrade did to the image the service runs, and
// separately whether the channel it follows moved, because those differ
// whenever the tag was pulled before the service was recreated from it.
func reportImage(out io.Writer, image, tagBefore, tagAfter, runBefore, runAfter string) {
	ui := PaletteFor(out)
	switch {
	case runBefore != "" && runAfter != "" && runBefore != runAfter:
		ui.Done(out, "Service image updated: %s -> %s", shortImageID(runBefore), shortImageID(runAfter))
	case runBefore != "" && runBefore == runAfter:
		ui.Done(out, "Service image unchanged: %s", shortImageID(runAfter))
	case tagAfter != "":
		ui.Done(out, "Service image applied: %s", shortImageID(tagAfter))
	default:
		ui.Warn(out, "Service restarted; image identity could not be verified")
	}
	ui.Hint(out, "%s", image)
}

func shortImageID(id string) string {
	id = strings.TrimSpace(strings.TrimPrefix(id, "sha256:"))
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

func prepareUpgrade(ctx context.Context, opts UpgradeOptions) (*upgradePlan, error) {
	p := &upgradePlan{opts: opts}
	data, err := os.ReadFile(DeploymentRecordPath(opts.ConfigDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("installer: read the existing deployment: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &p.record); err != nil {
			return nil, fmt.Errorf("installer: the deployment record is invalid; restore it before upgrading: %w", err)
		}
		if p.record.Deployment != DeploymentNative && !p.record.Deployment.Containerised() {
			return nil, fmt.Errorf("installer: unknown deployment %q; no service was changed", p.record.Deployment)
		}
	}
	if opts.Mode != "" && p.record.Mode != "" && opts.Mode != p.record.Mode {
		return nil, fmt.Errorf("installer: this is a %s deployment, not %s; run the command on the intended host", p.record.Mode, opts.Mode)
	}
	p.opts.Runtime = deploymentRuntime(p.record, opts.Runtime)
	if !p.record.Deployment.Containerised() {
		if err := p.prepareNative(ctx); err != nil {
			return nil, err
		}
		return p, nil
	}
	if p.record.Directory == "" || p.record.EnvFile == "" {
		return nil, fmt.Errorf("installer: the deployment record has no directory or environment file; restore it before upgrading")
	}
	if _, err := os.ReadFile(p.record.EnvFile); err != nil {
		return nil, fmt.Errorf("installer: read the existing environment: %w", err)
	}
	p.image, err = upgradeImage(p.record, opts.Image)
	if err != nil {
		return nil, err
	}
	if p.record.Deployment == DeploymentCompose {
		// A missing Compose file is one the upgrade offers to write again
		// (see layout.go), and a file it writes uses ZOOMIES_IMAGE by
		// construction; there is nothing to ask Compose about yet.
		if _, err := os.Stat(p.record.ComposeFile()); os.IsNotExist(err) {
			return p, nil
		}
		// Do not claim an upgrade if the operator has replaced the image
		// variable with a fixed reference in the compose file.
		images, err := p.compose(ctx, "config", "--images")
		if err != nil {
			return nil, err
		}
		found := false
		for _, image := range strings.Fields(images) {
			if image == p.image {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("installer: %s does not use ZOOMIES_IMAGE; restore that image setting before upgrading", p.record.ComposeFile())
		}
		return p, nil
	}
	endpoint := opts.DockerHost
	if endpoint == "" {
		endpoint = os.Getenv("DOCKER_HOST")
	}
	if endpoint == "" {
		endpoint = "unix:///var/run/docker.sock"
	}
	p.client, err = backend.NewAPIClient(endpoint)
	if err != nil {
		return nil, err
	}
	p.replacement, err = p.client.PrepareReplacement(ctx, containerOr(p.record), p.image)
	if err != nil {
		return nil, err
	}
	if opts.Image == "" && p.replacement.Image != p.record.Image {
		return nil, fmt.Errorf("installer: the running container uses a different image from its deployment record; pass --image with the intended replacement")
	}
	backup := p.replacement.Name + "-before-upgrade"
	if _, err := p.client.ContainerInspect(ctx, backup); backend.StatusCode(err) != 404 {
		return nil, fmt.Errorf("installer: cannot reserve backup container %s; inspect or remove an earlier upgrade backup before retrying", backup)
	}
	return p, nil
}

func upgradeImage(rec DeploymentRecord, override string) (string, error) {
	if override != "" {
		if strings.HasPrefix(override, "-") || strings.ContainsAny(override, " \t\r\n") {
			return "", fmt.Errorf("installer: --image must be one image reference")
		}
		return override, nil
	}
	base := strings.Split(strings.Split(rec.Image, "@")[0], ":")[0]
	if base != stockControllerRepository && base != stockAgentRepository {
		return "", fmt.Errorf("installer: %s is a custom image; pass --image with the image to upgrade to", rec.Image)
	}
	image := DefaultImage()
	if rec.Mode == ModeAgent || base == stockAgentRepository {
		image, _ = AgentImageFor(image)
	}
	return image, nil
}

func (p *upgradePlan) prepareNative(ctx context.Context) error {
	units := []string{UnitController, UnitAgent}
	// A provider-only host upgrades through its private executable too.
	if filepath.Dir(filepath.Dir(p.opts.BinaryPath)) == "/var/lib/zoomies-proxmox" && proxmoxKey.MatchString(filepath.Base(filepath.Dir(p.opts.BinaryPath))) {
		units = []string{"zoomies-proxmox-" + filepath.Base(filepath.Dir(p.opts.BinaryPath)) + ".service"}
	}
	if p.opts.Mode == ModeAgent {
		units = []string{UnitAgent}
	} else if p.opts.Mode != "" {
		units = []string{UnitController}
	}
	for _, unit := range units {
		if runtime.GOOS == "darwin" {
			label := (ServiceSpec{Unit: unit}).Label()
			path := LaunchdPlistPath(label, os.Geteuid() == 0)
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			binary, err := launchdBinary(data)
			if err != nil || filepath.Clean(binary) != p.opts.BinaryPath {
				return fmt.Errorf("installer: %s does not run %s; pass --installed-binary for its ProgramArguments executable", label, p.opts.BinaryPath)
			}
			domain := fmt.Sprintf("gui/%d/", os.Geteuid())
			if os.Geteuid() == 0 {
				domain = "system/"
			}
			p.nativeUnits = append(p.nativeUnits, domain+label)
			if p.unit == "" {
				p.unit = domain + label
			}
			p.launchd = true
			continue
		}
		state, err := p.opts.run(ctx, "systemctl", "show", "--property=LoadState", "--value", unit)
		if err != nil || strings.TrimSpace(state) != "loaded" {
			continue
		}
		execStart, err := p.opts.run(ctx, "systemctl", "show", "--property=ExecStart", "--value", unit)
		if err != nil {
			return err
		}
		if !strings.Contains(execStart, "path="+p.opts.BinaryPath+" ;") && !strings.Contains(execStart, "path="+p.opts.BinaryPath+";") {
			return fmt.Errorf("installer: %s does not run %s; pass --installed-binary with the path its ExecStart uses", unit, p.opts.BinaryPath)
		}
		p.nativeUnits = append(p.nativeUnits, unit)
		if p.unit == "" {
			p.unit = unit
		}
	}
	if p.unit == "" {
		return fmt.Errorf("installer: no existing Zoomies deployment was found; run on the installed host, or pass --config-dir for its deployment record")
	}
	return nil
}

func (p *upgradePlan) docker(ctx context.Context, args ...string) (string, error) {
	name, argv := p.dockerArgv(args...)
	return p.opts.run(ctx, name, argv...)
}

func (p *upgradePlan) dockerArgv(args ...string) (string, []string) {
	if p.opts.Runtime == "podman" {
		if p.opts.DockerHost != "" {
			args = append([]string{"--remote", "--url", p.opts.DockerHost}, args...)
		}
		return "podman", args
	}
	if p.opts.DockerHost != "" {
		args = append([]string{"--host", p.opts.DockerHost}, args...)
	}
	return "docker", args
}

func (p *upgradePlan) compose(ctx context.Context, args ...string) (string, error) {
	name, argv := p.composeArgv(args...)
	return p.opts.run(ctx, name, argv...)
}

func (p *upgradePlan) composeArgv(args ...string) (string, []string) {
	name, command := ComposeArgs(p.record.ComposeCommand, p.record.ComposeFile(), args...)
	prefix := []string{"ZOOMIES_IMAGE=" + p.image}
	if p.opts.DockerHost != "" {
		prefix = append(prefix, "DOCKER_HOST="+p.opts.DockerHost)
	}
	for n, arg := range command {
		if arg == "--env-file" && n+1 < len(command) {
			command[n+1] = p.record.EnvFile
		}
	}
	return "env", append(append(prefix, name), command...)
}

func (p *upgradePlan) pullRunnerImages(ctx context.Context) error {
	if !p.record.Deployment.Containerised() && p.opts.DockerHost == "" {
		s := p.settings(ctx)
		runsAgent := p.unit == UnitAgent || len(p.nativeUnits) > 1 || s.cfg.Agent.Embedded
		if !runsAgent || (s.cfg.Agent.Backend != "docker" && s.cfg.Agent.Backend != "podman") {
			return nil
		}
		p.opts.DockerHost = s.cfg.Agent.DockerHost
		if s.cfg.Agent.Backend == "podman" {
			p.opts.Runtime = "podman"
		}
	}
	images, err := p.docker(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return fmt.Errorf("read cached runner images: %w", err)
	}
	seen := map[string]bool{}
	var stock []string
	for _, image := range strings.Fields(images) {
		repo, _, _ := naming.SplitImage(image)
		if (repo != naming.RunnerImageRepo && repo != "ghcr.io/eyupio/zoomies-runner-docker" && repo != naming.RunnerFullImageRepo) || strings.HasSuffix(image, ":<none>") || seen[image] {
			continue
		}
		seen[image] = true
		stock = append(stock, image)
	}
	if len(stock) == 0 {
		PaletteFor(p.opts.Out).Hint(p.opts.Out, "No cached stock runner images to refresh")
		return nil
	}
	PaletteFor(p.opts.Out).Doing(p.opts.Out, "Refreshing %d runner image(s) for future jobs; running runners keep theirs", len(stock))
	for _, image := range stock {
		if _, err := p.docker(ctx, "pull", image); err != nil {
			return fmt.Errorf("pull %s; the deployment has not been restarted: %w", image, err)
		}
	}
	PaletteFor(p.opts.Out).Done(p.opts.Out, "Runner images refreshed for future jobs")
	return nil
}

// Only the image line changes. Secrets, comments and hand-edited settings stay
// byte-for-byte intact, and the original is available for a failed restart.
func replaceEnvImage(path, image string) ([]byte, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	value, err := quoteEnvValue("ZOOMIES_IMAGE", image)
	if err != nil {
		return nil, 0, err
	}
	lines := strings.Split(string(data), "\n")
	found := false
	for n, line := range lines {
		key, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		if ok && strings.TrimSpace(key) == "ZOOMIES_IMAGE" {
			lines[n] = "ZOOMIES_IMAGE=" + value
			found = true
		}
	}
	updated := strings.Join(lines, "\n")
	if !found {
		if !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		updated += "ZOOMIES_IMAGE=" + value + "\n"
	}
	if err := writeFileAtomic(path, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func (p *upgradePlan) upgradeCompose(ctx context.Context) error {
	PaletteFor(p.opts.Out).Doing(p.opts.Out, "Updating service image %s", p.image)
	if _, err := p.compose(ctx, "pull", "zoomies"); err != nil {
		return err
	}
	if kept, err := p.keepCurrentContainer(ctx); kept || err != nil {
		return err
	}
	previous, err := ParseEnvFile(p.record.EnvFile)
	if err != nil {
		return err
	}
	var unmove func() error
	if len(p.move) > 0 {
		unmove = p.moveComposeSettings(ctx)
	}
	old, mode, err := replaceEnvImage(p.record.EnvFile, p.image)
	if err != nil {
		return err
	}
	_, err = p.compose(ctx, "up", "-d", "--no-deps", "--force-recreate", "--timeout", strconv.Itoa(int(serviceStopTimeout.Seconds())), "zoomies")
	if err == nil {
		var running string
		running, err = p.docker(ctx, "inspect", "--format", "{{.State.Running}}", containerOr(p.record))
		if err == nil && strings.TrimSpace(running) != "true" {
			err = fmt.Errorf("the upgraded container did not stay running")
		}
	}
	if err != nil {
		// The files go back as they were before the move as well: the image
		// being rolled back to may predate the settings it would now find
		// only in the database.
		var restore error
		if unmove != nil {
			restore = unmove()
		} else {
			restore = writeFileAtomic(p.record.EnvFile, old, mode)
		}
		if restore == nil {
			rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), serviceStopTimeout+time.Minute)
			defer cancel()
			p.image = previous["ZOOMIES_IMAGE"]
			if p.image == "" {
				p.image = p.record.Image
			}
			_, restore = p.compose(rollback, "up", "-d", "--no-deps", "--force-recreate", "--timeout", strconv.Itoa(int(serviceStopTimeout.Seconds())), "zoomies")
		}
		return errors.Join(err, restore)
	}
	p.record.Image = p.image
	_, err = WriteDeploymentRecord(p.opts.ConfigDir, p.record)
	return err
}

func (p *upgradePlan) upgradeDocker(ctx context.Context) error {
	PaletteFor(p.opts.Out).Doing(p.opts.Out, "Updating service image %s", p.image)
	if _, err := p.docker(ctx, "pull", p.image); err != nil {
		return err
	}
	if kept, err := p.keepCurrentContainer(ctx); kept || err != nil {
		return err
	}
	old := p.replacement
	if err := p.client.ContainerStop(ctx, old.ID, serviceStopTimeout); err != nil {
		return err
	}
	moved := len(p.move) > 0 && p.moveDockerSettings(ctx)
	backup := old.Name + "-before-upgrade"
	if err := p.client.ContainerRename(ctx, old.ID, backup); err != nil {
		if old.Running {
			_ = p.client.ContainerStart(context.WithoutCancel(ctx), old.ID)
		}
		return err
	}
	id, err := p.client.CreateReplacement(ctx, old)
	if err == nil {
		err = p.client.ContainerStart(ctx, id)
	}
	if err == nil {
		var state *backend.ContainerInspect
		state, err = p.client.ContainerInspect(ctx, id)
		if err == nil && (state.State == nil || !state.State.Running) {
			err = fmt.Errorf("replacement container did not stay running")
		}
	}
	if err != nil {
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), serviceStopTimeout+time.Minute)
		defer cancel()
		var restore error
		if id != "" {
			restore = p.client.RemoveContainerKeepingVolumes(rollback, id, true)
		}
		if restore == nil {
			restore = p.client.ContainerRename(rollback, old.ID, old.Name)
		}
		if restore == nil && old.Running {
			restore = p.client.ContainerStart(rollback, old.ID)
		}
		return errors.Join(err, restore)
	}
	if _, _, err := replaceEnvImage(p.record.EnvFile, p.image); err != nil {
		return fmt.Errorf("new container is running but its environment file could not be updated: %w", err)
	}
	if moved {
		p.commentDockerEnvFile()
	}
	p.record.Image = p.image
	if _, err := WriteDeploymentRecord(p.opts.ConfigDir, p.record); err != nil {
		return err
	}
	if err := p.client.RemoveContainerKeepingVolumes(ctx, old.ID, false); err != nil {
		return fmt.Errorf("new container is running; remove the stopped %s container when convenient: %w", backup, err)
	}
	return nil
}

// Native services name an absolute executable even when --prefix was relative.
func upgradeBinaryPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}

func launchdBinary(data []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	arguments := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "key" {
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return "", err
			}
			arguments = key == "ProgramArguments"
		} else if arguments && start.Name.Local == "string" {
			var binary string
			err := decoder.DecodeElement(&binary, &start)
			return binary, err
		}
	}
}
