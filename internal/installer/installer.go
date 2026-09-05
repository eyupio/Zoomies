// Package installer is the interactive setup behind `zoomies init`,
// `zoomies uninstall` and `zoomies agent join`.
//
// The goal it is written against is a specific one: take a fresh Ubuntu,
// Debian, Fedora or Alpine host -- or macOS, for a development controller --
// from nothing to a running controller with a registered GitHub App, in one
// sitting, without the operator having to read the documentation first.
//
// Two rules shape the code. The first is that install.sh has already detected
// the host, so nothing here asks a question the script answered. The second is
// that the prompting is deliberately thin: every decision lives in a plain
// function that takes a struct and returns a struct, and the huh forms only
// fill that struct in. That is what makes the unattended path the same code as
// the interactive one rather than a second implementation that drifts.
package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// ErrAborted is returned when the operator pressed ctrl-c at a prompt. It is a
// distinct error so that a caller can exit quietly rather than printing a
// failure for something the operator chose.
var ErrAborted = errors.New("installer: setup cancelled")

// ErrAbortedDirty is ErrAborted after the host has already been changed. The
// two are distinct because "Nothing was changed" is a true and calming thing to
// say at the third question and a false one at the ninth -- by which point a
// system account exists, an encryption key is on disk and a GitHub App may
// have been created on the operator's organisation. Somebody told the machine
// is untouched does not go looking for what to undo.
var ErrAbortedDirty = errors.New("installer: setup cancelled after changes were made")

// Mode is what this host is being set up as.
type Mode string

const (
	// ModeSingle is a controller with an embedded agent: one process, one VM,
	// the common case.
	ModeSingle Mode = "single"
	// ModeController is a controller that runs no runners itself; hosts join
	// it separately.
	ModeController Mode = "controller"
	// ModeAgent is a runner host joining an existing controller.
	ModeAgent Mode = "agent"
)

// ParseMode validates a --mode value, naming the alternatives when it is not
// one of them.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeSingle:
		return ModeSingle, nil
	case ModeController:
		return ModeController, nil
	case ModeAgent:
		return ModeAgent, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("installer: %q is not an install mode; use single (controller plus an embedded agent), controller, or agent", s)
	}
}

// Options carries every flag install.sh passes, plus the few things a caller
// inside this process needs to supply.
//
// The Detected* fields are the script's findings. They are trusted over local
// probing: the script ran on this host moments ago, and re-deriving them here
// could only produce a confusing disagreement.
type Options struct {
	DetectedOS       string
	DetectedArch     string
	DetectedDistro   string
	DetectedInit     string
	DetectedRuntime  string
	DetectedSocket   string
	DetectedRootless bool
	// DetectedCompose is the compose command install.sh found, e.g.
	// "docker compose" or "docker-compose".
	DetectedCompose string
	// InstalledBinary is where install.sh put the binary, which is what the
	// service unit will exec.
	InstalledBinary string

	// Mode, Deployment, ControllerURL and JoinToken come from the command
	// line. When Mode or Deployment is empty the installer asks -- install.sh
	// deliberately does not, so that there is one place these questions are
	// worded.
	Mode          Mode
	Deployment    Deployment
	ControllerURL string
	JoinToken     string

	// AnswersFile is a YAML answer file for unattended setup.
	AnswersFile string
	// NonInteractive forbids prompting. A missing answer is then an error
	// naming the key, never a silent default.
	NonInteractive bool
	// AssumeYes accepts confirmations that are not destructive. Anything that
	// would overwrite an encryption key or a database still asks.
	AssumeYes bool

	// ConfigDir and StateDir override the platform defaults. Tests set them;
	// operators normally do not.
	ConfigDir string
	StateDir  string

	Out    io.Writer
	In     io.Reader
	Logger *slog.Logger
}

func (o Options) configDir() string {
	if o.ConfigDir != "" {
		return o.ConfigDir
	}
	return config.ConfigDir()
}

func (o Options) stateDir() string {
	if o.StateDir != "" {
		return o.StateDir
	}
	return config.StateDir()
}

