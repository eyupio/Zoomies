package installer

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// The shape of a containerised Zoomies. These are constants rather than
// questions because nothing is gained by asking: inside the container there is
// one process, one port and one data directory, and the operator's choices --
// which port to publish, where to keep the files -- are made on the outside.
const (
	// DefaultImage is the published controller image.
	DefaultImage = "ghcr.io/eyupio/zoomies:latest"
	// ContainerPort is what Zoomies listens on inside the container.
	ContainerPort = 8080
	// ContainerStateDir is the mount point of the data volume.
	ContainerStateDir = "/var/lib/zoomies"
	// ContainerDBPath and ContainerWorkDir live in that volume, so that
	// replacing the container keeps the fleet.
	ContainerDBPath  = ContainerStateDir + "/zoomies.db"
	ContainerWorkDir = ContainerStateDir + "/work"

	// ComposeFileName is the file a compose deployment writes.
	ComposeFileName = "docker-compose.yml"
	// ContainerName, VolumeName and NetworkName are what a deployment creates
	// on the host. They are recorded in the deployment record too, so that
	// uninstall removes what was made rather than what today's defaults say.
	ContainerName = "zoomies"
	VolumeName    = "zoomies-data"
	NetworkName   = "zoomies"
)

// ComposeFileSpec is everything the compose template needs.
//
// It carries no values: every setting the container reads comes from the
// generated environment file beside it, and this struct decides only the
// file's shape -- which mounts exist, whether a port is published, whether the
// socket is there at all.
type ComposeFileSpec struct {
	// Mode decides whether this project runs a controller or an agent.
	Mode Mode
	// Command is the subcommand the image runs.
	Command string
	// ContainerPort is the listener inside the container.
	ContainerPort int
	// Publish adds the port mapping. An agent has no listener to publish.
	Publish bool
	// MountSocket mounts the container runtime's socket, which the embedded
	// agent needs in order to create runner containers.
	MountSocket bool
	SocketPath  string
	// GroupAdd adds the host's docker group. A rootless socket needs no group,
	// so the line is left out rather than written with a meaningless gid.
	GroupAdd bool
	// TLSCertFile and TLSKeyFile are mounted read-only at the same paths they
	// have on the host, which is why the environment file can name them
	// directly.
	TLSCertFile string
	TLSKeyFile  string
	// Healthcheck is off for an agent, which serves nothing to check.
	Healthcheck bool

	Container string
	Volume    string
	Network   string
}

// defaults fills what a caller may leave empty.
func (s ComposeFileSpec) defaults() ComposeFileSpec {
	if s.Mode == "" {
		s.Mode = ModeSingle
	}
	if s.Command == "" {
		s.Command = "controller"
		if s.Mode == ModeAgent {
			s.Command = "agent"
		}
	}
	if s.ContainerPort == 0 {
		s.ContainerPort = ContainerPort
	}
	if s.Container == "" {
		s.Container = ContainerName
	}
	if s.Volume == "" {
		s.Volume = VolumeName
	}
	if s.Network == "" {
		s.Network = NetworkName
	}
	if s.SocketPath == "" && s.MountSocket {
		s.SocketPath = "/var/run/docker.sock"
	}
	return s
}

// RenderComposeFile renders the docker-compose.yml a compose deployment is
// brought up with.
//
// It mirrors the docker-compose.yml in the repository deliberately: same
// service, same volume, same healthcheck, and the same comment about the
// Docker socket being Zoomies' own access rather than something handed to
// jobs. An operator who has read one has read the other.
func RenderComposeFile(spec ComposeFileSpec) (string, error) {
	return render("deployment-compose.yml.tmpl", spec.defaults())
}

