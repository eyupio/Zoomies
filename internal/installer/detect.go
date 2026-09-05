package installer

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// InitSystem names the supervisor this host uses, which decides whether the
// installer can offer to write a unit file or has to print a compose file
// instead.
type InitSystem string

const (
	// InitSystemd is the common case on Debian, Ubuntu and Fedora.
	InitSystemd InitSystem = "systemd"
	// InitLaunchd is macOS, where Zoomies is a development controller.
	InitLaunchd InitSystem = "launchd"
	// InitOpenRC is Alpine. Zoomies does not write OpenRC scripts in v1, so it
	// prints a compose file and says why.
	InitOpenRC InitSystem = "openrc"
	// InitNone covers containers and anything unrecognised.
	InitNone InitSystem = "none"
)

// RuntimeInfo is what one container runtime looks like from here, including
// whether this particular user could actually use it. The distinction matters:
// a Docker socket that exists but rejects us is a completely different problem
// from one that is not there, and it has a different remedy.
type RuntimeInfo struct {
	Kind store.BackendKind
	// Available means the daemon answered a version request.
	Available bool
	// Rootless means a container escape lands on an unprivileged account
	// rather than on root, which is why it is preferred everywhere.
	Rootless bool
	Endpoint string
	Version  string
	// Detail explains an unavailable runtime in words the operator can act on.
	Detail string
	// Installed means the CLI is on PATH even though the socket is not
	// reachable, which is the "start the service" case rather than the
	// "install it" case.
	Installed bool
}

// ComposeInfo is the Docker Compose command this host can run.
//
// Compose v2 is a subcommand of docker; v1 was a separate docker-compose
// binary. Both are still in the field, so whichever answered is carried around
// as an argv prefix rather than re-derived at each call site -- which is how a
// host with only v1 ends up being told to run a v2 command it does not have.
type ComposeInfo struct {
	// Command is the argv prefix, e.g. ["docker","compose"].
	Command []string
	// Available means the command answered a version request. install.sh's
	// finding is trusted here without re-running it: the script asked this
	// same host moments ago.
	Available bool
	// Detail explains an absent compose in words the operator can act on.
	Detail string
}

// String renders the command the way an operator would type it.
func (c ComposeInfo) String() string { return strings.Join(c.Command, " ") }

// ParseComposeCommand turns install.sh's --detected-compose value into an argv
// prefix.
func ParseComposeCommand(s string) []string { return strings.Fields(s) }