func (o Options) binaryPath() string {
	if o.InstalledBinary != "" {
		return o.InstalledBinary
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	return "/usr/local/bin/zoomies"
}

// interactive reports whether there is a terminal to prompt on. install.sh
// reconnects /dev/tty when the script was piped into sh, so a false here
// really does mean nobody is watching.
func (o Options) interactive() bool {
	if o.NonInteractive {
		return false
	}
	return isTerminal(os.Stdin)
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Installer runs the setup. One Installer is one `zoomies init`.
type Installer struct {
	opts    Options
	out     io.Writer
	in      io.Reader
	log     *slog.Logger
	ui      *ui
	answers *Answers
	det     Detection
	// interactive is decided once at construction, so a mid-run change of
	// heart cannot leave half the questions asked.
	interactive bool
	// written records, in order, everything this run has put on the host. It
	// is what an abort prints instead of claiming nothing happened, and what a
	// failure prints so the operator knows what to clean up.
	written []string
}

// wrote records a change to the host and reports it in one move, so a step
// cannot print that it did something without the ledger learning about it.
func (i *Installer) wrote(what string) {
	i.written = append(i.written, what)
	i.ui.ok(what)
}

// Written is what this run has changed on the host, oldest first.
func (i *Installer) Written() []string { return append([]string(nil), i.written...) }

// New validates the options and prepares an installer. It reads the answer
// file eagerly, because a typo in it should stop the run before anything on
// the host has been touched.
func New(opts Options) (*Installer, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Mode != "" {
		m, err := ParseMode(string(opts.Mode))
		if err != nil {
			return nil, err
		}
		opts.Mode = m
	}
	if opts.Deployment != "" {
		d, err := ParseDeployment(string(opts.Deployment))
		if err != nil {
			return nil, err
		}
		opts.Deployment = d
	}

	i := &Installer{
		opts:        opts,
		out:         opts.Out,
		in:          opts.In,
		log:         opts.Logger.With("component", "installer"),
		ui:          newUI(opts.Out),
		interactive: opts.interactive(),
	}
	if opts.AnswersFile != "" {
		a, err := Load(opts.AnswersFile)
		if err != nil {
			return nil, err
		}
		i.answers = a
	}
	return i, nil
}

// Run executes the setup, printing a line per step that the operator can act
// on. It is safe to run again on a host that already has Zoomies: the existing
// installation is detected and an upgrade offered instead of a reinstall.
func (i *Installer) Run(ctx context.Context) error {
	i.det = Detect(ctx, i.opts)

	// The same heading and the same column install.sh just used, at the same
	// weight: an operator has read this inventory once already, seconds ago,
	// and the second copy under a different name in a fainter style reads as
	// two programs disagreeing rather than one carrying on.
	i.ui.step("Checking this host")
	for _, f := range i.det.Fields() {
		i.ui.field(f.Key, f.Value)
	}
	i.ui.blank()

	if !i.interactive && !i.opts.NonInteractive {
		i.ui.warn("no terminal is attached, so setup is continuing without prompts.")
		i.ui.note("every answer must come from --answers or from flags; missing ones are listed below.")
	}

	mode, err := i.resolveMode(ctx)
	if err != nil {
		return err
	}
	if mode == ModeAgent {
		return i.runAgent(ctx)
	}

	plan, err := i.resolvePlan(ctx, mode)
	if err != nil {
		return err
	}
	if plan.Upgrade {
		return i.runUpgrade(ctx, plan)
	}
	// The last moment at which nothing has been written. Everything past here
	// creates accounts, directories and files.
	proceed, err := i.stepReview(ctx, plan)
	if err != nil {
		return err
	}
	if !proceed {
		return nil
	}
	if plan.Deployment.Containerised() {
		return i.runContainer(ctx, plan)
	}
	return i.runInstall(ctx, plan)
}

// stepReview shows the whole plan and asks. It returns false when the operator
// chose to stop, which is not an error: nothing has been written, and telling
// them so is the point of the screen.
//
// An unattended run is not asked, but it is still shown the block, so the log
// of an automated install records the plan it acted on.
func (i *Installer) stepReview(ctx context.Context, p Plan) (bool, error) {
	i.ui.blank()
	i.ui.step("Ready to install")
	for _, line := range p.Review() {
		i.ui.field(line.Key, line.Value)
	}
	i.ui.blank()
	i.ui.note("writes")
	for _, path := range p.Writes() {
		i.ui.field("", path)
	}
	if !p.Deployment.Containerised() {
		i.ui.field("", "the "+p.ServiceUser+" system account, if it does not exist")
	}
	i.ui.blank()

	if !i.interactive || i.opts.AssumeYes {
		i.ui.note("nothing is asked here: this run has no terminal, or --yes was given.")
		i.ui.blank()
		return true, nil
	}

	const (
		optGo     = "go"
		optAgain  = "again"
		optCancel = "cancel"
	)
	choice := optGo
	if err := i.selectOne(ctx, "Install with these settings?",
		"Nothing has been written yet. Stopping here leaves this host exactly as it is.",
		[]huh.Option[string]{
			huh.NewOption("Install (default)", optGo),
			huh.NewOption("Change an answer -- ask the questions again", optAgain),
			huh.NewOption("Stop -- nothing has been written", optCancel),
		}, &choice); err != nil {
		return false, err
	}
	switch choice {
	case optCancel:
		i.ui.blank()
		i.ui.ok("Stopped. Nothing on this host was changed.")
		return false, nil
	case optAgain:
		revised, err := i.ask(ctx, p)
		if err != nil {
			return false, err
		}
		return i.stepReview(ctx, containerise(revised))
	}
	return true, nil
}

// resolveMode settles what is being installed. install.sh's --mode wins, then
// the answer file, then the operator.
func (i *Installer) resolveMode(ctx context.Context) (Mode, error) {
	if i.opts.Mode != "" {
		return i.opts.Mode, nil
	}
	if i.answers != nil && i.answers.Mode != "" {
		return ParseMode(i.answers.Mode)
	}
	if !i.interactive {
		return ModeSingle, nil
	}

	choice := string(ModeSingle)
	err := i.selectOne(ctx, "What is this host?",
		"A single VM is the usual answer: one process running the controller and an embedded agent.",
		[]huh.Option[string]{
			huh.NewOption("Single host -- controller with an embedded agent (default)", string(ModeSingle)),
			huh.NewOption("Controller only -- runner hosts join it separately", string(ModeController)),
			huh.NewOption("Agent only -- this host just runs runners", string(ModeAgent)),
		}, &choice)
	if err != nil {
		return "", err
	}
	return ParseMode(choice)
}

// runAgent hands over to the join flow, which is the same code path
// `zoomies agent join` uses.
func (i *Installer) runAgent(ctx context.Context) error {
	// A runner host joins natively whatever --deployment says, and is told so
	// rather than quietly getting something other than it asked for: the join
	// exchanges the token for credentials written to this host's state
	// directory, and `zoomies agent` reads them from there on every start.
	if i.opts.Deployment.Containerised() {
		i.ui.warn("a runner host is set up natively, so --deployment " + string(i.opts.Deployment) + " does not apply here.")
		i.ui.note("the join writes this host's credentials to its state directory, and the agent reads them")
		i.ui.note("from there at every start. The deployment choice is a controller one.")
	}
	token := i.opts.JoinToken
	controller := i.opts.ControllerURL
	if i.answers != nil {
		if controller == "" {
			controller = i.answers.Agent.ControllerURL
		}
		if token == "" {
			t, err := i.answers.JoinToken()
			if err != nil {
				return err
			}
			token = t
		}
	}
	return Join(ctx, JoinOptions{
		ControllerURL:  controller,
		JoinToken:      token,
		ConfigDir:      i.opts.configDir(),
		StateDir:       i.opts.stateDir(),
		BinaryPath:     i.opts.binaryPath(),
		Answers:        i.answers,
		NonInteractive: !i.interactive,
		AssumeYes:      i.opts.AssumeYes,
		Out:            i.out,
		In:             i.in,
		Logger:         i.log,
		detection:      &i.det,
	})
}

// ---------------------------------------------------------------------------
// The plan
// ---------------------------------------------------------------------------

// Plan is every decision `zoomies init` needs, resolved.
//
// It is the seam between asking and doing: defaultPlan derives it from
// detection, the answer file overlays it, the prompts fill in what is left,
// and only then does anything touch the host. Config renders the zoomies.yaml
// it describes, which is why the installer's output can be tested without a
// terminal.
type Plan struct {
	Mode Mode
	// Deployment is how this host runs Zoomies: the binary under a supervisor,
	// a compose project, or a single container. It is orthogonal to Mode, and
	// it decides which of the remaining questions are worth asking at all.
	Deployment Deployment
	// Upgrade means a previous installation was found and its configuration,
	// key and database are to be kept.
	Upgrade bool

	ServiceUser  string
	ServiceGroup string
	ConfigDir    string
	StateDir     string
	ConfigFile   string
	KeyFile      string
	DBPath       string
	WorkDir      string

	Backend    store.BackendKind
	DockerHost string
	// Rootless records that the chosen socket belongs to a per-user daemon,
	// which is the difference between a container escape landing on an
	// unprivileged account and landing on root.
	Rootless bool
	// DockerGroup is the group the service must join to reach a root socket.
	DockerGroup string
	Capacity    int
	Embedded    bool

	Listen         ListenChoice
	Bind           string
	TLSMode        config.TLSMode
	TLSCertFile    string
	TLSKeyFile     string
	TLSHosts       []string
	TrustedProxies []string
	ExternalURL    string

	GitHub GitHubPlan

	// PoolName is the pool setup created, empty when it created none. The
	// summary reads it to decide between "here is your pool" and "here is the
	// command that makes one".
	PoolName string

	AdminUser string
	// adminPassword is unexported so that it cannot be printed by accident,
	// serialised into a config file, or reach a log line.
	adminPassword string

	Service       ServiceKind
	EnableService bool
	StartService  bool

	// DeployDir is where a containerised deployment's docker-compose.yml and
	// environment file are written.
	DeployDir string
	// Image is what a containerised deployment runs.
	Image string
	// ComposeCommand is this host's compose argv prefix, carried rather than
	// re-derived so that a v1-only host is never handed a v2 command line.
	ComposeCommand []string
	// PublishAddr and PublishedPort are the host side of a container's
	// listener; Bind is the inside of it.
	PublishAddr   string
	PublishedPort int
	// DockerGID is the host's docker group, which the container joins to reach
	// a root-owned socket. Zero means this host has no such group.
	DockerGID int
}

// defaultPlan is what the installer would do if the operator pressed Enter at
// every prompt: the safe answer everywhere, and the detected answer wherever
// the host has already decided.
func defaultPlan(d Detection, mode Mode) Plan {
	p := Plan{
		Mode:          mode,
		Deployment:    DefaultDeployment(d),
		ConfigDir:     d.ConfigDir,
		StateDir:      d.StateDir,
		Capacity:      defaultCapacity(),
		Embedded:      mode == ModeSingle,
		Listen:        ListenLoopback,
		TLSMode:       config.TLSOff,
		Service:       DetectServiceKind(d),
		EnableService: true,
		StartService:  true,
		AdminUser:     "admin",
	}
	p.ConfigFile = filepath.Join(p.ConfigDir, "zoomies.yaml")
	p.KeyFile = filepath.Join(p.ConfigDir, "encryption.key")
	p.DBPath = filepath.Join(p.StateDir, "zoomies.db")
	p.WorkDir = filepath.Join(p.StateDir, "work")

	p.ServiceUser, p.ServiceGroup = defaultServiceUser(d)
	p.DeployDir = p.ConfigDir
	p.Image = DefaultImage
	p.ComposeCommand = d.Compose.Command
	if c := backendChoices(d); len(c) > 0 {
		p.Backend = c[0].Kind
		p.DockerHost = c[0].Socket
		p.Rootless = c[0].Rootless
	}
	// The group that owns the chosen socket, which is not always the group
	// called "docker" -- a Podman socket, or a distribution that names it
	// something else, would otherwise be given the wrong gid to join.
	p.DockerGID = SocketGroupGID(p.DockerHost)

	port := 8080
	if !PortFree("127.0.0.1", port) {
		if next, ok := NextFreePort("127.0.0.1", port+1, 20); ok {
			port = next
		}
	}
	p.Bind = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p.ExternalURL = defaultExternalURL(p.Listen, p.Bind, p.TLSMode, d.Hostname)
	p.GitHub.APIBaseURL = "https://api.github.com"
	return p
}

// defaultServiceUser picks the account the service runs as. Only a root
// install can create a dedicated one; everything else runs as whoever is
// setting it up, which is right for a development controller on a laptop.
func defaultServiceUser(d Detection) (userName, group string) {
	if d.OS != "darwin" && d.Root {
		return "zoomies", "zoomies"
	}
	name := d.User
	if name == "" {
		name = "zoomies"
	}
	return name, name
}

func defaultCapacity() int {
	if n := runtime.NumCPU() / 2; n > 0 {
		return n
	}
	return 1
}

// ListenChoice is how the controller is reached, which is one question with
// four very different security stories rather than four separate settings.
type ListenChoice string

const (
	// ListenLoopback binds 127.0.0.1 and leaves reaching it to a reverse proxy
	// or an SSH tunnel.
	ListenLoopback ListenChoice = "loopback"
	// ListenTLSFiles serves a certificate the operator already has, which is
	// the only choice GitHub will deliver webhooks to directly.
	ListenTLSFiles ListenChoice = "tls-files"
	// ListenSelfSigned generates a certificate. Browsers warn and GitHub
	// refuses, so it is for internal use and polling.
	ListenSelfSigned ListenChoice = "self-signed"
	// ListenProxy binds every interface with TLS off, for a proxy in front.
	ListenProxy ListenChoice = "reverse-proxy"
	// ListenCloudflare is the proxy choice specialised for Cloudflare: TLS off,
	// and Cloudflare's published ranges trusted as proxies.
	ListenCloudflare ListenChoice = "cloudflare"
)

// listenOption describes one listener choice for the prompt: what it does, why
// it matters, and what choosing it costs.
type listenOption struct {
	Choice      ListenChoice
	Label       string
	Description string
}

func listenOptions() []listenOption {
	return []listenOption{
		{ListenLoopback, "Loopback only (default)",
			"Nothing off this host can reach it. Put a reverse proxy in front, or use an SSH tunnel: ssh -L 8080:127.0.0.1:8080 this-host. GitHub cannot deliver webhooks to it, so scaling falls back to the poller until you add a proxy."},
		{ListenTLSFiles, "Serve a certificate I already have",
			"Binds every interface and terminates TLS here. This is the choice GitHub will deliver webhooks to directly."},
		{ListenSelfSigned, "Generate a self-signed certificate",
			"Binds every interface. Browsers will warn, and GitHub will refuse to deliver webhooks to it -- scaling will depend on the fallback poller."},
		{ListenProxy, "Reverse proxy in front (0.0.0.0, TLS off here)",
			"The proxy terminates TLS and forwards plain HTTP. List the proxy's CIDR as a trusted proxy, or every audit entry and rate limit records the proxy's address instead of the client's."},
		{ListenCloudflare, "Cloudflare in front (0.0.0.0, TLS off here)",
			"Cloudflare terminates TLS and forwards plain HTTP. Its published edge ranges are trusted as proxies, so audit entries and rate limits record the real client instead of Cloudflare."},
	}
}

// apply writes a listener choice into the plan, including the settings that
// only make sense together with it.
func (c ListenChoice) apply(p *Plan, port int, hostname string) {
	switch c {
	case ListenLoopback:
		p.Bind = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		p.TLSMode = config.TLSOff
	case ListenTLSFiles:
		p.Bind = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
		p.TLSMode = config.TLSFiles
	case ListenSelfSigned:
		p.Bind = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
		p.TLSMode = config.TLSSelfSigned
		if len(p.TLSHosts) == 0 && hostname != "" {
			p.TLSHosts = []string{hostname}
		}
	case ListenProxy, ListenCloudflare:
		p.Bind = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
		p.TLSMode = config.TLSOff
		if c == ListenCloudflare && !slices.Contains(p.TrustedProxies, config.TrustedProxyCloudflare) {
			p.TrustedProxies = append([]string{config.TrustedProxyCloudflare}, p.TrustedProxies...)
		}
	}
	p.Listen = c
	p.ExternalURL = defaultExternalURL(c, p.Bind, p.TLSMode, hostname)
}

// defaultExternalURL derives the URL GitHub and browsers will use from the
// bind address and this host's name. It is only a default: an operator with a
// proxy or a DNS name will overwrite it, which is exactly the prompt that
// follows.
func defaultExternalURL(c ListenChoice, bind string, mode config.TLSMode, hostname string) string {
	host, port, err := splitBind(bind)
	if err != nil {
		return ""
	}
	if c == ListenProxy || c == ListenCloudflare {
		// The proxy terminates TLS on 443, so the URL is the host's name and
		// nothing else.
		return "https://" + hostname
	}
	scheme := "http"
	if mode != config.TLSOff {
		scheme = "https"
	}
	name := hostname
	if isLoopbackHost(host) {
		name = "localhost"
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + name
	}
	return fmt.Sprintf("%s://%s:%d", scheme, name, port)
}

func splitBind(bind string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(bind))
	if err != nil {
		return "", 0, fmt.Errorf("%q is not a host:port address: %w", bind, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("%q does not end in a port number: %w", bind, err)
	}
	return host, port, nil
}

func isLoopbackHost(host string) bool {
	switch host {
	case "", "0.0.0.0", "::", "[::]", "*":
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// BackendChoice is one option the backend question offers, in the order the
// question offers them: rootless first, and every choice that gives something
// up says so.
type BackendChoice struct {
	Kind   store.BackendKind
	Label  string
	Socket string
	// Rootless is the property the ordering exists to prefer.
	Rootless bool
	// Warning is the consequence of choosing this one, empty for the safe
	// choices.
	Warning string
	// Available is false for a runtime that is installed but not reachable;
	// such a choice is still shown, with what to start.
	Available bool
	// Fix is what to run to make an unavailable runtime available.
	Fix string
}

// backendChoices returns the backends this host can offer, best first.
//
// The order is the policy: rootless Docker, rootless Podman, root Docker, root
// Podman, and the process backend last. The process backend is always offered
// because a host with no container runtime must still be able to finish setup,
// but it is offered with what it costs written next to it.
func backendChoices(d Detection) []BackendChoice {
	var out []BackendChoice
	add := func(r RuntimeInfo, rootless bool) {
		if !r.Available || r.Rootless != rootless {
			return
		}
		c := BackendChoice{
			Kind: r.Kind, Socket: r.Endpoint, Rootless: rootless, Available: true,
		}
		name := strings.ToUpper(string(r.Kind)[:1]) + string(r.Kind)[1:]
		if rootless {
			c.Label = fmt.Sprintf("Rootless %s -- %s", name, r.Endpoint)
		} else {
			c.Label = fmt.Sprintf("%s (root daemon) -- %s", name, r.Endpoint)
			c.Warning = "This daemon runs as root, so a container escape from a job lands on a root-owned process. A rootless daemon is worth the twenty minutes it takes to set up."
		}
		out = append(out, c)
	}
	add(d.Docker, true)
	add(d.Podman, true)
	add(d.Docker, false)
	add(d.Podman, false)

	out = append(out, BackendChoice{
		Kind:      store.BackendProcess,
		Label:     "Process -- run jobs directly on this host",
		Available: true,
		Warning: "There is no container isolation: workflow steps run as the agent's user, " +
			"sharing this host's filesystem, package manager and network. Nothing is cleaned up between jobs beyond the work directory.",
	})

	// Installed-but-dead runtimes come last, after the process backend, so
	// that the first entry -- which is what the prompt and defaultPlan both
	// pre-select -- is always something that can actually run a job. Offering
	// them at all is right: a stopped daemon is a fixable problem and naming
	// it is more use than hiding it.
	for _, r := range []RuntimeInfo{d.Docker, d.Podman} {
		if r.Available || !r.Installed {
			continue
		}
		out = append(out, BackendChoice{
			Kind:    r.Kind,
			Label:   fmt.Sprintf("%s -- installed, but its socket is not reachable", r.Kind),
			Warning: firstLine(r.Detail),
			Fix:     startCommand(r.Kind),
		})
	}
	return out
}

// startCommand is what to run to bring a runtime's socket up, which is the
// difference between a dead end and a fixable problem.
func startCommand(kind store.BackendKind) string {
	if kind == store.BackendPodman {
		return "systemctl --user enable --now podman.socket   (or, for the system socket: sudo systemctl enable --now podman.socket)"
	}
	return "systemctl --user start docker   (rootless), or: sudo systemctl start docker"
}

// PoolSuggestion is the first pool the summary proposes. Setting up a fleet
// and then staring at an empty Pools page is the one part of this that has no
// obvious next step, so the installer writes the command out.
type PoolSuggestion struct {
	Name       string
	Labels     []string
	Backend    store.BackendKind
	MaxRunners int
}

// SuggestPool builds the suggestion from this host's architecture and chosen
// backend, so that the label in the suggestion is the one a workflow's
// runs-on can actually use.
//
// The name is branded -- "zoomies-linux-x64", not "linux-x64" -- because it is
// the word a workflow in somebody else's repository has to write. An unbranded
// label reads like one of GitHub's own and gives a reviewer of that pull
// request no way to tell where the job is about to run.
func SuggestPool(osName, arch string, kind store.BackendKind, capacity int) PoolSuggestion {
	name := store.RunnerNamePrefix + archLabel(osName, arch)
	if kind == store.BackendProcess {
		// A process-backend pool answers to a different label on purpose:
		// jobs that land on it get the host, not a container.
		name += "-host"
	}
	maxRunners := capacity
	if maxRunners < 1 {
		maxRunners = 1
	}
	return PoolSuggestion{
		Name: name,
		// Both labels, always: the specific one for a workflow that means this
		// pool, and the brand for one that means "anywhere in this fleet".
		Labels:     store.BrandLabels([]string{name}),
		Backend:    kind,
		MaxRunners: maxRunners,
	}
}

func archLabel(osName, arch string) string {
	o := osName
	switch osName {
	case "darwin":
		o = "macos"
	case "":
		o = "linux"
	}
	a := arch
	switch arch {
	case "amd64", "x86_64":
		a = "x64"
	case "aarch64":
		a = "arm64"
	case "":
		a = "x64"
	}
	return o + "-" + a
}

// applyAnswers overlays what an answer file said, leaving the derived defaults
// wherever it said nothing.
func (p *PoolSuggestion) applyAnswers(a AnswersPool) {
	if name := store.BrandedName(a.Name); name != "" {
		p.Name = name
		// The labels default to the name, so a renamed pool that was not given
		// labels of its own follows the new name rather than answering to the
		// old one.
		if len(a.Labels) == 0 {
			p.Labels = store.BrandLabels([]string{store.BrandedLabel(name)})
		}
	}
	if len(a.Labels) > 0 {
		p.Labels = store.BrandLabels(a.Labels)
	}
	if a.MaxRunners > 0 {
		p.MaxRunners = a.MaxRunners
	}
}

// RunsOn renders the runs-on value a workflow writes to reach this pool.
func (p PoolSuggestion) RunsOn() string { return store.RunsOn(p.Labels) }

// Command renders the suggestion as a line the operator can paste.
//
// --installation is not optional to `pools create`, so a command printed
// without it cannot run -- which is what the installer's closing advice used to
// be. When the installation is not known here, a placeholder goes in with the
// command that finds it, rather than a line that fails on paste.
func (p PoolSuggestion) Command(installationID string) string {
	if installationID == "" {
		installationID = "<id from `zoomies installations list`>"
	}
	return fmt.Sprintf("zoomies pools create --name %s --labels %s --backend %s --max %d --installation %s",
		p.Name, strings.Join(p.Labels, ","), p.Backend, p.MaxRunners, installationID)
}

// ReviewLine is one row of the plan an operator is shown before anything is
// written. Key is the twelve-column label; Value is what will happen.
type ReviewLine struct {
	Key   string
	Value string
}

// Review renders the whole plan as the operator will read it.
//
// It is a method on Plan, and a pure one, for the same reason Config is: the
// screen an operator approves and the deployment they get are then the same
// object, and a test can assert that every setting which changes the host
// appears on it. An installer that writes to /etc and /var/lib without ever
// saying what it is about to do is the reason people ctrl-c at the last
// question.
func (p Plan) Review() []ReviewLine {
	kind := "a controller with an embedded agent"
	switch p.Mode {
	case ModeController:
		kind = "a controller; runner hosts join it separately"
	case ModeAgent:
		kind = "a runner host"
	}
	how := "the binary under " + string(p.Service)
	switch p.Deployment {
	case DeploymentCompose:
		how = "a compose project in " + p.DeployDir
	case DeploymentDocker:
		how = "one container from " + p.Image
	case DeploymentNative:
		if p.Service == ServiceNone {
			how = "the binary, with no service manager to restart it"
		}
	}

	out := []ReviewLine{
		{"this host", kind},
		{"run as", how},
	}
	if !p.Deployment.Containerised() {
		out = append(out, ReviewLine{"account", p.ServiceUser + ":" + p.ServiceGroup})
	}
	if p.Embedded {
		backend := string(p.Backend)
		if p.DockerHost != "" {
			backend += " -- " + p.DockerHost
		}
		if p.Rootless {
			backend += " (rootless)"
		}
		out = append(out,
			ReviewLine{"backend", backend},
			ReviewLine{"capacity", fmt.Sprintf("%d %s at once", p.Capacity, pluralise(p.Capacity, "runner"))},
		)
	}
	listener := p.Bind + ", TLS " + string(p.TLSMode)
	if p.PublishedPort > 0 {
		listener = fmt.Sprintf("%s:%d on this host -> %s in the container, TLS %s",
			p.PublishAddr, p.PublishedPort, p.Bind, p.TLSMode)
	}
	out = append(out, ReviewLine{"listener", listener})
	if len(p.TrustedProxies) > 0 {
		out = append(out, ReviewLine{"proxies", "trusting " + strings.Join(p.TrustedProxies, ", ")})
	}
	out = append(out, ReviewLine{"reached at", p.ExternalURL})

	switch {
	case p.GitHub.Skip:
		out = append(out, ReviewLine{"github", "not now -- connect it later in the browser"})
	case p.GitHub.Target != "":
		out = append(out, ReviewLine{"github", p.GitHub.Target + " (" + string(p.GitHub.TargetType) + ")"})
	default:
		out = append(out, ReviewLine{"github", "a new App, created in your browser"})
	}
	if !p.Deployment.Containerised() {
		out = append(out, ReviewLine{"admin", p.AdminUser})
	} else {
		out = append(out, ReviewLine{"admin", "created in the browser once the container is up"})
	}
	return out
}

// Writes lists the paths this plan will create or overwrite. Naming them is
// most of what makes the review screen worth reading: an operator who can see
// exactly which four files are involved can decide in a second.
func (p Plan) Writes() []string {
	var out []string
	if p.Deployment.Containerised() {
		return append(out,
			filepath.Join(p.DeployDir, "docker-compose.yml"),
			filepath.Join(p.DeployDir, ".env"),
			p.StateDir+" (the database, in a volume)",
		)
	}
	out = append(out, p.ConfigFile, p.KeyFile+" (mode 0600 -- the encryption key)", p.DBPath)
	if p.Service == ServiceSystemd {
		out = append(out, SystemdUnitPath(UnitController))
	}
	return out
}

// Config renders the zoomies.yaml this plan describes.
//
// It is a pure function of the plan so that a test can assert the installer
// produces a configuration that config.Validate accepts, and that each
// listener choice produces exactly the warnings it should.
func (p Plan) Config() *config.Config {
	cfg := config.Default()
	cfg.Server.Bind = p.Bind
	cfg.Server.ExternalURL = strings.TrimRight(p.ExternalURL, "/")
	cfg.Server.TLS.Mode = p.TLSMode
	cfg.Server.TLS.CertFile = p.TLSCertFile
	cfg.Server.TLS.KeyFile = p.TLSKeyFile
	cfg.Server.TLS.Hosts = p.TLSHosts
	cfg.Server.TrustedProxies = p.TrustedProxies

	cfg.Database.Path = p.DBPath
	cfg.Security.EncryptionKeyFile = p.KeyFile
	// The key itself never goes into the config file: anything that can read
	// zoomies.yaml -- a backup, configuration management, a support bundle --
	// would then be able to decrypt every stored secret.
	cfg.Security.EncryptionKey = ""

	if p.GitHub.APIBaseURL != "" {
		cfg.GitHub.APIBaseURL = p.GitHub.APIBaseURL
	}

	cfg.Agent.Embedded = p.Embedded
	cfg.Agent.Capacity = p.Capacity
	cfg.Agent.WorkDir = p.WorkDir
	if p.Backend != "" {
		cfg.Agent.Backend = string(p.Backend)
	}
	cfg.Agent.DockerHost = p.DockerHost
	if cfg.Agent.Name == "" {
		cfg.Agent.Name = hostname()
	}

	secure := p.TLSMode != config.TLSOff || strings.HasPrefix(cfg.Server.ExternalURL, "https://")
	cfg.Security.CookieSecure = &secure
	return cfg
}

// Missing lists the answers a non-interactive run still needs, phrased with
// the answer-file key so the operator can go straight to the line to edit.
func (p Plan) Missing() []MissingAnswer {
	var out []MissingAnswer
	if p.ExternalURL == "" {
		out = append(out, MissingAnswer{"external_url", "the URL GitHub and your browser reach this controller on"})
	}
	// An existing database already holds the accounts, so a re-run does not
	// need to be told about an administrator it is not going to create -- and
	// a containerised deployment creates its first one in the browser, in a
	// database this process never opens.
	if !p.Deployment.Containerised() && !exists(p.DBPath) {
		if p.AdminUser == "" {
			out = append(out, MissingAnswer{"admin.username", "the first administrator's login name"})
		}
		if p.adminPassword == "" {
			out = append(out, MissingAnswer{"admin.password", "the first administrator's password (at least " + strconv.Itoa(auth.MinPasswordLength) + " characters), or admin.password_file"})
		}
	}
	if p.TLSMode == config.TLSFiles && (p.TLSCertFile == "" || p.TLSKeyFile == "") {
		out = append(out, MissingAnswer{"tls.cert_file", "the certificate and key to serve"})
	}
	if p.Backend == "" {
		out = append(out, MissingAnswer{"backend", "docker, podman or process"})
	}
	return out
}

// applyAnswers overlays an answer file onto a plan. Anything the file does not
// mention keeps the detected default, which is what makes a three-line answer
// file useful.
func applyAnswers(p Plan, a *Answers) (Plan, error) {
	if a == nil {
		return p, nil
	}
	if a.Deployment != "" {
		d, err := ParseDeployment(a.Deployment)
		if err != nil {
			return p, fmt.Errorf("installer: deployment in the answer file: %w", err)
		}
		p.Deployment = d
	}
	if a.Image != "" {
		p.Image = a.Image
	}
	if a.ServiceUser != "" {
		p.ServiceUser, p.ServiceGroup = a.ServiceUser, a.ServiceUser
	}
	if a.ConfigDir != "" {
		p.ConfigDir = a.ConfigDir
		p.ConfigFile = filepath.Join(a.ConfigDir, "zoomies.yaml")
		p.KeyFile = filepath.Join(a.ConfigDir, "encryption.key")
		p.DeployDir = a.ConfigDir
	}
	if a.StateDir != "" {
		p.StateDir = a.StateDir
		p.DBPath = filepath.Join(a.StateDir, "zoomies.db")
		p.WorkDir = filepath.Join(a.StateDir, "work")
	}
	if a.Backend != "" {
		kind := store.BackendKind(a.Backend)
		if !kind.Valid() {
			return p, fmt.Errorf("installer: backend %q in the answer file is not one of docker, podman or process", a.Backend)
		}
		p.Backend = kind
	}
	if a.DockerHost != "" {
		p.DockerHost = a.DockerHost
	}
	if a.Capacity > 0 {
		p.Capacity = a.Capacity
	}
	if a.Bind != "" {
		if _, _, err := splitBind(a.Bind); err != nil {
			return p, fmt.Errorf("installer: bind in the answer file: %w", err)
		}
		p.Bind = a.Bind
	}
	switch a.TLS.Mode {
	case "":
	case "off":
		p.TLSMode = config.TLSOff
	case "files":
		p.TLSMode = config.TLSFiles
	case "self-signed", "selfsigned":
		p.TLSMode = config.TLSSelfSigned
	default:
		return p, fmt.Errorf("installer: tls.mode %q in the answer file is not off, files or self-signed", a.TLS.Mode)
	}
	if a.TLS.CertFile != "" {
		p.TLSCertFile = a.TLS.CertFile
	}
	if a.TLS.KeyFile != "" {
		p.TLSKeyFile = a.TLS.KeyFile
	}
	if len(a.TLS.Hosts) > 0 {
		p.TLSHosts = a.TLS.Hosts
	}
	if len(a.TrustedProxies) > 0 {
		p.TrustedProxies = a.TrustedProxies
	}
	if a.ExternalURL != "" {
		p.ExternalURL = a.ExternalURL
	}
	// The listener choice is derived rather than asked for, so that an answer
	// file that sets only bind and tls still produces the right warnings.
	p.Listen = listenChoiceFor(p.Bind, p.TLSMode, p.TrustedProxies)

	if a.GitHub.APIBaseURL != "" {
		p.GitHub.APIBaseURL = a.GitHub.APIBaseURL
	}
	p.GitHub.Skip = a.GitHub.Skip
	if a.GitHub.Target != "" {
		p.GitHub.Target = a.GitHub.Target
		p.GitHub.TargetType = targetTypeFor(a.GitHub.Target, a.GitHub.TargetType)
	}
	p.GitHub.AppID = a.GitHub.AppID
	p.GitHub.InstallationID = a.GitHub.InstallationID
	p.GitHub.PrivateKeyFile = a.GitHub.PrivateKeyFile

	if a.Admin.Username != "" {
		p.AdminUser = a.Admin.Username
	}
	pw, err := a.AdminPassword()
	if err != nil {
		return p, err
	}
	if pw != "" {
		p.adminPassword = pw
	}

	if a.Service.Manager != "" {
		p.Service = ServiceKind(a.Service.Manager)
	}
	if a.Service.Enable != nil {
		p.EnableService = *a.Service.Enable
	}
	if a.Service.Start != nil {
		p.StartService = *a.Service.Start
	}
	// Applied last so that it wins over config_dir, which is only the default
	// place for these two files.
	if a.DeploymentDir != "" {
		p.DeployDir = a.DeploymentDir
	}
	return p, nil
}

// listenChoiceFor recovers the listener choice from a bind address and TLS
// mode, which is what an answer file gives us instead of the choice itself.
// Trusting the cloudflare token is what tells a plain-HTTP public bind apart
// from Cloudflare specifically.
func listenChoiceFor(bind string, mode config.TLSMode, trusted []string) ListenChoice {
	host, _, err := splitBind(bind)
	if err == nil && isLoopbackHost(host) {
		return ListenLoopback
	}
	switch mode {
	case config.TLSFiles:
		return ListenTLSFiles
	case config.TLSSelfSigned:
		return ListenSelfSigned
	default:
		if slices.Contains(trusted, config.TrustedProxyCloudflare) {
			return ListenCloudflare
		}
		return ListenProxy
	}
}

func targetTypeFor(target, declared string) store.TargetType {
	switch store.TargetType(declared) {
	case store.TargetOrg:
		return store.TargetOrg
	case store.TargetRepo:
		return store.TargetRepo
	}
	if strings.Contains(target, "/") {
		return store.TargetRepo
	}
	return store.TargetOrg
}

// resolvePlan produces the plan: defaults, then the answer file, then the
// operator, then a check that nothing required is still missing.
func (i *Installer) resolvePlan(ctx context.Context, mode Mode) (Plan, error) {
	p := defaultPlan(i.det, mode)
	// "Installed" has to mean "finished", not "started". zoomies.yaml is
	// written at step five of ten -- before the GitHub App, the administrator
	// and the first pool -- so a run that died at the App step left behind
	// exactly the file this used to read as proof of a working install. The
	// next run then "upgraded" it, skipped the three remaining steps and told
	// the operator to go and log in to a controller with no account on it.
	p.Upgrade = i.det.Existing.ConfigFile != "" && finished(ctx, i.det.Existing.Database)
	if i.det.Existing.ConfigFile != "" && !p.Upgrade {
		i.ui.step("A previous run stopped part-way")
		i.ui.note("there is a configuration here but no administrator account, so setup did not finish.")
		i.ui.note("this run carries on from where it stopped; your encryption key and database are kept.")
		i.ui.blank()
	}

	var err error
	if p, err = applyAnswers(p, i.answers); err != nil {
		return p, err
	}
	if p.Upgrade {
		if p, err = i.askExisting(ctx, p); err != nil {
			return p, err
		}
		if p.Upgrade {
			return p, nil
		}
	}

	if p, err = i.resolveDeployment(ctx, p); err != nil {
		return p, err
	}
	if i.interactive {
		if p, err = i.ask(ctx, p); err != nil {
			return p, err
		}
	}
	// The operator answered questions about this host; a containerised
	// deployment needs the same answers translated to the inside of a
	// container. Doing it once, here, is what keeps the compose file, the
	// environment file and the health check describing one deployment.
	p = containerise(p)
	if missing := p.Missing(); len(missing) > 0 {
		var b strings.Builder
		b.WriteString("installer: this run cannot continue without:\n")
		for _, m := range missing {
			fmt.Fprintf(&b, "  - %s: %s\n", m.Key, m.Why)
		}
		b.WriteString("supply them in an answer file (`zoomies init --print-answers > answers.yaml`) and pass --answers answers.yaml")
		return p, errors.New(strings.TrimRight(b.String(), "\n"))
	}
	return p, nil
}

// finished reports whether a previous run got as far as creating an
// administrator. It is the one question whose answer distinguishes a finished
// install from an abandoned one, and it is asked read-only: a database that
// cannot be opened is not evidence of anything, so it answers false and the
// remaining steps run again -- all of which are safe to repeat.
func finished(ctx context.Context, dbPath string) bool {
	if dbPath == "" || !exists(dbPath) {
		return false
	}
	st, err := store.Open(ctx, store.Options{Path: dbPath})
	if err != nil {
		return false
	}
	defer func() { _ = st.Close() }()
	n, err := st.CountUsers(ctx)
	return err == nil && n > 0
}

// resolveDeployment settles how this host will run Zoomies.
//
// The flag wins, then the answer file, then the operator. It is asked before
// anything else because it decides which of the later questions exist at all:
// a container has no service unit to install and creates its first
// administrator in the browser rather than here.
func (i *Installer) resolveDeployment(ctx context.Context, p Plan) (Plan, error) {
	named := i.opts.Deployment != "" || (i.answers != nil && i.answers.Deployment != "")
	if i.opts.Deployment != "" {
		p.Deployment = i.opts.Deployment
	}

	if named {
		if why := UnavailableDeployment(i.det, p.Deployment); why != "" {
			return p, errors.New(why)
		}
		i.ui.step("Deployment: " + string(p.Deployment))
		i.ui.note(deploymentConsequence(i.det, p.Deployment))
		return p, nil
	}

	if !i.interactive {
		// A missing answer takes the default rather than failing: there is a
		// right answer for this host, and refusing to install over a question
		// nobody was there to answer helps nobody. It is said out loud,
		// though, because the operator did not choose it.
		p.Deployment = DefaultDeployment(i.det)
		i.ui.step("Deployment: " + string(p.Deployment))
		i.ui.note(defaultDeploymentReason(i.det))
		i.ui.note("set `deployment:` in the answer file, or pass --deployment, to choose another.")
		return p, nil
	}

	opts := DeploymentOptions(i.det)
	huhOpts := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		huhOpts = append(huhOpts, huh.NewOption(o.Label+" -- "+o.Description, string(o.Deployment)))
	}
	choice := string(DefaultDeployment(i.det))
	if err := i.selectOne(ctx, "How should Zoomies run on this host?",
		"Only what this host can actually do is listed. "+defaultDeploymentReason(i.det),
		huhOpts, &choice); err != nil {
		return p, err
	}
	chosen, err := ParseDeployment(choice)
	if err != nil {
		return p, err
	}
	p.Deployment = chosen
	i.ui.ok("deployment: " + string(p.Deployment))
	return p, nil
}

// defaultDeploymentReason says why the default is the default, because a
// default an operator does not understand is one they cannot safely accept.
func defaultDeploymentReason(d Detection) string {
	if d.Compose.Available {
		return "Compose is the default because `" + d.Compose.String() + "` answered here, and a compose deployment is the easiest to upgrade and to move."
	}
	return "Native is the default because this host has no compose command; the binary under a supervisor needs nothing else installed."
}

// deploymentConsequence is the one line a non-interactive run gets instead of
// the prompt's description.
func deploymentConsequence(d Detection, want Deployment) string {
	for _, o := range DeploymentOptions(d) {
		if o.Deployment == want {
			return o.Description
		}
	}
	return "Runs the binary directly, supervised by whatever this host has."
}

// askExisting decides what to do about a previous installation. Nothing here
// removes anything: the destructive path moves the old key and database aside
// and says where they went, because an encryption key that is gone is a
// GitHub App that has to be recreated.
func (i *Installer) askExisting(ctx context.Context, p Plan) (Plan, error) {
	i.ui.step("Zoomies is already installed here")
	for _, line := range i.det.Existing.Items() {
		i.ui.note(line)
	}
	if !i.interactive || i.opts.AssumeYes {
		i.ui.note("continuing as an upgrade: the binary and the service unit are rewritten, configuration and data are left alone.")
		return p, nil
	}

	const (
		optUpgrade   = "upgrade"
		optReconfig  = "reconfigure"
		optStartOver = "start-over"
	)
	choice := optUpgrade
	err := i.selectOne(ctx, "What should this run do?",
		"Upgrading keeps your configuration, encryption key, database and runners exactly as they are.",
		[]huh.Option[string]{
			huh.NewOption("Upgrade in place -- rewrite the service unit, keep everything else (default)", optUpgrade),
			huh.NewOption("Reconfigure -- ask the questions again, keep the encryption key and database", optReconfig),
			huh.NewOption("Start again -- move the existing key and database aside", optStartOver),
		}, &choice)
	if err != nil {
		return p, err
	}

	switch choice {
	case optUpgrade:
		return p, nil
	case optReconfig:
		p.Upgrade = false
		return p, nil
	default:
		p.Upgrade = false
		typed := ""
		err := i.input(ctx, "Type ERASE to confirm",
			"The encryption key and the database will be renamed with a timestamp, not deleted. Without the key, "+
				"the stored GitHub App private key and webhook secrets cannot be decrypted and must be entered again.",
			"", &typed, func(s string) error {
				if strings.TrimSpace(s) != "ERASE" {
					return errors.New("type ERASE in capitals to confirm, or press ctrl-c to stop")
				}
				return nil
			})
		if err != nil {
			return p, err
		}
		if err := i.archiveExisting(p); err != nil {
			return p, err
		}
		return p, nil
	}
}

// archiveExisting renames the key and database out of the way. Renaming rather
// than deleting is deliberate: the operator who typed ERASE at 2am can still
// get their fleet back.
func (i *Installer) archiveExisting(p Plan) error {
	stamp := time.Now().UTC().Format("20060102-150405")
	for _, path := range []string{p.KeyFile, p.DBPath, p.ConfigFile} {
		if path == "" || !exists(path) {
			continue
		}
		moved := path + "." + stamp + ".bak"
		if err := os.Rename(path, moved); err != nil {
			return fmt.Errorf("installer: moving %s aside: %w", path, err)
		}
		i.ui.ok("moved " + path + " to " + moved)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Prompts
// ---------------------------------------------------------------------------

// ask fills in everything the answer file did not. Each question shows its
// default, why it matters and what the non-default choice costs; that is the
// difference between an installer and an interrogation.
func (i *Installer) ask(ctx context.Context, p Plan) (Plan, error) {
	var err error
	if p.Embedded {
		if p, err = i.askBackend(ctx, p); err != nil {
			return p, err
		}
	} else {
		i.ui.step("Backend: none on this host")
		i.ui.note("this is a controller only; runners come from hosts that join it with `zoomies agent join`.")
	}
	if p, err = i.askListener(ctx, p); err != nil {
		return p, err
	}
	if p, err = i.askExternalURL(ctx, p); err != nil {
		return p, err
	}
	if p.Deployment.Containerised() {
		// A container keeps its database in a volume this process cannot
		// reach, so the first administrator is created in the browser on first
		// start, and the deployment is its own supervisor.
		i.ui.note("the first administrator is created in the browser once the container is up.")
		return p, nil
	}
	if p, err = i.askAdmin(ctx, p); err != nil {
		return p, err
	}
	return i.askService(ctx, p)
}

func (i *Installer) askBackend(ctx context.Context, p Plan) (Plan, error) {
	choices := backendChoices(i.det)
	opts := make([]huh.Option[string], 0, len(choices))
	byKey := map[string]BackendChoice{}
	for n, c := range choices {
		key := strconv.Itoa(n)
		byKey[key] = c
		label := c.Label
		if !c.Available {
			label += "  (not usable yet)"
		} else if c.Warning != "" {
			// Every other select in this installer carries its consequence in
			// the label. This one printed the warning only after the choice
			// was made, which is the one place the installer's own promise --
			// that each question says what the non-default choice costs --
			// was not kept.
			label += " -- " + firstSentence(c.Warning)
		}
		opts = append(opts, huh.NewOption(label, key))
	}

	desc := "Rootless is preferred: a container escape from a job then lands on an unprivileged account rather than on root."
	if !i.det.Docker.Available && !i.det.Podman.Available {
		desc = "No container runtime answered here. " + startCommand(store.BackendDocker) +
			" -- or continue with the process backend, which runs jobs directly on this host."
	}

	selected := "0"
	if err := i.selectOne(ctx, "Which backend should run your jobs?", desc, opts, &selected); err != nil {
		return p, err
	}
	c := byKey[selected]

	if !c.Available {
		// The operator picked an installed-but-dead runtime, which is a fixable
		// problem rather than a choice: say what to start and let them decide
		// between stopping now and the process backend.
		i.ui.warn(c.Label)
		i.ui.note(c.Warning)
		i.ui.note("start it with: " + c.Fix)
		useProcess := false
		if err := i.confirm(ctx, "Continue with the process backend instead?",
			"Answering no stops setup now so you can start the daemon and run `zoomies init` again. "+
				"The process backend runs jobs directly on this host with no container isolation.",
			&useProcess); err != nil {
			return p, err
		}
		if !useProcess {
			return p, fmt.Errorf("installer: stopped so that %s can be started first; run `zoomies init` again afterwards", c.Kind)
		}
		// Take the real process choice, so its warning is printed below rather
		// than lost by substituting a bare kind here.
		for _, alt := range choices {
			if alt.Kind == store.BackendProcess {
				c = alt
			}
		}
	}

	p.Backend = c.Kind
	p.DockerHost = c.Socket
	p.Rootless = c.Rootless
	if c.Warning != "" {
		i.ui.warn(c.Warning)
	}
	i.ui.ok(fmt.Sprintf("backend: %s%s", c.Kind, socketSuffix(c.Socket)))

	capacity := strconv.Itoa(p.Capacity)
	if err := i.input(ctx, "How many runners may this host hold at once?",
		fmt.Sprintf("This host has %d CPUs. One runner per two cores leaves the machine room to breathe; a pool's max_runners can be lower, never higher than this.", runtime.NumCPU()),
		capacity, &capacity, func(s string) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < 1 {
				return errors.New("enter a whole number of at least 1")
			}
			return nil
		}); err != nil {
		return p, err
	}
	p.Capacity, _ = strconv.Atoi(strings.TrimSpace(capacity))
	return p, nil
}

// firstSentence trims a multi-sentence consequence down to something that fits
// on one line of a select.
func firstSentence(s string) string {
	if n := strings.Index(s, ". "); n > 0 {
		return s[:n+1]
	}
	return s
}

func socketSuffix(socket string) string {
	if socket == "" {
		return ""
	}
	return " at " + socket
}

func (i *Installer) askListener(ctx context.Context, p Plan) (Plan, error) {
	opts := listenOptions()
	huhOpts := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		huhOpts = append(huhOpts, huh.NewOption(o.Label+" -- "+o.Description, string(o.Choice)))
	}
	choice := string(p.Listen)
	if err := i.selectOne(ctx, "How should the controller be reached?",
		"Loopback is the default because it is the only one that is safe before you have a certificate.",
		huhOpts, &choice); err != nil {
		return p, err
	}

	_, port, err := splitBind(p.Bind)
	if err != nil {
		port = 8080
	}
	portStr := strconv.Itoa(port)
	if err := i.input(ctx, "Which port?", "8080 is the default. Ports below 1024 need a capability the unit will grant explicitly.",
		portStr, &portStr, func(s string) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < 1 || n > 65535 {
				return errors.New("enter a port between 1 and 65535")
			}
			return nil
		}); err != nil {
		return p, err
	}
	port, _ = strconv.Atoi(strings.TrimSpace(portStr))

	// A port that is already taken is worth catching now: the alternative is a
	// service that installs cleanly and then fails to start.
	host := "127.0.0.1"
	if ListenChoice(choice) != ListenLoopback {
		host = ""
	}
	if !PortFree(host, port) {
		i.ui.warn(fmt.Sprintf("port %d is already in use on this host.", port))
		if next, ok := NextFreePort(host, port+1, 20); ok {
			use := true
			if err := i.confirm(ctx, fmt.Sprintf("Use port %d instead?", next),
				"Something else is already listening on the port you chose, so the service would fail to start.", &use); err != nil {
				return p, err
			}
			if use {
				port = next
			}
		}
	}

	ListenChoice(choice).apply(&p, port, i.det.Hostname)

	switch p.Listen {
	case ListenTLSFiles:
		if err := i.input(ctx, "Certificate file", "The full chain, PEM encoded. GitHub must be able to verify it, or it will not deliver webhooks.",
			p.TLSCertFile, &p.TLSCertFile, fileMustExist); err != nil {
			return p, err
		}
		if err := i.input(ctx, "Private key file", "PEM encoded, readable by the service user and nobody else.",
			p.TLSKeyFile, &p.TLSKeyFile, fileMustExist); err != nil {
			return p, err
		}
	case ListenSelfSigned:
		i.ui.warn("GitHub will not deliver webhooks to a self-signed certificate.")
		i.ui.note("scaling will fall back to the queued-job poller, which reacts in tens of seconds rather than instantly.")
	case ListenProxy, ListenCloudflare:
		proxies := strings.Join(p.TrustedProxies, ",")
		if err := i.input(ctx, "Which proxies may set X-Forwarded-For?",
			"Comma-separated CIDRs, e.g. 10.0.0.0/8; the word cloudflare stands for Cloudflare's published ranges. "+
				"Leave empty to take client addresses from the socket: safe, but every "+
				"audit entry and every login rate limit will then record your proxy's address instead of the real client's.",
			proxies, &proxies, validateCIDRList); err != nil {
			return p, err
		}
		p.TrustedProxies = splitList(proxies)
	}
	i.ui.ok(fmt.Sprintf("listener: %s, TLS %s", p.Bind, p.TLSMode))
	return p, nil
}

func (i *Installer) askExternalURL(ctx context.Context, p Plan) (Plan, error) {
	value := p.ExternalURL
	if err := i.input(ctx, "What URL will GitHub and your browser use?",
		"Webhook deliveries go to this URL plus /webhooks/github. Get it wrong and the fleet still works, but only through the "+
			"fallback poller, which reacts in tens of seconds rather than instantly.",
		value, &value, validateAbsoluteURL); err != nil {
		return p, err
	}
	p.ExternalURL = strings.TrimRight(strings.TrimSpace(value), "/")
	i.ui.ok("external URL: " + p.ExternalURL)
	return p, nil
}

func (i *Installer) askAdmin(ctx context.Context, p Plan) (Plan, error) {
	if p.AdminUser != "" && p.adminPassword != "" {
		return p, nil
	}
	if exists(p.DBPath) {
		// The accounts in an existing database are left alone, so there is
		// nothing to ask about here.
		return p, nil
	}
	return i.askAdminCredentials(ctx, p)
}

// askAdminCredentials asks for the first administrator. It is separate from
// askAdmin because stepAdmin calls it too: a database that turns out to have
// no accounts still needs one, even if the plan thought otherwise.
func (i *Installer) askAdminCredentials(ctx context.Context, p Plan) (Plan, error) {
	i.ui.step("Your administrator account")
	name := p.AdminUser
	if err := i.input(ctx, "Username", "This is the account you will log in with; it can create pools, tokens and other users.",
		name, &name, func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("a username is required")
			}
			return nil
		}); err != nil {
		return p, err
	}
	p.AdminUser = strings.TrimSpace(name)

	var pw, again string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Password").
			Description(fmt.Sprintf("At least %d characters. It is stored as an argon2id hash, never in the clear.", auth.MinPasswordLength)).
			EchoMode(huh.EchoModePassword).Value(&pw).
			Validate(func(s string) error { return auth.CheckPassword(s) }),
		huh.NewInput().Title("Password again").
			EchoMode(huh.EchoModePassword).Value(&again).
			Validate(func(s string) error {
				if s != pw {
					return errors.New("the two passwords do not match")
				}
				return nil
			}),
	))
	if err := i.runForm(ctx, form); err != nil {
		return p, err
	}
	p.adminPassword = pw
	return p, nil
}