// ComposeFileSpecFor derives the compose file's shape from a resolved plan, so
// that the same decisions drive the file and the environment beside it.
func ComposeFileSpecFor(p Plan) ComposeFileSpec {
	mountSocket := p.runsRunners() && p.Backend != store.BackendProcess && p.DockerHost != ""
	return ComposeFileSpec{
		Mode:          p.Mode,
		ContainerPort: ContainerPort,
		Publish:       p.Mode != ModeAgent,
		MountSocket:   mountSocket,
		SocketPath:    socketPath(p.DockerHost),
		GroupAdd:      mountSocket && p.DockerGID > 0,
		TLSCertFile:   p.TLSCertFile,
		TLSKeyFile:    p.TLSKeyFile,
		Healthcheck:   p.Mode != ModeAgent,
	}
}

// runsRunners reports whether this host will create runner containers itself,
// which is the only reason a container is ever given the runtime's socket.
func (p Plan) runsRunners() bool { return p.Embedded || p.Mode == ModeAgent }

// socketPath turns a docker host URL into the path to bind-mount. A TCP
// endpoint has no path to mount, and correctly yields nothing.
func socketPath(dockerHost string) string {
	if !strings.HasPrefix(dockerHost, "unix://") {
		return ""
	}
	return strings.TrimPrefix(dockerHost, "unix://")
}

// EnvSpecFor derives the environment file from a resolved plan. It is the
// single point where the installer's decisions become the container's
// settings, which is what keeps the compose file, the run command and the
// summary all describing the same deployment.
func EnvSpecFor(p Plan) EnvSpec {
	poll := true
	return EnvSpec{
		Deployment:       p.Deployment,
		Mode:             p.Mode,
		ExternalURL:      p.ExternalURL,
		Bind:             p.Bind,
		PublishAddr:      p.PublishAddr,
		PublishedPort:    p.PublishedPort,
		TLSMode:          p.TLSMode,
		TLSCertFile:      p.TLSCertFile,
		TLSKeyFile:       p.TLSKeyFile,
		TrustedProxies:   p.TrustedProxies,
		Embedded:         p.Embedded,
		Backend:          string(p.Backend),
		DockerHost:       p.DockerHost,
		Capacity:         p.Capacity,
		WorkDir:          p.WorkDir,
		DBPath:           p.DBPath,
		StateDir:         ContainerStateDir,
		LogFormat:        "json",
		LogLevel:         "info",
		PollFallback:     &poll,
		GitHubAPIBaseURL: p.GitHub.APIBaseURL,
		Image:            p.Image,
		DockerGID:        p.DockerGID,
	}
}

// containerise rewrites a plan's paths for a deployment that runs inside a
// container.
//
// The operator answered questions about a host: which port, where the socket
// is. Inside the container the answers are different -- Zoomies always binds
// :8080 there and always keeps its state in the volume -- and the port they
// chose becomes the published port rather than the listener. Doing that
// conversion in one pure function is what stops the two sets of paths getting
// mixed up in the compose file, the environment file and the health check.
func containerise(p Plan) Plan {
	if !p.Deployment.Containerised() {
		return p
	}
	host, port, err := splitBind(p.Bind)
	if err != nil {
		host, port = "127.0.0.1", ContainerPort
	}
	p.PublishedPort = port
	if p.PublishAddr == "" {
		p.PublishAddr = "0.0.0.0"
		if isLoopbackHost(host) {
			p.PublishAddr = "127.0.0.1"
		}
	}
	p.Bind = net.JoinHostPort("0.0.0.0", strconv.Itoa(ContainerPort))
	p.DBPath = ContainerDBPath
	p.WorkDir = ContainerWorkDir
	if p.Image == "" {
		p.Image = DefaultImage
	}
	if p.DeployDir == "" {
		p.DeployDir = p.ConfigDir
	}
	return p
}

// EnvFileFor is the environment file name a deployment uses.
func EnvFileFor(d Deployment) string {
	if d == DeploymentDocker {
		return DockerEnvFileName
	}
	return EnvFileName
}

// ---------------------------------------------------------------------------
// Command lines
// ---------------------------------------------------------------------------

