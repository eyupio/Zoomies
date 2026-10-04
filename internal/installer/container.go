package installer

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// The shape of a containerised Zoomies. These are constants rather than
// questions because nothing is gained by asking: inside the container there is
// one process, one port and one data directory, and the operator's choices --
// which port to publish, where to keep the files -- are made on the outside.
const (
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
	// GroupAdd adds the group that owns the host's socket -- the docker group
	// for a root daemon, the user's own group for a rootless one. It is left out
	// rather than written with a meaningless gid when the socket could not be
	// read at all.
	GroupAdd bool
	// TLSCertFile and TLSKeyFile are mounted read-only at the same paths they
	// have on the host, which is why the TLS settings can name them directly.
	// They are written into the file rather than read from .env, which holds
	// no settings for a controller.
	TLSCertFile string
	TLSKeyFile  string
	// Healthcheck is off for an agent, which serves nothing to check.
	Healthcheck bool

	Container string
	Volume    string
	Network   string
	// MountShared mounts the host's shared folder, SharedDir, at the same
	// path. Only a host that runs runners has any use for it: the folder is
	// what the daemon binds runners' caches from.
	MountShared bool
	SharedDir   string
}

// defaults fills what a caller may leave empty.
func (s ComposeFileSpec) defaults() ComposeFileSpec {
	if s.SharedDir == "" {
		s.SharedDir = SharedHostDir
	}
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
		MountShared:   p.runsRunners(),
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
		Network:          NetworkName,
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

// The two repositories AgentImageFor translates between. Both workflows build
// both from the same commit and publish them under the same tags, which is what
// makes deriving one from the other sound rather than a guess.
const (
	stockControllerRepository = "ghcr.io/eyupio/zoomies"
	stockAgentRepository      = "ghcr.io/eyupio/zoomies-agent"
)

// DefaultImage is the controller image a fresh containerised install runs: the
// one that matches this installer's own build.
//
// It used to be a constant naming :latest, which was wrong twice over the
// moment :latest became a release's name rather than main's. Every release this
// project has made so far carries a hyphen -- v0.1-alpha, v0.2-beta, and a
// v1.0.0-RC1 would too -- and a hyphen is this project saying the release is not
// finished, so the release workflow deliberately leaves :latest where it is.
// Nothing has published :latest since main stopped, and nothing will until a
// release without a hyphen: an installer defaulting to it would write a compose
// file pulling a build older than the installer that wrote it, and an agent one
// pulling a tag that has never existed at all.
//
// Naming this build instead is what the join command already does for the
// binary. An installer taken from a release installs that release's container,
// and a build from main installs :dev, which is the tag main publishes.
func DefaultImage() string { return defaultImageFor(version.Version) }

// defaultImageFor is the pure half, so that both branches can be tested without
// relinking the binary.
func defaultImageFor(v string) string {
	if tag, ok := version.Release(v); ok {
		// The stamped version has no leading v -- the release workflow strips
		// it, because a binary reports 1.2.3 -- and the image tag is the git
		// tag, which has one.
		return stockControllerRepository + ":v" + tag
	}
	return stockControllerRepository + ":dev"
}

// AgentImageFor returns the image a runner host should run, given the
// controller image this deployment was told to use, and whether one could be
// worked out at all.
//
// A runner host given the controller image is not broken -- the entrypoint is
// the same binary and the compose file already overrides the command -- but it
// is the wrong image: it carries the UI, it labels itself as a controller in
// every registry listing and `docker inspect`, and it declares a healthcheck
// against a listener an agent does not have, so the container reports unhealthy
// for as long as it runs. Deriving the right one costs nothing and the operator
// was never going to be asked which of two images to name.
//
// The derivation is deliberately narrow, in the same way RunnerImageFor's is,
// and refuses rather than guesses in the three places a guess would be wrong:
//
//   - A digest names one exact set of bytes. There is no arithmetic that turns
//     the controller's digest into the agent's, so a pinned deployment keeps
//     what it pinned.
//   - A mirror or an operator's own build lives in a repository this code knows
//     nothing about. Whether it carries an -agent counterpart is not something
//     that can be known from the name.
//   - A repository that merely starts the same way -- zoomies-runner -- is a
//     different image, which is why this compares the whole repository rather
//     than taking a prefix.
//
// The tag is carried across untouched, including a digest-free pin, because a
// tag that exists for the controller exists for the agent: the same workflow
// step publishes both.
func AgentImageFor(controller string) (string, bool) {
	repo, tag, digest := naming.SplitImage(controller)
	if repo != stockControllerRepository || digest != "" {
		return "", false
	}
	return stockAgentRepository + tag, true
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
		p.Image = DefaultImage()
	}
	// A runner host runs the agent image at whatever tag the controller image
	// names, so a fleet that pins v1.2.3 gets an agent of v1.2.3 without the
	// operator having to know there are two images. An image this cannot be
	// derived from -- a digest, a mirror, something of the operator's own -- is
	// left exactly as given: it still works, because the command is overridden
	// either way, and second-guessing a pin is worse than running the image the
	// operator asked for.
	if p.Mode == ModeAgent {
		if img, ok := AgentImageFor(p.Image); ok {
			p.Image = img
		}
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
	// MountShared mounts the shared folder, for a host that runs runners.
	MountShared bool
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
		s.Image = DefaultImage()
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

	// No --health-cmd: the image declares its own HEALTHCHECK, in exec form,
	// which a plain `docker run` inherits. Repeating it here would have to be
	// the string form, and docker wraps that in /bin/sh -- which a distroless
	// image does not have, so the check would fail on every probe.
	//
	// --stop-timeout is the same budget Compose and the systemd units carry. A
	// bare `docker stop` or `docker restart` honours the container's own value
	// and Docker's default is ten seconds, which SIGKILLs an embedded agent in
	// the middle of an admitted runner creation.
	//
	// The hostname is the name the embedded agent registers the host under;
	// without one it is the container's random ID, which then follows the host
	// into every scheduler reason and problem message.
	args := []string{"run", "--detach", "--name", s.Container, "--hostname", s.Container, "--restart", "unless-stopped",
		"--stop-timeout", strconv.Itoa(int(serviceStopTimeout.Seconds()))}
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
	if s.MountShared {
		// The shared folder, at the path it has on the host; see SharedHostDir.
		args = append(args, "--volume", SharedHostDir+":"+SharedHostDir)
	}
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
		MountShared:    p.runsRunners(),
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
func (i *Installer) runContainer(ctx context.Context, p Plan) (retErr error) {
	if i.opts.AfterHostSetup != nil && p.runsRunners() && i.det.OS == "linux" && i.det.Init == "systemd" {
		defer func() {
			if retErr != nil {
				return
			}
			work := p.WorkDir
			if mount, err := runCommand(ctx, "docker", "volume", "inspect", "--format", "{{.Mountpoint}}", VolumeName); err == nil && filepath.IsAbs(strings.TrimSpace(mount)) {
				work = filepath.Join(strings.TrimSpace(mount), "work")
			}
			if err := installHostHealth(ctx, i.opts.binaryPath(), work, p.DockerHost, runCommand); err != nil {
				i.ui.warn("Native host health service was not installed: " + err.Error())
			}
		}()
	}

	_, rerun := ReadDeploymentRecord(p.ConfigDir)

	i.ui.step("Writing the deployment into " + p.DeployDir)
	if err := os.MkdirAll(p.DeployDir, 0o750); err != nil {
		return fmt.Errorf("installer: creating %s: %w", p.DeployDir, err)
	}
	// Before the container, which mounts it: a bind to a path that is not
	// there is created by the daemon owned by root, and the image's account
	// could then never add to it. A controller that runs no runners has no
	// use for it and is not given one.
	if p.runsRunners() {
		created, err := PrepareSharedDir(SharedHostDir, ImageUID, ImageUID)
		if err != nil {
			return err
		}
		if len(created) > 0 {
			i.wrote(fmt.Sprintf("created %s for runners' caches, owned by uid %d so later releases can add to it", SharedHostDir, ImageUID))
		}
		if problems := SharedDirProblems(SharedHostDir, ImageUID); len(problems) > 0 {
			for _, pr := range problems {
				i.ui.warn(pr)
			}
			i.ui.note(fmt.Sprintf("run: sudo chown -R %d:%d %s", ImageUID, ImageUID, SharedHostDir))
		}
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
	i.checkContainerSocketAccess(p)

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
	var setupToken string
	var setupTokenErr error
	// A rerun may retain historical logs containing a token from before the
	// first administrator existed. Only a new deployment can safely treat the
	// token in its startup log as the credential for this running process.
	if !rerun && p.Mode != ModeAgent {
		setupToken, setupTokenErr = i.containerSetupToken(ctx, p)
	}
	i.containerSummary(p, envPath, res.ReusedKey, setupToken, setupTokenErr)
	return nil
}

// warnAboutRootlessSocket names the one combination that looks right and is
// not: the image runs as an unprivileged uid, which cannot use a socket that
// belongs to the operator's own user.
// ImageUID is the account the published image runs as. It is not an operator's
// account and does not exist on the host, which is why a container is given a
// group when it is created rather than joined to one afterwards.
const ImageUID = 65532

// containerSocketVerdict is what the image's account will be able to do with a
// socket, decided from the socket and the gid the deployment would add.
type containerSocketVerdict int

const (
	// socketUsable: the container will be able to open it.
	socketUsable containerSocketVerdict = iota
	// socketWrongGID: the group bits are right and the gid being added is not
	// the one that owns the socket, which is a one-line fix in the env file.
	socketWrongGID
	// socketRootGroup: the socket belongs to group root. A container could only
	// reach it by being given the root group, which Zoomies will not do.
	socketRootGroup
	// socketNoGroupBits: no gid can help; the mode itself is the refusal.
	socketNoGroupBits
)

// judgeContainerSocket is the whole decision, kept apart from the printing so
// every branch can be checked without a container runtime, a second group or
// root.
func judgeContainerSocket(facts socketFacts, dockerGID int) containerSocketVerdict {
	// Exactly what the container will be: the image's uid, plus the one group
	// the deployment adds -- and only when there is one to add.
	acct := account{uid: ImageUID}
	if dockerGID > 0 {
		acct.groups = []int{dockerGID}
	}
	switch {
	case canOpen(facts, acct):
		return socketUsable
	case facts.gid == 0:
		return socketRootGroup
	case joinable(facts, acct):
		return socketWrongGID
	default:
		return socketNoGroupBits
	}
}

// checkContainerSocketAccess proves, before the container is started, that the
// image's account will be able to open the socket that is about to be mounted
// into it.
//
// The container equivalent of the native install's group check, and the one
// that matters more: there is no usermod to fall back on afterwards, because
// the image's account does not exist on the host. A wrong or missing DOCKER_GID
// produces a container that comes up healthy, reports every backend as
// unavailable, and leaves its pools queueing forever.
func (i *Installer) checkContainerSocketAccess(p Plan) {
	socket := SocketPathOf(p.DockerHost)
	if !p.runsRunners() || p.Backend == store.BackendProcess || socket == "" {
		return
	}
	facts, ok := statSocket(socket)
	if !ok {
		i.ui.note("no socket at " + socket + " to check yet; the agent re-probes as it runs, so it will start taking work once the daemon is up.")
		return
	}

	switch judgeContainerSocket(facts, p.DockerGID) {
	case socketUsable:
		if p.DockerGID > 0 {
			i.ui.ok(fmt.Sprintf("the container joins group %d, which owns %s", p.DockerGID, socket))
		} else {
			i.ui.ok("the container can use " + socket)
		}
	case socketWrongGID:
		i.ui.warn(fmt.Sprintf("the container would run as uid %d with no access to %s, so no runner could be created",
			ImageUID, socket))
		if p.Deployment == DeploymentCompose {
			// Compose reads the gid from the env file at up time.
			i.ui.note(fmt.Sprintf("set DOCKER_GID=%d in %s and bring the deployment up again -- that gid owns the socket on this host.",
				facts.gid, EnvFileFor(p.Deployment)))
			return
		}
		// A plain container carries the group in its own run command, so the
		// env file is not where this one lives.
		i.ui.note(fmt.Sprintf("recreate the container with --group-add %d, which is the gid that owns the socket on this host.", facts.gid))
	case socketRootGroup:
		i.ui.warn(socket + " belongs to group root, so the only gid that would reach it is 0")
		i.ui.note("Zoomies will not put a container in the root group. Give the socket a group of its own -- `sudo groupadd docker`, then restart the daemon so it takes the group -- or run a rootless daemon and point " + dockerHostSetting(p) + " at its socket.")
	case socketNoGroupBits:
		i.ui.warn(fmt.Sprintf("%s is mode %04o, which grants nothing to its group, so no gid added to the container can open it",
			socket, facts.mode.Perm()))
		i.ui.note("run a rootless daemon and point " + dockerHostSetting(p) + " at its socket, or change the socket's own permissions.")
	}
}

// dockerHostSetting names where the socket is set for this deployment: an
// agent's environment, or a controller's settings, which live in its database.
func dockerHostSetting(p Plan) string {
	if p.Mode == ModeAgent {
		return "ZOOMIES_DOCKER_HOST"
	}
	return "the Docker socket setting (agent.docker_host)"
}

func (i *Installer) warnAboutRootlessSocket(p Plan) {
	if !p.Rootless || !p.runsRunners() || p.Backend == store.BackendProcess {
		return
	}
	i.ui.warn("the socket at " + p.DockerHost + " belongs to your user, and the image runs as uid 65532.")
	i.ui.note("if runners fail to start, point " + dockerHostSetting(p) + " at the system socket, or run the")
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

	if p.Mode != ModeAgent {
		if rerun {
			// The settings are stored with the controller stopped: a running
			// one holds the database's lock, and has already read what this
			// would change.
			name, args := ComposeArgs(p.ComposeCommand, file, "stop", "zoomies")
			if err := i.stream(ctx, p.DeployDir, name, args...); err != nil {
				return fmt.Errorf("installer: stopping the controller to store its settings: %w", err)
			}
		}
		name, args := ComposeArgs(p.ComposeCommand, file, "run", "--rm", "--no-deps", "-T", "zoomies", "config", "import-env")
		if err := i.storeSettings(ctx, p, name, args); err != nil {
			return err
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
		// Named outright because a container made by an older binary carries no
		// stop timeout of its own, and this is the stop that replaces it.
		_, _ = runCommand(ctx, "docker", "stop", "--time", strconv.Itoa(int(serviceStopTimeout.Seconds())), ContainerName)
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

	if p.Mode != ModeAgent {
		// A one-off container of the same image, on the same volume, with
		// nothing published and no network: it stores the settings and goes.
		importArgs := []string{"run", "--rm", "-i", "--network", "none", "--env-file", envPath,
			"--volume", VolumeName + ":" + ContainerStateDir, p.Image, "config", "import-env"}
		if err := i.storeSettings(ctx, p, "docker", importArgs); err != nil {
			return err
		}
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

// storeSettings puts the controller's settings in its database before it
// first starts, through `zoomies config import-env` in a one-off container of
// the image being deployed. The .env holds only what opens the database, so a
// controller started without them would come up on the defaults -- listening
// on the container's loopback, where the published port cannot reach it.
//
// The image's own binary writes its own database, which is why this is a
// container and not this process: the volume may be somewhere this host
// cannot open, and the schema is the image's to create. The variables go in on
// standard input, where no other process can read them.
func (i *Installer) storeSettings(ctx context.Context, p Plan, name string, args []string) error {
	input, err := SettingsEnv(EnvSpecFor(p))
	if err != nil {
		return err
	}
	if input == "" {
		return nil
	}
	i.ui.step("Storing the controller's settings in its database")
	i.ui.blank()
	if err := i.streamInput(ctx, p.DeployDir, input, name, args...); err != nil {
		return fmt.Errorf("installer: storing the controller's settings: %w\n      "+
			"the image must be of this release or later, which has `zoomies config import-env`", err)
	}
	i.ui.blank()
	i.ui.ok("stored them; change them on the settings page from now on")
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

// streamInput is stream with input on the command's standard input.
func (i *Installer) streamInput(ctx context.Context, dir, input, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not on PATH, so this deployment cannot be brought up here: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
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

var setupTokenLogPattern = regexp.MustCompile(`(?i)setup token[^a-z2-7]+([a-z2-7]{32})`)

// setupTokenFromLogs extracts the controller's one-time first-admin credential
// from either its human banner or its structured log line. Both are emitted at
// startup, before the health endpoint begins answering.
func setupTokenFromLogs(logs string) string {
	matches := setupTokenLogPattern.FindAllStringSubmatch(logs, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

func (i *Installer) containerSetupToken(ctx context.Context, p Plan) (string, error) {
	var (
		out string
		err error
	)
	if p.Deployment == DeploymentCompose {
		name, args := ComposeArgs(p.ComposeCommand, filepath.Join(p.DeployDir, ComposeFileName), "logs", "--tail", "100")
		out, err = runCommand(ctx, name, args...)
	} else {
		out, err = runCommand(ctx, "docker", "logs", "--tail", "100", ContainerName)
	}
	if err != nil {
		return "", err
	}
	if token := setupTokenFromLogs(out); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("the running controller's startup log did not contain a setup token")
}

// containerSummary is the last thing the operator reads. It is the handful of
// commands they will actually want next, and nothing else.
func (i *Installer) containerSummary(p Plan, envPath string, reusedKey bool, setupToken string, setupTokenErr error) {
	file := filepath.Join(p.DeployDir, ComposeFileName)
	i.ui.blank()
	i.ui.step("Installation complete")
	i.ui.ok("Zoomies is installed and the container deployment is running.")
	i.ui.blank()
	i.ui.step("Deployment")
	// The helper owns the column, and it is not faint: the URL an operator must
	// open and the file holding their encryption key were the dimmest lines on
	// the screen that told them about both.
	i.ui.field("status", "running; local health check completed")
	i.ui.field("mode", string(p.Mode))
	i.ui.field("public URL", p.ExternalURL)
	i.ui.field("host port", net.JoinHostPort(p.PublishAddr, strconv.Itoa(p.PublishedPort)))
	i.ui.field("image", p.Image)
	i.ui.field("environment", envPath+" (mode 0600; contains the encryption key)")
	if p.Deployment == DeploymentCompose {
		i.ui.field("deployment", "Docker Compose")
		i.ui.field("compose file", file)
	} else {
		i.ui.field("deployment", "Docker container")
		i.ui.field("container", ContainerName)
	}
	if p.runsRunners() {
		i.ui.field("runner backend", fmt.Sprintf("%s; capacity %d", p.Backend, p.Capacity))
	}
	i.ui.field("database", "volume "+VolumeName+" (survives restart, update and down)")
	if p.Mode != ModeAgent {
		i.ui.field("Web UI", p.ExternalURL)
		switch {
		case setupToken != "":
			i.ui.field("Web UI key", setupToken)
			i.ui.field("", "paste this into the first-run page to create the administrator")
			i.ui.field("", "one-time bootstrap credential; it changes if the controller restarts")
		case setupTokenErr != nil:
			i.ui.field("Web UI key", "run `zoomies logs` and copy the line beginning \"setup token\"")
			i.ui.note("the installer could not read it automatically: " + setupTokenErr.Error())
		default:
			i.ui.field("Web UI key", "not required on an existing installation")
		}
	}
	i.ui.blank()

	if !reusedKey {
		i.ui.warn("Back up " + envPath + " now: it holds this deployment's encryption key.")
		i.ui.note("without it the stored GitHub App private key and every webhook secret are lost.")
		i.ui.blank()
	}

	if p.PublishAddr == "127.0.0.1" {
		i.ui.note("The container is published on loopback only, so reach it from your laptop with:")
		i.ui.note(fmt.Sprintf("  ssh -L %d:127.0.0.1:%d %s", p.PublishedPort, p.PublishedPort, hostname()))
		i.ui.blank()
	}

	// A container keeps its database in a volume this process cannot reach, so
	// none of the three things a native install does for the operator -- the
	// administrator, the GitHub App, the first pool -- happened here. Naming
	// them, in order, with the exact address of each, is the whole handover.
	sug := SuggestPool(i.det, p.Backend, p.Capacity, p.Embedded)
	// The same four steps, in the same order and with the same names, as the
	// browser's own checklist and the first-run card it hands over from.
	i.ui.step("Next steps")
	i.ui.field("  1.", "Create the first administrator")
	i.ui.field("", p.ExternalURL)
	i.ui.field("  2.", "Connect GitHub -- nothing can run until an App is installed")
	i.ui.field("", p.ExternalURL+"/installations")
	i.ui.field("  3.", "Create a pool -- suggested for this "+i.det.Arch+" host: "+sug.Name)
	i.ui.field("", p.ExternalURL+"/pools/new")
	i.ui.field("  4.", "Point a workflow at it")
	i.ui.field("", "runs-on: "+sug.RunsOn())
	i.ui.blank()

	i.ui.step("Manage this deployment")
	for _, line := range i.deploymentCommands(p) {
		i.ui.field("", line)
	}
	i.ui.field("uninstall", "zoomies uninstall (add --volumes only when the database should also be deleted)")
}

// deploymentCommands uses the binary's record-aware lifecycle commands rather
// than making the operator remember Compose file and environment arguments.
func (i *Installer) deploymentCommands(p Plan) []string {
	return []string{
		"status    zoomies deployment status",
		"logs      zoomies logs",
		"start     zoomies deployment start",
		"stop      zoomies deployment stop",
		"restart   zoomies deployment restart",
		"update    zoomies update",
		"down      zoomies deployment down  (keeps the database volume)",
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

// ContainerImageRemoveCommand removes the exact image recorded at install
// time. It deliberately does not match a repository prefix: another Zoomies
// deployment, or an operator's custom runner image, may share this host.
func ContainerImageRemoveCommand(rec DeploymentRecord) (string, []string) {
	runtime := "docker"
	if len(rec.ComposeCommand) > 0 {
		switch rec.ComposeCommand[0] {
		case "docker", "podman":
			runtime = rec.ComposeCommand[0]
		}
	}
	return runtime, []string{"image", "rm", rec.Image}
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
	if rec.Image == "" {
		return
	}
	name, args := ContainerImageRemoveCommand(rec)
	if _, err := runCommand(ctx, name, args...); err != nil {
		u.warn("could not remove the " + rec.Image + " image: " + err.Error())
		u.note("remove it by hand with: " + name + " " + strings.Join(args, " "))
	} else {
		record("removed the %s container image", rec.Image)
	}
}

func volumeOr(rec DeploymentRecord) string {
	if rec.Volume != "" {
		return rec.Volume
	}
	return VolumeName
}