func (i *Installer) askService(ctx context.Context, p Plan) (Plan, error) {
	opts := []huh.Option[string]{}
	switch {
	case i.det.HasSystemd:
		opts = append(opts, huh.NewOption("systemd unit (default) -- starts at boot, restarts on failure", string(ServiceSystemd)))
	case i.det.HasLaunchd:
		opts = append(opts, huh.NewOption("launchd job (default) -- starts at login, restarts on failure", string(ServiceLaunchd)))
	}
	opts = append(opts,
		huh.NewOption("Nothing -- I will run `zoomies controller` myself", string(ServiceNone)),
	)
	choice := string(p.Service)
	// A default this list does not offer -- ServiceCompose, on a host with no
	// supervisor -- would leave the prompt showing a value nobody can pick.
	if !containsOption(opts, choice) {
		choice = opts[0].Value
	}
	if err := i.selectOne(ctx, "How should Zoomies be kept running?",
		"Without a service, the controller stops when this terminal does and does not come back after a reboot.",
		opts, &choice); err != nil {
		return p, err
	}
	p.Service = ServiceKind(choice)
	return p, nil
}

// containsOption reports whether a value is one of the choices offered.
func containsOption(opts []huh.Option[string], value string) bool {
	for _, o := range opts {
		if o.Value == value {
			return true
		}
	}
	return false
}