// ComposeArgs builds the argv for one compose subcommand against a specific
// file, so that the command works from any directory.
//
// The environment file is named explicitly rather than left to be found.
// Compose v2 takes the project directory from the compose file's own and would
// find it; v1 looked in the working directory and would not, and would then
// interpolate every value to empty and bring up a controller with no
// encryption key. Naming it costs one flag and removes that difference.
func ComposeArgs(compose []string, file string, args ...string) (string, []string) {
	if len(compose) == 0 {
		compose = []string{"docker", "compose"}
	}
	out := append([]string{}, compose[1:]...)
	out = append(out, "-f", file, "--env-file", filepath.Join(filepath.Dir(file), EnvFileName))
	return compose[0], append(out, args...)
}

// ComposeLine renders a compose command as something the operator can paste.
func ComposeLine(compose []string, file string, args ...string) string {
	name, argv := ComposeArgs(compose, file, args...)
	return name + " " + strings.Join(argv, " ")
}

// DockerRunSpec is everything `docker run` needs for a single-container
// deployment.
type DockerRunSpec struct {
	Image     string
	Container string
	EnvFile   string
	Volume    string
	Network   string
	// Command is the subcommand the image runs.
	Command string

	Publish       bool
	PublishAddr   string
	PublishedPort int
	ContainerPort int

	// MountSocket and SocketPath give the embedded agent its runtime.
	MountSocket bool
	SocketPath  string
	// DockerGID is added as a supplementary group, which is how the image's
	// unprivileged user reaches a root-owned socket.
	DockerGID int

	// ReadOnlyMounts are host paths mounted at the same path inside, used for
	// the TLS certificate and key.
	ReadOnlyMounts []string
}

// DockerRunArgs builds the argv for the run command.
//
// It is a plain function over a struct so that the command an operator will
// live with can be asserted on in a test: a run command that is subtly wrong
// -- a missing --group-add, a volume that is not the one uninstall removes --
// is the kind of thing that only shows up weeks later.
func DockerRunArgs(s DockerRunSpec) []string {
	if s.Image == "" {
		s.Image = DefaultImage
	}
	if s.Container == "" {
		s.Container = ContainerName
	}
	if s.Volume == "" {
		s.Volume = VolumeName
	}
	if s.ContainerPort == 0 {
		s.ContainerPort = ContainerPort
	}
	if s.Command == "" {
		s.Command = "controller"
	}

	args := []string{"run", "--detach", "--name", s.Container, "--restart", "unless-stopped"}
	if s.EnvFile != "" {
		args = append(args, "--env-file", s.EnvFile)
	}
	if s.Publish {
		published := strconv.Itoa(s.PublishedPort) + ":" + strconv.Itoa(s.ContainerPort)
		if s.PublishAddr != "" {
			published = s.PublishAddr + ":" + published
		}
		args = append(args, "--publish", published)
	}
	args = append(args, "--volume", s.Volume+":"+ContainerStateDir)
	if s.MountSocket && s.SocketPath != "" {
		// The socket is mounted at the path it has on the host, so that
		// ZOOMIES_DOCKER_HOST means the same thing on both sides.
		args = append(args, "--volume", s.SocketPath+":"+s.SocketPath)
	}
	for _, m := range s.ReadOnlyMounts {
		if m == "" {
			continue
		}
		args = append(args, "--volume", m+":"+m+":ro")
	}
	if s.MountSocket && s.DockerGID > 0 {
		args = append(args, "--group-add", strconv.Itoa(s.DockerGID))
	}
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	return append(args, s.Image, s.Command)
}

// DockerRunSpecFor derives the run command from a resolved plan.
func DockerRunSpecFor(p Plan, envFile string) DockerRunSpec {
	mountSocket := p.runsRunners() && p.Backend != store.BackendProcess && p.DockerHost != ""
	var mounts []string
	if p.TLSMode == config.TLSFiles {
		mounts = append(mounts, p.TLSCertFile, p.TLSKeyFile)
	}
	command := "controller"
	if p.Mode == ModeAgent {
		command = "agent"
	}
	return DockerRunSpec{
		Image:          p.Image,
		Container:      ContainerName,
		EnvFile:        envFile,
		Volume:         VolumeName,
		Network:        NetworkName,
		Command:        command,
		Publish:        p.Mode != ModeAgent,
		PublishAddr:    p.PublishAddr,
		PublishedPort:  p.PublishedPort,
		ContainerPort:  ContainerPort,
		MountSocket:    mountSocket,
		SocketPath:     socketPath(p.DockerHost),
		DockerGID:      p.DockerGID,
		ReadOnlyMounts: mounts,
	}
}