// detectCompose settles which compose command this host has.
//
// install.sh already ran both probes before any privilege change, so its
// answer wins. The probing here is for `zoomies init` run on its own, and it
// asks in the same order the script does: the v2 plugin first, because a host
// with both should use the one that is still maintained.
func detectCompose(ctx context.Context, hint string) ComposeInfo {
	if cmd := ParseComposeCommand(hint); len(cmd) > 0 {
		return ComposeInfo{Command: cmd, Available: true}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if lookPath("docker") != "" {
		if _, err := runCommand(ctx, "docker", "compose", "version"); err == nil {
			return ComposeInfo{Command: []string{"docker", "compose"}, Available: true}
		}
	}
	if lookPath("docker-compose") != "" {
		if _, err := runCommand(ctx, "docker-compose", "--version"); err == nil {
			return ComposeInfo{Command: []string{"docker-compose"}, Available: true}
		}
	}
	return ComposeInfo{Detail: "neither `docker compose` nor `docker-compose` answered here"}
}

// PortStatus records whether the installer could bind a port, so that the bind
// prompt can offer another one instead of failing at first start.
type PortStatus struct {
	Port int
	Free bool
	// Detail carries the bind error, which distinguishes "in use" from
	// "permission denied" on a privileged port.
	Detail string
}

// ExistingInstall lists the parts of a previous installation found on this
// host. Every field is either an existing path or empty, so the caller can
// print exactly what is there without stat-ing anything again.
type ExistingInstall struct {
	ConfigFile    string
	KeyFile       string
	Database      string
	Unit          string
	AgentUnit     string
	AgentState    string
	Binary        string
	BinaryVersion string
}

// Present reports whether anything of a previous install survives. It is the
// signal that `zoomies init` should offer an upgrade rather than a fresh
// install.
func (e ExistingInstall) Present() bool {
	return e.ConfigFile != "" || e.KeyFile != "" || e.Database != "" ||
		e.Unit != "" || e.AgentUnit != "" || e.AgentState != ""
}

// HasState reports whether an existing install holds data that would be
// destroyed by starting again: the encryption key and the database. Those two
// are the ones that need a typed confirmation rather than a yes/no.
func (e ExistingInstall) HasState() bool { return e.KeyFile != "" || e.Database != "" }

// Items lists what was found, one line each, for the confirmation screen.
func (e ExistingInstall) Items() []string {
	var out []string
	add := func(label, path string) {
		if path != "" {
			out = append(out, fmt.Sprintf("%-16s %s", label, path))
		}
	}
	add("config", e.ConfigFile)
	add("encryption key", e.KeyFile)
	add("database", e.Database)
	add("service", e.Unit)
	add("agent service", e.AgentUnit)
	add("agent creds", e.AgentState)
	if e.Binary != "" {
		v := e.BinaryVersion
		if v == "" {
			v = "unknown version"
		}
		out = append(out, fmt.Sprintf("%-16s %s (%s)", "binary", e.Binary, v))
	}
	return out
}

// Detection is everything the installer knows about this host before it asks
// the operator anything. Run prints it once, at the top, because half of
// setting up a fleet controller is finding out what the machine already has.
type Detection struct {
	OS       string
	Arch     string
	Distro   string
	Init     InitSystem
	Hostname string

	// User, UID and GID describe who is running the installer, which decides
	// whether a service user can be created at all.
	User string
	UID  int
	GID  int
	Root bool

	Docker RuntimeInfo
	Podman RuntimeInfo

	// HasSystemd, HasLaunchd and HasCompose say which supervisors this host
	// can actually run, so the installer never shells out to a binary that is
	// not there.
	HasSystemd bool
	HasLaunchd bool
	HasCompose bool

	// Compose is the Docker Compose command this host has, which decides
	// whether a compose deployment can be offered at all.
	Compose ComposeInfo

	// Ports records the bind check for the ports setup would like to use.
	Ports []PortStatus

	Existing ExistingInstall

	// ConfigDir and StateDir are where this run will put things.
	ConfigDir string
	StateDir  string

	// Interactive is false when there is no terminal to prompt on, which
	// forces the non-interactive path.
	Interactive bool

	// BinaryPath is the zoomies binary the service unit will point at.
	BinaryPath string
}

// defaultPorts are the ports setup checks: the default listener and the
// privileged HTTPS port an operator may want instead.
var defaultPorts = []int{8080, 443}

// Detect works out everything install.sh could not tell us, and everything
// needed when `zoomies init` is run directly rather than through the script.
//
// Flags from install.sh always win over local probing: the script ran before
// any privilege change and its answers describe the same host, so re-deriving
// them here could only disagree confusingly.
func Detect(ctx context.Context, opts Options) Detection {
	d := Detection{
		OS:          firstNonEmpty(opts.DetectedOS, runtime.GOOS),
		Arch:        firstNonEmpty(opts.DetectedArch, runtime.GOARCH),
		Distro:      firstNonEmpty(opts.DetectedDistro, readDistroID()),
		Hostname:    hostname(),
		UID:         os.Geteuid(),
		GID:         os.Getegid(),
		ConfigDir:   opts.configDir(),
		StateDir:    opts.stateDir(),
		Interactive: opts.interactive(),
		BinaryPath:  opts.binaryPath(),
	}
	d.Root = d.UID == 0
	d.User = currentUserName(d.UID)

	if opts.DetectedInit != "" {
		d.Init = InitSystem(opts.DetectedInit)
	} else {
		d.Init = detectInit()
	}
	d.HasSystemd = d.Init == InitSystemd && lookPath("systemctl") != ""
	d.HasLaunchd = d.Init == InitLaunchd && lookPath("launchctl") != ""
	d.Compose = detectCompose(ctx, opts.DetectedCompose)
	d.HasCompose = d.Compose.Available

	d.Docker = probeRuntime(ctx, store.BackendDocker, socketHintFor(opts, store.BackendDocker))
	d.Podman = probeRuntime(ctx, store.BackendPodman, socketHintFor(opts, store.BackendPodman))
	// install.sh worked out rootlessness from the socket's path before any
	// privilege change. A daemon that does not advertise it in /info -- some
	// Podman builds do not -- would otherwise be treated as a root one and
	// warned about for no reason.
	if opts.DetectedRootless {
		markRootless(&d.Docker, opts)
		markRootless(&d.Podman, opts)
	}

	for _, p := range defaultPorts {
		d.Ports = append(d.Ports, checkPort(p))
	}
	d.Existing = ExistingInstallAt(d.ConfigDir, d.StateDir, d.BinaryPath)
	return d
}

// markRootless applies install.sh's finding to the runtime it described, and
// only when the probe landed on the same socket the script was talking about.
func markRootless(r *RuntimeInfo, opts Options) {
	if !r.Available || r.Rootless {
		return
	}
	hint := socketHintFor(opts, r.Kind)
	if hint != "" && hint == r.Endpoint {
		r.Rootless = true
	}
}

// socketHintFor returns the socket install.sh found, but only for the runtime
// it belongs to; handing a Podman socket to the Docker probe would report a
// Docker daemon that is not there.
func socketHintFor(opts Options, kind store.BackendKind) string {
	if opts.DetectedSocket == "" {
		return ""
	}
	if !strings.HasPrefix(opts.DetectedRuntime, string(kind)) {
		return ""
	}
	if strings.Contains(opts.DetectedSocket, "://") {
		return opts.DetectedSocket
	}
	return "unix://" + opts.DetectedSocket
}

// probeRuntime asks one runtime what it can do here. A missing daemon is not
// an error: the installer has to be able to report it and carry on offering
// the other backends.
func probeRuntime(ctx context.Context, kind store.BackendKind, hint string) RuntimeInfo {
	out := RuntimeInfo{Kind: kind}
	cli := "docker"
	if kind == store.BackendPodman {
		cli = "podman"
	}
	out.Installed = lookPath(cli) != ""

	var (
		b   backend.Backend
		err error
	)
	opts := backend.DockerOptions{Host: hint}
	if kind == store.BackendPodman {
		b, err = backend.NewPodman(opts)
	} else {
		b, err = backend.NewDocker(opts)
	}
	if err != nil {
		out.Detail = err.Error()
		return out
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	info := b.Probe(ctx)
	out.Available = info.Available
	out.Rootless = info.Rootless
	out.Endpoint = info.Endpoint
	out.Version = info.Version
	out.Detail = info.Detail
	if !out.Available && out.Detail == "" && out.Endpoint != "" {
		if err := backend.CanUseDockerSocket(out.Endpoint); err != nil {
			out.Detail = err.Error()
		}
	}
	return out
}

// probeTimeout bounds each runtime probe. A daemon that has not answered in
// five seconds is not one the operator wants to wait on during setup.
const probeTimeout = 5 * time.Second

// detectInit works out the supervisor without help from install.sh, which is
// the path taken when `zoomies init` is run on its own.
func detectInit() InitSystem {
	if runtime.GOOS == "darwin" {
		if lookPath("launchctl") != "" {
			return InitLaunchd
		}
		return InitNone
	}
	if backend.HasSystemd() {
		return InitSystemd
	}
	if _, err := os.Stat("/sbin/openrc-run"); err == nil {
		return InitOpenRC
	}
	return InitNone
}

// ExistingInstallAt reports which pieces of a previous installation are on
// disk. It takes its directories as arguments so that it can be tested against
// a temporary directory rather than against the real /etc.
func ExistingInstallAt(configDir, stateDir, binary string) ExistingInstall {
	var e ExistingInstall
	if p := filepath.Join(configDir, "zoomies.yaml"); exists(p) {
		e.ConfigFile = p
	}
	if p := filepath.Join(configDir, "encryption.key"); exists(p) {
		e.KeyFile = p
	}
	if p := filepath.Join(stateDir, "zoomies.db"); exists(p) {
		e.Database = p
	}
	if p := filepath.Join(stateDir, "work", "agent.json"); exists(p) {
		e.AgentState = p
	}
	if p := SystemdUnitPath(UnitController); exists(p) {
		e.Unit = p
	}
	if p := SystemdUnitPath(UnitAgent); exists(p) {
		e.AgentUnit = p
	}
	if binary != "" && exists(binary) {
		e.Binary = binary
		e.BinaryVersion = binaryVersion(binary)
	}
	return e
}

// binaryVersion asks an installed binary what it is. It is best effort: an
// unreadable or foreign binary simply has no version to report.
func binaryVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version", "--short").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkPort reports whether the installer could bind a port on all interfaces.
// Binding is the only honest test: a port can be free in /proc and still be
// refused by a container's network namespace or by a privileged-port rule.
func checkPort(port int) PortStatus {
	s := PortStatus{Port: port}
	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		s.Detail = err.Error()
		return s
	}
	_ = ln.Close()
	s.Free = true
	return s
}

// PortFree reports whether a TCP port can be bound on host. An empty host
// means every interface, which is the stricter check of the two.
func PortFree(host string, port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// BindFree reports whether a "host:port" bind address is available, returning
// the parse error for an address that is not one.
func BindFree(bind string) (bool, error) {
	host, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return false, fmt.Errorf("%q is not a host:port address: %w", bind, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return false, fmt.Errorf("%q does not end in a port number: %w", bind, err)
	}
	return PortFree(host, port), nil
}

// NextFreePort suggests a port near the one the operator wanted, so a busy
// 8080 becomes an offer of 8081 rather than a dead end.
func NextFreePort(host string, from int, tries int) (int, bool) {
	for p := from; p < from+tries && p < 65536; p++ {
		if PortFree(host, p) {
			return p, true
		}
	}
	return 0, false
}

// Lines renders the detection as the block Run prints at the start. The shape
// deliberately matches install.sh's own "Looking around" output, so the two
// halves of one install read as one program.
// Fields is Lines without the padding: the same key/value pairs, for a caller
// that owns the column itself. `ui.field` is that caller, and it is not faint
// -- which the host report ought not to be either, given install.sh has just
// printed the same facts at full weight.
func (d Detection) Fields() []Field {
	out := make([]Field, 0, 12)
	for _, line := range d.Lines() {
		key := strings.TrimSpace(line[:min(12, len(line))])
		value := ""
		if len(line) > 12 {
			value = line[12:]
		}
		out = append(out, Field{Key: key, Value: value})
	}
	return out
}

// Field is one key/value row of the host report.
type Field struct {
	Key   string
	Value string
}

func (d Detection) Lines() []string {
	out := []string{
		fmt.Sprintf("%-12s%s/%s (%s)", "os", d.OS, d.Arch, d.Distro),
		fmt.Sprintf("%-12s%s", "init", d.Init),
		fmt.Sprintf("%-12s%s (uid %d)%s", "user", d.User, d.UID, map[bool]string{true: " -- root", false: ""}[d.Root]),
		fmt.Sprintf("%-12s%s", "config", d.ConfigDir),
		fmt.Sprintf("%-12s%s", "state", d.StateDir),
	}
	out = append(out, runtimeLine(d.Docker), runtimeLine(d.Podman))
	if d.Compose.Available {
		out = append(out, fmt.Sprintf("%-12s%s", "compose", d.Compose))
	}
	for _, p := range d.Ports {
		if !p.Free {
			out = append(out, fmt.Sprintf("%-12sport %d is not available: %s", "ports", p.Port, p.Detail))
		}
	}
	if !d.Interactive {
		out = append(out, fmt.Sprintf("%-12sno terminal attached; answers must come from the answer file or flags", "prompts"))
	}
	if d.Existing.Present() {
		out = append(out, fmt.Sprintf("%-12sa previous installation is present", "existing"))
	}
	return out
}

func runtimeLine(r RuntimeInfo) string {
	label := string(r.Kind)
	switch {
	case r.Available && r.Rootless:
		return fmt.Sprintf("%-12srootless, %s -- %s", label, r.Version, r.Endpoint)
	case r.Available:
		return fmt.Sprintf("%-12sroot, %s -- %s", label, r.Version, r.Endpoint)
	case r.Installed:
		return fmt.Sprintf("%-12sinstalled but its socket is not reachable: %s", label, firstLine(r.Detail))
	default:
		return fmt.Sprintf("%-12snot installed", label)
	}
}

// CanUseSocket reports whether this user can talk to a runtime's socket, and
// why not when it cannot. The installer uses it to decide whether the service
// user needs to be added to a group.
func CanUseSocket(endpoint string) error { return backend.CanUseDockerSocket(endpoint) }

// dockerGroupGID returns the numeric gid of the docker group, or 0 when the
// host has no such group. A root Docker socket is usually reachable only for
// members of that group, and the systemd unit has to name it.
func dockerGroupGID() int {
	g, err := user.LookupGroup("docker")
	if err != nil {
		return 0
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0
	}
	return gid
}

func exists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func lookPath(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	return p
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	return h
}

func currentUserName(uid int) string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return "uid " + strconv.Itoa(uid)
}

// readDistroID reads ID= from /etc/os-release, the same field install.sh uses,
// so that a directly-run `zoomies init` reports the distro the same way.
func readDistroID() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ID="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return "unknown"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// userExists reports whether a local account of this name is present, which is
// how uninstall avoids running userdel for an account that is already gone.
func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}