func fileMustExist(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("a path is required")
	}
	if _, err := os.Stat(s); err != nil {
		return fmt.Errorf("cannot read %s: %w", s, err)
	}
	return nil
}

func validateAbsoluteURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("a URL is required; GitHub needs somewhere to deliver webhooks")
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("include the scheme, e.g. https://zoomies.example.com")
	}
	return nil
}

func validateCIDRList(s string) error {
	for _, part := range splitList(s) {
		if part == config.TrustedProxyCloudflare {
			continue
		}
		if _, _, err := net.ParseCIDR(part); err != nil {
			if net.ParseIP(part) == nil {
				return fmt.Errorf("%q is not an IP address or CIDR, e.g. 10.0.0.0/8", part)
			}
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// runInstall performs the plan. Each step prints one line the operator can act
// on, and anything that weakens the default posture says so where it happens
// rather than only in the summary.
func (i *Installer) runInstall(ctx context.Context, p Plan) error {
	i.ui.total = nativeStepCount(p)
	if err := i.stepUserAndDirs(ctx, &p); err != nil {
		return err
	}
	key, freshKey, err := i.stepKey(p)
	if err != nil {
		return err
	}
	cfg, err := i.stepConfig(p)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, store.Options{Path: p.DBPath})
	if err != nil {
		return fmt.Errorf("installer: opening the database at %s: %w", p.DBPath, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = st.Close()
		}
	}()
	i.wrote("database ready at " + p.DBPath)
	i.chown(&p, p.DBPath)

	// The GitHub step can change the external URL: it refuses to create an App
	// whose webhook would point at loopback, and offers to take the real
	// address there and then. zoomies.yaml has already been written by this
	// point, so without this the operator ends up with an App pointing one way
	// and a controller configured the other -- session cookies, the webhook the
	// controller verifies against, and every link in the UI all built from an
	// address they were never shown.
	urlBefore := p.ExternalURL
	if err := i.stepGitHubApp(ctx, st, key, &p); err != nil {
		return err
	}
	if p.ExternalURL != urlBefore {
		if cfg, err = i.resaveConfig(p); err != nil {
			return err
		}
	}
	if err := i.stepAdmin(ctx, st, cfg, p); err != nil {
		return err
	}
	if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
		return err
	}

	// The service opens the same SQLite file, so the installer lets go of it
	// before anything is started.
	closed = true
	if err := st.Close(); err != nil {
		return fmt.Errorf("installer: closing the database: %w", err)
	}

	mgr, err := i.stepService(ctx, p)
	if err != nil {
		return err
	}
	if mgr != nil && p.StartService {
		i.stepHealth(ctx, p, mgr)
	}
	i.stepSummary(p, freshKey)
	return nil
}

// nativeStepCount is how many `->` headings a native install will print, so
// each one can say where it is in the sequence. An operator halfway through a
// five-minute browser handshake wants to know whether they are nearly done.
func nativeStepCount(p Plan) int {
	// Service user and directories, encryption key, configuration, GitHub App,
	// administrator, service, done.
	n := 7
	if p.Mode == ModeSingle {
		n++ // first pool
	}
	if p.StartService {
		n++ // health check
	}
	return n
}

// runUpgrade rewrites what a new binary needs -- the unit -- and leaves
// everything an operator would be upset to lose.
func (i *Installer) runUpgrade(ctx context.Context, p Plan) error {
	i.ui.step("Upgrading in place")
	i.ui.note("configuration, encryption key, database and runners are left alone.")

	cfg, err := config.Load(p.ConfigFile)
	if err != nil {
		return fmt.Errorf("installer: reading the existing configuration: %w", err)
	}
	p.Bind = cfg.Server.Bind
	p.ExternalURL = cfg.Server.ExternalURL
	p.TLSMode = cfg.Server.TLS.Mode
	p.Capacity = cfg.Agent.Capacity
	p.Backend = store.BackendKind(cfg.Agent.Backend)
	p.DockerHost = cfg.Agent.DockerHost
	p.Embedded = cfg.Agent.Embedded

	// The unit is about to be rewritten, so whatever account it already runs
	// as has to survive: moving the service onto a different user would leave
	// it unable to read the very files it has been writing.
	if user, group, groups := ReadUnitIdentity(SystemdUnitPath(UnitController)); user != "" {
		p.ServiceUser, p.ServiceGroup = user, group
		if group == "" {
			p.ServiceGroup = user
		}
		if len(groups) > 0 {
			p.DockerGroup = groups[0]
		}
	}

	mgr, err := i.stepService(ctx, p)
	if err != nil {
		return err
	}
	if mgr != nil {
		if err := mgr.Stop(ctx); err != nil {
			i.ui.note("could not stop the running service: " + err.Error())
		}
		if err := mgr.Start(ctx); err != nil {
			return fmt.Errorf("installer: starting %s: %w", p.Service, err)
		}
		i.stepHealth(ctx, p, mgr)
	}
	i.ui.blank()
	i.ui.step("Done")
	i.ui.note("upgraded in place; " + p.ConfigFile + " and " + p.DBPath + " were not touched.")
	if p.ExternalURL != "" {
		i.ui.note("open " + p.ExternalURL + " and check the UI's problems drawer.")
	}
	return nil
}

// stepUserAndDirs creates the service account and the two directories, with
// the permissions the security model assumes.
func (i *Installer) stepUserAndDirs(ctx context.Context, p *Plan) error {
	i.ui.step("Service user and directories")

	if i.det.Root && i.det.OS != "darwin" {
		created, err := ensureServiceUser(ctx, p.ServiceUser, p.ServiceGroup, p.StateDir)
		if err != nil {
			return err
		}
		if created {
			i.wrote(fmt.Sprintf("created the system user %s (no login shell, no home directory of its own)", p.ServiceUser))
		} else {
			i.ui.note(fmt.Sprintf("user %s already exists", p.ServiceUser))
		}
	} else if i.det.OS == "darwin" {
		i.ui.note("running as " + p.ServiceUser + "; macOS installs are per-user, which is what a development controller wants.")
	} else {
		i.ui.note("not running as root, so the service will run as " + p.ServiceUser + " and everything lives under your own directories.")
	}

	for _, dir := range []string{p.ConfigDir, p.StateDir, p.WorkDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("installer: creating %s: %w", dir, err)
		}
		// MkdirAll respects the umask, so the mode is set explicitly: 0750 is
		// what keeps the key file and the database off other local accounts.
		if err := os.Chmod(dir, 0o750); err != nil {
			return fmt.Errorf("installer: setting permissions on %s: %w", dir, err)
		}
		i.chown(p, dir)
	}
	i.wrote(fmt.Sprintf("%s and %s are mode 0750, owned by %s", p.ConfigDir, p.StateDir, p.ServiceUser))

	i.ensureSocketAccess(ctx, p)
	return nil
}