// DockerCommandLine renders a docker argv as something the operator can paste.
func DockerCommandLine(args []string) string {
	return "docker " + strings.Join(args, " ")
}

// ---------------------------------------------------------------------------
// Running it
// ---------------------------------------------------------------------------

// runContainer performs a compose or docker deployment.
//
// It is deliberately not runInstall with branches. A containerised controller
// keeps its database inside a volume this process cannot reach, so there is no
// key file to write, no database to migrate and no administrator to create
// here: the container does all of that on first start, and the operator
// creates the first administrator in the browser. What this function owns is
// the two files, bringing them up, and proving the result answers.
func (i *Installer) runContainer(ctx context.Context, p Plan) error {
	_, rerun := ReadDeploymentRecord(p.ConfigDir)

	i.ui.step("Writing the deployment into " + p.DeployDir)
	if err := os.MkdirAll(p.DeployDir, 0o750); err != nil {
		return fmt.Errorf("installer: creating %s: %w", p.DeployDir, err)
	}

	envPath := filepath.Join(p.DeployDir, EnvFileFor(p.Deployment))
	res, err := WriteEnv(envPath, EnvSpecFor(p))
	if err != nil {
		return err
	}
	if res.Backup != "" {
		i.ui.note("the previous environment file is at " + res.Backup)
	}
	i.ui.ok("wrote " + envPath + " (mode 0600, fully populated)")
	if res.ReusedKey {
		i.ui.ok("kept the encryption key that was already in " + filepath.Base(envPath))
		i.ui.note("this is what makes a re-run an upgrade: a new key would leave every stored secret undecryptable.")
	} else {
		i.ui.warn("A new encryption key was generated into " + filepath.Base(envPath) + ". Back it up now.")
		i.ui.note("without it the stored GitHub App private key and every webhook secret cannot be decrypted")
		i.ui.note("and must be entered again. Copy it with:  sudo grep ZOOMIES_ENCRYPTION_KEY " + envPath)
	}
	i.warnAboutRootlessSocket(p)

	// The record is written before anything is started, not after. A failed
	// `up` can still have created a network, a volume or a container, and an
	// operator whose install fell over half-way should be able to run
	// `zoomies uninstall` and have it find them.
	path, err := WriteDeploymentRecord(p.ConfigDir, DeploymentRecord{
		Deployment:     p.Deployment,
		Directory:      p.DeployDir,
		ComposeCommand: p.ComposeCommand,
		Container:      ContainerName,
		Image:          p.Image,
		Volume:         VolumeName,
		Network:        NetworkName,
		EnvFile:        envPath,
		Mode:           p.Mode,
	})
	if err != nil {
		return err
	}
	i.ui.note("recorded this deployment in " + path + ", which is how `zoomies uninstall` knows to take it down.")

	switch p.Deployment {
	case DeploymentCompose:
		if err := i.upCompose(ctx, p, rerun); err != nil {
			return err
		}
	default:
		if err := i.upDocker(ctx, p, envPath, rerun); err != nil {
			return err
		}
	}

	if p.Mode != ModeAgent {
		i.stepContainerHealth(ctx, p)
	}
	i.containerSummary(p, envPath, res.ReusedKey)
	return nil
}

// warnAboutRootlessSocket names the one combination that looks right and is
// not: the image runs as an unprivileged uid, which cannot use a socket that
// belongs to the operator's own user.
func (i *Installer) warnAboutRootlessSocket(p Plan) {
	if !p.Rootless || !p.runsRunners() || p.Backend == store.BackendProcess {
		return
	}
	i.ui.warn("the socket at " + p.DockerHost + " belongs to your user, and the image runs as uid 65532.")
	i.ui.note("if runners fail to start, point ZOOMIES_DOCKER_HOST at the system socket, or run the")
	if p.Deployment == DeploymentCompose {
		i.ui.note("container as yourself by adding  user: \"$(id -u):$(id -g)\"  to the service in")
		i.ui.note(filepath.Join(p.DeployDir, ComposeFileName) + ".")
		return
	}
	i.ui.note("container as yourself by adding  --user $(id -u):$(id -g)  to the run command below.")
}

// upCompose writes the compose file and brings the project up.
func (i *Installer) upCompose(ctx context.Context, p Plan, rerun bool) error {
	body, err := RenderComposeFile(ComposeFileSpecFor(p))
	if err != nil {
		return err
	}
	file := filepath.Join(p.DeployDir, ComposeFileName)
	if err := writeFileAtomic(file, []byte(body), 0o644); err != nil {
		return fmt.Errorf("installer: writing %s: %w", file, err)
	}
	i.ui.ok("wrote " + file)

	if rerun {
		// A re-run is an upgrade, and `up -d` on its own will happily keep
		// running the image that is already on this host.
		i.ui.step("Pulling " + p.Image)
		name, args := ComposeArgs(p.ComposeCommand, file, "pull")
		if err := i.stream(ctx, p.DeployDir, name, args...); err != nil {
			i.ui.warn("could not pull a newer image: " + err.Error())
			i.ui.note("carrying on with the image already on this host.")
		}
	}

	i.ui.step("Bringing the project up")
	name, args := ComposeArgs(p.ComposeCommand, file, "up", "-d")
	i.ui.note(ComposeLine(p.ComposeCommand, file, "up", "-d"))
	i.ui.blank()
	if err := i.stream(ctx, p.DeployDir, name, args...); err != nil {
		return fmt.Errorf("installer: %w\n      see what it did with: %s", err, ComposeLine(p.ComposeCommand, file, "logs"))
	}
	i.ui.blank()
	i.ui.ok("the project is up")
	return nil
}

// upDocker replaces any container from a previous run and starts a new one.
func (i *Installer) upDocker(ctx context.Context, p Plan, envPath string, rerun bool) error {
	if rerun {
		// A re-run is an upgrade, and starting the image already on this host
		// would make it a very elaborate restart instead.
		i.ui.step("Pulling " + p.Image)
		if err := i.stream(ctx, p.DeployDir, "docker", "pull", p.Image); err != nil {
			i.ui.warn("could not pull a newer image: " + err.Error())
			i.ui.note("carrying on with the image already on this host.")
		}
	}
	// A container from an earlier run holds the name and the port, so it is
	// removed first. Its volume is untouched, which is what makes this an
	// upgrade rather than a reinstall.
	if containerExists(ctx, ContainerName) {
		i.ui.note("replacing the existing " + ContainerName + " container; its volume, and so the database, is kept.")
		_, _ = runCommand(ctx, "docker", "stop", ContainerName)
		if _, err := runCommand(ctx, "docker", "rm", ContainerName); err != nil {
			return fmt.Errorf("installer: removing the previous container: %w", err)
		}
	}
	if _, err := runCommand(ctx, "docker", "network", "inspect", NetworkName); err != nil {
		if _, err := runCommand(ctx, "docker", "network", "create", NetworkName); err != nil {
			return fmt.Errorf("installer: creating the %s network, which keeps runner containers off the default bridge: %w", NetworkName, err)
		}
		i.ui.ok("created the " + NetworkName + " network")
	}

	args := DockerRunArgs(DockerRunSpecFor(p, envPath))
	i.ui.step("Starting the container")
	i.ui.note(DockerCommandLine(args))
	i.ui.blank()
	if err := i.stream(ctx, p.DeployDir, "docker", args...); err != nil {
		return fmt.Errorf("installer: %w\n      see what it did with: docker logs %s", err, ContainerName)
	}
	i.ui.blank()
	i.ui.ok("the " + ContainerName + " container is running")
	return nil
}