// ensureSocketAccess makes the container socket usable by the account the
// service will run as, and says plainly when it cannot.
//
// This is the failure the fleet reports later as "no host can take a new docker
// runner": the daemon is running, the socket is there, and the service account
// may not open it. It is cheap to settle here, while the installer is still
// root and the operator is still watching, and expensive to settle afterwards
// from a pool page.
func (i *Installer) ensureSocketAccess(ctx context.Context, p *Plan) {
	socket := SocketPathOf(p.DockerHost)
	if p.Backend == store.BackendProcess || socket == "" {
		return
	}
	facts, ok := statSocket(socket)
	if !ok {
		// No socket to inspect. The daemon may simply not be running yet, which
		// the agent re-probes for on every heartbeat, so this is a note rather
		// than a warning.
		i.ui.note("no socket at " + socket + " yet; the agent re-probes as it runs, so it will start taking work once the daemon is up.")
		return
	}
	// The unit and the container both need the socket's own group, which is not
	// always the group called "docker".
	p.DockerGID = facts.gid
	group := socketGroupName(facts.gid)

	acct, err := lookupAccount(p.ServiceUser)
	if err != nil {
		// No such account yet (a dry run, or a non-root install on a host where
		// the user is created later): say what will be needed rather than
		// guessing at what is true.
		i.ui.note(fmt.Sprintf("%s must be in the %s group to reach %s: usermod -aG %s %s", p.ServiceUser, group, socket, group, p.ServiceUser))
		p.DockerGroup = group
		return
	}
	if canOpen(facts, acct) {
		switch {
		case acct.uid == 0:
			// True, and not a recommendation: the config validator has its own
			// opinion about a service that runs as root.
			i.ui.ok(p.ServiceUser + " is root, so it can reach " + socket)
		case containsInt(acct.groups, facts.gid):
			// Record the group so the unit names it too, which is what keeps the
			// service working across a reboot.
			p.DockerGroup = group
			i.ui.ok(p.ServiceUser + " can reach " + socket + " through the " + group + " group")
		default:
			// A rootless socket the account owns, or a permissive mode. Naming
			// a group it does not need would be noise.
			i.ui.ok(p.ServiceUser + " can reach " + socket)
		}
		return
	}

	if !joinable(facts, acct) {
		// Group membership cannot help: the socket's group bits do not grant
		// read and write, so the only ways out are a different daemon or a
		// change to the socket itself.
		i.ui.warn(fmt.Sprintf("%s cannot use %s: it is owned by %s:%s with mode %04o, which grants nothing to its group",
			p.ServiceUser, socket, ownerName(facts.uid), group, facts.mode.Perm()))
		i.ui.note("run a rootless daemon and set agent.docker_host to its socket, or change the socket's own permissions; until then no runner can be created on this host.")
		return
	}

	if !i.det.Root {
		i.ui.warn(p.ServiceUser + " is not in the " + group + " group, so the service cannot open " + socket)
		i.ui.note(fmt.Sprintf("run: sudo usermod -aG %s %s   then restart the service, since a running process keeps the groups it started with.", group, p.ServiceUser))
		return
	}

	if err := addUserToGroup(ctx, p.ServiceUser, group); err != nil {
		i.ui.warn("could not add " + p.ServiceUser + " to the " + group + " group: " + err.Error())
		i.ui.note("without it the service cannot reach " + socket + "; add it by hand with: usermod -aG " + group + " " + p.ServiceUser)
		return
	}
	p.DockerGroup = group
	// Re-read the account rather than assuming: usermod can succeed against a
	// group the account still does not end up in, and the whole point of this
	// step is to stop the installer reporting a success it did not verify.
	if after, err := lookupAccount(p.ServiceUser); err != nil || !canOpen(facts, after) {
		i.ui.warn(p.ServiceUser + " was added to the " + group + " group but still cannot open " + socket)
		i.ui.note("check `ls -l " + socket + "` and the directories above it, or run a rootless daemon instead.")
		return
	}
	i.wrote(p.ServiceUser + " joined the " + group + " group and can now reach " + socket)
}