// containerExists reports whether a container of this name is present, running
// or not.
func containerExists(ctx context.Context, name string) bool {
	_, err := runCommand(ctx, "docker", "container", "inspect", name)
	return err == nil
}

// stream runs a command with its output going straight to the installer's
// output, because `compose up` is the one step here that takes long enough
// that silence looks like a hang.
func (i *Installer) stream(ctx context.Context, dir, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not on PATH, so this deployment cannot be brought up here: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdout = i.out
	cmd.Stderr = i.out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// stepContainerHealth waits for the published listener to answer, and turns a
// failure into the command that shows why.
func (i *Installer) stepContainerHealth(ctx context.Context, p Plan) {
	i.ui.step("Health check")
	target := containerHealthURL(p)
	if err := waitHealthy(ctx, healthClient(p), target, healthTimeout); err != nil {
		i.ui.warn("the controller did not answer " + target + " within " + healthTimeout.String())
		i.ui.note(err.Error())
		i.ui.note("see why with: " + i.logsCommand(p))
		return
	}
	i.ui.ok("the controller is answering on " + target)
}

// containerHealthURL points at the published port on loopback: the question is
// whether the container came up, not whether DNS and a proxy are in place yet.
func containerHealthURL(p Plan) string {
	scheme := "http"
	if p.TLSMode != config.TLSOff {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d/healthz", scheme, p.PublishedPort)
}

// logsCommand is how this deployment's logs are read.
func (i *Installer) logsCommand(p Plan) string {
	if p.Deployment == DeploymentCompose {
		return ComposeLine(p.ComposeCommand, filepath.Join(p.DeployDir, ComposeFileName), "logs", "-f")
	}
	return "docker logs -f " + ContainerName
}

// containerSummary is the last thing the operator reads. It is the handful of
// commands they will actually want next, and nothing else.
func (i *Installer) containerSummary(p Plan, envPath string, reusedKey bool) {
	file := filepath.Join(p.DeployDir, ComposeFileName)
	i.ui.blank()
	i.ui.step("Done")
	i.ui.note("URL       " + p.ExternalURL)
	i.ui.note("env       " + envPath + " (mode 0600 -- it holds the encryption key)")
	if p.Deployment == DeploymentCompose {
		i.ui.note("compose   " + file)
	} else {
		i.ui.note("container " + ContainerName + " from " + p.Image)
	}
	i.ui.note("volume    " + VolumeName + " -- the database lives here, not in the container")
	i.ui.blank()

	if !reusedKey {
		i.ui.note("Open " + p.ExternalURL + " and create the first administrator.")
		i.ui.blank()
	}

	i.ui.note("The commands you will want:")
	for _, line := range i.deploymentCommands(p) {
		i.ui.note("  " + line)
	}
	i.ui.blank()

	if p.PublishAddr == "127.0.0.1" {
		i.ui.note("The container is published on loopback only, so reach it from your laptop with:")
		i.ui.note(fmt.Sprintf("  ssh -L %d:127.0.0.1:%d %s", p.PublishedPort, p.PublishedPort, hostname()))
		i.ui.blank()
	}

	sug := SuggestPool(i.det.OS, i.det.Arch, p.Backend, p.Capacity)
	i.ui.note("Your first pool -- this host is " + i.det.Arch + " with the " + string(p.Backend) + " backend:")
	i.ui.note("  " + sug.Command())
	i.ui.note("then put  runs-on: [self-hosted, " + sug.Name + "]  in a workflow.")
}

// deploymentCommands lists the four things an operator does to a running
// deployment: read it, restart it, upgrade it and stop it.
func (i *Installer) deploymentCommands(p Plan) []string {
	if p.Deployment == DeploymentCompose {
		file := filepath.Join(p.DeployDir, ComposeFileName)
		return []string{
			"logs      " + ComposeLine(p.ComposeCommand, file, "logs", "-f"),
			"restart   " + ComposeLine(p.ComposeCommand, file, "restart"),
			"upgrade   " + ComposeLine(p.ComposeCommand, file, "pull") + " && " + ComposeLine(p.ComposeCommand, file, "up", "-d"),
			"stop      " + ComposeLine(p.ComposeCommand, file, "down") + "   (add -v to delete the database too)",
		}
	}
	envPath := filepath.Join(p.DeployDir, EnvFileFor(p.Deployment))
	run := DockerCommandLine(DockerRunArgs(DockerRunSpecFor(p, envPath)))
	return []string{
		"logs      docker logs -f " + ContainerName,
		"restart   docker restart " + ContainerName,
		"upgrade   docker pull " + p.Image + " && docker rm -f " + ContainerName + " && " + run,
		"stop      docker stop " + ContainerName,
	}
}

// ---------------------------------------------------------------------------
// Taking it down
// ---------------------------------------------------------------------------

// ComposeDownArgs builds the teardown command for a compose deployment.
// Removing the volume is a separate decision because the volume is the
// database.
func ComposeDownArgs(rec DeploymentRecord, removeVolumes bool) (string, []string) {
	args := []string{"down"}
	if removeVolumes {
		args = append(args, "-v")
	}
	return ComposeArgs(rec.ComposeCommand, rec.ComposeFile(), args...)
}

// DockerStopArgs, DockerRemoveArgs and DockerVolumeRemoveArgs are the same
// teardown for a single container, in the order they must run.
func DockerStopArgs(rec DeploymentRecord) []string {
	return []string{"stop", containerOr(rec)}
}

func DockerRemoveArgs(rec DeploymentRecord) []string {
	return []string{"rm", containerOr(rec)}
}

func DockerVolumeRemoveArgs(rec DeploymentRecord) []string {
	volume := rec.Volume
	if volume == "" {
		volume = VolumeName
	}
	return []string{"volume", "rm", volume}
}

func containerOr(rec DeploymentRecord) string {
	if rec.Container != "" {
		return rec.Container
	}
	return ContainerName
}

// tearDownDeployment stops and removes a containerised deployment.
//
// It is the half of uninstall that the record exists for: without it, a
// compose project or a container keeps running -- and keeps creating runners
// -- long after the operator has been told Zoomies is gone.
func tearDownDeployment(ctx context.Context, rec DeploymentRecord, removeVolume bool, u *ui, record func(string, ...any)) {
	switch rec.Deployment {
	case DeploymentCompose:
		name, args := ComposeDownArgs(rec, removeVolume)
		if _, err := runCommand(ctx, name, args...); err != nil {
			u.warn("could not bring the compose project down: " + err.Error())
			u.note("take it down by hand with: " + name + " " + strings.Join(args, " "))
			return
		}
		if removeVolume {
			record("brought the compose project down and removed its volume")
		} else {
			record("brought the compose project down; the %s volume was kept", volumeOr(rec))
		}
	case DeploymentDocker:
		if _, err := runCommand(ctx, "docker", DockerStopArgs(rec)...); err != nil {
			u.note("the " + containerOr(rec) + " container was not running")
		}
		if _, err := runCommand(ctx, "docker", DockerRemoveArgs(rec)...); err != nil {
			u.warn("could not remove the " + containerOr(rec) + " container: " + err.Error())
			u.note("remove it by hand with: docker rm -f " + containerOr(rec))
		} else {
			record("stopped and removed the %s container", containerOr(rec))
		}
		if removeVolume {
			if _, err := runCommand(ctx, "docker", DockerVolumeRemoveArgs(rec)...); err != nil {
				u.warn("could not remove the " + volumeOr(rec) + " volume: " + err.Error())
			} else {
				record("removed the %s volume, including the database", volumeOr(rec))
			}
		} else {
			u.note("left the " + volumeOr(rec) + " volume in place; remove it with: docker volume rm " + volumeOr(rec))
		}
	}
}

func volumeOr(rec DeploymentRecord) string {
	if rec.Volume != "" {
		return rec.Volume
	}
	return VolumeName
}