// ownerName is a uid as an operator would see it in `ls -l`.
func ownerName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u != nil && u.Username != "" {
		return u.Username
	}
	return strconv.Itoa(uid)
}

// chown gives a path to the service user when the installer is root. Failures
// are reported rather than fatal: an operator running as themselves has
// nothing to chown to.
func (i *Installer) chown(p *Plan, path string) {
	if !i.det.Root || i.det.OS == "darwin" {
		return
	}
	uid, gid, err := lookupUser(p.ServiceUser, p.ServiceGroup)
	if err != nil {
		return
	}
	if err := os.Chown(path, uid, gid); err != nil {
		i.log.Warn("could not change ownership", "path", path, "user", p.ServiceUser, "error", err)
	}
}

// stepKey generates or keeps the instance encryption key, and says plainly
// what losing it costs.
// stepKey loads or mints the instance key. The second return value says
// whether this run made it, which the summary needs: the backup instruction
// printed here scrolls off behind the GitHub handshake, the administrator and
// the service install, and it is the one thing in the whole setup that cannot
// be reconstructed afterwards.
func (i *Installer) stepKey(p Plan) (*cryptox.Key, bool, error) {
	i.ui.step("Encryption key")
	if exists(p.KeyFile) {
		key, err := cryptox.LoadKeyFile(p.KeyFile)
		if err != nil {
			return nil, false, fmt.Errorf("installer: the existing key at %s cannot be used: %w", p.KeyFile, err)
		}
		i.ui.ok("keeping the existing key at " + p.KeyFile)
		return key, false, nil
	}

	key, err := cryptox.GenerateKey()
	if err != nil {
		return nil, false, fmt.Errorf("installer: generating an encryption key: %w", err)
	}
	if err := cryptox.WriteKeyFile(p.KeyFile, key); err != nil {
		return nil, false, fmt.Errorf("installer: writing the encryption key to %s: %w", p.KeyFile, err)
	}
	i.chown(&p, p.KeyFile)
	i.wrote("wrote a new 32-byte key to " + p.KeyFile + " (mode 0600)")
	i.ui.warn("Back this key up now, somewhere that is not the same backup as the database.")
	i.ui.note("without it, the stored GitHub App private key and every webhook secret cannot be decrypted")
	i.ui.note("and must be entered again. Pools, runners, jobs and the audit log are not encrypted:")
	i.ui.note("the fleet's state survives, the credentials do not.")
	i.ui.note("copy it with:  sudo cat " + p.KeyFile)
	return key, true, nil
}

// stepConfig writes zoomies.yaml and prints every finding the configuration
// produces, so that a weakened default is named where it is chosen.
func (i *Installer) stepConfig(p Plan) (*config.Config, error) {
	i.ui.step("Configuration")
	cfg := p.Config()

	findings := cfg.Validate()
	if err := findings.Err(); err != nil {
		return nil, err
	}
	for _, f := range findings.Warnings() {
		i.ui.warn(f.Title)
		if f.Detail != "" {
			i.ui.note(f.Detail)
		}
		if f.Fix != "" {
			i.ui.note("fix: " + f.Fix)
		}
	}

	if exists(p.ConfigFile) {
		backup := p.ConfigFile + ".bak"
		if err := os.Rename(p.ConfigFile, backup); err != nil {
			return nil, fmt.Errorf("installer: keeping a copy of the old %s: %w", p.ConfigFile, err)
		}
		i.ui.note("the previous configuration is at " + backup)
	}
	if err := cfg.Save(p.ConfigFile); err != nil {
		return nil, fmt.Errorf("installer: writing %s: %w", p.ConfigFile, err)
	}
	i.chown(&p, p.ConfigFile)
	i.wrote("wrote " + p.ConfigFile)
	return cfg, nil
}

// resaveConfig rewrites zoomies.yaml after a later step changed the plan.
//
// It is deliberately quiet about the backup the first write already made: the
// operator has not touched the file in between, so a second ".bak" note would
// only be noise.
func (i *Installer) resaveConfig(p Plan) (*config.Config, error) {
	cfg := p.Config()
	if err := cfg.Validate().Err(); err != nil {
		return nil, err
	}
	if err := cfg.Save(p.ConfigFile); err != nil {
		return nil, fmt.Errorf("installer: rewriting %s with the new external URL: %w", p.ConfigFile, err)
	}
	i.chown(&p, p.ConfigFile)
	i.ui.ok("updated " + p.ConfigFile + " -- external URL is now " + p.ExternalURL)
	return cfg, nil
}

// stepAdmin creates the first administrator in the database that was just
// created. An existing administrator is left alone, which is what makes a
// re-run safe.
func (i *Installer) stepAdmin(ctx context.Context, st *store.Store, cfg *config.Config, p Plan) error {
	i.ui.step("Administrator account")
	svc := auth.New(st, cfg, nil, auth.WithLogger(i.log))

	needs, err := svc.NeedsBootstrap(ctx)
	if err != nil {
		return fmt.Errorf("installer: checking for existing accounts: %w", err)
	}
	if !needs {
		i.ui.note("an account already exists; leaving it alone. Reset a forgotten password with `zoomies users passwd`.")
		return nil
	}
	if p.adminPassword == "" {
		if !i.interactive {
			return errors.New("installer: this database has no accounts yet and no administrator password was given; " +
				"set admin.username and admin.password (or admin.password_file) in the answer file")
		}
		var err error
		if p, err = i.askAdminCredentials(ctx, p); err != nil {
			return err
		}
	}
	if _, err := svc.CreateFirstAdmin(ctx, p.AdminUser, p.adminPassword); err != nil {
		return fmt.Errorf("installer: creating the administrator %q: %w", p.AdminUser, err)
	}
	i.wrote("created the administrator " + p.AdminUser)
	return nil
}

// stepFirstPool creates the pool that makes the host Zoomies was just
// installed on usable.
//
// Without one, setup ends with a controller, a connected App, a healthy host --
// and nothing that can run a job. A pool is what a queued workflow_job is
// matched against, so until one exists every delivery is received and then
// dropped for want of anywhere to place a runner. The installer used to print
// the `zoomies pools create` line in the summary and stop there, which left the
// last step of setup outside setup.
//
// It is deliberately conservative: it only ever adds the first pool, on a
// single-host install, when GitHub is connected. Anything else is a fleet whose
// shape the operator has already decided, and re-running `zoomies init` must
// not touch it.
func (i *Installer) stepFirstPool(ctx context.Context, st *store.Store, cfg *config.Config, p *Plan) error {
	// Only the single-host mode puts an agent on this machine. A controller
	// with no embedded agent, or an agent joining someone else's controller,
	// has no runners here for a pool to describe.
	if p.Mode != ModeSingle {
		return nil
	}
	i.ui.step("First pool")

	if p.GitHub.Skip {
		i.ui.note("skipped: a pool belongs to a GitHub App installation, and none is connected yet.")
		i.ui.note("Connect GitHub, then create the pool on the Pools page.")
		return nil
	}
	pools, err := st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("installer: reading the existing pools: %w", err)
	}
	if len(pools) > 0 {
		i.ui.note(fmt.Sprintf("this fleet already has %d %s; leaving them alone.", len(pools), pluralise(len(pools), "pool")))
		return nil
	}
	insts, err := st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("installer: reading the installations: %w", err)
	}
	if len(insts) == 0 {
		i.ui.note("skipped: no GitHub App installation was recorded, and a pool has to belong to one.")
		return nil
	}

	sug := SuggestPool(i.det.OS, i.det.Arch, p.Backend, p.Capacity)
	if i.answers != nil {
		if i.answers.Pool.Skip {
			i.ui.note("skipped: pool.skip is set in the answer file.")
			return nil
		}
		sug.applyAnswers(i.answers.Pool)
	}

	create := true
	if i.interactive {
		if err := i.confirm(ctx, fmt.Sprintf("Create the %q pool now?", sug.Name),
			fmt.Sprintf("It runs at most %d %s on this host with the %s backend, and answers to "+
				"runs-on: %s. Answering no leaves the Pools page empty; nothing can run until "+
				"a pool exists.", sug.MaxRunners, pluralise(sug.MaxRunners, "runner"), sug.Backend, sug.RunsOn()),
			&create); err != nil {
			return err
		}
	}
	if !create {
		i.ui.note("skipped. Create one at " + p.ExternalURL + "/pools/new, or with:")
		i.ui.note("  " + sug.Command(insts[0].ID))
		return nil
	}

	pool := &store.Pool{
		Name:           sug.Name,
		InstallationID: insts[0].ID,
		Labels:         store.StringSlice(sug.Labels),
		Backend:        sug.Backend,
		Image:          cfg.GitHub.RunnerImage,
		MinRunners:     0,
		MaxRunners:     sug.MaxRunners,
		IdleTimeout:    store.Duration(5 * time.Minute),
		// Ephemeral, like every pool the API creates: a runner that takes one
		// job and is destroyed is the only arrangement that keeps one
		// workflow's leftovers out of the next one.
		Ephemeral:  true,
		DockerMode: store.DockerNone,
		Enabled:    true,
	}
	if err := st.CreatePool(ctx, pool); err != nil {
		return fmt.Errorf("installer: creating the %s pool: %w", sug.Name, err)
	}
	p.PoolName = pool.Name
	i.wrote(fmt.Sprintf("created the %q pool for %s, up to %d %s",
		pool.Name, insts[0].Target, pool.MaxRunners, pluralise(pool.MaxRunners, "runner")))
	i.ui.note("put  runs-on: " + sug.RunsOn() + "  in a workflow and it will run here.")
	return nil
}

// pluralise writes "1 runner" and "4 runners". The installer says these counts
// in several places and "1 runners" reads like a bug in everything around it.
func pluralise(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// stepService installs and starts the supervisor's unit, or prints what to run
// when there is no supervisor to install into.
func (i *Installer) stepService(ctx context.Context, p Plan) (ServiceManager, error) {
	i.ui.step("Service")

	switch p.Service {
	case ServiceCompose:
		i.ui.note("this host has no service manager Zoomies can install into, so here is a compose file.")
		i.ui.note("save it as docker-compose.yml and run `docker compose up -d`.")
		i.ui.note("or re-run with --deployment compose and setup will write it, and a populated .env, for you.")
		i.ui.blank()
		spec := ComposeSpec{
			ExternalURL: p.ExternalURL,
			Backend:     string(p.Backend),
			DockerHost:  p.DockerHost,
			Capacity:    p.Capacity,
			Embedded:    p.Embedded,
			DockerGID:   p.DockerGID,
		}
		if _, port, err := splitBind(p.Bind); err == nil {
			spec.Port = port
		}
		if err := RenderCompose(i.out, spec); err != nil {
			return nil, err
		}
		i.ui.blank()
		i.ui.note("put the encryption key in .env with:  echo ZOOMIES_ENCRYPTION_KEY=$(sudo cat " + p.KeyFile + ") >> .env")
		return nil, nil
	case ServiceNone:
		i.ui.note("no service installed. Start it yourself with:")
		i.ui.note("  " + i.det.BinaryPath + " controller --config " + p.ConfigFile)
		return nil, nil
	}

	mgr, err := NewServiceManager(p.Service, UnitController)
	if err != nil {
		return nil, err
	}
	spec := ServiceSpec{
		Unit:       UnitController,
		ExecPath:   i.det.BinaryPath,
		ConfigFile: p.ConfigFile,
		User:       p.ServiceUser,
		Group:      p.ServiceGroup,
		StateDir:   p.StateDir,
		ConfigDir:  p.ConfigDir,
		Bind:       p.Bind,
		// A Docker- or Podman-backed embedded agent must not start before the
		// daemon it needs, or its first probe fails for no good reason.
		WantsDocker: p.Embedded && p.Backend != store.BackendProcess,
		RuntimeName: string(p.Backend),
		Home:        p.StateDir,
	}
	if p.DockerGroup != "" {
		spec.SupplementaryGroups = []string{p.DockerGroup}
	}

	path, err := mgr.Install(ctx, spec)
	if err != nil {
		return nil, err
	}
	i.wrote("installed " + path)

	if p.EnableService {
		if err := mgr.Enable(ctx); err != nil {
			return mgr, fmt.Errorf("installer: enabling the service: %w", err)
		}
		i.ui.ok("enabled at boot")
	}
	if p.StartService {
		if err := mgr.Start(ctx); err != nil {
			out, _ := mgr.Logs(ctx, 20)
			if out != "" {
				i.ui.blank()
				i.ui.note(out)
			}
			return mgr, fmt.Errorf("installer: starting the service: %w (see %s)", err, mgr.LogCommand())
		}
		i.ui.ok("started")
	}
	return mgr, nil
}

// stepHealth waits for the controller to answer, and turns a failure into the
// two things an operator needs: the last of the log, and the command that
// shows the rest.
func (i *Installer) stepHealth(ctx context.Context, p Plan, mgr ServiceManager) {
	i.ui.step("Health check")
	target := localHealthURL(p)

	if err := waitHealthy(ctx, healthClient(p), target, healthTimeout); err != nil {
		i.ui.warn("the controller did not answer " + target + " within " + healthTimeout.String())
		i.ui.note(err.Error())
		if mgr != nil {
			if out, logErr := mgr.Logs(ctx, 20); logErr == nil && out != "" {
				i.ui.blank()
				for _, line := range strings.Split(out, "\n") {
					i.ui.note(line)
				}
				i.ui.blank()
			}
			i.ui.note("see more with: " + mgr.LogCommand())
		}
		return
	}
	i.ui.ok("the controller is answering on " + target)
}

// healthTimeout is how long the installer waits before showing the log. Thirty
// seconds is long enough for migrations on a slow disk and short enough that a
// wedged start is not mistaken for a slow one.
const healthTimeout = 30 * time.Second

// localHealthURL points at the listener over loopback rather than at the
// external URL, because a reverse proxy or a DNS record may not exist yet and
// the question here is only whether the process came up.
func localHealthURL(p Plan) string {
	_, port, err := splitBind(p.Bind)
	if err != nil {
		port = 8080
	}
	scheme := "http"
	if p.TLSMode != config.TLSOff {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d/healthz", scheme, port)
}

// healthClient dials the controller's own listener. Certificate verification
// is off for exactly this check: the certificate may be the self-signed one
// generated moments ago, and the connection never leaves the loopback
// interface, so there is nothing on the path to impersonate it.
func healthClient(p Plan) *http.Client {
	c := &http.Client{Timeout: 5 * time.Second}
	if p.TLSMode != config.TLSOff {
		c.Transport = insecureLoopbackTransport()
	}
	return c
}

// waitHealthy polls until the endpoint answers, the context ends, or the
// timeout passes.
func waitHealthy(ctx context.Context, client *http.Client, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("%s answered HTTP %d", target, resp.StatusCode)
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			if last == nil {
				last = errors.New("no response")
			}
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// stepSummary is the last thing the operator reads. It answers four questions
// in order: where do I go, how do I get in, what do I have to keep, and what
// do I do next -- and the "what next" is a numbered list of the steps that are
// genuinely still outstanding, not a list of everything setup can do.
//
// Values are rendered with `field` rather than `note`, because `note` is faint
// and this block is the most important output of the whole install.
func (i *Installer) stepSummary(p Plan, freshKey bool) {
	i.ui.blank()
	i.ui.step("Done")
	i.ui.field("URL", p.ExternalURL)
	i.ui.field("login", p.AdminUser)
	i.ui.field("config", p.ConfigFile)
	i.ui.field("key", p.KeyFile)
	i.ui.field("logs", logHint(p))
	i.ui.blank()

	if freshKey {
		// Repeated on purpose. The instruction was printed thirty to fifty
		// lines ago, behind a browser handshake and a health check, and it is
		// the only thing here that cannot be recreated.
		i.ui.warn("Back up " + p.KeyFile + " now, separately from the database.")
		i.ui.note("without it the stored GitHub App private key and every webhook secret are lost.")
		i.ui.note("copy it with:  sudo cat " + p.KeyFile)
		i.ui.blank()
	}

	if p.Listen == ListenLoopback {
		i.ui.note("The listener is on loopback, so reach it from your laptop with:")
		_, port, _ := splitBind(p.Bind)
		i.ui.note(fmt.Sprintf("  ssh -L %d:127.0.0.1:%d %s", port, port, i.det.Hostname))
		i.ui.blank()
	}

	// What is genuinely left. An empty list is the good case, and saying so
	// beats printing instructions for work that is already done.
	i.ui.step("Next")
	n := 0
	next := func(what, where string) {
		n++
		i.ui.field(fmt.Sprintf("  %d.", n), what)
		if where != "" {
			i.ui.field("", where)
		}
	}
	if p.GitHub.Skip {
		next("Connect GitHub -- nothing can run until an App is installed",
			p.ExternalURL+"/installations")
	}
	if p.PoolName == "" {
		sug := SuggestPool(i.det.OS, i.det.Arch, p.Backend, p.Capacity)
		next("Create a pool -- it decides what labels your runners answer to",
			p.ExternalURL+"/pools/new")
		i.ui.field("", "suggested for this "+i.det.Arch+" host: "+sug.Name+
			", up to "+strconv.Itoa(sug.MaxRunners))
	}
	runsOn := ""
	if p.PoolName != "" {
		runsOn = store.BrandedLabel(p.PoolName)
	} else {
		runsOn = SuggestPool(i.det.OS, i.det.Arch, p.Backend, p.Capacity).RunsOn()
	}
	next("Point a workflow at it", "runs-on: "+runsOn)
	if n == 1 {
		// Only the workflow line: GitHub is connected and a pool exists.
		i.ui.blank()
		i.ui.ok("This host is ready -- the " + p.PoolName + " pool runs on it.")
	}
}

func logHint(p Plan) string {
	switch p.Service {
	case ServiceSystemd:
		return "journalctl -u zoomies -f"
	case ServiceLaunchd:
		return "tail -f " + filepath.Join(p.StateDir, "zoomies.log")
	default:
		return "wherever you send the process's output"
	}
}

// ---------------------------------------------------------------------------
// Host plumbing
// ---------------------------------------------------------------------------

// ensureServiceUser creates the dedicated system account when it is missing.
// It supports both the shadow-utils tools and BusyBox's, because Alpine is one
// of the four distributions this has to work on out of the box.
func ensureServiceUser(ctx context.Context, name, group, home string) (bool, error) {
	if _, err := user.Lookup(name); err == nil {
		return false, nil
	}
	if _, err := user.LookupGroup(group); err != nil {
		switch {
		case lookPath("groupadd") != "":
			if _, err := runCommand(ctx, "groupadd", "--system", group); err != nil {
				return false, err
			}
		case lookPath("addgroup") != "":
			if _, err := runCommand(ctx, "addgroup", "-S", group); err != nil {
				return false, err
			}
		}
	}
	switch {
	case lookPath("useradd") != "":
		_, err := runCommand(ctx, "useradd", "--system", "--gid", group, "--home-dir", home,
			"--no-create-home", "--shell", "/usr/sbin/nologin", name)
		if err != nil {
			return false, fmt.Errorf("installer: creating the %s user: %w", name, err)
		}
	case lookPath("adduser") != "":
		// BusyBox adduser: -S system, -D no password, -H no home directory.
		_, err := runCommand(ctx, "adduser", "-S", "-D", "-H", "-h", home, "-s", "/sbin/nologin", "-G", group, name)
		if err != nil {
			return false, fmt.Errorf("installer: creating the %s user: %w", name, err)
		}
	default:
		return false, fmt.Errorf("installer: no useradd or adduser on this host, so the %s account cannot be created; create it by hand and re-run: useradd --system --shell /usr/sbin/nologin %s", name, name)
	}
	return true, nil
}

// addUserToGroup is how a service reaches a root-owned Docker socket without
// being root itself.
func addUserToGroup(ctx context.Context, name, group string) error {
	switch {
	case lookPath("usermod") != "":
		_, err := runCommand(ctx, "usermod", "-aG", group, name)
		return err
	case lookPath("addgroup") != "":
		_, err := runCommand(ctx, "addgroup", name, group)
		return err
	default:
		return fmt.Errorf("no usermod or addgroup on this host; add %s to %s by hand", name, group)
	}
}

func lookupUser(name, group string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, _ := strconv.Atoi(u.Gid)
	if group != "" && group != name {
		if g, err := user.LookupGroup(group); err == nil {
			if parsed, err := strconv.Atoi(g.Gid); err == nil {
				gid = parsed
			}
		}
	}
	return uid, gid, nil
}

// openBrowser asks the desktop to open a URL. It is best effort by design:
// every caller also prints the URL, because half of these hosts have no
// desktop at all.
func openBrowser(ctx context.Context, target string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	default:
		cmd = "xdg-open"
	}
	if lookPath(cmd) == "" {
		return fmt.Errorf("installer: %s is not on this host, so the browser cannot be opened from here", cmd)
	}
	args = append(args, target)
	return exec.CommandContext(ctx, cmd, args...).Start()
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

// ui writes the installer's output. The shapes -- "->", "ok", "!!" and an
// indented note -- are install.sh's, so that the shell half and the Go half of
// one installation read as one program.
type ui struct {
	w      io.Writer
	step_  lipgloss.Style
	okS    lipgloss.Style
	warnS  lipgloss.Style
	noteS  lipgloss.Style
	titleS lipgloss.Style
	// total is how many steps this run will print, and n how many it has.
	// Together they answer the question an operator asks halfway through a
	// five-minute browser handshake: is it nearly done, or have I just
	// started? Zero means the run does not know, and no counter is shown.
	total int
	n     int
}

func newUI(w io.Writer) *ui {
	r := lipgloss.NewRenderer(w)
	return &ui{
		w: w,
		// Runner Blue, the same accent the web UI uses. lipgloss down-samples
		// it for 256- and 16-colour terminals, so a truecolour terminal gets
		// the brand colour and a limited one degrades cleanly.
		step_:  r.NewStyle().Foreground(lipgloss.Color("#2F80ED")).Bold(true),
		okS:    r.NewStyle().Foreground(lipgloss.Color("42")),
		warnS:  r.NewStyle().Foreground(lipgloss.Color("214")),
		noteS:  r.NewStyle().Faint(true),
		titleS: r.NewStyle().Bold(true),
	}
}

func (u *ui) step(msg string) {
	if u.total > 0 {
		u.n++
		fmt.Fprintf(u.w, "%s %s\n", u.step_.Render("->"),
			u.titleS.Render(fmt.Sprintf("[%d/%d] %s", u.n, u.total, msg)))
		return
	}
	fmt.Fprintf(u.w, "%s %s\n", u.step_.Render("->"), u.titleS.Render(msg))
}

func (u *ui) ok(msg string) {
	fmt.Fprintf(u.w, "%s %s\n", u.okS.Render("   ok"), msg)
}

func (u *ui) warn(msg string) {
	fmt.Fprintf(u.w, "%s %s\n", u.warnS.Render("   !!"), msg)
}

func (u *ui) note(msg string) {
	fmt.Fprintf(u.w, "      %s\n", u.noteS.Render(msg))
}

// field is a key and its value in the installer's aligned two-column blocks --
// "Checking this host", the plan review, the final summary. It is deliberately
// not faint: install.sh's report uses the same twelve-column key, and the most
// important output of the whole install should not be the dimmest.
func (u *ui) field(key, value string) {
	fmt.Fprintf(u.w, "      %-12s%s\n", key, value)
}

func (u *ui) blank() { fmt.Fprintln(u.w) }

// ---------------------------------------------------------------------------
// Prompt helpers
// ---------------------------------------------------------------------------

func (i *Installer) runForm(ctx context.Context, f *huh.Form) error {
	err := f.RunWithContext(ctx)
	switch {
	case errors.Is(err, huh.ErrUserAborted):
		if len(i.written) > 0 {
			return &AbortedError{Written: i.Written()}
		}
		return ErrAborted
	case err != nil:
		return err
	}
	return nil
}

// AbortedError carries what a cancelled run had already done, so the CLI can
// list it rather than saying "Nothing was changed" over the top of a system
// account, an encryption key and a live GitHub App.
type AbortedError struct{ Written []string }

func (e *AbortedError) Error() string {
	return fmt.Sprintf("installer: setup cancelled after %d %s to this host",
		len(e.Written), pluralise(len(e.Written), "change"))
}

func (e *AbortedError) Is(target error) bool {
	return target == ErrAborted || target == ErrAbortedDirty
}

func (i *Installer) selectOne(ctx context.Context, title, description string, opts []huh.Option[string], value *string) error {
	sel := huh.NewSelect[string]().Title(title).Description(description).Options(opts...).Value(value)
	return i.runForm(ctx, huh.NewForm(huh.NewGroup(sel)))
}

func (i *Installer) input(ctx context.Context, title, description, placeholder string, value *string, validate func(string) error) error {
	in := huh.NewInput().Title(title).Description(description).Value(value)
	if placeholder != "" {
		in = in.Placeholder(placeholder)
	}
	if validate != nil {
		in = in.Validate(validate)
	}
	return i.runForm(ctx, huh.NewForm(huh.NewGroup(in)))
}

func (i *Installer) confirm(ctx context.Context, title, description string, value *bool) error {
	c := huh.NewConfirm().Title(title).Description(description).Value(value)
	return i.runForm(ctx, huh.NewForm(huh.NewGroup(c)))
}
